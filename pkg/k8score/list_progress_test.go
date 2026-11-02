package k8score

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"slices"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

// The reflector wraps a failed list this way before the watch error handler
// sees it.
func failedList() error {
	return fmt.Errorf("failed to list *v1.ConfigMap: %w", apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "", errors.New("denied")))
}

func quietProgress(rv *string) *listProgress {
	return newListProgress("configmaps", func() string { return *rv }, log.New(io.Discard, "", 0))
}

func TestListProgressEpisodes(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)

	t.Run("lists that keep failing make the store stale", func(t *testing.T) {
		rv := "10"
		p := quietProgress(&rv)
		p.observe(failedList(), t0)
		if p.stale(t0.Add(ListStaleAfter - time.Second)) {
			t.Fatal("stale before the threshold")
		}
		p.observe(failedList(), t0.Add(time.Minute))
		if !p.stale(t0.Add(ListStaleAfter)) {
			t.Fatal("not stale at the threshold, counted from the first failure")
		}
		// Silence after the failures is not progress: a reflector stuck on a
		// hung request reports nothing either.
		if !p.stale(t0.Add(time.Hour)) {
			t.Fatal("recovered by elapsed time alone")
		}
	})

	t.Run("an error after a successful list closes the episode", func(t *testing.T) {
		rv := "10"
		p := quietProgress(&rv)
		p.observe(failedList(), t0)
		p.observe(errors.New("very short watch: Unexpected watch close"), t0.Add(time.Second))
		if p.stale(t0.Add(2 * ListStaleAfter)) {
			t.Fatal("a watch error after a successful list left the episode open")
		}
	})

	t.Run("resource version progress closes the episode", func(t *testing.T) {
		rv := "10"
		p := quietProgress(&rv)
		p.observe(failedList(), t0)
		if !p.stale(t0.Add(ListStaleAfter)) {
			t.Fatal("not stale")
		}
		rv = "11"
		if p.stale(t0.Add(ListStaleAfter)) {
			t.Fatal("still stale after the store moved")
		}
		// A failure after progress starts a new episode.
		p.observe(failedList(), t0.Add(2*ListStaleAfter))
		if p.stale(t0.Add(3*ListStaleAfter - time.Second)) {
			t.Fatal("the new episode kept the old one's start")
		}
		if !p.stale(t0.Add(3 * ListStaleAfter)) {
			t.Fatal("the new episode never went stale")
		}
	})

	t.Run("a failure after unobserved progress restarts the clock", func(t *testing.T) {
		rv := "10"
		p := quietProgress(&rv)
		p.observe(failedList(), t0)
		rv = "11"
		p.observe(failedList(), t0.Add(ListStaleAfter))
		if p.stale(t0.Add(ListStaleAfter + time.Second)) {
			t.Fatal("progress between two failures did not count")
		}
	})

	t.Run("a nil tracker is never stale", func(t *testing.T) {
		var p *listProgress
		p.observe(failedList(), t0)
		if p.stale(t0.Add(time.Hour)) {
			t.Fatal("nil tracker stale")
		}
	})
}

// client-go doesn't type the errors its reflector hands the watch error
// handler; the list phase is recognized by its wording, which this pins.
func TestReflectorListErrorsAreListPhase(t *testing.T) {
	var mu sync.Mutex
	var seen []error
	lw := &cache.ListWatch{
		ListWithContextFunc: func(context.Context, metav1.ListOptions) (runtime.Object, error) {
			return nil, apierrors.NewInternalError(errors.New("conversion webhook for widgets.example.io failed"))
		},
		WatchFuncWithContext: func(context.Context, metav1.ListOptions) (watch.Interface, error) {
			return nil, errors.New("watch must not start before a list succeeds")
		},
	}
	inf := cache.NewSharedIndexInformer(lw, &corev1.ConfigMap{}, 0, cache.Indexers{})
	if err := inf.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		mu.Lock()
		seen = append(seen, err)
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go inf.Run(stop)
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		got := slices.Clone(seen)
		mu.Unlock()
		if len(got) > 0 {
			if !isListPhaseError(got[0]) {
				t.Fatalf("a failed list reached the handler as %q, which isListPhaseError doesn't recognize", got[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the handler never saw the failed list")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A loaded store whose lists keep failing stops counting as read: its
// namespace becomes a Failed, Stale part of the assessment, a miss there
// proves nothing, and the first sign of progress restores it.
func TestAssessKindReadStaleStore(t *testing.T) {
	synced := true
	rv := "10"
	gamma := podStore(t, &synced, pod("gamma", "g1"))
	gamma.progress = quietProgress(&rv)
	rc := &ResourceCache{
		enabledResources: map[string]bool{ConfigMaps: true},
		config: CacheConfig{
			ResourceScopes:          map[string]ResourceScope{ConfigMaps: {Enabled: true, Namespace: "alpha"}},
			ResourceScopeNamespaces: map[string][]string{ConfigMaps: {"alpha", "gamma"}},
		},
		informerStatuses:  []InformerSyncStatus{{Key: ConfigMaps}},
		informerHasSynced: []func() bool{func() bool { return true }},
		kindStores: map[string]map[string]kindStore{ConfigMaps: {
			"alpha": podStore(t, &synced, pod("alpha", "a1")),
			"gamma": gamma,
		}},
	}

	if got := rc.AssessKindRead(ConfigMaps, nil); got.Readiness != KindReady || len(got.Stale) != 0 || !slices.Equal(got.Read, []string{"alpha", "gamma"}) {
		t.Fatalf("before any failure = %+v", got)
	}

	gamma.progress.observe(failedList(), time.Now().Add(-ListStaleAfter-time.Minute))
	got := rc.AssessKindRead(ConfigMaps, nil)
	if got.Readiness != KindFailed || !got.KindStale || !slices.Equal(got.Read, []string{"alpha"}) ||
		!slices.Equal(got.Failed, []string{"gamma"}) || !slices.Equal(got.Stale, []string{"gamma"}) {
		t.Fatalf("stale gamma = %+v", got)
	}
	if named := rc.AssessKindRead(ConfigMaps, []string{"gamma"}); named.Complete || named.TargetReadiness() != KindFailed {
		t.Fatalf("named stale target = %+v", named)
	}
	if alpha := rc.AssessKindRead(ConfigMaps, []string{"alpha"}); !alpha.Complete {
		t.Fatalf("alpha is current: %+v", alpha)
	}
	if _, _, ok := rc.ReadableObject(ConfigMaps, "gamma", "missing"); ok {
		t.Fatal("a miss in a stale store was authoritative")
	}
	if _, ok := rc.ReadableObjects(ConfigMaps, []string{"alpha", "gamma"}); ok {
		t.Fatal("a stale store was read as complete")
	}
	if !rc.StoreStale(ConfigMaps, "gamma") || rc.StoreStale(ConfigMaps, "alpha") {
		t.Fatal("StoreStale disagrees with the assessment")
	}
	// A cache hit still proves the object existed.
	if _, found := rc.CachedObject(ConfigMaps, "gamma", "g1"); !found {
		t.Fatal("cached object hidden")
	}

	rv = "11"
	if got := rc.AssessKindRead(ConfigMaps, []string{"gamma"}); !got.Complete || got.Readiness != KindReady || len(got.Stale) != 0 {
		t.Fatalf("after progress = %+v", got)
	}
	if _, found, ok := rc.ReadableObject(ConfigMaps, "gamma", "missing"); !ok || found {
		t.Fatal("a miss after progress must establish absence again")
	}
}

// One informer for every namespace has no partition: the kind itself is
// stale.
func TestAssessKindReadStaleClusterWideStore(t *testing.T) {
	synced := true
	rv := "10"
	st := podStore(t, &synced, pod("alpha", "a1"))
	st.progress = quietProgress(&rv)
	rc := &ResourceCache{
		enabledResources:  map[string]bool{Pods: true},
		config:            CacheConfig{ResourceScopes: map[string]ResourceScope{Pods: {Enabled: true}}},
		informerStatuses:  []InformerSyncStatus{{Key: Pods}},
		informerHasSynced: []func() bool{func() bool { return true }},
		kindStores:        map[string]map[string]kindStore{Pods: {"": st}},
	}
	st.progress.observe(failedList(), time.Now().Add(-ListStaleAfter-time.Minute))
	if all := rc.AssessKindRead(Pods, nil); all.Complete || all.Readiness != KindFailed || !all.KindStale || all.Read != nil {
		t.Fatalf("unscoped = %+v", all)
	}
	if named := rc.AssessKindRead(Pods, []string{"alpha"}); named.Complete || !slices.Equal(named.Stale, []string{"alpha"}) || !slices.Equal(named.Failed, []string{"alpha"}) {
		t.Fatalf("named = %+v", named)
	}
	synced = false
	// A store that never loaded is loading or overdue, not stale.
	if pending := rc.AssessKindRead(Pods, []string{"alpha"}); pending.KindStale || len(pending.Stale) != 0 {
		t.Fatalf("unsynced store reported stale: %+v", pending)
	}
}
