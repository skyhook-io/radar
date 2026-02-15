package context

import (
	"encoding/json"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// minifyDetail strips metadata noise and per-type status noise, but keeps full spec
// and all annotations/labels. For MCP get_resource and YAML review.
func minifyDetail(obj runtime.Object) (map[string]any, error) {
	// Handle Secrets specially — never include data/stringData
	if secret, ok := obj.(*corev1.Secret); ok {
		return minifySecretDetail(secret), nil
	}

	data, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	pruneMapDetail(m)
	return m, nil
}

// minifyDetailUnstructured applies Detail-level pruning to an unstructured resource.
func minifyDetailUnstructured(obj map[string]any) map[string]any {
	pruneMapDetail(obj)
	return obj
}

func pruneMapDetail(m map[string]any) {
	// Prune metadata — strip noise keys but keep ALL annotations and labels
	if meta, ok := m["metadata"].(map[string]any); ok {
		pruneMetadataCommon(meta)
		// At Detail level: annotations are NOT filtered (all kept)
	}

	// Prune spec — strip noisy pod spec fields
	if spec, ok := m["spec"].(map[string]any); ok {
		pruneSpecDetail(spec)
	}

	// Per-type status pruning
	kind, _ := m["kind"].(string)
	if status, ok := m["status"].(map[string]any); ok {
		pruneStatusForKind(strings.ToLower(kind), status)
	}
}

func pruneSpecDetail(spec map[string]any) {
	// Strip noisy pod spec fields
	for key := range stripPodSpecFields {
		delete(spec, key)
	}

	// Prune template.spec (for Deployments, StatefulSets, etc.)
	if template, ok := spec["template"].(map[string]any); ok {
		if tSpec, ok := template["spec"].(map[string]any); ok {
			prunePodSpec(tSpec)
		}
	}

	// Direct pod spec (for Pod resources)
	pruneContainersInSpec(spec)
}

func pruneStatusForKind(kind string, status map[string]any) {
	switch kind {
	case "pod":
		pruneStatusPod(status)
	case "deployment", "statefulset", "daemonset", "replicaset":
		pruneStatusWorkload(status)
	}
}

func minifySecretDetail(secret *corev1.Secret) map[string]any {
	keys := make([]string, 0, len(secret.Data)+len(secret.StringData))
	for k := range secret.Data {
		keys = append(keys, k)
	}
	for k := range secret.StringData {
		keys = append(keys, k)
	}

	result := map[string]any{
		"kind":      "Secret",
		"name":      secret.Name,
		"namespace": secret.Namespace,
		"type":      string(secret.Type),
		"keys":      keys,
	}

	if len(secret.Labels) > 0 {
		result["labels"] = secret.Labels
	}
	if len(secret.Annotations) > 0 {
		result["annotations"] = secret.Annotations
	}

	return result
}
