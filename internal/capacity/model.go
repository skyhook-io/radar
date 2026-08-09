package capacity

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	"github.com/skyhook-io/radar/pkg/subject"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	instanceTypeLabel             = "node.kubernetes.io/instance-type"
	zoneLabel                     = "topology.kubernetes.io/zone"
	architectureLabel             = "kubernetes.io/arch"
	defaultTopWorkloads           = 10
	defaultWorkloadNodeLimit      = 50
	defaultCompositionBucketLimit = 20
)

type NodeUsageSample struct {
	Resources  corev1.ResourceList
	ObservedAt time.Time
}

type Snapshot struct {
	GeneratedAt     time.Time
	NodePools       []*unstructured.Unstructured
	NodeClaims      []*unstructured.Unstructured
	NodeClasses     map[string]*unstructured.Unstructured
	Nodes           []*corev1.Node
	Pods            []*corev1.Pod
	NodeUsage       map[string]NodeUsageSample
	Coverage        capacityapi.CoverageBySource
	ResolvePodOwner func(*corev1.Pod) *subject.Ref
}

type PoolModel struct {
	Observation capacityapi.PoolObservation
	Nodes       []capacityapi.PoolMember
	Claims      []capacityapi.PoolMember
	Workloads   []capacityapi.PoolMember
}

type Model struct {
	GeneratedAt        time.Time
	Pools              []PoolModel
	PendingPodCount    int
	OrphanedClaimCount int
	UnpooledNodeCount  int
	// Scheduling is the cluster-level ledger across Karpenter-pooled nodes
	// only; ClaimStages rolls up lifecycle stages across ALL claims,
	// orphaned ones included, matching the claim count the overview reports.
	Scheduling  *capacityapi.SchedulingCapacity
	ClaimStages *capacityapi.ClaimLifecycleSummary
}

func NodeClassLookupKey(group, kind, name string) string {
	return group + "\x00" + kind + "\x00" + name
}

func Build(snapshot Snapshot) Model {
	if snapshot.GeneratedAt.IsZero() {
		snapshot.GeneratedAt = time.Now().UTC()
	}
	model := Model{GeneratedAt: snapshot.GeneratedAt, Pools: []PoolModel{}}

	poolsByName := make(map[string]*unstructured.Unstructured, len(snapshot.NodePools))
	modelsByName := make(map[string]*PoolModel, len(snapshot.NodePools))
	for _, pool := range snapshot.NodePools {
		if pool == nil || pool.GetName() == "" {
			continue
		}
		poolsByName[pool.GetName()] = pool
		poolModel := PoolModel{
			Observation: basePoolObservation(pool, snapshot),
			Nodes:       []capacityapi.PoolMember{},
			Claims:      []capacityapi.PoolMember{},
			Workloads:   []capacityapi.PoolMember{},
		}
		model.Pools = append(model.Pools, poolModel)
		modelsByName[pool.GetName()] = &model.Pools[len(model.Pools)-1]
	}

	claimsByPool := map[string][]*unstructured.Unstructured{}
	poolForNodeName := map[string]string{}
	poolForProviderID := map[string]string{}
	claimForNodeName := map[string]*unstructured.Unstructured{}
	claimForProviderID := map[string]*unstructured.Unstructured{}
	rejectedNodeNames := map[string]bool{}
	rejectedProviderIDs := map[string]bool{}
	for _, claim := range snapshot.NodeClaims {
		pool, _ := karpenter.ResolveNodePoolForClaim(claim, poolsByName)
		if pool == nil {
			if claim != nil {
				model.OrphanedClaimCount++
				if nodeName := karpenter.ClaimNodeName(claim); nodeName != "" {
					rejectedNodeNames[nodeName] = true
				}
				if providerID := karpenter.ClaimProviderID(claim); providerID != "" {
					rejectedProviderIDs[providerID] = true
				}
			}
			continue
		}
		poolName := pool.GetName()
		claimsByPool[poolName] = append(claimsByPool[poolName], claim)
		if nodeName := karpenter.ClaimNodeName(claim); nodeName != "" {
			poolForNodeName[nodeName] = poolName
			claimForNodeName[nodeName] = claim
		}
		if providerID := karpenter.ClaimProviderID(claim); providerID != "" {
			poolForProviderID[providerID] = poolName
			claimForProviderID[providerID] = claim
		}
	}

	nodesByPool := map[string][]*corev1.Node{}
	poolForNode := make(map[string]string, len(poolForNodeName))
	for nodeName, poolName := range poolForNodeName {
		poolForNode[nodeName] = poolName
	}
	nodeForClaim := map[*unstructured.Unstructured]*corev1.Node{}
	for _, node := range snapshot.Nodes {
		if node == nil || node.Name == "" {
			continue
		}
		poolName := poolForNodeName[node.Name]
		claim := claimForNodeName[node.Name]
		if poolName == "" && node.Spec.ProviderID != "" {
			poolName = poolForProviderID[node.Spec.ProviderID]
			claim = claimForProviderID[node.Spec.ProviderID]
		}
		rejectedClaimLink := rejectedNodeNames[node.Name] || (node.Spec.ProviderID != "" && rejectedProviderIDs[node.Spec.ProviderID])
		if poolName == "" && !rejectedClaimLink {
			poolName = node.Labels[karpenter.NodePoolLabelKey]
		}
		if modelsByName[poolName] == nil {
			model.UnpooledNodeCount++
			continue
		}
		nodesByPool[poolName] = append(nodesByPool[poolName], node)
		poolForNode[node.Name] = poolName
		if claim != nil {
			nodeForClaim[claim] = node
		}
	}

	podsByPool := map[string][]*corev1.Pod{}
	for _, pod := range snapshot.Pods {
		if pod == nil || isTerminal(pod) {
			continue
		}
		if pod.Spec.NodeName == "" {
			// Gated pods are excluded to match aggregate demand: the scheduler
			// (and Karpenter) will not act on them until a controller lifts
			// the gates, so counting them as pending demand misleads.
			if pod.DeletionTimestamp == nil && !isDaemonSetPod(pod) && !isSchedulingGatedPod(pod) {
				model.PendingPodCount++
			}
			continue
		}
		if poolName := poolForNode[pod.Spec.NodeName]; poolName != "" {
			podsByPool[poolName] = append(podsByPool[poolName], pod)
		}
	}

	var pooledNodes []*corev1.Node
	var pooledPods []*corev1.Pod
	var pooledClaims []*unstructured.Unstructured
	for i := range model.Pools {
		poolName := model.Pools[i].Observation.Resource.Ref.Name
		pooledNodes = append(pooledNodes, nodesByPool[poolName]...)
		pooledPods = append(pooledPods, podsByPool[poolName]...)
		pooledClaims = append(pooledClaims, claimsByPool[poolName]...)
		populatePool(&model.Pools[i], claimsByPool[poolName], nodesByPool[poolName], podsByPool[poolName], nodeForClaim, snapshot)
	}
	buildSchedulingAggregate(&model, pooledNodes, pooledPods, pooledClaims, nodeForClaim, snapshot)
	sort.Slice(model.Pools, func(i, j int) bool {
		return poolModelLess(model.Pools[i], model.Pools[j])
	})
	return model
}

// inFlightAccumulator gathers capacity of claims that are neither settled
// (ready/failed/terminating) nor linked to a node yet — capacity Karpenter
// has asked the provider for but the scheduler cannot use.
type inFlightAccumulator struct {
	resources    corev1.ResourceList
	count        int
	withCapacity int
}

func (a *inFlightAccumulator) add(claim *unstructured.Unstructured, stage capacityapi.ClaimStage, nodeForClaim map[*unstructured.Unstructured]*corev1.Node) {
	if stage == capacityapi.ClaimStageReady || stage == capacityapi.ClaimStageFailed || stage == capacityapi.ClaimStageTerminating {
		return
	}
	if karpenter.ClaimNodeName(claim) != "" || nodeForClaim[claim] != nil {
		return
	}
	a.count++
	if capacityResources := karpenter.NodeClaimCapacity(claim); capacityResources != nil {
		a.withCapacity++
		addResources(a.resources, capacityResources)
	}
}

func (a *inFlightAccumulator) observation(claimCertainty capacityapi.Certainty, asOf time.Time) *capacityapi.QuantityObservation {
	if a.count == 0 {
		return nil
	}
	certainty := claimCertainty
	if a.withCapacity == 0 {
		certainty = capacityapi.CertaintyUnknown
	} else if a.withCapacity < a.count && certainty == capacityapi.CertaintyExact {
		certainty = capacityapi.CertaintyLowerBound
	}
	observation := QuantityObservation(a.resources, certainty, capacityapi.GranularityAggregate, asOf, "nodeclaims.status.capacity")
	return &observation
}

// buildSchedulingAggregate mirrors populatePool's ledger semantics at cluster
// scope: requests and allocatable cover Karpenter-pooled nodes only (the bar
// explicitly excludes unpooled nodes), in-flight covers pooled claims, and
// the claim-stage rollup covers every claim — orphans included — so it sums
// to the overview's claim count. The same nil-gates apply: values from an
// unobserved source are absent, never zero.
func buildSchedulingAggregate(model *Model, pooledNodes []*corev1.Node, pooledPods []*corev1.Pod, pooledClaims []*unstructured.Unstructured, nodeForClaim map[*unstructured.Unstructured]*corev1.Node, snapshot Snapshot) {
	nodeCertainty := sourceCertainty(snapshot.Coverage, capacityapi.CoverageNodes)
	claimCertainty := sourceCertainty(snapshot.Coverage, capacityapi.CoverageNodeClaims)

	scheduling := capacityapi.SchedulingCapacity{}
	accounting := AccountResources(pooledNodes, pooledPods)
	if sourceObserved(snapshot.Coverage, capacityapi.CoveragePods) {
		requests := QuantityObservation(accounting.ScheduledRequests, scheduledRequestCertainty(snapshot.Coverage), capacityapi.GranularityAggregate, snapshot.GeneratedAt, "pods.spec.resources")
		scheduling.ScheduledRequests = &requests
		if negative := negativePriorityScheduledRequests(pooledPods); len(negative) > 0 {
			negativeRequests := QuantityObservation(negative, scheduledRequestCertainty(snapshot.Coverage), capacityapi.GranularityAggregate, snapshot.GeneratedAt, "pods.spec.resources", "pods.spec.priority")
			scheduling.NegativePriorityRequests = &negativeRequests
		}
	}
	if sourceObserved(snapshot.Coverage, capacityapi.CoverageNodes) {
		allocatable := QuantityObservation(accounting.Allocatable, nodeCertainty, capacityapi.GranularityAggregate, snapshot.GeneratedAt, "nodes.status.allocatable")
		scheduling.Allocatable = &allocatable
	}
	inFlight := inFlightAccumulator{resources: corev1.ResourceList{}}
	for _, claim := range pooledClaims {
		if claim != nil {
			inFlight.add(claim, claimStage(claim), nodeForClaim)
		}
	}
	scheduling.InFlightCapacity = inFlight.observation(claimCertainty, snapshot.GeneratedAt)
	if scheduling.ScheduledRequests != nil || scheduling.Allocatable != nil || scheduling.InFlightCapacity != nil {
		model.Scheduling = &scheduling
	}

	if sourceObserved(snapshot.Coverage, capacityapi.CoverageNodeClaims) {
		stages := capacityapi.ClaimLifecycleSummary{}
		for _, claim := range snapshot.NodeClaims {
			if claim == nil {
				continue
			}
			stages.Total++
			incrementClaimStage(&stages, claimStage(claim))
		}
		model.ClaimStages = &stages
	}
}

func AttachPendingEligibilityForPool(model *Model, snapshot Snapshot, poolName string) {
	if model == nil || poolName == "" {
		return
	}
	poolModel, found := model.Pool(poolName)
	if !found || poolModel.Observation.Workloads == nil {
		return
	}
	var nodePool *unstructured.Unstructured
	for _, pool := range snapshot.NodePools {
		if pool != nil && pool.GetName() == poolName {
			nodePool = pool
			break
		}
	}
	if nodePool == nil {
		return
	}

	input := DemandPoolInput{
		NodePool:             nodePool,
		ProvisionedKnown:     karpenter.NodePoolStatusResources(nodePool) != nil,
		ObservedMemberShapes: ObservedMemberShapesByPool(snapshot.Nodes, snapshot.NodeClaims)[poolName],
	}
	if poolModel.Observation.NodeClass != nil {
		input.NodeClassReady = poolModel.Observation.NodeClass.Ready
	}
	asOf := snapshot.GeneratedAt
	if asOf.IsZero() {
		asOf = model.GeneratedAt
	}
	groups := BuildDemandGroupModels(DemandInput{
		GeneratedAt:     asOf,
		Pods:            snapshot.Pods,
		ResolvePodOwner: snapshot.ResolvePodOwner,
	})
	workloads := poolModel.Observation.Workloads
	workloads.PendingEligibleGroupCount = 0
	for _, group := range groups {
		if group.Group.State != capacityapi.DemandAwaitingCapacity {
			continue
		}
		if evaluateDemandPool(group.scheduling, group.requests, input).Result != capacityapi.PoolDeclaredCompatible {
			continue
		}
		workloads.PendingEligibleGroupCount++
	}
}

func (m Model) Pool(name string) (*PoolModel, bool) {
	for i := range m.Pools {
		if m.Pools[i].Observation.Resource.Ref.Name == name {
			return &m.Pools[i], true
		}
	}
	return nil, false
}

func (m Model) Summaries() []capacityapi.PoolSummary {
	result := make([]capacityapi.PoolSummary, 0, len(m.Pools))
	for _, pool := range m.Pools {
		summary := capacityapi.NewPoolSummary()
		summary.Resource = pool.Observation.Resource
		summary.Mode = pool.Observation.Mode
		summary.Ready = pool.Observation.Ready
		if pool.Observation.NodeClass != nil {
			ref := pool.Observation.NodeClass.Reference
			summary.NodeClass = &ref
		}
		summary.Ledger = pool.Observation.Ledger
		summary.Claims = pool.Observation.Claims
		summary.Nodes = pool.Observation.Nodes
		summary.IssueCount = len(pool.Observation.Issues)
		summary.FactCount = len(pool.Observation.Facts)
		result = append(result, summary)
	}
	return result
}

func basePoolObservation(pool *unstructured.Unstructured, snapshot Snapshot) capacityapi.PoolObservation {
	observation := capacityapi.NewPoolObservation()
	observation.Resource = identityForUnstructured(pool)
	observation.Generation = pool.GetGeneration()
	createdTimestamp := pool.GetCreationTimestamp()
	if !createdTimestamp.IsZero() {
		created := createdTimestamp.Time
		observation.CreatedAt = &created
	}
	conditions := karpenter.NodePoolConditions(pool)
	observation.Conditions = normalizeConditions(conditions)
	if ready := karpenter.NodePoolReadyCondition(pool); ready != nil && ready.ObservedGeneration > 0 {
		observed := ready.ObservedGeneration
		observation.ObservedGeneration = &observed
	}
	switch karpenter.NodePoolReadiness(pool) {
	case karpenter.ReadinessReady:
		observation.Ready = boolPointer(true)
	case karpenter.ReadinessNotReady:
		observation.Ready = boolPointer(false)
	}

	normalized := karpenter.NormalizeNodePoolSpec(pool)
	if normalized.Replicas != nil {
		observation.Mode = capacityapi.PoolModeStatic
	} else {
		observation.Mode = capacityapi.PoolModeDynamic
	}
	observation.Configuration = poolConfiguration(normalized)
	if fact := acceleratorFact(observation.Configuration.Requirements); fact != nil {
		observation.Facts = append(observation.Facts, *fact)
	}
	observation.Disruption = disruptionPolicy(normalized)
	observation.Coverage = copyCoverage(snapshot.Coverage)

	if len(normalized.Limits) > 0 {
		configured := QuantityObservation(normalized.Limits, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, snapshot.GeneratedAt, "nodepool.spec.limits")
		observation.Ledger.ConfiguredLimit = &configured
	}
	provisionedResources := karpenter.NodePoolStatusResources(pool)
	if provisionedResources != nil {
		provisioned := QuantityObservation(provisionedResources, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, snapshot.GeneratedAt, "nodepool.status.resources")
		observation.Ledger.Provisioned = &provisioned
		observation.Ledger.LimitPressure = limitPressure(provisionedResources, normalized.Limits)
	}
	if len(normalized.Limits) > 0 && provisionedResources != nil {
		headroom := QuantityObservation(subtractLeftResourceLists(normalized.Limits, provisionedResources), capacityapi.CertaintyExact, capacityapi.GranularityAggregate, snapshot.GeneratedAt, "nodepool.spec.limits", "nodepool.status.resources")
		observation.Ledger.LimitHeadroom = &headroom
	}
	for _, pressure := range observation.Ledger.LimitPressure {
		// OverLimit alone must fire too: a zero limit (the documented
		// halt-provisioning idiom) has no percentage yet is maximally binding.
		if pressure.OverLimit || (pressure.Percent != nil && *pressure.Percent >= 80) {
			summary := pressure.Resource + " provisioned capacity is near its configured limit"
			if pressure.OverLimit {
				summary = pressure.Resource + " provisioned capacity exceeds its configured limit"
			}
			observation.Facts = append(observation.Facts, capacityapi.PostureFact{
				Code:        "configured_limit_pressure",
				Summary:     summary,
				SourcePaths: []string{"spec.limits." + pressure.Resource, "status.resources." + pressure.Resource},
			})
		}
	}

	if normalized.NodeClassRef != nil {
		ref := capacityapi.NodeClassReference{
			Group:      normalized.NodeClassRef.Group,
			Kind:       normalized.NodeClassRef.Kind,
			Name:       normalized.NodeClassRef.Name,
			APIVersion: normalized.NodeClassRef.APIVersion,
		}
		nodeClass := capacityapi.NewNodeClassObservation(ref)
		if resource := snapshot.NodeClasses[NodeClassLookupKey(ref.Group, ref.Kind, ref.Name)]; resource != nil {
			identity := identityForUnstructured(resource)
			nodeClass.Resource = &identity
			nodeClass.Reference.APIVersion = resource.GetAPIVersion()
			nodeClass.Conditions = normalizeConditions(karpenter.Conditions(resource))
			switch karpenter.ResourceReadiness(resource) {
			case karpenter.ReadinessReady:
				nodeClass.Ready = boolPointer(true)
			case karpenter.ReadinessNotReady:
				nodeClass.Ready = boolPointer(false)
			}
		}
		observation.NodeClass = &nodeClass
	}
	return observation
}

func populatePool(model *PoolModel, claims []*unstructured.Unstructured, nodes []*corev1.Node, pods []*corev1.Pod, nodeForClaim map[*unstructured.Unstructured]*corev1.Node, snapshot Snapshot) {
	observation := &model.Observation
	nodeCertainty := sourceCertainty(snapshot.Coverage, capacityapi.CoverageNodes)
	podCertainty := sourceCertainty(snapshot.Coverage, capacityapi.CoveragePods)
	claimCertainty := sourceCertainty(snapshot.Coverage, capacityapi.CoverageNodeClaims)
	scheduledCertainty := scheduledRequestCertainty(snapshot.Coverage)

	accounting := AccountResources(nodes, pods)
	// Pod-derived values are only meaningful when Pods were actually observed
	// — otherwise "requests" is an empty vector and allocatable−requests
	// fabricates a fully-free pool. Same for the difference when Nodes are
	// missing: with pods attributed via NodeClaims it would go negative.
	// Absent, never zero, in both directions.
	if sourceObserved(snapshot.Coverage, capacityapi.CoveragePods) {
		requests := QuantityObservation(accounting.ScheduledRequests, scheduledCertainty, capacityapi.GranularityAggregate, snapshot.GeneratedAt, "pods.spec.resources")
		observation.Ledger.ScheduledRequests = &requests
	}
	if sourceObserved(snapshot.Coverage, capacityapi.CoverageNodes) {
		allocatable := QuantityObservation(accounting.Allocatable, nodeCertainty, capacityapi.GranularityAggregate, snapshot.GeneratedAt, "nodes.status.allocatable")
		observation.Ledger.Allocatable = &allocatable
		if sourceObserved(snapshot.Coverage, capacityapi.CoveragePods) {
			unallocatedCertainty := differenceCertainty(nodeCertainty, podCertainty)
			unallocated := QuantityObservation(subtractResourceLists(accounting.Allocatable, accounting.ScheduledRequests), unallocatedCertainty, capacityapi.GranularityAggregateNotBinpacked, snapshot.GeneratedAt, "nodes.status.allocatable", "pods.spec.resources")
			observation.Ledger.AggregateUnallocatedRequests = &unallocated
		}
	}

	if sourceObserved(snapshot.Coverage, capacityapi.CoverageNodeClaims) {
		observation.Claims = &capacityapi.ClaimLifecycleSummary{Total: len(claims)}
	}
	inFlight := inFlightAccumulator{resources: corev1.ResourceList{}}
	for _, claim := range claims {
		if claim == nil {
			continue
		}
		stage := claimStage(claim)
		if observation.Claims != nil {
			incrementClaimStage(observation.Claims, stage)
		}
		inFlight.add(claim, stage, nodeForClaim)
		member := capacityapi.NewClaimMember()
		member.Stage = stage
		member.Conditions = normalizeConditions(karpenter.NodeClaimConditions(claim))
		member.NodeName = karpenter.ClaimNodeName(claim)
		capacityResources := karpenter.NodeClaimCapacity(claim)
		if capacityResources != nil {
			capacityObservation := QuantityObservation(capacityResources, claimCertainty, capacityapi.GranularityAggregate, snapshot.GeneratedAt, "nodeclaims.status.capacity")
			member.Capacity = &capacityObservation
		}
		if node := nodeForClaim[claim]; node != nil {
			identity := identityForNode(node)
			member.Node = &identity
		}
		model.Claims = append(model.Claims, capacityapi.PoolMember{Type: capacityapi.MemberClaim, Resource: identityForUnstructured(claim), Claim: &member})
	}
	observation.Ledger.InFlightCapacity = inFlight.observation(claimCertainty, snapshot.GeneratedAt)

	if sourceObserved(snapshot.Coverage, capacityapi.CoverageNodes) {
		observation.Nodes = &capacityapi.NodeLifecycleSummary{Total: len(nodes)}
		composition := capacityapi.NewPoolComposition()
		observation.Composition = &composition
	}
	podsByNode := map[string][]*corev1.Pod{}
	for _, pod := range pods {
		if pod != nil && pod.Spec.NodeName != "" {
			podsByNode[pod.Spec.NodeName] = append(podsByNode[pod.Spec.NodeName], pod)
		}
	}
	usageResources := corev1.ResourceList{}
	coveredAllocatable := corev1.ResourceList{}
	coveredNodes := 0
	var actualAsOf time.Time
	for _, node := range nodes {
		if node == nil {
			continue
		}
		ready := nodeReady(node)
		if observation.Nodes != nil {
			if ready == nil {
				observation.Nodes.NotReady++
			} else if *ready {
				observation.Nodes.Ready++
			} else {
				observation.Nodes.NotReady++
			}
			if node.Spec.Unschedulable {
				observation.Nodes.Cordoned++
			}
			if node.DeletionTimestamp != nil {
				observation.Nodes.Terminating++
			}
		}

		member := capacityapi.NewNodeMember()
		member.Ready = ready
		member.Cordoned = node.Spec.Unschedulable
		member.Conditions = normalizeNodeConditions(node.Status.Conditions)
		member.InstanceType = node.Labels[instanceTypeLabel]
		member.CapacityType = node.Labels[karpenter.CapacityTypeLabelKey]
		member.Zone = node.Labels[zoneLabel]
		member.Architecture = node.Labels[architectureLabel]
		member.Image = node.Status.NodeInfo.OSImage
		if sourceObserved(snapshot.Coverage, capacityapi.CoveragePods) {
			podCount := len(podsByNode[node.Name])
			member.PodCount = &podCount
			nodeRequests := AccountResources(nil, podsByNode[node.Name]).ScheduledRequests
			nodeRequestObservation := QuantityObservation(nodeRequests, podCertainty, capacityapi.GranularityPerNode, snapshot.GeneratedAt, "pods.spec.resources")
			member.ScheduledRequests = &nodeRequestObservation
		}
		nodeAllocatable := QuantityObservation(node.Status.Allocatable, nodeCertainty, capacityapi.GranularityPerNode, snapshot.GeneratedAt, "node.status.allocatable")
		member.Allocatable = &nodeAllocatable
		if sample, ok := snapshot.NodeUsage[node.Name]; ok {
			coveredNodes++
			addResources(usageResources, sample.Resources)
			addResources(coveredAllocatable, node.Status.Allocatable)
			if actualAsOf.IsZero() || sample.ObservedAt.Before(actualAsOf) {
				actualAsOf = sample.ObservedAt
			}
			usage := capacityapi.NewUsageObservation(sample.ObservedAt)
			usage.CoveredNodes = 1
			usage.TotalNodes = 1
			usage.Quantity = QuantityObservation(sample.Resources, capacityapi.CertaintyExact, capacityapi.GranularityPerNode, sample.ObservedAt, "metrics.k8s.io/nodes")
			usage.CoveredAllocatable = QuantityObservation(node.Status.Allocatable, nodeCertainty, capacityapi.GranularityPerNode, sample.ObservedAt, "node.status.allocatable")
			usage.Utilization = utilization(sample.Resources, node.Status.Allocatable)
			member.ActualUsage = &usage
		}
		model.Nodes = append(model.Nodes, capacityapi.PoolMember{Type: capacityapi.MemberNode, Resource: identityForNode(node), Node: &member})
		if observation.Composition != nil {
			addComposition(observation.Composition, node)
		}
	}
	if coveredNodes > 0 {
		usage := capacityapi.NewUsageObservation(actualAsOf)
		usage.CoveredNodes = coveredNodes
		usage.TotalNodes = len(nodes)
		// Usage summed over a sampled subset of the pool's nodes is a lower
		// bound of pool usage, not an exact reading — 3 sampled nodes out of
		// 100 must not present as "=".
		usageCertainty := capacityapi.CertaintyExact
		if coveredNodes < len(nodes) || nodeCertainty != capacityapi.CertaintyExact {
			usageCertainty = capacityapi.CertaintyLowerBound
		}
		usage.Quantity = QuantityObservation(usageResources, usageCertainty, capacityapi.GranularityAggregate, actualAsOf, "metrics.k8s.io/nodes")
		usage.CoveredAllocatable = QuantityObservation(coveredAllocatable, nodeCertainty, capacityapi.GranularityAggregate, actualAsOf, "node.status.allocatable")
		usage.Utilization = utilization(usageResources, coveredAllocatable)
		observation.Ledger.ActualUsage = &usage
	}

	model.Workloads = buildWorkloadMembers(pods, snapshot)
	if sourceObserved(snapshot.Coverage, capacityapi.CoverageWorkloads) {
		workloads := capacityapi.NewWorkloadSummary()
		observation.Workloads = &workloads
	}
	if observation.Workloads != nil {
		observation.Workloads.ScheduledPodCount = len(pods)
		observation.Workloads.WorkloadCount = len(model.Workloads)
		observation.Workloads.TopScheduledMeta.Total = len(model.Workloads)
	}
	limit := len(model.Workloads)
	if limit > defaultTopWorkloads {
		limit = defaultTopWorkloads
		if observation.Workloads != nil {
			observation.Workloads.TopScheduledMeta.Truncated = true
		}
	}
	if observation.Workloads != nil {
		observation.Workloads.TopScheduledMeta.Returned = limit
		for _, item := range model.Workloads[:limit] {
			observation.Workloads.TopScheduled = append(observation.Workloads.TopScheduled, capacityapi.WorkloadAttribution{
				Owner:    item.Workload.Owner,
				PodCount: item.Workload.PodCount,
				Requests: item.Workload.Requests,
			})
		}
	}

	sortMembers(model.Nodes)
	sortMembers(model.Claims)
	sortMembers(model.Workloads)
	if observation.Composition != nil {
		sortComposition(observation.Composition)
	}
}

// acceleratorRequirementKeys are the well-known requirement keys that declare
// a pool provisions accelerator instances: Karpenter's AWS and Azure GPU
// instance selectors, the NVIDIA GPU-operator/NFD label family, and GKE's
// accelerator label. The accelerator fact only ever ECHOES a declared
// requirement — no instance-type name parsing, no inference — so an unknown
// provider simply produces no fact, never a wrong one.
var acceleratorRequirementKeys = map[string]bool{
	"karpenter.k8s.aws/instance-gpu-count":        true,
	"karpenter.k8s.aws/instance-gpu-name":         true,
	"karpenter.k8s.aws/instance-gpu-manufacturer": true,
	"karpenter.azure.com/sku-gpu-count":           true,
	"karpenter.azure.com/sku-gpu-name":            true,
	"karpenter.azure.com/sku-gpu-manufacturer":    true,
	"nvidia.com/gpu.present":                      true,
	"nvidia.com/gpu.product":                      true,
	"nvidia.com/gpu.count":                        true,
	"cloud.google.com/gke-accelerator":            true,
}

// acceleratorInstanceKeys are the requirement keys whose VALUES name instance
// types, families, or categories — recognizable at family-prefix granularity.
var acceleratorInstanceKeys = map[string]bool{
	"node.kubernetes.io/instance-type":    true,
	"karpenter.k8s.aws/instance-family":   true,
	"karpenter.k8s.aws/instance-category": true,
	"karpenter.azure.com/sku-family":      true,
}

// isAcceleratorInstanceValue recognizes accelerator instance FAMILIES by each
// cloud's naming convention — deliberately prefix-level, never a catalogue:
// AWS accelerated families all begin p/g/trn/inf/dl before a digit (a
// convention stable across generations — g6 and p5 matched the day they
// shipped), GCP's accelerator-optimized families are a2/a3/a4/g2, Azure's are
// the N series. A family under a new letter is simply missed — a silent null,
// never a wrong claim.
func isAcceleratorInstanceValue(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	// AWS instance-category values.
	case "g", "p", "inf", "trn", "dl":
		return true
	// Azure sku-family value.
	case "n":
		return true
	}
	for _, prefix := range []string{"p", "g", "trn", "inf", "dl"} {
		if rest, ok := strings.CutPrefix(v, prefix); ok && len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
			return true
		}
	}
	for _, prefix := range []string{"a2-", "a3-", "a4-", "a4x-", "g2-"} {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return strings.HasPrefix(v, "standard_n") &&
		(strings.HasPrefix(v, "standard_nc") || strings.HasPrefix(v, "standard_nd") ||
			strings.HasPrefix(v, "standard_ng") || strings.HasPrefix(v, "standard_nv"))
}

func requirementDeclaresAccelerator(requirement capacityapi.Requirement) bool {
	if acceleratorRequirementKeys[requirement.Key] {
		return true
	}
	if !acceleratorInstanceKeys[requirement.Key] || requirement.Operator != "In" {
		return false
	}
	for _, value := range requirement.Values {
		if isAcceleratorInstanceValue(value) {
			return true
		}
	}
	return false
}

// acceleratorFact names the accelerator a pool is FOR when its declared
// requirements say so. A scale-to-zero or failing GPU pool has no allocatable
// to put the accelerator on the ledger — the declaration is the only truth
// available, and naming it is what lets the "why is GPU demand unserved"
// journey confirm it reached the right pool.
func acceleratorFact(requirements []capacityapi.Requirement) *capacityapi.PostureFact {
	var matched []capacityapi.Requirement
	for _, requirement := range requirements {
		if requirementDeclaresAccelerator(requirement) {
			matched = append(matched, requirement)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	lead := matched[0]
	summary := "Declared accelerator pool — " + lead.Key + " " + lead.Operator
	if len(lead.Values) > 0 {
		values := lead.Values
		if len(values) > 4 {
			summary += " [" + strings.Join(values[:4], ", ") + ", +" + strconv.Itoa(len(values)-4) + " more]"
		} else {
			summary += " [" + strings.Join(values, ", ") + "]"
		}
	}
	if len(matched) > 1 {
		summary += " (+" + strconv.Itoa(len(matched)-1) + " more)"
	}
	return &capacityapi.PostureFact{
		Code:        "declared_accelerator_pool",
		Summary:     summary,
		Detail:      "Requirements pin accelerator instances. Declared, not measured: it says what this pool would provision, not that accelerator capacity exists right now.",
		SourcePaths: []string{"spec.template.spec.requirements"},
	}
}

func poolConfiguration(spec karpenter.NodePoolSpec) capacityapi.PoolConfiguration {
	configuration := capacityapi.NewPoolConfiguration()
	configuration.Weight = spec.Weight
	configuration.Replicas = spec.Replicas
	for key, value := range spec.TemplateLabels {
		configuration.Labels[key] = value
	}
	for _, requirement := range spec.Requirements {
		configuration.Requirements = append(configuration.Requirements, capacityapi.Requirement{
			Key: requirement.Key, Operator: string(requirement.Operator), Values: append([]string{}, requirement.Values...), MinValues: requirement.MinValues,
		})
	}
	for _, taint := range spec.Taints {
		configuration.Taints = append(configuration.Taints, capacityapi.Taint{Key: taint.Key, Value: taint.Value, Effect: string(taint.Effect)})
	}
	for _, taint := range spec.StartupTaints {
		configuration.StartupTaints = append(configuration.StartupTaints, capacityapi.Taint{Key: taint.Key, Value: taint.Value, Effect: string(taint.Effect)})
	}
	configuration.ExpireAfter = spec.ExpireAfter
	configuration.TerminationGracePeriod = spec.TerminationGracePeriod
	return configuration
}

func disruptionPolicy(spec karpenter.NodePoolSpec) capacityapi.DisruptionPolicy {
	policy := capacityapi.NewDisruptionPolicy()
	policy.ConsolidationPolicy = spec.Disruption.ConsolidationPolicy
	policy.ConsolidateAfter = spec.Disruption.ConsolidateAfter
	for _, budget := range spec.Disruption.Budgets {
		policy.Budgets = append(policy.Budgets, capacityapi.DisruptionBudget{
			Nodes: budget.Nodes, Reasons: append([]string{}, budget.Reasons...), Schedule: budget.Schedule, Duration: budget.Duration,
		})
	}
	return policy
}

func buildWorkloadMembers(pods []*corev1.Pod, snapshot Snapshot) []capacityapi.PoolMember {
	type aggregate struct {
		owner    subject.Ref
		pods     int
		requests corev1.ResourceList
		nodes    map[string]capacityapi.ResourceIdentity
	}
	groups := map[string]*aggregate{}
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		owner := defaultPodOwner(pod)
		if snapshot.ResolvePodOwner != nil {
			if resolved := snapshot.ResolvePodOwner(pod); resolved != nil {
				owner = *resolved
			}
		}
		key := owner.Group + "\x00" + owner.Kind + "\x00" + owner.Namespace + "\x00" + owner.Name
		group := groups[key]
		if group == nil {
			group = &aggregate{owner: owner, requests: corev1.ResourceList{}, nodes: map[string]capacityapi.ResourceIdentity{}}
			groups[key] = group
		}
		group.pods++
		addResources(group.requests, EffectivePodRequests(pod))
		if pod.Spec.NodeName != "" {
			group.nodes[pod.Spec.NodeName] = capacityapi.ResourceIdentity{Ref: subject.Ref{Kind: "Node", Name: pod.Spec.NodeName}, APIVersion: "v1"}
		}
	}

	result := make([]capacityapi.PoolMember, 0, len(groups))
	certainty := scheduledRequestCertainty(snapshot.Coverage)
	for _, group := range groups {
		workload := capacityapi.NewWorkloadMember()
		workload.Owner = group.owner
		workload.PodCount = group.pods
		requests := QuantityObservation(group.requests, certainty, capacityapi.GranularityAggregate, snapshot.GeneratedAt, "pods.spec.resources")
		workload.Requests = &requests
		nodeNames := make([]string, 0, len(group.nodes))
		for name := range group.nodes {
			nodeNames = append(nodeNames, name)
		}
		sort.Strings(nodeNames)
		workload.NodesMeta.Total = len(nodeNames)
		nodeLimit := len(nodeNames)
		if nodeLimit > defaultWorkloadNodeLimit {
			nodeLimit = defaultWorkloadNodeLimit
			workload.NodesMeta.Truncated = true
		}
		workload.NodesMeta.Returned = nodeLimit
		for _, name := range nodeNames[:nodeLimit] {
			workload.Nodes = append(workload.Nodes, group.nodes[name])
		}
		result = append(result, capacityapi.PoolMember{
			Type:     capacityapi.MemberWorkload,
			Resource: capacityapi.ResourceIdentity{Ref: group.owner},
			Workload: &workload,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Workload.PodCount != result[j].Workload.PodCount {
			return result[i].Workload.PodCount > result[j].Workload.PodCount
		}
		return memberKey(result[i]) < memberKey(result[j])
	})
	return result
}

// DemandOwnerForPod returns the subject a pod's demand group is keyed by —
// the identical resolution BuildDemandGroupModels applies, so a filter built
// from it can never disagree with the grouping.
func DemandOwnerForPod(pod *corev1.Pod, resolve func(*corev1.Pod) *subject.Ref) subject.Ref {
	owner := defaultPodOwner(pod)
	if resolve != nil {
		if resolved := resolve(pod); resolved != nil && resolved.Kind != "" && resolved.Name != "" {
			owner = *resolved
		}
	}
	return owner
}

func defaultPodOwner(pod *corev1.Pod) subject.Ref {
	for _, owner := range pod.OwnerReferences {
		if owner.Controller != nil && *owner.Controller {
			return subject.Ref{Group: schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind).Group, Kind: owner.Kind, Namespace: pod.Namespace, Name: owner.Name}
		}
	}
	return subject.Ref{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
}

func identityForUnstructured(resource *unstructured.Unstructured) capacityapi.ResourceIdentity {
	group := schema.FromAPIVersionAndKind(resource.GetAPIVersion(), resource.GetKind()).Group
	return capacityapi.ResourceIdentity{
		Ref:        subject.Ref{Group: group, Kind: resource.GetKind(), Namespace: resource.GetNamespace(), Name: resource.GetName()},
		APIVersion: resource.GetAPIVersion(),
		UID:        string(resource.GetUID()),
	}
}

func identityForNode(node *corev1.Node) capacityapi.ResourceIdentity {
	return capacityapi.ResourceIdentity{Ref: subject.Ref{Kind: "Node", Name: node.Name}, APIVersion: "v1", UID: string(node.UID)}
}

func normalizeConditions(conditions []metav1.Condition) []capacityapi.Condition {
	result := make([]capacityapi.Condition, 0, len(conditions))
	for _, condition := range conditions {
		normalized := capacityapi.Condition{
			Type: condition.Type, Status: capacityapi.ConditionStatus(condition.Status), Reason: condition.Reason, Message: condition.Message,
		}
		if condition.ObservedGeneration > 0 {
			observed := condition.ObservedGeneration
			normalized.ObservedGeneration = &observed
		}
		if !condition.LastTransitionTime.IsZero() {
			transition := condition.LastTransitionTime.Time
			normalized.LastTransitionTime = &transition
		}
		result = append(result, normalized)
	}
	return result
}

func normalizeNodeConditions(conditions []corev1.NodeCondition) []capacityapi.Condition {
	result := make([]capacityapi.Condition, 0, len(conditions))
	for _, condition := range conditions {
		normalized := capacityapi.Condition{
			Type: string(condition.Type), Status: capacityapi.ConditionStatus(condition.Status), Reason: condition.Reason, Message: condition.Message,
		}
		if !condition.LastTransitionTime.IsZero() {
			transition := condition.LastTransitionTime.Time
			normalized.LastTransitionTime = &transition
		}
		result = append(result, normalized)
	}
	return result
}

func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

func conditionBool(status metav1.ConditionStatus) *bool {
	switch status {
	case metav1.ConditionTrue:
		return boolPointer(true)
	case metav1.ConditionFalse:
		return boolPointer(false)
	default:
		return nil
	}
}

func nodeReady(node *corev1.Node) *bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return conditionBool(metav1.ConditionStatus(condition.Status))
		}
	}
	return nil
}

func claimStage(claim *unstructured.Unstructured) capacityapi.ClaimStage {
	if claim.GetDeletionTimestamp() != nil {
		return capacityapi.ClaimStageTerminating
	}
	conditions := karpenter.NodeClaimConditions(claim)
	if condition := findCondition(conditions, "Ready"); condition != nil && condition.Status == metav1.ConditionTrue {
		return capacityapi.ClaimStageReady
	}
	for _, condition := range conditions {
		if isClaimLifecycleCondition(condition.Type) && karpenter.IsFailedLifecycleConditionForVersion(claim.GetAPIVersion(), condition) {
			return capacityapi.ClaimStageFailed
		}
	}
	for _, candidate := range []struct {
		condition string
		stage     capacityapi.ClaimStage
	}{{"Initialized", capacityapi.ClaimStageInitialized}, {"Registered", capacityapi.ClaimStageRegistered}, {"Launched", capacityapi.ClaimStageLaunched}} {
		if condition := findCondition(conditions, candidate.condition); condition != nil && condition.Status == metav1.ConditionTrue {
			return candidate.stage
		}
	}
	return capacityapi.ClaimStagePending
}

func isClaimLifecycleCondition(conditionType string) bool {
	switch conditionType {
	case "Ready", "Initialized", "Registered", "Launched":
		return true
	default:
		return false
	}
}

func incrementClaimStage(summary *capacityapi.ClaimLifecycleSummary, stage capacityapi.ClaimStage) {
	switch stage {
	case capacityapi.ClaimStageReady:
		summary.Ready++
	case capacityapi.ClaimStageInitialized:
		summary.Initialized++
	case capacityapi.ClaimStageRegistered:
		summary.Registered++
	case capacityapi.ClaimStageLaunched:
		summary.Launched++
	case capacityapi.ClaimStageFailed:
		summary.Failed++
	case capacityapi.ClaimStageTerminating:
		summary.Terminating++
	default:
		summary.Pending++
	}
}

func addComposition(composition *capacityapi.PoolComposition, node *corev1.Node) {
	addBucket(&composition.CapacityTypes, valueOrUnknown(node.Labels[karpenter.CapacityTypeLabelKey]))
	addBucket(&composition.InstanceTypes, valueOrUnknown(node.Labels[instanceTypeLabel]))
	addBucket(&composition.Zones, valueOrUnknown(node.Labels[zoneLabel]))
	addBucket(&composition.Architectures, valueOrUnknown(node.Labels[architectureLabel]))
	addBucket(&composition.Images, valueOrUnknown(node.Status.NodeInfo.OSImage))
}

func addBucket(buckets *[]capacityapi.CompositionBucket, value string) {
	for i := range *buckets {
		if (*buckets)[i].Value == value {
			(*buckets)[i].Count++
			return
		}
	}
	*buckets = append(*buckets, capacityapi.CompositionBucket{Value: value, Count: 1})
}

func sortComposition(composition *capacityapi.PoolComposition) {
	for _, axis := range []struct {
		buckets *[]capacityapi.CompositionBucket
		meta    *capacityapi.BoundedResultMeta
	}{
		{&composition.CapacityTypes, &composition.CapacityTypesMeta},
		{&composition.InstanceTypes, &composition.InstanceTypesMeta},
		{&composition.Zones, &composition.ZonesMeta},
		{&composition.Architectures, &composition.ArchitecturesMeta},
		{&composition.Images, &composition.ImagesMeta},
	} {
		buckets := axis.buckets
		sort.Slice(*buckets, func(i, j int) bool {
			if (*buckets)[i].Count != (*buckets)[j].Count {
				return (*buckets)[i].Count > (*buckets)[j].Count
			}
			return (*buckets)[i].Value < (*buckets)[j].Value
		})
		axis.meta.Total = len(*buckets)
		if len(*buckets) > defaultCompositionBucketLimit {
			*buckets = (*buckets)[:defaultCompositionBucketLimit]
			axis.meta.Truncated = true
		}
		axis.meta.Returned = len(*buckets)
	}
}

func valueOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func sourceCertainty(coverage capacityapi.CoverageBySource, source capacityapi.CoverageSource) capacityapi.Certainty {
	entry, ok := coverage[source]
	if !ok || entry.Status == capacityapi.CoverageDenied || entry.Status == capacityapi.CoverageSyncing || entry.Status == capacityapi.CoverageUnavailable || entry.Status == capacityapi.CoverageError {
		return capacityapi.CertaintyUnknown
	}
	if entry.Status == capacityapi.CoveragePartial && entry.ItemCount == nil {
		return capacityapi.CertaintyUnknown
	}
	if entry.Status == capacityapi.CoverageAvailable && entry.Scope == capacityapi.CoverageScopeCluster {
		return capacityapi.CertaintyExact
	}
	return capacityapi.CertaintyLowerBound
}

func scheduledRequestCertainty(coverage capacityapi.CoverageBySource) capacityapi.Certainty {
	pods := sourceCertainty(coverage, capacityapi.CoveragePods)
	nodes := sourceCertainty(coverage, capacityapi.CoverageNodes)
	claims := sourceCertainty(coverage, capacityapi.CoverageNodeClaims)
	attribution := capacityapi.CertaintyUnknown
	if nodes == capacityapi.CertaintyExact || claims == capacityapi.CertaintyExact {
		attribution = capacityapi.CertaintyExact
	} else if nodes == capacityapi.CertaintyLowerBound || claims == capacityapi.CertaintyLowerBound {
		attribution = capacityapi.CertaintyLowerBound
	}
	if pods == capacityapi.CertaintyUnknown || attribution == capacityapi.CertaintyUnknown {
		return capacityapi.CertaintyUnknown
	}
	if pods == capacityapi.CertaintyExact && attribution == capacityapi.CertaintyExact {
		return capacityapi.CertaintyExact
	}
	return capacityapi.CertaintyLowerBound
}

func sourceObserved(coverage capacityapi.CoverageBySource, source capacityapi.CoverageSource) bool {
	entry, ok := coverage[source]
	if !ok {
		return false
	}
	// Partial without an item count means discovery acknowledged the source but
	// no list ever happened — treating it as observed would fabricate zeros.
	return entry.Status == capacityapi.CoverageAvailable ||
		(entry.Status == capacityapi.CoveragePartial && entry.ItemCount != nil)
}

func copyCoverage(source capacityapi.CoverageBySource) capacityapi.CoverageBySource {
	result := capacityapi.CoverageBySource{}
	for name, coverage := range source {
		coverage.Namespaces = append([]string{}, coverage.Namespaces...)
		coverage.ImpactFields = append([]string{}, coverage.ImpactFields...)
		result[name] = coverage
	}
	return result
}

func sortMembers(items []capacityapi.PoolMember) {
	sort.Slice(items, func(i, j int) bool { return memberKey(items[i]) < memberKey(items[j]) })
}

func memberKey(member capacityapi.PoolMember) string {
	ref := member.Resource.Ref
	return string(member.Type) + "\x00" + ref.Group + "\x00" + ref.Kind + "\x00" + ref.Namespace + "\x00" + ref.Name
}

func poolModelLess(left, right PoolModel) bool {
	leftReady := left.Observation.Ready
	rightReady := right.Observation.Ready
	leftRank := readinessRank(leftReady)
	rightRank := readinessRank(rightReady)
	if leftRank != rightRank {
		return leftRank < rightRank
	}
	leftPressure := highestPressure(left.Observation.Ledger.LimitPressure)
	rightPressure := highestPressure(right.Observation.Ledger.LimitPressure)
	if leftPressure != rightPressure {
		return leftPressure > rightPressure
	}
	return left.Observation.Resource.Ref.Name < right.Observation.Resource.Ref.Name
}

func readinessRank(ready *bool) int {
	if ready == nil {
		return 1
	}
	if !*ready {
		return 0
	}
	return 2
}

func highestPressure(pressures []capacityapi.LimitPressure) float64 {
	result := -1.0
	for _, pressure := range pressures {
		if pressure.OverLimit && result < 100 {
			result = 100
		}
		if pressure.Percent != nil && *pressure.Percent > result {
			result = *pressure.Percent
		}
	}
	return result
}

func boolPointer(value bool) *bool { return &value }
