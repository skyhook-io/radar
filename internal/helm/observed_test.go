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
	"github.com/skyhook-io/radar/pkg/readreason"
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
		{"Event", "events.k8s.io", true},
		{"Deployment", "apps", false},
		{"ConfigMap", "", false},
		// A chart can deploy these directly; what a controller made is skipped
		// by its owner, not its kind.
		{"Pod", "", false},
		{"ReplicaSet", "apps", false},
		// A custom kind with a built-in's name is not that built-in.
		{"Secret", "example.com", false},
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
		// Set only when the chosen chart's objects disagree.
		wantVersions    []string
		wantAppVersions []string
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
			// A known limit, stated in the callout: the labels can't show which
			// chart is the parent, so a release named after a subchart gets it.
			name:        "release named after a subchart",
			release:     "redis",
			objects:     []stampedObject{{chartLabel: "platform-2.0.0"}, {chartLabel: "redis-19.0.0"}},
			wantChart:   "redis",
			wantVersion: "19.0.0",
			wantOthers:  []string{"platform-2.0.0"},
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
			// A majority would hide the one object still on another version.
			name:    "a minority version is reported, not outvoted",
			release: "metrics",
			objects: []stampedObject{
				{chartLabel: "metrics-server-3.13.0", appVersion: "0.8.0"},
				{chartLabel: "metrics-server-3.13.0", appVersion: "0.8.0"},
				{chartLabel: "metrics-server-3.13.0", appVersion: "0.7.2"},
			},
			wantChart:       "metrics-server",
			wantVersion:     "3.13.0",
			wantAppVersions: []string{"0.7.2", "0.8.0"},
		},
		{
			name:            "versions that disagree stay unknown",
			release:         "mid-upgrade",
			objects:         []stampedObject{{chartLabel: "app-1.0.0", appVersion: "1"}, {chartLabel: "app-1.1.0", appVersion: "2"}},
			wantChart:       "app",
			wantVersions:    []string{"1.0.0", "1.1.0"},
			wantAppVersions: []string{"1", "2"},
		},
		{
			// Voting on each label separately would show 2.0.0 with app 1.0, a
			// pair no object carries.
			name:    "chart and app version aren't paired from different objects",
			release: "api",
			objects: []stampedObject{
				{chartLabel: "api-2.0.0"}, {chartLabel: "api-2.0.0"}, {chartLabel: "api-2.0.0"},
				{chartLabel: "api-1.0.0", appVersion: "1.0"},
			},
			wantChart:    "api",
			wantApp:      "1.0",
			wantVersions: []string{"1.0.0", "2.0.0"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseChart(tc.release, tc.objects)
			if got.chart != tc.wantChart || got.version != tc.wantVersion || got.appVersion != tc.wantApp {
				t.Errorf("chart %q version %q app %q, want %q %q %q", got.chart, got.version, got.appVersion, tc.wantChart, tc.wantVersion, tc.wantApp)
			}
			sameList := func(name string, got, want []string) {
				if len(got) != len(want) || (len(got) > 0 && !reflect.DeepEqual(got, want)) {
					t.Errorf("%s = %v, want %v", name, got, want)
				}
			}
			sameList("others", got.others, tc.wantOthers)
			sameList("versions", got.versions, tc.wantVersions)
			sameList("app versions", got.appVersions, tc.wantAppVersions)
		})
	}
}

func TestParseChartLabel(t *testing.T) {
	cut := "a-very-long-chart-name-that-runs-right-up-to-the-limit-1.2.3-rc"
	if len(cut) != maxChartLabelLength {
		t.Fatalf("fixture is %d characters", len(cut))
	}
	// The `helm create` helper truncates to 63 and then trims a trailing "-",
	// which turns name-1.2.3-rc.1 into a 62-character label ending in 1.2.3.
	trimmed := strings.TrimSuffix((strings.Repeat("a", 56) + "-1.2.3-rc.1")[:maxChartLabelLength], "-")
	if len(trimmed) != maxChartLabelLength-1 {
		t.Fatalf("trimmed fixture is %d characters", len(trimmed))
	}
	for _, tc := range []struct{ label, name, version string }{
		{"kube-prometheus-stack-45.27.2", "kube-prometheus-stack", "45.27.2"},
		{trimmed, strings.Repeat("a", 56), ""},
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
	// A chart can deploy a bare Pod.
	scan.add(search.ScannedObject{Kind: "Pod", Object: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "one-off", Namespace: "apps", Annotations: stamps("api", "apps")}}})
	// A Deployment copies its annotations onto its ReplicaSets.
	scan.add(search.ScannedObject{Kind: "ReplicaSet", Group: "apps", Object: &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "api-abc", Namespace: "apps",
		Annotations: stamps("api", "apps"), OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "api", Controller: &owner}}}}})
	// An operator copied the stamps onto an object it created.
	scan.add(search.ScannedObject{Kind: "StatefulSet", Group: "apps", Object: &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "copy", Namespace: "apps",
		Annotations: stamps("api", "apps"), OwnerReferences: []metav1.OwnerReference{{Kind: "Installation", Name: "x", Controller: &owner}}}}})

	if len(scan.stamped) != 2 || scan.stamped[0].name != "api" || scan.stamped[1].name != "one-off" {
		t.Fatalf("stamped = %+v, want the Deployment and the bare Pod", scan.stamped)
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

	long := "a-namespace-with-quite-a-long-name-for-this"
	shortened, ok := parseFluxEvidence(fluxHelmRelease(map[string]any{}, map[string]any{"targetNamespace": long + "-and-then-some"}))
	if !ok || len(shortened.releaseName) > 53 {
		t.Fatalf("shortened = %+v, want Flux's limit on default names", shortened)
	}

	if _, ok := parseFluxEvidence(fluxHelmRelease(map[string]any{}, map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "remote"}}})); ok {
		t.Fatal("a HelmRelease that installs into another cluster says nothing about this one")
	}
}

func TestObservedRowCarriesK3sOwner(t *testing.T) {
	key := releaseKey{"kube-system", "traefik"}
	objects := []stampedObject{{chartLabel: "traefik-34.2.1"}}
	k3s := map[string]k3sOwner{"kube-system/traefik": {chart: "kube-system/traefik", config: "kube-system/traefik"}}
	if row := observedRow(key, objects, nil, k3s); row.Controller == nil || row.Controller.Tool != "k3s" || row.Controller.Ref() != "kube-system/traefik" || row.Controller.ValuesRef.Exists == nil || !*row.Controller.ValuesRef.Exists {
		t.Fatalf("row = %+v", row)
	}
	flux := []fluxEvidence{{ref: "flux-system/traefik", releaseName: "traefik", releaseNamespace: "kube-system", storageNamespace: "kube-system"}}
	if row := observedRow(key, objects, flux, k3s); row.Controller == nil || row.Controller.Tool != "flux" {
		t.Fatalf("Flux wins, row = %+v", row)
	}
	if row := observedRow(key, objects, nil, nil); row.Controller != nil {
		t.Fatalf("no readable HelmChart, row = %+v", row)
	}
}

func TestObservedRowUsesFluxRecordOverLabels(t *testing.T) {
	key := releaseKey{"apps", "web"}
	objects := []stampedObject{{chartLabel: "grafana-6.0.0", appVersion: "9"}, {chartLabel: "wrapper-1.0.0", appVersion: "1"}}
	row := observedRow(key, objects, []fluxEvidence{{ref: "flux-system/web", releaseName: "web", releaseNamespace: "apps", storageNamespace: "flux-system", historyChart: "wrapper", historyVersion: "1.0.1", historyAppVersion: "1.1"}}, nil)
	if row.Chart != "wrapper" || row.ChartVersion != "1.0.1" || row.AppVersion != "1.1" {
		t.Fatalf("chart = %+v", row)
	}
	if row.ManagedByFluxHelmRelease != "flux-system/web" || row.StorageNamespace != "flux-system" {
		t.Fatalf("flux = %+v", row)
	}
	if !reflect.DeepEqual(row.OtherCharts, []string{"grafana-6.0.0"}) {
		t.Fatalf("others = %v", row.OtherCharts)
	}

	unread := observedRow(key, objects, nil, nil)
	if unread.ManagedByFluxHelmRelease != "" || unread.StorageNamespace != "" {
		t.Fatalf("a HelmRelease the caller can't read contributes nothing: %+v", unread)
	}
}

// Two HelmReleases deploying the same release can't be told apart by its
// objects, so the row takes neither's name, storage namespace or chart.
func TestObservedRowCreditsNeitherOfTwoHelmReleases(t *testing.T) {
	key := releaseKey{"apps", "web"}
	objects := []stampedObject{{chartLabel: "web-1.0.0", appVersion: "1"}}
	row := observedRow(key, objects, []fluxEvidence{
		{ref: "team-a/web", releaseName: "web", releaseNamespace: "apps", storageNamespace: "team-a", historyChart: "web", historyVersion: "1.0.0"},
		{ref: "team-b/web", releaseName: "web", releaseNamespace: "apps", storageNamespace: "team-b", historyChart: "web", historyVersion: "2.0.0"},
	}, nil)
	if row.ManagedByFluxHelmRelease != "" || row.StorageNamespace != "" || row.ChartVersion != "1.0.0" {
		t.Fatalf("row = %+v, want labels only", row)
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
	for _, want := range []string{"Partial:", "1 releases read from Helm and 1 observed", "in team-b", "status, revision", "Absence proves nothing", "get_helm_release returns one of them", "not proof that a release exists", "exposes every Secret"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q: %s", want, summary)
		}
	}
	if strings.Contains(summary, "weren't read") {
		t.Errorf("summary reports a gap the read didn't have: %s", summary)
	}
}

func TestInventorySummaryReportsObjectGaps(t *testing.T) {
	for _, tc := range []struct {
		name      string
		code      readreason.Code
		wantRetry bool
	}{
		{"can settle", readreason.KindSyncPending, true},
		{"needs someone to act", readreason.RadarKindNotCollected, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary := (&ReleaseInventory{StorageDenied: resourceapi.AllNames(), ObjectCoverage: &resourceapi.SearchCoverage{Gaps: []resourceapi.Gap{{Code: tc.code}}}}).Summary()
			if !strings.Contains(summary, "Some objects weren't read ("+string(tc.code)+")") {
				t.Errorf("summary doesn't name the gap: %s", summary)
			}
			if got := strings.Contains(summary, "Call list_helm_releases again"); got != tc.wantRetry {
				t.Errorf("retry instruction = %v, want %v: %s", got, tc.wantRetry, summary)
			}
		})
	}
}

func TestCoverageMaySettle(t *testing.T) {
	for _, tc := range []struct {
		name string
		gaps []resourceapi.Gap
		want bool
	}{
		{"complete", nil, false},
		{"still loading", []resourceapi.Gap{{Code: readreason.KindSyncPending}}, true},
		{"read failed", []resourceapi.Gap{{Code: readreason.ReadError}}, true},
		{"access check failed", []resourceapi.Gap{{Code: readreason.UserPermissionCheckFailed}}, true},
		{"denied by RBAC", []resourceapi.Gap{{Code: readreason.UserForbidden}}, false},
		{"mixed", []resourceapi.Gap{{Code: readreason.UserForbidden}, {Code: readreason.KindSyncPending}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := coverageMaySettle(tc.gaps); got != tc.want {
				t.Fatalf("coverageMaySettle = %v, want %v", got, tc.want)
			}
		})
	}
}
