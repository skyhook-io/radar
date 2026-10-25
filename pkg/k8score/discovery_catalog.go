package k8score

import "slices"

// DiscoveryCatalog is the served API as discovery last saw it. Unlike
// GetAPIResources it keeps every served version of a resource and never
// refreshes, so a reader can tell which versions an apiVersion may name and
// rely on the catalog not changing while it works.
type DiscoveryCatalog struct {
	// Resources holds one entry per served group, version and resource. Their
	// Verbs are shared with discovery and must not be modified.
	Resources []APIResource
	// Complete: the discovery request that set Resources returned no error,
	// so a kind absent from them wasn't served when it ran. Resources added
	// without such a request never make a catalog complete.
	Complete bool
}

// Catalog returns what discovery serves now, without refreshing it.
func (d *ResourceDiscovery) Catalog() DiscoveryCatalog {
	if d == nil {
		return DiscoveryCatalog{}
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return DiscoveryCatalog{Resources: slices.Clone(d.resources), Complete: d.complete}
}
