package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	"github.com/skyhook-io/radar/pkg/topology"
)

// gitopsRequest is the parsed identity of a single GitOps tree/insights
// request, with the namespace allowlist already resolved against RBAC.
type gitopsRequest struct {
	Kind, Namespace, Name, Group string
	Cache                        *k8s.ResourceCache
	AllowedNamespaces            []string
}

// HasNamespaceAccess reports whether the caller is allowed to inspect any
// namespace's resources. False means handlers should short-circuit with an
// empty success response (see the per-handler empty value).
func (g *gitopsRequest) HasNamespaceAccess() bool {
	return !noNamespaceAccess(g.AllowedNamespaces)
}

// parseGitOpsRequest pulls the GitOps URL params and runs the namespace
// access check shared by /api/gitops/{tree,insights}/.... Returns ok=false
// after writing an error response (caller must return immediately).
func (s *Server) parseGitOpsRequest(w http.ResponseWriter, r *http.Request) (*gitopsRequest, bool) {
	kind := normalizeKind(chi.URLParam(r, "kind"))
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	group := r.URL.Query().Get("group")
	if namespace == "_" {
		namespace = ""
	}
	if namespace != "" {
		allowed := s.getUserNamespaces(r, []string{namespace})
		if noNamespaceAccess(allowed) {
			s.writeError(w, http.StatusForbidden, fmt.Sprintf("no access to namespace %q", namespace))
			return nil, false
		}
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return nil, false
	}

	// Detail endpoints intentionally use the user's full RBAC ceiling rather
	// than the active UI namespace filter. The root resource (e.g. an Argo
	// Application in `argocd`) commonly manages workloads in *other*
	// namespaces; if we honored the UI filter, the tree would be empty
	// whenever the user has narrowed Radar to a different namespace than the
	// managed ones. The root namespace itself is still RBAC-checked above
	// (s.getUserNamespaces with the namespace param).
	return &gitopsRequest{
		Kind:              kind,
		Namespace:         namespace,
		Name:              name,
		Group:             group,
		Cache:             cache,
		AllowedNamespaces: s.getUserNamespaces(r, nil),
	}, true
}

// buildGitOpsTree constructs the topology + GitOps resource tree for a
// parsed request. The live root unstructured is returned alongside the tree
// so downstream consumers (insights) can derive views without re-fetching.
//
// The topology build itself is the dominant cost — it walks every cached
// resource of every kind. We route it through s.topoMemo so a page-load
// firing /tree + /insights, or the in-flight 2s polling, all share a single
// build. Topology is a deterministic projection of the informer cache, so
// the short TTL has no semantic effect.
func (s *Server) buildGitOpsTree(ctx context.Context, req *gitopsRequest) (*gitopstree.ResourceTree, *unstructured.Unstructured, error) {
	opts := topology.DefaultBuildOptions()
	opts.Namespaces = req.AllowedNamespaces
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true

	topo, err := s.topoMemo.Get(opts, func() (*topology.Topology, error) {
		return topology.NewBuilder(k8s.NewTopologyResourceProvider(req.Cache)).
			WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())).
			Build(opts)
	})
	if err != nil {
		return nil, nil, err
	}

	return gitopstree.NewBuilder(req.Cache, topo).
		WithAllowedNamespaces(req.AllowedNamespaces).
		Build(ctx, req.Kind, req.Namespace, req.Name, req.Group)
}

// writeGitOpsBuildError maps tree-build errors to HTTP status codes.
// Uses errors.Is on typed sentinels rather than string matching so the HTTP
// status doesn't drift if an upstream error message gets reworded.
func (s *Server) writeGitOpsBuildError(w http.ResponseWriter, req *gitopsRequest, err error) {
	switch {
	case errors.Is(err, k8s.ErrUnknownDynamicKind):
		s.writeError(w, http.StatusBadRequest, err.Error())
	case apierrors.IsNotFound(err):
		s.writeError(w, http.StatusNotFound, err.Error())
	default:
		log.Printf("[gitops] Failed to build tree for %s %s/%s (group=%q): %v", sanitizeForLog(req.Kind), sanitizeForLog(req.Namespace), sanitizeForLog(req.Name), sanitizeForLog(req.Group), err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleGitOpsTree(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	req, ok := s.parseGitOpsRequest(w, r)
	if !ok {
		return
	}
	if !req.HasNamespaceAccess() {
		s.writeJSON(w, &gitopstree.ResourceTree{
			Nodes:    []gitopstree.Node{},
			Edges:    []gitopstree.Edge{},
			Warnings: []string{"You do not have access to any namespace; managed resources are filtered out."},
		})
		return
	}
	tree, _, err := s.buildGitOpsTree(r.Context(), req)
	if err != nil {
		s.writeGitOpsBuildError(w, req, err)
		return
	}
	tree = s.filterGitOpsTreeForUser(r, req, tree)
	s.writeJSON(w, tree)
}

func (s *Server) handleGitOpsInsights(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	req, ok := s.parseGitOpsRequest(w, r)
	if !ok {
		return
	}
	if !req.HasNamespaceAccess() {
		s.writeJSON(w, gitopsinsights.Insight{
			Warnings: []string{"You do not have access to any namespace; insights are filtered out."},
		})
		return
	}
	tree, root, err := s.buildGitOpsTree(r.Context(), req)
	if err != nil {
		s.writeGitOpsBuildError(w, req, err)
		return
	}
	tree = s.filterGitOpsTreeForUser(r, req, tree)
	canAccess := func(group, kind, namespace, name string) bool {
		return s.canAccessGitOpsRef(r, req, group, kind, namespace, name, false)
	}
	resolver := newInsightsResolver(r.Context(), req.Cache, req.AllowedNamespaces, canAccess)
	insight := gitopsinsights.Build(root, tree, resolver)
	insight.Warnings = appendWarnings(insight.Warnings, tree.Warnings...)
	insight = s.filterGitOpsInsightForUser(r, req, insight)
	s.writeJSON(w, insight)
}

// managedScanKinds is the set of K8s kinds /api/gitops/managed-resources
// iterates when discovering resources tagged with Argo's tracking
// annotation/label.
//
// Scope: controller-level kinds Argo CD typically declares directly in
// manifests (Deployments, Services, ConfigMaps, RBAC, …). Pods and
// ReplicaSets are deliberately excluded — they're created by their owning
// workload (Deployment → ReplicaSet → Pod) and don't carry Argo's
// tracking annotation. Including them would scan thousands of cache
// entries per request without adding signal.
//
// Subtree expansion (showing the Pods owned by a matched Deployment) is
// a future enhancement that would walk the topology graph rather than the
// flat annotation index — keep that out of the hot per-request scan.
//
// CRDs managed by Argo Apps aren't iterated here. Real-world need would
// drive adding e.g. cert-manager Certificates, KEDA ScaledObjects, etc.
type managedScanKind struct {
	Kind  string
	Group string
}

// managedScanDenyList is the set of Kinds we skip when scanning for
// resources with Argo's tracking annotation. Two reasons a Kind belongs
// here:
//
//  1. Descendants of Argo-managed resources that are owned, not declared.
//     Argo never stamps the tracking annotation on Pods/ReplicaSets/etc.
//     — those are walked from the matched owners by the tree builder
//     (see pkg/gitops/tree.expandSubtree). Including them in the scan
//     produces double-counting + a slower scan.
//
//  2. Platform-internal noise that's never user-managed:
//     Events (ephemeral), Leases (controller heartbeats), Endpoints /
//     EndpointSlice (Service shadow), FlowSchema / PriorityLevelConfiguration
//     (API priority internals), {Local,Self,}SubjectAccessReview /
//     TokenRequest / TokenReview (subject-action stubs, not stored).
//
// NOT a security boundary — the per-node RBAC filter (filterGitOpsTreeForUser)
// is what gates what each caller sees. This list just keeps the scan
// focused on Kinds Argo realistically manages.
var managedScanDenyList = map[string]bool{
	"Pod":                        true,
	"ReplicaSet":                 true,
	"ControllerRevision":         true,
	"Endpoints":                  true,
	"EndpointSlice":              true,
	"Event":                      true,
	"Lease":                      true,
	"FlowSchema":                 true,
	"PriorityLevelConfiguration": true,
	"TokenRequest":               true,
	"TokenReview":                true,
	"SubjectAccessReview":        true,
	"SelfSubjectAccessReview":    true,
	"LocalSubjectAccessReview":   true,
	"SelfSubjectRulesReview":     true,
	"Binding":                    true,
}

// managedScanKindsFromDiscovery builds the kind list for the managed-
// resources scan by asking radar's existing APIResources discovery what
// the cluster actually has. Filters out:
//
//   - kinds in managedScanDenyList (see above)
//   - kinds whose verbs don't include `list` (we can't scan them anyway)
//
// Returns the full list — CRDs included. This is the architectural lever
// that makes the managed-resources endpoint complete: cert-manager
// Certificates, Argo Rollouts, Gateway API HTTPRoutes, KEDA ScaledObjects,
// operator CRs — all show up in the tree without anyone editing this file.
//
// Empty discovery (boot-time race or a degraded cluster) returns nil;
// caller surfaces it as "discovery unavailable" rather than silently
// scanning nothing.
func managedScanKindsFromDiscovery() []managedScanKind {
	disc := k8s.GetResourceDiscovery()
	if disc == nil {
		return nil
	}
	resources, err := disc.GetAPIResources()
	if err != nil || len(resources) == 0 {
		return nil
	}
	out := make([]managedScanKind, 0, len(resources))
	for _, r := range resources {
		if managedScanDenyList[r.Kind] {
			continue
		}
		hasList := false
		for _, v := range r.Verbs {
			if v == "list" {
				hasList = true
				break
			}
		}
		if !hasList {
			continue
		}
		out = append(out, managedScanKind{Kind: r.Kind, Group: r.Group})
	}
	return out
}

// handleGitOpsManagedResources discovers resources in THIS cluster that are
// managed by the named Argo Application, returning them as a synthetic
// ResourceTree the frontend can render with the existing GitOpsTreeGraph
// component.
//
// The endpoint is the destination-side companion to /api/gitops/tree —
// the latter is controller-side (walks live ownership from the Application
// CRD outward, only works when controller + workloads share a cluster);
// this one is destination-side (matches by Argo's tracking annotation,
// works regardless of where the controller lives). Used by Radar Hub's
// fleet GitOps detail page to render the workload graph for cross-cluster
// Argo apps; also useful for single-cluster Radar users connected to a
// destination cluster who want to see "what's managed here by app X".
//
// Query params:
//   - app       (required): Argo Application name. Resources are matched
//     when annotation argocd.argoproj.io/tracking-id starts with
//     "<app>:" OR label app.kubernetes.io/instance=<app>.
//   - namespace (optional): restrict the synthetic root's display ns +
//     filter matched resources to this namespace.
func (s *Server) handleGitOpsManagedResources(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	app := strings.TrimSpace(r.URL.Query().Get("app"))
	if app == "" {
		s.writeError(w, http.StatusBadRequest, "app query parameter is required")
		return
	}
	nsFilter := strings.TrimSpace(r.URL.Query().Get("namespace"))

	allowedNamespaces := s.getUserNamespaces(r, nil)
	if noNamespaceAccess(allowedNamespaces) {
		// Caller has no namespace access — return a tree with just the
		// synthetic root + a warning. Mirrors handleGitOpsTree's behavior
		// rather than 403'ing so the frontend can render an honest empty state.
		empty := gitopstree.BuildManagedTree(app, nsFilter, nil)
		empty.Warnings = []string{"You do not have access to any namespace; managed resources are filtered out."}
		s.writeJSON(w, empty)
		return
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	// Discover what Kinds the cluster actually has — CRDs included —
	// instead of relying on a static list. Customers using cert-manager,
	// Argo Rollouts, Gateway API, KEDA, External Secrets, etc. all get
	// their resources in the tree without any code change here. The
	// scan deny-list keeps the list focused (drops descendants like
	// Pod/ReplicaSet that are walked from owners separately, and
	// platform-internal noise like Events/Leases).
	scanKinds := managedScanKindsFromDiscovery()
	if len(scanKinds) == 0 {
		// Discovery is unavailable (boot-time race or degraded cluster).
		// Surface to caller rather than scanning nothing silently.
		empty := gitopstree.BuildManagedTree(app, nsFilter, nil)
		empty.Warnings = []string{"API resource discovery is not available yet — managed resources can't be enumerated until discovery completes."}
		s.writeJSON(w, empty)
		return
	}

	matched := make([]*unstructured.Unstructured, 0, 32)
	// kindErrors accumulates per-kind list failures so a kind-class
	// blackout (informer-sync stuck post-restart, cluster-wide RBAC
	// denial) is visible to the caller as a warning rather than
	// indistinguishable from "this app legitimately manages 0
	// resources." A single-kind failure (CRD not installed, narrow
	// RBAC) is expected and the scan continues; if every kind fails,
	// the warning surfaces in the response so the frontend can render an
	// inline note.
	var kindErrors []string
	for _, mk := range scanKinds {
		// Empty namespace = list across all namespaces from the cache.
		// We rely on filterGitOpsTreeForUser below for per-resource RBAC
		// (matching the contract handleGitOpsTree uses) — including the
		// per-kind canRead check for cluster-scoped resources that the
		// previous inline namespace-only filter was missing.
		objs, err := cache.ListDynamicWithGroup(r.Context(), mk.Kind, "", mk.Group)
		if err != nil {
			kindErrors = append(kindErrors, mk.Kind+": "+err.Error())
			continue
		}
		for _, obj := range objs {
			ns := obj.GetNamespace()
			// `ns == ""` means cluster-scoped; let it pass nsFilter so apps that manage
			// ClusterRole/ClusterRoleBinding/Namespace alongside namespaced workloads
			// still show those resources. filterGitOpsTreeForUser enforces per-kind canRead.
			if nsFilter != "" && ns != "" && ns != nsFilter {
				continue
			}
			if !gitopstree.ArgoTrackingMatches(obj, app) {
				continue
			}
			matched = append(matched, obj)
		}
	}

	tree := gitopstree.BuildManagedTree(app, nsFilter, matched)
	if len(kindErrors) > 0 {
		// Log when more than a couple of kinds failed — single-kind
		// errors (one CRD missing) are noise; a widespread blackout is
		// a real operator signal. ≥3 means "the informer cache or RBAC
		// is broken across most kinds," not "this cluster is missing
		// some optional CRDs."
		if len(kindErrors) >= 3 {
			// Sanitize each kind-error string before logging — k8s API
			// errors echo CRD names + resource fields that, while not
			// directly user-controlled, could carry CR/LF and forge
			// log lines on aggregators.
			safeErrors := make([]string, len(kindErrors))
			for i, e := range kindErrors {
				safeErrors[i] = sanitizeForLog(e)
			}
			log.Printf("[gitops] managed-resources: %d/%d kinds failed to list for app=%s: %v",
				len(kindErrors), len(scanKinds), sanitizeForLog(app), safeErrors)
		}
		tree.Warnings = append(tree.Warnings,
			fmt.Sprintf("Some resource kinds couldn't be scanned (%d of %d failed); the list may be incomplete.",
				len(kindErrors), len(scanKinds)))
	}

	// Apply the existing per-resource RBAC filter — same gate
	// handleGitOpsTree uses for its tree, so cross-cluster discovery
	// can't expose more than the controller-side endpoint already would.
	// Critically, this calls canRead for cluster-scoped Kinds (e.g.
	// ClusterRole), closing the gap where the previous namespace-only
	// gate let any authenticated namespace user see cluster-scoped
	// Argo-managed resources.
	req := &gitopsRequest{
		Kind:              "applications",
		Namespace:         nsFilter,
		Name:              app,
		Group:             "argoproj.io",
		Cache:             cache,
		AllowedNamespaces: allowedNamespaces,
	}
	tree = s.filterGitOpsTreeForUser(r, req, tree)
	s.writeJSON(w, tree)
}

func (s *Server) filterGitOpsTreeForUser(r *http.Request, req *gitopsRequest, tree *gitopstree.ResourceTree) *gitopstree.ResourceTree {
	if tree == nil || auth.UserFromContext(r.Context()) == nil {
		return tree
	}
	keep := make(map[string]bool, len(tree.Nodes))
	for _, node := range tree.Nodes {
		allowed := node.Role == gitopstree.RoleRoot || s.canAccessGitOpsRef(r, req, node.Ref.Group, node.Ref.Kind, node.Ref.Namespace, node.Ref.Name, false)
		keep[node.ID] = allowed
	}
	filteredNodes := make([]gitopstree.Node, 0, len(tree.Nodes))
	for _, node := range tree.Nodes {
		if !keep[node.ID] {
			continue
		}
		if len(node.GroupedNodeIDs) > 0 {
			grouped := make([]string, 0, len(node.GroupedNodeIDs))
			for _, id := range node.GroupedNodeIDs {
				if keep[id] {
					grouped = append(grouped, id)
				}
			}
			node.GroupedNodeIDs = grouped
			node.Count = len(grouped)
		}
		filteredNodes = append(filteredNodes, node)
	}
	if len(filteredNodes) == len(tree.Nodes) {
		return tree
	}

	filteredEdges := make([]gitopstree.Edge, 0, len(tree.Edges))
	for _, edge := range tree.Edges {
		if keep[edge.Source] && keep[edge.Target] {
			filteredEdges = append(filteredEdges, edge)
		}
	}
	out := *tree
	out.Nodes = filteredNodes
	out.Edges = filteredEdges
	if keep[tree.Root.ID] {
		out.Root = tree.Root
	}
	out.Summary = summarizeGitOpsTree(filteredNodes)
	out.Warnings = appendWarnings(append([]string{}, tree.Warnings...), "Some managed resources are hidden by RBAC.")
	return &out
}

func (s *Server) filterGitOpsInsightForUser(r *http.Request, req *gitopsRequest, insight gitopsinsights.Insight) gitopsinsights.Insight {
	if auth.UserFromContext(r.Context()) == nil {
		return insight
	}
	changed := false

	// Allocate fresh slices rather than the `[:0]` in-place pattern.
	// `insight` is value-received, so its slice *headers* are local copies,
	// but the backing arrays still alias the caller's data — if gitopsinsights.Build
	// ever caches its return value (memoization, pooling) or shares it across
	// requests, filtering in-place would corrupt other callers. The allocation
	// cost is negligible compared to the JSON encode that follows.
	changes := make([]gitopsinsights.Change, 0, len(insight.Changes))
	for _, change := range insight.Changes {
		if s.canAccessGitOpsRef(r, req, change.Ref.Group, change.Ref.Kind, change.Ref.Namespace, change.Ref.Name, false) {
			changes = append(changes, change)
		} else {
			changed = true
		}
	}
	insight.Changes = changes

	plan := make([]gitopsinsights.PlanItem, 0, len(insight.Plan))
	for _, item := range insight.Plan {
		if !s.canAccessGitOpsRef(r, req, item.Ref.Group, item.Ref.Kind, item.Ref.Namespace, item.Ref.Name, false) {
			changed = true
			continue
		}
		blockedBy := make([]gitopsinsights.Ref, 0, len(item.BlockedBy))
		for _, ref := range item.BlockedBy {
			if s.canAccessGitOpsRef(r, req, ref.Group, ref.Kind, ref.Namespace, ref.Name, false) {
				blockedBy = append(blockedBy, ref)
			} else {
				changed = true
			}
		}
		item.BlockedBy = blockedBy
		plan = append(plan, item)
	}
	insight.Plan = plan

	issues := make([]gitopsinsights.Issue, 0, len(insight.Issues))
	for _, issue := range insight.Issues {
		if len(issue.Refs) == 0 {
			issues = append(issues, issue)
			continue
		}
		refs := make([]gitopsinsights.Ref, 0, len(issue.Refs))
		for _, ref := range issue.Refs {
			if s.canAccessGitOpsRef(r, req, ref.Group, ref.Kind, ref.Namespace, ref.Name, false) {
				refs = append(refs, ref)
			} else {
				changed = true
			}
		}
		if len(refs) == 0 {
			changed = true
			continue
		}
		issue.Refs = refs
		issues = append(issues, issue)
	}
	insight.Issues = issues

	if changed {
		insight.Warnings = appendWarnings(insight.Warnings, "Some managed resources are hidden by RBAC.")
	}
	return insight
}

func appendWarnings(existing []string, warnings ...string) []string {
	for _, warning := range warnings {
		if warning == "" {
			continue
		}
		seen := false
		for _, existingWarning := range existing {
			if existingWarning == warning {
				seen = true
				break
			}
		}
		if !seen {
			existing = append(existing, warning)
		}
	}
	return existing
}

func summarizeGitOpsTree(nodes []gitopstree.Node) gitopstree.Summary {
	var s gitopstree.Summary
	for _, n := range nodes {
		switch n.Role {
		case gitopstree.RoleDeclared:
			s.Declared++
		case gitopstree.RoleGenerated:
			s.Generated++
		case gitopstree.RoleGroup:
			s.Grouped += n.Count
		}
		if n.Health == "Degraded" || n.Health == "Missing" {
			s.Degraded++
		}
		if n.Sync == "OutOfSync" {
			s.OutOfSync++
		}
	}
	return s
}

func (s *Server) canAccessGitOpsRef(r *http.Request, req *gitopsRequest, group, kind, namespace, name string, root bool) bool {
	if auth.UserFromContext(r.Context()) == nil {
		return true
	}
	if root {
		return true
	}
	if strings.EqualFold(kind, "Namespace") || strings.EqualFold(kind, "namespaces") {
		if s.canRead(r, "", "namespaces", "", "list") {
			return true
		}
		if name == "" {
			return false
		}
		allowed := s.getUserNamespaces(r, []string{name})
		return !noNamespaceAccess(allowed)
	}
	if namespace != "" {
		return namespaceAllowedForGitOps(req.AllowedNamespaces, namespace)
	}
	if clusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(kind, group); clusterScoped {
		return s.canRead(r, gvrGroup, gvrResource, "", "list")
	}
	return false
}

func namespaceAllowedForGitOps(allowed []string, namespace string) bool {
	if allowed == nil {
		return true
	}
	for _, ns := range allowed {
		if ns == namespace {
			return true
		}
	}
	return false
}

// insightsResolver wires the dynamic cache + event lister into the
// gitopsinsights package without exposing internal/k8s types to the public
// pkg/gitops/insights API. Per-request: ctx + namespace allowlist captured
// once at construction; lookups are namespace-filtered to enforce RBAC.
type insightsResolver struct {
	ctx               context.Context
	cache             *k8s.ResourceCache
	allowedNamespaces []string
	canAccess         func(group, kind, namespace, name string) bool

	// The cluster-wide issue set is composed at most once per insights request
	// (lazily, only if a degraded managed resource asks for it) and reused
	// across every ResourceProblems lookup — a full Compose per managed
	// resource on a 2s-polling detail page would be O(resources × cluster).
	composeOnce     sync.Once
	composedFlat    []issues.Issue
	composedGrouped []issues.Issue
}

func newInsightsResolver(ctx context.Context, cache *k8s.ResourceCache, allowed []string, canAccess func(group, kind, namespace, name string) bool) *insightsResolver {
	return &insightsResolver{ctx: ctx, cache: cache, allowedNamespaces: allowed, canAccess: canAccess}
}

// recentEventsCap bounds events returned per resource. Beyond ~5 the user
// is better served opening the standard drawer; this is meant to surface
// the headline cause inline, not be a full event log.
const recentEventsCap = 5

func (r *insightsResolver) GetLive(group, kind, namespace, name string) *unstructured.Unstructured {
	if r == nil || r.cache == nil || name == "" || kind == "" {
		return nil
	}
	if !r.namespaceAllowed(namespace) {
		return nil
	}
	if r.canAccess != nil && !r.canAccess(group, kind, namespace, name) {
		return nil
	}
	obj, err := r.cache.GetDynamicWithGroupPreserveLastApplied(r.ctx, kind, namespace, name, group)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.Printf("[gitops] insights GetLive %s/%s %s/%s failed: %v", group, kind, namespace, name, err)
		}
		return nil
	}
	return obj
}

// ResourceProblems implements gitopsinsights.Resolver. It returns the workload
// "why" (crashloop / oom / image-pull / unschedulable / pvc-pending) the
// cluster-wide issues engine already classified for a managed resource, so the
// detail page can show it inline behind Argo's coarse Degraded/Missing. Scoped
// to the same namespace allowlist + per-resource access gate as GetLive; an
// RBAC-blocked lookup returns empty, which the caller renders as generic
// "go inspect" guidance rather than implying the resource is healthy.
func (r *insightsResolver) ResourceProblems(group, kind, namespace, name string) []gitopsinsights.ResourceProblem {
	if r == nil || r.cache == nil {
		return nil
	}
	if !r.namespaceAllowed(namespace) {
		return nil
	}
	if r.canAccess != nil && !r.canAccess(group, kind, namespace, name) {
		return nil
	}
	r.composeOnce.Do(func() {
		r.composedFlat = issues.Compose(issues.NewCacheProvider(), issues.Filters{Namespaces: r.allowedNamespaces, Limit: issues.NoLimit})
		r.composedGrouped = issues.GroupIssues(r.composedFlat)
	})
	related := issues.RelatedIssuesFrom(r.composedFlat, r.composedGrouped, group, kind, namespace, name)
	out := make([]gitopsinsights.ResourceProblem, 0, len(related))
	for _, iss := range related {
		out = append(out, gitopsinsights.ResourceProblem{
			Reason:   iss.Reason,
			Message:  iss.Message,
			Category: string(iss.Category),
			Severity: string(iss.Severity),
		})
	}
	return out
}

func (r *insightsResolver) RecentEvents(group, kind, namespace, name string) []gitopsinsights.EventSummary {
	if r == nil || r.cache == nil || r.cache.Events() == nil {
		return nil
	}
	if !r.namespaceAllowed(namespace) {
		return nil
	}
	if r.canAccess != nil && !r.canAccess(group, kind, namespace, name) {
		return nil
	}
	// Lister scope: namespace-scoped lookup is cheaper than cluster-wide
	// + filter; cluster-scoped resources (namespace="") fall back to the
	// cross-namespace lister and are matched only by kind+name.
	var events []runtime.Object
	if namespace != "" {
		items, err := r.cache.Events().Events(namespace).List(labels.Everything())
		if err != nil {
			log.Printf("[gitops] insights RecentEvents list ns=%s %s/%s/%s failed: %v", namespace, group, kind, name, err)
			return nil
		}
		events = make([]runtime.Object, 0, len(items))
		for _, e := range items {
			events = append(events, e)
		}
	} else {
		items, err := r.cache.Events().List(labels.Everything())
		if err != nil {
			log.Printf("[gitops] insights RecentEvents cluster-list %s/%s/%s failed: %v", group, kind, name, err)
			return nil
		}
		events = make([]runtime.Object, 0, len(items))
		for _, e := range items {
			events = append(events, e)
		}
	}
	matched := make([]*corev1.Event, 0, recentEventsCap)
	for _, ro := range events {
		e, ok := ro.(*corev1.Event)
		if !ok {
			continue
		}
		if e.InvolvedObject.Kind != kind || e.InvolvedObject.Name != name {
			continue
		}
		if !eventMatchesGroup(group, e.InvolvedObject.APIVersion) {
			continue
		}
		matched = append(matched, e)
	}
	// Newest-first by lastTimestamp (falls back to eventTime / firstTimestamp
	// for events that don't fill it). Cap to recentEventsCap after sort so
	// we always return the most recent ones.
	sort.SliceStable(matched, func(i, j int) bool {
		return eventTime(matched[i]).After(eventTime(matched[j]))
	})
	if len(matched) > recentEventsCap {
		matched = matched[:recentEventsCap]
	}
	out := make([]gitopsinsights.EventSummary, 0, len(matched))
	for _, e := range matched {
		out = append(out, gitopsinsights.EventSummary{
			Type:               e.Type,
			Reason:             e.Reason,
			Message:            e.Message,
			Count:              e.Count,
			LastTimestamp:      eventTime(e).Format("2006-01-02T15:04:05Z07:00"),
			ReportingComponent: e.ReportingController,
		})
	}
	return out
}

// FinalizerOwnerStatus implements gitopsinsights.Resolver.
//
// Looks up the controller responsible for a finalizer (via the static
// catalog in pkg/gitops/insights/finalizers.go) and reports the
// aggregate health of its pods in the install namespace. Returns ""
// when the finalizer isn't recognized, the install namespace is empty
// (e.g. operator deployed Flux into a non-default namespace), or the
// pod-list lookup itself fails — caller treats empty as "no signal".
//
// The format embeds both the controller name and a short status verb
// so the calling Issue's Cause text reads naturally when surfaced.
// Actual outputs (kept in sync with summarizeControllerHealth):
//
//	"argocd-application-controller is CrashLoopBackOff (2/2 pods)"
//	"kustomize-controller is healthy (1 pod ready)"
//	"source-controller is degraded (1/2 pods ready)"
//	"helm-controller is not running in namespace flux-system (controller may not be installed, or runs under a different namespace)"
func (r *insightsResolver) FinalizerOwnerStatus(finalizer string, root *unstructured.Unstructured) string {
	owner := gitopsinsights.ResolveFinalizerOwner(finalizer, root)
	if owner == nil {
		return ""
	}
	if r.cache == nil || r.cache.Pods() == nil {
		return ""
	}
	if !r.namespaceAllowed(owner.Namespace) {
		return ""
	}
	if r.canAccess != nil && !r.canAccess("", "Pod", owner.Namespace, "") {
		return ""
	}
	pods, err := r.cache.Pods().Pods(owner.Namespace).List(labels.Everything())
	if err != nil {
		log.Printf("[gitops] insights FinalizerOwnerStatus pods list ns=%s controller=%s failed: %v", owner.Namespace, owner.Controller, err)
		return ""
	}
	var matched []*corev1.Pod
	for _, p := range pods {
		if p.Labels[owner.SelectorKey] == owner.SelectorValue {
			matched = append(matched, p)
		}
	}
	if len(matched) == 0 {
		// Could be: operator changed the namespace, or the controller
		// isn't installed (broken finalizer-only state). Either way,
		// the user needs to know "the controller isn't where I expect"
		// to start debugging.
		return owner.Controller + " is not running in namespace " + owner.Namespace + " (controller may not be installed, or runs under a different namespace)"
	}
	return summarizeControllerHealth(owner.Controller, matched)
}

// summarizeControllerHealth distills a slice of controller pods into a
// short, operator-readable status verb. Aggregates over multiple
// replicas (Argo's controller is typically deployed as a 2-replica
// StatefulSet for HA): if any pod is in CrashLoopBackOff or Error, that
// fact dominates the status. If all pods are Ready, "healthy". Anything
// in between is "degraded" with a count.
func summarizeControllerHealth(controller string, pods []*corev1.Pod) string {
	health := summarizeControllerPods(pods)
	switch {
	case health.Crashing > 0:
		return fmt.Sprintf("%s is %s (%d/%d pods)", controller, health.CrashReason, health.Crashing, health.Total)
	case health.Ready == health.Total && health.Total > 0:
		// All pods Ready — if the resource is *still* stuck deleting
		// despite a healthy controller, it's a different problem (RBAC,
		// network, broken finalizer logic). Surface the healthy state
		// so the operator knows to dig into the controller's logs
		// rather than its lifecycle.
		suffix := "s"
		if health.Ready == 1 {
			suffix = ""
		}
		return fmt.Sprintf("%s is healthy (%d pod%s ready)", controller, health.Ready, suffix)
	case health.Pending > 0:
		return fmt.Sprintf("%s is pending start (%d/%d pods Pending)", controller, health.Pending, health.Total)
	default:
		return fmt.Sprintf("%s is degraded (%d/%d pods ready)", controller, health.Ready, health.Total)
	}
}

func (r *insightsResolver) namespaceAllowed(namespace string) bool {
	if r.allowedNamespaces == nil {
		return true
	}
	if namespace == "" {
		// Cluster-scoped resources are visible to anyone with any
		// namespace access; gating them on namespace allowlist would
		// hide things like Namespaces, ClusterRoles from every user.
		return true
	}
	for _, ns := range r.allowedNamespaces {
		if ns == namespace {
			return true
		}
	}
	return false
}

// eventMatchesGroup decides whether an event's involvedObject.apiVersion
// matches the requested group. apiVersion is "group/version" for grouped
// kinds or just "version" for core resources; either side being empty is
// treated as a match so informers that strip apiVersion still surface
// events alongside the resource they belong to.
func eventMatchesGroup(group, apiVersion string) bool {
	if group == "" || apiVersion == "" {
		return true
	}
	ig := apiVersion
	if i := strings.IndexByte(ig, '/'); i > 0 {
		ig = ig[:i]
	}
	if ig == "" {
		return true
	}
	return ig == group
}

// eventTime returns the most useful timestamp from an Event. Modern events
// (eventTime non-zero) prefer that; legacy events fall back to
// lastTimestamp then firstTimestamp.
func eventTime(e *corev1.Event) time.Time {
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	return e.FirstTimestamp.Time
}
