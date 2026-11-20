package issues

import "time"

// EvidenceView is how a finding that rests on RequiredReads reaches a caller.
type EvidenceView int

const (
	EvidenceFull EvidenceView = iota
	EvidenceDegraded
	EvidenceWithheld
	// EvidenceExpected: the caller can make every read, and they show the
	// finding is expected, so the caller doesn't get it. Not withholding.
	EvidenceExpected
)

// ViewEvidence decides how a caller sees a finding with inventory
// dependencies: in full when it can make every read, degraded when it can't
// and the finding has a degraded form, otherwise not at all. A nil authorizer
// can't make any read, unless a shared internal memo keeps findings in full
// for authorization at its per-user projection. An expected finding a caller
// can verify is EvidenceExpected.
func ViewEvidence(reads []EvidenceRead, degraded *DegradedFinding, expected bool, canRead func(EvidenceRead) bool, allowUnfiltered bool) EvidenceView {
	if len(reads) == 0 || (canRead == nil && allowUnfiltered) {
		return EvidenceFull
	}
	readable := canRead != nil
	for _, read := range reads {
		if !readable {
			break
		}
		readable = canRead(read)
	}
	switch {
	case readable && expected:
		return EvidenceExpected
	case readable:
		return EvidenceFull
	case degraded != nil:
		return EvidenceDegraded
	}
	return EvidenceWithheld
}

// ExactEvidenceRead reports a namespaced read that namespace access does not
// authorize, so the evidence gates check the caller's own permission for it,
// as the views read those kinds: Secrets, and Velero storage locations, whose
// name and access mode a schedule's finding would otherwise reveal.
func ExactEvidenceRead(read EvidenceRead) bool {
	switch {
	case read.Group == "" && read.Resource == "secrets":
		return true
	case read.Group == VeleroGroup && read.Resource == "backupstoragelocations":
		return true
	}
	return false
}

// ForEvidence returns the issue as the caller sees it, and false when the
// caller doesn't get it.
func ForEvidence(issue Issue, canRead func(EvidenceRead) bool, allowUnfiltered bool) (Issue, bool) {
	shown, view := forEvidence(issue, canRead, allowUnfiltered)
	return shown, view == EvidenceFull || view == EvidenceDegraded
}

// degradeIssue tells the finding without its evidence. Evidence-derived onset
// goes too; a detector can retain timing owned by the readable subject itself.
func degradeIssue(issue Issue) Issue {
	d := issue.Degraded
	issue.Message, issue.Cause, issue.Action = d.Message, d.Cause, d.Action
	if issue.RawMessage != "" {
		issue.RawMessage = d.Message
	}
	issue.ActionPrimary, issue.ActionSecondaryTitle, issue.ActionSecondary = "", "", nil
	issue.Hazard = ""
	if !d.KeepSubjectOnset {
		issue.FirstSeen, issue.OnsetUnknown, issue.OnsetCoverage = time.Time{}, true, nil
		issue.IssueTiming, issue.IssueTimingBasis = "", ""
	}
	issue.RequiredReads, issue.Degraded = nil, nil
	return issue
}

func applyEvidenceAccess(list []Issue, canRead func(EvidenceRead) bool, allowUnfiltered bool, onWithheld func(Issue)) []Issue {
	out := list[:0]
	for _, issue := range list {
		switch shown, view := forEvidence(issue, canRead, allowUnfiltered); view {
		case EvidenceFull, EvidenceDegraded:
			out = append(out, shown)
		case EvidenceWithheld:
			if onWithheld != nil {
				onWithheld(issue)
			}
		}
	}
	return out
}

func forEvidence(issue Issue, canRead func(EvidenceRead) bool, allowUnfiltered bool) (Issue, EvidenceView) {
	view := ViewEvidence(issue.RequiredReads, issue.Degraded, issue.ExpectedWhenVerified, canRead, allowUnfiltered)
	if view == EvidenceDegraded {
		issue = degradeIssue(issue)
		issue.ExpectedWhenVerified = false
	}
	return issue, view
}
