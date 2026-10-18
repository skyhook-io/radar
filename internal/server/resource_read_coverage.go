package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"golang.org/x/sync/errgroup"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const resourceListHardLimit = 50000

// readStatus is the HTTP status of a failed resource read, by code. It is
// transport only — clients branch on error_code — but it is chosen so a UI or
// fleet client that branches on status alone still does the right thing:
// Radar's own collection is never 401/403 (that reads as "you lack access"),
// and a configuration limit is never 503 (that reads as "retry"). Fleet
// clients exclude 503 from their cluster circuit breaker, so a collector
// failure scoped to one kind is 503 rather than another 5xx.
var readStatus = map[readreason.Code]int{
	readreason.UserForbidden:              http.StatusForbidden,
	readreason.UserNoNamespaceAccess:      http.StatusForbidden,
	readreason.UserNamespaceNotVisible:    http.StatusForbidden,
	readreason.UserPermissionCheckFailed:  http.StatusServiceUnavailable,
	readreason.RadarForbidden:             http.StatusServiceUnavailable,
	readreason.RadarCredentialsRejected:   http.StatusServiceUnavailable,
	readreason.RadarNamespaceNotCollected: http.StatusConflict,
	readreason.KindSyncPending:            http.StatusServiceUnavailable,
	readreason.KindSyncTimedOut:           http.StatusServiceUnavailable,
	readreason.RadarKindNotCollected:      http.StatusServiceUnavailable,
	readreason.KindUnknown:                http.StatusBadRequest,
	readreason.KindNotWatchable:           http.StatusBadRequest,
	readreason.OutsideNamespacePin:        http.StatusConflict,
	readreason.OutsideNamespaceSelection:  http.StatusConflict,
	readreason.NamespaceNotFound:          http.StatusNotFound,
	readreason.NamespaceRequired:          http.StatusBadRequest,
	readreason.ObjectNotFound:             http.StatusNotFound,
	readreason.ListTooLarge:               http.StatusBadRequest,
	readreason.FilterEvalFailed:           http.StatusBadRequest,
	readreason.ClusterConnecting:          http.StatusServiceUnavailable,
	readreason.ClusterDisconnected:        http.StatusServiceUnavailable,
	readreason.ConnectionChanged:          http.StatusConflict,
	readreason.ReadError:                  http.StatusInternalServerError,
}

func readStatusFor(code readreason.Code) int {
	if status, ok := readStatus[code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

// resourceReadFailure is a read that served nothing. gaps keeps every reason
// when namespaces failed for different ones; code is the most actionable.
type resourceReadFailure struct {
	code    readreason.Code
	message string
	gaps    []resourceapi.Gap
}

func (e *resourceReadFailure) Error() string { return e.message }

func readFailuref(code readreason.Code, format string, args ...any) *resourceReadFailure {
	return &resourceReadFailure{code: code, message: fmt.Sprintf(format, args...)}
}

// permissionFailure is a caller permission check that didn't allow the read:
// a denial, or — when the check itself failed — a retryable non-answer.
func permissionFailure(checked bool, denial string) *resourceReadFailure {
	if !checked {
		return readFailuref(readreason.UserPermissionCheckFailed, "Radar could not complete your permission check; retry when it is available")
	}
	return readFailuref(readreason.UserForbidden, "%s", denial)
}

// errReadHandlesChanged marks a read whose cache, dynamic cache or discovery
// was replaced while it ran, so its rows may mix two clusters.
var errReadHandlesChanged = errors.New("Radar's cluster connection changed during this read; confirm which cluster Radar is connected to, then retry")

func collectionFailure(resource string, err error) *resourceReadFailure {
	switch {
	case errors.Is(err, errReadHandlesChanged):
		return readFailuref(readreason.ConnectionChanged, "%s", err.Error())
	case errors.Is(err, k8score.ErrResourceListTooLarge):
		return readFailuref(readreason.ListTooLarge, "Too many resources to load; select fewer namespaces")
	case errors.Is(err, k8score.ErrCollectionNotSupported):
		return readFailuref(readreason.KindNotWatchable, "%s cannot be collected with list/watch", resource)
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return readFailuref(readreason.ReadError, "Kubernetes read did not finish within the request budget; retry with fewer namespaces")
	case apierrors.IsUnauthorized(err):
		return readFailuref(readreason.RadarCredentialsRejected, "Kubernetes rejected Radar's collector credentials")
	case apierrors.IsForbidden(err):
		return readFailuref(readreason.RadarForbidden, "Radar's collector cannot read %s in the requested scope", resource)
	case errors.Is(err, k8s.ErrUnknownDynamicKind):
		return readFailuref(readreason.KindUnknown, "%s", err.Error())
	case errors.Is(err, k8s.ErrDynamicNotReady):
		return readFailuref(readreason.KindSyncPending, "Resource discovery is still loading")
	default:
		return readFailuref(readreason.ReadError, "Radar could not read %s", resource)
	}
}

// writeReadFailure answers a failed read with {error, error_code, coverage?}.
// target, when the read has one, gives the failure its coverage: the scope
// and every gap, with no read scope because Radar read nothing.
func (s *Server) writeReadFailure(w http.ResponseWriter, resource string, err error, target *listTarget) {
	var failure *resourceReadFailure
	if !errors.As(err, &failure) {
		failure = collectionFailure(resource, err)
	}
	if failure.code == readreason.ReadError {
		log.Printf("[resource-read] Failed to read %s: %v", resource, err)
	}
	body := resourceapi.ReadError{Error: failure.message, ErrorCode: failure.code}
	if target != nil {
		gaps := append(slices.Clone(target.gaps), failure.gaps...)
		if !slices.ContainsFunc(gaps, func(gap resourceapi.Gap) bool { return gap.Code == failure.code }) {
			gaps = append(gaps, resourceapi.Gap{Code: failure.code})
		}
		body.Coverage = &resourceapi.Coverage{Status: resourceapi.CoveragePartial, Scope: target.scope(), NarrowedBy: target.narrowedBy, Gaps: gaps}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(readStatusFor(failure.code))
	s.writeJSON(w, body)
}

// writeCollectionFailure answers a failed read that has no list target.
func (s *Server) writeCollectionFailure(w http.ResponseWriter, resource string, err error) {
	s.writeReadFailure(w, resource, err, nil)
}

// listTarget is what a list is about: the namespaces the request named, or
// what remained of every namespace after the --namespace-scope pin, the saved
// namespace pick and the caller's access narrowed it. A named namespace stays
// in scope even when it can't be read, so what's missing is a gap; implicit
// narrowing is declared instead. gaps are the caller's own: named namespaces
// they can't see, and namespaces they can see where exact kind authorization
// denies the kind.
type listTarget struct {
	clusterScoped bool
	// explicit: the request named its namespaces.
	explicit bool
	// namespaces is what may be read; named, on an explicit request, is every
	// namespace it named, including ones the caller can't see or the pin
	// excludes: those stay in scope, as gaps.
	namespaces, named []string
	narrowedBy        []resourceapi.Narrowing
	gaps              []resourceapi.Gap
}

func (t listTarget) scope() resourceapi.Scope {
	switch {
	case t.clusterScoped:
		return resourceapi.Scope{Type: resourceapi.ScopeCluster}
	case t.explicit:
		return resourceapi.Scope{Type: resourceapi.ScopeNamespaces, Namespaces: resourceapi.Names(t.named, resourceapi.RESTSetLimit)}
	case t.namespaces == nil:
		return resourceapi.Scope{Type: resourceapi.ScopeAllNamespaces}
	default:
		return resourceapi.Scope{Type: resourceapi.ScopeNamespaces, Namespaces: resourceapi.Names(t.namespaces, resourceapi.RESTSetLimit)}
	}
}

// coverage is a successful list's coverage: read is the namespaces read, or
// nil when Radar read the whole scope.
func (t listTarget) coverage(read []string, gaps []resourceapi.Gap) resourceapi.Coverage {
	gaps = append(slices.Clone(t.gaps), gaps...)
	cov := resourceapi.Coverage{Status: resourceapi.CoverageComplete, Scope: t.scope(), NarrowedBy: t.narrowedBy, Gaps: gaps}
	readScope := cov.Scope
	switch {
	case t.clusterScoped:
	case read != nil:
		readScope = resourceapi.Scope{Type: resourceapi.ScopeNamespaces, Namespaces: resourceapi.Names(read, resourceapi.RESTSetLimit)}
	case t.namespaces != nil:
		// The whole readable target was read, which never includes named
		// namespaces the caller can't see or the pin excludes.
		readScope = resourceapi.Scope{Type: resourceapi.ScopeNamespaces, Namespaces: resourceapi.Names(t.namespaces, resourceapi.RESTSetLimit)}
	}
	cov.Read = &readScope
	if len(gaps) > 0 {
		cov.Status = resourceapi.CoveragePartial
	}
	if cov.Gaps == nil {
		cov.Gaps = []resourceapi.Gap{}
	}
	return cov
}

// resourceListTarget resolves a list's target from the request. visible is
// parseNamespacesForUser's answer (nil: every namespace) and kindGaps what
// exact kind authorization withholds of it.
func (s *Server) resourceListTarget(r *http.Request, visible []string, kindGaps []resourceapi.Gap, clusterScoped bool) listTarget {
	if clusterScoped {
		return listTarget{clusterScoped: true}
	}
	return s.requestListTarget(r, visible).withGaps(kindGaps)
}

// requestListTarget is the part of a list's target that depends only on the
// request, not on the kind.
func (s *Server) requestListTarget(r *http.Request, visible []string) listTarget {
	t := listTarget{namespaces: visible}
	if named := parseNamespaces(r.URL.Query()); named != nil {
		t.explicit = true
		// A named namespace the caller can't see is a gap, named back: it is
		// what they asked for, and namespace_not_visible reads the same whether
		// that namespace exists or not.
		t.named = slices.Compact(slices.Sorted(slices.Values(named)))
		var pinned, hidden []string
		t.namespaces = []string{}
		for _, ns := range t.named {
			switch {
			case visible == nil || slices.Contains(visible, ns):
				t.namespaces = append(t.namespaces, ns)
			case k8s.ForceNamespaceScope && ns != k8s.GetNamespaceScopeTarget():
				pinned = append(pinned, ns)
			default:
				hidden = append(hidden, ns)
			}
		}
		if len(pinned) > 0 {
			t.gaps = append(t.gaps, gapOf(readreason.OutsideNamespacePin, pinned))
		}
		if len(hidden) > 0 {
			t.gaps = append(t.gaps, gapOf(readreason.UserNamespaceNotVisible, hidden))
		}
	} else if visible != nil {
		if s.getUserNamespaces(r, nil) != nil {
			t.narrowedBy = append(t.narrowedBy, resourceapi.NarrowedByUserAccess)
		}
		if k8s.ForceNamespaceScope {
			t.narrowedBy = append(t.narrowedBy, resourceapi.NarrowedByNamespacePin)
		} else if _, picks := s.getActiveNamespaceForUserInContext(r); len(picks) > 0 {
			t.narrowedBy = append(t.narrowedBy, resourceapi.NarrowedByNamespaceSelection)
		}
	}
	return t
}

// readCache is the typed cache a read would use now. A handler captures it
// before authorizing and checks it again before answering, so authorization,
// coverage and rows all come from the connection the request started on.
func readCache() *k8s.ResourceCache {
	active, syncing := k8s.SnapshotCaches()
	if active != nil {
		return active
	}
	return syncing
}

// outsideNamespacePin refuses a read of one namespace Radar's --namespace-scope
// pin excludes: its cache holds nothing there, so a miss would read as absence.
func outsideNamespacePin(namespace string) *resourceReadFailure {
	if namespace != "" && k8s.ForceNamespaceScope && namespace != k8s.GetNamespaceScopeTarget() {
		return readFailuref(readreason.OutsideNamespacePin, "namespace %q is outside Radar's --namespace-scope pin", namespace)
	}
	return nil
}

// noAccessFailure says why a list the caller can read nothing of failed: the
// namespaces it named are outside the pin or hidden, the caller's namespace
// access couldn't be determined, or they have none.
func noAccessFailure(failure *resourceReadFailure, target listTarget, accessCheckFailed bool) *resourceReadFailure {
	if failure.code != readreason.UserNoNamespaceAccess {
		return failure
	}
	if accessCheckFailed {
		return readFailuref(readreason.UserPermissionCheckFailed, "Radar could not determine your namespace access; retry when the permission check is available")
	}
	if len(target.gaps) > 0 {
		codes := make([]readreason.Code, 0, len(target.gaps))
		for _, gap := range target.gaps {
			codes = append(codes, gap.Code)
		}
		return &resourceReadFailure{code: readreason.Primary(codes), message: failure.message}
	}
	return failure
}

// withGaps adds the caller's own gaps within namespaces they can see: exact
// kind authorization (Secrets, LimitRanges) denying the kind there, or a
// check that couldn't complete. Naming them leaks nothing.
func (t listTarget) withGaps(gaps []resourceapi.Gap) listTarget {
	t.gaps = append(slices.Clone(t.gaps), gaps...)
	return t
}

// typedRead is what a typed kind's cache can serve of a target now.
type typedRead struct {
	// read: the namespaces to read; nil means the whole target.
	read []string
	// whole: the kind is ready as a whole, so its listers serve the read; a
	// kind still loading elsewhere is read from its namespaces' own stores.
	whole bool
	gaps  []resourceapi.Gap
	// collected, when non-nil, is the part of an implicit target Radar
	// collects the kind in: the read was narrowed to it. uncovered is what it
	// left out of a finite target.
	collected, uncovered []string
}

// narrowedByCollection narrows t to what Radar collects of it: collected when
// t is every namespace, else t without uncovered.
func (t listTarget) narrowedByCollection(collected, uncovered []string) listTarget {
	if t.namespaces == nil {
		t.namespaces = collected
	} else {
		dropped := make(map[string]bool, len(uncovered))
		for _, ns := range uncovered {
			dropped[ns] = true
		}
		t.namespaces = slices.DeleteFunc(slices.Clone(t.namespaces), func(ns string) bool { return dropped[ns] })
	}
	if !slices.Contains(t.narrowedBy, resourceapi.NarrowedByNamespaceCollection) {
		t.narrowedBy = append(slices.Clone(t.narrowedBy), resourceapi.NarrowedByNamespaceCollection)
	}
	return t
}

// typedReadCoverage assesses what of namespaces (nil: every namespace) a typed
// kind can be read in now, and why the rest can't. It fails only when nothing
// can be read. Namespaces Radar doesn't collect are gaps when the request
// named them (explicit); otherwise they narrow the read.
func typedReadCoverage(cache *k8s.ResourceCache, resource string, namespaces []string, clusterScoped, explicit bool) (typedRead, error) {
	a := cache.AssessKindRead(resource, namespaces)
	pending := func(ns ...string) resourceapi.Gap {
		return gapOf(readreason.KindSyncPending, ns)
	}
	failed := func(ns ...string) resourceapi.Gap {
		return gapOf(readreason.KindSyncTimedOut, ns)
	}
	switch {
	case a.Readiness == k8s.KindUnavailable:
		if permissions := cache.PermissionResult(); permissions != nil {
			if scope, known := permissions.Scopes[resource]; known && !scope.Enabled {
				failure := collectionFailure(resource, permissions.ProbeErrors[resource])
				if failure.code == readreason.ReadError {
					failure = readFailuref(readreason.RadarForbidden, "Radar's collector cannot list/watch %s", resource)
				}
				return typedRead{}, failure
			}
		}
		return typedRead{}, readFailuref(readreason.RadarKindNotCollected, "Radar is not watching %s", resource)
	case a.AllNamespaces && namespaces == nil:
		switch a.Readiness {
		case k8s.KindReady:
			return typedRead{whole: true}, nil
		case k8s.KindFailed:
			return typedRead{}, &resourceReadFailure{code: readreason.KindSyncTimedOut, message: fmt.Sprintf("%s did not finish loading within the sync deadline; Radar keeps trying, so retry later", resource), gaps: []resourceapi.Gap{failed()}}
		default:
			return typedRead{}, &resourceReadFailure{code: readreason.KindSyncPending, message: fmt.Sprintf("%s are still loading, please retry shortly", resource), gaps: []resourceapi.Gap{pending()}}
		}
	}
	var gaps []resourceapi.Gap
	var collected []string
	// A namespace whose access check Kubernetes answered with a credential
	// rejection is not a limit of what Radar collects: it stays in scope, as
	// a gap.
	var rejected []string
	if !clusterScoped {
		rejected = slices.DeleteFunc(cache.PermissionResult().CredentialRejectedNamespaces(resource, namespaces), func(ns string) bool {
			return slices.Contains(a.Read, ns) || slices.Contains(a.Loading, ns) || slices.Contains(a.Failed, ns)
		})
	}
	uncovered := slices.DeleteFunc(slices.Clone(a.Uncovered), func(ns string) bool { return slices.Contains(rejected, ns) })
	if len(rejected) > 0 {
		gaps = append(gaps, gapOf(readreason.RadarCredentialsRejected, rejected))
	}
	switch {
	case explicit && len(uncovered) > 0:
		gaps = append(gaps, gapOf(readreason.RadarNamespaceNotCollected, uncovered))
	case len(uncovered) > 0 || (namespaces == nil && !a.AllNamespaces):
		collected = slices.Sorted(slices.Values(slices.Concat(a.Read, a.Loading, a.Failed, rejected)))
		if len(collected) == 0 {
			gaps = append(gaps, gapOf(readreason.RadarNamespaceNotCollected, uncovered))
		}
	}
	if len(a.Loading) > 0 {
		gaps = append(gaps, pending(a.Loading...))
	}
	if len(a.Failed) > 0 {
		gaps = append(gaps, failed(a.Failed...))
	}
	if len(a.Read) == 0 && !(namespaces != nil && len(namespaces) == 0) {
		codes := make([]readreason.Code, 0, len(gaps))
		for _, gap := range gaps {
			codes = append(codes, gap.Code)
		}
		code := readreason.Primary(codes)
		message := fmt.Sprintf("Radar's collector does not cover the requested scope for %s", resource)
		switch code {
		case readreason.RadarCredentialsRejected:
			message = fmt.Sprintf("Kubernetes rejected Radar's collector credentials when it checked its access to %s", resource)
		case readreason.KindSyncPending:
			message = fmt.Sprintf("%s are still loading, please retry shortly", resource)
		case readreason.KindSyncTimedOut:
			message = fmt.Sprintf("%s did not finish loading within the sync deadline; Radar keeps trying, so retry later", resource)
		}
		return typedRead{}, &resourceReadFailure{code: code, message: message, gaps: gaps}
	}
	read := a.Read
	if clusterScoped || (a.AllNamespaces && namespaces == nil) {
		read = nil
	}
	return typedRead{read: read, whole: a.Readiness == k8s.KindReady, gaps: gaps, collected: collected, uncovered: uncovered}, nil
}

func gapOf(code readreason.Code, namespaces []string) resourceapi.Gap {
	gap := resourceapi.Gap{Code: code}
	if len(namespaces) > 0 {
		gap.Namespaces = resourceapi.Names(namespaces, resourceapi.RESTSetLimit)
	}
	return gap
}

func parseResourceListLimit(r *http.Request, summary bool) (int, error) {
	limit := 25000
	if summary {
		limit = resourceListHardLimit
	}
	if value := r.URL.Query().Get("maxItems"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > resourceListHardLimit {
			return 0, fmt.Errorf("maxItems must be between 1 and %d", resourceListHardLimit)
		}
		limit = parsed
	}
	return limit, nil
}

func (s *Server) checkResourceListSize(w http.ResponseWriter, count, limit int) bool {
	if count <= limit {
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	s.writeJSON(w, map[string]any{"error": "Too many resources to load; select fewer namespaces", "error_code": readreason.ListTooLarge, "count": count, "limit": limit})
	return false
}

func listSize(items any) int {
	if items == nil {
		return 0
	}
	value := reflect.ValueOf(items)
	if value.Kind() == reflect.Slice {
		return value.Len()
	}
	return 0
}

func typedListCount(cache *k8s.ResourceCache, resource string, namespaces []string) int {
	// ListCountNamespaced reads an empty slice as every namespace.
	if namespaces != nil && len(namespaces) == 0 {
		return 0
	}
	for _, kind := range k8score.AllKindListers() {
		if informerKeyForKind(kind.Kind()) == resource {
			return k8score.ListCountNamespaced(kind.Lister()(cache.ResourceCache), namespaces)
		}
	}
	return 0
}

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

// unansweredProbeCode is why namespaces whose access check got no answer
// weren't read: still being retried, or never answered.
func unansweredProbeCode(observation k8score.DynamicResourceObservation) readreason.Code {
	if observation.ProbeRetrying {
		return readreason.KindSyncPending
	}
	return readreason.ReadError
}

func namespaceReadFailure(dynamicCache *k8s.DynamicResourceCache, gvr schema.GroupVersionResource, namespace string) *resourceReadFailure {
	observation := dynamicCache.NamespaceObservation(gvr, namespace)
	switch {
	case observation.ReasonCode == "cache_stopped":
		// A context switch stopped the cache mid-read: its stores no longer
		// describe the cluster this request is about.
		return readFailuref(readreason.ConnectionChanged, "Radar's cluster connection changed during this read, so the result was discarded; confirm which cluster Radar is connected to, then retry")
	case observation.ReasonCode == "sync_stalled":
		return readFailuref(readreason.KindSyncTimedOut, "%s have not finished loading; Radar keeps trying, so retry later", gvr.Resource)
	case observation.State == k8score.DynamicObservationDenied && observation.ReasonCode == "credentials_rejected":
		return readFailuref(readreason.RadarCredentialsRejected, "Kubernetes rejected Radar's own credentials when it checked access to %s", gvr.Resource)
	case observation.State == k8score.DynamicObservationDenied:
		return readFailuref(readreason.RadarForbidden, "Radar's collector cannot read %s in the requested scope", gvr.Resource)
	case observation.State == k8score.DynamicObservationUnwatched && dynamicCache.Observation(gvr).State == k8score.DynamicObservationSynced:
		return readFailuref(readreason.RadarNamespaceNotCollected, "Radar's collector does not cover the requested scope for %s", gvr.Resource)
	case observation.State == k8score.DynamicObservationUnsupported:
		return readFailuref(readreason.KindNotWatchable, "%s cannot be collected with list/watch", gvr.Resource)
	case observation.State == k8score.DynamicObservationDeferred && observation.ReasonCode == "scope_probe_failed":
		return readFailuref(readreason.ReadError, "Radar's access check for %s failed, so it can't say what exists there; retry", gvr.Resource)
	default:
		return readFailuref(readreason.KindSyncPending, "%s are still loading, please retry shortly", gvr.Resource)
	}
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
			if !apierrors.IsForbidden(listErr) || clusterScoped {
				return dynamicList{}, listErr
			}
			targets = dynamicCache.FallbackNamespaces()
		} else {
			// Rows read before the informer finished syncing are incomplete
			// even if it finishes during the read, so a sync seen only after
			// the read means reading again.
			syncedBefore := dynamicCache.IsNamespaceSynced(gvr, "")
			items, listErr := dynamicCache.ListContext(r.Context(), gvr, "")
			if listErr != nil {
				if !current() {
					return dynamicList{}, errReadHandlesChanged
				}
				return dynamicList{}, probeRetryFailure(dynamicCache, gvr, listErr)
			}
			if !syncedBefore && dynamicCache.IsNamespaceSynced(gvr, "") {
				syncedBefore = true
				if items, listErr = dynamicCache.ListContext(r.Context(), gvr, ""); listErr != nil {
					if !current() {
						return dynamicList{}, errReadHandlesChanged
					}
					return dynamicList{}, probeRetryFailure(dynamicCache, gvr, listErr)
				}
			}
			if syncedBefore {
				if !current() {
					return dynamicList{}, errReadHandlesChanged
				}
				return dynamicList{rows: items}, nil
			}
			observation := dynamicCache.Observation(gvr)
			if observation.Scope != k8score.DynamicObservationScopeExplicitNamespaces {
				failure := namespaceReadFailure(dynamicCache, gvr, "")
				failure.gaps = []resourceapi.Gap{{Code: failure.code}}
				return dynamicList{}, failure
			}
			targets = observation.Namespaces
			unanswered = observation.UnansweredNamespaces
			unansweredCode = unansweredProbeCode(observation)
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
	tooLarge := false
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
				syncedBefore := dynamicCache.IsNamespaceSynced(gvr, namespace)
				items, readErr = dynamicCache.ListBlockingContext(ctx, gvr, namespace, 3*time.Second)
				switch {
				case readErr != nil:
				case !dynamicCache.IsNamespaceSynced(gvr, namespace):
					readErr = namespaceReadFailure(dynamicCache, gvr, namespace)
				case !syncedBefore:
					// The wait may have handed back rows from before the sync.
					items, readErr = dynamicCache.ListContext(ctx, gvr, namespace)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if readErr != nil {
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
			if len(rows)+len(items) > limit {
				tooLarge = true
				cancel()
			} else {
				rows = append(rows, items...)
			}
			return nil
		})
	}
	_ = groupReads.Wait()
	if !current() {
		return dynamicList{}, errReadHandlesChanged
	}
	if tooLarge || len(failures[readreason.ListTooLarge]) > 0 {
		return dynamicList{}, readFailuref(readreason.ListTooLarge, "Too many resources to load; select fewer namespaces")
	}
	// Radar not collecting, or being refused, a namespace the request didn't
	// name narrows it, once something was read.
	var uncovered []string
	if !explicit && !clusterScoped && len(read) > 0 {
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
		failures[unansweredCode] = append(failures[unansweredCode], unanswered...)
		if failureByCode[unansweredCode] == nil {
			failureByCode[unansweredCode] = readFailuref(unansweredCode, "Radar has not established whether it can collect %s in some namespaces", gvr.Resource)
		}
	}
	var gaps []resourceapi.Gap
	for _, code := range slices.Sorted(maps.Keys(failures)) {
		gaps = append(gaps, gapOf(code, failures[code]))
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
	if !narrowed && len(failures) == 0 {
		read = nil
	}
	slices.Sort(read)
	return dynamicList{rows: rows, read: read, gaps: gaps, narrowed: narrowed, collected: collected, uncovered: uncovered}, nil
}

// dynamicCountCoverage reports which of namespaces (nil: every namespace) a
// dynamic kind can be counted in now: read is nil when all of them. A
// namespace whose informer is still loading is a kind_sync_pending gap. One
// Radar doesn't collect narrows a scope the request didn't name (collected
// for every namespace, uncovered for a finite one) and is a gap in one it did.
func dynamicCountCoverage(cache *k8s.DynamicResourceCache, gvr schema.GroupVersionResource, namespaces []string, clusterScoped, explicit bool) (read, collected, uncovered []string, gaps []resourceapi.Gap, ok bool) {
	if cache.IsNamespaceSynced(gvr, "") {
		return nil, nil, nil, nil, true
	}
	if clusterScoped {
		return nil, nil, nil, nil, false
	}
	observation := cache.Observation(gvr)
	watched := make(map[string]bool, len(observation.Namespaces))
	for _, ns := range observation.Namespaces {
		watched[ns] = true
	}
	unanswered := make(map[string]bool, len(observation.UnansweredNamespaces))
	for _, ns := range observation.UnansweredNamespaces {
		unanswered[ns] = true
	}
	// A namespace whose access check hasn't answered may be collected: it is
	// a gap, never outside the collection.
	collects := func(ns string) bool {
		return observation.Scope != k8score.DynamicObservationScopeExplicitNamespaces || watched[ns] || unanswered[ns]
	}
	targets := namespaces
	if targets == nil {
		targets = slices.Concat(observation.Namespaces, observation.UnansweredNamespaces)
	}
	var loading, outside []string
	read = []string{}
	for _, namespace := range targets {
		switch {
		case cache.IsNamespaceSynced(gvr, namespace):
			read = append(read, namespace)
		case collects(namespace):
			loading = append(loading, namespace)
		default:
			outside = append(outside, namespace)
		}
	}
	if len(read) == 0 {
		return nil, nil, nil, nil, false
	}
	var unchecked []string
	loading = slices.DeleteFunc(loading, func(ns string) bool {
		if unanswered[ns] {
			unchecked = append(unchecked, ns)
			return true
		}
		return false
	})
	if len(loading) > 0 {
		gaps = append(gaps, gapOf(readreason.KindSyncPending, loading))
	}
	if len(unchecked) > 0 {
		gaps = append(gaps, gapOf(unansweredProbeCode(observation), unchecked))
	}
	switch {
	case namespaces == nil:
		collected = slices.Sorted(slices.Values(targets))
	case len(outside) == 0:
	case explicit:
		gaps = append(gaps, gapOf(readreason.RadarNamespaceNotCollected, outside))
	default:
		uncovered = outside
	}
	return read, collected, uncovered, gaps, true
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
	syncedBefore := dynamicCache.IsNamespaceSynced(gvr, namespace)
	resource, err := dynamicCache.Get(gvr, namespace, name)
	if !current() {
		return nil, errReadHandlesChanged
	}
	if err == nil {
		return resource, nil
	}
	if apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err) {
		return nil, err
	}
	if apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound) {
		if !dynamicCache.IsNamespaceSynced(gvr, namespace) {
			return nil, namespaceReadFailure(dynamicCache, gvr, namespace)
		}
		// A miss looked up before the informer finished syncing proves
		// nothing; look again now that it has.
		if !syncedBefore {
			resource, err = dynamicCache.Get(gvr, namespace, name)
			if !current() {
				return nil, errReadHandlesChanged
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

const resourceCountsNamespaceBudget = 2048

// boundCountCoverageDetails caps the namespace samples of a counts response,
// which carries a coverage per kind, so it can't grow with namespaces × kinds.
func boundCountCoverageDetails(coverage map[string]resourceapi.Coverage) {
	sets := 0
	for _, value := range coverage {
		if value.Scope.Namespaces != nil {
			sets++
		}
		if value.Read != nil && value.Read.Namespaces != nil {
			sets++
		}
		for _, gap := range value.Gaps {
			if gap.Namespaces != nil {
				sets++
			}
		}
	}
	if sets == 0 {
		return
	}
	limit := min(resourceapi.RESTSetLimit, resourceCountsNamespaceBudget/sets)
	for kind, value := range coverage {
		value.Scope.Namespaces = value.Scope.Namespaces.Capped(limit)
		if value.Read != nil {
			read := *value.Read
			read.Namespaces = read.Namespaces.Capped(limit)
			value.Read = &read
		}
		value.Gaps = slices.Clone(value.Gaps)
		for i := range value.Gaps {
			value.Gaps[i].Namespaces = value.Gaps[i].Namespaces.Capped(limit)
		}
		coverage[kind] = value
	}
}
