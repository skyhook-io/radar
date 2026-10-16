package issues

import (
	"fmt"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func netpolDetection(refs ...Ref) k8s.Detection {
	return k8s.Detection{
		Kind: "NetworkPolicy", Group: "networking.k8s.io", Namespace: "shop", Name: "deny-all",
		Severity: "warning", Reason: k8s.NetworkPolicyDenyAllIngressReason,
		Message:      "Would block all inbound traffic to the 2 pods behind Service recommendation",
		AffectedRefs: refs,
	}
}

var netpolAffected = []Ref{
	{Kind: "Service", Namespace: "shop", Name: "recommendation"},
	{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "recommendation"},
	{Kind: "Pod", Namespace: "shop", Name: "rec-a"},
}

// The policy issue shows up on every resource it isolates, through the same
// lookup the resource drawer, diagnose and get_resource use.
func TestNetworkPolicyIssueRelatesToIsolatedResources(t *testing.T) {
	p := &fakeProvider{problems: []k8s.Detection{netpolDetection(netpolAffected...)}}
	readAll := RelatedIssueOptions{CanReadRelated: func(Ref) bool { return true }}
	for _, ref := range append([]Ref{{Group: "networking.k8s.io", Kind: "NetworkPolicy", Namespace: "shop", Name: "deny-all"}}, netpolAffected...) {
		got := RelatedIssues(p, readAll, ref.Group, ref.Kind, ref.Namespace, ref.Name)
		if len(got) != 1 || got[0].Kind != "NetworkPolicy" || got[0].Category != issuesapi.CategoryNetworkPolicyBlock {
			t.Errorf("RelatedIssues(%s %s) = %+v, want the NetworkPolicy issue", ref.Kind, ref.Name, got)
		}
	}
	if got := RelatedIssues(p, readAll, "", "Service", "shop", "frontend"); len(got) != 0 {
		t.Errorf("RelatedIssues(unaffected Service) = %+v, want none", got)
	}
	// Without an authorizer the links fail closed, like every cross-resource
	// reference; the policy's own lookup still finds it.
	if got := RelatedIssues(p, RelatedIssueOptions{}, "", "Service", "shop", "recommendation"); len(got) != 0 {
		t.Errorf("RelatedIssues(Service, nil authorizer) = %+v, want none", got)
	}
}

// The detection's own fact survives the diagnostic-context enrichment that
// rebuilds every issue's context.
func TestNetworkPolicyIsolationFactSurvivesEnrichment(t *testing.T) {
	p := &fakeProvider{problems: []k8s.Detection{netpolDetection(netpolAffected...)}}
	got := Compose(p, Filters{Limit: NoLimit})
	if len(got) != 1 || got[0].DiagnosticContext == nil {
		t.Fatalf("Compose = %+v, want one issue with a diagnostic context", got)
	}
	ctx := got[0].DiagnosticContext
	if ctx.Role != issuesapi.DiagnosticRoleCandidate || len(ctx.Facts) != 1 || ctx.Facts[0].Type != factNetworkPolicyIsolates || len(ctx.Facts[0].Refs) != 3 {
		t.Fatalf("diagnostic context = %+v", ctx)
	}
}

// Links stop at the cap with Services and workloads first; the message says
// how many were left out.
func TestNetworkPolicyIsolationRefsCap(t *testing.T) {
	refs := append([]Ref(nil), netpolAffected[:2]...)
	for i := range 12 {
		refs = append(refs, Ref{Kind: "Pod", Namespace: "shop", Name: fmt.Sprintf("rec-%02d", i)})
	}
	ctx := networkPolicyIsolationContext(netpolDetection(refs...))
	fact := ctx.Facts[0]
	if len(fact.Refs) != maxNetworkPolicyIsolationRefs || fact.Refs[0].Kind != "Service" || fact.Refs[1].Kind != "Deployment" {
		t.Fatalf("refs = %+v", fact.Refs)
	}
	if !strings.Contains(fact.Message, "Only the first links are shown") || strings.ContainsAny(fact.Message, "0123456789") {
		t.Errorf("message = %q, want a cap note without counts", fact.Message)
	}

	// A resource past the display cap still finds the issue.
	p := &fakeProvider{problems: []k8s.Detection{netpolDetection(refs...)}}
	readAll := RelatedIssueOptions{CanReadRelated: func(Ref) bool { return true }}
	if got := RelatedIssues(p, readAll, "", "Pod", "shop", "rec-11"); len(got) != 1 {
		t.Errorf("RelatedIssues(Pod past the cap) = %d, want 1", len(got))
	}
	if networkPolicyIsolationContext(netpolDetection()) != nil {
		t.Error("a detection without refs got a context")
	}
}

// In auth mode a caller only sees, and is only matched through, links to
// resources they can read; the issue itself stays visible.
func TestNetworkPolicyIsolationRefsReadGate(t *testing.T) {
	p := &fakeProvider{problems: []k8s.Detection{netpolDetection(netpolAffected...)}}
	cannotReadPods := func(ref Ref) bool { return ref.Kind != "Pod" }
	opts := RelatedIssueOptions{CanReadRelated: cannotReadPods}

	if got := RelatedIssues(p, opts, "", "Pod", "shop", "rec-a"); len(got) != 0 {
		t.Errorf("matched through an unreadable Pod link: %+v", got)
	}
	got := RelatedIssues(p, opts, "", "Service", "shop", "recommendation")
	if len(got) != 1 {
		t.Fatalf("RelatedIssues(Service) = %+v, want the issue", got)
	}
	for _, ref := range got[0].DiagnosticContext.Facts[0].Refs {
		if ref.Kind == "Pod" {
			t.Errorf("unreadable Pod link leaked: %+v", ref)
		}
	}

	readNothing := Compose(p, Filters{Limit: NoLimit, CanReadRelated: func(Ref) bool { return false }})
	if len(readNothing) != 1 || readNothing[0].DiagnosticContext != nil {
		t.Errorf("with no readable links the issue should stay and lose its fact, got %+v", readNothing)
	}
}

// A user-agnostic composed pair (a shared memo) still returns only the links
// the caller can read.
func TestRelatedIssuesFromRedactsIsolationLinks(t *testing.T) {
	p := &fakeProvider{problems: []k8s.Detection{netpolDetection(netpolAffected...)}}
	flat := Compose(p, Filters{Limit: NoLimit})
	grouped := GroupIssues(flat)
	noPods := RelatedIssueOptions{CanReadRelated: func(ref Ref) bool { return ref.Kind != "Pod" }}
	got := RelatedIssuesFrom(flat, grouped, noPods, "", "Service", "shop", "recommendation")
	if len(got) != 1 {
		t.Fatalf("RelatedIssuesFrom = %+v, want the issue", got)
	}
	for _, ref := range append(append([]Ref(nil), got[0].AffectedRefs...), got[0].DiagnosticContext.Facts[0].Refs...) {
		if ref.Kind == "Pod" {
			t.Errorf("unreadable Pod link returned: %+v", ref)
		}
	}
}

// A resource's issue summary names the policy when the top issue is about it,
// so an agent reading the Service's context needs no second lookup.
func TestSummarySubjectNamesTheOtherSubject(t *testing.T) {
	p := &fakeProvider{problems: []k8s.Detection{netpolDetection(netpolAffected...)}}
	readAll := RelatedIssueOptions{CanReadRelated: func(Ref) bool { return true }}
	got := RelatedIssues(p, readAll, "", "Service", "shop", "recommendation")
	if len(got) != 1 {
		t.Fatalf("RelatedIssues = %d, want 1", len(got))
	}
	ref := SummarySubject(got[0], "", "Service", "shop", "recommendation")
	if ref == nil || ref.Kind != "NetworkPolicy" || ref.Name != "deny-all" || ref.Group != "networking.k8s.io" {
		t.Fatalf("SummarySubject = %+v, want the NetworkPolicy", ref)
	}
	if SummarySubject(got[0], "networking.k8s.io", "NetworkPolicy", "shop", "deny-all") != nil {
		t.Error("the policy's own summary named itself as another subject")
	}
}

// A Pod's own grouped Deployment issue isn't "another subject".
func TestOtherSubjectSkipsOwnGroup(t *testing.T) {
	p := &fakeProvider{problems: []k8s.Detection{
		{Kind: "Pod", Namespace: "prod", Name: "web-abc-1", Reason: "CrashLoopBackOff", Severity: "critical",
			OwnerGroup: "apps", OwnerKind: "Deployment", OwnerName: "web"},
	}}
	got := RelatedIssues(p, RelatedIssueOptions{}, "", "Pod", "prod", "web-abc-1")
	if len(got) != 1 {
		t.Fatalf("RelatedIssues = %d, want 1", len(got))
	}
	if ref := OtherSubject(got[0], "", "Pod", "prod", "web-abc-1"); ref != nil {
		t.Fatalf("OtherSubject = %+v, want nil for the Pod's own Deployment issue", ref)
	}
}
