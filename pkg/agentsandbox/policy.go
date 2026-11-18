// Package agentsandbox holds Radar's readings of Agent Sandbox
// (agents.x-k8s.io, extensions.agents.x-k8s.io) across its objects: when a
// Sandbox or claim is a finding, how a warm pool and the controller read, and
// the workspace summary built from them. Radar's Issues and the Agent Sandbox
// workspace both decide with the rules here; pkg/health reads one object's
// conditions.
package agentsandbox

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/health"
)

const (
	Group           = "agents.x-k8s.io"
	ExtensionsGroup = "extensions.agents.x-k8s.io"

	// ControllerNamespace and ControllerName are where the upstream release
	// manifest installs the controller. Radar looks only there: an
	// installation under another name reads as not found, never as stopped.
	ControllerNamespace = "agent-sandbox-system"
	ControllerName      = "agent-sandbox-controller"
)

const (
	// IncidentAfter is how long a Sandbox or claim may stay in progress before
	// Radar's Issues raise it. Shorter, and every cold image pull or node
	// scale-up would be an incident.
	IncidentAfter = 30 * time.Minute
	// SlowAfter is the workspace's earlier advisory for the same state.
	// Sandboxes normally start within seconds, so one still starting after
	// this is worth a look before it becomes an Issue; it is never raised as
	// one.
	SlowAfter = 5 * time.Minute
)

// Progress is how long an in-progress reading has lasted, against the two
// thresholds above.
type Progress string

const (
	ProgressOnTime  Progress = ""
	ProgressSlow    Progress = "slow"
	ProgressStalled Progress = "stalled"
)

// ProgressOf reads an in-progress state's age. A state with no known start
// (a suspension, a deletion) is never late: Kubernetes keeps a condition's
// transition time when only its reason changes, so there is no clock to read.
func ProgressOf(r health.AgentSandboxReadiness, now time.Time) Progress {
	if !r.InProgress || !r.SinceKnown {
		return ProgressOnTime
	}
	switch age := now.Sub(r.Since); {
	case age > IncidentAfter:
		return ProgressStalled
	case age >= SlowAfter:
		return ProgressSlow
	}
	return ProgressOnTime
}

// SuppressesIssue reports whether Radar's Issues stay silent about a Sandbox's
// or claim's Ready=False. A settled state is never an Issue. A claim's state
// forwarded from its Sandbox is the Sandbox's to report while in progress; a
// forwarded failure stays on the claim too, since the caller may read only
// the claim. Anything else in progress is one once it has stalled.
func SuppressesIssue(r health.AgentSandboxReadiness, now time.Time) bool {
	switch {
	case r.Settled:
		return true
	case r.InProgress && r.Mirrored:
		return true
	case r.InProgress:
		return ProgressOf(r, now) != ProgressStalled
	}
	return false
}

// Failed reports a terminal failure: a Sandbox's own unhealthy reading (an
// image that can't be pulled, a crashing container, PodFailed, an invalid
// spec), or one a claim forwards from its Sandbox. Its Issue is critical and
// so is its workspace row. A claim's own reasons (WarmPoolNotFound,
// TemplateNotFound, …) are not terminal: fixing the reference binds it.
func Failed(u *unstructured.Unstructured, r health.AgentSandboxReadiness) bool {
	if r.Verdict.Level != health.LevelUnhealthy {
		return false
	}
	return u.GetKind() != "SandboxClaim" || r.Mirrored
}

// Unreported: the controller has written no Ready condition on the object.
func Unreported(u *unstructured.Unstructured) bool {
	if u == nil || u.GetDeletionTimestamp() != nil {
		return false
	}
	_, found := condition(u, "Ready")
	return !found
}

// UnreportedStalled: no Ready condition for longer than IncidentAfter since
// the object was created. The generic condition walk has nothing to raise
// here, so Issues raise it on their own.
func UnreportedStalled(u *unstructured.Unstructured, now time.Time) bool {
	return Unreported(u) && ProgressOf(health.ReadAgentSandbox(u), now) == ProgressStalled
}

// IssueRaised reports whether Radar's Issues list the object at now: a refused
// suspension, an unreported object past IncidentAfter, or a Ready=False the
// generic walk is not told to suppress.
func IssueRaised(u *unstructured.Unstructured, r health.AgentSandboxReadiness, now time.Time) bool {
	if r.SuspensionRefused {
		return true
	}
	if Unreported(u) {
		return ProgressOf(r, now) == ProgressStalled
	}
	if SuppressesIssue(r, now) {
		return false
	}
	ready, _ := condition(u, "Ready")
	return ready.status == "False"
}

// Reasons Radar raises itself, prefixed because Issue reasons share one
// namespace across integrations. The controller's own reasons
// (TemplateNotFound, WarmPoolNotFound) stay as it wrote them.
const (
	// ReasonTemplateNotFound: a SandboxTemplate a warm pool names, read in
	// full in its namespace and not there. An Issue on the pool; on a claim
	// row, Radar's finding about the claim's pool.
	ReasonTemplateNotFound = "AgentSandboxTemplateNotFound"
	// ReasonWarmPoolNotFound: a claim's warm pool, read in full and not there.
	ReasonWarmPoolNotFound = "AgentSandboxWarmPoolNotFound"
	// ReasonNotReported: an Issue on a Sandbox or claim the controller has not
	// written a Ready condition on past IncidentAfter.
	ReasonNotReported = "AgentSandboxNotReported"
	// The controller row's reasons.
	ReasonControllerNotReady     = "AgentSandboxControllerNotReady"
	ReasonControllerScaledToZero = "AgentSandboxControllerScaledToZero"
)
