package context

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- Pod helpers ---

type podOption func(*corev1.Pod)

func makePod(name string, opts ...podOption) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	for _, opt := range opts {
		opt(pod)
	}
	return pod
}

func withPhase(phase corev1.PodPhase) podOption {
	return func(p *corev1.Pod) { p.Status.Phase = phase }
}

func withReadyContainers(ready, total int) podOption {
	return func(p *corev1.Pod) {
		p.Status.ContainerStatuses = make([]corev1.ContainerStatus, total)
		for i := range total {
			p.Status.ContainerStatuses[i] = corev1.ContainerStatus{
				Name:  "app",
				Ready: i < ready,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}
		}
	}
}

func withContainerWaiting(reason string) podOption {
	return func(p *corev1.Pod) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  "app",
			Ready: false,
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: reason},
			},
		}}
	}
}

func withContainerTerminated(reason string) podOption {
	return func(p *corev1.Pod) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  "app",
			Ready: false,
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{Reason: reason},
			},
		}}
	}
}

func withLastTerminationOOM() podOption {
	return func(p *corev1.Pod) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  "app",
			Ready: false,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"},
			},
		}}
	}
}

func withInitContainerWaiting(reason string) podOption {
	return func(p *corev1.Pod) {
		p.Status.InitContainerStatuses = []corev1.ContainerStatus{{
			Name:  "init",
			Ready: false,
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: reason},
			},
		}}
	}
}

// --- Workload helpers ---

func makeDeployment(name string, ready, total int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "myapp:v1"}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:      total,
			ReadyReplicas: ready,
		},
	}
}

func makeStatefulSet(name string, ready, total int32) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &total,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "myapp:v1"}},
				},
			},
		},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: ready,
		},
	}
}

func makeDaemonSet(name string, ready, desired int32) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "myapp:v1"}},
				},
			},
		},
		Status: appsv1.DaemonSetStatus{
			NumberReady:            ready,
			DesiredNumberScheduled: desired,
		},
	}
}

// --- Tests ---

func TestSummary_PodStatus(t *testing.T) {
	tests := []struct {
		name       string
		pod        *corev1.Pod
		wantStatus string
		wantIssue  string
		wantReady  string
	}{
		{
			name:       "running healthy",
			pod:        makePod("healthy", withReadyContainers(1, 1)),
			wantStatus: "Running",
			wantIssue:  "",
			wantReady:  "1/1",
		},
		{
			name:       "CrashLoopBackOff",
			pod:        makePod("crash", withContainerWaiting("CrashLoopBackOff")),
			wantStatus: "CrashLoopBackOff",
			wantIssue:  "CrashLoopBackOff",
			wantReady:  "0/1",
		},
		{
			name:       "OOMKilled via terminated state",
			pod:        makePod("oom", withContainerTerminated("OOMKilled")),
			wantStatus: "OOMKilled",
			wantIssue:  "OOMKilled",
			wantReady:  "0/1",
		},
		{
			name:       "OOMKilled via lastTerminationState",
			pod:        makePod("oom-last", withLastTerminationOOM()),
			wantStatus: "OOMKilled",
			wantIssue:  "OOMKilled",
			wantReady:  "0/1",
		},
		{
			name:       "ImagePullBackOff",
			pod:        makePod("imgpull", withPhase(corev1.PodPending), withContainerWaiting("ImagePullBackOff")),
			wantStatus: "ImagePullBackOff",
			wantIssue:  "ImagePullBackOff",
			wantReady:  "0/1",
		},
		{
			name:       "init container failing",
			pod:        makePod("init-fail", withPhase(corev1.PodPending), withInitContainerWaiting("CrashLoopBackOff")),
			wantStatus: "CrashLoopBackOff",
			wantIssue:  "CrashLoopBackOff",
			wantReady:  "",
		},
		{
			name:       "completed",
			pod:        makePod("done", withPhase(corev1.PodSucceeded)),
			wantStatus: "Succeeded",
			wantIssue:  "",
			wantReady:  "",
		},
		{
			name:       "terminated Completed is not an issue",
			pod:        makePod("completed-term", withContainerTerminated("Completed")),
			wantStatus: "Running",
			wantIssue:  "",
			wantReady:  "0/1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := Minify(tt.pod, LevelSummary)
			if err != nil {
				t.Fatalf("Minify failed: %v", err)
			}
			s := raw.(*ResourceSummary)

			if s.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", s.Status, tt.wantStatus)
			}
			if s.Issue != tt.wantIssue {
				t.Errorf("Issue = %q, want %q", s.Issue, tt.wantIssue)
			}
			if s.Ready != tt.wantReady {
				t.Errorf("Ready = %q, want %q", s.Ready, tt.wantReady)
			}
		})
	}
}

func TestSummary_WorkloadStatus(t *testing.T) {
	tests := []struct {
		name       string
		obj        interface{ GetName() string }
		wantStatus string
		wantReady  string
		wantImage  string
	}{
		{
			name:       "Deployment healthy",
			obj:        makeDeployment("web", 3, 3),
			wantStatus: "Running",
			wantReady:  "3/3",
			wantImage:  "myapp:v1",
		},
		{
			name:       "Deployment progressing",
			obj:        makeDeployment("web", 1, 3),
			wantStatus: "Progressing",
			wantReady:  "1/3",
			wantImage:  "myapp:v1",
		},
		{
			name:       "Deployment scaled to 0",
			obj:        makeDeployment("web", 0, 0),
			wantStatus: "Scaled to 0",
			wantReady:  "0/0",
			wantImage:  "myapp:v1",
		},
		{
			name:       "StatefulSet healthy",
			obj:        makeStatefulSet("db", 3, 3),
			wantStatus: "Running",
			wantReady:  "3/3",
			wantImage:  "myapp:v1",
		},
		{
			name:       "StatefulSet progressing",
			obj:        makeStatefulSet("db", 1, 3),
			wantStatus: "Progressing",
			wantReady:  "1/3",
			wantImage:  "myapp:v1",
		},
		{
			name:       "StatefulSet scaled to 0",
			obj:        makeStatefulSet("db", 0, 0),
			wantStatus: "Scaled to 0",
			wantReady:  "0/0",
			wantImage:  "myapp:v1",
		},
		{
			name:       "DaemonSet healthy",
			obj:        makeDaemonSet("agent", 5, 5),
			wantStatus: "Running",
			wantReady:  "5/5",
			wantImage:  "myapp:v1",
		},
		{
			name:       "DaemonSet progressing",
			obj:        makeDaemonSet("agent", 3, 5),
			wantStatus: "Progressing",
			wantReady:  "3/5",
			wantImage:  "myapp:v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw any
			var err error
			switch o := tt.obj.(type) {
			case *appsv1.Deployment:
				raw, err = Minify(o, LevelSummary)
			case *appsv1.StatefulSet:
				raw, err = Minify(o, LevelSummary)
			case *appsv1.DaemonSet:
				raw, err = Minify(o, LevelSummary)
			}
			if err != nil {
				t.Fatalf("Minify failed: %v", err)
			}
			s := raw.(*ResourceSummary)

			if s.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", s.Status, tt.wantStatus)
			}
			if s.Ready != tt.wantReady {
				t.Errorf("Ready = %q, want %q", s.Ready, tt.wantReady)
			}
			if s.Image != tt.wantImage {
				t.Errorf("Image = %q, want %q", s.Image, tt.wantImage)
			}
		})
	}
}

func TestSummary_Service(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web-svc", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: "10.96.0.1",
			Ports: []corev1.ServicePort{
				{Port: 80, Protocol: corev1.ProtocolTCP},
				{Port: 443, Protocol: corev1.ProtocolTCP},
			},
			Selector: map[string]string{"app": "web"},
		},
	}

	raw, err := Minify(svc, LevelSummary)
	if err != nil {
		t.Fatalf("Minify failed: %v", err)
	}
	s := raw.(*ResourceSummary)

	if s.Type != "ClusterIP" {
		t.Errorf("Type = %q, want ClusterIP", s.Type)
	}
	if s.Ports != "80/TCP, 443/TCP" {
		t.Errorf("Ports = %q, want %q", s.Ports, "80/TCP, 443/TCP")
	}
	if s.Selector != "app=web" {
		t.Errorf("Selector = %q, want %q", s.Selector, "app=web")
	}
	if s.ClusterIP != "10.96.0.1" {
		t.Errorf("ClusterIP = %q, want 10.96.0.1", s.ClusterIP)
	}
}

func TestSummary_Job(t *testing.T) {
	now := metav1.Now()
	later := metav1.NewTime(now.Add(30e9)) // 30 seconds later

	tests := []struct {
		name            string
		job             *batchv1.Job
		wantStatus      string
		wantCompletions string
	}{
		{
			name: "complete",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "default"},
				Spec:       batchv1.JobSpec{Completions: int32Ptr(1)},
				Status: batchv1.JobStatus{
					Succeeded: 1,
					Conditions: []batchv1.JobCondition{
						{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
					},
					StartTime:      &now,
					CompletionTime: &later,
				},
			},
			wantStatus:      "Complete",
			wantCompletions: "1/1",
		},
		{
			name: "failed",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "default"},
				Spec:       batchv1.JobSpec{Completions: int32Ptr(1)},
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{
						{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
					},
				},
			},
			wantStatus:      "Failed",
			wantCompletions: "0/1",
		},
		{
			name: "running",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "default"},
				Spec:       batchv1.JobSpec{Completions: int32Ptr(3)},
				Status: batchv1.JobStatus{
					Succeeded: 1,
				},
			},
			wantStatus:      "Running",
			wantCompletions: "1/3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := Minify(tt.job, LevelSummary)
			if err != nil {
				t.Fatalf("Minify failed: %v", err)
			}
			s := raw.(*ResourceSummary)

			if s.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", s.Status, tt.wantStatus)
			}
			if s.Completions != tt.wantCompletions {
				t.Errorf("Completions = %q, want %q", s.Completions, tt.wantCompletions)
			}
		})
	}

	// Verify Duration is populated for complete jobs
	raw, _ := Minify(tests[0].job, LevelSummary)
	s := raw.(*ResourceSummary)
	if s.Duration == "" {
		t.Error("Duration should be populated for complete jobs")
	}
}

func TestSummary_Node(t *testing.T) {
	tests := []struct {
		name           string
		node           *corev1.Node
		wantStatus     string
		wantPressures  []string
		wantVersion    string
		wantRolesCount int
	}{
		{
			name: "ready",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "node-1",
					Labels: map[string]string{"node-role.kubernetes.io/worker": ""},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
					NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.28.0"},
				},
			},
			wantStatus:     "Ready",
			wantPressures:  nil,
			wantVersion:    "v1.28.0",
			wantRolesCount: 1,
		},
		{
			name: "memory pressure",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-2"},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
						{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
					},
					NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.28.0"},
				},
			},
			wantStatus:    "Ready",
			wantPressures: []string{"MemoryPressure"},
			wantVersion:   "v1.28.0",
		},
		{
			name: "not ready",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-3"},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady"},
					},
					NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.28.0"},
				},
			},
			wantStatus:  "NotReady",
			wantVersion: "v1.28.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := Minify(tt.node, LevelSummary)
			if err != nil {
				t.Fatalf("Minify failed: %v", err)
			}
			s := raw.(*ResourceSummary)

			if s.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", s.Status, tt.wantStatus)
			}
			if s.Version != tt.wantVersion {
				t.Errorf("Version = %q, want %q", s.Version, tt.wantVersion)
			}
			if len(s.Pressures) != len(tt.wantPressures) {
				t.Errorf("Pressures = %v, want %v", s.Pressures, tt.wantPressures)
			}
			if tt.wantRolesCount > 0 && len(s.Roles) != tt.wantRolesCount {
				t.Errorf("Roles count = %d, want %d", len(s.Roles), tt.wantRolesCount)
			}
		})
	}
}

func TestSummary_CronJob(t *testing.T) {
	suspended := true
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly-backup", Namespace: "default"},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 2 * * *",
			Suspend:  &suspended,
		},
		Status: batchv1.CronJobStatus{
			Active: []corev1.ObjectReference{{Name: "job-1"}, {Name: "job-2"}},
		},
	}

	raw, err := Minify(cj, LevelSummary)
	if err != nil {
		t.Fatalf("Minify failed: %v", err)
	}
	s := raw.(*ResourceSummary)

	if s.Schedule != "0 2 * * *" {
		t.Errorf("Schedule = %q, want %q", s.Schedule, "0 2 * * *")
	}
	if s.Suspended == nil || !*s.Suspended {
		t.Error("Suspended should be true")
	}
	if s.Active != 2 {
		t.Errorf("Active = %d, want 2", s.Active)
	}
}

//go:fix inline
func int32Ptr(i int32) *int32 { return new(i) }
