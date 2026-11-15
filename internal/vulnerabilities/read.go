package vulnerabilities

import (
	"context"
	"errors"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/search"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// ErrNotInstalled: the cluster serves no VulnerabilityReport kind.
var ErrNotInstalled = errors.New("no vulnerability scanner reports in this cluster: Trivy Operator's VulnerabilityReport kind isn't served")

// ErrNotReady: the caches the rollup joins haven't synced yet.
var ErrNotReady = errors.New("vulnerability reports aren't ready yet: Radar is still loading the cluster")

// syncWait bounds how long a read waits for the report watch to sync.
const syncWait = 10 * time.Second

// Request is one read, with the caller's access as the calling surface
// checks it.
type Request struct {
	// Namespaces is the caller's namespace ceiling; nil means every
	// namespace.
	Namespaces []string
	// NamespaceCheckFailed is true when the caller's namespace access couldn't
	// be determined.
	NamespaceCheckFailed bool
	// ReportAccess authorizes the report kind itself, which needs the
	// caller's own list permission (as a Secret does); nil when auth is off.
	ReportAccess *search.DynamicKindAccess
	// CanRead is the caller's access to a workload kind in a namespace, for
	// the scanned objects and workloads the answer names; nil allows all.
	CanRead func(group, kind, namespace string) bool
	View    View
	Filter  Filter
}

// Read rolls up the reports the caller may read, joined with what runs in
// those namespaces.
func Read(ctx context.Context, cache *k8s.ResourceCache, req Request) (Response, error) {
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		return Response{}, ErrNotReady
	}
	gvr, ok := discovery.GetGVRWithGroup("VulnerabilityReport", aicontext.TrivyGroup)
	if !ok {
		return Response{}, ErrNotInstalled
	}
	dynamic := k8s.GetDynamicResourceCache()
	if dynamic == nil || cache == nil {
		return Response{}, ErrNotReady
	}
	// A report list read before its watch synced would read as no reports,
	// and every running image as unscanned.
	list := func(ns string) ([]*unstructured.Unstructured, error) {
		if err := dynamic.EnsureWatchingContext(ctx, gvr, ns); err != nil {
			return nil, err
		}
		waitCtx, cancel := context.WithTimeout(ctx, syncWait)
		synced := dynamic.WaitForSyncContext(waitCtx, gvr, ns)
		cancel()
		if !synced {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, ErrNotReady
		}
		return dynamic.ListReadOnlyContext(ctx, gvr, ns)
	}

	if req.NamespaceCheckFailed {
		return empty(req.View, resourceapi.Gap{Code: readreason.UserPermissionCheckFailed, Namespaces: resourceapi.UnknownNames()}), nil
	}
	if req.Namespaces != nil && len(req.Namespaces) == 0 {
		return empty(req.View, resourceapi.Gap{Code: readreason.UserNoNamespaceAccess}), nil
	}

	readable, gaps := readableNamespaces(gvr, req, clusterNamespaces(cache))

	// What runs is known only where Radar collects Pods and they have
	// loaded; reports elsewhere are withheld and named, never read as
	// inactive.
	pods := cache.AssessKindRead("pods", readable)
	switch {
	case pods.Readiness == k8score.KindUnavailable:
		return Response{}, ErrNotReady
	case pods.AllNamespaces:
		if pods.Readiness != k8score.KindReady {
			return Response{}, ErrNotReady
		}
	default:
		gaps = appendGap(gaps, readreason.RadarNamespaceNotCollected, pods.Uncovered)
		if readable == nil {
			// Radar collects Pods only in some namespaces; the rest are unknown.
			gaps = append(gaps, resourceapi.Gap{Code: readreason.RadarNamespaceNotCollected, Namespaces: resourceapi.UnknownNames()})
		}
		gaps = appendGap(gaps, readreason.KindSyncPending, pods.Loading)
		gaps = appendGap(gaps, readreason.KindSyncTimedOut, pods.Failed)
		readable = pods.Read
	}

	var items []*unstructured.Unstructured
	for _, ns := range scopeNamespaces(readable) {
		got, err := list(ns)
		if err != nil {
			return Response{}, err
		}
		items = append(items, got...)
	}

	live, ready := liveTargets(cache, readable, req.CanRead)
	if !ready {
		return Response{}, ErrNotReady
	}
	targets := live.targets
	// Where the caller can't read Pods, what runs is unknown: its reports
	// would otherwise all read as inactive. Reports about an object the
	// caller can't read are withheld the same way.
	if len(live.podDenied) > 0 {
		items = withoutNamespaces(items, live.podDenied)
		gaps = appendGap(gaps, readreason.UserForbidden, live.podDenied)
	}
	if live.cronJobsUnknown {
		before := len(items)
		items = slices.DeleteFunc(items, func(u *unstructured.Unstructured) bool {
			r, ok := reportHeader(u)
			return ok && r.owner.kind == "CronJob"
		})
		if len(items) < before {
			gaps = append(gaps, resourceapi.Gap{Code: readreason.KindSyncPending, Namespaces: resourceapi.UnknownNames()})
		}
	}
	if len(live.withheld) > 0 {
		var namespaces []string
		items = slices.DeleteFunc(items, func(u *unstructured.Unstructured) bool {
			r, ok := reportHeader(u)
			if ok && live.withheld[r.owner] {
				if !slices.Contains(namespaces, r.owner.namespace) {
					namespaces = append(namespaces, r.owner.namespace)
				}
				return true
			}
			return false
		})
		slices.Sort(namespaces)
		gaps = appendGap(gaps, readreason.UserForbidden, namespaces)
	}
	resp, err := build(ctx, items, targets, req.View, req.Filter)
	if err != nil {
		return Response{}, err
	}
	resp.Coverage.Gaps = append([]resourceapi.Gap{}, gaps...)
	resp.Coverage.Complete = len(gaps) == 0 && !resp.Coverage.Truncated && resp.Coverage.RowsOmitted == 0
	return resp, nil
}

// readableNamespaces is where the caller may list the report kind: the whole
// ceiling with a cluster-wide grant (or with auth off), else the namespaces a
// per-namespace check allowed. Asked for every namespace without the
// cluster-wide grant, it checks the cluster's namespaces, which the caller
// may see.
func readableNamespaces(gvr schema.GroupVersionResource, req Request, clusterNamespaces []string) (readable []string, gaps []resourceapi.Gap) {
	access := req.ReportAccess
	if access == nil {
		return req.Namespaces, nil
	}
	if allowed, _ := access.ClusterWide(gvr.Group, gvr.Resource); allowed {
		return req.Namespaces, nil
	}
	candidates := req.Namespaces
	if candidates == nil {
		candidates = clusterNamespaces
	}
	allowed, unchecked := access.InNamespaces(gvr.Group, gvr.Resource, candidates)
	var denied []string
	for _, ns := range candidates {
		if !slices.Contains(allowed, ns) && !slices.Contains(unchecked, ns) {
			denied = append(denied, ns)
		}
	}
	if len(denied) > 0 {
		gaps = append(gaps, gap(readreason.UserForbidden, denied))
	}
	if len(unchecked) > 0 {
		gaps = append(gaps, gap(readreason.UserPermissionCheckFailed, unchecked))
	}
	if allowed == nil {
		allowed = []string{}
	}
	return allowed, gaps
}

func clusterNamespaces(cache *k8s.ResourceCache) []string {
	if cache == nil || cache.Namespaces() == nil {
		return []string{}
	}
	list, err := cache.Namespaces().List(labels.Everything())
	if err != nil {
		return []string{}
	}
	out := make([]string, 0, len(list))
	for _, ns := range list {
		out = append(out, ns.Name)
	}
	slices.Sort(out)
	return out
}

// appendGap adds a gap for namespaces, merging into an existing gap with the
// same code.
func appendGap(gaps []resourceapi.Gap, code readreason.Code, namespaces []string) []resourceapi.Gap {
	if len(namespaces) == 0 {
		return gaps
	}
	for i := range gaps {
		if gaps[i].Code == code && gaps[i].Namespaces != nil {
			merged := slices.Compact(slices.Sorted(slices.Values(slices.Concat(gaps[i].Namespaces.Names, namespaces))))
			gaps[i].Namespaces = resourceapi.Names(merged, 64)
			return gaps
		}
	}
	return append(gaps, gap(code, namespaces))
}

func gap(code readreason.Code, namespaces []string) resourceapi.Gap {
	g := resourceapi.Gap{Code: code, Namespaces: resourceapi.Names(namespaces, 64)}
	if action, ok := readreason.ActionFor(code); ok {
		g.Action = action
	}
	return g
}

func empty(view View, g resourceapi.Gap) Response {
	if action, ok := readreason.ActionFor(g.Code); ok {
		g.Action = action
	}
	resp, _ := build(context.Background(), nil, nil, view, Filter{})
	resp.Coverage = Coverage{Gaps: []resourceapi.Gap{g}}
	return resp
}

func withoutNamespaces(items []*unstructured.Unstructured, namespaces []string) []*unstructured.Unstructured {
	out := make([]*unstructured.Unstructured, 0, len(items))
	for _, u := range items {
		if !slices.Contains(namespaces, u.GetNamespace()) {
			out = append(out, u)
		}
	}
	return out
}
