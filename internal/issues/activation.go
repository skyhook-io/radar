package issues

// issueToActivation projects an Issue into the top-level CEL bindings
// declared by internal/filter.envIssue. Time is exposed as a unix-second
// integer so the agent can compare with `last_seen > 1700000000` or
// `last_seen > timestamp("2025-01-01T00:00:00Z").getSeconds()` —
// CEL's int domain is the lowest-friction lingua franca.
func issueToActivation(i Issue) map[string]any {
	var lastSeen, firstSeen int64
	if !i.LastSeen.IsZero() {
		lastSeen = i.LastSeen.Unix()
	}
	if !i.FirstSeen.IsZero() {
		firstSeen = i.FirstSeen.Unix()
	}
	return map[string]any{
		"severity":       string(i.Severity),
		"source":         string(i.Source),
		"category":       string(i.Category),
		"category_group": string(i.CategoryGroup),
		"kind":           i.Kind,
		"group":          i.Group,
		// `ns` rather than `namespace` — `namespace` is a CEL reserved
		// identifier and bare references fail at parse time. See
		// internal/filter.envIssue for the rationale.
		"ns":      i.Namespace,
		"name":    i.Name,
		"reason":  i.Reason,
		"message": i.Message,
		"count":   int64(i.Count),
		// first_seen is the observed issue age anchor (the axis the queue sorts on);
		// last_seen churns to compose-time every poll, so `last_seen > X` ("older
		// than…") is near-useless. Both are int unix seconds.
		"first_seen":             firstSeen,
		"last_seen":              lastSeen,
		"grouping_scope":         string(i.GroupingScope),
		"restart_count":          int64(i.RestartCount),
		"last_terminated_reason": i.LastTerminatedReason,
		// Semantics documented on issuesapi.Issue.IssueTiming / IssueTimingBasis.
		"issue_timing":       i.IssueTiming,
		"issue_timing_basis": i.IssueTimingBasis,
	}
}
