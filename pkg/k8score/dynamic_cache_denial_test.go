package k8score

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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
