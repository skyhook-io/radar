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
	"k8s.io/apimachinery/pkg/types"
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
	if len(scoped) != 1 || scoped[0].Name != "public" || scoped[0].Category != issuesapi.CategoryTerminationStuck || scoped[0].Severity != SeverityWarning {
		t.Fatalf("scoped=%+v", scoped)
	}
	if !strings.Contains(scoped[0].Cause, "external cleanup pending") || !strings.Contains(scoped[0].Cause, "controller unknown") || strings.Contains(scoped[0].Action, "patch_resource") || !strings.Contains(scoped[0].Action, "couldn't identify") {
		t.Fatalf("unknown controller guidance: %+v", scoped[0])
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
	const preview = `For "crossplane.io/cleanup", preview with patch_resource {}`
	for _, tc := range []struct {
		name         string
		finalizers   []string
		observation  finalizerObservation
		wantSeverity Severity
		wantAction   string
		wantPatch    bool
		wantCalls    int
	}{
		{"healthy infrastructure", []string{"crossplane.io/cleanup"}, finalizerObservation{Text: "crossplane is healthy (1 pod ready)", Controller: "crossplane", Running: true, Healthy: true}, SeverityWarning, "Check its logs", false, 1},
		{"identified crashloop", []string{"crossplane.io/cleanup"}, finalizerObservation{Text: "crossplane is CrashLoopBackOff (1/1 pods)", Controller: "crossplane", Stopped: true}, SeverityCritical, "intentionally removed", true, 1},
		{"identified absent", []string{"crossplane.io/cleanup"}, finalizerObservation{Text: "crossplane is not running", Controller: "crossplane", Stopped: true}, SeverityCritical, "intentionally removed", true, 1},
		{"stopped controller that releases infrastructure", []string{"crossplane.io/cleanup"}, finalizerObservation{Text: "crossplane is CrashLoopBackOff (1/1 pods)", Controller: "crossplane", Stopped: true, ReleasesInfrastructure: true}, SeverityCritical, "Get crossplane running again", false, 1},
		{"identified but unconfirmed", []string{"crossplane.io/cleanup"}, finalizerObservation{Text: "crossplane has no pods; it may run outside the cluster", Controller: "crossplane"}, SeverityWarning, "can't confirm", false, 1},
		{"unknown", []string{"crossplane.io/cleanup"}, finalizerObservation{Text: "controller unknown"}, SeverityWarning, "couldn't identify", false, 1},
		{"foreground only", []string{metav1.FinalizerDeleteDependents}, finalizerObservation{}, SeverityCritical, "dependents", false, 0},
		{"protection only", []string{"gateway-exists-finalizer.gateway.networking.k8s.io"}, finalizerObservation{}, SeverityCritical, "in-use guard", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &observingFinalizerProvider{observation: tc.observation}
			p.terminatingProblems = []k8s.Detection{{Kind: "Widget", Group: "crossplane.io", Namespace: "test", Name: "deleting", Reason: "Terminating stuck", Severity: "critical", TerminatingFinalizers: tc.finalizers, TerminatingRemovalPreviews: map[string]string{"crossplane.io/cleanup": preview}}}
			out := Compose(p, Filters{CanListResource: func(string, string, string) bool { return true }})
			if len(out) != 1 || out[0].Severity != tc.wantSeverity || !strings.Contains(out[0].Action, tc.wantAction) || p.calls != tc.wantCalls {
				t.Fatalf("out=%+v calls=%d", out, p.calls)
			}
			if got := strings.Contains(out[0].Action, "patch_resource"); got != tc.wantPatch {
				t.Fatalf("patch offered=%v, want %v: %s", got, tc.wantPatch, out[0].Action)
			}
			if !tc.wantPatch && !tc.observation.Running && tc.wantCalls > 0 && !strings.Contains(out[0].Action, "orphan external resources") {
				t.Fatalf("missing removal warning: %s", out[0].Action)
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

func TestFinalizerOwnerRemovalEvidence(t *testing.T) {
	t.Cleanup(k8s.ResetResourceCache)
	scopes := map[string]k8score.ResourceScope{"pods": {Enabled: true}, "deployments": {Enabled: true}, "statefulsets": {Enabled: true}}
	karpenter := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "karpenter", Namespace: "kube-system", Labels: map[string]string{"app.kubernetes.io/name": "karpenter"}}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "karpenter"}}}}
	crashing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "karpenter-1", Namespace: "kube-system", Labels: map[string]string{"app.kubernetes.io/name": "karpenter"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}}}
	appsDenied := func(group, _, namespace string) bool { return group != "apps" || namespace != "" }
	vmLabel := map[string]string{"app.kubernetes.io/name": "victoria-metrics-operator"}
	vmOperator := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "victoria-metrics-operator", Namespace: "monitoring", Labels: vmLabel}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: vmLabel}}}
	vmCrashing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "vm-operator-0", Namespace: "monitoring", Labels: vmLabel}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}}}
	for _, tc := range []struct {
		name, finalizer, group string
		objects                []runtime.Object
		access                 func(string, string, string) bool
		want                   string
		wantStopped            bool
		wantInfrastructure     bool
	}{
		{name: "catalog controller absent", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", want: "victoria-metrics-operator is not running across the watched cluster", wantStopped: true},
		{name: "absence without a cluster-wide workload search", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", access: appsDenied, want: "could not be searched cluster-wide"},
		{name: "managed karpenter", finalizer: "karpenter.sh/termination", group: "karpenter.sh", want: "karpenter has no pods across the watched cluster; it may run outside the cluster (EKS Auto Mode, AKS node auto-provisioning)", wantInfrastructure: true},
		{name: "managed karpenter node class", finalizer: "karpenter.k8s.aws/termination", group: "karpenter.k8s.aws", want: "may run outside the cluster", wantInfrastructure: true},
		{name: "self-hosted karpenter crashing", finalizer: "karpenter.sh/termination", group: "karpenter.sh", objects: []runtime.Object{karpenter, crashing}, want: "karpenter is CrashLoopBackOff (1/1 pods) in namespace kube-system", wantStopped: true, wantInfrastructure: true},
		{name: "managed argo cd", finalizer: "resources-finalizer.argocd.argoproj.io", group: "argoproj.io", want: "argocd-application-controller has no pods in namespace argocd; it may run outside the cluster (Amazon EKS Capabilities)"},
		{name: "flux controller absent", finalizer: "finalizers.kustomize.toolkit.fluxcd.io", group: "kustomize.toolkit.fluxcd.io", want: "kustomize-controller is not running in namespace flux-system", wantStopped: true},
		{name: "visible controller down without a cluster-wide workload search", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmOperator, vmCrashing}, access: appsDenied, want: "victoria-metrics-operator is CrashLoopBackOff (1/1 pods) in namespace monitoring, but Deployments and StatefulSets could not be searched cluster-wide"},
		{name: "visible controller down", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmOperator, vmCrashing}, want: "victoria-metrics-operator is CrashLoopBackOff (1/1 pods) in namespace monitoring", wantStopped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := k8s.InitScopedTestResourceCache(fake.NewClientset(tc.objects...), scopes); err != nil {
				t.Fatal(err)
			}
			observation := NewCacheProvider().FinalizerOwnerStatus(tc.finalizer, Ref{Group: tc.group, Kind: "Subject"}, tc.access)
			if !strings.Contains(observation.Text, tc.want) || observation.Stopped != tc.wantStopped || observation.ReleasesInfrastructure != tc.wantInfrastructure || observation.Controller == "" || observation.Running {
				t.Fatalf("observation=%+v", observation)
			}
		})
	}
}

// Real finalizer keys from infrastructure operators. Removing any of them while
// their controller may still be deleting cloud resources orphans that
// infrastructure, so only a controller Radar identified and saw stopped may get
// a ready-made removal.
func TestStuckDeletionRemovalNeedsStoppedController(t *testing.T) {
	t.Cleanup(k8s.ResetResourceCache)
	t.Cleanup(k8s.ResetTestDynamicState)
	type subject struct {
		gvr        schema.GroupVersionResource
		kind       string
		namespace  string
		finalizers []string
	}
	subjects := []subject{
		{schema.GroupVersionResource{Group: "ec2.aws.upbound.io", Version: "v1beta1", Resource: "instances"}, "Instance", "", []string{"finalizer.managedresource.crossplane.io"}},
		{schema.GroupVersionResource{Group: "platform.example.org", Version: "v1alpha1", Resource: "xdatabases"}, "XDatabase", "", []string{"composite.apiextensions.crossplane.io"}},
		{schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta1", Resource: "clusters"}, "Cluster", "capi", []string{"cluster.cluster.x-k8s.io"}},
		{schema.GroupVersionResource{Group: "sql.cnrm.cloud.google.com", Version: "v1beta1", Resource: "sqlinstances"}, "SQLInstance", "kcc", []string{"cnrm.cloud.google.com/finalizer", "cnrm.cloud.google.com/deletion-defender"}},
		{schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta1", Resource: "clusterqueues"}, "ClusterQueue", "", []string{"kueue.x-k8s.io/resource-in-use"}},
		{schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodeclaims"}, "NodeClaim", "", []string{"karpenter.sh/termination"}},
		{schema.GroupVersionResource{Group: "operator.victoriametrics.com", Version: "v1beta1", Resource: "vmagents"}, "VMAgent", "monitoring", []string{"apps.victoriametrics.com/finalizer"}},
	}
	compose := func(t *testing.T, typed ...runtime.Object) map[string]Issue {
		t.Helper()
		k8s.ResetTestDynamicState()
		if err := k8s.InitScopedTestResourceCache(fake.NewClientset(typed...), map[string]k8score.ResourceScope{"pods": {Enabled: true}, "deployments": {Enabled: true}, "statefulsets": {Enabled: true}}); err != nil {
			t.Fatal(err)
		}
		listKinds := map[schema.GroupVersionResource]string{}
		var resources []k8s.APIResource
		var objects []runtime.Object
		for _, s := range subjects {
			listKinds[s.gvr] = s.kind + "List"
			resources = append(resources, k8s.APIResource{Group: s.gvr.Group, Version: s.gvr.Version, Kind: s.kind, Name: s.gvr.Resource, Namespaced: s.namespace != "", IsCRD: true, Verbs: []string{"list", "watch"}})
			u := &unstructured.Unstructured{}
			u.SetAPIVersion(s.gvr.GroupVersion().String())
			u.SetKind(s.kind)
			u.SetName("stuck")
			u.SetNamespace(s.namespace)
			u.SetUID(types.UID("uid-" + s.kind))
			u.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-24 * time.Hour)))
			u.SetDeletionTimestamp(&metav1.Time{Time: time.Now().Add(-2 * time.Hour)})
			u.SetFinalizers(s.finalizers)
			objects = append(objects, u)
		}
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objects...)
		if err := k8s.InitTestDynamicResourceCache(client, resources); err != nil {
			t.Fatal(err)
		}
		dc := k8s.GetDynamicResourceCache()
		for _, s := range subjects {
			if err := dc.EnsureWatching(s.gvr); err != nil {
				t.Fatal(err)
			}
			if !dc.WaitForSync(s.gvr, 2*time.Second) {
				t.Fatalf("%s did not sync", s.gvr)
			}
		}
		out := map[string]Issue{}
		for _, issue := range Compose(NewCacheProvider(), Filters{CanListResource: func(string, string, string) bool { return true }}) {
			if issue.Category == issuesapi.CategoryTerminationStuck {
				if _, dup := out[issue.Kind]; dup {
					t.Fatalf("duplicate %s row", issue.Kind)
				}
				out[issue.Kind] = issue
			}
		}
		if len(out) != len(subjects) {
			t.Fatalf("rows=%+v", out)
		}
		return out
	}
	offersRemoval := func(issue Issue) bool {
		return strings.Contains(issue.Action, "patch_resource") || strings.Contains(issue.Action, "kubectl patch")
	}

	controllersAbsent := compose(t)
	if action := controllersAbsent["SQLInstance"].Action; strings.Count(action, "couldn't identify") != 1 || !strings.Contains(action, `finalizers "cnrm.cloud.google.com/finalizer", "cnrm.cloud.google.com/deletion-defender"`) {
		t.Errorf("Config Connector finalizers should share one sentence: %s", action)
	}
	for _, kind := range []string{"Instance", "XDatabase", "Cluster", "SQLInstance"} {
		issue := controllersAbsent[kind]
		if offersRemoval(issue) || issue.Severity != SeverityWarning || !strings.Contains(issue.Action, "couldn't identify which controller owns") || !strings.Contains(issue.Action, "orphan external resources") {
			t.Errorf("%s with an unidentified controller: severity=%s action=%s", kind, issue.Severity, issue.Action)
		}
	}
	if issue := controllersAbsent["ClusterQueue"]; offersRemoval(issue) || !strings.Contains(issue.Action, "in-use guard") {
		t.Errorf("Kueue in-use guard: %s", issue.Action)
	}
	if issue := controllersAbsent["NodeClaim"]; offersRemoval(issue) || issue.Severity != SeverityWarning || !strings.Contains(issue.Cause, "may run outside the cluster (EKS Auto Mode, AKS node auto-provisioning)") || !strings.Contains(issue.Action, "can't confirm") {
		t.Errorf("managed Karpenter: severity=%s cause=%s action=%s", issue.Severity, issue.Cause, issue.Action)
	}
	vm := controllersAbsent["VMAgent"]
	if !offersRemoval(vm) || vm.Severity != SeverityCritical || !strings.Contains(vm.Cause, "victoria-metrics-operator is not running") || !strings.Contains(vm.Action, `"name":"stuck"`) {
		t.Errorf("uninstalled VictoriaMetrics operator: severity=%s cause=%s action=%s", vm.Severity, vm.Cause, vm.Action)
	}

	label := map[string]string{"app.kubernetes.io/name": "victoria-metrics-operator"}
	vmOperator := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "vm-victoria-metrics-operator", Namespace: "monitoring", Labels: label}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: label}}}
	vmPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "vm-operator-0", Namespace: "monitoring", Labels: label}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}}
	karpenterLabel := map[string]string{"app.kubernetes.io/name": "karpenter"}
	karpenter := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "karpenter", Namespace: "kube-system", Labels: karpenterLabel}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: karpenterLabel}}}
	karpenterPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "karpenter-0", Namespace: "kube-system", Labels: karpenterLabel}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: false}}}}

	controllersPresent := compose(t, vmOperator, vmPod, karpenter, karpenterPod)
	if vm := controllersPresent["VMAgent"]; offersRemoval(vm) || vm.Severity != SeverityWarning || !strings.Contains(vm.Action, "controller is running") {
		t.Errorf("healthy VictoriaMetrics operator: severity=%s action=%s", vm.Severity, vm.Action)
	}
	if issue := controllersPresent["NodeClaim"]; offersRemoval(issue) || issue.Severity != SeverityCritical || !strings.Contains(issue.Cause, "karpenter is degraded (0/1 pods ready)") || !strings.Contains(issue.Action, "Get karpenter running again") {
		t.Errorf("self-hosted Karpenter not ready: severity=%s cause=%s action=%s", issue.Severity, issue.Cause, issue.Action)
	}
}
