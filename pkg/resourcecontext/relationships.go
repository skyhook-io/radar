package resourcecontext

import (
	"maps"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
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
			if !listsField(ident, name) {
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

// listsField reports whether a context of ident's kind can list the field, so
// an answer's gaps mark only fields that kind has.
func listsField(ident resourceIdentity, name string) bool {
	gk := resourceid.GroupKind{Group: ident.Group, Kind: ident.Kind}
	switch {
	case strings.HasPrefix(name, "uses."):
		return listsUses(ident)
	case name == "referencedBy":
		return slices.Contains(relations.ConfigReferences.Targets, gk) || slices.Contains(relations.PodPlacement.Targets, gk)
	case name == "runsOn":
		return gk == resourceid.GroupKind{Kind: "Pod"}
	}
	return true
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
