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
	kindGateway      = resourceid.GroupKind{Group: configrefs.GatewayAPIGroup, Kind: "Gateway"}
	kindCertificate  = resourceid.GroupKind{Group: configrefs.CertManagerGroup, Kind: "Certificate"}
)

// gatewayRoutes are the Gateway API route kinds, as sources.
var gatewayRoutes = func() []Source {
	var out []Source
	for _, kind := range configrefs.GatewayRouteKinds {
		out = append(out, Source{Kind: resourceid.GroupKind{Group: configrefs.GatewayAPIGroup, Kind: kind}})
	}
	return out
}()

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
	Sources: []Source{{Kind: kindCertificate}},
	Targets: []resourceid.GroupKind{kindSecret},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, kindCertificate); ok {
			if r, ok := configrefs.CertificateSecret(u); ok {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}

// GatewayParents is the family of the Gateways a Gateway API route attaches
// to, which may be in another namespace.
var GatewayParents = &Family{
	Name:           "gatewayParents",
	Sources:        gatewayRoutes,
	Targets:        []resourceid.GroupKind{kindGateway},
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			for _, r := range configrefs.GatewayRouteParents(u) {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}

// GatewayBackends is the family of the Services a Gateway API route sends
// traffic to, which may be in another namespace. A cross-namespace backend is
// declared whether or not a ReferenceGrant permits it.
var GatewayBackends = &Family{
	Name:           "gatewayBackends",
	Sources:        gatewayRoutes,
	Targets:        []resourceid.GroupKind{kindService},
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			for _, r := range configrefs.GatewayRouteBackendServices(u) {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}

// GatewayClasses is the family of the GatewayClass a Gateway names.
var GatewayClasses = &Family{
	Name:    "gatewayClasses",
	Sources: []Source{{Kind: kindGateway}},
	Targets: []resourceid.GroupKind{{Group: configrefs.GatewayAPIGroup, Kind: "GatewayClass"}},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, kindGateway); ok {
			if r, ok := configrefs.GatewayClassOf(u); ok {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}

// CertManagerIssuers is the family of the Issuer or ClusterIssuer a
// cert-manager Certificate, CertificateRequest, ACME Order or Challenge
// names.
var CertManagerIssuers = &Family{
	Name: "certManagerIssuers",
	Sources: []Source{
		{Kind: kindCertificate},
		{Kind: resourceid.GroupKind{Group: configrefs.CertManagerGroup, Kind: "CertificateRequest"}},
		{Kind: resourceid.GroupKind{Group: configrefs.CertManagerACMEGroup, Kind: "Order"}},
		{Kind: resourceid.GroupKind{Group: configrefs.CertManagerACMEGroup, Kind: "Challenge"}},
	},
	Targets: []resourceid.GroupKind{{Group: configrefs.CertManagerGroup, Kind: "Issuer"}, {Group: configrefs.CertManagerGroup, Kind: "ClusterIssuer"}},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			if r, ok := configrefs.CertManagerIssuer(u); ok {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}
