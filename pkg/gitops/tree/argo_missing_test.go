package tree

import (
	"context"
	"errors"
	"testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestArgoMissingRequiresConfirmedLocalNotFound(t *testing.T) {
	for _, kind := range []string{"ConfigMap", "Secret"} {
		for _, tc := range []struct {
			name                                 string
			err                                  error
			remote, restricted, present, missing bool
		}{
			{name: "typed not found", err: apierrors.NewNotFound(schema.GroupResource{Resource: kind}, "values"), missing: true},
			{name: "dynamic not found", err: k8score.ErrResourceNotFound, missing: true},
			{name: "forbidden", err: apierrors.NewForbidden(schema.GroupResource{Resource: kind}, "values", errors.New("denied"))},
			{name: "unavailable", err: errors.New("cache unavailable")},
			{name: "not observed"},
			{name: "remote", err: k8score.ErrResourceNotFound, remote: true},
			{name: "namespace excluded", err: k8score.ErrResourceNotFound, restricted: true},
			{name: "present", present: true},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				ref := ResourceRef{Kind: kind, Namespace: "workloads", Name: "values"}
				server := "https://kubernetes.default.svc"
				if tc.remote {
					server = "https://remote.example"
				}
				app := &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
					"metadata": map[string]any{"name": "app", "namespace": "argocd"},
					"spec":     map[string]any{"destination": map[string]any{"server": server}},
					"status":   map[string]any{"resources": []any{map[string]any{"kind": kind, "namespace": "workloads", "name": "values", "status": "OutOfSync"}}},
				}}
				dynamic := &fakeDynamic{objects: map[string]*unstructured.Unstructured{
					refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "app"}): app,
				}, errs: map[string]error{refKey(ref): tc.err}}
				if tc.present {
					dynamic.objects[refKey(ref)] = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"namespace": "workloads", "name": "values", "uid": "live-uid"}}}
				}
				builder := NewBuilder(verifiedFakeDynamic{dynamic}, nil)
				if tc.restricted {
					builder.WithAllowedNamespaces([]string{"argocd"})
				}
				tree, _, err := builder.Build(context.Background(), "Application", "argocd", "app", "argoproj.io")
				if err != nil {
					t.Fatal(err)
				}
				node := findNode(t, tree, ref)
				if node.Missing != tc.missing {
					t.Fatalf("Missing = %v, want %v", node.Missing, tc.missing)
				}
				if node.Health == "Missing" {
					t.Fatal("local presence must not replace controller health")
				}
			})
		}
	}
}

type verifiedFakeDynamic struct{ *fakeDynamic }

func (f verifiedFakeDynamic) GetDynamicWithGroupVerified(ctx context.Context, kind, namespace, name, group string) (*unstructured.Unstructured, error) {
	return f.GetDynamicWithGroup(ctx, kind, namespace, name, group)
}

func TestUnverifiedGetterDoesNotConfirmAbsence(t *testing.T) {
	ref := ResourceRef{Kind: "ConfigMap", Namespace: "workloads", Name: "values"}
	b := NewBuilder(&fakeDynamic{errs: map[string]error{refKey(ref): k8score.ErrResourceNotFound}}, nil)
	_, _, missing := b.prefetchObjects(context.Background(), []ResourceRef{ref})
	if missing[refKey(ref)] {
		t.Fatal("plain cache NotFound must not confirm absence")
	}
}
