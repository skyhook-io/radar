package resourcecontext

import (
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// ControllerOmission describes an unreadable or unsettled controller walk,
// including a subject the caller cannot get. A readable prefix can still exist.
func ControllerOmission(chain *topology.ControllerChain) OmittedReason {
	if chain == nil {
		return OmittedUnavailable
	}
	switch chain.Stop {
	case "withheld":
		return OmittedRBACDenied
	case "unknown":
		if chain.Reason == readreason.KindSyncPending || chain.Reason == readreason.RadarKindNotCollected {
			return OmittedCacheCold
		}
		return OmittedUnavailable
	}
	return ""
}

// ControllerSummary projects an owner-reference or spec-declared controller.
// Its source is native even when the controller belongs to a GitOps API group.
func ControllerSummary(ref topology.ResourceRef) *ManagedByRef {
	out := ManagedByFromOwner(ref.Kind, ref.Group, ref.Namespace, ref.Name)
	if out != nil {
		out.Source = "native"
		out.Group = ref.Group
		if builtin, ok := resourceid.BuiltinForKind(ref.Kind); ok && builtin.Group == ref.Group {
			out.Group = ""
		}
	}
	return out
}

// ManagerSummary emits a navigation lead, not a second management verdict.
// Claims need a readable manager; native Helm names a logical release instead.
func ManagerSummary(answer ownership.ManagerAnswer) *ManagedByRef {
	if !answer.Subject.Present() || answer.Lead == nil || answer.Lead.Namespace == "" {
		return nil
	}
	lead := answer.Lead
	switch answer.State {
	case ownership.ManagerAgree, ownership.ManagerListed, ownership.ManagerShared:
		return ManagedByFromOwner(lead.Kind, lead.Group, lead.Namespace, lead.Name)
	case ownership.ManagerClaimed:
		evidence := answer.Direct
		if answer.Via != nil {
			evidence = *answer.Via
		}
		for _, claim := range evidence.Claims {
			if claim.Copied || claim.Weak || claim.Manager != *lead {
				continue
			}
			switch claim.Check {
			case ownership.ClaimRecorded, ownership.ClaimOmitted, ownership.ClaimUnreadable:
				return ManagedByFromOwner(lead.Kind, lead.Group, lead.Namespace, lead.Name)
			case ownership.ClaimNotRecording:
				if lead.Group == "" && lead.Kind == "HelmRelease" {
					return ManagedByFromOwner(lead.Kind, lead.Group, lead.Namespace, lead.Name)
				}
			}
		}
	}
	return nil
}
