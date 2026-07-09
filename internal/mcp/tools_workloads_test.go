package mcp

import (
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestApplyMCPTerminalJobEmptyState(t *testing.T) {
	metadata := mcpWorkloadLogEmptyMetadata{
		Reason:  "no-pods",
		Message: "No pods found for this Job yet.",
		Command: "kubectl logs job/nightly -n ci",
	}
	job := &batchv1.Job{
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	applyMCPTerminalJobEmptyState(&metadata, job, "ci", "nightly")

	if metadata.Reason != "pods-gone" {
		t.Fatalf("Reason = %q, want pods-gone", metadata.Reason)
	}
	if !strings.Contains(metadata.Message, "finished") || !strings.Contains(metadata.Message, "kubectl logs job/nightly -n ci") {
		t.Fatalf("Message = %q, want terminal kubectl guidance", metadata.Message)
	}
	if metadata.Command != "kubectl logs job/nightly -n ci" {
		t.Fatalf("Command = %q", metadata.Command)
	}
}

func TestApplyMCPTerminalWorkflowEmptyStateWithArchiveLogs(t *testing.T) {
	metadata := mcpWorkloadLogEmptyMetadata{
		Reason:  "no-pods",
		Message: "No Workflow pods found yet.",
		Command: "argo logs nightly -n ci",
	}
	workflow := map[string]any{
		"status": map[string]any{
			"phase": "Succeeded",
		},
		"spec": map[string]any{
			"archiveLogs": true,
		},
	}

	applyMCPTerminalWorkflowEmptyState(&metadata, workflow, "ci", "nightly")

	if metadata.Reason != "pods-gone" {
		t.Fatalf("Reason = %q, want pods-gone", metadata.Reason)
	}
	if !strings.Contains(metadata.Message, "Archived logs") || !strings.Contains(metadata.Message, "argo logs nightly -n ci") {
		t.Fatalf("Message = %q, want archived-log guidance", metadata.Message)
	}
	if metadata.Command != "argo logs nightly -n ci" {
		t.Fatalf("Command = %q", metadata.Command)
	}
}

func TestMCPJobConditionIgnoresRetryCounters(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "retrying",
			Namespace: "ci",
		},
		Status: batchv1.JobStatus{
			Failed: 1,
		},
	}

	if _, ok := mcpJobCondition(job, batchv1.JobFailed); ok {
		t.Fatal("mcpJobCondition treated failed counter as terminal condition")
	}
}
