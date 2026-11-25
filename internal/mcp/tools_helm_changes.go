package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/issuecorrelation"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func helmRecentChangesForContext(ctx context.Context, input getChangesInput, since time.Duration) ([]issuesapi.RecentChange, error) {
	if input.Kind != "" && !issues.KindFilterIncludes([]string{input.Kind}, "HelmRelease", "helmreleases") {
		return nil, nil
	}
	ctx = k8s.CaptureConnection(ctx)
	helmClient := helm.GetClient(ctx)
	if helmClient == nil {
		return nil, nil
	}
	user := userFromContext(ctx)
	releases, err := helmClient.ListReleasesAcrossNamespaces(ctx, resolveHelmListNamespaces(ctx, input.Namespace), user, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list Helm releases: %w", err)
	}
	now := time.Now()
	return issuecorrelation.HelmRecentChanges(releases, input.Name, issuecorrelation.Window{From: now.Add(-since), To: now, OpenEnded: true}), nil
}
