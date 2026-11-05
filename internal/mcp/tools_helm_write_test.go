package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestManageHelmRejectsAmbiguousAndRedactedInput(t *testing.T) {
	base := manageHelmInput{Action: "install", Namespace: "team-a", Name: "cache", Chart: "redis", Repository: "https://charts.example", Version: "1.2.3"}
	if err := base.validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*manageHelmInput){
		func(x *manageHelmInput) { x.Namespace = "../other" },
		func(x *manageHelmInput) { x.Version = "" },
		func(x *manageHelmInput) { x.Version = ">=1.2.3" },
		func(x *manageHelmInput) { x.Action = "remove" },
		func(x *manageHelmInput) { x.Confirm = "true" },
		func(x *manageHelmInput) { x.Values = map[string]any{"auth": map[string]any{"password": "[REDACTED]"}} },
		func(x *manageHelmInput) { x.Action = "rollback"; x.Revision = 0 },
	} {
		in := base
		mutate(&in)
		if err := in.validate(); err == nil {
			t.Fatalf("accepted invalid input: %#v", in)
		}
	}
}

func TestManageHelmDeniesBeforeFetchingChart(t *testing.T) {
	hits := 0
	repo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; http.Error(w, "must not fetch", 500) }))
	defer repo.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/authorization.k8s.io/v1/subjectaccessreviews" {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var review authv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Error(err)
		}
		if review.Spec.User != "helm-mcp-denied" || review.Spec.ResourceAttributes.Namespace != "team-a" {
			t.Errorf("wrong caller/scope: %+v", review.Spec)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(authv1.SubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"}, Status: authv1.SubjectAccessReviewStatus{Allowed: false}})
	}))
	defer api.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL, QPS: -1, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	old := k8s.SetTestClient(client)
	defer k8s.SetTestClient(old)
	status := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	defer k8s.SetConnectionStatus(status)
	ctx := auth.ContextWithUser(context.Background(), &auth.User{Username: "helm-mcp-denied"})
	_, _, err = handleManageHelm(ctx, nil, manageHelmInput{Action: "install", Namespace: "team-a", Name: "cache", Chart: "redis", Repository: repo.URL, Version: "1.2.3"})
	if err == nil || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("want denied, got %v", err)
	}
	if hits != 0 {
		t.Fatalf("denied caller fetched repository %d times", hits)
	}
}
