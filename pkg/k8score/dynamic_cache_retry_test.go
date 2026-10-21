package k8score

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

func TestProbeRetryNamespaceOutcomes(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, tc := range []struct {
		name       string
		allowed    bool
		refused    int
		unresolved bool
	}{
		{"OK", true, 2, false}, {"Forbidden", false, 2, false}, {"exhausted", true, 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed := []string{"a"}
			if tc.allowed {
				allowed = append(allowed, "b")
			}
			d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, allowed, map[string]int{"b": tc.refused}), NamespaceFallbacks: []string{"a", "b"}, ProbeRetryBackoff: []time.Duration{100 * time.Millisecond, time.Millisecond}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			view := d.NewObservationView([]schema.GroupVersionResource{gvr})
			// Exercise startup's owner, rather than relying on a later read.
			d.WarmupParallel([]schema.GroupVersionResource{gvr}, time.Millisecond)
			initial := d.Observation(gvr)
			if got := view.NamespaceObservation(gvr, ""); got.ProbeRetrying != initial.ProbeRetrying || !slices.Equal(got.UnansweredNamespaces, initial.UnansweredNamespaces) {
				t.Fatalf("initial view: %+v; cache: %+v", got, initial)
			}
			if !initial.ProbeRetrying || !slices.Equal(initial.UnansweredNamespaces, []string{"b"}) {
				t.Fatalf("retry not published: %+v", initial)
			}
			if specific := d.NamespaceObservation(gvr, "a"); specific.ProbeRetrying || len(specific.UnansweredNamespaces) > 0 {
				t.Fatalf("a inherited b's gap: %+v", specific)
			}
			d.mu.RLock()
			r := d.probeRetries[gvr]
			d.mu.RUnlock()
			select {
			case <-r.done:
			case <-time.After(3 * time.Second):
				t.Fatal("retry did not finish")
			}
			got := d.Observation(gvr)
			if got.ProbeRetrying || (len(got.UnansweredNamespaces) > 0) != tc.unresolved {
				t.Fatalf("terminal observation: %+v", got)
			}
			for _, ns := range []string{"a", "b", "outside"} {
				want := d.NamespaceObservation(gvr, ns)
				got := view.NamespaceObservation(gvr, ns)
				if got.Scope != want.Scope || !slices.Equal(got.Namespaces, want.Namespaces) || got.ProbeRetrying != want.ProbeRetrying || !slices.Equal(got.UnansweredNamespaces, want.UnansweredNamespaces) {
					t.Fatalf("terminal %s view: %+v; cache: %+v", ns, got, want)
				}
			}
			if d.HasCoveringInformer(gvr, "b") != (tc.allowed && !tc.unresolved) {
				t.Fatalf("wrong namespace watch: %+v", got)
			}
			if !tc.unresolved && !d.WaitForSync(gvr, time.Second) {
				t.Fatal("watch did not sync")
			}
			if tc.name == "Forbidden" && (got.State == DynamicObservationDenied || got.ReasonCode == "scope_probe_incomplete" || got.Truncated) {
				t.Fatalf("denial did not become scope: %+v", got)
			}
			if tc.unresolved {
				// Repeated reads must not renew the retry budget.
				for range 10 {
					if err := d.EnsureWatching(gvr); err != nil {
						t.Fatal(err)
					}
				}
				d.mu.RLock()
				same := d.probeRetries[gvr] == r && !r.running
				d.mu.RUnlock()
				if !same {
					t.Fatal("exhausted retry restarted")
				}
			}
		})
	}
}

func TestProbeRetryWholeKindOutcomes(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, tc := range []struct {
		name      string
		allowed   []string
		refused   int
		fallbacks []string
		want      DynamicObservationState
		whole     bool
	}{
		{"OK", []string{""}, 1, nil, DynamicObservationSynced, false},
		{"Forbidden", nil, 1, nil, DynamicObservationDenied, false},
		// When the budget runs out the kind is reported unanswered; the slow
		// recovery re-checks it later (TestProbeRecoveryAfterTheBudget).
		{"exhausted", nil, 100, nil, DynamicObservationDeferred, true},
		{"exhausted, with fallbacks", nil, 100, []string{"a"}, DynamicObservationDeferred, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, tc.allowed, map[string]int{"": tc.refused}), NamespaceFallbacks: tc.fallbacks, ProbeRetryBackoff: []time.Duration{50 * time.Millisecond}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			if err := d.EnsureWatching(gvr); !IsClientRateLimited(err) {
				t.Fatalf("initial walk: %v", err)
			}
			if !d.Observation(gvr).ProbeRetrying {
				t.Fatal("whole-kind retry not published")
			}
			d.mu.RLock()
			r := d.probeRetries[gvr]
			d.mu.RUnlock()
			select {
			case <-r.done:
			case <-time.After(time.Second):
				t.Fatal("retry did not finish")
			}
			if tc.want == DynamicObservationSynced && !d.WaitForSync(gvr, time.Second) {
				t.Fatal("watch did not sync")
			}
			got := d.Observation(gvr)
			if got.ProbeRetrying || got.State != tc.want || (got.ReasonCode == "scope_probe_incomplete") != tc.whole {
				t.Fatalf("terminal observation: %+v", got)
			}
		})
	}
}

// A 401 rejects Radar's credentials, not one scope. A retry that meets one
// records it as such, stops, and settling the walk doesn't overwrite it.
func TestProbeRetryKeepsRejectedCredentials(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, tc := range []struct {
		name      string
		fallbacks []string
		// throttled is how often each namespace's probe is refused by the
		// client-side rate limiter before it gets the 401: enough to outlast
		// the first walk, which retries throttled candidates once itself.
		throttled map[string]int
		// forbiddenClusterWide answers the cluster-wide check with a 403, so
		// the walk falls back to namespaces.
		forbiddenClusterWide bool
	}{
		{"whole kind", nil, map[string]int{"": 1}, false},
		{"namespace", []string{"a", "b"}, map[string]int{"a": 2, "b": 2}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			probed := map[string]int{}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
			dyn.PrependReactor("list", "widgets", func(action ktesting.Action) (bool, runtime.Object, error) {
				ns := action.GetNamespace()
				mu.Lock()
				defer mu.Unlock()
				probed[ns]++
				if ns == "" && tc.forbiddenClusterWide {
					return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("collector cannot list"))
				}
				if tc.throttled[ns] > 0 {
					tc.throttled[ns]--
					return true, nil, errClientRateLimited
				}
				return true, nil, apierrors.NewUnauthorized("token rejected")
			})
			d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, NamespaceFallbacks: tc.fallbacks, ProbeRetryBackoff: []time.Duration{20 * time.Millisecond, 20 * time.Millisecond}, ProbeRecoveryIntervals: []time.Duration{10 * time.Millisecond}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			_ = d.EnsureWatching(gvr)
			mu.Lock()
			bBefore := probed["b"]
			mu.Unlock()
			d.mu.RLock()
			r := d.probeRetries[gvr]
			d.mu.RUnlock()
			if r == nil {
				t.Fatal("no retry started for the unanswered check")
			}
			select {
			case <-r.done:
			case <-time.After(3 * time.Second):
				t.Fatal("retry did not finish")
			}
			if got := d.Observation(gvr); got.State != DynamicObservationDenied || got.ReasonCode != "credentials_rejected" {
				t.Fatalf("observation = %+v, want denied/credentials_rejected", got)
			}
			// Nor does the slow recovery after the budget probe again.
			deadline := time.Now().Add(time.Second)
			for ownerLive(d, r) && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			mu.Lock()
			total := 0
			for _, n := range probed {
				total += n
			}
			mu.Unlock()
			time.Sleep(100 * time.Millisecond)
			mu.Lock()
			defer mu.Unlock()
			after := 0
			for _, n := range probed {
				after += n
			}
			if ownerLive(d, r) || after != total {
				t.Fatalf("re-checks went on after a 401: owner live %v, %d probes then %d", ownerLive(d, r), total, after)
			}
			if len(tc.fallbacks) > 1 && probed["b"] != bBefore {
				t.Fatalf("the retry went on probing after a 401: b probed %d times, %d before the retry", probed["b"], bBefore)
			}
		})
	}
}

func TestProbeRetryCandidateCapIsOnlySetup(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, []string{"a"}, nil), NamespaceFallbacks: []string{"a"}, NamespaceFallbacksTruncated: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !d.WaitForSync(gvr, time.Second) {
		t.Fatal("watch did not sync")
	}
	got := d.Observation(gvr)
	if !got.Truncated || got.ProbeRetrying || len(got.UnansweredNamespaces) > 0 {
		t.Fatalf("cap became unanswered: %+v", got)
	}
	d.mu.RLock()
	r := d.probeRetries[gvr]
	d.mu.RUnlock()
	if r != nil {
		t.Fatal("cap started a retry")
	}
}

func TestProbeRetryOneLoopPerGVR(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, nil, map[string]int{"b": 100}), ProbeRetryBackoff: []time.Duration{time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	o := probeOutcome{startedAt: time.Now(), unanswered: []string{"b"}}
	d.noteWalk(gvr, o)
	d.mu.RLock()
	r := d.probeRetries[gvr]
	d.mu.RUnlock()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { d.noteWalk(gvr, o) })
	}
	wg.Wait()
	d.mu.RLock()
	same := d.probeRetries[gvr] == r && r.running
	d.mu.RUnlock()
	if !same {
		t.Fatal("multiple retry owners")
	}
	d.Stop()
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("Stop leaked scheduled retry")
	}
	d.mu.RLock()
	running := r.running
	d.mu.RUnlock()
	if running {
		t.Fatal("Stop left retry running")
	}
}

func TestProbeRetryDeadlineIncludesFirstWalk(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	var probes atomic.Int32
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	dyn.PrependReactor("list", "widgets", func(ktesting.Action) (bool, runtime.Object, error) {
		probes.Add(1)
		return true, nil, context.DeadlineExceeded
	})
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, ProbeRetryBackoff: []time.Duration{time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	d.noteWalk(gvr, probeOutcome{startedAt: time.Now().Add(-probeRetryBudget + 50*time.Millisecond), unanswered: []string{"b"}})
	d.mu.RLock()
	r := d.probeRetries[gvr]
	d.mu.RUnlock()
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("retry budget reset after first walk")
	}
	if probes.Load() != 0 || !slices.Equal(d.Observation(gvr).UnansweredNamespaces, []string{"b"}) {
		t.Fatal("deadline lost unanswered check")
	}
}

func TestProbeRetryLaterReadClearsUnanswered(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, []string{"a", "b"}, map[string]int{"b": 3}), NamespaceFallbacks: []string{"a", "b"}, ProbeRetryBackoff: []time.Duration{time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	d.mu.RLock()
	r := d.probeRetries[gvr]
	d.mu.RUnlock()
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("retry did not exhaust")
	}
	if !slices.Equal(d.Observation(gvr).UnansweredNamespaces, []string{"b"}) {
		t.Fatal("missing exhausted namespace")
	}
	if err := d.EnsureWatchingContext(t.Context(), gvr, "b"); err != nil {
		t.Fatal(err)
	}
	got := d.Observation(gvr)
	if got.ProbeRetrying || len(got.UnansweredNamespaces) > 0 || !d.HasCoveringInformer(gvr, "b") {
		t.Fatalf("successful read did not settle gap: %+v", got)
	}
}

func TestProbeRetryStopCancelsInFlightRequest(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	entered, canceled := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	defer srv.Close()
	client, err := dynamic.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: client, ProbeRetryBackoff: []time.Duration{time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	// Inject an already unanswered walk to isolate the retry request's cancellation.
	d.noteWalk(gvr, probeOutcome{startedAt: time.Now(), unanswered: []string{"b"}})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("retry request not started")
	}
	d.mu.RLock()
	retry := d.probeRetries[gvr]
	d.mu.RUnlock()
	d.Stop()
	select {
	case <-retry.done:
	case <-time.After(time.Second):
		t.Fatal("Stop leaked in-flight retry")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel HTTP request")
	}
}

func TestProbeRetryNoInformerNamespaceIsolation(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, nil, map[string]int{"b": 100}), NamespaceFallbacks: []string{"a", "b"}, ProbeRetryBackoff: []time.Duration{time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.EnsureWatching(gvr); !IsClientRateLimited(err) {
		t.Fatalf("initial walk: %v", err)
	}
	a, b := d.NamespaceObservation(gvr, "a"), d.NamespaceObservation(gvr, "b")
	if a.ProbeRetrying || len(a.UnansweredNamespaces) > 0 || a.ReasonCode == "scope_probe_incomplete" || a.Scope != DynamicObservationScopeExplicitNamespaces {
		t.Fatalf("answered namespace inherited gap: %+v", a)
	}
	if !b.ProbeRetrying || !slices.Equal(b.UnansweredNamespaces, []string{"b"}) {
		t.Fatalf("unanswered namespace lost gap: %+v", b)
	}
}

func TestProbeRetryAllNamespacesForbiddenBecomeScope(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, nil, map[string]int{"b": 2}), NamespaceFallbacks: []string{"b"}, ProbeRetryBackoff: []time.Duration{time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.EnsureWatching(gvr); !IsClientRateLimited(err) {
		t.Fatalf("initial walk: %v", err)
	}
	d.mu.RLock()
	r := d.probeRetries[gvr]
	d.mu.RUnlock()
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("retry did not finish")
	}
	got := d.Observation(gvr)
	if got.ProbeRetrying || len(got.UnansweredNamespaces) > 0 || got.Scope != DynamicObservationScopeExplicitNamespaces || len(got.Namespaces) > 0 {
		t.Fatalf("Forbidden did not become empty collector scope: %+v", got)
	}
}

func TestProbeRetryClusterScopedDenialStaysKindAccess(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	discovery, err := NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	discovery.AddAPIResource(APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: false, Verbs: []string{"list", "watch"}})
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, nil, map[string]int{"": 1}), Discovery: discovery, NamespaceFallbacks: []string{"b"}, NamespaceFallbacksTruncated: true, ProbeRetryBackoff: []time.Duration{time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.EnsureWatching(gvr); !IsClientRateLimited(err) {
		t.Fatalf("initial walk: %v", err)
	}
	d.mu.RLock()
	r := d.probeRetries[gvr]
	d.mu.RUnlock()
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("retry did not finish")
	}
	got := d.Observation(gvr)
	if got.State != DynamicObservationDenied || got.Scope == DynamicObservationScopeExplicitNamespaces || got.Truncated || got.ProbeRetrying || len(got.UnansweredNamespaces) > 0 {
		t.Fatalf("cluster-scoped denial changed scope: %+v", got)
	}
}

func TestProbeRetryExplicitNamespaceFirst(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, clusterWide := range []bool{false, true} {
		t.Run(fmt.Sprintf("clusterWide=%v", clusterWide), func(t *testing.T) {
			allowed, refused := []string{"a", "b"}, map[string]int{"a": 2, "b": 1}
			if clusterWide {
				allowed, refused = []string{"", "a", "b"}, map[string]int{"": 1}
			}
			dyn := rateLimitedLists(gvr, allowed, refused)
			for _, ns := range []string{"a", "b"} {
				if err := dyn.Tracker().Add(&unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.io/v1", "kind": "Widget", "metadata": map[string]any{"namespace": ns, "name": "robusta-" + ns}}}); err != nil {
					t.Fatal(err)
				}
			}
			d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, NamespaceFallbacks: []string{"a", "b"}, ProbeRetryBackoff: []time.Duration{50 * time.Millisecond, 500 * time.Millisecond}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			if err := d.EnsureWatchingContext(t.Context(), gvr, "b"); !IsClientRateLimited(err) {
				t.Fatalf("initial explicit read: %v", err)
			}
			d.mu.RLock()
			r := d.probeRetries[gvr]
			fullWalk, whole := r.fullWalk, r.whole
			d.mu.RUnlock()
			if fullWalk || whole != clusterWide {
				t.Fatalf("explicit walk lost its scope: fullWalk=%v whole=%v", fullWalk, whole)
			}
			if !clusterWide {
				deadline := time.Now().Add(time.Second)
				for !d.HasCoveringInformer(gvr, "b") && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if !d.HasCoveringInformer(gvr, "b") || d.HasCoveringInformer(gvr, "a") {
					t.Fatal("expected b recovered while a remains unanswered")
				}
				if !d.WaitForSync(gvr, time.Second) {
					t.Fatal("b did not sync")
				}
				if _, err := d.Count(gvr, nil); err == nil {
					t.Fatalf("incomplete fanout produced an all-namespace count: %v", err)
				}
				if o := d.Observation(gvr); !o.ProbeRetrying || !slices.Equal(o.UnansweredNamespaces, []string{"a"}) {
					t.Fatalf("one namespace settled the scope: %+v", o)
				}
			}
			select {
			case <-r.done:
			case <-time.After(2 * time.Second):
				t.Fatal("retry did not finish")
			}
			if !d.WaitForSync(gvr, time.Second) || !d.HasCoveringInformer(gvr, "a") || !d.HasCoveringInformer(gvr, "b") {
				t.Fatal("both candidates were not eventually watched")
			}
			if got, err := d.Count(gvr, nil); err != nil || got != 2 {
				t.Fatalf("all-namespace count=%d err=%v", got, err)
			}
			if clusterWide && d.Observation(gvr).Scope != DynamicObservationScopeCluster {
				t.Fatalf("cluster-wide access became a namespace-only informer: %+v", d.Observation(gvr))
			}
		})
	}
}

func TestProbeRetryExhaustedNormalReadRecovery(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, read := range []string{"EnsureWatching", "List", "Get"} {
		t.Run(read, func(t *testing.T) {
			var allowed atomic.Bool
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.io/v1", "kind": "Widget", "metadata": map[string]any{"name": "robusta"}}}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, obj)
			dyn.PrependReactor("list", "widgets", func(ktesting.Action) (bool, runtime.Object, error) {
				if !allowed.Load() {
					return true, nil, errClientRateLimited
				}
				return false, nil, nil
			})
			discovery, err := NewResourceDiscovery(fake.NewClientset().Discovery())
			if err != nil {
				t.Fatal(err)
			}
			discovery.AddAPIResource(APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: false, Verbs: []string{"get", "list", "watch"}})
			d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery, ProbeRetryBackoff: []time.Duration{time.Millisecond}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			if err := d.EnsureWatching(gvr); !IsClientRateLimited(err) {
				t.Fatalf("initial read: %v", err)
			}
			d.mu.RLock()
			r := d.probeRetries[gvr]
			deadline := r.deadline
			d.mu.RUnlock()
			select {
			case <-r.done:
			case <-time.After(time.Second):
				t.Fatal("retry did not exhaust")
			}
			allowed.Store(true)
			switch read {
			case "EnsureWatching":
				err = d.EnsureWatching(gvr)
			case "List":
				_, err = d.List(gvr, "")
			case "Get":
				_, err = d.Get(gvr, "", "robusta")
			}
			if err != nil || !d.WaitForSync(gvr, time.Second) {
				t.Fatalf("normal read did not recover: %v", err)
			}
			if o := d.Observation(gvr); o.ProbeRetrying || o.State != DynamicObservationSynced || o.ReasonCode == "scope_probe_incomplete" {
				t.Fatalf("exhausted observation did not recover: %+v", o)
			}
			d.mu.RLock()
			same := d.probeRetries[gvr] == r && !r.running && r.deadline.Equal(deadline)
			d.mu.RUnlock()
			if !same {
				t.Fatal("on-demand read renewed the automatic retry")
			}
		})
	}
}

func TestProbeRetryMergesNewNamespaces(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, exhausted := range []bool{false, true} {
		t.Run(fmt.Sprintf("exhausted=%v", exhausted), func(t *testing.T) {
			var cAllowed atomic.Bool
			dyn := rateLimitedLists(gvr, []string{"b", "c"}, map[string]int{"b": 100, "c": 1})
			dyn.PrependReactor("list", "widgets", func(a ktesting.Action) (bool, runtime.Object, error) {
				if a.GetNamespace() == "c" && cAllowed.Load() {
					return true, &unstructured.UnstructuredList{}, nil
				}
				return false, nil, nil
			})
			d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, NamespaceFallbacks: []string{"b"}, ProbeRetryBackoff: []time.Duration{100 * time.Millisecond}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			startedAt := time.Now()
			if exhausted {
				startedAt = startedAt.Add(-probeRetryBudget)
			}
			d.noteWalk(gvr, probeOutcome{startedAt: startedAt, fullWalk: true, unanswered: []string{"b"}})
			d.mu.RLock()
			r, deadline := d.probeRetries[gvr], d.probeRetries[gvr].deadline
			d.mu.RUnlock()
			if exhausted {
				select {
				case <-r.done:
				case <-time.After(time.Second):
					t.Fatal("retry did not exhaust")
				}
			}
			if err := d.EnsureWatchingContext(t.Context(), gvr, "c"); !IsClientRateLimited(err) {
				t.Fatalf("new namespace read: %v", err)
			}
			o := d.Observation(gvr)
			if !slices.Equal(o.UnansweredNamespaces, []string{"b", "c"}) || o.ProbeRetrying == exhausted {
				t.Fatalf("new unanswered namespace was discarded: %+v", o)
			}
			if c := d.NamespaceObservation(gvr, "c"); !slices.Equal(c.UnansweredNamespaces, []string{"b", "c"}) || c.ProbeRetrying == exhausted {
				t.Fatalf("c was misclassified as collector scope: %+v", c)
			}
			d.mu.RLock()
			same := d.probeRetries[gvr] == r && r.deadline.Equal(deadline)
			d.mu.RUnlock()
			if !same {
				t.Fatal("new namespace replaced the retry owner or deadline")
			}
			if exhausted {
				cAllowed.Store(true)
				if err := d.EnsureWatching(gvr); err != nil {
					t.Fatalf("new exhausted namespace did not recover on demand: %v", err)
				}
			} else {
				select {
				case <-r.done:
				case <-time.After(time.Second):
					t.Fatal("retry did not finish")
				}
			}
			if !d.HasCoveringInformer(gvr, "c") || !slices.Equal(d.Observation(gvr).UnansweredNamespaces, []string{"b"}) {
				t.Fatalf("merged namespace did not recover independently: %+v", d.Observation(gvr))
			}
			d.mu.RLock()
			same = d.probeRetries[gvr] == r && !r.running && r.deadline.Equal(deadline)
			d.mu.RUnlock()
			if !same {
				t.Fatal("merged namespace recovery renewed the automatic retry")
			}
		})
	}
}

func TestProbeRetrySharedStartupLimit(t *testing.T) {
	const limit, kinds = 2, 12
	var active, peak atomic.Int32
	entered, release := make(chan struct{}, kinds), make(chan struct{})
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		requests.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	client, err := dynamic.NewForConfig(&rest.Config{Host: srv.URL, QPS: 10000, Burst: 10000})
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: client, ProbeRetryBackoff: []time.Duration{time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	d.probeSlots = make(chan struct{}, limit)
	gvrs := make([]schema.GroupVersionResource, kinds)
	for i := range gvrs {
		gvrs[i] = schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: fmt.Sprintf("widgets%d", i)}
	}
	startupDone := make(chan struct{})
	go func() {
		d.WarmupParallel(gvrs[:limit], time.Millisecond)
		close(startupDone)
	}()
	for range limit {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("startup probes did not enter")
		}
	}
	for i, gvr := range gvrs[limit:] {
		o := probeOutcome{startedAt: time.Now(), whole: i%2 == 0, fullWalk: true}
		if !o.whole {
			o.unanswered = []string{"b"}
		}
		d.noteWalk(gvr, o)
	}
	select {
	case <-entered:
		t.Fatal("retry bypassed occupied startup probe slots")
	case <-time.After(50 * time.Millisecond):
	}
	d.Stop()
	select {
	case <-startupDone:
	case <-time.After(time.Second):
		t.Fatal("startup did not cancel")
	}
	for _, gvr := range gvrs[limit:] {
		d.mu.RLock()
		r := d.probeRetries[gvr]
		d.mu.RUnlock()
		select {
		case <-r.done:
		case <-time.After(time.Second):
			t.Fatal("queued retry did not cancel")
		}
	}
	if peak.Load() != limit || requests.Load() != limit {
		t.Fatalf("peak=%d requests=%d want=%d", peak.Load(), requests.Load(), limit)
	}
}

func TestProbeRetryExplicitNamespaceOutsideFallback(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, exhausted := range []bool{false, true} {
		t.Run(fmt.Sprintf("exhausted=%v", exhausted), func(t *testing.T) {
			dyn := rateLimitedLists(gvr, []string{"a", "c"}, map[string]int{"": 1})
			var recovered atomic.Bool
			dyn.PrependReactor("list", "widgets", func(ktesting.Action) (bool, runtime.Object, error) {
				if exhausted && !recovered.Load() {
					return true, nil, errClientRateLimited
				}
				return false, nil, nil
			})
			d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, NamespaceFallbacks: []string{"a"}, ProbeRetryBackoff: []time.Duration{50 * time.Millisecond}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			if err := d.EnsureWatchingContext(t.Context(), gvr, "c"); !IsClientRateLimited(err) {
				t.Fatalf("initial explicit read: %v", err)
			}
			if o := d.Observation(gvr); !slices.Equal(o.UnansweredNamespaces, []string{"c"}) {
				t.Fatalf("cluster-wide gap lost the requested namespace: %+v", o)
			}
			d.mu.RLock()
			r := d.probeRetries[gvr]
			deadline := r.deadline
			d.mu.RUnlock()
			select {
			case <-r.done:
			case <-time.After(time.Second):
				t.Fatal("retry did not finish")
			}
			if exhausted {
				recovered.Store(true)
				if err := d.EnsureWatchingContext(t.Context(), gvr, "c"); !IsClientRateLimited(err) {
					t.Fatalf("first recovery probe: %v", err)
				}
				if err := d.EnsureWatchingContext(t.Context(), gvr, "c"); err != nil {
					t.Fatal(err)
				}
				d.mu.RLock()
				whole := r.whole
				d.mu.RUnlock()
				if whole || d.NamespaceObservation(gvr, "other").ReasonCode == "scope_probe_incomplete" {
					t.Fatal("answered cluster-wide denial left a whole-kind gap")
				}
				if err := d.EnsureWatching(gvr); err != nil {
					t.Fatal(err)
				}
			}
			if !d.HasCoveringInformer(gvr, "a") || !d.HasCoveringInformer(gvr, "c") || !d.WaitForSync(gvr, time.Second) {
				t.Fatal("fallback walk dropped the explicit namespace")
			}
			if o := d.Observation(gvr); o.ProbeRetrying || len(o.UnansweredNamespaces) > 0 || o.ReasonCode == "scope_probe_incomplete" {
				t.Fatalf("scope did not recover: %+v", o)
			}
			d.mu.RLock()
			same := d.probeRetries[gvr] == r && !r.running && r.deadline.Equal(deadline)
			d.mu.RUnlock()
			if !same {
				t.Fatal("outside-fallback recovery renewed the retry")
			}
		})
	}
}

func TestProbeRetryPreservesPriorFullWalk(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	dyn := rateLimitedLists(gvr, []string{"a", "c"}, map[string]int{"c": 1})
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, NamespaceFallbacks: []string{"a"}, ProbeRetryBackoff: []time.Duration{50 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.EnsureWatching(gvr); err != nil || !d.WaitForSync(gvr, time.Second) {
		t.Fatalf("initial full walk: %v", err)
	}
	before := len(dyn.Actions())
	if err := d.EnsureWatchingContext(t.Context(), gvr, "c"); !IsClientRateLimited(err) {
		t.Fatalf("explicit namespace probe: %v", err)
	}
	d.mu.RLock()
	r := d.probeRetries[gvr]
	fullWalk := r.fullWalk
	d.mu.RUnlock()
	if !fullWalk {
		t.Fatal("explicit gap discarded the already-completed fallback walk")
	}
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("retry did not finish")
	}
	if !d.HasCoveringInformer(gvr, "c") || !d.WaitForSync(gvr, time.Second) {
		t.Fatal("explicit gap did not recover")
	}
	for _, action := range dyn.Actions()[before:] {
		if action.GetVerb() == "list" && action.GetNamespace() == "a" {
			t.Fatal("explicit retry needlessly repeated the settled fallback walk")
		}
	}
}

func TestProbeRetrySingleFallbackCountsRemainUnavailable(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, exhausted := range []bool{false, true} {
		t.Run(fmt.Sprintf("exhausted=%v", exhausted), func(t *testing.T) {
			backoff, refused := time.Hour, 1
			if exhausted {
				backoff, refused = time.Millisecond, 2
			}
			d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, []string{"a", "c"}, map[string]int{"c": refused}), NamespaceFallbacks: []string{"a"}, ProbeRetryBackoff: []time.Duration{backoff}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			if err := d.EnsureWatching(gvr); err != nil || !d.WaitForSync(gvr, time.Second) {
				t.Fatalf("initial fallback sync: %v", err)
			}
			if n, err := d.Count(gvr, nil); err != nil || n != 0 {
				t.Fatalf("initial count: %d, %v", n, err)
			}
			if err := d.EnsureWatchingContext(t.Context(), gvr, "c"); !IsClientRateLimited(err) {
				t.Fatalf("explicit namespace probe: %v", err)
			}
			if exhausted {
				d.mu.RLock()
				done := d.probeRetries[gvr].done
				d.mu.RUnlock()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("retry did not exhaust")
				}
			}
			if o := d.Observation(gvr); o.ProbeRetrying == exhausted || !slices.Equal(o.UnansweredNamespaces, []string{"c"}) {
				t.Fatalf("missing unresolved scope: %+v", o)
			}
			_, countErr := d.Count(gvr, nil)
			_, counted := d.CountWatched(nil)[gvr]
			if countErr == nil || counted {
				t.Fatalf("unanswered c accepted as authoritative: Count error=%v, CountWatched present=%v", countErr, counted)
			}
			if n, err := d.Count(gvr, []string{"a"}); err != nil || n != 0 {
				t.Fatalf("answered namespace count unavailable: %d, %v", n, err)
			}
			if n, ok := d.CountWatched([]string{"a"})[gvr]; !ok || n != 0 {
				t.Fatalf("answered namespace missing from watched counts: %d, %v", n, ok)
			}
			if err := d.EnsureWatchingContext(t.Context(), gvr, "c"); err != nil || !d.WaitForSync(gvr, time.Second) {
				t.Fatalf("explicit scope did not recover: %v", err)
			}
			if n, err := d.Count(gvr, nil); err != nil || n != 0 {
				t.Fatalf("recovered count unavailable: %d, %v", n, err)
			}
			if n, ok := d.CountWatched(nil)[gvr]; !ok || n != 0 {
				t.Fatalf("recovered watched count unavailable: %d, %v", n, ok)
			}
		})
	}
}

func TestProbeStartupQueueDoesNotConsumeWalkBudget(t *testing.T) {
	const limit, kinds = 2, 12
	gvrs := make([]schema.GroupVersionResource, kinds)
	listKinds := map[schema.GroupVersionResource]string{}
	for i := range gvrs {
		gvrs[i] = schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: fmt.Sprintf("widgets%d", i)}
		listKinds[gvrs[i]] = "WidgetList"
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	d.probeSlots = make(chan struct{}, limit)
	for range limit {
		d.probeSlots <- struct{}{}
	}
	results := make(chan probeOutcome, kinds)
	for _, gvr := range gvrs {
		go func() { results <- d.walkScopes(gvr, "") }()
	}
	select {
	case <-results:
		t.Fatal("queued walk bypassed startup admission")
	case <-time.After(50 * time.Millisecond):
	}
	releasedAt := time.Now()
	for range limit {
		<-d.probeSlots
	}
	for range kinds {
		select {
		case o := <-results:
			if o.startedAt.Before(releasedAt) || !o.complete || o.whole || o.err != nil {
				t.Fatalf("queue time consumed the walk budget: %+v release=%v", o, releasedAt)
			}
		case <-time.After(time.Second):
			t.Fatal("queued startup walk did not answer")
		}
	}
}

func TestProbeRetrySingleNamespaceAnswerDoesNotSettle(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, []string{"a", "b"}, map[string]int{"b": 1}), NamespaceFallbacks: []string{"a", "b"}, ProbeRetryBackoff: []time.Duration{500 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.EnsureWatchingContext(t.Context(), gvr, "b"); !IsClientRateLimited(err) {
		t.Fatalf("initial explicit read: %v", err)
	}
	d.mu.RLock()
	r := d.probeRetries[gvr]
	d.mu.RUnlock()
	if err := d.EnsureWatchingContext(t.Context(), gvr, "b"); err != nil {
		t.Fatal(err)
	}
	if d.HasCoveringInformer(gvr, "") || !d.Observation(gvr).ProbeRetrying {
		t.Fatalf("one answered namespace settled the fallback scope: %+v", d.Observation(gvr))
	}
	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
		t.Fatal("full fallback walk did not finish")
	}
	if !d.HasCoveringInformer(gvr, "a") || !d.HasCoveringInformer(gvr, "b") || !d.WaitForSync(gvr, time.Second) {
		t.Fatal("explicit answer prevented the remaining fallback walk")
	}
}

// Checks still unanswered when the retry budget runs out keep being re-checked,
// slowly, so a reader of the cache alone gets the kind once the API answers.
// Each re-check walks as a first probe would: an identity limited to some
// namespaces falls back to them instead of watching cluster-wide on a guess.
func TestProbeRecoveryAfterTheBudget(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, tc := range []struct {
		name      string
		listable  []string
		throttled map[string]int
		fallbacks []string
		pinned    string
		watched   string
	}{
		{"cluster-wide identity", []string{""}, map[string]int{"": 3}, nil, "", ""},
		{"identity limited to a namespace", []string{"a"}, map[string]int{"": 3}, []string{"a"}, "", "a"},
		{"pinned namespace", []string{"shop"}, map[string]int{"shop": 3}, nil, "shop", "shop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := NewDynamicResourceCache(DynamicCacheConfig{
				DynamicClient:          rateLimitedLists(gvr, tc.listable, tc.throttled),
				NamespaceFallbacks:     tc.fallbacks,
				NamespaceScoped:        tc.pinned != "",
				Namespace:              tc.pinned,
				ProbeRetryBackoff:      []time.Duration{10 * time.Millisecond},
				ProbeRecoveryIntervals: []time.Duration{20 * time.Millisecond},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			_ = d.EnsureWatching(gvr)
			d.mu.RLock()
			r := d.probeRetries[gvr]
			d.mu.RUnlock()
			if r == nil {
				t.Fatal("no retry started")
			}
			select {
			case <-r.done:
			case <-time.After(3 * time.Second):
				t.Fatal("retry did not finish")
			}
			if o := d.Observation(gvr); o.ProbeRetrying {
				t.Fatalf("an exhausted budget must not read as still retrying: %+v", o)
			}
			deadline := time.Now().Add(3 * time.Second)
			for !d.HasCoveringInformer(gvr, tc.watched) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if !d.HasCoveringInformer(gvr, tc.watched) {
				t.Fatalf("recovery never watched the kind: %+v", d.Observation(gvr))
			}
			if tc.watched != "" && d.HasCoveringInformer(gvr, "elsewhere") {
				t.Fatal("recovery watched cluster-wide instead of the namespace")
			}
		})
	}
}

// A retry record that settled has no owner left, so a gap a later read finds
// starts a fresh retry instead of waiting forever.
func TestProbeRetryRearmsAfterSettling(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient:          rateLimitedLists(gvr, []string{"a", "c"}, map[string]int{"a": 2, "c": 2}),
		NamespaceFallbacks:     []string{"a"},
		ProbeRetryBackoff:      []time.Duration{10 * time.Millisecond, 10 * time.Millisecond},
		ProbeRecoveryIntervals: []time.Duration{time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	_ = d.EnsureWatching(gvr)
	d.mu.RLock()
	first := d.probeRetries[gvr]
	d.mu.RUnlock()
	if first == nil {
		t.Fatal("no retry for the throttled fallback namespace")
	}
	<-first.done
	d.mu.RLock()
	settled := first.settled()
	d.mu.RUnlock()
	if !settled || !d.HasCoveringInformer(gvr, "a") {
		t.Fatalf("first retry didn't settle on namespace a: %+v", d.Observation(gvr))
	}
	_ = d.EnsureWatchingContext(t.Context(), gvr, "c")
	deadline := time.Now().Add(3 * time.Second)
	for !d.HasCoveringInformer(gvr, "c") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	d.mu.RLock()
	rearmed := d.probeRetries[gvr] != first
	d.mu.RUnlock()
	if !rearmed || !d.HasCoveringInformer(gvr, "c") {
		t.Fatalf("a gap found after settling was never retried: rearmed=%v %+v", rearmed, d.Observation(gvr))
	}
	if ownerLive(d, first) {
		t.Fatal("the settled record's owner outlived it")
	}
}

// Stopping the cache ends a recovery waiting for its next re-check, which
// would otherwise hold the stopped cache until then.
func TestProbeRecoveryStopsWithTheCache(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient:          rateLimitedLists(gvr, []string{""}, map[string]int{"": 100}),
		ProbeRetryBackoff:      []time.Duration{10 * time.Millisecond},
		ProbeRecoveryIntervals: []time.Duration{time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = d.EnsureWatching(gvr)
	d.mu.RLock()
	r := d.probeRetries[gvr]
	d.mu.RUnlock()
	<-r.done
	if !ownerLive(d, r) {
		t.Fatal("no recovery after the budget ran out")
	}
	d.Stop()
	deadline := time.Now().Add(time.Second)
	for ownerLive(d, r) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ownerLive(d, r) {
		t.Fatal("the recovery outlived Stop")
	}
}

func ownerLive(d *DynamicResourceCache, r *probeRetry) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return r.owned
}

// A cluster-wide watch started on a failed check is refused, under the same
// lock that would replace them, while namespace watches exist.
func TestFailedOpenWatchKeepsNamespaceWatches(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, []string{"", "a"}, nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.startWatching(gvr, "a"); err != nil {
		t.Fatal(err)
	}
	if err := d.startWatchingScope(gvr, "", true); !errors.Is(err, errNamespaceWatchesKept) {
		t.Fatalf("failed-open cluster-wide watch = %v", err)
	}
	if !d.HasCoveringInformer(gvr, "a") || d.HasCoveringInformer(gvr, "elsewhere") {
		t.Fatal("the namespace watch was replaced")
	}
	if err := d.startWatchingScope(gvr, "", false); err != nil || !d.HasCoveringInformer(gvr, "elsewhere") {
		t.Fatalf("a passed cluster-wide check must still replace namespace watches: %v", err)
	}
}

// A recovery walk whose cluster-wide check fails rather than passes (here a
// 503 from a proxy) must not replace namespace watches that work with a
// cluster-wide one.
func TestProbeRecoveryKeepsNamespaceWatchesOnAFailedCheck(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	var mu sync.Mutex
	clusterCalls := 0
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	dyn.PrependReactor("list", "widgets", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() != "" {
			return false, nil, nil
		}
		mu.Lock()
		defer mu.Unlock()
		clusterCalls++
		if clusterCalls <= 2 {
			return true, nil, errClientRateLimited
		}
		return true, nil, apierrors.NewServiceUnavailable("proxy unavailable")
	})
	d, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient:          dyn,
		ProbeRetryBackoff:      []time.Duration{50 * time.Millisecond},
		ProbeRecoveryIntervals: []time.Duration{10 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	_ = d.EnsureWatching(gvr)
	// A read started a namespace watch before the background retries ran.
	if err := d.startWatching(gvr, "a"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		calls := clusterCalls
		mu.Unlock()
		if calls >= 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	calls := clusterCalls
	mu.Unlock()
	if calls < 5 {
		t.Fatalf("recovery made %d cluster-wide checks, so the failed ones were never tried", calls)
	}
	d.mu.RLock()
	_, clusterWide := d.informers[informerKey{gvr: gvr}]
	d.mu.RUnlock()
	if clusterWide || !d.HasCoveringInformer(gvr, "a") {
		t.Fatalf("a failed cluster-wide check replaced the namespace watch: clusterWide=%v", clusterWide)
	}
}

// A retained credential rejection is never reported as an unfinished check,
// whatever the retry record still holds.
func TestObservationKeepsCredentialRejection(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, nil, nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	d.retainObservation(gvr, DynamicObservationDenied, "credentials_rejected", false)
	d.mu.Lock()
	d.probeRetries[gvr] = &probeRetry{whole: true}
	d.mu.Unlock()
	if o := d.Observation(gvr); o.ReasonCode != "credentials_rejected" {
		t.Fatalf("observation = %+v", o)
	}
}

// A new gap on a record whose owner can no longer take it, because the owner
// ended or met rejected credentials, gets a fresh owner that also takes over
// every gap still outstanding.
func TestProbeRetryRearmTakesOverOutstandingGaps(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, tc := range []struct {
		name            string
		owned, rejected bool
	}{
		{"owner ended", false, false},
		{"owner rejected but not yet released", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := NewDynamicResourceCache(DynamicCacheConfig{
				DynamicClient:          rateLimitedLists(gvr, nil, map[string]int{"b": 100, "c": 100, "d": 100}),
				NamespaceFallbacks:     []string{"a", "b", "c"},
				ProbeRetryBackoff:      []time.Duration{time.Hour},
				ProbeRecoveryIntervals: []time.Duration{time.Hour},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			prev := &probeRetry{unanswered: []string{"b", "c"}, fullWalk: true, owned: tc.owned, rejected: tc.rejected, answered: map[string]time.Time{}, ctx: ctx, cancel: cancel, done: make(chan struct{})}
			d.mu.Lock()
			d.probeRetries[gvr] = prev
			d.mu.Unlock()
			d.noteWalk(gvr, probeOutcome{unanswered: []string{"d"}, startedAt: time.Now()})
			d.mu.RLock()
			r := d.probeRetries[gvr]
			gaps := slices.Sorted(slices.Values(r.unanswered))
			live := r.owned && !r.rejected
			d.mu.RUnlock()
			if r == prev || !live || !slices.Equal(gaps, []string{"b", "c", "d"}) {
				t.Fatalf("re-armed=%v live=%v gaps=%v, want a fresh owner of b, c and d", r != prev, live, gaps)
			}
			if ctx.Err() == nil {
				t.Fatal("the replaced record's owner was not ended")
			}
		})
	}
}
