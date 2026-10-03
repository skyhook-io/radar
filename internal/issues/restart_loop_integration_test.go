package issues

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// restartLoopTick is one poll of a Deployment whose single pod is in a restart
// loop: the pod's container state, the Deployment's availability, and the
// kubelet events visible at that moment.
type restartLoopTick struct {
	name   string
	status corev1.ContainerStatus
	ready  bool
	events []*corev1.Event
}

// composeRestartLoopTick runs one tick through the real detector and composer
// and returns the grouped issues plus the raw detections.
func composeRestartLoopTick(t *testing.T, now time.Time, tick restartLoopTick) ([]Issue, []k8s.Detection) {
	t.Helper()
	k8s.ResetTestState()
	controller := true
	replicas := int32(1)
	available, unavailable := int32(0), int32(1)
	podReady := corev1.ConditionFalse
	if tick.ready {
		available, unavailable = 1, 0
		podReady = corev1.ConditionTrue
	}
	created := metav1.NewTime(now.Add(-48 * time.Hour))
	objs := []runtime.Object{
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "knative", CreationTimestamp: created},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
			Status: appsv1.DeploymentStatus{
				Replicas:            1,
				UpdatedReplicas:     1,
				ReadyReplicas:       available,
				AvailableReplicas:   available,
				UnavailableReplicas: unavailable,
			},
		},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name:              "gateway-rs",
			Namespace:         "knative",
			CreationTimestamp: created,
			OwnerReferences:   []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "gateway", Controller: &controller}},
		}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "gateway-abc",
				Namespace:         "knative",
				CreationTimestamp: created,
				OwnerReferences:   []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "gateway-rs", Controller: &controller}},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:           "gateway",
				LivenessProbe:  &corev1.Probe{},
				ReadinessProbe: &corev1.Probe{},
			}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: podReady, LastTransitionTime: metav1.NewTime(now.Add(-6 * time.Minute))},
					{Type: corev1.ContainersReady, Status: podReady, LastTransitionTime: metav1.NewTime(now.Add(-6 * time.Minute))},
				},
				ContainerStatuses: []corev1.ContainerStatus{tick.status},
			},
		},
	}
	for _, e := range tick.events {
		objs = append(objs, e)
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
		t.Fatalf("%s: InitTestResourceCache: %v", tick.name, err)
	}
	provider := &CacheProvider{cache: k8s.GetResourceCache()}
	var detections []k8s.Detection
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		detections = provider.DetectProblems([]string{"knative"})
		if hasDetection(detections, "Pod", "gateway-abc") || tick.ready {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return Compose(provider, Filters{Namespaces: []string{"knative"}, Grouped: true, Limit: NoLimit}), detections
}

func hasDetection(ds []k8s.Detection, kind, name string) bool {
	for _, d := range ds {
		if d.Kind == kind && d.Name == name {
			return true
		}
	}
	return false
}

func probeEvent(name, probe string, at time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "knative"},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Pod", Namespace: "knative", Name: "gateway-abc",
			FieldPath: "spec.containers{gateway}",
		},
		Type:          corev1.EventTypeWarning,
		Reason:        "Unhealthy",
		Message:       probe + " probe failed: HTTP probe failed with statuscode: 503",
		LastTimestamp: metav1.NewTime(at),
	}
}

// TestCompose_RestartLoopKeepsOneIssueAcrossTheCycle walks a liveness-driven
// restart loop (graceful exit 0, the kourier-gateway pattern seen in
// production) through every state the kubelet reports during one cycle. Read
// tick by tick, these used to be crashloop, readiness_failed,
// liveness_probe_failed, workload_degraded, and nothing at all — five issue
// ids for one problem. They must now be one critical crashloop issue.
func TestCompose_RestartLoopKeepsOneIssueAcrossTheCycle(t *testing.T) {
	defer k8s.ResetTestState()
	now := time.Now()
	at := func(ago time.Duration) metav1.Time { return metav1.NewTime(now.Add(-ago)) }
	completed := func(ago time.Duration) *corev1.ContainerStateTerminated {
		return &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0, StartedAt: at(ago + 3*time.Minute), FinishedAt: at(ago)}
	}
	base := corev1.ContainerStatus{Name: "gateway", RestartCount: 4592}
	with := func(mut func(*corev1.ContainerStatus)) corev1.ContainerStatus {
		cs := base
		mut(&cs)
		return cs
	}

	ticks := []restartLoopTick{
		{name: "backoff", status: with(func(cs *corev1.ContainerStatus) {
			cs.State.Waiting = &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 5m0s restarting failed container=gateway"}
			cs.LastTerminationState.Terminated = completed(2 * time.Minute)
		})},
		{name: "running-unready-no-events", status: with(func(cs *corev1.ContainerStatus) {
			cs.State.Running = &corev1.ContainerStateRunning{StartedAt: at(30 * time.Second)}
			cs.LastTerminationState.Terminated = completed(3 * time.Minute)
		})},
		{name: "readiness-failing", status: with(func(cs *corev1.ContainerStatus) {
			cs.State.Running = &corev1.ContainerStateRunning{StartedAt: at(time.Minute)}
			cs.LastTerminationState.Terminated = completed(4 * time.Minute)
		}), events: []*corev1.Event{probeEvent("r", "Readiness", now.Add(-10*time.Second))}},
		{name: "liveness-failing", status: with(func(cs *corev1.ContainerStatus) {
			cs.State.Running = &corev1.ContainerStateRunning{StartedAt: at(2 * time.Minute)}
			cs.LastTerminationState.Terminated = completed(5 * time.Minute)
		}), events: []*corev1.Event{probeEvent("l", "Liveness", now.Add(-5*time.Second))}},
		{name: "just-terminated-exit-0", status: with(func(cs *corev1.ContainerStatus) {
			cs.State.Terminated = completed(5 * time.Second)
			cs.LastTerminationState.Terminated = completed(6 * time.Minute)
		}), events: []*corev1.Event{probeEvent("l", "Liveness", now.Add(-20*time.Second))}},
		{name: "serving-between-crashes", ready: true, status: with(func(cs *corev1.ContainerStatus) {
			cs.Ready = true
			cs.State.Running = &corev1.ContainerStateRunning{StartedAt: at(8 * time.Minute)}
			cs.LastTerminationState.Terminated = completed(8*time.Minute + 10*time.Second)
		})},
		{name: "serving-29m-after-last-crash", ready: true, status: with(func(cs *corev1.ContainerStatus) {
			cs.Ready = true
			cs.State.Running = &corev1.ContainerStateRunning{StartedAt: at(28 * time.Minute)}
			cs.LastTerminationState.Terminated = completed(29 * time.Minute)
		})},
	}

	var id, cause string
	for _, tick := range ticks {
		issues, detections := composeRestartLoopTick(t, now, tick)
		var mine []Issue
		for _, iss := range issues {
			if iss.Namespace == "knative" && iss.Name == "gateway" {
				mine = append(mine, iss)
			}
		}
		if len(mine) != 1 {
			t.Fatalf("%s: gateway issues = %+v, want exactly one", tick.name, mine)
		}
		got := mine[0]
		if got.Category != issuesapi.CategoryCrashLoop || got.Severity != SeverityCritical {
			t.Fatalf("%s: issue = %s/%s, want critical crashloop", tick.name, got.Severity, got.Category)
		}
		if id == "" {
			id = got.ID
		} else if got.ID != id {
			t.Fatalf("%s: issue id = %s, want %s for the whole loop", tick.name, got.ID, id)
		}
		if got.RestartLoop == nil || got.RestartLoop.Container != "gateway" || got.RestartLoop.RestartCount != 4592 || got.RestartLoop.LastExitCode != 0 {
			t.Fatalf("%s: restart loop evidence = %+v", tick.name, got.RestartLoop)
		}
		if got.Cause == "" || got.Action == "" {
			t.Fatalf("%s: issue has no diagnosis: %+v", tick.name, got)
		}
		if cause == "" {
			cause = got.Cause
		} else if got.Cause != cause {
			t.Fatalf("%s: cause changed mid-loop:\n%q\n%q", tick.name, got.Cause, cause)
		}
		// The Deployment's own degraded row is folded into the loop, not
		// dropped because it never fired: assert it was detected when down.
		if !tick.ready && !hasDetection(detections, "Deployment", "gateway") {
			t.Fatalf("%s: fixture should emit the Deployment degraded detection", tick.name)
		}
		switch tick.name {
		case "liveness-failing", "just-terminated-exit-0":
			if got.RestartLoop.LivenessProbeFailure == nil {
				t.Fatalf("%s: want liveness evidence, got %+v", tick.name, got.RestartLoop)
			}
			if fact := restartCauseFactMessage(got); !strings.Contains(fact, "liveness probe failure last seen") {
				t.Fatalf("%s: restart_cause fact = %q, want the liveness observation", tick.name, fact)
			}
		case "readiness-failing":
			if got.RestartLoop.ReadinessProbeFailure == nil {
				t.Fatalf("%s: want readiness evidence, got %+v", tick.name, got.RestartLoop)
			}
		}
	}

	// 31 minutes after the last crash, the loop is over: no issue.
	issues, _ := composeRestartLoopTick(t, now, restartLoopTick{name: "recovered", ready: true, status: with(func(cs *corev1.ContainerStatus) {
		cs.Ready = true
		cs.State.Running = &corev1.ContainerStateRunning{StartedAt: at(30 * time.Minute)}
		cs.LastTerminationState.Terminated = completed(31 * time.Minute)
	})})
	for _, iss := range issues {
		if iss.Name == "gateway" || iss.Name == "gateway-abc" {
			t.Fatalf("recovered: issue %+v still open 31m after the last crash", iss)
		}
	}
}

func restartCauseFactMessage(i Issue) string {
	if i.DiagnosticContext == nil {
		return ""
	}
	for _, f := range i.DiagnosticContext.Facts {
		if f.Type == factRestartCause {
			return f.Message
		}
	}
	return ""
}
