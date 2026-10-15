package helm

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/repo"
)

func TestUpdateRepositoryRefreshesHTTPRepository(t *testing.T) {
	const index = `apiVersion: v1
entries:
  crowdsec-web-ui:
  - apiVersion: v2
    name: crowdsec-web-ui
    version: 0.55.0
    description: A CrowdSec web interface
    urls:
    - charts/crowdsec-web-ui-0.55.0.tgz
generated: "2026-10-14T00:00:00Z"
`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, index)
	}))
	t.Cleanup(server.Close)

	client := updateRepositoryTestClient(t, &repo.Entry{Name: "zekker6", URL: server.URL})
	if err := client.UpdateRepository("zekker6"); err != nil {
		t.Fatalf("UpdateRepository() error = %v", err)
	}

	indexPath := filepath.Join(client.settings.RepositoryCache, "zekker6-index.yaml")
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("repository index was not written to configured cache: %v", err)
	}

	indexFile, err := repo.LoadIndexFile(indexPath)
	if err != nil {
		t.Fatalf("cached repository index is not usable: %v", err)
	}
	if _, err := indexFile.Get("crowdsec-web-ui", "0.55.0"); err != nil {
		t.Fatalf("cached repository index does not contain expected chart: %v", err)
	}

	result, err := client.SearchCharts("crowdsec-web-ui", true)
	if err != nil {
		t.Fatalf("SearchCharts() error = %v", err)
	}
	if len(result.Charts) != 1 || result.Charts[0].Repository != "zekker6" || result.Charts[0].Name != "crowdsec-web-ui" || result.Charts[0].Version != "0.55.0" {
		t.Fatalf("SearchCharts() = %#v, want refreshed zekker6/crowdsec-web-ui 0.55.0", result.Charts)
	}
}

func TestUpdateRepositoryReturnsErrorForBrokenRepository(t *testing.T) {
	t.Run("unsupported protocol", func(t *testing.T) {
		client := updateRepositoryTestClient(t, &repo.Entry{Name: "broken", URL: "unsupported://example.test/charts"})
		err := client.UpdateRepository("broken")
		if err == nil || !strings.Contains(err.Error(), "could not find protocol handler") {
			t.Fatalf("UpdateRepository() error = %v, want unsupported protocol error", err)
		}
	})

	t.Run("HTTP failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "repository unavailable", http.StatusServiceUnavailable)
		}))
		t.Cleanup(server.Close)

		client := updateRepositoryTestClient(t, &repo.Entry{Name: "broken", URL: server.URL})
		if err := client.UpdateRepository("broken"); err == nil {
			t.Fatal("UpdateRepository() error = nil, want HTTP failure")
		}
	})
}

func updateRepositoryTestClient(t *testing.T, entry *repo.Entry) *Client {
	t.Helper()
	dir := t.TempDir()
	repositoryConfig := filepath.Join(dir, "repositories.yaml")
	repositories := repo.NewFile()
	repositories.Update(entry)
	if err := repositories.WriteFile(repositoryConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	return &Client{settings: &cli.EnvSettings{
		RepositoryConfig: repositoryConfig,
		RepositoryCache:  filepath.Join(dir, "repository-cache"),
	}}
}
