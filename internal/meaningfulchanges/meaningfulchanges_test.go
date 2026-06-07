package meaningfulchanges

import (
	"context"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestShouldAttachIssueSidecar(t *testing.T) {
	if !ShouldAttachIssueSidecar(nil) {
		t.Fatalf("zero critical issues should allow the sidecar")
	}
	if got := IssueSidecarReason(nil); got != SidecarReasonNoCriticalIssues {
		t.Fatalf("IssueSidecarReason(nil) = %q, want %q", got, SidecarReasonNoCriticalIssues)
	}
	baseline := []issuesapi.Issue{{Severity: issuesapi.SeverityCritical, IssueTiming: "started_at_resource_creation"}}
	if !ShouldAttachIssueSidecar(baseline) {
		t.Fatalf("baseline-dominated critical issues should allow the sidecar")
	}
	if got := IssueSidecarReason(baseline); got != SidecarReasonAllCriticalIssuesStartedAtResourceCreation {
		t.Fatalf("IssueSidecarReason(baseline) = %q, want %q", got, SidecarReasonAllCriticalIssuesStartedAtResourceCreation)
	}
	runtime := []issuesapi.Issue{{Severity: issuesapi.SeverityCritical, IssueTiming: "started_after_resource_was_healthy"}}
	if ShouldAttachIssueSidecar(runtime) {
		t.Fatalf("runtime critical issue should suppress the sidecar")
	}
	if got := IssueSidecarReason(runtime); got != "" {
		t.Fatalf("IssueSidecarReason(runtime) = %q, want empty", got)
	}
	unknown := []issuesapi.Issue{{Severity: issuesapi.SeverityCritical}}
	if ShouldAttachIssueSidecar(unknown) {
		t.Fatalf("critical issue with unknown timing should suppress the sidecar")
	}
	if got := IssueSidecarReason(unknown); got != "" {
		t.Fatalf("IssueSidecarReason(unknown) = %q, want empty", got)
	}
}

func TestIssueSidecarQueryEligible(t *testing.T) {
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
			if got := IssueSidecarQueryEligible(tt.kind, tt.filter, tt.severity); got != tt.want {
				t.Fatalf("IssueSidecarQueryEligible() = %v, want %v", got, tt.want)
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
		ID:        "status",
		Timestamp: now,
		Source:    timeline.SourceInformer,
		Kind:      "Deployment",
		Namespace: "shop",
		Name:      "frontend",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "status.readyReplicas", OldValue: int32(1), NewValue: int32(0)}}, Summary: "ready: 1→0"},
	}); err != nil {
		t.Fatalf("append status: %v", err)
	}
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID:        "config",
		Timestamp: now.Add(-time.Minute),
		Source:    timeline.SourceInformer,
		Kind:      "ConfigMap",
		Namespace: "shop",
		Name:      "flagd-config",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data.flags.paymentFailure.defaultVariant", OldValue: "off", NewValue: "on"}}, Summary: "flag changed"},
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
