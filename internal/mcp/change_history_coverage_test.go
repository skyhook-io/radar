package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// initEvictingStore returns a store that has observed for two hours but whose
// ring of maxSize has already evicted events from inside the last hour. The
// newest evicted event is at the returned time.
func initEvictingStore(t *testing.T) (timeline.EventStore, time.Time) {
	t.Helper()
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 3}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	timeline.SetObservationStartForTest(time.Now().Add(-2 * time.Hour))
	setConnectedForTest(t)
	store := timeline.GetStore()
	now := time.Now()
	for i := range 5 {
		appendInformerEvent(t, store, timeline.TimelineEvent{
			ID: fmt.Sprintf("busy-%d", i), Timestamp: now.Add(-time.Duration(50-i) * time.Minute),
			Kind: "Deployment", APIVersion: "apps/v1", Namespace: "busy", Name: "api",
			EventType: timeline.EventTypeUpdate,
			Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.replicas", OldValue: int32(i), NewValue: int32(i + 1)}}},
		})
	}
	return store, now.Add(-49 * time.Minute)
}

func appendInformerEvent(t *testing.T, store timeline.EventStore, e timeline.TimelineEvent) {
	t.Helper()
	e.Source = timeline.SourceInformer
	e.ClusterContext = k8s.ActiveClusterContext()
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf("append %s: %v", e.ID, err)
	}
}

func wireCoverage(t *testing.T, text string) map[string]any {
	t.Helper()
	var wire map[string]any
	if err := json.Unmarshal([]byte(text), &wire); err != nil {
		t.Fatalf("decode wire: %v", err)
	}
	coverage, _ := wire["historyCoverage"].(map[string]any)
	return coverage
}

// An empty answer over evicted history must say the history is incomplete,
// or it reads as a complete {"changes":[]}. The store-wide boundary goes only
// to callers who can read every namespace.
func TestGetChangesQualifiesEmptyAnswerOverEvictedHistory(t *testing.T) {
	_, evictedThrough := initEvictingStore(t)

	response, text := getChangesForTest(t, context.Background(), getChangesInput{Namespace: "quiet"})
	if len(response.Changes) != 0 {
		t.Fatalf("changes = %+v, want none", response.Changes)
	}
	coverage := response.HistoryCoverage
	if coverage == nil || !coverage.Incomplete || !slices.Equal(coverage.Reasons, []issuesapi.HistoryGapReason{issuesapi.HistoryEventsEvicted}) {
		t.Fatalf("historyCoverage = %+v, want incomplete with events_evicted", coverage)
	}
	if coverage.CompleteSince.Before(evictedThrough) || coverage.CompleteSince.After(evictedThrough.Add(time.Second)) {
		t.Fatalf("complete_since = %v, want the newest evicted event's time %v", coverage.CompleteSince, evictedThrough)
	}
	if !strings.Contains(response.NarrowHint, "is complete only from "+coverage.CompleteSince.Format(time.RFC3339)) ||
		!strings.Contains(response.NarrowHint, "does not establish that nothing changed") {
		t.Fatalf("narrowHint = %q, want the coverage sentence with the boundary", response.NarrowHint)
	}
	if _, ok := wireCoverage(t, text)["complete_since"]; !ok {
		t.Fatalf("wire historyCoverage = %s, want complete_since for an unrestricted caller", text)
	}

	restricted, restrictedText := getChangesForTest(t, withRestrictedUser(t, "quiet-viewer", []string{"quiet"}), getChangesInput{Namespace: "quiet"})
	if restricted.HistoryCoverage == nil || !restricted.HistoryCoverage.Incomplete || len(restricted.HistoryCoverage.Reasons) != 1 {
		t.Fatalf("restricted historyCoverage = %+v, want incomplete with reasons", restricted.HistoryCoverage)
	}
	if _, ok := wireCoverage(t, restrictedText)["complete_since"]; ok {
		t.Fatalf("restricted caller received the store-wide boundary: %s", restrictedText)
	}
	if strings.Contains(restricted.NarrowHint, coverage.CompleteSince.Format(time.RFC3339)) {
		t.Fatalf("restricted narrowHint carries the boundary: %q", restricted.NarrowHint)
	}

	// A window that starts after the evicted events is complete.
	covered, _ := getChangesForTest(t, context.Background(), getChangesInput{Namespace: "quiet", Since: "30m"})
	if covered.HistoryCoverage != nil || covered.NarrowHint != "" {
		t.Fatalf("covered window = %+v, want no coverage warning", covered)
	}
}

// Rows a restricted caller can't read are removed before the internal top-50
// cut, so newer unreadable rows can't push out the only readable one.
func TestGetChangesDropsUnreadableRowsBeforeTheCut(t *testing.T) {
	store := initCorrelationStore(t)
	now := time.Now()
	appendInformerEvent(t, store, timeline.TimelineEvent{
		ID: "readable", Timestamp: now.Add(-30 * time.Minute),
		Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.replicas", OldValue: int32(1), NewValue: int32(2)}}},
	})
	for i := range 60 {
		appendInformerEvent(t, store, timeline.TimelineEvent{
			ID: fmt.Sprintf("hidden-%d", i), Timestamp: now.Add(-time.Duration(60-i) * time.Second),
			Kind: "ConfigMap", APIVersion: "v1", Namespace: "shop", Name: fmt.Sprintf("cfg-%d", i),
			EventType: timeline.EventTypeUpdate,
			Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)"}}},
		})
	}
	ctx := withClusterAdmin(t, "deploy-reader")
	getPermCache().Get("deploy-reader", nil).SetCanI("list", "apps", "deployments", "shop", true)

	response, _ := getChangesForTest(t, ctx, getChangesInput{Namespace: "shop", Limit: 50})
	if len(response.Changes) != 1 || response.Changes[0].Name != "web" {
		t.Fatalf("changes = %+v, want the readable Deployment edit", response.Changes)
	}
}

func issuesForTest(t *testing.T, ctx context.Context) issuesapi.Response {
	t.Helper()
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset()); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
	result, _, err := handleIssuesTool(ctx, nil, issuesInput{Namespace: "shop"})
	if err != nil {
		t.Fatalf("handleIssuesTool: %v", err)
	}
	var response issuesapi.Response
	if err := json.Unmarshal([]byte(extractText(t, result)), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}

// A saturated recent-changes fetch with no surviving change still reports
// the truncation; an omitted block reads the same as "nothing changed".
func TestIssuesReportsSaturatedFetchWithNoSurvivingChange(t *testing.T) {
	store := initCorrelationStore(t)
	now := time.Now()
	// Secret creates fill the lifecycle query; the feed shows Secrets only
	// when deleted, so none survives.
	for i := range meaningfulchanges.FetchBound + 1 {
		appendInformerEvent(t, store, timeline.TimelineEvent{
			ID: fmt.Sprintf("secret-%d", i), Timestamp: now.Add(-time.Duration(i) * time.Second),
			Kind: "Secret", APIVersion: "v1", Namespace: "shop", Name: fmt.Sprintf("s-%d", i),
			EventType: timeline.EventTypeAdd,
		})
	}

	response := issuesForTest(t, context.Background())
	if len(response.RecentChanges) != 0 {
		t.Fatalf("recent_changes = %+v, want none", response.RecentChanges)
	}
	if !response.RecentChangesTruncated {
		t.Fatal("a saturated fetch must set recent_changes_truncated even with no surviving change")
	}
	if response.RecentChangesGuidance != meaningfulchanges.FetchSaturatedGuidance {
		t.Fatalf("recent_changes_guidance = %q, want only the saturation warning", response.RecentChangesGuidance)
	}
	if response.RecentChangesReason != meaningfulchanges.ChangesReasonNoCriticalIssues {
		t.Fatalf("recent_changes_reason = %q", response.RecentChangesReason)
	}
	if response.RecentChangesHistoryCoverage != nil {
		t.Fatalf("history is complete here, got %+v", response.RecentChangesHistoryCoverage)
	}
}

func TestIssuesQualifiesRecentChangesOverEvictedHistory(t *testing.T) {
	initEvictingStore(t)

	response := issuesForTest(t, context.Background())
	coverage := response.RecentChangesHistoryCoverage
	if coverage == nil || !coverage.Incomplete || coverage.CompleteSince.IsZero() {
		t.Fatalf("recent_changes_history_coverage = %+v, want incomplete with complete_since", coverage)
	}
	if response.RecentChangesTruncated {
		t.Fatal("coverage is reported on its own field, not as fetch truncation")
	}
	if !strings.Contains(response.RecentChangesGuidance, "is complete only from") {
		t.Fatalf("recent_changes_guidance = %q, want the coverage sentence", response.RecentChangesGuidance)
	}

	restricted := issuesForTest(t, withRestrictedUser(t, "shop-viewer", []string{"shop"}))
	if restricted.RecentChangesHistoryCoverage == nil || !restricted.RecentChangesHistoryCoverage.CompleteSince.IsZero() {
		t.Fatalf("restricted coverage = %+v, want incomplete without complete_since", restricted.RecentChangesHistoryCoverage)
	}
}

// When the subject's own history overflows its fetch bound but a change
// still surfaces, the attached list is not the subject's full history.
func TestAttachIssueChangeCorrelation_FlagsIncompleteCorrelatedChanges(t *testing.T) {
	store := initCorrelationStore(t)
	now := time.Now()
	for i := range meaningfulchanges.NamedFetchBound + 1 {
		appendInformerEvent(t, store, timeline.TimelineEvent{
			ID: fmt.Sprintf("status-%d", i), Timestamp: now.Add(-time.Duration(30*60-i) * time.Second),
			Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web",
			EventType: timeline.EventTypeUpdate,
			Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "status.readyReplicas", OldValue: int32(1), NewValue: int32(0)}}},
		})
	}
	appendInformerEvent(t, store, timeline.TimelineEvent{
		ID: "image-edit", Timestamp: now.Add(-time.Minute),
		Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.template.spec.containers[web].image", OldValue: "web:1", NewValue: "web:2"}}},
	})

	resp := issues.ListResponse{Issues: []issuesapi.Issue{criticalIssue("Deployment", "web")}}
	attachIssueChangeCorrelation(context.Background(), &resp)
	web := resp.Issues[0]
	if len(web.CorrelatedChanges) != 1 || !web.CorrelatedChangesIncomplete {
		t.Fatalf("correlated = %+v incomplete=%v, want the edit flagged incomplete", web.CorrelatedChanges, web.CorrelatedChangesIncomplete)
	}
}

func TestAttachIssueChangeCorrelation_EvictedHistory(t *testing.T) {
	correlate := func(t *testing.T, deploymentCreated time.Time) *issuesapi.Issue {
		t.Helper()
		if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", CreationTimestamp: metav1.NewTime(deploymentCreated)},
		})); err != nil {
			t.Fatalf("InitTestResourceCache: %v", err)
		}
		t.Cleanup(k8s.ResetTestState)
		initEvictingStore(t)
		iss := criticalIssue("Deployment", "web")
		// A grouped issue's resource_created_at can be a newer member's;
		// it must not date the subject.
		iss.ResourceCreatedAt = time.Now().Add(-time.Minute)
		resp := issues.ListResponse{Issues: []issuesapi.Issue{iss}}
		attachIssueChangeCorrelation(context.Background(), &resp)
		return &resp.Issues[0]
	}

	t.Run("withholds no_recent_changes for a workload older than the boundary", func(t *testing.T) {
		if got := correlate(t, time.Now().Add(-3*time.Hour)); got.NoRecentChanges != nil {
			t.Fatalf("evicted history must withhold the marker, got %+v", got.NoRecentChanges)
		}
	})

	t.Run("claims no_recent_changes for a workload created after the boundary", func(t *testing.T) {
		if got := correlate(t, time.Now().Add(-10*time.Minute)); got.NoRecentChanges == nil {
			t.Fatal("a workload created after the history boundary has complete history and earns the marker")
		}
	})
}

// A caller who can read a kind cluster-wide is answered with one check per
// kind, not one per namespace: a cluster-wide query can span hundreds of
// namespaces, and each uncached check is a SubjectAccessReview.
func TestRecentChangeReadFilterUsesClusterWideGrant(t *testing.T) {
	ctx := withClusterAdmin(t, "cluster-deploy-reader")
	getPermCache().Get("cluster-deploy-reader", nil).SetCanI("list", "apps", "deployments", "", true)

	allow := recentChangeReadFilter(ctx)
	for i := range 50 {
		// No per-namespace grant is seeded; with no client, a per-namespace
		// check would deny.
		if !allow(issuesapi.RecentChange{Kind: "Deployment", APIVersion: "apps/v1", Namespace: fmt.Sprintf("ns-%d", i)}) {
			t.Fatalf("cluster-wide Deployment reader denied in ns-%d", i)
		}
	}
	if allow(issuesapi.RecentChange{Kind: "ConfigMap", APIVersion: "v1", Namespace: "ns-0"}) {
		t.Fatal("a kind with no grant must stay denied")
	}
}

// A referenced ConfigMap the caller can't read must not be queried: its rows
// and its fetch saturation would otherwise tell the caller about its activity.
func TestAttachIssueChangeCorrelation_SkipsUnreadableConfigMapSource(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "web",
			EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "web-config"},
			}}},
		}}}}},
	})); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
	store := initCorrelationStore(t)
	now := time.Now()
	for i := range meaningfulchanges.NamedFetchBound + 1 {
		appendInformerEvent(t, store, timeline.TimelineEvent{
			ID: fmt.Sprintf("cm-%d", i), Timestamp: now.Add(-time.Duration(30*60-i) * time.Second),
			Kind: "ConfigMap", APIVersion: "v1", Namespace: "shop", Name: "web-config",
			EventType: timeline.EventTypeUpdate,
			Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)"}}},
		})
	}
	appendInformerEvent(t, store, timeline.TimelineEvent{
		ID: "image-edit", Timestamp: now.Add(-time.Minute),
		Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.template.spec.containers[web].image", OldValue: "web:1", NewValue: "web:2"}}},
	})
	ctx := withClusterAdmin(t, "deploy-only")
	getPermCache().Get("deploy-only", nil).SetCanI("list", "apps", "deployments", "shop", true)

	resp := issues.ListResponse{Issues: []issuesapi.Issue{criticalIssue("Deployment", "web")}}
	attachIssueChangeCorrelation(ctx, &resp)
	web := resp.Issues[0]
	if len(web.CorrelatedChanges) != 1 || web.CorrelatedChanges[0].Kind != "Deployment" {
		t.Fatalf("correlated = %+v, want only the readable Deployment edit", web.CorrelatedChanges)
	}
	if web.CorrelatedChangesIncomplete {
		t.Fatal("an unreadable ConfigMap's saturation leaked through correlated_changes_incomplete")
	}
}
