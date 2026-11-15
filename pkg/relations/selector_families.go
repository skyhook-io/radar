package relations

import (
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

var (
	kindJob     = resourceid.GroupKind{Group: "batch", Kind: "Job"}
	kindCronJob = resourceid.GroupKind{Group: "batch", Kind: "CronJob"}
)

// Workloads present their pod template's labels: what a selector that
// chooses pods would choose them by.
var (
	selectDeployment = Selectable{Kind: kindDeployment, Labels: func(obj runtime.Object) (labels.Set, bool) {
		d, ok := obj.(*appsv1.Deployment)
		return templateLabels(ok, func() map[string]string { return d.Spec.Template.Labels })
	}}
	selectStatefulSet = Selectable{Kind: kindStatefulSet, Labels: func(obj runtime.Object) (labels.Set, bool) {
		s, ok := obj.(*appsv1.StatefulSet)
		return templateLabels(ok, func() map[string]string { return s.Spec.Template.Labels })
	}}
	selectDaemonSet = Selectable{Kind: kindDaemonSet, Labels: func(obj runtime.Object) (labels.Set, bool) {
		d, ok := obj.(*appsv1.DaemonSet)
		return templateLabels(ok, func() map[string]string { return d.Spec.Template.Labels })
	}}
	// A finished Job has no pods left to choose.
	selectActiveJob = Selectable{Kind: kindJob, Labels: func(obj runtime.Object) (labels.Set, bool) {
		j, ok := obj.(*batchv1.Job)
		return templateLabels(ok && j.Status.Active > 0, func() map[string]string { return j.Spec.Template.Labels })
	}}
	selectCronJob = Selectable{Kind: kindCronJob, Labels: func(obj runtime.Object) (labels.Set, bool) {
		c, ok := obj.(*batchv1.CronJob)
		return templateLabels(ok, func() map[string]string { return c.Spec.JobTemplate.Spec.Template.Labels })
	}}
	selectRollout = Selectable{Kind: kindRollout, Labels: func(obj runtime.Object) (labels.Set, bool) {
		u, ok := unstructuredOf(obj, kindRollout)
		if !ok {
			return nil, false
		}
		set, _, _ := unstructured.NestedStringMap(u.Object, "spec", "template", "metadata", "labels")
		return set, true
	}}
	selectService = Selectable{Kind: kindService, Labels: func(obj runtime.Object) (labels.Set, bool) {
		s, ok := obj.(*corev1.Service)
		return templateLabels(ok, func() map[string]string { return s.Labels })
	}}
)

func templateLabels(ok bool, get func() map[string]string) (labels.Set, bool) {
	if !ok {
		return nil, false
	}
	return get(), true
}

// labelSelection is a metav1.LabelSelector's selection: a nil one chooses
// nothing, and an empty one everything, as the API defines both.
func labelSelection(sel *metav1.LabelSelector) (Selection, bool, error) {
	if sel == nil {
		return Selection{}, false, nil
	}
	parsed, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return Selection{}, false, err
	}
	return Selection{Labels: parsed}, true, nil
}

// unstructuredSelection reads a metav1.LabelSelector at fields of u.
func unstructuredSelection(u *unstructured.Unstructured, fields ...string) (Selection, bool, error) {
	raw, found, err := unstructured.NestedMap(u.Object, fields...)
	if err != nil || !found {
		return Selection{}, false, err
	}
	var sel metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &sel); err != nil {
		return Selection{}, false, err
	}
	return labelSelection(&sel)
}

// ServiceSelectors is the family of the workloads a Service's selector
// chooses: those whose pod template it matches. A Service without a
// selector chooses none. A Rollout's canary, stable, active and preview
// Services are chosen by name (RolloutServices), not by selector.
var ServiceSelectors = &SelectorFamily{
	Name: "serviceSelectors",
	Selectors: []Selector{{Kind: kindService, Selection: func(obj runtime.Object) (Selection, bool, error) {
		svc, ok := obj.(*corev1.Service)
		if !ok || len(svc.Spec.Selector) == 0 {
			return Selection{}, false, nil
		}
		return Selection{Labels: labels.SelectorFromSet(svc.Spec.Selector)}, true, nil
	}}},
	Selectables: []Selectable{selectDeployment, selectRollout, selectStatefulSet, selectDaemonSet, selectActiveJob},
}

// DisruptionBudgets is the family of the workloads a PodDisruptionBudget
// protects: those whose pod template its selector matches.
var DisruptionBudgets = &SelectorFamily{
	Name: "disruptionBudgets",
	Selectors: []Selector{{Kind: resourceid.GroupKind{Group: "policy", Kind: "PodDisruptionBudget"}, Selection: func(obj runtime.Object) (Selection, bool, error) {
		pdb, ok := obj.(*policyv1.PodDisruptionBudget)
		if !ok {
			return Selection{}, false, nil
		}
		return labelSelection(pdb.Spec.Selector)
	}}},
	Selectables: []Selectable{selectDeployment, selectStatefulSet, selectDaemonSet},
}

// NetworkPolicies is the family of the workloads a NetworkPolicy or
// CiliumNetworkPolicy applies to: those whose pod template its pod or
// endpoint selector matches. An empty selector applies to every workload in
// the policy's namespace, as a default-deny policy does.
var NetworkPolicies = &SelectorFamily{
	Name: "networkPolicies",
	Selectors: []Selector{
		{Kind: resourceid.GroupKind{Group: "networking.k8s.io", Kind: "NetworkPolicy"}, Selection: func(obj runtime.Object) (Selection, bool, error) {
			np, ok := obj.(*networkingv1.NetworkPolicy)
			if !ok {
				return Selection{}, false, nil
			}
			return labelSelection(&np.Spec.PodSelector)
		}},
		{Kind: resourceid.GroupKind{Group: "cilium.io", Kind: "CiliumNetworkPolicy"}, Selection: func(obj runtime.Object) (Selection, bool, error) {
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return Selection{}, false, nil
			}
			return unstructuredSelection(u, "spec", "endpointSelector")
		}},
	},
	Selectables: []Selectable{selectDeployment, selectStatefulSet, selectDaemonSet},
}

// monitorSelection is a Prometheus Operator monitor's selection: its
// selector, in the namespaces its namespaceSelector names, any namespace,
// or its own.
func monitorSelection(obj runtime.Object) (Selection, bool, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return Selection{}, false, nil
	}
	s, ok, err := unstructuredSelection(u, "spec", "selector")
	if !ok {
		return s, ok, err
	}
	if anyNamespace, _, _ := unstructured.NestedBool(u.Object, "spec", "namespaceSelector", "any"); anyNamespace {
		s.InNamespace = func(string) bool { return true }
	} else if names, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "namespaceSelector", "matchNames"); len(names) > 0 {
		s.InNamespace = func(namespace string) bool { return slices.Contains(names, namespace) }
	}
	return s, true, nil
}

// Monitors is the family of what a Prometheus Operator ServiceMonitor or
// PodMonitor scrapes: the Services a ServiceMonitor selects, and the
// workloads whose pod template a PodMonitor selects, in the namespaces its
// namespaceSelector names.
var Monitors = &SelectorFamily{
	Name: "monitors",
	Selectors: []Selector{
		{Kind: resourceid.GroupKind{Group: "monitoring.coreos.com", Kind: "ServiceMonitor"}, Selects: []resourceid.GroupKind{kindService}, Selection: monitorSelection},
		{Kind: resourceid.GroupKind{Group: "monitoring.coreos.com", Kind: "PodMonitor"}, Selects: []resourceid.GroupKind{kindDeployment, kindRollout, kindStatefulSet, kindDaemonSet, kindJob, kindCronJob}, Selection: monitorSelection},
	},
	Selectables:    []Selectable{selectService, selectDeployment, selectRollout, selectStatefulSet, selectDaemonSet, selectActiveJob, selectCronJob},
	CrossNamespace: true,
}
