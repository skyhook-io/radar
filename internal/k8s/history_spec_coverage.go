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
	maxUntrackedSpecRows  = 25
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

// newTemplateRevision states only what a pod-template metadata change is
// known to cause. Whether and when pods are replaced depends on the kind,
// strategy, pause state and partition, and what an annotation such as
// checksum/config hashes is a chart convention Radar cannot verify.
const newTemplateRevision = " (new pod template revision)"

// diffPodTemplateMetadata covers pod-template labels and annotations. Changing
// either creates a new pod template revision, and they carry common rollout
// triggers: `kubectl rollout restart` and config-checksum annotations from
// Helm charts.
func diffPodTemplateMetadata(oldMeta, newMeta metav1.ObjectMeta) ([]FieldChange, []string) {
	var changes []FieldChange
	var summary []string
	for _, key := range changedMapKeys(oldMeta.Annotations, newMeta.Annotations) {
		oldVal, oldOK := oldMeta.Annotations[key]
		newVal, newOK := newMeta.Annotations[key]
		oldShown, newShown := templateAnnotationValues(key, oldVal, oldOK, newVal, newOK)
		changes = append(changes, FieldChange{
			Path:     specPathKey("spec.template.metadata.annotations", key),
			OldValue: oldShown,
			NewValue: newShown,
		})
		summary = append(summary, fmt.Sprintf("pod template annotation %s %s%s", key, mapKeyVerb(oldOK, newOK), newTemplateRevision))
	}
	for _, key := range changedMapKeys(oldMeta.Labels, newMeta.Labels) {
		oldVal, oldOK := oldMeta.Labels[key]
		newVal, newOK := newMeta.Labels[key]
		changes = append(changes, FieldChange{
			Path:     specPathKey("spec.template.metadata.labels", key),
			OldValue: templateLabelValue(key, oldVal, oldOK),
			NewValue: templateLabelValue(key, newVal, newOK),
		})
		summary = append(summary, fmt.Sprintf("pod template label %s %s%s", key, mapKeyVerb(oldOK, newOK), newTemplateRevision))
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
func templateAnnotationValues(key, oldVal string, oldOK bool, newVal string, newOK bool) (any, any) {
	if key == restartedAtAnnotation && isTimestamp(oldVal, oldOK) && isTimestamp(newVal, newOK) {
		return valueOrNil(oldVal, oldOK), valueOrNil(newVal, newOK)
	}
	return hiddenValues(oldOK, newOK)
}

func isTimestamp(v string, ok bool) bool {
	if !ok {
		return true
	}
	_, err := time.Parse(time.RFC3339, v)
	return err == nil
}

// Markers for a value the feed does not show (a secret, a config blob, or a
// structure too large to diff). Absence stays nil on either side, so a row
// still says whether the field was set, changed or removed; the UI renders
// these as a badge rather than as a value.
const (
	hiddenValueMarker = "(value hidden)"
	hiddenSetMarker   = "(set)"
)

func hiddenValues(oldPresent, newPresent bool) (any, any) {
	switch {
	case oldPresent && newPresent:
		return hiddenValueMarker, hiddenValueMarker
	case newPresent:
		return nil, hiddenSetMarker
	case oldPresent:
		return hiddenSetMarker, nil
	default:
		return nil, nil
	}
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
// leaf under root (the object's desired state: spec, or webhooks for admission
// webhook configurations) that changed but is not under a path in tracked or
// ignored becomes its own row, so a field the explicit differs don't know
// about is never hidden behind the fields they do report. Values are shown
// only for scalars.
func untrackedSpecChanges(oldObj, newObj, oldProjected, newProjected any, root string, ignored []string, tracked []FieldChange) ([]FieldChange, []string) {
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
	// (Service, ResourceQuota) compare the root field first, which is cheap.
	if oldMeta.GetGeneration() == newMeta.GetGeneration() && newMeta.GetGeneration() > 0 && oldMeta.GetUID() == newMeta.GetUID() {
		return nil, nil
	}
	if rootFieldsEqual(oldObj, newObj, root) {
		return nil, nil
	}
	oldU, ok := backstopRootOf(oldObj, root)
	if !ok {
		return nil, nil
	}
	newU, ok := backstopRootOf(newObj, root)
	if !ok {
		return nil, nil
	}

	var found []changedSpecPath
	collectChangedSpecPaths(root, oldU, newU, "", &found)

	trackedSegments := make([][]string, 0, len(tracked)+len(ignored))
	for _, change := range tracked {
		trackedSegments = append(trackedSegments, trackedCoverage(change)...)
	}
	for _, path := range ignored {
		trackedSegments = append(trackedSegments, splitSpecPath(path))
	}
	// The explicit differs read typed projections when the informer delivers
	// unstructured objects. A field the compiled types do not know never
	// reached them, so no row can cover it.
	known := func(string) bool { return true }
	if oldProjected != nil && newProjected != nil {
		oldP, oldOK := backstopRootOf(oldProjected, root)
		newP, newOK := backstopRootOf(newProjected, root)
		if oldOK && newOK {
			known = func(path string) bool {
				_, inOldRaw := lookupSpecPath(oldU, root, path)
				_, inNewRaw := lookupSpecPath(newU, root, path)
				_, inOldP := lookupSpecPath(oldP, root, path)
				_, inNewP := lookupSpecPath(newP, root, path)
				return (!inOldRaw || inOldP) && (!inNewRaw || inNewP)
			}
		}
	}
	var untracked, covered []changedSpecPath
	for _, c := range found {
		if known(c.path) && specPathCovered(trackedSegments, splitSpecPath(c.path), c.order) {
			covered = append(covered, c)
		} else {
			untracked = append(untracked, c)
		}
	}
	reported := make(map[string]bool, len(tracked))
	for _, change := range tracked {
		reported[change.Path] = true
	}
	untracked = collapseUntracked(untracked, covered, reported)
	if len(untracked) == 0 {
		return nil, nil
	}

	var changes []FieldChange
	var omitted []string
	labels := make([]string, 0, len(untracked))
	for _, c := range untracked {
		oldVal, newVal := c.oldV, c.newV
		oldFound, newFound := oldVal != nil, newVal != nil
		label := shortSpecPath(c.path)
		switch {
		case c.order:
			label += " order"
		case strings.HasSuffix(c.path, "]") && !oldFound && newFound:
			label += " (added)"
		case strings.HasSuffix(c.path, "]") && oldFound && !newFound:
			label += " (removed)"
		}
		labels = append(labels, label)
		if len(changes) == maxUntrackedSpecRows {
			omitted = append(omitted, c.path)
			continue
		}
		if c.order {
			changes = append(changes, FieldChange{Path: c.path, OldValue: c.oldOrder, NewValue: c.newOrder})
			continue
		}
		oldShown, newShown := untrackedDisplayValues(c.path, oldVal, newVal)
		changes = append(changes, FieldChange{Path: c.path, OldValue: oldShown, NewValue: newShown})
	}
	// Past the row cap, one row still names every changed field, so no field
	// identity is lost even when its values are.
	if len(omitted) > 0 {
		changes = append(changes, FieldChange{Path: root + " (more fields)", OldValue: nil, NewValue: omitted})
	}
	shown := labels
	if len(shown) > maxUntrackedSummary {
		shown = shown[:maxUntrackedSummary]
	}
	// Webhook configurations have no spec; their root is the webhooks list.
	prefix := "other spec changes: "
	if root != "spec" {
		prefix = "other changes: "
	}
	summary := prefix + strings.Join(shown, ", ")
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
	// absentRoot is the outermost enclosing block that exists on only one
	// side, or "" when the path's parent exists on both.
	absentRoot string
	// oldV and newV are the values compared at path, captured during the
	// walk so the row shows exactly what was compared; nil means absent.
	oldV, newV any
}

// backstopRootOf returns the object's root field (a map for spec, a list for
// webhooks) in unstructured form.
func backstopRootOf(obj any, root string) (any, bool) {
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
	value, ok := full[root]
	return value, ok && value != nil
}

func specMapOf(obj any) (map[string]any, bool) {
	value, ok := backstopRootOf(obj, "spec")
	if !ok {
		return nil, false
	}
	spec, ok := value.(map[string]any)
	return spec, ok
}

// rootFieldsEqual compares the root field without converting the objects:
// directly for unstructured objects, through the typed struct's field for
// typed ones. false means "different or unknown".
func rootFieldsEqual(oldObj, newObj any, root string) bool {
	oldU, oldIsU := oldObj.(*unstructured.Unstructured)
	newU, newIsU := newObj.(*unstructured.Unstructured)
	if oldIsU && newIsU {
		return reflect.DeepEqual(oldU.Object[root], newU.Object[root])
	}
	field := strings.ToUpper(root[:1]) + root[1:]
	oldV := reflect.Indirect(reflect.ValueOf(oldObj))
	newV := reflect.Indirect(reflect.ValueOf(newObj))
	if oldV.Kind() != reflect.Struct || newV.Kind() != reflect.Struct {
		return false
	}
	oldF, newF := oldV.FieldByName(field), newV.FieldByName(field)
	if !oldF.IsValid() || !newF.IsValid() {
		return false
	}
	return reflect.DeepEqual(oldF.Interface(), newF.Interface())
}

// collectChangedSpecPaths records the shallowest path at which old and new
// differ. Lists of named objects are keyed by name to match the explicit
// differs' `containers[app]` notation; other lists are keyed by index.
func collectChangedSpecPaths(path string, oldV, newV any, absentRoot string, out *[]changedSpecPath) {
	if reflect.DeepEqual(oldV, newV) {
		return
	}
	// A block that appears or disappears is walked to its leaves, so a row a
	// differ reports for one leaf inside it does not hide its siblings;
	// collapseUntracked folds the leaves back into the block for display.
	// An empty block has no leaves to walk; its appearance is the change.
	if (oldV == nil && isEmptyBlock(newV)) || (newV == nil && isEmptyBlock(oldV)) {
		*out = append(*out, changedSpecPath{path: path, absentRoot: absentRoot, oldV: oldV, newV: newV})
		return
	}
	if absentRoot == "" && ((oldV == nil && isBlock(newV)) || (newV == nil && isBlock(oldV))) {
		absentRoot = path
	}
	oldV, newV = emptyLike(oldV, newV), emptyLike(newV, oldV)
	switch o := oldV.(type) {
	case map[string]any:
		n, ok := newV.(map[string]any)
		if !ok {
			*out = append(*out, changedSpecPath{path: path, absentRoot: absentRoot, oldV: oldV, newV: newV})
			return
		}
		for _, k := range unionKeys(o, n) {
			collectChangedSpecPaths(specPathKey(path, k), o[k], n[k], absentRoot, out)
		}
	case []any:
		n, ok := newV.([]any)
		if !ok {
			*out = append(*out, changedSpecPath{path: path, absentRoot: absentRoot, oldV: oldV, newV: newV})
			return
		}
		if field := listKeyField(o, n); field != "" {
			oldByKey, _ := listByKey(o, field)
			newByKey, _ := listByKey(n, field)
			if oldOrder, newOrder := sharedKeyOrder(o, newByKey, field), sharedKeyOrder(n, oldByKey, field); orderMatters(path) && !equalStringSlices(oldOrder, newOrder) {
				*out = append(*out, changedSpecPath{path: path, order: true, oldOrder: oldOrder, newOrder: newOrder})
			}
			for _, k := range unionKeys(oldByKey, newByKey) {
				collectChangedSpecPaths(path+"["+k+"]", oldByKey[k], newByKey[k], absentRoot, out)
			}
			return
		}
		// Unkeyed items are compared by position; items past the shorter
		// list are reported as added or removed.
		for i := 0; i < max(len(o), len(n)); i++ {
			var oldItem, newItem any
			if i < len(o) {
				oldItem = o[i]
			}
			if i < len(n) {
				newItem = n[i]
			}
			collectChangedSpecPaths(fmt.Sprintf("%s[%d]", path, i), oldItem, newItem, absentRoot, out)
		}
	default:
		*out = append(*out, changedSpecPath{path: path, absentRoot: absentRoot, oldV: oldV, newV: newV})
	}
}

// collapseUntracked reports an uncovered leaf inside a block that exists on
// only one side at the shallowest enclosing block that holds no covered leaf:
// "rules[1]" rather than each of its fields, while a block that also holds a
// field a differ reported is split down to the uncovered part.
func collapseUntracked(untracked, covered []changedSpecPath, reported map[string]bool) []changedSpecPath {
	coveredSegs := make([][]string, 0, len(covered))
	for _, c := range covered {
		coveredSegs = append(coveredSegs, splitSpecPath(c.path))
	}
	holdsCovered := func(prefix []string) bool {
		for _, segs := range coveredSegs {
			if len(segs) >= len(prefix) && slices.Equal(segs[:len(prefix)], prefix) {
				return true
			}
		}
		return false
	}
	var out []changedSpecPath
	seen := map[string]int{}
	for _, c := range untracked {
		if c.order || c.absentRoot == "" {
			out = append(out, c)
			continue
		}
		segs := splitSpecPath(c.path)
		path := c.path
		for n := len(splitSpecPath(c.absentRoot)); n <= len(segs); n++ {
			if !holdsCovered(segs[:n]) {
				path = joinSpecPath(segs[:n])
				break
			}
		}
		// A block collapsed onto a reported summary row's own path repeats
		// it. Items beneath that row keep their own rows: a count says that
		// something changed, not what.
		if reported[path] && isSummaryRow(path) {
			continue
		}
		if path == c.path {
			out = append(out, c)
			continue
		}
		// A collapsed block shows only whether it exists on each side.
		present := func(v any) any {
			if v != nil {
				return map[string]any{}
			}
			return nil
		}
		if i, ok := seen[path]; ok {
			if out[i].oldV == nil {
				out[i].oldV = present(c.oldV)
			}
			if out[i].newV == nil {
				out[i].newV = present(c.newV)
			}
			continue
		}
		seen[path] = len(out)
		out = append(out, changedSpecPath{path: path, oldV: present(c.oldV), newV: present(c.newV)})
	}
	return out
}

// joinSpecPath is the inverse of splitSpecPath.
func joinSpecPath(segments []string) string {
	var b strings.Builder
	for i, seg := range segments {
		if i > 0 && !strings.HasPrefix(seg, "[") {
			b.WriteByte('.')
		}
		b.WriteString(seg)
	}
	return b.String()
}

func isEmptyBlock(v any) bool {
	switch typed := v.(type) {
	case map[string]any:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	}
	return false
}

func isBlock(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// emptyLike returns an empty map or list in place of nil when other is one,
// and v unchanged otherwise.
func emptyLike(v, other any) any {
	if v != nil {
		return v
	}
	switch other.(type) {
	case map[string]any:
		return map[string]any{}
	case []any:
		return []any{}
	}
	return nil
}

// orderMatters limits order-only rows to lists whose sequence has an effect:
// anything in a pod template (a reorder there is a new template revision,
// and init containers and env vars are order-sensitive), admission webhooks
// (mutating webhooks run in order), and route rules (the first of two
// equally specific matches wins).
func orderMatters(path string) bool {
	return strings.Contains(path, "template.") || strings.HasPrefix(path, "webhooks") || path == "spec.rules"
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
// summaryRowPaths are rows whose values summarize a whole list or block
// rather than compare it: counts (rules, TLS, grants, listeners, canary
// steps), a Rollout's strategy kind, and reference lists for Service ports.
// Like summaryRowSuffixes, they cover only their own path, so edits beneath
// them that the summary does not capture are still reported.
var summaryRowPaths = map[string]bool{
	"spec.rules": true, "spec.tls": true, "spec.from": true, "spec.to": true,
	"spec.listeners": true, "spec.strategy": true, "spec.strategy.canary.steps": true,
	"spec.ports": true,
}

// summaryRowSuffixes are pod-template rows that compare references only:
// volume sources by name, mounts by path, ports by number, tolerations by
// key and effect, envFrom by source. Fields they leave out (defaultMode,
// sizeLimit, mountPropagation, hostPort, tolerationSeconds, optional) would
// otherwise be hidden whenever a reference changed in the same update.
var summaryRowSuffixes = []string{"spec.volumes", "spec.tolerations", "].volumeMounts", "].ports", "].envFrom"}

func isSummaryRow(path string) bool {
	if summaryRowPaths[path] {
		return true
	}
	for _, suffix := range summaryRowSuffixes {
		if strings.HasSuffix(path, suffix) && strings.Contains(path, "template.spec") {
			return true
		}
	}
	return false
}

// exactCoverage marks a tracked path that covers only itself.
const exactCoverage = "="

// trackedCoverage returns the subtrees a reported row actually compared. A
// row covers only its own path unless it is known to compare more: a whole
// item added or removed, a row whose value is the complete subtree, or a row
// that compares a known set of child fields. Anything a row does not compare
// stays visible to the backstop, so one reported field never hides another.
func trackedCoverage(change FieldChange) [][]string {
	path := change.Path
	segments := splitSpecPath(path)
	within := func(children ...string) [][]string {
		out := make([][]string, 0, len(children))
		for _, child := range children {
			out = append(out, append(slices.Clone(segments), splitSpecPath(child)...))
		}
		return out
	}
	switch {
	case isSummaryRow(path):
		return [][]string{append(slices.Clone(segments), exactCoverage)}
	case path == "spec.template.spec.containers[*].resources":
		return within("limits", "requests")
	case len(segments) == 2 && segments[0] == "webhooks":
		// An added or removed webhook is still covered whole, since its path
		// is a prefix of every entry here.
		return within(webhookSummaryFields...)
	case strings.HasPrefix(path, "spec.hard."):
		// Quota names contain dots (requests.cpu); the backstop brackets them.
		return [][]string{splitSpecPath(specPathKey("spec.hard", strings.TrimPrefix(path, "spec.hard.")))}
	case change.OldValue == nil || change.NewValue == nil:
		return [][]string{segments}
	case isProbeRow(path):
		return within(probeComparedFields...)
	case isEnvRow(path):
		return within(envComparedFields...)
	case isCompleteRow(path):
		return [][]string{segments}
	}
	return [][]string{append(slices.Clone(segments), exactCoverage)}
}

// probeComparedFields are the probe fields normalizedProbe compares.
var probeComparedFields = []string{
	"initialDelaySeconds", "timeoutSeconds", "periodSeconds", "successThreshold", "failureThreshold",
	"httpGet.scheme", "httpGet.port", "httpGet.path", "tcpSocket.port", "grpc.port", "grpc.service", "exec.command",
}

// envComparedFields are the env var fields envVarDisplayValue shows.
var envComparedFields = []string{
	"value", "valueFrom.configMapKeyRef.name", "valueFrom.configMapKeyRef.key",
	"valueFrom.secretKeyRef.name", "valueFrom.secretKeyRef.key",
	"valueFrom.fieldRef.fieldPath", "valueFrom.resourceFieldRef.resource",
}

func isProbeRow(path string) bool {
	for _, probe := range []string{"].readinessProbe", "].livenessProbe", "].startupProbe"} {
		if strings.HasSuffix(path, probe) {
			return true
		}
	}
	return false
}

func isEnvRow(path string) bool {
	segments := splitSpecPath(path)
	return len(segments) >= 2 && segments[len(segments)-2] == "env" && strings.HasPrefix(segments[len(segments)-1], "[")
}

// completeRowSuffixes are rows whose value is the whole subtree at their
// path: full maps and lists, and summaries built from every field of the type
// they describe. The securityContext and affinity "changed" markers are not:
// the fields under them are reported too, so a reader sees which changed.
var completeRowSuffixes = []string{
	"spec.nodeSelector", "spec.dnsConfig.nameservers",
	"spec.dnsConfig.searches", "spec.dnsConfig.options", "].command", "].args", "].image",
	"spec.hostnames", "spec.externalIPs", "spec.loadBalancerSourceRanges", "spec.selector",
	"spec.limits", "spec.metrics", "spec.scaleTargetRef", "spec.behavior.scaleUp", "spec.behavior.scaleDown",
}

func isCompleteRow(path string) bool {
	for _, suffix := range completeRowSuffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

// specPathCovered reports whether a tracked row explains a changed path: one
// is a prefix of the other, with `[*]` or `[]` in the tracked path matching
// any list key. An order change is explained only by a row at or above the list, since
// rows for individual items don't describe their sequence.
func specPathCovered(tracked [][]string, path []string, order bool) bool {
	for _, t := range tracked {
		if n := len(t); n > 0 && t[n-1] == exactCoverage {
			if slices.Equal(t[:n-1], path) {
				return true
			}
			continue
		}
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

func lookupSpecPath(rootValue any, root, path string) (any, bool) {
	segments := splitSpecPath(path)
	if len(segments) == 0 || segments[0] != root {
		return nil, false
	}
	var cur any = rootValue
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
func untrackedDisplayValues(path string, oldV, newV any) (any, any) {
	if untrackedValueHidden(path, oldV) || untrackedValueHidden(path, newV) {
		return hiddenValues(oldV != nil, newV != nil)
	}
	return untrackedScalar(oldV), untrackedScalar(newV)
}

func untrackedValueHidden(path string, v any) bool {
	switch v.(type) {
	case nil:
		return false
	case bool, int64, float64:
		// Numbers can be credentials too (a PIN, a numeric password); a
		// sensitive path or a free-form map hides every scalar.
		return untrackedPathSensitive(path) || inFreeFormMap(path)
	case string:
		return untrackedPathSensitive(path) || !safeStringField(path)
	default:
		return true
	}
}

// safeStringFields name string fields whose values are identifiers, enums or
// routing coordinates rather than free-form text, so the backstop shows them.
// Every other string (a CEL expression, a header or query value, a command
// line, an annotation) is hidden: a credential literal can sit in any
// free-form field, and pattern redaction cannot recognize all of them.
var safeStringFields = map[string]bool{
	"name": true, "kind": true, "apiVersion": true, "group": true, "namespace": true,
	"hostname": true, "hostnames": true, "host": true, "hosts": true, "path": true, "pathType": true,
	"protocol": true, "scheme": true, "port": true, "type": true, "mode": true, "operator": true,
	"effect": true, "key": true, "resource": true, "resources": true, "verbs": true, "operations": true,
	"apiGroups": true, "apiVersions": true, "scope": true, "schedule": true,
	"timeZone": true, "image": true, "matchPolicy": true, "sideEffects": true, "failurePolicy": true,
	"sizeLimit": true,
}

// safeStringSuffixes cover enum and reference fields by naming convention
// (restartPolicy, serviceAccountName, ingressClassName, pathType...).
var safeStringSuffixes = []string{"Policy", "Name", "ClassName", "Type", "Mode"}

// freeFormMaps hold arbitrary user keys, so a key that happens to be called
// "name" or "type" inside them says nothing about its value.
var freeFormMaps = map[string]bool{
	"labels": true, "annotations": true, "matchLabels": true, "nodeSelector": true,
	"parameters": true, "data": true, "stringData": true, "options": true, "config": true,
	"plugins": true, "args": true,
}

func inFreeFormMap(path string) bool {
	for _, seg := range splitSpecPath(path) {
		if freeFormMaps[seg] {
			return true
		}
	}
	return false
}

func safeStringField(path string) bool {
	if inFreeFormMap(path) {
		return false
	}
	segments := splitSpecPath(path)
	// A string list item takes its list's field name (hostnames[0]).
	for i := len(segments) - 1; i >= 0; i-- {
		key := segments[i]
		if strings.HasPrefix(key, "[") {
			continue
		}
		if safeStringFields[key] {
			return true
		}
		for _, suffix := range safeStringSuffixes {
			if strings.HasSuffix(key, suffix) {
				return true
			}
		}
		return false
	}
	return false
}

func untrackedScalar(v any) any {
	if s, ok := v.(string); ok {
		return truncateConfigScalar(aicontext.RedactSecrets(s), 200)
	}
	return v
}

func untrackedPathSensitive(path string) bool {
	for _, seg := range splitSpecPath(path) {
		key := strings.TrimSuffix(strings.TrimPrefix(seg, "["), "]")
		switch key {
		case "containers", "initContainers", "ephemeralContainers":
			return true
		}
		// Header and query-parameter matches and modifiers (Gateway API
		// routes) carry Authorization, cookie and API-key values; annotations
		// at any depth carry free-form config.
		lower := strings.ToLower(key)
		if strings.Contains(lower, "header") || strings.Contains(lower, "queryparam") || key == "annotations" {
			return true
		}
		if envNameLooksSecret(key) {
			return true
		}
	}
	return false
}
