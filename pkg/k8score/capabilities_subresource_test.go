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
