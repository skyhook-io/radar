package ownershiptest

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Queries are the questions Conform asks both sessions: Object and Recorders
// of every ref, OwnerCandidates of every UID in every domain at every limit,
// and Inventory of every manager.
type Queries struct {
	Refs     []resourceid.Ref
	UIDs     []types.UID
	Domains  []ownership.Domain
	Limits   []int
	Managers []resourceid.Ref
}

// QueriesFor derives questions from w: every object, refs that miss in each
// way a ref can, every UID an object has or names, domains around every
// namespace, every manager and every member a manager lists.
func QueriesFor(w World) Queries {
	var q Queries
	seenRef := map[resourceid.Ref]bool{}
	addRef := func(r resourceid.Ref) {
		if !seenRef[r] {
			seenRef[r] = true
			q.Refs = append(q.Refs, r)
		}
	}
	seenUID := map[types.UID]bool{}
	addUID := func(uid types.UID) {
		if !seenUID[uid] {
			seenUID[uid] = true
			q.UIDs = append(q.UIDs, uid)
		}
	}
	var namespaces []string
	for _, o := range w.Objects {
		addRef(o.Ref)
		missing := o.Ref
		missing.Name += "-missing"
		addRef(missing)
		if o.Ref.Namespace != "" {
			elsewhere := o.Ref
			elsewhere.Namespace += "-elsewhere"
			addRef(elsewhere)
			noNamespace := o.Ref
			noNamespace.Namespace = ""
			addRef(noNamespace)
			namespaces = append(namespaces, o.Ref.Namespace)
		} else {
			inNamespace := o.Ref
			inNamespace.Namespace = "default"
			addRef(inNamespace)
		}
		addUID(o.UID)
		for _, owner := range o.Owners {
			addUID(owner.UID)
			group, _ := resourceid.SplitAPIVersion(owner.APIVersion)
			addRef(resourceid.Ref{Group: group, Kind: owner.Kind, Namespace: o.Ref.Namespace, Name: owner.Name})
			addRef(resourceid.Ref{Group: group, Kind: owner.Kind, Name: owner.Name})
		}
	}
	for _, o := range w.Objects {
		if ownership.IsRecordingManager(o.Ref.GroupKind()) {
			q.Managers = append(q.Managers, o.Ref)
			for _, m := range o.Members {
				addRef(m)
			}
		}
	}
	q.Managers = append(q.Managers,
		resourceid.Ref{Group: ownership.ArgoApplication.Group, Kind: ownership.ArgoApplication.Kind, Namespace: "argocd", Name: "no-such-app"},
		resourceid.Ref{Group: ownership.FluxKustomization.Group, Kind: ownership.FluxKustomization.Kind, Name: "no-namespace"},
		resourceid.Ref{Kind: "ConfigMap", Namespace: "alpha", Name: "cfg"},
	)
	addRef(resourceid.Ref{Group: "example.io", Kind: "Unserved", Namespace: "default", Name: "x"})
	addRef(resourceid.Ref{Kind: "Pod", Namespace: "default"})
	addRef(resourceid.Ref{Namespace: "default", Name: "no-kind"})
	addUID("uid-nothing-names")
	addUID("")

	slices.Sort(namespaces)
	namespaces = slices.Compact(namespaces)
	q.Domains = []ownership.Domain{
		ownership.Everywhere(),
		{AllNamespaces: true},
		{ClusterScoped: true},
		{},
		{Namespaces: []string{"no-such-namespace"}},
	}
	for _, ns := range namespaces {
		q.Domains = append(q.Domains, ownership.InNamespace(ns))
	}
	if len(namespaces) > 0 {
		// AllNamespaces wins over a namespace list; an empty entry never
		// means every namespace.
		q.Domains = append(q.Domains,
			ownership.Domain{AllNamespaces: true, Namespaces: namespaces[:1]},
			ownership.Domain{Namespaces: []string{"", namespaces[0]}},
		)
	}
	q.Domains = append(q.Domains, ownership.Domain{Namespaces: []string{""}, ClusterScoped: true})
	if len(namespaces) >= 2 {
		q.Domains = append(q.Domains, ownership.Domain{Namespaces: []string{namespaces[1], namespaces[0], namespaces[1]}, ClusterScoped: true})
	}
	q.Limits = []int{0, 1}
	return q
}

// Asked names a Session question.
type Asked uint8

const (
	AskedObject Asked = iota
	AskedCandidates
	AskedRecorders
	AskedInventory
)

// Difference is one question two sessions answered differently.
type Difference struct {
	Question  string
	Want, Got string
	// Objects are the objects the answers disagree about.
	Objects []resourceid.Ref
	// Coverage: the answers disagree about what was read (gaps or
	// truncation), not only about objects.
	Coverage bool
	// The question itself: Object(Ref), OwnerCandidates(UID, Domain,
	// Limit), Recorders(Ref) or Inventory(Ref).
	Asked  Asked
	Ref    resourceid.Ref
	UID    types.UID
	Domain ownership.Domain
	Limit  int
	// WantGaps are the gaps the first session reported for a search.
	WantGaps []ownership.Gap
}

func (d Difference) String() string {
	return fmt.Sprintf("%s:\n  want %s\n  got  %s", d.Question, d.Want, d.Got)
}

// Differences returns every question on which got answers differently from
// want.
func Differences(want, got ownership.Session, q Queries) []Difference {
	var diffs []Difference
	for _, ref := range q.Refs {
		if w, g := want.Object(ref), got.Object(ref); !sameRead(w, g) {
			unread := func(r ownership.ObjectRead) bool {
				return r.State == ownership.Unavailable || r.State == ownership.Excluded
			}
			diffs = append(diffs, Difference{Question: fmt.Sprintf("Object(%v)", ref), Want: describeRead(w), Got: describeRead(g), Objects: []resourceid.Ref{ref},
				Coverage: unread(w) || unread(g), Asked: AskedObject, Ref: ref})
		}
	}
	for _, ref := range q.Refs {
		if w, g := want.Recorders(ref), got.Recorders(ref); !sameRecorders(w, g) {
			diffs = append(diffs, Difference{Question: fmt.Sprintf("Recorders(%v)", ref), Want: describeRecorders(w), Got: describeRecorders(g),
				Objects: differingRecorders(w, g), Coverage: !slices.Equal(w.Gaps, g.Gaps), Asked: AskedRecorders, Ref: ref, WantGaps: w.Gaps})
		}
	}
	for _, m := range q.Managers {
		if w, g := want.Inventory(m), got.Inventory(m); !sameInventory(w, g) {
			unread := func(r ownership.InventoryRead) bool { return r.Manager.State == ownership.Unavailable }
			diffs = append(diffs, Difference{Question: fmt.Sprintf("Inventory(%v)", m), Want: describeInventory(w), Got: describeInventory(g), Objects: []resourceid.Ref{m},
				Coverage: unread(w) || unread(g), Asked: AskedInventory, Ref: m})
		}
	}
	for _, uid := range q.UIDs {
		for _, d := range q.Domains {
			for _, limit := range q.Limits {
				w, g := want.OwnerCandidates(uid, d, limit), got.OwnerCandidates(uid, d, limit)
				if !sameCandidates(w, g) {
					diffs = append(diffs, Difference{
						Question: fmt.Sprintf("OwnerCandidates(%q, %+v, %d)", uid, d, limit),
						Want:     describeCandidates(w), Got: describeCandidates(g),
						Objects:  differingCandidates(w, g),
						Coverage: w.Truncated != g.Truncated || !slices.Equal(w.Gaps, g.Gaps),
						Asked:    AskedCandidates, UID: uid, Domain: d, Limit: limit, WantGaps: w.Gaps,
					})
				}
			}
		}
	}
	return diffs
}

// Conform fails t for every question on which got answers differently from
// want. It returns the number of differences.
func Conform(t testing.TB, want, got ownership.Session, q Queries) int {
	t.Helper()
	diffs := Differences(want, got, q)
	const shown = 20
	for i, d := range diffs {
		if i == shown {
			t.Errorf("... and %d more differences", len(diffs)-shown)
			break
		}
		t.Error(d)
	}
	return len(diffs)
}

// differingCandidates returns the objects one answer has and the other
// doesn't, or has differently.
func differingCandidates(a, b ownership.CandidateRead) []resourceid.Ref {
	seen := map[resourceid.Ref]ownership.Observation{}
	for _, c := range a.Candidates {
		seen[c.Ref] = c
	}
	var out []resourceid.Ref
	for _, c := range b.Candidates {
		if other, ok := seen[c.Ref]; !ok || !sameObservation(other, c) {
			out = append(out, c.Ref)
		}
		delete(seen, c.Ref)
	}
	for ref := range seen {
		out = append(out, ref)
	}
	slices.SortFunc(out, compareRefs)
	return out
}

// differingRecorders returns the managers one answer lists as recording, or
// as unreadable, and the other doesn't, or lists differently.
func differingRecorders(a, b ownership.RecorderRead) []resourceid.Ref {
	out := differingCandidates(ownership.CandidateRead{Candidates: a.Recorders}, ownership.CandidateRead{Candidates: b.Recorders})
	for _, ref := range a.Unreadable {
		if !slices.Contains(b.Unreadable, ref) {
			out = append(out, ref)
		}
	}
	for _, ref := range b.Unreadable {
		if !slices.Contains(a.Unreadable, ref) {
			out = append(out, ref)
		}
	}
	slices.SortFunc(out, compareRefs)
	return slices.Compact(out)
}

func sameRecorders(a, b ownership.RecorderRead) bool {
	if !slices.Equal(a.Gaps, b.Gaps) || !slices.Equal(a.Unreadable, b.Unreadable) || len(a.Recorders) != len(b.Recorders) {
		return false
	}
	for i := range a.Recorders {
		if !sameObservation(a.Recorders[i], b.Recorders[i]) {
			return false
		}
	}
	return true
}

func sameInventory(a, b ownership.InventoryRead) bool {
	return sameRead(a.Manager, b.Manager) && a.Membership == b.Membership && slices.Equal(a.Members, b.Members) && (a.Members == nil) == (b.Members == nil)
}

func describeRecorders(r ownership.RecorderRead) string {
	var refs []string
	for _, m := range r.Recorders {
		refs = append(refs, describeObservation(m))
	}
	return fmt.Sprintf("recorders [%s] unreadable %v gaps %+v", strings.Join(refs, ", "), r.Unreadable, r.Gaps)
}

func describeInventory(r ownership.InventoryRead) string {
	if r.Manager.State != ownership.Present {
		return "manager " + describeRead(r.Manager)
	}
	if r.Membership != ownership.MembersRecorded {
		return r.Membership.String()
	}
	return fmt.Sprintf("recorded %v", r.Members)
}

func sameRead(a, b ownership.ObjectRead) bool {
	return a.State == b.State && a.Gap == b.Gap && sameObservation(a.Object, b.Object)
}

func sameCandidates(a, b ownership.CandidateRead) bool {
	if a.Truncated != b.Truncated || !slices.Equal(a.Gaps, b.Gaps) || len(a.Candidates) != len(b.Candidates) {
		return false
	}
	for i := range a.Candidates {
		if !sameObservation(a.Candidates[i], b.Candidates[i]) {
			return false
		}
	}
	return true
}

// SameObservation reports whether two observations are of the same object
// in the same state, comparing timestamps by instant.
func SameObservation(a, b ownership.Observation) bool {
	return sameObservation(a, b)
}

func sameObservation(a, b ownership.Observation) bool {
	at, bt := a.DeletionTimestamp, b.DeletionTimestamp
	if (at == nil) != (bt == nil) || at != nil && !at.Equal(bt) {
		return false
	}
	a.DeletionTimestamp, b.DeletionTimestamp = nil, nil
	if len(a.Finalizers) == 0 && len(b.Finalizers) == 0 {
		a.Finalizers, b.Finalizers = nil, nil
	}
	if len(a.OwnerReferences) == 0 && len(b.OwnerReferences) == 0 {
		a.OwnerReferences, b.OwnerReferences = nil, nil
	}
	return reflect.DeepEqual(a, b)
}

func describeRead(r ownership.ObjectRead) string {
	switch r.State {
	case ownership.Present:
		return "present " + describeObservation(r.Object)
	case ownership.Absent:
		return "absent"
	case ownership.Excluded:
		return "excluded"
	}
	return fmt.Sprintf("unavailable %+v", r.Gap)
}

func describeCandidates(r ownership.CandidateRead) string {
	var refs []string
	for _, c := range r.Candidates {
		refs = append(refs, describeObservation(c))
	}
	return fmt.Sprintf("candidates [%s] gaps %+v truncated %v", strings.Join(refs, ", "), r.Gaps, r.Truncated)
}

func describeObservation(o ownership.Observation) string {
	var owners []string
	for _, ref := range o.OwnerReferences {
		flags := ""
		if ref.Controller != nil {
			flags += fmt.Sprintf(" controller=%v", *ref.Controller)
		}
		if ref.BlockOwnerDeletion != nil {
			flags += fmt.Sprintf(" block=%v", *ref.BlockOwnerDeletion)
		}
		owners = append(owners, fmt.Sprintf("%s %s/%s %s%s", ref.APIVersion, ref.Kind, ref.Name, ref.UID, flags))
	}
	deleting := ""
	if o.DeletionTimestamp != nil {
		deleting = " deleting " + o.DeletionTimestamp.UTC().Format("15:04:05")
	}
	return fmt.Sprintf("%v@%s uid=%s rv=%s owners=[%s] finalizers=%v%s", o.Ref, o.APIVersion, o.UID, o.ResourceVersion, strings.Join(owners, "; "), o.Finalizers, deleting)
}
