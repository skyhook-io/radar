package capacity

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEffectivePodRequests_InitContainerMaximum(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Containers: []corev1.Container{
			containerWithRequests(map[corev1.ResourceName]string{corev1.ResourceCPU: "100m"}),
			containerWithRequests(map[corev1.ResourceName]string{corev1.ResourceCPU: "200m"}),
		},
		InitContainers: []corev1.Container{
			containerWithRequests(map[corev1.ResourceName]string{corev1.ResourceCPU: "400m"}),
			containerWithRequests(map[corev1.ResourceName]string{corev1.ResourceCPU: "600m"}),
		},
	}}

	requests := EffectivePodRequests(pod)
	assertQuantity(t, requests, corev1.ResourceCPU, "600m")
}

func TestEffectivePodRequests_RestartableInitSidecars(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Containers: []corev1.Container{
			containerWithRequests(map[corev1.ResourceName]string{corev1.ResourceCPU: "100m"}),
			containerWithRequests(map[corev1.ResourceName]string{corev1.ResourceCPU: "200m"}),
		},
		InitContainers: []corev1.Container{
			{
				RestartPolicy: &always,
				Resources: corev1.ResourceRequirements{Requests: resourceList(map[corev1.ResourceName]string{
					corev1.ResourceCPU: "200m",
				})},
			},
			containerWithRequests(map[corev1.ResourceName]string{corev1.ResourceCPU: "600m"}),
		},
	}}

	requests := EffectivePodRequests(pod)
	assertQuantity(t, requests, corev1.ResourceCPU, "800m")
	assertQuantity(t, requests, corev1.ResourcePods, "1")
}

func TestEffectivePodRequests_OverheadPodLevelAndExtendedResources(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Containers: []corev1.Container{containerWithRequests(map[corev1.ResourceName]string{
			corev1.ResourceCPU:              "200m",
			corev1.ResourceMemory:           "256Mi",
			corev1.ResourceEphemeralStorage: "1Gi",
			"nvidia.com/gpu":                "2",
			"hugepages-2Mi":                 "64Mi",
		})},
		Resources: &corev1.ResourceRequirements{Requests: resourceList(map[corev1.ResourceName]string{
			corev1.ResourceCPU:    "1",
			corev1.ResourceMemory: "2Gi",
		})},
		Overhead: resourceList(map[corev1.ResourceName]string{
			corev1.ResourceCPU:    "50m",
			corev1.ResourceMemory: "128Mi",
		}),
	}}

	requests := EffectivePodRequests(pod)
	assertQuantity(t, requests, corev1.ResourceCPU, "1050m")
	assertQuantity(t, requests, corev1.ResourceMemory, "2176Mi")
	assertQuantity(t, requests, corev1.ResourceEphemeralStorage, "1Gi")
	assertQuantity(t, requests, "nvidia.com/gpu", "2")
	assertQuantity(t, requests, "hugepages-2Mi", "64Mi")
	assertQuantity(t, requests, corev1.ResourcePods, "1")
}

func TestAccountResources_ClassifiesPodsAndUsesAllocatable(t *testing.T) {
	nodes := []*corev1.Node{
		{Status: corev1.NodeStatus{
			Capacity: resourceList(map[corev1.ResourceName]string{
				corev1.ResourceCPU: "8", corev1.ResourceMemory: "16Gi", "nvidia.com/gpu": "8",
			}),
			Allocatable: resourceList(map[corev1.ResourceName]string{
				corev1.ResourceCPU: "7600m", corev1.ResourceMemory: "14Gi", corev1.ResourcePods: "110", "nvidia.com/gpu": "4",
			}),
		}},
		{Status: corev1.NodeStatus{
			Capacity: resourceList(map[corev1.ResourceName]string{
				corev1.ResourceCPU: "32", corev1.ResourceMemory: "64Gi", "nvidia.com/gpu": "8",
			}),
			Allocatable: resourceList(map[corev1.ResourceName]string{
				corev1.ResourceCPU: "400m", corev1.ResourceMemory: "1Gi", corev1.ResourcePods: "10", "nvidia.com/gpu": "1",
			}),
		}},
	}
	assigned := podWithRequests("node-a", corev1.PodRunning, map[corev1.ResourceName]string{
		corev1.ResourceCPU: "500m", "nvidia.com/gpu": "1",
	})
	assignedPending := podWithRequests("node-a", corev1.PodPending, map[corev1.ResourceName]string{
		corev1.ResourceCPU: "250m",
	})
	unassigned := podWithRequests("", corev1.PodPending, map[corev1.ResourceName]string{
		corev1.ResourceCPU: "1", "example.com/fpga": "2",
	})
	daemon := podWithRequests("node-a", corev1.PodRunning, map[corev1.ResourceName]string{
		corev1.ResourceCPU: "100m",
	})
	controller := true
	daemon.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "apps/v1", Kind: "DaemonSet", Name: "agent", Controller: &controller,
	}}
	pendingDaemon := podWithRequests("", corev1.PodPending, map[corev1.ResourceName]string{
		corev1.ResourceCPU: "2",
	})
	pendingDaemon.OwnerReferences = daemon.OwnerReferences
	held := podWithRequests("", corev1.PodPending, map[corev1.ResourceName]string{
		corev1.ResourceCPU: "3",
	})
	held.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: "example.com/quota"}}
	succeeded := podWithRequests("node-a", corev1.PodSucceeded, map[corev1.ResourceName]string{
		corev1.ResourceCPU: "10",
	})
	failed := podWithRequests("", corev1.PodFailed, map[corev1.ResourceName]string{
		corev1.ResourceCPU: "10",
	})
	terminating := podWithRequests("", corev1.PodPending, map[corev1.ResourceName]string{
		corev1.ResourceCPU: "7",
	})
	deletedAt := metav1.NewTime(time.Now())
	terminating.DeletionTimestamp = &deletedAt

	accounting := AccountResources(nodes, []*corev1.Pod{assigned, assignedPending, unassigned, daemon, pendingDaemon, held, succeeded, failed, terminating})

	assertQuantity(t, accounting.Allocatable, corev1.ResourceCPU, "8")
	assertQuantity(t, accounting.Allocatable, corev1.ResourceMemory, "15Gi")
	assertQuantity(t, accounting.Allocatable, corev1.ResourcePods, "120")
	assertQuantity(t, accounting.Allocatable, "nvidia.com/gpu", "5")
	assertQuantity(t, accounting.ScheduledRequests, corev1.ResourceCPU, "850m")
	assertQuantity(t, accounting.ScheduledRequests, corev1.ResourcePods, "3")
	assertQuantity(t, accounting.ScheduledRequests, "nvidia.com/gpu", "1")
	assertQuantity(t, accounting.PendingRequests, corev1.ResourceCPU, "1")
	assertQuantity(t, accounting.PendingRequests, corev1.ResourcePods, "1")
	assertQuantity(t, accounting.PendingRequests, "example.com/fpga", "2")
	assertQuantity(t, accounting.DaemonRequests, corev1.ResourceCPU, "100m")
	assertQuantity(t, accounting.DaemonRequests, corev1.ResourcePods, "1")
}

func containerWithRequests(requests map[corev1.ResourceName]string) corev1.Container {
	return corev1.Container{Resources: corev1.ResourceRequirements{Requests: resourceList(requests)}}
}

func podWithRequests(nodeName string, phase corev1.PodPhase, requests map[corev1.ResourceName]string) *corev1.Pod {
	return &corev1.Pod{
		Spec: corev1.PodSpec{
			NodeName:   nodeName,
			Containers: []corev1.Container{containerWithRequests(requests)},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func resourceList(values map[corev1.ResourceName]string) corev1.ResourceList {
	result := make(corev1.ResourceList, len(values))
	for name, value := range values {
		result[name] = resource.MustParse(value)
	}
	return result
}

func assertQuantity(t *testing.T, resources corev1.ResourceList, name corev1.ResourceName, want string) {
	t.Helper()
	got, ok := resources[name]
	if !ok {
		t.Fatalf("resource %q absent from %v", name, resources)
	}
	wantQuantity := resource.MustParse(want)
	if got.Cmp(wantQuantity) != 0 {
		t.Errorf("resource %q = %s, want %s", name, got.String(), wantQuantity.String())
	}
}
