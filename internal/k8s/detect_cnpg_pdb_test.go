package k8s

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

const cnpgTestNS = "db"

// cnpgPDBFixture mirrors what CloudNativePG v1.30.1 creates for a healthy
// Cluster "pg": pods pg-1 (primary) .. pg-N, each on node-<i>.
type cnpgPDBFixture struct {
	cluster *unstructured.Unstructured
	pdb     *policyv1.PodDisruptionBudget
	pods    []*corev1.Pod
	pdbs    []*policyv1.PodDisruptionBudget
	nodes   map[string]*corev1.Node
}

func newCNPGPDBFixture(instances int) *cnpgPDBFixture {
	uid := types.UID("cluster-uid")
	cluster := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": "pg", "namespace": cnpgTestNS, "uid": string(uid)},
		"spec":       map[string]any{"instances": int64(instances)},
		"status": map[string]any{
			"phase":          "Cluster in healthy state",
			"readyInstances": int64(instances),
			"currentPrimary": "pg-1",
			"targetPrimary":  "pg-1",
		},
	}}
	yes := true
	owner := metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg", UID: uid, Controller: &yes}
	one := intstr.FromInt32(1)
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-primary", Namespace: cnpgTestNS, Generation: 1, OwnerReferences: []metav1.OwnerReference{owner}, Labels: map[string]string{"cnpg.io/cluster": "pg"}},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &one,
			Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"cnpg.io/cluster": "pg", "cnpg.io/instanceRole": "primary"}},
		},
		Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1, ExpectedPods: 1, CurrentHealthy: 1, DesiredHealthy: 1},
	}
	f := &cnpgPDBFixture{cluster: cluster, pdb: pdb, pdbs: []*policyv1.PodDisruptionBudget{pdb}, nodes: map[string]*corev1.Node{}}
	for i := 1; i <= instances; i++ {
		role := "replica"
		if i == 1 {
			role = "primary"
		}
		node := fmt.Sprintf("node-%d", i)
		f.pods = append(f.pods, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pg-%d", i), Namespace: cnpgTestNS, OwnerReferences: []metav1.OwnerReference{owner},
				Labels: map[string]string{"cnpg.io/cluster": "pg", "cnpg.io/instanceRole": role}},
			Spec:   corev1.PodSpec{NodeName: node},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
		})
		f.nodes[node] = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	}
	return f
}

func (f *cnpgPDBFixture) classify() (cnpgPDBVerdict, string) {
	return classifyCNPGPrimaryPDB(f.pdb, f.cluster, f.pods, f.pdbs, func(name string) *corev1.Node { return f.nodes[name] })
}

func TestClassifyCNPGPrimaryPDB(t *testing.T) {
	notReady := []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	for _, tc := range []struct {
		name      string
		instances int
		mutate    func(*cnpgPDBFixture)
		want      cnpgPDBVerdict
	}{
		{"two instances on two usable nodes", 2, nil, cnpgPDBExpected},
		{"three instances", 3, nil, cnpgPDBExpected},
		{"single instance", 1, nil, cnpgPDBSingleInstance},
		{"single instance needs no pods or nodes", 1, func(f *cnpgPDBFixture) { f.pods, f.nodes = nil, nil }, cnpgPDBSingleInstance},

		{"name is not <cluster>-primary", 2, func(f *cnpgPDBFixture) { f.pdb.Name = "pg-custom" }, cnpgPDBNone},
		{"owner UID mismatch", 2, func(f *cnpgPDBFixture) { f.pdb.OwnerReferences[0].UID = "other" }, cnpgPDBNone},
		{"owner not controller", 2, func(f *cnpgPDBFixture) { f.pdb.OwnerReferences[0].Controller = nil }, cnpgPDBNone},
		{"owner group foreign", 2, func(f *cnpgPDBFixture) { f.pdb.OwnerReferences[0].APIVersion = "other.io/v1" }, cnpgPDBNone},
		{"second owner reference", 2, func(f *cnpgPDBFixture) {
			f.pdb.OwnerReferences = append(f.pdb.OwnerReferences, metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "x", UID: "x"})
		}, cnpgPDBNone},
		{"minAvailable percent", 2, func(f *cnpgPDBFixture) { v := intstr.FromString("100%"); f.pdb.Spec.MinAvailable = &v }, cnpgPDBNone},
		{"maxUnavailable set", 2, func(f *cnpgPDBFixture) { v := intstr.FromInt32(0); f.pdb.Spec.MaxUnavailable = &v }, cnpgPDBNone},
		{"extra matchLabel", 2, func(f *cnpgPDBFixture) { f.pdb.Spec.Selector.MatchLabels["extra"] = "x" }, cnpgPDBNone},
		{"matchExpression", 2, func(f *cnpgPDBFixture) {
			f.pdb.Spec.Selector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: "x", Operator: metav1.LabelSelectorOpDoesNotExist}}
		}, cnpgPDBNone},
		{"status counts not 1", 2, func(f *cnpgPDBFixture) {
			f.pdb.Status.ExpectedPods, f.pdb.Status.CurrentHealthy, f.pdb.Status.DesiredHealthy = 2, 2, 2
		}, cnpgPDBNone},
		{"stale budget status", 2, func(f *cnpgPDBFixture) { f.pdb.Generation = 2 }, cnpgPDBNone},
		{"cluster deleting", 2, func(f *cnpgPDBFixture) { ts := metav1.Now(); f.cluster.SetDeletionTimestamp(&ts) }, cnpgPDBNone},
		{"single instance cluster deleting", 1, func(f *cnpgPDBFixture) { ts := metav1.Now(); f.cluster.SetDeletionTimestamp(&ts) }, cnpgPDBNone},
		{"cluster missing", 2, func(f *cnpgPDBFixture) { f.cluster = nil }, cnpgPDBNone},

		{"switchover in progress", 2, func(f *cnpgPDBFixture) {
			_ = unstructured.SetNestedField(f.cluster.Object, "Switchover in progress", "status", "phase")
		}, cnpgPDBBlocking},
		{"upgrading", 2, func(f *cnpgPDBFixture) {
			_ = unstructured.SetNestedField(f.cluster.Object, "Upgrading cluster", "status", "phase")
		}, cnpgPDBBlocking},
		{"target primary differs", 2, func(f *cnpgPDBFixture) {
			_ = unstructured.SetNestedField(f.cluster.Object, "pg-2", "status", "targetPrimary")
		}, cnpgPDBBlocking},
		{"not all instances ready", 2, func(f *cnpgPDBFixture) {
			_ = unstructured.SetNestedField(f.cluster.Object, int64(1), "status", "readyInstances")
		}, cnpgPDBBlocking},
		{"maintenance window in progress", 2, func(f *cnpgPDBFixture) {
			_ = unstructured.SetNestedField(f.cluster.Object, true, "spec", "nodeMaintenanceWindow", "inProgress")
		}, cnpgPDBBlocking},
		{"standby shares the primary's node", 3, func(f *cnpgPDBFixture) { f.pods[2].Spec.NodeName = "node-1" }, cnpgPDBBlocking},
		{"primary node cordoned", 2, func(f *cnpgPDBFixture) { f.nodes["node-1"].Spec.Unschedulable = true }, cnpgPDBBlocking},
		{"primary node drain-tainted", 2, func(f *cnpgPDBFixture) {
			f.nodes["node-1"].Spec.Taints = []corev1.Taint{{Key: "karpenter.sh/disrupted", Effect: corev1.TaintEffectNoSchedule}}
		}, cnpgPDBBlocking},
		{"primary node missing", 2, func(f *cnpgPDBFixture) { delete(f.nodes, "node-1") }, cnpgPDBBlocking},
		{"only standby's node cordoned", 2, func(f *cnpgPDBFixture) { f.nodes["node-2"].Spec.Unschedulable = true }, cnpgPDBBlocking},
		{"only standby's node not ready", 2, func(f *cnpgPDBFixture) {
			f.nodes["node-2"].Status.Conditions[0].Status = corev1.ConditionUnknown
		}, cnpgPDBBlocking},
		{"only standby's node missing", 2, func(f *cnpgPDBFixture) { delete(f.nodes, "node-2") }, cnpgPDBBlocking},
		{"only standby not ready", 2, func(f *cnpgPDBFixture) { f.pods[1].Status.Conditions = notReady }, cnpgPDBBlocking},
		{"one usable standby is enough", 3, func(f *cnpgPDBFixture) { f.nodes["node-2"].Spec.Unschedulable = true }, cnpgPDBExpected},
		{"primary pod not ready", 2, func(f *cnpgPDBFixture) { f.pods[0].Status.Conditions = notReady }, cnpgPDBBlocking},
		{"primary pod deleting", 2, func(f *cnpgPDBFixture) { ts := metav1.Now(); f.pods[0].DeletionTimestamp = &ts }, cnpgPDBBlocking},
		{"budget selects a pod that is not the current primary", 2, func(f *cnpgPDBFixture) { f.pods[0].Name = "pg-9" }, cnpgPDBBlocking},
		{"budget selects two pods", 2, func(f *cnpgPDBFixture) { f.pods[1].Labels["cnpg.io/instanceRole"] = "primary" }, cnpgPDBBlocking},
		{"selected pod is not an instance of this cluster", 2, func(f *cnpgPDBFixture) { f.pods[0].OwnerReferences = nil }, cnpgPDBBlocking},
		{"pods unknown", 2, func(f *cnpgPDBFixture) { f.pods = nil }, cnpgPDBBlocking},
		{"a second budget selects the primary", 2, func(f *cnpgPDBFixture) {
			other := f.pdb.DeepCopy()
			other.Name, other.OwnerReferences = "custom", nil
			f.pdbs = append(f.pdbs, other)
		}, cnpgPDBBlocking},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCNPGPDBFixture(tc.instances)
			if tc.mutate != nil {
				tc.mutate(f)
			}
			got, reason := f.classify()
			if got != tc.want {
				t.Fatalf("verdict = %q (%s), want %q", got, reason, tc.want)
			}
			if (got == cnpgPDBBlocking) != (reason != "") {
				t.Fatalf("a reason belongs to blocking only: %q %q", got, reason)
			}
		})
	}
}

// detectCNPGPDB runs the real detector over caches built from the fixture.
// mode: "" (all synced), "no-cluster" (Cluster CRD not watched),
// "no-nodes" (Nodes not in the cache).
func detectCNPGPDB(t *testing.T, f *cnpgPDBFixture, mode string) (Detection, Detection) {
	t.Helper()
	t.Cleanup(ResetTestState)
	t.Cleanup(ResetTestDynamicState)
	f.pdb.CreationTimestamp = metav1.NewTime(time.Now().Add(-48 * time.Hour))
	f.pdb.Status.Conditions = []metav1.Condition{{Type: policyv1.DisruptionAllowedCondition, Status: metav1.ConditionFalse, Reason: "InsufficientPods", LastTransitionTime: metav1.NewTime(time.Now().Add(-24 * time.Hour))}}
	objects := []runtime.Object{f.pdb}
	for _, p := range f.pods {
		objects = append(objects, p)
	}
	if mode != "no-nodes" {
		for _, n := range f.nodes {
			objects = append(objects, n)
		}
	}
	// The unrecognised twin: same spec and status, no CloudNativePG identity.
	twin := f.pdb.DeepCopy()
	twin.Name, twin.OwnerReferences = "plain", nil
	twin.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "plain"}}
	objects = append(objects, twin)
	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatal(err)
	}
	var dynObjects []runtime.Object
	if f.cluster != nil {
		dynObjects = append(dynObjects, f.cluster)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{cnpgClusterGVR: "ClusterList"}, dynObjects...)
	if err := InitTestDynamicResourceCache(dyn, []APIResource{{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster", Name: "clusters", Namespaced: true, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	if mode != "no-cluster" {
		WarmupCommonCRDs()
		if !GetDynamicResourceCache().WaitForSync(cnpgClusterGVR, 3*time.Second) {
			t.Fatal("Clusters did not sync")
		}
	}
	problems := DetectProblemsInScope(GetResourceCache(), cnpgTestNS, nil)
	got, ok := lookupProblem(problems, "PodDisruptionBudget", "pg-primary", "Voluntary evictions blocked")
	if !ok {
		t.Fatalf("PDB warning missing: %+v", problems)
	}
	plain, ok := lookupProblem(problems, "PodDisruptionBudget", "plain", "Voluntary evictions blocked")
	if !ok {
		t.Fatal("unrecognised twin missing")
	}
	return got, plain
}

func TestDetectCNPGPrimaryPDBExpectedIsMarked(t *testing.T) {
	got, plain := detectCNPGPDB(t, newCNPGPDBFixture(2), "")
	if !got.ExpectedWhenVerified {
		t.Fatalf("healthy HA primary budget not marked expected: %+v", got)
	}
	wantReads := []issuesapi.EvidenceRead{
		{Group: "postgresql.cnpg.io", Resource: "clusters", Namespace: cnpgTestNS, Verb: "list", Exact: true},
		{Resource: "pods", Namespace: cnpgTestNS, Verb: "list", Exact: true},
		{Resource: "nodes", Verb: "list", Exact: true},
	}
	if !reflect.DeepEqual(got.RequiredReads, wantReads) {
		t.Fatalf("reads = %+v", got.RequiredReads)
	}
	if got.Degraded == nil || got.Degraded.Message != plain.Message || got.Degraded.Action != plain.Action || got.Degraded.Cause != plain.Cause || !got.Degraded.KeepSubjectOnset {
		t.Fatalf("degraded form must be the unrecognised warning with its onset: %+v vs %+v", got.Degraded, plain)
	}
	if got.Message != plain.Message || got.Action != plain.Action || got.Severity != plain.Severity || got.Fingerprint != plain.Fingerprint ||
		got.OnsetAt != plain.OnsetAt || got.OnsetUnknown != plain.OnsetUnknown {
		t.Fatalf("expected detection changed the warning itself: %+v vs %+v", got, plain)
	}
	if plain.ExpectedWhenVerified || plain.RequiredReads != nil || plain.Degraded != nil {
		t.Fatalf("unrecognised budget changed: %+v", plain)
	}
}

func TestDetectCNPGPrimaryPDBSingleInstance(t *testing.T) {
	got, plain := detectCNPGPDB(t, newCNPGPDBFixture(1), "")
	if got.ExpectedWhenVerified || got.Message != cnpgSingleInstancePDBMessage || got.Action != cnpgSingleInstancePDBAction {
		t.Fatalf("single instance copy missing: %+v", got)
	}
	if !reflect.DeepEqual(got.RequiredReads, []issuesapi.EvidenceRead{{Group: "postgresql.cnpg.io", Resource: "clusters", Namespace: cnpgTestNS, Verb: "list", Exact: true}}) {
		t.Fatalf("reads = %+v", got.RequiredReads)
	}
	if got.Degraded == nil || got.Degraded.Message != plain.Message || got.Degraded.Action != plain.Action || !got.Degraded.KeepSubjectOnset {
		t.Fatalf("degraded form must be the unrecognised warning: %+v", got.Degraded)
	}
	if got.Severity != plain.Severity || got.Reason != plain.Reason || got.Fingerprint != plain.Fingerprint {
		t.Fatalf("single instance changed severity/reason/fingerprint: %+v", got)
	}
}

func TestDetectCNPGPrimaryPDBUnknownKeepsWarning(t *testing.T) {
	for _, mode := range []string{"no-cluster", "no-nodes", "blocking", "missing-cluster"} {
		t.Run(mode, func(t *testing.T) {
			f := newCNPGPDBFixture(2)
			switch mode {
			case "blocking":
				f.nodes["node-1"].Spec.Unschedulable = true
			case "missing-cluster":
				f.cluster = nil
			}
			got, plain := detectCNPGPDB(t, f, mode)
			if got.ExpectedWhenVerified || got.RequiredReads != nil || got.Degraded != nil || got.Message != plain.Message || got.Action != plain.Action {
				t.Fatalf("unverified budget changed: %+v", got)
			}
		})
	}
}
