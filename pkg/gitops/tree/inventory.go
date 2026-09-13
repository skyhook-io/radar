package tree

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/gitops"
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
		kind := gitops.StringValue(m["kind"])
		name := gitops.StringValue(m["name"])
		if kind == "" || name == "" {
			continue
		}
		ref := ResourceRef{
			Group:     gitops.StringValue(m["group"]),
			Kind:      kind,
			Namespace: gitops.StringValue(m["namespace"]),
			Name:      name,
		}
		health := ""
		if hm, ok := m["health"].(map[string]any); ok {
			health = gitops.StringValue(hm["status"])
		}
		out = append(out, managedResource{
			Ref:    ref,
			Sync:   normalizeSync(gitops.StringValue(m["status"])),
			Health: normalizeHealth(health),
			Data: map[string]any{
				"hook":      gitops.StringValue(m["hook"]),
				"syncWave":  gitops.StringValue(m["syncWave"]),
				"syncPhase": gitops.StringValue(m["syncPhase"]),
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
		ref, ok := parseFluxInventoryID(gitops.StringValue(m["id"]))
		if ok {
			out = append(out, managedResource{Ref: ref, Data: map[string]any{"version": gitops.StringValue(m["v"])}})
		}
	}
	return out
}

func parseFluxInventoryID(id string) (ResourceRef, bool) {
	group, kind, namespace, name, ok := gitops.ParseFluxInventoryID(id)
	if !ok {
		return ResourceRef{}, false
	}
	return ResourceRef{Group: group, Kind: kind, Namespace: namespace, Name: name}, true
}

func rootStatus(root *unstructured.Unstructured, tool Tool) gitOpsStatus {
	if tool == ToolArgoCD {
		sync, _, _ := unstructured.NestedString(root.Object, "status", "sync", "status")
		health, _, _ := unstructured.NestedString(root.Object, "status", "health", "status")
		return gitOpsStatus{Sync: normalizeSync(sync), Health: normalizeHealth(health)}
	}
	st := gitops.FluxStatus(root)
	return gitOpsStatus{Sync: st.Sync, Health: st.Health}
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
