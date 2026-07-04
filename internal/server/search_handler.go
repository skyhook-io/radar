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
	//
	// globalNs=1 makes search ignore the per-user namespace-switcher pick and
	// scan the user's full RBAC ceiling — a "global" search whose only scope is
	// the query's own `ns:` tokens. The omnibar sets it so a deliberately broad
	// ⌘K lookup isn't silently narrowed to whatever namespace the view filter
	// happens to be on. Still RBAC-bounded (getUserNamespaces filters by the
	// caller's identity); it only drops the cosmetic pick, never the ceiling.
	var allowed []string
	if r.URL.Query().Get("globalNs") == "1" {
		allowed = s.getUserNamespaces(r, parseNamespaces(r.URL.Query()))
	} else {
		allowed = s.parseNamespacesForUser(r)
	}
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
	skipKinds := s.computeSearchSkipKinds(r)
	// Secrets are namespaced and get per-namespace RBAC treatment so a user
	// with per-namespace Secret access (e.g. a Role-bound viewer) sees those
	// rows in search instead of having Secret dropped at cluster scope.
	// scanNamespaces (not allowed) is the right input: a user with
	// AllowedNamespaces==nil (cluster-wide-pods sentinel) who queries
	// `ns:team-a` should get per-namespace fanout over team-a, not a
	// cluster-scope `list secrets` SAR — they may have list-secrets in
	// team-a but not cluster-wide. nil scanNamespaces (truly cluster-wide
	// query) still routes to the cluster-scope SAR branch.
	var namespacesByKind map[string][]string
	switch decision, scoped := s.computeSearchSecretsRBAC(r, scanNamespaces); decision {
	case "skip":
		if skipKinds == nil {
			skipKinds = make(map[string]bool)
		}
		skipKinds["Secret"] = true
	case "override":
		// scoped ⊆ scanNamespaces ⊆ parsed.NSFilter already (the SAR fanout
		// iterates scanNamespaces, which is the upstream intersection of
		// allowed and parsed.NSFilter). Use the SAR result directly.
		namespacesByKind = map[string][]string{"Secret": scoped}
	}

	opts := search.Options{
		Limit:      parseLimit(r.URL.Query().Get("limit")),
		Include:    include,
		Namespaces: scanNamespaces,
		// SAR-gate sensitive cluster-scoped kinds (Node, PV, StorageClass,
		// Namespace) by the END user's identity, not the SA's. The cache
		// itself reads as the SA so it carries those rows, but exposing
		// them through search to a namespace-bound viewer would let them
		// enumerate cluster-scope info their k8s RBAC denies. Secrets get
		// per-namespace RBAC via NamespacesByKind/SkipKinds above. In
		// auth-mode=none, computeSearchSkipKinds returns nil and the SA's
		// own RBAC at the cache lister layer is the only filter.
		SkipKinds:        skipKinds,
		NamespacesByKind: namespacesByKind,
		CanReadClusterScoped: func(kind, group, resource string) bool {
			if auth.UserFromContext(r.Context()) == nil {
				return true
			}
			return s.canRead(r, group, resource, "", "list")
		},
	}
	// summaryContext attaches managedBy/health/issueCount per hit. Build
	// the per-request closure once (one Compose call + cached topology
	// snapshot) and let the search executor invoke it per kept hit.
	// ?context=none opts out so legacy callers don't pay for the join.
	//
	// Search uses the dual-index variant: hits are mixed-kind in one
	// response (namespaced Pods alongside cluster-scoped Nodes), so a
	// single-namespace-scoped issue index would zero issueCount on
	// cluster-scoped hits (whose issues live at namespace=""). The
	// builder routes per-hit by scope. SAR gating above
	// (CanReadClusterScoped) already constrains which cluster-scoped
	// kinds are reachable.
	if r.URL.Query().Get("context") != "none" {
		if builder := s.newSearchSummaryContextBuilder(scanNamespaces); builder != nil {
			opts.SummaryBuilder = search.SummaryBuilderFunc(builder)
		}
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

// parseInclude maps search's include values. Default (absent) is summary.
//
// NB: search's summary/raw are TRANSFORMED representations from the
// ai/context layer (LevelSummary micro-shape / Detail-minified body), NOT
// the same-schema strip that /api/resources/{kind}?include=summary returns.
// Same word, different shape contract — don't cross-assume.
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
