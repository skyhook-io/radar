package relations

import (
	"cmp"
	"slices"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// A Family is one way objects name others: what a source object declares,
// such as the ConfigMaps a Pod mounts. A View answers both directions of
// every family the same way, so adding one is a table entry.
//
// A target is in its source's namespace or cluster-scoped, unless the family
// is CrossNamespace: the sources naming a namespaced target are found by
// reading its namespace, and those naming a cluster-scoped one, or any
// target of a CrossNamespace family, by reading every namespace the caller
// may read.
type Family struct {
	// Name keys the family's coverage on the wire.
	Name string
	// Sources are the kinds that declare, in the order answers list them.
	Sources []Source
	// Targets are the kinds a declaration can name.
	Targets []resourceid.GroupKind
	// IsTarget, when set, says instead which kinds a declaration can name,
	// for a family whose targets are open-ended, as Karpenter's NodeClasses
	// are: each provider defines its own.
	IsTarget func(resourceid.GroupKind) bool
	// CrossNamespace: a declaration may name a namespaced target in another
	// namespace, as a Gateway API route may name a Gateway.
	CrossNamespace bool
	// Declared visits what obj declares, each with the path declaring it.
	// It returns an error when part of obj couldn't be read, after visiting
	// what could.
	Declared func(obj runtime.Object, visit func(Declaration)) error
}

// Source is a kind that declares. Targets narrows the family's Targets to
// the ones it can declare, when it can't declare them all, so an answer
// about another kind neither reads nor reports it.
type Source struct {
	Kind    resourceid.GroupKind
	Targets []resourceid.GroupKind
}

func (s Source) declares(target resourceid.GroupKind) bool {
	return len(s.Targets) == 0 || slices.Contains(s.Targets, target)
}

func (f *Family) target(gk resourceid.GroupKind) bool {
	if f.IsTarget != nil {
		return f.IsTarget(gk)
	}
	return slices.Contains(f.Targets, gk)
}

func (f *Family) source(gk resourceid.GroupKind) int {
	return slices.IndexFunc(f.Sources, func(s Source) bool { return s.Kind == gk })
}

// Declaration is one target a source declares, and where.
type Declaration struct {
	Target resourceid.Ref
	Path   string
}

// Covers reports whether f says anything about an object of gk: a kind it
// declares or one that declares.
func (f *Family) Covers(gk resourceid.GroupKind) bool {
	return f.source(gk) >= 0 || f.target(gk)
}

// TargetState is what a declared reference found when Radar read its target.
type TargetState uint8

const (
	// TargetPresent: the target is in Radar's cache.
	TargetPresent TargetState = iota
	// TargetAbsent: the target's store had loaded in full and doesn't hold it.
	TargetAbsent
	// TargetUnread: Radar couldn't read the target, or couldn't check the
	// caller's access to it; Gap says why.
	TargetUnread
	// TargetWithheld: the caller may not read the target.
	TargetWithheld
)

// Declared is one target a subject declares, every path that declares it,
// and what reading it found.
type Declared struct {
	Ref   resourceid.Ref
	Paths []string
	State TargetState
	// Gap is set when State is TargetUnread.
	Gap ownership.Gap
}

// ReferencesAnswer is what a subject declares. Withheld targets are listed,
// so a host can say a reference exists without naming what the caller may
// not read; Coverage.Narrowed is set when there are any.
type ReferencesAnswer struct {
	Declared []Declared
	Coverage Coverage
}

// Referrer is one object that declares the subject, with every path
// declaring it.
type Referrer struct {
	Ref   resourceid.Ref
	Paths []string
}

// ReferrersAnswer is what declares a subject, as the caller may read it.
type ReferrersAnswer struct {
	Referrers []Referrer
	Coverage  Coverage
}

// References answers what subject declares in f. fetched is the subject as
// the host read it, or nil to read it from the cache. A caller who may not
// read the subject gets nothing.
func (v *View) References(f *Family, subject resourceid.Ref, fetched runtime.Object) ReferencesAnswer {
	var a ReferencesAnswer
	i := f.source(subject.GroupKind())
	if i < 0 || !v.mayRead(subject.GroupKind(), subject.Namespace, &a.Coverage) {
		return a
	}
	source := f.Sources[i]
	obj := fetched
	if obj == nil {
		borrowed, read := v.session.Borrow(subject)
		switch read.State {
		case ownership.Present:
			obj = borrowed
		case ownership.Unavailable:
			a.Coverage.addGap(read.Gap)
			return a
		default:
			return a
		}
	}
	byTarget := map[resourceid.Ref]int{}
	err := f.Declared(obj, func(d Declaration) {
		if !f.target(d.Target.GroupKind()) || !source.declares(d.Target.GroupKind()) {
			return
		}
		i, seen := byTarget[d.Target]
		if !seen {
			i = len(a.Declared)
			byTarget[d.Target] = i
			a.Declared = append(a.Declared, Declared{Ref: d.Target})
		}
		if !slices.Contains(a.Declared[i].Paths, d.Path) {
			a.Declared[i].Paths = append(a.Declared[i].Paths, d.Path)
		}
	})
	if err != nil {
		a.Coverage.addGap(ownership.Gap{Kind: subject.GroupKind(), Namespace: subject.Namespace, Reason: readreason.ReadError})
	}
	slices.SortFunc(a.Declared, func(x, y Declared) int {
		return cmp.Or(cmp.Compare(x.Ref.Group, y.Ref.Group), cmp.Compare(x.Ref.Kind, y.Ref.Kind), cmp.Compare(x.Ref.Name, y.Ref.Name))
	})
	for j := range a.Declared {
		d := &a.Declared[j]
		switch v.decide(d.Ref.GroupKind(), d.Ref.Namespace) {
		case ownership.Allowed:
		case ownership.Undecided:
			d.State = TargetUnread
			d.Gap = ownership.Gap{Kind: d.Ref.GroupKind(), Namespace: d.Ref.Namespace, Reason: readreason.UserPermissionCheckFailed}
			a.Coverage.addGap(d.Gap)
			continue
		default:
			d.State = TargetWithheld
			a.Coverage.Narrowed = true
			continue
		}
		switch read := v.session.Object(d.Ref); read.State {
		case ownership.Present:
			d.State = TargetPresent
		case ownership.Absent:
			d.State = TargetAbsent
		default:
			d.State = TargetUnread
			d.Gap = read.Gap
			a.Coverage.addGap(read.Gap)
		}
	}
	return a
}

// Referrers answers what declares subject in f, among the objects of f's
// source kinds that may name it (see Family), up to the view's limit with a
// total. It
// answers only for a subject Radar holds: a caller who may not read it gets
// nothing, and a subject Radar couldn't read gets its gap. fetched is the
// subject as the host read it, or nil: a subject the host read that the
// cache doesn't hold yet leaves the answer incomplete rather than empty.
//
// Objects are read through the namespace index every store keeps and
// evaluated as asked, rather than kept in an index of every
// declaration: for config references on a Pod store such an index cost
// about 1.4 KB a Pod and made a relist about five times as long.
func (v *View) Referrers(f *Family, subject resourceid.Ref, fetched runtime.Object) ReferrersAnswer {
	var a ReferrersAnswer
	if !f.target(subject.GroupKind()) || !v.mayRead(subject.GroupKind(), subject.Namespace, &a.Coverage) {
		return a
	}
	switch read := v.session.Object(subject); read.State {
	case ownership.Present:
	case ownership.Unavailable:
		a.Coverage.addGap(read.Gap)
		return a
	case ownership.Absent:
		if fetched != nil {
			a.Coverage.addGap(ownership.Gap{Kind: subject.GroupKind(), Namespace: subject.Namespace, Reason: readreason.ObjectNotFound})
		}
		return a
	default:
		return a
	}
	// A namespaced target's sources are in its namespace. A cluster-scoped
	// one's, or a CrossNamespace family's, can be anywhere, so, as
	// ownership.View.Children does, the search covers the namespaces the
	// caller may read something in, each source is checked where it is, and a
	// gap is kept only if it covers what the caller may read.
	across := subject.Namespace == "" || f.CrossNamespace
	scope, index, keys := ownership.InNamespace(subject.Namespace), cache.NamespaceIndex, []string{subject.Namespace}
	if across {
		namespaces, all := v.access.Namespaces()
		scope = ownership.SearchScope{AllNamespaces: all, Namespaces: namespaces, IncludeClusterScoped: true}
		index, keys = "", nil
		if !all {
			// Cluster-scoped sources file under "" in the namespace index.
			index, keys = cache.NamespaceIndex, append(slices.Clone(namespaces), "")
		}
		a.Coverage.Narrowed = v.access != ownership.Unrestricted()
	}
	for _, source := range f.Sources {
		gk := source.Kind
		if !source.declares(subject.GroupKind()) || !across && !v.mayRead(gk, subject.Namespace, &a.Coverage) {
			continue
		}
		read := v.session.Indexed(gk, index, keys, scope)
		for _, g := range read.Gaps {
			if !across || ownership.GapCoversReadable(v.access, v.session.Catalog(), g, scope) {
				a.Coverage.addGap(g)
			}
		}
		for _, o := range read.Objects {
			// Saying a check failed would say a referrer may be there, so an
			// undecided one narrows the answer as a denied one does.
			if across && v.decide(gk, o.Ref.Namespace) != ownership.Allowed {
				a.Coverage.Narrowed = true
				continue
			}
			var paths []string
			err := f.Declared(o.Object, func(d Declaration) {
				if d.Target == subject && !slices.Contains(paths, d.Path) {
					paths = append(paths, d.Path)
				}
			})
			if err != nil {
				a.Coverage.addGap(ownership.Gap{Kind: gk, Namespace: o.Ref.Namespace, Reason: readreason.ReadError})
			}
			if len(paths) > 0 {
				a.Referrers = append(a.Referrers, Referrer{Ref: o.Ref, Paths: paths})
			}
		}
	}
	slices.SortStableFunc(a.Referrers, func(x, y Referrer) int {
		return cmp.Or(cmp.Compare(f.source(x.Ref.GroupKind()), f.source(y.Ref.GroupKind())), cmp.Compare(x.Ref.Name, y.Ref.Name))
	})
	if v.limit > 0 && len(a.Referrers) > v.limit {
		a.Coverage.Truncated = true
		a.Coverage.Total = len(a.Referrers)
		a.Referrers = a.Referrers[:v.limit]
	}
	return a
}
