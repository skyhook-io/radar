package issues

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestMergeExternalIssuesFiltersSortsAndCaps(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	base := []Issue{
		testIssueForMerge("Deployment", "apps", "web", SeverityCritical, now.Add(-30*time.Minute)),
	}
	extras := []Issue{
		testIssueForMerge("HelmRelease", "apps", "cart", SeverityCritical, now.Add(-5*time.Minute)),
		testIssueForMerge("Pod", "apps", "ignored", SeverityCritical, now.Add(-1*time.Minute)),
	}

	got, stats := MergeExternalIssues(base, ComposeStats{TotalMatched: len(base)}, Filters{
		Kinds: []string{"Deployment", "HelmRelease"},
		Limit: 1,
	}, extras)

	if stats.TotalMatched != 2 {
		t.Fatalf("TotalMatched = %d, want 2", stats.TotalMatched)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want cap 1", len(got))
	}
	if got[0].Kind != "HelmRelease" || got[0].Name != "cart" {
		t.Fatalf("first issue = %s/%s, want HelmRelease/cart", got[0].Kind, got[0].Name)
	}
}

func TestMergeExternalIssuesAggregatesDuplicateEnvWithoutExtras(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	members := []Issue{
		duplicateEnvIssueForMerge("apps", "web", "app", "APP_MODE", now.Add(-10*time.Minute), now),
		duplicateEnvIssueForMerge("apps", "web", "app", "API_PASSWORD", now.Add(-30*time.Minute), now.Add(time.Minute)),
		duplicateEnvIssueForMerge("apps", "web", "init", "INIT_MODE", now.Add(-20*time.Minute), now.Add(2*time.Minute)),
	}
	members[0].Cause = "member-only cause"
	members[0].Action = "member-only action"
	members[0].DiagnosticContext = &issuesapi.DiagnosticContext{Role: issuesapi.DiagnosticRoleCandidate}
	members[0].ChangeContext = &issuesapi.ChangeContext{}
	members[0].IssueTiming = "started_at_resource_creation"
	members[1].Message = `Container app defines env API_PASSWORD at 1="secret-one", 2="secret-two"`
	unrelated := testIssueForMerge("Deployment", "apps", "web", SeverityCritical, now.Add(-time.Hour))

	got, stats := MergeExternalIssues(append(append([]Issue(nil), members...), unrelated), ComposeStats{}, Filters{
		Grouped: true,
		Limit:   NoLimit,
	}, nil)

	if stats.TotalMatched != 2 || len(got) != 2 {
		t.Fatalf("got %d rows and TotalMatched=%d, want 2", len(got), stats.TotalMatched)
	}
	var aggregate *Issue
	for i := range got {
		if got[i].Reason == "DuplicateEnvVar" {
			aggregate = &got[i]
		}
	}
	if aggregate == nil {
		t.Fatal("duplicate-env aggregate missing")
	}
	wantMessage := "Duplicate environment variables: API_PASSWORD (app), APP_MODE (app), INIT_MODE (init). This is a configuration risk: the workload may be running normally, but later declarations shadow earlier ones and apply/patch can drop shadowed entries."
	if aggregate.Message != wantMessage {
		t.Fatalf("message = %q, want %q", aggregate.Message, wantMessage)
	}
	if strings.Contains(aggregate.Message, "secret-one") || strings.Contains(aggregate.Message, "secret-two") {
		t.Fatalf("aggregate message leaked member values: %q", aggregate.Message)
	}
	wantAction := "Remove duplicate entries from the workload manifest or chart so each environment variable is declared once per container, then redeploy through the normal delivery path."
	if aggregate.Action != wantAction {
		t.Fatalf("action = %q, want %q", aggregate.Action, wantAction)
	}
	if !aggregate.FirstSeen.Equal(now.Add(-30*time.Minute)) || !aggregate.LastSeen.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("aggregate timestamps = %s..%s", aggregate.FirstSeen, aggregate.LastSeen)
	}
	if aggregate.Count != 0 || aggregate.Cause != "" || aggregate.DiagnosticContext != nil || aggregate.ChangeContext != nil || aggregate.IssueTiming != "" {
		t.Fatalf("aggregate inherited member-only fields: %+v", *aggregate)
	}
	for _, member := range members {
		if aggregate.ID == member.ID {
			t.Fatalf("aggregate ID %q collides with member", aggregate.ID)
		}
	}
	capped, cappedStats := MergeExternalIssues(append(append([]Issue(nil), members...), unrelated), ComposeStats{}, Filters{Grouped: true, Limit: 1}, nil)
	if len(capped) != 1 || cappedStats.TotalMatched != 2 || capped[0].ID != unrelated.ID {
		t.Fatalf("post-aggregation cap = %+v, TotalMatched=%d", capped, cappedStats.TotalMatched)
	}

	reversed := []Issue{members[2], unrelated, members[1], members[0]}
	again, _ := MergeExternalIssues(reversed, ComposeStats{}, Filters{Grouped: true, Limit: NoLimit}, nil)
	for i := range again {
		if again[i].Reason == "DuplicateEnvVar" && again[i].ID != aggregate.ID {
			t.Fatalf("aggregate ID changed under permutation: %q != %q", again[i].ID, aggregate.ID)
		}
	}
	workerMembers := []Issue{
		duplicateEnvIssueForMerge("apps", "worker", "app", "APP_MODE", now, now),
		duplicateEnvIssueForMerge("apps", "worker", "app", "API_PASSWORD", now, now),
	}
	workerAggregate := newDuplicateEnvAggregate(workerMembers)
	if workerAggregate.ID == aggregate.ID {
		t.Fatalf("aggregate ID %q collides across workloads", aggregate.ID)
	}
	if !reflect.DeepEqual(got[0], unrelated) {
		t.Fatalf("unrelated issue changed:\n got: %+v\nwant: %+v", got[0], unrelated)
	}
}

func TestMergeExternalIssuesDuplicateEnvPresentationBoundaries(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	web := []Issue{
		duplicateEnvIssueForMerge("apps", "web", "app", "APP_MODE", now, now),
		duplicateEnvIssueForMerge("apps", "web", "init", "APP_MODE", now, now),
	}
	worker := duplicateEnvIssueForMerge("apps", "worker", "app", "APP_MODE", now, now)
	extra := testIssueForMerge("HelmRelease", "apps", "cart", SeverityCritical, now)

	grouped, stats := MergeExternalIssues(append(web, worker), ComposeStats{}, Filters{Grouped: true, Limit: NoLimit}, []Issue{extra})
	if len(grouped) != 3 || stats.TotalMatched != 3 {
		t.Fatalf("grouped rows=%d TotalMatched=%d, want 3", len(grouped), stats.TotalMatched)
	}
	for _, issue := range grouped {
		if issue.Name == "worker" && issue.Message != worker.Message {
			t.Fatalf("single duplicate changed from detailed message %q to %q", worker.Message, issue.Message)
		}
	}
	flat, _ := MergeExternalIssues(append(web, worker), ComposeStats{}, Filters{Grouped: false, Limit: NoLimit}, nil)
	if len(flat) != 3 {
		t.Fatalf("flat rows=%d, want 3 detailed findings", len(flat))
	}

	p := &fakeProvider{problems: []k8s.Detection{
		{Group: "apps", Kind: "Deployment", Namespace: "apps", Name: "web", Severity: "warning", Reason: "DuplicateEnvVar", Message: "APP_MODE in app", Fingerprint: k8s.FormatDuplicateEnvVarFingerprint("apps", "web", "app", "APP_MODE")},
		{Group: "apps", Kind: "Deployment", Namespace: "apps", Name: "web", Severity: "warning", Reason: "DuplicateEnvVar", Message: "APP_MODE in init", Fingerprint: k8s.FormatDuplicateEnvVarFingerprint("apps", "web", "init", "APP_MODE")},
	}}
	base, baseStats := ComposeWithStats(p, Filters{Grouped: true, Limit: NoLimit})
	composed, composedStats := MergeExternalIssues(base, baseStats, Filters{Grouped: true, Limit: NoLimit}, nil)
	if len(composed) != 1 || composedStats.TotalMatched != 1 || !strings.Contains(composed[0].Message, "APP_MODE (app), APP_MODE (init)") {
		t.Fatalf("composed aggregate lost env evidence: %+v, TotalMatched=%d", composed, composedStats.TotalMatched)
	}
	related := RelatedIssues(p, nil, "apps", "Deployment", "apps", "web")
	if len(related) != 2 {
		t.Fatalf("RelatedIssues rows=%d, want 2 detailed findings", len(related))
	}
}

func TestMergeExternalIssuesFiltersDuplicateEnvAggregatePublicShape(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	members := []Issue{
		duplicateEnvIssueForMerge("apps", "web", "app", "APP_MODE", now, now),
		duplicateEnvIssueForMerge("apps", "web", "init", "API_PASSWORD", now, now),
	}

	memberFilter, err := filter.CompileIssueFilter(`message.contains("APP_MODE in app")`)
	if err != nil {
		t.Fatal(err)
	}
	got, stats := MergeExternalIssues(members, ComposeStats{}, Filters{Grouped: true, Limit: NoLimit, Filter: memberFilter}, nil)
	if len(got) != 0 || stats.TotalMatched != 0 {
		t.Fatalf("member-only filter matched rewritten aggregate: %+v, TotalMatched=%d", got, stats.TotalMatched)
	}

	aggregateFilter, err := filter.CompileIssueFilter(`message.contains("API_PASSWORD (init)")`)
	if err != nil {
		t.Fatal(err)
	}
	got, stats = MergeExternalIssues(members, ComposeStats{}, Filters{Grouped: true, Limit: NoLimit, Filter: aggregateFilter}, nil)
	if len(got) != 1 || stats.TotalMatched != 1 || got[0].Fingerprint != duplicateEnvAggregateFingerprint {
		t.Fatalf("aggregate filter result = %+v, TotalMatched=%d", got, stats.TotalMatched)
	}
}

func TestDuplicateEnvAggregateBoundsEvidenceSummary(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	members := make([]Issue, 0, 7)
	for i := range 7 {
		members = append(members, duplicateEnvIssueForMerge("apps", "web", "app", fmt.Sprintf("VAR_%d", i), now, now))
	}

	aggregate := newDuplicateEnvAggregate(members)
	if !strings.Contains(aggregate.Message, "VAR_0 (app), VAR_1 (app), VAR_2 (app), VAR_3 (app), VAR_4 (app), +2 more") {
		t.Fatalf("bounded aggregate message = %q", aggregate.Message)
	}
	if strings.Contains(aggregate.Message, "VAR_5") || strings.Contains(aggregate.Message, "VAR_6") {
		t.Fatalf("aggregate message exceeded detail cap: %q", aggregate.Message)
	}
}

func testIssueForMerge(kind, namespace, name string, severity Severity, firstSeen time.Time) Issue {
	iss := Issue{
		Severity:  severity,
		Source:    SourceProblem,
		Kind:      kind,
		Group:     resolveGroup("", kind),
		Namespace: namespace,
		Name:      name,
		Reason:    "TestReason",
		FirstSeen: firstSeen,
		LastSeen:  firstSeen,
	}
	classifyIssue(&iss)
	enrichIdentity(&iss)
	return iss
}

func duplicateEnvIssueForMerge(namespace, name, container, envName string, firstSeen, lastSeen time.Time) Issue {
	iss := Issue{
		Severity:    SeverityWarning,
		Source:      SourceProblem,
		Kind:        "Deployment",
		Group:       "apps",
		Namespace:   namespace,
		Name:        name,
		Reason:      "DuplicateEnvVar",
		Message:     envName + " in " + container,
		Fingerprint: k8s.FormatDuplicateEnvVarFingerprint(namespace, name, container, envName),
		FirstSeen:   firstSeen,
		LastSeen:    lastSeen,
	}
	classifyIssue(&iss)
	enrichIdentity(&iss)
	return iss
}
