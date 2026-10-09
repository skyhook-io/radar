package cnpg

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCNPGQuorumArithmetic(t *testing.T) {
	ready := func(name string, ok bool) *corev1.Pod {
		st := corev1.ConditionFalse
		if ok {
			st = corev1.ConditionTrue
		}
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: st}}}}
	}
	pods := []*corev1.Pod{ready("a", true), ready("b", true), ready("c", false)}

	q := CNPGHAQuorum{Status: &CNPGHAQuorumStatus{StandbyNames: []string{"b", "c"}, StandbyNumber: 2, Primary: "a"}}
	cnpgQuorumArithmetic(&q, pods, true)
	if *q.R != 1 || !*q.Holds {
		t.Errorf("R=1 W=2 N=2 should hold: %+v", q)
	}

	reset := CNPGHAQuorum{Status: &CNPGHAQuorumStatus{StandbyNames: []string{}}}
	cnpgQuorumArithmetic(&reset, pods, true)
	if reset.N != nil || reset.Holds != nil {
		t.Errorf("a reset object records no configuration; nothing may be computed: %+v", reset)
	}

	unknownPods := CNPGHAQuorum{Status: &CNPGHAQuorumStatus{StandbyNames: []string{"b"}, StandbyNumber: 1}}
	cnpgQuorumArithmetic(&unknownPods, nil, false)
	if unknownPods.N == nil || unknownPods.R != nil || unknownPods.Holds != nil {
		t.Errorf("without Pods R and the verdict are unknown: %+v", unknownPods)
	}
}

func TestCNPGUncachedReasonSaysWhatIsUnknownAndWhy(t *testing.T) {
	if got := cnpgUncachedReason("Zones", "Nodes", "", true, false, false); got != "Zones unknown: Radar's own credentials could not list Nodes when it connected" {
		t.Errorf("uncached = %q", got)
	}
	if got := cnpgUncachedReason("Instance Jobs", "Jobs", "pg", false, true, true); got != "Instance Jobs unknown: Radar watches Jobs only in the namespaces it chose when it connected, and pg is not one of them" {
		t.Errorf("out of scope = %q", got)
	}
	if got := cnpgUncachedReason("Instance Jobs", "Jobs", "pg", false, false, false); got != "Instance Jobs unknown: Radar is still loading Jobs" {
		t.Errorf("syncing = %q", got)
	}
	if got := cnpgUncachedReason("Zones", "Nodes", "", false, false, true); got != "" {
		t.Errorf("cached = %q", got)
	}
}

func TestCNPGHAJobSchedulerEvidence(t *testing.T) {
	job := cnpgJob("db", "analytics-1-initdb", "job-uid", clusterRef("analytics", "cluster-uid"))
	job.Status.Active = 1
	pod := cnpgJobPod("db", "analytics-1-initdb-abc", "analytics", jobRef(job.Name, string(job.UID)))
	got := cnpgHAJobOf(job, pod)
	if got.Phase != "pending" || !strings.Contains(got.Reason, "Pod cannot be scheduled: Unschedulable: 0/2 nodes") {
		t.Fatalf("job = %+v", got)
	}
	if got := cnpgHAJobOf(job); got.Phase != "active" {
		t.Fatalf("without Pod access: %+v", got)
	}
	pod.OwnerReferences[0].UID = "previous-job"
	if got := cnpgHAJobOf(job, pod); got.Phase != "active" {
		t.Fatalf("stale Job Pod adopted: %+v", got)
	}
	pod.OwnerReferences[0] = jobRef(job.Name, string(job.UID))
	pod.Status.Phase = corev1.PodRunning
	if got := cnpgHAJobOf(job, pod); got.Phase != "active" {
		t.Fatalf("active Job: %+v", got)
	}
	pending := pod.DeepCopy()
	pending.Status.Phase = corev1.PodPending
	for _, pods := range [][]*corev1.Pod{{pending, pod}, {pod, pending}} {
		if got := cnpgHAJobOf(job, pods...); got.Phase != "active" {
			t.Fatalf("Job with a running Pod: %+v", got)
		}
	}
}
