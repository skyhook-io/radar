package helm

import (
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

func stamps(release, namespace string) map[string]string {
	return map[string]string{releaseNameAnnotation: release, releaseNamespaceAnnotation: namespace}
}

func objectsWithCharts(labels map[string]int) []stampedObject {
	objects := []stampedObject{}
	for label, count := range labels {
		for range count {
			objects = append(objects, stampedObject{chartLabel: label})
		}
	}
	return objects
}

func TestSkipScanKindNeverReadsSecrets(t *testing.T) {
	for _, tc := range []struct {
		kind, group string
		skip        bool
	}{
		{"Secret", "", true},
		{"Pod", "", true},
		{"ReplicaSet", "apps", true},
		{"Event", "events.k8s.io", true},
		{"Deployment", "apps", false},
		{"ConfigMap", "", false},
		// A custom kind with a built-in's name is not that built-in.
		{"Secret", "example.com", false},
		{"Pod", "example.com", false},
	} {
		if got := skipScanKind(tc.kind, tc.group); got != tc.skip {
			t.Errorf("skipScanKind(%q, %q) = %t, want %t", tc.kind, tc.group, got, tc.skip)
		}
	}
}

func TestChooseChart(t *testing.T) {
	for _, tc := range []struct {
		name        string
		release     string
		objects     []stampedObject
		wantChart   string
		wantVersion string
		wantApp     string
		wantOthers  []string
	}{
		{
			name:        "one chart",
			release:     "prom-compare",
			objects:     []stampedObject{{chartLabel: "prometheus-29.36.1", appVersion: "v3.15.0"}, {chartLabel: "prometheus-29.36.1", appVersion: "v3.15.0"}},
			wantChart:   "prometheus",
			wantVersion: "29.36.1",
			wantApp:     "v3.15.0",
		},
		{
			name:    "no chart label",
			release: "rel-a",
			objects: []stampedObject{{}},
		},
		{
			// The count favours a subchart: chart name, not frequency, finds the parent.
			name:    "subchart outnumbers its parent",
			release: "signoz",
			objects: append(append(
				objectsWithCharts(map[string]int{"clickhouse-24.1.18": 21, "zookeeper-0.0.1": 5}),
				stampedObject{chartLabel: "signoz-0.145.0", appVersion: "v0.145.0"}),
				stampedObject{chartLabel: "signoz-0.145.0", appVersion: "v0.145.0"}),
			wantChart:   "signoz",
			wantVersion: "0.145.0",
			wantApp:     "v0.145.0",
			wantOthers:  []string{"clickhouse-24.1.18", "zookeeper-0.0.1"},
		},
		{
			name:       "several charts and none named like the release",
			release:    "umbrella-x",
			objects:    objectsWithCharts(map[string]int{"parent-2.0.0": 1, "sub-0.5.0": 3}),
			wantOthers: []string{"parent-2.0.0", "sub-0.5.0"},
		},
		{
			name:    "a subchart's app version isn't the release's",
			release: "caretta",
			objects: []stampedObject{
				{chartLabel: "grafana-6.48.0", appVersion: "9.3.1"},
				{chartLabel: "grafana-6.48.0", appVersion: "9.3.1"},
				{chartLabel: "caretta-0.0.16", appVersion: "v0.0.16"},
			},
			wantChart:   "caretta",
			wantVersion: "0.0.16",
			wantApp:     "v0.0.16",
			wantOthers:  []string{"grafana-6.48.0"},
		},
		{
			name:    "a stray object doesn't outvote the rest",
			release: "metrics",
			objects: []stampedObject{
				{chartLabel: "metrics-server-3.13.0", appVersion: "0.8.0"},
				{chartLabel: "metrics-server-3.13.0", appVersion: "0.8.0"},
				{chartLabel: "metrics-server-3.13.0", appVersion: "0.7.2"},
			},
			wantChart:   "metrics-server",
			wantVersion: "3.13.0",
			wantApp:     "0.8.0",
		},
		{
			name:      "tied versions stay unknown",
			release:   "mid-upgrade",
			objects:   []stampedObject{{chartLabel: "app-1.0.0", appVersion: "1"}, {chartLabel: "app-1.1.0", appVersion: "2"}},
			wantChart: "app",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chart, version, app, others := chooseChart(tc.release, tc.objects)
			if chart != tc.wantChart || version != tc.wantVersion || app != tc.wantApp {
				t.Errorf("chart %q version %q app %q, want %q %q %q", chart, version, app, tc.wantChart, tc.wantVersion, tc.wantApp)
			}
			if len(others) != len(tc.wantOthers) || (len(others) > 0 && !reflect.DeepEqual(others, tc.wantOthers)) {
				t.Errorf("others = %v, want %v", others, tc.wantOthers)
			}
		})
	}
}

func TestParseChartLabel(t *testing.T) {
	cut := "a-very-long-chart-name-that-runs-right-up-to-the-limit-1.2.3-rc"
	if len(cut) != maxChartLabelLength {
		t.Fatalf("fixture is %d characters", len(cut))
	}
	for _, tc := range []struct{ label, name, version string }{
		{"kube-prometheus-stack-45.27.2", "kube-prometheus-stack", "45.27.2"},
		{"app-1.2.3_build.4", "app", "1.2.3+build.4"},
		{"app-1.2.3-1", "app", "1.2.3-1"},
		{cut, "a-very-long-chart-name-that-runs-right-up-to-the-limit", ""},
	} {
		name, version := parseChartLabel(tc.label)
		if name != tc.name || version != tc.version {
			t.Errorf("parseChartLabel(%q) = %q, %q, want %q, %q", tc.label, name, version, tc.name, tc.version)
		}
	}
}

func TestStampScanKeepsOnlyWhatHelmDeployed(t *testing.T) {
	owner := true
	scan := &stampScan{seen: map[string]bool{}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps", Annotations: stamps("api", "apps"),
		Labels: map[string]string{chartLabelKey: "api-1.0.0", appVersionLabelKey: "2.0"}}}
	scan.add(search.ScannedObject{Kind: "Deployment", Group: "apps", Object: deployment})
	// Served by both the typed and the dynamic path.
	scan.add(search.ScannedObject{Kind: "Deployment", Group: "apps", Object: deployment})
	// Labels alone are what a rendered chart carries with no Helm release, as
	// under Argo CD.
	scan.add(search.ScannedObject{Kind: "ConfigMap", Object: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "rendered", Namespace: "apps",
		Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm", chartLabelKey: "api-1.0.0"}}}})
	scan.add(search.ScannedObject{Kind: "ConfigMap", Object: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "half", Namespace: "apps",
		Annotations: map[string]string{releaseNameAnnotation: "api"}}}})
	// An operator copied the stamps onto an object it created.
	scan.add(search.ScannedObject{Kind: "StatefulSet", Group: "apps", Object: &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "copy", Namespace: "apps",
		Annotations: stamps("api", "apps"), OwnerReferences: []metav1.OwnerReference{{Kind: "Installation", Name: "x", Controller: &owner}}}}})

	if len(scan.stamped) != 1 || scan.stamped[0].name != "api" {
		t.Fatalf("stamped = %+v, want only the Deployment", scan.stamped)
	}
}

func fluxHelmRelease(status map[string]any, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": "hr", "namespace": "flux-system"},
		"spec":     spec, "status": status,
	}}
}

func TestParseFluxEvidence(t *testing.T) {
	recorded, ok := parseFluxEvidence(fluxHelmRelease(map[string]any{
		"storageNamespace": "flux-system",
		"history": []any{
			map[string]any{"name": "real-name", "namespace": "apps", "version": int64(4), "status": "failed", "chartName": "podinfo", "chartVersion": "6.17.0", "appVersion": "6.17.0", "digest": "sha256:a", "lastDeployed": "2026-10-26T21:30:48Z"},
			map[string]any{"name": "real-name", "namespace": "apps", "version": int64(3), "status": "superseded", "chartName": "podinfo", "chartVersion": "6.16.0"},
		},
	}, map[string]any{"targetNamespace": "other"}))
	if !ok {
		t.Fatal("not parsed")
	}
	if recorded.ref != "flux-system/hr" || recorded.releaseName != "real-name" || recorded.releaseNamespace != "apps" || recorded.storageNamespace != "flux-system" {
		t.Fatalf("identity = %+v", recorded)
	}
	if len(recorded.history) != 2 || recorded.history[0].Revision != 4 || recorded.history[0].Status != "failed" || recorded.history[0].LastDeployed == nil || recorded.historyChart != "podinfo" {
		t.Fatalf("history = %+v", recorded)
	}

	implied, ok := parseFluxEvidence(fluxHelmRelease(map[string]any{}, map[string]any{"targetNamespace": "apps"}))
	if !ok || implied.releaseName != "apps-hr" || implied.releaseNamespace != "apps" || implied.storageNamespace != "flux-system" {
		t.Fatalf("implied = %+v, want Flux's default release name", implied)
	}

	if _, ok := parseFluxEvidence(fluxHelmRelease(map[string]any{}, map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "remote"}}})); ok {
		t.Fatal("a HelmRelease that installs into another cluster says nothing about this one")
	}
}

func TestObservedRowUsesFluxRecordOverLabels(t *testing.T) {
	key := releaseKey{"apps", "web"}
	objects := []stampedObject{{chartLabel: "grafana-6.0.0", appVersion: "9"}, {chartLabel: "wrapper-1.0.0", appVersion: "1"}}
	row := observedRow(key, objects, []fluxEvidence{{ref: "flux-system/web", releaseName: "web", releaseNamespace: "apps", storageNamespace: "flux-system", historyChart: "wrapper", historyVersion: "1.0.1", historyAppVersion: "1.1"}})
	if row.Chart != "wrapper" || row.ChartVersion != "1.0.1" || row.AppVersion != "1.1" {
		t.Fatalf("chart = %+v", row)
	}
	if row.ManagedByFluxHelmRelease != "flux-system/web" || row.StorageNamespace != "flux-system" {
		t.Fatalf("flux = %+v", row)
	}
	if !reflect.DeepEqual(row.OtherCharts, []string{"grafana-6.0.0"}) {
		t.Fatalf("others = %v", row.OtherCharts)
	}

	unread := observedRow(key, objects, nil)
	if unread.ManagedByFluxHelmRelease != "" || unread.StorageNamespace != "" {
		t.Fatalf("a HelmRelease the caller can't read contributes nothing: %+v", unread)
	}
}

func TestObservedReleaseHasNoFieldToGuessFrom(t *testing.T) {
	fields := reflect.TypeOf(ObservedRelease{})
	for i := range fields.NumField() {
		name := strings.ToLower(fields.Field(i).Name)
		for _, forbidden := range []string{"status", "revision", "updated"} {
			if name == forbidden {
				t.Fatalf("ObservedRelease has %s, which only Helm's storage knows", fields.Field(i).Name)
			}
		}
	}
}

func TestAddObservedHealthDoesNotClaimHealthyOverAGap(t *testing.T) {
	// Without a cache no status is read, so drive the rule through its result.
	row := ObservedRelease{}
	addObservedHealth(&row, []stampedObject{{kind: "ConfigMap", namespace: "apps", name: "c"}}, true)
	if row.ResourceHealth != "unknown" {
		t.Fatalf("health = %q, want unknown", row.ResourceHealth)
	}
}

func TestInventorySummarySaysWhatIsUnknown(t *testing.T) {
	summary := (&ReleaseInventory{Releases: []HelmRelease{{Name: "a"}}, Observed: []ObservedRelease{{Name: "b"}}, StorageDenied: resourceapi.Names([]string{"team-b"}, resourceapi.MCPSetLimit)}).Summary()
	for _, want := range []string{"Partial:", "1 releases read from Helm and 1 observed", "in team-b", "status, revision", "Absence proves nothing"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q: %s", want, summary)
		}
	}
}
