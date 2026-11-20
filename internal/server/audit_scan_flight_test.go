package server

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
)

// fakeAuditScans replaces the scan with one that counts calls and blocks
// until released, so concurrent callers overlap.
func fakeAuditScans(t *testing.T) (calls *atomic.Int32, release func()) {
	t.Helper()
	invalidateAuditCache()
	gate := make(chan struct{})
	calls = &atomic.Int32{}
	prev := runAuditScan
	runAuditScan = func(_ *k8s.ResourceCache, _ []string, _ *audit.RunOptions) *bp.ScanResults {
		calls.Add(1)
		<-gate
		return &bp.ScanResults{}
	}
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() {
		release()
		runAuditScan = prev
		invalidateAuditCache()
	})
	return calls, release
}

func runConcurrently(t *testing.T, release func(), fns ...func()) {
	t.Helper()
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}
	// Let every caller reach the scan before it finishes.
	time.Sleep(50 * time.Millisecond)
	release()
	wg.Wait()
}

func TestAuditScanSharedByIdenticalConcurrentRequests(t *testing.T) {
	calls, release := fakeAuditScans(t)
	opts := &audit.RunOptions{Scope: &audit.ReadScope{Namespaces: []string{"a", "b"}}}
	scan := func() { getCachedResults(nil, []string{"b", "a"}, opts) }

	runConcurrently(t, release, scan, scan, scan)

	if got := calls.Load(); got != 1 {
		t.Fatalf("scans = %d, want 1 for identical concurrent requests", got)
	}
}

// Same namespaces but different Secret or cluster-resource access is a
// different authorization scope; those results must never be shared.
func TestAuditScanNotSharedAcrossDifferentAccess(t *testing.T) {
	calls, release := fakeAuditScans(t)
	namespaces := []string{"a"}
	base := &audit.RunOptions{Scope: &audit.ReadScope{Namespaces: namespaces}}
	withSecrets := &audit.RunOptions{Scope: &audit.ReadScope{Namespaces: namespaces, SecretNamespaces: []string{"a"}}}
	withNodes := &audit.RunOptions{Scope: &audit.ReadScope{Namespaces: namespaces, ClusterResources: map[string]bool{"nodes": true}}}

	runConcurrently(t, release,
		func() { getCachedResults(nil, namespaces, base) },
		func() { getCachedResults(nil, namespaces, withSecrets) },
		func() { getCachedResults(nil, namespaces, withNodes) },
	)

	if got := calls.Load(); got != 3 {
		t.Fatalf("scans = %d, want 3 for three different access scopes", got)
	}
}
