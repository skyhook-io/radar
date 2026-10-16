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
	// Generation increases whenever Resources change. 0 means discovery has
	// never returned any, so the catalog says nothing about what is served.
	Generation uint64
	// Partial: the refresh that produced Resources missed the groups in
	// FailedGroups, so a kind absent from the catalog may still be served.
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
	c := DiscoveryCatalog{Resources: slices.Clone(d.resources), Generation: d.generation, Partial: d.partial}
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
