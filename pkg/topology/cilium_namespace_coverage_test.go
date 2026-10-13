package topology

import (
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCiliumNamespaceSpecsAndClusterwideCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, kind                                        string
		namespaceKnown, selected, undecided, corePositive bool
	}{
		{"known CNP", "CiliumNetworkPolicy", true, true, false, false},
		{"unknown CNP", "CiliumNetworkPolicy", false, false, true, false},
		{"known CCNP", "CiliumClusterwideNetworkPolicy", true, true, false, false},
		{"unknown CCNP", "CiliumClusterwideNetworkPolicy", false, false, true, false},
		{"positive overrides unknown", "CiliumNetworkPolicy", false, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gvr := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
			ns := "app"
			if tc.kind == "CiliumClusterwideNetworkPolicy" {
				gvr.Resource = "ciliumclusterwidenetworkpolicies"
				ns = ""
			}
			policy := genericIdentityObject(gvr, tc.kind, ns, "policy")
			policy.Object["specs"] = []any{map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"app": "web", "k8s:io.cilium.k8s.namespace.labels.team": "platform"}}}}
			d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: tc.kind}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {policy}}, listCalls: map[schema.GroupVersionResource]int{}}
			p := &mockProvider{deployments: []*appsv1.Deployment{
				{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}},
				{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "db"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "db"}}}}},
			}}
			if tc.corePositive {
				p.networkPolicies = []*networkingv1.NetworkPolicy{{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "all"}}}
			}
			var provider ResourceProvider = p
			if tc.namespaceKnown {
				provider = &calicoTestProvider{mockProvider: p, namespaces: []*corev1.Namespace{{ObjectMeta: metav1.ObjectMeta{Name: "app", Labels: map[string]string{"team": "platform"}}}}}
			}
			opts := DefaultBuildOptions()
			opts.ShowPolicyEffect = true
			topo, err := NewBuilder(provider).WithDynamic(d).Build(opts)
			if err != nil {
				t.Fatal(err)
			}
			want := "unprotected"
			if tc.selected {
				want = "protected"
			} else if tc.undecided {
				want = "unknown"
			}
			if got := nodeByID(topo.Nodes, "deployment/app/web").Data["policyStatus"]; got != want {
				t.Fatalf("web status=%v, want %s", got, want)
			}
			if !tc.corePositive && nodeByID(topo.Nodes, "deployment/app/db").Data["policyStatus"] != "unprotected" {
				t.Fatal("known mismatch must not become unknown")
			}
		})
	}
}

func TestCiliumIngressPolicyDoesNotMakeApplicationWorkloadsUnknown(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"}
	policy := genericIdentityObject(gvr, "CiliumClusterwideNetworkPolicy", "", "ingress-only")
	policy.Object["spec"] = map[string]any{"endpointSelector": map[string]any{"matchExpressions": []any{map[string]any{"key": "reserved:ingress", "operator": "Exists"}}}}
	d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "CiliumClusterwideNetworkPolicy"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {policy}}, listCalls: map[schema.GroupVersionResource]int{}}
	p := &mockProvider{}
	for _, ns := range []string{"app", "other"} {
		p.deployments = append(p.deployments, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "web"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}})
	}
	opts := DefaultBuildOptions()
	opts.ShowPolicyEffect = true
	topo, err := NewBuilder(p).WithDynamic(d).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{"app", "other"} {
		node := nodeByID(topo.Nodes, "deployment/"+ns+"/web")
		if node == nil || node.Data["networkPolicyMatchesIncomplete"] != nil || node.Data["policyStatus"] != "unprotected" {
			t.Fatalf("ingress identity affected application %s: %+v", ns, node)
		}
		rel := GetRelationships("deployments", ns, "web", topo, p, d)
		if rel != nil && (rel.NetworkPolicyMatchesIncomplete || len(rel.NetworkPolicies) > 0) {
			t.Fatalf("ingress policy joined application %s: %+v", ns, rel)
		}
	}
}

// Exercise the namespaced selector hot path with many unrelated namespaces.
func BenchmarkCiliumNamespacedPolicySelection(b *testing.B) {
	const namespaces, workloadsPerNamespace, policiesPerNamespace = 500, 30, 6
	nodes := make([]Node, 0, namespaces*workloadsPerNamespace)
	deployments := make([]*appsv1.Deployment, 0, cap(nodes))
	policies := make([]*unstructured.Unstructured, 0, namespaces*policiesPerNamespace)
	gvr := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
	for n := 0; n < namespaces; n++ {
		ns := fmt.Sprintf("ns-%d", n)
		for w := 0; w < workloadsPerNamespace; w++ {
			name := fmt.Sprintf("web-%d", w)
			nodes = append(nodes, Node{ID: "deployment/" + ns + "/" + name, Kind: KindDeployment, Name: name, Data: map[string]any{"namespace": ns}})
			deployments = append(deployments, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}})
		}
		for p := 0; p < policiesPerNamespace; p++ {
			obj := genericIdentityObject(gvr, "CiliumNetworkPolicy", ns, fmt.Sprintf("policy-%d", p))
			obj.Object["spec"] = map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"app": "web"}}}
			policies = append(policies, obj)
		}
	}
	dp := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "CiliumNetworkPolicy"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: policies}, listCalls: map[schema.GroupVersionResource]int{}}
	builder := NewBuilder(&mockProvider{}).WithDynamic(dp)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var warnings []string
		_, edges := builder.addCiliumPolicyNodes(nodes, nil, DefaultBuildOptions(), &warnings, deployments, nil, nil)
		if len(edges) != namespaces*workloadsPerNamespace*policiesPerNamespace {
			b.Fatalf("lost namespace selection: %d edges", len(edges))
		}
	}
}
