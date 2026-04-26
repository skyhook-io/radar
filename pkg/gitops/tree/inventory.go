package tree

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type gitOpsStatus struct {
	Sync   string
	Health string
}

func parseArgoManagedResources(root *unstructured.Unstructured) []managedResource {
	raw, ok, _ := unstructured.NestedSlice(root.Object, "status", "resources")
	if !ok {
		return nil
	}
	out := make([]managedResource, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind := stringValue(m["kind"])
		name := stringValue(m["name"])
		if kind == "" || name == "" {
			continue
		}
		ref := ResourceRef{
			Group:     stringValue(m["group"]),
			Kind:      kind,
			Namespace: stringValue(m["namespace"]),
			Name:      name,
		}
		health := ""
		if hm, ok := m["health"].(map[string]any); ok {
			health = stringValue(hm["status"])
		}
		out = append(out, managedResource{
			Ref:    ref,
			Sync:   normalizeSync(stringValue(m["status"])),
			Health: normalizeHealth(health),
			Data: map[string]any{
				"hook":      stringValue(m["hook"]),
				"syncWave":  stringValue(m["syncWave"]),
				"syncPhase": stringValue(m["syncPhase"]),
			},
		})
	}
	return out
}

func parseFluxManagedResources(root *unstructured.Unstructured) []managedResource {
	raw, ok, _ := unstructured.NestedSlice(root.Object, "status", "inventory", "entries")
	if !ok {
		return nil
	}
	out := make([]managedResource, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ref, ok := parseFluxInventoryID(stringValue(m["id"]))
		if ok {
			out = append(out, managedResource{Ref: ref, Data: map[string]any{"version": stringValue(m["v"])}})
		}
	}
	return out
}

func parseFluxInventoryID(id string) (ResourceRef, bool) {
	parts := strings.Split(id, "_")
	if len(parts) < 4 {
		return ResourceRef{}, false
	}
	kind := parts[len(parts)-1]
	group := parts[len(parts)-2]
	namespace := parts[0]
	name := strings.Join(parts[1:len(parts)-2], "_")
	if kind == "" || name == "" {
		return ResourceRef{}, false
	}
	if group == "core" {
		group = ""
	}
	return ResourceRef{Group: group, Kind: kind, Namespace: namespace, Name: name}, true
}

func rootStatus(root *unstructured.Unstructured, tool Tool) gitOpsStatus {
	if tool == ToolArgoCD {
		sync, _, _ := unstructured.NestedString(root.Object, "status", "sync", "status")
		health, _, _ := unstructured.NestedString(root.Object, "status", "health", "status")
		return gitOpsStatus{Sync: normalizeSync(sync), Health: normalizeHealth(health)}
	}
	conditions, _, _ := unstructured.NestedSlice(root.Object, "status", "conditions")
	ready := ""
	reconciling := false
	stalled := false
	for _, item := range conditions {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		t := stringValue(m["type"])
		s := stringValue(m["status"])
		if t == "Ready" {
			ready = s
		}
		if t == "Reconciling" && s == "True" {
			reconciling = true
		}
		if t == "Stalled" && s == "True" {
			stalled = true
		}
	}
	if root.Object["spec"] != nil {
		if suspended, _, _ := unstructured.NestedBool(root.Object, "spec", "suspend"); suspended {
			return gitOpsStatus{Sync: "Unknown", Health: "Suspended"}
		}
	}
	if reconciling {
		return gitOpsStatus{Sync: "Reconciling", Health: "Progressing"}
	}
	if stalled {
		return gitOpsStatus{Sync: "OutOfSync", Health: "Degraded"}
	}
	if ready == "True" {
		return gitOpsStatus{Sync: "Synced", Health: "Healthy"}
	}
	if ready == "False" {
		return gitOpsStatus{Sync: "OutOfSync", Health: "Degraded"}
	}
	return gitOpsStatus{Sync: "Unknown", Health: "Unknown"}
}

func normalizeSync(status string) string {
	switch status {
	case "Synced", "OutOfSync", "Reconciling":
		return status
	case "":
		return ""
	default:
		return "Unknown"
	}
}

func normalizeHealth(status string) string {
	switch status {
	case "Healthy", "Progressing", "Degraded", "Suspended", "Missing", "Unknown":
		return status
	case "":
		return ""
	default:
		return "Unknown"
	}
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
