package meaningfulchanges

import (
	"context"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestShouldAttachIssueChanges(t *testing.T) {
	if !ShouldAttachIssueChanges(nil) {
		t.Fatalf("zero critical issues should allow the recent-changes attachment")
	}
	if got := IssueChangesReason(nil); got != ChangesReasonNoCriticalIssues {
		t.Fatalf("IssueChangesReason(nil) = %q, want %q", got, ChangesReasonNoCriticalIssues)
	}
	baseline := []issuesapi.Issue{{Severity: issuesapi.SeverityCritical, IssueTiming: "started_at_resource_creation"}}
	if !ShouldAttachIssueChanges(baseline) {
		t.Fatalf("baseline-dominated critical issues should allow the recent-changes attachment")
	}
	if got := IssueChangesReason(baseline); got != ChangesReasonAllCriticalStartedAtCreation {
		t.Fatalf("IssueChangesReason(baseline) = %q, want %q", got, ChangesReasonAllCriticalStartedAtCreation)
	}
	runtime := []issuesapi.Issue{{Severity: issuesapi.SeverityCritical, IssueTiming: "started_after_resource_was_healthy"}}
	if ShouldAttachIssueChanges(runtime) {
		t.Fatalf("runtime critical issue should suppress the recent-changes attachment")
	}
	if got := IssueChangesReason(runtime); got != "" {
		t.Fatalf("IssueChangesReason(runtime) = %q, want empty", got)
	}
	unknown := []issuesapi.Issue{{Severity: issuesapi.SeverityCritical}}
	if ShouldAttachIssueChanges(unknown) {
		t.Fatalf("critical issue with unknown timing should suppress the recent-changes attachment")
	}
	if got := IssueChangesReason(unknown); got != "" {
		t.Fatalf("IssueChangesReason(unknown) = %q, want empty", got)
	}
}

func TestIssueChangesQueryEligible(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		filter   string
		severity string
		want     bool
	}{
		{name: "default query", want: true},
		{name: "critical only", severity: "critical", want: true},
		{name: "critical and warning", severity: "critical,warning", want: true},
		{name: "warning only", severity: "warning", want: false},
		{name: "kind filtered", kind: "Deployment", want: false},
		{name: "cel filtered", filter: `category == "crashloop"`, want: false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := IssueChangesQueryEligible(tt.kind, tt.filter, tt.severity); got != tt.want {
				t.Fatalf("IssueChangesQueryEligible() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRecentRanksSpecConfigAboveStatusChurn(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	store := timeline.GetStore()
	now := time.Now()
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID:             "status",
		Timestamp:      now,
		Source:         timeline.SourceInformer,
		ClusterContext: k8s.ActiveClusterContext(),
		Kind:           "Deployment",
		Namespace:      "shop",
		Name:           "frontend",
		EventType:      timeline.EventTypeUpdate,
		Diff:           &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "status.readyReplicas", OldValue: int32(1), NewValue: int32(0)}}, Summary: "ready: 1→0"},
	}); err != nil {
		t.Fatalf("append status: %v", err)
	}
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID:             "config",
		Timestamp:      now.Add(-time.Minute),
		Source:         timeline.SourceInformer,
		ClusterContext: k8s.ActiveClusterContext(),
		Kind:           "ConfigMap",
		Namespace:      "shop",
		Name:           "flagd-config",
		EventType:      timeline.EventTypeUpdate,
		Diff:           &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data.flags.paymentFailure.defaultVariant", OldValue: "off", NewValue: "on"}}, Summary: "flag changed"},
	}); err != nil {
		t.Fatalf("append config: %v", err)
	}

	changes, _, err := Recent(context.Background(), Query{Namespaces: []string{"shop"}, Limit: 2})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes length = %d, want 2", len(changes))
	}
	if changes[0].Kind != "ConfigMap" || changes[0].ChangeCategory != "spec_config" {
		t.Fatalf("first change = %+v, want ConfigMap spec_config", changes[0])
	}
}

// The persistent timeline outlives context switches; Recent must never serve
// another cluster's events as this cluster's changes.
func TestRecentExcludesOtherClusterEvents(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	store := timeline.GetStore()
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "foreign", Timestamp: time.Now(), Source: timeline.SourceInformer,
		Kind: "Deployment", Namespace: "shop", Name: "frontend",
		EventType:      timeline.EventTypeUpdate,
		ClusterContext: "some-other-cluster",
		Diff:           &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.replicas", OldValue: int32(1), NewValue: int32(3)}}, Summary: "replicas: 1→3"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	changes, _, err := Recent(context.Background(), Query{Namespaces: []string{"shop"}, Since: time.Hour, Limit: 5, FieldLimit: 5})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("another cluster's events leaked into Recent: %+v", changes)
	}
}
