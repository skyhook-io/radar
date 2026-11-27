package relations

import (
	"cmp"
	"slices"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/rollouts"
)

// Kinds is the bounded collection dependency set for relationship answers.
// Both Traefik and Calico groups are needed: the families read both even when
// topology prefers one. Open-ended target predicates do not expand discovery
// into watches of Knative internals or arbitrary NodeClass providers.
// Crossplane composites/claims, like ownership's arbitrary owner kinds, are
// read from already active watches; their unbounded identities are not added.
// Hosts keep typed kinds on typed informers and activate the remaining kinds.
func Kinds() []resourceid.GroupKind {
	set := map[resourceid.GroupKind]struct{}{}
	add := func(gks ...resourceid.GroupKind) {
		for _, gk := range gks {
			set[gk] = struct{}{}
		}
	}
	for _, f := range []*Family{
		ConfigReferences, Autoscalers, KEDAAuthentication, IngressBackends,
		RolloutServices, CertificateSecrets, GatewayParents, GatewayBackends,
		GatewayClasses, CertManagerIssuers, FluxSources, FluxDependsOn, NodeClasses, ClusterClasses,
		KnativeEventDestinations, KnativeTriggerBrokers, KnativeRouteRevisions,
		IstioGateways, IstioServiceHosts(""), ArgoWorkflowTemplates,
		TraefikBackends, TraefikRouteConfig, TraefikSecrets, ContourBackends,
		ContourSecrets, PodPlacement, Reflections, Storage, MachineNodes,
		AnalysisRuns, SealedSecrets,
	} {
		add(f.Targets...)
		for _, s := range f.Sources {
			add(s.Kind)
			add(s.Targets...)
		}
	}
	for _, f := range []*SelectorFamily{ServiceSelectors, DisruptionBudgets, NetworkPolicies, Monitors, CalicoPolicies, CalicoStagedPolicies} {
		for _, s := range f.Selectors {
			add(s.Kind)
			add(s.Selects...)
		}
		for _, s := range f.Selectables {
			add(s.Kind)
		}
	}
	// Auxiliary selector reads and projection/manager producers aren't family edges.
	add(resourceid.GroupKind{Kind: "Namespace"}, resourceid.GroupKind{Kind: "ServiceAccount"})
	for _, gk := range rollouts.WorkloadRefKinds() {
		add(resourceid.GroupKind{Group: gk.Group, Kind: gk.Kind})
	}
	for _, kind := range []string{"Service", "Configuration", "DomainMapping", "Revision", "Route"} {
		add(knative("serving.knative.dev", kind))
	}
	add(knative("eventing.knative.dev", "Broker"))
	for _, kind := range []string{"Channel", "InMemoryChannel"} {
		add(knative("messaging.knative.dev", kind))
	}
	for _, kind := range []string{"Sequence", "Parallel"} {
		add(knative("flows.knative.dev", kind))
	}
	add(knative("sinks.knative.dev", "JobSink"))
	for _, kind := range []string{"MachineHealthCheck", "Cluster", "MachineDeployment", "MachineSet", "Machine", "MachinePool"} {
		add(resourceid.GroupKind{Group: "cluster.x-k8s.io", Kind: kind})
	}
	add(resourceid.GroupKind{Group: "controlplane.cluster.x-k8s.io", Kind: "KubeadmControlPlane"})
	add(resourceid.GroupKind{Group: "argoproj.io", Kind: "Application"})
	add(resourceid.GroupKind{Group: "jobset.x-k8s.io", Kind: "JobSet"},
		resourceid.GroupKind{Group: "cilium.io", Kind: "CiliumClusterwideNetworkPolicy"},
		resourceid.GroupKind{Group: "policy.networking.k8s.io", Kind: "ClusterNetworkPolicy"})
	add(resourceid.GroupKind{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass"},
		resourceid.GroupKind{Group: "karpenter.azure.com", Kind: "AKSNodeClass"},
		resourceid.GroupKind{Group: "karpenter.k8s.gcp", Kind: "GCENodeClass"},
		resourceid.GroupKind{Group: "eks.amazonaws.com", Kind: "NodeClass"})
	out := make([]resourceid.GroupKind, 0, len(set))
	for gk := range set {
		out = append(out, gk)
	}
	slices.SortFunc(out, func(a, b resourceid.GroupKind) int {
		if c := cmp.Compare(a.Group, b.Group); c != 0 {
			return c
		}
		return cmp.Compare(a.Kind, b.Kind)
	})
	return out
}
