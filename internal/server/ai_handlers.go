package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	bpaudit "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/policyreports"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/topology"
)

// policyReportLookupAdapter wraps internal/k8s.GetPolicyReportIndex() into
// the resourcecontext.PolicyReportLookup interface, translating the
// richer pkg/policyreports.Finding shape (which carries Severity +
// Category) into the agent-facing resourcecontext.KyvernoFinding shape
// (Policy / Rule / Result / Message only). Keeping the projection narrow
// here lets unrelated changes to policyreports.Finding evolve without
// perturbing the wire contract that downstream callers depend on.
type policyReportLookupAdapter struct {
	idx *policyreports.Index
}

func (a policyReportLookupAdapter) FindingsFor(kind, namespace, name string) []resourcecontext.KyvernoFinding {
	if a.idx == nil {
		return nil
	}
	findings := a.idx.FindingsFor(kind, namespace, name)
	if len(findings) == 0 {
		return nil
	}
	out := make([]resourcecontext.KyvernoFinding, len(findings))
	for i, f := range findings {
		out[i] = resourcecontext.KyvernoFinding{
			Policy:  f.Policy,
			Rule:    f.Rule,
			Result:  f.Result,
			Message: f.Message,
		}
	}
	return out
}

// parseVerbosity reads the ?verbosity= query parameter and returns the matching level.
func parseVerbosity(r *http.Request, defaultLevel aicontext.VerbosityLevel) aicontext.VerbosityLevel {
	switch r.URL.Query().Get("verbosity") {
	case "summary":
		return aicontext.LevelSummary
	case "detail":
		return aicontext.LevelDetail
	case "compact":
		return aicontext.LevelCompact
	default:
		return defaultLevel
	}
}

// handleAIListResources returns a minified list of resources for AI consumption.
// GET /api/ai/resources/{kind}?namespace=X&group=X&verbosity=summary|detail|compact
func (s *Server) handleAIListResources(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kind := chi.URLParam(r, "kind")
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, []any{})
		return
	}
	group := r.URL.Query().Get("group")
	level := parseVerbosity(r, aicontext.LevelSummary)

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	// Try typed cache first
	objs, err := k8s.FetchResourceList(cache, kind, namespaces)
	if err == k8s.ErrUnknownKind {
		// Fall through to dynamic cache for CRDs
		s.aiListDynamic(w, r, cache, kind, namespaces, group, level)
		return
	}
	if err != nil {
		if strings.HasPrefix(err.Error(), "forbidden:") {
			s.writeError(w, http.StatusForbidden, fmt.Sprintf("insufficient permissions to list %s", kind))
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	results, err := aicontext.MinifyList(objs, level)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, results)
}

// aiListDynamic handles the CRD/dynamic fallback for AI list.
func (s *Server) aiListDynamic(w http.ResponseWriter, r *http.Request, cache *k8s.ResourceCache, kind string, namespaces []string, group string, level aicontext.VerbosityLevel) {
	var allItems []*unstructured.Unstructured

	if len(namespaces) > 0 {
		for _, ns := range namespaces {
			items, err := cache.ListDynamicWithGroup(r.Context(), kind, ns, group)
			if err != nil {
				if strings.Contains(err.Error(), "unknown resource kind") {
					s.writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				s.writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			allItems = append(allItems, items...)
		}
	} else {
		items, err := cache.ListDynamicWithGroup(r.Context(), kind, "", group)
		if err != nil {
			if strings.Contains(err.Error(), "unknown resource kind") {
				s.writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			s.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		allItems = items
	}

	results := make([]any, 0, len(allItems))
	for _, item := range allItems {
		results = append(results, aicontext.MinifyUnstructured(item, level))
	}

	s.writeJSON(w, results)
}

// handleAIGetResource returns a single minified resource for AI consumption,
// wrapped with a resourceContext enrichment block by default.
//
// GET /api/ai/resources/{kind}/{namespace}/{name}
//
// Query params:
//   - group=X         API group disambiguator for CRDs.
//   - verbosity=...   summary | detail | compact (default: detail).
//   - context=none    Skip resourceContext build, return bare minified resource.
//
// Response shape (default):
//
//	{ "resource": <minified>, "resourceContext": { ...basic tier... } }
//
// Response shape (context=none):
//
//	<minified>
func (s *Server) handleAIGetResource(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kind := normalizeKind(chi.URLParam(r, "kind"))
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	group := r.URL.Query().Get("group")
	level := parseVerbosity(r, aicontext.LevelDetail)
	skipContext := r.URL.Query().Get("context") == "none"

	// Handle cluster-scoped resources: "_" is used as placeholder for empty namespace
	if namespace == "_" {
		namespace = ""
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	obj, isUnstructured, err := s.fetchAIResource(r.Context(), cache, kind, namespace, name, group)
	if err != nil {
		s.writeAIFetchError(w, kind, err)
		return
	}

	if !isUnstructured {
		k8s.SetTypeMeta(obj)
	}

	minified, err := minifyForAI(obj, isUnstructured, level)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if skipContext {
		s.writeJSON(w, minified)
		return
	}

	rc := s.buildAIResourceContext(r, obj, kind, namespace, name)
	s.writeJSON(w, map[string]any{
		"resource":        minified,
		"resourceContext": rc,
	})
}

// fetchAIResource resolves the resource from the typed cache or dynamic cache.
// The bool reports whether the returned object is an unstructured (CRD) value.
func (s *Server) fetchAIResource(ctx context.Context, cache *k8s.ResourceCache, kind, namespace, name, group string) (runtime.Object, bool, error) {
	obj, err := k8s.FetchResource(cache, kind, namespace, name)
	if err == nil {
		return obj, false, nil
	}
	if err != k8s.ErrUnknownKind {
		return nil, false, err
	}
	u, dynErr := cache.GetDynamicWithGroup(ctx, kind, namespace, name, group)
	if dynErr != nil {
		return nil, false, dynErr
	}
	return u, true, nil
}

// writeAIFetchError maps fetch errors to HTTP status codes. Mirrors the
// previous inline behavior so consumers don't see a status-code drift.
func (s *Server) writeAIFetchError(w http.ResponseWriter, kind string, err error) {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "forbidden:"):
		s.writeError(w, http.StatusForbidden, fmt.Sprintf("insufficient permissions to access %s", kind))
	case strings.Contains(msg, "unknown resource kind"):
		s.writeError(w, http.StatusBadRequest, msg)
	case strings.Contains(msg, "not found"):
		s.writeError(w, http.StatusNotFound, msg)
	default:
		s.writeError(w, http.StatusNotFound, msg)
	}
}

// minifyForAI dispatches to the right Minify variant based on whether the
// resource is unstructured (CRD) or typed.
func minifyForAI(obj runtime.Object, isUnstructured bool, level aicontext.VerbosityLevel) (any, error) {
	if isUnstructured {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("internal: object marked unstructured but is %T", obj)
		}
		return aicontext.MinifyUnstructured(u, level), nil
	}
	return aicontext.Minify(obj, level)
}

// buildAIResourceContext assembles the Options struct and calls Build.
// Returns the populated context — never nil unless obj is nil.
func (s *Server) buildAIResourceContext(r *http.Request, obj runtime.Object, kind, namespace, name string) *resourcecontext.ResourceContext {
	if obj == nil {
		return nil
	}
	cache := k8s.GetResourceCache()

	issueSum := computeIssueSummaryForResource(cache, kind, namespace, name)
	auditSum := computeAuditSummaryForResource(cache, namespace, name)

	opts := resourcecontext.Options{
		Tier:          resourcecontext.TierBasic,
		AccessChecker: s.newRequestScopedChecker(r),
		EmitHints:     true,
		IssueSummary:  issueSum,
		AuditSummary:  auditSum,
	}

	// Wire the PolicyReport index when Kyverno is installed. Build emits a
	// counts-only `policySummary.kyverno` on the basic tier; diagnostic
	// tier (T10) will surface the top[] findings.
	if idx := k8s.GetPolicyReportIndex(); idx != nil {
		opts.PolicyReports = policyReportLookupAdapter{idx: idx}
	}

	if topo, prov, dyn, ok := s.topologyForContext(namespace); ok {
		opts.Topology = topo
		opts.Provider = prov
		opts.DynamicProv = dyn
	}

	return resourcecontext.Build(r.Context(), obj, opts)
}

// topologyForContext builds (or fetches the memoized) topology scoped to the
// resource's namespace. Cluster-scoped resources get an all-namespaces build.
// Returns ok=false when the cache isn't ready yet.
func (s *Server) topologyForContext(namespace string) (*topology.Topology, topology.ResourceProvider, topology.DynamicProvider, bool) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, nil, false
	}
	opts := topology.DefaultBuildOptions()
	if namespace != "" {
		opts.Namespaces = []string{namespace}
	}
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true

	provider := k8s.NewTopologyResourceProvider(cache)
	dyn := k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())

	topo, err := s.topoMemo.Get(opts, func() (*topology.Topology, error) {
		return topology.NewBuilder(provider).WithDynamic(dyn).Build(opts)
	})
	if err != nil || topo == nil {
		return nil, nil, nil, false
	}
	return topo, provider, dyn, true
}

// computeIssueSummaryForResource rolls up per-resource issue-composer rows
// (problem + condition + optional audit) into an IssueSummary.
//
// The composer is the canonical "what's wrong with this resource" surface —
// it merges problem detection (Deployment/DS/etc.), pod-level conditions,
// and generic CRD condition fallback. Filtering to a single (kind, name)
// is done client-side; the composer's native namespace filter restricts the
// scan to the resource's namespace so we don't walk the whole cluster.
//
// Returns nil when no issues match — Build then omits the IssueSummary field.
func computeIssueSummaryForResource(cache *k8s.ResourceCache, kind, namespace, name string) *resourcecontext.IssueSummary {
	if cache == nil {
		return nil
	}
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}
	filters := issues.Filters{
		Kinds: []string{kind},
		Limit: issues.MaxLimit,
	}
	if namespace != "" {
		filters.Namespaces = []string{namespace}
	}
	rows, _ := issues.ComposeWithStats(provider, filters)

	var count int
	var topReason string
	var topSeverity issues.Severity
	bySource := make(map[string]int)
	for _, row := range rows {
		if row.Name != name {
			continue
		}
		if namespace != "" && row.Namespace != namespace {
			continue
		}
		count++
		bySource[string(row.Source)]++
		if topSeverity == "" || composeSeverityRank(row.Severity) > composeSeverityRank(topSeverity) {
			topSeverity = row.Severity
			topReason = row.Reason
		}
	}
	if count == 0 {
		return nil
	}
	return &resourcecontext.IssueSummary{
		Count:           count,
		HighestSeverity: string(topSeverity),
		TopReason:       topReason,
		BySource:        bySource,
	}
}

// composeSeverityRank orders issues.Severity for highest-wins rollup.
func composeSeverityRank(s issues.Severity) int {
	switch s {
	case issues.SeverityCritical:
		return 2
	case issues.SeverityWarning:
		return 1
	}
	return 0
}

// computeAuditSummaryForResource looks up audit findings for the subject
// resource. Uses pkg/audit.IndexByResource so the lookup is keyed on the
// canonical (Kind/ns/name) tuple — handles plural→singular normalization
// via the Finding.Kind values written by the check runner.
func computeAuditSummaryForResource(cache *k8s.ResourceCache, namespace, name string) *resourcecontext.AuditSummary {
	if cache == nil {
		return nil
	}
	results := audit.RunFromCache(cache, []string{namespace}, nil)
	if results == nil || len(results.Findings) == 0 {
		return nil
	}
	idx := bpaudit.IndexByResource(results.Findings)
	var match []bpaudit.Finding
	for key, fs := range idx {
		parts := strings.SplitN(key, "/", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[1] == namespace && parts[2] == name {
			match = append(match, fs...)
		}
	}
	if len(match) == 0 {
		return nil
	}

	var topSeverity, topFinding string
	for _, f := range match {
		if topSeverity == "" || auditSeverityRank(f.Severity) > auditSeverityRank(topSeverity) {
			topSeverity = f.Severity
			topFinding = f.CheckID
		}
	}
	return &resourcecontext.AuditSummary{
		Count:           len(match),
		HighestSeverity: topSeverity,
		TopFinding:      topFinding,
	}
}

// auditSeverityRank orders audit finding severities ("danger" > "warning").
func auditSeverityRank(s string) int {
	switch s {
	case bpaudit.SeverityDanger:
		return 2
	case bpaudit.SeverityWarning:
		return 1
	}
	return 0
}
