package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/reachability"
	"github.com/skyhook-io/radar/internal/trace"
	"github.com/skyhook-io/radar/pkg/probe"
)

type probeCapabilityResponse struct {
	Allowed   bool   `json:"allowed"`
	Reason    string `json:"reason,omitempty"`
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace"`
	// MaxProbes is the per-call ceiling on probe Pods. Reported so the consent
	// screen can state what a run actually covers: route order is deterministic,
	// so routes past the cap are not merely deferred - re-running starts from the
	// same first MaxProbes and never reaches them.
	MaxProbes int `json:"maxProbes"`
}

// handleProbeInClusterCapability tells the UI whether the in-cluster test will
// actually run for this caller, so the button only appears when it works (a
// button that 403s mid-incident is worse than none). It also names the cluster +
// namespace the probe pod would be created in - the safety rail, since the
// runner creates pods in whatever cluster Radar is connected to. Cluster is the
// stable per-cluster identity (context name, or a kube-system-UID fallback for
// in-cluster deployments) - the frontend keys its "don't ask again" consent on
// it, so a shared sentinel here would let one cluster's consent silently
// suppress the pod-creating confirm on every cluster a shared Hub origin serves.
func (s *Server) handleProbeInClusterCapability(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	if !s.requireConnected(w) {
		return
	}
	// Resolved only past the connected gate: the fallback path issues a live
	// kube-system GET, which is a doomed API call while disconnected.
	resp := probeCapabilityResponse{
		Cluster:   k8s.ClusterIdentity(r.Context()),
		Namespace: namespace,
		MaxProbes: reachability.MaxInClusterProbes,
	}
	client := s.getClientForRequest(r)
	if client == nil {
		resp.Reason = "cluster client not available"
		s.writeJSON(w, resp)
		return
	}
	// Mirror the POST's namespace boundary so the capability answer matches what the
	// POST will actually allow - never report "allowed" for an out-of-scope namespace.
	namespaces := s.traceNamespaceCeiling(r)
	if noNamespaceAccess(namespaces) || (namespace != "" && !namespaceAllowed(namespaces, namespace)) {
		resp.Reason = fmt.Sprintf("no access to namespace %q", namespace)
		s.writeJSON(w, resp)
		return
	}
	// The POST gate is additive: K8s RBAC AND the Cloud-role tier (Member). Mirror
	// the Cloud-role half here (without the 403) so the capability answer matches
	// what the POST will actually do - a cloud:viewer with the right RBAC must not
	// see allowed=true and then get a 403 cloud_role_insufficient on submit.
	if !auth.CloudRoleFromContext(r.Context()).AtLeast(auth.RoleMember) {
		resp.Reason = "your Radar Cloud role cannot run an in-cluster reachability test"
		s.writeJSON(w, resp)
		return
	}
	allowed, reason, err := reachability.Capability(r.Context(), client, namespace)
	switch {
	case err != nil:
		resp.Reason = "couldn't verify permissions to run an in-cluster test: " + err.Error()
	case !allowed:
		resp.Reason = reason
	default:
		resp.Allowed = true
	}
	s.writeJSON(w, resp)
}

type traceInClusterRequest struct {
	Path string `json:"path,omitempty"`
}

type traceInClusterResponse struct {
	Trace          *trace.Trace                       `json:"trace"`
	InClusterTests []reachability.InClusterTestResult `json:"inClusterTests,omitempty"`
}

// handleTraceInCluster is the WHOLE-subject in-cluster reachability test: it
// builds the probed trace, runs the live in-cluster probe for each intended route
// (per-route, keyed by InClusterResultKey so a probe of one route can't falsely
// vouch for a sibling that shares its backend), folds the results in via the
// canonical trace.ApplyInClusterResults (which re-derives outcome/confidence/
// verdict/coverage/diagnosis and reconciles contradicted netpol predictions), and
// returns the FINALIZED trace. The frontend just displays it - there is no
// second, weaker merge that could diverge from this server-authoritative one.
func (s *Server) handleTraceInCluster(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if !trace.IsEntryKind(kind) {
		s.writeError(w, http.StatusBadRequest, "trace is only supported for Service, Ingress, HTTPRoute, GRPCRoute, or Gateway")
		return
	}
	// Creating transient probe pods is the one mutating action here - gate on the
	// Cloud-role tier every mutating handler enforces (Member), mirroring the MCP
	// diagnose(inCluster) path.
	if !s.requireCloudRole(w, r, auth.RoleMember, "run an in-cluster reachability test") {
		return
	}
	namespaces := s.traceNamespaceCeiling(r)
	// Mirror handleTrace: never leak that a resource exists outside the caller's
	// namespace scope - return an unknown-verdict trace instead.
	if noNamespaceAccess(namespaces) || (namespace != "" && !namespaceAllowed(namespaces, namespace)) {
		s.writeJSON(w, traceInClusterResponse{Trace: &trace.Trace{
			Subject:    trace.ResourceRef{Kind: kind, Namespace: namespace, Name: name},
			Downstream: []trace.Hop{},
			Upstreams:  []trace.Hop{},
			Verdict:    trace.VerdictUnknown,
			BrokenAt:   -1,
			Reason:     "no namespace access for current user",
		}})
		return
	}
	var req traceInClusterRequest
	// An empty body is fine (path is optional), but malformed JSON is a broken
	// client - silently defaulting it to "/" would create probe Jobs against a
	// path the caller never asked for.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	deps := trace.Deps{
		Cache:             k8s.GetResourceCache(),
		Dynamic:           k8s.GetDynamicResourceCache(),
		Discovery:         k8s.GetResourceDiscovery(),
		Issues:            issues.NewCacheProvider(),
		Client:            k8s.ClientFromContext(r.Context()),
		AllowedNamespaces: namespaces,
	}
	// Probe must run so routes carry their per-route InClusterRequest; an
	// in-cluster test with no probed routes would be a silent no-op.
	tr, err := trace.BuildTraceWithOptions(r.Context(), deps, kind, namespace, name, trace.Options{Probe: true, ProbePath: req.Path})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	auth.AuditLog(r, namespace, name)

	// The frontend's path form always submits something ("/" by default), so a
	// bare "/" is the untouched default, not a customization - each route keeps
	// its own declared path then. Anything else is the operator saying "test THIS
	// path", and the in-cluster probes must honor it, not silently probe the
	// declared route paths instead.
	pathOverride := reachability.NormalizeProbePath(req.Path)
	if pathOverride == "/" {
		pathOverride = ""
	}
	tests, byTarget := reachability.RunInClusterTestsWithOptions(r.Context(), tr, namespace, reachability.InClusterTestOptions{PathOverride: pathOverride})
	// Canonical fold: upgrades confidence, re-derives verdict/coverage/diagnosis,
	// and reconciles netpol would-deny predictions the live success contradicts.
	trace.ApplyInClusterResults(tr, byTarget)
	// Stamp the live probes onto their hops so the per-hop reachability matrix shows
	// the "real in-cluster traffic" column - the route outcomes already reflect them.
	stampInClusterProbes(tr, tests)
	s.writeJSON(w, traceInClusterResponse{Trace: tr, InClusterTests: tests})
}

// stampInClusterProbes appends each route's live in-cluster probe results to the
// hop that carries its backend, stamping the in-cluster vantage + data path so the
// matrix attributes them to the "real" column. A probe that didn't get through
// from the throwaway pod is recorded as a SKIP (not a confirmed break) - the same
// fail-toward-silence the route fold already applies, so the matrix never paints a
// red unreachable a different client identity could contradict.
func stampInClusterProbes(tr *trace.Trace, tests []reachability.InClusterTestResult) {
	if tr == nil {
		return
	}
	backendName := func(target string) string {
		if i := strings.LastIndex(target, ":"); i > 0 {
			return target[:i]
		}
		return target
	}
	for _, tst := range tests {
		if len(tst.Results) == 0 {
			continue
		}
		name := backendName(tst.Target)
		_, portText, _ := net.SplitHostPort(tst.Target)
		portNumber, _ := strconv.ParseInt(portText, 10, 32)
		for hi := range tr.Downstream {
			if tr.Downstream[hi].Resource.Name != name {
				continue
			}
			// Two downstream hops can share a Service name across namespaces
			// (Gateway API cross-namespace backendRef). Match the hop by name AND
			// namespace so a probe lands on its own hop; a second same-named hop
			// in another namespace gets its own probes (no early break). An empty
			// TargetNamespace means the SUBJECT's namespace (the producer omits it
			// for same-namespace backends) - it must never match by name alone, or
			// a same-ns probe stamps its live evidence onto a cross-ns hop that
			// was never dialled. An empty hop namespace reads as the subject's too.
			wantNS := tst.TargetNamespace
			if wantNS == "" {
				wantNS = tr.Subject.Namespace
			}
			hopNS := tr.Downstream[hi].Resource.Namespace
			if hopNS == "" {
				hopNS = tr.Subject.Namespace
			}
			if hopNS != wantNS {
				continue
			}
			for _, pr := range tst.Results {
				pr.Vantage = probe.VantageInCluster
				pr.Source = probe.SourceProbeJob
				if pr.Port == 0 && portNumber > 0 {
					pr.Port = int32(portNumber)
				}
				if pr.Path == "" {
					pr.Path = probe.PathData
				}
				// A throwaway-pod result is kept informational, never a confident
				// condemnation - its identity/path/auth can differ from real clients.
				// Demote BOTH a failed connect and a reached-but-degraded response
				// (OK=true, ToneDegraded - a 5xx or TLS/cert problem) to a skip, so
				// the matrix doesn't paint the path degraded when the fold logic
				// deliberately withheld it. A folded (clean) result never carries a
				// degraded probe, so this only touches unfolded results.
				if !pr.Skipped && (!pr.OK || pr.Tone == probe.ToneDegraded) {
					pr.Skipped = true
					// Structural marker: this probe RAN and was demoted - it is a
					// disposition, not a coverage gap, and the UI must never
					// render it as "never tried".
					pr.SkipClass = probe.SkipClassInformational
					if pr.Reason == "" {
						if pr.OK {
							pr.Reason = "in-cluster probe from a throwaway pod reached the backend but got a degraded response (a server error, or a TLS/certificate problem) - the pod's identity/path/auth can differ from real clients, so it's kept informational, not a confirmed failure"
						} else {
							pr.Reason = "in-cluster probe from a throwaway pod didn't get through - this can differ from real client traffic (source-scoped NetworkPolicy / mesh mTLS), so it's not treated as a confirmed failure"
						}
					}
				}
				tr.Downstream[hi].Probes = append(tr.Downstream[hi].Probes, pr)
			}
		}
	}
}
