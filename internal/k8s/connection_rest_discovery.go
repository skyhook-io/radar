package k8s

import (
	"context"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
)

type connectionRESTDiscovery struct {
	once   sync.Once
	client discovery.CachedDiscoveryInterface
	mapper meta.RESTMapper
}

// CachedRESTDiscovery uses the connection's own identity and discovery rate limits.
func (c *Connection) CachedRESTDiscovery() (discovery.CachedDiscoveryInterface, meta.RESTMapper) {
	client := c.DiscoveryClient()
	if client == nil {
		return nil, nil
	}
	c.restDiscovery.once.Do(func() {
		cached := memory.NewMemCacheClient(client)
		mapper := refreshOnMappingMiss{restmapper.NewDeferredDiscoveryRESTMapper(cached)}
		c.restDiscovery.client = cached
		c.restDiscovery.mapper = restmapper.NewShortcutExpander(mapper, cached, func(string) {})
	})
	return c.restDiscovery.client, c.restDiscovery.mapper
}

type refreshOnMappingMiss struct {
	*restmapper.DeferredDiscoveryRESTMapper
}

func (m refreshOnMappingMiss) RESTMappingWithContext(ctx context.Context, kind schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	mapping, err := m.DeferredDiscoveryRESTMapper.RESTMappingWithContext(ctx, kind, versions...)
	// A populated memory cache reports itself fresh even after a CRD is added.
	if meta.IsNoMatchError(err) {
		m.ResetWithContext(ctx)
		return m.DeferredDiscoveryRESTMapper.RESTMappingWithContext(ctx, kind, versions...)
	}
	return mapping, err
}
