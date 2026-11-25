package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stest "k8s.io/client-go/testing"
)

func TestNamedPodDebugAccess(t *testing.T) {
	for _, tc := range []struct {
		name         string
		exec, update authv1.SubjectAccessReviewStatus
		updateErr    error
		want         string
		checks       int
	}{
		{"allowed", authv1.SubjectAccessReviewStatus{Allowed: true}, authv1.SubjectAccessReviewStatus{Allowed: true}, nil, "allowed", 2},
		{"denied", authv1.SubjectAccessReviewStatus{Allowed: true}, authv1.SubjectAccessReviewStatus{}, nil, "denied", 2},
		{"partial", authv1.SubjectAccessReviewStatus{Allowed: true}, authv1.SubjectAccessReviewStatus{EvaluationError: "timeout"}, nil, "unknown", 2},
		{"explicit deny", authv1.SubjectAccessReviewStatus{Allowed: true}, authv1.SubjectAccessReviewStatus{Denied: true, EvaluationError: "timeout"}, nil, "denied", 2},
		{"review failed", authv1.SubjectAccessReviewStatus{Allowed: true}, authv1.SubjectAccessReviewStatus{}, errors.New("offline"), "unknown", 2},
		{"exec denied", authv1.SubjectAccessReviewStatus{}, authv1.SubjectAccessReviewStatus{}, nil, "unavailable", 1},
		{"exec unresolved", authv1.SubjectAccessReviewStatus{EvaluationError: "timeout"}, authv1.SubjectAccessReviewStatus{}, nil, "unknown", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			var reviews []authv1.ResourceAttributes
			client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stest.Action) (bool, runtime.Object, error) {
				review := action.(k8stest.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
				attrs := *review.Spec.ResourceAttributes
				reviews = append(reviews, attrs)
				review.Status = tc.exec
				if attrs.Subresource == "ephemeralcontainers" {
					review.Status = tc.update
					return true, review, tc.updateErr
				}
				return true, review, nil
			})
			if got := reviewPodDebugAccess(context.Background(), client, "payments", "checkout", false); got.State != tc.want || got.Exec != tc.exec.Allowed {
				t.Fatalf("access=%+v, want state=%s, exec=%v", got, tc.want, tc.exec.Allowed)
			}
			want := []authv1.ResourceAttributes{{Namespace: "payments", Name: "checkout", Verb: "create", Resource: "pods", Subresource: "exec"}, {Namespace: "payments", Name: "checkout", Verb: "update", Resource: "pods", Subresource: "ephemeralcontainers"}}
			if !reflect.DeepEqual(reviews, want[:tc.checks]) {
				t.Fatalf("requests=%#v", reviews)
			}
		})
	}
}

func TestHostedPodDebugChecksOnlyExec(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stest.Action) (bool, runtime.Object, error) {
		review := action.(k8stest.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		if attrs := review.Spec.ResourceAttributes; attrs.Name != "api" || attrs.Subresource != "exec" {
			t.Fatalf("unexpected hosted review: %+v", attrs)
		}
		review.Status.Allowed = true
		return true, review, nil
	})
	if got := reviewPodDebugAccess(context.Background(), client, "demo", "api", true); got != (podDebugAccess{State: "unavailable", Exec: true}) {
		t.Fatalf("%+v", got)
	}
}

func TestPodDebugAccessScopeAndMode(t *testing.T) {
	for _, tc := range []struct {
		name, cloud   string
		pin, disabled bool
		want          int
	}{
		{"shared unauthenticated, disabled exec", "false", false, true, http.StatusOK},

		{"outside namespace pin", "false", true, false, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RADAR_CLOUD_MODE", tc.cloud)
			oldPin, oldDisabled := k8s.ForceNamespaceScope, k8s.ForceDisableExec
			k8s.ForceNamespaceScope, k8s.ForceDisableExec = tc.pin, tc.disabled
			t.Cleanup(func() { k8s.ForceNamespaceScope, k8s.ForceDisableExec = oldPin, oldDisabled })
			if tc.pin {
				k8s.SetFallbackNamespace("allowed")
				t.Cleanup(func() { k8s.SetFallbackNamespace("") })
			}
			router := chi.NewRouter()
			router.Get("/pods/{namespace}/{name}/debug-access", (&Server{listenAddress: "0.0.0.0"}).handlePodDebugAccess)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pods/payments/api/debug-access", nil))
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.want == http.StatusOK {
				var result podDebugAccess
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result != (podDebugAccess{State: "unavailable"}) {
					t.Fatalf("%+v %v", result, err)
				}
			}
		})
	}
}
