package server

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
)

// handleIssues serves GET /api/issues — "what's broken right now."
// Composes the curated operational sources (workload/pod problems,
// dangling references, pod-startup blockers, and False CRD conditions),
// severity-ranked. Raw Warning events live at /api/events + the timeline;
// policy posture (Kyverno) and static best-practice findings live in
// /api/audit. Those are deliberately NOT issue sources — detection
// provenance is not a triage axis, so there is no source= filter (the
// `source` field is still on each returned row, and filter= CEL can slice
// on it for power users).
//
// Query params:
//
//	namespace= / namespaces=  one or comma-separated
//	severity=  critical,warning  (default: all)
//	kind=      Pod,Deployment,...  (default: all)
//	filter=    optional CEL predicate over each row (bindings include source)
//	limit=     default 200, max 1000 (counts issue groups, not member objects)
//	view=      flat → raw pre-fold evidence rows (debug); default → grouped
func (s *Server) handleIssues(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	provider := issues.NewCacheProvider()
	if provider == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	q := r.URL.Query()

	// Auth-filter the requested namespaces. nil = "all namespaces" (user
	// is unrestricted); non-nil empty = "user has no access to anything
	// they asked for".
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		// If the caller EXPLICITLY named namespace(s) they can't access, that's
		// a denial — surface it as 403, not an empty (reads-as-"nothing broken")
		// list. Bad trust boundary otherwise, especially for an agent.
		if q.Get("namespace") != "" || q.Get("namespaces") != "" {
			s.writeError(w, http.StatusForbidden, "no access to the requested namespace(s)")
			return
		}
		s.writeJSON(w, map[string]any{"issues": []any{}, "total": 0, "total_matched": 0})
		return
	}

	severities, err := parseSeverities(q.Get("severity"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filters := issues.Filters{
		Namespaces: namespaces,
		Severities: severities,
		Kinds:      splitCSV(q.Get("kind")),
		Limit:      parseLimit(q.Get("limit")),
		// Grouped is the product default — one row per subject+category.
		// ?view=flat returns the raw pre-fold evidence rows for debugging
		// ("what folded into this group?") and internal inspection.
		Grouped: q.Get("view") != "flat",
		CanReadClusterScoped: func(kind, group string) bool {
			if auth.UserFromContext(r.Context()) == nil {
				return true
			}
			clusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(kind, group)
			if !clusterScoped {
				return false
			}
			return s.canRead(r, gvrGroup, gvrResource, "", "list")
		},
	}
	if expr := q.Get("filter"); expr != "" {
		f, err := filter.CachedIssueFilter(expr)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "filter: "+err.Error())
			return
		}
		filters.Filter = f
	}

	composeFilters := filters
	composeFilters.Limit = issues.NoLimit
	composeFilters.Filter = nil
	out, stats := issues.ComposeWithStats(provider, composeFilters)
	out, stats = issues.MergeExternalIssues(out, stats, filters, s.nativeHelmIssuesForRequest(r, namespaces, filters))
	// Shared base response shape (issues.ListResponse); surfaces add their
	// own enrichments after this point.
	resp := issues.NewListResponse(out, stats)
	resp.ClusterContext = provider.ClusterContextForIssues(namespaces, func(group, resource string) bool {
		return s.canRead(r, group, resource, "kube-system", "list")
	})
	if len(namespaces) == 1 && stats.TotalMatched == len(out) && meaningfulchanges.IssueChangesQueryEligible(q.Get("kind"), q.Get("filter"), q.Get("severity")) {
		if recentChangesReason := meaningfulchanges.IssueChangesReason(out); recentChangesReason != "" {
			if changes, _, err := meaningfulchanges.Recent(r.Context(), meaningfulchanges.Query{
				Namespaces: []string{namespaces[0]},
				Since:      meaningfulchanges.DefaultSince,
				Limit:      meaningfulchanges.IssueChangesLimit,
				FieldLimit: meaningfulchanges.DefaultFieldLimit,
			}); err == nil && len(changes) > 0 {
				resp.RecentChanges = changes
				resp.RecentChangesReason = recentChangesReason
			}
		}
	}
	if result := k8s.GetCachedPermissionResult(); result != nil {
		if visibility := k8s.BuildVisibilitySummary(result, k8s.VisibilityNamespace(namespaces)); visibility != nil {
			resp.Visibility = visibility
		}
	}
	s.writeJSON(w, resp)
}

func (s *Server) nativeHelmIssuesForRequest(r *http.Request, namespaces []string, filters issues.Filters) []issues.Issue {
	if !issues.KindFilterIncludes(filters.Kinds, "HelmRelease", "helmreleases") {
		return nil
	}
	helmClient := helm.GetClient()
	if helmClient == nil {
		return nil
	}
	username, groups := "", []string(nil)
	if user := auth.UserFromContext(r.Context()); user != nil {
		username = user.Username
		groups = user.Groups
	}
	helmNamespaces := namespaces
	if helmNamespaces == nil {
		var ok bool
		helmNamespaces, ok = s.resolveHelmNamespaces(r)
		if !ok {
			return nil
		}
	}
	releases, err := helmClient.ListReleasesAcrossNamespaces(helmNamespaces, username, groups)
	if err != nil {
		if !helm.IsForbiddenError(err) {
			log.Printf("[issues] Failed to list Helm releases for issue stream: %v", err)
		}
		return nil
	}
	return issues.NativeHelmReleaseIssues(releases, time.Now())
}

// handleResourceIssues serves GET /api/issues/resource/{kind}/{namespace}/{name}
// — the live Issues that touch ONE resource: its own issues plus, for a workload,
// the issues on its owned pods (owner rollup). Backs the "Operational Issues"
// section in the resource detail. Namespace "_" denotes a cluster-scoped resource;
// optional ?group= disambiguates a CRD whose kind collides with a core kind.
//
// RBAC: namespaced targets are gated by the namespace auth-filter (the frontend
// passes ?namespaces=<ns> to scope the scan); cluster-scoped targets are gated by
// the same list permission /api/issues uses, so this can't surface a node's
// issues to a user who can't list nodes.
func (s *Server) handleResourceIssues(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	provider := issues.NewCacheProvider()
	if provider == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}
	rawKind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if namespace == "_" { // cluster-scoped sentinel
		namespace = ""
	}
	group := r.URL.Query().Get("group")

	// Authorize exactly like the resource drawer's GET (preflightResourceGet):
	// cluster-scoped get-SAR (fails closed), namespace access, and the
	// per-namespace Secret get-SAR — so this can't surface issues for a resource
	// the caller couldn't open in the drawer.
	if status, msg, ok := s.preflightResourceGet(r, normalizeKind(rawKind), namespace, name, group); !ok {
		s.writeError(w, status, msg)
		return
	}

	// RelatedIssues matches by canonical Kind (EqualFold). Resolve the route's
	// plural name to the canonical Kind via discovery — covers every kind + CRDs
	// (jobs, cronjobs, nodes, pvcs, hpas, pdbs, …), so a direct API consumer
	// passing a plural can't silently get an empty result. Canonical (PascalCase)
	// input passes straight through the rawKind fallback when discovery can't
	// resolve it (e.g. not yet connected).
	kind := rawKind
	if disc := k8s.GetResourceDiscovery(); disc != nil {
		if gvr, ok := disc.GetGVRWithGroup(rawKind, group); ok {
			if canonical := disc.GetKindForGVR(gvr); canonical != "" {
				kind = canonical
			}
		}
	}

	// Scope the scan to the resource's namespace (a workload's owned pods live
	// there too); cluster-scoped resources scan all namespaces (nil).
	var namespaces []string
	if namespace != "" {
		namespaces = []string{namespace}
	}

	related := issues.RelatedIssues(provider, namespaces, group, kind, namespace, name)
	if related == nil {
		related = []issues.Issue{}
	}
	s.writeJSON(w, related)
}

func parseSeverities(v string) ([]issues.Severity, error) {
	if v == "" {
		return nil, nil
	}
	parts := strings.Split(v, ",")
	out := make([]issues.Severity, 0, len(parts))
	for _, p := range parts {
		s := strings.ToLower(strings.TrimSpace(p))
		switch s {
		case "":
			continue
		case "critical":
			out = append(out, issues.SeverityCritical)
		case "warning":
			out = append(out, issues.SeverityWarning)
		default:
			return nil, fmt.Errorf("unknown severity %q (want: critical, warning)", p)
		}
	}
	return out, nil
}

func splitCSV(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
