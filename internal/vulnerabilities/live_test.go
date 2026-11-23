package vulnerabilities

import (
	"context"
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

	live, ok, _ := liveTargets(context.Background(), cache, nil, nil, nil, exposureIndex{})
	targets := live.targets
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
	limitedSet, _, _ := liveTargets(context.Background(), cache, []string{"shop"}, func(group, kind, ns string) bool { return kind != "Deployment" }, nil, exposureIndex{})
	limited := limitedSet.targets
	if !limitedSet.withheld[ownerKey{namespace: "shop", kind: "ReplicaSet", name: "web-1", container: "app"}] {
		t.Fatalf("a target named by an unreadable workload is withheld: %v", limitedSet.withheld)
	}
	var kinds []string
	for _, tg := range limited {
		kinds = append(kinds, tg.workload.Kind)
	}
	if slices.Contains(kinds, "Deployment") || len(limited) != 3 {
		t.Fatalf("limited: %v", kinds)
	}

	// Without Pod access in a namespace, nothing there is read as running.
	noneSet, _, _ := liveTargets(context.Background(), cache, []string{"shop"}, nil, func(ns string) bool { return false }, exposureIndex{})
	none, denied := noneSet.targets, noneSet.podDenied
	for _, tg := range none {
		if tg.key.kind != "CronJob" {
			t.Fatalf("read a Pod without Pod access: %+v", tg)
		}
	}
	if !slices.Equal(denied, []string{"shop"}) {
		t.Fatalf("denied: %v", denied)
	}

	if _, ok, _ := liveTargets(context.Background(), nil, nil, nil, nil, exposureIndex{}); ok {
		t.Fatal("no cache: nothing can be said about what runs")
	}
}

func TestLiveTargetsCarryTheRunningDigest(t *testing.T) {
	p := pod("web", corev1.PodRunning, nil, "acme/web:latest")
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: p.Spec.Containers[0].Name, Image: "acme/web:latest", ImageID: "docker-pullable://acme/web@sha256:NEW"}}
	live, ok, _ := liveTargets(context.Background(), syncedCache(t, p), []string{p.Namespace}, nil, nil, exposureIndex{})
	if !ok || len(live.targets) != 1 || live.targets[0].digest != "sha256:new" {
		t.Fatalf("%+v", live.targets)
	}
	local := pod("local", corev1.PodRunning, nil, "acme/web:latest")
	local.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: local.Spec.Containers[0].Name, ImageID: "sha256:abc"}}
	live, _, _ = liveTargets(context.Background(), syncedCache(t, local), []string{local.Namespace}, nil, nil, exposureIndex{})
	if len(live.targets) != 1 || live.targets[0].digest != "" {
		t.Fatalf("a local image ID is no registry digest: %+v", live.targets)
	}
}

// Trivy Operator's own scan and node-collector Jobs are never scanned and
// aren't anything a user upgrades: they stay out of the running set.
func TestTrivysOwnJobsAreLeftOut(t *testing.T) {
	trivy := map[string]string{"app.kubernetes.io/managed-by": "trivy-operator"}
	collector := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "node-collector-1", Namespace: "shop", UID: "uid-node-collector-1", Labels: trivy}}
	scan := pod("scan-vulnerabilityreport-1", corev1.PodRunning, nil, "aquasec/trivy:0.76.1")
	scan.Labels = trivy
	cache := syncedCache(t, collector, scan,
		pod("node-collector-1-x", corev1.PodPending, owned("batch/v1", "Job", "node-collector-1"), "aquasec/node-collector:1"),
		pod("web", corev1.PodRunning, nil, "acme/web:2"),
	)
	live, _, _ := liveTargets(context.Background(), cache, nil, nil, nil, exposureIndex{})
	var images []string
	for _, tg := range live.targets {
		images = append(images, tg.image)
	}
	if !slices.Equal(images, []string{"acme/web:2"}) {
		t.Fatalf("got %v, want only acme/web:2", images)
	}
}

// A started container whose image has no registry digest (a local image ID)
// runs a build no scan can be shown to describe: it reads as not scanned,
// whatever a sibling replica runs or its spec now pins.
func TestUnresolvedRunningBuildStaysUnscanned(t *testing.T) {
	started := func(p *corev1.Pod, imageID string) *corev1.Pod {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Image: p.Spec.Containers[0].Image, ImageID: imageID, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
		return p
	}

	// Alone, against a scan of the tag's registry build.
	local := started(pod("web", corev1.PodRunning, nil, "acme/web:latest"), "sha256:local-id")
	live, _, _ := liveTargets(context.Background(), syncedCache(t, local), []string{"shop"}, nil, nil, exposureIndex{})
	tagScan := vulnReport(reportSpec{name: "tag", ns: "shop", ownerKind: "Pod", ownerName: "web", container: "app", repo: "acme/web", tag: "latest", digest: "sha256:registry"})
	if got := mustBuild(t, []*unstructured.Unstructured{tagScan}, live.targets, ViewImage, Filter{}); got.Summary.ImagesScanned != 0 || got.Summary.ImagesUnscanned != 1 {
		t.Errorf("alone: %+v", got.Summary)
	}

	// Beside a replica of the same ReplicaSet that runs a scanned build.
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop", UID: "uid-web-1", OwnerReferences: owned("apps/v1", "Deployment", "checkout")}}
	known := started(pod("web-1-a", corev1.PodRunning, owned("apps/v1", "ReplicaSet", "web-1"), "acme/web:latest"), "acme/web@sha256:d")
	unknown := started(pod("web-1-b", corev1.PodRunning, owned("apps/v1", "ReplicaSet", "web-1"), "acme/web:latest"), "sha256:local-id")
	live, _, _ = liveTargets(context.Background(), syncedCache(t, rs, known, unknown), []string{"shop"}, nil, nil, exposureIndex{})
	scan := vulnReport(reportSpec{name: "known", ns: "shop", ownerKind: "ReplicaSet", ownerName: "web-1", container: "app", repo: "acme/web", tag: "latest", digest: "sha256:d"})
	if got := mustBuild(t, []*unstructured.Unstructured{scan}, live.targets, ViewWorkload, Filter{}); got.Summary.ImagesUnscanned != 1 || len(got.CleanWorkloads) != 0 {
		t.Errorf("beside a scanned sibling: %+v clean=%+v", got.Summary, got.CleanWorkloads)
	}

	// Its spec edited in place to pin another build before it restarts.
	edited := started(pod("web", corev1.PodRunning, nil, "acme/web@sha256:new"), "sha256:local-id")
	live, _, _ = liveTargets(context.Background(), syncedCache(t, edited), []string{"shop"}, nil, nil, exposureIndex{})
	newScan := vulnReport(reportSpec{name: "new", ns: "shop", ownerKind: "Pod", ownerName: "web", container: "app", repo: "acme/web", digest: "sha256:new"})
	if got := mustBuild(t, []*unstructured.Unstructured{newScan}, live.targets, ViewImage, Filter{}); got.Summary.ImagesScanned != 0 {
		t.Errorf("a pinned spec doesn't say what runs: %+v", got.Summary)
	}
}

// Two replicas of one build are one target; a Service that selects only one
// of them still marks the workload.
func TestLiveTargetsMergeReplicaRoutes(t *testing.T) {
	owners := owned("apps/v1", "StatefulSet", "db")
	first := pod("db-0", corev1.PodRunning, owners, "postgres:16")
	second := pod("db-1", corev1.PodRunning, owners, "postgres:16")
	second.Labels = map[string]string{"statefulset.kubernetes.io/pod-name": "db-1"}
	exposure := mustIndex(routeSources{services: []*corev1.Service{withNodePort(service("shop", "db-1-public", corev1.ServiceTypeNodePort, map[string]string{"statefulset.kubernetes.io/pod-name": "db-1"}))}}, nil, nil)
	live, _, _ := liveTargets(context.Background(), syncedCache(t, first, second), []string{"shop"}, nil, nil, exposure)
	if len(live.targets) != 1 || !slices.Equal(live.targets[0].routes, []Route{{Via: "NodePort", Namespace: "shop", Name: "db-1-public"}}) {
		t.Fatalf("targets: %+v", live.targets)
	}
}
