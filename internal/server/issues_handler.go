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
// endpoint. Composes problems + condition fallback by default; audit
// + event sources are opt-in (both are loud — audit findings run 50–
// 200 per cluster, and events flood with thousands of redundant
// rows on noisy clusters).
//
// Query params:
//
//	namespace= / namespaces=  one or comma-separated
//	severity=  critical,warning  (default: all)
//	source=    problem,audit,event,condition. Defaults to problem+
//	           condition (audit + event excluded). Pass any source
//	           explicitly to opt it in; "audit" lifts include_audit,
//	           "event" lifts include_events. The two flags exist so
//	           callers can opt those sources in without also
//	           narrowing to ONLY them.
//	kind=      Pod,Deployment,...  (default: all)
//	since=     duration like 15m, 1h. Affects event source only;
//	           when events are enabled and since is omitted, the
//	           handler defaults to 1h to avoid pulling the full
//	           cached event backlog.
//	limit=     default 200, max 1000
//	include_audit=true   opt audit findings in
//	include_events=true  opt warning events in
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
	sources, err := parseSources(q.Get("source"))
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
		Namespaces:    namespaces,
		Severities:    severities,
		Sources:       sources,
		Kinds:         splitCSV(q.Get("kind")),
		Since:         since,
		Limit:         parseLimit(q.Get("limit")),
		IncludeAudit:  q.Get("include_audit") == "true" || hasSource(q.Get("source"), "audit"),
		IncludeEvents: includeEvents,
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
	if stats.FilterErrors > 0 {
		resp["filter_errors"] = stats.FilterErrors
		resp["filter_error_sample"] = stats.FilterErrorSample
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

func parseSources(v string) ([]issues.Source, error) {
	if v == "" {
		return nil, nil
	}
	parts := strings.Split(v, ",")
	out := make([]issues.Source, 0, len(parts))
	for _, p := range parts {
		s := strings.ToLower(strings.TrimSpace(p))
		switch s {
		case "":
			continue
		case "problem":
			out = append(out, issues.SourceProblem)
		case "audit":
			out = append(out, issues.SourceAudit)
		case "event":
			out = append(out, issues.SourceEvent)
		case "condition":
			out = append(out, issues.SourceCondition)
		default:
			return nil, fmt.Errorf("unknown source %q (want: problem, audit, event, condition)", p)
		}
	}
	return out, nil
}

// hasSource reports whether the caller's `?source=` list explicitly
// names `target`. Used to derive the opt-in flags for audit and
// event sources — passing them in the source list is more
// discoverable than the parallel include_* booleans, and we honor
// both.
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
