package issues

import (
	"fmt"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

const duplicateEnvAggregateFingerprint = "duplicate-env-workload-aggregate"
const maxDuplicateEnvAggregateDetails = 5

func aggregateDuplicateEnvIssues(in []Issue) []Issue {
	buckets := make(map[string][]Issue)
	for _, issue := range in {
		key, ok := duplicateEnvAggregationKey(issue)
		if !ok {
			continue
		}
		buckets[key] = append(buckets[key], issue)
	}

	if len(buckets) == 0 {
		return in
	}

	out := make([]Issue, 0, len(in))
	emitted := make(map[string]bool, len(buckets))
	for _, issue := range in {
		key, ok := duplicateEnvAggregationKey(issue)
		if !ok {
			out = append(out, issue)
			continue
		}
		members := buckets[key]
		if len(members) < 2 {
			out = append(out, issue)
			continue
		}
		if emitted[key] {
			continue
		}
		emitted[key] = true
		out = append(out, newDuplicateEnvAggregate(members))
	}

	sort.SliceStable(out, func(i, j int) bool { return lessIssue(out[i], out[j]) })
	return out
}

func duplicateEnvAggregationKey(issue Issue) (string, bool) {
	if issue.Reason != "DuplicateEnvVar" || issue.Category != issuesapi.CategoryInvalidConfiguration {
		return "", false
	}
	group := resolveGroup(issue.Group, issue.Kind)
	return resourceKey(group, issue.Kind, issue.Namespace, issue.Name), true
}

func newDuplicateEnvAggregate(members []Issue) Issue {
	first := members[0]
	severity := first.Severity
	firstSeen := first.FirstSeen
	lastSeen := first.LastSeen
	for _, member := range members[1:] {
		if SeverityRank(member.Severity) > SeverityRank(severity) {
			severity = member.Severity
		}
		if !member.FirstSeen.IsZero() && (firstSeen.IsZero() || member.FirstSeen.Before(firstSeen)) {
			firstSeen = member.FirstSeen
		}
		if member.LastSeen.After(lastSeen) {
			lastSeen = member.LastSeen
		}
	}

	issue := Issue{
		Severity:    severity,
		Source:      SourceProblem,
		Kind:        first.Kind,
		Group:       resolveGroup(first.Group, first.Kind),
		Namespace:   first.Namespace,
		Name:        first.Name,
		Reason:      "DuplicateEnvVar",
		Message:     duplicateEnvAggregateMessage(members),
		Action:      "Remove duplicate entries from the workload manifest or chart so each environment variable is declared once per container, then redeploy through the normal delivery path.",
		Fingerprint: duplicateEnvAggregateFingerprint,
		FirstSeen:   firstSeen,
		LastSeen:    lastSeen,
	}
	classifyIssue(&issue)
	enrichIdentity(&issue)
	return issue
}

func duplicateEnvAggregateMessage(members []Issue) string {
	detailSet := make(map[string]bool, len(members))
	for _, member := range members {
		fingerprint, ok := k8s.ParseDuplicateEnvVarFingerprint(member.Fingerprint)
		if !ok {
			continue
		}
		detailSet[fmt.Sprintf("%s (%s)", fingerprint.EnvName, fingerprint.Container)] = true
	}
	details := make([]string, 0, len(detailSet))
	for detail := range detailSet {
		details = append(details, detail)
	}
	sort.Strings(details)
	if len(details) > maxDuplicateEnvAggregateDetails {
		omitted := len(details) - maxDuplicateEnvAggregateDetails
		details = append(details[:maxDuplicateEnvAggregateDetails], fmt.Sprintf("+%d more", omitted))
	}

	summary := "Duplicate environment variables are declared more than once"
	if len(details) > 0 {
		summary = "Duplicate environment variables: " + strings.Join(details, ", ")
	}
	return summary + ". This is a configuration risk: the workload may be running normally, but later declarations shadow earlier ones and apply/patch can drop shadowed entries."
}
