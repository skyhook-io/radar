package mcp

import (
	"context"
	"strings"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// Per-issue change correlation answers the first triage question — "did
// anything change on THIS subject recently, or has it always been like
// this?" — as deterministic per-issue facts. Radar makes no judgment call:
// no demotion, no reordering, no causal claim. A chronic decoy issue
// truthfully carries no_recent_changes; an incident workload carries the
// correlated change refs; the consumer weighs them.
const (
	// correlationIssueCap bounds the per-issue lookups per response. When the
	// cap skips criticals, Response.CorrelationTruncated says so explicitly —
	// an unmarked issue under truncation means "not checked", never "no
	// changes".
	correlationIssueCap = 10
	// correlationChangeCap bounds refs per issue: the top-ranked few changes
	// are the evidence; the full feed stays one get_changes call away.
	correlationChangeCap = 3
	// correlationFieldLimit keeps per-ref field diffs compact.
	correlationFieldLimit = 5
)

// attachIssueChangeCorrelation fills CorrelatedChanges / NoRecentChanges on
// critical issues. Single-namespace responses only — cross-namespace listings
// are inventory sweeps where per-issue timeline lookups would multiply cost
// without a triage question on the table.
func attachIssueChangeCorrelation(ctx context.Context, resp *issues.ListResponse) {
	checked := 0
	for i := range resp.Issues {
		iss := &resp.Issues[i]
		if iss.Severity != issuesapi.SeverityCritical {
			continue
		}
		// Only kinds whose changes the feed records can truthfully claim "no
		// changes" — for untracked kinds the marker is omitted (= unknown).
		if !meaningfulchanges.TrackedKind(iss.Kind) {
			continue
		}
		if checked >= correlationIssueCap {
			resp.CorrelationTruncated = true
			return
		}
		checked++

		changes, err := correlationChangesForIssue(ctx, iss)
		if err != nil {
			continue // marker omitted = unknown, never a false "no changes"
		}
		// The marker's contract is spec/config evidence: status churn on a
		// failing workload is the SYMPTOM, not a change that could explain it
		// — including it would make every failing issue read as "correlated".
		changes = filterSpecConfigChanges(changes)
		if len(changes) == 0 {
			iss.NoRecentChanges = &issuesapi.NoRecentChangesMarker{
				WindowSeconds: int(meaningfulchanges.DefaultSince.Seconds()),
			}
			continue
		}
		if len(changes) > correlationChangeCap {
			changes = changes[:correlationChangeCap]
		}
		iss.CorrelatedChanges = changes
	}
}

func filterSpecConfigChanges(changes []issuesapi.RecentChange) []issuesapi.RecentChange {
	out := changes[:0]
	for _, c := range changes {
		if c.ChangeCategory == "spec_config" || c.ChangeCategory == "lifecycle" {
			out = append(out, c)
		}
	}
	return out
}

func correlationChangesForIssue(ctx context.Context, iss *issuesapi.Issue) ([]issuesapi.RecentChange, error) {
	if meaningfulchanges.WorkloadKind(iss.Kind) {
		// Workload subjects also correlate against their directly referenced
		// ConfigMaps; obj==nil degrades to workload-only changes.
		obj := workloadObjectFromCache(iss.Kind, iss.Namespace, iss.Name)
		return meaningfulchanges.RecentForWorkloadAndConfigMaps(
			ctx, obj, iss.Kind, iss.Namespace, iss.Name,
			meaningfulchanges.DefaultSince, correlationChangeCap, correlationFieldLimit,
		)
	}
	return meaningfulchanges.RecentForResource(
		ctx, iss.Kind, iss.Namespace, iss.Name,
		meaningfulchanges.DefaultSince, correlationChangeCap, correlationFieldLimit,
	)
}

func workloadObjectFromCache(kind, namespace, name string) any {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	switch strings.ToLower(kind) {
	case "deployment":
		if l := cache.Deployments(); l != nil {
			if o, err := l.Deployments(namespace).Get(name); err == nil {
				return o
			}
		}
	case "statefulset":
		if l := cache.StatefulSets(); l != nil {
			if o, err := l.StatefulSets(namespace).Get(name); err == nil {
				return o
			}
		}
	case "daemonset":
		if l := cache.DaemonSets(); l != nil {
			if o, err := l.DaemonSets(namespace).Get(name); err == nil {
				return o
			}
		}
	}
	return nil
}
