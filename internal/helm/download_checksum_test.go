package helm

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/repo"
)

func TestRepositoryChartChecksum(t *testing.T) {
	for _, digest := range []string{"wrong", "malformed", "whitespace", "valid", "absent"} {
		for _, consumer := range []string{"source", "install", "progress", "detail", "direct-install", "direct-progress"} {
			t.Run(digest+"/"+consumer, func(t *testing.T) {
				c := newChartSourceTestClient(t)
				path, err := chartutil.Save(sourceTestChart("publisher"), t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				advertised := ""
				switch digest {
				case "wrong":
					advertised = strings.Repeat("0", 64)
				case "malformed":
					advertised = "not-sha256"
				case "whitespace":
					advertised = "   "
				case "valid":
					advertised = fmt.Sprintf("%x", sha256.Sum256(body))
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/index.yaml" {
						fmt.Fprintf(w, "apiVersion: v1\nentries:\n  radar:\n  - apiVersion: v2\n    name: radar\n    version: 1.0.0\n    urls: [radar.tgz]\n    digest: %q\n", advertised)
					} else {
						w.Write(body)
					}
				}))
				defer srv.Close()
				f := repo.NewFile()
				f.Update(&repo.Entry{Name: "probe", URL: srv.URL})
				if err := f.WriteFile(c.settings.RepositoryConfig, 0600); err != nil {
					t.Fatal(err)
				}
				if err := c.UpdateRepository("probe"); err != nil {
					t.Fatal(err)
				}
				cfg := memoryActionConfig(t)
				req := &InstallRequest{ReleaseName: "checksum-test", Namespace: "default", Repository: "probe", ChartName: "radar", Version: "1.0.0"}
				if strings.HasPrefix(consumer, "direct-") {
					req.Repository = srv.URL
				}
				switch consumer {
				case "source":
					_, _, err = c.loadSourceChart(nil, ChartSourceCandidate{Type: "repository", Reference: "probe", URL: srv.URL}, "radar", "1.0.0")
				case "install", "direct-install":
					_, err = c.installWith(cfg, req)
				case "progress", "direct-progress":
					_, err = c.installWithProgressUsing(cfg, req, make(chan InstallProgress, 100))
				case "detail":
					_, err = c.GetChartDetail("probe", "radar", "1.0.0")
				}
				if digest == "wrong" || digest == "malformed" || digest == "whitespace" {
					if err == nil || !strings.Contains(err.Error(), "digest") {
						t.Fatalf("expected digest rejection, got %v", err)
					}
					if consumer == "source" || strings.HasPrefix(consumer, "direct-") {
						if strings.Contains(strings.ToLower(err.Error()), "refresh the repository") {
							t.Fatalf("fresh index must not recommend another refresh: %v", err)
						}
					} else if !strings.Contains(strings.ToLower(err.Error()), "refresh the repository") {
						t.Fatalf("cached index needs actionable refresh guidance: %v", err)
					}
					if consumer == "install" || consumer == "progress" {
						if _, e := cfg.Releases.Last(req.ReleaseName); e == nil {
							t.Fatal("rejected chart created release")
						}
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestChecksumFailurePreservesRelease(t *testing.T) {
	for _, operation := range []string{"source", "upgrade", "preview", "apply", "values", "preview-values"} {
		t.Run(operation, func(t *testing.T) {
			c := newChartSourceTestClient(t)
			var bad atomic.Bool
			archives := map[string][]byte{}
			for _, version := range []string{"1.0.0", "2.0.0"} {
				ch := sourceTestChart("publisher")
				ch.Metadata.Version = version
				ch.Metadata.Dependencies = []*chart.Dependency{{Name: "child", Version: "1.0.0", Repository: "file://charts/child"}}
				ch.AddDependency(&chart.Chart{Metadata: &chart.Metadata{Name: "child", Version: "1.0.0", APIVersion: "v2"}})
				path, err := chartutil.Save(ch, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				archives[version], err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/index.yaml" {
					fmt.Fprint(w, "apiVersion: v1\nentries:\n  radar:\n")
					for _, v := range []string{"2.0.0", "1.0.0"} {
						digest := fmt.Sprintf("%x", sha256.Sum256(archives[v]))
						if bad.Load() {
							digest = strings.Repeat("0", 64)
						}
						fmt.Fprintf(w, "  - apiVersion: v2\n    name: radar\n    version: %s\n    urls: [%s]\n    digest: %s\n", v, v, digest)
					}
				} else {
					w.Write(archives[strings.TrimPrefix(r.URL.Path, "/")])
				}
			}))
			defer srv.Close()
			f := repo.NewFile()
			f.Update(&repo.Entry{Name: "probe", URL: srv.URL})
			if err := f.WriteFile(c.settings.RepositoryConfig, 0600); err != nil {
				t.Fatal(err)
			}
			if err := c.UpdateRepository("probe"); err != nil {
				t.Fatal(err)
			}
			cfg := memoryActionConfig(t)
			req := &InstallRequest{ReleaseName: "proof", Namespace: "default", Repository: "probe", ChartName: "radar", Version: "1.0.0"}
			if _, err := c.installWith(cfg, req); err != nil {
				t.Fatal(err)
			}
			before, err := cfg.Releases.Last("proof")
			if err != nil {
				t.Fatal(err)
			}
			source := ChartSourceCandidate{Type: "repository", Reference: "probe", URL: srv.URL}
			selection := SourceUpgradeSelection{Source: source, ExpectedRevision: 1, ExpectedSource: &source}
			if operation == "apply" {
				preview, err := c.sourceUpgradeWith(cfg, "proof", "2.0.0", map[string]any{}, selection, true, func(string, string, string) {})
				if err != nil {
					t.Fatal(err)
				}
				selection.PreviewDigest = preview.PreviewDigest
			}
			if operation == "source" {
				before.Labels = nil
				if err := cfg.Releases.Update(before); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "values" || operation == "preview-values" {
				before.Chart = roundTripStoredChart(t, before.Chart)
				if err := cfg.Releases.Update(before); err != nil {
					t.Fatal(err)
				}
			}
			bad.Store(true)
			switch operation {
			case "source":
				err = c.setSourceWith(cfg, "proof", source)
			case "upgrade":
				err = c.upgradeWith(cfg, "proof", "2.0.0", "probe", func(string, string, string) {})
			case "preview", "apply":
				_, err = c.sourceUpgradeWith(cfg, "proof", "2.0.0", map[string]any{}, selection, operation == "preview", func(string, string, string) {})
			case "values":
				err = c.applyValuesWith(cfg, "proof", map[string]any{})
			case "preview-values":
				_, err = c.previewValuesChangeWith(cfg, "proof", map[string]any{}, "", "")
			}
			if !errors.Is(err, errChartDigest) {
				t.Fatalf("%s: expected typed digest rejection, got %v", operation, err)
			}
			if got := chartSourceErrorStatus(err); got != http.StatusBadGateway {
				t.Fatalf("digest status=%d", got)
			}
			after, err := cfg.Releases.Last("proof")
			if err != nil {
				t.Fatal(err)
			}
			if after.Version != 1 || after.Manifest != before.Manifest {
				t.Fatal("rejected archive changed release")
			}
			if operation == "source" {
				if _, ok := chartSourceFromRelease(after); ok {
					t.Fatal("rejected archive recorded source")
				}
			}
		})
	}
}

func TestDigestInstallErrorClassification(t *testing.T) {
	err := verifyChartDigest([]byte("archive"), strings.Repeat("0", 64))
	got := classifyInstallError(fmt.Errorf("chart radar version 1.0.0: %w", err))
	if got.Status != http.StatusBadGateway || got.Code != "chart_digest_failed" {
		t.Fatalf("classification: %+v", got)
	}
}

func TestHTTPIndexOCIPackageChecksum(t *testing.T) {
	for _, consumer := range []string{"install", "progress", "detail"} {
		t.Run(consumer, func(t *testing.T) {
			c := newChartSourceTestClient(t)
			path, err := chartutil.Save(sourceTestChart("publisher"), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			c.chartPathLocator = func(*action.Configuration, string, string) (string, error) { return path, nil }
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, "apiVersion: v1\nentries:\n  radar:\n  - apiVersion: v2\n    name: radar\n    version: 1.0.0\n    urls: [oci://registry.example.test/charts/radar]\n    digest: %s\n", strings.Repeat("0", 64))
			}))
			defer srv.Close()
			f := repo.NewFile()
			f.Update(&repo.Entry{Name: "probe", URL: srv.URL})
			if err := f.WriteFile(c.settings.RepositoryConfig, 0600); err != nil {
				t.Fatal(err)
			}
			if err := c.UpdateRepository("probe"); err != nil {
				t.Fatal(err)
			}
			req := &InstallRequest{ReleaseName: "proof", Namespace: "default", Repository: "probe", ChartName: "radar", Version: "1.0.0"}
			cfg := memoryActionConfig(t)
			switch consumer {
			case "install":
				_, err = c.installWith(cfg, req)
			case "progress":
				_, err = c.installWithProgressUsing(cfg, req, make(chan InstallProgress, 100))
			case "detail":
				_, err = c.GetChartDetail("probe", "radar", "1.0.0")
			}
			if !errors.Is(err, errChartDigest) {
				t.Fatalf("expected HTTP index checksum rejection: %v", err)
			}
		})
	}
}

func TestDigestResponseHasStableCode(t *testing.T) {
	err := verifyChartDigest([]byte("archive"), strings.Repeat("0", 64))
	rec := httptest.NewRecorder()
	writeChartSourceOperationError(rec, err, "test-source", "default", "proof")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"errorCode":"chart_digest_failed"`) {
		t.Fatalf("source response: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	writeInstallError(rec, err)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"error_code":"chart_digest_failed"`) {
		t.Fatalf("install response: %d %s", rec.Code, rec.Body.String())
	}
}
