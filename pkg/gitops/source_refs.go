package gitops

import (
	"slices"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// FluxSourceRef is a declared source dependency, distinct from managed objects.
type FluxSourceRef struct {
	resourceid.Ref
	Role string
}

// FluxSourceReferences follows the source contracts of Kustomization,
// HelmRelease and HelmChart. Missing required kind/name does not name a source.
// A HelmChart's local sourceRef never crosses its namespace.
func FluxSourceReferences(root *unstructured.Unstructured) []FluxSourceRef {
	if root == nil {
		return nil
	}
	var fields, kinds []string
	role, crossNamespace := "source", true
	switch resourceid.GroupFromAPIVersion(root.GetAPIVersion()) + "/" + root.GetKind() {
	case "kustomize.toolkit.fluxcd.io/Kustomization":
		fields = []string{"spec", "sourceRef"}
		kinds = []string{"GitRepository", "OCIRepository", "Bucket", "ExternalArtifact"}
	case "helm.toolkit.fluxcd.io/HelmRelease":
		if _, present, _ := unstructured.NestedMap(root.Object, "spec", "chartRef"); present {
			fields = []string{"spec", "chartRef"}
			kinds = []string{"HelmChart", "OCIRepository", "ExternalArtifact"}
			role = "chart reference"
		} else {
			fields = []string{"spec", "chart", "spec", "sourceRef"}
			kinds = []string{"HelmRepository", "GitRepository", "Bucket"}
			role = "chart source"
		}
	case "source.toolkit.fluxcd.io/HelmChart":
		fields = []string{"spec", "sourceRef"}
		kinds = []string{"HelmRepository", "GitRepository", "Bucket"}
		crossNamespace = false
	default:
		return nil
	}
	m, present, _ := unstructured.NestedMap(root.Object, fields...)
	if !present {
		return nil
	}
	kind, name := StringValue(m["kind"]), StringValue(m["name"])
	if name == "" || !slices.Contains(kinds, kind) {
		return nil
	}
	group := "source.toolkit.fluxcd.io"
	if version := StringValue(m["apiVersion"]); version != "" && resourceid.GroupFromAPIVersion(version) != group {
		return nil
	}
	namespace := root.GetNamespace()
	if crossNamespace && StringValue(m["namespace"]) != "" {
		namespace = StringValue(m["namespace"])
	}
	return []FluxSourceRef{{Ref: resourceid.NewRef(group, kind, namespace, name), Role: role}}
}
