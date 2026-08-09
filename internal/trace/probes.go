package trace

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/pkg/probe"
)

const (
	defaultProbeBudget = 3 * time.Second
	dnsTimeout         = 250 * time.Millisecond
	tcpTimeout         = 700 * time.Millisecond
	tlsTimeout         = time.Second
	httpTimeout        = time.Second
)

// detectVantage is an internal alias so the trace package can mock vantage
// in tests via package-level override without touching pkg/probe's API.
var detectVantage = probe.DetectVantage

// SanitizeHTTPPath normalizes an operator-chosen HTTP request path just enough
// to be a safe request target: trim surrounding whitespace, strip CR/LF and
// other control characters (header/request-line injection), and ensure a
// leading '/'. Empty/whitespace-only input returns "" so callers keep their
// own no-path semantics. Everything else - trailing slashes, repeated slashes,
// dot-segments, query strings - is preserved verbatim: an HTTP path is an
// opaque route key, not a filesystem path, and "cleaning" it makes the request
// something the operator didn't ask for (/api/v1/ and /api/v1 are different
// resources to an APPEND_SLASH-style server, and path.Clean would rewrite a
// query string containing "..") - producing false failures on healthy
// backends. Exported as the one sanitizer for probe paths; the in-cluster
// runner (internal/reachability) delegates to it.
func SanitizeHTTPPath(p string) string {
	p = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(p))
	if p == "" {
		return ""
	}
	if p[0] != '/' {
		p = "/" + p
	}
	return p
}

// httpPath is SanitizeHTTPPath with the trace probes' default: an empty path
// means probe the root. Every L7 probe requests this path.
func httpPath(p string) string {
	if s := SanitizeHTTPPath(p); s != "" {
		return s
	}
	return "/"
}

// serviceProxyProbe and podProxyProbe are call-site indirections so tests
// can stub the apiserver-proxy path without exercising client-go's REST
// stack (the typed fake client returns a non-nil RESTClient interface
// holding a nil pointer, which panics deep inside client-go).
var (
	serviceProxyProbe = probe.ServiceProxy
	podProxyProbe     = probe.PodProxy
)

// ingressDNSProbe/ingressTCPProbe/ingressTLSProbe/ingressHTTPProbe are
// call-site indirections (same pattern as serviceProxyProbe) so tests can
// drive probeIngress's full emission shape without real listeners: the SSRF
// dial guard refuses loopback targets and :80/:443 are privileged ports, so a
// hermetic test can't bind real ones.
var (
	ingressDNSProbe  = probe.DNS
	ingressTCPProbe  = probe.TCP
	ingressTLSProbe  = probe.TLS
	ingressHTTPProbe = probe.HTTP
)

// runProbes augments the static trace with reachability probes, sized to
// the caller's budget. Hops are probed in parallel because they target
// independent resources - a slow Gateway listener shouldn't delay the
// Service hop's TCP probe. Within each hop, probes stay sequential because
// later layers depend on earlier ones (TLS only matters if TCP succeeded).
// The overall budget enforces that even a misbehaving fanout cannot exceed
// the operator's patience.
func runProbes(ctx context.Context, t *Trace, opts Options, client kubernetes.Interface) {
	if t == nil || ctx.Err() != nil {
		return
	}
	budget := opts.ProbeBudget
	if budget <= 0 {
		budget = defaultProbeBudget
	}
	deadline := time.Now().Add(budget)
	probeCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	vantage := detectVantage()
	t.RunVantage = string(vantage)
	// The HTTP path every L7 probe requests. Defaults to "/"; an operator can
	// point it at a health endpoint (e.g. /healthz) via the "what to test" form.
	path := httpPath(opts.ProbePath)

	var wg sync.WaitGroup
	probeHops := func(hops []Hop) {
		for i := range hops {
			i := i
			// A drained (weight-0) backend serves no traffic by design - dialing it
			// would waste budget and could attach a misleading failure. Leave it
			// unprobed; buildRoutes folds it in as a benign skip.
			if drained, _ := hops[i].Meta["drained"].(bool); drained {
				continue
			}
			if time.Now().After(deadline) {
				hops[i].Probes = append(hops[i].Probes, probe.Skipped(
					probe.LayerTCP, "", vantage, "the test ran out of time before reaching this hop",
				))
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				hops[i].Probes = probeHop(probeCtx, &hops[i], vantage, client, path)
			}()
		}
	}
	probeHops(t.Downstream)
	probeHops(t.Upstreams)
	wg.Wait()

	// Dual-path divergence is the most operator-actionable signal the active
	// layer produces: when an in-cluster probe succeeds via one path and
	// fails via the other, the failure is isolated to that path's subsystem
	// (NetworkPolicy / kube-proxy / sidecar vs apiserver / RBAC).
	attachPathDivergenceFindings(t)

	// Reconcile the static NetworkPolicy prior against what live traffic
	// actually did. A static "would deny" is only a prior - kindnet creates the
	// policy object but enforces nothing - so it must be downgraded when live
	// traffic got through, and only confirmed when the data path was dropped.
	reconcilePolicyFindings(t)
}

// reconcilePolicyFindings settles the static NetworkPolicy would-deny prior
// against the live probe outcome on each Pods hop:
//   - live data path got through → the rule isn't enforced here; downgrade to a
//     reassuring info note (the kindnet / no-policy-CNI case).
//   - live data path was dropped while the apiserver path passed → confirmed;
//     fold the policy name into the divergence finding and drop the prior so the
//     hop doesn't surface the same cause twice.
//   - no live data path probed (laptop, no in-cluster run) → leave the warning
//     and its "check manually" fallback intact.
func reconcilePolicyFindings(t *Trace) {
	visit := func(hops []Hop) {
		for i := range hops {
			h := &hops[i]
			if h.Meta == nil || h.Meta["policyVerdict"] != "would-deny" {
				continue
			}
			idx := findingIndexByCode(h.Findings, codePolicyWouldDeny)
			if idx < 0 {
				continue
			}
			port := metaInt32(h.Meta["policyDenyPort"])
			o := portScopedOutcome(h.Probes, port)
			names := metaStrings(h.Meta["policyNames"])
			switch {
			case o.dataOK && !o.dataFail && port > 0:
				// Real in-cluster traffic on the DENIED port got through UNANIMOUSLY.
				// The apiserver proxy bypasses NetworkPolicy, but the DATA path does
				// not - a clean data-path success is ground truth the prediction
				// didn't hold. Gated on a KNOWN deny port: with port==0 the success
				// could be on an unrelated port (portScopedOutcome falls back to all
				// probes), so we must not over-reassure - keep the prediction.
				// Mixed (some pods dropped) also falls through and stays a prediction.
				h.Findings[idx] = Finding{
					Code:     codePolicyWouldDeny,
					Severity: SeverityInfo,
					Message:  "Traffic got through. A network rule here would block it - but either this cluster's network plugin doesn't enforce NetworkPolicy, or the static read missed an allow rule for this port.",
					Command:  h.Findings[idx].Command,
				}
			case o.dataFail && !o.dataOK && o.apiOK && !o.apiFail:
				// CLEAN divergence on the SAME port: the in-cluster data path
				// dropped unanimously while the apiserver proxy (which bypasses the
				// rule) reached it unanimously. That isolates the drop to the
				// in-cluster path - the rule is the confirmed cause. A mixed
				// apiserver result is NOT a clean divergence, so don't confirm on it.
				if enrichDivergenceWithPolicy(h, names) {
					h.Findings = append(h.Findings[:idx], h.Findings[idx+1:]...)
				} else {
					h.Findings[idx].Cause = fmt.Sprintf("In-cluster traffic on this port was dropped while the API-proxy check (which bypasses network rules) reached it - consistent with the rule (%s) being the cause (the data-path drop could also be kube-proxy or a sidecar).", strings.Join(names, ", "))
				}
			case o.dataFail:
				// Data path failed but there was no clean divergence (the apiserver
				// path also failed, or results were mixed) - the backend may simply
				// be down or not listening. Don't assert the rule; keep the
				// prediction, note it's consistent-but-unconfirmed.
				h.Findings[idx].Cause = fmt.Sprintf("Consistent with a network rule (%s) denying this port - but the backend could also be down or not listening. Run the in-cluster test to tell them apart.", strings.Join(names, ", "))
			}
			sortFindingsBySeverity(h.Findings)
		}
	}
	visit(t.Downstream)
	visit(t.Upstreams)
}

type probeOutcome struct {
	dataOK, dataFail bool
	apiOK, apiFail   bool
}

// portScopedOutcome reports the data-path and apiserver-path probe results for
// a hop, filtered to the policy's denied port when known (port > 0). Filtering
// matters on a multi-port backend: another port's success must not downgrade a
// real block on this one. When port is 0 (couldn't pin a single port), it falls
// back to all of the hop's probes. Skipped rows don't count either way.
func portScopedOutcome(probes []probe.Result, port int32) probeOutcome {
	var o probeOutcome
	for _, p := range probes {
		if p.Skipped {
			continue
		}
		if port > 0 && p.Port != port {
			continue
		}
		switch p.Path {
		case probe.PathData:
			if p.OK {
				o.dataOK = true
			} else {
				o.dataFail = true
			}
		case probe.PathAPIServer:
			if p.OK {
				o.apiOK = true
			} else {
				o.apiFail = true
			}
		}
	}
	return o
}

func metaInt32(v any) int32 {
	switch n := v.(type) {
	case int32:
		return n
	case int:
		return int32(n)
	case int64:
		return int32(n)
	case float64:
		return int32(n)
	}
	return 0
}

// enrichDivergenceWithPolicy replaces the generic "usual causes" cause on a
// data-path-only-broken finding with the named policy, wording it as
// "consistent with" rather than asserting NetworkPolicy is the cause (the live
// drop confirms a drop, not which subsystem dropped it). Returns false when
// the hop has no divergence finding to enrich.
func enrichDivergenceWithPolicy(h *Hop, names []string) bool {
	idx := findingIndexByCode(h.Findings, "probe:data-path-only-broken")
	if idx < 0 {
		return false
	}
	h.Findings[idx].Cause = fmt.Sprintf("In-cluster traffic on this port was dropped while the API-proxy check (which bypasses network rules) reached it - consistent with the rule (%s) being the cause (the data-path drop could also be kube-proxy or a sidecar).", strings.Join(names, ", "))
	h.Findings[idx].Action = "Confirm the rule and add an ingress rule that allows this port if the traffic is intended."
	return true
}

// budgetSkipIfExhausted converts a FAILED direct-dial result into a budget skip
// when the overall probe budget (the parent ctx) expired during the dial - a
// context-deadline error from budget exhaustion is not a real "unreachable", so
// it must degrade to a skip rather than a red row. A genuine per-probe timeout
// (parent ctx still alive, only the per-probe ctx fired) passes through
// unchanged so it still reads as real evidence.
func budgetSkipIfExhausted(ctx context.Context, r probe.Result, vantage probe.Vantage) probe.Result {
	if r.OK || ctx.Err() == nil {
		return r
	}
	skip := probe.SkippedCmd(r.Layer, r.Target, vantage, "the test ran out of time before this check finished", "")
	skip.Path = r.Path
	skip.Port = r.Port
	return skip
}

func findingIndexByCode(findings []Finding, code string) int {
	for i := range findings {
		if findings[i].Code == code {
			return i
		}
	}
	return -1
}

func metaStrings(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// attachPathDivergenceFindings walks every hop and, when the in-cluster
// data path and the apiserver path produced contradictory results for the
// same target, attaches a Finding that names which path failed and points
// at the likely subsystems to investigate. The finding flows through the
// same Finding pipeline the rest of the UI already renders.
//
// Hop findings are re-sorted worst-first after the append: the UI treats
// findings[0] as the hop's overall severity, so a freshly-appended warning
// divergence row mustn't sit after a pre-existing info finding.
func attachPathDivergenceFindings(t *Trace) {
	visit := func(hops []Hop) {
		for i := range hops {
			extras := pathDivergenceFindings(hops[i].Probes)
			if len(extras) == 0 {
				continue
			}
			hops[i].Findings = append(hops[i].Findings, extras...)
			sortFindingsBySeverity(hops[i].Findings)
		}
	}
	visit(t.Downstream)
	visit(t.Upstreams)
}

// pathDivergenceFindings inspects one hop's probe results and emits one
// Finding per port where the data-path and apiserver-path verdicts
// disagree. Same-port same-result rows are silent. Returns an empty slice
// when there's no divergence to flag.
//
// Bucket key is the port - the two paths label their targets differently
// ("10.0.0.5:80" vs "port 80"), but a hop is one logical resource, so the
// trailing port number is enough to pair up data-path vs apiserver-path
// results that refer to the same backend.
//
// Buckets are visited in sorted-key order so output is deterministic; map
// iteration would otherwise hide some divergences and randomise which one
// surfaces between requests.
//
// On a multi-replica Pods hop, several probes share a port key. Mixed
// results on one side (one pod OK, one pod fail) are NOT divergence -
// that's a partial-fleet failure the per-row severities already surface.
// Divergence requires unanimous failure on one side and unanimous success
// on the other for the same port; anything else stays silent.
func pathDivergenceFindings(probes []probe.Result) []Finding {
	type sides struct {
		dataOK, dataFail int
		apiOK, apiFail   int
	}
	by := map[string]*sides{}
	for _, p := range probes {
		if p.Skipped || p.Target == "" {
			continue
		}
		key := portKey(p.Target)
		if key == "" {
			continue
		}
		s := by[key]
		if s == nil {
			s = &sides{}
			by[key] = s
		}
		switch p.Path {
		case probe.PathData:
			if p.OK {
				s.dataOK++
			} else {
				s.dataFail++
			}
		case probe.PathAPIServer:
			if p.OK {
				s.apiOK++
			} else {
				s.apiFail++
			}
		}
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Finding
	for _, k := range keys {
		s := by[k]
		dataAllFail := s.dataFail > 0 && s.dataOK == 0
		dataAllOK := s.dataOK > 0 && s.dataFail == 0
		apiAllFail := s.apiFail > 0 && s.apiOK == 0
		apiAllOK := s.apiOK > 0 && s.apiFail == 0
		if dataAllFail && apiAllOK {
			out = append(out, Finding{
				Code:     "probe:data-path-only-broken",
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("A direct workload-to-workload connection on port %s failed, but the Kubernetes API server reached the same target. Real in-cluster traffic to this port may be blocked even though the check through the API server passed.", k),
				Cause:    "The failure is on the direct in-cluster route only. The usual causes are a NetworkPolicy, an unhealthy kube-proxy on the receiving node, or a service-mesh sidecar intercepting the port.",
				Action:   "Check NetworkPolicies in the namespace, kube-proxy health on the receiving node, and any service-mesh sidecar that may be intercepting the port.",
			})
			continue
		}
		if dataAllOK && apiAllFail {
			out = append(out, Finding{
				Code:     "probe:apiserver-path-only-broken",
				Severity: SeverityInfo,
				Message:  fmt.Sprintf("A direct workload-to-workload connection on port %s succeeded, but Radar's check through the Kubernetes API server could not reach the same target. Real workload traffic is probably fine - this usually reflects a limit of testing through the API server, not an outage.", k),
				Cause:    "The API server's proxy was refused, or the port speaks a protocol the proxy can't relay (it only relays HTTP).",
				Action:   "This usually needs no action. To make the API-server check work, confirm your identity has get services/proxy or get pods/proxy in this namespace, and that the port serves HTTP.",
			})
		}
	}
	return out
}

// probeHop dispatches by hop kind. The primitive used depends on what works
// from the current vantage - in-cluster gets direct TCP for ClusterIPs;
// local falls back to the Kubernetes API server proxy when a kubeconfig is
// available. The user never sees this distinction at the primary level;
// it surfaces only in the Detail tag of each result.
func probeHop(ctx context.Context, h *Hop, vantage probe.Vantage, client kubernetes.Interface, path string) []probe.Result {
	switch h.Resource.Kind {
	case "Service":
		return probeService(ctx, h, vantage, client, path)
	case "Pods":
		return probePods(ctx, h, vantage, client, path)
	case "ExternalName":
		return probeExternalName(ctx, h, vantage, path)
	case "Ingress":
		return probeIngress(ctx, h, vantage, path)
	case "Gateway":
		return probeGateway(ctx, h, vantage, path)
	case "HTTPRoute", "GRPCRoute":
		// Routes don't carry their own routable address; reachability
		// belongs on the Gateway listener and on the backend Service.
		return []probe.Result{probe.Skipped(probe.LayerTCP, "", vantage,
			"route has no own address; reachability lives on parent Gateway and backend Service")}
	case "TCPRoute", "TLSRoute":
		// L4 routes are inventoried on the Gateway trace but not traced or
		// tested - say so instead of leaving the hop's coverage unexplained.
		return []probe.Result{probe.Skipped(probe.LayerTCP, "", vantage,
			"L4 route - radar doesn't trace or test TCPRoute/TLSRoute backends; the parent Gateway listener's TCP reachability is still probed")}
	}
	return nil
}

// External hosts are slower than in-cluster targets, so ExternalName probes
// get more generous timeouts than the in-cluster constants.
const (
	extDNSTimeout  = time.Second
	extTCPTimeout  = time.Second
	extHTTPTimeout = 1500 * time.Millisecond
)

// probeExternalName tests an ExternalName Service's target - a host OUTSIDE the
// cluster (a DNS alias; no pods, no ClusterIP). It IS reachability-testable, just
// not in-cluster: does the alias host RESOLVE (DNS), and does it ANSWER? DNS is
// the definitive resolve check; per-port probing follows the Service's DECLARED
// spec.ports (carried on the hop Config) so the protocol tested is the one the
// operator declared, never a guess presented as a verdict. Only when the
// Service declares no ports does the fallback assumed-HTTP-on-80 probe run,
// and its failure stays a skip (the assumption, not the dependency, may be
// what failed).
func probeExternalName(ctx context.Context, h *Hop, vantage probe.Vantage, path string) []probe.Result {
	host := strings.TrimSpace(h.Resource.Name)
	if host == "" {
		return nil
	}
	var out []probe.Result

	dctx, dcancel := context.WithTimeout(ctx, extDNSTimeout)
	dns := probe.DNS(dctx, host, vantage)
	dcancel()
	if !dns.OK && vantage == probe.VantageLocal {
		// An ExternalName can alias a cluster-internal / split-horizon host a laptop
		// can't resolve. A failed LOCAL lookup isn't proof the alias is broken -
		// demote to a skip instead of a confident red row (mirrors probeIngress's
		// local-vantage demotion). Radar's in-cluster test never dials external
		// aliases (its probes stay inside the cluster), so the skip must not
		// prescribe it - it hands a copyable manual check instead.
		return []probe.Result{classed(probe.SkippedCmd(probe.LayerDNS, host, vantage,
			"couldn't resolve this external alias from where Radar runs - it may be a split-horizon or cluster-internal name that only resolves inside the cluster. Radar doesn't test external aliases from in-cluster; check it from a pod by hand.",
			fmt.Sprintf("kubectl run alias-check --rm -i --restart=Never --image=busybox:1.36 -- nslookup %s", host)), SkipClassVantage)}
	}
	dns.Path = probe.PathData
	out = append(out, dns)
	// If the name doesn't resolve, there's nothing to reach - don't add a noisy
	// HTTP failure on top of the real DNS answer.
	if ctx.Err() != nil || !dns.OK {
		return out
	}
	if h.Config == nil || len(h.Config.Ports) == 0 {
		return append(out, probeExternalNameAssumedHTTP(ctx, host, path, vantage))
	}
	// If the alias resolves only to internal/private addresses, a failed dial
	// from a laptop is "can't reach from here", not a broken dependency - the
	// per-port probes demote those failures to skips (mirrors probeIngress).
	internalOnly := vantage == probe.VantageLocal && hostResolvesInternalOnly(ctx, host)
	for _, p := range h.Config.Ports {
		if ctx.Err() != nil {
			break
		}
		out = append(out, probeExternalNamePort(ctx, host, path, p, vantage, internalOnly))
	}
	return out
}

// probeExternalNamePort tests ONE declared Service port against the external
// host, picking the probe by what the operator declared rather than guessing:
//
//   - a TLS port (443 / named https/wss / appProtocol) gets a real HTTPS
//     request with SNI + Host set to the external hostname;
//   - a confidently-HTTP port (80, named http, well-known web port) gets the
//     plain HTTP request;
//   - everything else gets a TCP dial only - "the port accepts connections"
//     is all that can be claimed without knowing the protocol, so no HTTP
//     verdict is invented for it (an assumed-HTTP failure must never read as
//     "broken");
//   - UDP/SCTP can't be tested from here at all, so they skip honestly.
func probeExternalNamePort(ctx context.Context, host, path string, p PortMap, vantage probe.Vantage, internalOnly bool) probe.Result {
	target := net.JoinHostPort(host, strconv.Itoa(int(p.Port)))
	if proto := strings.ToUpper(strings.TrimSpace(p.Protocol)); proto == "UDP" || proto == "SCTP" {
		skip := probe.SkippedCmd(probe.LayerTCP, target, vantage,
			fmt.Sprintf("port %d is %s - a TCP dial can't test it; reachability not verified.", p.Port, proto), "")
		skip.Path = probe.PathData
		skip.Port = p.Port
		return skip
	}
	var r probe.Result
	switch {
	case isHTTPSPort(p.Name, p.AppProtocol, p.Port):
		hctx, cancel := context.WithTimeout(ctx, extHTTPTimeout)
		r = probe.HTTP(hctx, "https://"+hostPortForURL(host, p.Port, 443)+path, host, vantage)
		cancel()
	case isHTTPProbablePort(p.Name, p.AppProtocol, p.Port) && !httpPortAssumed(p.Name, p.AppProtocol, p.Port):
		hctx, cancel := context.WithTimeout(ctx, extHTTPTimeout)
		r = probe.HTTP(hctx, "http://"+hostPortForURL(host, p.Port, 80)+path, host, vantage)
		cancel()
	default:
		tctx, cancel := context.WithTimeout(ctx, extTCPTimeout)
		r = probe.TCP(tctx, target, vantage)
		cancel()
	}
	r.Path = probe.PathData
	r.Port = p.Port
	// The dial-time SSRF guard refused an internal/metadata target (an ExternalName
	// can alias 169.254.169.254 or a loopback address). Its raw "(SSRF guard)"
	// string must never surface as a red "unreachable" verdict at ANY vantage -
	// demote to an honest skip naming the blocked target (mirrors probeGateway).
	if !r.OK && isInternalGuardError(r) {
		skip := probe.SkippedCmd(r.Layer, r.Target, vantage,
			fmt.Sprintf("%q maps to an internal or blocked address (e.g. cloud metadata or loopback) that can't be probed from here.", host), "")
		skip.Path = probe.PathData
		skip.Port = p.Port
		return skip
	}
	if !r.OK && internalOnly {
		skip := probe.SkippedCmd(r.Layer, r.Target, vantage,
			fmt.Sprintf("%q resolves to an internal address your machine can't reach - it may be cluster-internal. Radar doesn't test external aliases from in-cluster; check it from a pod by hand.", host),
			fmt.Sprintf("kubectl run alias-check --rm -i --restart=Never --image=busybox:1.36 -- nc -vz -w 3 %s %d", host, p.Port))
		skip.Path = probe.PathData
		skip.Port = p.Port
		return classed(skip, SkipClassVantage)
	}
	return budgetSkipIfExhausted(ctx, r, vantage)
}

// probeExternalNameAssumedHTTP is the no-declared-ports fallback: best-effort
// plain HTTP over the common web port. The port AND protocol are assumptions,
// so a failure is surfaced as a skip naming the assumption, never a hard fail
// condemning a dependency that may simply be HTTPS-only or on another port.
func probeExternalNameAssumedHTTP(ctx context.Context, host, path string, vantage probe.Vantage) probe.Result {
	hctx, hcancel := context.WithTimeout(ctx, extHTTPTimeout)
	r := probe.HTTP(hctx, "http://"+host+path, host, vantage)
	hcancel()
	if !r.OK {
		// A guard-refused internal/metadata alias must read as a neutral skip, not a
		// red "unreachable" carrying the raw "(SSRF guard)" string (mirrors probeGateway).
		if isInternalGuardError(r) {
			return probe.SkippedCmd(probe.LayerHTTP, "http://"+host+path, vantage,
				fmt.Sprintf("%q maps to an internal or blocked address (e.g. cloud metadata or loopback) that can't be probed from here.", host), "")
		}
		return probe.SkippedCmd(probe.LayerHTTP, "http://"+host+path, vantage,
			"assumed plain HTTP on port 80 (an ExternalName declares no protocol) and couldn't reach it that way - the real dependency may be HTTPS or on another port.",
			"curl -sS https://"+host+path)
	}
	r.Path = probe.PathData
	return r
}

// hostPortForURL renders host[:port] for a probe URL, omitting the port when
// it's the scheme default so the target reads the way an operator would type it.
func hostPortForURL(host string, port, schemeDefault int32) string {
	if port == schemeDefault {
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port)))
}

// probeService runs every feasible path for each port. In-cluster + client
// gets both direct TCP (the data path through kube-proxy) and ServiceProxy
// (through the apiserver) - divergence between them isolates a NetworkPolicy
// or kube-proxy issue from an apiserver-side issue. From a laptop only the
// apiserver path is reachable; in-cluster without a client falls back to
// direct TCP only. The apiserver path is HTTP-only, so non-HTTP ports are
// skipped with a reason instead of producing a false fail.
// declaredPortLabel names a declared ServicePort for skip rows. The bare
// number was not an identity: Kubernetes permits TCP and UDP ports with the
// same number (kube-dns declares both :53), and two rows reading "port 53"
// were indistinguishable. The protocol rides on non-TCP ports and the declared
// name (usually the protocol: dns-tcp, otlp-grpc, redis) rides always - for
// untestable non-HTTP ports the name IS the useful information.
func declaredPortLabel(p PortMap) string {
	label := fmt.Sprintf("port %d", p.Port)
	if proto := strings.ToUpper(strings.TrimSpace(p.Protocol)); proto != "" && proto != "TCP" {
		label += "/" + proto
	}
	if name := strings.TrimSpace(p.Name); name != "" {
		label += " (" + name + ")"
	} else if ap := strings.TrimSpace(p.AppProtocol); ap != "" {
		label += " (" + ap + ")"
	}
	return label
}

func probeService(ctx context.Context, h *Hop, vantage probe.Vantage, client kubernetes.Interface, path string) []probe.Result {
	if h.Config == nil || len(h.Config.Ports) == 0 {
		return nil
	}
	headless := h.Config.ClusterIP == "" || h.Config.ClusterIP == "None"

	var out []probe.Result
	for _, p := range h.Config.Ports {
		if ctx.Err() != nil {
			break
		}
		// A UDP/SCTP Service port doesn't accept TCP - a TCP dial (and the
		// HTTP apiserver path below) would fail and falsely condemn a healthy
		// non-TCP service. We can't test UDP/SCTP reachability honestly from
		// here, so say so instead of guessing. Mirrors the Gateway-listener
		// UDP skip. Empty Protocol defaults to TCP and falls through.
		if proto := strings.ToUpper(strings.TrimSpace(p.Protocol)); proto == "UDP" || proto == "SCTP" {
			cmd := fmt.Sprintf("nc -u -vz %s.%s %d", h.Resource.Name, h.Resource.Namespace, p.Port)
			if proto == "SCTP" {
				cmd = fmt.Sprintf("# %s.%s:%d is SCTP - test with an SCTP-capable client (e.g. sctp_test)", h.Resource.Name, h.Resource.Namespace, p.Port)
			}
			skip := probe.SkippedCmd(probe.LayerTCP, declaredPortLabel(p), vantage,
				fmt.Sprintf("port %d is %s - a TCP dial can't test it; reachability not verified. Test with a %s client.", p.Port, proto, proto),
				cmd)
			skip.Port = p.Port
			skip.Protocol = proto
			out = append(out, skip)
			continue
		}
		dataReachable := !headless && vantage == probe.VantageInCluster
		if dataReachable {
			pctx, cancel := context.WithTimeout(ctx, tcpTimeout)
			r := probe.TCP(pctx, net.JoinHostPort(h.Config.ClusterIP, strconv.Itoa(int(p.Port))), vantage)
			cancel()
			r.Path = probe.PathData
			r.Port = p.Port
			out = append(out, budgetSkipIfExhausted(ctx, r, vantage))
		}
		if client != nil && ctx.Err() != nil {
			// Budget expired during the data-path probe. Running the apiserver probe
			// now would map a context-deadline error into a false "Timed out …
			// unreachable" row - emit an honest budget skip instead.
			skip := probe.SkippedCmd(probe.LayerHTTP, declaredPortLabel(p), vantage,
				"the test ran out of time before the API-server path could be tested", "")
			skip.Path = probe.PathAPIServer
			skip.Port = p.Port
			out = append(out, skip)
			break
		}
		if client != nil {
			target := declaredPortLabel(p)
			if !isHTTPProbablePort(p.Name, p.AppProtocol, p.Port) {
				cmd := portForwardCmd("svc", h.Resource.Namespace, h.Resource.Name, p.Port)
				if isGRPCLike(p.Name, p.AppProtocol) {
					// -plaintext is for cleartext gRPC (h2c). A TLS gRPC port,
					// appProtocol "h2" or a gRPC port on 443/8443, needs a TLS
					// handshake where -plaintext fails, so use -insecure there and the
					// copyable reproducer does not mislead.
					flag := "-plaintext"
					if isHTTPSPort(p.Name, p.AppProtocol, p.Port) || strings.EqualFold(strings.TrimSpace(p.AppProtocol), "h2") {
						flag = "-insecure"
					}
					cmd += fmt.Sprintf("   # then: grpcurl %s localhost:%d list", flag, p.Port)
				} else {
					cmd += fmt.Sprintf("   # then connect a client for this protocol on localhost:%d", p.Port)
				}
				skip := probe.SkippedCmd(probe.LayerHTTP, target, vantage,
					nonHTTPSkipReason(p.Name, p.AppProtocol, p.Port, vantage, dataReachable),
					cmd)
				skip.Path = probe.PathAPIServer
				// The skip is ABOUT this declared port; without the stamp it fell
				// into the port-agnostic pool and the port lost its identity - and
				// with it, any chance of a preserved test candidate.
				skip.Port = p.Port
				if !dataReachable {
					skip = classed(skip, SkipClassVantage)
				} else {
					// TCP is the complete automatic check for an explicitly
					// non-HTTP route. The inapplicable HTTP proxy path is useful
					// context, but it is not lost route coverage.
					skip = classed(skip, SkipClassBenign)
				}
				out = append(out, skip)
				continue
			}
			// The API server proxy speaks plain HTTP to the backend, so it
			// can't verify a TLS port - probing an HTTPS backend as plain HTTP
			// would falsely read "unreachable/broken". Skip honestly; the
			// in-cluster TCP probe above still proves the port is open.
			if isHTTPSPort(p.Name, p.AppProtocol, p.Port) {
				skip := probe.SkippedCmd(probe.LayerHTTP, target, vantage,
					"HTTPS backend - the API-server proxy speaks plain HTTP and can't verify TLS on this port. Test it directly.",
					portForwardCmd("svc", h.Resource.Namespace, h.Resource.Name, p.Port)+fmt.Sprintf("   # then: curl -k https://localhost:%d/", p.Port))
				skip.Path = probe.PathAPIServer
				// Port-stamped so the declared HTTPS candidate survives as a
				// not-tested route the in-cluster test can actually dial.
				skip.Port = p.Port
				skip = classed(skip, SkipClassVantage)
				out = append(out, skip)
				continue
			}
			pctx, cancel := context.WithTimeout(ctx, httpTimeout)
			assumed := httpPortAssumed(p.Name, p.AppProtocol, p.Port)
			assumedCmd := portForwardCmd("svc", h.Resource.Namespace, h.Resource.Name, p.Port) + fmt.Sprintf("   # then curl localhost:%d to test the real protocol", p.Port)
			pr := softenAssumedHTTP(serviceProxyProbe(pctx, client, h.Resource.Namespace, h.Resource.Name, p.Port, path, vantage), assumed, assumedCmd)
			pr.Port = p.Port
			out = append(out, budgetSkipIfExhausted(ctx, pr, vantage))
			cancel()
			continue
		}
		if !dataReachable {
			// A nil client here has two distinct causes that the probe
			// layer can't otherwise distinguish: from laptop vantage
			// it means no kubeconfig is wired up for proxying; from
			// in-cluster vantage it means the per-request impersonated
			// identity could not be constructed (an auth/RBAC failure).
			// Use vantage to pick the message so the operator
			// investigates the right layer.
			reason := "Kubernetes API isn't reachable from here, so the apiserver path can't be tested."
			// The in-cluster cause is RBAC, so hand the operator the exact
			// can-i check to confirm it's permissions, not an outage. From a
			// laptop the cause is a missing kubeconfig - auth can-i wouldn't run,
			// so no command there.
			var cmd string
			if vantage == probe.VantageInCluster {
				reason = "Apiserver path couldn't be tested for this request - your identity may lack permission to proxy."
				cmd = fmt.Sprintf("kubectl auth can-i get services/proxy -n %s", h.Resource.Namespace)
			}
			skip := probe.SkippedCmd(probe.LayerHTTP, declaredPortLabel(p), vantage, reason, cmd)
			skip.Path = probe.PathAPIServer
			skip.Port = p.Port
			skip = classed(skip, SkipClassVantage)
			out = append(out, skip)
		}
	}
	return out
}

// isHTTPProbablePort decides whether the API server proxy is likely to
// succeed against the given port. Signals checked in priority order:
//
//  1. appProtocol (k8s 1.20+) - authoritative when set.
//  2. Conventional port name - many Helm charts label ports "http",
//     "postgres", etc.
//  3. Well-known port number - catches the common no-metadata case (a Helm
//     chart that ships Redis on 6379 without setting name or appProtocol).
//
// gRPC and HTTP/2 (h2c/h2) are explicitly excluded: probe.HTTP uses the
// standard net/http client which speaks HTTP/1.1, so an HTTP/2-only upstream
// would report a false fail.
//
// No-signal default is true - most Service ports are HTTP-shaped - so an
// unclassified web service still gets probed rather than unexplained-skipped.
func isHTTPProbablePort(name, appProtocol string, port int32) bool {
	if ap := strings.ToLower(strings.TrimSpace(appProtocol)); ap != "" {
		// Everything the TLS classifier accepts as an HTTPS appProtocol must be
		// HTTP-probable too - the classifiers disagreeing gave an https2 port a
		// TCP-only candidate instead of a real HTTPS request.
		switch ap {
		case "http", "https", "https2", "ws", "wss", "kubernetes.io/ws", "kubernetes.io/wss":
			return true
		}
		return false
	}
	// An explicit http/https name outranks the port NUMBER below. An operator who
	// names a port "http-webhook" has said what speaks there, and Kubernetes
	// treats the port name as the protocol hint - so a number that happens to sit
	// in the misc-TCP list must not overrule it. Checked after the non-HTTP names
	// so "grpc-web" and "h2c" keep losing.
	if nameSaysHTTP(name) {
		return true
	}
	// A name the TLS classifier reads as TLS ("tls", "wss") speaks HTTP over
	// TLS - same rule as the appProtocol tier above.
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "tls", "wss":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "grpc", "grpc-web", "h2", "h2c",
		"postgres", "postgresql", "pg",
		"mysql", "mariadb",
		"redis", "valkey",
		"mongo", "mongodb",
		"kafka",
		"amqp", "rabbitmq",
		"smtp", "imap", "pop3",
		"ssh", "ftp", "sftp",
		"dns",
		"udp", "tcp",
		"mqtt",
		"memcached",
		"cassandra",
		"elasticsearch-transport":
		return false
	}
	switch port {
	case 5432, 5433, // postgres
		3306, 3307, // mysql / mariadb
		6379, 6380, // redis
		27017, 27018, 27019, // mongodb
		9042,         // cassandra
		9092,         // kafka
		5672,         // amqp / rabbitmq
		25, 465, 587, // smtp
		22,         // ssh
		21,         // ftp
		53,         // dns
		11211,      // memcached
		1883, 8883, // mqtt
		2181,       // zookeeper
		7000, 7001: // misc TCP defaults
		return false
	}
	return true
}

// hostResolvesInternalOnly reports whether every IP host resolves to is an
// internal/private address - i.e. a laptop typically can't route to it even
// when in-cluster traffic is fine. Returns false if it doesn't resolve or any
// resolved IP is public (a TCP failure to a public address IS real evidence).
func hostResolvesInternalOnly(ctx context.Context, host string) bool {
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if !isInternalAddr(a) {
			return false
		}
	}
	return true
}

// isInternalAddr reports whether addr is a private/internal IP (RFC1918,
// loopback, link-local, unique-local) - an address a laptop typically can't
// route to even when in-cluster traffic to it is fine. A non-IP hostname
// returns false (it isn't a literal internal IP we can be sure about).
func isInternalAddr(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	// IsUnspecified (0.0.0.0 / ::) is a common Gateway/Ingress status address when
	// the controller hasn't published a routable one yet - it is not a real target
	// and the dial-time SSRF guard refuses it, so treat it as internal/untestable.
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// isInternalGuardError reports whether a probe failed because the dial-time SSRF
// guard (denyInternalControl) refused an internal/unspecified target. Its raw
// "(SSRF guard)" message must NEVER surface as an operator verdict, so callers
// demote it to a clean skip at ANY vantage.
func isInternalGuardError(r probe.Result) bool {
	return strings.Contains(r.Error, "SSRF guard") || strings.Contains(r.Error, "refusing to probe internal address")
}

// isHTTPSPort reports whether a port terminates TLS (HTTPS/WSS). The API
// server proxy can't verify these - it speaks plain HTTP - so they're skipped
// on the apiserver path rather than probed as plain HTTP (which would falsely
// fail). Signals: appProtocol, then a telling port name, then well-known TLS
// port numbers.
func isHTTPSPort(name, appProtocol string, port int32) bool {
	switch strings.ToLower(strings.TrimSpace(appProtocol)) {
	case "https", "wss", "kubernetes.io/wss", "https2":
		return true
	}
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "https", "wss", "tls":
		return true
	}
	// "https-alt" and friends: a name that says https means https.
	if strings.Contains(n, "https") {
		return true
	}
	return port == 443 || port == 8443
}

// httpPortAssumed reports whether isHTTPProbablePort chose "probe as HTTP" only
// by default - no appProtocol, no telling port name, no well-known port. In
// that case an HTTP result is built on an assumption about the protocol, so a
// failure is as likely "this port isn't HTTP" as a real outage and must not be
// presented as a confident break.
func httpPortAssumed(name, appProtocol string, port int32) bool {
	if !isHTTPProbablePort(name, appProtocol, port) {
		return false
	}
	if strings.TrimSpace(appProtocol) != "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "http", "https", "web", "www", "http-web", "https-web", "ui", "api":
		return false
	}
	switch port {
	case 80, 443, 8080, 8443, 8000, 8081, 3000:
		return false
	}
	return true
}

// softenAssumedHTTP rewrites a bad probe on an assumed-HTTP port into an honest
// "couldn't verify" skip. We guessed the protocol, so a transport failure OR a
// 5xx (the apiserver proxy answers 503 when a backend doesn't speak HTTP) is as
// likely "this port isn't HTTP" as a real problem. Claiming the path is
// broken/degraded on a guess would be the exact confident-but-wrong result this
// feature must never produce. A verified 2xx or a reached 3xx/4xx passes
// through - those genuinely reached an HTTP server, assumption or not.
func softenAssumedHTTP(r probe.Result, assumed bool, gapCmd string) probe.Result {
	if !assumed || r.Skipped {
		return r
	}
	// A connection-refused / no-listener is unambiguous: nothing is on the port,
	// regardless of protocol. Don't soften that into "maybe not HTTP" - it's a
	// real dead port (a declared port nothing serves), and softening it would
	// hide the failure behind a green verdict. Only soften signals that actually
	// suggest a non-HTTP protocol (EOF / closed-before-response, or a proxied
	// 5xx from a backend that can't be spoken to over plain HTTP).
	low := strings.ToLower(r.Error + " " + r.Detail)
	deadPort := strings.Contains(low, "connection refused") ||
		strings.Contains(low, "nothing is listening") ||
		strings.Contains(low, "no route to host")
	bad := (!r.OK && r.Error != "") || r.Tone == probe.ToneDegraded
	if !bad || deadPort {
		return r
	}
	detail := r.Error
	if detail == "" {
		detail = r.Detail
	}
	s := probe.SkippedCmd(r.Layer, r.Target, r.Vantage,
		fmt.Sprintf("assumed HTTP (port has no protocol hint) but got: %s - the port may not speak HTTP. Test the real protocol to confirm.", detail),
		gapCmd)
	s.Path = r.Path
	return s
}

// portForwardCmd is the copyable first step for verifying a port the apiserver
// proxy can't (non-HTTP, HTTPS backend, assumed-HTTP that failed): forward the
// port locally, then point a protocol-appropriate client at localhost. kind is
// "svc" or "pod".
func portForwardCmd(kind, ns, name string, port int32) string {
	return fmt.Sprintf("kubectl port-forward %s/%s -n %s %d:%d", kind, name, ns, port, port)
}

// curlReachCmd is the copyable command for an HTTP(S) host the current vantage
// can't reach (wildcard, unresolvable, or internal-only address): run it from
// somewhere that can route to the host.
func curlReachCmd(scheme, host string) string {
	return fmt.Sprintf("curl -sS -o /dev/null -w 'HTTP %%{http_code}\\n' %s://%s/", scheme, host)
}

// nonHTTPSkipReason explains in plain operator English why we didn't run the
// apiserver-path probe against this port, and (from a laptop) why no TCP
// probe ran either. The laptop case is load-bearing: dataReachable is false,
// so the apiserver skip is the ONLY row the operator sees; without the
// "run in-cluster" hint they may assume TCP was checked when it wasn't.
// tcpRan reports whether a data-path TCP probe actually ran for THIS hop:
// in-cluster only dials a routable address, so a headless Service (no ClusterIP)
// or a pods hop with names but no IPs gets no TCP probe even in-cluster - the
// gRPC "still checked at the TCP level" line would overclaim there.
// Avoid Kubernetes spec syntax - the reader is debugging connectivity,
// not editing YAML.
func nonHTTPSkipReason(portName, appProtocol string, port int32, vantage probe.Vantage, tcpRan bool) string {
	base := nonHTTPBaseReason(portName, appProtocol, port)
	if vantage == probe.VantageLocal {
		return base + " Run Radar from in-cluster to verify TCP reachability."
	}
	// Only claim TCP was checked when a data-path TCP probe actually ran on this
	// hop. From a laptop the caller has appended the "run in-cluster" hint instead.
	if tcpRan && isGRPCLike(portName, appProtocol) {
		return base + " Reachability still checked at the TCP level."
	}
	return base
}

func nonHTTPBaseReason(portName, appProtocol string, port int32) string {
	if isGRPCLike(portName, appProtocol) {
		return "Port speaks gRPC or HTTP/2; the probe only knows HTTP/1.1."
	}
	if appProtocol != "" {
		return fmt.Sprintf("Port is declared as %q, not HTTP. The Kubernetes API path only carries HTTP traffic.", appProtocol)
	}
	// Name the signal that ACTUALLY fired. Reporting the port name whenever one
	// exists blamed the name for a decision the port NUMBER made, sending the
	// reader to rename a port that was never the problem.
	if nameLooksNonHTTP(portName) {
		return fmt.Sprintf("Port named %q looks non-HTTP. The Kubernetes API path only carries HTTP traffic.", portName)
	}
	if portName != "" {
		return fmt.Sprintf("Port %d (%q) is a well-known non-HTTP port. The Kubernetes API path only carries HTTP traffic.", port, portName)
	}
	return fmt.Sprintf("Port %d is a well-known non-HTTP port. The Kubernetes API path only carries HTTP traffic.", port)
}

// nameSaysHTTP reports whether a port NAME positively declares HTTP. Kept
// narrow: an exact http/https, or an http-/https- prefixed name such as
// "http-webhook" or "https-api".
func nameSaysHTTP(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "http" || n == "https" || strings.HasPrefix(n, "http-") || strings.HasPrefix(n, "https-")
}

// nameLooksNonHTTP reports whether the port NAME is what disqualified it, as
// opposed to the port number.
func nameLooksNonHTTP(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "grpc", "grpc-web", "h2", "h2c",
		"postgres", "postgresql", "pg",
		"mysql", "mariadb",
		"redis", "valkey",
		"mongo", "mongodb",
		"kafka",
		"amqp", "rabbitmq",
		"smtp", "imap", "pop3",
		"ssh", "ftp", "sftp",
		"dns",
		"udp", "tcp",
		"mqtt",
		"memcached",
		"cassandra",
		"elasticsearch-transport":
		return true
	}
	return false
}

// portKey extracts the port number from a probe target so divergence
// detection can pair "10.0.0.5:80" with a declared-port label. Declared-port
// labels carry protocol and name suffixes ("port 53/UDP (dns)",
// "port 6379 (redis)") - anchoring on a TRAILING integer paired nothing for
// any named port, silently disabling every per-port reconciliation.
// Returns "" when no port number is present.
func portKey(target string) string {
	// "...:N" (IP-style and "name:port" route targets).
	if i := strings.LastIndexByte(target, ':'); i >= 0 && i < len(target)-1 {
		rest := target[i+1:]
		if isAllDigits(rest) {
			return rest
		}
	}
	// "port N..." labels: the digits directly after the marker, whatever
	// follows them ("/UDP", "(redis)", nothing).
	if i := strings.LastIndex(target, "port "); i >= 0 {
		rest := target[i+len("port "):]
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		if end > 0 {
			return rest[:end]
		}
	}
	// "...space N" (friendly "name 8080" targets).
	if i := strings.LastIndexByte(target, ' '); i >= 0 && i < len(target)-1 {
		rest := target[i+1:]
		if isAllDigits(rest) {
			return rest
		}
	}
	return ""
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isGRPCLike(name, appProtocol string) bool {
	switch strings.ToLower(strings.TrimSpace(appProtocol)) {
	case "grpc", "grpc-web", "h2", "h2c", "kubernetes.io/h2c":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "grpc", "grpc-web", "h2", "h2c":
		return true
	}
	return false
}

const maxPodsToProbe = 3

// classed stamps a coverage SkipClass on a skip result so the coverage layer
// reads the class structurally instead of re-deriving it from the reason text
// (which drifts when the message is reworded). Skips left unstamped fall back to
// classifySkip, so only the vantage and benign sites, the ones the substring match
// keys on, need it.
func classed(r probe.Result, class string) probe.Result {
	r.SkipClass = class
	return r
}

// probePods runs every feasible path against each sampled pod's container
// ports. In-cluster + PodIPs gets direct TCP (data path); a client + pod
// names gets PodProxy (apiserver path). Both run when both are feasible -
// divergence shows whether kube-proxy / NetworkPolicy is blocking pod-to-pod
// while the apiserver's proxy still reaches.
func probePods(ctx context.Context, h *Hop, vantage probe.Vantage, client kubernetes.Interface, path string) []probe.Result {
	if h.Config == nil || len(h.Config.ContainerPorts) == 0 {
		return nil
	}
	dataReachable := vantage == probe.VantageInCluster && len(h.Config.PodIPs) > 0
	apiReachable := client != nil && len(h.Config.PodNames) > 0
	if !dataReachable && !apiReachable {
		return []probe.Result{probe.Skipped(probe.LayerTCP, "", vantage,
			"no ready pods identified for probing")}
	}
	var out []probe.Result
	if dataReachable {
		out = append(out, probePodsByIP(ctx, h, vantage)...)
	}
	if apiReachable {
		out = append(out, probePodsByName(ctx, h, vantage, client, path)...)
	}
	return out
}

func probePodsByIP(ctx context.Context, h *Hop, vantage probe.Vantage) []probe.Result {
	ips := h.Config.PodIPs
	if len(ips) > maxPodsToProbe {
		ips = ips[:maxPodsToProbe]
	}
	var out []probe.Result
	for _, ip := range ips {
		for _, cp := range h.Config.ContainerPorts {
			if ctx.Err() != nil {
				break
			}
			// A UDP/SCTP container port doesn't accept TCP - a TCP dial would
			// falsely condemn a healthy non-TCP pod (e.g. CoreDNS :53/UDP). Skip
			// honestly, mirroring probeService. Empty protocol defaults to TCP.
			if proto := strings.ToUpper(strings.TrimSpace(cp.Protocol)); proto == "UDP" || proto == "SCTP" {
				skip := probe.SkippedCmd(probe.LayerTCP, fmt.Sprintf("port %d", cp.Port), vantage,
					fmt.Sprintf("port %d is %s - a TCP dial can't test it; reachability not verified.", cp.Port, proto), "")
				skip.Port = cp.Port
				out = append(out, skip)
				continue
			}
			pctx, cancel := context.WithTimeout(ctx, tcpTimeout)
			r := probe.TCP(pctx, net.JoinHostPort(ip, strconv.Itoa(int(cp.Port))), vantage)
			cancel()
			r.Path = probe.PathData
			r.Port = cp.Port
			out = append(out, budgetSkipIfExhausted(ctx, r, vantage))
		}
	}
	if len(h.Config.PodIPs) > len(ips) {
		out = append(out, classed(probe.Skipped(probe.LayerTCP, "", vantage,
			fmt.Sprintf("sampled %d of %d ready pods", len(ips), len(h.Config.PodIPs))), SkipClassBenign))
	}
	return out
}

func probePodsByName(ctx context.Context, h *Hop, vantage probe.Vantage, client kubernetes.Interface, path string) []probe.Result {
	names := h.Config.PodNames
	if len(names) > maxPodsToProbe {
		names = names[:maxPodsToProbe]
	}
	dataReachable := vantage == probe.VantageInCluster && len(h.Config.PodIPs) > 0
	var out []probe.Result
	for _, name := range names {
		for _, cp := range h.Config.ContainerPorts {
			if ctx.Err() != nil {
				break
			}
			// PodProxy is HTTP-only via the API server, same constraint as
			// ServiceProxy. Container ports don't carry appProtocol, so the
			// port name is the only signal for non-HTTP detection.
			target := fmt.Sprintf("%s port %d", name, cp.Port)
			// A UDP/SCTP container port doesn't accept TCP - the apiserver
			// pod-proxy HTTP probe would get "connection refused" and
			// softenAssumedHTTP would falsely condemn a healthy non-TCP pod.
			// Skip honestly, mirroring probePodsByIP / probeService. Empty
			// protocol defaults to TCP and falls through.
			if proto := strings.ToUpper(strings.TrimSpace(cp.Protocol)); proto == "UDP" || proto == "SCTP" {
				skip := probe.SkippedCmd(probe.LayerHTTP, target, vantage,
					fmt.Sprintf("port %d is %s - a TCP/HTTP dial can't test it; reachability not verified.", cp.Port, proto), "")
				skip.Path = probe.PathAPIServer
				skip.Port = cp.Port
				out = append(out, skip)
				continue
			}
			if !isHTTPProbablePort(cp.Name, "", cp.Port) {
				skip := probe.SkippedCmd(probe.LayerHTTP, target, vantage, nonHTTPSkipReason(cp.Name, "", cp.Port, vantage, dataReachable),
					portForwardCmd("pod", h.Resource.Namespace, name, cp.Port)+fmt.Sprintf("   # then connect a client for this protocol on localhost:%d", cp.Port))
				skip.Path = probe.PathAPIServer
				skip.Port = cp.Port
				if dataReachable {
					skip = classed(skip, SkipClassBenign)
				} else {
					skip = classed(skip, SkipClassVantage)
				}
				out = append(out, skip)
				continue
			}
			if isHTTPSPort(cp.Name, "", cp.Port) {
				skip := probe.SkippedCmd(probe.LayerHTTP, target, vantage,
					"HTTPS backend - the API-server proxy speaks plain HTTP and can't verify TLS on this port. Test it directly.",
					portForwardCmd("pod", h.Resource.Namespace, name, cp.Port)+fmt.Sprintf("   # then: curl -k https://localhost:%d/", cp.Port))
				skip.Path = probe.PathAPIServer
				skip.Port = cp.Port
				skip = classed(skip, SkipClassVantage)
				out = append(out, skip)
				continue
			}
			pctx, cancel := context.WithTimeout(ctx, httpTimeout)
			assumed := httpPortAssumed(cp.Name, "", cp.Port)
			assumedCmd := portForwardCmd("pod", h.Resource.Namespace, name, cp.Port) + fmt.Sprintf("   # then curl localhost:%d to test the real protocol", cp.Port)
			pr := softenAssumedHTTP(podProxyProbe(pctx, client, h.Resource.Namespace, name, cp.Port, path, vantage), assumed, assumedCmd)
			pr.Port = cp.Port
			out = append(out, budgetSkipIfExhausted(ctx, pr, vantage))
			cancel()
		}
	}
	if len(h.Config.PodNames) > len(names) {
		out = append(out, classed(probe.Skipped(probe.LayerHTTP, "", vantage,
			fmt.Sprintf("sampled %d of %d ready pods", len(names), len(h.Config.PodNames))), SkipClassBenign))
	}
	return out
}

// probeIngress walks rules + spec hosts and runs the ladder against each
// host. The interesting failure mode is "DNS resolves but TCP doesn't
// connect" - that's a routing problem operators routinely chase by hand.
// Each host gets one DNS probe + one TCP+HTTP probe per port (80 + 443).
func probeIngress(ctx context.Context, h *Hop, vantage probe.Vantage, path string) []probe.Result {
	if h.Config == nil {
		return nil
	}
	hosts := uniqueHosts(h.Config.Hostnames, h.Config.Rules)
	if len(hosts) == 0 {
		return []probe.Result{probe.Skipped(probe.LayerDNS, "", vantage,
			"Ingress declares no hostnames; reachability test would have no target")}
	}
	tlsHosts := make(map[string]bool, len(h.Config.TLSHosts))
	for _, th := range h.Config.TLSHosts {
		tlsHosts[th] = true
	}
	hostServesTLS := func(host string) bool {
		if tlsHosts[host] {
			return true
		}
		// A wildcard TLS host ("*.example.com") covers exactly one left-most
		// label, so it matches "api.example.com" but not "a.b.example.com".
		if i := strings.IndexByte(host, '.'); i > 0 && tlsHosts["*"+host[i:]] {
			return true
		}
		return false
	}
	var out []probe.Result
	for _, host := range hosts {
		if ctx.Err() != nil {
			break
		}
		// A wildcard host never resolves as written - the concrete subdomain
		// is dynamic - so a DNS probe against "*.example.com" would report a
		// false "unreachable" on a perfectly valid Ingress. Skip and tell the
		// user to test a concrete hostname rather than inventing a verdict.
		if isWildcardHost(host) {
			out = append(out, probe.SkippedCmd(probe.LayerDNS, host, vantage,
				"wildcard host - test a concrete hostname to check reachability",
				curlReachCmd("https", strings.Replace(host, "*", "YOUR-SUBDOMAIN", 1))+"   # or http:// for a plain-HTTP listener"))
			continue
		}
		dctx, dcancel := context.WithTimeout(ctx, dnsTimeout)
		dnsRes := ingressDNSProbe(dctx, host, vantage)
		dcancel()
		if !dnsRes.OK {
			// A DNS failure from a laptop says nothing about in-cluster
			// reachability - the host may resolve only via cluster-internal /
			// split-horizon DNS (very common). Condemning a valid Ingress as
			// "broken" because the operator's machine can't resolve it is the
			// worst kind of confident-wrong verdict, so skip (don't fail) and
			// hand over a command to test from somewhere that can resolve it.
			// In-cluster, an unresolvable host IS real evidence - keep it.
			if vantage == probe.VantageLocal {
				out = append(out, classed(probe.SkippedCmd(probe.LayerDNS, host, vantage,
					fmt.Sprintf("%q doesn't resolve from your machine - it may be cluster-internal DNS. Run Radar in-cluster, or test from a host that resolves it.", host),
					curlReachCmd("https", host)+"   # from a host that resolves it; or http://"), SkipClassVantage))
				continue
			}
			out = append(out, dnsRes)
			continue
		}
		out = append(out, dnsRes)
		// If the host resolves to internal-only IPs (RFC1918) a laptop usually
		// can't route to, a TCP failure from local vantage is "can't reach from
		// here", not a broken Ingress - same demotion the DNS-miss and the
		// Gateway paths already apply. (If a VPN can reach it, the TCP simply
		// succeeds and no demotion happens.)
		internalOnly := vantage == probe.VantageLocal && hostResolvesInternalOnly(ctx, host)
		// Only probe 443 where the Ingress actually terminates TLS for this
		// host. Probing 443 on a plain-HTTP host dials a port the Ingress
		// doesn't serve (or hits the controller's default cert) and reports a
		// false unreachable / cert failure. Port 80 is always HTTP.
		servesTLS := hostServesTLS(host)
		hostStart := len(out)
		ports := []int{80}
		if servesTLS {
			ports = append(ports, 443)
		}
		for _, port := range ports {
			if ctx.Err() != nil {
				break
			}
			addr := net.JoinHostPort(host, strconv.Itoa(port))
			tctx, tcancel := context.WithTimeout(ctx, tcpTimeout)
			tcpRes := ingressTCPProbe(tctx, addr, vantage)
			tcancel()
			if !tcpRes.OK && ctx.Err() != nil {
				// Budget exhausted mid-dial - a deadline error isn't a real
				// unreachable; degrade to a skip and stop (later ports can't run).
				out = append(out, budgetSkipIfExhausted(ctx, tcpRes, vantage))
				break
			}
			// The dial-time SSRF guard refused an internal/metadata target (a host
			// resolving to 169.254.169.254, loopback, or an internal address). Its raw
			// "(SSRF guard)" string must never read as a red "unreachable" verdict at
			// ANY vantage - demote to an honest skip naming the blocked target
			// (mirrors probeGateway).
			if !tcpRes.OK && isInternalGuardError(tcpRes) {
				out = append(out, probe.SkippedCmd(probe.LayerTCP, addr, vantage,
					fmt.Sprintf("%q maps to an internal or blocked address (e.g. cloud metadata or loopback) that can't be probed from here.", host),
					curlReachCmd("https", host)))
				continue
			}
			if !tcpRes.OK && internalOnly {
				out = append(out, classed(probe.SkippedCmd(probe.LayerTCP, addr, vantage,
					fmt.Sprintf("%q resolves to an internal address your machine can't reach - it may be cluster-internal. Run Radar in-cluster, or test from a host that routes to it.", host),
					curlReachCmd("http", host)), SkipClassVantage))
				continue
			}
			out = append(out, tcpRes)
			if !tcpRes.OK {
				continue
			}
			if port == 443 {
				lctx, lcancel := context.WithTimeout(ctx, tlsTimeout)
				out = append(out, budgetSkipIfExhausted(ctx, ingressTLSProbe(lctx, addr, host, vantage), vantage))
				lcancel()
			}
			hctx, hcancel := context.WithTimeout(ctx, httpTimeout)
			scheme := "http"
			if port == 443 {
				scheme = "https"
			}
			out = append(out, budgetSkipIfExhausted(ctx, ingressHTTPProbe(hctx, fmt.Sprintf("%s://%s%s", scheme, host, path), host, vantage), vantage))
			hcancel()
		}
		if servesTLS {
			reconcileHTTPSOnlyHost(out[hostStart:], host, vantage)
		}
	}
	return out
}

// reconcileHTTPSOnlyHost finalizes a TLS-serving host's :80 outcome AFTER :443
// has been probed - the two ports are separate entry surfaces, and only the
// pair tells the story. When the :443 TCP dial succeeded while :80's failed,
// the host is likely serving HTTPS only (no plain-HTTP listener) - a valid,
// common configuration - so the :80 failure demotes to a skip instead of
// failing every route folded onto this host (mirrors the file's other
// demote-to-skip patterns). When :443 ALSO failed, both failures stand: that's
// a real outage, not an HTTPS-only setup. Rows already demoted to skips
// (budget, internal-address vantage) are left alone.
func reconcileHTTPSOnlyHost(rs []probe.Result, host string, vantage probe.Vantage) {
	addr80 := net.JoinHostPort(host, "80")
	addr443 := net.JoinHostPort(host, "443")
	fail80 := -1
	ok443 := false
	for i, r := range rs {
		if r.Layer != probe.LayerTCP || r.Skipped {
			continue
		}
		switch r.Target {
		case addr80:
			if !r.OK {
				fail80 = i
			}
		case addr443:
			if r.OK {
				ok443 = true
			}
		}
	}
	if fail80 < 0 || !ok443 {
		return
	}
	rs[fail80] = probe.SkippedCmd(probe.LayerTCP, addr80, vantage,
		"port 80 not reachable - this host may be HTTPS-only; port 443 reached. Plain-HTTP clients would need a listener on :80.",
		curlReachCmd("https", host))
}

// probeGateway probes each listener against the Gateway's status.addresses.
// A Gateway with no addresses (controller hasn't programmed it) yields a
// skip - the path is unreachable but that's already a critical static
// finding; the probe would just echo it.
func probeGateway(ctx context.Context, h *Hop, vantage probe.Vantage, path string) []probe.Result {
	if h.Config == nil {
		return nil
	}
	if len(h.Config.Addresses) == 0 {
		return []probe.Result{probe.Skipped(probe.LayerTCP, "", vantage,
			"Gateway has no programmed addresses yet; static findings already cover this case")}
	}
	if len(h.Config.Listeners) == 0 {
		return nil
	}
	var out []probe.Result
	for _, addr := range h.Config.Addresses {
		for _, l := range h.Config.Listeners {
			if ctx.Err() != nil {
				break
			}
			target := net.JoinHostPort(addr, strconv.Itoa(int(l.Port)))
			// A UDP listener (DNS, QUIC, etc.) doesn't accept TCP - a TCP probe
			// would fail and falsely condemn a healthy UDP config. We can't do
			// UDP reachability honestly here, so say so instead of guessing.
			if strings.EqualFold(l.Protocol, "UDP") {
				out = append(out, probe.SkippedCmd(probe.LayerTCP, target, vantage,
					fmt.Sprintf("listener %q is UDP - TCP reachability doesn't apply; test with a UDP client.", l.Name),
					fmt.Sprintf("nc -u -vz %s %d   # or: dig @%s", addr, l.Port, addr)))
				continue
			}
			tctx, tcancel := context.WithTimeout(ctx, tcpTimeout)
			tcpRes := probe.TCP(tctx, target, vantage)
			tcancel()
			if !tcpRes.OK && ctx.Err() != nil {
				// Budget exhausted mid-dial - not a real unreachable; skip and stop.
				out = append(out, budgetSkipIfExhausted(ctx, tcpRes, vantage))
				break
			}
			// The dial-time SSRF guard refused an internal/unspecified target (a
			// Gateway/Ingress reporting 0.0.0.0, loopback, or link-local - common
			// before the controller publishes a routable address). Its raw "(SSRF
			// guard)" string must never read as a red "unreachable" verdict at ANY
			// vantage - demote to an honest skip naming the unpublished address.
			if !tcpRes.OK && isInternalGuardError(tcpRes) {
				out = append(out, probe.SkippedCmd(probe.LayerTCP, target, vantage,
					fmt.Sprintf("entry address %s is internal/unspecified (e.g. 0.0.0.0) - the controller hasn't published a routable external address yet, so it can't be probed.", addr),
					"kubectl get gateway,ingress -A -o wide  # check the published ADDRESS"))
				continue
			}
			// A TCP failure from a laptop to an INTERNAL address (internal
			// LoadBalancer / RFC1918) says nothing about in-cluster reachability
			// - the laptop just can't route there. Condemning the Gateway as
			// "broken" would be the same confident-wrong verdict the DNS-from-
			// laptop path already avoids, so skip with a fill-the-gap command.
			// A public address failing from a laptop IS real, so only demote
			// internal addresses; in-cluster failures stay real evidence.
			if !tcpRes.OK && vantage == probe.VantageLocal && (isInternalAddr(addr) || hostResolvesInternalOnly(ctx, addr)) {
				out = append(out, classed(probe.SkippedCmd(probe.LayerTCP, target, vantage,
					fmt.Sprintf("couldn't reach internal address %s from your machine - it may be cluster-internal. Run Radar in-cluster, or test from a host that routes to it.", addr),
					fmt.Sprintf("nc -vz %s %d", addr, l.Port)), SkipClassVantage))
				continue
			}
			out = append(out, tcpRes)
			if !tcpRes.OK {
				continue
			}
			// A wildcard listener host (e.g. "*.example.com") is a valid
			// config, but sending it as TLS SNI fails cert verification and
			// as a Host header hits a default backend - both would read as a
			// false failure. TCP above already proved the listener address is
			// reachable; the host-specific layers are simply unprovable
			// without a concrete name, so say that instead of guessing.
			if l.Hostname != "" && isWildcardHost(l.Hostname) {
				out = append(out, probe.Skipped(probe.LayerHTTP, l.Hostname, vantage,
					fmt.Sprintf("listener host %q is a wildcard - test a concrete host to verify TLS/HTTP", l.Hostname)))
				continue
			}
			isHTTPS := strings.EqualFold(l.Protocol, "HTTPS")
			if isHTTPS || strings.EqualFold(l.Protocol, "TLS") {
				// A TLS probe needs a concrete hostname for SNI + cert
				// verification. With no listener hostname (IP-based or
				// catch-all listener) we'd validate the cert against the bare
				// IP and report a false TLS failure on a healthy listener. TCP
				// already proved the listener is reachable; say the rest needs
				// a host rather than inventing a cert error.
				if l.Hostname == "" {
					out = append(out, probe.SkippedCmd(probe.LayerTLS, target, vantage,
						"listener has no hostname - provide a concrete host/SNI to verify the TLS certificate",
						fmt.Sprintf("openssl s_client -connect %s:%d -servername <host> </dev/null   # replace <host> with a name clients use", addr, l.Port)))
					continue
				}
				lctx, lcancel := context.WithTimeout(ctx, tlsTimeout)
				out = append(out, budgetSkipIfExhausted(ctx, probe.TLS(lctx, target, l.Hostname, vantage), vantage))
				lcancel()
			}
			// HTTP-level probe for HTTP/HTTPS listeners - TCP success
			// alone would let a Gateway whose controller returns 5xx
			// read as verified at the chip level, while probeIngress
			// on the same shape surfaces the failure. Dial the
			// Gateway's programmed address (the IP the TCP probe just
			// succeeded against) and pass the listener Hostname as the
			// Host header. Using the hostname in the URL would let
			// split-horizon DNS resolve to a different IP (e.g. a CDN
			// in front of the Gateway), masking cluster-side
			// misconfigurations.
			if isHTTPS || strings.EqualFold(l.Protocol, "HTTP") {
				scheme := "http"
				if isHTTPS {
					scheme = "https"
				}
				hctx, hcancel := context.WithTimeout(ctx, httpTimeout)
				url := fmt.Sprintf("%s://%s%s", scheme, target, path)
				out = append(out, budgetSkipIfExhausted(ctx, probe.HTTP(hctx, url, l.Hostname, vantage), vantage))
				hcancel()
			}
		}
	}
	return out
}

// isWildcardHost reports whether h is a wildcard DNS name (e.g.
// "*.example.com"). Such names are valid Ingress/Gateway config but never
// resolve as written and can't be used as TLS SNI for a specific host, so
// probing them literally produces a false failure. Callers skip them with an
// honest "test a concrete host" reason instead.
func isWildcardHost(h string) bool {
	return strings.Contains(h, "*")
}

func uniqueHosts(declared []string, rules []RouteRule) []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	for _, h := range declared {
		add(h)
	}
	for _, r := range rules {
		for _, h := range r.Hosts {
			add(h)
		}
	}
	return out
}
