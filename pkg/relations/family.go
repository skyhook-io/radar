package relations

import (
	"cmp"
	"slices"

	"k8s.io/apimachinery/pkg/runtime"

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
	Name       string
	Projection Disposition
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
	// ReferrersIndex, when set, finds the sources that may name a target
	// through a store index keyed by the target alone, instead of reading
	// every source in scope. What each source found declares is still
	// evaluated. An index costs work on every update and relist of its source
	// kind, so a family whose reverse search reads every namespace has one
	// only where that cost is within the admission bar
	// (BenchmarkRelationIndexUpdate); otherwise ScanAccepted says why it
	// scans.
	ReferrersIndex *ReferrersIndex
	// ScanAccepted explains why a reverse search deliberately reads every source.
	ScanAccepted string
	// Declared visits what obj declares, each with the path declaring it.
	// It returns an error when part of obj couldn't be read, after visiting
	// what could.
	Declared func(obj runtime.Object, visit func(Declaration)) error
}

// ReferrersIndex is a store index the sources of a family keep, and the key
// it files them under for a target.
type ReferrersIndex struct {
	Name string
	Key  func(target resourceid.Ref) string
	// Candidates can index a superset when resolution depends on request-time state.
	Candidates func(runtime.Object, func(Declaration)) error
}

// Source is a kind that declares. Targets narrows the family's Targets to
// the ones it can declare, when it can't declare them all, so an answer
// about another kind neither reads nor reports it. Empty Targets means every
// target of the family.
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

// Declaration is one target a source declares, and where. Path is the
// declaring field from the object's root, with [] for each list it passes
// through, such as spec.template.spec.volumes[].configMap.name; readers
// compare these spellings, so they are part of the contract.
type Declaration struct {
	Target resourceid.Ref
	Path   string
	// Or are the same reference in other API groups the controller also
	// resolves it from, in the order it tries them after Target: the
	// declaration names the first that exists.
	Or []resourceid.Ref
}

// candidates are the refs d may name that f's source may declare: Target
// first, then each of Or.
func (d Declaration) candidates(f *Family, source Source) []resourceid.Ref {
	var out []resourceid.Ref
	for _, r := range append([]resourceid.Ref{d.Target}, d.Or...) {
		if f.target(r.GroupKind()) && source.declares(r.GroupKind()) && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
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
// the host read it, read instead of the cache's copy, or nil to read the
// cache's. A caller who may not read the subject gets nothing.
//
// fetched means something else to Referrers and the selector answers, which
// read the subject from the cache and take fetched only as evidence that it
// exists. A host that wants every answer to read its copy wraps the session
// with CaptureSubject.
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
	var candidates [][]resourceid.Ref
	err := f.Declared(obj, func(d Declaration) {
		c := d.candidates(f, source)
		if len(c) == 0 {
			return
		}
		i, seen := byTarget[c[0]]
		if !seen {
			i = len(a.Declared)
			byTarget[c[0]] = i
			a.Declared = append(a.Declared, Declared{Ref: c[0]})
			candidates = append(candidates, c)
		}
		if !slices.Contains(a.Declared[i].Paths, d.Path) {
			a.Declared[i].Paths = append(a.Declared[i].Paths, d.Path)
		}
	})
	if err != nil {
		a.Coverage.addGap(ownership.Gap{Kind: subject.GroupKind(), Namespace: subject.Namespace, Reason: readreason.ReadError})
	}
	for j := range a.Declared {
		v.readTarget(&a.Declared[j], candidates[j], &a.Coverage)
	}
	slices.SortFunc(a.Declared, func(x, y Declared) int {
		return cmp.Or(cmp.Compare(x.Ref.Group, y.Ref.Group), cmp.Compare(x.Ref.Kind, y.Ref.Kind), cmp.Compare(x.Ref.Name, y.Ref.Name))
	})
	return a
}

// readTarget reads what d declares as its controller resolves it: the
// first of candidates that exists. A candidate Radar couldn't read, or the
// caller may not, ends the search there, since a later one may be shadowed
// by it: d is then unread or withheld rather than resolved to a fallback.
// d names the candidate the search stopped at, or, absent when none
// exists, the first candidate the cluster serves. One found only after
// served candidates that lack it is unread, with the legacyGroupOnly gap.
func (v *View) readTarget(d *Declared, candidates []resourceid.Ref, c *Coverage) {
	served := v.served(candidates)
	d.Ref, d.State = served[0], TargetAbsent
	for i, r := range served {
		switch v.decide(r.GroupKind(), r.Namespace) {
		case ownership.Allowed:
		case ownership.Undecided:
			d.Ref, d.State, d.Gap = r, TargetUnread, ownership.Gap{Kind: r.GroupKind(), Namespace: r.Namespace, Reason: readreason.UserPermissionCheckFailed}
			c.addGap(d.Gap)
			return
		default:
			d.Ref, d.State = r, TargetWithheld
			c.Narrowed = true
			return
		}
		switch read := v.session.Object(r); read.State {
		case ownership.Present:
			d.Ref, d.State = r, TargetPresent
			if i > 0 {
				d.State, d.Gap = TargetUnread, legacyGroupOnly(r)
				c.addGap(d.Gap)
			}
			return
		case ownership.Absent:
		default:
			d.Ref, d.State, d.Gap = r, TargetUnread, read.Gap
			c.addGap(read.Gap)
			return
		}
	}
}

// legacyGroupOnly is the gap of a declaration that resolves to r only
// because each candidate before it is served and lacks the object: whether
// the controller reads r's group too, as Traefik 2.10 and 2.11 do, or no
// longer does, as Traefik 3 doesn't, decides whether it uses r.
func legacyGroupOnly(r resourceid.Ref) ownership.Gap {
	return ownership.Gap{Kind: r.GroupKind(), Namespace: r.Namespace, Reason: readreason.LegacyGroupOnly}
}

// served are the candidates whose kind the cluster serves, so an API group
// a cluster doesn't have, such as traefik.io before Traefik 2.10, shadows
// nothing. It drops one only when discovery covered its group and another
// is served; otherwise every candidate is tried, and an unserved one says
// why.
func (v *View) served(candidates []resourceid.Ref) []resourceid.Ref {
	if len(candidates) < 2 {
		return candidates
	}
	catalog := v.session.Catalog()
	var out []resourceid.Ref
	for _, r := range candidates {
		// An ambiguous kind is served, just not readable: it stays, so its
		// read stops the resolution. So does one whose group discovery
		// missed.
		if _, known := catalog.Kind(r.GroupKind()); known || catalog.Ambiguous(r.GroupKind()) || !catalog.CoversGroup(r.Group) {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return candidates
	}
	return out
}

// resolvesTo reports whether a declaration whose candidates are candidates
// resolves to subject: subject is one of them and none before it exists. A
// candidate before it that Radar couldn't read, or the caller may not, leaves
// that unknown, so the answer doesn't claim it and says why in c.
func (v *View) resolvesTo(candidates []resourceid.Ref, subject resourceid.Ref, c *Coverage) bool {
	for i, r := range v.served(candidates) {
		if r == subject {
			if i > 0 {
				c.addGap(legacyGroupOnly(r))
			}
			return true
		}
		if !v.mayRead(r.GroupKind(), r.Namespace, c) {
			return false
		}
		switch read := v.session.Object(r); read.State {
		case ownership.Absent:
		case ownership.Present:
			return false
		default:
			c.addGap(read.Gap)
			return false
		}
	}
	return false
}

// Referrers answers what declares subject in f, among the objects of f's
// source kinds that may name it (see Family), up to the view's limit with a
// total. It
// answers only for a subject Radar holds: a caller who may not read it gets
// nothing, and a subject Radar couldn't read gets its gap. fetched is the
// subject as the host read it, or nil: a subject the host read that the
// cache doesn't hold yet leaves the answer incomplete rather than empty.
//
// Candidates come from the family's index, or from the namespace index for
// a local or explicitly accepted scan. Every candidate is re-evaluated as
// asked, including alternative-group and mesh-domain resolution.
func (v *View) Referrers(f *Family, subject resourceid.Ref, fetched runtime.Object) ReferrersAnswer {
	var a ReferrersAnswer
	if !f.target(subject.GroupKind()) {
		return a
	}
	if _, held := v.heldSubject(subject, fetched, &a.Coverage); !held {
		return a
	}
	// A namespaced target's sources are in its namespace; a cluster-scoped
	// one's, or a CrossNamespace family's, can be anywhere.
	s := v.newSearch(subject.Namespace, subject.Namespace == "" || f.CrossNamespace)
	if f.ReferrersIndex != nil {
		s.index, s.keys = f.ReferrersIndex.Name, []string{f.ReferrersIndex.Key(subject), unreadReferrerKey}
	}
	for _, source := range f.Sources {
		if !source.declares(subject.GroupKind()) {
			continue
		}
		s.each(source.Kind, &a.Coverage, func(o IndexedObject, own *Coverage) func() {
			var paths []string
			err := f.Declared(o.Object, func(d Declaration) {
				if slices.Contains(paths, d.Path) || d.Target != subject && !slices.Contains(d.Or, subject) {
					return
				}
				if len(d.Or) == 0 || v.resolvesTo(d.candidates(f, source), subject, own) {
					paths = append(paths, d.Path)
				}
			})
			if err != nil {
				own.addGap(ownership.Gap{Kind: source.Kind, Namespace: o.Ref.Namespace, Reason: readreason.ReadError})
			}
			if len(paths) == 0 {
				return nil
			}
			return func() { a.Referrers = append(a.Referrers, Referrer{Ref: o.Ref, Paths: paths}) }
		})
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
