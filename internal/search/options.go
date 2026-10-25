package search

import (
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// Options configures a Search call. Per-user exact-kind authorization requires
// both TypedKindAccess and DynamicKindAccess: they protect separate paths and
// must share one request budget.
type Options struct {
	// Limit defaults to DefaultLimit when non-positive and is capped at MaxLimit.
	Limit int
	// Include defaults to IncludeSummary.
	Include IncludeMode
	// Selection must be resolved for this query, caller and pin against the
	// same captured provider. Use SelectNamespaces with the namespace check's
	// result, and set NamespaceCheckFailed when that check did not answer.
	Selection readscope.Selection
	// NamespaceCheckFailed suppresses namespaced reads and records unknown
	// access; it must agree with the check used to build Selection.
	NamespaceCheckFailed bool
	// TypedKindAccess partitions typed exact-kind access before reading collector
	// state. Nil disables the gate (auth off).
	// A nil allowed list means every namespace; denied and unchecked use ""
	// for a whole-scope decision when namespaces cannot be enumerated.
	TypedKindAccess func(namespaces []string, group, resource string) (allowed, denied, unchecked []string)
	// CanReadClusterScoped authorizes cluster-scoped resources before the
	// cache walker scans them. Handlers provide a per-user SAR-backed
	// decision; authoritative distinguishes denial from check failure. nil
	// preserves auth-mode=none behavior, where collector RBAC is the only gate.
	CanReadClusterScoped func(kind, group, resource string) (allowed, authoritative bool)
	// DynamicKindAccess authorizes namespaced kinds requiring exact access by the
	// caller's own list permission on that kind, within Selection.Namespaces. Both
	// ClusterWide and InNamespaces callbacks are required when non-nil. nil
	// disables the gate (auth off: the cache's own RBAC is the only filter).
	DynamicKindAccess *DynamicKindAccess
	// Filter is an optional compiled CEL predicate. When set, each
	// candidate that passed the modifier+token match is also evaluated
	// against the filter; non-truthy results (including eval errors)
	// drop the candidate. Compile happens in the handler; this layer
	// just runs the program.
	Filter *CELFilter
	// SummaryBuilder, when non-nil, is invoked once per returned hit after
	// sorting and limiting, to attach summaryContext (managedBy + health +
	// issueCount). Handlers provide a closure that wraps the
	// request-scoped topology + per-namespace issue index so the
	// per-row cost stays flat. Pass nil to opt out (context=none) —
	// the field is omitempty and consumers must tolerate its absence.
	SummaryBuilder SummaryBuilderFunc
}

// SelectNamespaces acquires missing-name facts only for unrestricted callers,
// before classifying access and the pin through the shared namespace policy.
func SelectNamespaces(p Provider, q Query, access readscope.NamespaceAccess, pin *string) readscope.Selection {
	var missing []string
	if access.Checked && access.Namespaces == nil && len(q.NSFilter) > 0 {
		missing = p.KnownMissingNamespaces(q.NSFilter)
	}
	selection, _ := readscope.ResolveNamespaces(q.NSFilter, access, pin, missing)
	// A failed check still narrows an implicit search; it cannot widen its scope.
	if !access.Checked && len(q.NSFilter) == 0 {
		if access.Namespaces != nil {
			selection.NarrowedBy = append(selection.NarrowedBy, resourceapi.NarrowedByUserAccess)
		}
		if pin != nil {
			selection.NarrowedBy = append(selection.NarrowedBy, resourceapi.NarrowedByNamespacePin)
		}
	}
	return selection
}
