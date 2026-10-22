package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Evidence is authorized at the granularity Radar reads it: namespace access
// for a namespace's inventories, a Secrets check for Secrets.
func TestRESTCachedIssuesAuthorizeEvidenceAtReadGranularity(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"db"}}
	perms.SetCanI("list", "", "secrets", "db", false)
	s.permCache.Set("evidence-user", nil, perms)
	r := requestWithUser(http.MethodGet, "/api/issues/resource/widget/db/w", &auth.User{Username: "evidence-user"})
	options := issues.RelatedIssueOptions{CanReadEvidence: s.issueEvidenceAccess(r)}
	lookup := func(reads ...issues.EvidenceRead) int {
		issue := issues.Issue{ID: "derived", Group: "example.com", Kind: "Widget", Namespace: "db", Name: "w", RequiredReads: reads}
		return len(issues.RelatedIssuesFrom(nil, []issues.Issue{issue}, options, issue.Group, issue.Kind, issue.Namespace, issue.Name))
	}
	widgets := issues.EvidenceRead{Group: "example.com", Resource: "widgets", Namespace: "db", Verb: "list"}
	pods := issues.EvidenceRead{Resource: "pods", Namespace: "db", Verb: "list"}
	secrets := issues.EvidenceRead{Resource: "secrets", Namespace: "db", Verb: "list"}
	if lookup(widgets, pods) != 1 {
		t.Fatal("namespace access must authorize the namespace's inventories")
	}
	if lookup(issues.EvidenceRead{Resource: "pods", Namespace: "other", Verb: "list"}) != 0 {
		t.Fatal("another namespace's inventory authorized")
	}
	if lookup(pods, secrets) != 0 {
		t.Fatal("Secrets authorized without Secret access")
	}
	perms.SetCanI("list", "", "secrets", "db", true)
	if lookup(pods, secrets) != 1 {
		t.Fatal("granted Secret inventory withheld")
	}
}

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

func TestTerminationInventoryFollowsReadGranularity(t *testing.T) {
	initRelatedIssueAuthDiscovery(t)
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	s.permCache.Set("inventory-reader", nil, perms)
	r := requestWithUser(http.MethodGet, "/api/issues?namespace=team", &auth.User{Username: "inventory-reader"})
	access := s.issueEvidenceAccess(r)
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

func TestRESTIssuesHandlerUsesTerminationReadGranularity(t *testing.T) {
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
		if len(response.Issues) != 1 || response.Issues[0].Category != issuesapi.CategoryTerminationStuck || response.Issues[0].Name != "stuck" {
			t.Fatalf("namespace access must compose CR termination regardless of per-kind list permission: %+v", response.Issues)
		}
	}
}

// The finalizer removal for a catalog controller Radar didn't find travels as
// a separate conditional field, never inside the issue's next step.
func TestRESTIssuesCarryConditionalCleanup(t *testing.T) {
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

	s := newAuthServer(auth.Config{})
	w := httptest.NewRecorder()
	s.handleIssues(w, httptest.NewRequest(http.MethodGet, "/api/issues?namespace=team&kind=VMAgent", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Issues []struct {
			Severity           string `json:"severity"`
			Action             string `json:"action"`
			ConditionalCleanup *struct {
				Condition     string `json:"condition"`
				AgentGuidance string `json:"agent_guidance"`
				Removals      []struct {
					Finalizer     string         `json:"finalizer"`
					PatchResource map[string]any `json:"patch_resource"`
					Kubectl       string         `json:"kubectl"`
				} `json:"removals"`
			} `json:"conditional_cleanup"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Issues) != 1 {
		t.Fatalf("issues=%s", w.Body.String())
	}
	issue := response.Issues[0]
	if issue.Severity != string(issuesapi.SeverityWarning) || !strings.HasPrefix(issue.Action, "No recognized controller found") || strings.Contains(issue.Action, "patch") {
		t.Fatalf("next step must restore the controller, not patch: %+v", issue)
	}
	cleanup := issue.ConditionalCleanup
	if cleanup == nil || !strings.HasPrefix(cleanup.Condition, "If victoria-metrics-operator was uninstalled on purpose") || !strings.Contains(cleanup.AgentGuidance, "only after the user confirms the condition") || len(cleanup.Removals) != 1 {
		t.Fatalf("conditional_cleanup=%s", w.Body.String())
	}
	if removal := cleanup.Removals[0]; removal.Finalizer != "apps.victoriametrics.com/finalizer" || removal.PatchResource["dry_run"] != true || removal.PatchResource["name"] != "stuck" || !strings.HasSuffix(removal.Kubectl, "--dry-run=server") {
		t.Fatalf("removal=%+v", removal)
	}
}

// A NetworkPolicy issue links the Services, workloads and Pods it isolates.
// Those links are namespaced, so in auth mode they follow the caller's
// namespace access: a caller without it neither sees the links nor matches
// the issue through them.
func TestRESTRelatedIssuesAuthorizeNetworkPolicyIsolationLinks(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("shop-user", nil, &auth.UserPermissions{AllowedNamespaces: []string{"shop"}})
	s.permCache.Set("other-user", nil, &auth.UserPermissions{AllowedNamespaces: []string{"other"}})
	svc := issues.Ref{Kind: "Service", Namespace: "shop", Name: "recommendation"}
	links := []issuesapi.Ref{svc, {Kind: "Pod", Namespace: "shop", Name: "rec-a"}}
	policyIssue := issues.Issue{
		ID: "netpol", Group: "networking.k8s.io", Kind: "NetworkPolicy", Namespace: "shop", Name: "deny-all",
		AffectedRefs: links,
		DiagnosticContext: &issuesapi.DiagnosticContext{Facts: []issuesapi.DiagnosticFact{{
			Type: "network_policy_isolates",
			Refs: links,
		}}},
	}
	lookup := func(user string) ([]issues.Issue, issues.Issue) {
		r := requestWithUser(http.MethodGet, "/api/issues/resource/service/shop/recommendation", &auth.User{Username: user})
		access := s.issueRelatedResourceAccess(r)
		shown := issues.RedactIssueRelatedRefs(policyIssue, access)
		return issues.RelatedIssuesFrom(nil, []issues.Issue{shown}, issues.RelatedIssueOptions{CanReadRelated: access}, svc.Group, svc.Kind, svc.Namespace, svc.Name), shown
	}
	if got, shown := lookup("shop-user"); len(got) != 1 || len(shown.DiagnosticContext.Facts[0].Refs) != 2 {
		t.Fatalf("caller with namespace access: related=%d shown=%+v", len(got), shown.DiagnosticContext)
	}
	if got, shown := lookup("other-user"); len(got) != 0 || shown.DiagnosticContext != nil {
		t.Fatalf("caller without namespace access matched or saw links: related=%d shown=%+v", len(got), shown.DiagnosticContext)
	}
}
