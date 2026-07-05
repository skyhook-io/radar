package issues

import (
	"fmt"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// flatPod builds a flat pod issue the way Compose would — classified +
// identity-enriched — so grouping tests exercise the real id/owner/scope.
func flatPod(name, reason string, sev Severity, owner Ref, first, last time.Time) Issue {
	i := Issue{
		Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: name,
		Reason: reason, Severity: sev, Owner: owner,
		FirstSeen: first, LastSeen: last, Count: 1,
	}
	classifyIssue(&i)
	enrichIdentity(&i)
	return i
}

func TestGroupIssues_FoldsMembersUnderOwner(t *testing.T) {
	dep := Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web"}
	t0, t1, t2 := time.Unix(1000, 0), time.Unix(2000, 0), time.Unix(3000, 0)
	// web-b is the worst member: critical, oldest first_seen, newest last_seen,
	// and a distinct (same-category) reason — it must drive the rep fields.
	flat := []Issue{
		flatPod("web-a", "ImagePullBackOff", SeverityWarning, dep, t1, t1),
		flatPod("web-b", "ErrImagePull", SeverityCritical, dep, t0, t2),
		flatPod("web-c", "ImagePullBackOff", SeverityWarning, dep, t1, t1),
	}
	got := GroupIssues(flat)
	if len(got) != 1 {
		t.Fatalf("want 1 grouped row, got %d", len(got))
	}
	g := got[0]
	if g.Group != "apps" || g.Kind != "Deployment" || g.Name != "web" {
		t.Errorf("subject = %s/%s/%s, want apps/Deployment/web", g.Group, g.Kind, g.Name)
	}
	if g.Count != 3 || g.Affected.Pods != 3 || len(g.Members) != 3 {
		t.Errorf("count=%d affected.pods=%d members=%d, want 3/3/3", g.Count, g.Affected.Pods, len(g.Members))
	}
	if g.Severity != SeverityCritical {
		t.Errorf("severity = %q, want critical (max of members)", g.Severity)
	}
	if g.Reason != "ErrImagePull" {
		t.Errorf("reason = %q, want the worst member's ErrImagePull", g.Reason)
	}
	if !g.FirstSeen.Equal(t0) {
		t.Errorf("first_seen = %v, want oldest %v", g.FirstSeen, t0)
	}
	if !g.LastSeen.Equal(t2) {
		t.Errorf("last_seen = %v, want newest %v", g.LastSeen, t2)
	}
	if g.Members[0].Name != "web-a" || g.Members[2].Name != "web-c" {
		t.Errorf("members not sorted by name: %+v", g.Members)
	}
	if g.Owner.Kind != "" {
		t.Errorf("grouped row should not carry Owner (subject is top-level): %+v", g.Owner)
	}
}

func TestGroupIssues_RolloutSubjectIsWorkload(t *testing.T) {
	ro := Ref{Group: "argoproj.io", Kind: "Rollout", Namespace: "ns", Name: "web"}
	i := Issue{
		Source: SourceProblem, Group: "apps", Kind: "ReplicaSet", Namespace: "ns", Name: "web-abc123",
		Reason: "ReplicaFailure", Severity: SeverityCritical, Owner: ro,
		FirstSeen: time.Unix(1000, 0), LastSeen: time.Unix(1000, 0), Count: 1,
	}
	classifyIssue(&i)
	enrichIdentity(&i)

	got := GroupIssues([]Issue{i})
	if len(got) != 1 {
		t.Fatalf("want 1 grouped row, got %d", len(got))
	}
	g := got[0]
	if g.GroupingScope != issuesapi.ScopeWorkload {
		t.Fatalf("rollout scope = %q, want workload", g.GroupingScope)
	}
	if g.Group != "argoproj.io" || g.Kind != "Rollout" || g.Name != "web" {
		t.Fatalf("subject = %s/%s/%s, want argoproj.io/Rollout/web", g.Group, g.Kind, g.Name)
	}
	if g.Count != 1 || g.Affected.Workloads != 1 {
		t.Fatalf("count=%d affected.workloads=%d, want 1/1", g.Count, g.Affected.Workloads)
	}
}

// TestGroupIssues_CarriesAgreedDiagnosis pins that a single-member group (e.g.
// a GitOps Application, which is its own subject) carries the parsed
// cause/remediation onto the grouped row — without this foldGroup would emit
// the issue with an empty cause.
func TestGroupIssues_CarriesAgreedDiagnosis(t *testing.T) {
	app := Issue{
		Source: SourceProblem, Kind: "Application", Group: "argoproj.io", Namespace: "argocd", Name: "broken-sync",
		Reason: "OperationFailed", Severity: SeverityCritical,
		Cause: "The destination namespace does not exist.", RemediationKind: "create-namespace", RemediationTarget: "demo-broken-sync",
		Count: 1,
	}
	classifyIssue(&app)
	enrichIdentity(&app)
	got := GroupIssues([]Issue{app})
	if len(got) != 1 {
		t.Fatalf("want 1 grouped row, got %d", len(got))
	}
	g := got[0]
	if g.Cause != app.Cause || g.RemediationKind != "create-namespace" || g.RemediationTarget != "demo-broken-sync" {
		t.Errorf("grouped row dropped diagnosis: cause=%q kind=%q target=%q", g.Cause, g.RemediationKind, g.RemediationTarget)
	}
}

// TestGroupIssues_OmitsLoneDiagnosis pins that a workload rollup only carries
// diagnosis when the diagnosis applies to every folded member.
func TestGroupIssues_OmitsLoneDiagnosis(t *testing.T) {
	dep := Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web"}
	t0 := time.Unix(1000, 0)
	a := flatPod("web-a", "CrashLoopBackOff", SeverityCritical, dep, t0, t0)
	a.Cause = "Application exited with a config error."
	b := flatPod("web-b", "CrashLoopBackOff", SeverityCritical, dep, t0, t0) // no cause
	got := GroupIssues([]Issue{a, b})
	if len(got) != 1 {
		t.Fatalf("want 1 grouped row, got %d", len(got))
	}
	if got[0].Cause != "" {
		t.Errorf("lone member diagnosis must not carry to grouped row, got cause %q", got[0].Cause)
	}
}

// TestGroupIssues_OmitsConflictingDiagnosis pins the safety rule: when members
// of one group carry different parsed causes, the grouped row presents none
// rather than misattributing one member's fix to the whole group.
func TestGroupIssues_OmitsConflictingDiagnosis(t *testing.T) {
	dep := Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web"}
	t0 := time.Unix(1000, 0)
	// Same reason → same category → one group; differing parsed causes within it.
	a := flatPod("web-a", "CrashLoopBackOff", SeverityCritical, dep, t0, t0)
	a.Cause = "Container ran out of memory."
	b := flatPod("web-b", "CrashLoopBackOff", SeverityCritical, dep, t0, t0)
	b.Cause = "Application exited with a config error."
	got := GroupIssues([]Issue{a, b})
	if len(got) != 1 {
		t.Fatalf("want 1 grouped row, got %d", len(got))
	}
	if got[0].Cause != "" {
		t.Errorf("conflicting member causes must yield empty group cause, got %q", got[0].Cause)
	}
}

func TestGroupIssues_StandalonePodIsOwnSubject(t *testing.T) {
	flat := []Issue{flatPod("solo", "CrashLoopBackOff", SeverityCritical, Ref{}, time.Unix(1, 0), time.Unix(1, 0))}
	got := GroupIssues(flat)
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	g := got[0]
	if g.Kind != "Pod" || g.Name != "solo" {
		t.Errorf("subject = %s/%s, want Pod/solo", g.Kind, g.Name)
	}
	// No fan-out: the subject is the only resource, so the affected-resource
	// count (non-subject members) is 0.
	if g.Count != 0 || len(g.Members) != 0 || g.Affected.Pods != 0 {
		t.Errorf("single-resource issue: count=%d members=%d affected.pods=%d, want 0/0/0", g.Count, len(g.Members), g.Affected.Pods)
	}
}

func TestGroupIssues_DistinctCategoriesStaySeparate(t *testing.T) {
	dep := Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web"}
	flat := []Issue{
		flatPod("web-a", "ImagePullBackOff", SeverityCritical, dep, time.Unix(1, 0), time.Unix(1, 0)),
		flatPod("web-b", "CrashLoopBackOff", SeverityCritical, dep, time.Unix(1, 0), time.Unix(1, 0)),
	}
	got := GroupIssues(flat)
	if len(got) != 2 {
		t.Fatalf("same owner, different categories must stay separate: got %d rows", len(got))
	}
}

func TestGroupIssues_MemberTruncation(t *testing.T) {
	dep := Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web"}
	var flat []Issue
	for i := 0; i < 12; i++ {
		flat = append(flat, flatPod(fmt.Sprintf("web-%02d", i), "ImagePullBackOff", SeverityCritical, dep, time.Unix(1, 0), time.Unix(1, 0)))
	}
	got := GroupIssues(flat)
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	g := got[0]
	// Counts reflect all 12; the inline member slice is capped + flagged.
	if g.Count != 12 || g.Affected.Pods != 12 {
		t.Errorf("count=%d affected.pods=%d, want 12/12", g.Count, g.Affected.Pods)
	}
	if !g.MembersTruncated || len(g.Members) != maxInlineMembers {
		t.Errorf("members len=%d truncated=%v, want %d/true", len(g.Members), g.MembersTruncated, maxInlineMembers)
	}
}

func TestGroupIssues_Deterministic(t *testing.T) {
	dep := Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web"}
	sts := Ref{Group: "apps", Kind: "StatefulSet", Namespace: "ns", Name: "db"}
	mk := func() []Issue {
		return []Issue{
			flatPod("web-a", "ImagePullBackOff", SeverityWarning, dep, time.Unix(1, 0), time.Unix(1, 0)),
			flatPod("web-b", "ErrImagePull", SeverityCritical, dep, time.Unix(1, 0), time.Unix(2, 0)),
			flatPod("db-x", "CrashLoopBackOff", SeverityCritical, sts, time.Unix(1, 0), time.Unix(3, 0)),
		}
	}
	a := GroupIssues(mk())
	in := mk()
	in[0], in[2] = in[2], in[0] // reorder input
	b := GroupIssues(in)
	if len(a) != len(b) {
		t.Fatalf("len differs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Reason != b[i].Reason || a[i].Name != b[i].Name {
			t.Errorf("non-deterministic at %d: %+v vs %+v", i, a[i], b[i])
		}
	}
}

func TestComposeWithStats_GroupedCapsOnGroups(t *testing.T) {
	var probs []k8s.Detection
	for i := 0; i < 5; i++ {
		probs = append(probs, k8s.Detection{
			Kind: "Pod", Namespace: "ns", Name: fmt.Sprintf("web-%d", i),
			Severity: "critical", Reason: "ImagePullBackOff",
			OwnerKind: "Deployment", OwnerName: "web",
		})
	}
	p := &fakeProvider{problems: probs}

	flat, fstats := ComposeWithStats(p, Filters{})
	if len(flat) != 5 || fstats.TotalMatched != 5 {
		t.Fatalf("flat: want 5 rows / matched 5, got %d / %d", len(flat), fstats.TotalMatched)
	}

	g, gstats := ComposeWithStats(p, Filters{Grouped: true})
	if len(g) != 1 || gstats.TotalMatched != 1 {
		t.Fatalf("grouped: want 1 row / matched 1 (cap counts groups), got %d / %d", len(g), gstats.TotalMatched)
	}
	if g[0].Affected.Pods != 5 || g[0].Count != 5 {
		t.Errorf("grouped row should reflect 5 members: affected.pods=%d count=%d", g[0].Affected.Pods, g[0].Count)
	}
}

// TestRelatedIssues_SubjectAndMember pins what diagnose relies on: querying a
// resource returns the grouped issues where it's the SUBJECT or an affected
// MEMBER. A Pod-evidenced crashloop under a Deployment is returned for both the
// Deployment (subject) and the Pod (member); kind match is case-insensitive.
func TestRelatedIssues_SubjectAndMember(t *testing.T) {
	p := &fakeProvider{problems: []k8s.Detection{
		{Kind: "Pod", Namespace: "prod", Name: "web-abc-1", Reason: "CrashLoopBackOff", Severity: "critical",
			OwnerGroup: "apps", OwnerKind: "Deployment", OwnerName: "web"},
	}}
	if got := RelatedIssues(p, nil, "apps", "Deployment", "prod", "web"); len(got) != 1 {
		t.Fatalf("RelatedIssues(owning Deployment) = %d, want 1 (subject match)", len(got))
	}
	if got := RelatedIssues(p, nil, "", "pod", "prod", "web-abc-1"); len(got) != 1 {
		t.Errorf("RelatedIssues(evidence Pod, case-insensitive) = %d, want 1 (member match)", len(got))
	}
	if got := RelatedIssues(p, nil, "apps", "Deployment", "prod", "other"); len(got) != 0 {
		t.Errorf("RelatedIssues(unrelated) = %d, want 0", len(got))
	}
}

// TestRelatedIssues_PopulatesIncidentParent pins that the per-resource path runs
// the grouped-mode enrichment, so a symptom returned for the drawer / get_resource
// / diagnose carries the symptom→root incident_parent (not just the forward links).
func TestRelatedIssues_PopulatesIncidentParent(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "PersistentVolumeClaim", Namespace: "prod", Name: "data", Reason: "Pending", Severity: "critical"},
		},
		scheduling: []k8s.Detection{
			{Kind: "Pod", Namespace: "prod", Name: "db-0", Reason: "Unschedulable", Severity: "critical",
				Message: "pod has unbound immediate PersistentVolumeClaims"},
		},
		podsMountingPVC: map[string][]Ref{"prod/data": {{Kind: "Pod", Namespace: "prod", Name: "db-0"}}},
	}
	got := RelatedIssues(p, nil, "", "Pod", "prod", "db-0")
	if len(got) != 1 {
		t.Fatalf("RelatedIssues(db-0) = %d, want 1", len(got))
	}
	ip := got[0].IncidentParent
	if ip == nil || ip.Ref.Kind != "PersistentVolumeClaim" || ip.Ref.Name != "data" || ip.Confidence != issuesapi.ConfidenceHigh {
		t.Fatalf("expected incident_parent → PVC/data (high), got %+v", ip)
	}
}

// TestRelatedIssues_IncidentParentCoverageGate pins that the per-resource path
// honors the whole-row coverage gate: when a root explains only SOME members of
// a grouped symptom (2 of 3 unschedulable pods mount the Pending PVC), the row
// gets no incident_parent — attributing a mixed-cause row to one root would
// over-claim. Guards the grouped argument at the RelatedIssues enrichment call:
// passing flat rows there would satisfy the happy-path test but break this one.
func TestRelatedIssues_IncidentParentCoverageGate(t *testing.T) {
	sched := make([]k8s.Detection, 0, 3)
	for _, pod := range []string{"db-0", "db-1", "db-2"} {
		sched = append(sched, k8s.Detection{
			Kind: "Pod", Namespace: "prod", Name: pod, Reason: "Unschedulable", Severity: "critical",
			Message:    "pod has unbound immediate PersistentVolumeClaims",
			OwnerGroup: "apps", OwnerKind: "StatefulSet", OwnerName: "db",
		})
	}
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "PersistentVolumeClaim", Namespace: "prod", Name: "data", Reason: "Pending", Severity: "critical"},
		},
		scheduling: sched,
		podsMountingPVC: map[string][]Ref{"prod/data": {
			{Kind: "Pod", Namespace: "prod", Name: "db-0"},
			{Kind: "Pod", Namespace: "prod", Name: "db-1"},
		}},
	}
	got := RelatedIssues(p, nil, "", "Pod", "prod", "db-0")
	if len(got) != 1 {
		t.Fatalf("RelatedIssues(db-0) = %d, want 1", len(got))
	}
	if got[0].IncidentParent != nil {
		t.Fatalf("partially-covered row (2 of 3 members mount the PVC) must not carry incident_parent, got %+v", got[0].IncidentParent)
	}
}

// TestRelatedIssuesFrom_MatchesPrecomposed pins that the compose-once helper
// returns the same MATCH SET as RelatedIssues when handed an already-composed
// (flat, grouped) pair — the path the GitOps insights resolver uses to avoid a
// full Compose per managed resource. Row payload is a different contract: it
// reflects whatever pair the caller passes (see
// TestRelatedIssuesFrom_NoIncidentParentOnBarePair).
func TestRelatedIssuesFrom_MatchesPrecomposed(t *testing.T) {
	p := &fakeProvider{problems: []k8s.Detection{
		{Kind: "Pod", Namespace: "prod", Name: "web-abc-1", Reason: "CrashLoopBackOff", Severity: "critical",
			OwnerGroup: "apps", OwnerKind: "Deployment", OwnerName: "web"},
	}}
	flat := Compose(p, Filters{Limit: NoLimit})
	grouped := GroupIssues(flat)
	if got := RelatedIssuesFrom(flat, grouped, "apps", "Deployment", "prod", "web"); len(got) != 1 {
		t.Errorf("RelatedIssuesFrom(owning Deployment) = %d, want 1", len(got))
	}
	if got := RelatedIssuesFrom(flat, grouped, "apps", "Deployment", "prod", "other"); len(got) != 0 {
		t.Errorf("RelatedIssuesFrom(unrelated) = %d, want 0", len(got))
	}
}

// TestRelatedIssuesFrom_NoIncidentParentOnBarePair pins the contract split
// between the two entry points: RelatedIssues enriches its grouped set, so the
// same scenario carries incident_parent there (TestRelatedIssues_PopulatesIncidentParent),
// while a caller handing RelatedIssuesFrom a bare GroupIssues pair gets rows
// without the pointer — grouped-mode enrichment is the caller's responsibility.
func TestRelatedIssuesFrom_NoIncidentParentOnBarePair(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "PersistentVolumeClaim", Namespace: "prod", Name: "data", Reason: "Pending", Severity: "critical"},
		},
		scheduling: []k8s.Detection{
			{Kind: "Pod", Namespace: "prod", Name: "db-0", Reason: "Unschedulable", Severity: "critical",
				Message: "pod has unbound immediate PersistentVolumeClaims"},
		},
		podsMountingPVC: map[string][]Ref{"prod/data": {{Kind: "Pod", Namespace: "prod", Name: "db-0"}}},
	}
	flat := Compose(p, Filters{Limit: NoLimit})
	grouped := GroupIssues(flat)
	got := RelatedIssuesFrom(flat, grouped, "", "Pod", "prod", "db-0")
	if len(got) != 1 {
		t.Fatalf("RelatedIssuesFrom(db-0) = %d, want 1", len(got))
	}
	if got[0].IncidentParent != nil {
		t.Fatalf("bare GroupIssues pair must not carry incident_parent, got %+v", got[0].IncidentParent)
	}
}

// TestRelatedIssuesFrom_NormalizesGroup pins that a caller passing a raw,
// unnormalized group (as the GitOps L4 bridge forwards from Argo's
// status.resources — "" for built-ins) still matches the resolved subject
// ("apps" for a Deployment). Without group normalization the bridge silently
// returned nothing for typical workloads.
func TestRelatedIssuesFrom_NormalizesGroup(t *testing.T) {
	p := &fakeProvider{problems: []k8s.Detection{
		{Kind: "Pod", Namespace: "prod", Name: "web-abc-1", Reason: "CrashLoopBackOff", Severity: "critical",
			OwnerGroup: "apps", OwnerKind: "Deployment", OwnerName: "web"},
	}}
	flat := Compose(p, Filters{Limit: NoLimit})
	grouped := GroupIssues(flat)
	if got := RelatedIssuesFrom(flat, grouped, "", "Deployment", "prod", "web"); len(got) != 1 {
		t.Errorf("raw empty query group should normalize to apps and match, got %d", len(got))
	}
}

func TestRelatedIssues_GroupIsExact(t *testing.T) {
	p := &fakeProvider{problems: []k8s.Detection{
		{Kind: "Service", Namespace: "prod", Name: "api", Reason: "0/1 selected pods ready", Severity: "critical"},
		{Kind: "Service", Group: "serving.knative.dev", Namespace: "prod", Name: "api", Reason: "0/1 selected pods ready", Severity: "critical"},
	}}

	core := RelatedIssues(p, nil, "", "Service", "prod", "api")
	if len(core) != 1 || core[0].Group != "" {
		t.Fatalf("core Service lookup should match only core-group issue, got %+v", core)
	}
	knative := RelatedIssues(p, nil, "serving.knative.dev", "Service", "prod", "api")
	if len(knative) != 1 || knative[0].Group != "serving.knative.dev" {
		t.Fatalf("Knative Service lookup should match only serving.knative.dev issue, got %+v", knative)
	}
}

// TestRelatedIssues_UncappedMembers pins that a resource resolves its issue even
// when it's member #11+ of a large grouped fan-out — RelatedIssues matches flat
// evidence (uncapped), not the inline Members slice (capped at maxInlineMembers).
func TestRelatedIssues_UncappedMembers(t *testing.T) {
	var probs []k8s.Detection
	for i := 0; i < 12; i++ { // > maxInlineMembers (10)
		probs = append(probs, k8s.Detection{
			Kind: "Pod", Namespace: "prod", Name: fmt.Sprintf("web-abc-%d", i),
			Reason: "CrashLoopBackOff", Severity: "critical",
			OwnerGroup: "apps", OwnerKind: "Deployment", OwnerName: "web",
		})
	}
	p := &fakeProvider{problems: probs}
	if got := RelatedIssues(p, nil, "", "Pod", "prod", "web-abc-11"); len(got) != 1 {
		t.Errorf("pod beyond the inline-Members cap = %d related issues, want 1 (uncapped lookup)", len(got))
	}
	if got := RelatedIssues(p, nil, "apps", "Deployment", "prod", "web"); len(got) != 1 {
		t.Errorf("owning Deployment = %d related issues, want 1", len(got))
	}
}

func TestGroupIssuesPreservesDiagnosticContext(t *testing.T) {
	flat := []Issue{{
		ID: "pvc-x", Kind: "PersistentVolumeClaim", Namespace: "ns", Name: "data",
		Category: issuesapi.CategoryPVCPending, Severity: SeverityCritical,
		DiagnosticContext: &issuesapi.DiagnosticContext{
			Role:  issuesapi.DiagnosticRoleCandidate,
			Facts: []issuesapi.DiagnosticFact{{Type: "pvc_blast_radius", Confidence: issuesapi.ConfidenceHigh}},
		},
	}}
	grouped := GroupIssues(flat)
	if len(grouped) != 1 || grouped[0].DiagnosticContext == nil || len(grouped[0].DiagnosticContext.Facts) != 1 {
		t.Fatalf("GroupIssues must carry the representative's diagnostic_context through the regroup (the per-resource RelatedIssues path depends on it), got %+v", grouped[0].DiagnosticContext)
	}
	if grouped[0].DiagnosticContext.Facts[0].Confidence != issuesapi.ConfidenceHigh {
		t.Fatalf("confidence lost in regroup")
	}
}
