package issues

import (
	"reflect"
	"testing"
	"time"

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
	wantMessage := "This workload has duplicate environment variable definitions in 3 places across its containers. Later declarations hide earlier ones, and apply/patch may drop hidden entries."
	if aggregate.Message != wantMessage {
		t.Fatalf("message = %q, want %q", aggregate.Message, wantMessage)
	}
	if !aggregate.FirstSeen.Equal(now.Add(-30*time.Minute)) || !aggregate.LastSeen.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("aggregate timestamps = %s..%s", aggregate.FirstSeen, aggregate.LastSeen)
	}
	if aggregate.Count != 0 || aggregate.Cause != "" || aggregate.Action != "" || aggregate.DiagnosticContext != nil || aggregate.ChangeContext != nil || aggregate.IssueTiming != "" {
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
		{Group: "apps", Kind: "Deployment", Namespace: "apps", Name: "web", Severity: "warning", Reason: "DuplicateEnvVar", Message: "APP_MODE in app", Fingerprint: "dup-env:apps:web:app:APP_MODE"},
		{Group: "apps", Kind: "Deployment", Namespace: "apps", Name: "web", Severity: "warning", Reason: "DuplicateEnvVar", Message: "APP_MODE in init", Fingerprint: "dup-env:apps:web:init:APP_MODE"},
	}}
	related := RelatedIssues(p, nil, "apps", "Deployment", "apps", "web")
	if len(related) != 2 {
		t.Fatalf("RelatedIssues rows=%d, want 2 detailed findings", len(related))
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
		Fingerprint: "dup-env:" + namespace + ":" + name + ":" + container + ":" + envName,
		FirstSeen:   firstSeen,
		LastSeen:    lastSeen,
	}
	classifyIssue(&iss)
	enrichIdentity(&iss)
	return iss
}
