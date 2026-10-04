package k8s

import (
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

func restartLoopPod(now time.Time, cs corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: cs.Name}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{cs},
		},
	}
}

func terminatedAgo(now time.Time, ago time.Duration, reason string, code int32) *corev1.ContainerStateTerminated {
	return &corev1.ContainerStateTerminated{Reason: reason, ExitCode: code, FinishedAt: metav1.NewTime(now.Add(-ago))}
}

func TestActiveRestartLoop(t *testing.T) {
	now := time.Now()
	running := func(ago time.Duration) corev1.ContainerState {
		return corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-ago))}}
	}
	status := func(restarts int32, state corev1.ContainerState, last *corev1.ContainerStateTerminated) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: "app", RestartCount: restarts, State: state, LastTerminationState: corev1.ContainerState{Terminated: last}}
	}
	sidecarPolicy := corev1.ContainerRestartPolicyAlways

	cases := []struct {
		name    string
		pod     *corev1.Pod
		want    bool
		sidecar bool
	}{
		{"clean exit inside window", restartLoopPod(now, status(3, running(time.Minute), terminatedAgo(now, 2*time.Minute, "Completed", 0))), true, false},
		{"last crash 29m ago", restartLoopPod(now, status(5, running(28*time.Minute), terminatedAgo(now, 29*time.Minute, "Error", 1))), true, false},
		{"last crash 31m ago", restartLoopPod(now, status(5, running(30*time.Minute), terminatedAgo(now, 31*time.Minute, "Error", 1))), false, false},
		{"two restarts is not a loop", restartLoopPod(now, status(2, running(time.Minute), terminatedAgo(now, 2*time.Minute, "Error", 1))), false, false},
		{"run started 9m after the termination", restartLoopPod(now, status(5, running(time.Minute), terminatedAgo(now, 10*time.Minute, "Error", 1))), true, false},
		{"run started 11m after the termination", restartLoopPod(now, status(5, running(time.Minute), terminatedAgo(now, 12*time.Minute, "Error", 1))), false, false},
		{"OOM loop counts (the row classifies as oom_killed)", restartLoopPod(now, status(5, running(time.Minute), terminatedAgo(now, 2*time.Minute, "OOMKilled", 137))), true, false},
		{"recovered: Ready past max(10m, 2x last run)", func() *corev1.Pod {
			cs := status(5, running(11*time.Minute), &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, StartedAt: metav1.NewTime(now.Add(-14 * time.Minute)), FinishedAt: metav1.NewTime(now.Add(-11*time.Minute - 10*time.Second))})
			cs.Ready = true
			return restartLoopPod(now, cs)
		}(), false, false},
		{"still looping: Ready 9m after a 3m run", func() *corev1.Pod {
			cs := status(5, running(9*time.Minute), &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, StartedAt: metav1.NewTime(now.Add(-12 * time.Minute)), FinishedAt: metav1.NewTime(now.Add(-9*time.Minute - 10*time.Second))})
			cs.Ready = true
			return restartLoopPod(now, cs)
		}(), true, false},
		{"held while unready, even past the early-clear line", restartLoopPod(now, status(5, running(15*time.Minute), &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, StartedAt: metav1.NewTime(now.Add(-18 * time.Minute)), FinishedAt: metav1.NewTime(now.Add(-15*time.Minute - 10*time.Second))})), true, false},
		{"current termination counts", restartLoopPod(now, status(5, corev1.ContainerState{Terminated: terminatedAgo(now, 5*time.Second, "Completed", 0)}, terminatedAgo(now, 40*time.Minute, "Error", 1))), true, false},
		{"first restart after a long run (node bounce)", restartLoopPod(now, status(5, running(time.Minute), &corev1.ContainerStateTerminated{
			Reason: "Error", ExitCode: 255, StartedAt: metav1.NewTime(now.Add(-72 * time.Hour)), FinishedAt: metav1.NewTime(now.Add(-2 * time.Minute)),
		})), false, false},
		{"short previous run", restartLoopPod(now, status(5, running(time.Minute), &corev1.ContainerStateTerminated{
			Reason: "Error", ExitCode: 1, StartedAt: metav1.NewTime(now.Add(-5 * time.Minute)), FinishedAt: metav1.NewTime(now.Add(-2 * time.Minute)),
		})), true, false},
		{"start failure stamped with the Unix epoch", restartLoopPod(now, status(5, corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, &corev1.ContainerStateTerminated{
			Reason: "StartError", ExitCode: 128, StartedAt: metav1.NewTime(time.Unix(0, 0)), FinishedAt: metav1.NewTime(now.Add(-time.Minute)),
		})), true, false},
		{"pod being deleted", func() *corev1.Pod {
			p := restartLoopPod(now, status(5, corev1.ContainerState{Terminated: terminatedAgo(now, 5*time.Second, "Completed", 0)}, terminatedAgo(now, 3*time.Minute, "Error", 1)))
			deleted := metav1.NewTime(now.Add(-10 * time.Second))
			p.DeletionTimestamp = &deleted
			return p
		}(), false, false},
		{"no termination recorded", restartLoopPod(now, status(5, running(time.Minute), nil)), false, false},
		{"OnFailure pod (Job worker)", func() *corev1.Pod {
			p := restartLoopPod(now, status(5, running(time.Minute), terminatedAgo(now, 2*time.Minute, "Error", 1)))
			p.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
			return p
		}(), false, false},
		{"succeeded pod", func() *corev1.Pod {
			p := restartLoopPod(now, status(5, running(time.Minute), terminatedAgo(now, 2*time.Minute, "Error", 1)))
			p.Status.Phase = corev1.PodSucceeded
			return p
		}(), false, false},
		{"ordinary init container failing (Init:CrashLoopBackOff)", func() *corev1.Pod {
			p := restartLoopPod(now, corev1.ContainerStatus{Name: "app"})
			p.Spec.InitContainers = []corev1.Container{{Name: "init"}}
			p.Status.Phase = corev1.PodPending
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "init", RestartCount: 5, State: running(time.Minute),
				LastTerminationState: corev1.ContainerState{Terminated: terminatedAgo(now, 2*time.Minute, "Error", 1)}}}
			return p
		}(), true, false},
		{"ordinary init container that finally succeeded", func() *corev1.Pod {
			p := restartLoopPod(now, corev1.ContainerStatus{Name: "app"})
			p.Spec.InitContainers = []corev1.Container{{Name: "init"}}
			p.Status.Phase = corev1.PodPending
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "init", RestartCount: 5,
				State:                corev1.ContainerState{Terminated: terminatedAgo(now, time.Minute, "Completed", 0)},
				LastTerminationState: corev1.ContainerState{Terminated: terminatedAgo(now, 3*time.Minute, "Error", 1)}}}
			return p
		}(), false, false},
		{"native sidecar looping", func() *corev1.Pod {
			p := restartLoopPod(now, corev1.ContainerStatus{Name: "app", State: running(time.Hour)})
			p.Spec.InitContainers = []corev1.Container{{Name: "proxy", RestartPolicy: &sidecarPolicy}}
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "proxy", RestartCount: 5, State: running(time.Minute),
				LastTerminationState: corev1.ContainerState{Terminated: terminatedAgo(now, 2*time.Minute, "Completed", 0)}}}
			return p
		}(), true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loop, ok := activeRestartLoop(tc.pod, nil, now)
			if ok != tc.want {
				t.Fatalf("activeRestartLoop = %v, want %v (loop %+v)", ok, tc.want, loop)
			}
			if ok && loop.sidecar != tc.sidecar {
				t.Fatalf("sidecar = %v, want %v", loop.sidecar, tc.sidecar)
			}
		})
	}
}

func TestActiveRestartLoop_ProbeEvidenceIsPerContainer(t *testing.T) {
	now := time.Now()
	pod := restartLoopPod(now, corev1.ContainerStatus{Name: "app", RestartCount: 4, LastTerminationState: corev1.ContainerState{Terminated: terminatedAgo(now, time.Minute, "Completed", 0)}})
	probes := map[string]probeFailure{
		"ns/p/other/" + livenessProbeFailedReason: {reason: livenessProbeFailedReason, at: now},
		"ns/p//" + readinessProbeFailedReason:     {reason: readinessProbeFailedReason, at: now, message: "Readiness probe failed"},
	}
	loop, ok := activeRestartLoop(pod, probes, now)
	if !ok {
		t.Fatal("want a loop")
	}
	if loop.liveness != nil {
		t.Fatalf("liveness failure of another container attributed to the loop: %+v", loop.liveness)
	}
	if loop.readiness == nil {
		t.Fatal("pod-level readiness failure (no fieldPath) should be attributed")
	}
}

func TestRestartLoopDiagnosis_StableAcrossPollsAndReplicas(t *testing.T) {
	a := restartLoop{container: "app", restartCount: 4, lastExitCode: 0, lastReason: "Completed", liveness: &probeFailure{}}
	b := restartLoop{container: "app", restartCount: 4592, lastExitCode: 0, lastReason: "Completed", lastFinishedAt: time.Now()}
	causeA, actionA := a.diagnosis()
	causeB, actionB := b.diagnosis()
	if causeA != causeB || actionA != actionB {
		t.Fatalf("diagnosis differs by restart count, time, or probe evidence:\n%q\n%q", causeA, causeB)
	}
	if strings.Contains(causeA, "caused") {
		t.Fatalf("cause = %q, must not assert causation", causeA)
	}
	if !strings.Contains(a.message(), "liveness probe failures observed") || strings.Contains(b.message(), "liveness") {
		t.Fatalf("message must name probe observations only when seen: %q / %q", a.message(), b.message())
	}
}

func TestActiveRestartLoop_UnattributedProbeEventNeedsSingleContainer(t *testing.T) {
	now := time.Now()
	cs := corev1.ContainerStatus{Name: "app", RestartCount: 4, LastTerminationState: corev1.ContainerState{Terminated: terminatedAgo(now, time.Minute, "Completed", 0)}}
	probes := map[string]probeFailure{"ns/p//" + livenessProbeFailedReason: {reason: livenessProbeFailedReason, at: now}}
	single := restartLoopPod(now, cs)
	if loop, _ := activeRestartLoop(single, probes, now); loop.liveness == nil {
		t.Fatal("single-container pod: unattributed liveness event should count")
	}
	multi := restartLoopPod(now, cs)
	multi.Spec.Containers = append(multi.Spec.Containers, corev1.Container{Name: "other"})
	if loop, _ := activeRestartLoop(multi, probes, now); loop.liveness != nil {
		t.Fatal("multi-container pod: unattributed liveness event must not be pinned on the looping container")
	}
}

func TestActiveRestartLoop_IgnoresProbeEventsOfAPredecessorPod(t *testing.T) {
	now := time.Now()
	pod := restartLoopPod(now, corev1.ContainerStatus{Name: "app", RestartCount: 4, LastTerminationState: corev1.ContainerState{Terminated: terminatedAgo(now, time.Minute, "Completed", 0)}})
	pod.UID = "new"
	key := "ns/p/app/" + livenessProbeFailedReason
	if loop, _ := activeRestartLoop(pod, map[string]probeFailure{key: {at: now, podUID: "old"}}, now); loop.liveness != nil {
		t.Fatal("probe event from a previous pod with the same name was attributed")
	}
	if loop, _ := activeRestartLoop(pod, map[string]probeFailure{key: {at: now, podUID: "new"}}, now); loop.liveness == nil {
		t.Fatal("probe event of this pod should be attributed")
	}
}

func TestStalledInitContainerProblem_IgnoresStartedNativeSidecar(t *testing.T) {
	now := time.Now()
	always := corev1.ContainerRestartPolicyAlways
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: "proxy", RestartPolicy: &always}}},
		Status: corev1.PodStatus{Phase: corev1.PodPending, InitContainerStatuses: []corev1.ContainerStatus{{
			Name: "proxy", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-20 * time.Minute))}},
		}}},
	}
	if _, ok := stalledInitContainerProblem(pod, now, ""); !ok {
		t.Fatal("a sidecar that has not passed its startup probe still blocks the pod")
	}
	started := true
	pod.Status.InitContainerStatuses[0].Started = &started
	if got, ok := stalledInitContainerProblem(pod, now, ""); ok {
		t.Fatalf("started native sidecar reported as a stalled init container: %+v", got)
	}
}

// A restart loop must not hide a more specific problem: an invalid liveness
// target keeps its fingerprinted row on every tick, including a ready one, and
// an image pull failure on a sibling container keeps its own reason.
func TestDetectProblems_RestartLoopPrecedence(t *testing.T) {
	defer ResetTestState()
	now := time.Now()
	old := metav1.NewTime(now.Add(-time.Hour))
	loopStatus := corev1.ContainerStatus{
		Name: "app", Ready: true, RestartCount: 9,
		State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-time.Minute))}},
		LastTerminationState: corev1.ContainerState{Terminated: terminatedAgo(now, 2*time.Minute, "Completed", 0)},
	}
	badProbe := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-liveness", Namespace: "prod", CreationTimestamp: old},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromString("admin")},
			}},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{loopStatus}},
	}
	imageSibling := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "image-sibling", Namespace: "prod", CreationTimestamp: old},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}, {Name: "img"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{
			loopStatus,
			{Name: "img", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
		}},
	}
	plainLoop := badProbe.DeepCopy()
	plainLoop.Name = "plain-loop"
	plainLoop.Spec.Containers[0].LivenessProbe = nil
	// Between attempts a container that cannot start waits in
	// RunContainerError; its terminations are StartError with an epoch start.
	startFailure := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "start-failure", Namespace: "prod", CreationTimestamp: old},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "app", RestartCount: 5,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "RunContainerError"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "StartError", ExitCode: 128, Message: `exec: "/nonexistent": no such file or directory`,
				StartedAt: metav1.NewTime(time.Unix(0, 0)), FinishedAt: metav1.NewTime(now.Add(-time.Minute)),
			}},
		}}},
	}

	if err := InitTestResourceCache(fake.NewClientset(badProbe, imageSibling, plainLoop, startFailure)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	var problems []Detection
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		problems = DetectProblems(GetResourceCache(), "prod")
		if hasProblem(problems, "Pod", "bad-liveness", livenessProbeInvalidReason) && hasProblem(problems, "Pod", "image-sibling", "ImagePullBackOff") &&
			hasProblem(problems, "Pod", "plain-loop", crashLoopReason) && hasProblem(problems, "Pod", "start-failure", crashLoopReason) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got, ok := lookupProblem(problems, "Pod", "bad-liveness", livenessProbeInvalidReason); !ok || got.Fingerprint == "" || got.RestartLoop == nil || got.Severity != "critical" {
		t.Fatalf("bad-liveness = %+v (found %v), want the fingerprinted invalid-probe row", got, ok)
	}
	if got, ok := lookupProblem(problems, "Pod", "image-sibling", "ImagePullBackOff"); !ok || got.RestartLoop != nil {
		t.Fatalf("image-sibling = %+v (found %v), want the image pull row", got, ok)
	}
	if got, ok := lookupProblem(problems, "Pod", "start-failure", crashLoopReason); !ok || got.RestartLoop == nil || !strings.Contains(got.RawMessage, "no such file") || !strings.Contains(got.Cause, "failing to start") {
		t.Fatalf("start-failure = %+v (found %v), want a crashloop row carrying the runtime error", got, ok)
	}
	if got, ok := lookupProblem(problems, "Pod", "plain-loop", crashLoopReason); !ok || got.Severity != "critical" || got.RestartLoop == nil {
		t.Fatalf("plain-loop = %+v (found %v), want a critical crashloop row on a ready tick", got, ok)
	}
}

func TestActiveRestartLoop_NamesTheContainerFailingNow(t *testing.T) {
	now := time.Now()
	recovered := corev1.ContainerStatus{Name: "api", Ready: true, RestartCount: 3,
		State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-19 * time.Minute))}},
		LastTerminationState: corev1.ContainerState{Terminated: terminatedAgo(now, 20*time.Minute, "Error", 1)}}
	failing := corev1.ContainerStatus{Name: "sql-proxy", RestartCount: 4,
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: terminatedAgo(now, time.Minute, "Error", 2)}}
	pod := restartLoopPod(now, recovered)
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "sql-proxy"})
	pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, failing)
	loop, ok := activeRestartLoop(pod, nil, now)
	if !ok || loop.container != "sql-proxy" || loop.lastExitCode != 2 {
		t.Fatalf("loop = %+v, want the container that crashed a minute ago", loop)
	}
}

func TestActiveRestartLoop_UsesContainerRestartPolicy(t *testing.T) {
	now := time.Now()
	cs := corev1.ContainerStatus{Name: "app", RestartCount: 5, LastTerminationState: corev1.ContainerState{Terminated: terminatedAgo(now, time.Minute, "Error", 1)}}
	always, onFailure := corev1.ContainerRestartPolicyAlways, corev1.ContainerRestartPolicyOnFailure

	jobPodAlwaysContainer := restartLoopPod(now, cs)
	jobPodAlwaysContainer.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
	jobPodAlwaysContainer.Spec.Containers[0].RestartPolicy = &always
	if _, ok := activeRestartLoop(jobPodAlwaysContainer, nil, now); !ok {
		t.Fatal("a container with restartPolicy Always loops whatever the pod's policy")
	}
	alwaysPodOnFailureContainer := restartLoopPod(now, cs)
	alwaysPodOnFailureContainer.Spec.Containers[0].RestartPolicy = &onFailure
	if _, ok := activeRestartLoop(alwaysPodOnFailureContainer, nil, now); ok {
		t.Fatal("a container with restartPolicy OnFailure retries by design and is not a restart loop")
	}
}

func TestActiveRestartLoop_StartupProbeEvidence(t *testing.T) {
	now := time.Now()
	pod := restartLoopPod(now, corev1.ContainerStatus{Name: "app", RestartCount: 4, LastTerminationState: corev1.ContainerState{Terminated: terminatedAgo(now, time.Minute, "Error", 143)}})
	probes := map[string]probeFailure{"ns/p/app/" + startupProbeFailedReason: {reason: startupProbeFailedReason, at: now, message: "Startup probe failed: connection refused"}}
	loop, ok := activeRestartLoop(pod, probes, now)
	if !ok || loop.startup == nil || loop.evidence().StartupProbeFailure == nil || !strings.Contains(loop.message(), "startup probe failures observed") {
		t.Fatalf("loop = %+v, want startup probe evidence", loop)
	}
	if cause, _ := loop.diagnosis(); !strings.Contains(cause, "143 (SIGTERM)") {
		t.Fatalf("cause = %q, want the SIGTERM exit explained", cause)
	}
}

func TestEventLastTime_UsesSeriesLastObservedTime(t *testing.T) {
	first := metav1.NewMicroTime(time.Now().Add(-time.Hour))
	last := metav1.NewMicroTime(time.Now().Add(-time.Minute))
	e := &corev1.Event{EventTime: first, Series: &corev1.EventSeries{LastObservedTime: last}}
	if got := eventLastTime(e); !got.Equal(last.Time) {
		t.Fatalf("eventLastTime = %v, want the series' last observation %v", got, last.Time)
	}
}

func TestDetectProblems_RestartLoopSeverityTier(t *testing.T) {
	defer ResetTestState()
	now := time.Now()
	controller := true
	created := metav1.NewTime(now.Add(-24 * time.Hour))
	looping := func(name string, run time.Duration) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: "app", RestartCount: 6,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1,
				StartedAt: metav1.NewTime(now.Add(-time.Minute - run)), FinishedAt: metav1.NewTime(now.Add(-time.Minute))}}}
	}
	healthy := corev1.ContainerStatus{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: created}}}
	var objs []runtime.Object
	workload := func(dep string, statuses ...corev1.ContainerStatus) {
		objs = append(objs,
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: dep, Namespace: "prod", CreationTimestamp: created}},
			&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: dep + "-rs", Namespace: "prod", CreationTimestamp: created,
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: dep, Controller: &controller}}}})
		for i, cs := range statuses {
			objs = append(objs, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d", dep, i), Namespace: "prod", CreationTimestamp: created,
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: dep + "-rs", Controller: &controller}}},
				Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
				Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{cs}},
			})
		}
	}
	workload("one-of-four", looping("x", time.Minute), healthy, healthy, healthy)
	workload("half", looping("x", time.Minute), looping("y", time.Minute), healthy, healthy)
	workload("slow-single", looping("x", 11*time.Minute))
	workload("fast-single", looping("x", 30*time.Second))
	if err := InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	want := map[string]string{"one-of-four-0": "high", "half-0": "critical", "half-1": "critical", "slow-single-0": "high", "fast-single-0": "critical"}
	var problems []Detection
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		problems = DetectProblems(GetResourceCache(), "prod")
		n := 0
		for name := range want {
			if hasProblem(problems, "Pod", name, crashLoopReason) {
				n++
			}
		}
		if n == len(want) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for name, sev := range want {
		assertProblem(t, problems, "Pod", name, crashLoopReason, sev)
	}
}

func TestDetectProblems_InitAndOOMLoops(t *testing.T) {
	defer ResetTestState()
	now := time.Now()
	old := metav1.NewTime(now.Add(-time.Hour))
	// A failing migration: each attempt runs 7 minutes (past the 5-minute
	// stall line) and fails; the loop, not a stall, owns the row.
	initLoop := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "prod", CreationTimestamp: old},
		Spec:       corev1.PodSpec{InitContainers: []corev1.Container{{Name: "migrate"}}, Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{{Name: "migrate", RestartCount: 4,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-7 * time.Minute))}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1,
					StartedAt: metav1.NewTime(now.Add(-15 * time.Minute)), FinishedAt: metav1.NewTime(now.Add(-8 * time.Minute))}}}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}}},
		},
	}
	// An OOM loop between kills: Running and Ready again.
	oomLoop := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "leaky", Namespace: "prod", CreationTimestamp: old},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, RestartCount: 9,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-6 * time.Minute))}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137,
				StartedAt: metav1.NewTime(now.Add(-10 * time.Minute)), FinishedAt: metav1.NewTime(now.Add(-6*time.Minute - 5*time.Second))}}}}},
	}
	if err := InitTestResourceCache(fake.NewClientset(initLoop, oomLoop)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	var problems []Detection
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		problems = DetectProblems(GetResourceCache(), "prod")
		if hasProblem(problems, "Pod", "migrate", crashLoopReason) && hasProblem(problems, "Pod", "leaky", crashLoopReason) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got, ok := lookupProblem(problems, "Pod", "migrate", crashLoopReason); !ok || got.RestartLoop == nil || !strings.Contains(got.Cause, "init container") {
		t.Fatalf("migrate = %+v (found %v), want an init-container restart loop, not a stall", got, ok)
	}
	if hasProblem(problems, "Pod", "migrate", initContainerStalledReason) {
		t.Fatalf("a looping init container must not also read as stalled: %+v", problems)
	}
	got, ok := lookupProblem(problems, "Pod", "leaky", crashLoopReason)
	if !ok || got.RestartLoop == nil || got.LastTerminatedReason != "OOMKilled" || !strings.Contains(got.Cause, "OOMKilled") {
		t.Fatalf("leaky = %+v (found %v), want an OOM loop row held on a Ready tick", got, ok)
	}
}

func TestOtherContainerActiveOOM(t *testing.T) {
	now := time.Now()
	oom := func(name string) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Terminated: terminatedAgo(now, 5*time.Second, "OOMKilled", 137)}}
	}
	pod := restartLoopPod(now, oom("app"))
	if otherContainerActiveOOM(pod, "app", now) {
		t.Fatal("the loop container's own OOM must not veto its OOM loop")
	}
	pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, oom("cache"))
	if !otherContainerActiveOOM(pod, "app", now) {
		t.Fatal("a sibling's active OOM owns the row")
	}
}

func TestRestartLoopDiagnosis_OOMWordingFollowsTheLimit(t *testing.T) {
	limited := restartLoop{container: "app", lastReason: "OOMKilled", lastExitCode: 137, memoryLimit: true}
	if cause, action := limited.diagnosis(); !strings.Contains(cause, "OOMKilled") || !strings.Contains(action, "limit") || strings.Contains(cause, "exceeds") {
		t.Fatalf("limited OOM diagnosis = %q / %q", cause, action)
	}
	unlimited := limited
	unlimited.memoryLimit = false
	if cause, _ := unlimited.diagnosis(); !strings.Contains(cause, "no memory limit") || !strings.Contains(cause, "node memory pressure") {
		t.Fatalf("unlimited OOM diagnosis = %q, want node pressure named", cause)
	}
}
