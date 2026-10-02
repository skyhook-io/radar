package helm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

// With a signed-in caller the Helm write gate asks about the caller, in the
// release's namespace, not about Radar's ServiceAccount: an IdP group bound to
// edit in one namespace can manage releases there without rbac.helm=true.
func TestRequireHelmWrite_ChecksCallerInNamespace(t *testing.T) {
	var reviews atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/apis/authorization.k8s.io/v1/subjectaccessreviews" {
			writeK8sStatus(t, w, http.StatusNotFound, "NotFound", "unexpected test request")
			return
		}
		reviews.Add(1)
		var review authv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Fatalf("decode subject access review: %v", err)
		}
		a := review.Spec.ResourceAttributes
		allowed := a != nil && a.Namespace == "payments" && a.Resource == "secrets" && a.Verb == "create" &&
			slices.Contains(review.Spec.Groups, "radar:idp:platform-eng")
		w.Header().Set("Content-Type", "application/json")
		writeTestJSON(t, w, authv1.SubjectAccessReview{
			TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"},
			Status:   authv1.SubjectAccessReviewStatus{Allowed: allowed},
		})
	}))
	t.Cleanup(srv.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatalf("create test client: %v", err)
	}
	prev := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(prev) })
	prevStatus := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prevStatus) })

	cases := []struct {
		name      string
		user      string
		groups    []string
		namespace string
		want      int
	}{
		{"group bound in the namespace", "helm-gate-dana", []string{"radar:viewer", "radar:idp:platform-eng"}, "payments", http.StatusOK},
		{"same group, another namespace", "helm-gate-dana", []string{"radar:viewer", "radar:idp:platform-eng"}, "billing", http.StatusForbidden},
		{"hub role alone grants nothing here", "helm-gate-eli", []string{"radar:owner"}, "payments", http.StatusForbidden},
		{"pod-local change needs no cluster permission", "helm-gate-fay", []string{"radar:viewer"}, "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/helm/releases", nil)
			req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: tc.user, Groups: tc.groups}))
			rec := httptest.NewRecorder()
			ok := requireHelmWrite(rec, req, tc.namespace)
			if got := map[bool]int{true: http.StatusOK, false: rec.Code}[ok]; got != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", got, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusForbidden && strings.Contains(rec.Body.String(), "rbac.helm") {
				t.Errorf("a caller's own RBAC denial must not point at rbac.helm: %s", rec.Body.String())
			}
		})
	}
	if reviews.Load() == 0 {
		t.Fatal("no SubjectAccessReview was made for the caller")
	}

	// Preview can fetch a chart version from a repository, so it is gated like
	// the write it previews.
	t.Run("preview values in a namespace the caller can't write", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/helm/releases/billing/web/preview-values", strings.NewReader(`{"values":{},"version":"1.2.3","repository":"https://charts.example"}`))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("namespace", "billing")
		rctx.URLParams.Add("name", "web")
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		req = req.WithContext(auth.ContextWithUser(ctx, &auth.User{Username: "helm-gate-dana", Groups: []string{"radar:viewer", "radar:idp:platform-eng"}}))
		rec := httptest.NewRecorder()
		NewHandlers(nil).handlePreviewValues(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 before any chart fetch (body %s)", rec.Code, rec.Body.String())
		}
	})
}
