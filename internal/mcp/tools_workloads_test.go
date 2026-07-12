package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
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
	if !strings.Contains(metadata.Message, "finished") || !strings.Contains(metadata.Message, "conditions and events") {
		t.Fatalf("Message = %q, want terminal inspection guidance", metadata.Message)
	}
	if metadata.Command != "kubectl describe job/nightly -n ci" {
		t.Fatalf("Command = %q", metadata.Command)
	}
}

func TestWorkloadSelectorMCPError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "missing lister is forbidden",
			err:  fmt.Errorf("%w: list jobs", k8s.ErrWorkloadAccessDenied),
			want: "forbidden: cannot access job ci/nightly",
		},
		{
			name: "kubernetes forbidden is explicit",
			err: apierrors.NewForbidden(
				schema.GroupResource{Group: "batch", Resource: "jobs"},
				"nightly",
				fmt.Errorf("denied"),
			),
			want: "forbidden: cannot access job ci/nightly",
		},
		{
			name: "not found is actionable",
			err: apierrors.NewNotFound(
				schema.GroupResource{Group: "batch", Resource: "jobs"},
				"nightly",
			),
			want: "resource not found:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := workloadSelectorMCPError(context.Background(), tt.err, "job", "ci", "nightly")
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err, tt.want)
			}
		})
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
