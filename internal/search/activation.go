package search

import (
	"encoding/json"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
)

// CEL sees the same sanitized detail object as resource output, so predicates
// cannot recover Secret bodies or values removed by minification.
func objectActivation(obj runtime.Object, kind string) (map[string]any, error) {
	obj = obj.DeepCopyObject()
	k8s.SetTypeMeta(obj)
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return sanitizedActivation(m, kind), nil
}

// unstructuredActivation builds the activation map for a CRD object.
// We use a deep copy of u.Object so the in-place number normalization
// doesn't mutate the cache-held unstructured.
func unstructuredActivation(u *unstructured.Unstructured, kind string) map[string]any {
	if u == nil || u.Object == nil {
		return nil
	}
	m := u.DeepCopy().Object
	return sanitizedActivation(m, kind)
}

// normalizeNumbers walks v and converts every whole-number float64 to
// int64 in-place. CEL's int and double types don't share arithmetic
// overloads, so a JSON-roundtripped `spec.replicas` (float64) errors
// on `+ 1` (int literal) instead of evaluating. Recursion order
// doesn't matter — we mutate slice elements and map values directly.
func normalizeNumbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			x[k] = normalizeNumbers(vv)
		}
		return x
	case []any:
		for i, vv := range x {
			x[i] = normalizeNumbers(vv)
		}
		return x
	case float64:
		if x == float64(int64(x)) {
			return int64(x)
		}
		return x
	default:
		return v
	}
}

// assembleActivation projects the JSON-shaped object into the bound
// variable names. Keys missing from the object resolve to empty
// values so `has()` guards work as expected.
func sanitizedActivation(obj map[string]any, kind string) map[string]any {
	if kind == "Secret" {
		delete(obj, "data")
		delete(obj, "stringData")
	}
	m := aicontext.MinifyUnstructured(&unstructured.Unstructured{Object: obj}, aicontext.LevelDetail).(map[string]any)
	normalizeNumbers(m)
	return assembleActivation(m, kind)
}

func assembleActivation(obj map[string]any, kind string) map[string]any {
	obj["kind"] = firstString(obj["kind"], kind)
	metadata := asMap(obj["metadata"])
	if _, ok := metadata["namespace"]; !ok {
		metadata["namespace"] = ""
	}
	obj["metadata"] = metadata
	out := map[string]any{
		"object":      obj,
		"kind":        firstString(obj["kind"], kind),
		"apiVersion":  asString(obj["apiVersion"]),
		"metadata":    asMap(obj["metadata"]),
		"spec":        asMap(obj["spec"]),
		"status":      asMap(obj["status"]),
		"labels":      asStringMap(getNested(obj, "metadata", "labels")),
		"annotations": asStringMap(getNested(obj, "metadata", "annotations")),
	}
	return out
}

// issueActivation builds the activation for an Issue-row CEL filter.
// Mirrors envIssue's variable declarations.
func issueActivation(severity, source, kind, group, namespace, name, reason, message, cluster string, count int, lastSeenUnix int64) map[string]any {
	return map[string]any{
		"severity":  severity,
		"source":    source,
		"kind":      kind,
		"group":     group,
		"namespace": namespace,
		"name":      name,
		"reason":    reason,
		"message":   message,
		"count":     int64(count),
		"cluster":   cluster,
		"last_seen": lastSeenUnix,
	}
}

func firstString(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func getNested(obj map[string]any, keys ...string) any {
	cur := any(obj)
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

func asStringMap(v any) map[string]string {
	switch m := v.(type) {
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, val := range m {
			if s, ok := val.(string); ok {
				out[k] = s
			}
		}
		return out
	case map[string]string:
		return m
	}
	return map[string]string{}
}
