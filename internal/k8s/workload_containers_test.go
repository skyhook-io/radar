package k8s

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func initializingPod(initialized corev1.ConditionStatus, initStatus corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: initStatus.Name}},
			Containers:     []corev1.Container{{Name: "kserve-container"}},
		},
		Status: corev1.PodStatus{
			Conditions:            []corev1.PodCondition{{Type: corev1.PodInitialized, Status: initialized}},
			InitContainerStatuses: []corev1.ContainerStatus{initStatus},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "kserve-container",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}},
			}},
		},
	}
}

func TestGetContainersForPodDefaultsIncludeOnlyFailingInitContainersWhileInitializing(t *testing.T) {
	crashLooping := corev1.ContainerStatus{
		Name:                 "storage-initializer",
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
	}
	running := corev1.ContainerStatus{Name: "storage-initializer", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	completed := corev1.ContainerStatus{Name: "storage-initializer", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}
	retrying := corev1.ContainerStatus{
		Name:                 "storage-initializer",
		State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
	}
	failed := corev1.ContainerStatus{Name: "storage-initializer", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2, Reason: "Error"}}}
	recovered := corev1.ContainerStatus{
		Name:                 "storage-initializer",
		State:                corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
	}
	notStarted := corev1.ContainerStatus{Name: "storage-initializer", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}}

	for _, tc := range []struct {
		name        string
		pod         *corev1.Pod
		includeInit bool
		want        []string
	}{
		{"crash-looping init container", initializingPod(corev1.ConditionFalse, crashLooping), true, []string{"storage-initializer", "kserve-container"}},
		{"init container retrying after a failure", initializingPod(corev1.ConditionFalse, retrying), true, []string{"storage-initializer", "kserve-container"}},
		{"init container that exited with an error", initializingPod(corev1.ConditionFalse, failed), true, []string{"storage-initializer", "kserve-container"}},
		{"init container still making progress", initializingPod(corev1.ConditionFalse, running), true, []string{"kserve-container"}},
		{"init container that succeeded on a retry", initializingPod(corev1.ConditionFalse, recovered), true, []string{"kserve-container"}},
		{"init container OOM-killed with exit code 0", initializingPod(corev1.ConditionFalse, corev1.ContainerStatus{Name: "storage-initializer", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "OOMKilled"}}}), true, []string{"storage-initializer", "kserve-container"}},
		{"init container not started yet", initializingPod(corev1.ConditionFalse, notStarted), true, []string{"kserve-container"}},
		{"initialized Pod", initializingPod(corev1.ConditionTrue, completed), true, []string{"kserve-container"}},
		{"init containers not requested", initializingPod(corev1.ConditionFalse, crashLooping), false, []string{"kserve-container"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := GetContainersForPod(tc.pod, "", tc.includeInit); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("containers=%v; want %v", got, tc.want)
			}
		})
	}

	always := corev1.ContainerRestartPolicyAlways
	started := true
	withSidecar := initializingPod(corev1.ConditionFalse, crashLooping)
	withSidecar.Spec.InitContainers = append(withSidecar.Spec.InitContainers, corev1.Container{Name: "proxy", RestartPolicy: &always})
	startedSidecar := running
	startedSidecar.Name, startedSidecar.Started = "proxy", &started
	withSidecar.Status.InitContainerStatuses = append(withSidecar.Status.InitContainerStatuses, startedSidecar)
	if got := GetContainersForPod(withSidecar, "", true); !reflect.DeepEqual(got, []string{"storage-initializer", "kserve-container"}) {
		t.Fatalf("started native sidecar during initialization=%v", got)
	}
	failingSidecar := crashLooping
	failingSidecar.Name = "proxy"
	withSidecar.Status.InitContainerStatuses[1] = failingSidecar
	if got := GetContainersForPod(withSidecar, "", true); !reflect.DeepEqual(got, []string{"storage-initializer", "proxy", "kserve-container"}) {
		t.Fatalf("native sidecar failing to start=%v", got)
	}
	if got := GetContainersForPod(withSidecar, "proxy", true); !reflect.DeepEqual(got, []string{"proxy"}) {
		t.Fatalf("explicit sidecar selection=%v", got)
	}

	steps := initializingPod(corev1.ConditionFalse, crashLooping)
	steps.Spec.InitContainers = append([]corev1.Container{{Name: "istio-init"}}, steps.Spec.InitContainers...)
	steps.Status.InitContainerStatuses = append([]corev1.ContainerStatus{{Name: "istio-init", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}}, steps.Status.InitContainerStatuses...)
	if got := GetContainersForPod(steps, "", true); !reflect.DeepEqual(got, []string{"storage-initializer", "kserve-container"}) {
		t.Fatalf("finished step beside a failing one=%v", got)
	}
	if got := GetContainersForPod(steps, "istio-init", true); !reflect.DeepEqual(got, []string{"istio-init"}) {
		t.Fatalf("explicit finished step=%v", got)
	}

	startingSidecar := running
	startingSidecar.Name = "proxy"
	withSidecar.Status.InitContainerStatuses[1] = startingSidecar
	if got := GetContainersForPod(withSidecar, "", true); !reflect.DeepEqual(got, []string{"storage-initializer", "kserve-container"}) {
		t.Fatalf("native sidecar still starting=%v", got)
	}
	exitedSidecar := corev1.ContainerStatus{
		Name:                 "proxy",
		State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}},
	}
	withSidecar.Status.InitContainerStatuses[1] = exitedSidecar
	if got := GetContainersForPod(withSidecar, "", true); !reflect.DeepEqual(got, []string{"storage-initializer", "proxy", "kserve-container"}) {
		t.Fatalf("native sidecar that exited 0 before starting=%v", got)
	}

	pod := initializingPod(corev1.ConditionFalse, crashLooping)
	if got := GetContainersForPod(pod, "storage-initializer", true); !reflect.DeepEqual(got, []string{"storage-initializer"}) {
		t.Fatalf("explicit init selection=%v", got)
	}
	if got := GetContainersForPod(pod, "storage-initializer", false); got != nil {
		t.Fatalf("init selection without includeInit=%v", got)
	}
}
