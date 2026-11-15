package k8s

import (
	"context"
	"fmt"

	"github.com/skyhook-io/radar/pkg/ownership/cachesource"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/topology"
)

// AnswerNeighborhood binds the walk and display data to the request's cache.
func AnswerNeighborhood(ctx context.Context, cache *ResourceCache, root topology.ResourceRef, opts topology.NeighborhoodOptions) (*topology.Subgraph, error) {
	session, err := BeginOwnershipRead(cache)
	if err != nil {
		return nil, err
	}
	rs, ok := session.(relations.Session)
	if !ok {
		return nil, fmt.Errorf("relation session unavailable")
	}
	caches, ok := cachesource.SessionCaches(session)
	if !ok {
		return nil, fmt.Errorf("relation caches unavailable")
	}
	provider := NewTopologyResourceProvider(&ResourceCache{ResourceCache: caches.Typed})
	var dp topology.DynamicProvider
	if caches.Dynamic != nil && caches.Discovery != nil {
		dp = NewTopologyDynamicProvider(&DynamicResourceCache{DynamicResourceCache: caches.Dynamic}, &ResourceDiscovery{ResourceDiscovery: caches.Discovery})
	}
	sub, err := topology.WalkNeighborhood(ctx, rs, root, opts, provider, dp)
	if err != nil {
		return nil, err
	}
	if err := session.ValidateCaches(); err != nil {
		return nil, err
	}
	return sub, nil
}
