package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
		{Group: "widgets.example.com", Version: "v1", Kind: "Widget", Name: "widgets", Namespaced: true},
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

func TestRESTPodTemplateEvidenceRequiresKindRead(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	s.permCache.Set("template-reader", nil, perms)
	r := requestWithUser(http.MethodGet, "/api/issues", &auth.User{Username: "template-reader"})
	access := s.issueRelatedResourceAccess(r)
	for _, tc := range []struct{ kind, group, resource string }{{"ReplicaSet", "apps", "replicasets"}, {"Job", "batch", "jobs"}, {"LimitRange", "", "limitranges"}} {
		ref := issues.Ref{Kind: tc.kind, Group: tc.group, Namespace: "test", Name: "evidence"}
		perms.SetCanI("get", tc.group, tc.resource, "test", false)
		if access(ref) {
			t.Fatalf("namespace visibility exposed denied %s", tc.kind)
		}
		perms.SetCanI("get", tc.group, tc.resource, "test", true)
		if !access(ref) {
			t.Fatalf("readable %s hidden", tc.kind)
		}
	}
}

func TestTerminationInventoryRequiresExactListPermissions(t *testing.T) {
	initRelatedIssueAuthDiscovery(t)
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	s.permCache.Set("inventory-reader", nil, perms)
	r := requestWithUser(http.MethodGet, "/api/issues?namespace=team", &auth.User{Username: "inventory-reader"})
	access := s.changeAuthorizerForCtx(r.Context())
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

func TestRESTIssuesHandlerPassesTerminationListAuthorizer(t *testing.T) {
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

	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	perms.SetCanI("get", gvr.Group, gvr.Resource, "team", true)
	s.permCache.Set("termination-reader", nil, perms)

	for _, allowed := range []bool{false, true} {
		perms.SetCanI("list", gvr.Group, gvr.Resource, "team", allowed)
		r := requestWithUser(http.MethodGet, "/api/issues?namespace=team&kind=Widget", &auth.User{Username: "termination-reader"})
		w := httptest.NewRecorder()
		s.handleIssues(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var response issuesapi.Response
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
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
