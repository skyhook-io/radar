package issues

import (
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/subject"
)

// resourceKey is the canonical group|kind|namespace|name key, shared with
// pkg/resourceid so issue grouping and audit deep-links never drift apart.
func resourceKey(group, kind, namespace, name string) string {
	return resourceid.ResourceKey(group, kind, namespace, name)
}

// enrichIdentity derives the grouping subject, scope, and deterministic ID for a
// classified issue via the shared pkg/subject resolver. The subject is the
// topmost owner when one was resolved (member pods collapse under their
// workload), otherwise the resource itself — issues' owner is pre-resolved by
// the detectors, so this is a depth-0 use of the shared Subject identity. Must
// run after classifyIssue (the category is part of the ID). subject.StableID is
// byte-identical to the previous local hash, so no existing issue re-keys.
func enrichIdentity(i *Issue) {
	subjRef := Ref{Group: i.Group, Kind: i.Kind, Namespace: i.Namespace, Name: i.Name}
	if i.Owner.Kind != "" {
		subjRef = i.Owner
	}
	scope := subject.ScopeForKind(subjRef.Kind)
	i.GroupingScope = issuesapi.Scope(scope)
	i.ID = subject.StableID(scope, resourceKey(subjRef.Group, subjRef.Kind, subjRef.Namespace, subjRef.Name), discriminator(i))
}

// discriminator is the cause portion of the issue ID. Category alone is the
// user-facing rollup but too coarse to be the durable identity for categories
// where one subject can have several distinct causes: it would collapse them
// into one row and drop every cause but the representative's. So:
//   - a detector-supplied stable Fingerprint (missing-ref target) splits each
//     distinct cause into its own issue;
//   - unknown has no curated grouping, so key on source+reason;
//   - every other (single-cause) category stays category-only — byte-identical
//     to the prior ID, so those issues do not re-key.
func discriminator(i *Issue) string {
	switch {
	case i.Fingerprint != "":
		return string(i.Category) + "\x00" + i.Fingerprint
	case i.Category == issuesapi.CategoryUnknown:
		return string(i.Category) + "\x00" + string(i.Source) + "\x00" + i.Reason
	default:
		return string(i.Category)
	}
}
