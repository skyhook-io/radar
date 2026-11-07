package k8score

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

var deniedWidgets = schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}

// countedWidgetLists answers every list with answer(namespace) and counts the
// calls. A nil answer falls through to an empty list.
func countedWidgetLists(calls *atomic.Int64, answer func(ns string) error) *dynamicfake.FakeDynamicClient {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{deniedWidgets: "WidgetList"})
	dyn.PrependReactor("list", "widgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		calls.Add(1)
		if err := answer(action.GetNamespace()); err != nil {
			return true, nil, err
		}
		return false, nil, nil
	})
	return dyn
}

func forbidden(string) error {
	return apierrors.NewForbidden(deniedWidgets.GroupResource(), "", errors.New("collector cannot list"))
}

func deniedCache(t *testing.T, calls *atomic.Int64, answer func(string) error) *DynamicResourceCache {
	t.Helper()
	d, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient:      countedWidgetLists(calls, answer),
		NamespaceFallbacks: []string{"a", "b"},
		ProbeRetryBackoff:  []time.Duration{time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	return d
}

func expireDenials(d *DynamicResourceCache) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for key, denial := range d.deniedScopes {
		denial.at = denial.at.Add(-deniedScopeTTL)
		d.deniedScopes[key] = denial
	}
}

func TestCompleteDenialAnswersRepeatedReads(t *testing.T) {
	var calls atomic.Int64
	d := deniedCache(t, &calls, forbidden)

	first := d.EnsureWatching(deniedWidgets)
	if !apierrors.IsForbidden(first) {
		t.Fatalf("first read: %v", first)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("first read made %d checks, want the cluster-wide one and two namespaces", got)
	}
	observed := d.Observation(deniedWidgets)

	for range 5 {
		if _, err := d.List(deniedWidgets, ""); !apierrors.IsForbidden(err) || err.Error() != first.Error() {
			t.Fatalf("repeated read returned %v, want %v", err, first)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("repeated reads walked again: %d checks", got)
	}
	if got := d.Observation(deniedWidgets); got.State != observed.State || !got.ObservedAt.Equal(*observed.ObservedAt) {
		t.Fatalf("a remembered answer changed the observation: %+v, was %+v", got, observed)
	}
}

func TestDenialExpiryFindsNewGrant(t *testing.T) {
	for _, tc := range []struct {
		name    string
		granted string
		want    string
	}{
		{"cluster-wide", "", ""},
		{"one candidate namespace", "b", "b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls, access atomic.Int64
			d := deniedCache(t, &calls, func(ns string) error {
				if access.Load() == 1 && ns == tc.granted {
					return nil
				}
				return forbidden(ns)
			})
			if err := d.EnsureWatching(deniedWidgets); !apierrors.IsForbidden(err) {
				t.Fatalf("before the grant: %v", err)
			}
			access.Store(1)
			if err := d.EnsureWatching(deniedWidgets); !apierrors.IsForbidden(err) {
				t.Fatalf("within the TTL: %v", err)
			}
			expireDenials(d)
			if err := d.EnsureWatching(deniedWidgets); err != nil {
				t.Fatalf("after the TTL: %v", err)
			}
			if !d.HasCoveringInformer(deniedWidgets, tc.want) {
				t.Fatalf("no watch of %q after the grant", tc.want)
			}
		})
	}
}

// Only a walk whose every check was answered Forbidden is a denial. Each of
// these leaves the next read free to ask again.
func TestOnlyCompleteRefusalsAreRemembered(t *testing.T) {
	unauthorized := apierrors.NewUnauthorized("token expired")
	for _, tc := range []struct {
		name   string
		answer func(ns string) error
	}{
		{"namespace rate limited", func(ns string) error {
			if ns == "b" {
				return errClientRateLimited
			}
			return forbidden(ns)
		}},
		{"namespace timed out", func(ns string) error {
			if ns == "b" {
				return context.DeadlineExceeded
			}
			return forbidden(ns)
		}},
		{"cluster-wide check timed out", func(string) error { return context.DeadlineExceeded }},
		{"credentials rejected", func(string) error { return unauthorized }},
		{"credentials rejected in a namespace", func(ns string) error {
			if ns == "a" {
				return unauthorized
			}
			return forbidden(ns)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			d := deniedCache(t, &calls, tc.answer)
			if err := d.EnsureWatching(deniedWidgets); err == nil {
				t.Fatal("expected an error")
			}
			if err := d.recentDenial(deniedWidgets, ""); err != nil {
				t.Fatalf("remembered as a denial: %v", err)
			}
		})
	}
}

// A walk for all namespaces only reached its candidates, and a refused
// namespace says nothing about the others.
func TestDenialCoversOnlyTheScopeWalked(t *testing.T) {
	var calls atomic.Int64
	d := deniedCache(t, &calls, forbidden)
	if err := d.EnsureWatching(deniedWidgets); !apierrors.IsForbidden(err) {
		t.Fatal(err)
	}
	if _, err := d.List(deniedWidgets, "outside"); !apierrors.IsForbidden(err) {
		t.Fatalf("namespace read: %v", err)
	}
	if got := calls.Load(); got != 5 {
		t.Fatalf("%d checks, want the namespace read's own two after the walk's three", got)
	}
	if _, err := d.List(deniedWidgets, "outside"); !apierrors.IsForbidden(err) {
		t.Fatalf("repeated namespace read: %v", err)
	}
	if got := calls.Load(); got != 5 {
		t.Fatalf("repeated namespace read walked again: %d checks", got)
	}
}

// The startup passes record a kind's refusal before anything reads it, so the
// first read needs no walk of its own.
func TestStartupDenialsAnswerTheFirstRead(t *testing.T) {
	for name, startup := range map[string]func(d *DynamicResourceCache){
		"warmup": func(d *DynamicResourceCache) {
			d.WarmupParallel([]schema.GroupVersionResource{deniedWidgets}, time.Millisecond)
		},
		"small CRD scan": func(d *DynamicResourceCache) {
			d.watchSmallCRDs([]schema.GroupVersionResource{deniedWidgets})
		},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			d := deniedCache(t, &calls, forbidden)
			startup(d)
			before := calls.Load()
			if err := d.EnsureWatching(deniedWidgets); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "no access to widgets.example.io/v1") {
				t.Fatalf("first read: %v", err)
			}
			if got := calls.Load(); got != before {
				t.Fatalf("first read walked again: %d checks after %d", got, before)
			}
		})
	}
}

// A later check that found something other than a refusal replaces the memory
// of one, the same way it replaces the retained observation.
func TestLaterIncompleteCheckClearsDenial(t *testing.T) {
	var calls, slow atomic.Int64
	d := deniedCache(t, &calls, func(ns string) error {
		if slow.Load() == 1 {
			return context.DeadlineExceeded
		}
		return forbidden(ns)
	})
	if err := d.EnsureWatching(deniedWidgets); !apierrors.IsForbidden(err) {
		t.Fatal(err)
	}
	slow.Store(1)
	d.WarmupParallel([]schema.GroupVersionResource{deniedWidgets}, time.Millisecond)
	if err := d.recentDenial(deniedWidgets, ""); err != nil {
		t.Fatalf("denial survived an unanswered check: %v", err)
	}
}

func TestStoppedCacheServesNoDenial(t *testing.T) {
	var calls atomic.Int64
	d := deniedCache(t, &calls, forbidden)
	if err := d.EnsureWatching(deniedWidgets); !apierrors.IsForbidden(err) {
		t.Fatal(err)
	}
	d.Stop()
	if err := d.recentDenial(deniedWidgets, ""); err != nil {
		t.Fatalf("stopped cache answered from memory: %v", err)
	}
}

// candidateBarrierClient refuses every list. Namespace checks wait until want
// of them are in flight, or for patience, and peak is the most it saw at once.
// It is an HTTP server because the fake dynamic client runs reactors one at a
// time.
func candidateBarrierClient(t *testing.T, want int32, patience time.Duration) (client dynamic.Interface, peak *atomic.Int32) {
	t.Helper()
	var inFlight atomic.Int32
	peak = &atomic.Int32{}
	full := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/namespaces/") {
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			if n == want {
				close(full)
			}
			select {
			case <-full:
			case <-time.After(patience):
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`)
	}))
	t.Cleanup(srv.Close)
	client, err := dynamic.NewForConfig(&rest.Config{Host: srv.URL, QPS: 10000, Burst: 10000})
	if err != nil {
		t.Fatal(err)
	}
	return client, peak
}

func TestSpareProbeSlotsCheckCandidatesAtOnce(t *testing.T) {
	namespaces := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	client, peak := candidateBarrierClient(t, int32(len(namespaces)), time.Second)
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: client, NamespaceFallbacks: namespaces})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.EnsureWatching(deniedWidgets); !apierrors.IsForbidden(err) {
		t.Fatal(err)
	}
	if got := peak.Load(); got != int32(len(namespaces)) {
		t.Fatalf("%d candidates in flight at once, want %d", got, len(namespaces))
	}
	if err := d.recentDenial(deniedWidgets, ""); err == nil {
		t.Fatal("a walk checked at once did not end in a complete denial")
	}
}

// Other walks holding the slots leave this one to its own: it never adds to
// what is running, so a busy startup stays within the shared limit.
func TestBusyProbeSlotsCheckCandidatesInTurn(t *testing.T) {
	namespaces := []string{"a", "b", "c"}
	client, peak := candidateBarrierClient(t, int32(len(namespaces)), 5*time.Millisecond)
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: client, NamespaceFallbacks: namespaces})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	d.probeSlots = make(chan struct{}, 1)
	if err := d.EnsureWatching(deniedWidgets); !apierrors.IsForbidden(err) {
		t.Fatal(err)
	}
	if got := peak.Load(); got != 1 {
		t.Fatalf("%d candidates in flight with no spare slot, want 1", got)
	}
}

// Candidates finish in any order; the scopes a walk returns must not.
func TestConcurrentWalkKeepsCandidateOrder(t *testing.T) {
	namespaces := []string{"e", "d", "c", "b", "a"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(deniedWidgets, namespaces, nil), NamespaceFallbacks: namespaces})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	scopes, complete, err := d.probeScopes(deniedWidgets, "")
	if err != nil || !complete || !slices.Equal(scopes, namespaces) {
		t.Fatalf("scopes %v complete %v err %v, want %v", scopes, complete, err, namespaces)
	}
}
