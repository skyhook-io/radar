package server

import (
	"context"
	"net/http"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// kueueQueueNameLabel marks a job Kueue queues; Kueue also manages jobs
// without it when configured to, which the owned-Workload check catches.
const kueueQueueNameLabel = "kueue.x-k8s.io/queue-name"

// kueueHistoryKinds are the kinds whose rows tell a queued workload's
// admission story: its Workload (owned by the workload) and the
// ProvisioningRequests its admission checks create (owned by the Workload).
var kueueHistoryKinds = []resourceapi.KindRef{
	{Kind: "Workload", Group: "kueue.x-k8s.io"},
	{Kind: "ProvisioningRequest", Group: "autoscaling.x-k8s.io"},
}

// kueueHistoryReads reports, for a workload Kueue queues, what keeps its
// admission story out of the history: kinds the caller may not list in the
// namespace, and kinds Radar doesn't collect there. The gaps follow from
// access and collection, not from which rows were dropped, so they read the
// same on every page and say nothing about whether hidden rows exist.
// watchedSince is when Radar began watching the kinds it does collect, when
// that is later than this process's observation start.
func (s *Server) kueueHistoryReads(r *http.Request, namespace string, live liveIdentity) (gaps []resourceapi.Gap, watchedSince time.Time) {
	discovery := k8s.GetResourceDiscovery()
	dynamicCache := k8s.GetDynamicResourceCache()
	if discovery == nil || dynamicCache == nil || live.Object == nil {
		return nil, time.Time{}
	}
	type servedKind struct {
		ref resourceapi.KindRef
		gvr schema.GroupVersionResource
	}
	var served []servedKind
	for _, ref := range kueueHistoryKinds {
		if gvr, ok := discovery.GetGVRWithGroup(ref.Kind, ref.Group); ok {
			served = append(served, servedKind{ref, gvr})
		}
	}
	if len(served) == 0 || served[0].ref.Kind != "Workload" || !kueueManagedRoot(dynamicCache, served[0].gvr, namespace, live) {
		return nil, time.Time{}
	}
	reads := make([]kueueKindRead, 0, len(served))
	for _, kind := range served {
		read := kueueKindRead{ref: kind.ref}
		read.allowed, read.authoritative = s.canReadDecision(r, kind.gvr.Group, kind.gvr.Resource, namespace, "list")
		if read.allowed {
			for _, part := range readplan.Dynamic(dynamicCache, kind.gvr, []string{namespace}, false).Partitions {
				if part.Verdict.State != readplan.Readable {
					read.uncollected = append(read.uncollected, part.Verdict.Code)
				}
			}
			read.watchedSince, _ = dynamicCache.WatchedSince(kind.gvr)
		}
		reads = append(reads, read)
	}
	return kueueReadGaps(namespace, reads)
}

// kueueKindRead is what kueueReadGaps needs to know about one kind: whether
// the caller may list it in the namespace (authoritative false when the check
// itself failed), why Radar can't serve it there, if it can't, and since when
// Radar has watched it.
type kueueKindRead struct {
	ref                    resourceapi.KindRef
	allowed, authoritative bool
	uncollected            []readreason.Code
	watchedSince           time.Time
}

// kueueReadGaps groups the kinds the caller can't get into one gap per
// reason, and returns the latest watch start among the kinds it can.
func kueueReadGaps(namespace string, reads []kueueKindRead) (gaps []resourceapi.Gap, watchedSince time.Time) {
	byCode := map[readreason.Code][]resourceapi.KindRef{}
	var codes []readreason.Code
	note := func(code readreason.Code, ref resourceapi.KindRef) {
		if _, ok := byCode[code]; !ok {
			codes = append(codes, code)
		}
		byCode[code] = append(byCode[code], ref)
	}
	for _, read := range reads {
		switch {
		case !read.authoritative:
			note(readreason.UserPermissionCheckFailed, read.ref)
		case !read.allowed:
			note(readreason.UserForbidden, read.ref)
		case len(read.uncollected) > 0:
			for _, code := range read.uncollected {
				note(code, read.ref)
			}
		case read.watchedSince.After(watchedSince):
			watchedSince = read.watchedSince
		}
	}
	for _, code := range codes {
		kinds := byCode[code]
		gaps = append(gaps, resourceapi.Gap{
			Code:       code,
			Kinds:      &resourceapi.KindSet{Count: len(kinds), Names: kinds},
			Namespaces: &resourceapi.NameSet{Count: 1, Names: []string{namespace}},
		})
	}
	return gaps, watchedSince
}

// kueueManagedRoot reports a workload Kueue queues: it carries the queue
// label, or Radar's cache holds a Workload it controls.
func kueueManagedRoot(dynamicCache *k8s.DynamicResourceCache, workloads schema.GroupVersionResource, namespace string, live liveIdentity) bool {
	if _, ok := live.Object.GetLabels()[kueueQueueNameLabel]; ok {
		return true
	}
	if live.UID == "" {
		return false
	}
	owned, ok := dynamicCache.ReadableIndexed(workloads, namespace, k8score.OwnerUIDIndex, live.UID)
	if !ok {
		return false
	}
	return slices.ContainsFunc(owned, func(u *unstructured.Unstructured) bool {
		ref := metav1.GetControllerOf(u)
		return ref != nil && string(ref.UID) == live.UID
	})
}

// workloadHistoryCoverage says whether Radar has held every change it could
// observe to the workload's resources since the workload existed. The window
// starts at the live workload's creation. When the store also holds an
// earlier incarnation under the same key, or the workload is gone, that
// start is unknown, so the window starts at the zero time. collectedSince is
// a later start for kinds the history depends on (see kueueHistoryReads).
func workloadHistoryCoverage(ctx context.Context, store timeline.EventStore, clusterContext string, key resourceid.Ref, live liveIdentity, collectedSince time.Time, readsAllNamespaces bool) *issuesapi.HistoryCoverage {
	var windowStart time.Time
	if live.Object != nil {
		windowStart = live.Object.GetCreationTimestamp().Time
	}
	coverage := meaningfulchanges.HistoryCoverageSince(windowStart)
	if !coverage.Incomplete && !windowStart.IsZero() && hasEarlierIncarnation(ctx, store, clusterContext, key, live.UID) {
		coverage = meaningfulchanges.HistoryCoverageSince(time.Time{})
	}
	if collectedSince.After(windowStart) && collectedSince.After(coverage.CompleteSince) {
		if !slices.Contains(coverage.Reasons, issuesapi.HistoryObservationStartedLate) {
			coverage.Reasons = append(coverage.Reasons, issuesapi.HistoryObservationStartedLate)
		}
		coverage.Incomplete = true
		coverage.CompleteSince = collectedSince.Truncate(time.Second).Add(time.Second).UTC()
	}
	return meaningfulchanges.HistoryCoverageFor(coverage, readsAllNamespaces)
}

// hasEarlierIncarnation reports a recorded UID under the key other than the
// live one. A failed read reports one: the window then can't be dated.
func hasEarlierIncarnation(ctx context.Context, store timeline.EventStore, clusterContext string, key resourceid.Ref, liveUID string) bool {
	ids, err := store.Identities(ctx, timeline.IdentityQuery{ClusterContext: clusterContext, Namespace: key.Namespace, Ref: &key}, 8)
	if err != nil {
		return true
	}
	return slices.ContainsFunc(ids, func(id timeline.Identity) bool { return id.UID != "" && id.UID != liveUID })
}
