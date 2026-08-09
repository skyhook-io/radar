package main

import (
	"context"
	"errors"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCanSelfUpgradeProbesEveryOperationTheEndpointPerforms(t *testing.T) {
	kc := fake.NewSimpleClientset()
	var seen []authv1.ResourceAttributes
	kc.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		seen = append(seen, *review.Spec.ResourceAttributes)
		return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})

	if !canSelfUpgrade(context.Background(), kc, "radar", "radar") {
		t.Fatal("all probes allowed but capability reported unavailable")
	}

	// The Deployment reviews must be named — rbac.selfUpgrade's Role is
	// resourceNames-scoped, so an unnamed review answers no on correctly
	// configured clusters — and the Secret read must be probed too, because a
	// BYO-RBAC install can pass a patch-only probe and still fail mid-upgrade.
	want := []authv1.ResourceAttributes{
		{Namespace: "radar", Group: "apps", Resource: "deployments", Name: "radar", Verb: "get"},
		{Namespace: "radar", Group: "apps", Resource: "deployments", Name: "radar", Verb: "patch"},
		{Namespace: "radar", Resource: "secrets", Verb: "list"},
	}
	if len(seen) != len(want) {
		t.Fatalf("probes = %+v, want %d probes", seen, len(want))
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("probe %d = %+v, want %+v", i, seen[i], want[i])
		}
	}
}

func TestCanSelfUpgradeFailsClosed(t *testing.T) {
	t.Run("any denied probe", func(t *testing.T) {
		kc := fake.NewSimpleClientset()
		kc.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
			review := action.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
			allowed := review.Spec.ResourceAttributes.Verb != "patch"
			return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
		})
		if canSelfUpgrade(context.Background(), kc, "radar", "radar") {
			t.Fatal("patch denied but capability reported available")
		}
	})

	t.Run("probe error", func(t *testing.T) {
		kc := fake.NewSimpleClientset()
		kc.PrependReactor("create", "selfsubjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("apiserver unavailable")
		})
		if canSelfUpgrade(context.Background(), kc, "radar", "radar") {
			t.Fatal("probe error but capability reported available")
		}
	})

	t.Run("missing identity or client", func(t *testing.T) {
		ctx := context.Background()
		if canSelfUpgrade(ctx, fake.NewSimpleClientset(), "", "radar") ||
			canSelfUpgrade(ctx, fake.NewSimpleClientset(), "radar", "") ||
			canSelfUpgrade(ctx, nil, "radar", "radar") {
			t.Fatal("capability advertised without identity or client")
		}
	})
}
