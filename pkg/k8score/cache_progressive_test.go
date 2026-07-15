package k8score

import (
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// waitForCondition polls until fn() is true or the deadline passes.
func waitForCondition(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// While one kind's initial LIST is blocked, the mid-sync handle must report
// the others Ready and the blocked kind Pending — never serve it as empty.
func TestNewResourceCache_ProgressiveReadinessDuringSync(t *testing.T) {
	client := fake.NewSimpleClientset()
	// The fake clientset serializes the whole reaction chain under one lock,
	// so a reactor must never block — fail the LIST until released instead
	// (the reflector retries with backoff, keeping the kind unsynced).
	release := make(chan struct{})
	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		select {
		case <-release:
			return false, nil, nil // fall through to the default empty list
		default:
			return true, nil, errors.New("simulated slow LIST")
		}
	})

	started := make(chan *ResourceCache, 1)
	done := make(chan struct{})
	var rc *ResourceCache
	var buildErr error
	go func() {
		rc, buildErr = NewResourceCache(CacheConfig{
			Client: client,
			ResourceTypes: map[string]bool{
				Pods:     true,
				Services: true,
			},
			OnInformersStarted: func(h *ResourceCache) { started <- h },
		})
		close(done)
	}()

	var handle *ResourceCache
	select {
	case handle = <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("OnInformersStarted was not invoked before Phase-1 wait")
	}

	waitForCondition(t, "services to sync while pods LIST is blocked", func() bool {
		return handle.KindReadinessFor(Services) == KindReady
	})
	if got := handle.KindReadinessFor(Pods); got != KindPending {
		t.Errorf("pods readiness during blocked LIST = %v, want KindPending", got)
	}
	if got := handle.KindReadinessFor(Secrets); got != KindUnavailable {
		t.Errorf("disabled kind readiness = %v, want KindUnavailable", got)
	}
	if got := handle.KindReadinessFor("no-such-kind"); got != KindUnavailable {
		t.Errorf("unknown kind readiness = %v, want KindUnavailable", got)
	}

	snap := handle.GetSyncSnapshot()
	if snap.Phase != SyncPhaseCritical {
		t.Errorf("snapshot phase mid-sync = %q, want %q", snap.Phase, SyncPhaseCritical)
	}
	var sawPodsPending, sawServicesSynced bool
	for _, k := range snap.Kinds {
		if k.Key == Pods && !k.Synced {
			sawPodsPending = true
		}
		if k.Key == Services && k.Synced {
			sawServicesSynced = true
		}
	}
	if !sawPodsPending || !sawServicesSynced {
		t.Errorf("snapshot kinds = %+v, want pods unsynced + services synced", snap.Kinds)
	}

	close(release)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("NewResourceCache did not return after LIST unblocked")
	}
	if buildErr != nil {
		t.Fatalf("NewResourceCache: %v", buildErr)
	}
	defer rc.Stop()

	if got := rc.KindReadinessFor(Pods); got != KindReady {
		t.Errorf("pods readiness after sync = %v, want KindReady", got)
	}
	if snap := rc.GetSyncSnapshot(); snap.CriticalSynced != snap.CriticalTotal {
		t.Errorf("post-sync snapshot critical %d/%d, want all synced", snap.CriticalSynced, snap.CriticalTotal)
	}
}

// A deferred kind that never syncs before the deferred deadline must go
// terminal (KindFailed), not report Pending forever.
func TestKindReadinessFor_FailedAfterDeferredTimeout(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("simulated permanently failing LIST")
	})

	rc, err := NewResourceCache(CacheConfig{
		Client: client,
		ResourceTypes: map[string]bool{
			Services:   true,
			ConfigMaps: true,
		},
		DeferredTypes:       map[string]bool{ConfigMaps: true},
		DeferredSyncTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	defer rc.Stop()

	waitForCondition(t, "deferred deadline to mark configmaps failed", func() bool {
		return rc.KindReadinessFor(ConfigMaps) == KindFailed
	})
	if got := rc.KindReadinessFor(Services); got != KindReady {
		t.Errorf("services readiness = %v, want KindReady (unaffected by sibling failure)", got)
	}
}
