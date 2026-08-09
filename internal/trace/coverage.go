package trace

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/probe"
)

// Coverage-honest projection of a trace. This is a server-authoritative VIEW
// computed alongside the verdict (computeCoverage) - it never changes the
// verdict; it describes only what was actually tested. The invariant: the
// per-route Outcome+Confidence reflect REAL-TRAFFIC evidence; behind-the-gate
// / apiserver / direct-pod facts live on Localization, structurally apart, so
// they can never be mistaken for a real-traffic pass/fail.

// Route outcome vocabulary (coverage-honest; pairs with a Confidence).
const (
	OutcomeVerified    = "verified"     // real HTTP 2xx
	OutcomeReached     = "reached"      // 3xx/4xx, or transport-only (TCP/TLS) - reached, route not verified
	OutcomeServerError = "server-error" // 5xx - reached, backend erroring
	OutcomeUnreachable = "unreachable"  // transport failure
	OutcomeNotTested   = "not-tested"   // no non-skipped probe ran for this route
)

// Confidence qualifies an outcome by HOW it was learned.
const (
	ConfidenceReal     = "real"     // tested the way real traffic flows (direct dial / in-cluster data path)
	ConfidenceIndirect = "indirect" // only the apiserver proxy reached it - annotates, never sets the headline
)

// VantageAPIServer is the ONE operator-facing name for the apiserver-proxy
// vantage: a real request that took the CONTROL-PLANE path (API server →
// endpoint/kubelet → pod), bypassing kube-proxy (ClusterIP routing) and
// NetworkPolicy. Named by path, not real/fake - it proves the backend answers,
// not that the Service's real data path delivers. MUST stay identical to the TS
// const VIA_API_SERVER (reachVerdict.ts); pinned by TestVantageAPIServerName and
// the TS counterpart so the two headline generators can't drift.
const VantageAPIServer = "via API server"

// Skip reason classes. Only "coverage" + "vantage" are real coverage gaps that
// should cap a full-green headline; "benign" loses no coverage.
const (
	SkipClassCoverage = "coverage" // a declared path or protocol we genuinely couldn't test
	SkipClassBenign   = "benign"   // no coverage lost (sampling, duplicate path, or inapplicable proxy beside a direct test)
	SkipClassVantage  = "vantage"  // this execution path couldn't run from here; a later vantage may resolve the gap
)

// Coverage counts intended routes: Tested = routes that got any non-skipped
// probe; Passed = reachable (verified/reached); Failed = unreachable/server-error;
// Skipped = routes/targets we could not test (len(NotTested)).
type Coverage struct {
	Tested  int `json:"tested"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
	// Derived counts routes known broken WITHOUT dialling them - from what is
	// declared, or from current cluster state. They are real breaks, but they are
	// not test results, so they are counted apart from Tested/Passed/Failed
	// rather than reported as requests that failed.
	Derived int `json:"derived,omitempty"`
}

// ProbeFact is a minimal, JSON-light record of one probe, used for the
// behind-the-gate Localization list on a route.
type ProbeFact struct {
	Layer  string `json:"layer"`
	Path   string `json:"path,omitempty"` // data | apiserver | "" (direct)
	Target string `json:"target,omitempty"`
	OK     bool   `json:"ok"`
	Tone   string `json:"tone,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// RouteResult is one INTENDED route (a declared host+path→backend entry, or the
// Service→Pods chain for a Service subject) and its real-traffic outcome.
type RouteResult struct {
	Route  string `json:"route"`            // host+path identity, or the subject/port identity
	Target string `json:"target,omitempty"` // backend svc:port
	// TargetNamespace is the backend Service's namespace. For a cross-namespace
	// Gateway API backendRef this differs from the subject namespace, so the
	// in-cluster probe must dial an FQDN (name.ns.svc) rather than the bare name -
	// otherwise it resolves in the probe pod's namespace and hits the wrong
	// Service or NXDOMAIN.
	TargetNamespace string `json:"targetNamespace,omitempty"`
	Outcome         string `json:"outcome"` // see Outcome* constants
	// FailedLayer names which layer failed on a non-reachable route so the UI can say
	// exactly what broke: "tcp" | "tls" | "http" (unreachable), or "upstream" for a
	// 502/504 where HTTP was reached but the gateway couldn't reach its backend.
	// Empty for a reachable outcome.
	FailedLayer string `json:"failedLayer,omitempty"`
	Confidence  string `json:"confidence,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
	Command     string `json:"command,omitempty"`
	// Benign marks a route that is unreachable by DESIGN - the backing workload is
	// intentionally scaled to 0 (deliberate dormancy, not an outage). The Outcome
	// stays unreachable (factually true) but the headline/tone read amber-benign,
	// not red.
	Benign bool `json:"benign,omitempty"`
	// Localization holds behind-the-gate facts (apiserver proxy, direct-pod
	// dials). They explain WHERE a route broke / that a component answers, but
	// are kept apart so they can never feed Outcome/Confidence.
	Localization []ProbeFact `json:"localization,omitempty"`
	// InClusterRequest is the best-guess concrete request to test this route
	// from inside the cluster (the runner dials the Service directly, bypassing
	// the Ingress). It is a STARTING POINT the user can edit before running -
	// when the route's path is a pattern (regex/wildcard) there is no single
	// faithful request, so PathGuessed marks it as a guess. nil for routes that
	// aren't testable that way (a known static break with no backend).
	InClusterRequest *ProbeRequest `json:"inClusterRequest,omitempty"`
	// Basis says where this route's Outcome came from. Empty (the normal case)
	// means it was OBSERVED - some vantage dialled it and ByVantage carries who.
	// BasisDeclared / BasisState mean it was DERIVED and no vantage dialled it at
	// all, so it must never be attributed to whichever vantage the reader happens
	// to have selected, nor described as a request that failed.
	//
	// This is stated rather than inferred from an empty ByVantage: absence is
	// ambiguous, and a consumer guessing at it is how a derived break came to be
	// rendered as a laptop's failed observation.
	Basis string `json:"basis,omitempty"`
	// ByVantage is each vantage's OWN view of this route.
	//
	// Outcome/Confidence/Evidence above are a documented lossy rollup: they
	// bucket by mechanism and then take worst-wins, so a route that works
	// in-cluster and fails from a laptop collapses to "unreachable" with no
	// field left to say where it did work. Consumers that need to answer "did
	// THIS path work from THIS vantage" must read this instead.
	//
	// It costs nothing to produce: the probes handed to routeFromProbes are
	// already scoped to one route by the caller, and each one already carries
	// its vantage - the rollup simply discarded it.
	ByVantage []VantageResult `json:"byVantage,omitempty"`
}

// VantageResult is one vantage's unmerged view of a route. Keyed by
// (Vantage, Path) because those two together are what an operator picks
// between: the same in-cluster vantage relayed through the API server is a
// different claim from one that used the real network path.
type VantageResult struct {
	Vantage string `json:"vantage"` // in-cluster | local
	Path    string `json:"path"`    // data | apiserver
	// Source is WHO dialled - radar | probe-job. Empty means radar. Kept apart
	// from Vantage because a throwaway Job and Radar-as-a-Pod share a vantage
	// and a path but are different observers with different identities.
	Source  string `json:"source,omitempty"`
	Outcome string `json:"outcome"`
	// Confidence mirrors the rollup's rule per group: anything relayed by the
	// API server is indirect no matter how clean the response was.
	Confidence  string `json:"confidence,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
	FailedLayer string `json:"failedLayer,omitempty"`
	// FailedBoundary names WHICH hop-to-hop boundary broke, when two
	// observations either side of one boundary establish it. Empty means we
	// could not tell - which is the common case and must never be guessed.
	// See localizeBoundaries.
	FailedBoundary string `json:"failedBoundary,omitempty"`
	// Segment names which part of the DECLARED path this row's dial exercised.
	// "backend" means the dial went to the backend Service/Pods while the route
	// has a front door (Ingress/Gateway/Route) the dial bypassed - so this row
	// can never vouch for the entry path. Empty means the route has no separate
	// entry segment (or the row's dial is of the entry itself). Topology
	// decides, never protocol - a TCP LoadBalancer route gets identical
	// semantics.
	Segment string `json:"segment,omitempty"`
}

// SegmentBackend marks a vantage row whose dial bypassed the route's front door.
const SegmentBackend = "backend"

// RouteBackendScoped reports whether a route's evidence is entirely
// backend-segment dials - rows exist and every one bypassed the front door, so
// nothing in this route has exercised the entry path.
func RouteBackendScoped(r RouteResult) bool {
	if len(r.ByVantage) == 0 {
		return false
	}
	for _, v := range r.ByVantage {
		if v.Segment != SegmentBackend {
			return false
		}
	}
	return true
}

// traceHasFrontDoor reports whether the declared path includes an entry
// segment (Ingress/Gateway/Route) that a backend dial bypasses. Topological,
// deliberately protocol-blind.
func traceHasFrontDoor(t *Trace) bool {
	if t == nil {
		return false
	}
	if len(t.Upstreams) > 0 {
		return true
	}
	switch t.Subject.Kind {
	case "Ingress", "Gateway", "HTTPRoute", "GRPCRoute":
		return true
	}
	return false
}

// BoundaryServiceRouting is the one boundary we can currently localize: the
// Service could not be reached from a vantage, yet that SAME vantage reached
// the Pods behind it directly. The packets are getting to the workload, so what
// is between them - the Service's own routing (targetPort, selector, endpoints)
// - is where it breaks.
const BoundaryServiceRouting = "service-routing"

// A route's Outcome is either OBSERVED (some vantage dialled it; ByVantage says
// who) or DERIVED. Derived splits in two, because they are different classes of
// fact and describing them alike overstates one of them:
//
//   - BasisDeclared - read off what is DECLARED. A backendRef naming a Service
//     that does not exist is broken no matter what the cluster is doing.
//   - BasisState - read off CURRENT CLUSTER STATE, such as a backend with no
//     ready endpoints. True right now; it changes when the workload does.
//
// Neither was dialled, so neither carries ByVantage rows and neither may be
// rendered in the language of a request that got through or failed.
const (
	BasisDeclared = "declared-config"
	BasisState    = "cluster-state"
)

// localizeBoundaries attributes a route failure to a specific boundary, but
// only where two observations on OPPOSITE sides of that boundary establish it.
//
// Hops are probed independently and concurrently, so a hop result is a
// target-check, not a segment of one request - which is exactly why this cannot
// be inferred from a single failing hop. What CAN be concluded is the sandwich:
// Service unreachable from vantage V + Pods reachable from the same vantage V
// means the break is the Service's routing. Anything else gets no boundary at
// all; an undifferentiated failure must colour nothing.
func localizeBoundaries(t *Trace) {
	if t == nil {
		return
	}
	pairs := backendPairs(t)
	if len(pairs) == 0 {
		return
	}
	for i := range t.Routes {
		r := &t.Routes[i]
		name, servicePort, ok := routeBackend(*r)
		if !ok {
			continue
		}
		ns := r.TargetNamespace
		if ns == "" {
			ns = t.Subject.Namespace
		}
		pair, ok := pairs[backendKey(ns, name)]
		if !ok {
			continue
		}
		// The two sides of the sandwich are numbered in DIFFERENT port spaces: a
		// route names the Service port, a pod probe dials a containerPort. Compare
		// them only through the Service's own mapping; where it can't be resolved
		// there is no sandwich, and an unresolvable mapping must localize nothing
		// rather than compare unrelated numbers.
		podPort, ok := podPortForService(pair.svc, pair.pods, servicePort)
		if !ok {
			continue
		}
		reached := reachedPodOrigins(pair.pods, podPort)
		if len(reached) == 0 {
			continue
		}
		for j := range r.ByVantage {
			v := &r.ByVantage[j]
			if v.Outcome != OutcomeUnreachable {
				continue
			}
			if reached[originIdentity(v.Vantage, v.Path, v.Source)] {
				v.FailedBoundary = BoundaryServiceRouting
			}
		}
	}
}

func backendKey(namespace, name string) string { return namespace + "\x00" + name }

// hopPorts is the hop's declared Service port map, nil-safe.
func hopPorts(h Hop) []PortMap {
	if h.Config == nil {
		return nil
	}
	return h.Config.Ports
}

// backendPair is one backend Service hop with the Pods hop that sits behind it.
type backendPair struct {
	svc  *Hop
	pods *Hop
}

// backendPairs indexes each backend Service by identity so a route's evidence is
// only ever read from ITS OWN backend. A multi-backend Ingress/Route emits one
// Service+Pods hop per backend (see the fan-out loops in entries.go), so reading
// "the Pods hop" of a trace would let one backend's healthy pods localize a
// different backend's failure - the same cross-attribution the per-vantage split
// exists to prevent, one axis over.
func backendPairs(t *Trace) map[string]backendPair {
	out := map[string]backendPair{}
	hops := t.Downstream
	add := func(svc, pods *Hop) {
		if svc.Resource.Name == "" {
			return
		}
		out[backendKey(svc.Resource.Namespace, svc.Resource.Name)] = backendPair{svc: svc, pods: pods}
	}
	if branches := downstreamBranches(hops); len(branches) > 0 {
		for _, b := range branches {
			if b.end-b.start >= 2 && hops[b.start+1].Resource.Kind == "Pods" {
				add(&hops[b.start], &hops[b.start+1])
			}
		}
		return out
	}
	// Single chain (Service subject): the subject hop and its Pods.
	if len(hops) >= 2 && hops[0].Resource.Kind == "Service" && hops[1].Resource.Kind == "Pods" {
		add(&hops[0], &hops[1])
	}
	return out
}

// routeBackend reads the backend Service name and Service port out of a route's
// Target ("name:port"). Targets that name no concrete backend port - the
// port-agnostic ":80 -> 8080" front-door form - yield false: with no port there
// is nothing to line up against a pod probe.
func routeBackend(r RouteResult) (name string, port int32, ok bool) {
	target := strings.TrimSpace(r.Target)
	i := strings.LastIndex(target, ":")
	if i <= 0 {
		return "", 0, false
	}
	name = strings.TrimSpace(target[:i])
	p, err := strconv.Atoi(strings.TrimSpace(target[i+1:]))
	// Bounded to the valid TCP port range before the int32 narrowing - an
	// out-of-range number is a malformed target, not a port to wrap around.
	if name == "" || err != nil || p <= 0 || p > 65535 {
		return "", 0, false
	}
	return name, int32(p), true
}

// podPortForService maps a Service port to the pod-side port it forwards to,
// reading named ports from the Pods hop's config snapshot. Reports false when
// the Service declares no such port or a named port resolves to nothing - the
// caller must then decline to localize.
func podPortForService(svc, pods *Hop, servicePort int32) (int32, bool) {
	if svc.Config == nil {
		return 0, false
	}
	for _, pm := range svc.Config.Ports {
		if pm.Port != servicePort {
			continue
		}
		return resolvePortMap(pm, func(name string) (int32, bool) {
			if pods.Config == nil {
				return 0, false
			}
			for _, cp := range pods.Config.ContainerPorts {
				if cp.Name == name {
					return cp.Port, true
				}
			}
			return 0, false
		})
	}
	return 0, false
}

// reachedPodOrigins returns the (vantage, path) origins that reached a pod on
// this specific port. Only a real success counts - a skip carries no observation,
// and a success on a DIFFERENT container port says nothing about this one.
func reachedPodOrigins(pods *Hop, port int32) map[string]bool {
	out := map[string]bool{}
	for _, pr := range pods.Probes {
		if pr.Skipped || !pr.OK || pr.Port != port {
			continue
		}
		out[originIdentity(string(pr.Vantage), string(pr.Path), pr.Source)] = true
	}
	return out
}

// originIdentity is the full identity of WHO observed something: vantage, path,
// and issuer. Source matters exactly as much here as in mergeVantages - Radar's
// own in-cluster process and a throwaway probe Job are both in-cluster/data, but
// a source-scoped NetworkPolicy or mesh policy can admit one and refuse the
// other, so one identity's direct-pod reach must never localize a boundary for
// the other's Service failure. An empty source is Radar's own process.
func originIdentity(vantage, path, source string) string {
	if source == "" {
		source = probe.SourceRadar
	}
	return vantage + "\x00" + path + "\x00" + source
}

// ProbeRequest is a concrete request a user can run against a Service from
// inside the cluster. Every field is derivable from the declared route; Scheme
// is meaningful only for HTTP/HTTPS and comes from the BACKEND Service port
// (not the Ingress TLS, which terminates at the front door the in-cluster dial
// bypasses).
type ProbeRequest struct {
	Protocol    string `json:"protocol"`         // http | https | tcp
	Scheme      string `json:"scheme,omitempty"` // http | https; HTTP(S) only
	Host        string `json:"host,omitempty"`   // Host header / SNI (omitted when none declared)
	Path        string `json:"path,omitempty"`   // HTTP request path
	PathGuessed bool   `json:"pathGuessed,omitempty"`
}

// RouteSkip is one route/target we could not actively test, with why + a
// copyable command where one can be formed honestly.
type RouteSkip struct {
	Route       string `json:"route,omitempty"`
	Reason      string `json:"reason"`
	ReasonClass string `json:"reasonClass,omitempty"`
	Command     string `json:"command,omitempty"`
}

// computeCoverage builds the coverage projection from the already-finalized
// trace. ADDITIVE: it reads Downstream/Upstreams/branches/probes and writes
// only the Coverage/Routes/NotTested/BrokenRoute fields. It never touches
// Verdict or BrokenAt.
func computeCoverage(t *Trace) {
	if t == nil {
		return
	}
	if t.BrokenAt >= 0 && t.BrokenAt < len(t.Downstream) {
		ref := t.Downstream[t.BrokenAt].Resource
		t.BrokenRoute = &ref
	}

	routes, unprobed := buildRoutes(t)
	t.Routes = routes
	localizeBoundaries(t)
	upgradeDefinitiveBackendDown(t)
	t.NotTested = append(buildNotTested(t), unprobed...)
	markBenignServiceSkips(t)

	// A static-only trace (no probing) leaves Coverage nil; the headline below
	// still resolves ("Configuration only - not yet tested").
	recountCoverage(t)
	// Single source of truth for the banner sentence - UI and MCP both read this.
	t.Headline = CoverageHeadline(t)
	t.Diagnosis = computeDiagnosis(t)
	t.EntryProblems = computeEntryProblems(t)
}

// upgradeDefinitiveBackendDown promotes a Service route's unreachability from
// indirect (apiserver-proxy-only) to a DEFINITIVE real failure when the backend
// has zero ready endpoints. Zero ready endpoints is an authoritative cache fact
// (pod / EndpointSlice readiness), not a vantage limitation - so the proxy's "no
// ready endpoints" IS the real answer, and every coverage-tone surface should
// read it as a genuine break (red), never the soft "couldn't confirm via the
// proxy" amber that indirect normally earns. Scoped to a single-Service subject,
// where the one route maps unambiguously to the one backend; multi-backend entries
// keep the conservative indirect read (a route can't be matched to its backend's
// readiness here without risking a mis-attribution). All ports of a Service share
// the same backend pods, so a 0-ready backend is definitive for every port.
func upgradeDefinitiveBackendDown(t *Trace) {
	if t.Subject.Kind != "Service" {
		return
	}
	// Definitive only: a CRITICAL no-ready-endpoints finding means zero ready pods
	// (authoritative). The WARNING variant ("couldn't verify whether the workload
	// is scaled to 0", Rollout lookup failed) is genuinely uncertain - leave it
	// soft so an unverifiable state is never condemned as a hard break.
	definitive := false
	for _, h := range t.Downstream {
		for i := range h.Findings {
			if h.Findings[i].Severity == SeverityCritical && isNoReadyEndpointsFinding(&h.Findings[i]) {
				definitive = true
				break
			}
		}
	}
	if !definitive {
		return
	}
	for i := range t.Routes {
		r := &t.Routes[i]
		if r.Outcome == OutcomeUnreachable && r.Confidence == ConfidenceIndirect && !r.Benign {
			r.Confidence = ConfidenceReal
		}
	}
}

// recountCoverage derives the tested/passed/failed/skipped tally from the
// current Routes + NotTested. Split out of computeCoverage so a later fold (an
// in-cluster probe upgrading a route) can re-tally without rebuilding routes.
func recountCoverage(t *Trace) {
	if len(t.Routes) == 0 && len(t.NotTested) == 0 {
		return
	}
	// The counting unit is coherent per HOST: a not-tested route and the raw
	// host-level skip rows for its host describe the SAME gap. A host with ≥1
	// not-tested route contributes its ROUTES (each not-tested route is its own
	// gap - a sibling rule on the same host must not be swallowed) and its raw
	// skip rows are absorbed entirely; a host with no matching route contributes
	// its row count as-is.
	skipped := 0
	// Host → count of raw non-benign skip rows. Keyed on the host
	// (RouteSkip.Route is a probe target like "shop.example.com" or
	// "host:port"); the not-tested route below carries the same host, so the
	// two key spaces line up and the absorption actually fires.
	skipRowsByHost := map[string]int{}
	for _, s := range t.NotTested {
		// Benign skips (sampled identical pods, duplicate default backend) lose no
		// coverage by design - counting them would downgrade a fully-tested route
		// to footnote-green "· 1 not tested", contradicting SkipClassBenign.
		if s.ReasonClass == SkipClassBenign {
			continue
		}
		skipped++
		if h := routeHostKey(s.Route); h != "" {
			skipRowsByHost[h]++
		}
	}
	consumedHosts := map[string]bool{}
	cov := Coverage{}
	for _, r := range t.Routes {
		// A derived break was never dialled, so counting it as Tested/Failed made
		// the coverage line report a request that failed when none was sent.
		if r.Basis != "" {
			cov.Derived++
			continue
		}
		switch r.Outcome {
		case OutcomeVerified, OutcomeReached:
			cov.Tested++
			cov.Passed++
		case OutcomeUnreachable, OutcomeServerError:
			cov.Tested++
			cov.Failed++
		case OutcomeNotTested:
			// A route whose only evidence was a DNS resolve (transport probes
			// skipped from this vantage) is a coverage gap. The first not-tested
			// route on a host consumes ALL of that host's raw skip rows (they
			// describe the same untested front door); subsequent sibling routes on
			// the host just count themselves.
			// Service subjects get NO second per-port deduction here: buildNotTested
			// already absorbs every same-gap TCP row structurally, so a row that
			// SURVIVES beside a Service route is a distinct gap by construction -
			// the deliberately retained UDP/SCTP sibling of a TCP candidate. A
			// port-number deduction re-erased exactly that gap from the count.
			if t.Subject.Kind != "Service" {
				if h := routeResultHostKey(r); h != "" && !consumedHosts[h] {
					if n := skipRowsByHost[h]; n > 0 {
						skipped -= n
						consumedHosts[h] = true
					}
				}
			}
			skipped++
		default:
			cov.Tested++
		}
	}
	cov.Skipped = skipped
	t.Coverage = &cov
}

// InClusterResultKey is the identity under which an in-cluster probe result is
// folded back onto its route. It must be UNIQUE per intended route: two routes
// that share a backend svc:port but differ by host/path (same Target) or by
// namespace (same name:port, different TargetNamespace) must NOT collide, or a
// clean probe of one route would falsely vouch for the other (a false
// "verified over live traffic"). Producer (internal/mcp runInClusterTests) and
// consumer (ApplyInClusterResults) MUST key identically.
func InClusterResultKey(route, target, targetNamespace string) string {
	return route + "\x00" + target + "\x00" + targetNamespace
}

// mergeVantages upserts fresh per-vantage results over prior ones, keyed by
// (vantage, path). A vantage the new run did not exercise keeps its previous
// result rather than disappearing; a vantage it did exercise is replaced,
// because the newer observation of the SAME vantage supersedes the older one.
// Prior order is preserved so the UI's rows do not jump on a re-run.
func mergeVantages(prior, fresh []VantageResult) []VantageResult {
	if len(fresh) == 0 {
		return prior
	}
	// Source is part of the key: Radar's own in-cluster dial and a throwaway
	// Job's are both (in-cluster, data), so keying without it let a Job run
	// silently replace Radar's own observation instead of adding to it.
	key := func(v VantageResult) string { return v.Vantage + "\x00" + v.Path + "\x00" + vantageSource(v) }
	byKey := make(map[string]VantageResult, len(fresh))
	for _, v := range fresh {
		byKey[key(v)] = v
	}
	out := make([]VantageResult, 0, len(prior)+len(fresh))
	seen := make(map[string]bool, len(prior))
	for _, v := range prior {
		k := key(v)
		seen[k] = true
		if updated, ok := byKey[k]; ok {
			out = append(out, updated)
			continue
		}
		out = append(out, v)
	}
	for _, v := range fresh {
		if !seen[key(v)] {
			out = append(out, v)
		}
	}
	return out
}

// ApplyInClusterResults folds in-cluster probe results (keyed by InClusterResultKey)
// into the finalized trace. The in-cluster data path IS real traffic, so a route
// the apiserver proxy could only reach INDIRECTLY is re-derived from the live
// result and earns ConfidenceReal when reached/verified - closing the honesty gap
// the proxy vantage leaves open. Counts, headline, and diagnosis are recomputed so
// the whole projection stays consistent. Benign (intentional scale-to-0) routes
// are left untouched - they are deliberately dormant, not a path to confirm.
// Routes with no in-cluster result keep their prior (proxy/static) outcome.
//
// A run that folded NOTHING (nil/empty map, or no key matching a route) leaves the
// trace completely unchanged: the runner returns an empty map for every degraded
// case (impersonation failure, probe cap, unclean throwaway-pod probe, guessed
// path), and a test that produced no evidence must not move a verdict, prune a
// skip row, or clear a reason.
func ApplyInClusterResults(t *Trace, byTarget map[string][]probe.Result) {
	if t == nil {
		return
	}
	folded := false
	for i := range t.Routes {
		if t.Routes[i].Benign {
			continue
		}
		res := byTarget[InClusterResultKey(t.Routes[i].Route, t.Routes[i].Target, t.Routes[i].TargetNamespace)]
		if len(res) == 0 {
			continue
		}
		rr, ok := routeFromProbes(t.Routes[i].Route, t.Routes[i].Target, res, traceHasFrontDoor(t))
		if !ok {
			continue
		}
		// Preserve the route's identity guess so the agent/UI keep the editable
		// request; the outcome/confidence/evidence now reflect the live dataplane.
		rr.InClusterRequest = t.Routes[i].InClusterRequest
		rr.TargetNamespace = t.Routes[i].TargetNamespace
		// The ROLLUP is still replaced - an in-cluster result is real traffic and
		// supersedes a proxy-only verdict, which is the whole point of this fold.
		// The per-vantage list is MERGED instead: this run only observed the
		// in-cluster vantage, and replacing the list wholesale would delete the
		// laptop's and the proxy's own results, which are still true and are
		// exactly the disagreement ByVantage exists to preserve.
		rr.ByVantage = mergeVantages(t.Routes[i].ByVantage, rr.ByVantage)
		t.Routes[i] = rr
		folded = true
	}
	if !folded {
		return
	}
	// A route that was only DNS-resolvable from the laptop/proxy vantage carries a
	// SkipClassVantage "run radar in-cluster" row in NotTested. When the live pass
	// just upgraded that same route to a real reach/verify, that advice is stale and
	// contradictory (the user already ran in-cluster and it passed), and recountCoverage
	// would count the route as Passed AND its same-host skip row as Skipped. Drop the
	// vantage rows the live pass resolved before recounting.
	// Host-level, because the vantage skip rows themselves are host-level (one
	// row per probe target). But a host is only "resolved" when EVERY non-benign
	// route on it now carries a real reach/verify: with a partial fold (probe
	// cap, throwaway-denied sibling), /web passing must not delete the advice
	// row that is still the truth for /admin on the same host.
	// Service-subject rows are PORT-scoped instead: their skip target is
	// "port N" while the route target is "service:N", so the two key spaces
	// meet on the port number. Only vantage skips may be removed either way -
	// a UDP/SCTP coverage gap can share a number with TCP and is never
	// resolved by a successful TCP result.
	resolvedHosts := map[string]bool{}
	unresolvedHosts := map[string]bool{}
	resolvedPorts := map[string]bool{}
	unresolvedPorts := map[string]bool{}
	for i := range t.Routes {
		r := t.Routes[i]
		if r.Benign {
			continue
		}
		resolved := r.Confidence == ConfidenceReal && (r.Outcome == OutcomeVerified || r.Outcome == OutcomeReached)
		if h := routeResultHostKey(r); h != "" {
			if resolved {
				resolvedHosts[h] = true
			} else {
				unresolvedHosts[h] = true
			}
		}
		if t.Subject.Kind == "Service" {
			if p := portKey(r.Target); p != "" {
				if resolved {
					resolvedPorts[p] = true
				} else {
					unresolvedPorts[p] = true
				}
			}
		}
	}
	for h := range unresolvedHosts {
		delete(resolvedHosts, h)
	}
	for p := range unresolvedPorts {
		delete(resolvedPorts, p)
	}
	if len(resolvedHosts) > 0 || len(resolvedPorts) > 0 {
		kept := t.NotTested[:0]
		for _, s := range t.NotTested {
			if t.Subject.Kind == "Service" && s.ReasonClass == SkipClassVantage &&
				resolvedPorts[portKey(s.Route)] {
				continue
			}
			if s.ReasonClass == SkipClassVantage &&
				resolvedHosts[routeHostKey(s.Route)] {
				continue
			}
			kept = append(kept, s)
		}
		t.NotTested = kept
	}
	// A static would-deny is a PREDICTION; live in-cluster traffic that actually
	// reached the backend is ground truth that the rule isn't blocking this path.
	// Downgrade the surviving would-deny WARNING so computeDiagnosis can't
	// re-promote a prediction that the confirmed live success contradicts.
	reconcileInClusterPolicy(t)
	// Same ground-truth principle for the targetPort suspicion: live in-cluster
	// traffic that reached the suspect Service port proves a listener exists, so
	// downgrade the surviving svc:targetport-no-listener WARNING before computeDiagnosis
	// can re-promote a static guess the confirmed live success contradicts. The static
	// reconcileTargetPortAdvisory reads h.Probes, which the in-cluster flow stamps only
	// after this recompute - so the fold must happen off the passed-backend ports here.
	reconcileInClusterTargetPort(t)
	// Re-derive the verdict over the updated findings, mirroring BuildTraceWithOptions:
	// a would-deny WARNING that reconcileInClusterPolicy just downgraded to info must
	// not leave a stale 'degraded' beside a now-healthy headline, and a surviving real
	// critical re-derives honestly to broken. Only a genuine special-shape unknown
	// (selectorless / RBAC / lookup failure / timed out; those set UnknownClass) stays
	// unknown. An unknown produced by the coverage collapse of a healthy-but-indirect
	// laptop trace (UnknownClass empty) MUST re-derive here, or the in-cluster pass that
	// just upgraded its routes to real can never lift it off unknown.
	if !(t.Verdict == VerdictUnknown && t.UnknownClass != "") {
		// computeVerdict only sets t.Reason when it's empty, so a prior "all routes
		// broken" sentence would survive a flip toward healthy. Clear it first so the
		// fresh verdict re-explains (or leaves it empty when there's nothing to explain).
		t.Reason = ""
		t.Verdict, t.BrokenAt = computeVerdict(t)
		// Mirror BuildTraceWithOptions: computeVerdict reads only static findings,
		// while probe-failure evidence (a failed entry front-door dial) lives ONLY
		// in the hop probes and reaches the verdict via reviseVerdictWithProbes.
		// Without this re-run, a clean backend fold would silently discard the
		// entry-hop failure and flip a broken front door to healthy - a backend-only
		// in-cluster success must never vouch for the entry segment.
		t.Verdict, t.BrokenAt = reviseVerdictWithProbes(t)
	}
	// BrokenAt may have moved (a live confirmation cleared an earlier break);
	// recountCoverage never refreshes BrokenRoute, so re-derive it the same way
	// computeCoverage does - nil when nothing is broken - or a healthy/degraded
	// trace ships a stale brokenRoute ref pointing at the now-contradicted hop.
	if t.BrokenAt >= 0 && t.BrokenAt < len(t.Downstream) {
		ref := t.Downstream[t.BrokenAt].Resource
		t.BrokenRoute = &ref
	} else {
		t.BrokenRoute = nil
	}
	recountCoverage(t)
	t.Headline = CoverageHeadline(t)
	t.Diagnosis = computeDiagnosis(t)
	t.EntryProblems = computeEntryProblems(t)
	// Same collapse as the standard path (BuildTraceWithOptions): ship the one
	// coverage-honest verdict so REST, UI, and MCP never diverge on the in-cluster
	// flow.
	t.Verdict = CoverageVerdict(t)
}

// reconcileInClusterPolicy downgrades a static netpol:would-deny WARNING to the
// reassuring info note on any Pods hop whose route was confirmed reachable over
// REAL in-cluster traffic. Without this, ApplyInClusterResults would fold the
// live success into the route outcome but leave the contradicting prediction on
// the hop, where primaryProblemFinding could re-promote it as the lead cause.
func reconcileInClusterPolicy(t *Trace) {
	passed, anyPass := passedBackends(t)
	if !anyPass {
		return
	}
	// downgrade rewrites a would-deny finding ONLY when the real pass actually
	// covered the port the deny is about (or the deny is all-ports). A pass on
	// :80 must not clear a :443 prediction. ports==nil means this branch had no
	// real pass at all.
	downgrade := func(start, end int, ports map[int32]bool) {
		for i := start; i < end && i < len(t.Downstream); i++ {
			h := &t.Downstream[i]
			if h.Meta == nil || h.Meta["policyVerdict"] != "would-deny" {
				continue
			}
			if !realPassCoversDenyPort(ports, hopDenyPort(h.Meta)) {
				continue
			}
			idx := findingIndexByCode(h.Findings, codePolicyWouldDeny)
			if idx < 0 {
				continue
			}
			h.Findings[idx] = Finding{
				Code:     codePolicyWouldDeny,
				Severity: SeverityInfo,
				Message:  "Traffic got through from inside the cluster. A network rule here would block it, but live in-cluster traffic actually reached the backend - so the rule isn't blocking this path.",
				Command:  h.Findings[idx].Command,
			}
			sortFindingsBySeverity(h.Findings)
		}
	}
	branches := downstreamBranches(t.Downstream)
	if len(branches) == 0 {
		// Single chain (Service subject): the one route covers the whole downstream.
		// Pool every passed port across the single route(s).
		downgrade(0, len(t.Downstream), anyPassedPorts(passed))
		return
	}
	for _, b := range branches {
		if ports, ok := branchPassedPorts(passed, t.Downstream[b.start].Resource); ok {
			downgrade(b.start, b.end, ports)
		}
	}
}

// reconcileInClusterTargetPort downgrades a static svc:targetport-no-listener
// WARNING to the reassuring info note on any Service hop whose suspect ports were
// all covered by REAL in-cluster traffic. The static path reconciles this off the
// hop's own probes (reconcileTargetPortAdvisory), but the in-cluster flow stamps
// those probes onto the hops only AFTER ApplyInClusterResults re-derives the
// verdict/diagnosis - so the suspect-port suspicion must be cleared here, off the
// passed-backend ports, or the route reads verified-over-real-traffic while the hop
// still condemns the targetPort the live traffic just proved has a listener.
// Mirrors reconcileInClusterPolicy: only a real pass on the exact suspect port
// clears it (a pass whose port couldn't be pinned does not).
func reconcileInClusterTargetPort(t *Trace) {
	passed, anyPass := passedBackends(t)
	if !anyPass {
		return
	}
	downgrade := func(start, end int, ports map[int32]bool) {
		for i := start; i < end && i < len(t.Downstream); i++ {
			h := &t.Downstream[i]
			if h.Meta == nil {
				continue
			}
			idx := findingIndexByCode(h.Findings, "svc:targetport-no-listener")
			if idx < 0 {
				continue
			}
			suspects := metaInt32Slice(h.Meta["targetPortSuspectPorts"])
			if len(suspects) == 0 {
				continue
			}
			allCovered := true
			for _, sp := range suspects {
				if !realPassCoversDenyPort(ports, sp) {
					allCovered = false
					break
				}
			}
			if !allCovered {
				continue
			}
			downgradeTargetPortFinding(&h.Findings[idx])
			sortFindingsBySeverity(h.Findings)
		}
	}
	branches := downstreamBranches(t.Downstream)
	if len(branches) == 0 {
		downgrade(0, len(t.Downstream), anyPassedPorts(passed))
		return
	}
	for _, b := range branches {
		if ports, ok := branchPassedPorts(passed, t.Downstream[b.start].Resource); ok {
			downgrade(b.start, b.end, ports)
		}
	}
}

// passedBackends maps "name\x00namespace" → the set of ports confirmed reachable
// over REAL in-cluster traffic for that backend (a port of 0 marks a pass whose
// port couldn't be pinned). Keyed by name AND namespace so a pass on nsA/svc
// can't silence a prediction scoped to nsB/svc.
func passedBackends(t *Trace) (map[string]map[int32]bool, bool) {
	passed := map[string]map[int32]bool{}
	anyPass := false
	for _, r := range t.Routes {
		if r.Confidence != ConfidenceReal {
			continue
		}
		if r.Outcome != OutcomeVerified && r.Outcome != OutcomeReached {
			continue
		}
		anyPass = true
		name, port := splitTargetPort(r.Target)
		key := name + "\x00" + r.TargetNamespace
		if passed[key] == nil {
			passed[key] = map[int32]bool{}
		}
		passed[key][port] = true
	}
	return passed, anyPass
}

func branchPassedPorts(passed map[string]map[int32]bool, res ResourceRef) (map[int32]bool, bool) {
	p, ok := passed[res.Name+"\x00"+res.Namespace]
	return p, ok
}

// anyPassedPorts unions every passed-port set - used for the single-chain Service
// subject where the one route's pass covers the whole downstream.
func anyPassedPorts(passed map[string]map[int32]bool) map[int32]bool {
	out := map[int32]bool{}
	for _, ports := range passed {
		for p := range ports {
			out[p] = true
		}
	}
	return out
}

// realPassCoversDenyPort reports whether a real pass clears a would-deny scoped
// to denyPort. An all-ports deny (denyPort 0) is cleared by any pass; a
// port-specific deny is cleared only by a pass on that exact port - a pass whose
// port we couldn't pin must not clear a specific-port prediction.
func realPassCoversDenyPort(ports map[int32]bool, denyPort int32) bool {
	if ports == nil {
		return false
	}
	if denyPort == 0 {
		return true
	}
	return ports[denyPort]
}

func splitTargetPort(target string) (string, int32) {
	name := target
	var port int32
	if i := strings.LastIndex(name, ":"); i > 0 {
		if p, err := strconv.ParseInt(name[i+1:], 10, 32); err == nil {
			port = int32(p)
		}
		name = name[:i]
	}
	return name, port
}

func hopDenyPort(meta map[string]any) int32 {
	if meta == nil {
		return 0
	}
	if v, ok := meta["policyDenyPort"]; ok {
		if p, ok := v.(int32); ok {
			return p
		}
	}
	return 0
}

// Diagnosis is the one-finding-that-matters, hoisted from the path so an agent
// (or the UI) doesn't have to hunt through path[].findings for the cause. Every
// field is PROMOTED from a real finding (or a coverage state) - never
// synthesized. CauseCode is the structured code, but ONLY for the trustworthy
// structural fingerprints (missing-ref / svc:*); a pod-state code (e.g.
// "problem:Completed" on a crashloop) would mislabel, so it is omitted and the
// honest Summary prose carries the diagnosis instead. The name says "code" so a
// consumer never mistakes the enum for the plain-English explanation (Summary).
// Diagnosis classes. A FAULT is something wrong in the user's system, promoted
// from a real finding; COVERAGE explains what could or could not be tested. The
// UI's problem list shows faults only - a coverage sentence there duplicated the
// headline, which is generated from the same coverage state.
const (
	DiagnosisClassFault    = "fault"
	DiagnosisClassCoverage = "coverage"
)

type Diagnosis struct {
	// Class is "fault" (default, promoted from a finding) or "coverage".
	Class string `json:"class,omitempty"`
	// Severity of the finding this was promoted from, so a consumer renders the
	// weight the detector assigned rather than assuming the worst.
	Severity  string `json:"severity,omitempty"`
	CauseCode string `json:"causeCode,omitempty"`
	// Route names WHICH route this diagnosis explains, when it is attributable
	// to exactly one. Empty means it describes the resource as a whole.
	//
	// A trace carries one Diagnosis but can carry many routes. Without this the
	// selected-path panel rendered whichever route's cause happened to win, so
	// an operator reading path B was shown path A's culprit under "THIS PATH".
	Route           string       `json:"route,omitempty"`
	Summary         string       `json:"summary"`
	CulpritResource *ResourceRef `json:"culpritResource,omitempty"`
	NextAction      string       `json:"nextAction,omitempty"`
	Command         string       `json:"command,omitempty"`
}

// computeDiagnosis promotes the primary problem on the intended route into a
// Diagnosis, or describes the coverage state when there is no failure to
// explain (reachable only via the proxy, or nothing testable from here).
// Returns nil when every route was verified over real traffic - nothing to
// diagnose. Benign (intentional scale-to-0) routes are NOT treated as a problem
// here; their route already reads amber-benign with its own evidence.
// computeEntryProblems promotes warning+ findings on the DECLARED ENTRY hops
// (upstreams) so a front door that cannot carry traffic is stated where the
// reader looks, instead of living only as a dot inside a graph node. Kept out
// of the Diagnosis ranking on purpose: entries are parallel, so a broken
// sibling must never hijack the headline of a path that works. Anything the
// Diagnosis already names is dropped, so the two surfaces never say it twice.
func computeEntryProblems(t *Trace) []EntryProblem {
	if t == nil || len(t.Upstreams) == 0 {
		return nil
	}
	var out []EntryProblem
	for _, h := range t.Upstreams {
		// A missing-ref on an entry is about a DIFFERENT backendRef: the entry can
		// still serve this Service perfectly while another of its backends is
		// missing. computeVerdict already refuses to let a sibling's break count
		// here; promoting it as "this entry cannot carry traffic" would be the
		// same misattribution one surface up.
		for _, f := range nonMissingRefFindings(h.Findings) {
			if f.Severity != SeverityCritical && f.Severity != SeverityWarning {
				continue
			}
			if isScaleZeroFinding(f) {
				continue
			}
			summary := firstNonEmpty(f.Message, f.Cause)
			if summary == "" {
				continue
			}
			detail := ""
			if f.Cause != "" && f.Cause != summary {
				detail = f.Cause
			}
			// Already stated by the Diagnosis band - printing it twice reads as
			// two problems.
			if t.Diagnosis != nil && t.Diagnosis.Summary == summary {
				continue
			}
			out = append(out, EntryProblem{
				Resource: h.Resource,
				Summary:  summary,
				Detail:   detail,
				Severity: f.Severity,
				Code:     f.Code,
				Action:   f.Action,
				Command:  f.Command,
			})
		}
	}
	return out
}

func computeDiagnosis(t *Trace) *Diagnosis {
	if t == nil {
		return nil
	}
	if f, hopRef, ok := primaryProblemFinding(t); ok {
		d := &Diagnosis{
			Summary: firstNonEmpty(f.Cause, f.Message),
			Command: f.Command,
			// The promoted finding's OWN severity. Without it a consumer has to
			// invent one, and a warning-tier prediction (a would-deny, a soft pod
			// condition) renders as a red critical it never earned.
			Severity: f.Severity,
		}
		if trustworthyCauseCode(f.Code) {
			d.CauseCode = f.Code
		}
		// Culprit precedence: the specific object the finding is about (a named
		// Pod) → the named broken route (a missing backend) → the hop the finding
		// hangs on. Most-specific wins.
		switch {
		case f.Resource != nil:
			d.CulpritResource = f.Resource
		case t.BrokenRoute != nil:
			d.CulpritResource = t.BrokenRoute
		default:
			ref := hopRef
			d.CulpritResource = &ref
		}
		if f.Action != "" {
			d.NextAction = f.Action
		} else {
			d.NextAction = "investigate " + refLabel(d.CulpritResource)
		}
		// Attributable only when exactly one route is broken; with several, the
		// finding cannot be pinned to one of them and claiming otherwise would
		// reproduce the misattribution this field exists to stop.
		if only, ok := soleFailedRoute(t.Routes); ok {
			d.Route = only.Route
		}
		return d
	}
	// A failed route with no classified finding - e.g. a backend that didn't
	// resolve (branchKnownBreak synthesizes evidence from absence, no Finding).
	// Promote the route's own real evidence; do NOT invent a cause code.
	if r, ok := worstNonBenignFailedRoute(t.Routes); ok {
		// A route that was dialled and failed is the strongest fault this page can
		// state. Leaving Severity empty demotes it to the UI's warning default, so
		// confirmed unreachability would render weaker than a predicted one.
		d := &Diagnosis{Route: r.Route, Severity: SeverityCritical, Summary: firstNonEmpty(r.Evidence, "route is unreachable")}
		if t.BrokenRoute != nil {
			d.CulpritResource = t.BrokenRoute
		}
		d.NextAction = "investigate " + firstNonEmpty(r.Route, r.Target)
		return d
	}
	if t.Coverage == nil {
		return nil
	}
	// Reachable only through the apiserver proxy. Kept on the wire for agents -
	// one machine-readable "what next" - but tagged COVERAGE, because it is a
	// statement about what could be tested, not a fault in the user's system.
	// The UI's problem list renders faults only: the headline already says this
	// ("Reached via API server - not live traffic"), the viewing strip says it
	// for the selected vantage, and the next-step block offers the in-cluster
	// run, so a fourth copy read as a separate problem.
	if t.Coverage.Tested > 0 && !anyRealPass(t.Routes) && anyIndirectReach(t.Routes) {
		return &Diagnosis{
			Class:      DiagnosisClassCoverage,
			Summary:    "reachable via API server - the real-traffic path wasn't confirmed from here",
			NextAction: "run the in-cluster reachability test to confirm the real path",
		}
	}
	// Probing happened but nothing could actually be tested from this vantage. Lead
	// with the REAL reason there was nothing to test - a generic "couldn't test"
	// reads like a tool failure when the actual cause is actionable. Only when a probe
	// ACTUALLY ran - a static glance (the drawer) hasn't tried anything, so it gets no
	// "couldn't test" diagnosis.
	if t.Coverage.Tested == 0 && t.Coverage.Skipped > 0 && anyProbeRan(t) {
		// Nothing is RUNNING: the dominant fact is the workload, not the vantage.
		if hopsHaveScaleZero(t.Downstream) {
			return &Diagnosis{
				Class:      DiagnosisClassCoverage,
				Summary:    "no running pods - the backing workload is scaled to 0, so there's nothing to reach",
				NextAction: "scale the workload up to test it (e.g. kubectl scale --replicas=1), or leave it if it's intentionally idle",
			}
		}
		// A skip we can act on DIRECTLY (HTTPS / non-HTTP that the proxy can't verify):
		// name the reason and hand over the exact command, not a vague in-cluster nudge.
		// The per-path reasons are the whole answer to "why couldn't you test
		// this" and they were being discarded unless a skip happened to carry a
		// command - leaving a generic sentence that restated the headline and
		// told the reader nothing they could act on.
		reasons := distinctSkipReasons(t.NotTested)
		switch {
		case len(reasons) == 1:
			// No "couldn't test from here -" prefix: the headline says that much.
			// The reason is the only part that adds anything.
			d := &Diagnosis{Class: DiagnosisClassCoverage, Summary: reasons[0]}
			d.NextAction = firstNonEmpty(firstSkipCommand(t.NotTested), "run the in-cluster reachability test to confirm the real path")
			return d
		case len(reasons) > 1:
			return &Diagnosis{
				Class:      DiagnosisClassCoverage,
				Summary:    fmt.Sprintf("couldn't test any of the %d declared paths from here, for %d different reasons - select a path to see its own", len(t.NotTested), len(reasons)),
				NextAction: "run the in-cluster reachability test to confirm the real path",
			}
		}
		return &Diagnosis{
			Class:      DiagnosisClassCoverage,
			Summary:    "couldn't actively test any route from here",
			NextAction: "run the in-cluster reachability test to confirm the real path",
		}
	}
	return nil
}

// realPassByBranch maps each downstream hop index to whether its branch's route
// was confirmed reachable over REAL traffic (direct dial / in-cluster data path).
// Used to demote a contradicted static would-deny prediction from the diagnosis.
func realPassByBranch(t *Trace) map[int]map[int32]bool {
	out := map[int]map[int32]bool{}
	passed, anyPass := passedBackends(t)
	if !anyPass {
		return out
	}
	branches := downstreamBranches(t.Downstream)
	if len(branches) == 0 {
		// Single chain (Service subject): the one route covers the whole downstream.
		ports := anyPassedPorts(passed)
		for i := range t.Downstream {
			out[i] = ports
		}
		return out
	}
	for _, b := range branches {
		if ports, ok := branchPassedPorts(passed, t.Downstream[b.start].Resource); ok {
			for i := b.start; i < b.end && i < len(t.Downstream); i++ {
				out[i] = ports
			}
		}
	}
	return out
}

// primaryProblemFinding returns the worst-severity critical/warning finding on
// the intended route, with the hop resource it hangs on. Anchors on the break
// hop (BrokenAt) and its downstream when one is set, else scans the whole
// downstream (a missing-ref break is expressed on the entry hop). Benign
// scale-to-0 findings are skipped - they are not a problem to diagnose.
func primaryProblemFinding(t *Trace) (Finding, ResourceRef, bool) {
	type cand struct {
		f     Finding
		hop   ResourceRef
		depth int
	}
	var cands []cand
	passed := realPassByBranch(t)
	gather := func(hops []Hop, base int) {
		for i, h := range hops {
			for _, f := range h.Findings {
				if isScaleZeroFinding(f) {
					continue
				}
				// A static would-deny is a PREDICTION, not ground truth. Once real
				// traffic actually passed this branch ON THE DENIED PORT (direct dial
				// or in-cluster data path), the prediction is contradicted and must not
				// lead the diagnosis - demote it out of the candidates. A pass on a
				// different port leaves the prediction standing.
				if f.Code == codePolicyWouldDeny && realPassCoversDenyPort(passed[base+i], hopDenyPort(h.Meta)) {
					continue
				}
				if f.Severity == SeverityCritical || f.Severity == SeverityWarning {
					cands = append(cands, cand{f, h.Resource, base + i})
				}
			}
		}
	}
	if t.BrokenAt >= 0 && t.BrokenAt < len(t.Downstream) {
		// For a multi-backend entry the flat downstream concatenates independent
		// branches. Bound the gather to the branch that actually contains BrokenAt
		// so a sibling route's finding can't be promoted as this route's culprit.
		if span, ok := branchContaining(t.Downstream, t.BrokenAt); ok {
			gather(t.Downstream[span.start:span.end], span.start)
		} else {
			gather(t.Downstream[t.BrokenAt:], t.BrokenAt)
		}
	}
	if len(cands) == 0 {
		gather(t.Downstream, 0)
	}
	if len(cands) == 0 {
		return Finding{}, ResourceRef{}, false
	}
	// Rank: worst severity first; then prefer a finding that names a specific
	// object (a Pod-level root cause) over a hop-level SYMPTOM - a crashloop pod
	// is the real culprit, the Service's "no ready endpoints" is just its shadow;
	// then the deepest hop (root cause sits downstream of the symptom); then code
	// for a stable tiebreak.
	sort.SliceStable(cands, func(i, j int) bool {
		ri, rj := severityRank(cands[i].f.Severity), severityRank(cands[j].f.Severity)
		if ri != rj {
			return ri > rj
		}
		si, sj := cands[i].f.Resource != nil, cands[j].f.Resource != nil
		if si != sj {
			return si
		}
		if cands[i].depth != cands[j].depth {
			return cands[i].depth > cands[j].depth
		}
		return cands[i].f.Code < cands[j].f.Code
	})
	return cands[0].f, cands[0].hop, true
}

// trustworthyCauseCode reports whether a finding code is a structural fingerprint
// safe to surface as the Diagnosis.CauseCode. Only the network-shape detectors
// qualify (missing-ref, svc:* - scaled-to-zero, no-ready-endpoints,
// targetport-no-listener, …). A pod-state issue code ("problem:Completed",
// "problem:CrashLoopBackOff") reflects transient runtime state and would
// mislabel the cause, so it is excluded - the honest prose Summary stands alone.
func trustworthyCauseCode(code string) bool {
	if code == "" {
		return false
	}
	return IsMissingRefCode(code) || strings.HasPrefix(code, "svc:")
}

// isScaleZeroFinding matches the intentional scale-to-0 marker in either form
// (the raw detection fingerprint, or the "<source>:<reason>" code that issue
// grouping leaves) - mirrors hopsHaveScaleZero.
func isScaleZeroFinding(f Finding) bool {
	if f.Severity == SeverityCritical {
		return false
	}
	return f.Code == k8s.ScaledToZeroFingerprint || strings.HasSuffix(f.Code, k8s.ScaledToZeroReason)
}

func worstNonBenignFailedRoute(routes []RouteResult) (RouteResult, bool) {
	var best RouteResult
	found := false
	for _, r := range routes {
		if r.Benign {
			continue
		}
		switch r.Outcome {
		case OutcomeUnreachable:
			return r, true // worst failure - return immediately
		case OutcomeServerError:
			if !found {
				best, found = r, true
			}
		}
	}
	return best, found
}

// allPassesIndirect reports whether every reachable (verified/reached) route was
// reached ONLY via the apiserver proxy - at least one such pass exists and none
// over the real-traffic path. When true the multi-route headline must say
// proxy-only, mirroring singleRouteHeadline, instead of bare "All N reachable".
func allPassesIndirect(routes []RouteResult) bool {
	return !anyRealPass(routes) && anyIndirectReach(routes)
}

func anyIndirectReach(routes []RouteResult) bool {
	for _, r := range routes {
		if r.Confidence == ConfidenceIndirect && (r.Outcome == OutcomeVerified || r.Outcome == OutcomeReached) {
			return true
		}
	}
	return false
}

func refLabel(r *ResourceRef) string {
	if r == nil {
		return "the path"
	}
	name := r.Name
	if r.Namespace != "" {
		name = r.Namespace + "/" + r.Name
	}
	if r.Kind != "" {
		return r.Kind + " " + name
	}
	return name
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// CoverageVerdict is the shipped verdict, reconciled with the coverage projection
// so it cannot contradict the headline or routes (bug B3). It reuses the internal
// verdict for broken, degraded, and unknown, which are already honest, and only
// corrects the one over-claim: a healthy verdict whose only verifying evidence is
// the apiserver proxy (indirect) is not a confident green (#1a/B3), because the
// proxy reached a pod but never confirmed the real-traffic path. That downgrades
// to unknown, and the headline and routes explain why. It is computed once over
// the internal verdict and coverage, then stored back into t.Verdict (in
// BuildTraceWithOptions and ApplyInClusterResults), so every surface (REST, UI,
// MCP) reads one answer. It is idempotent: re-running it on the stored value
// returns the same verdict.
func CoverageVerdict(t *Trace) string {
	if t == nil {
		return VerdictUnknown
	}
	// broken / degraded / unknown from the internal verdict are already honest,
	// EXCEPT: an intentional scale-to-0 reads as broken via the probe (no ready
	// endpoints), but it's deliberate dormancy, not an outage - soften
	// broken → degraded when every failed route is benign.
	if t.Verdict != VerdictHealthy {
		// Soften broken → degraded ONLY when the broken-ness is attributable to the
		// benign routes. allFailuresBenign inspects route outcomes alone, so an
		// unrelated static critical (e.g. no resolvable IngressClass) alongside one
		// benign scale-to-0 route would otherwise be under-reported as amber.
		if t.Verdict == VerdictBroken && allFailuresBenign(t.Routes) && !hasNonBenignCriticalFinding(t) {
			return VerdictDegraded
		}
		return t.Verdict
	}
	c := t.Coverage
	if c == nil {
		// No active probing was attempted (e.g. probe=false, or a config-only
		// shape like ExternalName) - the static config assessment stands.
		return VerdictHealthy
	}
	if c.Tested == 0 {
		// Probing happened but nothing was actually tested (every intended route
		// was skipped / unreachable from here) - not a confident green; the
		// "couldn't test any route" headline must not sit beside "healthy".
		return VerdictUnknown
	}
	if !anyRealPass(t.Routes) {
		// Every reachable route was reached ONLY via the apiserver proxy - the
		// real-traffic path was never confirmed (#1a/B3).
		return VerdictUnknown
	}
	if c.Failed > 0 {
		// A real pass exists, but other ports/routes failed. The internal verdict
		// can read healthy while a dead secondary port leaves Coverage.Failed>0 and
		// the headline says "1 of 2 ports reachable · 1 unreachable" - green here
		// would contradict the headline (the B3 contradiction this guard prevents).
		if c.Passed == 0 {
			return VerdictBroken
		}
		return VerdictDegraded
	}
	if c.Skipped > 0 {
		// A real pass exists AND nothing failed, but some intended routes were never
		// actually tested (budget exhausted, vantage-skipped from here, or otherwise
		// couldn't-test). recountCoverage already excludes by-design BENIGN skips from
		// Coverage.Skipped (SkipClassBenign is dropped), so a positive count is a
		// genuine coverage gap - a non-benign route whose real path was never
		// confirmed. "healthy" beside a "· N not tested" headline would over-claim, so
		// the honest verdict is unknown: partial coverage, not a confident green.
		return VerdictUnknown
	}
	return VerdictHealthy
}

// anyRealPass reports whether at least one intended route was reached over the
// REAL-traffic path (direct dial / in-cluster data path) - not merely via the
// apiserver proxy. Indirect-only evidence never earns a confident healthy.
// allFailuresBenign reports whether there is at least one failed route and EVERY
// failed (unreachable/server-error) route is benign (intentional scale-to-0).
func allFailuresBenign(routes []RouteResult) bool {
	failed := 0
	for _, r := range routes {
		if r.Outcome == OutcomeUnreachable || r.Outcome == OutcomeServerError {
			failed++
			if !r.Benign {
				return false
			}
		}
	}
	return failed > 0
}

// hasNonBenignCriticalFinding reports whether any hop carries a critical finding
// that isn't a benign scale-to-0 marker - i.e. a real structural break that must
// not be softened away just because the failed ROUTES happen to be benign.
func hasNonBenignCriticalFinding(t *Trace) bool {
	for _, hop := range t.Downstream {
		if hopHasNonBenignCritical(hop.Findings) {
			return true
		}
	}
	// An upstream missing-ref critical is about a SIBLING backend route, not the
	// path into this subject (which exists as a backend) - scope it out exactly as
	// computeVerdict's nonMissingRefFindings does, so a broken sibling doesn't block
	// the broken→degraded softening for a benign scale-to-0 here.
	for _, hop := range t.Upstreams {
		if hopHasNonBenignCritical(nonMissingRefFindings(hop.Findings)) {
			return true
		}
	}
	return false
}

func hopHasNonBenignCritical(fs []Finding) bool {
	for _, f := range fs {
		if f.Severity == SeverityCritical && !isScaleZeroFinding(f) {
			return true
		}
	}
	return false
}

func anyRealPass(routes []RouteResult) bool {
	for _, r := range routes {
		if r.Confidence != ConfidenceReal {
			continue
		}
		if r.Outcome == OutcomeVerified || r.Outcome == OutcomeReached {
			return true
		}
	}
	return false
}

// buildRoutes derives one RouteResult per intended route. For a multi-backend
// entry it walks downstreamBranches; for a Service/single-chain subject it
// yields a single route over the whole downstream. Returns the routes plus the
// branches that were genuinely un-probed with no break (surfaced as NotTested)
// - a declared route must never silently vanish.
// externalNameHop returns the alias-host hop of an ExternalName Service path (a
// DNS CNAME to a host outside the cluster), or nil. That hop carries the real
// reachability probes; the Service hop above it has no ClusterIP/ports to dial.
func externalNameHop(hops []Hop) *Hop {
	for i := range hops {
		if hops[i].Resource.Kind == "ExternalName" {
			return &hops[i]
		}
	}
	return nil
}

// anyProbeRan reports whether any active probe actually executed on this trace. A
// STATIC (un-probed) trace still produces skipped routes (buildRoutes marks them
// "not actively tested"), which must NOT read as "Couldn't actively test any route
// from here" - that wording is for a trace that DID probe and found everything
// unprobeable from this vantage. No probes at all = "not tested yet", not "couldn't".
func anyProbeRan(t *Trace) bool {
	for _, h := range t.Downstream {
		if len(h.Probes) > 0 {
			return true
		}
	}
	for _, h := range t.Upstreams {
		if len(h.Probes) > 0 {
			return true
		}
	}
	return false
}

// probesHaveInClusterVantage reports whether any non-skipped probe actually ran
// from inside the cluster - the only vantage that proves real in-cluster reachability.
func probesHaveInClusterVantage(probes []probe.Result) bool {
	for _, p := range probes {
		if !p.Skipped && p.Vantage == probe.VantageInCluster {
			return true
		}
	}
	return false
}

func buildRoutes(t *Trace) ([]RouteResult, []RouteSkip) {
	if len(t.Downstream) == 0 {
		return nil, nil
	}
	entry := t.Downstream[0]
	branches := downstreamBranches(t.Downstream)
	var out []RouteResult
	var unprobed []RouteSkip

	if len(branches) == 0 {
		// ExternalName Service: the alias-host hop (below the Service) carries the
		// real reachability test - DNS-resolve + HTTP-reach the external host. The
		// Service hop itself has no ClusterIP/ports to dial, so its OWN probes are
		// empty; the route's outcome IS the external host's reachability.
		if ext := externalNameHop(t.Downstream); ext != nil && len(ext.Probes) > 0 {
			if r, ok := routeFromProbes(subjectRouteLabel(t.Subject, entry), ext.Resource.Name, ext.Probes, false); ok {
				// A laptop dials the external host from Radar's OWN network - that is
				// NOT proof of real in-cluster reachability (split-horizon DNS, egress
				// rules, and routing can all differ inside the cluster). Only an
				// in-cluster vantage earns ConfidenceReal; otherwise it's indirect, so
				// the verdict reads "reached" with a caveat, never a green "verified".
				if r.Confidence == ConfidenceReal && !probesHaveInClusterVantage(ext.Probes) {
					r.Confidence = ConfidenceIndirect
				}
				return []RouteResult{r}, nil
			}
		}
		// Single chain (Service/Pod subject). The Service hop's OWN probes are the
		// intended-route test, one route per Service port; the Pods-hop probes sit
		// behind the Service, so they're localization, never a separate route.
		podLoc := factsFromProbes(downstreamProbes(t.Downstream[1:]))
		routes := routesByPort(subjectRouteLabel(t.Subject, entry), entry.Resource.Name, subjectTarget(entry), entry.Probes, nil, podLoc, hopPorts(entry), traceHasFrontDoor(t), true)
		setTargetNamespace(routes, entry.Resource.Namespace)
		markBenignScaleZero(routes, entry)
		attachInClusterRequest(routes, "", "", entry.Config)
		return routes, nil
	}

	for _, b := range branches {
		backend := t.Downstream[b.start]
		// A Gateway's attached route is a parallel ENTRY path, traced as its own
		// subject - NOT a resolvable Service backend of this Gateway. It carries no
		// Config/probes here, so condemning it as an unreachable backend (the
		// Config==nil arm of branchKnownBreak) would false-condemn every healthy
		// attached route. Exclude it from routes; its own skipped probe surfaces as
		// a NotTested coverage gap instead.
		if strings.HasPrefix(backend.Edge, "Gateway->") {
			continue
		}
		// ONE RouteResult PER declared host+path rule (never joined): each rule is
		// its own intended route with its own fold key, so an in-cluster result for
		// /web can never vouch for a sibling /admin that shares the backend (the
		// InClusterResultKey invariant). Fallback when no declared rule is readable
		// (nil entry Config, or rules that don't name this backend): the backend
		// name, scoped to every port the entry declares for it.
		rules := ruleRoutesForBackend(entry, backend.Resource.Name, backend.Resource.Namespace, backend.Config)
		if len(rules) == 0 {
			rules = []ruleRoute{{label: backend.Resource.Name, ports: backendDeclaredPorts(entry, backend.Resource.Name, backend.Resource.Namespace, backend.Config)}}
		}
		// A drained (explicit weight-0) backend carries no traffic by design - it
		// was traced informationally and never probed. Record it as a benign skip
		// (no coverage lost) instead of a coverage gap or a failed route.
		if isDrainedBackendHop(backend) {
			for _, rr := range rules {
				unprobed = append(unprobed, RouteSkip{
					Route:       rr.label,
					Reason:      "backend is weighted to 0 (drained / canary-cutover) - serves no traffic by design",
					ReasonClass: SkipClassBenign,
				})
			}
			continue
		}
		target := backend.Resource.Name
		if backend.Config != nil && len(backend.Config.Ports) > 0 {
			target = fmt.Sprintf("%s:%d", backend.Resource.Name, backend.Config.Ports[0].Port)
		}
		// A KNOWN static break (missing backend, critical finding) is a FAILED
		// route REGARDLESS of whether the shared front door dialed OK - a working
		// front door doesn't make a route to a non-existent backend reachable.
		// Every rule pointing at the broken backend is genuinely broken.
		if ev, basis, broken := branchKnownBreak(t, entry, b); broken {
			for _, rr := range rules {
				out = append(out, RouteResult{
					Route:           rr.label,
					Target:          target,
					TargetNamespace: backend.Resource.Namespace,
					Outcome:         OutcomeUnreachable,
					Evidence:        ev,
					Basis:           basis,
				})
			}
			continue
		}
		// Front-door (entry) host dials are shared, port-agnostic context for this
		// backend's routes (scoped to the rule's own host); the backend Service
		// hop's probes carry the per-port outcome; the Pods hop sits behind the
		// Service → localization. Scope to the port(s) THIS rule declares for the
		// backend - empty/unresolvable scope means all probed ports.
		end := b.end
		if end > len(t.Downstream) {
			end = len(t.Downstream)
		}
		podLoc := factsFromProbes(downstreamProbes(t.Downstream[b.start+1 : end]))
		// Scope front-door dials to the rule's own declared host only on a
		// multi-host entry, where sibling hosts' dials would cross-contaminate
		// this rule's outcome. A single-host entry's dials all belong to its one
		// host, and the fallback rule (no readable declared rule) can't be
		// scoped - both pass "" so every entry probe applies.
		multiHost := entry.Config != nil && len(entry.Config.Hostnames) > 1
		for _, rr := range rules {
			scopeHost := ""
			if multiHost {
				scopeHost = rr.host
			}
			shared := entryProbesForHost(entry, scopeHost)
			outcomeProbes := append(append([]probe.Result{}, shared...), backend.Probes...)
			routes := routesByPort(rr.label, backend.Resource.Name, target, outcomeProbes, rr.ports, podLoc, hopPorts(backend), true, false)
			if len(routes) > 0 {
				setTargetNamespace(routes, backend.Resource.Namespace)
				markBenignScaleZero(routes, backend)
				// Each route carries ITS OWN rule's host+path - the request must test
				// the exact thing this route declares, never a sibling's.
				attachInClusterRequest(routes, rr.host, rr.path, backend.Config)
				out = append(out, routes...)
				continue
			}
			// Not a known break and no probe result → a genuine coverage gap.
			unprobed = append(unprobed, RouteSkip{Route: rr.label, Reason: "route not actively tested", ReasonClass: SkipClassCoverage})
		}
	}
	// A Gateway subject's attached routes were all skipped above (each is its own
	// entry path). Surface the Gateway's OWN front-door reachability as the route
	// so its probe evidence isn't dropped and coverage doesn't read empty.
	if entry.Resource.Kind == "Gateway" && len(out) == 0 {
		gw := routesByPort(subjectRouteLabel(t.Subject, entry), entry.Resource.Name, subjectTarget(entry), entry.Probes, nil, nil, hopPorts(entry), false, false)
		setTargetNamespace(gw, entry.Resource.Namespace)
		out = append(out, gw...)
	}
	return out, unprobed
}

// setTargetNamespace stamps the backend Service namespace on each route so the
// in-cluster probe can build an FQDN dial for cross-namespace backends.
func setTargetNamespace(routes []RouteResult, namespace string) {
	for i := range routes {
		routes[i].TargetNamespace = namespace
	}
}

// markBenignScaleZero normalizes an unreachable or vantage-only route when a
// contributing hop carries the intentional-scale-to-0 finding. The backend is
// authoritatively absent by design, so a not-tested transport candidate is also
// factually unreachable and must not become a runnable incident probe.
func markBenignScaleZero(routes []RouteResult, hops ...Hop) {
	if !hopsHaveScaleZero(hops) {
		return
	}
	for i := range routes {
		if routes[i].Outcome == OutcomeUnreachable || routes[i].Outcome == OutcomeNotTested {
			routes[i].Outcome = OutcomeUnreachable
			routes[i].Benign = true
			routes[i].Evidence = "no running backends (scaled to 0)"
		}
	}
}

// markBenignServiceSkips keeps the raw per-hop skip rows aligned with a Service
// route already proven dormant by scale-to-zero. Port scoping is load-bearing:
// an unrelated untested port must remain a real coverage gap.
func markBenignServiceSkips(t *Trace) {
	if t.Subject.Kind != "Service" {
		return
	}
	benignPorts := map[string]bool{}
	for _, r := range t.Routes {
		if r.Benign {
			if port := portKey(r.Target); port != "" {
				benignPorts[port] = true
			}
		}
	}
	for i := range t.NotTested {
		if benignPorts[portKey(t.NotTested[i].Route)] {
			t.NotTested[i].ReasonClass = SkipClassBenign
			t.NotTested[i].Reason = "protocol reachability was not tested because the Service has no running backends (scaled to 0)"
			t.NotTested[i].Command = ""
		}
	}
}

func hopsHaveScaleZero(hops []Hop) bool {
	for _, h := range hops {
		for _, f := range h.Findings {
			// The detection carries a stable Fingerprint, but issue grouping
			// (RelatedIssues) does not preserve it on the grouped representative,
			// so the trace usually sees the "<source>:<reason>" code instead.
			// Match either form against the shared constants.
			if isScaleZeroFinding(f) {
				return true
			}
		}
	}
	return false
}

// routesByPort emits one RouteResult per (backend, port), the unit of coverage:
// a port is part of the intended-route identity (svc:80 = the app, svc:9090 =
// metrics - different listeners). Port-0 probes (host-level DNS / front-door
// dials) are shared context applied to every port. scope, when non-empty,
// restricts output to those ports (an Ingress route targets a declared port);
// empty scope means every probed port is an intended route (a Service subject's
// own ports). fallbackTarget labels the host-level route when no per-port probe
// ran. extraLoc (the behind-the-gate pod facts) is appended to every route.
// httpProbablePort reports whether the declared Service port speaks HTTP per
// the SAME predicate the prober used to decide what to dial. Fail-closed: an
// unknown port must not become a guessed HTTP probe Job. EVERY declared entry
// for the number must pass - Kubernetes permits TCP and UDP ports with the
// same number, and first-match would let a UDP :80 acquire an HTTP Job whenever its
// TCP sibling happened to be declared first.
func httpProbablePort(ports []PortMap, port int32) bool {
	matched := false
	for _, pm := range ports {
		if pm.Port != port {
			continue
		}
		if !httpProbablePortMap(pm) {
			return false
		}
		matched = true
	}
	return matched
}

// httpProbablePortMap is the one protocol boundary for anything that would
// SEND an HTTP request at a declared port. An explicit UDP/SCTP protocol
// refuses outright - an unnamed UDP :80 must never acquire an HTTP Job the
// prober itself declined to send. Empty protocol is the Kubernetes default
// (TCP).
func httpProbablePortMap(pm PortMap) bool {
	switch strings.ToUpper(strings.TrimSpace(pm.Protocol)) {
	case "", "TCP":
	default:
		return false
	}
	return isHTTPProbablePort(pm.Name, pm.AppProtocol, pm.Port)
}

func routesByPort(routeID, backendName, fallbackTarget string, probes []probe.Result, scope []int32, extraLoc []ProbeFact, ports []PortMap, backendScoped, materializeVantageSkips bool) []RouteResult {
	inScope := func(port int32) bool {
		if len(scope) == 0 {
			return true
		}
		for _, s := range scope {
			if s == port {
				return true
			}
		}
		return false
	}
	var shared []probe.Result
	byPort := map[int32][]probe.Result{}
	var order []int32
	for _, p := range probes {
		if p.Port == 0 {
			shared = append(shared, p)
			continue
		}
		if !inScope(p.Port) {
			continue
		}
		if _, ok := byPort[p.Port]; !ok {
			order = append(order, p.Port)
		}
		byPort[p.Port] = append(byPort[p.Port], p)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	emit := func(rid, target string, ps []probe.Result) (RouteResult, bool) {
		r, ok := routeFromProbes(rid, target, ps, backendScoped)
		if !ok {
			if materializeVantageSkips {
				for _, p := range ps {
					if p.Skipped && skipClassOf(p) == SkipClassVantage {
						return RouteResult{
							Route: rid, Target: target, Outcome: OutcomeNotTested,
							Evidence: p.Reason, Command: p.Command,
						}, true
					}
				}
			}
			return r, false
		}
		r.Localization = dedupeFacts(append(r.Localization, extraLoc...))
		return r, true
	}

	if len(order) == 0 {
		// No per-port probe ran - a single host-level route (DNS-only / front door).
		if r, ok := emit(routeID, fallbackTarget, shared); ok {
			return []RouteResult{r}
		}
		return nil
	}
	// A host-level (Port==0) front-door probe is port-agnostic CONTEXT: its "/" 2xx
	// says the front door is reachable, NOT that THIS backend port's declared
	// path/protocol is. So when combined with a specific port's probes it must not,
	// on its own, VERIFY that port - cap a front-door HTTP 2xx at "reached" here, so
	// a port whose own probes all skipped reads "reached, route not verified" instead
	// of a false green "verified". The port's OWN healthy probe still wins to verified.
	demotedShared := demoteSharedFrontDoor(shared)
	multi := len(order) > 1
	var out []RouteResult
	for _, port := range order {
		ps := append(append([]probe.Result{}, demotedShared...), byPort[port]...)
		target := fmt.Sprintf("%s:%d", backendName, port)
		rid := routeID
		if multi {
			rid = target // distinguish multiple ports of the same backend
			if routeID != "" && routeID != backendName {
				// A host+path rule label with multiple declared ports: keep the rule
				// identity in the route ID - the bare target would collide with a
				// sibling rule's same-port route and break the per-route fold-key
				// invariant (InClusterResultKey).
				rid = routeID + " · " + target
			}
		}
		if r, ok := emit(rid, target, ps); ok {
			out = append(out, r)
			continue
		}
		// Every probe for this declared port SKIPPED (apiserver timeout, budget
		// exhaustion, vantage). Dropping the route erased the declared test
		// candidate with it - InClusterRequest only rides on routes - so the
		// offered in-cluster recovery could not run exactly when it was the
		// recovery. Preserve the candidate as an honest not-tested route (the
		// skip rows keep the reasons; recountCoverage absorbs them per host).
		// ONLY for ports the prober itself considers HTTP: a deliberately
		// skipped Redis/gRPC/UDP port must not become a guessed HTTP Job that
		// fabricates identity/mTLS evidence.
		if len(ps) > 0 && httpProbablePort(ports, port) {
			// Carry the skip's reason as the route's evidence - the scenario tab
			// must still say WHY this is untested once the raw skip rows are
			// absorbed into the route (buildNotTested drops them structurally).
			evidence := ""
			for _, sp := range ps {
				if sp.Reason != "" {
					evidence = sp.Reason
					break
				}
			}
			out = append(out, RouteResult{Route: rid, Target: target, Outcome: OutcomeNotTested, Evidence: evidence})
		}
	}
	return out
}

// demoteSharedFrontDoor caps host-level (port-agnostic) front-door probes so they
// can't, on their own, VERIFY a specific backend port route: an HTTP 2xx to the
// front door's "/" is reachability CONTEXT, not proof that this port's declared
// path/protocol serves. A healthy front-door HTTP probe is demoted to the "reached"
// tone (worstOutcome then yields at most OutcomeReached for a port whose own probes
// didn't independently verify); the port's OWN healthy probe still wins to verified.
// Non-HTTP and failing front-door probes are passed through unchanged.
func demoteSharedFrontDoor(shared []probe.Result) []probe.Result {
	if len(shared) == 0 {
		return shared
	}
	out := make([]probe.Result, len(shared))
	for i, p := range shared {
		if p.OK && !p.Skipped && p.Layer == probe.LayerHTTP && p.Tone == probe.ToneHealthy {
			p.Tone = probe.ToneReached
		}
		out[i] = p
	}
	return out
}

// downstreamProbes flattens the probes across a set of hops.
func downstreamProbes(hops []Hop) []probe.Result {
	var out []probe.Result
	for _, h := range hops {
		out = append(out, h.Probes...)
	}
	return out
}

// dedupeFacts removes exact-duplicate Localization facts (same layer, target,
// ok, tone, detail, path) - the pod-fanout and per-port projections can append
// the identical apiserver fact twice. It dedupes on EVERY field rather than
// normalizing the target, so a pod-level confirmation is never collapsed into a
// service-level one (that would hide which pod answered).
func dedupeFacts(facts []ProbeFact) []ProbeFact {
	if len(facts) <= 1 {
		return facts
	}
	seen := make(map[string]bool, len(facts))
	out := facts[:0:0]
	for _, f := range facts {
		key := f.Layer + "\x00" + f.Path + "\x00" + f.Target + "\x00" + strconv.FormatBool(f.OK) + "\x00" + f.Tone + "\x00" + f.Detail
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}

// factsFromProbes projects non-skipped probes to behind-the-gate Localization facts.
func factsFromProbes(probes []probe.Result) []ProbeFact {
	var out []ProbeFact
	for _, p := range probes {
		if p.Skipped {
			continue
		}
		out = append(out, toFact(p))
	}
	return out
}

// backendDeclaredPorts returns the resolved Service ports the entry's rules
// declare for a backend (an Ingress/route backendRef names a specific port).
// Empty when no rule names a resolvable port - the caller then covers all
// probed ports.
func backendDeclaredPorts(entry Hop, backendName, backendNS string, cfg *HopConfig) []int32 {
	if entry.Config == nil || backendName == "" {
		return nil
	}
	seen := map[int32]bool{}
	var out []int32
	for _, rule := range entry.Config.Rules {
		for _, b := range rule.Backends {
			if !backendRefMatches(b, backendName, entry.Resource.Namespace, backendNS) {
				continue
			}
			if p := resolveBackendPort(b.Port, cfg); p > 0 && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// resolveBackendPort turns a backendRef port (numeric or a named Service port)
// into a port number, using the backend Service's port map for named ports.
func resolveBackendPort(portStr string, cfg *HopConfig) int32 {
	if portStr == "" {
		return 0
	}
	if n, err := strconv.ParseInt(portStr, 10, 32); err == nil {
		return int32(n)
	}
	if cfg != nil {
		for _, pm := range cfg.Ports {
			if pm.Name == portStr {
				return pm.Port
			}
		}
	}
	return 0
}

// branchKnownBreak reports whether a branch that produced no probe result is
// nonetheless a KNOWN static break, with evidence for the failed route. Signals:
// a missing-backend ref on the entry naming this backend (reuses the shared
// missingRefCodePrefix - no prose-sniffing), a critical finding on the branch,
// or a backend Service that didn't resolve (no Config).
func branchKnownBreak(t *Trace, entry Hop, b branchSpan) (evidence, basis string, broken bool) {
	backend := t.Downstream[b.start]
	for _, f := range entry.Findings {
		if strings.HasPrefix(f.Code, missingRefCodePrefix) && backend.Resource.Name != "" && missingRefMatchesBackend(f.Message, backend.Resource.Name, backend.Resource.Namespace) {
			return f.Message, BasisDeclared, true
		}
	}
	for i := b.start; i < b.end && i < len(t.Downstream); i++ {
		// A backend that still has ready endpoints is reachable - a per-pod crash
		// among serving replicas is a degradation, not a known break. Skip it so a
		// 1-of-2-ready Service isn't condemned unreachable (mirrors hopReachSeverity).
		if hopHasReadyEndpoints(t.Downstream[i]) {
			continue
		}
		for _, f := range t.Downstream[i].Findings {
			if f.Severity == SeverityCritical {
				return f.Message, BasisState, true
			}
		}
	}
	if backend.Config == nil {
		// A backend we merely couldn't READ (RBAC-redacted cross-namespace, or a
		// transient API failure marked endpointSource=unknown) has no Config but is
		// NOT a confirmed break. Condemning it as unreachable would false-condemn a
		// route the caller can't see - fall through to the unprobed/NotTested path
		// so it reads as not-tested/unknown, matching the verdict layer.
		if src, _ := backend.Meta["endpointSource"].(string); src == "unknown" {
			return "", "", false
		}
		for _, f := range backend.Findings {
			if f.Code == "rbac:cross-namespace-redacted" {
				return "", "", false
			}
		}
		return fmt.Sprintf("backend %s %s could not be resolved", backend.Resource.Kind, backend.Resource.Name), BasisDeclared, true
	}
	return "", "", false
}

// missingRefMatchesBackend reports whether a missing-ref finding message names
// THIS backend (by name and, when the message carries a namespace qualifier,
// namespace). Gateway-API messages read "...references Service "api" in namespace
// "nsA" which does not exist..."; without the namespace check a missing nsA/api
// would condemn a healthy same-named nsB/api sibling.
func missingRefMatchesBackend(msg, name, namespace string) bool {
	if !containsResourceName(msg, name) {
		return false
	}
	if i := strings.Index(msg, "in namespace "); i >= 0 && namespace != "" {
		return containsResourceName(msg[i:], namespace)
	}
	return true
}

// MissingRefNamesSubject reports whether a missing-ref finding message names the
// given subject (by name and, when the message qualifies it, namespace). Exported
// so the MCP projection can keep a subject-relevant missing-ref (e.g. an upstream
// Ingress referencing the subject Service on a port it doesn't expose) while still
// dropping a sibling-route missing-ref that only condemns the upstream's other
// backends.
func MissingRefNamesSubject(msg, name, namespace string) bool {
	return missingRefMatchesBackend(msg, name, namespace)
}

// containsResourceName reports whether name appears in s as a whole Kubernetes
// resource name - bounded by characters that can't be part of a DNS-1123 name,
// so backend "api" does NOT match inside a missing-ref message about "api-v2" or
// "my-api". A raw substring match would condemn a healthy same-prefixed sibling.
func containsResourceName(s, name string) bool {
	if name == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(s[from:], name)
		if i < 0 {
			return false
		}
		i += from
		var before, after byte = ' ', ' '
		if i > 0 {
			before = s[i-1]
		}
		if i+len(name) < len(s) {
			after = s[i+len(name)]
		}
		if !isNameByte(before) && !isNameByte(after) {
			return true
		}
		from = i + 1
	}
}

func isNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '.'
}

// routeFromProbes computes a route's outcome from its probe set. Returns ok=false
// when no non-skipped probe ran (the route is "not tested", handled elsewhere).
// perVantage splits an ALREADY route-scoped probe set into one result per
// (vantage, path) and reuses worstOutcome per group, so a group's verdict is
// derived by exactly the same rule as the rollup - just not merged across
// vantages. Skipped probes carry no observation and are dropped, matching
// routeFromProbes.
//
// Order is deterministic (first appearance) so a re-run cannot reshuffle the
// UI's rows when nothing changed.
func perVantage(probes []probe.Result) []VantageResult {
	type key struct {
		vantage probe.Vantage
		path    probe.Path
		source  string
	}
	groups := map[key][]probe.Result{}
	var order []key
	for _, p := range probes {
		if p.Skipped {
			continue
		}
		k := key{vantage: p.Vantage, path: p.Path, source: probeSource(p)}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], p)
	}
	if len(order) == 0 {
		return nil
	}
	out := make([]VantageResult, 0, len(order))
	for _, k := range order {
		outcome, evidence, failedLayer := worstOutcome(groups[k])
		confidence := ConfidenceReal
		if k.path == probe.PathAPIServer {
			confidence = ConfidenceIndirect
		}
		out = append(out, VantageResult{
			Vantage:     string(k.vantage),
			Path:        string(k.path),
			Source:      k.source,
			Outcome:     outcome,
			Confidence:  confidence,
			Evidence:    evidence,
			FailedLayer: failedLayer,
		})
	}
	return out
}

func routeFromProbes(routeID, target string, probes []probe.Result, backendScoped bool) (RouteResult, bool) {
	var real, indirect []probe.Result
	var localization []ProbeFact
	for _, p := range probes {
		if p.Skipped {
			continue
		}
		if p.Path == probe.PathAPIServer {
			// Behind-the-gate evidence: it confirms a component answers, but not
			// via real traffic. Localization-only - never the route's outcome.
			indirect = append(indirect, p)
			localization = append(localization, toFact(p))
		} else {
			real = append(real, p)
		}
	}
	if len(real) == 0 && len(indirect) == 0 {
		return RouteResult{}, false
	}
	r := RouteResult{Route: routeID, Target: target, Localization: localization}
	if len(real) > 0 {
		r.Outcome, r.Evidence, r.FailedLayer = worstOutcome(real)
		r.Confidence = ConfidenceReal
	} else {
		// Only the apiserver proxy reached it: indirect evidence. We record the
		// outcome it observed, but Confidence=indirect means it must NEVER be
		// read as a real-traffic pass/fail (the headline rule enforces that).
		r.Outcome, r.Evidence, r.FailedLayer = worstOutcome(indirect)
		r.Confidence = ConfidenceIndirect
	}
	r.ByVantage = perVantage(probes)
	if backendScoped {
		for i := range r.ByVantage {
			r.ByVantage[i].Segment = SegmentBackend
		}
	}
	return r, true
}

// worstOutcome reduces a probe set to one outcome. Precedence: any transport
// failure -> unreachable; else a degraded layer -> server-error; else an HTTP 2xx
// -> verified; else reached (3xx/4xx, or a transport-only TCP/TLS success that
// didn't verify an HTTP response). Evidence is the deciding probe's detail.
// failedLayer names WHICH layer failed on a non-reachable outcome ("tcp"/"tls"/
// "http" for unreachable; "tls" for a cert failure; "upstream" for a 502/504 where
// HTTP was reached but the gateway couldn't reach its backend) and is empty for a
// reachable outcome.
func worstOutcome(probes []probe.Result) (outcome, evidence, failedLayer string) {
	// Precedence is decided over the WHOLE set, never first-hit, so the result is
	// invariant under probe reordering. The deciding probe within each class is
	// chosen deterministically: failures/degraded prefer the EARLIEST network
	// layer (the root break - dns < tcp < tls < http; an HTTP failure behind a
	// failed TCP is a consequence, not the cause), successes prefer the LATEST
	// (most conclusive) layer, with target+detail tiebreaks.
	var failed, degraded, verified, reached, dnsOnly *probe.Result
	better := func(cur *probe.Result, p *probe.Result, preferEarly bool) bool {
		if cur == nil {
			return true
		}
		if a, b := layerRank(cur.Layer), layerRank(p.Layer); a != b {
			if preferEarly {
				return b < a
			}
			return b > a
		}
		if cur.Target != p.Target {
			return p.Target < cur.Target
		}
		return probeDetail(*p) < probeDetail(*cur)
	}
	pick := func(cur **probe.Result, p *probe.Result, preferEarly bool) {
		if better(*cur, p, preferEarly) {
			*cur = p
		}
	}
	for i := range probes {
		p := &probes[i]
		switch {
		case !p.OK || p.Tone == probe.ToneUnhealthy:
			pick(&failed, p, true)
		case p.Tone == probe.ToneDegraded:
			pick(&degraded, p, true)
		case p.Layer == probe.LayerDNS:
			// A name resolving is not transport reachability. Record it as evidence
			// but never let a DNS-only success read as "server reached" - that
			// overclaims reachability from a name lookup (the host may be internal /
			// split-horizon with TCP/TLS/HTTP skipped from this vantage).
			pick(&dnsOnly, p, true)
		case p.Layer == probe.LayerHTTP && p.Tone == probe.ToneHealthy:
			pick(&verified, p, false)
		default:
			pick(&reached, p, false)
		}
	}
	switch {
	case failed != nil:
		// ANY transport failure condemns the set, regardless of where it sat in the
		// slice. The failing layer names where the path broke: a TCP connect, a TLS
		// handshake, or an HTTP request that got no response back.
		return OutcomeUnreachable, probeDetail(*failed), string(failed.Layer)
	case degraded != nil:
		// A degraded probe still reached the responder. Name the layer honestly.
		switch degraded.Layer {
		case probe.LayerTLS:
			// A cert verification failure (expired / wrong host). A valid cert that
			// only expires soon is NOT degraded - it stays reachable (probe.go).
			return OutcomeServerError, probeDetail(*degraded), "tls"
		case probe.LayerHTTP:
			// A 502/504: HTTP was reached (a response came back), whose meaning is
			// that this gateway couldn't reach its upstream - an upstream fault,
			// never an HTTP failure.
			return OutcomeServerError, probeDetail(*degraded), "upstream"
		default:
			// No other layer sets ToneDegraded today; name the layer rather than
			// silently mislabeling a future one as "upstream".
			return OutcomeServerError, probeDetail(*degraded), string(degraded.Layer)
		}
	case verified != nil:
		return OutcomeVerified, probeDetail(*verified), ""
	case reached != nil:
		// Reached the server (3xx/4xx) or only a transport layer (TCP/TLS)
		// succeeded: reachable, but the exact HTTP route wasn't verified.
		return OutcomeReached, probeDetail(*reached), ""
	case dnsOnly != nil:
		// Only DNS resolved (TCP/TLS/HTTP skipped for a vantage reason): name
		// resolution alone is not server reachability - report not-tested.
		return OutcomeNotTested, probeDetail(*dnsOnly), ""
	}
	return OutcomeNotTested, "", ""
}

// layerRank orders probe layers by network depth (dns < tcp < tls < http) for
// worstOutcome's deterministic deciding-probe selection.
func layerRank(l probe.Layer) int {
	switch l {
	case probe.LayerDNS:
		return 0
	case probe.LayerTCP:
		return 1
	case probe.LayerTLS:
		return 2
	case probe.LayerHTTP:
		return 3
	}
	return 4
}

// hopHasLiveHTTP reports whether a non-skipped HTTP-layer probe ran against
// the given port on this hop - the evidence that lets a live-tested route
// absorb an HTTP-layer skip row for the same port.
func hopHasLiveHTTP(h Hop, port int32) bool {
	for _, p := range h.Probes {
		if !p.Skipped && p.Layer == probe.LayerHTTP && p.Port == port {
			return true
		}
	}
	return false
}

// isNonTCPProto reports a declared L4 protocol that is not TCP; empty is the
// Kubernetes default (TCP).
func isNonTCPProto(proto string) bool {
	switch strings.ToUpper(strings.TrimSpace(proto)) {
	case "", "TCP":
		return false
	}
	return true
}

// buildNotTested lists every distinct skip on the intended route (DOWNSTREAM
// only - upstreams are context, not the subject's routes), classified. A
// truncated fan-out is itself a coverage gap.
func buildNotTested(t *Trace) []RouteSkip {
	var out []RouteSkip
	seen := map[string]bool{}
	// A preserved route and the raw per-port skip rows behind it are the SAME
	// gap - keeping both rendered "argocd-server:80" and "port 80" as separate
	// scenarios. Structured identity (backend namespace/name + port), never
	// display strings: "port 80" can't string-match "argocd-server:80".
	type preservedRoute struct {
		// Only a REACHED route discriminates by layer: transport got through
		// and the app layer may be genuinely untried. Every other outcome
		// absorbs all its rows - not-tested and benign-dormant are the same
		// gap as their rows, verified proves the app layer, and a FAILED
		// transport already condemns the path (keeping its HTTP row counted
		// one broken port as failed AND couldn't-be-tried).
		absorbsAllLayers bool
	}
	preserved := map[string]preservedRoute{}
	for _, r := range t.Routes {
		name, port, ok := routeBackend(r)
		if !ok {
			continue
		}
		ns := r.TargetNamespace
		if ns == "" {
			ns = t.Subject.Namespace
		}
		preserved[fmt.Sprintf("%s\x00%s\x00%d", ns, name, port)] = preservedRoute{
			absorbsAllLayers: r.Outcome != OutcomeReached,
		}
	}
	for _, h := range t.Downstream {
		// Pod dials are LOCALIZATION evidence behind the Service, never intended
		// routes - sweeping their skips in here grew per-Pod "routes" beside the
		// Service's own (a Service:80 and its Pod:8080 read as "2 routes"). The
		// probes stay fully visible on the hop itself (pod rows, anomalies,
		// sampling notes all read Downstream[].Probes).
		if h.Resource.Kind == "Pods" {
			continue
		}
		hopNS := h.Resource.Namespace
		if hopNS == "" {
			hopNS = t.Subject.Namespace
		}
		for _, p := range h.Probes {
			if !p.Skipped || p.Reason == "" {
				continue
			}
			// Absorption is keyed by port NUMBER, and a preserved route is a TCP
			// candidate - so it may only absorb a skip that is itself TCP-side.
			// The ROW's declared protocol decides: kube-dns declares UDP :53
			// beside TCP :53, and a number-level guard either let the TCP route
			// swallow the UDP row (erasing a declared path) or kept the TCP
			// sibling's own skip beside a route that already covers it
			// (a contradiction).
			// One layer-aware exception: a LIVE-tested route absorbs an
			// HTTP-layer skip only when that layer actually ran on the port.
			// A transport-only TCP reach on an HTTPS port must not swallow the
			// HTTPS gap - the contract is that HTTPS stays a coverage gap until
			// an application-layer request completes. Ports that expect no app
			// layer (redis, dns-tcp) are complete at TCP and absorb as before.
			if p.Port != 0 && !isNonTCPProto(p.Protocol) {
				if pr, ok := preserved[fmt.Sprintf("%s\x00%s\x00%d", hopNS, h.Resource.Name, p.Port)]; ok {
					appLayerGap := p.Layer == probe.LayerHTTP &&
						httpProbablePort(hopPorts(h), p.Port) &&
						!hopHasLiveHTTP(h, p.Port)
					if pr.absorbsAllLayers || !appLayerGap {
						continue
					}
				}
			}
			key := string(p.Layer) + "|" + p.Target + "|" + p.Reason
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, RouteSkip{
				Route:       p.Target,
				Reason:      p.Reason,
				ReasonClass: skipClassOf(p),
				Command:     p.Command,
			})
		}
	}
	if t.Truncated {
		out = append(out, RouteSkip{
			Reason:      "some backends/routes were not traced (fan-out exceeded the cap)",
			ReasonClass: SkipClassCoverage,
		})
	}
	return out
}

// classifySkip maps a skip reason to its coverage impact. Pod-sampling is the
// only benign case (the unsampled pods are identical replicas); vantage skips
// name an inability to reach FROM HERE; everything else is a real coverage gap.
// skipClassOf returns the coverage class of a skipped probe, preferring the
// structured SkipClass the probe stamped at its skip site. It falls back to
// matching the human Reason text only when the class is unset, so a reworded
// message no longer silently misclassifies a skip that carries the field.
func skipClassOf(p probe.Result) string {
	if p.SkipClass != "" {
		return p.SkipClass
	}
	return classifySkip(p.Reason)
}

func classifySkip(reason string) string {
	low := strings.ToLower(reason)
	switch {
	case strings.Contains(low, "sampled "):
		return SkipClassBenign
	case strings.Contains(low, "from your machine"),
		strings.Contains(low, "from here"),
		strings.Contains(low, "internal address"),
		strings.Contains(low, "run radar in-cluster"),
		strings.Contains(low, "run radar from in-cluster"):
		return SkipClassVantage
	default:
		return SkipClassCoverage
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func toFact(p probe.Result) ProbeFact {
	return ProbeFact{
		Layer:  string(p.Layer),
		Path:   string(p.Path),
		Target: p.Target,
		OK:     p.OK,
		Tone:   string(p.Tone),
		Detail: probeDetail(p),
	}
}

func probeDetail(p probe.Result) string {
	if p.Detail != "" {
		return p.Detail
	}
	return p.Error
}

// ruleRoute is ONE declared host+path rule selecting a backend - the unit of
// route identity. label is the route's display/fold identity (host included only
// when the entry serves >1 host); host+path build the route's OWN in-cluster
// request so it tests exactly what this rule declares; ports are the backend
// ports THIS rule resolves (empty when the rule doesn't pin one).
type ruleRoute struct {
	label string
	host  string
	path  string
	ports []int32
}

// ruleRoutesForBackend derives the per-rule route identities that select a given
// backend. Never joined: two rules sharing a backend stay two routes, each with
// its own host+path, so an observation of one can never vouch for the other.
// Duplicate labels (the same host+path declared twice for this backend) collapse
// into one, merging their declared ports.
func ruleRoutesForBackend(entry Hop, backendName, backendNS string, cfg *HopConfig) []ruleRoute {
	if entry.Config == nil || backendName == "" {
		return nil
	}
	multiHost := len(entry.Config.Hostnames) > 1
	var out []ruleRoute
	idx := map[string]int{}
	add := func(label, host, path string, ports []int32) {
		if label == "" {
			return
		}
		if i, ok := idx[label]; ok {
			out[i].ports = mergePorts(out[i].ports, ports)
			return
		}
		idx[label] = len(out)
		out = append(out, ruleRoute{label: label, host: host, path: path, ports: ports})
	}
	for _, rule := range entry.Config.Rules {
		var ports []int32
		matched := false
		for _, b := range rule.Backends {
			if !backendRefMatches(b, backendName, entry.Resource.Namespace, backendNS) {
				continue
			}
			matched = true
			if p := resolveBackendPort(b.Port, cfg); p > 0 {
				ports = mergePorts(ports, []int32{p})
			}
		}
		if !matched {
			continue
		}
		paths := rule.Paths
		if len(paths) == 0 {
			paths = []string{"/"}
		}
		// Gateway-API routes keep hostnames at Config.Hostnames, not per-rule
		// (ingressConfig sets rule.Hosts; routeConfig does not). Fall back to the
		// entry's hostnames so a multi-host route still emits host-qualified
		// labels with the rule's own host - otherwise entryProbesForHost has no
		// host to scope by and every front-door host dial folds into each
		// backend, cross-contaminating sibling hosts' outcomes.
		ruleHosts := rule.Hosts
		if len(ruleHosts) == 0 {
			ruleHosts = entry.Config.Hostnames
		}
		for _, p := range paths {
			if multiHost && len(ruleHosts) > 0 {
				for _, h := range ruleHosts {
					add(routeLabel(h, p), h, p, ports)
				}
			} else {
				// Single-host entry: the label omits the host, but the in-cluster
				// request still needs the declared Host header when one exists.
				host := ""
				if len(ruleHosts) > 0 {
					host = ruleHosts[0]
				}
				add(routeLabel("", p), host, p, ports)
			}
		}
	}
	return out
}

func mergePorts(existing, add []int32) []int32 {
	for _, p := range add {
		if !slices.Contains(existing, p) {
			existing = append(existing, p)
		}
	}
	return existing
}

// attachInClusterRequest fills each route's best-guess in-cluster request from
// the route's declared host/path and the backend Service port (parsed back from
// the route Target so multi-port routes each get their own protocol).
func attachInClusterRequest(routes []RouteResult, host, path string, cfg *HopConfig) {
	for i := range routes {
		// A benign-dormant route is deliberately not runnable - recommending a
		// probe against a Service scaled to zero offers a test that cannot work.
		if routes[i].Benign {
			continue
		}
		pm, ok := portFromTarget(routes[i].Target, cfg)
		if !ok {
			continue
		}
		req := guessInClusterRequest(host, path, pm)
		// A route can exist on non-HTTP evidence (a direct TCP reach of a
		// redis port). Such a port gets a TCP-shaped request - never an
		// HTTP-shaped one against a protocol the prober itself declined to
		// speak - and a UDP/SCTP port gets no request at all.
		if req.Protocol == "" {
			continue
		}
		routes[i].InClusterRequest = &req
	}
}

// guessInClusterRequest derives a concrete, runnable request from a declared
// route. Pattern paths (regex/wildcard) have no single faithful request, so it
// guesses the leading literal and flags PathGuessed - the UI surfaces that and
// lets the user correct it before running.
func guessInClusterRequest(host, path string, port PortMap) ProbeRequest {
	protocol := protocolForPort(port)
	req := ProbeRequest{Protocol: protocol}
	if protocol == "http" || protocol == "https" {
		req.Scheme = protocol
		req.Host = concreteHost(host)
		req.Path, req.PathGuessed = guessConcretePath(path)
	}
	return req
}

func protocolForPort(port PortMap) string {
	if protocol := strings.ToUpper(strings.TrimSpace(port.Protocol)); protocol == "UDP" || protocol == "SCTP" {
		return ""
	}
	if !isHTTPProbablePort(port.Name, port.AppProtocol, port.Port) {
		return "tcp"
	}
	return schemeForPort(port)
}

// schemeForPort reads the L7 scheme off the backend Service port: the explicit
// appProtocol hint wins, then a conventional "https" port name, then the 443
// fallback; everything else is plain http.
// schemeForPort reads the L7 scheme off the backend Service port by delegating
// to the SAME TLS classifier the prober uses. Two classifiers disagreed on
// wss/tls names and bare 8443 - the request builder sent plaintext HTTP Jobs
// at ports the prober itself treats as TLS.
func schemeForPort(port PortMap) string {
	if isHTTPSPort(port.Name, port.AppProtocol, port.Port) {
		return "https"
	}
	return "http"
}

// concreteHost turns a declared host into one a request can actually send. A
// wildcard host (*.example.com) isn't a real hostname, so it's specialized to a
// plausible concrete subdomain the user can change.
func concreteHost(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "*.") {
		return "www." + host[2:]
	}
	return host
}

// strongPathMetachars are the characters that unambiguously turn a path into a
// pattern (Ingress ImplementationSpecific regex, nginx regex locations). A bare
// '.' is deliberately NOT one of them: it is a common literal in real paths
// (version numbers like /api/v1.0, file extensions), and is only a regex
// metacharacter when it hugs a quantifier (the '.' of '.*'/'.+'), which is
// handled by trimming it off the literal prefix below.
const strongPathMetachars = `*+?()[]{}|^$\`

// guessConcretePath returns a concrete path to request plus whether it was a
// guess. An Exact match is a single literal path and is dialed verbatim (never a
// guess); a pattern path is reduced to its leading literal (everything before the
// first regex metacharacter), which still matches the pattern, and flagged as a
// guess. A plain literal path is used verbatim.
func guessConcretePath(path string) (string, bool) {
	path = strings.TrimSpace(path)
	// Gateway-API routes carry the match type as a display prefix ("Exact:/foo",
	// "RegularExpression:/foo.*"). Strip it so the request path is the real URL,
	// not "/Exact:/foo".
	matchType := ""
	if tag := leadingTypeTag(path); tag != "" {
		matchType = strings.TrimSuffix(tag, ":")
		path = path[len(tag):]
	}
	if path == "" {
		path = "/"
	}
	// An Exact match is one concrete literal path; the direct dial exercises the
	// route identically, so it is faithful and its characters are all literal.
	if matchType == "Exact" {
		return ensureLeadingSlash(path), false
	}
	if path == "/" {
		return "/", matchType == "RegularExpression"
	}
	if i := strings.IndexAny(path, strongPathMetachars); i >= 0 {
		// A '.' immediately before the metacharacter is part of the pattern atom
		// (the '.' of '.*'), not a literal, so trim it from the probed prefix.
		lit := strings.TrimRight(path[:i], ".")
		if lit == "" || lit == "/" {
			return "/", true
		}
		return ensureLeadingSlash(lit), true
	}
	// No strong metacharacter: a literal path. Only a RegularExpression match stays
	// a guess (its value may use '.' as any-char); every other path is faithful.
	return ensureLeadingSlash(path), matchType == "RegularExpression"
}

// leadingTypeTag returns a leading "Word:" match-type tag (e.g. "Exact:") when
// the path carries one before the actual "/path", else "". Used to peel the
// Gateway-API display prefix off before building a runnable request path.
func leadingTypeTag(path string) string {
	i := strings.IndexByte(path, ':')
	if i <= 0 || i+1 >= len(path) || path[i+1] != '/' {
		return ""
	}
	for _, r := range path[:i] {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
			return ""
		}
	}
	return path[:i+1]
}

func ensureLeadingSlash(p string) string {
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

// portFromTarget parses the "name:port" route Target and returns its unique
// Service PortMap. A numeric target cannot distinguish two Service ports that
// share a number but use different protocols, so that shape fails closed rather
// than attaching a potentially false TCP/HTTP request.
func portFromTarget(target string, cfg *HopConfig) (PortMap, bool) {
	num := int32(0)
	if i := strings.LastIndexByte(target, ':'); i >= 0 {
		if n, err := strconv.ParseInt(target[i+1:], 10, 32); err == nil {
			num = int32(n)
		}
	}
	if cfg != nil {
		// A route target is something a request was (or would be) sent to, and
		// requests only travel TCP - so when TCP and UDP share the number, the
		// TCP entry owns the request. Declaration order never decides: with no
		// TCP entry among the matches (UDP beside SCTP), the shape fails closed
		// rather than attaching a request in a protocol nothing speaks.
		var fallback *PortMap
		matches := 0
		for i := range cfg.Ports {
			p := cfg.Ports[i]
			if p.Port != num {
				continue
			}
			matches++
			switch strings.ToUpper(strings.TrimSpace(p.Protocol)) {
			case "", "TCP":
				return p, true
			default:
				if fallback == nil {
					fallback = &p
				}
			}
		}
		if matches > 1 {
			return PortMap{}, false
		}
		if fallback != nil {
			return *fallback, true
		}
	}
	return PortMap{Port: num}, num != 0
}

// backendRefMatches reports whether a route BackendRef points at the given
// backend hop. Names must match; namespaces must match too once the backend's
// namespace is known - a BackendRef with no namespace defaults to the route's
// (entry's) namespace. Without the namespace guard two same-named Services in
// different namespaces (legal via a Gateway-API ReferenceGrant) would merge
// their labels/ports and could verify/condemn the wrong route. When either
// namespace is unknown it falls back to name-only (can't disprove).
func backendRefMatches(b BackendRef, backendName, entryNS, backendNS string) bool {
	if b.Name != backendName {
		return false
	}
	if backendNS == "" {
		return true
	}
	refNS := b.Namespace
	if refNS == "" {
		refNS = entryNS
	}
	if refNS == "" {
		return true
	}
	return refNS == backendNS
}

func routeLabel(host, path string) string {
	if path == "(default backend)" {
		if host != "" {
			return host + " (default backend)"
		}
		return "default backend"
	}
	if host != "" {
		return host + path
	}
	return path
}

// entryProbesForHost returns the entry hop's front-door probes whose target
// matches the given declared host - the real-traffic dials that belong to a
// rule's routes. An empty host means every entry probe applies (single-host
// entry, or a fallback rule with no declared host to scope by).
func entryProbesForHost(entry Hop, host string) []probe.Result {
	if len(entry.Probes) == 0 {
		return nil
	}
	if host == "" {
		return entry.Probes
	}
	var out []probe.Result
	for _, p := range entry.Probes {
		if targetHost(p.Target) == host {
			out = append(out, p)
		}
	}
	return out
}

// targetHost extracts the host from a probe target. HTTP probes carry a full URL
// (scheme://host[:port]/path) while DNS/TCP/TLS probes carry host[:port]; without
// stripping the scheme+path an HTTP target like "http://host/path" parses to
// "http" and every front-door HTTP probe is dropped from the backend outcome
// (a 5xx/2xx silently lost - a false-clear on multi-host entries).
func targetHost(target string) string {
	if i := strings.Index(target, "://"); i >= 0 {
		target = target[i+3:]
		if j := strings.IndexByte(target, '/'); j >= 0 {
			target = target[:j]
		}
	}
	if i := strings.LastIndexByte(target, ':'); i > 0 {
		// Only strip when the suffix is a port (no other ':' - i.e. not IPv6).
		if !strings.Contains(target[:i], ":") {
			return target[:i]
		}
	}
	return target
}

// routeHostKey reduces a route label or probe target to its bare host so the
// not-tested dedup compares like with like: it strips a scheme, a trailing path,
// and a port (leaving IPv6 colons intact). "http://shop.example.com/api" and
// "shop.example.com:443" both reduce to "shop.example.com".
// routeResultHostKey recovers the front-door host a route's probes target, for
// deduping a NotTested route against its own skipped probe rows (keyed by probe
// Target host). A single-host route's Route label is path-only ("/api") because
// ruleRoutesForBackend omits the host when the entry serves one host, so
// reading the host off the label yields "" and the dedup misfires - counting the
// route AND its skip rows. The declared host lives on InClusterRequest (the same
// concretized host the front-door probe dialed), so fall back to it.
func routeResultHostKey(r RouteResult) string {
	if h := routeHostKey(r.Route); h != "" {
		return h
	}
	if r.InClusterRequest != nil {
		return routeHostKey(r.InClusterRequest.Host)
	}
	return ""
}

func routeHostKey(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, ':'); i > 0 && !strings.Contains(s[:i], ":") {
		s = s[:i]
	}
	return s
}

// subjectRouteLabel names the single route for a Service/Pod subject.
func subjectRouteLabel(subject ResourceRef, entry Hop) string {
	if entry.Config != nil && len(entry.Config.Hostnames) > 0 {
		return entry.Config.Hostnames[0]
	}
	if subject.Name != "" {
		return subject.Name
	}
	return entry.Resource.Name
}

func subjectTarget(entry Hop) string {
	if entry.Config != nil && len(entry.Config.Ports) > 0 {
		return fmt.Sprintf("%s:%d", entry.Resource.Name, entry.Config.Ports[0].Port)
	}
	return entry.Resource.Name
}

// CoverageHeadline renders the coverage-honest one-line summary from the
// computed Coverage + Routes. It is the agent/UI primary read and counts only
// INTENDED routes. Invariant: it NEVER claims "verified" for an indirect
// (apiserver) route - behind-the-gate evidence is reported as "reached via the
// management API", never as real-traffic verification.
func CoverageHeadline(t *Trace) string {
	if t == nil || t.Coverage == nil {
		return "Configuration only - not yet tested"
	}
	c := t.Coverage
	// A derived break is a route we KNOW cannot work, so the headline counts it
	// among the unreachable - "All N reachable" beside a missing backend would be
	// false. That it was never dialled is a coverage nuance, carried by the
	// footer and the route's own chip, not by the top-line verdict.
	tested := c.Tested + c.Derived
	failed := c.Failed + c.Derived
	// A bare Service subject's intended routes ARE its ports, so the headline
	// speaks of "ports"; an Ingress/Gateway/Route entry speaks of "routes".
	noun := "routes"
	if t.Subject.Kind == "Service" {
		noun = "ports"
	}
	if tested == 0 {
		// "Couldn't actively test" is only honest when a probe ACTUALLY ran and every
		// route skipped from this vantage. An un-probed/static trace (the drawer) has
		// skipped routes only because nothing was tried yet → "not tested yet".
		if c.Skipped > 0 && anyProbeRan(t) {
			return "Couldn't actively test any route from here"
		}
		return "Configuration only - not yet tested"
	}
	// Single intended route/port: speak in the singular, no fraction.
	if tested == 1 && len(t.Routes) == 1 {
		return singleRouteHeadline(t.Routes[0], c.Skipped, noun)
	}
	notTested := ""
	if c.Skipped > 0 {
		// Name WHAT is counted: a bare "· 1 not tested" beside the headline read
		// as a rendering artifact (1 what - probes? hops? resources?).
		n := noun
		if c.Skipped == 1 {
			n = strings.TrimSuffix(noun, "s")
		}
		notTested = fmt.Sprintf(" · %d %s not tested", c.Skipped, n)
	}
	// When every passing route's evidence bypassed the front door, the counts
	// prove the backends - not the entry path. Say so once, headline-level.
	backendOnly := ""
	if c.Passed > 0 {
		all := true
		for _, r := range t.Routes {
			if r.Benign {
				continue
			}
			if r.Outcome == OutcomeVerified || r.Outcome == OutcomeReached {
				if !RouteBackendScoped(r) {
					all = false
					break
				}
			}
		}
		if all {
			backendOnly = " · backend only - the entry path was not exercised"
		}
	}
	notTested += backendOnly
	// A server-error route WAS reached (it answered with a 5xx); lumping it into
	// "unreachable" would wrongly imply a dead network path. Separate the two so
	// the headline never says "none reachable" about routes that did respond.
	errored, benignFailed := failureKinds(t.Routes)
	unreachable := failed - errored - benignFailed
	if unreachable < 0 {
		unreachable = 0
	}
	// A proxy-only (indirect) unreachable never condemns the real path - qualify
	// the multi-route headline the same way singleRouteHeadline does, instead of
	// a bare "unreachable" that false-condemns a laptop-vantage failure.
	indirectUnreach := unreachable > 0 && allUnreachableIndirect(t.Routes)
	// When every passing route was reached ONLY via the apiserver proxy, the
	// real-traffic path was never confirmed - say so, mirroring singleRouteHeadline,
	// instead of a bare "reachable" that CoverageVerdict (gating on !anyRealPass)
	// would contradict with unknown (the B3 headline/verdict contradiction).
	proxyOnly := allPassesIndirect(t.Routes)
	switch {
	case failed == 0 && c.Skipped == 0:
		if proxyOnly {
			return fmt.Sprintf("All %d %s reached - checked only via API server, real path not confirmed", tested, noun)
		}
		return fmt.Sprintf("All %d %s reachable", tested, noun)
	case failed == 0:
		// All tested routes passed, but a real coverage gap exists (1A footnote-green).
		if proxyOnly {
			return fmt.Sprintf("All %d tested %s reached - checked only via API server, real path not confirmed%s", tested, noun, notTested)
		}
		return fmt.Sprintf("All %d tested %s reachable%s", tested, noun, notTested)
	case c.Passed == 0 && allFailuresBenign(t.Routes):
		// Every route is a deliberate scale-to-0, not an outage. "None reachable"
		// would frame intentional dormancy as a failure and contradict
		// CoverageVerdict, which softens this to amber degraded.
		return fmt.Sprintf("All %d %s intentionally scaled to 0 (no running backends)%s", tested, noun, notTested)
	case c.Passed == 0 && errored > 0:
		// At least one route answered (5xx) - "none reachable" would be dishonest.
		return fmt.Sprintf("0 of %d %s reachable · %s%s", tested, noun, failClause(unreachable, errored, indirectUnreach), notTested)
	case c.Passed == 0 && indirectUnreach:
		// Every unreachable route was seen only via the apiserver proxy - the real
		// path was never confirmed, so don't bare-condemn the whole entry.
		return fmt.Sprintf("None of %d %s confirmed reachable - checked only via API server, real path not confirmed%s", tested, noun, notTested)
	case c.Passed == 0:
		return fmt.Sprintf("None of %d %s reachable%s", tested, noun, notTested)
	default:
		// failClause covers unreachable + erroring; a mixed trace can also carry a
		// benign scale-to-0 route, which would otherwise be silently dropped from
		// both the reachable count and the failure clause (leaving a dangling
		// trailing "· "). Surface it and only append the clause when non-empty.
		clause := failClause(unreachable, errored, indirectUnreach)
		if benignFailed > 0 {
			benignClause := fmt.Sprintf("%d scaled to 0", benignFailed)
			if clause == "" {
				clause = benignClause
			} else {
				clause = clause + " · " + benignClause
			}
		}
		if clause == "" {
			return fmt.Sprintf("%d of %d %s reachable%s", c.Passed, tested, noun, notTested)
		}
		return fmt.Sprintf("%d of %d %s reachable · %s%s", c.Passed, tested, noun, clause, notTested)
	}
}

// failureKinds splits failed routes into the count that were REACHED but
// returned a server error and the count that are benign (intentional scale-0).
// The remaining failures (Coverage.Failed minus these) are genuine transport
// unreachability. Benign routes carry their own amber framing, so the headline
// neither calls them "unreachable" nor "erroring".
func failureKinds(routes []RouteResult) (errored, benign int) {
	for _, r := range routes {
		switch {
		case r.Benign:
			benign++
		case r.Outcome == OutcomeServerError:
			errored++
		}
	}
	return
}

// failClause renders the failure breakdown, omitting any zero clause: e.g.
// "1 unreachable · 2 reached but erroring", or just "2 reached but erroring".
func failClause(unreachable, errored int, indirectUnreach bool) string {
	parts := make([]string, 0, 2)
	if unreachable > 0 {
		u := fmt.Sprintf("%d unreachable", unreachable)
		if indirectUnreach {
			u += " via API server (real path not confirmed)"
		}
		parts = append(parts, u)
	}
	if errored > 0 {
		parts = append(parts, fmt.Sprintf("%d reached but erroring", errored))
	}
	return strings.Join(parts, " · ")
}

// allUnreachableIndirect reports whether every genuinely-unreachable route
// (non-benign, transport-unreachable) was observed ONLY via the apiserver proxy.
// When true the headline must qualify "unreachable" - a proxy-only failure never
// condemns the real path.
func allUnreachableIndirect(routes []RouteResult) bool {
	any := false
	for _, r := range routes {
		if r.Benign || r.Outcome != OutcomeUnreachable {
			continue
		}
		any = true
		if r.Confidence != ConfidenceIndirect {
			return false
		}
	}
	return any
}

func singleRouteHeadline(r RouteResult, skipped int, noun string) string {
	suffix := ""
	if skipped > 0 {
		n := noun
		if skipped == 1 {
			n = strings.TrimSuffix(noun, "s")
		}
		suffix = fmt.Sprintf(" · %d %s not tested", skipped, n)
	}
	ev := ""
	if r.Evidence != "" {
		ev = " (" + r.Evidence + ")"
	}
	// Intentional scale-to-0: deliberate dormancy, not an outage - never "Unreachable".
	if r.Benign {
		return "No running backends (scaled to 0)" + suffix
	}
	switch r.Outcome {
	case OutcomeVerified, OutcomeReached:
		// A SUCCESS reached only via the apiserver proxy is not the live-traffic
		// path - never "verified". (Only successes get this prefix; a failure is a
		// failure regardless of which path observed it - see below.)
		if r.Confidence == ConfidenceIndirect {
			return "Reached " + VantageAPIServer + " - not live traffic" + ev + suffix
		}
		// A real pass whose every dial bypassed the front door has proven the
		// BACKEND, not the route's entry path - saying bare "verified" let an
		// in-cluster pass paint a dead Ingress green.
		if RouteBackendScoped(r) {
			if r.Outcome == OutcomeVerified {
				return "Backend verified - the entry path was not exercised by this test" + ev + suffix
			}
			return "Backend reached - the entry path was not exercised by this test" + ev + suffix
		}
		if r.Outcome == OutcomeVerified {
			return "Reachable - verified" + ev + suffix
		}
		return "Reachable - server reached, route not verified" + ev + suffix
	case OutcomeServerError:
		if r.Confidence == ConfidenceIndirect {
			return "Server error " + VantageAPIServer + " - not live traffic" + ev + suffix
		}
		return "Reached - server error" + ev + suffix
	case OutcomeUnreachable:
		// A failure observed ONLY through the apiserver proxy isolates the symptom
		// to that path, not the real-traffic path. Stating a bare "Unreachable"
		// would headline a condemnation from proxy-only evidence - never honest
		// about the live path. Qualify it symmetrically with the success case.
		if r.Confidence == ConfidenceIndirect {
			return "Unreachable " + VantageAPIServer + " - real path not confirmed" + ev + suffix
		}
		return "Unreachable" + ev + suffix
	default:
		return "Not tested" + suffix
	}
}

// probeSource normalises a probe's issuer; an unset Source means Radar itself,
// which is what every inline probe is.
func probeSource(p probe.Result) string {
	if p.Source == "" {
		return probe.SourceRadar
	}
	return p.Source
}

// vantageSource mirrors probeSource for an already-projected vantage row.
func vantageSource(v VantageResult) string {
	if v.Source == "" {
		return probe.SourceRadar
	}
	return v.Source
}

// soleFailedRoute returns the one non-benign failed route when there is exactly
// one, so a resource-level finding can be attributed to it. With several failed
// routes the finding belongs to no single path and must stay unattributed.
func soleFailedRoute(routes []RouteResult) (RouteResult, bool) {
	var found RouteResult
	n := 0
	for _, r := range routes {
		if r.Benign || (r.Outcome != OutcomeUnreachable && r.Outcome != OutcomeServerError) {
			continue
		}
		found = r
		n++
	}
	return found, n == 1
}

// distinctSkipReasons collapses the not-tested reasons to the unique set, in
// first-appearance order. One shared reason is the resource's answer; several
// mean the paths differ and each carries its own.
func distinctSkipReasons(skips []RouteSkip) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range skips {
		r := strings.TrimSpace(s.Reason)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

func firstSkipCommand(skips []RouteSkip) string {
	for _, s := range skips {
		if s.Command != "" {
			return s.Command
		}
	}
	return ""
}
