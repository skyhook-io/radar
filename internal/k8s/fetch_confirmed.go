package k8s

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// errClusterChanged means a context switch happened while a direct read was
// confirming a cache answer, so neither the read nor the cache may be served.
var errClusterChanged = errors.New("the cluster connection changed during the read")

// CacheUnconfirmedError reports a single-object read that missed a cache which
// cannot confirm absence (list-only or stale), when the direct read meant to
// confirm it failed too. It is not a "not found": the object may exist.
type CacheUnconfirmedError struct {
	Resource  string
	Namespace string
	Name      string
	State     string
	Err       error
}

func (e *CacheUnconfirmedError) Error() string {
	where := e.Resource
	if e.Namespace != "" {
		where += " in namespace " + e.Namespace
	}
	return fmt.Sprintf("%s %q is not in Radar's cache, but Radar's cache for %s %s, and a direct read to confirm failed, so Radar can't tell whether it exists: %v",
		e.Resource, e.Name, where, e.State, e.Err)
}

func (e *CacheUnconfirmedError) Unwrap() error { return e.Err }

// FetchResourceConfirmed is FetchResource for one user-requested object. When
// the kind's cache in that namespace is list-only (watch denied, so it is a
// periodic snapshot) or stale (its LIST stopped completing), a miss is
// confirmed with a direct GET, and a stale hit is replaced by one. note is
// set when the answer comes from a cache Radar could not refresh.
//
// The direct GET uses Radar's own identity, like the cache it stands in for;
// callers must have authorized the read already.
func FetchResourceConfirmed(ctx context.Context, cache *ResourceCache, kind, namespace, name string) (obj runtime.Object, note string, err error) {
	obj, err = FetchResource(cache, kind, namespace, name)
	gvr, typed := lookupTypedBuiltinGVR(kind)
	if !typed || cache == nil {
		return obj, "", err
	}
	key := gvr.Resource
	listOnly := cache.KindListOnly(key, namespace)
	stale := cache.KindStale(key, namespace)
	switch {
	case !listOnly && !stale:
		return obj, "", err
	case err == nil && !stale:
		return obj, "", nil
	case err != nil && !apierrors.IsNotFound(err):
		return obj, "", err
	}

	state := "is refreshed by periodic list because watch is not permitted"
	if stale {
		state = fmt.Sprintf("has not refreshed since %s", cache.KindLastListAt(key, namespace).Format(time.RFC3339))
	}
	live, liveErr := directTypedGet(ctx, cache, gvr, namespace, name)
	switch {
	case liveErr == nil:
		return live, "", nil
	case apierrors.IsNotFound(liveErr):
		return nil, "", liveErr
	case errors.Is(liveErr, errClusterChanged):
		return nil, "", &CacheUnconfirmedError{Resource: key, Namespace: namespace, Name: name, State: state, Err: liveErr}
	case err == nil:
		return obj, fmt.Sprintf("Radar's cache for %s %s; this copy may be out of date (a direct read failed: %v)", key, state, liveErr), nil
	default:
		return nil, "", &CacheUnconfirmedError{Resource: key, Namespace: namespace, Name: name, State: state, Err: liveErr}
	}
}

// directTypedGet reads one built-in object from the cluster cache belongs to
// and returns it in the form FetchResource would: typed, with the cache's
// transform applied and Helm release Secret values removed.
func directTypedGet(ctx context.Context, cache *ResourceCache, gvr schema.GroupVersionResource, namespace, name string) (runtime.Object, error) {
	// A context switch replaces both caches; reading through a dynamic cache
	// taken after it would answer from the other cluster. The dynamic cache
	// is torn down before the typed generation moves, so a matching
	// generation checked after taking it proves it is cache's cluster. An
	// answer that arrives after a switch is dropped too, and so is a missing
	// dynamic cache mid-switch.
	dynamicCache := GetDynamicResourceCache()
	if !cacheGenerationCurrent(cache.Generation()) {
		return nil, errClusterChanged
	}
	if dynamicCache == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}
	u, err := dynamicCache.GetDirect(ctx, gvr, namespace, name)
	if !cacheGenerationCurrent(cache.Generation()) {
		return nil, errClusterChanged
	}
	if err != nil {
		return nil, err
	}
	typed, err := scheme.Scheme.New(u.GroupVersionKind())
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", gvr.Resource, err)
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, typed); err != nil {
		return nil, fmt.Errorf("decode %s: %w", gvr.Resource, err)
	}
	out, err := k8score.DropManagedFields(typed)
	if err != nil {
		return nil, err
	}
	if secret, ok := out.(*corev1.Secret); ok {
		return k8score.WithoutHelmReleaseValues(secret), nil
	}
	return out.(runtime.Object), nil
}
