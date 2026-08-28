package loadtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

func TestAppsForAndPodsInApp(t *testing.T) {
	cases := []struct {
		pods, perApp, wantApps int
	}{
		{0, 200, 0},
		{1, 200, 1},
		{200, 200, 1},
		{201, 200, 2},
		{50000, 200, 250},
	}
	for _, c := range cases {
		if got := appsFor(c.pods, c.perApp); got != c.wantApps {
			t.Errorf("appsFor(%d,%d)=%d want %d", c.pods, c.perApp, got, c.wantApps)
		}
	}
	// pod distribution sums back to the total
	perApp, total := 200, 50000
	sum := 0
	for j := 0; j < appsFor(total, perApp); j++ {
		sum += podsInApp(j, total, perApp)
	}
	if sum != total {
		t.Fatalf("podsInApp sum=%d want %d", sum, total)
	}
}

func TestSeedObjectsTopology(t *testing.T) {
	g := New(Config{Pods: 1005, Nodes: 5, Namespaces: 3, PodsPerApp: 100})
	objs := g.SeedObjects()

	var pods, deploys, rss, svcs, nodes, nss, cms, secrets int
	deployUIDs := map[string]bool{}
	rsUIDs := map[string]bool{}
	for _, o := range objs {
		switch v := o.(type) {
		case *corev1.ConfigMap:
			cms++
		case *corev1.Secret:
			secrets++
		case *appsv1.Deployment:
			deploys++
			deployUIDs[string(v.UID)] = true
		case *appsv1.ReplicaSet:
			rss++
			rsUIDs[string(v.UID)] = true
			if len(v.OwnerReferences) != 1 || !deployUIDs[string(v.OwnerReferences[0].UID)] {
				t.Fatalf("replicaset %s not owned by a generated deployment", v.Name)
			}
		case *corev1.Pod:
			pods++
			if len(v.OwnerReferences) != 1 || v.OwnerReferences[0].Kind != "ReplicaSet" || !rsUIDs[string(v.OwnerReferences[0].UID)] {
				t.Fatalf("pod %s not owned by a generated replicaset", v.Name)
			}
			if v.Spec.NodeName == "" {
				t.Fatalf("pod %s not scheduled to a node", v.Name)
			}
		case *corev1.Service:
			svcs++
		case *corev1.Node:
			nodes++
		case *corev1.Namespace:
			nss++
		}
	}

	wantApps := appsFor(1005, 100) // 11
	if pods != 1005 {
		t.Errorf("pods=%d want 1005", pods)
	}
	if deploys != wantApps || rss != wantApps || svcs != wantApps || cms != wantApps || secrets != wantApps {
		t.Errorf("apps: deploy=%d rs=%d svc=%d cm=%d secret=%d want %d each", deploys, rss, svcs, cms, secrets, wantApps)
	}
	if nodes != 5 || nss != 3 {
		t.Errorf("nodes=%d nss=%d want 5/3", nodes, nss)
	}
}

// TestScaleRoundTripThroughInformer exercises the real fake-clientset watch
// path: a running informer consumes Create/Delete events while the scaler
// paces against the informer's store. It asserts convergence up and down and,
// implicitly, that batching keeps the fake watch channel from panicking.
func TestScaleRoundTripThroughInformer(t *testing.T) {
	g := New(Config{Pods: 300, Nodes: 4, Namespaces: 2, PodsPerApp: 100})
	client := fake.NewClientset(g.SeedObjects()...)

	factory := informers.NewSharedInformerFactory(client, 0)
	podInformer := factory.Core().V1().Pods().Informer()
	deployInformer := factory.Apps().V1().Deployments().Informer()
	stop := make(chan struct{})
	defer close(stop)
	factory.Start(stop)
	if !cache.WaitForCacheSync(stop, podInformer.HasSynced, deployInformer.HasSynced) {
		t.Fatal("informer failed to sync")
	}

	count := func(kind string) int {
		switch kind {
		case "Pod":
			return len(podInformer.GetStore().ListKeys())
		case "Deployment":
			return len(deployInformer.GetStore().ListKeys())
		}
		return 0
	}
	if got := waitCount(func() int { return count("Pod") }, 300, 5*time.Second); got != 300 {
		t.Fatalf("seed count=%d want 300", got)
	}

	ctx := context.Background()
	for _, target := range []int{900, 50, 450, 0, 250} {
		res, err := g.ScaleTo(ctx, client, target, count)
		if err != nil {
			t.Fatalf("ScaleTo(%d): %v", target, err)
		}
		if !res.Converged {
			t.Fatalf("ScaleTo(%d) did not converge; informer=%d", target, count("Pod"))
		}
		if got := count("Pod"); got != target {
			t.Fatalf("after ScaleTo(%d) informer store=%d", target, got)
		}
		// apps track pods: one Deployment per app of PodsPerApp
		wantApps := appsFor(target, g.Config().PodsPerApp)
		if got := count("Deployment"); got != wantApps {
			t.Fatalf("after ScaleTo(%d) deployments=%d want %d", target, got, wantApps)
		}
	}
}

// TestScaleDownAfterPartialProgressLeavesNoOrphanApps reproduces the case where
// a scale-down deletes pods (advancing the pod counter) but fails before
// deleting the app skeletons. A retry must still remove every orphaned
// Deployment/ReplicaSet/Service/ConfigMap/Secret — it must not derive the app
// count from pod progress.
func TestScaleDownAfterPartialProgressLeavesNoOrphanApps(t *testing.T) {
	g := New(Config{Pods: 1000, Nodes: 4, Namespaces: 2, PodsPerApp: 200}) // 5 apps
	client := fake.NewClientset(g.SeedObjects()...)

	factory := informers.NewSharedInformerFactory(client, 0)
	podInformer := factory.Core().V1().Pods().Informer()
	deployInformer := factory.Apps().V1().Deployments().Informer()
	svcInformer := factory.Core().V1().Services().Informer()
	cmInformer := factory.Core().V1().ConfigMaps().Informer()
	stop := make(chan struct{})
	defer close(stop)
	factory.Start(stop)
	if !cache.WaitForCacheSync(stop, podInformer.HasSynced, deployInformer.HasSynced, svcInformer.HasSynced, cmInformer.HasSynced) {
		t.Fatal("informer failed to sync")
	}
	count := func(kind string) int {
		switch kind {
		case "Pod":
			return len(podInformer.GetStore().ListKeys())
		case "Deployment":
			return len(deployInformer.GetStore().ListKeys())
		case "Service":
			return len(svcInformer.GetStore().ListKeys())
		case "ConfigMap":
			return len(cmInformer.GetStore().ListKeys())
		}
		return 0
	}
	waitCount(func() int { return count("Pod") }, 1000, 5*time.Second)

	ctx := context.Background()
	// Simulate the partial failure: pods deleted down to 400 (current advances),
	// but the app skeletons are left untouched (currentApps stays 5).
	if err := g.deletePods(ctx, client, 400, 1000, count); err != nil {
		t.Fatalf("setup deletePods: %v", err)
	}
	if g.AppCount() != 5 {
		t.Fatalf("precondition: AppCount=%d want 5 (apps must be untouched)", g.AppCount())
	}

	// Retry the scale-down; it must clean up the orphaned apps.
	res, err := g.ScaleTo(ctx, client, 200, count)
	if err != nil {
		t.Fatalf("ScaleTo: %v", err)
	}
	if !res.Converged || count("Pod") != 200 {
		t.Fatalf("pods=%d converged=%v want 200/true", count("Pod"), res.Converged)
	}
	// 200 pods / 200 per app = 1 app. Every other app kind must match — no
	// orphans. Assert against the client (authoritative truth): ScaleTo only
	// paces the Deployment informer and converges on Pods, so the other kinds'
	// informer stores may still be draining delete events when it returns.
	assertClientCount := func(kind string, list func() (int, error)) {
		n, err := list()
		if err != nil {
			t.Fatalf("list %s: %v", kind, err)
		}
		if n != 1 {
			t.Fatalf("orphan %s skeletons in client: %d want 1", kind, n)
		}
	}
	assertClientCount("Deployment", func() (int, error) {
		l, e := client.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
		return len(l.Items), e
	})
	assertClientCount("ReplicaSet", func() (int, error) {
		l, e := client.AppsV1().ReplicaSets("").List(ctx, metav1.ListOptions{})
		return len(l.Items), e
	})
	assertClientCount("Service", func() (int, error) {
		l, e := client.CoreV1().Services("").List(ctx, metav1.ListOptions{})
		return len(l.Items), e
	})
	assertClientCount("ConfigMap", func() (int, error) {
		l, e := client.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{})
		return len(l.Items), e
	})
	assertClientCount("Secret", func() (int, error) {
		l, e := client.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
		return len(l.Items), e
	})
}

// TestScaleUpCreateFailureThenScaleDownLeavesNoOrphanApps reproduces a scale-up
// that fails partway through creating app skeletons: the app count must still
// reflect what was written, so a later scale-down cleans up every skeleton
// (including the partially-written app whose Deployment create failed).
func TestScaleUpCreateFailureThenScaleDownLeavesNoOrphanApps(t *testing.T) {
	g := New(Config{Pods: 0, Nodes: 4, Namespaces: 2, PodsPerApp: 200})
	client := fake.NewClientset(g.SeedObjects()...)

	// Fail the Deployment create for app index 2 exactly once, mid scale-up.
	failed := false
	client.PrependReactor("create", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
		d := a.(clienttesting.CreateAction).GetObject().(*appsv1.Deployment)
		if d.Name == "app-0002" && !failed {
			failed = true
			return true, nil, fmt.Errorf("injected create failure")
		}
		return false, nil, nil
	})

	factory := informers.NewSharedInformerFactory(client, 0)
	deployInformer := factory.Apps().V1().Deployments().Informer()
	svcInformer := factory.Core().V1().Services().Informer()
	cmInformer := factory.Core().V1().ConfigMaps().Informer()
	stop := make(chan struct{})
	defer close(stop)
	factory.Start(stop)
	if !cache.WaitForCacheSync(stop, deployInformer.HasSynced, svcInformer.HasSynced, cmInformer.HasSynced) {
		t.Fatal("informer failed to sync")
	}
	count := func(kind string) int {
		switch kind {
		case "Deployment":
			return len(deployInformer.GetStore().ListKeys())
		case "Service":
			return len(svcInformer.GetStore().ListKeys())
		case "ConfigMap":
			return len(cmInformer.GetStore().ListKeys())
		}
		return 0
	}

	ctx := context.Background()
	// Scale up to 1000 pods (5 apps). createApps fails at app 2's Deployment.
	if _, err := g.ScaleTo(ctx, client, 1000, count); err == nil {
		t.Fatal("expected ScaleTo to fail on injected create error")
	}
	// Precondition: app 2 is partially written (its ConfigMap exists, its
	// Deployment does not) — the state cleanup must fully remove.
	if _, err := client.CoreV1().ConfigMaps("loadtest-00").Get(ctx, "app-0002-config", metav1.GetOptions{}); err != nil {
		t.Fatalf("expected partial app 2 ConfigMap to exist: %v", err)
	}

	// Scale back down to 0; every skeleton — including the partial app — must go.
	if _, err := g.ScaleTo(ctx, client, 0, count); err != nil {
		t.Fatalf("cleanup ScaleTo(0): %v", err)
	}
	// Assert against the client (authoritative truth, no informer lag): no app
	// skeleton of any kind may survive.
	assertNoneInClient := func(kind string, list func() (int, error)) {
		n, err := list()
		if err != nil {
			t.Fatalf("list %s: %v", kind, err)
		}
		if n != 0 {
			t.Fatalf("orphan %s skeletons in client after cleanup: %d want 0", kind, n)
		}
	}
	assertNoneInClient("Deployment", func() (int, error) {
		l, e := client.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
		return len(l.Items), e
	})
	assertNoneInClient("ReplicaSet", func() (int, error) {
		l, e := client.AppsV1().ReplicaSets("").List(ctx, metav1.ListOptions{})
		return len(l.Items), e
	})
	assertNoneInClient("Service", func() (int, error) {
		l, e := client.CoreV1().Services("").List(ctx, metav1.ListOptions{})
		return len(l.Items), e
	})
	assertNoneInClient("ConfigMap", func() (int, error) {
		l, e := client.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{})
		return len(l.Items), e
	})
	assertNoneInClient("Secret", func() (int, error) {
		l, e := client.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
		return len(l.Items), e
	})
}

// TestScaleUpRetryCompletesPartialApp reproduces a scale-up that fails partway
// through an app, then retries: the retry must finish the partially-written app
// (create its missing Deployment/ReplicaSet/Service) rather than skip it, so no
// pod ends up referencing a nonexistent workload.
func TestScaleUpRetryCompletesPartialApp(t *testing.T) {
	g := New(Config{Pods: 0, Nodes: 4, Namespaces: 2, PodsPerApp: 200})
	client := fake.NewClientset(g.SeedObjects()...)

	failed := false
	client.PrependReactor("create", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
		d := a.(clienttesting.CreateAction).GetObject().(*appsv1.Deployment)
		if d.Name == "app-0002" && !failed {
			failed = true
			return true, nil, fmt.Errorf("injected create failure")
		}
		return false, nil, nil
	})

	factory := informers.NewSharedInformerFactory(client, 0)
	podInformer := factory.Core().V1().Pods().Informer()
	deployInformer := factory.Apps().V1().Deployments().Informer()
	stop := make(chan struct{})
	defer close(stop)
	factory.Start(stop)
	if !cache.WaitForCacheSync(stop, podInformer.HasSynced, deployInformer.HasSynced) {
		t.Fatal("informer failed to sync")
	}
	count := func(kind string) int {
		switch kind {
		case "Pod":
			return len(podInformer.GetStore().ListKeys())
		case "Deployment":
			return len(deployInformer.GetStore().ListKeys())
		}
		return 0
	}

	ctx := context.Background()
	if _, err := g.ScaleTo(ctx, client, 1000, count); err == nil {
		t.Fatal("expected first ScaleTo to fail on injected error")
	}
	// Retry the same target; the reactor now passes, so app 2 must be finished.
	res, err := g.ScaleTo(ctx, client, 1000, count)
	if err != nil {
		t.Fatalf("retry ScaleTo: %v", err)
	}
	if !res.Converged || count("Pod") != 1000 {
		t.Fatalf("pods=%d converged=%v want 1000/true", count("Pod"), res.Converged)
	}
	// The previously-partial app 2 must now have its Deployment, and all 5 apps
	// must be fully present — no pod references a missing workload.
	if _, err := client.AppsV1().Deployments("loadtest-00").Get(ctx, "app-0002", metav1.GetOptions{}); err != nil {
		t.Fatalf("app 2 Deployment missing after retry: %v", err)
	}
	if got := count("Deployment"); got != 5 {
		t.Fatalf("deployments=%d want 5 (all apps complete)", got)
	}
}

// TestReconcileFixesFormerBoundaryReplicasAfterPodDivergence covers the case
// where a scale created a partial boundary app but the pod count then diverged
// from the app count; a later scale-up must still bring that former-boundary
// Deployment's replica count up to full rather than leave it stale.
func TestReconcileFixesFormerBoundaryReplicasAfterPodDivergence(t *testing.T) {
	g := New(Config{Pods: 0, Nodes: 4, Namespaces: 2, PodsPerApp: 10})
	client := fake.NewClientset(g.SeedObjects()...)

	factory := informers.NewSharedInformerFactory(client, 0)
	podInformer := factory.Core().V1().Pods().Informer()
	deployInformer := factory.Apps().V1().Deployments().Informer()
	stop := make(chan struct{})
	defer close(stop)
	factory.Start(stop)
	if !cache.WaitForCacheSync(stop, podInformer.HasSynced, deployInformer.HasSynced) {
		t.Fatal("informer failed to sync")
	}
	count := func(kind string) int {
		switch kind {
		case "Pod":
			return len(podInformer.GetStore().ListKeys())
		case "Deployment":
			return len(deployInformer.GetStore().ListKeys())
		}
		return 0
	}

	ctx := context.Background()
	// 45 pods over PodsPerApp=10 => 5 apps, app 4 partial (5 replicas).
	if _, err := g.ScaleTo(ctx, client, 45, count); err != nil {
		t.Fatalf("ScaleTo(45): %v", err)
	}
	d, _ := client.AppsV1().Deployments("loadtest-00").Get(ctx, "app-0004", metav1.GetOptions{})
	if *d.Spec.Replicas != 5 {
		t.Fatalf("app 4 replicas=%d want 5 (partial boundary)", *d.Spec.Replicas)
	}

	// Simulate pods diverging below the app count (as after a partial pod
	// failure): the pod counter drops but the 5 apps remain.
	g.mu.Lock()
	g.current = 5
	g.mu.Unlock()

	// Grow to 250 pods (25 apps). App 4 is now a full interior app and its
	// replica count must be corrected to PodsPerApp, not left at 5.
	if _, err := g.ScaleTo(ctx, client, 250, count); err != nil {
		t.Fatalf("ScaleTo(250): %v", err)
	}
	d, _ = client.AppsV1().Deployments("loadtest-00").Get(ctx, "app-0004", metav1.GetOptions{})
	if *d.Spec.Replicas != 10 {
		t.Fatalf("app 4 replicas=%d want 10 (former boundary must be re-fulled)", *d.Spec.Replicas)
	}
}

func waitCount(count func() int, want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if count() == want {
			return want
		}
		time.Sleep(2 * time.Millisecond)
	}
	return count()
}
