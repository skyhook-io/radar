package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/agentsandbox"
	"github.com/skyhook-io/radar/pkg/readreason"
)

var agentSandboxTestKinds = []k8s.APIResource{
	cnpgTestResource(agentsandbox.Group, "Sandbox", "sandboxes", true),
	cnpgTestResource(agentsandbox.ExtensionsGroup, "SandboxClaim", "sandboxclaims", true),
	cnpgTestResource(agentsandbox.ExtensionsGroup, "SandboxWarmPool", "sandboxwarmpools", true),
	cnpgTestResource(agentsandbox.ExtensionsGroup, "SandboxTemplate", "sandboxtemplates", true),
}

func agentSandboxObj(group, kind, ns, name string, age time.Duration, spec, status map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": group + "/v1", "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": ns, "generation": int64(1), "creationTimestamp": time.Now().Add(-age).UTC().Format(time.RFC3339)},
	}
	if spec != nil {
		obj["spec"] = spec
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

func readyFalseStatus(reason, message string, age time.Duration) map[string]any {
	return map[string]any{"conditions": []any{map[string]any{
		"type": "Ready", "status": "False", "reason": reason, "message": message, "observedGeneration": int64(1),
		"lastTransitionTime": time.Now().Add(-age).UTC().Format(time.RFC3339),
	}}}
}

// One namespace's worth: a failed Sandbox, a claim whose pool is missing and
// one whose Sandbox failed, a pool with its template.
func agentSandboxFixture(ns string) []runtime.Object {
	return []runtime.Object{
		agentSandboxObj(agentsandbox.Group, "Sandbox", ns, "failed", time.Hour, nil, readyFalseStatus("PodFailed", "Pod failed", time.Hour)),
		agentSandboxObj(agentsandbox.ExtensionsGroup, "SandboxClaim", ns, "no-pool", time.Hour,
			map[string]any{"warmPoolRef": map[string]any{"name": "no-such-pool"}}, readyFalseStatus("WarmPoolNotFound", `SandboxWarmPool "no-such-pool" not found`, time.Hour)),
		agentSandboxObj(agentsandbox.ExtensionsGroup, "SandboxClaim", ns, "bound", time.Hour,
			map[string]any{"warmPoolRef": map[string]any{"name": "warm"}},
			func() map[string]any {
				s := readyFalseStatus("PodFailed", "Pod failed", time.Hour)
				s["sandbox"] = map[string]any{"name": "failed"}
				return s
			}()),
		agentSandboxObj(agentsandbox.ExtensionsGroup, "SandboxWarmPool", ns, "warm", time.Hour,
			map[string]any{"replicas": int64(1), "sandboxTemplateRef": map[string]any{"name": "tmpl"}},
			map[string]any{"observedGeneration": int64(1), "readyReplicas": int64(1)}),
		agentSandboxObj(agentsandbox.ExtensionsGroup, "SandboxTemplate", ns, "tmpl", time.Hour, map[string]any{}, nil),
	}
}

// seedAgentSandbox is seedCNPGWorkspace with Sandboxes tracked under their
// real plural: the fake client would guess "sandboxs" from the kind.
func seedAgentSandbox(t *testing.T, objs ...runtime.Object) {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{}
	for _, k := range agentSandboxTestKinds {
		listKinds[schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Name}] = k.Kind + "List"
	}
	var others []runtime.Object
	var sandboxes []*unstructured.Unstructured
	for _, o := range objs {
		if u := o.(*unstructured.Unstructured); u.GetKind() == "Sandbox" {
			sandboxes = append(sandboxes, u)
		} else {
			others = append(others, o)
		}
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, others...)
	gvr := schema.GroupVersionResource{Group: agentsandbox.Group, Version: "v1", Resource: "sandboxes"}
	for _, u := range sandboxes {
		if err := dyn.Tracker().Create(gvr, u, u.GetNamespace()); err != nil {
			t.Fatalf("track sandbox: %v", err)
		}
	}
	if err := k8s.InitTestDynamicResourceCache(dyn, agentSandboxTestKinds); err != nil {
		t.Fatalf("seed agent sandbox: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
}

func getAgentSandboxWorkspace(t *testing.T, resp *http.Response) AgentSandboxWorkspaceResponse {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out AgentSandboxWorkspaceResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func agentSandboxRow(got AgentSandboxWorkspaceResponse, kind, ns, name string) *agentsandbox.Attention {
	for i := range got.Attention {
		s := got.Attention[i].Subject
		if s.Kind == kind && s.Namespace == ns && s.Name == name {
			return &got.Attention[i]
		}
	}
	return nil
}

func TestAgentSandboxWorkspace_NotInstalled(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds)
	resp, err := http.Get(testServer.URL + "/api/agentsandbox/workspace")
	if err != nil {
		t.Fatal(err)
	}
	got := getAgentSandboxWorkspace(t, resp)
	if got.Installed || got.Coverage["sandboxes"].State != integration.KindCoverageNotInstalled || len(got.Attention) != 0 {
		t.Fatalf("installed=%v coverage=%+v attention=%+v", got.Installed, got.Coverage, got.Attention)
	}
	if got.Counts.Sandboxes != nil || got.Sidebar.Total.Count == nil || *got.Sidebar.Total.Count != 0 {
		t.Errorf("counts %+v sidebar %+v", got.Counts, got.Sidebar)
	}
}

func TestAgentSandboxWorkspace_AuthDisabledReadsEverything(t *testing.T) {
	seedAgentSandbox(t, agentSandboxFixture("agents")...)
	resp, err := http.Get(testServer.URL + "/api/agentsandbox/workspace")
	if err != nil {
		t.Fatal(err)
	}
	got := getAgentSandboxWorkspace(t, resp)
	for _, k := range []string{"sandboxes", "claims", "warmPools", "templates"} {
		if got.Coverage[k].State != integration.KindCoverageFull {
			t.Errorf("%s coverage %+v", k, got.Coverage[k])
		}
	}
	if agentSandboxRow(got, "Sandbox", "agents", "failed") == nil || agentSandboxRow(got, "SandboxClaim", "agents", "no-pool") == nil {
		t.Fatalf("attention %+v", got.Attention)
	}
	// The Sandbox's own row covers the claim it forwarded its failure to.
	if agentSandboxRow(got, "SandboxClaim", "agents", "bound") != nil {
		t.Error("a forwarded failure was listed twice")
	}
	if got.Counts.Sandboxes == nil || got.Counts.Sandboxes.Failed != 1 || got.Counts.Sandboxes.LowerBound {
		t.Errorf("sandbox counts %+v", got.Counts.Sandboxes)
	}
	if got.Sidebar.Total.Count == nil || *got.Sidebar.Total.Count != 2 {
		t.Errorf("sidebar %+v", got.Sidebar)
	}
	// Nothing at the controller's place in this fixture: not found, never stopped.
	if got.Controller.State != agentsandbox.ControllerNotFound {
		t.Errorf("controller %+v", got.Controller)
	}
	if len(got.WarmPools) != 1 || got.WarmPools[0].Claims == nil || got.WarmPools[0].Claims.Total != 1 || got.WarmPools[0].TemplatePresence != agentsandbox.Present {
		t.Errorf("warm pools %+v", got.WarmPools)
	}
}

func TestAgentSandboxWorkspace_NamespaceFilter(t *testing.T) {
	objs := append(agentSandboxFixture("team-a"), agentSandboxFixture("team-b")...)
	seedAgentSandbox(t, objs...)
	resp, err := http.Get(testServer.URL + "/api/agentsandbox/workspace?namespaces=team-a")
	if err != nil {
		t.Fatal(err)
	}
	got := getAgentSandboxWorkspace(t, resp)
	if len(got.Namespaces) != 1 || got.Namespaces[0] != "team-a" {
		t.Errorf("namespaces %v", got.Namespaces)
	}
	for _, a := range got.Attention {
		if a.Subject.Namespace == "team-b" {
			t.Errorf("a row from outside the filter: %+v", a)
		}
	}
	if agentSandboxRow(got, "Sandbox", "team-a", "failed") == nil || got.Counts.Sandboxes == nil || got.Counts.Sandboxes.Total != 1 {
		t.Errorf("attention %+v counts %+v", got.Attention, got.Counts.Sandboxes)
	}
}

// A caller who may read claims but not Sandboxes keeps the failure the claim
// reports, with its Sandbox unread, and no Sandbox count at all.
func TestAgentSandboxWorkspace_DeniedKind(t *testing.T) {
	seedAgentSandbox(t, agentSandboxFixture("agents")...)
	seedNamespaces(t, "agents")
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"agents"}}
	for _, k := range agentSandboxKinds {
		ok := k.Resource != "sandboxes"
		allow(perms, k.Group, k.Resource, "", false)
		allow(perms, k.Group, k.Resource, "agents", ok)
	}
	perms.SetCanI("get", "apps", "deployments", agentsandbox.ControllerNamespace, false)
	env.srv.permCache.Set("claims-only", nil, perms)

	got := getAgentSandboxWorkspace(t, env.authGet(t, "/api/agentsandbox/workspace?namespaces=agents", "claims-only", ""))
	if got.Coverage["sandboxes"].State != integration.KindCoverageDenied || got.Coverage["claims"].State != integration.KindCoverageFull {
		t.Fatalf("coverage %+v", got.Coverage)
	}
	if agentSandboxRow(got, "Sandbox", "agents", "failed") != nil {
		t.Error("a row about a Sandbox the caller can't read")
	}
	bound := agentSandboxRow(got, "SandboxClaim", "agents", "bound")
	if bound == nil || bound.Rule != agentsandbox.RuleFailed || !bound.Issue {
		t.Fatalf("bound = %+v, want the claim's own failure", bound)
	}
	for _, r := range bound.Relations {
		if r.Role == "sandbox" && r.Presence != agentsandbox.Unread {
			t.Errorf("sandbox relation %+v, want unread", r)
		}
	}
	if got.Counts.Sandboxes != nil || got.Sidebar.Sandboxes.Count != nil || !got.Sidebar.Total.LowerBound || got.AttentionComplete {
		t.Errorf("counts %+v sidebar %+v complete=%v", got.Counts.Sandboxes, got.Sidebar, got.AttentionComplete)
	}
	if got.Controller.State != agentsandbox.ControllerUnread || got.Controller.Code != readreason.UserForbidden || got.Controller.Ready != nil || got.Controller.Desired != nil {
		t.Errorf("controller %+v, want unread (denied)", got.Controller)
	}
}

// Claims read in one namespace and not another: the claims served by a pool
// in the unread one are unknown, never "none".
func TestAgentSandboxWorkspace_PartialClaimsLeaveServedUnknown(t *testing.T) {
	objs := append(agentSandboxFixture("team-a"), agentSandboxFixture("team-b")...)
	seedAgentSandbox(t, objs...)
	seedNamespaces(t, "team-a", "team-b")
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team-a", "team-b"}}
	for _, k := range agentSandboxKinds {
		allow(perms, k.Group, k.Resource, "", false)
		allow(perms, k.Group, k.Resource, "team-a", true)
		allow(perms, k.Group, k.Resource, "team-b", k.Resource != "sandboxclaims")
	}
	env.srv.permCache.Set("split", nil, perms)

	got := getAgentSandboxWorkspace(t, env.authGet(t, "/api/agentsandbox/workspace?namespaces=team-a,team-b", "split", ""))
	cov := got.Coverage["claims"]
	if cov.State != integration.KindCoveragePartial || len(cov.DeniedNamespaces) != 1 || cov.DeniedNamespaces[0] != "team-b" {
		t.Fatalf("claims coverage %+v", cov)
	}
	for _, p := range got.WarmPools {
		switch p.Namespace {
		case "team-a":
			if p.Claims == nil || p.Claims.Total != 1 {
				t.Errorf("team-a pool %+v", p)
			}
		case "team-b":
			if p.Claims != nil {
				t.Errorf("team-b pool claims %+v, want unknown", p.Claims)
			}
		}
	}
	if got.Counts.Claims == nil || !got.Counts.Claims.LowerBound {
		t.Errorf("claim counts %+v, want a lower bound", got.Counts.Claims)
	}
}

func TestAgentSandboxWorkspace_ForcedScope(t *testing.T) {
	seedAgentSandbox(t, append(agentSandboxFixture("team-a"), agentSandboxFixture("team-b")...)...)
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("team-b")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = false
		k8s.SetFallbackNamespace("")
	})
	resp, err := http.Get(testServer.URL + "/api/agentsandbox/workspace?namespaces=team-a")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a filter outside the pin: status %d, want 403", resp.StatusCode)
	}
	resp, err = http.Get(testServer.URL + "/api/agentsandbox/workspace")
	if err != nil {
		t.Fatal(err)
	}
	got := getAgentSandboxWorkspace(t, resp)
	if got.ForcedNamespace != "team-b" || agentSandboxRow(got, "Sandbox", "team-a", "failed") != nil || agentSandboxRow(got, "Sandbox", "team-b", "failed") == nil {
		t.Errorf("forced=%q attention %+v", got.ForcedNamespace, got.Attention)
	}
	if got.Controller.State != agentsandbox.ControllerUnread || got.Controller.Code != readreason.OutsideNamespacePin {
		t.Errorf("controller %+v", got.Controller)
	}
}
