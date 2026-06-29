package mcp

import (
	"context"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
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
	// Backdate observation past the full window: a fresh store has watched
	// for ~0s and correlation correctly refuses to claim anything.
	timeline.SetObservationStartForTest(time.Now().Add(-2 * time.Hour))
	return timeline.GetStore()
}

// A store that has only just started observing must emit NO markers — in
// either direction — rather than claim an hour-long window it never watched.
func TestAttachIssueChangeCorrelation_SkipsOnShortObservation(t *testing.T) {
	initCorrelationStore(t)
	timeline.SetObservationStartForTest(time.Now().Add(-90 * time.Second))

	resp := issues.ListResponse{Issues: []issuesapi.Issue{criticalIssue("Deployment", "web")}}
	attachIssueChangeCorrelation(context.Background(), &resp)

	if resp.Issues[0].NoRecentChanges != nil || len(resp.Issues[0].CorrelatedChanges) > 0 {
		t.Fatalf("90s-old store must not claim anything: %+v", resp.Issues[0])
	}
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
	// Status churn on the chronic workload — the SYMPTOM, not a change; it
	// must not count as correlation evidence.
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "quiet-status", Timestamp: time.Now().Add(-2 * time.Minute),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Deployment", Namespace: "shop", Name: "quiet",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "status.readyReplicas", OldValue: int32(1), NewValue: int32(0)}}, Summary: "ready: 1→0"},
	}); err != nil {
		t.Fatalf("append quiet status: %v", err)
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
		t.Fatalf("quiet should carry no_recent_changes{3600} despite status churn, got %+v (correlated=%+v)", quiet.NoRecentChanges, quiet.CorrelatedChanges)
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

// The claim window clamps to actual observation time: a process restarted 30
// minutes ago must not assert anything about the full default hour.
func TestCorrelationWindow_ClampsToObservation(t *testing.T) {
	initCorrelationStore(t)
	cases := []struct {
		name     string
		observed time.Duration // 0 = zero observation start (no store)
		want     time.Duration
	}{
		{"observed longer than default window", 2 * time.Hour, meaningfulchanges.DefaultSince},
		{"clamped to observation time", 30 * time.Minute, 30 * time.Minute},
		{"below minimum observation", 4 * time.Minute, 0},
		{"zero observation start", 0, 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if tt.observed == 0 {
				timeline.SetObservationStartForTest(time.Time{})
			} else {
				timeline.SetObservationStartForTest(time.Now().Add(-tt.observed))
			}
			got := correlationWindow()
			if tt.want == 0 {
				if got != 0 {
					t.Fatalf("window = %v, want 0", got)
				}
				return
			}
			// The clamped window is measured a beat after the start is set, so
			// it can exceed want by the elapsed time between the two calls.
			if got > tt.want+time.Second || got < tt.want-time.Second {
				t.Fatalf("window = %v, want ~%v", got, tt.want)
			}
		})
	}
}

// When the subject's candidate fetch saturates (churn-heavy subjects can
// exceed the newest-N window in an hour), an empty filtered result is
// UNKNOWN: emitting no_recent_changes would steer the agent away from a real
// change the fetch never saw.
func TestAttachIssueChangeCorrelation_OmitsMarkerOnSaturatedFetch(t *testing.T) {
	store := initCorrelationStore(t)
	now := time.Now()
	for i := 0; i < 100; i++ {
		if err := store.Append(context.Background(), timeline.TimelineEvent{
			ID: fmt.Sprintf("churn-%d", i), Timestamp: now.Add(-time.Duration(i) * time.Second),
			Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "Deployment", Namespace: "shop", Name: "web",
			EventType: timeline.EventTypeUpdate,
			Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "status.readyReplicas", OldValue: int32(1), NewValue: int32(0)}}, Summary: "ready: 1→0"},
		}); err != nil {
			t.Fatalf("append churn %d: %v", i, err)
		}
	}

	resp := issues.ListResponse{Issues: []issuesapi.Issue{criticalIssue("Deployment", "web")}}
	attachIssueChangeCorrelation(context.Background(), &resp)

	web := resp.Issues[0]
	if web.NoRecentChanges != nil {
		t.Fatalf("saturated fetch must omit the marker (unknown, not 'no changes'), got %+v", web.NoRecentChanges)
	}
	if len(web.CorrelatedChanges) != 0 {
		t.Fatalf("status churn must not surface as correlated changes: %+v", web.CorrelatedChanges)
	}
}

// A critical workload's correlation fans out to ConfigMaps its pod spec
// references directly — the headline scenario: a config change crashes the
// consumer, and the issue carries the ConfigMap change as evidence.
func TestAttachIssueChangeCorrelation_WorkloadConfigMapFanout(t *testing.T) {
	client := k8sfake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "flagd", Namespace: "shop"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: "config",
					VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "flagd-config"},
					}},
				}},
				Containers: []corev1.Container{{Name: "flagd", Image: "flagd:v1"}},
			}},
		},
	})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
	store := initCorrelationStore(t)

	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "cm-change", Timestamp: time.Now().Add(-5 * time.Minute),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "ConfigMap", Namespace: "shop", Name: "flagd-config",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data.flags.adHighCpu.defaultVariant", OldValue: "off", NewValue: "on"}}, Summary: "flag changed"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	resp := issues.ListResponse{Issues: []issuesapi.Issue{criticalIssue("Deployment", "flagd")}}
	attachIssueChangeCorrelation(context.Background(), &resp)

	flagd := resp.Issues[0]
	if len(flagd.CorrelatedChanges) != 1 {
		t.Fatalf("correlated_changes = %+v, want exactly the ConfigMap change", flagd.CorrelatedChanges)
	}
	c := flagd.CorrelatedChanges[0]
	if c.Kind != "ConfigMap" || c.Name != "flagd-config" {
		t.Fatalf("unexpected correlated change: %+v", c)
	}
	if len(c.ConsumedBy) != 1 || c.ConsumedBy[0] != "Deployment/flagd" {
		t.Fatalf("consumed_by = %v, want [Deployment/flagd]", c.ConsumedBy)
	}
	if flagd.NoRecentChanges != nil {
		t.Fatalf("issue with correlated changes must not also carry no_recent_changes: %+v", flagd.NoRecentChanges)
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
