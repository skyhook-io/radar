package server

import (
	"context"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	longhornsvc "github.com/skyhook-io/radar/internal/longhorn"
	"github.com/skyhook-io/radar/pkg/k8score"
	longhornpkg "github.com/skyhook-io/radar/pkg/longhorn"
)

// handleLonghornWorkspace serves GET /api/longhorn/workspace: every Longhorn
// Volume the caller can read. The namespace picker is not applied, since
// Longhorn keeps its objects in longhorn-system; each kind is authorized per
// namespace on the caller's own access, as for Velero. PVs need the caller's
// cluster-wide list, and a claim is named only with PVC access in its
// namespace.
func (s *Server) handleLonghornWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) || s.writeIfDiscoveryPending(w) {
		return
	}
	reader, current, ok := s.longhornReader(r)
	if !ok {
		s.writeError(w, http.StatusServiceUnavailable, "cluster connection is changing; try again")
		return
	}
	resp := reader.Workspace(r.Context())
	// A switch during the read may have let claim checks run against the
	// new cluster while the PVs came from the old one.
	if !current() {
		s.writeError(w, http.StatusServiceUnavailable, "cluster connection is changing; try again")
		return
	}
	s.writeJSON(w, resp)
}

// longhornReader captures the connection once and binds every read to the
// caller, as veleroReader does. ok=false when the connection changes during
// capture; current reports whether it is still the one serving.
func (s *Server) longhornReader(r *http.Request) (reader *longhornsvc.Reader, current func() bool, ok bool) {
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
		return nil, nil, false
	}
	current = func() bool { return operation != nil && operation.Err() == nil }
	budget := newSyncBudget(r.Context())
	budget.bound, budget.discovery, budget.dynamicCache, budget.readOnly = true, discovery, dynamicCache, true

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
		_, installed = discovery.GetGVRWithGroup("Volume", longhornpkg.Group)
	}

	// Where Longhorn is installed: every namespace Radar holds a Volume or an
	// Engine in, checked for a caller without cluster-wide list access.
	installations := workspaceInstallations(cache, longhornsvc.WorkspaceKinds(), budget)

	return &longhornsvc.Reader{
		ClusterContext: clusterContext,
		Observations: longhornsvc.Observations{
			Installed:       installed,
			Namespaces:      scope,
			ForcedNamespace: forcedNamespace,
			WorkspaceRead: func(ctx context.Context, k integration.WorkspaceKind, namespaces []string) (integration.KindAccess, []*unstructured.Unstructured) {
				if !current() {
					return integration.KindAccess{State: integration.KindCoverageSyncing}, nil
				}
				access, items := s.readWorkspaceKindInstalled(r.WithContext(ctx), cache, k, namespaces, []string{longhornpkg.Group}, budget, installations)
				if !current() {
					return integration.KindAccess{State: integration.KindCoverageSyncing}, nil
				}
				return access, items
			},
			PersistentVolumes: func() ([]*corev1.PersistentVolume, string) {
				if !s.canRead(r, "", "persistentvolumes", "", "list") {
					return nil, integration.KindCoverageDenied
				}
				objs, ok := cache.ReadableObjects("persistentvolumes", nil)
				switch {
				case !ok && cache.KindReadinessFor("persistentvolumes") == k8score.KindUnavailable:
					return nil, integration.KindCoverageUncached
				case !ok || !current():
					return nil, integration.KindCoverageSyncing
				}
				pvs := make([]*corev1.PersistentVolume, 0, len(objs))
				for _, o := range objs {
					if pv, isPV := o.(*corev1.PersistentVolume); isPV {
						pvs = append(pvs, pv)
					}
				}
				return pvs, integration.KindCoverageFull
			},
			ClaimReadable: func(namespace string) bool {
				return s.canRead(r, "", "persistentvolumeclaims", namespace, "list")
			},
			VolumeNames: func(ctx context.Context, k integration.WorkspaceKind) (map[string]int, bool) {
				list, err := listKindInGroups(ctx, cache, k, nil, []string{longhornpkg.Group}, budget)
				if err != nil || !current() {
					return nil, false
				}
				counts := map[string]int{}
				for _, v := range list {
					counts[v.GetName()]++
				}
				return counts, true
			},
		},
	}, current, true
}
