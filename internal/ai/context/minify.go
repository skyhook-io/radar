package context

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// VerbosityLevel controls how much detail is retained when minifying resources.
type VerbosityLevel int

const (
	// LevelSummary produces a typed struct with identity + health at a glance.
	// Used by MCP list_resources.
	LevelSummary VerbosityLevel = iota

	// LevelCompact aggressively prunes spec while keeping diagnostic status.
	// Internal only — used by AssembleContext for RCA with local LLMs.
	LevelCompact

	// LevelDetail keeps full spec with metadata noise and per-type status pruning.
	// Used by MCP get_resource and YAML review.
	LevelDetail
)

// Minify converts a typed K8s resource at the requested verbosity level.
// Summary returns *ResourceSummary, Detail and Compact return map[string]any.
func Minify(obj runtime.Object, level VerbosityLevel) (any, error) {
	switch level {
	case LevelSummary:
		return summarize(obj)
	case LevelDetail:
		return minifyDetail(obj)
	case LevelCompact:
		return minifyCompact(obj)
	default:
		return nil, fmt.Errorf("unknown verbosity level: %d", level)
	}
}

// MinifyUnstructured minifies a CRD/dynamic resource at the requested level.
func MinifyUnstructured(obj *unstructured.Unstructured, level VerbosityLevel) any {
	switch level {
	case LevelSummary:
		return summarizeUnstructured(obj)
	case LevelDetail:
		return minifyDetailUnstructured(obj.DeepCopy().Object)
	case LevelCompact:
		return minifyCompactUnstructured(obj.DeepCopy().Object)
	default:
		return minifyCompactUnstructured(obj.DeepCopy().Object)
	}
}

// MinifyList minifies a slice of resources at the requested level.
func MinifyList(objs []runtime.Object, level VerbosityLevel) ([]any, error) {
	results := make([]any, 0, len(objs))
	for _, obj := range objs {
		m, err := Minify(obj, level)
		if err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	return results, nil
}

// MinifyResource is the backward-compatible wrapper. Returns Compact level.
func MinifyResource(obj runtime.Object) (map[string]any, error) {
	result, err := minifyCompact(obj)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// MinifyResourceUnstructured is the backward-compatible wrapper for unstructured resources.
func MinifyResourceUnstructured(obj *unstructured.Unstructured) map[string]any {
	return minifyCompactUnstructured(obj.DeepCopy().Object)
}
