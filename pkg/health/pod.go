package health

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Pod classifies a pod's health into a canonical Verdict. This is the single
// source of truth that ClassifyPodHealth (legacy string), the timeline, the
// dashboards, and MCP all reduce to.
//
// Precedence (broken-state checks first, benign last — a benign branch must
// never be ordered ahead of a failure check):
//
//	Succeeded            → neutral  (completed; lifecycle, not a problem)
//	Failed               → unhealthy
//	stable crashloop     → unhealthy when down, degraded when Running+Ready
//	fatal waiting reason → unhealthy
//	active OOMKilled     → unhealthy
//	Pending > 5m         → degraded ; Pending < 5m → healthy (startup grace)
//	readiness probe fail → degraded
//	active thrash        → degraded
//	default              → healthy
//
// Unschedulable and stuck-termination are NOT folded in here: they are detected
// by callers that own their own pre-emption (the scheduling detector, the
// terminating detector) and folding them in would change health-counter outputs.
// They move into this classifier in a later migration where those changes are
// owned explicitly.
func Pod(pod *corev1.Pod, now time.Time) Verdict {
	level := classifyPodLevel(pod, now)
	v := Verdict{Level: level}
	switch level {
	case LevelDegraded, LevelUnhealthy:
		v.Reason = PodProblemReason(pod, now)
		v.Message = PodProblemMessage(pod)
	case LevelNeutral:
		if pod.Status.Phase == corev1.PodSucceeded {
			v.Reason = "Completed"
		}
	}
	return v
}

// PodDisplayLevel is the pod level for the DISPLAY surfaces — topology node
// color, timeline events, and the AI/MCP summary. On top of the canonical Pod()
// verdict it folds the scheduling + stuck-terminating signals Pod() deliberately
// leaves to its caller, as a FLOOR: escalate to at least degraded so a
// scheduler-failed or wedged pod doesn't read healthy, but never downgrade a real
// unhealthy (a crashlooping pod mid-deletion stays red).
//
// The dashboard / MCP health COUNTERS deliberately do NOT use this — they bucket
// Pod().Level directly and handle scheduling through their own rollup.
func PodDisplayLevel(pod *corev1.Pod, now time.Time) Level {
	base := Pod(pod, now).Level
	if IsPodUnschedulable(pod) || IsStuckTerminating(pod, now) {
		return WorseOf(base, LevelDegraded)
	}
	return base
}

func classifyPodLevel(pod *corev1.Pod, now time.Time) Level {
	if pod.Status.Phase == corev1.PodSucceeded {
		return LevelNeutral
	}
	if pod.Status.Phase == corev1.PodFailed {
		return LevelUnhealthy
	}
	// Phase Unknown means the node is unreachable and the kubelet has stopped
	// reporting — the container states are stale and untrustworthy, so this is
	// genuinely unknown, not healthy. (Without this, the default fall-through to
	// healthy paints a node-lost pod green.)
	if pod.Status.Phase == corev1.PodUnknown {
		return LevelUnknown
	}

	// Crash history keeps the problem present across the kubelet's
	// Waiting→Running oscillation, while current state controls urgency.
	// Unhealthy may return immediately; degraded must not mask an unrelated
	// fatal state on another container.
	crashLoopLevel := podStableCrashLoopLevel(pod, now)
	if crashLoopLevel == LevelUnhealthy {
		return LevelUnhealthy
	}

	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && isFatalWaitingReason(cs.State.Waiting.Reason) {
			return LevelUnhealthy
		}
	}
	if podHasActiveOOMKilled(pod, now) {
		return LevelUnhealthy
	}

	// Init container errors.
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil && isFatalWaitingReason(cs.State.Waiting.Reason) {
			return LevelUnhealthy
		}
	}
	if crashLoopLevel == LevelDegraded {
		return LevelDegraded
	}

	// Pods pending past the startup grace window.
	if pod.Status.Phase == corev1.PodPending {
		if now.Sub(pod.CreationTimestamp.Time) > 5*time.Minute {
			return LevelDegraded
		}
		return LevelHealthy
	}

	if podHasReadinessProbeFailure(pod, now) {
		return LevelDegraded
	}

	// A container actively thrashing — high cumulative restarts AND currently
	// not ready AND still churning. A plain RestartCount>N check also fires on a
	// pod that crashed at startup and has since been Ready for hours
	// (RestartCount never resets), and on nodes whose restarts are stale
	// laptop-sleep / reboot artifacts — both are healthy now. The thrash gate
	// (not-ready + recent/Waiting) excludes those.
	if podActiveThrashContainer(pod, now) {
		return LevelDegraded
	}

	return LevelHealthy
}

// PodProblemReason returns a short reason token for a problematic pod. Walks
// init containers first because when init is failing the pod stays Pending and
// main ContainerStatuses haven't been populated yet — without the init check the
// reason would fall through to "Pending", masking CrashLoopBackOff /
// ImagePullBackOff / etc. on the actual failing init container.
//
// now is injected (callers pass the same clock they classify with) so the reason
// is reproducible and the crashloop/thrash overrides use a single consistent
// time base.
func PodProblemReason(pod *corev1.Pod, now time.Time) string {
	reason := podProblemReasonRaw(pod, now)
	// Stable-crashloop normalization: a crashlooping container oscillates
	// Waiting("CrashLoopBackOff") → Running (just restarted) → Terminated →
	// Waiting between polls. On the "Running" tick the raw walk returns a bare
	// phase ("Running") — which category classification maps to `unknown`,
	// flipping the category (and the category-hashed issue_id) mid-cycle. When
	// the stable history fields say this is a crashloop, emit the canonical
	// reason so the row's category stays stable across the whole oscillation. We
	// only override when the raw reason isn't already a more-specific, stable
	// signal (ImagePullBackOff, OOMKilled, an init failure, …) — those win.
	if podHasStableCrashLoop(pod, now) && isPhaseOrCrashReason(reason) {
		return reasonCrashLoop
	}
	if podHasActiveOOMKilled(pod, now) && isPhaseOnlyReason(reason) {
		return "OOMKilled"
	}
	// Actively-thrashing-but-not-a-classic-backoff: a container churning on
	// failed readiness probes with clean (exit 0) terminations isn't a stable
	// crashloop, so the raw walk returns a bare phase ("Running") that would
	// classify as `unknown`. Name it HighRestartCount so the row lands in a
	// runtime category instead of the catch-all. Only override a bare phase — a
	// specific reason (ImagePullBackOff, an init failure, a real crash) wins.
	if podActiveThrashContainer(pod, now) && isPhaseOnlyReason(reason) {
		return reasonHighRestart
	}
	return reason
}

// podProblemReasonRaw is the phase/state walk: init containers first (they block
// the pod Pending before main ContainerStatuses populate), then main containers,
// falling back to the bare phase string.
func podProblemReasonRaw(pod *corev1.Pod, now time.Time) string {
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
	}
	if podHasReadinessProbeFailure(pod, now) {
		return reasonReadinessProbeFail
	}
	return string(pod.Status.Phase)
}

// PodProblemMessage returns the kubelet's waiting/terminated message for the
// first container in a problem state (init containers first, mirroring
// podProblemReasonRaw's walk). This is the actionable detail behind an otherwise
// bare reason — ImagePullBackOff's "Failed to pull image X: …not found",
// CreateContainerConfigError's "couldn't find key Y in Secret Z".
func PodProblemMessage(pod *corev1.Pod) string {
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Message != "" {
			return cs.State.Waiting.Message
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Message != "" {
			return cs.State.Terminated.Message
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Message != "" {
			return cs.State.Waiting.Message
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Message != "" {
			return cs.State.Terminated.Message
		}
	}
	return ""
}

// PodRestartContext extracts crash-debugging context: total restarts across main
// + init containers, and the kubelet-recorded reason for the most recent
// container termination (OOMKilled, Error, Completed, …). Lets callers tell
// chronic-vs-acute and pick the right next step (OOMKilled → memory analysis;
// Error → previous logs).
func PodRestartContext(pod *corev1.Pod) (restartCount int32, lastTerminatedReason string) {
	var newestFinish time.Time
	walk := func(statuses []corev1.ContainerStatus) {
		for _, cs := range statuses {
			restartCount += cs.RestartCount
			if t := cs.LastTerminationState.Terminated; t != nil && !t.FinishedAt.IsZero() {
				if newestFinish.IsZero() || t.FinishedAt.After(newestFinish) {
					newestFinish = t.FinishedAt.Time
					lastTerminatedReason = t.Reason
				}
			}
		}
	}
	walk(pod.Status.ContainerStatuses)
	walk(pod.Status.InitContainerStatuses)
	return restartCount, lastTerminatedReason
}

type crashLoopCandidate struct {
	status     corev1.ContainerStatus
	init       bool
	index      int
	stateRank  int
	finishedAt time.Time
}

// PodCrashLoopDiagnosis returns a human-readable cause + suggested action for a
// pod's active crashloop candidate, or empty strings when there is no actionable
// crash (no candidate, OOM — which has its own path — or a clean exit).
func PodCrashLoopDiagnosis(pod *corev1.Pod, now time.Time) (cause, action string) {
	candidate, ok := activeCrashLoopCandidate(pod, now)
	if !ok {
		return "", ""
	}
	term := candidate.status.LastTerminationState.Terminated
	if term == nil || term.ExitCode == 0 || term.Reason == "OOMKilled" {
		return "", ""
	}

	ref := fmt.Sprintf("container %q", candidate.status.Name)
	if candidate.init {
		ref = fmt.Sprintf("init container %q", candidate.status.Name)
	}
	run := shortRunContext(term)
	if IsCrashLoopContainerServing(pod, candidate.status) {
		exit := fmt.Sprintf("exit code %d", term.ExitCode)
		action := "Watch for another restart. If it repeats, inspect previous container logs and verify the pod command, args, config, and dependencies."
		switch term.ExitCode {
		case 127:
			exit = "exit code 127 (command not found)"
			action = "Watch for another restart. If it repeats, check the image entrypoint and pod command/args; verify the binary exists in the image."
		case 126:
			exit = "exit code 126 (command found but not executable)"
			action = "Watch for another restart. If it repeats, check executable permissions, the shebang/interpreter, and the pod command/args."
		case 139:
			exit = "exit code 139 (segmentation fault)"
			action = "Watch for another restart. If it repeats, inspect previous container logs and recent image/code changes; check native libraries or unsafe code."
		case 137:
			exit = "exit code 137 (Kubernetes did not report OOMKilled)"
			action = "Watch for another restart. If it repeats, check node pressure, process-level SIGKILLs, and memory limits."
		}
		return fmt.Sprintf("%s restarted after %s and is serving again within the 5-minute settle window.%s", ref, exit, run), action
	}

	switch term.ExitCode {
	case 127:
		return fmt.Sprintf("%s exited with code 127: command not found.%s", ref, run),
			"Check the image entrypoint and pod command/args; verify the binary exists in the image."
	case 126:
		return fmt.Sprintf("%s exited with code 126: command found but not executable.%s", ref, run),
			"Check executable permissions, the shebang/interpreter, and the pod command/args."
	case 139:
		return fmt.Sprintf("%s exited with code 139: segmentation fault.%s", ref, run),
			"Inspect previous container logs and recent image/code changes; check native libraries or unsafe code for a segfault."
	case 137:
		return fmt.Sprintf("%s exited with code 137, but Kubernetes did not report OOMKilled.%s", ref, run),
			"Check node pressure, process-level SIGKILLs, and memory limits; inspect previous container logs for shutdown context."
	default:
		return fmt.Sprintf("%s is crashlooping after exit code %d.%s", ref, term.ExitCode, run),
			"Inspect previous container logs for this container and verify the pod command, args, config, and dependencies."
	}
}

func activeCrashLoopCandidate(pod *corev1.Pod, now time.Time) (crashLoopCandidate, bool) {
	var best crashLoopCandidate
	have := false
	consider := func(cs corev1.ContainerStatus, init bool, index int) {
		if !isStableCrashLoop(&cs, now) {
			return
		}
		c := crashLoopCandidate{
			status:    cs,
			init:      init,
			index:     index,
			stateRank: crashLoopStateRank(cs),
		}
		if t := cs.LastTerminationState.Terminated; t != nil {
			c.finishedAt = t.FinishedAt.Time
		}
		if !have || betterCrashLoopCandidate(c, best) {
			best, have = c, true
		}
	}
	for i := range pod.Status.InitContainerStatuses {
		cs := pod.Status.InitContainerStatuses[i]
		consider(cs, true, i)
	}
	for i := range pod.Status.ContainerStatuses {
		cs := pod.Status.ContainerStatuses[i]
		consider(cs, false, i)
	}
	return best, have
}

func crashLoopStateRank(cs corev1.ContainerStatus) int {
	if cs.State.Waiting != nil {
		return 0
	}
	if cs.State.Terminated != nil {
		return 1
	}
	if cs.State.Running != nil {
		return 2
	}
	return 3
}

func betterCrashLoopCandidate(cand, cur crashLoopCandidate) bool {
	if cand.stateRank != cur.stateRank {
		return cand.stateRank < cur.stateRank
	}
	if !cand.finishedAt.Equal(cur.finishedAt) {
		return cand.finishedAt.After(cur.finishedAt)
	}
	if cand.init != cur.init {
		return cand.init
	}
	if cand.status.Name != cur.status.Name {
		return cand.status.Name < cur.status.Name
	}
	return cand.index < cur.index
}

func shortRunContext(term *corev1.ContainerStateTerminated) string {
	if term == nil || term.StartedAt.IsZero() || term.FinishedAt.IsZero() {
		return ""
	}
	runDuration := term.FinishedAt.Time.Sub(term.StartedAt.Time)
	if runDuration <= 0 || runDuration >= shortCrashRunWindow {
		return ""
	}
	return " It exited within seconds of starting."
}
