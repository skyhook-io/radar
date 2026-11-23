package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/kagent"
	"github.com/skyhook-io/radar/pkg/readreason"
	"k8s.io/apimachinery/pkg/runtime"
)

func seedKagentWorkspace(t *testing.T) {
	t.Helper()
	kinds := []k8s.APIResource{}
	for _, k := range kagentWorkspaceKinds {
		kinds = append(kinds, cnpgTestResource(k.Group, k.Kind, k.Resource, true))
	}
	a := agentSandboxObj(kagent.Group, "Agent", "agents", "blocked", time.Hour,
		map[string]any{"template": map[string]any{"inline": map[string]any{"modelConfig": map[string]any{"name": "bad", "namespace": "models"}}}},
		map[string]any{"observedGeneration": int64(1), "conditions": []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "Blocked", "observedGeneration": int64(1)},
			map[string]any{"type": "ResolvedRefs", "status": "False", "reason": "ReferenceResolutionFailed", "message": `resolve ModelConfig "models/bad": secret missing not found`, "observedGeneration": int64(1)},
		}})
	m := agentSandboxObj(kagent.Group, "ModelConfig", "models", "bad", time.Hour,
		map[string]any{"provider": "OpenAI", "model": "gpt", "allowedNamespaces": map[string]any{"from": "All"}, "apiKeySecret": "missing", "apiKeySecretKey": "OPENAI_API_KEY", "defaultHeaders": map[string]any{"Authorization": "Bearer MODEL_LITERAL"}},
		map[string]any{"observedGeneration": int64(1), "conditions": []any{map[string]any{"type": "Accepted", "status": "True", "observedGeneration": int64(1)}, map[string]any{"type": "ResolvedRefs", "status": "False", "reason": "APIKeySecretNotFound", "message": "secret missing not found", "observedGeneration": int64(1)}}})
	server := agentSandboxObj(kagent.Group, "RemoteMCPServer", "agents", "broken", time.Hour,
		map[string]any{"url": "https://user:URL_LITERAL@example.invalid/mcp?token=QUERY_LITERAL", "headersFrom": []any{map[string]any{"name": "Authorization", "value": "Bearer HEADER_LITERAL"}}},
		map[string]any{"observedGeneration": int64(1), "conditions": []any{map[string]any{"type": "Accepted", "status": "False", "reason": "DiscoveryFailed", "message": "Post https://example.invalid/mcp?token=MESSAGE_LITERAL: connection refused", "observedGeneration": int64(1)}}})
	objs := []runtime.Object{a, m, server}
	seedCNPGWorkspace(t, kinds, objs...)
	seedNamespaces(t, "agents", "models")
}

func getKagentWorkspace(t *testing.T, resp *http.Response) kagentWorkspaceResponse {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	for _, credential := range []string{"MODEL_LITERAL", "URL_LITERAL", "QUERY_LITERAL", "HEADER_LITERAL", "MESSAGE_LITERAL"} {
		if strings.Contains(string(raw), credential) {
			t.Fatalf("credential %s leaked", credential)
		}
	}
	var out kagentWorkspaceResponse
	out.Workspace = &kagent.Workspace{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestKagentWorkspaceSharedWithoutAuthAndNamespaceFilter(t *testing.T) {
	seedKagentWorkspace(t)
	resp, err := http.Get(testServer.URL + "/api/kagent/workspace")
	if err != nil {
		t.Fatal(err)
	}
	all := getKagentWorkspace(t, resp)
	if !all.Installed || len(all.Agents) != 1 || len(all.Models) != 1 || len(all.MCPServers) != 1 || len(all.Groups) != 2 || len(all.Findings) != 3 || all.Groups[0].Severity != kagent.SeverityCritical {
		t.Fatalf("workspace %+v groups=%+v findings=%+v", all, all.Groups, all.Findings)
	}
	resp, err = http.Get(testServer.URL + "/api/kagent/workspace?namespaces=agents")
	if err != nil {
		t.Fatal(err)
	}
	view := getKagentWorkspace(t, resp)
	if len(view.Models) != 0 || view.Agents[0].Model.State != kagent.RefPresent || view.Agents[0].Model.Reading == nil || len(view.Groups) != 2 || len(view.Groups[0].Folded) != 0 || view.Counts.Models.N != 0 {
		t.Fatalf("filtered workspace %+v", view)
	}
	req, _ := http.NewRequest("GET", testServer.URL+"/api/kagent/workspace", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	blocked, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	blocked.Body.Close()
	if blocked.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site status=%d", blocked.StatusCode)
	}
}

func TestKagentWorkspaceExactKindAccessAndGetOnlyDependency(t *testing.T) {
	seedKagentWorkspace(t)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"agents", "models"}}
	for _, k := range kagentWorkspaceKinds {
		allow(perms, k.Group, k.Resource, "", false)
		for _, ns := range []string{"agents", "models"} {
			allow(perms, k.Group, k.Resource, ns, k.Kind != kagent.KindModelConfig && k.Kind != kagent.KindRemoteMCPServer)
		}
	}
	perms.SetCanI("get", kagent.Group, "modelconfigs", "models", true)
	env.srv.permCache.Set("agent-reader", nil, perms)
	got := getKagentWorkspace(t, env.authGet(t, "/api/kagent/workspace?namespaces=agents", "agent-reader", ""))
	if len(got.Models) != 0 || len(got.MCPServers) != 0 || got.Agents[0].Model.State != kagent.RefUnread || got.Agents[0].Model.Unread != integration.KindCoverageDenied || len(got.Groups) != 1 || len(got.Groups[0].Folded) != 0 || !got.Counts.Findings.LowerBound {
		t.Fatalf("denied dependency %+v", got)
	}
}

func TestKagentWorkspaceNamespacePinRefusesOutsideDependencies(t *testing.T) {
	seedKagentWorkspace(t)
	oldForce, oldTarget := k8s.ForceNamespaceScope, k8s.GetNamespaceScopeTarget()
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("agents")
	t.Cleanup(func() { k8s.ForceNamespaceScope = oldForce; k8s.SetFallbackNamespace(oldTarget) })
	resp, err := http.Get(testServer.URL + "/api/kagent/workspace")
	if err != nil {
		t.Fatal(err)
	}
	got := getKagentWorkspace(t, resp)
	if got.ForcedNamespace != "agents" || got.Agents[0].Model.State != kagent.RefUnread || got.Agents[0].Model.Unread != string(readreason.OutsideNamespacePin) || len(got.Models) != 0 {
		t.Fatalf("pin escaped: %+v", got)
	}
	resp, err = http.Get(testServer.URL + "/api/kagent/workspace?namespaces=models")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("outside pin=%d", resp.StatusCode)
	}
}

func TestKagentWorkspaceNotInstalled(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds)
	resp, err := http.Get(testServer.URL + "/api/kagent/workspace")
	if err != nil {
		t.Fatal(err)
	}
	got := getKagentWorkspace(t, resp)
	if got.Installed || got.Counts.Findings.N != 0 || got.Counts.Findings.LowerBound || got.Coverage["models"].State != integration.KindCoverageNotInstalled {
		t.Fatalf("not installed=%+v", got)
	}
}

func TestKagentWorkspaceSelectedCoverageIsSeparateFromReferences(t *testing.T) {
	seedKagentWorkspace(t)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"agents", "models"}}
	for _, kind := range kagentWorkspaceKinds {
		allow(perms, kind.Group, kind.Resource, "", false)
		for _, ns := range []string{"agents", "models"} {
			allow(perms, kind.Group, kind.Resource, ns, kind.Kind != kagent.KindModelConfig || ns == "models")
		}
	}
	env.srv.permCache.Set("split-reader", nil, perms)
	got := getKagentWorkspace(t, env.authGet(t, "/api/kagent/workspace?namespaces=agents", "split-reader", ""))
	if got.Coverage["models"].State != integration.KindCoverageDenied || got.ReferenceCoverage["models"].State != integration.KindCoveragePartial || got.Agents[0].Model.State != kagent.RefPresent || len(got.Models) != 0 {
		t.Fatalf("selected=%+v references=%+v Agent=%+v", got.Coverage["models"], got.ReferenceCoverage["models"], got.Agents[0])
	}
}

func TestKagentWorkspaceBoundsComposition(t *testing.T) {
	kind := cnpgTestResource(kagent.Group, kagent.KindAgent, "agents", true)
	objs := make([]runtime.Object, kagentWorkspaceObjectLimit+1)
	for i := range objs {
		objs[i] = agentSandboxObj(kagent.Group, kagent.KindAgent, "agents", fmt.Sprintf("a-%d", i), time.Minute, nil, nil)
	}
	seedCNPGWorkspace(t, []k8s.APIResource{kind}, objs...)
	resp, err := http.Get(testServer.URL + "/api/kagent/workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(raw), "composition limit") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
	}
}
