package k8s

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// Restart-loop issue continuity. A container stuck restarting cycles through
// states that each look like a different problem on a single poll: Waiting
// (CrashLoopBackOff), Running but not ready (readiness), Unhealthy events
// (liveness), Terminated with exit 0, and Running+Ready between crashes. Read
// tick by tick, the pod row changed category (and therefore issue id) on every
// cycle and dropped out entirely on the healthy-looking ticks, so downstream
// alerting saw thousands of open/resolve generations for one ongoing problem.
//
// This classifier is issue-layer only. It decides that one crashloop row stays
// present, with stable severity, for the whole loop; it does not change the
// pod's health level (pkg/health), which still reports what the pod is doing
// right now.
const (
	// restartLoopWindow is how long after its last termination a restarting
	// container is still treated as looping. It is the hysteresis: the issue
	// clears this long after the last crash. Long enough to bridge kubelet
	// backoff (max 5m) and loops whose Running phase lasts many minutes before
	// the next liveness kill.
	restartLoopWindow = 30 * time.Minute
	// restartLoopStaleGap separates one continuous loop from an old termination
	// followed by a fresh start, e.g. a node that was down for a while. Twice
	// the kubelet's maximum backoff, matching pkg/health's stale-crash guard.
	restartLoopStaleGap = 10 * time.Minute
	// restartLoopMinRestarts is the restart count from which a recent
	// termination reads as a loop rather than a one-off crash. RestartCount is
	// cumulative over the container's life, so this approximates a rate: a
	// container that restarted three times last month and once a minute ago
	// also qualifies, for at most restartLoopWindow.
	restartLoopMinRestarts = 3
	// restartLoopProbeMessageMax caps the probe output copied into evidence;
	// exec probes can print arbitrary amounts.
	restartLoopProbeMessageMax = 300
)

// restartLoop is the evidence behind an active restart loop, taken from one
// container so every field describes the same termination.
type restartLoop struct {
	container      string
	sidecar        bool
	restartCount   int32
	lastExitCode   int32
	lastReason     string
	lastFinishedAt time.Time
	liveness       *probeFailure
	readiness      *probeFailure
}

// activeRestartLoop reports the container keeping this pod in a restart loop.
//
// Eligible containers are those the kubelet restarts by design: regular
// containers of a restartPolicy=Always pod and native sidecars (init
// containers with restartPolicy=Always). Ordinary init containers retrying
// until they succeed, and OnFailure/Never pods such as Job workers, are
// excluded; their retries are handled by the init and Job paths.
//
// The exit code is deliberately ignored. The kubelet backs off restarts after
// any exit, and a liveness kill with a graceful shutdown ends Completed/0, so
// requiring a non-zero exit misses the loops that flap the most. An OOMKilled
// last termination is left to the OOM path, which has its own category.
//
// When several containers loop, the first in status order wins (regular
// containers before sidecars) so the evidence does not hop between containers
// from one poll to the next.
func activeRestartLoop(pod *corev1.Pod, probes map[string]probeFailure, now time.Time) (restartLoop, bool) {
	// A pod being deleted stops its containers on purpose: a rollout or
	// drain SIGTERM ends Completed/0 and is not a crash.
	if pod == nil || pod.DeletionTimestamp != nil || (pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending) {
		return restartLoop{}, false
	}
	if pod.Spec.RestartPolicy == "" || pod.Spec.RestartPolicy == corev1.RestartPolicyAlways {
		for i := range pod.Status.ContainerStatuses {
			if loop, ok := containerRestartLoop(&pod.Status.ContainerStatuses[i], now); ok {
				return withProbeEvidence(loop, pod, probes), true
			}
		}
	}
	for i := range pod.Status.InitContainerStatuses {
		cs := &pod.Status.InitContainerStatuses[i]
		if !isNativeSidecarName(pod, cs.Name) {
			continue
		}
		if loop, ok := containerRestartLoop(cs, now); ok {
			loop.sidecar = true
			return withProbeEvidence(loop, pod, probes), true
		}
	}
	return restartLoop{}, false
}

func isNativeSidecarName(pod *corev1.Pod, name string) bool {
	for _, c := range pod.Spec.InitContainers {
		if c.Name == name {
			return isRestartableInitContainer(c)
		}
	}
	return false
}

func containerRestartLoop(cs *corev1.ContainerStatus, now time.Time) (restartLoop, bool) {
	if cs.RestartCount < restartLoopMinRestarts {
		return restartLoop{}, false
	}
	// The current termination is the newest one when present; the kubelet
	// only moves it to LastTerminationState once the next run starts.
	term := cs.State.Terminated
	if term == nil {
		term = cs.LastTerminationState.Terminated
	}
	if term == nil || term.FinishedAt.IsZero() || term.Reason == "OOMKilled" {
		return restartLoop{}, false
	}
	finished := term.FinishedAt.Time
	if now.Sub(finished) > restartLoopWindow {
		return restartLoop{}, false
	}
	// A run that lasted longer than the window was not part of a loop: a
	// container that served for days and restarted once (a node or kubelet
	// bounce) keeps its old restarts in RestartCount, and would otherwise
	// read as looping on its first restart. A real loop is caught from its
	// next, short run. A container that never started (StartError,
	// ContainerCannotRun) is stamped with the Unix epoch, which is no start.
	if started := term.StartedAt.Time; started.Unix() > 0 && finished.Sub(started) > restartLoopWindow {
		return restartLoop{}, false
	}
	if r := cs.State.Running; r != nil && !r.StartedAt.IsZero() && r.StartedAt.Sub(finished) > restartLoopStaleGap {
		return restartLoop{}, false
	}
	return restartLoop{
		container:      cs.Name,
		restartCount:   cs.RestartCount,
		lastExitCode:   term.ExitCode,
		lastReason:     term.Reason,
		lastFinishedAt: finished,
	}, true
}

func withProbeEvidence(loop restartLoop, pod *corev1.Pod, probes map[string]probeFailure) restartLoop {
	if pf, ok := probeFailureFor(probes, pod, loop.container, livenessProbeFailedReason); ok {
		loop.liveness = &pf
	}
	if pf, ok := probeFailureFor(probes, pod, loop.container, readinessProbeFailedReason); ok {
		loop.readiness = &pf
	}
	return loop
}

// probeFailureFor finds the newest probe failure of one type for a container.
// The kubelet names the container in the event's fieldPath; an event without
// one is attributed only when the pod has a single container, so it can never
// pin another container's probe on the looping one.
func probeFailureFor(probes map[string]probeFailure, pod *corev1.Pod, container, reason string) (probeFailure, bool) {
	prefix := pod.Namespace + "/" + pod.Name + "/"
	pf, ok := probes[prefix+container+"/"+reason]
	if !ok && len(pod.Spec.Containers)+len(pod.Spec.InitContainers) == 1 {
		pf, ok = probes[prefix+"/"+reason]
	}
	// Events outlive a pod; a recreated pod with the same name (a
	// StatefulSet replica) must not inherit its predecessor's failures.
	if ok && pf.podUID != "" && pod.UID != "" && pf.podUID != pod.UID {
		return probeFailure{}, false
	}
	return pf, ok
}

// restartLoopMayReplace reports whether the loop's crashloop reason may stand
// in for the reason the pod walk produced. Phase strings, crash-class reasons,
// and probe/thrash reasons are exactly the per-tick faces of the loop. Anything
// more specific (image pulls, container-create errors, OOM, init stalls) names a
// different problem and keeps its own row.
func restartLoopMayReplace(reason string) bool {
	switch reason {
	case "Running", "Pending", "Unknown", "", "PodInitializing", "ContainerCreating",
		crashLoopReason, "Error", "Completed", "StartError", "ContainerCannotRun",
		highRestartReason, livenessProbeFailedReason, readinessProbeFailedReason:
		return true
	}
	return false
}

func (l restartLoop) ref() string {
	if l.sidecar {
		return fmt.Sprintf("sidecar container %q", l.container)
	}
	return fmt.Sprintf("container %q", l.container)
}

func (l restartLoop) lastExit() string {
	if l.lastReason != "" {
		return fmt.Sprintf("exit code %d (%s)", l.lastExitCode, l.lastReason)
	}
	return fmt.Sprintf("exit code %d", l.lastExitCode)
}

// message is the row's one-line summary. It names the probe failures seen
// alongside the restarts without claiming they caused them.
func (l restartLoop) message() string {
	msg := fmt.Sprintf("%s is in a restart loop: %d restarts, last %s", l.ref(), l.restartCount, l.lastExit())
	var seen []string
	if l.liveness != nil {
		seen = append(seen, "liveness")
	}
	if l.readiness != nil {
		seen = append(seen, "readiness")
	}
	if len(seen) > 0 {
		msg += "; " + strings.Join(seen, " and ") + " probe failures observed"
	}
	return msg
}

// diagnosis returns the loop's cause and next step. The text depends only on
// the container and its last exit code — not on serving state, restart counts,
// or whether a probe event happens to be inside its window — so it holds
// between polls of one loop and agrees across replicas, which a grouped
// workload row requires before it carries a diagnosis (agreedDiagnosis). Probe
// observations live in the evidence and the message instead.
func (l restartLoop) diagnosis() (cause, action string) {
	ref := l.ref()
	switch code := l.lastExitCode; code {
	case 0:
		return fmt.Sprintf("%s keeps restarting, and its last run ended with exit code 0. Under restartPolicy Always the kubelet restarts a container whenever it exits; a failed liveness probe also ends this way, because the kubelet lets the process shut down gracefully.", ref),
			"Check the pod's Unhealthy events for liveness probe failures; if present, check the probe against the app (path and port, timeoutSeconds, failureThreshold, startup time or a missing startupProbe). If not, check why the main process exits: command and args, a process that daemonizes, or one-shot work that belongs in a Job."
	case 127:
		return fmt.Sprintf("%s keeps restarting after exit code 127: command not found.", ref),
			"Check the image entrypoint and pod command/args; verify the binary exists in the image."
	case 126:
		return fmt.Sprintf("%s keeps restarting after exit code 126: command found but not executable.", ref),
			"Check executable permissions, the shebang/interpreter, and the pod command/args."
	case 139:
		return fmt.Sprintf("%s keeps restarting after exit code 139: segmentation fault.", ref),
			"Inspect previous container logs and recent image/code changes; check native libraries or unsafe code for a segfault."
	case 137:
		return fmt.Sprintf("%s keeps restarting after exit code 137 (SIGKILL), but Kubernetes did not report OOMKilled.", ref),
			"Check for a liveness probe kill that outlived its grace period, node pressure, process-level SIGKILLs, and memory limits; inspect previous container logs for shutdown context."
	default:
		return fmt.Sprintf("%s keeps restarting after exit code %d.", ref, code),
			"Inspect previous container logs for this container and verify the pod command, args, config, and dependencies."
	}
}

func (l restartLoop) evidence() *issuesapi.RestartLoop {
	out := &issuesapi.RestartLoop{
		Container:      l.container,
		Sidecar:        l.sidecar,
		RestartCount:   l.restartCount,
		LastExitCode:   l.lastExitCode,
		LastReason:     l.lastReason,
		LastFinishedAt: l.lastFinishedAt.UTC(),
	}
	if l.liveness != nil {
		out.LivenessProbeFailure = &issuesapi.ProbeFailure{LastSeen: l.liveness.at.UTC(), Message: Truncate(l.liveness.message, restartLoopProbeMessageMax)}
	}
	if l.readiness != nil {
		out.ReadinessProbeFailure = &issuesapi.ProbeFailure{LastSeen: l.readiness.at.UTC(), Message: Truncate(l.readiness.message, restartLoopProbeMessageMax)}
	}
	return out
}
