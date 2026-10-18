package mcp

import (
	"context"
	"log"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
			subjectTracked := nativeHelmIssue || meaningfulchanges.TrackedKindForGroup(iss.Kind, iss.Group)
			// An admission rejection on an untracked subject (a Job, a
			// ReplicaSet) is still correlated against the policy that
			// rejected it.
			if !subjectTracked && !isAdmissionIssue(iss) {
				continue
			}
			if checked >= correlationIssueCap {
				resp.CorrelationTruncated = true
				return
			}
			checked++
			correlateIssue(ctx, iss, window, nativeHelmIssue, subjectTracked)
		}
	}
}

// correlateIssue attaches CorrelatedChanges or NoRecentChanges to one issue,
// or neither when the answer is unknown (fetch error, saturated fetch,
// evicted history). An admission issue whose message names its blocking
// object also carries that object's changes and which side changed last.
func correlateIssue(ctx context.Context, iss *issuesapi.Issue, window time.Duration, nativeHelmIssue, subjectTracked bool) {
	var result meaningfulchanges.RecentResult
	var sourcesWithheld bool
	var obj any
	var changes []issuesapi.RecentChange
	var rbacHidden bool
	if subjectTracked {
		var err error
		if nativeHelmIssue {
			// Helm history comes from release records, not the timeline store,
			// so timeline coverage does not apply.
			result.Changes, err = helmIssueChangesForCorrelation(ctx, iss, window)
		} else {
			limit := correlationChangeCap
			if isAdmissionIssue(iss) {
				// Saying which side changed last needs the newest change,
				// which a significance-ranked top 3 can drop.
				limit = admissionOrderFetchLimit
			}
			result, sourcesWithheld, obj, err = correlationChangesForIssue(ctx, iss, window, limit)
		}
		if err != nil {
			log.Printf("[mcp] issue change correlation failed for %s %s/%s: %v", iss.Kind, iss.Namespace, iss.Name, err)
			return // marker omitted = unknown, never a false "no changes"
		}
		changes, rbacHidden = applyCorrelationVisibilityFilters(ctx, result.Changes)
	}
	// A saturated candidate fetch may have missed older changes in the
	// window (churn-heavy subjects overflow the fetch bound), an RBAC-hidden
	// change exists even though this caller can't see it (or a source they
	// can't read was not queried), and evicted history may have held one —
	// all are unknown, not "no changes".
	subjectComplete := subjectTracked && !result.FetchSaturated && !rbacHidden && !sourcesWithheld &&
		correlationHistoryComplete(obj, iss.Namespace, result.History)
	if blocker := admissionBlockerForIssue(ctx, iss, window); blocker != nil &&
		attachAdmissionBlocker(iss, blocker, changes, subjectComplete, window) {
		iss.CorrelatedChangesIncomplete = result.FetchSaturated || blocker.saturated
		return
	}
	if !subjectTracked {
		return
	}
	if len(changes) == 0 {
		if !subjectComplete {
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
	iss.CorrelatedChangesIncomplete = result.FetchSaturated
}

// correlationHistoryComplete reports whether Radar holds the whole window's
// history for the correlated objects: either the store covers the window, or
// every object was created after the point where its history becomes
// complete, so nothing about it can have been evicted. Only a cached workload
// is dated, from its own creation time (a grouped issue's
// resource_created_at can be a newer member's); its referenced ConfigMaps
// must be new too, and one missing from the cache is not known to be new.
func correlationHistoryComplete(obj any, namespace string, coverage issuesapi.HistoryCoverage) bool {
	if !coverage.Incomplete {
		return true
	}
	workload, ok := obj.(metav1.Object)
	if !ok || !workload.GetCreationTimestamp().After(coverage.CompleteSince) {
		return false
	}
	cache := k8s.GetResourceCache()
	for _, name := range meaningfulchanges.DirectConfigMapNames(obj) {
		if cache == nil || cache.ConfigMaps() == nil {
			return false
		}
		cm, err := cache.ConfigMaps().ConfigMaps(namespace).Get(name)
		if err != nil || !cm.CreationTimestamp.After(coverage.CompleteSince) {
			return false
		}
	}
	return true
}

// applyCorrelationVisibilityFilters reduces candidate changes to the evidence
// this caller may see. The marker's contract is non-status evidence: status
// churn on a failing workload is the SYMPTOM, not a change that could explain
// it — including it would make every failing issue read as "correlated". The
// category filter runs first so rbacHidden only reflects rows that would have
// counted as evidence; per-kind RBAC then drops rows the caller can't read (a
// workload subject's correlated changes include its consumed ConfigMaps).
//
// rbacHidden=true means relevant evidence exists that this caller can't see —
// the marker path must treat that as unknown, never as an affirmative
// no_recent_changes claim (the consumer would read "chronic issue, nothing
// changed" for a workload whose consumed ConfigMap just rotated).
func applyCorrelationVisibilityFilters(ctx context.Context, changes []issuesapi.RecentChange) (visible []issuesapi.RecentChange, rbacHidden bool) {
	changes = filterSpecConfigChanges(changes)
	relevant := len(changes)
	changes = filterRecentChangesRBAC(ctx, changes)
	return changes, len(changes) < relevant
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

func helmIssueChangesForCorrelation(ctx context.Context, iss *issuesapi.Issue, window time.Duration) ([]issuesapi.RecentChange, error) {
	return helmRecentChangesForContext(ctx, getChangesInput{
		Namespace: iss.Namespace,
		Kind:      "HelmRelease",
		Name:      iss.Name,
	}, window)
}

// correlationChangesForIssue also reports whether sources were withheld from
// the caller, and returns the workload object whose ConfigMaps were
// correlated, or nil.
func correlationChangesForIssue(ctx context.Context, iss *issuesapi.Issue, window time.Duration, limit int) (meaningfulchanges.RecentResult, bool, any, error) {
	if meaningfulchanges.WorkloadKind(iss.Kind) {
		// Workload subjects also correlate against their directly referenced
		// ConfigMaps; obj==nil degrades to workload-only changes.
		obj := workloadObjectFromCache(iss.Kind, iss.Namespace, iss.Name)
		result, sourcesWithheld, err := meaningfulchanges.RecentForWorkloadAndConfigMapsAuthorizedDetailed(
			ctx, obj, iss.Kind, iss.Namespace, iss.Name,
			window, limit, correlationFieldLimit, configMapSourceFilter(ctx, iss.Namespace),
		)
		return result, sourcesWithheld, obj, err
	}
	result, err := meaningfulchanges.RecentForResourceDetailed(
		ctx, iss.Kind, iss.Namespace, iss.Name,
		window, limit, correlationFieldLimit,
	)
	return result, false, nil, err
}

// configMapSourceFilter skips a workload's referenced ConfigMaps the caller
// can't read before they are queried, so neither their rows nor their fetch
// saturation reach the caller. nil when auth is off.
func configMapSourceFilter(ctx context.Context, namespace string) func(kind, name string) bool {
	allow := recentChangeReadFilter(ctx)
	if allow == nil {
		return nil
	}
	return func(kind, _ string) bool {
		return kind != "ConfigMap" || allow(issuesapi.RecentChange{Kind: "ConfigMap", APIVersion: "v1", Namespace: namespace})
	}
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
