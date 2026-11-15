package relations

import (
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

var (
	kindDeployment   = resourceid.GroupKind{Group: "apps", Kind: "Deployment"}
	kindStatefulSet  = resourceid.GroupKind{Group: "apps", Kind: "StatefulSet"}
	kindDaemonSet    = resourceid.GroupKind{Group: "apps", Kind: "DaemonSet"}
	kindReplicaSet   = resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"}
	kindRollout      = resourceid.GroupKind{Group: configrefs.ArgoRolloutsGroup, Kind: "Rollout"}
	kindService      = resourceid.GroupKind{Kind: "Service"}
	kindSecret       = resourceid.GroupKind{Kind: "Secret"}
	kindScaledObject = resourceid.GroupKind{Group: "keda.sh", Kind: "ScaledObject"}
)

// unstructuredOf returns obj when it is an unstructured object of gk.
func unstructuredOf(obj runtime.Object, gk resourceid.GroupKind) (*unstructured.Unstructured, bool) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || u.GetKind() != gk.Kind || resourceid.GroupFromAPIVersion(u.GetAPIVersion()) != gk.Group {
		return nil, false
	}
	return u, true
}

func visitPathRef(visit func(Declaration), r configrefs.PathRef) {
	visit(Declaration{Target: r.Ref, Path: r.Path})
}

// Autoscalers is the family of the workloads a HorizontalPodAutoscaler,
// VerticalPodAutoscaler or KEDA ScaledObject scales.
var Autoscalers = &Family{
	Name: "autoscalers",
	Sources: []Source{
		{Kind: resourceid.GroupKind{Group: "autoscaling", Kind: "HorizontalPodAutoscaler"}, Targets: []resourceid.GroupKind{kindDeployment, kindStatefulSet, kindReplicaSet, kindRollout}},
		{Kind: resourceid.GroupKind{Group: "autoscaling.k8s.io", Kind: "VerticalPodAutoscaler"}},
		{Kind: kindScaledObject, Targets: []resourceid.GroupKind{kindDeployment, kindStatefulSet, kindReplicaSet, kindRollout}},
	},
	Targets: []resourceid.GroupKind{kindDeployment, kindStatefulSet, kindDaemonSet, kindReplicaSet, kindRollout},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if hpa, ok := obj.(*autoscalingv2.HorizontalPodAutoscaler); ok {
			if r, ok := configrefs.HPAScaleTarget(hpa); ok {
				visitPathRef(visit, r)
			}
			return nil
		}
		if u, ok := unstructuredOf(obj, resourceid.GroupKind{Group: "autoscaling.k8s.io", Kind: "VerticalPodAutoscaler"}); ok {
			if r, ok := configrefs.VPATarget(u); ok {
				visitPathRef(visit, r)
			}
			return nil
		}
		if u, ok := unstructuredOf(obj, kindScaledObject); ok {
			if r, ok := configrefs.KEDAScaleTargetRef(u); ok {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}

// KEDAAuthentication is the family of the TriggerAuthentications and
// ClusterTriggerAuthentications a KEDA ScaledObject or ScaledJob's triggers
// use.
var KEDAAuthentication = &Family{
	Name: "kedaAuthentication",
	Sources: []Source{
		{Kind: kindScaledObject},
		{Kind: resourceid.GroupKind{Group: "keda.sh", Kind: "ScaledJob"}},
	},
	Targets: []resourceid.GroupKind{{Group: "keda.sh", Kind: "TriggerAuthentication"}, {Group: "keda.sh", Kind: "ClusterTriggerAuthentication"}},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil
		}
		for _, r := range configrefs.KEDAAuthenticationReferences(u) {
			visit(Declaration{Target: r, Path: "spec.triggers[].authenticationRef"})
		}
		return nil
	},
}

// IngressBackends is the family of the Services an Ingress routes to.
var IngressBackends = &Family{
	Name:    "ingressBackends",
	Sources: []Source{{Kind: resourceid.GroupKind{Group: "networking.k8s.io", Kind: "Ingress"}}},
	Targets: []resourceid.GroupKind{kindService},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		ing, ok := obj.(*networkingv1.Ingress)
		if !ok {
			return nil
		}
		for _, r := range configrefs.IngressBackendServiceRefs(ing) {
			visitPathRef(visit, r)
		}
		return nil
	},
}

// RolloutServices is the family of the Services an Argo Rollout's strategy
// names for its canary and stable, or active and preview, traffic.
var RolloutServices = &Family{
	Name:    "rolloutServices",
	Sources: []Source{{Kind: kindRollout}},
	Targets: []resourceid.GroupKind{kindService},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, kindRollout); ok {
			for _, r := range configrefs.RolloutServices(u) {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}

// CertificateSecrets is the family of the Secret a cert-manager Certificate
// writes its certificate to.
var CertificateSecrets = &Family{
	Name:    "certificateSecrets",
	Sources: []Source{{Kind: resourceid.GroupKind{Group: "cert-manager.io", Kind: "Certificate"}}},
	Targets: []resourceid.GroupKind{kindSecret},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, resourceid.GroupKind{Group: "cert-manager.io", Kind: "Certificate"}); ok {
			if r, ok := configrefs.CertificateSecret(u); ok {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}
