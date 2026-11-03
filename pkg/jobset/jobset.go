// Package jobset holds the JobSet facts more than one Radar surface must
// answer identically: which API it reads, which Jobs a JobSet controls, and
// the root lifecycle. The topology graph, the member API and the agent
// execution context all decide through it, so they cannot disagree.
package jobset

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	Group      = "jobset.x-k8s.io"
	Kind       = "JobSet"
	APIVersion = Group + "/v1alpha2"
)

// GVK is the only JobSet API version Radar interprets. The mapping follows
// JobSet v0.12–v0.13; the served version does not identify the controller
// release.
var GVK = schema.GroupVersionKind{Group: Group, Version: "v1alpha2", Kind: Kind}

// Supported reports whether obj is a JobSet of the interpreted API version.
func Supported(obj *unstructured.Unstructured) bool {
	return obj != nil && obj.GetAPIVersion() == APIVersion && obj.GetKind() == Kind
}

// Controls reports whether jobSet is child's controller: same namespace, and
// the child's controller owner reference names this exact JobSet incarnation
// by API version, kind, name and UID. Labels, non-controller owner references
// and references to an earlier JobSet with the same name do not count.
func Controls(jobSet *unstructured.Unstructured, child metav1.Object) bool {
	if !Supported(jobSet) || child == nil || child.GetNamespace() != jobSet.GetNamespace() {
		return false
	}
	owner := metav1.GetControllerOf(child)
	if owner == nil || owner.APIVersion != APIVersion || owner.Kind != Kind || owner.Name != jobSet.GetName() {
		return false
	}
	return jobSet.GetUID() != "" && owner.UID == jobSet.GetUID()
}

// Phase is a JobSet's normalized root lifecycle.
type Phase string

const (
	// PhasePending: the controller reported role status with no Job
	// activity yet. An unreported status is PhaseUnknown, not pending.
	PhasePending Phase = "pending"
	// PhaseActive: not finished, with reported child Jobs in any state
	// (including failed or succeeded ones) or a startup or recreation in
	// progress. It doesn't establish that Jobs or Pods are running.
	PhaseActive    Phase = "active"
	PhaseSuspended Phase = "suspended"
	PhaseFinished  Phase = "finished"
	PhaseUnknown   Phase = "unknown"
)

// Outcome is set only for a finished JobSet.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
)

// State is the lifecycle answer and the True condition that decided it, if
// one did, with that condition's reason. A decision from status.terminalState
// alone or from child counts has no Condition.
type State struct {
	Phase     Phase
	Outcome   Outcome
	Condition string
	Reason    string
}

// Lifecycle reads a JobSet's root lifecycle. status.terminalState is
// authoritative even when conditions disagree, and an unrecognized terminal
// state is unknown rather than falling through. Child activity cannot prove a
// root outcome, and an unreported child status cannot prove inactivity.
func Lifecycle(u *unstructured.Unstructured) State {
	if !Supported(u) {
		return State{Phase: PhaseUnknown}
	}
	conditions := trueConditions(u)
	terminalState, _, _ := unstructured.NestedString(u.Object, "status", "terminalState")
	switch terminalState {
	case "Failed":
		return conditions.decide(State{Phase: PhaseFinished, Outcome: OutcomeFailed}, "Failed")
	case "Completed":
		return conditions.decide(State{Phase: PhaseFinished, Outcome: OutcomeSucceeded}, "Completed")
	case "":
	default:
		return State{Phase: PhaseUnknown}
	}
	if conditions.true("Failed") {
		return conditions.decide(State{Phase: PhaseFinished, Outcome: OutcomeFailed}, "Failed")
	}
	if conditions.true("Completed") {
		return conditions.decide(State{Phase: PhaseFinished, Outcome: OutcomeSucceeded}, "Completed")
	}
	suspendRequested, _, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
	// In-order resume retains Suspended until every role has started.
	if !suspendRequested && conditions.true("StartupPolicyInProgress") {
		return conditions.decide(State{Phase: PhaseActive}, "StartupPolicyInProgress")
	}
	if conditions.true("Suspended") {
		return conditions.decide(State{Phase: PhaseSuspended}, "Suspended")
	}
	for _, conditionType := range []string{"RestartingJobSet", "StartupPolicyInProgress"} {
		if conditions.true(conditionType) {
			return conditions.decide(State{Phase: PhaseActive}, conditionType)
		}
	}
	children := ObservedChildren(u)
	switch {
	case children == nil:
		return State{Phase: PhaseUnknown}
	case children.any():
		return State{Phase: PhaseActive}
	default:
		return State{Phase: PhasePending}
	}
}

// conditionSet maps each condition type to its reason when its status is
// True, and to nil otherwise.
type conditionSet map[string]*string

func (c conditionSet) true(conditionType string) bool {
	return c[conditionType] != nil
}

// decide names conditionType as the deciding condition when it is True.
func (c conditionSet) decide(state State, conditionType string) State {
	if reason := c[conditionType]; reason != nil {
		state.Condition, state.Reason = conditionType, *reason
	}
	return state
}

// trueConditions keeps the last entry per type, as the status list is a map
// keyed by type in the API.
func trueConditions(u *unstructured.Unstructured) conditionSet {
	raw, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	set := conditionSet{}
	for _, item := range raw {
		condition, ok := item.(map[string]any)
		if !ok {
			continue
		}
		conditionType, _ := condition["type"].(string)
		if conditionType == "" {
			continue
		}
		set[conditionType] = nil
		if status, _ := condition["status"].(string); status == "True" {
			reason, _ := condition["reason"].(string)
			set[conditionType] = &reason
		}
	}
	return set
}

// Declared returns the roles spec.replicatedJobs declares and the Jobs they
// ask for, counting an omitted replicas as the controller's default of one.
// Anything but a supported JobSet declares none.
func Declared(u *unstructured.Unstructured) (roles, jobs int64) {
	if !Supported(u) {
		return 0, 0
	}
	replicatedJobs, _, _ := unstructured.NestedSlice(u.Object, "spec", "replicatedJobs")
	for _, item := range replicatedJobs {
		roles++
		role, ok := item.(map[string]any)
		if !ok {
			continue
		}
		replicas, found, _ := unstructured.NestedInt64(role, "replicas")
		if !found {
			replicas = 1
		}
		jobs += replicas
	}
	return roles, jobs
}

// ChildCounts sums status.replicatedJobsStatus over the roles it names.
type ChildCounts struct {
	Roles     int64
	Ready     int64
	Active    int64
	Succeeded int64
	Failed    int64
	Suspended int64
}

func (c ChildCounts) any() bool {
	return c.Ready > 0 || c.Active > 0 || c.Succeeded > 0 || c.Failed > 0 || c.Suspended > 0
}

// ObservedChildren returns the controller's reported child Job counts, or nil
// when status.replicatedJobsStatus is absent (unreported is not zero) or u
// isn't a supported JobSet.
func ObservedChildren(u *unstructured.Unstructured) *ChildCounts {
	if !Supported(u) {
		return nil
	}
	statuses, observed, _ := unstructured.NestedSlice(u.Object, "status", "replicatedJobsStatus")
	if !observed {
		return nil
	}
	counts := &ChildCounts{}
	for _, item := range statuses {
		status, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, _, _ := unstructured.NestedString(status, "name"); name == "" {
			continue
		}
		counts.Roles++
		counts.Ready += int64Field(status, "ready")
		counts.Active += int64Field(status, "active")
		counts.Succeeded += int64Field(status, "succeeded")
		counts.Failed += int64Field(status, "failed")
		counts.Suspended += int64Field(status, "suspended")
	}
	return counts
}

func int64Field(object map[string]any, field string) int64 {
	value, _, _ := unstructured.NestedInt64(object, field)
	return value
}
