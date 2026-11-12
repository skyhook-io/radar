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
	// Alias is the name an ArtifactGenerator gives a source in its copy rules.
	Alias string
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
	case "source.toolkit.fluxcd.io/ExternalArtifact":
		return externalArtifactGenerator(root)
	case "source.extensions.fluxcd.io/ArtifactGenerator":
		return artifactGeneratorSources(root)
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

// externalArtifactGenerator follows an ExternalArtifact to the ArtifactGenerator
// that produces it; another producer (or another group) names no generator.
func externalArtifactGenerator(ea *unstructured.Unstructured) []FluxSourceRef {
	m, _, _ := unstructured.NestedMap(ea.Object, "spec", "sourceRef")
	const group = "source.extensions.fluxcd.io"
	name := StringValue(m["name"])
	if name == "" || StringValue(m["kind"]) != "ArtifactGenerator" {
		return nil
	}
	if version := StringValue(m["apiVersion"]); version != "" && resourceid.GroupFromAPIVersion(version) != group {
		return nil
	}
	namespace := StringValue(m["namespace"])
	if namespace == "" {
		namespace = ea.GetNamespace()
	}
	return []FluxSourceRef{{Ref: resourceid.NewRef(group, "ArtifactGenerator", namespace, name), Role: "generator"}}
}

// artifactGeneratorSources lists the Flux sources an ArtifactGenerator composes.
func artifactGeneratorSources(gen *unstructured.Unstructured) []FluxSourceRef {
	sources, _, _ := unstructured.NestedSlice(gen.Object, "spec", "sources")
	var out []FluxSourceRef
	for _, raw := range sources {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind, name := StringValue(m["kind"]), StringValue(m["name"])
		if name == "" || !slices.Contains([]string{"GitRepository", "OCIRepository", "Bucket"}, kind) {
			continue
		}
		namespace := StringValue(m["namespace"])
		if namespace == "" {
			namespace = gen.GetNamespace()
		}
		out = append(out, FluxSourceRef{Ref: resourceid.NewRef("source.toolkit.fluxcd.io", kind, namespace, name), Role: "source", Alias: StringValue(m["alias"])})
	}
	return out
}

// MaxFluxSourceLevels bounds a walk up a Flux source chain from what a root
// references (ExternalArtifact → ArtifactGenerator → its sources).
const MaxFluxSourceLevels = 3

// IsUpstreamFluxSource reports whether a source names further sources, so a
// walk up the chain continues from it.
func IsUpstreamFluxSource(group, kind string) bool {
	switch group {
	case "source.toolkit.fluxcd.io":
		return kind == "HelmChart" || kind == "ExternalArtifact"
	case "source.extensions.fluxcd.io":
		return kind == "ArtifactGenerator"
	}
	return false
}
