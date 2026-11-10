package gitops

import (
	"slices"
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// FluxSourceRef identifies an artifact dependency, distinct from managed objects.
type FluxSourceRef struct {
	resourceid.Ref
	Role string
}

// FluxSourceReferences follows the source contracts of Kustomization,
// HelmRelease and HelmChart. Missing required kind/name does not name a source.
// Inline releases follow the controller-recorded status.helmChart when present.
// A HelmChart's local sourceRef never crosses its namespace.
func FluxSourceReferences(root *unstructured.Unstructured) []FluxSourceRef {
	if root == nil {
		return nil
	}
	var fields, kinds []string
	var chartRefs []FluxSourceRef
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
			if _, inline, _ := unstructured.NestedMap(root.Object, "spec", "chart"); inline {
				chart, _, _ := unstructured.NestedString(root.Object, "status", "helmChart")
				namespace, name, qualified := strings.Cut(chart, "/")
				if qualified && namespace != "" && name != "" && !strings.Contains(name, "/") {
					chartRefs = []FluxSourceRef{{Ref: resourceid.NewRef("source.toolkit.fluxcd.io", "HelmChart", namespace, name), Role: "chart reference"}}
				}
			}
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
		return chartRefs
	}
	kind, name := StringValue(m["kind"]), StringValue(m["name"])
	if name == "" || !slices.Contains(kinds, kind) {
		return chartRefs
	}
	group := "source.toolkit.fluxcd.io"
	if version := StringValue(m["apiVersion"]); version != "" && resourceid.GroupFromAPIVersion(version) != group {
		return nil
	}
	namespace := root.GetNamespace()
	if crossNamespace && StringValue(m["namespace"]) != "" {
		namespace = StringValue(m["namespace"])
	}
	return append([]FluxSourceRef{{Ref: resourceid.NewRef(group, kind, namespace, name), Role: role}}, chartRefs...)
}
