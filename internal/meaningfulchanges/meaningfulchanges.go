package meaningfulchanges

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
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
	maxCandidateLimit          = 100
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
	FetchSaturatedGuidance = "Radar's change query hit its candidate cap, so this window may contain changes the query never saw; absence from this feed is not evidence a change did not occur."
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
		// Cluster-scoped (namespace==""), so they surface in a cluster-wide
		// get_changes, not in a single-namespace query — correct, since one
		// webhook config gates admission across all namespaces.
		"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration",
	}
	// lifecycleOnlyKinds surface ONLY their delete events (name-only — never
	// data). A deleted Secret breaks every consumer with zero K8s symptom on
	// the Secret itself; its updates stay out of the feed entirely.
	lifecycleOnlyKinds = []string{"Secret"}
)

type Query struct {
	Namespaces []string
	Kinds      []string
	Name       string
	Since      time.Duration
	Limit      int
	FieldLimit int
}

type RecentResult struct {
	Changes        []issuesapi.RecentChange
	OutputCapped   bool
	FetchSaturated bool
}

func Recent(ctx context.Context, q Query) (RecentResult, error) {
	changes, outputCapped, fetchSaturated, err := recent(ctx, q)
	return RecentResult{
		Changes:        changes,
		OutputCapped:   outputCapped,
		FetchSaturated: fetchSaturated,
	}, err
}

// recent returns ranked changes plus two distinct truncation signals:
// outputCapped (more qualifying changes existed than the requested limit) and
// fetchSaturated (a candidate query hit its cap, so the window may contain
// older events the fetch never saw). Negative claims ("no recent changes")
// must key on fetchSaturated alone — output capping still observes the full
// window and ranks spec/config changes above the churn that gets dropped.
func recent(ctx context.Context, q Query) ([]issuesapi.RecentChange, bool, bool, error) {
	store := timeline.GetStore()
	if store == nil {
		return nil, false, false, fmt.Errorf("timeline store not initialized")
	}
	q = normalizeQuery(q)

	if len(q.Kinds) > 0 || q.Name != "" {
		queryLimit := candidateLimit(q.Limit, q.Name != "")
		events, rawEvents, err := queryCandidates(ctx, store, q, q.Kinds, queryLimit)
		if err != nil {
			return nil, false, false, err
		}
		// Lifecycle events (add/delete) are fetched separately so a burst of
		// update churn can't push them out of the newest-N candidate window
		// before ranking ever sees them. Ranking scores deletes above status
		// churn, but it can only rank what the fetch returns.
		lifecycleEvents, rawLifecycle, err := queryLifecycleCandidates(ctx, store, q, q.Kinds)
		if err != nil {
			return nil, false, false, err
		}
		changes, capped, err := rankedChanges(coalesceRecreatePairs(dedupeEvents(append(events, lifecycleEvents...))), q.Name, q.Limit, q.FieldLimit)
		saturated := rawEvents >= queryLimit || rawLifecycle >= lifecycleCandidateLimit
		return changes, capped, saturated, err
	}

	perQueryLimit := candidateLimit(q.Limit, false)
	configEvents, rawConfig, err := queryCandidates(ctx, store, q, configKinds, perQueryLimit)
	if err != nil {
		return nil, false, false, err
	}
	specEvents, rawSpec, err := queryCandidates(ctx, store, q, specKinds, perQueryLimit)
	if err != nil {
		return nil, false, false, err
	}
	lifecycleKinds := append(append([]string{}, configKinds...), specKinds...)
	lifecycleKinds = append(lifecycleKinds, lifecycleOnlyKinds...)
	lifecycleEvents, rawLifecycle, err := queryLifecycleCandidates(ctx, store, q, lifecycleKinds)
	if err != nil {
		return nil, false, false, err
	}
	merged := coalesceRecreatePairs(dedupeEvents(append(append(configEvents, specEvents...), lifecycleEvents...)))
	changes, capped, err := rankedChanges(merged, "", q.Limit, q.FieldLimit)
	saturated := rawConfig >= perQueryLimit || rawSpec >= perQueryLimit || rawLifecycle >= lifecycleCandidateLimit
	return changes, capped, saturated, err
}

// RecentForResource returns the subject's ranked changes plus a saturation
// flag: true when a candidate fetch hit its cap and the window may contain
// changes the query never saw. Callers asserting "no recent changes" must
// treat saturation as unknown, never as evidence of absence.
func RecentForResource(ctx context.Context, kind, namespace, name string, since time.Duration, limit, fieldLimit int) ([]issuesapi.RecentChange, bool, error) {
	changes, _, fetchSaturated, err := recent(ctx, Query{
		Namespaces: []string{namespace},
		Kinds:      []string{canonicalKind(kind)},
		Name:       name,
		Since:      since,
		Limit:      limit,
		FieldLimit: fieldLimit,
	})
	return changes, fetchSaturated, err
}

func RecentForWorkloadAndConfigMaps(ctx context.Context, obj any, kind, namespace, name string, since time.Duration, limit, fieldLimit int) ([]issuesapi.RecentChange, bool, error) {
	result, err := RecentForWorkloadAndConfigMapsDetailed(ctx, obj, kind, namespace, name, since, limit, fieldLimit)
	return result.Changes, result.FetchSaturated, err
}

// RecentForWorkloadAndConfigMapsDetailed preserves the distinct output-cap
// and candidate-fetch saturation signals needed by consumers that display
// source coverage. The historical wrapper above intentionally exposes only
// fetch saturation because issue-correlation uses that bool for negative-claim
// gating.
func RecentForWorkloadAndConfigMapsDetailed(ctx context.Context, obj any, kind, namespace, name string, since time.Duration, limit, fieldLimit int) (RecentResult, error) {
	result, _, err := RecentForWorkloadAndConfigMapsAuthorizedDetailed(
		ctx, obj, kind, namespace, name, since, limit, fieldLimit, nil,
	)
	return result, err
}

// RecentForWorkloadAndConfigMapsAuthorizedDetailed is the authorization-aware
// form used by cached-data surfaces. includeSource is evaluated for every
// resource whose timeline would be queried, before the query runs. A false
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
) (result RecentResult, coverageLimited bool, err error) {
	fetch := func(resourceKind, resourceName string) error {
		canonicalResourceKind := canonicalKind(resourceKind)
		if includeSource != nil && !includeSource(canonicalResourceKind, resourceName) {
			coverageLimited = true
			return nil
		}
		resourceResult, err := Recent(ctx, Query{
			Namespaces: []string{namespace},
			Kinds:      []string{canonicalResourceKind},
			Name:       resourceName,
			Since:      since,
			Limit:      limit,
			FieldLimit: fieldLimit,
		})
		if err != nil {
			return err
		}
		result.Changes = append(result.Changes, resourceResult.Changes...)
		result.OutputCapped = result.OutputCapped || resourceResult.OutputCapped
		result.FetchSaturated = result.FetchSaturated || resourceResult.FetchSaturated
		return nil
	}

	if isWorkloadKind(kind) {
		if err := fetch(kind, name); err != nil {
			return RecentResult{}, coverageLimited, err
		}
	}
	for _, cm := range DirectConfigMapNames(obj) {
		if err := fetch("ConfigMap", cm); err != nil {
			return RecentResult{}, coverageLimited, err
		}
	}
	preCapCount := len(result.Changes)
	RankAndCap(&result.Changes, limit)
	result.OutputCapped = result.OutputCapped || len(result.Changes) < preCapCount
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

func DirectConfigMapNames(obj any) []string {
	spec, ok := podSpecForObject(obj)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" {
			seen[name] = true
		}
	}
	for _, v := range spec.Volumes {
		if v.ConfigMap != nil {
			add(v.ConfigMap.Name)
		}
		if v.Projected != nil {
			for _, source := range v.Projected.Sources {
				if source.ConfigMap != nil {
					add(source.ConfigMap.Name)
				}
			}
		}
	}
	for _, c := range allContainers(spec) {
		for _, from := range c.EnvFrom {
			if from.ConfigMapRef != nil {
				add(from.ConfigMapRef.Name)
			}
		}
		for _, env := range c.Env {
			if env.ValueFrom != nil && env.ValueFrom.ConfigMapKeyRef != nil {
				add(env.ValueFrom.ConfigMapKeyRef.Name)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
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
	return q
}

func candidateLimit(finalLimit int, nameFiltered bool) int {
	limit := finalLimit * 4
	if limit < 50 {
		limit = 50
	}
	if nameFiltered && limit < 100 {
		limit = 100
	}
	if limit > maxCandidateLimit {
		limit = maxCandidateLimit
	}
	return limit
}

// lifecycleCandidateLimit bounds the dedicated add/delete query. Lifecycle
// events are rare relative to updates, so a modest window covers the lookback
// without re-importing the churn the separate query exists to escape.
const lifecycleCandidateLimit = 50

// queryLifecycleCandidates fetches add/delete events for the given kinds in a
// query of their own, immune to crowding by update events.
// queryLifecycleCandidates returns group-filtered events plus the bounded
// query's count. When all kinds share one known group, the store applies that
// group before its limit; heterogeneous queries retain the row-level guard.
func queryLifecycleCandidates(ctx context.Context, store timeline.EventStore, q Query, kinds []string) ([]timeline.TimelineEvent, int, error) {
	queryKinds := compactKinds(kinds)
	opts := timeline.QueryOptions{
		Namespaces:       q.Namespaces,
		Kinds:            queryKinds,
		Names:            compactNames(q.Name),
		APIGroups:        commonTrackedAPIGroups(queryKinds),
		Since:            time.Now().Add(-q.Since),
		Sources:          []timeline.EventSource{timeline.SourceInformer},
		EventTypes:       []timeline.EventType{timeline.EventTypeAdd, timeline.EventTypeDelete},
		ClusterContext:   k8s.ActiveClusterContext(),
		Limit:            lifecycleCandidateLimit,
		IncludeManaged:   false,
		IncludeK8sEvents: false,
	}
	events, err := store.Query(ctx, opts)
	return filterTrackedGroupEvents(events), len(events), err
}

// queryCandidates returns group-filtered events plus the bounded query's
// count (see queryLifecycleCandidates for group-filter placement).
func queryCandidates(ctx context.Context, store timeline.EventStore, q Query, kinds []string, limit int) ([]timeline.TimelineEvent, int, error) {
	queryKinds := compactKinds(kinds)
	opts := timeline.QueryOptions{
		Namespaces: q.Namespaces,
		Kinds:      queryKinds,
		Names:      compactNames(q.Name),
		APIGroups:  commonTrackedAPIGroups(queryKinds),
		Since:      time.Now().Add(-q.Since),
		Sources:    []timeline.EventSource{timeline.SourceInformer},
		// Changes are root-cause evidence for the CURRENT cluster — the
		// persistent store retains other contexts' events across switches,
		// and serving those here hands agents phantom changes.
		ClusterContext:   k8s.ActiveClusterContext(),
		Limit:            limit,
		IncludeManaged:   false,
		IncludeK8sEvents: false,
	}
	events, err := store.Query(ctx, opts)
	return filterTrackedGroupEvents(events), len(events), err
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
	if !ok {
		return true
	}
	group := "" // bare "v1" = core group
	if idx := strings.IndexByte(apiVersion, '/'); idx > 0 {
		group = apiVersion[:idx]
	}
	return group == expected
}

func rankedChanges(events []timeline.TimelineEvent, name string, limit, fieldLimit int) ([]issuesapi.RecentChange, bool, error) {
	out := make([]issuesapi.RecentChange, 0, len(events))
	for _, e := range events {
		if name != "" && e.Name != name {
			continue
		}
		change := fromEvent(e, fieldLimit)
		if change.ChangeCategory == "" {
			continue
		}
		out = append(out, change)
	}
	capped := len(out) > limit
	annotateConfigMapConsumers(out)
	RankAndCap(&out, limit)
	return out, capped, nil
}

// maxConsumersPerEntry bounds the consumed_by list on a ConfigMap entry.
const maxConsumersPerEntry = 5

// annotateConfigMapConsumers fills ConsumedBy on ConfigMap change entries from
// a single workload-cache pass. Direct references only: a workload that reads
// this ConfigMap's data through an intermediary service is invisible here, and
// ConsumedBy deliberately makes no claim about it.
func annotateConfigMapConsumers(changes []issuesapi.RecentChange) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return
	}
	needsIndex := false
	for i := range changes {
		if changes[i].Kind == "ConfigMap" && changes[i].Namespace != "" {
			needsIndex = true
			break
		}
	}
	if !needsIndex {
		return
	}
	namespaces := map[string]bool{}
	for _, change := range changes {
		if change.Kind == "ConfigMap" && change.Namespace != "" {
			namespaces[change.Namespace] = true
		}
	}
	index := configMapConsumerIndex(cache, namespaces)
	for i := range changes {
		if changes[i].Kind != "ConfigMap" || changes[i].Namespace == "" {
			continue
		}
		changes[i].ConsumedBy = index[configMapKey(changes[i].Namespace, changes[i].Name)]
	}
}

func configMapConsumerIndex(cache *k8s.ResourceCache, namespaces map[string]bool) map[string][]string {
	index := map[string][]string{}
	add := func(namespace, kind, name string, obj any) {
		for _, cm := range DirectConfigMapNames(obj) {
			key := configMapKey(namespace, cm)
			index[key] = append(index[key], kind+"/"+name)
		}
	}
	if lister := cache.Deployments(); lister != nil {
		for namespace := range namespaces {
			if items, err := lister.Deployments(namespace).List(labels.Everything()); err == nil {
				for _, item := range items {
					add(namespace, "Deployment", item.Name, item)
				}
			}
		}
	}
	if lister := cache.StatefulSets(); lister != nil {
		for namespace := range namespaces {
			if items, err := lister.StatefulSets(namespace).List(labels.Everything()); err == nil {
				for _, item := range items {
					add(namespace, "StatefulSet", item.Name, item)
				}
			}
		}
	}
	if lister := cache.DaemonSets(); lister != nil {
		for namespace := range namespaces {
			if items, err := lister.DaemonSets(namespace).List(labels.Everything()); err == nil {
				for _, item := range items {
					add(namespace, "DaemonSet", item.Name, item)
				}
			}
		}
	}
	if lister := cache.Jobs(); lister != nil {
		for namespace := range namespaces {
			if items, err := lister.Jobs(namespace).List(labels.Everything()); err == nil {
				for _, item := range items {
					add(namespace, "Job", item.Name, item)
				}
			}
		}
	}
	if lister := cache.CronJobs(); lister != nil {
		for namespace := range namespaces {
			if items, err := lister.CronJobs(namespace).List(labels.Everything()); err == nil {
				for _, item := range items {
					add(namespace, "CronJob", item.Name, item)
				}
			}
		}
	}
	for key, consumers := range index {
		sort.Strings(consumers)
		if len(consumers) > maxConsumersPerEntry {
			index[key] = consumers[:maxConsumersPerEntry]
		} else {
			index[key] = consumers
		}
	}
	return index
}

func configMapKey(namespace, name string) string {
	return namespace + "\x00" + name
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
		if changes[i].Kind == "ConfigMap" {
			// consumed_by is capped. At the cap, an omitted consumer may carry
			// the issue, so absence is not strong enough to promote this entry.
			if len(changes[i].ConsumedBy) >= maxConsumersPerEntry {
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
// specKinds) — no lifecycleOnlyKinds. Secret is delete-only in the feed
// (updates are never recorded), so making Secret issues marker-eligible
// would emit a false no_recent_changes after a data rotation the feed
// cannot see. The drift-guard test pins both directions.
var trackedKindGroups = map[string]string{
	"ConfigMap": "", "Service": "", "ResourceQuota": "", "LimitRange": "",
	"Deployment": "apps", "StatefulSet": "apps", "DaemonSet": "apps",
	"Rollout":                        "argoproj.io",
	"Ingress":                        "networking.k8s.io",
	"HorizontalPodAutoscaler":        "autoscaling",
	"Application":                    "argoproj.io",
	"Kustomization":                  "kustomize.toolkit.fluxcd.io",
	"HelmRelease":                    "helm.toolkit.fluxcd.io",
	"GitRepository":                  "source.toolkit.fluxcd.io",
	"OCIRepository":                  "source.toolkit.fluxcd.io",
	"HelmRepository":                 "source.toolkit.fluxcd.io",
	"MutatingWebhookConfiguration":   "admissionregistration.k8s.io",
	"ValidatingWebhookConfiguration": "admissionregistration.k8s.io",
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
			return issuesapi.ChangeCategoryLifecycle, "resource create/delete for config or desired state"
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

func classifyApplicationConfigurationChange(change *issuesapi.RecentChange) {
	if change == nil || change.NotLinkedToReturnedIssues {
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
	default:
		return corev1.PodSpec{}, false
	}
}

func allContainers(spec corev1.PodSpec) []corev1.Container {
	out := make([]corev1.Container, 0, len(spec.InitContainers)+len(spec.Containers))
	out = append(out, spec.InitContainers...)
	out = append(out, spec.Containers...)
	return out
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
