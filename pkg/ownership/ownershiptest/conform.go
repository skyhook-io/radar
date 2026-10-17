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

// Queries are the questions Conform asks both sessions.
type Queries struct {
	Refs    []resourceid.Ref
	UIDs    []types.UID
	Domains []ownership.Domain
	Limits  []int
}

// QueriesFor derives questions from w: every object, refs that miss in each
// way a ref can, every UID an object has or names, and domains around every
// namespace.
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

// Difference is one question two sessions answered differently.
type Difference struct {
	Question  string
	Want, Got string
	// Objects are the objects the answers disagree about.
	Objects []resourceid.Ref
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
			diffs = append(diffs, Difference{Question: fmt.Sprintf("Object(%v)", ref), Want: describeRead(w), Got: describeRead(g), Objects: []resourceid.Ref{ref}})
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
						Objects: differingCandidates(w, g),
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

// sameObservation compares observations, timestamps by instant.
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
