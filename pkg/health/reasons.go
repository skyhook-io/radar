package health

import (
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Canonical reason tokens this package emits. They are deliberately the
// Kubernetes-canonical strings so downstream category classification stays
// stable across the kubelet's instantaneous state oscillation.
const (
	reasonCrashLoop          = "CrashLoopBackOff"
	reasonHighRestart        = "HighRestartCount"
	reasonReadinessProbeFail = "ReadinessProbeFailed"
)

// highRestartThreshold is the cumulative per-container RestartCount above which
// a still-unhealthy container is treated as actively thrashing.
const highRestartThreshold = 3

const shortCrashRunWindow = 10 * time.Second

// isStableCrashLoop reports whether a container is in an ACTIVE crashloop: it
// has restarted with a crash-class last termination (CrashLoopBackOff / generic
// Error / non-zero exit), AND it has not since recovered. It reads the stable
// history fields (RestartCount + LastTerminationState) rather than the
// instantaneous State the kubelet flips between polls — so a real loop's brief
// "Running" blip doesn't downgrade the verdict — but it must NOT fire on a
// container that crashed once and is now running healthily: RestartCount and
// LastTerminationState persist for the life of the container, so without the
// recovery guard below a pod that restarted once at startup would read as a
// crashloop forever. Three recovery signals clear it: a container currently
// Ready when that Ready is probe-gated (readyTrusted) has passed its readiness
// probe and is serving NOW; a container Running continuously past the kubelet's
// max CrashLoopBackOff backoff (5m) has outlived the loop; and a container whose
// CURRENT state is a clean exit (Terminated, exit 0) has succeeded — the common
// init-container-retries-then-completes case, whose failed prior attempt lingers
// in LastTerminationState. OOMKilled is intentionally excluded — it has its own
// category/severity path upstream.
//
// readyTrusted gates the Ready short-circuit because Ready is only a meaningful
// recovery signal when a readiness probe backs it: without a probe Ready just
// mirrors Running and flips true during a loop's brief between-crash window, so
// for probe-less containers the 5m Running guard below stays the discriminator.
func isStableCrashLoop(cs *corev1.ContainerStatus, now time.Time, readyTrusted bool) bool {
	if cs.RestartCount == 0 {
		return false
	}
	if readyTrusted && cs.Ready {
		return false
	}
	if r := cs.State.Running; r != nil && !r.StartedAt.IsZero() && now.Sub(r.StartedAt.Time) > 5*time.Minute {
		return false
	}
	if term := cs.State.Terminated; term != nil && term.ExitCode == 0 {
		return false
	}
	t := cs.LastTerminationState.Terminated
	if t == nil {
		return false
	}
	switch t.Reason {
	case "OOMKilled":
		// Memory pressure is classified separately (CategoryOOMKilled); don't
		// fold it into the generic crashloop bucket.
		return false
	case "CrashLoopBackOff", "Error":
		return true
	}
	// A non-zero exit code with no special reason is still a crash — the app
	// died and the kubelet is restarting it.
	return t.ExitCode != 0
}

// ActiveCrashLoopContainerStatuses returns main and init container statuses
// that are still in an active crashloop after applying Radar's recovery guards.
func ActiveCrashLoopContainerStatuses(pod *corev1.Pod, now time.Time) []corev1.ContainerStatus {
	if pod == nil {
		return nil
	}
	var active []corev1.ContainerStatus
	for i := range pod.Status.ContainerStatuses {
		cs := pod.Status.ContainerStatuses[i]
		if isStableCrashLoop(&cs, now, containerHasReadinessProbe(pod.Spec.Containers, cs.Name)) {
			active = append(active, cs)
		}
	}
	// Init containers carry no readiness probe and their Ready field is not a
	// serving signal — never trust Ready as recovery there; the Running-window
	// and clean-exit guards still apply.
	for i := range pod.Status.InitContainerStatuses {
		cs := pod.Status.InitContainerStatuses[i]
		if isStableCrashLoop(&cs, now, false) {
			active = append(active, cs)
		}
	}
	return active
}

// podHasStableCrashLoop reports whether any main or init container is in a
// stable crashloop (see isStableCrashLoop).
func podHasStableCrashLoop(pod *corev1.Pod, now time.Time) bool {
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		if isStableCrashLoop(cs, now, containerHasReadinessProbe(pod.Spec.Containers, cs.Name)) {
			return true
		}
	}
	for i := range pod.Status.InitContainerStatuses {
		if isStableCrashLoop(&pod.Status.InitContainerStatuses[i], now, false) {
			return true
		}
	}
	return false
}

// containerHasReadinessProbe reports whether the named container declares a
// readiness probe — the condition under which its ContainerStatus.Ready is a
// trustworthy "serving now" signal rather than a mirror of Running.
func containerHasReadinessProbe(containers []corev1.Container, name string) bool {
	for i := range containers {
		if containers[i].Name == name {
			return containers[i].ReadinessProbe != nil
		}
	}
	return false
}

// restartedRecently reports whether a container's most recent termination
// finished within the given window — i.e. it is still actively churning, not a
// container that crashed long ago and has since gone quiet (the laptop-sleep /
// node-reboot artifact where RestartCount is high but every termination is days
// old).
func restartedRecently(cs *corev1.ContainerStatus, now time.Time, within time.Duration) bool {
	if t := cs.LastTerminationState.Terminated; t != nil && !t.FinishedAt.IsZero() {
		return now.Sub(t.FinishedAt.Time) <= within
	}
	return false
}

// isActivelyThrashing reports whether a container has a high cumulative restart
// count AND is currently unhealthy AND is still churning. The Ready gate is what
// clears the recovered-after-crash false positive — a pod that restarted many
// times at startup but is now Ready and stable (its RestartCount never resets)
// no longer trips this. The Waiting/recency gate clears the slept-then-woken
// node whose restarts are days old. The 5m window matches isStableCrashLoop's
// horizon so the two guards don't drift.
func isActivelyThrashing(cs *corev1.ContainerStatus, now time.Time) bool {
	if cs.RestartCount <= highRestartThreshold || cs.Ready {
		return false
	}
	if cs.State.Waiting != nil {
		return true
	}
	return restartedRecently(cs, now, 5*time.Minute)
}

// podActiveThrashContainer reports whether any main container is actively
// thrashing (see isActivelyThrashing). Init containers are excluded — a failing
// init container surfaces through podProblemReasonRaw's init walk with a
// specific reason already.
func podActiveThrashContainer(pod *corev1.Pod, now time.Time) bool {
	for i := range pod.Status.ContainerStatuses {
		if isActivelyThrashing(&pod.Status.ContainerStatuses[i], now) {
			return true
		}
	}
	return false
}

func containerHasTrustedReady(containers []corev1.Container, cs corev1.ContainerStatus) bool {
	return cs.Ready && containerHasReadinessProbe(containers, cs.Name)
}

func containerRecentlyRecoveredFromOOM(containers []corev1.Container, cs corev1.ContainerStatus, now time.Time) bool {
	if cs.LastTerminationState.Terminated == nil || cs.LastTerminationState.Terminated.Reason != "OOMKilled" {
		return false
	}
	if containerHasTrustedReady(containers, cs) {
		return false
	}
	if r := cs.State.Running; r != nil && !r.StartedAt.IsZero() {
		return now.Sub(r.StartedAt.Time) <= 5*time.Minute
	}
	return cs.State.Waiting != nil || restartedRecently(&cs, now, 5*time.Minute)
}

// ActiveOOMKilledContainers returns the regular containers currently affected
// by an OOM kill. Init containers are excluded so callers can match the result
// to a workload template's regular containers without conflating the two lists.
func ActiveOOMKilledContainers(pod *corev1.Pod, now time.Time) []corev1.ContainerStatus {
	var active []corev1.ContainerStatus
	for i := range pod.Status.ContainerStatuses {
		cs := pod.Status.ContainerStatuses[i]
		if cs.State.Terminated != nil && cs.State.Terminated.Reason == "OOMKilled" {
			active = append(active, cs)
			continue
		}
		if containerRecentlyRecoveredFromOOM(pod.Spec.Containers, cs, now) {
			active = append(active, cs)
		}
	}
	return active
}

func podHasActiveOOMKilled(pod *corev1.Pod, now time.Time) bool {
	if len(ActiveOOMKilledContainers(pod, now)) > 0 {
		return true
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Terminated != nil && cs.State.Terminated.Reason == "OOMKilled" {
			return true
		}
		if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
			if cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0 {
				continue
			}
			if r := cs.State.Running; r != nil && !r.StartedAt.IsZero() && now.Sub(r.StartedAt.Time) > 5*time.Minute {
				continue
			}
			return true
		}
	}
	return false
}

// PodHasReadinessProbeFailure reports whether a running pod has been
// Ready=False past the grace window with a readiness-probed container still
// failing — exported for the problem detector, which shares this predicate.
func PodHasReadinessProbeFailure(pod *corev1.Pod, now time.Time) bool {
	return podHasReadinessProbeFailure(pod, now)
}

func podHasReadinessProbeFailure(pod *corev1.Pod, now time.Time) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	if !podReadyFalseLongEnough(pod, now, 5*time.Minute) {
		return false
	}
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		if cs.Ready || cs.State.Running == nil {
			continue
		}
		if containerHasReadinessProbe(pod.Spec.Containers, cs.Name) {
			return true
		}
	}
	return false
}

func podReadyFalseLongEnough(pod *corev1.Pod, now time.Time, minAge time.Duration) bool {
	for i := range pod.Status.Conditions {
		cond := &pod.Status.Conditions[i]
		if cond.Type != corev1.PodReady && cond.Type != corev1.ContainersReady {
			continue
		}
		if cond.Status != corev1.ConditionFalse {
			continue
		}
		if !cond.LastTransitionTime.IsZero() {
			return now.Sub(cond.LastTransitionTime.Time) >= minAge
		}
		if !pod.CreationTimestamp.IsZero() {
			return now.Sub(pod.CreationTimestamp.Time) >= minAge
		}
		return true
	}
	return false
}

// isFatalWaitingReason reports whether a container's Waiting reason is a hard
// failure that won't self-resolve on its own — as opposed to the transient
// PodInitializing/ContainerCreating states. InvalidImageName is permanent (a
// malformed image reference never becomes valid); the *ContainerError family
// means the container couldn't be created or started (bad command, missing
// device, runtime rejection).
func isFatalWaitingReason(reason string) bool {
	switch reason {
	case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "InvalidImageName",
		"ImageInspectError", "CreateContainerConfigError", "CreateContainerError",
		"RunContainerError":
		return true
	}
	return false
}

// isPhaseOrCrashReason reports whether reason is one that a stable-crashloop
// override may safely replace: a bare lifecycle phase / no-op waiting state
// (the instantaneous values that flap), or an already-crash-class reason
// (so the canonical string is used consistently). A distinct, more-specific
// reason like ImagePullBackOff or OOMKilled is NOT in this set and is left
// untouched.
func isPhaseOrCrashReason(reason string) bool {
	switch reason {
	case "Running", "Pending", "Succeeded", "Failed", "Unknown", "",
		"PodInitializing", "ContainerCreating",
		"CrashLoopBackOff", "Error":
		return true
	}
	return false
}

// isPhaseOnlyReason is the narrower set the HighRestartCount override may
// replace: bare lifecycle phases / no-op waiting states only. It deliberately
// excludes the crash-class reasons (CrashLoopBackOff/Error) and terminal phases
// (Succeeded/Failed) that isPhaseOrCrashReason allows, so a thrash override can
// never clobber a real crash or terminal signal — the stable-crashloop check
// above already owns those.
func isPhaseOnlyReason(reason string) bool {
	switch reason {
	case "Running", "Pending", "Unknown", "",
		"PodInitializing", "ContainerCreating":
		return true
	}
	return false
}

// StuckTerminatingThreshold is how long a pod may sit with a deletionTimestamp
// before its termination is treated as stuck (wedged on a finalizer / ungraceful
// node drain) rather than a normal graceful shutdown.
const StuckTerminatingThreshold = 10 * time.Minute

// IsStuckTerminating reports whether a pod has been terminating longer than
// StuckTerminatingThreshold. The canonical Pod classifier deliberately ignores
// deletionTimestamp (folding it in would shift the dashboard/MCP health counters);
// the display surfaces (timeline, topology) fold this signal in on top so a wedged
// pod doesn't read healthy.
func IsStuckTerminating(pod *corev1.Pod, now time.Time) bool {
	dt := pod.DeletionTimestamp
	return dt != nil && now.Sub(dt.Time) > StuckTerminatingThreshold
}

// IsPodUnschedulable reports whether the scheduler tried and failed to place the
// pod (PodScheduled=False with reason=Unschedulable). reason=SchedulingGated is
// an intentional not-yet-scheduled state, not a placement failure, and is
// excluded.
func IsPodUnschedulable(pod *corev1.Pod) bool {
	for i := range pod.Status.Conditions {
		c := &pod.Status.Conditions[i]
		if c.Type == corev1.PodScheduled {
			return c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable
		}
	}
	return false
}
