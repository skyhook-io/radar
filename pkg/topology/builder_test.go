package topology

import (
	"fmt"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	k8score "github.com/skyhook-io/radar/pkg/k8score"
)

// mockProvider implements ResourceProvider with configurable slices.
type mockProvider struct {
	pods            []*corev1.Pod
	deployments     []*appsv1.Deployment
	services        []*corev1.Service
	daemonSets      []*appsv1.DaemonSet
	statefulSets    []*appsv1.StatefulSet
	replicaSets     []*appsv1.ReplicaSet
	jobs            []*batchv1.Job
	cronJobs        []*batchv1.CronJob
	ingresses       []*networkingv1.Ingress
	configMaps      []*corev1.ConfigMap
	secrets         []*corev1.Secret
	pvcs            []*corev1.PersistentVolumeClaim
	pvs             []*corev1.PersistentVolume
	hpas            []*autoscalingv2.HorizontalPodAutoscaler
	pdbs            []*policyv1.PodDisruptionBudget
	networkPolicies []*networkingv1.NetworkPolicy
	nodes           []*corev1.Node
}

func (m *mockProvider) Pods() ([]*corev1.Pod, error)                 { return m.pods, nil }
func (m *mockProvider) Services() ([]*corev1.Service, error)         { return m.services, nil }
func (m *mockProvider) Deployments() ([]*appsv1.Deployment, error)   { return m.deployments, nil }
func (m *mockProvider) DaemonSets() ([]*appsv1.DaemonSet, error)     { return m.daemonSets, nil }
func (m *mockProvider) StatefulSets() ([]*appsv1.StatefulSet, error) { return m.statefulSets, nil }
func (m *mockProvider) ReplicaSets() ([]*appsv1.ReplicaSet, error)   { return m.replicaSets, nil }
func (m *mockProvider) Jobs() ([]*batchv1.Job, error)                { return m.jobs, nil }
func (m *mockProvider) CronJobs() ([]*batchv1.CronJob, error)        { return m.cronJobs, nil }
func (m *mockProvider) Ingresses() ([]*networkingv1.Ingress, error)  { return m.ingresses, nil }
func (m *mockProvider) ConfigMaps() ([]*corev1.ConfigMap, error)     { return m.configMaps, nil }
func (m *mockProvider) Secrets() ([]*corev1.Secret, error)           { return m.secrets, nil }
func (m *mockProvider) PersistentVolumeClaims() ([]*corev1.PersistentVolumeClaim, error) {
	return m.pvcs, nil
}
func (m *mockProvider) PersistentVolumes() ([]*corev1.PersistentVolume, error) { return m.pvs, nil }
func (m *mockProvider) HorizontalPodAutoscalers() ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
	return m.hpas, nil
}
func (m *mockProvider) PodDisruptionBudgets() ([]*policyv1.PodDisruptionBudget, error) {
	return m.pdbs, nil
}
func (m *mockProvider) NetworkPolicies() ([]*networkingv1.NetworkPolicy, error) {
	return m.networkPolicies, nil
}
func (m *mockProvider) Nodes() ([]*corev1.Node, error) { return m.nodes, nil }
func (m *mockProvider) GetResourceStatus(kind, namespace, name string) *ResourceStatus {
	return nil
}

type rolloutDynamicProvider struct {
	gvr                 schema.GroupVersionResource
	rollouts            []*unstructured.Unstructured
	listCalls           int
	listNamespacesCalls int
}

func (m *rolloutDynamicProvider) List(_ schema.GroupVersionResource, _ string) ([]*unstructured.Unstructured, error) {
	m.listCalls++
	return nil, nil
}

func (m *rolloutDynamicProvider) ListNamespaces(_ schema.GroupVersionResource, _ []string) ([]*unstructured.Unstructured, error) {
	m.listNamespacesCalls++
	return m.rollouts, nil
}

func (m *rolloutDynamicProvider) Get(_ schema.GroupVersionResource, _, _ string) (*unstructured.Unstructured, error) {
	return nil, nil
}

func (m *rolloutDynamicProvider) GetWatchedResources() []schema.GroupVersionResource {
	return nil
}

func (m *rolloutDynamicProvider) GetDiscoveryStatus() k8score.CRDDiscoveryStatus {
	return k8score.CRDDiscoveryComplete
}

func (m *rolloutDynamicProvider) GetGVR(kindOrName string) (schema.GroupVersionResource, bool) {
	if kindOrName == "Rollout" {
		return m.gvr, true
	}
	return schema.GroupVersionResource{}, false
}

func (m *rolloutDynamicProvider) GetGVRWithGroup(kindOrName, _ string) (schema.GroupVersionResource, bool) {
	return m.GetGVR(kindOrName)
}

func (m *rolloutDynamicProvider) GetKindForGVR(gvr schema.GroupVersionResource) string {
	if gvr == m.gvr {
		return "Rollout"
	}
	return ""
}

func (m *rolloutDynamicProvider) IsCRD(kind string) bool {
	return kind == "Rollout"
}

// --- Generators ---

func makePods(n int, ns string) []*corev1.Pod {
	pods := make([]*corev1.Pod, n)
	for i := range n {
		pods[i] = &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pod-%d", i), Namespace: ns},
		}
	}
	return pods
}

func makeDeployments(n int, ns string) []*appsv1.Deployment {
	deps := make([]*appsv1.Deployment, n)
	for i := range n {
		deps[i] = &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("deploy-%d", i), Namespace: ns},
		}
	}
	return deps
}

func makeServices(n int, ns string) []*corev1.Service {
	svcs := make([]*corev1.Service, n)
	for i := range n {
		svcs[i] = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("svc-%d", i), Namespace: ns},
		}
	}
	return svcs
}

// largeProvider returns a mockProvider that exceeds the large cluster threshold.
// 500 deployments + 500 services + 500 pods(÷5=100) = 1100 estimated nodes.
func largeProvider() *mockProvider {
	return &mockProvider{
		deployments: makeDeployments(500, "default"),
		services:    makeServices(500, "default"),
		pods:        makePods(500, "default"),
	}
}

// smallProvider returns a mockProvider well under the threshold.
func smallProvider() *mockProvider {
	return &mockProvider{
		deployments: makeDeployments(5, "default"),
		services:    makeServices(5, "default"),
		pods:        makePods(10, "default"),
	}
}

// --- Tests ---

func TestBuildResourcesTopology_ReusesListedRolloutsForServiceEdges(t *testing.T) {
	rolloutGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	rollout := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Rollout",
		"metadata": map[string]any{
			"name":      "web",
			"namespace": "prod",
		},
		"spec": map[string]any{
			"replicas": int64(1),
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]any{"app": "web"},
				},
			},
		},
	}}
	dynamic := &rolloutDynamicProvider{
		gvr:      rolloutGVR,
		rollouts: []*unstructured.Unstructured{rollout},
	}
	provider := &mockProvider{
		services: []*corev1.Service{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"},
				Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "web"}},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "web-canary", Namespace: "prod"},
				Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "web"}},
			},
		},
	}

	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if dynamic.listNamespacesCalls != 1 {
		t.Fatalf("expected one Rollout ListNamespaces call, got %d", dynamic.listNamespacesCalls)
	}
	if dynamic.listCalls != 0 {
		t.Fatalf("expected Service matching to reuse listed Rollouts without List calls, got %d", dynamic.listCalls)
	}

	wantTargets := map[string]bool{
		"service/prod/web":        false,
		"service/prod/web-canary": false,
	}
	for _, edge := range topo.Edges {
		if edge.Type == EdgeExposes && edge.Target == "rollout/prod/web" {
			if _, ok := wantTargets[edge.Source]; ok {
				wantTargets[edge.Source] = true
			}
		}
	}
	for serviceID, found := range wantTargets {
		if !found {
			t.Fatalf("expected %s to expose rollout/prod/web; edges=%+v", serviceID, topo.Edges)
		}
	}
}

func TestLargeClusterDetection(t *testing.T) {
	tests := []struct {
		name        string
		provider    *mockProvider
		wantLarge   bool
		description string
	}{
		{
			name:        "small cluster",
			provider:    &mockProvider{deployments: makeDeployments(10, "default"), services: makeServices(10, "default"), pods: makePods(50, "default")},
			wantLarge:   false,
			description: "10+10+10(pods/5) = 30, well under 1000",
		},
		{
			name: "just under threshold",
			provider: &mockProvider{
				deployments: makeDeployments(400, "default"),
				services:    makeServices(400, "default"),
				pods:        makePods(500, "default"), // (500+4)/5 = 100
			},
			wantLarge:   false,
			description: "400+400+100 = 900 < 1000",
		},
		{
			name: "at threshold",
			provider: &mockProvider{
				deployments: makeDeployments(400, "default"),
				services:    makeServices(400, "default"),
				pods:        makePods(1000, "default"), // (1000+4)/5 = 200
			},
			wantLarge:   true,
			description: "400+400+200 = 1000 >= 1000",
		},
		{
			name:        "pods-heavy cluster",
			provider:    &mockProvider{pods: makePods(5000, "default")},
			wantLarge:   true,
			description: "(5000+4)/5 = 1000 pod groups",
		},
		{
			name:        "many workloads no pods",
			provider:    &mockProvider{deployments: makeDeployments(600, "default"), services: makeServices(600, "default")},
			wantLarge:   true,
			description: "600+600 = 1200 > 1000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBuilder(tt.provider)
			opts := DefaultBuildOptions()
			gotLarge, _, _ := b.detectLargeClusterAndOptimize(&opts)
			if gotLarge != tt.wantLarge {
				t.Errorf("detectLargeClusterAndOptimize() = %v, want %v (%s)", gotLarge, tt.wantLarge, tt.description)
			}
		})
	}
}

func TestLargeClusterOptimizations(t *testing.T) {
	b := NewBuilder(largeProvider())
	opts := DefaultBuildOptions()

	if !opts.IncludeConfigMaps {
		t.Fatal("precondition: IncludeConfigMaps should default to true")
	}
	if !opts.IncludePVCs {
		t.Fatal("precondition: IncludePVCs should default to true")
	}

	isLarge, hiddenKinds, _ := b.detectLargeClusterAndOptimize(&opts)
	if !isLarge {
		t.Fatal("expected large cluster detection")
	}

	// MaxIndividualPods reduced from 5 to 2
	if opts.MaxIndividualPods != 2 {
		t.Errorf("MaxIndividualPods = %d, want 2", opts.MaxIndividualPods)
	}

	// ConfigMaps and PVCs auto-hidden
	if opts.IncludeConfigMaps {
		t.Error("IncludeConfigMaps should be false after optimization")
	}
	if opts.IncludePVCs {
		t.Error("IncludePVCs should be false after optimization")
	}
	if !slices.Contains(hiddenKinds, "ConfigMap") {
		t.Errorf("hiddenKinds %v should contain ConfigMap", hiddenKinds)
	}
	if !slices.Contains(hiddenKinds, "PersistentVolumeClaim") {
		t.Errorf("hiddenKinds %v should contain PersistentVolumeClaim", hiddenKinds)
	}
}

func TestSmallClusterUnaffected(t *testing.T) {
	b := NewBuilder(smallProvider())
	opts := DefaultBuildOptions()

	isLarge, hiddenKinds, _ := b.detectLargeClusterAndOptimize(&opts)
	if isLarge {
		t.Error("small cluster should not be detected as large")
	}
	if hiddenKinds != nil {
		t.Errorf("hiddenKinds should be nil for small cluster, got %v", hiddenKinds)
	}
	if opts.MaxIndividualPods != 5 {
		t.Errorf("MaxIndividualPods = %d, want 5 (default)", opts.MaxIndividualPods)
	}
	if !opts.IncludeConfigMaps {
		t.Error("IncludeConfigMaps should remain true")
	}
	if !opts.IncludePVCs {
		t.Error("IncludePVCs should remain true")
	}
}

func TestLargeClusterRequiresNamespaceFilter(t *testing.T) {
	b := NewBuilder(largeProvider())
	opts := DefaultBuildOptions()
	// No namespace filter → should get requiresNamespaceFilter response

	topo, err := b.Build(opts)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if !topo.RequiresNamespaceFilter {
		t.Error("RequiresNamespaceFilter should be true")
	}
	if !topo.LargeCluster {
		t.Error("LargeCluster should be true")
	}
	if len(topo.Nodes) != 0 {
		t.Errorf("Nodes should be empty, got %d", len(topo.Nodes))
	}
	if len(topo.Edges) != 0 {
		t.Errorf("Edges should be empty, got %d", len(topo.Edges))
	}
	if topo.Nodes == nil {
		t.Error("Nodes should be empty slice, not nil (Go nil → JSON null)")
	}
	if topo.Edges == nil {
		t.Error("Edges should be empty slice, not nil (Go nil → JSON null)")
	}
	if len(topo.Warnings) == 0 {
		t.Error("Warnings should contain guidance for MCP consumers")
	}
}

func TestLargeClusterForRelationshipCache(t *testing.T) {
	b := NewBuilder(largeProvider())
	opts := DefaultBuildOptions()
	opts.ForRelationshipCache = true
	// No namespace filter, but ForRelationshipCache bypasses the guard

	topo, err := b.Build(opts)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if topo.RequiresNamespaceFilter {
		t.Error("RequiresNamespaceFilter should be false when ForRelationshipCache is set")
	}
	// Should have actual nodes from the build (deployments + services + pod groups)
	if len(topo.Nodes) == 0 {
		t.Error("ForRelationshipCache should bypass large cluster guard and produce nodes")
	}
	// LargeCluster flag should still be set (for informational purposes)
	if !topo.LargeCluster {
		t.Error("LargeCluster should be true even with ForRelationshipCache")
	}
}

func TestLargeClusterWithNamespaceFilter(t *testing.T) {
	// Create resources across two namespaces — large in total, but filtered to one
	provider := &mockProvider{
		deployments: append(makeDeployments(500, "ns-a"), makeDeployments(500, "ns-b")...),
		services:    append(makeServices(500, "ns-a"), makeServices(500, "ns-b")...),
		pods:        append(makePods(500, "ns-a"), makePods(500, "ns-b")...),
	}

	b := NewBuilder(provider)
	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"ns-a"}

	topo, err := b.Build(opts)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if topo.RequiresNamespaceFilter {
		t.Error("RequiresNamespaceFilter should be false when namespace is specified")
	}
	if len(topo.Nodes) == 0 {
		t.Error("Should produce nodes for the filtered namespace")
	}

	// All nodes should be from ns-a
	for _, n := range topo.Nodes {
		ns, _ := n.Data["namespace"].(string)
		if ns != "" && ns != "ns-a" {
			t.Errorf("Node %s has namespace %s, expected ns-a", n.ID, ns)
		}
	}
}

func TestNamespaceFilterReducesEstimate(t *testing.T) {
	// 2000 deployments spread across 20 namespaces (100 per ns)
	// All-namespace: 2000 estimated nodes → large
	// Single namespace: 100 estimated nodes → small
	var allDeploys []*appsv1.Deployment
	for i := range 20 {
		ns := fmt.Sprintf("ns-%d", i)
		allDeploys = append(allDeploys, makeDeployments(100, ns)...)
	}

	provider := &mockProvider{deployments: allDeploys}
	b := NewBuilder(provider)

	// All namespaces → large
	allOpts := DefaultBuildOptions()
	isLarge, _, _ := b.detectLargeClusterAndOptimize(&allOpts)
	if !isLarge {
		t.Error("all-namespace should be detected as large (2000 deployments)")
	}

	// Single namespace → small
	filteredOpts := DefaultBuildOptions()
	filteredOpts.Namespaces = []string{"ns-0"}
	isLarge, _, _ = b.detectLargeClusterAndOptimize(&filteredOpts)
	if isLarge {
		t.Error("single namespace (100 deployments) should NOT be detected as large")
	}
}

func TestNetworkPolicyTopologyNodes(t *testing.T) {
	provider := &mockProvider{
		deployments: []*appsv1.Deployment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "demo"},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "frontend"}},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "demo"},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "backend"}},
					},
				},
			},
		},
		networkPolicies: []*networkingv1.NetworkPolicy{
			{
				// Specific selector — should create edge to frontend deployment
				ObjectMeta: metav1.ObjectMeta{Name: "allow-frontend", Namespace: "demo"},
				Spec: networkingv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "frontend"},
					},
					PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				},
			},
			{
				// Empty selector — should NOT create edges (matchesAllPods)
				ObjectMeta: metav1.ObjectMeta{Name: "default-deny", Namespace: "demo"},
				Spec: networkingv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{},
					PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				},
			},
			{
				// Different namespace — should not match demo deployments
				ObjectMeta: metav1.ObjectMeta{Name: "other-ns-policy", Namespace: "other"},
				Spec: networkingv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "frontend"},
					},
					PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				},
			},
		},
	}

	b := NewBuilder(provider)
	opts := DefaultBuildOptions()
	topo, err := b.Build(opts)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Check NetworkPolicy nodes exist
	npNodes := make(map[string]Node)
	for _, n := range topo.Nodes {
		if n.Kind == KindNetworkPolicy {
			npNodes[n.Name] = n
		}
	}

	if len(npNodes) != 3 {
		t.Errorf("expected 3 NetworkPolicy nodes, got %d", len(npNodes))
	}

	// Check allow-frontend node exists and is healthy
	if n, ok := npNodes["allow-frontend"]; !ok {
		t.Error("missing allow-frontend NetworkPolicy node")
	} else if n.Status != StatusHealthy {
		t.Errorf("allow-frontend status = %s, want healthy", n.Status)
	}

	// Check default-deny has matchesAllPods flag
	if n, ok := npNodes["default-deny"]; !ok {
		t.Error("missing default-deny NetworkPolicy node")
	} else if n.Data["matchesAllPods"] != true {
		t.Error("default-deny should have matchesAllPods=true")
	}

	// Check edges: allow-frontend should have edge to frontend deployment
	var npEdges []Edge
	for _, e := range topo.Edges {
		if e.Type == EdgeProtects {
			for _, n := range npNodes {
				if e.Source == n.ID {
					npEdges = append(npEdges, e)
				}
			}
		}
	}

	// Only allow-frontend should create edges (default-deny has empty selector, other-ns is different namespace)
	if len(npEdges) != 1 {
		t.Errorf("expected 1 NetworkPolicy edge, got %d", len(npEdges))
		for _, e := range npEdges {
			t.Logf("  edge: %s → %s", e.Source, e.Target)
		}
	}

	if len(npEdges) == 1 {
		if npEdges[0].Source != "networkpolicy/demo/allow-frontend" {
			t.Errorf("edge source = %s, want networkpolicy/demo/allow-frontend", npEdges[0].Source)
		}
		if npEdges[0].Target != "deployment/demo/frontend" {
			t.Errorf("edge target = %s, want deployment/demo/frontend", npEdges[0].Target)
		}
	}
}

func TestNetworkPolicyNamespaceIsolation(t *testing.T) {
	// Policy in ns-a should NOT create edges to deployments in ns-b
	provider := &mockProvider{
		deployments: []*appsv1.Deployment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns-b"},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
					},
				},
			},
		},
		networkPolicies: []*networkingv1.NetworkPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "web-policy", Namespace: "ns-a"},
				Spec: networkingv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "web"},
					},
					PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				},
			},
		},
	}

	b := NewBuilder(provider)
	opts := DefaultBuildOptions()
	topo, err := b.Build(opts)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Should have the NetworkPolicy node but NO edges (different namespace)
	var npEdges int
	for _, e := range topo.Edges {
		if e.Source == "networkpolicy/ns-a/web-policy" {
			npEdges++
		}
	}
	if npEdges != 0 {
		t.Errorf("expected 0 cross-namespace edges, got %d", npEdges)
	}
}

func TestMatchesStringMap(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		selector map[string]any
		want     bool
	}{
		{"exact match", map[string]string{"app": "web"}, map[string]any{"app": "web"}, true},
		{"subset match", map[string]string{"app": "web", "tier": "frontend"}, map[string]any{"app": "web"}, true},
		{"mismatch value", map[string]string{"app": "web"}, map[string]any{"app": "api"}, false},
		{"missing key", map[string]string{"app": "web"}, map[string]any{"tier": "frontend"}, false},
		{"empty selector", map[string]string{"app": "web"}, map[string]any{}, true},
		{"empty labels", map[string]string{}, map[string]any{"app": "web"}, false},
		{"non-string value rejects", map[string]string{"app": "web"}, map[string]any{"app": 123}, false},
		{"nil labels", nil, map[string]any{"app": "web"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesStringMap(tt.labels, tt.selector); got != tt.want {
				t.Errorf("matchesStringMap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAnnotateNodePolicyCoverage(t *testing.T) {
	deployments := []*appsv1.Deployment{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "demo"},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "demo"},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
				},
			},
		},
	}

	netpols := []*networkingv1.NetworkPolicy{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "web-policy", Namespace: "demo"},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "web"},
				},
			},
		},
	}

	nodes := []Node{
		{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "demo"}},
		{ID: "deployment/demo/api", Kind: KindDeployment, Name: "api", Data: map[string]any{"namespace": "demo"}},
		{ID: "service/demo/web", Kind: KindService, Name: "web", Data: map[string]any{"namespace": "demo"}},
	}

	// EdgeProtects from web-policy to web deployment (created by topology builder)
	edges := []Edge{
		{ID: "np-to-web", Source: "networkpolicy/demo/web-policy", Target: "deployment/demo/web", Type: EdgeProtects},
	}

	annotateNodePolicyCoverage(nodes, edges, netpols, deployments, nil, nil)

	// web deployment should be protected
	if nodes[0].Data["policyStatus"] != "protected" {
		t.Errorf("web deployment: got policyStatus=%v, want protected", nodes[0].Data["policyStatus"])
	}

	// api deployment should be unprotected (no matching policy)
	if nodes[1].Data["policyStatus"] != "unprotected" {
		t.Errorf("api deployment: got policyStatus=%v, want unprotected", nodes[1].Data["policyStatus"])
	}

	// service should have no policyStatus (not a workload)
	if _, ok := nodes[2].Data["policyStatus"]; ok {
		t.Errorf("service should not have policyStatus, got %v", nodes[2].Data["policyStatus"])
	}
}
