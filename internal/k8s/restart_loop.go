package k8s

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/pkg/health"
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
	// restartLoopFastRun is the run length below which a loop is "fast": the
	// container spends most of its time down or in backoff. It is the
	// kubelet's own line: a container that runs longer than 10 minutes has its
	// crash backoff reset.
	restartLoopFastRun = 10 * time.Minute
	// restartLoopImpactShare is the share of a workload's pods that must be
	// looping for a fast loop to be critical.
	restartLoopImpactShare = 0.5
	// restartLoopProbeMessageMax caps the probe output copied into evidence;
	// exec probes can print arbitrary amounts.
	restartLoopProbeMessageMax = 300
)

// restartLoop is the evidence behind an active restart loop, taken from one
// container so every field describes the same termination.
type restartLoop struct {
	container string
	sidecar   bool
	init      bool
	// status is the looping container's status, for diagnoses that need it.
	status corev1.ContainerStatus
	// memoryLimit reports whether the container declares a memory limit.
	memoryLimit    bool
	restartCount   int32
	lastExitCode   int32
	lastReason     string
	lastFinishedAt time.Time
	// lastRun is how long the terminated run lasted; zero when the container
	// never started (StartError) or the runtime did not record a start.
	lastRun time.Duration
	// lastMessage is the runtime's message for the last termination; for a
	// start failure it is the actual error (e.g. exec: no such file).
	lastMessage string
	liveness    *probeFailure
	readiness   *probeFailure
	startup     *probeFailure
}

// activeRestartLoop reports the container keeping this pod in a restart loop.
//
// Eligible containers are those the kubelet keeps restarting: regular
// containers whose effective restartPolicy is Always, native sidecars (init
// containers with restartPolicy=Always), and ordinary init containers that
// keep failing while the pod is still Pending (Init:CrashLoopBackOff). Job
// workers (OnFailure/Never) retry by design and are excluded.
//
// The exit code is deliberately ignored for regular containers and sidecars.
// The kubelet backs off restarts after any exit, and a liveness kill with a
// graceful shutdown ends Completed/0, so requiring a non-zero exit misses the
// loops that flap the most. An ordinary init container that exits 0 has done
// its job, so only its failures count. OOMKilled terminations count too; the
// row then classifies as oom_killed through its last termination reason.
//
// When several containers qualify, the one that terminated most recently is
// the one actively failing; a container that looped earlier and has since
// recovered can still qualify for the rest of the window and must not be the
// one named. Ties keep status order (regular containers before sidecars).
func activeRestartLoop(pod *corev1.Pod, probes map[string]probeFailure, now time.Time) (restartLoop, bool) {
	// A pod being deleted stops its containers on purpose: a rollout or
	// drain SIGTERM ends Completed/0 and is not a crash.
	if pod == nil || pod.DeletionTimestamp != nil || (pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending) {
		return restartLoop{}, false
	}
	var best restartLoop
	found := false
	consider := func(loop restartLoop, ok bool) {
		if ok && (!found || loop.lastFinishedAt.After(best.lastFinishedAt)) {
			best, found = loop, true
		}
	}
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		if containerRestartsAlways(pod, cs.Name) {
			consider(containerRestartLoop(cs, now))
		}
	}
	for i := range pod.Status.InitContainerStatuses {
		cs := &pod.Status.InitContainerStatuses[i]
		loop, ok := containerRestartLoop(cs, now)
		if isNativeSidecarName(pod, cs.Name) {
			loop.sidecar = true
		} else {
			// An ordinary init container blocks the pod until it succeeds;
			// it loops only while the pod is still initializing and its
			// latest attempt failed.
			loop.init = true
			ok = ok && pod.Status.Phase == corev1.PodPending && loop.lastExitCode != 0
		}
		consider(loop, ok)
	}
	if !found {
		return restartLoop{}, false
	}
	best.memoryLimit = containerHasMemoryLimit(pod, best.container)
	return withProbeEvidence(best, pod, probes), true
}

func containerHasMemoryLimit(pod *corev1.Pod, name string) bool {
	for _, list := range [][]corev1.Container{pod.Spec.Containers, pod.Spec.InitContainers} {
		for i := range list {
			if list[i].Name == name {
				_, ok := list[i].Resources.Limits[corev1.ResourceMemory]
				return ok
			}
		}
	}
	return false
}

// otherContainerActiveOOM reports an active OOM on any container other than
// the named one. A sibling's OOM owns the pod's row over a different
// container's loop; the loop container's own OOM does not veto its loop.
func otherContainerActiveOOM(pod *corev1.Pod, name string, now time.Time) bool {
	for _, cs := range health.ActiveOOMKilledContainers(pod, now) {
		if cs.Name != name {
			return true
		}
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.Name != name && cs.State.Terminated != nil && cs.State.Terminated.Reason == "OOMKilled" {
			return true
		}
	}
	return false
}

// oomStatuses is the container an OOM loop's limit diagnosis should examine:
// the looping regular container, or nil (the pod's active OOMs) otherwise.
func (l restartLoop) oomStatuses() []corev1.ContainerStatus {
	if l.lastReason != "OOMKilled" || l.init || l.sidecar {
		return nil
	}
	return []corev1.ContainerStatus{l.status}
}

// containerRestartsAlways reports whether the kubelet restarts this regular
// container after every exit: its own restartPolicy when set (per-container
// policies, Kubernetes 1.34+), else the pod's. OnFailure and Never containers
// (Job workers) retry or stop by design and are not restart loops.
func containerRestartsAlways(pod *corev1.Pod, name string) bool {
	policy := corev1.ContainerRestartPolicy(pod.Spec.RestartPolicy)
	for i := range pod.Spec.Containers {
		if c := &pod.Spec.Containers[i]; c.Name == name && c.RestartPolicy != nil {
			policy = *c.RestartPolicy
		}
	}
	return policy == "" || policy == corev1.ContainerRestartPolicyAlways
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
	if term == nil || term.FinishedAt.IsZero() {
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
	var lastRun time.Duration
	if started := term.StartedAt.Time; started.Unix() > 0 {
		lastRun = finished.Sub(started)
	}
	if lastRun > restartLoopWindow {
		return restartLoop{}, false
	}
	if r := cs.State.Running; r != nil && !r.StartedAt.IsZero() {
		if r.StartedAt.Sub(finished) > restartLoopStaleGap {
			return restartLoop{}, false
		}
		// Recovered: the container is serving and its current run has
		// clearly outlived the loop's rhythm. A loop restarts within one run
		// length plus backoff, so a Ready run past twice the last one (and
		// past the kubelet's 10-minute stability line) means the cause went
		// away; clear now instead of holding the full window after the last
		// crash. An unready container stays held, so the row does not fall
		// straight into a readiness issue. The window above still bounds the
		// hold either way.
		if cs.Ready && now.Sub(r.StartedAt.Time) > max(restartLoopFastRun, 2*lastRun) {
			return restartLoop{}, false
		}
	}
	return restartLoop{
		status:         *cs,
		container:      cs.Name,
		restartCount:   cs.RestartCount,
		lastExitCode:   term.ExitCode,
		lastReason:     term.Reason,
		lastFinishedAt: finished,
		lastRun:        lastRun,
		lastMessage:    strings.TrimSpace(term.Message),
	}, true
}

func withProbeEvidence(loop restartLoop, pod *corev1.Pod, probes map[string]probeFailure) restartLoop {
	if pf, ok := probeFailureFor(probes, pod, loop.container, livenessProbeFailedReason); ok {
		loop.liveness = &pf
	}
	if pf, ok := probeFailureFor(probes, pod, loop.container, readinessProbeFailedReason); ok {
		loop.readiness = &pf
	}
	if pf, ok := probeFailureFor(probes, pod, loop.container, startupProbeFailedReason); ok {
		loop.startup = &pf
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
// more specific (image pulls, config errors, OOM, init stalls) names a
// different problem and keeps its own row. RunContainerError is the Waiting
// state between attempts of a container that fails to start; once the loop
// rule holds, the container has been started and terminated repeatedly, so it
// is another face of the same loop.
func restartLoopMayReplace(reason string) bool {
	switch reason {
	case "Running", "Pending", "Unknown", "", "PodInitializing", "ContainerCreating",
		crashLoopReason, "Error", "Completed", "StartError", "ContainerCannotRun", "RunContainerError",
		highRestartReason, livenessProbeFailedReason, readinessProbeFailedReason:
		return true
	}
	return false
}

// fast reports whether the loop keeps the container mostly down: its last run
// was shorter than the kubelet's stability line, or it never started.
func (l restartLoop) fast() bool {
	return l.lastRun < restartLoopFastRun
}

func (l restartLoop) ref() string {
	if l.sidecar {
		return fmt.Sprintf("sidecar container %q", l.container)
	}
	if l.init {
		return fmt.Sprintf("init container %q", l.container)
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
	if l.startup != nil {
		seen = append(seen, "startup")
	}
	if l.liveness != nil {
		seen = append(seen, "liveness")
	}
	if l.readiness != nil {
		seen = append(seen, "readiness")
	}
	if len(seen) > 0 {
		msg += "; " + strings.Join(seen, ", ") + " probe failures observed"
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
	if l.lastReason == "OOMKilled" {
		if !l.memoryLimit {
			return fmt.Sprintf("%s keeps being OOMKilled. It has no memory limit, so the kernel killed it under node memory pressure.", ref),
				"Check the node's memory pressure and what else runs there, set a memory request that reflects real usage so the scheduler places it with room, and look for a leak or spike in this container."
		}
		return fmt.Sprintf("%s keeps being OOMKilled.", ref),
			"Compare the container's memory use before each kill with its limit: raise the limit if the working set is legitimate, or find the leak or spike (heap settings, caches, request bursts). If it stayed under its limit, the node was under memory pressure: check the node."
	}
	if l.lastReason == "StartError" || l.lastReason == "ContainerCannotRun" {
		return fmt.Sprintf("%s keeps failing to start (%s): the runtime could not start its process.", ref, l.lastReason),
			"Read the runtime error on this issue: usually a missing binary or entrypoint, a bad working directory, or a permission or mount problem in the container spec."
	}
	switch code := l.lastExitCode; code {
	case 0:
		return fmt.Sprintf("%s keeps restarting, and its last run ended with exit code 0. Under restartPolicy Always the kubelet restarts a container whenever it exits; a failed liveness or startup probe also ends this way, because the kubelet lets the process shut down gracefully.", ref),
			"Check the pod's Unhealthy events for liveness or startup probe failures; if present, check the probe against the app (path and port, timeoutSeconds, failureThreshold, startup time). If not, check why the main process exits: command and args, a process that daemonizes, or one-shot work that belongs in a Job."
	case 127:
		return fmt.Sprintf("%s keeps restarting after exit code 127: command not found.", ref),
			"Check the image entrypoint and pod command/args; verify the binary exists in the image."
	case 126:
		return fmt.Sprintf("%s keeps restarting after exit code 126: command found but not executable.", ref),
			"Check executable permissions, the shebang/interpreter, and the pod command/args."
	case 139:
		return fmt.Sprintf("%s keeps restarting after exit code 139: segmentation fault.", ref),
			"Inspect previous container logs and recent image/code changes; check native libraries or unsafe code for a segfault."
	case 143:
		return fmt.Sprintf("%s keeps restarting after exit code 143 (SIGTERM).", ref),
			"A process that exits 143 on SIGTERM (common for JVM and Node apps) was told to stop: check the pod's Unhealthy events for a liveness or startup probe restart, and the previous container logs for shutdown context."
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
	if l.startup != nil {
		out.StartupProbeFailure = &issuesapi.ProbeFailure{LastSeen: l.startup.at.UTC(), Message: Truncate(l.startup.message, restartLoopProbeMessageMax)}
	}
	return out
}

// loopingInitContainer names the ordinary init container a loop is on, or ""
// when the loop is elsewhere or absent.
func loopingInitContainer(loop restartLoop, looping bool) string {
	if looping && loop.init {
		return loop.container
	}
	return ""
}

// loopRow marks a detection emitted for a pod in a restart loop, for
// setLoopSeverities.
type loopRow struct {
	index int
	owner string
	fast  bool
}

func loopOwnerKey(pod *corev1.Pod, ownerGroup, ownerKind, ownerName string) string {
	if ownerKind == "" {
		return pod.Namespace + "//Pod/" + pod.Name
	}
	return pod.Namespace + "/" + ownerGroup + "/" + ownerKind + "/" + ownerName
}

// setLoopSeverities sets each loop row's pinned severity. A loop is critical
// when it is fast (the container is mostly down) and it covers at least half
// of its workload's pods; a slow loop, or one bad replica among many, is a
// warning. Both inputs hold steady for the life of a loop, so the severity
// does too. The denominator is the workload's live pods as observed, which
// works for any owner kind; pods that were never created are not counted,
// which errs toward critical.
func setLoopSeverities(cache *ResourceCache, problems []Detection, rows []loopRow, podsByNamespace map[string][]*corev1.Pod) {
	if len(rows) == 0 {
		return
	}
	looping := map[string]int{}
	namespaces := map[string]bool{}
	for _, r := range rows {
		looping[r.owner]++
		namespaces[problems[r.index].Namespace] = true
	}
	observed := map[string]int{}
	for ns := range namespaces {
		for _, pod := range podsByNamespace[ns] {
			if pod.DeletionTimestamp != nil || (pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending) {
				continue
			}
			group, kind, name := podOwnerKindName(cache, pod)
			if key := loopOwnerKey(pod, group, kind, name); looping[key] > 0 {
				observed[key]++
			}
		}
	}
	for _, r := range rows {
		share := 1.0
		if n := observed[r.owner]; n > 0 {
			share = float64(looping[r.owner]) / float64(n)
		}
		if r.fast && share >= restartLoopImpactShare {
			problems[r.index].Severity = "critical"
		} else {
			problems[r.index].Severity = "high"
		}
	}
}
