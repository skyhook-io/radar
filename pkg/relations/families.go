package relations

import (
	"strings"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/karpenter"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

var (
	kindDeployment   = resourceid.GroupKind{Group: "apps", Kind: "Deployment"}
	kindStatefulSet  = resourceid.GroupKind{Group: "apps", Kind: "StatefulSet"}
	kindDaemonSet    = resourceid.GroupKind{Group: "apps", Kind: "DaemonSet"}
	kindReplicaSet   = resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"}
	kindRollout      = resourceid.GroupKind{Group: configrefs.ArgoGroup, Kind: "Rollout"}
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
	visit(Declaration{Target: r.Ref, Path: r.Path, Or: r.Or})
}

// Autoscalers is the family of the workloads a HorizontalPodAutoscaler,
// VerticalPodAutoscaler or KEDA ScaledObject scales.
var Autoscalers = &Family{
	Name:       "autoscalers",
	Projection: project("uses", false, "ScaleTarget", "Scalers"),
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
	ReferrersIndex: referrersIndex("kedaAuthentication"),
	Name:           "kedaAuthentication",
	Projection:     project("configures", true, "Dependencies,ConfigRefs", "Consumers,Dependents"),
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
	Name:       "ingressBackends",
	Projection: project("routes-to", false, "Backends", "Ingresses"),
	Sources:    []Source{{Kind: resourceid.GroupKind{Group: "networking.k8s.io", Kind: "Ingress"}}},
	Targets:    []resourceid.GroupKind{kindService},
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
	Name:       "rolloutServices",
	Projection: project("exposes", true, "Services", "Backends"),
	Sources:    []Source{{Kind: kindRollout}},
	Targets:    []resourceid.GroupKind{kindService},
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
	Name:       "certificateSecrets",
	Projection: project("manages", false, "Manages", "Managers"),
	Sources:    []Source{{Kind: kindCertificate}},
	Targets:    []resourceid.GroupKind{kindSecret},
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
	ReferrersIndex: referrersIndex("gatewayParents"),
	Name:           "gatewayParents",
	Projection:     project("routes-to", true, "Gateways", "Backends"),
	Sources:        gatewayRoutes,
	Targets:        []resourceid.GroupKind{kindGateway},
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			configrefs.VisitGatewayRouteParents(u, func(r configrefs.PathRef) { visitPathRef(visit, r) })
		}
		return nil
	},
}

// GatewayBackends is the family of the Services a Gateway API route sends
// traffic to, which may be in another namespace. A cross-namespace backend is
// declared whether or not a ReferenceGrant permits it.
var GatewayBackends = &Family{
	ReferrersIndex: referrersIndex("gatewayBackends"),
	Name:           "gatewayBackends",
	Projection:     project("routes-to", false, "Backends", "Routes"),
	Sources:        gatewayRoutes,
	Targets:        []resourceid.GroupKind{kindService},
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			configrefs.VisitGatewayRouteBackendServices(u, func(r configrefs.PathRef) { visitPathRef(visit, r) })
		}
		return nil
	},
}

// GatewayClasses is the family of the GatewayClass a Gateway names.
var GatewayClasses = &Family{
	ReferrersIndex: referrersIndex("gatewayClasses"),
	Name:           "gatewayClasses",
	Projection:     project("manages", true, "Managers", "Manages"),
	Sources:        []Source{{Kind: kindGateway}},
	Targets:        []resourceid.GroupKind{{Group: configrefs.GatewayAPIGroup, Kind: "GatewayClass"}},
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
	ReferrersIndex: referrersIndex("certManagerIssuers"),
	Name:           "certManagerIssuers",
	Projection:     project("uses", false, "Dependencies,ConfigRefs", "Dependents,Consumers"),
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

func fluxSource(kind string) resourceid.GroupKind {
	return resourceid.GroupKind{Group: "source.toolkit.fluxcd.io", Kind: kind}
}

// FluxSources is the family of the artifacts a Flux Kustomization,
// HelmRelease or HelmChart builds from: its source, chart reference or chart
// source, which a Kustomization or HelmRelease may name in another
// namespace. An inline HelmRelease also declares the HelmChart the controller
// recorded for it.
var FluxSources = &Family{
	ScanAccepted: "Indexing a Flux object's sourceRef exceeds the per-update admission bar (BenchmarkRelationIndexUpdate).",
	Name:         "fluxSources",
	Projection:   project("configures", true, "Dependencies,ConfigRefs", "Consumers,Dependents"),
	Sources: []Source{
		{Kind: resourceid.GroupKind{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization"}, Targets: []resourceid.GroupKind{fluxSource("GitRepository"), fluxSource("OCIRepository"), fluxSource("Bucket"), fluxSource("ExternalArtifact")}},
		{Kind: resourceid.GroupKind{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease"}},
		{Kind: fluxSource("HelmChart"), Targets: []resourceid.GroupKind{fluxSource("HelmRepository"), fluxSource("GitRepository"), fluxSource("Bucket")}},
	},
	Targets:        []resourceid.GroupKind{fluxSource("GitRepository"), fluxSource("OCIRepository"), fluxSource("Bucket"), fluxSource("ExternalArtifact"), fluxSource("HelmRepository"), fluxSource("HelmChart")},
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			for _, r := range gitops.FluxSourceReferences(u) {
				visit(Declaration{Target: r.Ref, Path: r.Path})
			}
		}
		return nil
	},
}

// NodeClasses is the family of the NodeClass a Karpenter NodePool's nodes
// are launched from, whichever provider defines it: a Karpenter provider's,
// or EKS Auto Mode's NodeClass.
var NodeClasses = &Family{
	ReferrersIndex: referrersIndex("nodeClasses"),
	Name:           "nodeClasses",
	Projection:     project("configures", true, "ConfigRefs", "Consumers"),
	Sources:        []Source{{Kind: resourceid.GroupKind{Group: karpenter.Group, Kind: "NodePool"}}},
	IsTarget: func(gk resourceid.GroupKind) bool {
		return karpenter.IsNodeClassKind(gk.Group, gk.Kind) || gk == resourceid.GroupKind{Group: "eks.amazonaws.com", Kind: "NodeClass"}
	},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, resourceid.GroupKind{Group: karpenter.Group, Kind: "NodePool"}); ok {
			if r, ok := karpenter.NodeClassRefForNodePool(u); ok {
				visit(Declaration{Target: resourceid.NewRef(r.Group, r.Kind, "", r.Name), Path: "spec.template.spec.nodeClassRef"})
			}
		}
		return nil
	},
}

// ClusterClasses is the family of the ClusterClass a Cluster API Cluster's
// topology is built from, which may be in another namespace.
var ClusterClasses = &Family{
	ReferrersIndex: referrersIndex("clusterClasses"),
	Name:           "clusterClasses",
	Projection:     project("configures", true, "ConfigRefs", "Consumers"),
	Sources:        []Source{{Kind: resourceid.GroupKind{Group: configrefs.CAPIGroup, Kind: "Cluster"}}},
	Targets:        []resourceid.GroupKind{{Group: configrefs.CAPIGroup, Kind: "ClusterClass"}},
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, resourceid.GroupKind{Group: configrefs.CAPIGroup, Kind: "Cluster"}); ok {
			if r, ok := configrefs.CAPIClusterClass(u); ok {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}

func knative(group, kind string) resourceid.GroupKind {
	return resourceid.GroupKind{Group: group, Kind: kind}
}

var (
	kindTrigger  = knative(configrefs.KnativeEventingGroup, "Trigger")
	kindBroker   = knative(configrefs.KnativeEventingGroup, "Broker")
	kindChannel  = knative(configrefs.KnativeMessagingGroup, "Channel")
	kindInMemory = knative(configrefs.KnativeMessagingGroup, "InMemoryChannel")
)

// knativeEventSources are the kinds that send events somewhere: Triggers,
// the event sources Knative ships, and Channels, which send to their backing
// channel.
var knativeEventSources = func() []Source {
	out := []Source{{Kind: kindTrigger}}
	for _, kind := range configrefs.KnativeSourceKinds {
		out = append(out, Source{Kind: knative(configrefs.KnativeSourcesGroup, kind)})
	}
	return append(out, Source{Kind: kindChannel}, Source{Kind: kindInMemory})
}()

// KnativeEventDestinations is the family of where a Knative Trigger
// delivers events, an event source sends them, or a Channel's backing
// channel is, in any namespace. Any addressable kind can be a destination;
// the family answers for core Services and every kind in a Knative group,
// such as JobSink, rather than every kind there is, which would make every
// object's answer depend on Knative's stores.
var KnativeEventDestinations = &Family{
	ReferrersIndex: referrersIndex("knativeEventDestinations"),
	Name:           "knativeEventDestinations",
	Projection:     project("exposes", false, "Backends", "RoutedFrom"),
	Sources:        knativeEventSources,
	IsTarget: func(gk resourceid.GroupKind) bool {
		return gk == kindService || gk.Group == "knative.dev" || strings.HasSuffix(gk.Group, ".knative.dev")
	},
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			configrefs.VisitKnativeEventDestinations(u, func(r configrefs.PathRef) { visitPathRef(visit, r) })
		}
		return nil
	},
}

// KnativeTriggerBrokers is the family of the Broker a Knative Trigger
// subscribes to, which its brokerRef may name in another namespace.
var KnativeTriggerBrokers = &Family{
	ReferrersIndex: referrersIndex("knativeTriggerBrokers"),
	Name:           "knativeTriggerBrokers",
	Projection:     project("exposes", true, "RoutedFrom", "Backends"),
	Sources:        []Source{{Kind: kindTrigger}},
	Targets:        []resourceid.GroupKind{kindBroker},
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, kindTrigger); ok {
			if r, ok := configrefs.KnativeTriggerBroker(u); ok {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}

// KnativeRouteRevisions is the family of the Revisions a Knative Route sends
// traffic to, as its status resolves them.
var KnativeRouteRevisions = &Family{
	Name:       "knativeRouteRevisions",
	Projection: project("exposes", false, "Backends", "Routes"),
	Sources:    []Source{{Kind: knative(configrefs.KnativeServingGroup, "Route")}},
	Targets:    []resourceid.GroupKind{knative(configrefs.KnativeServingGroup, "Revision")},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, knative(configrefs.KnativeServingGroup, "Route")); ok {
			for _, r := range configrefs.KnativeRouteRevisions(u) {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}

var (
	kindVirtualService  = resourceid.GroupKind{Group: configrefs.IstioNetworkingGroup, Kind: "VirtualService"}
	kindDestinationRule = resourceid.GroupKind{Group: configrefs.IstioNetworkingGroup, Kind: "DestinationRule"}
)

// IstioGateways is the family of the Istio Gateways a VirtualService binds
// to, which may be in another namespace.
var IstioGateways = &Family{
	ReferrersIndex: referrersIndex("istioGateways"),
	Name:           "istioGateways",
	Projection:     project("exposes", true, "RoutedFrom", "Backends"),
	Sources:        []Source{{Kind: kindVirtualService}},
	Targets:        []resourceid.GroupKind{{Group: configrefs.IstioNetworkingGroup, Kind: "Gateway"}},
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, kindVirtualService); ok {
			configrefs.VisitIstioVirtualServiceGateways(u, func(r configrefs.PathRef) { visitPathRef(visit, r) })
		}
		return nil
	},
}

// IstioServiceHosts is the family of the Services a VirtualService routes to
// and a DestinationRule configures, by host, in any namespace. Hosts resolve
// with the mesh's domain (configrefs.IstioServiceDomain), which comes from
// istiod rather than from the objects, so the family is made for it.
func IstioServiceHosts(domain string) *Family {
	index := referrersIndex("istioServiceHosts")
	index.Candidates = istioHostCandidates
	return &Family{
		Name:           "istioServiceHosts",
		ReferrersIndex: index,
		Projection:     project("exposes", false, "Backends", "Routes").whenSource(kindDestinationRule, "configures", false, "Consumers", "ConfigRefs"),
		Sources:        []Source{{Kind: kindVirtualService}, {Kind: kindDestinationRule}},
		Targets:        []resourceid.GroupKind{kindService},
		CrossNamespace: true,
		Declared: func(obj runtime.Object, visit func(Declaration)) error {
			if u, ok := unstructuredOf(obj, kindVirtualService); ok {
				for _, r := range configrefs.IstioVirtualServiceDestinations(u, domain) {
					visitPathRef(visit, r)
				}
			}
			if u, ok := unstructuredOf(obj, kindDestinationRule); ok {
				if r, ok := configrefs.IstioDestinationRuleService(u, domain); ok {
					visitPathRef(visit, r)
				}
			}
			return nil
		},
	}
}

var (
	kindWorkflow     = resourceid.GroupKind{Group: configrefs.ArgoGroup, Kind: "Workflow"}
	kindCronWorkflow = resourceid.GroupKind{Group: configrefs.ArgoGroup, Kind: "CronWorkflow"}
)

// ArgoWorkflowTemplates is the family of the WorkflowTemplates and
// ClusterWorkflowTemplates an Argo Workflow or CronWorkflow uses, through its
// workflowTemplateRef or a task's or step's templateRef.
var ArgoWorkflowTemplates = &Family{
	ScanAccepted: "Indexing a Workflow's workflowTemplateRef and task templateRefs exceeds the per-update admission bar (BenchmarkRelationIndexUpdate).",
	Name:         "argoWorkflowTemplates",
	Projection:   project("configures", true, "ConfigRefs", "Consumers"),
	Sources:      []Source{{Kind: kindWorkflow}, {Kind: kindCronWorkflow}},
	Targets:      []resourceid.GroupKind{{Group: configrefs.ArgoGroup, Kind: "WorkflowTemplate"}, {Group: configrefs.ArgoGroup, Kind: "ClusterWorkflowTemplate"}},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		fields := []string{"spec"}
		u, ok := unstructuredOf(obj, kindWorkflow)
		if !ok {
			if u, ok = unstructuredOf(obj, kindCronWorkflow); !ok {
				return nil
			}
			fields = []string{"spec", "workflowSpec"}
		}
		for _, r := range configrefs.ArgoWorkflowTemplateRefs(u.Object, u.GetNamespace(), fields...) {
			visitPathRef(visit, r)
		}
		return nil
	},
}
