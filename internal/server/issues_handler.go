package server

import (
	"context"
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
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// filterRecentChangesByRBAC drops RecentChange rows the ctx user can't read, via
// the shared per-kind gate (RecentChange.APIVersion disambiguates CRD kind
// collisions). Auth off → returned unchanged. Used by both the /api/issues
// recent_changes enrichment and per-issue change correlation.
func (s *Server) filterRecentChangesByRBAC(ctx context.Context, changes []issuesapi.RecentChange) []issuesapi.RecentChange {
	if auth.UserFromContext(ctx) == nil {
		return changes
	}
	authz := s.changeAuthorizerForCtx(ctx)
	out := changes[:0]
	for _, c := range changes {
		if k8s.ChangeReadAllowed(c.Kind, c.APIVersion, c.Namespace, authz) {
			out = append(out, c)
		}
	}
	return out
}

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
		Grouped:              q.Get("view") != "flat",
		CanReadClusterScoped: s.issueClusterScopedAccess(r),
		CanReadRelated:       s.issueRelatedResourceAccess(r),
		CanReadEvidence:      s.issueEvidenceAccess(r),
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
			if recentResult, err := meaningfulchanges.Recent(r.Context(), meaningfulchanges.Query{
				Namespaces: []string{namespaces[0]},
				Since:      meaningfulchanges.DefaultSince,
				Limit:      meaningfulchanges.IssueChangesFetchLimit(recentChangesReason),
				FieldLimit: meaningfulchanges.DefaultFieldLimit,
			}); err == nil && len(recentResult.Changes) > 0 {
				// Per-kind RBAC: recent_changes is namespace-filtered but not
				// per-kind — drop changes for kinds the caller can't read before
				// ranking, so priority/recap logic operates on the visible set.
				recentResult.Changes = s.filterRecentChangesByRBAC(r.Context(), recentResult.Changes)
				changes, guidance, recapped := meaningfulchanges.PrioritizeIssueChanges(
					recentResult.Changes, out, meaningfulchanges.IssueChangePriorityOptions{
						Reason:             recentChangesReason,
						Limit:              meaningfulchanges.IssueChangesLimit,
						UnfilteredIssueSet: meaningfulchanges.IssueSeveritySetComplete(severities),
						FetchSaturated:     recentResult.FetchSaturated,
					},
				)
				resp.RecentChanges = changes
				resp.RecentChangesReason = recentChangesReason
				resp.RecentChangesGuidance = guidance
				resp.RecentChangesTruncated = recentResult.OutputCapped || recentResult.FetchSaturated || recapped
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

func (s *Server) issueClusterScopedAccess(r *http.Request) func(kind, group string) bool {
	return func(kind, group string) bool {
		if auth.UserFromContext(r.Context()) == nil {
			return true
		}
		clusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(kind, group)
		if !clusterScoped {
			return false
		}
		return s.canRead(r, gvrGroup, gvrResource, "", "list")
	}
}

func (s *Server) issueRelatedResourceAccess(r *http.Request) func(issues.Ref) bool {
	return func(ref issues.Ref) bool {
		if auth.UserFromContext(r.Context()) == nil {
			return true
		}
		if ref.Namespace != "" || strings.EqualFold(ref.Kind, "Namespace") {
			_, _, ok := s.preflightResourceGet(r, normalizeKind(ref.Kind), ref.Namespace, ref.Name, ref.Group)
			if !ok {
				return false
			}
			switch {
			case ref.Group == "apps" && ref.Kind == "ReplicaSet":
				return s.canRead(r, ref.Group, "replicasets", ref.Namespace, "get")
			case ref.Group == "batch" && ref.Kind == "Job":
				return s.canRead(r, ref.Group, "jobs", ref.Namespace, "get")
			}
			return true
		}
		clusterScoped, group, resource := k8s.ClassifyKindScope(ref.Kind, ref.Group)
		return clusterScoped && s.canRead(r, group, resource, "", "get")
	}
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
// RBAC: the drawer's preflight (namespace access, cluster-scoped get) and then
// a get-SAR on the subject's own kind: namespace access does not imply reading
// every kind in it, and an issue's message describes the object. Grouped issues
// whose subject the caller can't get are withheld, as are members of kinds it
// can't get; ?coverage=1 returns the envelope that counts them and says whether
// Radar is watching the subject's kind at all.
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
	if kind == rawKind {
		if b, ok := resourceid.BuiltinForName(rawKind); ok && (group == "" || group == b.Group) {
			kind = b.Kind
		}
	}

	if !s.canGetIssueRef(r, issues.Ref{Group: group, Kind: kind, Namespace: namespace, Name: name}) {
		s.writeError(w, http.StatusForbidden, fmt.Sprintf("no access to %s %s", kind, name))
		return
	}

	// Scope the scan to the resource's namespace (a workload's owned pods live
	// there too); cluster-scoped resources scan all namespaces (nil).
	var namespaces []string
	if namespace != "" {
		namespaces = []string{namespace}
	}

	related := issues.RelatedIssues(provider, issues.RelatedIssueOptions{
		Namespaces:           namespaces,
		CanReadClusterScoped: s.issueClusterScopedAccess(r),
		CanReadRelated:       s.issueRelatedResourceAccess(r),
		CanReadEvidence:      s.issueEvidenceAccess(r),
	}, group, kind, namespace, name)
	related, withheld := s.withholdUnreadableIssueRefs(r, related)
	if related == nil {
		related = []issues.Issue{}
	}
	if r.URL.Query().Get("coverage") != "1" {
		s.writeJSON(w, related)
		return
	}
	withheld.Issues += s.clusterScopedSubjectIssuesWithheld(r, provider, related, group, kind, namespace, name)
	resp := ResourceIssuesResponse{
		Issues:   related,
		Coverage: resourceIssuesCoverage(kind, group, namespace),
	}
	if withheld.Issues > 0 || withheld.Members > 0 {
		resp.Withheld = &withheld
	}
	if result := k8s.GetCachedPermissionResult(); result != nil {
		resp.Visibility = k8s.BuildVisibilitySummary(result, k8s.VisibilityNamespace(namespaces))
	}
	s.writeJSON(w, resp)
}

// ResourceIssuesResponse is /api/issues/resource with ?coverage=1.
type ResourceIssuesResponse struct {
	Issues []issues.Issue `json:"issues"`
	// Whether the issues engine reads the subject's kind: ok, syncing, or
	// notWatched. Anything but ok makes an empty list unknown, not none.
	Coverage   string                  `json:"coverage"`
	Withheld   *ResourceIssuesWithheld `json:"withheld,omitempty"`
	Visibility *k8s.VisibilitySummary  `json:"visibility,omitempty"`
}

// ResourceIssuesWithheld counts what the caller's RBAC kept out of the answer.
type ResourceIssuesWithheld struct {
	// Grouped issues whose subject the caller can't get.
	Issues int `json:"issues"`
	// Member refs of kinds the caller can't get, dropped from returned issues.
	Members int `json:"members"`
}

const (
	resourceIssuesCoverageOK         = "ok"
	resourceIssuesCoverageSyncing    = "syncing"
	resourceIssuesCoverageNotWatched = "notWatched"
)

// canGetIssueRef reports whether the caller may get ref: its kind where it
// lives, or failing that the named object, which a resourceNames-restricted
// grant allows. Unresolvable kinds fail closed.
func (s *Server) canGetIssueRef(r *http.Request, ref issues.Ref) bool {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		return true
	}
	group, resource, clusterScoped, ok := k8s.ResolveChangeGVR(ref.Kind, ref.Group)
	if !ok {
		return false
	}
	namespace := ref.Namespace
	if clusterScoped {
		namespace = ""
	}
	if s.canRead(r, group, resource, namespace, "get") {
		return true
	}
	if ref.Name == "" || s.permCache == nil {
		return false
	}
	perms := s.permCache.Get(user.Username, user.Groups)
	if perms != nil {
		if v, ok := perms.CanINamed("get", group, resource, namespace, ref.Name); ok {
			return v
		}
	}
	client := k8s.GetClient()
	if client == nil {
		return false
	}
	allowed, err := auth.SubjectCanINamed(r.Context(), client, user.Username, user.Groups, namespace, group, resource, ref.Name, "get")
	if err != nil {
		return false
	}
	if perms != nil {
		perms.SetCanINamed("get", group, resource, namespace, ref.Name, allowed)
	}
	return allowed
}

// withholdUnreadableIssueRefs drops grouped issues whose subject the caller
// can't get and member refs of kinds it can't get, counting both. A trimmed
// member list is marked truncated so it never reads as the whole fan-out.
func (s *Server) withholdUnreadableIssueRefs(r *http.Request, in []issues.Issue) ([]issues.Issue, ResourceIssuesWithheld) {
	var withheld ResourceIssuesWithheld
	if auth.UserFromContext(r.Context()) == nil {
		return in, withheld
	}
	out := make([]issues.Issue, 0, len(in))
	for _, issue := range in {
		if !s.canGetIssueRef(r, issues.Ref{Group: issue.Group, Kind: issue.Kind, Namespace: issue.Namespace, Name: issue.Name}) {
			withheld.Issues++
			continue
		}
		if len(issue.Members) > 0 {
			kept := make([]issues.Ref, 0, len(issue.Members))
			for _, m := range issue.Members {
				if s.canGetIssueRef(r, m) {
					kept = append(kept, m)
				} else {
					withheld.Members++
				}
			}
			if len(kept) < len(issue.Members) {
				issue.Members = kept
				issue.MembersTruncated = true
			}
		}
		out = append(out, issue)
	}
	return out, withheld
}

// clusterScopedSubjectIssuesWithheld counts the issues about a cluster-scoped
// subject that the composition left out because the caller can get it but not
// list its kind (the gate /api/issues applies). Without the count a NotReady
// Node would read as having none.
func (s *Server) clusterScopedSubjectIssuesWithheld(r *http.Request, provider issues.Provider, returned []issues.Issue, group, kind, namespace, name string) int {
	if namespace != "" || auth.UserFromContext(r.Context()) == nil {
		return 0
	}
	resolvedGroup, _, clusterScoped, ok := k8s.ResolveChangeGVR(kind, group)
	if !ok || !clusterScoped || s.issueClusterScopedAccess(r)(kind, resolvedGroup) {
		return 0
	}
	subjectKind := func(k, g string) bool {
		return strings.EqualFold(k, kind) && resourceid.NormalizeGroup(g) == resourceid.NormalizeGroup(resolvedGroup)
	}
	all := issues.Compose(provider, issues.Filters{
		SkipPodTemplateContext: true,
		Kinds:                  []string{kind},
		Limit:                  issues.NoLimit,
		CanReadClusterScoped:   subjectKind,
		CanReadRelated:         s.issueRelatedResourceAccess(r),
		CanReadEvidence:        s.issueEvidenceAccess(r),
		Grouped:                true,
	})
	seen := make(map[string]bool, len(returned))
	for _, i := range returned {
		seen[i.ID] = true
	}
	n := 0
	for _, i := range all {
		if !seen[i.ID] && i.Name == name && i.Namespace == "" && subjectKind(i.Kind, i.Group) {
			n++
		}
	}
	return n
}

// resourceIssuesCoverage reports whether the issues engine has the subject's
// kind to read: a typed informer, or a dynamic one synced for its namespace.
func resourceIssuesCoverage(kind, group, namespace string) string {
	if cache := k8s.GetResourceCache(); cache != nil {
		if g, resource, _, ok := k8s.ResolveChangeGVR(kind, group); ok {
			if b, builtin := resourceid.BuiltinForKind(kind); builtin && b.Group == g {
				if synced, known := cache.InformerSynced(resource); known {
					switch {
					case !cache.KindCoversNamespace(resource, namespace):
						return resourceIssuesCoverageNotWatched
					case !synced:
						return resourceIssuesCoverageSyncing
					}
					return resourceIssuesCoverageOK
				}
			}
		}
	}
	disc := k8s.GetResourceDiscovery()
	dyn := k8s.GetDynamicResourceCache()
	if disc == nil || dyn == nil {
		return resourceIssuesCoverageNotWatched
	}
	gvr, ok := disc.GetGVRWithGroup(kind, group)
	if !ok {
		return resourceIssuesCoverageNotWatched
	}
	if dyn.IsNamespaceSynced(gvr, namespace) {
		return resourceIssuesCoverageOK
	}
	for _, watched := range dyn.GetWatchedResources() {
		if watched == gvr {
			return resourceIssuesCoverageSyncing
		}
	}
	return resourceIssuesCoverageNotWatched
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

func (s *Server) issueEvidenceAccess(r *http.Request) func(issues.EvidenceRead) bool {
	return func(read issues.EvidenceRead) bool {
		return s.canRead(r, read.Group, read.Resource, read.Namespace, read.Verb)
	}
}
