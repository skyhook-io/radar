package prometheus

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDetectWorkloadManager(t *testing.T) {
	controller := true
	cases := []struct {
		name       string
		meta       metav1.ObjectMeta
		wantTool   string
		wantName   string
		wantSignal string
	}{
		{
			// An operator reconciles its workloads continuously, so it wins
			// over Helm metadata copied from the operator's own chart.
			name: "controller ownerReference",
			meta: metav1.ObjectMeta{Namespace: "monitoring", Annotations: map[string]string{"meta.helm.sh/release-name": "kps"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "monitoring.coreos.com/v1", Kind: "Prometheus", Name: "k8s", Controller: &controller}}},
			wantTool: "controller", wantName: "k8s", wantSignal: "controller ownerReference",
		},
		{
			name:     "flux helmrelease",
			meta:     metav1.ObjectMeta{Labels: map[string]string{"helm.toolkit.fluxcd.io/name": "api", "helm.toolkit.fluxcd.io/namespace": "flux-system"}},
			wantTool: "flux", wantName: "api",
		},
		{
			name:     "argo tracking id",
			meta:     metav1.ObjectMeta{Annotations: map[string]string{"argocd.argoproj.io/tracking-id": "argocd_dev-vcs:apps/Deployment:dev/vcs"}},
			wantTool: "argocd", wantName: "dev-vcs", wantSignal: "annotation argocd.argoproj.io/tracking-id",
		},
		{
			name:     "helm release",
			meta:     metav1.ObjectMeta{Annotations: map[string]string{"meta.helm.sh/release-name": "checkout", "meta.helm.sh/release-namespace": "shop"}},
			wantTool: "helm", wantName: "checkout",
		},
		{
			name:     "addon manager reconcile",
			meta:     metav1.ObjectMeta{Labels: map[string]string{"addonmanager.kubernetes.io/mode": "Reconcile"}},
			wantTool: "addon-manager", wantSignal: "label addonmanager.kubernetes.io/mode=Reconcile",
		},
	}
	for _, tc := range cases {
		got := detectWorkloadManager(&appsv1.Deployment{ObjectMeta: tc.meta})
		if got == nil {
			t.Errorf("%s: no manager detected", tc.name)
			continue
		}
		if got.Tool != tc.wantTool || got.Name != tc.wantName || (tc.wantSignal != "" && got.Signal != tc.wantSignal) {
			t.Errorf("%s: got %+v", tc.name, got)
		}
		if got.Signal == "" {
			t.Errorf("%s: a detected manager must say which metadata named it", tc.name)
		}
	}
}

func TestDetectWorkloadManagerReportsNothingWithoutAReconcilingSignal(t *testing.T) {
	for name, meta := range map[string]metav1.ObjectMeta{
		// EnsureExists only recreates a deleted object; a patch survives it.
		"addon manager ensure-exists": {Labels: map[string]string{"addonmanager.kubernetes.io/mode": "EnsureExists"}},
		// Argo CD's default tracking label is also every Helm chart's instance
		// label, so it cannot name a manager on its own.
		"instance label only": {Labels: map[string]string{"app.kubernetes.io/instance": "vcs"}},
		"no metadata":         {},
	} {
		if got := detectWorkloadManager(&appsv1.Deployment{ObjectMeta: meta}); got != nil {
			t.Errorf("%s: got %+v, want nil", name, got)
		}
	}
}

func TestDemandTargetBasisNamesTheStatistic(t *testing.T) {
	cpu := RightsizingRow{Resource: "cpu", Observed: &ObservedStatistic{Name: "P95", Value: 0.5}}
	if got := DemandTargetBasis(cpu); got != "7d P95 x 1.15" {
		t.Errorf("cpu basis = %q", got)
	}
	memory := RightsizingRow{Resource: "memory", Observed: &ObservedStatistic{Name: "Max", Value: 512 * 1024 * 1024}}
	if got := DemandTargetBasis(memory); got != "7d Max x 1.15" {
		t.Errorf("memory basis = %q", got)
	}
	floor := RightsizingRow{Resource: "memory", Observed: &ObservedStatistic{Name: "Max", Value: 1024 * 1024}}
	if got := DemandTargetBasis(floor); got != "minimum request "+formatRightsizingValue(rightsizingMemoryMin, "memory") {
		t.Errorf("a target set by the floor must say so, got %q", got)
	}
	if got := DemandTargetBasis(RightsizingRow{Resource: "cpu"}); got != "" {
		t.Errorf("no observation, no basis; got %q", got)
	}
}

func TestScanSkipsDaemonSetsThatMatchNoNode(t *testing.T) {
	zero := int32(0)
	parked := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "parked", Annotations: map[string]string{"meta.helm.sh/release-name": "parked"}},
		Spec: appsv1.DeploymentSpec{Replicas: &zero}}
	noNodes := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "nvidia-gpu-device-plugin", Generation: 2},
		Status: appsv1.DaemonSetStatus{ObservedGeneration: 2}}
	running := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "fluentbit"},
		Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 3}}
	// Selector just changed; the controller has not re-counted its nodes yet.
	pending := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "node-exporter", Generation: 4},
		Status: appsv1.DaemonSetStatus{ObservedGeneration: 3}}

	if err := k8s.InitTestResourceCache(fake.NewClientset(parked, noNodes, running, pending)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	defer k8s.ResetTestState()
	cache := k8s.GetResourceCache()

	scopes := map[string][]string{"Deployment": nil, "DaemonSet": nil}
	var workloads []scanWorkload
	var skipped []string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		workloads, _, skipped = snapshotScanWorkloads(context.Background(), cache, scopes)
		if len(workloads)+len(skipped) == 4 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !reflect.DeepEqual(skipped, []string{"kube-system/nvidia-gpu-device-plugin"}) || len(workloads) != 3 {
		t.Fatalf("got %d workloads and skipped %v, want 3 and [kube-system/nvidia-gpu-device-plugin]", len(workloads), skipped)
	}
	for _, workload := range workloads {
		switch workload.name {
		case "parked":
			// A Deployment scaled to zero is a real review signal and stays.
			if !workload.workload.scaledToZero {
				t.Error("a Deployment with replicas=0 must stay scaledToZero")
			}
			if workload.workload.managedBy == nil || workload.workload.managedBy.Tool != "helm" {
				t.Errorf("managedBy = %+v, want helm", workload.workload.managedBy)
			}
		case "node-exporter":
			// A replica count of zero with no scaledToZero would rank every
			// oversized row in_range, since impact multiplies by replicas.
			if workload.replicas != 0 || !workload.workload.scaledToZero {
				t.Errorf("unobserved zero DaemonSet = %+v, want replicas 0 and scaledToZero", workload)
			}
		case "fluentbit":
			if workload.replicas != 3 || workload.workload.scaledToZero {
				t.Errorf("running DaemonSet = %+v", workload)
			}
		default:
			t.Errorf("unexpected workload %s", workload.name)
		}
	}

	// Named directly, the skipped DaemonSet is a node pool scaled away: its
	// retained history is served as a scaled-to-zero workload.
	single, err := loadRightsizingWorkload(context.Background(), "DaemonSet", "kube-system", "nvidia-gpu-device-plugin")
	if err != nil {
		t.Fatalf("loadRightsizingWorkload: %v", err)
	}
	if !single.scaledToZero || single.replicas != 0 {
		t.Errorf("single DaemonSet with no nodes = replicas %d scaledToZero %v, want 0 and true", single.replicas, single.scaledToZero)
	}
	unobserved, err := loadRightsizingWorkload(context.Background(), "DaemonSet", "kube-system", "node-exporter")
	if err != nil {
		t.Fatalf("loadRightsizingWorkload: %v", err)
	}
	if !unobserved.scaledToZero {
		t.Error("a zero desired count the controller has not re-observed must still read scaledToZero, not zero-impact balanced")
	}
}

func TestDaemonSetZeroIsTrustedOnlyOnceObserved(t *testing.T) {
	observed := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Generation: 3}, Status: appsv1.DaemonSetStatus{ObservedGeneration: 3}}
	if !daemonSetMatchesNoNode(observed) {
		t.Error("an observed zero means no node matches")
	}
	// The selector just changed to match nodes; the old zero is stale.
	pending := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Generation: 4}, Status: appsv1.DaemonSetStatus{ObservedGeneration: 3}}
	if daemonSetMatchesNoNode(pending) {
		t.Error("a zero the controller has not re-observed must not skip the DaemonSet")
	}
}

func TestScanOfOnlySkippedDaemonSetsIsNotNoWorkloads(t *testing.T) {
	resp := newRightsizingScanResponse(time.Now(), RightsizingScanScope{})
	resp.Coverage.DaemonSetsWithoutNodes = 3
	resp = computeRightsizingScan(context.Background(), &fakeScanQuerier{}, nil, resp)
	if resp.State != RightsizingScanComplete || resp.Reason != "only_daemonsets_without_nodes" {
		t.Errorf("state=%s reason=%s, want complete and only_daemonsets_without_nodes", resp.State, resp.Reason)
	}
}

func TestClusterScopedControllerOwnerCarriesNoNamespace(t *testing.T) {
	controller := true
	// Node is a builtin cluster-scoped kind, so its scope resolves without
	// discovery; a CRD such as ClusterPolicy takes the same branch through it.
	obj := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "gpu-operator",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: "n1", Controller: &controller}}}}
	if got := detectWorkloadManager(obj); got == nil || got.Namespace != "" {
		t.Errorf("a cluster-scoped owner must not inherit the workload namespace, got %+v", got)
	}
	namespaced := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "monitoring.coreos.com/v1", Kind: "Prometheus", Name: "k8s", Controller: &controller}}}}
	if got := detectWorkloadManager(namespaced); got == nil || got.Namespace != "monitoring" {
		t.Errorf("a namespaced owner lives beside its workload, got %+v", got)
	}
}
