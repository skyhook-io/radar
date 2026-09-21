package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/auth"
)

func TestRuntimeCatalogUsesRequestEligibility(t *testing.T) {
	defer k8s.SetTestLocalMode()()
	t.Setenv("RADAR_CLOUD_MODE", "false")
	handler := NewHandler()
	existing := listRegisteredToolsWith(t, true)
	for _, tc := range []struct {
		name                                                     string
		remote, authenticated, cloud, inCluster, tunnel, allowed bool
	}{
		{name: "local", allowed: true},
		{name: "remote", remote: true},
		{name: "authenticated", authenticated: true},
		{name: "cloud", cloud: true},
		{name: "in-cluster", inCluster: true},
		{name: "cloud-tunnel", tunnel: true},
		{name: "local-after-denials", allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := "false"
			if tc.cloud {
				mode = "true"
			}
			t.Setenv("RADAR_CLOUD_MODE", mode)
			k8s.ForceInCluster = tc.inCluster
			defer func() { k8s.ForceInCluster = false }()
			wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.remote {
					r.RemoteAddr = "192.0.2.1:54321"
				}
				if tc.authenticated {
					r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "operator"}))
				}
				if tc.tunnel {
					cloud.AuthenticatedTunnelHandler(handler).ServeHTTP(w, r)
				} else {
					handler.ServeHTTP(w, r)
				}
			})
			server := httptest.NewServer(wrapped)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "catalog-test", Version: "test"}, nil)
			session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: server.URL}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			names := map[string]bool{}
			cursor := ""
			for {
				result, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{Cursor: cursor})
				if err != nil {
					t.Fatal(err)
				}
				for _, tool := range result.Tools {
					names[tool.Name] = true
				}
				cursor = result.NextCursor
				if cursor == "" {
					break
				}
			}
			if names["collect_runtime_evidence"] != tc.allowed {
				t.Fatalf("collector listed=%v allowed=%v", names["collect_runtime_evidence"], tc.allowed)
			}
			for _, tool := range existing {
				if tool.Name != "collect_runtime_evidence" && !names[tool.Name] {
					t.Errorf("existing tool %s removed", tool.Name)
				}
			}
			if !tc.allowed {
				_, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "collect_runtime_evidence", Arguments: map[string]any{"adapter": "vault", "namespace": "lab", "pod": "vault", "confirm_network_access": true}})
				if err == nil || !strings.Contains(err.Error(), `unknown tool "collect_runtime_evidence"`) {
					t.Fatalf("expected unknown tool, got %v", err)
				}
			}
		})
	}
}
