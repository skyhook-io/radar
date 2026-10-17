package k8score

import "slices"

// DiscoveryCatalog is the served API as one discovery generation saw it. Unlike
// GetAPIResources it keeps every served version of a resource and never
// refreshes, so a reader can tell which versions an apiVersion may name and
// rely on the catalog not changing while it works.
type DiscoveryCatalog struct {
	// Resources holds one entry per served group, version and resource. Their
	// Verbs are shared with discovery and must not be modified.
	Resources []APIResource
	// Generation increases whenever Resources change.
	Generation uint64
	// Complete: the discovery request that set Resources returned no error,
	// so a kind absent from them wasn't served when it ran. Resources added
	// without such a request never make a catalog complete.
	Complete bool
	// Partial: the last discovery request missed the groups in FailedGroups.
	Partial      bool
	FailedGroups []string
}

// Catalog returns the current discovery generation without refreshing it.
func (d *ResourceDiscovery) Catalog() DiscoveryCatalog {
	if d == nil {
		return DiscoveryCatalog{}
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	c := DiscoveryCatalog{Resources: slices.Clone(d.resources), Generation: d.generation, Complete: d.complete, Partial: d.partial}
	for group := range d.failedGroup {
		c.FailedGroups = append(c.FailedGroups, group)
	}
	slices.Sort(c.FailedGroups)
	return c
}

// Generation returns the generation Catalog would return, without copying it.
func (d *ResourceDiscovery) Generation() uint64 {
	if d == nil {
		return 0
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.generation
}
