package issues

import (
	"fmt"
	"time"

	"github.com/skyhook-io/radar/pkg/agentsandbox"
	"github.com/skyhook-io/radar/pkg/health"
	"github.com/skyhook-io/radar/pkg/timeutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	agentSandboxGroup           = "agents.x-k8s.io"
	agentSandboxExtensionsGroup = "extensions.agents.x-k8s.io"
	substrateGroup              = "ate.dev"
)

// agentSandboxSuppressesGenericConditions reports whether the generic
// Ready=False warning should stay silent for an Agent Sandbox Sandbox or
// SandboxClaim. The rule is agentsandbox.SuppressesIssue, which the Agent
// Sandbox workspace applies too.
func agentSandboxSuppressesGenericConditions(group, kind string, u *unstructured.Unstructured) bool {
	if !(group == agentSandboxGroup && kind == "Sandbox") && !(group == agentSandboxExtensionsGroup && kind == "SandboxClaim") {
		return false
	}
	return agentsandbox.SuppressesIssue(health.ReadAgentSandbox(u), time.Now())
}

// agentSandboxFailedSeverity is critical for an Agent Sandbox Sandbox or
// SandboxClaim reporting a terminal failure (agentsandbox.Failed), which the
// workspace shows at the same severity.
func agentSandboxFailedSeverity(group, kind string, u *unstructured.Unstructured) (Severity, bool) {
	if !(group == agentSandboxGroup && kind == "Sandbox") && !(group == agentSandboxExtensionsGroup && kind == "SandboxClaim") {
		return "", false
	}
	if !agentsandbox.Failed(u, health.ReadAgentSandbox(u)) {
		return "", false
	}
	return SeverityCritical, true
}

// detectSandboxSuspensionBlocked reports a Sandbox whose suspension the
// controller refuses to carry out because the Pod it tracks for the Sandbox belongs to
// something else. Ready keeps its ordinary suspension reason, so only the
// Suspended condition says why nothing happens.
func detectSandboxSuspensionBlocked(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured) []Issue {
	if kind != "Sandbox" {
		return nil
	}
	r := health.ReadAgentSandbox(u)
	if !r.SuspensionRefused {
		return nil
	}
	// Suspended stays False from the running state into the refusal, so its
	// transition time predates the refusal: the onset is unknown.
	message := "Suspension is blocked: " + r.Verdict.Message
	return []Issue{newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), SeverityWarning, "PodNotOwned", message, time.Time{}, false, "", u.GetCreationTimestamp().Time)}
}

// detectWorkerPoolIssues reports each sandbox class of an Agent Substrate
// WorkerPool whose spec does not resolve to a usable SandboxConfig. The
// condition is nested per class in status.sandboxClasses[], where the generic
// condition walk never looks. Workers that are not ready are left to the
// worker Deployment the pool owns, which has Issues of its own; a pool whose
// Deployment never appears is reported here, since nothing else would.
func detectWorkerPoolIssues(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured) []Issue {
	desired, _, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
	ready, _, _ := unstructured.NestedInt64(u.Object, "status", "readyReplicas")
	var out []Issue
	for _, c := range health.WorkerPoolClasses(u) {
		if c.Status != "False" {
			continue
		}
		reason := c.Reason
		if reason == "" {
			reason = "Resolved=False"
		}
		message := c.Message
		// With no worker ready, nothing can host the pool's actors. Ready
		// workers keep running on the last resolved config.
		severity := SeverityWarning
		if desired > 0 && ready == 0 {
			severity = SeverityCritical
		}
		// A spec edit is not a recovery: keep a failure reported for an
		// older generation, qualified, until the controller judges the new one.
		if c.Stale {
			severity = SeverityWarning
			message = fmt.Sprintf("Reported for generation %d; the pool is now at generation %d. %s", c.ObservedGeneration, u.GetGeneration(), message)
		}
		if c.Name != "" {
			message = fmt.Sprintf("Sandbox class %s: %s", c.Name, message)
		}
		out = append(out, newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), severity, reason, message, c.TransitionAt, !c.TransitionAt.IsZero(), c.Name, u.GetCreationTimestamp().Time))
	}
	if len(out) == 0 {
		if issue, ok := workerPoolDeploymentMissing(gvr, kind, u, desired, time.Now()); ok {
			out = append(out, issue)
		}
	}
	return out
}

// workerPoolDeploymentMissing reports a pool whose worker Deployment never
// appeared (health.SubstrateWorkerPoolStalled), which its status reads as
// Provisioning stalled.
func workerPoolDeploymentMissing(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured, desired int64, now time.Time) (Issue, bool) {
	since, stalled := health.SubstrateWorkerPoolStalled(u, now)
	if !stalled {
		return Issue{}, false
	}
	message := fmt.Sprintf("%d workers wanted, but no worker Deployment has appeared; every sandbox class resolved %s ago. Substrate's controller only logs a rejected create, so check its logs for the reason, for example an admission policy or a missing permission.", desired, timeutil.FormatAgeShort(now.Sub(since)))
	return newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), SeverityCritical, "DeploymentNotCreated", message, since, true, "", u.GetCreationTimestamp().Time), true
}
