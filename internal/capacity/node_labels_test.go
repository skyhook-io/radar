package capacity

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func labelledNode(labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n", Labels: labels}}
}

func TestNodeCapacityType(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"karpenter spot", map[string]string{"karpenter.sh/capacity-type": "spot", "cloud.google.com/gke-nodepool": "p"}, "spot"},
		{"karpenter reserved", map[string]string{"karpenter.sh/capacity-type": "reserved"}, "reserved"},
		{"eks managed spot", map[string]string{"eks.amazonaws.com/capacityType": "SPOT"}, "spot"},
		{"eks managed on-demand", map[string]string{"eks.amazonaws.com/capacityType": "ON_DEMAND"}, "on-demand"},
		{"gke spot", map[string]string{"cloud.google.com/gke-nodepool": "spot-pool", "cloud.google.com/gke-spot": "true"}, "spot"},
		{"gke preemptible", map[string]string{"cloud.google.com/gke-nodepool": "old", "cloud.google.com/gke-preemptible": "true"}, "preemptible"},
		// GKE and AKS label every spot node, so an unlabelled pool node is on-demand.
		{"gke standard", map[string]string{"cloud.google.com/gke-nodepool": "pool-1"}, "on-demand"},
		{"aks spot", map[string]string{"kubernetes.azure.com/agentpool": "np1", "kubernetes.azure.com/scalesetpriority": "spot"}, "spot"},
		{"aks regular", map[string]string{"kubernetes.azure.com/agentpool": "np1"}, "on-demand"},
		// Elsewhere a missing spot label says nothing.
		{"self-managed eks", map[string]string{"eks.amazonaws.com/nodegroup": "ng"}, ""},
		{"kind", map[string]string{"kubernetes.io/hostname": "kind-control-plane"}, ""},
	}
	for _, tc := range cases {
		if got := NodeCapacityType(labelledNode(tc.labels)); got != tc.want {
			t.Errorf("%s: NodeCapacityType = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNodePoolFollowsCapacityGroupIdentity(t *testing.T) {
	if name, source, ok := NodePool(labelledNode(map[string]string{"cloud.google.com/gke-nodepool": "pool-1"})); !ok || name != "pool-1" || source != "gke" {
		t.Errorf("gke pool = %q %q %v", name, source, ok)
	}
	if name, source, ok := NodePool(labelledNode(map[string]string{"karpenter.sh/nodepool": "default", "eks.amazonaws.com/nodegroup": "ng"})); !ok || name != "default" || source != "karpenter" {
		t.Errorf("karpenter must outrank the platform label, got %q %q %v", name, source, ok)
	}
	if _, _, ok := NodePool(labelledNode(map[string]string{"cloud.google.com/gke-nodepool": "a", "eks.amazonaws.com/nodegroup": "b"})); ok {
		t.Error("two platform identities are ambiguous and must stay unattributed")
	}
}
