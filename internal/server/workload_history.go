package server

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

const (
	workloadHistoryDefaultLimit = 2000
	workloadHistoryMaxLimit     = 10000
	// Ownership rarely nests deeper than CronJob → Job → Pod or
	// Deployment → ReplicaSet → Pod; the bound only stops a cycle or a
	// pathological tree from running away.
	workloadHistoryMaxDepth = 4
	workloadHistoryMaxUIDs  = 5000
	// Rows under the workload's own key read to learn its UIDs (every
	// incarnation Radar recorded).
	workloadHistoryKeyScan = 2000
)

type workloadHistoryResponse struct {
	// Events are newest arrival first.
	Events []timeline.TimelineEvent `json:"events"`
	// Truncated reports that older events exist; pass NextBeforeSeq as
	// before_seq to read them.
	Truncated     bool  `json:"truncated"`
	NextBeforeSeq int64 `json:"nextBeforeSeq,omitempty"`
}

// handleWorkloadHistory returns the timeline of one workload: the workload
// under its key (every incarnation), the resources it owns found by owner UID
// (ReplicaSets, Pods, Jobs…), K8s Events about any of those, and the
// resources attached to it now (Services exposing it, ConfigMaps and Secrets
// it uses, scalers, PDBs, NetworkPolicies, PVCs). Sibling workloads are not
// included, so the view doesn't depend on how busy the namespace is.
func (s *Server) handleWorkloadHistory(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if allowed := s.getUserNamespaces(r, []string{namespace}); noNamespaceAccess(allowed) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}
	kind, group, ok := resolveWorkloadKind(chi.URLParam(r, "kind"), r.URL.Query().Get("group"))
	if !ok {
		s.writeError(w, http.StatusBadRequest, "unknown resource kind "+chi.URLParam(r, "kind"))
		return
	}
	limit := workloadHistoryDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			s.writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = min(n, workloadHistoryMaxLimit)
	}
	var beforeSeq int64
	if v := r.URL.Query().Get("before_seq"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			s.writeError(w, http.StatusBadRequest, "invalid before_seq")
			return
		}
		beforeSeq = n
	}
	store := timeline.GetStore()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Timeline store not available")
		return
	}

	key := resourceid.NewRef(group, kind, namespace, name)
	clusterContext := k8s.ActiveClusterContext()
	scope, err := workloadHistoryScope(r.Context(), store, clusterContext, key, s.workloadAttachments(r, key))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	events, err := store.Query(r.Context(), timeline.QueryOptions{
		Namespaces:       []string{namespace},
		Scope:            scope,
		ClusterContext:   clusterContext,
		UntilSeq:         beforeSeq,
		SequenceOrder:    timeline.SequenceOrderDescending,
		Limit:            limit + 1,
		IncludeManaged:   true,
		IncludeK8sEvents: true,
	})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := workloadHistoryResponse{Events: events}
	if len(events) > limit {
		resp.Events = events[:limit]
		resp.Truncated = true
		resp.NextBeforeSeq = resp.Events[limit-1].Seq
	}
	// The page cursor comes from the unfiltered page: rows the user can't read
	// must not end the paging early.
	resp.Events = s.filterEventsByRBAC(r, resp.Events)
	if resp.Events == nil {
		resp.Events = []timeline.TimelineEvent{}
	}
	s.writeJSON(w, resp)
}

// resolveWorkloadKind turns the route's plural resource name (or a Kind) and
// optional group into the Kind and group timeline rows are recorded under.
func resolveWorkloadKind(resource, group string) (kind, resolvedGroup string, ok bool) {
	if discovery := k8s.GetResourceDiscovery(); discovery != nil {
		if res, found := discovery.GetResourceWithGroup(resource, group); found {
			return res.Kind, res.Group, true
		}
	}
	if group == "" {
		if builtinKind, found := k8s.BuiltinKindForResource(resource); found {
			builtinGroup, _ := resourceid.BuiltinGroup(builtinKind)
			return builtinKind, builtinGroup, true
		}
	}
	return "", "", false
}

// workloadAttachments are the resources attached to the workload right now,
// from the cached topology the detail view's relationships use.
func (s *Server) workloadAttachments(r *http.Request, key resourceid.Ref) []resourceid.Ref {
	if s.broadcaster == nil {
		return nil
	}
	cachedTopo, relIdx := s.broadcaster.GetCachedTopologyWithIndex()
	if cachedTopo == nil {
		return nil
	}
	if auth.UserFromContext(r.Context()) != nil {
		cachedTopo = s.relationshipTopologyForUser(r, cachedTopo)
		relIdx = nil
	}
	rel := topology.GetRelationshipsWithIndex(key.Kind, key.Namespace, key.Name, cachedTopo,
		k8s.NewTopologyResourceProvider(k8s.GetResourceCache()),
		k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()), relIdx)
	return attachedRefs(rel, key.Namespace)
}

// attachedRefs lists the resources whose own history belongs with a
// workload's: the Services exposing it and the ConfigMaps, Secrets, scalers,
// PDBs, NetworkPolicies and PVCs attached to it. Only the workload's
// namespace counts; a Service's other backends are never included.
func attachedRefs(rel *topology.Relationships, namespace string) []resourceid.Ref {
	if rel == nil {
		return nil
	}
	var out []resourceid.Ref
	seen := map[resourceid.Ref]bool{}
	for _, list := range [][]topology.ResourceRef{
		rel.Services, rel.ConfigRefs, rel.Scalers, rel.PDBs, rel.NetworkPolicies, rel.StorageRefs,
	} {
		for _, ref := range list {
			if ref.Namespace != namespace || ref.Kind == "" || ref.Name == "" {
				continue
			}
			group := ref.Group
			if group == "" {
				group, _ = resourceid.BuiltinGroup(ref.Kind)
			}
			id := resourceid.NewRef(group, ref.Kind, ref.Namespace, ref.Name)
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// workloadHistoryScope resolves the rows that belong to a workload's history:
// the UIDs recorded under its key (every incarnation), everything owned
// beneath them by owner UID, and the attached resources by key.
func workloadHistoryScope(ctx context.Context, store timeline.EventStore, clusterContext string, key resourceid.Ref, attached []resourceid.Ref) (timeline.ResourceScope, error) {
	keyRows, err := store.Query(ctx, timeline.QueryOptions{
		Namespaces:       []string{key.Namespace},
		Scope:            timeline.ResourceScope{Refs: []resourceid.Ref{key}},
		ClusterContext:   clusterContext,
		SequenceOrder:    timeline.SequenceOrderDescending,
		Limit:            workloadHistoryKeyScan,
		IncludeManaged:   true,
		IncludeK8sEvents: true,
	})
	if err != nil {
		return timeline.ResourceScope{}, err
	}
	seen := map[string]bool{}
	var uids []string
	add := func(uid string) bool {
		if uid == "" || seen[uid] || len(uids) >= workloadHistoryMaxUIDs {
			return false
		}
		seen[uid] = true
		uids = append(uids, uid)
		return true
	}
	var frontier []string
	for _, e := range keyRows {
		if add(e.UID) {
			frontier = append(frontier, e.UID)
		}
	}
	for depth := 0; depth < workloadHistoryMaxDepth && len(frontier) > 0 && len(uids) < workloadHistoryMaxUIDs; depth++ {
		children, err := store.OwnedUIDs(ctx, clusterContext, frontier, workloadHistoryMaxUIDs-len(uids))
		if err != nil {
			return timeline.ResourceScope{}, err
		}
		frontier = frontier[:0]
		for _, uid := range children {
			if add(uid) {
				frontier = append(frontier, uid)
			}
		}
	}
	return timeline.ResourceScope{
		UIDs: uids,
		// Rows owned by a collected UID that the expansion didn't reach
		// before its UID cap.
		OwnerUIDs: uids,
		Refs:      append([]resourceid.Ref{key}, attached...),
	}, nil
}
