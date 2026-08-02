package capacity

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/autoscalerstatus"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestGroupIdentityRecognizesPlatformDomains(t *testing.T) {
	cases := map[string]struct {
		labels map[string]string
		wantID string
	}{
		"kops":     {map[string]string{labelKopsInstanceGroup: "nodes-us-east-1a"}, "kops-instancegroup/nodes-us-east-1a"},
		"doks":     {map[string]string{labelDOKSNodePool: "pool-web"}, "doks-nodepool/pool-web"},
		"ack":      {map[string]string{labelACKNodePoolID: "np-abc123"}, "ack-nodepool/np-abc123"},
		"lke":      {map[string]string{labelLKEPoolID: "12345"}, "lke-pool/12345"},
		"scaleway": {map[string]string{labelScalewayPoolName: "default"}, "scaleway-pool/default"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			id, _, _, ok := groupIdentityForNode(groupsTestNode("node-1", testCase.labels))
			if !ok || id != testCase.wantID {
				t.Fatalf("identity = %q ok=%v, want %q", id, ok, testCase.wantID)
			}
		})
	}
}

func TestGroupIdentityConflictRules(t *testing.T) {
	karpenterOnGKE := groupsTestNode("n1", map[string]string{
		"karpenter.sh/nodepool": "fast",
		labelGKENodePool:        "platform-pool",
	})
	id, _, domain, ok := groupIdentityForNode(karpenterOnGKE)
	if !ok || id != "karpenter-nodepool/fast" || domain != "karpenter" {
		t.Fatalf("active provisioner must outrank the platform label: %q %q %v", id, domain, ok)
	}

	twoPlatforms := groupsTestNode("n2", map[string]string{
		labelKopsInstanceGroup: "nodes",
		labelDOKSNodePool:      "pool-web",
	})
	if _, _, _, ok := groupIdentityForNode(twoPlatforms); ok {
		t.Fatal("two platform identities on one node is ambiguity — must stay unattributed, never a silent pick")
	}
}

func TestBuildGroupsModelEKSChildJoinsByValidatedNameDerivation(t *testing.T) {
	node := groupsTestNode("ip-10-0-1-5.ec2.internal", map[string]string{labelEKSNodeGroup: "system-pool"})
	shortNode := groupsTestNode("ip-10-0-1-6.ec2.internal", map[string]string{labelEKSNodeGroup: "system"})
	probe := capacityTestTime()
	status := &autoscalerstatus.Status{
		Format:          autoscalerstatus.FormatStructured,
		AutoscalerState: "Running",
		Time:            &probe,
		NodeGroups: []autoscalerstatus.NodeGroup{
			{
				Name:     "eks-system-pool-28cf09b0-ae43-f32a-94c0-27fea5fc8bdc",
				Basename: "eks-system-pool-28cf09b0-ae43-f32a-94c0-27fea5fc8bdc",
				MinSize:  intPtr(2), MaxSize: intPtr(4), Target: intPtr(2),
				Health: autoscalerstatus.Health{Status: "Healthy", Ready: intPtr(2)},
			},
			// A zero-node MNG has no label-evidence group — stays an orphan.
			{
				Name:     "eks-loadtest-od-26cf0a34-4af9-ec18-066e-a8b0142e7d03",
				Basename: "eks-loadtest-od-26cf0a34-4af9-ec18-066e-a8b0142e7d03",
				MinSize:  intPtr(0), MaxSize: intPtr(1), Target: intPtr(0),
			},
			// A non-UUID remainder is not an EKS managed-node-group ASG name.
			{Name: "eks-system-pool-notauuid", Basename: "eks-system-pool-notauuid"},
		},
	}
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node, shortNode}, Coverage: capacityTestCoverage()}, status)

	group := mustFindGroup(t, gm.Groups, "eks-nodegroup/system-pool")
	if len(group.Children) != 1 || group.Children[0].ID != "eks-system-pool-28cf09b0-ae43-f32a-94c0-27fea5fc8bdc" {
		t.Fatalf("children = %+v, want the derived ASG child", group.Children)
	}
	if group.Manager != capacityapi.ManagerClusterAutoscaler || !group.ManagerValidated {
		t.Fatalf("manager = %q validated=%v, want cluster_autoscaler validated", group.Manager, group.ManagerValidated)
	}
	// "system" is a prefix of "system-pool": the UUID-shaped-remainder rule
	// must keep the child off the shorter group.
	short := mustFindGroup(t, gm.Groups, "eks-nodegroup/system")
	if len(short.Children) != 0 {
		t.Fatalf("system group children = %+v, want none", short.Children)
	}
	if len(gm.OrphanAutoscalerGroups) != 2 {
		t.Fatalf("orphans = %+v, want the zero-node MNG and the non-UUID name", gm.OrphanAutoscalerGroups)
	}
}

func TestBuildGroupsModelGKEJoinStripsGrpSuffix(t *testing.T) {
	node := groupsTestNode("gke-cluster-pool-abc123-x7kq", map[string]string{labelGKENodePool: "cluster-pool"})
	probe := capacityTestTime()
	status := &autoscalerstatus.Status{
		Format:          autoscalerstatus.FormatStructured,
		AutoscalerState: "Running",
		Time:            &probe,
		NodeGroups: []autoscalerstatus.NodeGroup{{
			Name:     "https://www.googleapis.com/compute/v1/projects/p/zones/z/instanceGroupManagers/gke-cluster-pool-abc123-grp",
			Basename: "gke-cluster-pool-abc123-grp",
			MinSize:  intPtr(1),
			MaxSize:  intPtr(10),
			Target:   intPtr(3),
			Health:   autoscalerstatus.Health{Status: "Healthy", Ready: intPtr(3), Registered: intPtr(3), LastProbeTime: &probe},
		}},
	}
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node}, Coverage: capacityTestCoverage()}, status)

	group := mustFindGroup(t, gm.Groups, "gke-nodepool/cluster-pool")
	if group.Name != "cluster-pool" {
		t.Fatalf("group name = %q, want the node-label value cluster-pool", group.Name)
	}
	if group.Manager != capacityapi.ManagerGKEAutoscaler || !group.ManagerValidated {
		t.Fatalf("manager = %q validated=%v, want gke_autoscaler validated", group.Manager, group.ManagerValidated)
	}
	if len(group.Children) != 1 || group.Children[0].ID != "gke-cluster-pool-abc123-grp" {
		t.Fatalf("children = %+v, want single child with basename ID", group.Children)
	}
	assertFact(t, group.Scaling, "bounds", "1–10 nodes")
	assertFact(t, group.Scaling, "target", "target 3")
	if len(gm.OrphanAutoscalerGroups) != 0 {
		t.Fatalf("orphans = %+v, want none", gm.OrphanAutoscalerGroups)
	}
}

func TestBuildGroupsModelAKSJoinWithoutSeparator(t *testing.T) {
	node := groupsTestNode("aks-nodepool1-12345678-vmss000000", map[string]string{labelAKSAgentPool: "nodepool1"})
	probe := capacityTestTime()
	status := &autoscalerstatus.Status{
		Format:          autoscalerstatus.FormatStructured,
		AutoscalerState: "Running",
		Time:            &probe,
		NodeGroups: []autoscalerstatus.NodeGroup{{
			Name:     "aks-nodepool1-12345678-vmss",
			Basename: "aks-nodepool1-12345678-vmss",
			MinSize:  intPtr(1),
			MaxSize:  intPtr(5),
			Health:   autoscalerstatus.Health{Status: "Healthy"},
		}},
	}
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node}, Coverage: capacityTestCoverage()}, status)

	group := mustFindGroup(t, gm.Groups, "aks-agentpool/nodepool1")
	if group.Manager != capacityapi.ManagerAKSAutoscaler || group.ManagerValidated {
		t.Fatalf("manager = %q validated=%v, want aks_autoscaler unvalidated", group.Manager, group.ManagerValidated)
	}
	if len(group.Children) != 1 {
		t.Fatalf("expected the VMSS child to join by prefix, got %+v", group.Children)
	}
	// Every child publishes min+max but no target → bounds only, no target fact.
	assertFact(t, group.Scaling, "bounds", "1–5 nodes")
	if _, ok := findFact(group.Scaling, "target"); ok {
		t.Fatalf("did not expect a target fact when target is unpublished: %+v", group.Scaling)
	}
}

func TestBuildGroupsModelTruncatedMIGJoinsByPrefixKeepsLabelName(t *testing.T) {
	// The MIG basename is truncated by the provider; identity must still come
	// from the node label, never from parsing the truncated group name.
	node := groupsTestNode("gke-longname-clust-longname-clust-4712e1a8-x7kq", map[string]string{labelGKENodePool: "longname-cluster-pool"})
	probe := capacityTestTime()
	status := &autoscalerstatus.Status{
		Format:          autoscalerstatus.FormatStructured,
		AutoscalerState: "Running",
		Time:            &probe,
		NodeGroups: []autoscalerstatus.NodeGroup{{
			Name:     "https://www.googleapis.com/compute/v1/projects/p/zones/z/instanceGroupManagers/gke-longname-clust-longname-clust-4712e1a8-grp",
			Basename: "gke-longname-clust-longname-clust-4712e1a8-grp",
			MinSize:  intPtr(0),
			MaxSize:  intPtr(8),
			Health:   autoscalerstatus.Health{Status: "Healthy"},
		}},
	}
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node}, Coverage: capacityTestCoverage()}, status)

	group := mustFindGroup(t, gm.Groups, "gke-nodepool/longname-cluster-pool")
	if group.Name != "longname-cluster-pool" {
		t.Fatalf("group name = %q, want the label value not the truncated MIG name", group.Name)
	}
	if len(group.Children) != 1 || group.Children[0].ID != "gke-longname-clust-longname-clust-4712e1a8-grp" {
		t.Fatalf("child not attached by prefix: %+v", group.Children)
	}
}

func TestBuildGroupsModelScaleToZeroChildStaysOrphan(t *testing.T) {
	probe := capacityTestTime()
	status := &autoscalerstatus.Status{
		Format:          autoscalerstatus.FormatStructured,
		AutoscalerState: "Running",
		Time:            &probe,
		NodeGroups: []autoscalerstatus.NodeGroup{{
			Name:     "gke-idle-pool-99887766-grp",
			Basename: "gke-idle-pool-99887766-grp",
			MinSize:  intPtr(0),
			MaxSize:  intPtr(5),
			Target:   intPtr(0),
			// LastProbeTime nil: inactive groups publish no probe time; AsOf must
			// stay nil, never become zero.
			Health: autoscalerstatus.Health{Status: "Healthy"},
		}},
	}
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Coverage: capacityTestCoverage()}, status)

	if len(gm.Groups) != 0 {
		t.Fatalf("scale-to-zero child must not fabricate a group: %+v", gm.Groups)
	}
	if len(gm.OrphanAutoscalerGroups) != 1 {
		t.Fatalf("expected one orphan, got %+v", gm.OrphanAutoscalerGroups)
	}
	orphan := gm.OrphanAutoscalerGroups[0]
	if orphan.ID != "gke-idle-pool-99887766-grp" {
		t.Fatalf("orphan ID = %q, want the basename", orphan.ID)
	}
	if orphan.Target == nil || *orphan.Target != 0 {
		t.Fatalf("orphan target = %v, want 0", orphan.Target)
	}
	if orphan.AsOf != nil {
		t.Fatalf("orphan AsOf = %v, want nil (unpublished probe time)", orphan.AsOf)
	}
}

func TestBuildGroupsModelUnattributedNodesAreCountedNotGrouped(t *testing.T) {
	node := groupsTestNode("plain-worker-1", map[string]string{"role": "worker"})
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node}, Coverage: capacityTestCoverage()}, nil)

	if len(gm.Groups) != 0 {
		t.Fatalf("unattributed node must never form a group: %+v", gm.Groups)
	}
	if gm.UnattributedNodeCount != 1 {
		t.Fatalf("unattributed count = %d, want 1", gm.UnattributedNodeCount)
	}
}

func TestBuildGroupsModelEksctlLabelAloneIsUnattributed(t *testing.T) {
	// alpha.eksctl.io/nodegroup-name is a name hint, not identity — a node with
	// only that label is unattributed, not a group.
	node := groupsTestNode("ip-10-0-1-1", map[string]string{"alpha.eksctl.io/nodegroup-name": "ng-1"})
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node}, Coverage: capacityTestCoverage()}, nil)

	if len(gm.Groups) != 0 {
		t.Fatalf("eksctl label alone must not form a group: %+v", gm.Groups)
	}
	if gm.UnattributedNodeCount != 1 {
		t.Fatalf("unattributed count = %d, want 1", gm.UnattributedNodeCount)
	}
}

func TestBuildGroupsModelKarpenterZeroNodePoolStillGroup(t *testing.T) {
	pool := capacityTestPool("default", "default-uid", nil, map[string]any{
		"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
	})
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), NodePools: []*unstructured.Unstructured{pool}, Coverage: capacityTestCoverage()}, nil)

	group := mustFindGroup(t, gm.Groups, "karpenter-nodepool/default")
	if group.NodeCount != 0 {
		t.Fatalf("node count = %d, want 0", group.NodeCount)
	}
	if group.Manager != capacityapi.ManagerKarpenter || !group.ManagerValidated {
		t.Fatalf("manager = %q validated=%v, want karpenter validated", group.Manager, group.ManagerValidated)
	}
	assertFact(t, group.Scaling, "limits", "no limits configured")

	manager := mustFindManager(t, gm.Managers, capacityapi.ManagerKarpenter)
	if manager.GroupCount != 1 || manager.Status != capacityapi.ManagerHealthy {
		t.Fatalf("karpenter manager = %+v, want 1 group healthy", manager)
	}
}

func TestBuildGroupsModelKarpenterLimitsFactFormatsResourceOrder(t *testing.T) {
	pool := capacityTestPool("gpu", "gpu-uid", map[string]any{
		"limits": map[string]any{"memory": "1Ti", "cpu": "400", "nvidia.com/gpu": "8"},
	}, map[string]any{
		"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
	})
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), NodePools: []*unstructured.Unstructured{pool}, Coverage: capacityTestCoverage()}, nil)

	group := mustFindGroup(t, gm.Groups, "karpenter-nodepool/gpu")
	assertFact(t, group.Scaling, "limits", "limits: cpu 400 · memory 1Ti · nvidia.com/gpu 8")
}

func TestBuildGroupsModelKarpenterLabelWithoutPoolReportsPoolNotObserved(t *testing.T) {
	// A node carries the Karpenter NodePool label but no NodePool CRD was
	// observed — the Karpenter-denied case, and the label-remnant case after the
	// CRD was removed. The group stands on label evidence, but every claim about
	// the pool's spec/status must degrade honestly: scaling is "NodePool not
	// observed" (never "no limits configured" — that asserts a spec we never
	// read), and the manager rollup is unknown with no detail (pool readiness is
	// unreadable), never a fabricated "healthy".
	node := groupsTestNode("ip-10-0-2-2", map[string]string{karpenter.NodePoolLabelKey: "ghost-pool"})
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node}, Coverage: capacityTestCoverage()}, nil)

	group := mustFindGroup(t, gm.Groups, "karpenter-nodepool/ghost-pool")
	if group.Manager != capacityapi.ManagerKarpenter {
		t.Fatalf("manager = %q, want karpenter from label evidence", group.Manager)
	}
	assertFact(t, group.Scaling, "pool_not_observed", "NodePool not observed")
	if _, ok := findFact(group.Scaling, "limits"); ok {
		t.Fatalf("label-only karpenter group must not claim limits: %+v", group.Scaling)
	}

	manager := mustFindManager(t, gm.Managers, capacityapi.ManagerKarpenter)
	if manager.GroupCount != 1 || manager.Status != capacityapi.ManagerUnknown || manager.Detail != "" {
		t.Fatalf("karpenter rollup = %+v, want 1 group unknown with no detail", manager)
	}
}

func TestBuildGroupsModelScheduledRequestsGatedOnPodObservation(t *testing.T) {
	node := groupsTestNode("gke-pool-a-1234-x7kq", map[string]string{labelGKENodePool: "pool-a"})
	pod := capacityTestPod("web-1", node.Name, "web", map[corev1.ResourceName]string{corev1.ResourceCPU: "1"})
	coverage := capacityTestCoverage()
	coverage[capacityapi.CoveragePods] = capacityapi.NewSourceCoverage(capacityapi.CoverageUnavailable, capacityapi.CoverageScopeCluster)

	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node}, Pods: []*corev1.Pod{pod}, Coverage: coverage}, nil)

	group := mustFindGroup(t, gm.Groups, "gke-nodepool/pool-a")
	if group.ScheduledRequests != nil {
		t.Fatalf("scheduled requests must be absent when pods unobserved, got %+v", group.ScheduledRequests)
	}
	if group.Allocatable == nil {
		t.Fatalf("allocatable should still be present from observed nodes")
	}
	if gm.ClusterScheduling == nil || gm.ClusterScheduling.ScheduledRequests != nil {
		t.Fatalf("cluster scheduled requests must be absent when pods unobserved: %+v", gm.ClusterScheduling)
	}
	if gm.ClusterScheduling.Allocatable == nil {
		t.Fatalf("cluster allocatable should be present from observed nodes")
	}
}

func TestBuildGroupsModelClusterSchedulingSumsAllNodesIncludingUnattributed(t *testing.T) {
	grouped := groupsTestNodeAlloc("gke-pool-a-1234-x7kq", map[string]string{labelGKENodePool: "pool-a"}, resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "4"}))
	unattributed := groupsTestNodeAlloc("plain-worker", map[string]string{"role": "worker"}, resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "2"}))
	groupedPod := capacityTestPod("web-1", grouped.Name, "web", map[corev1.ResourceName]string{corev1.ResourceCPU: "1"})
	unattributedPod := capacityTestPod("sys-1", unattributed.Name, "sys", map[corev1.ResourceName]string{corev1.ResourceCPU: "1"})

	gm := buildGroups(Snapshot{
		GeneratedAt: capacityTestTime(),
		Nodes:       []*corev1.Node{grouped, unattributed},
		Pods:        []*corev1.Pod{groupedPod, unattributedPod},
		Coverage:    capacityTestCoverage(),
	}, nil)

	if gm.UnattributedNodeCount != 1 {
		t.Fatalf("unattributed count = %d, want 1", gm.UnattributedNodeCount)
	}
	if gm.ClusterScheduling == nil {
		t.Fatalf("cluster scheduling should be present with nodes observed")
	}
	assertVectorQuantity(t, gm.ClusterScheduling.Allocatable.Resources, "cpu", "6")
	// Both bound pods count (1 cpu each), including the pod on the unattributed node.
	assertVectorQuantity(t, gm.ClusterScheduling.ScheduledRequests.Resources, "cpu", "2")
}

func TestBuildGroupsModelClusterSchedulingSplitsNegativePriorityRequests(t *testing.T) {
	node := groupsTestNodeAlloc("gke-pool-a-1234-x7kq", map[string]string{labelGKENodePool: "pool-a"}, resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "8"}))

	t.Run("mixed pods yield a strict subset", func(t *testing.T) {
		gm := buildGroups(Snapshot{
			GeneratedAt: capacityTestTime(),
			Nodes:       []*corev1.Node{node},
			Pods: []*corev1.Pod{
				capacityTestPod("web-1", node.Name, "web", map[corev1.ResourceName]string{corev1.ResourceCPU: "1"}),
				withPriority(capacityTestPod("batch-1", node.Name, "batch", map[corev1.ResourceName]string{corev1.ResourceCPU: "2"}), -10),
			},
			Coverage: capacityTestCoverage(),
		}, nil)
		if gm.ClusterScheduling == nil {
			t.Fatal("cluster scheduling is nil")
		}
		assertVectorQuantity(t, gm.ClusterScheduling.ScheduledRequests.Resources, "cpu", "3")
		capacityTestAssertObservation(t, gm.ClusterScheduling.NegativePriorityRequests, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{"cpu": "2", "pods": "1"})
		capacityTestAssertSubset(t, gm.ClusterScheduling.NegativePriorityRequests, gm.ClusterScheduling.ScheduledRequests, true)
	})

	t.Run("nil-priority-only pods leave the field absent", func(t *testing.T) {
		gm := buildGroups(Snapshot{
			GeneratedAt: capacityTestTime(),
			Nodes:       []*corev1.Node{node},
			Pods: []*corev1.Pod{
				capacityTestPod("web-1", node.Name, "web", map[corev1.ResourceName]string{corev1.ResourceCPU: "1"}),
			},
			Coverage: capacityTestCoverage(),
		}, nil)
		if gm.ClusterScheduling == nil || gm.ClusterScheduling.NegativePriorityRequests != nil {
			t.Fatalf("negative-priority requests = %+v, want absent", gm.ClusterScheduling)
		}
	})

	t.Run("pods unobserved gates the field off", func(t *testing.T) {
		coverage := capacityTestCoverage()
		coverage[capacityapi.CoveragePods] = capacityapi.NewSourceCoverage(capacityapi.CoverageDenied, capacityapi.CoverageScopeCluster)
		gm := buildGroups(Snapshot{
			GeneratedAt: capacityTestTime(),
			Nodes:       []*corev1.Node{node},
			Pods: []*corev1.Pod{
				withPriority(capacityTestPod("batch-1", node.Name, "batch", map[corev1.ResourceName]string{corev1.ResourceCPU: "2"}), -10),
			},
			Coverage: coverage,
		}, nil)
		if gm.ClusterScheduling == nil {
			t.Fatal("cluster scheduling is nil (allocatable should keep it present)")
		}
		if gm.ClusterScheduling.ScheduledRequests != nil || gm.ClusterScheduling.NegativePriorityRequests != nil {
			t.Fatalf("pods-denied cluster scheduling = %+v, want requests and negative-priority both absent", gm.ClusterScheduling)
		}
	})

	t.Run("negative-priority-only pods make the subset equal the total", func(t *testing.T) {
		gm := buildGroups(Snapshot{
			GeneratedAt: capacityTestTime(),
			Nodes:       []*corev1.Node{node},
			Pods: []*corev1.Pod{
				withPriority(capacityTestPod("batch-1", node.Name, "batch", map[corev1.ResourceName]string{corev1.ResourceCPU: "2"}), -10),
			},
			Coverage: capacityTestCoverage(),
		}, nil)
		if gm.ClusterScheduling == nil {
			t.Fatal("cluster scheduling is nil")
		}
		capacityTestAssertObservation(t, gm.ClusterScheduling.NegativePriorityRequests, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, map[string]string{"cpu": "2", "pods": "1"})
		capacityTestAssertSubset(t, gm.ClusterScheduling.NegativePriorityRequests, gm.ClusterScheduling.ScheduledRequests, false)
	})
}

func TestBuildGroupsModelManagerDegradedOnChildBackoff(t *testing.T) {
	node := groupsTestNode("gke-pool-a-1234-x7kq", map[string]string{labelGKENodePool: "pool-a"})
	probe := capacityTestTime()
	status := &autoscalerstatus.Status{
		Format:          autoscalerstatus.FormatStructured,
		AutoscalerState: "Running",
		Time:            &probe,
		NodeGroups: []autoscalerstatus.NodeGroup{{
			Name:     "gke-pool-a-1234-grp",
			Basename: "gke-pool-a-1234-grp",
			MinSize:  intPtr(1),
			MaxSize:  intPtr(10),
			Health:   autoscalerstatus.Health{Status: "Healthy"},
			ScaleUp: autoscalerstatus.Condition{
				Status:  "Backoff",
				Backoff: &autoscalerstatus.Backoff{ErrorClass: "OutOfResource", ErrorCode: "QUOTA_EXCEEDED"},
			},
		}},
	}
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node}, Coverage: capacityTestCoverage()}, status)

	manager := mustFindManager(t, gm.Managers, capacityapi.ManagerGKEAutoscaler)
	if manager.Status != capacityapi.ManagerDegraded {
		t.Fatalf("manager status = %q, want degraded", manager.Status)
	}
	if manager.Detail != "scale-up backoff on gke-pool-a-1234-grp" {
		t.Fatalf("manager detail = %q, want the backoff reason", manager.Detail)
	}
}

func TestBuildGroupsModelNoStatusYieldsNoManagerDetected(t *testing.T) {
	node := groupsTestNode("gke-pool-a-1234-x7kq", map[string]string{labelGKENodePool: "pool-a"})
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node}, Coverage: capacityTestCoverage()}, nil)

	group := mustFindGroup(t, gm.Groups, "gke-nodepool/pool-a")
	assertFact(t, group.Scaling, "no_manager_detected", "no capacity manager detected")
	if group.Manager != "" {
		t.Fatalf("manager = %q, want empty (none detected)", group.Manager)
	}
	if len(gm.Managers) != 0 {
		t.Fatalf("managers = %+v, want none (no NodePools, no autoscaler status)", gm.Managers)
	}
}

func TestBuildGroupsModelUnreadableStatusNeverClaimsNoManager(t *testing.T) {
	// The server maps denied, out-of-cache-scope, read-failed and parse-failed
	// all to a nil Status. "No capacity manager detected" is a cluster fact; it
	// may only follow from a read that actually happened.
	node := groupsTestNode("gke-pool-a-1234-x7kq", map[string]string{labelGKENodePool: "pool-a"})
	snapshot := func() Snapshot {
		return Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node}, Coverage: capacityTestCoverage()}
	}

	for _, name := range []string{"denied", "cache scope", "read failed", "parse failed"} {
		t.Run(name, func(t *testing.T) {
			gm := buildGroupsWithDetection(snapshot(), nil, AutoscalerUnavailable)
			group := mustFindGroup(t, gm.Groups, "gke-nodepool/pool-a")
			assertFact(t, group.Scaling, "manager_detection_unavailable", "manager detection unavailable")
			if _, ok := findFact(group.Scaling, "no_manager_detected"); ok {
				t.Fatalf("unreadable detection claimed no manager: %+v", group.Scaling)
			}
			for _, manager := range gm.Managers {
				if manager.Status == capacityapi.ManagerHealthy {
					t.Fatalf("fabricated a healthy manager from an unreadable source: %+v", manager)
				}
			}
		})
	}

	// The ConfigMap confirmed absent IS an observation — that case keeps saying
	// so, and must not be softened into "unavailable".
	observed := buildGroupsWithDetection(snapshot(), nil, AutoscalerNotPublished)
	group := mustFindGroup(t, observed.Groups, "gke-nodepool/pool-a")
	assertFact(t, group.Scaling, "no_manager_detected", "no capacity manager detected")
	if _, ok := findFact(group.Scaling, "manager_detection_unavailable"); ok {
		t.Fatalf("confirmed-absent ConfigMap reported as unavailable: %+v", group.Scaling)
	}
}

func TestBuildGroupsModelJoinedChildAttributesUnvalidatedPlatformsToClusterAutoscaler(t *testing.T) {
	// kops/doks/ack/lke/scaleway run the upstream Cluster Autoscaler; a joined
	// child is proof the group is autoscaler-managed, so leaving Manager empty
	// dropped the group from every rollup.
	node := groupsTestNode("nodes-us-east-1a-abc", map[string]string{labelKopsInstanceGroup: "nodes-us-east-1a"})
	probe := capacityTestTime()
	status := &autoscalerstatus.Status{
		Format:          autoscalerstatus.FormatStructured,
		AutoscalerState: "Running",
		Time:            &probe,
		NodeGroups: []autoscalerstatus.NodeGroup{{
			Name:     "nodes-us-east-1a",
			Basename: "nodes-us-east-1a",
			MinSize:  intPtr(1),
			MaxSize:  intPtr(9),
			Health:   autoscalerstatus.Health{Status: "Healthy"},
		}},
	}
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node}, Coverage: capacityTestCoverage()}, status)

	group := mustFindGroup(t, gm.Groups, "kops-instancegroup/nodes-us-east-1a")
	if group.Manager != capacityapi.ManagerClusterAutoscaler {
		t.Fatalf("manager = %q, want %q", group.Manager, capacityapi.ManagerClusterAutoscaler)
	}
	if group.ManagerValidated {
		t.Fatal("the kops join rule is not validated against a live cluster — managerValidated must be false")
	}
	if group.Platform != "kops" {
		t.Fatalf("group.Platform = %q, want the identity domain the row exists by", group.Platform)
	}
	manager := mustFindManager(t, gm.Managers, capacityapi.ManagerClusterAutoscaler)
	if manager.GroupCount != 1 {
		t.Fatalf("rollup groupCount = %d, want 1 — it must agree with the attributed group rows", manager.GroupCount)
	}
}

func TestBuildGroupsModelLegacyBackoffWithoutDetailsIsDegraded(t *testing.T) {
	// The legacy text format states backoff in the ScaleUp status and never
	// carries structured backoffInfo. Requiring the details rendered a scale-up
	// that cannot proceed as an unknown-but-fine manager.
	node := groupsTestNode("aks-spotgpu-41529630-vmss000000", map[string]string{labelAKSAgentPool: "spotgpu"})
	probe := capacityTestTime()
	status := &autoscalerstatus.Status{
		Format:          autoscalerstatus.FormatLegacyText,
		AutoscalerState: "Running",
		Time:            &probe,
		NodeGroups: []autoscalerstatus.NodeGroup{{
			Name:     "aks-spotgpu-41529630-vmss",
			Basename: "aks-spotgpu-41529630-vmss",
			MinSize:  intPtr(0),
			MaxSize:  intPtr(6),
			Health:   autoscalerstatus.Health{Status: "Healthy"},
			ScaleUp:  autoscalerstatus.Condition{Status: "Backoff"},
		}},
	}
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node}, Coverage: capacityTestCoverage()}, status)

	group := mustFindGroup(t, gm.Groups, "aks-agentpool/spotgpu")
	if len(group.Children) != 1 || group.Children[0].Backoff == nil {
		t.Fatalf("child lost its backoff because the details were unpublished: %+v", group.Children)
	}
	manager := mustFindManager(t, gm.Managers, capacityapi.ManagerAKSAutoscaler)
	if manager.Status != capacityapi.ManagerDegraded {
		t.Fatalf("manager status = %q, want degraded", manager.Status)
	}
}

func TestBuildGroupsModelChildKeepsPublishedNameAndBasenameID(t *testing.T) {
	node := groupsTestNode("gke-pool-a-1234-x7kq", map[string]string{labelGKENodePool: "pool-a"})
	published := "https://www.googleapis.com/compute/v1/projects/p/zones/us-east1-b/instanceGroups/gke-pool-a-1234-grp"
	probe := capacityTestTime()
	status := &autoscalerstatus.Status{
		Format:          autoscalerstatus.FormatStructured,
		AutoscalerState: "Running",
		Time:            &probe,
		NodeGroups: []autoscalerstatus.NodeGroup{{
			Name:     published,
			Basename: "gke-pool-a-1234-grp",
			MinSize:  intPtr(1),
			MaxSize:  intPtr(10),
			Health:   autoscalerstatus.Health{Status: "Healthy"},
		}},
	}
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node}, Coverage: capacityTestCoverage()}, status)

	group := mustFindGroup(t, gm.Groups, "gke-nodepool/pool-a")
	if len(group.Children) != 1 {
		t.Fatalf("children = %+v, want the joined MIG", group.Children)
	}
	child := group.Children[0]
	if child.ID != "gke-pool-a-1234-grp" {
		t.Fatalf("child ID = %q, want the stable basename", child.ID)
	}
	if child.Name != published {
		t.Fatalf("child Name = %q, want the full published name %q", child.Name, published)
	}
}

func TestBuildGroupsModelChildIDStableAcrossOrphanAndParented(t *testing.T) {
	basename := "gke-pool-a-1234-grp"
	makeStatus := func() *autoscalerstatus.Status {
		probe := capacityTestTime()
		return &autoscalerstatus.Status{
			Format:          autoscalerstatus.FormatStructured,
			AutoscalerState: "Running",
			Time:            &probe,
			NodeGroups: []autoscalerstatus.NodeGroup{{
				Name:     basename,
				Basename: basename,
				MinSize:  intPtr(1),
				MaxSize:  intPtr(10),
				Health:   autoscalerstatus.Health{Status: "Healthy"},
			}},
		}
	}

	parentedNode := groupsTestNode("gke-pool-a-1234-x7kq", map[string]string{labelGKENodePool: "pool-a"})
	parented := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{parentedNode}, Coverage: capacityTestCoverage()}, makeStatus())
	group := mustFindGroup(t, parented.Groups, "gke-nodepool/pool-a")
	if len(group.Children) != 1 || group.Children[0].ID != basename {
		t.Fatalf("parented child ID = %+v, want %q", group.Children, basename)
	}

	orphaned := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Coverage: capacityTestCoverage()}, makeStatus())
	if len(orphaned.OrphanAutoscalerGroups) != 1 || orphaned.OrphanAutoscalerGroups[0].ID != basename {
		t.Fatalf("orphan child ID = %+v, want %q — must match the parented ID", orphaned.OrphanAutoscalerGroups, basename)
	}
}

func TestBuildGroupsModelNilBoundsChildYieldsBoundsNotPublished(t *testing.T) {
	node := groupsTestNode("gke-pool-a-1234-x7kq", map[string]string{labelGKENodePool: "pool-a"})
	probe := capacityTestTime()
	status := &autoscalerstatus.Status{
		Format:          autoscalerstatus.FormatStructured,
		AutoscalerState: "Running",
		Time:            &probe,
		NodeGroups: []autoscalerstatus.NodeGroup{{
			Name:     "gke-pool-a-1234-grp",
			Basename: "gke-pool-a-1234-grp",
			// MinSize published, MaxSize nil → bounds are not fully published.
			MinSize: intPtr(1),
			Health:  autoscalerstatus.Health{Status: "Healthy"},
		}},
	}
	gm := buildGroups(Snapshot{GeneratedAt: capacityTestTime(), Nodes: []*corev1.Node{node}, Coverage: capacityTestCoverage()}, status)

	group := mustFindGroup(t, gm.Groups, "gke-nodepool/pool-a")
	assertFact(t, group.Scaling, "bounds_not_published", "bounds not published in-cluster")
	if _, ok := findFact(group.Scaling, "bounds"); ok {
		t.Fatalf("must not fabricate a bounds fact from a partial child: %+v", group.Scaling)
	}
}

// --- helpers ---

// buildGroups treats a nil status as the ConfigMap being confirmed absent —
// the true-observation case. Tests covering a denied or unreadable source call
// buildGroupsWithDetection.
func buildGroups(snapshot Snapshot, status *autoscalerstatus.Status) GroupsModel {
	detection := AutoscalerObserved
	if status == nil {
		detection = AutoscalerNotPublished
	}
	return buildGroupsWithDetection(snapshot, status, detection)
}

func buildGroupsWithDetection(snapshot Snapshot, status *autoscalerstatus.Status, detection AutoscalerDetection) GroupsModel {
	model := Build(snapshot)
	return BuildGroupsModel(snapshot, status, detection, &model, snapshot.GeneratedAt)
}

func groupsTestNode(name string, labels map[string]string) *corev1.Node {
	return groupsTestNodeAlloc(name, labels, resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "4", corev1.ResourceMemory: "8Gi"}))
}

func groupsTestNodeAlloc(name string, labels map[string]string, allocatable corev1.ResourceList) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid"), Labels: labels},
		Status: corev1.NodeStatus{
			Allocatable: allocatable,
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func intPtr(value int) *int { return &value }

func mustFindGroup(t *testing.T, groups []capacityapi.CapacityGroupSummary, id string) capacityapi.CapacityGroupSummary {
	t.Helper()
	for _, group := range groups {
		if group.ID == id {
			return group
		}
	}
	t.Fatalf("group %q not found in %+v", id, groups)
	return capacityapi.CapacityGroupSummary{}
}

func mustFindManager(t *testing.T, managers []capacityapi.ManagerSummary, manager capacityapi.CapacityManager) capacityapi.ManagerSummary {
	t.Helper()
	for _, entry := range managers {
		if entry.Manager == manager {
			return entry
		}
	}
	t.Fatalf("manager %q not found in %+v", manager, managers)
	return capacityapi.ManagerSummary{}
}

func findFact(facts []capacityapi.ScalingFact, code string) (capacityapi.ScalingFact, bool) {
	for _, fact := range facts {
		if fact.Code == code {
			return fact, true
		}
	}
	return capacityapi.ScalingFact{}, false
}

func assertFact(t *testing.T, facts []capacityapi.ScalingFact, code, summary string) {
	t.Helper()
	fact, ok := findFact(facts, code)
	if !ok {
		t.Fatalf("scaling fact %q not found in %+v", code, facts)
	}
	if fact.Summary != summary {
		t.Fatalf("scaling fact %q summary = %q, want %q", code, fact.Summary, summary)
	}
}

func assertVectorQuantity(t *testing.T, vector capacityapi.ResourceVector, name, want string) {
	t.Helper()
	got, ok := vector[name]
	if !ok {
		t.Fatalf("resource %q missing from vector %+v", name, vector)
	}
	gotQuantity := resource.MustParse(got)
	wantQuantity := resource.MustParse(want)
	if gotQuantity.Cmp(wantQuantity) != 0 {
		t.Fatalf("resource %q = %q, want %q", name, got, want)
	}
}
