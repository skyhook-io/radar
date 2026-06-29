package k8s

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func TestDetectNodeProblems(t *testing.T) {
	tests := []struct {
		name         string
		nodes        []*corev1.Node
		wantCount    int
		wantSeverity string // first problem severity if any
		wantProblem  string // first problem type if any
	}{
		{
			name: "no problems",
			nodes: []*corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "node1"},
					Status: corev1.NodeStatus{
						Conditions: []corev1.NodeCondition{
							{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
						},
					},
				},
			},
			wantCount: 0,
		},
		{
			name: "mixed problems",
			nodes: []*corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "not-ready"},
					Status: corev1.NodeStatus{
						Conditions: []corev1.NodeCondition{
							{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "cordoned"},
					Spec:       corev1.NodeSpec{Unschedulable: true},
					Status: corev1.NodeStatus{
						Conditions: []corev1.NodeCondition{
							{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
						},
					},
				},
			},
			wantCount:    2,
			wantSeverity: "critical",
			wantProblem:  "NotReady",
		},
		{
			name: "cordoned only",
			nodes: []*corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "cordoned"},
					Spec:       corev1.NodeSpec{Unschedulable: true},
					Status: corev1.NodeStatus{
						Conditions: []corev1.NodeCondition{
							{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
						},
					},
				},
			},
			wantCount:    1,
			wantSeverity: "medium",
			wantProblem:  "Cordoned",
		},
		{
			name: "pressure conditions",
			nodes: []*corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pressured"},
					Status: corev1.NodeStatus{
						Conditions: []corev1.NodeCondition{
							{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
							{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
							{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue},
						},
					},
				},
			},
			wantCount:    2,
			wantSeverity: "critical",
			wantProblem:  "MemoryPressure",
		},
		{
			name: "not ready with pressure produces both",
			nodes: []*corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "failing"},
					Status: corev1.NodeStatus{
						Conditions: []corev1.NodeCondition{
							{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Message: "kubelet stopped"},
							{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
						},
					},
				},
			},
			wantCount:    2,
			wantSeverity: "critical",
			wantProblem:  "NotReady",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := DetectNodeProblems(tt.nodes)
			if len(problems) != tt.wantCount {
				t.Errorf("DetectNodeProblems() returned %d problems, want %d", len(problems), tt.wantCount)
			}
			if tt.wantCount > 0 && len(problems) > 0 {
				if problems[0].Severity != tt.wantSeverity {
					t.Errorf("first problem severity = %q, want %q", problems[0].Severity, tt.wantSeverity)
				}
				if problems[0].Problem != tt.wantProblem {
					t.Errorf("first problem type = %q, want %q", problems[0].Problem, tt.wantProblem)
				}
			}
		})
	}
}

func TestDetectVersionSkew(t *testing.T) {
	tests := []struct {
		name    string
		nodes   []*corev1.Node
		wantNil bool
		wantMin string
		wantMax string
	}{
		{
			name:    "empty nodes",
			nodes:   nil,
			wantNil: true,
		},
		{
			name: "same version",
			nodes: []*corev1.Node{
				{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.28.3"}}},
				{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.28.5"}}},
			},
			wantNil: true, // same minor, different patch
		},
		{
			name: "different minor versions",
			nodes: []*corev1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.27.8"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "node2"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.28.3"}}},
			},
			wantNil: false,
			wantMin: "1.27",
			wantMax: "1.28",
		},
		{
			name: "same minor different patch is nil",
			nodes: []*corev1.Node{
				{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.29.0"}}},
				{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.29.4"}}},
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectVersionSkew(tt.nodes)
			if tt.wantNil {
				if got != nil {
					t.Errorf("DetectVersionSkew() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("DetectVersionSkew() = nil, want non-nil")
			}
			if got.MinVersion != tt.wantMin {
				t.Errorf("MinVersion = %q, want %q", got.MinVersion, tt.wantMin)
			}
			if got.MaxVersion != tt.wantMax {
				t.Errorf("MaxVersion = %q, want %q", got.MaxVersion, tt.wantMax)
			}
		})
	}
}
