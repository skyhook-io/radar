package trace

import (
	"reflect"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/probe"
)

// cleanInClusterPass is a live in-cluster HTTP 200 - the unambiguous real-traffic
// evidence the fold upgrades a route with.
func cleanInClusterPass() []probe.Result {
	return []probe.Result{{
		Layer: probe.LayerHTTP, Path: probe.PathData, Vantage: probe.VantageInCluster,
		OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200",
	}}
}

// ApplyInClusterResults re-derives the verdict over the updated
// findings - a stale 'degraded' left after a would-deny downgrade must not sit
// beside a now-healthy projection. The re-derive only happens when the run
// actually folded evidence in.
func TestApplyInClusterResults_RederivesVerdict(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "prod", Name: "api"},
		Verdict: VerdictDegraded, // stale from a static would-deny prediction
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "api"}, Edge: "entry:Service"},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods"},
		},
		Routes:   []RouteResult{{Route: "api", Target: "api:80", Outcome: OutcomeReached, Confidence: ConfidenceIndirect}},
		Coverage: &Coverage{Tested: 1, Passed: 1},
	}
	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey("api", "api:80", ""): cleanInClusterPass(),
	})
	if tr.Verdict != VerdictHealthy {
		t.Errorf("verdict = %q, want healthy - clean findings must re-derive over a stale degraded once live evidence folded", tr.Verdict)
	}
}

// When an in-cluster confirmation flips a broken verdict toward healthy,
// the stale Reason + BrokenRoute must be cleared so they don't contradict the
// now-healthy projection. The break here is a Pods-hop symptom with no probe or
// finding evidence left, so the live pass honestly clears it.
func TestApplyInClusterResults_ClearsStaleReasonAndBrokenRoute(t *testing.T) {
	podRef := ResourceRef{Kind: "Pods", Namespace: "prod"}
	tr := &Trace{
		Subject:     ResourceRef{Kind: "Service", Namespace: "prod", Name: "api"},
		Verdict:     VerdictBroken,
		Reason:      "the entry is unreachable - traffic can't pass through it",
		BrokenAt:    1,
		BrokenRoute: &podRef,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "api"}, Edge: "entry:Service"},
			{Resource: podRef, Edge: "Service->Pods"},
		},
		Routes:   []RouteResult{{Route: "api", Target: "api:80", Outcome: OutcomeUnreachable, Confidence: ConfidenceIndirect}},
		Coverage: &Coverage{Tested: 1, Failed: 1},
	}
	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey("api", "api:80", ""): cleanInClusterPass(),
	})
	if tr.Verdict != VerdictHealthy {
		t.Fatalf("verdict = %q, want healthy", tr.Verdict)
	}
	if tr.Reason != "" {
		t.Errorf("Reason = %q, want cleared - a stale break sentence must not survive a flip to healthy", tr.Reason)
	}
	if tr.BrokenRoute != nil {
		t.Errorf("BrokenRoute = %+v, want nil - no break remains", tr.BrokenRoute)
	}
}

// TestApplyInClusterResults_UpgradesCollapsedUnknown pins the in-cluster path
// against the verdict-collapse regression: an unknown that the coverage collapse
// produced because the laptop trace was apiserver-only (UnknownClass empty) MUST
// re-derive after the in-cluster pass upgrades its routes to real. Only a genuine
// special-shape unknown (UnknownClass set) stays unknown. Without the guard fix,
// the "Test in-cluster" button could never lift a trace off unknown.
func TestApplyInClusterResults_UpgradesCollapsedUnknown(t *testing.T) {
	base := func(unknownClass string) *Trace {
		return &Trace{
			Subject:      ResourceRef{Kind: "Service", Namespace: "prod", Name: "api"},
			Verdict:      VerdictUnknown,
			UnknownClass: unknownClass,
			BrokenAt:     -1,
			Downstream: []Hop{
				{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "api"}, Edge: "entry:Service"},
				{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods"},
			},
			Routes:   []RouteResult{{Route: "api", Target: "api:80", Outcome: OutcomeReached, Confidence: ConfidenceIndirect}},
			Coverage: &Coverage{Tested: 1, Passed: 1},
		}
	}
	results := map[string][]probe.Result{
		InClusterResultKey("api", "api:80", ""): cleanInClusterPass(),
	}
	// Collapse-induced unknown (no UnknownClass): the in-cluster real pass must lift it.
	up := base("")
	ApplyInClusterResults(up, results)
	if up.Verdict != VerdictHealthy {
		t.Errorf("collapse-unknown after in-cluster real pass = %q, want healthy (must re-derive)", up.Verdict)
	}
	// Genuine special-shape unknown (e.g. selectorless): stays unknown.
	special := base(UnknownClassByDesign)
	ApplyInClusterResults(special, results)
	if special.Verdict != VerdictUnknown {
		t.Errorf("special-shape unknown = %q, want unknown preserved", special.Verdict)
	}
}

// entryBrokenTrace is the empirically-reproduced shape: the Ingress front door
// itself refused the connection (entry-hop probe failure), so the whole path is
// broken even though the backend behind it may be fine.
func entryBrokenTrace() *Trace {
	return &Trace{
		Subject:  ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "shop"},
		Verdict:  VerdictBroken,
		Reason:   "the entry is unreachable - traffic can't pass through it",
		BrokenAt: 0,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "shop"}, Edge: "entry:Ingress",
				Config: &HopConfig{Hostnames: []string{"shop.example.com"}, Rules: []RouteRule{
					{Hosts: []string{"shop.example.com"}, Paths: []string{"/"}, Backends: []BackendRef{{Kind: "Service", Name: "web"}}},
				}},
				Probes: []probe.Result{{Layer: probe.LayerTCP, Target: "shop.example.com:80", OK: false, Tone: probe.ToneUnhealthy, Error: "connection refused"}}},
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "web"}, Edge: "Ingress->Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}}},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods"},
		},
		Routes: []RouteResult{{
			Route: "/", Target: "web:80", Outcome: OutcomeUnreachable, Confidence: ConfidenceReal,
			Evidence:         "connection refused",
			InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Host: "shop.example.com", Path: "/"},
		}},
		NotTested: []RouteSkip{},
		Coverage:  &Coverage{Tested: 1, Failed: 1},
	}
}

// Empirical repro 1: a clean BACKEND in-cluster pass must NOT heal a failed FRONT
// DOOR. The entry-hop probe failure is evidence about the entry segment; the
// in-cluster probe dialed the Service directly and says nothing about the Ingress.
// The verdict stays broken (entry unreachable) while the backend route row
// honestly shows the live in-cluster success.
func TestApplyInClusterResults_BackendFoldKeepsEntryProbeFailure(t *testing.T) {
	tr := entryBrokenTrace()
	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey("/", "web:80", ""): cleanInClusterPass(),
	})

	if tr.Verdict != VerdictBroken {
		t.Fatalf("verdict = %q, want broken - a backend-only in-cluster pass must not vouch for the failed entry", tr.Verdict)
	}
	if tr.BrokenAt != 0 {
		t.Errorf("BrokenAt = %d, want 0 (the entry hop still carries the failed probe)", tr.BrokenAt)
	}
	if !strings.Contains(tr.Reason, "entry is unreachable") {
		t.Errorf("Reason = %q, want the entry-unreachable explanation preserved/re-derived", tr.Reason)
	}
	// The backend route row honestly reflects the live in-cluster evidence.
	if r := tr.Routes[0]; r.Outcome != OutcomeVerified || r.Confidence != ConfidenceReal {
		t.Errorf("route = %s/%s, want verified/real - the backend WAS reached in-cluster", r.Outcome, r.Confidence)
	}
}

// A fold that produced NO evidence (nil map, empty map, or a map whose keys
// match no route) must leave the trace completely unchanged - the runner
// returns an empty map in every degraded case (impersonation failure, probe
// cap, unclean throwaway-pod probe, guessed path), and folding nothing must
// never clear Reason, prune vantage skips, or flip broken → healthy.
func TestApplyInClusterResults_NoFoldIsNoOp(t *testing.T) {
	broken := func() *Trace {
		tr := entryBrokenTrace()
		tr.NotTested = []RouteSkip{{
			Route:       "internal.example.com:443",
			Reason:      "couldn't reach an internal address from your machine - run radar in-cluster",
			ReasonClass: SkipClassVantage,
		}}
		return tr
	}
	cases := []struct {
		name string
		in   map[string][]probe.Result
	}{
		{"nil map", nil},
		{"empty map", map[string][]probe.Result{}},
		{"no matching key", map[string][]probe.Result{
			InClusterResultKey("/other", "other:80", ""): cleanInClusterPass(),
		}},
		{"matching key but only skipped results", map[string][]probe.Result{
			InClusterResultKey("/", "web:80", ""): {{Layer: probe.LayerHTTP, Skipped: true, Reason: "probe skipped"}},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := broken()
			ApplyInClusterResults(got, c.in)
			if want := broken(); !reflect.DeepEqual(got, want) {
				t.Errorf("trace mutated with zero folded evidence:\n got: %+v\nwant: %+v", got, want)
			}
		})
	}
}

// Sibling honesty: with per-rule route identity, an in-cluster pass on /web must
// upgrade ONLY /web. The untested /admin sibling (same backend Service) keeps its
// prior indirect outcome - it must never read verified-over-real-traffic off its
// sibling's evidence.
func TestApplyInClusterResults_SiblingRouteNotVouched(t *testing.T) {
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
				Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathAPIServer, Port: 80, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200"}}},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods"},
		},
	}
	computeCoverage(tr)
	if len(tr.Routes) != 2 {
		t.Fatalf("want 2 per-rule routes (/web, /admin), got %d: %+v", len(tr.Routes), tr.Routes)
	}
	byRoute := map[string]*RouteResult{}
	for i := range tr.Routes {
		byRoute[tr.Routes[i].Route] = &tr.Routes[i]
	}
	web, admin := byRoute["/web"], byRoute["/admin"]
	if web == nil || admin == nil {
		t.Fatalf("want routes /web and /admin, got %+v", tr.Routes)
	}
	if admin.InClusterRequest == nil || admin.InClusterRequest.Path != "/admin" {
		t.Fatalf("/admin must carry ITS OWN rule's request path, got %+v", admin.InClusterRequest)
	}
	if web.InClusterRequest == nil || web.InClusterRequest.Path != "/web" {
		t.Fatalf("/web must carry ITS OWN rule's request path, got %+v", web.InClusterRequest)
	}

	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey("/web", web.Target, web.TargetNamespace): cleanInClusterPass(),
	})

	if web.Outcome != OutcomeVerified || web.Confidence != ConfidenceReal {
		t.Errorf("/web = %s/%s, want verified/real (it WAS tested in-cluster)", web.Outcome, web.Confidence)
	}
	if admin.Confidence == ConfidenceReal {
		t.Errorf("/admin = %s/%s - an untested sibling must NOT be upgraded by /web's evidence", admin.Outcome, admin.Confidence)
	}
	if admin.Outcome != OutcomeVerified || admin.Confidence != ConfidenceIndirect {
		t.Errorf("/admin = %s/%s, want its prior indirect outcome preserved", admin.Outcome, admin.Confidence)
	}
}

// ApplyInClusterResults must prune the SkipClassVantage "run radar
// in-cluster" NotTested rows for routes the live in-cluster pass just upgraded to a
// real reach/verify. Otherwise the response reports the route verified while
// advising "run radar in-cluster" for the SAME route, and coverage counts it as
// Passed AND Skipped.
func TestApplyInClusterResults_PrunesResolvedVantageSkip(t *testing.T) {
	tr := &Trace{
		Verdict: VerdictUnknown, // skip verdict re-derive; isolate the coverage/NotTested fold
		Routes: []RouteResult{{
			Route:            "api:80",
			Target:           "api:80",
			TargetNamespace:  "prod",
			Outcome:          OutcomeNotTested,
			InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Host: "api", Path: "/"},
		}},
		NotTested: []RouteSkip{{
			Route:       "api:80",
			Reason:      "couldn't reach an internal address from your machine - run radar in-cluster",
			ReasonClass: SkipClassVantage,
		}},
	}
	key := InClusterResultKey("api:80", "api:80", "prod")
	byTarget := map[string][]probe.Result{
		key: {{
			Layer:   probe.LayerTCP,
			Target:  "10.0.0.1:80",
			Path:    probe.PathData,
			Port:    80,
			OK:      true,
			Vantage: probe.VantageInCluster,
		}},
	}

	ApplyInClusterResults(tr, byTarget)

	if got := tr.Routes[0].Outcome; got != OutcomeReached && got != OutcomeVerified {
		t.Fatalf("route Outcome = %q, want reached/verified after live in-cluster pass", got)
	}
	if tr.Routes[0].Confidence != ConfidenceReal {
		t.Fatalf("route Confidence = %q, want real", tr.Routes[0].Confidence)
	}
	if len(tr.NotTested) != 0 {
		t.Errorf("NotTested = %+v, want empty - the live pass satisfied the run-in-cluster advice", tr.NotTested)
	}
	if tr.Coverage == nil {
		t.Fatal("Coverage nil")
	}
	if tr.Coverage.Skipped != 0 {
		t.Errorf("Coverage.Skipped = %d, want 0 (no stale same-route skip)", tr.Coverage.Skipped)
	}
	if tr.Coverage.Passed != 1 {
		t.Errorf("Coverage.Passed = %d, want 1", tr.Coverage.Passed)
	}
}

func TestApplyInClusterResults_PrunesResolvedServiceProtocolSkip(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "prod", Name: "secure-api"},
		Verdict: VerdictUnknown,
		Routes: []RouteResult{{
			Route: "secure-api", Target: "secure-api:443", TargetNamespace: "prod",
			Outcome: OutcomeReached, Confidence: ConfidenceIndirect,
			InClusterRequest: &ProbeRequest{Protocol: "https", Scheme: "https", Path: "/"},
		}},
		NotTested: []RouteSkip{{
			Route: "port 443", ReasonClass: SkipClassVantage,
			Reason: "the API-server proxy cannot verify an HTTPS backend",
		}},
	}

	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey("secure-api", "secure-api:443", "prod"): cleanInClusterPass(),
	})

	if len(tr.NotTested) != 0 {
		t.Fatalf("NotTested = %+v, want the now-tested HTTPS port gap removed", tr.NotTested)
	}
	if tr.Coverage == nil || tr.Coverage.Passed != 1 || tr.Coverage.Skipped != 0 {
		t.Fatalf("Coverage = %+v, want passed=1 skipped=0", tr.Coverage)
	}
}

func TestApplyInClusterResults_KeepsSameNumberUDPGapAfterTCPPass(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "kube-system", Name: "kube-dns"},
		Verdict: VerdictUnknown,
		Routes: []RouteResult{{
			Route: "kube-dns", Target: "kube-dns:53", TargetNamespace: "kube-system",
			Outcome:          OutcomeNotTested,
			InClusterRequest: &ProbeRequest{Protocol: "tcp"},
		}},
		NotTested: []RouteSkip{
			{
				Route: "port 53", ReasonClass: SkipClassVantage,
				Reason: "run the TCP test from inside the cluster",
			},
			{
				Route: "port 53", ReasonClass: SkipClassCoverage,
				Reason: "port 53 is UDP - test it with a UDP client",
			},
		},
	}

	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey("kube-dns", "kube-dns:53", "kube-system"): cleanInClusterPass(),
	})

	if len(tr.NotTested) != 1 || tr.NotTested[0].ReasonClass != SkipClassCoverage {
		t.Fatalf("NotTested = %+v, want only the unresolved UDP coverage gap", tr.NotTested)
	}
	if tr.Coverage == nil || tr.Coverage.Passed != 1 || tr.Coverage.Skipped != 1 {
		t.Fatalf("Coverage = %+v, want TCP passed=1 and UDP skipped=1", tr.Coverage)
	}
}

// Regression: a vantage skip whose route did NOT get a live in-cluster pass must
// survive - pruning must be scoped to routes the live pass actually resolved,
// even while a DIFFERENT route's result folds in.
func TestApplyInClusterResults_KeepsUnresolvedVantageSkip(t *testing.T) {
	tr := &Trace{
		Verdict: VerdictUnknown,
		Routes: []RouteResult{
			{
				Route:            "api:80",
				Target:           "api:80",
				TargetNamespace:  "prod",
				Outcome:          OutcomeNotTested,
				InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Host: "api", Path: "/"},
			},
			{
				Route:            "other:80",
				Target:           "other:80",
				TargetNamespace:  "prod",
				Outcome:          OutcomeNotTested,
				InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Host: "other", Path: "/"},
			},
		},
		NotTested: []RouteSkip{{
			Route:       "other:80",
			Reason:      "couldn't reach an internal address from your machine - run radar in-cluster",
			ReasonClass: SkipClassVantage,
		}},
	}
	// Only api:80 gets a live pass; other:80 stays untested.
	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey("api:80", "api:80", "prod"): cleanInClusterPass(),
	})

	if len(tr.NotTested) != 1 {
		t.Errorf("NotTested = %+v, want the unresolved vantage skip preserved", tr.NotTested)
	}
}

// The vantage skip rows are HOST-level (one row per probe target), so a partial
// fold on a multi-path host must not prune the shared row: /web passing while
// /admin stays untested leaves the "run radar in-cluster" advice as the truth
// for /admin, and deleting it made the partial run read as complete.
func TestApplyInClusterResults_PartialFoldKeepsSameHostVantageSkip(t *testing.T) {
	tr := &Trace{
		Verdict: VerdictUnknown,
		Routes: []RouteResult{
			{
				Route:            "/web",
				Target:           "api:80",
				TargetNamespace:  "prod",
				Outcome:          OutcomeNotTested,
				InClusterRequest: &ProbeRequest{Host: "shop.example.com", Path: "/web"},
			},
			{
				Route:            "/admin",
				Target:           "api:80",
				TargetNamespace:  "prod",
				Outcome:          OutcomeNotTested,
				InClusterRequest: &ProbeRequest{Host: "shop.example.com", Path: "/admin"},
			},
		},
		NotTested: []RouteSkip{{
			Route:       "shop.example.com:443",
			Reason:      "couldn't reach an internal address from your machine - run radar in-cluster",
			ReasonClass: SkipClassVantage,
		}},
	}
	// Only /web gets a live pass; /admin (same host) stays untested.
	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey("/web", "api:80", "prod"): cleanInClusterPass(),
	})

	if len(tr.NotTested) != 1 {
		t.Errorf("NotTested = %+v, want the same-host vantage skip preserved while /admin is still untested", tr.NotTested)
	}
}

// ApplyInClusterResults reconciles the static would-deny WARNING off
// the live in-cluster pass and must also clear the svc:targetport-no-listener
// WARNING. The static reconcileTargetPortAdvisory reads h.Probes, which
// the in-cluster flow stamps only AFTER the verdict/diagnosis recompute - so a
// route the in-cluster pod just verified over REAL traffic would read
// verified/ConfidenceReal while the hop kept the targetPort warning, leaving a
// stale 'degraded' verdict and a 'Service targetPort likely wrong' diagnosis that
// the live success disproves. reconcileInClusterTargetPort must downgrade it.
func TestApplyInClusterResults_DowngradesTargetPortOnLivePass(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "mismatch"},
		Verdict:  VerdictDegraded, // stale from the static targetPort prediction
		BrokenAt: -1,
		Routes: []RouteResult{{
			Route: "mismatch", Target: "mismatch:80",
			Outcome: OutcomeReached, Confidence: ConfidenceIndirect,
			InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Path: "/"},
		}},
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "mismatch"}, Edge: "entry:Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}}},
				Meta:   map[string]any{"targetPortSuspectPorts": []int32{80}},
				Findings: []Finding{{
					Code: "svc:targetport-no-listener", Severity: SeverityWarning,
					Message: "Service targetPort :9999 matches no port the ready pods declare",
					Cause:   "Service targetPort likely wrong",
					Action:  "Confirm the Service targetPort matches the port the container listens on",
				}}},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods"},
		},
	}

	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey("mismatch", "mismatch:80", ""): cleanInClusterPass(),
	})

	idx := findingIndexByCode(tr.Downstream[0].Findings, "svc:targetport-no-listener")
	if idx < 0 {
		t.Fatal("targetPort finding disappeared; want it downgraded to info, not dropped")
	}
	f := tr.Downstream[0].Findings[idx]
	if f.Severity != SeverityInfo {
		t.Errorf("severity = %q, want info - live in-cluster traffic reached the suspect port", f.Severity)
	}
	if f.Cause != "" {
		t.Errorf("downgraded finding must drop the 'likely wrong' Cause, got %q", f.Cause)
	}
	if tr.Verdict == VerdictDegraded {
		t.Error("verdict must re-derive off 'degraded' once the contradicted targetPort warning is info")
	}
	if tr.Diagnosis != nil && tr.Diagnosis.CauseCode == "svc:targetport-no-listener" {
		t.Errorf("diagnosis must not promote the targetPort guess the live pass disproved, got %+v", tr.Diagnosis)
	}
}

// Port scope: a live in-cluster pass on a DIFFERENT port than the suspect must NOT
// clear the targetPort warning - mirrors the would-deny port-scope guard.
func TestApplyInClusterResults_KeepsTargetPortOnDifferentPortPass(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "mismatch"},
		Verdict:  VerdictDegraded,
		BrokenAt: -1,
		Routes: []RouteResult{{
			Route: "mismatch", Target: "mismatch:443",
			Outcome: OutcomeReached, Confidence: ConfidenceIndirect,
			InClusterRequest: &ProbeRequest{Protocol: "https", Scheme: "https", Path: "/"},
		}},
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "mismatch"}, Edge: "entry:Service",
				Config: &HopConfig{Ports: []PortMap{{Port: 80}, {Port: 443}}},
				Meta:   map[string]any{"targetPortSuspectPorts": []int32{80}},
				Findings: []Finding{{
					Code: "svc:targetport-no-listener", Severity: SeverityWarning,
					Message: "Service targetPort :9999 matches no port the ready pods declare",
					Cause:   "Service targetPort likely wrong",
					Action:  "Confirm the Service targetPort matches the port the container listens on",
				}}},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods"},
		},
	}

	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey("mismatch", "mismatch:443", ""): cleanInClusterPass(),
	})

	f := tr.Downstream[0].Findings[findingIndexByCode(tr.Downstream[0].Findings, "svc:targetport-no-listener")]
	if f.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning kept - the live pass hit :443, not the suspect :80", f.Severity)
	}
}

// A front-doored route verified by the in-cluster fold has proven the BACKEND -
// every dial bypassed the entry. The rows say so structurally and the headline
// must never read as a bare "verified" that paints a dead Ingress green.
func TestApplyInClusterResults_BackendScopeSurvivesTheFold(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "web"},
		Verdict:  VerdictUnknown,
		BrokenAt: -1,
		Upstreams: []Hop{{
			Resource: ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "web"},
		}},
		Routes: []RouteResult{{
			Route: "GET /", Target: "web:80", TargetNamespace: "prod",
			Outcome:          OutcomeNotTested,
			InClusterRequest: &ProbeRequest{Host: "web", Path: "/"},
		}},
	}
	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey("GET /", "web:80", "prod"): cleanInClusterPass(),
	})

	r := tr.Routes[0]
	if r.Confidence != ConfidenceReal {
		t.Fatalf("Confidence = %q, want real", r.Confidence)
	}
	if !RouteBackendScoped(r) {
		t.Fatalf("route not backend-scoped after a fold that bypassed the front door: %+v", r.ByVantage)
	}
	h := CoverageHeadline(tr)
	if !strings.Contains(h, "entry path was not exercised") {
		t.Errorf("headline %q must say the entry path was not exercised", h)
	}
	if strings.HasPrefix(h, "Reachable - verified") {
		t.Errorf("headline %q reads as a bare verified for a backend-only pass", h)
	}
}

// The same trace WITHOUT a front door keeps the plain verified headline - the
// Service IS the whole declared path, and a qualifier about a nonexistent
// entry would be nonsense.
func TestApplyInClusterResults_NoFrontDoorStaysPlainVerified(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "web"},
		Verdict:  VerdictUnknown,
		BrokenAt: -1,
		Routes: []RouteResult{{
			Route: "GET /", Target: "web:80", TargetNamespace: "prod",
			Outcome:          OutcomeNotTested,
			InClusterRequest: &ProbeRequest{Host: "web", Path: "/"},
		}},
	}
	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey("GET /", "web:80", "prod"): cleanInClusterPass(),
	})
	if RouteBackendScoped(tr.Routes[0]) {
		t.Fatalf("no front door - the Service is the whole path; rows must not claim a backend segment")
	}
	if h := CoverageHeadline(tr); strings.Contains(h, "entry path") {
		t.Errorf("headline %q qualifies a nonexistent entry", h)
	}
}

// An entry whose own probes unanimously failed stays broken THROUGH the fold: a
// backend verified in-cluster must never lift a dead front door to healthy.
func TestApplyInClusterResults_DeadEntrySurvivesBackendVerify(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "web"},
		Verdict:  VerdictHealthy,
		BrokenAt: -1,
		Downstream: []Hop{
			{
				Resource: ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "web"},
				Edge:     "entry:Ingress",
				Probes: []probe.Result{
					{Layer: probe.LayerTCP, Target: "34.0.0.1:443", Vantage: probe.VantageLocal, Path: probe.PathData, OK: false, Tone: probe.ToneUnhealthy},
				},
			},
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "web"}, Edge: "Ingress->Service"},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods"},
		},
		Routes: []RouteResult{{
			Route: "GET /", Target: "web:80", TargetNamespace: "prod",
			Outcome:          OutcomeNotTested,
			InClusterRequest: &ProbeRequest{Host: "web", Path: "/"},
		}},
	}
	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey("GET /", "web:80", "prod"): cleanInClusterPass(),
	})
	if tr.Verdict == VerdictHealthy {
		t.Fatalf("verdict = healthy after a backend-only verify while the entry's own probes all failed")
	}
}
