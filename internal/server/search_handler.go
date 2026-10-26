package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
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
//	namespace, namespaces — explicit namespace names, unioned with ns: terms
//
// The returned shape is a search.Result. Per-cluster, no cross-cluster
// concerns — radar-hub is responsible for fan-out and re-ranking.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	q := r.URL.Query().Get("q")
	parsed := search.Parse(q)
	parsed.NSFilter = append(parsed.NSFilter, parseNamespaces(r.URL.Query())...)

	provider := search.NewCacheProvider()
	if provider == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	// Namespaced kinds scan the namespaces the user can see, intersected with
	// any `ns:` modifier; listers never read namespaces outside the user's
	// RBAC. Cluster-scoped kinds are gated per kind by CanReadClusterScoped.
	//
	// A search never inherits the stored namespace pick: it scans the user's
	// RBAC ceiling within the --namespace-scope pin, narrowed only by the
	// explicit namespace names in query terms or URL parameters. A named
	// namespace outside the pin is reported as outside_namespace_pin;
	// cluster-scoped kinds are still searched under the
	// pin, gated per kind. globalNs=1 (the omnibar's ⌘K and Radar Hub's fleet
	// search) is accepted and asks for exactly this.
	ceiling, namespaceCheckFailed := s.getUserNamespacesWithStatus(r, nil)

	include, err := parseInclude(r.URL.Query().Get("include"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	kindRBAC := s.computeSearchKindRBAC(r)
	opts := search.Options{
		TypedKindAccess: kindRBAC,
		Limit:           parseLimit(r.URL.Query().Get("limit")),
		Include:         include,
		CanReadClusterScoped: func(kind, group, resource string) (bool, bool) {
			return s.canReadDecision(r, group, resource, "", "list")
		},
		DynamicKindAccess: s.searchKindAccess(r, kindRBAC),
	}
	var pin *string
	if k8s.ForceNamespaceScope {
		target := k8s.GetNamespaceScopeTarget()
		pin = &target
	}
	opts.Selection = search.SelectNamespaces(provider, parsed, readscope.NamespaceAccess{Namespaces: ceiling, Checked: !namespaceCheckFailed}, pin)
	opts.NamespaceCheckFailed = namespaceCheckFailed
	scanNamespaces := opts.Selection.Namespaces
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
