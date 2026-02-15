package context

import (
	"encoding/json"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// minifyCompact aggressively prunes both spec and status for token-constrained RCA prompts.
// Internal only — never exposed to MCP agents.
func minifyCompact(obj runtime.Object) (map[string]any, error) {
	// Handle Secrets specially — never include data/stringData
	if secret, ok := obj.(*corev1.Secret); ok {
		return minifySecretCompact(secret), nil
	}

	data, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	pruneMapCompact(m)
	return m, nil
}

// minifyCompactUnstructured applies Compact-level pruning to an unstructured resource.
func minifyCompactUnstructured(obj map[string]any) map[string]any {
	pruneMapCompact(obj)
	return obj
}

func pruneMapCompact(m map[string]any) {
	// Prune metadata — strip noise keys AND filter annotations
	if meta, ok := m["metadata"].(map[string]any); ok {
		pruneMetadataCommon(meta)
		pruneAnnotationsCompact(meta)
	}

	// Aggressively prune spec
	if spec, ok := m["spec"].(map[string]any); ok {
		pruneSpecCompact(spec)
	}

	// Per-type status pruning (same as Detail — savings come from spec)
	kind, _ := m["kind"].(string)
	if status, ok := m["status"].(map[string]any); ok {
		pruneStatusForKind(strings.ToLower(kind), status)
	}
}

func pruneSpecCompact(spec map[string]any) {
	// Strip all noisy pod spec fields (basic + compact)
	for key := range stripPodSpecFields {
		delete(spec, key)
	}
	for key := range stripPodSpecFieldsCompact {
		delete(spec, key)
	}

	// Prune template.spec (for Deployments, StatefulSets, etc.)
	if template, ok := spec["template"].(map[string]any); ok {
		if tSpec, ok := template["spec"].(map[string]any); ok {
			prunePodSpecCompact(tSpec)
		}
	}

	// Direct pod spec (for Pod resources)
	pruneContainersCompact(spec, "containers")
	pruneContainersCompact(spec, "initContainers")
}

func minifySecretCompact(secret *corev1.Secret) map[string]any {
	keys := make([]string, 0, len(secret.Data)+len(secret.StringData))
	for k := range secret.Data {
		keys = append(keys, k)
	}
	for k := range secret.StringData {
		keys = append(keys, k)
	}
	return map[string]any{
		"kind":      "Secret",
		"name":      secret.Name,
		"namespace": secret.Namespace,
		"type":      string(secret.Type),
		"keys":      keys,
	}
}
