package issues

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/agentsandbox"
)

func unreported(name string, age time.Duration) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name, "namespace": "agents", "creationTimestamp": time.Now().Add(-age).UTC().Format(time.RFC3339)},
	}}
}

// The generic walk has no Ready condition to raise on an object the
// controller never reported on; past the incident threshold it is one.
func TestAgentSandboxNotReported(t *testing.T) {
	for _, c := range []struct {
		gvr  schema.GroupVersionResource
		kind string
	}{
		{schema.GroupVersionResource{Group: agentSandboxGroup, Version: "v1beta1", Resource: "sandboxes"}, "Sandbox"},
		{schema.GroupVersionResource{Group: agentSandboxExtensionsGroup, Version: "v1beta1", Resource: "sandboxclaims"}, "SandboxClaim"},
	} {
		got := composeOne(c.gvr, c.kind, unreported("old", 40*time.Hour), unreported("new", 10*time.Minute))
		old, ok := got["old"]
		if !ok || old.Reason != agentsandbox.ReasonNotReported || !strings.Contains(old.Message, "hasn't reported on this "+c.kind) {
			t.Errorf("%s old = %+v, want %s", c.kind, old, agentsandbox.ReasonNotReported)
		}
		if _, ok := got["new"]; ok {
			t.Errorf("%s: an object within the grace period raised an issue", c.kind)
		}
	}
}

func warmPoolNaming(name, template string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentSandboxExtensionsGroup + "/v1beta1", "kind": "SandboxWarmPool",
		"metadata": map[string]any{"name": name, "namespace": "agents"},
		"spec":     map[string]any{"replicas": int64(2), "sandboxTemplateRef": map[string]any{"name": template}},
	}}
}

func TestWarmPoolTemplateMissing(t *testing.T) {
	pools := schema.GroupVersionResource{Group: agentSandboxExtensionsGroup, Version: "v1beta1", Resource: "sandboxwarmpools"}
	templates := schema.GroupVersionResource{Group: agentSandboxExtensionsGroup, Version: "v1beta1", Resource: "sandboxtemplates"}
	tmpl := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentSandboxExtensionsGroup + "/v1beta1", "kind": "SandboxTemplate",
		"metadata": map[string]any{"name": "tmpl", "namespace": "agents"},
	}}
	base := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			pools:     {warmPoolNaming("ok", "tmpl"), warmPoolNaming("broken", "gone"), deleting(warmPoolNaming("going", "gone"))},
			templates: {tmpl},
		},
		kinds:      map[schema.GroupVersionResource]string{pools: "SandboxWarmPool", templates: "SandboxTemplate"},
		namespaced: map[schema.GroupVersionResource]bool{pools: true, templates: true},
	}
	readAll := func(EvidenceRead) bool { return true }
	reasons := func(p Provider, canRead func(EvidenceRead) bool) map[string]Issue {
		out := map[string]Issue{}
		for _, iss := range Compose(p, Filters{Limit: NoLimit, CanReadEvidence: canRead}) {
			if iss.Kind == "SandboxWarmPool" {
				out[iss.Name] = iss
			}
		}
		return out
	}

	got := reasons(base, readAll)
	broken, ok := got["broken"]
	if !ok || broken.Reason != agentsandbox.ReasonTemplateNotFound || len(broken.RequiredReads) != 2 {
		t.Fatalf("broken = %+v, want %s resting on pools and templates", broken, agentsandbox.ReasonTemplateNotFound)
	}
	if _, ok := got["ok"]; ok {
		t.Error("a pool whose template exists raised an issue")
	}
	if _, ok := got["going"]; ok {
		t.Error("a pool being deleted raised an issue")
	}

	// An unsynced template informer lists empty: that is not evidence.
	if got := reasons(&cnpgSyncProvider{fakeProvider: base, unsynced: map[schema.GroupVersionResource]bool{templates: true}}, readAll); len(got) != 0 {
		t.Errorf("unsynced templates raised %v", got)
	}
	// The finding is the templates' absence: a caller who can't list them doesn't get it.
	noTemplates := func(r EvidenceRead) bool { return r.Resource != "sandboxtemplates" }
	if got := reasons(base, noTemplates); len(got) != 0 {
		t.Errorf("a caller who can't list templates got %v", got)
	}
}

func deleting(u *unstructured.Unstructured) *unstructured.Unstructured {
	u.Object["metadata"].(map[string]any)["deletionTimestamp"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	return u
}
