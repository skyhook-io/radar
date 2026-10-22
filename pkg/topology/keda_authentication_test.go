package topology

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestKEDAAuthenticationDependenciesObservedAndScoped(t *testing.T) {
	gvrs := map[string]schema.GroupVersionResource{}
	for kind, resource := range map[string]string{"ScaledObject": "scaledobjects", "ScaledJob": "scaledjobs", "TriggerAuthentication": "triggerauthentications", "ClusterTriggerAuthentication": "clustertriggerauthentications"} {
		gvrs[kind] = schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: resource}
	}
	makeObject := func(kind, namespace, name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": kind, "metadata": map[string]any{"namespace": namespace, "name": name}}}
	}
	so, sj := makeObject("ScaledObject", "team", "worker"), makeObject("ScaledJob", "team", "jobs")
	so.Object["spec"] = map[string]any{"triggers": []any{
		map[string]any{"authenticationRef": map[string]any{"name": "local"}},
		map[string]any{"authenticationRef": map[string]any{"name": "local"}},
		map[string]any{"authenticationRef": map[string]any{"name": "absent"}},
	}}
	sj.Object["spec"] = map[string]any{"triggers": []any{map[string]any{"authenticationRef": map[string]any{"name": "global", "kind": "ClusterTriggerAuthentication"}}}}
	p := &issuerTestProvider{monitorDynamicProvider: monitorDynamicProvider{gvrs: gvrs, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gvrs["ScaledObject"]: {so}, gvrs["ScaledJob"]: {sj},
		gvrs["TriggerAuthentication"]:        {makeObject("TriggerAuthentication", "team", "local"), makeObject("TriggerAuthentication", "other", "local"), makeObject("TriggerAuthentication", "team", "unused")},
		gvrs["ClusterTriggerAuthentication"]: {makeObject("ClusterTriggerAuthentication", "", "global")},
	}}, listCalls: map[string]int{}}
	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"team"}
	topo, err := NewBuilder(&mockProvider{}).WithDynamic(p).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(topo.Nodes) != 4 || len(topo.Edges) != 2 {
		t.Fatalf("observed authentication graph: nodes=%+v edges=%+v", topo.Nodes, topo.Edges)
	}
	for _, edge := range topo.Edges {
		if edge.Type != EdgeConfigures || edge.Label != "authentication" {
			t.Fatalf("authentication presented as scaler usage: %+v", edge)
		}
	}
	for _, tc := range []struct{ kind, name, auth, namespace string }{{"ScaledObject", "worker", "TriggerAuthentication", "team"}, {"ScaledJob", "jobs", "ClusterTriggerAuthentication", ""}} {
		rel := GetRelationships(tc.kind, "team", tc.name, topo, nil, p)
		if rel == nil || len(rel.Dependencies) != 1 || rel.Dependencies[0].Kind != tc.auth || rel.Dependencies[0].Namespace != tc.namespace || rel.ScaleTarget != nil {
			t.Fatalf("authentication misclassified for %s: %+v", tc.kind, rel)
		}
		name := "local"
		if tc.namespace == "" {
			name = "global"
		}
		reverse := GetRelationships(tc.auth, tc.namespace, name, topo, nil, p)
		if reverse == nil || len(reverse.Dependents) != 1 || reverse.Dependents[0].Kind != tc.kind {
			t.Fatalf("reverse authentication: %+v", reverse)
		}
	}
	if p.getCalls != 0 || p.listCalls["triggerauthentications"] != 1 || p.listCalls["clustertriggerauthentications"] != 1 {
		t.Fatalf("per-object lookup or repeated kind list: gets=%d lists=%v", p.getCalls, p.listCalls)
	}
	if tuples := topo.ClusterScopedDynamicRBACTuples(); len(tuples) != 1 || tuples[0].Group != "keda.sh" || tuples[0].Resource != "clustertriggerauthentications" {
		t.Fatalf("cluster authorization tuples = %+v", tuples)
	}
	topo.StripClusterScopedDynamicExcept(nil)
	if len(topo.Nodes) != 3 || len(topo.Edges) != 1 {
		t.Fatalf("denied cluster authentication retained: %+v", topo)
	}
}
