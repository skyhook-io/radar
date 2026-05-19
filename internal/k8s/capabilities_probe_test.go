package k8s

import (
	"context"
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
