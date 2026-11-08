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

func TestGetContainersForPodDefaultsIncludeInitContainersOnlyWhileInitializing(t *testing.T) {
	crashLooping := corev1.ContainerStatus{
		Name:                 "storage-initializer",
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
	}
	running := corev1.ContainerStatus{Name: "storage-initializer", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	completed := corev1.ContainerStatus{Name: "storage-initializer", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}
	notStarted := corev1.ContainerStatus{Name: "storage-initializer", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}}

	for _, tc := range []struct {
		name        string
		pod         *corev1.Pod
		includeInit bool
		want        []string
	}{
		{"crash-looping init container", initializingPod(corev1.ConditionFalse, crashLooping), true, []string{"storage-initializer", "kserve-container"}},
		{"running init container", initializingPod(corev1.ConditionFalse, running), true, []string{"storage-initializer", "kserve-container"}},
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
	withSidecar := initializingPod(corev1.ConditionFalse, crashLooping)
	withSidecar.Spec.InitContainers = append(withSidecar.Spec.InitContainers, corev1.Container{Name: "proxy", RestartPolicy: &always})
	withSidecar.Status.InitContainerStatuses = append(withSidecar.Status.InitContainerStatuses, running)
	withSidecar.Status.InitContainerStatuses[1].Name = "proxy"
	if got := GetContainersForPod(withSidecar, "", true); !reflect.DeepEqual(got, []string{"storage-initializer", "kserve-container"}) {
		t.Fatalf("native sidecar during initialization=%v", got)
	}
	if got := GetContainersForPod(withSidecar, "proxy", true); !reflect.DeepEqual(got, []string{"proxy"}) {
		t.Fatalf("explicit sidecar selection=%v", got)
	}

	pod := initializingPod(corev1.ConditionFalse, crashLooping)
	if got := GetContainersForPod(pod, "storage-initializer", true); !reflect.DeepEqual(got, []string{"storage-initializer"}) {
		t.Fatalf("explicit init selection=%v", got)
	}
	if got := GetContainersForPod(pod, "storage-initializer", false); got != nil {
		t.Fatalf("init selection without includeInit=%v", got)
	}
}
