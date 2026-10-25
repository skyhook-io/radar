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
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/internal/readscope"
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
	code, _ := readplan.CollectorErrorCode(err)
	switch {
	case errors.Is(err, errReadHandlesChanged):
		return readFailuref(readreason.ConnectionChanged, "%s", err.Error())
	case code == readreason.ListTooLarge:
		return readFailuref(code, "Too many resources to load; select fewer namespaces")
	case code == readreason.KindNotWatchable:
		return readFailuref(code, "%s cannot be collected with list/watch", resource)
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return readFailuref(readreason.ReadError, "Kubernetes read did not finish within the request budget; retry with fewer namespaces")
	case code == readreason.RadarCredentialsRejected:
		return readFailuref(code, "Kubernetes rejected Radar's collector credentials")
	case code == readreason.RadarForbidden:
		return readFailuref(code, "Radar's collector cannot read %s in the requested scope", resource)
	case code == readreason.KindUnknown:
		return readFailuref(code, "%s", err.Error())
	case code == readreason.KindSyncPending:
		return readFailuref(code, "Resource discovery is still loading")
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
		body.Coverage = &resourceapi.Coverage{Status: resourceapi.CoveragePartial, Scope: target.scope(), NarrowedBy: target.narrowedBy, Gaps: target.placeGaps(gaps, nil)}
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
// what remained of every namespace after the --namespace-scope pin and the
// caller's access narrowed it. A named namespace stays
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
	gaps = t.placeGaps(append(slices.Clone(t.gaps), gaps...), read)
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

// resourceListTarget resolves a list's target from the request and the gaps
// exact kind authorization withholds of it.
func (s *Server) resourceListTarget(r *http.Request, kindGaps []resourceapi.Gap, clusterScoped bool) listTarget {
	if clusterScoped {
		return listTarget{clusterScoped: true}
	}
	return s.requestListTarget(r).withGaps(kindGaps)
}

// requestListTarget is the part of a list's target that depends only on the
// request, not on the kind.
func (s *Server) requestListTarget(r *http.Request) listTarget {
	selection, _ := s.requestNamespaceSelection(r, parseNamespaces(r.URL.Query()))
	t := listTarget{namespaces: selection.Namespaces, named: selection.Named, explicit: selection.Named != nil, narrowedBy: selection.NarrowedBy}
	for _, gap := range selection.Gaps {
		t.gaps = append(t.gaps, gapOf(gap.Code, gap.Namespaces))
	}
	return t
}

func (s *Server) requestNamespaceSelection(r *http.Request, requested []string) (readscope.Selection, readreason.Code) {
	ceiling, failed := s.getUserNamespacesWithStatus(r, nil)
	var pin *string
	if k8s.ForceNamespaceScope {
		target := k8s.GetNamespaceScopeTarget()
		pin = &target
	}
	var missing []string
	if !failed && ceiling == nil && requested != nil {
		missing = readCache().KnownMissingNamespaces(requested)
	}
	return readscope.ResolveNamespaces(requested, readscope.NamespaceAccess{Namespaces: ceiling, Checked: !failed}, pin, missing)
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
	if namespace != "" && k8s.ForceNamespaceScope {
		pin := k8s.GetNamespaceScopeTarget()
		selection, _ := readscope.ResolveNamespaces([]string{namespace}, readscope.NamespaceAccess{Checked: true}, &pin, nil)
		if len(selection.Gaps) > 0 {
			return readFailuref(selection.Gaps[0].Code, "namespace %q is outside Radar's --namespace-scope pin", namespace)
		}
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
		primary := readreason.Primary(codes)
		return &resourceReadFailure{code: primary, message: namespaceGapMessage(primary, target.gaps, failure.message)}
	}
	return failure
}

// namespaceGapMessage words a list failure after the namespaces of its primary
// gap, as a get of one of them would. They are names the caller asked for, so
// naming them reveals nothing.
func namespaceGapMessage(code readreason.Code, gaps []resourceapi.Gap, fallback string) string {
	var names []string
	for _, gap := range gaps {
		if gap.Code == code && gap.Namespaces != nil {
			names = append(names, gap.Namespaces.Names...)
		}
	}
	if len(names) == 0 {
		return fallback
	}
	quoted := make([]string, len(names))
	for i, ns := range names {
		quoted[i] = strconv.Quote(ns)
	}
	list := strings.Join(quoted, ", ")
	one := len(names) == 1
	noun := "namespaces " + list
	if one {
		noun = "namespace " + list
	}
	switch code {
	case readreason.NamespaceNotFound:
		if one {
			return noun + " doesn't exist in this cluster"
		}
		return noun + " don't exist in this cluster"
	case readreason.UserNamespaceNotVisible:
		if one {
			return noun + " is not among the namespaces you can see, or does not exist"
		}
		return noun + " are not among the namespaces you can see, or do not exist"
	case readreason.OutsideNamespacePin:
		if one {
			return noun + " is outside Radar's --namespace-scope pin"
		}
		return noun + " are outside Radar's --namespace-scope pin"
	}
	return fallback
}

// withGaps adds the caller's own gaps within namespaces they can see: exact
// kind authorization (Secrets, LimitRanges) denying the kind there, or a
// check that couldn't complete. Naming them leaks nothing.
func (t listTarget) withGaps(gaps []resourceapi.Gap) listTarget {
	// A namespace already a gap of the target (one that doesn't exist, is
	// hidden or is outside the pin) stays in that one gap.
	claimed := map[string]bool{}
	for _, gap := range t.gaps {
		if gap.Namespaces != nil {
			for _, ns := range gap.Namespaces.Names {
				claimed[ns] = true
			}
		}
	}
	out := slices.Clone(t.gaps)
	for _, gap := range gaps {
		if gap.Namespaces == nil || gap.Namespaces.Extent != "" || len(gap.Namespaces.Names) != gap.Namespaces.Count {
			out = append(out, gap)
			continue
		}
		rest := slices.DeleteFunc(slices.Clone(gap.Namespaces.Names), func(ns string) bool { return claimed[ns] })
		if len(rest) > 0 {
			out = append(out, gapOf(gap.Code, rest))
		}
	}
	t.gaps = out
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
	return typedReadCoverageFrom(cache, cache.PermissionResult(), resource, namespaces, clusterScoped, explicit)
}

func typedReadCoverageFrom(cache *k8s.ResourceCache, permissions *k8s.PermissionCheckResult, resource string, namespaces []string, clusterScoped, explicit bool) (typedRead, error) {
	facts := readplan.Typed(cache, permissions, resource, namespaces, clusterScoped)
	plan := typedRead{whole: facts.WholeKindReady}
	readable := namespaces != nil && len(namespaces) == 0 && !clusterScoped
	if readable {
		plan.read = []string{}
	}
	var intended, outside []string
	for _, part := range facts.Partitions {
		if part.Verdict.State == readplan.OutsideCollection {
			outside = append(outside, part.Namespaces...)
			continue
		}
		intended = append(intended, part.Namespaces...)
		if part.Verdict.State == readplan.Readable {
			readable = true
			if part.Extent == readplan.NamespaceSet {
				plan.read = append(plan.read, part.Namespaces...)
			}
		} else {
			plan.gaps = append(plan.gaps, gapOf(part.Verdict.Code, part.Namespaces))
		}
	}
	if explicit && len(outside) > 0 {
		at := 0
		for at < len(plan.gaps) && plan.gaps[at].Code == readreason.RadarCredentialsRejected {
			at++
		}
		plan.gaps = slices.Insert(plan.gaps, at, gapOf(readreason.RadarNamespaceNotCollected, outside))
	} else if facts.CollectionLimited || len(outside) > 0 {
		plan.collected = slices.Compact(slices.Sorted(slices.Values(intended)))
		if plan.collected == nil {
			plan.collected = []string{}
		}
		plan.uncovered = outside
		if len(plan.collected) == 0 {
			plan.gaps = append(plan.gaps, gapOf(readreason.RadarNamespaceNotCollected, outside))
		}
	}
	if readable {
		return plan, nil
	}
	codes := make([]readreason.Code, 0, len(plan.gaps))
	for _, gap := range plan.gaps {
		codes = append(codes, gap.Code)
	}
	code := readreason.Primary(codes)
	message := fmt.Sprintf("Radar's collector does not cover the requested scope for %s", resource)
	switch code {
	case readreason.RadarForbidden:
		message = fmt.Sprintf("Radar's collector cannot list/watch %s", resource)
	case readreason.RadarKindNotCollected:
		message = fmt.Sprintf("Radar is not watching %s", resource)
	case readreason.RadarCredentialsRejected:
		message = fmt.Sprintf("Kubernetes rejected Radar's collector credentials when it checked its access to %s", resource)
		if cache.KindReadinessFor(resource) == k8s.KindUnavailable {
			message = "Kubernetes rejected Radar's collector credentials"
		}
	case readreason.KindSyncPending:
		message = fmt.Sprintf("%s are still loading, please retry shortly", resource)
	case readreason.KindSyncTimedOut:
		message = fmt.Sprintf("%s did not finish loading within the sync deadline; Radar keeps trying, so retry later", resource)
	}
	if cache.KindReadinessFor(resource) == k8s.KindUnavailable {
		plan.gaps = nil
	}
	return plan, &resourceReadFailure{code: code, message: message, gaps: plan.gaps}
}

// placeGaps gives each gap of t's answer its namespaces, since an axis that
// applies is always present. A gap built without namespaces reached whatever
// of the scope nothing else accounts for: all of it when it is the only part,
// the rest of a listed scope, or an unknown part when the rest can't be told.
// A cluster-scoped answer's gaps have no namespace axis. read is the
// namespaces read; nil when none were.
func (t listTarget) placeGaps(gaps []resourceapi.Gap, read []string) []resourceapi.Gap {
	out := make([]resourceapi.Gap, 0, len(gaps))
	if t.clusterScoped {
		for _, gap := range gaps {
			gap.Namespaces = nil
			out = append(out, gap)
		}
		return out
	}
	unplaced := func(gap resourceapi.Gap) bool { return gap.Namespaces == nil && gap.Objects == nil }
	claimed := map[string]bool{}
	for _, ns := range read {
		claimed[ns] = true
	}
	// The rest of the scope is known only when one gap is unplaced and every
	// other gap lists all its namespaces.
	unplacedCount, unlisted := 0, false
	for _, gap := range gaps {
		switch {
		case unplaced(gap):
			unplacedCount++
		case gap.Namespaces != nil:
			unlisted = unlisted || gap.Namespaces.Extent != "" || len(gap.Namespaces.Names) != gap.Namespaces.Count
			for _, ns := range gap.Namespaces.Names {
				claimed[ns] = true
			}
		}
	}
	remainderKnown := unplacedCount == 1 && !unlisted
	scope := t.namespaces
	if t.explicit {
		scope = t.named
	}
	for _, gap := range gaps {
		if !unplaced(gap) {
			out = append(out, gap)
			continue
		}
		switch {
		case len(gaps) == 1 && len(read) == 0:
			gap.Namespaces = resourceapi.AllNames()
		case scope != nil && remainderKnown:
			rest := slices.DeleteFunc(slices.Clone(scope), func(ns string) bool { return claimed[ns] })
			if len(rest) == 0 {
				continue
			}
			gap.Namespaces = resourceapi.Names(rest, resourceapi.RESTSetLimit)
		default:
			gap.Namespaces = resourceapi.UnknownNames()
		}
		out = append(out, gap)
	}
	return out
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

func dynamicPlanFailure(resource string, code readreason.Code) *resourceReadFailure {
	switch code {
	case readreason.ConnectionChanged:
		return readFailuref(code, "Radar's cluster connection changed during this read, so the result was discarded; confirm which cluster Radar is connected to, then retry")
	case readreason.KindSyncTimedOut:
		return readFailuref(code, "%s have not finished loading; Radar keeps trying, so retry later", resource)
	case readreason.RadarCredentialsRejected:
		return readFailuref(code, "Kubernetes rejected Radar's own credentials when it checked access to %s", resource)
	case readreason.RadarForbidden:
		return readFailuref(code, "Radar's collector cannot read %s in the requested scope", resource)
	case readreason.RadarNamespaceNotCollected:
		return readFailuref(code, "Radar's collector does not cover the requested scope for %s", resource)
	case readreason.KindNotWatchable:
		return readFailuref(code, "%s cannot be collected with list/watch", resource)
	case readreason.ReadError:
		return readFailuref(code, "Radar's access check for %s failed, so it can't say what exists there; retry", resource)
	case readreason.RadarKindNotCollected:
		return readFailuref(code, "Radar is not watching %s", resource)
	default:
		return readFailuref(code, "%s are still loading, please retry shortly", resource)
	}
}

// A read must use the same synced stores on both sides; promotion can replace
// namespace informers inside an otherwise unchanged cache handle.
func readCachedDynamicList(cache *k8s.DynamicResourceCache, gvr schema.GroupVersionResource, namespace string, read func(first bool) ([]*unstructured.Unstructured, error)) ([]*unstructured.Unstructured, bool, error) {
	var rows []*unstructured.Unstructured
	for attempt := 0; attempt < 2; attempt++ {
		before, syncedBefore := cache.ReadSource(gvr, namespace)
		var err error
		rows, err = read(attempt == 0)
		after, syncedAfter := cache.ReadSource(gvr, namespace)
		if err != nil {
			return nil, false, err
		}
		if syncedBefore && syncedAfter && before.Same(after) {
			return rows, true, nil
		}
		if !syncedAfter {
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
			items, settled, listErr := readCachedDynamicList(dynamicCache, gvr, "", func(bool) ([]*unstructured.Unstructured, error) {
				return dynamicCache.ListContext(r.Context(), gvr, "")
			})
			if listErr != nil {
				if !current() {
					return dynamicList{}, errReadHandlesChanged
				}
				return dynamicList{}, probeRetryFailure(dynamicCache, gvr, listErr)
			}
			if settled {
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
				if part.Verdict.State == readplan.Gap && slices.ContainsFunc(part.Namespaces, func(ns string) bool { return slices.Contains(unanswered, ns) }) {
					unansweredCode = part.Verdict.Code
				}
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
	// nil means every namespace, which only an unbounded request read. A
	// finite request keeps the namespaces it read: exact kind authorization
	// may have left some of its target out.
	if !narrowed && len(failures) == 0 && namespaces == nil {
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
	facts := readplan.Dynamic(cache, gvr, namespaces, clusterScoped)
	read = []string{}
	var intended, outside []string
	for _, part := range facts.Partitions {
		if part.Verdict.State == readplan.OutsideCollection {
			outside = append(outside, part.Namespaces...)
			continue
		}
		intended = append(intended, part.Namespaces...)
		if part.Verdict.State == readplan.Readable {
			ok = true
			if part.Extent == readplan.NamespaceSet {
				read = append(read, part.Namespaces...)
			} else {
				read = nil
			}
		} else {
			gaps = append(gaps, gapOf(part.Verdict.Code, part.Namespaces))
		}
	}
	if explicit && len(outside) > 0 {
		gaps = append(gaps, gapOf(readreason.RadarNamespaceNotCollected, outside))
	} else if facts.CollectionLimited || len(outside) > 0 {
		collected = slices.Compact(slices.Sorted(slices.Values(intended)))
		if collected == nil {
			collected = []string{}
		}
		uncovered = outside
		if len(intended) == 0 && len(outside) > 0 {
			gaps = append(gaps, gapOf(readreason.RadarNamespaceNotCollected, outside))
		}
	}
	slices.Sort(read)
	return
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
	before, syncedBefore := dynamicCache.ReadSource(gvr, namespace)
	resource, err := dynamicCache.Get(gvr, namespace, name)
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
		after, syncedAfter := dynamicCache.ReadSource(gvr, namespace)
		if failure := namespaceReadFailure(dynamicCache, gvr, namespace); failure != nil {
			return nil, failure
		}
		if !syncedBefore || !syncedAfter || !before.Same(after) {
			before, syncedBefore = dynamicCache.ReadSource(gvr, namespace)
			resource, err = dynamicCache.Get(gvr, namespace, name)
			after, syncedAfter = dynamicCache.ReadSource(gvr, namespace)
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
				if !syncedBefore || !syncedAfter || !before.Same(after) {
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
