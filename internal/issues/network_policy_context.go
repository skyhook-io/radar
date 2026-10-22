package issues

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// factNetworkPolicyIsolates links a NetworkPolicy issue to the Services,
// workloads and Pods it leaves without inbound traffic, so the issue also
// shows up on those resources (RelatedIssuesFrom matches its refs).
const factNetworkPolicyIsolates = "network_policy_isolates"

// maxNetworkPolicyIsolationRefs bounds the links the fact shows. Matching uses
// the issue's uncapped AffectedRefs, so a capped link still finds the issue.
const maxNetworkPolicyIsolationRefs = 10

func networkPolicyIsolationContext(p k8s.Detection) *issuesapi.DiagnosticContext {
	if p.Reason != k8s.NetworkPolicyDenyAllIngressReason || len(p.AffectedRefs) == 0 {
		return nil
	}
	refs := limitRefs(p.AffectedRefs, maxNetworkPolicyIsolationRefs)
	// No counts: after per-caller redaction a total would describe resources
	// the caller can't read.
	message := "This policy leaves these resources with no allowed inbound traffic."
	if len(refs) < len(p.AffectedRefs) {
		message += " Only the first links are shown; Services and workloads come first."
	}
	return &issuesapi.DiagnosticContext{
		Role: issuesapi.DiagnosticRoleCandidate,
		Facts: []issuesapi.DiagnosticFact{{
			Type:       factNetworkPolicyIsolates,
			Message:    message,
			Confidence: issuesapi.ConfidenceHigh,
			Refs:       refs,
		}},
	}
}

// redactNetworkPolicyIsolationRefs drops the links to resources the caller
// can't read, and the fact when none remain.
func redactNetworkPolicyIsolationRefs(fact issuesapi.DiagnosticFact, canRead func(Ref) bool) (issuesapi.DiagnosticFact, bool, bool) {
	kept := make([]issuesapi.Ref, 0, len(fact.Refs))
	for _, ref := range fact.Refs {
		if canRead(Ref(ref)) {
			kept = append(kept, ref)
		}
	}
	if len(kept) == len(fact.Refs) {
		return fact, true, false
	}
	if len(kept) == 0 {
		return fact, false, true
	}
	fact.Refs = kept
	return fact, true, true
}
