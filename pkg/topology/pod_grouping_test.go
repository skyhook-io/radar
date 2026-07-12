package topology

import (
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
