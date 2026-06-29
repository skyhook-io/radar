package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/pkg/helmhistory"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

const helmChangeSource = "helm"

func helmRecentChangesForContext(ctx context.Context, input getChangesInput, since time.Duration) ([]issuesapi.RecentChange, error) {
	if input.Kind != "" && !issues.KindFilterIncludes([]string{input.Kind}, "HelmRelease", "helmreleases") {
		return nil, nil
	}
	helmClient := helm.GetClient()
	if helmClient == nil {
		return nil, nil
	}
	username, groups := userFromContext(ctx)
	releases, err := helmClient.ListReleasesAcrossNamespaces(resolveHelmListNamespaces(ctx, input.Namespace), username, groups)
	if err != nil {
		return nil, fmt.Errorf("failed to list Helm releases: %w", err)
	}
	return helmRecentChanges(releases, input.Name, since, time.Now()), nil
}

func helmRecentChanges(releases []helm.HelmRelease, name string, since time.Duration, now time.Time) []issuesapi.RecentChange {
	cutoff := now.Add(-since)
	var out []issuesapi.RecentChange
	for _, rel := range releases {
		if rel.ManagedByFluxHelmRelease != "" {
			continue
		}
		if name != "" && rel.Name != name {
			continue
		}
		operations := mergeHelmOperations(rel.Operations, rel.LastOperation)
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
		Source:         helmChangeSource,
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
		Source:         helmChangeSource,
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
