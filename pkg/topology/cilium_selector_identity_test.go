package topology

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

func TestCiliumEndpointSelectorExpressionsAndCoverage(t *testing.T) {
	expr := func(key, op string, values ...string) map[string]any {
		vs := []any{}
		for _, v := range values {
			vs = append(vs, v)
		}
		return map[string]any{"key": key, "operator": op, "values": vs}
	}
	for _, tc := range []struct {
		name          string
		selector      map[string]any
		selected, all bool
	}{
		{"expressions", map[string]any{"matchExpressions": []any{expr("app", "In", "web")}}, true, false},
		{"and", map[string]any{"matchLabels": map[string]any{"app": "web"}, "matchExpressions": []any{expr("tier", "In", "frontend")}}, false, false},
		{"notin", map[string]any{"matchExpressions": []any{expr("app", "NotIn", "db")}}, true, false},
		{"exists", map[string]any{"matchExpressions": []any{expr("web-only", "Exists")}}, true, false},
		{"doesnotexist", map[string]any{"matchExpressions": []any{expr("db-only", "DoesNotExist")}}, true, false},
		{"empty", map[string]any{}, true, true},
		// Cilium keys may name their label source.
		{"k8s source label", map[string]any{"matchLabels": map[string]any{"k8s:app": "web"}}, true, false},
		{"any source expression", map[string]any{"matchExpressions": []any{expr("any:app", "In", "web")}}, true, false},
		{"reserved source", map[string]any{"matchLabels": map[string]any{"reserved:host": ""}}, false, false},
		// Every endpoint carries its namespace as a label.
		{"namespace label", map[string]any{"matchLabels": map[string]any{"k8s:io.kubernetes.pod.namespace": "app", "app": "web"}}, true, false},
		{"invalid", map[string]any{"matchExpressions": []any{expr("app", "Invalid", "web")}}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gvr := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
			policy := genericIdentityObject(gvr, "CiliumNetworkPolicy", "app", "policy")
			policy.Object["spec"] = map[string]any{"endpointSelector": tc.selector}
			d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "CiliumNetworkPolicy"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {policy}}, listCalls: map[schema.GroupVersionResource]int{}}
			provider := &mockProvider{}
			for _, ns := range []string{"app", "other"} {
				for _, name := range []string{"web", "db"} {
					lab := map[string]string{"app": name, "tier": "backend", name + "-only": "yes"}
					meta := metav1.ObjectMeta{Namespace: ns, Name: name}
					template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: lab}}
					provider.deployments = append(provider.deployments, &appsv1.Deployment{ObjectMeta: meta, Spec: appsv1.DeploymentSpec{Template: template}})
					provider.statefulSets = append(provider.statefulSets, &appsv1.StatefulSet{ObjectMeta: meta, Spec: appsv1.StatefulSetSpec{Template: template}})
					provider.daemonSets = append(provider.daemonSets, &appsv1.DaemonSet{ObjectMeta: meta, Spec: appsv1.DaemonSetSpec{Template: template}})
				}
			}
			opts := DefaultBuildOptions()
			opts.ShowPolicyEffect = true
			topo, err := NewBuilder(provider).WithDynamic(d).Build(opts)
			if err != nil {
				t.Fatal(err)
			}
			policyNode := nodeByID(topo.Nodes, "ciliumnetworkpolicy/app/policy")
			if policyNode == nil {
				t.Fatal("missing policy")
			}
			if (policyNode.Data["matchesAllPods"] == true) != tc.all {
				t.Errorf("matchesAllPods=%v want %v", policyNode.Data["matchesAllPods"], tc.all)
			}
			for _, kind := range []string{"deployment", "statefulset", "daemonset"} {
				for _, ns := range []string{"app", "other"} {
					for _, name := range []string{"web", "db"} {
						id := kind + "/" + ns + "/" + name
						want := ns == "app" && (tc.all || (name == "web" && tc.selected))
						edgeFound := false
						for _, e := range topo.Edges {
							edgeFound = edgeFound || e.Source == policyNode.ID && e.Target == id && e.Type == EdgeProtects
						}
						if edgeFound != (want && !tc.all) {
							t.Errorf("%s edge=%v want %v", id, edgeFound, want && !tc.all)
						}
						n := nodeByID(topo.Nodes, id)
						if n == nil {
							t.Fatalf("missing workload %s", id)
						}
						if (n.Data["policyStatus"] == "protected") != want {
							t.Errorf("%s coverage=%v want protected=%v", id, n.Data["policyStatus"], want)
						}
					}
				}
			}
		})
	}
}
