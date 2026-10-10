package issues

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestFinalizerOwnerObservation(t *testing.T) {
	t.Cleanup(k8s.ResetResourceCache)
	scopes := map[string]k8score.ResourceScope{"pods": {Enabled: true}, "deployments": {Enabled: true}, "statefulsets": {Enabled: true}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "release-widgets-operator", Namespace: "operators"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "widget-controller"}}}}
	stateful := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "custom", Namespace: "custom-vm", Labels: map[string]string{"app.kubernetes.io/name": "victoria-metrics-operator"}}, Spec: appsv1.StatefulSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "vm-controller"}}}}
	pods := []runtime.Object{
		deployment, stateful,
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "widget-pod", Namespace: "operators", Labels: map[string]string{"app": "widget-controller"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "vm-pod", Namespace: "custom-vm", Labels: map[string]string{"app": "vm-controller"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "finished", Namespace: "flux-system", Labels: map[string]string{"app": "helm-controller"}}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
	}
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(pods...), scopes); err != nil {
		t.Fatal(err)
	}
	p := NewCacheProvider()
	cases := []struct {
		name, finalizer, group, want string
		access                       func(Ref) bool
	}{
		{name: "catalog present custom namespace", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", want: "1 running pod(s) found matching custom in namespace custom-vm"},
		{name: "catalog absent", finalizer: "finalizers.kustomize.toolkit.fluxcd.io", group: "kustomize.toolkit.fluxcd.io", want: "no running pods found matching the controller for finalizers.kustomize.toolkit.fluxcd.io"},
		{name: "completed is not running", finalizer: "finalizers.helm.toolkit.fluxcd.io", group: "helm.toolkit.fluxcd.io", want: "no running pods found"},
		{name: "heuristic present", finalizer: "widgets.example.com/cleanup", group: "widgets.example.com", want: "1 running pod(s) found matching release-widgets-operator"},
		{name: "unknown", finalizer: "unknown.example.com/cleanup", group: "unknown.example.com", want: "controller unknown"},
		{name: "catalog unreadable", finalizer: "finalizers.kustomize.toolkit.fluxcd.io", group: "kustomize.toolkit.fluxcd.io", want: "controller unknown", access: func(Ref) bool { return false }},
		{name: "hidden heuristic workload", finalizer: "widgets.example.com/cleanup", group: "widgets.example.com", want: "controller unknown", access: func(r Ref) bool { return r.Namespace == "visible" }},
		{name: "hidden catalog custom workload", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", want: "controller unknown", access: func(r Ref) bool { return r.Namespace == "visible" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := p.FinalizerOwnerStatus(tt.finalizer, Ref{Group: tt.group, Kind: "Widget", Namespace: "visible"}, tt.access)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			if tt.name == "hidden heuristic workload" && strings.Contains(got, "release-widgets-operator") {
				t.Fatal("leaked workload")
			}
		})
	}
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), scopes); err != nil {
		t.Fatal(err)
	}
	p = NewCacheProvider()
	if got := p.FinalizerOwnerStatus("apps.victoriametrics.com/finalizer", Ref{Group: "operator.victoriametrics.com"}, nil); !strings.Contains(got, "no running pods found") || !strings.Contains(got, "across the watched cluster") {
		t.Fatal(got)
	}
	if got := p.FinalizerOwnerStatus("apps.victoriametrics.com/finalizer", Ref{Group: "operator.victoriametrics.com"}, func(r Ref) bool { return r.Namespace == "visible" }); !strings.Contains(got, "controller unknown") {
		t.Fatal(got)
	}
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "visible"}}); err != nil {
		t.Fatal(err)
	}
	p = NewCacheProvider()
	if got := p.FinalizerOwnerStatus("finalizers.helm.toolkit.fluxcd.io", Ref{Group: "helm.toolkit.fluxcd.io"}, nil); !strings.Contains(got, "controller unknown") {
		t.Fatal(got)
	}
}

func TestIssuesDynamicTerminationAuthorization(t *testing.T) {
	t.Cleanup(k8s.ResetResourceCache)
	t.Cleanup(k8s.ResetTestDynamicState)
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	gvr := schema.GroupVersionResource{Group: "widgets.example.com", Version: "v1", Resource: "widgets"}
	cluster := schema.GroupVersionResource{Group: gvr.Group, Version: "v1", Resource: "clusterwidgets"}
	makeWidget := func(kind, ns, name string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetAPIVersion(gvr.Group + "/v1")
		u.SetKind(kind)
		u.SetName(name)
		u.SetNamespace(ns)
		u.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-24 * time.Hour)))
		u.SetDeletionTimestamp(&metav1.Time{Time: time.Now().Add(-2 * time.Hour)})
		u.SetFinalizers([]string{"widgets.example.com/cleanup"})
		return u
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList", cluster: "ClusterWidgetList"}, makeWidget("Widget", "visible", "public"), makeWidget("Widget", "hidden", "secret"), makeWidget("ClusterWidget", "", "global"))
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{
		{Group: gvr.Group, Version: "v1", Kind: "Widget", Name: gvr.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}},
		{Group: gvr.Group, Version: "v1", Kind: "ClusterWidget", Name: cluster.Resource, IsCRD: true, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	dc := k8s.GetDynamicResourceCache()
	for _, resource := range []schema.GroupVersionResource{gvr, cluster} {
		if err := dc.EnsureWatching(resource); err != nil {
			t.Fatal(err)
		}
		if !dc.WaitForSync(resource, 2*time.Second) {
			t.Fatal("not synced")
		}
	}
	p := NewCacheProvider()
	scoped := Compose(p, Filters{Namespaces: []string{"visible"}, Grouped: true, CanReadRelated: func(r Ref) bool { return r.Namespace == "visible" }})
	if len(scoped) != 1 || scoped[0].Name != "public" || scoped[0].Category != issuesapi.CategoryTerminationStuck || scoped[0].Severity != SeverityCritical {
		t.Fatalf("scoped=%+v", scoped)
	}
	if !strings.Contains(scoped[0].Cause, "controller unknown") || !strings.Contains(scoped[0].Action, "patch_resource") {
		t.Fatalf("missing guidance: %+v", scoped[0])
	}
	denied := Compose(p, Filters{CanReadClusterScoped: func(string, string) bool { return false }})
	for _, issue := range denied {
		if issue.Namespace == "" {
			t.Fatalf("cluster denial leaked: %+v", issue)
		}
	}
	all := Compose(p, Filters{Grouped: true})
	if len(all) != 3 {
		t.Fatalf("all=%+v", all)
	}
}
