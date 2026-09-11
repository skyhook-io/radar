package server

import (
	"net/http"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestRESTRelatedIssuesAuthorizeNodeClassSubject(t *testing.T) {
	initRelatedIssueAuthDiscovery(t)
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("get", karpenter.Group, "nodepools", "", true)
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	s.permCache.Set("alice", nil, perms)
	r := requestWithUser(http.MethodGet, "/api/issues/resource/nodepool/_/compute", &auth.User{Username: "alice"})
	access := s.issueRelatedResourceAccess(r)

	pool := issues.Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: "compute"}
	nodeClass := issues.Ref{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass", Name: "default"}
	if !access(pool) || access(nodeClass) {
		t.Fatalf("related access: pool=%v nodeClass=%v, want allowed/denied", access(pool), access(nodeClass))
	}

	grouped := relatedNodeClassIssueForAuthTest(pool, nodeClass)
	if got := issues.RelatedIssuesFrom(nil, grouped, issues.RelatedIssueOptions{CanReadRelated: access}, pool.Group, pool.Kind, pool.Namespace, pool.Name); len(got) != 0 {
		t.Fatalf("NodePool lookup leaked denied NodeClass issue: %+v", got)
	}

	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	if got := issues.RelatedIssuesFrom(nil, grouped, issues.RelatedIssueOptions{CanReadRelated: access}, pool.Group, pool.Kind, pool.Namespace, pool.Name); len(got) != 1 {
		t.Fatalf("NodePool lookup omitted authorized NodeClass issue: %+v", got)
	}
}

func TestRESTRelatedIssuesAuthorizeMissingNodeClassEvidence(t *testing.T) {
	initRelatedIssueAuthDiscovery(t)
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("get", karpenter.Group, "nodepools", "", true)
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	s.permCache.Set("alice", nil, perms)
	r := requestWithUser(http.MethodGet, "/api/issues/resource/nodepool/_/compute", &auth.User{Username: "alice"})
	access := s.issueRelatedResourceAccess(r)
	pool := issues.Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: "compute"}
	nodeClass := issues.Ref{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass", Name: "missing"}
	grouped := relatedMissingNodeClassIssueForAuthTest(pool, nodeClass)
	if got := issues.RelatedIssuesFrom(nil, grouped, issues.RelatedIssueOptions{CanReadRelated: access}, pool.Group, pool.Kind, "", pool.Name); len(got) != 0 {
		t.Fatalf("NodePool lookup leaked missing NodeClass evidence: %+v", got)
	}
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	if got := issues.RelatedIssuesFrom(nil, grouped, issues.RelatedIssueOptions{CanReadRelated: access}, pool.Group, pool.Kind, "", pool.Name); len(got) != 1 {
		t.Fatalf("NodePool lookup omitted authorized missing NodeClass evidence: %+v", got)
	}
}

func initRelatedIssueAuthDiscovery(t *testing.T) {
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

func relatedNodeClassIssueForAuthTest(pool, nodeClass issues.Ref) []issues.Issue {
	return []issues.Issue{{
		ID: "nodeclass-not-ready", Group: nodeClass.Group, Kind: nodeClass.Kind, Name: nodeClass.Name,
		DiagnosticContext: &issuesapi.DiagnosticContext{Facts: []issuesapi.DiagnosticFact{{
			Type: "karpenter_referenced_by_nodepools", Refs: []issuesapi.Ref{pool},
		}}},
	}}
}

func relatedMissingNodeClassIssueForAuthTest(pool, nodeClass issues.Ref) []issues.Issue {
	return []issues.Issue{{
		ID: "nodeclass-missing", Group: pool.Group, Kind: pool.Kind, Name: pool.Name,
		DiagnosticContext: &issuesapi.DiagnosticContext{Facts: []issuesapi.DiagnosticFact{{
			Type: "karpenter_referenced_nodeclass", Refs: []issuesapi.Ref{nodeClass},
		}}},
	}}
}
