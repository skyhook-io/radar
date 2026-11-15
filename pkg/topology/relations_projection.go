package topology

import (
	"cmp"
	"slices"

	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// RelationCoverage says what a relation family's part of a Relationships
// couldn't cover. Every field is empty when the family's answer was
// complete.
type RelationCoverage struct {
	// NotChecked are the kinds and namespaces Radar couldn't read, and why.
	NotChecked []OwnershipGap `json:"notChecked,omitempty"`
	// Truncated: the family found more than the response lists; Total says
	// how many.
	Truncated bool `json:"truncated,omitempty"`
	Total     int  `json:"total,omitempty"`
	// Narrowed: the caller may not read some of what the family covers, so
	// only what it may read is listed.
	Narrowed bool `json:"narrowed,omitempty"`
}

func relationCoverage(c relations.Coverage) RelationCoverage {
	out := RelationCoverage{Truncated: c.Truncated, Total: c.Total, Narrowed: c.Narrowed}
	for _, g := range c.NotChecked {
		out.NotChecked = append(out.NotChecked, OwnershipGap{Group: g.Kind.Group, Kind: g.Kind.Kind, Namespace: g.Namespace, Reason: string(g.Reason)})
	}
	return out
}

func mergeCoverage(a, b RelationCoverage) RelationCoverage {
	out := RelationCoverage{Truncated: a.Truncated || b.Truncated, Narrowed: a.Narrowed || b.Narrowed}
	if out.Truncated {
		out.Total = a.Total + b.Total
	}
	out.NotChecked = append(slices.Clone(a.NotChecked), b.NotChecked...)
	slices.SortFunc(out.NotChecked, func(x, y OwnershipGap) int {
		return cmp.Or(cmp.Compare(x.Group, y.Group), cmp.Compare(x.Kind, y.Kind), cmp.Compare(x.Namespace, y.Namespace), cmp.Compare(x.Reason, y.Reason))
	})
	out.NotChecked = slices.Compact(out.NotChecked)
	return out
}

// ApplyConfigReferences adds the config-reference family's answers about
// subject to rel, beside what other families put in the same fields: the
// ConfigMaps and Secrets it declares to ConfigRefs, its claims to
// StorageRefs, a workload's ServiceAccount, and what declares it to
// Consumers. Only targets Radar holds and the caller may read are listed,
// as the graph listed them. rel.Coverage records the family's coverage.
// rel must come from GetRelationshipsWithOptions answering the family, so
// the graph's copy of it isn't added again.
func ApplyConfigReferences(rel *Relationships, subject resourceid.Ref, references relations.ConfigReferencesAnswer, consumers relations.ConfigConsumersAnswer) {
	for _, d := range references.Declared {
		if d.State != relations.TargetPresent {
			continue
		}
		ref := refOf(d.Ref)
		switch d.Ref.Kind {
		case "ConfigMap", "Secret":
			rel.ConfigRefs = appendResourceRef(rel.ConfigRefs, ref)
		case "PersistentVolumeClaim":
			rel.StorageRefs = appendResourceRef(rel.StorageRefs, ref)
		case "ServiceAccount":
			// A Pod's own ServiceAccount is set from its spec, whether or not
			// Radar holds it.
			if !(subject.Group == "" && subject.Kind == "Pod") {
				rel.ServiceAccount = &ref
			}
		}
	}
	for _, c := range consumers.Consumers {
		rel.Consumers = appendResourceRef(rel.Consumers, refOf(c.Ref))
	}
	if rel.Coverage == nil {
		rel.Coverage = map[string]RelationCoverage{}
	}
	rel.Coverage[ConfigReferenceFamily] = mergeCoverage(relationCoverage(references.Coverage), relationCoverage(consumers.Coverage))
}

// RelationFamilyUnavailable records that Radar couldn't answer family for
// rel at all, with a readreason code saying why.
func RelationFamilyUnavailable(rel *Relationships, family, reason string) {
	if rel.Coverage == nil {
		rel.Coverage = map[string]RelationCoverage{}
	}
	rel.Coverage[family] = RelationCoverage{NotChecked: []OwnershipGap{{Reason: reason}}}
}
