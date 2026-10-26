package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"golang.org/x/sync/errgroup"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Rows and observations use these captured handles. A lifecycle change before
// capture completes is retryable rather than mixing data from two clusters.
func dynamicReadHandles(cache *k8s.ResourceCache, kind, group string) (*k8s.DynamicResourceCache, schema.GroupVersionResource, error) {
	dynamicCache := k8s.GetDynamicResourceCache()
	discovery := k8s.GetResourceDiscovery()
	if cache == nil || cache != k8s.GetResourceCache() || dynamicCache == nil || discovery == nil {
		return nil, schema.GroupVersionResource{}, k8s.ErrDynamicNotReady
	}
	var gvr schema.GroupVersionResource
	var found bool
	if group != "" {
		gvr, found = discovery.GetGVRWithGroup(kind, group)
	} else {
		gvr, found = discovery.GetGVR(kind)
	}
	if !found {
		gvr, found = k8s.BuiltinGVR(kind, group)
	}
	if !found {
		return nil, gvr, fmt.Errorf("%w: %s", k8s.ErrUnknownDynamicKind, kind)
	}
	if cache != k8s.GetResourceCache() || dynamicCache != k8s.GetDynamicResourceCache() || discovery != k8s.GetResourceDiscovery() {
		return nil, gvr, k8s.ErrDynamicNotReady
	}
	return dynamicCache, gvr, nil
}

func namespaceReadFailure(dynamicCache *k8s.DynamicResourceCache, gvr schema.GroupVersionResource, namespace string) *resourceReadFailure {
	var target []string
	if namespace != "" {
		target = []string{namespace}
	}
	plan := readplan.Dynamic(dynamicCache, gvr, target, false)
	for _, part := range plan.Partitions {
		if part.Verdict.State != readplan.Readable {
			return dynamicPlanFailure(gvr.Resource, part.Verdict.Code)
		}
	}
	return nil
}

// A read must use the same synced stores on both sides; promotion can replace
// namespace informers inside an otherwise unchanged cache handle.
func readCachedDynamicList(cache *k8s.DynamicResourceCache, gvr schema.GroupVersionResource, namespace string, read func(first bool) ([]*unstructured.Unstructured, error)) ([]*unstructured.Unstructured, bool, error) {
	var rows []*unstructured.Unstructured
	for attempt := 0; attempt < 2; attempt++ {
		var err error
		var authoritative bool
		rows, authoritative, err = k8score.ReadWithSourceCheck(cache.DynamicResourceCache, gvr, namespace, func() ([]*unstructured.Unstructured, error) {
			return read(attempt == 0)
		})
		if err != nil {
			return nil, false, err
		}
		if authoritative {
			return rows, true, nil
		}
		if _, synced := cache.ReadSource(gvr, namespace); !synced {
			break
		}
	}
	return rows, false, nil
}

// dynamicList is what a dynamic kind's list read: its rows, the namespaces
// read (nil: all of the target), and the gaps. When Radar's collection
// narrowed a request that named no namespaces, narrowed is set, with collected
// the namespaces it collects the kind in for an every-namespace target, and
// uncovered what it left out of a finite one.
type dynamicList struct {
	rows                 []*unstructured.Unstructured
	read                 []string
	gaps                 []resourceapi.Gap
	narrowed             bool
	collected, uncovered []string
}

// readDynamicResourceList lists a dynamic kind in namespaces (nil: every
// namespace). Each namespace is a separate read: one that fails is a gap, and
// the list fails only when none succeeds — or when the list is over its size
// budget, or the read handles changed, which invalidate the whole answer. A
// namespace Radar doesn't collect, or is refused in, narrows a request that
// named no namespaces (explicit false) instead of leaving a gap in it.
func readDynamicResourceList(r *http.Request, cache *k8s.ResourceCache, kind, group string, namespaces []string, explicit bool, limit int) (dynamicList, error) {
	dynamicCache, gvr, err := dynamicReadHandles(cache, kind, group)
	if err != nil {
		return dynamicList{}, err
	}
	// A context switch stops the old cache before replacing it, so a stopped
	// cache is as stale as a replaced one.
	current := func() bool {
		return cache == k8s.GetResourceCache() && dynamicCache == k8s.GetDynamicResourceCache() && !dynamicCache.IsStopped()
	}
	if !current() {
		return dynamicList{}, errReadHandlesChanged
	}
	clusterScoped, _, _ := k8s.ClassifyKindScope(kind, group)
	direct := k8s.ShouldBypassDynamicInformer(gvr)
	targets := namespaces
	var collected, unanswered []string
	var unansweredCode readreason.Code
	var unknownGaps []resourceapi.Gap
	if namespaces == nil {
		if direct {
			items, listErr := dynamicCache.ListDirectBounded(r.Context(), gvr, "", limit)
			if listErr == nil {
				if !current() {
					return dynamicList{}, errReadHandlesChanged
				}
				return dynamicList{rows: items}, nil
			}
			if !current() {
				return dynamicList{}, errReadHandlesChanged
			}
			if errors.Is(listErr, k8score.ErrResourceListTooLarge) {
				return dynamicList{}, &resourceReadFailure{code: readreason.ListTooLarge, message: "Too many resources to load; select fewer namespaces", count: limit + 1, limit: limit}
			}
			if !apierrors.IsForbidden(listErr) || clusterScoped {
				return dynamicList{}, listErr
			}
			targets = dynamicCache.FallbackNamespaces()
		} else {
			items, settled, listErr := readCachedDynamicList(dynamicCache, gvr, "", func(bool) ([]*unstructured.Unstructured, error) {
				return dynamicCache.ListContext(r.Context(), gvr, "")
			})
			if !current() {
				return dynamicList{}, errReadHandlesChanged
			}
			if listErr != nil && collectionFailure(gvr.Resource, listErr).code == readreason.ListTooLarge {
				return dynamicList{}, &resourceReadFailure{code: readreason.ListTooLarge, message: "Too many resources to load; select fewer namespaces", count: limit + 1, limit: limit}
			}
			if listErr == nil && settled {
				if !current() {
					return dynamicList{}, errReadHandlesChanged
				}
				if failure := namespaceReadFailure(dynamicCache, gvr, ""); failure != nil {
					return dynamicList{}, failure
				}
				return dynamicList{rows: items}, nil
			}
			observation := dynamicCache.Observation(gvr)
			if observation.Scope != k8score.DynamicObservationScopeExplicitNamespaces {
				if listErr != nil {
					return dynamicList{}, probeRetryFailure(dynamicCache, gvr, listErr)
				}
				failure := namespaceReadFailure(dynamicCache, gvr, "")
				if failure == nil {
					// The store finished loading after the read: a retry
					// reads it.
					failure = dynamicPlanFailure(gvr.Resource, readreason.KindSyncPending)
				}
				failure.gaps = []resourceapi.Gap{{Code: failure.code}}
				return dynamicList{}, failure
			}
			targets = observation.Namespaces
			unanswered = observation.UnansweredNamespaces
			facts := readplan.Dynamic(dynamicCache, gvr, nil, clusterScoped)
			for _, part := range facts.Partitions {
				if part.Verdict.State == readplan.Gap && part.Extent == readplan.UnknownNamespaces {
					unknownGaps = append(unknownGaps, partitionGap(part))
				}
				if part.Verdict.State == readplan.Gap && slices.ContainsFunc(part.Namespaces, func(ns string) bool { return slices.Contains(unanswered, ns) }) {
					unansweredCode = part.Verdict.Code
				}
			}
			// The cache records a whole-scope failure only for a kind with no
			// informer, so beside a loaded namespace the planner can't see it.
			// A refusal is what narrowed the collection; any other failure
			// leaves the rest of the scope unread.
			if listErr != nil && len(unknownGaps) == 0 && !apierrors.IsForbidden(listErr) {
				unknownGaps = append(unknownGaps, resourceapi.Gap{Code: collectionFailure(gvr.Resource, listErr).code, Namespaces: resourceapi.UnknownNames()})
			}
		}
		// A namespace whose access check hasn't answered may be collected: it
		// stays in the scope, as a gap.
		collected = slices.Compact(slices.Sorted(slices.Values(slices.Concat(targets, unanswered))))
	}
	targets = slices.Compact(slices.Sorted(slices.Values(targets)))
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var mu sync.Mutex
	var rows []*unstructured.Unstructured
	read := []string{}
	failures := make(map[readreason.Code][]string)
	failureByCode := make(map[readreason.Code]*resourceReadFailure)
	count := 0
	var groupReads errgroup.Group
	groupReads.SetLimit(8)
	for _, namespace := range targets {
		groupReads.Go(func() error {
			var items []*unstructured.Unstructured
			var readErr error
			if ctx.Err() != nil {
				// The budget ran out before this namespace was tried: that
				// says nothing about whether Radar collects it.
				readErr = collectionFailure(gvr.Resource, ctx.Err())
			} else if direct {
				items, readErr = dynamicCache.ListDirectBounded(ctx, gvr, namespace, limit)
			} else {
				var settled bool
				items, settled, readErr = readCachedDynamicList(dynamicCache, gvr, namespace, func(first bool) ([]*unstructured.Unstructured, error) {
					if first {
						return dynamicCache.ListBlockingContext(ctx, gvr, namespace, 3*time.Second)
					}
					return dynamicCache.ListContext(ctx, gvr, namespace)
				})
				if readErr == nil {
					if failure := namespaceReadFailure(dynamicCache, gvr, namespace); failure != nil {
						readErr = failure
					} else if !settled {
						readErr = dynamicPlanFailure(gvr.Resource, readreason.KindSyncPending)
					}
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if readErr != nil {
				if errors.Is(readErr, k8score.ErrResourceListTooLarge) {
					// Bounded direct reads prove at least one item beyond the limit.
					count += limit + 1
				}
				var failure *resourceReadFailure
				if !errors.As(readErr, &failure) {
					failure = collectionFailure(gvr.Resource, readErr)
				}
				failures[failure.code] = append(failures[failure.code], namespace)
				if failureByCode[failure.code] == nil {
					failureByCode[failure.code] = failure
				}
				return nil
			}
			read = append(read, namespace)
			count += len(items)
			if count <= limit {
				rows = append(rows, items...)
			}
			return nil
		})
	}
	_ = groupReads.Wait()
	if !current() {
		return dynamicList{}, errReadHandlesChanged
	}
	// Radar not collecting, or being refused, a namespace the request didn't
	// name narrows it, once something was read.
	var uncovered []string
	if count <= limit && !explicit && !clusterScoped && len(read) > 0 {
		for _, code := range []readreason.Code{readreason.RadarNamespaceNotCollected, readreason.RadarForbidden} {
			uncovered = append(uncovered, failures[code]...)
			delete(failures, code)
		}
		if collected != nil {
			collected = slices.DeleteFunc(collected, func(ns string) bool { return slices.Contains(uncovered, ns) })
		}
	}
	narrowed := collected != nil || len(uncovered) > 0
	if len(unanswered) > 0 {
		if unansweredCode == "" {
			// Their checks answered between the two observations: a retry
			// reads them.
			unansweredCode = readreason.KindSyncPending
		}
		failures[unansweredCode] = append(failures[unansweredCode], unanswered...)
		if failureByCode[unansweredCode] == nil {
			failureByCode[unansweredCode] = readFailuref(unansweredCode, "Radar has not established whether it can collect %s in some namespaces", gvr.Resource)
		}
	}
	gaps := unknownGaps
	for _, code := range slices.Sorted(maps.Keys(failures)) {
		if code != readreason.ListTooLarge {
			gaps = append(gaps, gapOf(code, failures[code]))
		}
	}
	if count > limit {
		return dynamicList{}, &resourceReadFailure{code: readreason.ListTooLarge, message: "Too many resources to load; select fewer namespaces", gaps: gaps, count: count, limit: limit}
	}
	if len(read) == 0 {
		codes := slices.Collect(maps.Keys(failures))
		code := readreason.Primary(codes)
		failure := failureByCode[code]
		if failure == nil {
			failure = readFailuref(readreason.RadarNamespaceNotCollected, "Radar has no readable scope for %s", kind)
		}
		return dynamicList{}, &resourceReadFailure{code: failure.code, message: failure.message, gaps: gaps}
	}
	// nil means every namespace, which only an unbounded request read. A
	// finite request keeps the namespaces it read: exact kind authorization
	// may have left some of its target out.
	if !narrowed && len(failures) == 0 && namespaces == nil {
		read = nil
	}
	slices.Sort(read)
	return dynamicList{rows: rows, read: read, gaps: gaps, narrowed: narrowed, collected: collected, uncovered: uncovered}, nil
}

func readDynamicResourceGet(ctx context.Context, cache *k8s.ResourceCache, kind, group, namespace, name string) (*unstructured.Unstructured, error) {
	dynamicCache, gvr, err := dynamicReadHandles(cache, kind, group)
	if err != nil {
		return nil, err
	}
	// A context switch stops the old cache before replacing it, so a stopped
	// cache is as stale as a replaced one.
	current := func() bool {
		return cache == k8s.GetResourceCache() && dynamicCache == k8s.GetDynamicResourceCache() && !dynamicCache.IsStopped()
	}
	if k8s.ShouldBypassDynamicInformer(gvr) || (gvr.Group == "apiextensions.k8s.io" && gvr.Resource == "customresourcedefinitions") || (gvr.Group == "apiregistration.k8s.io" && gvr.Resource == "apiservices") {
		resource, err := dynamicCache.GetDirect(ctx, gvr, namespace, name)
		if !current() {
			return nil, errReadHandlesChanged
		}
		return resource, err
	}
	read := func() (*unstructured.Unstructured, error) { return dynamicCache.Get(gvr, namespace, name) }
	resource, authoritative, err := k8score.ReadWithSourceCheck(dynamicCache.DynamicResourceCache, gvr, namespace, read)
	if !current() {
		return nil, errReadHandlesChanged
	}
	if err == nil {
		// A hit proves existence even before initial sync; only misses require a complete store.
		return resource, nil
	}
	if apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err) {
		return nil, err
	}
	if apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound) {
		if failure := namespaceReadFailure(dynamicCache, gvr, namespace); failure != nil {
			return nil, failure
		}
		if !authoritative {
			resource, authoritative, err = k8score.ReadWithSourceCheck(dynamicCache.DynamicResourceCache, gvr, namespace, read)
			if !current() {
				return nil, errReadHandlesChanged
			}
			if err == nil {
				return resource, nil
			}
			if apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound) {
				if failure := namespaceReadFailure(dynamicCache, gvr, namespace); failure != nil {
					return nil, failure
				}
				if !authoritative {
					return nil, dynamicPlanFailure(gvr.Resource, readreason.KindSyncPending)
				}
			}
		}
	}

	return resource, probeRetryFailure(dynamicCache, gvr, err)
}

// probeRetryFailure reads a failure no other code explains as still loading
// while Radar is still retrying its access check for the kind: until the
// check answers, Radar can't say where it collects the kind.
func probeRetryFailure(dynamicCache *k8s.DynamicResourceCache, gvr schema.GroupVersionResource, err error) error {
	var failure *resourceReadFailure
	if err == nil || errors.As(err, &failure) || apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound) || collectionFailure(gvr.Resource, err).code != readreason.ReadError {
		return err
	}
	if !dynamicCache.Observation(gvr).ProbeRetrying {
		return err
	}
	return readFailuref(readreason.KindSyncPending, "Radar is still checking its access to %s, please retry shortly", gvr.Resource)
}
