package helm

import (
	"context"
	"reflect"
	"testing"

	"helm.sh/helm/v3/pkg/release"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func k3sObj(kind, ns, name string, spec map[string]any) *unstructured.Unstructured {
	obj := map[string]any{"apiVersion": "helm.cattle.io/v1", "kind": kind, "metadata": map[string]any{"namespace": ns, "name": name}}
	if spec != nil {
		obj["spec"] = spec
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestK3sHelmChartKeys(t *testing.T) {
	got := k3sHelmChartKeys(
		[]*unstructured.Unstructured{
			k3sObj("HelmChart", "kube-system", "traefik", map[string]any{"chart": "traefik"}),
			k3sObj("HelmChart", "kube-system", "app", map[string]any{"targetNamespace": "apps"}),
		},
		[]*unstructured.Unstructured{k3sObj("HelmChartConfig", "kube-system", "traefik", nil)},
	)
	want := map[string]k3sOwner{
		"kube-system/traefik": {chart: "kube-system/traefik", config: "kube-system/traefik"},
		"apps/app":            {chart: "kube-system/app"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %+v, want %+v", got, want)
	}
}

func TestApplyK3sOwnershipYieldsToFlux(t *testing.T) {
	m := map[string]k3sOwner{"apps/app": {chart: "kube-system/app"}}
	if got := applyK3sOwnership("app", "apps", m, false); got.chart != "kube-system/app" {
		t.Fatalf("owner = %+v", got)
	}
	if got := applyK3sOwnership("app", "apps", m, true); got != (k3sOwner{}) {
		t.Fatalf("Flux wins, got %+v", got)
	}
	if got := applyK3sOwnership("app", "kube-system", m, false); got != (k3sOwner{}) {
		t.Fatalf("a HelmChart targeting apps doesn't own the release stored in kube-system, got %+v", got)
	}
	if got := applyK3sOwnership("app", "apps", nil, false); got != (k3sOwner{}) {
		t.Fatalf("nil map, got %+v", got)
	}
}

func TestHelmReleaseRowsStampK3sOwnership(t *testing.T) {
	snapshot := func() *helmReleaseStorageSnapshot {
		return &helmReleaseStorageSnapshot{
			storageNamespaces: map[string]string{},
			histories:         map[string][]HelmRevision{},
			latest:            []*release.Release{helmTestRelease("traefik", "kube-system", 1, release.StatusDeployed, "Install complete")},
		}
	}
	k3s := map[string]k3sOwner{"kube-system/traefik": {chart: "kube-system/traefik", config: "kube-system/traefik"}}
	rows, err := helmReleaseRowsFromStorageSnapshot(context.Background(), snapshot(), nil, k3s, "", false)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, err %v", rows, err)
	}
	if rows[0].ManagedByK3sHelmChart != "kube-system/traefik" || rows[0].K3sHelmChartConfig != "kube-system/traefik" {
		t.Fatalf("row = %+v", rows[0])
	}
	rows, _ = helmReleaseRowsFromStorageSnapshot(context.Background(), snapshot(), map[string]string{"kube-system/traefik": "flux-system/traefik"}, k3s, "", false)
	if rows[0].ManagedByFluxHelmRelease != "flux-system/traefik" || rows[0].ManagedByK3sHelmChart != "" || rows[0].K3sHelmChartConfig != "" {
		t.Fatalf("Flux wins, row = %+v", rows[0])
	}
}
