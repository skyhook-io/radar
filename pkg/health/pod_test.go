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
