package k8s

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
)

// maxConditionMessageRunes bounds each recorded condition or check message.
// Kueue's own messages fit; a longer one is cut rather than stored in full on
// every transition row.
const maxConditionMessageRunes = 300

// kueueStatusCounters are Workload status fields the differ leaves out: they
// count or total things another recorded field already marks the moment of
// (schedulingStats.evictions moves with the Evicted condition), or mirror the
// spec (resourceRequests), whose edits the spec backstop records.
var kueueStatusCounters = []string{"schedulingStats", "accumulatedPastExecutionTimeSeconds", "resourceRequests"}

// diffKueueWorkload records a Kueue Workload's admission story: condition
// transitions with their messages, the admission assignment (ClusterQueue,
// and per PodSet its flavors, count and usage), admission check states,
// requeue scheduling, deactivation and queue changes. Every other status
// field is reported by path, so a field a newer Kueue adds still records.
// Two kinds of update record nothing: a counter-only update, and a condition
// message rewritten with only its quantities changed (Kueue rewrites a
// pending Workload's "N more needed" on scheduling attempts).
func diffKueueWorkload(oldObj, newObj any) ([]FieldChange, []string) {
	oldU, newU, ok := unstructuredPair(oldObj, newObj)
	if !ok {
		warnUnstructuredAssertFailed("Workload", newObj)
		return nil, nil
	}
	var d statusDiff
	d.deletion(oldU, newU)
	d.workloadSpec(oldU, newU)
	d.metadataMap("labels", oldU.GetLabels(), newU.GetLabels(), "kueue.x-k8s.io/")
	d.metadataMap("annotations", oldU.GetAnnotations(), newU.GetAnnotations(), "kueue.x-k8s.io/")
	d.conditions(oldU, newU, kueueConditionSummary(newU))
	d.admission(oldU, newU)
	d.admissionChecks(oldU, newU)
	d.requeueState(oldU, newU)
	d.otherStatus(oldU, newU, append([]string{"conditions", "admission", "admissionChecks", "requeueState"}, kueueStatusCounters...))
	return d.changes, d.summary
}

// diffProvisioningRequest records a ProvisioningRequest's condition
// transitions (Accepted, Provisioned, Failed, BookingExpired,
// CapacityRevoked) and any other status change by path.
func diffProvisioningRequest(oldObj, newObj any) ([]FieldChange, []string) {
	oldU, newU, ok := unstructuredPair(oldObj, newObj)
	if !ok {
		warnUnstructuredAssertFailed("ProvisioningRequest", newObj)
		return nil, nil
	}
	var d statusDiff
	d.deletion(oldU, newU)
	d.conditions(oldU, newU, genericConditionSummary)
	d.otherStatus(oldU, newU, []string{"conditions"})
	return d.changes, d.summary
}

type statusDiff struct {
	changes []FieldChange
	summary []string
}

func (d *statusDiff) add(path string, oldV, newV any) {
	d.changes = append(d.changes, FieldChange{Path: path, OldValue: oldV, NewValue: newV})
}

func (d *statusDiff) deletion(oldU, newU *unstructured.Unstructured) {
	if oldU.GetDeletionTimestamp() == nil && newU.GetDeletionTimestamp() != nil {
		d.add("metadata.deletionTimestamp", nil, newU.GetDeletionTimestamp().UTC().Format(time.RFC3339))
		d.summary = append(d.summary, "deletion started")
	}
}

func (d *statusDiff) workloadSpec(oldU, newU *unstructured.Unstructured) {
	oldActive, newActive := workloadActive(oldU), workloadActive(newU)
	if oldActive != newActive {
		d.add("spec.active", oldActive, newActive)
		if newActive {
			d.summary = append(d.summary, "reactivated")
		} else {
			d.summary = append(d.summary, "deactivated")
		}
	}
	oldQueue, oldOK, _ := unstructured.NestedString(oldU.Object, "spec", "queueName")
	newQueue, newOK, _ := unstructured.NestedString(newU.Object, "spec", "queueName")
	if oldQueue != newQueue || oldOK != newOK {
		d.add("spec.queueName", optionalString(oldQueue, oldOK), optionalString(newQueue, newOK))
		d.summary = append(d.summary, fmt.Sprintf("queue: %s → %s", orNone(oldQueue), orNone(newQueue)))
	}
}

// workloadActive reads spec.active, which defaults to true.
func workloadActive(u *unstructured.Unstructured) bool {
	active, found, _ := unstructured.NestedBool(u.Object, "spec", "active")
	return !found || active
}

// metadataMap records changes to the labels or annotations under prefix:
// Kueue's own keys (admission gating, queue name, priority class).
func (d *statusDiff) metadataMap(field string, oldA, newA map[string]string, prefix string) {
	keys := map[string]bool{}
	for k := range oldA {
		keys[k] = true
	}
	for k := range newA {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		if strings.HasPrefix(k, prefix) {
			sorted = append(sorted, k)
		}
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		oldV, oldOK := oldA[k]
		newV, newOK := newA[k]
		if oldOK == newOK && oldV == newV {
			continue
		}
		d.add("metadata."+field+"["+k+"]", optionalString(boundMessage(oldV), oldOK), optionalString(boundMessage(newV), newOK))
		d.summary = append(d.summary, fmt.Sprintf("%s %s %s", strings.TrimSuffix(field, "s"), k, mapKeyVerb(oldOK, newOK)))
	}
}

type observedCondition struct {
	status, reason, message string
}

func conditionsByType(u *unstructured.Unstructured) (map[string]observedCondition, []string) {
	items, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	out := make(map[string]observedCondition, len(items))
	var order []string
	for _, item := range items {
		cond, ok := item.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := cond["type"].(string)
		if typ == "" {
			continue
		}
		status, _ := cond["status"].(string)
		reason, _ := cond["reason"].(string)
		message, _ := cond["message"].(string)
		if _, seen := out[typ]; !seen {
			order = append(order, typ)
		}
		out[typ] = observedCondition{status: status, reason: reason, message: message}
	}
	return out, order
}

func conditionValue(c observedCondition, ok bool) any {
	if !ok {
		return nil
	}
	if c.reason == "" {
		return c.status
	}
	return fmt.Sprintf("%s (%s)", c.status, c.reason)
}

// conditionSummary phrases one condition's transition for the row summary.
type conditionSummary func(typ string, old observedCondition, oldOK bool, cur observedCondition, curOK bool) string

func genericConditionSummary(typ string, _ observedCondition, _ bool, cur observedCondition, curOK bool) string {
	if !curOK {
		return typ + " condition removed"
	}
	if cur.reason == "" {
		return fmt.Sprintf("%s=%s", typ, cur.status)
	}
	return fmt.Sprintf("%s=%s (%s)", typ, cur.status, cur.reason)
}

// kueueConditionSummary names the transitions of Kueue's own condition types
// in Kueue's terms; any other type, and every False transition, reads as
// Type=Status (Reason).
func kueueConditionSummary(cur *unstructured.Unstructured) conditionSummary {
	clusterQueue, _, _ := unstructured.NestedString(cur.Object, "status", "admission", "clusterQueue")
	return func(typ string, old observedCondition, oldOK bool, c observedCondition, ok bool) string {
		if !ok || c.status != "True" {
			return genericConditionSummary(typ, old, oldOK, c, ok)
		}
		withReason := func(text string) string {
			if c.reason == "" || c.reason == typ {
				return text
			}
			return fmt.Sprintf("%s (%s)", text, c.reason)
		}
		switch typ {
		case "QuotaReserved":
			if clusterQueue != "" {
				return "quota reserved in ClusterQueue " + clusterQueue
			}
			return "quota reserved"
		case "Admitted":
			return withReason("admitted")
		case "Evicted":
			return withReason("evicted")
		case "Preempted":
			return withReason("preempted")
		case "Requeued":
			return withReason("requeued")
		case "PodsReady":
			return withReason("pods ready")
		case "Finished":
			return withReason("finished")
		}
		return genericConditionSummary(typ, old, oldOK, c, ok)
	}
}

func (d *statusDiff) conditions(oldU, newU *unstructured.Unstructured, summarize conditionSummary) {
	oldC, oldOrder := conditionsByType(oldU)
	newC, newOrder := conditionsByType(newU)
	types := newOrder
	for _, typ := range oldOrder {
		if _, ok := newC[typ]; !ok {
			types = append(types, typ)
		}
	}
	for _, typ := range types {
		o, oldOK := oldC[typ]
		n, newOK := newC[typ]
		path := "status.conditions[" + typ + "]"
		signalChanged := oldOK != newOK || o.status != n.status || o.reason != n.reason
		messageChanged := o.message != n.message
		if signalChanged {
			d.add(path, conditionValue(o, oldOK), conditionValue(n, newOK))
			d.summary = append(d.summary, summarize(typ, o, oldOK, n, newOK))
		}
		if !messageChanged {
			continue
		}
		if !signalChanged && maskQuantities(o.message) == maskQuantities(n.message) {
			continue
		}
		d.add(path+".message", optionalMessage(o.message, oldOK), optionalMessage(n.message, newOK))
		if !signalChanged {
			d.summary = append(d.summary, typ+" message changed")
		}
	}
}

func (d *statusDiff) admission(oldU, newU *unstructured.Unstructured) {
	oldCQ, _, _ := unstructured.NestedString(oldU.Object, "status", "admission", "clusterQueue")
	newCQ, _, _ := unstructured.NestedString(newU.Object, "status", "admission", "clusterQueue")
	reported := false
	if oldCQ != newCQ {
		d.add("status.admission.clusterQueue", optionalString(oldCQ, oldCQ != ""), optionalString(newCQ, newCQ != ""))
		reported = true
	}
	oldA, oldOrder := podSetAssignments(oldU)
	newA, newOrder := podSetAssignments(newU)
	names := newOrder
	for _, name := range oldOrder {
		if _, ok := newA[name]; !ok {
			names = append(names, name)
		}
	}
	for _, name := range names {
		o, oldOK := oldA[name]
		n, newOK := newA[name]
		if oldOK == newOK && o == n {
			continue
		}
		d.add("status.admission.podSetAssignments["+name+"]", optionalString(o, oldOK), optionalString(n, newOK))
		reported = true
	}
	// QuotaReserved's transition already names the ClusterQueue; a change of
	// assignment without one (a flavor reassignment) gets its own summary.
	if reported && !slices.ContainsFunc(d.summary, func(s string) bool {
		return strings.HasPrefix(s, "quota reserved") || strings.HasPrefix(s, "QuotaReserved=")
	}) {
		switch {
		case newCQ == "" && oldCQ != "":
			d.summary = append(d.summary, "admission assignment cleared")
		case oldCQ != newCQ && oldCQ != "":
			d.summary = append(d.summary, fmt.Sprintf("ClusterQueue: %s → %s", oldCQ, newCQ))
		default:
			d.summary = append(d.summary, "admission assignment changed")
		}
	}
}

// podSetAssignments renders each PodSet's assignment as one stable string:
// count, then each resource's flavor and usage, sorted by resource.
func podSetAssignments(u *unstructured.Unstructured) (map[string]string, []string) {
	items, _, _ := unstructured.NestedSlice(u.Object, "status", "admission", "podSetAssignments")
	out := make(map[string]string, len(items))
	var order []string
	for _, item := range items {
		ps, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := ps["name"].(string)
		if name == "" {
			continue
		}
		flavors, _ := ps["flavors"].(map[string]any)
		usage, _ := ps["resourceUsage"].(map[string]any)
		resources := make([]string, 0, len(flavors)+len(usage))
		for r := range flavors {
			resources = append(resources, r)
		}
		for r := range usage {
			if _, ok := flavors[r]; !ok {
				resources = append(resources, r)
			}
		}
		sort.Strings(resources)
		parts := make([]string, 0, len(resources)+1)
		if count, ok := ps["count"]; ok && count != nil {
			parts = append(parts, fmt.Sprintf("count %v", count))
		}
		for _, r := range resources {
			part := r
			if flavor, ok := flavors[r].(string); ok && flavor != "" {
				part += "=" + flavor
			}
			if q, ok := usage[r]; ok && q != nil {
				part += fmt.Sprintf(" (%v)", q)
			}
			parts = append(parts, part)
		}
		if _, seen := out[name]; !seen {
			order = append(order, name)
		}
		out[name] = strings.Join(parts, "; ")
	}
	return out, order
}

type observedCheck struct {
	state, message string
	retries        int64
}

func admissionChecks(u *unstructured.Unstructured) (map[string]observedCheck, []string) {
	items, _, _ := unstructured.NestedSlice(u.Object, "status", "admissionChecks")
	out := make(map[string]observedCheck, len(items))
	var order []string
	for _, item := range items {
		check, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := check["name"].(string)
		if name == "" {
			continue
		}
		state, _ := check["state"].(string)
		message, _ := check["message"].(string)
		retries, _, _ := unstructured.NestedInt64(check, "retryCount")
		if _, seen := out[name]; !seen {
			order = append(order, name)
		}
		out[name] = observedCheck{state: state, message: message, retries: retries}
	}
	return out, order
}

func checkValue(c observedCheck, ok bool) any {
	if !ok {
		return nil
	}
	if c.retries > 0 {
		return fmt.Sprintf("%s (retry %d)", c.state, c.retries)
	}
	return c.state
}

func (d *statusDiff) admissionChecks(oldU, newU *unstructured.Unstructured) {
	oldC, oldOrder := admissionChecks(oldU)
	newC, newOrder := admissionChecks(newU)
	names := newOrder
	for _, name := range oldOrder {
		if _, ok := newC[name]; !ok {
			names = append(names, name)
		}
	}
	for _, name := range names {
		o, oldOK := oldC[name]
		n, newOK := newC[name]
		path := "status.admissionChecks[" + name + "]"
		stateChanged := oldOK != newOK || o.state != n.state || o.retries != n.retries
		if stateChanged {
			d.add(path, checkValue(o, oldOK), checkValue(n, newOK))
			switch {
			case !newOK:
				d.summary = append(d.summary, "check "+name+" removed")
			case !oldOK:
				d.summary = append(d.summary, fmt.Sprintf("check %s: %s", name, n.state))
			case o.state == n.state:
				d.summary = append(d.summary, fmt.Sprintf("check %s: %s (retry %d)", name, n.state, n.retries))
			default:
				d.summary = append(d.summary, fmt.Sprintf("check %s: %s → %s", name, o.state, n.state))
			}
		}
		if o.message == n.message || (!stateChanged && maskQuantities(o.message) == maskQuantities(n.message)) {
			continue
		}
		d.add(path+".message", optionalMessage(o.message, oldOK && o.message != ""), optionalMessage(n.message, newOK && n.message != ""))
		if !stateChanged {
			d.summary = append(d.summary, "check "+name+" message changed")
		}
	}
}

func (d *statusDiff) requeueState(oldU, newU *unstructured.Unstructured) {
	oldV, oldOK := requeueStateValue(oldU)
	newV, newOK := requeueStateValue(newU)
	if oldOK == newOK && oldV == newV {
		return
	}
	d.add("status.requeueState", optionalString(oldV, oldOK), optionalString(newV, newOK))
	at, _, _ := unstructured.NestedString(newU.Object, "status", "requeueState", "requeueAt")
	switch {
	case !newOK:
		d.summary = append(d.summary, "requeue state cleared")
	case at != "":
		d.summary = append(d.summary, "requeue scheduled for "+at)
	default:
		d.summary = append(d.summary, "requeue state changed")
	}
}

func requeueStateValue(u *unstructured.Unstructured) (string, bool) {
	state, found, _ := unstructured.NestedMap(u.Object, "status", "requeueState")
	if !found || len(state) == 0 {
		return "", false
	}
	var parts []string
	if count, ok := state["count"]; ok && count != nil {
		parts = append(parts, fmt.Sprintf("count %v", count))
	}
	if at, ok := state["requeueAt"].(string); ok && at != "" {
		parts = append(parts, "requeue at "+at)
	}
	return strings.Join(parts, ", "), true
}

// otherStatus reports every status field not handled above or left out on
// purpose, by path, with values shown under the backstop's rules.
func (d *statusDiff) otherStatus(oldU, newU *unstructured.Unstructured, handled []string) {
	oldS, _, _ := unstructured.NestedMap(oldU.Object, "status")
	newS, _, _ := unstructured.NestedMap(newU.Object, "status")
	var found []changedSpecPath
	for _, key := range unionKeys(oldS, newS) {
		if slices.Contains(handled, key) || isTimelineNoiseKey(key) {
			continue
		}
		collectChangedPaths("status."+key, stripTimelineNoise(oldS[key]), stripTimelineNoise(newS[key]), "", func(string) bool { return false }, &found)
	}
	if len(found) == 0 {
		return
	}
	found = collapseUntracked(found, nil, nil)
	changes, _ := untrackedSpecRows("status", found)
	d.changes = append(d.changes, changes...)
	labels := make([]string, 0, len(found))
	for _, c := range found {
		labels = append(labels, strings.TrimPrefix(c.path, "status."))
	}
	if len(labels) > maxUntrackedSummary {
		labels = append(labels[:maxUntrackedSummary], fmt.Sprintf("+%d more", len(found)-maxUntrackedSummary))
	}
	d.summary = append(d.summary, "other status changes: "+strings.Join(labels, ", "))
}

// stripTimelineNoise drops heartbeat-like keys (lastTransitionTime,
// observedGeneration…) inside a status block, so a timestamp refresh alone
// isn't a change.
func stripTimelineNoise(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, child := range typed {
			if !isTimelineNoiseKey(k) {
				out[k] = stripTimelineNoise(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = stripTimelineNoise(child)
		}
		return out
	default:
		return v
	}
}

// kueueQuantitySlots are where Kueue's messages carry amounts that change
// from one scheduling attempt to the next: parenthesized quantities
// ("current podset request (2)", "maximum capacity (1200m)") and the
// shortfall ("50m more needed"). Names (flavors, resources, queues) are
// never masked, even when they contain digits.
var kueueQuantitySlots = []struct {
	pattern *regexp.Regexp
	repl    string
}{
	{regexp.MustCompile(`\(\d[\d.]*[A-Za-z]*\)`), "(#)"},
	{regexp.MustCompile(`\b\d[\d.]*[A-Za-z]*( more needed)`), "#$1"},
}

func maskQuantities(message string) string {
	for _, slot := range kueueQuantitySlots {
		message = slot.pattern.ReplaceAllString(message, slot.repl)
	}
	return message
}

// boundMessage redacts, cuts and cleans a message for storage: Postgres jsonb
// rejects NUL and invalid UTF-8, and one bad field would drop the whole row.
func boundMessage(message string) string {
	message = strings.ToValidUTF8(strings.ReplaceAll(message, "\x00", ""), "\uFFFD")
	message = aicontext.RedactSecrets(message)
	runes := []rune(message)
	if len(runes) <= maxConditionMessageRunes {
		return message
	}
	return string(runes[:maxConditionMessageRunes-1]) + "…"
}

func optionalMessage(message string, present bool) any {
	if !present {
		return nil
	}
	return boundMessage(message)
}

func optionalString(v string, present bool) any {
	if !present {
		return nil
	}
	return v
}

func orNone(v string) string {
	if v == "" {
		return "(none)"
	}
	return v
}

// kueueHistoricalEvents reconstructs what a Workload's or
// ProvisioningRequest's status can date when Radar first sees it: when it was
// created, and when each condition and admission check last changed state. A
// lastTransitionTime dates a status change only; the condition's current
// reason and message may have been written later, so the rows carry the
// status alone.
func kueueHistoricalEvents(u *unstructured.Unstructured) []historicalTransition {
	var out []historicalTransition
	items, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, item := range items {
		cond, ok := item.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := cond["type"].(string)
		status, _ := cond["status"].(string)
		at, ok := parseTransitionTime(cond["lastTransitionTime"])
		if typ == "" || !ok {
			continue
		}
		out = append(out, historicalTransition{at: at, reason: typ + "=" + status})
	}
	checks, _, _ := unstructured.NestedSlice(u.Object, "status", "admissionChecks")
	for _, item := range checks {
		check, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := check["name"].(string)
		state, _ := check["state"].(string)
		at, ok := parseTransitionTime(check["lastTransitionTime"])
		if name == "" || !ok {
			continue
		}
		out = append(out, historicalTransition{at: at, reason: "Check " + name + "=" + state})
	}
	return out
}

type historicalTransition struct {
	at     time.Time
	reason string
}

func parseTransitionTime(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil || t.IsZero() {
		return time.Time{}, false
	}
	return t, true
}
