package k8s

import (
	"context"
	"reflect"
	"testing"

	"github.com/skyhook-io/radar/internal/timeline"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func testRoleBinding(subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "vpa-updater", Namespace: "saleor", UID: "rb-1"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "vpa-updater"},
		Subjects:   subjects,
	}
}

var vpaUpdaterSA = rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: "kube-system", Name: "vpa-updater"}

func TestDiffRBACBindingSubjects(t *testing.T) {
	oldRB := testRoleBinding(rbacv1.Subject{Kind: rbacv1.UserKind, Name: "alice"})
	newRB := testRoleBinding(rbacv1.Subject{Kind: rbacv1.UserKind, Name: "alice"}, vpaUpdaterSA)
	diff := ComputeDiff("RoleBinding", oldRB, newRB)
	if diff == nil || len(diff.Fields) != 1 {
		t.Fatalf("diff = %+v, want one subjects field", diff)
	}
	got := diff.Fields[0]
	if got.Path != "subjects" ||
		!reflect.DeepEqual(got.OldValue, []string{"User alice"}) ||
		!reflect.DeepEqual(got.NewValue, []string{"ServiceAccount kube-system/vpa-updater", "User alice"}) {
		t.Fatalf("subjects field = %+v", got)
	}

	// Metadata-only writes record nothing; the kind has a differ, so the
	// timeline drops the update.
	relabeled := newRB.DeepCopy()
	relabeled.Labels = map[string]string{"team": "x"}
	if diff := ComputeDiff("RoleBinding", newRB, relabeled); diff != nil {
		t.Fatalf("metadata-only diff = %+v, want nil", diff)
	}
}

// A RoleBinding ServiceAccount subject without a namespace is in the
// binding's own namespace, so it is not an outside actor.
func TestRBACSubjectWithoutNamespaceIsTheBindings(t *testing.T) {
	rb := testRoleBinding(rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "default"})
	subjects, _, _ := rbacBindingFields(rb)
	if want := []string{"ServiceAccount saleor/default"}; !reflect.DeepEqual(subjects, want) {
		t.Fatalf("subjects = %v, want %v", subjects, want)
	}
	if outside := OutsideServiceAccountSubjects("RoleBinding", "saleor", subjects); len(outside) != 0 {
		t.Fatalf("outside = %v, want none", outside)
	}
}

func TestDiffRBACRoleRules(t *testing.T) {
	oldRole := &rbacv1.ClusterRole{Rules: []rbacv1.PolicyRule{{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get"}}}}
	newRole := oldRole.DeepCopy()
	newRole.Rules = append(newRole.Rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{"db"}, Verbs: []string{"get", "list"}})
	newRole.AggregationRule = &rbacv1.AggregationRule{ClusterRoleSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"rbac.example.com/aggregate-to-edit": "true"}}}}
	diff := ComputeDiff("ClusterRole", oldRole, newRole)
	if diff == nil || len(diff.Fields) != 2 {
		t.Fatalf("diff = %+v, want rules and aggregationRule", diff)
	}
	// Only the changed rules, not the whole list.
	if want := []string{"core/secrets[db]: get,list"}; diff.Fields[0].Path != "rules (changed entries)" || len(diff.Fields[0].OldValue.([]string)) != 0 || !reflect.DeepEqual(diff.Fields[0].NewValue, want) {
		t.Fatalf("rules = %+v, want added %v", diff.Fields[0], want)
	}
	if want := []string{"rbac.example.com/aggregate-to-edit=true"}; diff.Fields[1].Path != "aggregationRule" || !reflect.DeepEqual(diff.Fields[1].NewValue, want) {
		t.Fatalf("aggregationRule = %+v", diff.Fields[1])
	}
}

func TestDiffRBACRoleIgnoresOrderAndReadsSelectorExpressions(t *testing.T) {
	oldRole := &rbacv1.ClusterRole{Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{"apps"}, Resources: []string{"deployments", "statefulsets"}, Verbs: []string{"get", "list"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}},
	}}
	reordered := oldRole.DeepCopy()
	reordered.Rules[0], reordered.Rules[1] = reordered.Rules[1], reordered.Rules[0]
	reordered.Rules[1].Verbs = []string{"list", "get"}
	reordered.Rules[1].Resources = []string{"statefulsets", "deployments"}
	if diff := ComputeDiff("ClusterRole", oldRole, reordered); diff != nil {
		t.Fatalf("reorder-only diff = %+v, want nil", diff)
	}
	withAgg := oldRole.DeepCopy()
	withAgg.AggregationRule = &rbacv1.AggregationRule{ClusterRoleSelectors: []metav1.LabelSelector{{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"a"}}}}}}
	changed := withAgg.DeepCopy()
	changed.AggregationRule.ClusterRoleSelectors[0].MatchExpressions[0].Values = []string{"b"}
	if diff := ComputeDiff("ClusterRole", withAgg, changed); diff == nil || diff.Fields[0].Path != "aggregationRule" {
		t.Fatalf("matchExpressions edit diff = %+v", diff)
	}
}

func TestDiffRBACUnstructured(t *testing.T) {
	toU := func(rb *rbacv1.RoleBinding) *unstructured.Unstructured {
		obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(rb)
		if err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{Object: obj}
		u.SetAPIVersion("rbac.authorization.k8s.io/v1")
		u.SetKind("RoleBinding")
		return u
	}
	diff := ComputeDiff("RoleBinding", toU(testRoleBinding()), toU(testRoleBinding(vpaUpdaterSA)))
	if diff == nil || diff.Fields[0].Path != "subjects" {
		t.Fatalf("unstructured diff = %+v", diff)
	}
}

func TestRecordBindingDeleteKeepsWhatItGranted(t *testing.T) {
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(timeline.ResetStore)

	recordToTimelineStore(ActiveClusterContext(), "RoleBinding", "saleor", "vpa-updater", "rb-1", "delete", testRoleBinding(vpaUpdaterSA), nil, nil, false)
	events, err := timeline.GetStore().Query(context.Background(), timeline.QueryOptions{Kinds: []string{"RoleBinding"}})
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %+v, err = %v", events, err)
	}
	diff := events[0].Diff
	if diff == nil || len(diff.Fields) != 2 {
		t.Fatalf("delete diff = %+v, want subjects and roleRef", diff)
	}
	if !reflect.DeepEqual(diff.Fields[0].OldValue, []string{"ServiceAccount kube-system/vpa-updater"}) || diff.Fields[1].OldValue != "Role vpa-updater" {
		t.Fatalf("delete diff fields = %+v", diff.Fields)
	}
	if diff.Summary != "deleted; it bound Role vpa-updater" {
		t.Fatalf("summary = %q", diff.Summary)
	}

	// A create records its grant only when it reaches outside the namespace,
	// so ordinary creates stay plain.
	prevSynced := initialSyncComplete.Load()
	initialSyncComplete.Store(true)
	t.Cleanup(func() { initialSyncComplete.Store(prevSynced) })
	created := func(name string, subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
		rb := testRoleBinding(subjects...)
		rb.Name, rb.CreationTimestamp = name, metav1.Now()
		return rb
	}
	recordToTimelineStore(ActiveClusterContext(), "RoleBinding", "saleor", "vpa-updater", "rb-2", "add", nil, created("vpa-updater", vpaUpdaterSA), nil, false)
	recordToTimelineStore(ActiveClusterContext(), "RoleBinding", "saleor", "local", "rb-3", "add", nil, created("local", rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: "saleor", Name: "app"}), nil, false)
	events, _ = timeline.GetStore().Query(context.Background(), timeline.QueryOptions{Kinds: []string{"RoleBinding"}, EventTypes: []timeline.EventType{timeline.EventTypeAdd}})
	if len(events) != 2 {
		t.Fatalf("creates = %+v", events)
	}
	for _, e := range events {
		switch e.Name {
		case "vpa-updater":
			if e.Diff == nil || !reflect.DeepEqual(e.Diff.Fields[0].NewValue, []string{"ServiceAccount kube-system/vpa-updater"}) || e.Diff.Fields[1].NewValue != "Role vpa-updater" {
				t.Errorf("outside-actor create diff = %+v", e.Diff)
			}
		case "local":
			if e.Diff != nil {
				t.Errorf("in-namespace create diff = %+v, want none", e.Diff)
			}
		}
	}

	// Other kinds' deletes stay content-free.
	recordToTimelineStore(ActiveClusterContext(), "Role", "saleor", "vpa-updater", "role-1", "delete", &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "vpa-updater", Namespace: "saleor"}}, nil, nil, false)
	events, _ = timeline.GetStore().Query(context.Background(), timeline.QueryOptions{Kinds: []string{"Role"}})
	if len(events) != 1 || events[0].Diff != nil {
		t.Fatalf("Role delete = %+v, want no diff", events)
	}
}
