package resourcecontext

import (
	"maps"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

func addRelationshipOmissions(ident resourceIdentity, extras *topology.RelationshipExtras, omitted *omittedTracker) {
	if extras == nil {
		return
	}
	fields := map[string][]string{
		"Services": {"exposes", "exposedBy"}, "Ingresses": {"exposes", "exposedBy"},
		"Gateways": {"exposes", "exposedBy"}, "Routes": {"exposes", "exposedBy"}, "RoutedFrom": {"exposes", "exposedBy"},
		"Entrypoints": {"exposes", "exposedBy"}, "Backends": {"exposes", "backends"}, "Pods": {"exposes", "backends"},
		"PDBs": {"selectedBy"}, "NetworkPolicies": {"selectedBy"}, "Scalers": {"scaledBy"},
		"ScalerManagers": {"scaledBy.managedBy"}, "Protects": {"protects"}, "HealthChecks": {"healthChecks"},
		"StagedPolicies": {"stagedPolicies"}, "Monitors": {"monitors"}, "MonitorTargets": {"monitorTargets"},
		"Dependencies": {"dependencies"}, "Dependents": {"dependents"}, "ReferencedBy": {"referencedBy"},
		"Uses": {"uses.configMaps", "uses.secrets", "uses.pvcs", "uses.serviceAccount"},
		"Node": {"runsOn"}, "Managers": {"managedBy"},
		"subject": {"exposes", "exposedBy", "backends", "selectedBy", "scaledBy", "protects", "healthChecks", "stagedPolicies", "monitors", "monitorTargets", "dependencies", "dependents", "referencedBy", "uses.configMaps", "uses.secrets", "uses.pvcs", "uses.serviceAccount", "runsOn"},
		// No answer at all: every field one fills, but runsOn, which a Pod's
		// own spec names without one.
		"answer": {"exposes", "exposedBy", "backends", "selectedBy", "scaledBy", "protects", "healthChecks", "stagedPolicies", "monitors", "monitorTargets", "dependencies", "dependents", "referencedBy", "uses"},
	}
	has := topology.AnswerFields(resourceid.GroupKind{Group: ident.Group, Kind: ident.Kind})
	// A field the subject's kind can have is one some field an answer fills
	// for that kind feeds; uses is listed only for pod specs.
	lists := func(name string) bool {
		if name == "uses" || strings.HasPrefix(name, "uses.") {
			return listsUses(ident)
		}
		for field, names := range fields {
			if has[field] && slices.Contains(names, name) {
				return true
			}
		}
		return false
	}
	for _, field := range slices.Sorted(maps.Keys(extras.Fields)) {
		coverage := extras.Fields[field]
		reason := OmittedReason("")
		if coverage.SubjectState != "" || coverage.Truncated {
			reason = OmittedUnavailable
		}
		for _, gap := range coverage.NotChecked {
			if gap.Reason == readreason.KindSyncPending && reason == "" {
				reason = OmittedCacheCold
			} else if gap.Reason != readreason.KindSyncPending {
				reason = OmittedUnavailable
			}
		}
		for _, name := range fields[field] {
			if !lists(name) {
				continue
			}
			if coverage.Narrowed {
				omitted.add(name, OmittedRBACDenied)
			}
			if reason != "" {
				omitted.add(name, reason)
			}
		}
	}
}

// listsUses reports the kinds whose context lists what their pod spec uses.
func listsUses(ident resourceIdentity) bool {
	switch ident.Group {
	case "":
		return ident.Kind == "Pod"
	case "apps":
		return slices.Contains([]string{"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet"}, ident.Kind)
	case "batch":
		return ident.Kind == "Job" || ident.Kind == "CronJob"
	case "argoproj.io":
		return ident.Kind == "Rollout"
	}
	return false
}
