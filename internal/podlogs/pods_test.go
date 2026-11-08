package podlogs

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestBuildPodInfoMarksNativeSidecars(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "proxy", RestartPolicy: &always}, {Name: "wait-gcs-ready"}},
			Containers:     []corev1.Container{{Name: "ray-worker"}},
		},
		Status: corev1.PodStatus{
			Phase:                 corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{{Name: "proxy"}, {Name: "wait-gcs-ready"}},
			ContainerStatuses:     []corev1.ContainerStatus{{Name: "ray-worker"}},
		},
	}
	got := map[string]ContainerInfo{}
	for _, status := range BuildPodInfo(pod, time.Now()).ContainerStatuses {
		got[status.Name] = status
	}
	if !got["proxy"].Init || !got["proxy"].Sidecar {
		t.Fatalf("proxy = %+v, want an init sidecar", got["proxy"])
	}
	if !got["wait-gcs-ready"].Init || got["wait-gcs-ready"].Sidecar {
		t.Fatalf("wait-gcs-ready = %+v, want a regular init container", got["wait-gcs-ready"])
	}
	if got["ray-worker"].Init || got["ray-worker"].Sidecar {
		t.Fatalf("ray-worker = %+v, want an application container", got["ray-worker"])
	}
}

func TestBuildPodInfoReportsContainerStates(t *testing.T) {
	started := true
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "storage-initializer"}},
			Containers:     []corev1.Container{{Name: "kserve-container"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name:                 "storage-initializer",
				State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
			}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "kserve-container",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}},
			}},
		},
	}
	got := BuildPodInfo(pod, time.Now()).ContainerStatuses
	want := []ContainerInfo{
		{Name: "storage-initializer", Init: true, State: "waiting", Reason: "CrashLoopBackOff"},
		{Name: "kserve-container", State: "waiting", Reason: "PodInitializing"},
	}
	if len(got) != len(want) {
		t.Fatalf("statuses=%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("status %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	pod.Status.InitContainerStatuses[0] = corev1.ContainerStatus{Name: "storage-initializer", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}}
	pod.Status.ContainerStatuses[0] = corev1.ContainerStatus{Name: "kserve-container", Ready: true, Started: &started, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	got = BuildPodInfo(pod, time.Now()).ContainerStatuses
	if got[0].State != "terminated" || got[0].Reason != "Completed" || got[1].State != "running" || !got[1].Started {
		t.Fatalf("initialized statuses=%+v", got)
	}
}
