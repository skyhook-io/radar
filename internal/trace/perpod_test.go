package trace

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestPodsConfig_PerPodRoster pins that the Pods hop emits a roster of EVERY
// selected pod - the ready one with its IP (so its probe joins), and the crashing
// one with a plain-English cause (so the grid shows it even though nothing probed
// it). Without the roster the grid can only show the ready sample and misses the
// exact pod that is broken.
func TestPodsConfig_PerPodRoster(t *testing.T) {
	ready := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ready-1", Namespace: "prod"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Ports: []corev1.ContainerPort{{ContainerPort: 8080}}}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			PodIP:      "10.0.0.1",
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	crashing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-1", Namespace: "prod"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}},
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
		},
	}

	c := podsConfig([]*corev1.Pod{ready, crashing}, &corev1.Service{})
	if c == nil {
		t.Fatal("podsConfig returned nil")
	}
	if len(c.Pods) != 2 {
		t.Fatalf("roster len = %d, want 2 (ready AND crashing)", len(c.Pods))
	}
	byName := map[string]PodStatus{}
	for _, p := range c.Pods {
		byName[p.Name] = p
	}
	if !byName["ready-1"].Ready || byName["ready-1"].IP != "10.0.0.1" {
		t.Errorf("ready-1 = %+v, want ready with IP 10.0.0.1", byName["ready-1"])
	}
	if byName["bad-1"].Ready {
		t.Errorf("bad-1 should be NotReady")
	}
	if !strings.Contains(strings.ToLower(byName["bad-1"].Reason), "crash") {
		t.Errorf("bad-1 reason should name the crashloop cause, got %q", byName["bad-1"].Reason)
	}
}

// TestPodsConfig_CulpritNeverSampledOut pins the incident-critical invariant: a
// not-ready pod is ALWAYS in the roster (even when it sorts past the sample cap),
// while ready pods are capped to the probe sample so no row reads a useless "not
// tested". PodTotal carries the real count for the "N of total" disclosure.
func TestPodsConfig_CulpritNeverSampledOut(t *testing.T) {
	var pods []*corev1.Pod
	for _, n := range []string{"a", "b", "c", "d", "e"} { // 5 ready, all sort BEFORE the culprit
		pods = append(pods, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: n},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				PodIP:      "10.0.0.9",
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			},
		})
	}
	pods = append(pods, &corev1.Pod{ // the culprit sorts LAST by name
		ObjectMeta: metav1.ObjectMeta{Name: "zzz-crash"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}},
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
		},
	})

	c := podsConfig(pods, &corev1.Service{})
	if c == nil {
		t.Fatal("podsConfig returned nil")
	}
	if c.PodTotal != 6 {
		t.Errorf("PodTotal = %d, want 6 (the true count)", c.PodTotal)
	}
	hasCulprit, readyCount := false, 0
	for _, p := range c.Pods {
		if p.Name == "zzz-crash" {
			hasCulprit = true
		}
		if p.Ready {
			readyCount++
		}
	}
	if !hasCulprit {
		t.Error("the not-ready culprit must be in the roster, never sampled out by name order")
	}
	if readyCount > maxPodsToProbe {
		t.Errorf("ready pods in roster = %d, want <= maxPodsToProbe (%d)", readyCount, maxPodsToProbe)
	}
}
