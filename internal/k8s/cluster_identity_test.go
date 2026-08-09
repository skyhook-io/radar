package k8s

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// TestClusterUIDIdentity pins the in-cluster consent-identity fallback: the
// kube-system namespace UID is the stable per-cluster identifier, and any
// failure to read it yields "" - callers treat empty as "no stable identity"
// and must never persist consent under a shared fallback key.
func TestClusterUIDIdentity(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID("11111111-2222-3333-4444-555555555555")},
	})
	if got := clusterUIDIdentity(context.Background(), client); got != "cluster-11111111-2222-3333-4444-555555555555" {
		t.Errorf("clusterUIDIdentity = %q, want the kube-system UID form", got)
	}

	// Read denied / namespace missing → no identity, never a made-up one.
	if got := clusterUIDIdentity(context.Background(), fake.NewSimpleClientset()); got != "" {
		t.Errorf("missing kube-system must yield empty identity, got %q", got)
	}

	denied := fake.NewSimpleClientset()
	denied.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("forbidden")
	})
	if got := clusterUIDIdentity(context.Background(), denied); got != "" {
		t.Errorf("a denied read must yield empty identity, got %q", got)
	}

	// A namespace with an empty UID (should not happen, but an empty identity
	// component would collapse every such cluster onto one key) → empty.
	noUID := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}})
	if got := clusterUIDIdentity(context.Background(), noUID); got != "" {
		t.Errorf("empty UID must yield empty identity, got %q", got)
	}
}

// countingDeniedClient returns a fake client whose kube-system Get always fails,
// plus a counter of how many namespace GETs actually hit the (fake) apiserver.
func countingDeniedClient() (*fake.Clientset, *int) {
	calls := 0
	c := fake.NewSimpleClientset()
	c.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		calls++
		return true, nil, fmt.Errorf("forbidden")
	})
	return c, &calls
}

// resetClusterUIDCaches clears the per-context memo state this test mutates so
// runs stay order-independent.
func resetClusterUIDCaches(name string) {
	clusterUIDMu.Lock()
	delete(clusterUIDCache, name)
	delete(clusterUIDRetryAt, name)
	clusterUIDMu.Unlock()
}

// TestCachedClusterUIDIdentity_NegativeCache pins the failed-lookup memo: an
// install that can't read kube-system must not re-issue the doomed GET on every
// call (one per resource navigation), but the failure memory expires so a fixed
// permission self-heals without a restart.
func TestCachedClusterUIDIdentity_NegativeCache(t *testing.T) {
	const name = "test-negative-cache"
	resetClusterUIDCaches(name)
	t.Cleanup(func() { resetClusterUIDCaches(name) })

	denied, calls := countingDeniedClient()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if got := cachedClusterUIDIdentity(ctx, name, denied); got != "" {
			t.Fatalf("call %d: denied lookup must yield empty identity, got %q", i, got)
		}
	}
	if *calls != 1 {
		t.Errorf("3 calls within the negative TTL must issue exactly 1 API GET, got %d", *calls)
	}

	// TTL expiry → the lookup retries, and a now-working cluster self-heals to a
	// real identity (which also clears the failure memory).
	clusterUIDMu.Lock()
	clusterUIDRetryAt[name] = time.Now().Add(-time.Second)
	clusterUIDMu.Unlock()
	working := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID("aaaa-bbbb")},
	})
	if got := cachedClusterUIDIdentity(ctx, name, working); got != "cluster-aaaa-bbbb" {
		t.Errorf("after TTL expiry a working lookup must self-heal, got %q", got)
	}
	clusterUIDMu.Lock()
	_, negativeRemains := clusterUIDRetryAt[name]
	clusterUIDMu.Unlock()
	if negativeRemains {
		t.Error("a successful lookup must clear the negative entry")
	}

	// The success is memoized forever (the UID is immutable): a later call never
	// re-reads, even with a client that would now fail.
	deniedAgain, callsAgain := countingDeniedClient()
	if got := cachedClusterUIDIdentity(ctx, name, deniedAgain); got != "cluster-aaaa-bbbb" {
		t.Errorf("cached identity must be served without a lookup, got %q", got)
	}
	if *callsAgain != 0 {
		t.Errorf("a cached success must issue no API GET, got %d", *callsAgain)
	}
}

// TestCachedClusterUIDIdentity_NilClientNotNegativelyCached: a nil client means
// no API call was made, so it must not start the failure TTL - the next call
// with a real client must be allowed to look up immediately.
func TestCachedClusterUIDIdentity_NilClientNotNegativelyCached(t *testing.T) {
	const name = "test-nil-client"
	resetClusterUIDCaches(name)
	t.Cleanup(func() { resetClusterUIDCaches(name) })

	ctx := context.Background()
	if got := cachedClusterUIDIdentity(ctx, name, nil); got != "" {
		t.Fatalf("nil client must yield empty identity, got %q", got)
	}
	working := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID("cccc-dddd")},
	})
	if got := cachedClusterUIDIdentity(ctx, name, working); got != "cluster-cccc-dddd" {
		t.Errorf("a nil-client miss must not block the next real lookup, got %q", got)
	}
}
