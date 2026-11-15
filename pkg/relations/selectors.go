package relations

import (
	"cmp"
	"slices"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// A SelectorFamily is one way objects choose others by labels, as a
// PodDisruptionBudget's selector chooses the workloads whose pods it
// matches. Where a Family's source names its targets, a selector is matched
// against each candidate, so both directions read the candidates' namespace
// and evaluate on ask.
type SelectorFamily struct {
	// Name keys the family's coverage on the wire.
	Name string
	// Selectors are the kinds that choose, in the order answers list them.
	Selectors []Selector
	// Selectables are the kinds that can be chosen, in the order answers
	// list them.
	Selectables []Selectable
	// CrossNamespace: a selector may choose objects in other namespaces, as
	// a ServiceMonitor's namespaceSelector does.
	CrossNamespace bool
	// Identity, when set, names the object a match is, for kinds served by
	// two groups that hold the same objects, as Calico's are: each object is
	// listed once, under the first group answers find it in.
	Identity func(resourceid.Ref) resourceid.Ref
}

// Selector is a kind that chooses. Selects narrows the family's
// Selectables to the kinds it can choose, when it can't choose them all.
type Selector struct {
	Kind    resourceid.GroupKind
	Selects []resourceid.GroupKind
	// Selection returns what obj chooses. ok is false when it chooses
	// nothing; err is set when its selector can't be read.
	Selection func(obj runtime.Object) (s Selection, ok bool, err error)
}

// Selection is what one selector chooses, and where.
type Selection struct {
	// Matches reports whether c is chosen, reading through lookup any other
	// object that takes, such as its Namespace for labels a selector names.
	// decided is false when that can't be told from what Radar holds.
	Matches func(c Candidate, lookup Lookup) (chosen, decided bool)
	// Namespaces are the namespaces it chooses in when it names them, and
	// AnyNamespace says it chooses in every one; otherwise it chooses in its
	// own.
	Namespaces   []string
	AnyNamespace bool
}

// LabelSelection is the Selection of a label selector over a candidate's
// labels, in the selector's own namespace.
func LabelSelection(sel labels.Selector) Selection {
	return Selection{Matches: func(c Candidate, _ Lookup) (bool, bool) { return sel.Matches(c.Labels), true }}
}

func (s Selection) chooses(own, namespace string) bool {
	switch {
	case s.AnyNamespace:
		return true
	case len(s.Namespaces) > 0:
		return slices.Contains(s.Namespaces, namespace)
	}
	return namespace == own
}

// Candidate is an object as a selector sees it: a workload is seen as the
// pods its template makes.
type Candidate struct {
	Ref            resourceid.Ref
	Labels         labels.Set
	ServiceAccount string
}

// Lookup reads another object an answer depends on, as the caller may read
// it, recording in the answer's coverage why it couldn't. ok is false when
// the object isn't there to read.
type Lookup func(ref resourceid.Ref) (obj runtime.Object, ok bool)

// Selectable is a kind that can be chosen. Candidate returns obj as a
// selector sees it; ok is false when it can't be chosen now, as a Job that
// has finished, or when what it presents depends on an object lookup
// couldn't read.
type Selectable struct {
	Kind      resourceid.GroupKind
	Candidate func(obj runtime.Object, lookup Lookup) (c Candidate, ok bool)
}

func (s Selector) selects(gk resourceid.GroupKind) bool {
	return len(s.Selects) == 0 || slices.Contains(s.Selects, gk)
}

func (f *SelectorFamily) selector(gk resourceid.GroupKind) int {
	return slices.IndexFunc(f.Selectors, func(s Selector) bool { return s.Kind == gk })
}

func (f *SelectorFamily) selectable(gk resourceid.GroupKind) int {
	return slices.IndexFunc(f.Selectables, func(s Selectable) bool { return s.Kind == gk })
}

// Covers reports whether f says anything about an object of gk.
func (f *SelectorFamily) Covers(gk resourceid.GroupKind) bool {
	return f.selector(gk) >= 0 || f.selectable(gk) >= 0
}

// MatchesAnswer is the objects one side of a selector family matches, as
// the caller may read them, up to the view's limit with a total.
type MatchesAnswer struct {
	Matches  []resourceid.Ref
	Coverage Coverage
}

// lookup reads objects for an answer whose coverage is c.
func (v *View) lookup(c *Coverage) Lookup {
	return func(ref resourceid.Ref) (runtime.Object, bool) {
		if !v.mayRead(ref.GroupKind(), ref.Namespace, c) {
			return nil, false
		}
		obj, read := v.session.Borrow(ref)
		switch read.State {
		case ownership.Present:
			return obj, true
		case ownership.Unavailable:
			c.addGap(read.Gap)
		}
		return nil, false
	}
}

func (v *View) candidate(s Selectable, o IndexedObject, c *Coverage) (Candidate, bool) {
	cand, ok := s.Candidate(o.Object, v.lookup(c))
	cand.Ref = o.Ref
	return cand, ok
}

// match reports whether selection chooses cand, recording in c that it
// couldn't tell when it can't.
func (v *View) match(selection Selection, selector resourceid.Ref, cand Candidate, c *Coverage) bool {
	chosen, decided := selection.Matches(cand, v.lookup(c))
	if !decided {
		c.addGap(ownership.Gap{Kind: selector.GroupKind(), Namespace: selector.Namespace, Reason: readreason.FilterEvalFailed})
	}
	return chosen && decided
}

// Selected answers what subject, a selector of f, chooses. It answers only
// for a subject Radar holds, as Referrers does.
func (v *View) Selected(f *SelectorFamily, subject resourceid.Ref, fetched runtime.Object) MatchesAnswer {
	var a MatchesAnswer
	i := f.selector(subject.GroupKind())
	if i < 0 {
		return a
	}
	obj, held := v.heldSubject(subject, fetched, &a.Coverage)
	if !held {
		return a
	}
	sel := f.Selectors[i]
	selection, ok, err := sel.Selection(obj)
	if err != nil {
		a.Coverage.addGap(ownership.Gap{Kind: subject.GroupKind(), Namespace: subject.Namespace, Reason: readreason.ReadError})
	}
	if !ok {
		return a
	}
	var s search
	switch {
	case selection.AnyNamespace:
		s = v.newSearch(subject.Namespace, true, &a.Coverage)
	case len(selection.Namespaces) > 0:
		s = v.newSearchIn(selection.Namespaces, &a.Coverage)
	default:
		s = v.newSearch(subject.Namespace, false, &a.Coverage)
	}
	for _, target := range f.Selectables {
		if !sel.selects(target.Kind) {
			continue
		}
		s.each(target.Kind, &a.Coverage, func(o IndexedObject) {
			if !selection.chooses(subject.Namespace, o.Ref.Namespace) {
				return
			}
			if cand, ok := v.candidate(target, o, &a.Coverage); ok && v.match(selection, subject, cand, &a.Coverage) {
				a.Matches = append(a.Matches, o.Ref)
			}
		})
	}
	v.order(&a, f.Identity, func(gk resourceid.GroupKind) int { return f.selectable(gk) })
	return a
}

// SelectedBy answers what chooses subject, a selectable of f, among the
// selectors in its namespace, or, for a CrossNamespace family, in every
// namespace the caller may read. It answers only for a subject Radar holds.
func (v *View) SelectedBy(f *SelectorFamily, subject resourceid.Ref, fetched runtime.Object) MatchesAnswer {
	var a MatchesAnswer
	i := f.selectable(subject.GroupKind())
	if i < 0 {
		return a
	}
	obj, held := v.heldSubject(subject, fetched, &a.Coverage)
	if !held {
		return a
	}
	cand, ok := v.candidate(f.Selectables[i], IndexedObject{Ref: subject, Object: obj}, &a.Coverage)
	if !ok {
		return a
	}
	s := v.newSearch(subject.Namespace, f.CrossNamespace, &a.Coverage)
	for _, sel := range f.Selectors {
		if !sel.selects(subject.GroupKind()) {
			continue
		}
		s.each(sel.Kind, &a.Coverage, func(o IndexedObject) {
			selection, ok, err := sel.Selection(o.Object)
			if err != nil {
				a.Coverage.addGap(ownership.Gap{Kind: sel.Kind, Namespace: o.Ref.Namespace, Reason: readreason.ReadError})
			}
			if ok && selection.chooses(o.Ref.Namespace, subject.Namespace) && v.match(selection, o.Ref, cand, &a.Coverage) {
				a.Matches = append(a.Matches, o.Ref)
			}
		})
	}
	v.order(&a, f.Identity, func(gk resourceid.GroupKind) int { return f.selector(gk) })
	return a
}

// order sorts a's matches by kind rank, namespace and name, lists each
// identity once, and applies the view's limit.
func (v *View) order(a *MatchesAnswer, identity func(resourceid.Ref) resourceid.Ref, rank func(resourceid.GroupKind) int) {
	slices.SortStableFunc(a.Matches, func(x, y resourceid.Ref) int {
		return cmp.Or(cmp.Compare(rank(x.GroupKind()), rank(y.GroupKind())), cmp.Compare(x.Namespace, y.Namespace), cmp.Compare(x.Name, y.Name))
	})
	if identity != nil {
		seen := map[resourceid.Ref]bool{}
		a.Matches = slices.DeleteFunc(a.Matches, func(r resourceid.Ref) bool {
			id := identity(r)
			if seen[id] {
				return true
			}
			seen[id] = true
			return false
		})
	}
	if v.limit > 0 && len(a.Matches) > v.limit {
		a.Coverage.Truncated = true
		a.Coverage.Total = len(a.Matches)
		a.Matches = a.Matches[:v.limit]
	}
}
