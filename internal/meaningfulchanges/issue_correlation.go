package meaningfulchanges

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
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
	// CorrelationIssueCap bounds the per-issue lookups per issue list, shared
	// by criticals and warnings — criticals consume slots first, so a
	// warning can never cost a critical its lookup. When the cap skips
	// issues, the caller says so explicitly — an unmarked issue under
	// truncation means "not checked", never "no changes".
	CorrelationIssueCap = 10
	// CorrelationChangeCap bounds refs per issue: the top-ranked few changes
	// are the evidence; the full feed stays one change-feed query away.
	CorrelationChangeCap = 3
	// CorrelationFieldLimit keeps per-ref field diffs compact.
	CorrelationFieldLimit = 5
)

// correlationMinObservation is the least watch time that justifies a "no
// recent changes" claim. Below it (fresh start, recent restart) the marker is
// omitted entirely — a 90-second-old store asserting anything about the past
// hour would be fiction.
const correlationMinObservation = 5 * time.Minute

// ErrCorrelationNotPermitted is returned by a CorrelationSources lookup when
// the caller may not read the subject's history.
var ErrCorrelationNotPermitted = errors.New("not permitted to read change history")

// CorrelationSources carries what differs between surfaces. REST and MCP
// authorize through different permission caches, so each passes its own.
type CorrelationSources struct {
	// Visible drops change rows the caller may not read. Required.
	Visible func(context.Context, []issuesapi.RecentChange) []issuesapi.RecentChange
	// HelmChanges lists one native Helm release's changes within window, read
	// as the caller. Nil leaves native Helm subjects unanswered.
	HelmChanges func(ctx context.Context, namespace, name string, window time.Duration) ([]issuesapi.RecentChange, error)
}

// Correlation is one subject's answer: exactly one of Changes,
// NoRecentChanges or Unknown is set.
type Correlation struct {
	Changes         []issuesapi.RecentChange
	NoRecentChanges *issuesapi.NoRecentChangesMarker
	Unknown         issuesapi.CorrelationUnknownReason
}

// CorrelationWindow returns the truthful claim window: the default lookback
// clamped to how long this process has been observing and, once the store
// has dropped events, to the oldest event it still holds. A zero window comes
// with the reason no claim can be made.
func CorrelationWindow() (time.Duration, issuesapi.CorrelationUnknownReason) {
	start := timeline.ObservationStart()
	if start.IsZero() {
		return 0, issuesapi.CorrelationObservationTooShort
	}
	window := DefaultSince
	if observed := time.Since(start); observed < window {
		window = observed
	}
	if window < correlationMinObservation {
		return 0, issuesapi.CorrelationObservationTooShort
	}
	if oldest, ok := retainedHistoryStart(); ok {
		if retained := time.Since(oldest); retained < window {
			window = retained
		}
		if window < correlationMinObservation {
			return 0, issuesapi.CorrelationHistoryIncomplete
		}
	}
	return window, ""
}

// retainedHistoryTTL bounds how often the store is asked for its oldest event:
// database-backed Stats() runs aggregate queries, and every issue list and
// correlation lookup needs the answer.
const retainedHistoryTTL = 10 * time.Second

var retainedHistory struct {
	sync.Mutex
	store   timeline.EventStore
	checked time.Time
	oldest  time.Time
	dropped bool
}

// retainedHistoryStart reports the oldest event the store still holds, when
// the store has dropped events (ring eviction, retention or size trimming).
// Before any drop, observation start already bounds the window.
func retainedHistoryStart() (time.Time, bool) {
	store := timeline.GetStore()
	if store == nil {
		return time.Time{}, false
	}
	retainedHistory.Lock()
	defer retainedHistory.Unlock()
	if retainedHistory.store != store || time.Since(retainedHistory.checked) > retainedHistoryTTL {
		stats := store.Stats()
		retainedHistory.store = store
		retainedHistory.checked = time.Now()
		retainedHistory.oldest = stats.OldestEvent
		retainedHistory.dropped = stats.EventsEvicted && !stats.OldestEvent.IsZero()
	}
	return retainedHistory.oldest, retainedHistory.dropped
}

// IsNativeHelmSubject reports whether an issue subject is a native Helm
// release, whose history comes from release records rather than the timeline.
func IsNativeHelmSubject(kind, group string) bool {
	return kind == "HelmRelease" && group == issues.NativeHelmGroup
}

// CorrelationEligible reports whether "no changes" can be claimed truthfully
// for a subject: only kinds whose changes the feed records qualify.
// Group-aware: a CRD issue whose kind collides with a tracked one (Knative
// Service vs core Service) must not be correlated against the same-named core
// object's changes.
func CorrelationEligible(kind, group string) bool {
	return IsNativeHelmSubject(kind, group) || TrackedKindForGroup(kind, group)
}

// AttachIssueChangeCorrelation fills CorrelatedChanges / NoRecentChanges on
// critical and warning issues, up to CorrelationIssueCap lookups, and reports
// whether the cap skipped any. An issue left unmarked means unknown.
//
// Two passes share one cap: every critical is checked before any warning, so
// cap priority never depends on the list's sort order. Warnings matter
// because the active fault in a degraded-not-down incident is often
// warning-severity (e.g. a Service selector matching no pods) — leaving it
// uncorrelated hands the loudest chronic critical the only evidence trail.
func AttachIssueChangeCorrelation(ctx context.Context, list []issuesapi.Issue, src CorrelationSources) (truncated bool) {
	window, _ := CorrelationWindow()
	if window == 0 {
		return false // not enough observation to claim anything, in either direction
	}
	checked := 0
	for _, severity := range []issuesapi.Severity{issuesapi.SeverityCritical, issuesapi.SeverityWarning} {
		for i := range list {
			iss := &list[i]
			if iss.Severity != severity || !CorrelationEligible(iss.Kind, iss.Group) {
				continue
			}
			if checked >= CorrelationIssueCap {
				return true
			}
			checked++
			c := CorrelateSubject(ctx, iss.Kind, iss.Group, iss.Namespace, iss.Name, window, src)
			iss.CorrelatedChanges = c.Changes
			iss.NoRecentChanges = c.NoRecentChanges
		}
	}
	return false
}

// CorrelateSubject answers one subject within window (from CorrelationWindow).
func CorrelateSubject(ctx context.Context, kind, group, namespace, name string, window time.Duration, src CorrelationSources) Correlation {
	if !CorrelationEligible(kind, group) {
		return Correlation{Unknown: issuesapi.CorrelationUntrackedKind}
	}
	var changes []issuesapi.RecentChange
	var saturated bool
	var err error
	if IsNativeHelmSubject(kind, group) {
		if src.HelmChanges == nil {
			return Correlation{Unknown: issuesapi.CorrelationLookupFailed}
		}
		changes, err = src.HelmChanges(ctx, namespace, name, window)
	} else {
		changes, saturated, err = subjectChanges(ctx, kind, namespace, name, window)
	}
	if errors.Is(err, ErrCorrelationNotPermitted) {
		return Correlation{Unknown: issuesapi.CorrelationNotPermitted}
	}
	if err != nil {
		log.Printf("[changes] issue change correlation failed for %s %s/%s: %v", kind, namespace, name, err)
		return Correlation{Unknown: issuesapi.CorrelationLookupFailed}
	}
	changes, rbacHidden := visibleEvidence(ctx, changes, src.Visible)
	if len(changes) == 0 {
		// A saturated candidate fetch may have missed older changes in
		// the window (churn-heavy subjects overflow the newest-N query),
		// and an RBAC-hidden change exists even though this caller can't
		// see it — both are unknown, not "no changes".
		if saturated {
			return Correlation{Unknown: issuesapi.CorrelationHistoryIncomplete}
		}
		if rbacHidden {
			return Correlation{Unknown: issuesapi.CorrelationCannotConfirm}
		}
		return Correlation{NoRecentChanges: &issuesapi.NoRecentChangesMarker{
			WindowSeconds: int(window.Seconds()),
		}}
	}
	if len(changes) > CorrelationChangeCap {
		changes = changes[:CorrelationChangeCap]
	}
	return Correlation{Changes: changes}
}

// visibleEvidence reduces candidate changes to the evidence this caller may
// see. The marker's contract is non-status evidence: status churn on a failing
// workload is the SYMPTOM, not a change that could explain it — including it
// would make every failing issue read as "correlated". The category filter
// runs first so rbacHidden only reflects rows that would have counted as
// evidence; per-kind RBAC then drops rows the caller can't read (a workload
// subject's correlated changes include its consumed ConfigMaps).
//
// rbacHidden=true means relevant evidence exists that this caller can't see —
// the marker path must treat that as unknown, never as an affirmative
// no_recent_changes claim (the consumer would read "chronic issue, nothing
// changed" for a workload whose consumed ConfigMap just rotated).
func visibleEvidence(ctx context.Context, changes []issuesapi.RecentChange, visible func(context.Context, []issuesapi.RecentChange) []issuesapi.RecentChange) (out []issuesapi.RecentChange, rbacHidden bool) {
	changes = specConfigChanges(changes)
	relevant := len(changes)
	changes = visible(ctx, changes)
	return changes, len(changes) < relevant
}

func specConfigChanges(changes []issuesapi.RecentChange) []issuesapi.RecentChange {
	out := changes[:0]
	for _, c := range changes {
		if c.ChangeCategory == issuesapi.ChangeCategorySpecConfig || c.ChangeCategory == issuesapi.ChangeCategoryLifecycle {
			out = append(out, c)
		}
	}
	return out
}

func subjectChanges(ctx context.Context, kind, namespace, name string, window time.Duration) ([]issuesapi.RecentChange, bool, error) {
	if WorkloadKind(kind) {
		// Workload subjects also correlate against their directly referenced
		// ConfigMaps; obj==nil degrades to workload-only changes.
		obj := workloadObjectFromCache(kind, namespace, name)
		return RecentForWorkloadAndConfigMaps(
			ctx, obj, kind, namespace, name,
			window, CorrelationChangeCap, CorrelationFieldLimit,
		)
	}
	return RecentForResource(
		ctx, kind, namespace, name,
		window, CorrelationChangeCap, CorrelationFieldLimit,
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
