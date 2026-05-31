package k8s

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

// TestDetectProblems_PopulatesGroup pins that every built-in Problem
// emitted by DetectProblems carries the correct canonical API group.
//
// The summary_context issue index keys per-resource counts as
// "group|kind|ns|name" — a Problem with an empty Group collides with
// no real bucket, silently zeroing issueCount for that workload row.
// Pre-fix, all the built-in append-Problem sites omitted the field, so
// every broken Deployment/StatefulSet/DaemonSet/HPA/CronJob/Job
// reported issueCount: 0 in the AI list envelope — a regression
// against the pre-group-aware behavior.
//
// Construct one broken object per built-in kind, drive DetectProblems
// against a fake client, and assert each emitted Problem's Group
// matches the canonical group for its kind.
func TestDetectProblems_PopulatesGroup(t *testing.T) {
	defer ResetTestState()

	oneReplica := int32(1)
	minReplicas := int32(1)
	now := time.Now()
	// Job needs to be older than 1h to surface a "stuck" problem.
	jobStart := metav1.NewTime(now.Add(-2 * time.Hour))

	client := fake.NewClientset(
		// Deployment with unavailable replicas — triggers the
		// "X/Y available" Problem branch.
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
			Spec:       appsv1.DeploymentSpec{Replicas: &oneReplica},
			Status: appsv1.DeploymentStatus{
				Replicas:            1,
				UnavailableReplicas: 1,
			},
		},
		// StatefulSet with readyReplicas < replicas.
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"},
			Spec:       appsv1.StatefulSetSpec{Replicas: &oneReplica},
			Status: appsv1.StatefulSetStatus{
				Replicas:      1,
				ReadyReplicas: 0,
			},
		},
		// DaemonSet with numberUnavailable > 0.
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "logger", Namespace: "prod"},
			Status: appsv1.DaemonSetStatus{
				NumberUnavailable: 2,
			},
		},
		// HPA at its replica ceiling — DetectHPAProblems flags
		// "maxed" when current and desired both hit MaxReplicas.
		// The wrapper sets Group="autoscaling".
		&autoscalingv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				MinReplicas: &minReplicas,
				MaxReplicas: 10,
			},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{
				CurrentReplicas: 10,
				DesiredReplicas: 10,
			},
		},
		// Job stuck Active>0 for >1h with no completions.
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "prod", CreationTimestamp: jobStart},
			Status: batchv1.JobStatus{
				Active:    1,
				Succeeded: 0,
				Failed:    0,
			},
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	// Allow informers a brief moment to populate. The fake clientset
	// pre-seeds the store, but the lister types reconstruct via
	// informer events on a separate goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hasAllProblemTypes(DetectProblems(cache, "prod")) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	problems := DetectProblems(cache, "prod")

	wantGroup := map[string]string{
		"Deployment":              "apps",
		"StatefulSet":             "apps",
		"DaemonSet":               "apps",
		"HorizontalPodAutoscaler": "autoscaling",
		"Job":                     "batch",
	}

	got := make(map[string]string, len(problems))
	for _, p := range problems {
		// One Problem per kind is enough for the Group assertion;
		// duplicates (e.g. Deployment Available + ProgressDeadline)
		// must agree on Group so the last-write-wins shape is fine.
		got[p.Kind] = p.Group
	}

	for kind, want := range wantGroup {
		gotGroup, ok := got[kind]
		if !ok {
			t.Errorf("no Problem emitted for %s — fixture wiring broken; got %d problems: %+v", kind, len(problems), problems)
			continue
		}
		if gotGroup != want {
			t.Errorf("%s.Group = %q, want %q (summary_context index keys by group — empty Group zeros issueCount)", kind, gotGroup, want)
		}
	}
}

func hasAllProblemTypes(problems []Detection) bool {
	seen := map[string]bool{}
	for _, p := range problems {
		seen[p.Kind] = true
	}
	return seen["Deployment"] && seen["StatefulSet"] && seen["DaemonSet"] && seen["HorizontalPodAutoscaler"] && seen["Job"]
}

func TestDetectProblems_OperationalSignals(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	old := metav1.NewTime(now.Add(-10 * time.Minute))
	jobFailedAt := metav1.NewTime(now.Add(-2 * time.Minute))

	client := fake.NewClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "crashy", Namespace: "prod", CreationTimestamp: old},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "app",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "not-ready", Namespace: "prod", Labels: map[string]string{"app": "not-ready"}, CreationTimestamp: old},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{{
					Type:   corev1.PodReady,
					Status: corev1.ConditionFalse,
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", Labels: map[string]string{"app": "api"}, CreationTimestamp: old},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:  "app",
				Ports: []corev1.ContainerPort{{Name: "admin", ContainerPort: 9090}},
			}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				}},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "missing"},
				Ports:    []corev1.ServicePort{{Port: 80}},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "not-ready", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "not-ready"},
				Ports:    []corev1.ServicePort{{Port: 80}},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "api"},
				Ports: []corev1.ServicePort{{
					Port:       80,
					TargetPort: intstr.FromString("http"),
				}},
			},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "prod", CreationTimestamp: old},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimLost},
		},
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "prod", CreationTimestamp: old},
			Status: batchv1.JobStatus{
				Conditions: []batchv1.JobCondition{{
					Type:               batchv1.JobFailed,
					Status:             corev1.ConditionTrue,
					Reason:             "BackoffLimitExceeded",
					Message:            "Job has reached the specified backoff limit",
					LastTransitionTime: jobFailedAt,
				}},
			},
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "Pod", "crashy", "CrashLoopBackOff") &&
			hasProblem(problems, "Service", "empty", "Selector matches no pods") &&
			hasProblem(problems, "Service", "not-ready", "0/1 selected pods ready") &&
			hasProblem(problems, "Service", "api", "Unresolved named targetPort: http") &&
			hasProblem(problems, "PersistentVolumeClaim", "data", "Lost") &&
			hasProblem(problems, "Job", "migrate", "BackoffLimitExceeded") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "Pod", "crashy", "CrashLoopBackOff", "critical")
	// "Selector matches no pods" is warning, not critical — could be a
	// deliberately scaled-to-zero workload. The "0/N selected pods ready"
	// case below stays critical (workload exists, routing is actually
	// broken).
	assertProblem(t, problems, "Service", "empty", "Selector matches no pods", "warning")
	assertProblem(t, problems, "Service", "not-ready", "0/1 selected pods ready", "critical")
	assertProblem(t, problems, "Service", "api", "Unresolved named targetPort: http", "high")
	assertProblem(t, problems, "PersistentVolumeClaim", "data", "Lost", "critical")
	assertProblem(t, problems, "Job", "migrate", "BackoffLimitExceeded", "critical")
}

func TestDetectProblems_ProbeFailures(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	old := metav1.NewTime(now.Add(-10 * time.Minute))
	recent := metav1.NewTime(now.Add(-1 * time.Minute))
	timeless := metav1.Time{}

	client := fake.NewClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "readiness", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:           "app",
				ReadinessProbe: &corev1.Probe{},
			}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{{
					Type:               corev1.PodReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: old,
				}},
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "app",
					Ready: false,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: old}},
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "liveness", Namespace: "prod", CreationTimestamp: old},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "app",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "thrash", Namespace: "prod", CreationTimestamp: old},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:         "app",
					Ready:        false,
					RestartCount: highRestartThreshold + 1,
					State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: old}},
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", FinishedAt: recent},
					},
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "stale-probe", Namespace: "prod", CreationTimestamp: old},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "app",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				}},
			},
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "liveness.1", Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "prod", Name: "liveness"},
			Type:           corev1.EventTypeWarning,
			Reason:         "Unhealthy",
			Message:        "Liveness probe failed: HTTP probe failed with statuscode: 500",
			LastTimestamp:  recent,
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "thrash.1", Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "prod", Name: "thrash"},
			Type:           corev1.EventTypeWarning,
			Reason:         "Unhealthy",
			Message:        "Liveness probe failed: HTTP probe failed with statuscode: 500",
			LastTimestamp:  recent,
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "stale-probe.1", Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "prod", Name: "stale-probe"},
			Type:           corev1.EventTypeWarning,
			Reason:         "Unhealthy",
			Message:        "Liveness probe failed: HTTP probe failed with statuscode: 500",
			LastTimestamp:  timeless,
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "Pod", "readiness", "ReadinessProbeFailed") &&
			hasProblem(problems, "Pod", "liveness", "LivenessProbeFailed") &&
			hasProblem(problems, "Pod", "thrash", "HighRestartCount") &&
			hasProblem(problems, "Pod", "stale-probe", "CrashLoopBackOff") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "Pod", "readiness", "ReadinessProbeFailed", "high")
	assertProblem(t, problems, "Pod", "liveness", "LivenessProbeFailed", "critical")
	assertProblem(t, problems, "Pod", "thrash", "HighRestartCount", "high")
	assertProblem(t, problems, "Pod", "stale-probe", "CrashLoopBackOff", "critical")
	if hasProblem(problems, "Pod", "thrash", "LivenessProbeFailed") {
		t.Fatalf("liveness event should not mask high restart thrash: %+v", problems)
	}
	if hasProblem(problems, "Pod", "stale-probe", "LivenessProbeFailed") {
		t.Fatalf("timeless probe event should not override the current pod reason: %+v", problems)
	}
	if got, ok := lookupProblem(problems, "Pod", "liveness", "LivenessProbeFailed"); !ok || !strings.Contains(got.Message, "HTTP probe failed") {
		t.Fatalf("liveness probe problem = %+v, want event message detail", got)
	}
}

func TestDetectProblems_InvalidProbeTargetAndStalledInit(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	old := metav1.NewTime(now.Add(-10 * time.Minute))
	recent := metav1.NewTime(now.Add(-1 * time.Minute))

	client := fake.NewClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-readiness", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:           "app",
				Ports:          []corev1.ContainerPort{{Name: "admin", ContainerPort: 9090}},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Port: intstr.FromString("http")}}},
			}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{{
					Type:               corev1.PodReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: recent,
				}},
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "app",
					Ready: false,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: old}},
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-liveness", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:          "app",
				Ports:         []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
				LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromString("admin")}}},
			}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "app",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "stuck-init", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.PodSpec{InitContainers: []corev1.Container{{
				Name:    "wait",
				Image:   "busybox",
				Command: []string{"sh", "-c", "while true; do sleep 5; done"},
			}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				InitContainerStatuses: []corev1.ContainerStatus{{
					Name:  "wait",
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: old}},
				}},
			},
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "Pod", "bad-readiness", "ReadinessProbeInvalid") &&
			hasProblem(problems, "Pod", "bad-liveness", "LivenessProbeInvalid") &&
			hasProblem(problems, "Pod", "stuck-init", "InitContainerStalled") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "Pod", "bad-readiness", "ReadinessProbeInvalid", "high")
	assertProblem(t, problems, "Pod", "bad-liveness", "LivenessProbeInvalid", "critical")
	assertProblem(t, problems, "Pod", "stuck-init", "InitContainerStalled", "high")
	if got, ok := lookupProblem(problems, "Pod", "bad-readiness", "ReadinessProbeInvalid"); !ok || !strings.Contains(got.Message, "named port \"http\"") {
		t.Fatalf("readiness invalid problem = %+v, want named port detail", got)
	}
	if got, ok := lookupProblem(problems, "Pod", "stuck-init", "InitContainerStalled"); !ok || !strings.Contains(got.Message, "init container \"wait\"") {
		t.Fatalf("stalled init problem = %+v, want init container detail", got)
	}
}

func TestDetectProblems_DaemonSetSchedulingStatus(t *testing.T) {
	defer ResetTestState()

	old := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	client := fake.NewClientset(
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 4,
				CurrentNumberScheduled: 2,
				NumberUnavailable:      2,
			},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "wrong-node", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 4,
				CurrentNumberScheduled: 4,
				NumberMisscheduled:     1,
				NumberUnavailable:      1,
			},
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "DaemonSet", "missing", "2 not scheduled") &&
			hasProblem(problems, "DaemonSet", "wrong-node", "1 misscheduled") &&
			hasProblem(problems, "DaemonSet", "wrong-node", "1 unavailable") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "DaemonSet", "missing", "2 not scheduled", "critical")
	assertProblem(t, problems, "DaemonSet", "wrong-node", "1 misscheduled", "high")
	assertProblem(t, problems, "DaemonSet", "wrong-node", "1 unavailable", "critical")
}

func TestDetectProblems_DeploymentReplicaFailure(t *testing.T) {
	defer ResetTestState()

	old := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	client := fake.NewClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DeploymentStatus{
				Conditions: []appsv1.DeploymentCondition{{
					Type:               appsv1.DeploymentReplicaFailure,
					Status:             corev1.ConditionTrue,
					Reason:             "FailedCreate",
					Message:            "pods is forbidden: exceeded quota",
					LastTransitionTime: old,
				}, {
					Type:               appsv1.DeploymentProgressing,
					Status:             corev1.ConditionFalse,
					Reason:             "ProgressDeadlineExceeded",
					Message:            "ReplicaSet has timed out progressing",
					LastTransitionTime: old,
				}},
			},
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "Deployment", "api", "ReplicaFailure") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "Deployment", "api", "ReplicaFailure", "critical")
	if hasProblem(problems, "Deployment", "api", "Rollout stuck") {
		t.Fatalf("ReplicaFailure should suppress duplicate rollout-stuck row for the same Deployment: %+v", problems)
	}
	if p, ok := lookupProblem(problems, "Deployment", "api", "ReplicaFailure"); !ok || !strings.Contains(p.Message, "exceeded quota") {
		t.Fatalf("replica failure problem = %+v, want controller message", p)
	}
}

func TestDetectProblems_NetworkAndStorageState(t *testing.T) {
	defer ResetTestState()

	old := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	client := fake.NewClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "edge", Namespace: "prod", CreationTimestamp: old},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "assigned", Namespace: "prod", CreationTimestamp: old},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
			Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{
				IP: "203.0.113.10",
			}}}},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "grow", Namespace: "prod", CreationTimestamp: old},
			Status: corev1.PersistentVolumeClaimStatus{
				Phase:    corev1.ClaimBound,
				Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				Conditions: []corev1.PersistentVolumeClaimCondition{{
					Type:               corev1.PersistentVolumeClaimConditionType("ControllerResizeError"),
					Status:             corev1.ConditionTrue,
					Message:            "resize rejected by storage backend",
					LastTransitionTime: old,
				}},
			},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "fs-pending", Namespace: "prod", CreationTimestamp: old},
			Status: corev1.PersistentVolumeClaimStatus{
				Phase: corev1.ClaimBound,
				Conditions: []corev1.PersistentVolumeClaimCondition{{
					Type:               corev1.PersistentVolumeClaimFileSystemResizePending,
					Status:             corev1.ConditionTrue,
					Message:            "waiting for filesystem expansion on node",
					LastTransitionTime: old,
				}},
			},
		},
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-bad", CreationTimestamp: old},
			Status: corev1.PersistentVolumeStatus{
				Phase:   corev1.VolumeFailed,
				Message: "volume is gone",
			},
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "")
		if hasProblem(problems, "Service", "edge", "LoadBalancer pending") &&
			hasProblem(problems, "PersistentVolumeClaim", "grow", "ControllerResizeError") &&
			hasProblem(problems, "PersistentVolume", "pv-bad", "Failed") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "Service", "edge", "LoadBalancer pending", "high")
	if hasProblem(problems, "Service", "assigned", "LoadBalancer pending") {
		t.Fatalf("assigned LoadBalancer Service should not be flagged: %+v", problems)
	}
	assertProblem(t, problems, "PersistentVolumeClaim", "grow", "ControllerResizeError", "critical")
	if hasProblem(problems, "PersistentVolumeClaim", "fs-pending", string(corev1.PersistentVolumeClaimFileSystemResizePending)) {
		t.Fatalf("FileSystemResizePending is in-progress, not a resize failure: %+v", problems)
	}
	assertProblem(t, problems, "PersistentVolume", "pv-bad", "Failed", "critical")
}

func TestDetectProblems_TerminatingResources(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	oldCreated := metav1.NewTime(now.Add(-2 * time.Hour))
	oldDelete := metav1.NewTime(now.Add(-35 * time.Minute))
	recentDelete := metav1.NewTime(now.Add(-2 * time.Minute))

	client := fake.NewClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "pod-stuck",
				Namespace:         "prod",
				CreationTimestamp: oldCreated,
				DeletionTimestamp: &oldDelete,
				Finalizers:        []string{"example.com/finalizer"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "svc-recent",
				Namespace:         "prod",
				CreationTimestamp: oldCreated,
				DeletionTimestamp: &recentDelete,
			},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "deploy-stuck",
				Namespace:         "prod",
				CreationTimestamp: oldCreated,
				DeletionTimestamp: &oldDelete,
			},
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "Pod", "pod-stuck", "Terminating stuck") &&
			hasProblem(problems, "Deployment", "deploy-stuck", "Terminating stuck") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "Pod", "pod-stuck", "Terminating stuck", "critical")
	if p, ok := lookupProblem(problems, "Pod", "pod-stuck", "Terminating stuck"); !ok || !strings.Contains(p.Message, "example.com/finalizer") {
		t.Fatalf("terminating pod problem = %+v, want finalizer context", p)
	}
	assertProblem(t, problems, "Deployment", "deploy-stuck", "Terminating stuck", "critical")
	if hasProblem(problems, "Service", "svc-recent", "Terminating stuck") {
		t.Fatalf("recently deleting Service should not be flagged: %+v", problems)
	}
}

func hasProblem(problems []Detection, kind, name, reason string) bool {
	for _, p := range problems {
		if p.Kind == kind && p.Name == name && p.Reason == reason {
			return true
		}
	}
	return false
}

func assertProblem(t *testing.T, problems []Detection, kind, name, reason, severity string) {
	t.Helper()
	for _, p := range problems {
		if p.Kind != kind || p.Name != name || p.Reason != reason {
			continue
		}
		if p.Severity != severity {
			t.Fatalf("%s/%s severity = %q, want %q; problem=%+v", kind, name, p.Severity, severity, p)
		}
		return
	}
	t.Fatalf("missing problem kind=%s name=%s reason=%q; got %+v", kind, name, reason, problems)
}

func lookupProblem(problems []Detection, kind, name, reason string) (Detection, bool) {
	for _, p := range problems {
		if p.Kind == kind && p.Name == name && p.Reason == reason {
			return p, true
		}
	}
	return Detection{}, false
}

func TestDetectProblems_PDBBlocksEvictions(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	old := metav1.NewTime(now.Add(-10 * time.Minute))
	one := intstr.FromInt32(1)
	half := intstr.FromString("50%")

	mkPDB := func(name string, minAvailable intstr.IntOrString, allowed, current, desired, expected int32) *policyv1.PodDisruptionBudget {
		return &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", CreationTimestamp: old, Generation: 1},
			Spec: policyv1.PodDisruptionBudgetSpec{
				MinAvailable: &minAvailable,
				Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			},
			Status: policyv1.PodDisruptionBudgetStatus{
				ObservedGeneration: 1,
				DisruptionsAllowed: allowed,
				CurrentHealthy:     current,
				DesiredHealthy:     desired,
				ExpectedPods:       expected,
				Conditions: []metav1.Condition{{
					Type:               policyv1.DisruptionAllowedCondition,
					Status:             metav1.ConditionFalse,
					Reason:             policyv1.InsufficientPodsReason,
					LastTransitionTime: old,
				}},
			},
		}
	}

	client := fake.NewClientset(
		mkPDB("blocked", one, 0, 1, 1, 1),                // all selected pods healthy, but no eviction budget
		mkPDB("temporarily-unhealthy", half, 0, 1, 1, 2), // no budget because a pod is unhealthy
		mkPDB("has-budget", half, 1, 3, 2, 3),            // healthy and at least one eviction allowed
		mkPDB("empty", one, 0, 0, 0, 0),                  // selector currently matches no pods
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()

	const reason = "Voluntary evictions blocked"
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hasProblem(DetectProblems(cache, "prod"), "PodDisruptionBudget", "blocked", reason) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	problems := DetectProblems(cache, "prod")

	p, ok := lookupProblem(problems, "PodDisruptionBudget", "blocked", reason)
	if !ok {
		t.Fatalf("missing blocked PDB problem; got %+v", problems)
	}
	if p.Severity != "high" || p.Group != "policy" {
		t.Fatalf("blocked PDB severity/group = %q/%q, want high/policy; problem=%+v", p.Severity, p.Group, p)
	}
	if !strings.Contains(p.Message, "node drains and upgrades cannot evict") {
		t.Fatalf("blocked PDB message should explain drain/upgrade impact; got %q", p.Message)
	}
	for _, name := range []string{"temporarily-unhealthy", "has-budget", "empty"} {
		if hasProblem(problems, "PodDisruptionBudget", name, reason) {
			t.Errorf("PDB %s should not be flagged as structurally blocking evictions: %+v", name, problems)
		}
	}
}

// TestDetectProblems_SharedRWOVolume pins the multi-replica ReadWriteOnce
// conflict detector: a Deployment wanting >1 replica that mounts an RWO PVC is
// flagged (only one node can attach it), while a single-replica RWO mount and a
// multi-replica ReadWriteMany mount are not.
func TestDetectProblems_SharedRWOVolume(t *testing.T) {
	defer ResetTestState()

	two := int32(2)
	one := int32(1)
	three := int32(3)

	mkDeploy := func(name string, replicas *int32, claim string) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod"},
			Spec: appsv1.DeploymentSpec{
				Replicas: replicas,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:         "app",
						VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
					}},
					Volumes: []corev1.Volume{{
						Name:         "data",
						VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}},
					}},
				}},
			},
		}
	}
	mkPVC := func(name string, mode corev1.PersistentVolumeAccessMode) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod"},
			Spec:       corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{mode}},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, AccessModes: []corev1.PersistentVolumeAccessMode{mode}},
		}
	}

	client := fake.NewClientset(
		mkDeploy("conflict", &two, "rwo-pvc"), // 2 replicas + RWO → flagged
		mkDeploy("single", &one, "rwo-pvc"),   // 1 replica + RWO → fine
		mkDeploy("rwx", &three, "rwx-pvc"),    // 3 replicas + RWX → fine
		mkPVC("rwo-pvc", corev1.ReadWriteOnce),
		mkPVC("rwx-pvc", corev1.ReadWriteMany),
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()

	const reason = "ReadWriteOnce volume shared across replicas"
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hasProblem(DetectProblems(cache, "prod"), "Deployment", "conflict", reason) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	problems := DetectProblems(cache, "prod")

	assertProblem(t, problems, "Deployment", "conflict", reason, "high")
	if hasProblem(problems, "Deployment", "single", reason) {
		t.Errorf("single-replica RWO mount should not be flagged: %+v", problems)
	}
	if hasProblem(problems, "Deployment", "rwx", reason) {
		t.Errorf("multi-replica RWX mount should not be flagged: %+v", problems)
	}
}

func TestDetectProblems_RolloutStuckExplainsRWORollingUpdate(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	old := metav1.NewTime(now.Add(-20 * time.Minute))
	transition := metav1.NewTime(now.Add(-5 * time.Minute))
	one := int32(1)

	mkDeploy := func(name string, strategy appsv1.DeploymentStrategyType) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", CreationTimestamp: old},
			Spec: appsv1.DeploymentSpec{
				Replicas: &one,
				Strategy: appsv1.DeploymentStrategy{Type: strategy},
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:         "app",
						VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
					}},
					Volumes: []corev1.Volume{{
						Name:         "data",
						VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}},
					}},
				}},
			},
			Status: appsv1.DeploymentStatus{
				Conditions: []appsv1.DeploymentCondition{{
					Type:               appsv1.DeploymentProgressing,
					Status:             corev1.ConditionFalse,
					Reason:             "ProgressDeadlineExceeded",
					Message:            "ReplicaSet has timed out progressing.",
					LastTransitionTime: transition,
				}},
			},
		}
	}
	client := fake.NewClientset(
		mkDeploy("rolling", appsv1.RollingUpdateDeploymentStrategyType),
		mkDeploy("recreate", appsv1.RecreateDeploymentStrategyType),
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "prod"},
			Spec:       corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}},
		},
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()

	const reason = "Rollout stuck"
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hasProblem(DetectProblems(cache, "prod"), "Deployment", "rolling", reason) &&
			hasProblem(DetectProblems(cache, "prod"), "Deployment", "recreate", reason) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	problems := DetectProblems(cache, "prod")

	rolling, ok := lookupProblem(problems, "Deployment", "rolling", reason)
	if !ok {
		t.Fatalf("missing rolling rollout problem; got %+v", problems)
	}
	if !strings.Contains(rolling.Message, "strategy: Recreate") || !strings.Contains(rolling.Message, `ReadWriteOnce PVC "data"`) {
		t.Fatalf("rolling rollout message should include RWO/RollingUpdate fix; got %q", rolling.Message)
	}
	recreate, ok := lookupProblem(problems, "Deployment", "recreate", reason)
	if !ok {
		t.Fatalf("missing recreate rollout problem; got %+v", problems)
	}
	if strings.Contains(recreate.Message, "strategy: Recreate") {
		t.Fatalf("recreate rollout should not get RWO/RollingUpdate hint; got %q", recreate.Message)
	}
}
