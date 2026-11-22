package upgradereadiness

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Upgrade impact keeps reporting CloudNativePG's primary budget as a drain
// blocker; only Issues treats it as expected protection.
func TestCNPGPrimaryBudgetStillBlocksUpgrade(t *testing.T) {
	yes, one := true, intstr.FromInt32(1)
	owner := metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg", UID: "cluster-uid", Controller: &yes}
	labels := map[string]string{"cnpg.io/cluster": "pg", "cnpg.io/instanceRole": "primary"}
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-primary", Namespace: "db", Generation: 1, OwnerReferences: []metav1.OwnerReference{owner}},
		Spec:       policyv1.PodDisruptionBudgetSpec{MinAvailable: &one, Selector: &metav1.LabelSelector{MatchLabels: labels}},
		Status:     policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1, ExpectedPods: 1, CurrentHealthy: 1, DesiredHealthy: 1},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-1", Namespace: "db", Labels: labels, OwnerReferences: []metav1.OwnerReference{owner}},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	input := completeInput()
	input.Namespaces = []string{"db"}
	input.PodDisruptionBudgets = []*policyv1.PodDisruptionBudget{pdb}
	input.Pods = []*corev1.Pod{pod}
	result, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	got := checkByID(t, result, "node-drain-feasibility")
	if got.Status != CheckBlocked || len(got.Findings) != 1 || got.Findings[0].Level != LevelBlocker {
		t.Fatalf("CloudNativePG primary budget no longer blocks upgrade: %+v", got)
	}
}
