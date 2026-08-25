package k8s

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// The CRD warmup/discovery LISTs share the apiserver connection with the
// deferred typed LISTs (ReplicaSets, Secrets — the heaviest kinds on large
// clusters). These tests pin the sequencing contract: no CRD traffic until the
// deferred informers finish their initial sync.

var (
	fatKnownCRD = schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledjobs"}
	smallCRD    = schema.GroupVersionResource{Group: "protection.crossplane.io", Version: "v1beta1", Resource: "usages"}
)

// blockedDeferredCache builds a ResourceCache shaped like the large-cluster
// startup moment this guards: critical kinds synced (first paint done), the
// deferred ReplicaSet LIST still in flight. The returned release func unblocks
// the LIST; it is safe to call more than once and always runs before Stop so
// the informer goroutine parked in the reactor can exit.
func blockedDeferredCache(t *testing.T) (*k8score.ResourceCache, *fakeclientset.Clientset, func()) {
	t.Helper()

	releaseRS := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseRS) })

	typedClient := fakeclientset.NewSimpleClientset()
	typedClient.PrependReactor("list", "replicasets", func(k8stesting.Action) (bool, runtime.Object, error) {
		<-releaseRS
		return false, nil, nil
	})

	rc, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client: typedClient,
		ResourceTypes: map[string]bool{
			k8score.Pods:        true,
			k8score.Services:    true,
			k8score.Deployments: true,
			k8score.ReplicaSets: true,
		},
		DeferredTypes: map[string]bool{
			k8score.ReplicaSets: true,
		},
	})
	if err != nil {
		release()
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	t.Cleanup(func() {
		release()
		rc.Stop()
	})

	select {
	case <-rc.DeferredDone():
		t.Fatal("precondition: deferred sync completed while the ReplicaSet LIST was still blocked")
	default:
	}

	return rc, typedClient, release
}

// crdFixture builds a DynamicResourceCache over a fake discovery that serves
// two CRDs — one known-integration shaped (the WarmupCommonCRDs path) and one
// small unknown (the DiscoverAllCRDs eager-watch path) — with a reactor
// counting every LIST the dynamic client issues.
func crdFixture(t *testing.T) (*k8score.DynamicResourceCache, *atomic.Int64) {
	t.Helper()

	discoveryClient := fakeclientset.NewSimpleClientset()
	discoveryClient.Fake.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: fatKnownCRD.Group + "/" + fatKnownCRD.Version,
			APIResources: []metav1.APIResource{
				{Name: fatKnownCRD.Resource, Kind: "ScaledJob", Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch"}},
			},
		},
		{
			GroupVersion: smallCRD.Group + "/" + smallCRD.Version,
			APIResources: []metav1.APIResource{
				{Name: smallCRD.Resource, Kind: "Usage", Namespaced: false, Verbs: metav1.Verbs{"get", "list", "watch"}},
			},
		},
	}
	discovery, err := k8score.NewResourceDiscovery(discoveryClient.Discovery())
	if err != nil {
		t.Fatalf("NewResourceDiscovery failed: %v", err)
	}

	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			fatKnownCRD: "ScaledJobList",
			smallCRD:    "UsageList",
		},
	)
	var crdListCalls atomic.Int64
	dynClient.PrependReactor("list", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		crdListCalls.Add(1)
		return false, nil, nil
	})

	dc, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{
		DynamicClient: dynClient,
		Discovery:     discovery,
	})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}
	t.Cleanup(dc.Stop)

	return dc, &crdListCalls
}

func TestCRDWarmupSequenceGatedOnDeferredSync(t *testing.T) {
	rc, _, release := blockedDeferredCache(t)
	dc, crdListCalls := crdFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	warmupRan := make(chan struct{})
	discoverRan := make(chan struct{})
	go runCRDWarmupSequence(ctx, rc.DeferredDone(), nil,
		func() {
			if err := dc.EnsureWatching(fatKnownCRD); err != nil {
				t.Errorf("EnsureWatching(%v) failed: %v", fatKnownCRD, err)
			}
			close(warmupRan)
		},
		func() {
			dc.DiscoverAllCRDs()
			close(discoverRan)
		},
	)

	select {
	case <-warmupRan:
		t.Fatal("CRD warmup step ran while the deferred ReplicaSet LIST was still in flight")
	case <-time.After(300 * time.Millisecond):
	}
	if n := crdListCalls.Load(); n != 0 {
		t.Fatalf("%d CRD LIST(s) issued while deferred informers were still syncing", n)
	}

	release()
	select {
	case <-rc.DeferredDone():
	case <-time.After(5 * time.Second):
		t.Fatal("deferred sync did not complete after releasing the ReplicaSet LIST")
	}

	select {
	case <-warmupRan:
	case <-time.After(5 * time.Second):
		t.Fatal("CRD warmup step did not run after deferred sync completed")
	}
	select {
	case <-discoverRan:
	case <-time.After(5 * time.Second):
		t.Fatal("CRD discovery step did not run after deferred sync completed")
	}

	deadline := time.Now().Add(5 * time.Second)
	for dc.GetDiscoveryStatus() != k8score.CRDDiscoveryComplete && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := dc.GetDiscoveryStatus(); got != k8score.CRDDiscoveryComplete {
		t.Fatalf("CRD discovery status = %v after gate release, want complete", got)
	}
	if !dc.WaitForSync(smallCRD, 5*time.Second) {
		t.Fatalf("small CRD %v was not eagerly watched after gate release", smallCRD)
	}
	if crdListCalls.Load() == 0 {
		t.Fatal("expected CRD LISTs after the gate released")
	}
}

func TestCRDWarmupSequenceAbortsOnContextCancel(t *testing.T) {
	rc, _, _ := blockedDeferredCache(t)

	ctx, cancel := context.WithCancel(context.Background())
	var stepRan atomic.Bool
	seqDone := make(chan struct{})
	go func() {
		defer close(seqDone)
		runCRDWarmupSequence(ctx, rc.DeferredDone(), nil, func() { stepRan.Store(true) })
	}()

	cancel()
	select {
	case <-seqDone:
	case <-time.After(2 * time.Second):
		t.Fatal("runCRDWarmupSequence did not return after context cancellation")
	}
	if stepRan.Load() {
		t.Fatal("CRD step ran despite context cancellation while gated")
	}
}

// TestCRDWarmupSequenceSurvivesInitContextCancel pins the lifecycle contract
// InitAllSubsystems must honor: callers cancel the init ctx as soon as critical
// sync returns, while deferred sync is still in flight. The sequence must ride
// a longer-lived lifecycle ctx (OperationContext), not the init ctx — otherwise
// CRD warmup silently never runs on large clusters.
func TestCRDWarmupSequenceSurvivesInitContextCancel(t *testing.T) {
	rc, _, release := blockedDeferredCache(t)

	initCtx, initCancel := context.WithCancel(context.Background())
	lifecycleCtx, lifeCancel := context.WithCancel(context.Background())
	defer lifeCancel()

	var stepRan atomic.Bool
	seqDone := make(chan struct{})
	go func() {
		defer close(seqDone)
		runCRDWarmupSequence(lifecycleCtx, rc.DeferredDone(), nil, func() { stepRan.Store(true) })
	}()

	// Simulate InitializeCluster / switchContext returning and defer-canceling
	// the init ctx while deferred informers are still blocked.
	initCancel()
	select {
	case <-seqDone:
		t.Fatal("sequence returned after init-ctx cancel — it must not observe that ctx")
	case <-time.After(300 * time.Millisecond):
	}
	if stepRan.Load() {
		t.Fatal("CRD step ran while deferred sync was still blocked")
	}
	// initCtx is intentionally unused after cancel: the bug was wiring it in.
	_ = initCtx

	release()
	select {
	case <-seqDone:
	case <-time.After(5 * time.Second):
		t.Fatal("sequence did not complete after deferred sync finished")
	}
	if !stepRan.Load() {
		t.Fatal("CRD step did not run after deferred sync; lifecycle ctx was abandoned")
	}
}

func TestCRDWarmupSequenceAbortsWhenCachesReplaced(t *testing.T) {
	rc, _, release := blockedDeferredCache(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stepRan atomic.Bool
	alive := atomic.Bool{}
	alive.Store(true)
	seqDone := make(chan struct{})
	go func() {
		defer close(seqDone)
		runCRDWarmupSequence(ctx, rc.DeferredDone(), func() bool { return alive.Load() },
			func() { stepRan.Store(true) },
		)
	}()

	alive.Store(false)
	release()
	select {
	case <-seqDone:
	case <-time.After(5 * time.Second):
		t.Fatal("sequence did not return after caches were marked replaced")
	}
	if stepRan.Load() {
		t.Fatal("CRD step ran against a replaced cluster view")
	}
}
