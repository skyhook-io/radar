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
	kinds map[resourceid.GroupKind]Kind
	// ambiguous are the kinds two resources of one group serve: a reference
	// names a kind, not a resource, so it can't say which it means.
	ambiguous []resourceid.GroupKind
	complete  bool
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
		resource := served[0].Resource
		if slices.ContainsFunc(served, func(r ServedResource) bool { return r.Resource != resource }) {
			c.ambiguous = append(c.ambiguous, gk)
			continue
		}
		kind := Kind{GroupKind: gk, Resource: resource}
		for _, r := range served {
			kind.Namespaced = r.Namespaced
			kind.Versions = append(kind.Versions, r.Version)
			kind.GarbageCollected = kind.GarbageCollected || hasVerbs(r.Verbs, "delete", "list", "watch")
		}
		slices.Sort(kind.Versions)
		kind.Versions = slices.Compact(kind.Versions)
		c.kinds[gk] = kind
	}
	slices.SortFunc(c.ambiguous, compareGroupKinds)
	return c
}

func compareGroupKinds(a, b resourceid.GroupKind) int {
	return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Kind, b.Kind))
}

func hasVerbs(verbs []string, want ...string) bool {
	for _, v := range want {
		if !slices.Contains(verbs, v) {
			return false
		}
	}
	return true
}

// Kind returns what the catalog knows about gk. An ambiguous kind is not
// found.
func (c *Catalog) Kind(gk resourceid.GroupKind) (Kind, bool) {
	k, ok := c.kinds[gk]
	return k, ok
}

// Ambiguous reports whether two resources of gk's group serve gk.
func (c *Catalog) Ambiguous(gk resourceid.GroupKind) bool {
	_, found := slices.BinarySearchFunc(c.ambiguous, gk, compareGroupKinds)
	return found
}

// AmbiguousKinds returns the kinds two resources of one group serve, sorted
// by group and kind. They are in no other answer of the catalog.
func (c *Catalog) AmbiguousKinds() []resourceid.GroupKind {
	return slices.Clone(c.ambiguous)
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
// Radar keeps none of their owner references, so an answer about an object's
// dependents covers every kind but Events.
func (c *Catalog) DependentKinds() []Kind {
	var out []Kind
	for gk, k := range c.kinds {
		if k.GarbageCollected && !IsEvent(gk) {
			out = append(out, k)
		}
	}
	slices.SortFunc(out, func(a, b Kind) int { return compareGroupKinds(a.GroupKind, b.GroupKind) })
	return out
}

// IsEvent reports whether gk is either API group's Event.
func IsEvent(gk resourceid.GroupKind) bool {
	return gk.Kind == "Event" && (gk.Group == "" || gk.Group == "events.k8s.io")
}
