package meaningfulchanges

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestShouldAttachIssueChanges(t *testing.T) {
	if !ShouldAttachIssueChanges(nil) {
		t.Fatalf("zero critical issues should allow the recent-changes attachment")
	}
	if got, want := IssueChangesReason(nil), "no_critical_issues"; got != want {
		t.Fatalf("IssueChangesReason(nil) = %q, want %q", got, want)
	}
	baseline := []issuesapi.Issue{{Severity: issuesapi.SeverityCritical, IssueTiming: "started_at_resource_creation"}}
	if !ShouldAttachIssueChanges(baseline) {
		t.Fatalf("creation-time critical issues should allow the recent-changes attachment")
	}
	if got, want := IssueChangesReason(baseline), "recent_changes_with_all_critical_issues_at_creation"; got != want {
		t.Fatalf("IssueChangesReason(baseline) = %q, want %q", got, want)
	}
	runtime := []issuesapi.Issue{{Severity: issuesapi.SeverityCritical, IssueTiming: "started_after_resource_was_healthy"}}
	if ShouldAttachIssueChanges(runtime) {
		t.Fatalf("runtime critical issue should suppress the recent-changes attachment")
	}
	if got := IssueChangesReason(runtime); got != "" {
		t.Fatalf("IssueChangesReason(runtime) = %q, want empty", got)
	}
	mixed := append(append([]issuesapi.Issue{}, baseline...), runtime...)
	if got := IssueChangesReason(mixed); got != "" {
		t.Fatalf("IssueChangesReason(mixed) = %q, want empty", got)
	}
	unknown := []issuesapi.Issue{{Severity: issuesapi.SeverityCritical}}
	if ShouldAttachIssueChanges(unknown) {
		t.Fatalf("critical issue with unknown timing should suppress the recent-changes attachment")
	}
	if got := IssueChangesReason(unknown); got != "" {
		t.Fatalf("IssueChangesReason(unknown) = %q, want empty", got)
	}
}

// The creation-timing collision is the exact spot where the old dismissive
// token steered agents away from the change feed; the guidance must ride that
// reason and no other, and must steer without claiming causation.
func TestIssueChangesGuidance(t *testing.T) {
	guidance := IssueChangesGuidance(ChangesReasonWithAllCreationTimeCriticalIssues)
	if !strings.Contains(guidance, "does not clear the listed changes") || !strings.Contains(guidance, "Review the changes") {
		t.Fatalf("collision-reason guidance must steer toward the feed, got %q", guidance)
	}
	if strings.Contains(strings.ToLower(guidance), "caused the") {
		t.Fatalf("guidance must not claim causation, got %q", guidance)
	}
	// The timing classification is per-resource ("failing since creation"); it
	// establishes no ordering against the change feed, so the prose must never
	// claim the issues predate the changes — a resource created after a bad
	// change fails from creation because of that change.
	if strings.Contains(strings.ToLower(guidance), "predate") {
		t.Fatalf("guidance must not claim an issue-vs-change ordering, got %q", guidance)
	}
	if got := IssueChangesGuidance(ChangesReasonNoCriticalIssues); got != "" {
		t.Fatalf("no_critical_issues carries no landmine and needs no guidance, got %q", got)
	}
	if got := IssueChangesGuidance(""); got != "" {
		t.Fatalf("empty reason must yield empty guidance, got %q", got)
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

// A webhook config UPDATE must reach the feed classified as spec_config. The
// differ emits top-level webhooks[...] paths (webhook configs have no spec.),
// so without isWebhookConfigKind in hasSpecConfigField, classify would return
// "" and rankedChanges would drop the change — silently no-op'ing the feature
// for its main case. Cluster-scoped (namespace==""), so query cluster-wide.
func TestRecentSurfacesWebhookConfigUpdate(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	store := timeline.GetStore()
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID:             "wh-update",
		Timestamp:      time.Now(),
		Source:         timeline.SourceInformer,
		ClusterContext: k8s.ActiveClusterContext(),
		Kind:           "MutatingWebhookConfiguration",
		Name:           "pod-policy",
		EventType:      timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{
			Fields:  []timeline.FieldChange{{Path: "webhooks[pod-policy.k8s.io]", OldValue: "failurePolicy=Ignore", NewValue: "failurePolicy=Fail"}},
			Summary: "webhook pod-policy.k8s.io changed",
		},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	changes, _, err := Recent(context.Background(), Query{Limit: 5})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %d, want the webhook update: %+v", len(changes), changes)
	}
	if changes[0].Kind != "MutatingWebhookConfiguration" || changes[0].ChangeCategory != issuesapi.ChangeCategorySpecConfig {
		t.Fatalf("webhook update misclassified: %+v", changes[0])
	}
}

// A Service delete followed by a burst of status-churn updates must still
// surface: lifecycle events are fetched in a query of their own, so the
// newest-N candidate window for updates cannot starve them out before
// ranking.
func TestRecentLifecycleEventSurvivesUpdateChurn(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 1000}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	store := timeline.GetStore()
	now := time.Now()

	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID:             "svc-delete",
		Timestamp:      now.Add(-10 * time.Minute),
		Source:         timeline.SourceInformer,
		ClusterContext: k8s.ActiveClusterContext(),
		Kind:           "Service",
		Namespace:      "shop",
		Name:           "user-service",
		EventType:      timeline.EventTypeDelete,
	}); err != nil {
		t.Fatalf("append delete: %v", err)
	}
	// 120 newer status-churn updates — more than the candidate window
	// (candidateLimit(20) = 80), so the delete cannot survive a single
	// newest-first fetch.
	for i := 0; i < 120; i++ {
		if err := store.Append(context.Background(), timeline.TimelineEvent{
			ID:             fmt.Sprintf("churn-%d", i),
			Timestamp:      now.Add(-time.Duration(120-i) * time.Second),
			Source:         timeline.SourceInformer,
			ClusterContext: k8s.ActiveClusterContext(),
			Kind:           "Deployment",
			Namespace:      "shop",
			Name:           fmt.Sprintf("web-%d", i%10),
			EventType:      timeline.EventTypeUpdate,
			Diff:           &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "status.readyReplicas", OldValue: int32(1), NewValue: int32(0)}}, Summary: "ready: 1→0"},
		}); err != nil {
			t.Fatalf("append churn %d: %v", i, err)
		}
	}

	changes, _, err := Recent(context.Background(), Query{Namespaces: []string{"shop"}})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	found := false
	for _, c := range changes {
		if c.Kind == "Service" && c.Name == "user-service" && c.ChangeType == "delete" {
			found = true
			if c.ChangeCategory != "lifecycle" {
				t.Fatalf("delete category = %q, want lifecycle", c.ChangeCategory)
			}
		}
	}
	if !found {
		t.Fatalf("Service delete starved out of ranked changes; got %d changes, first: %+v", len(changes), changes[0])
	}
	// The delete (lifecycle, 70) must also outrank the churn (runtime_status, 40).
	if changes[0].ChangeType != "delete" {
		t.Fatalf("first change = %+v, want the Service delete ranked first", changes[0])
	}
}

// Per-resource lookups must apply the resource name before store limits are
// applied. Otherwise a busy namespace with many newer lifecycle events for the
// same kind can hide this resource's delete and make per-issue correlation emit
// a false no_recent_changes marker.
func TestRecentForResourceLifecycleEventSurvivesLifecycleChurn(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 1000}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	store := timeline.GetStore()
	now := time.Now()

	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID:             "target-delete",
		Timestamp:      now.Add(-10 * time.Minute),
		Source:         timeline.SourceInformer,
		ClusterContext: k8s.ActiveClusterContext(),
		Kind:           "Service",
		Namespace:      "shop",
		Name:           "user-service",
		EventType:      timeline.EventTypeDelete,
	}); err != nil {
		t.Fatalf("append target delete: %v", err)
	}

	// More than both candidate caps for a named query (100) and lifecycle query
	// (50). Without a store-level name filter, the target delete is not fetched
	// and RecentForResource returns no changes.
	for i := 0; i < 120; i++ {
		if err := store.Append(context.Background(), timeline.TimelineEvent{
			ID:             fmt.Sprintf("other-delete-%d", i),
			Timestamp:      now.Add(-time.Duration(120-i) * time.Second),
			Source:         timeline.SourceInformer,
			ClusterContext: k8s.ActiveClusterContext(),
			Kind:           "Service",
			Namespace:      "shop",
			Name:           fmt.Sprintf("other-service-%d", i),
			EventType:      timeline.EventTypeDelete,
		}); err != nil {
			t.Fatalf("append lifecycle churn %d: %v", i, err)
		}
	}

	changes, saturated, err := RecentForResource(context.Background(), "Service", "shop", "user-service", time.Hour, ResourceLimit, DefaultFieldLimit)
	if err != nil {
		t.Fatalf("RecentForResource: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want exactly the target delete", changes)
	}
	if c := changes[0]; c.Kind != "Service" || c.Name != "user-service" || c.ChangeType != "delete" {
		t.Fatalf("unexpected change: %+v", c)
	}
	// The name filter applies at the store, so the churn never counts against
	// this subject's candidate caps.
	if saturated {
		t.Fatal("other resources' churn must not saturate a named lookup")
	}
}

// When the subject itself produces more events than the candidate cap, the
// fetch may have missed older changes in the window — RecentForResource must
// report saturation so callers never turn "saw nothing" into "nothing
// happened".
func TestRecentForResourceReportsSaturation(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 1000}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	store := timeline.GetStore()
	now := time.Now()

	for i := 0; i < maxCandidateLimit; i++ {
		if err := store.Append(context.Background(), timeline.TimelineEvent{
			ID:             fmt.Sprintf("self-churn-%d", i),
			Timestamp:      now.Add(-time.Duration(i) * time.Second),
			Source:         timeline.SourceInformer,
			ClusterContext: k8s.ActiveClusterContext(),
			Kind:           "Deployment",
			Namespace:      "shop",
			Name:           "web",
			EventType:      timeline.EventTypeUpdate,
			Diff:           &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "status.readyReplicas", OldValue: int32(1), NewValue: int32(0)}}, Summary: "ready: 1→0"},
		}); err != nil {
			t.Fatalf("append churn %d: %v", i, err)
		}
	}

	_, saturated, err := RecentForResource(context.Background(), "Deployment", "shop", "web", time.Hour, ResourceLimit, DefaultFieldLimit)
	if err != nil {
		t.Fatalf("RecentForResource: %v", err)
	}
	if !saturated {
		t.Fatal("a candidate fetch at its cap must report saturation")
	}
}

// A recreate (delete + ReasonRecreated add carrying the cross-recreate diff)
// must surface as exactly ONE entry — the diff-bearing add, classified
// spec_config — with the paired delete coalesced away. Entry count strictly
// reduced is the acceptance criterion.
func TestRecentCoalescesRecreatePairs(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	store := timeline.GetStore()
	now := time.Now()

	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "dep-delete", Timestamp: now.Add(-10 * time.Second),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Deployment", Namespace: "shop", Name: "profile",
		EventType: timeline.EventTypeDelete,
	}); err != nil {
		t.Fatalf("append delete: %v", err)
	}
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "dep-recreate", Timestamp: now.Add(-8 * time.Second),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Deployment", Namespace: "shop", Name: "profile",
		EventType: timeline.EventTypeAdd, Reason: timeline.ReasonRecreated,
		Diff: &timeline.DiffInfo{
			Fields:  []timeline.FieldChange{{Path: "spec.template.spec.containers(profile).command", OldValue: "[profile]", NewValue: "[geo]"}},
			Summary: "recreated with changes: command(profile) changed",
		},
	}); err != nil {
		t.Fatalf("append recreate add: %v", err)
	}
	// A true delete with no recreate must survive coalescing.
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "svc-true-delete", Timestamp: now.Add(-5 * time.Second),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Service", Namespace: "shop", Name: "user-service",
		EventType: timeline.EventTypeDelete,
	}); err != nil {
		t.Fatalf("append true delete: %v", err)
	}
	// A final delete AFTER the recreate must also survive — only the delete
	// paired with (i.e. preceding) the recreate add is folded away.
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "dep-final-delete", Timestamp: now.Add(-3 * time.Second),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Deployment", Namespace: "shop", Name: "profile",
		EventType: timeline.EventTypeDelete,
	}); err != nil {
		t.Fatalf("append final delete: %v", err)
	}

	changes, _, err := Recent(context.Background(), Query{Namespaces: []string{"shop"}})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(changes) != 3 {
		t.Fatalf("changes = %d entries %+v, want exactly 3 (recreate add + final delete + true delete)", len(changes), changes)
	}
	var recreate, finalDelete, trueDelete bool
	for _, c := range changes {
		if c.Name == "profile" && c.ChangeType == "add" {
			if c.ChangeCategory != "spec_config" {
				t.Fatalf("recreate add category = %q, want spec_config", c.ChangeCategory)
			}
			recreate = true
		}
		if c.Name == "profile" && c.ChangeType == "delete" {
			finalDelete = true
		}
		if c.Name == "user-service" && c.ChangeType == "delete" {
			trueDelete = true
		}
	}
	if !recreate || !finalDelete || !trueDelete {
		t.Fatalf("missing entries: recreate=%v finalDelete=%v trueDelete=%v in %+v", recreate, finalDelete, trueDelete, changes)
	}
}

// The static canonicalKind fallback (cold start, partial discovery) must
// cover every feed kind: a miss best-effort-capitalizes lowercase input
// ("resourcequota" → "Resourcequota") which silently matches no timeline
// events.
func TestCanonicalKindCoversFeedKinds(t *testing.T) {
	for _, kind := range append(append([]string{}, configKinds...), specKinds...) {
		if got := canonicalKind(strings.ToLower(kind)); got != kind {
			t.Errorf("canonicalKind(%q) = %q, want %q", strings.ToLower(kind), got, kind)
		}
	}
	// kubectl shortnames and plurals for the kinds most recently added.
	for in, want := range map[string]string{
		"resourcequotas": "ResourceQuota",
		"quota":          "ResourceQuota",
		"limitranges":    "LimitRange",
		"limits":         "LimitRange",
	} {
		if got := canonicalKind(in); got != want {
			t.Errorf("canonicalKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every kind the change feed tracks must participate in recreate-join —
// otherwise a delete+recreate of that kind surfaces as a contentless
// delete+add pair instead of one diff-bearing change.
func TestFeedKindsHaveRecreateJoin(t *testing.T) {
	for _, kind := range append(append([]string{}, configKinds...), specKinds...) {
		if !k8s.RecreateJoinKind(kind) {
			t.Errorf("feed kind %s missing from the recreate stash", kind)
		}
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

// Secret deletes surface (name-only); Secret adds and updates never do.
func TestRecentSecretDeleteOnly(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	store := timeline.GetStore()
	now := time.Now()

	for _, e := range []timeline.TimelineEvent{
		{ID: "sec-add", Timestamp: now.Add(-30 * time.Second), EventType: timeline.EventTypeAdd},
		{ID: "sec-del", Timestamp: now.Add(-10 * time.Second), EventType: timeline.EventTypeDelete},
	} {
		e.Source = timeline.SourceInformer
		e.ClusterContext = k8s.ActiveClusterContext()
		e.Kind, e.Namespace, e.Name = "Secret", "shop", "db-credentials"
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("append %s: %v", e.ID, err)
		}
	}

	changes, _, err := Recent(context.Background(), Query{Namespaces: []string{"shop"}})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want exactly the Secret delete", changes)
	}
	c := changes[0]
	if c.Kind != "Secret" || c.ChangeType != "delete" || c.ChangeCategory != "lifecycle" {
		t.Fatalf("unexpected entry: %+v", c)
	}
	if len(c.Fields) != 0 {
		t.Fatalf("secret delete must be name-only, got fields: %+v", c.Fields)
	}
}

// ConfigMap change entries name the workloads that directly mount/reference
// them — binding the config change to its consumer without a topology call.
// Jobs and CronJobs count: a migration/load-gen Job is often the only
// consumer of a config-script ConfigMap.
func TestRecentAnnotatesConfigMapConsumers(t *testing.T) {
	client := fake.NewSimpleClientset(
		&appsv1.Deployment{
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
		},
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "seed", Namespace: "shop"},
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    "seed",
						Image:   "seed:v1",
						EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "flagd-config"}}}},
					}},
				}},
			},
		},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)

	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
		ID: "cm-change", Timestamp: time.Now().Add(-time.Minute),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "ConfigMap", Namespace: "shop", Name: "flagd-config",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)", OldValue: []string{"LOG_LEVEL"}, NewValue: []string{"LOG_LEVEL"}}}, Summary: "modified keys: [LOG_LEVEL]"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	changes, _, err := Recent(context.Background(), Query{Namespaces: []string{"shop"}})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want 1", changes)
	}
	got := changes[0].ConsumedBy
	if len(got) != 2 || got[0] != "Deployment/flagd" || got[1] != "Job/seed" {
		t.Fatalf("consumed_by = %v, want [Deployment/flagd Job/seed]", got)
	}
	if changes[0].Salience != issuesapi.ChangeSalienceConfigEdit {
		t.Fatalf("salience = %q, want config_edit", changes[0].Salience)
	}
}

func TestRankAndCapApplicationConfigTiebreak(t *testing.T) {
	at := time.Now()
	changes := []issuesapi.RecentChange{
		recentChange("Deployment", "shop", "ad", at, "spec.template.spec.containers[ad].image"),
		recentChange("Deployment", "shop", "flagd", at.Add(-time.Second), "metadata.generation"),
		{
			Kind: "ConfigMap", Namespace: "shop", Name: "flagd-config",
			Timestamp:      at.Add(-2 * time.Second).Format(time.RFC3339),
			ChangeCategory: issuesapi.ChangeCategorySpecConfig,
			Fields:         []issuesapi.ChangeField{{Path: "data.demo.flagd.json.flags.adFailure.defaultVariant"}},
			ConsumedBy:     []string{"Deployment/flagd"},
		},
		{
			Kind: "Service", Namespace: "shop", Name: "jaeger",
			Timestamp:      at.Add(-3 * time.Second).Format(time.RFC3339),
			ChangeType:     string(timeline.EventTypeAdd),
			ChangeCategory: issuesapi.ChangeCategorySpecConfig,
			RankReason:     "recreated with desired-state or configuration changes",
			Fields:         []issuesapi.ChangeField{{Path: "spec.type"}},
		},
	}

	RankAndCap(&changes, 5)

	if got := changes[0]; got.Name != "flagd-config" || got.Salience != issuesapi.ChangeSalienceConfigEdit {
		t.Fatalf("first change = %+v, want the consumed structured ConfigMap edit", got)
	}
	if got := changes[len(changes)-1]; got.Name != "flagd" || !strings.Contains(got.RankReason, "generation") {
		t.Fatalf("last change = %+v, want generation-only below real spec changes", got)
	}
	if got := changes[len(changes)-2]; got.Name != "jaeger" || !strings.Contains(got.RankReason, "recreated") {
		t.Fatalf("jaeger recreate = %+v, want retained as real spec change", got)
	}
}

func TestBenchmarkFlagEditsBecomePrimeSuspects(t *testing.T) {
	for _, flag := range []string{"adFailure", "cartFailure"} {
		t.Run(flag, func(t *testing.T) {
			at := time.Now()
			changes := []issuesapi.RecentChange{
				recentChange("Deployment", "shop", "ad", at, "metadata.generation"),
				recentChange("Deployment", "shop", "flagd", at.Add(-time.Second), "metadata.generation"),
				{
					Kind: "ConfigMap", Namespace: "shop", Name: "flagd-config",
					Timestamp:      at.Add(-2 * time.Second).Format(time.RFC3339),
					ChangeCategory: issuesapi.ChangeCategorySpecConfig,
					Fields:         []issuesapi.ChangeField{{Path: "data.demo.flagd.json.flags." + flag + ".defaultVariant"}},
					ConsumedBy:     []string{"Deployment/flagd"},
				},
			}

			got, guidance, _ := PrioritizeIssueChanges(
				changes,
				[]issuesapi.Issue{{
					Kind: "Deployment", Namespace: "shop", Name: "product-catalog",
					Severity: issuesapi.SeverityCritical,
				}},
				ChangesReasonWithAllCreationTimeCriticalIssues,
				IssueChangesLimit,
			)

			if got[0].Name != "flagd-config" || got[0].Salience != issuesapi.ChangeSaliencePrimeSuspect {
				t.Fatalf("first change = %+v, want issue-less flag edit", got[0])
			}
			if !strings.Contains(guidance, "ConfigMap/shop/flagd-config") ||
				!strings.Contains(guidance, "not proof of cause") {
				t.Fatalf("guidance did not name and hedge the flag edit: %q", guidance)
			}
		})
	}
}

func TestApplicationConfigClassifierFalsePositiveGuards(t *testing.T) {
	tests := []struct {
		name      string
		change    issuesapi.RecentChange
		want      issuesapi.ChangeSalience
		wantScore int
	}{
		{
			name: "workload env", change: recentChange("Deployment", "shop", "frontend", time.Now(),
				"spec.template.spec.containers[frontend].env[CART_ADDR]"),
			want: issuesapi.ChangeSalienceConfigEdit, wantScore: 105,
		},
		{
			name: "init container is represented by the same producer path", change: recentChange("Deployment", "shop", "frontend", time.Now(),
				"spec.template.spec.containers[migrate].envFrom"),
			want: issuesapi.ChangeSalienceConfigEdit, wantScore: 105,
		},
		{
			name: "command", change: recentChange("StatefulSet", "shop", "db", time.Now(),
				"spec.template.spec.containers[db].command"),
			want: issuesapi.ChangeSalienceConfigEdit, wantScore: 105,
		},
		{
			name: "image", change: recentChange("Deployment", "shop", "frontend", time.Now(),
				"spec.template.spec.containers[frontend].image"),
			wantScore: 100,
		},
		{
			name: "replicas", change: recentChange("Deployment", "shop", "frontend", time.Now(),
				"spec.replicas"),
			wantScore: 100,
		},
		{
			name: "HPA target", change: recentChange("HorizontalPodAutoscaler", "shop", "frontend", time.Now(),
				"spec.maxReplicas"),
			wantScore: 100,
		},
		{
			name: "envoy substring", change: recentChange("Deployment", "shop", "frontend", time.Now(),
				"spec.template.metadata.annotations.envoy.io/config"),
			wantScore: 100,
		},
		{
			name: "unrelated CRD prose", change: recentChange("Widget", "shop", "frontend", time.Now(),
				"spec.sql.command"),
			wantScore: 100,
		},
		{
			name: "consumed ConfigMap scalar value fallback", change: issuesapi.RecentChange{
				Kind: "ConfigMap", Namespace: "shop", Name: "flags",
				ChangeCategory: issuesapi.ChangeCategorySpecConfig,
				Fields:         []issuesapi.ChangeField{{Path: "data (modified keys)"}},
				ConsumedBy:     []string{"Deployment/flagd"},
			},
			want: issuesapi.ChangeSalienceConfigEdit, wantScore: 105,
		},
		{
			name: "consumed ConfigMap key removal", change: issuesapi.RecentChange{
				Kind: "ConfigMap", Namespace: "shop", Name: "frontend-env",
				ChangeCategory: issuesapi.ChangeCategorySpecConfig,
				Fields:         []issuesapi.ChangeField{{Path: "data (removed keys)"}},
				ConsumedBy:     []string{"Deployment/frontend"},
			},
			want: issuesapi.ChangeSalienceConfigEdit, wantScore: 105,
		},
		{
			name: "ConfigMap binary data", change: issuesapi.RecentChange{
				Kind: "ConfigMap", Namespace: "shop", Name: "assets",
				ChangeCategory: issuesapi.ChangeCategorySpecConfig,
				Fields:         []issuesapi.ChangeField{{Path: "binaryData (modified keys)"}},
				ConsumedBy:     []string{"Deployment/frontend"},
			},
			wantScore: 100,
		},
		{
			name: "unconsumed ConfigMap", change: issuesapi.RecentChange{
				Kind: "ConfigMap", Namespace: "shop", Name: "flags",
				ChangeCategory: issuesapi.ChangeCategorySpecConfig,
				Fields:         []issuesapi.ChangeField{{Path: "data.flags.enabled"}},
			},
			wantScore: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changes := []issuesapi.RecentChange{tt.change}
			RankAndCap(&changes, 1)
			if got := changes[0].Salience; got != tt.want {
				t.Fatalf("salience = %q, want %q", got, tt.want)
			}
			if got := score(changes[0]); got != tt.wantScore {
				t.Fatalf("score = %d, want %d", got, tt.wantScore)
			}
		})
	}
}

func TestPrioritizeIssueChangesMarksOnlyUnexplainedEdits(t *testing.T) {
	at := time.Now()
	changes := []issuesapi.RecentChange{
		recentChange("Deployment", "shop", "frontend", at, "spec.template.spec.containers[frontend].env[CART_ADDR]"),
		{
			Kind: "ConfigMap", Namespace: "shop", Name: "flagd-config",
			Timestamp:      at.Add(-time.Second).Format(time.RFC3339),
			ChangeCategory: issuesapi.ChangeCategorySpecConfig,
			Fields:         []issuesapi.ChangeField{{Path: "data.demo.flagd.json.flags.cartFailure.defaultVariant"}},
			ConsumedBy:     []string{"Deployment/flagd"},
		},
		recentChange("Deployment", "shop", "checkout", at.Add(-2*time.Second),
			"spec.template.spec.containers[checkout].env[PAYMENT_ADDR]"),
	}
	issues := []issuesapi.Issue{
		{
			Kind: "Deployment", Namespace: "shop", Name: "frontend",
			Severity: issuesapi.SeverityCritical,
		},
		{
			Kind: "Pod", Namespace: "shop", Name: "flagd-abc",
			Owner:    issuesapi.Ref{Kind: "Deployment", Namespace: "shop", Name: "flagd"},
			Severity: issuesapi.SeverityCritical,
		},
	}

	got, guidance, capped := PrioritizeIssueChanges(
		changes, issues, ChangesReasonWithAllCreationTimeCriticalIssues, 5,
	)

	if capped {
		t.Fatal("three entries must not report a five-entry cap")
	}
	if got[0].Name != "checkout" || got[0].Salience != issuesapi.ChangeSaliencePrimeSuspect {
		t.Fatalf("first change = %+v, want sole unexplained edit", got[0])
	}
	if got[1].Name == "checkout" || got[1].Salience != issuesapi.ChangeSalienceConfigEdit {
		t.Fatalf("explained edit was promoted: %+v", got[1])
	}
	if got[2].Salience != issuesapi.ChangeSalienceConfigEdit {
		t.Fatalf("ConfigMap explained through its consumer was promoted: %+v", got[2])
	}
	if !strings.Contains(guidance, "Deployment/shop/checkout") ||
		!strings.Contains(guidance, "a lead to verify, not proof") {
		t.Fatalf("guidance did not name and hedge the sole returned suspect: %q", guidance)
	}
}

func TestIssueChangesFetchLimitOnlyWidensForIssueAwareRanking(t *testing.T) {
	if got := IssueChangesFetchLimit(ChangesReasonNoCriticalIssues); got != IssueChangesLimit {
		t.Fatalf("no-critical fetch limit = %d, want %d", got, IssueChangesLimit)
	}
	if got := IssueChangesFetchLimit(ChangesReasonWithAllCreationTimeCriticalIssues); got != IssueChangesCandidateLimit {
		t.Fatalf("issue-aware fetch limit = %d, want %d", got, IssueChangesCandidateLimit)
	}
	if got := IssueChangesFetchLimit(""); got != IssueChangesLimit {
		t.Fatalf("unknown-reason fetch limit = %d, want %d", got, IssueChangesLimit)
	}
}

func TestPrioritizeIssueChangesDoesNotMarkHealthyClusterEdits(t *testing.T) {
	changes := []issuesapi.RecentChange{
		recentChange("Deployment", "shop", "frontend", time.Now(),
			"spec.template.spec.containers[frontend].env[CART_ADDR]"),
	}

	got, guidance, _ := PrioritizeIssueChanges(changes, nil, ChangesReasonNoCriticalIssues, 5)

	if got[0].Salience != issuesapi.ChangeSalienceConfigEdit {
		t.Fatalf("healthy-cluster edit salience = %q, want config_edit only", got[0].Salience)
	}
	if guidance != "" {
		t.Fatalf("healthy-cluster guidance = %q, want none", guidance)
	}
}

func TestPrioritizeIssueChangesSuppressesCrowdedNamedGuidance(t *testing.T) {
	var changes []issuesapi.RecentChange
	for _, name := range []string{"frontend", "checkout", "payment"} {
		changes = append(changes, recentChange("Deployment", "shop", name, time.Now(),
			"spec.template.spec.containers["+name+"].args"))
	}

	got, guidance, _ := PrioritizeIssueChanges(
		changes,
		[]issuesapi.Issue{{Kind: "Deployment", Namespace: "shop", Name: "unrelated", Severity: issuesapi.SeverityCritical}},
		ChangesReasonWithAllCreationTimeCriticalIssues,
		5,
	)

	for _, change := range got {
		if change.Salience != issuesapi.ChangeSaliencePrimeSuspect {
			t.Fatalf("unexplained edit not marked: %+v", change)
		}
	}
	if strings.Contains(guidance, "marks ") {
		t.Fatalf("crowded suspect set must suppress named guidance: %q", guidance)
	}
	if !strings.Contains(guidance, "does not clear the listed changes") {
		t.Fatalf("base timing guidance must remain: %q", guidance)
	}
}

func TestPrioritizeIssueChangesFailsClosedOnCappedConsumers(t *testing.T) {
	change := issuesapi.RecentChange{
		Kind: "ConfigMap", Namespace: "shop", Name: "shared-config",
		ChangeCategory: issuesapi.ChangeCategorySpecConfig,
		Fields:         []issuesapi.ChangeField{{Path: "data.flags.enabled"}},
		ConsumedBy: []string{
			"Deployment/a", "Deployment/b", "Deployment/c", "Deployment/d", "Deployment/e",
		},
	}

	got, _, _ := PrioritizeIssueChanges(
		[]issuesapi.RecentChange{change},
		[]issuesapi.Issue{{Kind: "Deployment", Namespace: "shop", Name: "unrelated", Severity: issuesapi.SeverityCritical}},
		ChangesReasonWithAllCreationTimeCriticalIssues,
		5,
	)

	if got[0].Salience != issuesapi.ChangeSalienceConfigEdit {
		t.Fatalf("capped consumer evidence must fail closed instead of claiming unexplained: %+v", got[0])
	}
}

func TestPrioritizeIssueChangesReportsSecondStageCap(t *testing.T) {
	var changes []issuesapi.RecentChange
	for i := 0; i < IssueChangesCandidateLimit; i++ {
		changes = append(changes, recentChange(
			"Deployment", "shop", fmt.Sprintf("workload-%d", i), time.Now().Add(-time.Duration(i)*time.Second),
			"spec.replicas",
		))
	}

	got, _, capped := PrioritizeIssueChanges(changes, nil, ChangesReasonNoCriticalIssues, IssueChangesLimit)

	if len(got) != IssueChangesLimit || !capped {
		t.Fatalf("second-stage cap = len %d capped %v, want %d/true", len(got), capped, IssueChangesLimit)
	}
}

func TestFromEventClassifiesSalienceBeforeFieldDisplayCap(t *testing.T) {
	change := fromEvent(timeline.TimelineEvent{
		Source: timeline.SourceInformer, Kind: "Deployment", Namespace: "shop", Name: "frontend",
		EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{
			{Path: "spec.replicas"},
			{Path: "spec.template.spec.containers[frontend].env[CART_ADDR]"},
		}},
	}, 1)
	changes := []issuesapi.RecentChange{change}
	RankAndCap(&changes, 1)

	if len(changes[0].Fields) != 1 {
		t.Fatalf("display fields = %d, want cap 1", len(changes[0].Fields))
	}
	if changes[0].Salience != issuesapi.ChangeSalienceConfigEdit {
		t.Fatalf("full producer evidence did not survive display cap: %+v", changes[0])
	}
	if got := changes[0].Fields[0].Path; got != "spec.template.spec.containers[frontend].env[CART_ADDR]" {
		t.Fatalf("displayed field = %q, want the evidence supporting salience", got)
	}
}

func TestFromEventDoesNotDemoteTruncatedRealSpecChangeAsGenerationOnly(t *testing.T) {
	change := fromEvent(timeline.TimelineEvent{
		Source: timeline.SourceInformer, Kind: "Widget", Namespace: "shop", Name: "example",
		EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{
			{Path: "metadata.generation"},
			{Path: "spec.mode"},
		}},
	}, 1)
	changes := []issuesapi.RecentChange{change}
	RankAndCap(&changes, 1)

	if len(changes[0].Fields) != 1 {
		t.Fatalf("display fields = %d, want cap 1", len(changes[0].Fields))
	}
	if got := score(changes[0]); got != 100 {
		t.Fatalf("multi-field spec change score = %d, want 100: %+v", got, changes[0])
	}
	if got := changes[0].Fields[0].Path; got != "spec.mode" {
		t.Fatalf("displayed field = %q, want non-generation evidence", got)
	}
}

func TestRecreatedConfigEditKeepsRecreateContext(t *testing.T) {
	changes := []issuesapi.RecentChange{{
		Kind: "Deployment", Namespace: "shop", Name: "frontend",
		ChangeType:     string(timeline.EventTypeAdd),
		ChangeCategory: issuesapi.ChangeCategorySpecConfig,
		RankReason:     "recreated with desired-state or configuration changes",
		Fields: []issuesapi.ChangeField{{
			Path: "spec.template.spec.containers[frontend].env[CART_ADDR]",
		}},
	}}

	RankAndCap(&changes, 1)

	if changes[0].Salience != issuesapi.ChangeSalienceConfigEdit ||
		!strings.Contains(changes[0].RankReason, "recreated") {
		t.Fatalf("recreate context was lost: %+v", changes[0])
	}

	got, _, _ := PrioritizeIssueChanges(
		changes,
		[]issuesapi.Issue{{
			Kind: "Deployment", Namespace: "shop", Name: "unrelated",
			Severity: issuesapi.SeverityCritical,
		}},
		ChangesReasonWithAllCreationTimeCriticalIssues,
		1,
	)
	if got[0].Salience != issuesapi.ChangeSaliencePrimeSuspect ||
		!strings.Contains(got[0].RankReason, "recreated") {
		t.Fatalf("Tier B lost recreate context: %+v", got[0])
	}
}

func TestRecentDemotesRealGenericCRDGenerationOnlyChange(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
		ID: "widget-gen", Timestamp: time.Now(),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Widget", Namespace: "shop", Name: "example",
		EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{
			Fields: []timeline.FieldChange{{Path: "metadata.generation", OldValue: int64(1), NewValue: int64(2)}},
		},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	changes, _, err := Recent(context.Background(), Query{
		Namespaces: []string{"shop"}, Kinds: []string{"Widget"}, Limit: 5,
	})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(changes) != 1 || score(changes[0]) != 80 || changes[0].Salience != "" {
		t.Fatalf("generic CRD generation change = %+v, want retained score 80 without salience", changes)
	}
}

func recentChange(kind, namespace, name string, at time.Time, fieldPath string) issuesapi.RecentChange {
	return issuesapi.RecentChange{
		Kind: kind, Namespace: namespace, Name: name,
		Timestamp:      at.Format(time.RFC3339),
		ChangeCategory: issuesapi.ChangeCategorySpecConfig,
		Fields:         []issuesapi.ChangeField{{Path: fieldPath}},
	}
}
