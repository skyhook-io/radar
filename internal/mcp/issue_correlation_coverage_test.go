package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// The tool says "no recent changes" only when Radar can back it up, as the
// Issues page does. When the subject's own reads found nothing but a source
// or the admission policy side can't be vouched for, the issue carries no
// marker and correlation.unknown says why.

// correlationAnswer is what the tool says about one issue's changes.
func correlationAnswer(t *testing.T, iss issuesapi.Issue) string {
	t.Helper()
	data, err := json.Marshal(struct {
		NoRecentChanges   *issuesapi.NoRecentChangesMarker `json:"no_recent_changes,omitempty"`
		CorrelatedChanges []issuesapi.RecentChange         `json:"correlated_changes,omitempty"`
		Correlation       *issuesapi.ChangeCorrelation     `json:"correlation,omitempty"`
		TimingSummary     string                           `json:"timing_summary,omitempty"`
	}{iss.NoRecentChanges, iss.CorrelatedChanges, iss.Correlation, iss.TimingSummary})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func correlateOne(t *testing.T, iss issuesapi.Issue) issuesapi.Issue {
	t.Helper()
	resp := issues.ListResponse{Issues: []issuesapi.Issue{iss}}
	attachIssueChangeCorrelation(context.Background(), &resp)
	return resp.Issues[0]
}

func assertAnswer(t *testing.T, iss issuesapi.Issue, want string) {
	t.Helper()
	if got := correlationAnswer(t, iss); got != want {
		t.Fatalf("answer:\n got %s\nwant %s", got, want)
	}
}

// A workload missing from Radar's cache has unknown ConfigMaps and Secrets.
func TestAttachIssueChangeCorrelation_WorkloadNotCachedIsUnknown(t *testing.T) {
	initCorrelationStore(t)
	iss := criticalIssue("Deployment", "ghost")
	iss.Group = "apps"
	assertAnswer(t, correlateOne(t, iss),
		`{"correlation":{"unknown":{"reason":"sources_unresolved","kinds":[{"group":"apps","kind":"Deployment"}]}}}`)
}

// A referenced source whose kind Radar doesn't collect may hold the change.
func TestAttachIssueChangeCorrelation_SourceKindNotCollectedIsUnknown(t *testing.T) {
	client := k8sfake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "flagd", Namespace: "shop"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "flagd", EnvFrom: []corev1.EnvFromSource{{
				ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "flagd-config"}},
			}}}},
		}}},
	})
	// Radar collects Deployments only.
	if err := k8s.InitScopedTestResourceCache(client, map[string]k8score.ResourceScope{k8score.Deployments: {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	initCorrelationStore(t)
	iss := criticalIssue("Deployment", "flagd")
	iss.Group = "apps"
	assertAnswer(t, correlateOne(t, iss),
		`{"correlation":{"unknown":{"reason":"radar_kind_not_collected","kinds":[{"kind":"ConfigMap"}]}}}`)
}

// An admission policy Radar can't identify, or doesn't collect, may be the
// side that changed.
func TestAttachIssueChangeCorrelation_AdmissionPolicyUnknown(t *testing.T) {
	quotaMsg := `pods "web-5c7f9-abcde" is forbidden: exceeded quota: compute, requested: pods=1, used: pods=3, limited: pods=3`
	firstSeen := time.Now().Add(-4 * time.Minute)

	t.Run("no policy named", func(t *testing.T) {
		initAdmissionCache(t)
		initCorrelationStore(t)
		iss := admissionIssue(issuesapi.CategoryQuotaExceeded, "Deployment", "web", "Error creating: forbidden by an admission policy", firstSeen)
		assertAnswer(t, correlateOne(t, iss),
			`{"correlation":{"unknown":{"reason":"sources_unresolved"}},"timing_summary":"Its owner workload never became healthy after deployment."}`)
	})
	t.Run("named quota not in the cache", func(t *testing.T) {
		initAdmissionCache(t)
		initCorrelationStore(t)
		iss := admissionIssue(issuesapi.CategoryQuotaExceeded, "Deployment", "web", quotaMsg, firstSeen)
		assertAnswer(t, correlateOne(t, iss),
			`{"correlation":{"unknown":{"reason":"sources_unresolved","kinds":[{"kind":"ResourceQuota"}]}},"timing_summary":"Its owner workload never became healthy after deployment."}`)
	})
	t.Run("quota kind not collected", func(t *testing.T) {
		if err := k8s.InitScopedTestResourceCache(k8sfake.NewSimpleClientset(shopDeployments("web")...), map[string]k8score.ResourceScope{k8score.Deployments: {Enabled: true}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k8s.ResetTestState)
		initCorrelationStore(t)
		iss := admissionIssue(issuesapi.CategoryQuotaExceeded, "Deployment", "web", quotaMsg, firstSeen)
		assertAnswer(t, correlateOne(t, iss),
			`{"correlation":{"unknown":{"reason":"radar_kind_not_collected","kinds":[{"kind":"ResourceQuota"}]}},"timing_summary":"Its owner workload never became healthy after deployment."}`)
	})
}

// "The ResourceQuota changed last" rests on the workload having no newer
// change, which Radar can't tell for a workload missing from its cache: the
// quota change is listed, with no order sentence and the at-creation timing
// kept.
func TestAttachIssueChangeCorrelation_NoOrderForUncachedWorkload(t *testing.T) {
	initAdmissionCache(t) // holds no Deployment "billing"
	store := initCorrelationStore(t)
	now := time.Now().Truncate(time.Second)
	quotaAt := now.Add(-10 * time.Minute)
	setPolicyCreatedAt(t, quotaAt)
	appendChange(t, store, timeline.TimelineEvent{Timestamp: quotaAt, Kind: "ResourceQuota", APIVersion: "v1", Namespace: "shop", Name: "compute", EventType: timeline.EventTypeAdd})
	iss := admissionIssue(issuesapi.CategoryQuotaExceeded, "Deployment", "billing",
		`pods "billing-5c7f9-abcde" is forbidden: exceeded quota: compute, requested: pods=1, used: pods=3, limited: pods=3`, quotaAt.Add(time.Second))
	got := correlateOne(t, iss)
	if len(got.CorrelatedChanges) != 1 || got.CorrelatedChanges[0].Kind != "ResourceQuota" {
		t.Fatalf("the quota change must still be listed: %+v", got.CorrelatedChanges)
	}
	if got.TimingSummary != "Its owner workload never became healthy after deployment." || got.IssueTiming != "started_at_resource_creation" {
		t.Fatalf("timing must be untouched: timing=%q summary=%q", got.IssueTiming, got.TimingSummary)
	}
	if got.NoRecentChanges != nil || got.Correlation != nil {
		t.Fatalf("an issue with listed changes carries neither the marker nor a reason: %+v", got)
	}
}

// A kind Radar started watching inside the window holds no record of the
// changes before that, so "no recent changes" can't be claimed. The reason
// carries the history and the point it is complete from, for a caller limited
// to some namespaces too: a late watch dates no one's activity.
func TestAttachIssueChangeCorrelation_KindWatchedLateIsUnknown(t *testing.T) {
	watchStart := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Second)
	t.Cleanup(k8s.SetTestWatchClock(func() time.Time { return watchStart }))
	if err := k8s.InitTestResourceCache(k8sfake.NewSimpleClientset(shopDeployments("web")...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	initCorrelationStore(t)
	t.Cleanup(getPermCache().Invalidate)
	iss := criticalIssue("Deployment", "web")
	iss.Group = "apps"

	answer := func(ctx context.Context) issuesapi.Issue {
		resp := issues.ListResponse{Issues: []issuesapi.Issue{iss}}
		attachIssueChangeCorrelation(ctx, &resp)
		return resp.Issues[0]
	}
	since := watchStart.Format(time.RFC3339)
	assertAnswer(t, answer(context.Background()),
		`{"correlation":{"unknown":{"reason":"history_incomplete"},"history":{"incomplete":true,"complete_since":"`+since+`","reasons":["observation_started_late"]}}}`)

	restricted := withRestrictedUser(t, "shop-reader", []string{"shop"})
	perms := getPermCache().Get("shop-reader", nil)
	for _, resource := range []string{"deployments", "replicasets", "pods"} {
		group := "apps"
		if resource == "pods" {
			group = ""
		}
		perms.SetCanI("list", group, resource, "shop", true)
		perms.SetCanI("get", group, resource, "shop", true)
	}
	assertAnswer(t, answer(restricted),
		`{"correlation":{"unknown":{"reason":"history_incomplete"},"history":{"incomplete":true,"complete_since":"`+since+`","reasons":["observation_started_late"]}}}`)
}

// "The ResourceQuota changed last" rests on the workload's history covering
// the window. With the Deployment kind watched only since after the window
// began, the quota change is listed and no order is stated.
func TestAttachIssueChangeCorrelation_NoOrderOverLateWatch(t *testing.T) {
	t.Cleanup(k8s.SetTestWatchClock(func() time.Time { return time.Now().Add(-15 * time.Minute) }))
	initAdmissionCache(t)
	store := initCorrelationStore(t)
	now := time.Now().Truncate(time.Second)
	quotaAt := now.Add(-10 * time.Minute)
	setPolicyCreatedAt(t, quotaAt)
	appendChange(t, store, timeline.TimelineEvent{Timestamp: quotaAt, Kind: "ResourceQuota", APIVersion: "v1", Namespace: "shop", Name: "compute", EventType: timeline.EventTypeAdd})
	iss := admissionIssue(issuesapi.CategoryQuotaExceeded, "Deployment", "web",
		`pods "web-5c7f9-abcde" is forbidden: exceeded quota: compute, requested: pods=1, used: pods=3, limited: pods=3`, quotaAt.Add(time.Second))
	got := correlateOne(t, iss)
	if len(got.CorrelatedChanges) != 1 || got.CorrelatedChanges[0].Kind != "ResourceQuota" {
		t.Fatalf("the quota change must still be listed: %+v", got.CorrelatedChanges)
	}
	if strings.Contains(got.TimingSummary, "changed last") || got.IssueTiming != "started_at_resource_creation" {
		t.Fatalf("no order over a late watch: timing=%q summary=%q", got.IssueTiming, got.TimingSummary)
	}
}
