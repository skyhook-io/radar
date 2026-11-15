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

	"github.com/skyhook-io/radar/pkg/netpol"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/rollouts"
)

var (
	kindJob     = resourceid.GroupKind{Group: "batch", Kind: "Job"}
	kindCronJob = resourceid.GroupKind{Group: "batch", Kind: "CronJob"}
)

// podTemplate is a workload as a selector sees it: the pods its template
// makes.
func podTemplate(t corev1.PodTemplateSpec) (Candidate, bool) {
	return Candidate{Labels: t.Labels, ServiceAccount: t.Spec.ServiceAccountName}, true
}

// Workloads are chosen by their pod template.
var (
	selectDeployment = Selectable{Kind: kindDeployment, Candidate: func(obj runtime.Object, _ Lookup) (Candidate, bool) {
		if d, ok := obj.(*appsv1.Deployment); ok {
			return podTemplate(d.Spec.Template)
		}
		return Candidate{}, false
	}}
	selectStatefulSet = Selectable{Kind: kindStatefulSet, Candidate: func(obj runtime.Object, _ Lookup) (Candidate, bool) {
		if s, ok := obj.(*appsv1.StatefulSet); ok {
			return podTemplate(s.Spec.Template)
		}
		return Candidate{}, false
	}}
	selectDaemonSet = Selectable{Kind: kindDaemonSet, Candidate: func(obj runtime.Object, _ Lookup) (Candidate, bool) {
		if d, ok := obj.(*appsv1.DaemonSet); ok {
			return podTemplate(d.Spec.Template)
		}
		return Candidate{}, false
	}}
	// A finished Job has no pods left to choose.
	selectActiveJob = Selectable{Kind: kindJob, Candidate: func(obj runtime.Object, _ Lookup) (Candidate, bool) {
		if j, ok := obj.(*batchv1.Job); ok && j.Status.Active > 0 {
			return podTemplate(j.Spec.Template)
		}
		return Candidate{}, false
	}}
	selectCronJob = Selectable{Kind: kindCronJob, Candidate: func(obj runtime.Object, _ Lookup) (Candidate, bool) {
		if c, ok := obj.(*batchv1.CronJob); ok {
			return podTemplate(c.Spec.JobTemplate.Spec.Template)
		}
		return Candidate{}, false
	}}
	selectRollout = Selectable{Kind: kindRollout, Candidate: rolloutCandidate}
	selectService = Selectable{Kind: kindService, Candidate: func(obj runtime.Object, _ Lookup) (Candidate, bool) {
		if s, ok := obj.(*corev1.Service); ok {
			return Candidate{Labels: s.Labels}, true
		}
		return Candidate{}, false
	}}
)

// rolloutWorkloadGroups are the kinds an Argo Rollout's workloadRef may
// take its pod template from, by group.
var rolloutWorkloadGroups = map[string]string{"Deployment": "apps", "ReplicaSet": "apps", "PodTemplate": ""}

// rolloutCandidate is a Rollout's pods: its own template, or, for a Rollout
// whose workloadRef names one, the referenced workload's (an auxiliary read:
// see Lookup).
func rolloutCandidate(obj runtime.Object, lookup Lookup) (Candidate, bool) {
	u, ok := unstructuredOf(obj, kindRollout)
	if !ok {
		return Candidate{}, false
	}
	kind, name, ref := rollouts.WorkloadRef(u)
	if !ref {
		return unstructuredTemplate(u, []string{"spec", "template"})
	}
	group, known := rolloutWorkloadGroups[kind]
	if !known {
		return Candidate{}, false
	}
	workload, found := lookup(resourceid.NewRef(group, kind, u.GetNamespace(), name))
	if !found {
		return Candidate{}, false
	}
	switch w := workload.(type) {
	case *appsv1.Deployment:
		return podTemplate(w.Spec.Template)
	case *appsv1.ReplicaSet:
		return podTemplate(w.Spec.Template)
	case *corev1.PodTemplate:
		return podTemplate(w.Template)
	case *unstructured.Unstructured:
		// Kinds Radar doesn't keep typed, as PodTemplates, come from the
		// dynamic cache.
		path := []string{"spec", "template"}
		if kind == "PodTemplate" {
			path = []string{"template"}
		}
		return unstructuredTemplate(w, path)
	}
	return Candidate{}, false
}

func unstructuredTemplate(u *unstructured.Unstructured, path []string) (Candidate, bool) {
	podLabels, _, _ := unstructured.NestedStringMap(u.Object, slices.Concat(path, []string{"metadata", "labels"})...)
	serviceAccount, _, _ := unstructured.NestedString(u.Object, slices.Concat(path, []string{"spec", "serviceAccountName"})...)
	return Candidate{Labels: podLabels, ServiceAccount: serviceAccount}, true
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
	return LabelSelection(parsed), true, nil
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
// selector, or an ExternalName Service, whose selector Kubernetes ignores,
// chooses none. A Rollout's canary, stable, active and preview Services are
// chosen by name (RolloutServices), not by selector.
var ServiceSelectors = &SelectorFamily{
	Name:       "serviceSelectors",
	Projection: project("exposes", false, "Backends", "Services"),
	Selectors: []Selector{{Kind: kindService, Selection: func(obj runtime.Object) (Selection, bool, error) {
		svc, ok := obj.(*corev1.Service)
		if !ok || len(svc.Spec.Selector) == 0 || svc.Spec.Type == corev1.ServiceTypeExternalName {
			return Selection{}, false, nil
		}
		return LabelSelection(labels.SelectorFromSet(svc.Spec.Selector)), true, nil
	}}},
	Selectables: []Selectable{selectDeployment, selectRollout, selectStatefulSet, selectDaemonSet, selectActiveJob},
}

// DisruptionBudgets is the family of the workloads a PodDisruptionBudget
// protects: those whose pod template its selector matches.
var DisruptionBudgets = &SelectorFamily{
	Name:       "disruptionBudgets",
	Projection: project("protects", false, "Protects", "PDBs"),
	Selectors: []Selector{{Kind: resourceid.GroupKind{Group: "policy", Kind: "PodDisruptionBudget"}, Selection: func(obj runtime.Object) (Selection, bool, error) {
		pdb, ok := obj.(*policyv1.PodDisruptionBudget)
		if !ok {
			return Selection{}, false, nil
		}
		return labelSelection(pdb.Spec.Selector)
	}}},
	Selectables: []Selectable{selectDeployment, selectStatefulSet, selectDaemonSet},
}

// ciliumSelection is a CiliumNetworkPolicy's selection, as Cilium matches
// it (netpol.CiliumSelects): any of its specs' endpoint selectors, with
// Cilium's label sources and the identity labels it derives for a pod. A
// selector on Namespace labels is undecided, as Radar doesn't read them here.
func ciliumSelection(obj runtime.Object) (Selection, bool, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return Selection{}, false, nil
	}
	return Selection{Matches: func(c Candidate, _ Lookup) (bool, bool) {
		return netpol.CiliumSelects(u, netpol.CiliumIdentityOf(c.Ref.Namespace, c.Labels, c.ServiceAccount, nil))
	}}, true, nil
}

// NetworkPolicies is the family of the workloads a NetworkPolicy or
// CiliumNetworkPolicy applies to: those whose pod template its pod or
// endpoint selectors match. An empty selector applies to every workload in
// the policy's namespace, as a default-deny policy does.
var NetworkPolicies = &SelectorFamily{
	Name:       "networkPolicies",
	Projection: project("protects", false, "Protects", "NetworkPolicies"),
	Selectors: []Selector{
		{Kind: resourceid.GroupKind{Group: "networking.k8s.io", Kind: "NetworkPolicy"}, Selection: func(obj runtime.Object) (Selection, bool, error) {
			np, ok := obj.(*networkingv1.NetworkPolicy)
			if !ok {
				return Selection{}, false, nil
			}
			return labelSelection(&np.Spec.PodSelector)
		}},
		{Kind: resourceid.GroupKind{Group: "cilium.io", Kind: "CiliumNetworkPolicy"}, Selection: ciliumSelection},
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
		s.AnyNamespace = true
	} else {
		s.Namespaces, _, _ = unstructured.NestedStringSlice(u.Object, "spec", "namespaceSelector", "matchNames")
	}
	return s, true, nil
}

// Monitors is the family of what a Prometheus Operator ServiceMonitor or
// PodMonitor scrapes: the Services a ServiceMonitor selects, and the
// workloads whose pod template a PodMonitor selects, in the namespaces its
// namespaceSelector names.
var Monitors = &SelectorFamily{
	Name:       "monitors",
	Projection: project("configures", false, "MonitorTargets", "Monitors"),
	Selectors: []Selector{
		{Kind: resourceid.GroupKind{Group: "monitoring.coreos.com", Kind: "ServiceMonitor"}, Selects: []resourceid.GroupKind{kindService}, Selection: monitorSelection},
		{Kind: resourceid.GroupKind{Group: "monitoring.coreos.com", Kind: "PodMonitor"}, Selects: []resourceid.GroupKind{kindDeployment, kindRollout, kindStatefulSet, kindDaemonSet, kindJob, kindCronJob}, Selection: monitorSelection},
	},
	Selectables:    []Selectable{selectService, selectDeployment, selectRollout, selectStatefulSet, selectDaemonSet, selectActiveJob, selectCronJob},
	CrossNamespace: true,
}
