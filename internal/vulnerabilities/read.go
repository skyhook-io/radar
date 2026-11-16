package vulnerabilities

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
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

// ErrConnectionChanged: Radar's cluster connection was replaced while the
// read ran, so its inputs may come from two clusters.
var ErrConnectionChanged = errors.New("Radar's cluster connection changed during this read (a context switch or reconnect); confirm which cluster Radar is connected to, then retry")

// syncWait bounds how long a read waits for the report watch to sync.
const syncWait = 10 * time.Second

// maxConcurrentLists bounds the per-namespace report reads in flight.
const maxConcurrentLists = 8

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
	// CanListPods is the caller's list access to Pods in a namespace, which
	// the inventory of what runs is read with; nil allows all.
	CanListPods func(namespace string) bool
	View        View
	Filter      Filter
}

// Served is whether the connected cluster serves the report kind the rollup
// reads: false without Trivy Operator, or before discovery has run.
func Served() bool {
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		return false
	}
	_, ok := discovery.GetGVRWithGroup("VulnerabilityReport", aicontext.TrivyGroup)
	return ok
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
	// and every running image as unscanned. Every namespace's wait shares
	// one deadline, so queued namespaces don't add their waits up.
	// The deadline covers starting the watch too, which can wait on
	// discovery.
	var (
		readyOnce   sync.Once
		readyCtx    context.Context
		cancelReady context.CancelFunc = func() {}
	)
	defer func() { cancelReady() }()
	list := func(ns string) ([]*unstructured.Unstructured, error) {
		readyOnce.Do(func() { readyCtx, cancelReady = context.WithTimeout(ctx, syncWait) })
		notReady := func(err error) error {
			if ctx.Err() == nil && readyCtx.Err() != nil {
				return ErrNotReady
			}
			return err
		}
		if err := dynamic.EnsureWatchingContext(readyCtx, gvr, ns); err != nil {
			return nil, notReady(err)
		}
		if !dynamic.WaitForSyncContext(readyCtx, gvr, ns) {
			return nil, notReady(ctx.Err())
		}
		return dynamic.ListReadOnlyContext(ctx, gvr, ns)
	}

	if req.NamespaceCheckFailed {
		return empty(req.View, resourceapi.Gap{Code: readreason.UserPermissionCheckFailed, Namespaces: resourceapi.UnknownNames()}), nil
	}
	if req.Namespaces != nil && len(req.Namespaces) == 0 {
		return empty(req.View, resourceapi.Gap{Code: readreason.UserNoNamespaceAccess}), nil
	}

	namespaces, namespacesKnown := clusterNamespaces(cache)
	var gaps []resourceapi.Gap
	if !namespacesKnown && req.Namespaces == nil && req.ReportAccess != nil && !clusterWide(req.ReportAccess, gvr) {
		// Radar can't list namespaces to check access in each; check the
		// ones that hold reports and say the scope is unknown.
		all, err := list("")
		if err != nil {
			return Response{}, err
		}
		namespaces = reportNamespaces(all)
		gaps = append(gaps, resourceapi.Gap{Code: readreason.RadarKindNotCollected, Kinds: namespaceKind, Namespaces: resourceapi.UnknownNames()})
	}
	readable, accessGaps := readableNamespaces(gvr, req, namespaces)
	gaps = append(gaps, accessGaps...)

	// A namespace whose reports Radar can't read is a gap, coded by why; the
	// request fails only when nothing is left to answer from.
	var items []*unstructured.Unstructured
	failed := map[readreason.Code][]string{}
	var lastErr error
	scope := scopeNamespaces(readable)
	type result struct {
		ns    string
		items []*unstructured.Unstructured
		err   error
	}
	// Each namespace's read may wait for its own watch to sync; reading
	// them one at a time would add those waits up.
	results := make([]result, len(scope))
	sem := make(chan struct{}, maxConcurrentLists)
	var wg sync.WaitGroup
	for i, ns := range scope {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			got, err := list(ns)
			results[i] = result{ns: ns, items: got, err: err}
		}()
	}
	wg.Wait()
	for _, res := range results {
		if err := res.err; err != nil {
			if res.ns == "" || ctx.Err() != nil {
				return Response{}, err
			}
			code := readreason.KindSyncPending
			if !errors.Is(err, ErrNotReady) {
				code = readreason.ReadError
				if c, ok := readplan.CollectorErrorCode(err); ok {
					code = c
				}
			}
			failed[code] = append(failed[code], res.ns)
			lastErr = err
			continue
		}
		items = append(items, res.items...)
	}
	if len(failed) > 0 {
		var all []string
		for code, namespaces := range failed {
			gaps = appendGap(gaps, reportKind, code, namespaces)
			all = append(all, namespaces...)
		}
		if len(all) == len(readable) {
			return Response{}, lastErr
		}
		readable = slices.DeleteFunc(slices.Clone(readable), func(ns string) bool { return slices.Contains(all, ns) })
	}

	// Each join input answers only where Radar collects it and it has
	// loaded. Elsewhere reports are withheld and the namespaces named: a
	// missing report would otherwise read as an unscanned image, a missing
	// Pod or CronJob as an inactive report.
	readable, gaps = narrowToDynamic(readplan.Dynamic(dynamic, gvr, readable, false), readable, gaps)
	podRead := cache.AssessKindRead("pods", readable)
	if podRead.Readiness == k8score.KindUnavailable {
		// Without Pods nothing is known to run: every report would read as
		// inactive. Say so instead of retrying a cache that won't load.
		return empty(req.View, resourceapi.Gap{Code: readreason.RadarKindNotCollected, Kinds: podKind, Namespaces: resourceapi.UnknownNames()}), nil
	}
	readable, gaps, ok = narrowToTyped(podKind, podRead, readable, gaps)
	if !ok {
		return Response{}, ErrNotReady
	}
	items = filterNamespaces(items, readable)
	cronRead := cache.AssessKindRead("cronjobs", readable)
	cronReadable, cronGaps, cronOK := narrowToTyped(cronJobKind, cronRead, readable, nil)
	jobRead := cache.AssessKindRead("jobs", readable)
	jobReadable, jobGaps, jobOK := narrowToTyped(jobKind, jobRead, readable, nil)
	rsRead := cache.AssessKindRead("replicasets", readable)
	rsReadable, rsGaps, rsOK := narrowToTyped(replicaSetKind, rsRead, readable, nil)

	live, ready := liveTargets(cache, readable, req.CanRead, req.CanListPods)
	if !ready {
		return Response{}, ErrNotReady
	}
	// A namespace with no running Pod must still be one the caller may list
	// Pods in, or "nothing runs there" is a claim it can't make.
	if req.CanListPods != nil {
		check := readable
		if check == nil {
			check = reportNamespaces(items)
		}
		for _, ns := range check {
			if !slices.Contains(live.podDenied, ns) && !req.CanListPods(ns) {
				live.podDenied = append(live.podDenied, ns)
			}
		}
		slices.Sort(live.podDenied)
	}
	targets := live.targets
	// Where the caller can't read Pods, what runs is unknown: its reports
	// would otherwise all read as inactive. Reports about an object the
	// caller can't read are withheld the same way.
	if len(live.podDenied) > 0 {
		items = withoutNamespaces(items, live.podDenied)
		targets = slices.DeleteFunc(targets, func(t target) bool { return slices.Contains(live.podDenied, t.key.namespace) })
		gaps = appendGap(gaps, podKind, readreason.UserForbidden, live.podDenied)
	}
	// Where CronJobs aren't read, a CronJob between runs is unknown whether
	// or not it has a report yet.
	if !cronOK || len(cronGaps) > 0 {
		cronUnknown := func(ns string) bool { return !cronOK || (cronReadable != nil && !slices.Contains(cronReadable, ns)) }
		items = slices.DeleteFunc(items, func(u *unstructured.Unstructured) bool {
			r, ok := reportHeader(u)
			return ok && r.owner.kind == "CronJob" && cronUnknown(r.owner.namespace)
		})
		// Their running Jobs' images are withheld with them, not unscanned.
		targets = slices.DeleteFunc(targets, func(t target) bool { return t.key.kind == "CronJob" && cronUnknown(t.key.namespace) })
		if cronOK {
			gaps = append(gaps, cronGaps...)
		} else {
			gaps = append(gaps, kindGap(cronJobKind, cronRead))
		}
	}
	// Where Jobs aren't read, a Job's Pods can't be traced to the CronJob
	// that Trivy scanned them through: its report would read as inactive
	// and the running image as unscanned. Both are withheld instead.
	if !jobOK || len(jobGaps) > 0 {
		jobUnknown := func(ns string) bool { return !jobOK || (jobReadable != nil && !slices.Contains(jobReadable, ns)) }
		var namespaces []string
		targets = slices.DeleteFunc(targets, func(t target) bool {
			if t.key.kind == "Job" && jobUnknown(t.key.namespace) {
				if !slices.Contains(namespaces, t.key.namespace) {
					namespaces = append(namespaces, t.key.namespace)
				}
				return true
			}
			return false
		})
		// A CronJob that still has its own (unsuspended) target keeps its
		// report; others there may be what the withheld Pods run.
		scheduled := map[ownerKey]bool{}
		for _, t := range targets {
			if t.key.kind == "CronJob" {
				scheduled[t.key.owner()] = true
			}
		}
		items = slices.DeleteFunc(items, func(u *unstructured.Unstructured) bool {
			r, ok := reportHeader(u)
			if !ok || !slices.Contains(namespaces, r.owner.namespace) {
				return false
			}
			return r.owner.kind == "Job" || (r.owner.kind == "CronJob" && !scheduled[r.owner])
		})
		if len(namespaces) > 0 {
			if jobOK {
				gaps = append(gaps, jobGaps...)
			} else {
				gaps = append(gaps, kindGap(jobKind, jobRead))
			}
		}
	}
	// Where ReplicaSets aren't read, a Deployment's Pods are named by their
	// ReplicaSet. The findings hold, but the workloads are not the ones a
	// person would name, so the answer says it is partial.
	if !rsOK || len(rsGaps) > 0 {
		rsUnknown := func(ns string) bool { return !rsOK || (rsReadable != nil && !slices.Contains(rsReadable, ns)) }
		if slices.ContainsFunc(targets, func(t target) bool { return t.key.kind == "ReplicaSet" && rsUnknown(t.key.namespace) }) {
			if rsOK {
				gaps = append(gaps, rsGaps...)
			} else {
				gaps = append(gaps, kindGap(replicaSetKind, rsRead))
			}
		}
	}
	// A running object the caller can't read is withheld whether or not it
	// has a report yet: without the gap, an unscanned one would just vanish.
	if len(live.withheld) > 0 {
		var namespaces []string
		for owner := range live.withheld {
			if !slices.Contains(namespaces, owner.namespace) {
				namespaces = append(namespaces, owner.namespace)
			}
		}
		items = slices.DeleteFunc(items, func(u *unstructured.Unstructured) bool {
			r, ok := reportHeader(u)
			return ok && live.withheld[r.owner]
		})
		slices.Sort(namespaces)
		gaps = appendGap(gaps, resourceapi.Kinds(live.deniedKinds, 64), readreason.UserForbidden, namespaces)
	}
	resp, err := build(ctx, items, targets, req.View, req.Filter)
	if err != nil {
		return Response{}, err
	}
	if cache != k8s.GetResourceCache() || dynamic != k8s.GetDynamicResourceCache() || discovery != k8s.GetResourceDiscovery() {
		return Response{}, ErrConnectionChanged
	}
	resp.Coverage.Gaps = append([]resourceapi.Gap{}, gaps...)
	c := resp.Coverage
	resp.Coverage.Complete = len(gaps) == 0 && !c.Truncated && c.RowsOmitted == 0 && c.CleanOmitted == 0 && c.UnscannedOmitted == 0
	return resp, nil
}

// readableNamespaces is where the caller may list the report kind: the whole
// ceiling with a cluster-wide grant (or with auth off), else the namespaces a
// per-namespace check allowed. Asked for every namespace without the
// cluster-wide grant, it checks the cluster's namespaces, which the caller
// may see.
// clusterWide is whether the caller may list the reports in every namespace,
// when no namespace list is needed to check access.
func clusterWide(access *search.DynamicKindAccess, gvr schema.GroupVersionResource) bool {
	allowed, _ := access.ClusterWide(gvr.Group, gvr.Resource)
	return allowed
}

func readableNamespaces(gvr schema.GroupVersionResource, req Request, clusterNamespaces []string) (readable []string, gaps []resourceapi.Gap) {
	access := req.ReportAccess
	if access == nil {
		return req.Namespaces, nil
	}
	if clusterWide(access, gvr) {
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
		gaps = append(gaps, gap(reportKind, readreason.UserForbidden, denied))
	}
	if len(unchecked) > 0 {
		gaps = append(gaps, gap(reportKind, readreason.UserPermissionCheckFailed, unchecked))
	}
	if allowed == nil {
		allowed = []string{}
	}
	return allowed, gaps
}

// clusterNamespaces lists the cluster's namespaces; known is false when
// Radar doesn't collect them or hasn't loaded them.
func clusterNamespaces(cache *k8s.ResourceCache) (namespaces []string, known bool) {
	if cache == nil || cache.Namespaces() == nil || !cache.IsKindReady("namespaces") {
		return []string{}, false
	}
	list, err := cache.Namespaces().List(labels.Everything())
	if err != nil {
		return []string{}, false
	}
	out := make([]string, 0, len(list))
	for _, ns := range list {
		out = append(out, ns.Name)
	}
	slices.Sort(out)
	return out, true
}

func filterNamespaces(items []*unstructured.Unstructured, namespaces []string) []*unstructured.Unstructured {
	if namespaces == nil {
		return items
	}
	out := make([]*unstructured.Unstructured, 0, len(items))
	for _, u := range items {
		if slices.Contains(namespaces, u.GetNamespace()) {
			out = append(out, u)
		}
	}
	return out
}

// kindGap is the gap for a join input that can't be read anywhere: one
// Radar doesn't collect, or one still loading.
func kindGap(kind *resourceapi.KindSet, a k8score.KindReadAssessment) resourceapi.Gap {
	if a.Readiness == k8score.KindUnavailable {
		return resourceapi.Gap{Code: readreason.RadarKindNotCollected, Kinds: kind, Namespaces: resourceapi.UnknownNames()}
	}
	return resourceapi.Gap{Code: readreason.KindSyncPending, Kinds: kind, Namespaces: resourceapi.UnknownNames()}
}

// narrowToTyped narrows readable to the namespaces where a typed kind has
// loaded, naming the others. ok is false when the kind isn't collected at
// all or hasn't loaded anywhere.
func narrowToTyped(kind *resourceapi.KindSet, a k8score.KindReadAssessment, readable []string, gaps []resourceapi.Gap) ([]string, []resourceapi.Gap, bool) {
	switch {
	case a.Readiness == k8score.KindUnavailable:
		return readable, gaps, false
	case a.AllNamespaces:
		return readable, gaps, a.Readiness == k8score.KindReady
	}
	gaps = appendGap(gaps, kind, readreason.RadarNamespaceNotCollected, a.Uncovered)
	if readable == nil {
		// Radar collects the kind only in some namespaces; the rest are unknown.
		gaps = append(gaps, resourceapi.Gap{Code: readreason.RadarNamespaceNotCollected, Kinds: kind, Namespaces: resourceapi.UnknownNames()})
	}
	gaps = appendGap(gaps, kind, readreason.KindSyncPending, a.Loading)
	gaps = appendGap(gaps, kind, readreason.KindSyncTimedOut, a.Failed)
	return a.Read, gaps, true
}

// narrowToDynamic is narrowToTyped for the report kind, from its read plan.
func narrowToDynamic(plan readplan.Plan, readable []string, gaps []resourceapi.Gap) ([]string, []resourceapi.Gap) {
	var read []string
	whole := false
	for _, part := range plan.Partitions {
		switch {
		case part.Verdict.State == readplan.Readable && part.Extent == readplan.NamespaceSet:
			read = append(read, part.Namespaces...)
		case part.Verdict.State == readplan.Readable:
			whole = true
		case part.Extent == readplan.NamespaceSet:
			gaps = appendGap(gaps, reportKind, part.Verdict.Code, part.Namespaces)
		default:
			gaps = append(gaps, resourceapi.Gap{Code: part.Verdict.Code, Kinds: reportKind, Namespaces: resourceapi.UnknownNames()})
		}
	}
	if whole {
		return readable, gaps
	}
	if plan.CollectionLimited {
		// Radar collects reports only in some namespaces; the rest are unknown.
		gaps = append(gaps, resourceapi.Gap{Code: readreason.RadarNamespaceNotCollected, Kinds: reportKind, Namespaces: resourceapi.UnknownNames()})
	}
	if read == nil {
		read = []string{}
	}
	slices.Sort(read)
	return read, gaps
}

func reportNamespaces(items []*unstructured.Unstructured) []string {
	var out []string
	for _, u := range items {
		if ns := u.GetNamespace(); !slices.Contains(out, ns) {
			out = append(out, ns)
		}
	}
	slices.Sort(out)
	return out
}

// Each gap names what Radar didn't read: the reports themselves, or a kind
// the join needs.
var (
	reportKind     = kinds(resourceapi.KindRef{Kind: "VulnerabilityReport", Group: aicontext.TrivyGroup})
	podKind        = kinds(resourceapi.KindRef{Kind: "Pod"})
	cronJobKind    = kinds(resourceapi.KindRef{Kind: "CronJob", Group: "batch"})
	jobKind        = kinds(resourceapi.KindRef{Kind: "Job", Group: "batch"})
	replicaSetKind = kinds(resourceapi.KindRef{Kind: "ReplicaSet", Group: "apps"})
	namespaceKind  = kinds(resourceapi.KindRef{Kind: "Namespace"})
)

func kinds(k ...resourceapi.KindRef) *resourceapi.KindSet { return resourceapi.Kinds(k, 64) }

// appendGap adds a gap for namespaces, merging into an existing gap with the
// same code and kinds.
func appendGap(gaps []resourceapi.Gap, kind *resourceapi.KindSet, code readreason.Code, namespaces []string) []resourceapi.Gap {
	if len(namespaces) == 0 {
		return gaps
	}
	for i := range gaps {
		// A gap past the name cap only counts its namespaces: merging into it
		// would replace that count with the names it no longer lists.
		if gaps[i].Code == code && gaps[i].Namespaces != nil && gaps[i].Namespaces.Extent == "" &&
			len(gaps[i].Namespaces.Names) == gaps[i].Namespaces.Count && reflect.DeepEqual(gaps[i].Kinds, kind) {
			merged := slices.Compact(slices.Sorted(slices.Values(slices.Concat(gaps[i].Namespaces.Names, namespaces))))
			gaps[i].Namespaces = resourceapi.Names(merged, 64)
			return gaps
		}
	}
	return append(gaps, gap(kind, code, namespaces))
}

func gap(kind *resourceapi.KindSet, code readreason.Code, namespaces []string) resourceapi.Gap {
	g := resourceapi.Gap{Code: code, Kinds: kind, Namespaces: resourceapi.Names(namespaces, 64)}
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
