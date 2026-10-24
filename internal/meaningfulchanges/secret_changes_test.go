package meaningfulchanges

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func secretFeedFixture(t *testing.T, objects ...runtime.Object) timeline.EventStore {
	t.Helper()
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(objects...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 3000}); err != nil {
		t.Fatal(err)
	}
	timeline.SetObservationStartForTest(time.Now().Add(-2 * time.Hour))
	return timeline.GetStore()
}

func feedSecret(name string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "dummy-last-applied-secret"}}, Data: map[string][]byte{"A": []byte("dummy-secret-value"), "B": []byte("dummy-secret-value-B")}}
}

func feedEnv(kind, name, key string) corev1.EnvVar {
	ref := &corev1.EnvVarSource{}
	if kind == "Secret" {
		ref.SecretKeyRef = &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key}
	} else {
		ref.ConfigMapKeyRef = &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key}
	}
	return corev1.EnvVar{Name: key, ValueFrom: ref}
}

func feedPod(name string, started time.Time, env ...corev1.EnvVar) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: env}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(started)}}}}}}
}

func appendFeedWrite(t *testing.T, store timeline.EventStore, kind, name string, at time.Time, keys ...string) {
	t.Helper()
	if err := store.Append(context.Background(), timeline.TimelineEvent{ID: fmt.Sprintf("%s/%s/%d", kind, name, at.UnixNano()), Kind: kind, APIVersion: "v1", Namespace: "shop", Name: name, Timestamp: at, Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(), EventType: timeline.EventTypeUpdate, Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)", OldValue: keys, NewValue: keys}}, Summary: fmt.Sprintf("data modified keys: %v", keys)}}); err != nil {
		t.Fatal(err)
	}
}

func feedRecent(t *testing.T, q Query) []issuesapi.RecentChange {
	t.Helper()
	q.Namespaces = []string{"shop"}
	result, err := Recent(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return result.Changes
}

func TestSecretAdmissionAndPreviousValue(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name           string
		mutate         func(*corev1.Pod)
		present, stale bool
	}{
		{"env before write", func(*corev1.Pod) {}, true, true},
		{"env after restart", func(p *corev1.Pod) { p.Status.ContainerStatuses[0].State.Running.StartedAt = metav1.NewTime(now) }, true, false},
		{"terminating env reader", func(p *corev1.Pod) { deleted := metav1.NewTime(now); p.DeletionTimestamp = &deleted }, true, false},
		{"secret volume", func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = nil
			p.Spec.Volumes = []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "db"}}}}
		}, true, false},
		{"projected secret", func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = nil
			p.Spec.Volumes = []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "db"}}}}}}}}
		}, true, false},
		{"image pull only", func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = nil
			p.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "db"}}
		}, false, false},
		{"no consumer", func(p *corev1.Pod) { p.Spec.Containers[0].Env = nil }, false, false},
		{"envFrom", func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = nil
			p.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "db"}}}}
		}, true, true},
		{"completed init", func(p *corev1.Pod) {
			p.Spec.InitContainers = []corev1.Container{{Name: "init", Env: p.Spec.Containers[0].Env}}
			p.Spec.Containers[0].Env = nil
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "init", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}
		}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := feedPod("bare", now.Add(-10*time.Minute), feedEnv("Secret", "db", "A"))
			tc.mutate(pod)
			store := secretFeedFixture(t, feedSecret("db"), pod)
			appendFeedWrite(t, store, "Secret", "db", now.Add(-time.Minute), "A")
			rows := feedRecent(t, Query{})
			if (len(rows) == 1) != tc.present {
				t.Fatalf("present=%v rows=%+v", tc.present, rows)
			}
			if tc.present {
				row := rows[0]
				if (row.RunningPreviousValue != nil) != tc.stale {
					t.Fatalf("stale=%v row=%+v", tc.stale, row)
				}
				if !reflect.DeepEqual(row.ConsumedBy, []string{"Pod/bare"}) {
					t.Fatalf("consumed_by=%v", row.ConsumedBy)
				}
				if tc.stale && (row.RunningPreviousValue.Pods != 1 || !reflect.DeepEqual(row.RunningPreviousValue.Keys, []string{"A"}) || row.RunningPreviousValue.Note != runningPreviousValueNote || score(row) != 105) {
					t.Fatalf("annotation=%+v score=%d", row.RunningPreviousValue, score(row))
				}
			}
			for _, q := range []Query{{Kinds: []string{"Secret"}}, {Name: "db"}} {
				all := feedRecent(t, q)
				if len(all) != 1 {
					t.Fatalf("explicit history lost: %+v", all)
				}
			}
		})
	}
}

// Two writes inside one second share a row timestamp; each row keeps only the
// readers of the keys it changed.
func TestSecretSameSecondWritesKeepTheirOwnReaders(t *testing.T) {
	second := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	store := secretFeedFixture(t, feedSecret("db"), feedPod("a", second.Add(-20*time.Minute), feedEnv("Secret", "db", "A")), feedPod("b", second.Add(-20*time.Minute), feedEnv("Secret", "db", "B")))
	appendFeedWrite(t, store, "Secret", "db", second.Add(100*time.Millisecond), "A")
	appendFeedWrite(t, store, "Secret", "db", second.Add(600*time.Millisecond), "B")
	rows := feedRecent(t, Query{Kinds: []string{"Secret"}})
	if len(rows) != 2 {
		t.Fatalf("rows=%+v", rows)
	}
	for _, row := range rows {
		keys := secretChangedKeys(row.Fields)
		if row.RunningPreviousValue == nil || len(row.RunningPreviousValue.Keys) != 1 || !keys[row.RunningPreviousValue.Keys[0]] {
			t.Fatalf("row carries another write's readers: %+v", row)
		}
	}
}

func TestConfigMapSameSecondWritesKeepTheirOwnReaders(t *testing.T) {
	second := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "shop"}, Data: map[string]string{"A": "a", "B": "b"}}
	store := secretFeedFixture(t, config, feedPod("a", second.Add(-20*time.Minute), feedEnv("ConfigMap", "config", "A")), feedPod("b", second.Add(-20*time.Minute), feedEnv("ConfigMap", "config", "B")))
	appendFeedWrite(t, store, "ConfigMap", "config", second.Add(100*time.Millisecond), "A")
	appendFeedWrite(t, store, "ConfigMap", "config", second.Add(600*time.Millisecond), "B")
	rows := feedRecent(t, Query{})
	if len(rows) != 2 {
		t.Fatalf("rows=%+v", rows)
	}
	for _, row := range rows {
		if row.RunningPreviousValue == nil || len(row.RunningPreviousValue.Keys) != 1 || !rowChangedKey(row.Fields, row.RunningPreviousValue.Keys[0]) {
			t.Fatalf("row carries another write's readers: %+v", row)
		}
	}
}

func TestSecretCollapseKeepsNonKeyFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rows := collapseSecretUpdates([]issuesapi.RecentChange{
		{Kind: "Secret", Namespace: "shop", Name: "db", ChangeType: "update", Timestamp: now.Add(-time.Minute).Format(time.RFC3339), Fields: []issuesapi.ChangeField{{Path: "data (modified keys)", OldValue: []string{"A"}, NewValue: []string{"A"}}}},
		{Kind: "Secret", Namespace: "shop", Name: "db", ChangeType: "update", Timestamp: now.Add(-2 * time.Minute).Format(time.RFC3339), Fields: []issuesapi.ChangeField{{Path: "immutable", OldValue: false, NewValue: true}}},
	})
	if len(rows) != 1 || rows[0].UpdatesInWindow != 2 || rows[0].Summary != "data modified keys: [A]; immutable changed" {
		t.Fatalf("collapsed row=%+v", rows)
	}
}

// rank_reason claims references only when consumed_by names them.
func TestSecretRankReasonStatesOnlyEstablishedFacts(t *testing.T) {
	store := secretFeedFixture(t, feedSecret("loose"))
	appendFeedWrite(t, store, "Secret", "loose", time.Now().Add(-time.Minute), "A")
	rows := feedRecent(t, Query{Kinds: []string{"Secret"}})
	if len(rows) != 1 || rows[0].RankReason != "Secret data changed" {
		t.Fatalf("unreferenced Secret row: %+v", rows)
	}
	one := secretRankReason(issuesapi.RecentChange{Kind: "Secret", Fields: rows[0].Fields, RunningPreviousValue: &issuesapi.RunningPreviousValue{Pods: 1}})
	if one != "Secret data changed; 1 Pod likely still holds the earlier value" {
		t.Fatalf("single-Pod reason = %q", one)
	}
	immutable := secretRankReason(issuesapi.RecentChange{Kind: "Secret", Fields: []issuesapi.ChangeField{{Path: "immutable", OldValue: false, NewValue: true}}})
	if immutable != "Secret changed" {
		t.Fatalf("immutable-only reason = %q", immutable)
	}
}

func TestSecretHelmAndAPICollisionExcluded(t *testing.T) {
	secret := feedSecret("sh.helm.release.v1.app.v1")
	secret.Type = "helm.sh/release.v1"
	store := secretFeedFixture(t, secret)
	appendFeedWrite(t, store, "Secret", secret.Name, time.Now().Add(-time.Minute), "release")
	if rows := feedRecent(t, Query{}); len(rows) != 0 {
		t.Fatalf("unreferenced Helm Secret admitted: %+v", rows)
	}
	if err := store.Append(context.Background(), timeline.TimelineEvent{ID: "collision", Kind: "Secret", APIVersion: "example.io/v1", Name: "db", Namespace: "shop", Timestamp: time.Now(), Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(), EventType: timeline.EventTypeUpdate, Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)", NewValue: []string{"A"}}}}}); err != nil {
		t.Fatal(err)
	}
	if rows := feedRecent(t, Query{Kinds: []string{"Secret"}, Name: "db"}); len(rows) != 0 {
		t.Fatalf("non-core Secret admitted: %+v", rows)
	}
}

func TestSecretPerKeyWritesRemainSeparate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store := secretFeedFixture(t, feedSecret("db"), feedPod("a", now.Add(-20*time.Minute), feedEnv("Secret", "db", "A")), feedPod("b", now.Add(-20*time.Minute), feedEnv("Secret", "db", "B")))
	appendFeedWrite(t, store, "Secret", "db", now.Add(-10*time.Minute), "A")
	appendFeedWrite(t, store, "Secret", "db", now.Add(-5*time.Minute), "B")
	for _, q := range []Query{{}, {Kinds: []string{"Secret"}}, {Name: "db"}} {
		rows := feedRecent(t, q)
		if len(rows) != 2 {
			t.Fatalf("per-key rows collapsed: %+v", rows)
		}
		for i, key := range []string{"B", "A"} {
			if rows[i].RunningPreviousValue == nil || !reflect.DeepEqual(rows[i].RunningPreviousValue.Keys, []string{key}) || rows[i].RunningPreviousValue.Pods != 1 {
				t.Fatalf("row %d: %+v", i, rows[i])
			}
		}
	}
}

func TestConfigMapPreviousValueAndPodDeduplication(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "shop"}, Data: map[string]string{"A": "value"}}
	pod := feedPod("reader", now.Add(-10*time.Minute), feedEnv("ConfigMap", "config", "A"), feedEnv("ConfigMap", "config", "B"))
	store := secretFeedFixture(t, cm, pod)
	appendFeedWrite(t, store, "ConfigMap", "config", now.Add(-time.Minute), "A", "B")
	rows := feedRecent(t, Query{})
	if len(rows) != 1 || rows[0].RunningPreviousValue == nil || rows[0].RunningPreviousValue.Pods != 1 || !reflect.DeepEqual(rows[0].RunningPreviousValue.Keys, []string{"A", "B"}) {
		t.Fatalf("rows=%+v", rows)
	}
	rows = feedRecent(t, Query{Viewer: Viewer{CanReadPods: func(string) bool { return false }}})
	if len(rows) != 1 || rows[0].RunningPreviousValue != nil || len(rows[0].ConsumedBy) != 0 {
		t.Fatalf("Pod-derived context leaked: %+v", rows)
	}
}

func TestSecretAuthorizationAndQueryConstructors(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pod := feedPod("hidden", now.Add(-10*time.Minute), feedEnv("Secret", "db", "A"))
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: pod.Spec}}}
	store := secretFeedFixture(t, feedSecret("db"), pod, deployment)
	appendFeedWrite(t, store, "Secret", "db", now.Add(-time.Minute), "A")
	appendFeedWrite(t, store, "ConfigMap", "config", now.Add(-2*time.Minute), "A")
	rows := feedRecent(t, Query{Limit: 1, Allow: func(c issuesapi.RecentChange) bool { return c.Kind != "Secret" }})
	if len(rows) != 1 || rows[0].Kind != "ConfigMap" {
		t.Fatalf("hidden row took cap: %+v", rows)
	}
	for _, constructor := range []string{"Recent", "RecentForResource", "RecentForResourceDetailed", "RecentForWorkloadAndConfigMaps", "RecentForWorkloadAndConfigMapsAuthorizedDetailed"} {
		t.Run(constructor, func(t *testing.T) {
			calls := 0
			deny := func(ns string) bool {
				if ns != "shop" {
					t.Fatalf("namespace=%s", ns)
				}
				calls++
				return false
			}
			var rows []issuesapi.RecentChange
			var err error
			switch constructor {
			case "Recent":
				rows = feedRecent(t, Query{Kinds: []string{"Secret"}, Viewer: Viewer{CanReadPods: deny}})
			case "RecentForResource":
				rows, _, err = RecentForResource(context.Background(), "Secret", "shop", "db", time.Hour, 20, 10, Viewer{CanReadPods: deny})
			case "RecentForResourceDetailed":
				var r RecentResult
				r, err = RecentForResourceDetailed(context.Background(), "Secret", "shop", "db", time.Hour, 20, 10, Viewer{CanReadPods: deny})
				rows = r.Changes
			case "RecentForWorkloadAndConfigMaps":
				rows, _, err = RecentForWorkloadAndConfigMaps(context.Background(), deployment, "Deployment", "shop", "app", time.Hour, 20, 10, Viewer{CanReadPods: deny})
			case "RecentForWorkloadAndConfigMapsAuthorizedDetailed":
				var r RecentResult
				r, _, err = RecentForWorkloadAndConfigMapsAuthorizedDetailed(context.Background(), deployment, "Deployment", "shop", "app", time.Hour, 20, 10, nil, Viewer{CanReadPods: deny})
				rows = r.Changes
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || len(rows) != 1 || rows[0].RunningPreviousValue != nil || !reflect.DeepEqual(rows[0].ConsumedBy, []string{"Deployment/app"}) {
				t.Fatalf("calls=%d rows=%+v", calls, rows)
			}
		})
	}
	rows = feedRecent(t, Query{Viewer: Viewer{CanReadPods: func(string) bool { return false }}})
	if len(rows) != 2 {
		t.Fatalf("Pod permission gated admission: %+v", rows)
	}
	// A caller who can read the Secret and its Pods but not Deployments sees
	// the bare Pod, not the Deployment whose template references the Secret.
	noDeployments := Viewer{CanReadConsumer: func(c issuesapi.RecentChange) bool { return c.Kind != "Deployment" }}
	rows = feedRecent(t, Query{Kinds: []string{"Secret"}, Viewer: noDeployments})
	forbidden := func(n int) *issuesapi.Withheld {
		return &issuesapi.Withheld{Count: n, Reason: readreason.UserForbidden}
	}
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].ConsumedBy, []string{"Pod/hidden"}) || !reflect.DeepEqual(rows[0].ConsumedByWithheld, forbidden(1)) || rows[0].RunningPreviousValue == nil || !reflect.DeepEqual(rows[0].RunningPreviousValue.Workloads, []string{"Pod/hidden"}) || rows[0].RunningPreviousValue.Withheld != nil {
		t.Fatalf("some names hidden: %+v", rows)
	}
	// Without Pod read either, no consumer is named, and both are counted.
	rows = feedRecent(t, Query{Kinds: []string{"Secret"}, Viewer: Viewer{CanReadPods: func(string) bool { return false }, CanReadConsumer: noDeployments.CanReadConsumer}})
	if len(rows) != 1 || len(rows[0].ConsumedBy) != 0 || !reflect.DeepEqual(rows[0].ConsumedByWithheld, forbidden(2)) || rows[0].RunningPreviousValue != nil {
		t.Fatalf("all names hidden: %+v", rows)
	}
	// Unrestricted callers get no marker.
	rows = feedRecent(t, Query{Kinds: []string{"Secret"}})
	if len(rows) != 1 || rows[0].ConsumedByWithheld != nil || rows[0].RunningPreviousValue == nil || rows[0].RunningPreviousValue.Withheld != nil {
		t.Fatalf("unrestricted caller got a withheld marker: %+v", rows)
	}
	// A stale reader whose owner the caller can't read is counted, not named;
	// the Pod count is unchanged because the caller can read the Pods.
	noPodOwners := Viewer{CanReadConsumer: func(c issuesapi.RecentChange) bool { return c.Kind != "Pod" }}
	rows = feedRecent(t, Query{Kinds: []string{"Secret"}, Viewer: noPodOwners})
	if len(rows) != 1 || rows[0].RunningPreviousValue == nil || len(rows[0].RunningPreviousValue.Workloads) != 0 || !reflect.DeepEqual(rows[0].RunningPreviousValue.Withheld, forbidden(1)) || rows[0].RunningPreviousValue.Pods != 1 {
		t.Fatalf("stale owner hidden: %+v", rows)
	}
}

// The read check sees a Rollout owner as argoproj.io/rollouts: the group
// decides, whatever version the owner reference carries.
func TestSecretRolloutOwnerReadCheckUsesItsGroup(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	controller := true
	pod := feedPod("roll-abc", now.Add(-10*time.Minute), feedEnv("Secret", "db", "A"))
	pod.OwnerReferences = []metav1.OwnerReference{{Kind: "Rollout", APIVersion: "argoproj.io/v1alpha1", Name: "roll", Controller: &controller}}
	store := secretFeedFixture(t, feedSecret("db"), pod)
	appendFeedWrite(t, store, "Secret", "db", now.Add(-time.Minute), "A")
	var checked []string
	viewer := func(allowRollouts bool) Viewer {
		return Viewer{CanReadConsumer: func(c issuesapi.RecentChange) bool {
			group := resourceid.GroupFromAPIVersion(c.APIVersion)
			checked = append(checked, group+"/"+c.Kind)
			return group == "argoproj.io" && c.Kind == "Rollout" && allowRollouts
		}}
	}
	rows := feedRecent(t, Query{Kinds: []string{"Secret"}, Viewer: viewer(true)})
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].ConsumedBy, []string{"Rollout/roll"}) || rows[0].RunningPreviousValue == nil || !reflect.DeepEqual(rows[0].RunningPreviousValue.Workloads, []string{"Rollout/roll"}) {
		t.Fatalf("readable Rollout owner not named: %+v (checked %v)", rows, checked)
	}
	rows = feedRecent(t, Query{Kinds: []string{"Secret"}, Viewer: viewer(false)})
	if len(rows) != 1 || len(rows[0].ConsumedBy) != 0 || rows[0].ConsumedByWithheld == nil || rows[0].RunningPreviousValue.Withheld == nil {
		t.Fatalf("unreadable Rollout owner not withheld: %+v", rows)
	}
}

func TestSecretBurstSelectionAndMergedKeys(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	var objects []runtime.Object
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("secret-%d", i)
		objects = append(objects, feedSecret(name), feedPod("reader-"+name, now, feedEnv("Secret", name, "A")))
	}
	store := secretFeedFixture(t, objects...)
	for i := 0; i < 5; i++ {
		for j := 0; j < 3; j++ {
			appendFeedWrite(t, store, "Secret", fmt.Sprintf("secret-%d", i), now.Add(-time.Duration(j+1)*time.Minute), fmt.Sprintf("KEY%d", j))
		}
	}
	if err := store.Append(context.Background(), timeline.TimelineEvent{ID: "image", Kind: "Deployment", APIVersion: "apps/v1", Name: "app", Namespace: "shop", Timestamp: now.Add(-5 * time.Minute), Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(), EventType: timeline.EventTypeUpdate, Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.template.spec.containers[app].image", OldValue: "v1", NewValue: "v2"}}}}); err != nil {
		t.Fatal(err)
	}
	rows := feedRecent(t, Query{Limit: 6})
	if len(rows) != 6 || rows[0].Kind != "Deployment" {
		t.Fatalf("rows=%+v", rows)
	}
	for _, row := range rows[1:] {
		if row.UpdatesInWindow != 3 || row.FirstUpdateAt != now.Add(-3*time.Minute).Format(time.RFC3339) || score(row) != 100 || !reflect.DeepEqual(row.Fields[0].NewValue, []string{"KEY0", "KEY1", "KEY2"}) || row.Summary != "data modified keys: [KEY0 KEY1 KEY2]" {
			t.Fatalf("collapsed=%+v score=%d", row, score(row))
		}
	}
	var response issuesapi.Response
	ApplyIssueChanges(&response, RecentResult{Changes: rows}, nil, ChangesReasonWithAllCreationTimeCriticalIssues, true, true)
	if len(response.RecentChanges) != 5 || response.RecentChanges[0].Kind != "Deployment" {
		t.Fatalf("image crowded out: %+v", response.RecentChanges)
	}
	for _, row := range response.RecentChanges {
		if row.Kind == "Secret" && score(row) == 120 {
			t.Fatalf("healthy rotation promoted: %+v", row)
		}
	}
	if all := feedRecent(t, Query{Kinds: []string{"Secret"}, Limit: 50}); len(all) != 15 {
		t.Fatalf("kind query collapsed: %d", len(all))
	}
	if all := feedRecent(t, Query{Name: "secret-0"}); len(all) != 3 {
		t.Fatalf("name query collapsed: %d", len(all))
	}
}

func TestSecretRankingAndConsumerLinkage(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pod := feedPod("old", now.Add(-10*time.Minute), feedEnv("Secret", "db", "A"))
	controller := true
	pod.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", APIVersion: "apps/v1", Name: "app-old", Controller: &controller}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "app-old", Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", APIVersion: "apps/v1", Name: "app", Controller: &controller}}}}
	store := secretFeedFixture(t, feedSecret("db"), pod, rs)
	appendFeedWrite(t, store, "Secret", "db", now.Add(-time.Minute), "A")
	for _, path := range []string{"spec.replicas", "spec.template.spec.containers[app].image"} {
		if err := store.Append(context.Background(), timeline.TimelineEvent{ID: path, Kind: "Deployment", APIVersion: "apps/v1", Name: path, Namespace: "shop", Timestamp: now.Add(-time.Minute), Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(), EventType: timeline.EventTypeUpdate, Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: path, OldValue: "1", NewValue: "2"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	rows := feedRecent(t, Query{})
	if len(rows) != 3 || rows[0].Kind != "Secret" || score(rows[0]) != 105 || !reflect.DeepEqual(rows[0].RunningPreviousValue.Workloads, []string{"Deployment/app"}) {
		t.Fatalf("ranking=%+v", rows)
	}
	for _, tc := range []struct {
		name      string
		consumers []string
		issues    []issuesapi.Issue
		promote   bool
	}{
		{"unlinked", []string{"Deployment/app"}, nil, true},
		{"linked", []string{"Deployment/app"}, []issuesapi.Issue{{Kind: "Deployment", Namespace: "shop", Name: "app"}}, false},
		{"capped", []string{"Deployment/a", "Deployment/b", "Deployment/c", "Deployment/d", "Deployment/e"}, nil, false},
		{"withheld", nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := rows[0]
			row.ConsumedBy = tc.consumers
			if tc.name == "withheld" {
				row.ConsumedByWithheld = &issuesapi.Withheld{Count: 1, Reason: readreason.UserForbidden}
			}
			changes, _, _ := PrioritizeIssueChanges([]issuesapi.RecentChange{row}, tc.issues, IssueChangePriorityOptions{Reason: ChangesReasonWithAllCreationTimeCriticalIssues, UnfilteredIssueSet: true})
			if changes[0].NotLinkedToReturnedIssues != tc.promote {
				t.Fatalf("row=%+v", changes[0])
			}
		})
	}
}

func TestSecretReferenceUnionAndRolloutSources(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pod := feedPod("old", now, feedEnv("Secret", "old-secret", "A"))
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{feedEnv("Secret", "new-secret", "A")}}}}}}}
	pod.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "old-rs", APIVersion: "apps/v1"}}
	rollout := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "metadata": map[string]any{"name": "roll", "namespace": "shop"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app"}}, "volumes": []any{map[string]any{"name": "config", "secret": map[string]any{"secretName": "mounted"}}}}}}}}
	store := secretFeedFixture(t, pod, deployment)
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "RolloutList"}, rollout)
	if err := k8s.InitTestDynamicResourceCache(dynamic, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Rollout", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForSync(gvr, 2*time.Second) {
		t.Fatal("Rollout cache did not sync")
	}
	for _, name := range []string{"old-secret", "new-secret", "mounted"} {
		appendFeedWrite(t, store, "Secret", name, now.Add(-time.Minute), "A")
	}
	// The feed indexes Rollouts through their running Pods only, so a Rollout
	// template with no Pods admits nothing; diagnose and correlation still read
	// the Rollout's own sources below.
	rows := feedRecent(t, Query{})
	if len(rows) != 2 {
		t.Fatalf("reference union=%+v", rows)
	}
	for _, row := range rows {
		if row.Name == "mounted" {
			t.Fatalf("Pod-less Rollout template admitted a row: %+v", row)
		}
	}
	result, _, err := RecentForWorkloadAndConfigMapsAuthorizedDetailed(context.Background(), rollout, "Rollout", "shop", "roll", time.Hour, 20, 10, nil, Viewer{})
	if err != nil || len(result.Changes) != 1 || result.Changes[0].Name != "mounted" {
		t.Fatalf("mounted Rollout source: %+v %v", result, err)
	}
	result, _, err = RecentForWorkloadAndConfigMapsAuthorizedDetailed(context.Background(), deployment, "Deployment", "shop", "app", time.Hour, 20, 10, nil, Viewer{})
	if err != nil || len(result.Changes) != 1 || result.Changes[0].Name != "new-secret" {
		t.Fatalf("workload sources: %+v %v", result, err)
	}
}

func TestSecretChangeRowsRedactValuesAndAnnotations(t *testing.T) {
	secret := feedSecret("db")
	store := secretFeedFixture(t, secret, feedPod("reader", time.Now().Add(-10*time.Minute), feedEnv("Secret", "db", "A")))
	old := secret.DeepCopy()
	old.Data["A"] = []byte("dummy-old-secret-value")
	diff := k8s.ComputeDiff("Secret", old, secret)
	if diff == nil {
		t.Fatal("missing diff")
	}
	if err := store.Append(context.Background(), timeline.TimelineEvent{ID: "redaction", Kind: "Secret", APIVersion: "v1", Name: "db", Namespace: "shop", Timestamp: time.Now().Add(-time.Minute), Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(), EventType: timeline.EventTypeUpdate, Diff: diff}); err != nil {
		t.Fatal(err)
	}
	rows := feedRecent(t, Query{})
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{"dummy-secret-value", "dummy-old-secret-value", "dummy-last-applied-secret", "kubectl.kubernetes.io/last-applied-configuration"} {
		if strings.Contains(string(data), sentinel) {
			t.Fatal("response contains forbidden Secret material")
		}
	}
	if len(rows) != 1 || rows[0].RunningPreviousValue == nil {
		t.Fatalf("redaction fixture missing change: %+v", rows)
	}
}

func TestSecretManagedFieldsDoesNotAnnotateObservedRow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	secret := feedSecret("db")
	secret.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "controller", Operation: metav1.ManagedFieldsOperationUpdate, Time: &metav1.Time{Time: now.Add(-time.Minute)}, FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:A":{}}}`)}}}
	pod := feedPod("reader", now.Add(-10*time.Minute), feedEnv("Secret", "db", "A"))
	store := secretFeedFixture(t, secret, pod)
	appendFeedWrite(t, store, "Secret", "db", now.Add(-time.Minute), "B")
	rows := feedRecent(t, Query{})
	if len(rows) != 1 || rows[0].RunningPreviousValue != nil {
		t.Fatalf("managedFields evidence used: %+v", rows)
	}
	rows = feedRecent(t, Query{Viewer: Viewer{CanReadPods: func(string) bool { return false }}})
	if len(rows) != 1 || len(rows[0].ConsumedBy) != 0 || rows[0].RunningPreviousValue != nil {
		t.Fatalf("Pod-derived names leaked or admission lost: %+v", rows)
	}
}

func TestSecretPreviousValueCountsPodsAndCapsWorkloads(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	objects := []runtime.Object{feedSecret("db")}
	for i := 0; i < 6; i++ {
		objects = append(objects, feedPod(fmt.Sprintf("reader-%d", i), now.Add(-10*time.Minute), feedEnv("Secret", "db", "A"), feedEnv("Secret", "db", "A")))
	}
	store := secretFeedFixture(t, objects...)
	appendFeedWrite(t, store, "Secret", "db", now.Add(-time.Minute), "A")
	rows := feedRecent(t, Query{})
	if len(rows) != 1 || rows[0].RunningPreviousValue == nil {
		t.Fatalf("rows=%+v", rows)
	}
	annotation := rows[0].RunningPreviousValue
	if annotation.Pods != 6 || len(annotation.Workloads) != 5 || len(rows[0].ConsumedBy) != 5 || len(annotation.Keys) != 1 {
		t.Fatalf("annotation=%+v consumers=%v", annotation, rows[0].ConsumedBy)
	}
	config := issuesapi.RecentChange{Kind: "ConfigMap", Timestamp: now.Format(time.RFC3339), ChangeCategory: issuesapi.ChangeCategorySpecConfig, ConsumedBy: []string{"Deployment/app"}, Fields: []issuesapi.ChangeField{{Path: "data (modified keys)"}}, RunningPreviousValue: annotation}
	ranked := []issuesapi.RecentChange{config, rows[0]}
	RankAndCap(&ranked, 2)
	if ranked[0].Kind != "Secret" {
		t.Fatalf("stale Secret must lead tier 105: %+v", ranked)
	}
}

func TestSecretCandidateSaturation(t *testing.T) {
	store := secretFeedFixture(t, feedSecret("db"), feedPod("reader", time.Now(), feedEnv("Secret", "db", "A")))
	for i := 0; i <= FetchBound; i++ {
		appendFeedWrite(t, store, "Secret", "db", time.Now().Add(-time.Duration(i+1)*time.Second), "A")
	}
	result, err := Recent(context.Background(), Query{Namespaces: []string{"shop"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.FetchSaturated || len(result.Changes) != 1 || result.Changes[0].UpdatesInWindow != FetchBound {
		t.Fatalf("saturation=%v changes=%+v", result.FetchSaturated, result.Changes)
	}
}

func TestSecretRepeatedStaleWritesCollapseIntoNewestEvidence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store := secretFeedFixture(t, feedSecret("db"), feedPod("reader", now.Add(-10*time.Minute), feedEnv("Secret", "db", "A")))
	for i := 1; i <= 3; i++ {
		appendFeedWrite(t, store, "Secret", "db", now.Add(-time.Duration(i)*time.Minute), "A")
	}
	rows := feedRecent(t, Query{})
	if len(rows) != 1 || rows[0].UpdatesInWindow != 3 || rows[0].RunningPreviousValue == nil || rows[0].Timestamp != now.Add(-time.Minute).Format(time.RFC3339) {
		t.Fatalf("repeated stale writes=%+v", rows)
	}
	all := feedRecent(t, Query{Kinds: []string{"Secret"}})
	if len(all) != 3 || all[0].RunningPreviousValue == nil || all[1].RunningPreviousValue != nil || all[2].RunningPreviousValue != nil {
		t.Fatalf("explicit history=%+v", all)
	}
}
