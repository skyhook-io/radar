package ownership

import (
	"cmp"
	"slices"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// ServedResource is one version of one resource the API serves, as discovery
// lists it.
type ServedResource struct {
	Group   string
	Version string
	Kind    string
	// Resource is the plural name, such as "deployments".
	Resource   string
	Namespaced bool
	Verbs      []string
}

// Kind is what a catalog knows about one group and kind across the versions
// serving it.
type Kind struct {
	resourceid.GroupKind
	Resource   string
	Namespaced bool
	// Versions are every version serving the kind, sorted. They are the
	// catalog's own and must not be modified.
	Versions []string
	// GarbageCollected: Kubernetes' garbage collector acts on objects of this
	// kind, which it does for every resource served with delete, list and
	// watch. One such version is enough.
	GarbageCollected bool
}

// Catalog is the served API a session reads against. It is immutable.
type Catalog struct {
	kinds    map[resourceid.GroupKind]Kind
	complete bool
}

// NewCatalog builds a catalog from discovery's resources. complete says
// discovery answered for every API group: without it, a kind missing from the
// catalog may still be served.
func NewCatalog(resources []ServedResource, complete bool) *Catalog {
	byKind := map[resourceid.GroupKind][]ServedResource{}
	for _, r := range resources {
		gk := resourceid.GroupKind{Group: r.Group, Kind: r.Kind}
		byKind[gk] = append(byKind[gk], r)
	}
	c := &Catalog{kinds: make(map[resourceid.GroupKind]Kind, len(byKind)), complete: complete}
	for gk, served := range byKind {
		// A kind served by two resources of one group is rare; settle on one
		// so lookups don't depend on discovery's order.
		resource := slices.MinFunc(served, func(a, b ServedResource) int { return cmp.Compare(a.Resource, b.Resource) }).Resource
		kind := Kind{GroupKind: gk, Resource: resource}
		for _, r := range served {
			if r.Resource != resource {
				continue
			}
			kind.Namespaced = r.Namespaced
			kind.Versions = append(kind.Versions, r.Version)
			kind.GarbageCollected = kind.GarbageCollected || hasVerbs(r.Verbs, "delete", "list", "watch")
		}
		slices.Sort(kind.Versions)
		kind.Versions = slices.Compact(kind.Versions)
		c.kinds[gk] = kind
	}
	return c
}

func hasVerbs(verbs []string, want ...string) bool {
	for _, v := range want {
		if !slices.Contains(verbs, v) {
			return false
		}
	}
	return true
}

// Kind returns what the catalog knows about gk.
func (c *Catalog) Kind(gk resourceid.GroupKind) (Kind, bool) {
	k, ok := c.kinds[gk]
	return k, ok
}

// Serves reports whether version is a version serving gk.
func (c *Catalog) Serves(gk resourceid.GroupKind, version string) bool {
	k, ok := c.kinds[gk]
	return ok && slices.Contains(k.Versions, version)
}

// Complete reports whether the catalog lists every served kind.
func (c *Catalog) Complete() bool {
	return c.complete
}

// DependentKinds returns the kinds whose objects can be dependents the
// garbage collector acts on, sorted by group and kind. Events are left out:
// Radar doesn't index their owners, so an answer about an object's dependents
// covers every kind but Events.
func (c *Catalog) DependentKinds() []Kind {
	var out []Kind
	for gk, k := range c.kinds {
		if k.GarbageCollected && !IsEvent(gk) {
			out = append(out, k)
		}
	}
	slices.SortFunc(out, func(a, b Kind) int {
		return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Kind, b.Kind))
	})
	return out
}

// IsEvent reports whether gk is either API group's Event.
func IsEvent(gk resourceid.GroupKind) bool {
	return gk.Kind == "Event" && (gk.Group == "" || gk.Group == "events.k8s.io")
}
