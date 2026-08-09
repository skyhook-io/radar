package k8s

import (
	"testing"

	capacitymodel "github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/pkg/karpenter"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
)

// unschedulablePod builds a Pending pod the scheduler rejected, so the
// bind-time detector emits a Detection for it.
func unschedulablePod(name string, spec corev1.PodSpec) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod"},
		Spec:       spec,
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  "Unschedulable",
				Message: "0/1 nodes are available: 1 Insufficient cpu.",
			}},
		},
	}
}

func detectionFor(problems []Detection, name string) (Detection, bool) {
	for _, p := range problems {
		if p.Name == name {
			return p, true
		}
	}
	return Detection{}, false
}

func TestPodRequiresKarpenterNodePool(t *testing.T) {
	nodeAffinity := func(key string) *corev1.Affinity {
		return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      key,
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"pool-a"},
					}},
				}},
			},
		}}
	}

	opAffinity := func(op corev1.NodeSelectorOperator, values ...string) *corev1.Affinity {
		return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      karpenter.NodePoolLabelKey,
						Operator: op,
						Values:   values,
					}},
				}},
			},
		}}
	}

	cases := []struct {
		name string
		spec corev1.PodSpec
		want bool
	}{
		{
			name: "nodeSelector on nodepool key",
			spec: corev1.PodSpec{NodeSelector: map[string]string{karpenter.NodePoolLabelKey: "pool-a"}},
			want: true,
		},
		{
			name: "required nodeAffinity on nodepool key",
			spec: corev1.PodSpec{Affinity: nodeAffinity(karpenter.NodePoolLabelKey)},
			want: true,
		},
		{
			name: "unrelated nodeSelector",
			spec: corev1.PodSpec{NodeSelector: map[string]string{"kubernetes.io/arch": "arm64"}},
			want: false,
		},
		{
			name: "unrelated nodeAffinity",
			spec: corev1.PodSpec{Affinity: nodeAffinity("kubernetes.io/arch")},
			want: false,
		},
		{
			name: "no placement constraints",
			spec: corev1.PodSpec{},
			want: false,
		},
		{
			name: "Exists on nodepool key qualifies",
			spec: corev1.PodSpec{Affinity: opAffinity(corev1.NodeSelectorOpExists)},
			want: true,
		},
		{
			name: "DoesNotExist on nodepool key must not qualify (wants OFF a pool)",
			spec: corev1.PodSpec{Affinity: opAffinity(corev1.NodeSelectorOpDoesNotExist)},
			want: false,
		},
		{
			name: "NotIn on nodepool key must not qualify (wants OFF listed pools)",
			spec: corev1.PodSpec{Affinity: opAffinity(corev1.NodeSelectorOpNotIn, "pool-a")},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{Spec: tc.spec}
			if got := podRequiresKarpenterNodePool(pod); got != tc.want {
				t.Fatalf("podRequiresKarpenterNodePool = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectSchedulingProblems_CapacityRelevant(t *testing.T) {
	defer ResetTestState()

	nodeSelectorPod := unschedulablePod("nodepool-selector", corev1.PodSpec{
		NodeSelector: map[string]string{karpenter.NodePoolLabelKey: "pool-a"},
	})
	affinityPod := unschedulablePod("nodepool-affinity", corev1.PodSpec{
		Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      karpenter.NodePoolLabelKey,
						Operator: corev1.NodeSelectorOpExists,
					}},
				}},
			},
		}},
	})
	genericPod := unschedulablePod("generic", corev1.PodSpec{
		NodeSelector: map[string]string{"kubernetes.io/arch": "arm64"},
	})

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{"kubernetes.io/arch": "amd64"}}}
	if err := InitTestResourceCache(fake.NewClientset(node, nodeSelectorPod, affinityPod, genericPod)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	problems := DetectSchedulingProblems(GetResourceCache(), "prod")

	for _, name := range []string{"nodepool-selector", "nodepool-affinity"} {
		det, ok := detectionFor(problems, name)
		if !ok {
			t.Fatalf("expected Unschedulable detection for %q, got %+v", name, problems)
		}
		if !det.CapacityRelevant {
			t.Errorf("%q: CapacityRelevant = false, want true", name)
		}
	}

	det, ok := detectionFor(problems, "generic")
	if !ok {
		t.Fatalf("expected Unschedulable detection for generic pod, got %+v", problems)
	}
	if det.CapacityRelevant {
		t.Errorf("generic pod: CapacityRelevant = true, want false")
	}
}

// A pod that pins itself to a Karpenter NodePool but schedules fine produces no
// scheduling detection at all — so the flag is moot.
func TestDetectSchedulingProblems_NodePoolPinnedButScheduled(t *testing.T) {
	defer ResetTestState()

	scheduled := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "running", Namespace: "prod"},
		Spec: corev1.PodSpec{
			NodeName:     "n1",
			NodeSelector: map[string]string{karpenter.NodePoolLabelKey: "pool-a"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodScheduled,
				Status: corev1.ConditionTrue,
			}},
		},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}
	if err := InitTestResourceCache(fake.NewClientset(node, scheduled)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	if problems := DetectSchedulingProblems(GetResourceCache(), "prod"); len(problems) != 0 {
		t.Fatalf("expected no scheduling detections for a scheduled pod, got %+v", problems)
	}
}

func TestPodEvaluatedAgainstKarpenterPools(t *testing.T) {
	pool := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1", "kind": "NodePool",
		"metadata": map[string]any{"name": "pool-a"},
	}}
	pools := []capacitymodel.DemandPoolInput{{NodePool: pool}}

	// The widened semantic: an evaluated pod qualifies REGARDLESS of the
	// result. This pod's undeclared selector would evaluate incompatible with
	// every pool — exactly the GPU-demand-no-pool-can-serve archetype — and
	// that is the case where withholding the Demand link would hide the
	// diagnosis behind the worst news.
	rejected := unschedulablePod("rejected", corev1.PodSpec{
		NodeSelector: map[string]string{"radar.test/undeclared": "true"},
	})
	if !podEvaluatedAgainstKarpenterPools(rejected, pools) {
		t.Fatal("an evaluated-and-rejected pod must still correlate — bad news is still the answer")
	}
	if podEvaluatedAgainstKarpenterPools(rejected, nil) {
		t.Fatal("no pools means no evaluation — the bit must stay unset")
	}
	scheduled := rejected.DeepCopy()
	scheduled.Spec.NodeName = "n1"
	if podEvaluatedAgainstKarpenterPools(scheduled, pools) {
		t.Fatal("a scheduled pod builds no demand group and must not correlate")
	}
	if podEvaluatedAgainstKarpenterPools(nil, pools) {
		t.Fatal("nil pod must not correlate")
	}
}
