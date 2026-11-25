package k8score

import (
	"context"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCanISendsSubresourceInItsOwnField(t *testing.T) {
	client := fake.NewSimpleClientset()
	var got authv1.ResourceAttributes
	client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		got = *review.Spec.ResourceAttributes
		review.Status.Allowed = true
		return true, review, nil
	})

	if allowed, apiErr := CanI(context.Background(), client, "db", "", "pods/proxy", "get"); !allowed || apiErr {
		t.Fatalf("allowed=%v apiErr=%v", allowed, apiErr)
	}
	if got.Resource != "pods" || got.Subresource != "proxy" {
		t.Errorf("review = resource %q subresource %q, want pods / proxy", got.Resource, got.Subresource)
	}

	CanI(context.Background(), client, "db", "", "secrets", "list")
	if got.Resource != "secrets" || got.Subresource != "" {
		t.Errorf("plain resource = %q / %q", got.Resource, got.Subresource)
	}
}

func TestDebugPermissionUnknown(t *testing.T) {
	debug := authv1.ResourceAttributes{Namespace: "ops", Verb: "update", Resource: "pods", Subresource: "ephemeralcontainers"}
	exec := authv1.ResourceAttributes{Namespace: "ops", Verb: "create", Resource: "pods", Subresource: "exec"}
	for _, tc := range []struct {
		name    string
		attrs   authv1.ResourceAttributes
		status  authv1.SubjectAccessReviewStatus
		unknown bool
	}{
		{"partial debug", debug, authv1.SubjectAccessReviewStatus{EvaluationError: "webhook timeout"}, true},
		{"explicit deny", debug, authv1.SubjectAccessReviewStatus{Denied: true, EvaluationError: "another webhook timed out"}, false},
		{"explicit allow", debug, authv1.SubjectAccessReviewStatus{Allowed: true, EvaluationError: "another webhook timed out"}, false},
		{"no authorizer granted", debug, authv1.SubjectAccessReviewStatus{}, false},
		{"ordinary exec unchanged", exec, authv1.SubjectAccessReviewStatus{EvaluationError: "webhook timeout"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DebugPermissionUnknown(&tc.attrs, tc.status); got != tc.unknown {
				t.Fatalf("unknown=%v, want %v", got, tc.unknown)
			}
		})
	}
}
