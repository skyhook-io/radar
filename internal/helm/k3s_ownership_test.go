package helm

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
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
	got := k3sHelmChartKeys(k3sConfigsKnownEverywhere,
		[]*unstructured.Unstructured{
			k3sObj("HelmChart", "kube-system", "traefik", map[string]any{"chart": "traefik"}),
			k3sObj("HelmChart", "kube-system", "app", map[string]any{"chartContent": "H4sI", "targetNamespace": "apps"}),
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

// helm-controller's shouldManage skips these, so nothing installs their release.
func TestK3sHelmChartKeysSkipsChartsHelmControllerIgnores(t *testing.T) {
	annotated := func(name string, annotations map[string]string) *unstructured.Unstructured {
		u := k3sObj("HelmChart", "kube-system", name, map[string]any{"chart": name})
		u.SetAnnotations(annotations)
		return u
	}
	got := k3sHelmChartKeys(k3sConfigsKnownEverywhere, []*unstructured.Unstructured{
		annotated("unmanaged", map[string]string{"helmcharts.helm.cattle.io/unmanaged": "true"}),
		annotated("other", map[string]string{"helmcharts.cattle.io/managed-by": "custom-controller"}),
		k3sObj("HelmChart", "kube-system", "empty", map[string]any{"targetNamespace": "apps"}),
		annotated("claimed", map[string]string{"helmcharts.cattle.io/managed-by": "helm-controller"}),
		annotated("unclaimed", nil),
	}, nil)
	want := map[string]k3sOwner{
		"kube-system/claimed":   {chart: "kube-system/claimed", configKnown: true},
		"kube-system/unclaimed": {chart: "kube-system/unclaimed", configKnown: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %+v, want %+v", got, want)
	}
}

func TestK3sHelmChartKeysRecordsTheK3sAddon(t *testing.T) {
	packaged := k3sObj("HelmChart", "kube-system", "traefik", map[string]any{"chart": "traefik"})
	packaged.SetAnnotations(map[string]string{
		"objectset.rio.cattle.io/owner-gvk":  "k3s.cattle.io/v1, Kind=Addon",
		"objectset.rio.cattle.io/owner-name": "traefik",
	})
	other := k3sObj("HelmChart", "kube-system", "app", map[string]any{"chart": "app"})
	other.SetAnnotations(map[string]string{"objectset.rio.cattle.io/owner-gvk": "example.com/v1, Kind=Thing", "objectset.rio.cattle.io/owner-name": "x"})
	got := k3sHelmChartKeys(k3sConfigsKnownEverywhere, []*unstructured.Unstructured{packaged, other}, nil)
	if got["kube-system/traefik"].addon != "traefik" || got["kube-system/app"].addon != "" {
		t.Fatalf("keys = %+v", got)
	}
	if c := releaseController("", got["kube-system/traefik"]); c.Addon != "traefik" {
		t.Fatalf("controller = %+v", c)
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
	rows, err := helmReleaseRowsFromStorageSnapshot(context.Background(), snapshot(), nil, k3s, "", adminCaller())
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, err %v", rows, err)
	}
	if c := rows[0].Controller; c == nil || c.Tool != "k3s" || c.Ref() != "kube-system/traefik" || c.ValuesRef == nil || c.ValuesRef.Exists == nil || !*c.ValuesRef.Exists {
		t.Fatalf("row = %+v", rows[0].Controller)
	}
	rows, _ = helmReleaseRowsFromStorageSnapshot(context.Background(), snapshot(), map[string]string{"kube-system/traefik": "flux-system/traefik"}, k3s, "", adminCaller())
	if c := rows[0].Controller; rows[0].ManagedByFluxHelmRelease != "flux-system/traefik" || c == nil || c.Tool != "flux" || c.Kind != "HelmRelease" || c.Ref() != "flux-system/traefik" {
		t.Fatalf("Flux wins, row = %+v", rows[0].Controller)
	}
	// A caller who can't read the HelmChart's namespace isn't told which
	// object installs the release.
	// It still learns that k3s owns it, so writes stay refused.
	private := map[string]k3sOwner{"kube-system/traefik": {chart: "private-charts/traefik", config: "private-charts/traefik", addon: "private-addon"}}
	for _, access := range []StatusAccess{teamACaller(), nil} {
		rows, _ = helmReleaseRowsFromStorageSnapshot(context.Background(), snapshot(), nil, private, "", access)
		if rows[0].Controller != nil || !rows[0].ManagedByK3s {
			t.Fatalf("unreadable HelmChart: controller = %+v, managedByK3s = %v", rows[0].Controller, rows[0].ManagedByK3s)
		}
		if raw := mustJSON(t, rows[0]); strings.Contains(raw, "private-") {
			t.Fatalf("hidden HelmChart leaked: %s", raw)
		}
	}
	rows, _ = helmReleaseRowsFromStorageSnapshot(context.Background(), snapshot(), nil, k3s, "", adminCaller())
	if !rows[0].ManagedByK3s {
		t.Fatal("readable HelmChart lost managedByK3s")
	}
	// Flux ownership with its HelmRelease withheld still wins over the HelmChart.
	rows, _ = helmReleaseRowsFromStorageSnapshot(context.Background(), snapshot(), map[string]string{"kube-system/traefik": "flux-system/traefik"}, k3s, "", teamACaller())
	if !rows[0].ManagedByFlux || rows[0].Controller != nil || rows[0].ManagedByK3s {
		t.Fatalf("hidden Flux owner, row = %+v", rows[0])
	}
}

// Two HelmCharts that install the same release can't be told apart, so the
// release is credited to neither, as for two Flux HelmReleases.
func TestK3sHelmChartKeysCreditNeitherOfTwoCharts(t *testing.T) {
	got := k3sHelmChartKeys(k3sConfigsKnownEverywhere, []*unstructured.Unstructured{
		k3sObj("HelmChart", "kube-system", "app", map[string]any{"chart": "app", "targetNamespace": "apps"}),
		k3sObj("HelmChart", "team", "app", map[string]any{"chart": "app", "targetNamespace": "apps"}),
	}, nil)
	if _, ok := got["apps/app"]; ok {
		t.Fatalf("keys = %+v, want apps/app credited to neither", got)
	}
}

// The observed scan links a HelmChartConfig to its HelmChart whichever the
// caller's read returns first; only objects the scan saw count.
func TestObservedScanLinksK3sChartAndConfigInEitherOrder(t *testing.T) {
	chart := k3sObj("HelmChart", "kube-system", "app", map[string]any{"chart": "app", "targetNamespace": "apps"})
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
	unknown := k3sHelmChartKeys(k3sConfigsUnknown, []*unstructured.Unstructured{k3sObj("HelmChart", "kube-system", "a", map[string]any{"chart": "a"})}, nil)
	if o, ok := unknown["kube-system/a"]; !ok || o.configKnown {
		t.Error("configs not read: existence unknown")
	}
}

func k3sConfigsKnownEverywhere(string) bool { return true }
