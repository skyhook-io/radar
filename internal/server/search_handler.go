package server

import (
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/internal/search"
)

// handleSearch serves GET /api/search.
//
// Query params:
//
//	q       — search string (free tokens + modifiers: kind:, ns:, label:k=v, image:, cluster:)
//	limit   — max hits returned (default 50, capped at 500)
//	include — "summary" (default), "raw", or "none"
//
// The returned shape is a search.Result. Per-cluster, no cross-cluster
// concerns — radar-hub is responsible for fan-out and re-ranking.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	q := r.URL.Query().Get("q")
	parsed := search.Parse(q)

	provider := search.NewCacheProvider()
	if provider == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	// Auth-filter the namespaces the user can see, then intersect with any
	// `ns:` modifier parsed from the query. The result both gates the scan
	// (so listers don't read namespaces outside the user's RBAC) and
	// constrains the post-hoc match() filter.
	allowed := s.parseNamespacesForUser(r)
	if noNamespaceAccess(allowed) {
		s.writeJSON(w, search.Result{Hits: []search.Hit{}})
		return
	}
	scanNamespaces := intersectNamespaces(allowed, parsed.NSFilter)
	if allowed != nil && len(scanNamespaces) == 0 {
		// User is namespace-restricted but their `ns:` filter doesn't
		// intersect — empty result without scanning.
		s.writeJSON(w, search.Result{Hits: []search.Hit{}})
		return
	}
	parsed.NSFilter = scanNamespaces

	include, err := parseInclude(r.URL.Query().Get("include"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	opts := search.Options{
		Limit:      parseLimit(r.URL.Query().Get("limit")),
		Include:    include,
		Namespaces: scanNamespaces,
		// SAR-gate sensitive kinds (Secret + cluster-scoped) by the
		// END user's identity, not the SA's. The cache itself reads
		// as the SA so it carries Secrets/Nodes/etc., but exposing
		// them through search to a namespace-bound viewer would let
		// them enumerate cluster-scope info their k8s RBAC denies.
		// In auth-mode=none, returns nil and the SA's own RBAC at
		// the cache lister layer is the only filter.
		SkipKinds: s.computeSearchSkipKinds(r),
		CanReadClusterScoped: func(kind, group, resource string) bool {
			if auth.UserFromContext(r.Context()) == nil {
				return true
			}
			return s.canRead(r, group, resource, "", "list")
		},
	}
	if expr := r.URL.Query().Get("filter"); expr != "" {
		f, err := filter.CachedObjectFilter(expr)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "filter: "+err.Error())
			return
		}
		opts.Filter = f
	}

	result, err := search.Search(r.Context(), provider, parsed, opts)
	if err != nil {
		// Log before writing per radar's CLAUDE.md convention. Today
		// Search() never returns an error, but the moment that
		// changes we want context — the query string itself is the
		// most useful fingerprint.
		log.Printf("[search] failed q=%q filter=%q: %v", parsed.Raw, r.URL.Query().Get("filter"), err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, result)
}

// intersectNamespaces returns the namespaces to actually scan. nil `allowed`
// means the user is unrestricted; preserve `requested` (which may also be nil
// for cluster-wide). When the user is restricted, keep only the requested
// namespaces they're allowed to see; if `requested` is empty, fall back to
// the full allowed set.
func intersectNamespaces(allowed, requested []string) []string {
	if allowed == nil {
		return requested
	}
	if len(requested) == 0 {
		return allowed
	}
	allowSet := make(map[string]struct{}, len(allowed))
	for _, ns := range allowed {
		allowSet[ns] = struct{}{}
	}
	out := make([]string, 0, len(requested))
	for _, ns := range requested {
		if _, ok := allowSet[ns]; ok {
			out = append(out, ns)
		}
	}
	return out
}

func parseLimit(v string) int {
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func parseInclude(v string) (search.IncludeMode, error) {
	switch v {
	case "", "summary":
		return search.IncludeSummary, nil
	case "raw":
		return search.IncludeRaw, nil
	case "none":
		return search.IncludeNone, nil
	default:
		return 0, fmt.Errorf("unknown include=%q (want: summary, raw, none)", v)
	}
}
