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
	"github.com/skyhook-io/radar/pkg/k8score"
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
		{
			name: "invalid selector is explicit",
			err:  fmt.Errorf("rollout selector: %w", k8s.ErrWorkloadSelectorUnavailable),
			want: "invalid job ci/nightly:",
		},
		{
			name: "a workload the synced cache does not hold is not found",
			err:  fmt.Errorf("%w: Job ci/nightly", k8s.ErrWorkloadNotFound),
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

func TestWorkloadSelectorMCPErrorNamesAnAbsentRolloutCRD(t *testing.T) {
	err := workloadSelectorMCPError(context.Background(), fmt.Errorf("%w: read Rollout", k8s.ErrUnknownDynamicKind), "rollouts", "shop", "api")
	if err.Error() != "Rollout (argoproj.io) is not installed or not yet discovered in this cluster" {
		t.Fatalf("error = %q", err)
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

func TestDescribeNodeDrain(t *testing.T) {
	daemonSetSkip := k8score.PodDrainDecision{Namespace: "kube-system", Name: "agent", Outcome: k8score.DrainOutcomeSkip, Reason: "managed by a DaemonSet", DaemonSet: true}
	bareSkip := k8score.PodDrainDecision{Namespace: "shop", Name: "debug", Outcome: k8score.DrainOutcomeSkip, Reason: "not managed by a controller"}

	tests := []struct {
		name        string
		result      k8s.DrainResult
		wantStatus  string
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:        "a skipped bare pod is still on the node",
			result:      k8s.DrainResult{EvictedPods: []string{"shop/web-1"}, SkippedPods: []k8score.PodDrainDecision{bareSkip}, WaitedForDeletion: true},
			wantStatus:  "partial",
			wantContain: []string{"1 pods skipped and still on the node", "do not take it down"},
			wantAbsent:  []string{"confirmed gone"},
		},
		{
			name:        "DaemonSet pods do not stop the node being drained",
			result:      k8s.DrainResult{EvictedPods: []string{"shop/web-1"}, SkippedPods: []k8score.PodDrainDecision{daemonSetSkip}, WaitedForDeletion: true},
			wantStatus:  "ok",
			wantContain: []string{"1 pods evicted and confirmed gone", "1 DaemonSet pods left in place"},
		},
		{
			name:        "failed evictions are partial",
			result:      k8s.DrainResult{EvictedPods: []string{"shop/web-1"}, Errors: []string{"shop/db-1: refused"}, WaitedForDeletion: true},
			wantStatus:  "partial",
			wantContain: []string{"1 evictions failed"},
		},
		{
			name:        "pending pods are partial",
			result:      k8s.DrainResult{EvictedPods: []string{"shop/web-1"}, PendingPods: []string{"shop/web-1"}, WaitedForDeletion: true},
			wantStatus:  "partial",
			wantContain: []string{"1 evicted pods still on the node"},
		},
		{
			name:        "nothing to evict says so",
			result:      k8s.DrainResult{WaitedForDeletion: false},
			wantStatus:  "ok",
			wantContain: []string{"nothing to evict"},
			wantAbsent:  []string{"may still be terminating", "0 evictions"},
		},
		{
			name:        "not waiting keeps the terminating caveat",
			result:      k8s.DrainResult{EvictedPods: []string{"shop/web-1"}},
			wantStatus:  "ok",
			wantContain: []string{"1 evictions accepted; pods may still be terminating"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, msg := describeNodeDrain("worker-1", &tt.result)
			if status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (message %q)", status, tt.wantStatus, msg)
			}
			for _, want := range tt.wantContain {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q missing %q", msg, want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(msg, absent) {
					t.Errorf("message %q must not contain %q", msg, absent)
				}
			}
		})
	}
}

func TestSkippedPodsForMCPKeepsReasons(t *testing.T) {
	got := skippedPodsForMCP([]k8score.PodDrainDecision{{Namespace: "shop", Name: "debug", Reason: "not managed by a controller"}})
	if len(got) != 1 || got[0].Pod != "shop/debug" || got[0].Reason != "not managed by a controller" {
		t.Fatalf("got %+v", got)
	}
	if got := skippedPodsForMCP(nil); got == nil || len(got) != 0 {
		t.Fatalf("no skips must serialize as an empty list, got %#v", got)
	}
}
