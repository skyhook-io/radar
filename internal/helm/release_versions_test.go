package helm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
)

// releaseVersionsFixture serves a classic repository whose index offers
// versions, configures it as "probe", seeds a stale cached index that offers
// only 1.0.0, and installs release demo from chart radar 1.0.0.
func releaseVersionsFixture(t *testing.T, versions []string, bind bool) (*Client, *action.Configuration, *httptest.Server) {
	t.Helper()
	withOCISources(t, nil)
	c := newChartSourceTestClient(t)
	var index strings.Builder
	index.WriteString("apiVersion: v1\nentries:\n  radar:\n")
	for _, v := range versions {
		fmt.Fprintf(&index, "  - apiVersion: v2\n    name: radar\n    version: %s\n    urls: [radar-%s.tgz]\n", v, v)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(index.String()))
	}))
	t.Cleanup(srv.Close)
	f := repo.NewFile()
	f.Update(&repo.Entry{Name: "probe", URL: srv.URL})
	if err := f.WriteFile(c.settings.RepositoryConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	stale := "apiVersion: v1\nentries:\n  radar:\n  - apiVersion: v2\n    name: radar\n    version: 1.0.0\n    urls: [radar-1.0.0.tgz]\n"
	if err := os.WriteFile(filepath.Join(c.settings.RepositoryCache, "probe-index.yaml"), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "demo", release.StatusDeployed, 1)
	rel, err := cfg.Releases.Last("demo")
	if err != nil {
		t.Fatal(err)
	}
	rel.Chart = sourceTestChart("installed")
	if bind {
		bindTestRelease(rel, ChartSourceCandidate{Type: "repository", Reference: "probe", URL: srv.URL})
	}
	if err := cfg.Releases.Update(rel); err != nil {
		t.Fatal(err)
	}
	return c, cfg, srv
}

func TestFreshReleaseVersionsReadsCurrentIndexNotCache(t *testing.T) {
	c, cfg, _ := releaseVersionsFixture(t, []string{"1.0.0", "2.0.0", "1.1.0"}, true)
	got, err := c.freshReleaseVersionsWith(cfg, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Versions, ",") != "2.0.0,1.1.0,1.0.0" || got.Truncated {
		t.Fatalf("want the served index newest first, not the stale cache: %+v", got)
	}
	if got.Source.Type != "repository" || got.Source.Reference != "probe" {
		t.Fatalf("source must be the release's confirmed source: %+v", got.Source)
	}
	// Agents read the JSON; a complete list must still say it is complete.
	wire, err := json.Marshal(got)
	if err != nil || !strings.Contains(string(wire), `"truncated":false`) {
		t.Fatalf("truncated must always be serialized: %s %v", wire, err)
	}
}

func TestFreshRepositoryIndexHonorsByteCap(t *testing.T) {
	c, _, srv := releaseVersionsFixture(t, []string{"1.0.0", "2.0.0"}, true)
	source := ChartSourceCandidate{Type: "repository", Reference: "probe", URL: srv.URL}
	if _, _, err := c.fetchSelectedRepositoryIndex(source, t.TempDir(), freshVersionsTimeout, 64); err == nil || !strings.Contains(err.Error(), "exceeds 64 bytes") {
		t.Fatalf("an index over the cap must be refused before parsing: %v", err)
	}
	if _, index, err := c.fetchSelectedRepositoryIndex(source, t.TempDir(), freshVersionsTimeout, maxPreparedIndexBytes); err != nil || len(index.Entries["radar"]) != 2 {
		t.Fatalf("an index under the cap must load: %v", err)
	}
}

func TestFreshReleaseVersionsMarksTruncation(t *testing.T) {
	var versions []string
	for i := 0; i < maxAvailableVersions+10; i++ {
		versions = append(versions, fmt.Sprintf("1.%d.0", i))
	}
	c, cfg, _ := releaseVersionsFixture(t, versions, true)
	got, err := c.freshReleaseVersionsWith(cfg, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Versions) != maxAvailableVersions || !got.Truncated || got.Versions[0] != fmt.Sprintf("1.%d.0", maxAvailableVersions+9) {
		t.Fatalf("want newest %d with truncation marked: %d %v %v", maxAvailableVersions, len(got.Versions), got.Truncated, got.Versions[:1])
	}
}

func TestFreshReleaseVersionsExplainsMissingSource(t *testing.T) {
	c, cfg, _ := releaseVersionsFixture(t, []string{"1.0.0", "2.0.0"}, false)
	got, err := c.freshReleaseVersionsWith(cfg, "demo")
	if err == nil || !strings.Contains(err.Error(), "no usable chart source") {
		t.Fatalf("an unconfirmed source must be an error, not an empty list: %+v %v", got, err)
	}
}

func TestFreshReleaseVersionsReportsUnreachableSource(t *testing.T) {
	c, cfg, srv := releaseVersionsFixture(t, []string{"1.0.0"}, true)
	srv.Close()
	got, err := c.freshReleaseVersionsWith(cfg, "demo")
	if !errors.Is(err, errSourceUnavailable) {
		t.Fatalf("an unreachable source must not fall back to the stale cache: %+v %v", got, err)
	}
}

func TestChartVersionUnavailableMarksMissingVersionsOnly(t *testing.T) {
	c, _, srv := releaseVersionsFixture(t, []string{"1.0.0"}, true)
	source := ChartSourceCandidate{Type: "repository", Reference: "probe", URL: srv.URL}
	// Not in the index.
	if _, _, err := c.loadSourceChart(nil, source, "radar", "9.9.9"); !ChartVersionUnavailable(err) {
		t.Fatalf("missing version not marked: %v", err)
	}
	// In the index but the package can't be fetched, which is how OCI reports a missing tag.
	_, _, err := c.loadSourceChart(nil, source, "radar", "1.0.0")
	if !ChartVersionUnavailable(err) || !strings.Contains(err.Error(), "download selected chart") {
		t.Fatalf("failed download not marked or message changed: %v", err)
	}
	// Refusals unrelated to the version stay unmarked.
	if ChartVersionUnavailable(errSourceConflict) || ChartVersionUnavailable(errSourceRequired) {
		t.Fatal("source refusals must not suggest listing versions")
	}
}
