package k8s

import (
	"testing"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClassifyPodHealth(t *testing.T) {
	now := time.Now()
	oldTime := metav1.NewTime(now.Add(-10 * time.Minute))
	recentTime := metav1.NewTime(now.Add(-1 * time.Minute))

	tests := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{
			name: "healthy running pod",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Ready: true, RestartCount: 0},
					},
				},
			},
			want: "healthy",
		},
		{
			name: "succeeded pod",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
			},
			want: "healthy",
		},
		{
			name: "failed pod",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodFailed},
			},
			want: "error",
		},
		{
			name: "CrashLoopBackOff",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
					},
				},
			},
			want: "error",
		},
		{
			name: "OOMKilled",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}}},
					},
				},
			},
			want: "error",
		},
		{
			name: "LastTerminationState OOMKilled",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Ready:                true,
							LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
						},
					},
				},
			},
			want: "error",
		},
		{
			name: "init container error",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					InitContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
					},
				},
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: oldTime},
			},
			want: "error",
		},
		{
			name: "pending over 5 minutes",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: oldTime},
				Status:     corev1.PodStatus{Phase: corev1.PodPending},
			},
			want: "warning",
		},
		{
			name: "recently pending is healthy",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: recentTime},
				Status:     corev1.PodStatus{Phase: corev1.PodPending},
			},
			want: "healthy",
		},
		{
			name: "high restart count",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Ready: true, RestartCount: 10},
					},
				},
			},
			want: "warning",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyPodHealth(tt.pod, now)
			if got != tt.want {
				t.Errorf("ClassifyPodHealth() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyNodeHealth(t *testing.T) {
	tests := []struct {
		name            string
		node            *corev1.Node
		wantReady       bool
		wantUnschedulable bool
		wantPressures   int
	}{
		{
			name: "ready node",
			node: &corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
					NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.28.3"},
				},
			},
			wantReady:       true,
			wantUnschedulable: false,
			wantPressures:   0,
		},
		{
			name: "not ready node",
			node: &corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Message: "kubelet stopped"},
					},
				},
			},
			wantReady:       false,
			wantUnschedulable: false,
			wantPressures:   0,
		},
		{
			name: "cordoned and ready",
			node: &corev1.Node{
				Spec: corev1.NodeSpec{Unschedulable: true},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
			},
			wantReady:       true,
			wantUnschedulable: true,
			wantPressures:   0,
		},
		{
			name: "cordoned and not ready",
			node: &corev1.Node{
				Spec: corev1.NodeSpec{Unschedulable: true},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
					},
				},
			},
			wantReady:       false,
			wantUnschedulable: true,
			wantPressures:   0,
		},
		{
			name: "memory pressure",
			node: &corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
						{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
					},
				},
			},
			wantReady:       true,
			wantUnschedulable: false,
			wantPressures:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyNodeHealth(tt.node)
			if got.Ready != tt.wantReady {
				t.Errorf("Ready = %v, want %v", got.Ready, tt.wantReady)
			}
			if got.Unschedulable != tt.wantUnschedulable {
				t.Errorf("Unschedulable = %v, want %v", got.Unschedulable, tt.wantUnschedulable)
			}
			if len(got.Pressures) != tt.wantPressures {
				t.Errorf("Pressures = %v, want %d pressures", got.Pressures, tt.wantPressures)
			}
		})
	}
}

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
			wantSeverity: "error",
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
			wantSeverity: "warning",
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
			wantSeverity: "warning",
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
			wantSeverity: "error",
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
		name     string
		nodes    []*corev1.Node
		wantNil  bool
		wantMin  string
		wantMax  string
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

func TestFormatAge(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{48 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		got := FormatAge(tt.d)
		if got != tt.want {
			t.Errorf("FormatAge(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		s      string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"this is a longer string", 10, "this is..."},
		{"  trimmed  ", 20, "trimmed"},
	}
	for _, tt := range tests {
		got := Truncate(tt.s, tt.maxLen)
		if got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
		}
	}
}

func TestDetectHPAProblems(t *testing.T) {
	tests := []struct {
		name      string
		hpas      []*autoscalingv2.HorizontalPodAutoscaler
		wantCount int
		wantProblem string
	}{
		{
			name: "maxed HPA",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 10, DesiredReplicas: 10},
				},
			},
			wantCount:   1,
			wantProblem: "maxed",
		},
		{
			name: "not maxed",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 5, DesiredReplicas: 5},
				},
			},
			wantCount: 0,
		},
		{
			name: "zero replicas",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "idle", Namespace: "default"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 0, DesiredReplicas: 0},
				},
			},
			wantCount: 0,
		},
		{
			name: "maxReplicas zero is not a problem",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "broken", Namespace: "default"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 0},
					Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 0, DesiredReplicas: 0},
				},
			},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := DetectHPAProblems(tt.hpas)
			if len(problems) != tt.wantCount {
				t.Errorf("DetectHPAProblems() returned %d problems, want %d", len(problems), tt.wantCount)
			}
			if tt.wantCount > 0 && len(problems) > 0 {
				if problems[0].Problem != tt.wantProblem {
					t.Errorf("problem = %q, want %q", problems[0].Problem, tt.wantProblem)
				}
			}
		})
	}
}

func TestDetectCronJobProblems(t *testing.T) {
	now := time.Now()
	suspended := true
	notSuspended := false
	oldTime := metav1.NewTime(now.Add(-48 * time.Hour))
	freshTime := metav1.NewTime(now.Add(-1 * time.Hour))

	tests := []struct {
		name        string
		cronjobs    []*batchv1.CronJob
		wantCount   int
		wantProblem string
	}{
		{
			name: "stale cronjob",
			cronjobs: []*batchv1.CronJob{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
					Spec:       batchv1.CronJobSpec{Schedule: "0 2 * * *", Suspend: &notSuspended},
					Status:     batchv1.CronJobStatus{LastScheduleTime: &oldTime},
				},
			},
			wantCount:   1,
			wantProblem: "stale",
		},
		{
			name: "suspended old cronjob is ok",
			cronjobs: []*batchv1.CronJob{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
					Spec:       batchv1.CronJobSpec{Schedule: "0 2 * * *", Suspend: &suspended},
					Status:     batchv1.CronJobStatus{LastScheduleTime: &oldTime},
				},
			},
			wantCount: 0,
		},
		{
			name: "fresh cronjob is ok",
			cronjobs: []*batchv1.CronJob{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
					Spec:       batchv1.CronJobSpec{Schedule: "0 2 * * *", Suspend: &notSuspended},
					Status:     batchv1.CronJobStatus{LastScheduleTime: &freshTime},
				},
			},
			wantCount: 0,
		},
		{
			name: "never-scheduled cronjob",
			cronjobs: []*batchv1.CronJob{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "new-cron", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-48 * time.Hour))},
					Spec:       batchv1.CronJobSpec{Schedule: "0 * * * *", Suspend: &notSuspended},
					Status:     batchv1.CronJobStatus{},
				},
			},
			wantCount:   1,
			wantProblem: "never-scheduled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := DetectCronJobProblems(tt.cronjobs)
			if len(problems) != tt.wantCount {
				t.Errorf("DetectCronJobProblems() returned %d problems, want %d", len(problems), tt.wantCount)
			}
			if tt.wantCount > 0 && len(problems) > 0 {
				if problems[0].Problem != tt.wantProblem {
					t.Errorf("problem = %q, want %q", problems[0].Problem, tt.wantProblem)
				}
			}
		})
	}
}

func TestParseCPUToMillis(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"", 0},
		{"0", 0},
		{"1", 1000},
		{"2", 2000},
		{"250m", 250},
		{"1000m", 1000},
		{"500000000n", 500},
		{"100000000n", 100},
	}
	for _, tt := range tests {
		got := ParseCPUToMillis(tt.input)
		if got != tt.want {
			t.Errorf("ParseCPUToMillis(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseMemoryToBytes(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"", 0},
		{"0", 0},
		{"1024", 1024},
		{"1024Ki", 1024 * 1024},
		{"256Mi", 256 * 1024 * 1024},
		{"1Gi", 1024 * 1024 * 1024},
		{"2Gi", 2 * 1024 * 1024 * 1024},
	}
	for _, tt := range tests {
		got := ParseMemoryToBytes(tt.input)
		if got != tt.want {
			t.Errorf("ParseMemoryToBytes(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestPodProblemReason(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{
			name: "waiting reason",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
					},
				},
			},
			want: "CrashLoopBackOff",
		},
		{
			name: "terminated reason",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}}},
					},
				},
			},
			want: "OOMKilled",
		},
		{
			name: "falls back to phase",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
			want: "Pending",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PodProblemReason(tt.pod)
			if got != tt.want {
				t.Errorf("PodProblemReason() = %q, want %q", got, tt.want)
			}
		})
	}
}
