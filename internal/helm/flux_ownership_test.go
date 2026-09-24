package helm

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFluxHelmReleaseKeysIgnoresRemoteReleases(t *testing.T) {
	hr := func(name string, spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"namespace": "flux-system", "name": name},
			"spec":     spec,
		}}
	}
	got := fluxHelmReleaseKeys([]*unstructured.Unstructured{
		hr("local", map[string]any{"releaseName": "web", "storageNamespace": "apps"}),
		hr("prod", map[string]any{
			"releaseName":      "api",
			"storageNamespace": "apps",
			"kubeConfig":       map[string]any{"secretRef": map[string]any{"name": "prod-kubeconfig"}},
		}),
	})
	if got["apps/web"] != "flux-system/local" {
		t.Errorf("local release owner = %q, want flux-system/local", got["apps/web"])
	}
	if owner, ok := got["apps/api"]; ok {
		t.Errorf("remote HelmRelease claimed local release apps/api as %q", owner)
	}
}
