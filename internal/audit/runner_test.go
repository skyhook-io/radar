package audit

import (
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
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

func TestCollectNamespacesOnlyWhenClusterWideReady(t *testing.T) {
	replicas := int32(1)
	objects := func() []runtime.Object {
		return []runtime.Object{
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "solo", Namespace: "a"}, Spec: appsv1.DeploymentSpec{Replicas: &replicas}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "a", Annotations: map[string]string{bp.IgnoreChecksAnnotation: "singleReplica"}}},
		}
	}
	flagged := func(r *bp.ScanResults) bool {
		for _, f := range r.Findings {
			if f.CheckID == "singleReplica" && f.Name == "solo" {
				return true
			}
		}
		return false
	}
	for _, tc := range []struct {
		name       string
		namespaces bool
		want       bool
	}{
		{"cluster-wide and synced", true, false},
		{"Namespaces not listable", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scopes := map[string]k8score.ResourceScope{k8score.Deployments: {Enabled: true}, k8score.HorizontalPodAutoscalers: {Enabled: true}, k8score.Namespaces: {Enabled: tc.namespaces}}
			if err := k8s.InitScopedTestResourceCache(fake.NewClientset(objects()...), scopes); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			r := RunFromCache(k8s.GetResourceCache(), nil, nil)
			if got := flagged(r); got != tc.want {
				t.Fatalf("singleReplica flagged = %v, want %v", got, tc.want)
			}
			if r.NamespaceIgnoreChecks != tc.namespaces {
				t.Fatalf("NamespaceIgnoreChecks = %v, want %v", r.NamespaceIgnoreChecks, tc.namespaces)
			}
		})
	}
}
