package relations

// Families are every declared-reference family, with Istio's hosts resolved
// in istioDomain (configrefs.IstioServiceDomain).
func Families(istioDomain string) []*Family {
	return FamiliesIn(func() string { return istioDomain })
}

// FamiliesIn is Families asking for Istio's domain as IstioServiceHostsIn
// does.
func FamiliesIn(istioDomain func() string) []*Family {
	return []*Family{
		ConfigReferences, Storage, Reflections, SealedSecrets, FluxHelmOrigins, ArgoWorkflowCronWorkflows, ArgoPodWorkflows,
		Autoscalers, KEDAAuthentication,
		IngressBackends, RolloutServices, GatewayParents, GatewayBackends, GatewayClasses,
		IstioGateways, IstioServiceHostsIn(istioDomain),
		TraefikBackends, TraefikRouteConfig, TraefikSecrets, ContourBackends, ContourSecrets,
		KnativeEventDestinations, KnativeTriggerBrokers, KnativeRouteRevisions,
		CertificateSecrets, CertManagerIssuers, FluxSources, FluxDependsOn,
		NodeClasses, ClusterClasses, MachineNodes, PodPlacement, AnalysisRuns, ArgoWorkflowTemplates,
	}
}

// SelectorFamilies are every label-selector family.
func SelectorFamilies() []*SelectorFamily {
	return []*SelectorFamily{ServiceSelectors, DisruptionBudgets, NetworkPolicies, CalicoPolicies, CalicoStagedPolicies, Monitors}
}
