package k8s

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDetectProblems_CrashLoopSeverityTracksCurrentState(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	controller := true
	crash := func(finished time.Time) corev1.ContainerState {
		return corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason:     "Error",
			ExitCode:   1,
			FinishedAt: metav1.NewTime(finished),
		}}
	}
	runningCrashPod := func(name string, started time.Time, restarts int32) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Namespace:         "prod",
				CreationTimestamp: metav1.NewTime(now.Add(-8 * time.Minute)),
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:                 "app",
					Ready:                true,
					RestartCount:         restarts,
					State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(started)}},
					LastTerminationState: crash(started.Add(-time.Second)),
				}},
			},
		}
	}

	recoveredStartup := runningCrashPod("recovered-startup", now.Add(-time.Minute), 2)
	recoveredStartup.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "apps/v1",
		Kind:       "ReplicaSet",
		Name:       "catalog-rs",
		Controller: &controller,
	}}
	highCountServing := runningCrashPod("high-count-serving", now.Add(-time.Minute), 40)
	stable := runningCrashPod("stable", now.Add(-6*time.Minute), 2)
	probeReady := runningCrashPod("probe-ready", now.Add(-time.Minute), 2)
	probeReady.Spec.Containers = []corev1.Container{{Name: "app", ReadinessProbe: &corev1.Probe{}}}
	imagePullSibling := runningCrashPod("image-pull-sibling", now.Add(-time.Minute), 2)
	imagePullSibling.Status.ContainerStatuses = append(imagePullSibling.Status.ContainerStatuses, corev1.ContainerStatus{
		Name:  "image",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
	})
	oomSibling := runningCrashPod("oom-sibling", now.Add(-time.Minute), 2)
	oomSibling.Status.ContainerStatuses = append(oomSibling.Status.ContainerStatuses, corev1.ContainerStatus{
		Name: "memory",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137,
		}},
	})
	fatalInit := runningCrashPod("fatal-init", now.Add(-time.Minute), 2)
	fatalInit.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name:  "setup",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError"}},
	}}
	downAtCreation := runningCrashPod("down-at-creation", now.Add(-time.Minute), 2)
	downAtCreation.Status.ContainerStatuses[0].Ready = false
	downAtCreation.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
	}
	runtimeDown := downAtCreation.DeepCopy()
	runtimeDown.Name = "runtime-down"
	runtimeDown.CreationTimestamp = metav1.NewTime(now.Add(-2 * time.Hour))
	stillStarting := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "still-starting",
			Namespace:         "prod",
			CreationTimestamp: metav1.NewTime(now.Add(-time.Minute)),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "app",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}},
			}},
		},
	}

	deployCreated := now.Add(-8 * time.Minute)
	client := fake.NewClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name:              "catalog",
			Namespace:         "prod",
			CreationTimestamp: metav1.NewTime(deployCreated),
		}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name:              "catalog-rs",
			Namespace:         "prod",
			CreationTimestamp: metav1.NewTime(deployCreated),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "catalog",
				Controller: &controller,
			}},
		}},
		recoveredStartup,
		highCountServing,
		stable,
		probeReady,
		imagePullSibling,
		oomSibling,
		fatalInit,
		downAtCreation,
		runtimeDown,
		stillStarting,
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	var problems []Detection
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		problems = DetectProblems(GetResourceCache(), "prod")
		if hasProblem(problems, "Pod", "recovered-startup", "CrashLoopBackOff") &&
			hasProblem(problems, "Pod", "down-at-creation", "CrashLoopBackOff") &&
			hasProblem(problems, "Pod", "image-pull-sibling", "ImagePullBackOff") &&
			hasProblem(problems, "Pod", "oom-sibling", "OOMKilled") &&
			hasProblem(problems, "Pod", "fatal-init", "CreateContainerConfigError") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "Pod", "recovered-startup", "CrashLoopBackOff", "high")
	recovered, ok := lookupProblem(problems, "Pod", "recovered-startup", "CrashLoopBackOff")
	if !ok || recovered.IssueTiming != "started_at_resource_creation" {
		t.Fatalf("recovered startup issue = %+v, want warning with at-creation timing", recovered)
	}
	if !strings.Contains(recovered.Cause, "is serving again") || strings.Contains(recovered.Cause, "is crashlooping") {
		t.Fatalf("recovered startup cause = %q, want serving recovery copy", recovered.Cause)
	}
	if !strings.Contains(recovered.Action, "Watch for another restart") {
		t.Fatalf("recovered startup action = %q, want repeat-crash guidance", recovered.Action)
	}
	assertProblem(t, problems, "Pod", "high-count-serving", "CrashLoopBackOff", "high")
	assertProblem(t, problems, "Pod", "down-at-creation", "CrashLoopBackOff", "critical")
	assertProblem(t, problems, "Pod", "runtime-down", "CrashLoopBackOff", "critical")
	assertProblem(t, problems, "Pod", "image-pull-sibling", "ImagePullBackOff", "critical")
	assertProblem(t, problems, "Pod", "oom-sibling", "OOMKilled", "critical")
	assertProblem(t, problems, "Pod", "fatal-init", "CreateContainerConfigError", "critical")

	for _, name := range []string{"stable", "still-starting"} {
		if _, ok := lookupProblem(problems, "Pod", name, "CrashLoopBackOff"); ok {
			t.Errorf("%s unexpectedly emitted a crashloop problem: %+v", name, problems)
		}
	}
	assertProblem(t, problems, "Pod", "probe-ready", "CrashLoopBackOff", "high")
}
