package server

import (
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	capacitymodel "github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	internaltimeline "github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	"github.com/skyhook-io/radar/pkg/subject"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
)

const (
	capacityOverviewPoolLimit  = 100
	capacityDefaultPageLimit   = 50
	capacityMaxPageLimit       = 100
	capacityActionSubjectLimit = 25
)

// capacityClusterIdentity pins every cluster-scoped singleton a capacity
// response is assembled from — both the naming stamp and each source handle. A
// kubeconfig context switch replaces all of them, so a response stitched across
// one would silently mix two clusters: pool specs from the old cluster next to
// nodes from the new one, or cluster-A metadata over cluster-B data.
//
// Membership rule: the tuple must CHANGE whenever the cluster behind the
// process globals changes. Every context switch or reconnect runs
// ResetAllSubsystems, which constructs new objects for each pointer member
// here — so the members are the reset-swapped singletons (SAR client, the
// three caches, metrics history, timeline store) plus the naming stamp.
// Globals a loader reads that are NOT members (connection status, the
// timeline observation start, the namespace-scope target) are still covered:
// nothing changes what they describe without a reset that swaps the members,
// which the final equality check then rejects. Per-user caches (permCache,
// rbacMemo, capacityIssueMemo) are deliberately absent: they are Server
// fields that are never swapped — the switch hook invalidates their CONTENTS
// — so a pointer here would never change and would prove nothing.
type capacityClusterIdentity struct {
	// contextName, activeClusterContext and clusterName are three separate
	// reads, so a switch landing between them yields a mixed snapshot — which
	// the final equality check then rejects, exactly as it rejects a later one.
	contextName          string
	activeClusterContext string
	clusterName          string
	client               *kubernetes.Clientset
	resource             *k8s.ResourceCache
	dynamic              *k8s.DynamicResourceCache
	discovery            *k8s.ResourceDiscovery
	metrics              *k8s.MetricsHistoryStore
	timeline             internaltimeline.EventStore
}

// capacityClusterIdentityNow resolves the live cluster identity. Both the
// capture and the re-check go through this one seam: a real kubeconfig switch
// lands on another goroutine at an arbitrary moment, so injecting it here is
// the only deterministic way to exercise what the gate is for.
var capacityClusterIdentityNow = liveCapacityClusterIdentity

func liveCapacityClusterIdentity() capacityClusterIdentity {
	return capacityClusterIdentity{
		contextName:          k8s.GetContextName(),
		activeClusterContext: k8s.ActiveClusterContext(),
		clusterName:          k8s.GetClusterName(),
		client:               k8s.GetClient(),
		resource:             k8s.GetResourceCache(),
		dynamic:              k8s.GetDynamicResourceCache(),
		discovery:            k8s.GetResourceDiscovery(),
		metrics:              k8s.GetMetricsHistory(),
		timeline:             internaltimeline.GetStore(),
	}
}

func currentCapacityClusterIdentity() capacityClusterIdentity {
	return capacityClusterIdentityNow()
}

func (identity capacityClusterIdentity) stillCurrent() bool {
	return identity == capacityClusterIdentityNow()
}

type capacityLoadResult struct {
	state    capacityapi.IntegrationState
	meta     capacityapi.ResponseMeta
	model    *capacitymodel.Model
	snapshot *capacitymodel.Snapshot
	// identity is the cluster the sources behind this result were read from,
	// captured before the first read. writeCapacityResponse re-checks it.
	identity capacityClusterIdentity
}

// writeCapacityResponse is the ONLY way a capacity response reaches the wire.
// Every early return — denied, not detected, syncing, cache unavailable, list
// error — and every post-load enrichment the handlers add afterwards (the
// Overview's Karpenterless model and autoscaler groups) passes through here, so
// the coherence check cannot be bypassed by a path that skips the full loader.
//
// Three properties together make a mixed-cluster response unserializable:
//
//  1. CAPTURE FIRST. Every handler takes the identity snapshot as its literal
//     first statement — before the connection gate, metadata, cursor parsing,
//     authorization, and any source read. Nothing upstream of the snapshot can
//     contribute another cluster's data to the response.
//  2. THE SNAPSHOT IS THE SOURCE OF TRUTH for anything derived from cluster
//     naming: the response metadata's ClusterContext and the cursor's cluster
//     binding are stamped from it, never re-read from the globals.
//  3. FINAL EQUALITY. Pointer identity is what proves the reads were
//     consistent. The loaders do re-fetch globals rather than thread the
//     captured handles, but any switch or reconnect runs ResetAllSubsystems,
//     which constructs NEW objects for every pointer member — so a loader that
//     fetched across one implies the live tuple no longer equals the captured
//     one, and this check fires. Recycled addresses cannot forge a match: the
//     captured tuple itself keeps the old objects alive, so no later reset can
//     allocate at a captured address.
func (s *Server) writeCapacityResponse(w http.ResponseWriter, result capacityLoadResult, response any) {
	if !result.identity.stillCurrent() {
		// The kubeconfig context switched while this response was being
		// assembled. Its sources describe a cluster that is no longer connected;
		// serving the mixture would be worse than serving nothing.
		s.writeError(w, http.StatusServiceUnavailable, "Cluster context changed while the capacity response was being assembled")
		return
	}
	s.writeJSON(w, response)
}

func (s *Server) handleCapacityOverview(w http.ResponseWriter, r *http.Request) {
	identity := currentCapacityClusterIdentity()
	if !s.requireConnected(w) {
		return
	}
	result, ok := s.loadCapacityModel(w, r, identity, true)
	if !ok {
		return
	}
	response := capacityapi.NewOverviewResponse(result.meta.GeneratedAt)
	response.ResponseMeta = result.meta
	response.State = result.state
	if result.model == nil && (result.state == capacityapi.IntegrationNotDetected || result.state == capacityapi.IntegrationDenied) {
		// Karpenter absent — or present but its NodePools denied — is not the
		// end of capacity: the node fleet is visible (gated on list nodes), so
		// the Overview still carries groups, managers, and the cluster ledger
		// from nodes, pods, and the autoscaler status ConfigMap. Only the
		// Overview widens — Demand/Pools/Activity stay Karpenter-scoped, and
		// state keeps its frozen wire meaning (the Karpenter integration).
		s.loadKarpenterlessCapacityModel(r, &result)
		response.ResponseMeta = result.meta
	}
	if result.model == nil {
		s.writeCapacityResponse(w, result, response)
		return
	}
	issueProjection := s.capacityIssuesForRequest(r)
	issueProjection.attachPools(result.model)

	// Every Karpenter-scoped aggregate below is gated on NodePools actually
	// having been listed. On the denied / not-detected / syncing paths the model
	// is built from nodes and pods alone, so poolCount, the pooled scheduling
	// ledger, and "nodes outside a pool" would each describe a Karpenter fleet
	// nobody read — 0 pools, an exact-empty ledger, and every node unpooled.
	nodePoolsObserved := capacityCoverageObserved(result.meta.Coverage[capacityapi.CoverageNodePools])
	if nodePoolsObserved {
		poolCount := len(result.model.Pools)
		response.Summary.PoolCount = &poolCount
	}
	if capacityCoverageObserved(result.meta.Coverage[capacityapi.CoveragePods]) {
		pending := result.model.PendingPodCount
		response.Summary.PendingPodCount = &pending
		certainty := capacityapi.CertaintyLowerBound
		podCoverage := result.meta.Coverage[capacityapi.CoveragePods]
		if podCoverage.Status == capacityapi.CoverageAvailable && podCoverage.Scope == capacityapi.CoverageScopeCluster {
			certainty = capacityapi.CertaintyExact
		}
		accounting := capacitymodel.AccountResources(nil, result.snapshot.Pods)
		demand := capacitymodel.QuantityObservation(accounting.PendingRequests, certainty, capacityapi.GranularityAggregateNotBinpacked, result.meta.GeneratedAt, "pods.spec.resources")
		response.Summary.AggregateDemand = &demand
		response.Summary.Actions = append(response.Summary.Actions, capacityDemandActions(capacityOverviewDemandGroups(result))...)
	}
	if capacityCoverageObserved(result.meta.Coverage[capacityapi.CoverageNodeClaims]) {
		claimCount := len(result.snapshot.NodeClaims)
		response.Summary.ClaimCount = &claimCount
		orphaned := result.model.OrphanedClaimCount
		response.Summary.OrphanedClaimCount = &orphaned
	}
	if nodePoolsObserved {
		response.Summary.Scheduling = result.model.Scheduling
	}
	response.Summary.ClaimStages = result.model.ClaimStages
	if capacityCoverageObserved(result.meta.Coverage[capacityapi.CoverageNodes]) {
		nodeCount := len(result.snapshot.Nodes)
		response.Summary.NodeCount = &nodeCount
		if nodePoolsObserved {
			unpooled := result.model.UnpooledNodeCount
			response.Summary.UnpooledNodeCount = &unpooled
		}
	}
	s.attachCapacityGroups(r, result, &response)
	response.Summary.Actions = append(response.Summary.Actions, capacityActions(*result.model, issueProjection.available)...)
	summaries := result.model.Summaries()
	if len(summaries) > capacityOverviewPoolLimit {
		response.PoolsTruncated = true
		summaries = summaries[:capacityOverviewPoolLimit]
	}
	response.Pools = summaries
	s.writeCapacityResponse(w, result, response)
}

func (s *Server) handleCapacityPools(w http.ResponseWriter, r *http.Request) {
	identity := currentCapacityClusterIdentity()
	if !s.requireConnected(w) {
		return
	}
	pageRequest, err := parseCapacityPage(r.URL.Query(), capacityPageOptions{
		Scope:          "pools",
		ClusterContext: identity.activeClusterContext,
		DefaultLimit:   capacityDefaultPageLimit,
		MaxLimit:       capacityMaxPageLimit,
	})
	if err != nil {
		s.writeCapacityPageError(w, err)
		return
	}
	result, ok := s.loadCapacityModel(w, r, identity, false)
	if !ok {
		return
	}
	response := capacityapi.NewPoolListResponse(result.meta.GeneratedAt)
	response.ResponseMeta = result.meta
	response.State = result.state
	if result.model == nil {
		s.writeCapacityResponse(w, result, response)
		return
	}
	s.capacityIssuesForRequest(r).attachPools(result.model)

	items := result.model.Summaries()
	page, err := paginateCapacityKeyset(items, pageRequest, func(item capacityapi.PoolSummary) string {
		return item.Resource.Ref.Name
	})
	if err != nil {
		s.writeCapacityPageError(w, err)
		return
	}
	response.Items = page.items
	response.Page = capacityapi.PageInfo{HasMore: page.hasMore, NextCursor: page.nextCursor}
	s.writeCapacityResponse(w, result, response)
}

func (s *Server) handleCapacityPool(w http.ResponseWriter, r *http.Request) {
	identity := currentCapacityClusterIdentity()
	if !s.requireConnected(w) {
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		s.writeError(w, http.StatusBadRequest, "pool name is required")
		return
	}
	result, ok := s.loadCapacityModel(w, r, identity, false)
	if !ok {
		return
	}
	response := capacityapi.NewPoolDetailResponse(result.meta.GeneratedAt)
	response.ResponseMeta = result.meta
	response.State = result.state
	if result.model == nil {
		s.writeCapacityResponse(w, result, response)
		return
	}
	s.capacityIssuesForRequest(r).attachPools(result.model)
	if result.snapshot != nil {
		capacitymodel.AttachPendingEligibilityForPool(result.model, *result.snapshot, name)
	}
	pool, found := result.model.Pool(name)
	if !found {
		s.writeError(w, http.StatusNotFound, "NodePool not found")
		return
	}
	response.Pool = &pool.Observation
	s.writeCapacityResponse(w, result, response)
}

func (s *Server) handleCapacityPoolMembers(w http.ResponseWriter, r *http.Request) {
	identity := currentCapacityClusterIdentity()
	if !s.requireConnected(w) {
		return
	}
	name := chi.URLParam(r, "name")
	memberType := capacityapi.MemberType(r.URL.Query().Get("type"))
	if memberType != capacityapi.MemberNode && memberType != capacityapi.MemberClaim && memberType != capacityapi.MemberWorkload {
		s.writeError(w, http.StatusBadRequest, "type must be one of node, claim, or workload")
		return
	}
	filters := url.Values{"type": {string(memberType)}}
	pageRequest, err := parseCapacityPage(r.URL.Query(), capacityPageOptions{
		Scope:          "pool-members/" + name + "/" + string(memberType),
		Filters:        filters,
		ClusterContext: identity.activeClusterContext,
		DefaultLimit:   capacityDefaultPageLimit,
		MaxLimit:       capacityMaxPageLimit,
	})
	if err != nil {
		s.writeCapacityPageError(w, err)
		return
	}
	result, ok := s.loadCapacityModel(w, r, identity, false)
	if !ok {
		return
	}
	response := capacityapi.NewMemberListResponse(result.meta.GeneratedAt)
	response.ResponseMeta = result.meta
	response.State = result.state
	response.Type = memberType
	if result.model == nil {
		s.writeCapacityResponse(w, result, response)
		return
	}
	pool, found := result.model.Pool(name)
	if !found {
		s.writeError(w, http.StatusNotFound, "NodePool not found")
		return
	}
	response.Pool = pool.Observation.Resource
	var members []capacityapi.PoolMember
	switch memberType {
	case capacityapi.MemberNode:
		members = pool.Nodes
	case capacityapi.MemberClaim:
		members = pool.Claims
	case capacityapi.MemberWorkload:
		members = pool.Workloads
	}
	page, err := paginateCapacityKeyset(members, pageRequest, capacityMemberKey)
	if err != nil {
		s.writeCapacityPageError(w, err)
		return
	}
	response.Items = page.items
	response.Page = capacityapi.PageInfo{HasMore: page.hasMore, NextCursor: page.nextCursor}
	s.writeCapacityResponse(w, result, response)
}

// requireNodeVisibility is the first gate on every Capacity route: the whole
// surface is built on the node fleet, so a caller who cannot list Nodes
// cluster-wide has nothing honest to see. Karpenter access layers the Karpenter
// screens on top of this, never around it. (No-auth/local callers pass —
// canRead returns true there.)
func (s *Server) requireNodeVisibility(w http.ResponseWriter, r *http.Request) bool {
	if s.canRead(r, "", "nodes", "", "list") {
		return true
	}
	s.writeError(w, http.StatusForbidden, "Capacity requires cluster-level node visibility (list nodes)")
	return false
}

// capacityNodePoolDenialMessage keeps the 403 honest about WHICH gate closed.
// A failed informer start is not an RBAC refusal of the caller — telling an
// operator whose own permissions are fine to go fix their RBAC sends them to
// the wrong place.
func capacityNodePoolDenialMessage(reasonCode string) string {
	if reasonCode == "nodepools_watch_denied" {
		return "NodePools could not be watched (the CRD exists, but starting its watch failed — check the Radar service account's watch permission on nodepools)"
	}
	return "no access to NodePools (cluster-scoped resource requires explicit RBAC)"
}

// loadCapacityModel loads the shared capacity model behind the node-visibility
// gate. softKarpenterDenial is set only by the Overview: when true, a
// Karpenter-denied identity gets the cluster-only shape (state denied, NodePools
// coverage denied, model nil for the caller to fill from nodes/pods) instead of
// a 403. Every Karpenter-specific route passes false and fails closed.
// identity is the caller's snapshot, taken as the first statement of the
// request path — the loader never takes its own, which would sit downstream of
// the handler's cursor parsing and authorization.
func (s *Server) loadCapacityModel(w http.ResponseWriter, r *http.Request, identity capacityClusterIdentity, softKarpenterDenial bool) (capacityLoadResult, bool) {
	now := time.Now().UTC()
	result := capacityLoadResult{meta: newCapacityResponseMeta(now, identity), identity: identity}
	if !s.requireNodeVisibility(w, r) {
		return capacityLoadResult{}, false
	}
	capability := s.karpenterCapability(r)
	result.state = capability.State
	if capability.State == capacityapi.IntegrationDenied {
		if !softKarpenterDenial {
			s.writeError(w, http.StatusForbidden, capacityNodePoolDenialMessage(capability.ReasonCode))
			return capacityLoadResult{}, false
		}
		result.meta.Coverage[capacityapi.CoverageNodePools] = deniedCoverage(capability.ReasonCode, []string{"summary", "pools"})
		return result, true
	}
	if capability.State == capacityapi.IntegrationNotDetected || capability.State == capacityapi.IntegrationSyncing {
		result.meta.Coverage[capacityapi.CoverageNodePools] = sourceCoverageForState(capability.State, capability.ReasonCode)
		return result, true
	}

	discovery := k8s.GetResourceDiscovery()
	dynamicCache := k8s.GetDynamicResourceCache()
	if discovery == nil || dynamicCache == nil {
		result.meta.Coverage[capacityapi.CoverageNodePools] = unavailableCoverage("nodepool_cache_unavailable", []string{"summary", "pools"})
		return result, true
	}
	nodePoolGVR, found := discovery.GetGVRWithGroup(karpenter.NodePoolKind, karpenter.Group)
	if !found {
		result.state = capacityapi.IntegrationNotDetected
		result.meta.Coverage[capacityapi.CoverageNodePools] = sourceCoverageForState(result.state, "")
		return result, true
	}
	result.meta.Provider.APIVersionsByKind[karpenter.Group+"/"+karpenter.NodePoolKind] = []string{nodePoolGVR.Version}
	if !dynamicCache.IsSynced(nodePoolGVR) {
		result.state = capacityapi.IntegrationSyncing
		result.meta.Coverage[capacityapi.CoverageNodePools] = sourceCoverageForState(result.state, "nodepools_syncing")
		return result, true
	}
	nodePools, err := dynamicCache.List(nodePoolGVR, "")
	if err != nil {
		log.Printf("[capacity] Failed to list NodePools: %v", err)
		result.meta.Coverage[capacityapi.CoverageNodePools] = errorCoverage("nodepools_list_failed", []string{"summary", "pools"})
		return result, true
	}
	result.state = capacityapi.IntegrationAvailable
	result.meta.Coverage[capacityapi.CoverageNodePools] = availableClusterCoverage(now, len(nodePools), []string{"summary", "pools"})

	nodeClaims := s.loadCapacityNodeClaims(r, discovery, dynamicCache, &result.meta, now)
	nodeClasses := s.loadCapacityNodeClasses(r, discovery, dynamicCache, nodePools, &result.meta, now)
	nodes := s.loadCapacityNodes(r, &result.meta, now)
	pods := s.loadCapacityPods(r, &result.meta, now)
	nodeUsage := s.loadCapacityNodeUsage(r, &result.meta)
	applyCapacityPoolAttributionCoverage(result.meta.Coverage)
	result.meta.Provider = capacityProvider(result.meta.Provider, nodePools, nodeClaims, nodeClasses, result.meta.Coverage)
	resourceCache := k8s.GetResourceCache()
	ownerResolutionAllowed, workloadAttributionPartial := capacityOwnerResolutionPermissions(pods, func(group, resource, namespace string) bool {
		return s.canRead(r, group, resource, namespace, "list") && capacityCacheCoversNamespace(resourceCache, resource, namespace)
	})
	if workloadAttributionPartial {
		coverage := result.meta.Coverage[capacityapi.CoverageWorkloads]
		if capacityCoverageObserved(coverage) {
			coverage.Status = capacityapi.CoveragePartial
			coverage.ReasonCode = "workload_owner_visibility_partial"
			coverage.ImpactFields = []string{"workloads.owner", "workloads.topScheduled"}
			result.meta.Coverage[capacityapi.CoverageWorkloads] = coverage
		}
	}

	snapshot := capacitymodel.Snapshot{
		GeneratedAt: now,
		NodePools:   nodePools,
		NodeClaims:  nodeClaims,
		NodeClasses: nodeClasses,
		Nodes:       nodes,
		Pods:        pods,
		NodeUsage:   nodeUsage,
		Coverage:    result.meta.Coverage,
		ResolvePodOwner: func(pod *corev1.Pod) *subject.Ref {
			allowed, needsResolution := ownerResolutionAllowed[pod]
			if !needsResolution || !allowed {
				return nil
			}
			// Resolve against the SAME cache the permission decision above used —
			// re-fetching the global here could hand the closure a different
			// cluster's cache after a mid-request context switch.
			owner := k8s.TopOwnerForPod(resourceCache, pod)
			if owner == nil {
				return nil
			}
			return &subject.Ref{Group: owner.Group, Kind: owner.Kind, Namespace: pod.Namespace, Name: owner.Name}
		},
	}
	model := capacitymodel.Build(snapshot)
	if workloadAttributionPartial {
		for i := range model.Pools {
			model.Pools[i].Observation.Coverage[capacityapi.CoverageWorkloads] = result.meta.Coverage[capacityapi.CoverageWorkloads]
		}
	}
	result.model = &model
	result.snapshot = &snapshot
	return result, true
}

// capacityOverviewDemandGroups builds the Overview's demand groups and applies
// pool refinement under the SAME condition the Demand endpoint uses — Karpenter
// observed. Splitting that condition is what let one Karpenter cluster with
// zero NodePools read as "blocked" on Demand and scheduler-level on Overview.
// With Karpenter listable but empty, "no NodePool can take this demand" is
// literally true and actionable; without Karpenter there is no pool contract to
// evaluate against, so states stay at the scheduler-verdict level.
func capacityOverviewDemandGroups(result capacityLoadResult) []capacitymodel.DemandGroupModel {
	groups := capacitymodel.BuildDemandGroupModels(capacitymodel.DemandInput{
		GeneratedAt:     result.meta.GeneratedAt,
		Pods:            result.snapshot.Pods,
		ResolvePodOwner: result.snapshot.ResolvePodOwner,
	})
	if capacityCoverageObserved(result.meta.Coverage[capacityapi.CoverageNodePools]) {
		capacitymodel.ClassifyDemandGroupModels(groups, capacityDemandPoolInputs(result))
	}
	return groups
}

func capacityOwnerResolutionPermissions(pods []*corev1.Pod, canList func(group, resource, namespace string) bool) (map[*corev1.Pod]bool, bool) {
	result := map[*corev1.Pod]bool{}
	permissionByScope := map[string]bool{}
	partial := false
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		group, resource := "", ""
		for _, owner := range pod.OwnerReferences {
			if owner.Controller == nil || !*owner.Controller {
				continue
			}
			switch owner.Kind {
			case "ReplicaSet":
				group, resource = "apps", "replicasets"
			case "Job":
				group, resource = "batch", "jobs"
			}
			break
		}
		if resource == "" {
			continue
		}
		key := pod.Namespace + "\x00" + group + "\x00" + resource
		allowed, checked := permissionByScope[key]
		if !checked {
			allowed = canList(group, resource, pod.Namespace)
			permissionByScope[key] = allowed
		}
		result[pod] = allowed
		partial = partial || !allowed
	}
	return result, partial
}

func (s *Server) loadCapacityNodeClaims(r *http.Request, discovery *k8s.ResourceDiscovery, cache *k8s.DynamicResourceCache, meta *capacityapi.ResponseMeta, now time.Time) []*unstructured.Unstructured {
	if !s.canRead(r, karpenter.Group, "nodeclaims", "", "list") {
		meta.Coverage[capacityapi.CoverageNodeClaims] = deniedCoverage("nodeclaims_list_denied", []string{"claimCount", "inFlightCapacity", "claims"})
		return nil
	}
	gvr, found := discovery.GetGVRWithGroup(karpenter.NodeClaimKind, karpenter.Group)
	if !found {
		if discovery.GroupHadPartialDiscovery(karpenter.Group) {
			coverage := capacityapi.NewSourceCoverage(capacityapi.CoveragePartial, capacityapi.CoverageScopeCluster)
			coverage.ReasonCode = "nodeclaims_discovery_partial"
			coverage.ImpactFields = []string{"claimCount", "inFlightCapacity", "claims"}
			meta.Coverage[capacityapi.CoverageNodeClaims] = coverage
			return nil
		}
		meta.Coverage[capacityapi.CoverageNodeClaims] = unavailableCoverage("nodeclaims_not_discovered", []string{"claimCount", "inFlightCapacity", "claims"})
		return nil
	}
	meta.Provider.APIVersionsByKind[karpenter.Group+"/"+karpenter.NodeClaimKind] = []string{gvr.Version}
	if err := cache.EnsureWatching(gvr); err != nil {
		meta.Coverage[capacityapi.CoverageNodeClaims] = unavailableCoverage("nodeclaim_cache_unavailable", []string{"claimCount", "inFlightCapacity", "claims"})
		return nil
	}
	if !cache.IsSynced(gvr) {
		meta.Coverage[capacityapi.CoverageNodeClaims] = syncingCoverage("nodeclaims_syncing", []string{"claimCount", "inFlightCapacity", "claims"})
		return nil
	}
	claims, err := cache.List(gvr, "")
	if err != nil {
		log.Printf("[capacity] Failed to list NodeClaims: %v", err)
		meta.Coverage[capacityapi.CoverageNodeClaims] = errorCoverage("nodeclaims_list_failed", []string{"claimCount", "inFlightCapacity", "claims"})
		return nil
	}
	meta.Coverage[capacityapi.CoverageNodeClaims] = availableClusterCoverage(now, len(claims), []string{"claimCount", "inFlightCapacity", "claims"})
	return claims
}

func (s *Server) loadCapacityNodeClasses(r *http.Request, discovery *k8s.ResourceDiscovery, cache *k8s.DynamicResourceCache, pools []*unstructured.Unstructured, meta *capacityapi.ResponseMeta, now time.Time) map[string]*unstructured.Unstructured {
	type reference struct{ group, kind, name string }
	referencesByKey := map[string]reference{}
	for _, pool := range pools {
		if ref, ok := karpenter.NodeClassRefForNodePool(pool); ok {
			key := capacitymodel.NodeClassLookupKey(ref.Group, ref.Kind, ref.Name)
			referencesByKey[key] = reference{group: ref.Group, kind: ref.Kind, name: ref.Name}
		}
	}
	if len(referencesByKey) == 0 {
		meta.Coverage[capacityapi.CoverageNodeClasses] = availableClusterCoverage(now, 0, []string{"nodeClass"})
		return map[string]*unstructured.Unstructured{}
	}

	keys := make([]string, 0, len(referencesByKey))
	for key := range referencesByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := map[string]*unstructured.Unstructured{}
	denied, unavailable, syncing, failed := 0, 0, 0, 0
	notDiscovered := 0
	partialDiscovery := 0
	kinds := map[string]capacityapi.APIKind{}
	for _, key := range keys {
		ref := referencesByKey[key]
		apiResource, found := discovery.GetResourceWithGroup(ref.kind, ref.group)
		if !found {
			unavailable++
			if discovery.GroupHadPartialDiscovery(ref.group) {
				partialDiscovery++
				continue
			}
			notDiscovered++
			continue
		}
		gvr := schema.GroupVersionResource{Group: apiResource.Group, Version: apiResource.Version, Resource: apiResource.Name}
		if !s.canRead(r, ref.group, apiResource.Name, "", "list") {
			denied++
			continue
		}
		kindKey := ref.group + "/" + ref.kind
		kind := kinds[kindKey]
		kind.Group, kind.Kind, kind.Resource = ref.group, ref.kind, apiResource.Name
		if !capacityContainsString(kind.Versions, apiResource.Version) {
			kind.Versions = append(kind.Versions, apiResource.Version)
		}
		kinds[kindKey] = kind
		if err := cache.EnsureWatching(gvr); err != nil {
			unavailable++
			continue
		}
		if !cache.IsSynced(gvr) {
			syncing++
			continue
		}
		resource, err := cache.Get(gvr, "", ref.name)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			failed++
			continue
		}
		result[key] = resource
	}
	for _, kind := range kinds {
		sort.Strings(kind.Versions)
		meta.Provider.NodeClassKinds = append(meta.Provider.NodeClassKinds, kind)
		meta.Provider.APIVersionsByKind[kind.Group+"/"+kind.Kind] = append([]string{}, kind.Versions...)
	}
	sort.Slice(meta.Provider.NodeClassKinds, func(i, j int) bool {
		left, right := meta.Provider.NodeClassKinds[i], meta.Provider.NodeClassKinds[j]
		return left.Group+"/"+left.Kind < right.Group+"/"+right.Kind
	})
	coverage := availableClusterCoverage(now, len(result), []string{"nodeClass", "poolReadiness"})
	if denied+unavailable+syncing+failed > 0 {
		coverage.Status = capacityapi.CoveragePartial
		switch {
		case denied == len(keys):
			coverage.Status, coverage.ReasonCode = capacityapi.CoverageDenied, "nodeclasses_list_denied"
		case syncing == len(keys):
			coverage.Status, coverage.ReasonCode = capacityapi.CoverageSyncing, "nodeclasses_syncing"
		case notDiscovered == len(keys):
			coverage.Status, coverage.ReasonCode = capacityapi.CoverageUnavailable, "nodeclass_kinds_not_discovered"
		case partialDiscovery > 0:
			coverage.ReasonCode = "nodeclasses_discovery_partial"
		case failed > 0:
			coverage.ReasonCode = "nodeclasses_partial_error"
		default:
			coverage.ReasonCode = "nodeclasses_partial"
		}
	}
	meta.Coverage[capacityapi.CoverageNodeClasses] = coverage
	return result
}

func (s *Server) loadCapacityNodes(r *http.Request, meta *capacityapi.ResponseMeta, now time.Time) []*corev1.Node {
	if !s.canRead(r, "", "nodes", "", "list") {
		meta.Coverage[capacityapi.CoverageNodes] = deniedCoverage("nodes_list_denied", []string{"nodes", "allocatable", "composition", "actualUsage"})
		return nil
	}
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Nodes() == nil {
		meta.Coverage[capacityapi.CoverageNodes] = unavailableCoverage("node_cache_unavailable", []string{"nodes", "allocatable", "composition", "actualUsage"})
		return nil
	}
	nodes, err := cache.Nodes().List(labels.Everything())
	if err != nil {
		log.Printf("[capacity] Failed to list Nodes: %v", err)
		meta.Coverage[capacityapi.CoverageNodes] = errorCoverage("nodes_list_failed", []string{"nodes", "allocatable", "composition", "actualUsage"})
		return nil
	}
	meta.Coverage[capacityapi.CoverageNodes] = availableClusterCoverage(now, len(nodes), []string{"nodes", "allocatable", "composition", "actualUsage"})
	return nodes
}

func (s *Server) loadCapacityPods(r *http.Request, meta *capacityapi.ResponseMeta, now time.Time) []*corev1.Pod {
	baseNamespaces := s.capacityNamespacesForUser(r)
	namespaces := s.capacityNamespacesForSource(r, baseNamespaces, "", "pods")
	explicit := parseNamespaces(r.URL.Query()) != nil || k8s.ForceNamespaceScope
	if noNamespaceAccess(namespaces) {
		meta.Coverage[capacityapi.CoveragePods] = deniedCoverage("pods_list_denied", []string{"scheduledRequests", "aggregateDemand", "workloads", "summary.actions", "demand.summary"})
		meta.Coverage[capacityapi.CoverageWorkloads] = meta.Coverage[capacityapi.CoveragePods]
		return nil
	}
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Pods() == nil {
		meta.Coverage[capacityapi.CoveragePods] = unavailableCoverage("pod_cache_unavailable", []string{"scheduledRequests", "aggregateDemand", "workloads", "summary.actions", "demand.summary"})
		meta.Coverage[capacityapi.CoverageWorkloads] = meta.Coverage[capacityapi.CoveragePods]
		return nil
	}
	sourceNamespaces := namespaces
	cacheNamespaces := capacityNamespacesWithinCache(cache, "pods", sourceNamespaces)
	namespaces = cacheNamespaces.namespaces
	if cacheNamespaces.unavailable {
		coverage := unavailableCoverage("pod_cache_scope_unavailable", []string{"scheduledRequests", "aggregateDemand", "workloads", "summary.actions", "demand.summary"})
		if explicit || cacheNamespaces.limited && sourceNamespaces == nil {
			coverage.Scope = capacityapi.CoverageScopeExplicitNamespaces
		} else if sourceNamespaces != nil {
			coverage.Scope = capacityapi.CoverageScopeAllAuthorizedNamespaces
		}
		coverage.Namespaces = append([]string{}, namespaces...)
		meta.Coverage[capacityapi.CoveragePods] = coverage
		meta.Coverage[capacityapi.CoverageWorkloads] = coverage
		return nil
	}
	pods := listPodsScoped(cache.Pods(), namespaces)
	scope := capacityapi.CoverageScopeCluster
	status := capacityapi.CoverageAvailable
	if namespaces != nil || cacheNamespaces.limited {
		if explicit || cacheNamespaces.limited && sourceNamespaces == nil {
			scope = capacityapi.CoverageScopeExplicitNamespaces
		} else {
			scope = capacityapi.CoverageScopeAllAuthorizedNamespaces
		}
		if sourceNamespaces != nil && (baseNamespaces == nil || len(sourceNamespaces) < len(baseNamespaces)) {
			status = capacityapi.CoveragePartial
		}
	}
	if cacheNamespaces.partial {
		status = capacityapi.CoveragePartial
	}
	coverage := capacityapi.NewSourceCoverage(status, scope)
	if cacheNamespaces.partial {
		coverage.ReasonCode = "pod_cache_scope_partial"
	}
	coverage.Namespaces = append([]string{}, namespaces...)
	coverage.ObservedAt = &now
	count := len(pods)
	coverage.ItemCount = &count
	coverage.ImpactFields = []string{"scheduledRequests", "aggregateDemand", "workloads", "summary.actions", "demand.summary"}
	meta.Coverage[capacityapi.CoveragePods] = coverage
	meta.Coverage[capacityapi.CoverageWorkloads] = coverage
	return pods
}

func (s *Server) loadCapacityNodeUsage(r *http.Request, meta *capacityapi.ResponseMeta) map[string]capacitymodel.NodeUsageSample {
	if !s.canRead(r, "", "nodes", "", "list") || !s.canRead(r, "metrics.k8s.io", "nodes", "", "list") {
		meta.Coverage[capacityapi.CoverageNodeMetrics] = deniedCoverage("node_metrics_list_denied", []string{"actualUsage"})
		return map[string]capacitymodel.NodeUsageSample{}
	}
	store := k8s.GetMetricsHistory()
	if store == nil {
		meta.Coverage[capacityapi.CoverageNodeMetrics] = unavailableCoverage("node_metrics_unavailable", []string{"actualUsage"})
		return map[string]capacitymodel.NodeUsageSample{}
	}
	health := store.CollectionHealth().NodeMetrics
	metrics := store.GetAllNodeMetricsLatest()
	result, observedAt := capacityNodeUsageSamples(metrics)
	if observedAt.IsZero() && health.LastSuccess != "" {
		observedAt, _ = time.Parse(time.RFC3339, health.LastSuccess)
	}
	coverage := availableClusterCoverage(observedAt, len(metrics), []string{"actualUsage"})
	if health.LastSuccess == "" {
		coverage.Status = capacityapi.CoverageSyncing
		coverage.ReasonCode = "node_metrics_awaiting_sample"
		coverage.ItemCount = nil
		coverage.ObservedAt = nil
	} else if health.ConsecutiveErrors > 0 {
		coverage.Status = capacityapi.CoveragePartial
		coverage.ReasonCode = "node_metrics_stale"
	}
	meta.Coverage[capacityapi.CoverageNodeMetrics] = coverage
	return result
}

func capacityNodeUsageSamples(metrics []k8s.TopNodeMetrics) (map[string]capacitymodel.NodeUsageSample, time.Time) {
	result := make(map[string]capacitymodel.NodeUsageSample, len(metrics))
	var oldest time.Time
	for _, metric := range metrics {
		result[metric.Name] = capacitymodel.NodeUsageSample{
			Resources:  capacitymodel.MetricsResourceList(metric.CPU, metric.Memory),
			ObservedAt: metric.ObservedAt,
		}
		if oldest.IsZero() || metric.ObservedAt.Before(oldest) {
			oldest = metric.ObservedAt
		}
	}
	return result, oldest
}

// newCapacityResponseMeta stamps the cluster identity from the CAPTURED
// snapshot rather than re-reading the globals: an independent read here could
// land on the far side of a context switch and label cluster-B data with
// cluster-A's name while the final equality check still passed (B == B).
func newCapacityResponseMeta(now time.Time, identity capacityClusterIdentity) capacityapi.ResponseMeta {
	meta := capacityapi.NewResponseMeta(now)
	meta.ClusterContext = capacityapi.ClusterContext{ContextName: identity.activeClusterContext, ClusterName: identity.clusterName}
	for _, source := range capacityapi.CoverageSources {
		scope := capacityapi.CoverageScopeCluster
		if source == capacityapi.CoveragePods || source == capacityapi.CoverageWorkloads {
			scope = capacityapi.CoverageScopeAllAuthorizedNamespaces
		}
		coverage := capacityapi.NewSourceCoverage(capacityapi.CoverageUnavailable, scope)
		coverage.ReasonCode = "not_observed"
		meta.Coverage[source] = coverage
	}
	return meta
}

func capacityProvider(provider capacityapi.Provider, pools, claims []*unstructured.Unstructured, nodeClasses map[string]*unstructured.Unstructured, coverage capacityapi.CoverageBySource) capacityapi.Provider {
	if len(pools) > 0 {
		provider.ControllerMode = capacityapi.ControllerSelfManaged
	}
	staticCapacity := false
	for _, pool := range pools {
		if karpenter.NormalizeNodePoolSpec(pool).Replicas != nil {
			staticCapacity = true
		}
		if ref, ok := karpenter.NodeClassRefForNodePool(pool); ok && ref.Group == "eks.amazonaws.com" {
			provider.ControllerMode = capacityapi.ControllerEKSAuto
		}
	}
	provider.Features.StaticCapacity = &staticCapacity
	if provider.ControllerMode == capacityapi.ControllerUnknown {
		for _, claim := range claims {
			if ref, ok := karpenter.NodeClassRefForNodeClaim(claim); ok && ref.Group == "eks.amazonaws.com" {
				provider.ControllerMode = capacityapi.ControllerEKSAuto
			}
		}
	}
	// Only an actual observation proves metrics support. Denied, syncing, or
	// unavailable means "could not see" — serializing false there would tell
	// consumers the provider doesn't support metrics.
	if status := coverage[capacityapi.CoverageNodeMetrics].Status; status == capacityapi.CoverageAvailable || status == capacityapi.CoveragePartial {
		metrics := true
		provider.Features.Metrics = &metrics
	}
	_ = nodeClasses
	return provider
}

func capacityActions(model capacitymodel.Model, canonicalIssuesAvailable bool) []capacityapi.ActionSummary {
	actions := map[string]*capacityapi.ActionSummary{}
	for _, pool := range model.Pools {
		identity := pool.Observation.Resource
		issueActionCodes := map[string]bool{}
		// A pool-not-ready signal whose cause is the NodeClass signal beside it
		// is a cascade echo, not a second problem — one misconfiguration must
		// not read as two incidents. Standalone pool-unready (a NodeClass-
		// independent reason) keeps its signal; the rule mirrors the pool
		// detail page's issue dedup.
		nodeClassUnready := false
		for _, issue := range pool.Observation.Issues {
			if issue.Reason == issues.ReasonKarpenterNodeClassNotReady {
				nodeClassUnready = true
			}
		}
		if !canonicalIssuesAvailable {
			nodeClass := pool.Observation.NodeClass
			nodeClassRawUnready := nodeClass != nil && nodeClass.Ready != nil && !*nodeClass.Ready
			if pool.Observation.Ready != nil && !*pool.Observation.Ready && !nodeClassRawUnready {
				addCapacityAction(actions, "pool_not_ready", "warning", identity)
			}
			if nodeClassRawUnready {
				addCapacityAction(actions, "nodeclass_not_ready", "warning", identity)
			}
		}
		for _, issue := range pool.Observation.Issues {
			if nodeClassUnready && issue.Reason == issues.ReasonKarpenterNodePoolNotReady && strings.Contains(issue.Message, "NodeClassReady") {
				continue
			}
			if code := capacityActionCodeForIssue(issue.Reason); code != "" && !issueActionCodes[code] {
				addCapacityAction(actions, code, string(issue.Severity), identity)
				issueActionCodes[code] = true
			}
		}
		for _, pressure := range pool.Observation.Ledger.LimitPressure {
			if pressure.OverLimit || (pressure.Percent != nil && *pressure.Percent >= 80) {
				addCapacityAction(actions, "configured_limit_pressure", "warning", identity)
				break
			}
		}
		if pool.Observation.Nodes != nil && pool.Observation.Nodes.Total == 0 {
			addCapacityAction(actions, "pool_without_nodes", "info", identity)
		}
	}
	keys := make([]string, 0, len(actions))
	for key := range actions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]capacityapi.ActionSummary, 0, len(keys))
	for _, key := range keys {
		result = append(result, *actions[key])
	}
	return result
}

func capacityActionCodeForIssue(reason string) string {
	switch reason {
	case issues.ReasonKarpenterNodePoolNotReady:
		return "pool_not_ready"
	case issues.ReasonKarpenterNodeRegistrationUnhealthy:
		// Karpenter keeps this outside the pool's aggregate Ready, and deletes
		// the failing NodeClaims at the registration timeout — the condition is
		// the only durable trace of "nodes are not joining", so Capacity has to
		// carry it or the whole failure mode is invisible here.
		return "node_registration_unhealthy"
	case issues.ReasonKarpenterNodeClassNotReady:
		return "nodeclass_not_ready"
	case issues.ReasonKarpenterNodeClassNotFound:
		return "nodeclass_not_found"
	case issues.ReasonKarpenterNodeClassKindNotInstalled:
		return "nodeclass_kind_not_installed"
	case issues.ReasonKarpenterNodeClaimProvisioningFailed:
		return "nodeclaim_provisioning_failed"
	default:
		return ""
	}
}

func addCapacityAction(actions map[string]*capacityapi.ActionSummary, code, severity string, pool capacityapi.ResourceIdentity) {
	action := actions[code]
	if action == nil {
		value := capacityapi.NewActionSummary()
		value.Code = code
		value.HighestSeverity = severity
		action = &value
		actions[code] = action
	}
	action.Count++
	if len(action.Pools) < capacityActionSubjectLimit {
		action.Pools = append(action.Pools, pool)
	} else {
		action.Truncated = true
	}
}

func capacityMemberKey(member capacityapi.PoolMember) string {
	ref := member.Resource.Ref
	return string(member.Type) + "\x00" + ref.Group + "\x00" + ref.Kind + "\x00" + ref.Namespace + "\x00" + ref.Name
}

func sourceCoverageForState(state capacityapi.IntegrationState, reason string) capacityapi.SourceCoverage {
	status := capacityapi.CoverageSyncing
	if state == capacityapi.IntegrationNotDetected {
		status = capacityapi.CoverageUnavailable
	}
	coverage := capacityapi.NewSourceCoverage(status, capacityapi.CoverageScopeCluster)
	coverage.ReasonCode = reason
	coverage.ImpactFields = []string{"summary", "pools"}
	return coverage
}

func availableClusterCoverage(observedAt time.Time, itemCount int, impact []string) capacityapi.SourceCoverage {
	coverage := capacityapi.NewSourceCoverage(capacityapi.CoverageAvailable, capacityapi.CoverageScopeCluster)
	coverage.ObservedAt = &observedAt
	coverage.ItemCount = &itemCount
	coverage.ImpactFields = append([]string{}, impact...)
	return coverage
}

func deniedCoverage(reason string, impact []string) capacityapi.SourceCoverage {
	coverage := capacityapi.NewSourceCoverage(capacityapi.CoverageDenied, capacityapi.CoverageScopeCluster)
	coverage.ReasonCode = reason
	coverage.ImpactFields = append([]string{}, impact...)
	return coverage
}

func unavailableCoverage(reason string, impact []string) capacityapi.SourceCoverage {
	coverage := capacityapi.NewSourceCoverage(capacityapi.CoverageUnavailable, capacityapi.CoverageScopeCluster)
	coverage.ReasonCode = reason
	coverage.ImpactFields = append([]string{}, impact...)
	return coverage
}

func syncingCoverage(reason string, impact []string) capacityapi.SourceCoverage {
	coverage := capacityapi.NewSourceCoverage(capacityapi.CoverageSyncing, capacityapi.CoverageScopeCluster)
	coverage.ReasonCode = reason
	coverage.ImpactFields = append([]string{}, impact...)
	return coverage
}

func errorCoverage(reason string, impact []string) capacityapi.SourceCoverage {
	coverage := capacityapi.NewSourceCoverage(capacityapi.CoverageError, capacityapi.CoverageScopeCluster)
	coverage.ReasonCode = reason
	coverage.ImpactFields = append([]string{}, impact...)
	return coverage
}

func capacityContainsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func capacityCoverageObserved(coverage capacityapi.SourceCoverage) bool {
	// Partial without an item count means no list actually happened (e.g.
	// discovery skew acknowledged the group but the kind never resolved) —
	// emitting counts for it would fabricate "≥ 0" for an unobserved source.
	return coverage.Status == capacityapi.CoverageAvailable ||
		(coverage.Status == capacityapi.CoveragePartial && coverage.ItemCount != nil)
}

func applyCapacityPoolAttributionCoverage(coverage capacityapi.CoverageBySource) {
	workloads := coverage[capacityapi.CoverageWorkloads]
	if !capacityCoverageObserved(workloads) {
		return
	}
	nodes := coverage[capacityapi.CoverageNodes]
	claims := coverage[capacityapi.CoverageNodeClaims]
	complete := func(source capacityapi.SourceCoverage) bool {
		return source.Status == capacityapi.CoverageAvailable && source.Scope == capacityapi.CoverageScopeCluster
	}
	if complete(nodes) || complete(claims) {
		return
	}
	if capacityCoverageObserved(nodes) || capacityCoverageObserved(claims) {
		workloads.Status = capacityapi.CoveragePartial
		workloads.ReasonCode = "workload_pool_attribution_partial"
		workloads.ImpactFields = []string{"scheduledRequests", "workloads.pool"}
		coverage[capacityapi.CoverageWorkloads] = workloads
		return
	}
	coverage[capacityapi.CoverageWorkloads] = unavailableCoverage(
		"workload_pool_attribution_unavailable",
		[]string{"scheduledRequests", "workloads.pool"},
	)
}
