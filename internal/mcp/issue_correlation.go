package mcp

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/internal/timeline"
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
	correlationChangeCap = 3
	// correlationFieldLimit keeps per-ref field diffs compact.
	correlationFieldLimit = 5
)

// correlationMinObservation is the least watch time that justifies a "no
// recent changes" claim. Below it (fresh start, recent restart) the marker is
// omitted entirely — a 90-second-old store asserting anything about the past
// hour would be fiction.
const correlationMinObservation = 5 * time.Minute

// correlationWindow returns the truthful claim window: the default lookback
// clamped to how long this process's store has actually been observing.
// Returns 0 when observation is too short to claim anything.
func correlationWindow() time.Duration {
	window := meaningfulchanges.DefaultSince
	start := timeline.ObservationStart()
	if start.IsZero() {
		return 0
	}
	if observed := time.Since(start); observed < window {
		window = observed
	}
	if window < correlationMinObservation {
		return 0
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
		return // not enough observation to claim anything, in either direction
	}
	checked := 0
	for _, severity := range []issuesapi.Severity{issuesapi.SeverityCritical, issuesapi.SeverityWarning} {
		for i := range resp.Issues {
			iss := &resp.Issues[i]
			if iss.Severity != severity {
				continue
			}
			// Only kinds whose changes the feed records can truthfully claim "no
			// changes" — for untracked kinds the marker is omitted (= unknown).
			// Group-aware: a CRD issue whose kind collides with a tracked one
			// (Knative Service vs core Service) must not be correlated against
			// the same-named core object's changes.
			nativeHelmIssue := iss.Kind == "HelmRelease" && iss.Group == issues.NativeHelmGroup
			if !nativeHelmIssue && !meaningfulchanges.TrackedKindForGroup(iss.Kind, iss.Group) {
				continue
			}
			if checked >= correlationIssueCap {
				resp.CorrelationTruncated = true
				return
			}
			checked++
			correlateIssue(ctx, iss, window, nativeHelmIssue)
		}
	}
}

// correlateIssue attaches CorrelatedChanges or NoRecentChanges to one issue,
// or neither when the answer is unknown (fetch error, saturated fetch).
func correlateIssue(ctx context.Context, iss *issuesapi.Issue, window time.Duration, nativeHelmIssue bool) {
	var changes []issuesapi.RecentChange
	var saturated bool
	var err error
	if nativeHelmIssue {
		changes, saturated, err = helmIssueChangesForCorrelation(ctx, iss, window)
	} else {
		changes, saturated, err = correlationChangesForIssue(ctx, iss, window)
	}
	if err != nil {
		log.Printf("[mcp] issue change correlation failed for %s %s/%s: %v", iss.Kind, iss.Namespace, iss.Name, err)
		return // marker omitted = unknown, never a false "no changes"
	}
	// The marker's contract is non-status evidence: status churn on a
	// failing workload is the SYMPTOM, not a change that could explain it
	// — including it would make every failing issue read as "correlated".
	changes = filterSpecConfigChanges(changes)
	if len(changes) == 0 {
		// A saturated candidate fetch may have missed older changes in
		// the window (churn-heavy subjects overflow the newest-N query) —
		// that's unknown, not "no changes".
		if saturated {
			return
		}
		iss.NoRecentChanges = &issuesapi.NoRecentChangesMarker{
			WindowSeconds: int(window.Seconds()),
		}
		return
	}
	if len(changes) > correlationChangeCap {
		changes = changes[:correlationChangeCap]
	}
	iss.CorrelatedChanges = changes
}

func filterSpecConfigChanges(changes []issuesapi.RecentChange) []issuesapi.RecentChange {
	out := changes[:0]
	for _, c := range changes {
		if c.ChangeCategory == issuesapi.ChangeCategorySpecConfig || c.ChangeCategory == issuesapi.ChangeCategoryLifecycle {
			out = append(out, c)
		}
	}
	return out
}

func helmIssueChangesForCorrelation(ctx context.Context, iss *issuesapi.Issue, window time.Duration) ([]issuesapi.RecentChange, bool, error) {
	changes, err := helmRecentChangesForContext(ctx, getChangesInput{
		Namespace: iss.Namespace,
		Kind:      "HelmRelease",
		Name:      iss.Name,
	}, window)
	return changes, false, err
}

func correlationChangesForIssue(ctx context.Context, iss *issuesapi.Issue, window time.Duration) ([]issuesapi.RecentChange, bool, error) {
	if meaningfulchanges.WorkloadKind(iss.Kind) {
		// Workload subjects also correlate against their directly referenced
		// ConfigMaps; obj==nil degrades to workload-only changes.
		obj := workloadObjectFromCache(iss.Kind, iss.Namespace, iss.Name)
		return meaningfulchanges.RecentForWorkloadAndConfigMaps(
			ctx, obj, iss.Kind, iss.Namespace, iss.Name,
			window, correlationChangeCap, correlationFieldLimit,
		)
	}
	return meaningfulchanges.RecentForResource(
		ctx, iss.Kind, iss.Namespace, iss.Name,
		window, correlationChangeCap, correlationFieldLimit,
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
