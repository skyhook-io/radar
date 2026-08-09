package issues

import (
	"strings"

	"github.com/skyhook-io/radar/internal/filter"
)

// CELFilter aliased so callers don't need a separate import to set
// Filters.Filter.
type CELFilter = filter.Filter

// Filters narrows a Compose call. Empty fields are unconstrained.
//
// This (and the CEL alias above) lives apart from the domain types in types.go
// so the data model — Issue, Severity, Source, Category, Ref — stays a pure
// leaf with no dependency on the CEL implementation; HTTP/MCP own compilation.
type Filters struct {
	Namespaces []string
	Severities []Severity
	Kinds      []string
	// IncludeClusterScopedKarpenter keeps cluster-scoped Karpenter rows when
	// Namespaces is non-empty. Public namespace-filtered issue queries leave this
	// false; mixed-scope internal projections such as Capacity opt in explicitly.
	IncludeClusterScopedKarpenter bool
	// Limit caps the returned slice. Zero means default (200).
	Limit int
	// Filter is an optional compiled CEL predicate evaluated against
	// each composed Issue's row bindings (source is exposed there, so a
	// power user can still slice by detection method). Compile happens in
	// the handler (and is cached); this layer just runs the program.
	Filter *CELFilter
	// CanReadClusterScoped authorizes cluster-scoped Issue rows before
	// they are returned. Handlers provide a per-user SAR-backed predicate;
	// nil preserves auth-mode=none and tests where the provider's own
	// permissions are the only gate.
	CanReadClusterScoped func(kind, group string) bool
	// CanReadRelated authorizes resources whose observed state is required to
	// assert an issue on another subject, such as a NodeClass referenced by a
	// NodePool. Nil preserves internal/no-auth composition.
	CanReadRelated func(Ref) bool
	// Grouped folds the flat rows into the public grouped model
	// (GroupIssues) before the cap, so the limit counts issue groups, not
	// replica fan-out. The public /api/issues + MCP issues set this; flat
	// callers (summarycontext per-resource index, /api/issues?view=flat)
	// leave it false.
	Grouped bool
}

// RelatedIssueOptions controls the per-resource issue lookup. CanReadRelated
// authorizes the subject of an issue reached through a cross-resource
// diagnostic reference. A nil predicate fails closed for those associations;
// direct subject and evidence-member matches remain governed by the caller's
// authorization of the requested resource.
type RelatedIssueOptions struct {
	Namespaces []string
	// CanReadClusterScoped mirrors Filters.CanReadClusterScoped — it gates
	// cluster-scoped Issue rows AND the cluster-scoped state (NodePool specs)
	// folded into per-issue signals such as capacity relevance. Omitting it here
	// would let a caller who cannot list NodePools read that state through the
	// per-resource path. Nil preserves auth-mode=none and internal composition.
	CanReadClusterScoped func(kind, group string) bool
	CanReadRelated       func(Ref) bool
}

const (
	DefaultLimit = 200
	MaxLimit     = 1000
	// NoLimit disables the result cap. Pass as Filters.Limit when the
	// caller needs the full matched set (e.g. building a per-resource
	// issue index for summaryContext — capping there would silently zero
	// out counts for resources whose issues fall in the tail beyond
	// MaxLimit on large clusters). Stats.TotalMatched is reliable
	// regardless; this just turns off the post-sort slice.
	NoLimit = -1
)

func KindFilterIncludes(kinds []string, canonical string, aliases ...string) bool {
	if len(kinds) == 0 {
		return true
	}
	want := map[string]bool{strings.ToLower(canonical): true}
	for _, alias := range aliases {
		want[strings.ToLower(alias)] = true
	}
	for _, kind := range kinds {
		if want[strings.ToLower(strings.TrimSpace(kind))] {
			return true
		}
	}
	return false
}
