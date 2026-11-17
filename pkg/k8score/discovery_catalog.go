package k8score

import "slices"

// DiscoveryCatalog is the served API as discovery last saw it. Unlike
// GetAPIResources it keeps every served version of a resource and never
// refreshes, so a reader can tell which versions an apiVersion may name and
// rely on the catalog not changing while it works.
type DiscoveryCatalog struct {
	// Generation identifies this snapshot within its ResourceDiscovery instance.
	Generation uint64
	// Resources holds one entry per served group, version and resource. Their
	// Verbs are shared with discovery and must not be modified.
	Resources []APIResource
	// Complete: the discovery request that set Resources returned no error,
	// so a kind absent from them wasn't served when it ran. Resources added
	// without such a request never make a catalog complete.
	Complete bool
	// FailedGroups names every group the request that set Resources missed.
	// When incomplete with no failed groups, no group is covered.
	FailedGroups []string
}

// Catalog returns what discovery serves now, without refreshing it.
func (d *ResourceDiscovery) Catalog() DiscoveryCatalog {
	if d == nil {
		return DiscoveryCatalog{}
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.catalogLocked()
}

// CatalogIfChanged returns a snapshot only if generation differs from the
// current generation of this discovery instance. An unchanged generation
// returns a zero catalog and false without copying resources.
func (d *ResourceDiscovery) CatalogIfChanged(generation uint64) (DiscoveryCatalog, bool) {
	if d == nil {
		return DiscoveryCatalog{}, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.generation == generation {
		return DiscoveryCatalog{}, false
	}
	return d.catalogLocked(), true
}

func (d *ResourceDiscovery) catalogLocked() DiscoveryCatalog {
	catalog := DiscoveryCatalog{Generation: d.generation, Resources: slices.Clone(d.resources), Complete: d.complete}
	for group := range d.failedGroup {
		catalog.FailedGroups = append(catalog.FailedGroups, group)
	}
	slices.Sort(catalog.FailedGroups)
	return catalog
}
