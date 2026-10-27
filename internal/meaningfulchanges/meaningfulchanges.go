package meaningfulchanges

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/rollouts"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/readreason"
)

const (
	DefaultSince      = time.Hour
	DefaultLimit      = 20
	DefaultFieldLimit = 10
	IssueChangesLimit = 5
	// IssueChangesCandidateLimit leaves a small amount of room for the
	// issue-aware pass to distinguish several otherwise tied config edits
	// before the public response is capped.
	IssueChangesCandidateLimit = 10
	ResourceLimit              = 3
)

// Each candidate query reads at most this many of the newest matching rows;
// ranking and the output limit apply afterwards. A query that has more rows
// in the window marks the answer saturated. The bound decides which rows
// compete for the output, not how many are returned, so it does not depend on
// the output limit. A name filter reads one object's history, which a smaller
// bound covers.
const (
	FetchBound      = 1000
	NamedFetchBound = 100
)

const (
	ChangesReasonNoCriticalIssues                  = "no_critical_issues"
	ChangesReasonWithAllCreationTimeCriticalIssues = "recent_changes_with_all_critical_issues_at_creation"
)

const (
	filteredIssueSetGuidance      = "This response is severity-filtered, so Radar did not promote changes based on links to returned issues; omitted issue rows may explain them."
	truncatedIssueMembersGuidance = "Radar did not promote changes based on issue linkage because at least one returned grouped issue omitted members; an omitted member may explain a change."
	// FetchSaturatedGuidance distinguishes an incomplete candidate query from
	// a complete window whose lower-ranked results were omitted from output.
	FetchSaturatedGuidance = "A change query exceeded its fetch bound in this window, so relevant changes may be missing; absence from this feed is not evidence they did not occur."
)

// issueChangesGuidance returns response-level steering prose for reason tokens
// whose timing evidence could otherwise read as "the changes are irrelevant."
// The steer rides the response — not just the tool description — because the
// response is what the model attends to at decision time; the description may
// be skimmed once or never loaded. It is advice about how to treat the feed,
// never a causal claim about any specific change.
func issueChangesGuidance(reason string) string {
	if reason != ChangesReasonWithAllCreationTimeCriticalIssues {
		return ""
	}
	// State only what the timing classification establishes: each critical row
	// has been failing since its own resource was created. That is NOT an
	// ordering against the change feed — a resource created after a bad change
	// fails from creation precisely BECAUSE of that change.
	return "Every returned critical issue is classified as failing since its resource was created. " +
		"That does not clear the listed changes: a change can break a resource created after it, " +
		"and can cause application-layer symptoms that produce no issue row. " +
		"Review the changes before concluding the listed issues explain the reported problem."
}

type IssueChangePriorityOptions struct {
	Reason             string
	Limit              int
	UnfilteredIssueSet bool
	FetchSaturated     bool
}

func PrioritizeIssueChanges(changes []issuesapi.RecentChange, activeIssues []issuesapi.Issue, opts IssueChangePriorityOptions) ([]issuesapi.RecentChange, string, bool) {
	originalLen := len(changes)
	for i := range changes {
		changes[i].NotLinkedToReturnedIssues = false
		classifyApplicationConfigurationChange(&changes[i])
	}
	completeIssueLinks := issueLinksComplete(activeIssues)
	if opts.Reason == ChangesReasonWithAllCreationTimeCriticalIssues && opts.UnfilteredIssueSet && completeIssueLinks {
		markChangesNotLinkedToReturnedIssues(changes, activeIssues)
	}
	RankAndCap(&changes, opts.Limit)

	guidance := issueChangesGuidance(opts.Reason)
	var unlinked []string
	for _, change := range changes {
		if change.NotLinkedToReturnedIssues {
			unlinked = append(unlinked, changeRef(change))
		}
	}
	if len(unlinked) > 0 && len(unlinked) <= 2 {
		if len(unlinked) == 1 {
			guidance += " Radar could not link " + unlinked[0] +
				"'s application-configuration change to an issue in this response, so it appears first within `recent_changes`. " +
				"Verify it against runtime evidence; this is a lead, not evidence of cause."
		} else {
			guidance += " Radar could not link the application-configuration changes on " +
				strings.Join(unlinked, " and ") +
				" to issues in this response, so they appear first within `recent_changes`. " +
				"Verify them against runtime evidence; these are leads, not evidence of cause."
		}
	} else if len(unlinked) > 2 {
		guidance += fmt.Sprintf(" Radar could not link %d application-configuration changes to issues in "+
			"this response, so they appear first within `recent_changes`. "+
			"Verify them against runtime evidence; these are leads, not evidence of cause.", len(unlinked))
	}
	if !opts.UnfilteredIssueSet {
		guidance = appendGuidance(guidance, filteredIssueSetGuidance)
	}
	if opts.Reason == ChangesReasonWithAllCreationTimeCriticalIssues && opts.UnfilteredIssueSet && !completeIssueLinks {
		guidance = appendGuidance(guidance, truncatedIssueMembersGuidance)
	}
	if opts.FetchSaturated {
		guidance = appendGuidance(guidance, FetchSaturatedGuidance)
	}
	return changes, guidance, opts.Limit > 0 && originalLen > len(changes)
}

// ApplyIssueChanges fills an issues response's recent-changes fields from
// one Recent answer. A saturated answer, or one whose window starts before the
// history Radar holds, is reported even when no change survives, so an empty
// block never reads as a complete one.
func ApplyIssueChanges(resp *issuesapi.Response, result RecentResult, activeIssues []issuesapi.Issue, reason string, unfilteredIssueSet, readsAllNamespaces bool) {
	coverage := HistoryCoverageFor(result.History, readsAllNamespaces)
	if len(result.Changes) == 0 && !result.FetchSaturated && coverage == nil {
		return
	}
	changes := result.Changes
	var guidance string
	var recapped bool
	if len(changes) > 0 {
		changes, guidance, recapped = PrioritizeIssueChanges(changes, activeIssues, IssueChangePriorityOptions{
			Reason:             reason,
			Limit:              IssueChangesLimit,
			UnfilteredIssueSet: unfilteredIssueSet,
			FetchSaturated:     result.FetchSaturated,
		})
	} else if result.FetchSaturated {
		// The reason-specific guidance is about listed changes; with none
		// listed, only the completeness warning applies.
		guidance = FetchSaturatedGuidance
	}
	if coverage != nil {
		guidance = appendGuidance(guidance, HistoryCoverageGuidance(*coverage))
	}
	resp.RecentChanges = changes
	resp.RecentChangesReason = reason
	resp.RecentChangesGuidance = guidance
	resp.RecentChangesTruncated = result.OutputCapped || result.FetchSaturated || recapped
	resp.RecentChangesHistoryCoverage = coverage
}

func appendGuidance(current, addition string) string {
	if current == "" {
		return addition
	}
	return current + " " + addition
}

func issueLinksComplete(activeIssues []issuesapi.Issue) bool {
	for _, issue := range activeIssues {
		if issue.MembersTruncated {
			return false
		}
	}
	return true
}

// IssueSeveritySetComplete reports whether a parsed severity filter can omit
// any issue row from the current critical|warning public vocabulary.
func IssueSeveritySetComplete(severities []issuesapi.Severity) bool {
	if len(severities) == 0 {
		return true
	}
	var critical, warning bool
	for _, severity := range severities {
		switch severity {
		case issuesapi.SeverityCritical:
			critical = true
		case issuesapi.SeverityWarning:
			warning = true
		}
	}
	return critical && warning
}

func IssueChangesFetchLimit(reason string) int {
	if reason == ChangesReasonWithAllCreationTimeCriticalIssues {
		return IssueChangesCandidateLimit
	}
	return IssueChangesLimit
}

var (
	configKinds = []string{"ConfigMap"}
	specKinds   = []string{
		"Deployment", "StatefulSet", "DaemonSet", "Rollout", "Service", "Ingress",
		"HorizontalPodAutoscaler", "Application", "Kustomization", "HelmRelease",
		"GitRepository", "OCIRepository", "HelmRepository",
		"ResourceQuota", "LimitRange",
		"CronJob", "PodDisruptionBudget",
		"Gateway", "HTTPRoute", "GRPCRoute", "TCPRoute", "UDPRoute", "TLSRoute", "ReferenceGrant",
		// A policy that newly isolates running pods often has no traffic
		// symptom (established connections survive), so the object change is
		// the only evidence.
		"NetworkPolicy",
		// Cluster-scoped (namespace==""), so they surface in a cluster-wide
		// get_changes, not in a single-namespace query — correct, since one
		// webhook config gates admission across all namespaces.
		"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration", "GatewayClass",
	}
	// lifecycleOnlyKinds contribute deletes; Secret updates have a separate
	// reference gate in the default feed.
	lifecycleOnlyKinds = []string{"Secret"}
)

type Query struct {
	Namespaces []string
	Kinds      []string
	Name       string
	Since      time.Duration
	Limit      int
	FieldLimit int
	// Allow, when set, drops changes the caller may not read before ranking
	// and the output limit, so hidden rows cannot take the visible ones'
	// places.
	Allow func(issuesapi.RecentChange) bool

	// Viewer gates what rows say about other objects.
	Viewer Viewer

	// start is the window's lower bound, fixed once per answer so every
	// candidate query reads the same window.
	start time.Time
}

// Viewer gates the Pod and workload names a row carries beyond its own
// object. Nil fields mean auth is off.
type Viewer struct {
	// CanReadPods gates Pod-derived names and stale-env evidence.
	CanReadPods func(namespace string) bool
	// CanReadConsumer gates each workload named in consumed_by and
	// running_previous_value, checked as a change row of that kind.
	CanReadConsumer func(issuesapi.RecentChange) bool
}

func (v Viewer) canReadPods(namespace string) bool {
	return v.CanReadPods == nil || v.CanReadPods(namespace)
}

func (v Viewer) canNameConsumer(kind, group, namespace string) bool {
	if v.CanReadConsumer == nil {
		return true
	}
	// Owner references resolve to a group, not a version. The read check
	// keys on the group alone (k8s.ChangeReadAllowed reads
	// GroupFromAPIVersion), so any version names the same resource: an Argo
	// Rollout (argoproj.io/v1alpha1) checks as argoproj.io/rollouts.
	apiVersion := "v1"
	if group != "" {
		apiVersion = group + "/v1"
	}
	return v.CanReadConsumer(issuesapi.RecentChange{Kind: kind, APIVersion: apiVersion, Namespace: namespace})
}

type RecentResult struct {
	Changes        []issuesapi.RecentChange
	OutputCapped   bool
	FetchSaturated bool
	// History reports whether Radar's held history covers the window; see
	// HistoryCoverageSince.
	History issuesapi.HistoryCoverage
}

// Recent returns ranked changes plus two distinct truncation signals:
// OutputCapped (more qualifying changes existed than the requested limit) and
// FetchSaturated (a candidate query had more rows than its fetch bound, so the
// window may contain older events the fetch never saw). Negative claims ("no
// recent changes") must key on FetchSaturated and History, never on
// OutputCapped: output capping still read the full window and ranks
// spec/config changes above the churn that gets dropped.
func Recent(ctx context.Context, q Query) (RecentResult, error) {
	store := timeline.GetStore()
	if store == nil {
		return RecentResult{}, timeline.StoreMissingError()
	}
	q = normalizeQuery(q)
	bound := FetchBound
	if q.Name != "" {
		bound = NamedFetchBound
	}

	// Updates and lifecycle events (add/delete) are fetched in separate
	// queries, so a burst of creates can't push an edit out of the bound and
	// update churn can't push out a delete. Creates and deletes come only from
	// the lifecycle query.
	updateKinds := [][]string{q.Kinds}
	lifecycleKinds := q.Kinds
	if len(q.Kinds) == 0 && q.Name == "" {
		updateKinds = [][]string{configKinds, specKinds, {"Secret"}}
		lifecycleKinds = append(append(append([]string{}, configKinds...), specKinds...), lifecycleOnlyKinds...)
	}
	var events []timeline.TimelineEvent
	saturated := false
	for _, kinds := range updateKinds {
		got, full, err := queryCandidates(ctx, store, q, kinds, updateEventTypes, bound)
		if err != nil {
			return RecentResult{}, err
		}
		events = append(events, got...)
		saturated = saturated || full
	}
	got, full, err := queryCandidates(ctx, store, q, lifecycleKinds, lifecycleEventTypes, bound)
	if err != nil {
		return RecentResult{}, err
	}
	events = append(events, got...)
	saturated = saturated || full

	changes, capped := rankedChanges(ctx, coalesceRecreatePairs(dedupeEvents(events)), q)
	return RecentResult{
		Changes:        changes,
		OutputCapped:   capped,
		FetchSaturated: saturated,
		History:        historyCoverage(q.start, q.Namespaces),
	}, nil
}

// historyCoverage is HistoryCoverageSince for an answer scoped to
// namespaces. A namespace created after the boundary holds its whole history,
// since nothing in it can predate it; an answer scoped to such namespaces is
// complete.
func historyCoverage(windowStart time.Time, namespaces []string) issuesapi.HistoryCoverage {
	coverage := HistoryCoverageSince(windowStart)
	if coverage.Incomplete && namespacesCreatedAfter(namespaces, coverage.CompleteSince) {
		return issuesapi.HistoryCoverage{}
	}
	return coverage
}

func namespacesCreatedAfter(namespaces []string, t time.Time) bool {
	cache := k8s.GetResourceCache()
	if len(namespaces) == 0 || cache == nil || cache.Namespaces() == nil {
		return false
	}
	for _, name := range namespaces {
		ns, err := cache.Namespaces().Get(name)
		if err != nil || !ns.CreationTimestamp.After(t) {
			return false
		}
	}
	return true
}

// HistoryCoverageFor returns coverage as a response carries it: nil when the
// history covers the window. CompleteSince is store-wide, so it can date
// events the caller may not read; it is kept only for callers who can read
// every namespace.
func HistoryCoverageFor(coverage issuesapi.HistoryCoverage, readsAllNamespaces bool) *issuesapi.HistoryCoverage {
	if !coverage.Incomplete {
		return nil
	}
	if !readsAllNamespaces {
		coverage.CompleteSince = time.Time{}
	}
	return &coverage
}

// HistoryCoverageGuidance is the sentence an answer carries when its window
// starts before the history Radar holds.
func HistoryCoverageGuidance(coverage issuesapi.HistoryCoverage) string {
	const rest = "; changes before then may be missing, so an empty or short result does not establish that nothing changed. " +
		"For older changes to one object, check it directly (get_resource, its ReplicaSet revisions)."
	if coverage.CompleteSince.IsZero() {
		return "Radar's change history is incomplete for part of this window" + rest
	}
	return "Radar's change history is complete only from " + coverage.CompleteSince.Format(time.RFC3339) + rest
}

// HistoryCoverageSince reports whether the history Radar holds covers
// everything from windowStart on. It is incomplete when this process started
// observing after windowStart, or when the store has deleted events newer than
// windowStart; CompleteSince is then the later of those two times, rounded up
// to the second. Rows Radar never saw for other reasons (a missed watch event)
// are not covered by this.
func HistoryCoverageSince(windowStart time.Time) issuesapi.HistoryCoverage {
	var coverage issuesapi.HistoryCoverage
	if started := timeline.ObservationStart(); started.After(windowStart) {
		coverage.Reasons = append(coverage.Reasons, issuesapi.HistoryObservationStartedLate)
		coverage.CompleteSince = started
	}
	if evicted := timeline.HistoryEvictedThrough(); evicted.After(windowStart) {
		coverage.Reasons = append(coverage.Reasons, issuesapi.HistoryEventsEvicted)
		if evicted.After(coverage.CompleteSince) {
			coverage.CompleteSince = evicted
		}
	}
	if len(coverage.Reasons) == 0 {
		return issuesapi.HistoryCoverage{}
	}
	coverage.Incomplete = true
	if rounded := coverage.CompleteSince.Truncate(time.Second); !rounded.Equal(coverage.CompleteSince) {
		coverage.CompleteSince = rounded.Add(time.Second)
	}
	coverage.CompleteSince = coverage.CompleteSince.UTC()
	return coverage
}

// RecentForResource returns the subject's ranked changes plus a saturation
// flag: true when a candidate fetch hit its cap and the window may contain
// changes the query never saw. Callers asserting "no recent changes" must
// treat saturation as unknown, never as evidence of absence.
func RecentForResource(ctx context.Context, kind, namespace, name string, since time.Duration, limit, fieldLimit int, viewer Viewer) ([]issuesapi.RecentChange, bool, error) {
	result, err := RecentForResourceDetailed(ctx, kind, namespace, name, since, limit, fieldLimit, viewer)
	return result.Changes, result.FetchSaturated, err
}

// RecentForResourceDetailed preserves both completeness signals for consumers
// that display source coverage. The historical wrapper above intentionally
// exposes only fetch saturation because issue-correlation uses that bool for
// negative-claim gating.
func RecentForResourceDetailed(ctx context.Context, kind, namespace, name string, since time.Duration, limit, fieldLimit int, viewer Viewer) (RecentResult, error) {
	return Recent(ctx, Query{
		Namespaces: []string{namespace},
		Kinds:      []string{canonicalKind(kind)},
		Name:       name,
		Since:      since,
		Limit:      limit,
		FieldLimit: fieldLimit,
		Viewer:     viewer,
	})
}

// RecentForWorkloadAndConfigMaps includes directly referenced ConfigMaps and Secrets.
func RecentForWorkloadAndConfigMaps(ctx context.Context, obj any, kind, namespace, name string, since time.Duration, limit, fieldLimit int, viewer Viewer) ([]issuesapi.RecentChange, bool, error) {
	result, _, err := RecentForWorkloadAndConfigMapsAuthorizedDetailed(
		ctx, obj, kind, namespace, name, since, limit, fieldLimit, nil, viewer,
	)
	return result.Changes, result.FetchSaturated, err
}

// RecentForWorkloadAndConfigMapsAuthorizedDetailed is the authorization-aware
// form used by cached-data surfaces, including directly referenced Secrets.
// includeSource is evaluated for every resource whose timeline would be queried, before the query runs. A false
// result skips that source and makes coverageLimited true even when the source
// has no matching rows; callers can therefore avoid both leaking hidden-row
// counts and presenting a filtered empty result as complete coverage.
//
// A nil includeSource preserves the historical unfiltered behavior.
func RecentForWorkloadAndConfigMapsAuthorizedDetailed(
	ctx context.Context,
	obj any,
	kind, namespace, name string,
	since time.Duration,
	limit, fieldLimit int,
	includeSource func(kind, name string) bool,
	viewer Viewer,
) (result RecentResult, coverageLimited bool, err error) {
	store := timeline.GetStore()
	if store == nil {
		return RecentResult{}, false, timeline.StoreMissingError()
	}
	q := normalizeQuery(Query{Namespaces: []string{namespace}, Since: since, Limit: limit, FieldLimit: fieldLimit, Viewer: viewer})
	var events []timeline.TimelineEvent
	fetch := func(resourceKind, resourceName string) error {
		canonicalResourceKind := canonicalKind(resourceKind)
		if includeSource != nil && !includeSource(canonicalResourceKind, resourceName) {
			coverageLimited = true
			return nil
		}
		sourceQuery := q
		sourceQuery.Kinds = []string{canonicalResourceKind}
		sourceQuery.Name = resourceName
		for _, eventTypes := range [][]timeline.EventType{updateEventTypes, lifecycleEventTypes} {
			got, saturated, err := queryCandidates(ctx, store, sourceQuery, sourceQuery.Kinds, eventTypes, NamedFetchBound)
			if err != nil {
				return err
			}
			events = append(events, got...)
			result.FetchSaturated = result.FetchSaturated || saturated
		}
		return nil
	}

	if isWorkloadKind(kind) {
		if err := fetch(kind, name); err != nil {
			return RecentResult{}, coverageLimited, err
		}
	}
	for _, ref := range DirectConfigReferences(obj) {
		if err := fetch(ref.Kind, ref.Name); err != nil {
			return RecentResult{}, coverageLimited, err
		}
	}
	result.History = historyCoverage(q.start, q.Namespaces)
	// These are explicitly scoped sources: retain every update, as for a name query.
	q.Kinds = []string{canonicalKind(kind)}
	result.Changes, result.OutputCapped = rankedChanges(ctx, coalesceRecreatePairs(dedupeEvents(events)), q)
	return result, coverageLimited, nil
}

func ShouldAttachIssueChanges(issues []issuesapi.Issue) bool {
	return IssueChangesReason(issues) != ""
}

func IssueChangesQueryEligible(kindFilter, celFilter, severityFilter string) bool {
	if strings.TrimSpace(kindFilter) != "" || strings.TrimSpace(celFilter) != "" {
		return false
	}
	severityFilter = strings.TrimSpace(severityFilter)
	if severityFilter == "" {
		return true
	}
	for _, part := range strings.Split(severityFilter, ",") {
		if strings.ToLower(strings.TrimSpace(part)) == "critical" {
			return true
		}
	}
	return false
}

func IssueChangesReason(issues []issuesapi.Issue) string {
	criticalCount := 0
	for _, issue := range issues {
		if issue.Severity != issuesapi.SeverityCritical {
			continue
		}
		criticalCount++
		if issue.IssueTiming != "started_at_resource_creation" {
			return ""
		}
	}
	if criticalCount == 0 {
		return ChangesReasonNoCriticalIssues
	}
	return ChangesReasonWithAllCreationTimeCriticalIssues
}

func ConfigMapKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "configmap", "configmaps", "cm":
		return true
	default:
		return false
	}
}

func WorkloadKind(kind string) bool { return isWorkloadKind(kind) }

// DirectConfigReferences returns env and native volume references, excluding
// image-pull and volume-driver credentials.
func DirectConfigReferences(obj any) []configrefs.Ref {
	spec, ok := podSpecForObject(obj)
	if !ok {
		return nil
	}
	seen := map[configrefs.Ref]bool{}
	configrefs.VisitPodSpecReferences("", spec, func(ref configrefs.PodSpecReference) {
		if ref.Kind != "Secret" && ref.Kind != "ConfigMap" {
			return
		}
		if strings.HasPrefix(ref.Path, "volumes[].") &&
			!strings.HasPrefix(ref.Path, "volumes[].secret.") &&
			!strings.HasPrefix(ref.Path, "volumes[].configMap.") &&
			!strings.HasPrefix(ref.Path, "volumes[].projected.sources[].secret.") &&
			!strings.HasPrefix(ref.Path, "volumes[].projected.sources[].configMap.") {
			return
		}
		if strings.HasPrefix(ref.Path, "imagePullSecrets") {
			return
		}
		seen[ref.Ref] = true
	})
	out := make([]configrefs.Ref, 0, len(seen))
	for ref := range seen {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func normalizeQuery(q Query) Query {
	if q.Since <= 0 {
		q.Since = DefaultSince
	}
	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}
	if q.Limit > 50 {
		q.Limit = 50
	}
	if q.FieldLimit <= 0 {
		q.FieldLimit = DefaultFieldLimit
	}
	if q.FieldLimit > 50 {
		q.FieldLimit = 50
	}
	for i, kind := range q.Kinds {
		q.Kinds[i] = canonicalKind(kind)
	}
	if q.start.IsZero() {
		q.start = time.Now().Add(-q.Since)
	}
	return q
}

var (
	updateEventTypes    = []timeline.EventType{timeline.EventTypeUpdate}
	lifecycleEventTypes = []timeline.EventType{timeline.EventTypeAdd, timeline.EventTypeDelete}
)

// queryCandidates reads up to bound of the newest matching rows and reports
// whether the window held more: it asks for one extra row, which it drops.
// The returned events are group-filtered; when all kinds share one known
// group, the store applies that group before its limit, and heterogeneous
// queries keep the row-level guard.
func queryCandidates(ctx context.Context, store timeline.EventStore, q Query, kinds []string, eventTypes []timeline.EventType, bound int) ([]timeline.TimelineEvent, bool, error) {
	queryKinds := compactKinds(kinds)
	// A Node's owner reference (Karpenter's NodeClaim) doesn't make it a
	// derived object the way a Pod's or ReplicaSet's does, so owned Nodes stay.
	ownedNodes := slices.Contains(queryKinds, "Node")
	opts := timeline.QueryOptions{
		Namespaces: q.Namespaces,
		Kinds:      queryKinds,
		Names:      compactNames(q.Name),
		APIGroups:  commonTrackedAPIGroups(queryKinds),
		Since:      q.start,
		Sources:    []timeline.EventSource{timeline.SourceInformer},
		EventTypes: eventTypes,
		// Changes are root-cause evidence for the CURRENT cluster — the
		// persistent store retains other contexts' events across switches,
		// and serving those here hands agents phantom changes.
		ClusterContext:   k8s.ActiveClusterContext(),
		Limit:            bound + 1,
		IncludeManaged:   ownedNodes,
		IncludeK8sEvents: false,
	}
	events, err := store.Query(ctx, opts)
	if err != nil {
		return nil, false, err
	}
	if ownedNodes {
		events = slices.DeleteFunc(events, func(e timeline.TimelineEvent) bool { return e.Kind != "Node" && e.IsManaged() })
	}
	saturated := len(events) > bound
	if saturated {
		events = events[:bound]
	}
	return filterTrackedGroupEvents(events), saturated, nil
}

// commonTrackedAPIGroups returns a store-level filter only when every queried
// kind is tracked in the same API group. A flat group allow-list is unsafe for
// heterogeneous kinds because it cannot express the kind/group pairing (for
// example, core Service alongside apps Deployment).
func commonTrackedAPIGroups(kinds []string) []string {
	if len(kinds) == 0 {
		return nil
	}
	var common string
	for i, kind := range kinds {
		group, ok := trackedKindGroups[canonicalKind(kind)]
		if canonicalKind(kind) == "Secret" {
			group, ok = "", true
		}
		if !ok {
			return nil
		}
		if i == 0 {
			common = group
			continue
		}
		if group != common {
			return nil
		}
	}
	return []string{common}
}

// filterTrackedGroupEvents drops candidate events recorded from a different
// API group than the one the feed tracks for that kind — kind strings are
// queried by name, so without this a Knative Service event would enter a
// core Service's candidate set (and vice versa). Events with no recorded
// apiVersion are kept: unknown, not mismatched.
func filterTrackedGroupEvents(events []timeline.TimelineEvent) []timeline.TimelineEvent {
	out := events[:0]
	for _, e := range events {
		if eventGroupMatchesTracked(e.Kind, e.APIVersion) {
			out = append(out, e)
		}
	}
	return out
}

func eventGroupMatchesTracked(kind, apiVersion string) bool {
	if apiVersion == "" {
		return true // emitter did not record it — unknown, not mismatched
	}
	expected, ok := trackedKindGroups[canonicalKind(kind)]
	if canonicalKind(kind) == "Secret" {
		expected, ok = "", true
	}
	if !ok {
		return true
	}
	return resourceid.GroupFromAPIVersion(apiVersion) == expected
}

func rankedChanges(ctx context.Context, events []timeline.TimelineEvent, q Query) ([]issuesapi.RecentChange, bool) {
	out := make([]issuesapi.RecentChange, 0, len(events))
	for _, e := range events {
		if q.Name != "" && e.Name != q.Name {
			continue
		}
		change := fromEvent(e, q.FieldLimit)
		if change.ChangeCategory == "" {
			continue
		}
		if q.Allow != nil && !q.Allow(change) {
			continue
		}
		out = append(out, change)
	}
	index, pods := annotateConfigConsumers(ctx, out, q)
	if len(q.Kinds) == 0 && q.Name == "" {
		visible := out[:0]
		for _, change := range out {
			if change.Kind == "Secret" && change.ChangeType == "update" && !index[resourceKey(change.Kind, change.Namespace, change.Name)].referenced {
				continue
			}
			visible = append(visible, change)
		}
		out = visible
	}
	annotateRunningPreviousValue(ctx, out, pods, q.Viewer)
	if len(q.Kinds) == 0 && q.Name == "" {
		out = collapseSecretUpdates(out)
	}
	capped := len(out) > q.Limit
	RankAndCap(&out, q.Limit)
	return out, capped
}

const maxConsumersPerEntry = 5

type configConsumers struct {
	referenced bool
	names      []string
	// withheld holds the names the caller may not read; only their count
	// leaves this package.
	withheld []string
}

func annotateConfigConsumers(ctx context.Context, changes []issuesapi.RecentChange, q Query) (map[string]configConsumers, map[string][]*corev1.Pod) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil
	}
	namespaces := map[string]bool{}
	for _, change := range changes {
		if (change.Kind == "ConfigMap" || change.Kind == "Secret") && change.Namespace != "" {
			namespaces[change.Namespace] = true
		}
	}
	index, pods := configConsumerIndex(ctx, cache, namespaces, q)
	for i := range changes {
		entry := index[resourceKey(changes[i].Kind, changes[i].Namespace, changes[i].Name)]
		changes[i].ConsumedBy = entry.names
		changes[i].ConsumedByWithheld = withheldCount(len(entry.withheld))
	}
	return index, pods
}

func configConsumerIndex(ctx context.Context, cache *k8s.ResourceCache, namespaces map[string]bool, q Query) (map[string]configConsumers, map[string][]*corev1.Pod) {
	index := map[string]configConsumers{}
	pods := map[string][]*corev1.Pod{}
	nameable := map[string]bool{}
	canName := func(kind, group, namespace string) bool {
		key := group + "/" + kind + "/" + namespace
		allowed, checked := nameable[key]
		if !checked {
			allowed = q.Viewer.canNameConsumer(kind, group, namespace)
			nameable[key] = allowed
		}
		return allowed
	}
	add := func(namespace, kind, name string, obj any, showName bool) {
		for _, ref := range DirectConfigReferences(obj) {
			key := resourceKey(ref.Kind, namespace, ref.Name)
			entry := index[key]
			entry.referenced = true
			if showName {
				entry.names = append(entry.names, kind+"/"+name)
			} else {
				entry.withheld = append(entry.withheld, kind+"/"+name)
			}
			index[key] = entry
		}
	}
	if lister := cache.Deployments(); lister != nil {
		for namespace := range namespaces {
			if items, err := lister.Deployments(namespace).List(labels.Everything()); err == nil {
				for _, item := range items {
					add(namespace, "Deployment", item.Name, item, canName("Deployment", "apps", namespace))
				}
			}
		}
	}
	if lister := cache.StatefulSets(); lister != nil {
		for namespace := range namespaces {
			if items, err := lister.StatefulSets(namespace).List(labels.Everything()); err == nil {
				for _, item := range items {
					add(namespace, "StatefulSet", item.Name, item, canName("StatefulSet", "apps", namespace))
				}
			}
		}
	}
	if lister := cache.DaemonSets(); lister != nil {
		for namespace := range namespaces {
			if items, err := lister.DaemonSets(namespace).List(labels.Everything()); err == nil {
				for _, item := range items {
					add(namespace, "DaemonSet", item.Name, item, canName("DaemonSet", "apps", namespace))
				}
			}
		}
	}
	if lister := cache.Jobs(); lister != nil {
		for namespace := range namespaces {
			if items, err := lister.Jobs(namespace).List(labels.Everything()); err == nil {
				for _, item := range items {
					add(namespace, "Job", item.Name, item, canName("Job", "batch", namespace))
				}
			}
		}
	}
	if lister := cache.CronJobs(); lister != nil {
		for namespace := range namespaces {
			if items, err := lister.CronJobs(namespace).List(labels.Everything()); err == nil {
				for _, item := range items {
					add(namespace, "CronJob", item.Name, item, canName("CronJob", "batch", namespace))
				}
			}
		}
	}
	// Rollouts are indexed through their current Pods below, which resolve to
	// the Rollout owner: listing the dynamic kind here could start an informer
	// or probe access on every feed read.
	for namespace := range namespaces {
		if cache.Pods() == nil {
			continue
		}
		items, err := cache.Pods().Pods(namespace).List(labels.Everything())
		if err != nil {
			continue
		}
		readable := q.Viewer.canReadPods(namespace)
		for _, pod := range items {
			kind, group, name := "Pod", "", pod.Name
			if owner, ok := k8s.PodWorkload(cache, pod); ok {
				kind, group, name = owner.Kind, owner.Group, owner.Name
			}
			add(namespace, kind, name, pod, readable && canName(kind, group, namespace))
		}
		if readable {
			pods[namespace] = items
		}
	}
	for key, entry := range index {
		sort.Strings(entry.names)
		entry.names = slices.Compact(entry.names)
		entry.withheld = withheldNames(entry.withheld, entry.names)
		if len(entry.names) > maxConsumersPerEntry {
			entry.names = entry.names[:maxConsumersPerEntry]
		}
		index[key] = entry
	}
	return index, pods
}

// withheldNames returns the distinct hidden names that are not also listed:
// a workload named through its readable template stays named even when its
// Pods are not readable.
func withheldNames(hidden, shown []string) []string {
	var out []string
	for _, name := range hidden {
		if !slices.Contains(shown, name) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

func withheldCount(n int) *issuesapi.Withheld {
	if n == 0 {
		return nil
	}
	return &issuesapi.Withheld{Count: n, Reason: readreason.UserForbidden}
}

func markChangesNotLinkedToReturnedIssues(changes []issuesapi.RecentChange, activeIssues []issuesapi.Issue) {
	explained := map[string]bool{}
	addRef := func(ref issuesapi.Ref) {
		if ref.Kind != "" && ref.Name != "" {
			explained[resourceKey(ref.Kind, ref.Namespace, ref.Name)] = true
		}
	}
	for _, issue := range activeIssues {
		addRef(issuesapi.Ref{Kind: issue.Kind, Namespace: issue.Namespace, Name: issue.Name})
		addRef(issue.Owner)
		for _, member := range issue.Members {
			addRef(member)
		}
	}
	for i := range changes {
		if !changes[i].ApplicationConfigurationChange {
			continue
		}
		isExplained := explained[resourceKey(changes[i].Kind, changes[i].Namespace, changes[i].Name)]
		if changes[i].Kind == "ConfigMap" || changes[i].Kind == "Secret" {
			// consumed_by is capped and filtered by access. At the cap, or with
			// any consumer withheld, an omitted consumer may carry the issue,
			// so absence is not strong enough to promote this entry.
			if len(changes[i].ConsumedBy) >= maxConsumersPerEntry || changes[i].ConsumedByWithheld != nil {
				continue
			}
			for _, consumer := range changes[i].ConsumedBy {
				kind, name, ok := strings.Cut(consumer, "/")
				if ok && explained[resourceKey(kind, changes[i].Namespace, name)] {
					isExplained = true
					break
				}
			}
		}
		if !isExplained {
			changes[i].NotLinkedToReturnedIssues = true
			if changes[i].Kind == "Secret" {
				continue
			}
			if changes[i].ChangeType == string(timeline.EventTypeAdd) {
				changes[i].RankReason = "recreated with application configuration changes not linked to any returned issue"
			} else {
				changes[i].RankReason = "application configuration change not linked to any returned issue"
			}
		}
	}
}

func resourceKey(kind, namespace, name string) string {
	// Omitting API group is conservative here: a collision can only enlarge
	// the explained set and suppress a lead, never create a false lead.
	return strings.ToLower(strings.TrimSpace(kind)) + "\x00" + namespace + "\x00" + name
}

func changeRef(change issuesapi.RecentChange) string {
	if change.Namespace == "" {
		return change.Kind + "/" + change.Name
	}
	return change.Kind + "/" + change.Namespace + "/" + change.Name
}

// TrackedKind reports whether the change feed tracks this kind's updates —
// the gate for emitting per-issue "no recent changes" claims: asserting "no
// changes" for a kind whose changes are never recorded would be a false
// statement, not evidence.
func TrackedKind(kind string) bool {
	kind = canonicalKind(kind)
	return isConfigKind(kind) || isSpecKind(kind)
}

// trackedKindGroups maps each tracked (canonical) kind to the API group the
// feed actually records it from. Kind strings collide across groups — a
// Knative Service (serving.knative.dev) is not the core Service whose
// changes the feed tracks.
// NOTE: this map must cover exactly TrackedKind's set (configKinds +
// specKinds). Secret issues stay ineligible: API-only consumers are outside
// the reference-gated default feed, so absence cannot establish no changes.
var trackedKindGroups = map[string]string{
	"ConfigMap": "", "Service": "", "ResourceQuota": "", "LimitRange": "",
	"Deployment": "apps", "StatefulSet": "apps", "DaemonSet": "apps",
	"Rollout":                        "argoproj.io",
	"Ingress":                        "networking.k8s.io",
	"NetworkPolicy":                  "networking.k8s.io",
	"HorizontalPodAutoscaler":        "autoscaling",
	"Application":                    "argoproj.io",
	"Kustomization":                  "kustomize.toolkit.fluxcd.io",
	"HelmRelease":                    "helm.toolkit.fluxcd.io",
	"GitRepository":                  "source.toolkit.fluxcd.io",
	"OCIRepository":                  "source.toolkit.fluxcd.io",
	"HelmRepository":                 "source.toolkit.fluxcd.io",
	"MutatingWebhookConfiguration":   "admissionregistration.k8s.io",
	"ValidatingWebhookConfiguration": "admissionregistration.k8s.io",
	"CronJob":                        "batch",
	"PodDisruptionBudget":            "policy",
	"Gateway":                        "gateway.networking.k8s.io",
	"GatewayClass":                   "gateway.networking.k8s.io",
	"HTTPRoute":                      "gateway.networking.k8s.io",
	"GRPCRoute":                      "gateway.networking.k8s.io",
	"TCPRoute":                       "gateway.networking.k8s.io",
	"UDPRoute":                       "gateway.networking.k8s.io",
	"TLSRoute":                       "gateway.networking.k8s.io",
	"ReferenceGrant":                 "gateway.networking.k8s.io",
}

// TrackedKindForGroup is TrackedKind with kind-collision protection: when
// the caller KNOWS the subject's API group and it differs from the group the
// feed records for that kind, the subject is NOT tracked — correlating a
// Knative Service against the same-named core Service's changes would
// attach another resource's history to the issue. An empty group is
// permissive (unknown ⇒ current behavior).
func TrackedKindForGroup(kind, group string) bool {
	kind = canonicalKind(kind)
	expected, ok := trackedKindGroups[kind]
	if !ok {
		return false
	}
	return group == "" || group == expected
}

func RankAndCap(changes *[]issuesapi.RecentChange, limit int) {
	if changes == nil {
		return
	}
	for i := range *changes {
		classifyApplicationConfigurationChange(&(*changes)[i])
	}
	sort.SliceStable(*changes, func(i, j int) bool {
		a, b := (*changes)[i], (*changes)[j]
		if score(a) != score(b) {
			return score(a) > score(b)
		}
		aStaleSecret := a.Kind == "Secret" && a.RunningPreviousValue != nil
		bStaleSecret := b.Kind == "Secret" && b.RunningPreviousValue != nil
		if score(a) == 105 && aStaleSecret != bStaleSecret {
			return aStaleSecret
		}
		at, _ := time.Parse(time.RFC3339, a.Timestamp)
		bt, _ := time.Parse(time.RFC3339, b.Timestamp)
		if !at.Equal(bt) {
			return at.After(bt)
		}
		return changeKey(a) < changeKey(b)
	})
	if limit > 0 && len(*changes) > limit {
		*changes = (*changes)[:limit]
	}
}

func fromEvent(e timeline.TimelineEvent, fieldLimit int) issuesapi.RecentChange {
	category, reason := classify(e)
	if category == "" {
		return issuesapi.RecentChange{}
	}
	change := issuesapi.RecentChange{
		Kind:           e.Kind,
		APIVersion:     e.APIVersion,
		Namespace:      e.Namespace,
		Name:           e.Name,
		ChangeType:     string(e.EventType),
		Summary:        eventSummary(e),
		Timestamp:      e.Timestamp.Format(time.RFC3339),
		ChangeCategory: category,
		RankReason:     reason,
	}
	if e.Diff != nil {
		if hasApplicationConfigField(e.Kind, e.Diff.Fields) {
			change.ApplicationConfigurationChange = true
		}
		fields := e.Diff.Fields
		if fieldLimit > 0 && len(fields) > fieldLimit {
			fields = append([]timeline.FieldChange(nil), fields[:fieldLimit]...)
			// Reserve the final display slot for evidence supporting the
			// ranking hint when the ordinary field cap would hide it.
			if !hasApplicationConfigField(e.Kind, fields) &&
				change.ApplicationConfigurationChange {
				for _, field := range e.Diff.Fields[fieldLimit:] {
					if isApplicationConfigPath(e.Kind, field.Path) {
						fields[len(fields)-1] = field
						break
					}
				}
			}
			// A display cap must not make a multi-field change look like a
			// content-free generation bump.
			if isGenerationOnlyTimelineFields(fields) &&
				!isGenerationOnlyTimelineFields(e.Diff.Fields) {
				for _, field := range e.Diff.Fields[fieldLimit:] {
					if !isGenerationPath(field.Path) {
						fields[len(fields)-1] = field
						break
					}
				}
			}
		}
		for _, f := range fields {
			change.Fields = append(change.Fields, issuesapi.ChangeField{
				Path:     f.Path,
				OldValue: f.OldValue,
				NewValue: f.NewValue,
			})
		}
	}
	return change
}

func classify(e timeline.TimelineEvent) (issuesapi.ChangeCategory, string) {
	if e.Source != timeline.SourceInformer {
		return "", ""
	}
	if e.EventType == timeline.EventTypeAdd || e.EventType == timeline.EventTypeDelete {
		if isLifecycleOnlyKind(e.Kind) {
			if e.EventType == timeline.EventTypeDelete {
				return issuesapi.ChangeCategoryLifecycle, "secret deleted (name only)"
			}
			return "", ""
		}
		if isConfigKind(e.Kind) || isSpecKind(e.Kind) {
			// A recreate-join add carries the diff against the object it
			// replaced — that's a desired-state change, not mere lifecycle.
			if e.EventType == timeline.EventTypeAdd && e.Reason == timeline.ReasonRecreated && e.Diff != nil && len(e.Diff.Fields) > 0 {
				return issuesapi.ChangeCategorySpecConfig, "recreated with desired-state or configuration changes"
			}
			return issuesapi.ChangeCategoryLifecycle, lifecycleRankReason
		}
		return "", ""
	}
	if e.Diff == nil || len(e.Diff.Fields) == 0 {
		return "", ""
	}
	if hasSpecConfigField(e) {
		return issuesapi.ChangeCategorySpecConfig, "desired-state or configuration field changed"
	}
	if hasRuntimeStatusField(e) {
		return issuesapi.ChangeCategoryRuntimeStatus, "status field changed"
	}
	return "", ""
}

func hasSpecConfigField(e timeline.TimelineEvent) bool {
	// ConfigMap data and admission webhook configs hold their configuration at
	// the top level (data / webhooks[]), not under spec. — the path-prefix
	// heuristic below would drop their (curated, status-free) diffs entirely.
	if isConfigKind(e.Kind) || isWebhookConfigKind(e.Kind) {
		return true
	}
	for _, f := range e.Diff.Fields {
		p := strings.ToLower(f.Path)
		if e.Kind == "Node" && (strings.HasPrefix(p, "metadata.labels") || strings.HasPrefix(p, "metadata.annotations")) {
			return true
		}
		if strings.HasPrefix(p, "spec.") || strings.HasPrefix(p, "metadata.generation") || p == "immutable" || strings.HasPrefix(p, "data") {
			return true
		}
	}
	return false
}

func hasRuntimeStatusField(e timeline.TimelineEvent) bool {
	for _, f := range e.Diff.Fields {
		if strings.HasPrefix(strings.ToLower(f.Path), "status.") {
			return true
		}
	}
	return false
}

func score(c issuesapi.RecentChange) int {
	if c.NotLinkedToReturnedIssues {
		return 120
	}
	if c.ApplicationConfigurationChange {
		return 105
	}
	if isGenerationOnly(c) {
		return 80
	}
	switch c.ChangeCategory {
	case issuesapi.ChangeCategorySpecConfig:
		return 100
	case issuesapi.ChangeCategoryLifecycle:
		return 70
	case issuesapi.ChangeCategoryRuntimeStatus:
		return 40
	default:
		return 0
	}
}

const generationOnlyRankReason = "generation changed; specific fields were not tracked"

// secretRankReason states only what the row establishes: a data change when
// keys changed, references when consumed_by names them, and stale env readers
// when running_previous_value is set.
func secretRankReason(change issuesapi.RecentChange) string {
	reason := "Secret changed"
	if len(secretChangedKeys(change.Fields)) > 0 {
		reason = "Secret data changed"
	}
	switch {
	case change.RunningPreviousValue != nil:
		if change.RunningPreviousValue.Pods == 1 {
			return reason + "; 1 Pod likely still holds the earlier value"
		}
		return fmt.Sprintf("%s; %d Pods likely still hold the earlier value", reason, change.RunningPreviousValue.Pods)
	case len(change.ConsumedBy) > 0:
		return reason + "; referenced by a workload template or current Pod spec"
	default:
		return reason
	}
}

func classifyApplicationConfigurationChange(change *issuesapi.RecentChange) {
	if change == nil || change.NotLinkedToReturnedIssues {
		return
	}
	if change.Kind == "Secret" && change.ChangeCategory == issuesapi.ChangeCategorySpecConfig {
		change.ApplicationConfigurationChange = change.RunningPreviousValue != nil
		change.RankReason = secretRankReason(*change)
		return
	}
	if change.ApplicationConfigurationChange {
		if change.Kind != "ConfigMap" || len(change.ConsumedBy) > 0 {
			change.RankReason = appConfigRankReason(*change)
			return
		}
		change.ApplicationConfigurationChange = false
		change.RankReason = genericSpecRankReason(*change)
	}
	if isApplicationConfigurationChange(*change) {
		change.ApplicationConfigurationChange = true
		change.RankReason = appConfigRankReason(*change)
		return
	}
	if isGenerationOnly(*change) {
		change.RankReason = generationOnlyRankReason
	}
}

func appConfigRankReason(change issuesapi.RecentChange) string {
	if change.ChangeType == string(timeline.EventTypeAdd) {
		return "recreated with application configuration changes"
	}
	return "application configuration field changed"
}

func genericSpecRankReason(change issuesapi.RecentChange) string {
	if change.ChangeType == string(timeline.EventTypeAdd) {
		return "recreated with desired-state or configuration changes"
	}
	return "desired-state or configuration field changed"
}

func isApplicationConfigurationChange(change issuesapi.RecentChange) bool {
	if change.ChangeCategory != issuesapi.ChangeCategorySpecConfig {
		return false
	}
	if change.Kind == "ConfigMap" {
		return len(change.ConsumedBy) > 0 && hasApplicationConfigChange(change.Fields)
	}
	return isWorkloadKind(change.Kind) && hasWorkloadConfigChange(change.Fields)
}

func hasApplicationConfigField(kind string, fields []timeline.FieldChange) bool {
	for _, field := range fields {
		if isApplicationConfigPath(kind, field.Path) {
			return true
		}
	}
	return false
}

func isApplicationConfigPath(kind, path string) bool {
	if kind == "ConfigMap" {
		return isConfigMapDataPath(path)
	}
	return isWorkloadKind(kind) && isWorkloadConfigPath(path)
}

func hasApplicationConfigChange(fields []issuesapi.ChangeField) bool {
	for _, field := range fields {
		if isConfigMapDataPath(field.Path) {
			return true
		}
	}
	return false
}

func isConfigMapDataPath(path string) bool {
	switch path {
	case "data (added keys)", "data (removed keys)", "data (modified keys)":
		return true
	default:
		return strings.HasPrefix(path, "data.")
	}
}

func hasWorkloadConfigChange(fields []issuesapi.ChangeField) bool {
	for _, field := range fields {
		if isWorkloadConfigPath(field.Path) {
			return true
		}
	}
	return false
}

func isWorkloadConfigPath(path string) bool {
	var rest string
	for _, prefix := range []string{
		"spec.template.spec.containers[",
		"spec.template.spec.initContainers[",
	} {
		if strings.HasPrefix(path, prefix) {
			rest = strings.TrimPrefix(path, prefix)
			break
		}
	}
	if rest == "" {
		return false
	}
	end := strings.IndexByte(rest, ']')
	if end <= 0 {
		return false
	}
	if end == len(rest)-1 {
		return true
	}
	if rest[end+1] != '.' {
		return false
	}
	field := rest[end+2:]
	return strings.HasPrefix(field, "env[") ||
		field == "envFrom" ||
		field == "command" ||
		field == "args" ||
		field == "image"
}

func isGenerationOnly(change issuesapi.RecentChange) bool {
	return change.ChangeCategory == issuesapi.ChangeCategorySpecConfig &&
		len(change.Fields) == 1 &&
		isGenerationPath(change.Fields[0].Path)
}

func isGenerationOnlyTimelineFields(fields []timeline.FieldChange) bool {
	return len(fields) == 1 &&
		isGenerationPath(fields[0].Path)
}

func isGenerationPath(path string) bool {
	return strings.EqualFold(strings.TrimSpace(path), "metadata.generation")
}

func eventSummary(e timeline.TimelineEvent) string {
	if e.Diff != nil && e.Diff.Summary != "" {
		return e.Diff.Summary
	}
	if e.Message != "" {
		return truncate(e.Message, 160)
	}
	return string(e.EventType)
}

func compactKinds(kinds []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		kind = canonicalKind(kind)
		if kind == "" || seen[kind] {
			continue
		}
		seen[kind] = true
		out = append(out, kind)
	}
	return out
}

func compactNames(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return []string{name}
}

// coalesceRecreatePairs drops delete events whose resource was subsequently
// recreated (a newer add event marked ReasonRecreated, which carries the diff
// across the recreate). The synthesized add tells the whole story; keeping
// the paired delete would present one change as two entries. True deletions —
// no recreate add after them — always survive.
func coalesceRecreatePairs(events []timeline.TimelineEvent) []timeline.TimelineEvent {
	var recreates map[string]time.Time
	for _, e := range events {
		if e.EventType == timeline.EventTypeAdd && e.Reason == timeline.ReasonRecreated {
			if recreates == nil {
				recreates = map[string]time.Time{}
			}
			key := e.Kind + "/" + e.Namespace + "/" + e.Name
			if ts, ok := recreates[key]; !ok || e.Timestamp.After(ts) {
				recreates[key] = e.Timestamp
			}
		}
	}
	if recreates == nil {
		return events
	}
	out := make([]timeline.TimelineEvent, 0, len(events))
	for _, e := range events {
		if e.EventType == timeline.EventTypeDelete {
			if ts, ok := recreates[e.Kind+"/"+e.Namespace+"/"+e.Name]; ok && !e.Timestamp.After(ts) {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

func dedupeEvents(events []timeline.TimelineEvent) []timeline.TimelineEvent {
	seen := map[string]bool{}
	out := make([]timeline.TimelineEvent, 0, len(events))
	for _, e := range events {
		key := e.ID
		if key == "" {
			key = fmt.Sprintf("%s/%s/%s/%s/%s", e.Timestamp.Format(time.RFC3339Nano), e.Kind, e.Namespace, e.Name, e.EventType)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}

func isConfigKind(kind string) bool { return kind == "ConfigMap" }

// isWebhookConfigKind reports admission webhook configuration kinds. Their
// config lives at top-level webhooks[] with no status subresource, so every
// tracked diff is a configuration change.
func isWebhookConfigKind(kind string) bool {
	return kind == "MutatingWebhookConfiguration" || kind == "ValidatingWebhookConfiguration"
}

func isLifecycleOnlyKind(kind string) bool {
	for _, item := range lifecycleOnlyKinds {
		if kind == item {
			return true
		}
	}
	return false
}

func isSpecKind(kind string) bool {
	for _, item := range specKinds {
		if kind == item {
			return true
		}
	}
	return false
}

func isWorkloadKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "deployment", "deployments", "statefulset", "statefulsets", "daemonset", "daemonsets", "rollout", "rollouts", "pod", "pods":
		return true
	default:
		return false
	}
}

func canonicalKind(kind string) string {
	// Discovery is the authority: lowercase-keyed by kind AND plural, yields
	// the exact PascalCase Kind for everything on the cluster, CRDs included.
	// The static table below only covers the window before discovery exists
	// (cold start, context switch) and kinds not installed on this cluster.
	if d := k8s.GetResourceDiscovery(); d != nil {
		if res, ok := d.GetResource(strings.TrimSpace(kind)); ok && res.Kind != "" {
			return res.Kind
		}
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "cm", "configmap", "configmaps":
		return "ConfigMap"
	case "deploy", "deployment", "deployments":
		return "Deployment"
	case "statefulset", "statefulsets":
		return "StatefulSet"
	case "daemonset", "daemonsets":
		return "DaemonSet"
	case "rollout", "rollouts":
		return "Rollout"
	case "svc", "service", "services":
		return "Service"
	case "ingress", "ingresses":
		return "Ingress"
	case "hpa", "horizontalpodautoscaler", "horizontalpodautoscalers":
		return "HorizontalPodAutoscaler"
	case "pod", "pods":
		return "Pod"
	case "application", "applications":
		return "Application"
	case "kustomization", "kustomizations":
		return "Kustomization"
	case "helmrelease", "helmreleases":
		return "HelmRelease"
	case "gitrepository", "gitrepositories":
		return "GitRepository"
	case "ocirepository", "ocirepositories":
		return "OCIRepository"
	case "tcproute", "tcproutes":
		return "TCPRoute"
	case "udproute", "udproutes":
		return "UDPRoute"
	case "tlsroute", "tlsroutes":
		return "TLSRoute"
	case "referencegrant", "referencegrants":
		return "ReferenceGrant"
	case "gatewayclass", "gatewayclasses":
		return "GatewayClass"
	case "helmrepository", "helmrepositories":
		return "HelmRepository"
	case "cronjob", "cronjobs":
		return "CronJob"
	case "resourcequota", "resourcequotas", "quota", "quotas":
		return "ResourceQuota"
	case "limitrange", "limitranges", "limits":
		return "LimitRange"
	case "job", "jobs":
		return "Job"
	case "replicaset", "replicasets", "rs":
		return "ReplicaSet"
	case "secret", "secrets":
		return "Secret"
	case "pvc", "persistentvolumeclaim", "persistentvolumeclaims":
		return "PersistentVolumeClaim"
	case "pv", "persistentvolume", "persistentvolumes":
		return "PersistentVolume"
	case "serviceaccount", "serviceaccounts", "sa":
		return "ServiceAccount"
	case "networkpolicy", "networkpolicies":
		return "NetworkPolicy"
	case "poddisruptionbudget", "poddisruptionbudgets", "pdb":
		return "PodDisruptionBudget"
	case "mutatingwebhookconfiguration", "mutatingwebhookconfigurations":
		return "MutatingWebhookConfiguration"
	case "validatingwebhookconfiguration", "validatingwebhookconfigurations":
		return "ValidatingWebhookConfiguration"
	case "httproute", "httproutes":
		return "HTTPRoute"
	case "grpcroute", "grpcroutes":
		return "GRPCRoute"
	case "gateway", "gateways":
		return "Gateway"
	default:
		if kind == "" {
			return ""
		}
		// Mixed-case input is an exact kind (CRDs the table can't know) —
		// pass it through. Only best-effort capitalize all-lowercase input;
		// timeline events store Kubernetes PascalCase, so "Cronjob"-style
		// guesses on multi-word kinds would silently match nothing.
		if strings.ToLower(kind) != kind {
			return kind
		}
		return strings.ToUpper(kind[:1]) + kind[1:]
	}
}

func podSpecForObject(obj any) (corev1.PodSpec, bool) {
	switch o := obj.(type) {
	case *corev1.Pod:
		return o.Spec, true
	case *appsv1.Deployment:
		return o.Spec.Template.Spec, true
	case *appsv1.StatefulSet:
		return o.Spec.Template.Spec, true
	case *appsv1.DaemonSet:
		return o.Spec.Template.Spec, true
	case *batchv1.Job:
		return o.Spec.Template.Spec, true
	case *batchv1.CronJob:
		return o.Spec.JobTemplate.Spec.Template.Spec, true
	case *unstructured.Unstructured:
		if o.GetKind() == "Rollout" && resourceid.GroupFromAPIVersion(o.GetAPIVersion()) == "argoproj.io" {
			template, found, err := unstructured.NestedMap(o.Object, "spec", "template", "spec")
			if err == nil && found {
				var spec corev1.PodSpec
				if runtime.DefaultUnstructuredConverter.FromUnstructured(template, &spec) == nil {
					return spec, true
				}
			}
			return rolloutWorkloadRefPodSpec(o)
		}
	}
	return corev1.PodSpec{}, false
}

// rolloutWorkloadRefPodSpec reads the pod template of the Deployment or
// ReplicaSet a Rollout's spec.workloadRef names, from the informer cache.
func rolloutWorkloadRefPodSpec(rollout *unstructured.Unstructured) (corev1.PodSpec, bool) {
	target, err := rollouts.ResolveTemplateTarget(rollout)
	cache := k8s.GetResourceCache()
	if err != nil || target.GVR == rollouts.GVR || cache == nil {
		return corev1.PodSpec{}, false
	}
	switch target.GVR.Resource {
	case "deployments":
		if lister := cache.Deployments(); lister != nil {
			if deployment, err := lister.Deployments(rollout.GetNamespace()).Get(target.Name); err == nil {
				return deployment.Spec.Template.Spec, true
			}
		}
	case "replicasets":
		if lister := cache.ReplicaSets(); lister != nil {
			if replicaSet, err := lister.ReplicaSets(rollout.GetNamespace()).Get(target.Name); err == nil {
				return replicaSet.Spec.Template.Spec, true
			}
		}
	}
	return corev1.PodSpec{}, false
}

// ConfigReferencesKnown reports whether obj's pod template could be read, so
// its referenced ConfigMaps and Secrets are known.
func ConfigReferencesKnown(obj any) bool {
	_, ok := podSpecForObject(obj)
	return ok
}

func changeKey(c issuesapi.RecentChange) string {
	return c.Kind + "/" + c.Namespace + "/" + c.Name + "/" + c.ChangeType
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
