package resourcecontext

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestChildrenContextPreservesCanonicalReferenceAccess(t *testing.T) {
	input := &ChildrenContext{Items: []ContextRef{{Kind: "Deployment", Group: "apps", Namespace: "app", Name: "private"}, {Kind: "ConfigMap", Namespace: "app", Name: "public"}}, Complete: true}
	got := Build(context.Background(), &corev1.Pod{}, Options{Children: input, AccessChecker: denyChecker{group: "apps", kind: "Deployment", namespace: "app"}})
	if got.Children == nil || len(got.Children.Items) != 1 || got.Children.Items[0].Name != "public" || !got.Children.Narrowed {
		t.Fatalf("unreadable child leaked: %+v", got.Children)
	}
	if len(input.Items) != 2 || input.Narrowed {
		t.Fatal("canonical projection mutated the host observation")
	}
}
