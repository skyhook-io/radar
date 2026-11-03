package issues

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func (p *CacheProvider) kueueInventory(group, kind string, namespaces []string) kueueInventory {
	if p == nil || p.discovery == nil || p.dynamic == nil {
		return kueueInventory{}
	}
	apiResource, ok := p.discovery.GetResourceWithGroup(kind, group)
	if !ok {
		if p.discovery.GroupHadPartialDiscovery(group) {
			return kueueInventory{Gap: issuesapi.UncheckedReasonReadFailed}
		}
		return kueueInventory{}
	}
	gvr := schema.GroupVersionResource{Group: apiResource.Group, Version: apiResource.Version, Resource: apiResource.Name}
	if err := p.dynamic.EnsureWatching(gvr); err != nil {
		if p.dynamic.Observation(gvr).State == k8score.DynamicObservationDenied {
			return kueueInventory{Gap: issuesapi.UncheckedReasonCollectorAccessDenied}
		}
		return kueueInventory{Gap: issuesapi.UncheckedReasonReadFailed}
	}
	scope := namespaces
	if !apiResource.Namespaced {
		scope = []string{""}
	}
	if gap := p.dynamicSyncGap(gvr, scope); gap != "" {
		return kueueInventory{Gap: gap}
	}
	// Read-only: the detector only reads these objects within this compose.
	var items []*unstructured.Unstructured
	switch {
	case !apiResource.Namespaced:
		list, err := p.dynamic.ListReadOnly(gvr, "")
		if err != nil {
			return kueueInventory{Gap: issuesapi.UncheckedReasonReadFailed}
		}
		items = list
	case len(namespaces) == 0:
		// Only callers authorized cluster-wide compose without namespaces.
		list, err := p.dynamic.ListWatchedReadOnly(gvr)
		if err != nil {
			return kueueInventory{Gap: issuesapi.UncheckedReasonReadFailed}
		}
		items = list
	default:
		for _, ns := range namespaces {
			list, err := p.dynamic.ListReadOnly(gvr, ns)
			if err != nil {
				return kueueInventory{Gap: issuesapi.UncheckedReasonReadFailed}
			}
			items = append(items, list...)
		}
	}
	return kueueInventory{Items: items, OK: true}
}

func (p *CacheProvider) kueueRoots() kueueRootResolver {
	if p == nil || p.cache == nil {
		return nil
	}
	return &cacheKueueRoots{cache: p.cache}
}

// cacheKueueRoots follows Workloads' controller references through Radar's
// own view of the caches. Callers' access to the root is applied later, as
// for every issue owner (WithReadableOwner).
type cacheKueueRoots struct {
	cache   *k8s.ResourceCache
	session ownership.ReadSession
	view    *ownership.View
	failed  bool
}

func (r *cacheKueueRoots) chain(w *unstructured.Unstructured) []Ref {
	if r.failed || w == nil || w.GetUID() == "" {
		return nil
	}
	if r.view == nil {
		session, err := k8s.BeginOwnershipRead(r.cache)
		if err != nil {
			r.failed = true
			return nil
		}
		r.session, r.view = session, ownership.NewView(session, ownership.Unrestricted())
	}
	answer := r.view.ControllerChain(resourceid.Ref{Group: kueueGroup, Kind: kueueWorkloadKind, Namespace: w.GetNamespace(), Name: w.GetName()})
	return kueueChain(answer, w)
}

func (r *cacheKueueRoots) valid() bool {
	return r.session == nil || r.session.ValidateCaches() == nil
}

// kueueChain is the chain above the Workload, read from the same Workload
// incarnation that was classified, so a Workload recreated under the same
// name between the two reads is never given the other's run.
func kueueChain(answer ownership.ControllerChainAnswer, w *unstructured.Unstructured) []Ref {
	if !answer.Subject.Present() || answer.Subject.Read.Object.UID != w.GetUID() || answer.Cycle || answer.Truncated || len(answer.Chain) < 2 {
		return nil
	}
	out := make([]Ref, 0, len(answer.Chain)-1)
	for _, ref := range answer.Chain[1:] {
		out = append(out, Ref{Group: ref.Group, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name})
	}
	return out
}
