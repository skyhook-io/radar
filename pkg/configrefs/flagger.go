package configrefs

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// FlaggerAnalysis follows the controller's preference for spec.analysis over
// the still-served spec.canaryAnalysis. The returned map is borrowed: read only.
func FlaggerAnalysis(canary *unstructured.Unstructured) map[string]any {
	if canary == nil {
		return nil
	}
	if analysis, ok, _ := unstructured.NestedFieldNoCopy(canary.Object, "spec", "analysis"); ok && analysis != nil {
		m, _ := analysis.(map[string]any)
		return m
	}
	analysis, _, _ := unstructured.NestedFieldNoCopy(canary.Object, "spec", "canaryAnalysis")
	m, _ := analysis.(map[string]any)
	return m
}
