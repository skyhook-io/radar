package k8s

import (
	"context"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func newFakeClient() *fake.Clientset {
	return fake.NewClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
		},
	)
}

// TestInitResetReinit verifies the full lifecycle: init → use → reset → re-init.
// This catches sync.Once reset bugs (the exact pattern we changed from value to
// pointer type in 6 files). If the Once isn't properly reset, re-init silently
// becomes a no-op and the cache stays nil.
func TestInitResetReinit(t *testing.T) {
	defer ResetTestState()

	// First init
	if err := InitTestResourceCache(newFakeClient()); err != nil {
		t.Fatalf("first init: %v", err)
	}
	cache1 := GetResourceCache()
	if cache1 == nil {
		t.Fatal("cache nil after first init")
	}
	if n := cache1.GetResourceCount(); n == 0 {
		t.Fatal("cache empty after first init")
	}

	// Reset
	ResetResourceCache()
	if c := GetResourceCache(); c != nil {
		t.Fatal("cache not nil after reset")
	}

	// Re-init (tests that sync.Once was properly reset)
	if err := InitTestResourceCache(newFakeClient()); err != nil {
		t.Fatalf("re-init: %v", err)
	}
	cache2 := GetResourceCache()
	if cache2 == nil {
		t.Fatal("cache nil after re-init — sync.Once not reset?")
	}
	if n := cache2.GetResourceCount(); n == 0 {
		t.Fatal("cache empty after re-init")
	}
}

// TestStopIsNonBlocking verifies that cache Stop() returns quickly instead of
// blocking on factory.Shutdown(). The old code blocked until all informer
// goroutines drained, which could take 30+ seconds with stuck exec plugins.
func TestStopIsNonBlocking(t *testing.T) {
	defer ResetTestState()

	if err := InitTestResourceCache(newFakeClient()); err != nil {
		t.Fatalf("init: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil")
	}

	done := make(chan struct{})
	go func() {
		cache.Stop()
		close(done)
	}()

	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() blocked for >2s — should be non-blocking")
	}
}

// TestDoubleResetNoPanic verifies that calling ResetAllSubsystems twice
// doesn't panic (e.g., from double-close on channels or nil pointer deref).
func TestDoubleResetNoPanic(t *testing.T) {
	defer ResetTestState()

	if err := InitTestResourceCache(newFakeClient()); err != nil {
		t.Fatalf("init: %v", err)
	}

	// First reset — normal teardown
	ResetAllSubsystems()

	// Second reset — everything is already nil, should be safe
	ResetAllSubsystems()
}

// TestInitCancelReturnsPromptly verifies that InitResourceCache respects
// context cancellation and returns quickly instead of blocking on RBAC checks
// or informer sync. This catches the bug where discoveryWg.Wait() blocked
// for 39 seconds after the context was canceled.
func TestInitCancelReturnsPromptly(t *testing.T) {
	defer ResetTestState()

	// Create a real clientset pointing to a non-existent server.
	// We just need k8sClient != nil so InitResourceCache doesn't bail
	// at the nil check. RBAC calls will bail at ctx.Err() first.
	dummyClient, err := kubernetes.NewForConfig(&rest.Config{Host: "http://localhost:1"})
	if err != nil {
		t.Fatalf("creating dummy client: %v", err)
	}
	clientMu.Lock()
	k8sClient = dummyClient
	clientMu.Unlock()
	defer func() {
		clientMu.Lock()
		k8sClient = nil
		clientMu.Unlock()
	}()

	// Cancel the context before calling init
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		done <- InitResourceCache(ctx)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from canceled context, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("InitResourceCache blocked >3s after context cancel")
	}
}

// TestConcurrentInvalidateDuringPermissionCheck verifies that
// InvalidateResourcePermissionsCache does not deadlock when called
// concurrently with CheckResourcePermissions. The old code held
// resourcePermsMu.Lock() for the entire duration of network RBAC
// calls, blocking InvalidateResourcePermissionsCache during context switch.
func TestConcurrentInvalidateDuringPermissionCheck(t *testing.T) {
	defer ResetTestState()

	// Create a real clientset so CheckResourcePermissions runs the RBAC path.
	// Points to a non-existent server — RBAC calls will fail fast, which is fine;
	// we're testing lock contention, not RBAC correctness.
	dummyClient, err := kubernetes.NewForConfig(&rest.Config{Host: "http://localhost:1"})
	if err != nil {
		t.Fatalf("creating dummy client: %v", err)
	}
	clientMu.Lock()
	k8sClient = dummyClient
	clientMu.Unlock()
	defer func() {
		clientMu.Lock()
		k8sClient = nil
		clientMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run CheckResourcePermissions and InvalidateResourcePermissionsCache
	// concurrently. With the old locking pattern this would deadlock.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		CheckResourcePermissions(ctx)
	}()

	go func() {
		defer wg.Done()
		// Small delay so the permission check has time to start
		time.Sleep(10 * time.Millisecond)
		InvalidateResourcePermissionsCache()
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// good — no deadlock
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: InvalidateResourcePermissionsCache blocked by CheckResourcePermissions")
	}
}

// TestCacheUsableAfterReset verifies that after a full reset cycle, the cache
// can serve data from a new client. This simulates what happens during context
// switch: old cluster data is cleared and new cluster data becomes available.
func TestCacheUsableAfterReset(t *testing.T) {
	defer ResetTestState()

	// Init with a pod named "old-pod"
	client1 := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "old-pod", Namespace: "default"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
		},
	)
	if err := InitTestResourceCache(client1); err != nil {
		t.Fatalf("init 1: %v", err)
	}
	cache1 := GetResourceCache()
	pods1, _ := cache1.Pods().List(labels.Everything())
	if len(pods1) != 1 || pods1[0].Name != "old-pod" {
		t.Fatalf("expected old-pod, got %v", pods1)
	}

	// Reset and re-init with a different pod
	ResetResourceCache()
	client2 := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "new-pod", Namespace: "default"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
		},
	)
	if err := InitTestResourceCache(client2); err != nil {
		t.Fatalf("init 2: %v", err)
	}
	cache2 := GetResourceCache()
	pods2, _ := cache2.Pods().List(labels.Everything())
	if len(pods2) != 1 || pods2[0].Name != "new-pod" {
		t.Fatalf("expected new-pod after re-init, got %v", pods2)
	}
}
