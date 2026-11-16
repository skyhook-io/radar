package resourcecontext

import (
	"maps"
	"slices"

	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// fieldKinds names the one kind a context field lists, where a relationship
// field fills several context fields, so a withheld target marks only its own.
var fieldKinds = map[string]resourceid.GroupKind{
	"uses.configMaps": {Kind: "ConfigMap"}, "uses.secrets": {Kind: "Secret"},
	"uses.pvcs": {Kind: "PersistentVolumeClaim"}, "uses.serviceAccount": {Kind: "ServiceAccount"},
}

func addRelationshipOmissions(extras *topology.RelationshipExtras, omitted *omittedTracker) {
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
		"Dependencies": {"dependencies"}, "Dependents": {"dependents"}, "Consumers": {"referencedBy"},
		"ConfigRefs": {"uses.configMaps", "uses.secrets"}, "StorageRefs": {"uses.pvcs"}, "ServiceAccount": {"uses.serviceAccount"},
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
		withheld := extras.WithheldKinds[field]
		for _, name := range fields[field] {
			if coverage.Narrowed && (len(withheld) == 0 || slices.ContainsFunc(withheld, func(k resourceid.GroupKind) bool {
				return fieldKinds[name] == (resourceid.GroupKind{}) || fieldKinds[name] == k
			})) {
				omitted.add(name, OmittedRBACDenied)
			}
			if reason != "" {
				omitted.add(name, reason)
			}
		}
	}
}
