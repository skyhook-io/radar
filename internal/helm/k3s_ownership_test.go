package helm

import (
	"context"
	"encoding/json"
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
	got := k3sHelmChartKeys(true,
		[]*unstructured.Unstructured{
			k3sObj("HelmChart", "kube-system", "traefik", map[string]any{"chart": "traefik"}),
			k3sObj("HelmChart", "kube-system", "app", map[string]any{"targetNamespace": "apps"}),
		},
		[]*unstructured.Unstructured{k3sObj("HelmChartConfig", "kube-system", "traefik", nil)},
	)
	want := map[string]k3sOwner{
		"kube-system/traefik": {chart: "kube-system/traefik", config: "kube-system/traefik", configKnown: true},
		"apps/app":            {chart: "kube-system/app", configKnown: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %+v, want %+v", got, want)
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
	if c := rows[0].Controller; c == nil || c.Tool != "k3s" || c.Ref() != "kube-system/traefik" || c.ValuesRef == nil || c.ValuesRef.Exists == nil || !*c.ValuesRef.Exists {
		t.Fatalf("row = %+v", rows[0].Controller)
	}
	rows, _ = helmReleaseRowsFromStorageSnapshot(context.Background(), snapshot(), map[string]string{"kube-system/traefik": "flux-system/traefik"}, k3s, "", false)
	if c := rows[0].Controller; rows[0].ManagedByFluxHelmRelease != "flux-system/traefik" || c == nil || c.Tool != "flux" || c.Kind != "HelmRelease" || c.Ref() != "flux-system/traefik" {
		t.Fatalf("Flux wins, row = %+v", rows[0].Controller)
	}
}

// Two HelmCharts that install the same release can't be told apart, so the
// release is credited to neither, as for two Flux HelmReleases.
func TestK3sHelmChartKeysCreditNeitherOfTwoCharts(t *testing.T) {
	got := k3sHelmChartKeys(true, []*unstructured.Unstructured{
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
	if c := releaseController("", k3sOwner{chart: "kube-system/app", config: "kube-system/app", configKnown: true}); c.ValuesRef.Exists == nil || !*c.ValuesRef.Exists {
		t.Fatalf("config read, got %+v", c.ValuesRef)
	}
	if c := releaseController("flux-system/app", k3sOwner{chart: "kube-system/app"}); c.Tool != "flux" || c.ValuesRef != nil {
		t.Fatalf("Flux wins, got %+v", c)
	}
}

// Whether Radar's own Helm issues and changes defer to the controller is a
// property of the controller, so a new detector decides it in one place.
func TestReleaseControllerReportsOwnHistory(t *testing.T) {
	if !(&ReleaseController{Tool: "flux"}).ReportsOwnHistory() {
		t.Error("Flux records its own failures and revisions")
	}
	if (&ReleaseController{Tool: "k3s"}).ReportsOwnHistory() || (*ReleaseController)(nil).ReportsOwnHistory() {
		t.Error("k3s and no controller keep nothing of their own")
	}
}

// The Diagnose card and MCP clients read these keys; renaming a tag breaks them.
func TestReleaseControllerWireKeys(t *testing.T) {
	raw, err := json.Marshal(releaseController("", k3sOwner{chart: "kube-system/app", config: "kube-system/app", configKnown: true}))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"tool":"k3s","kind":"HelmChart","group":"helm.cattle.io","namespace":"kube-system","name":"app","valuesRef":{"kind":"HelmChartConfig","group":"helm.cattle.io","namespace":"kube-system","name":"app","exists":true}}`
	if string(raw) != want {
		t.Fatalf("wire = %s\nwant   %s", raw, want)
	}
	if _, ok := reflect.TypeOf(HelmRelease{}).FieldByName("Controller"); !ok {
		t.Fatal("HelmRelease.Controller missing")
	}
	f, _ := reflect.TypeOf(HelmRelease{}).FieldByName("Controller")
	if f.Tag.Get("json") != "controller,omitempty" {
		t.Fatalf("HelmRelease.Controller tag = %q", f.Tag.Get("json"))
	}
}

// Whether a values object exists is unknown, not false, when Radar couldn't
// read the configs (or reads as a caller who may not see them).
func TestValuesRefExistsIsTriState(t *testing.T) {
	exists := func(o k3sOwner) *bool { return releaseController("", o).ValuesRef.Exists }
	if e := exists(k3sOwner{chart: "kube-system/a", config: "kube-system/a", configKnown: true}); e == nil || !*e {
		t.Errorf("read and found: %v", e)
	}
	if e := exists(k3sOwner{chart: "kube-system/a", configKnown: true}); e == nil || *e {
		t.Errorf("read, none: %v", e)
	}
	if e := exists(k3sOwner{chart: "kube-system/a"}); e != nil {
		t.Errorf("unknown: %v", *e)
	}
	unknown := k3sHelmChartKeys(false, []*unstructured.Unstructured{k3sObj("HelmChart", "kube-system", "a", nil)}, nil)
	if unknown["kube-system/a"].configKnown {
		t.Error("configs not read: existence unknown")
	}
}
