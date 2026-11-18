package k8s

import (
	"reflect"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const cnpgTestNS = CNPGPDBTestNamespace

func (f *CNPGPDBTestFixture) classify() (cnpgPDBVerdict, string) {
	return classifyCNPGPrimaryPDB(f.PDB, f.Cluster, f.Pods, f.PDBs, func(name string) *corev1.Node { return f.Nodes[name] })
}

func TestClassifyCNPGPrimaryPDB(t *testing.T) {
	notReady := []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	for _, tc := range []struct {
		name      string
		instances int
		mutate    func(*CNPGPDBTestFixture)
		want      cnpgPDBVerdict
	}{
		{"two instances on two usable nodes", 2, nil, cnpgPDBExpected},
		{"three instances", 3, nil, cnpgPDBExpected},
		{"single instance", 1, nil, cnpgPDBSingleInstance},
		{"single instance needs no pods or nodes", 1, func(f *CNPGPDBTestFixture) { f.Pods, f.Nodes = nil, nil }, cnpgPDBSingleInstance},

		{"name is not <cluster>-primary", 2, func(f *CNPGPDBTestFixture) { f.PDB.Name = "pg-custom" }, cnpgPDBNone},
		{"owner UID mismatch", 2, func(f *CNPGPDBTestFixture) { f.PDB.OwnerReferences[0].UID = "other" }, cnpgPDBNone},
		{"owner not controller", 2, func(f *CNPGPDBTestFixture) { f.PDB.OwnerReferences[0].Controller = nil }, cnpgPDBNone},
		{"owner group foreign", 2, func(f *CNPGPDBTestFixture) { f.PDB.OwnerReferences[0].APIVersion = "other.io/v1" }, cnpgPDBNone},
		{"second owner reference", 2, func(f *CNPGPDBTestFixture) {
			f.PDB.OwnerReferences = append(f.PDB.OwnerReferences, metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "x", UID: "x"})
		}, cnpgPDBNone},
		{"minAvailable percent", 2, func(f *CNPGPDBTestFixture) { v := intstr.FromString("100%"); f.PDB.Spec.MinAvailable = &v }, cnpgPDBNone},
		{"maxUnavailable set", 2, func(f *CNPGPDBTestFixture) { v := intstr.FromInt32(0); f.PDB.Spec.MaxUnavailable = &v }, cnpgPDBNone},
		{"extra matchLabel", 2, func(f *CNPGPDBTestFixture) { f.PDB.Spec.Selector.MatchLabels["extra"] = "x" }, cnpgPDBNone},
		{"matchExpression", 2, func(f *CNPGPDBTestFixture) {
			f.PDB.Spec.Selector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: "x", Operator: metav1.LabelSelectorOpDoesNotExist}}
		}, cnpgPDBNone},
		{"status counts not 1", 2, func(f *CNPGPDBTestFixture) {
			f.PDB.Status.ExpectedPods, f.PDB.Status.CurrentHealthy, f.PDB.Status.DesiredHealthy = 2, 2, 2
		}, cnpgPDBNone},
		{"stale budget status", 2, func(f *CNPGPDBTestFixture) { f.PDB.Generation = 2 }, cnpgPDBNone},
		{"cluster deleting", 2, func(f *CNPGPDBTestFixture) { ts := metav1.Now(); f.Cluster.SetDeletionTimestamp(&ts) }, cnpgPDBNone},
		{"single instance cluster deleting", 1, func(f *CNPGPDBTestFixture) { ts := metav1.Now(); f.Cluster.SetDeletionTimestamp(&ts) }, cnpgPDBNone},
		{"cluster missing", 2, func(f *CNPGPDBTestFixture) { f.Cluster = nil }, cnpgPDBNone},

		{"switchover in progress", 2, func(f *CNPGPDBTestFixture) {
			_ = unstructured.SetNestedField(f.Cluster.Object, "Switchover in progress", "status", "phase")
		}, cnpgPDBBlocking},
		{"upgrading", 2, func(f *CNPGPDBTestFixture) {
			_ = unstructured.SetNestedField(f.Cluster.Object, "Upgrading cluster", "status", "phase")
		}, cnpgPDBBlocking},
		{"target primary differs", 2, func(f *CNPGPDBTestFixture) {
			_ = unstructured.SetNestedField(f.Cluster.Object, "pg-2", "status", "targetPrimary")
		}, cnpgPDBBlocking},
		{"not all instances ready", 2, func(f *CNPGPDBTestFixture) {
			_ = unstructured.SetNestedField(f.Cluster.Object, int64(1), "status", "readyInstances")
		}, cnpgPDBBlocking},
		{"maintenance window in progress", 2, func(f *CNPGPDBTestFixture) {
			_ = unstructured.SetNestedField(f.Cluster.Object, true, "spec", "nodeMaintenanceWindow", "inProgress")
		}, cnpgPDBBlocking},
		{"standby shares the primary's node", 3, func(f *CNPGPDBTestFixture) { f.Pods[2].Spec.NodeName = "node-1" }, cnpgPDBBlocking},
		{"primary node cordoned", 2, func(f *CNPGPDBTestFixture) { f.Nodes["node-1"].Spec.Unschedulable = true }, cnpgPDBBlocking},
		{"primary node drain-tainted", 2, func(f *CNPGPDBTestFixture) {
			f.Nodes["node-1"].Spec.Taints = []corev1.Taint{{Key: "karpenter.sh/disrupted", Effect: corev1.TaintEffectNoSchedule}}
		}, cnpgPDBBlocking},
		{"primary node missing", 2, func(f *CNPGPDBTestFixture) { delete(f.Nodes, "node-1") }, cnpgPDBBlocking},
		{"custom untolerated drain taint on the standby's node", 2, func(f *CNPGPDBTestFixture) {
			f.Nodes["node-2"].Spec.Taints = []corev1.Taint{{Key: "example.com/draining", Effect: corev1.TaintEffectNoSchedule}}
		}, cnpgPDBBlocking},
		{"custom untolerated NoExecute taint on the primary's node", 2, func(f *CNPGPDBTestFixture) {
			f.Nodes["node-1"].Spec.Taints = []corev1.Taint{{Key: "example.com/evict", Effect: corev1.TaintEffectNoExecute}}
		}, cnpgPDBBlocking},
		{"dedicated nodes the instances tolerate", 2, func(f *CNPGPDBTestFixture) {
			for _, n := range f.Nodes {
				n.Spec.Taints = []corev1.Taint{{Key: "dedicated", Value: "postgres", Effect: corev1.TaintEffectNoSchedule}}
			}
			for _, p := range f.Pods {
				p.Spec.Tolerations = []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "postgres", Effect: corev1.TaintEffectNoSchedule}}
			}
		}, cnpgPDBExpected},
		{"PreferNoSchedule is ignored", 2, func(f *CNPGPDBTestFixture) {
			f.Nodes["node-2"].Spec.Taints = []corev1.Taint{{Key: "example.com/soft", Effect: corev1.TaintEffectPreferNoSchedule}}
		}, cnpgPDBExpected},
		{"a tolerated default drain taint still counts", 2, func(f *CNPGPDBTestFixture) {
			f.Nodes["node-2"].Spec.Taints = []corev1.Taint{{Key: "karpenter.sh/disrupted", Effect: corev1.TaintEffectNoSchedule}}
			f.Pods[1].Spec.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
		}, cnpgPDBBlocking},
		{"only standby's node cordoned", 2, func(f *CNPGPDBTestFixture) { f.Nodes["node-2"].Spec.Unschedulable = true }, cnpgPDBBlocking},
		{"only standby's node not ready", 2, func(f *CNPGPDBTestFixture) {
			f.Nodes["node-2"].Status.Conditions[0].Status = corev1.ConditionUnknown
		}, cnpgPDBBlocking},
		{"only standby's node missing", 2, func(f *CNPGPDBTestFixture) { delete(f.Nodes, "node-2") }, cnpgPDBBlocking},
		{"only standby not ready", 2, func(f *CNPGPDBTestFixture) { f.Pods[1].Status.Conditions = notReady }, cnpgPDBBlocking},
		{"one usable standby is enough", 3, func(f *CNPGPDBTestFixture) { f.Nodes["node-2"].Spec.Unschedulable = true }, cnpgPDBExpected},
		{"primary pod not ready", 2, func(f *CNPGPDBTestFixture) { f.Pods[0].Status.Conditions = notReady }, cnpgPDBBlocking},
		{"primary pod deleting", 2, func(f *CNPGPDBTestFixture) { ts := metav1.Now(); f.Pods[0].DeletionTimestamp = &ts }, cnpgPDBBlocking},
		{"budget selects a pod that is not the current primary", 2, func(f *CNPGPDBTestFixture) { f.Pods[0].Name = "pg-9" }, cnpgPDBBlocking},
		{"budget selects two pods", 2, func(f *CNPGPDBTestFixture) { f.Pods[1].Labels["cnpg.io/instanceRole"] = "primary" }, cnpgPDBBlocking},
		{"selected pod is not an instance of this cluster", 2, func(f *CNPGPDBTestFixture) { f.Pods[0].OwnerReferences = nil }, cnpgPDBBlocking},
		{"pods unknown", 2, func(f *CNPGPDBTestFixture) { f.Pods = nil }, cnpgPDBBlocking},
		{"a second budget selects the primary", 2, func(f *CNPGPDBTestFixture) {
			other := f.PDB.DeepCopy()
			other.Name, other.OwnerReferences = "custom", nil
			f.PDBs = append(f.PDBs, other)
		}, cnpgPDBBlocking},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := NewCNPGPDBTestFixture(tc.instances)
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
func detectCNPGPDB(t *testing.T, f *CNPGPDBTestFixture, mode string) (Detection, Detection) {
	t.Helper()
	t.Cleanup(ResetTestState)
	t.Cleanup(ResetTestDynamicState)
	if err := f.Install(mode); err != nil {
		t.Fatal(err)
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
	got, plain := detectCNPGPDB(t, NewCNPGPDBTestFixture(2), "")
	if !got.ExpectedWhenVerified {
		t.Fatalf("healthy HA primary budget not marked expected: %+v", got)
	}
	// Nodes first: a restricted caller fails on one cluster-wide check.
	wantReads := []issuesapi.EvidenceRead{
		{Resource: "nodes", Verb: "list", Exact: true},
		{Group: "postgresql.cnpg.io", Resource: "clusters", Namespace: cnpgTestNS, Verb: "list", Exact: true},
		{Resource: "pods", Namespace: cnpgTestNS, Verb: "list", Exact: true},
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
	got, plain := detectCNPGPDB(t, NewCNPGPDBTestFixture(1), "")
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
	for _, mode := range []string{"no-cluster", "no-nodes", "missing-cluster"} {
		t.Run(mode, func(t *testing.T) {
			f := NewCNPGPDBTestFixture(2)
			switch mode {
			case "missing-cluster":
				f.Cluster = nil
			}
			got, plain := detectCNPGPDB(t, f, mode)
			if got.ExpectedWhenVerified || got.RequiredReads != nil || got.Degraded != nil || got.Message != plain.Message || got.Action != plain.Action {
				t.Fatalf("unverified budget changed: %+v", got)
			}
		})
	}
}

// A verified operator budget outside the idle layout keeps the warning, with
// CloudNativePG's own advice and the classifier's reason instead of the
// generic advice to relax the budget.
func TestDetectCNPGPrimaryPDBBlockingExplainsTheOperator(t *testing.T) {
	f := NewCNPGPDBTestFixture(2)
	f.Nodes["node-2"].Spec.Unschedulable = true
	got, plain := detectCNPGPDB(t, f, "")
	_, reason := classifyCNPGPrimaryPDB(f.PDB, f.Cluster, f.Pods, f.PDBs, func(n string) *corev1.Node { return f.Nodes[n] })
	if got.ExpectedWhenVerified || got.Message != cnpgBlockingPDBMessage(reason) || got.Action != cnpgBlockingPDBAction {
		t.Fatalf("blocking copy missing: %+v", got)
	}
	if !strings.Contains(got.Message, "no ready standby is on a schedulable node") || strings.Contains(got.Action, "Relax") {
		t.Fatalf("blocking copy = %q / %q", got.Message, got.Action)
	}
	wantReads := []issuesapi.EvidenceRead{
		{Resource: "nodes", Verb: "list", Exact: true},
		{Group: "postgresql.cnpg.io", Resource: "clusters", Namespace: cnpgTestNS, Verb: "list", Exact: true},
		{Resource: "pods", Namespace: cnpgTestNS, Verb: "list", Exact: true},
	}
	if !reflect.DeepEqual(got.RequiredReads, wantReads) {
		t.Fatalf("reads = %+v", got.RequiredReads)
	}
	if got.Degraded == nil || got.Degraded.Message != plain.Message || got.Degraded.Action != plain.Action || !got.Degraded.KeepSubjectOnset {
		t.Fatalf("degraded form must be the unrecognised warning: %+v", got.Degraded)
	}
	if got.Severity != plain.Severity || got.Reason != plain.Reason || got.Fingerprint != plain.Fingerprint {
		t.Fatalf("blocking changed severity/reason/fingerprint: %+v", got)
	}
}
