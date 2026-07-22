package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/internal/issues"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
)

func TestCrashCauseForDiagnoseSelectsAndDeduplicatesEvidence(t *testing.T) {
	now := time.Now()
	pods := []*corev1.Pod{
		activeCrashLoopPod("api-1", "app", now),
		activeCrashLoopPod("api-0", "app", now),
	}
	previous := []podLogEntry{
		{Pod: "api-0", Container: "app", Logs: aicontext.FilterLogs("WARN retrying\npanic: assignment to entry in nil map\nERROR cleanup failed")},
		{Pod: "api-1", Container: "app", Logs: aicontext.FilterLogs("WARN retrying\npanic: assignment to entry in nil map\nERROR cleanup failed")},
	}

	got, truncated := crashCauseForDiagnose(pods, nil, previous, now)
	if truncated {
		t.Fatal("crash cause unexpectedly truncated")
	}
	if len(got) != 1 {
		t.Fatalf("crash causes = %+v, want one deduplicated row", got)
	}
	cause := got[0]
	if strings.Join(cause.Pods, ",") != "api-0,api-1" {
		t.Fatalf("pods = %v, want both replicas", cause.Pods)
	}
	if cause.Container != "app" || cause.Reason != "Error" || cause.ExitCode != 1 {
		t.Fatalf("status attribution = %+v, want app/Error/1", cause)
	}
	if cause.LogLine != "panic: assignment to entry in nil map" || cause.LogSource != "previous" || cause.BestEffort {
		t.Fatalf("selected evidence = %+v, want matched previous panic", cause)
	}
}

func TestCrashCauseForDiagnoseUsesCurrentLogsForCurrentTermination(t *testing.T) {
	now := time.Now()
	pod := activeCrashLoopPod("api-0", "app", now)
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 2}}
	current := []podLogEntry{{Pod: "api-0", Container: "app", Logs: aicontext.FilterLogs("FATAL current instance failed")}}
	previous := []podLogEntry{{Pod: "api-0", Container: "app", Logs: aicontext.FilterLogs("FATAL previous instance failed")}}

	got, _ := crashCauseForDiagnose([]*corev1.Pod{pod}, current, previous, now)
	if len(got) != 1 {
		t.Fatalf("crash causes = %+v, want one", got)
	}
	if got[0].ExitCode != 2 || got[0].LogLine != "FATAL current instance failed" || got[0].LogSource != "current" {
		t.Fatalf("current termination pairing = %+v", got[0])
	}
}

func TestCrashCauseForDiagnoseFallbackAndRedaction(t *testing.T) {
	now := time.Now()
	pods := []*corev1.Pod{
		activeCrashLoopPod("fallback", "app", now),
		activeCrashLoopPod("secret", "app", now),
	}
	secret := "sk-abc123def456ghi789jkl012mno345pqr678stu901"
	previous := []podLogEntry{
		{Pod: "fallback", Container: "app", Logs: aicontext.FilterLogs("INFO starting\nconnection closed")},
		{Pod: "secret", Container: "app", Logs: aicontext.FilterLogs("FATAL authentication failed with key " + secret)},
	}

	got, _ := crashCauseForDiagnose(pods, nil, previous, now)
	if len(got) != 2 {
		t.Fatalf("crash causes = %+v, want two", got)
	}
	if got[0].LogLine != "connection closed" || !got[0].BestEffort {
		t.Fatalf("fallback evidence = %+v", got[0])
	}
	if strings.Contains(got[1].LogLine, secret) || !strings.Contains(got[1].LogLine, "[REDACTED]") {
		t.Fatalf("secret evidence was not preserved in redacted form: %q", got[1].LogLine)
	}
}

func TestCrashCauseForDiagnoseFailsClosed(t *testing.T) {
	now := time.Now()
	recovered := activeCrashLoopPod("recovered", "app", now)
	recovered.Spec.Containers[0].ReadinessProbe = &corev1.Probe{}
	recovered.Status.ContainerStatuses[0].Ready = true
	recovered.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-time.Minute))}}
	failedOnce := activeCrashLoopPod("failed-once", "app", now)
	failedOnce.Status.ContainerStatuses[0].RestartCount = 0
	missing := activeCrashLoopPod("missing", "app", now)
	errored := activeCrashLoopPod("errored", "app", now)
	empty := activeCrashLoopPod("empty", "app", now)
	previous := []podLogEntry{
		{Pod: "recovered", Container: "app", Logs: aicontext.FilterLogs("FATAL stale")},
		{Pod: "failed-once", Container: "app", Logs: aicontext.FilterLogs("FATAL not a crashloop")},
		{Pod: "errored", Container: "app", Error: "previous container not found"},
		{Pod: "empty", Container: "app", Logs: aicontext.FilterLogs("")},
	}

	got, truncated := crashCauseForDiagnose([]*corev1.Pod{recovered, failedOnce, missing, errored, empty}, nil, previous, now)
	if truncated || len(got) != 0 {
		t.Fatalf("fail-closed cases returned causes=%+v truncated=%v", got, truncated)
	}
}

func TestCrashCauseForDiagnoseSupportsAlreadyFetchedInitLogs(t *testing.T) {
	now := time.Now()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-0"},
		Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{
			Name: "migrate", RestartCount: 2,
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 127}},
		}}},
	}
	previous := []podLogEntry{{Pod: "api-0", Container: "migrate", Logs: aicontext.FilterLogs("FATAL migration binary missing")}}

	got, _ := crashCauseForDiagnose([]*corev1.Pod{pod}, nil, previous, now)
	if len(got) != 1 || got[0].Container != "migrate" || got[0].ExitCode != 127 {
		t.Fatalf("init crash cause = %+v", got)
	}
}

func TestCrashCauseForDiagnoseBoundsRowsAndLines(t *testing.T) {
	now := time.Now()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "many"}}
	previous := make([]podLogEntry, 0, maxDiagnoseCrashCauses+1)
	for i := 0; i < maxDiagnoseCrashCauses+1; i++ {
		name := "app-" + string(rune('a'+i))
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: name})
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{
			Name: name, RestartCount: 1,
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: int32(i + 1)}},
		})
		previous = append(previous, podLogEntry{Pod: "many", Container: name, Logs: aicontext.FilterLogs("FATAL bounded failure")})
	}

	got, truncated := crashCauseForDiagnose([]*corev1.Pod{pod}, nil, previous, now)
	if !truncated || len(got) != maxDiagnoseCrashCauses {
		t.Fatalf("bounded causes len=%d truncated=%v", len(got), truncated)
	}

	longPod := activeCrashLoopPod("long", "app", now)
	longLine := "FATAL " + strings.Repeat("界", maxCrashCauseRunes)
	longCauses, longTruncated := crashCauseForDiagnose(
		[]*corev1.Pod{longPod},
		nil,
		[]podLogEntry{{Pod: "long", Container: "app", Logs: aicontext.FilterLogs(longLine)}},
		now,
	)
	if longTruncated || len(longCauses) != 1 {
		t.Fatalf("long line causes=%+v truncated=%v", longCauses, longTruncated)
	}
	if len([]rune(longCauses[0].LogLine)) != maxCrashCauseRunes || !strings.HasSuffix(longCauses[0].LogLine, "…") {
		t.Fatalf("bounded line has %d runes", len([]rune(longCauses[0].LogLine)))
	}
}

func TestCrashCauseForDiagnoseBoundsTotalBytes(t *testing.T) {
	now := time.Now()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "many"}}
	previous := make([]podLogEntry, 0, maxDiagnoseCrashCauses)
	for i := 0; i < maxDiagnoseCrashCauses; i++ {
		name := "app-" + string(rune('a'+i))
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: name})
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{
			Name: name, RestartCount: 1,
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: int32(i + 1)}},
		})
		line := "FATAL " + strings.Repeat("界", maxCrashCauseRunes-6)
		previous = append(previous, podLogEntry{Pod: "many", Container: name, Logs: aicontext.FilterLogs(line)})
	}

	got, truncated := crashCauseForDiagnose([]*corev1.Pod{pod}, nil, previous, now)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(encoded) > maxCrashCauseBytes {
		t.Fatalf("byte-bounded causes len=%d bytes=%d truncated=%v", len(got), len(encoded), truncated)
	}
}

func TestDiagnoseResponseKeepsRelatedIssuesWithCrashCause(t *testing.T) {
	response := diagnoseResponse{
		Resource:      map[string]any{"kind": "Pod"},
		RelatedIssues: []issues.Issue{{Kind: "Pod", Name: "api-0"}},
		CrashCause: []diagnoseCrashCause{{
			Pods: []string{"api-0"}, Container: "app", Reason: "Error", ExitCode: 1,
			LogLine: "FATAL startup failed", LogSource: "previous",
		}},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		RelatedIssues []issues.Issue       `json:"relatedIssues"`
		CrashCause    []diagnoseCrashCause `json:"crashCause"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.RelatedIssues) != 1 || decoded.RelatedIssues[0].Name != "api-0" || len(decoded.CrashCause) != 1 {
		t.Fatalf("response lost additive fields: %+v", decoded)
	}
}

func TestSelectCrashLogLineSkipsOmissionSentinel(t *testing.T) {
	lines := []string{"ERROR first", "... (42 lines omitted) ...", "WARN last"}
	if got := selectCrashLogLine(lines); got != "WARN last" {
		t.Fatalf("selected %q, want last real generic line", got)
	}
}

func activeCrashLoopPod(name, container string, now time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: container}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: container, RestartCount: 2,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "Error", ExitCode: 1, FinishedAt: metav1.NewTime(now.Add(-time.Second)),
			}},
		}}},
	}
}
