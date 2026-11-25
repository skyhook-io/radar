package workloadrestart

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestApplyRequiresCheckAndMarkerPath(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	resource := client.Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}).Namespace("prod")
	review := Preconditions{UID: "uid"}
	for _, tc := range []struct {
		name  string
		check func(*unstructured.Unstructured) error
		path  []string
	}{
		{"missing check", nil, []string{"spec", "marker"}},
		{"missing path", func(*unstructured.Unstructured) error { return nil }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Apply(t.Context(), resource, "web", review, tc.check, time.Now(), time.RFC3339, tc.path...); err == nil || len(client.Actions()) != 0 {
				t.Fatalf("error = %v, Kubernetes requests = %v", err, client.Actions())
			}
		})
	}
}

func TestApplyReadsPreviousMarkerWithItsLayout(t *testing.T) {
	const previous = "2026-06-10 12:00:00"
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"namespace": "prod", "name": "web", "uid": "uid", "resourceVersion": "1"},
		"spec":     map[string]any{"marker": previous},
	}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
	resource := client.Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}).Namespace("prod")
	check := func(*unstructured.Unstructured) error { return nil }
	at := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	got, err := Apply(t.Context(), resource, "web", Preconditions{UID: obj.GetUID()}, check, at, time.DateTime, "spec", "marker")
	if err != nil || got.Changed || got.RestartedAt != previous || len(client.Actions()) != 1 || client.Actions()[0].GetVerb() != "get" {
		t.Fatalf("result = %+v, error = %v, Kubernetes requests = %v", got, err, client.Actions())
	}
}
