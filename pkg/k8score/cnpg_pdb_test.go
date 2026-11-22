package k8score

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// CloudNativePG's primary budget stays a drain blocker here even where Issues
// treats it as expected: the drain plan reports what the Eviction API does.
func TestCNPGPrimaryBudgetStillBlocksTheDrainPlan(t *testing.T) {
	yes, one := true, intstr.FromInt32(1)
	owner := metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg", UID: "cluster-uid", Controller: &yes}
	labels := map[string]string{"cnpg.io/cluster": "pg", "cnpg.io/instanceRole": "primary"}
	pdb := policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-primary", Namespace: "db", Generation: 1, OwnerReferences: []metav1.OwnerReference{owner}},
		Spec:       policyv1.PodDisruptionBudgetSpec{MinAvailable: &one, Selector: &metav1.LabelSelector{MatchLabels: labels}},
		Status:     policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1, ExpectedPods: 1, CurrentHealthy: 1, DesiredHealthy: 1},
	}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-1", Namespace: "db", Labels: labels, OwnerReferences: []metav1.OwnerReference{owner}},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	if got := ClassifyPodForDrain(pod, DrainOptions{}, []policyv1.PodDisruptionBudget{pdb}); got.Outcome != DrainOutcomeMayBlock || got.PDB != "db/pg-primary" {
		t.Fatalf("CloudNativePG primary budget no longer blocks the drain plan: %+v", got)
	}
	if PodDisruptionBudgetEvictionState(&pdb) != PDBEvictionBlocked {
		t.Fatal("CloudNativePG primary budget no longer reads as blocked")
	}
}
