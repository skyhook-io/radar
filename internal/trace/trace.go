// Package trace composes a path-shaped diagnostic over a network entry kind
// (Service, Ingress, HTTPRoute, GRPCRoute, Gateway) - answering "if traffic
// is sent toward this resource, does it reach a healthy process, and if not
// which hop breaks first?"
//
// The output is a hop-ordered diagnosis, not a pile of alerts: findings are
// attached to the hop where the failure is observable, the first critical
// Downstream hop is named BrokenAt, and parallel Upstreams are judged
// independently so one broken Ingress does not condemn a Service the other
// Ingresses still deliver to.
//
// This package is deliberately the declared-axis only: pure functions over the
// informer cache, zero per-check API calls, zero RBAC asks, zero user
// configuration. Active probes, EndpointSlice on-demand reads, and
// NetworkPolicy rule evaluation are out of scope - they would force this
// feature out of the local-first zero-config envelope. NetworkPolicies that
// select the subject's pods are noted as an advisory only.
//
// Existing detection lives in internal/issues and internal/k8s/detect*.go;
// this package COMPOSES those findings into a path shape rather than running a
// parallel check engine.
package trace

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/probe"
)

const (
	VerdictHealthy  = "healthy"
	VerdictDegraded = "degraded"
	VerdictBroken   = "broken"
	VerdictUnknown  = "unknown"

	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"

	gatewayRouteCap = 20
)

// Deps lets the trace builder share Radar's caches with the call site. The
// typed/unstructured split mirrors internal/k8s/detect_missing_refs.go:
// Services/Pods/Ingresses/NetworkPolicies come from typed listers; Gateway API
// kinds come from the dynamic cache as *unstructured.Unstructured.
type Deps struct {
	Cache     *k8s.ResourceCache
	Dynamic   *k8s.DynamicResourceCache
	Discovery *k8s.ResourceDiscovery
	Issues    *issues.CacheProvider
	// Client is the live K8s clientset used by reachability probes that go
	// through the API server proxy (Service/Pod /proxy). Optional - when
	// nil, probes fall back to direct TCP only.
	Client kubernetes.Interface
	// AllowedNamespaces is the per-request namespace allow-list derived
	// from the caller's RBAC. A nil slice means "no scope filtering"
	// (single-user / auth-disabled / cluster-wide caller); a non-nil
	// empty slice denies every namespace. When the list scopes a hop
	// out, that hop is rendered as a referenced ResourceRef without
	// config or probes, and flagged unverifiable in the verdict - the
	// caller can see that a cross-namespace dependency exists without
	// leaking the dependent's shape or live state. See NamespaceAllowed.
	AllowedNamespaces []string
}

// NamespaceAllowed reports whether the caller's RBAC scope permits
// reading resources in ns. The nil/non-nil distinction matches the
// rest of the codebase: a nil slice means "no scope filtering"
// (single-user / auth-disabled / cluster-wide caller), a non-nil
// empty slice means "no namespaces allowed" (denies all). Without
// this split, a user whose permission lookup returned an empty
// allow-list (rather than nil) would silently get cross-namespace
// read.
func (d Deps) NamespaceAllowed(ns string) bool {
	if d.AllowedNamespaces == nil {
		return true
	}
	for _, allowed := range d.AllowedNamespaces {
		if allowed == ns {
			return true
		}
	}
	return false
}

// ResourceRef points at a single Kubernetes object in the trace.
type ResourceRef struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// Finding is one observation attached to a hop. Code is stable across runs so
// agents/UIs can compare; Severity matches the issues vocabulary
// (critical|warning|info). Command is a kubectl reproducer where derivable.
//
// Cause + Action carry parsed domain diagnosis when the issues pipeline
// classified the failure (CrashLoopBackOff exit codes, ImagePullBackOff
// registry/auth/not-found, PVC provisioning failures). When present, Cause
// is the plain-English root cause and Action is the next step the operator
// should take. Both empty for detectors without a parser - the UI falls back
// to Message + Remediation in that case.
type Finding struct {
	Code        string `json:"code"`
	Severity    string `json:"severity"`
	Message     string `json:"message"`
	Cause       string `json:"cause,omitempty"`
	Action      string `json:"action,omitempty"`
	Remediation string `json:"remediation,omitempty"`
	Command     string `json:"command,omitempty"`
	// Chips is a renderable strip of small tags carrying structured detail a
	// flat sentence can't hold - today the egress-note's allowed destinations.
	// On-chip text stays jargon-free; the jargon lives in each chip's Title.
	Chips []FindingChip `json:"chips,omitempty"`
	// ChipsLabel is the tiny field label printed before the chip strip
	// ("Allowed out"). Empty when the chips read on their own (deny-all,
	// allow-all).
	ChipsLabel string `json:"chipsLabel,omitempty"`
	// ChipNotes are quiet tertiary caveats printed under the chips - the
	// declared-vs-enforced reminder and the gated DNS observation. Kept
	// subordinate so an info side-note never outshouts the inbound verdict.
	ChipNotes []string `json:"chipNotes,omitempty"`
	// Resource names the specific object this finding is about when it differs
	// from the hop it hangs on - a pod-fanout hop carries one finding per code
	// but the culprit is a named Pod, not the "Pods" collection. Set so the
	// coverage Diagnosis can point at the actual pod. Empty = the hop's own
	// resource is the subject.
	Resource *ResourceRef `json:"resource,omitempty"`
}

// FindingChip is one tag in a Finding's chip strip. Tone "accent" renders the
// info-blue variant (anywhere / deny-all); empty renders the muted variant.
type FindingChip struct {
	Text  string `json:"text"`
	Title string `json:"title,omitempty"`
	Tone  string `json:"tone,omitempty"`
}

// Hop is a single resource along the traffic path. Edge labels the relation
// from the previous resource ("HTTPRoute->Service"). Meta carries hop-shaped
// extras the UI may surface (pod counts, endpointSource, headless flag).
// Config carries the declared-config shape this hop contributes to the path
// (Service ports, container ports, route rules, gateway listeners) so the
// UI can display the wiring without re-fetching each resource.
type Hop struct {
	Resource ResourceRef    `json:"resource"`
	Edge     string         `json:"edge"`
	Findings []Finding      `json:"findings"`
	Meta     map[string]any `json:"meta,omitempty"`
	Config   *HopConfig     `json:"config,omitempty"`
	Probes   []probe.Result `json:"probes,omitempty"`
}

// HopConfig is the structured per-hop config snapshot. Every field is
// optional; only the fields relevant to the hop's resource kind are filled.
// The UI is expected to gracefully ignore fields it doesn't recognize.
type HopConfig struct {
	// Service hops
	Ports       []PortMap         `json:"ports,omitempty"`
	ServiceType string            `json:"serviceType,omitempty"`
	ClusterIP   string            `json:"clusterIP,omitempty"`
	Selector    map[string]string `json:"selector,omitempty"`

	// Pod-collection hops
	ContainerPorts []ContainerPortRef `json:"containerPorts,omitempty"`
	Probes         []ProbeRef         `json:"probes,omitempty"`
	// PodIPs lists the IPs of selected pods at trace time (capped to keep
	// the JSON bounded on large fleets). Drives the in-cluster TCP probe
	// against pod-level reachability; from a local vantage this is
	// informational only - operators reading the trace can see "these are
	// the pods that would receive traffic if the vantage were inside".
	PodIPs []string `json:"podIPs,omitempty"`
	// PodNames is the same set as PodIPs but by name - used by the API
	// server proxy probe (which addresses pods by name, not IP). Kept
	// separate from PodIPs because the in-cluster TCP path needs IPs and
	// the local kube-proxy path needs names.
	PodNames []string `json:"podNames,omitempty"`
	// Pods is the per-pod roster (ready AND not-ready) for the reachability
	// grid, capped like PodIPs. A ready pod also carries a probe result on the
	// hop (joined by Name for the apiserver path or IP for the data path); a
	// not-ready pod has nothing to reach, so the grid reads its state from here.
	Pods []PodStatus `json:"pods,omitempty"`
	// PodTotal is the count of ALL selected pods (Pods is capped for JSON size).
	// The grid renders "N of PodTotal" so a sampled fleet never reads as complete.
	PodTotal int `json:"podTotal,omitempty"`
	// Workload is what RUNS these Pods, resolved through their owner chain
	// (Pod -> ReplicaSet -> Deployment, or a direct StatefulSet/DaemonSet owner).
	// Not a network object, which is why it took this long to appear here - but
	// it is what an operator calls the thing behind a Service, and leaving it out
	// made them hold that connection in their head. Nil when the selected Pods
	// have no single owner (a bare Pod, or two workloads behind one Service).
	Workload *ResourceRef `json:"workload,omitempty"`

	// Ingress / HTTPRoute / GRPCRoute hops
	Hostnames []string    `json:"hostnames,omitempty"`
	Rules     []RouteRule `json:"rules,omitempty"`
	// TLSHosts are the Ingress hosts that declare TLS (spec.tls[].hosts).
	// Only these serve HTTPS on 443, so the probe must not test 443 against a
	// plain-HTTP host - that would dial a port the Ingress doesn't serve and
	// report a false "unreachable" / cert failure.
	TLSHosts []string `json:"tlsHosts,omitempty"`

	// Gateway hops
	Listeners []GatewayListener `json:"listeners,omitempty"`
	Addresses []string          `json:"addresses,omitempty"`

	// Ingress hops: who serves this Ingress (the ingress controller). ServedBy
	// is a short pill label ("ingress-nginx"); ServedByTitle is its tooltip.
	// Quiet/healthy cases live here as a pill so they don't light up the hop as
	// a finding - only a controller PROBLEM (none / unready) is a finding.
	ServedBy      string `json:"servedBy,omitempty"`
	ServedByTitle string `json:"servedByTitle,omitempty"`
}

// PortMap describes a Service port mapping: the Service-side port + the
// targetPort on the pod side (numeric or named). Protocol defaults to TCP
// when omitted by the Service spec. AppProtocol (when set) is the
// authoritative L7 hint - drives whether the laptop-vantage probe can
// reach it via the HTTP-only API server proxy.
type PortMap struct {
	Name        string `json:"name,omitempty"`
	Port        int32  `json:"port"`
	TargetPort  string `json:"targetPort,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	AppProtocol string `json:"appProtocol,omitempty"`
}

// ContainerPortRef carries a named or unnamed container port observed on at
// PodStatus is one row of the Pods hop's per-pod reachability grid: a pod's
// own kubelet-reported readiness plus a plain-English cause when NotReady.
// Ready pods join their probe result by Name (apiserver path) or IP (data
// path); a NotReady pod has no probe (nothing to reach) and reads from Reason.
type PodStatus struct {
	Name   string `json:"name"`
	IP     string `json:"ip,omitempty"`
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
}

// least one of the Service's selected pods. The Container field names the
// container so multi-container pods stay readable.
type ContainerPortRef struct {
	Container string `json:"container"`
	Name      string `json:"name,omitempty"`
	Port      int32  `json:"port"`
	Protocol  string `json:"protocol,omitempty"`
}

// ProbeRef describes a single readiness/liveness/startup probe declaration
// on a Service-selected pod's container. Port is the resolved numeric port
// (string form to keep named/numeric symmetry with k8s intstr).
type ProbeRef struct {
	Container string `json:"container"`
	Type      string `json:"type"`
	Port      string `json:"port,omitempty"`
	Path      string `json:"path,omitempty"`
	Scheme    string `json:"scheme,omitempty"`
}

// RouteRule summarizes one rule on an Ingress or Gateway-API route: which
// hostnames/paths it matches and which backends it points at. The shape is
// deliberately union-compatible across Ingress and Gateway API so the UI can
// render one row per backend.
type RouteRule struct {
	Hosts    []string     `json:"hosts,omitempty"`
	Paths    []string     `json:"paths,omitempty"`
	Backends []BackendRef `json:"backends,omitempty"`
}

// BackendRef points at a route's destination - almost always a Service in
// scope, but Kind is included so the UI can show non-Service backends as
// "out of scope" without misrendering them as Services.
type BackendRef struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Port      string `json:"port,omitempty"`
}

// GatewayListener describes one listener on a Gateway: the port and
// protocol traffic enters on, plus the listener's name for cross-reference
// with route parentRefs.sectionName.
type GatewayListener struct {
	Name     string `json:"name,omitempty"`
	Port     int32  `json:"port"`
	Protocol string `json:"protocol,omitempty"`
	Hostname string `json:"hostname,omitempty"`
}

// Trace is the diagnosis. Downstream is the chain FROM the subject TOWARD
// pods; this is where BrokenAt applies. Upstreams are the parallel hops INTO
// the subject (e.g. multiple Ingresses pointing at one Service) - they are
// judged independently because a single broken Ingress does not condemn the
// other delivery paths.
type Trace struct {
	Subject    ResourceRef `json:"subject"`
	Upstreams  []Hop       `json:"upstreams"`
	Downstream []Hop       `json:"downstream"`
	Verdict    string      `json:"verdict"`
	BrokenAt   int         `json:"brokenAt"`
	// Reason explains an unknown/degraded verdict in a single sentence; the
	// UI shows it under the verdict banner without expansion.
	Reason string `json:"reason,omitempty"`
	// UnknownClass distinguishes the two flavors of an unknown verdict so
	// the UI can pick the right visual register. by-design covers Services
	// whose shape inherently can't be auto-verified (selectorless,
	// manually-managed endpoints) - these read as informational because
	// nothing is broken. investigate covers cases where the trace tried
	// and couldn't read state (cache cold, RBAC denied, lookup error,
	// transient API failure) - these read as warning because something
	// merits operator attention. Empty for non-unknown verdicts.
	UnknownClass string `json:"unknownClass,omitempty"`
	// Truncated is set when fan-out (e.g. Gateway attached routes) exceeded
	// the cap, so the UI can surface "showing N of M".
	Truncated bool `json:"truncated,omitempty"`
	// RunVantage names WHERE Radar itself runs, and so where its inline probes are
	// sent from: "in-cluster" (Radar is a pod - its direct probes are real
	// pod-to-pod, the in-cluster data path is already covered, and it CANNOT test
	// an external ingress front door) or "local" (a laptop - the direct probes are
	// the external client's view, and the in-cluster data path needs the manual
	// in-cluster Job). Set on every trace, probed or not: it is a deployment fact,
	// and the UI needs it to decide which vantages are offerable at all. The UI
	// labels columns, gates the in-cluster button, and words the verdict honestly
	// per architecture.
	RunVantage string `json:"runVantage,omitempty"`

	// Coverage-honest projection of the trace, computed server-side alongside
	// the verdict (computeCoverage). These describe ONLY what was actually
	// tested - the intended routes and their outcomes, plus the routes we
	// couldn't test and why. Localization (behind-the-gate / apiserver /
	// direct-pod facts) is kept on each RouteResult as a structurally separate
	// field so it can never feed the headline. Additive: consumers that read
	// Verdict/BrokenAt are unaffected.
	Coverage    *Coverage     `json:"coverage,omitempty"`
	Routes      []RouteResult `json:"routes,omitempty"`
	NotTested   []RouteSkip   `json:"notTested,omitempty"`
	BrokenRoute *ResourceRef  `json:"brokenRoute,omitempty"`
	// Headline is the single coverage-honest sentence both the UI banner and the
	// MCP summary render, set in computeCoverage so the two can never drift.
	Headline string `json:"headline,omitempty"`
	// Diagnosis hoists the one finding that matters - cause, the named culprit,
	// and the next action - by PROMOTING an existing path finding (never
	// synthesizing). Set in computeCoverage; nil when there is nothing to
	// diagnose (every route verified over real traffic). See Diagnosis.
	Diagnosis *Diagnosis `json:"diagnosis,omitempty"`
	// EntryProblems are faults on the resource's DECLARED ENTRY POINTS - an
	// Ingress or route that will never receive traffic. They are deliberately
	// NOT part of Diagnosis: upstreams are parallel, so one dead front door must
	// not condemn a Service that is genuinely reachable through another. But a
	// verdict scoped to the tested path can't see them either, which left a real
	// misconfiguration visible only as a dot inside a graph node. Vantage-
	// invariant (they are config facts) and deduped against Diagnosis.
	EntryProblems []EntryProblem `json:"entryProblems,omitempty"`
}

// EntryProblem is one declared entry point that cannot carry traffic, promoted
// from an existing upstream Finding - never synthesized.
type EntryProblem struct {
	Resource ResourceRef `json:"resource"`
	// Summary is the HUMAN line ("Not attached: no listener matches its hosts").
	// Deliberately Message-first, the opposite of Diagnosis: a diagnosis wants
	// the deeper cause, but a one-line row under the header wants the sentence a
	// non-expert can read - the raw controller condition goes in Detail, one
	// hover away.
	Summary string `json:"summary"`
	// Detail is the underlying cause (a controller condition, usually), shown on
	// hover. Empty when it would just repeat Summary.
	Detail   string `json:"detail,omitempty"`
	Severity string `json:"severity"`
	Code     string `json:"code,omitempty"`
	Action   string `json:"action,omitempty"`
	Command  string `json:"command,omitempty"`
}

// UnknownClass enumerates the two distinct flavors of an unknown verdict.
// Kept as string constants on the wire so the UI can switch on the value
// without sharing a typed enum across the language boundary.
const (
	UnknownClassByDesign    = "by-design"
	UnknownClassInvestigate = "investigate"
)

// Options shape what BuildTrace does beyond the static walk. Probe is opt-in
// because active checks cost wall time and generate traffic external systems
// can see - repeating them every poll cycle would create observability noise.
// Defaults are the cheapest sensible answer: static only.
type Options struct {
	// Probe controls active reachability checks. When true, BuildTrace
	// attaches probe.Result entries to each hop, labeled with the vantage
	// the binary is running from.
	Probe bool
	// ProbeBudget caps total wall time across all probes in this trace.
	// Zero means "use the package default" (3 seconds).
	ProbeBudget time.Duration
	// TotalBudget caps total wall time for the WHOLE diagnose (static build +
	// probes). Zero means the package default (3s). The probe phase runs within
	// the remaining budget - context propagation picks the earlier deadline - so
	// the whole call returns within TotalBudget on success, a hanging backend,
	// or an unknown/slow path alike.
	TotalBudget time.Duration
	// ProbePath is the HTTP path every L7 probe requests (default "/"). An
	// operator can point it at a health endpoint (e.g. "/healthz") via the
	// "what to test" form. Applies to the in-cluster Job probe too.
	ProbePath string
}

// defaultTotalBudget bounds the ENTIRE diagnose (static phase + probes). The
// probe budget runs inside it, so success / error-that-hangs / unknown all
// return within this. Sized to the same 3s the probe phase already used.
const defaultTotalBudget = 3 * time.Second

// BuildTraceWithOptions dispatches by entry kind. Kinds outside the accepted
// set return an empty trace with VerdictUnknown - the caller should validate
// input before reaching here; the guard exists only to avoid panics on
// frontend/MCP misuse. Callers that want a static-only trace pass Options{}.
func BuildTraceWithOptions(ctx context.Context, deps Deps, kind, namespace, name string, opts Options) (*Trace, error) {
	subject := ResourceRef{Kind: kind, Namespace: namespace, Name: name}

	if !cacheReady(deps) {
		return &Trace{
			Subject:      subject,
			RunVantage:   string(detectVantage()),
			Downstream:   []Hop{},
			Upstreams:    []Hop{},
			Verdict:      VerdictUnknown,
			BrokenAt:     -1,
			Reason:       "cache syncing - initial informer sync has not completed yet",
			UnknownClass: UnknownClassInvestigate,
		}, nil
	}

	// Bound the WHOLE call (static + probes). The entry handlers and the probe
	// phase honour this deadline, so the response returns within budget even on
	// a slow apiserver or a hanging backend.
	totalBudget := opts.TotalBudget
	if totalBudget <= 0 {
		totalBudget = defaultTotalBudget
	}
	ctx, cancel := context.WithTimeout(ctx, totalBudget)
	defer cancel()

	// Stamped before any probe runs. Where Radar sits is a deployment fact, not a
	// result, and the UI needs it on the static trace too: it decides which
	// vantages are even offerable, and a trace with no probes would otherwise
	// advertise a workstation vantage that a Radar running as a Pod can never use.
	t := &Trace{Subject: subject, BrokenAt: -1, RunVantage: string(detectVantage())}

	switch normalizeKind(kind) {
	case "Service":
		traceServiceEntry(ctx, deps, t)
	case "Ingress":
		traceIngressEntry(ctx, deps, t)
	case "HTTPRoute":
		traceRouteEntry(ctx, deps, t, "HTTPRoute")
	case "GRPCRoute":
		traceRouteEntry(ctx, deps, t, "GRPCRoute")
	case "Gateway":
		traceGatewayEntry(ctx, deps, t)
	default:
		t.Downstream = []Hop{}
		t.Upstreams = []Hop{}
		t.Verdict = VerdictUnknown
		t.Reason = "trace not supported for kind " + kind
		t.UnknownClass = UnknownClassInvestigate
		return t, nil
	}

	if t.Downstream == nil {
		t.Downstream = []Hop{}
	}
	if t.Upstreams == nil {
		t.Upstreams = []Hop{}
	}
	normalizeHopFindings(t)
	// If the total budget expired while building the static trace, the trace is
	// incomplete - say so honestly rather than computing a verdict over a
	// partial walk or hanging. A partial-unknown beats a confident wrong answer
	// or a stalled response.
	if ctx.Err() != nil {
		t.Verdict, t.BrokenAt = VerdictUnknown, -1
		t.Reason = "diagnosis timed out - partial result; try again or run in-cluster"
		t.UnknownClass = UnknownClassInvestigate
		computeCoverage(t)
		return t, nil
	}
	// Entry handlers may set Verdict directly (NotFound, RBAC denied,
	// missing API). Treat that as authoritative - computing "healthy" over
	// an empty Downstream after a failed resource lookup would be a lie.
	if t.Verdict == "" {
		t.Verdict, t.BrokenAt = computeVerdict(t)
		if t.Verdict == VerdictUnknown && t.Reason == "" {
			t.Reason = unknownReason(t)
		}
	}
	// Classify the unknown verdict once, after both the entry-handler path
	// and computeVerdict have run. Entry handlers set unknown on lookup
	// failures (NotFound, RBAC, lookup error) which are investigate-class.
	// computeVerdict's path downgrades degraded → unknown when endpoints
	// are unverifiable; that's where the by-design vs investigate split
	// matters.
	if t.Verdict == VerdictUnknown && t.UnknownClass == "" {
		t.UnknownClass = classifyUnknown(t)
	}

	// Probes augment the static verdict, never override it: a successful
	// probe against a statically broken trace could be an external
	// bystander. A hop whose every non-skipped probe failed is evidence
	// of a real break on that hop, so fold probe state into the verdict
	// after probes attach. Escalation only - broken stays broken (a
	// critical static finding outranks probe state) and unknown stays
	// unknown (an unverifiable path can't be honestly resolved by probe
	// outcomes).
	if opts.Probe {
		runProbes(ctx, t, opts, deps.Client)
		// The probe pass can reconcile a static finding away - e.g. a NetworkPolicy
		// would-deny that the live in-cluster probe disproved is downgraded to an
		// info note, or a targetPort "no listener" suspicion a live reach disproves.
		// Refresh the static verdict from the updated findings so a resolved warning
		// doesn't leave a stale 'degraded' behind (computeVerdict is pure over
		// findings and never returns unknown, so guard it out to keep an
		// unverifiable path honest).
		reconcileTargetPortAdvisory(t)
		if t.Verdict != VerdictUnknown {
			t.Verdict, t.BrokenAt = computeVerdict(t)
		}
		t.Verdict, t.BrokenAt = reviseVerdictWithProbes(t)
		// Honesty: a healthy verdict that wasn't FULLY verified (an HTTP 2xx on
		// a real path) must say what it actually proved, so neither the banner
		// nor the MCP output overclaims. Two distinct under-verifications:
		// (a) only the apiserver proxy returned 2xx - the route works via the
		//     proxy but the in-cluster data path wasn't exercised;
		// (b) only "reached" (3xx/4xx) or a port (TCP/TLS) - route unverified.
		if t.Verdict == VerdictHealthy && t.Reason == "" && anyProbeReached(t) && !hasVerifiedProbe(t) {
			if hasApiserverVerified(t) {
				if hasDataProbe(t) {
					t.Reason = "HTTP verified via API server; the in-cluster data path was confirmed at TCP only"
				} else {
					t.Reason = "verified via API server; the in-cluster data path wasn't tested"
				}
			} else {
				t.Reason = "reached a server but didn't verify the exact route (got 3xx/4xx, or only the port was open)"
			}
		}
	}
	// Coverage projection: server-authoritative view of what was actually
	// tested, computed over the internal verdict. Additive; it does not mutate
	// Verdict or BrokenAt itself.
	computeCoverage(t)
	// Collapse the shipped verdict to the single coverage-honest value so every
	// surface (the REST `verdict` field, the UI diagram, and the MCP response) reads
	// one answer. The internal computeVerdict and reviseVerdictWithProbes result
	// feeds this, but on its own it overclaims: a route reached only via the
	// apiserver proxy, or nothing tested at all, leaves Verdict=healthy while the
	// coverage tier honestly reads unknown. One source of truth, no per-surface drift.
	t.Verdict = CoverageVerdict(t)
	return t, nil
}

// anyProbeReached reports whether any hop carries a probe that actually REACHED
// a server (p.OK - a TCP/TLS port answered, or HTTP returned a status). A probe
// that FAILED (connection refused, cluster API down) has OK==false and does NOT
// count: otherwise the "reached a server but didn't verify" reason would fire on
// a path where nothing was reached at all (false-heal). Skipped probes never
// count either.
func anyProbeReached(t *Trace) bool {
	for _, h := range append(append([]Hop{}, t.Downstream...), t.Upstreams...) {
		for _, p := range h.Probes {
			if !p.Skipped && p.OK {
				return true
			}
		}
	}
	return false
}

// hasVerifiedProbe reports whether any probe FULLY verified a target - an HTTP
// 2xx on a REAL path (direct Ingress/Gateway dial or in-cluster data path).
// An apiserver-proxy 2xx does NOT count (it proves a backend answers, not the
// data path); "reached" (3xx/4xx) and transport-only (TCP/TLS) don't either.
// Mirrors probeChipFor's full-green rule so the chip and the reason agree.
func hasVerifiedProbe(t *Trace) bool {
	for _, h := range append(append([]Hop{}, t.Downstream...), t.Upstreams...) {
		for _, p := range h.Probes {
			if !p.Skipped && p.Layer == probe.LayerHTTP && p.OK && p.Tone == probe.ToneHealthy && p.Path != probe.PathAPIServer {
				return true
			}
		}
	}
	return false
}

// hasDataProbe reports whether any non-skipped probe ran on the in-cluster data
// path (a direct pod-to-pod/ClusterIP dial), so we can say it was confirmed at
// TCP rather than "not tested".
func hasDataProbe(t *Trace) bool {
	for _, h := range append(append([]Hop{}, t.Downstream...), t.Upstreams...) {
		for _, p := range h.Probes {
			if !p.Skipped && p.Path == probe.PathData {
				return true
			}
		}
	}
	return false
}

// hasApiserverVerified reports whether the only HTTP 2xx came through the
// apiserver proxy - the route works, but the in-cluster data path is untested.
func hasApiserverVerified(t *Trace) bool {
	for _, h := range append(append([]Hop{}, t.Downstream...), t.Upstreams...) {
		for _, p := range h.Probes {
			if !p.Skipped && p.Layer == probe.LayerHTTP && p.OK && p.Tone == probe.ToneHealthy && p.Path == probe.PathAPIServer {
				return true
			}
		}
	}
	return false
}

// reviseVerdictWithProbes folds attached probe outcomes into the verdict.
// A hop whose every non-skipped probe failed escalates to broken (same
// weight as a critical static finding); a hop with at least one failed or
// degraded non-skipped probe escalates to degraded. Broken and unknown
// verdicts are left alone - a critical static finding outranks probe state,
// and unknown means we can't honestly read the path either way.
func reviseVerdictWithProbes(t *Trace) (string, int) {
	if t.Verdict == VerdictBroken || t.Verdict == VerdictUnknown {
		return t.Verdict, t.BrokenAt
	}
	verdict := t.Verdict
	brokenAt := t.BrokenAt
	branches := downstreamBranches(t.Downstream)
	if len(branches) == 0 {
		// Single linear chain: a hop whose every real probe failed breaks it.
		for i, hop := range t.Downstream {
			failed, degraded, real := classifyHopProbes(hop)
			if real == 0 {
				continue
			}
			if failed == real {
				if brokenAt < 0 {
					brokenAt = i
				}
				verdict = VerdictBroken
			} else if hopVotesDegraded(failed, degraded, real) && verdict == VerdictHealthy {
				verdict = VerdictDegraded
			}
		}
	} else {
		// The entry hop (the Ingress/Gateway front door) is authoritative: if
		// its OWN probes failed unanimously, traffic can't even enter, so the
		// whole path is broken regardless of how healthy the backends look.
		// (computeVerdict does this for static findings; the probe revision
		// must too, or a reachable-backend + unreachable-entry reads "healthy".)
		ef, ed, er := classifyHopProbes(t.Downstream[0])
		// Only condemn the whole front door when every listener was testable and
		// failed. If some listeners were skipped (no-SNI, non-HTTP, wildcard host), a
		// failure on the tested ones is real evidence but does not prove the entry is
		// unreachable, since an untested listener may still carry traffic. Fall through
		// to the degraded path instead of overclaiming broken (V2/B3).
		if er > 0 && ef == er && !hopHasSkippedProbe(t.Downstream[0]) {
			t.Verdict, t.BrokenAt = VerdictBroken, 0
			if t.Reason == "" {
				t.Reason = "the entry is unreachable - traffic can't pass through it"
			}
			return VerdictBroken, 0
		}
		// A degraded or partially-failing entry (e.g. HTTP 5xx, or 1 of N host
		// probes failed) is real evidence too - fold it in after the backends
		// so a broken backend still takes precedence, but a degraded entry
		// upgrades a would-be-healthy verdict.
		entryDegraded := er > 0 && hopVotesDegraded(ef, ed, er)
		// Multi-backend entry: one route's probes failing breaks only that
		// route. Reserve broken for EVERY route broken; partial is degraded.
		probedBranches, brokenBranches, firstBroken, firstDegraded := 0, 0, -1, -1
		for _, b := range branches {
			broken, degraded := branchProbeVerdict(t.Downstream, b)
			if !broken && !degraded {
				if branchHasRealProbe(t.Downstream, b) {
					probedBranches++
				}
				continue
			}
			probedBranches++
			if broken {
				brokenBranches++
				if firstBroken < 0 {
					firstBroken = b.start
				}
			} else if firstDegraded < 0 {
				firstDegraded = b.start
			}
		}
		switch {
		case probedBranches > 0 && brokenBranches == probedBranches:
			verdict = VerdictBroken
			if brokenAt < 0 {
				brokenAt = firstBroken
			}
			if t.Reason == "" {
				t.Reason = fmt.Sprintf("all %d probed routes unreachable - traffic can't pass through this entry", probedBranches)
			}
		case brokenBranches > 0:
			if verdict != VerdictBroken {
				verdict = VerdictDegraded
			}
			if brokenAt < 0 {
				brokenAt = firstBroken
			}
			if t.Reason == "" {
				ref := t.Downstream[firstBroken].Resource
				t.Reason = fmt.Sprintf("%d of %d probed routes unreachable - backend %s %s failed; other routes still deliver", brokenBranches, probedBranches, ref.Kind, ref.Name)
			}
		case firstDegraded >= 0 && verdict == VerdictHealthy:
			verdict = VerdictDegraded
			if brokenAt < 0 {
				brokenAt = firstDegraded
			}
		}
		// A degraded entry front door upgrades a still-healthy verdict (a
		// broken/degraded backend above already took precedence).
		if entryDegraded && verdict == VerdictHealthy {
			verdict = VerdictDegraded
			if brokenAt < 0 {
				brokenAt = 0
			}
			if t.Reason == "" {
				t.Reason = "the entry hop is degraded"
			}
		}
	}
	if verdict != VerdictBroken {
		// Skip-only upstreams (e.g. Route hops that emit only a
		// "no probe target" skip) carry no evidence either way and
		// must not vote in the all-broken tally. Count only upstreams
		// that produced real probe results, and escalate to broken
		// when at least one real probe set ran AND every real-probe
		// upstream failed unanimously.
		realUpstreams := 0
		allRealUpstreamBroken := true
		anyUpstreamProbeIssue := false
		for _, hop := range t.Upstreams {
			failed, degraded, real := classifyHopProbes(hop)
			if real == 0 {
				continue
			}
			realUpstreams++
			if failed != real {
				allRealUpstreamBroken = false
			}
			if hopVotesDegraded(failed, degraded, real) {
				anyUpstreamProbeIssue = true
			}
		}
		if realUpstreams > 0 && allRealUpstreamBroken {
			verdict = VerdictBroken
			// The break is in the entry paths (upstreams), not the subject's
			// own downstream chain. Do NOT anchor brokenAt to downstream[0] -
			// that makes the banner say "Service X is broken" when the Service
			// and its Pods are healthy and only the Ingress/Gateway entry paths
			// failed. Leave brokenAt unanchored and name the real situation.
			if t.Reason == "" {
				t.Reason = "all entry paths into this resource are unreachable; the resource itself looks healthy"
			}
		} else if anyUpstreamProbeIssue && verdict == VerdictHealthy {
			verdict = VerdictDegraded
		}
	}
	return verdict, brokenAt
}

// hopVotesDegraded decides whether a hop's probe outcomes carry enough
// evidence to escalate the whole-trace verdict from healthy to degraded.
// A single failure on one replica of a many-replica hop is usually
// transient and shouldn't move the whole-path verdict; the per-hop chip
// still reflects the failure for follow-up. The threshold is
// "weighted failures >= real count," where each full failure counts as
// 2 and each degraded-tone response (HTTP 3xx/4xx) counts as 1, so:
//
//	1 of 1 failed → escalates (single-probe hop, unambiguous)
//	1 of 3 failed → no escalation (likely transient)
//	2 of 3 failed → escalates (majority)
//	every probe responded with a degraded tone → escalates
func hopVotesDegraded(failed, degraded, real int) bool {
	if real == 0 {
		return false
	}
	weighted := 2*failed + degraded
	return weighted >= real
}

// classifyHopProbes counts probe outcomes on a single hop, ignoring skipped
// rows. Returns (failed, degraded, totalCounted).
//
// Layers are DEPENDENT, not independent: a successful TCP is a prerequisite of
// the HTTP probe above it on the same target, so counting every layer equally
// lets a DNS+TCP success dilute a genuine HTTP 5xx ("server error") on that
// path - the banner reads "healthy" while the chip says "server error". HTTP is
// the authoritative application-reachability layer, so when any non-skipped
// HTTP probe ran the hop verdict is driven by the HTTP results (replica
// dilution still applies among them - 1 of N pods failing is transient);
// otherwise we fall back to the highest transport layer actually probed.
//
// Tone is load-bearing: HTTP 5xx carries tone=degraded with ok=true; 3xx/4xx
// carry tone=reached (neither fail nor degrade); transport failures are !ok.
//
// PATH is authoritative before layer: the real traffic path (pod-to-pod direct
// dial / Ingress-Gateway direct) wins over the apiserver proxy. An apiserver
// failure (RBAC denied, non-HTTP backend) must NOT condemn a hop whose data
// path succeeded - that divergence is surfaced as an info finding, not a broken
// verdict. From a laptop where only the apiserver path ran, it is the verdict.
// hopHasSkippedProbe reports whether any of the hop's probes was skipped: a
// listener or port that could not be tested from here (no SNI, non-HTTP, wildcard
// host). The entry-unreachable escalation guards on this so a partly-testable
// front door is not condemned whole on the strength of the listeners that were
// reachable.
func hopHasSkippedProbe(hop Hop) bool {
	for _, p := range hop.Probes {
		if p.Skipped {
			return true
		}
	}
	return false
}

func classifyHopProbes(hop Hop) (failed, degraded, real int) {
	var dataP, apiP, directP []probe.Result
	for _, p := range hop.Probes {
		if p.Skipped {
			continue
		}
		switch p.Path {
		case probe.PathData:
			dataP = append(dataP, p)
		case probe.PathAPIServer:
			apiP = append(apiP, p)
		default:
			directP = append(directP, p)
		}
	}
	// Direct (Ingress/Gateway) and data (in-cluster Service/Pod) are the real
	// paths; the apiserver proxy is the fallback used only when no real path
	// ran (laptop vantage). They don't co-occur on the same hop, so this is an
	// ordering, not a merge.
	counted := directP
	if len(counted) == 0 {
		counted = dataP
	}
	if len(counted) == 0 {
		// Only the apiserver-proxy path ran (laptop vantage, no real path). The
		// proxy bypasses the real dataplane (NetworkPolicy / mesh mTLS / internal
		// routing), so a proxy-only result must NEVER set the headline - it can
		// localize a symptom but can't escalate the verdict. Fail toward silence:
		// return no verdict vote (real=0). The coverage layer carries the honest
		// indirect outcome ("reached / unreachable via API server").
		return 0, 0, 0
	}
	// Within the chosen path, higher layers subsume lower (a successful TCP is
	// a prerequisite of HTTP on the same target), so a 5xx isn't diluted by its
	// own DNS/TCP. NOTE (known coarseness): this collapses by layer across the
	// whole hop, so a transport failure on a SECONDARY port (e.g. TCP:443
	// refused) can be hidden in the one-line verdict when a higher layer
	// succeeded on the primary port (HTTP:80 OK). The per-probe ROW always
	// shows that failure; only the summary verdict is coarse. A precise fix
	// needs a per-(host,port) target model.
	var http, tls, tcp, dns []probe.Result
	for _, p := range counted {
		switch p.Layer {
		case probe.LayerHTTP:
			http = append(http, p)
		case probe.LayerTLS:
			tls = append(tls, p)
		case probe.LayerTCP:
			tcp = append(tcp, p)
		default:
			dns = append(dns, p)
		}
	}
	layer := http
	if len(layer) == 0 {
		layer = tls
	}
	if len(layer) == 0 {
		layer = tcp
	}
	if len(layer) == 0 {
		layer = dns
	}
	for _, p := range layer {
		real++
		if !p.OK || p.Tone == probe.ToneUnhealthy {
			failed++
			continue
		}
		if p.Tone == probe.ToneDegraded {
			degraded++
		}
	}
	return
}

// normalizeHopFindings enforces two wire-format invariants before the trace
// leaves the package: Hop.Findings is always a non-nil slice (Go would
// marshal nil as JSON null, which the TS panel iterates as a crash), and
// findings on every hop are sorted worst-severity-first so consumers
// don't have to re-order.
func normalizeHopFindings(t *Trace) {
	for i := range t.Downstream {
		if t.Downstream[i].Findings == nil {
			t.Downstream[i].Findings = []Finding{}
		}
		sortFindingsBySeverity(t.Downstream[i].Findings)
	}
	for i := range t.Upstreams {
		if t.Upstreams[i].Findings == nil {
			t.Upstreams[i].Findings = []Finding{}
		}
		sortFindingsBySeverity(t.Upstreams[i].Findings)
	}
}

// computeVerdict walks Downstream first (BrokenAt indexes Downstream only),
// then folds Upstreams: an all-broken upstream set is a system-level break;
// a mixed set is only degraded because other parallel hops can still deliver.
func computeVerdict(t *Trace) (string, int) {
	verdict := VerdictHealthy
	brokenAt := -1
	branches := downstreamBranches(t.Downstream)
	if len(branches) == 0 {
		// Single linear chain (Service/Pod subject, or a one-backend entry):
		// any critical hop breaks the whole path - EXCEPT a backend that still has
		// ready endpoints (a per-pod crash among serving replicas is degraded, not
		// broken; see hopReachSeverity).
		for i, hop := range t.Downstream {
			switch hopReachSeverity(hop) {
			case SeverityCritical:
				if brokenAt < 0 {
					brokenAt = i
				}
				verdict = VerdictBroken
			case SeverityWarning:
				if verdict == VerdictHealthy {
					verdict = VerdictDegraded
				}
			}
		}
	} else {
		// Multi-backend entry (Ingress/Route): the entry hop is authoritative
		// - a critical there means nothing can enter, so the whole path is
		// broken. But a broken backend branch only breaks ITS route; other
		// routes still deliver, so partial breakage is degraded, not the
		// confident-wrong "traffic can't pass". Reserve broken for entry
		// failure or EVERY route broken.
		// A per-backend missing-ref (a route pointing at a Service that doesn't
		// exist) is reported as a critical on the ENTRY hop, but it only breaks
		// THAT route - so split it out: it counts as a broken route below, not
		// a total-entry failure. True entry-level criticals (no controller
		// address, host unresolvable) remain authoritative.
		var entryReal []Finding
		entryMissingRefBroken := 0
		for _, f := range t.Downstream[0].Findings {
			// Only a CRITICAL missing-ref (route → nonexistent Service) is split out
			// as a broken route. A warning-severity missing-ref (e.g. a missing TLS
			// secret, where the controller falls back to the default cert) is a real
			// entry-level degrade - keep it in entryReal so worstSeverity degrades.
			if strings.HasPrefix(f.Code, missingRefCodePrefix) && f.Severity == SeverityCritical {
				entryMissingRefBroken++
				continue
			}
			entryReal = append(entryReal, f)
		}
		switch worstSeverity(entryReal) {
		case SeverityCritical:
			verdict = VerdictBroken
			brokenAt = 0
		case SeverityWarning:
			verdict = VerdictDegraded
		}
		if verdict != VerdictBroken {
			// Drained (weight-0) backends carry no traffic by design - exclude them
			// from both the failure tally and the route denominator so a down drained
			// backend neither counts as broken nor blocks the "all routes broken"
			// verdict for the routes that DO carry traffic.
			liveBranches := branches[:0:0]
			for _, b := range branches {
				if b.start < len(t.Downstream) && isDrainedBackendHop(t.Downstream[b.start]) {
					continue
				}
				liveBranches = append(liveBranches, b)
			}
			broken, firstBroken, firstDegraded := entryMissingRefBroken, -1, -1
			for _, b := range liveBranches {
				switch branchSeverity(t.Downstream, b) {
				case SeverityCritical:
					broken++
					if firstBroken < 0 {
						firstBroken = b.start
					}
				case SeverityWarning:
					if firstDegraded < 0 {
						firstDegraded = b.start
					}
				}
			}
			if broken > len(liveBranches) {
				broken = len(liveBranches)
			}
			// Anchor on the first broken backend branch when there is one; an
			// entry-expressed missing-ref break has its descriptive finding on
			// the entry row, so fall back to that.
			brokenAnchor := firstBroken
			if brokenAnchor < 0 && entryMissingRefBroken > 0 {
				brokenAnchor = 0
			}
			switch {
			case broken > 0 && broken == len(liveBranches):
				// Every traffic-carrying route is broken - traffic genuinely can't pass.
				verdict = VerdictBroken
				brokenAt = brokenAnchor
				if t.Reason == "" {
					t.Reason = fmt.Sprintf("all %d routes broken - traffic can't pass through this entry", len(liveBranches))
				}
			case broken > 0:
				// Some routes broken, others deliver: degraded, anchored on the
				// broken route so the UI highlights the real culprit instead of
				// crying "broken" over a path that still mostly works.
				verdict = VerdictDegraded
				brokenAt = brokenAnchor
				if t.Reason == "" {
					t.Reason = fmt.Sprintf("%d of %d routes broken - other routes still deliver", broken, len(liveBranches))
				}
			case firstDegraded >= 0:
				if verdict == VerdictHealthy {
					verdict = VerdictDegraded
				}
				if brokenAt < 0 {
					brokenAt = firstDegraded
				}
			}
		}
	}

	if len(t.Upstreams) > 0 {
		brokenUp := 0
		warnUp := 0
		for _, hop := range t.Upstreams {
			// A missing-backend ref on an upstream entry is about a DIFFERENT
			// route than the one reaching this subject - the subject is itself
			// a backend of that entry, so it demonstrably exists. Such a finding
			// must not degrade this subject (e.g. an Ingress with /web→here and
			// /api→ghost: the broken /api doesn't make `here` unhealthy).
			switch worstSeverity(nonMissingRefFindings(hop.Findings)) {
			case SeverityCritical:
				brokenUp++
			case SeverityWarning:
				warnUp++
			}
		}
		if brokenUp == len(t.Upstreams) && verdict != VerdictBroken {
			verdict = VerdictBroken
			// All upstream entries are broken: traffic can't reach the subject at
			// all. The break is in the entry paths, not the subject's own chain -
			// mirror the probe path (reviseVerdictWithProbes) and do NOT anchor
			// brokenAt to the healthy subject row, which would make the banner say
			// "Service X is broken" while the Service and its Pods are clean. Name
			// the real situation instead.
			if t.Reason == "" {
				t.Reason = "all entry paths into this resource are unreachable; the resource itself looks healthy"
			}
		} else if (brokenUp > 0 || warnUp > 0) && verdict == VerdictHealthy {
			verdict = VerdictDegraded
		}
	}

	// A trace can't honestly be called healthy *or* degraded when endpoint
	// resolution itself was unverifiable. Selectorless Services (manual
	// endpoints, no proof a target is wired up) and any hop that flagged
	// endpointSource=unknown (pod lister failed; RBAC or cache sync the
	// likely cause) downgrade to unknown - including over a degraded
	// verdict, since we can't be confident the warning describes the
	// whole story. Broken stays broken: a critical finding is a known
	// problem the operator can act on regardless of endpoint visibility.
	if verdict != VerdictBroken && hasUnverifiableEndpoints(t) {
		verdict = VerdictUnknown
	}

	return verdict, brokenAt
}

// classifyUnknown decides whether an unknown verdict is by-design (the
// resource shape inherently can't be auto-verified, e.g. selectorless
// Service) or investigate (the trace tried and couldn't read state - RBAC
// denied, lister error, cache cold, lookup failure). The two classes ride
// the same verdict word but get different visual register in the UI:
// by-design reads as informational; investigate reads as warning.
// When the trace contains both signals, investigate wins - a cold cache
// behind a selectorless Service still wants operator attention.
func classifyUnknown(t *Trace) string {
	hasByDesign := false
	for _, hop := range allHops(t) {
		if hop.Meta == nil {
			continue
		}
		if src, _ := hop.Meta["endpointSource"].(string); src == "unknown" {
			return UnknownClassInvestigate
		}
		if v, _ := hop.Meta["selectorless"].(bool); v {
			hasByDesign = true
		}
	}
	if hasByDesign {
		return UnknownClassByDesign
	}
	return UnknownClassInvestigate
}

// allHops yields every hop in the trace (downstream + upstreams) so the
// unverifiability scans see signals attached anywhere in the path. The
// downstream-only walk dropped markers placed on upstream Gateway hops
// (the route → parent-Gateway path) and left the verdict reading clean
// while the trace explicitly carried an unverifiable hop.
// isDrainedBackendHop reports whether a backend hop is an explicit weight-0
// (drained / canary-cutover) Gateway-API backend - traced informationally and
// excluded from the verdict/coverage failure tally.
func isDrainedBackendHop(h Hop) bool {
	v, _ := h.Meta["drained"].(bool)
	return v
}

func allHops(t *Trace) []Hop {
	out := make([]Hop, 0, len(t.Downstream)+len(t.Upstreams))
	out = append(out, t.Downstream...)
	out = append(out, t.Upstreams...)
	return out
}

func hasUnverifiableEndpoints(t *Trace) bool {
	for _, hop := range allHops(t) {
		if hop.Meta == nil {
			continue
		}
		if v, _ := hop.Meta["selectorless"].(bool); v {
			return true
		}
		if src, _ := hop.Meta["endpointSource"].(string); src == "unknown" {
			return true
		}
	}
	return false
}

// unknownReason names the dominant cause for an unknown verdict so the
// banner can be specific instead of "we don't know".
func unknownReason(t *Trace) string {
	// Match classifyUnknown's precedence: endpointSource=="unknown" (investigate)
	// wins over selectorless (by-design). Scanning selectorless first would pair
	// the investigate visual register with the benign by-design sentence.
	for _, hop := range allHops(t) {
		if hop.Meta == nil {
			continue
		}
		if src, _ := hop.Meta["endpointSource"].(string); src == "unknown" {
			return "Backing state couldn't be read on at least one hop. RBAC may be blocking the listing, or the cache is still syncing."
		}
	}
	for _, hop := range allHops(t) {
		if hop.Meta == nil {
			continue
		}
		if v, _ := hop.Meta["selectorless"].(bool); v {
			return "Service has no selector. Endpoints are managed manually, so we can't confirm a target is reachable."
		}
	}
	return "Path can't be verified from declared config alone."
}

// branchSpan is one backend branch in the flat downstream: the Service hop and
// the optional Pods hop that follows it. [start, end).
type branchSpan struct{ start, end int }

// downstreamBranches segments a flat downstream into backend branches. A hop
// whose Edge ends in "->Service" opens a branch; a following "Service->Pods"
// hop joins it. The entry hop (index 0) is never a branch, and a Service/Pod
// subject (no "->Service" hop) yields no branches - callers fall back to flat
// single-chain handling. This lets a multi-backend Ingress/Route be judged
// per-route without any tree or new Hop field: the Edge labels the fan-out
// already writes are the grouping key.
func downstreamBranches(hops []Hop) []branchSpan {
	var spans []branchSpan
	for i := 1; i < len(hops); i++ {
		switch {
		case strings.HasSuffix(hops[i].Edge, "->Service") && hops[i].Resource.Name != "":
			// An Ingress/Route backend: the Service hop + its optional Pods hop.
			// The name guard excludes the anonymous cross-namespace-redacted
			// aggregate hop: opening a branch for it would manufacture a blank
			// route row (and, with probes, a false-verified one from the entry's
			// front-door dials). Its rbac finding is the honest disclosure.
			end := i + 1
			if end < len(hops) && hops[end].Edge == "Service->Pods" {
				end++
			}
			spans = append(spans, branchSpan{i, end})
		case strings.HasPrefix(hops[i].Edge, "Gateway->") && hops[i].Resource.Name != "":
			// A Gateway attached route (Gateway->HTTPRoute/GRPCRoute/...): each
			// is a parallel entry path, so one broken route degrades - it
			// doesn't break the whole Gateway. The synthetic "Gateway->Routes"
			// truncation hop carries no name and is excluded.
			spans = append(spans, branchSpan{i, i + 1})
		}
	}
	return spans
}

// branchContaining returns the backend branch whose [start,end) span contains
// the given downstream index, if any. Single-chain traces (no branches) return
// false so the caller keeps its flat fallback.
func branchContaining(hops []Hop, idx int) (branchSpan, bool) {
	for _, b := range downstreamBranches(hops) {
		if idx >= b.start && idx < b.end {
			return b, true
		}
	}
	return branchSpan{}, false
}

// missingRefCodePrefix marks a finding about a route's backend Service that
// doesn't exist. Derived from the issues Source so it tracks any rename there;
// the UI mirrors the resulting literal to scope upstream rows, pinned by
// TestMissingRefPrefixMirrored.
var missingRefCodePrefix = string(issues.SourceMissingRef) + ":"

// IsMissingRefCode reports whether a finding code marks a missing-backend ref -
// a specific route's backend Service that doesn't exist. Exported so the MCP
// upstream scoping reuses the one definition instead of mirroring the prefix.
func IsMissingRefCode(code string) bool {
	return strings.HasPrefix(code, missingRefCodePrefix)
}

// nonMissingRefFindings drops missing-backend-ref findings. They describe a
// specific route's backend that doesn't exist; on an UPSTREAM entry hop that
// is never the route reaching the current subject (which exists as a backend),
// so it must not fold into the subject's verdict.
func nonMissingRefFindings(fs []Finding) []Finding {
	out := fs[:0:0]
	for _, f := range fs {
		if strings.HasPrefix(f.Code, missingRefCodePrefix) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// branchSeverity is the worst finding severity across a branch's hops.
func branchSeverity(hops []Hop, b branchSpan) string {
	worst := ""
	for i := b.start; i < b.end && i < len(hops); i++ {
		if s := hopReachSeverity(hops[i]); severityRank(s) > severityRank(worst) {
			worst = s
		}
	}
	return worst
}

// hopHasReadyEndpoints reports whether a backend hop still has ready endpoints
// (meta ready > 0) - i.e. the Service can serve traffic to the ready pods.
func hopHasReadyEndpoints(hop Hop) bool {
	r, ok := hop.Meta["ready"].(int)
	return ok && r > 0
}

// hopReachSeverity returns a hop's severity for the REACHABILITY verdict, capping
// a critical at warning when the hop still has READY endpoints. A backend with
// live endpoints IS reachable - traffic flows to the ready pods - so a per-pod
// critical (one replica crashlooping while siblings serve) is a DEGRADATION, not
// a break. Without this a 1-of-2-ready Service false-condemns as broken/unreachable
// even though it serves. A 0-ready hop keeps its critical (genuinely no backend).
func hopReachSeverity(hop Hop) string {
	sev := worstSeverity(hop.Findings)
	if sev == SeverityCritical && hopHasReadyEndpoints(hop) {
		return SeverityWarning
	}
	return sev
}

// branchHasRealProbe reports whether any hop in the branch produced a
// non-skipped probe result (so an all-skipped branch doesn't count as "probed").
func branchHasRealProbe(hops []Hop, b branchSpan) bool {
	for i := b.start; i < b.end && i < len(hops); i++ {
		if _, _, real := classifyHopProbes(hops[i]); real > 0 {
			return true
		}
	}
	return false
}

// branchProbeVerdict folds a branch's hop probes: broken when every hop with
// real probes failed unanimously; degraded when any hop votes degraded.
func branchProbeVerdict(hops []Hop, b branchSpan) (broken, degraded bool) {
	anyReal, allFailed := false, true
	for i := b.start; i < b.end && i < len(hops); i++ {
		failed, deg, real := classifyHopProbes(hops[i])
		if real == 0 {
			continue
		}
		anyReal = true
		if failed != real {
			allFailed = false
		}
		if hopVotesDegraded(failed, deg, real) {
			degraded = true
		}
	}
	if !anyReal {
		return false, false
	}
	if allFailed {
		return true, false
	}
	return false, degraded
}

func worstSeverity(fs []Finding) string {
	worst := ""
	for _, f := range fs {
		switch f.Severity {
		case SeverityCritical:
			return SeverityCritical
		case SeverityWarning:
			worst = SeverityWarning
		case SeverityInfo:
			if worst == "" {
				worst = SeverityInfo
			}
		}
	}
	return worst
}

func cacheReady(deps Deps) bool {
	if deps.Cache == nil {
		return false
	}
	// Listers return nil when their informer hasn't synced yet; the Services
	// lister is the cheapest readiness probe for trace work since every entry
	// kind needs it.
	if deps.Cache.Services() == nil {
		return false
	}
	return true
}

// IsEntryKind returns true if the given kind (any plural/lowercase form) is
// one of the network entry kinds the trace surface supports. Used by the
// server's input gate and the trace dispatcher; keeping them aligned on one
// helper prevents the "added a kind in one place, forgot the other" bug.
func IsEntryKind(kind string) bool {
	switch normalizeKind(kind) {
	case "Service", "Ingress", "HTTPRoute", "GRPCRoute", "Gateway":
		return true
	}
	return false
}

func normalizeKind(k string) string {
	switch strings.ToLower(k) {
	case "service", "services":
		return "Service"
	case "ingress", "ingresses":
		return "Ingress"
	case "httproute", "httproutes":
		return "HTTPRoute"
	case "grpcroute", "grpcroutes":
		return "GRPCRoute"
	case "gateway", "gateways":
		return "Gateway"
	}
	return k
}
