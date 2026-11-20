package health

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestPodGoldenVectors is the canonical, clock-injected health contract for
// pods. Every case fixes an explicit `now` and uses timestamps relative to it,
// so the table is reproducible and portable (the frontend mirrors it in vitest to
// keep the TS classifier from drifting). It asserts both the Level and, for
// problem pods, the Reason token.
func TestPodGoldenVectors(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	old := metav1.NewTime(now.Add(-10 * time.Minute))
	recent := metav1.NewTime(now.Add(-1 * time.Minute))

	cases := []struct {
		name       string
		pod        *corev1.Pod
		wantLevel  Level
		wantReason string // "" = don't assert reason
	}{
		{
			name: "healthy running pod",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase:             corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{Ready: true, RestartCount: 0}},
			}},
			wantLevel: LevelHealthy,
		},
		{
			name:       "succeeded pod is neutral (completed)",
			pod:        &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
			wantLevel:  LevelNeutral,
			wantReason: "Completed",
		},
		{
			// Batch work in flight is not an all-clear. Workload() already says
			// this for the Job itself (jobVerdict returns Neutral/"Running");
			// without the same call here the Job read Neutral while its own pods
			// read Healthy — one piece of work graded two ways.
			name: "job-owned pod is neutral, not an affirmative healthy",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{
					{Kind: "Job", APIVersion: "batch/v1", Name: "archiver-123"},
				}},
				Status: corev1.PodStatus{
					Phase:             corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{{Ready: true}},
				},
			},
			wantLevel: LevelNeutral,
		},
		{
			// The masking guard. Neutral sits after every failure check, so a
			// genuinely broken Job pod keeps its real verdict. If this ever
			// returns Neutral the ordering has regressed and failing batch
			// workloads have gone silent.
			name: "crashlooping job-owned pod stays unhealthy",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{
					{Kind: "Job", APIVersion: "batch/v1", Name: "archiver-123"},
				}},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{{
						Ready:        false,
						RestartCount: 6,
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
						},
					}},
				},
			},
			wantLevel:  LevelUnhealthy,
			wantReason: "CrashLoopBackOff",
		},
		{
			// A CRD that merely happens to be Kind "Job" must not match; the
			// owner's API group is checked, not just the kind string.
			name: "non-batch Job kind does not trigger the neutral branch",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{
					{Kind: "Job", APIVersion: "acme.example.com/v1", Name: "not-a-batch-job"},
				}},
				Status: corev1.PodStatus{
					Phase:             corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{{Ready: true}},
				},
			},
			wantLevel: LevelHealthy,
		},
		{
			name:      "failed pod is unhealthy",
			pod:       &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed}},
			wantLevel: LevelUnhealthy,
		},
		{
			// Node unreachable / lost — container states are stale, so genuinely
			// unknown, not the default healthy.
			name:      "node-lost (phase Unknown) is unknown",
			pod:       &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodUnknown}},
			wantLevel: LevelUnknown,
		},
		{
			name: "CrashLoopBackOff is unhealthy",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
				},
			}},
			wantLevel:  LevelUnhealthy,
			wantReason: "CrashLoopBackOff",
		},
		{
			name: "OOMKilled is unhealthy",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}}},
				},
			}},
			wantLevel:  LevelUnhealthy,
			wantReason: "OOMKilled",
		},
		{
			name: "recovered LastTerminationState OOMKilled is healthy",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Ready:                true,
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
				}},
			}},
			wantLevel: LevelHealthy,
		},
		{
			name: "init container fatal waiting is unhealthy",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: old},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					InitContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
					},
				},
			},
			wantLevel:  LevelUnhealthy,
			wantReason: "ImagePullBackOff",
		},
		{
			name: "pending over 5 minutes is degraded",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: old},
				Status:     corev1.PodStatus{Phase: corev1.PodPending},
			},
			wantLevel:  LevelDegraded,
			wantReason: "Pending",
		},
		{
			name: "recently pending is healthy (startup grace)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: recent},
				Status:     corev1.PodStatus{Phase: corev1.PodPending},
			},
			wantLevel: LevelHealthy,
		},
		{
			name: "readiness probe failed long enough is degraded",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: old},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", ReadinessProbe: &corev1.Probe{}}}},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{{
						Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: old,
					}},
					ContainerStatuses: []corev1.ContainerStatus{{
						Name: "app", Ready: false,
						State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: old}},
					}},
				},
			},
			wantLevel:  LevelDegraded,
			wantReason: "ReadinessProbeFailed",
		},
		{
			name: "recent readiness probe failure is still starting (healthy)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: recent},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", ReadinessProbe: &corev1.Probe{}}}},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{{
						Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: recent,
					}},
					ContainerStatuses: []corev1.ContainerStatus{{
						Name: "app", Ready: false,
						State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: recent}},
					}},
				},
			},
			wantLevel: LevelHealthy,
		},
		{
			name: "recovered: high restart count but now ready and stable is healthy",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Ready: true, RestartCount: 10,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-2 * time.Hour))}},
				}},
			}},
			wantLevel: LevelHealthy,
		},
		{
			name: "actively thrashing is degraded",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Ready: false, RestartCount: 1659,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-30 * time.Minute))}},
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						Reason: "Completed", ExitCode: 0, FinishedAt: metav1.NewTime(now.Add(-30 * time.Second)),
					}},
				}},
			}},
			wantLevel:  LevelDegraded,
			wantReason: "HighRestartCount",
		},
		{
			name: "stale restarts (days old) is healthy",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Ready: false, RestartCount: 200,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-72 * time.Hour))}},
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						Reason: "Completed", ExitCode: 0, FinishedAt: metav1.NewTime(now.Add(-72 * time.Hour)),
					}},
				}},
			}},
			wantLevel: LevelHealthy,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Pod(c.pod, now)
			if got.Level != c.wantLevel {
				t.Errorf("Pod().Level = %q, want %q", got.Level, c.wantLevel)
			}
			if c.wantReason != "" && got.Reason != c.wantReason {
				t.Errorf("Pod().Reason = %q, want %q", got.Reason, c.wantReason)
			}
		})
	}
}

// TestPodStableCrashLoopAcrossPhases pins a non-serving crashloop's continuity
// while its instantaneous State flaps Waiting → Running → Waiting.
func TestPodStableCrashLoopAcrossPhases(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	crashHistory := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}
	mkPod := func(state corev1.ContainerState) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				RestartCount: 7, State: state, LastTerminationState: crashHistory,
			}},
		}}
	}
	states := []corev1.ContainerState{
		{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now)}},
		{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
	}
	for i, st := range states {
		v := Pod(mkPod(st), now)
		if v.Level != LevelUnhealthy || v.Reason != "CrashLoopBackOff" {
			t.Errorf("phase %d: got {%q,%q}, want stable {unhealthy,CrashLoopBackOff}", i, v.Level, v.Reason)
		}
	}
}

// TestPodProblemReasonInitWalk pins that init-container reasons win over the bare
// Pending phase (the init-blocking case) and that specific reasons aren't clobbered.
func TestPodProblemReasonInitWalk(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{
			name: "init waiting reason wins over phase",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				InitContainerStatuses: []corev1.ContainerStatus{
					{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
				},
			}},
			want: "CrashLoopBackOff",
		},
		{
			name: "falls back to phase",
			pod:  &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}},
			want: "Pending",
		},
		{
			name: "active ImagePullBackOff keeps specific reason over crashloop",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					RestartCount:         2,
					State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
				}},
			}},
			want: "ImagePullBackOff",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PodProblemReason(c.pod, now); got != c.want {
				t.Errorf("PodProblemReason = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPodCrashLoopDiagnosis(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	started := metav1.NewTime(now.Add(-3 * time.Second))
	finished := metav1.NewTime(now.Add(-2 * time.Second))
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "app",
				RestartCount: 2,
				State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-1 * time.Second))}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "Error", ExitCode: 127, StartedAt: started, FinishedAt: finished,
				}},
			}},
		},
	}
	cause, action := PodCrashLoopDiagnosis(pod, now)
	if !strings.Contains(cause, `container "app"`) || !strings.Contains(cause, "code 127") || !strings.Contains(cause, "within seconds") {
		t.Fatalf("cause = %q, want app exit-127 diagnosis with short-run context", cause)
	}
	if !strings.Contains(action, "command/args") {
		t.Fatalf("action = %q, want command/args guidance", action)
	}
}

// An init container that exited 0 did its job. Reporting its "Completed" as the
// pod's problem reason names a success as the fault, buries the real failure in
// the container below it, and classifies to the catch-all category — so the row
// reads "critical" with nothing to act on. CNPG's bootstrap pods hit this
// (successful init, failed join), but any pod with init containers can.
func TestPodProblemReasonSkipsSuccessfulInitContainers(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodFailed,
		InitContainerStatuses: []corev1.ContainerStatus{
			{Name: "bootstrap", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0}}},
		},
		ContainerStatuses: []corev1.ContainerStatus{
			{Name: "app", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, Message: "join failed"}}},
		},
	}}

	if got := PodProblemReason(pod, now); got != "Error" {
		t.Errorf("PodProblemReason = %q, want %q — the failed container is the problem, not the init container that succeeded", got, "Error")
	}
	if got := PodProblemMessage(pod); got != "join failed" {
		t.Errorf("PodProblemMessage = %q, want %q", got, "join failed")
	}
}

// A failed init container is still the pod's problem — it blocks everything
// after it, and the main containers never start.
func TestPodProblemReasonKeepsFailedInitContainers(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodPending,
		InitContainerStatuses: []corev1.ContainerStatus{
			{Name: "ok", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0}}},
			{Name: "broken", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 2, Message: "migration failed"}}},
		},
	}}

	if got := PodProblemReason(pod, now); got != "Error" {
		t.Errorf("PodProblemReason = %q, want %q", got, "Error")
	}
	if got := PodProblemMessage(pod); got != "migration failed" {
		t.Errorf("PodProblemMessage = %q, want %q", got, "migration failed")
	}
}

// The reason and the message must describe the same container. A chatty init
// container that succeeded used to supply the message while the failed one
// supplied the reason, pairing a failure with an unrelated explanation.
func TestPodProblemMessageMatchesTheReasonsContainer(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodFailed,
		InitContainerStatuses: []corev1.ContainerStatus{
			{Name: "bootstrap", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "Completed", ExitCode: 0, Message: "bootstrap finished cleanly"}}},
		},
		ContainerStatuses: []corev1.ContainerStatus{
			{Name: "app", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "Error", ExitCode: 1, Message: "could not reach primary"}}},
		},
	}}

	if got := PodProblemReason(pod, now); got != "Error" {
		t.Errorf("PodProblemReason = %q, want %q", got, "Error")
	}
	if got := PodProblemMessage(pod); got != "could not reach primary" {
		t.Errorf("PodProblemMessage = %q, want the failed container's message", got)
	}
}

// A pod whose containers all succeeded still reports what it did, rather than
// falling through to a bare phase.
func TestPodProblemReasonStillReportsAnAllSucceededPod(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodSucceeded,
		ContainerStatuses: []corev1.ContainerStatus{
			{Name: "job", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0}}},
		},
	}}

	if got := PodProblemReason(pod, now); got != "Completed" {
		t.Errorf("PodProblemReason = %q, want %q", got, "Completed")
	}
}

// A completed init container must not shadow a derived pod-level failure. The
// readiness check used to sit behind the plain container walk, so a pod whose
// init container finished cleanly reported "Completed" while its main container
// failed readiness.
func TestPodProblemReasonSucceededInitDoesNotShadowReadinessFailure(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	// Every clause podHasReadinessProbeFailure requires must hold, or this
	// passes through the phase fallback and proves nothing about precedence:
	// a declared readiness probe, Running phase, and Ready=False for over 5min.
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:           "app",
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz"}}},
		}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionFalse,
				LastTransitionTime: metav1.NewTime(now.Add(-20 * time.Minute)),
			}},
			InitContainerStatuses: []corev1.ContainerStatus{
				{Name: "setup", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0}}},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: false, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-20 * time.Minute))}}},
			},
		},
	}

	if got := PodProblemReason(pod, now); got != reasonReadinessProbeFail {
		t.Errorf("PodProblemReason = %q, want %q — a completed init container shadowed the readiness failure", got, reasonReadinessProbeFail)
	}
}

// The message must be gated the same way the reason is. A still-running pod
// takes its reason from readiness or from the phase, and used to take its
// message from a container that had merely finished.
func TestPodProblemMessageDoesNotBorrowOnAStillRunningPod(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:           "app",
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz"}}},
		}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionFalse,
				LastTransitionTime: metav1.NewTime(now.Add(-20 * time.Minute)),
			}},
			InitContainerStatuses: []corev1.ContainerStatus{
				{Name: "setup", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "Completed", ExitCode: 0, Message: "setup wrote 4 files"}}},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: false, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-20 * time.Minute))}}},
			},
		},
	}

	if got := PodProblemReason(pod, now); got != reasonReadinessProbeFail {
		t.Fatalf("PodProblemReason = %q, want %q", got, reasonReadinessProbeFail)
	}
	if got := PodProblemMessage(pod); got != "" {
		t.Errorf("PodProblemMessage = %q — borrowed from a container that merely finished", got)
	}
}

// "Completed" is in neither override predicate set, so returning it from a
// still-running pod silences the crashloop, OOM and thrash normalizations that
// exist to name exactly this state. A running pod is described by its phase.
func TestPodProblemReasonDoesNotReportCompletedOnARunningPod(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodRunning,
		InitContainerStatuses: []corev1.ContainerStatus{
			{Name: "setup", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0}}},
		},
		ContainerStatuses: []corev1.ContainerStatus{
			{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-time.Minute))}}},
		},
	}}

	if got := PodProblemReason(pod, now); got != "Running" {
		t.Errorf("PodProblemReason = %q, want %q", got, "Running")
	}
}

// The message must come from the container the reason came from, even when that
// container has no message of its own. Borrowing a later container's message
// pairs a failure with an unrelated explanation.
func TestPodProblemMessageDoesNotBorrowFromAnotherContainer(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodFailed,
		InitContainerStatuses: []corev1.ContainerStatus{
			{Name: "setup", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "Completed", ExitCode: 0, Message: "setup wrote 4 files"}}},
		},
		ContainerStatuses: []corev1.ContainerStatus{
			{Name: "app", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "Error", ExitCode: 1}}},
			{Name: "sidecar", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "Completed", ExitCode: 0, Message: "sidecar shut down cleanly"}}},
		},
	}}

	if got := PodProblemReason(pod, now); got != "Error" {
		t.Errorf("PodProblemReason = %q, want %q", got, "Error")
	}
	if got := PodProblemMessage(pod); got != "" {
		t.Errorf("PodProblemMessage = %q — the failing container has no message, so this was borrowed from another container", got)
	}
}

// An evicted pod has no failing container: the kubelet stops its containers
// cleanly on the way out, so every one of them terminates with exit 0. Reading
// the reason off a container reported the pod as "Completed" while its verdict
// said unhealthy, and dropped the pod-level message that says WHY it was
// evicted — the only actionable text there is.
func TestPodProblemReasonUsesPodLevelReasonOnAFailedPod(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase:   corev1.PodFailed,
		Reason:  "Evicted",
		Message: "The node was low on resource: ephemeral-storage.",
		ContainerStatuses: []corev1.ContainerStatus{
			{Name: "app", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0}}},
		},
	}}

	if got := PodProblemReason(pod, now); got != "Evicted" {
		t.Errorf("PodProblemReason = %q, want %q", got, "Evicted")
	}
	if got := PodProblemMessage(pod); got != "The node was low on resource: ephemeral-storage." {
		t.Errorf("PodProblemMessage = %q, want the pod-level eviction message", got)
	}

	// The verdict and the reason must agree: unhealthy must never read as a success.
	v := Pod(pod, now)
	if v.Level != LevelUnhealthy || v.Reason == "Completed" {
		t.Errorf("Pod() = %v/%q — an unhealthy pod reported a successful reason", v.Level, v.Reason)
	}
}

// A failed pod with no pod-level reason falls back to the phase, not to a
// container that exited cleanly.
func TestPodProblemReasonFailedPodWithoutPodLevelReason(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodFailed,
		ContainerStatuses: []corev1.ContainerStatus{
			{Name: "app", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0}}},
		},
	}}
	if got := PodProblemReason(pod, now); got != "Failed" {
		t.Errorf("PodProblemReason = %q, want %q", got, "Failed")
	}
}

// A genuinely failed container still wins over the pod-level reason: it is the
// more specific explanation.
func TestPodProblemReasonPrefersAFailedContainerOverThePodReason(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase:  corev1.PodFailed,
		Reason: "Evicted",
		ContainerStatuses: []corev1.ContainerStatus{
			{Name: "app", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "OOMKilled", ExitCode: 137, Message: "container was OOM killed"}}},
		},
	}}
	if got := PodProblemReason(pod, now); got != "OOMKilled" {
		t.Errorf("PodProblemReason = %q, want %q", got, "OOMKilled")
	}
	if got := PodProblemMessage(pod); got != "container was OOM killed" {
		t.Errorf("PodProblemMessage = %q, want the container's message", got)
	}
}

// TestPodSchedulingGated covers pods deliberately held by a scheduling gate: they
// must read Neutral (not an age-based Pending problem) and carry an explanation,
// while a real container fault on a gated pod still wins, and a released pod ages
// normally again.
func TestPodSchedulingGated(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	old := metav1.NewTime(now.Add(-30 * time.Minute))

	gatedPod := func(gates ...string) *corev1.Pod {
		sg := make([]corev1.PodSchedulingGate, 0, len(gates))
		for _, g := range gates {
			sg = append(sg, corev1.PodSchedulingGate{Name: g})
		}
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{CreationTimestamp: old},
			Spec:       corev1.PodSpec{SchedulingGates: sg},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		}
	}

	t.Run("gated pending past threshold is neutral and explained", func(t *testing.T) {
		v := Pod(gatedPod("kueue.x-k8s.io/admission"), now)
		if v.Level != LevelNeutral {
			t.Fatalf("level = %v, want neutral", v.Level)
		}
		if v.Reason != corev1.PodReasonSchedulingGated {
			t.Errorf("reason = %q, want %q", v.Reason, corev1.PodReasonSchedulingGated)
		}
		if !strings.Contains(v.Message, "kueue.x-k8s.io/admission") {
			t.Errorf("message = %q, want it to name the gate", v.Message)
		}
	})

	t.Run("gated with no PodScheduled condition is still neutral", func(t *testing.T) {
		// The spec field is authoritative before the scheduler writes any condition.
		if got := Pod(gatedPod("custom-gate"), now).Level; got != LevelNeutral {
			t.Errorf("level = %v, want neutral", got)
		}
	})

	t.Run("one of several gates removed is still gated", func(t *testing.T) {
		if got := Pod(gatedPod("gate-b"), now).Level; got != LevelNeutral {
			t.Errorf("level = %v, want neutral (pod stays held until all gates clear)", got)
		}
	})

	t.Run("gate does not mask a real container fault", func(t *testing.T) {
		pod := gatedPod("kueue.x-k8s.io/admission")
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: "app",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
				Reason:  "ImagePullBackOff",
				Message: "Back-off pulling image \"nope:latest\"",
			}},
		}}
		if got := Pod(pod, now).Level; got != LevelUnhealthy {
			t.Errorf("level = %v, want unhealthy (container fault must still surface)", got)
		}
	})

	t.Run("all gates removed ages normally", func(t *testing.T) {
		released := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{CreationTimestamp: old},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		}
		if got := Pod(released, now).Level; got != LevelDegraded {
			t.Errorf("level = %v, want degraded (released pod past 5m ages normally)", got)
		}
	})
}
