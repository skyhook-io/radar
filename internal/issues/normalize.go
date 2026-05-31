package issues

import (
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// resolveGroup returns the explicit group if set, else falls back to the
// built-in (Kind→Group) table. Some legacy Problem emission sites in
// k8s.DetectProblems still leave Group="" for built-in workloads
// (Deployment, StatefulSet, etc.) — without this fallback, the
// group-aware consumer (computeIssueSummaryForResource) would silently
// drop those rows when looking up by canonical group like "apps".
// Centralised here so the (Kind→Group) map lives in one place across
// packages (pkg/resourceid owns the table; this is a pass-through).
func resolveGroup(group, kind string) string {
	if group != "" {
		return group
	}
	return resourceid.GroupForBuiltinKind(kind)
}

func fromProblem(p k8s.Detection, now time.Time, source Source) Issue {
	sev := SeverityWarning
	if p.Severity == "critical" {
		sev = SeverityCritical
	}
	since := now.Add(-time.Duration(p.DurationSeconds) * time.Second)
	if p.DurationSeconds == 0 && p.AgeSeconds > 0 {
		// Detectors that don't track how long the problem has persisted leave
		// DurationSeconds zero; without this, FirstSeen would reset to `now` on
		// every compose and the queue (sorted by first_seen) would keep a chronic
		// issue looking fresh. AgeSeconds (resource age) is a stable lower bound.
		since = now.Add(-time.Duration(p.AgeSeconds) * time.Second)
	}
	iss := Issue{
		Severity:             sev,
		Source:               source,
		Kind:                 p.Kind,
		Group:                resolveGroup(p.Group, p.Kind),
		Namespace:            p.Namespace,
		Name:                 p.Name,
		Reason:               p.Reason,
		Message:              p.Message,
		Fingerprint:          p.Fingerprint,
		FirstSeen:            since,
		LastSeen:             now,
		Count:                1,
		RestartCount:         p.RestartCount,
		LastTerminatedReason: p.LastTerminatedReason,
	}
	if p.OwnerKind != "" {
		// Prefer the owner group resolved at detection (carries the real group
		// for CRD controllers like Argo Rollout); fall back to the builtin
		// Kind→Group table for legacy emitters that leave it empty.
		ownerGroup := p.OwnerGroup
		if ownerGroup == "" {
			ownerGroup = resolveGroup("", p.OwnerKind)
		}
		iss.Owner = Ref{
			Group:     ownerGroup,
			Kind:      p.OwnerKind,
			Namespace: p.Namespace,
			Name:      p.OwnerName,
		}
	}
	classifyIssue(&iss)
	enrichIdentity(&iss)
	return iss
}

// classifyIssue derives the user-facing Category + its CategoryGroup rollup
// from the row's detection signal. Pure: same inputs always yield the same
// labels, so the category stays stable across recomposes (a prerequisite for
// the future category-in-issue-id contract).
func classifyIssue(i *Issue) {
	i.Category = Classify(classifyInput{
		Source:               i.Source,
		APIGroup:             i.Group,
		Kind:                 i.Kind,
		Reason:               i.Reason,
		LastTerminatedReason: i.LastTerminatedReason,
	})
	i.CategoryGroup = issuesapi.GroupOf(i.Category)
}

// ---------------------------------------------------------------------------
// Filter + sort helpers
// ---------------------------------------------------------------------------
