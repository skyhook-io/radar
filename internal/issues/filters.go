package issues

import "strings"

func applyFilters(in []Issue, f Filters) []Issue {
	if len(f.Severities) == 0 && len(f.Kinds) == 0 {
		return in
	}
	wantSev := map[Severity]bool{}
	for _, s := range f.Severities {
		wantSev[s] = true
	}
	wantKind := map[string]bool{}
	for _, k := range f.Kinds {
		wantKind[strings.ToLower(k)] = true
	}
	out := in[:0]
	for _, i := range in {
		if len(wantSev) > 0 && !wantSev[i.Severity] {
			continue
		}
		if len(wantKind) > 0 && !wantKind[strings.ToLower(i.Kind)] {
			continue
		}
		out = append(out, i)
	}
	return out
}

func applyClusterScopedAccess(in []Issue, f Filters) []Issue {
	if f.CanReadClusterScoped == nil {
		return in
	}
	out := make([]Issue, 0, len(in))
	for _, i := range in {
		if i.Namespace != "" {
			out = append(out, i)
			continue
		}
		// Namespace-less issue: must be cluster-scoped (a namespaced
		// resource without a namespace would be invalid wire data). Don't
		// pre-gate on a static cluster-scoped kind list here: dynamically
		// discovered cluster-scoped CRDs (e.g. Karpenter NodePool), already
		// classified cluster-scoped by their emitter, would be dropped for
		// authenticated users. CanReadClusterScoped (SAR-backed) is the
		// authoritative access gate; no pre-classification is needed.
		if f.CanReadClusterScoped(i.Kind, i.Group) {
			out = append(out, i)
		}
	}
	return out
}

// applySubjectAccess drops namespaced rows whose own resource the caller can't
// read. Cluster-scoped rows are applyClusterScopedAccess's.
func applySubjectAccess(in []Issue, canRead func(Ref) bool) []Issue {
	if canRead == nil {
		return in
	}
	out := make([]Issue, 0, len(in))
	for _, i := range in {
		if i.Namespace == "" || canRead(Ref{Group: i.Group, Kind: i.Kind, Namespace: i.Namespace, Name: i.Name}) {
			out = append(out, i)
		}
	}
	return out
}

func applyOwnerAccess(in []Issue, f Filters) []Issue {
	for idx := range in {
		in[idx] = WithReadableOwner(in[idx], f.CanReadSubject, f.CanReadClusterScoped)
	}
	return in
}

// WithReadableOwner makes a row whose owner the caller can't read its own
// subject. Grouping would otherwise publish the owner as the issue, and
// dropping the row would hide a failure of a resource the caller can read.
func WithReadableOwner(i Issue, canReadSubject func(Ref) bool, canReadClusterScoped func(kind, group string) bool) Issue {
	if i.Owner.Kind == "" {
		return i
	}
	readable := canReadSubject == nil || canReadSubject(i.Owner)
	if i.Owner.Namespace == "" {
		readable = canReadClusterScoped == nil || canReadClusterScoped(i.Owner.Kind, i.Owner.Group)
	}
	if !readable {
		i.Owner = Ref{}
		enrichIdentity(&i)
	}
	return i
}

// SeverityRank orders the normalized issue severity, higher = worse. Shared by
// the grouping comparators here and by the per-resource summary rollups in the
// server/MCP layers, so all three rank off one function.
func SeverityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	}
	return 0
}
