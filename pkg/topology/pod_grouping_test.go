package topology

import (
	"encoding/json"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGroupPodsKeepsWorkflowRunsSeparate(t *testing.T) {
	pods := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "run-a-1", Labels: map[string]string{"app.kubernetes.io/name": "migration", "workflows.argoproj.io/workflow": "run-a"}}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "run-b-1", Labels: map[string]string{"app.kubernetes.io/name": "migration", "workflows.argoproj.io/workflow": "run-b"}}},
	}

	groups := GroupPods(pods, PodGroupingOptions{}).Groups
	if len(groups) != 2 {
		t.Fatalf("expected one pod group per Workflow run, got %d", len(groups))
	}
	if groups["dev/Workflow/run-a"] == nil || groups["dev/Workflow/run-b"] == nil {
		t.Fatalf("expected Workflow run groups, got %#v", groups)
	}
}

func TestGroupPodsKeepsJobRunsSeparate(t *testing.T) {
	pods := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "job-a-1", Labels: map[string]string{"app": "reporting", "job-name": "job-a"}}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "job-b-1", Labels: map[string]string{"app": "reporting", "batch.kubernetes.io/job-name": "job-b"}}},
	}

	groups := GroupPods(pods, PodGroupingOptions{}).Groups
	if len(groups) != 2 {
		t.Fatalf("expected one pod group per Job run, got %d", len(groups))
	}
	if groups["dev/Job/job-a"] == nil || groups["dev/Job/job-b"] == nil {
		t.Fatalf("expected Job run groups, got %#v", groups)
	}
}

// Listers hand GroupPods pods in map order. The pod-group node must not
// depend on it, or an unchanged cluster serializes differently on every build
// and the SSE stream re-sends a graph the client already has.
func TestPodGroupNodeIgnoresPodOrder(t *testing.T) {
	var pods []*corev1.Pod
	for _, name := range []string{"web-c", "web-a", "web-d", "web-b"} {
		pods = append(pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: name, Labels: map[string]string{"app": "web"}}})
	}
	node := func(pods []*corev1.Pod) string {
		groups := GroupPods(pods, PodGroupingOptions{}).Groups
		if len(groups) != 1 {
			t.Fatalf("expected one group, got %d", len(groups))
		}
		for _, g := range groups {
			b, err := json.Marshal(CreatePodGroupNode(g, nil))
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		return ""
	}
	want := node(pods)
	reversed := slices.Clone(pods)
	slices.Reverse(reversed)
	if got := node(reversed); got != want {
		t.Fatalf("pod-group node depends on pod order:\n got %s\nwant %s", got, want)
	}
}
