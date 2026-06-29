package k8s

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// fakeDyn builds a dynamic.Interface whose list calls are gated by `allow`.
//
// allow is a predicate over (gvr, namespace) that returns true when list
// should succeed (returns an empty UnstructuredList) and false when it
// should be denied (returns a 403 Forbidden, mirroring real apiserver
// behavior under denied RBAC). Empty namespace means cluster-wide list.
func fakeDyn(t *testing.T, allow func(gvr schema.GroupVersionResource, namespace string) bool) dynamic.Interface {
	t.Helper()

	scheme := runtime.NewScheme()
	// Pre-register the list kinds for every probe target so the fake
	// client knows how to construct an empty list result. Without this it
	// returns an error that's neither Forbidden nor NotFound and the
	// probe treats it as transient — not what we want for these tests.
	gvrToListKind := map[schema.GroupVersionResource]string{}
	perms := &ResourcePermissions{}
	for _, p := range resourceProbeTargets(perms) {
		// Use a synthetic singular Kind name; the probe doesn't decode the
		// list body, only the error.
		gvrToListKind[p.gvr] = p.gvr.Resource + "List"
	}

	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
	client.PrependReactor("list", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		la, ok := action.(clienttesting.ListAction)
		if !ok {
			return false, nil, nil
		}
		gvr := la.GetResource()
		ns := la.GetNamespace()
		if allow(gvr, ns) {
			return false, nil, nil // fall through to the default reactor (empty list)
		}
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource},
			"",
			nil,
		)
	})
	return client
}

// scopeOf is a tiny helper for assertions: returns the scope keyed by k.
func scopeOf(r *PermissionCheckResult, k string) k8score.ResourceScope {
	if r == nil {
		return k8score.ResourceScope{}
	}
	return r.Scopes[k]
}

func TestProbeResourceAccess_ClusterWideUser(t *testing.T) {
	dyn := fakeDyn(t, func(_ schema.GroupVersionResource, _ string) bool { return true })

	result, hadErrors := probeResourceAccess(context.Background(), dyn, nil, false)

	if hadErrors {
		t.Fatalf("hadErrors should be false on a clean run")
	}
	if result.NamespaceScoped {
		t.Errorf("NamespaceScoped should be false for a cluster-wide user")
	}
	if result.Namespace != "" {
		t.Errorf("Namespace should be empty when no fallback ns is set")
	}
	for k, scope := range result.Scopes {
		if !scope.Enabled {
			t.Errorf("kind %s should be enabled", k)
		}
		if scope.Namespace != "" {
			t.Errorf("kind %s should be cluster-wide, got ns=%q", k, scope.Namespace)
		}
	}
	if !result.Perms.Pods || !result.Perms.Deployments {
		t.Errorf("legacy bool view should mark Pods/Deployments true")
	}
}

func TestProbeResourceAccess_NamespaceOnlyUser(t *testing.T) {
	const ns = "dev-ns-1"

	// User has access to nothing cluster-wide; only the fallback namespace.
	dyn := fakeDyn(t, func(_ schema.GroupVersionResource, namespace string) bool {
		return namespace == ns
	})

	result, _ := probeResourceAccess(context.Background(), dyn, []string{ns}, false)

	if !result.NamespaceScoped {
		t.Fatalf("NamespaceScoped should be true when namespaced fallback succeeded")
	}
	if result.Namespace != ns {
		t.Errorf("Namespace should be %q, got %q", ns, result.Namespace)
	}
	// All namespaceable kinds should be scoped to ns.
	if got := scopeOf(result, k8score.Pods); got != (k8score.ResourceScope{Enabled: true, Namespace: ns}) {
		t.Errorf("Pods scope = %+v, want enabled+%q", got, ns)
	}
	if got := scopeOf(result, k8score.Deployments); got != (k8score.ResourceScope{Enabled: true, Namespace: ns}) {
		t.Errorf("Deployments scope = %+v, want enabled+%q", got, ns)
	}
	// Cluster-scoped kinds must remain disabled — no namespace fallback exists.
	if scopeOf(result, k8score.Nodes).Enabled {
		t.Errorf("Nodes should remain disabled when only namespaced perms granted")
	}
	if scopeOf(result, k8score.Namespaces).Enabled {
		t.Errorf("Namespaces should remain disabled when only namespaced perms granted")
	}
	if scopeOf(result, k8score.PersistentVolumes).Enabled {
		t.Errorf("PersistentVolumes should remain disabled (cluster-scoped) when only namespaced perms granted")
	}
}

func TestPickPrimaryNs(t *testing.T) {
	scope := func(ns string) k8score.ResourceScope {
		return k8score.ResourceScope{Enabled: true, Namespace: ns}
	}
	disabled := k8score.ResourceScope{}

	cases := []struct {
		name       string
		candidates []string
		scopes     map[string]k8score.ResourceScope
		want       string
	}{
		{
			name:       "no grants falls back to first candidate",
			candidates: []string{"default", "team-a"},
			scopes:     map[string]k8score.ResourceScope{k8score.Pods: disabled},
			want:       "default",
		},
		{
			name:       "first candidate granted",
			candidates: []string{"default", "team-a"},
			scopes:     map[string]k8score.ResourceScope{k8score.Pods: scope("default")},
			want:       "default",
		},
		{
			name:       "later candidate granted, primary follows the actual grant",
			candidates: []string{"default", "team-a"},
			scopes:     map[string]k8score.ResourceScope{k8score.Secrets: scope("team-a")},
			want:       "team-a",
		},
		{
			name:       "grants in multiple candidates, earliest in priority wins",
			candidates: []string{"default", "team-a", "team-b"},
			scopes: map[string]k8score.ResourceScope{
				k8score.Secrets:    scope("team-b"),
				k8score.ConfigMaps: scope("team-a"),
			},
			want: "team-a",
		},
		{
			name:       "no candidates returns empty",
			candidates: nil,
			scopes:     map[string]k8score.ResourceScope{k8score.Pods: scope("team-a")},
			want:       "",
		},
		{
			name:       "cluster-wide grant ignored (empty ns doesn't count as a grant location)",
			candidates: []string{"default"},
			scopes:     map[string]k8score.ResourceScope{k8score.Pods: scope("")},
			want:       "default",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickPrimaryNs(tc.candidates, tc.scopes); got != tc.want {
				t.Errorf("pickPrimaryNs(%v, …) = %q, want %q", tc.candidates, got, tc.want)
			}
		})
	}
}

// genNamespaces produces N distinct namespace names — used to drive
// mergeScopeCandidates past the cap, which a live-client test can't do
// without a fake clientset.
func genNamespaces(prefix string, count int) []string {
	out := make([]string, count)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%03d", prefix, i)
	}
	return out
}

func TestMergeScopeCandidates_NonAuthoritativeIgnoresAccessible(t *testing.T) {
	out, dropped := mergeScopeCandidates("dev", "prod", []string{"alpha", "beta"}, false)
	want := []string{"dev", "prod"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("out = %v, want %v (accessible must be ignored when non-authoritative)", out, want)
	}
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0 (no truncation reported on non-authoritative path)", dropped)
	}
}

// Regression: the previous implementation `break`-ed out of the accessible
// loop on atCap, so dropped never reached the log threshold and operators
// got no truncation breadcrumb in the very case the log was added for.
func TestMergeScopeCandidates_CountsAllDrops(t *testing.T) {
	// 25 accessible namespaces, no overlap with context/flag — pure cap test.
	out, dropped := mergeScopeCandidates("", "", genNamespaces("ns", 25), true)
	if len(out) != MaxScopeCandidates {
		t.Errorf("len(out) = %d, want %d", len(out), MaxScopeCandidates)
	}
	if dropped != 5 {
		t.Errorf("dropped = %d, want 5 (25 accessible - 20 cap)", dropped)
	}
}

// Drops must still count when context+flag occupy the first slots — the
// case the second-round review flagged where the original predicate
// missed truncation.
func TestMergeScopeCandidates_CountsDropsWithContextAndFlag(t *testing.T) {
	out, dropped := mergeScopeCandidates("dev", "prod", genNamespaces("ns", 25), true)
	if len(out) != MaxScopeCandidates {
		t.Errorf("len(out) = %d, want %d", len(out), MaxScopeCandidates)
	}
	if out[0] != "dev" || out[1] != "prod" {
		t.Errorf("out[0..1] = %v, want [dev prod]", out[:2])
	}
	// 25 accessible, 18 of them fit (after dev+prod take 2 slots), 7 drop.
	if dropped != 7 {
		t.Errorf("dropped = %d, want 7 (25 accessible - 18 remaining cap slots)", dropped)
	}
}

// A user with secret-list permission in a namespace that isn't the
// kubeconfig context namespace should still get the Secret informer wired
// — the probe must walk every candidate, not just the first one.
func TestProbeResourceAccess_FallbackCandidatesBeyondFirst(t *testing.T) {
	const accessibleNs = "team-a"
	dyn := fakeDyn(t, func(gvr schema.GroupVersionResource, namespace string) bool {
		// User has secret-list in team-a only — nothing cluster-wide, nothing
		// in `default` (which would be the kubeconfig fallback).
		return gvr.Group == "" && gvr.Resource == "secrets" && namespace == accessibleNs
	})

	// `default` is first in the candidate list (typical kubeconfig context),
	// team-a follows. Pre-fix the probe stopped at `default` and disabled
	// Secrets — now it walks to team-a.
	result, _ := probeResourceAccess(context.Background(), dyn, []string{"default", accessibleNs}, false)

	if got := scopeOf(result, k8score.Secrets); got != (k8score.ResourceScope{Enabled: true, Namespace: accessibleNs}) {
		t.Errorf("Secrets scope = %+v, want enabled+%q (fallback must walk past first candidate)", got, accessibleNs)
	}
	// result.Namespace must follow the actual grant — otherwise the dynamic
	// CRD cache (which reads it as NamespaceFallback) would pin CRD informers
	// to `default` where the user has nothing.
	if result.Namespace != accessibleNs {
		t.Errorf("result.Namespace = %q, want %q so CRD informer fallback lands where the user has reads", result.Namespace, accessibleNs)
	}
}

// TestProbeResourceAccess_MixedScope verifies that each kind is probed
// independently: a kind with cluster-wide read access (e.g. Events) must
// not suppress the namespace-scoped retry for other kinds in the same run.
func TestProbeResourceAccess_MixedScope(t *testing.T) {
	const ns = "dev-ns-1"

	dyn := fakeDyn(t, func(gvr schema.GroupVersionResource, namespace string) bool {
		// Events: cluster-wide allowed.
		if gvr.Group == "" && gvr.Resource == "events" {
			return true
		}
		// Everything else: only allowed in fallback namespace.
		return namespace == ns
	})

	result, _ := probeResourceAccess(context.Background(), dyn, []string{ns}, false)

	if !result.NamespaceScoped {
		t.Fatalf("NamespaceScoped should be true (some kinds ended up ns-scoped)")
	}
	// Events: cluster-wide
	if got := scopeOf(result, k8score.Events); got != (k8score.ResourceScope{Enabled: true, Namespace: ""}) {
		t.Errorf("Events scope = %+v, want cluster-wide", got)
	}
	// Pods, Deployments, Services etc.: namespace-scoped
	for _, k := range []string{k8score.Pods, k8score.Deployments, k8score.Services, k8score.ConfigMaps} {
		if got := scopeOf(result, k); got != (k8score.ResourceScope{Enabled: true, Namespace: ns}) {
			t.Errorf("%s scope = %+v, want enabled+%q", k, got, ns)
		}
	}
	// Cluster-scoped kinds remain off.
	if scopeOf(result, k8score.Nodes).Enabled {
		t.Errorf("Nodes should remain disabled")
	}
}

func TestProbeResourceAccess_AllDenied(t *testing.T) {
	dyn := fakeDyn(t, func(_ schema.GroupVersionResource, _ string) bool { return false })

	// No fallback namespace — nothing can succeed.
	result, _ := probeResourceAccess(context.Background(), dyn, nil, false)

	if result.NamespaceScoped {
		t.Errorf("NamespaceScoped should be false when nothing succeeded")
	}
	for k, scope := range result.Scopes {
		if scope.Enabled {
			t.Errorf("kind %s should be disabled, got enabled", k)
		}
	}
	if result.Perms.Pods {
		t.Errorf("legacy bool view should mark Pods false")
	}
}

func TestProbeResourceAccess_TransientErrorTreatedAsAllow(t *testing.T) {
	// A non-auth error (network, 503, NotFound for missing CRD) must NOT
	// gate the informer — we want the reflector to retry rather than
	// permanently disable the resource for the session.
	transient := apierrors.NewServerTimeout(schema.GroupResource{Resource: "pods"}, "list", 1)

	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{}
	perms := &ResourcePermissions{}
	for _, p := range resourceProbeTargets(perms) {
		gvrToListKind[p.gvr] = p.gvr.Resource + "List"
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
	dyn.PrependReactor("list", "pods", func(_ clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, transient
	})

	result, hadErrors := probeResourceAccess(context.Background(), dyn, nil, false)

	if !hadErrors {
		t.Errorf("hadErrors should be true when a probe hit a transient error")
	}
	if got := scopeOf(result, k8score.Pods); !got.Enabled || got.Namespace != "" {
		t.Errorf("Pods scope = %+v, want optimistically enabled cluster-wide despite transient error", got)
	}
}

// TestProbeResourceAccess_ForceNamespaceClusterWideUser verifies the
// in-app namespace switcher behavior. A user with cluster-wide read who
// explicitly picks a namespace should end up with namespace-scoped
// informers for namespaced kinds — without this, the cache would still
// be cluster-wide and the picker would silently do nothing.
//
// Cluster-only kinds (nodes, namespaces, PV, storageclasses) must stay
// enabled cluster-wide so the dashboard / Resources view don't lose
// visibility of resources that have no namespace dimension.
func TestProbeResourceAccess_ForceNamespaceClusterWideUser(t *testing.T) {
	const ns = "dev-ns-1"

	// User has cluster-wide list everywhere. forceNamespace=true should
	// pin namespaced kinds to ns and keep cluster-only kinds cluster-wide.
	dyn := fakeDyn(t, func(_ schema.GroupVersionResource, _ string) bool { return true })

	result, _ := probeResourceAccess(context.Background(), dyn, []string{ns}, true)

	if !result.NamespaceScoped {
		t.Fatalf("NamespaceScoped should be true in forced-namespace mode")
	}
	if result.Namespace != ns {
		t.Errorf("Namespace = %q, want %q", result.Namespace, ns)
	}
	for _, k := range []string{k8score.Pods, k8score.Deployments, k8score.Services} {
		if got := scopeOf(result, k); got != (k8score.ResourceScope{Enabled: true, Namespace: ns}) {
			t.Errorf("%s scope = %+v, want enabled+%q (cluster-wide ignored under forceNamespace)", k, got, ns)
		}
	}
	for _, k := range []string{k8score.Nodes, k8score.Namespaces, k8score.PersistentVolumes, k8score.StorageClasses} {
		if got := scopeOf(result, k); got != (k8score.ResourceScope{Enabled: true, Namespace: ""}) {
			t.Errorf("%s scope = %+v, want enabled cluster-wide under forceNamespace (cluster-only kind)", k, got)
		}
	}
}

// When a cluster-admin scopes to a namespace, denying Node list cluster-wide
// (e.g. a tenant operator that grants ns-only RBAC on top of a service
// account that lacks Node read) must still cleanly disable Nodes. Other
// cluster-only kinds the user can list stay enabled cluster-wide.
func TestProbeResourceAccess_ForceNamespaceClusterOnlyMixed(t *testing.T) {
	const ns = "dev-ns-1"

	dyn := fakeDyn(t, func(gvr schema.GroupVersionResource, namespace string) bool {
		// Deny Node cluster-wide; allow everything else.
		if gvr.Group == "" && gvr.Resource == "nodes" && namespace == "" {
			return false
		}
		return true
	})

	result, _ := probeResourceAccess(context.Background(), dyn, []string{ns}, true)

	if scopeOf(result, k8score.Nodes).Enabled {
		t.Errorf("Nodes should be disabled when cluster-wide Node list is forbidden")
	}
	if got := scopeOf(result, k8score.Namespaces); got != (k8score.ResourceScope{Enabled: true, Namespace: ""}) {
		t.Errorf("Namespaces scope = %+v, want enabled cluster-wide", got)
	}
	if got := scopeOf(result, k8score.Pods); got != (k8score.ResourceScope{Enabled: true, Namespace: ns}) {
		t.Errorf("Pods scope = %+v, want enabled+%q", got, ns)
	}
}

// In forced-namespace mode the user's intent is to be ns-scoped even when
// every probe fails. NamespaceScoped must stay true so the dynamic cache
// (which reads it) doesn't silently fall back to cluster-wide watches.
func TestProbeResourceAccess_ForceNamespaceAllDeniedKeepsScoped(t *testing.T) {
	const ns = "dev-ns-1"
	dyn := fakeDyn(t, func(_ schema.GroupVersionResource, _ string) bool { return false })

	result, _ := probeResourceAccess(context.Background(), dyn, []string{ns}, true)

	if !result.NamespaceScoped {
		t.Errorf("NamespaceScoped should remain true under forceNamespace even when every probe failed")
	}
	if result.Namespace != ns {
		t.Errorf("Namespace = %q, want %q", result.Namespace, ns)
	}
}

// Cluster-scoped kinds (nodes, namespaces, PV, storageclasses) must NEVER
// fall back to a namespace probe — that probe would 404 since the resource
// doesn't live in any namespace. Verify the probe loop respects clusterOnly.
func TestProbeResourceAccess_ClusterOnlyKindsNoNsFallback(t *testing.T) {
	const ns = "dev-ns-1"

	// Track every list call so we can assert no ns-scoped probe was made for
	// cluster-scoped kinds.
	var nsProbedClusterOnly []schema.GroupVersionResource
	dyn := fakeDyn(t, func(gvr schema.GroupVersionResource, namespace string) bool {
		if namespace != "" {
			// Record any namespaced probe against a cluster-scoped GVR.
			isClusterOnly := false
			for _, p := range resourceProbeTargets(&ResourcePermissions{}) {
				if p.gvr == gvr && p.clusterOnly {
					isClusterOnly = true
					break
				}
			}
			if isClusterOnly {
				nsProbedClusterOnly = append(nsProbedClusterOnly, gvr)
			}
		}
		// Deny everything cluster-wide so cluster-only kinds want to retry —
		// the test asserts the retry doesn't happen.
		return namespace == ns
	})

	_, _ = probeResourceAccess(context.Background(), dyn, []string{ns}, false)

	if len(nsProbedClusterOnly) > 0 {
		t.Errorf("cluster-scoped kinds were probed namespace-scoped (would 404 in real cluster): %v", nsProbedClusterOnly)
	}
}
