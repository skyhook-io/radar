package k8score

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// errClientRateLimited is what client-go returns when its client-side rate
// limiter won't wait past the request's deadline: the request is never sent.
var errClientRateLimited = fmt.Errorf("client rate limiter Wait returned an error: %w", errors.New("rate: Wait(n=1) would exceed context deadline"))

// rateLimitedLists answers list probes per namespace: listable namespaces
// succeed, others are forbidden, and each namespace's first `throttled[ns]`
// probes are refused client-side before reaching the server.
func rateLimitedLists(gvr schema.GroupVersionResource, listable []string, throttled map[string]int) *dynamicfake.FakeDynamicClient {
	var mu sync.Mutex
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	dyn.PrependReactor("list", "widgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ns := action.GetNamespace()
		mu.Lock()
		if throttled[ns] > 0 {
			throttled[ns]--
			mu.Unlock()
			return true, nil, errClientRateLimited
		}
		mu.Unlock()
		for _, allowed := range listable {
			if ns == allowed {
				return false, nil, nil
			}
		}
		return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("collector cannot list here"))
	})
	return dyn
}

// A probe the client-side rate limiter refused never reached the API server,
// so it proves nothing about access: it must not become a grant (a watch in a
// namespace the collector can't list, which then never syncs).
func TestRateLimitedProbeIsNotAGrant(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, tc := range []struct {
		name      string
		throttled map[string]int
		want      []string
		complete  bool
	}{
		// Refused once at the candidate's own deadline, then answered from
		// the walk's budget: the true answer wins.
		{"answered on retry", map[string]int{"team-a": 1, "team-denied": 1}, []string{"team-b", "team-a"}, true},
		// Never answered: the namespace is left out and the walk retried later.
		{"never answered", map[string]int{"team-a": 1, "team-denied": 100}, []string{"team-b", "team-a"}, false},
		// Every refused candidate gets its retry, not just the first.
		{"all refused once", map[string]int{"team-a": 1, "team-b": 1, "team-denied": 1}, []string{"team-a", "team-b"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := NewDynamicResourceCache(DynamicCacheConfig{
				DynamicClient:      rateLimitedLists(gvr, []string{"team-a", "team-b"}, tc.throttled),
				NamespaceFallbacks: []string{"team-a", "team-b", "team-denied"},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			scopes, complete, err := d.probeScopes(gvr, "")
			if err != nil || !reflect.DeepEqual(scopes, tc.want) || complete != tc.complete {
				t.Fatalf("scopes=%v complete=%v err=%v, want %v complete=%v", scopes, complete, err, tc.want, tc.complete)
			}
		})
	}
}

func TestRateLimitedProbeStartsNoWatch(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, tc := range []struct {
		name, namespace string
		throttled       map[string]int
	}{
		{"cluster-wide probe", "", map[string]int{"": 100}},
		{"requested namespace probe", "team-denied", map[string]int{"team-denied": 100}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, nil, tc.throttled)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			if err := d.EnsureWatchingContext(t.Context(), gvr, tc.namespace); !IsClientRateLimited(err) {
				t.Fatalf("rate-limited probe: %v", err)
			}
			if watched := d.GetWatchedResources(); len(watched) != 0 {
				t.Fatalf("rate-limited probe started a watch: %v", watched)
			}
			if got := d.Observation(gvr); got.State == DynamicObservationDenied {
				t.Fatalf("rate-limited probe recorded as denial: %+v", got)
			}
		})
	}
}

// A walk that granted nothing because some candidates never answered is not
// a denial: it neither reports Forbidden (which a resource list would turn
// into "you can't") nor records the kind as denied.
func TestUnansweredWalkIsNotADenial(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient:      rateLimitedLists(gvr, []string{"team-a"}, map[string]int{"team-a": 100}),
		NamespaceFallbacks: []string{"team-a", "team-denied"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	err = d.EnsureWatchingContext(t.Context(), gvr, "")
	if err == nil || apierrors.IsForbidden(err) || !IsClientRateLimited(err) {
		t.Fatalf("unanswered walk returned %v", err)
	}
	if watched := d.GetWatchedResources(); len(watched) != 0 {
		t.Fatalf("unanswered walk started a watch: %v", watched)
	}
	if got := d.Observation(gvr); got.State != DynamicObservationDeferred || got.ReasonCode != "scope_probe_incomplete" {
		t.Fatalf("unanswered walk observation = %+v", got)
	}
}

// A probe still in flight when the walk's budget runs out answers nothing,
// even if it comes back Forbidden: with another candidate unanswered, the walk
// is incomplete, not a denial.
func TestWalkOutOfTimeIsNotADenial(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	dyn.PrependReactor("list", "widgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		switch action.GetNamespace() {
		case "team-a":
			return true, nil, errClientRateLimited
		case "team-slow":
			time.Sleep(7 * time.Second) // past the two-candidate walk budget
		}
		return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("denied"))
	})
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, NamespaceFallbacks: []string{"team-a", "team-slow"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	scopes, complete, err := d.probeScopes(gvr, "")
	if len(scopes) != 0 || complete || err == nil || apierrors.IsForbidden(err) {
		t.Fatalf("scopes=%v complete=%v err=%v; want an incomplete walk that is not a denial", scopes, complete, err)
	}
}
