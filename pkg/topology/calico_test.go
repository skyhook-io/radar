package topology

import (
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	k8score "github.com/skyhook-io/radar/pkg/k8score"
)

type calicoTestProvider struct {
	*mockProvider
	namespaces []*corev1.Namespace
}

func (p *calicoTestProvider) Namespaces() ([]*corev1.Namespace, error) {
	return p.namespaces, nil
}

type calicoTestDynamic struct {
	gvrs      map[string]schema.GroupVersionResource
	resources map[schema.GroupVersionResource][]*unstructured.Unstructured
	watched   []schema.GroupVersionResource
}

func (d *calicoTestDynamic) List(gvr schema.GroupVersionResource, _ string) ([]*unstructured.Unstructured, error) {
	return d.resources[gvr], nil
}

func (d *calicoTestDynamic) ListNamespaces(gvr schema.GroupVersionResource, _ []string) ([]*unstructured.Unstructured, error) {
	return d.resources[gvr], nil
}

func (d *calicoTestDynamic) Get(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	for _, resource := range d.resources[gvr] {
		if resource.GetNamespace() == namespace && resource.GetName() == name {
			return resource, nil
		}
	}
	return nil, nil
}

func (d *calicoTestDynamic) GetWatchedResources() []schema.GroupVersionResource { return d.watched }
func (d *calicoTestDynamic) GetDiscoveryStatus() k8score.CRDDiscoveryStatus {
	return k8score.CRDDiscoveryComplete
}
func (d *calicoTestDynamic) GetGVR(kindOrName string) (schema.GroupVersionResource, bool) {
	for key, gvr := range d.gvrs {
		if key == "projectcalico.org\x00"+kindOrName || key == "crd.projectcalico.org\x00"+kindOrName {
			return gvr, true
		}
	}
	return schema.GroupVersionResource{}, false
}
func (d *calicoTestDynamic) GetGVRWithGroup(kindOrName, group string) (schema.GroupVersionResource, bool) {
	gvr, ok := d.gvrs[group+"\x00"+kindOrName]
	return gvr, ok
}
func (d *calicoTestDynamic) GetKindForGVR(gvr schema.GroupVersionResource) string {
	for key, candidate := range d.gvrs {
		if candidate == gvr {
			return key[len(gvr.Group)+1:]
		}
	}
	return ""
}
func (d *calicoTestDynamic) IsCRD(string) bool { return true }
func (d *calicoTestDynamic) IsCRDGVR(schema.GroupVersionResource) bool { return true }

func calicoTestObject(group, version, kind, namespace, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": group + "/" + version,
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}}
}

func calicoTestDynamicFor(group string) *calicoTestDynamic {
	definitions := []struct {
		kind       string
		resource   string
		version    string
		namespaced bool
	}{
		{"NetworkPolicy", "networkpolicies", "v3", true},
		{"GlobalNetworkPolicy", "globalnetworkpolicies", "v3", false},
		{"StagedNetworkPolicy", "stagednetworkpolicies", "v3", true},
		{"StagedGlobalNetworkPolicy", "stagedglobalnetworkpolicies", "v3", false},
		{"StagedKubernetesNetworkPolicy", "stagedkubernetesnetworkpolicies", "v3", true},
	}
	d := &calicoTestDynamic{
		gvrs:      map[string]schema.GroupVersionResource{},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{},
	}
	for _, definition := range definitions {
		gvr := schema.GroupVersionResource{Group: group, Version: definition.version, Resource: definition.resource}
		d.gvrs[group+"\x00"+definition.kind] = gvr
		switch definition.kind {
		case "NetworkPolicy":
			d.resources[gvr] = []*unstructured.Unstructured{calicoTestObject(group, definition.version, definition.kind, "demo", "frontend-policy", map[string]any{
				"selector": "app == 'frontend'",
			})}
		case "GlobalNetworkPolicy":
			d.resources[gvr] = []*unstructured.Unstructured{calicoTestObject(group, definition.version, definition.kind, "", "backend-global", map[string]any{
				"selector": "app == 'backend'",
			})}
		case "StagedNetworkPolicy":
			d.resources[gvr] = []*unstructured.Unstructured{calicoTestObject(group, definition.version, definition.kind, "demo", "frontend-staged", map[string]any{
				"selector":     "app == 'frontend'",
				"stagedAction": "Set",
			})}
		case "StagedGlobalNetworkPolicy":
			d.resources[gvr] = []*unstructured.Unstructured{calicoTestObject(group, definition.version, definition.kind, "", "all-staged", map[string]any{
				"selector": "all()",
			})}
		case "StagedKubernetesNetworkPolicy":
			d.resources[gvr] = []*unstructured.Unstructured{calicoTestObject(group, definition.version, definition.kind, "demo", "frontend-kubernetes-staged", map[string]any{
				"podSelector": map[string]any{
					"matchLabels": map[string]any{"app": "frontend"},
				},
				"policyTypes": []any{"Ingress"},
				"ingress": []any{map[string]any{
					"from": []any{map[string]any{"podSelector": map[string]any{
						"matchLabels": map[string]any{"app": "backend"},
					}}},
				}},
			})}
		}
	}
	return d
}

func TestCalicoPolicyTopology(t *testing.T) {
	provider := &calicoTestProvider{
		mockProvider: &mockProvider{
			deployments: []*appsv1.Deployment{
				{ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "demo"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "frontend"}}}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "demo"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "backend"}}}}},
			},
		},
	}

	topo, err := NewBuilder(provider).WithDynamic(calicoTestDynamicFor("projectcalico.org")).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	nodes := map[NodeKind]Node{}
	for _, node := range topo.Nodes {
		if node.Kind == KindCalicoNetworkPolicy || node.Kind == KindCalicoGlobalNetworkPolicy || node.Kind == KindCalicoStagedNetworkPolicy || node.Kind == KindCalicoStagedGlobalNetworkPolicy || node.Kind == KindCalicoStagedKubernetesNetworkPolicy {
			nodes[node.Kind] = node
		}
	}
	for _, kind := range []NodeKind{KindCalicoNetworkPolicy, KindCalicoGlobalNetworkPolicy, KindCalicoStagedNetworkPolicy, KindCalicoStagedGlobalNetworkPolicy, KindCalicoStagedKubernetesNetworkPolicy} {
		node, ok := nodes[kind]
		if !ok {
			t.Fatalf("missing Calico node %s", kind)
		}
		if node.Data["apiVersion"] != "projectcalico.org/v3" {
			t.Errorf("%s apiVersion = %v", kind, node.Data["apiVersion"])
		}
	}
	if nodes[KindCalicoStagedNetworkPolicy].Status != StatusNeutral {
		t.Errorf("staged policy status = %s, want neutral", nodes[KindCalicoStagedNetworkPolicy].Status)
	}
	for _, edge := range topo.Edges {
		if edge.Source == "caliconetworkpolicy/demo/frontend-policy" && edge.Partial {
			t.Error("enforced Calico policy edge should not be partial")
		}
		if edge.Source == "calicostagednetworkpolicy/demo/frontend-staged" && !edge.Partial {
			t.Error("staged Calico policy edge should be partial")
		}
	}

	hasEdge := func(source, target string) bool {
		for _, edge := range topo.Edges {
			if edge.Type == EdgeProtects && edge.Source == source && edge.Target == target {
				return true
			}
		}
		return false
	}
	if !hasEdge("caliconetworkpolicy/demo/frontend-policy", "deployment/demo/frontend") {
		t.Error("namespaced Calico policy did not select frontend")
	}
	if !hasEdge("calicoglobalnetworkpolicy//backend-global", "deployment/demo/backend") {
		t.Error("global Calico policy did not select backend")
	}
	if !hasEdge("calicostagedkubernetesnetworkpolicy/demo/frontend-kubernetes-staged", "deployment/demo/frontend") {
		t.Error("staged Kubernetes Calico policy did not select frontend")
	}
	for _, edge := range topo.Edges {
		if edge.Source == "calicostagedkubernetesnetworkpolicy/demo/frontend-kubernetes-staged" && !edge.Partial {
			t.Error("staged Kubernetes Calico policy edge should be partial")
		}
	}
	if hasEdge("calicoglobalnetworkpolicy//backend-global", "deployment/demo/frontend") {
		t.Error("global Calico policy selected the wrong workload")
	}
	if nodes[KindCalicoStagedGlobalNetworkPolicy].Data["matchesAllPods"] != true {
		t.Error("all() staged global policy should advertise matchesAllPods")
	}
}

func TestStagedKubernetesCalicoPolicyIsExcludedFromGenericCRDs(t *testing.T) {
	provider := &calicoTestProvider{
		mockProvider: &mockProvider{
			deployments: []*appsv1.Deployment{
				{ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "demo"}},
			},
		},
	}
	dynamic := calicoTestDynamicFor("projectcalico.org")
	gvr := dynamic.gvrs["projectcalico.org\x00StagedKubernetesNetworkPolicy"]
	dynamic.resources[gvr][0].SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "frontend"}})
	dynamic.watched = []schema.GroupVersionResource{gvr}

	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(BuildOptions{IncludeGenericCRDs: true})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	var policyNodes int
	for _, node := range topo.Nodes {
		if node.Name == "frontend-kubernetes-staged" {
			policyNodes++
			if node.Kind != KindCalicoStagedKubernetesNetworkPolicy {
				t.Fatalf("staged policy was added as generic kind %s", node.Kind)
			}
		}
	}
	if policyNodes != 1 {
		t.Fatalf("staged policy node count = %d, want 1", policyNodes)
	}
}

func TestStagedCalicoPoliciesDoNotCountAsCoverage(t *testing.T) {
	workloadID := "deployment/demo/frontend"
	nodes := []Node{
		{ID: "calicostagednetworkpolicy/demo/preview", Kind: KindCalicoStagedNetworkPolicy, Data: map[string]any{
			"namespace":               "demo",
			"matchesAllPods":          true,
			"policyCoverageWorkloads": []string{workloadID},
		}},
		{ID: workloadID, Kind: KindDeployment, Data: map[string]any{}},
	}
	edges := []Edge{{Source: nodes[0].ID, Target: workloadID, Type: EdgeProtects, Partial: true}}

	annotateNodePolicyCoverage(nodes, edges, nil, nil, nil, nil)

	if got := nodes[1].Data["policyStatus"]; got != "unprotected" {
		t.Fatalf("staged policy coverage status = %v, want unprotected", got)
	}
}

func TestCalicoPolicySelectorsFailClosed(t *testing.T) {
	labels := map[string]string{"app": "frontend", "tier": "web"}
	tests := []struct {
		expression string
		want       bool
		valid      bool
	}{
		{"all()", true, true},
		{"app == 'frontend' && tier in {'web', 'api'}", true, true},
		{"app == 'backend' || has(tier)", true, true},
		{"app ==", false, false},
		{"app ??? 'frontend'", false, false},
	}
	for _, test := range tests {
		expression, valid := compileCalicoSelector(test.expression)
		if valid != test.valid {
			t.Errorf("compileCalicoSelector(%q) valid = %v, want %v", test.expression, valid, test.valid)
		}
		if valid && expression(labels) != test.want {
			t.Errorf("compileCalicoSelector(%q)(labels) = %v, want %v", test.expression, expression(labels), test.want)
		}
	}
}

func TestCalicoEndpointSelectorsUseAutomaticLabels(t *testing.T) {
	workload := newCalicoWorkload("deployment/demo/frontend", "demo", map[string]string{"app": "frontend"}, "default")

	for _, test := range []struct {
		name      string
		selector  string
		wantMatch bool
	}{
		{name: "namespace", selector: "projectcalico.org/namespace == 'demo'", wantMatch: true},
		{name: "orchestrator", selector: "projectcalico.org/orchestrator == 'k8s'", wantMatch: true},
		{name: "wrong namespace", selector: "projectcalico.org/namespace == 'other'", wantMatch: false},
		{name: "wrong orchestrator", selector: "projectcalico.org/orchestrator == 'calico'", wantMatch: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := calicoTestObject("projectcalico.org", "v3", "NetworkPolicy", "demo", test.name, map[string]any{"selector": test.selector})
			matched, valid := CompileCalicoPolicyMatcher(policy).Matches(workload.endpointLabels, nil, workload.serviceAccount, nil)
			if !valid || matched != test.wantMatch {
				t.Fatalf("selector %q matched=%v valid=%v, want matched=%v valid=true", test.selector, matched, valid, test.wantMatch)
			}
		})
	}
}

func TestStagedKubernetesCalicoPolicyUsesRawPodLabels(t *testing.T) {
	policy := calicoTestObject("projectcalico.org", "v3", "StagedKubernetesNetworkPolicy", "demo", "preview", map[string]any{
		"podSelector": map[string]any{"matchLabels": map[string]any{"projectcalico.org/namespace": "demo"}},
	})
	workload := newCalicoWorkload("deployment/demo/preview", "demo", map[string]string{"app": "preview"}, "default")
	matched, valid := CompileCalicoPolicyMatcher(policy).Matches(workload.labels, nil, workload.serviceAccount, nil)
	if !valid {
		t.Fatal("raw Kubernetes pod selector was invalid")
	}
	if matched {
		t.Fatal("staged Kubernetes policy matched a Calico automatic endpoint label")
	}
}

func TestCalicoGlobalNamespaceSelectorRequiresNamespaceLabels(t *testing.T) {
	provider := &calicoTestProvider{mockProvider: &mockProvider{deployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "demo"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "frontend"}}}}}}}}
	dynamic := calicoTestDynamicFor("projectcalico.org")
	gvr := dynamic.gvrs["projectcalico.org\x00GlobalNetworkPolicy"]
	dynamic.resources[gvr][0].Object["spec"].(map[string]any)["selector"] = "all()"
	dynamic.resources[gvr][0].Object["spec"].(map[string]any)["namespaceSelector"] = "team == 'prod'"

	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	for _, edge := range topo.Edges {
		if edge.Source == "calicoglobalnetworkpolicy//backend-global" && edge.Type == EdgeProtects {
			t.Fatal("global namespace selector matched without namespace labels")
		}
	}
}

func TestCalicoPolicyResourceRefsCarryGroup(t *testing.T) {
	policy := Node{
		ID: "calicoglobalnetworkpolicy//global", Kind: KindCalicoGlobalNetworkPolicy, Name: "global",
		Data: map[string]any{"namespace": "", "apiVersion": "projectcalico.org/v3"},
	}
	workload := Node{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "demo"}}
	topo := &Topology{Nodes: []Node{policy, workload}, Edges: []Edge{{Source: policy.ID, Target: workload.ID, Type: EdgeProtects}}}
	rel := GetRelationships("Deployment", "demo", "web", topo, nil, nil)
	if rel == nil || len(rel.NetworkPolicies) != 1 || rel.NetworkPolicies[0].Group != "projectcalico.org" {
		t.Fatalf("NetworkPolicies = %+v, want one projectcalico.org ref", rel)
	}
}

func TestStagedKubernetesCalicoPolicyIdentity(t *testing.T) {
	for _, test := range []struct {
		group, version string
	}{
		{group: "projectcalico.org", version: "v3"},
		{group: "crd.projectcalico.org", version: "v1"},
	} {
		t.Run(test.group, func(t *testing.T) {
			node := Node{
				ID:   "calicostagedkubernetesnetworkpolicy/demo/preview",
				Kind: KindCalicoStagedKubernetesNetworkPolicy,
				Name: "preview",
				Data: map[string]any{"namespace": "demo", "apiVersion": test.group + "/" + test.version},
			}
			tuples, ok := CalicoPolicyRBACTuples(&node)
			if !ok || len(tuples) != 1 || tuples[0] != (SARTuple{Group: test.group, Resource: "stagedkubernetesnetworkpolicies", Namespace: "demo"}) {
				t.Fatalf("RBAC tuples = %+v, %v", tuples, ok)
			}
			if got := buildNodeID("stagedkubernetesnetworkpolicies", "demo", "preview", nil); got != "calicostagedkubernetesnetworkpolicy/demo/preview" {
				t.Fatalf("buildNodeID = %q", got)
			}
		})
	}
}

func TestCalicoPolicyFilterUsesExactGroupAndPreservesNativePolicy(t *testing.T) {
	sharedID := "calicoglobalnetworkpolicy//shared"
	legacyOnlyID := "calicoglobalnetworkpolicy//legacy-only"
	stagedID := "calicostagedglobalnetworkpolicy//preview"
	nativeID := "networkpolicy/demo/native"
	workloadID := "deployment/demo/web"
	topo := &Topology{
		Nodes: []Node{
			{ID: sharedID, Kind: KindCalicoGlobalNetworkPolicy, Name: "shared", Data: map[string]any{
				"apiVersion":  "projectcalico.org/v3",
				"apiVersions": []string{"projectcalico.org/v3", "crd.projectcalico.org/v1"},
			}},
			{ID: legacyOnlyID, Kind: KindCalicoGlobalNetworkPolicy, Name: "legacy-only", Data: map[string]any{
				"apiVersion":  "crd.projectcalico.org/v1",
				"apiVersions": []string{"crd.projectcalico.org/v1"},
			}},
			{ID: stagedID, Kind: KindCalicoStagedGlobalNetworkPolicy, Name: "preview", Data: map[string]any{
				"apiVersion":  "projectcalico.org/v3",
				"apiVersions": []string{"projectcalico.org/v3"},
			}},
			{ID: nativeID, Kind: KindNetworkPolicy, Name: "native", Data: map[string]any{"namespace": "demo", "apiVersion": "networking.k8s.io/v1"}},
			{ID: workloadID, Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "demo"}},
		},
		Edges: []Edge{
			{Source: sharedID, Target: workloadID, Type: EdgeProtects},
			{Source: legacyOnlyID, Target: workloadID, Type: EdgeProtects},
			{Source: stagedID, Target: workloadID, Type: EdgeProtects, Partial: true},
			{Source: nativeID, Target: workloadID, Type: EdgeProtects},
		},
	}

	topo.StripCalicoPoliciesExcept(map[SARTuple]bool{
		{Group: "projectcalico.org", Resource: "globalnetworkpolicies"}:       true,
		{Group: "projectcalico.org", Resource: "stagedglobalnetworkpolicies"}: true,
	})

	if len(topo.Nodes) != 4 {
		t.Fatalf("nodes after exact Calico filter = %+v, want the dual-group policy plus staged, native and workload", topo.Nodes)
	}
	for _, node := range topo.Nodes {
		if node.ID == legacyOnlyID {
			t.Fatal("a crd.projectcalico.org-only policy survived a projectcalico.org-only filter")
		}
	}
	if len(topo.Edges) != 3 {
		t.Fatalf("edges after exact Calico filter = %+v, want 3", topo.Edges)
	}
	for _, edge := range topo.Edges {
		if edge.Source == stagedID && !edge.Partial {
			t.Fatal("staged Calico edge lost its partial marker")
		}
		if edge.Source == nativeID && edge.Partial {
			t.Fatal("native NetworkPolicy edge became partial")
		}
	}
}

func TestCalicoPolicyVisibleThroughEitherServingGroup(t *testing.T) {
	sharedID := "calicoglobalnetworkpolicy//shared"
	for _, allowedGroup := range []string{"projectcalico.org", "crd.projectcalico.org"} {
		t.Run(allowedGroup, func(t *testing.T) {
			topo := &Topology{Nodes: []Node{{
				ID: sharedID, Kind: KindCalicoGlobalNetworkPolicy, Name: "shared",
				Data: map[string]any{
					"apiVersion":  "projectcalico.org/v3",
					"apiVersions": []string{"projectcalico.org/v3", "crd.projectcalico.org/v1"},
				},
			}}}
			topo.StripCalicoPoliciesExcept(map[SARTuple]bool{
				{Group: allowedGroup, Resource: "globalnetworkpolicies"}: true,
			})
			if len(topo.Nodes) != 1 {
				t.Fatalf("policy readable through %s was stripped", allowedGroup)
			}
		})
	}

	topo := &Topology{Nodes: []Node{{
		ID: sharedID, Kind: KindCalicoGlobalNetworkPolicy, Name: "shared",
		Data: map[string]any{
			"apiVersion":  "projectcalico.org/v3",
			"apiVersions": []string{"projectcalico.org/v3", "crd.projectcalico.org/v1"},
		},
	}}}
	topo.StripCalicoPoliciesExcept(map[SARTuple]bool{})
	if len(topo.Nodes) != 0 {
		t.Fatal("policy survived with neither group authorized")
	}
}

func TestCalicoPseudoKindsResolveInNeighborhood(t *testing.T) {
	for _, test := range []struct {
		kind string
		want NodeKind
	}{
		{"NetworkPolicy", KindCalicoNetworkPolicy},
		{"GlobalNetworkPolicy", KindCalicoGlobalNetworkPolicy},
		{"StagedNetworkPolicy", KindCalicoStagedNetworkPolicy},
		{"StagedGlobalNetworkPolicy", KindCalicoStagedGlobalNetworkPolicy},
		{"StagedKubernetesNetworkPolicy", KindCalicoStagedKubernetesNetworkPolicy},
	} {
		node := Node{
			ID:   strings.ToLower(string(test.want)) + "//policy",
			Kind: test.want,
			Name: "policy",
			Data: map[string]any{"namespace": "", "apiVersion": "projectcalico.org/v3"},
		}
		topo := &Topology{Nodes: []Node{node}}
		sub := BuildNeighborhoodWithIndex(topo, ResourceRef{Kind: test.kind, Name: "policy", Group: "projectcalico.org"}, NeighborhoodOptions{Profile: ProfileAuto, Hops: 1}, nil, nil)
		if len(sub.Nodes) != 1 || sub.Nodes[0].Kind != test.want {
			t.Errorf("%s resolved to %+v, want %s", test.kind, sub.Nodes, test.want)
		}
	}
}

func TestBuildNeighborhood_CalicoPolicyResolvesFromEitherGroup(t *testing.T) {
	policyID := "caliconetworkpolicy/demo/shared"
	targetID := "deployment/demo/web"
	topo := &Topology{
		Nodes: []Node{
			{ID: policyID, Kind: KindCalicoNetworkPolicy, Name: "shared", Data: map[string]any{
				"namespace":   "demo",
				"apiVersion":  "projectcalico.org/v3",
				"apiVersions": []string{"projectcalico.org/v3", "crd.projectcalico.org/v1"},
			}},
			{ID: targetID, Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "demo"}},
		},
		Edges: []Edge{{Source: policyID, Target: targetID, Type: EdgeProtects}},
	}

	for _, group := range []string{"projectcalico.org", "crd.projectcalico.org"} {
		t.Run(group, func(t *testing.T) {
			sub := BuildNeighborhoodWithIndex(topo, ResourceRef{
				Kind: "NetworkPolicy", Namespace: "demo", Name: "shared", Group: group,
			}, NeighborhoodOptions{Profile: ProfileAll, Hops: 1}, nil, nil)
			if len(sub.Nodes) != 2 || sub.Nodes[0].ID != policyID || sub.Nodes[1].ID != targetID {
				t.Fatalf("neighborhood via %s = %+v, want root %s and target %s", group, sub.Nodes, policyID, targetID)
			}
		})
	}
}

// calicoTestDynamicForBoth serves the same policies under both Calico API
// groups, which is what a cluster running the Calico apiserver does.
func calicoTestDynamicForBoth() *calicoTestDynamic {
	combined := calicoTestDynamicFor(calicoProjectGroup)
	legacy := calicoTestDynamicFor(calicoCRDGroup)
	for key, gvr := range legacy.gvrs {
		combined.gvrs[key] = gvr
		combined.resources[gvr] = legacy.resources[gvr]
	}
	return combined
}

func TestCalicoPolicyServedByBothGroupsBuildsOneNode(t *testing.T) {
	provider := &calicoTestProvider{mockProvider: &mockProvider{deployments: []*appsv1.Deployment{
		{ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "demo"}, Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "frontend"}}},
		}},
	}}}

	topo, err := NewBuilder(provider).WithDynamic(calicoTestDynamicForBoth()).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	seen := map[string]int{}
	for _, node := range topo.Nodes {
		if IsCalicoPolicyKind(node.Kind) {
			seen[string(node.Kind)+"/"+nodeNamespaceFromData(&node)+"/"+node.Name]++
		}
	}
	for identity, count := range seen {
		if count != 1 {
			t.Errorf("%s rendered %d times, want a single node for a policy both groups serve", identity, count)
		}
	}

	policyID := "caliconetworkpolicy/demo/frontend-policy"
	var policy *Node
	for i := range topo.Nodes {
		if topo.Nodes[i].ID == policyID {
			policy = &topo.Nodes[i]
		}
	}
	if policy == nil {
		t.Fatalf("no node with the unqualified ID %q; nodes = %+v", policyID, topo.Nodes)
	}
	tuples, ok := CalicoPolicyRBACTuples(policy)
	if !ok || len(tuples) != 2 {
		t.Fatalf("RBAC tuples = %+v (%v), want one per serving group", tuples, ok)
	}

	edges := 0
	for _, edge := range topo.Edges {
		if edge.Source == policyID && edge.Target == "deployment/demo/frontend" && edge.Type == EdgeProtects {
			edges++
		}
	}
	if edges != 1 {
		t.Fatalf("protects edges from %s = %d, want 1", policyID, edges)
	}
}

func TestCalicoStagedDeletionPreviewsNoProtection(t *testing.T) {
	// The Calico API rejects a selector on a staged deletion, so its spec carries
	// only the action — an absent selector that must not read as "selects all".
	deletion := calicoTestObject(calicoProjectGroup, "v3", "StagedNetworkPolicy", "demo", "retire-frontend", map[string]any{
		"stagedAction": "Delete",
	})
	ignored := calicoTestObject(calicoProjectGroup, "v3", "StagedNetworkPolicy", "demo", "parked", map[string]any{
		"stagedAction": "Ignore",
		"selector":     "all()",
	})
	if CalicoStagedActionPreviewsProtection(deletion) {
		t.Error("a staged deletion was treated as previewed protection")
	}
	if CalicoStagedActionPreviewsProtection(ignored) {
		t.Error("an ignored staged policy was treated as previewed protection")
	}
	set := calicoTestObject(calicoProjectGroup, "v3", "StagedNetworkPolicy", "demo", "tighten", map[string]any{
		"stagedAction": "Set",
		"selector":     "app == 'frontend'",
	})
	if !CalicoStagedActionPreviewsProtection(set) {
		t.Error("a staged Set was not treated as previewed protection")
	}

	provider := &calicoTestProvider{mockProvider: &mockProvider{deployments: []*appsv1.Deployment{
		{ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "demo"}, Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "frontend"}}},
		}},
	}}}
	dynamic := calicoTestDynamicFor(calicoProjectGroup)
	dynamic.resources[dynamic.gvrs[calicoProjectGroup+"\x00StagedNetworkPolicy"]] = []*unstructured.Unstructured{deletion}

	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	deletionID := "calicostagednetworkpolicy/demo/retire-frontend"
	var node *Node
	for i := range topo.Nodes {
		if topo.Nodes[i].ID == deletionID {
			node = &topo.Nodes[i]
		}
	}
	if node == nil {
		t.Fatalf("the staged deletion is not rendered at all; nodes = %+v", topo.Nodes)
	}
	if node.Data["matchesAllPods"] == true {
		t.Error("a staged deletion claimed to select every workload")
	}
	if node.Data["policyCoverageWorkloads"] != nil {
		t.Errorf("a staged deletion claimed coverage: %v", node.Data["policyCoverageWorkloads"])
	}
	for _, edge := range topo.Edges {
		if edge.Source == deletionID {
			t.Errorf("a staged deletion drew a protects edge to %s", edge.Target)
		}
	}

	// A staged policy that DOES select something must still preview its edge, so
	// the assertions above are about the action and not about staged policies in
	// general.
	previewing := calicoTestDynamicFor(calicoProjectGroup)
	previewing.resources[previewing.gvrs[calicoProjectGroup+"\x00StagedNetworkPolicy"]] = []*unstructured.Unstructured{
		calicoTestObject(calicoProjectGroup, "v3", "StagedNetworkPolicy", "demo", "tighten", map[string]any{
			"stagedAction": "Set",
			"selector":     "app == 'frontend'",
		}),
	}
	previewTopo, err := NewBuilder(provider).WithDynamic(previewing).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	preview := 0
	for _, edge := range previewTopo.Edges {
		if edge.Source == "calicostagednetworkpolicy/demo/tighten" && edge.Partial {
			preview++
		}
	}
	if preview != 1 {
		t.Fatalf("staged Set preview edges = %d, want 1 — the deletion assertions must not pass by staged policies never drawing edges", preview)
	}
}

// antreaTestDynamic serves a NetworkPolicy CRD under a non-Calico group, which
// is what a cluster running a different CNI looks like.
type antreaTestDynamic struct {
	*calicoTestDynamic
	antrea    schema.GroupVersionResource
	resources []*unstructured.Unstructured
}

func (d *antreaTestDynamic) GetWatchedResources() []schema.GroupVersionResource {
	return append(d.calicoTestDynamic.GetWatchedResources(), d.antrea)
}

func (d *antreaTestDynamic) ListNamespaces(gvr schema.GroupVersionResource, ns []string) ([]*unstructured.Unstructured, error) {
	if gvr == d.antrea {
		return d.resources, nil
	}
	return d.calicoTestDynamic.ListNamespaces(gvr, ns)
}

func (d *antreaTestDynamic) List(gvr schema.GroupVersionResource, ns string) ([]*unstructured.Unstructured, error) {
	if gvr == d.antrea {
		return d.resources, nil
	}
	return d.calicoTestDynamic.List(gvr, ns)
}

func (d *antreaTestDynamic) GetKindForGVR(gvr schema.GroupVersionResource) string {
	if gvr == d.antrea {
		return "NetworkPolicy"
	}
	return d.calicoTestDynamic.GetKindForGVR(gvr)
}

// Calico's policy kinds are skipped from the generic CRD path by API GROUP. A
// name-based skip also swallows another CNI's identically-named CRD, which
// silently disappears from the graph — the reason the skip is group-scoped.
func TestForeignNetworkPolicyCRDStillRendersAsGenericNode(t *testing.T) {
	antreaGVR := schema.GroupVersionResource{Group: "crd.antrea.io", Version: "v1alpha1", Resource: "networkpolicies"}
	policy := calicoTestObject("crd.antrea.io", "v1alpha1", "NetworkPolicy", "demo", "antrea-policy", map[string]any{
		"priority": int64(5),
	})
	policy.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "frontend"}})

	dynamic := &antreaTestDynamic{
		calicoTestDynamic: calicoTestDynamicFor(calicoProjectGroup),
		antrea:            antreaGVR,
		resources:         []*unstructured.Unstructured{policy},
	}
	provider := &calicoTestProvider{mockProvider: &mockProvider{deployments: []*appsv1.Deployment{
		{ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "demo"}, Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "frontend"}}},
		}},
	}}}

	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	var found *Node
	for i := range topo.Nodes {
		if topo.Nodes[i].Name == "antrea-policy" {
			found = &topo.Nodes[i]
		}
	}
	if found == nil {
		var ids []string
		for _, n := range topo.Nodes {
			ids = append(ids, n.ID)
		}
		t.Fatalf("another CNI's NetworkPolicy CRD was dropped from the topology; nodes = %v", ids)
	}
	if group := nodeAPIGroupFromData(found); group != "crd.antrea.io" {
		t.Errorf("foreign policy node API group = %q, want crd.antrea.io", group)
	}
	// The Calico policies from the same build must still be folded and present,
	// so the group-scoped skip is doing its own job too.
	calico := 0
	for _, n := range topo.Nodes {
		if IsCalicoPolicyKind(n.Kind) {
			calico++
		}
	}
	if calico == 0 {
		t.Error("Calico policy nodes disappeared while making room for the foreign CRD")
	}
}

func TestCalicoPolicyRBACTuplesSurviveAJSONRoundTrip(t *testing.T) {
	// Node data is marshalled and decoded on the SSE and hub paths, which turns
	// []string into []any. Failing to read that shape would fail closed and hide
	// every dual-group policy on exactly those paths.
	node := Node{
		ID: "calicoglobalnetworkpolicy//shared", Kind: KindCalicoGlobalNetworkPolicy, Name: "shared",
		Data: map[string]any{
			"apiVersion":  "projectcalico.org/v3",
			"apiVersions": []string{"projectcalico.org/v3", "crd.projectcalico.org/v1"},
		},
	}
	encoded, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Node
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := decoded.Data["apiVersions"].([]any); !ok {
		t.Fatalf("decoded apiGroups is %T, expected the []any shape this test exists for", decoded.Data["apiVersions"])
	}
	tuples, ok := CalicoPolicyRBACTuples(&decoded)
	if !ok || len(tuples) != 2 {
		t.Fatalf("tuples after a round trip = %+v (%v), want one per serving group", tuples, ok)
	}
}

func TestCalicoMatcherRejectsUnusableOptionalSelectors(t *testing.T) {
	workload := newCalicoWorkload("deployment/demo/frontend", "demo", map[string]string{"app": "frontend"}, "api-sa")
	namespaceLabels := map[string]string{"env": "prod"}
	serviceAccountLabels := map[string]string{"team": "payments"}

	for _, test := range []struct {
		name string
		spec map[string]any
		want bool
	}{
		{name: "usable namespace selector", spec: map[string]any{"selector": "app == 'frontend'", "namespaceSelector": "env == 'prod'"}, want: true},
		{name: "unparseable namespace selector", spec: map[string]any{"selector": "app == 'frontend'", "namespaceSelector": "env ==="}},
		{name: "usable service account selector", spec: map[string]any{"selector": "app == 'frontend'", "serviceAccountSelector": "team == 'payments'"}, want: true},
		{name: "service account selector that does not match", spec: map[string]any{"selector": "app == 'frontend'", "serviceAccountSelector": "team == 'other'"}},
		{name: "unparseable service account selector", spec: map[string]any{"selector": "app == 'frontend'", "serviceAccountSelector": "team ==="}},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := calicoTestObject(calicoProjectGroup, "v3", "NetworkPolicy", "demo", test.name, test.spec)
			matched, valid := CompileCalicoPolicyMatcher(policy).Matches(
				workload.endpointLabels, namespaceLabels, workload.serviceAccount, serviceAccountLabels,
			)
			if !valid {
				t.Fatalf("endpoint selector reported invalid; only the endpoint selector may do that")
			}
			if matched != test.want {
				t.Fatalf("matched = %v, want %v", matched, test.want)
			}
		})
	}
}

func TestCalicoNarrowedAllSelectorDoesNotClaimEveryWorkload(t *testing.T) {
	// all() endpoints narrowed by a service-account selector covers whichever
	// workloads use a matching ServiceAccount, not the whole namespace. The
	// match-all shortcut skips the per-workload check, so claiming it here marks
	// workloads protected that nothing protects.
	for _, test := range []struct {
		name string
		spec map[string]any
		want bool
	}{
		{name: "bare all()", spec: map[string]any{"selector": "all()"}, want: true},
		{name: "no selector at all", spec: map[string]any{}, want: true},
		{name: "all() with a service account selector", spec: map[string]any{"selector": "all()", "serviceAccountSelector": "team == 'payments'"}},
		{name: "all() with a namespace selector", spec: map[string]any{"selector": "all()", "namespaceSelector": "env == 'prod'"}},
		{name: "all() with all() selectors", spec: map[string]any{"selector": "all()", "namespaceSelector": "all()"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := calicoTestObject(calicoProjectGroup, "v3", "NetworkPolicy", "demo", test.name, test.spec)
			definition, _ := calicoPolicyDefinitionForNodeKind(KindCalicoNetworkPolicy)
			if got := calicoPolicyMatchesAllWorkloads(policy, definition); got != test.want {
				t.Fatalf("matchesAllWorkloads = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCalicoPolicyReadvertisedUnderAnAuthorizedGroup(t *testing.T) {
	// The node advertises one apiVersion and the UI builds its detail fetch from
	// it. A caller who can read the policy only through the other serving group
	// must not be pointed at the group it cannot address.
	build := func() *Topology {
		return &Topology{Nodes: []Node{{
			ID: "calicoglobalnetworkpolicy//shared", Kind: KindCalicoGlobalNetworkPolicy, Name: "shared",
			Data: map[string]any{
				"apiVersion":  "projectcalico.org/v3",
				"apiVersions": []string{"projectcalico.org/v3", "crd.projectcalico.org/v1"},
			},
		}}}
	}

	legacyOnly := build()
	legacyOnly.StripCalicoPoliciesExcept(map[SARTuple]bool{
		{Group: "crd.projectcalico.org", Resource: "globalnetworkpolicies"}: true,
	})
	if len(legacyOnly.Nodes) != 1 {
		t.Fatal("policy readable through the legacy group was stripped")
	}
	if got := legacyOnly.Nodes[0].Data["apiVersion"]; got != "crd.projectcalico.org/v1" {
		t.Fatalf("apiVersion = %v, want the group this caller can address", got)
	}

	preferred := build()
	preferred.StripCalicoPoliciesExcept(map[SARTuple]bool{
		{Group: "projectcalico.org", Resource: "globalnetworkpolicies"}: true,
	})
	if got := preferred.Nodes[0].Data["apiVersion"]; got != "projectcalico.org/v3" {
		t.Fatalf("apiVersion = %v, want the preferred group left alone", got)
	}

	// The rewrite must not reach the shared build every other viewer reads.
	shared := build()
	original := shared.Nodes[0].Data
	shared.StripCalicoPoliciesExcept(map[SARTuple]bool{
		{Group: "crd.projectcalico.org", Resource: "globalnetworkpolicies"}: true,
	})
	if original["apiVersion"] != "projectcalico.org/v3" {
		t.Fatalf("the original data map was mutated: %v", original["apiVersion"])
	}
}

func TestReadvertiseCalicoPolicyNodesLeavesOtherKindsAlone(t *testing.T) {
	// The neighborhood paths filter node by node rather than through
	// StripCalicoPoliciesExcept, so they re-advertise separately. Only Calico
	// policy nodes may be touched, and only their apiVersion.
	nodes := []Node{
		{ID: "calicoglobalnetworkpolicy//shared", Kind: KindCalicoGlobalNetworkPolicy, Name: "shared", Data: map[string]any{
			"apiVersion":  "projectcalico.org/v3",
			"apiVersions": []string{"projectcalico.org/v3", "crd.projectcalico.org/v1"},
			"namespace":   "",
		}},
		{ID: "networkpolicy/demo/native", Kind: KindNetworkPolicy, Name: "native", Data: map[string]any{
			"apiVersion": "networking.k8s.io/v1", "namespace": "demo",
		}},
	}

	ReadvertiseCalicoPolicyNodes(nodes, func(tuple SARTuple) bool {
		return tuple.Group == "crd.projectcalico.org"
	})

	if got := nodes[0].Data["apiVersion"]; got != "crd.projectcalico.org/v1" {
		t.Fatalf("Calico node apiVersion = %v, want the group the caller holds", got)
	}
	if got := nodes[1].Data["apiVersion"]; got != "networking.k8s.io/v1" {
		t.Fatalf("a native NetworkPolicy was rewritten to %v", got)
	}

	// A nil authorizer must change nothing rather than pick a group at random.
	before := nodes[0].Data["apiVersion"]
	ReadvertiseCalicoPolicyNodes(nodes, nil)
	if nodes[0].Data["apiVersion"] != before {
		t.Fatal("a missing authorizer rewrote the advertised group")
	}
}
