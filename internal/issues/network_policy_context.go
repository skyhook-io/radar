package issues

import (
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// factNetworkPolicyIsolates links a NetworkPolicy issue to the Services,
// workloads and Pods it leaves without inbound traffic, so the issue also
// shows up on those resources (RelatedIssuesFrom matches its refs).
const factNetworkPolicyIsolates = "network_policy_isolates"

// maxAffectedRefLinks bounds the links an affected-resources fact shows.
// Matching uses the issue's uncapped AffectedRefs, so a capped link still
// finds the issue.
const maxAffectedRefLinks = 10

// truncatedLinksNote ends an affected-resources fact whose links are capped.
// Per-caller redaction recomputes it over the links the caller can read.
const truncatedLinksNote = " Only the first links are shown."

func networkPolicyIsolationContext(p k8s.Detection) *issuesapi.DiagnosticContext {
	if p.Reason != k8s.NetworkPolicyDenyAllIngressReason || len(p.AffectedRefs) == 0 {
		return nil
	}
	// No counts: after per-caller redaction a total would describe resources
	// the caller can't read.
	return &issuesapi.DiagnosticContext{
		Role:  issuesapi.DiagnosticRoleCandidate,
		Facts: []issuesapi.DiagnosticFact{affectedRefsFact(factNetworkPolicyIsolates, "This policy leaves these resources with no allowed inbound traffic; Services and workloads come first.", p.AffectedRefs)},
	}
}

func affectedRefsFact(factType, message string, refs []Ref) issuesapi.DiagnosticFact {
	fact := issuesapi.DiagnosticFact{Type: factType, Message: message, Confidence: issuesapi.ConfidenceHigh, Refs: limitRefs(refs, maxAffectedRefLinks)}
	if len(refs) > maxAffectedRefLinks {
		fact.Message += truncatedLinksNote
	}
	return fact
}

// redactAffectedRefsFact rebuilds the fact's links from the affected
// resources the caller can read, capping only after the unreadable ones are
// gone, so neither the links nor the truncation note reflect resources the
// caller can't see. With none left, the fact is dropped.
func redactAffectedRefsFact(fact issuesapi.DiagnosticFact, readable []Ref) (issuesapi.DiagnosticFact, bool) {
	if len(readable) == 0 {
		return fact, false
	}
	return affectedRefsFact(fact.Type, strings.TrimSuffix(fact.Message, truncatedLinksNote), readable), true
}
