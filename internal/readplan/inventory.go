package readplan

import "slices"

// CollectionScope retains intended collection, including namespaces still
// loading or failed. Outside alone is removed from a finite target; unrelated
// access gaps must stay within that target.
type CollectionScope struct {
	Namespaces []string
	Outside    []string
}

// Narrow returns the intended collection for an all-namespace target, or a
// finite target without namespaces outside collection. Nil and empty targets
// retain their distinct meanings; the input slices are not modified.
func (c CollectionScope) Narrow(target []string) []string {
	if target == nil {
		return slices.Clone(c.Namespaces)
	}
	outside := make(map[string]bool, len(c.Outside))
	for _, ns := range c.Outside {
		outside[ns] = true
	}
	return slices.DeleteFunc(slices.Clone(target), func(ns string) bool { return outside[ns] })
}

// Inventory is planned execution, not evidence that a read happened. All
// distinguishes a whole-scope read from finite Namespaces (always non-nil).
// CanReadTarget is true when any partition is readable or the finite target is
// empty. Gaps preserve planner extents until a transport projects them;
// Collection is non-nil only for narrowing.
type Inventory struct {
	Namespaces     []string
	All            bool
	CanReadTarget  bool
	WholeKindReady bool
	Gaps           []Partition
	Collection     *CollectionScope
}

// ForInventory projects cache facts for an inventory read. Explicit requests
// retain outside-collection namespaces as gaps; implicit requests narrow to
// intended collection, including namespaces that are not readable yet. Error
// precedence, wording and completed read coverage belong to the caller.
func ForInventory(plan Plan, target []string, explicit, clusterScoped bool) Inventory {
	out := Inventory{Namespaces: []string{}, WholeKindReady: plan.WholeKindReady,
		CanReadTarget: target != nil && len(target) == 0 && !clusterScoped}
	var intended, outside []string
	for _, part := range plan.Partitions {
		part.Namespaces = slices.Clone(part.Namespaces)
		if part.Verdict.State == OutsideCollection {
			outside = append(outside, part.Namespaces...)
			if explicit {
				out.Gaps = append(out.Gaps, part)
			}
			continue
		}
		intended = append(intended, part.Namespaces...)
		if part.Verdict.State == Readable {
			out.CanReadTarget = true
			if part.Extent == NamespaceSet {
				out.Namespaces = append(out.Namespaces, part.Namespaces...)
			} else {
				out.All = true
			}
		} else {
			out.Gaps = append(out.Gaps, part)
		}
	}
	slices.Sort(out.Namespaces)
	if !explicit && (plan.CollectionLimited || len(outside) > 0) {
		namespaces := slices.Compact(slices.Sorted(slices.Values(intended)))
		if namespaces == nil {
			namespaces = []string{}
		}
		out.Collection = &CollectionScope{Namespaces: namespaces, Outside: outside}
	}
	return out
}
