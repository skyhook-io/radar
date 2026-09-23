package mcp

import (
	"context"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// attachIssueChangeCorrelation fills CorrelatedChanges / NoRecentChanges on
// critical and warning issues. Single-namespace responses only —
// cross-namespace listings are inventory sweeps where per-issue timeline
// lookups would multiply cost without a triage question on the table.
func attachIssueChangeCorrelation(ctx context.Context, resp *issues.ListResponse) {
	if meaningfulchanges.AttachIssueChangeCorrelation(ctx, resp.Issues, mcpCorrelationSources) {
		resp.CorrelationTruncated = true
	}
}

var mcpCorrelationSources = meaningfulchanges.CorrelationSources{
	Visible: filterRecentChangesRBAC,
	HelmChanges: func(ctx context.Context, namespace, name string, window time.Duration) ([]issuesapi.RecentChange, error) {
		return helmRecentChangesForContext(ctx, getChangesInput{
			Namespace: namespace,
			Kind:      "HelmRelease",
			Name:      name,
		}, window)
	},
}
