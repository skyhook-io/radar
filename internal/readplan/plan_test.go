package readplan

import (
	"errors"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
	"reflect"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDynamicVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		o       k8score.DynamicResourceObservation
		outside bool
		state   State
		code    readreason.Code
	}{
		{"ready empty", k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced}, false, Readable, ""},
		{"pending", k8score.DynamicResourceObservation{State: k8score.DynamicObservationSyncing}, false, Gap, readreason.KindSyncPending},
		{"stalled", k8score.DynamicResourceObservation{State: k8score.DynamicObservationSyncing, ReasonCode: "sync_stalled"}, false, Gap, readreason.KindSyncTimedOut},
		{"rejection beats retry", k8score.DynamicResourceObservation{ReasonCode: "credentials_rejected", ProbeRetrying: true}, true, Gap, readreason.RadarCredentialsRejected},
		{"retry beats outside", k8score.DynamicResourceObservation{ProbeRetrying: true}, true, Gap, readreason.KindSyncPending},
		{"exhausted beats outside", k8score.DynamicResourceObservation{UnansweredNamespaces: []string{"b"}}, true, Gap, readreason.ReadError},
		{"unknown whole", k8score.DynamicResourceObservation{ReasonCode: "scope_probe_incomplete"}, false, Gap, readreason.ReadError},
		{"outside", k8score.DynamicResourceObservation{State: k8score.DynamicObservationUnwatched}, true, OutsideCollection, readreason.RadarNamespaceNotCollected},
		{"forbidden", k8score.DynamicResourceObservation{State: k8score.DynamicObservationDenied}, false, Gap, readreason.RadarForbidden},
		{"unsupported", k8score.DynamicResourceObservation{State: k8score.DynamicObservationUnsupported}, false, Gap, readreason.KindNotWatchable},
		{"stopped beats ready", k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced, ReasonCode: "cache_stopped"}, false, Gap, readreason.ConnectionChanged},
		{"eager", k8score.DynamicResourceObservation{State: k8score.DynamicObservationDeferred, ReasonCode: "eager_watch_pending"}, false, Gap, readreason.KindSyncPending},
		{"failed probe", k8score.DynamicResourceObservation{State: k8score.DynamicObservationDeferred, ReasonCode: "scope_probe_failed"}, false, Gap, readreason.ReadError},
		{"unwatched", k8score.DynamicResourceObservation{}, false, Gap, readreason.RadarKindNotCollected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, code := dynamicVerdict(tc.o, tc.outside)
			if state != tc.state || code != tc.code {
				t.Fatalf("got %v/%s, want %v/%s", state, code, tc.state, tc.code)
			}
		})
	}
}

func TestDynamicEmptyAndWholeExtents(t *testing.T) {
	cache := &k8s.DynamicResourceCache{}
	// A nil embedded cache's observation is an unwatched kind.
	for _, tc := range []struct {
		target  []string
		cluster bool
		extent  Extent
		parts   int
	}{
		{[]string{}, false, NamespaceSet, 0},
		{nil, false, UnknownNamespaces, 1},
		{nil, true, Cluster, 1},
	} {
		p := Dynamic(cache, schema.GroupVersionResource{}, tc.target, tc.cluster)
		if len(p.Partitions) != tc.parts || tc.parts > 0 && p.Partitions[0].Extent != tc.extent {
			t.Fatalf("%+v: %+v", tc, p)
		}
	}
}

func TestTypedPartitionsRetainCredentialGaps(t *testing.T) {
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(), ResourceTypes: map[string]bool{"configmaps": true}, ResourceScopes: map[string]k8score.ResourceScope{"configmaps": {Enabled: true, Namespace: "a"}}, DeferredTypes: map[string]bool{"configmaps": true}, DebugSyncDelays: map[string]time.Duration{"configmaps": time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}
	permissions := &k8s.PermissionCheckResult{NamespaceProbeErrors: map[string]map[string]error{"configmaps": {"b": apierrors.NewUnauthorized("rejected")}}}
	p := Typed(cache, permissions, "configmaps", []string{"c", "b", "a", "b"}, false)
	want := []Partition{
		{Extent: NamespaceSet, Namespaces: []string{"b"}, Verdict: Verdict{Gap, readreason.RadarCredentialsRejected}},
		{Extent: NamespaceSet, Namespaces: []string{"c"}, Verdict: Verdict{OutsideCollection, readreason.RadarNamespaceNotCollected}},
		{Extent: NamespaceSet, Namespaces: []string{"a"}, Verdict: Verdict{Gap, readreason.KindSyncPending}},
	}
	if !reflect.DeepEqual(p.Partitions, want) {
		t.Fatalf("%+v", p)
	}
	p = Typed(cache, permissions, "configmaps", nil, false)
	if !p.CollectionLimited || len(p.Partitions) != 2 {
		t.Fatalf("all: %+v", p)
	}
	if p := Typed(cache, permissions, "missing", []string{}, false); len(p.Partitions) != 0 {
		t.Fatalf("empty: %+v", p)
	}
	for _, tc := range []struct {
		resource string
		code     readreason.Code
	}{{"missing", readreason.RadarKindNotCollected}, {"pods", readreason.RadarForbidden}} {
		permissions.Scopes = map[string]k8score.ResourceScope{"pods": {Enabled: false}}
		p := Typed(cache, permissions, tc.resource, nil, false)
		if len(p.Partitions) != 1 || p.Partitions[0].Verdict.Code != tc.code {
			t.Fatalf("unavailable: %+v", p)
		}
	}
}

func TestDynamicBroadPlanKeepsUnansweredNeighbor(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	dyn.PrependReactor("list", "widgets", func(a ktesting.Action) (bool, runtime.Object, error) {
		switch a.GetNamespace() {
		case "":
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("denied"))
		case "b":
			return true, nil, errors.New("client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline")
		}
		return false, nil, nil
	})
	core, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, NamespaceFallbacks: []string{"a", "b"}, ProbeRetryBackoff: []time.Duration{time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.DynamicResourceCache{DynamicResourceCache: core}
	if err := cache.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !cache.IsNamespaceSynced(gvr, "a") {
		if time.Now().After(deadline) {
			t.Fatal("a did not sync")
		}
		time.Sleep(time.Millisecond)
	}
	before := len(dyn.Actions())
	p := Dynamic(cache, gvr, nil, false)
	if !p.CollectionLimited || len(p.Partitions) != 2 {
		t.Fatalf("%+v", p)
	}
	for _, part := range p.Partitions {
		switch part.Verdict.State {
		case Readable:
			if !reflect.DeepEqual(part.Namespaces, []string{"a"}) {
				t.Fatalf("read: %+v", part)
			}
		case Gap:
			if part.Verdict.Code != readreason.KindSyncPending || !reflect.DeepEqual(part.Namespaces, []string{"b"}) {
				t.Fatalf("gap: %+v", part)
			}
		default:
			t.Fatalf("unanswered neighbor narrowed away: %+v", part)
		}
	}
	if len(dyn.Actions()) != before {
		t.Fatal("planning performed I/O")
	}
}

type countingObserver struct{ calls int }

func (o *countingObserver) NamespaceObservation(_ schema.GroupVersionResource, ns string) k8score.DynamicResourceObservation {
	if ns == "" {
		return k8score.DynamicResourceObservation{State: k8score.DynamicObservationUnwatched}
	}
	o.calls++
	if o.calls == 1 {
		return k8score.DynamicResourceObservation{State: k8score.DynamicObservationSyncing}
	}
	return k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced}
}

func TestDynamicObservesEachTargetOnce(t *testing.T) {
	observer := &countingObserver{}
	p := Dynamic(observer, schema.GroupVersionResource{}, []string{"a", "a"}, false)
	if observer.calls != 1 || len(p.Partitions) != 1 || p.Partitions[0].Verdict.Code != readreason.KindSyncPending {
		t.Fatalf("duplicate target observed %d times: %+v", observer.calls, p)
	}
}

type wholeRetryObserver struct{ retry bool }

func (o wholeRetryObserver) NamespaceObservation(_ schema.GroupVersionResource, ns string) k8score.DynamicResourceObservation {
	result := k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeExplicitNamespaces, Namespaces: []string{"read-a"}}
	if ns == "" {
		result.ProbeRetrying = o.retry
		if !o.retry {
			result.ReasonCode = "scope_probe_incomplete"
		}
	}
	return result
}
func TestDynamicWholeProbeKeepsUnknownRemainder(t *testing.T) {
	for _, retry := range []bool{true, false} {
		observer := wholeRetryObserver{retry: retry}
		plan := Dynamic(observer, schema.GroupVersionResource{}, nil, false)
		code := readreason.ReadError
		if retry {
			code = readreason.KindSyncPending
		}
		if len(plan.Partitions) != 2 || plan.Partitions[0].Verdict.State != Readable || plan.Partitions[1].Extent != UnknownNamespaces || plan.Partitions[1].Verdict.Code != code {
			t.Fatalf("retry=%v: %+v", retry, plan)
		}
		explicit := Dynamic(observer, schema.GroupVersionResource{}, []string{"read-a"}, false)
		if len(explicit.Partitions) != 1 || explicit.Partitions[0].Verdict.State != Readable {
			t.Fatalf("explicit: %+v", explicit)
		}
	}
}
