package k8s

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func ptr32(i int32) *int32 { return &i }

// Exercises the bind-time detector end-to-end: a Pending pod the scheduler
// rejected on arch, with the node-fit resolver naming the offending label.
func TestDetectSchedulingProblems_BindTime(t *testing.T) {
	defer ResetTestState()
	node := func(name string) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"kubernetes.io/arch": "amd64"}}}
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"},
		Spec:       corev1.PodSpec{NodeSelector: map[string]string{"kubernetes.io/arch": "arm64"}},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  "Unschedulable",
				Message: "0/2 nodes are available: 2 node(s) didn't match Pod's node affinity/selector.",
			}},
		},
	}
	if err := InitTestResourceCache(fake.NewClientset(node("n1"), node("n2"), pod)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	problems := DetectSchedulingProblems(GetResourceCache(), "prod")

	if !findProblem(problems, "Pod", "prod", "web", "Unschedulable") {
		t.Fatalf("expected Unschedulable Pod problem, got %+v", problems)
	}
	for _, p := range problems {
		if p.Name == "web" {
			for _, want := range []string{"kubernetes.io/arch", "arm64", "amd64"} {
				if !strings.Contains(p.Message, want) {
					t.Errorf("message %q should name the offending label %q", p.Message, want)
				}
			}
		}
	}
}

// Exercises the admission FailedCreate path: dedup to one row per object, the
// recovered-workload cross-check (created-but-not-ready is skipped), and that
// the LATEST event wins when the active blocker changed (quota → webhook).
func TestDetectAdmissionProblems_FailedCreateCrossCheck(t *testing.T) {
	defer ResetTestState()
	// replicas = pods actually CREATED. "blocked" = couldn't create (replicas<2);
	// created-but-not-ready (replicas==2, ready==0, e.g. now unschedulable) is
	// NOT admission-blocked and must be skipped.
	rs := func(name string, replicas int32) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod"},
			Spec:       appsv1.ReplicaSetSpec{Replicas: ptr32(2)},
			Status:     appsv1.ReplicaSetStatus{Replicas: replicas, ReadyReplicas: 0},
		}
	}
	evt := func(name, rsName, msg string, last metav1.Time) *corev1.Event {
		return &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: "ReplicaSet", Namespace: "prod", Name: rsName},
			Reason:         "FailedCreate",
			Type:           corev1.EventTypeWarning,
			Message:        msg,
			LastTimestamp:  last,
		}
	}
	quotaMsg := `Error creating: pods "x" is forbidden: exceeded quota: mem-quota, used: requests.memory=2Gi, limited: requests.memory=2Gi`
	webhookMsg := `Error creating: admission webhook "vpod.example.com" denied the request: blocked`
	nowT := metav1.Now()
	oldT := metav1.NewTime(nowT.Add(-10 * time.Minute))

	// rs-blocked has two events: an OLDER quota rejection and a NEWER webhook
	// rejection (the active blocker changed). Expect exactly one row, carrying
	// the LATEST reason (webhook) — not whichever the informer iterates first.
	if err := InitTestResourceCache(fake.NewClientset(
		rs("rs-blocked", 0), rs("rs-ok", 2),
		evt("e1", "rs-blocked", quotaMsg, oldT), evt("e1b", "rs-blocked", webhookMsg, nowT),
		evt("e2", "rs-ok", quotaMsg, nowT),
	)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	problems := DetectAdmissionProblems(GetResourceCache(), "prod")

	if !findProblem(problems, "ReplicaSet", "prod", "rs-blocked", "WebhookDenied") {
		t.Errorf("rs-blocked should surface the LATEST blocker (WebhookDenied), got %+v", problems)
	}
	blockedRows := 0
	for _, p := range problems {
		if p.Name == "rs-blocked" {
			blockedRows++
			if p.Reason == "QuotaExceeded" {
				t.Errorf("stale (older) quota event must not win over the newer webhook one: %+v", p)
			}
		}
		if p.Name == "rs-ok" {
			t.Errorf("ReplicaSet with pods created (replicas met) but not ready — e.g. now unschedulable — is not admission-blocked and must be skipped: %+v", p)
		}
	}
	if blockedRows != 1 {
		t.Errorf("expected exactly 1 row for rs-blocked (deduped by object), got %d: %+v", blockedRows, problems)
	}
}

// A SchedulingGated pod has PodScheduled=False but reason=SchedulingGated —
// the scheduler hasn't tried yet because the pod carries scheduling gates.
// That's an intentional not-yet-scheduled state, not a placement failure, so
// it must NOT surface as Unschedulable (matching the frontend's reason gate).
func TestDetectSchedulingProblems_SchedulingGatedIsNotUnschedulable(t *testing.T) {
	defer ResetTestState()
	gated := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "gated", Namespace: "prod"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  corev1.PodReasonSchedulingGated,
				Message: "Scheduling is blocked due to non-empty scheduling gates",
			}},
		},
	}
	if err := InitTestResourceCache(fake.NewClientset(gated)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	if IsPodUnschedulable(gated) {
		t.Errorf("SchedulingGated pod must not be reported unschedulable")
	}
	for _, p := range DetectSchedulingProblems(GetResourceCache(), "prod") {
		if p.Name == "gated" {
			t.Errorf("SchedulingGated pod must not surface a scheduling problem: %+v", p)
		}
	}
}

// Exercises the post-bind detector's latest-event-wins dedup: a pod stuck
// scheduled (Pending, PodScheduled!=False) with two kubelet events — an older
// NetworkNotReady and a newer FailedMount — yields one row carrying the LATEST
// blocker, not whichever the informer iterated first.
func TestDetectPostBindProblems_LatestEventWins(t *testing.T) {
	defer ResetTestState()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod", CreationTimestamp: metav1.NewTime(time.Now().Add(-8 * time.Minute))},
		Status:     corev1.PodStatus{Phase: corev1.PodPending}, // scheduled (no PodScheduled=False condition)
	}
	nowT := metav1.Now()
	oldT := metav1.NewTime(nowT.Add(-5 * time.Minute))
	ev := func(name, reason, msg string, last metav1.Time) *corev1.Event {
		return &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "prod", Name: "web"},
			Reason:         reason,
			Type:           corev1.EventTypeWarning,
			Message:        msg,
			LastTimestamp:  last,
		}
	}
	if err := InitTestResourceCache(fake.NewClientset(
		pod,
		ev("e1", "FailedCreatePodSandBox", "failed to create pod sandbox: network is not ready", oldT),
		ev("e2", "FailedMount", "Unable to attach or mount volumes: timed out waiting for the condition", nowT),
	)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	problems := DetectPostBindProblems(GetResourceCache(), "prod")

	if !findProblem(problems, "Pod", "prod", "web", "VolumeMount") {
		t.Fatalf("expected the LATEST blocker (VolumeMount) to win, got %+v", problems)
	}
	rows := 0
	for _, p := range problems {
		if p.Name == "web" {
			rows++
			if p.Reason == "SandboxCreationFailed" {
				t.Errorf("stale (older) sandbox event must not win over the newer mount one: %+v", p)
			}
		}
	}
	if rows != 1 {
		t.Errorf("expected exactly 1 post-bind row for web (deduped by pod), got %d: %+v", rows, problems)
	}
}

// Exercises the cross-check for Job + DaemonSet, whose created-count signals
// differ from the replica kinds: a Job that created no pod and a partially
// scheduled DaemonSet are still blocked; a terminally-failed Job (Failed>0) and
// a fully-scheduled DaemonSet are not, so stale quota events must not surface.
func TestDetectAdmissionProblems_JobAndDaemonSetCrossCheck(t *testing.T) {
	defer ResetTestState()
	evt := func(name, kind, objName string) *corev1.Event {
		return &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: kind, Namespace: "prod", Name: objName},
			Reason:         "FailedCreate",
			Type:           corev1.EventTypeWarning,
			Message:        `Error creating: pods "x" is forbidden: exceeded quota: q, used: pods=1, limited: pods=1`,
			LastTimestamp:  metav1.Now(),
		}
	}
	jobBlocked := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job-blocked", Namespace: "prod"}} // all counters 0 → created nothing → blocked
	jobFailed := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job-failed", Namespace: "prod"}, Status: batchv1.JobStatus{Failed: 3}}
	dsBlocked := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "ds-blocked", Namespace: "prod"}, Status: appsv1.DaemonSetStatus{CurrentNumberScheduled: 1, DesiredNumberScheduled: 3}}
	dsOk := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "ds-ok", Namespace: "prod"}, Status: appsv1.DaemonSetStatus{CurrentNumberScheduled: 3, DesiredNumberScheduled: 3}}

	if err := InitTestResourceCache(fake.NewClientset(
		jobBlocked, jobFailed, dsBlocked, dsOk,
		evt("je1", "Job", "job-blocked"), evt("je2", "Job", "job-failed"),
		evt("de1", "DaemonSet", "ds-blocked"), evt("de2", "DaemonSet", "ds-ok"),
	)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	problems := DetectAdmissionProblems(GetResourceCache(), "prod")

	if !findProblem(problems, "Job", "prod", "job-blocked", "QuotaExceeded") {
		t.Errorf("Job that created no pod should surface QuotaExceeded, got %+v", problems)
	}
	if !findProblem(problems, "DaemonSet", "prod", "ds-blocked", "QuotaExceeded") {
		t.Errorf("partially-scheduled DaemonSet should surface QuotaExceeded, got %+v", problems)
	}
	for _, p := range problems {
		if p.Name == "job-failed" {
			t.Errorf("terminally-failed Job (Failed>0) created a pod, so it's not admission-blocked and must be skipped: %+v", p)
		}
		if p.Name == "ds-ok" {
			t.Errorf("fully-scheduled DaemonSet must be skipped: %+v", p)
		}
	}
}
