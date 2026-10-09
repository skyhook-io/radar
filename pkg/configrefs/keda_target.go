package configrefs

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// KEDAScaleTarget returns a ScaledObject's scale target with KEDA's defaults:
// an omitted kind is Deployment and an omitted apiVersion is apps/v1, so a
// bare `kind: Rollout` names an apps Rollout (which KEDA cannot find), never
// the Argo Rollout of the same name.
func KEDAScaleTarget(so *unstructured.Unstructured) (apiVersion, kind, name string, ok bool) {
	name, _, _ = unstructured.NestedString(so.Object, "spec", "scaleTargetRef", "name")
	if name == "" {
		return "", "", "", false
	}
	kind, _, _ = unstructured.NestedString(so.Object, "spec", "scaleTargetRef", "kind")
	if kind == "" {
		kind = "Deployment"
	}
	apiVersion, _, _ = unstructured.NestedString(so.Object, "spec", "scaleTargetRef", "apiVersion")
	if apiVersion == "" {
		apiVersion = "apps/v1"
	}
	return apiVersion, kind, name, true
}
