package issues

import "time"

// EvidenceView is how a finding that rests on RequiredReads reaches a caller.
type EvidenceView int

const (
	EvidenceFull EvidenceView = iota
	EvidenceDegraded
	EvidenceWithheld
)

// ViewEvidence decides how a caller sees a finding with inventory
// dependencies: in full when it can make every read, degraded when it can't
// and the finding has a degraded form, otherwise not at all. A nil authorizer
// can't make any read, unless a shared internal memo keeps findings in full
// for authorization at its per-user projection.
func ViewEvidence(reads []EvidenceRead, degraded *DegradedFinding, canRead func(EvidenceRead) bool, allowUnfiltered bool) EvidenceView {
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
	case readable:
		return EvidenceFull
	case degraded != nil:
		return EvidenceDegraded
	}
	return EvidenceWithheld
}

// ForEvidence returns the issue as the caller sees it, and false when the
// caller doesn't get it.
func ForEvidence(issue Issue, canRead func(EvidenceRead) bool, allowUnfiltered bool) (Issue, bool) {
	switch ViewEvidence(issue.RequiredReads, issue.Degraded, canRead, allowUnfiltered) {
	case EvidenceWithheld:
		return issue, false
	case EvidenceDegraded:
		return degradeIssue(issue), true
	}
	return issue, true
}

// degradeIssue tells the finding without its evidence. The onset goes too:
// it is when the evidence changed.
func degradeIssue(issue Issue) Issue {
	d := issue.Degraded
	issue.Message, issue.RawMessage, issue.Cause, issue.Action = d.Message, d.Message, d.Cause, d.Action
	issue.FirstSeen, issue.OnsetUnknown, issue.OnsetCoverage = time.Time{}, true, nil
	issue.IssueTiming, issue.IssueTimingBasis = "", ""
	issue.RequiredReads, issue.Degraded = nil, nil
	return issue
}

func applyEvidenceAccess(list []Issue, canRead func(EvidenceRead) bool, allowUnfiltered bool, onWithheld func(Issue)) []Issue {
	out := list[:0]
	for _, issue := range list {
		if shown, ok := ForEvidence(issue, canRead, allowUnfiltered); ok {
			out = append(out, shown)
		} else if onWithheld != nil {
			onWithheld(issue)
		}
	}
	return out
}
