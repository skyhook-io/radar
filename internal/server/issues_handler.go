package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
)

// handleIssues serves GET /api/issues — the unified cluster-health
// endpoint. Composes problems + condition fallback by default. Events
// and Kyverno are opt-in because both are loud — events flood with
// thousands of redundant rows on noisy clusters, and Kyverno
// PolicyReports add 10+ rows per workload under a baseline PSS profile.
// Static best-practice / posture findings are intentionally not an
// issues source; use /api/audit or MCP get_cluster_audit.
//
// Query params:
//
//	namespace= / namespaces=  one or comma-separated
//	severity=  critical,warning  (default: all)
//	source=    Comma-separated list of sources to RETURN. When set,
//	           only the listed sources appear in the response.
//	           Allowed: problem, missing_ref, event, condition, kyverno.
//	           Default (no source param): problem + missing_ref +
//	           condition (event + kyverno excluded because they can
//	           flood with noisy rows). missing_ref surfaces dangling-
//	           reference errors (Pod→missing PVC/CM/Secret/SA, HPA→
//	           missing target, Ingress→missing backend, RoleBinding→
//	           missing roleRef, webhook→missing Service).
//	           NOTE: source acts as a filter, not an additive opt-in.
//	           Passing source=kyverno returns ONLY Kyverno rows, not
//	           "defaults plus Kyverno". Use include_kyverno=true (or
//	           include_events) when you want
//	           "defaults plus X".
//	include_events/include_kyverno=true
//	           Add the named source to the DEFAULT set without
//	           silencing the defaults. Effective filter:
//	           include_X=true is equivalent to source=problem,
//	           condition,X. These flags are also implicitly set when
//	           the matching source appears in source= so the warmup
//	           / collection path knows to fetch that source's data.
//	kind=      Pod,Deployment,...  (default: all)
//	since=     duration like 15m, 1h. Affects event source only;
//	           when events are enabled and since is omitted, the
//	           handler defaults to 1h to avoid pulling the full
//	           cached event backlog.
//	limit=     default 200, max 1000
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
	// they asked for" → return empty rather than leak cluster-wide rows.
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, map[string]any{"issues": []any{}, "total": 0})
		return
	}

	severities, err := parseSeverities(q.Get("severity"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sources, err := issues.ParseSources(q.Get("source"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	since, err := parseDuration(q.Get("since"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	includeEvents := q.Get("include_events") == "true" || hasSource(q.Get("source"), "event")
	// When events are enabled and no explicit window was passed, cap
	// the lookback at 1h. Without this an opt-in immediately yields
	// the full cache window (hours of accumulated Warning events,
	// most of which duplicate problem-source rows already returned).
	if includeEvents && since == 0 {
		since = time.Hour
	}
	filters := issues.Filters{
		Namespaces:     namespaces,
		Severities:     severities,
		Sources:        sources,
		Kinds:          splitCSV(q.Get("kind")),
		Since:          since,
		Limit:          parseLimit(q.Get("limit")),
		IncludeEvents:  includeEvents,
		IncludeKyverno: q.Get("include_kyverno") == "true" || hasSource(q.Get("source"), "kyverno"),
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

	out, stats := issues.ComposeWithStats(provider, filters)
	resp := map[string]any{
		"issues": out,
		"total":  len(out),
		// total_matched is the uncapped count — i.e. how many issues
		// would have been in `issues` if no limit applied. Tells the
		// caller whether they're looking at a windowed view or the
		// whole set. The hub forwards this per-cluster in fleet
		// envelopes so the SPA can render "X of N total".
		"total_matched": stats.TotalMatched,
	}
	if result := k8s.GetCachedPermissionResult(); result != nil {
		if visibility := k8s.BuildVisibilitySummary(result, k8s.VisibilityNamespace(namespaces)); visibility != nil {
			resp["visibility"] = visibility
		}
	}
	if stats.FilterErrors > 0 {
		resp["filter_errors"] = stats.FilterErrors
		resp["filter_error_sample"] = stats.FilterErrorSample
	}
	// When the caller asked for Kyverno findings (either via opt-in flag
	// or source=kyverno), surface the index lifecycle phase under
	// `meta.kyverno`. Without this, an empty list collapses four distinct
	// states (not_installed / deferred / warmup / ready-but-empty) into
	// one and the SPA + agents can't render the right copy. Emitted on
	// every kyverno-touching request — agents can ignore it, but humans
	// in the SPA get a clear "Kyverno not installed" vs "Indexing in
	// progress" vs "No violations" distinction.
	if filters.IncludeKyverno {
		meta, _ := resp["meta"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
		}
		meta["kyverno"] = provider.KyvernoStatus()
		resp["meta"] = meta
	}
	s.writeJSON(w, resp)
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

// hasSource reports whether the caller's `?source=` list explicitly
// names `target`. Used to derive the opt-in flags for event / Kyverno
// sources — passing them in the source list is more
// discoverable than the parallel include_* booleans, and we honor both.
func hasSource(v, target string) bool {
	for _, p := range strings.Split(v, ",") {
		if strings.EqualFold(strings.TrimSpace(p), target) {
			return true
		}
	}
	return false
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

func parseDuration(v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid since=%q: %w", v, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("since must be non-negative, got %s", d)
	}
	return d, nil
}
