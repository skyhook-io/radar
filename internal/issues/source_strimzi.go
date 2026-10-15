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
	if !strimziStatusCurrent(u) {
		return nil
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	notReady := false
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok || c["status"] != "True" {
			continue
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

func strimziStatusCurrent(u *unstructured.Unstructured) bool {
	if u == nil || u.GetDeletionTimestamp() != nil || strings.EqualFold(u.GetAnnotations()["strimzi.io/pause-reconciliation"], "true") {
		return false
	}
	observed, ok, err := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	if err != nil || !ok || observed <= 0 || observed != u.GetGeneration() {
		return false
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, raw := range conds {
		if c, ok := raw.(map[string]any); ok && c["type"] == "ReconciliationPaused" && c["status"] == "True" {
			return false
		}
	}
	return true
}

func StrimziConnectorRecoveryObserved(u *unstructured.Unstructured) bool {
	if !strimziStatusCurrent(u) {
		return false
	}
	conds, found, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	ready := false
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		if c["type"] == "NotReady" && c["status"] == "True" {
			return false
		}
		if c["type"] == "Ready" && c["status"] == "True" {
			ready = true
		}
	}
	state, found, err := unstructured.NestedString(u.Object, "status", "connectorStatus", "connector", "state")
	if !ready || err != nil || !found || state != "RUNNING" {
		return false
	}
	tasks, found, err := unstructured.NestedSlice(u.Object, "status", "connectorStatus", "tasks")
	if err != nil || !found || len(tasks) == 0 {
		return false
	}
	for _, raw := range tasks {
		task, ok := raw.(map[string]any)
		if !ok || task["state"] != "RUNNING" {
			return false
		}
	}
	return true
}

type StrimziSnapshotAssessment struct {
	State              string `json:"state"`
	Reason             string `json:"reason,omitempty"`
	Readiness          string `json:"readiness"`
	GenerationCurrent  bool   `json:"generationCurrent"`
	ObservedGeneration *int64 `json:"observedGeneration,omitempty"`
}

func AssessStrimziSnapshot(gvr schema.GroupVersionResource, u *unstructured.Unstructured) StrimziSnapshotAssessment {
	out := StrimziSnapshotAssessment{State: "unknown", Reason: "missing_or_malformed", Readiness: "unknown"}
	if u == nil {
		return out
	}
	observed, found, err := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	if err == nil && found {
		out.ObservedGeneration = &observed
	}
	if u.GetDeletionTimestamp() != nil {
		out.Reason = "terminating"
		out.Readiness = "terminating"
		return out
	}
	if strings.EqualFold(u.GetAnnotations()["strimzi.io/pause-reconciliation"], "true") {
		out.Reason = "paused"
		out.Readiness = "paused"
		return out
	}
	if err != nil || !found || observed <= 0 {
		return out
	}
	if observed != u.GetGeneration() {
		out.Reason = "stale"
		return out
	}
	out.GenerationCurrent = true
	conds, conditionsFound, conditionsErr := unstructured.NestedSlice(u.Object, "status", "conditions")
	validConditions := conditionsFound && conditionsErr == nil
	ready, notReady := false, false
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok {
			validConditions = false
			continue
		}
		if c["type"] == "ReconciliationPaused" && c["status"] == "True" {
			out.Reason = "paused"
			out.Readiness = "paused"
			return out
		}
		if c["type"] == "Ready" {
			ready = c["status"] == "True"
			notReady = notReady || c["status"] == "False"
		}
		if c["type"] == "NotReady" && c["status"] == "True" {
			notReady = true
		}
	}
	if validConditions {
		if notReady {
			out.Readiness = "not_ready"
		} else if ready {
			out.Readiness = "ready"
		}
	}
	if u.GetKind() == "KafkaConnector" {
		if len(detectStrimziConnectorIssues(gvr, u)) > 0 {
			out.State = "failing"
			out.Reason = "operator_snapshot"
			return out
		}
		if StrimziConnectorRecoveryObserved(u) {
			out.State = "recovered"
			out.Reason = ""
			return out
		}
		state, _, _ := unstructured.NestedString(u.Object, "status", "connectorStatus", "connector", "state")
		if state == "PAUSED" || state == "STOPPED" {
			out.Reason = "inactive"
		} else {
			out.Reason = "incomplete_runtime"
		}
		return out
	}
	if out.Readiness == "ready" || out.Readiness == "not_ready" {
		out.State = "observed"
		out.Reason = ""
	}
	return out
}
