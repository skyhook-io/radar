package gitops

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// FluxSourceRef identifies an artifact dependency, distinct from managed objects.
type FluxSourceRef struct {
	resourceid.Ref
	Role string
	// Path is the field that names the source.
	Path string
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
		if raw, _, _ := unstructured.NestedFieldNoCopy(root.Object, "spec", "chartRef"); isSourceMap(raw) {
			fields = []string{"spec", "chartRef"}
			kinds = []string{"HelmChart", "OCIRepository", "ExternalArtifact"}
			role = "chart reference"
		} else {
			if raw, _, _ := unstructured.NestedFieldNoCopy(root.Object, "spec", "chart"); isSourceMap(raw) {
				chart, _, _ := unstructured.NestedString(root.Object, "status", "helmChart")
				namespace, name, qualified := strings.Cut(chart, "/")
				if qualified && namespace != "" && name != "" && !strings.Contains(name, "/") {
					chartRefs = []FluxSourceRef{{Ref: resourceid.NewRef("source.toolkit.fluxcd.io", "HelmChart", namespace, name), Role: "chart reference", Path: "status.helmChart"}}
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
	raw, present, _ := unstructured.NestedFieldNoCopy(root.Object, fields...)
	m, isMap := raw.(map[string]any)
	if !present || !isMap {
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
	return append([]FluxSourceRef{{Ref: resourceid.NewRef(group, kind, namespace, name), Role: role, Path: strings.Join(fields, ".")}}, chartRefs...)
}

// FluxDependsOn reads a Kustomization's or HelmRelease's spec.dependsOn: each
// entry names an object of the root's own kind and API group, the only
// targets Flux accepts there, in the root's namespace unless the entry names
// another. An entry without a name, or naming the root itself, names nothing.
func FluxDependsOn(root *unstructured.Unstructured) []resourceid.Ref {
	if root == nil {
		return nil
	}
	kind := resourceid.GroupKind{Group: resourceid.GroupFromAPIVersion(root.GetAPIVersion()), Kind: root.GetKind()}
	if kind != fluxKustomizationKind && kind != fluxHelmReleaseKind {
		return nil
	}
	entries, _, _ := unstructured.NestedSlice(root.Object, "spec", "dependsOn")
	var refs []resourceid.Ref
	for _, entry := range entries {
		dep, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, namespace := StringValue(dep["name"]), StringValue(dep["namespace"])
		if name == "" {
			continue
		}
		if namespace == "" {
			namespace = root.GetNamespace()
		}
		if namespace == root.GetNamespace() && name == root.GetName() {
			continue
		}
		refs = append(refs, resourceid.NewRef(kind.Group, kind.Kind, namespace, name))
	}
	return refs
}

func isSourceMap(raw any) bool { _, ok := raw.(map[string]any); return ok }

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
	return []FluxSourceRef{{Ref: resourceid.NewRef(group, "ArtifactGenerator", namespace, name), Role: "generator", Path: "spec.sourceRef"}}
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
		if name == "" || !slices.Contains([]string{"GitRepository", "OCIRepository", "Bucket", "HelmChart", "ExternalArtifact"}, kind) {
			continue
		}
		namespace := StringValue(m["namespace"])
		if namespace == "" {
			namespace = gen.GetNamespace()
		}
		out = append(out, FluxSourceRef{Ref: resourceid.NewRef("source.toolkit.fluxcd.io", kind, namespace, name), Role: "source", Path: "spec.sources", Alias: StringValue(m["alias"])})
	}
	return out
}

// MaxFluxSourceLevels bounds a walk up a Flux source chain from what a root
// references (ExternalArtifact → ArtifactGenerator → its sources). Each nested
// generator costs two levels, so this follows four generators fed by one
// another; a walk that stops here with more to follow says so.
const MaxFluxSourceLevels = 8

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

// ArtifactGeneratorPaused reports whether source-watcher has been told to stop
// reconciling a generator. It has no spec.suspend: the annotation pauses it,
// and the generator keeps its last Ready condition while paused, so that
// condition must not be read as a current failure.
func ArtifactGeneratorPaused(obj *unstructured.Unstructured) bool {
	if obj == nil || obj.GetKind() != "ArtifactGenerator" || resourceid.GroupFromAPIVersion(obj.GetAPIVersion()) != "source.extensions.fluxcd.io" {
		return false
	}
	return strings.EqualFold(obj.GetAnnotations()["source.extensions.fluxcd.io/reconcile"], "disabled")
}
