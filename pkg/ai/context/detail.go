package context

import (
	"encoding/json"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/prune"
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
	redactUnstructuredSecrets(obj)
	return obj
}

// redactUnstructuredSecrets redacts inline secret-shaped values in a CRD's
// spec/status. Core Secret bodies are handled structurally elsewhere; this is
// the only value-level pass that reaches arbitrary CRD specs (e.g. a Traefik
// Middleware basicAuth user) before they go to an LLM.
func redactUnstructuredSecrets(obj map[string]any) {
	if spec, ok := obj["spec"]; ok {
		RedactInlineSecrets(spec)
	}
	if status, ok := obj["status"]; ok {
		RedactInlineSecrets(status)
	}
}

func pruneMapDetail(m map[string]any) {
	// Metadata/spec/container key drops via the shared profiles (annotations
	// NOT filtered at Detail level). Per-container conditional pruning
	// (imagePullPolicy rule, env redaction) can't be a path drop — below.
	prune.ApplyInPlace(m, detailBaseProfile)
	pruneSpecElements(m)

	kind, _ := m["kind"].(string)
	if p, ok := statusProfileByKind[strings.ToLower(kind)]; ok {
		prune.ApplyInPlace(m, p)
	}
}

// pruneSpecElements handles what the shared profiles can't: conditional
// per-element pruning inside container slices (unconditional key drops ride
// detailBaseProfile's Drop + ElementDrops).
func pruneSpecElements(m map[string]any) {
	forEachContainer(m, pruneContainerConditional)
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
