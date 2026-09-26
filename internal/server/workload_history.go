package server

import (
	"context"
	"log"
	"net/http"
	"regexp"
	"strconv"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

const (
	workloadHistoryDefaultLimit = 2000
	// Below the stores' 10,000-row query cap, so the extra row that detects
	// a further page is never clamped away.
	workloadHistoryMaxLimit = 5000
	// Ownership rarely nests deeper than CronJob → Job → Pod or
	// Deployment → ReplicaSet → Pod; the bound only stops a cycle or a
	// pathological tree from running away.
	workloadHistoryMaxDepth = 4
	// Bounds each set the scope is built from: the workload's incarnations
	// and everything they own, and past children found by name.
	workloadHistoryMaxResources = 5000
)

type workloadHistoryResponse struct {
	// Events are newest arrival first.
	Events []timeline.TimelineEvent `json:"events"`
	// Truncated reports that older events exist; pass NextBeforeSeq as
	// before_seq to read them.
	Truncated     bool  `json:"truncated"`
	NextBeforeSeq int64 `json:"nextBeforeSeq,omitempty"`
	// Incomplete reports that the workload has more related resources than
	// the history follows, so some of their events are missing.
	Incomplete bool `json:"incomplete,omitempty"`
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
	live := liveWorkloadIdentity(r.Context(), chi.URLParam(r, "kind"), group, namespace, name)
	scope, incomplete, err := workloadHistoryScope(r.Context(), store, clusterContext, key, live, s.workloadAttachments(r, key, live.Object))
	if err != nil {
		log.Printf("[workload-history] Failed to resolve the history scope of %s %s/%s: %v", kind, namespace, name, err)
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
		log.Printf("[workload-history] Failed to query the history of %s %s/%s: %v", kind, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := workloadHistoryResponse{Events: events, Incomplete: incomplete}
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

// liveIdentity is the workload as it exists now: the object, its UID and its
// controller's UID.
type liveIdentity struct {
	Object   metav1.Object
	UID      string
	OwnerUID string
}

// liveWorkloadIdentity reads the workload as it exists now. The timeline
// doesn't always hold a row under the workload's own key (a busy cluster may
// only have recorded its ReplicaSets and Pods), so the ownership walk can't
// rely on those rows alone to know the workload's UID.
func liveWorkloadIdentity(ctx context.Context, resource, group, namespace, name string) liveIdentity {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return liveIdentity{}
	}
	var obj metav1.Object
	if group == "" || k8s.TypedKindOwnsGroup(resource, group) {
		if o, err := k8s.FetchResource(cache, resource, namespace, name); err == nil {
			if m, err := meta.Accessor(o); err == nil {
				obj = m
			}
		}
	}
	if obj == nil {
		if u, err := cache.GetDynamicWithGroup(ctx, resource, namespace, name, group); err == nil && u != nil {
			obj = u
		}
	}
	if obj == nil {
		return liveIdentity{}
	}
	id := liveIdentity{Object: obj, UID: string(obj.GetUID())}
	if ref := metav1.GetControllerOf(obj); ref != nil {
		id.OwnerUID = string(ref.UID)
	}
	return id
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
// from the cached topology the detail view's relationships use. The live
// object, when there is one, keeps a CRD whose kind collides with another's
// (a Volcano Job and a batch Job) on its own topology node.
func (s *Server) workloadAttachments(r *http.Request, key resourceid.Ref, obj metav1.Object) []resourceid.Ref {
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
	var object any
	if obj != nil {
		object = obj
	}
	rel := topology.GetRelationshipsWithObject(key.Kind, key.Namespace, key.Name, object, cachedTopo,
		k8s.NewTopologyResourceProvider(k8s.GetResourceCache()),
		k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()), relIdx)
	return attachedRefs(rel, key.Namespace)
}

// attachedRefs lists the resources whose own history belongs with a
// workload's: the Services exposing it, the Ingresses and routes in front of
// those, and the ConfigMaps, Secrets, scalers, PDBs, NetworkPolicies and PVCs
// attached to it. Only the workload's
// namespace counts; a Service's other backends are never included.
func attachedRefs(rel *topology.Relationships, namespace string) []resourceid.Ref {
	if rel == nil {
		return nil
	}
	var out []resourceid.Ref
	seen := map[resourceid.Ref]bool{}
	// Gateways are left out: one usually fronts many unrelated Services.
	for _, list := range [][]topology.ResourceRef{
		rel.Services, rel.Ingresses, rel.Routes, rel.ConfigRefs, rel.Scalers, rel.PDBs, rel.NetworkPolicies, rel.StorageRefs,
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
// its live UID and the UIDs recorded under its key (every incarnation),
// everything owned beneath them by owner UID, its controller's own rows, the
// attached resources by key, and past children whose rows never learned an
// owner, by the name their controller gives them. incomplete reports that a
// bound stopped the walk short.
func workloadHistoryScope(ctx context.Context, store timeline.EventStore, clusterContext string, key resourceid.Ref, live liveIdentity, attached []resourceid.Ref) (scope timeline.ResourceScope, incomplete bool, err error) {
	incarnations, err := store.Identities(ctx, timeline.IdentityQuery{
		ClusterContext: clusterContext,
		Namespace:      key.Namespace,
		Ref:            &key,
	}, workloadHistoryMaxResources)
	if err != nil {
		return timeline.ResourceScope{}, false, err
	}
	incomplete = len(incarnations) >= workloadHistoryMaxResources
	seen := map[string]bool{}
	var uids []string
	add := func(uid string) bool {
		if uid == "" || seen[uid] {
			return false
		}
		if len(uids) >= workloadHistoryMaxResources {
			incomplete = true
			return false
		}
		seen[uid] = true
		uids = append(uids, uid)
		return true
	}
	var frontier []string
	if add(live.UID) {
		frontier = append(frontier, live.UID)
	}
	parents := map[string]bool{}
	if live.OwnerUID != "" {
		parents[live.OwnerUID] = true
	}
	for _, id := range incarnations {
		if add(id.UID) {
			frontier = append(frontier, id.UID)
		}
		if id.OwnerUID != "" {
			parents[id.OwnerUID] = true
		}
	}
	for depth := 0; len(frontier) > 0; depth++ {
		if depth == workloadHistoryMaxDepth {
			deeper, err := store.OwnedUIDs(ctx, clusterContext, frontier, 1)
			if err != nil {
				return timeline.ResourceScope{}, false, err
			}
			incomplete = incomplete || len(deeper) > 0
			break
		}
		children, err := store.OwnedUIDs(ctx, clusterContext, frontier, workloadHistoryMaxResources-len(uids)+1)
		if err != nil {
			return timeline.ResourceScope{}, false, err
		}
		frontier = frontier[:0]
		for _, uid := range children {
			if add(uid) {
				frontier = append(frontier, uid)
			}
		}
	}
	named, namedIncomplete, err := unownedChildrenByName(ctx, store, clusterContext, key)
	if err != nil {
		return timeline.ResourceScope{}, false, err
	}
	scopeUIDs := append([]string{}, uids...)
	for uid := range parents {
		// The controller's own rows only. Its other children stay out: its
		// UID is never walked or used as an owner.
		if !seen[uid] {
			scopeUIDs = append(scopeUIDs, uid)
		}
	}
	return timeline.ResourceScope{
		UIDs: scopeUIDs,
		// Rows owned by a collected UID whose own rows the walk didn't reach
		// (a K8s Event recorded before its subject's first informer row).
		OwnerUIDs:     uids,
		Refs:          append([]resourceid.Ref{key}, attached...),
		OwnerlessRefs: named,
	}, incomplete || namedIncomplete, nil
}

// safeSuffix is the alphabet Kubernetes uses for generated name suffixes
// (pod-template hashes, pod names): consonants and digits, so a word like
// "backend" in a sibling's name never passes for one.
const safeSuffix = `[bcdfghjklmnpqrstvwxz2456789]`

// childPattern is the name a controller gives one kind of child, and the
// API group that child is created in.
type childPattern struct {
	group string
	name  *regexp.Regexp
}

// childNamePatterns are the names a workload's controller gives what it
// creates, keyed by child kind: a CronJob's Jobs are <name>-<scheduled
// minute>, a Deployment's ReplicaSets <name>-<hash>, and each level's Pods
// add a generated suffix. They're the built-in controllers' (and Argo's)
// contracts, so they apply only to a parent in that controller's group — a
// same-named kind from another group (Volcano's Job) names children its own
// way. A bare Workflow has no such pattern: many are themselves named
// <prefix>-<random>, so its siblings would match.
func childNamePatterns(group, kind, name string) map[string]childPattern {
	n := regexp.QuoteMeta(name)
	child := func(childGroup, expr string) childPattern {
		return childPattern{group: childGroup, name: regexp.MustCompile("^" + n + expr + "$")}
	}
	pod := func(expr string) childPattern { return child("", expr) }
	switch group + "/" + kind {
	case "batch/CronJob":
		return map[string]childPattern{"Job": child("batch", `-[0-9]{8,10}`), "Pod": pod(`-[0-9]{8,10}-` + safeSuffix + `{5}`)}
	case "batch/Job", "apps/ReplicaSet", "apps/DaemonSet":
		return map[string]childPattern{"Pod": pod(`-` + safeSuffix + `{5}`)}
	case "apps/Deployment", "argoproj.io/Rollout":
		return map[string]childPattern{"ReplicaSet": child("apps", `-`+safeSuffix+`{5,10}`), "Pod": pod(`-` + safeSuffix + `{5,10}-` + safeSuffix + `{5}`)}
	case "apps/StatefulSet":
		return map[string]childPattern{"Pod": pod(`-[0-9]+`)}
	case "argoproj.io/CronWorkflow":
		// Workflows are <name>-<scheduled unix second>; their Pods add
		// -<template>-<node hash>.
		return map[string]childPattern{"Workflow": child("argoproj.io", `-[0-9]{9,11}`), "Pod": pod(`-[0-9]{9,11}-[a-z0-9-]+-[0-9]+`)}
	}
	return nil
}

// unownedChildrenByName finds past children whose rows never learned their
// owner — mostly K8s Events about Jobs and Pods deleted before Radar started —
// by the names the workload's controller gives its children. They join the
// scope as OwnerlessRefs, so a row that does record an owner is never pulled
// in by name.
func unownedChildrenByName(ctx context.Context, store timeline.EventStore, clusterContext string, key resourceid.Ref) ([]resourceid.Ref, bool, error) {
	patterns := childNamePatterns(key.Group, key.Kind, key.Name)
	if len(patterns) == 0 {
		return nil, false, nil
	}
	kinds := make([]string, 0, len(patterns))
	for kind := range patterns {
		kinds = append(kinds, kind)
	}
	ids, err := store.Identities(ctx, timeline.IdentityQuery{
		ClusterContext: clusterContext,
		Namespace:      key.Namespace,
		Kinds:          kinds,
		NamePrefix:     key.Name + "-",
		OwnerUnknown:   true,
	}, workloadHistoryMaxResources)
	if err != nil {
		return nil, false, err
	}
	seen := map[resourceid.Ref]bool{}
	var out []resourceid.Ref
	for _, id := range ids {
		pattern, ok := patterns[id.Kind]
		if !ok || !pattern.name.MatchString(id.Name) {
			continue
		}
		// A row that didn't record its apiVersion can't contradict the group.
		if id.APIVersion != "" && resourceid.GroupFromAPIVersion(id.APIVersion) != pattern.group {
			continue
		}
		ref := resourceid.NewRef(pattern.group, id.Kind, key.Namespace, id.Name)
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out, len(ids) >= workloadHistoryMaxResources, nil
}
