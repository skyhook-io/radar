package issues

// Findings with inventory dependencies require an authorizer. A shared internal
// memo may explicitly retain them for authorization at its per-user projection.
func CanReadIssueEvidence(issue Issue, canRead func(EvidenceRead) bool, allowUnfiltered bool) bool {
	if canRead == nil {
		return allowUnfiltered || len(issue.RequiredReads) == 0
	}
	for _, read := range issue.RequiredReads {
		if !canRead(read) {
			return false
		}
	}
	return true
}

func filterEvidenceAccess(list []Issue, canRead func(EvidenceRead) bool, allowUnfiltered bool) []Issue {
	out := list[:0]
	for _, issue := range list {
		if CanReadIssueEvidence(issue, canRead, allowUnfiltered) {
			out = append(out, issue)
		}
	}
	return out
}
