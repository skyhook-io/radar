package configrefs

import (
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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

// FlaggerTargetReference resolves the supported native clients selected by
// targetRef.kind. Flagger ignores apiVersion except for Knative Service v1.
// Unsupported kinds are left unresolved rather than assuming its fallback.
func FlaggerTargetReference(canary *unstructured.Unstructured) (resourceid.Ref, bool) {
	if canary == nil || canary.GroupVersionKind().Group != "flagger.app" || canary.GetKind() != "Canary" {
		return resourceid.Ref{}, false
	}
	kind, _, _ := unstructured.NestedString(canary.Object, "spec", "targetRef", "kind")
	name, _, _ := unstructured.NestedString(canary.Object, "spec", "targetRef", "name")
	group := "apps"
	switch kind {
	case "Deployment", "DaemonSet":
	case "Service":
		group = ""
		version, _, _ := unstructured.NestedString(canary.Object, "spec", "targetRef", "apiVersion")
		if version == "serving.knative.dev/v1" {
			group = "serving.knative.dev"
		}
	default:
		return resourceid.Ref{}, false
	}
	return resourceid.Ref{Group: group, Kind: kind, Namespace: canary.GetNamespace(), Name: name}, name != ""
}

// FlaggerMetricTemplateReferences uses the MetricTemplate client regardless of
// templateRef's kind/version and defaults namespace to the Canary's namespace.
func FlaggerMetricTemplateReferences(canary *unstructured.Unstructured) []resourceid.Ref {
	if canary == nil || canary.GroupVersionKind().Group != "flagger.app" || canary.GetKind() != "Canary" {
		return nil
	}
	metrics, _, _ := unstructured.NestedSlice(FlaggerAnalysis(canary), "metrics")
	seen := map[resourceid.Ref]bool{}
	var refs []resourceid.Ref
	for _, value := range metrics {
		metric, ok := value.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(metric, "templateRef", "name")
		if name == "" {
			continue
		}
		namespace, _, _ := unstructured.NestedString(metric, "templateRef", "namespace")
		if namespace == "" {
			namespace = canary.GetNamespace()
		}
		ref := resourceid.Ref{Group: "flagger.app", Kind: "MetricTemplate", Namespace: namespace, Name: name}
		if !seen[ref] {
			refs = append(refs, ref)
			seen[ref] = true
		}
	}
	return refs
}

// FlaggerProviderSecretReference names the Secret fetched in the referenced
// MetricTemplate's namespace, not the namespace of the consuming Canary.
func FlaggerProviderSecretReference(template *unstructured.Unstructured) (resourceid.Ref, bool) {
	if template == nil || template.GroupVersionKind().Group != "flagger.app" || template.GetKind() != "MetricTemplate" {
		return resourceid.Ref{}, false
	}
	name, _, _ := unstructured.NestedString(template.Object, "spec", "provider", "secretRef", "name")
	return resourceid.Ref{Kind: "Secret", Namespace: template.GetNamespace(), Name: name}, name != ""
}
