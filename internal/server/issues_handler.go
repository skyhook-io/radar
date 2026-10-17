package server

import (
	"context"
	"errors"
	"fmt"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/internal/readscope"
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
	if readscope.NoNamespaceAccess(namespaces) {
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
		CanReadSubject:       s.issueSubjectAccess(r),
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
	helmIssues, helmUnchecked := s.nativeHelmIssuesForRequest(r, namespaces, filters)
	out, stats = issues.MergeExternalIssues(out, stats, filters, helmIssues)
	// Shared base response shape (issues.ListResponse); surfaces add their
	// own enrichments after this point.
	resp := issues.NewListResponse(out, stats)
	if helmUnchecked {
		resp.UncheckedSources = []issuesapi.UncheckedSource{{Source: issuesapi.UncheckedSourceHelm, Reason: issuesapi.UncheckedReasonReadFailed}}
	}
	cache, _, _ := provider.CacheIdentity()
	resp.UncheckedSources = append(resp.UncheckedSources, issues.AbsenceUncheckedSources(cache, namespaces)...)
	resp.ClusterContext = provider.ClusterContextForIssues(namespaces, func(group, resource string) bool {
		return s.canRead(r, group, resource, "kube-system", "list")
	})
	if len(namespaces) == 1 && stats.TotalMatched == len(out) && meaningfulchanges.IssueChangesQueryEligible(q.Get("kind"), q.Get("filter"), q.Get("severity")) {
		if recentChangesReason := meaningfulchanges.IssueChangesReason(out); recentChangesReason != "" {
			recentResult, err := meaningfulchanges.Recent(r.Context(), meaningfulchanges.Query{
				Namespaces: []string{namespaces[0]},
				Since:      meaningfulchanges.DefaultSince,
				Limit:      meaningfulchanges.IssueChangesFetchLimit(recentChangesReason),
				FieldLimit: meaningfulchanges.DefaultFieldLimit,
			})
			if err != nil {
				resp.RecentChangesError = err.Error()
			} else if len(recentResult.Changes) > 0 {
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

var helmIssuesDeniedLogged sync.Map

// nativeHelmIssuesForRequest reads Helm release storage on the request's
// context, so the read stops when the caller disconnects instead of running
// to completion for nobody. unchecked reports a failed read whose answer
// someone is still waiting for (see helm.IssueReadUnchecked).
func (s *Server) nativeHelmIssuesForRequest(r *http.Request, namespaces []string, filters issues.Filters) (found []issues.Issue, unchecked bool) {
	if !issues.KindFilterIncludes(filters.Kinds, "HelmRelease", "helmreleases") {
		return nil, false
	}
	helmClient := helm.GetClient()
	if helmClient == nil {
		return nil, false
	}
	ctx := r.Context()
	username, groups := "", []string(nil)
	if user := auth.UserFromContext(ctx); user != nil {
		username = user.Username
		groups = user.Groups
	}
	helmNamespaces := namespaces
	if helmNamespaces == nil {
		var ok bool
		helmNamespaces, ok = s.resolveHelmNamespaces(r)
		if !ok {
			return nil, false
		}
	}
	releases, err := helmClient.ListIssueReleasesAcrossNamespaces(ctx, helmNamespaces, username, groups)
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			// The caller went away; nobody is left to tell.
		case !helm.IssueReadDenied(err):
			log.Printf("[issues] Failed to list Helm releases for issue stream: %v", err)
		default:
			if _, seen := helmIssuesDeniedLogged.LoadOrStore(pkgauth.IdentityCacheKey(username, groups), struct{}{}); !seen {
				// Logged once per identity (username + groups): the alerts worker
				// polls this, and a cluster without a Secret-read binding would
				// otherwise drop Helm alerts with no trace anywhere.
				log.Printf("[issues] Helm release issues omitted for %q: Kubernetes denied listing release Secrets", username)
			}
		}
		return nil, helm.IssueReadUnchecked(ctx, err)
	}
	return issues.NativeHelmReleaseIssues(releases, time.Now()), false
}

// handleResourceIssues serves GET /api/issues/resource/{kind}/{namespace}/{name}
// — the live Issues that touch ONE resource: its own issues plus, for a workload,
// the issues on its owned pods (owner rollup). Backs the "Operational Issues"
// section in the resource detail. Namespace "_" denotes a cluster-scoped resource;
// optional ?group= disambiguates a CRD whose kind collides with a core kind.
//
// RBAC follows the drawer (docs/authentication.md, "Read granularity"): the
// preflight gates the subject, and every row is gated the same way before
// grouping, so a member, count or diagnostic link never comes from a namespace
// or a Secret the caller can't open. A cluster-scoped subject's issues are
// composed across namespaces, which that per-row gate keeps to the caller's.
// ?coverage=1 returns an envelope that also says whether Radar watches the
// subject's kind and counts what the caller's access withheld.
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

	// Scope the scan to the resource's namespace (a workload's owned pods live
	// there too); cluster-scoped resources scan all namespaces (nil), and
	// CanReadSubject drops the rows from namespaces the caller can't open.
	var namespaces []string
	if namespace != "" {
		namespaces = []string{namespace}
	}

	evidenceWithheld := map[string]bool{}
	related := issues.RelatedIssues(provider, issues.RelatedIssueOptions{
		Namespaces:           namespaces,
		CanReadClusterScoped: s.issueClusterScopedAccess(r),
		CanReadRelated:       s.issueRelatedResourceAccess(r),
		CanReadSubject:       s.issueSubjectAccess(r),
		CanReadEvidence:      s.issueEvidenceAccess(r),
		OnEvidenceWithheld: func(i issues.Issue) {
			if issues.Concerns(i, group, kind, namespace, name) {
				evidenceWithheld[i.ID] = true
			}
		},
	}, group, kind, namespace, name)
	if related == nil {
		related = []issues.Issue{}
	}
	if r.URL.Query().Get("coverage") != "1" {
		s.writeJSON(w, related)
		return
	}
	withheld := ResourceIssuesWithheld{Issues: len(evidenceWithheld)}
	withheld.Issues += s.clusterScopedSubjectIssuesWithheld(r, provider, related, group, kind, namespace, name)
	resp := ResourceIssuesResponse{
		Issues:   related,
		Coverage: resourceIssuesCoverage(kind, group, namespace),
	}
	if withheld.Issues > 0 {
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
	// Whether the issues engine reads the subject's kind: ok, syncing,
	// notWatched, or unknownKind (discovery doesn't serve the kind, as when a
	// link made on one cluster opens on another). Anything but ok makes an
	// empty list unknown, not none.
	Coverage   string                  `json:"coverage"`
	Withheld   *ResourceIssuesWithheld `json:"withheld,omitempty"`
	Visibility *k8s.VisibilitySummary  `json:"visibility,omitempty"`
}

// ResourceIssuesWithheld counts what the caller's access kept out of the
// answer, without naming it.
type ResourceIssuesWithheld struct {
	// Issues about the subject the caller's access removed: a cluster-scoped
	// kind it can get but not list, or evidence it can't read.
	Issues int `json:"issues"`
}

const (
	resourceIssuesCoverageOK          = "ok"
	resourceIssuesCoverageSyncing     = "syncing"
	resourceIssuesCoverageNotWatched  = "notWatched"
	resourceIssuesCoverageUnknownKind = "unknownKind"
)

// clusterScopedSubjectIssuesWithheld counts the issues about a cluster-scoped
// subject that the composition left out because the caller can get it but not
// list its kind (the gate /api/issues applies). Without the count a NotReady
// Node would read as having none.
func (s *Server) clusterScopedSubjectIssuesWithheld(r *http.Request, provider issues.Provider, returned []issues.Issue, group, kind, namespace, name string) int {
	if namespace != "" || auth.UserFromContext(r.Context()) == nil {
		return 0
	}
	resolvedGroup, resource, clusterScoped, ok := k8s.ResolveChangeGVR(kind, group)
	if !ok || !clusterScoped || s.issueClusterScopedAccess(r)(kind, resolvedGroup) {
		return 0
	}
	subjectKind := func(k, g string) bool {
		return strings.EqualFold(k, kind) && resourceid.NormalizeGroup(g) == resourceid.NormalizeGroup(resolvedGroup)
	}
	canReadEvidence := s.issueEvidenceAccess(r)
	// The get-authorized subject is counted, never returned; controller
	// inventories still use the caller's permissions.
	countSubjectEvidence := func(read issues.EvidenceRead) bool {
		if read.Group == resolvedGroup && read.Resource == resource && read.Namespace == "" && read.Verb == "list" {
			return true
		}
		return canReadEvidence(read)
	}
	all := issues.Compose(provider, issues.Filters{
		SkipPodTemplateContext: true,
		Kinds:                  []string{kind},
		Limit:                  issues.NoLimit,
		CanReadClusterScoped:   subjectKind,
		CanReadRelated:         s.issueRelatedResourceAccess(r),
		CanReadSubject:         s.issueSubjectAccess(r),
		CanReadEvidence:        countSubjectEvidence,
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
		return resourceIssuesCoverageUnknownKind
	}
	if dyn.IsNamespaceSynced(gvr, namespace) {
		return resourceIssuesCoverageOK
	}
	// A watch in another namespace doesn't cover this one.
	if dyn.HasCoveringInformer(gvr, namespace) {
		return resourceIssuesCoverageSyncing
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

// issueSubjectAccess is the drawer's gate for an issue's own namespaced
// resource: namespace access, plus a get for Secrets and LimitRanges. It runs
// once per composed row, so the caller's namespaces are resolved once.
func (s *Server) issueSubjectAccess(r *http.Request) func(issues.Ref) bool {
	if auth.UserFromContext(r.Context()) == nil {
		return func(issues.Ref) bool { return true }
	}
	inNamespace := s.callerNamespaces(r)
	return func(ref issues.Ref) bool {
		if ref.Namespace == "" {
			return true // cluster-scoped rows are CanReadClusterScoped's
		}
		if !inNamespace(ref.Namespace) {
			return false
		}
		if ref.Group == "" {
			switch {
			case strings.EqualFold(ref.Kind, "Secret"):
				return s.canRead(r, "", "secrets", ref.Namespace, "get")
			case strings.EqualFold(ref.Kind, "LimitRange"):
				return s.canRead(r, "", "limitranges", ref.Namespace, "get")
			}
		}
		return true
	}
}

// issueEvidenceAccess authorizes an inventory a finding was derived from at
// the granularity Radar reads it: a namespaced read needs namespace access
// (and a Secret read a Secrets check); a cluster-wide read needs the SAR.
func (s *Server) issueEvidenceAccess(r *http.Request) func(issues.EvidenceRead) bool {
	if auth.UserFromContext(r.Context()) == nil {
		return func(issues.EvidenceRead) bool { return true }
	}
	inNamespace := s.callerNamespaces(r)
	return func(read issues.EvidenceRead) bool {
		if read.Namespace == "" {
			return s.canRead(r, read.Group, read.Resource, "", read.Verb)
		}
		if !inNamespace(read.Namespace) {
			return false
		}
		if read.Group == "" && read.Resource == "secrets" {
			return s.canRead(r, "", "secrets", read.Namespace, read.Verb)
		}
		return true
	}
}

// callerNamespaces reports whether the caller may read a namespace, resolving
// its namespaces on first use.
func (s *Server) callerNamespaces(r *http.Request) func(string) bool {
	var once sync.Once
	var allowed map[string]bool
	all := false
	return func(namespace string) bool {
		once.Do(func() {
			namespaces := s.getUserNamespaces(r, nil)
			if namespaces == nil {
				all = true
				return
			}
			allowed = make(map[string]bool, len(namespaces))
			for _, ns := range namespaces {
				allowed[ns] = true
			}
		})
		return all || allowed[namespace]
	}
}
