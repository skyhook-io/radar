package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/k8score"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func resourcePermissionError(checked bool, verb, group, resource, namespace string) error {
	scope := "at cluster scope"
	if strings.Contains(namespace, ",") {
		scope = fmt.Sprintf("in namespaces %q", namespace)
	} else if namespace != "" {
		scope = fmt.Sprintf("in namespace %q", namespace)
	}
	if !checked {
		return fmt.Errorf("permission_check_failed: could not verify whether your role can %s resource %q in API group %q %s; access was not granted, retry when the permission check is available", verb, resource, group, scope)
	}
	return fmt.Errorf("forbidden: your role cannot %s resource %q in API group %q %s; ask a cluster administrator to grant access", verb, resource, group, scope)
}

func resourceReadNamespaces(ctx context.Context, requested []string) ([]string, error) {
	clamped, ok := clampToNamespacePin(requested)
	if !ok {
		return nil, fmt.Errorf("%s: requested namespace %q is excluded by Radar's --namespace-scope selection", ReasonOutsideNamespaceScope, namespaceForError(requested))
	}
	user, perms, checked := resolveUserPermsDecision(ctx)
	if !checked {
		return nil, fmt.Errorf("permission_check_failed: could not discover your Radar namespace access; retry when the permission check is available")
	}
	allowed := pkgauth.FilterNamespacesForUser(clamped, user, perms)
	if allowed != nil && len(allowed) == 0 {
		scope := "any namespace in the requested scope"
		if len(clamped) == 1 {
			scope = fmt.Sprintf("namespace %q", clamped[0])
		} else if len(clamped) > 0 {
			scope = fmt.Sprintf("namespaces %q", clamped)
		}
		return nil, fmt.Errorf("no_namespace_access: no Radar access to %s (Radar grants namespace access when your role can list pods or deployments there; re-checked every ~2 minutes)", scope)
	}
	return allowed, nil
}

func namespaceForError(namespaces []string) string {
	if len(namespaces) == 1 {
		return namespaces[0]
	}
	if len(namespaces) > 10 {
		return fmt.Sprintf("%s (+%d more)", strings.Join(namespaces[:10], ","), len(namespaces)-10)
	}
	return strings.Join(namespaces, ",")
}

// resourceReadCache returns the cache to read kind from and the namespaces to
// read: requested (nil = every namespace the caller may read) narrowed to the
// typed collector's coverage under namespaceCoverageError's rule. Dynamic
// kinds learn their coverage by reading, so their namespaces pass through.
func resourceReadCache(kind, group string, namespaces []string) (*k8s.ResourceCache, []string, error) {
	gvr, builtin := k8s.BuiltinGVRAnyGroup(kind)
	typed := builtin && (group == "" || group == gvr.Group) && k8s.TypedKindOwnsGroup(kind, gvr.Group)
	if !typed {
		cache := k8s.GetResourceCache()
		if cache == nil {
			return nil, nil, errNotConnected()
		}

		return cache, namespaces, nil
	}
	cache, readiness := k8s.ReadableCacheForKind(gvr.Resource)
	if cache == nil {
		return nil, nil, errNotConnected()
	}
	result := k8s.GetCachedPermissionResult()
	for _, ns := range namespaces {
		if result != nil {
			if probeErr := result.NamespaceProbeErrors[gvr.Resource][ns]; apierrors.IsUnauthorized(probeErr) {
				return nil, nil, collectorUnauthorizedError(gvr.Resource, probeErr)
			}
		}
	}
	switch readiness {
	case k8s.KindPending:
		return nil, nil, fmt.Errorf("kind_sync_pending: %s are still loading, please retry shortly", gvr.Resource)
	case k8s.KindFailed:
		return nil, nil, fmt.Errorf("kind_sync_failed: %s failed to load within the sync deadline; check Radar's collector connection and list/watch permissions", gvr.Resource)
	case k8s.KindUnavailable:
		if result != nil {
			if scope, probed := result.Scopes[gvr.Resource]; probed && !scope.Enabled {
				if apierrors.IsUnauthorized(result.ProbeErrors[gvr.Resource]) {
					return nil, nil, collectorUnauthorizedError(gvr.Resource, result.ProbeErrors[gvr.Resource])
				}
				return nil, nil, fmt.Errorf("collector_forbidden: Radar's service account / kubeconfig identity can't list/watch %s in API group %q", gvr.Resource, gvr.Group)
			}
		}
		return nil, nil, fmt.Errorf("kind_not_watched: Radar is not watching resource %q in API group %q; check Radar's collector list/watch permissions and collection configuration", gvr.Resource, gvr.Group)
	}
	covered := cache.KindNamespaces(gvr.Resource)
	if covered == nil {
		return cache, namespaces, nil
	}
	if namespaces == nil {
		return cache, covered, nil
	}
	var readable, uncovered []string
	for _, ns := range namespaces {
		if cache.KindCoversNamespace(gvr.Resource, ns) {
			readable = append(readable, ns)
		} else {
			uncovered = append(uncovered, ns)
		}
	}
	if err := namespaceCoverageError(gvr.Resource, len(readable), uncovered, covered, nil); err != nil {
		return nil, nil, err
	}
	return cache, readable, nil
}

// namespaceCoverageError is the one rule typed and dynamic reads share when
// Radar's collector covers only some namespaces: answer from the covered
// requested namespaces and omit the rest (the partial view an unrestricted
// caller already gets), but fail when none is covered, so an uncovered scope
// never reads as empty. firstErr, the read error of the first uncovered
// namespace when reads were attempted, is kept when it is the only one or when
// no covered namespace is known to name instead.
func namespaceCoverageError(resource string, read int, uncovered, covered []string, firstErr error) error {
	if read > 0 || len(uncovered) == 0 {
		return nil
	}
	if firstErr != nil && (len(uncovered) == 1 || len(covered) == 0) {
		return firstErr
	}
	return outsideCoverageError(resource, uncovered, covered, nil)
}

func outsideCoverageError(resource string, uncovered, covered []string, cause error) error {
	scope := fmt.Sprintf("namespace %q; this namespace is", uncovered[0])
	if len(uncovered) > 1 {
		scope = fmt.Sprintf("namespaces %q; these namespaces are", namespaceForError(uncovered))
	}
	msg := fmt.Sprintf("kind_not_watched: Radar does not watch %s in %s outside the collector's scope; covered namespaces: %q", resource, scope, covered)
	if cause != nil {
		return fmt.Errorf("%s: %w", msg, cause)
	}
	return errors.New(msg)
}

// Dynamic reads start informers on demand. Check their scope after the read so
// a cold or incomplete store cannot establish absence. The bool reports that
// the scope is now synced, so a read that may have preceded the sync can be
// repeated. Direct API reads need no informer and remain authoritative without
// one.
func checkDynamicResourceRead(kind, group, namespace, verb string, readErr error) (bool, error) {
	if errors.Is(readErr, k8s.ErrUnknownDynamicKind) {
		return false, fmt.Errorf("unknown_kind: %w", readErr)
	}
	if errors.Is(readErr, k8s.ErrDynamicNotReady) {
		if k8s.GetConnectionStatus().State == k8s.StateConnecting {
			return false, fmt.Errorf("kind_sync_pending: %s: %w; cluster connection is still loading, please retry shortly", kind, readErr)
		}
		return false, fmt.Errorf("kind_not_watched: %s: %w", kind, readErr)
	}
	dynamicCache := k8s.GetDynamicResourceCache()
	gvr, ok := dynamicReadGVR(kind, group)
	direct := ok && (k8s.ShouldBypassDynamicInformer(gvr) ||
		(verb == "get" && ((gvr.Group == "apiextensions.k8s.io" && gvr.Resource == "customresourcedefinitions") ||
			(gvr.Group == "apiregistration.k8s.io" && gvr.Resource == "apiservices"))))
	if apierrors.IsUnauthorized(readErr) {
		return false, collectorUnauthorizedError(kind, readErr)
	}
	if apierrors.IsForbidden(readErr) {
		if ok && dynamicCache != nil && !direct && namespace != "" {
			observation := dynamicCache.Observation(gvr)
			if observation.Scope == k8score.DynamicObservationScopeExplicitNamespaces && !slices.Contains(observation.Namespaces, namespace) {
				return false, outsideCoverageError(kind, []string{namespace}, observation.Namespaces, readErr)
			}
		}

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
	if readErr != nil && !isObjectNotFound(readErr) {
		return false, fmt.Errorf("%s_error: failed to %s %s: %w", verb, verb, kind, readErr)
	}
	if !ok || dynamicCache == nil || direct {
		return false, readErr
	}
	if readErr == nil || isObjectNotFound(readErr) {
		if !dynamicCache.IsNamespaceSynced(gvr, namespace) {
			_, waitErr := dynamicCache.ListBlocking(gvr, namespace, dynamicSyncWait)
			if waitErr != nil {
				return checkDynamicResourceRead(kind, group, namespace, verb, waitErr)
			}
		}
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
	case k8score.DynamicObservationUnsupported:
		return false, fmt.Errorf("kind_not_watched: %s does not support list/watch", kind)
	}
	return dynamicCache.IsNamespaceSynced(gvr, namespace), readErr
}

const dynamicSyncWait = 3 * time.Second

func discoveredGVR(kind, group string) (schema.GroupVersionResource, bool) {
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		return schema.GroupVersionResource{}, false
	}
	if group != "" {
		return discovery.GetGVRWithGroup(kind, group)
	}
	return discovery.GetGVR(kind)
}

func dynamicReadGVR(kind, group string) (schema.GroupVersionResource, bool) {
	if gvr, ok := discoveredGVR(kind, group); ok {
		return gvr, true
	}
	gvr, ok := k8s.BuiltinGVRAnyGroup(kind)
	return gvr, ok && (group == "" || group == gvr.Group)
}

func dynamicScopeSynced(kind, group, namespace string) bool {
	gvr, ok := dynamicReadGVR(kind, group)
	dynamicCache := k8s.GetDynamicResourceCache()
	return ok && dynamicCache != nil && dynamicCache.IsNamespaceSynced(gvr, namespace)
}

// readDynamicScope performs one dynamic-cache read and checks its scope. A
// read that may have started before the scope's informer finished its initial
// sync can have served a partial store, so it is repeated once sync is proven.
// A scope already synced before and after the read was answered by that read;
// repeating it would only deep-copy the store a second time.
func readDynamicScope[T any](kind, group, namespace, verb string, read func() (T, error)) (T, error) {
	syncedBefore := dynamicScopeSynced(kind, group, namespace)
	out, err := read()
	settled := syncedBefore && dynamicScopeSynced(kind, group, namespace)
	ready, err := checkDynamicResourceRead(kind, group, namespace, verb, err)
	if ready && !settled && (err == nil || isObjectNotFound(err)) {
		out, err = read()
		if err != nil {
			_, err = checkDynamicResourceRead(kind, group, namespace, verb, err)
		}
	}
	return out, err
}

func isObjectNotFound(err error) bool {
	return apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound)
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
	if isObjectNotFound(err) {
		return notFoundError(ctx, err, kind, namespace, name)
	}
	return fmt.Errorf("get_error: failed to get %s %s/%s: %w", kind, namespace, name, err)
}

func fetchMCPDynamicResource(ctx context.Context, cache *k8s.ResourceCache, kind, group, namespace, name string) (*unstructured.Unstructured, error) {
	obj, err := readDynamicScope(kind, group, namespace, "get", func() (*unstructured.Unstructured, error) {
		return cache.GetDynamicWithGroup(ctx, kind, namespace, name, group)
	})
	if isObjectNotFound(err) {
		return obj, resourceGetError(ctx, err, kind, namespace, name)
	}
	return obj, err
}

func collectorUnauthorizedError(kind string, err error) error {
	credentials := "kubeconfig credentials"
	if k8s.IsInCluster() {
		credentials = "service-account credentials"
	}
	return fmt.Errorf("collector_unauthorized: Radar's %s were rejected while reading %s; refresh them and retry: %w", credentials, kind, err)
}
