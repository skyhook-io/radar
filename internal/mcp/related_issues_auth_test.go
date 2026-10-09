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

func TestMCPCachedIssuesAuthorizeCNPGInventories(t *testing.T) {
	ctx := withTestUserPerms(t, "cnpg-evidence", nil, []string{"db"})
	perms := getPermCache().Get("cnpg-evidence", nil)
	perms.SetCanI("list", "postgresql.cnpg.io", "clusters", "db", true)
	perms.SetCanI("get", "", "pods", "db", true)
	perms.SetCanI("list", "", "pods", "db", false)
	perms.SetCanI("list", "other.example", "pods", "db", true)
	issue := issues.Issue{ID: "contradiction", Group: "postgresql.cnpg.io", Kind: "Cluster", Namespace: "db", Name: "pg", RequiredReads: []issues.EvidenceRead{
		{Group: "postgresql.cnpg.io", Resource: "clusters", Namespace: "db", Verb: "list"},
		{Resource: "pods", Namespace: "db", Verb: "list"},
	}}
	options := issues.RelatedIssueOptions{CanReadEvidence: issueEvidenceAccess(ctx)}
	lookup := func() []issues.Issue {
		return issues.RelatedIssuesFrom(nil, []issues.Issue{issue}, options, issue.Group, issue.Kind, issue.Namespace, issue.Name)
	}
	if len(lookup()) != 0 {
		t.Fatal("get Pods or list another group's Pods must not authorize the evidence")
	}
	perms.SetCanI("list", "", "pods", "db", true)
	if len(lookup()) != 1 {
		t.Fatal("authorized inventory withheld")
	}
	if options.CanReadEvidence(issues.EvidenceRead{Resource: "pods", Namespace: "other", Verb: "list"}) {
		t.Fatal("unread namespace authorized")
	}
}
