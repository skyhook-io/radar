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
	ResourceLimit     = 3
	maxCandidateLimit = 100
)

const (
	ChangesReasonNoCriticalIssues             = "no_critical_issues"
	ChangesReasonAllCriticalStartedAtCreation = "all_critical_issues_started_at_resource_creation"
)

var (
	configKinds = []string{"ConfigMap"}
	specKinds   = []string{
		"Deployment", "StatefulSet", "DaemonSet", "Service", "Ingress",
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

func Recent(ctx context.Context, q Query) ([]issuesapi.RecentChange, bool, error) {
	changes, outputCapped, fetchSaturated, err := recent(ctx, q)
	return changes, outputCapped || fetchSaturated, err
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
	changes, _, saturated, err := recent(ctx, Query{
		Namespaces: []string{namespace},
		Kinds:      []string{canonicalKind(kind)},
		Name:       name,
		Since:      since,
		Limit:      limit,
		FieldLimit: fieldLimit,
	})
	return changes, saturated, err
}

func RecentForWorkloadAndConfigMaps(ctx context.Context, obj any, kind, namespace, name string, since time.Duration, limit, fieldLimit int) ([]issuesapi.RecentChange, bool, error) {
	var all []issuesapi.RecentChange
	saturated := false
	if isWorkloadKind(kind) {
		changes, sat, err := RecentForResource(ctx, kind, namespace, name, since, limit, fieldLimit)
		if err != nil {
			return nil, false, err
		}
		saturated = saturated || sat
		all = append(all, changes...)
	}
	for _, cm := range DirectConfigMapNames(obj) {
		changes, sat, err := RecentForResource(ctx, "ConfigMap", namespace, cm, since, limit, fieldLimit)
		if err != nil {
			return nil, false, err
		}
		saturated = saturated || sat
		all = append(all, changes...)
	}
	RankAndCap(&all, limit)
	return all, saturated, nil
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
	return ChangesReasonAllCriticalStartedAtCreation
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
// queryLifecycleCandidates returns group-filtered events plus the RAW
// pre-filter count — saturation must key on how many events the bounded
// query consumed, not how many survived filtering, or mismatched-group
// events crowding the window would turn "unknown" into a false "no
// changes".
func queryLifecycleCandidates(ctx context.Context, store timeline.EventStore, q Query, kinds []string) ([]timeline.TimelineEvent, int, error) {
	opts := timeline.QueryOptions{
		Namespaces:       q.Namespaces,
		Kinds:            compactKinds(kinds),
		Names:            compactNames(q.Name),
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

// queryCandidates returns group-filtered events plus the RAW pre-filter
// count (see queryLifecycleCandidates for why saturation needs it).
func queryCandidates(ctx context.Context, store timeline.EventStore, q Query, kinds []string, limit int) ([]timeline.TimelineEvent, int, error) {
	opts := timeline.QueryOptions{
		Namespaces: q.Namespaces,
		Kinds:      compactKinds(kinds),
		Names:      compactNames(q.Name),
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
	RankAndCap(&out, limit)
	annotateConfigMapConsumers(out)
	return out, capped, nil
}

// maxConsumersPerEntry bounds the consumed_by list on a ConfigMap entry.
const maxConsumersPerEntry = 5

// annotateConfigMapConsumers fills ConsumedBy on ConfigMap change entries by
// scanning the cached workload listers for direct spec references (volumes,
// envFrom, env valueFrom). Runs after ranking/capping, so at most one feed's
// worth of entries triggers the scan. Direct references only: a workload that
// reads this ConfigMap's data through an intermediary service is invisible
// here, and ConsumedBy deliberately makes no claim about it.
func annotateConfigMapConsumers(changes []issuesapi.RecentChange) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return
	}
	for i := range changes {
		if changes[i].Kind != "ConfigMap" || changes[i].Namespace == "" {
			continue
		}
		changes[i].ConsumedBy = consumersOfConfigMap(cache, changes[i].Namespace, changes[i].Name)
	}
}

func consumersOfConfigMap(cache *k8s.ResourceCache, namespace, name string) []string {
	var out []string
	referencesConfigMap := func(obj any) bool {
		for _, cm := range DirectConfigMapNames(obj) {
			if cm == name {
				return true
			}
		}
		return false
	}
	if lister := cache.Deployments(); lister != nil {
		if items, err := lister.Deployments(namespace).List(labels.Everything()); err == nil {
			for _, d := range items {
				if referencesConfigMap(d) {
					out = append(out, "Deployment/"+d.Name)
				}
			}
		}
	}
	if lister := cache.StatefulSets(); lister != nil {
		if items, err := lister.StatefulSets(namespace).List(labels.Everything()); err == nil {
			for _, s := range items {
				if referencesConfigMap(s) {
					out = append(out, "StatefulSet/"+s.Name)
				}
			}
		}
	}
	if lister := cache.DaemonSets(); lister != nil {
		if items, err := lister.DaemonSets(namespace).List(labels.Everything()); err == nil {
			for _, d := range items {
				if referencesConfigMap(d) {
					out = append(out, "DaemonSet/"+d.Name)
				}
			}
		}
	}
	if lister := cache.Jobs(); lister != nil {
		if items, err := lister.Jobs(namespace).List(labels.Everything()); err == nil {
			for _, j := range items {
				if referencesConfigMap(j) {
					out = append(out, "Job/"+j.Name)
				}
			}
		}
	}
	if lister := cache.CronJobs(); lister != nil {
		if items, err := lister.CronJobs(namespace).List(labels.Everything()); err == nil {
			for _, cj := range items {
				if referencesConfigMap(cj) {
					out = append(out, "CronJob/"+cj.Name)
				}
			}
		}
	}
	sort.Strings(out)
	if len(out) > maxConsumersPerEntry {
		out = out[:maxConsumersPerEntry]
	}
	return out
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
		Namespace:      e.Namespace,
		Name:           e.Name,
		ChangeType:     string(e.EventType),
		Summary:        eventSummary(e),
		Timestamp:      e.Timestamp.Format(time.RFC3339),
		ChangeCategory: category,
		RankReason:     reason,
	}
	if e.Diff != nil {
		fields := e.Diff.Fields
		if fieldLimit > 0 && len(fields) > fieldLimit {
			fields = fields[:fieldLimit]
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
	case "deployment", "deployments", "statefulset", "statefulsets", "daemonset", "daemonsets", "pod", "pods":
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
