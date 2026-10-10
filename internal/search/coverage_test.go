package search

import (
	"context"
	"fmt"
	"testing"

	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestSearchCoverageAndNewKinds(t *testing.T) {
	for _, tk := range typedKinds {
		t.Run(tk.Kind, func(t *testing.T) {
			p := &fakeProvider{}
			res, err := Search(context.Background(), p, Parse("kind:"+tk.Kind), Options{SkipKinds: map[string]bool{tk.Kind: true}})
			if err != nil || !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Reason != "rbac_denied" || res.Searched != 0 {
				t.Fatalf("denial coverage: %+v %v", res, err)
			}
		})
	}
	p := &fakeProvider{typed: map[string][]runtime.Object{"rolebindings": {&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "binding", Namespace: "team-a"}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "worker"}}}}}}
	f, err := filter.CompileObjectFilter(`has(object.subjects) && object.subjects.exists(s, s.name == "worker")`)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Search(context.Background(), p, Parse("kind:RoleBinding"), Options{Filter: f})
	if err != nil || len(res.Hits) != 1 || res.Partial {
		t.Fatalf("RBAC search: %+v %v", res, err)
	}
}

func TestSearchCoverageColdWarmAndUnknown(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationUnwatched}}}
	res, _ := Search(context.Background(), p, Parse(""), Options{})
	if !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Reason != "cold" || len(p.warmed) != 0 {
		t.Fatalf("cold: %+v warmed=%v", res, p.warmed)
	}
	res, _ = Search(context.Background(), p, Parse("kind:Widget"), Options{})
	if res.Partial || len(p.warmed) != 1 {
		t.Fatalf("warm: %+v warmed=%v", res, p.warmed)
	}
	res, _ = Search(context.Background(), p, Parse("kind:DoesNotExist"), Options{})
	if !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Reason != "not_indexed" {
		t.Fatalf("unknown: %+v", res)
	}
}

func TestSearchFilterFailuresAreBoundedAndSecretSafe(t *testing.T) {
	pods := make([]runtime.Object, 25)
	for i := range pods {
		pods[i] = newPod("team-a", fmt.Sprintf("p-%02d", i), "image", nil)
	}
	p := &fakeProvider{typed: map[string][]runtime.Object{"pods": pods}}
	f, _ := filter.CompileObjectFilter(`object.nonexistent == 1`)
	res, _ := Search(context.Background(), p, Parse("kind:Pod"), Options{Filter: f})
	if !res.Partial || res.FilterErrors != 25 || len(res.FilterFailedObjects) != 20 || res.FilterErrorSample == "" {
		t.Fatalf("failed refs: %+v", res)
	}
	p.typed = map[string][]runtime.Object{"secrets": {&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "team-a"}, Data: map[string][]byte{"token": []byte("sensitive")}, StringData: map[string]string{"other": "sensitive"}}}}
	f, _ = filter.CompileObjectFilter(`!has(object.data) && !has(object.stringData) && metadata.namespace == object.metadata.namespace`)
	res, _ = Search(context.Background(), p, Parse("kind:Secret"), Options{Filter: f})
	if res.Partial || len(res.Hits) != 1 {
		t.Fatalf("Secret binding: %+v", res)
	}
}

func TestSearchCoverageReadFailuresAndPartialNamespaces(t *testing.T) {
	for _, reason := range []string{"syncing", "sync_failed", "cold", "namespace_scope"} {
		t.Run(reason, func(t *testing.T) {
			p := &fakeProvider{typedReasons: map[string]string{"Pod": reason}, typed: map[string][]runtime.Object{"pods": {newPod("a", "visible", "x", nil)}}}
			res, _ := Search(context.Background(), p, Parse("kind:Pod"), Options{})
			if !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Reason != reason {
				t.Fatalf("read coverage: %+v", res)
			}
			if reason != "namespace_scope" && len(res.Hits) != 0 {
				t.Fatalf("incomplete cache served rows: %+v", res)
			}
		})
	}
	for _, tc := range []struct {
		err    error
		reason string
	}{{fmt.Errorf("forbidden: pods"), "sa_forbidden"}, {fmt.Errorf("broken list"), "list_error"}} {
		p := &fakeProvider{listErrors: map[string]error{"pods": tc.err}}
		res, _ := Search(context.Background(), p, Parse("kind:Pod"), Options{})
		if !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Reason != tc.reason {
			t.Fatalf("list failure: %+v", res)
		}
	}
	rec := &recordingProvider{fakeProvider: &fakeProvider{}}
	res, _ := Search(context.Background(), rec, Parse("kind:Role"), Options{Namespaces: []string{"a", "b"}, NamespacesByKind: map[string][]string{"Role": {"a"}}})
	if !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Reason != "rbac_denied" || len(rec.listedWith["roles"]) != 1 || rec.listedWith["roles"][0] != "a" {
		t.Fatalf("namespace narrowing: %+v lists=%v", res, rec.listedWith)
	}
	res, _ = Search(context.Background(), rec, Parse("kind:Role"), Options{NamespacesByKind: map[string][]string{"Role": {}}})
	if !res.Partial || res.Searched != 0 {
		t.Fatalf("empty override: %+v", res)
	}
}

func TestSearchDynamicCoverageDoesNotWarmDeniedAndRetainsGoodNamespaces(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationUnwatched}}}
	res, _ := Search(context.Background(), p, Parse("kind:Widget"), Options{CanReadNamespaced: func(kind, group, resource, ns string) bool { return false }})
	if !res.Partial || res.Unsearched[0].Reason != "rbac_denied" || len(p.warmed) != 0 || len(p.dynamicListNamespaces) != 0 {
		t.Fatalf("denied kind warmed or listed: %+v %+v", res, p)
	}
	for _, tc := range []struct {
		state  k8score.DynamicObservationState
		reason string
	}{{k8score.DynamicObservationDenied, "sa_forbidden"}, {k8score.DynamicObservationSyncing, "syncing"}, {k8score.DynamicObservationUnsupported, "not_indexed"}, {k8score.DynamicObservationDeferred, "cold"}} {
		p.observations[gvr] = k8score.DynamicResourceObservation{State: tc.state}
		res, _ = Search(context.Background(), p, Parse(""), Options{})
		if !res.Partial || res.Unsearched[0].Reason != tc.reason {
			t.Fatalf("dynamic state %s: %+v", tc.state, res)
		}
	}
	p.observations[gvr] = k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced}
	p.dynamicErrors = map[string]error{"a": fmt.Errorf("broken list")}
	res, _ = Search(context.Background(), p, Parse("kind:Widget"), Options{Namespaces: []string{"a", "b"}})
	if !res.Partial || len(p.dynamicListNamespaces) != 2 || res.Unsearched[0].Reason != "list_error" {
		t.Fatalf("dynamic partial list: %+v lists=%v", res, p.dynamicListNamespaces)
	}
}

func TestSearchObjectClusterNamespaceAndGuardedArgo(t *testing.T) {
	p := &fakeProvider{typed: map[string][]runtime.Object{"clusterroles": {&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "reader"}}}}}
	f, _ := filter.CompileObjectFilter(`metadata.namespace == "" && object.metadata.namespace == "" && !("app" in labels)`)
	res, _ := Search(context.Background(), p, Parse("kind:ClusterRole"), Options{Filter: f})
	if res.Partial || len(res.Hits) != 1 {
		t.Fatalf("cluster namespace: %+v", res)
	}
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	p = &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "Application"}, dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {
		{Object: map[string]any{"kind": "Application", "metadata": map[string]any{"name": "single"}, "spec": map[string]any{"source": map[string]any{"repoURL": "https://github.com/one"}}}},
		{Object: map[string]any{"kind": "Application", "metadata": map[string]any{"name": "multi"}, "spec": map[string]any{"sources": []any{map[string]any{"repoURL": "https://github.com/two"}}}}},
	}}}
	f, err := filter.CompileObjectFilter(`kind == "Application" && has(spec.sources) && spec.sources.exists(s, has(s.repoURL) && s.repoURL.contains("github.com"))`)
	if err != nil {
		t.Fatal(err)
	}
	res, _ = Search(context.Background(), p, Parse("kind:Application"), Options{Filter: f})
	if res.Partial || len(res.Hits) != 1 || res.Hits[0].Name != "multi" {
		t.Fatalf("Argo guard: %+v", res)
	}
}
