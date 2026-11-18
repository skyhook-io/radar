package issuecorrelation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// webDeployment references a ConfigMap and a Secret.
func webDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", CreationTimestamp: metav1.NewTime(time.Now().Add(-24 * time.Hour))},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "web-config"},
			}}}},
			Containers: []corev1.Container{{Name: "web", Image: "web:1", Env: []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "web-creds"}, Key: "token"},
			}}}}},
		}}},
	}
}

func webIssue(firstSeen time.Time) issuesapi.Issue {
	return issuesapi.Issue{
		ID: "web-crashloop", Severity: issuesapi.SeverityCritical, Category: issuesapi.CategoryCrashLoop,
		Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web", FirstSeen: firstSeen,
	}
}

func anchoredOptions(iss issuesapi.Issue) Options {
	return Options{Window: WindowAroundFirstSeen(iss, time.Now()), Selection: SelectAroundFirstSeen, CheckCoverage: true}
}

func specUpdate(kind, name string, at time.Time, value string) timeline.TimelineEvent {
	return timeline.TimelineEvent{
		ID: fmt.Sprintf("%s/%s/%d", kind, name, at.UnixNano()), Timestamp: at,
		Kind: kind, APIVersion: apiVersionFor(kind), Namespace: "shop", Name: name, EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.template.spec.containers[web].image", OldValue: "web:0", NewValue: value}}},
	}
}

func apiVersionFor(kind string) string {
	if kind == "Deployment" {
		return "apps/v1"
	}
	return "v1"
}

func checkDenying(kinds ...string) func(kind, group, namespace string) Decision {
	return func(kind, _, _ string) Decision {
		if slices.Contains(kinds, kind) {
			return Forbidden
		}
		return Allowed
	}
}

// Every reason an empty answer is unknown, from the caller's permissions or
// Radar's own collection, never from what hidden data exists.
func TestCorrelate_UnknownReasons(t *testing.T) {
	firstSeen := time.Now().Add(-10 * time.Minute)
	webSources := []k8score.ResourceType{k8score.Deployments, k8score.ConfigMaps, k8score.Secrets}

	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T)
		access Access
		want   issuesapi.CorrelationUnknownReason
		kinds  []string
	}{
		{
			name:   "user_forbidden on a referenced ConfigMap",
			setup:  func(t *testing.T) { initCache(t, webDeployment()); initStore(t) },
			access: Access{Check: checkDenying("ConfigMap")},
			want:   issuesapi.CorrelationUnknownUserForbidden, kinds: []string{"ConfigMap"},
		},
		{
			name:   "user_forbidden on the subject",
			setup:  func(t *testing.T) { initCache(t, webDeployment()); initStore(t) },
			access: Access{Check: checkDenying("Deployment")},
			want:   issuesapi.CorrelationUnknownUserForbidden, kinds: []string{"Deployment"},
		},
		{
			name:  "user_permission_check_failed",
			setup: func(t *testing.T) { initCache(t, webDeployment()); initStore(t) },
			access: Access{Check: func(kind, _, _ string) Decision {
				if kind == "Secret" {
					return CheckFailed
				}
				return Allowed
			}},
			want: issuesapi.CorrelationUnknownPermissionCheckFailed, kinds: []string{"Secret"},
		},
		{
			name: "radar_kind_not_collected",
			setup: func(t *testing.T) {
				initScopedCache(t, map[string]k8score.ResourceScope{
					k8score.Deployments: {Enabled: true}, k8score.Secrets: {Enabled: true},
				}, webDeployment())
				initStore(t)
			},
			want: issuesapi.CorrelationUnknownKindNotCollected, kinds: []string{"ConfigMap"},
		},
		{
			name: "radar_namespace_not_collected",
			setup: func(t *testing.T) {
				initScopedCache(t, map[string]k8score.ResourceScope{
					k8score.Deployments: {Enabled: true}, k8score.Secrets: {Enabled: true}, k8score.ConfigMaps: {Enabled: true, Namespace: "other"},
				}, webDeployment())
				initStore(t)
			},
			want: issuesapi.CorrelationUnknownNamespaceNotCollected, kinds: []string{"ConfigMap"},
		},
		{
			name: "kind_sync_pending",
			setup: func(t *testing.T) {
				types := map[string]bool{}
				for _, rt := range webSources {
					types[rt] = true
				}
				if err := k8s.InitTestResourceCacheFromConfig(k8score.CacheConfig{
					Client: k8sfake.NewSimpleClientset(webDeployment()), ResourceTypes: types,
					DeferredTypes:   map[string]bool{k8score.Secrets: true},
					DebugSyncDelays: map[string]time.Duration{k8score.Secrets: time.Hour},
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(k8s.ResetTestState)
				waitSynced(t, k8score.Deployments)
				initStore(t)
			},
			want: issuesapi.CorrelationUnknownKindSyncPending, kinds: []string{"Secret"},
		},
		{
			name: "read_error",
			setup: func(t *testing.T) {
				initCache(t, webDeployment())
				timeline.ResetStore()
				t.Cleanup(timeline.ResetStore)
			},
			want: issuesapi.CorrelationUnknownReadError, kinds: []string{"Deployment"},
		},
		{
			name:  "sources_unresolved",
			setup: func(t *testing.T) { initCache(t); initStore(t) },
			want:  issuesapi.CorrelationUnknownSourcesUnresolved, kinds: []string{"Deployment"},
		},
		{
			name: "history_incomplete",
			setup: func(t *testing.T) {
				initCache(t, webDeployment())
				initStore(t)
				timeline.SetObservationStartForTest(time.Now().Add(-20 * time.Minute))
			},
			want: issuesapi.CorrelationUnknownHistoryIncomplete,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			iss := webIssue(firstSeen)
			got := Correlate(context.Background(), iss, anchoredOptions(iss), tc.access)
			if !got.Eligible || got.NoChanges {
				t.Fatalf("an unknown answer claimed absence: %+v", got)
			}
			if got.Unknown == nil || got.Unknown.Reason != tc.want {
				t.Fatalf("unknown = %+v, want %s", got.Unknown, tc.want)
			}
			var kinds []string
			for _, k := range got.Unknown.Kinds {
				kinds = append(kinds, k.Kind)
			}
			if !slices.Equal(kinds, tc.kinds) {
				t.Fatalf("kinds = %v, want %v", kinds, tc.kinds)
			}
		})
	}

	t.Run("no reason: absence is claimed", func(t *testing.T) {
		initCache(t, webDeployment())
		initStore(t)
		iss := webIssue(firstSeen)
		got := Correlate(context.Background(), iss, anchoredOptions(iss), Access{Check: checkDenying()})
		if got.Unknown != nil || !got.NoChanges {
			t.Fatalf("complete quiet answer = %+v", got)
		}
	})
}

func initScopedCache(t *testing.T, scopes map[string]k8score.ResourceScope, objs ...*appsv1.Deployment) {
	t.Helper()
	client := k8sfake.NewSimpleClientset()
	for _, o := range objs {
		if _, err := client.AppsV1().Deployments(o.Namespace).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := k8s.InitScopedTestResourceCache(client, scopes); err != nil {
		t.Fatalf("InitScopedTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
}

func waitSynced(t *testing.T, resource string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if synced, known := k8s.GetResourceCache().InformerSynced(resource); known && synced {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never synced", resource)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A hidden source is decided before it is read: the answer is the same
// whether or not it changed.
func TestCorrelate_HiddenSourceSameAnswerEitherWay(t *testing.T) {
	firstSeen := time.Now().Add(-10 * time.Minute)
	answer := func(changed bool) Result {
		initCache(t, webDeployment())
		store := initStore(t)
		if changed {
			appendChange(t, store, specUpdate("ConfigMap", "web-config", firstSeen.Add(-2*time.Minute), "on"))
		}
		iss := webIssue(firstSeen)
		return Correlate(context.Background(), iss, anchoredOptions(iss), Access{Check: checkDenying("ConfigMap")})
	}
	quiet, changed := answer(false), answer(true)
	if quiet.NoChanges || changed.NoChanges || len(quiet.Changes) != 0 || len(changed.Changes) != 0 ||
		quiet.Unknown == nil || !reflect.DeepEqual(quiet.Unknown, changed.Unknown) {
		t.Fatalf("answers differ:\nquiet:   %+v\nchanged: %+v", quiet, changed)
	}
}

// Changes are picked around first seen: the trigger before it survives a run
// of later repairs, and the cut is reported.
func TestCorrelate_AnchoredSelectionKeepsTheTrigger(t *testing.T) {
	initCache(t, webDeployment())
	store := initStore(t)
	timeline.SetObservationStartForTest(time.Now().Add(-5 * time.Hour))
	firstSeen := time.Now().Add(-3 * time.Hour)
	trigger := firstSeen.Add(-4 * time.Minute).Truncate(time.Second)
	appendChange(t, store, specUpdate("Deployment", "web", trigger, "web:bad"))
	// Older than the window: an hour before first seen is where it starts.
	appendChange(t, store, specUpdate("Deployment", "web", firstSeen.Add(-2*time.Hour), "web:ancient"))
	var repairs []time.Time
	for i, ago := range []time.Duration{150 * time.Minute, 90 * time.Minute, 40 * time.Minute, 10 * time.Minute} {
		at := time.Now().Add(-ago).Truncate(time.Second)
		repairs = append(repairs, at)
		appendChange(t, store, specUpdate("Deployment", "web", at, fmt.Sprintf("web:fix%d", i)))
	}
	iss := webIssue(firstSeen)
	got := Correlate(context.Background(), iss, anchoredOptions(iss), Access{})
	var times []string
	for _, c := range got.Changes {
		times = append(times, utc(changeTime(c)))
	}
	want := []string{utc(trigger), utc(repairs[3]), utc(repairs[2])}
	if !slices.Equal(times, want) || !got.MoreNotShown {
		t.Fatalf("changes = %v (more %v), want %v with more not shown", times, got.MoreNotShown, want)
	}
}

func TestSelectAroundFirstSeen(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	firstSeen := now.Add(-time.Hour)
	at := func(d time.Duration) issuesapi.RecentChange {
		return issuesapi.RecentChange{Kind: "Deployment", Name: "web", Timestamp: firstSeen.Add(d).UTC().Format(time.RFC3339)}
	}
	offsets := func(changes []issuesapi.RecentChange) []time.Duration {
		var out []time.Duration
		for _, c := range changes {
			out = append(out, changeTime(c).Sub(firstSeen))
		}
		return out
	}
	m := time.Minute
	for _, tc := range []struct {
		name     string
		iss      issuesapi.Issue
		changes  []issuesapi.RecentChange
		want     []time.Duration
		wantMore bool
	}{
		{"closest before first, then newest after", issuesapi.Issue{FirstSeen: firstSeen},
			[]issuesapi.RecentChange{at(-50 * m), at(-2 * m), at(-20 * m), at(-5 * m), at(10 * m), at(40 * m), at(20 * m)},
			[]time.Duration{-2 * m, -5 * m, -20 * m, 40 * m, 20 * m}, true},
		{"within the slop counts as before", issuesapi.Issue{FirstSeen: firstSeen},
			[]issuesapi.RecentChange{at(20 * time.Second), at(5 * m)},
			[]time.Duration{20 * time.Second, 5 * m}, false},
		{"onset unknown keeps the three newest", issuesapi.Issue{FirstSeen: firstSeen, OnsetUnknown: true},
			[]issuesapi.RecentChange{at(-50 * m), at(-2 * m), at(10 * m), at(40 * m)},
			[]time.Duration{40 * m, 10 * m, -2 * m}, true},
		{"mixed grouped onset keeps the three newest", issuesapi.Issue{FirstSeen: firstSeen, OnsetCoverage: &issuesapi.OnsetCoverage{Known: 1, Unknown: 1}},
			[]issuesapi.RecentChange{at(-50 * m), at(-2 * m)},
			[]time.Duration{-2 * m, -50 * m}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, more := selectAroundFirstSeen(tc.changes, tc.iss)
			if !slices.Equal(offsets(got), tc.want) || more != tc.wantMore {
				t.Fatalf("got %v more=%v, want %v more=%v", offsets(got), more, tc.want, tc.wantMore)
			}
		})
	}
}

// more_not_shown counts only changes the caller could be shown: status-only
// rows never set it, however many there are; a final cut does.
func TestCorrelate_MoreNotShown(t *testing.T) {
	firstSeen := time.Now().Add(-10 * time.Minute)

	t.Run("status-only rows", func(t *testing.T) {
		initCache(t, webDeployment())
		store := initStore(t)
		for i := range 80 {
			appendChange(t, store, timeline.TimelineEvent{
				ID: fmt.Sprintf("status-%d", i), Timestamp: firstSeen.Add(-time.Duration(i) * time.Second),
				Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web", EventType: timeline.EventTypeUpdate,
				Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "status.readyReplicas", OldValue: int32(i % 2), NewValue: int32((i + 1) % 2)}}},
			})
		}
		appendChange(t, store, specUpdate("Deployment", "web", firstSeen.Add(-30*time.Minute), "web:2"))
		iss := webIssue(firstSeen)
		got := Correlate(context.Background(), iss, anchoredOptions(iss), Access{})
		if len(got.Changes) != 1 || got.MoreNotShown {
			t.Fatalf("status churn counted: %d changes, more=%v", len(got.Changes), got.MoreNotShown)
		}
	})

	t.Run("hidden rows", func(t *testing.T) {
		initCache(t, webDeployment())
		store := initStore(t)
		appendChange(t, store, specUpdate("Deployment", "web", firstSeen.Add(-30*time.Minute), "web:2"))
		for i := range 4 {
			appendChange(t, store, specUpdate("ConfigMap", "web-config", firstSeen.Add(-time.Duration(i+1)*time.Minute), fmt.Sprint(i)))
		}
		iss := webIssue(firstSeen)
		// Allowed by the check, dropped by the row filter: never counted.
		got := Correlate(context.Background(), iss, anchoredOptions(iss), Access{RowFilter: func(c issuesapi.RecentChange) bool { return c.Kind == "Deployment" }})
		if len(got.Changes) != 1 || got.MoreNotShown {
			t.Fatalf("hidden rows counted: %d changes, more=%v", len(got.Changes), got.MoreNotShown)
		}
	})

	t.Run("final cut", func(t *testing.T) {
		initCache(t, webDeployment())
		store := initStore(t)
		for i := range 4 {
			appendChange(t, store, specUpdate("Deployment", "web", firstSeen.Add(-time.Duration(i+1)*time.Minute), fmt.Sprint(i)))
		}
		iss := webIssue(firstSeen)
		got := Correlate(context.Background(), iss, anchoredOptions(iss), Access{})
		if len(got.Changes) != anchoredBefore || !got.MoreNotShown {
			t.Fatalf("cut not reported: %d changes, more=%v", len(got.Changes), got.MoreNotShown)
		}
	})

	t.Run("admission policy cut", func(t *testing.T) {
		quota := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "compute", Namespace: "shop"}}
		initCache(t, webDeployment(), quota)
		store := initStore(t)
		for i := range 3 {
			appendChange(t, store, timeline.TimelineEvent{
				ID: fmt.Sprintf("quota-%d", i), Timestamp: firstSeen.Add(-time.Duration(i+1) * time.Minute),
				Kind: "ResourceQuota", APIVersion: "v1", Namespace: "shop", Name: "compute", EventType: timeline.EventTypeUpdate,
				Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.hard.pods", OldValue: fmt.Sprint(i + 4), NewValue: fmt.Sprint(i + 3)}}},
			})
		}
		iss := webIssue(firstSeen)
		iss.Category = issuesapi.CategoryQuotaExceeded
		iss.Message = `pod creation blocked: pods "web-x" is forbidden: exceeded quota: compute, requested: pods=1, used: pods=3, limited: pods=3`
		got := Correlate(context.Background(), iss, anchoredOptions(iss), Access{})
		if len(got.Changes) != admissionBlockerMaxChanges || !got.MoreNotShown {
			t.Fatalf("policy cut not reported: %d changes, more=%v", len(got.Changes), got.MoreNotShown)
		}
	})
}

// A native Helm release's history is read as the caller: a refusal is the
// caller's, any other failure a read error, and neither claims absence.
func TestCorrelate_HelmHistoryReadAsCaller(t *testing.T) {
	initStore(t)
	iss := issuesapi.Issue{ID: "helm", Severity: issuesapi.SeverityCritical, Kind: "HelmRelease", Group: "helm.sh", Namespace: "shop", Name: "payments", FirstSeen: time.Now().Add(-5 * time.Minute)}
	history := func(err error, rows ...issuesapi.RecentChange) Access {
		return Access{HelmHistory: func(context.Context, string, string, Window) ([]issuesapi.RecentChange, error) { return rows, err }}
	}
	for _, tc := range []struct {
		name   string
		access Access
		want   issuesapi.CorrelationUnknownReason
	}{
		{"denied", history(fmt.Errorf("list release secrets: %w", ErrForbidden)), issuesapi.CorrelationUnknownUserForbidden},
		{"failed", history(errors.New("connection refused")), issuesapi.CorrelationUnknownReadError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Correlate(context.Background(), iss, anchoredOptions(iss), tc.access)
			if got.NoChanges || got.Unknown == nil || got.Unknown.Reason != tc.want {
				t.Fatalf("got %+v", got)
			}
		})
	}
	t.Run("ok", func(t *testing.T) {
		row := issuesapi.RecentChange{Source: HelmChangeSource, Kind: "HelmRelease", Namespace: "shop", Name: "payments", ChangeType: "upgrade_failed",
			Timestamp: time.Now().Add(-6 * time.Minute).UTC().Format(time.RFC3339), ChangeCategory: issuesapi.ChangeCategorySpecConfig}
		// A row filter that would drop every row: Helm rows are already read as
		// the caller, so it doesn't apply to them.
		access := history(nil, row)
		access.RowFilter = func(issuesapi.RecentChange) bool { return false }
		got := Correlate(context.Background(), iss, anchoredOptions(iss), access)
		if got.Unknown != nil || len(got.Changes) != 1 {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestWindowAroundFirstSeen(t *testing.T) {
	now := time.Now()
	known := issuesapi.Issue{FirstSeen: now.Add(-3 * time.Hour)}
	if w := WindowAroundFirstSeen(known, now); !w.From.Equal(now.Add(-4*time.Hour)) || !w.To.Equal(now) || w.OpenEnded {
		t.Fatalf("known onset window = %+v", w)
	}
	for _, iss := range []issuesapi.Issue{{}, {FirstSeen: now.Add(-3 * time.Hour), OnsetUnknown: true}, {FirstSeen: now.Add(time.Hour)}} {
		if w := WindowAroundFirstSeen(iss, now); !w.From.Equal(now.Add(-time.Hour)) {
			t.Fatalf("issue %+v: window = %+v, want the hour before now", iss, w)
		}
	}
}
