package server

import (
	"context"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
	velerosvc "github.com/skyhook-io/radar/internal/velero"
	veleropkg "github.com/skyhook-io/radar/pkg/velero"
)

// handleVeleroWorkspace serves GET /api/velero/workspace: every Velero
// installation the caller can read, as schedule and storage-location readings.
//
// The namespace picker is not applied. Velero keeps its objects in its own
// namespace (velero, or kommander on NKP) while the picker selects
// application namespaces, so applying it would empty the view. Each kind is
// still authorized per namespace on the caller's own access, the
// --namespace-scope clamp still bounds the request, and no namespace the
// caller did not name is ever reported as denied.
func (s *Server) handleVeleroWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	reader, ok := s.veleroReader(r)
	if !ok {
		s.writeError(w, http.StatusServiceUnavailable, "cluster connection is changing; try again")
		return
	}
	s.writeJSON(w, reader.Workspace(r.Context()))
}

// handleVeleroScheduleActivity serves
// GET /api/velero/schedules/{namespace}/{name}/activity: one Schedule's
// reading with its recent runs, for the Schedule drawer.
func (s *Server) handleVeleroScheduleActivity(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if namespace == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "schedule namespace and name are required")
		return
	}
	reader, ok := s.veleroReader(r)
	if !ok {
		s.writeError(w, http.StatusServiceUnavailable, "cluster connection is changing; try again")
		return
	}
	if !reader.Observations.Installed {
		s.writeError(w, http.StatusNotFound, "Velero is not installed")
		return
	}
	if forced := reader.Observations.ForcedNamespace; forced != "" && forced != namespace {
		s.writeError(w, http.StatusForbidden, "namespace is outside Radar's namespace scope")
		return
	}
	resp, readable, found := reader.ScheduleActivity(r.Context(), namespace, name)
	switch {
	case !readable && resp.Coverage["schedules"].State == integration.KindCoverageDenied:
		s.writeError(w, http.StatusForbidden, "no access to Velero schedules")
	case !readable && resp.Coverage["schedules"].State == integration.KindCoverageSyncing:
		s.writeError(w, http.StatusServiceUnavailable, "Velero schedules are still loading")
	case !readable:
		s.writeError(w, http.StatusServiceUnavailable, "Radar could not read Velero schedules in this namespace")
	case !found:
		s.writeError(w, http.StatusNotFound, "schedule not found")
	default:
		s.writeJSON(w, resp)
	}
}

// veleroReader captures the connection once and binds every read to the
// caller. ok=false when the cluster connection changes during capture.
func (s *Server) veleroReader(r *http.Request) (*velerosvc.Reader, bool) {
	var cache *k8s.ResourceCache
	var discovery *k8s.ResourceDiscovery
	var dynamicCache *k8s.DynamicResourceCache
	var operation context.Context
	var clusterContext, target string
	var forced bool
	captured := k8s.CaptureClusterReads(func() {
		operation = k8s.OperationContext()
		cache, discovery, dynamicCache = k8s.GetResourceCache(), k8s.GetResourceDiscovery(), k8s.GetDynamicResourceCache()
		clusterContext = k8s.ActiveClusterContext()
		forced, target = k8s.ForceNamespaceScope, k8s.GetNamespaceScopeTarget()
	})
	if !captured || cache == nil {
		return nil, false
	}
	current := func() bool { return operation != nil && operation.Err() == nil }
	budget := newSyncBudget(r.Context())
	budget.bound, budget.discovery, budget.dynamicCache, budget.readOnly = true, discovery, dynamicCache, true

	// nil scope: every namespace the caller may read, never named when denied.
	// A forced --namespace-scope narrows the request to its target.
	var scope []string
	forcedNamespace := ""
	if forced {
		scope, forcedNamespace = []string{target}, target
		if target == "" {
			scope = []string{}
		}
	}
	installed := false
	if discovery != nil {
		_, installed = discovery.GetGVRWithGroup("Schedule", veleropkg.Group)
	}

	return &velerosvc.Reader{
		ClusterContext: clusterContext,
		Observations: velerosvc.Observations{
			Installed:       installed,
			Namespaces:      scope,
			ForcedNamespace: forcedNamespace,
			WorkspaceRead: func(ctx context.Context, k integration.WorkspaceKind, namespaces []string) (integration.KindAccess, []*unstructured.Unstructured) {
				if !current() {
					return integration.KindAccess{State: integration.KindCoverageSyncing}, nil
				}
				access, items := s.readWorkspaceKind(r.WithContext(ctx), cache, k, namespaces, []string{veleropkg.Group}, budget)
				if !current() {
					return integration.KindAccess{State: integration.KindCoverageSyncing}, nil
				}
				return access, items
			},
			Deployments: func(ctx context.Context, namespace string) ([]*appsv1.Deployment, bool) {
				if !current() || !s.canRead(r.WithContext(ctx), "apps", "deployments", namespace, "list") {
					return nil, false
				}
				if !slices.Contains(cache.AssessKindRead("deployments", []string{namespace}).Read, namespace) || cache.Deployments() == nil {
					return nil, false
				}
				deployments, err := cache.Deployments().Deployments(namespace).List(labels.Everything())
				return deployments, err == nil && current()
			},
			Issues: func(ctx context.Context, namespaces []string) []issues.Issue {
				if !current() || readscope.NoNamespaceAccess(namespaces) {
					return nil
				}
				provider := issues.NewCacheProvider()
				if provider == nil {
					return nil
				}
				rq := r.WithContext(ctx)
				rows, _ := issues.ComposeWithStats(provider, issues.Filters{
					Namespaces: namespaces, Limit: issues.NoLimit,
					CanReadClusterScoped: s.issueClusterScopedAccess(rq), CanReadRelated: s.issueRelatedResourceAccess(rq),
					CanReadSubject: s.issueSubjectAccess(rq), CanReadEvidence: s.issueEvidenceAccess(rq),
				})
				if !current() {
					return nil
				}
				return rows
			},
		},
	}, true
}
