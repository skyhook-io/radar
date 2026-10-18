package k8score

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The reflector wraps a failed list this way before the watch error handler
// sees it.
func listErr(err error) error { return fmt.Errorf("failed to list *v1.ConfigMap: %w", err) }

func serverErr() error {
	return apierrors.NewInternalError(errors.New("conversion webhook for widgets.example.io failed"))
}

func TestListProgressEpisodes(t *testing.T) {
	const after = time.Minute
	t0 := time.Unix(1_000_000, 0)
	rv := "10"
	newP := func() *listProgress {
		return newListProgress("ConfigMap", func() string { return rv }, func() bool { return true }, nil)
	}
	failing := func(p *listProgress, now time.Time) bool {
		_, ok := p.failure(now, after, nil)
		return ok
	}

	t.Run("lists that keep failing make the store stale", func(t *testing.T) {
		rv = "10"
		p := newP()
		p.observe(listErr(serverErr()), t0)
		if failing(p, t0.Add(after-time.Second)) {
			t.Fatal("failing before the threshold")
		}
		p.observe(listErr(serverErr()), t0.Add(30*time.Second))
		f, ok := p.failure(t0.Add(after), after, nil)
		if !ok || !f.Since.Equal(t0) || f.Cause != ListFailureServerError || !f.Stale {
			t.Fatalf("failure = %+v, %v", f, ok)
		}
		// Silence after the failures is not progress: a reflector stuck on a
		// hung request reports nothing either.
		if !failing(p, t0.Add(time.Hour)) {
			t.Fatal("recovered by elapsed time alone")
		}
	})

	t.Run("an error after a successful list closes the episode", func(t *testing.T) {
		rv = "10"
		p := newP()
		p.observe(listErr(serverErr()), t0)
		p.observe(apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "", errors.New("watch denied")), t0.Add(time.Second))
		if failing(p, t0.Add(2*after)) {
			t.Fatal("a watch error after a successful list left the episode open")
		}
	})

	t.Run("resource version progress closes the episode", func(t *testing.T) {
		rv = "10"
		p := newP()
		p.observe(listErr(serverErr()), t0)
		rv = "11"
		if failing(p, t0.Add(2*after)) {
			t.Fatal("still failing after the store moved")
		}
		// A failure after progress starts a new episode.
		p.observe(listErr(serverErr()), t0.Add(3*after))
		if f, ok := p.failure(t0.Add(4*after), after, nil); !ok || !f.Since.Equal(t0.Add(3*after)) {
			t.Fatalf("new episode = %+v, %v", f, ok)
		}
	})

	t.Run("a passing probe vouches for a quiet kind until the reflector fails again", func(t *testing.T) {
		rv = "10"
		p := newP()
		p.probe = func(context.Context) error { return nil }
		p.observe(listErr(serverErr()), t0)
		if !p.startProbe(t0.Add(time.Second), time.Second, after) {
			t.Fatal("probe not started")
		}
		if p.startProbe(t0.Add(2*time.Second), time.Second, after) {
			t.Fatal("a second probe started while one runs")
		}
		p.probeDone(nil, t0.Add(time.Second))
		if failing(p, t0.Add(2*after)) {
			t.Fatal("failing after a passing probe")
		}
		// The probe and the reflector's own list disagree: keep the episode's
		// start, and stop trusting probes.
		p.observe(listErr(serverErr()), t0.Add(2*after))
		if f, ok := p.failure(t0.Add(2*after), after, nil); !ok || !f.Since.Equal(t0) {
			t.Fatalf("after the reflector failed again = %+v, %v", f, ok)
		}
		if p.startProbe(t0.Add(3*after), time.Second, after) {
			t.Fatal("probed again after the reflector contradicted a probe")
		}
		rv = "11"
		if failing(p, t0.Add(3*after)) {
			t.Fatal("reflector progress did not close the episode")
		}
	})

	t.Run("a failing probe keeps the episode open", func(t *testing.T) {
		rv = "10"
		p := newP()
		p.probe = func(context.Context) error { return nil }
		p.observe(listErr(serverErr()), t0)
		p.startProbe(t0.Add(time.Second), time.Second, after)
		p.probeDone(apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "", errors.New("denied")), t0.Add(time.Second))
		if f, ok := p.failure(t0.Add(after), after, nil); !ok || f.Cause != ListFailureForbidden {
			t.Fatalf("failure = %+v, %v", f, ok)
		}
	})

	t.Run("a failing heartbeat opens an episode only a passing probe closes", func(t *testing.T) {
		rv = "10"
		recovered := 0
		p := newP()
		p.probe = func(context.Context) error { return nil }
		p.heartbeat, p.onRecover = true, func() { recovered++ }
		if !p.startProbe(t0.Add(after), time.Second, after) {
			t.Fatal("no heartbeat on a quiet kind")
		}
		p.probeDone(serverErr(), t0.Add(after))
		// Bookmarks move the version while the watch drops events.
		rv = "11"
		f, ok := p.failure(t0.Add(2*after), after, nil)
		if !ok || f.Cause != ListFailureServerError || !f.Since.Equal(t0.Add(after)) {
			t.Fatalf("after a failed heartbeat = %+v, %v", f, ok)
		}
		if !p.startProbe(t0.Add(2*after), time.Second, after) {
			t.Fatal("no verification probe in a heartbeat episode")
		}
		p.probeDone(nil, t0.Add(2*after))
		if failing(p, t0.Add(3*after)) || recovered != 1 {
			t.Fatalf("after a passing probe: failing=%v, relists=%d", failing(p, t0.Add(3*after)), recovered)
		}
		if p.startProbe(t0.Add(2*after+time.Second), time.Second, after) {
			t.Fatal("heartbeat ran again before the threshold")
		}
	})

	t.Run("no heartbeat without the flag", func(t *testing.T) {
		p := newP()
		p.probe = func(context.Context) error { return nil }
		if p.startProbe(t0.Add(time.Hour), time.Second, after) {
			t.Fatal("a healthy kind without heartbeat was probed")
		}
	})

	t.Run("a store that never loaded fails with its cause", func(t *testing.T) {
		p := newListProgress("ConfigMap", func() string { return "" }, func() bool { return false }, nil)
		p.observe(listErr(apierrors.NewUnauthorized("token expired")), t0)
		f, ok := p.failure(t0.Add(after), after, nil)
		if !ok || f.Stale || f.Cause != ListFailureUnauthorized {
			t.Fatalf("failure = %+v, %v", f, ok)
		}
	})

	t.Run("an unreachable API server freezes stores that stopped moving", func(t *testing.T) {
		rv = "10"
		reach := &APIReachability{}
		p := newP()
		reach.since, reach.epoch = t0, 1
		// The first look in the episode records where the store stood, even
		// if its version moved just before the API server went away.
		if _, ok := p.failure(t0.Add(time.Second), after, reach); ok {
			t.Fatal("unreachable before the threshold")
		}
		f, ok := p.failure(t0.Add(after), after, reach)
		if !ok || f.Cause != ListFailureUnreachable || !f.Since.Equal(t0) {
			t.Fatalf("failure = %+v, %v", f, ok)
		}
		// A store whose watch still delivers is not frozen.
		rv = "12"
		if _, ok := p.failure(t0.Add(after+time.Second), after, reach); ok {
			t.Fatal("a store still updating read as frozen")
		}
		// Once it stops moving, it freezes from its last move.
		if _, ok := p.failure(t0.Add(2*after), after, reach); ok {
			t.Fatal("frozen before the threshold from its last move")
		}
		if f, ok := p.failure(t0.Add(2*after+time.Second), after, reach); !ok || !f.Since.Equal(t0.Add(after+time.Second)) {
			t.Fatalf("a store that stopped moving = %+v, %v", f, ok)
		}
		// A new episode takes a new snapshot.
		reach.since, reach.epoch = t0.Add(2*after), 2
		p.failure(t0.Add(2*after), after, reach)
		if _, ok := p.failure(t0.Add(3*after), after, reach); !ok {
			t.Fatal("a store frozen in a second episode read as updating")
		}
	})
}

func TestClassifyListFailure(t *testing.T) {
	gr := schema.GroupResource{Resource: "configmaps"}
	for _, tc := range []struct {
		err  error
		want ListFailureCause
	}{
		{apierrors.NewForbidden(gr, "", errors.New("no")), ListFailureForbidden},
		{apierrors.NewUnauthorized("no"), ListFailureUnauthorized},
		{serverErr(), ListFailureServerError},
		{apierrors.NewServiceUnavailable("down"), ListFailureServerError},
		{context.DeadlineExceeded, ListFailureTimeout},
		{apierrors.NewTimeoutError("slow", 1), ListFailureTimeout},
		{&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, ListFailureUnreachable},
		{&net.DNSError{Err: "no such host", Name: "api"}, ListFailureUnreachable},
		{errors.New("something else"), ListFailureOther},
	} {
		if got := classifyListFailure(listErr(tc.err)); got != tc.want {
			t.Errorf("%v: %s, want %s", tc.err, got, tc.want)
		}
	}
}

func TestAPIReachability(t *testing.T) {
	a := &APIReachability{}
	req, _ := http.NewRequest(http.MethodGet, "https://api/api/v1/pods", nil)
	a.ObserveRoundTrip(req, nil, errors.New("connection refused"))
	if _, ok := a.unreachableSince(time.Now(), 0); !ok {
		t.Fatal("a failed request did not open an episode")
	}
	a.ObserveRoundTrip(req, &http.Response{StatusCode: http.StatusInternalServerError}, nil)
	if _, ok := a.unreachableSince(time.Now(), 0); ok {
		t.Fatal("a response did not close the episode")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.ObserveRoundTrip(req.WithContext(ctx), nil, context.Canceled)
	if _, ok := a.unreachableSince(time.Now(), 0); ok {
		t.Fatal("a request its caller cancelled counted")
	}
}

// cmListWatch fakes a configmaps API whose lists can fail and whose watches
// the test can break, so the reflector relists.
type cmListWatch struct {
	client  *fake.Clientset
	listErr atomic.Pointer[error]
	// watchErr, when set, fails every watch start.
	watchErr atomic.Pointer[error]
	probes   atomic.Int32

	mu       sync.Mutex
	watchers []*watch.FakeWatcher
}

func newCMListWatch(objects ...runtime.Object) *cmListWatch {
	lw := &cmListWatch{client: fake.NewSimpleClientset(objects...)}
	lw.client.PrependReactor("list", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		if err := lw.listErr.Load(); err != nil {
			return true, nil, *err
		}
		return false, nil, nil
	})
	lw.client.PrependWatchReactor("configmaps", func(k8stesting.Action) (bool, watch.Interface, error) {
		if err := lw.watchErr.Load(); err != nil {
			return true, nil, *err
		}
		w := watch.NewFake()
		lw.mu.Lock()
		lw.watchers = append(lw.watchers, w)
		lw.mu.Unlock()
		return true, w, nil
	})
	return lw
}

func (lw *cmListWatch) failLists(err error) {
	if err == nil {
		lw.listErr.Store(nil)
		return
	}
	lw.listErr.Store(&err)
}

// breakWatches ends every open watch with a server error, which makes the
// reflector relist.
func (lw *cmListWatch) breakWatches() {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	for _, w := range lw.watchers {
		w.Error(&metav1.Status{Status: metav1.StatusFailure, Code: http.StatusInternalServerError, Message: "watch broke"})
	}
	lw.watchers = nil
}

func (lw *cmListWatch) probe(context.Context, string, string) error {
	lw.probes.Add(1)
	if err := lw.listErr.Load(); err != nil {
		return *err
	}
	return nil
}

func newFailureTrackingCache(t *testing.T, lw *cmListWatch, after time.Duration, reach *APIReachability) *ResourceCache {
	return newFailureTrackingCacheDeferred(t, lw, after, reach, map[string]bool{})
}

func newFailureTrackingCacheDeferred(t *testing.T, lw *cmListWatch, after time.Duration, reach *APIReachability, deferred map[string]bool) *ResourceCache {
	t.Helper()
	rc, err := NewResourceCache(CacheConfig{
		Client:           lw.client,
		ResourceTypes:    map[string]bool{ConfigMaps: true},
		DeferredTypes:    deferred,
		ListFailureAfter: after,
		ListProbe:        lw.probe,
		APIReachability:  reach,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rc.Stop)
	return rc
}

func configMap(ns, name string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

func TestAssessKindReadStaleStoreFailsAndRecovers(t *testing.T) {
	lw := newCMListWatch(configMap("default", "a"))
	rc := newFailureTrackingCache(t, lw, 300*time.Millisecond, nil)
	waitForCondition(t, "configmaps to load", func() bool {
		return rc.AssessKindRead(ConfigMaps, []string{"default"}).Complete
	})

	lw.failLists(serverErr())
	lw.breakWatches()
	waitForCondition(t, "the store to read stale", func() bool {
		a := rc.AssessKindRead(ConfigMaps, []string{"default"})
		f, ok := a.ListFailures["default"]
		return !a.Complete && slices.Equal(a.Failed, []string{"default"}) && ok && f.Stale && f.Cause == ListFailureServerError
	})
	whole := rc.AssessKindRead(ConfigMaps, nil)
	if whole.Readiness != KindFailed || whole.Complete || !whole.ListFailures[""].Stale {
		t.Fatalf("whole kind while stale = %+v", whole)
	}
	if rc.KindReadinessFor(ConfigMaps) != KindReady {
		t.Fatal("whole-kind readiness for the UI changed; it keeps serving last-known data")
	}

	lw.failLists(nil)
	waitForCondition(t, "the store to recover", func() bool {
		a := rc.AssessKindRead(ConfigMaps, []string{"default"})
		return a.Complete && a.ListFailures == nil
	})
}

func TestAssessKindReadQuietKindStaysRead(t *testing.T) {
	lw := newCMListWatch(configMap("default", "a"))
	rc := newFailureTrackingCache(t, lw, 100*time.Millisecond, nil)
	waitForCondition(t, "configmaps to load", func() bool {
		return rc.AssessKindRead(ConfigMaps, []string{"default"}).Complete
	})
	time.Sleep(time.Second)
	if a := rc.AssessKindRead(ConfigMaps, []string{"default"}); !a.Complete {
		t.Fatalf("a quiet healthy kind = %+v", a)
	}
	if n := lw.probes.Load(); n != 0 {
		t.Fatalf("a healthy kind was probed %d times", n)
	}
}

// One failed list, then a successful one with nothing new: the probe settles
// it before the threshold, so it never reads stale.
func TestAssessKindReadTransientFailureOnQuietKind(t *testing.T) {
	lw := newCMListWatch(configMap("default", "a"))
	rc := newFailureTrackingCache(t, lw, 400*time.Millisecond, nil)
	waitForCondition(t, "configmaps to load", func() bool {
		return rc.AssessKindRead(ConfigMaps, []string{"default"}).Complete
	})
	lw.failLists(serverErr())
	lw.breakWatches()
	waitForCondition(t, "the failed list", func() bool {
		st, _ := rc.storeFor(ConfigMaps, "")
		st.progress.mu.Lock()
		defer st.progress.mu.Unlock()
		return !st.progress.since.IsZero()
	})
	lw.failLists(nil)
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if a := rc.AssessKindRead(ConfigMaps, []string{"default"}); !a.Complete {
			t.Fatalf("a transient failure read as stale: %+v", a)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A kind Radar may list but not watch re-lists every cycle; its lists
// succeed, so it stays readable for listing.
func TestAssessKindReadListOnlyStaysRead(t *testing.T) {
	lw := newCMListWatch(configMap("default", "a"))
	denied := error(apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "", errors.New("watch denied")))
	lw.watchErr.Store(&denied)
	rc := newFailureTrackingCache(t, lw, 100*time.Millisecond, nil)
	waitForCondition(t, "configmaps to load", func() bool {
		return rc.AssessKindRead(ConfigMaps, []string{"default"}).Complete
	})
	time.Sleep(time.Second)
	if a := rc.AssessKindRead(ConfigMaps, []string{"default"}); !a.Complete {
		t.Fatalf("a list-only kind = %+v", a)
	}
}

// A kind refused on its very first load fails with the refusal, before any
// sync deadline.
func TestAssessKindReadRefusedFirstLoad(t *testing.T) {
	lw := newCMListWatch()
	lw.failLists(apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "", errors.New("denied")))
	// Deferred, so building the cache doesn't wait for a load that can't
	// finish; its sync deadline is far off.
	rc := newFailureTrackingCacheDeferred(t, lw, 200*time.Millisecond, nil, map[string]bool{ConfigMaps: true})
	waitForCondition(t, "the refused kind to fail", func() bool {
		a := rc.AssessKindRead(ConfigMaps, []string{"default"})
		f := a.ListFailures["default"]
		return slices.Equal(a.Failed, []string{"default"}) && !f.Stale && f.Cause == ListFailureForbidden
	})
}

func TestAssessKindReadUnreachableAPIServer(t *testing.T) {
	lw := newCMListWatch(configMap("default", "a"))
	reach := &APIReachability{}
	rc := newFailureTrackingCache(t, lw, 200*time.Millisecond, reach)
	waitForCondition(t, "configmaps to load", func() bool {
		return rc.AssessKindRead(ConfigMaps, []string{"default"}).Complete
	})
	time.Sleep(100 * time.Millisecond) // let the watchdog see the loaded store
	req, _ := http.NewRequest(http.MethodGet, "https://api/api/v1/configmaps", nil)
	reach.ObserveRoundTrip(req, nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED})
	waitForCondition(t, "every store to read frozen", func() bool {
		a := rc.AssessKindRead(ConfigMaps, nil)
		return a.Readiness == KindFailed && a.ListFailures[""].Cause == ListFailureUnreachable
	})
	reach.ObserveRoundTrip(req, &http.Response{StatusCode: http.StatusOK}, nil)
	if a := rc.AssessKindRead(ConfigMaps, nil); !a.Complete {
		t.Fatalf("after the API server answered = %+v", a)
	}
}

// In a kind watched namespace by namespace, only the failing namespace's
// store fails, and the whole kind is no longer Ready.
func TestAssessKindReadStaleNamespaceOfPartitionedKind(t *testing.T) {
	synced := true
	stale := newListProgress("ConfigMap in namespace gamma", func() string { return "5" }, func() bool { return true }, nil)
	stale.observe(listErr(serverErr()), time.Now().Add(-time.Hour))
	rc := &ResourceCache{
		enabledResources: map[string]bool{ConfigMaps: true},
		config: CacheConfig{
			ResourceScopes:          map[string]ResourceScope{ConfigMaps: {Enabled: true, Namespace: "alpha"}},
			ResourceScopeNamespaces: map[string][]string{ConfigMaps: {"alpha", "gamma"}},
			ListFailureAfter:        time.Minute,
		},
		informerStatuses:  []InformerSyncStatus{{Key: ConfigMaps}},
		informerHasSynced: []func() bool{func() bool { return true }},
		kindStores: map[string]map[string]kindStore{ConfigMaps: {
			"alpha": podStore(t, &synced, pod("alpha", "a1")),
			"gamma": func() kindStore {
				st := podStore(t, &synced, pod("gamma", "g1"))
				st.progress = stale
				return st
			}(),
		}},
	}
	a := rc.AssessKindRead(ConfigMaps, nil)
	if a.Readiness != KindFailed || !slices.Equal(a.Read, []string{"alpha"}) || !slices.Equal(a.Failed, []string{"gamma"}) ||
		!a.ListFailures["gamma"].Stale || len(a.ListFailures) != 1 {
		t.Fatalf("partitioned kind = %+v", a)
	}
	if alpha := rc.AssessKindRead(ConfigMaps, []string{"alpha"}); !alpha.Complete {
		t.Fatalf("alpha alone = %+v", alpha)
	}
}

func TestDynamicNamespaceListFailure(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v2", Resource: "widgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	var failing atomic.Bool
	var watchers []*watch.FakeWatcher
	var mu sync.Mutex
	dyn.PrependReactor("list", "widgets", func(k8stesting.Action) (bool, runtime.Object, error) {
		if failing.Load() {
			return true, nil, serverErr()
		}
		return false, nil, nil
	})
	dyn.PrependWatchReactor("widgets", func(k8stesting.Action) (bool, watch.Interface, error) {
		w := watch.NewFake()
		mu.Lock()
		watchers = append(watchers, w)
		mu.Unlock()
		return true, w, nil
	})
	breakWatches := func() {
		mu.Lock()
		defer mu.Unlock()
		for _, w := range watchers {
			w.Error(&metav1.Status{Status: metav1.StatusFailure, Code: http.StatusInternalServerError, Message: "conversion failed"})
		}
		watchers = nil
	}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, ListFailureAfter: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.startWatching(gvr, "alpha"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, "widgets to load", func() bool { return d.IsNamespaceSynced(gvr, "alpha") })
	if _, ok := d.NamespaceListFailure(gvr, "alpha"); ok {
		t.Fatal("a healthy kind reported a list failure")
	}

	failing.Store(true)
	breakWatches()
	waitForCondition(t, "widgets to read stale", func() bool {
		f, ok := d.NamespaceListFailure(gvr, "alpha")
		return ok && f.Stale && f.Cause == ListFailureServerError
	})
	if _, ok := d.NamespaceListFailure(gvr, "beta"); ok {
		t.Fatal("an unwatched namespace reported a failure")
	}

	failing.Store(false)
	waitForCondition(t, "widgets to recover", func() bool {
		_, ok := d.NamespaceListFailure(gvr, "alpha")
		return !ok
	})

	failing.Store(true)
	breakWatches()
	waitForCondition(t, "widgets to read stale again", func() bool {
		_, ok := d.NamespaceListFailure(gvr, "alpha")
		return ok
	})
	d.Stop()
	if _, ok := d.NamespaceListFailure(gvr, "alpha"); ok {
		t.Fatal("a stopped cache vouched for a list failure")
	}
}

// An API server drops watch events it can't convert when a custom resource's
// conversion webhook fails, without ending the watch. A multi-version custom
// resource is therefore probed on a heartbeat: the failed probe makes it stale,
// and once lists work again the informer relists, so the events it missed
// reach the store.
func TestDynamicSilentlyDroppedEventsNeedHeartbeat(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v2", Resource: "widgets"}
	w1 := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.io/v2", "kind": "Widget", "metadata": map[string]any{"name": "w1", "namespace": "alpha"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, w1)
	var converting atomic.Bool
	converting.Store(true)
	dyn.PrependReactor("list", "widgets", func(k8stesting.Action) (bool, runtime.Object, error) {
		if !converting.Load() {
			return true, nil, serverErr()
		}
		return false, nil, nil
	})
	// The watch stays open and delivers nothing, as a real one does while
	// conversion fails.
	dyn.PrependWatchReactor("widgets", func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, watch.NewFake(), nil
	})
	disc := &ResourceDiscovery{resources: []APIResource{
		{Group: "example.io", Version: "v1", Kind: "Widget", Name: "widgets", Namespaced: true, IsCRD: true},
		{Group: "example.io", Version: "v2", Kind: "Widget", Name: "widgets", Namespaced: true, IsCRD: true},
	}}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, Discovery: disc, ListFailureAfter: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.startWatching(gvr, ""); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, "widgets to load", func() bool { return d.IsNamespaceSynced(gvr, "alpha") })

	converting.Store(false)
	// An object the open watch never delivers.
	w2 := w1.DeepCopy()
	w2.SetName("w2")
	if _, err := dyn.Resource(gvr).Namespace("alpha").Create(context.Background(), w2, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, "widgets to read stale", func() bool {
		f, ok := d.NamespaceListFailure(gvr, "alpha")
		return ok && f.Stale && f.Cause == ListFailureServerError
	})

	converting.Store(true)
	waitForCondition(t, "widgets to relist with w2", func() bool {
		if _, failing := d.NamespaceListFailure(gvr, "alpha"); failing || !d.IsNamespaceSynced(gvr, "alpha") {
			return false
		}
		_, err := d.GetWatched(gvr, "alpha", "w2")
		return err == nil
	})
}

// Only a custom resource served in several versions can go through a
// conversion webhook, so a single-version one is never probed while healthy.
func TestDynamicSingleVersionCRDIsNeverProbed(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "gadgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "GadgetList"})
	var probes atomic.Int32
	dyn.PrependReactor("list", "gadgets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.ListActionImpl).GetListOptions().Limit == 1 {
			probes.Add(1)
		}
		return false, nil, nil
	})
	disc := &ResourceDiscovery{resources: []APIResource{
		{Group: "example.io", Version: "v1", Kind: "Gadget", Name: "gadgets", Namespaced: true, IsCRD: true},
	}}
	if disc.CRDServedInOtherVersions(gvr) {
		t.Fatal("a single-version custom resource reads as multi-version")
	}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, Discovery: disc, ListFailureAfter: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.startWatching(gvr, ""); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, "gadgets to load", func() bool { return d.IsNamespaceSynced(gvr, "alpha") })
	time.Sleep(time.Second) // ten heartbeat periods
	if n := probes.Load(); n != 0 {
		t.Fatalf("a single-version custom resource was probed %d times", n)
	}
	// The same kind served in a second version is probed.
	disc.resources = append(disc.resources, APIResource{Group: "example.io", Version: "v2", Kind: "Gadget", Name: "gadgets", Namespaced: true, IsCRD: true})
	if !disc.CRDServedInOtherVersions(gvr) {
		t.Fatal("a multi-version custom resource reads as single-version")
	}
}
