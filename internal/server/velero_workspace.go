package server

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"sync"

	"github.com/go-chi/chi/v5"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
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
	if !s.requireConnected(w) || s.writeIfDiscoveryPending(w) {
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
	if s.refuseOutsideVeleroPin(w, namespace) {
		return
	}
	if namespace == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "schedule namespace and name are required")
		return
	}
	if s.writeIfDiscoveryPending(w) {
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

// handleVeleroRuns serves GET /api/velero/runs/{kind}?namespace=: every
// Backup or Restore the caller can read, newest first, through the same
// per-kind access as the views, so a reader of Velero's kinds alone gets them
// without Pod or Deployment access. Coverage says what the read covered; a
// partial read is never an exact count. schedule= filters Backups by their
// schedule label; backup= filters Restores by spec.backupName, before the cap.
func (s *Server) handleVeleroRuns(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	key := velerosvc.RunsKey(chi.URLParam(r, "kind"))
	if key == "" {
		s.writeError(w, http.StatusBadRequest, "runs are listed for backups and restores")
		return
	}
	namespace := r.URL.Query().Get("namespace")
	if namespace != "" && s.refuseOutsideVeleroPin(w, namespace) {
		return
	}
	if s.writeIfDiscoveryPending(w) {
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
	s.writeJSON(w, reader.Runs(r.Context(), key, namespace, velerosvc.RunsFilter{Schedule: r.URL.Query().Get("schedule"), Backup: r.URL.Query().Get("backup")}))
}

// refuseOutsideVeleroPin answers 403 for a namespace Radar's --namespace-scope
// pin excludes. A read there would find an empty cache and report absence, and
// a write would reach past the scope Radar was started with.
func (s *Server) refuseOutsideVeleroPin(w http.ResponseWriter, namespace string) bool {
	if outsideNamespacePin(namespace) == nil {
		return false
	}
	s.writeError(w, http.StatusForbidden, "namespace is outside Radar's namespace scope")
	return true
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

	// Where Velero is installed: every namespace Radar holds any Velero kind
	// in. Each kind is still authorized there on its own, so an installation
	// with no Backups yet is checked for Backups too, and reads as none rather
	// than denied.
	installations := workspaceInstallations(cache, velerosvc.WorkspaceKinds(), budget)

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
				access, items := s.readWorkspaceKindInstalled(r.WithContext(ctx), cache, k, namespaces, []string{veleropkg.Group}, budget, installations)
				if !current() {
					return integration.KindAccess{State: integration.KindCoverageSyncing}, nil
				}
				return access, items
			},
			Deployments: func(ctx context.Context, namespace string) ([]*appsv1.Deployment, bool) {
				if !current() {
					return nil, false
				}
				deployments, ok := s.veleroDeployments(r.WithContext(ctx), cache, namespace)
				return deployments, ok && current()
			},
		},
	}, true
}

// namespacesHeld is where Radar's own identity holds objects of k. Their
// union over a workspace's kinds is where a caller without cluster-wide list
// access is checked: Velero and Longhorn keep their objects in one or two
// namespaces, so this costs a handful of access checks where every namespace
// in the cluster would cost one each, and it does not depend on Radar listing
// Namespaces. When Radar watches k namespace by namespace, its watched
// namespaces stand in. err is the failed read when neither is known.
func namespacesHeld(ctx context.Context, cache *k8s.ResourceCache, k integration.WorkspaceKind, budget *syncBudget) ([]string, error) {
	list, err := listKindInGroups(ctx, cache, k, nil, []string{k.Group}, budget)
	if err == nil {
		seen := map[string]bool{}
		var out []string
		for _, o := range list {
			if ns := o.GetNamespace(); ns != "" && !seen[ns] {
				seen[ns] = true
				out = append(out, ns)
			}
		}
		sort.Strings(out)
		return out, nil
	}
	if _, _, watched, ok := dynamicNamespaceWatches(k, budget); ok {
		return watched, nil
	}
	return nil, err
}

// workspaceInstallations is the union of namespacesHeld over kinds, read once
// per request. err is set only when nothing was found and some read failed:
// an installation Radar hasn't read yet is not the same as none.
func workspaceInstallations(cache *k8s.ResourceCache, kinds []integration.WorkspaceKind, budget *syncBudget) func(context.Context) ([]string, error) {
	var once sync.Once
	var out []string
	var failed error
	return func(ctx context.Context) ([]string, error) {
		once.Do(func() {
			seen := map[string]bool{}
			for _, k := range kinds {
				held, err := namespacesHeld(ctx, cache, k, budget)
				if err != nil && failed == nil {
					failed = err
				}
				for _, ns := range held {
					if !seen[ns] {
						seen[ns] = true
						out = append(out, ns)
					}
				}
			}
			sort.Strings(out)
			if len(out) > 0 {
				failed = nil
			}
		})
		return out, failed
	}
}

// readWorkspaceKindInstalled is readWorkspaceKindAmong checking a caller
// without cluster-wide list access where the workspace is installed. When
// that is unknown because Radar's own read failed, the kind reads as that
// failure (syncing, uncached), not as denied.
func (s *Server) readWorkspaceKindInstalled(r *http.Request, cache *k8s.ResourceCache, k integration.WorkspaceKind, namespaces, groups []string, budget *syncBudget, installations func(context.Context) ([]string, error)) (integration.KindAccess, []*unstructured.Unstructured) {
	var failed error
	candidates := func() []string {
		held, err := installations(r.Context())
		failed = err
		return held
	}
	access, items := s.readWorkspaceKindAmong(r, cache, k, namespaces, groups, budget, candidates, true)
	if access.State == integration.KindCoverageDenied && failed != nil {
		return kindAccessFromListError(k, failed), nil
	}
	return access, items
}

// veleroDeployments are a namespace's Deployments, where the caller may list
// them and Radar holds them: what the Velero server's state is read from.
func (s *Server) veleroDeployments(r *http.Request, cache *k8s.ResourceCache, namespace string) ([]*appsv1.Deployment, bool) {
	if cache == nil || !s.canRead(r, "apps", "deployments", namespace, "list") {
		return nil, false
	}
	if !slices.Contains(cache.AssessKindRead("deployments", []string{namespace}).Read, namespace) || cache.Deployments() == nil {
		return nil, false
	}
	deployments, err := cache.Deployments().Deployments(namespace).List(labels.Everything())
	return deployments, err == nil
}
