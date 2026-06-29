package tree

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/topology"
)

const fluxSourceGroup = "source.toolkit.fluxcd.io"

// HelmRelease, unlike Kustomization, doesn't maintain a status.inventory
// listing because the Helm controller delegates tracking to Helm's own release
// storage (a Secret in the release namespace). To recover the managed set at
// query time we match on the labels Flux *itself* stamps onto every resource
// it reconciles — these are universal across charts, unlike chart-author-
// controlled labels (app.kubernetes.io/instance is recommended but many
// charts use app.kubernetes.io/name instead).
//
// Limitation: charts that strip these labels in pre-render hooks won't be
// detected. Reading the release Secret directly would be definitive but
// requires base64+gzip+JSON decoding of Helm's wire format, deferred until
// this label-based heuristic proves insufficient in practice.
const (
	fluxHelmNameLabel      = "helm.toolkit.fluxcd.io/name"
	fluxHelmNamespaceLabel = "helm.toolkit.fluxcd.io/namespace"
)

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
			Type: EdgeSource,
			Data: map[string]any{"relationship": "source"},
		})
	}
	if ref, ok := fluxSourceRef(root, root.GetNamespace(), "spec", "chart", "spec", "sourceRef"); ok {
		out = append(out, relatedResource{
			Ref:  ref,
			Type: EdgeSource,
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
			Type: EdgeDependsOn,
			Data: map[string]any{"relationship": "depends on"},
		})
	}

	return out
}

// fluxHelmReleaseManaged returns the live resources stamped with this
// HelmRelease's identity (matched by labels Flux writes onto each managed
// object). Scans the supplied topology nodes (which the builder has already
// collected via the live informer cache) so we don't pay for an extra cluster
// round-trip per detail page open.
func fluxHelmReleaseManaged(root *unstructured.Unstructured, topo []topology.Node) []managedResource {
	hrName := root.GetName()
	hrNamespace := root.GetNamespace()
	if hrName == "" {
		return nil
	}

	var out []managedResource
	for _, n := range topo {
		labels, ok := n.Data["labels"].(map[string]string)
		if !ok {
			// Tolerate the case where labels were stored as map[string]any
			// (some topology builders use that shape). Fall back to nil so the
			// match check below cleanly returns false.
			if generic, ok2 := n.Data["labels"].(map[string]any); ok2 {
				labels = stringMapFromAny(generic)
			} else {
				continue
			}
		}
		if labels[fluxHelmNameLabel] != hrName {
			continue
		}
		if labels[fluxHelmNamespaceLabel] != hrNamespace {
			continue
		}
		ref := refFromTopologyNode(n)
		// Don't include the HelmRelease itself if it somehow ended up in the
		// topology with matching labels (defensive — this shouldn't happen but
		// would create a self-edge).
		if strings.EqualFold(ref.Kind, "HelmRelease") && ref.Name == hrName && ref.Namespace == hrNamespace {
			continue
		}
		out = append(out, managedResource{Ref: ref})
	}
	return out
}

func stringMapFromAny(in map[string]any) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if s, ok := v.(string); ok {
			out[k] = s
		}
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
