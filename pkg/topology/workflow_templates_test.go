package topology

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestWorkflowsAreConfiguredByTheTemplatesTheyName(t *testing.T) {
	gvr := func(resource string) schema.GroupVersionResource {
		return schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: resource}
	}
	wfGVR, cwfGVR, wtGVR, cwtGVR := gvr("workflows"), gvr("cronworkflows"), gvr("workflowtemplates"), gvr("clusterworkflowtemplates")
	run := genericIdentityObject(wfGVR, "Workflow", "demo", "run")
	run.Object["spec"] = map[string]any{
		"workflowTemplateRef": map[string]any{"name": "main"},
		"templates": []any{
			map[string]any{"name": "dag", "dag": map[string]any{"tasks": []any{
				map[string]any{"name": "build", "templateRef": map[string]any{"name": "shared", "clusterScope": true}},
				map[string]any{"name": "test", "templateRef": map[string]any{"name": "shared", "clusterScope": true}},
			}}},
			map[string]any{"name": "steps", "steps": []any{[]any{map[string]any{"name": "cleanup", "templateRef": map[string]any{"name": "cleanup"}}}}},
		},
	}
	nightly := genericIdentityObject(cwfGVR, "CronWorkflow", "demo", "nightly")
	nightly.Object["spec"] = map[string]any{"schedule": "0 1 * * *", "workflowSpec": map[string]any{"workflowTemplateRef": map[string]any{"name": "main"}}}
	d := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{wfGVR, cwfGVR, wtGVR, cwtGVR},
		kinds:   map[schema.GroupVersionResource]string{wfGVR: "Workflow", cwfGVR: "CronWorkflow", wtGVR: "WorkflowTemplate", cwtGVR: "ClusterWorkflowTemplate"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			wfGVR:  {run},
			cwfGVR: {nightly},
			wtGVR:  {genericIdentityObject(wtGVR, "WorkflowTemplate", "demo", "main"), genericIdentityObject(wtGVR, "WorkflowTemplate", "other", "cleanup")},
			cwtGVR: {genericIdentityObject(cwtGVR, "ClusterWorkflowTemplate", "", "shared")},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	topo, err := NewBuilder(&mockProvider{}).WithDynamic(d).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range topo.Edges {
		if e.Type == EdgeConfigures {
			got = append(got, e.ID)
		}
	}
	slices.Sort(got)
	// cleanup is named without a namespace, so it is demo's, which has none;
	// other/cleanup is a different template.
	want := []string{
		"clusterworkflowtemplate//shared-to-workflow/demo/run",
		"workflowtemplate/demo/main-to-cronworkflow/demo/nightly",
		"workflowtemplate/demo/main-to-workflow/demo/run",
	}
	if !slices.Equal(got, want) {
		t.Errorf("template edges = %v, want %v", got, want)
	}
}
