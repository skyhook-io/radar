package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/trace"
)

// buildNetworkDiagnoseResponse must project a finalized Trace into the coverage-
// honest agent shape: a counts+headline summary, a NAMED broken route (never a
// raw index), per-route + notTested arrays, the path with its static findings
// preserved, and NO duplicated relatedIssues.
func TestBuildNetworkDiagnoseResponse(t *testing.T) {
	ghost := trace.ResourceRef{Kind: "Service", Name: "ghost"}
	tr := &trace.Trace{
		Subject:     trace.ResourceRef{Kind: "Ingress", Name: "shop"},
		Verdict:     "degraded",
		Reason:      "1 of 2 routes broken",
		BrokenAt:    2,
		BrokenRoute: &ghost,
		Coverage:    &trace.Coverage{Tested: 2, Passed: 1, Failed: 1},
		Routes: []trace.RouteResult{
			{Route: "/web", Target: "web:80", Outcome: trace.OutcomeVerified, Confidence: trace.ConfidenceReal},
			{Route: "/api", Target: "ghost", Outcome: trace.OutcomeUnreachable, Confidence: trace.ConfidenceReal},
		},
		NotTested: []trace.RouteSkip{{Route: "*.cdn", Reason: "wildcard host", ReasonClass: trace.SkipClassCoverage}},
		Downstream: []trace.Hop{
			{Resource: trace.ResourceRef{Kind: "Ingress", Name: "shop"}, Edge: "entry:Ingress",
				Findings: []trace.Finding{{Code: "missing_ref:x", Severity: "critical", Message: "/api references ghost which does not exist"}}},
			{Resource: trace.ResourceRef{Kind: "Service", Name: "web"}, Edge: "Ingress->Service"},
			{Resource: ghost, Edge: "Ingress->Service"},
		},
	}

	// The real flow sets tr.Headline in computeCoverage; mirror that - the MCP
	// now reads the trace's shared headline rather than recomputing it.
	tr.Headline = trace.CoverageHeadline(tr)
	resp := buildNetworkDiagnoseResponse(tr)

	if resp.Summary.Tested != 2 || resp.Summary.Passed != 1 || resp.Summary.Failed != 1 {
		t.Errorf("summary = %+v, want tested 2 passed 1 failed 1", resp.Summary)
	}
	if !strings.Contains(resp.Summary.Headline, "1 of 2 routes reachable") {
		t.Errorf("headline = %q, want a coverage sentence", resp.Summary.Headline)
	}
	if resp.BrokenRoute == nil || resp.BrokenRoute.Name != "ghost" {
		t.Errorf("brokenRoute = %+v, want the NAMED route 'ghost' (not a raw index)", resp.BrokenRoute)
	}
	if len(resp.Routes) != 2 || len(resp.NotTested) != 1 {
		t.Errorf("routes=%d notTested=%d, want 2 and 1", len(resp.Routes), len(resp.NotTested))
	}

	// Static findings must survive in path (not lost when we drop the raw Trace dump).
	foundMissingRef := false
	for _, h := range resp.Path {
		for _, f := range h.Findings {
			if f.Code == "missing_ref:x" {
				foundMissingRef = true
			}
		}
	}
	if !foundMissingRef {
		t.Error("path must preserve the entry hop's static findings (missing-ref)")
	}

	// No duplicated relatedIssues on the network response.
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "relatedIssues") {
		t.Error("network response must not carry the duplicated relatedIssues list")
	}
	// And no raw verbose probe dump leaked onto the slimmed hops.
	if strings.Contains(string(b), "\"probes\"") {
		t.Error("slimmed path hops must not carry the verbose per-probe dump")
	}
}

// Upstream scoping: an upstream entry's missing-ref findings are about ITS
// sibling routes, not the subject - they must not leak into the subject's
// response. Other upstream findings (its own problems) are preserved.
func TestBuildNetworkDiagnoseResponse_UpstreamScoping(t *testing.T) {
	tr := &trace.Trace{
		Subject:    trace.ResourceRef{Kind: "Service", Name: "echo"},
		Verdict:    "healthy",
		BrokenAt:   -1,
		Coverage:   &trace.Coverage{Tested: 1, Passed: 1},
		Downstream: []trace.Hop{{Resource: trace.ResourceRef{Kind: "Service", Name: "echo"}, Edge: "entry:Service"}},
		Upstreams: []trace.Hop{
			{Resource: trace.ResourceRef{Kind: "Ingress", Name: "multi"}, Edge: "Ingress->Service",
				Findings: []trace.Finding{
					{Code: "missing_ref:ghost", Severity: "critical", Message: "/api references ghost which does not exist"},
					{Code: "ingress:no-class", Severity: "warning", Message: "no ingressClassName"},
				}},
		},
	}
	tr.Headline = trace.CoverageHeadline(tr)
	resp := buildNetworkDiagnoseResponse(tr)

	b, _ := json.Marshal(resp)
	if strings.Contains(string(b), "missing_ref:ghost") || strings.Contains(string(b), "ghost which does not exist") {
		t.Error("upstream's sibling-route missing-ref finding leaked into the subject's response")
	}
	// The upstream's OWN (non-missing-ref) finding must survive.
	if !strings.Contains(string(b), "ingress:no-class") {
		t.Error("upstream scoping must keep the upstream's own non-missing-ref findings")
	}
}

// The trace's computed Diagnosis is surfaced on the agent response as the lead
// read (cause/culprit/next-action), serialized under "diagnosis".
func TestBuildNetworkDiagnoseResponse_DiagnosisExposed(t *testing.T) {
	pod := trace.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "app-xyz"}
	tr := &trace.Trace{
		Subject:  trace.ResourceRef{Kind: "Service", Namespace: "prod", Name: "crash"},
		Verdict:  trace.VerdictBroken,
		BrokenAt: -1,
		Diagnosis: &trace.Diagnosis{
			Summary:         "Container 'app' keeps crashing",
			CulpritResource: &pod,
			NextAction:      "Inspect the previous container's logs",
			Command:         "kubectl logs app-xyz -n prod --previous",
		},
	}
	tr.Headline = trace.CoverageHeadline(tr)
	resp := buildNetworkDiagnoseResponse(tr)
	if resp.Diagnosis == nil || resp.Diagnosis.CulpritResource == nil || resp.Diagnosis.CulpritResource.Name != "app-xyz" {
		t.Fatalf("Diagnosis must be surfaced with its culprit, got %+v", resp.Diagnosis)
	}
	b, _ := json.Marshal(resp)
	if !strings.Contains(string(b), "\"diagnosis\"") {
		t.Error("response JSON must carry the diagnosis under \"diagnosis\"")
	}
}

// TestInClusterParam_SelfExplanatory pins that the in_cluster arg's schema teaches
// an agent WHAT it does, WHEN to use it, and that it CREATES a pod - so the agent
// can decide for itself, not just be told a next step it can't take.
func TestInClusterParam_SelfExplanatory(t *testing.T) {
	var found string
	for _, f := range reflect.VisibleFields(reflect.TypeOf(diagnoseInput{})) {
		if f.Tag.Get("json") == "in_cluster,omitempty" {
			found = f.Tag.Get("jsonschema")
		}
	}
	if found == "" {
		t.Fatal("diagnoseInput must have an in_cluster field with a jsonschema description")
	}
	for _, kw := range []string{"INSIDE the cluster", "pod", "confidence:indirect", "create"} {
		if !strings.Contains(found, kw) {
			t.Errorf("in_cluster schema should mention %q so an agent knows what/when; got: %s", kw, found)
		}
	}
}

// A broken/degraded verdict must carry its cause in Reason - an entry-path break
// (all upstreams unreachable while the subject itself is healthy) is probe-derived
// with no finding, so without Reason an agent sees verdict=broken beside a
// "Reachable" headline and no cause.
func TestBuildNetworkDiagnoseResponse_BrokenFromUpstreamIncludesReason(t *testing.T) {
	tr := &trace.Trace{
		Subject:  trace.ResourceRef{Kind: "Service", Name: "echo"},
		Verdict:  trace.VerdictBroken,
		Reason:   "all entry paths into this resource are unreachable; the resource itself looks healthy",
		BrokenAt: -1,
		Coverage: &trace.Coverage{Tested: 1, Passed: 1},
		Routes: []trace.RouteResult{
			{Route: "echo", Target: "echo:80", Outcome: trace.OutcomeVerified, Confidence: trace.ConfidenceReal},
		},
	}
	tr.Headline = trace.CoverageHeadline(tr)
	resp := buildNetworkDiagnoseResponse(tr)
	if !strings.Contains(resp.Reason, "all entry paths") {
		t.Errorf("a broken verdict must carry its cause in Reason, got %q (headline %q)", resp.Reason, resp.Summary.Headline)
	}
}
