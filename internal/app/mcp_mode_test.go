package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
)

func TestServerMCPModes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := k8s.InitTestResourceCache(fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-test"}})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)
	for _, mode := range []struct {
		name              string
		enabled, readOnly bool
	}{
		{"default", true, false}, {"disabled", false, false}, {"read-only", true, true},
	} {
		for _, base := range []string{"", "/radar"} {
			t.Run(mode.name+base, func(t *testing.T) {
				srv := CreateServer(AppConfig{MCPEnabled: mode.enabled, MCPReadOnly: mode.readOnly, BasePath: base})
				host := httptest.NewServer(srv.Handler())
				defer host.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				for _, path := range []string{"/mcp", "/mcp-readonly", "/mcp-investigation"} {
					available := mode.enabled && (path != "/mcp" || !mode.readOnly)
					if !available {
						for _, suffix := range []string{"", "/", "/tools/call"} {
							for _, method := range []string{http.MethodGet, http.MethodPost} {
								req, _ := http.NewRequestWithContext(ctx, method, host.URL+base+path+suffix, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
								resp, err := http.DefaultClient.Do(req)
								if err != nil {
									t.Fatal(err)
								}
								body, _ := io.ReadAll(resp.Body)
								resp.Body.Close()
								if resp.StatusCode != 404 || strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
									t.Fatalf("%s %s: %d %s", method, path+suffix, resp.StatusCode, body)
								}
							}
						}
						continue
					}
					if path == "/mcp-investigation" {
						resp, err := http.Get(host.URL + base + path)
						if err != nil {
							t.Fatal(err)
						}
						resp.Body.Close()
						if resp.StatusCode != 400 {
							t.Fatalf("investigation mount = %d", resp.StatusCode)
						}
						continue
					}
					client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "mode-test", Version: "1"}, nil)
					session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: host.URL + base + path}, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer session.Close()
					tools, err := session.ListTools(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					hasWrite := false
					for _, tool := range tools.Tools {
						if tool.Name == "apply_resource" {
							hasWrite = true
						}
					}
					if hasWrite != (path == "/mcp") {
						t.Fatalf("%s write tool present = %v", path, hasWrite)
					}
					result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "list_namespaces", Arguments: struct{}{}})
					if err != nil || result.IsError {
						t.Fatalf("read tool: %v, %v", result, err)
					}
					payload, _ := json.Marshal(result)
					if !strings.Contains(string(payload), "mcp-test") {
						t.Fatalf("read result lost namespace: %s", payload)
					}
					if path == "/mcp-readonly" {
						result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "apply_resource", Arguments: struct{}{}})
						if err == nil || !strings.Contains(err.Error(), "unknown tool") {
							t.Fatalf("write call = %v, %v; want unknown tool", result, err)
						}
						result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "diagnose", Arguments: struct {
							Kind      string `json:"kind"`
							Namespace string `json:"namespace"`
							Name      string `json:"name"`
							InCluster bool   `json:"in_cluster"`
						}{"Service", "mcp-test", "api", true}})
						if err != nil || !result.IsError {
							t.Fatalf("mutating diagnostic = %v, %v; want validation refusal", result, err)
						}
						payload, _ = json.Marshal(result)
						if !strings.Contains(string(payload), "unexpected additional properties") || !strings.Contains(string(payload), "in_cluster") {
							t.Fatalf("diagnostic refusal did not identify blocked parameter: %s", payload)
						}
					}
				}
				resp, err := http.Get(host.URL + base + "/api/config")
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				var cfg struct {
					Effective config.Config `json:"effective"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
					t.Fatal(err)
				}
				if cfg.Effective.MCP == nil || *cfg.Effective.MCP != mode.enabled || cfg.Effective.MCPReadOnly == nil || *cfg.Effective.MCPReadOnly != mode.readOnly {
					t.Fatalf("effective config: %+v", cfg.Effective)
				}
				capsResp, err := http.Get(host.URL + base + "/api/capabilities")
				if err != nil {
					t.Fatal(err)
				}
				defer capsResp.Body.Close()
				var caps k8s.Capabilities
				if err := json.NewDecoder(capsResp.Body).Decode(&caps); err != nil {
					t.Fatal(err)
				}
				if capsResp.StatusCode != 200 || caps.MCPEnabled != mode.enabled || caps.MCPReadOnly != mode.readOnly {
					t.Fatalf("capabilities: status=%d enabled=%v readOnly=%v", capsResp.StatusCode, caps.MCPEnabled, caps.MCPReadOnly)
				}
			})
		}
	}
}
