package issuecorrelation

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// RowChange is the one change a collapsed issue row names, picked from the
// changes an anchored correlation selected for iss: the newest change at or
// before first seen (with the same slack the selection uses), or, when the
// issue has no first-seen time, the newest change in the window. A change
// after first seen is never picked. For a grouped issue whose members' onsets
// are mixed, the selection holds the window's newest changes, so a change
// before first seen that is older than those isn't found and the row names
// nothing.
//
// A row names nothing when part of the answer couldn't be read (a source the
// caller can't list, a failed read, sources Radar couldn't identify or isn't
// collecting): that part may hold a newer change, and the row has no room to
// say so. Incomplete history is the exception: what Radar no longer holds or
// never saw is older than anything it recorded, so the newest recorded change
// stands.
func RowChange(iss issuesapi.Issue) (issuesapi.RecentChange, bool) {
	if iss.Correlation != nil && iss.Correlation.Unknown != nil && iss.Correlation.Unknown.Reason != issuesapi.CorrelationUnknownHistoryIncomplete {
		return issuesapi.RecentChange{}, false
	}
	var pick issuesapi.RecentChange
	found := false
	boundary := iss.FirstSeen.Add(anchorSlop)
	for _, change := range iss.CorrelatedChanges {
		t := changeTime(change)
		if !iss.FirstSeen.IsZero() && t.After(boundary) {
			continue
		}
		if !found || t.After(changeTime(pick)) {
			pick, found = change, true
		}
	}
	if !found {
		return issuesapi.RecentChange{}, false
	}
	return rowFields(pick), true
}

// rowFields keeps what a row needs to name a change: the object, when, the
// change type and summary its verb is read from, the changed field paths,
// and the timeline event it links to. Field values are dropped except on
// Helm rows, whose revision says whether the release was installed or
// upgraded.
func rowFields(change issuesapi.RecentChange) issuesapi.RecentChange {
	out := issuesapi.RecentChange{
		Source:     change.Source,
		Kind:       change.Kind,
		APIVersion: change.APIVersion,
		Namespace:  change.Namespace,
		Name:       change.Name,
		ChangeType: change.ChangeType,
		Summary:    change.Summary,
		Timestamp:  change.Timestamp,
		RankReason: change.RankReason,
		EventID:    change.EventID,
	}
	for _, field := range change.Fields {
		kept := issuesapi.ChangeField{Path: field.Path}
		if change.Source == HelmChangeSource && strings.HasPrefix(field.Path, "helm.") {
			kept.OldValue, kept.NewValue = field.OldValue, field.NewValue
		}
		out.Fields = append(out.Fields, kept)
	}
	return out
}
