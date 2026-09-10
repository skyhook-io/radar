package k8score

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func drainTestPod(name string, mutate ...func(*corev1.Pod)) corev1.Pod {
	yes := true
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "shop",
			Labels:    map[string]string{"app": "web"},
			OwnerReferences: []metav1.OwnerReference{{
				Kind: "ReplicaSet", Name: "web-abc", Controller: &yes,
			}},
		},
		Spec:   corev1.PodSpec{NodeName: "worker-1"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	for _, m := range mutate {
		m(&pod)
	}
	return pod
}

func withOwner(kind string, controller bool) func(*corev1.Pod) {
	return func(p *corev1.Pod) {
		c := controller
		p.OwnerReferences = []metav1.OwnerReference{{Kind: kind, Name: "owner", Controller: &c}}
	}
}

func withoutOwner(p *corev1.Pod) { p.OwnerReferences = nil }
func withEmptyDir(p *corev1.Pod) {
	p.Spec.Volumes = []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
}
func withMirror(p *corev1.Pod) { p.Annotations = map[string]string{corev1.MirrorPodAnnotationKey: "x"} }
func withDeletionTimestamp(p *corev1.Pod) {
	now := metav1.Now()
	p.DeletionTimestamp = &now
}
func withPhase(ph corev1.PodPhase) func(*corev1.Pod) {
	return func(p *corev1.Pod) { p.Status.Phase = ph }
}

func pdb(ns, name string, selector *metav1.LabelSelector, allowed int32) policyv1.PodDisruptionBudget {
	return policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       policyv1.PodDisruptionBudgetSpec{Selector: selector},
		Status:     policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: allowed},
	}
}

func TestClassifyPodForDrain(t *testing.T) {
	webSelector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}
	defaults := DrainOptions{IgnoreDaemonSets: true}

	tests := []struct {
		name string
		pod  corev1.Pod
		opts DrainOptions
		pdbs []policyv1.PodDisruptionBudget
		want DrainOutcome
	}{
		{name: "managed running pod is evicted", pod: drainTestPod("a"), opts: defaults, want: DrainOutcomeEvict},
		{name: "succeeded pod is skipped", pod: drainTestPod("a", withPhase(corev1.PodSucceeded)), opts: defaults, want: DrainOutcomeSkip},
		{name: "failed pod is skipped", pod: drainTestPod("a", withPhase(corev1.PodFailed)), opts: defaults, want: DrainOutcomeSkip},
		{name: "mirror pod is skipped even with force", pod: drainTestPod("a", withMirror), opts: DrainOptions{IgnoreDaemonSets: true, Force: true}, want: DrainOutcomeSkip},
		{name: "DaemonSet pod is skipped when ignored", pod: drainTestPod("a", withOwner("DaemonSet", true)), opts: defaults, want: DrainOutcomeSkip},
		{name: "DaemonSet pod is never evicted, even when not ignored", pod: drainTestPod("a", withOwner("DaemonSet", true)), opts: DrainOptions{}, want: DrainOutcomeSkip},
		{name: "terminating pod is evicted without consulting PDBs", pod: drainTestPod("a", withDeletionTimestamp), opts: defaults, pdbs: []policyv1.PodDisruptionBudget{pdb("shop", "web", webSelector, 0)}, want: DrainOutcomeEvict},
		{name: "terminating DaemonSet pod is still skipped", pod: drainTestPod("a", withOwner("DaemonSet", true), withDeletionTimestamp), opts: defaults, want: DrainOutcomeSkip},
		{name: "non-controller DaemonSet reference does not make a DaemonSet pod", pod: drainTestPod("a", withOwner("DaemonSet", false)), opts: defaults, want: DrainOutcomeSkip}, // unmanaged, no force
		{name: "unmanaged pod is skipped without force", pod: drainTestPod("a", withoutOwner), opts: defaults, want: DrainOutcomeSkip},
		{name: "unmanaged pod is evicted with force", pod: drainTestPod("a", withoutOwner), opts: DrainOptions{IgnoreDaemonSets: true, Force: true}, want: DrainOutcomeEvict},
		{name: "owner reference without controller flag counts as unmanaged", pod: drainTestPod("a", withOwner("ReplicaSet", false)), opts: defaults, want: DrainOutcomeSkip},
		{name: "emptyDir pod is skipped unless allowed", pod: drainTestPod("a", withEmptyDir), opts: defaults, want: DrainOutcomeSkip},
		{name: "emptyDir pod is evicted when allowed", pod: drainTestPod("a", withEmptyDir), opts: DrainOptions{IgnoreDaemonSets: true, DeleteEmptyDirData: true}, want: DrainOutcomeEvict},
		{name: "exhausted PDB may block", pod: drainTestPod("a"), opts: defaults, pdbs: []policyv1.PodDisruptionBudget{pdb("shop", "web", webSelector, 0)}, want: DrainOutcomeMayBlock},
		{name: "PDB with budget left does not block", pod: drainTestPod("a"), opts: defaults, pdbs: []policyv1.PodDisruptionBudget{pdb("shop", "web", webSelector, 1)}, want: DrainOutcomeEvict},
		{name: "PDB in another namespace is ignored", pod: drainTestPod("a"), opts: defaults, pdbs: []policyv1.PodDisruptionBudget{pdb("other", "web", webSelector, 0)}, want: DrainOutcomeEvict},
		{name: "PDB with non-matching selector is ignored", pod: drainTestPod("a"), opts: defaults, pdbs: []policyv1.PodDisruptionBudget{pdb("shop", "db", &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}, 0)}, want: DrainOutcomeEvict},
		{name: "PDB with nil selector matches nothing", pod: drainTestPod("a"), opts: defaults, pdbs: []policyv1.PodDisruptionBudget{pdb("shop", "none", nil, 0)}, want: DrainOutcomeEvict},
		{name: "PDB with empty selector matches every pod in the namespace", pod: drainTestPod("a"), opts: defaults, pdbs: []policyv1.PodDisruptionBudget{pdb("shop", "all", &metav1.LabelSelector{}, 0)}, want: DrainOutcomeMayBlock},
		{name: "skip decisions win over PDB", pod: drainTestPod("a", withEmptyDir), opts: defaults, pdbs: []policyv1.PodDisruptionBudget{pdb("shop", "web", webSelector, 0)}, want: DrainOutcomeSkip},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyPodForDrain(tt.pod, tt.opts, tt.pdbs)
			if got.Outcome != tt.want {
				t.Fatalf("outcome = %q (%s), want %q", got.Outcome, got.Reason, tt.want)
			}
			if got.Reason == "" {
				t.Fatalf("every decision must carry an operator-facing reason")
			}
			if got.Namespace != tt.pod.Namespace || got.Name != tt.pod.Name {
				t.Fatalf("decision must identify the pod: %+v", got)
			}
			if tt.want == DrainOutcomeMayBlock && got.PDB == "" {
				t.Fatalf("may-block decision must name the PDB: %+v", got)
			}
			if got.EmptyDir != hasLocalStorage(tt.pod) {
				t.Fatalf("emptyDir flag must reflect the pod's volumes regardless of outcome: %+v", got)
			}
		})
	}
}

func TestPlanNodeDrainIsReadOnlyAndCountsOutcomes(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
		func() *corev1.Pod { p := drainTestPod("web-1"); return &p }(),
		func() *corev1.Pod { p := drainTestPod("web-2", withEmptyDir); return &p }(),
		func() *corev1.Pod { p := drainTestPod("agent", withOwner("DaemonSet", true)); return &p }(),
		func() *corev1.Pod { p := drainTestPod("elsewhere"); p.Spec.NodeName = "worker-2"; return &p }(),
		&policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"},
			Spec:       policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}},
			Status:     policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 0},
		},
	)
	// the fake clientset does not implement field selectors; filter like the API server would
	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		list := &corev1.PodList{}
		all, _ := client.Tracker().List(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, "")
		for _, p := range all.(*corev1.PodList).Items {
			if p.Spec.NodeName == "worker-1" {
				list.Items = append(list.Items, p)
			}
		}
		return true, list, nil
	})

	plan, err := PlanNodeDrain(context.Background(), client, "worker-1", DrainOptions{IgnoreDaemonSets: true})
	if err != nil {
		t.Fatalf("PlanNodeDrain: %v", err)
	}
	for _, a := range client.Actions() {
		switch a.GetVerb() {
		case "get", "list":
		default:
			t.Fatalf("plan must not mutate the cluster, saw %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}
	if !plan.Estimate || plan.Node != "worker-1" || plan.GeneratedAt.IsZero() {
		t.Fatalf("plan header is incomplete: %+v", plan)
	}
	if !plan.PDBsEvaluated {
		t.Fatalf("PDBs were listable and must be reported as evaluated")
	}
	for _, d := range plan.Pods {
		if !d.PDBChecked {
			t.Fatalf("every pod on a plan with readable PDBs must be marked pdbChecked: %+v", d)
		}
	}
	if got := plan.Summary; got.Evict != 0 || got.Skip != 2 || got.MayBlock != 1 {
		t.Fatalf("summary = %+v, want evict=0 skip=2 mayBlock=1", got)
	}
	if len(plan.Pods) != 3 {
		t.Fatalf("plan must list only pods on the node, got %d", len(plan.Pods))
	}
}

func TestPlanNodeDrainReportsUnevaluatedPDBs(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
		func() *corev1.Pod { p := drainTestPod("web-1"); return &p }(),
	)
	client.PrependReactor("list", "poddisruptionbudgets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "policy", Resource: "poddisruptionbudgets"}, "", nil)
	})

	plan, err := PlanNodeDrain(context.Background(), client, "worker-1", DrainOptions{IgnoreDaemonSets: true})
	if err != nil {
		t.Fatalf("a forbidden PDB list must not fail the plan: %v", err)
	}
	if plan.PDBsEvaluated || plan.PDBError == "" {
		t.Fatalf("plan must say PDBs were not evaluated instead of reporting zero: %+v", plan)
	}
	if plan.Summary.MayBlock != 0 || plan.Pods[0].Outcome != DrainOutcomeEvict || plan.Pods[0].PDBChecked {
		t.Fatalf("pod must be classified without PDB knowledge and flagged: %+v", plan.Pods[0])
	}
}

func TestPlanNodeDrainUnknownNode(t *testing.T) {
	client := fake.NewSimpleClientset()
	_, err := PlanNodeDrain(context.Background(), client, "ghost", DrainOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("want NotFound for an unknown node, got %v", err)
	}
}

func TestDrainNodeReportsSkippedPodsWithoutPDBClaims(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
		func() *corev1.Pod { p := drainTestPod("bare", withoutOwner); return &p }(),
	)
	res, err := DrainNode(context.Background(), client, "worker-1", DrainOptions{IgnoreDaemonSets: true, Timeout: time.Second})
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if len(res.SkippedPods) != 1 || res.SkippedPods[0].Outcome != DrainOutcomeSkip || res.SkippedPods[0].PDBChecked {
		t.Fatalf("skipped pods must be reported with pdbChecked=false (PDBs are not consulted during execution): %+v", res.SkippedPods)
	}
}
