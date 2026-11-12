package helm

import (
	"context"
	"reflect"
	"testing"

	"github.com/skyhook-io/radar/internal/search"
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
	if c := rows[0].ManagedBy; c == nil || c.Tool != "k3s" || c.Ref() != "kube-system/traefik" || c.ValuesRef == nil || !c.ValuesRef.Exists {
		t.Fatalf("row = %+v", rows[0].ManagedBy)
	}
	rows, _ = helmReleaseRowsFromStorageSnapshot(context.Background(), snapshot(), map[string]string{"kube-system/traefik": "flux-system/traefik"}, k3s, "", false)
	if c := rows[0].ManagedBy; rows[0].ManagedByFluxHelmRelease != "flux-system/traefik" || c == nil || c.Tool != "flux" || c.Kind != "HelmRelease" || c.Ref() != "flux-system/traefik" {
		t.Fatalf("Flux wins, row = %+v", rows[0].ManagedBy)
	}
}

// Two HelmCharts that install the same release can't be told apart, so the
// release is credited to neither, as for two Flux HelmReleases.
func TestK3sHelmChartKeysCreditNeitherOfTwoCharts(t *testing.T) {
	got := k3sHelmChartKeys([]*unstructured.Unstructured{
		k3sObj("HelmChart", "kube-system", "app", map[string]any{"targetNamespace": "apps"}),
		k3sObj("HelmChart", "team", "app", map[string]any{"targetNamespace": "apps"}),
	}, nil)
	if _, ok := got["apps/app"]; ok {
		t.Fatalf("keys = %+v, want apps/app credited to neither", got)
	}
}

// The observed scan links a HelmChartConfig to its HelmChart whichever the
// caller's read returns first; only objects the scan saw count.
func TestObservedScanLinksK3sChartAndConfigInEitherOrder(t *testing.T) {
	chart := k3sObj("HelmChart", "kube-system", "app", map[string]any{"targetNamespace": "apps"})
	config := k3sObj("HelmChartConfig", "kube-system", "app", nil)
	for name, order := range map[string][]*unstructured.Unstructured{"chart first": {chart, config}, "config first": {config, chart}} {
		scan := &stampScan{seen: map[string]bool{}}
		for _, o := range order {
			scan.add(search.ScannedObject{Kind: o.GetKind(), Group: "helm.cattle.io", Object: o})
		}
		scan.finishK3s()
		if got := scan.k3s["apps/app"]; got != (k3sOwner{chart: "kube-system/app", config: "kube-system/app"}) {
			t.Errorf("%s: owner = %+v", name, got)
		}
	}
	unread := &stampScan{seen: map[string]bool{}}
	unread.add(search.ScannedObject{Kind: "HelmChart", Group: "helm.cattle.io", Object: chart})
	unread.finishK3s()
	if got := unread.k3s["apps/app"]; got.config != "" {
		t.Errorf("a HelmChartConfig the scan didn't see is not linked: %+v", got)
	}
}

func TestReleaseControllerPicksOneOwner(t *testing.T) {
	if c := releaseController("", k3sOwner{}); c != nil {
		t.Fatalf("no owner, got %+v", c)
	}
	c := releaseController("", k3sOwner{chart: "kube-system/app"})
	want := &ReleaseController{Tool: "k3s", Kind: "HelmChart", Group: "helm.cattle.io", Namespace: "kube-system", Name: "app",
		ValuesRef: &ControllerValuesRef{Kind: "HelmChartConfig", Group: "helm.cattle.io", Namespace: "kube-system", Name: "app"}}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("k3s without a config = %+v / %+v", c, c.ValuesRef)
	}
	if c := releaseController("", k3sOwner{chart: "kube-system/app", config: "kube-system/app"}); !c.ValuesRef.Exists {
		t.Fatalf("config read, got %+v", c.ValuesRef)
	}
	if c := releaseController("flux-system/app", k3sOwner{chart: "kube-system/app"}); c.Tool != "flux" || c.ValuesRef != nil {
		t.Fatalf("Flux wins, got %+v", c)
	}
}
