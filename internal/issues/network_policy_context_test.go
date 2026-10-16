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
	for _, ref := range append([]Ref{{Group: "networking.k8s.io", Kind: "NetworkPolicy", Namespace: "shop", Name: "deny-all"}}, netpolAffected...) {
		got := RelatedIssues(p, RelatedIssueOptions{}, ref.Group, ref.Kind, ref.Namespace, ref.Name)
		if len(got) != 1 || got[0].Kind != "NetworkPolicy" || got[0].Category != issuesapi.CategoryNetworkPolicyBlock {
			t.Errorf("RelatedIssues(%s %s) = %+v, want the NetworkPolicy issue", ref.Kind, ref.Name, got)
		}
	}
	if got := RelatedIssues(p, RelatedIssueOptions{}, "", "Service", "shop", "frontend"); len(got) != 0 {
		t.Errorf("RelatedIssues(unaffected Service) = %+v, want none", got)
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
	if !strings.Contains(fact.Message, fmt.Sprintf("Showing %d of %d links", maxNetworkPolicyIsolationRefs, len(refs))) {
		t.Errorf("message = %q", fact.Message)
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
