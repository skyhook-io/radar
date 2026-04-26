package tree

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
		name := stringValue(m["name"])
		if name == "" {
			continue
		}
		namespace := stringValue(m["namespace"])
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
	kind := stringValue(source["kind"])
	name := stringValue(source["name"])
	if kind == "" || name == "" {
		return ResourceRef{}, false
	}
	namespace := stringValue(source["namespace"])
	if namespace == "" {
		namespace = defaultNamespace
	}
	group := groupFromAPIVersion(stringValue(source["apiVersion"]))
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

func groupFromAPIVersion(apiVersion string) string {
	if apiVersion == "" || apiVersion == "v1" {
		return ""
	}
	if before, _, ok := strings.Cut(apiVersion, "/"); ok {
		return before
	}
	return apiVersion
}
