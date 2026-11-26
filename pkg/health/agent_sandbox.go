package health

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// AgentSandboxReadiness is how an Agent Sandbox Sandbox or SandboxClaim
// (agents.x-k8s.io, extensions.agents.x-k8s.io) reads. The controller reports
// every non-running state as Ready=False, so the reason decides whether the
// object failed, is still being acted on, or settled where someone put it.
type AgentSandboxReadiness struct {
	Verdict Verdict
	// Settled: a state someone chose and the controller completed — a
	// suspension, an expiry, a Pod that ran to completion, an expired claim.
	Settled bool
	// InProgress: the controller is still bringing the sandbox up, tearing it
	// down or binding a claim.
	InProgress bool
	// Mirrored: a claim's Ready forwarded from its Sandbox, which reports the
	// same state under its own name.
	Mirrored bool
	// Since is the best evidence of when the current in-progress state began.
	// Kubernetes keeps a condition's lastTransitionTime when only its reason
	// changes, so a Sandbox resumed after a long suspension would otherwise look
	// as if it had been provisioning the whole time.
	Since      time.Time
	SinceKnown bool
	// SuspensionRefused: the controller will not delete the Pod it tracks
	// for the Sandbox because the Sandbox does not own it (Suspended=False,
	// reason PodNotOwned), so the suspension waits until someone resolves it.
	SuspensionRefused bool
}

var sandboxSettledReasons = map[string]string{
	"SandboxExpired": "Expired",
	"PodSucceeded":   "Finished",
	"ClaimExpired":   "Expired",
}

var sandboxProgressReasons = map[string]string{
	"DependenciesNotReady": "Provisioning",
	"PodTerminating":       "Terminating",
	"SandboxCreatePending": "Provisioning",
	"SandboxNotReady":      "Provisioning",
	"AdoptionConflict":     "Binding",
}

// Ready reasons a claim controller sets itself; any other claim reason is
// forwarded from the claim's Sandbox.
var sandboxClaimOwnReasons = map[string]bool{
	"TemplateNotFound": true, "WarmPoolNotFound": true, "AdoptionConflict": true,
	"SandboxCreatePending": true, "InvalidMetadata": true, "EnvVarsInjectionRejected": true,
	"ClaimExpired": true, "VolumeClaimTemplatesError": true, "SandboxMissing": true,
	"SandboxNotReady": true, "ReconcilerError": true,
}

// The controller names a container that is not starting normally in Ready's
// message: `container "x" is waiting: ImagePullBackOff`, or
// `container "x" is in CrashLoopBackOff (last exit: Error)`.
var sandboxWaitingReason = regexp.MustCompile(`is waiting: ([A-Za-z]+)|is in (CrashLoopBackOff)`)

func sandboxContainerFailure(message string) string {
	m := sandboxWaitingReason.FindStringSubmatch(message)
	if m == nil {
		return ""
	}
	reason := m[1] + m[2]
	if !isFatalWaitingReason(reason) {
		return ""
	}
	return reason
}

type sandboxCondition struct {
	status, reason, message string
	observedGeneration      int64
	transition              time.Time
	transitionKnown         bool
	found                   bool
}

func findSandboxCondition(u *unstructured.Unstructured, condType string) sandboxCondition {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, raw := range conds {
		cond, ok := raw.(map[string]any)
		if !ok || cond["type"] != condType {
			continue
		}
		c := sandboxCondition{found: true}
		c.status, _ = cond["status"].(string)
		c.reason, _ = cond["reason"].(string)
		c.message, _ = cond["message"].(string)
		c.observedGeneration, _, _ = unstructured.NestedInt64(cond, "observedGeneration")
		if ts, _ := cond["lastTransitionTime"].(string); ts != "" {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				c.transition, c.transitionKnown = t, true
			}
		}
		return c
	}
	return sandboxCondition{}
}

// later returns whichever condition last changed status. Neither alone marks
// when the current state began: Kubernetes keeps a condition's transition time
// when only its reason changes.
func later(a, b sandboxCondition) sandboxCondition {
	if !b.transitionKnown || (a.transitionKnown && !b.transition.After(a.transition)) {
		return a
	}
	return b
}

// AgentSandbox returns the health of an Agent Sandbox Sandbox or SandboxClaim.
func AgentSandbox(u *unstructured.Unstructured) Verdict {
	return ReadAgentSandbox(u).Verdict
}

// ReadAgentSandbox classifies a Sandbox's or SandboxClaim's Ready condition.
// The order follows the controller's own: reconcile errors before the
// suspension, the suspension before Pod state.
func ReadAgentSandbox(u *unstructured.Unstructured) AgentSandboxReadiness {
	if u == nil {
		return AgentSandboxReadiness{Verdict: Verdict{Level: LevelUnknown, Reason: "Not reported"}}
	}
	if u.GetDeletionTimestamp() != nil {
		return AgentSandboxReadiness{Verdict: Verdict{Level: LevelDegraded, Reason: "Terminating"}, InProgress: true}
	}
	isClaim := u.GetKind() == "SandboxClaim"
	ready := findSandboxCondition(u, "Ready")
	if !ready.found {
		created := u.GetCreationTimestamp().Time
		return AgentSandboxReadiness{Verdict: Verdict{Level: LevelDegraded, Reason: "Pending"}, InProgress: true, Since: created, SinceKnown: !created.IsZero()}
	}
	progress := func(text string, since sandboxCondition) AgentSandboxReadiness {
		r := AgentSandboxReadiness{Verdict: Verdict{Level: LevelDegraded, Reason: text}, InProgress: true, Since: since.transition, SinceKnown: since.transitionKnown}
		r.Mirrored = isClaim && !sandboxClaimOwnReasons[ready.reason]
		return r
	}
	if converging(u.GetGeneration(), ready.observedGeneration) {
		return progress("Updating", ready)
	}
	switch ready.status {
	case "True":
		return AgentSandboxReadiness{Verdict: Verdict{Level: LevelHealthy, Reason: "Ready"}}
	case "False":
	default:
		return AgentSandboxReadiness{Verdict: Verdict{Level: LevelUnknown, Reason: "Unknown"}}
	}

	reason := ready.reason
	if reason == "SandboxSuspended" {
		// Ready says Suspended as soon as it is requested; the Suspended
		// condition says when the Pod is actually gone.
		suspended := findSandboxCondition(u, "Suspended")
		if isClaim || (suspended.status == "True") {
			r := AgentSandboxReadiness{Verdict: Verdict{Level: LevelNeutral, Reason: "Suspended"}, Settled: true}
			r.Mirrored = isClaim
			return r
		}
		// The controller refuses to delete the Pod it tracks for the Sandbox when
		// the Sandbox does not own it, and waits. Nothing is being torn down, so this
		// is not a suspension in progress.
		if suspended.status == "False" && suspended.reason == "PodNotOwned" {
			return AgentSandboxReadiness{Verdict: Verdict{Level: LevelDegraded, Reason: "Suspension blocked", Message: suspensionRefusedMessage(u.GetNamespace(), sandboxPodName(u), suspended.message)}, SuspensionRefused: true}
		}
		// Both conditions can stay False through a suspension, keeping
		// transition times from before it began, so none marks its start. A Pod
		// that never terminates is reported as a stuck deletion instead.
		r := progress("Suspending", ready)
		r.Since, r.SinceKnown = time.Time{}, false
		return r
	}
	if text, ok := sandboxSettledReasons[reason]; ok {
		return AgentSandboxReadiness{Verdict: Verdict{Level: LevelNeutral, Reason: text}, Settled: true, Mirrored: isClaim && !sandboxClaimOwnReasons[reason]}
	}
	if reason == "DependenciesNotReady" {
		if failure := sandboxContainerFailure(ready.message); failure != "" {
			return AgentSandboxReadiness{Verdict: Verdict{Level: LevelUnhealthy, Reason: failure}, Mirrored: isClaim}
		}
		// A resume flips Suspended back to False; Ready stayed False all along
		// and kept its old transition time.
		since := ready
		if suspended := findSandboxCondition(u, "Suspended"); suspended.found && suspended.status == "False" {
			since = later(ready, suspended)
		}
		if scheduled := findSandboxCondition(u, "PodScheduled"); scheduled.found && scheduled.status == "False" && scheduled.reason != "" {
			return progress(scheduled.reason, since)
		}
		return progress("Provisioning", since)
	}
	if text, ok := sandboxProgressReasons[reason]; ok {
		return progress(text, ready)
	}
	if reason == "ReconcilerError" {
		return AgentSandboxReadiness{Verdict: Verdict{Level: LevelDegraded, Reason: reason}, Mirrored: isClaim && !sandboxClaimOwnReasons[reason]}
	}
	// A claim whose warm pool or template is missing binds once the reference
	// is fixed: a warning, as in its Issue and the workspace. Mirrored in k8s-ui.
	if isClaim && (reason == "WarmPoolNotFound" || reason == "TemplateNotFound") {
		return AgentSandboxReadiness{Verdict: Verdict{Level: LevelDegraded, Reason: reason}}
	}
	if reason == "" {
		reason = "Not Ready"
	}
	return AgentSandboxReadiness{Verdict: Verdict{Level: LevelUnhealthy, Reason: reason}, Mirrored: isClaim && !sandboxClaimOwnReasons[ready.reason]}
}

// AgentSandboxPodName is the Pod the controller tracks for a Sandbox.
func AgentSandboxPodName(u *unstructured.Unstructured) string { return sandboxPodName(u) }

// sandboxPodName is the Pod the controller looks for: the name a pod-name
// annotation records (Sandboxes adopted from older warm pools carry one),
// otherwise the Sandbox's own name.
func sandboxPodName(u *unstructured.Unstructured) string {
	if name := u.GetAnnotations()["agents.x-k8s.io/pod-name"]; name != "" {
		return name
	}
	return u.GetName()
}

// PodOwnersCommand prints a Pod's owner references: who else may need the Pod
// a Sandbox doesn't own, before anyone removes it.
func PodOwnersCommand(namespace, name string) string {
	ns := ""
	if namespace != "" {
		ns = " -n " + namespace
	}
	return fmt.Sprintf("kubectl get pod %s%s -o jsonpath='{.metadata.ownerReferences}'", name, ns)
}

// suspensionRefusedMessage explains a refused suspension: the controller's own
// words, then what unblocks it. The Pod may belong to something else, so the
// step is to check its owner before removing it. Mirrored in k8s-ui's
// resource-utils-agent-runtimes.ts.
func suspensionRefusedMessage(namespace, name, controllerMessage string) string {
	next := fmt.Sprintf("The conflicting Pod is %q. Check who owns it (%s) before removing it, or recreate the Sandbox under another name.", name, PodOwnersCommand(namespace, name))
	if controllerMessage == "" {
		return next
	}
	return strings.TrimSuffix(controllerMessage, ".") + ". " + next
}
