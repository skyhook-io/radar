package audit

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestLocalArgoAppNamesSkipsRemoteDestinations(t *testing.T) {
	app := func(name string, dest map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"namespace": "argocd", "name": name},
			"spec":     map[string]any{"destination": dest},
		}}
	}
	got := localArgoAppNames([]*unstructured.Unstructured{
		app("web", map[string]any{"server": "https://kubernetes.default.svc"}),
		app("api", map[string]any{"name": "prod-spoke"}),
	})
	if _, ok := got["web"]; !ok {
		t.Error("in-cluster Application web missing")
	}
	if _, ok := got["api"]; ok {
		t.Error("remote Application api must not vouch for a local instance label")
	}
}
