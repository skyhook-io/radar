package capacity

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/autoscalerstatus"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Group identity comes ONLY from node labels or Karpenter CRDs — never from
// parsing an autoscaler's own group names, which cloud providers truncate.
// Precedence when a node carries several: karpenter > gke > eks > aks.
const (
	labelGKENodePool  = "cloud.google.com/gke-nodepool"
	labelEKSNodeGroup = "eks.amazonaws.com/nodegroup"
	labelAKSAgentPool = "kubernetes.azure.com/agentpool"
)

// maxGroupChildren bounds the per-group autoscaler-child list; the meta carries
// the truncation so nothing is silently dropped.
const maxGroupChildren = 32

type GroupsModel struct {
	Groups                 []capacityapi.CapacityGroupSummary
	OrphanAutoscalerGroups []capacityapi.AutoscalerChildObservation
	OrphanMeta             capacityapi.BoundedResultMeta
	Managers               []capacityapi.ManagerSummary
	UnattributedNodeCount  int
	ClusterScheduling      *capacityapi.SchedulingCapacity
}

// groupBuilder accumulates a logical group's evidence before it is finalized
// into a CapacityGroupSummary. domain is the label family the identity came
// from ("karpenter", "gke", "eks", "aks") and drives manager detection.
type groupBuilder struct {
	id       string
	name     string
	domain   string
	pool     *unstructured.Unstructured
	nodes    []*corev1.Node
	children []capacityapi.AutoscalerChildObservation
}

// BuildGroupsModel derives the logical-group inventory across every capacity
// manager. status is nil when the cluster-autoscaler status ConfigMap was not
// observed. model is the already-built Karpenter model (never nil).
func BuildGroupsModel(snapshot Snapshot, status *autoscalerstatus.Status, model *Model, generatedAt time.Time) GroupsModel {
	asOf := generatedAt
	if asOf.IsZero() {
		asOf = snapshot.GeneratedAt
	}
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}

	result := GroupsModel{
		Groups:                 []capacityapi.CapacityGroupSummary{},
		OrphanAutoscalerGroups: []capacityapi.AutoscalerChildObservation{},
		Managers:               []capacityapi.ManagerSummary{},
	}

	nodeByName := make(map[string]*corev1.Node, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		if node != nil && node.Name != "" {
			nodeByName[node.Name] = node
		}
	}
	poolByName := make(map[string]*unstructured.Unstructured, len(snapshot.NodePools))
	for _, pool := range snapshot.NodePools {
		if pool != nil && pool.GetName() != "" {
			poolByName[pool.GetName()] = pool
		}
	}

	builders := map[string]*groupBuilder{}
	order := []string{}
	getBuilder := func(id string) *groupBuilder {
		if b := builders[id]; b != nil {
			return b
		}
		b := &groupBuilder{id: id}
		builders[id] = b
		order = append(order, id)
		return b
	}

	// groupIDByNode maps each attributed node to its group; unattributed nodes
	// are deliberately absent so an autoscaler child that only joins them stays
	// an orphan rather than fabricating a parent.
	groupIDByNode := map[string]string{}
	attributed := map[string]bool{}

	// Every NodePool is a group — even with zero nodes, the CRD is evidence.
	// Node membership (and thus node counts) is reused from the model, which
	// resolves claim-linked nodes the raw label would miss.
	for i := range model.Pools {
		poolName := model.Pools[i].Observation.Resource.Ref.Name
		if poolName == "" {
			continue
		}
		b := getBuilder("karpenter-nodepool/" + poolName)
		b.domain = "karpenter"
		b.name = poolName
		b.pool = poolByName[poolName]
		for _, member := range model.Pools[i].Nodes {
			nodeName := member.Resource.Ref.Name
			attributed[nodeName] = true
			groupIDByNode[nodeName] = b.id
			if node := nodeByName[nodeName]; node != nil {
				b.nodes = append(b.nodes, node)
			}
		}
	}

	// Remaining nodes are bucketed by identity label; those with none go to the
	// unattributed presentation bucket — never a group, never called "static".
	for _, node := range snapshot.Nodes {
		if node == nil || node.Name == "" || attributed[node.Name] {
			continue
		}
		id, name, domain, ok := groupIdentityForNode(node)
		if !ok {
			result.UnattributedNodeCount++
			continue
		}
		b := getBuilder(id)
		if b.name == "" {
			b.name = name
		}
		if b.domain == "" {
			b.domain = domain
		}
		b.nodes = append(b.nodes, node)
		groupIDByNode[node.Name] = id
	}

	// Join each autoscaler-published child (a zonal MIG/VMSS) to a logical group
	// by node-name-prefix evidence. Children with no joinable nodes — scale-to-
	// zero included — stay orphans with an ID equal to the basename, so a child
	// that gains a parent later never changes ID.
	var allChildren []capacityapi.AutoscalerChildObservation
	if status != nil {
		for _, group := range status.NodeGroups {
			child := mapChild(group)
			allChildren = append(allChildren, child)
			// GKE MIG basenames end "-grp" and append node names with a "-";
			// AKS VMSS names append with no separator, so stripping "-grp" and
			// prefix-matching covers both.
			joinKey := strings.TrimSuffix(group.Basename, "-grp")
			consensus := ""
			matched := 0
			conflict := false
			if joinKey != "" {
				for _, node := range snapshot.Nodes {
					if node == nil || node.Name == "" || !strings.HasPrefix(node.Name, joinKey) {
						continue
					}
					matched++
					gid := groupIDByNode[node.Name]
					if gid == "" {
						conflict = true
						continue
					}
					if consensus == "" {
						consensus = gid
					} else if consensus != gid {
						conflict = true
					}
				}
			}
			if matched > 0 && !conflict && consensus != "" && builders[consensus] != nil {
				builders[consensus].children = append(builders[consensus].children, child)
			} else {
				result.OrphanAutoscalerGroups = append(result.OrphanAutoscalerGroups, child)
			}
		}
	}

	podsByNode := map[string][]*corev1.Pod{}
	for _, pod := range snapshot.Pods {
		if pod != nil && pod.Spec.NodeName != "" {
			podsByNode[pod.Spec.NodeName] = append(podsByNode[pod.Spec.NodeName], pod)
		}
	}

	for _, id := range order {
		result.Groups = append(result.Groups, finalizeGroup(builders[id], snapshot, podsByNode, status != nil, asOf))
	}
	sort.Slice(result.Groups, func(i, j int) bool {
		if result.Groups[i].Name != result.Groups[j].Name {
			return result.Groups[i].Name < result.Groups[j].Name
		}
		return result.Groups[i].ID < result.Groups[j].ID
	})

	sort.Slice(result.OrphanAutoscalerGroups, func(i, j int) bool {
		return result.OrphanAutoscalerGroups[i].ID < result.OrphanAutoscalerGroups[j].ID
	})
	result.OrphanMeta = capacityapi.BoundedResultMeta{
		Total:    len(result.OrphanAutoscalerGroups),
		Returned: len(result.OrphanAutoscalerGroups),
	}

	result.Managers = buildManagers(snapshot, status, orderedBuilders(builders, order), allChildren)
	result.ClusterScheduling = buildClusterScheduling(snapshot, model, asOf)
	return result
}

func groupIdentityForNode(node *corev1.Node) (id, name, domain string, ok bool) {
	if v := node.Labels[karpenter.NodePoolLabelKey]; v != "" {
		return "karpenter-nodepool/" + v, v, "karpenter", true
	}
	if v := node.Labels[labelGKENodePool]; v != "" {
		return "gke-nodepool/" + v, v, "gke", true
	}
	if v := node.Labels[labelEKSNodeGroup]; v != "" {
		return "eks-nodegroup/" + v, v, "eks", true
	}
	if v := node.Labels[labelAKSAgentPool]; v != "" {
		return "aks-agentpool/" + v, v, "aks", true
	}
	return "", "", "", false
}

func finalizeGroup(b *groupBuilder, snapshot Snapshot, podsByNode map[string][]*corev1.Pod, statusObserved bool, asOf time.Time) capacityapi.CapacityGroupSummary {
	summary := capacityapi.CapacityGroupSummary{
		ID:       b.id,
		Name:     b.name,
		Children: []capacityapi.AutoscalerChildObservation{},
	}
	summary.Manager, summary.ManagerValidated = detectManager(b)

	summary.NodeCount = len(b.nodes)
	ready := 0
	for _, node := range b.nodes {
		if r := nodeReady(node); r != nil && *r {
			ready++
		}
	}
	summary.ReadyNodeCount = ready

	// Node allocatable is exact once the node object is in hand; pod-derived
	// requests follow the same gate the pooled ledger uses — absent, never zero,
	// when pods were not observed.
	if sourceObserved(snapshot.Coverage, capacityapi.CoverageNodes) {
		allocatable := QuantityObservation(AccountResources(b.nodes, nil).Allocatable, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, asOf, "nodes.status.allocatable")
		summary.Allocatable = &allocatable
	}
	if sourceObserved(snapshot.Coverage, capacityapi.CoveragePods) {
		var pods []*corev1.Pod
		for _, node := range b.nodes {
			pods = append(pods, podsByNode[node.Name]...)
		}
		requests := QuantityObservation(AccountResources(nil, pods).ScheduledRequests, scheduledRequestCertainty(snapshot.Coverage), capacityapi.GranularityAggregate, asOf, "pods.spec.resources")
		summary.ScheduledRequests = &requests
	}

	summary.Scaling = scalingFacts(b, statusObserved)

	sort.Slice(b.children, func(i, j int) bool { return b.children[i].ID < b.children[j].ID })
	summary.ChildrenMeta.Total = len(b.children)
	returned := len(b.children)
	if returned > maxGroupChildren {
		returned = maxGroupChildren
		summary.ChildrenMeta.Truncated = true
	}
	summary.ChildrenMeta.Returned = returned
	summary.Children = append(summary.Children, b.children[:returned]...)
	return summary
}

func detectManager(b *groupBuilder) (capacityapi.CapacityManager, bool) {
	if b.domain == "karpenter" {
		return capacityapi.ManagerKarpenter, true
	}
	if len(b.children) == 0 {
		return "", false
	}
	switch b.domain {
	case "gke":
		// GKE's autoscaler is validated live against real clusters.
		return capacityapi.ManagerGKEAutoscaler, true
	case "aks":
		return capacityapi.ManagerAKSAutoscaler, false
	case "eks":
		return capacityapi.ManagerClusterAutoscaler, false
	}
	return "", false
}

// scalingFacts always returns at least one fact — bounds/target when the
// autoscaler publishes them, otherwise an honest statement of what is not
// published or not detected. It never emits a bare dash.
func scalingFacts(b *groupBuilder, statusObserved bool) []capacityapi.ScalingFact {
	if b.domain == "karpenter" {
		var limits corev1.ResourceList
		if b.pool != nil {
			limits = karpenter.NormalizeNodePoolSpec(b.pool).Limits
		}
		if len(limits) > 0 {
			return []capacityapi.ScalingFact{{Code: "limits", Summary: "limits: " + formatResourceList(limits)}}
		}
		return []capacityapi.ScalingFact{{Code: "limits", Summary: "no limits configured"}}
	}

	if len(b.children) > 0 {
		allBounds := true
		allTarget := true
		var sumMin, sumMax, sumTarget int
		for _, child := range b.children {
			if child.MinSize == nil || child.MaxSize == nil {
				allBounds = false
			}
			if child.Target == nil {
				allTarget = false
			}
			if child.MinSize != nil {
				sumMin += *child.MinSize
			}
			if child.MaxSize != nil {
				sumMax += *child.MaxSize
			}
			if child.Target != nil {
				sumTarget += *child.Target
			}
		}
		if !allBounds {
			return []capacityapi.ScalingFact{{Code: "bounds_not_published", Summary: "bounds not published in-cluster"}}
		}
		facts := []capacityapi.ScalingFact{{Code: "bounds", Summary: fmt.Sprintf("%d–%d nodes", sumMin, sumMax)}}
		if allTarget {
			facts = append(facts, capacityapi.ScalingFact{Code: "target", Summary: fmt.Sprintf("target %d", sumTarget)})
		}
		return facts
	}

	if statusObserved {
		return []capacityapi.ScalingFact{{Code: "bounds_not_published", Summary: "bounds not published in-cluster"}}
	}
	return []capacityapi.ScalingFact{{Code: "no_manager_detected", Summary: "no capacity manager detected"}}
}

func mapChild(group autoscalerstatus.NodeGroup) capacityapi.AutoscalerChildObservation {
	child := capacityapi.AutoscalerChildObservation{
		ID:         group.Basename,
		Name:       group.Basename,
		MinSize:    intPtrCopy(group.MinSize),
		MaxSize:    intPtrCopy(group.MaxSize),
		Target:     intPtrCopy(group.Target),
		Health:     group.Health.Status,
		ReadyNodes: intPtrCopy(group.Health.Ready),
		TotalNodes: intPtrCopy(group.Health.Registered),
		AsOf:       timePtrCopy(group.Health.LastProbeTime),
	}
	if group.ScaleUp.Backoff != nil {
		child.Backoff = &capacityapi.AutoscalerBackoff{
			ErrorClass:   group.ScaleUp.Backoff.ErrorClass,
			ErrorCode:    group.ScaleUp.Backoff.ErrorCode,
			ErrorMessage: group.ScaleUp.Backoff.ErrorMessage,
		}
	}
	return child
}

func buildManagers(snapshot Snapshot, status *autoscalerstatus.Status, builders []*groupBuilder, allChildren []capacityapi.AutoscalerChildObservation) []capacityapi.ManagerSummary {
	managers := []capacityapi.ManagerSummary{}

	if len(snapshot.NodePools) > 0 {
		count := 0
		for _, b := range builders {
			if b.domain == "karpenter" {
				count++
			}
		}
		st, detail := karpenterHealthRollup(snapshot.NodePools)
		managers = append(managers, capacityapi.ManagerSummary{
			Manager:    capacityapi.ManagerKarpenter,
			GroupCount: count,
			Status:     st,
			Detail:     detail,
		})
	}

	if status != nil {
		manager := capacityapi.ManagerClusterAutoscaler
		if anyDomainJoined(builders, "gke") {
			manager = capacityapi.ManagerGKEAutoscaler
		} else if anyDomainJoined(builders, "aks") {
			manager = capacityapi.ManagerAKSAutoscaler
		}
		groupCount := 0
		for _, b := range builders {
			if b.domain != "karpenter" && len(b.children) > 0 {
				groupCount++
			}
		}
		st, detail := autoscalerHealthRollup(status, allChildren)
		asOf := timePtrCopy(status.Time)
		managers = append(managers, capacityapi.ManagerSummary{
			Manager:    manager,
			GroupCount: groupCount,
			Status:     st,
			Detail:     detail,
			AsOf:       asOf,
		})
	}

	sort.Slice(managers, func(i, j int) bool {
		return string(managers[i].Manager) < string(managers[j].Manager)
	})
	return managers
}

func karpenterHealthRollup(pools []*unstructured.Unstructured) (capacityapi.ManagerRollupStatus, string) {
	ready, notReady, unknown := 0, 0, 0
	for _, pool := range pools {
		if pool == nil {
			continue
		}
		switch karpenter.NodePoolReadiness(pool) {
		case karpenter.ReadinessReady:
			ready++
		case karpenter.ReadinessNotReady:
			notReady++
		default:
			unknown++
		}
	}
	total := ready + notReady + unknown
	if notReady > 0 {
		return capacityapi.ManagerDegraded, fmt.Sprintf("%d of %d NodePools ready", ready, total)
	}
	if unknown > 0 {
		return capacityapi.ManagerUnknown, ""
	}
	return capacityapi.ManagerHealthy, ""
}

// autoscalerHealthRollup deliberately applies NO staleness downgrade — live
// clusters publish hours-old payloads while perfectly healthy, so freshness is
// carried as AsOf, not folded into status.
func autoscalerHealthRollup(status *autoscalerstatus.Status, children []capacityapi.AutoscalerChildObservation) (capacityapi.ManagerRollupStatus, string) {
	sorted := append([]capacityapi.AutoscalerChildObservation{}, children...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	degraded := false
	detail := ""
	for _, child := range sorted {
		if child.Backoff != nil {
			degraded = true
			if detail == "" {
				detail = "scale-up backoff on " + child.ID
			}
		}
	}
	if !degraded {
		for _, child := range sorted {
			if child.Health == "Unhealthy" {
				degraded = true
				if detail == "" {
					detail = "node group " + child.ID + " unhealthy"
				}
			}
		}
	}
	if status.ClusterWide.Health.Status == "Unhealthy" {
		degraded = true
		if detail == "" {
			detail = "autoscaler reports cluster-wide health unhealthy"
		}
	}
	if degraded {
		return capacityapi.ManagerDegraded, detail
	}
	if status.AutoscalerState == "Running" {
		return capacityapi.ManagerHealthy, ""
	}
	return capacityapi.ManagerUnknown, ""
}

// buildClusterScheduling sums scheduled requests vs allocatable across ALL
// observed nodes and bound pods — every manager included — and reuses
// Karpenter's in-flight capacity, the only future segment Radar can observe.
func buildClusterScheduling(snapshot Snapshot, model *Model, asOf time.Time) *capacityapi.SchedulingCapacity {
	if len(snapshot.Nodes) == 0 {
		return nil
	}
	accounting := AccountResources(snapshot.Nodes, snapshot.Pods)
	scheduling := &capacityapi.SchedulingCapacity{}
	if sourceObserved(snapshot.Coverage, capacityapi.CoverageNodes) {
		allocatable := QuantityObservation(accounting.Allocatable, sourceCertainty(snapshot.Coverage, capacityapi.CoverageNodes), capacityapi.GranularityAggregate, asOf, "nodes.status.allocatable")
		scheduling.Allocatable = &allocatable
	}
	if sourceObserved(snapshot.Coverage, capacityapi.CoveragePods) {
		requests := QuantityObservation(accounting.ScheduledRequests, scheduledRequestCertainty(snapshot.Coverage), capacityapi.GranularityAggregate, asOf, "pods.spec.resources")
		scheduling.ScheduledRequests = &requests
	}
	if model != nil && model.Scheduling != nil {
		scheduling.InFlightCapacity = model.Scheduling.InFlightCapacity
	}
	return scheduling
}

func anyDomainJoined(builders []*groupBuilder, domain string) bool {
	for _, b := range builders {
		if b.domain == domain && len(b.children) > 0 {
			return true
		}
	}
	return false
}

func orderedBuilders(builders map[string]*groupBuilder, order []string) []*groupBuilder {
	result := make([]*groupBuilder, 0, len(order))
	for _, id := range order {
		result = append(result, builders[id])
	}
	return result
}

func formatResourceList(list corev1.ResourceList) string {
	names := make([]string, 0, len(list))
	for name := range list {
		names = append(names, string(name))
	}
	sort.Slice(names, func(i, j int) bool {
		ri, rj := resourceOrderRank(names[i]), resourceOrderRank(names[j])
		if ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	parts := make([]string, 0, len(names))
	for _, name := range names {
		quantity := list[corev1.ResourceName(name)]
		parts = append(parts, name+" "+quantity.String())
	}
	return strings.Join(parts, " · ")
}

func resourceOrderRank(name string) int {
	switch name {
	case string(corev1.ResourceCPU):
		return 0
	case string(corev1.ResourceMemory):
		return 1
	default:
		return 2
	}
}

func intPtrCopy(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func timePtrCopy(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
