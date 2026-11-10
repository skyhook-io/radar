package mcp

import (
	"context"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// An admission rejection has two sides: the workload asking for pods and the
// policy refusing them (a ResourceQuota, a LimitRange, a webhook). Which one
// changed last decides the fix: a new or tightened policy is reverted or
// raised, a workload that changed into a standing policy is made to comply.
// The subject's own correlation covers only the workload side, so these
// helpers add the policy side and say which changed last.

// admissionBlockerMaxChanges bounds the policy-side refs in the shared
// correlationChangeCap, leaving room for the workload's own change.
const admissionBlockerMaxChanges = 2

// admissionOrderFetchLimit is the per-side fetch for admission issues: deep
// enough that the newest change survives significance ranking.
const admissionOrderFetchLimit = 10

// admissionOnsetSlop absorbs the gap between event timestamps (whole
// seconds, from the controller) and Radar's receipt time for the policy change.
const admissionOnsetSlop = 30 * time.Second

// admissionRecordLag is how far a policy add's recorded time may trail the
// object's creationTimestamp and still date the creation. An add recorded
// from an informer's (re)list can trail it by up to 30s (isSyncAdd); clock
// skew between Radar and the API server is the rest of the allowance.
const admissionRecordLag = 5 * time.Second

const (
	admissionBlockerRankReason    = "the object named by the admission rejection"
	admissionLimitRangeRankReason = "a LimitRange in the rejecting namespace (LimitRange rejections name none)"
)

type admissionBlockerEvidence struct {
	// unchanged states that the policy side has no recorded change in the
	// given window ("hour", "45 minutes").
	unchanged func(window string) string
	// changes are the visible spec and lifecycle changes, newest first.
	changes []issuesapi.RecentChange
	// saturated means a fetch hit its bound (correlated_changes_incomplete).
	saturated bool
	// complete means nothing was missed: the object was resolved, the fetch
	// was not saturated, and RBAC hid nothing. Only then may an empty list be
	// stated as "no recorded change".
	complete bool
}

// policyCreatedAt returns a cached ResourceQuota or LimitRange's
// creationTimestamp; ok=false when it isn't cached or isn't one of those kinds.
var policyCreatedAt = func(kind, namespace, name string) (time.Time, bool) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return time.Time{}, false
	}
	switch kind {
	case "ResourceQuota":
		if l := cache.ResourceQuotas(); l != nil {
			if o, err := l.ResourceQuotas(namespace).Get(name); err == nil {
				return o.CreationTimestamp.Time, true
			}
		}
	case "LimitRange":
		if l := cache.LimitRanges(); l != nil {
			if o, err := l.LimitRanges(namespace).Get(name); err == nil {
				return o.CreationTimestamp.Time, true
			}
		}
	}
	return time.Time{}, false
}

// creationTimeKnown reports whether a policy change's recorded time dates it:
// false for an add recorded well after the object was created, as one listed
// when its informer (re)synced is.
func creationTimeKnown(c issuesapi.RecentChange) bool {
	if c.ChangeType != "add" {
		return true
	}
	created, ok := policyCreatedAt(c.Kind, c.Namespace, c.Name)
	return !ok || changeTime(c).Sub(created) <= admissionRecordLag
}

func isAdmissionIssue(iss *issuesapi.Issue) bool {
	return iss.Category == issuesapi.CategoryQuotaExceeded || iss.Category == issuesapi.CategoryAdmissionWebhookBlocking
}

// admissionBlockerForIssue returns the policy-side evidence for an admission
// issue whose message names its blocking object, or nil.
func admissionBlockerForIssue(ctx context.Context, iss *issuesapi.Issue, window time.Duration) *admissionBlockerEvidence {
	if !isAdmissionIssue(iss) || iss.Namespace == "" {
		return nil
	}
	blocker, ok := k8s.ParseAdmissionBlocker(iss.Message)
	if !ok {
		return nil
	}
	ev := &admissionBlockerEvidence{complete: true}
	var changes []issuesapi.RecentChange
	// fetch reads one policy object's changes (every object of kind in the
	// namespace when name is empty). A kind the caller can't read is not
	// queried and leaves the policy side unknown, so no sentence rests on
	// history the caller may not see.
	fetch := func(kind, apiVersion, namespace, name string) bool {
		if pkgauth.UserFromContext(ctx) != nil && !k8s.ChangeReadAllowed(kind, apiVersion, namespace, mcpChangeAuthorizer(ctx)) {
			ev.complete = false
			return true
		}
		var res meaningfulchanges.RecentResult
		var err error
		if name == "" {
			res, err = meaningfulchanges.Recent(ctx, meaningfulchanges.Query{
				Namespaces: []string{namespace}, Kinds: []string{kind},
				Since: window, Limit: admissionOrderFetchLimit, FieldLimit: correlationFieldLimit,
				Viewer: recentChangeViewer(ctx), TrackedOnly: true,
			})
		} else {
			res, err = meaningfulchanges.RecentForResourceDetailed(ctx, kind, namespace, name, window, admissionOrderFetchLimit, correlationFieldLimit, recentChangeViewer(ctx))
		}
		if err != nil {
			log.Printf("[mcp] admission blocker correlation failed for %s %s/%s: %v", kind, namespace, name, err)
			return false
		}
		changes = append(changes, res.Changes...)
		ev.saturated = ev.saturated || res.FetchSaturated
		// Evicted history may have held a change, as may an overfull fetch.
		ev.complete = ev.complete && !res.FetchSaturated && !res.History.Incomplete
		return true
	}
	// Without an informer covering the namespace for the kind (no list
	// access for Radar itself), the timeline holds none of its changes, and
	// an empty answer means nothing.
	collected := func(kind string) bool {
		cache := k8s.GetResourceCache()
		if cache == nil {
			return false
		}
		if kind == "ResourceQuota" {
			return cache.ResourceQuotas() != nil && cache.KindCoversNamespace("resourcequotas", iss.Namespace)
		}
		return cache.LimitRanges() != nil && cache.KindCoversNamespace("limitranges", iss.Namespace)
	}
	switch {
	case blocker.Kind == "ResourceQuota":
		ev.complete = collected("ResourceQuota")
		ev.unchanged = func(window string) string {
			return "ResourceQuota " + blocker.Name + " has no recorded change in the last " + window
		}
		if !fetch("ResourceQuota", "v1", iss.Namespace, blocker.Name) {
			return nil
		}
	case blocker.Kind == "LimitRange":
		ev.complete = collected("LimitRange")
		ev.unchanged = func(window string) string {
			return "no LimitRange in namespace " + iss.Namespace + " has a recorded change in the last " + window
		}
		if !fetch("LimitRange", "v1", iss.Namespace, "") {
			return nil
		}
	case blocker.WebhookName != "":
		ev.unchanged = func(window string) string {
			return fmt.Sprintf("the configuration of webhook %q has no recorded change in the last %s", blocker.WebhookName, window)
		}
		configs := meaningfulchanges.WebhookConfigurationsDeclaring(blocker.WebhookName)
		if len(configs) == 0 {
			// Not cached, or no longer declared: whether it changed is unknown.
			ev.complete = false
		}
		for _, cfg := range configs {
			if !fetch(cfg.Kind, "admissionregistration.k8s.io/v1", "", cfg.Name) {
				return nil
			}
		}
	default:
		return nil
	}
	visible, hidden := applyCorrelationVisibilityFilters(ctx, changes)
	ev.complete = ev.complete && !hidden
	sortChangesNewestFirst(visible)
	rankReason := admissionBlockerRankReason
	if blocker.Kind == "LimitRange" {
		rankReason = admissionLimitRangeRankReason
	}
	for i := range visible {
		visible[i].RankReason = rankReason
	}
	ev.changes = visible
	return ev
}

// attachAdmissionBlocker sets the issue's correlated changes from both sides,
// states which side changed last, and withdraws a "started at resource
// creation" timing that the policy change contradicts. subject holds the
// workload side's visible changes; subjectComplete says an empty subject list
// is a true "no recorded change". absenceKnown says Radar has observed long
// enough for a side with no recorded change to count as unchanged. It returns
// false, leaving the issue untouched, when neither side has a visible change.
func attachAdmissionBlocker(iss *issuesapi.Issue, ev *admissionBlockerEvidence, subject []issuesapi.RecentChange, subjectComplete, absenceKnown bool, window time.Duration) bool {
	refs := append([]issuesapi.RecentChange{}, ev.changes[:min(len(ev.changes), admissionBlockerMaxChanges)]...)
	refs = append(refs, subject[:min(len(subject), correlationChangeCap-len(refs))]...)
	if len(refs) == 0 {
		return false
	}
	iss.CorrelatedChanges = refs

	sentence, policyChangedLast := admissionChangeOrder(iss, ev, subject, subjectComplete, absenceKnown, window)
	if sentence == "" {
		return true
	}
	if policyChangedLast && iss.IssueTiming == "started_at_resource_creation" && !iss.FirstSeen.IsZero() {
		// A policy created after the workload's last change cannot have
		// rejected it at creation; leave timing unknown rather than point at
		// the workload. Only a recorded creation counts (a later update, such
		// as a CA-bundle injection, doesn't hide it): after an update alone,
		// an older version of the policy may have blocked it from the start.
		if createdAt, ok := newestPolicyCreation(ev.changes); ok &&
			createdAt.After(newestWorkloadChange(iss, subject)) &&
			!iss.FirstSeen.Add(admissionOnsetSlop).Before(createdAt) {
			iss.IssueTiming, iss.IssueTimingBasis = "", ""
			iss.TimingSummary = issues.TimingSummary(*iss)
		}
	}
	if iss.TimingSummary != "" {
		iss.TimingSummary += " "
	}
	iss.TimingSummary += sentence
	return true
}

// admissionChangeOrder states which side of an admission rejection changed
// last, from recorded changes only. Empty when neither side has a recorded
// change, or when the side without one may have changes Radar did not see.
// When both sides have one, each record shows that side was being watched by
// then, so the order holds however young the store is; a side with none
// counts as unchanged only when absenceKnown.
func admissionChangeOrder(iss *issuesapi.Issue, ev *admissionBlockerEvidence, subject []issuesapi.RecentChange, subjectComplete, absenceKnown bool, windowDur time.Duration) (string, bool) {
	workload := iss.Kind + " " + iss.Name
	// The workload object only, as in newestWorkloadChange.
	var newestSubject *issuesapi.RecentChange
	for i := range subject {
		if subject[i].Kind != iss.Kind || subject[i].Name != iss.Name {
			continue
		}
		if newestSubject == nil || changeTime(subject[i]).After(changeTime(*newestSubject)) {
			newestSubject = &subject[i]
		}
	}
	window := windowText(windowDur)
	switch {
	case len(ev.changes) > 0 && newestSubject != nil:
		// Each verdict needs the other side complete: an unseen newer change
		// there would reverse it.
		policy := ev.changes[0]
		if changeTime(policy).Equal(changeTime(*newestSubject)) {
			// Change times are Radar's whole-second receipt times.
			return fmt.Sprintf("%s and %s: Radar recorded both in the same second, so it can't tell which changed last.", describeChange(policy), describeChange(*newestSubject)), false
		}
		if changeTime(policy).After(changeTime(*newestSubject)) {
			if !subjectComplete || !creationTimeKnown(policy) {
				return "", false
			}
			return fmt.Sprintf("%s, after %s: the %s changed last.", describeChange(policy), describeChange(*newestSubject), policy.Kind), true
		}
		if !ev.complete {
			return "", false
		}
		return fmt.Sprintf("%s, after %s: the workload changed last.", describeChange(*newestSubject), describeChange(policy)), false
	case len(ev.changes) > 0 && subjectComplete && absenceKnown && creationTimeKnown(ev.changes[0]):
		policy := ev.changes[0]
		return fmt.Sprintf("%s; %s has no recorded change in the last %s: the %s changed last.", describeChange(policy), workload, window, policy.Kind), true
	case newestSubject != nil && ev.complete && absenceKnown:
		return fmt.Sprintf("%s; %s: the workload changed last.", describeChange(*newestSubject), ev.unchanged(window)), false
	}
	return "", false
}

// newestWorkloadChange is the time of the workload object's newest change
// (zero when it has none). Order is judged against the object itself: a
// ConfigMap edit stays a correlated ref but changes nothing admission checks.
func newestWorkloadChange(iss *issuesapi.Issue, subject []issuesapi.RecentChange) time.Time {
	var newest time.Time
	for _, c := range subject {
		if c.Kind == iss.Kind && c.Name == iss.Name && changeTime(c).After(newest) {
			newest = changeTime(c)
		}
	}
	return newest
}

// newestPolicyCreation is the time of the newest recorded creation among the
// policy-side changes; ok=false when there is none or its recorded time
// doesn't date it.
func newestPolicyCreation(changes []issuesapi.RecentChange) (time.Time, bool) {
	var newest *issuesapi.RecentChange
	for i, c := range changes {
		if c.ChangeType == "add" && (newest == nil || changeTime(c).After(changeTime(*newest))) {
			newest = &changes[i]
		}
	}
	if newest == nil || !creationTimeKnown(*newest) {
		return time.Time{}, false
	}
	return changeTime(*newest), true
}

func describeChange(c issuesapi.RecentChange) string {
	verb := "changed"
	switch c.ChangeType {
	case "add":
		verb = "was created"
	case "update":
		verb = "was updated"
	case "delete":
		verb = "was deleted"
	}
	at := c.Timestamp
	if t := changeTime(c); !t.IsZero() {
		at = t.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%s %s %s at %s", c.Kind, c.Name, verb, at)
}

func changeTime(c issuesapi.RecentChange) time.Time {
	t, _ := time.Parse(time.RFC3339, c.Timestamp)
	return t
}

func sortChangesNewestFirst(changes []issuesapi.RecentChange) {
	slices.SortStableFunc(changes, func(a, b issuesapi.RecentChange) int {
		return changeTime(b).Compare(changeTime(a))
	})
}

func windowText(window time.Duration) string {
	if window >= time.Hour && window%time.Hour == 0 {
		hours := int(window / time.Hour)
		if hours == 1 {
			return "hour"
		}
		return fmt.Sprintf("%d hours", hours)
	}
	return fmt.Sprintf("%d minutes", int(window.Minutes()))
}
