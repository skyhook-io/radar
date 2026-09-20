package audit

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func resilienceInput(nodes ...string) *CheckInput {
	owner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "deployment-uid", Controller: ptr(true)}
	input := &CheckInput{Deployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app", UID: owner.UID}, Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(3)), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "shared"}}}}}, ReplicaSets: []*appsv1.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{Name: "web-rs", Namespace: "app", UID: "rs-uid", OwnerReferences: []metav1.OwnerReference{owner}}}}, Pods: []*corev1.Pod{}}
	for i, node := range nodes {
		input.Pods = append(input.Pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: string(rune('a' + i)), Namespace: "app", UID: types.UID(string(rune('a' + i))), Labels: map[string]string{"app": "shared"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-rs", UID: "rs-uid", Controller: ptr(true)}}}, Spec: corev1.PodSpec{NodeName: node}, Status: corev1.PodStatus{Phase: corev1.PodRunning}})
	}
	return input
}

func TestPodHARiskOwnershipAndCurrentPlacement(t *testing.T) {
	tests := []struct {
		name                string
		mutate              func(*CheckInput)
		findings, evaluated int
		missing             string
	}{
		{"colocated", func(*CheckInput) {}, 1, 1, ""},
		{"distributed", func(i *CheckInput) { i.Pods[1].Spec.NodeName = "node-b" }, 0, 1, ""},
		{"running unready remains placement", func(i *CheckInput) {
			for _, p := range i.Pods {
				p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
			}
		}, 1, 1, ""},
		{"assigned pending excluded", func(i *CheckInput) { i.Pods[1].Status.Phase = corev1.PodPending }, 0, 0, ""},
		{"completed excluded", func(i *CheckInput) { i.Pods[1].Status.Phase = corev1.PodSucceeded }, 0, 0, ""},
		{"failed excluded", func(i *CheckInput) { i.Pods[1].Status.Phase = corev1.PodFailed }, 0, 0, ""},
		{"terminating excluded", func(i *CheckInput) { now := metav1.Now(); i.Pods[1].DeletionTimestamp = &now }, 0, 0, ""},
		{"unassigned excluded", func(i *CheckInput) { i.Pods[1].Spec.NodeName = "" }, 0, 0, ""},
		{"bare matching stranger excluded", func(i *CheckInput) { i.Pods[1].OwnerReferences = nil }, 0, 0, ""},
		{"selector drift cannot erase ownership", func(i *CheckInput) { i.Pods[1].Labels = nil }, 1, 1, ""},
		{"rolling revisions same deployment", func(i *CheckInput) {
			rs := i.ReplicaSets[0].DeepCopy()
			rs.Name = "revision-two"
			rs.UID = "second-rs"
			i.ReplicaSets = append(i.ReplicaSets, rs)
			i.Pods[1].OwnerReferences[0].Name = rs.Name
			i.Pods[1].OwnerReferences[0].UID = rs.UID
		}, 1, 1, ""},
		{"overlapping selector other deployment", func(i *CheckInput) {
			rs := i.ReplicaSets[0].DeepCopy()
			rs.Name = "other-rs"
			rs.UID = "other-rs"
			rs.OwnerReferences[0].UID = "other-deployment"
			rs.OwnerReferences[0].Name = "other"
			i.ReplicaSets = append(i.ReplicaSets, rs)
			i.Pods[1].OwnerReferences[0].Name = rs.Name
			i.Pods[1].OwnerReferences[0].UID = rs.UID
		}, 0, 0, ""},
		{"custom rollout parent excluded", func(i *CheckInput) {
			i.ReplicaSets[0].OwnerReferences[0].Kind = "Rollout"
			i.ReplicaSets[0].OwnerReferences[0].APIVersion = "argoproj.io/v1alpha1"
		}, 0, 0, ""},
		{"recreated deployment UID mismatch", func(i *CheckInput) { i.Deployments[0].UID = "recreated" }, 0, 0, ""},
		{"recreated replicaSet UID mismatch", func(i *CheckInput) { i.ReplicaSets[0].UID = "recreated" }, 0, 0, "replicaset-ownership"},
		{"partial replicaSet namespace inventory", func(i *CheckInput) { i.ReplicaSets = []*appsv1.ReplicaSet{} }, 0, 0, "replicaset-ownership"},
		{"nil replicaSet inventory", func(i *CheckInput) { i.ReplicaSets = nil }, 0, 0, "replicasets"},
		{"nil pod inventory", func(i *CheckInput) { i.Pods = nil }, 0, 0, "pods"},
		{"missing deployment UID", func(i *CheckInput) { i.Deployments[0].UID = "" }, 0, 0, ""},
		{"noncontroller reference excluded", func(i *CheckInput) { i.Pods[1].OwnerReferences[0].Controller = ptr(false) }, 0, 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			i := resilienceInput("node-a", "node-a")
			tc.mutate(i)
			r := RunChecks(i)
			count := 0
			for _, f := range r.Findings {
				if f.CheckID == "podHARisk" {
					count++
				}
			}
			if count != tc.findings || r.CheckCounts["podHARisk"].Evaluated != tc.evaluated {
				t.Fatalf("findings=%d counts=%+v", count, r.CheckCounts["podHARisk"])
			}
			if tc.missing != "" && !slices.Contains(r.MissingInputs, tc.missing) {
				t.Fatalf("missing %s: %+v", tc.missing, r.MissingInputs)
			}
			if r.CheckCounts["podHARisk"].Passed != tc.evaluated-tc.findings {
				t.Fatal("incorrect passing count")
			}
		})
	}
}

func TestPodHARiskStaleOutsiderCannotMaskColocation(t *testing.T) {
	for _, phase := range []corev1.PodPhase{corev1.PodPending, corev1.PodSucceeded, corev1.PodFailed} {
		i := resilienceInput("node-a", "node-a", "node-b")
		i.Pods[2].Status.Phase = phase
		r := RunChecks(i)
		if r.CheckCounts["podHARisk"] != (CheckCount{Evaluated: 1, Passed: 0}) {
			t.Fatalf("phase%s masks colocated running pods: %+v", phase, r.CheckCounts)
		}
	}
}

func TestPodHARiskUnknownOwnershipIsNamespaceScopedAndPreservesOtherChecks(t *testing.T) {
	i := resilienceInput("node-a", "node-a")
	unknown := i.Pods[0].DeepCopy()
	unknown.Name = "unknown"
	unknown.Namespace = "other"
	i.Pods = append(i.Pods, unknown)
	r := RunChecks(i)
	if r.CheckCounts["podHARisk"].Evaluated != 1 {
		t.Fatal("other namespace contaminated evaluated deployment")
	}
	unknown.Namespace = "app"
	unknown.OwnerReferences[0].UID = "missing"
	r = RunChecks(i)
	if r.CheckCounts["podHARisk"].Evaluated != 0 || !slices.Contains(r.MissingInputs, "replicaset-ownership") {
		t.Fatal("unknown was counted as passing")
	}
	found := false
	for _, f := range r.Findings {
		if f.CheckID == "missingTopologySpread" {
			found = true
		}
	}
	if !found {
		t.Fatal("independent topology policy finding lost")
	}
}
