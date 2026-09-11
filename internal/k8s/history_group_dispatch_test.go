package k8s

import (
	"context"
	"testing"

	"github.com/skyhook-io/radar/internal/timeline"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestComputeDiff_CoreJobKeepsAuditedDiffer(t *testing.T) {
	oldJob := &batchv1.Job{Status: batchv1.JobStatus{Active: 1}}
	newJob := oldJob.DeepCopy()
	newJob.Status.Active = 2

	for _, test := range []struct {
		name string
		old  any
		new  any
	}{
		{name: "typed", old: oldJob, new: newJob},
		{name: "matching unstructured", old: jobAsUnstructured(t, oldJob), new: jobAsUnstructured(t, newJob)},
	} {
		t.Run(test.name, func(t *testing.T) {
			diff := ComputeDiff("Job", test.old, test.new)
			if diff == nil || !containsPath(diff, "status.active") {
				t.Fatalf("core batch Job lost its audited diff: %+v", diff)
			}
		})
	}

	diff := ComputeDiffFromUnstructured("Job", jobAsUnstructured(t, oldJob), jobAsUnstructured(t, newJob))
	if diff == nil || !containsPath(diff, "status.active") {
		t.Fatalf("unstructured entry point lost the core Job audited diff: %+v", diff)
	}
}

func TestComputeDiff_CollidingCRDsUseGenericDiffer(t *testing.T) {
	tests := []struct {
		name       string
		apiVersion string
		kind       string
		oldObject  map[string]any
		newObject  map[string]any
	}{
		{
			name:       "Volcano Job",
			apiVersion: "batch.volcano.sh/v1alpha1",
			kind:       "Job",
			oldObject:  map[string]any{"status": map[string]any{"state": map[string]any{"phase": "Pending"}}},
			newObject:  map[string]any{"status": map[string]any{"state": map[string]any{"phase": "Running"}}},
		},
		{
			name:       "Istio Gateway",
			apiVersion: "networking.istio.io/v1",
			kind:       "Gateway",
			oldObject:  map[string]any{"spec": map[string]any{"servers": []any{}}},
			newObject:  map[string]any{"spec": map[string]any{"servers": []any{map[string]any{"port": map[string]any{"number": int64(443)}}}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldObject := collisionObject(test.apiVersion, test.kind, test.oldObject)
			newObject := collisionObject(test.apiVersion, test.kind, test.newObject)
			diff := ComputeDiff(test.kind, oldObject, newObject)
			if diff == nil || !containsPath(diff, "resource") {
				t.Fatalf("colliding CRD update did not use the generic differ: %+v", diff)
			}
			fromUnstructured := ComputeDiffFromUnstructured(test.kind, oldObject, newObject)
			if fromUnstructured == nil || !containsPath(fromUnstructured, "resource") {
				t.Fatalf("unstructured entry point did not use the generic differ: %+v", fromUnstructured)
			}
		})
	}
}

func TestComputeDiff_MatchingAuditedCRDKeepsCuratedDiffer(t *testing.T) {
	oldApplication := collisionObject("argoproj.io/v1alpha1", "Application", map[string]any{
		"status": map[string]any{"sync": map[string]any{"status": "Synced"}},
	})
	newApplication := collisionObject("argoproj.io/v1alpha1", "Application", map[string]any{
		"status": map[string]any{"sync": map[string]any{"status": "OutOfSync"}},
	})

	diff := ComputeDiff("Application", oldApplication, newApplication)
	if diff == nil || !containsPath(diff, "status.sync.status") {
		t.Fatalf("Argo Application lost its curated diff: %+v", diff)
	}
}

func TestComputeDiff_CollidingCRDReconcileChurnStillDrops(t *testing.T) {
	oldJob := collisionObject("batch.volcano.sh/v1alpha1", "Job", map[string]any{
		"status": map[string]any{"state": map[string]any{"phase": "Running", "lastTransitionTime": "2026-08-31T10:00:00Z"}},
	})
	newJob := oldJob.DeepCopy()
	newJob.SetResourceVersion("2")
	if err := unstructured.SetNestedField(newJob.Object, "2026-08-31T10:01:00Z", "status", "state", "lastTransitionTime"); err != nil {
		t.Fatalf("set transition time: %v", err)
	}

	if diff := ComputeDiff("Job", oldJob, newJob); diff != nil {
		t.Fatalf("reconcile-only metadata churn produced a diff: %+v", diff)
	}
}

func TestComputeDiff_DifferentAPIGroupsAreNotDiffed(t *testing.T) {
	oldApplication := collisionObject("example.com/v1", "Application", map[string]any{
		"status": map[string]any{"phase": "Ready"},
	})
	newApplication := collisionObject("argoproj.io/v1alpha1", "Application", map[string]any{
		"status": map[string]any{"sync": map[string]any{"status": "Synced"}},
	})

	if diff := ComputeDiff("Application", oldApplication, newApplication); diff != nil {
		t.Fatalf("different API-group identities were diffed as one resource: %+v", diff)
	}
}

func TestRecordToTimelineStore_CollidingCRDUpdateSurvives(t *testing.T) {
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(timeline.ResetStore)

	oldJob := collisionObject("batch.volcano.sh/v1alpha1", "Job", map[string]any{
		"status": map[string]any{"state": map[string]any{"phase": "Pending"}},
	})
	newJob := collisionObject("batch.volcano.sh/v1alpha1", "Job", map[string]any{
		"status": map[string]any{"state": map[string]any{"phase": "Running"}},
	})
	newJob.SetResourceVersion("2")

	diff := ComputeDiff("Job", oldJob, newJob)
	recordToTimelineStore(ActiveClusterContext(), "Job", "gpu-demo", "collision-demo", "volcano-job-uid", "update", oldJob, newJob, diff, true)

	events, err := timeline.GetStore().Query(context.Background(), timeline.QueryOptions{
		Kinds: []string{"Job"}, Namespaces: []string{"gpu-demo"},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 || events[0].Diff == nil {
		t.Fatalf("Volcano Job update disappeared from the timeline: %+v", events)
	}
	if events[0].APIVersion != "batch.volcano.sh/v1alpha1" {
		t.Fatalf("timeline apiVersion = %q", events[0].APIVersion)
	}
}

func collisionObject(apiVersion, kind string, body map[string]any) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":            "collision-demo",
			"namespace":       "gpu-demo",
			"uid":             "volcano-job-uid",
			"resourceVersion": "1",
		},
	}
	for key, value := range body {
		object[key] = value
	}
	return &unstructured.Unstructured{Object: object}
}

func jobAsUnstructured(t *testing.T, job *batchv1.Job) *unstructured.Unstructured {
	t.Helper()
	job = job.DeepCopy()
	job.TypeMeta = metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"}
	job.UID = types.UID("core-job-uid")
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(job)
	if err != nil {
		t.Fatalf("convert Job: %v", err)
	}
	return &unstructured.Unstructured{Object: object}
}
