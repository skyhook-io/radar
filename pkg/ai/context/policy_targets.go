package context

import "github.com/skyhook-io/radar/pkg/resourcecontext"

// PolicyTargetBudgetWarning explains a structured omission at agent-facing
// presentation boundaries without putting prose into ResourceContext.
func PolicyTargetBudgetWarning(rc *resourcecontext.ResourceContext) string {
	if rc == nil {
		return ""
	}
	for _, omission := range rc.Omitted {
		if omission.Field == "protects" && omission.Reason == resourcecontext.OmittedBudgetExceeded {
			return "Policy-target context is incomplete: Radar reached a reference or permission-check budget. " +
				"For the full visible target list, open this policy's Related Resources in Radar. " +
				"With MCP, use list_resources for Deployments, StatefulSets and DaemonSets in namespaces of interest, " +
				"then get_resource for each workload and check resourceContext.selectedBy " +
				"(resourceContext.stagedPolicies for staged policies). " +
				"Preserve networkPolicyMatchesIncomplete and RBAC omissions. " +
				"Repeating this policy lookup or using get_neighborhood does not produce an exhaustive target list."
		}
	}
	return ""
}
