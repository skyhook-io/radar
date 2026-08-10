package issues

import (
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// RelatedIssues returns the grouped issues whose subject OR an affected member
// is the given resource — what an agent diagnosing an object wants: "issues
// Radar already classified here." Kind is matched case-insensitively (callers
// may pass the K8s Kind or a normalized form); group is exact, including the
// empty core API group, so core/CRD kind collisions cannot bleed into each
// other's resourceContext.
func RelatedIssues(p Provider, opts RelatedIssueOptions, group, kind, namespace, name string) []Issue {
	// Compose FLAT (uncapped) then group: matching against the flat evidence —
	// not the grouped issue's inline Members (capped at maxInlineMembers) — is
	// what makes member #11..#N in a large fan-out resolve correctly.
	flat := Compose(p, Filters{
		Namespaces:           opts.Namespaces,
		Limit:                NoLimit,
		CanReadClusterScoped: opts.CanReadClusterScoped,
		CanReadRelated:       opts.CanReadRelated,
	})
	grouped := GroupIssues(flat)
	// Run the grouped-mode enrichment (mirrors the cluster path) so the grouped
	// issues get coverage-gated incident_parent pointers — GroupIssues alone only
	// carries the representative's DiagnosticContext, never the reverse pointer.
	grouped = enrichDiagnosticContextAuthorized(grouped, flat, grouped, p, opts.CanReadClusterScoped)
	return RelatedIssuesFrom(flat, grouped, opts, group, kind, namespace, name)
}

// RelatedIssuesFrom is the matching half of RelatedIssues over an
// already-composed (flat, grouped) pair. Split out so a caller that needs the
// related issues for MANY resources in one request (e.g. the GitOps insights
// resolver enriching every degraded managed resource) can Compose once and
// match repeatedly, instead of running a full cluster Compose per resource.
// It only matches — grouped-mode enrichment is the caller's responsibility, so
// a bare GroupIssues pair yields rows without incident_parent.
func RelatedIssuesFrom(flat, grouped []Issue, opts RelatedIssueOptions, group, kind, namespace, name string) []Issue {
	// Normalize the query group so a caller passing a raw API group — e.g. the
	// GitOps L4 bridge forwarding Argo's status.resources[].group, which is ""
	// for core kinds and "apps" for Deployments — matches composed issues, whose
	// group was already run through resolveGroup. Comparing raw "" against a
	// resolved "apps" would silently return nothing for typical workloads.
	wantGroup := resolveGroup(group, kind)
	match := func(g, k, ns, n string) bool {
		return strings.EqualFold(k, kind) && ns == namespace && n == name && resolveGroup(g, k) == wantGroup
	}
	matched := make(map[string]bool) // grouped issue IDs the resource touches
	for _, g := range grouped {      // as the grouped SUBJECT (owner-collapsed)
		if match(g.Group, g.Kind, g.Namespace, g.Name) && CanReadIssueRelatedRefs(g, opts.CanReadRelated) {
			matched[g.ID] = true
		}
		if g.DiagnosticContext != nil {
			for _, fact := range g.DiagnosticContext.Facts {
				if fact.Type != karpenterFactReferencedByNodePools {
					continue
				}
				for _, ref := range fact.Refs {
					if match(ref.Group, ref.Kind, ref.Namespace, ref.Name) && opts.CanReadRelated != nil && opts.CanReadRelated(Ref{
						Group: g.Group, Kind: g.Kind, Namespace: g.Namespace, Name: g.Name,
					}) {
						matched[g.ID] = true
						break
					}
				}
			}
		}
	}
	for _, f := range flat { // as ANY evidence row (uncapped)
		if match(f.Group, f.Kind, f.Namespace, f.Name) && CanReadIssueRelatedRefs(f, opts.CanReadRelated) {
			matched[f.ID] = true
		}
	}
	var out []Issue
	for _, g := range grouped {
		if matched[g.ID] {
			out = append(out, g)
		}
	}
	return out
}

// maxInlineMembers bounds the member refs carried inline on a grouped issue.
// Enough for a human or agent to see what folded without a second call;
// full member state stays lazy (evidence). Past this, membersTruncated is
// set and the slice is capped.
const maxInlineMembers = 10

// Affected counts the underlying resources folded into a grouped issue, by
// kind bucket. Empty for single-resource issues (no fan-out) — there the
// subject row already says everything.
type Affected = issuesapi.Affected

// GroupIssues folds flat issue rows into the public grouped model: one row
// per shared ID (subject + category). The flat rows are the evidence; a
// grouped row is the operational issue an operator or agent triages.
//
// Deterministic by construction — the representative member and member
// ordering are chosen by total comparators, so the same input always
// yields the same output regardless of input order. Input is not mutated.
func GroupIssues(flat []Issue) []Issue {
	buckets := make(map[string][]Issue)
	for _, r := range flat {
		buckets[r.ID] = append(buckets[r.ID], r)
	}
	out := make([]Issue, 0, len(buckets))
	for _, members := range buckets {
		out = append(out, foldGroup(members))
	}
	sort.SliceStable(out, func(i, j int) bool { return lessIssue(out[i], out[j]) })
	return out
}

// foldGroup collapses one group's member rows into a single grouped issue,
// applying the representative rules: the worst member drives severity +
// reason/message/crash context; first_seen is the earliest member active-time
// anchor, resource_created_at is the oldest member creation time, and last_seen
// is the newest observation. members are the folded underlying resources (the
// fan-out), excluding the subject itself.
func foldGroup(members []Issue) Issue {
	rep := members[0]
	for _, m := range members[1:] {
		if betterRepresentative(m, rep) {
			rep = m
		}
	}

	subject := Ref{Group: rep.Group, Kind: rep.Kind, Namespace: rep.Namespace, Name: rep.Name}
	if rep.Owner.Kind != "" {
		subject = rep.Owner
	}

	g := Issue{
		Severity:             rep.Severity,
		Source:               rep.Source,
		Category:             rep.Category,
		CategoryGroup:        rep.CategoryGroup,
		ID:                   rep.ID,
		GroupingScope:        rep.GroupingScope,
		Kind:                 subject.Kind,
		Group:                subject.Group,
		Namespace:            subject.Namespace,
		Name:                 subject.Name,
		Reason:               rep.Reason,
		Message:              rep.Message,
		RawMessage:           rep.RawMessage,
		Fingerprint:          rep.Fingerprint,
		RestartCount:         rep.RestartCount,
		LastTerminatedReason: rep.LastTerminatedReason,
		FirstSeen:            rep.FirstSeen,
		OnsetUnknown:         rep.OnsetUnknown,
		ResourceCreatedAt:    rep.ResourceCreatedAt,
		LastSeen:             rep.LastSeen,
		// Per-issue context carries from the representative, like Reason/Message.
		// In the cluster-wide path grouping runs before enrichment, so these are
		// nil here and enrichment attaches to the grouped row afterward. On the
		// per-resource RelatedIssues path Compose enriches the flat rows first,
		// so without this the regroup would drop the representative's causal
		// links / change context.
		DiagnosticContext: rep.DiagnosticContext,
		ChangeContext:     rep.ChangeContext,
	}
	// A parsed diagnosis (cause/action/remediation) describes ONE resource's
	// failure. Carry it onto the grouped row only when it is true for the
	// entire group: a single-member group carries its own diagnosis, but a
	// workload rollup omits diagnosis unless every folded member has the same
	// tuple.
	if dg, ok := agreedDiagnosis(members); ok {
		g.Cause = dg.Cause
		g.Action = dg.Action
		g.RemediationKind = dg.RemediationKind
		g.RemediationTarget = dg.RemediationTarget
		g.OperationRetryCount = dg.OperationRetryCount
		g.Stuck = dg.Stuck
	}

	var refs []Ref
	for _, m := range members {
		if m.LastSeen.After(g.LastSeen) {
			g.LastSeen = m.LastSeen
		}
		own := Ref{Group: m.Group, Kind: m.Kind, Namespace: m.Namespace, Name: m.Name}
		if own != subject {
			refs = append(refs, own)
		}
	}
	g.FirstSeen, g.OnsetUnknown, g.OnsetCoverage, g.ResourceCreatedAt = foldIssueOnset(members)
	sortRefs(refs)

	// IssueTiming: keep only if all members with issue_timing agree; any mix of
	// "started_at_resource_creation" and "started_after_resource_was_healthy" → omit. Members
	// without issue_timing don't contribute (they're simply unknown). IssueTiming is
	// computed by this agreement scan, never copied from the representative's
	// own field — the representative drives Reason/Message, but its issue_timing
	// alone could disagree with other members.
	// Basis follows the same rule: agreeing issue_timings with mixed bases (e.g.
	// "condition" + "owner_condition") keep the issue_timing but drop the basis
	// rather than crediting one member's evidence for the whole group.
	var groupIssueTiming, groupBasis string
	for _, m := range members {
		if m.IssueTiming == "" {
			continue
		}
		if groupIssueTiming == "" {
			groupIssueTiming, groupBasis = m.IssueTiming, m.IssueTimingBasis
		} else if groupIssueTiming != m.IssueTiming {
			groupIssueTiming, groupBasis = "", "" // disagreement → omit
			break
		} else if groupBasis != m.IssueTimingBasis {
			groupBasis = ""
		}
	}
	g.IssueTiming = groupIssueTiming
	g.IssueTimingBasis = groupBasis
	if g.OnsetCoverage != nil && g.OnsetCoverage.Known > 0 && g.OnsetCoverage.Unknown > 0 {
		g.IssueTiming = ""
		g.IssueTimingBasis = ""
	}

	// CapacityRelevant: a group is capacity-relevant if ANY folded member is
	// (an unschedulable pod pinned to a Karpenter NodePool) — so a workload
	// rollup of NodePool-pinned pods keeps the "View in Capacity" link even
	// when the representative member happens not to carry the flag.
	for _, m := range members {
		if m.CapacityRelevant {
			g.CapacityRelevant = true
			break
		}
	}

	// IncidentParent is deliberately NOT carried through foldGroup: members of one
	// grouped symptom share an issue ID, so carrying a member's pointer can't honor
	// the whole-row coverage check. It is assigned AFTER grouping instead, in
	// enrichDiagnosticContext's grouped mode — the cluster path inline, the
	// per-resource RelatedIssues path via a second enrichment over its grouped set.
	// Precomposed RelatedIssuesFrom callers that skip that enrichment (the GitOps
	// insights resolver) get rows without the pointer.

	// Count is the affected-resource fan-out — the non-subject members under
	// this subject (the subject is shown separately as the header, not under
	// "Affected resources"). Matches the UI/TS contract; captured before the
	// inline-member truncation below so "Showing X of N" stays honest.
	g.Count = len(refs)
	g.Affected = affectedOf(refs)
	if len(refs) > maxInlineMembers {
		g.MembersTruncated = true
		refs = refs[:maxInlineMembers]
	}
	g.Members = refs
	return g
}

func foldIssueOnset(members []Issue) (time.Time, bool, *issuesapi.OnsetCoverage, time.Time) {
	var firstSeen, resourceCreatedAt time.Time
	known, unknown := 0, 0
	for _, member := range members {
		if member.OnsetCoverage != nil {
			known += member.OnsetCoverage.Known
			unknown += member.OnsetCoverage.Unknown
		} else if member.FirstSeen.IsZero() {
			unknown++
		} else {
			known++
		}
		if !member.FirstSeen.IsZero() && (firstSeen.IsZero() || member.FirstSeen.Before(firstSeen)) {
			firstSeen = member.FirstSeen
		}
		if !member.ResourceCreatedAt.IsZero() && (resourceCreatedAt.IsZero() || member.ResourceCreatedAt.Before(resourceCreatedAt)) {
			resourceCreatedAt = member.ResourceCreatedAt
		}
	}
	var coverage *issuesapi.OnsetCoverage
	if unknown > 0 && known+unknown > 1 {
		coverage = &issuesapi.OnsetCoverage{Known: known, Unknown: unknown}
	}
	return firstSeen, known == 0, coverage, resourceCreatedAt
}

// agreedDiagnosis returns the parsed diagnosis shared by a group's members, or
// ok=false when members carry conflicting or incomplete diagnoses. The full
// (cause, action, remediation) tuple must match across every member in a
// multi-resource group, so a mixed rollup omits diagnosis rather than
// misattributing one member's fix to the group.
func agreedDiagnosis(members []Issue) (Issue, bool) {
	var picked Issue
	if len(members) == 0 {
		return Issue{}, false
	}
	for _, m := range members {
		if !hasDiagnosis(m) {
			return Issue{}, false
		}
		if !hasDiagnosis(picked) {
			picked = m
			continue
		}
		if m.Cause != picked.Cause || m.Action != picked.Action ||
			m.RemediationKind != picked.RemediationKind || m.RemediationTarget != picked.RemediationTarget ||
			m.OperationRetryCount != picked.OperationRetryCount || m.Stuck != picked.Stuck {
			return Issue{}, false
		}
	}
	return picked, true
}

func hasDiagnosis(i Issue) bool {
	return i.Cause != "" || i.Action != "" || i.RemediationKind != "" || i.RemediationTarget != "" ||
		i.OperationRetryCount != 0 || i.Stuck
}

// betterRepresentative reports whether cand should replace cur as a group's
// representative: worst severity wins, then newest last_seen, then a fully
// deterministic total order over the identity-bearing fields. The representative
// donates Source/Reason/Message/crash-context to the grouped row, so the
// tiebreak must be total — same name with a different kind/group/source must
// resolve the same way regardless of input order.
func betterRepresentative(cand, cur Issue) bool {
	if c, r := SeverityRank(cand.Severity), SeverityRank(cur.Severity); c != r {
		return c > r
	}
	if !cand.LastSeen.Equal(cur.LastSeen) {
		return cand.LastSeen.After(cur.LastSeen)
	}
	ck := []string{cand.Group, cand.Kind, cand.Namespace, cand.Name, string(cand.Source), cand.Reason, cand.Message}
	rk := []string{cur.Group, cur.Kind, cur.Namespace, cur.Name, string(cur.Source), cur.Reason, cur.Message}
	for i := range ck {
		if ck[i] != rk[i] {
			return ck[i] < rk[i]
		}
	}
	return false
}

func affectedOf(refs []Ref) Affected {
	var a Affected
	for _, r := range refs {
		switch r.Kind {
		case "Pod":
			a.Pods++
		case "Deployment", "Rollout", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob":
			a.Workloads++
		case "Service":
			a.Services++
		case "PersistentVolumeClaim":
			a.PVCs++
		case "Node":
			a.Nodes++
		}
	}
	return a
}

func sortRefs(refs []Ref) {
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].Namespace != refs[j].Namespace {
			return refs[i].Namespace < refs[j].Namespace
		}
		if refs[i].Name != refs[j].Name {
			return refs[i].Name < refs[j].Name
		}
		if refs[i].Kind != refs[j].Kind {
			return refs[i].Kind < refs[j].Kind
		}
		return refs[i].Group < refs[j].Group
	})
}

// lessIssue is the canonical issue sort: severity desc, direct-blocker source
// priority, then a stable anchor (proven first_seen, otherwise resource creation)
// descending — deliberately NOT last_seen, which bumps to compose-time on every
// poll and would reshuffle same-severity rows on each refetch. Then namespace,
// name, and the stable id as a total tiebreak.
// This is byte-for-byte the order the shared UI comparator (k8s-ui
// issues/types.ts:compareIssues) produces for a single cluster — the UI's only
// extra key is `cluster`, which it sorts on for fleet (multi-cluster) views and
// which is constant here. So /api/issues, MCP, and the single-cluster UI return
// one identical queue. (id is the final tiebreak — two rows can share
// subject+ns+name and differ only by cause.)
func issueSortAnchor(i Issue) time.Time {
	if !i.FirstSeen.IsZero() {
		return i.FirstSeen
	}
	return i.ResourceCreatedAt
}

func lessIssue(a, b Issue) bool {
	if a.Severity != b.Severity {
		return SeverityRank(a.Severity) > SeverityRank(b.Severity)
	}
	if issueSourceRank(a.Source) != issueSourceRank(b.Source) {
		return issueSourceRank(a.Source) > issueSourceRank(b.Source)
	}
	aAnchor, bAnchor := issueSortAnchor(a), issueSortAnchor(b)
	if !aAnchor.Equal(bAnchor) {
		return aAnchor.After(bAnchor)
	}
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID < b.ID
}

func issueSourceRank(s Source) int {
	switch s {
	case SourceScheduling:
		return 4 // active bind/admission/post-bind blockers explain why work cannot run
	case SourceMissingRef:
		return 3 // direct references to absent objects are usually root-cause shaped
	case SourceCondition:
		return 2
	case SourceProblem:
		return 1
	}
	return 0
}
