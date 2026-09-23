package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

const helmChangeSource = meaningfulchanges.HelmChangeSource

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
	return meaningfulchanges.HelmRecentChanges(releases, input.Name, since, time.Now()), nil
}
