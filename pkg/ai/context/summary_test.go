package context

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

// withExtraRunningReadyContainer appends a second container that is running and
// ready — the sidecar half of the "main container finished, sidecar didn't"
// shape. Added to spec as well as status so the READY denominator is right.
func withExtraRunningReadyContainer(name string) podOption {
	return func(p *corev1.Pod) {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: name, Image: "fluent-bit:2"})
		p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{
			Name:  name,
			Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		})
	}
}

func withPodReady(ready bool) podOption {
	return func(p *corev1.Pod) {
		status := corev1.ConditionFalse
		if ready {
			status = corev1.ConditionTrue
		}
		p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{
			Type: corev1.PodReady, Status: status,
		})
	}
}

// withPodReason sets the pod-level reason (Evicted, NodeAffinity, ...), which
// lives on the pod rather than on any container.
func withPodReason(reason string) podOption {
	return func(p *corev1.Pod) { p.Status.Reason = reason }
}

func withSchedulingGate() podOption {
	return func(p *corev1.Pod) {
		p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionFalse,
			Reason: corev1.PodReasonSchedulingGated,
		})
	}
}

// withLastTerminationOOM builds a container that was OOMKilled and came back.
// RestartCount is non-zero because that is the only shape a kubelet produces: a
// container cannot have a LastTerminationState and zero restarts. Termination
// history is reported only when something actually restarted, so a fixture
// without the count would exercise a state that cannot occur.
func withLastTerminationOOM() podOption {
	return func(p *corev1.Pod) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:         "app",
			Ready:        false,
			RestartCount: 2,
			State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
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

// makeDeployment builds a converged Deployment: `total` is the DESIRED count,
// so it is set on spec as well as status. Real Deployments always carry
// spec.replicas; a fixture that set only status let the status-denominated
// READY bug pass unnoticed.
func makeDeployment(name string, ready, total int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &total,
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
		// wantLastTerminated is the termination HISTORY field, asserted apart
		// from Status on purpose: a pod OOMKilled an hour ago that has been
		// Ready since is Running now, and the reason belongs in history rather
		// than masquerading as the current state.
		wantLastTerminated string
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
			// A PAST OOM kill is not the current status. kubectl keeps STATUS
			// on the live state and reports the reason under "Last State";
			// health.Pod agrees (pkg/health/pod_test.go: "recovered
			// LastTerminationState OOMKilled is healthy"). Before this split
			// the row claimed Status "OOMKilled" for a pod that was running
			// fine, contradicting the health verdict on the very same object.
			name:               "recovered OOM reports as running, with history",
			pod:                makePod("oom-last", withLastTerminationOOM()),
			wantStatus:         "Running",
			wantIssue:          "OOMKilled", // getPodIssue is the diagnostic signal; unchanged
			wantReady:          "0/1",
			wantLastTerminated: "OOMKilled",
		},
		{
			name:       "ImagePullBackOff",
			pod:        makePod("imgpull", withPhase(corev1.PodPending), withContainerWaiting("ImagePullBackOff")),
			wantStatus: "ImagePullBackOff",
			wantIssue:  "ImagePullBackOff",
			wantReady:  "0/1",
		},
		{
			// The Init: prefix is load-bearing — it tells a reader the failure
			// is in initialization, not the main workload, which points at a
			// different fix. kubectl has carried it for years.
			name:       "init container failing is attributed to init",
			pod:        makePod("init-fail", withPhase(corev1.PodPending), withInitContainerWaiting("CrashLoopBackOff")),
			wantStatus: "Init:CrashLoopBackOff",
			wantIssue:  "CrashLoopBackOff",
			wantReady:  "0/1", // denominator is spec containers, as kubectl does
		},
		{
			name:       "completed",
			pod:        makePod("done", withPhase(corev1.PodSucceeded)),
			wantStatus: "Succeeded",
			wantIssue:  "",
			wantReady:  "0/1",
		},
		{
			// Nothing else is running, so "Completed" is the whole truth —
			// this is the branch that does NOT become NotReady.
			name:       "terminated Completed with nothing else running",
			pod:        makePod("completed-term", withContainerTerminated("Completed")),
			wantStatus: "Completed",
			wantIssue:  "",
			wantReady:  "0/1",
		},
		{
			// THE case this work exists for. A Job pod whose main container
			// finished while a sidecar keeps running: phase stays Running
			// forever, so the bare phase said "Running" and radar called it
			// healthy. kubectl reports NotReady, which is what let other tools
			// spot a CronJob wedged by a non-exiting sidecar.
			name: "completed main container with a lingering sidecar is NotReady",
			pod: makePod("job-sidecar",
				withContainerTerminated("Completed"),
				withExtraRunningReadyContainer("sidecar"),
				withPodReady(false)),
			wantStatus: "NotReady",
			wantReady:  "1/2",
		},
		{
			// Same shape, but the pod IS Ready — a legitimately healthy pod
			// that happens to have a finished container. Must stay Running or
			// the fix would false-positive on every such pod.
			name: "completed container but pod is Ready stays Running",
			pod: makePod("job-ready",
				withContainerTerminated("Completed"),
				withExtraRunningReadyContainer("sidecar"),
				withPodReady(true)),
			wantStatus: "Running",
			wantReady:  "1/2",
		},
		{
			// Node pressure evicted it. The reason lives on the pod, not on any
			// container, so a container-only walk loses it entirely.
			name:       "evicted pod names the eviction",
			pod:        makePod("eviction", withPhase(corev1.PodFailed), withPodReason("Evicted")),
			wantStatus: "Evicted",
			wantReady:  "0/1",
		},
		{
			// Intentionally parked (quota gate, Kueue). Worth naming so an
			// agent doesn't chase it as slow scheduling.
			name:       "scheduling gated is named, not generic Pending",
			pod:        makePod("gated", withPhase(corev1.PodPending), withSchedulingGate()),
			wantStatus: "SchedulingGated",
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
			if s.LastTerminatedReason != tt.wantLastTerminated {
				t.Errorf("LastTerminatedReason = %q, want %q", s.LastTerminatedReason, tt.wantLastTerminated)
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
			// Scale-up before the controller reacts: spec says 10, status still
			// says 3. Denominating by status reported "3/3 Running" for a
			// workload 7 replicas short of target — worse than kubectl, which
			// denominates by spec and shows 3/10.
			name: "Deployment scaled up before the controller reacts",
			obj: func() *appsv1.Deployment {
				d := makeDeployment("web", 3, 3)
				ten := int32(10)
				d.Spec.Replicas = &ten
				return d
			}(),
			wantStatus: "Progressing",
			wantReady:  "3/10",
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

func TestSummary_HPAIssue(t *testing.T) {
	tests := []struct {
		name      string
		hpa       *autoscalingv2.HorizontalPodAutoscaler
		wantIssue string
	}{
		{
			name: "maxed",
			hpa: &autoscalingv2.HorizontalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
				Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
					MaxReplicas:    10,
					ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"},
				},
				Status: autoscalingv2.HorizontalPodAutoscalerStatus{
					CurrentReplicas: 10,
					DesiredReplicas: 10,
					Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
						{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionTrue, Reason: "TooManyReplicas", Message: "the desired replica count is more than the maximum replica count"},
					},
				},
			},
			wantIssue: "maxed",
		},
		{
			name: "normal",
			hpa: &autoscalingv2.HorizontalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
				Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
					MaxReplicas:    10,
					ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"},
				},
				Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 5, DesiredReplicas: 5},
			},
			wantIssue: "",
		},
		{
			name: "metrics unavailable",
			hpa: &autoscalingv2.HorizontalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
				Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
					MaxReplicas:    10,
					ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"},
				},
				Status: autoscalingv2.HorizontalPodAutoscalerStatus{
					CurrentReplicas: 5,
					DesiredReplicas: 5,
					Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
						{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetResourceMetric"},
					},
				},
			},
			wantIssue: "HPA cannot compute replicas because required metrics are unavailable",
		},
		{
			name: "scaling disabled is not an issue",
			hpa: &autoscalingv2.HorizontalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "paused", Namespace: "default"},
				Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
					MaxReplicas:    10,
					ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "paused"},
				},
				Status: autoscalingv2.HorizontalPodAutoscalerStatus{
					CurrentReplicas: 0,
					DesiredReplicas: 0,
					Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
						{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "ScalingDisabled"},
					},
				},
			},
			wantIssue: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := Minify(tt.hpa, LevelSummary)
			if err != nil {
				t.Fatalf("Minify failed: %v", err)
			}
			s := raw.(*ResourceSummary)
			if s.Issue != tt.wantIssue {
				t.Errorf("Issue = %q, want %q", s.Issue, tt.wantIssue)
			}
		})
	}
}

func TestSummary_CronJobIssue(t *testing.T) {
	now := time.Now()
	suspended := true
	notSuspended := false
	oldTime := metav1.NewTime(now.Add(-48 * time.Hour))
	freshTime := metav1.NewTime(now.Add(-1 * time.Hour))

	tests := []struct {
		name      string
		cj        *batchv1.CronJob
		wantIssue string
	}{
		{
			name: "stale",
			cj: &batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
				Spec:       batchv1.CronJobSpec{Schedule: "0 2 * * *", Suspend: &notSuspended},
				Status:     batchv1.CronJobStatus{LastScheduleTime: &oldTime},
			},
			wantIssue: "no recent runs",
		},
		{
			name: "suspended is ok",
			cj: &batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
				Spec:       batchv1.CronJobSpec{Schedule: "0 2 * * *", Suspend: &suspended},
				Status:     batchv1.CronJobStatus{LastScheduleTime: &oldTime},
			},
			wantIssue: "",
		},
		{
			name: "fresh is ok",
			cj: &batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
				Spec:       batchv1.CronJobSpec{Schedule: "0 2 * * *", Suspend: &notSuspended},
				Status:     batchv1.CronJobStatus{LastScheduleTime: &freshTime},
			},
			wantIssue: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := Minify(tt.cj, LevelSummary)
			if err != nil {
				t.Fatalf("Minify failed: %v", err)
			}
			s := raw.(*ResourceSummary)
			if s.Issue != tt.wantIssue {
				t.Errorf("Issue = %q, want %q", s.Issue, tt.wantIssue)
			}
		})
	}
}

// TestSummary_TerminatingFields pins the lifecycle fields on the AI
// summary output. AI agents (and the MCP list_resources tool)
// rely on these to spot zombie/finalizer-stuck resources at a glance
// — without them, an LLM advising on "why is this not converging"
// has no way to detect a Terminating resource short of fetching the
// Detail-level YAML.
func TestSummary_TerminatingFields(t *testing.T) {
	dt := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	pod := makePod("terminating-pod", withPhase(corev1.PodRunning))
	pod.DeletionTimestamp = &dt
	pod.Finalizers = []string{"example.io/cleanup", "kubernetes"}

	raw, err := Minify(pod, LevelSummary)
	if err != nil {
		t.Fatalf("Minify failed: %v", err)
	}
	s := raw.(*ResourceSummary)
	if !s.Terminating {
		t.Fatal("expected Terminating=true on a pod with deletionTimestamp set")
	}
	if len(s.Finalizers) != 2 || s.Finalizers[0] != "example.io/cleanup" {
		t.Fatalf("Finalizers = %v, want [example.io/cleanup kubernetes]", s.Finalizers)
	}

	// Healthy pod (no deletionTimestamp) must report Terminating=false
	// so prune-on-zero keeps the field out of the AI context entirely.
	healthy := makePod("healthy-pod", withPhase(corev1.PodRunning))
	rawH, _ := Minify(healthy, LevelSummary)
	if rawH.(*ResourceSummary).Terminating {
		t.Fatal("expected Terminating=false on a pod without deletionTimestamp")
	}
}

//go:fix inline
func int32Ptr(i int32) *int32 { return &i }

// TestSummary_GenericFallback covers kinds without an explicit summarizer.
// The load-bearing path is the TypeMeta-empty branch — informer-cached
// objects have TypeMeta stripped, so summarizeGeneric must extract the
// kind name from the Go type via fmt.Sprintf("%T", obj). A future
// refactor that breaks strings.LastIndex(".") would silently degrade MCP
// output for every kind covered by the generic path.
func TestSummary_GenericFallback(t *testing.T) {
	created := metav1.NewTime(time.Now().Add(-3 * time.Hour))

	cases := []struct {
		name       string
		obj        runtime.Object
		wantKind   string
		wantName   string
		wantNs     string // expected namespace, "" for cluster-scoped
		wantAgeSet bool
	}{
		{
			name:       "PersistentVolume (cluster-scoped, no namespace)",
			obj:        &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-1", CreationTimestamp: created}},
			wantKind:   "PersistentVolume",
			wantName:   "pv-1",
			wantNs:     "",
			wantAgeSet: true,
		},
		{
			name:       "StorageClass (cluster-scoped)",
			obj:        &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "standard", CreationTimestamp: created}},
			wantKind:   "StorageClass",
			wantName:   "standard",
			wantNs:     "",
			wantAgeSet: true,
		},
		{
			name:       "NetworkPolicy (namespaced)",
			obj:        &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny-all", Namespace: "prod", CreationTimestamp: created}},
			wantKind:   "NetworkPolicy",
			wantName:   "deny-all",
			wantNs:     "prod",
			wantAgeSet: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := Minify(tc.obj, LevelSummary)
			if err != nil {
				t.Fatalf("Minify failed: %v", err)
			}
			s := raw.(*ResourceSummary)
			if s.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q (TypeMeta-empty path may be broken)", s.Kind, tc.wantKind)
			}
			if s.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", s.Name, tc.wantName)
			}
			if s.Namespace != tc.wantNs {
				t.Errorf("Namespace = %q, want %q", s.Namespace, tc.wantNs)
			}
			if tc.wantAgeSet && s.Age == "" {
				t.Errorf("Age unset on object with CreationTimestamp set")
			}
		})
	}
}

// TestSummary_GenericFallback_TypeMetaPopulated covers the other branch:
// when obj.GetObjectKind() returns a non-empty Kind, that wins over the
// Go-type reflection fallback. Production rarely hits this (informers
// strip TypeMeta) but the branch exists.
func TestSummary_GenericFallback_TypeMetaPopulated(t *testing.T) {
	pv := &corev1.PersistentVolume{
		TypeMeta:   metav1.TypeMeta{Kind: "PersistentVolume", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Name: "pv-with-typemeta"},
	}
	raw, err := Minify(pv, LevelSummary)
	if err != nil {
		t.Fatalf("Minify failed: %v", err)
	}
	if got := raw.(*ResourceSummary).Kind; got != "PersistentVolume" {
		t.Errorf("Kind = %q, want PersistentVolume", got)
	}
}

// TestSummary_GenericFallback_TypedNilSafe guards against Go's typed-nil-
// through-interface trap: var pv *PV = nil; var obj runtime.Object = pv;
// obj != nil but calling methods on it panics. summarizeGeneric must
// detect this via reflection.
func TestSummary_GenericFallback_TypedNilSafe(t *testing.T) {
	var pv *corev1.PersistentVolume // typed nil
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("summarizeGeneric panicked on typed-nil obj: %v", r)
		}
	}()
	raw, err := Minify(pv, LevelSummary)
	if err != nil {
		t.Fatalf("Minify failed: %v", err)
	}
	if got := raw.(*ResourceSummary).Kind; got != "Unknown" {
		t.Errorf("Kind = %q, want Unknown for typed-nil obj", got)
	}
}
