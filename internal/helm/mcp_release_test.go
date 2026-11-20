package helm

import (
	"encoding/json"
	"errors"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/storage/driver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
)

func TestMCPStoredChartPreviewAndApplyPreserveValues(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "merge", true: "replace"}[replace], func(t *testing.T) {
			cfg, writes := targetNamespaceActionConfig(t)
			seedRelease(t, cfg, "demo", release.StatusDeployed, 1)
			rel, _ := cfg.Releases.Get("demo", 1)
			rel.Namespace = "app"
			rel.Manifest = targetNamespaceManifest("update")
			rel.Config = map[string]any{"message": "old", "auth": map[string]any{"password": "keep-secret", "user": "old"}, "ports": []any{80.0}}
			rel.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0", APIVersion: "v2"},
				Templates: []*chart.File{{Name: "templates/cm.yaml", Data: []byte(strings.ReplaceAll(rel.Manifest, "message: old", "message: {{ .Values.message }}"))}}}
			if err := cfg.Releases.Update(rel); err != nil {
				t.Fatal(err)
			}
			client := newChartSourceTestClient(t)
			values := map[string]any{"message": "new", "auth": map[string]any{"user": "new"}, "ports": []any{443.0}}
			beforeValues := cloneInstallValues(rel.Config)
			opts := valuesUpgradeOptions{allowStoredChart: true, replace: replace, dryRun: true, expectedRevision: 1}
			before, preview, err := client.runValuesUpgrade(cfg, "demo", "", "", cloneInstallValues(values), opts, progressSender(nil))
			if err != nil {
				t.Fatal(err)
			}
			current, _ := cfg.Releases.Last("demo")
			if current.Version != 1 || len(*writes) != 0 || !reflect.DeepEqual(current.Config, beforeValues) {
				t.Fatalf("preview changed state: rev=%d values=%v writes=%v", current.Version, current.Config, *writes)
			}
			if before.Version != 1 || !strings.Contains(preview.Manifest, "message: new") {
				t.Fatalf("bad preview: %s", preview.Manifest)
			}
			opts.dryRun = false
			_, applied, err := client.runValuesUpgrade(cfg, "demo", "", "", cloneInstallValues(values), opts, progressSender(nil))
			if err != nil {
				t.Fatal(err)
			}
			if len(*writes) == 0 || applied.Manifest != preview.Manifest || !reflect.DeepEqual(applied.Config, preview.Config) {
				t.Fatalf("preview/apply differ: %v / %v", preview.Config, applied.Config)
			}
			password, exists := applied.Config["auth"].(map[string]any)["password"]
			if replace && exists || !replace && password != "keep-secret" {
				t.Fatalf("wrong secret preservation (replace=%v): %v", replace, applied.Config)
			}
			if _, _, err := client.runValuesUpgrade(cfg, "demo", "", "", values, opts, progressSender(nil)); err == nil || !strings.Contains(err.Error(), "expected 1") {
				t.Fatalf("stale revision accepted: %v", err)
			}
		})
	}
}

func TestMCPStoredChartRefusesAmbiguousOrUnsafeHistory(t *testing.T) {
	for _, scenario := range []string{"failed-version", "failed-source", "missing-dependency", "pending", "strict-ui"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := memoryActionConfig(t)
			seedRelease(t, cfg, "demo", release.StatusDeployed, 1)
			rel, _ := cfg.Releases.Get("demo", 1)
			rel.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0", APIVersion: "v2"}}
			rel.Config = map[string]any{"message": "stable"}
			if err := cfg.Releases.Update(rel); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(scenario, "failed") {
				next := *rel
				next.Version = 2
				next.Info = &release.Info{Status: release.StatusFailed}
				if scenario == "failed-version" {
					next.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "2.0.0", APIVersion: "v2"}}
				} else {
					next.Labels = map[string]string{chartSourceAttemptLabel: "true"}
				}
				if err := cfg.Releases.Create(&next); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "missing-dependency" {
				rel.Chart.Metadata.Dependencies = []*chart.Dependency{{Name: "child", Version: "1.0.0"}}
			} else if scenario == "pending" {
				rel.Info.Status = release.StatusPendingUpgrade
			}
			client := newChartSourceTestClient(t)
			_, _, err := client.runValuesUpgrade(cfg, "demo", "", "", nil, valuesUpgradeOptions{dryRun: true, allowStoredChart: scenario != "strict-ui"}, progressSender(nil))
			if err == nil {
				t.Fatal("unsafe/unsupported edit accepted")
			}
			if scenario == "pending" && !errors.Is(err, errReleaseChanging) {
				t.Fatalf("wrong pending refusal: %v", err)
			}
			if (scenario == "failed-source" || scenario == "missing-dependency" || scenario == "strict-ui") && !errors.Is(err, errSourceRequired) {
				t.Fatalf("wrong source refusal: %v", err)
			}

			if scenario == "failed-version" && (!strings.Contains(err.Error(), "rollback to revision 1") || !strings.Contains(err.Error(), "2.0.0")) {
				t.Fatalf("missing recovery: %v", err)
			}
		})
	}
}

func TestMCPUpgradePreviewUsesCallerLookups(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(map[bool]string{false: "allowed", true: "denied"}[denied], func(t *testing.T) {
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != "GET" || r.Header.Get("Impersonate-User") != "fixture-reader" {
					t.Errorf("unexpected request %s as %s", r.Method, r.Header.Get("Impersonate-User"))
				}
				if r.URL.Path == "/api/v1" {
					writeTestJSON(t, w, map[string]any{"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "v1", "resources": []any{map[string]any{"name": "configmaps", "kind": "ConfigMap", "namespaced": true, "verbs": []string{"get"}}}})
					return
				}
				if r.URL.Path != "/api/v1/namespaces/default/configmaps/lookup" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				hits++
				if denied {
					writeK8sStatus(t, w, 403, "Forbidden", "lookup denied")
					return
				}
				writeTestJSON(t, w, map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "lookup", "namespace": "default"}, "data": map[string]any{"marker": "observed"}})
			}))
			defer srv.Close()
			cfg := memoryActionConfig(t)
			cfg.RESTClientGetter = newRESTConfigGetter(&rest.Config{Host: srv.URL, Impersonate: rest.ImpersonationConfig{UserName: "fixture-reader"}}, "default", nil)
			seedRelease(t, cfg, "demo", release.StatusDeployed, 1)
			rel, _ := cfg.Releases.Get("demo", 1)
			rel.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0", APIVersion: "v2"}, Templates: []*chart.File{{Name: "templates/cm.yaml", Data: []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  marker: {{ (lookup "v1" "ConfigMap" .Release.Namespace "lookup").data.marker | quote }}
`)}}}
			if err := cfg.Releases.Update(rel); err != nil {
				t.Fatal(err)
			}
			_, next, err := newChartSourceTestClient(t).runValuesUpgrade(cfg, "demo", "", "", nil, valuesUpgradeOptions{dryRun: true, allowStoredChart: true}, progressSender(nil))
			if denied {
				if err == nil || !strings.Contains(err.Error(), "lookup denied") {
					t.Fatalf("denied lookup returned preview: %v", err)
				}
			} else if err != nil || !strings.Contains(next.Manifest, "observed") {
				t.Fatalf("server lookup not used: %v / %v", err, next)
			}
			latest, _ := cfg.Releases.Last("demo")
			if hits != 1 || latest.Version != 1 {
				t.Fatalf("lookup hits %d revision %d", hits, latest.Version)
			}
		})
	}
}

func TestMCPUpgradePreviewSelectsDeployedOrFailedInitialBaseline(t *testing.T) {
	for _, initialFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "deployed-before-failure", true: "failed-initial"}[initialFailure], func(t *testing.T) {
			cfg := memoryActionConfig(t)
			status := release.StatusDeployed
			if initialFailure {
				status = release.StatusFailed
			}
			seedRelease(t, cfg, "demo", status, 1)
			rel, _ := cfg.Releases.Get("demo", 1)
			rel.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0", APIVersion: "v2"}}
			rel.Config = map[string]any{"retained": "baseline", "ports": []any{80.0}, "optional": "remove"}
			if err := cfg.Releases.Update(rel); err != nil {
				t.Fatal(err)
			}
			if !initialFailure {
				next := *rel
				next.Version = 2
				next.Info = &release.Info{Status: release.StatusFailed}
				next.Config = map[string]any{"retained": "failed"}
				if err := cfg.Releases.Create(&next); err != nil {
					t.Fatal(err)
				}
			}
			before, next, err := newChartSourceTestClient(t).runValuesUpgrade(cfg, "demo", "", "", map[string]any{"ports": []any{443.0}, "optional": nil}, valuesUpgradeOptions{dryRun: true, allowStoredChart: true}, progressSender(nil))
			if err != nil {
				t.Fatal(err)
			}
			if before.Version != 1 || next.Config["retained"] != "baseline" || !reflect.DeepEqual(next.Config["ports"], []any{443.0}) || next.Config["optional"] != nil {
				t.Fatalf("wrong merged baseline: %v", next.Config)
			}
		})
	}
}

func TestMCPStoredChartDoesNotConfirmRecordedSource(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "demo", release.StatusDeployed, 1)
	rel, _ := cfg.Releases.Get("demo", 1)
	rel.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0", APIVersion: "v2"}}
	rel.Labels = chartSourceLabels(&ChartSourceCandidate{Type: "repository", Reference: "original", URL: "https://example.test/charts"})
	rel.Labels[chartSourceBindingLabel] = "stale-binding"
	if err := cfg.Releases.Update(rel); err != nil {
		t.Fatal(err)
	}
	_, next, err := newChartSourceTestClient(t).runValuesUpgrade(cfg, "demo", "", "", nil, valuesUpgradeOptions{dryRun: true, allowStoredChart: true}, progressSender(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := confirmedChartSource(next); ok || !reflect.DeepEqual(next.Labels, rel.Labels) {
		t.Fatalf("source provenance changed: %v", next.Labels)
	}
}

func TestMCPUndeclaredSubchartsRequireReconstruction(t *testing.T) {
	for _, hookOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "manifest", true: "hook"}[hookOnly], func(t *testing.T) {
			parent := &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0", APIVersion: "v2"}}
			rel := &release.Release{Name: "demo", Chart: parent}
			source := "fixture/charts/child/templates/cm.yaml"
			if hookOnly {
				rel.Hooks = []*release.Hook{{Path: source}}
			} else {
				rel.Manifest = "# Source: " + source + "\n" + targetNamespaceManifest("child")
			}
			if err := validateStoredChartDependencyBodies(parent, rel); err == nil {
				t.Fatal("missing undeclared body accepted")
			}
			loaded := &chart.Chart{Metadata: parent.Metadata}
			loaded.AddDependency(&chart.Chart{Metadata: &chart.Metadata{Name: "child", Version: "1.0.0"}})
			hits := 0
			got, err := (&Client{}).chartForUpgradeTargetWithLoader(nil, rel, "", "", progressSender(nil), func(*action.Configuration, *release.Release, string, string, func(string, string, string)) (*chart.Chart, error) {
				hits++
				return loaded, nil
			})
			if err != nil || got != loaded || hits != 1 {
				t.Fatalf("reconstruction not used: %v hits%d", err, hits)
			}
			_, err = (&Client{}).chartForUpgradeTargetWithLoader(nil, rel, "", "", progressSender(nil), func(*action.Configuration, *release.Release, string, string, func(string, string, string)) (*chart.Chart, error) {
				return parent, nil
			})
			if err == nil || !strings.Contains(err.Error(), "still lacks") {
				t.Fatalf("incomplete fetched chart accepted: %v", err)
			}
		})
	}
}

func TestMCPPreviewReportsCompleteObjectChangesAndRedactsValues(t *testing.T) {
	for _, fixture := range []struct{ kind, api, old, new string }{
		{"PersistentVolumeClaim", "v1", `spec: {resources: {requests: {storage: 1Gi}}}`, `spec: {resources: {requests: {storage: 2Gi}}}`},
		{"Application", "argoproj.io/v1alpha1", `spec: {source: {targetRevision: main}}`, `spec: {source: {targetRevision: release}}`},
		{"HelmRelease", "helm.toolkit.fluxcd.io/v2", `spec: {interval: 1m}`, `spec: {interval: 5m}`},
		{"ConfigMap", "v1", `metadata: {name: demo, annotations: {load: old}}`, `metadata: {name: demo, annotations: {load: new}}`},
		{"Service", "v1", `metadata: {name: demo, labels: {app: old}}`, `metadata: {name: demo, labels: {app: new}}`},
	} {
		t.Run(fixture.kind, func(t *testing.T) {
			prefix := "apiVersion: " + fixture.api + "\nkind: " + fixture.kind + "\n"
			if !strings.HasPrefix(fixture.old, "metadata:") {
				prefix += "metadata: {name: demo}\n"
			}
			before := &release.Release{Namespace: "default", Version: 1, Chart: &chart.Chart{Metadata: &chart.Metadata{Version: "1.0.0"}}, Manifest: prefix + fixture.old, Config: map[string]any{"password": "old-credential"}}
			after := &release.Release{Namespace: "default", Version: 2, Manifest: prefix + fixture.new, Config: map[string]any{"password": "new-credential"}, Chart: &chart.Chart{Metadata: &chart.Metadata{Version: "1.0.0"}}}
			result, err := mcpUpgradeResult(before, after, true)
			if err != nil || len(result.Changed) != 1 || result.ParseErrors != 0 || strings.Contains(result.ValuesDiff, "credential") {
				t.Fatalf("incomplete or unsafe preview: %+v %v", result, err)
			}
			after.Manifest += "\n---\n" + targetNamespaceManifest("added") + "\n---\ninvalid: ["
			result, err = mcpUpgradeResult(before, after, true)
			if err != nil || len(result.Added) != 1 || result.ParseErrors != 1 {
				t.Fatalf("partial/addition comparison: %+v %v", result, err)
			}
			result, err = mcpUpgradeResult(after, before, true)
			if err == nil && len(result.Removed) != 1 {
				t.Fatalf("removal missing: %+v", result)
			}
		})
	}
}

func TestMCPUpgradePreflightIsNotPartialMutation(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "demo", release.StatusDeployed, 2)
	_, _, err := newChartSourceTestClient(t).runValuesUpgrade(cfg, "demo", "", "", nil, valuesUpgradeOptions{expectedRevision: 1}, progressSender(nil))
	var preflight *OperationPreflightError
	if !errors.As(err, &preflight) || !strings.Contains(err.Error(), "expected 1") {
		t.Fatalf("stale refusal not classified: %v", err)
	}
	if _, err := getReleaseWith(cfg, "", "default", "demo", nil); err == nil || !strings.Contains(err.Error(), "no usable") {
		t.Fatalf("corrupt release not safely refused: %v", err)
	}
}

func TestStoredChartRenderedDependencyAliases(t *testing.T) {
	parent := &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0", Dependencies: []*chart.Dependency{{Name: "child", Alias: "alias", Version: "1.0.0"}}}}
	parent.AddDependency(&chart.Chart{Metadata: &chart.Metadata{Name: "child", Version: "1.0.0"}})
	rel := &release.Release{Manifest: "# Source: fixture/charts/alias/templates/cm.yaml\n"}
	if err := validateStoredChartDependencyBodies(parent, rel); err != nil {
		t.Fatalf("raw package alias rejected: %v", err)
	}
}

func TestStoredChartDependencyEvidenceDoesNotVetoVersionChange(t *testing.T) {
	target := &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "2.0.0"}}
	baseline := &release.Release{Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0"}}, Manifest: "# Source: fixture/charts/removed/templates/cm.yaml\n"}
	if err := validateStoredChartDependencyBodies(target, baseline); err != nil {
		t.Fatalf("previous version prevents deliberate chart change: %v", err)
	}
}

func TestMCPNotesValuesRejectCorruptHistoricalChart(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "demo", release.StatusSuperseded, 1)
	seedRelease(t, cfg, "demo", release.StatusDeployed, 2)
	latest, _ := cfg.Releases.Get("demo", 2)
	latest.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0"}}
	if err := cfg.Releases.Update(latest); err != nil {
		t.Fatal(err)
	}
	if _, err := getValuesWith(cfg, "demo", true, 1); err == nil || !strings.Contains(err.Error(), "revision 1") {
		t.Fatalf("corrupt historical chart not refused: %v", err)
	}
	if _, err := getValuesWith(cfg, "demo", true, 2); err != nil {
		t.Fatalf("valid latest chart refused: %v", err)
	}
}

func TestMCPPreviewExpandsListDocuments(t *testing.T) {
	ch := &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0"}}
	old := `apiVersion: v1
kind: ConfigMapList
items:
- apiVersion: v1
  kind: ConfigMap
  metadata: {name: removed}
- apiVersion: v1
  kind: ConfigMap
  metadata: {name: changed}
  data: {message: old}
`
	next := `apiVersion: v1
kind: List
items:
- apiVersion: v1
  kind: List
  items:
  - apiVersion: v1
    kind: ConfigMap
    metadata: {name: added}
- apiVersion: v1
  kind: ConfigMap
  metadata: {name: changed}
  data: {message: new}
`
	before := &release.Release{Chart: ch, Version: 1, Namespace: "default", Manifest: old}
	after := &release.Release{Chart: ch, Version: 2, Namespace: "default", Manifest: next}
	got, err := mcpUpgradeResult(before, after, true)
	if err != nil || got.ParseErrors != 0 || len(got.Added) != 1 || len(got.Removed) != 1 || len(got.Changed) != 1 {
		t.Fatalf("list changes lost: %+v %v", got, err)
	}
	if got.Added[0].Name != "added" || got.Removed[0].Name != "removed" || got.Changed[0].Name != "changed" {
		t.Fatalf("wrong list identities: %+v", got)
	}
	if refs := parseManifestResources(old, "default"); len(refs) != 2 {
		t.Fatalf("uninstall preview lost list contents: %v", refs)
	}
	for _, malformed := range []string{`kind: ConfigMap`, `apiVersion: v1
kind: List
items: broken`, `apiVersion: v1
kind: List
items: [42]`} {
		_, errors := parseManifestResourceObjects(malformed, "default")
		if errors == 0 {
			t.Fatalf("unidentified document counted complete: %s", malformed)
		}
	}
}

func TestManifestCommentDocumentsAndTypedListIdentity(t *testing.T) {
	for _, manifest := range []string{"# Source: fixture/templates/disabled.yaml\n# Copyright fixture", "null", "~", "apiVersion: v1\nkind: List\nitems:\n", "apiVersion: v1\nkind: ConfigMapList\nitems: null\n"} {
		objects, failures := parseManifestResourceObjects(manifest, "default")
		if len(objects) != 0 || failures != 0 {
			t.Fatalf("empty document counted incomplete: %q: %v %d", manifest, objects, failures)
		}
	}
	for _, tc := range []struct {
		name, fields string
		wantErrors   int
	}{
		{"inherited", "", 0},
		{"explicit", "  kind: ConfigMap\n  apiVersion: v1\n", 0},
		{"partial kind", "  kind: ConfigMap\n", 1},
		{"partial version", "  apiVersion: v1\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects, failures := parseManifestResourceObjects("apiVersion: v1\nkind: ConfigMapList\nitems:\n- metadata: {name: inherited}\n"+tc.fields, "default")
			if failures != tc.wantErrors {
				t.Fatalf("failures=%d want %d", failures, tc.wantErrors)
			}
			if tc.wantErrors == 0 && (len(objects) != 1 || objects[0].Ref.Kind != "ConfigMap" || objects[0].Ref.APIVersion != "v1" || objects[0].Ref.Namespace != "default") {
				t.Fatalf("wrong inherited identity: %+v", objects)
			}
		})
	}
	objects, failures := parseManifestResourceObjects("apiVersion: v1\nkind: List\nitems:\n- metadata: {name: ambiguous}\n", "default")
	if failures != 1 || len(objects) != 0 {
		t.Fatalf("generic List invented an item kind: %+v %d", objects, failures)
	}
}

func TestMCPPreviewMatchesIdentityAcrossAPIVersions(t *testing.T) {
	ch := &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0"}}
	manifest := "apiVersion: autoscaling/v2beta2\nkind: HorizontalPodAutoscaler\nmetadata: {name: app, namespace: default}\nspec: {minReplicas: 1}\n"
	for _, tc := range []struct {
		name, next string
		changed    bool
	}{
		{"version", strings.ReplaceAll(manifest, "autoscaling/v2beta2", "autoscaling/v2"), true},
		{"group", strings.ReplaceAll(manifest, "autoscaling/v2beta2", "other.example/v2"), false},
		{"namespace", strings.ReplaceAll(manifest, "namespace: default", "namespace: other"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mcpUpgradeResult(&release.Release{Chart: ch, Version: 1, Namespace: "default", Manifest: manifest}, &release.Release{Chart: ch, Version: 2, Namespace: "default", Manifest: tc.next}, true)
			if err != nil || got.ParseErrors != 0 {
				t.Fatalf("preview failed: %+v %v", got, err)
			}
			if tc.changed {
				if len(got.Added) != 0 || len(got.Removed) != 0 || len(got.Changed) != 1 || got.Changed[0].APIVersion != "autoscaling/v2" {
					t.Fatalf("API version change misclassified: %+v", got)
				}
			} else if len(got.Added) != 1 || len(got.Removed) != 1 || len(got.Changed) != 0 {
				t.Fatalf("different identity collapsed: %+v", got)
			}
		})
	}
}

func TestMCPNotesDiffRedactsBeforeAddingLinePrefixes(t *testing.T) {
	secrets := fake.NewClientset().CoreV1().Secrets("default")
	store := driver.NewSecrets(secrets)
	credential := "first-private-line\nsecond-private-line"
	for revision, word := range []string{"before", "after"} {
		rel := &release.Release{Name: "demo", Namespace: "default", Version: revision + 1, Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0"}}, Config: map[string]any{"password": credential}, Info: &release.Info{Status: release.StatusDeployed, Notes: word + "\n" + credential}}
		if err := store.Create("sh.helm.release.v1.demo.v"+string(rune('1'+revision)), rel); err != nil {
			t.Fatal(err)
		}
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected mutation %s", r.Method)
		}
		if r.URL.Path == "/version" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"gitVersion": "v1.36.2"})
			return
		}
		secret, err := secrets.Get(r.Context(), path.Base(r.URL.Path), metav1.GetOptions{})
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		secret.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(secret)
	}))
	defer api.Close()
	client := NewStandaloneClient(&rest.Config{Host: api.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	result, err := client.GetRedactedNotesDiffAsUser("default", "demo", 1, 2, nil, func(notes string, values map[string]any) string {
		secret, _ := values["password"].(string)
		if secret != "" {
			return strings.ReplaceAll(notes, secret, "[REDACTED]")
		}
		return notes
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Diff, "private-line") || !strings.Contains(result.Diff, "[REDACTED]") || !strings.Contains(result.Diff, "after") {
		t.Fatalf("credential escaped diff masking: %s", result.Diff)
	}
}

func TestMCPFreshInstallErrorIsPreflight(t *testing.T) {
	_, err := newChartSourceTestClient(t).installFreshWith(memoryActionConfig(t), &InstallRequest{ReleaseName: "demo", Namespace: "default", ChartName: "fixture", Repository: "https://unconfigured.test", Version: "1.0.0"})
	var preflight *OperationPreflightError
	if !errors.As(err, &preflight) {
		t.Fatalf("source refusal not classified: %v", err)
	}
}
