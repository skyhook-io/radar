package meaningfulchanges

import (
	"fmt"
	"time"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/pkg/helmhistory"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// HelmChangeSource marks change rows synthesized from native Helm release
// history rather than the timeline. They have no Kubernetes GVR, so per-kind
// RBAC gates cannot evaluate them; callers authorize them upstream by listing
// the releases as the caller.
const HelmChangeSource = "helm"

func HelmRecentChanges(releases []helm.HelmRelease, name string, since time.Duration, now time.Time) []issuesapi.RecentChange {
	cutoff := now.Add(-since)
	var out []issuesapi.RecentChange
	for _, rel := range releases {
		if rel.ManagedByFluxHelmRelease != "" {
			continue
		}
		if name != "" && rel.Name != name {
			continue
		}
		operations := MergeHelmOperations(rel.Operations, rel.LastOperation)
		currentRevisionCovered := false
		for _, op := range operations {
			ts := helmOperationTime(op, rel)
			if ts.IsZero() || ts.Before(cutoff) {
				continue
			}
			if op.Revision == rel.Revision || op.RollbackRevision == rel.Revision {
				currentRevisionCovered = true
			}
			out = append(out, helmOperationChange(rel, op, ts))
		}
		if !currentRevisionCovered && !rel.Updated.IsZero() && !rel.Updated.Before(cutoff) {
			out = append(out, helmRevisionChange(rel))
		}
	}
	return out
}

func MergeHelmOperations(operations []helm.HelmOperation, lastOperation *helm.HelmOperation) []helm.HelmOperation {
	merged := make([]helm.HelmOperation, 0, len(operations)+1)
	seen := make(map[string]struct{}, len(operations)+1)
	if lastOperation != nil {
		key := helmOperationKey(*lastOperation)
		seen[key] = struct{}{}
		merged = append(merged, *lastOperation)
	}
	for _, op := range operations {
		key := helmOperationKey(op)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, op)
	}
	return merged
}

func helmOperationKey(operation helm.HelmOperation) string {
	return fmt.Sprintf(
		"%s:%s:%d:%d:%d:%d",
		operation.Kind,
		operation.Status,
		operation.Revision,
		operation.FailedRevision,
		operation.RollbackRevision,
		operation.TargetRevision,
	)
}

func helmOperationTime(op helm.HelmOperation, rel helm.HelmRelease) time.Time {
	if !op.Updated.IsZero() {
		return op.Updated
	}
	return rel.Updated
}

func helmOperationChange(rel helm.HelmRelease, op helm.HelmOperation, ts time.Time) issuesapi.RecentChange {
	fields := []issuesapi.ChangeField{
		{Path: "helm.operation", NewValue: string(op.Kind)},
		{Path: "helm.status", NewValue: string(op.Status)},
	}
	if op.Revision > 0 {
		fields = append(fields, issuesapi.ChangeField{Path: "helm.revision", NewValue: op.Revision})
	}
	if op.FailedRevision > 0 {
		fields = append(fields, issuesapi.ChangeField{Path: "helm.failedRevision", NewValue: op.FailedRevision})
	}
	if op.RollbackRevision > 0 {
		fields = append(fields, issuesapi.ChangeField{Path: "helm.rollbackRevision", NewValue: op.RollbackRevision})
	}
	if op.TargetRevision > 0 {
		fields = append(fields, issuesapi.ChangeField{Path: "helm.targetRevision", NewValue: op.TargetRevision})
	}
	return issuesapi.RecentChange{
		Source:         HelmChangeSource,
		Kind:           "HelmRelease",
		Namespace:      helmChangeNamespace(rel),
		Name:           rel.Name,
		ChangeType:     string(op.Kind),
		Summary:        helmOperationSummary(rel, op),
		Timestamp:      ts.Format(time.RFC3339),
		ChangeCategory: issuesapi.ChangeCategorySpecConfig,
		RankReason:     "Helm release operation history",
		Fields:         fields,
	}
}

func helmRevisionChange(rel helm.HelmRelease) issuesapi.RecentChange {
	return issuesapi.RecentChange{
		Source:         HelmChangeSource,
		Kind:           "HelmRelease",
		Namespace:      helmChangeNamespace(rel),
		Name:           rel.Name,
		ChangeType:     "helm_release_revision",
		Summary:        fmt.Sprintf("Helm release %q is at rev %d (%s).", rel.Name, rel.Revision, helmChartDisplay(rel)),
		Timestamp:      rel.Updated.Format(time.RFC3339),
		ChangeCategory: issuesapi.ChangeCategoryLifecycle,
		RankReason:     "Helm release revision changed",
		Fields: []issuesapi.ChangeField{
			{Path: "helm.revision", NewValue: rel.Revision},
			{Path: "helm.status", NewValue: rel.Status},
			{Path: "helm.chart", NewValue: rel.Chart},
			{Path: "helm.chartVersion", NewValue: rel.ChartVersion},
		},
	}
}

func helmOperationSummary(rel helm.HelmRelease, op helm.HelmOperation) string {
	if op.Message != "" {
		return op.Message
	}
	switch op.Kind {
	case helmhistory.KindUpgradeRolledBack:
		return fmt.Sprintf("Helm upgrade for %q failed at rev %d and rolled back to rev %d.", rel.Name, op.FailedRevision, op.TargetRevision)
	case helmhistory.KindRollback:
		return fmt.Sprintf("Helm release %q rolled back to rev %d.", rel.Name, op.TargetRevision)
	case helmhistory.KindPending:
		return fmt.Sprintf("Helm release %q is stuck in %s at rev %d.", rel.Name, op.PendingStatus, op.Revision)
	case helmhistory.KindUpgradeFailed:
		return fmt.Sprintf("Helm upgrade for %q failed at rev %d.", rel.Name, op.Revision)
	case helmhistory.KindReleaseFailed:
		return fmt.Sprintf("Helm release %q failed at rev %d.", rel.Name, op.Revision)
	default:
		return fmt.Sprintf("Helm release %q recorded %s at rev %d.", rel.Name, op.Kind, op.Revision)
	}
}

func helmChangeNamespace(rel helm.HelmRelease) string {
	if rel.StorageNamespace != "" {
		return rel.StorageNamespace
	}
	return rel.Namespace
}

func helmChartDisplay(rel helm.HelmRelease) string {
	if rel.ChartVersion == "" {
		return rel.Chart
	}
	if rel.Chart == "" {
		return rel.ChartVersion
	}
	return rel.Chart + "-" + rel.ChartVersion
}
