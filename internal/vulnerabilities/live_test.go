package vulnerabilities

import (
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func owned(apiVersion, kind, name string) []metav1.OwnerReference {
	controller := true
	return []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID("uid-" + name), Controller: &controller}}
}

func pod(name string, phase corev1.PodPhase, owners []metav1.OwnerReference, images ...string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("uid-" + name), OwnerReferences: owners}, Status: corev1.PodStatus{Phase: phase}}
	for i, img := range images {
		c := corev1.Container{Name: []string{"app", "sidecar"}[i], Image: img}
		p.Spec.Containers = append(p.Spec.Containers, c)
	}
	return p
}

func syncedCache(t *testing.T, objs ...runtime.Object) *k8s.ResourceCache {
	t.Helper()
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        fake.NewClientset(objs...),
		ResourceTypes: map[string]bool{k8score.Pods: true, k8score.ReplicaSets: true, k8score.Jobs: true, k8score.CronJobs: true},
		DeferredTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}
	deadline := time.Now().Add(5 * time.Second)
	for !(cache.IsKindReady("pods") && cache.IsKindReady("cronjobs") && cache.IsKindReady("jobs") && cache.IsKindReady("replicasets")) {
		if time.Now().After(deadline) {
			t.Fatal("cache never synced")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cache
}

// Each Pod's containers are keyed by the object Trivy Operator scans for it,
// and named by the workload a person would name.
func TestLiveTargets(t *testing.T) {
	suspend := true
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop", UID: "uid-web-1", OwnerReferences: owned("apps/v1", "Deployment", "web")}}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "nightly-1", Namespace: "shop", UID: "uid-nightly-1", OwnerReferences: owned("batch/v1", "CronJob", "nightly")}}
	nightly := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "shop", UID: "uid-nightly"}}
	nightly.Spec.JobTemplate.Spec.Template.Spec.Containers = []corev1.Container{{Name: "app", Image: "acme/report:1"}}
	paused := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "paused", Namespace: "shop"}, Spec: batchv1.CronJobSpec{Suspend: &suspend}}
	paused.Spec.JobTemplate.Spec.Template.Spec.Containers = []corev1.Container{{Name: "app", Image: "acme/paused:1"}}

	cache := syncedCache(t, rs, job, nightly, paused,
		pod("web-1-a", corev1.PodRunning, owned("apps/v1", "ReplicaSet", "web-1"), "acme/web:2", "envoy:1"),
		pod("web-1-b", corev1.PodRunning, owned("apps/v1", "ReplicaSet", "web-1"), "acme/web:2", "envoy:1"),
		pod("nightly-1-x", corev1.PodRunning, owned("batch/v1", "Job", "nightly-1"), "acme/report:1"),
		pod("pg-1", corev1.PodRunning, owned("postgresql.cnpg.io/v1", "Cluster", "pg"), "postgres:16"),
		pod("debug", corev1.PodUnknown, nil, "busybox"),
		pod("done", corev1.PodSucceeded, nil, "acme/once:1"),
	)

	targets, ok := liveTargets(cache, nil, nil)
	if !ok {
		t.Fatal("cache is ready")
	}
	got := map[targetKey]WorkloadRef{}
	for _, tg := range targets {
		got[tg.key] = tg.workload
	}
	want := map[targetKey]WorkloadRef{
		{namespace: "shop", kind: "ReplicaSet", name: "web-1", container: "app", image: "acme/web:2"}:   {Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web"},
		{namespace: "shop", kind: "ReplicaSet", name: "web-1", container: "sidecar", image: "envoy:1"}:  {Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web"},
		{namespace: "shop", kind: "CronJob", name: "nightly", container: "app", image: "acme/report:1"}: {Kind: "CronJob", Group: "batch", Namespace: "shop", Name: "nightly"},
		{namespace: "shop", kind: "Pod", name: "pg-1", container: "app", image: "postgres:16"}:          {Kind: "Cluster", Group: "postgresql.cnpg.io", Namespace: "shop", Name: "pg"},
		{namespace: "shop", kind: "Pod", name: "debug", container: "app", image: "busybox"}:             {Kind: "Pod", Namespace: "shop", Name: "debug"},
	}
	if len(got) != len(want) {
		t.Fatalf("targets:\n got %v\nwant %v", got, want)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%+v: got %+v, want %+v", k, got[k], w)
		}
	}

	// The caller sees only what it may read: here, not Deployments.
	limited, _ := liveTargets(cache, []string{"shop"}, func(group, kind, ns string) bool { return kind != "Deployment" })
	var kinds []string
	for _, tg := range limited {
		kinds = append(kinds, tg.workload.Kind)
	}
	if slices.Contains(kinds, "Deployment") || len(limited) != 3 {
		t.Fatalf("limited: %v", kinds)
	}

	if _, ok := liveTargets(nil, nil, nil); ok {
		t.Fatal("no cache: nothing can be said about what runs")
	}
}
