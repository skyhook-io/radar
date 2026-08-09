package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/trace"
)

// traceNamespaceCeiling returns the caller's RBAC-allowed namespaces (nil =
// unrestricted). A trace subject is directly addressed, so gating it on the
// in-app namespace VIEW pick (parseNamespacesForUser's fallback) was wrong
// twice over: page navigation races the pick save, so a freshly-opened detail
// page read the previous page's pick and claimed "RBAC denies access" on a
// fully readable resource; and the pick redacted cross-namespace fan-out (a
// parent Gateway living in its own namespace) the caller is allowed to see.
// Mirror preflightResourceGet: RBAC only. ForceNamespaceScope still wins -
// in that mode nothing outside the target namespace may be served.
func (s *Server) traceNamespaceCeiling(r *http.Request) []string {
	if k8s.ForceNamespaceScope {
		target := k8s.GetNamespaceScopeTarget()
		if target == "" {
			return []string{}
		}
		return s.getUserNamespaces(r, []string{target})
	}
	return s.getUserNamespaces(r, nil)
}

// handleTrace returns a path-shaped diagnosis for one resource.
// GET /api/trace/{kind}/{namespace}/{name}
func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
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

	namespaces := s.traceNamespaceCeiling(r)
	// Mirror handleAuditResource: when the user has no namespace access
	// (RBAC trims the set to empty), return an unknown-verdict trace
	// instead of leaking that the resource even exists.
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, &trace.Trace{
			Subject:    trace.ResourceRef{Kind: kind, Namespace: namespace, Name: name},
			Downstream: []trace.Hop{},
			Upstreams:  []trace.Hop{},
			Verdict:    trace.VerdictUnknown,
			BrokenAt:   -1,
			Reason:     "no namespace access for current user",
		})
		return
	}
	if namespace != "" && !namespaceAllowed(namespaces, namespace) {
		s.writeJSON(w, &trace.Trace{
			Subject:    trace.ResourceRef{Kind: kind, Namespace: namespace, Name: name},
			Downstream: []trace.Hop{},
			Upstreams:  []trace.Hop{},
			Verdict:    trace.VerdictUnknown,
			BrokenAt:   -1,
			Reason:     "RBAC denies access to this namespace",
		})
		return
	}

	deps := trace.Deps{
		Cache:     k8s.GetResourceCache(),
		Dynamic:   k8s.GetDynamicResourceCache(),
		Discovery: k8s.GetResourceDiscovery(),
		Issues:    issues.NewCacheProvider(),
		// Probes call services/proxy + pods/proxy on this client. Use the
		// per-request impersonated identity (or the SA when auth is disabled)
		// so the apiserver enforces the caller's RBAC on the proxy verbs -
		// not radar's broader SA permissions. ClientFromContext returns nil
		// on impersonation failure; the probe layer treats nil as "skip the
		// apiserver path," which is the correct fail-closed behavior.
		Client: k8s.ClientFromContext(r.Context()),
		// Carry the per-user namespace allow-list into the trace walk so
		// cross-namespace fan-out (Route backendRefs, parent Gateways,
		// upstream Routes for a Service) is redacted when the dependent
		// is outside scope. Without this, the cluster-wide cache would
		// leak Service config + pod IPs + findings the handler did not
		// authorize. A nil list means single-user / auth-disabled, where
		// every namespace is in scope; the non-nil empty (no access) case
		// is handled above by noNamespaceAccess before we reach here.
		AllowedNamespaces: namespaces,
	}
	opts := trace.Options{
		Probe: queryTrue(r, "probe"),
		// Optional operator-chosen HTTP path (e.g. /healthz) for the L7 probes;
		// defaults to "/" inside the trace package.
		ProbePath: r.URL.Query().Get("path"),
	}
	result, err := trace.BuildTraceWithOptions(r.Context(), deps, kind, namespace, name, opts)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, result)
}
