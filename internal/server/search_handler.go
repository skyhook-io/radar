package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/internal/summarycontext"
	"github.com/skyhook-io/radar/pkg/readreason"
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

	// Namespaced kinds scan the namespaces the user can see, intersected with
	// any `ns:` modifier; listers never read namespaces outside the user's
	// RBAC. Cluster-scoped kinds are gated per kind by CanReadClusterScoped.
	//
	// Without globalNs=1, the per-user namespace-switcher pick (or the
	// --namespace-scope namespace) narrows the scan; an `ns:` term naming a
	// namespace it leaves out is reported as outside_namespace_selection, or
	// outside_namespace_scope under the pin.
	// globalNs=1 drops the cosmetic pick and scans the user's full RBAC
	// ceiling (within the pin) — a "global" search whose only scope is the
	// query's own `ns:` tokens. The omnibar sets it so a deliberately broad ⌘K
	// lookup isn't narrowed to whatever namespace the view filter is on.
	ceiling, namespaceCheckFailed := s.getUserNamespacesWithStatus(r, parseNamespaces(r.URL.Query()))
	visible := ceiling
	switch {
	case namespaceCheckFailed:
	case r.URL.Query().Get("globalNs") == "1":
		// globalNs drops the saved pick, never the --namespace-scope pin.
		visible = s.capacityNamespacesForUser(r)
	default:
		visible = s.parseNamespacesForUser(r)
	}

	include, err := parseInclude(r.URL.Query().Get("include"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	opts := search.Options{
		NamespacedRBAC: func(namespaces []string, group, resource string) (string, []string) {
			return s.computeSearchKindRBAC(r, namespaces, group, resource)
		},
		Limit:   parseLimit(r.URL.Query().Get("limit")),
		Include: include,
		CanReadClusterScoped: func(kind, group, resource string) (bool, bool) {
			return s.canReadDecision(r, group, resource, "", "list")
		},
	}
	opts.Pinned = k8s.ForceNamespaceScope
	opts.ScopeNamespaces(parsed, visible, ceiling)
	opts.NamespaceCheckFailed = namespaceCheckFailed
	scanNamespaces := opts.Namespaces
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
		if builder := summarycontext.Within(summarycontext.SearchBudget, func() summarycontext.Builder {
			return s.newSearchSummaryContextBuilder(r, scanNamespaces)
		}); builder != nil {
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
	if err == nil && !provider.Current() {
		err = search.ErrConnectionChanged
	}
	if errors.Is(err, search.ErrConnectionChanged) {
		s.writeErrorCode(w, http.StatusConflict, string(readreason.ConnectionChanged), err.Error())
		return
	}
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
