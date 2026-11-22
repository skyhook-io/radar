package issues

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/kagent"
)

var (
	kagentAgents  = schema.GroupVersionResource{Group: kagent.Group, Version: "v1alpha3", Resource: "agents"}
	kagentServers = schema.GroupVersionResource{Group: kagent.Group, Version: "v1alpha3", Resource: "remotemcpservers"}
	elasticAgents = schema.GroupVersionResource{Group: "agent.k8s.elastic.co", Version: "v1alpha1", Resource: "agents"}
)

func kagentObject(name string, conds ...map[string]any) *unstructured.Unstructured {
	list := make([]any, len(conds))
	for i, c := range conds {
		list[i] = c
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name, "namespace": "agents", "generation": int64(1), "creationTimestamp": time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)},
		"status":   map[string]any{"observedGeneration": int64(1), "conditions": list},
	}}
}

func kagentCondition(condType, status, reason, message string, age time.Duration) map[string]any {
	return map[string]any{"type": condType, "status": status, "reason": reason, "message": message, "observedGeneration": int64(1), "lastTransitionTime": time.Now().Add(-age).UTC().Format(time.RFC3339)}
}

// composeAll returns every Issue on each object, in compose order.
func composeAll(gvr schema.GroupVersionResource, kind string, objects ...*unstructured.Unstructured) map[string][]Issue {
	for _, u := range objects {
		u.SetAPIVersion(gvr.GroupVersion().String())
		u.SetKind(kind)
	}
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: objects},
		kinds:      map[schema.GroupVersionResource]string{gvr: kind},
		namespaced: map[schema.GroupVersionResource]bool{gvr: true},
	}
	byName := map[string][]Issue{}
	for _, issue := range Compose(p, Filters{Limit: NoLimit}) {
		byName[issue.Name] = append(byName[issue.Name], issue)
	}
	return byName
}

func TestKagentAgentIssues(t *testing.T) {
	blockedBy := func(name string) []map[string]any {
		return []map[string]any{
			kagentCondition("ResolvedRefs", "False", "ReferenceResolutionFailed", `resolve ModelConfig "agents/`+name+`": not found`, time.Hour),
			kagentCondition("Compatible", "False", "Blocked", "blocked by ResolvedRefs", time.Hour),
			kagentCondition("Ready", "False", "Blocked", "blocked by ResolvedRefs", time.Hour),
		}
	}
	egressMissing := kagentCondition("EgressPoliciesApplied", "False", "EgressPolicyNotFound", `agents/github: egresspolicies.api.kagent.dev "github" not found`, time.Hour)
	ready := kagentCondition("Ready", "True", "Ready", "ActorTemplate golden snapshot is ready", time.Hour)

	got := composeAll(kagentAgents, "Agent",
		kagentObject("preparing", kagentCondition("Ready", "False", "ActorTemplatePending", "waiting for the ActorTemplate golden snapshot", 5*time.Minute)),
		kagentObject("blocked", blockedBy("gpt")...),
		kagentObject("ready-egress", ready, egressMissing),
		kagentObject("blocked-egress", append(blockedBy("gpt"), egressMissing)...),
	)

	if issues := got["preparing"]; len(issues) != 0 {
		t.Errorf("preparing within the grace period raised %+v", issues)
	}
	if issues := got["blocked"]; len(issues) != 1 || issues[0].Severity != SeverityCritical || issues[0].Reason != "ReferenceResolutionFailed" ||
		issues[0].Action != "Create ModelConfig agents/gpt or point the agent at an existing one." || strings.Contains(issues[0].Message, "Create ModelConfig") {
		t.Errorf("blocked = %+v, want one critical ReferenceResolutionFailed with the cause in message and the step in action", issues)
	}
	if issues := got["ready-egress"]; len(issues) != 1 || issues[0].Severity != SeverityWarning || issues[0].Reason != "EgressPolicyNotFound" || issues[0].Fingerprint != "egress" {
		t.Errorf("ready-egress = %+v, want one egress warning", issues)
	}
	if issues := got["blocked-egress"]; len(issues) != 2 || issues[0].ID == issues[1].ID {
		t.Errorf("blocked-egress = %+v, want the block and the egress warning as two Issues", issues)
	}
}

// Another API group's Agent keeps the generic condition walk.
func TestKagentRulesClaimOnlyKagentsGroup(t *testing.T) {
	got := composeAll(elasticAgents, "Agent", kagentObject("fleet", kagentCondition("Ready", "False", "ActorTemplatePending", "", 5*time.Minute)))
	if issues := got["fleet"]; len(issues) != 1 || issues[0].Severity != SeverityWarning {
		t.Errorf("elastic agent = %+v, want the generic Ready=False warning", issues)
	}
}

func TestKagentRemoteMCPServerIssues(t *testing.T) {
	got := composeAll(kagentServers, "RemoteMCPServer",
		kagentObject("off", kagentCondition("Accepted", "True", "DiscoveryDisabled", "Global tool discovery is disabled by the kagent.dev/discovery label", time.Hour)),
		kagentObject("failed", kagentCondition("Accepted", "False", "DiscoveryFailed", "failed to connect: 401 Unauthorized", time.Hour)),
	)
	if issues := got["off"]; len(issues) != 0 {
		t.Errorf("discovery off raised %+v", issues)
	}
	if issues := got["failed"]; len(issues) != 1 || issues[0].Reason != "DiscoveryFailed" || issues[0].Severity != SeverityWarning {
		t.Errorf("failed = %+v, want one DiscoveryFailed warning", issues)
	}
}
