package capacity

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestBuildAuthoritativePoolClaimNodeJoins(t *testing.T) {
	poolA := capacityTestPool("pool-a", "pool-a-uid", nil, nil)
	poolB := capacityTestPool("pool-b", "pool-b-uid", nil, nil)
	byName := capacityTestClaim("claim-by-name", "claim-by-name-uid", poolA, map[string]any{
		"nodeName": "node-by-name", "providerID": "aws:///node-by-name",
	})
	byProvider := capacityTestClaim("claim-by-provider", "claim-by-provider-uid", poolA, map[string]any{
		"providerID": "aws:///node-by-provider",
	})
	stale := capacityTestClaim("stale-claim", "stale-claim-uid", poolA, map[string]any{
		"nodeName": "stale-node", "providerID": "aws:///stale-node",
	})
	stale.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: karpenter.APIVersionV1,
		Kind:       karpenter.NodePoolKind,
		Name:       poolA.GetName(),
		UID:        types.UID("replaced-pool-a-uid"),
	}})

	nodeByName := capacityTestNode("node-by-name", "node-by-name-uid", "aws:///does-not-match", "pool-b", nil)
	nodeByProvider := capacityTestNode("node-by-provider", "node-by-provider-uid", "aws:///node-by-provider", "pool-b", nil)
	labelFallback := capacityTestNode("node-label-fallback", "node-label-fallback-uid", "", "pool-b", nil)
	staleNode := capacityTestNode("stale-node", "stale-node-uid", "aws:///stale-node", "pool-a", nil)

	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(),
		NodePools:   []*unstructured.Unstructured{poolA, poolB},
		NodeClaims:  []*unstructured.Unstructured{byName, byProvider, stale},
		Nodes:       []*corev1.Node{nodeByName, nodeByProvider, labelFallback, staleNode},
		Coverage:    capacityTestCoverage(),
	})

	gotA := capacityTestMustPool(t, model, "pool-a")
	gotB := capacityTestMustPool(t, model, "pool-b")
	if gotA.Observation.Claims == nil || gotA.Observation.Claims.Total != 2 || len(gotA.Claims) != 2 {
		t.Errorf("pool-a claims = %+v/%d, want 2 authoritative claims", gotA.Observation.Claims, len(gotA.Claims))
	}
	if gotA.Observation.Nodes == nil || gotA.Observation.Nodes.Total != 2 || len(gotA.Nodes) != 2 {
		t.Errorf("pool-a nodes = %+v/%d, want node-name and providerID joins", gotA.Observation.Nodes, len(gotA.Nodes))
	}
	if gotB.Observation.Nodes == nil || gotB.Observation.Nodes.Total != 1 || len(gotB.Nodes) != 1 || gotB.Nodes[0].Resource.Ref.Name != "node-label-fallback" {
		t.Errorf("pool-b nodes = %+v, want only label fallback node", gotB.Nodes)
	}
	if capacityTestHasMember(gotA.Claims, "stale-claim") || capacityTestHasMember(gotA.Nodes, "stale-node") {
		t.Error("owner UID mismatch relinked stale claim or its node")
	}
	if model.OrphanedClaimCount != 1 || model.UnpooledNodeCount != 1 {
		t.Errorf("structural gap counts = claims:%d nodes:%d, want 1/1", model.OrphanedClaimCount, model.UnpooledNodeCount)
	}

	claimByName := capacityTestMember(t, gotA.Claims, "claim-by-name")
	if claimByName.Resource.UID != "claim-by-name-uid" {
		t.Errorf("claim identity UID = %q, want claim-by-name-uid", claimByName.Resource.UID)
	}
	if claimByName.Claim == nil || claimByName.Claim.Node == nil {
		t.Fatal("claim-by-name did not retain its observed Node relationship")
	}
	if claimByName.Claim.Node.UID != "node-by-name-uid" {
		t.Errorf("claim Node UID = %q, want node-by-name-uid", claimByName.Claim.Node.UID)
	}
	claimByProvider := capacityTestMember(t, gotA.Claims, "claim-by-provider")
	if claimByProvider.Claim == nil || claimByProvider.Claim.Node == nil || claimByProvider.Claim.Node.Ref.Name != "node-by-provider" {
		t.Errorf("providerID-joined claim Node = %+v, want node-by-provider identity", claimByProvider.Claim)
	}
	if node := capacityTestMember(t, gotA.Nodes, "node-by-provider"); node.Resource.UID != "node-by-provider-uid" {
		t.Errorf("node identity UID = %q, want node-by-provider-uid", node.Resource.UID)
	}
}

func TestBuildClaimNodeReferenceRequiresObservedNode(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)
	claim := capacityTestClaim("claim-a", "claim-uid", pool, map[string]any{
		"nodeName": "unobserved-node", "providerID": "aws:///unobserved-node",
	})
	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(),
		NodePools:   []*unstructured.Unstructured{pool},
		NodeClaims:  []*unstructured.Unstructured{claim},
		Coverage:    capacityTestCoverage(),
	})
	member := capacityTestMember(t, capacityTestMustPool(t, model, "compute").Claims, "claim-a")
	if member.Claim == nil || member.Claim.Node != nil {
		t.Fatalf("unobserved claim Node = %+v, want no fabricated identity", member.Claim)
	}
	if member.Claim.NodeName != "unobserved-node" {
		t.Fatalf("claim status node name = %q, want unobserved-node", member.Claim.NodeName)
	}
}

func TestBuildNodePodCountRequiresObservedPods(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)
	node := capacityTestNode("node-a", "node-uid", "", "compute", nil)

	for _, test := range []struct {
		name        string
		podStatus   capacityapi.CoverageStatus
		wantPresent bool
	}{
		{name: "known zero", podStatus: capacityapi.CoverageAvailable, wantPresent: true},
		{name: "not observed", podStatus: capacityapi.CoverageDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			coverage := capacityTestCoverage()
			coverage[capacityapi.CoveragePods] = capacityapi.NewSourceCoverage(test.podStatus, capacityapi.CoverageScopeCluster)
			model := Build(Snapshot{
				GeneratedAt: capacityTestTime(),
				NodePools:   []*unstructured.Unstructured{pool},
				Nodes:       []*corev1.Node{node},
				Coverage:    coverage,
			})
			member := capacityTestMember(t, capacityTestMustPool(t, model, "compute").Nodes, "node-a")
			if member.Node == nil {
				t.Fatal("node member is nil")
			}
			if !test.wantPresent {
				if member.Node.PodCount != nil {
					t.Fatalf("pod count = %d, want omitted", *member.Node.PodCount)
				}
				return
			}
			if member.Node.PodCount == nil || *member.Node.PodCount != 0 {
				t.Fatalf("pod count = %v, want known zero", member.Node.PodCount)
			}
		})
	}
}

func TestBuildExcludesTerminatingUnscheduledPodsFromPendingCount(t *testing.T) {
	live := capacityTestPod("live", "", "worker", map[corev1.ResourceName]string{corev1.ResourceCPU: "500m"})
	terminating := capacityTestPod("terminating", "", "worker", map[corev1.ResourceName]string{corev1.ResourceCPU: "1"})
	deletedAt := metav1.NewTime(capacityTestTime())
	terminating.DeletionTimestamp = &deletedAt

	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(),
		Pods:        []*corev1.Pod{live, terminating},
		Coverage:    capacityTestCoverage(),
	})
	if model.PendingPodCount != 1 {
		t.Fatalf("pending pod count = %d, want only the non-terminating Pod", model.PendingPodCount)
	}
}

func TestBuildCapacityLedgerKeepsCapacityDomainsDistinct(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", map[string]any{
		"limits": map[string]any{"cpu": "100", "nvidia.com/gpu": "8"},
	}, map[string]any{
		"resources": map[string]any{"cpu": "60", "memory": "256Gi", "pods": "100", "nvidia.com/gpu": "3"},
	})
	node := capacityTestNode("node-a", "node-uid", "", "compute", resourceList(map[corev1.ResourceName]string{
		corev1.ResourceCPU: "50", corev1.ResourceMemory: "200Gi", corev1.ResourcePods: "110", "nvidia.com/gpu": "2",
	}))
	pod := capacityTestPod("workload-a", "node-a", "owner-a", map[corev1.ResourceName]string{
		corev1.ResourceCPU: "10", corev1.ResourceMemory: "20Gi", "nvidia.com/gpu": "1",
	})

	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(),
		NodePools:   []*unstructured.Unstructured{pool},
		Nodes:       []*corev1.Node{node},
		Pods:        []*corev1.Pod{pod},
		Coverage:    capacityTestCoverage(),
	})
	ledger := capacityTestMustPool(t, model, "compute").Observation.Ledger

	capacityTestAssertObservation(t, ledger.ConfiguredLimit, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{
		"cpu": "100", "nvidia.com/gpu": "8",
	})
	capacityTestAssertObservation(t, ledger.Provisioned, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{
		"cpu": "60", "memory": "256Gi", "pods": "100", "nvidia.com/gpu": "3",
	})
	capacityTestAssertObservation(t, ledger.Allocatable, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{
		"cpu": "50", "memory": "200Gi", "pods": "110", "nvidia.com/gpu": "2",
	})
	capacityTestAssertObservation(t, ledger.ScheduledRequests, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{
		"cpu": "10", "memory": "20Gi", "pods": "1", "nvidia.com/gpu": "1",
	})
	capacityTestAssertObservation(t, ledger.AggregateUnallocatedRequests, capacityapi.CertaintyExact, capacityapi.GranularityAggregateNotBinpacked, map[string]string{
		"cpu": "40", "memory": "180Gi", "pods": "109", "nvidia.com/gpu": "1",
	})
	capacityTestAssertObservation(t, ledger.LimitHeadroom, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{
		"cpu": "40", "nvidia.com/gpu": "5",
	})
}

func TestBuildDistinguishesAbsentFromExplicitEmptyProvisionedResources(t *testing.T) {
	limits := map[string]any{"cpu": "10"}
	absent := capacityTestPool("absent", "absent-uid", map[string]any{"limits": limits}, nil)
	explicitEmpty := capacityTestPool("explicit-empty", "explicit-empty-uid", map[string]any{"limits": limits}, map[string]any{
		"resources": map[string]any{},
	})

	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(),
		NodePools:   []*unstructured.Unstructured{absent, explicitEmpty},
		Coverage:    capacityTestCoverage(),
	})

	absentLedger := capacityTestMustPool(t, model, "absent").Observation.Ledger
	if absentLedger.Provisioned != nil || absentLedger.LimitHeadroom != nil || len(absentLedger.LimitPressure) != 0 {
		t.Fatalf("absent status.resources produced observations: %+v", absentLedger)
	}

	emptyLedger := capacityTestMustPool(t, model, "explicit-empty").Observation.Ledger
	capacityTestAssertObservation(t, emptyLedger.Provisioned, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{})
	capacityTestAssertObservation(t, emptyLedger.LimitHeadroom, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{"cpu": "10"})
	if len(emptyLedger.LimitPressure) != 1 || emptyLedger.LimitPressure[0].Provisioned != "0" {
		t.Fatalf("explicit-empty status.resources pressure = %+v, want observed zero", emptyLedger.LimitPressure)
	}
}

func TestBuildUnallocatedRequestCertaintyUsesIntervalDirection(t *testing.T) {
	tests := []struct {
		name         string
		nodeCoverage capacityapi.SourceCoverage
		podCoverage  capacityapi.SourceCoverage
		want         capacityapi.Certainty
		wantAbsent   bool
	}{
		{
			name:         "complete operands are exact",
			nodeCoverage: capacityapi.NewSourceCoverage(capacityapi.CoverageAvailable, capacityapi.CoverageScopeCluster),
			podCoverage:  capacityapi.NewSourceCoverage(capacityapi.CoverageAvailable, capacityapi.CoverageScopeCluster),
			want:         capacityapi.CertaintyExact,
		},
		{
			name:         "exact allocatable minus lower-bound requests is an upper bound",
			nodeCoverage: capacityapi.NewSourceCoverage(capacityapi.CoverageAvailable, capacityapi.CoverageScopeCluster),
			podCoverage:  capacityTestPartialCoverage(capacityapi.CoverageScopeAllAuthorizedNamespaces, 1),
			want:         capacityapi.CertaintyUpperBound,
		},
		{
			name:         "lower-bound allocatable minus exact requests is a lower bound",
			nodeCoverage: capacityTestPartialCoverage(capacityapi.CoverageScopeCluster, 1),
			podCoverage:  capacityapi.NewSourceCoverage(capacityapi.CoverageAvailable, capacityapi.CoverageScopeCluster),
			want:         capacityapi.CertaintyLowerBound,
		},
		{
			name:         "two lower bounds have no directional guarantee",
			nodeCoverage: capacityTestPartialCoverage(capacityapi.CoverageScopeCluster, 1),
			podCoverage:  capacityTestPartialCoverage(capacityapi.CoverageScopeAllAuthorizedNamespaces, 1),
			want:         capacityapi.CertaintyUnknown,
		},
		{
			// A difference whose minuend was never observed is not emitted at
			// all — with Nodes denied and claim-attributed pods it would be a
			// meaningless negative number on the ledger.
			name:         "unobserved allocatable emits no difference",
			nodeCoverage: capacityapi.NewSourceCoverage(capacityapi.CoverageDenied, capacityapi.CoverageScopeCluster),
			podCoverage:  capacityapi.NewSourceCoverage(capacityapi.CoverageAvailable, capacityapi.CoverageScopeCluster),
			wantAbsent:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := capacityTestPool("compute", "pool-uid", nil, nil)
			node := capacityTestNode("node-a", "node-uid", "", "compute", resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "4"}))
			pod := capacityTestPod("pod-a", "node-a", "owner-a", map[corev1.ResourceName]string{corev1.ResourceCPU: "1"})
			coverage := capacityTestCoverage()
			coverage[capacityapi.CoverageNodes] = tt.nodeCoverage
			coverage[capacityapi.CoveragePods] = tt.podCoverage

			model := Build(Snapshot{GeneratedAt: capacityTestTime(), NodePools: []*unstructured.Unstructured{pool}, Nodes: []*corev1.Node{node}, Pods: []*corev1.Pod{pod}, Coverage: coverage})
			observation := capacityTestMustPool(t, model, "compute").Observation.Ledger.AggregateUnallocatedRequests
			if tt.wantAbsent {
				if observation != nil {
					t.Fatalf("unallocated = %v, want absent when allocatable was unobserved", observation)
				}
				return
			}
			if observation == nil || observation.Certainty != tt.want {
				t.Fatalf("unallocated certainty = %v, want %q", observation, tt.want)
			}
		})
	}
}

func TestBuildLifecycleCountsFollowSourceAvailability(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)

	t.Run("unavailable sources remain unknown", func(t *testing.T) {
		coverage := capacityTestCoverage()
		coverage[capacityapi.CoverageNodeClaims] = capacityapi.NewSourceCoverage(capacityapi.CoverageDenied, capacityapi.CoverageScopeCluster)
		coverage[capacityapi.CoverageNodes] = capacityapi.NewSourceCoverage(capacityapi.CoverageUnavailable, capacityapi.CoverageScopeCluster)
		model := Build(Snapshot{GeneratedAt: capacityTestTime(), NodePools: []*unstructured.Unstructured{pool}, Coverage: coverage})
		observation := capacityTestMustPool(t, model, "compute").Observation
		if observation.Claims != nil || observation.Nodes != nil {
			t.Fatalf("unavailable lifecycle counts = claims:%+v nodes:%+v, want nil", observation.Claims, observation.Nodes)
		}
	})

	t.Run("partial sources expose lower-bound zero", func(t *testing.T) {
		coverage := capacityTestCoverage()
		coverage[capacityapi.CoverageNodeClaims] = capacityTestPartialCoverage(capacityapi.CoverageScopeCluster, 0)
		coverage[capacityapi.CoverageNodes] = capacityTestPartialCoverage(capacityapi.CoverageScopeCluster, 0)
		model := Build(Snapshot{GeneratedAt: capacityTestTime(), NodePools: []*unstructured.Unstructured{pool}, Coverage: coverage})
		observation := capacityTestMustPool(t, model, "compute").Observation
		if observation.Claims == nil || observation.Nodes == nil || observation.Claims.Total != 0 || observation.Nodes.Total != 0 {
			t.Fatalf("partial lifecycle counts = claims:%+v nodes:%+v, want observed zero pointers", observation.Claims, observation.Nodes)
		}
	})
}

func TestBuildPendingEligibilityUsesObservedNodeClassReadiness(t *testing.T) {
	readyPool := capacityTestPoolWithNodeClass("class-ready", "ready-class")
	notReadyPool := capacityTestPoolWithNodeClass("class-not-ready", "not-ready-class")
	unobservedPool := capacityTestPoolWithNodeClass("class-unobserved", "unobserved-class")
	pod := demandTestPod("pending", "500m")
	pod.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
		Reason: corev1.PodReasonUnschedulable, Message: "0/3 nodes are available: 3 Insufficient cpu.",
	}}
	// A gated group is not awaiting capacity — it must never read as eligible
	// pending demand for a pool.
	held := demandTestPod("held", "500m")
	held.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: "example.com/gate"}}

	snapshot := Snapshot{
		GeneratedAt: capacityTestTime(),
		NodePools:   []*unstructured.Unstructured{readyPool, notReadyPool, unobservedPool},
		NodeClasses: map[string]*unstructured.Unstructured{
			NodeClassLookupKey("karpenter.k8s.aws", "EC2NodeClass", "ready-class"):     capacityTestNodeClass("ready-class", corev1.ConditionTrue),
			NodeClassLookupKey("karpenter.k8s.aws", "EC2NodeClass", "not-ready-class"): capacityTestNodeClass("not-ready-class", corev1.ConditionFalse),
		},
		Pods:     []*corev1.Pod{pod, held},
		Coverage: capacityTestCoverage(),
	}
	model := Build(snapshot)
	for _, name := range []string{"class-ready", "class-not-ready", "class-unobserved"} {
		workloads := capacityTestMustPool(t, model, name).Observation.Workloads
		if workloads == nil || workloads.PendingEligibleGroupCount != 0 {
			t.Fatalf("Build() eagerly populated pool %q pending eligibility: %+v", name, workloads)
		}
	}

	AttachPendingEligibilityForPool(&model, snapshot, "class-ready")
	AttachPendingEligibilityForPool(&model, snapshot, "class-not-ready")
	AttachPendingEligibilityForPool(&model, snapshot, "class-unobserved")

	ready := capacityTestMustPool(t, model, "class-ready").Observation.Workloads
	if ready == nil || ready.PendingEligibleGroupCount != 1 {
		t.Fatalf("ready NodeClass pending eligibility = %+v, want one group", ready)
	}
	for _, name := range []string{"class-not-ready", "class-unobserved"} {
		workloads := capacityTestMustPool(t, model, name).Observation.Workloads
		if workloads == nil || workloads.PendingEligibleGroupCount != 0 {
			t.Errorf("pool %q pending eligibility = %+v, want none", name, workloads)
		}
	}
}

func TestBuildDoesNotTrustStaleNodeClassReadyCondition(t *testing.T) {
	pool := capacityTestPoolWithNodeClass("compute", "default")
	nodeClass := capacityTestNodeClass("default", corev1.ConditionTrue)
	nodeClass.SetGeneration(2)
	conditions := nodeClass.Object["status"].(map[string]any)["conditions"].([]any)
	conditions[0].(map[string]any)["observedGeneration"] = int64(1)

	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(),
		NodePools:   []*unstructured.Unstructured{pool},
		NodeClasses: map[string]*unstructured.Unstructured{
			NodeClassLookupKey("karpenter.k8s.aws", "EC2NodeClass", "default"): nodeClass,
		},
		Coverage: capacityTestCoverage(),
	})

	observation := capacityTestMustPool(t, model, "compute").Observation
	if observation.NodeClass == nil || observation.NodeClass.Resource == nil {
		t.Fatalf("NodeClass observation = %+v, want the observed resource", observation.NodeClass)
	}
	if observation.NodeClass.Ready != nil {
		t.Fatalf("stale NodeClass Ready = %v, want unknown", *observation.NodeClass.Ready)
	}
	for _, fact := range observation.Facts {
		if fact.Code == "nodeclass_not_ready" {
			t.Fatalf("stale Ready=True emitted a not-ready fact: %+v", fact)
		}
	}
}

func TestBuildKeepsMissingNodeClassAsObservationWithoutDuplicatingIssueFact(t *testing.T) {
	pool := capacityTestPoolWithNodeClass("compute", "default")
	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(),
		NodePools:   []*unstructured.Unstructured{pool},
		Coverage:    capacityTestCoverage(),
	})

	observation := capacityTestMustPool(t, model, "compute").Observation
	if observation.NodeClass == nil || observation.NodeClass.Reference.Kind != "EC2NodeClass" || observation.NodeClass.Reference.Name != "default" {
		t.Fatalf("NodeClass observation = %+v, want the unresolved reference", observation.NodeClass)
	}
	for _, fact := range observation.Facts {
		if fact.Code == "nodeclass_kind_not_installed" {
			t.Fatalf("missing NodeClass API duplicated the canonical Issue as a posture fact: %+v", fact)
		}
	}
}

func TestBuildActualUsageUsesOnlyCoveredAllocatable(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)
	nodes := []*corev1.Node{
		capacityTestNode("node-a", "node-a-uid", "", "compute", resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "4", corev1.ResourceMemory: "8Gi"})),
		capacityTestNode("node-b", "node-b-uid", "", "compute", resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "8", corev1.ResourceMemory: "16Gi"})),
		capacityTestNode("node-c", "node-c-uid", "", "compute", resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "16", corev1.ResourceMemory: "32Gi"})),
	}
	firstSample := capacityTestTime().Add(-10 * time.Minute)
	latestSample := capacityTestTime().Add(-5 * time.Minute)

	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(),
		NodePools:   []*unstructured.Unstructured{pool},
		Nodes:       nodes,
		NodeUsage: map[string]NodeUsageSample{
			"node-a": {Resources: resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "1", corev1.ResourceMemory: "2Gi"}), ObservedAt: firstSample},
			"node-b": {Resources: resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "4", corev1.ResourceMemory: "8Gi"}), ObservedAt: latestSample},
			"other":  {Resources: resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "100"}), ObservedAt: capacityTestTime()},
		},
		Coverage: capacityTestCoverage(),
	})
	poolModel := capacityTestMustPool(t, model, "compute")
	usage := poolModel.Observation.Ledger.ActualUsage
	if usage == nil {
		t.Fatal("ActualUsage is nil with covered nodes")
	}
	if usage.CoveredNodes != 2 || usage.TotalNodes != 3 {
		t.Errorf("metrics coverage = %d/%d nodes, want 2/3", usage.CoveredNodes, usage.TotalNodes)
	}
	// Only 2 of 3 nodes had samples — pool usage is a lower bound, never "=".
	capacityTestAssertObservation(t, &usage.Quantity, capacityapi.CertaintyLowerBound, capacityapi.GranularityAggregate, map[string]string{
		"cpu": "5", "memory": "10Gi",
	})
	capacityTestAssertObservation(t, &usage.CoveredAllocatable, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{
		"cpu": "12", "memory": "24Gi",
	})
	if !usage.Quantity.AsOf.Equal(firstSample) || !usage.CoveredAllocatable.AsOf.Equal(firstSample) {
		t.Errorf("aggregate metrics asOf = %s/%s, want oldest contributing sample %s", usage.Quantity.AsOf, usage.CoveredAllocatable.AsOf, firstSample)
	}
	wantNodeAsOf := map[string]time.Time{"node-a": firstSample, "node-b": latestSample}
	checkedNodes := 0
	for _, member := range poolModel.Nodes {
		want, covered := wantNodeAsOf[member.Resource.Ref.Name]
		if !covered {
			continue
		}
		checkedNodes++
		if member.Node == nil || member.Node.ActualUsage == nil {
			t.Errorf("node %q actual usage is nil", member.Resource.Ref.Name)
			continue
		}
		if !member.Node.ActualUsage.Quantity.AsOf.Equal(want) {
			t.Errorf("node %q metrics asOf = %s, want exact sample %s", member.Resource.Ref.Name, member.Node.ActualUsage.Quantity.AsOf, want)
		}
	}
	if checkedNodes != len(wantNodeAsOf) {
		t.Errorf("checked per-node sample timestamps for %d nodes, want %d", checkedNodes, len(wantNodeAsOf))
	}
	gotCPU := capacityTestUtilization(t, usage, "cpu")
	if math.Abs(gotCPU.Percent-(5.0/12.0*100)) > 0.0001 || gotCPU.Allocatable != "12" {
		t.Errorf("CPU utilization = %+v, want 5/12 of covered allocatable", gotCPU)
	}
}

func TestBuildClaimLifecycleStagesAndInFlightCapacity(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)
	claims := []*unstructured.Unstructured{
		capacityTestClaimWithConditions("pending", pool, nil),
		capacityTestClaimWithConditions("launched", pool, []map[string]any{{"type": "Launched", "status": "True"}}),
		capacityTestClaimWithConditions("registered", pool, []map[string]any{{"type": "Launched", "status": "True"}, {"type": "Registered", "status": "True"}}),
		capacityTestClaimWithConditions("initialized", pool, []map[string]any{{"type": "Initialized", "status": "True"}}),
		capacityTestClaimWithConditions("ready", pool, []map[string]any{{"type": "Ready", "status": "True"}}),
		// Real Karpenter keeps a failed launch at status=Unknown with a failure
		// reason; status=False is reserved for hard invariant failures like
		// Registered=False/MultipleNodesFound. Both shapes must read as Failed,
		// while Unknown with an in-progress reason (NodeNotFound) must not.
		capacityTestClaimWithConditions("failed", pool, []map[string]any{{"type": "Launched", "status": "Unknown", "reason": "LaunchFailed"}}),
		capacityTestClaimWithConditions("multiple-nodes", pool, []map[string]any{{"type": "Launched", "status": "True"}, {"type": "Registered", "status": "False", "reason": "MultipleNodesFound"}}),
		capacityTestClaimWithConditions("stuck-registering", pool, []map[string]any{{"type": "Launched", "status": "True"}, {"type": "Registered", "status": "Unknown", "reason": "NodeNotFound"}}),
		capacityTestClaimWithConditions("ready-with-unrelated-error", pool, []map[string]any{{"type": "Ready", "status": "True"}, {"type": "Consistency", "status": "False", "reason": "ReconciliationError"}}),
		capacityTestClaimWithConditions("terminating", pool, []map[string]any{{"type": "Ready", "status": "True"}}),
	}
	deletedAt := metav1.NewTime(capacityTestTime().Add(-time.Minute))
	claims[len(claims)-1].SetDeletionTimestamp(&deletedAt)
	for _, claim := range claims {
		status := claim.Object["status"].(map[string]any)
		status["capacity"] = map[string]any{"cpu": "1", "example.com/fpga": "1"}
	}
	claims[2].Object["status"].(map[string]any)["nodeName"] = "registered-node"
	claims[3].Object["status"].(map[string]any)["nodeName"] = "initialized-node"

	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(), NodePools: []*unstructured.Unstructured{pool}, NodeClaims: claims,
		Nodes: []*corev1.Node{
			capacityTestNode("registered-node", "registered-node-uid", "", "compute", resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "1"})),
			capacityTestNode("initialized-node", "initialized-node-uid", "", "compute", resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "1"})),
		},
		Coverage: capacityTestCoverage(),
	})
	got := capacityTestMustPool(t, model, "compute")
	stages := map[string]capacityapi.ClaimStage{}
	for _, member := range got.Claims {
		stages[member.Resource.Ref.Name] = member.Claim.Stage
	}
	wantStages := map[string]capacityapi.ClaimStage{
		"pending": capacityapi.ClaimStagePending, "launched": capacityapi.ClaimStageLaunched,
		"registered": capacityapi.ClaimStageRegistered, "initialized": capacityapi.ClaimStageInitialized,
		"ready": capacityapi.ClaimStageReady, "failed": capacityapi.ClaimStageFailed,
		"multiple-nodes": capacityapi.ClaimStageFailed, "stuck-registering": capacityapi.ClaimStageLaunched,
		"ready-with-unrelated-error": capacityapi.ClaimStageReady, "terminating": capacityapi.ClaimStageTerminating,
	}
	for name, want := range wantStages {
		if stages[name] != want {
			t.Errorf("claim %q stage = %q, want %q", name, stages[name], want)
		}
	}
	summary := got.Observation.Claims
	if summary == nil {
		t.Fatal("claim lifecycle summary is nil with available claim coverage")
	}
	if summary.Total != 10 || summary.Pending != 1 || summary.Launched != 2 || summary.Registered != 1 || summary.Initialized != 1 || summary.Ready != 2 || summary.Failed != 2 || summary.Terminating != 1 {
		t.Errorf("claim lifecycle summary = %+v", summary)
	}
	capacityTestAssertObservation(t, got.Observation.Ledger.InFlightCapacity, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{
		"cpu": "3", "example.com/fpga": "3",
	})
}

func TestBuildPartialInFlightClaimCapacityIsLowerBound(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)
	observed := capacityTestClaimWithConditions("observed", pool, nil)
	observed.Object["status"].(map[string]any)["capacity"] = map[string]any{"cpu": "2"}
	unobserved := capacityTestClaimWithConditions("unobserved", pool, nil)

	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(),
		NodePools:   []*unstructured.Unstructured{pool},
		NodeClaims:  []*unstructured.Unstructured{observed, unobserved},
		Coverage:    capacityTestCoverage(),
	})

	ledger := capacityTestMustPool(t, model, "compute").Observation.Ledger
	capacityTestAssertObservation(t, ledger.InFlightCapacity, capacityapi.CertaintyLowerBound, capacityapi.GranularityAggregate, map[string]string{"cpu": "2"})
}

func TestBuildLedgerOmitsPodDerivedValuesWhenPodsUnobserved(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)
	node := capacityTestNode("pooled", "pooled-uid", "", "compute", resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "4"}))
	coverage := capacityTestCoverage()
	coverage[capacityapi.CoveragePods] = capacityapi.NewSourceCoverage(capacityapi.CoverageUnavailable, capacityapi.CoverageScopeCluster)

	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(),
		NodePools:   []*unstructured.Unstructured{pool},
		Nodes:       []*corev1.Node{node},
		Coverage:    coverage,
	})
	poolModel := capacityTestMustPool(t, model, "compute")
	ledger := poolModel.Observation.Ledger
	// Requests from an unobserved pod source would be an empty vector, and
	// allocatable minus that empty vector reads as a fully free pool —
	// fabricated headroom. Both must be absent, never zero.
	if ledger.ScheduledRequests != nil || ledger.AggregateUnallocatedRequests != nil {
		t.Fatalf("pods-unobserved ledger emitted requests=%#v unallocated=%#v, want both absent",
			ledger.ScheduledRequests, ledger.AggregateUnallocatedRequests)
	}
	capacityTestAssertObservation(t, ledger.Allocatable, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{"cpu": "4"})
	// Same contract per node member: no pod source, no per-node requests.
	member := capacityTestMember(t, poolModel.Nodes, "pooled")
	if member.Node == nil || member.Node.ScheduledRequests != nil || member.Node.PodCount != nil {
		t.Fatalf("pods-unobserved node member = %#v, want absent requests and pod count", member.Node)
	}
}

func TestClaimStageRespectsConditionDialects(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)

	// v1beta1 used False for ordinary unmet stages — a claim still
	// provisioning must not read as failed.
	inProgress := capacityTestClaimWithConditions("in-progress", pool, []map[string]any{
		{"type": "Launched", "status": "True"},
		{"type": "Initialized", "status": "False", "reason": "NotInitialized"},
		{"type": "Ready", "status": "False", "reason": "NotInitialized"},
	})
	inProgress.Object["apiVersion"] = karpenter.APIVersionV1Beta1
	if stage := claimStage(inProgress); stage != capacityapi.ClaimStageLaunched {
		t.Fatalf("v1beta1 in-progress claim stage = %q, want launched", stage)
	}

	// A v1beta1 failure still carries failure vocabulary.
	failed := capacityTestClaimWithConditions("failed", pool, []map[string]any{
		{"type": "Launched", "status": "False", "reason": "LaunchFailed"},
	})
	failed.Object["apiVersion"] = karpenter.APIVersionV1Beta1
	if stage := claimStage(failed); stage != capacityapi.ClaimStageFailed {
		t.Fatalf("v1beta1 failed claim stage = %q, want failed", stage)
	}

	// v1 reserves False for hard invariants — no failure text needed.
	invariant := capacityTestClaimWithConditions("invariant", pool, []map[string]any{
		{"type": "Registered", "status": "False", "reason": "MultipleNodesFound"},
	})
	if stage := claimStage(invariant); stage != capacityapi.ClaimStageFailed {
		t.Fatalf("v1 invariant claim stage = %q, want failed", stage)
	}

	// The load-bearing v1 shape: a failing stage stays at Unknown carrying the
	// cloud provider's reason verbatim. Reading it as "still launching" is what
	// makes a dead fleet look busy.
	for _, reason := range []string{"InsufficientInstanceCapacity", "VCPULimitExceeded", "SpotQuotaExceeded", "AMINotFound"} {
		claim := capacityTestClaimWithConditions("cloud-"+reason, pool, []map[string]any{
			{"type": "Launched", "status": "Unknown", "reason": reason},
		})
		if stage := claimStage(claim); stage != capacityapi.ClaimStageFailed {
			t.Fatalf("Launched=Unknown/%s claim stage = %q, want failed", reason, stage)
		}
	}
}

func TestBuildClusterSchedulingAggregateScopesToPooledNodes(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)
	ghostPool := capacityTestPool("ghost", "ghost-uid", nil, nil)
	inFlight := capacityTestClaimWithConditions("launching", pool, []map[string]any{{"type": "Launched", "status": "True"}})
	inFlight.Object["status"].(map[string]any)["capacity"] = map[string]any{"cpu": "2"}
	// References a pool absent from the snapshot — orphaned, so it must count
	// in the claim-stage rollup but never in pooled in-flight capacity.
	orphan := capacityTestClaimWithConditions("orphan", ghostPool, []map[string]any{{"type": "Launched", "status": "True"}})
	orphan.Object["status"].(map[string]any)["capacity"] = map[string]any{"cpu": "64"}

	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(),
		NodePools:   []*unstructured.Unstructured{pool},
		NodeClaims:  []*unstructured.Unstructured{inFlight, orphan},
		Nodes: []*corev1.Node{
			capacityTestNode("pooled", "pooled-uid", "", "compute", resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "4"})),
			capacityTestNode("unpooled", "unpooled-uid", "", "", resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "32"})),
		},
		Pods: []*corev1.Pod{
			capacityTestPod("worker", "pooled", "worker", map[corev1.ResourceName]string{corev1.ResourceCPU: "500m"}),
			capacityTestPod("outsider", "unpooled", "outsider", map[corev1.ResourceName]string{corev1.ResourceCPU: "8"}),
		},
		Coverage: capacityTestCoverage(),
	})

	if model.Scheduling == nil {
		t.Fatal("cluster scheduling aggregate is nil with observed sources")
	}
	capacityTestAssertObservation(t, model.Scheduling.Allocatable, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{"cpu": "4"})
	capacityTestAssertObservation(t, model.Scheduling.ScheduledRequests, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{"cpu": "500m", "pods": "1"})
	capacityTestAssertObservation(t, model.Scheduling.InFlightCapacity, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{"cpu": "2"})
	if model.ClaimStages == nil || model.ClaimStages.Total != 2 || model.ClaimStages.Launched != 2 {
		t.Fatalf("claim stage rollup = %+v, want total 2 launched 2 (orphan included)", model.ClaimStages)
	}
}

func TestBuildClusterSchedulingAggregateGatesOnCoverage(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)
	node := capacityTestNode("pooled", "pooled-uid", "", "compute", resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "4"}))
	pod := capacityTestPod("worker", "pooled", "worker", map[corev1.ResourceName]string{corev1.ResourceCPU: "500m"})

	coverage := capacityTestCoverage()
	coverage[capacityapi.CoveragePods] = capacityapi.NewSourceCoverage(capacityapi.CoverageDenied, capacityapi.CoverageScopeCluster)
	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(), NodePools: []*unstructured.Unstructured{pool},
		Nodes: []*corev1.Node{node}, Coverage: coverage,
	})
	if model.Scheduling == nil || model.Scheduling.ScheduledRequests != nil {
		t.Fatalf("pods-denied aggregate = %+v, want allocatable only (absent requests, never zero)", model.Scheduling)
	}
	capacityTestAssertObservation(t, model.Scheduling.Allocatable, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{"cpu": "4"})

	coverage = capacityTestCoverage()
	coverage[capacityapi.CoverageNodes] = capacityapi.NewSourceCoverage(capacityapi.CoverageUnavailable, capacityapi.CoverageScopeCluster)
	coverage[capacityapi.CoverageNodeClaims] = capacityapi.NewSourceCoverage(capacityapi.CoverageDenied, capacityapi.CoverageScopeCluster)
	model = Build(Snapshot{
		GeneratedAt: capacityTestTime(), NodePools: []*unstructured.Unstructured{pool},
		Pods: []*corev1.Pod{pod}, Coverage: coverage,
	})
	if model.Scheduling == nil || model.Scheduling.Allocatable != nil || model.Scheduling.InFlightCapacity != nil {
		t.Fatalf("nodes-unavailable aggregate = %+v, want requests only", model.Scheduling)
	}
	if model.ClaimStages != nil {
		t.Fatalf("claims-denied stage rollup = %+v, want nil (unavailable is not zero claims)", model.ClaimStages)
	}
	// The pod cannot be attributed to a pool without nodes or claims — pooled
	// requests degrade to an unknown-certainty empty vector, never a made-up
	// exact zero.
	capacityTestAssertObservation(t, model.Scheduling.ScheduledRequests, capacityapi.CertaintyUnknown, capacityapi.GranularityAggregate, map[string]string{})

	model = Build(Snapshot{GeneratedAt: capacityTestTime(), NodePools: []*unstructured.Unstructured{pool}, Coverage: capacityapi.CoverageBySource{}})
	if model.Scheduling != nil {
		t.Fatalf("no-coverage aggregate = %+v, want nil", model.Scheduling)
	}
}

func TestBuildClusterSchedulingAggregateSplitsNegativePriorityRequests(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)
	node := capacityTestNode("pooled", "pooled-uid", "", "compute", resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "8"}))

	t.Run("mixed pods yield a strict subset", func(t *testing.T) {
		model := Build(Snapshot{
			GeneratedAt: capacityTestTime(),
			NodePools:   []*unstructured.Unstructured{pool},
			Nodes:       []*corev1.Node{node},
			Pods: []*corev1.Pod{
				capacityTestPod("normal", "pooled", "web", map[corev1.ResourceName]string{corev1.ResourceCPU: "1"}),
				withPriority(capacityTestPod("victim", "pooled", "batch", map[corev1.ResourceName]string{corev1.ResourceCPU: "2"}), -10),
			},
			Coverage: capacityTestCoverage(),
		})
		if model.Scheduling == nil {
			t.Fatal("scheduling aggregate is nil")
		}
		capacityTestAssertObservation(t, model.Scheduling.ScheduledRequests, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{"cpu": "3", "pods": "2"})
		capacityTestAssertObservation(t, model.Scheduling.NegativePriorityRequests, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{"cpu": "2", "pods": "1"})
		capacityTestAssertSubset(t, model.Scheduling.NegativePriorityRequests, model.Scheduling.ScheduledRequests, true)
	})

	t.Run("nil-priority-only pods leave the field absent", func(t *testing.T) {
		model := Build(Snapshot{
			GeneratedAt: capacityTestTime(),
			NodePools:   []*unstructured.Unstructured{pool},
			Nodes:       []*corev1.Node{node},
			Pods: []*corev1.Pod{
				capacityTestPod("normal", "pooled", "web", map[corev1.ResourceName]string{corev1.ResourceCPU: "1"}),
			},
			Coverage: capacityTestCoverage(),
		})
		if model.Scheduling == nil || model.Scheduling.NegativePriorityRequests != nil {
			t.Fatalf("negative-priority requests = %+v, want absent (no negative-priority pods)", model.Scheduling)
		}
	})

	t.Run("pods unobserved gates the field off", func(t *testing.T) {
		coverage := capacityTestCoverage()
		coverage[capacityapi.CoveragePods] = capacityapi.NewSourceCoverage(capacityapi.CoverageDenied, capacityapi.CoverageScopeCluster)
		model := Build(Snapshot{
			GeneratedAt: capacityTestTime(),
			NodePools:   []*unstructured.Unstructured{pool},
			Nodes:       []*corev1.Node{node},
			Pods: []*corev1.Pod{
				withPriority(capacityTestPod("victim", "pooled", "batch", map[corev1.ResourceName]string{corev1.ResourceCPU: "2"}), -10),
			},
			Coverage: coverage,
		})
		if model.Scheduling == nil {
			t.Fatal("scheduling aggregate is nil (allocatable should keep it present)")
		}
		if model.Scheduling.ScheduledRequests != nil || model.Scheduling.NegativePriorityRequests != nil {
			t.Fatalf("pods-denied aggregate = %+v, want requests and negative-priority both absent", model.Scheduling)
		}
	})

	t.Run("negative-priority-only pods make the subset equal the total", func(t *testing.T) {
		model := Build(Snapshot{
			GeneratedAt: capacityTestTime(),
			NodePools:   []*unstructured.Unstructured{pool},
			Nodes:       []*corev1.Node{node},
			Pods: []*corev1.Pod{
				withPriority(capacityTestPod("victim-a", "pooled", "batch", map[corev1.ResourceName]string{corev1.ResourceCPU: "2"}), -10),
				withPriority(capacityTestPod("victim-b", "pooled", "batch", map[corev1.ResourceName]string{corev1.ResourceCPU: "500m"}), -1),
			},
			Coverage: capacityTestCoverage(),
		})
		if model.Scheduling == nil {
			t.Fatal("scheduling aggregate is nil")
		}
		capacityTestAssertObservation(t, model.Scheduling.NegativePriorityRequests, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{"cpu": "2500m", "pods": "2"})
		// Every scheduled pod is negative priority, so the subset is not strict.
		capacityTestAssertSubset(t, model.Scheduling.NegativePriorityRequests, model.Scheduling.ScheduledRequests, false)
		capacityTestAssertObservation(t, model.Scheduling.ScheduledRequests, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{"cpu": "2500m", "pods": "2"})
	})
}

func TestBuildAttributesScheduledPodsThroughVisibleClaimsWhenNodesAreDenied(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)
	claim := capacityTestClaimWithConditions("registered", pool, []map[string]any{
		{"type": "Launched", "status": "True"},
		{"type": "Registered", "status": "True"},
	})
	claim.Object["status"].(map[string]any)["nodeName"] = "hidden-node"
	claim.Object["status"].(map[string]any)["capacity"] = map[string]any{"cpu": "2"}
	pod := capacityTestPod("worker", "hidden-node", "worker", map[corev1.ResourceName]string{corev1.ResourceCPU: "750m"})
	coverage := capacityTestCoverage()
	coverage[capacityapi.CoverageNodes] = capacityapi.NewSourceCoverage(capacityapi.CoverageDenied, capacityapi.CoverageScopeCluster)

	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(),
		NodePools:   []*unstructured.Unstructured{pool},
		NodeClaims:  []*unstructured.Unstructured{claim},
		Pods:        []*corev1.Pod{pod},
		Coverage:    coverage,
	})
	got := capacityTestMustPool(t, model, "compute")
	capacityTestAssertObservation(t, got.Observation.Ledger.ScheduledRequests, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{"cpu": "750m", "pods": "1"})
	if len(got.Workloads) != 1 || got.Workloads[0].Workload == nil || got.Workloads[0].Workload.PodCount != 1 {
		t.Fatalf("claim-attributed workloads = %#v", got.Workloads)
	}
	if got.Observation.Ledger.InFlightCapacity != nil {
		t.Fatalf("registered claim with status.nodeName counted in flight: %#v", got.Observation.Ledger.InFlightCapacity)
	}
	// With Nodes denied, allocatable and allocatable−requests must be absent —
	// emitting the difference here would fabricate a negative "unallocated".
	if got.Observation.Ledger.Allocatable != nil || got.Observation.Ledger.AggregateUnallocatedRequests != nil {
		t.Fatalf("nodes-denied ledger emitted allocatable=%#v unallocated=%#v, want both absent",
			got.Observation.Ledger.Allocatable, got.Observation.Ledger.AggregateUnallocatedRequests)
	}
	claimMember := capacityTestMember(t, got.Claims, "registered")
	if claimMember.Claim == nil || claimMember.Claim.Node != nil || claimMember.Claim.NodeName != "hidden-node" {
		t.Fatalf("claim member = %#v, want status node name without fabricated Node identity", claimMember.Claim)
	}
}

func TestBuildBoundsTopWorkloadsAndAggregatesComposition(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)
	nodes := []*corev1.Node{
		capacityTestNodeWithComposition("node-a", "compute", "spot", "m7g.large", "zone-a", "arm64", "image-a"),
		capacityTestNodeWithComposition("node-b", "compute", "spot", "m7g.large", "zone-a", "arm64", "image-a"),
		capacityTestNodeWithComposition("node-c", "compute", "on-demand", "c7i.large", "zone-b", "amd64", "image-b"),
	}
	pods := []*corev1.Pod{}
	for owner := 1; owner <= 12; owner++ {
		for replica := 0; replica < owner; replica++ {
			pod := capacityTestPod(
				fmt.Sprintf("pod-%02d-%02d", owner, replica),
				nodes[replica%len(nodes)].Name,
				fmt.Sprintf("owner-%02d", owner),
				map[corev1.ResourceName]string{corev1.ResourceCPU: "100m", "example.com/fpga": "1"},
			)
			pods = append(pods, pod)
		}
	}

	model := Build(Snapshot{GeneratedAt: capacityTestTime(), NodePools: []*unstructured.Unstructured{pool}, Nodes: nodes, Pods: pods, Coverage: capacityTestCoverage()})
	got := capacityTestMustPool(t, model, "compute")
	workloads := got.Observation.Workloads
	if workloads.WorkloadCount != 12 || len(got.Workloads) != 12 {
		t.Errorf("workload totals = %d/%d, want all 12 detail groups", workloads.WorkloadCount, len(got.Workloads))
	}
	if workloads.TopScheduledMeta != (capacityapi.BoundedResultMeta{Total: 12, Returned: 10, Truncated: true}) || len(workloads.TopScheduled) != 10 {
		t.Errorf("top workload bound = %+v with %d rows", workloads.TopScheduledMeta, len(workloads.TopScheduled))
	}
	for i, workload := range workloads.TopScheduled {
		wantCount := 12 - i
		if workload.PodCount != wantCount {
			t.Errorf("top workload %d pod count = %d, want %d", i, workload.PodCount, wantCount)
		}
	}
	capacityTestAssertBuckets(t, "capacity types", got.Observation.Composition.CapacityTypes, []capacityapi.CompositionBucket{{Value: "spot", Count: 2}, {Value: "on-demand", Count: 1}})
	capacityTestAssertBuckets(t, "instance types", got.Observation.Composition.InstanceTypes, []capacityapi.CompositionBucket{{Value: "m7g.large", Count: 2}, {Value: "c7i.large", Count: 1}})
	capacityTestAssertBuckets(t, "zones", got.Observation.Composition.Zones, []capacityapi.CompositionBucket{{Value: "zone-a", Count: 2}, {Value: "zone-b", Count: 1}})
}

func TestBuildBoundsCompositionBuckets(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)
	nodes := make([]*corev1.Node, 0, defaultCompositionBucketLimit+2)
	for index := 0; index < defaultCompositionBucketLimit+2; index++ {
		nodes = append(nodes, capacityTestNodeWithComposition(
			fmt.Sprintf("node-%02d", index),
			"compute",
			"spot",
			fmt.Sprintf("instance-%02d", index),
			"zone-a",
			"amd64",
			"image-a",
		))
	}

	model := Build(Snapshot{
		GeneratedAt: capacityTestTime(),
		NodePools:   []*unstructured.Unstructured{pool},
		Nodes:       nodes,
		Coverage:    capacityTestCoverage(),
	})
	composition := capacityTestMustPool(t, model, "compute").Observation.Composition
	if composition == nil {
		t.Fatal("composition is nil with observed nodes")
	}
	wantMeta := capacityapi.BoundedResultMeta{Total: defaultCompositionBucketLimit + 2, Returned: defaultCompositionBucketLimit, Truncated: true}
	if composition.InstanceTypesMeta != wantMeta || len(composition.InstanceTypes) != defaultCompositionBucketLimit {
		t.Fatalf("instance type buckets = %+v with %d rows, want %+v", composition.InstanceTypesMeta, len(composition.InstanceTypes), wantMeta)
	}
	for index, bucket := range composition.InstanceTypes {
		want := fmt.Sprintf("instance-%02d", index)
		if bucket.Value != want || bucket.Count != 1 {
			t.Errorf("instance type bucket %d = %+v, want %q with count 1", index, bucket, want)
		}
	}
	if composition.CapacityTypesMeta != (capacityapi.BoundedResultMeta{Total: 1, Returned: 1}) {
		t.Errorf("untruncated capacity type meta = %+v, want total/returned 1", composition.CapacityTypesMeta)
	}
}

func TestBuildBoundsWorkloadNodeReferences(t *testing.T) {
	pool := capacityTestPool("compute", "pool-uid", nil, nil)
	nodes := make([]*corev1.Node, 0, defaultWorkloadNodeLimit+1)
	pods := make([]*corev1.Pod, 0, defaultWorkloadNodeLimit+1)
	for index := 0; index <= defaultWorkloadNodeLimit; index++ {
		name := fmt.Sprintf("node-%02d", index)
		nodes = append(nodes, capacityTestNodeWithComposition(name, "compute", "spot", "m7g.large", "zone-a", "arm64", "image-a"))
		pods = append(pods, capacityTestPod(fmt.Sprintf("pod-%02d", index), name, "owner", map[corev1.ResourceName]string{corev1.ResourceCPU: "100m"}))
	}

	model := Build(Snapshot{GeneratedAt: capacityTestTime(), NodePools: []*unstructured.Unstructured{pool}, Nodes: nodes, Pods: pods, Coverage: capacityTestCoverage()})
	workload := capacityTestMustPool(t, model, "compute").Workloads[0].Workload
	wantMeta := capacityapi.BoundedResultMeta{Total: defaultWorkloadNodeLimit + 1, Returned: defaultWorkloadNodeLimit, Truncated: true}
	if workload.NodesMeta != wantMeta || len(workload.Nodes) != defaultWorkloadNodeLimit {
		t.Fatalf("node refs = %+v with %d rows, want %+v", workload.NodesMeta, len(workload.Nodes), wantMeta)
	}
}

func capacityTestTime() time.Time {
	return time.Date(2026, time.July, 13, 8, 0, 0, 0, time.UTC)
}

// capacityTestPartialCoverage mirrors real partial producers, which always
// carry an item count — partial without one means "nothing was listed" and is
// deliberately treated as unobserved.
func capacityTestPartialCoverage(scope capacityapi.CoverageScope, itemCount int) capacityapi.SourceCoverage {
	coverage := capacityapi.NewSourceCoverage(capacityapi.CoveragePartial, scope)
	coverage.ItemCount = &itemCount
	return coverage
}

func capacityTestCoverage() capacityapi.CoverageBySource {
	coverage := capacityapi.CoverageBySource{}
	for _, source := range []capacityapi.CoverageSource{capacityapi.CoverageNodePools, capacityapi.CoverageNodeClaims, capacityapi.CoverageNodeClasses, capacityapi.CoverageNodes, capacityapi.CoveragePods, capacityapi.CoverageWorkloads, capacityapi.CoverageNodeMetrics} {
		coverage[source] = capacityapi.NewSourceCoverage(capacityapi.CoverageAvailable, capacityapi.CoverageScopeCluster)
	}
	return coverage
}

func capacityTestPoolWithNodeClass(name, nodeClassName string) *unstructured.Unstructured {
	pool := capacityTestPool(name, name+"-uid", map[string]any{
		"template": map[string]any{"spec": map[string]any{
			"nodeClassRef": map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": nodeClassName},
		}},
	}, map[string]any{
		"conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(1)}},
	})
	pool.SetGeneration(1)
	return pool
}

func capacityTestNodeClass(name string, ready corev1.ConditionStatus) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.k8s.aws/v1",
		"kind":       "EC2NodeClass",
		"metadata":   map[string]any{"name": name, "uid": name + "-uid"},
		"status":     map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": string(ready)}}},
	}}
}

func capacityTestPool(name, uid string, spec, status map[string]any) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": karpenter.APIVersionV1,
		"kind":       karpenter.NodePoolKind,
		"metadata":   map[string]any{"name": name, "uid": uid},
	}
	if spec != nil {
		object["spec"] = spec
	}
	if status != nil {
		object["status"] = status
	}
	return &unstructured.Unstructured{Object: object}
}

func capacityTestClaim(name, uid string, pool *unstructured.Unstructured, status map[string]any) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": karpenter.APIVersionV1,
		"kind":       karpenter.NodeClaimKind,
		"metadata":   map[string]any{"name": name, "uid": uid},
	}
	if status != nil {
		object["status"] = status
	}
	claim := &unstructured.Unstructured{Object: object}
	claim.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: karpenter.APIVersionV1,
		Kind:       karpenter.NodePoolKind,
		Name:       pool.GetName(),
		UID:        pool.GetUID(),
	}})
	return claim
}

func capacityTestClaimWithConditions(name string, pool *unstructured.Unstructured, conditions []map[string]any) *unstructured.Unstructured {
	rawConditions := make([]any, 0, len(conditions))
	for _, condition := range conditions {
		rawConditions = append(rawConditions, condition)
	}
	return capacityTestClaim(name, name+"-uid", pool, map[string]any{"conditions": rawConditions})
}

func capacityTestNode(name, uid, providerID, pool string, allocatable corev1.ResourceList) *corev1.Node {
	labels := map[string]string{}
	if pool != "" {
		labels[karpenter.NodePoolLabelKey] = pool
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid), Labels: labels},
		Spec:       corev1.NodeSpec{ProviderID: providerID},
		Status:     corev1.NodeStatus{Allocatable: allocatable},
	}
}

func capacityTestNodeWithComposition(name, pool, capacityType, instanceType, zone, architecture, image string) *corev1.Node {
	node := capacityTestNode(name, name+"-uid", "", pool, resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "4"}))
	node.Labels[karpenter.CapacityTypeLabelKey] = capacityType
	node.Labels[instanceTypeLabel] = instanceType
	node.Labels[zoneLabel] = zone
	node.Labels[architectureLabel] = architecture
	node.Status.NodeInfo.OSImage = image
	return node
}

func capacityTestPod(name, nodeName, ownerName string, requests map[corev1.ResourceName]string) *corev1.Pod {
	controller := true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "Deployment", Name: ownerName, Controller: &controller,
			}},
		},
		Spec: corev1.PodSpec{
			NodeName:   nodeName,
			Containers: []corev1.Container{containerWithRequests(requests)},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func withPriority(pod *corev1.Pod, priority int32) *corev1.Pod {
	pod.Spec.Priority = &priority
	return pod
}

// capacityTestAssertSubset fails unless every resource in subset is <= the same
// resource in total; strict additionally requires at least one resource to be
// strictly smaller (a proper subset).
func capacityTestAssertSubset(t *testing.T, subset, total *capacityapi.QuantityObservation, strict bool) {
	t.Helper()
	if subset == nil || total == nil {
		t.Fatalf("subset=%v total=%v, both must be present", subset, total)
	}
	anyStrict := false
	for name, raw := range subset.Resources {
		totalRaw, ok := total.Resources[name]
		if !ok {
			t.Fatalf("resource %q in subset but absent from total %+v", name, total.Resources)
		}
		sub := resource.MustParse(raw)
		tot := resource.MustParse(totalRaw)
		switch sub.Cmp(tot) {
		case 1:
			t.Fatalf("resource %q subset %s exceeds total %s", name, raw, totalRaw)
		case -1:
			anyStrict = true
		}
	}
	if strict && !anyStrict {
		t.Fatalf("subset %+v is not strictly smaller than total %+v", subset.Resources, total.Resources)
	}
}

func capacityTestMustPool(t *testing.T, model Model, name string) *PoolModel {
	t.Helper()
	pool, ok := model.Pool(name)
	if !ok {
		t.Fatalf("pool %q not found in model", name)
	}
	return pool
}

func capacityTestMember(t *testing.T, members []capacityapi.PoolMember, name string) capacityapi.PoolMember {
	t.Helper()
	for _, member := range members {
		if member.Resource.Ref.Name == name {
			return member
		}
	}
	t.Fatalf("member %q not found", name)
	return capacityapi.PoolMember{}
}

func capacityTestHasMember(members []capacityapi.PoolMember, name string) bool {
	for _, member := range members {
		if member.Resource.Ref.Name == name {
			return true
		}
	}
	return false
}

func capacityTestAssertObservation(t *testing.T, observation *capacityapi.QuantityObservation, certainty capacityapi.Certainty, granularity capacityapi.Granularity, want map[string]string) {
	t.Helper()
	if observation == nil {
		t.Fatal("quantity observation is nil")
	}
	if observation.Certainty != certainty || observation.Granularity != granularity {
		t.Errorf("observation certainty/granularity = %q/%q, want %q/%q", observation.Certainty, observation.Granularity, certainty, granularity)
	}
	if len(observation.Resources) != len(want) {
		t.Errorf("resources = %v, want exactly %v", observation.Resources, want)
	}
	for name, wantQuantity := range want {
		got, ok := observation.Resources[name]
		if !ok {
			t.Errorf("resource %q missing from %v", name, observation.Resources)
			continue
		}
		gotParsed, gotErr := resource.ParseQuantity(got)
		wantParsed, wantErr := resource.ParseQuantity(wantQuantity)
		if gotErr != nil || wantErr != nil || gotParsed.Cmp(wantParsed) != 0 {
			t.Errorf("resource %q = %q, want %q", name, got, wantQuantity)
		}
	}
}

func capacityTestUtilization(t *testing.T, usage *capacityapi.UsageObservation, name string) capacityapi.ResourceUtilization {
	t.Helper()
	for _, item := range usage.Utilization {
		if item.Resource == name {
			return item
		}
	}
	t.Fatalf("utilization for %q not found", name)
	return capacityapi.ResourceUtilization{}
}

func capacityTestAssertBuckets(t *testing.T, name string, got, want []capacityapi.CompositionBucket) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s buckets = %+v, want %+v", name, got, want)
	}
	for i := range want {
		if got[i].Value != want[i].Value || got[i].Count != want[i].Count {
			t.Errorf("%s bucket %d = %+v, want %+v", name, i, got[i], want[i])
		}
	}
}

func TestAcceleratorFactEchoesDeclaredRequirementsOnly(t *testing.T) {
	gpuCount := capacityapi.Requirement{Key: "karpenter.k8s.aws/instance-gpu-count", Operator: "In", Values: []string{"1", "4"}}
	gpuName := capacityapi.Requirement{Key: "nvidia.com/gpu.product", Operator: "Exists"}
	arch := capacityapi.Requirement{Key: "kubernetes.io/arch", Operator: "In", Values: []string{"amd64"}}

	fact := acceleratorFact([]capacityapi.Requirement{arch, gpuCount, gpuName})
	if fact == nil {
		t.Fatal("a declared GPU requirement must produce the accelerator fact")
	}
	if fact.Code != "declared_accelerator_pool" {
		t.Fatalf("fact.Code = %q", fact.Code)
	}
	// The summary quotes the requirement it found — echo, never inference —
	// so it structurally cannot claim more than the spec declares.
	if fact.Summary != "Declared accelerator pool — karpenter.k8s.aws/instance-gpu-count In [1, 4] (+1 more)" {
		t.Fatalf("fact.Summary = %q", fact.Summary)
	}

	if acceleratorFact([]capacityapi.Requirement{arch}) != nil {
		t.Fatal("an unknown or non-GPU requirement must produce NO fact — graceful null, never a wrong claim")
	}
	if acceleratorFact(nil) != nil {
		t.Fatal("no requirements, no fact")
	}

	azure := acceleratorFact([]capacityapi.Requirement{{Key: "karpenter.azure.com/sku-gpu-count", Operator: "Exists"}})
	if azure == nil || azure.Summary != "Declared accelerator pool — karpenter.azure.com/sku-gpu-count Exists" {
		t.Fatalf("azure fact = %+v", azure)
	}
}

func TestAcceleratorFamilyRecognitionByCloudConvention(t *testing.T) {
	instanceType := func(values ...string) capacityapi.Requirement {
		return capacityapi.Requirement{Key: "node.kubernetes.io/instance-type", Operator: "In", Values: values}
	}

	// The live-fixture shape: GPU-ness declared purely by instance-type names.
	fact := acceleratorFact([]capacityapi.Requirement{instanceType("g5.xlarge", "g5.2xlarge", "p3.2xlarge")})
	if fact == nil || fact.Summary != "Declared accelerator pool — node.kubernetes.io/instance-type In [g5.xlarge, g5.2xlarge, p3.2xlarge]" {
		t.Fatalf("AWS name-pinned pool fact = %+v", fact)
	}

	for _, tc := range []struct {
		name   string
		values []string
		want   bool
	}{
		{"aws mixed gpu and general", []string{"m5.large", "g4dn.xlarge"}, true},
		{"aws general only", []string{"m5.large", "c5.large", "t3.micro", "i3.large", "d3.xlarge"}, false},
		{"aws trainium and inferentia", []string{"trn1.2xlarge", "inf2.xlarge"}, true},
		{"gcp accelerator-optimized", []string{"a2-highgpu-1g"}, true},
		{"gcp l4", []string{"g2-standard-4"}, true},
		{"gcp general", []string{"n2-standard-4", "e2-medium"}, false},
		{"azure n-series", []string{"Standard_NC6s_v3"}, true},
		{"azure general", []string{"Standard_D4s_v5"}, false},
	} {
		got := acceleratorFact([]capacityapi.Requirement{instanceType(tc.values...)}) != nil
		if got != tc.want {
			t.Errorf("%s: fact fired = %v, want %v", tc.name, got, tc.want)
		}
	}

	family := capacityapi.Requirement{Key: "karpenter.k8s.aws/instance-family", Operator: "In", Values: []string{"g5", "p4d"}}
	if acceleratorFact([]capacityapi.Requirement{family}) == nil {
		t.Fatal("instance-family values must be recognized by the same prefix rule")
	}
	category := capacityapi.Requirement{Key: "karpenter.k8s.aws/instance-category", Operator: "In", Values: []string{"g"}}
	if acceleratorFact([]capacityapi.Requirement{category}) == nil {
		t.Fatal("instance-category g is AWS's own accelerated-computing category")
	}
	// Only In is inspectable: an exclusion cannot declare what the pool IS.
	notIn := capacityapi.Requirement{Key: "node.kubernetes.io/instance-type", Operator: "NotIn", Values: []string{"g5.xlarge"}}
	if acceleratorFact([]capacityapi.Requirement{notIn}) != nil {
		t.Fatal("NotIn must not fire the fact")
	}
	// Value lists cap at four in the summary.
	long := acceleratorFact([]capacityapi.Requirement{instanceType("g5.xlarge", "g5.2xlarge", "g5.4xlarge", "g5.8xlarge", "g5.12xlarge", "g5.16xlarge")})
	if long == nil || long.Summary != "Declared accelerator pool — node.kubernetes.io/instance-type In [g5.xlarge, g5.2xlarge, g5.4xlarge, g5.8xlarge, +2 more]" {
		t.Fatalf("capped summary = %+v", long)
	}
}
