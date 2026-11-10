package configrefs

import (
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// KEDAAuthenticationReferences extracts trigger authentication declarations.
// KEDA defines omitted kind as TriggerAuthentication in the scaler namespace;
// ClusterTriggerAuthentication is cluster-scoped. Credential scope is separate.
func KEDAAuthenticationReferences(scaler *unstructured.Unstructured) []resourceid.Ref {
	if scaler == nil || resourceid.GroupFromAPIVersion(scaler.GetAPIVersion()) != "keda.sh" || scaler.GetKind() != "ScaledObject" && scaler.GetKind() != "ScaledJob" {
		return nil
	}
	triggers, _, _ := unstructured.NestedSlice(scaler.Object, "spec", "triggers")
	seen := map[resourceid.Ref]bool{}
	var refs []resourceid.Ref
	for _, value := range triggers {
		trigger, ok := value.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(trigger, "authenticationRef", "name")
		kind, _, _ := unstructured.NestedString(trigger, "authenticationRef", "kind")
		if kind == "" {
			kind = "TriggerAuthentication"
		}
		if name == "" || kind != "TriggerAuthentication" && kind != "ClusterTriggerAuthentication" {
			continue
		}
		namespace := scaler.GetNamespace()
		if kind == "ClusterTriggerAuthentication" {
			namespace = ""
		}
		ref := resourceid.NewRef("keda.sh", kind, namespace, name)
		if !seen[ref] {
			refs = append(refs, ref)
			seen[ref] = true
		}
	}
	return refs
}
