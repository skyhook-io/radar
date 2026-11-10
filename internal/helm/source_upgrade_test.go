package helm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
)

func sourceTestChart(marker string) *chart.Chart {
	return &chart.Chart{Metadata: &chart.Metadata{Name: "radar", Version: "1.0.0", APIVersion: "v2"}, Templates: []*chart.File{{Name: "templates/configmap.yaml", Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + marker + "\ndata:\n  publisher: " + marker + "\n")}}}
}

func bindTestRelease(rel *release.Release, source ChartSourceCandidate) {
	rel.Labels = chartSourceLabels(&source)
	rel.Labels[chartSourceBindingLabel] = chartSourceBinding(rel.Chart)
}

func sourceTestFixture(t *testing.T) (*Client, *action.Configuration, SourceUpgradeSelection) {
	t.Helper()
	withOCISources(t, nil)
	c := newChartSourceTestClient(t)
	oldChart, newChart := sourceTestChart("official"), sourceTestChart("other")
	srv := preparedChartServer(t, newChart)
	t.Cleanup(srv.Close)
	alias, err := c.ensureClassicRepository(srv.URL, "other")
	if err != nil {
		t.Fatal(err)
	}
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "demo", release.StatusDeployed, 1)
	rel, err := cfg.Releases.Last("demo")
	if err != nil {
		t.Fatal(err)
	}
	rel.Chart = oldChart
	rel.Manifest = string(oldChart.Templates[0].Data)
	oldSource := ChartSourceCandidate{Type: "repository", Reference: "official", URL: "https://official.example.test/charts"}
	bindTestRelease(rel, oldSource)
	if err := cfg.Releases.Update(rel); err != nil {
		t.Fatal(err)
	}
	return c, cfg, SourceUpgradeSelection{Source: ChartSourceCandidate{Type: "repository", Reference: alias, URL: srv.URL}, ExpectedRevision: 1, ExpectedSource: &oldSource}
}

func TestSourceBindingSurvivesStorageButRejectsExternalChartChanges(t *testing.T) {
	ch := sourceTestChart("official")
	ch.Metadata.Sources = []string{}
	ch.Metadata.Keywords = []string{}
	ch.Metadata.Annotations = map[string]string{}
	rel := &release.Release{Name: "binding", Namespace: "default", Version: 1, Info: &release.Info{Status: release.StatusDeployed}, Chart: ch}
	bindTestRelease(rel, ChartSourceCandidate{Type: "repository", Reference: "a", URL: "https://example.test/charts"})
	client := k8sfake.NewSimpleClientset()
	store := storage.Init(driver.NewSecrets(client.CoreV1().Secrets("default")))
	if err := store.Create(rel); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Last(rel.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := confirmedChartSource(stored); !ok {
		t.Fatal("binding did not survive Helm Secret storage")
	}
	stored.Chart.Values = map[string]any{"coalesced": "values"}
	if _, ok := confirmedChartSource(stored); !ok {
		t.Fatal("coalesced values invalidated source binding")
	}
	stored.Chart.Templates[0].Data = []byte("different publisher at the same name and version")
	if _, ok := confirmedChartSource(stored); ok {
		t.Fatal("inherited source labels trusted after chart change")
	}
}

func TestUnrecordedUpgradeRequiresExplicitSourceEvenWithSameVersionCandidate(t *testing.T) {
	c := testHelmClientWithRepoVersions(t, map[string][]string{"bitnami": {"1.0.0", "2.0.0"}})
	rel := &release.Release{Name: "demo", Chart: sourceTestChart("official")}
	rel.Chart.Metadata.Name = "argo-cd"
	_, err := c.loadTargetChart(nil, rel, "2.0.0", "bitnami", func(string, string, string) {})
	if !errors.Is(err, errSourceRequired) {
		t.Fatalf("unrecorded source: %v", err)
	}
}

func TestSourceUpgradeSameVersionPreviewsSelectedPublisherAndPreservesHistory(t *testing.T) {
	c, cfg, selection := sourceTestFixture(t)
	values := map[string]any{}
	preview, err := c.sourceUpgradeWith(cfg, "demo", "1.0.0", values, selection, true, func(string, string, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.ManifestDiff, "other") || !strings.Contains(preview.ManifestDiff, "official") || preview.PreviewDigest == "" || preview.BaselineRevision != 1 {
		t.Fatalf("wrong preview: %+v", preview)
	}
	latest, _ := cfg.Releases.Last("demo")
	if latest.Version != 1 {
		t.Fatal("preview created a revision")
	}
	selection.PreviewDigest = preview.PreviewDigest
	if _, err := c.sourceUpgradeWith(cfg, "demo", "1.0.0", values, selection, false, func(string, string, string) {}); err != nil {
		t.Fatal(err)
	}
	latest, _ = cfg.Releases.Last("demo")
	source, ok := confirmedChartSource(latest)
	if !ok || !sameChartSource(*source, selection.Source) || !strings.Contains(latest.Manifest, "other") {
		t.Fatalf("wrong applied source/revision: %+v %v", source, ok)
	}
	original, _ := cfg.Releases.Get("demo", 1)
	if source, _ := chartSourceFromRelease(original); source.URL != selection.ExpectedSource.URL {
		t.Fatal("previous revision source rewritten")
	}
}

func TestSourceUpgradeRejectsStaleValuesAndRevisionBeforeWriting(t *testing.T) {
	for _, kind := range []string{"values", "revision", "package", "generation"} {
		t.Run(kind, func(t *testing.T) {
			c, cfg, selection := sourceTestFixture(t)
			values := map[string]any{"setting": "before"}
			preview, err := c.sourceUpgradeWith(cfg, "demo", "1.0.0", values, selection, true, func(string, string, string) {})
			if err != nil {
				t.Fatal(err)
			}
			selection.PreviewDigest = preview.PreviewDigest
			switch kind {
			case "values":
				values["setting"] = "after"
			case "revision":
				selection.ExpectedRevision = 2
			case "generation":
				rel, _ := cfg.Releases.Last("demo")
				rel.Info.FirstDeployed = rel.Info.FirstDeployed.Add(time.Hour)
				if err := cfg.Releases.Update(rel); err != nil {
					t.Fatal(err)
				}
			case "package":
				file, err := chartutil.Save(sourceTestChart("republished"), t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				c.chartPathLocator = func(*action.Configuration, string, string) (string, error) { return file, nil }
			}
			_, err = c.sourceUpgradeWith(cfg, "demo", "1.0.0", values, selection, false, func(string, string, string) {})
			if !errors.Is(err, errSourceConflict) {
				t.Fatalf("stale %s accepted: %v", kind, err)
			}
			latest, _ := cfg.Releases.Last("demo")
			if latest.Version != 1 {
				t.Fatal("stale preview wrote a revision")
			}
		})
	}
}

func TestFailedSourceSwitchBlocksNormalAndValuesOnlyWrites(t *testing.T) {
	c, cfg, selection := sourceTestFixture(t)
	seedRelease(t, cfg, "demo", release.StatusFailed, 2)
	failed, _ := cfg.Releases.Last("demo")
	failed.Chart = sourceTestChart("other")
	failed.Manifest = string(failed.Chart.Templates[0].Data)
	bindTestRelease(failed, selection.Source)
	if err := cfg.Releases.Update(failed); err != nil {
		t.Fatal(err)
	}
	for _, write := range []struct {
		name string
		run  func() error
	}{
		{"normal", func() error { return c.upgradeWith(cfg, "demo", "1.0.0", "", func(string, string, string) {}) }},
		{"values", func() error { return c.applyValuesWith(cfg, "demo", map[string]any{}) }},
	} {
		if err := write.run(); !errors.Is(err, errSourceRequired) {
			t.Fatalf("%s bypassed failed switch: %v", write.name, err)
		}
	}
	selection.ExpectedRevision = 2
	selection.ExpectedSource = &selection.Source
	preview, err := c.sourceUpgradeWith(cfg, "demo", "1.0.0", map[string]any{}, selection, true, func(string, string, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if preview.BaselineRevision != 1 || !strings.Contains(preview.ManifestDiff, "official") {
		t.Fatalf("preview used failed attempt as baseline: %+v", preview)
	}
}

func TestSourceAssociationRejectsSameVersionDifferentPackage(t *testing.T) {
	c, cfg, selection := sourceTestFixture(t)
	rel, _ := cfg.Releases.Last("demo")
	rel.Labels = nil
	if err := cfg.Releases.Update(rel); err != nil {
		t.Fatal(err)
	}
	if err := c.setSourceWith(cfg, "demo", selection.Source); !errors.Is(err, errInvalidChartSource) {
		t.Fatalf("wrong publisher was associated: %v", err)
	}
	rel, _ = cfg.Releases.Last("demo")
	if _, ok := chartSourceFromRelease(rel); ok {
		t.Fatal("failed association changed release")
	}
}

func TestSourceDownloadsWithSameNameAndVersionRemainIsolated(t *testing.T) {
	withOCISources(t, nil)
	c := newChartSourceTestClient(t)
	var sources []ChartSourceCandidate
	for _, name := range []string{"first", "second"} {
		srv := preparedChartServer(t, sourceTestChart(name))
		t.Cleanup(srv.Close)
		if _, err := c.ensureClassicRepository(srv.URL, name); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, ChartSourceCandidate{Type: "repository", Reference: name, URL: srv.URL})
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		for _, source := range sources {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ch, _, err := c.loadSourceChart(nil, source, "radar", "1.0.0")
				if err != nil {
					t.Error(err)
					return
				}
				if !strings.Contains(string(ch.Templates[0].Data), "publisher: "+source.Reference) {
					t.Errorf("publisher swapped for %s", source.Reference)
				}
			}()
		}
	}
	wg.Wait()
}

func TestFailedUntrackedReleaseStillAllowsValuesRepair(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "demo", release.StatusDeployed, 1)
	first, _ := cfg.Releases.Last("demo")
	first.Chart = sourceTestChart("original")
	first.Manifest = string(first.Chart.Templates[0].Data)
	if err := cfg.Releases.Update(first); err != nil {
		t.Fatal(err)
	}
	seedRelease(t, cfg, "demo", release.StatusFailed, 2)
	failed, _ := cfg.Releases.Last("demo")
	failed.Chart = sourceTestChart("original")
	failed.Manifest = first.Manifest
	if err := cfg.Releases.Update(failed); err != nil {
		t.Fatal(err)
	}
	c := newChartSourceTestClient(t)
	if err := c.applyValuesWith(cfg, "demo", map[string]any{"repair": true}); err != nil {
		t.Fatalf("ordinary values repair blocked: %v", err)
	}
}

func TestSourceDigestIgnoresLocalAliasNames(t *testing.T) {
	source := ChartSourceCandidate{Type: "repository", Reference: "first-machine", URL: "https://example.test/charts"}
	selection := SourceUpgradeSelection{Source: source, ExpectedRevision: 1, ExpectedSource: &source}
	first, err := sourcePreviewDigest(selection, "1.0.0", "archive", "revision", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	selection.Source.Reference = "second-machine"
	renamed := source
	renamed.Reference = "another-local-alias"
	selection.ExpectedSource = &renamed
	second, err := sourcePreviewDigest(selection, "1.0.0", "archive", "revision", map[string]any{})
	if err != nil || first != second {
		t.Fatalf("local aliases changed exact-source digest: %v", err)
	}
}

func TestReconstructionSourceRequiredErrorNamesTheRealRecovery(t *testing.T) {
	c := newChartSourceTestClient(t)
	ch := sourceTestChart("original")
	ch.Metadata.Dependencies = []*chart.Dependency{{Name: "missing-child", Version: "1.0.0"}}
	rel := &release.Release{Name: "demo", Chart: ch}
	_, err := c.chartForUpgradeTargetWithLoader(nil, rel, "1.0.0", "", func(string, string, string) {}, func(*action.Configuration, *release.Release, string, string, func(string, string, string)) (*chart.Chart, error) {
		return nil, errSourceRequired
	})
	if err != errSourceRequired {
		t.Fatalf("source choice was hidden by misleading no-repository error: %v", err)
	}
}

func TestFailedInstallRecoveryClearsUnrecordableSource(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "demo", release.StatusFailed, 1)
	old, _ := cfg.Releases.Last("demo")
	old.Chart = sourceTestChart("original")
	old.Manifest = string(old.Chart.Templates[0].Data)
	bindTestRelease(old, ChartSourceCandidate{Type: "repository", Reference: "old", URL: "https://old.example.test"})
	if err := cfg.Releases.Update(old); err != nil {
		t.Fatal(err)
	}
	rel, err := runInstallOrUpgrade(cfg, &InstallRequest{ReleaseName: "demo", Namespace: "default", Repository: "https://private.example.test/chart.tgz", Values: map[string]any{}}, sourceTestChart("replacement"), installUpgrade)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := chartSourceFromRelease(rel); ok || rel.Labels[chartSourceBindingLabel] != "" {
		t.Fatal("old source carried onto explicit unrecordable install")
	}
}

func TestPendingSourceStatusPreservesRecordedSource(t *testing.T) {
	c, cfg, _ := sourceTestFixture(t)
	rel, _ := cfg.Releases.Last("demo")
	rel.Info.Status = release.StatusPendingUpgrade
	if err := cfg.Releases.Update(rel); err != nil {
		t.Fatal(err)
	}
	status, err := c.sourceStatusWith(cfg, "demo")
	if err != nil || !status.Changing || status.Recorded == nil {
		t.Fatalf("pending source was hidden: %+v %v", status, err)
	}
	ctx, err := releaseReadContext(cfg, "demo")
	if err != nil {
		t.Fatal(err)
	}
	_, issue, _ := upgradeSourceState(ctx)
	if issue != UpgradeSourceIssueChanging {
		t.Fatalf("pending operation misclassified: %s", issue)
	}
}

func TestUnconfirmedOCIOriginalSourceCanBeVerified(t *testing.T) {
	withOCISources(t, []string{"oci://registry.example.test/charts"})
	c := newChartSourceTestClient(t)
	ch := sourceTestChart("oci")
	file, err := chartutil.Save(ch, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c.chartPathLocator = func(_ *action.Configuration, ref, version string) (string, error) {
		if ref != "oci://registry.example.test/charts/radar" || version != "1.0.0" {
			t.Errorf("incorrect source resolution: %s %s", ref, version)
		}
		return file, nil
	}
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "demo", release.StatusDeployed, 1)
	rel, _ := cfg.Releases.Last("demo")
	rel.Chart = ch
	if err := cfg.Releases.Update(rel); err != nil {
		t.Fatal(err)
	}
	selected := ChartSourceCandidate{Type: "oci", Reference: "oci://registry.example.test/charts/radar"}
	if err := c.setSourceWith(cfg, "demo", selected); err != nil {
		t.Fatal(err)
	}
	latest, _ := cfg.Releases.Last("demo")
	source, ok := confirmedChartSource(latest)
	if !ok || !sameChartSource(*source, selected) {
		t.Fatal("OCI original source was not confirmed")
	}
}

func TestSelectedRepositoryCanRecoverItsMissingIndex(t *testing.T) {
	c := newChartSourceTestClient(t)
	srv := preparedChartServer(t, sourceTestChart("refresh"))
	defer srv.Close()
	if _, err := c.ensureClassicRepository(srv.URL, "fixture"); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(c.settings.RepositoryCache, "fixture-index.yaml")
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateRepository("fixture"); err != nil {
		t.Fatal(err)
	}
	versions, err := c.sourceVersions(ChartSourceCandidate{Type: "repository", Reference: "fixture", URL: srv.URL}, "radar")
	if err != nil || len(versions) != 1 || versions[0] != "1.0.0" {
		t.Fatalf("source index not recovered: %v %v", versions, err)
	}
}

func TestFailedSourceAssociationDoesNotImplySourceSwitch(t *testing.T) {
	baseline := &release.Release{Info: &release.Info{Status: release.StatusDeployed}, Chart: sourceTestChart("original")}
	latest := &release.Release{Info: &release.Info{Status: release.StatusFailed}, Chart: sourceTestChart("original")}
	bindTestRelease(latest, ChartSourceCandidate{Type: "repository", Reference: "original", URL: "https://example.test/charts"})
	op := releaseSourceContext{Latest: latest, Baseline: baseline}
	if failedSourceChange(op) {
		t.Fatal("recording original source invented a failed source change")
	}
	latest.Labels[chartSourceAttemptLabel] = "true"
	if !failedSourceChange(op) {
		t.Fatal("explicit failed source attempt was not detected")
	}
}

func TestSourceUpgradeRoutesRequireExplicitSelection(t *testing.T) {
	for _, dedicated := range []bool{false, true} {
		for _, selected := range []bool{false, true} {
			route := "/releases/{namespace}/{name}/preview"
			path := "/releases/default/source/preview"
			if dedicated {
				route = "/releases/{namespace}/{name}/source/preview"
				path = "/releases/default/source/source/preview"
			}
			router := chi.NewRouter()
			router.Post(route, func(w http.ResponseWriter, r *http.Request) {
				req := ApplyValuesRequest{}
				if selected {
					req.SourceUpgrade = &SourceUpgradeSelection{}
				}
				if validSourceUpgradeRoute(w, r, req) {
					w.WriteHeader(http.StatusNoContent)
				}
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
			expected := http.StatusBadRequest
			if dedicated == selected {
				expected = http.StatusNoContent
			}
			if response.Code != expected {
				t.Fatalf("dedicated=%v selected=%v status=%d", dedicated, selected, response.Code)
			}
		}
	}
}

func TestFailedExplicitSourceAttemptWithoutDeployedRevisionRequiresRetry(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "demo", release.StatusFailed, 1)
	seedRelease(t, cfg, "demo", release.StatusFailed, 2)
	latest, _ := cfg.Releases.Last("demo")
	latest.Chart = sourceTestChart("attempted")
	bindTestRelease(latest, ChartSourceCandidate{Type: "repository", Reference: "attempted", URL: "https://example.test/charts"})
	latest.Labels[chartSourceAttemptLabel] = "true"
	if err := cfg.Releases.Update(latest); err != nil {
		t.Fatal(err)
	}
	op, err := releaseContext(cfg, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !failedSourceChange(op) {
		t.Fatal("failed explicit attempt compared with itself and bypassed source selection")
	}
	c := newChartSourceTestClient(t)
	if err := c.applyValuesWith(cfg, "demo", map[string]any{}); !errors.Is(err, errSourceRequired) {
		t.Fatalf("values retry: %v", err)
	}
	if _, err := c.normalUpgradeSource(cfg, latest, ""); !errors.Is(err, errSourceRequired) {
		t.Fatalf("normal retry: %v", err)
	}
}

func TestPreparedUpgradeRefreshesOrClearsSourceBinding(t *testing.T) {
	for _, recordable := range []bool{true, false} {
		cfg := memoryActionConfig(t)
		seedRelease(t, cfg, "demo", release.StatusDeployed, 1)
		old, _ := cfg.Releases.Last("demo")
		old.Chart = sourceTestChart("old")
		old.Labels = map[string]string{"owner-team": "example"}
		for key, value := range boundChartSourceLabels(nil, &ChartSourceCandidate{Type: "repository", Reference: "old", URL: "https://old.example.test/charts"}, old.Chart) {
			if value != "null" {
				old.Labels[key] = value
			}
		}
		if err := cfg.Releases.Update(old); err != nil {
			t.Fatal(err)
		}
		ch := sourceTestChart("new")
		req := InstallRequest{Namespace: old.Namespace, ReleaseName: old.Name}
		if recordable {
			req.resolvedSource = &ChartSourceCandidate{Type: "repository", Reference: "new", URL: "https://new.example.test/charts"}
		}
		upgrade := preparedUpgradeAction(cfg, &req, ch, old.Labels)
		rel, err := upgrade.Run("demo", ch, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		source, confirmed := confirmedChartSource(rel)
		if confirmed != recordable {
			t.Fatalf("recordable=%v confirmed=%v", recordable, confirmed)
		}
		if confirmed && source.Reference != "new" {
			t.Fatalf("stale source: %+v", source)
		}
		if !recordable {
			if _, ok := chartSourceFromRelease(rel); ok {
				t.Fatal("unrecordable source inherited stale provenance")
			}
		}
		if rel.Labels["owner-team"] != "example" {
			t.Fatal("unrelated Helm label removed")
		}
	}
}

func TestUnavailableSourceVersionsReturnServiceUnavailable(t *testing.T) {
	c := testHelmClientWithRepoConfigOnly(t)
	_, err := c.sourceVersions(ChartSourceCandidate{Type: "repository", Reference: "broken", URL: "https://charts.example.invalid"}, "app")
	if !errors.Is(err, errSourceUnavailable) || chartSourceErrorStatus(err) != http.StatusServiceUnavailable {
		t.Fatalf("unavailable index: %v, status %d", err, chartSourceErrorStatus(err))
	}
	response := httptest.NewRecorder()
	writeChartSourceOperationError(response, errSourceRequired, "preview", "default", "demo")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"errorCode":"chart_source_required"`) {
		t.Fatalf("source error envelope: %d %s", response.Code, response.Body.String())
	}
}
