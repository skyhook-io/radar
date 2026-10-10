package mcp

import (
	"context"
	"encoding/json"
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

func TestTerminationInventoryRequiresExactListPermissions(t *testing.T) {
	initMCPRelatedIssueAuthDiscovery(t)
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "inventory-reader"})
	perms := &pkgauth.UserPermissions{AllowedNamespaces: []string{"team"}}
	getPermCache().Set("inventory-reader", nil, perms)
	t.Cleanup(func() { getPermCache().Invalidate() })
	access := mcpChangeAuthorizer(ctx)
	for _, tc := range []struct{ group, resource, namespace string }{
		{"widgets.example.com", "widgets", "team"}, {"", "pods", "team"}, {"apps", "deployments", "team"}, {"apps", "statefulsets", "team"}, {karpenter.Group, "nodepools", ""},
	} {
		perms.SetCanI("get", tc.group, tc.resource, tc.namespace, true)
		perms.SetCanI("list", tc.group, tc.resource, tc.namespace, false)
		if access(tc.group, tc.resource, tc.namespace) {
			t.Fatalf("namespace/get access exposed denied inventory: %+v", tc)
		}
		perms.SetCanI("list", tc.group, tc.resource, tc.namespace, true)
		if !access(tc.group, tc.resource, tc.namespace) {
			t.Fatalf("readable inventory hidden: %+v", tc)
		}
	}
}

func TestMCPIssuesHandlerPassesTerminationListAuthorizer(t *testing.T) {
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
		if !allowed {
			if len(response.Issues) != 0 {
				t.Fatalf("denied subject inventory leaked: %+v", response.Issues)
			}
			continue
		}
		if len(response.Issues) != 1 || response.Issues[0].Category != issuesapi.CategoryTerminationStuck || response.Issues[0].Name != "stuck" {
			t.Fatalf("authorized handler must compose CR termination with CanListResource: %+v", response.Issues)
		}
	}
}
