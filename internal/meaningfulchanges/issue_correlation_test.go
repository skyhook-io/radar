package meaningfulchanges

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// correlatedList stands in for the issue list responses the surfaces attach
// correlation to.
type correlatedList struct {
	Issues               []issuesapi.Issue
	CorrelationTruncated bool
}

var passThroughSources = CorrelationSources{
	Visible: func(_ context.Context, changes []issuesapi.RecentChange) []issuesapi.RecentChange { return changes },
}

func attachForTest(ctx context.Context, resp *correlatedList) {
	resp.CorrelationTruncated = AttachIssueChangeCorrelation(ctx, resp.Issues, passThroughSources)
}

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

	resp := correlatedList{Issues: []issuesapi.Issue{criticalIssue("Deployment", "web")}}
	attachForTest(context.Background(), &resp)

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

func warningIssue(kind, name string) issuesapi.Issue {
	return issuesapi.Issue{
		Severity: issuesapi.SeverityWarning,
		Kind:     kind, Namespace: "shop", Name: name,
		Reason: "SelectorMatchesNoPods",
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

	// A warning-severity Service whose selector was just changed — the
	// degraded-not-down case warning correlation exists for.
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "svc-selector", Timestamp: time.Now().Add(-3 * time.Minute),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Service", Namespace: "shop", Name: "warn-changed",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.selector.app", OldValue: "cart", NewValue: "cartt"}}, Summary: "selector changed"},
	}); err != nil {
		t.Fatalf("append service selector: %v", err)
	}

	resp := correlatedList{Issues: []issuesapi.Issue{
		criticalIssue("Deployment", "web"),
		criticalIssue("Deployment", "quiet"),
		warningIssue("Service", "warn-changed"),
		warningIssue("Deployment", "warn-quiet"),
		criticalIssue("Pod", "standalone"), // untracked kind: no marker either way
	}}
	attachForTest(context.Background(), &resp)

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
	warn := resp.Issues[2]
	if len(warn.CorrelatedChanges) != 1 || warn.CorrelatedChanges[0].Kind != "Service" || warn.CorrelatedChanges[0].Name != "warn-changed" {
		t.Fatalf("warning issue should carry its subject's spec change, got %+v", warn.CorrelatedChanges)
	}
	warnQuiet := resp.Issues[3]
	if warnQuiet.NoRecentChanges == nil {
		t.Fatalf("unchanged warning subject should carry no_recent_changes, got %+v", warnQuiet)
	}
	if pod := resp.Issues[4]; pod.NoRecentChanges != nil || len(pod.CorrelatedChanges) != 0 {
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
		{"observed longer than default window", 2 * time.Hour, DefaultSince},
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
			got, _ := CorrelationWindow()
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

	resp := correlatedList{Issues: []issuesapi.Issue{criticalIssue("Deployment", "web")}}
	attachForTest(context.Background(), &resp)

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

	resp := correlatedList{Issues: []issuesapi.Issue{criticalIssue("Deployment", "flagd")}}
	attachForTest(context.Background(), &resp)

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
	for i := 0; i < CorrelationIssueCap+2; i++ {
		list = append(list, criticalIssue("Deployment", fmt.Sprintf("dep-%d", i)))
	}
	resp := correlatedList{Issues: list}
	attachForTest(context.Background(), &resp)

	if !resp.CorrelationTruncated {
		t.Fatal("correlation_truncated must be set when criticals exceed the cap")
	}
	marked := 0
	for _, iss := range resp.Issues {
		if iss.NoRecentChanges != nil || len(iss.CorrelatedChanges) > 0 {
			marked++
		}
	}
	if marked != CorrelationIssueCap {
		t.Fatalf("marked = %d, want exactly the cap (%d)", marked, CorrelationIssueCap)
	}
	// The issues past the cap carry NO marker — "not checked", never a false
	// "no changes".
	last := resp.Issues[len(resp.Issues)-1]
	if last.NoRecentChanges != nil || len(last.CorrelatedChanges) > 0 {
		t.Fatalf("issue past cap must be unmarked: %+v", last)
	}
}

// Criticals own the cap regardless of where they sit in the response: a
// pile of warnings listed first must not consume the criticals' slots.
func TestAttachIssueChangeCorrelation_CriticalsPriorityUnderCap(t *testing.T) {
	initCorrelationStore(t)

	var list []issuesapi.Issue
	for i := 0; i < CorrelationIssueCap+2; i++ {
		list = append(list, warningIssue("Deployment", fmt.Sprintf("warn-%d", i)))
	}
	list = append(list, criticalIssue("Deployment", "crit-a"), criticalIssue("Deployment", "crit-b"))
	resp := correlatedList{Issues: list}
	attachForTest(context.Background(), &resp)

	if !resp.CorrelationTruncated {
		t.Fatal("correlation_truncated must be set when issues exceed the shared cap")
	}
	marked := 0
	for _, iss := range resp.Issues {
		if iss.NoRecentChanges != nil || len(iss.CorrelatedChanges) > 0 {
			marked++
			if iss.Severity == issuesapi.SeverityWarning && marked > CorrelationIssueCap {
				t.Fatalf("warning %s marked past the cap", iss.Name)
			}
		}
	}
	if marked != CorrelationIssueCap {
		t.Fatalf("marked = %d, want exactly the cap (%d)", marked, CorrelationIssueCap)
	}
	for _, iss := range resp.Issues {
		if iss.Severity == issuesapi.SeverityCritical && iss.NoRecentChanges == nil && len(iss.CorrelatedChanges) == 0 {
			t.Fatalf("critical %s lost its slot to an earlier-listed warning", iss.Name)
		}
	}
}

// A warning-only response truncates on the same shared cap.
func TestAttachIssueChangeCorrelation_WarningOnlyTruncation(t *testing.T) {
	initCorrelationStore(t)

	var list []issuesapi.Issue
	for i := 0; i < CorrelationIssueCap+2; i++ {
		list = append(list, warningIssue("Deployment", fmt.Sprintf("warn-%d", i)))
	}
	resp := correlatedList{Issues: list}
	attachForTest(context.Background(), &resp)

	if !resp.CorrelationTruncated {
		t.Fatal("correlation_truncated must be set for warning-only overflow")
	}
	marked := 0
	for _, iss := range resp.Issues {
		if iss.NoRecentChanges != nil || len(iss.CorrelatedChanges) > 0 {
			marked++
		}
	}
	if marked != CorrelationIssueCap {
		t.Fatalf("marked = %d, want exactly the cap (%d)", marked, CorrelationIssueCap)
	}
}

// A CRD issue whose kind collides with a tracked core kind (Knative Service
// vs core Service) must not be correlated against the same-named core
// object's changes — and must not carry a marker either way (its own changes
// are not tracked, so "no changes" would be a false statement).
func TestAttachIssueChangeCorrelation_GroupCollisionNotCorrelated(t *testing.T) {
	store := initCorrelationStore(t)
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "core-svc-change", Timestamp: time.Now().Add(-5 * time.Minute),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Service", Namespace: "shop", Name: "web",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.selector.app", OldValue: "a", NewValue: "b"}}, Summary: "selector changed"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	knative := warningIssue("Service", "web")
	knative.Group = "serving.knative.dev"
	core := warningIssue("Service", "web") // Group empty = unknown/core, keeps correlating
	resp := correlatedList{Issues: []issuesapi.Issue{knative, core}}
	attachForTest(context.Background(), &resp)

	if k := resp.Issues[0]; k.NoRecentChanges != nil || len(k.CorrelatedChanges) != 0 {
		t.Fatalf("Knative-group Service must not be correlated against core Service changes: %+v", k)
	}
	if c := resp.Issues[1]; len(c.CorrelatedChanges) != 1 {
		t.Fatalf("core Service should still correlate its own change, got %+v", c)
	}
}

// Secret issues are delete-only in the feed — updates are never recorded —
// so they must never be marker-eligible: a no_recent_changes after a data
// rotation the feed cannot see would be a false claim.
func TestAttachIssueChangeCorrelation_SecretIssuesNotMarkerEligible(t *testing.T) {
	initCorrelationStore(t)
	resp := correlatedList{Issues: []issuesapi.Issue{warningIssue("Secret", "db-credentials")}}
	attachForTest(context.Background(), &resp)
	if sec := resp.Issues[0]; sec.NoRecentChanges != nil || len(sec.CorrelatedChanges) != 0 {
		t.Fatalf("Secret issue must carry no markers (updates not recorded): %+v", sec)
	}
}

// Reverse collision direction: a CORE Service issue must not absorb a
// same-named Knative Service's change events — candidate events are group
// filtered, so the core subject truthfully reports no_recent_changes.
func TestAttachIssueChangeCorrelation_CoreIssueIgnoresCRDEvents(t *testing.T) {
	store := initCorrelationStore(t)
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "knative-svc-change", Timestamp: time.Now().Add(-5 * time.Minute),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Service", APIVersion: "serving.knative.dev/v1", Namespace: "shop", Name: "web",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.template", OldValue: "a", NewValue: "b"}}, Summary: "revision changed"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	resp := correlatedList{Issues: []issuesapi.Issue{warningIssue("Service", "web")}}
	attachForTest(context.Background(), &resp)

	core := resp.Issues[0]
	if len(core.CorrelatedChanges) != 0 {
		t.Fatalf("core Service issue absorbed a Knative Service's change: %+v", core.CorrelatedChanges)
	}
	if core.NoRecentChanges == nil {
		t.Fatalf("core Service should truthfully report no tracked (core) changes, got %+v", core)
	}
}

// Mismatched-group churn is filtered by the store before the bounded candidate
// window is applied. It therefore cannot saturate a tracked core resource's
// query or turn an observed empty core window into an ambiguous result.
func TestAttachIssueChangeCorrelation_CRDCrowdingDoesNotSaturateCoreQuery(t *testing.T) {
	store := initCorrelationStore(t)
	now := time.Now()
	for i := 0; i < 100; i++ { // name-filtered candidate limit
		if err := store.Append(context.Background(), timeline.TimelineEvent{
			ID: fmt.Sprintf("knative-%d", i), Timestamp: now.Add(-time.Duration(i) * time.Second),
			Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "Service", APIVersion: "serving.knative.dev/v1", Namespace: "shop", Name: "web",
			EventType: timeline.EventTypeUpdate,
			Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.template", OldValue: i, NewValue: i + 1}}, Summary: "revision churn"},
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	resp := correlatedList{Issues: []issuesapi.Issue{warningIssue("Service", "web")}}
	attachForTest(context.Background(), &resp)

	core := resp.Issues[0]
	if len(core.CorrelatedChanges) != 0 {
		t.Fatalf("core issue absorbed CRD events: %+v", core.CorrelatedChanges)
	}
	if core.NoRecentChanges == nil {
		t.Fatalf("CRD churn must not make the empty core window ambiguous: %+v", core)
	}
}

// Worst-case correlation payload (cap × changes × field diffs, with realistic
// field values) stays bounded — a guard against unnoticed schema growth now
// that warnings are eligible too.
func TestIssueCorrelation_WorstCasePayloadBounded(t *testing.T) {
	var iss []issuesapi.Issue
	for i := 0; i < CorrelationIssueCap; i++ {
		issue := criticalIssue("Deployment", fmt.Sprintf("dep-%d", i))
		for c := 0; c < CorrelationChangeCap; c++ {
			change := issuesapi.RecentChange{
				Source: "informer", Kind: "Deployment", Namespace: "shop",
				Name:           fmt.Sprintf("dep-%d", i),
				ChangeType:     "update",
				ChangeCategory: issuesapi.ChangeCategorySpecConfig,
				Summary:        "spec changed: containers[app].image, containers[app].resources, replicas, strategy, template.metadata.labels",
			}
			for f := 0; f < CorrelationFieldLimit; f++ {
				change.Fields = append(change.Fields, issuesapi.ChangeField{
					Path:     fmt.Sprintf("spec.template.spec.containers[app].env[FEATURE_FLAG_%d].value", f),
					OldValue: "a-realistically-long-previous-configuration-value-1234567890",
					NewValue: "a-realistically-long-updated-configuration-value-0987654321",
				})
			}
			issue.CorrelatedChanges = append(issue.CorrelatedChanges, change)
		}
		iss = append(iss, issue)
	}
	data, err := json.Marshal(issuesapi.Response{Issues: iss, Total: len(iss)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const maxBytes = 128 * 1024
	if len(data) > maxBytes {
		t.Fatalf("worst-case correlation payload = %d bytes, exceeds %d — correlation caps no longer bound the response", len(data), maxBytes)
	}
	t.Logf("worst-case correlation payload: %d bytes", len(data))
}

// Once the store has dropped events, a "no changes" claim may only cover the
// history it still holds: a busy cluster's evicted ring must not back an
// hour-long claim.
func TestCorrelationWindow_ClampsToRetainedHistory(t *testing.T) {
	appendAt := func(t *testing.T, store timeline.EventStore, ages ...time.Duration) {
		t.Helper()
		now := time.Now()
		for i, age := range ages {
			if err := store.Append(context.Background(), timeline.TimelineEvent{
				ID: fmt.Sprintf("evt-%d", i), Timestamp: now.Add(-age),
				Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
				Kind: "Deployment", Namespace: "shop", Name: fmt.Sprintf("dep-%d", i),
				EventType: timeline.EventTypeUpdate,
			}); err != nil {
				t.Fatalf("append %d: %v", i, err)
			}
		}
	}
	initRing := func(t *testing.T, size int) timeline.EventStore {
		t.Helper()
		timeline.ResetStore()
		t.Cleanup(timeline.ResetStore)
		if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: size}); err != nil {
			t.Fatalf("InitStore: %v", err)
		}
		timeline.SetObservationStartForTest(time.Now().Add(-2 * time.Hour))
		return timeline.GetStore()
	}

	t.Run("no eviction keeps the observation window", func(t *testing.T) {
		store := initRing(t, 10)
		appendAt(t, store, 50*time.Minute, 40*time.Minute)
		got, reason := CorrelationWindow()
		if got != DefaultSince || reason != "" {
			t.Fatalf("window = %v (%q), want %v", got, reason, DefaultSince)
		}
	})

	t.Run("eviction clamps to the oldest retained event", func(t *testing.T) {
		store := initRing(t, 3)
		appendAt(t, store, 40*time.Minute, 30*time.Minute, 20*time.Minute, 15*time.Minute, 12*time.Minute)
		got, reason := CorrelationWindow()
		if reason != "" || got > 20*time.Minute+time.Second || got < 20*time.Minute-time.Second {
			t.Fatalf("window = %v (%q), want ~20m (oldest retained event)", got, reason)
		}
	})

	t.Run("retained history too short to claim anything", func(t *testing.T) {
		store := initRing(t, 2)
		appendAt(t, store, 30*time.Minute, 3*time.Minute, 2*time.Minute)
		got, reason := CorrelationWindow()
		if got != 0 || reason != issuesapi.CorrelationHistoryIncomplete {
			t.Fatalf("window = %v (%q), want 0 (%q)", got, reason, issuesapi.CorrelationHistoryIncomplete)
		}

		resp := correlatedList{Issues: []issuesapi.Issue{criticalIssue("Deployment", "quiet")}}
		attachForTest(context.Background(), &resp)
		if q := resp.Issues[0]; q.NoRecentChanges != nil || len(q.CorrelatedChanges) != 0 {
			t.Fatalf("evicted history must not back a marker: %+v", q)
		}
	})
}

// Every way of not knowing carries its own reason, so a surface can say "Radar
// can't tell" instead of rendering silence that reads as "nothing changed".
func TestCorrelateSubject_UnknownReasons(t *testing.T) {
	store := initCorrelationStore(t)
	now := time.Now()
	for i := 0; i < 100; i++ {
		if err := store.Append(context.Background(), timeline.TimelineEvent{
			ID: fmt.Sprintf("churn-%d", i), Timestamp: now.Add(-time.Duration(i) * time.Second),
			Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "Deployment", Namespace: "shop", Name: "churny",
			EventType: timeline.EventTypeUpdate,
			Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "status.readyReplicas", OldValue: int32(1), NewValue: int32(0)}}, Summary: "ready: 1→0"},
		}); err != nil {
			t.Fatalf("append churn %d: %v", i, err)
		}
	}
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "cfg", Timestamp: now.Add(-5 * time.Minute),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Service", Namespace: "shop", Name: "hidden",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.selector.app", OldValue: "a", NewValue: "b"}}, Summary: "selector changed"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "quiet-status", Timestamp: now.Add(-3 * time.Minute),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Deployment", Namespace: "shop", Name: "quiet",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "status.readyReplicas", OldValue: int32(1), NewValue: int32(0)}}, Summary: "ready: 1→0"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	window, _ := CorrelationWindow()
	hideAll := CorrelationSources{Visible: func(context.Context, []issuesapi.RecentChange) []issuesapi.RecentChange { return nil }}
	helmRelease := issuesapi.RecentChange{Source: HelmChangeSource, Kind: "HelmRelease", Namespace: "shop", Name: "cart", ChangeCategory: issuesapi.ChangeCategorySpecConfig}
	helmSources := func(changes []issuesapi.RecentChange, err error) CorrelationSources {
		src := passThroughSources
		src.HelmChanges = func(context.Context, string, string, time.Duration) ([]issuesapi.RecentChange, error) {
			return changes, err
		}
		return src
	}

	cases := []struct {
		name        string
		kind, group string
		subject     string
		src         CorrelationSources
		want        issuesapi.CorrelationUnknownReason
		wantChanges int
	}{
		{"untracked kind", "Pod", "", "web-0", passThroughSources, issuesapi.CorrelationUntrackedKind, 0},
		{"saturated candidate fetch", "Deployment", "apps", "churny", passThroughSources, issuesapi.CorrelationHistoryIncomplete, 0},
		{"relevant change hidden from the caller", "Service", "", "hidden", hideAll, issuesapi.CorrelationCannotConfirm, 0},
		{"Helm lookup not wired", "HelmRelease", "helm.sh", "cart", passThroughSources, issuesapi.CorrelationLookupFailed, 0},
		{"Helm release not readable", "HelmRelease", "helm.sh", "cart", helmSources(nil, fmt.Errorf("list: %w", ErrCorrelationNotPermitted)), issuesapi.CorrelationNotPermitted, 0},
		{"Helm lookup error", "HelmRelease", "helm.sh", "cart", helmSources(nil, fmt.Errorf("boom")), issuesapi.CorrelationLookupFailed, 0},
		{"Helm release changed", "HelmRelease", "helm.sh", "cart", helmSources([]issuesapi.RecentChange{helmRelease}, nil), "", 1},
	}
	// Status churn is not evidence, so a filter that would hide it must not
	// turn a quiet subject into "can't tell".
	if got := CorrelateSubject(context.Background(), "Deployment", "apps", "shop", "quiet", window, hideAll); got.NoRecentChanges == nil || got.Unknown != "" {
		t.Fatalf("status-only history under a restrictive filter: want no_recent_changes, got %+v", got)
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := CorrelateSubject(context.Background(), tt.kind, tt.group, "shop", tt.subject, window, tt.src)
			if got.Unknown != tt.want || len(got.Changes) != tt.wantChanges {
				t.Fatalf("got unknown=%q changes=%d, want unknown=%q changes=%d", got.Unknown, len(got.Changes), tt.want, tt.wantChanges)
			}
			if tt.want != "" && got.NoRecentChanges != nil {
				t.Fatalf("an unknown answer must not also claim no changes: %+v", got)
			}
		})
	}
}
