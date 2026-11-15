package configrefs

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// ArgoWorkflowTemplateRefs returns the WorkflowTemplates, in namespace, and
// ClusterWorkflowTemplates the workflow spec at fields of obj uses: its
// workflowTemplateRef and every DAG task's or step's templateRef, each
// once, in the order first named.
func ArgoWorkflowTemplateRefs(obj map[string]any, namespace string, fields ...string) []PathRef {
	spec, found, _ := unstructured.NestedMap(obj, fields...)
	if !found {
		return nil
	}
	prefix := strings.Join(fields, ".")
	var refs []PathRef
	seen := map[resourceid.Ref]bool{}
	add := func(raw map[string]any, path string) {
		name, _, _ := unstructured.NestedString(raw, "name")
		if name == "" {
			return
		}
		ref := resourceid.NewRef(ArgoGroup, "WorkflowTemplate", namespace, name)
		if cluster, _, _ := unstructured.NestedBool(raw, "clusterScope"); cluster {
			ref = resourceid.NewRef(ArgoGroup, "ClusterWorkflowTemplate", "", name)
		}
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, PathRef{Ref: ref, Path: prefix + "." + path})
		}
	}
	if workflowRef, found, _ := unstructured.NestedMap(spec, "workflowTemplateRef"); found {
		add(workflowRef, "workflowTemplateRef")
	}
	templates, _, _ := unstructured.NestedSlice(spec, "templates")
	for _, rawTemplate := range templates {
		template, ok := rawTemplate.(map[string]any)
		if !ok {
			continue
		}
		tasks, _, _ := unstructured.NestedSlice(template, "dag", "tasks")
		for _, task := range tasks {
			if m, ok := task.(map[string]any); ok {
				if ref, found, _ := unstructured.NestedMap(m, "templateRef"); found {
					add(ref, "templates[].dag.tasks[].templateRef")
				}
			}
		}
		groups, _, _ := unstructured.NestedSlice(template, "steps")
		for _, group := range groups {
			steps, _ := group.([]any)
			for _, step := range steps {
				if m, ok := step.(map[string]any); ok {
					if ref, found, _ := unstructured.NestedMap(m, "templateRef"); found {
						add(ref, "templates[].steps[][].templateRef")
					}
				}
			}
		}
	}
	return refs
}
