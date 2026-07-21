package context

import (
	"encoding/json"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/prune"
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
	redactUnstructuredSecrets(obj)
	return obj
}

func pruneMapCompact(m map[string]any) {
	// Prune metadata — strip noise keys AND filter annotations
	// Metadata/spec/container key drops via the shared profiles; annotation
	// filtering + per-container conditional pruning stay local (below).
	prune.ApplyInPlace(m, detailBaseProfile)
	prune.ApplyInPlace(m, compactExtraProfile)
	if meta, ok := m["metadata"].(map[string]any); ok {
		pruneAnnotationsCompact(meta)
	}
	pruneSpecElementsCompact(m)

	// Per-type status pruning (same as Detail — savings come from spec)
	kind, _ := m["kind"].(string)
	if p, ok := statusProfileByKind[strings.ToLower(kind)]; ok {
		prune.ApplyInPlace(m, p)
	}
}

// pruneSpecElementsCompact handles conditional per-element container pruning;
// unconditional key drops ride the shared profiles' Drop + ElementDrops.
func pruneSpecElementsCompact(m map[string]any) {
	forEachContainer(m, pruneContainerConditionalCompact)
	sanitizeSpecEnvLists(m, true)
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
