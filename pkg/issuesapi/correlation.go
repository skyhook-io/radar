package issuesapi

// CorrelationUnknownReason says why a subject carries neither correlated
// changes nor a no_recent_changes marker. A consumer must render it as "Radar
// can't tell", never as "nothing changed".
type CorrelationUnknownReason string

const (
	// CorrelationObservationTooShort: Radar has not been watching long enough
	// to make any claim about the lookback window.
	CorrelationObservationTooShort CorrelationUnknownReason = "observation_too_short"
	// CorrelationHistoryIncomplete: the change history for the window is
	// known to be partial — the store dropped older events, or the candidate
	// query hit its cap.
	CorrelationHistoryIncomplete CorrelationUnknownReason = "history_incomplete"
	// CorrelationUntrackedKind: the change feed does not record this kind's
	// changes, so "no changes" could not be claimed truthfully.
	CorrelationUntrackedKind CorrelationUnknownReason = "untracked_kind"
	// CorrelationCannotConfirm: relevant history exists that the caller may
	// not see. Deliberately does not say what, so it discloses nothing about
	// resources the caller cannot read.
	CorrelationCannotConfirm CorrelationUnknownReason = "cannot_confirm"
	// CorrelationLookupFailed: the history lookup errored.
	CorrelationLookupFailed CorrelationUnknownReason = "lookup_failed"
	// CorrelationNotPermitted: the caller may not read the subject itself.
	CorrelationNotPermitted CorrelationUnknownReason = "not_permitted"
)

// IssueCorrelationSubject identifies one issue subject to correlate. On the
// wire it is a subject=kind/group/namespace/name query value.
type IssueCorrelationSubject struct {
	Kind      string `json:"kind"`
	Group     string `json:"group,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// IssueCorrelation is one subject's answer: exactly one of CorrelatedChanges,
// NoRecentChanges or UnknownReason is set. Same semantics as the fields of the
// same names on Issue.
type IssueCorrelation struct {
	IssueCorrelationSubject
	CorrelatedChanges []RecentChange           `json:"correlated_changes,omitempty"`
	NoRecentChanges   *NoRecentChangesMarker   `json:"no_recent_changes,omitempty"`
	UnknownReason     CorrelationUnknownReason `json:"unknown_reason,omitempty"`
}

// IssueCorrelationResponse answers GET /api/issues/correlation, one result per
// requested subject, in request order.
type IssueCorrelationResponse struct {
	Results []IssueCorrelation `json:"results"`
}
