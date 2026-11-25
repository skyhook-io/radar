// Package issuecorrelation answers, for one issue, "what changed on this
// around the time it broke?" as deterministic facts: the subject's own
// changes, its directly referenced ConfigMaps and Secrets, and for an
// admission rejection the policy that rejected it. It makes no causal claim.
//
// Two callers share it: the MCP issues tool (a fixed lookback ending now,
// top changes by significance) and the per-resource issues endpoint (a
// window anchored on the issue's first_seen, the earliest time evidence shows
// it active, or the last 24 hours when it has none). Each reads as its own
// caller through Access.
package issuecorrelation

import (
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

const (
	// ChangeCap bounds the changes a significance-selected issue carries: the
	// top-ranked few are the evidence; the full feed stays one query away.
	ChangeCap = 3
	// FieldLimit keeps per-change field diffs compact.
	FieldLimit = 5
	// anchoredBefore and anchoredAfter bound an anchored selection: changes
	// up to first seen, then the newest after it.
	anchoredBefore = 3
	anchoredAfter  = 2
	// anchorSlop counts a change recorded just after first seen as before
	// it: event times are whole seconds and can trail the change.
	anchorSlop = 30 * time.Second
)

// Decision is the answer to a caller's permission check.
type Decision int

const (
	Allowed Decision = iota
	Forbidden
	// CheckFailed means Radar couldn't complete the check. It is not a denial.
	CheckFailed
)

// ErrForbidden marks a HelmHistory error as the caller being refused.
var ErrForbidden = errors.New("the caller may not read this release's storage")

// Access is how the engine reads as its caller. Its zero value reads with
// no restriction (auth off).
type Access struct {
	// Check decides whether the caller may list kind (in group) in namespace.
	// Every source an answer depends on is checked before it is queried, so
	// what is said about permissions never depends on what hidden data
	// exists. nil allows everything.
	Check func(kind, group, namespace string) Decision
	// RowFilter drops change rows the caller may not read after a query, as
	// a second line behind Check. nil keeps every row.
	RowFilter func(issuesapi.RecentChange) bool
	// Viewer gates the Pod and workload names change rows carry.
	Viewer meaningfulchanges.Viewer
	// HelmHistory returns a native Helm release's operations in the window,
	// read as the caller. An error wrapping ErrForbidden is a denial.
	HelmHistory func(ctx context.Context, storageNamespace, name string, window Window) ([]issuesapi.RecentChange, error)
}

// Window bounds the changes read.
type Window struct {
	From time.Time
	To   time.Time
	// OpenEnded also reads changes recorded after To; To then only sizes the
	// window in sentences. The MCP lookback is "the last hour", whenever the
	// read runs.
	OpenEnded bool
}

func (w Window) length() time.Duration { return w.To.Sub(w.From) }

// Selection picks which of a window's changes an issue carries.
type Selection int

const (
	// SelectBySignificance keeps the ChangeCap most significant changes.
	SelectBySignificance Selection = iota
	// SelectAroundFirstSeen keeps the changes closest before the issue was
	// first seen, then the newest after it.
	SelectAroundFirstSeen
)

// Options are one caller's correlation policy.
type Options struct {
	Window    Window
	Selection Selection
	// MinWindow is the shortest window over which an absence of changes is
	// claimed: a shorter one (a fresh start) may still be filling.
	MinWindow time.Duration
	// PolicyOnlyAbsence lets an admission rejection on a subject whose kind
	// isn't tracked claim absence for the rejecting policy alone (PolicyOnly).
	PolicyOnlyAbsence bool
	// EventIDs asks for each change's timeline event ID, so a change can link
	// to its event.
	EventIDs bool
}

// Timing is what the issue's timing fields should read after correlation.
type Timing struct {
	Changed          bool
	IssueTiming      string
	IssueTimingBasis string
	TimingSummary    string
}

// Result is one issue's correlation.
type Result struct {
	// Eligible is false when the issue's subject is neither a tracked kind,
	// a native Helm release, nor rejected by admission. Nothing else is set.
	Eligible bool
	Changes  []issuesapi.RecentChange
	// MoreNotShown is set when the window held more changes the caller may
	// see than Changes lists: after status rows, hidden rows and every cut.
	MoreNotShown bool
	// FetchSaturated means a read hit its bound, so older changes in the
	// window were not read.
	FetchSaturated bool
	// NoChanges means the window held no change on any source, and every
	// source was read in full.
	NoChanges bool
	// NoChangesWithheld means the subject's own reads found nothing and were
	// complete, but absence isn't claimed because Radar can't vouch for the
	// rest: a workload's sources couldn't be resolved, a source's kind isn't
	// collected or is still loading, a kind involved was watched only since
	// after the window began, or the admission policy side is unknown.
	// Unknown says why.
	NoChangesWithheld bool
	// PolicyOnly scopes NoChanges to the admission policy that rejected the
	// issue: the subject's own kind isn't tracked, so only the policy was
	// checked.
	PolicyOnly bool
	Unknown    *issuesapi.CorrelationUnknown
	History    issuesapi.HistoryCoverage
	Timing     Timing
}

// IsAdmissionIssue reports an admission rejection, which is correlated
// against the policy that rejected it whatever its subject's kind.
func IsAdmissionIssue(iss *issuesapi.Issue) bool {
	return iss.Category == issuesapi.CategoryQuotaExceeded || iss.Category == issuesapi.CategoryAdmissionWebhookBlocking
}

// Eligible reports whether an issue can be correlated: only kinds whose
// changes Radar records can truthfully carry "no changes". Group-aware, so a
// CRD whose kind collides with a tracked one (Knative Service vs core
// Service) is not correlated against the core object's changes.
func Eligible(iss *issuesapi.Issue) bool {
	return isNativeHelm(iss) || meaningfulchanges.TrackedKindForGroup(iss.Kind, iss.Group) || IsAdmissionIssue(iss)
}

func isNativeHelm(iss *issuesapi.Issue) bool {
	return iss.Kind == "HelmRelease" && iss.Group == issues.NativeHelmGroup
}

// Correlate reads one issue's correlated changes. It does not modify iss.
// Requests run sequentially, and slices it returns are its own.
func Correlate(ctx context.Context, iss issuesapi.Issue, opts Options, access Access) Result {
	if !Eligible(&iss) {
		return Result{}
	}
	c := &run{ctx: ctx, iss: iss, opts: opts, access: access, unknown: map[issuesapi.CorrelationUnknownReason][]issuesapi.UncheckedKind{}}
	res := c.correlate()
	res.Eligible = true
	res.History = mergeHistory(mergeHistory(res.History, c.watchHistory), c.objectWatchHistory)
	res.Unknown = c.primaryUnknown()
	if c.iss.IssueTiming != iss.IssueTiming || c.iss.IssueTimingBasis != iss.IssueTimingBasis || c.iss.TimingSummary != iss.TimingSummary {
		res.Timing = Timing{Changed: true, IssueTiming: c.iss.IssueTiming, IssueTimingBasis: c.iss.IssueTimingBasis, TimingSummary: c.iss.TimingSummary}
	}
	return res
}

type run struct {
	ctx     context.Context
	iss     issuesapi.Issue
	opts    Options
	access  Access
	unknown map[issuesapi.CorrelationUnknownReason][]issuesapi.UncheckedKind
	// watchHistory is the coverage lost to kinds Radar started watching
	// after the window began.
	watchHistory issuesapi.HistoryCoverage
	// objectWatchHistory is the same for the subject workload's kind and its
	// sources' kinds, which historyCoversObjects can still clear.
	objectWatchHistory issuesapi.HistoryCoverage
}

func (c *run) correlate() Result {
	iss := &c.iss
	nativeHelm := isNativeHelm(iss)
	subjectTracked := nativeHelm || meaningfulchanges.TrackedKindForGroup(iss.Kind, iss.Group)
	var res Result
	var result meaningfulchanges.RecentResult
	var obj any
	var refs []configrefs.Ref
	var changes []issuesapi.RecentChange
	subjectAllowed, sourcesKnown, sourcesResolved := true, true, true
	if subjectTracked {
		switch {
		case nativeHelm:
			// Helm history comes from release records, not the timeline
			// store, so timeline coverage does not apply.
			rows, err := c.helmHistory()
			if err != nil {
				if errors.Is(err, ErrForbidden) {
					c.addUnknown(issuesapi.CorrelationUnknownUserForbidden, iss.Group, iss.Kind)
				} else {
					log.Printf("[issuecorrelation] Failed to read Helm history for %s/%s: %v", iss.Namespace, iss.Name, err)
					c.addUnknown(issuesapi.CorrelationUnknownReadError, iss.Group, iss.Kind)
				}
				return res
			}
			result.Changes = rows
		case !c.allowed(iss.Kind, iss.Group, iss.Namespace):
			// A subject the caller can't list is not read: neither its own
			// changes nor the sources its spec names, since the names
			// themselves come from an object the caller can't see.
			subjectAllowed = false
		default:
			var err error
			result, obj, refs, sourcesKnown, sourcesResolved, err = c.subjectChanges()
			if err != nil {
				log.Printf("[issuecorrelation] Failed to read changes for %s %s/%s: %v", iss.Kind, iss.Namespace, iss.Name, err)
				c.addUnknown(issuesapi.CorrelationUnknownReadError, iss.Group, iss.Kind)
				return res
			}
		}
		changes = c.visible(result.Changes)
		res.History = result.History
	}
	// A saturated read may have missed older changes, a source the caller
	// can't read was not queried, and evicted history may have held one:
	// all are unknown, not "no changes".
	// Kinds watched late so far are the subject's and its sources'; the
	// admission policy's are added below.
	storeHistoryComplete := historyCoversObjects(obj, refs, iss.Namespace, result.History)
	// A late watch on the workload's or its sources' kinds is cleared when
	// every object was created after it.
	watchComplete := !c.watchHistory.Incomplete &&
		(!c.objectWatchHistory.Incomplete || historyCoversObjects(obj, refs, iss.Namespace, mergeHistory(result.History, c.objectWatchHistory)))
	historyComplete := storeHistoryComplete && watchComplete
	if subjectTracked && !historyComplete {
		c.addHistoryUnknown(iss)
	}
	// subjectRead: every source that was resolved was read in full from the
	// history the store holds. subjectComplete also needs the sources
	// resolved and every kind watched since before the window began: only
	// then is an empty subject list a true "no recorded change".
	subjectRead := subjectTracked && subjectAllowed && sourcesKnown && !result.FetchSaturated && storeHistoryComplete
	subjectComplete := subjectRead && sourcesResolved && watchComplete
	absenceKnown := c.opts.Window.length() >= c.opts.MinWindow

	res.FetchSaturated = result.FetchSaturated

	blocker, blockerErr := c.admissionBlocker()
	if blockerErr != nil {
		// Read as "no blocker" for the changes listed, but the policy side
		// is unknown.
		c.addUnknown(issuesapi.CorrelationUnknownReadError, "", blockerErr.kind)
	}
	if blocker != nil {
		res.History = mergeHistory(res.History, blocker.history)
		res.FetchSaturated = res.FetchSaturated || blocker.saturated
		if shown, more, ok := c.attachAdmissionBlocker(blocker, changes, subjectComplete, absenceKnown); ok {
			res.Changes, res.MoreNotShown = shown, more || result.OutputCapped || blocker.outputCapped
			return res
		}
	}
	if !subjectTracked || len(changes) == 0 {
		// Absence needs every source read in full and nothing unknown: an
		// unresolved or uncollected source, a kind watched late, or an
		// unknown policy side may hold the change.
		if subjectRead && absenceKnown {
			if len(c.unknown) == 0 && (blocker == nil || blocker.complete) {
				res.NoChanges = true
			} else {
				res.NoChangesWithheld = true
			}
		}
		// An admission rejection on a subject whose own changes aren't
		// tracked (a Job, a bare ReplicaSet) can still say the policy that
		// rejected it didn't change, scoped to the policy. The MCP tool
		// says nothing for such a subject.
		if !subjectTracked && c.opts.PolicyOnlyAbsence && blocker != nil && blocker.complete && len(blocker.changes) == 0 && absenceKnown && len(c.unknown) == 0 {
			res.NoChanges, res.PolicyOnly = true, true
		}
		return res
	}
	res.Changes, res.MoreNotShown = c.selectChanges(changes)
	res.MoreNotShown = res.MoreNotShown || result.OutputCapped
	return res
}

// mergeHistory combines two reads' coverage: incomplete when either is, with
// both reasons and the later point from which history is complete.
func mergeHistory(a, b issuesapi.HistoryCoverage) issuesapi.HistoryCoverage {
	if !b.Incomplete {
		return a
	}
	if !a.Incomplete {
		return b
	}
	out := issuesapi.HistoryCoverage{Incomplete: true, CompleteSince: a.CompleteSince, Reasons: slices.Clone(a.Reasons)}
	if b.CompleteSince.After(out.CompleteSince) {
		out.CompleteSince = b.CompleteSince
	}
	for _, reason := range b.Reasons {
		if !slices.Contains(out.Reasons, reason) {
			out.Reasons = append(out.Reasons, reason)
		}
	}
	return out
}

// subjectChanges reads a tracked subject's changes, and a workload's
// directly referenced ConfigMaps and Secrets. sourcesKnown is false when a
// source was withheld from the caller, or when Radar doesn't fully hold the
// subject's own kind yet. sourcesResolved is false when Radar couldn't read
// the workload's pod template, so which sources it uses is unknown. obj is
// the workload whose sources were resolved, or nil, and refs are those
// sources.
func (c *run) subjectChanges() (result meaningfulchanges.RecentResult, obj any, refs []configrefs.Ref, sourcesKnown, sourcesResolved bool, err error) {
	iss := &c.iss
	q := c.query(c.subjectLimit())
	if !meaningfulchanges.WorkloadKind(iss.Kind) {
		q.Namespaces, q.Kinds, q.Name, q.TrackedOnly = []string{iss.Namespace}, []string{iss.Kind}, iss.Name, true
		// A subject kind Radar doesn't fully hold yet can't be said to be
		// unchanged. A late watch goes to watchHistory.
		subjectReady := c.kindCollected(iss.Kind, iss.Group, iss.Namespace)
		if subjectReady {
			c.watchCovers(iss.Kind, iss.Group, iss.Namespace)
		}
		result, err = meaningfulchanges.Recent(c.ctx, q)
		return result, nil, nil, subjectReady, true, err
	}
	obj = WorkloadObjectFromCache(iss.Kind, iss.Namespace, iss.Name)
	sourcesKnown, sourcesResolved = true, true
	// A Rollout that takes its template from a workloadRef target names its
	// sources through that object, so the caller must be able to list the
	// target's kind before the template is read.
	if group, kind, ok := meaningfulchanges.RolloutTemplateSource(obj); ok && !c.allowed(kind, group, iss.Namespace) {
		obj, sourcesKnown = nil, false
	} else if !meaningfulchanges.ConfigReferencesKnown(obj) {
		// A workload missing from the cache, or a Rollout whose template
		// can't be read (an unsupported or missing workloadRef target), has
		// unknown sources, so absence proves nothing.
		sourcesResolved = false
		c.addUnknown(issuesapi.CorrelationUnknownSourcesUnresolved, iss.Group, iss.Kind)
	}
	// The sources are resolved once: the ones checked are the only ones read.
	refs = meaningfulchanges.DirectConfigReferences(obj)
	decided := map[string]bool{}
	for _, ref := range refs {
		if _, ok := decided[ref.Kind]; !ok {
			decided[ref.Kind] = c.allowed(ref.Kind, "", iss.Namespace)
			if decided[ref.Kind] {
				if c.kindCollected(ref.Kind, "", iss.Namespace) {
					c.objectWatch(ref.Kind, "", iss.Namespace)
				}
			}
		}
	}
	subjectReady := c.kindCollected(iss.Kind, iss.Group, iss.Namespace)
	if subjectReady {
		c.objectWatch(iss.Kind, iss.Group, iss.Namespace)
	}
	result, withheld, err := meaningfulchanges.RecentForWorkloadSourceRefs(c.ctx, refs, iss.Kind, iss.Namespace, iss.Name, q, c.sourceFilter(decided))
	return result, obj, refs, sourcesKnown && subjectReady && !withheld, sourcesResolved, err
}

// sourceFilter admits the subject, which was checked before its sources
// were resolved, and the sources whose kind the caller may list. A source
// whose kind was never checked fails closed: it isn't read, and the caller is
// told its access couldn't be checked.
func (c *run) sourceFilter(decided map[string]bool) func(kind, name string) bool {
	subjectKind := c.iss.Kind
	return func(kind, name string) bool {
		if strings.EqualFold(kind, subjectKind) && name == c.iss.Name {
			return true
		}
		allowed, ok := decided[kind]
		if !ok {
			c.addUnknown(issuesapi.CorrelationUnknownPermissionCheckFailed, "", kind)
		}
		return ok && allowed
	}
}

func (c *run) subjectLimit() int {
	if IsAdmissionIssue(&c.iss) {
		// Saying which side changed last needs the newest change, which a
		// significance-ranked top 3 can drop.
		return admissionOrderFetchLimit
	}
	return ChangeCap
}

// query is the read every source shares. limit is the significance cut of
// a significance-selected read. An anchored read has no such cut: it drops
// status rows and rows the caller can't see and returns every other row read
// within the fetch bounds; the rows shown are picked afterwards.
func (c *run) query(limit int) meaningfulchanges.Query {
	q := meaningfulchanges.Query{
		From: c.opts.Window.From, Limit: limit, FieldLimit: FieldLimit, Viewer: c.access.Viewer,
		EventIDs: c.opts.EventIDs,
	}
	if !c.opts.Window.OpenEnded {
		q.To = c.opts.Window.To
	}
	if c.opts.Selection == SelectAroundFirstSeen {
		rowFilter := c.access.RowFilter
		q.Allow = func(change issuesapi.RecentChange) bool {
			return isEvidence(change) && (rowFilter == nil || rowFilter(change))
		}
		// Every qualifying row is kept: there is no significance cut, and the
		// rows shown are picked by time around first seen only after the
		// admission order and timing have seen all of them. The parts before
		// and after first seen are read with separate bounds.
		q.Select = func(changes []issuesapi.RecentChange) ([]issuesapi.RecentChange, bool) {
			return changes, false
		}
		if !c.iss.FirstSeen.IsZero() {
			q.SplitAt = c.iss.FirstSeen.Add(anchorSlop)
		}
	}
	return q
}

// visible reduces candidate changes to the evidence this caller may see. The
// contract is non-status evidence: status churn on a failing workload is the
// symptom, not a change that could explain it. Every source was checked
// before it was read, so the row filter is a second line only: a row it drops
// leaves the same answer as no row, and never makes the answer unknown.
// Helm rows are read as the caller already, so the row filter skips them.
func (c *run) visible(changes []issuesapi.RecentChange) []issuesapi.RecentChange {
	out := make([]issuesapi.RecentChange, 0, len(changes))
	for _, change := range changes {
		if !isEvidence(change) {
			continue
		}
		if change.Source != HelmChangeSource && c.access.RowFilter != nil && !c.access.RowFilter(change) {
			continue
		}
		out = append(out, change)
	}
	return out
}

func isEvidence(change issuesapi.RecentChange) bool {
	return change.ChangeCategory == issuesapi.ChangeCategorySpecConfig || change.ChangeCategory == issuesapi.ChangeCategoryLifecycle
}

func (c *run) helmHistory() ([]issuesapi.RecentChange, error) {
	if c.access.HelmHistory == nil {
		return nil, nil
	}
	return c.access.HelmHistory(c.ctx, c.iss.Namespace, c.iss.Name, c.opts.Window)
}

// allowed checks the caller's list permission on a source before it is
// read, recording a denial or a failed check.
func (c *run) allowed(kind, group, namespace string) bool {
	if c.access.Check == nil {
		return true
	}
	switch c.access.Check(kind, group, namespace) {
	case Allowed:
		return true
	case Forbidden:
		c.addUnknown(issuesapi.CorrelationUnknownUserForbidden, group, kind)
	default:
		c.addUnknown(issuesapi.CorrelationUnknownPermissionCheckFailed, group, kind)
	}
	return false
}

// addUnknown records reason for kind; an empty kind records the reason
// without naming one.
func (c *run) addUnknown(reason issuesapi.CorrelationUnknownReason, group, kind string) {
	kinds := c.unknown[reason]
	k := issuesapi.UncheckedKind{Group: group, Kind: kind}
	if kind != "" && !slices.Contains(kinds, k) {
		kinds = append(kinds, k)
	}
	c.unknown[reason] = kinds
}

func (c *run) addHistoryUnknown(iss *issuesapi.Issue) {
	c.addUnknown(issuesapi.CorrelationUnknownHistoryIncomplete, iss.Group, iss.Kind)
}

// unknownPriority orders the reasons when several apply; the first is the
// one an answer carries.
var unknownPriority = []issuesapi.CorrelationUnknownReason{
	issuesapi.CorrelationUnknownReadError,
	issuesapi.CorrelationUnknownUserForbidden,
	issuesapi.CorrelationUnknownPermissionCheckFailed,
	issuesapi.CorrelationUnknownSourcesUnresolved,
	issuesapi.CorrelationUnknownKindNotCollected,
	issuesapi.CorrelationUnknownNamespaceNotCollected,
	issuesapi.CorrelationUnknownKindSyncPending,
	issuesapi.CorrelationUnknownHistoryIncomplete,
}

func (c *run) primaryUnknown() *issuesapi.CorrelationUnknown {
	for _, reason := range unknownPriority {
		kinds, ok := c.unknown[reason]
		if !ok {
			continue
		}
		kinds = slices.Clone(kinds)
		slices.SortFunc(kinds, func(a, b issuesapi.UncheckedKind) int {
			if a.Group != b.Group {
				return strings.Compare(a.Group, b.Group)
			}
			return strings.Compare(a.Kind, b.Kind)
		})
		if reason == issuesapi.CorrelationUnknownHistoryIncomplete {
			kinds = nil
		}
		return &issuesapi.CorrelationUnknown{Reason: reason, Kinds: kinds}
	}
	return nil
}

// selectChanges applies the caller's selection to the visible changes.
func (c *run) selectChanges(changes []issuesapi.RecentChange) ([]issuesapi.RecentChange, bool) {
	if c.opts.Selection == SelectAroundFirstSeen {
		return selectAroundFirstSeen(changes, c.iss)
	}
	if len(changes) > ChangeCap {
		return changes[:ChangeCap], true
	}
	return changes, false
}

// anchorKnown reports whether the shown changes are picked around first
// seen: the issue has a first-seen time, even when its onset is unknown,
// unless it is a grouped issue whose members' onsets are mixed, which the UI
// shows as one list.
func anchorKnown(iss issuesapi.Issue) bool {
	if iss.FirstSeen.IsZero() {
		return false
	}
	return iss.OnsetCoverage == nil || iss.OnsetCoverage.Known == 0 || iss.OnsetCoverage.Unknown == 0
}

// selectAroundFirstSeen keeps up to three changes at or before first seen,
// closest first, then up to two after it, newest first. Without a first-seen
// time, or for a grouped issue with mixed onsets, it keeps the three newest.
func selectAroundFirstSeen(changes []issuesapi.RecentChange, iss issuesapi.Issue) ([]issuesapi.RecentChange, bool) {
	sorted := slices.Clone(changes)
	sortChangesNewestFirst(sorted)
	if !anchorKnown(iss) {
		if len(sorted) > anchoredBefore {
			return sorted[:anchoredBefore], true
		}
		return sorted, false
	}
	boundary := iss.FirstSeen.Add(anchorSlop)
	var before, after []issuesapi.RecentChange
	for _, change := range sorted {
		if changeTime(change).After(boundary) {
			after = append(after, change)
		} else {
			before = append(before, change)
		}
	}
	more := len(before) > anchoredBefore || len(after) > anchoredAfter
	out := append(before[:min(len(before), anchoredBefore)], after[:min(len(after), anchoredAfter)]...)
	return out, more
}

// unanchoredLookback is the window for an issue with no first-seen time.
// With nothing to anchor on, it reaches back far enough to hold a bad deploy
// made hours before the issue was noticed.
const unanchoredLookback = 24 * time.Hour

// WindowAroundFirstSeen is the anchored window: from an hour before the issue
// was first seen until now. It anchors on first seen whenever the issue has
// one, even when the onset itself is unknown or a grouped issue's onsets are
// mixed, so the window doesn't slide past the changes before it. An issue
// with no first-seen time gets the last 24 hours instead.
func WindowAroundFirstSeen(iss issuesapi.Issue, now time.Time) Window {
	if iss.FirstSeen.IsZero() {
		return Window{From: now.Add(-unanchoredLookback), To: now}
	}
	anchor := now
	if iss.FirstSeen.Before(now) {
		anchor = iss.FirstSeen
	}
	return Window{From: anchor.Add(-meaningfulchanges.DefaultSince), To: now}
}

// WorkloadObjectFromCache returns the cached workload whose pod template
// names its sources, or nil.
func WorkloadObjectFromCache(kind, namespace, name string) any {
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
	case "rollout":
		if o, err := cache.GetDynamicWithGroup(context.Background(), "Rollout", namespace, name, "argoproj.io"); err == nil {
			return o
		}
	}
	return nil
}
