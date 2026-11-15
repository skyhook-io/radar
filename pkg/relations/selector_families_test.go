package relations

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func labelled(obj runtime.Object, set map[string]string) runtime.Object {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		o.Spec.Template.Labels = set
	case *appsv1.DaemonSet:
		o.Spec.Template.Labels = set
	case *batchv1.Job:
		o.Spec.Template.Labels = set
	case *corev1.Service:
		o.Labels = set
	}
	return obj
}

func job(name string, active int32) runtime.Object {
	j := &batchv1.Job{ObjectMeta: meta(name)}
	j.Status.Active = active
	return typed(j, "batch", "v1", "Job")
}

// checkSelects asks f both ways about selector in a cluster of objects: what
// it selects must be want, and each of want must be selected by it.
func checkSelects(t *testing.T, f *SelectorFamily, objects []runtime.Object, selector runtime.Object, want ...runtime.Object) {
	t.Helper()
	v := NewView(allNamespaces{cluster: cluster{objects: objects}}, ownership.Unrestricted())
	var refs []resourceid.Ref
	for _, w := range want {
		refs = append(refs, refOf(w))
	}
	if got := v.Selected(f, refOf(selector), nil); !slices.Equal(got.Matches, refs) || !got.Coverage.Complete() {
		t.Errorf("%s: %v selects %+v; want %v", f.Name, refOf(selector), got, refs)
	}
	for _, w := range want {
		if got := v.SelectedBy(f, refOf(w), nil); !slices.Contains(got.Matches, refOf(selector)) {
			t.Errorf("%s: %v is selected by %v; want %v among them", f.Name, refOf(w), got.Matches, refOf(selector))
		}
	}
}

func TestServicesSelectTheWorkloadsWhosePodsTheyWouldServe(t *testing.T) {
	web := labelled(deployment("web", mounts()), map[string]string{"app": "web", "tier": "front"})
	other := labelled(typed(&appsv1.DaemonSet{ObjectMeta: meta("agent")}, "apps", "v1", "DaemonSet"), map[string]string{"app": "agent"})
	running := labelled(job("migrate", 1), map[string]string{"app": "web"})
	finished := labelled(job("seed", 0), map[string]string{"app": "web"})
	svc := typed(&corev1.Service{ObjectMeta: meta("web"), Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}}, "", "v1", "Service")
	checkSelects(t, ServiceSelectors, []runtime.Object{web, other, running, finished, svc}, svc, web, running)

	headless := typed(&corev1.Service{ObjectMeta: meta("external")}, "", "v1", "Service")
	if got := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{web, headless}}}, ownership.Unrestricted()).Selected(ServiceSelectors, refOf(headless), nil); len(got.Matches) != 0 {
		t.Errorf("a Service without a selector selects %v", got.Matches)
	}
}

func TestEmptyPolicySelectorsChooseEveryWorkloadInTheirNamespace(t *testing.T) {
	web := labelled(deployment("web", mounts()), map[string]string{"app": "web"})
	elsewhere := labelled(deployment("api", mounts()), map[string]string{"app": "api"})
	elsewhere.(*appsv1.Deployment).Namespace = "team"
	deny := typed(&networkingv1.NetworkPolicy{ObjectMeta: meta("default-deny")}, "networking.k8s.io", "v1", "NetworkPolicy")
	checkSelects(t, NetworkPolicies, []runtime.Object{web, elsewhere, deny}, deny, web)

	all := typed(&policyv1.PodDisruptionBudget{ObjectMeta: meta("all"), Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{}}}, "policy", "v1", "PodDisruptionBudget")
	checkSelects(t, DisruptionBudgets, []runtime.Object{web, all}, all, web)
	none := typed(&policyv1.PodDisruptionBudget{ObjectMeta: meta("none")}, "policy", "v1", "PodDisruptionBudget")
	checkSelects(t, DisruptionBudgets, []runtime.Object{web, none}, none)
}

func TestCiliumPoliciesMatchExpressions(t *testing.T) {
	web := labelled(deployment("web", mounts()), map[string]string{"app": "web"})
	cnp := object("cilium.io/v2", "CiliumNetworkPolicy", "web", map[string]any{"endpointSelector": map[string]any{
		"matchExpressions": []any{map[string]any{"key": "app", "operator": "In", "values": []any{"web", "api"}}},
	}})
	checkSelects(t, NetworkPolicies, []runtime.Object{web, cnp}, cnp, web)
}

func TestMonitorsSelectAcrossTheNamespacesTheyName(t *testing.T) {
	svc := labelled(typed(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "web"}}, "", "v1", "Service"), map[string]string{"metrics": "on"})
	unnamed := labelled(typed(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "web"}}, "", "v1", "Service"), map[string]string{"metrics": "on"})
	sm := inNamespace(object("monitoring.coreos.com/v1", "ServiceMonitor", "web", map[string]any{
		"selector":          map[string]any{"matchLabels": map[string]any{"metrics": "on"}},
		"namespaceSelector": map[string]any{"matchNames": []any{"apps"}},
	}), "monitoring")
	objects := []runtime.Object{svc, unnamed, sm}
	checkSelects(t, Monitors, objects, sm, svc)
	if got := NewView(allNamespaces{cluster: cluster{objects: objects}}, ownership.Unrestricted()).SelectedBy(Monitors, refOf(unnamed), nil); len(got.Matches) != 0 {
		t.Errorf("a Service in a namespace the monitor doesn't name is selected by %v", got.Matches)
	}

	local := inNamespace(object("monitoring.coreos.com/v1", "ServiceMonitor", "local", map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"metrics": "on"}}}), "apps")
	checkSelects(t, Monitors, []runtime.Object{svc, unnamed, local}, local, svc)

	// A caller who may read only apps sees the Service's monitors there.
	restricted := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{svc, sm, local}}}, namespaceAccess{namespace: "apps"}).SelectedBy(Monitors, refOf(svc), nil)
	if !slices.Equal(restricted.Matches, []resourceid.Ref{refOf(local)}) || !restricted.Coverage.Narrowed {
		t.Errorf("a caller reading only apps got %+v", restricted)
	}
}

func TestSelectedAnswersAreCappedAndCounted(t *testing.T) {
	var objects []runtime.Object
	for _, name := range []string{"a", "b", "c"} {
		objects = append(objects, labelled(deployment(name, mounts()), map[string]string{"app": "x"}))
	}
	svc := typed(&corev1.Service{ObjectMeta: meta("x"), Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "x"}}}, "", "v1", "Service")
	objects = append(objects, svc)
	got := NewView(allNamespaces{cluster: cluster{objects: objects}}, ownership.Unrestricted()).WithLimit(2).Selected(ServiceSelectors, refOf(svc), nil)
	if len(got.Matches) != 2 || !got.Coverage.Truncated || got.Coverage.Total != 3 {
		t.Errorf("capped at 2 got %+v", got)
	}
}
