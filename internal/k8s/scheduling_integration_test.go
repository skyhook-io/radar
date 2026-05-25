package k8s

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func ptr32(i int32) *int32 { return &i }

func quotaWith(name, ns string, hard, used corev1.ResourceList) *corev1.ResourceQuota {
	return &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status:     corev1.ResourceQuotaStatus{Hard: hard, Used: used},
	}
}

// Exercises detectQuotaPressure end-to-end through the cache: the >=90%/>=100%
// severity ramp AND the S1 filter that ignores object-count quotas (configmaps)
// which don't gate pod admission.
func TestDetectAdmissionProblems_QuotaSaturation(t *testing.T) {
	defer ResetTestState()
	near := quotaWith("mem-near", "prod",
		corev1.ResourceList{"requests.memory": resource.MustParse("300Mi")},
		corev1.ResourceList{"requests.memory": resource.MustParse("296Mi")}) // 98.6% → QuotaNearLimit
	full := quotaWith("mem-full", "prod",
		corev1.ResourceList{"requests.memory": resource.MustParse("300Mi")},
		corev1.ResourceList{"requests.memory": resource.MustParse("300Mi")}) // 100% → QuotaExceeded
	cmFull := quotaWith("cm-full", "prod",
		corev1.ResourceList{"configmaps": resource.MustParse("10")},
		corev1.ResourceList{"configmaps": resource.MustParse("10")}) // 100% but not pod-admission → no row

	if err := InitTestResourceCache(fake.NewClientset(near, full, cmFull)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	problems := DetectAdmissionProblems(GetResourceCache(), "prod")

	if !findProblem(problems, "ResourceQuota", "prod", "mem-near", "QuotaNearLimit") {
		t.Errorf("expected QuotaNearLimit for mem-near (98.6%%), got %+v", problems)
	}
	if !findProblem(problems, "ResourceQuota", "prod", "mem-full", "QuotaExceeded") {
		t.Errorf("expected QuotaExceeded for mem-full (100%%), got %+v", problems)
	}
	for _, p := range problems {
		if p.Name == "cm-full" {
			t.Errorf("object-count quota (configmaps) must NOT surface as a scheduling blocker: %+v", p)
		}
	}
}

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

// Exercises the admission FailedCreate path AND the recovered-workload
// cross-check: a quota rejection on a still-blocked ReplicaSet surfaces; the
// same event on a recovered ReplicaSet (all replicas ready) is skipped.
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
	evt := func(name, rsName string) *corev1.Event {
		return &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: "ReplicaSet", Namespace: "prod", Name: rsName},
			Reason:         "FailedCreate",
			Type:           corev1.EventTypeWarning,
			Message:        `Error creating: pods "x" is forbidden: exceeded quota: mem-quota, used: requests.memory=2Gi, limited: requests.memory=2Gi`,
			LastTimestamp:  metav1.Now(),
		}
	}
	// Two FailedCreate events for rs-blocked (a controller emits one per failed
	// attempt, each with a different generated pod name) → exactly one row.
	if err := InitTestResourceCache(fake.NewClientset(rs("rs-blocked", 0), rs("rs-ok", 2), evt("e1", "rs-blocked"), evt("e1b", "rs-blocked"), evt("e2", "rs-ok"))); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	problems := DetectAdmissionProblems(GetResourceCache(), "prod")

	if !findProblem(problems, "ReplicaSet", "prod", "rs-blocked", "QuotaExceeded") {
		t.Errorf("still-blocked ReplicaSet should surface QuotaExceeded, got %+v", problems)
	}
	blockedRows := 0
	for _, p := range problems {
		if p.Name == "rs-blocked" {
			blockedRows++
		}
	}
	if blockedRows != 1 {
		t.Errorf("expected exactly 1 row for rs-blocked (deduped by object), got %d: %+v", blockedRows, problems)
	}
	for _, p := range problems {
		if p.Name == "rs-ok" {
			t.Errorf("ReplicaSet with pods created (replicas met) but not ready — e.g. now unschedulable — is not admission-blocked and must be skipped: %+v", p)
		}
	}
}
