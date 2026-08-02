package mcp

import (
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const emptyCrashLogMarker = "do NOT infer a root cause from the empty log"

func TestComputePodLogsWarnings_EmptyCrashLog(t *testing.T) {
	tests := []struct {
		name             string
		previous         bool
		rawLines         int
		crashed          bool
		wantMarker       bool
		wantPreviousHint bool
	}{
		{name: "empty previous crash log", previous: true, crashed: true, wantMarker: true},
		{name: "empty previous healthy log", previous: true},
		{name: "empty current crash log", crashed: true, wantPreviousHint: true},
		{name: "non-empty raw previous crash log including grep-empty response", previous: true, rawLines: 3, crashed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := logWarningTestPod("pod-a", tt.crashed)
			if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(pod)); err != nil {
				t.Fatalf("InitTestResourceCache: %v", err)
			}
			defer k8s.ResetTestState()

			warnings := computePodLogsWarnings("default", pod.Name, "app", tt.previous, tt.rawLines)
			if got := warningsContain(warnings, emptyCrashLogMarker); got != tt.wantMarker {
				t.Errorf("empty crash-log marker present = %v, want %v; warnings: %v", got, tt.wantMarker, warnings)
			}
			if tt.wantMarker {
				for _, detail := range []string{"container `app`", "last recorded termination was `Error`", "exit code 42"} {
					if !warningsContain(warnings, detail) {
						t.Errorf("marker missing %q: %v", detail, warnings)
					}
				}
			}
			if got := warningsContain(warnings, "call again with `previous: true`"); got != tt.wantPreviousHint {
				t.Errorf("previous-log hint present = %v, want %v; warnings: %v", got, tt.wantPreviousHint, warnings)
			}
		})
	}
}

func TestComputeWorkloadLogsWarnings_EmptyCrashLog(t *testing.T) {
	crashed := logWarningTestPod("pod-a", true)
	healthy := logWarningTestPod("pod-b", false)

	tests := []struct {
		name             string
		pods             []*corev1.Pod
		logs             []podLogEntry
		previous         bool
		wantMarker       bool
		wantPreviousHint bool
	}{
		{
			name:       "empty previous crash log",
			pods:       []*corev1.Pod{crashed},
			logs:       []podLogEntry{{Pod: "pod-a", Container: "app"}},
			previous:   true,
			wantMarker: true,
		},
		{
			name:     "empty previous healthy log",
			pods:     []*corev1.Pod{healthy},
			logs:     []podLogEntry{{Pod: "pod-b", Container: "app"}},
			previous: true,
		},
		{
			name:             "empty current crash log",
			pods:             []*corev1.Pod{crashed},
			logs:             []podLogEntry{{Pod: "pod-a", Container: "app"}},
			wantPreviousHint: true,
		},
		{
			name:     "non-empty raw previous crash log including grep-empty response",
			pods:     []*corev1.Pod{crashed},
			logs:     []podLogEntry{{Pod: "pod-a", Container: "app", RawLines: 3}},
			previous: true,
		},
		{
			name:     "failed previous crash-log fetch",
			pods:     []*corev1.Pod{crashed},
			logs:     []podLogEntry{{Pod: "pod-a", Container: "app", Error: "fetch failed"}},
			previous: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings := computeWorkloadLogsWarnings(tt.pods, tt.logs, tt.previous)
			if got := warningsContain(warnings, emptyCrashLogMarker); got != tt.wantMarker {
				t.Errorf("empty crash-log marker present = %v, want %v; warnings: %v", got, tt.wantMarker, warnings)
			}
			if tt.wantMarker {
				for _, detail := range []string{"1 previous pod/container instance(s)", "`pod-a/app`", "last recorded termination: `Error`", "exit code 42"} {
					if !warningsContain(warnings, detail) {
						t.Errorf("marker missing %q: %v", detail, warnings)
					}
				}
			}
			if got := warningsContain(warnings, "call again with `previous: true`"); got != tt.wantPreviousHint {
				t.Errorf("previous-log hint present = %v, want %v; warnings: %v", got, tt.wantPreviousHint, warnings)
			}
		})
	}
}

func TestComputeWorkloadLogsWarnings_AggregatesEmptyCrashLogs(t *testing.T) {
	podA := logWarningTestPod("pod-a", true)
	podB := logWarningTestPod("pod-b", true)
	warnings := computeWorkloadLogsWarnings(
		[]*corev1.Pod{podA, podB},
		[]podLogEntry{
			{Pod: "pod-a", Container: "app"},
			{Pod: "pod-b", Container: "app"},
		},
		true,
	)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one aggregate warning", warnings)
	}
	if !warningsContain(warnings, "2 previous pod/container instance(s)") || !warningsContain(warnings, "`pod-a/app`") {
		t.Fatalf("aggregate warning missing count or example: %v", warnings)
	}
}

func TestComputeWorkloadLogsWarnings_MatchesEmptyLogToContainer(t *testing.T) {
	pod := logWarningTestPod("pod-a", true)
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "sidecar"})
	pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{Name: "sidecar"})

	warnings := computeWorkloadLogsWarnings(
		[]*corev1.Pod{pod},
		[]podLogEntry{{Pod: "pod-a", Container: "sidecar"}},
		true,
	)
	if warningsContain(warnings, emptyCrashLogMarker) {
		t.Fatalf("healthy sidecar inherited app crash marker: %v", warnings)
	}
}

func logWarningTestPod(name string, crashed bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app"}},
		},
	}
	if crashed {
		pod.Status.ContainerStatuses[0].RestartCount = 2
		pod.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{
			Reason:   "Error",
			ExitCode: 42,
		}
	}
	return pod
}

func warningsContain(warnings []string, substring string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substring) {
			return true
		}
	}
	return false
}
