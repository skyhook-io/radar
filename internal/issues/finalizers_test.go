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
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "deleting", Namespace: "flux-system", Labels: map[string]string{"app": "helm-controller"}, DeletionTimestamp: &metav1.Time{Time: time.Now()}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}},
	}
	for _, tc := range []struct {
		token  string
		phase  corev1.PodPhase
		status corev1.ContainerStatus
	}{
		{"crashes", corev1.PodRunning, corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}},
		{"pending", corev1.PodPending, corev1.ContainerStatus{}},
		{"degraded", corev1.PodRunning, corev1.ContainerStatus{Ready: false}},
	} {
		pods = append(pods, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: tc.token + "-operator", Namespace: "operators"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": tc.token}}}},
			&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: tc.token, Namespace: "operators", Labels: map[string]string{"app": tc.token}}, Status: corev1.PodStatus{Phase: tc.phase, ContainerStatuses: []corev1.ContainerStatus{tc.status}}})
	}
	pods = append(pods, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "karpenter", Namespace: "operators"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "karpenter"}}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "karpenter", Namespace: "operators", Labels: map[string]string{"app": "karpenter"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}})
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(pods...), scopes); err != nil {
		t.Fatal(err)
	}
	p := NewCacheProvider()
	cases := []struct {
		name, finalizer, group, want string
		access                       func(string, string, string) bool
	}{
		{name: "catalog present custom namespace", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", want: "custom is healthy (1 pod ready) in namespace custom-vm"},
		{name: "catalog absent", finalizer: "finalizers.kustomize.toolkit.fluxcd.io", group: "kustomize.toolkit.fluxcd.io", want: "kustomize-controller is not running"},
		{name: "completed and deleting are not running", finalizer: "finalizers.helm.toolkit.fluxcd.io", group: "helm.toolkit.fluxcd.io", want: "is not running"},
		{name: "heuristic present", finalizer: "widgets.example.com/cleanup", group: "widgets.example.com", want: "release-widgets-operator is healthy (1 pod ready)"},
		{name: "crashloop is not running", finalizer: "crashes.example.com/cleanup", group: "crashes.example.com", want: "crashes-operator is CrashLoopBackOff (1/1 pods)"},
		{name: "pending", finalizer: "pending.example.com/cleanup", group: "pending.example.com", want: "pending-operator is pending start"},
		{name: "degraded", finalizer: "degraded.example.com/cleanup", group: "degraded.example.com", want: "degraded-operator is degraded (0/1 pods ready)"},
		{name: "karpenter", finalizer: "karpenter.sh/termination", group: "karpenter.sh", want: "karpenter is healthy (1 pod ready)"},
		{name: "karpenter AWS provider", finalizer: "karpenter.k8s.aws/termination", group: "karpenter.k8s.aws", want: "karpenter is healthy (1 pod ready)"},
		{name: "karpenter Azure provider", finalizer: "karpenter.azure.com/termination", group: "karpenter.azure.com", want: "karpenter is healthy (1 pod ready)"},
		{name: "workloads readable pods denied", finalizer: "widgets.example.com/cleanup", group: "widgets.example.com", want: "controller unknown (controller pod inventory is unreadable)", access: func(_, resource, _ string) bool { return resource != "pods" }},
		{name: "pods readable workloads denied", finalizer: "widgets.example.com/cleanup", group: "widgets.example.com", want: "controller unknown", access: func(_, resource, _ string) bool { return resource == "pods" }},
		{name: "unknown", finalizer: "unknown.example.com/cleanup", group: "unknown.example.com", want: "controller unknown"},
		{name: "catalog unreadable", finalizer: "finalizers.kustomize.toolkit.fluxcd.io", group: "kustomize.toolkit.fluxcd.io", want: "controller unknown", access: func(string, string, string) bool { return false }},
		{name: "hidden heuristic workload", finalizer: "widgets.example.com/cleanup", group: "widgets.example.com", want: "controller unknown", access: func(_, _, namespace string) bool { return namespace == "visible" }},
		{name: "hidden catalog custom workload", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", want: "controller unknown", access: func(_, _, namespace string) bool { return namespace == "visible" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			observation := p.FinalizerOwnerStatus(tt.finalizer, Ref{Group: tt.group, Kind: "Widget", Namespace: "visible"}, tt.access)
			got := observation.Text
			if (tt.name == "crashloop is not running" || tt.name == "pending" || tt.name == "degraded") && observation.Running {
				t.Fatal("unhealthy controller marked running")
			}
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
	if got := p.FinalizerOwnerStatus("apps.victoriametrics.com/finalizer", Ref{Group: "operator.victoriametrics.com"}, nil).Text; !strings.Contains(got, "is not running") || !strings.Contains(got, "across the watched cluster") {
		t.Fatal(got)
	}
	if got := p.FinalizerOwnerStatus("apps.victoriametrics.com/finalizer", Ref{Group: "operator.victoriametrics.com"}, func(_, _, namespace string) bool { return namespace == "visible" }).Text; !strings.Contains(got, "controller unknown") {
		t.Fatal(got)
	}
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "visible"}}); err != nil {
		t.Fatal(err)
	}
	p = NewCacheProvider()
	if got := p.FinalizerOwnerStatus("finalizers.helm.toolkit.fluxcd.io", Ref{Group: "helm.toolkit.fluxcd.io"}, nil).Text; !strings.Contains(got, "controller unknown") {
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
		u.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "Deleting", "message": "external cleanup pending"}}}
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
	scoped := Compose(p, Filters{Namespaces: []string{"visible"}, Grouped: true, CanReadRelated: func(r Ref) bool { return r.Namespace == "visible" }, CanListResource: func(_, _, namespace string) bool { return namespace == "visible" }})
	if len(scoped) != 1 || scoped[0].Name != "public" || scoped[0].Category != issuesapi.CategoryTerminationStuck || scoped[0].Severity != SeverityCritical {
		t.Fatalf("scoped=%+v", scoped)
	}
	if !strings.Contains(scoped[0].Cause, "external cleanup pending") || !strings.Contains(scoped[0].Cause, "controller unknown") || !strings.Contains(scoped[0].Action, "patch_resource") {
		t.Fatalf("missing guidance: %+v", scoped[0])
	}
	denied := Compose(p, Filters{CanReadClusterScoped: func(string, string) bool { return false }, CanListResource: func(_, _, namespace string) bool { return namespace != "" }})
	for _, issue := range denied {
		if issue.Namespace == "" {
			t.Fatalf("cluster denial leaked: %+v", issue)
		}
	}
	deniedKinds := Compose(p, Filters{Namespaces: []string{"visible", "hidden"}, CanListResource: func(string, string, string) bool { return false }})
	if len(deniedKinds) != 2 {
		t.Fatalf("existing deleting condition rows were hidden: %+v", deniedKinds)
	}
	for _, issue := range deniedKinds {
		if issue.Source != SourceCondition || issue.Reason != "Ready: Deleting" || issue.Category == issuesapi.CategoryTerminationStuck || strings.Contains(issue.Cause, "finalizer") {
			t.Fatalf("expected only existing condition evidence: %+v", issue)
		}
	}
	multi := p.DetectDynamicTerminatingProblems([]string{"visible", "hidden"}, nil)
	if len(multi) != 2 {
		t.Fatalf("multi-namespace scan: %+v", multi)
	}
	for _, namespace := range []string{"", "visible", "hidden"} {
		for _, detection := range k8s.DetectProblems(p.cache, namespace) {
			if detection.Group == gvr.Group {
				t.Fatalf("dashboard/MCP health detector included CR termination: %+v", detection)
			}
		}
	}
	if implicit := Compose(p, Filters{}); len(implicit) != 3 {
		t.Fatalf("expected only existing condition rows without a caller-bound CR inventory gate: %+v", implicit)
	}
	for _, issue := range Compose(p, Filters{}) {
		if issue.Category == issuesapi.CategoryTerminationStuck {
			t.Fatalf("implicit CR scan: %+v", issue)
		}
	}
	all := Compose(p, Filters{Grouped: true, CanListResource: func(string, string, string) bool { return true }})
	if len(all) != 3 {
		t.Fatalf("all=%+v", all)
	}
}

type observingFinalizerProvider struct {
	fakeProvider
	observation finalizerObservation
	calls       int
}

func (p *observingFinalizerProvider) FinalizerOwnerStatus(string, Ref, func(string, string, string) bool) finalizerObservation {
	p.calls++
	return p.observation
}

func TestTerminatingGuidanceAndNoise(t *testing.T) {
	for _, tc := range []struct {
		name         string
		finalizers   []string
		observation  finalizerObservation
		wantSeverity Severity
		wantAction   string
		wantCalls    int
	}{
		{"healthy infrastructure", []string{"crossplane.io/cleanup"}, finalizerObservation{Text: "crossplane is healthy (1 pod ready)", Running: true, Healthy: true}, SeverityWarning, "Check its logs", 1},
		{"crashloop", []string{"crossplane.io/cleanup"}, finalizerObservation{Text: "crossplane is CrashLoopBackOff (1/1 pods)"}, SeverityCritical, "patch_resource", 1},
		{"foreground only", []string{metav1.FinalizerDeleteDependents}, finalizerObservation{}, SeverityCritical, "dependents", 0},
		{"protection only", []string{"gateway-exists-finalizer.gateway.networking.k8s.io"}, finalizerObservation{}, SeverityCritical, "in-use guard", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &observingFinalizerProvider{observation: tc.observation}
			p.terminatingProblems = []k8s.Detection{{Kind: "Widget", Group: "crossplane.io", Namespace: "test", Name: "deleting", Reason: "Terminating stuck", Severity: "critical", Action: "in-use guard; patch_resource", TerminatingFinalizers: tc.finalizers}}
			out := Compose(p, Filters{CanListResource: func(string, string, string) bool { return true }})
			if len(out) != 1 || out[0].Severity != tc.wantSeverity || !strings.Contains(out[0].Action, tc.wantAction) || p.calls != tc.wantCalls {
				t.Fatalf("out=%+v calls=%d", out, p.calls)
			}
			if tc.observation.Running && strings.Contains(out[0].Action, "patch_resource") {
				t.Fatal("healthy controller offered rescue patch")
			}
			if tc.name == "foreground only" && !strings.Contains(out[0].Cause, "garbage collector is waiting for dependents") {
				t.Fatal(out[0].Cause)
			}
		})
	}
	p := &observingFinalizerProvider{observation: finalizerObservation{Text: "controller unknown"}}
	pr := k8s.Detection{TerminatingFinalizers: []string{"a/one", "b/two", "c/three", "d/four", "e/five"}}
	pr = enrichTerminatingProblem(pr, p, nil, map[string]finalizerObservation{})
	if strings.Count(pr.Cause, "controller unknown") != 3 || !strings.Contains(pr.Cause, "+2 more") {
		t.Fatal(pr.Cause)
	}
}

func TestFoldDeletingConditions(t *testing.T) {
	in := []Issue{
		{Group: "example.com", Kind: "Widget", Namespace: "test", Name: "deleting", Category: issuesapi.CategoryTerminationStuck},
		{Group: "example.com", Kind: "Widget", Namespace: "test", Name: "deleting", Source: SourceCondition, Reason: "Ready: Deleting", Message: "external cleanup pending"},
		{Group: "other.com", Kind: "Widget", Namespace: "test", Name: "deleting", Source: SourceCondition, Reason: "Ready: Deleting"},
		{Group: "example.com", Kind: "Widget", Namespace: "test", Name: "deleting", Source: SourceCondition, Reason: "Ready: Failed"},
	}
	out := foldDeletingConditions(in)
	if len(out) != 3 || !strings.Contains(out[0].Cause, "external cleanup pending") || out[1].Group != "other.com" || out[2].Reason != "Ready: Failed" {
		t.Fatalf("out=%+v", out)
	}
}

func TestFinalizerOwnerPartialReadiness(t *testing.T) {
	t.Cleanup(k8s.ResetResourceCache)
	for _, tc := range []struct {
		name   string
		second corev1.PodStatus
		want   string
	}{
		{"rollout", corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: false}}}, "degraded (1/2 pods ready)"},
		{"HA ready leader", corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}}, "CrashLoopBackOff (1/2 pods)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "widgets-operator", Namespace: "operators"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "widgets"}}}}
			leader := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "leader", Namespace: "operators", Labels: map[string]string{"app": "widgets"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}}
			second := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "operators", Labels: map[string]string{"app": "widgets"}}, Status: tc.second}
			if err := k8s.InitScopedTestResourceCache(fake.NewClientset(deployment, leader, second), map[string]k8score.ResourceScope{"pods": {Enabled: true}, "deployments": {Enabled: true}}); err != nil {
				t.Fatal(err)
			}
			p := NewCacheProvider()
			observation := p.FinalizerOwnerStatus("widgets.example.com/cleanup", Ref{Group: "widgets.example.com", Kind: "Widget"}, nil)
			if !observation.Running || !strings.Contains(observation.Text, tc.want) {
				t.Fatalf("partial ready controller observation: %+v", observation)
			}
			pr := k8s.Detection{Kind: "Widget", Group: "widgets.example.com", Severity: "critical", Action: "patch_resource", TerminatingFinalizers: []string{"widgets.example.com/cleanup"}}
			pr = enrichTerminatingProblem(pr, p, nil, map[string]finalizerObservation{})
			if pr.Severity != "critical" || strings.Contains(pr.Action, "patch_resource") || !strings.Contains(pr.Action, "Check its logs") {
				t.Fatalf("partial ready controller must suppress rescue without warning cap: %+v", pr)
			}
		})
	}
}
