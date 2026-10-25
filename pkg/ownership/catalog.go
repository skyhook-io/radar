package ownership

import (
	"cmp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"

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

// CatalogKind is what a catalog knows about one group and kind across the
// versions serving it.
type CatalogKind struct {
	resourceid.GroupKind
	Resource   string
	Namespaced bool
	// Versions are every version serving the kind, sorted. They are the
	// catalog's own and must not be modified.
	Versions []string
	// HasGCVerbs: the kind is served with delete, list and watch, which
	// Kubernetes' garbage collector needs to act on its objects. One such
	// version is enough. The collector ignores Events whatever their verbs.
	HasGCVerbs bool
}

// Catalog is the served API a session reads against. It is immutable.
type Catalog struct {
	kinds map[resourceid.GroupKind]CatalogKind
	// dependents are DependentKinds, worked out once.
	dependents []CatalogKind
	// ambiguous are the kinds two resources of one group serve, or that
	// versions serve at different scopes: a reference names a kind, not a
	// resource or a scope, so it can't say which it means.
	ambiguous []AmbiguousKind
	// ambiguousDependents are those of them the garbage collector may act on.
	ambiguousDependents []AmbiguousKind
	// lowercase maps a kind's all-lowercase spelling, where it differs, to
	// the kinds it spells: the garbage collector's discovery RESTMapper maps
	// it too.
	lowercase map[resourceid.GroupKind][]resourceid.GroupKind
	complete  bool
}

// AmbiguousKind is a kind two resources of one group serve, or that versions
// serve at different scopes.
type AmbiguousKind struct {
	resourceid.GroupKind
	// Namespaced and ClusterScoped say whether a resource of that scope serves
	// the kind with delete, list and watch: the garbage collector acts on the
	// kind's objects there, so they may be dependents.
	Namespaced, ClusterScoped bool
}

// NewCatalog builds a catalog from discovery's resources. complete says
// discovery answered for every API group: without it, a kind missing from the
// catalog may still be served.
func NewCatalog(resources []ServedResource, complete bool) *Catalog {
	byKind := map[resourceid.GroupKind][]ServedResource{}
	c := &Catalog{lowercase: map[resourceid.GroupKind][]resourceid.GroupKind{}, complete: complete}
	for _, r := range resources {
		gk := resourceid.GroupKind{Group: r.Group, Kind: r.Kind}
		if byKind[gk] == nil {
			if lower := strings.ToLower(r.Kind); lower != r.Kind {
				spelled := resourceid.GroupKind{Group: r.Group, Kind: lower}
				c.lowercase[spelled] = append(c.lowercase[spelled], gk)
			}
		}
		byKind[gk] = append(byKind[gk], r)
	}
	c.kinds = make(map[resourceid.GroupKind]CatalogKind, len(byKind))
	for gk, served := range byKind {
		resource, namespaced := served[0].Resource, served[0].Namespaced
		if slices.ContainsFunc(served, func(r ServedResource) bool { return r.Resource != resource || r.Namespaced != namespaced }) {
			a := AmbiguousKind{GroupKind: gk}
			for _, r := range served {
				if hasVerbs(r.Verbs, "delete", "list", "watch") {
					a.Namespaced = a.Namespaced || r.Namespaced
					a.ClusterScoped = a.ClusterScoped || !r.Namespaced
				}
			}
			c.ambiguous = append(c.ambiguous, a)
			continue
		}
		kind := CatalogKind{GroupKind: gk, Resource: resource}
		for _, r := range served {
			kind.Namespaced = r.Namespaced
			kind.Versions = append(kind.Versions, r.Version)
			kind.HasGCVerbs = kind.HasGCVerbs || hasVerbs(r.Verbs, "delete", "list", "watch")
		}
		slices.Sort(kind.Versions)
		kind.Versions = slices.Compact(kind.Versions)
		c.kinds[gk] = kind
	}
	slices.SortFunc(c.ambiguous, func(a, b AmbiguousKind) int { return compareGroupKinds(a.GroupKind, b.GroupKind) })
	for _, a := range c.ambiguous {
		if (a.Namespaced || a.ClusterScoped) && !IsEvent(a.GroupKind) {
			c.ambiguousDependents = append(c.ambiguousDependents, a)
		}
	}
	for gk, k := range c.kinds {
		if k.HasGCVerbs && !IsEvent(gk) {
			c.dependents = append(c.dependents, k)
		}
	}
	slices.SortFunc(c.dependents, func(a, b CatalogKind) int { return compareGroupKinds(a.GroupKind, b.GroupKind) })
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
func (c *Catalog) Kind(gk resourceid.GroupKind) (CatalogKind, bool) {
	k, ok := c.kinds[gk]
	return k, ok
}

// Ambiguous reports whether two resources of gk's group serve gk, or its
// versions serve it at different scopes.
func (c *Catalog) Ambiguous(gk resourceid.GroupKind) bool {
	_, found := slices.BinarySearchFunc(c.ambiguous, gk, func(a AmbiguousKind, gk resourceid.GroupKind) int {
		return compareGroupKinds(a.GroupKind, gk)
	})
	return found
}

// AmbiguousDependentKinds returns the ambiguous kinds whose objects may be
// dependents the garbage collector acts on, sorted by group and kind, without
// Events. A search for dependents can't read them as one resource's, so it
// leaves them unread. An ambiguous kind no resource serves with delete, list
// and watch has no dependents to miss, as with the kinds aggregated APIs serve
// only to answer queries. The slice is the catalog's own and must not be
// modified.
func (c *Catalog) AmbiguousDependentKinds() []AmbiguousKind {
	return c.ambiguousDependents
}

// Serves reports whether version is a version serving gk.
func (c *Catalog) Serves(gk resourceid.GroupKind, version string) bool {
	k, ok := c.kinds[gk]
	return ok && slices.Contains(k.Versions, version)
}

// referencedKind is the kind an owner reference's group and kind name as the
// garbage collector's discovery RESTMapper reads them: the kind as spelled,
// or the kind whose all-lowercase spelling it is. ok is false when the
// RESTMapper's answer depends on the order discovery listed kinds in: a
// spelling two kinds share in lower case, or a served kind that is another's
// all-lowercase spelling.
func (c *Catalog) referencedKind(gk resourceid.GroupKind) (resourceid.GroupKind, bool) {
	if _, known := c.kinds[gk]; known {
		return gk, len(c.lowercase[gk]) == 0
	}
	if c.Ambiguous(gk) {
		return gk, true
	}
	switch spelled := c.lowercase[gk]; len(spelled) {
	case 0:
		return gk, true
	case 1:
		return spelled[0], true
	}
	return gk, false
}

// mapsAt reports whether the collector maps gk at version: a version serving
// it, or no version or the internal one, which it maps to any serving one.
func (c *Catalog) mapsAt(gk resourceid.GroupKind, version string) bool {
	if version == "" || version == runtime.APIVersionInternal {
		_, known := c.kinds[gk]
		return known
	}
	return c.Serves(gk, version)
}

// Complete reports whether the discovery that built the catalog answered for
// every API group. It describes that discovery, which can be minutes old: a
// kind installed since may be served and still missing.
func (c *Catalog) Complete() bool {
	return c.complete
}

// DependentKinds returns the kinds whose objects can be dependents the
// garbage collector acts on, sorted by group and kind. Events are left out:
// Radar keeps none of their owner references, so an answer about an object's
// dependents covers every kind but Events. The slice is the catalog's own and
// must not be modified.
func (c *Catalog) DependentKinds() []CatalogKind {
	return c.dependents
}

// IsEvent reports whether gk is either API group's Event.
func IsEvent(gk resourceid.GroupKind) bool {
	return gk.Kind == "Event" && (gk.Group == "" || gk.Group == "events.k8s.io")
}
