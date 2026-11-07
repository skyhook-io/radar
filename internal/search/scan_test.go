package search

import (
	"context"
	"fmt"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/k8score"
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
