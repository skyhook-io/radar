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
	// Selectables are the kinds that can be chosen, by the labels each
	// presents, in the order answers list them.
	Selectables []Selectable
	// CrossNamespace: a selector may choose objects in other namespaces, as
	// a ServiceMonitor's namespaceSelector does.
	CrossNamespace bool
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

// Selection is what one selector chooses: the objects whose labels match,
// in its own namespace unless InNamespace says otherwise.
type Selection struct {
	Labels labels.Selector
	// InNamespace, when set, says which namespaces the selector chooses in;
	// otherwise it chooses only in its own.
	InNamespace func(namespace string) bool
}

func (s Selection) chooses(own, namespace string, set labels.Set) bool {
	if s.InNamespace == nil && namespace != own || s.InNamespace != nil && !s.InNamespace(namespace) {
		return false
	}
	return s.Labels.Matches(set)
}

// Selectable is a kind that can be chosen, and the labels it presents: a
// workload presents its pod template's. ok is false when the object can't
// be chosen now, as a Job that has finished.
type Selectable struct {
	Kind   resourceid.GroupKind
	Labels func(obj runtime.Object) (set labels.Set, ok bool)
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
	s := v.newSearch(subject.Namespace, selection.InNamespace != nil, &a.Coverage)
	for _, target := range f.Selectables {
		if !sel.selects(target.Kind) {
			continue
		}
		s.each(target.Kind, &a.Coverage, func(o IndexedObject) {
			if set, ok := target.Labels(o.Object); ok && selection.chooses(subject.Namespace, o.Ref.Namespace, set) {
				a.Matches = append(a.Matches, o.Ref)
			}
		})
	}
	v.order(&a, func(gk resourceid.GroupKind) int { return f.selectable(gk) })
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
	set, ok := f.Selectables[i].Labels(obj)
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
			if ok && selection.chooses(o.Ref.Namespace, subject.Namespace, set) {
				a.Matches = append(a.Matches, o.Ref)
			}
		})
	}
	v.order(&a, func(gk resourceid.GroupKind) int { return f.selector(gk) })
	return a
}

// order sorts a's matches by kind rank, namespace and name, and applies the
// view's limit.
func (v *View) order(a *MatchesAnswer, rank func(resourceid.GroupKind) int) {
	slices.SortFunc(a.Matches, func(x, y resourceid.Ref) int {
		return cmp.Or(cmp.Compare(rank(x.GroupKind()), rank(y.GroupKind())), cmp.Compare(x.Namespace, y.Namespace), cmp.Compare(x.Name, y.Name))
	})
	if v.limit > 0 && len(a.Matches) > v.limit {
		a.Coverage.Truncated = true
		a.Coverage.Total = len(a.Matches)
		a.Matches = a.Matches[:v.limit]
	}
}
