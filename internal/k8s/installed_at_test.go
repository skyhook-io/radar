package k8s

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestInstalledAtReadsOwnDeployment(t *testing.T) {
	created := time.Date(2026, 3, 14, 9, 26, 53, 0, time.UTC)
	kc := fake.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name: "radar", Namespace: "radar", CreationTimestamp: metav1.NewTime(created),
		}},
		// A same-named Deployment elsewhere must not be mistaken for ours.
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name: "radar", Namespace: "other", CreationTimestamp: metav1.NewTime(created.Add(72 * time.Hour)),
		}},
	)
	if got := installedAtFrom(context.Background(), kc, "radar", "radar"); got != created.Unix() {
		t.Fatalf("installedAt = %d, want %d", got, created.Unix())
	}
}

func TestInstalledAtBacksOffThenRetriesAfterTransientReadFailure(t *testing.T) {
	created := time.Date(2026, 3, 14, 9, 26, 53, 0, time.UTC)
	kc := fake.NewSimpleClientset(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "radar", Namespace: "radar", CreationTimestamp: metav1.NewTime(created),
	}})
	failed := false
	kc.PrependReactor("get", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		if !failed {
			failed = true
			return true, nil, errors.New("temporary apiserver failure")
		}
		return false, nil, nil
	})
	resetInstalledAtCache()
	t.Cleanup(resetInstalledAtCache)

	if got := installedAtCachedFrom(context.Background(), kc, "radar", "radar"); got != 0 {
		t.Fatalf("first installedAt = %d, want 0 after transient failure", got)
	}
	if got := installedAtCachedFrom(context.Background(), kc, "radar", "radar"); got != 0 {
		t.Fatalf("backed-off installedAt = %d, want 0", got)
	}
	installedAtMu.Lock()
	installedAtTried = time.Now().Add(-installedAtRetryAfter)
	installedAtMu.Unlock()
	if got := installedAtCachedFrom(context.Background(), kc, "radar", "radar"); got != created.Unix() {
		t.Fatalf("retried installedAt = %d, want %d", got, created.Unix())
	}
}

func TestInstalledAtCachedDoesNotInitiateRead(t *testing.T) {
	resetInstalledAtCache()
	t.Cleanup(resetInstalledAtCache)

	if got := InstalledAtCached(); got != 0 {
		t.Fatalf("InstalledAtCached = %d, want 0", got)
	}
	installedAtMu.Lock()
	tried := installedAtTried
	installedAtMu.Unlock()
	if !tried.IsZero() {
		t.Fatalf("InstalledAtCached recorded an attempted read at %v", tried)
	}

	installedAtMu.Lock()
	installedAtCached = 1700000000
	installedAtMu.Unlock()
	if got := InstalledAtCached(); got != 1700000000 {
		t.Fatalf("InstalledAtCached = %d, want 1700000000", got)
	}
}

func TestInstalledAtZeroWhenUnknown(t *testing.T) {
	// Every unknown must be 0 so callers never substitute a value that churns.
	kc := fake.NewSimpleClientset()
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		client     kubernetes.Interface
		ns, deploy string
	}{
		{"no downward-API namespace", kc, "", "radar"},
		{"no downward-API name", kc, "radar", ""},
		{"deployment absent", kc, "radar", "radar"},
		{"nil client", nil, "radar", "radar"},
		// A typed nil in an interface is non-nil and panics on first use without the guard.
		{"typed-nil client", (*kubernetes.Clientset)(nil), "radar", "radar"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := installedAtFrom(ctx, tc.client, tc.ns, tc.deploy); got != 0 {
				t.Fatalf("installedAt = %d, want 0", got)
			}
		})
	}
}

func resetInstalledAtCache() {
	installedAtMu.Lock()
	defer installedAtMu.Unlock()
	installedAtCached = 0
	installedAtTried = time.Time{}
	installedAtLoading = nil
}
