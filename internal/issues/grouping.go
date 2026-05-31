package issues

import (
	"sort"
	"strings"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// RelatedIssues returns the grouped issues whose subject OR an affected member
// is the given resource — what an agent diagnosing an object wants: "issues
// Radar already classified here." Kind is matched case-insensitively (callers
// may pass the K8s Kind or a normalized form); group is exact, including the
// empty core API group, so core/CRD kind collisions cannot bleed into each
// other's resourceContext.
func RelatedIssues(p Provider, namespaces []string, group, kind, namespace, name string) []Issue {
	// Compose FLAT (uncapped) then group: matching against the flat evidence —
	// not the grouped issue's inline Members (capped at maxInlineMembers) — is
	// what makes member #11..#N in a large fan-out resolve correctly.
	flat := Compose(p, Filters{Namespaces: namespaces, Limit: NoLimit})
	grouped := GroupIssues(flat)
	match := func(g, k, ns, n string) bool {
		return strings.EqualFold(k, kind) && ns == namespace && n == name && g == group
	}
	matched := make(map[string]bool) // grouped issue IDs the resource touches
	for _, g := range grouped {      // as the grouped SUBJECT (owner-collapsed)
		if match(g.Group, g.Kind, g.Namespace, g.Name) {
			matched[g.ID] = true
		}
	}
	for _, f := range flat { // as ANY evidence row (uncapped)
		if match(f.Group, f.Kind, f.Namespace, f.Name) {
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
// reason/message/crash context; age is the oldest onset; last_seen the
// newest. members are the folded underlying resources (the fan-out),
// excluding the subject itself.
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
		RestartCount:         rep.RestartCount,
		LastTerminatedReason: rep.LastTerminatedReason,
		FirstSeen:            rep.FirstSeen,
		LastSeen:             rep.LastSeen,
	}

	var refs []Ref
	for _, m := range members {
		if !m.FirstSeen.IsZero() && (g.FirstSeen.IsZero() || m.FirstSeen.Before(g.FirstSeen)) {
			g.FirstSeen = m.FirstSeen
		}
		if m.LastSeen.After(g.LastSeen) {
			g.LastSeen = m.LastSeen
		}
		own := Ref{Group: m.Group, Kind: m.Kind, Namespace: m.Namespace, Name: m.Name}
		if own != subject {
			refs = append(refs, own)
		}
	}
	sortRefs(refs)
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
		case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob":
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

// lessIssue is the canonical issue sort: severity desc, then ONSET (first_seen
// desc) — deliberately NOT last_seen, which bumps to compose-time on every poll
// and would reshuffle same-severity rows on each refetch. Then namespace, name,
// and the stable id as a total tiebreak. This is byte-for-byte the order the
// shared UI comparator (k8s-ui issues/types.ts:compareIssues) produces for a
// single cluster — the UI's only extra key is `cluster`, which it sorts on for
// fleet (multi-cluster) views and which is constant here. So /api/issues, MCP,
// and the single-cluster UI return one identical queue. (id is the final
// tiebreak — two rows can share subject+ns+name and differ only by cause.)
func lessIssue(a, b Issue) bool {
	if a.Severity != b.Severity {
		return SeverityRank(a.Severity) > SeverityRank(b.Severity)
	}
	if !a.FirstSeen.Equal(b.FirstSeen) {
		return a.FirstSeen.After(b.FirstSeen)
	}
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID < b.ID
}
