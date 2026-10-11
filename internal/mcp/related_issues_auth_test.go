package mcp

import (
	"context"
	"testing"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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
		{Group: karpenter.Group, Version: "v1", Kind: karpenter.NodePoolKind, Name: "nodepools", Namespaced: false},
		{Group: "karpenter.k8s.aws", Version: "v1", Kind: "EC2NodeClass", Name: "ec2nodeclasses", Namespaced: false},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
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
