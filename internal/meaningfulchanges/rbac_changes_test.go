package meaningfulchanges

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func rbacEvent(kind, namespace, name, uid string, eventType timeline.EventType, at time.Time, diff *timeline.DiffInfo) timeline.TimelineEvent {
	return timeline.TimelineEvent{
		ID: kind + "/" + namespace + "/" + name + "/" + string(eventType), Kind: kind, APIVersion: "rbac.authorization.k8s.io/v1",
		Namespace: namespace, Name: name, UID: uid, Timestamp: at, Source: timeline.SourceInformer,
		ClusterContext: k8s.ActiveClusterContext(), EventType: eventType, Diff: diff,
	}
}

func appendEvents(t *testing.T, store timeline.EventStore, events ...timeline.TimelineEvent) {
	t.Helper()
	for _, e := range events {
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

func changeByName(changes []issuesapi.RecentChange, kind, name string) *issuesapi.RecentChange {
	for i := range changes {
		if changes[i].Kind == kind && changes[i].Name == name {
			return &changes[i]
		}
	}
	return nil
}

// The nightly_rebalance shape: a Role and a RoleBinding created in the
// workload's namespace, bound to a ServiceAccount in kube-system.
func TestRecentListsRBACChangesAndNamesOutsideActors(t *testing.T) {
	sa := func(namespace, name string) rbacv1.Subject {
		return rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: namespace, Name: name}
	}
	roleRef := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "vpa-updater"}
	objects := []runtime.Object{
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "vpa-updater", Namespace: "saleor", UID: "rb-foreign"}, RoleRef: roleRef, Subjects: []rbacv1.Subject{sa("kube-system", "vpa-updater")}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "saleor", UID: "rb-local"}, RoleRef: roleRef, Subjects: []rbacv1.Subject{sa("saleor", "app")}},
		// Recreated since the recorded create: a later object under the name.
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "replaced", Namespace: "saleor", UID: "rb-new"}, RoleRef: roleRef, Subjects: []rbacv1.Subject{sa("other", "x")}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "repointed", Namespace: "saleor", UID: "rb-repointed"}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "edit"}, Subjects: []rbacv1.Subject{sa("kube-system", "vpa-updater"), sa("saleor", "app")}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "ops", UID: "crb-1"}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "edit"}, Subjects: []rbacv1.Subject{sa("ops", "bot")}},
	}
	store := secretFeedFixture(t, objects...)
	now := time.Now()
	appendEvents(t, store,
		rbacEvent("Role", "saleor", "vpa-updater", "role-1", timeline.EventTypeAdd, now.Add(-5*time.Minute), nil),
		rbacEvent("RoleBinding", "saleor", "vpa-updater", "rb-foreign", timeline.EventTypeAdd, now.Add(-5*time.Minute), &timeline.DiffInfo{
			Summary: "created; it binds Role vpa-updater",
			Fields:  []timeline.FieldChange{{Path: "subjects", NewValue: []string{"ServiceAccount kube-system/vpa-updater"}}, {Path: "roleRef", NewValue: "Role vpa-updater"}},
		}),
		rbacEvent("RoleBinding", "saleor", "app", "rb-local", timeline.EventTypeAdd, now.Add(-4*time.Minute), nil),
		// The cached "replaced" is a later object, so its roleRef is not this one's.
		rbacEvent("RoleBinding", "saleor", "replaced", "rb-old", timeline.EventTypeUpdate, now.Add(-4*time.Minute), &timeline.DiffInfo{
			Summary: "subjects changed",
			Fields:  []timeline.FieldChange{{Path: "subjects", NewValue: []string{"ServiceAccount other/x"}}},
		}),
		rbacEvent("RoleBinding", "saleor", "widened", "rb-w", timeline.EventTypeUpdate, now.Add(-3*time.Minute), &timeline.DiffInfo{
			Summary: "subjects changed",
			// A persistent store returns []string values as []any.
			Fields: []timeline.FieldChange{{Path: "subjects", OldValue: []any{"ServiceAccount saleor/app"}, NewValue: []any{"ServiceAccount monitoring/prom", "ServiceAccount saleor/app"}}},
		}),
		rbacEvent("RoleBinding", "saleor", "revoked", "rb-r", timeline.EventTypeDelete, now.Add(-2*time.Minute), &timeline.DiffInfo{
			Summary: "deleted; it bound ClusterRole admin",
			Fields:  []timeline.FieldChange{{Path: "subjects", OldValue: []string{"ServiceAccount ci/deployer", "User alice"}}, {Path: "roleRef", OldValue: "ClusterRole admin"}},
		}),
		rbacEvent("Role", "saleor", "vpa-updater", "role-1", timeline.EventTypeUpdate, now.Add(-time.Minute), &timeline.DiffInfo{
			Summary: "rules changed",
			Fields:  []timeline.FieldChange{{Path: "rules", OldValue: []string{"batch/cronjobs: get"}, NewValue: []string{"batch/cronjobs: get,patch"}}},
		}),
		recreated(rbacEvent("RoleBinding", "saleor", "repointed", "rb-repointed", timeline.EventTypeAdd, now.Add(-90*time.Second), &timeline.DiffInfo{
			Summary: "recreated with changes: roleRef: Role vpa-updater→ClusterRole edit",
			Fields:  []timeline.FieldChange{{Path: "roleRef", OldValue: "Role vpa-updater", NewValue: "ClusterRole edit"}},
		})),
		rbacEvent("ClusterRoleBinding", "", "ops", "crb-1", timeline.EventTypeAdd, now.Add(-time.Minute), &timeline.DiffInfo{
			Summary: "created; it binds ClusterRole edit",
			Fields:  []timeline.FieldChange{{Path: "subjects", NewValue: []string{"ServiceAccount ops/bot"}}, {Path: "roleRef", NewValue: "ClusterRole edit"}},
		}),
	)

	changes := feedRecentIn(t, "saleor")
	wantReasons := map[string]string{
		"RoleBinding/vpa-updater": "grants Role vpa-updater to ServiceAccount kube-system/vpa-updater",
		"RoleBinding/app":         lifecycleRankReason,
		"RoleBinding/replaced":    "grants its role to ServiceAccount other/x",
		// widened is not cached, so its role is unknown.
		"RoleBinding/widened": "grants its role to ServiceAccount monitoring/prom",
		"RoleBinding/revoked": "deleted; it granted ClusterRole admin to ServiceAccount ci/deployer",
		"Role/vpa-updater":    "desired-state or configuration field changed",
	}
	for key, want := range wantReasons {
		kind, name, _ := strings.Cut(key, "/")
		got := changeByName(changes, kind, name)
		if got == nil {
			t.Fatalf("%s missing from the namespace feed: %+v", key, changes)
		}
		if got.RankReason != want {
			t.Errorf("%s rank_reason = %q, want %q", key, got.RankReason, want)
		}
	}
	if role := changeByName(changes, "Role", "vpa-updater"); role.ChangeCategory != issuesapi.ChangeCategorySpecConfig {
		t.Errorf("Role rules edit category = %q", role.ChangeCategory)
	}
	if changeByName(changes, "ClusterRoleBinding", "ops") != nil {
		t.Error("a ClusterRoleBinding must not list in a namespace feed")
	}

	cluster, err := Recent(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	crb := changeByName(cluster.Changes, "ClusterRoleBinding", "ops")
	if crb == nil || crb.RankReason != "grants ClusterRole edit cluster-wide to ServiceAccount ops/bot" {
		t.Fatalf("cluster-wide ClusterRoleBinding row = %+v", crb)
	}
}

func recreated(e timeline.TimelineEvent) timeline.TimelineEvent {
	e.Reason = timeline.ReasonRecreated
	return e
}

func feedRecentIn(t *testing.T, namespace string) []issuesapi.RecentChange {
	t.Helper()
	result, err := Recent(context.Background(), Query{Namespaces: []string{namespace}})
	if err != nil {
		t.Fatal(err)
	}
	return result.Changes
}

func TestNamedGrantSubjectsCap(t *testing.T) {
	got := namedSubjects([]string{"ServiceAccount a/1", "ServiceAccount a/2", "ServiceAccount a/3", "ServiceAccount a/4", "ServiceAccount a/5"})
	if want := "ServiceAccount a/1, ServiceAccount a/2, ServiceAccount a/3 and 2 more"; got != want {
		t.Fatalf("namedSubjects = %q, want %q", got, want)
	}
}

func TestRBACKindsAreTracked(t *testing.T) {
	for _, kind := range []string{"Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding"} {
		if !TrackedKindForGroup(kind, "rbac.authorization.k8s.io") || TrackedKindForGroup(kind, "example.com") {
			t.Errorf("%s tracking by group is wrong", kind)
		}
	}
}

// An install run of newer creates must not push an outside grant past the
// output limit.
func TestOutsideGrantRanksAboveInstallCreates(t *testing.T) {
	now := time.Now()
	changes := []issuesapi.RecentChange{{Kind: "RoleBinding", Name: "vpa-updater", ChangeType: "add", ChangeCategory: issuesapi.ChangeCategoryLifecycle,
		RankReason: "grants Role vpa-updater to ServiceAccount kube-system/vpa-updater", Timestamp: now.Add(-time.Minute).Format(time.RFC3339)}}
	for _, name := range []string{"a", "b", "c"} {
		changes = append(changes, issuesapi.RecentChange{Kind: "ConfigMap", Name: name, ChangeType: "add", ChangeCategory: issuesapi.ChangeCategoryLifecycle,
			RankReason: lifecycleRankReason, Timestamp: now.Format(time.RFC3339)})
	}
	RankAndCap(&changes, 2)
	if changes[0].Name != "vpa-updater" {
		t.Fatalf("ranked = %+v, want the outside grant first", changes)
	}
}
