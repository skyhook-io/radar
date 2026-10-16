package k8s

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/rolloutdiag"
)

const (
	restartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"
	maxUntrackedSpecRows  = 10
	maxUntrackedSummary   = 3
)

type specChangeSet struct {
	changes []FieldChange
	summary []string
}

func (s *specChangeSet) add(path, label string, oldVal, newVal any) {
	s.addRow(path, label, oldVal, newVal, true)
}

// addRow with summarize=false keeps the field row without a summary entry —
// for rolling-update parameters that stop mattering once the strategy type
// leaves RollingUpdate.
func (s *specChangeSet) addRow(path, label string, oldVal, newVal any, summarize bool) {
	if reflect.DeepEqual(oldVal, newVal) {
		return
	}
	s.changes = append(s.changes, FieldChange{Path: path, OldValue: oldVal, NewValue: newVal})
	if summarize {
		s.summary = append(s.summary, fmt.Sprintf("%s: %s→%s", label, specDisplay(oldVal), specDisplay(newVal)))
	}
}

func specDisplay(v any) string {
	if v == nil || v == "" {
		return "(default)"
	}
	return fmt.Sprint(v)
}

// Pointer and IntOrString fields become nil when unset so the row shows the
// field as defaulted rather than as an explicit zero.
func int32PtrValue(p *int32) any {
	if p == nil {
		return nil
	}
	return *p
}

func intOrStringValue(p *intstr.IntOrString) any {
	if p == nil {
		return nil
	}
	return p.String()
}

func diffDeploymentRolloutConfig(oldDep, newDep *appsv1.Deployment) ([]FieldChange, []string) {
	var s specChangeSet
	oldSpec, newSpec := oldDep.Spec, newDep.Spec
	s.add("spec.strategy.type", "strategy", string(oldSpec.Strategy.Type), string(newSpec.Strategy.Type))
	var oldRU, newRU appsv1.RollingUpdateDeployment
	if oldSpec.Strategy.RollingUpdate != nil {
		oldRU = *oldSpec.Strategy.RollingUpdate
	}
	if newSpec.Strategy.RollingUpdate != nil {
		newRU = *newSpec.Strategy.RollingUpdate
	}
	rolling := newSpec.Strategy.Type != appsv1.RecreateDeploymentStrategyType
	s.addRow("spec.strategy.rollingUpdate.maxUnavailable", "maxUnavailable", intOrStringValue(oldRU.MaxUnavailable), intOrStringValue(newRU.MaxUnavailable), rolling)
	s.addRow("spec.strategy.rollingUpdate.maxSurge", "maxSurge", intOrStringValue(oldRU.MaxSurge), intOrStringValue(newRU.MaxSurge), rolling)
	s.add("spec.minReadySeconds", "minReadySeconds", oldSpec.MinReadySeconds, newSpec.MinReadySeconds)
	s.add("spec.progressDeadlineSeconds", "progressDeadlineSeconds", int32PtrValue(oldSpec.ProgressDeadlineSeconds), int32PtrValue(newSpec.ProgressDeadlineSeconds))
	s.add("spec.revisionHistoryLimit", "revisionHistoryLimit", int32PtrValue(oldSpec.RevisionHistoryLimit), int32PtrValue(newSpec.RevisionHistoryLimit))

	// The strategy values alone don't say whether they are dangerous for this
	// replica count; name the consequence when the update newly allows it.
	// Recreate already replaced every pod at once, so leaving it adds no risk.
	if risk := rolloutdiag.Analyze(newDep); risk != nil && rolloutdiag.Analyze(oldDep) == nil && oldSpec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		s.summary = append(s.summary, fmt.Sprintf("rollout can now take all %d replicas unavailable at once", risk.Replicas))
	}
	return s.changes, s.summary
}

func diffStatefulSetRolloutConfig(oldSpec, newSpec appsv1.StatefulSetSpec) ([]FieldChange, []string) {
	var s specChangeSet
	s.add("spec.updateStrategy.type", "updateStrategy", string(oldSpec.UpdateStrategy.Type), string(newSpec.UpdateStrategy.Type))
	var oldRU, newRU appsv1.RollingUpdateStatefulSetStrategy
	if oldSpec.UpdateStrategy.RollingUpdate != nil {
		oldRU = *oldSpec.UpdateStrategy.RollingUpdate
	}
	if newSpec.UpdateStrategy.RollingUpdate != nil {
		newRU = *newSpec.UpdateStrategy.RollingUpdate
	}
	rolling := newSpec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType
	s.addRow("spec.updateStrategy.rollingUpdate.partition", "partition", int32PtrValue(oldRU.Partition), int32PtrValue(newRU.Partition), rolling)
	s.addRow("spec.updateStrategy.rollingUpdate.maxUnavailable", "maxUnavailable", intOrStringValue(oldRU.MaxUnavailable), intOrStringValue(newRU.MaxUnavailable), rolling)
	s.add("spec.podManagementPolicy", "podManagementPolicy", string(oldSpec.PodManagementPolicy), string(newSpec.PodManagementPolicy))
	s.add("spec.minReadySeconds", "minReadySeconds", oldSpec.MinReadySeconds, newSpec.MinReadySeconds)
	s.add("spec.revisionHistoryLimit", "revisionHistoryLimit", int32PtrValue(oldSpec.RevisionHistoryLimit), int32PtrValue(newSpec.RevisionHistoryLimit))
	var oldRetention, newRetention appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy
	if oldSpec.PersistentVolumeClaimRetentionPolicy != nil {
		oldRetention = *oldSpec.PersistentVolumeClaimRetentionPolicy
	}
	if newSpec.PersistentVolumeClaimRetentionPolicy != nil {
		newRetention = *newSpec.PersistentVolumeClaimRetentionPolicy
	}
	s.add("spec.persistentVolumeClaimRetentionPolicy.whenDeleted", "pvcRetention.whenDeleted", string(oldRetention.WhenDeleted), string(newRetention.WhenDeleted))
	s.add("spec.persistentVolumeClaimRetentionPolicy.whenScaled", "pvcRetention.whenScaled", string(oldRetention.WhenScaled), string(newRetention.WhenScaled))
	return s.changes, s.summary
}

func diffDaemonSetRolloutConfig(oldSpec, newSpec appsv1.DaemonSetSpec) ([]FieldChange, []string) {
	var s specChangeSet
	s.add("spec.updateStrategy.type", "updateStrategy", string(oldSpec.UpdateStrategy.Type), string(newSpec.UpdateStrategy.Type))
	var oldRU, newRU appsv1.RollingUpdateDaemonSet
	if oldSpec.UpdateStrategy.RollingUpdate != nil {
		oldRU = *oldSpec.UpdateStrategy.RollingUpdate
	}
	if newSpec.UpdateStrategy.RollingUpdate != nil {
		newRU = *newSpec.UpdateStrategy.RollingUpdate
	}
	rolling := newSpec.UpdateStrategy.Type != appsv1.OnDeleteDaemonSetStrategyType
	s.addRow("spec.updateStrategy.rollingUpdate.maxUnavailable", "maxUnavailable", intOrStringValue(oldRU.MaxUnavailable), intOrStringValue(newRU.MaxUnavailable), rolling)
	s.addRow("spec.updateStrategy.rollingUpdate.maxSurge", "maxSurge", intOrStringValue(oldRU.MaxSurge), intOrStringValue(newRU.MaxSurge), rolling)
	s.add("spec.minReadySeconds", "minReadySeconds", oldSpec.MinReadySeconds, newSpec.MinReadySeconds)
	s.add("spec.revisionHistoryLimit", "revisionHistoryLimit", int32PtrValue(oldSpec.RevisionHistoryLimit), int32PtrValue(newSpec.RevisionHistoryLimit))
	return s.changes, s.summary
}

// diffPodTemplateMetadata covers pod-template labels and annotations. Changing
// either rolls the workload, and they carry common rollout triggers:
// `kubectl rollout restart` and config-checksum annotations from Helm charts.
func diffPodTemplateMetadata(oldMeta, newMeta metav1.ObjectMeta) ([]FieldChange, []string) {
	var changes []FieldChange
	var summary []string
	for _, key := range changedMapKeys(oldMeta.Annotations, newMeta.Annotations) {
		oldVal, oldOK := oldMeta.Annotations[key]
		newVal, newOK := newMeta.Annotations[key]
		changes = append(changes, FieldChange{
			Path:     specPathKey("spec.template.metadata.annotations", key),
			OldValue: templateAnnotationValue(key, oldVal, oldOK),
			NewValue: templateAnnotationValue(key, newVal, newOK),
		})
		if key == restartedAtAnnotation && newOK {
			summary = append(summary, "rollout restarted")
		} else {
			summary = append(summary, fmt.Sprintf("pod template annotation %s %s", key, mapKeyVerb(oldOK, newOK)))
		}
	}
	for _, key := range changedMapKeys(oldMeta.Labels, newMeta.Labels) {
		oldVal, oldOK := oldMeta.Labels[key]
		newVal, newOK := newMeta.Labels[key]
		changes = append(changes, FieldChange{
			Path:     specPathKey("spec.template.metadata.labels", key),
			OldValue: templateLabelValue(key, oldVal, oldOK),
			NewValue: templateLabelValue(key, newVal, newOK),
		})
		summary = append(summary, fmt.Sprintf("pod template label %s %s", key, mapKeyVerb(oldOK, newOK)))
	}
	return changes, summary
}

func changedMapKeys(oldMap, newMap map[string]string) []string {
	var keys []string
	for k, v := range oldMap {
		if nv, ok := newMap[k]; !ok || nv != v {
			keys = append(keys, k)
		}
	}
	for k := range newMap {
		if _, ok := oldMap[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// Annotation values are free-form (often whole config blobs), so only a
// well-formed restart timestamp is shown.
func templateAnnotationValue(key, v string, ok bool) any {
	if !ok {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, v); err == nil && key == restartedAtAnnotation {
		return v
	}
	return "changed"
}

func templateLabelValue(key, v string, ok bool) any {
	if !ok {
		return nil
	}
	if envNameLooksSecret(key) {
		return "[REDACTED]"
	}
	return aicontext.RedactSecrets(v)
}

func mapKeyVerb(oldOK, newOK bool) string {
	switch {
	case !oldOK:
		return "added"
	case !newOK:
		return "removed"
	default:
		return "changed"
	}
}

// chainDiffs runs several diff functions over the same pair, in order.
func chainDiffs(fns ...kindDiffFunc) kindDiffFunc {
	return func(oldObj, newObj any) ([]FieldChange, []string) {
		var changes []FieldChange
		var summary []string
		for _, fn := range fns {
			c, s := fn(oldObj, newObj)
			changes = append(changes, c...)
			summary = append(summary, s...)
		}
		return changes, summary
	}
}

// untrackedSpecChanges is the completeness backstop for diff functions: any
// spec leaf that changed but is not under a path in tracked becomes its own
// row, so a field the explicit differs don't know about is never hidden behind
// the fields they do report. Values are shown only for scalars.
func untrackedSpecChanges(oldObj, newObj any, tracked []FieldChange) ([]FieldChange, []string) {
	oldMeta, err := meta.Accessor(oldObj)
	if err != nil {
		return nil, nil
	}
	newMeta, err := meta.Accessor(newObj)
	if err != nil {
		return nil, nil
	}
	// Every spec write bumps a non-zero generation; skipping equal ones keeps
	// the conversion off the status-update hot path. A UID change is a
	// recreate, which restarts generation. Kinds without a generation
	// (Service) are always compared.
	if oldMeta.GetGeneration() == newMeta.GetGeneration() && newMeta.GetGeneration() > 0 && oldMeta.GetUID() == newMeta.GetUID() {
		return nil, nil
	}
	oldU, ok := specMapOf(oldObj)
	if !ok {
		return nil, nil
	}
	newU, ok := specMapOf(newObj)
	if !ok {
		return nil, nil
	}

	var found []changedSpecPath
	collectChangedSpecPaths("spec", oldU, newU, &found)

	trackedSegments := make([][]string, 0, len(tracked))
	for _, change := range tracked {
		trackedSegments = append(trackedSegments, trackedCoverage(change.Path)...)
	}
	var untracked []changedSpecPath
	for _, c := range found {
		if !specPathCovered(trackedSegments, splitSpecPath(c.path), c.order) {
			untracked = append(untracked, c)
		}
	}
	if len(untracked) == 0 {
		return nil, nil
	}

	var changes []FieldChange
	labels := make([]string, 0, len(untracked))
	for _, c := range untracked {
		label := shortSpecPath(c.path)
		if c.order {
			label += " order"
		}
		labels = append(labels, label)
		if len(changes) == maxUntrackedSpecRows {
			continue
		}
		if c.order {
			changes = append(changes, FieldChange{Path: c.path, OldValue: c.oldOrder, NewValue: c.newOrder})
			continue
		}
		oldVal, _ := lookupSpecPath(oldU, c.path)
		newVal, _ := lookupSpecPath(newU, c.path)
		changes = append(changes, FieldChange{Path: c.path, OldValue: untrackedDisplayValue(c.path, oldVal), NewValue: untrackedDisplayValue(c.path, newVal)})
	}
	shown := labels
	if len(shown) > maxUntrackedSummary {
		shown = shown[:maxUntrackedSummary]
	}
	summary := "other spec changes: " + strings.Join(shown, ", ")
	if extra := len(labels) - len(shown); extra > 0 {
		summary += fmt.Sprintf(" (+%d more)", extra)
	}
	return changes, []string{summary}
}

// shortSpecPath trims a path for the summary line to what a reader names the
// field by: pod-template fields relative to the pod spec, everything else
// relative to the object's spec. Field rows keep the full path.
func shortSpecPath(path string) string {
	for _, prefix := range []struct{ from, to string }{
		{"spec.jobTemplate.spec.template.spec.", ""},
		{"spec.jobTemplate.spec.template.", "template."},
		{"spec.jobTemplate.spec.", "jobTemplate."},
		{"spec.template.spec.", ""},
		{"spec.", ""},
	} {
		if strings.HasPrefix(path, prefix.from) {
			return prefix.to + strings.TrimPrefix(path, prefix.from)
		}
	}
	return path
}

// changedSpecPath is one difference found by collectChangedSpecPaths. An
// order entry means the named items both lists share now appear in a
// different sequence; order matters for init containers (run in sequence) and
// env vars (later values can reference earlier ones).
type changedSpecPath struct {
	path               string
	order              bool
	oldOrder, newOrder []string
}

func specMapOf(obj any) (map[string]any, bool) {
	var full map[string]any
	if u, ok := obj.(*unstructured.Unstructured); ok {
		full = u.Object
	} else {
		converted, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			return nil, false
		}
		full = converted
	}
	spec, ok := full["spec"].(map[string]any)
	return spec, ok
}

// collectChangedSpecPaths records the shallowest path at which old and new
// differ. Lists of named objects are keyed by name to match the explicit
// differs' `containers[app]` notation; other lists are keyed by index, and a
// length change on them is reported at the list itself.
func collectChangedSpecPaths(path string, oldV, newV any, out *[]changedSpecPath) {
	if reflect.DeepEqual(oldV, newV) {
		return
	}
	switch o := oldV.(type) {
	case map[string]any:
		n, ok := newV.(map[string]any)
		if !ok {
			*out = append(*out, changedSpecPath{path: path})
			return
		}
		for _, k := range unionKeys(o, n) {
			collectChangedSpecPaths(specPathKey(path, k), o[k], n[k], out)
		}
	case []any:
		n, ok := newV.([]any)
		if !ok {
			*out = append(*out, changedSpecPath{path: path})
			return
		}
		if field := listKeyField(o, n); field != "" {
			oldByKey, _ := listByKey(o, field)
			newByKey, _ := listByKey(n, field)
			if oldOrder, newOrder := sharedKeyOrder(o, newByKey, field), sharedKeyOrder(n, oldByKey, field); !equalStringSlices(oldOrder, newOrder) {
				*out = append(*out, changedSpecPath{path: path, order: true, oldOrder: oldOrder, newOrder: newOrder})
			}
			for _, k := range unionKeys(oldByKey, newByKey) {
				collectChangedSpecPaths(path+"["+k+"]", oldByKey[k], newByKey[k], out)
			}
			return
		}
		if len(o) != len(n) {
			*out = append(*out, changedSpecPath{path: path})
			return
		}
		for i := range o {
			collectChangedSpecPaths(fmt.Sprintf("%s[%d]", path, i), o[i], n[i], out)
		}
	default:
		*out = append(*out, changedSpecPath{path: path})
	}
}

func sharedKeyOrder(items []any, other map[string]any, field string) []string {
	var keys []string
	for _, item := range items {
		key, _ := item.(map[string]any)[field].(string)
		if _, ok := other[key]; ok {
			keys = append(keys, key)
		}
	}
	return keys
}

func unionKeys(a, b map[string]any) []string {
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// listKeyFields are the fields that identify list items, in order of
// preference: most named lists use name; Ingress rules have no name and are
// identified by host.
var listKeyFields = []string{"name", "host"}

// listKeyField returns the field that uniquely identifies every item in all
// the given lists, or "" when the lists must be compared by position.
func listKeyField(lists ...[]any) string {
	for _, field := range listKeyFields {
		keyed := true
		for _, list := range lists {
			if _, ok := listByKey(list, field); !ok {
				keyed = false
				break
			}
		}
		if keyed {
			return field
		}
	}
	return ""
}

func listByKey(items []any, field string) (map[string]any, bool) {
	out := make(map[string]any, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		key, _ := m[field].(string)
		if key == "" {
			return nil, false
		}
		if _, dup := out[key]; dup {
			return nil, false
		}
		out[key] = m
	}
	return out, true
}

// specPathKey appends a map key, bracketing keys that are not plain
// identifiers (label and annotation keys contain dots and slashes).
func specPathKey(path, key string) string {
	if key != "" && strings.IndexFunc(key, func(r rune) bool {
		return !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) == -1 {
		return path + "." + key
	}
	return path + "[" + key + "]"
}

// splitSpecPath splits `a.b[c.d].e` into [a b [c.d] e].
func splitSpecPath(path string) []string {
	var segments []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			segments = append(segments, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(path); i++ {
		switch c := path[i]; c {
		case '.':
			flush()
		case '[':
			flush()
			end := strings.IndexByte(path[i:], ']')
			if end < 0 {
				cur.WriteString(path[i:])
				i = len(path)
				continue
			}
			segments = append(segments, path[i:i+end+1])
			i += end
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return segments
}

// trackedCoverage returns the subtrees a tracked row actually compares. The
// Deployment resources row compares only limits and requests, not claims.
func trackedCoverage(path string) [][]string {
	segments := splitSpecPath(path)
	if path == "spec.template.spec.containers[*].resources" {
		return [][]string{append(slices.Clone(segments), "limits"), append(slices.Clone(segments), "requests")}
	}
	return [][]string{segments}
}

// specPathCovered reports whether a tracked row explains a changed path: one
// is a prefix of the other, with `[*]` or `[]` in the tracked path matching
// any list key. An order change is explained only by a row at or above the list, since
// rows for individual items don't describe their sequence.
func specPathCovered(tracked [][]string, path []string, order bool) bool {
	for _, t := range tracked {
		if order && len(t) > len(path) {
			continue
		}
		if specPathsOverlap(t, path) {
			return true
		}
	}
	return false
}

func specPathsOverlap(tracked, path []string) bool {
	n := min(len(tracked), len(path))
	for i := 0; i < n; i++ {
		if tracked[i] == path[i] {
			continue
		}
		if (tracked[i] == "[*]" || tracked[i] == "[]") && strings.HasPrefix(path[i], "[") {
			continue
		}
		return false
	}
	return true
}

func lookupSpecPath(root map[string]any, path string) (any, bool) {
	segments := splitSpecPath(path)
	if len(segments) == 0 || segments[0] != "spec" {
		return nil, false
	}
	var cur any = root
	for _, seg := range segments[1:] {
		if strings.HasPrefix(seg, "[") {
			key := strings.TrimSuffix(strings.TrimPrefix(seg, "["), "]")
			switch typed := cur.(type) {
			case map[string]any:
				cur = typed[key]
			case []any:
				if field := listKeyField(typed); field != "" {
					byKey, _ := listByKey(typed, field)
					if v, found := byKey[key]; found {
						cur = v
						continue
					}
				}
				var idx int
				if _, err := fmt.Sscanf(key, "%d", &idx); err != nil || idx < 0 || idx >= len(typed) {
					return nil, false
				}
				cur = typed[idx]
			default:
				return nil, false
			}
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur = m[seg]
	}
	return cur, cur != nil
}

// untrackedDisplayValue never shows strings from container specs or headers:
// env values, args, exec hooks and header values carry credentials whose
// sensitivity only the explicit differs' key-aware redaction can judge, and a
// value they redacted to an equal marker reaches here as "untracked".
func untrackedDisplayValue(path string, v any) any {
	switch typed := v.(type) {
	case nil:
		return nil
	case string:
		if untrackedPathSensitive(path) {
			return "changed"
		}
		return truncateConfigScalar(aicontext.RedactSecrets(typed), 200)
	case bool, int64, float64:
		return typed
	default:
		return "changed"
	}
}

func untrackedPathSensitive(path string) bool {
	for _, seg := range splitSpecPath(path) {
		key := strings.TrimSuffix(strings.TrimPrefix(seg, "["), "]")
		switch key {
		case "containers", "initContainers", "ephemeralContainers":
			return true
		}
		// Header modifiers and header matches (Gateway API routes) carry
		// Authorization and cookie values keyed by header name.
		if strings.Contains(strings.ToLower(key), "header") {
			return true
		}
		if envNameLooksSecret(key) {
			return true
		}
	}
	return false
}
