package resourceid

import "sort"

// Catalog answers which API groups currently serve a Kind. Discovery
// implements it; tests use a fixed map.
type Catalog interface {
	// GroupsForKind returns the groups serving kind ("" for core).
	GroupsForKind(kind string) []string
	// Complete reports whether the answer covers every API group. Discovery
	// that failed for some groups is incomplete: a single match may hide
	// another group that serves the same Kind.
	Complete() bool
}

// Resolution says how a Result's group was established.
type Resolution uint8

const (
	// Unresolved: the group is missing and could not be established.
	Unresolved Resolution = iota
	// Resolved: the reference itself carried the group.
	Resolved
	// InferredBuiltin: the group was missing and the Kind is built-in, so the
	// built-in group was chosen. This is Radar's policy; kubectl usually lands on
	// the same group because discovery lists built-in groups first.
	InferredBuiltin
	// InferredUnique: the group was missing and exactly one group serves the
	// Kind right now.
	InferredUnique
	// Ambiguous: the group was missing and several groups serve the Kind.
	Ambiguous
)

// String names the resolution for logs and test failures.
func (r Resolution) String() string {
	switch r {
	case Resolved:
		return "resolved"
	case InferredBuiltin:
		return "inferred-builtin"
	case InferredUnique:
		return "inferred-unique"
	case Ambiguous:
		return "ambiguous"
	default:
		return "unresolved"
	}
}

// Result is the outcome of resolving a Reference. Ref is meaningful only when
// OK is true; for other outcomes its Group is "" and must not be used as the
// core group.
type Result struct {
	Ref        Ref
	Resolution Resolution
	// Candidates are the groups that serve the Kind when the group had to be
	// chosen: the competing groups for Ambiguous, and any groups a built-in
	// Kind is shadowed by for InferredBuiltin.
	Candidates []string
	// Uncertain marks an InferredUnique result drawn from an incomplete
	// catalog.
	Uncertain bool
	// UID carries the reference's incarnation through resolution.
	UID string
}

// OK reports whether the result names one object.
func (r Result) OK() bool {
	return r.Resolution == Resolved || r.Resolution == InferredBuiltin || r.Resolution == InferredUnique
}

// ResolveCurrent resolves a reference against the cluster as it is now, for
// addressing: a user or tool request that omits a group, or a live node that
// lacks an apiVersion. Ownership and attribution must not be established this
// way — they need evidence that carries the group (Reference.HasGroup), so an
// inferred group never turns into a claimed relationship:
//
//  1. A group the reference carries wins.
//  2. A built-in Kind resolves to its built-in group, even when a CRD also
//     serves that Kind.
//  3. Otherwise a Kind served by exactly one group resolves to that group,
//     flagged Uncertain when the catalog is incomplete.
//  4. Otherwise the result is Ambiguous, or Unresolved when no group serves
//     the Kind or no catalog is given.
func ResolveCurrent(ref Reference, catalog Catalog) Result {
	res := Result{Ref: Ref{Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}, UID: ref.UID}
	if ref.HasGroup() {
		res.Ref.Group = NormalizeGroup(ref.Group)
		res.Resolution = Resolved
		return res
	}
	var groups []string
	if catalog != nil {
		groups = normalizedGroups(catalog.GroupsForKind(ref.Kind))
	}
	if group, ok := BuiltinGroup(ref.Kind); ok {
		res.Ref.Group = group
		res.Resolution = InferredBuiltin
		for _, g := range groups {
			if g != group {
				res.Candidates = append(res.Candidates, g)
			}
		}
		return res
	}
	switch len(groups) {
	case 0:
		res.Resolution = Unresolved
	case 1:
		res.Ref.Group = groups[0]
		res.Resolution = InferredUnique
		res.Uncertain = !catalog.Complete()
	default:
		res.Resolution = Ambiguous
		res.Candidates = groups
	}
	return res
}

// ResolveHistorical resolves a stored observation. It never infers: which
// groups serve a Kind today says nothing about the object an old event or
// persisted row described, so a missing group stays Unresolved.
func ResolveHistorical(ref Reference) Result {
	res := Result{Ref: Ref{Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}, UID: ref.UID}
	if ref.HasGroup() {
		res.Ref.Group = NormalizeGroup(ref.Group)
		res.Resolution = Resolved
	}
	return res
}

func normalizedGroups(groups []string) []string {
	seen := make(map[string]bool, len(groups))
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		g = NormalizeGroup(g)
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	sort.Strings(out)
	return out
}
