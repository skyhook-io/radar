package helm

import (
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

func TestUpdateRepositoryRefreshesAuthenticatedHTTPSRepository(t *testing.T) {
	version := "1.0.0"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "reader" || password != "test-password" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, "apiVersion: v1\nentries:\n  app:\n  - apiVersion: v2\n    name: app\n    version: %s\n    urls: [app.tgz]\n", version)
	}))
	t.Cleanup(server.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	client := updateRepositoryTestClient(t, &repo.Entry{Name: "private", URL: server.URL, Username: "reader", Password: "test-password", CAFile: caFile})
	for _, next := range []string{"1.0.0", "2.0.0"} {
		version = next
		if err := client.UpdateRepository("private"); err != nil {
			t.Fatal(err)
		}
		result, err := client.SearchCharts("app", true)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Charts) != 1 || result.Charts[0].Version != next {
			t.Fatalf("cached charts = %#v, want only refreshed version %s", result.Charts, next)
		}
	}
}

func TestUpdateRepositoryPreservesReadersAndFailedRefreshCache(t *testing.T) {
	version, status := "1.0.0", http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "unavailable", status)
			return
		}
		if version == "invalid" {
			fmt.Fprint(w, "entries: [not a mapping")
			return
		}
		fmt.Fprintf(w, "apiVersion: v1\nentries:\n  app:\n  - apiVersion: v2\n    name: app\n    version: %s\n    urls: [app.tgz]\n", version)
	}))
	t.Cleanup(server.Close)
	client := updateRepositoryTestClient(t, &repo.Entry{Name: "cache", URL: server.URL})
	if err := client.UpdateRepository("cache"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(client.settings.RepositoryCache, "cache-index.yaml")
	// Windows does not allow replacing a file held open by os.Open.
	var reader *os.File
	if runtime.GOOS != "windows" {
		var err error
		reader, err = os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { reader.Close() })
	}
	version = "2.0.0"
	if err := client.UpdateRepository("cache"); err != nil {
		t.Fatal(err)
	}
	if reader != nil {
		old, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(old), "1.0.0") {
			t.Fatal("refresh rewrote an index already open by a reader")
		}
	}
	for _, failure := range []int{http.StatusUnauthorized, http.StatusServiceUnavailable} {
		status = failure
		if err := client.UpdateRepository("cache"); err == nil {
			t.Fatal("failed refresh returned success")
		}
		result, err := client.SearchCharts("app", true)
		if err != nil || len(result.Charts) != 1 || result.Charts[0].Version != "2.0.0" {
			t.Fatalf("lost cached chart: %#v, %v", result, err)
		}
	}
	status, version = http.StatusOK, "invalid"
	if err := client.UpdateRepository("cache"); err == nil {
		t.Fatal("invalid index returned success")
	}
	result, err := client.SearchCharts("app", true)
	if err != nil || len(result.Charts) != 1 || result.Charts[0].Version != "2.0.0" {
		t.Fatalf("invalid index replaced cached chart: %#v, %v", result, err)
	}
	staged, err := filepath.Glob(filepath.Join(client.settings.RepositoryCache, ".radar-repository-*"))
	if err != nil || len(staged) != 0 {
		t.Fatalf("failed refresh left staging directories: %v, %v", staged, err)
	}
}

func TestUpdateRepositoryRedactsCredentialURLs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("reader", "secret/p@ss")
	u.Path = "/private/path-credential"
	u.RawQuery = "token=private-token"
	client := updateRepositoryTestClient(t, &repo.Entry{Name: "private", URL: u.String()})
	err = client.UpdateRepository("private")
	if err == nil {
		t.Fatal("want failed refresh")
	}
	for _, secret := range []string{"reader", "secret", "p%40ss", "private-token", "path-credential"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("refresh error exposes credential %q", secret)
		}
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("lost useful status: %v", err)
	}
}

func TestUpdateRepositoryConcurrentRefreshAndSearch(t *testing.T) {
	index := "apiVersion: v1\nentries:\n  app:\n  - apiVersion: v2\n    name: app\n    version: 1.0.0\n    urls: [app.tgz]\n    description: " + strings.Repeat("chart ", 20000) + "\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, index) }))
	t.Cleanup(server.Close)
	client := updateRepositoryTestClient(t, &repo.Entry{Name: "shared", URL: server.URL})
	if err := client.UpdateRepository("shared"); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	failures := make(chan error, 20)
	for range 4 {
		group.Go(func() {
			for range 4 {
				if err := client.UpdateRepository("shared"); err != nil {
					failures <- err
				}
			}
		})
	}
	for range 4 {
		group.Go(func() {
			for range 20 {
				result, err := client.SearchCharts("app", true)
				if err != nil {
					failures <- err
					return
				}
				if len(result.Charts) != 1 {
					failures <- fmt.Errorf("search lost chart during refresh")
					return
				}
			}
		})
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	staged, err := filepath.Glob(filepath.Join(client.settings.RepositoryCache, ".radar-repository-*"))
	if err != nil || len(staged) != 0 {
		t.Fatalf("staging directories left: %v, %v", staged, err)
	}
}

func TestUpdateRepositoryRejectsChangedConfiguration(t *testing.T) {
	for _, change := range []string{"remove", "URL", "credentials"} {
		t.Run(change, func(t *testing.T) {
			var repositoryConfig string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				updated := repo.NewFile()
				if change != "remove" {
					entry := &repo.Entry{Name: "changed", URL: "https://new.example.test"}
					if change == "credentials" {
						entry.URL = "http://" + r.Host
						entry.Password = "new-password"
					}
					updated.Update(entry)
				}
				if err := writeRepositoryFileAtomic(updated, repositoryConfig); err != nil {
					t.Error(err)
					http.Error(w, "fixture failed", 500)
					return
				}
				fmt.Fprint(w, "apiVersion: v1\nentries:\n  app:\n  - apiVersion: v2\n    name: app\n    version: 2.0.0\n    urls: [app.tgz]\n")
			}))
			t.Cleanup(server.Close)
			client := updateRepositoryTestClient(t, &repo.Entry{Name: "changed", URL: server.URL})
			repositoryConfig = client.settings.RepositoryConfig
			if err := os.MkdirAll(client.settings.RepositoryCache, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(client.settings.RepositoryCache, "changed-index.yaml")
			const previous = "apiVersion: v1\nentries: {}\n"
			if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := client.UpdateRepository("changed"); !errors.Is(err, errRepositoryConflict) {
				t.Fatalf("want conflict, got %v", err)
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != previous {
				t.Fatalf("published stale source after config changed: %q, %v", body, err)
			}
		})
	}
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
