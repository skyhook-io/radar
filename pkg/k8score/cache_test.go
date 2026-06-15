package k8score

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

func TestNewResourceCache_Basic(t *testing.T) {
	client := fake.NewSimpleClientset()

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods:        true,
			Services:    true,
			Deployments: true,
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	if !rc.IsSyncComplete() {
		t.Error("expected IsSyncComplete() = true after NewResourceCache returns")
	}

	if rc.Pods() == nil {
		t.Error("expected Pods() lister to be non-nil")
	}
	if rc.Services() == nil {
		t.Error("expected Services() lister to be non-nil")
	}
	if rc.Deployments() == nil {
		t.Error("expected Deployments() lister to be non-nil")
	}

	// Disabled resources should return nil listers
	if rc.Secrets() != nil {
		t.Error("expected Secrets() lister to be nil (not enabled)")
	}
	if rc.Nodes() != nil {
		t.Error("expected Nodes() lister to be nil (not enabled)")
	}
}

func TestNewResourceCache_DeferredSync(t *testing.T) {
	client := fake.NewSimpleClientset()

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods:       true,
			Services:   true,
			ConfigMaps: true,
			Secrets:    true,
		},
		DeferredTypes: map[string]bool{
			ConfigMaps: true,
			Secrets:    true,
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	// Critical resources should be available immediately
	if rc.Pods() == nil {
		t.Error("expected Pods() lister to be available (critical)")
	}

	// Wait for deferred to complete (fake client syncs immediately)
	select {
	case <-rc.DeferredDone():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for deferred sync")
	}

	if !rc.IsDeferredSynced() {
		t.Error("expected IsDeferredSynced() = true")
	}

	if rc.ConfigMaps() == nil {
		t.Error("expected ConfigMaps() lister to be available after deferred sync")
	}
	if rc.Secrets() == nil {
		t.Error("expected Secrets() lister to be available after deferred sync")
	}
}

// TestNewResourceCache_DeferredSync_PartialFailure verifies that a permanently
// failing deferred informer (e.g. HPA autoscaling/v2 on a K8s <1.23 cluster,
// which responds with "the server could not find the requested resource")
// does not block sibling deferred informers from becoming ready. It also
// verifies the DeferredSyncTimeout path flips deferredFailed so stragglers
// return false from IsDeferredPending.
func TestNewResourceCache_DeferredSync_PartialFailure(t *testing.T) {
	client := fake.NewSimpleClientset()
	// Make HPA LIST fail forever, as happens when the v2 API isn't served.
	client.PrependReactor("list", "horizontalpodautoscalers", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("the server could not find the requested resource")
	})

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods:                     true,
			ConfigMaps:               true,
			Secrets:                  true,
			HorizontalPodAutoscalers: true,
		},
		DeferredTypes: map[string]bool{
			ConfigMaps:               true,
			Secrets:                  true,
			HorizontalPodAutoscalers: true,
		},
		DeferredSyncTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	// Poll for ConfigMaps and Secrets to become ready. A failing sibling
	// (HPA) must not block them.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rc.ConfigMaps() != nil && rc.Secrets() != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rc.ConfigMaps() == nil {
		t.Fatal("expected ConfigMaps() to become ready despite sibling HPA failing")
	}
	if rc.Secrets() == nil {
		t.Fatal("expected Secrets() to become ready despite sibling HPA failing")
	}

	// Pre-timeout contract check: while HPA is still stuck and the deadline
	// hasn't fired, IsDeferredPending must report HPA pending (HTTP handlers
	// return 503) and ConfigMaps not-pending (handlers serve data). This is
	// the 503-vs-403 distinction the fix is built around.
	if !rc.IsDeferredPending(HorizontalPodAutoscalers) {
		t.Error("pre-timeout: expected IsDeferredPending(HPA)=true while informer still stuck")
	}
	if rc.IsDeferredPending(ConfigMaps) {
		t.Error("pre-timeout: ConfigMaps synced, expected IsDeferredPending=false")
	}

	// deferredDone must close even though HPA never syncs — otherwise the
	// SSE warmup completion never fires.
	select {
	case <-rc.DeferredDone():
	case <-time.After(3 * time.Second):
		t.Fatal("deferredDone never closed after DeferredSyncTimeout")
	}

	// Post-timeout: HPA flips from pending to not-pending because
	// deferredFailed is now set — stops the perpetual-503 spinner.
	// ConfigMaps stays not-pending (it was already synced).
	if rc.IsDeferredPending(HorizontalPodAutoscalers) {
		t.Error("post-timeout: expected IsDeferredPending(HPA)=false (deferredFailed signals give-up)")
	}
	if rc.IsDeferredPending(ConfigMaps) {
		t.Error("post-timeout: ConfigMaps synced, expected IsDeferredPending=false")
	}
}

// TestNewResourceCache_MinimalSet_AllFast verifies that when the patience
// window is set but every informer syncs quickly, NewResourceCache returns
// via the all-critical-synced path (not the minimal-set fallback). No
// informers should be promoted, syncProgress should fire to completion.
func TestNewResourceCache_MinimalSet_AllFast(t *testing.T) {
	client := fake.NewSimpleClientset()

	var lastSynced, lastTotal int
	var lastReady bool
	var progMu sync.Mutex

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods:        true,
			Services:    true,
			Deployments: true,
			Ingresses:   true,
		},
		PatienceWindow: 2 * time.Second,
		MinimalSet: map[string]bool{
			Pods:     true,
			Services: true,
		},
		SyncProgress: func(synced, total int, ready bool) {
			progMu.Lock()
			lastSynced, lastTotal, lastReady = synced, total, ready
			progMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	// All four critical informers should be synced — no promotion.
	if got := rc.PromotedKinds(); len(got) != 0 {
		t.Errorf("expected no promoted kinds when all critical synced fast, got %v", got)
	}

	progMu.Lock()
	defer progMu.Unlock()
	if lastTotal != 4 {
		t.Errorf("expected SyncProgress.total=4, got %d", lastTotal)
	}
	if lastSynced != 4 {
		t.Errorf("expected SyncProgress.synced=4, got %d", lastSynced)
	}
	if !lastReady {
		t.Error("expected final SyncProgress to report minimalReady=true")
	}
}

// TestNewResourceCache_MinimalSet_Promotion verifies the slow-cluster path:
// when a non-minimal critical informer can't sync within the patience
// window but minimal-set members are ready, NewResourceCache returns and
// promotes the slow informer to deferred. The promoted informer must
// continue running and eventually become available.
func TestNewResourceCache_MinimalSet_Promotion(t *testing.T) {
	client := fake.NewSimpleClientset()

	// Make ingresses LIST fail. The reflector retries with backoff, so
	// HasSynced() stays false, but the failure returns immediately —
	// no shared tracker lock held, pods/services LIST proceed normally.
	// Ingress is critical (not in MinimalSet) so it should be the one
	// that gets promoted.
	client.PrependReactor("list", "ingresses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("simulated slow API: ingresses unavailable")
	})

	start := time.Now()
	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods:      true,
			Services:  true,
			Ingresses: true,
		},
		PatienceWindow: 200 * time.Millisecond,
		MinimalSet: map[string]bool{
			Pods:     true,
			Services: true,
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	elapsed := time.Since(start)
	if elapsed < 200*time.Millisecond {
		t.Errorf("expected return after at least the patience window (200ms), got %v", elapsed)
	}
	// Cap upper bound generously: patience window + a few jitter ticks.
	if elapsed > 3*time.Second {
		t.Errorf("returned far later than patience window — minimal-set fallback didn't trigger? elapsed=%v", elapsed)
	}

	promoted := rc.PromotedKinds()
	if len(promoted) != 1 || promoted[0] != "Ingress" {
		t.Errorf("expected Ingress to be promoted, got %v", promoted)
	}

	// Minimal-set listers should be available immediately.
	if rc.Pods() == nil {
		t.Error("expected Pods() lister available after first paint")
	}
	if rc.Services() == nil {
		t.Error("expected Services() lister available after first paint")
	}
}

// TestNewResourceCache_SyncTimeout_Promotion covers the legacy hard-cap
// path used by skyhook-connector. Without PatienceWindow/MinimalSet, a
// stuck critical informer must promote at SyncTimeout and the cache
// must return.
func TestNewResourceCache_SyncTimeout_Promotion(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "ingresses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("simulated stuck API")
	})

	start := time.Now()
	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods:      true,
			Ingresses: true,
		},
		SyncTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	elapsed := time.Since(start)
	if elapsed < 200*time.Millisecond {
		t.Errorf("expected return after SyncTimeout (200ms), got %v", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("returned far later than SyncTimeout — promotion didn't fire? elapsed=%v", elapsed)
	}

	promoted := rc.PromotedKinds()
	if len(promoted) != 1 || promoted[0] != "Ingress" {
		t.Errorf("expected Ingress to be promoted, got %v", promoted)
	}
}

// TestNewResourceCache_MinimalSet_UnknownKey verifies the validation
// log path: a typo or kind not enabled in ResourceTypes results in an
// empty effective minimal set; the cache returns at PatienceWindow with
// nothing meaningfully gating first paint. This is intentionally
// permissive (we don't fail construction) but the operator must see a
// warning. We don't capture log output here — just verify the cache
// still returns and shape is consistent.
func TestNewResourceCache_MinimalSet_UnknownKey(t *testing.T) {
	client := fake.NewSimpleClientset()

	start := time.Now()
	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods: true,
		},
		PatienceWindow: 200 * time.Millisecond,
		MinimalSet: map[string]bool{
			"pod": true, // typo — should be "pods"
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	// Pods syncs instantly on fake client, so allCritical fires before
	// patience even elapses — promoted should be empty.
	if got := rc.PromotedKinds(); len(got) != 0 {
		t.Errorf("expected no promoted kinds (everything synced), got %v", got)
	}
	if rc.Pods() == nil {
		t.Error("expected Pods() lister to be available")
	}
	// Sanity: returned in reasonable time despite the bogus minimal-set key
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("returned far too late with bogus MinimalSet key: %v", elapsed)
	}
}

// TestNewResourceCache_PatienceWindow_WithoutMinimalSet verifies the
// edge case where only PatienceWindow is set: useMinimalSet is false,
// so behavior degrades to "wait indefinitely for all critical" with no
// hard cap. With a fake client that syncs instantly, this should just
// return on the all-synced path.
func TestNewResourceCache_PatienceWindow_WithoutMinimalSet(t *testing.T) {
	client := fake.NewSimpleClientset()

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods: true,
		},
		PatienceWindow: 100 * time.Millisecond,
		// MinimalSet intentionally nil
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	if got := rc.PromotedKinds(); len(got) != 0 {
		t.Errorf("expected no promotion without MinimalSet, got %v", got)
	}
	if rc.Pods() == nil {
		t.Error("expected Pods() lister to be available")
	}
}

// TestPendingPromotedKinds verifies the live-filtered accessor that the
// dashboard banner relies on: starts equal to PromotedKinds, and shrinks
// as informers eventually sync.
func TestPendingPromotedKinds(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "ingresses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("simulated slow API")
	})

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods:      true,
			Services:  true,
			Ingresses: true,
		},
		PatienceWindow: 200 * time.Millisecond,
		MinimalSet: map[string]bool{
			Pods:     true,
			Services: true,
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	// At first paint, Ingress is both Promoted and Pending.
	if got := rc.PromotedKinds(); len(got) != 1 || got[0] != "Ingress" {
		t.Fatalf("PromotedKinds: expected [Ingress], got %v", got)
	}
	if got := rc.PendingPromotedKinds(); len(got) != 1 || got[0] != "Ingress" {
		t.Errorf("PendingPromotedKinds: expected [Ingress], got %v", got)
	}
	// PromotedKinds is a stable historical snapshot, PendingPromotedKinds
	// is the live view — verify they are distinct concepts and don't
	// share backing storage.
	promoted := rc.PromotedKinds()
	pending := rc.PendingPromotedKinds()
	if len(promoted) > 0 && len(pending) > 0 && &promoted[0] == &pending[0] {
		t.Error("PromotedKinds and PendingPromotedKinds must not share backing array")
	}
}

// TestPendingPromotedKinds_Drains verifies the live-filtering claim: as
// a promoted informer eventually catches up and reports HasSynced=true,
// it leaves PendingPromotedKinds. PromotedKinds (the snapshot) does not
// change. Without this, a UI banner would list kinds forever even after
// they finished loading.
func TestPendingPromotedKinds_Drains(t *testing.T) {
	client := fake.NewSimpleClientset()

	// Toggle: fail Ingress LIST until we flip the flag, then succeed.
	// The reflector retries on backoff so HasSynced flips when LIST
	// stops failing.
	var failIngress atomic.Bool
	failIngress.Store(true)
	client.PrependReactor("list", "ingresses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if failIngress.Load() {
			return true, nil, fmt.Errorf("simulated transient failure")
		}
		return false, nil, nil // pass through to default tracker
	})

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods:      true,
			Services:  true,
			Ingresses: true,
		},
		PatienceWindow: 200 * time.Millisecond,
		MinimalSet: map[string]bool{
			Pods:     true,
			Services: true,
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	// Sanity: at construction, Ingress is pending.
	if got := rc.PendingPromotedKinds(); len(got) != 1 || got[0] != "Ingress" {
		t.Fatalf("PendingPromotedKinds at start: expected [Ingress], got %v", got)
	}

	// Flip the reactor to succeed, then poll for the live view to drain.
	failIngress.Store(false)

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(rc.PendingPromotedKinds()) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := rc.PendingPromotedKinds(); len(got) != 0 {
		t.Errorf("PendingPromotedKinds did not drain after Ingress LIST began succeeding; still pending: %v", got)
	}

	// PromotedKinds is the snapshot — must NOT shrink.
	if got := rc.PromotedKinds(); len(got) != 1 || got[0] != "Ingress" {
		t.Errorf("PromotedKinds (snapshot) should not shrink; expected [Ingress], got %v", got)
	}
}

// TestNewResourceCache_MinimalSet_BackstopFires covers the worst case on
// the patience+minimal-set path: a kind that's IN the minimal set never
// syncs. Without a backstop the cache would block in NewResourceCache
// forever, trapping the caller on a connecting screen. SyncTimeout is
// the safety net — it must promote everything still pending (including
// minimal-set members) and let the cache return.
func TestNewResourceCache_MinimalSet_BackstopFires(t *testing.T) {
	client := fake.NewSimpleClientset()
	// Pods is in MinimalSet — make its LIST fail forever.
	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("simulated permanently-stuck API")
	})

	start := time.Now()
	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods:     true,
			Services: true,
		},
		PatienceWindow: 100 * time.Millisecond,
		MinimalSet: map[string]bool{
			Pods:     true,
			Services: true,
		},
		SyncTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	elapsed := time.Since(start)
	if elapsed < 500*time.Millisecond {
		t.Errorf("expected return after at least SyncTimeout (500ms), got %v", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("returned far later than SyncTimeout — backstop didn't fire? elapsed=%v", elapsed)
	}

	// Pods stuck → must be promoted; Services synced → must not be promoted.
	promoted := rc.PromotedKinds()
	if len(promoted) != 1 || promoted[0] != "Pod" {
		t.Errorf("expected only Pod to be promoted by backstop, got %v", promoted)
	}

	// Lister still available (it's just empty).
	if rc.Pods() == nil {
		t.Error("expected Pods() lister to be available even after backstop promotion")
	}
}

func TestNewResourceCache_Callbacks(t *testing.T) {
	// Pre-create a pod so the informer fires an add event
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			UID:       "test-uid",
		},
	}
	client := fake.NewSimpleClientset(pod)

	var mu sync.Mutex
	var changes []ResourceChange

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods: true,
		},
		OnChange: func(change ResourceChange, obj, oldObj any) {
			mu.Lock()
			changes = append(changes, change)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	// Wait for the informer add event to propagate
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	got := len(changes)
	mu.Unlock()

	if got == 0 {
		t.Error("expected OnChange to be called for the pre-existing pod add")
	}
}

func TestNewResourceCache_SuppressInitialAdds(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-pod",
			Namespace: "default",
			UID:       "uid-1",
		},
	}
	client := fake.NewSimpleClientset(pod)

	var mu sync.Mutex
	var callbackChanges []ResourceChange

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods: true,
		},
		SuppressInitialAdds: true,
		OnChange: func(change ResourceChange, obj, oldObj any) {
			mu.Lock()
			callbackChanges = append(callbackChanges, change)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	// Wait briefly for any events
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	got := len(callbackChanges)
	mu.Unlock()

	// With SuppressInitialAdds, the OnChange callback should NOT fire for
	// pre-existing resources during sync. However, the add events still
	// go to the changes channel. Note: since NewResourceCache blocks until
	// sync completes, the add fires DURING construction when syncComplete
	// is still false, so callback should be suppressed.
	if got != 0 {
		t.Errorf("expected 0 callback changes with SuppressInitialAdds, got %d", got)
	}
}

func TestNewResourceCache_OnReceived(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			UID:       "test-uid",
		},
	}
	client := fake.NewSimpleClientset(pod)

	var mu sync.Mutex
	receivedKinds := map[string]int{}

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods: true,
		},
		OnReceived: func(kind string) {
			mu.Lock()
			receivedKinds[kind]++
			mu.Unlock()
		},
		// Even with noisy filter that always returns true, OnReceived should fire
		IsNoisyResource: func(kind, name, op string) bool {
			return true // everything is noisy
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	podCount := receivedKinds["Pod"]
	mu.Unlock()

	if podCount == 0 {
		t.Error("expected OnReceived to fire even when IsNoisyResource returns true")
	}
}

func TestNewResourceCache_NamespaceScopedValidation(t *testing.T) {
	client := fake.NewSimpleClientset()

	_, err := NewResourceCache(CacheConfig{
		Client:          client,
		NamespaceScoped: true,
		Namespace:       "", // empty namespace with NamespaceScoped=true
		ResourceTypes:   map[string]bool{Pods: true},
	})
	if err == nil {
		t.Fatal("expected error when NamespaceScoped=true with empty Namespace")
	}
}

func TestNewResourceCache_CallbackPanicRecovery(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "panic-pod",
			Namespace: "default",
			UID:       "panic-uid",
		},
	}
	client := fake.NewSimpleClientset(pod)

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods: true,
		},
		OnChange: func(change ResourceChange, obj, oldObj any) {
			panic("test panic in OnChange")
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	// Wait for event to fire — the panic should be recovered, not crash
	time.Sleep(200 * time.Millisecond)

	// If we get here without crashing, the test passes
	if rc.Pods() == nil {
		t.Error("expected Pods() lister to still work after callback panic")
	}
}

func TestNewResourceCache_MapCloning(t *testing.T) {
	client := fake.NewSimpleClientset()

	resourceTypes := map[string]bool{
		Pods:     true,
		Services: true,
	}

	rc, err := NewResourceCache(CacheConfig{
		Client:        client,
		ResourceTypes: resourceTypes,
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	// Mutate the original map after construction
	resourceTypes[Pods] = false
	resourceTypes["bogus"] = true

	// The cache should not be affected
	if rc.Pods() == nil {
		t.Error("expected Pods() lister to still work after caller mutates resourceTypes map")
	}
	enabled := rc.GetEnabledResources()
	if !enabled[Pods] {
		t.Error("expected Pods to still be enabled after caller mutates resourceTypes map")
	}
}

func TestNewResourceCache_NilClient(t *testing.T) {
	_, err := NewResourceCache(CacheConfig{
		Client: nil,
	})
	if err == nil {
		t.Fatal("expected error with nil client")
	}
}

func TestNewResourceCache_NoEnabledResources(t *testing.T) {
	client := fake.NewSimpleClientset()

	rc, err := NewResourceCache(CacheConfig{
		Client:        client,
		ResourceTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer rc.Stop()

	if !rc.IsSyncComplete() {
		t.Error("expected IsSyncComplete() = true even with no resources")
	}
	if rc.GetResourceCount() != 0 {
		t.Errorf("expected 0 resource count, got %d", rc.GetResourceCount())
	}
}

func TestNewResourceCache_StopLifecycle(t *testing.T) {
	client := fake.NewSimpleClientset()

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Pods: true,
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}

	// Stop should be safe to call multiple times
	rc.Stop()
	rc.Stop()

	// Methods should be safe to call after stop
	_ = rc.Pods()
	_ = rc.Changes()
	_ = rc.GetResourceCount()
}

func TestDropManagedFields(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			ManagedFields: []metav1.ManagedFieldsEntry{
				{Manager: "test-manager"},
			},
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": `{"big":"json"}`,
				"keep-this": "yes",
			},
		},
	}

	result, err := DropManagedFields(pod)
	if err != nil {
		t.Fatalf("DropManagedFields failed: %v", err)
	}

	p := result.(*corev1.Pod)
	if len(p.ManagedFields) != 0 {
		t.Error("expected managedFields to be nil/empty")
	}
	if _, ok := p.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
		t.Error("expected last-applied-configuration annotation to be removed")
	}
	if p.Annotations["keep-this"] != "yes" {
		t.Error("expected other annotations to be preserved")
	}
}

func TestDropManagedFields_Event(t *testing.T) {
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-event",
			Namespace: "default",
			UID:       "event-uid",
			ManagedFields: []metav1.ManagedFieldsEntry{
				{Manager: "test-manager"},
			},
			Labels: map[string]string{"should": "be-stripped"},
		},
		Reason:  "Created",
		Message: "Pod created",
		Type:    "Normal",
		Count:   1,
	}

	result, err := DropManagedFields(event)
	if err != nil {
		t.Fatalf("DropManagedFields failed: %v", err)
	}

	e := result.(*corev1.Event)
	if e.Reason != "Created" {
		t.Errorf("expected Reason=Created, got %s", e.Reason)
	}
	if e.Labels != nil {
		t.Error("expected Labels to be stripped from event")
	}
	if len(e.ManagedFields) != 0 {
		t.Error("expected managedFields to be stripped from event")
	}
}

// TestNewResourceCache_ResourceScopesMixed verifies a mixed-scope user
// (some kinds cluster-wide, some kinds namespace-scoped) ends up with
// informers wired into the right factories. The fake client lets every
// list succeed; the assertion is structural — that listers exist for
// every enabled kind and the per-namespace factory is created.
func TestNewResourceCache_ResourceScopesMixed(t *testing.T) {
	const ns = "dev-ns-1"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: ns, UID: "u"},
	}
	client := fake.NewSimpleClientset(pod)

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceScopes: map[string]ResourceScope{
			Pods:        {Enabled: true, Namespace: ns}, // namespace-scoped
			Deployments: {Enabled: true, Namespace: ns}, // namespace-scoped
			Nodes:       {Enabled: true, Namespace: ""}, // cluster-wide (cluster-scoped kind)
			Services:    {Enabled: false},               // denied — no informer
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	if rc.Pods() == nil {
		t.Error("Pods lister should exist for an enabled kind")
	}
	if rc.Deployments() == nil {
		t.Error("Deployments lister should exist for an enabled kind")
	}
	if rc.Nodes() == nil {
		t.Error("Nodes lister should exist for an enabled kind")
	}
	if rc.Services() != nil {
		t.Error("Services lister should be nil — kind was disabled")
	}

	enabled := rc.GetEnabledResources()
	if !enabled[Pods] || !enabled[Deployments] || !enabled[Nodes] {
		t.Errorf("enabled map missing kinds: %+v", enabled)
	}
	if enabled[Services] {
		t.Errorf("disabled Services should not appear in enabled map")
	}

	// Internal: a namespace-scoped factory should have been created for
	// the namespace used by Pods/Deployments.
	if _, ok := rc.nsFactories[ns]; !ok {
		t.Errorf("expected nsFactories to contain %q, got keys %v", ns, mapKeys(rc.nsFactories))
	}
}

// TestNewResourceCache_ResourceScopesEmpty verifies that an explicitly
// empty ResourceScopes map (no kinds enabled) produces a cache with no
// informers, vs nil ResourceScopes which falls through to the legacy
// ResourceTypes path.
func TestNewResourceCache_ResourceScopesEmpty(t *testing.T) {
	client := fake.NewSimpleClientset()

	rc, err := NewResourceCache(CacheConfig{
		Client:         client,
		ResourceScopes: map[string]ResourceScope{}, // explicitly empty
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	if rc.Pods() != nil {
		t.Error("expected no Pods lister when ResourceScopes is empty")
	}
}

func TestNewResourceCache_RbacResources(t *testing.T) {
	client := fake.NewSimpleClientset(
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "dev"}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "cluster-reader"}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "reader-bind", Namespace: "dev"}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "cluster-reader-bind"}},
	)

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceScopes: map[string]ResourceScope{
			Roles:               {Enabled: true, Namespace: "dev"},
			ClusterRoles:        {Enabled: true},
			RoleBindings:        {Enabled: true, Namespace: "dev"},
			ClusterRoleBindings: {Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache failed: %v", err)
	}
	defer rc.Stop()

	if rc.Roles() == nil || rc.ClusterRoles() == nil || rc.RoleBindings() == nil || rc.ClusterRoleBindings() == nil {
		t.Fatalf("expected all RBAC listers to be non-nil")
	}

	counts := rc.GetKindObjectCounts()
	for _, kind := range []string{"Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding"} {
		if counts[kind] == 0 {
			t.Fatalf("expected %s to be counted, got counts=%v", kind, counts)
		}
	}

	enabled := rc.GetEnabledResources()
	for _, key := range []string{Roles, ClusterRoles, RoleBindings, ClusterRoleBindings} {
		if !enabled[key] {
			t.Fatalf("expected %s to be enabled, got %v", key, enabled)
		}
	}
}

func mapKeys[K comparable, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestRecordWatchError verifies that the WatchErrorHandler hook updates the
// per-informer status when a 403 surfaces from the reflector. Validates the
// observable behavior of the safety net rather than relying on simulating a
// full reflector watch failure (which is racy with the fake client).
func TestRecordWatchError(t *testing.T) {
	rc := &ResourceCache{
		stdlog: log.New(io.Discard, "", 0),
		informerStatuses: []InformerSyncStatus{
			{Kind: "Pod", Key: Pods},
			{Kind: "Service", Key: Services},
		},
	}

	rc.recordWatchError(0, Pods, "Pod", apierrors.NewForbidden(
		schema.GroupResource{Resource: "pods"}, "", nil,
	))

	rc.informerMu.RLock()
	defer rc.informerMu.RUnlock()
	pod := rc.informerStatuses[0]
	if !pod.ForbiddenSeen {
		t.Errorf("ForbiddenSeen should be true after a 403")
	}
	if pod.LastError == "" {
		t.Errorf("LastError should be set")
	}
	if pod.LastErrorAt == "" {
		t.Errorf("LastErrorAt should be set")
	}
	// Untouched informer should remain pristine.
	svc := rc.informerStatuses[1]
	if svc.ForbiddenSeen || svc.LastError != "" {
		t.Errorf("Service status should not be affected by Pod's 403, got %+v", svc)
	}
}

func TestRecordWatchError_TransientNotForbidden(t *testing.T) {
	rc := &ResourceCache{
		stdlog: log.New(io.Discard, "", 0),
		informerStatuses: []InformerSyncStatus{
			{Kind: "Pod", Key: Pods},
		},
	}

	rc.recordWatchError(0, Pods, "Pod", errors.New("dial tcp: connection refused"))

	rc.informerMu.RLock()
	defer rc.informerMu.RUnlock()
	if rc.informerStatuses[0].ForbiddenSeen {
		t.Errorf("ForbiddenSeen should NOT be set for a transient connection error")
	}
	if rc.informerStatuses[0].LastError == "" {
		t.Errorf("LastError should still be recorded for transient errors")
	}
}

func TestDynamicResourceCache_NamespaceFallbackIsPerGVR(t *testing.T) {
	const ns = "dev-ns-1"
	clusterGVR := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "clusterthings"}
	namespacedGVR := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "namespacedthings"}
	dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{
		clusterGVR:    "ClusterThingList",
		namespacedGVR: "NamespacedThingList",
	}, func(gvr schema.GroupVersionResource, namespace string) bool {
		if gvr == namespacedGVR {
			return namespace == ns
		}
		return namespace == ""
	})

	d, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient:     dyn,
		NamespaceFallback: ns,
	})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}

	clusterScope, err := d.probeScope(clusterGVR, "")
	if err != nil {
		t.Fatalf("cluster GVR probe failed: %v", err)
	}
	if clusterScope != "" {
		t.Errorf("cluster GVR scope = %q, want cluster-wide", clusterScope)
	}

	nsScope, err := d.probeScope(namespacedGVR, "")
	if err != nil {
		t.Fatalf("namespaced GVR probe failed: %v", err)
	}
	if nsScope != ns {
		t.Errorf("namespaced GVR scope = %q, want %q", nsScope, ns)
	}
}

func TestDynamicResourceCache_ForcedNamespaceScopesEveryGVR(t *testing.T) {
	const ns = "dev-ns-1"
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{
		gvr: "WidgetList",
	}, func(_ schema.GroupVersionResource, namespace string) bool {
		return namespace == ns
	})

	d, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient:   dyn,
		NamespaceScoped: true,
		Namespace:       ns,
	})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}

	scope, err := d.probeScope(gvr, "")
	if err != nil {
		t.Fatalf("forced namespace probe failed: %v", err)
	}
	if scope != ns {
		t.Errorf("GVR scope = %q, want %q", scope, ns)
	}
}

// The #768 core: when cluster-wide list is denied but the caller names a
// specific namespace they CAN list, the probe scopes to that namespace —
// not to the configured single fallback. This is what lets a namespace-
// restricted user read a CRD in the namespaces they actually have access to.
func TestDynamicResourceCache_ProbeScope_HonorsRequestedNamespace(t *testing.T) {
	const fallbackNs, wantedNs = "fallback-ns", "argocd"
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{
		gvr: "ApplicationList",
	}, func(_ schema.GroupVersionResource, namespace string) bool {
		return namespace == wantedNs // not cluster-wide, not the fallback
	})

	d, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient:     dyn,
		NamespaceFallback: fallbackNs,
	})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}

	// Requesting the namespace the user can list scopes the informer there.
	scope, err := d.probeScope(gvr, wantedNs)
	if err != nil {
		t.Fatalf("probeScope(%q) failed: %v", wantedNs, err)
	}
	if scope != wantedNs {
		t.Errorf("scope = %q, want %q", scope, wantedNs)
	}

	// With no requested namespace, it falls back to the configured fallback,
	// which the user cannot list — so the probe is forbidden.
	if _, err := d.probeScope(gvr, ""); !apierrors.IsForbidden(err) {
		t.Errorf("probeScope(\"\") err = %v, want forbidden", err)
	}
}

// List(gvr, "") must be served ONLY by a cluster-wide informer — it must not
// union incidental per-namespace informers, which would make results depend on
// what other requests warmed. ListNamespaces is the explicit union path.
func TestDynamicResourceCache_ListEmptyNamespaceDoesNotUnion(t *testing.T) {
	const nsA, nsB = "team-a", "team-b"
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{
		gvr: "WidgetList",
	}, func(_ schema.GroupVersionResource, namespace string) bool {
		return namespace == nsA || namespace == nsB // namespaced only, never cluster-wide
	})

	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}

	// Warm per-namespace informers for both namespaces.
	if _, err := d.ListBlocking(gvr, nsA, 2*time.Second); err != nil {
		t.Fatalf("ListBlocking(%q) failed: %v", nsA, err)
	}
	if _, err := d.ListBlocking(gvr, nsB, 2*time.Second); err != nil {
		t.Fatalf("ListBlocking(%q) failed: %v", nsB, err)
	}

	// List(gvr, "") finds no cluster-wide informer and must NOT union the two
	// per-namespace informers.
	if got := d.readEntries(gvr, ""); got != nil {
		t.Errorf("readEntries(gvr, \"\") returned %d entries, want none (no cluster-wide informer)", len(got))
	}
}

// Mirror of ListEmptyNamespaceDoesNotUnion for namespace-scoped mode: when Radar
// connects with a namespace-restricted identity (NamespaceFallback set),
// cluster-wide list is denied so a GVR's only informers are per-namespace. An
// all-namespaces read — List(gvr, "") and Count(gvr, nil) — MUST union them, or
// namespaced CRDs show 0 in the UI despite synced per-namespace informers. The
// NamespaceFallback gate is what keeps the no-union contract intact for a
// cluster-wide cache (the sibling test) while fixing the namespace-scoped case.
func TestDynamicResourceCache_ListEmptyNamespaceUnionsWhenNamespaceScoped(t *testing.T) {
	const nsA, nsB = "team-a", "team-b"
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{
		gvr: "WidgetList",
	}, func(_ schema.GroupVersionResource, namespace string) bool {
		return namespace == nsA || namespace == nsB // namespaced only, never cluster-wide
	})
	for _, ns := range []string{nsA, nsB} {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "example.com/v1",
			"kind":       "Widget",
			"metadata":   map[string]any{"name": "w-" + ns, "namespace": ns},
		}}
		if _, err := dyn.Resource(gvr).Namespace(ns).Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed %s: %v", ns, err)
		}
	}

	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, NamespaceFallback: nsA})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}
	for _, ns := range []string{nsA, nsB} {
		if _, err := d.ListBlocking(gvr, ns, 2*time.Second); err != nil {
			t.Fatalf("ListBlocking(%q) failed: %v", ns, err)
		}
	}

	got, err := d.List(gvr, "")
	if err != nil {
		t.Fatalf("List(gvr, \"\") failed: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("List(gvr, \"\") = %d objects, want 2 (union of per-namespace informers in namespace-scoped mode)", len(got))
	}

	n, err := d.Count(gvr, nil)
	if err != nil {
		t.Fatalf("Count(gvr, nil) failed: %v", err)
	}
	if n != 2 {
		t.Errorf("Count(gvr, nil) = %d, want 2", n)
	}
}

// ListWatched is the internal "scan what's already cached" path: it unions
// every watched scope so namespace-restricted callers (audit, PolicyReport
// indexing) don't silently drop namespace-scoped contents the way List(gvr,
// "") would.
func TestDynamicResourceCache_ListWatchedUnionsNamespaceInformers(t *testing.T) {
	const nsA, nsB = "team-a", "team-b"
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{
		gvr: "WidgetList",
	}, func(_ schema.GroupVersionResource, namespace string) bool {
		return namespace == nsA || namespace == nsB // namespaced only, never cluster-wide
	})

	// Seed one object in each namespace so the informers have content.
	for _, ns := range []string{nsA, nsB} {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "example.com/v1",
			"kind":       "Widget",
			"metadata":   map[string]any{"name": "w-" + ns, "namespace": ns},
		}}
		if _, err := dyn.Resource(gvr).Namespace(ns).Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed %s: %v", ns, err)
		}
	}

	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}
	for _, ns := range []string{nsA, nsB} {
		if _, err := d.ListBlocking(gvr, ns, 2*time.Second); err != nil {
			t.Fatalf("ListBlocking(%q) failed: %v", ns, err)
		}
	}

	got, err := d.ListWatched(gvr)
	if err != nil {
		t.Fatalf("ListWatched failed: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("ListWatched returned %d objects, want 2 (union of both namespace-scoped informers)", len(got))
	}
}

// ListNamespaces must short-circuit a cluster-scoped GVR to a cluster-wide
// read even when given a namespace set: cluster-scoped objects live under the
// "" namespace, so a per-namespace filter would index them to nothing. Pins
// the gvrIsNamespaced guard the topology paths rely on for Karpenter
// NodePool/NodeClaim and other cluster-scoped CRDs.
func TestDynamicResourceCache_ListNamespacesClusterScopedReadsClusterWide(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
	disc := &ResourceDiscovery{
		resources: []APIResource{{
			Group: gvr.Group, Version: gvr.Version, Kind: "NodePool", Name: gvr.Resource,
			Namespaced: false, IsCRD: true, Verbs: []string{"get", "list", "watch"},
		}},
		resourceMap: map[string]APIResource{},
		gvrMap:      map[string]schema.GroupVersionResource{},
		lastRefresh: time.Now(),
		cacheTTL:    time.Hour,
	}
	dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{
		gvr: "NodePoolList",
	}, func(schema.GroupVersionResource, string) bool { return true }) // cluster-wide allowed

	// Seed a cluster-scoped object (no namespace).
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodePool",
		"metadata":   map[string]any{"name": "default"},
	}}
	if _, err := dyn.Resource(gvr).Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed cluster-scoped object: %v", err)
	}

	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, Discovery: disc})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}
	if _, err := d.ListBlocking(gvr, "", 2*time.Second); err != nil {
		t.Fatalf("ListBlocking failed: %v", err)
	}

	// A namespace filter must NOT zero out a cluster-scoped resource.
	got, err := d.ListNamespaces(gvr, []string{"ns-a"})
	if err != nil {
		t.Fatalf("ListNamespaces failed: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("ListNamespaces(clusterScopedGVR, [ns-a]) = %d objects, want 1 (cluster-wide short-circuit)", len(got))
	}
}

// Starting a cluster-wide informer must supersede (stop + drop) any
// namespace-scoped informers for the same GVR — the "never both" invariant —
// so reads (ListWatched) and change callbacks don't double up.
func TestDynamicResourceCache_ClusterWideSupersedesNamespaceInformers(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{
		gvr: "WidgetList",
	}, func(schema.GroupVersionResource, string) bool { return true })
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}
	defer d.Stop()

	has := func(ns string) bool {
		d.mu.RLock()
		defer d.mu.RUnlock()
		_, ok := d.informers[informerKey{gvr: gvr, ns: ns}]
		return ok
	}

	if err := d.startWatching(gvr, "team-a"); err != nil {
		t.Fatalf("startWatching(team-a): %v", err)
	}
	if err := d.startWatching(gvr, "team-b"); err != nil {
		t.Fatalf("startWatching(team-b): %v", err)
	}
	if !has("team-a") || !has("team-b") {
		t.Fatal("expected both namespace-scoped informers before supersede")
	}

	if err := d.startWatching(gvr, ""); err != nil {
		t.Fatalf("startWatching(cluster-wide): %v", err)
	}
	if !has("") {
		t.Error("expected cluster-wide informer after supersede")
	}
	if has("team-a") || has("team-b") {
		t.Error("namespace-scoped informers must be superseded by the cluster-wide one")
	}
}

// A handler registered via AddGVRChangeHandler must reach informers created
// AFTER registration (lazy per-namespace watches, reap re-creations) — not
// only those present at call time — or derived caches miss those events.
func TestDynamicResourceCache_GVRChangeHandlerAppliesToLaterInformers(t *testing.T) {
	const nsA, nsB = "team-a", "team-b"
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{
		gvr: "WidgetList",
	}, func(_ schema.GroupVersionResource, ns string) bool { return ns != "" }) // namespaced only

	// Seed an object in nsB so its (later-created) informer has an add to deliver.
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"name": "w", "namespace": nsB},
	}}
	if _, err := dyn.Resource(gvr).Namespace(nsB).Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed nsB: %v", err)
	}

	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}
	defer d.Stop()

	// Warm nsA so the handler has an existing informer to attach to at registration.
	if _, err := d.ListBlocking(gvr, nsA, 2*time.Second); err != nil {
		t.Fatalf("ListBlocking(nsA): %v", err)
	}

	var adds atomic.Int64
	h := cache.ResourceEventHandlerFuncs{AddFunc: func(any) { adds.Add(1) }}
	if err := d.AddGVRChangeHandler(gvr, h); err != nil {
		t.Fatalf("AddGVRChangeHandler: %v", err)
	}

	// nsB's informer is created lazily after registration; it must still get h.
	if _, err := d.ListBlocking(gvr, nsB, 2*time.Second); err != nil {
		t.Fatalf("ListBlocking(nsB): %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && adds.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if adds.Load() == 0 {
		t.Error("handler did not fire for an informer created after registration")
	}
}

// GVR-level status APIs must span namespace-scoped informers: with only
// per-namespace watches (no cluster-wide one), the kind is still watched and
// synced, so IsSynced/WaitForSync must report true and AddGVRChangeHandler
// must register — they resolve via entriesForGVR, not readEntries(gvr, "").
func TestDynamicResourceCache_StatusAPIsSpanNamespaceInformers(t *testing.T) {
	const ns = "team-a"
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{
		gvr: "WidgetList",
	}, func(_ schema.GroupVersionResource, namespace string) bool {
		return namespace == ns // namespaced only, never cluster-wide
	})

	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}

	if _, err := d.ListBlocking(gvr, ns, 2*time.Second); err != nil {
		t.Fatalf("ListBlocking(%q) failed: %v", ns, err)
	}

	if !d.IsSynced(gvr) {
		t.Error("IsSynced = false for a GVR watched only via a namespace-scoped informer, want true")
	}
	if !d.WaitForSync(gvr, 2*time.Second) {
		t.Error("WaitForSync = false for a namespace-scoped-only GVR, want true")
	}
	if err := d.AddGVRChangeHandler(gvr, cache.ResourceEventHandlerFuncs{}); err != nil {
		t.Errorf("AddGVRChangeHandler failed for a namespace-scoped-only GVR: %v", err)
	}

	// A subsequent List(gvr, "") must NOT short-circuit on the existing
	// namespace-scoped informer and then find nothing. With no cluster-wide
	// informer it re-probes cluster-wide (denied here) and returns a clean
	// forbidden — never a spurious "informer not found".
	_, err = d.List(gvr, "")
	if err == nil {
		t.Fatal("List(gvr, \"\") = nil error, want forbidden (cluster-wide denied)")
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("List(gvr, \"\") err = %v, want a re-probed forbidden (not 'informer not found')", err)
	}
}

// EnsureWatching must surface a probe that's forbidden everywhere it looks
// (cluster-wide AND the fallback namespace) as an apierrors.IsForbidden-
// classifiable error through the %w wrap — that's what lets the resources
// handler map a denied CRD list to 403 instead of 500. Mirrors #768: a user
// who can read the CRD only in a namespace the cache never probes.
func TestDynamicResourceCache_EnsureWatching_ForbiddenIsClassifiable(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{
		gvr: "ApplicationList",
	}, func(schema.GroupVersionResource, string) bool { return false })

	d, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient:     dyn,
		NamespaceFallback: "other-ns",
	})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}

	err = d.EnsureWatching(gvr)
	if err == nil {
		t.Fatal("EnsureWatching succeeded, want forbidden error")
	}
	if !apierrors.IsForbidden(err) {
		t.Fatalf("EnsureWatching error not classifiable as forbidden (handler would 500 instead of 403): %v", err)
	}
}

func fakeDynamicForListAccess(
	t *testing.T,
	listKinds map[schema.GroupVersionResource]string,
	allow func(schema.GroupVersionResource, string) bool,
) *dynamicfake.FakeDynamicClient {
	t.Helper()

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	dyn.PrependReactor("list", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		listAction, ok := action.(k8stesting.ListAction)
		if !ok {
			return false, nil, nil
		}
		gvr := listAction.GetResource()
		if allow(gvr, listAction.GetNamespace()) {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, "", nil)
	})
	return dyn
}
