package health

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPodDisplayLevel(t *testing.T) {
	now := time.Now()
	unsched := &corev1.Pod{Status: corev1.PodStatus{
		Phase:      corev1.PodPending,
		Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable}},
	}}
	if got := PodDisplayLevel(unsched, now); got != LevelDegraded {
		t.Errorf("unschedulable pod display level = %q, want degraded", got)
	}
	// Floor, not ceiling: a stuck-terminating pod that is also crashlooping stays unhealthy.
	stuckCrash := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{Time: now.Add(-11 * time.Minute)}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
			},
		},
	}
	if got := PodDisplayLevel(stuckCrash, now); got != LevelUnhealthy {
		t.Errorf("stuck-terminating + crashloop display level = %q, want unhealthy (floor must not downgrade)", got)
	}
	healthy := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}}
	if got := PodDisplayLevel(healthy, now); got != LevelHealthy {
		t.Errorf("healthy pod display level = %q, want healthy", got)
	}
}

func TestIsStuckTerminating(t *testing.T) {
	now := time.Now()
	notTerminating := &corev1.Pod{}
	graceful := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{Time: now.Add(-2 * time.Minute)}}}
	stuck := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{Time: now.Add(-11 * time.Minute)}}}
	if IsStuckTerminating(notTerminating, now) {
		t.Error("a pod with no deletionTimestamp is not stuck-terminating")
	}
	if IsStuckTerminating(graceful, now) {
		t.Error("a pod terminating 2m is graceful, not stuck")
	}
	if !IsStuckTerminating(stuck, now) {
		t.Error("a pod terminating 11m is stuck")
	}
}

// legacy is a small helper: these recovery-guard cases were originally written
// against the three-value ClassifyPodHealth ("healthy"/"warning"/"error"); they
// now assert the same via Pod().LegacyString() so the guards stay pinned.
func legacy(pod *corev1.Pod, now time.Time) string { return Pod(pod, now).LegacyString() }

// TestRecoveredAfterCrashIsHealthy: a container that crashed earlier but has run
// continuously past the 5m backoff window has recovered — its stale history must
// not keep it flagged as a crashloop.
func TestRecoveredAfterCrashIsHealthy(t *testing.T) {
	now := time.Now()
	crash := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}

	recovered := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{{
			Ready: true, RestartCount: 2,
			State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-30 * time.Minute))}},
			LastTerminationState: crash,
		}},
	}}
	if got := legacy(recovered, now); got != "healthy" {
		t.Errorf("recovered-after-crash (Running 30m) = %q, want healthy", got)
	}

	looping := recovered.DeepCopy()
	looping.Status.ContainerStatuses[0].State.Running.StartedAt = metav1.NewTime(now.Add(-30 * time.Second))
	if got := legacy(looping, now); got != "error" {
		t.Errorf("just-restarted crashloop (Running 30s) = %q, want error", got)
	}

	completedInit := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodRunning,
		InitContainerStatuses: []corev1.ContainerStatus{{
			RestartCount:         1,
			State:                corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0}},
			LastTerminationState: crash,
		}},
		ContainerStatuses: []corev1.ContainerStatus{{
			Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-10 * time.Minute))}},
		}},
	}}
	if got := legacy(completedInit, now); got != "healthy" {
		t.Errorf("retried-then-completed init + healthy main = %q, want healthy", got)
	}
}

func TestRecoveredAfterOOMIsHealthy(t *testing.T) {
	now := time.Now()
	oom := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(now.Add(-30 * time.Minute)),
	}}
	recovered := &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", ReadinessProbe: &corev1.Probe{}}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app", Ready: true, RestartCount: 1,
				State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-20 * time.Minute))}},
				LastTerminationState: oom,
			}},
		},
	}
	if got := legacy(recovered, now); got != "healthy" {
		t.Errorf("recovered-after-OOM = %q, want healthy", got)
	}
	active := recovered.DeepCopy()
	active.Status.ContainerStatuses[0].Ready = false
	active.Status.ContainerStatuses[0].State.Running.StartedAt = metav1.NewTime(now.Add(-30 * time.Second))
	if got := legacy(active, now); got != "error" {
		t.Errorf("recent OOM restart = %q, want error", got)
	}
	if got := PodProblemReason(active, now); got != "OOMKilled" {
		t.Errorf("recent OOM reason = %q, want OOMKilled", got)
	}
}

// TestProbeGatedReadyClearsCrashLoop: Ready clears a crashloop only when a
// readiness probe backs it.
func TestProbeGatedReadyClearsCrashLoop(t *testing.T) {
	now := time.Now()
	crash := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}
	probedRecovered := &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", ReadinessProbe: &corev1.Probe{}}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app", Ready: true, RestartCount: 2,
				State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-90 * time.Second))}},
				LastTerminationState: crash,
			}},
		},
	}
	if got := legacy(probedRecovered, now); got != "healthy" {
		t.Errorf("probed Ready pod recovered 90s = %q, want healthy", got)
	}
	probelessLooping := probedRecovered.DeepCopy()
	probelessLooping.Spec.Containers[0].ReadinessProbe = nil
	if got := legacy(probelessLooping, now); got != "error" {
		t.Errorf("probe-less Ready pod Running 90s = %q, want error (Ready untrusted)", got)
	}
}

// TestStableCrashLoopPreservesSpecificReasons: OOMKilled is not folded to
// CrashLoopBackOff.
func TestStableCrashLoopPreservesSpecificReasons(t *testing.T) {
	now := time.Now()
	oom := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{{
			RestartCount:         4,
			State:                corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
		}},
	}}
	if got := PodProblemReason(oom, now); got != "OOMKilled" {
		t.Errorf("OOMKilled reason = %q, want OOMKilled (must not fold to crashloop)", got)
	}
	if got := legacy(oom, now); got != "error" {
		t.Errorf("OOMKilled health = %q, want error", got)
	}
}

func TestActiveOOMKilledContainers(t *testing.T) {
	now := time.Now()
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "terminated"},
			{Name: "recent"},
			{Name: "recovered", ReadinessProbe: &corev1.Probe{}},
		}},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  "terminated",
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
				},
				{
					Name:                 "recent",
					State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
				},
				{
					Name:                 "recovered",
					Ready:                true,
					State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-30 * time.Minute))}},
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
				},
			},
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name:  "init",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
			}},
		},
	}

	got := ActiveOOMKilledContainers(pod, now)
	if len(got) != 2 || got[0].Name != "terminated" || got[1].Name != "recent" {
		t.Fatalf("active regular OOM containers = %+v, want terminated and recent only", got)
	}
}

// TestPodCrashLoopDiagnosisOrdersCandidates: the current waiting crashloop wins
// over a newer running tick.
func TestPodCrashLoopDiagnosisOrdersCandidates(t *testing.T) {
	now := time.Now()
	older := metav1.NewTime(now.Add(-30 * time.Second))
	newer := metav1.NewTime(now.Add(-20 * time.Second))
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{
			{
				Name: "running-newer", RestartCount: 2,
				State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-1 * time.Second))}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 139, FinishedAt: newer}},
			},
			{
				Name: "waiting-older", RestartCount: 2,
				State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 126, FinishedAt: older}},
			},
		},
	}}
	cause, _ := PodCrashLoopDiagnosis(pod, now)
	if !strings.Contains(cause, `container "waiting-older"`) || !strings.Contains(cause, "code 126") {
		t.Fatalf("cause = %q, want current waiting crashloop to win over newer running tick", cause)
	}
}

// TestPodCrashLoopDiagnosisShortRunStableAcrossReplicas: the short-run context is
// identical across replicas that crashed after slightly different sub-window
// durations (no per-replica duration leaks into the message).
func TestPodCrashLoopDiagnosisShortRunStableAcrossReplicas(t *testing.T) {
	now := time.Now()
	mkPod := func(runFor time.Duration) *corev1.Pod {
		finished := metav1.NewTime(now.Add(-1 * time.Second))
		started := metav1.NewTime(finished.Time.Add(-runFor))
		return &corev1.Pod{Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app", RestartCount: 2,
				State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 139, StartedAt: started, FinishedAt: finished}},
			}},
		}}
	}
	fastCause, _ := PodCrashLoopDiagnosis(mkPod(2*time.Second), now)
	slowerCause, _ := PodCrashLoopDiagnosis(mkPod(4*time.Second), now)
	if fastCause == "" || fastCause != slowerCause {
		t.Fatalf("short-run causes should be identical across replicas: %q vs %q", fastCause, slowerCause)
	}
	if strings.Contains(fastCause, "2s") || strings.Contains(slowerCause, "4s") {
		t.Fatalf("short-run cause must not include per-replica duration: %q / %q", fastCause, slowerCause)
	}
}
