package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
)

var (
	kagentServersGVR      = schema.GroupVersionResource{Group: "api.kagent.dev", Version: "v1alpha3", Resource: "remotemcpservers"}
	kagentModelConfigsGVR = schema.GroupVersionResource{Group: "api.kagent.dev", Version: "v1alpha3", Resource: "modelconfigs"}
)

const kagentHeaderToken = "literal-header-t0ken"

func setupKagentCredentialKinds(t *testing.T) {
	t.Helper()
	addPackageNamespaces(t, "agents")
	server := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"description": "GitHub", "url": "https://mcp.example.com/",
		"headersFrom": []any{map[string]any{"name": "X-Api-Key", "value": kagentHeaderToken}},
	}}}
	server.SetAPIVersion("api.kagent.dev/v1alpha3")
	server.SetKind("RemoteMCPServer")
	server.SetNamespace("agents")
	server.SetName("github")
	model := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"provider": "OpenAI", "model": "gpt-4o", "defaultHeaders": map[string]any{"X-Api-Key": kagentHeaderToken},
	}}}
	model.SetAPIVersion("api.kagent.dev/v1alpha3")
	model.SetKind("ModelConfig")
	model.SetNamespace("agents")
	model.SetName("gpt-4o")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kagentServersGVR:      "RemoteMCPServerList",
		kagentModelConfigsGVR: "ModelConfigList",
		{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}: "EndpointSliceList",
	}, server, model)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "api.kagent.dev", Version: "v1alpha3", Kind: "RemoteMCPServer", Name: "remotemcpservers", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "api.kagent.dev", Version: "v1alpha3", Kind: "ModelConfig", Name: "modelconfigs", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dc := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{kagentServersGVR, kagentModelConfigsGVR} {
		if err := dc.EnsureWatching(gvr); err != nil {
			t.Fatal(err)
		}
		if !dc.WaitForSync(gvr, 5*time.Second) {
			t.Fatalf("%s cache did not sync", gvr.Resource)
		}
	}
}

// Seeing the agents namespace is not enough to read kagent's RemoteMCPServers
// or ModelConfigs, whose spec can hold request headers, tokens included,
// inline: like Secrets, they need the caller's own permission on the kind.
func TestProxyAuth_KagentCredentialKindsFollowKindRBAC(t *testing.T) {
	setupKagentCredentialKinds(t)
	for _, kind := range []struct {
		gvr       schema.GroupVersionResource
		kind      string
		name      string
		countsKey string
	}{
		{kagentServersGVR, "RemoteMCPServer", "github", "api.kagent.dev/RemoteMCPServer"},
		{kagentModelConfigsGVR, "ModelConfig", "gpt-4o", "api.kagent.dev/ModelConfig"},
	} {
		for _, tt := range []struct {
			name    string
			granted bool
		}{{"without " + kind.gvr.Resource + " RBAC", false}, {"with " + kind.gvr.Resource + " RBAC", true}} {
			t.Run(tt.name, func(t *testing.T) {
				env := newAuthTestServer(t)
				perms := &auth.UserPermissions{AllowedNamespaces: []string{"agents"}}
				for _, ns := range []string{"", "agents"} {
					perms.SetCanI("list", kind.gvr.Group, kind.gvr.Resource, ns, tt.granted)
					perms.SetCanI("get", kind.gvr.Group, kind.gvr.Resource, ns, tt.granted)
				}
				env.srv.permCache.Set("viewer", nil, perms)

				for _, path := range []string{
					"/api/resources/" + kind.gvr.Resource + "?group=api.kagent.dev&namespaces=agents",
					"/api/resources/" + kind.gvr.Resource + "/agents/" + kind.name + "?group=api.kagent.dev",
					"/api/ai/resources/" + kind.gvr.Resource + "/agents/" + kind.name + "?group=api.kagent.dev",
				} {
					resp := env.authGet(t, path, "viewer", "")
					body := readBody(t, resp)
					if tt.granted && resp.StatusCode != http.StatusOK {
						t.Errorf("%s: status %d, want 200", path, resp.StatusCode)
					}
					if !tt.granted && (resp.StatusCode != http.StatusForbidden || strings.Contains(body, kagentHeaderToken)) {
						t.Errorf("%s: status %d body %s, want 403 without the object", path, resp.StatusCode, body)
					}
					if tt.granted && strings.HasPrefix(path, "/api/ai/") && strings.Contains(body, kagentHeaderToken) {
						t.Errorf("%s: AI context carries the literal header value: %s", path, body)
					}
				}

				if !tt.granted {
					resp := env.authGet(t, "/api/search?q="+kind.name+"&include=raw", "viewer", "")
					if body := readBody(t, resp); strings.Contains(body, kind.kind) {
						t.Errorf("search returned the %s: %s", kind.kind, body)
					}
					resp = env.authGet(t, "/api/dashboard/crds", "viewer", "")
					if body := readBody(t, resp); strings.Contains(body, kind.kind) {
						t.Errorf("dashboard CRD counts include the %s: %s", kind.kind, body)
					}
				}

				resp := env.authGet(t, "/api/resource-counts", "viewer", "")
				var counts testCounts
				if err := json.Unmarshal([]byte(readBody(t, resp)), &counts); err != nil {
					t.Fatal(err)
				}
				if tt.granted && counts.Counts[kind.countsKey] != 1 {
					t.Errorf("count = %d, want 1", counts.Counts[kind.countsKey])
				}
				if !tt.granted && !slices.Contains(counts.Forbidden, kind.countsKey) {
					t.Errorf("counts forbidden = %v, want %s", counts.Forbidden, kind.countsKey)
				}

				access := env.srv.issueSubjectAccess(requestWithUser(http.MethodGet, "/", &auth.User{Username: "viewer"}))
				if got := access(issues.Ref{Group: "api.kagent.dev", Kind: kind.kind, Namespace: "agents", Name: kind.name}); got != tt.granted {
					t.Errorf("issue subject access = %v, want %v", got, tt.granted)
				}
			})
		}
	}
}
