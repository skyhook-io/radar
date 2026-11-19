package k8s

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/cachesource"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// AnswerNeighborhood binds the walk and display data to the request's cache.
// A root whose custom kind Radar isn't collecting yet starts that kind's
// watch, as a read of the object would, and is asked about once more after
// waiting as long as that read waits; it then answers from the watch, or says
// the kind is still loading or denied to Radar.
func AnswerNeighborhood(ctx context.Context, cache *ResourceCache, root topology.ResourceRef, opts topology.NeighborhoodOptions) (*topology.Subgraph, error) {
	sub, caches, err := answerNeighborhood(ctx, cache, root, opts)
	if err != nil || len(sub.Nodes) > 0 {
		return sub, err
	}
	tried, codes := collectRootKinds(ctx, caches, sub.UncollectedRoots)
	if !tried {
		return sub, nil
	}
	sub, _, err = answerNeighborhood(ctx, cache, root, opts)
	if err == nil && len(sub.Nodes) == 0 && sub.RootUnavailable == readreason.RadarKindNotCollected && len(codes) > 0 {
		// A refusal in the root's namespace is recorded only against that
		// watch request, and a watch still starting isn't recorded yet, so
		// the walk's reads see neither.
		sub.RootUnavailable = readreason.Primary(codes)
	}
	return sub, err
}

func answerNeighborhood(ctx context.Context, cache *ResourceCache, root topology.ResourceRef, opts topology.NeighborhoodOptions) (*topology.Subgraph, cachesource.Caches, error) {
	session, err := BeginOwnershipRead(cache)
	if err != nil {
		return nil, cachesource.Caches{}, err
	}
	if len(session.Catalog().DependentKinds()) == 0 {
		// Discovery hasn't answered yet, as while Radar starts, so no related
		// kind could be searched or resolved.
		return nil, cachesource.Caches{}, ownership.ErrNotConnected
	}
	rs, ok := session.(relations.Session)
	if !ok {
		return nil, cachesource.Caches{}, fmt.Errorf("relation session unavailable")
	}
	caches, ok := cachesource.SessionCaches(session)
	if !ok {
		return nil, cachesource.Caches{}, fmt.Errorf("relation caches unavailable")
	}
	provider := NewTopologyResourceProvider(&ResourceCache{ResourceCache: caches.Typed})
	var dp topology.DynamicProvider
	if caches.Dynamic != nil && caches.Discovery != nil {
		dp = NewTopologyDynamicProvider(&DynamicResourceCache{DynamicResourceCache: caches.Dynamic}, &ResourceDiscovery{ResourceDiscovery: caches.Discovery})
	}
	sub, err := topology.WalkNeighborhood(ctx, rs, root, opts, provider, dp)
	if err != nil {
		return nil, cachesource.Caches{}, err
	}
	if err := session.ValidateCaches(); err != nil {
		return nil, cachesource.Caches{}, err
	}
	return sub, caches, nil
}

// collectRootKinds starts the watch of each root candidate's custom kind the
// walk found uncollected (the walk lists only candidates the caller's Allow
// admitted) and waits for it, the whole step bounded as a read of one object
// waits: starting a watch otherwise waits as long as CRD discovery takes
// while Radar starts. It reports whether it tried any, and the codes of the
// watches Radar was refused or that hadn't started within the bound. Typed
// kinds are collected or not by configuration, and the kinds Radar reads
// directly are never watched, so neither is started here.
func collectRootKinds(ctx context.Context, caches cachesource.Caches, roots []resourceid.Ref) (tried bool, codes []readreason.Code) {
	if caches.Dynamic == nil || caches.Discovery == nil {
		return false, nil
	}
	bounded, cancel := context.WithTimeout(ctx, k8score.ObjectReadSyncWait)
	defer cancel()
	type watch struct {
		gvr       schema.GroupVersionResource
		namespace string
		err       error
	}
	var watches []watch
	for _, r := range roots {
		if isTypedKind(r.GroupKind()) {
			continue
		}
		gvr, ok := caches.Discovery.GetGVRWithGroup(r.Kind, r.Group)
		if !ok || ShouldBypassDynamicInformer(gvr) {
			continue
		}
		watches = append(watches, watch{gvr: gvr, namespace: r.Namespace})
	}
	// Every watch starts at once, and before any is waited for, so one slow
	// to start or to load can't keep another, of the same kind in another
	// group, from starting.
	var wg sync.WaitGroup
	for i := range watches {
		wg.Go(func() {
			watches[i].err = caches.Dynamic.EnsureWatchingContext(bounded, watches[i].gvr, watches[i].namespace)
		})
	}
	wg.Wait()
	for _, w := range watches {
		if w.err == nil {
			caches.Dynamic.WaitForSyncContext(bounded, w.gvr, w.namespace)
			continue
		}
		if code, ok := CollectorErrorCode(w.err); ok {
			codes = append(codes, code)
		} else if bounded.Err() != nil && ctx.Err() == nil {
			// Starting the watch outlasted the bound: it goes on in the
			// cache, or a retry starts it once discovery is done.
			codes = append(codes, readreason.KindSyncPending)
		}
	}
	return len(watches) > 0, codes
}
