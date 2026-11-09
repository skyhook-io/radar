package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"helm.sh/helm/v3/pkg/repo"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/helm"
)

// An agent about to install a chart must be able to pick an exact version on
// the read-only endpoint, with no release installed and no Kubernetes access.
func TestGetHelmChartListsVersionsOnTheReadOnlyEndpoint(t *testing.T) {
	index := "apiVersion: v1\nentries:\n  redis:\n" +
		"  - {apiVersion: v2, name: redis, version: 20.0.0, urls: [redis-20.0.0.tgz]}\n" +
		"  - {apiVersion: v2, name: redis, version: 21.0.0-rc.1, urls: [redis-21.0.0-rc.1.tgz]}\n" +
		"  - {apiVersion: v2, name: redis, version: 19.5.0, urls: [redis-19.5.0.tgz]}\n"
	charts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(index))
	}))
	defer charts.Close()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("HELM_REPOSITORY_CONFIG", filepath.Join(dir, "repositories.yaml"))
	t.Setenv("HELM_REPOSITORY_CACHE", filepath.Join(dir, "cache"))
	if err := os.MkdirAll(filepath.Join(dir, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := repo.NewFile()
	f.Update(&repo.Entry{Name: "bitnami", URL: charts.URL})
	if err := f.WriteFile(filepath.Join(dir, "repositories.yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	helm.ResetClient()
	if err := helm.InitializeWithRESTConfig(&rest.Config{Host: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(helm.ResetClient)

	ctx := context.Background()
	st, ct := mcpsdk.NewInMemoryTransports()
	ss, err := newServer(false, false, serverInstructions(false)).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Wait() })

	result, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_helm_chart", Arguments: map[string]any{"repository": "bitnami", "chart": "redis"}})
	if err != nil || result.IsError {
		t.Fatalf("get_helm_chart failed: %v %+v", err, result)
	}
	var got helm.ReleaseVersions
	if err := json.Unmarshal([]byte(result.Content[0].(*mcpsdk.TextContent).Text), &got); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Versions, ",") != "21.0.0-rc.1,20.0.0,19.5.0" || got.Latest != "20.0.0" || got.Source.Reference != "bitnami" {
		t.Fatalf("want the source's versions newest first and the newest stable as latest: %+v", got)
	}

	result, err = cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_helm_chart", Arguments: map[string]any{"repository": "elsewhere", "chart": "redis"}})
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].(*mcpsdk.TextContent).Text, "ask the Radar configuration owner") {
		t.Fatalf("an unconfigured source must be refused with the install path's guidance: %v %s", err, result.Content[0].(*mcpsdk.TextContent).Text)
	}

	// A repository argument with credentials is refused without echoing them,
	// in the result or in Radar's log.
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for _, repository := range []string{"https://user:s3cr3tpass@charts.example/x", "user:s3cr3tpass@charts.example/x"} {
		result, err = cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_helm_chart", Arguments: map[string]any{"repository": repository, "chart": "redis"}})
		if err != nil || !result.IsError {
			t.Fatalf("%s: want a refusal: %v %+v", repository, err, result)
		}
		if text := result.Content[0].(*mcpsdk.TextContent).Text; strings.Contains(text, "s3cr3tpass") {
			t.Fatalf("refusal echoes credentials: %s", text)
		}
	}
	if strings.Contains(logged.String(), "s3cr3tpass") {
		t.Fatalf("Radar's log records the credential: %s", logged.String())
	}
}

func TestManageHelmInstallWithoutVersionPointsAtChartVersions(t *testing.T) {
	err := manageHelmInput{Action: "install", Namespace: "team-a", Name: "cache", Chart: "redis", Repository: "bitnami"}.validate()
	if err == nil || !strings.Contains(err.Error(), "get_helm_chart") {
		t.Fatalf("a missing install version must say where versions are listed: %v", err)
	}
}
