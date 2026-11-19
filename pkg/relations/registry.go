package relations

// Families are every declared-reference family, with Istio's hosts resolved
// in istioDomain (configrefs.IstioServiceDomain).
func Families(istioDomain string) []*Family {
	return []*Family{
		ConfigReferences, Storage, Reflections, SealedSecrets, FluxHelmOrigins, ArgoWorkflowCronWorkflows, ArgoPodWorkflows,
		Autoscalers, KEDAAuthentication,
		IngressBackends, RolloutServices, GatewayParents, GatewayBackends, GatewayClasses,
		IstioGateways, IstioServiceHosts(istioDomain),
		TraefikBackends, TraefikRouteConfig, TraefikSecrets, ContourBackends, ContourSecrets,
		KnativeEventDestinations, KnativeTriggerBrokers, KnativeRouteRevisions,
		CertificateSecrets, CertManagerIssuers, FluxSources,
		NodeClasses, ClusterClasses, MachineNodes, PodPlacement, AnalysisRuns, ArgoWorkflowTemplates,
	}
}

// SelectorFamilies are every label-selector family.
func SelectorFamilies() []*SelectorFamily {
	return []*SelectorFamily{ServiceSelectors, DisruptionBudgets, NetworkPolicies, CalicoPolicies, CalicoStagedPolicies, Monitors}
}
