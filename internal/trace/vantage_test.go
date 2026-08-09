package trace

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/probe"
)

func httpProbe(v probe.Vantage, p probe.Path, ok bool, detail string) probe.Result {
	r := probe.Result{Layer: probe.LayerHTTP, Target: "checkout:80", Vantage: v, Path: p, OK: ok, Detail: detail}
	if ok {
		r.Tone = probe.ToneHealthy
	} else {
		r.Tone = probe.ToneUnhealthy
	}
	return r
}

// The case the whole change exists for: a Service that works from inside the
// cluster and fails from a laptop. Both are non-apiserver, so the rollup buckets
// them together and worst-wins collapses to unreachable - with no field left to
// say it DID work in-cluster. That is what made the UI report one vantage's
// result under another's name.
func TestPerVantageKeepsDisagreementTheRollupDestroys(t *testing.T) {
	probes := []probe.Result{
		httpProbe(probe.VantageInCluster, probe.PathData, true, "HTTP 200"),
		httpProbe(probe.VantageLocal, probe.PathData, false, "connection refused"),
	}
	r, ok := routeFromProbes("checkout.example.com/", "checkout:80", probes, false)
	if !ok {
		t.Fatal("expected a route result")
	}
	// The rollup is unchanged - it stays the documented lossy summary.
	if r.Outcome != OutcomeUnreachable {
		t.Errorf("rollup outcome = %q, want the worst-wins %q", r.Outcome, OutcomeUnreachable)
	}
	if len(r.ByVantage) != 2 {
		t.Fatalf("ByVantage = %d entries, want 2: %+v", len(r.ByVantage), r.ByVantage)
	}
	byV := map[string]VantageResult{}
	for _, v := range r.ByVantage {
		byV[v.Vantage] = v
	}
	if got := byV[string(probe.VantageInCluster)].Outcome; got == OutcomeUnreachable {
		t.Errorf("in-cluster outcome = %q; the vantage that WORKED must not inherit the merged failure", got)
	}
	if got := byV[string(probe.VantageLocal)].Outcome; got != OutcomeUnreachable {
		t.Errorf("local outcome = %q, want %q", got, OutcomeUnreachable)
	}
}

// A relayed request bypasses kube-proxy, NetworkPolicy and the mesh, so it can
// never be read as real-traffic proof - per group, exactly as the rollup rules.
func TestPerVantageMarksApiserverIndirect(t *testing.T) {
	probes := []probe.Result{
		httpProbe(probe.VantageLocal, probe.PathAPIServer, true, "HTTP 200"),
		httpProbe(probe.VantageInCluster, probe.PathData, true, "HTTP 200"),
	}
	r, _ := routeFromProbes("r", "checkout:80", probes, false)
	for _, v := range r.ByVantage {
		want := ConfidenceReal
		if v.Path == string(probe.PathAPIServer) {
			want = ConfidenceIndirect
		}
		if v.Confidence != want {
			t.Errorf("%s/%s confidence = %q, want %q", v.Vantage, v.Path, v.Confidence, want)
		}
	}
}

// The same vantage relayed through the API server is a DIFFERENT claim from one
// that used the real network path, so the key is (vantage, path), not vantage.
func TestPerVantageSplitsOneVantageAcrossMechanisms(t *testing.T) {
	probes := []probe.Result{
		httpProbe(probe.VantageLocal, probe.PathData, false, "connection refused"),
		httpProbe(probe.VantageLocal, probe.PathAPIServer, true, "HTTP 200"),
	}
	r, _ := routeFromProbes("r", "checkout:80", probes, false)
	if len(r.ByVantage) != 2 {
		t.Fatalf("want the two mechanisms kept apart, got %+v", r.ByVantage)
	}
}

func TestPerVantageDropsSkippedProbes(t *testing.T) {
	skipped := httpProbe(probe.VantageInCluster, probe.PathData, false, "")
	skipped.Skipped = true
	probes := []probe.Result{httpProbe(probe.VantageLocal, probe.PathData, true, "HTTP 200"), skipped}
	r, _ := routeFromProbes("r", "checkout:80", probes, false)
	if len(r.ByVantage) != 1 || r.ByVantage[0].Vantage != string(probe.VantageLocal) {
		t.Errorf("a skipped probe carries no observation and must not become a vantage row: %+v", r.ByVantage)
	}
}

// An in-cluster run observes ONE vantage. Replacing the list wholesale would
// delete the laptop's and the proxy's results, which are still true - and are
// exactly the disagreement this field exists to keep.
func TestMergeVantagesKeepsVantagesTheRunDidNotObserve(t *testing.T) {
	prior := []VantageResult{
		{Vantage: "local", Path: "data", Outcome: OutcomeUnreachable, Confidence: ConfidenceReal},
		{Vantage: "local", Path: "apiserver", Outcome: OutcomeVerified, Confidence: ConfidenceIndirect},
	}
	fresh := []VantageResult{{Vantage: "in-cluster", Path: "data", Outcome: OutcomeVerified, Confidence: ConfidenceReal}}
	got := mergeVantages(prior, fresh)
	if len(got) != 3 {
		t.Fatalf("want prior 2 + fresh 1, got %+v", got)
	}
	if got[0].Vantage != "local" || got[0].Path != "data" {
		t.Errorf("prior order must be stable so rows don't jump on a re-run: %+v", got)
	}
}

func TestMergeVantagesReplacesTheSameVantage(t *testing.T) {
	prior := []VantageResult{{Vantage: "in-cluster", Path: "data", Outcome: OutcomeUnreachable}}
	fresh := []VantageResult{{Vantage: "in-cluster", Path: "data", Outcome: OutcomeVerified}}
	got := mergeVantages(prior, fresh)
	if len(got) != 1 || got[0].Outcome != OutcomeVerified {
		t.Errorf("a newer observation of the SAME vantage supersedes the older one: %+v", got)
	}
}

func TestMergeVantagesWithNothingFreshKeepsPrior(t *testing.T) {
	prior := []VantageResult{{Vantage: "local", Path: "data", Outcome: OutcomeVerified}}
	if got := mergeVantages(prior, nil); len(got) != 1 {
		t.Errorf("a run that observed nothing must not erase what was known: %+v", got)
	}
}

func podProbe(v probe.Vantage, p probe.Path, ok bool) probe.Result {
	return podPortProbe(v, p, 8080, ok)
}

func podPortProbe(v probe.Vantage, p probe.Path, port int32, ok bool) probe.Result {
	r := probe.Result{Layer: probe.LayerTCP, Target: "10.244.1.5", Port: port, Vantage: v, Path: p, OK: ok}
	if !ok {
		r.Tone = probe.ToneUnhealthy
	}
	return r
}

// backendHops is one backend as the fan-out in entries.go actually emits it: the
// Service hop carrying the port map, then the Pods hop behind it. Tests build on
// this rather than a bare Pods hop - a lone Pods hop is a shape production never
// produces, and testing it is why cross-backend leakage went unnoticed.
func backendHops(ns, name string, servicePort int32, targetPort string, probes ...probe.Result) []Hop {
	return []Hop{
		{
			Resource: ResourceRef{Kind: "Service", Namespace: ns, Name: name},
			Edge:     "Ingress->Service",
			Config:   &HopConfig{Ports: []PortMap{{Port: servicePort, TargetPort: targetPort}}},
		},
		{Resource: ResourceRef{Kind: "Pods", Namespace: ns}, Edge: "Service->Pods", Probes: probes},
	}
}

func ingressTrace(backends ...[]Hop) *Trace {
	hops := []Hop{{Resource: ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "web"}}}
	for _, b := range backends {
		hops = append(hops, b...)
	}
	return &Trace{Subject: ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "web"}, Downstream: hops}
}

// serviceTrace is the single-chain shape: the subject Service and its Pods.
func serviceTrace(name string, servicePort int32, targetPort string, probes ...probe.Result) *Trace {
	hops := backendHops("prod", name, servicePort, targetPort, probes...)
	hops[0].Edge = ""
	return &Trace{Subject: ResourceRef{Kind: "Service", Namespace: "prod", Name: name}, Downstream: hops}
}

func unreachableFrom(target string, v probe.Vantage, p probe.Path) RouteResult {
	return RouteResult{
		Route: "r", Target: target, TargetNamespace: "prod", Outcome: OutcomeUnreachable,
		ByVantage: []VantageResult{{Vantage: string(v), Path: string(p), Outcome: OutcomeUnreachable}},
	}
}

// The one boundary two observations can establish: the Service was unreachable
// from a vantage, yet the SAME vantage reached the Pods behind it directly, so
// the break is the Service's own routing.
func TestLocalizeBoundariesNamesServiceRoutingFromTheSandwich(t *testing.T) {
	tr := serviceTrace("checkout", 80, "8080", podProbe(probe.VantageInCluster, probe.PathData, true))
	tr.Routes = []RouteResult{unreachableFrom("checkout:80", probe.VantageInCluster, probe.PathData)}
	localizeBoundaries(tr)
	if got := tr.Routes[0].ByVantage[0].FailedBoundary; got != BoundaryServiceRouting {
		t.Errorf("FailedBoundary = %q, want %q - Service unreachable + Pods reachable from the SAME vantage localizes the break", got, BoundaryServiceRouting)
	}
}

// A different vantage reaching the Pods proves nothing about this one - that is
// the cross-vantage attribution the whole change exists to prevent.
func TestLocalizeBoundariesWillNotBorrowAnotherVantagesPodEvidence(t *testing.T) {
	tr := serviceTrace("checkout", 80, "8080", podProbe(probe.VantageLocal, probe.PathAPIServer, true))
	tr.Routes = []RouteResult{unreachableFrom("checkout:80", probe.VantageInCluster, probe.PathData)}
	localizeBoundaries(tr)
	if got := tr.Routes[0].ByVantage[0].FailedBoundary; got != "" {
		t.Errorf("FailedBoundary = %q, want empty", got)
	}
}

// The same borrowing, one axis over: pods behind a DIFFERENT backend Service
// answered. A multi-backend Ingress emits one Service+Pods hop per backend, so
// reading "the" Pods hop of a trace would let backend A's healthy pods localize
// backend B's failure - a confident red claim about a Service never probed.
func TestLocalizeBoundariesWillNotBorrowAnotherBackendsPodEvidence(t *testing.T) {
	tr := ingressTrace(
		backendHops("prod", "web", 80, "8080", podProbe(probe.VantageLocal, probe.PathData, true)),
		backendHops("prod", "api", 443, "8443"),
	)
	tr.Routes = []RouteResult{unreachableFrom("api:443", probe.VantageLocal, probe.PathData)}
	localizeBoundaries(tr)
	if got := tr.Routes[0].ByVantage[0].FailedBoundary; got != "" {
		t.Errorf("FailedBoundary = %q: web's pods answering says nothing about api", got)
	}
}

// Each backend still localizes from its OWN pods.
func TestLocalizeBoundariesLocalizesEachBackendFromItsOwnPods(t *testing.T) {
	tr := ingressTrace(
		backendHops("prod", "web", 80, "8080", podProbe(probe.VantageLocal, probe.PathData, true)),
		backendHops("prod", "api", 443, "8443"),
	)
	tr.Routes = []RouteResult{
		unreachableFrom("web:80", probe.VantageLocal, probe.PathData),
		unreachableFrom("api:443", probe.VantageLocal, probe.PathData),
	}
	localizeBoundaries(tr)
	if got := tr.Routes[0].ByVantage[0].FailedBoundary; got != BoundaryServiceRouting {
		t.Errorf("web should localize from its own pods, got %q", got)
	}
	if got := tr.Routes[1].ByVantage[0].FailedBoundary; got != "" {
		t.Errorf("api has no pod evidence, got %q", got)
	}
}

// A route and a pod probe are numbered in different port spaces - Service port
// vs containerPort. Reaching a pod on 8080 says nothing about a route whose
// traffic lands on 9090.
func TestLocalizeBoundariesWillNotBorrowAnotherPortsPodEvidence(t *testing.T) {
	tr := serviceTrace("checkout", 443, "9090", podPortProbe(probe.VantageInCluster, probe.PathData, 8080, true))
	tr.Routes = []RouteResult{unreachableFrom("checkout:443", probe.VantageInCluster, probe.PathData)}
	localizeBoundaries(tr)
	if got := tr.Routes[0].ByVantage[0].FailedBoundary; got != "" {
		t.Errorf("FailedBoundary = %q: a success on port 8080 is not evidence about 9090", got)
	}
}

func TestLocalizeBoundariesResolvesNamedTargetPort(t *testing.T) {
	tr := serviceTrace("checkout", 80, "http", podPortProbe(probe.VantageInCluster, probe.PathData, 8080, true))
	tr.Downstream[1].Config = &HopConfig{ContainerPorts: []ContainerPortRef{{Name: "http", Port: 8080}}}
	tr.Downstream[1].Probes = []probe.Result{podPortProbe(probe.VantageInCluster, probe.PathData, 8080, true)}
	tr.Routes = []RouteResult{unreachableFrom("checkout:80", probe.VantageInCluster, probe.PathData)}
	localizeBoundaries(tr)
	if got := tr.Routes[0].ByVantage[0].FailedBoundary; got != BoundaryServiceRouting {
		t.Errorf("a named targetPort must resolve through the container ports, got %q", got)
	}
}

// An unresolvable port mapping is not a licence to guess.
func TestLocalizeBoundariesDeclinesWhenPortMappingUnresolvable(t *testing.T) {
	tr := serviceTrace("checkout", 80, "http", podPortProbe(probe.VantageInCluster, probe.PathData, 8080, true))
	tr.Routes = []RouteResult{unreachableFrom("checkout:80", probe.VantageInCluster, probe.PathData)}
	localizeBoundaries(tr)
	if got := tr.Routes[0].ByVantage[0].FailedBoundary; got != "" {
		t.Errorf("FailedBoundary = %q with an unresolvable named targetPort", got)
	}
}

// Both sides failing is an undifferentiated failure: the break could be the
// Service OR the workload, so it must colour nothing.
func TestLocalizeBoundariesStaysSilentWhenPodsAlsoFailed(t *testing.T) {
	tr := serviceTrace("checkout", 80, "8080", podProbe(probe.VantageInCluster, probe.PathData, false))
	tr.Routes = []RouteResult{unreachableFrom("checkout:80", probe.VantageInCluster, probe.PathData)}
	localizeBoundaries(tr)
	if got := tr.Routes[0].ByVantage[0].FailedBoundary; got != "" {
		t.Errorf("FailedBoundary = %q, want empty when neither side is known good", got)
	}
}

func TestLocalizeBoundariesIgnoresSkippedPodProbes(t *testing.T) {
	skipped := podProbe(probe.VantageInCluster, probe.PathData, true)
	skipped.Skipped = true
	tr := serviceTrace("checkout", 80, "8080", skipped)
	tr.Routes = []RouteResult{unreachableFrom("checkout:80", probe.VantageInCluster, probe.PathData)}
	localizeBoundaries(tr)
	if got := tr.Routes[0].ByVantage[0].FailedBoundary; got != "" {
		t.Errorf("a skipped probe carries no observation: got %q", got)
	}
}

// A route that got through has no boundary to name.
func TestLocalizeBoundariesLeavesReachableRoutesAlone(t *testing.T) {
	tr := serviceTrace("checkout", 80, "8080", podProbe(probe.VantageInCluster, probe.PathData, true))
	tr.Routes = []RouteResult{{
		Route: "r", Target: "checkout:80", TargetNamespace: "prod", Outcome: OutcomeVerified,
		ByVantage: []VantageResult{{Vantage: "in-cluster", Path: "data", Outcome: OutcomeVerified}},
	}}
	localizeBoundaries(tr)
	if got := tr.Routes[0].ByVantage[0].FailedBoundary; got != "" {
		t.Errorf("FailedBoundary = %q on a verified route", got)
	}
}

// A same-named Service in another namespace is a distinct backend.
func TestLocalizeBoundariesKeepsNamespacesApart(t *testing.T) {
	tr := ingressTrace(
		backendHops("staging", "api", 80, "8080", podProbe(probe.VantageLocal, probe.PathData, true)),
		backendHops("prod", "api", 80, "8080"),
	)
	tr.Routes = []RouteResult{{
		Route: "r", Target: "api:80", TargetNamespace: "prod", Outcome: OutcomeUnreachable,
		ByVantage: []VantageResult{{Vantage: "local", Path: "data", Outcome: OutcomeUnreachable}},
	}}
	localizeBoundaries(tr)
	if got := tr.Routes[0].ByVantage[0].FailedBoundary; got != "" {
		t.Errorf("FailedBoundary = %q: staging/api's pods are not evidence about prod/api", got)
	}
}

// A break read off declared configuration is true of every vantage and observed
// by none. Without an explicit marker the consumer can only infer it from an
// empty ByVantage, and absence is ambiguous - which is how a static break came
// to be rendered as the selected vantage's failed dial.
func TestKnownStaticBreakIsMarkedAsConfigDerived(t *testing.T) {
	tr := &Trace{
		Subject:    ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "web"},
		Downstream: []Hop{{Resource: ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "web"}}},
		Routes: []RouteResult{{
			Route: "a.example.com/", Target: "missing:80", Outcome: OutcomeUnreachable,
			Evidence: "backend Service does not exist", Basis: BasisDeclared,
		}},
	}
	localizeBoundaries(tr)
	r := tr.Routes[0]
	if r.Basis != BasisDeclared {
		t.Errorf("Basis = %q, want %q", r.Basis, BasisDeclared)
	}
	if len(r.ByVantage) != 0 {
		t.Errorf("a config-derived break was dialled by nobody, so it carries no vantage rows: %+v", r.ByVantage)
	}
}

// Radar-as-a-Pod and a throwaway probe Job are both (in-cluster, data). Keyed
// on that pair alone, running the Job REPLACED Radar's own observation instead
// of adding a second one - the stronger, more specific evidence silently
// overwriting the evidence that came from Radar's real identity.
func TestMergeVantagesKeepsRadarAndProbeJobApart(t *testing.T) {
	prior := []VantageResult{{Vantage: "in-cluster", Path: "data", Source: probe.SourceRadar, Outcome: OutcomeVerified}}
	fresh := []VantageResult{{Vantage: "in-cluster", Path: "data", Source: probe.SourceProbeJob, Outcome: OutcomeUnreachable}}
	got := mergeVantages(prior, fresh)
	if len(got) != 2 {
		t.Fatalf("want Radar's own result kept alongside the Job's, got %+v", got)
	}
	if got[0].Source != probe.SourceRadar || got[0].Outcome != OutcomeVerified {
		t.Errorf("Radar's own in-cluster observation was overwritten: %+v", got)
	}
}

func TestMergeVantagesStillReplacesTheSameSource(t *testing.T) {
	prior := []VantageResult{{Vantage: "in-cluster", Path: "data", Source: probe.SourceProbeJob, Outcome: OutcomeUnreachable}}
	fresh := []VantageResult{{Vantage: "in-cluster", Path: "data", Source: probe.SourceProbeJob, Outcome: OutcomeVerified}}
	if got := mergeVantages(prior, fresh); len(got) != 1 || got[0].Outcome != OutcomeVerified {
		t.Errorf("a re-run of the SAME source supersedes its own older result: %+v", got)
	}
}

// An unset Source is Radar's own probe, so a legacy row and a fresh Radar row
// must be the same observer rather than accumulating duplicates.
func TestMergeVantagesTreatsEmptySourceAsRadar(t *testing.T) {
	prior := []VantageResult{{Vantage: "local", Path: "data", Outcome: OutcomeUnreachable}}
	fresh := []VantageResult{{Vantage: "local", Path: "data", Source: probe.SourceRadar, Outcome: OutcomeVerified}}
	if got := mergeVantages(prior, fresh); len(got) != 1 || got[0].Outcome != OutcomeVerified {
		t.Errorf("empty Source must mean radar, not a distinct observer: %+v", got)
	}
}

func TestPerVantageSplitsRadarFromProbeJob(t *testing.T) {
	radar := httpProbe(probe.VantageInCluster, probe.PathData, true, "HTTP 200")
	job := httpProbe(probe.VantageInCluster, probe.PathData, false, "connection refused")
	job.Source = probe.SourceProbeJob
	r, _ := routeFromProbes("r", "checkout:80", []probe.Result{radar, job}, false)
	if len(r.ByVantage) != 2 {
		t.Fatalf("two observers at the same vantage must stay apart: %+v", r.ByVantage)
	}
}

// A trace carries ONE diagnosis but can carry many routes. Unattributed, the
// selected-path panel rendered whichever route's cause happened to win, so an
// operator reading path B was shown path A's culprit under "THIS PATH".
func TestDiagnosisNamesTheRouteItExplains(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "web"},
		BrokenAt: -1,
		Routes: []RouteResult{
			{Route: "shop.example.com/web", Target: "web:80", Outcome: OutcomeVerified},
			{Route: "shop.example.com/api", Target: "api:80", Outcome: OutcomeUnreachable, Evidence: "connection refused"},
		},
	}
	d := computeDiagnosis(tr)
	if d == nil {
		t.Fatal("expected a diagnosis for the failed route")
	}
	if d.Route != "shop.example.com/api" {
		t.Errorf("Route = %q, want the route the diagnosis is actually about", d.Route)
	}
}

// With several failed routes the cause pins to none of them; claiming one would
// be the same misattribution in a new place.
func TestDiagnosisStaysUnattributedWhenSeveralRoutesFailed(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "web"},
		BrokenAt: -1,
		Routes: []RouteResult{
			{Route: "a/", Target: "a:80", Outcome: OutcomeUnreachable, Evidence: "refused"},
			{Route: "b/", Target: "b:80", Outcome: OutcomeUnreachable, Evidence: "timed out"},
		},
	}
	d := computeDiagnosis(tr)
	if d == nil {
		t.Fatal("expected a diagnosis")
	}
	if d.Route != "" && d.Route != "a/" && d.Route != "b/" {
		t.Errorf("Route = %q, want empty or one of the real routes", d.Route)
	}
}

// A benign scale-to-zero route is not a failure, so a single real failure
// alongside it is still solely attributable.
func TestDiagnosisIgnoresBenignRoutesWhenAttributing(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "web"},
		BrokenAt: -1,
		Routes: []RouteResult{
			{Route: "dormant/", Target: "d:80", Outcome: OutcomeUnreachable, Benign: true},
			{Route: "real/", Target: "r:80", Outcome: OutcomeUnreachable, Evidence: "refused"},
		},
	}
	if d := computeDiagnosis(tr); d == nil || d.Route != "real/" {
		t.Errorf("want the non-benign route attributed, got %+v", d)
	}
}

// A completed in-cluster run must land in the vantage the reader can actually
// select. Unstamped results were read as Radar's own, so a successful run put
// its evidence in an origin the UI filters out when Radar runs on a laptop -
// and the test appeared to have done nothing at all.
func TestInClusterResultsLandInTheProbeJobVantage(t *testing.T) {
	tr := &Trace{
		Subject:  ResourceRef{Kind: "Service", Namespace: "prod", Name: "web"},
		BrokenAt: -1,
		Routes: []RouteResult{{
			Route: "GET /", Target: "web:80", Outcome: OutcomeNotTested,
			ByVantage: []VantageResult{{Vantage: "local", Path: "data", Outcome: OutcomeNotTested}},
		}},
		Downstream: []Hop{{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "web"}}},
	}
	ApplyInClusterResults(tr, map[string][]probe.Result{
		InClusterResultKey("GET /", "web:80", ""): {{
			Layer: probe.LayerHTTP, OK: true, Tone: probe.ToneHealthy, Detail: "HTTP 200",
			Vantage: probe.VantageInCluster, Path: probe.PathData, Source: probe.SourceProbeJob,
		}},
	})
	var found *VantageResult
	for i := range tr.Routes[0].ByVantage {
		if tr.Routes[0].ByVantage[i].Source == probe.SourceProbeJob {
			found = &tr.Routes[0].ByVantage[i]
		}
	}
	if found == nil {
		t.Fatalf("no probe-job vantage row after an in-cluster run: %+v", tr.Routes[0].ByVantage)
	}
	if found.Outcome != OutcomeVerified {
		t.Errorf("probe-job outcome = %q, want %q", found.Outcome, OutcomeVerified)
	}
}

// The same borrowing, on the ISSUER axis: Radar's own in-cluster process and a
// throwaway probe Job are both in-cluster/data, but a source-scoped
// NetworkPolicy or mesh policy can admit one and refuse the other - so Radar
// reaching the Pods directly must never localize a boundary for the JOB's
// Service failure.
func TestLocalizeBoundariesWillNotBorrowAnotherSourcesPodEvidence(t *testing.T) {
	// Radar's own process reached the pods (Source empty = radar).
	tr := serviceTrace("checkout", 80, "8080", podPortProbe(probe.VantageInCluster, probe.PathData, 8080, true))
	// The probe JOB failed at the Service.
	tr.Routes = []RouteResult{{
		Route: "r", Target: "checkout:80", TargetNamespace: "prod", Outcome: OutcomeUnreachable,
		ByVantage: []VantageResult{{
			Vantage: string(probe.VantageInCluster), Path: string(probe.PathData),
			Source: probe.SourceProbeJob, Outcome: OutcomeUnreachable,
		}},
	}}
	localizeBoundaries(tr)
	if got := tr.Routes[0].ByVantage[0].FailedBoundary; got != "" {
		t.Errorf("FailedBoundary = %q, want empty - radar's own pod reach must not vouch for the probe Job's journey", got)
	}
}

// And the aligned case still localizes: the Job failed at the Service and the
// JOB ITSELF reached the pods directly.
func TestLocalizeBoundariesSameSourceStillLocalizes(t *testing.T) {
	pr := podPortProbe(probe.VantageInCluster, probe.PathData, 8080, true)
	pr.Source = probe.SourceProbeJob
	tr := serviceTrace("checkout", 80, "8080", pr)
	tr.Routes = []RouteResult{{
		Route: "r", Target: "checkout:80", TargetNamespace: "prod", Outcome: OutcomeUnreachable,
		ByVantage: []VantageResult{{
			Vantage: string(probe.VantageInCluster), Path: string(probe.PathData),
			Source: probe.SourceProbeJob, Outcome: OutcomeUnreachable,
		}},
	}}
	localizeBoundaries(tr)
	if got := tr.Routes[0].ByVantage[0].FailedBoundary; got != BoundaryServiceRouting {
		t.Errorf("FailedBoundary = %q, want %q", got, BoundaryServiceRouting)
	}
}
