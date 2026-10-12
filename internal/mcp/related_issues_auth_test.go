package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestMCPRelatedIssuesAuthorizeNodeClassSubject(t *testing.T) {
	initMCPRelatedIssueAuthDiscovery(t)
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "alice"})
	perms := &pkgauth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("get", karpenter.Group, "nodepools", "", true)
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	getPermCache().Set("alice", nil, perms)
	t.Cleanup(func() { getPermCache().Invalidate() })
	access := issueRelatedResourceAccess(ctx)

	pool := issues.Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: "compute"}
	nodeClass := issues.Ref{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass", Name: "default"}
	if !access(pool) || access(nodeClass) {
		t.Fatalf("related access: pool=%v nodeClass=%v, want allowed/denied", access(pool), access(nodeClass))
	}

	grouped := []issues.Issue{{
		ID: "nodeclass-not-ready", Group: nodeClass.Group, Kind: nodeClass.Kind, Name: nodeClass.Name,
		DiagnosticContext: &issuesapi.DiagnosticContext{Facts: []issuesapi.DiagnosticFact{{
			Type: "karpenter_referenced_by_nodepools", Refs: []issuesapi.Ref{pool},
		}}},
	}}
	if got := issues.RelatedIssuesFrom(nil, grouped, issues.RelatedIssueOptions{CanReadRelated: access}, pool.Group, pool.Kind, pool.Namespace, pool.Name); len(got) != 0 {
		t.Fatalf("NodePool lookup leaked denied NodeClass issue: %+v", got)
	}

	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	if got := issues.RelatedIssuesFrom(nil, grouped, issues.RelatedIssueOptions{CanReadRelated: access}, pool.Group, pool.Kind, pool.Namespace, pool.Name); len(got) != 1 {
		t.Fatalf("NodePool lookup omitted authorized NodeClass issue: %+v", got)
	}
}

func TestMCPRelatedIssuesAuthorizeMissingNodeClassEvidence(t *testing.T) {
	initMCPRelatedIssueAuthDiscovery(t)
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "alice"})
	perms := &pkgauth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("get", karpenter.Group, "nodepools", "", true)
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	getPermCache().Set("alice", nil, perms)
	t.Cleanup(func() { getPermCache().Invalidate() })
	access := issueRelatedResourceAccess(ctx)
	pool := issues.Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: "compute"}
	nodeClass := issues.Ref{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass", Name: "missing"}
	grouped := []issues.Issue{{
		ID: "nodeclass-missing", Group: pool.Group, Kind: pool.Kind, Name: pool.Name,
		DiagnosticContext: &issuesapi.DiagnosticContext{Facts: []issuesapi.DiagnosticFact{{
			Type: "karpenter_referenced_nodeclass", Refs: []issuesapi.Ref{nodeClass},
		}}},
	}}
	if got := issues.RelatedIssuesFrom(nil, grouped, issues.RelatedIssueOptions{CanReadRelated: access}, pool.Group, pool.Kind, "", pool.Name); len(got) != 0 {
		t.Fatalf("NodePool lookup leaked missing NodeClass evidence: %+v", got)
	}
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	if got := issues.RelatedIssuesFrom(nil, grouped, issues.RelatedIssueOptions{CanReadRelated: access}, pool.Group, pool.Kind, "", pool.Name); len(got) != 1 {
		t.Fatalf("NodePool lookup omitted authorized missing NodeClass evidence: %+v", got)
	}
}

func initMCPRelatedIssueAuthDiscovery(t *testing.T) {
	t.Helper()
	k8s.ResetTestDynamicState()
	gvrKinds := map[schema.GroupVersionResource]string{
		{Group: karpenter.Group, Version: "v1", Resource: "nodepools"}:          "NodePoolList",
		{Group: "karpenter.k8s.aws", Version: "v1", Resource: "ec2nodeclasses"}: "EC2NodeClassList",
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrKinds)
	if err := k8s.InitTestDynamicResourceCache(dynamicClient, []k8s.APIResource{
		{Group: "widgets.example.com", Version: "v1", Kind: "Widget", Name: "widgets", Namespaced: true},
		{Group: karpenter.Group, Version: "v1", Kind: karpenter.NodePoolKind, Name: "nodepools", Namespaced: false},
		{Group: "karpenter.k8s.aws", Version: "v1", Kind: "EC2NodeClass", Name: "ec2nodeclasses", Namespaced: false},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
}

func TestTerminationInventoryFollowsReadGranularity(t *testing.T) {
	initMCPRelatedIssueAuthDiscovery(t)
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "inventory-reader"})
	perms := &pkgauth.UserPermissions{AllowedNamespaces: []string{"team"}}
	getPermCache().Set("inventory-reader", nil, perms)
	t.Cleanup(func() { getPermCache().Invalidate() })
	access := issueEvidenceAccess(ctx)
	for _, resource := range []struct{ group, resource string }{
		{"widgets.example.com", "widgets"}, {"", "pods"}, {"apps", "deployments"}, {"apps", "statefulsets"},
	} {
		perms.SetCanI("list", resource.group, resource.resource, "team", false)
		if !access(issues.EvidenceRead{Group: resource.group, Resource: resource.resource, Namespace: "team", Verb: "list"}) {
			t.Fatalf("namespace access must authorize namespaced inventory: %+v", resource)
		}
		if access(issues.EvidenceRead{Group: resource.group, Resource: resource.resource, Namespace: "other", Verb: "list"}) {
			t.Fatalf("another namespace's inventory was authorized: %+v", resource)
		}
	}
	for _, resource := range []struct{ group, resource string }{
		{karpenter.Group, "nodepools"}, {"", "pods"}, {"apps", "deployments"}, {"apps", "statefulsets"}, {"apps", "daemonsets"},
	} {
		read := issues.EvidenceRead{Group: resource.group, Resource: resource.resource, Verb: "list"}
		perms.SetCanI("list", resource.group, resource.resource, "", false)
		if access(read) {
			t.Fatalf("namespace access must not authorize cluster-wide inventory: %+v", read)
		}
		perms.SetCanI("list", resource.group, resource.resource, "", true)
		if !access(read) {
			t.Fatalf("readable cluster-wide inventory hidden: %+v", read)
		}
	}
}

func TestMCPIssuesHandlerUsesTerminationReadGranularity(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)

	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)
	gvr := schema.GroupVersionResource{Group: "widgets.example.com", Version: "v1", Resource: "widgets"}
	widget := &unstructured.Unstructured{}
	widget.SetAPIVersion(gvr.Group + "/" + gvr.Version)
	widget.SetKind("Widget")
	widget.SetName("stuck")
	widget.SetNamespace("team")
	widget.SetUID("widget-uid")
	widget.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-24 * time.Hour)))
	widget.SetDeletionTimestamp(&metav1.Time{Time: time.Now().Add(-2 * time.Hour)})
	widget.SetFinalizers([]string{"widgets.example.com/cleanup"})
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, widget)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dc.WaitForSync(gvr, 2*time.Second) {
		t.Fatal("Widget cache did not sync")
	}

	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "termination-reader"})
	perms := &pkgauth.UserPermissions{AllowedNamespaces: []string{"team"}}
	perms.SetCanI("get", gvr.Group, gvr.Resource, "team", true)
	getPermCache().Set("termination-reader", nil, perms)
	t.Cleanup(func() { getPermCache().Invalidate() })

	for _, allowed := range []bool{false, true} {
		perms.SetCanI("list", gvr.Group, gvr.Resource, "team", allowed)
		result, _, err := handleIssuesTool(ctx, nil, issuesInput{Namespace: "team", Kind: "Widget"})
		if err != nil {
			t.Fatal(err)
		}
		var response issuesapi.Response
		if err := json.Unmarshal([]byte(extractText(t, result)), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Issues) != 1 || response.Issues[0].Category != issuesapi.CategoryTerminationStuck || response.Issues[0].Name != "stuck" {
			t.Fatalf("namespace access must compose CR termination regardless of per-kind list permission: %+v", response.Issues)
		}
	}
}

// Agents read the finalizer removal as a conditional step with its own
// guidance, never as the issue's next step.
func TestMCPIssuesCarryConditionalCleanup(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)
	gvr := schema.GroupVersionResource{Group: "operator.victoriametrics.com", Version: "v1beta1", Resource: "vmagents"}
	agent := &unstructured.Unstructured{}
	agent.SetAPIVersion(gvr.Group + "/" + gvr.Version)
	agent.SetKind("VMAgent")
	agent.SetName("stuck")
	agent.SetNamespace("team")
	agent.SetUID("vmagent-uid")
	agent.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-24 * time.Hour)))
	agent.SetDeletionTimestamp(&metav1.Time{Time: time.Now().Add(-2 * time.Hour)})
	agent.SetFinalizers([]string{"apps.victoriametrics.com/finalizer"})
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "VMAgentList"}, agent)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "VMAgent", Name: gvr.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dc.WaitForSync(gvr, 2*time.Second) {
		t.Fatal("VMAgent cache did not sync")
	}

	result, _, err := handleIssuesTool(context.Background(), nil, issuesInput{Namespace: "team", Kind: "VMAgent"})
	if err != nil {
		t.Fatal(err)
	}
	text := extractText(t, result)
	var response struct {
		Issues []struct {
			Severity           string `json:"severity"`
			Action             string `json:"action"`
			ConditionalCleanup *struct {
				Condition     string `json:"condition"`
				AgentGuidance string `json:"agent_guidance"`
				Removals      []struct {
					PatchResource map[string]any `json:"patch_resource"`
				} `json:"removals"`
			} `json:"conditional_cleanup"`
		} `json:"issues"`
	}
	if err := json.Unmarshal([]byte(text), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Issues) != 1 {
		t.Fatalf("issues=%s", text)
	}
	issue := response.Issues[0]
	if issue.Severity != "warning" || strings.Contains(issue.Action, "patch_resource") || strings.Contains(issue.Action, "kubectl patch") {
		t.Fatalf("the next step must not carry the removal: %s", text)
	}
	cleanup := issue.ConditionalCleanup
	if cleanup == nil || !strings.HasPrefix(cleanup.Condition, "If victoria-metrics-operator was uninstalled on purpose (and nothing else should finalize this object)") || !strings.Contains(cleanup.AgentGuidance, "Propose this only after the user confirms the condition") || len(cleanup.Removals) != 1 || cleanup.Removals[0].PatchResource["dry_run"] != true {
		t.Fatalf("conditional_cleanup=%s", text)
	}
}

// Evidence is authorized at the granularity Radar reads it: namespace access
// for a namespace's inventories, a Secrets check for Secrets, the SAR for a
// cluster-wide read.
func TestMCPCachedIssuesAuthorizeEvidenceAtReadGranularity(t *testing.T) {
	ctx := withTestUserPerms(t, "evidence-user", nil, []string{"db"})
	perms := getPermCache().Get("evidence-user", nil)
	perms.SetCanI("list", "", "secrets", "db", false)
	perms.SetCanI("list", "", "nodes", "", false)
	options := issues.RelatedIssueOptions{CanReadEvidence: issueEvidenceAccess(ctx)}
	lookup := func(reads ...issues.EvidenceRead) int {
		issue := issues.Issue{ID: "derived", Group: "example.com", Kind: "Widget", Namespace: "db", Name: "w", RequiredReads: reads}
		return len(issues.RelatedIssuesFrom(nil, []issues.Issue{issue}, options, issue.Group, issue.Kind, issue.Namespace, issue.Name))
	}
	widgets := issues.EvidenceRead{Group: "example.com", Resource: "widgets", Namespace: "db", Verb: "list"}
	pods := issues.EvidenceRead{Resource: "pods", Namespace: "db", Verb: "list"}
	secrets := issues.EvidenceRead{Resource: "secrets", Namespace: "db", Verb: "list"}
	nodes := issues.EvidenceRead{Resource: "nodes", Verb: "list"}
	if lookup(widgets, pods) != 1 {
		t.Fatal("namespace access must authorize the namespace's inventories")
	}
	if lookup(issues.EvidenceRead{Resource: "pods", Namespace: "other", Verb: "list"}) != 0 {
		t.Fatal("another namespace's inventory authorized")
	}
	if lookup(pods, secrets) != 0 || lookup(nodes) != 0 {
		t.Fatal("Secrets or a cluster-wide inventory authorized without the SAR")
	}
	perms.SetCanI("list", "", "secrets", "db", true)
	perms.SetCanI("list", "", "nodes", "", true)
	if lookup(pods, secrets) != 1 || lookup(nodes) != 1 {
		t.Fatal("granted inventories withheld")
	}
}

func TestMCPIssueSubjectAccessFollowsTheDrawer(t *testing.T) {
	ctx := withTestUserPerms(t, "subject-user", nil, []string{"db"})
	perms := getPermCache().Get("subject-user", nil)
	perms.SetCanI("get", "", "secrets", "db", false)
	access := issueSubjectAccess(ctx)
	if !access(issues.Ref{Group: "apps", Kind: "Deployment", Namespace: "db", Name: "web"}) {
		t.Fatal("namespace access must open a Deployment's issues")
	}
	if access(issues.Ref{Group: "apps", Kind: "Deployment", Namespace: "other", Name: "web"}) {
		t.Fatal("another namespace's row authorized")
	}
	if access(issues.Ref{Kind: "Secret", Namespace: "db", Name: "creds"}) {
		t.Fatal("a Secret's issue authorized without Secret access")
	}
	perms.SetCanI("get", "", "secrets", "db", true)
	if !access(issues.Ref{Kind: "Secret", Namespace: "db", Name: "creds"}) {
		t.Fatal("Secret access did not open the Secret's issue")
	}
}
