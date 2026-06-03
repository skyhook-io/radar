package mcp

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/k8score"
)

type mutationVerificationOptions struct {
	Kind         string
	Group        string
	Namespace    string
	Name         string
	GVR          schema.GroupVersionResource
	Post         *unstructured.Unstructured
	Before       *unstructured.Unstructured
	BeforeErr    string
	Desired      *unstructured.Unstructured
	JSONPatchOps []jsonPatchOperation
	PreviewDiff  bool
}

func buildMutationVerification(ctx context.Context, dynClient dynamic.Interface, opts mutationVerificationOptions) map[string]any {
	out := map[string]any{"mode": "post_mutation_state"}
	if opts.PreviewDiff {
		out["mode"] = "dry_run_preview"
	}

	post := opts.Post
	if post == nil {
		if dynClient == nil {
			out["error"] = "post-mutation object unavailable and dynamic client is nil"
			return out
		}
		gvr := opts.GVR
		if gvr.Empty() {
			resolved, _, err := resolveMutationGVR(opts.Kind, opts.Group)
			if err != nil {
				out["error"] = err.Error()
				return out
			}
			gvr = resolved
		}
		client := dynClient.Resource(gvr)
		var ri dynamic.ResourceInterface = client
		if opts.Namespace != "" {
			ri = client.Namespace(opts.Namespace)
		}
		got, err := ri.Get(ctx, opts.Name, metav1.GetOptions{})
		if err != nil {
			out["error"] = fmt.Sprintf("post-mutation get failed: %v", err)
			return out
		}
		post = got
	}

	if len(opts.JSONPatchOps) > 0 {
		out["operations"] = verifyJSONPatchOperations(opts.Before, post, opts.JSONPatchOps, opts.BeforeErr)
	}
	if opts.BeforeErr != "" {
		out["preMutationRead"] = map[string]any{
			"status": "failed",
			"error":  opts.BeforeErr,
		}
	}
	if opts.PreviewDiff {
		if diff := beforeAfterPreviewDiff(opts.Before, post); len(diff) > 0 {
			out["previewDiff"] = diff
		}
	}
	if diff := submittedVsLiveDiff(opts.Desired, opts.Before, post); len(diff) > 0 {
		out["desiredLiveDiff"] = diff
	}

	out["resource"] = aicontext.MinifyUnstructured(post, aicontext.LevelDetail)
	if warnings := k8score.EnrichRuntimeObjectWarnings(post); len(warnings) > 0 {
		out["warnings"] = warnings
	}
	if opts.PreviewDiff {
		return out
	}
	if rollout := summarizeWorkloadRollout(post); len(rollout) > 0 {
		out["rollout"] = rollout
	}
	cacheSnapshot := false
	if pods := summarizeSelectedPods(post); len(pods) > 0 {
		pods["source"] = "informer_cache"
		pods["mayBeStale"] = true
		out["pods"] = pods
		cacheSnapshot = true
	}
	if related := relatedIssuesForObject(post); len(related) > 0 {
		out["currentIssues"] = related
		cacheSnapshot = true
	}
	if cacheSnapshot {
		out["cacheSnapshot"] = map[string]any{
			"source": "informer_cache",
			"note":   "pods and currentIssues are cache snapshots and may lag immediately after a mutation",
		}
	}

	return out
}

func beforeAfterPreviewDiff(before, after *unstructured.Unstructured) map[string]any {
	if after == nil {
		return nil
	}
	beforeObj := mutationPreviewObject(before)
	afterObj := mutationPreviewObject(after)
	var differences []map[string]any
	collectValueDiffs("", beforeObj, afterObj, &differences, 12)
	if len(differences) == 0 {
		return nil
	}
	return map[string]any{
		"mode":        "before_after",
		"differences": differences,
	}
}

func mutationPreviewObject(obj *unstructured.Unstructured) any {
	if obj == nil {
		return nil
	}
	out := map[string]any{}
	for _, path := range [][]string{
		{"metadata", "labels"},
		{"metadata", "annotations"},
		{"data"},
		{"spec"},
	} {
		if val, ok := nestedValue(obj, path...); ok {
			setNestedPreviewValue(out, val, path...)
		}
	}
	return out
}

func setNestedPreviewValue(root map[string]any, val any, fields ...string) {
	cur := root
	for i, field := range fields {
		if i == len(fields)-1 {
			cur[field] = val
			return
		}
		next, ok := cur[field].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[field] = next
		}
		cur = next
	}
}

func collectValueDiffs(path string, before, after any, differences *[]map[string]any, limit int) {
	if len(*differences) >= limit {
		return
	}
	if reflect.DeepEqual(normalizeJSONNumber(before), normalizeJSONNumber(after)) {
		return
	}
	switch a := after.(type) {
	case map[string]any:
		b, _ := before.(map[string]any)
		keys := make([]string, 0, len(a)+len(b))
		seen := map[string]bool{}
		for key := range a {
			keys = append(keys, key)
			seen[key] = true
		}
		for key := range b {
			if !seen[key] {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectValueDiffs(jsonPathAppend(path, key), b[key], a[key], differences, limit)
			if len(*differences) >= limit {
				return
			}
		}
		return
	case []any:
		b, _ := before.([]any)
		if len(a) == len(b) {
			for i := range a {
				collectValueDiffs(jsonPathAppend(path, fmt.Sprintf("%d", i)), b[i], a[i], differences, limit)
				if len(*differences) >= limit {
					return
				}
			}
			return
		}
	}

	entry := map[string]any{
		"type": "changed",
		"path": path,
	}
	switch {
	case before == nil:
		entry["type"] = "added"
	case after == nil:
		entry["type"] = "removed"
	}
	if before != nil {
		entry["before"] = previewValue(path, before)
	}
	if after != nil {
		entry["after"] = previewValue(path, after)
	}
	*differences = append(*differences, entry)
}

func jsonPathAppend(path, key string) string {
	escaped := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
	if path == "" {
		return "/" + escaped
	}
	return path + "/" + escaped
}

func previewValue(path string, val any) any {
	switch v := val.(type) {
	case string:
		redacted := aicontext.RedactSecrets(v)
		if isSensitivePreviewPath(path) && redacted == v {
			return "[redacted]"
		}
		if len(redacted) > 160 {
			return redacted[:160] + "..."
		}
		return redacted
	case map[string]any:
		return fmt.Sprintf("object(%d keys)", len(v))
	case []any:
		return fmt.Sprintf("array(%d items)", len(v))
	default:
		return normalizeJSONNumber(v)
	}
}

func isSensitivePreviewPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.Contains(lower, "secret") ||
		strings.Contains(lower, "token") ||
		strings.Contains(lower, "password") ||
		strings.Contains(lower, "credential") ||
		strings.Contains(lower, "/data/")
}

func submittedVsLiveDiff(desired, before, live *unstructured.Unstructured) map[string]any {
	if desired == nil || live == nil {
		return nil
	}
	var differences []map[string]any
	add := func(kind, path, message string) {
		if len(differences) >= 8 {
			return
		}
		differences = append(differences, map[string]any{
			"type":    kind,
			"path":    path,
			"message": message,
		})
	}

	for _, path := range scalarOrMapDiffPaths(desired.GetKind()) {
		desiredVal, desiredFound := nestedValue(desired, path...)
		liveVal, liveFound := nestedValue(live, path...)
		beforeVal, beforeFound := nestedValue(before, path...)
		ptr := "/" + strings.Join(path, "/")
		switch {
		case desiredFound && liveFound && !submittedValueMatchesLive(desiredVal, liveVal):
			add("submitted_value_differs", ptr, "live value differs from the submitted manifest after apply")
		case desiredFound && !liveFound:
			add("submitted_value_missing", ptr, "submitted field is absent from the live object after apply")
		case !desiredFound && beforeFound && liveFound && reflect.DeepEqual(normalizeJSONNumber(liveVal), normalizeJSONNumber(beforeVal)) && parentPathPresent(desired, path):
			// Server-side apply does not remove fields just because a later
			// partial manifest omitted them; surface likely retained ownership.
			add("omitted_field_retained", ptr, "field was omitted from the submitted manifest but remains live; use patch_resource or an explicit null/remove if removal was intended")
		}
	}

	for _, spec := range namedListDiffSpecs(desired.GetKind()) {
		compareNamedList(desired, live, spec.path, spec.fields, add)
	}

	if len(differences) == 0 {
		return nil
	}
	return map[string]any{
		"mode":        "submitted_vs_live",
		"differences": differences,
	}
}

func scalarOrMapDiffPaths(kind string) [][]string {
	switch strings.ToLower(kind) {
	case "deployment", "statefulset", "daemonset", "replicaset", "job", "cronjob":
		prefix := podSpecPathForKind(kind)
		return appendPathPrefix(prefix, [][]string{
			{"dnsPolicy"},
			{"dnsConfig"},
			{"nodeSelector"},
			{"affinity"},
			{"tolerations"},
			{"volumes"},
		})
	case "pod":
		return [][]string{
			{"spec", "dnsPolicy"},
			{"spec", "dnsConfig"},
			{"spec", "nodeSelector"},
			{"spec", "affinity"},
			{"spec", "tolerations"},
			{"spec", "volumes"},
		}
	case "service":
		return [][]string{{"spec", "selector"}, {"spec", "ports"}, {"spec", "type"}}
	default:
		return nil
	}
}

type namedListDiffSpec struct {
	path   []string
	fields []string
}

func namedListDiffSpecs(kind string) []namedListDiffSpec {
	fields := []string{"image", "command", "args", "ports", "env", "volumeMounts", "readinessProbe", "livenessProbe", "startupProbe", "resources"}
	switch strings.ToLower(kind) {
	case "deployment", "statefulset", "daemonset", "replicaset", "job", "cronjob":
		prefix := podSpecPathForKind(kind)
		return []namedListDiffSpec{
			{path: append(append([]string(nil), prefix...), "containers"), fields: fields},
			{path: append(append([]string(nil), prefix...), "initContainers"), fields: fields},
		}
	case "pod":
		return []namedListDiffSpec{
			{path: []string{"spec", "containers"}, fields: fields},
			{path: []string{"spec", "initContainers"}, fields: fields},
		}
	default:
		return nil
	}
}

func podSpecPathForKind(kind string) []string {
	switch strings.ToLower(kind) {
	case "cronjob":
		return []string{"spec", "jobTemplate", "spec", "template", "spec"}
	case "job":
		return []string{"spec", "template", "spec"}
	default:
		return []string{"spec", "template", "spec"}
	}
}

func appendPathPrefix(prefix []string, suffixes [][]string) [][]string {
	out := make([][]string, 0, len(suffixes))
	for _, suffix := range suffixes {
		path := append([]string(nil), prefix...)
		path = append(path, suffix...)
		out = append(out, path)
	}
	return out
}

func compareNamedList(desired, live *unstructured.Unstructured, path []string, fields []string, add func(kind, path, message string)) {
	desiredRaw, desiredFound := nestedValue(desired, path...)
	if !desiredFound {
		return
	}
	liveRaw, liveFound := nestedValue(live, path...)
	if !liveFound {
		add("submitted_list_missing", jsonPath(path), "submitted list is absent from the live object after apply")
		return
	}
	desiredItems := namedItems(desiredRaw)
	liveItems := namedItems(liveRaw)
	if len(desiredItems) == 0 || len(liveItems) == 0 {
		if !reflect.DeepEqual(normalizeJSONNumber(liveRaw), normalizeJSONNumber(desiredRaw)) {
			add("submitted_list_differs", jsonPath(path), "live list differs from the submitted manifest after apply")
		}
		return
	}

	var extra []string
	for name := range liveItems {
		if _, ok := desiredItems[name]; !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		add("extra_live_list_items", jsonPath(path), fmt.Sprintf("live list still contains item(s) omitted from the submitted manifest: %s", strings.Join(extra, ", ")))
	}

	var missing []string
	for name := range desiredItems {
		if _, ok := liveItems[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		add("submitted_list_items_missing", jsonPath(path), fmt.Sprintf("submitted item(s) are absent from the live object: %s", strings.Join(missing, ", ")))
	}

	names := make([]string, 0, len(desiredItems))
	for name := range desiredItems {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		desiredItem := desiredItems[name]
		liveItem, ok := liveItems[name]
		if !ok {
			continue
		}
		for _, field := range fields {
			desiredVal, desiredHas := desiredItem[field]
			if !desiredHas {
				continue
			}
			liveVal, liveHas := liveItem[field]
			if !liveHas {
				add("submitted_field_missing", jsonPath(append(append([]string(nil), path...), name, field)), "submitted field is absent from the live object after apply")
				continue
			}
			if !submittedValueMatchesLive(desiredVal, liveVal) {
				add("submitted_field_differs", jsonPath(append(append([]string(nil), path...), name, field)), "live field differs from the submitted manifest after apply")
			}
		}
	}
}

func submittedValueMatchesLive(desired, live any) bool {
	return desiredSubsetOfLive(normalizeJSONNumber(desired), normalizeJSONNumber(live))
}

func desiredSubsetOfLive(desired, live any) bool {
	switch d := desired.(type) {
	case map[string]any:
		l, ok := live.(map[string]any)
		if !ok {
			return false
		}
		for key, desiredVal := range d {
			liveVal, ok := l[key]
			if !ok || !desiredSubsetOfLive(desiredVal, liveVal) {
				return false
			}
		}
		return true
	case []any:
		l, ok := live.([]any)
		if !ok || len(d) != len(l) {
			return false
		}
		for i := range d {
			if !desiredSubsetOfLive(d[i], l[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(live, desired)
	}
}

func nestedValue(obj *unstructured.Unstructured, fields ...string) (any, bool) {
	if obj == nil {
		return nil, false
	}
	var cur any = obj.Object
	for _, field := range fields {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := m[field]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func parentPathPresent(obj *unstructured.Unstructured, path []string) bool {
	if len(path) <= 1 {
		return true
	}
	_, ok := nestedValue(obj, path[:len(path)-1]...)
	return ok
}

func namedItems(raw any) map[string]map[string]any {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make(map[string]map[string]any, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil
		}
		name, _ := m["name"].(string)
		if name == "" {
			return nil
		}
		out[name] = m
	}
	return out
}

func jsonPath(path []string) string {
	if len(path) == 0 {
		return "/"
	}
	return "/" + strings.Join(path, "/")
}

func resolveMutationGVR(kind, group string) (schema.GroupVersionResource, bool, error) {
	if disc := k8s.GetResourceDiscovery(); disc != nil {
		if group != "" {
			if ar, ok := disc.GetResourceWithGroup(kind, group); ok {
				return schema.GroupVersionResource{Group: ar.Group, Version: ar.Version, Resource: ar.Name}, ar.Namespaced, nil
			}
		} else if ar, ok := disc.GetResource(kind); ok {
			return schema.GroupVersionResource{Group: ar.Group, Version: ar.Version, Resource: ar.Name}, ar.Namespaced, nil
		}
	}

	if gvr, ok := k8s.BuiltinGVR(kind, group); ok {
		clusterScoped, _, _ := k8s.ClassifyKindScope(kind, group)
		return gvr, !clusterScoped, nil
	}
	if group == "" {
		if gvr, ok := k8s.BuiltinGVRAnyGroup(kind); ok {
			clusterScoped, _, _ := k8s.ClassifyKindScope(kind, gvr.Group)
			return gvr, !clusterScoped, nil
		}
	}
	if group != "" {
		return schema.GroupVersionResource{}, false, fmt.Errorf("unknown resource kind %q in group %q", kind, group)
	}
	return schema.GroupVersionResource{}, false, fmt.Errorf("unknown resource kind %q; pass group for built-in non-core kinds if discovery is unavailable", kind)
}

func summarizeWorkloadRollout(obj *unstructured.Unstructured) map[string]any {
	if obj == nil {
		return nil
	}
	kind := strings.ToLower(obj.GetKind())
	if kind != "deployment" && kind != "statefulset" && kind != "daemonset" {
		return nil
	}

	out := map[string]any{
		"kind":       obj.GetKind(),
		"generation": obj.GetGeneration(),
	}
	if observed, ok, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration"); ok {
		out["observedGeneration"] = observed
		out["observedCurrentGeneration"] = observed >= obj.GetGeneration()
	}

	switch kind {
	case "deployment":
		desired := nestedInt64Default(obj, 1, "spec", "replicas")
		updated := nestedInt64Default(obj, 0, "status", "updatedReplicas")
		available := nestedInt64Default(obj, 0, "status", "availableReplicas")
		unavailable := nestedInt64Default(obj, 0, "status", "unavailableReplicas")
		out["desiredReplicas"] = desired
		out["updatedReplicas"] = updated
		out["availableReplicas"] = available
		out["unavailableReplicas"] = unavailable
		out["complete"] = generationObserved(out) && desired == updated && desired == available && unavailable == 0
	case "statefulset":
		desired := nestedInt64Default(obj, 1, "spec", "replicas")
		ready := nestedInt64Default(obj, 0, "status", "readyReplicas")
		updated := nestedInt64Default(obj, 0, "status", "updatedReplicas")
		out["desiredReplicas"] = desired
		out["readyReplicas"] = ready
		out["updatedReplicas"] = updated
		out["complete"] = generationObserved(out) && desired == ready && desired == updated
	case "daemonset":
		desired := nestedInt64Default(obj, 0, "status", "desiredNumberScheduled")
		updated := nestedInt64Default(obj, 0, "status", "updatedNumberScheduled")
		available := nestedInt64Default(obj, 0, "status", "numberAvailable")
		unavailable := nestedInt64Default(obj, 0, "status", "numberUnavailable")
		out["desiredNumberScheduled"] = desired
		out["updatedNumberScheduled"] = updated
		out["numberAvailable"] = available
		out["numberUnavailable"] = unavailable
		out["complete"] = generationObserved(out) && desired == updated && desired == available && unavailable == 0
	}

	if conditions := compactConditions(obj); len(conditions) > 0 {
		out["conditions"] = conditions
	}
	return out
}

func generationObserved(out map[string]any) bool {
	v, ok := out["observedCurrentGeneration"].(bool)
	return ok && v
}

func nestedInt64Default(obj *unstructured.Unstructured, fallback int64, fields ...string) int64 {
	if v, ok, _ := unstructured.NestedInt64(obj.Object, fields...); ok {
		return v
	}
	if v, ok, _ := unstructured.NestedFloat64(obj.Object, fields...); ok {
		return int64(v)
	}
	return fallback
}

func compactConditions(obj *unstructured.Unstructured) []map[string]any {
	conditions, ok, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(conditions))
	for _, raw := range conditions {
		cond, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		entry := map[string]any{}
		for _, key := range []string{"type", "status", "reason", "message"} {
			if val, ok := cond[key]; ok && val != "" {
				entry[key] = val
			}
		}
		if len(entry) > 0 {
			out = append(out, entry)
		}
	}
	return out
}

func summarizeSelectedPods(obj *unstructured.Unstructured) map[string]any {
	if obj == nil || obj.GetNamespace() == "" {
		return nil
	}
	kind := strings.ToLower(obj.GetKind())
	if kind != "deployment" && kind != "statefulset" && kind != "daemonset" {
		return nil
	}
	selectorMap, ok, _ := unstructured.NestedMap(obj.Object, "spec", "selector")
	if !ok {
		return nil
	}
	var selector metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(selectorMap, &selector); err != nil {
		return map[string]any{"error": fmt.Sprintf("invalid selector: %v", err)}
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	pods := cache.GetPodsForWorkload(obj.GetNamespace(), &selector)
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })

	readyCount := 0
	restartTotal := int64(0)
	phases := map[string]int{}
	summary := map[string]any{
		"total":    len(pods),
		"ready":    readyCount,
		"restarts": restartTotal,
		"phases":   phases,
	}
	rows := make([]map[string]any, 0, min(len(pods), 10))
	for _, pod := range pods {
		ready, restarts, waiting := podContainerStatus(pod)
		if ready {
			readyCount++
		}
		restartTotal += int64(restarts)
		phases[string(pod.Status.Phase)]++
		if len(rows) < 10 {
			row := map[string]any{
				"name":     pod.Name,
				"phase":    pod.Status.Phase,
				"ready":    ready,
				"restarts": restarts,
			}
			if waiting != "" {
				row["waiting"] = waiting
			}
			rows = append(rows, row)
		}
	}
	summary["ready"] = readyCount
	summary["restarts"] = restartTotal
	summary["items"] = rows
	if len(pods) > len(rows) {
		summary["truncated"] = true
	}
	return summary
}

func podContainerStatus(pod *corev1.Pod) (bool, int32, string) {
	if pod == nil {
		return false, 0, ""
	}
	if len(pod.Status.ContainerStatuses) == 0 {
		return pod.Status.Phase == corev1.PodRunning, 0, ""
	}
	allReady := true
	var restarts int32
	var waiting string
	for _, cs := range pod.Status.ContainerStatuses {
		if !cs.Ready {
			allReady = false
		}
		restarts += cs.RestartCount
		if waiting == "" && cs.State.Waiting != nil {
			waiting = cs.State.Waiting.Reason
		}
	}
	return allReady, restarts, waiting
}

func relatedIssuesForObject(obj *unstructured.Unstructured) []map[string]any {
	if obj == nil {
		return nil
	}
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}
	gvk := obj.GroupVersionKind()
	kind := gvk.Kind
	if kind == "" {
		kind = obj.GetKind()
	}
	var namespaces []string
	if obj.GetNamespace() != "" {
		namespaces = []string{obj.GetNamespace()}
	}
	matched := issues.RelatedIssues(provider, namespaces, gvk.Group, kind, obj.GetNamespace(), obj.GetName())
	if len(matched) == 0 {
		return nil
	}
	sort.Slice(matched, func(i, j int) bool {
		ri, rj := issues.SeverityRank(matched[i].Severity), issues.SeverityRank(matched[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return matched[i].Reason < matched[j].Reason
	})
	if len(matched) > 5 {
		matched = matched[:5]
	}
	out := make([]map[string]any, 0, len(matched))
	for _, issue := range matched {
		entry := map[string]any{
			"severity":  issue.Severity,
			"source":    issue.Source,
			"category":  issue.Category,
			"kind":      issue.Kind,
			"group":     issue.Group,
			"namespace": issue.Namespace,
			"name":      issue.Name,
			"reason":    issue.Reason,
			"message":   issue.Message,
		}
		if issue.Count > 0 {
			entry["count"] = issue.Count
		}
		out = append(out, entry)
	}
	return out
}
