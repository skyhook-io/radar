package cachesource

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

func BenchmarkDependentCandidatesNamespaceScoped(b *testing.B) {
	f := newFixture(b, k8score.CacheConfig{}, func(f *fixture) {
		f.dynamic.PrependReactor("list", "widgets", func(a k8stesting.Action) (bool, runtime.Object, error) {
			if a.GetNamespace() == "" {
				return true, nil, apierrors.NewForbidden(widgets.GroupResource(), "", errors.New("namespace scoped"))
			}
			return false, nil, nil
		})
	})
	if _, err := f.caches.Dynamic.List(widgets, "alpha"); err != nil {
		b.Fatal(err)
	}
	if !f.caches.Dynamic.WaitForSync(widgets, 5*time.Second) {
		b.Fatal("namespace watch did not sync")
	}
	s := begin(b, f.source())
	scope := ownership.InNamespace("alpha")
	s.DependentCandidates("uid-none", scope)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s.DependentCandidates("uid-none", scope)
	}
}

func TestDependentCandidatesRefreshSharedScopeObservations(t *testing.T) {
	var hold atomic.Bool
	var betaLists atomic.Int32
	hold.Store(true)
	f := newFixture(t, k8score.CacheConfig{}, func(f *fixture) {
		f.dynamic.PrependReactor("list", "widgets", func(a k8stesting.Action) (bool, runtime.Object, error) {
			if a.GetNamespace() == "" {
				return true, nil, apierrors.NewForbidden(widgets.GroupResource(), "", errors.New("namespace scoped"))
			}
			if a.GetNamespace() == "beta" && betaLists.Add(1) > 1 && hold.Load() {
				return true, nil, errors.New("initial list held")
			}
			return false, nil, nil
		})
	})
	if _, err := f.caches.Dynamic.List(widgets, "alpha"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "alpha ready", func() bool { return f.caches.Dynamic.IsNamespaceSynced(widgets, "alpha") })
	s := begin(t, f.source())
	scope := ownership.InNamespace("beta")
	before := s.DependentCandidates("uid-none", scope)
	if !hasGap(before, ownership.Gap{Kind: widgetKind, Namespace: "beta", Reason: readreason.RadarKindNotCollected}) {
		t.Fatalf("before watch: %v", before.Gaps)
	}
	if err := f.caches.Dynamic.EnsureWatchingContext(context.Background(), widgets, "beta"); err != nil {
		t.Fatal(err)
	}
	loading := s.DependentCandidates("uid-none", scope)
	if !hasGap(loading, ownership.Gap{Kind: widgetKind, Namespace: "beta", Reason: readreason.KindSyncPending}) {
		t.Fatalf("new watch: %v", loading.Gaps)
	}
	hold.Store(false)
	waitUntil(t, "beta ready", func() bool { return f.caches.Dynamic.IsNamespaceSynced(widgets, "beta") })
	if ready := s.DependentCandidates("uid-none", scope); slices.ContainsFunc(ready.Gaps, func(g ownership.Gap) bool { return g.Kind == widgetKind }) {
		t.Fatalf("loaded watch: %v", ready.Gaps)
	}
}
