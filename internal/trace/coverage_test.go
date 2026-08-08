package trace

import (
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/probe"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestVantageAPIServerName pins the ONE operator-facing name for the apiserver-
// proxy vantage and asserts the headline generator actually uses it. The TS side
// (reachVerdict.test.ts) pins the identical literal on VIA_API_SERVER - the two
// pins are the anti-drift gate that keeps the Go + TS headline generators from
// diverging on this term.
func TestVantageAPIServerName(t *testing.T) {
	if VantageAPIServer != "via API server" {
		t.Fatalf("VantageAPIServer = %q, want \"via API server\" (must stay identical to TS VIA_API_SERVER)", VantageAPIServer)
	}
	h := singleRouteHeadline(RouteResult{Outcome: OutcomeReached, Confidence: ConfidenceIndirect}, 0, "routes")
	if !strings.Contains(h, VantageAPIServer) {
		t.Errorf("indirect-route headline %q must name the vantage %q", h, VantageAPIServer)
	}
}

// A healthy Gateway with attached HTTPRoute/GRPCRoute must NOT false-condemn the
// attached routes as unreachable backends: those are parallel entry paths (each
// traced as its own subject), carry no Config/probes here, and the Gateway's own
// front-door reach is what coverage reports. Regression guard for the blocker
// where Gateway->Route branches with nil Config were marked OutcomeUnreachable.
func TestComputeCoverage_GatewayAttachedRoutesNotCondemned(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Group: "gateway.networking.k8s.io", Kind: "Gateway", Namespace: "prod", Name: "gw"},
		Verdict:  VerdictHealthy,
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Group: "gateway.networking.k8s.io", Kind: "Gateway", Namespace: "prod", Name: "gw"},
				Edge:   "entry:Gateway",
				Config: &HopConfig{Listeners: []GatewayListener{{Name: "http", Port: 80, Protocol: "HTTP"}}, Addresses: []string{"203.0.113.7"}},
				Probes: []probe.Result{{Layer: probe.LayerTCP, Path: probe.PathData, Port: 80, OK: true, Tone: probe.ToneHealthy, Detail: "TCP connect OK", Vantage: probe.VantageInCluster}}},
			{Resource: ResourceRef{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute", Namespace: "prod", Name: "web-route"},
				Edge:   "Gateway->HTTPRoute",
				Probes: []probe.Result{probe.Skipped(probe.LayerTCP, "", probe.VantageInCluster, "route has no own address; reachability lives on parent Gateway and backend Service")}},
			{Resource: ResourceRef{Group: "gateway.networking.k8s.io", Kind: "GRPCRoute", Namespace: "prod", Name: "grpc-route"},
				Edge:   "Gateway->GRPCRoute",
				Probes: []probe.Result{probe.Skipped(probe.LayerTCP, "", probe.VantageInCluster, "route has no own address; reachability lives on parent Gateway and backend Service")}},
		},
	}
	computeCoverage(tr)
	for _, r := range tr.Routes {
		if r.Outcome == OutcomeUnreachable {
			t.Errorf("attached-route handling must not condemn a route as unreachable; got %+v", r)
		}
	}
	if strings.HasPrefix(tr.Headline, "Unreachable") || strings.Contains(tr.Headline, "None of") || strings.Contains(tr.Headline, "0 of") {
		t.Errorf("headline must not be a false condemnation; got %q", tr.Headline)
	}
	if v := CoverageVerdict(tr); v == VerdictBroken {
		t.Errorf("a healthy Gateway must not read broken; got %q", v)
	}
}

// Test 1 - a single Service route verified over the real-traffic (data) path.
func TestComputeCoverage_SingleRouteVerifiedReal(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "api"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "api"}, Edge: "entry:Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathData, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"}}},
			{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 1 {
		t.Fatalf("want 1 route, got %d (%+v)", len(tr.Routes), tr.Routes)
	}
	r := tr.Routes[0]
	if r.Outcome != OutcomeVerified || r.Confidence != ConfidenceReal {
		t.Errorf("route = %s/%s, want verified/real", r.Outcome, r.Confidence)
	}
	if r.Target != "api:80" {
		t.Errorf("target = %q, want api:80", r.Target)
	}
	if tr.Coverage == nil || tr.Coverage.Tested != 1 || tr.Coverage.Passed != 1 || tr.Coverage.Failed != 0 {
		t.Errorf("coverage = %+v, want tested 1 passed 1 failed 0", tr.Coverage)
	}
}

// A 0-ready-endpoints break is an authoritative cache fact, so a Service route
// that's only "unreachable via the apiserver proxy" (indirect) must be promoted
// to a DEFINITIVE (real) failure - it reads red, not the soft proxy-vantage amber.
func TestUpgradeDefinitiveBackendDown(t *testing.T) {
	svc := func(sev, code string, routes ...RouteResult) *Trace {
		return &Trace{
			Subject:    ResourceRef{Kind: "Service", Namespace: "p", Name: "s"},
			BrokenAt:   -1,
			Downstream: []Hop{{Resource: ResourceRef{Kind: "Service", Name: "s"}, Findings: []Finding{{Code: code, Severity: sev, Message: "no ready"}}}},
			Routes:     routes,
		}
	}
	ind := func(id string) RouteResult {
		return RouteResult{Route: id, Outcome: OutcomeUnreachable, Confidence: ConfidenceIndirect}
	}

	cases := []struct {
		name     string
		tr       *Trace
		mutate   func(*Trace)
		wantReal []bool // expected per-route: true = promoted to real
	}{
		{"critical problem:0/N promotes", svc(SeverityCritical, "problem:0/1 selected pods ready", ind("s")), nil, []bool{true}},
		{"fingerprint code promotes", svc(SeverityCritical, "svc:no-ready-endpoints", ind("s")), nil, []bool{true}},
		{"multi-port: every port promoted (same backend)", svc(SeverityCritical, "problem:0/2 selected pods ready", ind("80"), ind("9090")), nil, []bool{true, true}},
		{"uncertain WARNING stays soft (couldn't verify scale-to-0)", svc(SeverityWarning, "problem:0/1 selected pods ready", ind("s")), nil, []bool{false}},
		{"non-Service subject untouched", svc(SeverityCritical, "problem:0/1 selected pods ready", ind("s")), func(tr *Trace) { tr.Subject.Kind = "Ingress" }, []bool{false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.mutate != nil {
				c.mutate(c.tr)
			}
			upgradeDefinitiveBackendDown(c.tr)
			for i, want := range c.wantReal {
				got := c.tr.Routes[i].Confidence == ConfidenceReal
				if got != want {
					t.Errorf("route[%d] promoted=%v, want %v (confidence=%q)", i, got, want, c.tr.Routes[i].Confidence)
				}
			}
		})
	}
}

// An ExternalName Service is honestly testable: the Service hop has no
// ClusterIP/ports (no own probes), but the alias-host hop carries the real
// DNS-resolve + HTTP-reach probes. Those must produce ONE verified route to the
// external host - not an empty "configuration only" coverage.
func TestComputeCoverage_ExternalNameRouteFromAliasHop(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "extname"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "extname"}, Edge: "entry:Service"},
			{Resource: ResourceRef{Kind: "ExternalName", Name: "example.com"}, Edge: "Service->ExternalName",
				Probes: []probe.Result{
					{Layer: probe.LayerDNS, Path: probe.PathData, Vantage: probe.VantageInCluster, OK: true, Tone: probe.ToneHealthy, Detail: "resolved to 93.184.216.34"},
					{Layer: probe.LayerHTTP, Path: probe.PathData, Vantage: probe.VantageInCluster, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"},
				}},
		},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 1 {
		t.Fatalf("want 1 route, got %d (%+v)", len(tr.Routes), tr.Routes)
	}
	r := tr.Routes[0]
	// In-cluster vantage proves real reachability → verified/real.
	if r.Outcome != OutcomeVerified || r.Confidence != ConfidenceReal {
		t.Errorf("route = %s/%s, want verified/real", r.Outcome, r.Confidence)
	}
	if r.Target != "example.com" {
		t.Errorf("target = %q, want example.com", r.Target)
	}
	if tr.Coverage == nil || tr.Coverage.Tested != 1 || tr.Coverage.Passed != 1 {
		t.Errorf("coverage = %+v, want tested 1 passed 1", tr.Coverage)
	}
}

// A laptop dials the external host from Radar's own network - that is NOT proof of
// real in-cluster reachability, so the route must be INDIRECT (never a green "real").
func TestComputeCoverage_ExternalNameLocalVantageIsIndirect(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "extname"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "extname"}, Edge: "entry:Service"},
			{Resource: ResourceRef{Kind: "ExternalName", Name: "example.com"}, Edge: "Service->ExternalName",
				Probes: []probe.Result{
					{Layer: probe.LayerDNS, Path: probe.PathData, Vantage: probe.VantageLocal, OK: true, Tone: probe.ToneHealthy, Detail: "resolved"},
					{Layer: probe.LayerHTTP, Path: probe.PathData, Vantage: probe.VantageLocal, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"},
				}},
		},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 1 {
		t.Fatalf("want 1 route, got %d", len(tr.Routes))
	}
	if r := tr.Routes[0]; r.Confidence != ConfidenceIndirect {
		t.Errorf("local-vantage ExternalName confidence = %q, want indirect (must not over-claim real in-cluster reachability)", r.Confidence)
	}
}

// An un-probed ExternalName stays config-only - no route fabricated without probes.
func TestComputeCoverage_ExternalNameUnprobedHasNoRoute(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "extname"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "extname"}, Edge: "entry:Service"},
			{Resource: ResourceRef{Kind: "ExternalName", Name: "example.com"}, Edge: "Service->ExternalName"},
		},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 0 {
		t.Fatalf("want 0 routes for un-probed ExternalName, got %d (%+v)", len(tr.Routes), tr.Routes)
	}
}

// Test 2 - multi-route Ingress, one route reachable, one unreachable.
func TestComputeCoverage_MultiRoutePartial(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Ingress", Name: "shop"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Name: "shop"}, Edge: "entry:Ingress",
				Config: &HopConfig{Hostnames: []string{"shop.example.com"}, Rules: []RouteRule{
					{Hosts: []string{"shop.example.com"}, Paths: []string{"/web"}, Backends: []BackendRef{{Kind: "Service", Name: "web"}}},
					{Hosts: []string{"shop.example.com"}, Paths: []string{"/api"}, Backends: []BackendRef{{Kind: "Service", Name: "api"}}},
				}}},
			{Resource: ResourceRef{Kind: "Service", Name: "web"}, Edge: "Ingress->Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathData, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"}}},
			{Resource: ResourceRef{Kind: "Service", Name: "api"}, Edge: "Ingress->Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 8080}}},
				Probes: []probe.Result{{Layer: probe.LayerTCP, Path: probe.PathData, OK: false, Tone: probe.ToneUnhealthy, Error: "connection refused"}}},
		},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 2 {
		t.Fatalf("want 2 routes, got %d (%+v)", len(tr.Routes), tr.Routes)
	}
	byRoute := map[string]RouteResult{}
	for _, r := range tr.Routes {
		byRoute[r.Route] = r
	}
	if r := byRoute["/web"]; r.Outcome != OutcomeVerified {
		t.Errorf("/web = %q, want verified", r.Outcome)
	}
	if r := byRoute["/api"]; r.Outcome != OutcomeUnreachable {
		t.Errorf("/api = %q, want unreachable", r.Outcome)
	}
	if tr.Coverage.Tested != 2 || tr.Coverage.Passed != 1 || tr.Coverage.Failed != 1 {
		t.Errorf("coverage = %+v, want tested 2 passed 1 failed 1", tr.Coverage)
	}
}

// Test 3 - apiserver-only success must read INDIRECT, never real-traffic verified.
func TestComputeCoverage_ApiserverOnlyIsIndirect(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Name: "api"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "api"}, Edge: "entry:Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathAPIServer, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"}}},
			{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 1 {
		t.Fatalf("want 1 route, got %d", len(tr.Routes))
	}
	r := tr.Routes[0]
	if r.Confidence != ConfidenceIndirect {
		t.Errorf("apiserver-only confidence = %q, want indirect (must NOT be real)", r.Confidence)
	}
	if r.Confidence == ConfidenceReal {
		t.Errorf("apiserver-only must never render as real-traffic verified")
	}
	if len(r.Localization) == 0 {
		t.Errorf("apiserver probe should be recorded as localization, got none")
	}
}

// Test 4 - skips are listed and classified: coverage gap vs benign vs vantage.
func TestComputeCoverage_NotTestedClassification(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Ingress", Name: "x"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Name: "x"}, Edge: "entry:Ingress", Probes: []probe.Result{
				{Layer: probe.LayerDNS, Target: "*.example.com", Skipped: true, Reason: "wildcard host - test a concrete hostname to check reachability", Command: "curl https://YOUR-SUB.example.com/"},
			}},
			{Resource: ResourceRef{Kind: "Service", Name: "shop"}, Edge: "Ingress->Service", Probes: []probe.Result{
				{Layer: probe.LayerTCP, Target: "shop.internal:80", Skipped: true, Reason: `"shop.internal" resolves to an internal address your machine can't reach`},
			}},
			// Pod dials are localization evidence, never intended routes - their
			// skips must NOT become NotTested rows (a Service:80 and its Pod:8080
			// must never both surface as routes). Their honesty lives on the hop probes.
			{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods", Probes: []probe.Result{
				{Layer: probe.LayerTCP, Skipped: true, Reason: "sampled 2 of 5 ready pods"},
				{Layer: probe.LayerTCP, Target: "10.0.0.5:8080", Skipped: true, Reason: `"shop.internal" resolves to an internal address your machine can't reach`},
			}},
		},
	}
	computeCoverage(tr)
	byReason := map[string]string{}
	for _, s := range tr.NotTested {
		switch {
		case strings.Contains(s.Reason, "wildcard"):
			byReason["wildcard"] = s.ReasonClass
		case strings.Contains(s.Reason, "sampled"):
			byReason["sampled"] = s.ReasonClass
		case strings.Contains(s.Reason, "internal address"):
			byReason["internal"] = s.ReasonClass
		}
	}
	if byReason["wildcard"] != SkipClassCoverage {
		t.Errorf("wildcard skip class = %q, want coverage", byReason["wildcard"])
	}
	if byReason["internal"] != SkipClassVantage {
		t.Errorf("internal-address skip class = %q, want vantage", byReason["internal"])
	}
	if _, podRowLeaked := byReason["sampled"]; podRowLeaked {
		t.Errorf("a Pods-hop skip became a NotTested row - pod dials are localization, not routes")
	}
	for _, s := range tr.NotTested {
		if strings.Contains(s.Route, "10.0.0.5") {
			t.Errorf("a per-Pod target leaked into NotTested: %+v", s)
		}
	}
	// One wildcard coverage gap + one vantage gap; the Pods-hop rows contribute
	// nothing.
	if tr.Coverage == nil || tr.Coverage.Skipped != 2 {
		t.Errorf("coverage.skipped = %v, want 2", tr.Coverage)
	}
}

// CoverageHeadline - the agent/UI primary read. Invariant: indirect never "verified".
func TestCoverageHeadline(t *testing.T) {
	cases := []struct {
		name    string
		tr      *Trace
		want    string
		notWant string
	}{
		{"single verified real", &Trace{Coverage: &Coverage{Tested: 1, Passed: 1}, Routes: []RouteResult{{Outcome: OutcomeVerified, Confidence: ConfidenceReal, Evidence: "HTTP 200"}}}, "Reachable - verified", ""},
		{"single indirect is NOT verified", &Trace{Coverage: &Coverage{Tested: 1, Passed: 1}, Routes: []RouteResult{{Outcome: OutcomeVerified, Confidence: ConfidenceIndirect, Evidence: "HTTP 200"}}}, "API server", "verified"},
		{"single unreachable", &Trace{Coverage: &Coverage{Tested: 1, Failed: 1}, Routes: []RouteResult{{Outcome: OutcomeUnreachable, Confidence: ConfidenceReal, Evidence: "connection refused"}}}, "Unreachable", ""},
		{"multi all pass", &Trace{Coverage: &Coverage{Tested: 3, Passed: 3}, Routes: make([]RouteResult, 3)}, "All 3 routes reachable", ""},
		// The footnote names WHAT is counted - a bare "· 2 not tested" read as a
		// rendering artifact (2 what?).
		{"multi footnote green", &Trace{Coverage: &Coverage{Tested: 3, Passed: 3, Skipped: 2}, Routes: make([]RouteResult, 3)}, "All 3 tested routes reachable · 2 routes not tested", ""},
		{"multi partial", &Trace{Coverage: &Coverage{Tested: 4, Passed: 3, Failed: 1}, Routes: make([]RouteResult, 4)}, "3 of 4 routes reachable · 1 unreachable", ""},
		{"multi none pass", &Trace{Coverage: &Coverage{Tested: 2, Failed: 2}, Routes: make([]RouteResult, 2)}, "None of 2 routes reachable", ""},
		// A multi-route trace where every route ANSWERED with a 5xx: they were
		// reached, so "none reachable" would be dishonest about a live-but-erroring app.
		{"multi all server-error reached", &Trace{Coverage: &Coverage{Tested: 2, Failed: 2}, Routes: []RouteResult{{Outcome: OutcomeServerError}, {Outcome: OutcomeServerError}}}, "2 reached but erroring", "None of 2"},
		{"multi mixed unreachable + erroring", &Trace{Coverage: &Coverage{Tested: 3, Passed: 1, Failed: 2}, Routes: []RouteResult{{Outcome: OutcomeVerified}, {Outcome: OutcomeUnreachable}, {Outcome: OutcomeServerError}}}, "1 unreachable · 1 reached but erroring", ""},
		// One route reachable, the other a deliberate scale-to-0 (benign). The benign
		// route must be surfaced as "scaled to 0", never dropped to leave a dangling
		// trailing separator ("... reachable · ").
		{"multi mixed pass + benign scaled-to-0", &Trace{Coverage: &Coverage{Tested: 2, Passed: 1, Failed: 1}, Routes: []RouteResult{{Outcome: OutcomeVerified}, {Benign: true, Outcome: OutcomeUnreachable}}}, "1 of 2 routes reachable · 1 scaled to 0", ""},
		{"zero tested with skips AFTER probing (all skipped from this vantage)", &Trace{Coverage: &Coverage{Tested: 0, Skipped: 2}, Downstream: []Hop{{Probes: []probe.Result{{Layer: probe.LayerHTTP, Skipped: true}}}}}, "Couldn't actively test any route from here", ""},
		{"zero tested with skips but UN-PROBED (static drawer) reads not-yet-tested, never couldn't-test", &Trace{Coverage: &Coverage{Tested: 0, Skipped: 1}}, "Configuration only - not yet tested", ""},
		{"not probed", &Trace{}, "not yet tested", ""},
	}
	for _, c := range cases {
		got := CoverageHeadline(c.tr)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want substring %q", c.name, got, c.want)
		}
		if c.notWant != "" && strings.Contains(got, c.notWant) {
			t.Errorf("%s: got %q, must NOT contain %q (indirect must never read as verified)", c.name, got, c.notWant)
		}
	}
}

// Test 5 - computeCoverage is ADDITIVE: Verdict + BrokenAt are byte-identical
// after it runs, and a broken hop is named in BrokenRoute.
func TestComputeCoverage_AdditiveNoVerdictChange(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Ingress", Name: "shop"},
		Verdict:  VerdictDegraded,
		BrokenAt: 1,
		Reason:   "1 of 2 routes broken",
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Name: "shop"}, Edge: "entry:Ingress"},
			{Resource: ResourceRef{Kind: "Service", Name: "ghost"}, Edge: "Ingress->Service"},
		},
	}
	wantV, wantB, wantR := tr.Verdict, tr.BrokenAt, tr.Reason
	computeCoverage(tr)
	if tr.Verdict != wantV || tr.BrokenAt != wantB || tr.Reason != wantR {
		t.Errorf("computeCoverage mutated the verdict: %q/%d/%q, want %q/%d/%q",
			tr.Verdict, tr.BrokenAt, tr.Reason, wantV, wantB, wantR)
	}
	if tr.BrokenRoute == nil || tr.BrokenRoute.Name != "ghost" {
		t.Errorf("BrokenRoute = %+v, want the named broken hop (ghost)", tr.BrokenRoute)
	}
}

// B1 - a declared route whose backend is MISSING must appear as a FAILED route
// (counted in Coverage.Failed, honest headline), never vanish into a green-ish summary.
func TestComputeCoverage_MissingBackendIsFailedRoute(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Ingress", Name: "multi"},
		BrokenAt: 2,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Name: "multi"}, Edge: "entry:Ingress",
				Findings: []Finding{{Code: missingRefCodePrefix + "Missing backend Service", Severity: SeverityCritical, Message: "/api references ghost which does not exist"}},
				Config: &HopConfig{Hostnames: []string{"shop.example.com"}, Rules: []RouteRule{
					{Hosts: []string{"shop.example.com"}, Paths: []string{"/web"}, Backends: []BackendRef{{Kind: "Service", Name: "web"}}},
					{Hosts: []string{"shop.example.com"}, Paths: []string{"/api"}, Backends: []BackendRef{{Kind: "Service", Name: "ghost"}}},
				}}},
			{Resource: ResourceRef{Kind: "Service", Name: "web"}, Edge: "Ingress->Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathData, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"}}},
			{Resource: ResourceRef{Kind: "Service", Name: "ghost"}, Edge: "Ingress->Service"}, // missing: no Config, no probes
		},
	}
	computeCoverage(tr)
	var ghost *RouteResult
	for i := range tr.Routes {
		if tr.Routes[i].Target == "ghost" || strings.Contains(tr.Routes[i].Route, "/api") {
			ghost = &tr.Routes[i]
		}
	}
	if ghost == nil {
		t.Fatalf("missing-backend route vanished; routes=%+v", tr.Routes)
	}
	if ghost.Outcome != OutcomeUnreachable {
		t.Errorf("ghost outcome = %q, want unreachable", ghost.Outcome)
	}
	// Counted as Derived, not Failed: nothing dialled this route, so reporting it
	// among the test results would claim a request that was never sent. It must
	// still register as a break - the headline assertion below is what pins that.
	if tr.Coverage == nil || tr.Coverage.Derived < 1 {
		t.Errorf("coverage = %+v, want derived >= 1", tr.Coverage)
	}
	if tr.Coverage.Failed != 0 {
		t.Errorf("coverage = %+v, want a never-dialled route kept out of Failed", tr.Coverage)
	}
	if h := CoverageHeadline(tr); strings.HasPrefix(h, "All ") || !strings.Contains(h, "unreachable") {
		t.Errorf("headline = %q, want honest 'N of M reachable · K unreachable', not green-ish", h)
	}
}

// B4 - a Service subject's NotTested must list ONLY its intended-route (downstream)
// skips; upstream-context skips (the Ingresses pointing AT it) must be excluded.
func TestComputeCoverage_UpstreamSkipsExcludedFromNotTested(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Name: "echo"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "echo"}, Edge: "entry:Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathData, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"}}},
			{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
		},
		Upstreams: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Name: "shop"}, Edge: "Ingress->Service",
				Probes: []probe.Result{{Layer: probe.LayerDNS, Target: "shop.example.com", Skipped: true, Reason: "wildcard host - test a concrete hostname"}}},
		},
	}
	computeCoverage(tr)
	for _, s := range tr.NotTested {
		if strings.Contains(s.Route, "shop.example.com") || strings.Contains(s.Reason, "wildcard") {
			t.Errorf("upstream-context skip leaked into NotTested: %+v", s)
		}
	}
}

// Test 6 - a multiport Service reports EACH port honestly, not a collapsed
// total failure. :80 works, :9090 (nothing listening) is dead → 2 routes, 2/1/1,
// and a "1 of 2 ports reachable" headline rather than "unreachable".
func TestComputeCoverage_MultiportServicePerPort(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "payments"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "payments"}, Edge: "entry:Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}, {Port: 9090}}},
				Probes: []probe.Result{
					{Layer: probe.LayerHTTP, Path: probe.PathData, Port: 80, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"},
					{Layer: probe.LayerTCP, Path: probe.PathData, Port: 9090, OK: false, Tone: probe.ToneUnhealthy, Error: "connection refused"},
				}},
			{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 2 {
		t.Fatalf("want 2 per-port routes, got %d (%+v)", len(tr.Routes), tr.Routes)
	}
	byTarget := map[string]RouteResult{}
	for _, r := range tr.Routes {
		byTarget[r.Target] = r
	}
	if byTarget["payments:80"].Outcome != OutcomeVerified {
		t.Errorf(":80 = %q, want verified", byTarget["payments:80"].Outcome)
	}
	if byTarget["payments:9090"].Outcome != OutcomeUnreachable {
		t.Errorf(":9090 = %q, want unreachable", byTarget["payments:9090"].Outcome)
	}
	if tr.Coverage.Tested != 2 || tr.Coverage.Passed != 1 || tr.Coverage.Failed != 1 {
		t.Errorf("coverage = %+v, want tested 2 passed 1 failed 1", tr.Coverage)
	}
	if hl := CoverageHeadline(tr); hl != "1 of 2 ports reachable · 1 unreachable" {
		t.Errorf("headline = %q, want '1 of 2 ports reachable · 1 unreachable'", hl)
	}
}

// Test 7 - an Ingress route to a specific backend port must NOT read off a
// healthy/dead sibling port. /api → checkout:8080 (ok); checkout also serves
// :9090 (dead) - the route reflects only :8080.
func TestComputeCoverage_IngressRouteScopedToDeclaredPort(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Ingress", Name: "shop"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Name: "shop"}, Edge: "entry:Ingress",
				Config: &HopConfig{Hostnames: []string{"shop.example.com"}, Rules: []RouteRule{
					{Hosts: []string{"shop.example.com"}, Paths: []string{"/api"}, Backends: []BackendRef{{Kind: "Service", Name: "checkout", Port: "8080"}}},
				}}},
			{Resource: ResourceRef{Kind: "Service", Name: "checkout"}, Edge: "Ingress->Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 8080}, {Port: 9090}}},
				Probes: []probe.Result{
					{Layer: probe.LayerHTTP, Path: probe.PathData, Port: 8080, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"},
					{Layer: probe.LayerTCP, Path: probe.PathData, Port: 9090, OK: false, Tone: probe.ToneUnhealthy, Error: "connection refused"},
				}},
			{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 1 {
		t.Fatalf("want 1 route scoped to the declared port, got %d (%+v)", len(tr.Routes), tr.Routes)
	}
	r := tr.Routes[0]
	if r.Outcome != OutcomeVerified || r.Target != "checkout:8080" {
		t.Errorf("route = %s/%s, want verified/checkout:8080 (the :9090 sibling must not leak)", r.Outcome, r.Target)
	}
	if tr.Coverage.Failed != 0 {
		t.Errorf("coverage.failed = %d, want 0 - the dead :9090 is not this route's declared port", tr.Coverage.Failed)
	}
}

// CoverageVerdict reconciles the agent-facing tier with coverage (bug B3):
// broken/degraded/unknown pass through the internal verdict; only a HEALTHY that
// was reached ONLY via the apiserver proxy (indirect) downgrades - indirect is
// never a confident green.
func TestCoverageVerdict_RealVerifiedIsHealthy(t *testing.T) {
	tr := &Trace{Verdict: VerdictHealthy, Coverage: &Coverage{Tested: 1, Passed: 1},
		Routes: []RouteResult{{Outcome: OutcomeVerified, Confidence: ConfidenceReal}}}
	if v := CoverageVerdict(tr); v != VerdictHealthy {
		t.Errorf("real-verified all-pass = %q, want healthy", v)
	}
}

func TestCoverageVerdict_IndirectOnlyIsNotHealthy(t *testing.T) {
	tr := &Trace{Verdict: VerdictHealthy, Coverage: &Coverage{Tested: 1, Passed: 1},
		Routes: []RouteResult{{Outcome: OutcomeVerified, Confidence: ConfidenceIndirect}}}
	if v := CoverageVerdict(tr); v != VerdictUnknown {
		t.Errorf("indirect-only all-pass = %q, want unknown (must NOT read confident healthy - B3/#1a)", v)
	}
}

func TestCoverageVerdict_PartialAndNonePassThrough(t *testing.T) {
	deg := &Trace{Verdict: VerdictDegraded, Coverage: &Coverage{Tested: 2, Passed: 1, Failed: 1},
		Routes: []RouteResult{{Outcome: OutcomeVerified, Confidence: ConfidenceReal}, {Outcome: OutcomeUnreachable}}}
	if v := CoverageVerdict(deg); v != VerdictDegraded {
		t.Errorf("partial = %q, want degraded (pass-through)", v)
	}
	brk := &Trace{Verdict: VerdictBroken, Coverage: &Coverage{Tested: 1, Failed: 1},
		Routes: []RouteResult{{Outcome: OutcomeUnreachable}}}
	if v := CoverageVerdict(brk); v != VerdictBroken {
		t.Errorf("none-reachable = %q, want broken (pass-through)", v)
	}
}

func TestCoverageVerdict_SpecialShapeUnknownPreserved(t *testing.T) {
	tr := &Trace{Verdict: VerdictUnknown, Reason: "Service has no selector"}
	if v := CoverageVerdict(tr); v != VerdictUnknown {
		t.Errorf("special-shape unknown = %q, want unknown preserved", v)
	}
}

// TestSkipClassOf_PrefersStructuredField pins spine-a: the coverage class comes
// from the structured SkipClass a probe stamped, not from re-matching the reason
// text, so rewording a skip message cannot silently misclassify a stamped skip.
// Unstamped skips still fall back to the reason-text classifier.
func TestSkipClassOf_PrefersStructuredField(t *testing.T) {
	// The reason text says "sampled ..." (which classifySkip reads as benign), but the
	// stamped class is vantage, so the structured field must win.
	stamped := probe.Result{Skipped: true, SkipClass: SkipClassVantage, Reason: "sampled 1 of 3 ready pods"}
	if got := skipClassOf(stamped); got != SkipClassVantage {
		t.Errorf("skipClassOf(stamped) = %q, want %q (structured field must win over reason text)", got, SkipClassVantage)
	}
	// No stamp: fall back to classifying the reason text.
	unstamped := probe.Result{Skipped: true, Reason: "sampled 2 of 5 ready pods"}
	if got := skipClassOf(unstamped); got != SkipClassBenign {
		t.Errorf("skipClassOf(unstamped) = %q, want %q (reason-text fallback)", got, SkipClassBenign)
	}
}

// TestWorstOutcome_FailedLayerNamesTheBrokenLayer pins the per-layer model: a
// degraded TLS probe (cert failure) reports failedLayer "tls"; a degraded HTTP
// probe (a 502/504) reports "upstream" - HTTP was reached (a response came back),
// so it is never an HTTP failure; a transport failure reports its own layer; and a
// reachable outcome carries no failed layer.
func TestWorstOutcome_FailedLayerNamesTheBrokenLayer(t *testing.T) {
	tlsCert := probe.Result{Layer: probe.LayerTLS, OK: true, Tone: probe.ToneDegraded, Detail: "certificate expired"}
	if o, _, l := worstOutcome([]probe.Result{tlsCert}); o != OutcomeServerError || l != "tls" {
		t.Errorf("TLS cert failure = (%q, layer %q), want (server-error, tls)", o, l)
	}
	http502 := probe.Result{Layer: probe.LayerHTTP, OK: true, Tone: probe.ToneDegraded, Detail: "HTTP 502 · the front door couldn't reach the backend"}
	if o, _, l := worstOutcome([]probe.Result{http502}); o != OutcomeServerError || l != "upstream" {
		t.Errorf("HTTP 502 = (%q, layer %q), want (server-error, upstream) - HTTP reached, upstream failed", o, l)
	}
	tcpFail := probe.Result{Layer: probe.LayerTCP, OK: false, Detail: "connection refused"}
	if o, _, l := worstOutcome([]probe.Result{tcpFail}); o != OutcomeUnreachable || l != "tcp" {
		t.Errorf("TCP failure = (%q, layer %q), want (unreachable, tcp)", o, l)
	}
	ok := probe.Result{Layer: probe.LayerHTTP, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"}
	if o, _, l := worstOutcome([]probe.Result{ok}); o != OutcomeVerified || l != "" {
		t.Errorf("HTTP 200 = (%q, layer %q), want (verified, empty)", o, l)
	}
}

// worstOutcome truth table + permutation invariance: precedence is decided over
// the WHOLE probe set (any transport failure → unreachable; else any degraded →
// server-error; else verified/reached/not-tested), and the same set shuffled
// yields the identical (outcome, evidence, failedLayer) triple - slice order must
// never pick the deciding probe.
func TestWorstOutcome_PrecedencePermutationInvariant(t *testing.T) {
	tcpFail := probe.Result{Layer: probe.LayerTCP, Target: "a:80", OK: false, Error: "connection refused"}
	httpFail := probe.Result{Layer: probe.LayerHTTP, Target: "http://a/", OK: false, Error: "no response"}
	http502 := probe.Result{Layer: probe.LayerHTTP, Target: "http://a/", OK: true, Tone: probe.ToneDegraded, Detail: "HTTP 502"}
	tlsCert := probe.Result{Layer: probe.LayerTLS, Target: "a:443", OK: true, Tone: probe.ToneDegraded, Detail: "certificate expired"}
	httpOK := probe.Result{Layer: probe.LayerHTTP, Target: "http://a/", OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"}
	http404 := probe.Result{Layer: probe.LayerHTTP, Target: "http://a/", OK: true, Tone: probe.ToneReached, Detail: "HTTP 404"}
	tcpOK := probe.Result{Layer: probe.LayerTCP, Target: "a:80", OK: true, Tone: probe.ToneHealthy, Detail: "tcp ok"}
	dnsOK := probe.Result{Layer: probe.LayerDNS, Target: "a", OK: true, Tone: probe.ToneHealthy, Detail: "resolved"}

	cases := []struct {
		name        string
		probes      []probe.Result
		outcome     string
		evidence    string
		failedLayer string
	}{
		// Precedence, not first-hit: a real transport failure ALWAYS wins over a
		// degraded HTTP answer, regardless of probe order.
		{"transport failure beats degraded", []probe.Result{http502, tcpFail}, OutcomeUnreachable, "connection refused", "tcp"},
		{"transport failure beats verified", []probe.Result{httpOK, tcpFail}, OutcomeUnreachable, "connection refused", "tcp"},
		{"multiple failures: earliest layer is the root break", []probe.Result{httpFail, tcpFail}, OutcomeUnreachable, "connection refused", "tcp"},
		{"degraded beats verified", []probe.Result{httpOK, http502}, OutcomeServerError, "HTTP 502", "upstream"},
		{"tls degraded names tls", []probe.Result{tcpOK, tlsCert}, OutcomeServerError, "certificate expired", "tls"},
		{"verified with transport context", []probe.Result{dnsOK, tcpOK, httpOK}, OutcomeVerified, "HTTP 200", ""},
		{"reached: 404 answered", []probe.Result{tcpOK, http404}, OutcomeReached, "HTTP 404", ""},
		{"reached: transport only", []probe.Result{dnsOK, tcpOK}, OutcomeReached, "tcp ok", ""},
		{"dns only is not reachability", []probe.Result{dnsOK}, OutcomeNotTested, "resolved", ""},
	}
	var permute func(ps []probe.Result, k int, visit func([]probe.Result))
	permute = func(ps []probe.Result, k int, visit func([]probe.Result)) {
		if k == len(ps) {
			visit(ps)
			return
		}
		for i := k; i < len(ps); i++ {
			ps[k], ps[i] = ps[i], ps[k]
			permute(ps, k+1, visit)
			ps[k], ps[i] = ps[i], ps[k]
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := append([]probe.Result{}, c.probes...)
			permute(ps, 0, func(p []probe.Result) {
				o, e, l := worstOutcome(append([]probe.Result{}, p...))
				if o != c.outcome || e != c.evidence || l != c.failedLayer {
					t.Errorf("order %v: got (%q, %q, %q), want (%q, %q, %q)",
						p, o, e, l, c.outcome, c.evidence, c.failedLayer)
				}
			})
		})
	}
}

// Route identity is per host+path rule, never joined: two rules (/web, /admin)
// sharing one backend emit TWO RouteResults, each carrying its OWN rule's
// in-cluster request - so each has its own fold key and testing /web can never
// vouch for /admin.
func TestBuildRoutes_PerRuleIdentityNotJoined(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "shop"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "shop"}, Edge: "entry:Ingress",
				Config: &HopConfig{Hostnames: []string{"shop.example.com"}, Rules: []RouteRule{
					{Hosts: []string{"shop.example.com"}, Paths: []string{"/web"}, Backends: []BackendRef{{Kind: "Service", Name: "web"}}},
					{Hosts: []string{"shop.example.com"}, Paths: []string{"/admin"}, Backends: []BackendRef{{Kind: "Service", Name: "web"}}},
				}}},
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "web"}, Edge: "Ingress->Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathData, Port: 80, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"}}},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods"},
		},
	}
	routes, _ := buildRoutes(tr)
	if len(routes) != 2 {
		t.Fatalf("want 2 per-rule routes, got %d: %+v", len(routes), routes)
	}
	byRoute := map[string]RouteResult{}
	for _, r := range routes {
		byRoute[r.Route] = r
	}
	web, okW := byRoute["/web"]
	admin, okA := byRoute["/admin"]
	if !okW || !okA {
		t.Fatalf("want routes /web and /admin (never a joined \"/web, /admin\"), got %+v", routes)
	}
	if web.Target != "web:80" || admin.Target != "web:80" {
		t.Errorf("both rules share the backend target web:80, got %q / %q", web.Target, admin.Target)
	}
	if web.InClusterRequest == nil || web.InClusterRequest.Path != "/web" {
		t.Errorf("/web request = %+v, want its own path /web", web.InClusterRequest)
	}
	if admin.InClusterRequest == nil || admin.InClusterRequest.Path != "/admin" {
		t.Errorf("/admin request = %+v, want its own path /admin (never /web's)", admin.InClusterRequest)
	}
	k1 := InClusterResultKey(web.Route, web.Target, web.TargetNamespace)
	k2 := InClusterResultKey(admin.Route, admin.Target, admin.TargetNamespace)
	if k1 == k2 {
		t.Errorf("fold keys collide (%q) - a result for one rule would vouch for its sibling", k1)
	}
}

func TestCoverageVerdict_ZeroTestedIsNotHealthy(t *testing.T) {
	// Healthy internal verdict but nothing actually tested (all skipped) - the
	// "couldn't test any route" headline must not sit beside a confident healthy.
	tr := &Trace{Verdict: VerdictHealthy, Coverage: &Coverage{Tested: 0, Skipped: 2}}
	if v := CoverageVerdict(tr); v != VerdictUnknown {
		t.Errorf("zero-tested = %q, want unknown", v)
	}
}

// TestCoverageVerdict_UntestedRoutesTruthTable pins S2: a healthy internal verdict
// may only ship as healthy when every non-benign intended route was actually
// tested. A trace with a real pass but leftover non-benign coverage gaps
// (budget-exhausted / vantage-skipped / couldn't-test) over-claims as healthy - the
// honest verdict is unknown. By-design benign skips lose no coverage
// (recountCoverage drops them from Coverage.Skipped), so a fully-tested-except-benign
// trace stays healthy.
func TestCoverageVerdict_UntestedRoutesTruthTable(t *testing.T) {
	realPass := func(n int) []RouteResult {
		rs := make([]RouteResult, n)
		for i := range rs {
			rs[i] = RouteResult{Outcome: OutcomeVerified, Confidence: ConfidenceReal}
		}
		return rs
	}
	tests := []struct {
		name string
		tr   *Trace
		want string
	}{
		{
			name: "all tested + pass -> healthy",
			tr:   &Trace{Verdict: VerdictHealthy, Coverage: &Coverage{Tested: 5, Passed: 5}, Routes: realPass(5)},
			want: VerdictHealthy,
		},
		{
			name: "some pass + non-benign untested -> unknown (real coverage gap)",
			tr:   &Trace{Verdict: VerdictHealthy, Coverage: &Coverage{Tested: 5, Passed: 5, Skipped: 5}, Routes: realPass(5)},
			want: VerdictUnknown,
		},
		{
			name: "all pass + only benign skips (already excluded from Skipped) -> healthy",
			tr:   &Trace{Verdict: VerdictHealthy, Coverage: &Coverage{Tested: 5, Passed: 5, Skipped: 0}, Routes: realPass(5)},
			want: VerdictHealthy,
		},
		{
			name: "zero tested -> unknown",
			tr:   &Trace{Verdict: VerdictHealthy, Coverage: &Coverage{Tested: 0, Skipped: 5}},
			want: VerdictUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := CoverageVerdict(tt.tr)
			if v != tt.want {
				t.Errorf("CoverageVerdict = %q, want %q", v, tt.want)
			}
			// Idempotent: re-running on the stored verdict yields the same answer.
			tt.tr.Verdict = v
			if v2 := CoverageVerdict(tt.tr); v2 != tt.want {
				t.Errorf("CoverageVerdict (rerun) = %q, want %q (must be idempotent)", v2, tt.want)
			}
		})
	}
}

func TestSingleRouteHeadline_IndirectFailureIsNotReached(t *testing.T) {
	// An UNREACHABLE route observed via the apiserver proxy must NOT read
	// "Reached via API server" - that contradicts the failure.
	h := singleRouteHeadline(RouteResult{Outcome: OutcomeUnreachable, Confidence: ConfidenceIndirect, Evidence: "Connection refused"}, 0, "routes")
	if strings.Contains(h, "Reached") {
		t.Errorf("indirect unreachable headline = %q, must not say 'Reached'", h)
	}
	if !strings.Contains(h, "Unreachable") {
		t.Errorf("indirect unreachable headline = %q, want 'Unreachable'", h)
	}
}

// An intentionally scaled-to-0 Service is unreachable by DESIGN (deliberate
// dormancy) - the route is flagged benign, the verdict softens broken→degraded,
// and the headline reads "scaled to 0", not a red "Unreachable".
func TestComputeCoverage_ScaledToZeroIsBenign(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Name: "scaledzero"},
		Verdict:  VerdictBroken, // the probe found no endpoints
		BrokenAt: 0,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "scaledzero"}, Edge: "entry:Service",
				Config:   &HopConfig{Ports: []PortMap{{Port: 80}}},
				Findings: []Finding{{Code: k8s.ScaledToZeroFingerprint, Severity: SeverityWarning, Message: "Backing workload scaled to 0"}},
				Probes:   []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathAPIServer, OK: false, Tone: probe.ToneUnhealthy, Detail: "No ready backend endpoints"}}},
			{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 1 || !tr.Routes[0].Benign {
		t.Fatalf("route should be benign, got %+v", tr.Routes)
	}
	if tr.Routes[0].Outcome != OutcomeUnreachable {
		t.Errorf("outcome must stay factually unreachable, got %q", tr.Routes[0].Outcome)
	}
	if v := CoverageVerdict(tr); v != VerdictDegraded {
		t.Errorf("benign scale-0 verdict = %q, want degraded (not broken/red)", v)
	}
	if !strings.Contains(tr.Headline, "scaled to 0") {
		t.Errorf("headline = %q, want it to mention 'scaled to 0'", tr.Headline)
	}
}

func TestComputeCoverage_ScaledToZeroNonHTTPCandidateIsBenign(t *testing.T) {
	skip := probe.SkippedCmd(
		probe.LayerHTTP,
		"port 6379",
		probe.VantageLocal,
		"Port named \"redis\" looks non-HTTP. Run Radar from in-cluster to verify TCP reachability.",
		"kubectl port-forward svc/sleeper 6379:6379",
	)
	skip.Port = 6379
	skip.SkipClass = SkipClassVantage
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "prod", Name: "sleeper"},
		Verdict: VerdictBroken,
		Downstream: []Hop{{
			Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "sleeper"},
			Config:   &HopConfig{Ports: []PortMap{{Port: 6379, Name: "redis", Protocol: "TCP"}}},
			Findings: []Finding{{
				Code: k8s.ScaledToZeroFingerprint, Severity: SeverityWarning,
				Message: "Backing workload scaled to 0",
			}},
			Probes: []probe.Result{skip},
		}},
	}

	computeCoverage(tr)

	if len(tr.Routes) != 1 {
		t.Fatalf("Routes = %+v, want one dormant Service-port route", tr.Routes)
	}
	route := tr.Routes[0]
	if route.Outcome != OutcomeUnreachable || !route.Benign {
		t.Fatalf("route = %+v, want benign unreachable scale-to-zero framing", route)
	}
	if tr.Coverage == nil || tr.Coverage.Failed != 1 || tr.Coverage.Skipped != 0 {
		t.Fatalf("Coverage = %+v, want failed=1 skipped=0 for intentional dormancy", tr.Coverage)
	}
	// The raw skip row is absorbed into the route (they are the same gap - two
	// rows rendered one dormant port as two scenarios); the route itself now
	// carries the dormancy story, and it must not recommend a probe that
	// cannot work.
	if len(tr.NotTested) != 0 {
		t.Fatalf("NotTested = %+v, want the port skip absorbed into the benign route", tr.NotTested)
	}
	if !strings.Contains(route.Evidence, "no running backends") {
		t.Fatalf("route evidence = %q, want dormant-context explanation", route.Evidence)
	}
	if route.InClusterRequest != nil {
		t.Fatalf("InClusterRequest = %+v, dormant Service must not recommend a probe that cannot work", route.InClusterRequest)
	}
	if !strings.Contains(tr.Headline, "scaled to 0") {
		t.Fatalf("Headline = %q, want intentional scale-to-zero framing", tr.Headline)
	}
}

func TestMarkBenignServiceSkips_LeavesOtherPortGap(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Name: "mixed"},
		Routes: []RouteResult{
			{Target: "mixed:6379", Outcome: OutcomeUnreachable, Benign: true},
			{Target: "mixed:8080", Outcome: OutcomeNotTested},
		},
		NotTested: []RouteSkip{
			{Route: "port 6379", Reason: "run Radar in-cluster", ReasonClass: SkipClassVantage, Command: "kubectl port-forward svc/mixed 6379:6379"},
			{Route: "port 8080", Reason: "budget exhausted", ReasonClass: SkipClassCoverage, Command: "curl localhost:8080"},
		},
	}

	markBenignServiceSkips(tr)

	if tr.NotTested[0].ReasonClass != SkipClassBenign {
		t.Errorf("dormant port class = %q, want benign", tr.NotTested[0].ReasonClass)
	}
	if tr.NotTested[1].ReasonClass != SkipClassCoverage {
		t.Errorf("unrelated port class = %q, want coverage preserved", tr.NotTested[1].ReasonClass)
	}
	if tr.NotTested[1].Reason != "budget exhausted" || tr.NotTested[1].Command != "curl localhost:8080" {
		t.Errorf("unrelated port was rewritten: %+v", tr.NotTested[1])
	}
}

// A Service at replicas>0 with 0 ready (crashloop) is a REAL break - no scale-0
// finding → not benign, verdict stays broken/red.
func TestComputeCoverage_CrashloopStaysRed(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Name: "crash"},
		Verdict:  VerdictBroken,
		BrokenAt: 0,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "crash"}, Edge: "entry:Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathAPIServer, OK: false, Tone: probe.ToneUnhealthy, Detail: "No ready backend endpoints"}}},
			{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 1 || tr.Routes[0].Benign {
		t.Fatalf("crashloop route must NOT be benign, got %+v", tr.Routes)
	}
	if v := CoverageVerdict(tr); v != VerdictBroken {
		t.Errorf("crashloop verdict = %q, want broken (stays red)", v)
	}
}

// ── Diagnosis: the hoisted cause/culprit/next-action (PROMOTED, never synthesized) ──

// A crashloop pod is the culprit: the Diagnosis must name the actual Pod, carry
// the honest prose Summary, and the logs --previous command - but it must NOT
// emit a structured cause code (the pod-state code would mislabel the cause).
func TestDiagnosis_CrashloopProseNotCoded(t *testing.T) {
	pod := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "app-xyz"}
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "crash"},
		Verdict:  VerdictBroken,
		BrokenAt: 1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "crash"}, Edge: "entry:Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathAPIServer, OK: false, Tone: probe.ToneUnhealthy, Detail: "No ready backend endpoints"}}},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods",
				Findings: []Finding{{
					Code: "problem:CrashLoopBackOff", Severity: SeverityCritical,
					Message:  "CrashLoopBackOff - back-off restarting failed container",
					Cause:    "Container 'app' keeps crashing (exit code 1)",
					Action:   "Inspect the previous container's logs for the panic",
					Command:  "kubectl logs app-xyz -n prod --previous",
					Resource: &pod,
				}}},
		},
	}
	computeCoverage(tr)
	d := tr.Diagnosis
	if d == nil {
		t.Fatal("Diagnosis must be set for a crashloop break")
	}
	if d.CauseCode != "" {
		t.Errorf("Cause = %q, want EMPTY - a pod-state code must not be promoted as a structured cause", d.CauseCode)
	}
	if d.Summary != "Container 'app' keeps crashing (exit code 1)" {
		t.Errorf("Summary = %q, want the finding's honest Cause prose", d.Summary)
	}
	if d.CulpritResource == nil || d.CulpritResource.Kind != "Pod" || d.CulpritResource.Name != "app-xyz" {
		t.Errorf("CulpritResource = %+v, want the actual Pod app-xyz (not the coarse Service/Pods)", d.CulpritResource)
	}
	if !strings.Contains(d.Command, "logs") || !strings.Contains(d.Command, "--previous") {
		t.Errorf("Command = %q, want the pod-targeted logs --previous reproducer", d.Command)
	}
	if d.NextAction != "Inspect the previous container's logs for the panic" {
		t.Errorf("NextAction = %q, want the finding's Action", d.NextAction)
	}
}

// A missing backend IS safe to code: missing-ref is a structural fingerprint.
// The culprit is the named broken route (the backend that doesn't exist).
func TestDiagnosis_MissingBackendCoded(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Ingress", Name: "multi"},
		BrokenAt: 2,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Name: "multi"}, Edge: "entry:Ingress",
				Findings: []Finding{{Code: missingRefCodePrefix + "Missing backend Service", Severity: SeverityCritical, Message: "/api references ghost which does not exist"}},
				Config: &HopConfig{Hostnames: []string{"shop.example.com"}, Rules: []RouteRule{
					{Hosts: []string{"shop.example.com"}, Paths: []string{"/web"}, Backends: []BackendRef{{Kind: "Service", Name: "web"}}},
					{Hosts: []string{"shop.example.com"}, Paths: []string{"/api"}, Backends: []BackendRef{{Kind: "Service", Name: "ghost"}}},
				}}},
			{Resource: ResourceRef{Kind: "Service", Name: "web"}, Edge: "Ingress->Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathData, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"}}},
			{Resource: ResourceRef{Kind: "Service", Name: "ghost"}, Edge: "Ingress->Service"},
		},
	}
	computeCoverage(tr)
	d := tr.Diagnosis
	if d == nil {
		t.Fatal("Diagnosis must be set for a missing backend")
	}
	if !strings.HasPrefix(d.CauseCode, missingRefCodePrefix) {
		t.Errorf("Cause = %q, want the missing-ref structural code", d.CauseCode)
	}
	if d.CulpritResource == nil || d.CulpritResource.Name != "ghost" {
		t.Errorf("CulpritResource = %+v, want the named broken route 'ghost'", d.CulpritResource)
	}
	if !strings.Contains(d.Summary, "ghost") {
		t.Errorf("Summary = %q, want it to name the missing backend", d.Summary)
	}
}

// A targetPort mismatch is structural (svc:*) → safe to code. Its Action is the
// next step.
func TestDiagnosis_TargetportCoded(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "mismatch"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "mismatch"}, Edge: "entry:Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Findings: []Finding{{
					Code: "svc:targetport-no-listener", Severity: SeverityWarning,
					Message: "Service targetPort :9999 matches no port the ready pods declare",
					Cause:   "Service targetPort likely wrong",
					Action:  "Confirm the Service targetPort matches the port the container listens on",
					Command: "kubectl get svc mismatch -n prod -o yaml",
				}},
				Probes: []probe.Result{{Layer: probe.LayerTCP, Path: probe.PathData, Port: 80, OK: true, Tone: probe.ToneHealthy, Detail: "tcp ok"}}},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)
	d := tr.Diagnosis
	if d == nil {
		t.Fatal("Diagnosis must be set for a targetPort mismatch")
	}
	if d.CauseCode != "svc:targetport-no-listener" {
		t.Errorf("Cause = %q, want svc:targetport-no-listener", d.CauseCode)
	}
	if d.NextAction != "Confirm the Service targetPort matches the port the container listens on" {
		t.Errorf("NextAction = %q, want the finding's Action", d.NextAction)
	}
}

// Reachable only via the apiserver proxy → the Diagnosis points at the
// in-cluster runner, and the verdict stays unknown (never over-claims healthy).
func TestDiagnosis_IndirectReachReRunInCluster(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Name: "apionly"},
		Verdict:  VerdictHealthy, // the apiserver probe escalates to healthy; CoverageVerdict downgrades indirect→unknown
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "apionly"}, Edge: "entry:Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathAPIServer, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200 (via apiserver proxy)"}}},
			{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)
	d := tr.Diagnosis
	if d == nil {
		t.Fatal("Diagnosis must describe the indirect-only state")
	}
	if !strings.Contains(d.Summary, "API server") {
		t.Errorf("Summary = %q, want it to name the management-API (indirect) path", d.Summary)
	}
	if !strings.Contains(d.NextAction, "in-cluster") {
		t.Errorf("NextAction = %q, want the in-cluster re-run hint", d.NextAction)
	}
	if v := CoverageVerdict(tr); v != VerdictUnknown {
		t.Errorf("verdict = %q, want unknown (indirect must never read healthy)", v)
	}
}

// HONESTY INVARIANT: every promoted field traces to a real finding - the
// Summary is byte-equal to the finding's own Cause/Message, never invented.
func TestDiagnosis_SummaryIsPromotedNeverSynthesized(t *testing.T) {
	const cause = "Container 'app' keeps crashing (exit code 1)"
	pod := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "app-xyz"}
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "crash"},
		BrokenAt: 1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "crash"}, Edge: "entry:Service", Config: &HopConfig{Ports: []PortMap{{Port: 80}}}},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods",
				Findings: []Finding{{Code: "problem:CrashLoopBackOff", Severity: SeverityCritical, Message: "m", Cause: cause, Resource: &pod}}},
		},
	}
	computeCoverage(tr)
	if tr.Diagnosis == nil || tr.Diagnosis.Summary != cause {
		t.Errorf("Summary = %+v, want it byte-equal to the finding's Cause (promoted, not synthesized)", tr.Diagnosis)
	}
}

// Nothing to diagnose when every route verified over real traffic.
func TestDiagnosis_AllVerifiedRealIsNil(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Name: "api"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "api"}, Edge: "entry:Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathData, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"}}},
			{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)
	if tr.Diagnosis != nil {
		t.Errorf("Diagnosis = %+v, want nil - a fully-verified real path has nothing to diagnose", tr.Diagnosis)
	}
}

// Benign intentional scale-to-0 is not a problem to diagnose.
func TestDiagnosis_BenignScaleZeroIsNil(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Name: "scaledzero"},
		Verdict:  VerdictBroken,
		BrokenAt: 0,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "scaledzero"}, Edge: "entry:Service",
				Config:   &HopConfig{Ports: []PortMap{{Port: 80}}},
				Findings: []Finding{{Code: k8s.ScaledToZeroFingerprint, Severity: SeverityWarning, Message: "Backing workload scaled to 0"}},
				Probes:   []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathAPIServer, OK: false, Tone: probe.ToneUnhealthy, Detail: "No ready backend endpoints"}}},
			{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)
	if tr.Diagnosis != nil {
		t.Errorf("Diagnosis = %+v, want nil - benign scale-to-0 reads via its route, not a diagnosis", tr.Diagnosis)
	}
}

func TestDedupeFacts_CollapsesExactDuplicates(t *testing.T) {
	in := []ProbeFact{
		{Layer: "http", Path: "apiserver", Target: "svc:80", OK: true, Tone: "healthy", Detail: "200"},
		{Layer: "http", Path: "apiserver", Target: "svc:80", OK: true, Tone: "healthy", Detail: "200"},
		{Layer: "tcp", Path: "data", Target: "pod-x:80", OK: true, Tone: "healthy", Detail: "ok"},
	}
	out := dedupeFacts(in)
	if len(out) != 2 {
		t.Errorf("dedupeFacts kept %d, want 2 (exact dup collapsed, distinct pod fact kept)", len(out))
	}
}

// The live crashloop shape: BrokenAt=0 is the Service's "no ready endpoints"
// SYMPTOM (critical, no Resource); the real root cause is the crashloop Pod
// finding on the deeper Pods hop. The Diagnosis must name the POD (with
// logs --previous), not the Service symptom.
func TestDiagnosis_PrefersPodRootCauseOverServiceSymptom(t *testing.T) {
	pod := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "crash-xyz"}
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "crash"},
		Verdict:  VerdictBroken,
		BrokenAt: 0,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "crash"}, Edge: "entry:Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Findings: []Finding{{Code: "svc:no-ready-endpoints", Severity: SeverityCritical, Message: "0/1 selected pods ready",
					Command: "kubectl describe service crash -n prod"}}},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods",
				Findings: []Finding{{Code: "problem:CrashLoopBackOff", Severity: SeverityCritical,
					Message: "back-off restarting failed container", Cause: "Container 'app' keeps crashing (exit code 1)",
					Command: "kubectl logs crash-xyz -n prod --previous", Resource: &pod}}},
		},
	}
	computeCoverage(tr)
	d := tr.Diagnosis
	if d == nil {
		t.Fatal("Diagnosis must be set")
	}
	if d.CulpritResource == nil || d.CulpritResource.Kind != "Pod" || d.CulpritResource.Name != "crash-xyz" {
		t.Errorf("CulpritResource = %+v, want the crashloop Pod (root cause), not the Service symptom", d.CulpritResource)
	}
	if !strings.Contains(d.Command, "--previous") {
		t.Errorf("Command = %q, want the pod logs --previous reproducer", d.Command)
	}
	if d.CauseCode != "" {
		t.Errorf("Cause = %q, want empty (pod-state code not promoted)", d.CauseCode)
	}
}

// TestApplyInClusterResults_UpgradesIndirectToReal: an in-cluster probe (the real
// dataplane) folded into a route the apiserver proxy could only reach INDIRECTLY
// upgrades it to confidence:real and re-derives the counts/headline.
func TestApplyInClusterResults_UpgradesIndirectToReal(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "prod", Name: "api"},
		Routes: []RouteResult{{
			Route: "api", Target: "api:80", Outcome: OutcomeReached, Confidence: ConfidenceIndirect,
			Evidence: "HTTP 404 · reached via proxy", InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Path: "/"},
		}},
		Coverage: &Coverage{Tested: 1, Passed: 1},
	}
	results := map[string][]probe.Result{
		InClusterResultKey("api", "api:80", ""): {{Layer: probe.LayerHTTP, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"}},
	}
	ApplyInClusterResults(tr, results)

	r := tr.Routes[0]
	if r.Confidence != ConfidenceReal {
		t.Errorf("confidence = %q, want real (the in-cluster data path IS real traffic)", r.Confidence)
	}
	if r.Outcome != OutcomeVerified {
		t.Errorf("outcome = %q, want verified (HTTP 200 from inside)", r.Outcome)
	}
	if r.InClusterRequest == nil {
		t.Error("the route's InClusterRequest guess must be preserved through the fold")
	}
	if !anyRealPass(tr.Routes) {
		t.Error("a real-traffic pass should now register (the honesty upgrade the proxy couldn't make)")
	}
}

// TestApplyInClusterResults_LeavesBenignUntouched: a deliberately scaled-to-0
// route is dormant by design, not a path to confirm - the fold must skip it.
func TestApplyInClusterResults_LeavesBenignUntouched(t *testing.T) {
	tr := &Trace{
		Routes: []RouteResult{{
			Route: "api", Target: "api:80", Outcome: OutcomeUnreachable, Benign: true,
			InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Path: "/"},
		}},
		Coverage: &Coverage{Tested: 1, Failed: 1},
	}
	results := map[string][]probe.Result{
		"api:80": {{Layer: probe.LayerHTTP, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"}},
	}
	ApplyInClusterResults(tr, results)
	if tr.Routes[0].Outcome != OutcomeUnreachable || !tr.Routes[0].Benign {
		t.Errorf("benign scale-to-0 route must be left untouched, got %+v", tr.Routes[0])
	}
}

// RouteBackendDrained marks an explicit weight-0 backend with a live
// sibling as drained; a traffic-carrying or weight-omitted backend is not.
func TestRouteBackendDrained(t *testing.T) {
	route := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata":   map[string]any{"name": "r", "namespace": "prod"},
		"spec": map[string]any{
			"rules": []any{map[string]any{"backendRefs": []any{
				map[string]any{"name": "stable", "weight": int64(100)},
				map[string]any{"name": "canary", "weight": int64(0)},
			}}},
		},
	}}
	if !routeBackendDrained(route, "prod", "canary") {
		t.Error("canary (weight 0, live sibling) must be drained")
	}
	if routeBackendDrained(route, "prod", "stable") {
		t.Error("stable (weight 100) must NOT be drained")
	}

	// Weight omitted on the zero-candidate → defaults to traffic-carrying.
	noWeight := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "r", "namespace": "prod"},
		"spec": map[string]any{"rules": []any{map[string]any{"backendRefs": []any{
			map[string]any{"name": "stable", "weight": int64(100)},
			map[string]any{"name": "canary"},
		}}}},
	}}
	if routeBackendDrained(noWeight, "prod", "canary") {
		t.Error("weight-omitted backend must NOT be drained")
	}

	// All-zero (no live sibling) → not drained (no traffic anywhere, not a cutover).
	allZero := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "r", "namespace": "prod"},
		"spec": map[string]any{"rules": []any{map[string]any{"backendRefs": []any{
			map[string]any{"name": "a", "weight": int64(0)},
			map[string]any{"name": "b", "weight": int64(0)},
		}}}},
	}}
	if routeBackendDrained(allZero, "prod", "a") {
		t.Error("all-zero weights (no live sibling) must NOT be drained")
	}
}

// A drained backend hop folds into coverage as a benign skip (no
// coverage lost), never a failed route or a coverage gap.
func TestBuildRoutes_DrainedBackendBenignSkip(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute", Namespace: "prod", Name: "r"},
		Downstream: []Hop{
			{Resource: ResourceRef{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute", Namespace: "prod", Name: "r"}, Edge: "entry:HTTPRoute",
				Config: &HopConfig{Rules: []RouteRule{{Backends: []BackendRef{{Name: "stable"}}}, {Backends: []BackendRef{{Name: "canary"}}}}}},
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "stable"}, Edge: "HTTPRoute->Service", Config: &HopConfig{Ports: []PortMap{{Port: 80}}}},
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "canary"}, Edge: "HTTPRoute->Service",
				Config:   &HopConfig{Ports: []PortMap{{Port: 80}}},
				Meta:     map[string]any{"drained": true},
				Findings: []Finding{{Code: "route:drained-weight-zero", Severity: SeverityInfo}}},
		},
	}
	_, skips := buildRoutes(tr)
	var benignDrained bool
	for _, s := range skips {
		if s.ReasonClass == SkipClassBenign && strings.Contains(s.Reason, "drained") {
			benignDrained = true
		}
	}
	if !benignDrained {
		t.Errorf("drained backend must yield a benign skip, got skips=%+v", skips)
	}
}

// A multi-route headline whose every passing route was reached ONLY via
// the apiserver proxy must say proxy-only - not a bare "All N routes reachable"
// that contradicts CoverageVerdict (which returns unknown on !anyRealPass).
func TestCoverageHeadline_MultiRouteProxyOnly(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "shop"},
		Verdict: VerdictHealthy, // internal verdict; CoverageVerdict corrects it to unknown
		Routes: []RouteResult{
			{Route: "a.example.com/", Target: "a:80", Outcome: OutcomeReached, Confidence: ConfidenceIndirect},
			{Route: "b.example.com/", Target: "b:80", Outcome: OutcomeVerified, Confidence: ConfidenceIndirect},
		},
		Coverage: &Coverage{Tested: 2, Passed: 2},
	}
	h := CoverageHeadline(tr)
	if strings.Contains(h, "routes reachable") && !strings.Contains(h, "API server") {
		t.Errorf("headline = %q, want a proxy-only qualifier, not a bare 'reachable'", h)
	}
	if !strings.Contains(h, "API server") {
		t.Errorf("headline = %q, want it to name the API server (real path not confirmed)", h)
	}
	if CoverageVerdict(tr) != VerdictUnknown {
		t.Errorf("CoverageVerdict = %q, want unknown for proxy-only - headline must agree", CoverageVerdict(tr))
	}
}

// A multi-route headline with at least one REAL pass keeps the confident wording.
func TestCoverageHeadline_MultiRouteRealStaysReachable(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "shop"},
		Routes: []RouteResult{
			{Route: "a/", Target: "a:80", Outcome: OutcomeVerified, Confidence: ConfidenceReal},
			{Route: "b/", Target: "b:80", Outcome: OutcomeVerified, Confidence: ConfidenceReal},
		},
		Coverage: &Coverage{Tested: 2, Passed: 2},
	}
	if h := CoverageHeadline(tr); !strings.Contains(h, "All 2 routes reachable") {
		t.Errorf("headline = %q, want the confident 'All 2 routes reachable'", h)
	}
}

// A shared (Port==0) front-door HTTP 2xx must NOT, on its own, VERIFY a
// backend port route whose own probes didn't independently succeed - it caps at
// "reached", never a false green "verified · real".
func TestRoutesByPort_SharedFrontDoorDoesNotVerifyPort(t *testing.T) {
	shared := []probe.Result{
		{Layer: probe.LayerHTTP, Target: "http://shop/", Port: 0, OK: true, Tone: probe.ToneHealthy, Vantage: probe.VantageLocal},
	}
	// The backend's own :9090 probe skipped (non-HTTP from a laptop) - only the
	// shared front-door / 2xx is live for this port.
	skipped := probe.Skipped(probe.LayerHTTP, "port 9090", probe.VantageLocal, "non-HTTP port - can't verify from here")
	skipped.Port = 9090
	probes := append(append([]probe.Result{}, shared...), skipped)
	routes := routesByPort("api/", "api", "api:9090", probes, []int32{9090}, nil, nil, false, false)
	if len(routes) != 1 {
		t.Fatalf("want 1 route, got %d", len(routes))
	}
	if routes[0].Outcome == OutcomeVerified {
		t.Errorf("outcome = verified - a port-agnostic front-door 2xx must not verify a backend port route")
	}
	if routes[0].Outcome != OutcomeReached {
		t.Errorf("outcome = %q, want reached (front door reached, this port not independently verified)", routes[0].Outcome)
	}
}

// A port's OWN healthy HTTP probe still wins to verified even alongside the shared
// front-door context.
func TestRoutesByPort_OwnHealthyStillVerifies(t *testing.T) {
	shared := []probe.Result{
		{Layer: probe.LayerHTTP, Target: "http://shop/", Port: 0, OK: true, Tone: probe.ToneHealthy, Vantage: probe.VantageLocal},
	}
	own := probe.Result{Layer: probe.LayerHTTP, Target: "port 80", Port: 80, OK: true, Tone: probe.ToneHealthy, Vantage: probe.VantageInCluster}
	probes := append(append([]probe.Result{}, shared...), own)
	routes := routesByPort("api/", "api", "api:80", probes, []int32{80}, nil, nil, false, false)
	if len(routes) != 1 || routes[0].Outcome != OutcomeVerified {
		t.Fatalf("want a verified route from the port's own healthy probe, got %+v", routes)
	}
}

func TestRoutesByPort_VantageSkipsMaterializeOnlyForServiceSubjects(t *testing.T) {
	skip := probe.Skipped(
		probe.LayerHTTP,
		"port 6379",
		probe.VantageLocal,
		"non-HTTP Service port can only be tested from inside the cluster",
	)
	skip.Port = 6379
	skip.SkipClass = SkipClassVantage

	if got := routesByPort("entry/", "database", "database:6379", []probe.Result{skip}, []int32{6379}, nil, nil, false, false); len(got) != 0 {
		t.Fatalf("backend-only vantage skip became an intended route: %+v", got)
	}
	got := routesByPort("database", "database", "database:6379", []probe.Result{skip}, nil, nil, nil, false, true)
	if len(got) != 1 || got[0].Outcome != OutcomeNotTested {
		t.Fatalf("Service-subject vantage skip = %+v, want one not-tested route", got)
	}
}

func TestComputeCoverage_NonHTTPServiceBuildsInClusterCandidate(t *testing.T) {
	serviceSkip := probe.SkippedCmd(
		probe.LayerHTTP,
		"port 6379",
		probe.VantageLocal,
		"Port 6379 is a well-known non-HTTP port. Run Radar from in-cluster to verify TCP reachability.",
		"kubectl port-forward svc/valkey 6379:6379",
	)
	serviceSkip.Port = 6379
	serviceSkip.SkipClass = SkipClassVantage
	podSkip := serviceSkip
	podSkip.Target = "valkey-abc port 6379"

	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "default", Name: "valkey"},
		Downstream: []Hop{
			{
				Resource: ResourceRef{Kind: "Service", Namespace: "default", Name: "valkey"},
				Config:   &HopConfig{Ports: []PortMap{{Port: 6379, Protocol: "TCP"}}},
				Probes:   []probe.Result{serviceSkip},
			},
			{
				Resource: ResourceRef{Kind: "Pods", Namespace: "default"},
				Probes:   []probe.Result{podSkip},
			},
		},
	}

	computeCoverage(tr)

	if len(tr.Routes) != 1 {
		t.Fatalf("Routes = %+v, want one not-tested Service route", tr.Routes)
	}
	route := tr.Routes[0]
	if route.Outcome != OutcomeNotTested || route.Target != "valkey:6379" {
		t.Fatalf("route = %+v, want valkey:6379 not-tested", route)
	}
	if route.InClusterRequest == nil || route.InClusterRequest.Protocol != "tcp" {
		t.Fatalf("in-cluster request = %+v, want TCP candidate", route.InClusterRequest)
	}
	if tr.Coverage == nil || tr.Coverage.Skipped != 1 {
		t.Fatalf("Coverage = %+v, want one intended Service-port gap", tr.Coverage)
	}

	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey(route.Route, route.Target, route.TargetNamespace): {{
			Layer: probe.LayerTCP, Target: "10.96.0.10:6379", Port: 6379,
			Path: probe.PathData, Vantage: probe.VantageInCluster,
			OK: true, Tone: probe.ToneHealthy,
		}},
	})

	if tr.Routes[0].Outcome != OutcomeReached || tr.Routes[0].Confidence != ConfidenceReal {
		t.Fatalf("folded route = %+v, want reached with real confidence", tr.Routes[0])
	}
	if tr.Coverage == nil || tr.Coverage.Passed != 1 || tr.Coverage.Skipped != 0 {
		t.Fatalf("folded Coverage = %+v, want one pass and no stale skips", tr.Coverage)
	}
	if len(tr.NotTested) != 0 {
		t.Fatalf("NotTested = %+v, want resolved port skips removed", tr.NotTested)
	}
}

func TestComputeCoverage_SameNumberMultiProtocolServiceGetsTCPCandidate(t *testing.T) {
	tcpSkip := probe.SkippedCmd(
		probe.LayerHTTP,
		"port 53",
		probe.VantageLocal,
		"Port 53 is not HTTP. Run Radar from in-cluster to verify TCP reachability.",
		"",
	)
	tcpSkip.Port = 53
	tcpSkip.SkipClass = SkipClassVantage
	udpSkip := probe.SkippedCmd(
		probe.LayerTCP,
		"port 53",
		probe.VantageLocal,
		"port 53 is UDP - a TCP dial can't test it",
		"",
	)
	udpSkip.Port = 53
	udpSkip.SkipClass = SkipClassCoverage

	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "kube-system", Name: "kube-dns"},
		Downstream: []Hop{{
			Resource: ResourceRef{Kind: "Service", Namespace: "kube-system", Name: "kube-dns"},
			Config: &HopConfig{Ports: []PortMap{
				{Name: "dns-udp", Port: 53, Protocol: "UDP"},
				{Name: "dns-tcp", Port: 53, Protocol: "TCP"},
			}},
			Probes: []probe.Result{udpSkip, tcpSkip},
		}},
	}

	computeCoverage(tr)

	if len(tr.Routes) != 1 {
		t.Fatalf("Routes = %+v, want one numeric route", tr.Routes)
	}
	// TCP and UDP share :53, but the candidate is not ambiguous: requests only
	// travel TCP, so the TCP-declared sibling owns it - a TCP dial against the
	// TCP :53, never an HTTP guess and never a UDP claim.
	if tr.Routes[0].InClusterRequest == nil || tr.Routes[0].InClusterRequest.Protocol != "tcp" {
		t.Fatalf("InClusterRequest = %+v, want a TCP candidate for the TCP-declared sibling", tr.Routes[0].InClusterRequest)
	}
}

// A single-host Ingress route's label is path-only ("/api"), so
// routeHostKey returns "" and a NotTested route both counts its own skipped
// transport probe (under the host key) AND itself - inflating Coverage.Skipped.
// recountCoverage must recover the host from a host-bearing field (the declared
// host on InClusterRequest) so the dedup fires.
func TestRecountCoverage_SingleHostNotTestedNoDoubleCount(t *testing.T) {
	tr := &Trace{
		Routes: []RouteResult{{
			Route:            "/api", // path-only label (single-host Ingress)
			Target:           "shop:80",
			Outcome:          OutcomeNotTested,
			InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Host: "shop.example.com", Path: "/api"},
		}},
		// The route's own skipped transport probe, keyed by host in NotTested.
		NotTested: []RouteSkip{{
			Route:       "shop.example.com:443",
			Reason:      "the entry path couldn't be reached from where Radar ran",
			ReasonClass: SkipClassVantage,
		}},
	}
	recountCoverage(tr)
	if tr.Coverage == nil {
		t.Fatal("Coverage nil")
	}
	if tr.Coverage.Skipped != 1 {
		t.Errorf("Coverage.Skipped = %d, want 1 - the not-tested route and its skip row are the SAME gap, not two", tr.Coverage.Skipped)
	}
}

// A skip row that SURVIVES beside a Service route is a distinct gap by
// construction - buildNotTested absorbs every same-gap TCP row structurally -
// so recountCoverage must count both. A per-port deduction here re-erased
// exactly the deliberately retained UDP sibling of a TCP candidate.
func TestRecountCoverage_ServiceRetainedRowIsADistinctGap(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "kube-system", Name: "kube-dns"},
		Routes: []RouteResult{{
			Route: "kube-dns:53", Target: "kube-dns:53", Outcome: OutcomeNotTested,
			InClusterRequest: &ProbeRequest{Protocol: "tcp"},
		}},
		NotTested: []RouteSkip{{
			Route: "port 53/UDP (dns)", Reason: "port 53 is UDP - a TCP dial can't test it", ReasonClass: SkipClassCoverage,
		}},
	}

	recountCoverage(tr)

	if tr.Coverage == nil || tr.Coverage.Skipped != 2 {
		t.Fatalf("Coverage = %+v, want 2 distinct gaps (untested TCP route + untestable UDP sibling)", tr.Coverage)
	}
}

// Regression: the multi-host case (route label carries the host) still dedups.
func TestRecountCoverage_MultiHostNotTestedDedup(t *testing.T) {
	tr := &Trace{
		Routes: []RouteResult{{
			Route:   "shop.example.com/api", // host-qualified (multi-host Ingress)
			Target:  "shop:80",
			Outcome: OutcomeNotTested,
		}},
		NotTested: []RouteSkip{{
			Route:       "shop.example.com:443",
			Reason:      "the entry path couldn't be reached from where Radar ran",
			ReasonClass: SkipClassVantage,
		}},
	}
	recountCoverage(tr)
	if tr.Coverage.Skipped != 1 {
		t.Errorf("Coverage.Skipped = %d, want 1 (host-qualified label dedups)", tr.Coverage.Skipped)
	}
}

// Per-rule siblings: ONE host-level skip row absorbs ONE not-tested route, not
// every sibling on that host. Two untested rules (/web, /admin) with a single
// host vantage-skip row are TWO coverage gaps - host-wide dedup must not hide
// the second rule's gap.
func TestRecountCoverage_HostSkipDoesNotSwallowSiblingRoutes(t *testing.T) {
	tr := &Trace{
		Routes: []RouteResult{
			{
				Route:            "/web",
				Target:           "shop:80",
				Outcome:          OutcomeNotTested,
				InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Host: "shop.example.com", Path: "/web"},
			},
			{
				Route:            "/admin",
				Target:           "shop:80",
				Outcome:          OutcomeNotTested,
				InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Host: "shop.example.com", Path: "/admin"},
			},
		},
		NotTested: []RouteSkip{{
			Route:       "shop.example.com:443",
			Reason:      "the entry path couldn't be reached from where Radar ran",
			ReasonClass: SkipClassVantage,
		}},
	}
	recountCoverage(tr)
	if tr.Coverage.Skipped != 2 {
		t.Errorf("Coverage.Skipped = %d, want 2 - the skip row absorbs one route; the sibling is its own gap", tr.Coverage.Skipped)
	}
}

// A host-less not-tested route (subject/port identity, no front-door host) with no
// matching skip row is still its own genuine gap - counted once.
func TestRecountCoverage_HostlessNotTestedCountsOnce(t *testing.T) {
	tr := &Trace{
		Routes: []RouteResult{{
			Route:   ":8080",
			Target:  "svc:8080",
			Outcome: OutcomeNotTested,
		}},
	}
	recountCoverage(tr)
	if tr.Coverage.Skipped != 1 {
		t.Errorf("Coverage.Skipped = %d, want 1 (genuine gap, no dedup partner)", tr.Coverage.Skipped)
	}
}

// A cross-namespace-redacted aggregate backend hop (anonymous ResourceRef -
// Kind Service, no name) must never open a route branch. A branch over it
// manufactures a blank RouteSkip in the static case, and with front-door
// probes present the nameless fallback rule folds EVERY entry probe into a
// blank RouteResult that skips demoteSharedFrontDoor - a blank false-verified
// row; N redacted backends also collapse into 1 row. The aggregate hop's
// rbac:cross-namespace-redacted finding (which carries the count) is the
// honest disclosure - routes and coverage must not manufacture rows for it.
func TestComputeCoverage_RedactedAggregateBackendNoRouteRows(t *testing.T) {
	base := func() *Trace {
		return &Trace{
			Subject:  ResourceRef{Kind: "HTTPRoute", Namespace: "prod", Name: "web"},
			BrokenAt: -1,
			Downstream: []Hop{
				{Resource: ResourceRef{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute", Namespace: "prod", Name: "web"},
					Edge:   "entry:HTTPRoute",
					Config: &HopConfig{Hostnames: []string{"shop.example.com"}}},
				redactedAggregateHop(ResourceRef{Kind: "Service"}, "HTTPRoute->Service",
					"This route references 2 backend Services in namespaces outside your access. Identities, config, and probe results are not shown."),
			},
		}
	}

	t.Run("static", func(t *testing.T) {
		tr := base()
		if got := len(downstreamBranches(tr.Downstream)); got != 0 {
			t.Fatalf("downstreamBranches = %d spans, want 0 - the nameless aggregate must not open a branch", got)
		}
		computeCoverage(tr)
		if len(tr.Routes) != 0 {
			t.Errorf("static redacted-only trace manufactured route rows: %+v", tr.Routes)
		}
		for _, s := range tr.NotTested {
			if s.Route == "" && s.Reason == "route not actively tested" {
				t.Errorf("blank coverage-gap skip manufactured for the redacted aggregate: %+v", s)
			}
		}
	})

	t.Run("probed", func(t *testing.T) {
		tr := base()
		// The realistic probe shape for an HTTPRoute entry: its own hop carries
		// the no-own-address skip; the healthy front-door dial stands in for any
		// port-agnostic entry probe that must not leak into a nameless rule.
		tr.Downstream[0].Probes = []probe.Result{
			{Layer: probe.LayerTCP, Skipped: true, Reason: "route has no own address; reachability lives on parent Gateway and backend Service"},
			{Layer: probe.LayerHTTP, Path: probe.PathData, Target: "http://shop.example.com/", OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"},
		}
		computeCoverage(tr)
		for _, r := range tr.Routes {
			if r.Route == "" {
				t.Errorf("blank route row manufactured for the redacted aggregate: %+v", r)
			}
			if r.Target == "" && r.Outcome == OutcomeVerified {
				t.Errorf("false-verified target-less row for the redacted aggregate: %+v", r)
			}
		}
	})
}

// A redacted aggregate alongside an IN-SCOPE backend: the named backend keeps
// its per-rule routes; the aggregate contributes none.
func TestBuildRoutes_RedactedAggregateAlongsideInScopeBackend(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "HTTPRoute", Namespace: "prod", Name: "web"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute", Namespace: "prod", Name: "web"},
				Edge: "entry:HTTPRoute",
				Config: &HopConfig{Hostnames: []string{"shop.example.com"}, Rules: []RouteRule{
					{Paths: []string{"/web"}, Backends: []BackendRef{{Kind: "Service", Name: "web", Port: "80"}}},
				}}},
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "web"}, Edge: "HTTPRoute->Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathData, Port: 80, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"}}},
			redactedAggregateHop(ResourceRef{Kind: "Service"}, "HTTPRoute->Service",
				"This route references 1 backend Service in namespaces outside your access. Identities, config, and probe results are not shown."),
		},
	}
	if got := len(downstreamBranches(tr.Downstream)); got != 1 {
		t.Fatalf("downstreamBranches = %d spans, want 1 (the in-scope backend only)", got)
	}
	routes, unprobed := buildRoutes(tr)
	if len(routes) != 1 || routes[0].Route != "/web" {
		t.Fatalf("want exactly the in-scope /web route, got %+v", routes)
	}
	for _, s := range unprobed {
		if s.Route == "" {
			t.Errorf("blank skip manufactured for the redacted aggregate: %+v", s)
		}
	}
}

// Multi-host entry with a default-backend rule: only the rule's OWN host's
// front-door dials fold into its outcome. The label "host (default backend)"
// carries no '/', so deriving the host by re-parsing the label reads empty and
// folds EVERY host's dials into the rule - a sibling host's failure would
// condemn (or its success vouch for) a rule it never served.
func TestBuildRoutes_MultiHostDefaultBackendScopedToOwnHost(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "shop"},
		BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "shop"}, Edge: "entry:Ingress",
				Config: &HopConfig{Hostnames: []string{"a.example.com", "b.example.com"}, Rules: []RouteRule{
					{Hosts: []string{"a.example.com"}, Paths: []string{"(default backend)"}, Backends: []BackendRef{{Kind: "Service", Name: "web", Port: "80"}}},
				}},
				Probes: []probe.Result{
					{Layer: probe.LayerHTTP, Path: probe.PathData, Target: "http://a.example.com/", OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"},
					{Layer: probe.LayerTCP, Path: probe.PathData, Target: "b.example.com:80", OK: false, Error: "connect: connection refused"},
				}},
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "web"}, Edge: "Ingress->Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}}},
		},
	}
	routes, _ := buildRoutes(tr)
	if len(routes) != 1 {
		t.Fatalf("want 1 route, got %+v", routes)
	}
	r := routes[0]
	if r.Route != "a.example.com (default backend)" {
		t.Fatalf("route = %q, want the host-qualified default-backend label", r.Route)
	}
	if r.Outcome != OutcomeVerified {
		t.Errorf("outcome = %s (%s), want verified from a.example.com's own healthy dial - b.example.com's failure must not contaminate it", r.Outcome, r.Evidence)
	}
}

// The skip-count unit is coherent per host: a host consumed by >=1 not-tested
// route contributes its ROUTES (each not-tested route counts once) and its raw
// skip rows are absorbed entirely; hosts with no matching route contribute
// their row count.
func TestRecountCoverage_SkipAbsorptionTruthTable(t *testing.T) {
	row := func(target string) RouteSkip {
		return RouteSkip{Route: target, Reason: "couldn't be reached from where Radar ran", ReasonClass: SkipClassVantage}
	}
	route := func(path string) RouteResult {
		return RouteResult{Route: path, Target: "shop:80", Outcome: OutcomeNotTested,
			InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Host: "shop.example.com", Path: path}}
	}
	cases := []struct {
		name   string
		rows   []RouteSkip
		routes []RouteResult
		want   int
	}{
		{"2 rows + 2 sibling routes = 2", []RouteSkip{row("shop.example.com:80"), row("shop.example.com:443")}, []RouteResult{route("/web"), route("/admin")}, 2},
		{"1 row + 2 sibling routes = 2", []RouteSkip{row("shop.example.com:443")}, []RouteResult{route("/web"), route("/admin")}, 2},
		{"2 rows + 0 routes = 2", []RouteSkip{row("shop.example.com:80"), row("shop.example.com:443")}, nil, 2},
		{"2 rows + 1 route = 1", []RouteSkip{row("shop.example.com:80"), row("shop.example.com:443")}, []RouteResult{route("/web")}, 1},
		{"unrelated host's row stays counted", []RouteSkip{row("other.example.com:443")}, []RouteResult{route("/web")}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := &Trace{Routes: c.routes, NotTested: c.rows}
			recountCoverage(tr)
			if tr.Coverage == nil || tr.Coverage.Skipped != c.want {
				t.Errorf("Coverage.Skipped = %+v, want %d", tr.Coverage, c.want)
			}
		})
	}
}

// The per-path skip reasons ARE the answer to "why couldn't you test this".
// They were discarded unless a skip happened to carry a command, leaving a
// generic sentence that restated the headline and told the reader nothing.
func TestZeroTestedDiagnosisCarriesTheActualReason(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "argocd", Name: "argocd-server"},
		BrokenAt: -1,
		Coverage: &Coverage{Tested: 0, Skipped: 1},
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "argocd-server"},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Skipped: true}}},
		},
		NotTested: []RouteSkip{
			{Route: "port 443", Reason: "HTTPS backend - the API-server proxy speaks plain HTTP and can't verify TLS on this port."},
		},
	}
	d := computeDiagnosis(tr)
	if d == nil || !strings.Contains(d.Summary, "HTTPS backend") {
		t.Errorf("summary = %+v, want the real skip reason", d)
	}
}

// Several different reasons cannot be collapsed into one sentence honestly, so
// the resource-level line says so and sends the reader to the per-path tabs.
func TestZeroTestedDiagnosisSaysWhenReasonsDiffer(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "argocd", Name: "argocd-server"},
		BrokenAt: -1,
		Coverage: &Coverage{Tested: 0, Skipped: 2},
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "argocd-server"},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Skipped: true}}},
		},
		NotTested: []RouteSkip{
			{Route: "port 80", Reason: "the backend didn't respond within the probe budget"},
			{Route: "port 443", Reason: "HTTPS backend - the proxy can't verify TLS on this port."},
		},
	}
	d := computeDiagnosis(tr)
	if d == nil || !strings.Contains(d.Summary, "2 different reasons") {
		t.Errorf("summary = %+v, want the count of differing reasons", d)
	}
}

// Identical reasons across paths ARE the resource's answer, so say it once.
func TestZeroTestedDiagnosisCollapsesIdenticalReasons(t *testing.T) {
	same := "the backend didn't respond within the probe budget"
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "argocd", Name: "argocd-server"},
		BrokenAt: -1,
		Coverage: &Coverage{Tested: 0, Skipped: 2},
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "argocd-server"},
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Skipped: true}}},
		},
		NotTested: []RouteSkip{{Route: "port 80", Reason: same}, {Route: "port 8080", Reason: same}},
	}
	d := computeDiagnosis(tr)
	if d == nil || !strings.Contains(d.Summary, same) || strings.Contains(d.Summary, "different reasons") {
		t.Errorf("summary = %+v, want the one shared reason stated once", d)
	}
}

// The declared test candidate must survive a skipped-only first probe: an
// apiserver timeout / budget skip erased the route, and InClusterRequest only
// rides on routes - so the offered in-cluster recovery could not run exactly
// when it was the recovery.
func TestSkippedOnlyHTTPPortKeepsItsDeclaredCandidate(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "prod", Name: "argocd"},
		Downstream: []Hop{
			{
				Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "argocd"},
				Config:   &HopConfig{ClusterIP: "10.0.0.9", Ports: []PortMap{{Port: 80, Name: "http", TargetPort: "8080"}}},
				Probes: []probe.Result{{
					Layer: probe.LayerHTTP, Target: "argocd:80", Port: 80,
					Vantage: probe.VantageLocal, Path: probe.PathAPIServer,
					Skipped: true, Reason: "the API-server proxy timed out",
				}},
			},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)

	if len(tr.Routes) != 1 {
		t.Fatalf("routes = %d, want 1 - the declared HTTP port must survive as a not-tested route: %+v", len(tr.Routes), tr.Routes)
	}
	r := tr.Routes[0]
	if r.Outcome != OutcomeNotTested {
		t.Errorf("Outcome = %q, want not-tested", r.Outcome)
	}
	if r.InClusterRequest == nil {
		t.Fatalf("InClusterRequest = nil - the in-cluster recovery has no target to run")
	}
	// The intended-route count is the invariant (dedup is host-level absorption).
	if tr.Coverage == nil || tr.Coverage.Skipped != 1 {
		t.Errorf("Coverage.Skipped = %+v, want 1 intended-route gap", tr.Coverage)
	}
}

// A deliberately-skipped non-HTTP port must NOT become a guessed HTTP Job -
// the tcp,http-shaped probe would fabricate identity/mTLS evidence about a
// protocol it never spoke.
func TestSkippedOnlyNonHTTPPortStaysRouteless(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "prod", Name: "redis"},
		Downstream: []Hop{
			{
				Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "redis"},
				Config:   &HopConfig{ClusterIP: "10.0.0.10", Ports: []PortMap{{Port: 6379, Name: "redis", TargetPort: "6379"}}},
				Probes: []probe.Result{{
					Layer: probe.LayerTCP, Target: "redis:6379", Port: 6379,
					Vantage: probe.VantageLocal, Path: probe.PathData,
					Skipped: true, Reason: "redis is not an HTTP port",
				}},
			},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)

	for _, r := range tr.Routes {
		if r.InClusterRequest != nil {
			t.Fatalf("a non-HTTP skipped port grew a runnable HTTP request: %+v", r)
		}
	}
}

// A skipped multi-port Service is one gap per DECLARED port - the preserved
// routes and their raw per-port skip rows must never render as separate
// scenarios ("argocd-server:80" AND "port 80" = four tabs for two gaps).
// Identity is structural (backend ns/name + port), never display strings.
func TestSkippedMultiPortServiceIsOneGapPerPort(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "argocd", Name: "argocd-server"},
		Downstream: []Hop{
			{
				Resource: ResourceRef{Kind: "Service", Namespace: "argocd", Name: "argocd-server"},
				Config: &HopConfig{ClusterIP: "10.0.0.9", Ports: []PortMap{
					{Port: 80, Name: "http", TargetPort: "8080"},
					{Port: 443, Name: "https", TargetPort: "8080"},
				}},
				Probes: []probe.Result{
					{Layer: probe.LayerHTTP, Target: "port 80", Port: 80, Vantage: probe.VantageLocal, Path: probe.PathAPIServer, Skipped: true, Reason: "the backend didn't respond within the probe budget"},
					{Layer: probe.LayerHTTP, Target: "port 443", Port: 443, Vantage: probe.VantageLocal, Path: probe.PathAPIServer, Skipped: true, Reason: "HTTPS backend - the API-server proxy speaks plain HTTP"},
				},
			},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "argocd"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)

	if len(tr.Routes) != 2 {
		t.Fatalf("routes = %d, want 2 preserved candidates: %+v", len(tr.Routes), tr.Routes)
	}
	for _, r := range tr.Routes {
		if r.InClusterRequest == nil {
			t.Errorf("route %q has no runnable request", r.Route)
		}
		if r.Evidence == "" {
			t.Errorf("route %q lost its skip reason - the tab can no longer say WHY", r.Route)
		}
	}
	for _, s := range tr.NotTested {
		t.Errorf("raw per-port skip row survived beside its preserved route: %+v", s)
	}
	if tr.Coverage == nil || tr.Coverage.Skipped != 2 {
		t.Errorf("Coverage.Skipped = %+v, want 2 (one gap per declared port)", tr.Coverage)
	}
}

// The protocol boundary fails closed. An unnamed UDP port that happens to sit
// on 80 must never acquire an HTTP Job the prober itself declined to send; a
// route built from a direct TCP reach of a non-HTTP port must not carry an
// HTTP-shaped request either.
func TestProtocolBoundaryFailsClosed(t *testing.T) {
	if httpProbablePortMap(PortMap{Port: 80, Protocol: "UDP"}) {
		t.Error("an unnamed UDP :80 classified as HTTP-probable")
	}
	if httpProbablePortMap(PortMap{Port: 80, Protocol: "SCTP"}) {
		t.Error("an SCTP port classified as HTTP-probable")
	}
	if !httpProbablePortMap(PortMap{Port: 80, Protocol: ""}) {
		t.Error("empty protocol is the Kubernetes TCP default and must stay HTTP-probable")
	}

	// A redis route that exists on real TCP evidence gets a TCP-shaped request
	// - never an HTTP-shaped one against a protocol the prober declined to speak.
	routes := []RouteResult{{Route: "redis:6379", Target: "redis:6379", Outcome: OutcomeReached, Confidence: ConfidenceReal}}
	attachInClusterRequest(routes, "", "", &HopConfig{Ports: []PortMap{{Port: 6379, Name: "redis", TargetPort: "6379"}}})
	if routes[0].InClusterRequest == nil || routes[0].InClusterRequest.Protocol != "tcp" || routes[0].InClusterRequest.Scheme != "" {
		t.Errorf("non-HTTP route request = %+v, want a bare TCP candidate with no HTTP shape", routes[0].InClusterRequest)
	}

	// ...while its HTTP sibling still does.
	routes = []RouteResult{{Route: "web:80", Target: "web:80", Outcome: OutcomeReached, Confidence: ConfidenceReal}}
	attachInClusterRequest(routes, "", "", &HopConfig{Ports: []PortMap{{Port: 80, Name: "http", TargetPort: "8080"}}})
	if routes[0].InClusterRequest == nil {
		t.Error("the HTTP route lost its request")
	}
}

// Kubernetes permits TCP and UDP ServicePorts with the same number (kube-dns:
// UDP :53 beside TCP :53). The number alone is not a route identity.
func TestDuplicatePortNumber_IsNotAnIdentity(t *testing.T) {
	dup := []PortMap{
		{Port: 53, Protocol: "UDP", Name: "dns"},
		{Port: 53, Protocol: "TCP", Name: "dns-tcp"},
	}
	// Fail-closed regardless of declaration ORDER: TCP-first must not let the
	// UDP sibling acquire a guessed HTTP Job.
	tcpFirst := []PortMap{
		{Port: 80, Protocol: "TCP", Name: "http"},
		{Port: 80, Protocol: "UDP", Name: "http-udp"},
	}
	if httpProbablePort(dup, 53) {
		t.Fatal("ambiguous 53 (UDP+TCP) must not be HTTP-probable")
	}
	if httpProbablePort(tcpFirst, 80) {
		t.Fatal("TCP-first declaration must not make the ambiguous 80 HTTP-probable")
	}
	if !httpProbablePort([]PortMap{{Port: 80, Name: "http"}}, 80) {
		t.Fatal("a plain TCP http port must stay HTTP-probable")
	}

	// Scheme hints belong to the TCP entry - requests only travel TCP.
	cfg := &HopConfig{Ports: dup}
	if got, ok := portFromTarget("kube-dns:53", cfg); !ok || got.Name != "dns-tcp" {
		t.Fatalf("portFromTarget must prefer the TCP entry, got %q ok=%v", got.Name, ok)
	}
	// With no TCP interpretation among the matches, the shape fails closed.
	noTCP := &HopConfig{Ports: []PortMap{{Port: 53, Protocol: "UDP"}, {Port: 53, Protocol: "SCTP"}}}
	if _, ok := portFromTarget("kube-dns:53", noTCP); ok {
		t.Fatal("duplicate non-TCP-only matches must fail closed")
	}

	// Labels distinguish the two declared paths and carry the protocol.
	if a, b := declaredPortLabel(dup[0]), declaredPortLabel(dup[1]); a == b {
		t.Fatalf("duplicate-number labels must differ, both %q", a)
	} else {
		if !strings.Contains(a, "UDP") {
			t.Fatalf("UDP label must name the protocol, got %q", a)
		}
		if !strings.Contains(b, "dns-tcp") {
			t.Fatalf("TCP label should carry the declared name, got %q", b)
		}
	}

	// A preserved TCP route must not absorb the UDP sibling's skip row - but it
	// MUST still absorb the TCP side's own skip, or the picker shows a
	// contradictory "not tested" beside a route that already covers the port.
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Name: "kube-dns", Namespace: "kube-system"},
		Routes:  []RouteResult{{Route: "kube-dns:53", Target: "kube-dns:53", Outcome: OutcomeReached}},
		Downstream: []Hop{{
			Resource: ResourceRef{Kind: "Service", Name: "kube-dns", Namespace: "kube-system"},
			Config:   &HopConfig{Ports: dup},
			Probes: []probe.Result{
				{
					Layer: probe.LayerTCP, Target: "port 53/UDP (dns)", Port: 53, Protocol: "UDP",
					Skipped: true, Reason: "port 53 is UDP - a TCP dial can't test it",
				},
				{
					Layer: probe.LayerHTTP, Target: "port 53 (dns-tcp)", Port: 53,
					Skipped: true, Reason: "Port 53 (\"dns-tcp\") is a well-known non-HTTP port.",
				},
			},
		}},
	}
	skips := buildNotTested(tr)
	udpKept, tcpKept := false, false
	for _, s := range skips {
		if strings.Contains(s.Route, "UDP") {
			udpKept = true
		}
		if strings.Contains(s.Route, "dns-tcp") {
			tcpKept = true
		}
	}
	if !udpKept {
		t.Fatalf("the UDP :53 row must survive beside the TCP route, got %+v", skips)
	}
	if tcpKept {
		t.Fatalf("the TCP side's own skip must be absorbed by the preserved TCP route, got %+v", skips)
	}
}

// The PR contract both reviewers converged on: a transport-only TCP reach on
// an HTTPS port must not swallow the HTTPS application-layer gap. Radar
// in-cluster dials TCP directly and the HTTPS proxy skip must SURVIVE as a
// counted gap - coverage reads "1 got through · 1 couldn't be tried", never a
// clean pass with TLS/HTTP unrun.
func TestComputeCoverage_TransportOnlyReachKeepsHTTPSGap(t *testing.T) {
	httpsSkip := probe.SkippedCmd(probe.LayerHTTP, "port 8443 (https)", probe.VantageInCluster,
		"HTTPS backend - the API-server proxy speaks plain HTTP and can't verify TLS on this port. Test it directly.", "")
	httpsSkip.Path = probe.PathAPIServer
	httpsSkip.Port = 8443
	httpsSkip.SkipClass = SkipClassVantage
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "prod", Name: "secure-api"},
		Downstream: []Hop{{
			Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "secure-api"},
			Config:   &HopConfig{ClusterIP: "10.0.0.9", Ports: []PortMap{{Port: 8443, Name: "https"}}},
			Probes: []probe.Result{
				{Layer: probe.LayerTCP, Target: "10.0.0.9:8443", Port: 8443, Vantage: probe.VantageInCluster, Path: probe.PathData, OK: true, Tone: probe.ToneHealthy},
				httpsSkip,
			},
		}},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 1 || tr.Routes[0].Outcome != OutcomeReached {
		t.Fatalf("Routes = %+v, want one transport-reached route", tr.Routes)
	}
	kept := false
	for _, s := range tr.NotTested {
		if s.ReasonClass != SkipClassBenign && strings.Contains(s.Reason, "HTTPS backend") {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("NotTested = %+v, want the HTTPS app-layer gap kept beside the transport-only reach", tr.NotTested)
	}
	if tr.Coverage == nil || tr.Coverage.Passed != 1 || tr.Coverage.Skipped != 1 {
		t.Fatalf("Coverage = %+v, want passed=1 skipped=1 (transport through, app layer untried)", tr.Coverage)
	}
	// The same skip beside a VERIFIED (real HTTP ran) route absorbs as before.
	tr2 := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "prod", Name: "web"},
		Downstream: []Hop{{
			Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "web"},
			Config:   &HopConfig{ClusterIP: "10.0.0.10", Ports: []PortMap{{Port: 80, Name: "http"}}},
			Probes: []probe.Result{
				{Layer: probe.LayerHTTP, Target: "10.0.0.10:80", Port: 80, Vantage: probe.VantageInCluster, Path: probe.PathData, OK: true, Tone: probe.ToneHealthy},
				func() probe.Result {
					sk := probe.SkippedCmd(probe.LayerHTTP, "port 80 (http)", probe.VantageLocal, "couldn't reach an internal address from your machine", "")
					sk.Port = 80
					return sk
				}(),
			},
		}},
	}
	computeCoverage(tr2)
	for _, s := range tr2.NotTested {
		if strings.Contains(s.Reason, "internal address") {
			t.Fatalf("a live-HTTP-tested port must still absorb its own HTTP skip rows, got %+v", tr2.NotTested)
		}
	}
}

// The layer-aware keep applies to a transport-only REACH, never a transport
// FAILURE - a TCP-failed port's HTTP row is the same broken gap, and keeping
// it counted one broken path as failed AND couldn't-be-tried.
func TestComputeCoverage_TransportFailureAbsorbsAppLayerRow(t *testing.T) {
	httpsSkip := probe.SkippedCmd(probe.LayerHTTP, "port 8443 (https)", probe.VantageInCluster,
		"HTTPS backend - the API-server proxy speaks plain HTTP and can't verify TLS on this port. Test it directly.", "")
	httpsSkip.Path = probe.PathAPIServer
	httpsSkip.Port = 8443
	httpsSkip.SkipClass = SkipClassVantage
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "prod", Name: "secure-api"},
		Downstream: []Hop{{
			Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "secure-api"},
			Config:   &HopConfig{ClusterIP: "10.0.0.9", Ports: []PortMap{{Port: 8443, Name: "https"}}},
			Probes: []probe.Result{
				{Layer: probe.LayerTCP, Target: "10.0.0.9:8443", Port: 8443, Vantage: probe.VantageInCluster, Path: probe.PathData, OK: false, Tone: probe.ToneUnhealthy, Detail: "connection refused"},
				httpsSkip,
			},
		}},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 1 || tr.Routes[0].Outcome != OutcomeUnreachable {
		t.Fatalf("Routes = %+v, want one unreachable route", tr.Routes)
	}
	for _, sk := range tr.NotTested {
		if strings.Contains(sk.Reason, "HTTPS backend") {
			t.Fatalf("a transport FAILURE must absorb the app-layer row (same broken gap), got %+v", tr.NotTested)
		}
	}
	if tr.Coverage == nil || tr.Coverage.Failed != 1 || tr.Coverage.Skipped != 0 {
		t.Fatalf("Coverage = %+v, want failed=1 skipped=0 - one broken path counted once", tr.Coverage)
	}
}

// A dead front door is a real misconfiguration, but upstreams are parallel: it
// must be stated WITHOUT condemning a Service that other entries still serve.
func TestComputeEntryProblems_PromotesUpstreamFaultWithoutTouchingVerdict(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "staging", Name: "shop"},
		Upstreams: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Namespace: "staging", Name: "shop"}},
			{
				Resource: ResourceRef{Kind: "HTTPRoute", Namespace: "staging", Name: "shop"},
				Findings: []Finding{{
					Code: "gwroute:not-accepted", Severity: SeverityWarning,
					Message: "Not attached: no listener matches its hosts",
					Action:  "check the parent Gateway's listener hostnames",
				}},
			},
		},
		Downstream: []Hop{{Resource: ResourceRef{Kind: "Service", Namespace: "staging", Name: "shop"}}},
	}
	got := computeEntryProblems(tr)
	if len(got) != 1 {
		t.Fatalf("EntryProblems = %+v, want the HTTPRoute fault promoted", got)
	}
	if got[0].Resource.Kind != "HTTPRoute" || !strings.Contains(got[0].Summary, "no listener") {
		t.Fatalf("promoted the wrong finding: %+v", got[0])
	}
	// The row is read by a human at a glance: the friendly Message leads, and
	// the raw controller condition is one hover away - the opposite order from
	// Diagnosis, which wants the deeper cause first.
	tr.Upstreams[1].Findings[0].Cause = "Accepted: NoMatchingListenerHostname - there were no hostname intersections between the HTTPRoute and this parent ref's Listener(s)."
	promoted := computeEntryProblems(tr)[0]
	if !strings.Contains(promoted.Summary, "Not attached") {
		t.Errorf("Summary = %q, want the human Message to lead", promoted.Summary)
	}
	if !strings.Contains(promoted.Detail, "NoMatchingListenerHostname") {
		t.Errorf("Detail = %q, want the raw cause available for the hover", promoted.Detail)
	}
	if got[0].Action == "" {
		t.Error("an entry problem should carry its finding's action")
	}
	// Info-level advisories stay where they are.
	tr.Upstreams[1].Findings[0].Severity = SeverityInfo
	if n := len(computeEntryProblems(tr)); n != 0 {
		t.Errorf("info-severity findings must not surface as entry problems, got %d", n)
	}
}

// The two surfaces must never state the same fault twice.
func TestComputeEntryProblems_DedupesAgainstDiagnosis(t *testing.T) {
	same := "Not attached: no listener matches its hosts"
	tr := &Trace{
		Subject:   ResourceRef{Kind: "Service", Namespace: "staging", Name: "shop"},
		Diagnosis: &Diagnosis{Summary: same},
		Upstreams: []Hop{{
			Resource: ResourceRef{Kind: "HTTPRoute", Namespace: "staging", Name: "shop"},
			Findings: []Finding{{Code: "gwroute:not-accepted", Severity: SeverityWarning, Message: same}},
		}},
	}
	if got := computeEntryProblems(tr); len(got) != 0 {
		t.Fatalf("EntryProblems = %+v, want none - the Diagnosis already says it", got)
	}
}

// An entry can serve THIS Service perfectly while a different backendRef of the
// same entry is missing. Promoting that sibling's break as "this entry cannot
// carry traffic" is the misattribution computeVerdict already refuses to make.
func TestComputeEntryProblems_IgnoresSiblingMissingRef(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "store", Name: "shop"},
		Upstreams: []Hop{{
			Resource: ResourceRef{Kind: "Ingress", Namespace: "store", Name: "entry"},
			Findings: []Finding{
				{Code: missingRefCodePrefix + "service", Severity: SeverityCritical, Message: "backend Service checkout does not exist"},
				{Code: "gwroute:not-accepted", Severity: SeverityWarning, Message: "Not attached: no listener matches its hosts"},
			},
		}},
		Downstream: []Hop{{Resource: ResourceRef{Kind: "Service", Namespace: "store", Name: "shop"}}},
	}
	got := computeEntryProblems(tr)
	if len(got) != 1 {
		t.Fatalf("EntryProblems = %+v, want only the entry's OWN fault", got)
	}
	if strings.Contains(got[0].Summary, "checkout") {
		t.Errorf("a sibling backend's missing ref was promoted as this entry's problem: %q", got[0].Summary)
	}
}

// A route Radar dialled and watched fail is the strongest fault it can report.
// The UI renders anything short of critical as a warning, so an unset severity
// on this path would make confirmed unreachability look weaker than a predicted
// one.
func TestComputeDiagnosis_ConfirmedRouteFailureIsCritical(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "store", Name: "shop"},
		Routes: []RouteResult{{
			Route:      "GET /",
			Target:     "shop:80",
			Outcome:    OutcomeUnreachable,
			Confidence: "real",
			Evidence:   "connection refused",
		}},
	}
	d := computeDiagnosis(tr)
	if d == nil {
		t.Fatal("a failed route must produce a diagnosis")
	}
	if d.Severity != SeverityCritical {
		t.Errorf("severity = %q, want %q — a dialled-and-failed route is not a warning", d.Severity, SeverityCritical)
	}
	if d.Class == DiagnosisClassCoverage {
		t.Errorf("class = %q, want a fault — this is a real break, not an untested gap", d.Class)
	}
}
