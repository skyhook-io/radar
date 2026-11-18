package mcp

import (
	"context"
	"time"

	"github.com/skyhook-io/radar/internal/issuecorrelation"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/internal/timeline"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// Per-issue change correlation answers the first triage question — "did
// anything change on THIS subject recently, or has it always been like
// this?" — as deterministic per-issue facts. Radar makes no judgment call:
// no demotion, no reordering, no causal claim. A chronic pre-existing issue
// truthfully carries no_recent_changes; an incident workload carries the
// correlated change refs; the consumer weighs them.
const (
	// correlationIssueCap bounds the per-issue lookups per response, shared
	// by criticals and warnings — criticals consume slots first, so a
	// warning can never cost a critical its lookup. When the cap skips
	// issues, Response.CorrelationTruncated says so explicitly — an unmarked
	// issue under truncation means "not checked", never "no changes".
	correlationIssueCap = 10
	// correlationChangeCap bounds refs per issue: the top-ranked few changes
	// are the evidence; the full feed stays one get_changes call away.
	correlationChangeCap = issuecorrelation.ChangeCap
)

// correlationMinObservation is the least watch time that justifies a claim
// resting on the absence of a change: the no_recent_changes marker, or an
// admission order inferred from one side having no recorded change. Below it
// (fresh start, recent restart) deferred informers may still be filling, so
// only recorded changes are reported.
const correlationMinObservation = 5 * time.Minute

// correlationWindow returns the truthful claim window: the default lookback
// clamped to how long this process's store has actually been observing.
// Returns 0 when no store is observing.
func correlationWindow() time.Duration {
	window := meaningfulchanges.DefaultSince
	start := timeline.ObservationStart()
	if start.IsZero() {
		return 0
	}
	if observed := time.Since(start); observed < window {
		window = observed
	}
	return window
}

// attachIssueChangeCorrelation fills CorrelatedChanges / NoRecentChanges on
// critical and warning issues. Single-namespace responses only —
// cross-namespace listings are inventory sweeps where per-issue timeline
// lookups would multiply cost without a triage question on the table.
//
// Two passes share one cap: every critical is checked before any warning, so
// cap priority never depends on the response's sort order. Warnings matter
// because the active fault in a degraded-not-down incident is often
// warning-severity (e.g. a Service selector matching no pods) — leaving it
// uncorrelated hands the loudest chronic critical the only evidence trail.
func attachIssueChangeCorrelation(ctx context.Context, resp *issues.ListResponse) {
	window := correlationWindow()
	if window == 0 {
		return // no store observing
	}
	opts := correlationOptions(window)
	access := correlationAccess(ctx)
	checked := 0
	for _, severity := range []issuesapi.Severity{issuesapi.SeverityCritical, issuesapi.SeverityWarning} {
		for i := range resp.Issues {
			iss := &resp.Issues[i]
			if iss.Severity != severity || !issuecorrelation.Eligible(iss) {
				continue
			}
			if checked >= correlationIssueCap {
				resp.CorrelationTruncated = true
				return
			}
			checked++
			applyCorrelation(iss, issuecorrelation.Correlate(ctx, *iss, opts, access), window)
		}
	}
}

// correlationOptions is the tool's policy: the window ends whenever the read
// runs, and the most significant changes are kept.
func correlationOptions(window time.Duration) issuecorrelation.Options {
	now := time.Now()
	return issuecorrelation.Options{
		Window:    issuecorrelation.Window{From: now.Add(-window), To: now, OpenEnded: true},
		Selection: issuecorrelation.SelectBySignificance,
		MinWindow: correlationMinObservation,
	}
}

// applyCorrelation writes a correlation onto the issue as the tool reports
// it: the changes, or the no_recent_changes marker, and corrected timing.
// An issue with neither means unknown, never "no changes".
func applyCorrelation(iss *issuesapi.Issue, result issuecorrelation.Result, window time.Duration) {
	if result.Timing.Changed {
		iss.IssueTiming, iss.IssueTimingBasis, iss.TimingSummary = result.Timing.IssueTiming, result.Timing.IssueTimingBasis, result.Timing.TimingSummary
	}
	if len(result.Changes) > 0 {
		iss.CorrelatedChanges = result.Changes
		iss.CorrelatedChangesIncomplete = result.FetchSaturated
		return
	}
	if result.NoChanges {
		iss.NoRecentChanges = &issuesapi.NoRecentChangesMarker{WindowSeconds: int(window.Seconds())}
	}
}

// correlationAccess reads as the ctx user. The zero Access when auth is off.
func correlationAccess(ctx context.Context) issuecorrelation.Access {
	access := issuecorrelation.Access{
		HelmHistory: func(ctx context.Context, storageNamespace, name string, window issuecorrelation.Window) ([]issuesapi.RecentChange, error) {
			return helmRecentChangesForContext(ctx, getChangesInput{Namespace: storageNamespace, Kind: "HelmRelease", Name: name}, window.To.Sub(window.From))
		},
	}
	if pkgauth.UserFromContext(ctx) == nil {
		return access
	}
	access.Check = func(kind, group, namespace string) issuecorrelation.Decision {
		resolvedGroup, resource, clusterScoped, ok := k8s.ResolveChangeGVR(kind, group)
		if !ok {
			return issuecorrelation.CheckFailed
		}
		if clusterScoped {
			namespace = ""
		}
		allowed, authoritative := canReadInNamespaceDecision(ctx, resolvedGroup, resource, namespace, "list")
		switch {
		case allowed:
			return issuecorrelation.Allowed
		case authoritative:
			return issuecorrelation.Forbidden
		}
		return issuecorrelation.CheckFailed
	}
	access.RowFilter = recentChangeReadFilter(ctx)
	access.Viewer = recentChangeViewer(ctx)
	return access
}
