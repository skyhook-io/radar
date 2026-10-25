package readscope

import (
	"slices"

	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// NamespaceAccess is the caller's ceiling: nil namespaces means unrestricted,
// non-nil empty means no access. Checked distinguishes a denial from no answer.
type NamespaceAccess struct {
	Namespaces []string
	Checked    bool
}

// NamespaceGap names only namespaces the caller requested.
type NamespaceGap struct {
	Code       readreason.Code
	Namespaces []string
}

// Selection records the request and what it may read. Named is nil for an
// implicit request. Namespaces is nil for every namespace, non-nil empty for
// nothing to read.
type Selection struct {
	Named      []string
	Namespaces []string
	Gaps       []NamespaceGap
	NarrowedBy []resourceapi.Narrowing
}

// ResolveNamespaces classifies a request within the caller's access and pin.
// A nil pin means no pin; &"" pins to nothing readable; &"ns" pins to ns.
// Nil requested means implicit all; non-nil empty normally means no members,
// except that a restricted caller without a pin gets its full access ceiling.
// Missing names are considered only for unrestricted callers, before the pin;
// hidden names never disclose either existence or the pin. The returned code
// describes a scope-level failure; rejected named namespaces remain in Gaps.
func ResolveNamespaces(requested []string, access NamespaceAccess, pin *string, knownMissing []string) (Selection, readreason.Code) {
	s := Selection{Named: slices.Clone(requested), Namespaces: []string{}}
	if !access.Checked {
		return s, readreason.UserPermissionCheckFailed
	}
	if requested == nil {
		if access.Namespaces != nil {
			s.NarrowedBy = append(s.NarrowedBy, resourceapi.NarrowedByUserAccess)
		}
		if pin != nil {
			s.NarrowedBy = append(s.NarrowedBy, resourceapi.NarrowedByNamespacePin)
		}
		s.Namespaces = slices.Clone(access.Namespaces)
		if pin != nil {
			s.Namespaces = []string{}
			if *pin != "" && (access.Namespaces == nil || slices.Contains(access.Namespaces, *pin)) {
				s.Namespaces = []string{*pin}
			}
		}
		if NoNamespaceAccess(s.Namespaces) {
			return s, readreason.UserNoNamespaceAccess
		}
		return s, ""
	}
	slices.Sort(s.Named)
	s.Named = slices.Compact(s.Named)
	visible := make(map[string]bool, len(access.Namespaces))
	for _, ns := range access.Namespaces {
		visible[ns] = true
	}
	missing := make(map[string]bool, len(knownMissing))
	if access.Namespaces == nil {
		for _, ns := range knownMissing {
			missing[ns] = true
		}
	}
	var absent, outside, hidden []string
	for _, ns := range s.Named {
		switch {
		case missing[ns]:
			absent = append(absent, ns)
		case access.Namespaces != nil && !visible[ns]:
			hidden = append(hidden, ns)
		case pin != nil && (*pin == "" || ns != *pin):
			outside = append(outside, ns)
		default:
			s.Namespaces = append(s.Namespaces, ns)
		}
	}
	for _, gap := range []NamespaceGap{
		{readreason.NamespaceNotFound, absent},
		{readreason.OutsideNamespacePin, outside},
		{readreason.UserNamespaceNotVisible, hidden},
	} {
		if len(gap.Namespaces) > 0 {
			s.Gaps = append(s.Gaps, gap)
		}
	}
	// Only an originally empty request inherits a restricted caller's ceiling;
	// a nonempty request filtered to nothing must stay empty.
	if len(requested) == 0 && pin == nil {
		s.Namespaces = slices.Clone(SelectNamespaces(access.Namespaces, requested))
	}
	return s, ""
}
