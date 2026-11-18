package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/auth"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestLonghornEvidenceRequiresInstanceManagerSAR(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "allowed"}[allowed], func(t *testing.T) {
			getPermCache().Set("storage-reader", nil, &auth.UserPermissions{AllowedNamespaces: []string{"storage"}})
			t.Cleanup(getPermCache().Invalidate)
			var reviews []authv1.ResourceAttributes
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/apis/authorization.k8s.io/v1/subjectaccessreviews" {
					http.NotFound(w, r)
					return
				}
				var review authv1.SubjectAccessReview
				if err := json.NewDecoder(r.Body).Decode(&review); err != nil || review.Spec.ResourceAttributes == nil {
					t.Error("invalid SubjectAccessReview")
					http.Error(w, "invalid review", http.StatusBadRequest)
					return
				}
				attrs := *review.Spec.ResourceAttributes
				reviews = append(reviews, attrs)
				review.Status.Allowed = allowed && attrs.Group == "longhorn.io" && attrs.Resource == "instancemanagers" && attrs.Namespace == "storage" && attrs.Verb == "list"
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(review); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(apiServer.Close)
			client, err := kubernetes.NewForConfig(&rest.Config{Host: apiServer.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}, QPS: -1})
			if err != nil {
				t.Fatal(err)
			}
			previous := k8s.SetTestClient(client)
			t.Cleanup(func() { k8s.SetTestClient(previous) })
			ctx := auth.ContextWithUser(context.Background(), &auth.User{Username: "storage-reader"})
			access := issueEvidenceAccess(ctx)
			read := issues.EvidenceRead{Group: "longhorn.io", Resource: "instancemanagers", Namespace: "storage", Verb: "list"}
			if got := access(read); got != allowed {
				t.Fatalf("InstanceManager evidence allowed=%v, want %v", got, allowed)
			}
			if len(reviews) != 1 || reviews[0] != (authv1.ResourceAttributes{Group: "longhorn.io", Resource: "instancemanagers", Namespace: "storage", Verb: "list"}) {
				t.Fatalf("expected exact InstanceManager SAR, got %+v", reviews)
			}
			read.Namespace = "other"
			if access(read) || len(reviews) != 1 {
				t.Fatal("namespace gate must deny unrelated inventory before SAR")
			}
		})
	}
}
