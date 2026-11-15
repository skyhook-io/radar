package search

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
)

func scanned(t *testing.T, p Provider, access ScanAccess, skip func(kind, group string) bool) []string {
	t.Helper()
	got := []string{}
	if _, err := Scan(context.Background(), p, access, skip, func(o ScannedObject) {
		got = append(got, o.Kind+"/"+o.Object.GetNamespace()+"/"+o.Object.GetName())
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	return got
}

func TestScanNeverListsASkippedKind(t *testing.T) {
	p := &recordingProvider{fakeProvider: &fakeProvider{typed: map[string][]runtime.Object{
		"secrets":    {&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "cred"}}},
		"configmaps": {&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "cfg"}}},
	}}}
	got := scanned(t, p, ScanAccess{}, func(kind, _ string) bool { return kind == "Secret" })
	if !slices.Equal(got, []string{"ConfigMap/ns/cfg"}) {
		t.Fatalf("visited %v", got)
	}
	if _, listed := p.listedWith["secrets"]; listed {
		t.Fatalf("Secrets were listed: %v", p.listedWith)
	}
	if _, listed := p.listedWith["configmaps"]; !listed {
		t.Fatalf("the provider recorded no list at all: %v", p.listedWith)
	}
}

func TestScanAppliesTheCallersGates(t *testing.T) {
	p := &fakeProvider{typed: map[string][]runtime.Object{
		"configmaps":   {&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "mine", Name: "visible"}}, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "theirs", Name: "hidden"}}},
		"roles":        {&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: "mine", Name: "exact-gated"}}},
		"clusterroles": {&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "cluster-gated"}}},
	}}
	access := ScanAccess{
		Selection: readscope.Selection{Namespaces: []string{"mine"}},
		TypedKindAccess: func(namespaces []string, _, resource string) ([]string, []string, []string) {
			if resource == "roles" {
				return []string{}, namespaces, nil
			}
			return namespaces, nil, nil
		},
		CanReadClusterScoped: func(string, string, string) (bool, bool) { return false, true },
	}
	got := scanned(t, p, access, nil)
	if !slices.Equal(got, []string{"ConfigMap/mine/visible"}) {
		t.Fatalf("visited %v, want only what the caller may read", got)
	}
}

func TestScanHasNoResultLimit(t *testing.T) {
	configMaps := []runtime.Object{}
	for i := range MaxLimit + 50 {
		configMaps = append(configMaps, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: fmt.Sprintf("c-%d", i)}})
	}
	p := &fakeProvider{typed: map[string][]runtime.Object{"configmaps": configMaps}}
	if got := scanned(t, p, ScanAccess{}, nil); len(got) != MaxLimit+50 {
		t.Fatalf("visited %d", len(got))
	}
}

func TestScanVisitsWatchedDynamicKinds(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	p := &fakeProvider{
		kinds:      map[schema.GroupVersionResource]string{gvr: "Widget"},
		namespaced: map[schema.GroupVersionResource]bool{gvr: true},
		observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{
			gvr: {State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeCluster},
		},
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {
			{Object: map[string]any{"metadata": map[string]any{"namespace": "mine", "name": "seen"}}},
			{Object: map[string]any{"metadata": map[string]any{"namespace": "theirs", "name": "hidden"}}},
		}},
	}
	got := scanned(t, p, ScanAccess{Selection: readscope.Selection{Namespaces: []string{"mine"}}}, nil)
	if !slices.Equal(got, []string{"Widget/mine/seen"}) {
		t.Fatalf("visited %v", got)
	}
	if got := scanned(t, p, ScanAccess{Selection: readscope.Selection{Namespaces: []string{"mine"}}}, func(kind, _ string) bool { return kind == "Widget" }); len(got) != 0 {
		t.Fatalf("a skipped dynamic kind was visited: %v", got)
	}
}

// PermitsObject must agree with Scan about every object, so a status lookup
// gated by it never reaches an object Scan would withhold, under each gate.
func TestPermitsObjectAgreesWithScan(t *testing.T) {
	p := &fakeProvider{typed: map[string][]runtime.Object{
		"configmaps":   {&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "mine", Name: "visible"}}, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "theirs", Name: "hidden"}}},
		"deployments":  {&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "mine", Name: "app"}}, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "theirs", Name: "worker"}}},
		"roles":        {&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: "mine", Name: "denied"}}, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "allowed"}}},
		"secrets":      {&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "mine", Name: "unchecked"}}},
		"clusterroles": {&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "cluster-gated"}}},
		"nodes":        {&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}},
	}}
	objects := []struct{ kind, group, namespace, name string }{
		{"ConfigMap", "", "mine", "visible"},
		{"ConfigMap", "", "theirs", "hidden"},
		{"Deployment", "apps", "mine", "app"},
		{"Deployment", "apps", "theirs", "worker"},
		{"Role", "rbac.authorization.k8s.io", "mine", "denied"},
		{"Role", "rbac.authorization.k8s.io", "other", "allowed"},
		{"Secret", "", "mine", "unchecked"},
		{"ClusterRole", "rbac.authorization.k8s.io", "", "cluster-gated"},
		{"Node", "", "", "node-a"},
	}
	limited := ScanAccess{
		Selection: readscope.Selection{Namespaces: []string{"mine", "other"}},
		TypedKindAccess: func(namespaces []string, _, resource string) ([]string, []string, []string) {
			switch resource {
			case "roles":
				return slices.DeleteFunc(slices.Clone(namespaces), func(ns string) bool { return ns == "mine" }), []string{"mine"}, nil
			case "secrets":
				return []string{}, nil, namespaces
			}
			return namespaces, nil, nil
		},
		CanReadClusterScoped: func(_, _, resource string) (bool, bool) { return resource == "nodes", true },
	}
	for name, access := range map[string]ScanAccess{
		"limited caller": limited,
		"full access":    {},
		"check failed":   {NamespaceCheckFailed: true, CanReadClusterScoped: func(string, string, string) (bool, bool) { return false, false }},
	} {
		t.Run(name, func(t *testing.T) {
			permitted := []string{}
			for _, o := range objects {
				if ok, reason := access.PermitsObject(o.kind, o.group, o.namespace); ok {
					permitted = append(permitted, o.kind+"/"+o.namespace+"/"+o.name)
				} else if reason == "" {
					t.Errorf("%s/%s/%s refused without a reason", o.kind, o.namespace, o.name)
				}
			}
			slices.Sort(permitted)
			if got := scanned(t, p, access, nil); !slices.Equal(permitted, got) {
				t.Fatalf("PermitsObject allows %v, Scan visits %v", permitted, got)
			}
		})
	}
}

func TestPermitsObjectSaysWhy(t *testing.T) {
	access := ScanAccess{
		Selection: readscope.Selection{Namespaces: []string{"mine"}},
		TypedKindAccess: func(namespaces []string, _, resource string) ([]string, []string, []string) {
			if resource == "secrets" {
				return []string{}, nil, namespaces
			}
			return []string{}, namespaces, nil
		},
		CanReadClusterScoped: func(string, string, string) (bool, bool) { return false, false },
	}
	for _, tc := range []struct {
		kind, group, namespace string
		want                   readreason.Code
	}{
		{"Deployment", "apps", "theirs", readreason.UserNamespaceNotVisible},
		{"Role", "rbac.authorization.k8s.io", "mine", readreason.UserForbidden},
		{"Secret", "", "mine", readreason.UserPermissionCheckFailed},
		{"ClusterRole", "rbac.authorization.k8s.io", "", readreason.UserPermissionCheckFailed},
	} {
		if ok, reason := access.PermitsObject(tc.kind, tc.group, tc.namespace); ok || reason != tc.want {
			t.Errorf("%s in %q = %v, %q; want refused, %q", tc.kind, tc.namespace, ok, reason, tc.want)
		}
	}
	if ok, reason := (ScanAccess{NamespaceCheckFailed: true}).PermitsObject("Deployment", "apps", "mine"); ok || reason != readreason.UserPermissionCheckFailed {
		t.Errorf("failed namespace check = %v, %q", ok, reason)
	}
}

// Resolve checks each kind once, across all of its namespaces, however many
// objects share it, and PermitsObject answers on the result as it does on the
// access it came from.
func TestResolveChecksOncePerKindWithTheSameAnswers(t *testing.T) {
	var refs []ObjectRef
	for i := range 40 {
		ns := fmt.Sprintf("ns-%d", i%8)
		refs = append(refs,
			ObjectRef{"Deployment", "apps", ns},
			ObjectRef{"Secret", "", ns},
			ObjectRef{"Role", "rbac.authorization.k8s.io", ns},
			ObjectRef{"ClusterRole", "rbac.authorization.k8s.io", ""},
			ObjectRef{"Node", "", ""},
		)
	}
	refs = append(refs, ObjectRef{"Secret", "", "hidden"})
	typedCalls, clusterCalls := map[string]int{}, map[string]int{}
	access := ScanAccess{
		Selection: readscope.Selection{Namespaces: []string{"ns-0", "ns-1", "ns-2", "ns-3", "ns-4", "ns-5", "ns-6", "ns-7"}},
		TypedKindAccess: func(namespaces []string, _, resource string) (allowed, denied, unchecked []string) {
			typedCalls[resource]++
			for _, ns := range namespaces {
				switch {
				case resource == "secrets" && ns == "ns-1":
					unchecked = append(unchecked, ns)
				case resource == "roles" && ns == "ns-2":
					denied = append(denied, ns)
				default:
					allowed = append(allowed, ns)
				}
			}
			return allowed, denied, unchecked
		},
		CanReadClusterScoped: func(_, _, resource string) (bool, bool) {
			clusterCalls[resource]++
			return resource == "nodes", true
		},
	}
	resolved := access.Resolve(refs)
	if want := map[string]int{"secrets": 1, "roles": 1}; !maps.Equal(typedCalls, want) {
		t.Fatalf("exact-kind checks = %v, want one per kind", typedCalls)
	}
	if want := map[string]int{"clusterroles": 1, "nodes": 1}; !maps.Equal(clusterCalls, want) {
		t.Fatalf("cluster-scoped checks = %v, want one per kind", clusterCalls)
	}
	for _, ref := range refs {
		gotOK, gotReason := resolved.PermitsObject(ref.Kind, ref.Group, ref.Namespace)
		wantOK, wantReason := access.PermitsObject(ref.Kind, ref.Group, ref.Namespace)
		if gotOK != wantOK || gotReason != wantReason {
			t.Errorf("%s in %q = %v %q, want %v %q", ref.Kind, ref.Namespace, gotOK, gotReason, wantOK, wantReason)
		}
	}
	// The unresolved access checks once per object: 40 Secrets and 40 Roles
	// in visible namespaces.
	if want := map[string]int{"secrets": 1 + 40, "roles": 1 + 40}; !maps.Equal(typedCalls, want) {
		t.Fatalf("exact-kind checks after the per-object comparison = %v", typedCalls)
	}
}
