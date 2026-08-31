package server

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/health"
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

func TestBuildPodInfosForRevisionAttributesOnlyKnownIdentities(t *testing.T) {
	pods := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "new", Labels: map[string]string{"pod-template-hash": "rev-2"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "old", Labels: map[string]string{"pod-template-hash": "rev-1"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "unknown"}},
	}

	infos := buildPodInfosForRevision(pods, workloadRevisionTarget{label: "pod-template-hash", value: "rev-2"})
	if infos[0].UpdatedRevision == nil || !*infos[0].UpdatedRevision {
		t.Fatalf("new revision attribution = %#v", infos[0])
	}
	if infos[1].UpdatedRevision == nil || *infos[1].UpdatedRevision {
		t.Fatalf("old revision attribution = %#v", infos[1])
	}
	if infos[2].UpdatedRevision != nil {
		t.Fatalf("unknown revision was guessed: %#v", infos[2])
	}
}

func TestLimitWorkloadPodInfosBoundsProblemFirst(t *testing.T) {
	infos := []WorkloadPodInfo{
		{Name: "healthy", HealthLevel: string(health.LevelHealthy)},
		{Name: "degraded-low-restarts", HealthLevel: string(health.LevelDegraded), RestartCount: 1},
		{Name: "unhealthy", HealthLevel: string(health.LevelUnhealthy)},
		{Name: "degraded-high-restarts", HealthLevel: string(health.LevelDegraded), RestartCount: 4},
	}

	limited, truncated := limitWorkloadPodInfos(infos, 3)

	if !truncated {
		t.Fatal("expected response to be truncated")
	}
	want := []string{"unhealthy", "degraded-high-restarts", "degraded-low-restarts"}
	for i, name := range want {
		if limited[i].Name != name {
			t.Fatalf("limited[%d] = %q, want %q; full result %#v", i, limited[i].Name, name, limited)
		}
	}
}

func TestDaemonSetRevisionLookupUsesWorkloadSelector(t *testing.T) {
	uid := types.UID("daemonset-uid")
	daemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "agent", UID: uid},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "agent"}},
		},
	}
	useTestResourceCache(t, fake.NewSimpleClientset(daemonSet))

	controllerRevisionsGVR := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "controllerrevisions"}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		controllerRevisionsGVR: "ControllerRevisionList",
	})
	var selector string
	listCalls := 0
	dynamicClient.PrependReactor("list", "controllerrevisions", func(action clienttesting.Action) (bool, runtime.Object, error) {
		listCalls++
		selector = action.(clienttesting.ListAction).GetListRestrictions().Labels.String()
		return true, &unstructured.UnstructuredList{}, nil
	})

	server := &Server{}
	request := httptest.NewRequest("GET", "/api/workloads/daemonsets/ops/agent/pods", nil)
	server.workloadRevisionTargetForRequest(request, k8s.GetResourceCache(), dynamicClient, "test-context", "daemonsets", "ops", "agent")
	server.workloadRevisionTargetForRequest(request, k8s.GetResourceCache(), dynamicClient, "test-context", "daemonsets", "ops", "agent")
	if selector != "app=agent" {
		t.Fatalf("ControllerRevision selector = %q, want app=agent", selector)
	}
	if listCalls != 1 {
		t.Fatalf("ControllerRevision list calls = %d, want 1 within memo TTL", listCalls)
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

func TestDirectJobRunPreservesNonJobSetLauncher(t *testing.T) {
	controller := true
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      "nightly-abc",
		Namespace: "ci",
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "batch/v1",
			Kind:       "CronJob",
			Name:       "nightly",
			Controller: &controller,
		}},
	}}
	useTestResourceCache(t, fake.NewSimpleClientset(job))

	runs, err := (&Server{}).getWorkloadRuns(context.Background(), "jobs", "ci", "nightly-abc", nil, true)
	if err != nil {
		t.Fatalf("get direct Job run: %v", err)
	}
	if len(runs) != 1 || runs[0].Launcher == nil || runs[0].Launcher.Kind != "CronJob" || runs[0].Launcher.Name != "nightly" {
		t.Fatalf("non-JobSet launcher was lost: %#v", runs)
	}
}

func TestJobSetMemberRunsRequireExactControllerIdentity(t *testing.T) {
	jobSet := testJobSet("training", "distributed", types.UID("jobset-current"))
	controller := true
	member := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "distributed-workers-2",
			Namespace: "training",
			Annotations: map[string]string{
				jobSetRestartAttemptAnnotation:    "1",
				jobSetJobRestartAttemptAnnotation: "2",
			},
			Labels: map[string]string{
				replicatedJobNameLabel:     "workers",
				replicatedJobReplicasLabel: "4",
				jobSetJobIndexLabel:        "2",
				jobSetGlobalReplicasLabel:  "5",
				jobSetJobGlobalIndexLabel:  "3",
				jobSetGroupNameLabel:       "trainers",
				jobSetGroupReplicasLabel:   "4",
				jobSetJobGroupIndexLabel:   "2",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: jobSetAPIVersion,
				Kind:       "JobSet",
				Name:       "distributed",
				UID:        types.UID("jobset-current"),
				Controller: &controller,
			}},
		},
	}
	wrongGroup := member.DeepCopy()
	wrongGroup.Name = "wrong-group"
	wrongGroup.OwnerReferences[0].APIVersion = "example.io/v1alpha2"
	staleUID := member.DeepCopy()
	staleUID.Name = "stale-uid"
	staleUID.OwnerReferences[0].UID = types.UID("jobset-deleted")
	nonController := member.DeepCopy()
	nonController.Name = "not-controller"
	nonController.OwnerReferences[0].Controller = nil
	labelOnly := member.DeepCopy()
	labelOnly.Name = "label-only"
	labelOnly.OwnerReferences = nil
	missingLabels := member.DeepCopy()
	missingLabels.Name = "distributed-chief-0"
	missingLabels.Labels = nil

	result := jobSetMemberRuns(jobSet, []*batchv1.Job{wrongGroup, staleUID, nonController, labelOnly, member, missingLabels})

	if result.Total != 2 || result.Truncated || len(result.Runs) != 2 {
		t.Fatalf("unexpected member bounds: %#v", result)
	}
	var got *WorkloadRun
	for i := range result.Runs {
		if result.Runs[i].Name == member.Name {
			got = &result.Runs[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("owned member missing from %#v", result.Runs)
	}
	if got.ReplicatedJob != "workers" || got.ReplicatedJobReplicas != "4" || got.JobIndex != "2" || got.GlobalReplicas != "5" || got.GlobalIndex != "3" {
		t.Fatalf("unexpected role/index metadata: %#v", got)
	}
	if got.GroupName != "trainers" || got.GroupReplicas != "4" || got.GroupIndex != "2" {
		t.Fatalf("unexpected group metadata: %#v", got)
	}
	if got.RestartAttempt != "1" || got.JobRestartAttempt != "2" {
		t.Fatalf("unexpected restart metadata: %#v", got)
	}
	if got.Launcher == nil || got.Launcher.Kind != "JobSet" || got.Launcher.Group != "jobset.x-k8s.io" || got.Launcher.Name != "distributed" {
		t.Fatalf("unexpected JobSet backlink: %#v", got.Launcher)
	}
	missingUID := jobSet.DeepCopy()
	missingUID.SetUID("")
	if got := jobSetMemberRuns(missingUID, []*batchv1.Job{member}); got.Total != 0 {
		t.Fatalf("JobSet without authoritative UID matched %d members", got.Total)
	}
}

func TestJobSetLauncherRequiresValidatedLiveParent(t *testing.T) {
	jobSet := testJobSet("training", "distributed", types.UID("jobset-current"))
	controller := true
	member := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      "distributed-workers-0",
		Namespace: "training",
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: jobSetAPIVersion,
			Kind:       "JobSet",
			Name:       "distributed",
			UID:        types.UID("jobset-current"),
			Controller: &controller,
		}},
	}}

	launcher := jobSetLauncher(jobSet, member)
	if launcher == nil || launcher.Kind != "JobSet" || launcher.Name != "distributed" || launcher.Group != "jobset.x-k8s.io" {
		t.Fatalf("unexpected validated launcher: %#v", launcher)
	}

	stale := member.DeepCopy()
	stale.OwnerReferences[0].UID = types.UID("jobset-old")
	if launcher := jobSetLauncher(jobSet, stale); launcher != nil {
		t.Fatalf("stale parent UID produced launcher: %#v", launcher)
	}
}

func TestJobRunInfoDoesNotGuessJobSetBacklinkWithoutValidatedRoot(t *testing.T) {
	controller := true
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      "orphaned-member",
		Namespace: "training",
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: jobSetAPIVersion,
			Kind:       "JobSet",
			Name:       "recreated",
			UID:        "deleted-root",
			Controller: &controller,
		}},
	}}

	if launcher := jobRunInfo(job).Launcher; launcher != nil {
		t.Fatalf("unvalidated JobSet backlink = %#v, want nil", launcher)
	}
}

func TestJobSetMemberRunsAreBoundedAndProblemFirst(t *testing.T) {
	jobSet := testJobSet("training", "wide", types.UID("wide-uid"))
	controller := true
	jobs := make([]*batchv1.Job, 0, maxJobSetMemberRuns+1)
	for i := 0; i <= maxJobSetMemberRuns; i++ {
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("wide-worker-%03d", i),
			Namespace: "training",
			Labels: map[string]string{
				replicatedJobNameLabel: "workers",
				jobSetJobIndexLabel:    strconv.Itoa(i),
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: jobSetAPIVersion,
				Kind:       "JobSet",
				Name:       "wide",
				UID:        types.UID("wide-uid"),
				Controller: &controller,
			}},
		}}
		if i == maxJobSetMemberRuns {
			job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
		}
		jobs = append(jobs, job)
	}

	result := jobSetMemberRuns(jobSet, jobs)

	if result.Total != maxJobSetMemberRuns+1 || !result.Truncated || len(result.Runs) != maxJobSetMemberRuns {
		t.Fatalf("unexpected member bounds: total=%d returned=%d truncated=%v", result.Total, len(result.Runs), result.Truncated)
	}
	if result.Runs[0].Name != "wide-worker-200" || result.Runs[0].Phase != "Failed" {
		t.Fatalf("first member = %#v, want failed member", result.Runs[0])
	}
}

func TestJobSetMemberRunsPutTerminatingAttemptsAfterReplacements(t *testing.T) {
	jobSet := testJobSet("training", "restarting", types.UID("restarting-uid"))
	controller := true
	owner := metav1.OwnerReference{
		APIVersion: jobSetAPIVersion,
		Kind:       "JobSet",
		Name:       "restarting",
		UID:        types.UID("restarting-uid"),
		Controller: &controller,
	}
	deletionTime := metav1.Now()
	old := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:              "workers-old",
		Namespace:         "training",
		DeletionTimestamp: &deletionTime,
		Labels:            map[string]string{replicatedJobNameLabel: "workers", jobSetJobIndexLabel: "0"},
		OwnerReferences:   []metav1.OwnerReference{owner},
	}}
	replacement := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:            "workers-new",
		Namespace:       "training",
		Labels:          map[string]string{replicatedJobNameLabel: "workers", jobSetJobIndexLabel: "0"},
		Annotations:     map[string]string{jobSetRestartAttemptAnnotation: "1"},
		OwnerReferences: []metav1.OwnerReference{owner},
	}}

	result := jobSetMemberRuns(jobSet, []*batchv1.Job{old, replacement})

	if result.Runs[0].Name != "workers-new" || result.Runs[1].Phase != "Terminating" || result.Runs[1].Active {
		t.Fatalf("unexpected restart ordering: %#v", result.Runs)
	}
}

func TestSupportedJobSetRequiresV1Alpha2GVK(t *testing.T) {
	if !isSupportedJobSet(testJobSet("training", "supported", "uid")) {
		t.Fatal("expected v1alpha2 JobSet to be supported")
	}
	future := testJobSet("training", "future", "uid")
	future.SetAPIVersion("jobset.x-k8s.io/v1beta1")
	if isSupportedJobSet(future) {
		t.Fatal("future JobSet version was treated as supported")
	}
	foreign := testJobSet("training", "foreign", "uid")
	foreign.SetAPIVersion("example.io/v1alpha2")
	if isSupportedJobSet(foreign) {
		t.Fatal("foreign same-kind resource was treated as supported")
	}
}

func testJobSet(namespace, name string, uid types.UID) *unstructured.Unstructured {
	jobSet := &unstructured.Unstructured{}
	jobSet.SetAPIVersion(jobSetAPIVersion)
	jobSet.SetKind("JobSet")
	jobSet.SetNamespace(namespace)
	jobSet.SetName(name)
	jobSet.SetUID(uid)
	return jobSet
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
	if got := workloadSelectorGetError(fmt.Errorf("rollout selector: %w", k8s.ErrWorkloadSelectorUnavailable)); got.statusCode != 400 {
		t.Fatalf("invalid selector statusCode = %d, want 400", got.statusCode)
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
