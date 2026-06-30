package audit

import (
	"testing"

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
	jobs := listNamespaced(&testLister[batchv1.Job]{items: []*batchv1.Job{
		{ObjectMeta: metav1.ObjectMeta{Name: "keep-job", Namespace: "target"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "drop-job", Namespace: "other"}},
	}}, []string{"target"})
	if len(jobs) != 1 || jobs[0].Name != "keep-job" {
		t.Fatalf("expected only target namespace Job, got %#v", jobs)
	}

	cronJobs := listNamespaced(&testLister[batchv1.CronJob]{items: []*batchv1.CronJob{
		{ObjectMeta: metav1.ObjectMeta{Name: "keep-cronjob", Namespace: "target"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "drop-cronjob", Namespace: "other"}},
	}}, []string{"target"})
	if len(cronJobs) != 1 || cronJobs[0].Name != "keep-cronjob" {
		t.Fatalf("expected only target namespace CronJob, got %#v", cronJobs)
	}
}
