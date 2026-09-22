package issues

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func detectStrimziConnectorIssues(gvr schema.GroupVersionResource, u *unstructured.Unstructured) []Issue {
	if u == nil || u.GetDeletionTimestamp() != nil || strings.EqualFold(u.GetAnnotations()["strimzi.io/pause-reconciliation"], "true") {
		return nil
	}
	observed, ok, err := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	if err != nil || !ok || observed <= 0 || observed != u.GetGeneration() {
		return nil
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	notReady := false
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok || c["status"] != "True" {
			continue
		}
		if c["type"] == "ReconciliationPaused" {
			return nil
		}
		if c["type"] == "NotReady" {
			notReady = true
		}
	}
	state, _, _ := unstructured.NestedString(u.Object, "status", "connectorStatus", "connector", "state")
	tasks, _, _ := unstructured.NestedSlice(u.Object, "status", "connectorStatus", "tasks")
	ids := map[int64]bool{}
	failedTask := false
	for _, raw := range tasks {
		task, ok := raw.(map[string]any)
		if !ok || task["state"] != "FAILED" {
			continue
		}
		failedTask = true
		if id, ok := task["id"].(int64); ok && id >= 0 {
			ids[id] = true
		}
	}
	reason, message := "", ""
	switch {
	case state == "FAILED" && failedTask:
		reason, message = "StrimziConnectorFailed", "Strimzi reports the connector and one or more tasks as FAILED"
	case state == "FAILED":
		reason, message = "StrimziConnectorFailed", "Strimzi reports the connector as FAILED"
	case failedTask:
		reason, message = "StrimziConnectorTasksFailed", "Strimzi reports one or more connector tasks as FAILED"
	case notReady:
		reason, message = "StrimziConnectorNotReady", "Strimzi reports this connector as NotReady; inspect the connector and operator details"
	default:
		return nil
	}
	if failedTask && len(ids) > 0 {
		ordered := make([]int64, 0, len(ids))
		for id := range ids {
			ordered = append(ordered, id)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
		const limit = 10
		shown := ordered
		if len(shown) > limit {
			shown = shown[:limit]
		}
		names := make([]string, len(shown))
		for i, id := range shown {
			names[i] = strconv.FormatInt(id, 10)
		}
		message += " (reported task IDs: " + strings.Join(names, ", ")
		if len(ordered) > limit {
			message += fmt.Sprintf("; %d more", len(ordered)-limit)
		}
		message += ")"
	}
	message += ". This is an operator snapshot; runtime state may have changed."
	// Strimzi can rewrite condition timestamps during reconciliation; neither
	// they nor connectorStatus establish when the runtime failure began.
	return []Issue{newConditionIssue(gvr, "KafkaConnector", u.GetNamespace(), u.GetName(), SeverityWarning, reason, message, time.Time{}, false, "", u.GetCreationTimestamp().Time)}
}
