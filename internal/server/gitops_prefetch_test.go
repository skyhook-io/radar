package server

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
)

const testLastApplied = `{"kind":"Deployment","spec":{"replicas":1}}`

// prefetchTestEnv wires the dynamic test cache with one live Deployment
// carrying last-applied, and returns the fake dynamic client so tests can
// count the direct GETs prefetchLive issued. The typed cache is TestMain's
// shared package fixture — do NOT re-init or reset it here; that would tear
// down the fixture every later test in the package depends on.
func prefetchTestEnv(t *testing.T) *dynamicfake.FakeDynamicClient {
	t.Helper()
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	liveDep := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":      "billing",
			"namespace": "prod",
			"annotations": map[string]any{
				"kubectl.kubernetes.io/last-applied-configuration": testLastApplied,
			},
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "DeploymentList"},
		liveDep,
	)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true,
	}}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	return dyn
}

// resolverCache returns the package fixture's typed cache; the resolver only
// needs it non-nil (preserve-last-applied GETs go through the dynamic
// singleton wired above).
func resolverCache() *k8s.ResourceCache {
	return k8s.GetResourceCache()
}

func countGets(dyn *dynamicfake.FakeDynamicClient) int {
	n := 0
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "get" {
			n++
		}
	}
	return n
}

func outOfSyncRow() gitopsinsights.ManagedResourceRow {
	return gitopsinsights.ManagedResourceRow{
		Ref:  gitopsinsights.Ref{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"},
		Sync: "OutOfSync",
	}
}

func TestPrefetchLiveFetchesOutOfSyncRow(t *testing.T) {
	dyn := prefetchTestEnv(t)
	r := newInsightsResolver(context.Background(), resolverCache(), nil, nil)
	r.prefetchLive([]gitopsinsights.ManagedResourceRow{outOfSyncRow()}, "")

	if got := countGets(dyn); got != 1 {
		t.Fatalf("prefetch issued %d GETs, want 1", got)
	}
	live := r.GetLive("apps", "Deployment", "prod", "billing")
	if live == nil {
		t.Fatal("GetLive returned nil for prefetched ref")
	}
	if live.GetAnnotations()["kubectl.kubernetes.io/last-applied-configuration"] != testLastApplied {
		t.Fatalf("prefetched object lost last-applied: %v", live.GetAnnotations())
	}
	// Map-only mode: a second GetLive must not issue another GET.
	_ = r.GetLive("apps", "Deployment", "prod", "billing")
	if got := countGets(dyn); got != 1 {
		t.Fatalf("GetLive after prefetch issued extra GETs (total %d, want 1)", got)
	}
}

// While a sync operation runs, the UI polls insights every 2s purely to
// track progress — the poll must be a cache read, not an apiserver fan-out.
func TestPrefetchLiveSkipsAllFetchesWhileOperationRuns(t *testing.T) {
	dyn := prefetchTestEnv(t)
	for _, phase := range []string{"Running", "Terminating"} {
		r := newInsightsResolver(context.Background(), resolverCache(), nil, nil)
		r.prefetchLive([]gitopsinsights.ManagedResourceRow{outOfSyncRow()}, phase)
		if got := countGets(dyn); got != 0 {
			t.Fatalf("phase %s: prefetch issued %d GETs, want 0", phase, got)
		}
		if live := r.GetLive("apps", "Deployment", "prod", "billing"); live != nil {
			t.Fatalf("phase %s: GetLive fell back to a direct fetch", phase)
		}
	}
}

// Synced rows without a sync error carry no drift signal worth a round-trip
// — Argo's own normalization suppresses what a last-applied diff would show.
func TestPrefetchLiveSkipsCleanlySyncedRows(t *testing.T) {
	dyn := prefetchTestEnv(t)
	r := newInsightsResolver(context.Background(), resolverCache(), nil, nil)
	r.prefetchLive([]gitopsinsights.ManagedResourceRow{{
		Ref:  gitopsinsights.Ref{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"},
		Sync: "Synced",
	}}, "")
	if got := countGets(dyn); got != 0 {
		t.Fatalf("prefetch issued %d GETs for a Synced row, want 0", got)
	}

	// A Synced row that recorded a sync error is still worth the fetch.
	r2 := newInsightsResolver(context.Background(), resolverCache(), nil, nil)
	r2.prefetchLive([]gitopsinsights.ManagedResourceRow{{
		Ref:          gitopsinsights.Ref{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"},
		Sync:         "Synced",
		HasSyncError: true,
	}}, "")
	if got := countGets(dyn); got != 1 {
		t.Fatalf("prefetch issued %d GETs for a Synced row with sync error, want 1", got)
	}
}

// RBAC gates run before any fetch is scheduled: parallelism must never widen
// what a user can read, and a denied ref must not cost a round-trip.
func TestPrefetchLiveHonorsRBACGatesBeforeFetching(t *testing.T) {
	dyn := prefetchTestEnv(t)

	denyAll := func(group, kind, namespace, name string) bool { return false }
	r := newInsightsResolver(context.Background(), resolverCache(), nil, denyAll)
	r.prefetchLive([]gitopsinsights.ManagedResourceRow{outOfSyncRow()}, "")
	if got := countGets(dyn); got != 0 {
		t.Fatalf("prefetch issued %d GETs for access-denied ref, want 0", got)
	}
	if live := r.GetLive("apps", "Deployment", "prod", "billing"); live != nil {
		t.Fatal("GetLive returned an object the access gate denied")
	}

	// Namespace allowlist gate: ref outside the allowed set is not fetched.
	r2 := newInsightsResolver(context.Background(), resolverCache(), []string{"other"}, nil)
	r2.prefetchLive([]gitopsinsights.ManagedResourceRow{outOfSyncRow()}, "")
	if got := countGets(dyn); got != 0 {
		t.Fatalf("prefetch issued %d GETs for ref outside namespace allowlist, want 0", got)
	}
}

func TestManagedResourceRowsAndOperationPhase(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "a", "namespace": "argocd"},
		"status": map[string]any{
			"operationState": map[string]any{"phase": "Running"},
			"resources": []any{
				map[string]any{"group": "apps", "kind": "Deployment", "namespace": "prod", "name": "billing", "status": "OutOfSync"},
				map[string]any{"kind": "Service", "namespace": "prod", "name": "billing", "status": "Synced",
					"syncResult": map[string]any{"status": "SyncFailed", "message": "webhook denied"}},
				map[string]any{"kind": "", "name": "dropped"},
			},
		},
	}}
	rows := gitopsinsights.ManagedResourceRows(app)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (kindless row dropped): %#v", len(rows), rows)
	}
	if rows[0].Sync != "OutOfSync" || rows[0].HasSyncError {
		t.Fatalf("row 0 = %#v", rows[0])
	}
	if !rows[1].HasSyncError {
		t.Fatalf("row 1 should flag sync error: %#v", rows[1])
	}
	if phase := gitopsinsights.OperationPhase(app); phase != "Running" {
		t.Fatalf("OperationPhase = %q, want Running", phase)
	}
}

// The index must reproduce the old per-resource scan exactly: events are
// matched by involvedObject kind+name within the EVENT's own namespace —
// involvedObject.namespace is deliberately not consulted (controllers can
// leave it empty), and a cluster-scoped ref (namespace "") matches across
// all namespaces.
func TestMatchIndexedEventsNamespaceSemantics(t *testing.T) {
	ev := func(ns, kind, name, involvedNS, reason string) *corev1.Event {
		return &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: ns, Name: reason + "-ev"},
			InvolvedObject: corev1.ObjectReference{Kind: kind, Name: name, Namespace: involvedNS},
			Reason:         reason,
		}
	}
	idx := indexEventsByInvolvedObject([]*corev1.Event{
		ev("prod", "Deployment", "billing", "prod", "InNS"),
		ev("prod", "Deployment", "billing", "", "EmptyInvolvedNS"),
		ev("staging", "Deployment", "billing", "staging", "OtherNS"),
		ev("", "ClusterRole", "reader", "", "ClusterScoped"),
	})

	got := matchIndexedEvents(idx, "apps", "Deployment", "prod", "billing")
	reasons := map[string]bool{}
	for _, e := range got {
		reasons[e.Reason] = true
	}
	if !reasons["InNS"] || !reasons["EmptyInvolvedNS"] {
		t.Fatalf("events in the ref's namespace must match regardless of involvedObject.namespace: %v", reasons)
	}
	if reasons["OtherNS"] {
		t.Fatal("event living in another namespace must not match a namespaced ref")
	}

	if got := matchIndexedEvents(idx, "", "ClusterRole", "", "reader"); len(got) != 1 {
		t.Fatalf("cluster-scoped ref should match across namespaces, got %d", len(got))
	}
}
