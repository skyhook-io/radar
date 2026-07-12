package server

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestSortRunsPrefersActiveThenNewest(t *testing.T) {
	runs := []WorkloadRun{
		{Name: "success-new", Phase: "Succeeded", StartedAt: "2026-01-03T00:00:00Z"},
		{Name: "failed-old", Phase: "Failed", StartedAt: "2026-01-01T00:00:00Z"},
		{Name: "active-old", Phase: "Running", Active: true, StartedAt: "2025-12-31T00:00:00Z"},
		{Name: "failed-new", Phase: "Failed", StartedAt: "2026-01-02T00:00:00Z"},
	}

	sortRuns(runs)

	got := []string{runs[0].Name, runs[1].Name, runs[2].Name, runs[3].Name}
	want := []string{"active-old", "success-new", "failed-new", "failed-old"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order[%d] = %q, want %q; full order %v", i, got[i], want[i], got)
		}
	}
}

func TestSortRunsUsesNewestRunTimestamp(t *testing.T) {
	runs := []WorkloadRun{
		{Name: "started-newer", Phase: "Succeeded", StartedAt: "2026-01-03T00:00:00Z"},
		{Name: "finished-newer", Phase: "Succeeded", StartedAt: "2026-01-01T00:00:00Z", FinishedAt: "2026-01-04T00:00:00Z"},
	}

	sortRuns(runs)

	if got := runs[0].Name; got != "finished-newer" {
		t.Fatalf("first run = %q, want finished-newer", got)
	}
}

func TestWorkflowRunInfo(t *testing.T) {
	workflow := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":      "nightly-abc",
			"namespace": "ci",
			"annotations": map[string]any{
				"workflows.argoproj.io/scheduled-time": "2026-01-02T03:04:05Z",
			},
		},
		"status": map[string]any{
			"phase":      "Failed",
			"startedAt":  "2026-01-02T03:04:06Z",
			"finishedAt": "2026-01-02T03:05:06Z",
			"message":    "template failed",
		},
	}}

	run := workflowRunInfo(workflow)
	if run.Kind != "workflows" || run.Namespace != "ci" || run.Name != "nightly-abc" {
		t.Fatalf("unexpected identity: %#v", run)
	}
	if run.Phase != "Failed" || run.Active {
		t.Fatalf("unexpected phase/active: %#v", run)
	}
	if run.ScheduledAt != "2026-01-02T03:04:05Z" || run.Message != "template failed" {
		t.Fatalf("unexpected schedule/message: %#v", run)
	}
}

func TestApplyTerminalWorkflowEmptyStateWithoutNodes(t *testing.T) {
	metadata := workloadLogMetadata{
		EmptyReason:  "no-pods",
		EmptyMessage: "No Workflow pods found yet.",
		Command:      "argo logs finished -n ci",
	}

	applyTerminalWorkflowEmptyState(&metadata, map[string]any{
		"status": map[string]any{
			"phase": "Succeeded",
		},
	}, "ci", "finished")

	if metadata.EmptyReason != "pods-gone" {
		t.Fatalf("EmptyReason = %q, want pods-gone", metadata.EmptyReason)
	}
	if strings.Contains(metadata.EmptyMessage, "yet") {
		t.Fatalf("terminal workflow kept not-started message: %q", metadata.EmptyMessage)
	}
}

func TestApplyTerminalJobEmptyStateIgnoresRetryCounters(t *testing.T) {
	metadata := workloadLogMetadata{
		EmptyReason:  "no-pods",
		EmptyMessage: "No pods found for this Job yet.",
		Command:      "kubectl logs job/retrying -n ci",
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "retrying",
			Namespace: "ci",
		},
		Status: batchv1.JobStatus{
			Failed: 1,
		},
	}

	applyTerminalJobEmptyState(&metadata, job, "ci", "retrying")

	if metadata.EmptyReason != "no-pods" {
		t.Fatalf("EmptyReason = %q, want no-pods", metadata.EmptyReason)
	}
	if strings.Contains(metadata.EmptyMessage, "finished") {
		t.Fatalf("retrying job got terminal message: %q", metadata.EmptyMessage)
	}
}

func TestApplyTerminalJobEmptyStateUsesDescribeCommand(t *testing.T) {
	metadata := workloadLogMetadata{EmptyReason: "no-pods", Command: "kubectl logs job/nightly -n ci"}
	job := &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}}

	applyTerminalJobEmptyState(&metadata, job, "ci", "nightly")

	if metadata.EmptyReason != "pods-gone" || metadata.Command != "kubectl describe job/nightly -n ci" {
		t.Fatalf("unexpected terminal metadata: %#v", metadata)
	}
}

func TestJobRunInfoUsesTerminalConditions(t *testing.T) {
	startedAt := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	finishedAt := metav1.NewTime(time.Date(2026, 1, 2, 3, 5, 5, 0, time.UTC))
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "retry-then-pass",
			Namespace: "ci",
		},
		Status: batchv1.JobStatus{
			StartTime: &startedAt,
			Failed:    1,
			Succeeded: 1,
			Conditions: []batchv1.JobCondition{
				{
					Type:               batchv1.JobComplete,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: finishedAt,
					Message:            "completed after retry",
				},
			},
		},
	}

	run := jobRunInfo(job)
	if run.Phase != "Succeeded" || run.Active {
		t.Fatalf("unexpected phase/active: %#v", run)
	}
	if run.FinishedAt != "2026-01-02T03:05:05Z" || run.Message != "completed after retry" {
		t.Fatalf("unexpected finished/message: %#v", run)
	}
}

func TestJobRunInfoDoesNotTreatCountersAsTerminal(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "retrying",
			Namespace: "ci",
		},
		Status: batchv1.JobStatus{
			Failed: 1,
		},
	}

	run := jobRunInfo(job)

	if run.Phase != "Pending" || !run.Active {
		t.Fatalf("unexpected phase/active: %#v", run)
	}
}

func TestJobRunInfoTreatsPendingAsActive(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "not-started",
			Namespace: "ci",
		},
	}

	run := jobRunInfo(job)

	if run.Phase != "Pending" || !run.Active {
		t.Fatalf("unexpected phase/active: %#v", run)
	}
}

func TestJobRunInfoSuspendedAndLauncher(t *testing.T) {
	suspended := true
	controller := true
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "queue-worker-abc",
			Namespace:       "ci",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ScaledJob", Name: "queue-worker", Controller: &controller}},
		},
		Spec: batchv1.JobSpec{Suspend: &suspended},
	}

	run := jobRunInfo(job)
	if run.Phase != "Suspended" || run.Active {
		t.Fatalf("unexpected suspended phase/active: %#v", run)
	}
	if run.Trigger != "event" || run.Launcher == nil || run.Launcher.Kind != "ScaledJob" || run.Launcher.Name != "queue-worker" || run.Launcher.Group != "keda.sh" {
		t.Fatalf("unexpected launcher: %#v", run)
	}
}

func TestWorkflowRunInfoIncludesCronWorkflowLauncher(t *testing.T) {
	controller := true
	workflow := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":      "nightly-abc",
			"namespace": "ci",
			"ownerReferences": []any{map[string]any{
				"apiVersion": "argoproj.io/v1alpha1",
				"kind":       "CronWorkflow",
				"name":       "nightly",
				"uid":        "abc",
				"controller": controller,
			}},
		},
	}}

	run := workflowRunInfo(workflow)
	if run.Launcher == nil || run.Launcher.Kind != "CronWorkflow" || run.Launcher.Namespace != "ci" || run.Launcher.Name != "nightly" {
		t.Fatalf("unexpected launcher: %#v", run)
	}
}

func TestWorkflowRunInfoUsesCronWorkflowLabelLauncher(t *testing.T) {
	workflow := &unstructured.Unstructured{}
	workflow.SetName("nightly-abc")
	workflow.SetNamespace("ci")
	workflow.SetLabels(map[string]string{"workflows.argoproj.io/cron-workflow": "nightly"})

	run := workflowRunInfo(workflow)
	if run.Launcher == nil || run.Launcher.Kind != "CronWorkflow" || run.Launcher.Namespace != "ci" || run.Launcher.Name != "nightly" {
		t.Fatalf("unexpected launcher: %#v", run)
	}
}

func TestJobRunInfoDistinguishesManualAndScheduledCronRuns(t *testing.T) {
	startedAt := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	manual := jobRunInfo(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nightly-manual-1",
			Namespace: "ci",
			Annotations: map[string]string{
				"cronjob.kubernetes.io/instantiate":               "manual",
				"batch.kubernetes.io/cronjob-scheduled-timestamp": "2026-01-02T02:00:00Z",
			},
		},
		Status: batchv1.JobStatus{StartTime: &startedAt},
	})
	if manual.Trigger != "manual" || manual.ScheduledAt != "" {
		t.Fatalf("manual run trigger/schedule = %q/%q, want manual/empty", manual.Trigger, manual.ScheduledAt)
	}

	scheduled := jobRunInfo(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nightly-123",
			Namespace: "ci",
			Annotations: map[string]string{
				"batch.kubernetes.io/cronjob-scheduled-timestamp": "2026-01-02T02:00:00Z",
			},
		},
		Status: batchv1.JobStatus{StartTime: &startedAt},
	})
	if scheduled.Trigger != "schedule" || scheduled.ScheduledAt != "2026-01-02T02:00:00Z" {
		t.Fatalf("scheduled run trigger/schedule = %q/%q, want schedule/timestamp", scheduled.Trigger, scheduled.ScheduledAt)
	}
}

func TestFormatMetaTime(t *testing.T) {
	timestamp := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if got := formatMetaTime(&timestamp); got != "2026-01-02T03:04:05Z" {
		t.Fatalf("formatMetaTime() = %q", got)
	}
	if got := formatMetaTime(nil); got != "" {
		t.Fatalf("formatMetaTime(nil) = %q", got)
	}
}

func TestWorkloadParentGetErrorPreservesForbidden(t *testing.T) {
	err := apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "cronjobs"}, "nightly", nil)

	got := workloadParentGetError("cronjob", "ci", "nightly", err)

	if got.statusCode != 403 {
		t.Fatalf("statusCode = %d, want 403", got.statusCode)
	}
	if !strings.Contains(got.message, "insufficient permissions") {
		t.Fatalf("message = %q, want insufficient permissions", got.message)
	}
}

func TestWorkloadParentGetErrorClassifiesNotFoundAndUnexpectedErrors(t *testing.T) {
	notFound := workloadParentGetError("workflow", "ci", "nightly", fmt.Errorf("cache lookup: %w", k8score.ErrResourceNotFound))
	if notFound.statusCode != 404 {
		t.Fatalf("not found statusCode = %d, want 404", notFound.statusCode)
	}

	unexpected := workloadParentGetError("workflow", "ci", "nightly", errors.New("cache unavailable"))
	if unexpected.statusCode != 500 {
		t.Fatalf("unexpected statusCode = %d, want 500", unexpected.statusCode)
	}
	if !strings.Contains(unexpected.message, "cache unavailable") {
		t.Fatalf("message = %q, want original error context", unexpected.message)
	}
}

func TestWorkloadSelectorGetErrorPreservesKubernetesStatus(t *testing.T) {
	forbidden := fmt.Errorf("workflow dev/migration: %w", apierrors.NewForbidden(schema.GroupResource{Group: "argoproj.io", Resource: "workflows"}, "migration", nil))
	notFound := fmt.Errorf("job dev/nightly: %w", apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, "nightly"))

	if got := workloadSelectorGetError(forbidden); got.statusCode != 403 {
		t.Fatalf("forbidden statusCode = %d, want 403", got.statusCode)
	}
	if got := workloadSelectorGetError(notFound); got.statusCode != 404 {
		t.Fatalf("not found statusCode = %d, want 404", got.statusCode)
	}
	if got := workloadSelectorGetError(fmt.Errorf("workflow dev/migration: %w", k8score.ErrResourceNotFound)); got.statusCode != 404 {
		t.Fatalf("dynamic not found statusCode = %d, want 404", got.statusCode)
	}
	if got := workloadSelectorGetError(fmt.Errorf("%w: list jobs", k8s.ErrWorkloadAccessDenied)); got.statusCode != 403 {
		t.Fatalf("cache permission statusCode = %d, want 403", got.statusCode)
	}
}

func TestShouldWaitForPodsInLogStream(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		metadata workloadLogMetadata
		want     bool
	}{
		{
			name: "pending job",
			kind: "jobs",
			metadata: workloadLogMetadata{
				EmptyReason: "no-pods",
			},
			want: true,
		},
		{
			name: "pending workflow",
			kind: "workflow",
			metadata: workloadLogMetadata{
				EmptyReason: "no-pods",
			},
			want: true,
		},
		{
			name: "terminal job pods gone",
			kind: "jobs",
			metadata: workloadLogMetadata{
				EmptyReason: "pods-gone",
			},
			want: false,
		},
		{
			name: "deployment ends without pods",
			kind: "deployments",
			metadata: workloadLogMetadata{
				EmptyReason: "no-pods",
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldWaitForPodsInLogStream(tc.kind, tc.metadata); got != tc.want {
				t.Fatalf("shouldWaitForPodsInLogStream() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWorkloadLogEndPayloadIncludesEmptyMetadata(t *testing.T) {
	got := workloadLogEndPayload(workloadLogMetadata{
		EmptyReason:  "pods-gone",
		EmptyMessage: "finished and pods were removed",
		Command:      "kubectl logs job/nightly -n ci",
	})

	if got["reason"] != "pods-gone" || got["emptyReason"] != "pods-gone" {
		t.Fatalf("reason fields = %#v, want pods-gone", got)
	}
	if got["emptyMessage"] != "finished and pods were removed" {
		t.Fatalf("emptyMessage = %q", got["emptyMessage"])
	}
	if got["command"] != "kubectl logs job/nightly -n ci" {
		t.Fatalf("command = %q", got["command"])
	}
}
