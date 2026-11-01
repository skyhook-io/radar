package health

import (
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Messages are the kubelet's own formats (pkg/kubelet/eviction/helpers.go and
// the eviction manager's admission rejection).
func TestParsePodEviction(t *testing.T) {
	cases := []struct {
		name     string
		message  string
		want     PodEviction
		pressure string
	}{
		{
			name:    "disk pressure, current format",
			message: "The node was low on resource: ephemeral-storage. Threshold quantity: 4013935590, available: 3812960Ki. Container app was using 2380Ki, request is 0, has larger consumption of ephemeral-storage. ",
			want: PodEviction{Kind: EvictionNodePressure, Resource: "ephemeral-storage", Threshold: "4013935590", Available: "3812960Ki",
				Containers: []EvictedContainer{{Name: "app", Usage: "2380Ki", Request: "0"}}},
			pressure: "DiskPressure",
		},
		{
			name:    "memory pressure, two containers, fractional request",
			message: "The node was low on resource: memory. Threshold quantity: 100Mi, available: 91844Ki. Container app was using 1.5Gi, request is 512Mi, has larger consumption of memory. Container sidecar was using 80Mi, request is 0, has larger consumption of memory. ",
			want: PodEviction{Kind: EvictionNodePressure, Resource: "memory", Threshold: "100Mi", Available: "91844Ki",
				Containers: []EvictedContainer{{Name: "app", Usage: "1.5Gi", Request: "512Mi"}, {Name: "sidecar", Usage: "80Mi", Request: "0"}}},
			pressure: "MemoryPressure",
		},
		{
			name:     "legacy container format, no threshold",
			message:  "The node was low on resource: ephemeral-storage. Container writer was using 12Gi, which exceeds its request of 0. ",
			want:     PodEviction{Kind: EvictionNodePressure, Resource: "ephemeral-storage", Containers: []EvictedContainer{{Name: "writer", Usage: "12Gi", Request: "0"}}},
			pressure: "DiskPressure",
		},
		{
			name:     "inodes",
			message:  "The node was low on resource: inodes. Threshold quantity: 5%, available: 1024. ",
			want:     PodEviction{Kind: EvictionNodePressure, Resource: "inodes", Threshold: "5%", Available: "1024"},
			pressure: "DiskPressure",
		},
		{
			name:     "pids",
			message:  "The node was low on resource: pids. Threshold quantity: 3276, available: 1200. ",
			want:     PodEviction{Kind: EvictionNodePressure, Resource: "pids", Threshold: "3276", Available: "1200"},
			pressure: "PIDPressure",
		},
		{
			name:     "admission rejection",
			message:  "The node had condition: [DiskPressure]. ",
			want:     PodEviction{Kind: EvictionNodeCondition, Resource: "ephemeral-storage", Conditions: []string{"DiskPressure"}},
			pressure: "DiskPressure",
		},
		{
			name:    "container ephemeral-storage limit",
			message: `Pod ephemeral local storage usage exceeds the total limit of containers 1Gi. Container app exceeded its local ephemeral storage limit "1Gi". `,
			want: PodEviction{Kind: EvictionLocalStorageLimit, Resource: "ephemeral-storage",
				Containers:  []EvictedContainer{{Name: "app", Limit: "1Gi"}},
				LimitDetail: `Container app exceeded its local ephemeral storage limit "1Gi".`},
		},
		{
			name:    "emptyDir limit",
			message: `Usage of EmptyDir volume "cache" exceeds the limit "500Mi". `,
			want: PodEviction{Kind: EvictionLocalStorageLimit, Resource: "ephemeral-storage",
				LimitDetail: `Usage of EmptyDir volume "cache" exceeds the limit "500Mi".`},
		},
		{
			name:    "unparsed",
			message: "Pod was evicted for an unknown reason",
			want:    PodEviction{Kind: EvictionUnparsed},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParsePodEviction(tc.message)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParsePodEviction() = %+v, want %+v", got, tc.want)
			}
			if p := got.PressureCondition(); p != tc.pressure {
				t.Errorf("PressureCondition() = %q, want %q", p, tc.pressure)
			}
		})
	}
}

// The kubelet kills an evicted pod's containers, which then report Error or
// ContainerStatusUnknown with exit 137. That is a consequence of the eviction,
// so the pod-level reason and message must describe the pod.
func TestPodProblemReasonEvictedOutranksKilledContainers(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	const msg = "The node was low on resource: memory. Threshold quantity: 100Mi, available: 91844Ki. "
	for _, term := range []corev1.ContainerStateTerminated{
		{Reason: "Error", ExitCode: 137},
		{Reason: "ContainerStatusUnknown", ExitCode: 137, Message: "The container could not be located when the pod was terminated"},
	} {
		pod := &corev1.Pod{Status: corev1.PodStatus{
			Phase: corev1.PodFailed, Reason: "Evicted", Message: msg,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Terminated: &term}}},
		}}
		if got := PodProblemReason(pod, now); got != "Evicted" {
			t.Errorf("%s: PodProblemReason = %q, want Evicted", term.Reason, got)
		}
		if got := PodProblemMessage(pod); got != msg {
			t.Errorf("%s: PodProblemMessage = %q, want the eviction message", term.Reason, got)
		}
	}
}
