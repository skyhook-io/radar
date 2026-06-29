package topology

import (
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// makeOwnedPods creates n pods controlled by the named ReplicaSet, with the
// given phase, in namespace ns.
func makeOwnedPods(n int, ns, rsName string, phase corev1.PodPhase) []*corev1.Pod {
	ctrl := true
	pods := make([]*corev1.Pod, n)
	for i := range n {
		pods[i] = &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-pod-%d", rsName, i),
				Namespace: ns,
				OwnerReferences: []metav1.OwnerReference{
					{Kind: "ReplicaSet", Name: rsName, Controller: &ctrl},
				},
			},
			Status: corev1.PodStatus{Phase: phase},
		}
	}
	return pods
}

// deploymentWithReplicaSet wires a Deployment, its ReplicaSet, and pods so the
// builder's RS→Deployment resolution attributes pod counts to the Deployment.
func deploymentWithReplicaSet(ns, depName, rsName string, pods []*corev1.Pod) *mockProvider {
	ctrl := true
	replicas := int32(len(pods))
	return &mockProvider{
		deployments: []*appsv1.Deployment{
			{ObjectMeta: metav1.ObjectMeta{Name: depName, Namespace: ns}},
		},
		replicaSets: []*appsv1.ReplicaSet{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:            rsName,
					Namespace:       ns,
					OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: depName, Controller: &ctrl}},
				},
				Spec: appsv1.ReplicaSetSpec{Replicas: &replicas},
			},
		},
		pods: pods,
	}
}

func countKind(topo *Topology, kind NodeKind) int {
	n := 0
	for _, node := range topo.Nodes {
		if node.Kind == kind {
			n++
		}
	}
	return n
}

func findNode(topo *Topology, id string) *Node {
	for i := range topo.Nodes {
		if topo.Nodes[i].ID == id {
			return &topo.Nodes[i]
		}
	}
	return nil
}

// 10000 pods → estimate 2000 (pods/5) + 1 deployment ≥ SummaryModeThreshold.
func TestSummaryModeResourcesCollapsesPodTier(t *testing.T) {
	provider := deploymentWithReplicaSet("big", "web", "web-rs",
		makeOwnedPods(10000, "big", "web-rs", corev1.PodRunning))

	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"big"}
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !topo.SummaryMode {
		t.Fatalf("expected SummaryMode=true (estimated %d)", topo.EstimatedNodes)
	}
	if got := countKind(topo, KindPod); got != 0 {
		t.Errorf("expected 0 Pod nodes in summary mode, got %d", got)
	}
	if got := countKind(topo, KindPodGroup); got != 0 {
		t.Errorf("expected 0 PodGroup nodes in summary mode, got %d", got)
	}

	dep := findNode(topo, "deployment/big/web")
	if dep == nil {
		t.Fatal("deployment node missing")
	}
	summary, ok := dep.Data["podSummary"].(map[string]any)
	if !ok {
		t.Fatalf("deployment node missing podSummary, Data=%v", dep.Data)
	}
	if summary["total"] != 10000 {
		t.Errorf("podSummary.total = %v, want 10000", summary["total"])
	}
	if summary["healthy"] != 10000 {
		t.Errorf("podSummary.healthy = %v, want 10000", summary["healthy"])
	}
}

func TestSummaryModePodHealthBreakdown(t *testing.T) {
	var pods []*corev1.Pod
	pods = append(pods, makeOwnedPods(7000, "big", "web-rs", corev1.PodRunning)...)
	pods = append(pods, makeOwnedPods(2000, "big", "web-rs", corev1.PodPending)...)
	pods = append(pods, makeOwnedPods(1000, "big", "web-rs", corev1.PodFailed)...)
	// distinct names across phases
	for i, p := range pods {
		p.Name = fmt.Sprintf("web-rs-pod-%d", i)
	}
	provider := deploymentWithReplicaSet("big", "web", "web-rs", pods)

	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"big"}
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	dep := findNode(topo, "deployment/big/web")
	if dep == nil {
		t.Fatal("deployment node missing")
	}
	summary := dep.Data["podSummary"].(map[string]any)
	if summary["total"] != 10000 {
		t.Errorf("total = %v, want 10000", summary["total"])
	}
	if summary["healthy"] != 7000 {
		t.Errorf("healthy = %v, want 7000", summary["healthy"])
	}
	if summary["degraded"] != 2000 {
		t.Errorf("degraded = %v, want 2000 (Pending)", summary["degraded"])
	}
	if summary["unhealthy"] != 1000 {
		t.Errorf("unhealthy = %v, want 1000 (Failed)", summary["unhealthy"])
	}
}

// Below the threshold, normal mode still emits the pod tier.
func TestSummaryModeOffBelowThreshold(t *testing.T) {
	// 1000 pods → estimate 200, well under SummaryModeThreshold.
	provider := deploymentWithReplicaSet("small", "web", "web-rs",
		makeOwnedPods(1000, "small", "web-rs", corev1.PodRunning))

	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"small"}
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if topo.SummaryMode {
		t.Fatal("expected SummaryMode=false below threshold")
	}
	// 1000 pods under one RS → one PodGroup (well over maxIndividualPods)
	if got := countKind(topo, KindPodGroup); got == 0 {
		t.Error("expected PodGroup nodes in normal mode")
	}
}

// Summary mode requires a namespace filter — an unfiltered large cluster gets
// the RequiresNamespaceFilter prompt instead, never reaches summary mode.
func TestSummaryModeRequiresNamespaceFilter(t *testing.T) {
	provider := deploymentWithReplicaSet("big", "web", "web-rs",
		makeOwnedPods(10000, "big", "web-rs", corev1.PodRunning))

	opts := DefaultBuildOptions() // no Namespaces
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if topo.SummaryMode {
		t.Error("summary mode should not trigger without a namespace filter")
	}
	if !topo.RequiresNamespaceFilter {
		t.Error("expected RequiresNamespaceFilter for unfiltered large cluster")
	}
}

// makeDSOwnedPods creates n pods controlled by the named DaemonSet.
func makeDSOwnedPods(n int, ns, dsName string) []*corev1.Pod {
	ctrl := true
	pods := make([]*corev1.Pod, n)
	for i := range n {
		pods[i] = &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-pod-%d", dsName, i),
				Namespace: ns,
				OwnerReferences: []metav1.OwnerReference{
					{Kind: "DaemonSet", Name: dsName, Controller: &ctrl},
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
	}
	return pods
}

func TestSummaryModeDaemonSetPodsAttributed(t *testing.T) {
	provider := &mockProvider{
		daemonSets: []*appsv1.DaemonSet{
			{ObjectMeta: metav1.ObjectMeta{Name: "ds1", Namespace: "big"}},
		},
		pods: makeDSOwnedPods(10000, "big", "ds1"),
	}
	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"big"}
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !topo.SummaryMode {
		t.Fatalf("expected SummaryMode=true (estimated %d)", topo.EstimatedNodes)
	}
	ds := findNode(topo, "daemonset/big/ds1")
	if ds == nil {
		t.Fatal("daemonset node missing")
	}
	summary, ok := ds.Data["podSummary"].(map[string]any)
	if !ok {
		t.Fatalf("daemonset node missing podSummary, Data=%v", ds.Data)
	}
	if summary["total"] != 10000 {
		t.Errorf("podSummary.total = %v, want 10000", summary["total"])
	}
}

// Regression: when a DaemonSet/StatefulSet controller node was never created
// (e.g. the controller list was denied by RBAC while pods are listable), its
// pods must still be visible via an orphan PodGroup — not silently dropped.
func TestSummaryModeMissingControllerPodsNotDropped(t *testing.T) {
	provider := &mockProvider{
		// daemonSets intentionally empty — simulates RBAC denial of the list.
		pods: makeDSOwnedPods(10000, "big", "ds1"),
	}
	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"big"}
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !topo.SummaryMode {
		t.Fatalf("expected SummaryMode=true (estimated %d)", topo.EstimatedNodes)
	}
	// The controller node does not exist...
	if findNode(topo, "daemonset/big/ds1") != nil {
		t.Fatal("did not expect a daemonset node (controller was not listed)")
	}
	// ...and there must be no phantom podSummary stamped anywhere.
	for _, n := range topo.Nodes {
		if _, ok := n.Data["podSummary"]; ok {
			t.Errorf("unexpected podSummary on node %s — pods attributed to a non-existent controller", n.ID)
		}
	}
	// ...the pods are NOT dropped, but collapse into a single bounded summary
	// node per namespace — NOT a PodGroup carrying 10000 per-pod records.
	if got := countKind(topo, KindPodGroup); got != 1 {
		t.Fatalf("expected exactly 1 orphan summary node, got %d", got)
	}
	orphan := findNode(topo, "podgroup-orphans-big")
	if orphan == nil {
		t.Fatal("expected orphan summary node podgroup-orphans-big")
	}
	if _, hasPods := orphan.Data["pods"]; hasPods {
		t.Error("orphan summary node must NOT carry a per-pod 'pods' array (would defeat the summary guard + stay expandable)")
	}
	if orphan.Data["podCount"] != 10000 {
		t.Errorf("orphan podCount = %v, want 10000", orphan.Data["podCount"])
	}
	if orphan.Data["summaryOnly"] != true {
		t.Errorf("orphan node should be marked summaryOnly")
	}
}

func TestSummaryModeStandalonePodsFallBackToPodGroup(t *testing.T) {
	// 10000 deployment-owned pods cross the threshold and attribute to the
	// Deployment; a handful of standalone pods have no owner and must surface
	// as orphan PodGroups rather than being attributed or dropped.
	pods := makeOwnedPods(10000, "big", "web-rs", corev1.PodRunning)
	standalone := makePods(3, "big") // no OwnerReferences
	for i, p := range standalone {
		p.Name = fmt.Sprintf("standalone-%d", i)
	}
	pods = append(pods, standalone...)
	provider := deploymentWithReplicaSet("big", "web", "web-rs", pods)

	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"big"}
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	dep := findNode(topo, "deployment/big/web")
	if dep == nil {
		t.Fatal("deployment node missing")
	}
	// Standalone pods are not attributed to the deployment.
	if summary := dep.Data["podSummary"].(map[string]any); summary["total"] != 10000 {
		t.Errorf("deployment podSummary.total = %v, want 10000 (standalone pods excluded)", summary["total"])
	}
	// They surface as a single bounded orphan summary node (not 3 per-pod
	// PodGroups, and not one carrying a 'pods' array).
	if got := countKind(topo, KindPodGroup); got != 1 {
		t.Fatalf("expected exactly 1 orphan summary node for standalone pods, got %d", got)
	}
	orphan := findNode(topo, "podgroup-orphans-big")
	if orphan == nil {
		t.Fatal("expected orphan summary node podgroup-orphans-big")
	}
	if _, hasPods := orphan.Data["pods"]; hasPods {
		t.Error("orphan summary node must NOT carry a per-pod 'pods' array")
	}
	if orphan.Data["podCount"] != 3 {
		t.Errorf("orphan podCount = %v, want 3", orphan.Data["podCount"])
	}
}

func TestSummaryModeTrafficAttributesToService(t *testing.T) {
	// Pods labeled app=web, selected by svc "web-svc"; enough to cross threshold.
	pods := makeOwnedPods(10000, "big", "web-rs", corev1.PodRunning)
	for _, p := range pods {
		p.Labels = map[string]string{"app": "web"}
	}
	provider := deploymentWithReplicaSet("big", "web", "web-rs", pods)
	provider.services = []*corev1.Service{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "web-svc", Namespace: "big"},
			Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "web"}},
		},
	}

	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"big"}
	opts.ViewMode = ViewModeTraffic
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !topo.SummaryMode {
		t.Fatalf("expected SummaryMode=true (estimated %d)", topo.EstimatedNodes)
	}
	if got := countKind(topo, KindPod) + countKind(topo, KindPodGroup); got != 0 {
		t.Errorf("expected no pod tier nodes in traffic summary mode, got %d", got)
	}
	svc := findNode(topo, "service/big/web-svc")
	if svc == nil {
		t.Fatal("service node missing")
	}
	summary, ok := svc.Data["podSummary"].(map[string]any)
	if !ok {
		t.Fatalf("service node missing podSummary, Data=%v", svc.Data)
	}
	if summary["total"] != 10000 {
		t.Errorf("service podSummary.total = %v, want 10000", summary["total"])
	}
}
