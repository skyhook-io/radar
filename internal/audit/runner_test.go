package audit

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type testLister[T any] struct {
	items []*T
}

func (l testLister[T]) List(labels.Selector) ([]*T, error) {
	return l.items, nil
}

func TestListNamespacedFiltersBatchResources(t *testing.T) {
	jobs := ListNamespaced(&testLister[batchv1.Job]{items: []*batchv1.Job{
		{ObjectMeta: metav1.ObjectMeta{Name: "keep-job", Namespace: "target"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "drop-job", Namespace: "other"}},
	}}, []string{"target"})
	if len(jobs) != 1 || jobs[0].Name != "keep-job" {
		t.Fatalf("expected only target namespace Job, got %#v", jobs)
	}

	cronJobs := ListNamespaced(&testLister[batchv1.CronJob]{items: []*batchv1.CronJob{
		{ObjectMeta: metav1.ObjectMeta{Name: "keep-cronjob", Namespace: "target"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "drop-cronjob", Namespace: "other"}},
	}}, []string{"target"})
	if len(cronJobs) != 1 || cronJobs[0].Name != "keep-cronjob" {
		t.Fatalf("expected only target namespace CronJob, got %#v", cronJobs)
	}
}

func TestListNamespacedFiltersReplicaSetsAndUnknownTypesFailClosed(t *testing.T) {
	replicaSets := ListNamespaced(&testLister[appsv1.ReplicaSet]{items: []*appsv1.ReplicaSet{
		{ObjectMeta: metav1.ObjectMeta{Name: "keep", Namespace: "target"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "drop", Namespace: "other"}},
	}}, []string{"target"})
	if len(replicaSets) != 1 || replicaSets[0].Name != "keep" {
		t.Fatalf("expected only target namespace ReplicaSet, got %#v", replicaSets)
	}

	unknown := ListNamespaced(&testLister[string]{items: []*string{new(string)}}, []string{"target"})
	if len(unknown) != 0 {
		t.Fatalf("unknown object type must fail closed, got %#v", unknown)
	}
}
