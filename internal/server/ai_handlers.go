// /api/ai/* is the REST mirror of the MCP agent surface. Both target AI
// consumers (Claude, scripted agents) rather than the SPA, and both
// intentionally evolve at agent-iteration speed.
//
// Unlike /api/* (consumed by the SPA via a generated TypeScript client),
// the /api/ai/* surface is NOT specified in openapi.yaml. The original
// motivation for OpenAPI-first in radar was frontend/backend type safety —
// one spec, regenerated as Go server stubs + TS client. That value
// proposition does not apply here: the agent consumer doesn't read
// OpenAPI specs (it reads MCP tool descriptions or in-prompt instructions),
// and the SPA doesn't call these endpoints at all.
//
// Wire shapes for the agent surface live in pkg/resourcecontext (typed
// JSON DTOs) and pkg/topology. MCP tools document their wire via
// jsonschema struct tags. /api/ai/* follows the same code-defined
// discipline as MCP, treating them as one logical surface served over
// two protocols.
//
// Revisit this opt-out when:
//
//	(a) the agent surface stabilizes (no major shape changes for two
//	    release cycles), AND
//	(b) Skyhook commits to a public customer-facing AI SDK that needs
//	    generated bindings.
//
// Until both conditions are met, bringing /api/ai/* under openapi.yaml
// is premature — it would pay the spec-authoring tax during evolution
// without earning the SDK-generation benefit.
package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/summarycontext"
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

func (a policyReportLookupAdapter) FindingsFor(group, kind, namespace, name string) []resourcecontext.KyvernoFinding {
	if a.idx == nil {
		return nil
	}
	findings := a.idx.FindingsFor(group, kind, namespace, name)
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

type serviceBackendLookup struct {
	cache *k8s.ResourceCache
}

func (l serviceBackendLookup) PodsForServiceSelector(namespace string, selector labels.Selector) ([]*corev1.Pod, error) {
	if l.cache == nil || l.cache.Pods() == nil {
		return nil, nil
	}
	return l.cache.Pods().Pods(namespace).List(selector)
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
// GET /api/ai/resources/{kind}?namespace=X&group=X&verbosity=summary|detail|compact&context=none
//
// summaryContext (managedBy + health + issueCount) is attached per row
// at Summary verbosity by default. Pass ?context=none to opt out for a
// bare list.
func (s *Server) handleAIListResources(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kind := normalizeKind(chi.URLParam(r, "kind"))
	group := r.URL.Query().Get("group")
	level := parseVerbosity(r, aicontext.LevelSummary)
	skipContext := r.URL.Query().Get("context") == "none"

	// parseNamespacesForUser primes the per-user perm cache. preflightResourceList
	// then enforces the same RBAC gates as the REST list path (cluster-scoped
	// SAR for cluster-only kinds, list-namespaces SAR for `kind=namespaces`,
	// per-namespace and/or cluster-wide list-secrets SAR for `kind=secrets`).
	// AI callers get an explicit 403 on deny instead of the empty-list shape
	// the REST handler returns for backward compat.
	namespaces := s.parseNamespacesForUser(r)
	finalNamespaces, status, msg, ok := s.preflightResourceList(r, kind, group, namespaces)
	if !ok {
		s.writeError(w, status, msg)
		return
	}
	namespaces = finalNamespaces

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	// When a group is specified, route straight to the dynamic cache so
	// CRDs whose plural collides with a core kind (e.g. Knative
	// serving.knative.dev/Service vs corev1 ""/Service, KEDA's HPA-like
	// kinds) reach the right resource. FetchResourceList is group-blind
	// — it would silently return the core typed list, dropping the
	// query's group filter on the floor. Mirrors the same group-aware
	// short-circuit in handleGetResource (PR #721).
	if group != "" {
		s.aiListDynamic(w, r, cache, kind, namespaces, group, level, skipContext)
		return
	}

	// Try typed cache first (group=="" → core/built-in lookup).
	objs, err := k8s.FetchResourceList(cache, kind, namespaces)
	if err == k8s.ErrUnknownKind {
		// Fall through to dynamic cache for CRDs
		s.aiListDynamic(w, r, cache, kind, namespaces, group, level, skipContext)
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

	// Attach summaryContext per row at Summary verbosity. Compact/Detail
	// already carry richer context on the get-resource path; the
	// per-row attachment is specifically for cheap list triage.
	//
	// For cluster-scoped kinds (Node, PV, cluster-scoped CRDs) issues
	// live at namespace="" — scoping the issue index to the user's
	// namespace set would silently zero issueCount on every row. The
	// preflight RBAC above has already authorized cluster-scoped reads,
	// so we pass nil here to compose cluster-wide.
	if !skipContext && level == aicontext.LevelSummary {
		idxNamespaces := issueIndexNamespaces(namespaces, kind, group)
		if builder := s.newResourceSummaryContextBuilder(idxNamespaces); builder != nil {
			// Typed list resolves group from each object's TypeMeta —
			// MinifyList sets it via SetTypeMeta before producing rows,
			// so we can trust apiVersion on the typed source.
			summarycontext.AttachToTypedList(results, objs, builder)
		}
	}

	s.writeJSON(w, results)
}

// issueIndexNamespaces returns the namespace slice to scope the issue
// index by. For cluster-scoped kinds (Node, PV, cluster-scoped CRDs)
// returns nil so cluster-scoped issues (which live at namespace="") are
// not filtered out by the user's namespace-restricted access set.
// Namespaced kinds pass through unchanged.
func issueIndexNamespaces(namespaces []string, kind, group string) []string {
	clusterScoped, _, _ := k8s.ClassifyKindScope(kind, group)
	if clusterScoped {
		return nil
	}
	return namespaces
}

// aiListDynamic handles the CRD/dynamic fallback for AI list.
func (s *Server) aiListDynamic(w http.ResponseWriter, r *http.Request, cache *k8s.ResourceCache, kind string, namespaces []string, group string, level aicontext.VerbosityLevel, skipContext bool) {
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

	if !skipContext && level == aicontext.LevelSummary {
		idxNamespaces := issueIndexNamespaces(namespaces, kind, group)
		if builder := s.newResourceSummaryContextBuilder(idxNamespaces); builder != nil {
			summarycontext.AttachToUnstructuredList(results, allItems, builder)
		}
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

	// Run the same RBAC preflight as handleGetResource — the AI endpoint
	// returns the same resource bytes (just minified) and must gate on the
	// same per-user SAR / namespace-access tuple. Without this, a user with
	// no `get secrets` SAR could read Secret values via /api/ai/resources/…
	// even though /api/resources/… correctly returns 403. Runs BEFORE the
	// fetch so cluster-scoped denies don't leak existence by status code.
	if status, msg, ok := s.preflightResourceGet(r, kind, namespace, name, group); !ok {
		s.writeError(w, status, msg)
		return
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
//
// When a group is provided, the typed cache is skipped entirely and the
// dynamic cache is consulted with the group qualifier. This prevents kind
// collisions where a CRD plural shadows a core kind (e.g., Knative
// serving.knative.dev/Service vs core/v1 Service): without this branch,
// FetchResource("services", ...) would return the core Service from the
// typed informer and the requested group would never be consulted, leaking
// the wrong object via the AI surface. Mirrors handleGetResource's
// group-first dispatch in server.go.
func (s *Server) fetchAIResource(ctx context.Context, cache *k8s.ResourceCache, kind, namespace, name, group string) (runtime.Object, bool, error) {
	if group != "" {
		u, err := cache.GetDynamicWithGroup(ctx, kind, namespace, name, group)
		if err != nil {
			return nil, false, err
		}
		return u, true, nil
	}
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
		// Unknown errors are server-side problems (e.g. "resource discovery
		// not initialized", "dynamic resource cache not initialized") — surface
		// as 500 so debugging upstream issues isn't masked by a misleading 404.
		s.writeError(w, http.StatusInternalServerError, msg)
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

	// Canonical kind from the resource's own TypeMeta (set at fetch). Pascal
	// singular — matches what the audit check runner writes into Finding.Kind,
	// so the audit index lookup keys correctly. Falls back to the URL kind
	// only when TypeMeta is somehow empty; non-canonical input there would
	// silently mis-key the audit lookup.
	gvk := obj.GetObjectKind().GroupVersionKind()
	canonicalKind := gvk.Kind
	if canonicalKind == "" {
		canonicalKind = kind
	}
	canonicalGroup := gvk.Group

	issueSum := computeIssueSummaryForResource(cache, canonicalGroup, canonicalKind, namespace, name)
	auditSum := computeAuditSummaryForResource(cache, canonicalGroup, canonicalKind, namespace, name)

	opts := resourcecontext.Options{
		Tier:            resourcecontext.TierBasic,
		AccessChecker:   s.newRequestScopedChecker(r),
		IssueSummary:    issueSum,
		AuditSummary:    auditSum,
		ServiceBackends: serviceBackendLookup{cache: cache},
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
		// Relationships are computed inside Build via GetRelationshipsWithObject,
		// which applies the same KindForGVK pseudo-kind remap we used to do
		// here. Pre-computing in the handler doubled the work whenever the
		// lookup returned nil (no edges): handler call returned nil, Build's
		// `rel == nil && opts.Topology != nil` fallback re-ran the identical
		// scan. Leaving opts.Relationships unset is the canonical path.
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
// (problem + condition) into an IssueSummary.
//
// The composer is the canonical "what's wrong with this resource" surface —
// it merges problem detection (Deployment/DS/etc.), pod-level conditions,
// and generic CRD condition fallback. Filtering to a single (kind, name)
// is done client-side; the composer's native namespace filter restricts the
// scan to the resource's namespace so we don't walk the whole cluster.
//
// kind MUST be the Pascal singular form the issue composer writes into
// Issue.Kind (e.g. "Deployment", "Pod") — the caller derives it from obj's
// TypeMeta. The composer's Filters.Kinds matcher case-folds both sides, but
// it does NOT plural-to-singular convert, so URL forms ("deployments",
// "pods") drop every issue ("deployments" != lower("Deployment")) and the
// summary silently collapses to nil.
//
// Returns nil when no issues match — Build then omits the IssueSummary field.
func computeIssueSummaryForResource(cache *k8s.ResourceCache, group, kind, namespace, name string) *resourcecontext.IssueSummary {
	if cache == nil {
		return nil
	}
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}
	filters := issues.Filters{
		Kinds: []string{kind},
		// NoLimit (not MaxLimit): the post-compose filter narrows to a
		// single {group, kind, ns, name}, so a hard cap before that
		// filter can silently zero the per-resource summary on clusters
		// where the kind has many same-namespace siblings whose issues
		// outrank the target on (severity, last_seen). Mirrors the MCP
		// sibling in internal/mcp/resource_context.go and the
		// summarycontext.BuildIssueIndex rationale.
		Limit: issues.NoLimit,
	}
	if namespace != "" {
		filters.Namespaces = []string{namespace}
	}
	rows, _ := issues.ComposeWithStats(provider, filters)

	matched := make([]issues.Issue, 0, len(rows))
	bySource := make(map[string]int)
	for _, row := range rows {
		if row.Name != name {
			continue
		}
		if namespace != "" && row.Namespace != namespace {
			continue
		}
		// Group-aware match: Issue.Group is populated for problem,
		// condition, and event sources, so a Knative
		// serving.knative.dev/Service lookup won't pull in the core
		// Service's issues (or vice versa).
		if row.Group != group {
			continue
		}
		matched = append(matched, row)
		bySource[string(row.Source)]++
	}
	if len(matched) == 0 {
		return nil
	}
	// Sort by (severity desc, Reason asc) so TopReason is deterministic
	// across runs even when multiple rows tie on severity. Mirrors the
	// stable sort applied in computeAuditSummaryForResource.
	sort.Slice(matched, func(i, j int) bool {
		ri, rj := composeSeverityRank(matched[i].Severity), composeSeverityRank(matched[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return matched[i].Reason < matched[j].Reason
	})
	count := len(matched)
	topSeverity := matched[0].Severity
	topReason := matched[0].Reason
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
// resource via the canonical (Kind/ns/name) tuple. kind MUST be the Pascal
// singular form the audit check runner writes into Finding.Kind (e.g. "Pod",
// not "pod" or "pods") — the caller derives it from obj's TypeMeta. Without
// a Kind-aware key, a Deployment "web" in "prod" would inherit findings
// from a Service "web" in the same namespace, since map iteration in the
// previous implementation only compared (namespace, name).
//
// TopFinding is selected deterministically: highest severity wins, with
// CheckID as the ascending tiebreaker. Map iteration ordering does NOT
// influence the choice — agents pinning regression tests on
// resourceContext output rely on stable field values across runs.
func computeAuditSummaryForResource(cache *k8s.ResourceCache, group, kind, namespace, name string) *resourcecontext.AuditSummary {
	if cache == nil || kind == "" {
		return nil
	}
	// Match computeIssueSummaryForResource's guard: passing []string{""} to
	// RunFromCache would filter to literally namespace="" resources instead
	// of scanning all namespaces. Latent today since the audit suite
	// doesn't cover cluster-scoped kinds, but the inconsistency would
	// silently miss findings the moment a cluster-scoped check lands.
	var namespaces []string
	if namespace != "" {
		namespaces = []string{namespace}
	}
	results := audit.RunFromCache(cache, namespaces, nil)
	if results == nil || len(results.Findings) == 0 {
		return nil
	}
	idx := bpaudit.IndexByResource(results.Findings)
	match := idx[bpaudit.ResourceKey(group, kind, namespace, name)]
	if len(match) == 0 {
		return nil
	}

	// Sort by (severity desc, CheckID asc) so TopFinding is deterministic
	// across runs even when multiple findings tie on severity.
	sort.Slice(match, func(i, j int) bool {
		ri, rj := auditSeverityRank(match[i].Severity), auditSeverityRank(match[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return match[i].CheckID < match[j].CheckID
	})
	topFinding := match[0].CheckID
	return &resourcecontext.AuditSummary{
		Count:           len(match),
		HighestSeverity: normalizeAuditSeverity(match[0].Severity),
		TopFinding:      topFinding,
	}
}

// normalizeAuditSeverity maps the audit suite's emission vocabulary
// ("danger" / "warning") onto the unified resourceContext severity
// scale ("critical" / "warning") used by issueSummary. Two sibling
// fields in the same response reporting severity in different
// vocabularies — "danger" vs "critical" — is a wire-shape footgun for
// consumers. Empty / unknown severities pass through unchanged so the
// contract stays explicit if the audit suite ever grows new values.
func normalizeAuditSeverity(s string) string {
	switch s {
	case bpaudit.SeverityDanger:
		return string(issues.SeverityCritical)
	case bpaudit.SeverityWarning:
		return string(issues.SeverityWarning)
	}
	return s
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
