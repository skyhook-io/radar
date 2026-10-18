package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func appendChange(t *testing.T, store timeline.EventStore, e timeline.TimelineEvent) {
	t.Helper()
	e.Source, e.ClusterContext = timeline.SourceInformer, k8s.ActiveClusterContext()
	if e.ID == "" {
		e.ID = e.Kind + "/" + e.Name + "/" + string(e.EventType)
	}
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf("append %s: %v", e.ID, err)
	}
}

func admissionIssue(category issuesapi.Category, kind, name, message string, firstSeen time.Time) issuesapi.Issue {
	return issuesapi.Issue{
		Severity: issuesapi.SeverityCritical, Category: category,
		Kind: kind, Group: "apps", Namespace: "shop", Name: name,
		Message:   "pod creation blocked: " + message,
		FirstSeen: firstSeen,
		// What the owner-condition fold stamps on a young Deployment.
		IssueTiming: "started_at_resource_creation", IssueTimingBasis: "owner_condition",
		TimingSummary: "Its owner workload never became healthy after deployment.",
	}
}

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// initAdmissionCache starts the typed cache, so ResourceQuota and LimitRange
// changes count as collected.
func initAdmissionCache(t *testing.T) {
	t.Helper()
	if err := k8s.InitTestResourceCache(k8sfake.NewSimpleClientset()); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
}

// The bench case: the Deployment is created, a ResourceQuota lands after it,
// and the recreated ReplicaSet's pods are rejected. The quota is linked first,
// named as the last change, and the at-creation timing is withdrawn.
func TestAdmissionBlocker_QuotaAddedAfterWorkload(t *testing.T) {
	initAdmissionCache(t)
	store := initCorrelationStore(t)
	now := time.Now().Truncate(time.Second)
	deployAt, quotaAt := now.Add(-20*time.Minute), now.Add(-10*time.Minute)
	appendChange(t, store, timeline.TimelineEvent{Timestamp: deployAt, Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "search", EventType: timeline.EventTypeAdd})
	appendChange(t, store, timeline.TimelineEvent{Timestamp: quotaAt, Kind: "ResourceQuota", APIVersion: "v1", Namespace: "shop", Name: "memory-limit-quota", EventType: timeline.EventTypeAdd})
	// Another namespace's quota of the same name is not this one.
	appendChange(t, store, timeline.TimelineEvent{Timestamp: now.Add(-time.Minute), Kind: "ResourceQuota", APIVersion: "v1", Namespace: "other", Name: "memory-limit-quota", EventType: timeline.EventTypeAdd})

	resp := issues.ListResponse{Issues: []issuesapi.Issue{admissionIssue(issuesapi.CategoryQuotaExceeded, "Deployment", "search",
		`pods "search-7d996cf85b-j2wkp" is forbidden: failed quota: memory-limit-quota: must specify memory for: hotel-reserv-search`,
		quotaAt.Add(time.Second))}}
	attachIssueChangeCorrelation(context.Background(), &resp)
	iss := resp.Issues[0]

	if len(iss.CorrelatedChanges) != 2 {
		t.Fatalf("want quota + deployment refs, got %+v", iss.CorrelatedChanges)
	}
	quota := iss.CorrelatedChanges[0]
	if quota.Kind != "ResourceQuota" || quota.Name != "memory-limit-quota" || quota.Namespace != "shop" || quota.RankReason != admissionBlockerRankReason {
		t.Fatalf("quota must lead the refs: %+v", quota)
	}
	if iss.CorrelatedChanges[1].Kind != "Deployment" {
		t.Fatalf("workload ref missing: %+v", iss.CorrelatedChanges)
	}
	want := "ResourceQuota memory-limit-quota was created at " + utc(quotaAt) + ", after Deployment search was created at " + utc(deployAt) + ": the ResourceQuota changed last."
	if iss.TimingSummary != want {
		t.Fatalf("timing_summary:\n got %q\nwant %q", iss.TimingSummary, want)
	}
	if iss.IssueTiming != "" || iss.IssueTimingBasis != "" {
		t.Fatalf("at-creation timing must be withdrawn once the quota is the newer change: %q/%q", iss.IssueTiming, iss.IssueTimingBasis)
	}
	if iss.NoRecentChanges != nil {
		t.Fatalf("both markers set: %+v", iss)
	}
}

// The quota was there first and the workload scaled into it: the workload is
// the newer change, and the issue's timing is left alone.
func TestAdmissionBlocker_WorkloadScaledIntoStandingQuota(t *testing.T) {
	initAdmissionCache(t)
	store := initCorrelationStore(t)
	now := time.Now().Truncate(time.Second)
	quotaAt, scaleAt := now.Add(-50*time.Minute), now.Add(-5*time.Minute)
	appendChange(t, store, timeline.TimelineEvent{Timestamp: quotaAt, Kind: "ResourceQuota", APIVersion: "v1", Namespace: "shop", Name: "compute", EventType: timeline.EventTypeAdd})
	appendChange(t, store, timeline.TimelineEvent{Timestamp: scaleAt, Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web", EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.replicas", OldValue: int64(3), NewValue: int64(6)}}, Summary: "replicas 3→6"}})

	in := admissionIssue(issuesapi.CategoryQuotaExceeded, "Deployment", "web",
		`pods "web-5c7f9-abcde" is forbidden: exceeded quota: compute, requested: pods=1, used: pods=3, limited: pods=3`, scaleAt.Add(2*time.Second))
	in.IssueTiming, in.IssueTimingBasis, in.TimingSummary = "started_after_resource_was_healthy", "owner_condition", ""
	resp := issues.ListResponse{Issues: []issuesapi.Issue{in}}
	attachIssueChangeCorrelation(context.Background(), &resp)
	iss := resp.Issues[0]

	if len(iss.CorrelatedChanges) != 2 || iss.CorrelatedChanges[0].Kind != "ResourceQuota" || iss.CorrelatedChanges[1].Kind != "Deployment" {
		t.Fatalf("want quota + deployment refs, got %+v", iss.CorrelatedChanges)
	}
	want := "Deployment web was updated at " + utc(scaleAt) + ", after ResourceQuota compute was created at " + utc(quotaAt) + ": the workload changed last."
	if iss.TimingSummary != want {
		t.Fatalf("timing_summary:\n got %q\nwant %q", iss.TimingSummary, want)
	}
	if iss.IssueTiming != "started_after_resource_was_healthy" {
		t.Fatalf("timing must be kept when the workload changed last: %q", iss.IssueTiming)
	}
}

// A standing quota with no change in the window is stated as such when the
// workload changed, so "the workload changed last" rests on both sides.
func TestAdmissionBlocker_UnchangedQuotaStatedWhenWorkloadChanged(t *testing.T) {
	initAdmissionCache(t)
	store := initCorrelationStore(t)
	now := time.Now().Truncate(time.Second)
	scaleAt := now.Add(-5 * time.Minute)
	appendChange(t, store, timeline.TimelineEvent{Timestamp: scaleAt, Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web", EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.replicas", OldValue: int64(3), NewValue: int64(6)}}}})

	resp := issues.ListResponse{Issues: []issuesapi.Issue{admissionIssue(issuesapi.CategoryQuotaExceeded, "Deployment", "web",
		`pods "web-5c7f9-abcde" is forbidden: exceeded quota: compute, requested: pods=1, used: pods=3, limited: pods=3`, scaleAt)}}
	attachIssueChangeCorrelation(context.Background(), &resp)
	iss := resp.Issues[0]
	want := "Deployment web was updated at " + utc(scaleAt) + "; ResourceQuota compute has no recorded change in the last hour: the workload changed last."
	if !strings.HasSuffix(iss.TimingSummary, want) {
		t.Fatalf("timing_summary:\n got %q\nwant suffix %q", iss.TimingSummary, want)
	}
	if len(iss.CorrelatedChanges) != 1 || iss.CorrelatedChanges[0].Kind != "Deployment" {
		t.Fatalf("refs=%+v", iss.CorrelatedChanges)
	}
}

// LimitRange rejections never name the LimitRange, so every LimitRange in the
// namespace is the policy side.
func TestAdmissionBlocker_LimitRangeAddedAfterWorkload(t *testing.T) {
	initAdmissionCache(t)
	store := initCorrelationStore(t)
	now := time.Now().Truncate(time.Second)
	limitAt := now.Add(-3 * time.Minute)
	appendChange(t, store, timeline.TimelineEvent{Timestamp: limitAt, Kind: "LimitRange", APIVersion: "v1", Namespace: "shop", Name: "container-limits", EventType: timeline.EventTypeAdd})

	resp := issues.ListResponse{Issues: []issuesapi.Issue{admissionIssue(issuesapi.CategoryQuotaExceeded, "Deployment", "api",
		`pods "api-6d8b-xyz12" is forbidden: maximum memory usage per Container is 512Mi, but limit is 1Gi`, limitAt.Add(10*time.Second))}}
	attachIssueChangeCorrelation(context.Background(), &resp)
	iss := resp.Issues[0]

	if len(iss.CorrelatedChanges) != 1 || iss.CorrelatedChanges[0].Kind != "LimitRange" || iss.CorrelatedChanges[0].Name != "container-limits" ||
		iss.CorrelatedChanges[0].RankReason != admissionLimitRangeRankReason {
		t.Fatalf("LimitRange not linked: %+v", iss.CorrelatedChanges)
	}
	want := "LimitRange container-limits was created at " + utc(limitAt) + "; Deployment api has no recorded change in the last hour: the LimitRange changed last."
	if iss.TimingSummary != want {
		t.Fatalf("timing_summary:\n got %q\nwant %q", iss.TimingSummary, want)
	}
	if iss.IssueTiming != "" {
		t.Fatalf("at-creation timing kept: %q", iss.IssueTiming)
	}
}

// A webhook denial quotes webhooks[].name; the configuration declaring it is
// resolved from the cache and its change linked. A Job subject (untracked by
// the change feed) still gets the policy side.
func TestAdmissionBlocker_WebhookDenial(t *testing.T) {
	store := initCorrelationStore(t)
	vwcGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}
	mwcGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	config := &admissionv1.ValidatingWebhookConfiguration{
		TypeMeta:   metav1.TypeMeta{Kind: "ValidatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1"},
		ObjectMeta: metav1.ObjectMeta{Name: "pod-policy"},
		Webhooks:   []admissionv1.ValidatingWebhook{{Name: "deny-root.policy.example.com"}},
	}
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(config)
	if err != nil {
		t.Fatal(err)
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{vwcGVR: "ValidatingWebhookConfigurationList", mwcGVR: "MutatingWebhookConfigurationList"},
		&unstructured.Unstructured{Object: data})
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{
		{Group: vwcGVR.Group, Version: "v1", Kind: "ValidatingWebhookConfiguration", Name: vwcGVR.Resource, Verbs: []string{"list", "watch"}},
		{Group: mwcGVR.Group, Version: "v1", Kind: "MutatingWebhookConfiguration", Name: mwcGVR.Resource, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{vwcGVR, mwcGVR} {
		if err := cache.EnsureWatching(gvr); err != nil || !cache.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("watch %s: %v", gvr, err)
		}
	}

	now := time.Now().Truncate(time.Second)
	deployAt, webhookAt := now.Add(-40*time.Minute), now.Add(-4*time.Minute)
	appendChange(t, store, timeline.TimelineEvent{Timestamp: deployAt, Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "worker", EventType: timeline.EventTypeAdd})
	appendChange(t, store, timeline.TimelineEvent{Timestamp: webhookAt, Kind: "ValidatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1", Name: "pod-policy", EventType: timeline.EventTypeAdd})

	message := `Error creating: admission webhook "deny-root.policy.example.com" denied the request: runAsNonRoot is required`
	deploy := admissionIssue(issuesapi.CategoryAdmissionWebhookBlocking, "Deployment", "worker", message, webhookAt.Add(5*time.Second))
	job := admissionIssue(issuesapi.CategoryAdmissionWebhookBlocking, "Job", "nightly", message, webhookAt.Add(5*time.Second))
	job.Group = "batch"
	resp := issues.ListResponse{Issues: []issuesapi.Issue{deploy, job}}
	attachIssueChangeCorrelation(context.Background(), &resp)

	got := resp.Issues[0]
	if len(got.CorrelatedChanges) != 2 || got.CorrelatedChanges[0].Kind != "ValidatingWebhookConfiguration" || got.CorrelatedChanges[0].Name != "pod-policy" {
		t.Fatalf("webhook configuration not linked: %+v", got.CorrelatedChanges)
	}
	want := "ValidatingWebhookConfiguration pod-policy was created at " + utc(webhookAt) + ", after Deployment worker was created at " + utc(deployAt) + ": the ValidatingWebhookConfiguration changed last."
	if got.TimingSummary != want {
		t.Fatalf("timing_summary:\n got %q\nwant %q", got.TimingSummary, want)
	}

	gotJob := resp.Issues[1]
	if len(gotJob.CorrelatedChanges) != 1 || gotJob.CorrelatedChanges[0].Name != "pod-policy" {
		t.Fatalf("Job subject must still carry the webhook change: %+v", gotJob.CorrelatedChanges)
	}
	// The Job's own changes are not recorded, so no order is claimed.
	if gotJob.NoRecentChanges != nil || strings.Contains(gotJob.TimingSummary, "changed last") {
		t.Fatalf("untracked subject overclaimed: %+v", gotJob)
	}
}

// Per-kind RBAC applies to the policy side as it does to the workload side: a
// caller who can't read ResourceQuotas gets no quota ref, and no sentence that
// rests on the quota's history.
func TestAdmissionBlocker_RespectsPerKindRBAC(t *testing.T) {
	initAdmissionCache(t)
	store := initCorrelationStore(t)
	now := time.Now().Truncate(time.Second)
	appendChange(t, store, timeline.TimelineEvent{Timestamp: now.Add(-20 * time.Minute), Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "search", EventType: timeline.EventTypeAdd})
	appendChange(t, store, timeline.TimelineEvent{Timestamp: now.Add(-10 * time.Minute), Kind: "ResourceQuota", APIVersion: "v1", Namespace: "shop", Name: "memory-limit-quota", EventType: timeline.EventTypeAdd})

	ctx := withClusterAdmin(t, "deploy-reader")
	getPermCache().Get("deploy-reader", nil).SetCanI("list", "apps", "deployments", "shop", true)
	t.Cleanup(getPermCache().Invalidate)

	resp := issues.ListResponse{Issues: []issuesapi.Issue{admissionIssue(issuesapi.CategoryQuotaExceeded, "Deployment", "search",
		`pods "search-x" is forbidden: failed quota: memory-limit-quota: must specify memory for: search`, now.Add(-9*time.Minute))}}
	attachIssueChangeCorrelation(ctx, &resp)
	iss := resp.Issues[0]
	for _, c := range iss.CorrelatedChanges {
		if c.Kind == "ResourceQuota" {
			t.Fatalf("unreadable ResourceQuota leaked: %+v", iss.CorrelatedChanges)
		}
	}
	if strings.Contains(iss.TimingSummary, "ResourceQuota") || strings.Contains(iss.TimingSummary, "changed last") {
		t.Fatalf("summary rests on hidden history: %q", iss.TimingSummary)
	}
	if iss.IssueTiming != "started_at_resource_creation" {
		t.Fatalf("timing changed on hidden evidence: %q", iss.IssueTiming)
	}
}

// Orders that the record can't establish are not claimed: changes in the same
// second, and a quota kind Radar doesn't collect (an empty history means
// nothing then). A tightened quota (an update) doesn't withdraw at-creation
// timing, since an older version may have blocked the workload from the start.
func TestAdmissionBlocker_OnlyClaimsRecordedOrder(t *testing.T) {
	quotaMsg := `pods "web-x" is forbidden: exceeded quota: compute, requested: pods=1, used: pods=3, limited: pods=3`
	now := time.Now().Truncate(time.Second)

	t.Run("same second", func(t *testing.T) {
		initAdmissionCache(t)
		store := initCorrelationStore(t)
		at := now.Add(-5 * time.Minute)
		appendChange(t, store, timeline.TimelineEvent{Timestamp: at, Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web", EventType: timeline.EventTypeAdd})
		appendChange(t, store, timeline.TimelineEvent{Timestamp: at, Kind: "ResourceQuota", APIVersion: "v1", Namespace: "shop", Name: "compute", EventType: timeline.EventTypeAdd})
		resp := issues.ListResponse{Issues: []issuesapi.Issue{admissionIssue(issuesapi.CategoryQuotaExceeded, "Deployment", "web", quotaMsg, at)}}
		attachIssueChangeCorrelation(context.Background(), &resp)
		if iss := resp.Issues[0]; len(iss.CorrelatedChanges) != 2 || strings.Contains(iss.TimingSummary, "changed last") || iss.IssueTiming == "" {
			t.Fatalf("same-second changes ordered: %+v", iss)
		}
	})

	t.Run("quota not collected", func(t *testing.T) {
		store := initCorrelationStore(t) // no typed cache: no ResourceQuota informer
		appendChange(t, store, timeline.TimelineEvent{Timestamp: now.Add(-5 * time.Minute), Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web", EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.replicas", OldValue: int64(3), NewValue: int64(6)}}}})
		resp := issues.ListResponse{Issues: []issuesapi.Issue{admissionIssue(issuesapi.CategoryQuotaExceeded, "Deployment", "web", quotaMsg, now.Add(-4*time.Minute))}}
		attachIssueChangeCorrelation(context.Background(), &resp)
		if iss := resp.Issues[0]; strings.Contains(iss.TimingSummary, "no recorded change") || strings.Contains(iss.TimingSummary, "changed last") {
			t.Fatalf("claimed an uncollected quota unchanged: %q", iss.TimingSummary)
		}
	})

	t.Run("configmap edit is not a workload change", func(t *testing.T) {
		initAdmissionCache(t)
		store := initCorrelationStore(t)
		quotaAt := now.Add(-10 * time.Minute)
		appendChange(t, store, timeline.TimelineEvent{Timestamp: now.Add(-20 * time.Minute), Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web", EventType: timeline.EventTypeAdd})
		appendChange(t, store, timeline.TimelineEvent{Timestamp: quotaAt, Kind: "ResourceQuota", APIVersion: "v1", Namespace: "shop", Name: "compute", EventType: timeline.EventTypeAdd})
		cm := []issuesapi.RecentChange{{Kind: "ConfigMap", Namespace: "shop", Name: "web-config", ChangeType: "update", Timestamp: now.Add(-2 * time.Minute).Format(time.RFC3339)}}
		ev := admissionBlockerForIssue(context.Background(), &issuesapi.Issue{Category: issuesapi.CategoryQuotaExceeded, Kind: "Deployment", Namespace: "shop", Name: "web", Message: quotaMsg}, time.Hour)
		subject := append(cm, issuesapi.RecentChange{Kind: "Deployment", Namespace: "shop", Name: "web", ChangeType: "add", Timestamp: now.Add(-20 * time.Minute).Format(time.RFC3339)})
		sentence, policyLast := admissionChangeOrder(&issuesapi.Issue{Kind: "Deployment", Name: "web"}, ev, subject, true, time.Hour)
		if !policyLast || !strings.HasSuffix(sentence, "the ResourceQuota changed last.") {
			t.Fatalf("a ConfigMap edit decided the order: %q", sentence)
		}
	})

	t.Run("created then patched", func(t *testing.T) {
		// A webhook or quota patched right after creation (cert-manager's CA
		// injection) was still created after the workload.
		initAdmissionCache(t)
		store := initCorrelationStore(t)
		createdAt := now.Add(-10 * time.Minute)
		appendChange(t, store, timeline.TimelineEvent{Timestamp: now.Add(-20 * time.Minute), Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web", EventType: timeline.EventTypeAdd})
		appendChange(t, store, timeline.TimelineEvent{Timestamp: createdAt, Kind: "ResourceQuota", APIVersion: "v1", Namespace: "shop", Name: "compute", EventType: timeline.EventTypeAdd})
		appendChange(t, store, timeline.TimelineEvent{Timestamp: createdAt.Add(5 * time.Second), Kind: "ResourceQuota", APIVersion: "v1", Namespace: "shop", Name: "compute", EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.hard.pods", OldValue: "4", NewValue: "3"}}}})
		resp := issues.ListResponse{Issues: []issuesapi.Issue{admissionIssue(issuesapi.CategoryQuotaExceeded, "Deployment", "web", quotaMsg, createdAt.Add(10*time.Second))}}
		attachIssueChangeCorrelation(context.Background(), &resp)
		if iss := resp.Issues[0]; iss.IssueTiming != "" || !strings.HasSuffix(iss.TimingSummary, "the ResourceQuota changed last.") {
			t.Fatalf("creation hidden by a later patch: timing=%q summary=%q", iss.IssueTiming, iss.TimingSummary)
		}
	})

	t.Run("quota tightened", func(t *testing.T) {
		initAdmissionCache(t)
		store := initCorrelationStore(t)
		updateAt := now.Add(-3 * time.Minute)
		appendChange(t, store, timeline.TimelineEvent{Timestamp: now.Add(-20 * time.Minute), Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web", EventType: timeline.EventTypeAdd})
		appendChange(t, store, timeline.TimelineEvent{Timestamp: updateAt, Kind: "ResourceQuota", APIVersion: "v1", Namespace: "shop", Name: "compute", EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.hard.pods", OldValue: "4", NewValue: "3"}}}})
		resp := issues.ListResponse{Issues: []issuesapi.Issue{admissionIssue(issuesapi.CategoryQuotaExceeded, "Deployment", "web", quotaMsg, updateAt.Add(time.Second))}}
		attachIssueChangeCorrelation(context.Background(), &resp)
		iss := resp.Issues[0]
		if !strings.Contains(iss.TimingSummary, "ResourceQuota compute was updated at "+utc(updateAt)) || !strings.HasSuffix(iss.TimingSummary, "the ResourceQuota changed last.") {
			t.Fatalf("timing_summary=%q", iss.TimingSummary)
		}
		if iss.IssueTiming != "started_at_resource_creation" {
			t.Fatalf("an update withdrew at-creation timing: %q", iss.IssueTiming)
		}
	})
}
