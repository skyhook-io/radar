package mcp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func initCorrelationStore(t *testing.T) timeline.EventStore {
	t.Helper()
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 200}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	return timeline.GetStore()
}

func criticalIssue(kind, name string) issuesapi.Issue {
	return issuesapi.Issue{
		Severity: issuesapi.SeverityCritical,
		Kind:     kind, Namespace: "shop", Name: name,
		Reason: "CrashLoopBackOff",
	}
}

// The changed workload gets correlated_changes; the chronic one gets an
// explicit no_recent_changes marker with the window.
func TestAttachIssueChangeCorrelation_Markers(t *testing.T) {
	store := initCorrelationStore(t)
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "web-spec", Timestamp: time.Now().Add(-5 * time.Minute),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Deployment", Namespace: "shop", Name: "web",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.template.spec.containers[web].readinessProbe", OldValue: "/health", NewValue: "/healthz"}}, Summary: "readinessProbe(web) changed"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	resp := issues.ListResponse{Issues: []issuesapi.Issue{
		criticalIssue("Deployment", "web"),
		criticalIssue("Deployment", "quiet"),
		{Severity: issuesapi.SeverityWarning, Kind: "Deployment", Namespace: "shop", Name: "warn-only"},
		criticalIssue("Pod", "standalone"), // untracked kind: no marker either way
	}}
	attachIssueChangeCorrelation(context.Background(), &resp)

	web := resp.Issues[0]
	if len(web.CorrelatedChanges) == 0 {
		t.Fatalf("web should carry correlated_changes, got %+v", web)
	}
	if web.NoRecentChanges != nil {
		t.Fatalf("web has both markers: %+v", web)
	}
	quiet := resp.Issues[1]
	if quiet.NoRecentChanges == nil || quiet.NoRecentChanges.WindowSeconds != 3600 {
		t.Fatalf("quiet should carry no_recent_changes{3600}, got %+v", quiet.NoRecentChanges)
	}
	if warn := resp.Issues[2]; warn.NoRecentChanges != nil || len(warn.CorrelatedChanges) != 0 {
		t.Fatalf("warning issues must not be correlated: %+v", warn)
	}
	if pod := resp.Issues[3]; pod.NoRecentChanges != nil || len(pod.CorrelatedChanges) != 0 {
		t.Fatalf("untracked kinds must not carry markers (cannot truthfully claim 'no changes'): %+v", pod)
	}
	if resp.CorrelationTruncated {
		t.Fatal("no truncation expected")
	}
}

// Past the cap, remaining criticals are skipped and the response says so.
func TestAttachIssueChangeCorrelation_Truncation(t *testing.T) {
	initCorrelationStore(t)

	var list []issuesapi.Issue
	for i := 0; i < correlationIssueCap+2; i++ {
		list = append(list, criticalIssue("Deployment", fmt.Sprintf("dep-%d", i)))
	}
	resp := issues.ListResponse{Issues: list}
	attachIssueChangeCorrelation(context.Background(), &resp)

	if !resp.CorrelationTruncated {
		t.Fatal("correlation_truncated must be set when criticals exceed the cap")
	}
	marked := 0
	for _, iss := range resp.Issues {
		if iss.NoRecentChanges != nil || len(iss.CorrelatedChanges) > 0 {
			marked++
		}
	}
	if marked != correlationIssueCap {
		t.Fatalf("marked = %d, want exactly the cap (%d)", marked, correlationIssueCap)
	}
	// The issues past the cap carry NO marker — "not checked", never a false
	// "no changes".
	last := resp.Issues[len(resp.Issues)-1]
	if last.NoRecentChanges != nil || len(last.CorrelatedChanges) > 0 {
		t.Fatalf("issue past cap must be unmarked: %+v", last)
	}
}
