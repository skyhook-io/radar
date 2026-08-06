package reachability

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/trace"
	"github.com/skyhook-io/radar/pkg/probe"
	"k8s.io/client-go/kubernetes"
)

// MaxInClusterProbes bounds how many probe pods one in-cluster reachability call
// will create, so a many-route Ingress can't fan out into a pod storm.
const MaxInClusterProbes = 5

// The sequential probe runs must fit inside the caller's request deadline
// (Chi's 60s timeout on the REST path): budgetSafetyMargin is reserved for the
// work after the probes (trace fold + response write), and no run starts with
// less than minRunBudget - a run that would die on the request deadline
// mid-flight reports a confusing timeout AND wastes a probe pod.
const (
	budgetSafetyMargin = 3 * time.Second
	minRunBudget       = 5 * time.Second
)

// InClusterTestResult is one route's in-cluster probe outcome. Results is the raw
// probe set when it ran; Status carries the plain-English reason when it could not
// (pod couldn't start / RBAC), with FallbackCommand to run it manually.
type InClusterTestResult struct {
	Route           string              `json:"route"`
	Target          string              `json:"target,omitempty"`
	TargetNamespace string              `json:"targetNamespace,omitempty"`
	Request         *trace.ProbeRequest `json:"request,omitempty"`
	Results         []probe.Result      `json:"results,omitempty"`
	Status          string              `json:"status,omitempty"`
	FallbackCommand string              `json:"fallbackCommand,omitempty"`
}

// InClusterTestOptions tunes one whole-subject in-cluster test run.
type InClusterTestOptions struct {
	// PathOverride, when non-empty, replaces every HTTP(S) route's request path.
	// TCP-only routes ignore it because they have no HTTP request target. An
	// override that differs from an HTTP route's declared path stays evidence-only
	// for that route: a clean probe of /healthz says nothing about /admin.
	PathOverride string
}

// RunInClusterTests runs the live in-cluster probe for each intended route that
// can be tested (skips benign scale-to-0 routes and routes with no concrete
// request guess), via the shared reachability runner under the caller's RBAC. It
// returns the per-route results for the response AND a target→results map keyed by
// trace.InClusterResultKey for trace.ApplyInClusterResults. A nil client
// (impersonation failure) or a probe that couldn't run is reported honestly per
// route with a copyable fallback command - never a panic.
//
// This is the single definition both the REST handler and the MCP diagnose tool
// call, so the two paths can't diverge (the same per-route keying + fail-toward-
// silence semantics the backend documents on InClusterResultKey).
func RunInClusterTests(ctx context.Context, tr *trace.Trace, namespace string) ([]InClusterTestResult, map[string][]probe.Result) {
	return RunInClusterTestsWithOptions(ctx, tr, namespace, InClusterTestOptions{})
}

// RunInClusterTestsWithOptions is RunInClusterTests with per-run options (the
// REST handler passes the operator's custom probe path through here).
func RunInClusterTestsWithOptions(ctx context.Context, tr *trace.Trace, namespace string, testOpts InClusterTestOptions) ([]InClusterTestResult, map[string][]probe.Result) {
	typed := k8s.ClientFromContext(ctx)
	// Self-read uses radar's OWN service-account client (k8s.GetClient), not the
	// caller's impersonated one - the deployed image is radar's knowledge. Guard
	// the typed-nil interface so a nil base client falls through to RADAR_IMAGE.
	var selfClient kubernetes.Interface
	if base := k8s.GetClient(); base != nil {
		selfClient = base
	}
	image := ResolveImage(ctx, selfClient, "")
	return runInClusterTests(ctx, typed, image, tr, namespace, testOpts)
}

// runProbe is the per-route probe entry, indirected so tests can stub a clean
// probe result and pin the fold/no-fold behavior without a live cluster.
var runProbe = Run

// runInClusterTests is the client-injected core, split from the public entry so
// the budget/override/no-eligible-route behavior is unit-testable without the
// global client singletons.
func runInClusterTests(ctx context.Context, typed kubernetes.Interface, image string, tr *trace.Trace, namespace string, testOpts InClusterTestOptions) ([]InClusterTestResult, map[string][]probe.Result) {
	tests := make([]InClusterTestResult, 0)
	byTarget := map[string][]probe.Result{}
	if tr == nil {
		return tests, byTarget
	}
	override := NormalizeProbePath(testOpts.PathOverride)

	eligible := make([]int, 0, len(tr.Routes))
	benignSkips := 0
	for i := range tr.Routes {
		r := &tr.Routes[i]
		if r.Benign {
			benignSkips++
			continue
		}
		if r.InClusterRequest == nil || r.Target == "" {
			continue
		}
		eligible = append(eligible, i)
	}
	// Zero testable routes must produce an explicit row, not silence - an empty
	// result reads as "nothing to report", which the operator can't distinguish
	// from "everything passed".
	if len(eligible) == 0 {
		return append(tests, noEligibleRoutesResult(tr, benignSkips)), byTarget
	}
	plannedRuns := len(eligible)
	if plannedRuns > MaxInClusterProbes {
		plannedRuns = MaxInClusterProbes
	}

	count := 0
	for _, i := range eligible {
		r := tr.Routes[i]
		// Copy the request so an override never mutates the trace's own route, and
		// the result row honestly records the path that was actually probed. An
		// explicit override also clears PathGuessed - the operator chose this path,
		// it is not a guessed concretization of a pattern.
		req := *r.InClusterRequest
		httpRequest := req.Protocol == "http" || req.Protocol == "https"
		declaredPath := ""
		overrideMismatch := false
		if httpRequest {
			// Every HTTP path is sanitized before it reaches the Job, not only the
			// operator override. A malformed route spec must not inject controls
			// into the probe request.
			req.Path = NormalizeProbePath(req.Path)
			if req.Path == "" {
				req.Path = "/"
			}
			declaredPath = req.Path
			if override != "" {
				normalizedOverride := NormalizeProbePath(override)
				if normalizedOverride == "" {
					normalizedOverride = "/"
				}
				overrideMismatch = normalizedOverride != declaredPath
				req.Path = normalizedOverride
				req.PathGuessed = false
			}
		} else {
			// Transport-only probes must not carry contradictory HTTP fields. The
			// protocol is the complete request contract for these routes.
			req.Scheme = ""
			req.Host = ""
			req.Path = ""
			req.PathGuessed = false
		}
		// For a cross-namespace backend (Gateway API backendRef into another ns)
		// the bare "name:port" resolves in the probe pod's namespace and hits the
		// wrong Service or NXDOMAIN. Dial an FQDN (name.ns.svc:port) so it resolves
		// the intended Service regardless of where the probe pod runs.
		dialTarget := r.Target
		if r.TargetNamespace != "" && r.TargetNamespace != namespace {
			dialTarget = fqdnDialTarget(r.Target, r.TargetNamespace)
		}
		opts := RunOptions{
			Image: image, Namespace: namespace, Target: dialTarget,
			Scheme: schemeForProtocol(req.Protocol), Host: req.Host, Path: req.Path,
			Layers: layersForProtocol(req.Protocol),
		}
		res := InClusterTestResult{Route: r.Route, Target: r.Target, TargetNamespace: r.TargetNamespace, Request: &req}
		// No impersonated client → auth/impersonation failed for EVERY route. This
		// creates no probe pod, so it must be handled BEFORE the cap/counter -
		// otherwise each nil-client route burns a probe slot and routes past the cap
		// get mislabeled "capped" instead of reflecting the auth failure.
		if typed == nil {
			res.Status = "couldn't run the in-cluster probe: no impersonated cluster client (auth/impersonation failed)"
			res.FallbackCommand = FallbackCommand(opts)
			tests = append(tests, res)
			continue
		}
		if count >= MaxInClusterProbes {
			res.Status = fmt.Sprintf("not tested in-cluster: capped at %d probe pods per call", MaxInClusterProbes)
			tests = append(tests, res)
			continue
		}
		runCtx, cancel := runBudgetContext(ctx)
		if runCtx == nil {
			res.Status = fmt.Sprintf("not tested - request time budget exhausted (%d of %d routes tested); re-run to continue", count, plannedRuns)
			tests = append(tests, res)
			continue
		}
		count++
		results, fallback, err := runProbe(runCtx, typed, opts)
		cancel()
		// Stamp the issuer HERE, not at the hop-stamping step downstream: these
		// same results are folded into the route via ApplyInClusterResults, and
		// an unstamped result is read as Radar's own. That put a successful
		// in-cluster run into a vantage the reader never sees, so the test
		// appeared to do nothing.
		for i := range results {
			results[i].Source = probe.SourceProbeJob
			results[i].Vantage = probe.VantageInCluster
			if results[i].Path == "" {
				results[i].Path = probe.PathData
			}
		}
		switch {
		case err != nil:
			res.Status = err.Error()
			res.FallbackCommand = fallback
		case !inClusterClean(results):
			// The throwaway probe pod has a DIFFERENT identity than the real client,
			// so its result stays informational and is never folded into the route
			// outcome - but the reason must match what actually happened: a failed
			// connect (NetworkPolicy / mesh mTLS) reads differently than a backend
			// that answered with a 5xx.
			res.Results = results
			res.Status = uncleanProbeStatus(results)
			res.FallbackCommand = FallbackCommand(opts)
		case overrideMismatch:
			// The clean probe tested the operator's custom path, not this route's
			// declared one. Show it as evidence but never fold it into byTarget -
			// folding would upgrade the route to verified/real for a declared path
			// that was never dialed.
			res.Results = results
			res.Status = fmt.Sprintf("tested custom path %s: the in-cluster probe got through - shown as evidence only; the route's declared path %s was not tested in-cluster, so the route's outcome is unchanged", override, declaredPath)
		case req.PathGuessed:
			// Only ONE guessed concrete path of a wildcard/regex route was probed.
			// Report it (informational) but don't fold it into byTarget - a guessed
			// path must not escalate the whole pattern route to verified-real.
			res.Results = results
			res.Status = "in-cluster probe reached the backend on a GUESSED path - the route is a pattern (wildcard/regex), so this confirms the backend is alive but not that every path the route matches is verified"
		default:
			res.Results = results
			byTarget[trace.InClusterResultKey(r.Route, r.Target, r.TargetNamespace)] = results
		}
		tests = append(tests, res)
	}
	return tests, byTarget
}

func layersForProtocol(protocol string) string {
	if protocol == "http" || protocol == "https" {
		return "tcp,http"
	}
	return "tcp"
}

func schemeForProtocol(protocol string) string {
	if protocol == "http" || protocol == "https" {
		return protocol
	}
	return ""
}

// runBudgetContext sizes one probe run to the REMAINING request deadline minus
// the safety margin. Returns (nil, nil) when what's left can't fit a meaningful
// run - the caller emits an explicit "not tested" row instead of starting a run
// that would die on the deadline with a misleading timeout. Without a deadline
// the run relies on the runner's own per-run jobTimeout cap.
func runBudgetContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	remaining := time.Until(deadline) - budgetSafetyMargin
	if remaining < minRunBudget {
		return nil, nil
	}
	// The runner's jobTimeout still caps each run at 25s; this only tightens it
	// when less than that remains.
	return context.WithTimeout(ctx, remaining)
}

// noEligibleRoutesResult is the explicit row for a subject with zero
// in-cluster-testable routes, naming why nothing ran. Gateway subjects don't
// produce routes with a concrete in-cluster request yet, so they get the
// honest "not supported" answer rather than an empty response.
func noEligibleRoutesResult(tr *trace.Trace, benignSkips int) InClusterTestResult {
	res := InClusterTestResult{Route: strings.TrimSpace(tr.Subject.Kind + " " + tr.Subject.Name)}
	switch {
	case tr.Subject.Kind == "Gateway":
		res.Status = "in-cluster testing isn't supported for Gateway subjects yet - no route carries a concrete in-cluster request to test"
	case benignSkips > 0 && benignSkips == len(tr.Routes):
		res.Status = "nothing to test in-cluster - every route's backend is intentionally scaled to zero"
	default:
		res.Status = "in-cluster testing isn't supported for this subject yet - no route carries a concrete in-cluster request to test"
	}
	return res
}

// NormalizeProbePath sanitizes an operator-chosen HTTP probe path: control
// characters stripped, leading '/', "" for an empty/whitespace-only input.
// Delegates to trace.SanitizeHTTPPath - the single definition of "sanitized
// HTTP request target" (deliberately NO path.Clean; dot segments and doubled
// slashes are part of the path the user asked for, not filesystem noise).
func NormalizeProbePath(p string) string {
	return trace.SanitizeHTTPPath(p)
}

// fqdnDialTarget rewrites a "name:port" (or bare "name") target to its
// cluster-FQDN form "name.ns.svc:port" so a cross-namespace backend resolves
// from any probe-pod namespace.
func fqdnDialTarget(target, namespace string) string {
	name, port, hasPort := strings.Cut(target, ":")
	fqdn := name + "." + namespace + ".svc"
	if hasPort {
		return fqdn + ":" + port
	}
	return fqdn
}

// inClusterClean reports whether an in-cluster probe set is an unambiguous
// reach: at least one non-skipped probe succeeded and none failed. Only a clean
// result is folded into the route verdict - a probe failure from a throwaway pod
// must never escalate the static verdict to a confident unreachable.
// uncleanProbeStatus explains WHY an in-cluster probe result was not folded, with
// the reason matching what actually happened: a transport failure (the throwaway
// pod couldn't connect - NetworkPolicy / mesh mTLS) reads differently than a
// backend that answered with a server error (5xx), which the pod DID reach.
func uncleanProbeStatus(results []probe.Result) string {
	degradedDetail := ""
	for _, p := range results {
		if p.Skipped {
			continue
		}
		if !p.OK {
			return "in-cluster probe from a throwaway pod didn't get through - this can differ from real client traffic (source-scoped NetworkPolicy / mesh mTLS), so it's not treated as a confirmed failure"
		}
		if p.Tone == probe.ToneDegraded && degradedDetail == "" {
			// ToneDegraded covers both an HTTP server error and a TLS/certificate
			// problem - the pod reached a server either way, but the two read very
			// differently, so name the one that actually happened.
			if p.Layer == probe.LayerTLS {
				degradedDetail = "the TLS handshake had a certificate problem"
			} else {
				degradedDetail = "it returned a server error"
			}
		}
	}
	if degradedDetail != "" {
		return "in-cluster probe reached the backend but " + degradedDetail + " - kept informational (the throwaway pod's identity/path/auth can differ from real clients), so it's not folded into the route outcome"
	}
	return "in-cluster probe result was inconclusive - kept informational, not folded into the route outcome"
}

func inClusterClean(results []probe.Result) bool {
	sawOK := false
	for _, p := range results {
		if p.Skipped {
			continue
		}
		if !p.OK {
			return false
		}
		// A 5xx sets OK=true (the transport reached a server) but Tone=Degraded.
		// The throwaway probe pod's identity/path/auth differs from real clients,
		// so its 5xx must stay informational - folding it would escalate the route
		// to a confident server-error condemn. Not a clean reach.
		if p.Tone == probe.ToneDegraded {
			return false
		}
		sawOK = true
	}
	return sawOK
}
