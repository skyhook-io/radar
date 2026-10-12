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
	scopes := map[string]k8score.ResourceScope{"pods": {Enabled: true}, "deployments": {Enabled: true}, "statefulsets": {Enabled: true}, "daemonsets": {Enabled: true}}
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
		{name: "catalog not found", finalizer: "finalizers.kustomize.toolkit.fluxcd.io", group: "kustomize.toolkit.fluxcd.io", want: "no recognized controller found: no workload or pod across the watched cluster matches kustomize-controller by name, labels or image"},
		{name: "completed and deleting are not running", finalizer: "finalizers.helm.toolkit.fluxcd.io", group: "helm.toolkit.fluxcd.io", want: "no recognized controller found"},
		{name: "heuristic present", finalizer: "widgets.example.com/cleanup", group: "widgets.example.com", want: "no confident owner (name-only candidate: release-widgets-operator is healthy (1 pod ready)"},
		{name: "crashloop is not running", finalizer: "crashes.example.com/cleanup", group: "crashes.example.com", want: "crashes-operator is CrashLoopBackOff (1/1 pods)"},
		{name: "pending", finalizer: "pending.example.com/cleanup", group: "pending.example.com", want: "pending-operator is pending start"},
		{name: "degraded", finalizer: "degraded.example.com/cleanup", group: "degraded.example.com", want: "degraded-operator is degraded (0/1 pods ready)"},
		{name: "karpenter", finalizer: "karpenter.sh/termination", group: "karpenter.sh", want: "karpenter is healthy (1 pod ready)"},
		{name: "karpenter AWS provider", finalizer: "karpenter.k8s.aws/termination", group: "karpenter.k8s.aws", want: "karpenter is healthy (1 pod ready)"},
		{name: "karpenter Azure provider", finalizer: "karpenter.azure.com/termination", group: "karpenter.azure.com", want: "karpenter is healthy (1 pod ready)"},
		{name: "workloads readable pods denied", finalizer: "widgets.example.com/cleanup", group: "widgets.example.com", want: "state unknown (controller pod inventory is unreadable)", access: func(_, resource, _ string) bool { return resource != "pods" }},
		{name: "pods readable workloads denied", finalizer: "widgets.example.com/cleanup", group: "widgets.example.com", want: "controller unknown", access: func(_, resource, _ string) bool { return resource == "pods" }},
		{name: "unknown", finalizer: "unknown.example.com/cleanup", group: "unknown.example.com", want: "controller unknown"},
		{name: "catalog unreadable", finalizer: "finalizers.kustomize.toolkit.fluxcd.io", group: "kustomize.toolkit.fluxcd.io", want: "kustomize-controller state unknown", access: func(string, string, string) bool { return false }},
		{name: "hidden heuristic workload", finalizer: "widgets.example.com/cleanup", group: "widgets.example.com", want: "controller unknown", access: func(_, _, namespace string) bool { return namespace == "visible" }},
		{name: "hidden catalog custom workload", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", want: "victoria-metrics-operator state unknown", access: func(_, _, namespace string) bool { return namespace == "visible" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			observation := p.FinalizerOwnerStatus(tt.finalizer, Ref{Group: tt.group, Kind: "Widget", Namespace: "visible"}, tt.access)
			got := observation.Text
			// A name-only match never establishes the owner, so its state
			// can't make the controller running, down or missing.
			if (tt.name == "heuristic present" || tt.name == "crashloop is not running" || tt.name == "pending" || tt.name == "degraded") && (observation.Running || observation.Healthy || observation.NotFound || observation.Down || observation.Controller != "" || observation.Candidate == "") {
				t.Fatalf("name-only candidate must not be treated as the owner: %+v", observation)
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
	if got := p.FinalizerOwnerStatus("apps.victoriametrics.com/finalizer", Ref{Group: "operator.victoriametrics.com"}, nil).Text; !strings.Contains(got, "no recognized controller found") || !strings.Contains(got, "across the watched cluster") {
		t.Fatal(got)
	}
	if got := p.FinalizerOwnerStatus("apps.victoriametrics.com/finalizer", Ref{Group: "operator.victoriametrics.com"}, func(_, _, namespace string) bool { return namespace == "visible" }).Text; !strings.Contains(got, "victoria-metrics-operator state unknown") {
		t.Fatal(got)
	}
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "visible"}}); err != nil {
		t.Fatal(err)
	}
	p = NewCacheProvider()
	if got := p.FinalizerOwnerStatus("finalizers.helm.toolkit.fluxcd.io", Ref{Group: "helm.toolkit.fluxcd.io"}, nil).Text; !strings.Contains(got, "helm-controller state unknown") {
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
	scoped := Compose(p, Filters{Namespaces: []string{"visible"}, Grouped: true, CanReadRelated: func(r Ref) bool { return r.Namespace == "visible" }, CanReadEvidence: func(read EvidenceRead) bool { return read.Namespace == "visible" }})
	if len(scoped) != 1 || scoped[0].Name != "public" || scoped[0].Category != issuesapi.CategoryTerminationStuck || scoped[0].Severity != SeverityWarning {
		t.Fatalf("scoped=%+v", scoped)
	}
	if !strings.Contains(scoped[0].Cause, "external cleanup pending") || !strings.Contains(scoped[0].Cause, "controller unknown") || strings.Contains(scoped[0].Action, "patch_resource") || !strings.Contains(scoped[0].Action, "couldn't identify") {
		t.Fatalf("unknown controller guidance: %+v", scoped[0])
	}
	denied := Compose(p, Filters{CanReadClusterScoped: func(string, string) bool { return false }, CanReadEvidence: func(read EvidenceRead) bool { return read.Namespace != "" }})
	for _, issue := range denied {
		if issue.Namespace == "" {
			t.Fatalf("cluster denial leaked: %+v", issue)
		}
	}
	deniedKinds := Compose(p, Filters{Namespaces: []string{"visible", "hidden"}, CanReadEvidence: func(EvidenceRead) bool { return false }})
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
	all := Compose(p, Filters{Grouped: true, CanReadEvidence: func(EvidenceRead) bool { return true }})
	if len(all) != 3 {
		t.Fatalf("all=%+v", all)
	}
}

type observingFinalizerProvider struct {
	fakeProvider
	observation finalizerObservation
	byFinalizer map[string]finalizerObservation
	calls       int
}

func (p *observingFinalizerProvider) FinalizerOwnerStatus(finalizer string, _ Ref, _ func(string, string, string) bool) finalizerObservation {
	p.calls++
	if observation, ok := p.byFinalizer[finalizer]; ok {
		return observation
	}
	return p.observation
}

func TestTerminatingGuidanceAndNoise(t *testing.T) {
	removal := issuesapi.FinalizerRemoval{Finalizer: "crossplane.io/cleanup", PatchResource: []byte(`{"name":"deleting","dry_run":true}`), Kubectl: "kubectl patch 'widgets.crossplane.io' 'deleting' -n 'test' --type=json -p '[]' --dry-run=server"}
	for _, tc := range []struct {
		name          string
		finalizers    []string
		observation   finalizerObservation
		wantSeverity  Severity
		wantAction    string
		wantCondition string
		wantCalls     int
	}{
		{name: "healthy infrastructure", finalizers: []string{"crossplane.io/cleanup"}, observation: finalizerObservation{Text: "crossplane is healthy (1 pod ready)", Controller: "crossplane", Running: true, Healthy: true}, wantSeverity: SeverityWarning, wantAction: "Check its logs", wantCalls: 1},
		{name: "identified crashloop", finalizers: []string{"crossplane.io/cleanup"}, observation: finalizerObservation{Text: "crossplane is CrashLoopBackOff (1/1 pods)", Controller: "crossplane", Down: true, State: "CrashLoopBackOff (1/1 pods)"}, wantSeverity: SeverityCritical, wantAction: `Fix crossplane, which is CrashLoopBackOff (1/1 pods), so it can resume cleanup. Removing finalizer "crossplane.io/cleanup" would skip its cleanup.`, wantCalls: 1},
		{name: "no recognized controller", finalizers: []string{"crossplane.io/cleanup"}, observation: finalizerObservation{Text: "no recognized controller found", Controller: "crossplane", NotFound: true, AllowConditionalCleanup: true}, wantSeverity: SeverityWarning, wantAction: `No recognized controller found for finalizer "crossplane.io/cleanup". If crossplane should be running, restore it so it can resume cleanup.`, wantCondition: "If crossplane was uninstalled on purpose (and nothing else should finalize this object), remove the finalizer", wantCalls: 1},
		{name: "scaled to zero", finalizers: []string{"crossplane.io/cleanup"}, observation: finalizerObservation{Text: "crossplane is scaled to 0 replicas in namespace crossplane-system", Controller: "crossplane", NotFound: true, ScaledDown: true, AllowConditionalCleanup: true}, wantSeverity: SeverityWarning, wantAction: `No running controller found for finalizer "crossplane.io/cleanup": crossplane is scaled to 0 replicas in namespace crossplane-system. If it was stopped temporarily, scale it back up`, wantCondition: "If crossplane was scaled to 0 on purpose and won't be restarted (and nothing else should finalize this object), remove the finalizer", wantCalls: 1},
		{name: "catalog owner without cleanup opt-in", finalizers: []string{"crossplane.io/cleanup"}, observation: finalizerObservation{Text: "no recognized controller found", Controller: "crossplane", NotFound: true}, wantSeverity: SeverityWarning, wantAction: "Get crossplane running again rather than removing finalizer", wantCalls: 1},
		{name: "scaled down owner without cleanup opt-in", finalizers: []string{"crossplane.io/cleanup"}, observation: finalizerObservation{Text: "crossplane is scaled to 0 replicas", Controller: "crossplane", NotFound: true, ScaledDown: true}, wantSeverity: SeverityWarning, wantAction: "Get crossplane running again rather than removing finalizer", wantCalls: 1},
		{name: "not found controller that releases infrastructure", finalizers: []string{"crossplane.io/cleanup"}, observation: finalizerObservation{Text: "no recognized controller found", Controller: "crossplane", NotFound: true, ReleasesInfrastructure: true}, wantSeverity: SeverityWarning, wantAction: "Get crossplane running again", wantCalls: 1},
		{name: "down controller that releases infrastructure", finalizers: []string{"crossplane.io/cleanup"}, observation: finalizerObservation{Text: "crossplane is pending start (1/1 pods Pending)", Controller: "crossplane", Down: true, State: "pending start (1/1 pods Pending)", ReleasesInfrastructure: true}, wantSeverity: SeverityCritical, wantAction: "Fix crossplane, which is pending start", wantCalls: 1},
		{name: "identified but unconfirmed", finalizers: []string{"crossplane.io/cleanup"}, observation: finalizerObservation{Text: "crossplane has no pods; it may run outside the cluster", Controller: "crossplane"}, wantSeverity: SeverityWarning, wantAction: "can't confirm", wantCalls: 1},
		{name: "name-only candidate", finalizers: []string{"crossplane.io/cleanup"}, observation: finalizerObservation{Text: "no confident owner (name-only candidate: crossplane-operator is scaled to 0 replicas in namespace ops)", Candidate: "crossplane-operator"}, wantSeverity: SeverityWarning, wantAction: `Radar couldn't confirm which controller owns finalizer "crossplane.io/cleanup"; crossplane-operator resembles its API group by name only.`, wantCalls: 1},
		{name: "unknown", finalizers: []string{"crossplane.io/cleanup"}, observation: finalizerObservation{Text: "controller unknown"}, wantSeverity: SeverityWarning, wantAction: "couldn't identify", wantCalls: 1},
		{name: "foreground only", finalizers: []string{metav1.FinalizerDeleteDependents}, wantSeverity: SeverityCritical, wantAction: "dependents"},
		{name: "orphan only", finalizers: []string{metav1.FinalizerOrphanDependents}, wantSeverity: SeverityCritical, wantAction: "Orphaning preserves the dependents"},
		{name: "protection only", finalizers: []string{"gateway-exists-finalizer.gateway.networking.k8s.io"}, wantSeverity: SeverityCritical, wantAction: "in-use guard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &observingFinalizerProvider{observation: tc.observation}
			p.terminatingProblems = []k8s.Detection{{Kind: "Widget", Group: "crossplane.io", Namespace: "test", Name: "deleting", Reason: "Terminating stuck", Severity: "critical", TerminatingFinalizers: tc.finalizers, TerminatingRemovals: map[string]issuesapi.FinalizerRemoval{"crossplane.io/cleanup": removal}}}
			out := Compose(p, Filters{CanReadEvidence: func(EvidenceRead) bool { return true }})
			if len(out) != 1 || out[0].Severity != tc.wantSeverity || !strings.Contains(out[0].Action, tc.wantAction) || p.calls != tc.wantCalls {
				t.Fatalf("out=%+v calls=%d", out, p.calls)
			}
			if strings.Contains(out[0].Action, "patch_resource") || strings.Contains(out[0].Action, "kubectl patch") {
				t.Fatalf("a finalizer removal is never the next step: %s", out[0].Action)
			}
			cleanup := out[0].ConditionalCleanup
			if (cleanup != nil) != (tc.wantCondition != "") {
				t.Fatalf("conditional cleanup=%+v, want condition %q", cleanup, tc.wantCondition)
			}
			if cleanup != nil && (cleanup.Condition != tc.wantCondition || len(cleanup.Removals) != 1 || cleanup.Removals[0].Finalizer != "crossplane.io/cleanup" || !strings.Contains(cleanup.AgentGuidance, "only after the user confirms the condition") || !strings.Contains(cleanup.AgentGuidance, "never as the recommended fix")) {
				t.Fatalf("conditional cleanup=%+v", cleanup)
			}
			if cleanup == nil && !tc.observation.Running && !tc.observation.Down && tc.wantCalls > 0 && !strings.Contains(out[0].Action, "orphan external resources") {
				t.Fatalf("missing removal warning: %s", out[0].Action)
			}
			if tc.name == "foreground only" && !strings.Contains(out[0].Cause, "garbage collector is waiting for dependents") {
				t.Fatal(out[0].Cause)
			}
			if tc.name == "orphan only" && (strings.Contains(out[0].Cause, "waiting for dependents") || !strings.Contains(out[0].Cause, "orphaning its dependents") || strings.Contains(out[0].Action, "finish their deletion")) {
				t.Fatalf("orphaning keeps dependents: cause=%s action=%s", out[0].Cause, out[0].Action)
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
			deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "victoria-metrics-operator", Namespace: "operators"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "widgets"}}}}
			leader := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "leader", Namespace: "operators", Labels: map[string]string{"app": "widgets"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}}
			second := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "operators", Labels: map[string]string{"app": "widgets"}}, Status: tc.second}
			if err := k8s.InitScopedTestResourceCache(fake.NewClientset(deployment, leader, second), map[string]k8score.ResourceScope{"pods": {Enabled: true}, "deployments": {Enabled: true}}); err != nil {
				t.Fatal(err)
			}
			p := NewCacheProvider()
			observation := p.FinalizerOwnerStatus("apps.victoriametrics.com/finalizer", Ref{Group: "operator.victoriametrics.com", Kind: "VMAgent"}, nil)
			if !observation.Running || observation.Healthy || !strings.Contains(observation.Text, tc.want) {
				t.Fatalf("partial ready controller observation: %+v", observation)
			}
			pr := k8s.Detection{Kind: "VMAgent", Group: "operator.victoriametrics.com", Severity: "critical", TerminatingFinalizers: []string{"apps.victoriametrics.com/finalizer"}, TerminatingRemovals: map[string]issuesapi.FinalizerRemoval{"apps.victoriametrics.com/finalizer": {Finalizer: "apps.victoriametrics.com/finalizer"}}}
			pr = enrichTerminatingProblem(pr, p, nil, map[string]finalizerObservation{})
			if pr.Severity != "critical" || pr.ConditionalCleanup != nil || !strings.Contains(pr.Action, "Check its logs") {
				t.Fatalf("partial ready controller must suppress cleanup without warning cap: %+v", pr)
			}
		})
	}
}

// A workload that only resembles the finalizer's API group by name is a
// candidate, not the owner: neither its readiness nor its absence decides
// severity, and it never unlocks cleanup.
func TestFinalizerCandidateNeverEstablishesOwnership(t *testing.T) {
	t.Cleanup(k8s.ResetResourceCache)
	scopes := map[string]k8score.ResourceScope{"pods": {Enabled: true}, "deployments": {Enabled: true}, "statefulsets": {Enabled: true}, "daemonsets": {Enabled: true}}
	zero := int32(0)
	stopped := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "rabbitmq-cluster-operator", Namespace: "rabbitmq"}, Spec: appsv1.DeploymentSpec{Replicas: &zero, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "rabbitmq-cluster-operator"}}}}
	partial := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "widgets-operator", Namespace: "operators"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "widgets"}}}}
	leader := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "leader", Namespace: "operators", Labels: map[string]string{"app": "widgets"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}}
	crashing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "operators", Labels: map[string]string{"app": "widgets"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}}}
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(stopped, partial, leader, crashing), scopes); err != nil {
		t.Fatal(err)
	}
	p := NewCacheProvider()
	for _, tc := range []struct {
		finalizer, group, kind, candidate, state string
	}{
		{"cluster.example.io/cleanup", "cluster.example.io", "Cluster", "rabbitmq-cluster-operator", "rabbitmq-cluster-operator is scaled to 0 replicas in namespace rabbitmq"},
		{"widgets.example.com/cleanup", "widgets.example.com", "Widget", "widgets-operator", "widgets-operator is CrashLoopBackOff (1/2 pods) in namespace operators"},
	} {
		t.Run(tc.candidate, func(t *testing.T) {
			observation := p.FinalizerOwnerStatus(tc.finalizer, Ref{Group: tc.group, Kind: tc.kind}, nil)
			if observation.Controller != "" || observation.Candidate != tc.candidate || observation.NotFound || observation.ScaledDown || observation.Down || observation.Running || observation.Healthy || !strings.Contains(observation.Text, tc.state) {
				t.Fatalf("observation=%+v", observation)
			}
			pr := k8s.Detection{Kind: tc.kind, Group: tc.group, Severity: "critical", TerminatingFinalizers: []string{tc.finalizer}, TerminatingRemovals: map[string]issuesapi.FinalizerRemoval{tc.finalizer: {Finalizer: tc.finalizer}}}
			pr = enrichTerminatingProblem(pr, p, nil, map[string]finalizerObservation{})
			if pr.Severity != "high" || pr.ConditionalCleanup != nil || !strings.Contains(pr.Action, tc.candidate+" resembles its API group by name only") || strings.Contains(pr.Action, "running and may be working") {
				t.Fatalf("candidate must not establish ownership: %+v", pr)
			}
		})
	}
}

func TestVictoriaMetricsReloaderDoesNotEstablishControllerHealth(t *testing.T) {
	for _, tc := range []struct {
		name          string
		workload      string
		withPod       bool
		operatorState string
		reloaderDown  bool
	}{
		{name: "deployment and pod", workload: "Deployment", withPod: true},
		{name: "deployment template without pods", workload: "Deployment"},
		{name: "statefulset and pod", workload: "StatefulSet", withPod: true},
		{name: "daemonset and pod", workload: "DaemonSet", withPod: true},
		{name: "bare pod", withPod: true},
		{name: "healthy reloader cannot hide crashing operator", workload: "Deployment", withPod: true, operatorState: "crashing"},
		{name: "crashing reloader cannot degrade healthy operator", workload: "Deployment", withPod: true, operatorState: "healthy", reloaderDown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(k8s.ResetResourceCache)
			labels := map[string]string{"app.kubernetes.io/name": "vmagent"}
			metadata := metav1.ObjectMeta{Name: "vmagent-stuck", Namespace: "team", Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "operator.victoriametrics.com/v1beta1", Kind: "VMAgent", Name: "stuck", UID: "vmagent-uid"}}}
			template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "vmagent", Image: "victoriametrics/vmagent:v1.117.0"}, {Name: "config-reloader", Image: "victoriametrics/operator:config-reloader-v0.66.1"}}}}
			selector := &metav1.LabelSelector{MatchLabels: labels}
			var objects []runtime.Object
			switch tc.workload {
			case "Deployment":
				objects = append(objects, &appsv1.Deployment{ObjectMeta: metadata, Spec: appsv1.DeploymentSpec{Selector: selector, Template: template}})
			case "StatefulSet":
				objects = append(objects, &appsv1.StatefulSet{ObjectMeta: metadata, Spec: appsv1.StatefulSetSpec{Selector: selector, Template: template}})
			case "DaemonSet":
				objects = append(objects, &appsv1.DaemonSet{ObjectMeta: metadata, Spec: appsv1.DaemonSetSpec{Selector: selector, Template: template}})
			}
			if tc.withPod {
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "vmagent-stuck-0", Namespace: "team", Labels: labels}, Spec: template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "vmagent", Ready: true}, {Name: "config-reloader", Ready: true}}}}
				if tc.reloaderDown {
					pod.Status.ContainerStatuses[1] = corev1.ContainerStatus{Name: "config-reloader", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}
				}
				objects = append(objects, pod)
			}
			if tc.operatorState != "" {
				operator := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "vmop", Namespace: "monitoring"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Image: "victoriametrics/operator:v0.66.1"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "manager", Ready: true}}}}
				if tc.operatorState == "crashing" {
					operator.Status.ContainerStatuses[0] = corev1.ContainerStatus{Name: "manager", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}
				}
				objects = append(objects, operator)
			}
			scopes := map[string]k8score.ResourceScope{"pods": {Enabled: true}, "deployments": {Enabled: true}, "statefulsets": {Enabled: true}, "daemonsets": {Enabled: true}}
			if err := k8s.InitScopedTestResourceCache(fake.NewClientset(objects...), scopes); err != nil {
				t.Fatal(err)
			}
			provider := NewCacheProvider()
			finalizer := "apps.victoriametrics.com/finalizer"
			observation := provider.FinalizerOwnerStatus(finalizer, Ref{Group: "operator.victoriametrics.com", Kind: "VMAgent"}, nil)
			problem := enrichTerminatingProblem(k8s.Detection{Kind: "VMAgent", Group: "operator.victoriametrics.com", Severity: "critical", TerminatingFinalizers: []string{finalizer}, TerminatingRemovals: map[string]issuesapi.FinalizerRemoval{finalizer: {Finalizer: finalizer}}}, provider, nil, map[string]finalizerObservation{})
			switch tc.operatorState {
			case "crashing":
				if !observation.Down || observation.Running || problem.Severity != "critical" || problem.ConditionalCleanup != nil || !strings.Contains(problem.Action, "Fix victoria-metrics-operator") {
					t.Fatalf("healthy reloader hid failing operator: observation=%+v problem=%+v", observation, problem)
				}
			case "healthy":
				if !observation.Healthy || !observation.Running || problem.Severity != "high" || problem.ConditionalCleanup != nil {
					t.Fatalf("failing reloader degraded healthy operator: observation=%+v problem=%+v", observation, problem)
				}
			default:
				if !observation.NotFound || observation.Running || observation.Down || problem.Severity != "high" || problem.ConditionalCleanup == nil || !strings.Contains(problem.Action, "No recognized controller found") {
					t.Fatalf("reloader mistaken for operator: observation=%+v problem=%+v", observation, problem)
				}
			}
		})
	}
}

func TestFinalizerOwnerRemovalEvidence(t *testing.T) {
	t.Cleanup(k8s.ResetResourceCache)
	scopes := map[string]k8score.ResourceScope{"pods": {Enabled: true}, "deployments": {Enabled: true}, "statefulsets": {Enabled: true}, "daemonsets": {Enabled: true}}
	karpenter := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "karpenter", Namespace: "kube-system", Labels: map[string]string{"app.kubernetes.io/name": "karpenter"}}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "karpenter"}}}}
	crashing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "karpenter-1", Namespace: "kube-system", Labels: map[string]string{"app.kubernetes.io/name": "karpenter"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}}}
	appsDenied := func(group, _, namespace string) bool { return group != "apps" || namespace != "" }
	vmLabel := map[string]string{"app.kubernetes.io/name": "victoria-metrics-operator"}
	vmOperator := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "victoria-metrics-operator", Namespace: "monitoring", Labels: vmLabel}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: vmLabel}}}
	vmCrashing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "vm-operator-0", Namespace: "monitoring", Labels: vmLabel}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}}}
	vmImagePull := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "vm-operator-0", Namespace: "monitoring", Labels: vmLabel}, Status: corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}}}}
	zero := int32(0)
	vmScaledDown := vmOperator.DeepCopy()
	vmScaledDown.Spec.Replicas = &zero
	vmOtherCrashing := vmOperator.DeepCopy()
	vmOtherCrashing.Namespace = "monitoring-b"
	vmOtherCrashingPod := vmCrashing.DeepCopy()
	vmOtherCrashingPod.Namespace = "monitoring-b"
	// Only the pod template carries the catalog label.
	vmUnlabeled := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "vmop", Namespace: "monitoring"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "vmop"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "vmop", "app.kubernetes.io/name": "victoria-metrics-operator"}}}}}
	vmUnlabeledScaledDown := vmUnlabeled.DeepCopy()
	vmUnlabeledScaledDown.Spec.Replicas = &zero
	// A catalog pod outside every matched workload, such as one from a DaemonSet.
	vmStrayCrashing := vmCrashing.DeepCopy()
	vmStrayCrashing.Namespace = "elsewhere"
	vmStrayReady := vmStrayCrashing.DeepCopy()
	vmStrayReady.Status = corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}
	clusterPodsDenied := func(_, resource, namespace string) bool { return resource != "pods" || namespace != "" }
	argoLabel := map[string]string{"app.kubernetes.io/name": "argocd-application-controller"}
	argoScaledDown := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "argocd-application-controller", Namespace: "argocd", Labels: argoLabel}, Spec: appsv1.StatefulSetSpec{Replicas: &zero, Selector: &metav1.LabelSelector{MatchLabels: argoLabel}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: argoLabel}}}}
	karpenterScaledDown := karpenter.DeepCopy()
	karpenterScaledDown.Spec.Replicas = &zero
	fluxStrayCrashing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "kustomize-controller-x", Namespace: "custom-flux", Labels: map[string]string{"app": "kustomize-controller"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}}}
	fluxStrayReady := fluxStrayCrashing.DeepCopy()
	fluxStrayReady.Status = corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}
	vmReadyPod := vmCrashing.DeepCopy()
	vmReadyPod.Status = corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}
	// A Helm nameOverride renames the workload and its labels; only the image
	// still says what it runs.
	vmRenamedLabel := map[string]string{"app.kubernetes.io/name": "vmop"}
	two := int32(2)
	vmRenamed := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "vmop", Namespace: "monitoring", Labels: vmRenamedLabel}, Spec: appsv1.DeploymentSpec{Replicas: &two, Selector: &metav1.LabelSelector{MatchLabels: vmRenamedLabel}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: vmRenamedLabel}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "operator", Image: "victoriametrics/operator:v0.50.0"}}}}}}
	vmRenamedScaledDown := vmRenamed.DeepCopy()
	vmRenamedScaledDown.Spec.Replicas = &zero
	vmRenamedPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "vmop-0", Namespace: "monitoring", Labels: vmRenamedLabel}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "operator", Image: "docker.io/victoriametrics/operator:v0.50.0"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}}
	vmRenamedPulling := vmRenamedPod.DeepCopy()
	vmRenamedPulling.Status = corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}}}
	argoServerLabel := map[string]string{"app.kubernetes.io/name": "argocd-server"}
	argoServerSpec := corev1.PodSpec{Containers: []corev1.Container{{Name: "argocd-server", Image: "quay.io/argoproj/argocd:v2.13.0", Args: []string{"/usr/local/bin/argocd-server"}}}}
	argoServer := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "argocd-server", Namespace: "argocd", Labels: argoServerLabel}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: argoServerLabel}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: argoServerLabel}, Spec: argoServerSpec}}}
	argoServerPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "argocd-server-0", Namespace: "argocd", Labels: argoServerLabel}, Spec: argoServerSpec, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}}
	vmDaemon := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "vm-agent-operator", Namespace: "monitoring"}, Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "vmop-ds"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "vmop-ds", "app.kubernetes.io/name": "victoria-metrics-operator"}}}}}
	for _, tc := range []struct {
		name, finalizer, group string
		objects                []runtime.Object
		scopes                 map[string]k8score.ResourceScope
		access                 func(string, string, string) bool
		want                   string
		wantNotFound           bool
		wantScaledDown         bool
		wantDown               bool
		wantInfrastructure     bool
	}{
		{name: "no recognized catalog controller", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", want: "no recognized controller found: no workload or pod across the watched cluster matches victoria-metrics-operator by name, labels or image", wantNotFound: true},
		{name: "workload scaled to zero", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmScaledDown}, want: "victoria-metrics-operator is scaled to 0 replicas in namespace monitoring", wantNotFound: true, wantScaledDown: true},
		{name: "renamed workload recognized by template image, no pods", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmRenamed}, want: "vmop is not running (no pods for 2 desired replicas) in namespace monitoring", wantDown: true},
		{name: "renamed workload recognized by template image, image pull failing", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmRenamed, vmRenamedPulling}, want: "vmop is pending start (1/1 pods Pending) in namespace monitoring", wantDown: true},
		{name: "renamed workload recognized by template image, scaled to zero", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmRenamedScaledDown}, want: "vmop is scaled to 0 replicas in namespace monitoring", wantNotFound: true, wantScaledDown: true},
		{name: "Argo CD server image is not the application controller", finalizer: "resources-finalizer.argocd.argoproj.io", group: "argoproj.io", objects: []runtime.Object{argoServer, argoServerPod}, want: "argocd-application-controller has no pods across the watched cluster; it may run outside the cluster"},
		{name: "workload without pods", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmOperator}, want: "victoria-metrics-operator is not running (no pods for 1 desired replica) in namespace monitoring", wantDown: true},
		{name: "workload matched by pod template labels", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmUnlabeled}, want: "vmop is not running (no pods for 1 desired replica) in namespace monitoring", wantDown: true},
		{name: "image pull failure", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmOperator, vmImagePull}, want: "victoria-metrics-operator is pending start (1/1 pods Pending) in namespace monitoring", wantDown: true},
		{name: "one copy scaled down, another crashlooping", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmScaledDown, vmOtherCrashing, vmOtherCrashingPod}, want: "victoria-metrics-operator is CrashLoopBackOff (1/1 pods) in namespace monitoring-b", wantDown: true},
		{name: "scaled down copy beside an unreadable one", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmScaledDown, vmOtherCrashing, vmOtherCrashingPod}, access: func(_, resource, namespace string) bool { return resource != "pods" || namespace != "monitoring-b" }, want: "victoria-metrics-operator is scaled to 0 replicas in namespace monitoring"},
		{name: "scaled down workload beside a stray crashlooping catalog pod", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmUnlabeledScaledDown, vmStrayCrashing}, want: "vmop is scaled to 0 replicas in namespace monitoring; victoria-metrics-operator is CrashLoopBackOff (1/1 pods) across the watched cluster", wantDown: true},
		{name: "scaled down workload with catalog pods unsearchable", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmScaledDown}, access: clusterPodsDenied, want: "victoria-metrics-operator is scaled to 0 replicas in namespace monitoring; victoria-metrics-operator pods across the watched cluster could not be checked"},
		{name: "DaemonSet without pods", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmDaemon}, want: "vm-agent-operator is not running (no DaemonSet pods) in namespace monitoring", wantDown: true},
		{name: "DaemonSets not cached", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", scopes: map[string]k8score.ResourceScope{"pods": {Enabled: true}, "deployments": {Enabled: true}, "statefulsets": {Enabled: true}}, want: "could not be searched cluster-wide"},
		{name: "absence without a cluster-wide workload search", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", access: appsDenied, want: "could not be searched cluster-wide"},
		{name: "managed karpenter", finalizer: "karpenter.sh/termination", group: "karpenter.sh", want: "karpenter has no pods across the watched cluster; it may run outside the cluster (EKS Auto Mode, AKS node auto-provisioning)", wantInfrastructure: true},
		{name: "managed karpenter node class", finalizer: "karpenter.k8s.aws/termination", group: "karpenter.k8s.aws", want: "may run outside the cluster", wantInfrastructure: true},
		{name: "self-hosted karpenter crashing", finalizer: "karpenter.sh/termination", group: "karpenter.sh", objects: []runtime.Object{karpenter, crashing}, want: "karpenter is CrashLoopBackOff (1/1 pods) in namespace kube-system", wantDown: true, wantInfrastructure: true},
		{name: "managed argo cd", finalizer: "resources-finalizer.argocd.argoproj.io", group: "argoproj.io", want: "argocd-application-controller has no pods across the watched cluster; it may run outside the cluster (Amazon EKS Capabilities)"},
		{name: "argo cd scaled to zero may still run off-cluster", finalizer: "resources-finalizer.argocd.argoproj.io", group: "argoproj.io", objects: []runtime.Object{argoScaledDown}, want: "argocd-application-controller is scaled to 0 replicas in namespace argocd; argocd-application-controller may run outside the cluster (Amazon EKS Capabilities)"},
		{name: "karpenter scaled to zero may still run off-cluster", finalizer: "karpenter.sh/termination", group: "karpenter.sh", objects: []runtime.Object{karpenterScaledDown}, want: "karpenter is scaled to 0 replicas in namespace kube-system; karpenter may run outside the cluster (EKS Auto Mode, AKS node auto-provisioning)", wantInfrastructure: true},
		{name: "flux controller pod crashlooping outside flux-system", finalizer: "finalizers.kustomize.toolkit.fluxcd.io", group: "kustomize.toolkit.fluxcd.io", objects: []runtime.Object{fluxStrayCrashing}, want: "kustomize-controller is CrashLoopBackOff (1/1 pods) across the watched cluster", wantDown: true},
		{name: "no recognized flux controller", finalizer: "finalizers.kustomize.toolkit.fluxcd.io", group: "kustomize.toolkit.fluxcd.io", want: "no recognized controller found: no workload or pod across the watched cluster matches kustomize-controller by name, labels or image", wantNotFound: true},
		{name: "visible controller down without a cluster-wide workload search", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmOperator, vmCrashing}, access: appsDenied, want: "victoria-metrics-operator is CrashLoopBackOff (1/1 pods) in namespace monitoring, but Deployments, StatefulSets and DaemonSets could not be searched cluster-wide"},
		{name: "visible controller down", finalizer: "apps.victoriametrics.com/finalizer", group: "operator.victoriametrics.com", objects: []runtime.Object{vmOperator, vmCrashing}, want: "victoria-metrics-operator is CrashLoopBackOff (1/1 pods) in namespace monitoring", wantDown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caches := scopes
			if tc.scopes != nil {
				caches = tc.scopes
			}
			if err := k8s.InitScopedTestResourceCache(fake.NewClientset(tc.objects...), caches); err != nil {
				t.Fatal(err)
			}
			observation := NewCacheProvider().FinalizerOwnerStatus(tc.finalizer, Ref{Group: tc.group, Kind: "Subject"}, tc.access)
			if !strings.Contains(observation.Text, tc.want) || observation.NotFound != tc.wantNotFound || observation.ScaledDown != tc.wantScaledDown || observation.Down != tc.wantDown || observation.ReleasesInfrastructure != tc.wantInfrastructure || observation.Controller == "" || observation.Running {
				t.Fatalf("observation=%+v", observation)
			}
		})
	}
	t.Run("flux controller ready outside flux-system", func(t *testing.T) {
		if err := k8s.InitScopedTestResourceCache(fake.NewClientset(fluxStrayReady), scopes); err != nil {
			t.Fatal(err)
		}
		observation := NewCacheProvider().FinalizerOwnerStatus("finalizers.kustomize.toolkit.fluxcd.io", Ref{Group: "kustomize.toolkit.fluxcd.io", Kind: "Subject"}, nil)
		if !observation.Running || observation.NotFound || observation.Down || !strings.Contains(observation.Text, "kustomize-controller is healthy (1 pod ready) across the watched cluster") {
			t.Fatalf("observation=%+v", observation)
		}
	})
	t.Run("healthy install beside a crashlooping one is not healthy", func(t *testing.T) {
		if err := k8s.InitScopedTestResourceCache(fake.NewClientset(vmOperator, vmReadyPod, vmOtherCrashing, vmOtherCrashingPod), scopes); err != nil {
			t.Fatal(err)
		}
		p := NewCacheProvider()
		observation := p.FinalizerOwnerStatus("apps.victoriametrics.com/finalizer", Ref{Group: "operator.victoriametrics.com", Kind: "Subject"}, nil)
		if !observation.Running || observation.Healthy || observation.NotFound {
			t.Fatalf("observation=%+v", observation)
		}
		pr := k8s.Detection{Kind: "VMAgent", Group: "operator.victoriametrics.com", Severity: "critical", TerminatingFinalizers: []string{"apps.victoriametrics.com/finalizer"}}
		if pr = enrichTerminatingProblem(pr, p, nil, map[string]finalizerObservation{}); pr.Severity != "critical" {
			t.Fatalf("a crashlooping matched install must keep the deletion critical: %+v", pr)
		}
	})
	t.Run("healthy catalog pod beside a crashlooping matched workload is not healthy", func(t *testing.T) {
		kustomize := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "kustomize-controller", Namespace: "flux-system"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "kc-custom"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "kc-custom"}}}}}
		kustomizeCrashing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "kustomize-controller-0", Namespace: "flux-system", Labels: map[string]string{"app": "kc-custom"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}}}
		if err := k8s.InitScopedTestResourceCache(fake.NewClientset(kustomize, kustomizeCrashing, fluxStrayReady), scopes); err != nil {
			t.Fatal(err)
		}
		p := NewCacheProvider()
		observation := p.FinalizerOwnerStatus("finalizers.kustomize.toolkit.fluxcd.io", Ref{Group: "kustomize.toolkit.fluxcd.io", Kind: "Subject"}, nil)
		if !observation.Running || observation.Healthy || observation.NotFound || observation.Down {
			t.Fatalf("observation=%+v", observation)
		}
		pr := k8s.Detection{Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Severity: "critical", TerminatingFinalizers: []string{"finalizers.kustomize.toolkit.fluxcd.io"}}
		if pr = enrichTerminatingProblem(pr, p, nil, map[string]finalizerObservation{}); pr.Severity != "critical" {
			t.Fatalf("a crashlooping matched workload must keep the deletion critical: %+v", pr)
		}
	})
	t.Run("healthy matched workload beside a crashlooping catalog pod is not healthy", func(t *testing.T) {
		kustomize := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "kustomize-controller", Namespace: "flux-system"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "custom-controller"}}}}
		kustomizeReady := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "kustomize-controller-0", Namespace: "flux-system", Labels: map[string]string{"app": "custom-controller"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}}
		if err := k8s.InitScopedTestResourceCache(fake.NewClientset(kustomize, kustomizeReady, fluxStrayCrashing), scopes); err != nil {
			t.Fatal(err)
		}
		p := NewCacheProvider()
		observation := p.FinalizerOwnerStatus("finalizers.kustomize.toolkit.fluxcd.io", Ref{Group: "kustomize.toolkit.fluxcd.io", Kind: "Subject"}, nil)
		if !observation.Running || observation.Healthy || observation.NotFound || observation.Down || !strings.Contains(observation.Text, "kustomize-controller is CrashLoopBackOff (1/1 pods) across the watched cluster") {
			t.Fatalf("observation=%+v", observation)
		}
		pr := k8s.Detection{Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Severity: "critical", TerminatingFinalizers: []string{"finalizers.kustomize.toolkit.fluxcd.io"}, TerminatingRemovals: map[string]issuesapi.FinalizerRemoval{"finalizers.kustomize.toolkit.fluxcd.io": {Finalizer: "finalizers.kustomize.toolkit.fluxcd.io"}}}
		if pr = enrichTerminatingProblem(pr, p, nil, map[string]finalizerObservation{}); pr.Severity != "critical" || pr.ConditionalCleanup != nil {
			t.Fatalf("a crashlooping catalog pod must keep the deletion critical with no cleanup: %+v", pr)
		}
	})
	t.Run("renamed install recognized by image is healthy", func(t *testing.T) {
		if err := k8s.InitScopedTestResourceCache(fake.NewClientset(vmRenamed, vmRenamedPod), scopes); err != nil {
			t.Fatal(err)
		}
		p := NewCacheProvider()
		observation := p.FinalizerOwnerStatus("apps.victoriametrics.com/finalizer", Ref{Group: "operator.victoriametrics.com", Kind: "VMAgent"}, nil)
		if !observation.Running || !observation.Healthy || observation.NotFound || !strings.Contains(observation.Text, "vmop is healthy (1 pod ready) in namespace monitoring") {
			t.Fatalf("observation=%+v", observation)
		}
	})
	t.Run("renamed pod with no workload recognized by image", func(t *testing.T) {
		if err := k8s.InitScopedTestResourceCache(fake.NewClientset(vmRenamedPod), scopes); err != nil {
			t.Fatal(err)
		}
		observation := NewCacheProvider().FinalizerOwnerStatus("apps.victoriametrics.com/finalizer", Ref{Group: "operator.victoriametrics.com", Kind: "VMAgent"}, nil)
		if !observation.Running || !observation.Healthy || observation.NotFound || !strings.Contains(observation.Text, "victoria-metrics-operator is healthy (1 pod ready) across the watched cluster") {
			t.Fatalf("observation=%+v", observation)
		}
	})
	t.Run("catalog owner kept when the Pod cache is unavailable", func(t *testing.T) {
		if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), map[string]k8score.ResourceScope{"deployments": {Enabled: true}}); err != nil {
			t.Fatal(err)
		}
		p := NewCacheProvider()
		observation := p.FinalizerOwnerStatus("finalizers.kustomize.toolkit.fluxcd.io", Ref{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization"}, nil)
		if observation.Controller != "kustomize-controller" || observation.NotFound || observation.Down || observation.Running || !strings.Contains(observation.Text, "kustomize-controller state unknown (Pod cache is not ready)") {
			t.Fatalf("observation=%+v", observation)
		}
		pr := k8s.Detection{Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Severity: "critical", TerminatingFinalizers: []string{"finalizers.kustomize.toolkit.fluxcd.io"}, TerminatingRemovals: map[string]issuesapi.FinalizerRemoval{"finalizers.kustomize.toolkit.fluxcd.io": {Finalizer: "finalizers.kustomize.toolkit.fluxcd.io"}}}
		pr = enrichTerminatingProblem(pr, p, nil, map[string]finalizerObservation{})
		if pr.Severity != "high" || pr.ConditionalCleanup != nil || strings.Contains(pr.Action, "couldn't identify") || !strings.Contains(pr.Action, "whether kustomize-controller, which owns finalizer") {
			t.Fatalf("known owner with unavailable inventory: %+v", pr)
		}
	})
	t.Run("healthy install beside a copy scaled to zero is healthy", func(t *testing.T) {
		if err := k8s.InitScopedTestResourceCache(fake.NewClientset(vmOperator, vmReadyPod, vmUnlabeledScaledDown), scopes); err != nil {
			t.Fatal(err)
		}
		observation := NewCacheProvider().FinalizerOwnerStatus("apps.victoriametrics.com/finalizer", Ref{Group: "operator.victoriametrics.com", Kind: "Subject"}, nil)
		if !observation.Running || !observation.Healthy || observation.NotFound {
			t.Fatalf("observation=%+v", observation)
		}
	})
	t.Run("scaled down workload beside a ready catalog pod", func(t *testing.T) {
		if err := k8s.InitScopedTestResourceCache(fake.NewClientset(vmUnlabeledScaledDown, vmStrayReady), scopes); err != nil {
			t.Fatal(err)
		}
		observation := NewCacheProvider().FinalizerOwnerStatus("apps.victoriametrics.com/finalizer", Ref{Group: "operator.victoriametrics.com", Kind: "Subject"}, nil)
		if !observation.Running || observation.NotFound || observation.Down || !strings.Contains(observation.Text, "victoria-metrics-operator is healthy (1 pod ready) across the watched cluster") {
			t.Fatalf("observation=%+v", observation)
		}
	})
}

// Real finalizer keys from infrastructure operators. Removing any of them while
// their controller may still be deleting cloud resources orphans that
// infrastructure, so only a catalog controller Radar searched for completely
// and found no running copy of gets a removal, and only as a cleanup
// conditioned on an intentional uninstall: never as the next step.
func TestStuckDeletionCleanupNeedsNoRecognizedController(t *testing.T) {
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
		{schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}, "Application", "argocd", []string{"resources-finalizer.argocd.argoproj.io"}},
		{schema.GroupVersionResource{Group: "cluster.example.io", Version: "v1", Resource: "exampleclusters"}, "ExampleCluster", "team", []string{"cluster.example.io/cleanup"}},
	}
	compose := func(t *testing.T, typed ...runtime.Object) map[string]Issue {
		t.Helper()
		k8s.ResetTestDynamicState()
		if err := k8s.InitScopedTestResourceCache(fake.NewClientset(typed...), map[string]k8score.ResourceScope{"pods": {Enabled: true}, "deployments": {Enabled: true}, "statefulsets": {Enabled: true}, "daemonsets": {Enabled: true}}); err != nil {
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
		for _, issue := range Compose(NewCacheProvider(), Filters{CanReadEvidence: func(EvidenceRead) bool { return true }}) {
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
		if strings.Contains(issue.Action, "patch_resource") || strings.Contains(issue.Action, "kubectl patch") {
			t.Errorf("%s: a finalizer removal is never the next step: %s", issue.Kind, issue.Action)
		}
		return issue.ConditionalCleanup != nil
	}

	controllersAbsent := compose(t)
	if action := controllersAbsent["SQLInstance"].Action; strings.Count(action, "couldn't identify") != 1 || !strings.Contains(action, `finalizers "cnrm.cloud.google.com/finalizer", "cnrm.cloud.google.com/deletion-defender"`) {
		t.Errorf("Config Connector finalizers should share one sentence: %s", action)
	}
	for _, kind := range []string{"Instance", "XDatabase", "Cluster", "SQLInstance", "ExampleCluster"} {
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
	if issue := controllersAbsent["Application"]; offersRemoval(issue) || issue.Severity != SeverityWarning || !strings.Contains(issue.Cause, "may run outside the cluster (Amazon EKS Capabilities)") || !strings.Contains(issue.Action, "can't confirm") {
		t.Errorf("managed Argo CD: severity=%s cause=%s action=%s", issue.Severity, issue.Cause, issue.Action)
	}
	vm := controllersAbsent["VMAgent"]
	if !offersRemoval(vm) || vm.Severity != SeverityWarning || !strings.Contains(vm.Cause, "no recognized controller found") || !strings.HasPrefix(vm.Action, `No recognized controller found for finalizer "apps.victoriametrics.com/finalizer". If victoria-metrics-operator should be running, restore it`) {
		t.Errorf("no recognized VictoriaMetrics operator: severity=%s cause=%s action=%s", vm.Severity, vm.Cause, vm.Action)
	}
	if cleanup := vm.ConditionalCleanup; cleanup == nil || cleanup.Condition != "If victoria-metrics-operator was uninstalled on purpose (and nothing else should finalize this object), remove the finalizer" || len(cleanup.Removals) != 1 || !strings.Contains(string(cleanup.Removals[0].PatchResource), `"name":"stuck"`) || !strings.Contains(string(cleanup.Removals[0].PatchResource), `uid-VMAgent`) || !strings.HasSuffix(cleanup.Removals[0].Kubectl, "--dry-run=server") {
		t.Errorf("conditional cleanup for no recognized VictoriaMetrics operator: %+v", cleanup)
	}
	for _, word := range []string{"absent", "not installed"} {
		if strings.Contains(strings.ToLower(vm.Cause+vm.Action+vm.ConditionalCleanup.Condition), word) {
			t.Errorf("a negative search must not claim %q: %s %s", word, vm.Cause, vm.Action)
		}
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
	if issue := controllersPresent["NodeClaim"]; offersRemoval(issue) || issue.Severity != SeverityCritical || !strings.Contains(issue.Cause, "karpenter is degraded (0/1 pods ready)") || !strings.Contains(issue.Action, "Fix karpenter, which is degraded (0/1 pods ready)") {
		t.Errorf("self-hosted Karpenter not ready: severity=%s cause=%s action=%s", issue.Severity, issue.Cause, issue.Action)
	}

	// An installed controller that can't get a ready pod still intends to run
	// its cleanup once fixed; removing its finalizer would skip that.
	for _, tc := range []struct {
		name, state string
		status      corev1.PodStatus
	}{
		{"crashloop", "CrashLoopBackOff (1/1 pods)", corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}}},
		{"image pull", "pending start (1/1 pods Pending)", corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}}}},
		{"unschedulable", "pending start (1/1 pods Pending)", corev1.PodStatus{Phase: corev1.PodPending}},
	} {
		unhealthy := vmPod.DeepCopy()
		unhealthy.Status = tc.status
		vm := compose(t, vmOperator, unhealthy)["VMAgent"]
		if offersRemoval(vm) || vm.Severity != SeverityCritical || !strings.Contains(vm.Cause, "victoria-metrics-operator is "+tc.state) || !strings.Contains(vm.Action, "Fix vm-victoria-metrics-operator, which is "+tc.state+", so it can resume cleanup.") || !strings.Contains(vm.Action, "would skip its cleanup") {
			t.Errorf("installed VictoriaMetrics operator, %s: severity=%s cause=%s action=%s", tc.name, vm.Severity, vm.Cause, vm.Action)
		}
	}

	zero := int32(0)
	scaledDown := vmOperator.DeepCopy()
	scaledDown.Spec.Replicas = &zero
	argoLabel := map[string]string{"app.kubernetes.io/name": "argocd-application-controller"}
	argoScaledDown := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "argocd-application-controller", Namespace: "argocd", Labels: argoLabel}, Spec: appsv1.StatefulSetSpec{Replicas: &zero, Selector: &metav1.LabelSelector{MatchLabels: argoLabel}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: argoLabel}}}}
	if issue := compose(t, argoScaledDown)["Application"]; offersRemoval(issue) || issue.Severity != SeverityWarning || !strings.Contains(issue.Cause, "is scaled to 0 replicas in namespace argocd") || !strings.Contains(issue.Cause, "may run outside the cluster (Amazon EKS Capabilities)") || !strings.Contains(issue.Action, "can't confirm") {
		t.Errorf("in-cluster Argo CD scaled to zero beside a possible managed install: severity=%s cause=%s action=%s", issue.Severity, issue.Cause, issue.Action)
	}
	if vm := compose(t, scaledDown)["VMAgent"]; !offersRemoval(vm) || vm.Severity != SeverityWarning || !strings.Contains(vm.Cause, "vm-victoria-metrics-operator is scaled to 0 replicas in namespace monitoring") || !strings.Contains(vm.Action, "If it was stopped temporarily, scale it back up") || !strings.Contains(vm.ConditionalCleanup.Condition, "vm-victoria-metrics-operator was scaled to 0 on purpose") {
		t.Errorf("VictoriaMetrics operator scaled to zero: severity=%s cause=%s action=%s", vm.Severity, vm.Cause, vm.Action)
	}

	// Installed under a Helm nameOverride: only the image identifies it.
	renamedLabel := map[string]string{"app.kubernetes.io/name": "vmop"}
	renamed := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "vmop", Namespace: "monitoring", Labels: renamedLabel}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: renamedLabel}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: renamedLabel}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "operator", Image: "victoriametrics/operator:v0.50.0"}}}}}}
	renamedPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "vmop-0", Namespace: "monitoring", Labels: renamedLabel}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "operator", Image: "docker.io/victoriametrics/operator:v0.50.0"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}}
	if vm := compose(t, renamed, renamedPod)["VMAgent"]; offersRemoval(vm) || vm.Severity != SeverityWarning || !strings.Contains(vm.Cause, "vmop is healthy (1 pod ready) in namespace monitoring") || !strings.Contains(vm.Action, "controller is running") {
		t.Errorf("renamed healthy VictoriaMetrics operator: severity=%s cause=%s action=%s", vm.Severity, vm.Cause, vm.Action)
	}
	// Admission can block every pod of a renamed install; the template image
	// still identifies it as installed and down.
	if vm := compose(t, renamed)["VMAgent"]; offersRemoval(vm) || vm.Severity != SeverityCritical || !strings.Contains(vm.Action, "Fix vmop, which is not running (no pods for 1 desired replica), so it can resume cleanup.") {
		t.Errorf("renamed VictoriaMetrics operator with no pods: severity=%s cause=%s action=%s", vm.Severity, vm.Cause, vm.Action)
	}
	// "cluster" in cluster.example.io matches rabbitmq-cluster-operator by name
	// only; that stopped operator must not read as the finalizer's owner.
	rabbitmq := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "rabbitmq-cluster-operator", Namespace: "rabbitmq"}, Spec: appsv1.DeploymentSpec{Replicas: &zero, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "rabbitmq-cluster-operator"}}}}
	if issue := compose(t, rabbitmq)["ExampleCluster"]; offersRemoval(issue) || issue.Severity != SeverityWarning || !strings.Contains(issue.Cause, "name-only candidate: rabbitmq-cluster-operator is scaled to 0 replicas") || !strings.Contains(issue.Action, "rabbitmq-cluster-operator resembles its API group by name only") {
		t.Errorf("name collision with a stopped operator: severity=%s cause=%s action=%s", issue.Severity, issue.Cause, issue.Action)
	}
}

func TestTerminationCompositionHonorsControllerEvidence(t *testing.T) {
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)
	t.Cleanup(k8s.ResetResourceCache)
	gvr := schema.GroupVersionResource{Group: "operator.victoriametrics.com", Version: "v1beta1", Resource: "vmagents"}
	agent := &unstructured.Unstructured{}
	agent.SetAPIVersion(gvr.Group + "/" + gvr.Version)
	agent.SetKind("VMAgent")
	agent.SetNamespace("team")
	agent.SetName("stuck")
	agent.SetUID("vmagent-uid")
	agent.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-24 * time.Hour)))
	agent.SetDeletionTimestamp(&metav1.Time{Time: time.Now().Add(-2 * time.Hour)})
	agent.SetFinalizers([]string{"apps.victoriametrics.com/finalizer"})
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "VMAgentList"}, agent)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "VMAgent", Name: gvr.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dc.WaitForSync(gvr, 2*time.Second) {
		t.Fatal("VMAgent cache did not sync")
	}
	controller := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "private-controller", Namespace: "operators", Labels: map[string]string{"app.kubernetes.io/name": "victoria-metrics-operator"}},
		Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "vm-controller"}}},
	}
	ready := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "private-controller-pod", Namespace: "operators", Labels: map[string]string{"app": "vm-controller"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}},
	}
	crashing := ready.DeepCopy()
	crashing.Status.ContainerStatuses = []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}
	scopes := map[string]k8score.ResourceScope{"pods": {Enabled: true}, "deployments": {Enabled: true}, "statefulsets": {Enabled: true}, "daemonsets": {Enabled: true}}
	for _, tc := range []struct {
		name        string
		objects     []runtime.Object
		all         bool
		want        Severity
		wantCleanup bool
	}{
		{"namespace only with a hidden healthy controller", []runtime.Object{controller, ready}, false, SeverityWarning, false},
		{"namespace only with a hidden crashing controller", []runtime.Object{controller, crashing}, false, SeverityWarning, false},
		{"namespace only with no controller", nil, false, SeverityWarning, false},
		{"full inventory with a healthy controller", []runtime.Object{controller, ready}, true, SeverityWarning, false},
		{"full inventory with a crashing controller", []runtime.Object{controller, crashing}, true, SeverityCritical, false},
		{"full inventory with no controller", nil, true, SeverityWarning, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := k8s.InitScopedTestResourceCache(fake.NewClientset(tc.objects...), scopes); err != nil {
				t.Fatal(err)
			}
			out := Compose(NewCacheProvider(), Filters{
				Namespaces:      []string{"team"},
				CanReadEvidence: func(read EvidenceRead) bool { return tc.all || read.Namespace == "team" },
			})
			if len(out) != 1 || out[0].Category != issuesapi.CategoryTerminationStuck || out[0].Severity != tc.want || (out[0].ConditionalCleanup != nil) != tc.wantCleanup {
				t.Fatalf("termination verdict=%+v", out)
			}
			if !tc.all && (strings.Contains(out[0].Cause, "private-controller") || strings.Contains(out[0].Cause, "operators") || strings.Contains(out[0].Cause, "CrashLoopBackOff")) {
				t.Fatalf("unreadable controller evidence leaked: %+v", out[0])
			}
		})
	}
}

func TestMixedFinalizersRetainDownControllerGuidance(t *testing.T) {
	p := &observingFinalizerProvider{byFinalizer: map[string]finalizerObservation{
		"example.org/healthy":  {Controller: "healthy-controller", Running: true, Healthy: true, Text: "healthy-controller is healthy"},
		"example.org/crashing": {Controller: "crashing-controller", Down: true, State: "CrashLoopBackOff", Text: "crashing-controller is CrashLoopBackOff"},
	}}
	problem := k8s.Detection{Kind: "Widget", Severity: "critical", TerminatingFinalizers: []string{"example.org/healthy", "example.org/crashing"}}
	problem = enrichTerminatingProblem(problem, p, nil, map[string]finalizerObservation{})
	if problem.Severity != "high" || problem.ConditionalCleanup != nil || !strings.Contains(problem.Action, "Fix crashing-controller") || !strings.Contains(problem.Action, "matching controller is running") {
		t.Fatalf("retain the mixed-finalizer warning policy and both next steps: %+v", problem)
	}
}

func TestControllerObservationMemoSharedByCatalogOwner(t *testing.T) {
	p := &observingFinalizerProvider{observation: finalizerObservation{Controller: "victoria-metrics-operator", Running: true, Healthy: true}}
	memo := map[string]finalizerObservation{}
	for _, kind := range []string{"VMAgent", "VMSingle", "VMCluster"} {
		enrichTerminatingProblem(k8s.Detection{Group: "operator.victoriametrics.com", Kind: kind, TerminatingFinalizers: []string{"apps.victoriametrics.com/finalizer"}}, p, nil, memo)
	}
	if p.calls != 1 {
		t.Fatalf("one catalog owner must be observed once per composition, got %d", p.calls)
	}
}

func TestControllerObservationMemoDistinguishesFluxImageOwners(t *testing.T) {
	p := &observingFinalizerProvider{}
	memo := map[string]finalizerObservation{}
	for _, kind := range []string{"ImageUpdateAutomation", "ImageRepository"} {
		enrichTerminatingProblem(k8s.Detection{Group: "image.toolkit.fluxcd.io", Kind: kind, TerminatingFinalizers: []string{"finalizers.fluxcd.io"}}, p, nil, memo)
	}
	if p.calls != 2 {
		t.Fatalf("image automation and reflector are different owners, got %d observations", p.calls)
	}
}

func TestMixedFinalizersRetainOtherControllerGuidance(t *testing.T) {
	for _, tc := range []struct {
		name  string
		other finalizerObservation
		want  string
	}{
		{"restore", finalizerObservation{Controller: "stopped-controller", NotFound: true}, "Get stopped-controller running again"},
		{"unconfirmed", finalizerObservation{Controller: "hidden-controller"}, "can't confirm with your access"},
		{"candidate", finalizerObservation{Candidate: "possible-controller"}, "resembles its API group"},
		{"unidentified", finalizerObservation{}, "couldn't identify which controller"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &observingFinalizerProvider{byFinalizer: map[string]finalizerObservation{
				"example.org/healthy": {Controller: "healthy-controller", Running: true, Healthy: true},
				"example.org/other":   tc.other,
			}}
			problem := k8s.Detection{Kind: "Widget", Severity: "critical", TerminatingFinalizers: []string{"example.org/healthy", "example.org/other"}}
			problem = enrichTerminatingProblem(problem, p, nil, map[string]finalizerObservation{})
			if problem.Severity != "high" || problem.ConditionalCleanup != nil || !strings.Contains(problem.Action, tc.want) || !strings.Contains(problem.Action, "matching controller is running") {
				t.Fatalf("retain every finalizer's guidance without cleanup: %+v", problem)
			}
		})
	}
}

func TestFluxHelmIgnoresUnrelatedHelmController(t *testing.T) {
	for _, state := range []struct {
		name   string
		status corev1.ContainerStatus
	}{
		{"healthy", corev1.ContainerStatus{Ready: true}},
		{"crashing", corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}},
	} {
		t.Run(state.name, func(t *testing.T) {
			t.Cleanup(k8s.ResetResourceCache)
			identity := map[string]string{"app": "helm-controller"}
			template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: identity}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Image: "rancher/helm-controller:v0.16.0"}}}}
			deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "helm-controller", Namespace: "kube-system", Labels: identity}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: identity}, Template: template}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "helm-controller-pod", Namespace: "kube-system", Labels: identity}, Spec: template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{state.status}}}
			scopes := map[string]k8score.ResourceScope{"pods": {Enabled: true}, "deployments": {Enabled: true}, "statefulsets": {Enabled: true}, "daemonsets": {Enabled: true}}
			if err := k8s.InitScopedTestResourceCache(fake.NewClientset(deployment, pod), scopes); err != nil {
				t.Fatal(err)
			}
			provider := NewCacheProvider()
			observation := provider.FinalizerOwnerStatus("finalizers.helm.toolkit.fluxcd.io", Ref{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease"}, nil)
			if observation.NotFound || observation.Running || observation.Down || !strings.Contains(observation.Text, "unconfirmed image identity") {
				t.Fatalf("unrelated Helm controller must not establish Flux health: %+v", observation)
			}
		})
	}
}

func TestUnconfirmedHelmImagesNeverOfferCleanup(t *testing.T) {
	for _, image := range []string{"registry.corp/helm-controller:v1", "ghcr.io/vendor/distroless/helm-controller:v1"} {
		for _, bare := range []bool{false, true} {
			t.Run(image+map[bool]string{false: "/workload", true: "/bare-pod"}[bare], func(t *testing.T) {
				t.Cleanup(k8s.ResetResourceCache)
				identity := map[string]string{"app": "helm-controller"}
				template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: identity}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Image: image}}}}
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "helm-controller-pod", Namespace: "operators", Labels: identity}, Spec: template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}}
				objects := []runtime.Object{pod}
				if !bare {
					objects = append(objects, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "helm-controller", Namespace: "operators", Labels: identity}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: identity}, Template: template}})
				}
				scopes := map[string]k8score.ResourceScope{"pods": {Enabled: true}, "deployments": {Enabled: true}, "statefulsets": {Enabled: true}, "daemonsets": {Enabled: true}}
				if err := k8s.InitScopedTestResourceCache(fake.NewClientset(objects...), scopes); err != nil {
					t.Fatal(err)
				}
				key := "finalizers.helm.toolkit.fluxcd.io"
				problem := k8s.Detection{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Severity: "critical", TerminatingFinalizers: []string{key}, TerminatingRemovals: map[string]issuesapi.FinalizerRemoval{key: {Finalizer: key, PatchResource: []byte(`{}`)}}}
				problem = enrichTerminatingProblem(problem, NewCacheProvider(), nil, map[string]finalizerObservation{})
				if problem.Severity != "high" || problem.ConditionalCleanup != nil || !strings.Contains(problem.Cause, "unconfirmed image identity") || !strings.Contains(problem.Action, "can't confirm") {
					t.Fatalf("a matching name with an unknown image must block absence and cleanup: %+v", problem)
				}
			})
		}
	}
}
