package helm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestChartVersionsListsAConfiguredRepositoryBeforeInstall(t *testing.T) {
	c, _, srv := releaseVersionsFixture(t, []string{"1.0.0", "2.1.0-rc.1", "2.0.0"}, false)
	for _, repository := range []string{"probe", srv.URL} {
		got, err := c.ChartVersions(repository, "radar")
		if err != nil {
			t.Fatalf("%s: %v", repository, err)
		}
		if strings.Join(got.Versions, ",") != "2.1.0-rc.1,2.0.0,1.0.0" || got.Latest != "2.0.0" || got.Truncated {
			t.Fatalf("%s: want the fresh index newest first and the newest stable as latest: %+v", repository, got)
		}
		if got.Source.Reference != "probe" {
			t.Fatalf("%s: a configured URL must resolve to its configured repository: %+v", repository, got.Source)
		}
	}
}

func TestChartVersionsLatestSurvivesPrereleasesPastTheCap(t *testing.T) {
	versions := []string{"1.0.0"}
	for i := 0; i < maxAvailableVersions+5; i++ {
		versions = append(versions, fmt.Sprintf("2.0.0-rc.%d", i))
	}
	c, _, _ := releaseVersionsFixture(t, versions, false)
	got, err := c.ChartVersions("probe", "radar")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || slices.Contains(got.Versions, "1.0.0") || got.Latest != "1.0.0" {
		t.Fatalf("the newest stable version must be reported even when prereleases fill the list: %+v", got)
	}
}

func TestChartVersionsReportsAChartTheSourceDoesNotHave(t *testing.T) {
	c, _, _ := releaseVersionsFixture(t, []string{"1.0.0"}, false)
	got, err := c.ChartVersions("probe", "other")
	if err == nil || !strings.Contains(err.Error(), "lists no versions of chart other") {
		t.Fatalf("a chart missing from the source must be an error, not an empty list: %+v %v", got, err)
	}
}

// Only sources the install path accepts are read, and a refused source is
// refused before any request leaves Radar.
func TestChartVersionsRefusesUnconfiguredSourcesWithoutFetching(t *testing.T) {
	c, _, _ := releaseVersionsFixture(t, []string{"1.0.0"}, false)
	hits := 0
	unconfigured := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	t.Cleanup(unconfigured.Close)
	c.ociTags = tagListerFunc(func(ref string) ([]string, error) {
		t.Errorf("an unconfigured OCI source was queried: %s", ref)
		return nil, nil
	})
	for _, repository := range []string{"missing", unconfigured.URL, "https://user:secret@" + strings.TrimPrefix(unconfigured.URL, "http://"), "oci://registry.example/unconfigured"} {
		if got, err := c.ChartVersions(repository, "radar"); err == nil {
			t.Errorf("%s: an unconfigured source must be refused: %+v", repository, got)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: the refusal must not echo credentials: %v", repository, err)
		}
	}
	if hits != 0 {
		t.Fatalf("a refused source must not be fetched; got %d requests", hits)
	}
}

func TestChartVersionsReadsConfiguredOCITags(t *testing.T) {
	withOCISources(t, []string{"oci://registry.example/charts"})
	c := newChartSourceTestClient(t)
	var asked string
	c.ociTags = tagListerFunc(func(ref string) ([]string, error) {
		asked = ref
		return []string{"0.9.0", "1.1.0-beta.1", "1.0.0"}, nil
	})
	got, err := c.ChartVersions("oci://registry.example/charts", "radar")
	if err != nil {
		t.Fatal(err)
	}
	if asked != "registry.example/charts/radar" || strings.Join(got.Versions, ",") != "1.1.0-beta.1,1.0.0,0.9.0" || got.Latest != "1.0.0" || got.Source.Type != "oci" {
		t.Fatalf("want the configured registry's tags for the chart: asked %q, got %+v", asked, got)
	}
}

func TestFreshReleaseVersionsReportsAChartMissingFromItsSource(t *testing.T) {
	c, cfg, _ := releaseVersionsFixture(t, nil, true)
	got, err := c.freshReleaseVersionsWith(cfg, "demo")
	if err == nil || !strings.Contains(err.Error(), "lists no versions of chart") {
		t.Fatalf("a source without the chart must say so, not return an empty list: %+v %v", got, err)
	}
}

// countingRepository serves a one-chart index, counting downloads; fail makes
// the next download fail.
func countingRepository(t *testing.T, c *Client) (hits *atomic.Int32, fail *atomic.Bool, url string) {
	t.Helper()
	hits, fail = &atomic.Int32{}, &atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		time.Sleep(50 * time.Millisecond) // long enough for concurrent callers to overlap
		if fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("apiVersion: v1\nentries:\n  radar:\n  - {apiVersion: v2, name: radar, version: 1.0.0, urls: [radar-1.0.0.tgz]}\n  other:\n  - {apiVersion: v2, name: other, version: 2.0.0, urls: [other-2.0.0.tgz]}\n"))
	}))
	t.Cleanup(srv.Close)
	configureRepository(t, c, repo.Entry{Name: "probe", URL: srv.URL})
	return hits, fail, srv.URL
}

func configureRepository(t *testing.T, c *Client, entry repo.Entry) {
	t.Helper()
	f := repo.NewFile()
	f.Update(&entry)
	if err := f.WriteFile(c.settings.RepositoryConfig, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Concurrent listings share one download, and a listing within the reuse
// window doesn't download again, across charts of the same repository.
func TestChartVersionsShareRecentDownloads(t *testing.T) {
	withOCISources(t, nil)
	c := newChartSourceTestClient(t)
	hits, _, _ := countingRepository(t, c)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := c.ChartVersions("probe", "radar"); err != nil || got.Latest != "1.0.0" {
				t.Errorf("concurrent listing: %+v %v", got, err)
			}
		}()
	}
	wg.Wait()
	if got, err := c.ChartVersions("probe", "other"); err != nil || got.Latest != "2.0.0" {
		t.Fatalf("another chart of the same repository: %+v %v", got, err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("want one shared download, got %d", n)
	}
}

func TestChartVersionsDoNotReuseFailuresChangedCredentialsOrOldDownloads(t *testing.T) {
	withOCISources(t, nil)
	c := newChartSourceTestClient(t)
	hits, fail, url := countingRepository(t, c)

	fail.Store(true)
	if _, err := c.ChartVersions("probe", "radar"); err == nil {
		t.Fatal("a failed download must fail the listing")
	}
	fail.Store(false)
	if _, err := c.ChartVersions("probe", "radar"); err != nil {
		t.Fatalf("a failure must not be cached: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("want a retry after the failure, got %d downloads", n)
	}

	configureRepository(t, c, repo.Entry{Name: "probe", URL: url, Username: "rotated", Password: "x"})
	if _, err := c.ChartVersions("probe", "radar"); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 3 {
		t.Fatalf("changed credentials must not reuse the earlier listing, got %d downloads", n)
	}

	old := recentIndexTTL
	recentIndexTTL = 0
	t.Cleanup(func() { recentIndexTTL = old })
	if _, err := c.ChartVersions("probe", "radar"); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 4 {
		t.Fatalf("an expired listing must be downloaded again, got %d downloads", n)
	}
}

// A source that doesn't answer in time is reported as a timeout, before and
// after the response headers, so callers say "retry" rather than "reconfigure".
func TestSlowSourceIsReportedAsTimedOut(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if phase == "body" {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("apiVersion: v1\n"))
					w.(http.Flusher).Flush()
				}
				<-release
			}))
			defer srv.Close()
			defer close(release)
			c := newChartSourceTestClient(t)
			configureRepository(t, c, repo.Entry{Name: "slow", URL: srv.URL})
			_, _, err := c.fetchSelectedRepositoryIndex(ChartSourceCandidate{Type: "repository", Reference: "slow", URL: srv.URL}, t.TempDir(), 100*time.Millisecond, 0)
			if err == nil || !SourceTimedOut(err) {
				t.Fatalf("want a timeout: %v", err)
			}
		})
	}
	if SourceTimedOut(errSourceConflict) || SourceTimedOut(errors.New("repository returned HTTP 404")) {
		t.Fatal("other source errors must not read as timeouts")
	}
}

// The timeout survives to ChartVersions' callers for both source types, so
// get_helm_chart can say "retry" rather than "reconfigure".
func TestChartVersionsReportSourceTimeouts(t *testing.T) {
	withOCISources(t, []string{"oci://registry.example/charts"})
	c := newChartSourceTestClient(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	configureRepository(t, c, repo.Entry{Name: "slow", URL: srv.URL})
	old := freshVersionsTimeout
	freshVersionsTimeout = 100 * time.Millisecond
	t.Cleanup(func() { freshVersionsTimeout = old })
	if _, err := c.ChartVersions("slow", "radar"); !SourceTimedOut(err) || !errors.Is(err, errSourceUnavailable) {
		t.Fatalf("classic repository timeout lost: %v", err)
	}
	c.ociTags = tagListerFunc(func(string) ([]string, error) { return nil, context.DeadlineExceeded })
	if _, err := c.ChartVersions("oci://registry.example/charts", "radar"); !SourceTimedOut(err) {
		t.Fatalf("OCI timeout lost: %v", err)
	}
	c.ociTags = tagListerFunc(func(string) ([]string, error) { return nil, errors.New("denied") })
	if _, err := c.ChartVersions("oci://registry.example/charts", "radar"); err == nil || SourceTimedOut(err) {
		t.Fatalf("an OCI refusal must not read as a timeout: %v", err)
	}
}
