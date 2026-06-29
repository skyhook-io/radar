package topology

import (
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestGetPodStatusInspectsContainers pins the crashloop-green fix: a crashlooping
// pod has phase Running, so the old phase-only classifier coloured it green. The
// canonical classifier reads container state, so it now reads unhealthy — and
// matches the resource table.
func TestGetPodStatusInspectsContainers(t *testing.T) {
	crashloop := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{
			{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
		},
	}}
	if got := getPodStatus(crashloop); got != StatusUnhealthy {
		t.Errorf("crashlooping pod node color = %q, want unhealthy (was healthy under phase-only)", got)
	}

	ready := &corev1.Pod{Status: corev1.PodStatus{
		Phase:             corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{{Ready: true}},
	}}
	if got := getPodStatus(ready); got != StatusHealthy {
		t.Errorf("ready pod node color = %q, want healthy", got)
	}

	// Completed pod is neutral → renders sky (StatusNeutral), distinct from the
	// gray of an unknown/node-lost pod.
	completed := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
	if got := getPodStatus(completed); got != StatusNeutral {
		t.Errorf("completed pod node color = %q, want neutral", got)
	}

	// Unschedulable pod reads degraded (the topology surface folds the scheduling
	// signal, like the timeline) — not green, even when young.
	unsched := &corev1.Pod{Status: corev1.PodStatus{
		Phase:      corev1.PodPending,
		Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable}},
	}}
	if got := getPodStatus(unsched); got != StatusDegraded {
		t.Errorf("unschedulable pod node color = %q, want degraded", got)
	}

	// A pod in phase Unknown (node lost) must read gray/unknown, not green — the
	// canonical classifier would fall it through to healthy.
	nodeLost := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodUnknown}}
	if got := getPodStatus(nodeLost); got != StatusUnknown {
		t.Errorf("node-lost (phase Unknown) pod node color = %q, want unknown", got)
	}

	// A pod wedged in termination (>10m) reads degraded, not green — topology
	// folds the stuck-terminating signal like the timeline does.
	stuckTerm := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{Time: time.Now().Add(-11 * time.Minute)}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Ready: true}},
		},
	}
	if got := getPodStatus(stuckTerm); got != StatusDegraded {
		t.Errorf("stuck-terminating pod node color = %q, want degraded", got)
	}

	// A stuck-terminating pod that is ALSO crashlooping must stay RED — the
	// terminating/unschedulable floor must not downgrade a real unhealthy to amber.
	stuckCrash := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{Time: time.Now().Add(-11 * time.Minute)}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
			},
		},
	}
	if got := getPodStatus(stuckCrash); got != StatusUnhealthy {
		t.Errorf("stuck-terminating + crashloop pod = %q, want unhealthy (floor must not downgrade)", got)
	}

	if got := podSummaryStatus(nodeLost); got != StatusUnhealthy {
		t.Errorf("node-lost pod summary bucket = %q, want unhealthy (unknown surfaces in rollup)", got)
	}
}

// TestPodSummaryStatusBuckets pins the counter path (distinct from node color): a
// completed/neutral pod must count as healthy, NOT fall through to the unhealthy
// bucket; a crashloop counts unhealthy.
func TestPodSummaryStatusBuckets(t *testing.T) {
	cases := []struct {
		name string
		pod  *corev1.Pod
		want HealthStatus
	}{
		{"crashloop → unhealthy", &corev1.Pod{Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
			},
		}}, StatusUnhealthy},
		{"completed → healthy bucket", &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}, StatusHealthy},
		{"ready → healthy", &corev1.Pod{Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Ready: true}},
		}}, StatusHealthy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := podSummaryStatus(c.pod); got != c.want {
				t.Errorf("podSummaryStatus = %q, want %q", got, c.want)
			}
		})
	}
}

func TestGetPVCStatusPendingIsNeutral(t *testing.T) {
	pending := &corev1.PersistentVolumeClaim{Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}}
	if got := getPVCStatus(pending); got != StatusNeutral {
		t.Errorf("pending PVC = %q, want neutral (was degraded)", got)
	}
	bound := &corev1.PersistentVolumeClaim{Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
	if got := getPVCStatus(bound); got != StatusHealthy {
		t.Errorf("bound PVC = %q, want healthy", got)
	}
}

func TestGetJobStatusViaCanonical(t *testing.T) {
	failed := &batchv1.Job{Status: batchv1.JobStatus{
		Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}},
	}}
	if got := getJobStatus(failed); got != StatusUnhealthy {
		t.Errorf("failed job = %q, want unhealthy", got)
	}
	complete := &batchv1.Job{Status: batchv1.JobStatus{
		Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
	}}
	if got := getJobStatus(complete); got != StatusNeutral {
		t.Errorf("completed job = %q, want neutral", got)
	}
}

// TestExtractNodeStatusCordoned pins that a Ready-but-cordoned node reads degraded
// in topology — matching the node table badge + drawer + Cordoned audit — instead
// of green, which would contradict every other surface for the same node.
func TestExtractNodeStatusCordoned(t *testing.T) {
	ready := corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	if got := extractNodeStatus(ready); got != StatusHealthy {
		t.Errorf("ready node = %q, want healthy", got)
	}
	cordoned := ready
	cordoned.Spec.Unschedulable = true
	if got := extractNodeStatus(cordoned); got != StatusDegraded {
		t.Errorf("ready+cordoned node = %q, want degraded", got)
	}
	notReady := corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}}
	if got := extractNodeStatus(notReady); got != StatusUnhealthy {
		t.Errorf("not-ready node = %q, want unhealthy", got)
	}
}
