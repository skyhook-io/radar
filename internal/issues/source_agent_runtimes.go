package issues

import (
	"fmt"
	"time"

	"github.com/skyhook-io/radar/pkg/conditions"
	"github.com/skyhook-io/radar/pkg/health"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	agentSandboxGroup           = "agents.x-k8s.io"
	agentSandboxExtensionsGroup = "extensions.agents.x-k8s.io"
	substrateGroup              = "ate.dev"
)

const agentSandboxProgressGrace = 30 * time.Minute

// workerPoolProvisionGrace covers the controller's next pass: it creates the
// worker Deployment after writing the resolved classes, and records the
// Deployment's selector in status only on the pass after that.
const workerPoolProvisionGrace = 10 * time.Minute

// agentSandboxSuppressesGenericConditions reports whether the generic
// Ready=False warning should stay silent for an Agent Sandbox Sandbox or
// SandboxClaim (pkg/health reads the reasons). A settled state is never an
// Issue. A state still in progress is one only once it has lasted the grace
// period: a Sandbox whose Pod or Service is never created has no other finding
// to fall back on. A claim's state forwarded from its Sandbox is the Sandbox's
// to report. Anything else keeps the generic warning.
func agentSandboxSuppressesGenericConditions(group, kind string, u *unstructured.Unstructured) bool {
	if !(group == agentSandboxGroup && kind == "Sandbox") && !(group == agentSandboxExtensionsGroup && kind == "SandboxClaim") {
		return false
	}
	r := health.ReadAgentSandbox(u)
	switch {
	case r.Settled:
		return true
	case r.InProgress && r.Mirrored:
		return true
	case r.InProgress:
		return !r.SinceKnown || time.Since(r.Since) <= agentSandboxProgressGrace
	}
	return false
}

// detectSandboxSuspensionBlocked reports a Sandbox whose suspension the
// controller refuses to carry out because the Pod with its name belongs to
// something else. Ready keeps its ordinary suspension reason, so only the
// Suspended condition says why nothing happens.
func detectSandboxSuspensionBlocked(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured) []Issue {
	if kind != "Sandbox" {
		return nil
	}
	r := health.ReadAgentSandbox(u)
	if r.Verdict.Reason != "PodNotOwned" {
		return nil
	}
	suspended, _ := conditions.FindFalseConditionWithTime(u, "Suspended")
	message := "Suspension is blocked: " + r.Verdict.Message
	return []Issue{newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), SeverityWarning, r.Verdict.Reason, message, suspended.LastTransitionTime, suspended.HasLastTransitionTime, "", u.GetCreationTimestamp().Time)}
}

// detectWorkerPoolIssues reports each sandbox class of an Agent Substrate
// WorkerPool whose spec does not resolve to a usable SandboxConfig. The
// condition is nested per class in status.sandboxClasses[], where the generic
// condition walk never looks. Workers that are not ready are left to the
// worker Deployment the pool owns, which has Issues of its own; a pool whose
// Deployment never appears is reported here, since nothing else would.
func detectWorkerPoolIssues(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured) []Issue {
	classes, _, _ := unstructured.NestedSlice(u.Object, "status", "sandboxClasses")
	desired, _, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
	ready, _, _ := unstructured.NestedInt64(u.Object, "status", "readyReplicas")
	generation := u.GetGeneration()
	var out []Issue
	for _, raw := range classes {
		class, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		className, _ := class["name"].(string)
		conds, _ := class["conditions"].([]any)
		for _, rawCond := range conds {
			cond, ok := rawCond.(map[string]any)
			if !ok || cond["type"] != "Resolved" || cond["status"] != "False" {
				continue
			}
			reason, _ := cond["reason"].(string)
			if reason == "" {
				reason = "Resolved=False"
			}
			message, _ := cond["message"].(string)
			// With no worker ready, nothing can host the pool's actors. Ready
			// workers keep running on the last resolved config.
			severity := SeverityWarning
			if desired > 0 && ready == 0 {
				severity = SeverityCritical
			}
			// A spec edit is not a recovery: keep a failure reported for an
			// older generation, qualified, until the controller judges the new one.
			if observed, ok, _ := unstructured.NestedInt64(cond, "observedGeneration"); ok && generation > 0 && observed > 0 && observed < generation {
				severity = SeverityWarning
				message = fmt.Sprintf("Reported for generation %d; the pool is now at generation %d. %s", observed, generation, message)
			}
			if className != "" {
				message = fmt.Sprintf("Sandbox class %s: %s", className, message)
			}
			transitionAt, transitionKnown := conditionTransitionTime(cond)
			out = append(out, newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), severity, reason, message, transitionAt, transitionKnown, className, u.GetCreationTimestamp().Time))
		}
	}
	if len(out) == 0 {
		if issue, ok := workerPoolDeploymentMissing(gvr, kind, u, classes, desired, time.Now()); ok {
			out = append(out, issue)
		}
	}
	return out
}

// workerPoolDeploymentMissing reports a pool that wants workers, has every
// class resolved, and still has no worker Deployment past the grace period.
// The controller only logs a Deployment or ServiceAccount create the API
// server rejects, so an empty status.selector with no worker Pods is the only
// trace it leaves.
func workerPoolDeploymentMissing(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured, classes []any, desired int64, now time.Time) (Issue, bool) {
	selector, _, _ := unstructured.NestedString(u.Object, "status", "selector")
	pods, _, _ := unstructured.NestedInt64(u.Object, "status", "replicas")
	if desired <= 0 || selector != "" || pods > 0 || len(classes) == 0 || u.GetDeletionTimestamp() != nil {
		return Issue{}, false
	}
	since := u.GetCreationTimestamp().Time
	generation := u.GetGeneration()
	for _, raw := range classes {
		class, _ := raw.(map[string]any)
		conds, _ := class["conditions"].([]any)
		resolved := false
		for _, rawCond := range conds {
			cond, _ := rawCond.(map[string]any)
			if cond["type"] != "Resolved" || cond["status"] != "True" {
				continue
			}
			if observed, ok, _ := unstructured.NestedInt64(cond, "observedGeneration"); ok && generation > 0 && observed > 0 && observed < generation {
				continue
			}
			resolved = true
			if at, ok := conditionTransitionTime(cond); ok && at.After(since) {
				since = at
			}
		}
		if !resolved {
			return Issue{}, false
		}
	}
	if since.IsZero() || now.Sub(since) < workerPoolProvisionGrace {
		return Issue{}, false
	}
	message := fmt.Sprintf("%d workers wanted, but no worker Deployment has appeared %s after every sandbox class resolved. Substrate's controller only logs a rejected create, so check its logs for the reason, for example an admission policy or a missing permission.", desired, now.Sub(since).Round(time.Minute))
	return newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), SeverityCritical, "DeploymentNotCreated", message, since, true, "", u.GetCreationTimestamp().Time), true
}
