package health

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestNode(t *testing.T) {
	tests := []struct {
		name              string
		node              *corev1.Node
		wantReady         bool
		wantUnschedulable bool
		wantPressures     int
	}{
		{
			name: "ready node",
			node: &corev1.Node{Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
				NodeInfo:   corev1.NodeSystemInfo{KubeletVersion: "v1.28.3"},
			}},
			wantReady: true,
		},
		{
			name: "not ready node",
			node: &corev1.Node{Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Message: "kubelet stopped"}},
			}},
			wantReady: false,
		},
		{
			name: "cordoned and ready",
			node: &corev1.Node{
				Spec:   corev1.NodeSpec{Unschedulable: true},
				Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
			},
			wantReady: true, wantUnschedulable: true,
		},
		{
			name: "memory pressure",
			node: &corev1.Node{Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
				},
			}},
			wantReady: true, wantPressures: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Node(tt.node)
			if got.Ready != tt.wantReady {
				t.Errorf("Ready = %v, want %v", got.Ready, tt.wantReady)
			}
			if got.Unschedulable != tt.wantUnschedulable {
				t.Errorf("Unschedulable = %v, want %v", got.Unschedulable, tt.wantUnschedulable)
			}
			if len(got.Pressures) != tt.wantPressures {
				t.Errorf("Pressures = %v, want %d", got.Pressures, tt.wantPressures)
			}
		})
	}
}
