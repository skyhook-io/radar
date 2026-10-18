package meaningfulchanges

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	clusterScopedRowLimit   = 5
	clusterScopedNamedLimit = 10
	clusterScopedFieldLimit = 50
)

// clusterScopedKinds are the cluster-scoped kinds whose changes a namespace
// view includes. Admission configuration is listed because it rewrites or
// rejects the namespace's objects with no trace on them, and because its
// scope can be evaluated against the namespace exactly.
var clusterScopedKinds = []string{"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"}

// NamespaceLabels is a namespace's labels as the caller may read them.
// Known=false means the labels could not be read, so namespace selectors
// cannot be evaluated.
type NamespaceLabels struct {
	Labels labels.Set
	Known  bool
}

// ReadNamespaceLabels returns namespace's cached labels when canRead allows
// reading the Namespace object.
func ReadNamespaceLabels(namespace string, canRead bool) NamespaceLabels {
	cache := k8s.GetResourceCache()
	if !canRead || cache == nil || cache.Namespaces() == nil {
		return NamespaceLabels{}
	}
	ns, err := cache.Namespaces().Get(namespace)
	if err != nil {
		return NamespaceLabels{}
	}
	return NamespaceLabels{Labels: labels.Set(ns.Labels), Known: true}
}

// ClusterScopedForNamespace returns the window's admission-configuration
// changes that apply, or may apply, to namespace. canReadKind gates each kind
// before the timeline is read, so neither rows, saturation nor counts say
// anything about history the caller may not read. Every candidate in the
// window is judged before the row budget applies.
//
// quietStartup leaves out the notes that only say Radar started (or started
// watching webhooks) inside the window, unless something else is incomplete
// too. A view that is not about history, like an issues answer, would
// otherwise carry them on every call after a restart.
func ClusterScopedForNamespace(ctx context.Context, namespace string, nsLabels NamespaceLabels, since time.Duration, canReadKind func(kind string) bool, quietStartup bool) *issuesapi.ClusterScopedChanges {
	if since <= 0 {
		since = DefaultSince
	}
	out := &issuesapi.ClusterScopedChanges{Kinds: slices.Clone(clusterScopedKinds), WindowSeconds: int(since.Seconds()), Changes: []issuesapi.ClusterScopedChange{}}
	var incomplete []string
	var readable []string
	for _, kind := range clusterScopedKinds {
		if canReadKind == nil || canReadKind(kind) {
			readable = append(readable, kind)
		} else {
			incomplete = append(incomplete, kind+" changes are not readable by the caller.")
		}
	}
	var startup []string
	defer func() {
		if len(incomplete) > 0 || !quietStartup {
			incomplete = append(incomplete, startup...)
		}
		out.Incomplete = strings.Join(incomplete, " ")
	}()
	if len(readable) == 0 {
		return out
	}
	store := timeline.GetStore()
	if store == nil {
		out.Error = timeline.StoreMissingError().Error()
		return out
	}
	windowStart := time.Now().Add(-since)
	if start := timeline.ObservationStart(); !start.IsZero() && start.After(windowStart) {
		startup = append(startup, fmt.Sprintf("Radar has recorded changes only since %s; earlier changes in the window are unknown.", start.UTC().Format(time.RFC3339)))
	}
	// Eviction is a gap of its own: a recent start does not explain history
	// recorded since and then dropped. The eviction boundary can date any
	// namespace or kind, so its time is not shown.
	if timeline.HistoryEvictedThrough().After(windowStart) {
		incomplete = append(incomplete, "Radar has evicted older change history; earlier changes in the window are unknown.")
	}
	q := normalizeQuery(Query{Kinds: readable, Since: since, start: windowStart})
	var events []timeline.TimelineEvent
	for _, eventTypes := range [][]timeline.EventType{updateEventTypes, lifecycleEventTypes} {
		got, full, err := queryCandidates(ctx, store, q, q.Kinds, eventTypes, FetchBound)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		events = append(events, got...)
		out.Truncated = out.Truncated || full
	}
	merged := coalesceRecreatePairs(dedupeEvents(events))
	// Applicability is decided per changed webhook, so keep every recorded
	// webhook row and cap fields only for display.
	changes, _ := rankedChanges(merged, Query{Limit: len(merged), FieldLimit: clusterScopedFieldLimit})
	current, loaded, watchedSince := currentWebhookConfigurations()
	for _, kind := range readable {
		switch {
		case !loaded[kind]:
			incomplete = append(incomplete, fmt.Sprintf("Current %ss are not cached, so their scope could not be checked; their changes are listed as may_apply.", kind))
		case watchedSince[kind].After(windowStart):
			startup = append(startup, fmt.Sprintf("Radar has watched %ss only since %s; earlier changes in the window are unknown.", kind, watchedSince[kind].UTC().Format(time.RFC3339)))
		}
	}
	for _, kind := range readable {
		if failed := timeline.LastStoreFailure(kind); failed.After(windowStart) {
			incomplete = append(incomplete, fmt.Sprintf("Radar failed to record a %s change at %s; changes in the window may be missing.", kind, failed.UTC().Format(time.RFC3339)))
		}
	}
	var applicable []issuesapi.ClusterScopedChange
	for _, change := range changes {
		if change.Namespace != "" {
			continue
		}
		applicability, because := webhookChangeApplicability(change, namespace, nsLabels, current, loaded[change.Kind])
		if applicability == "" {
			continue
		}
		applicable = append(applicable, issuesapi.ClusterScopedChange{RecentChange: change, Applicability: applicability, AppliesBecause: because})
	}
	for idx, change := range applicable {
		switch {
		case idx < clusterScopedRowLimit:
			if len(change.Fields) > DefaultFieldLimit {
				change.Fields = change.Fields[:DefaultFieldLimit]
			}
			out.Changes = append(out.Changes, change)
		case idx < clusterScopedNamedLimit:
			out.NotListed = append(out.NotListed, fmt.Sprintf("%s %s (%s at %s)", change.Kind, change.Name, change.ChangeType, change.Timestamp))
		default:
			out.Truncated = true
		}
	}
	return out
}

type webhookScope struct {
	name            string
	namespace       *metav1.LabelSelector
	objectSelector  bool
	matchConditions bool
	namespaced      bool
	// scopeUnknown: no rule is known to reach namespaced objects, but some
	// rule names resources whose scope could not be established.
	scopeUnknown bool
}

// currentWebhookConfigurations returns the cached webhook scopes of each kind
// whose cache is synced, and since when each has been watched; a kind missing
// from loaded was not checked.
func currentWebhookConfigurations() (map[string][]webhookScope, map[string]bool, map[string]time.Time) {
	out := make(map[string][]webhookScope)
	loaded := make(map[string]bool)
	watchedSince := make(map[string]time.Time)
	cache, discovery := k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()
	if cache == nil || discovery == nil {
		return out, loaded, watchedSince
	}
	for _, kind := range clusterScopedKinds {
		gvr, ok := discovery.GetGVRWithGroup(kind, "admissionregistration.k8s.io")
		if !ok || !cache.IsClusterWideSynced(gvr) {
			continue
		}
		items, err := cache.ListWatched(gvr)
		if err != nil {
			continue
		}
		loaded[kind] = true
		if since, ok := cache.WatchedSince(gvr); ok {
			watchedSince[kind] = since
		}
		for _, item := range items {
			out[kind+"/"+item.GetName()] = webhookScopes(kind, item)
		}
	}
	return out, loaded, watchedSince
}

func webhookScopes(kind string, item *unstructured.Unstructured) []webhookScope {
	var scopes []webhookScope
	add := func(name string, ns, obj *metav1.LabelSelector, rules []admissionv1.RuleWithOperations, conditions int) {
		scope := webhookScope{name: name, namespace: ns, matchConditions: conditions > 0,
			objectSelector: obj != nil && (len(obj.MatchLabels) > 0 || len(obj.MatchExpressions) > 0)}
		for _, rule := range rules {
			namespaced, unknown := ruleScope(rule)
			scope.namespaced = scope.namespaced || namespaced
			scope.scopeUnknown = scope.scopeUnknown || unknown
		}
		scope.scopeUnknown = scope.scopeUnknown && !scope.namespaced
		scopes = append(scopes, scope)
	}
	if kind == "MutatingWebhookConfiguration" {
		var config admissionv1.MutatingWebhookConfiguration
		if runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &config) != nil {
			return nil
		}
		for _, w := range config.Webhooks {
			add(w.Name, w.NamespaceSelector, w.ObjectSelector, w.Rules, len(w.MatchConditions))
		}
		return scopes
	}
	var config admissionv1.ValidatingWebhookConfiguration
	if runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &config) != nil {
		return nil
	}
	for _, w := range config.Webhooks {
		add(w.Name, w.NamespaceSelector, w.ObjectSelector, w.Rules, len(w.MatchConditions))
	}
	return scopes
}

// ruleScope reports whether a rule can match a namespaced object, and
// whether that could not be established. The rule's scope narrows the
// resources it lists but never widens them: a rule naming only cluster-scoped
// resources (nodes, say) reaches no namespace whatever its scope. A wildcard
// group is judged by the core group (Gatekeeper's ["*"]/["*"] covers Pods); a
// wildcard resource in a named non-core group, or a resource discovery cannot
// place, leaves the answer unknown.
func ruleScope(rule admissionv1.RuleWithOperations) (namespaced, unknown bool) {
	if rule.Scope != nil && *rule.Scope == admissionv1.ClusterScope {
		return false, false
	}
	discovery := k8s.GetResourceDiscovery()
	for _, resource := range rule.Resources {
		base, _, _ := strings.Cut(resource, "/")
		for _, group := range rule.APIGroups {
			switch {
			case base == "*" && (group == "" || group == "*"):
				// Both include the core group, which holds Pods.
				return true, false
			case base == "*" || discovery == nil:
				unknown = true
				continue
			case group == "*":
				// A named resource under every group: the core group decides
				// for the resources that matter most here (Pods, Services…).
				group = ""
			}
			info, ok := discovery.GetResourceWithGroup(base, group)
			switch {
			case !ok:
				unknown = true
			case info.Namespaced:
				return true, false
			}
		}
	}
	return false, unknown
}

// webhookChangeApplicability decides whether a webhook configuration change
// belongs in namespace's view. The current configuration is evaluated against
// the namespace's labels; when that excludes the namespace but the change
// itself removed or re-scoped webhooks, the earlier scope is unknown (the
// timeline keeps a diff summary, not the old selectors), so it may have.
// Returns "" when the change does not concern the namespace.
func webhookChangeApplicability(change issuesapi.RecentChange, namespace string, nsLabels NamespaceLabels, current map[string][]webhookScope, loaded bool) (issuesapi.ClusterScopedApplicability, string) {
	if change.ChangeType == string(timeline.EventTypeDelete) {
		return issuesapi.ClusterScopedMayApply, "the configuration was deleted; its scope before deletion is not recorded"
	}
	if !loaded {
		return issuesapi.ClusterScopedMayApply, "current webhook configurations are not cached, so scope was not checked"
	}
	scopes, exists := current[change.Kind+"/"+change.Name]
	if !exists {
		return issuesapi.ClusterScopedMayApply, "the configuration no longer exists; its earlier scope is not recorded"
	}
	changed, rescoped := changedWebhooks(change)
	// Only the webhooks the change itemized are judged. With none itemized (a
	// create, or an update to fields the differ does not itemize) every current
	// webhook is, but an update can then only say may_apply: an unchanged
	// webhook selecting the namespace does not make the edit relevant to it.
	unitemizedUpdate := len(changed) == 0 && change.ChangeType == string(timeline.EventTypeUpdate)
	diffCut := len(change.Fields) >= clusterScopedFieldLimit
	var bestApplies, bestMay, bestName string
	for _, scope := range scopes {
		if len(changed) > 0 && !changed[scope.name] {
			continue
		}
		if !scope.namespaced && !scope.scopeUnknown {
			continue
		}
		selectsNamespace, known := selectorMatches(scope.namespace, nsLabels)
		switch {
		case !known:
			if bestMay == "" {
				bestMay = fmt.Sprintf("webhook %s has a namespaceSelector and namespace %s's labels are not readable", scope.name, namespace)
				if bestApplies == "" {
					bestName = scope.name
				}
			}
		case !selectsNamespace:
		case scope.objectSelector || scope.matchConditions:
			bestMay = fmt.Sprintf("webhook %s selects namespace %s; its %s not evaluated", scope.name, namespace, unevaluated(scope))
			if bestApplies == "" {
				bestName = scope.name
			}
		case scope.scopeUnknown:
			bestMay = fmt.Sprintf("webhook %s selects namespace %s; Radar could not confirm that its rules reach namespaced resources", scope.name, namespace)
			if bestApplies == "" {
				bestName = scope.name
			}
		default:
			if bestApplies == "" {
				bestApplies = fmt.Sprintf("webhook %s %s and its rules cover namespaced resources", scope.name, namespaceMatch(scope.namespace, namespace))
				bestName = scope.name
			}
		}
	}
	var also []string
	for _, name := range rescoped {
		if name != bestName {
			also = append(also, name)
		}
	}
	rescopedNote := ""
	if len(also) > 0 {
		rescopedNote = fmt.Sprintf("; this change also removed or re-scoped webhook %s, whose earlier scope is not recorded", strings.Join(also, ", "))
	}
	cutNote := ""
	if diffCut {
		cutNote = "; the timeline diff lists only part of this change's webhooks"
	}
	switch {
	case bestApplies != "" && unitemizedUpdate:
		return issuesapi.ClusterScopedMayApply, "this update changed fields the timeline does not itemize per webhook; currently " + bestApplies
	case bestApplies != "":
		return issuesapi.ClusterScopedApplies, bestApplies + rescopedNote + cutNote
	case bestMay != "":
		return issuesapi.ClusterScopedMayApply, bestMay + rescopedNote + cutNote
	case len(rescoped) > 0:
		return issuesapi.ClusterScopedMayApply, fmt.Sprintf("this change removed or re-scoped webhook %s; its earlier scope is not recorded", strings.Join(rescoped, ", ")) + cutNote
	case diffCut:
		return issuesapi.ClusterScopedMayApply, "this change touched more webhooks than the timeline diff lists, so a removed or re-scoped webhook may be missing"
	}
	return "", ""
}

// changedWebhooks reads the per-webhook diff rows the timeline records:
// webhooks[<name>] carries a summary of the fields webhook diffs compare, and
// webhooks[<name>].<field…> rows report any other changed field. rescoped
// lists webhooks the change removed or whose selectors, rules or match
// conditions it changed.
func changedWebhooks(change issuesapi.RecentChange) (map[string]bool, []string) {
	changed := make(map[string]bool)
	var rescoped []string
	note := func(path string, oldValue, newValue any) {
		rest, ok := strings.CutPrefix(path, "webhooks[")
		if !ok {
			return
		}
		name, subpath, ok := strings.Cut(rest, "]")
		if !ok || name == "" {
			return
		}
		changed[name] = true
		var scope bool
		if subpath == "" {
			oldSummary, hadOld := oldValue.(string)
			newSummary, hasNew := newValue.(string)
			scope = hadOld && (!hasNew || webhookScopeSummary(oldSummary) != webhookScopeSummary(newSummary))
		} else {
			for _, prefix := range []string{".matchConditions", ".namespaceSelector", ".objectSelector", ".rules"} {
				scope = scope || strings.HasPrefix(subpath, prefix)
			}
		}
		if scope && !slices.Contains(rescoped, name) {
			rescoped = append(rescoped, name)
		}
	}
	for _, field := range change.Fields {
		// Past its row cap the differ lists the remaining changed paths, without
		// values, in one overflow row.
		if field.Path == "webhooks (more fields)" {
			for _, path := range overflowPaths(field.NewValue) {
				note(path, nil, nil)
			}
			continue
		}
		note(field.Path, field.OldValue, field.NewValue)
	}
	return changed, rescoped
}

func overflowPaths(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if path, ok := item.(string); ok {
				out = append(out, path)
			}
		}
		return out
	}
	return nil
}

// webhookScopeSummary keeps the scope part of the timeline's webhook summary,
// which the history differ writes last: selectors and rules.
func webhookScopeSummary(summary string) string {
	start := len(summary)
	for _, marker := range []string{"namespaceSelector=", "objectSelector=", "rules="} {
		if idx := strings.Index(summary, marker); idx >= 0 && idx < start {
			start = idx
		}
	}
	return summary[start:]
}

func selectorMatches(selector *metav1.LabelSelector, nsLabels NamespaceLabels) (matches, known bool) {
	if selector == nil || (len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0) {
		return true, true
	}
	if !nsLabels.Known {
		return false, false
	}
	compiled, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return false, false
	}
	return compiled.Matches(nsLabels.Labels), true
}

func namespaceMatch(selector *metav1.LabelSelector, namespace string) string {
	if selector == nil || (len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0) {
		return "has no namespaceSelector"
	}
	return fmt.Sprintf("has a namespaceSelector matching namespace %s's labels", namespace)
}

func unevaluated(scope webhookScope) string {
	switch {
	case scope.objectSelector && scope.matchConditions:
		return "objectSelector and matchConditions are"
	case scope.objectSelector:
		return "objectSelector is"
	}
	return "matchConditions are"
}
