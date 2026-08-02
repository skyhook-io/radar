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
	if cause.State != "down" {
		t.Fatalf("state = %q, want down", cause.State)
	}
	if cause.LogLine != "panic: assignment to entry in nil map" || cause.LogSource != "previous" || cause.LineSelection != crashLineFatalPattern {
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
	if got[0].LogLine != "connection closed" || got[0].LineSelection != crashLineLogTail {
		t.Fatalf("fallback evidence = %+v", got[0])
	}
	if strings.Contains(got[1].LogLine, secret) || !strings.Contains(got[1].LogLine, "[REDACTED]") {
		t.Fatalf("secret evidence was not preserved in redacted form: %q", got[1].LogLine)
	}
}

func TestCrashCauseForDiagnoseIncludesRecentServingWarning(t *testing.T) {
	now := time.Now()
	recovered := activeCrashLoopPod("recovered", "app", now)
	recovered.Spec.Containers[0].ReadinessProbe = &corev1.Probe{}
	recovered.Status.ContainerStatuses[0].Ready = true
	recovered.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-time.Minute))},
	}
	previous := []podLogEntry{{
		Pod: "recovered", Container: "app", Logs: aicontext.FilterLogs("FATAL startup dependency unavailable"),
	}}

	got, truncated := crashCauseForDiagnose([]*corev1.Pod{recovered}, nil, previous, now)
	if truncated || len(got) != 1 {
		t.Fatalf("recent serving crash causes=%+v truncated=%v, want one warning-context cause", got, truncated)
	}
	if strings.Join(got[0].Pods, ",") != "recovered" || got[0].LogLine != "FATAL startup dependency unavailable" {
		t.Fatalf("recent serving crash cause = %+v", got[0])
	}
	if got[0].State != "recovering" {
		t.Fatalf("recent serving crash state = %q, want recovering", got[0].State)
	}
}

func TestCrashCauseForDiagnoseKeepsRecoveringAndDownEvidenceSeparate(t *testing.T) {
	now := time.Now()
	down := activeCrashLoopPod("down", "app", now)
	recovering := activeCrashLoopPod("recovering", "app", now)
	recovering.Status.ContainerStatuses[0].Ready = true
	recovering.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-time.Minute))},
	}
	previous := []podLogEntry{
		{Pod: "down", Container: "app", Logs: aicontext.FilterLogs("FATAL shared startup failure")},
		{Pod: "recovering", Container: "app", Logs: aicontext.FilterLogs("FATAL shared startup failure")},
	}

	got, truncated := crashCauseForDiagnose([]*corev1.Pod{recovering, down}, nil, previous, now)
	if truncated || len(got) != 2 {
		t.Fatalf("crash causes=%+v truncated=%v, want distinct down and recovering rows", got, truncated)
	}
	if got[0].State != "down" || strings.Join(got[0].Pods, ",") != "down" {
		t.Fatalf("down crash cause = %+v", got[0])
	}
	if got[1].State != "recovering" || strings.Join(got[1].Pods, ",") != "recovering" {
		t.Fatalf("recovering crash cause = %+v", got[1])
	}
}

func TestCrashCauseForDiagnoseFailsClosed(t *testing.T) {
	now := time.Now()
	failedOnce := activeCrashLoopPod("failed-once", "app", now)
	failedOnce.Status.ContainerStatuses[0].RestartCount = 0
	missing := activeCrashLoopPod("missing", "app", now)
	errored := activeCrashLoopPod("errored", "app", now)
	empty := activeCrashLoopPod("empty", "app", now)
	previous := []podLogEntry{
		{Pod: "failed-once", Container: "app", Logs: aicontext.FilterLogs("FATAL not a crashloop")},
		{Pod: "errored", Container: "app", Error: "previous container not found"},
		{Pod: "empty", Container: "app", Logs: aicontext.FilterLogs("")},
	}

	got, truncated := crashCauseForDiagnose([]*corev1.Pod{failedOnce, missing, errored, empty}, nil, previous, now)
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

// TestSelectCrashLogLineCorpus pins the selected evidence line and its
// selection token across real-world crash-log shapes.
func TestSelectCrashLogLineCorpus(t *testing.T) {
	cases := []struct {
		name       string
		lines      []string
		unfiltered bool
		want       string
		selection  string
	}{
		{"go panic header beats later noise",
			[]string{"panic: runtime error: index out of range", "goroutine 1 [running]:", "ERROR shutdown hook failed"},
			false, "panic: runtime error: index out of range", crashLineFatalPattern},
		{"go runtime fatal error",
			[]string{"fatal error: concurrent map read and map write", "goroutine 7 [running]:"},
			false, "fatal error: concurrent map read and map write", crashLineFatalPattern},
		{"rust panicked at (1.73+ format)",
			[]string{"thread 'main' panicked at src/main.rs:4:6:", "note: run with `RUST_BACKTRACE=1`"},
			false, "thread 'main' panicked at src/main.rs:4:6:", crashLineFatalPattern},
		{"rust panicked at (pre-1.73 format)",
			[]string{"thread 'main' panicked at 'called Option::unwrap() on a None value', src/main.rs:5:20"},
			false, "thread 'main' panicked at 'called Option::unwrap() on a None value', src/main.rs:5:20", crashLineFatalPattern},
		{"python exception final beats bare traceback header",
			[]string{"Traceback (most recent call last):", `  File "/app/main.py", line 12, in <module>`, "redis.exceptions.ConnectionError: Error 111 connecting to redis:6379"},
			false, "redis.exceptions.ConnectionError: Error 111 connecting to redis:6379", crashLineFatalPattern},
		{"python bare KeyError final is crash-class",
			[]string{"Traceback (most recent call last):", `  File "/app/main.py", line 12, in <module>`, "KeyError: 'FLAG'"},
			false, "KeyError: 'FLAG'", crashLineFatalPattern},
		{"python chained tracebacks skip every bare header",
			[]string{"2026-07-22T10:00:00Z Traceback (most recent call last):", "2026-07-22T10:00:00Z Traceback (most recent call last):", "2026-07-22T10:00:01Z FATAL worker cannot start"},
			false, "2026-07-22T10:00:01Z FATAL worker cannot start", crashLineFatalPattern},
		{"line after a bare header is picked but labeled honestly, not as block content",
			[]string{"Traceback (most recent call last):", "WARN cleanup handler slow"},
			false, "WARN cleanup handler slow", crashLineLastMatchedLine},
		{"header-only capture is labeled as such, and a line preceding the traceback never substitutes",
			[]string{"INFO starting worker", "Traceback (most recent call last):"},
			false, "Traceback (most recent call last):", crashLineHeaderOnly},
		{"jvm exception in thread",
			[]string{`Exception in thread "main" java.lang.NullPointerException`, "\tat com.example.Main.main(Main.java:4)"},
			false, `Exception in thread "main" java.lang.NullPointerException`, crashLineFatalPattern},
		{"jvm caused-by class suffix",
			[]string{"Caused by: java.net.ConnectException: Connection refused"},
			false, "Caused by: java.net.ConnectException: Connection refused", crashLineFatalPattern},
		{"dotnet unhandled exception",
			[]string{"Unhandled exception. System.InvalidOperationException: Sequence contains no elements"},
			false, "Unhandled exception. System.InvalidOperationException: Sequence contains no elements", crashLineFatalPattern},
		{"node stack top",
			[]string{"TypeError: Cannot read properties of undefined (reading 'foo')", "    at Object.<anonymous> (/app/index.js:3:1)"},
			false, "TypeError: Cannot read properties of undefined (reading 'foo')", crashLineFatalPattern},
		{"ruby exception class suffix",
			[]string{"main.rb:4:in 'foo': undefined method 'bar' (NoMethodError)"},
			false, "main.rb:4:in 'foo': undefined method 'bar' (NoMethodError)", crashLineFatalPattern},
		{"signal death",
			[]string{"WARN latency high", "SIGSEGV: segmentation violation code=0x1 addr=0x18"},
			false, "SIGSEGV: segmentation violation code=0x1 addr=0x18", crashLineFatalPattern},
		{"fatal beats earlier generic error regardless of position",
			[]string{"ERROR connection pool exhausted", "FATAL shutting down"},
			false, "FATAL shutting down", crashLineFatalPattern},
		{"lowercase exception prose does not outrank a later real crash header",
			[]string{"WARN caught exception in background thread, continuing", "panic: send on closed channel"},
			false, "panic: send on closed channel", crashLineFatalPattern},
		{"dedup-decorated traceback header is still a bare header",
			[]string{"Traceback (most recent call last): (repeated x3)", "KeyError: 'FLAG'"},
			false, "KeyError: 'FLAG'", crashLineFatalPattern},
		{"dedup-decorated header alone is labeled header-only",
			[]string{"2026-07-22T10:00:00Z Traceback (most recent call last): (repeated ×4, 10:00:00→10:04:00)"},
			false, "2026-07-22T10:00:00Z Traceback (most recent call last): (repeated ×4, 10:00:00→10:04:00)", crashLineHeaderOnly},
		{"generic matches only pick the latest, labeled as matched-line not error",
			[]string{"ERROR db timeout", "... (42 lines omitted) ...", "WARN db retry gave up"},
			false, "WARN db retry gave up", crashLineLastMatchedLine},
		{"no matches at all fall to the raw tail, labeled as such",
			[]string{"INFO starting", "connection closed"},
			true, "connection closed", crashLineLogTail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, selection := selectCrashLogLine(tc.lines, tc.unfiltered)
			if got != tc.want || selection != tc.selection {
				t.Fatalf("selected %q (%s), want %q (%s)", got, selection, tc.want, tc.selection)
			}
		})
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
