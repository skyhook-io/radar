package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/k8score"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func resourcePermissionError(checked bool, verb, group, resource, namespace string) error {
	scope := "at cluster scope"
	if namespace != "" {
		scope = fmt.Sprintf("in namespace %q", namespace)
	}
	if !checked {
		return fmt.Errorf("permission_check_failed: could not verify whether your role can %s resource %q in API group %q %s; access was not granted, retry when the permission check is available", verb, resource, group, scope)
	}
	return fmt.Errorf("forbidden: your role cannot %s resource %q in API group %q %s; ask a cluster administrator to grant access", verb, resource, group, scope)
}

func resourceReadNamespaces(ctx context.Context, requested []string, verb, kind, group string) ([]string, error) {
	clamped, ok := clampToNamespacePin(requested)
	if !ok {
		return nil, fmt.Errorf("%s: requested namespace %q is excluded by Radar's --namespace-scope selection", ReasonOutsideNamespaceScope, namespaceForError(requested))
	}
	if gvr, builtin := k8s.BuiltinGVRAnyGroup(kind); builtin && (group == "" || group == gvr.Group) {
		group = gvr.Group
	}
	user, perms, checked := resolveUserPermsDecision(ctx)
	if !checked {
		return nil, resourcePermissionError(false, verb, group, kind, namespaceForError(requested))
	}
	allowed := pkgauth.FilterNamespacesForUser(clamped, user, perms)
	if allowed != nil && len(allowed) == 0 {
		return nil, fmt.Errorf("%w; no namespace access for the requested scope", resourcePermissionError(true, verb, group, kind, namespaceForError(requested)))
	}
	return allowed, nil
}

func namespaceForError(namespaces []string) string {
	if len(namespaces) == 1 {
		return namespaces[0]
	}
	return ""
}

func resourceReadCache(kind, group string, namespaces []string) (*k8s.ResourceCache, error) {
	gvr, builtin := k8s.BuiltinGVRAnyGroup(kind)
	typed := builtin && (group == "" || group == gvr.Group) && k8s.TypedKindOwnsGroup(kind, gvr.Group)
	if !typed {
		cache := k8s.GetResourceCache()
		if cache == nil {
			return nil, errNotConnected()
		}
		return cache, nil
	}
	cache, readiness := k8s.ReadableCacheForKind(gvr.Resource)
	if cache == nil {
		return nil, errNotConnected()
	}
	switch readiness {
	case k8s.KindPending:
		return nil, fmt.Errorf("kind_sync_pending: %s are still loading, please retry shortly", gvr.Resource)
	case k8s.KindFailed:
		return nil, fmt.Errorf("kind_sync_failed: %s failed to load within the sync deadline; check Radar's collector connection and list/watch permissions", gvr.Resource)
	case k8s.KindUnavailable:
		return nil, fmt.Errorf("kind_not_watched: Radar is not watching resource %q in API group %q; check Radar's collector list/watch permissions and collection configuration", gvr.Resource, gvr.Group)
	}
	if len(namespaces) == 0 && len(cache.KindNamespaces(gvr.Resource)) > 0 {
		return nil, fmt.Errorf("kind_not_watched: Radar's %s cache covers only selected namespaces; specify a namespace within the collector's scope", gvr.Resource)
	}
	for _, ns := range namespaces {
		if !cache.KindCoversNamespace(gvr.Resource, ns) {
			return nil, fmt.Errorf("kind_not_watched: Radar does not watch %s in namespace %q; this namespace is outside the collector's scope", gvr.Resource, ns)
		}
	}
	return cache, nil
}

// Dynamic reads start informers on demand. Check their scope after the read so
// a cold or incomplete store cannot establish absence. Once sync is proven,
// callers re-read: it may have completed after the first store read. Direct API
// reads need no informer and remain authoritative without one.
func checkDynamicResourceRead(kind, group, namespace, verb string, readErr error) (bool, error) {
	if errors.Is(readErr, k8s.ErrUnknownDynamicKind) {
		return false, fmt.Errorf("unknown_kind: %w", readErr)
	}
	if errors.Is(readErr, k8s.ErrDynamicNotReady) {
		return false, fmt.Errorf("kind_not_watched: %w", readErr)
	}
	discovery := k8s.GetResourceDiscovery()
	dynamicCache := k8s.GetDynamicResourceCache()
	var gvr schema.GroupVersionResource
	var ok bool
	if discovery != nil {
		if group != "" {
			gvr, ok = discovery.GetGVRWithGroup(kind, group)
		} else {
			gvr, ok = discovery.GetGVR(kind)
		}
	}
	if !ok {
		gvr, ok = k8s.BuiltinGVRAnyGroup(kind)
		ok = ok && (group == "" || group == gvr.Group)
	}
	direct := ok && ((gvr.Group == "" && gvr.Resource == "endpoints") ||
		(gvr.Group == "discovery.k8s.io" && gvr.Resource == "endpointslices") ||
		(gvr.Group == "coordination.k8s.io" && gvr.Resource == "leases") ||
		(verb == "get" && ((gvr.Group == "apiextensions.k8s.io" && gvr.Resource == "customresourcedefinitions") ||
			(gvr.Group == "apiregistration.k8s.io" && gvr.Resource == "apiservices"))))
	if apierrors.IsForbidden(readErr) || apierrors.IsUnauthorized(readErr) {
		collector := "Radar's service account"
		if !k8s.IsInCluster() {
			collector = "Radar's kubeconfig identity"
		}
		action := "watch"
		if direct {
			action = verb
		}
		if ok {
			group = gvr.Group
			kind = gvr.Resource
		}
		return false, fmt.Errorf("collector_forbidden: %s can't %s %s in API group %q (namespace %q): %w", collector, action, kind, group, namespace, readErr)
	}
	if !ok || dynamicCache == nil || direct {
		return false, readErr
	}
	observation := dynamicCache.Observation(gvr)
	switch observation.State {
	case k8score.DynamicObservationSyncing:
		if !dynamicCache.IsNamespaceSynced(gvr, namespace) {
			if observation.ReasonCode == "sync_stalled" {
				return false, fmt.Errorf("kind_sync_failed: %s initial cache sync has stalled; check Radar's collector connection and list/watch permissions", kind)
			}
			return false, fmt.Errorf("kind_sync_pending: %s are still loading, please retry shortly", kind)
		}
	case k8score.DynamicObservationSynced:
		if namespace != "" && !dynamicCache.IsNamespaceSynced(gvr, namespace) {
			return false, fmt.Errorf("kind_not_watched: Radar does not watch %s in namespace %q", kind, namespace)
		}
		if namespace == "" && (observation.Truncated || observation.Scope == k8score.DynamicObservationScopeExplicitNamespaces) {
			return false, fmt.Errorf("kind_not_watched: Radar's %s cache covers only selected namespaces; specify a namespace within the collector's scope", kind)
		}
	case k8score.DynamicObservationUnsupported:
		return false, fmt.Errorf("kind_not_watched: %s does not support list/watch", kind)
	}
	return dynamicCache.IsNamespaceSynced(gvr, namespace), readErr
}

func resourceSecretNamespaces(ctx context.Context, allowed []string, verb string) ([]string, error) {
	if allowed == nil {
		granted, checked := canReadInNamespaceDecision(ctx, "", "secrets", "", verb)
		if !granted {
			return nil, resourcePermissionError(checked, verb, "", "secrets", "")
		}
		return nil, nil
	}
	namespaces, checked := filterNamespacesByCanReadDecision(ctx, "", "secrets", verb, allowed)
	if !checked {
		return nil, resourcePermissionError(false, verb, "", "secrets", namespaceForError(allowed))
	}
	if len(namespaces) > 0 {
		return namespaces, nil
	}
	return nil, resourcePermissionError(true, verb, "", "secrets", namespaceForError(allowed))
}

func resourceKindName(kind, group string) string {
	if gvr, ok := k8s.BuiltinGVRAnyGroup(kind); ok && (group == "" || group == gvr.Group) {
		return gvr.Resource
	}
	return kind
}

func resourceGetError(ctx context.Context, err error, kind, namespace, name string) error {
	if apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound) {
		return notFoundError(ctx, err, kind, namespace, name)
	}
	return fmt.Errorf("failed to get %s: %w", kind, err)
}

func fetchMCPDynamicResource(ctx context.Context, cache *k8s.ResourceCache, kind, group, namespace, name string) (*unstructured.Unstructured, error) {
	obj, err := cache.GetDynamicWithGroup(ctx, kind, namespace, name, group)
	cachedReady, err := checkDynamicResourceRead(kind, group, namespace, "get", err)
	if cachedReady && (err == nil || apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound)) {
		return cache.GetDynamicWithGroup(ctx, kind, namespace, name, group)
	}
	return obj, err
}
