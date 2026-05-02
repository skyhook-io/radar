package tree

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/gitops"
)

const fluxSourceGroup = "source.toolkit.fluxcd.io"

func fluxRelatedResources(root *unstructured.Unstructured) []relatedResource {
	var out []relatedResource
	rootRef := ResourceRef{
		Group:     apiGroup(root),
		Kind:      root.GetKind(),
		Namespace: root.GetNamespace(),
		Name:      root.GetName(),
	}

	if ref, ok := fluxSourceRef(root, root.GetNamespace(), "spec", "sourceRef"); ok {
		out = append(out, relatedResource{
			Ref:  ref,
			Type: "source",
			Data: map[string]any{"relationship": "source"},
		})
	}
	if ref, ok := fluxSourceRef(root, root.GetNamespace(), "spec", "chart", "spec", "sourceRef"); ok {
		out = append(out, relatedResource{
			Ref:  ref,
			Type: "source",
			Data: map[string]any{"relationship": "chart source"},
		})
	}

	deps, _, _ := unstructured.NestedSlice(root.Object, "spec", "dependsOn")
	for _, item := range deps {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := gitops.StringValue(m["name"])
		if name == "" {
			continue
		}
		namespace := gitops.StringValue(m["namespace"])
		if namespace == "" {
			namespace = root.GetNamespace()
		}
		ref := ResourceRef{
			Group:     rootRef.Group,
			Kind:      rootRef.Kind,
			Namespace: namespace,
			Name:      name,
		}
		out = append(out, relatedResource{
			Ref:  ref,
			Type: "dependsOn",
			Data: map[string]any{"relationship": "depends on"},
		})
	}

	return out
}

func fluxSourceRef(root *unstructured.Unstructured, defaultNamespace string, fields ...string) (ResourceRef, bool) {
	source, ok, _ := unstructured.NestedMap(root.Object, fields...)
	if !ok {
		return ResourceRef{}, false
	}
	kind := gitops.StringValue(source["kind"])
	name := gitops.StringValue(source["name"])
	if kind == "" || name == "" {
		return ResourceRef{}, false
	}
	namespace := gitops.StringValue(source["namespace"])
	if namespace == "" {
		namespace = defaultNamespace
	}
	group := gitops.GroupFromAPIVersion(gitops.StringValue(source["apiVersion"]))
	if group == "" {
		group = fluxSourceGroup
	}
	return ResourceRef{
		Group:     group,
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
	}, true
}

