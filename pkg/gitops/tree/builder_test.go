package tree

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/skyhook-io/radar/pkg/topology"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type fakeDynamic struct {
	objects map[string]*unstructured.Unstructured
	errs    map[string]error

	mu    sync.Mutex
	calls []ResourceRef
}

func (f *fakeDynamic) GetDynamicWithGroup(_ context.Context, kind string, namespace string, name string, group string) (*unstructured.Unstructured, error) {
	ref := ResourceRef{Group: group, Kind: kind, Namespace: namespace, Name: name}
	f.mu.Lock()
	f.calls = append(f.calls, ref)
	f.mu.Unlock()
	if err := f.errs[refKey(ref)]; err != nil {
		return nil, err
	}
	if obj := f.objects[refKey(ref)]; obj != nil {
		return obj, nil
	}
	return f.objects[refKey(ResourceRef{Group: group, Kind: "Application", Namespace: namespace, Name: name})], nil
}

func (f *fakeDynamic) recordedCalls() []ResourceRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ResourceRef{}, f.calls...)
}

func TestBuildArgoTreeUsesManagedInventoryAndOwnershipEdges(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":      "billing",
			"namespace": "argocd",
		},
		"spec": map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}},
		"status": map[string]any{
			"sync":   map[string]any{"status": "Synced"},
			"health": map[string]any{"status": "Healthy"},
			"resources": []any{
				map[string]any{"group": "apps", "kind": "Deployment", "namespace": "prod", "name": "billing", "status": "Synced", "health": map[string]any{"status": "Healthy"}},
				map[string]any{"kind": "Service", "namespace": "prod", "name": "billing", "status": "Synced", "health": map[string]any{"status": "Healthy"}},
			},
		},
	}}

	topo := &topology.Topology{
		Nodes: []topology.Node{
			{ID: "deployment/prod/billing", Kind: topology.KindDeployment, Name: "billing", Status: topology.StatusHealthy, Data: map[string]any{"namespace": "prod", "group": "apps"}},
			{ID: "replicaset/prod/billing-abc", Kind: topology.KindReplicaSet, Name: "billing-abc", Status: topology.StatusHealthy, Data: map[string]any{"namespace": "prod", "group": "apps"}},
			{ID: "pod/prod/billing-abc-1", Kind: topology.KindPod, Name: "billing-abc-1", Status: topology.StatusHealthy, Data: map[string]any{"namespace": "prod"}},
			{ID: "service/prod/billing", Kind: topology.KindService, Name: "billing", Status: topology.StatusHealthy, Data: map[string]any{"namespace": "prod"}},
		},
		Edges: []topology.Edge{
			{ID: "deployment-rs", Source: "deployment/prod/billing", Target: "replicaset/prod/billing-abc", Type: topology.EdgeManages},
			{ID: "rs-pod", Source: "replicaset/prod/billing-abc", Target: "pod/prod/billing-abc-1", Type: topology.EdgeManages},
			{ID: "service-pod", Source: "service/prod/billing", Target: "pod/prod/billing-abc-1", Type: topology.EdgeRoutesTo},
		},
	}

	dynamic := &fakeDynamic{objects: map[string]*unstructured.Unstructured{
		refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "billing"}): app,
	}}
	builder := NewBuilder(dynamic, topo)

	tree, _, err := builder.Build(context.Background(), "applications", "argocd", "billing", "argoproj.io")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	for _, n := range tree.Nodes {
		if n.Remote {
			t.Errorf("in-cluster Application marked %s remote", n.ID)
		}
	}

	assertNodeRole(t, tree, ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"}, RoleDeclared)
	assertNodeRole(t, tree, ResourceRef{Group: "apps", Kind: "ReplicaSet", Namespace: "prod", Name: "billing-abc"}, RoleGenerated)
	assertNodeRole(t, tree, ResourceRef{Kind: "Pod", Namespace: "prod", Name: "billing-abc-1"}, RoleGenerated)
	assertEdge(t, tree, nodeID(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "billing"}), nodeID(ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"}))
	assertEdge(t, tree, nodeID(ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"}), nodeID(ResourceRef{Group: "apps", Kind: "ReplicaSet", Namespace: "prod", Name: "billing-abc"}))
	assertNoEdge(t, tree, "service/prod/billing", "pod/prod/billing-abc-1")
}

func TestBuildDoesNotEnrichManagedResourcesOutsideAllowedNamespaces(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":      "billing",
			"namespace": "argocd",
		},
		"spec": map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}},
		"status": map[string]any{
			"sync":   map[string]any{"status": "Synced"},
			"health": map[string]any{"status": "Healthy"},
			"resources": []any{
				map[string]any{"group": "apps", "kind": "Deployment", "namespace": "prod", "name": "billing", "status": "Synced"},
			},
		},
	}}
	deployment := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":      "billing",
			"namespace": "prod",
			"labels":    map[string]any{"secret-label": "should-not-leak"},
		},
	}}
	dynamic := &fakeDynamic{objects: map[string]*unstructured.Unstructured{
		refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "billing"}): app,
		refKey(ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"}):           deployment,
	}}

	tree, _, err := NewBuilder(dynamic, nil).
		WithAllowedNamespaces([]string{"argocd"}).
		Build(context.Background(), "applications", "argocd", "billing", "argoproj.io")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	deploymentID := nodeID(ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"})
	for _, node := range tree.Nodes {
		if node.ID != deploymentID {
			continue
		}
		if _, ok := node.Data["labels"]; ok {
			t.Fatalf("deployment node leaked labels from disallowed namespace: %#v", node.Data["labels"])
		}
		assertNoDynamicCall(t, dynamic, ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"})
		return
	}
	t.Fatalf("deployment node %s not found", deploymentID)
}

func assertNodeRole(t *testing.T, tree *ResourceTree, ref ResourceRef, role NodeRole) {
	t.Helper()
	id := nodeID(ref)
	for _, node := range tree.Nodes {
		if node.ID == id {
			if node.Role != role {
				t.Fatalf("node %s role = %s, want %s", id, node.Role, role)
			}
			return
		}
	}
	t.Fatalf("node %s not found", id)
}

func assertEdge(t *testing.T, tree *ResourceTree, source string, target string) {
	t.Helper()
	for _, edge := range tree.Edges {
		if edge.Source == source && edge.Target == target {
			return
		}
	}
	t.Fatalf("edge %s -> %s not found", source, target)
}

func assertNoEdge(t *testing.T, tree *ResourceTree, source string, target string) {
	t.Helper()
	for _, edge := range tree.Edges {
		if edge.Source == source && edge.Target == target {
			t.Fatalf("unexpected edge %s -> %s", source, target)
		}
	}
}

func assertNoDynamicCall(t *testing.T, dynamic *fakeDynamic, ref ResourceRef) {
	t.Helper()
	for _, call := range dynamic.recordedCalls() {
		if call.Group == ref.Group && call.Kind == ref.Kind && call.Namespace == ref.Namespace && call.Name == ref.Name {
			t.Fatalf("unexpected dynamic lookup for %#v", ref)
		}
	}
}

func TestBuildUnknownKindWarnsOnceAndKeepsSyntheticNodes(t *testing.T) {
	unknownErr := errors.New("unknown resource kind: Prometheus (group: monitoring.coreos.com)")
	resources := make([]any, 0, 13)
	for i := 0; i < 12; i++ {
		resources = append(resources, map[string]any{
			"group": "monitoring.coreos.com", "kind": "Prometheus", "namespace": "monitoring",
			"name": fmt.Sprintf("prom-%d", i), "status": "Synced",
		})
	}
	resources = append(resources, map[string]any{
		"group": "apps", "kind": "Deployment", "namespace": "prod", "name": "billing", "status": "Synced",
	})
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "monitoring", "namespace": "argocd"},
		"spec":       map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}},
		"status":     map[string]any{"resources": resources},
	}}
	deployment := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "billing", "namespace": "prod", "labels": map[string]any{"a": "b"}},
	}}
	dynamic := &fakeDynamic{
		objects: map[string]*unstructured.Unstructured{
			refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): app,
			refKey(ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"}):              deployment,
		},
		errs: map[string]error{},
	}
	for i := 0; i < 12; i++ {
		dynamic.errs[refKey(ResourceRef{Group: "monitoring.coreos.com", Kind: "Prometheus", Namespace: "monitoring", Name: fmt.Sprintf("prom-%d", i)})] = unknownErr
	}

	tree, _, err := NewBuilder(dynamic, nil).
		WithUnknownKindMatcher(func(err error) bool { return errors.Is(err, unknownErr) }).
		Build(context.Background(), "applications", "argocd", "monitoring", "argoproj.io")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	var warned []string
	for _, w := range tree.Warnings {
		if strings.Contains(w, "unavailable in this cluster's API discovery") {
			warned = append(warned, w)
		}
	}
	if len(warned) != 1 {
		t.Fatalf("want exactly one unknown-kind warning, got %d: %v", len(warned), tree.Warnings)
	}
	if !strings.Contains(warned[0], "Prometheus (monitoring.coreos.com)") {
		t.Fatalf("warning should name the kind+group: %q", warned[0])
	}

	// The nodes still render from Argo's status.resources — an absent CRD
	// hides enrichment, never the resource itself.
	assertNodeRole(t, tree, ResourceRef{Group: "monitoring.coreos.com", Kind: "Prometheus", Namespace: "monitoring", Name: "prom-0"}, RoleDeclared)
	// The known kind was still enriched.
	depID := nodeID(ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"})
	for _, node := range tree.Nodes {
		if node.ID == depID {
			if _, ok := node.Data["labels"]; !ok {
				t.Fatalf("Deployment node missing enrichment: %#v", node.Data)
			}
		}
	}
}

func TestBuildParallelEnrichmentMatchesObjects(t *testing.T) {
	resources := make([]any, 0, 40)
	objects := map[string]*unstructured.Unstructured{}
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("cm-%d", i)
		resources = append(resources, map[string]any{"kind": "ConfigMap", "namespace": "prod", "name": name, "status": "Synced"})
		objects[refKey(ResourceRef{Kind: "ConfigMap", Namespace: "prod", Name: name})] = &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]any{"name": name, "namespace": "prod", "uid": "uid-" + name},
		}}
	}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "cms", "namespace": "argocd"},
		"spec":       map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}},
		"status":     map[string]any{"resources": resources},
	}}
	objects[refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "cms"})] = app

	tree, _, err := NewBuilder(&fakeDynamic{objects: objects}, nil).
		Build(context.Background(), "applications", "argocd", "cms", "argoproj.io")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	// Every node must carry its own object's UID — a mismatched UID would
	// mean the parallel prefetch wired an object to the wrong ref.
	found := 0
	for _, node := range tree.Nodes {
		if node.Ref.Kind != "ConfigMap" {
			continue
		}
		found++
		if node.Ref.UID != "uid-"+node.Ref.Name {
			t.Fatalf("node %s has UID %q, want %q", node.Ref.Name, node.Ref.UID, "uid-"+node.Ref.Name)
		}
	}
	if found != 40 {
		t.Fatalf("found %d ConfigMap nodes, want 40", found)
	}
}

func findNode(t *testing.T, tree *ResourceTree, ref ResourceRef) Node {
	t.Helper()
	for _, n := range tree.Nodes {
		if n.Ref.Group == ref.Group && n.Ref.Kind == ref.Kind && n.Ref.Namespace == ref.Namespace && n.Ref.Name == ref.Name {
			return n
		}
	}
	t.Fatalf("node %+v not in tree", ref)
	return Node{}
}

func healthProvenanceApp(status map[string]any) *unstructured.Unstructured {
	status["sync"] = map[string]any{"status": "Synced"}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "billing", "namespace": "argocd"},
		"spec":       map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}},
		"status":     status,
	}}
}

func healthProvenanceTopo() *topology.Topology {
	return &topology.Topology{Nodes: []topology.Node{
		{ID: "deployment/prod/billing", Kind: topology.KindDeployment, Name: "billing", Status: topology.StatusUnhealthy, Data: map[string]any{"namespace": "prod", "group": "apps"}},
	}}
}

// TestBuild_ControllerHealthWinsOverTopology: Argo persisted Healthy for a
// Deployment Radar's topology sees as unhealthy. The controller's verdict is
// the node's health AND its tone — the graph must not paint the node red
// on Radar's opinion while the chip says Healthy.
func TestBuild_ControllerHealthWinsOverTopology(t *testing.T) {
	app := healthProvenanceApp(map[string]any{
		"health": map[string]any{"status": "Healthy"},
		"resources": []any{
			map[string]any{"group": "apps", "kind": "Deployment", "namespace": "prod", "name": "billing", "status": "Synced", "health": map[string]any{"status": "Healthy"}},
		},
	})
	dynamic := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "billing"}): app}}
	tree, _, err := NewBuilder(dynamic, healthProvenanceTopo()).Build(context.Background(), "applications", "argocd", "billing", "argoproj.io")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if tree.HealthMode != HealthModeInline || tree.RemoteDestination {
		t.Errorf("HealthMode=%q RemoteDestination=%v, want inline/false", tree.HealthMode, tree.RemoteDestination)
	}
	n := findNode(t, tree, ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"})
	if n.Health != "Healthy" || n.HealthSource != HealthSourceController || n.TopologyStatus != "healthy" {
		t.Errorf("node = health %q source %q tone %q, want Healthy/controller/healthy", n.Health, n.HealthSource, n.TopologyStatus)
	}
}

// TestBuild_AppTreeModeFillsTopologyKindsFromRadar: Argo 3 default — no
// per-resource health in the CR. Topology kinds get Radar's status,
// labelled radar; kinds Radar doesn't model stay without health.
func TestBuild_AppTreeModeFillsTopologyKindsFromRadar(t *testing.T) {
	app := healthProvenanceApp(map[string]any{
		"health":               map[string]any{"status": "Degraded"},
		"resourceHealthSource": "appTree",
		"resources": []any{
			map[string]any{"group": "apps", "kind": "Deployment", "namespace": "prod", "name": "billing", "status": "Synced"},
			map[string]any{"group": "external-secrets.io", "kind": "ClusterSecretStore", "name": "platform", "status": "Synced"},
		},
	})
	dynamic := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "billing"}): app}}
	tree, _, err := NewBuilder(dynamic, healthProvenanceTopo()).Build(context.Background(), "applications", "argocd", "billing", "argoproj.io")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if tree.HealthMode != HealthModeAppTree {
		t.Errorf("HealthMode = %q, want appTree", tree.HealthMode)
	}
	dep := findNode(t, tree, ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"})
	if dep.Health != "Degraded" || dep.HealthSource != HealthSourceRadar || dep.TopologyStatus != "unhealthy" {
		t.Errorf("Deployment = health %q source %q tone %q, want Degraded/radar/unhealthy", dep.Health, dep.HealthSource, dep.TopologyStatus)
	}
	css := findNode(t, tree, ResourceRef{Group: "external-secrets.io", Kind: "ClusterSecretStore", Name: "platform"})
	if css.Health != "" || css.HealthSource != "" || css.TopologyStatus != "unknown" {
		t.Errorf("ClusterSecretStore = health %q source %q tone %q, want no health (nothing fabricated)", css.Health, css.HealthSource, css.TopologyStatus)
	}
	if tree.Summary.Degraded != 1 {
		t.Errorf("Summary.Degraded = %d, want 1 (the Radar-derived Deployment)", tree.Summary.Degraded)
	}
}

// TestBuild_RemoteDestinationReadsNothingLocal: an app deploying to a
// spoke cluster declares a Deployment that happens to share kind/ns/name
// with an unhealthy local one. The local object's health, ownership edges
// and metadata must not be attributed to the remote resource.
func TestBuild_RemoteDestinationReadsNothingLocal(t *testing.T) {
	app := healthProvenanceApp(map[string]any{
		"health":               map[string]any{"status": "Degraded"},
		"resourceHealthSource": "appTree",
		"resources": []any{
			map[string]any{"group": "apps", "kind": "Deployment", "namespace": "prod", "name": "billing", "status": "Synced"},
		},
	})
	app.Object["spec"] = map[string]any{"destination": map[string]any{"server": "https://spoke-1.example.com:6443"}}
	localDep := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "billing", "namespace": "prod", "uid": "local-uid", "labels": map[string]any{"local": "yes"}},
	}}
	dynamic := &fakeDynamic{objects: map[string]*unstructured.Unstructured{
		refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "billing"}): app,
		refKey(ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"}):           localDep,
	}}
	topo := healthProvenanceTopo()
	topo.Nodes = append(topo.Nodes, topology.Node{ID: "pod/prod/billing-1", Kind: topology.KindPod, Name: "billing-1", Status: topology.StatusUnhealthy, Data: map[string]any{"namespace": "prod"}})
	topo.Edges = []topology.Edge{{ID: "e", Source: "deployment/prod/billing", Target: "pod/prod/billing-1", Type: topology.EdgeManages}}
	tree, _, err := NewBuilder(dynamic, topo).Build(context.Background(), "applications", "argocd", "billing", "argoproj.io")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !tree.RemoteDestination {
		t.Fatalf("RemoteDestination = false, want true for a spoke destination")
	}
	dep := findNode(t, tree, ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"})
	if dep.Health != "" || dep.HealthSource != "" || dep.TopologyStatus != "unknown" {
		t.Errorf("remote Deployment took local health: %+v", dep)
	}
	if dep.Ref.UID != "" || dep.Data["labels"] != nil {
		t.Errorf("remote Deployment took local object metadata: uid=%q data=%v", dep.Ref.UID, dep.Data)
	}
	if !dep.Remote {
		t.Error("a remote Application's declared resource must be marked remote")
	}
	if root := findNode(t, tree, ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "billing"}); root.Remote {
		t.Error("the Application itself lives on this cluster")
	}
	for _, n := range tree.Nodes {
		if n.Ref.Kind == "Pod" {
			t.Errorf("local Pod attached to a remote app's tree: %+v", n.Ref)
		}
	}
	if tree.Summary.Degraded != 0 {
		t.Errorf("Summary.Degraded = %d, want 0 (nothing local counts)", tree.Summary.Degraded)
	}
	assertNoDynamicCall(t, dynamic, ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"})
}

// A Flux Kustomization with spec.kubeConfig applies to another cluster: like a
// remote Argo destination, its inventory must not pick up a same-named local
// Deployment's health, metadata or children.
func TestBuild_RemoteFluxKustomizationReadsNothingLocal(t *testing.T) {
	ks := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "fleet-prod", "namespace": "flux-system"},
		"spec": map[string]any{
			"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "prod-kubeconfig"}},
			"sourceRef":  map[string]any{"kind": "GitRepository", "name": "fleet"},
		},
		"status": map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "prod_billing_apps_Deployment", "v": "v1"},
		}}},
	}}
	localDep := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "billing", "namespace": "prod", "uid": "local-uid", "labels": map[string]any{"local": "yes"}},
	}}
	dynamic := &fakeDynamic{objects: map[string]*unstructured.Unstructured{
		refKey(ResourceRef{Group: "kustomize.toolkit.fluxcd.io", Kind: "kustomizations", Namespace: "flux-system", Name: "fleet-prod"}): ks,
		refKey(ResourceRef{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "flux-system", Name: "fleet-prod"}):  ks,
		refKey(ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"}):                                      localDep,
	}}
	topo := &topology.Topology{
		Nodes: []topology.Node{
			{ID: "deployment/prod/billing", Kind: topology.KindDeployment, Name: "billing", Status: topology.StatusUnhealthy, Data: map[string]any{"namespace": "prod", "group": "apps"}},
			{ID: "pod/prod/billing-1", Kind: topology.KindPod, Name: "billing-1", Status: topology.StatusUnhealthy, Data: map[string]any{"namespace": "prod"}},
		},
		Edges: []topology.Edge{{ID: "e", Source: "deployment/prod/billing", Target: "pod/prod/billing-1", Type: topology.EdgeManages}},
	}
	tree, _, err := NewBuilder(dynamic, topo).Build(context.Background(), "kustomizations", "flux-system", "fleet-prod", "kustomize.toolkit.fluxcd.io")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !tree.RemoteDestination {
		t.Fatal("RemoteDestination = false, want true for a kubeConfig Kustomization")
	}
	dep := findNode(t, tree, ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"})
	if dep.Ref.UID != "" || dep.Data["labels"] != nil || dep.TopologyStatus != "unknown" {
		t.Errorf("remote Deployment took local state: %+v", dep)
	}
	if !dep.Remote {
		t.Error("an inventory entry of a remote Kustomization must be marked remote")
	}
	// The source and the root live on the controller's cluster.
	src := findNode(t, tree, ResourceRef{Group: "source.toolkit.fluxcd.io", Kind: "GitRepository", Namespace: "flux-system", Name: "fleet"})
	root := findNode(t, tree, ResourceRef{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "flux-system", Name: "fleet-prod"})
	if src.Remote || root.Remote {
		t.Errorf("local nodes marked remote: source=%v root=%v", src.Remote, root.Remote)
	}
	for _, n := range tree.Nodes {
		if n.Ref.Kind == "Pod" {
			t.Errorf("local Pod attached to a remote Kustomization's tree: %+v", n.Ref)
		}
	}
	assertNoDynamicCall(t, dynamic, ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "billing"})
}

// A HelmRelease has no inventory, so its tree is recovered from local objects
// carrying its Helm labels — which a release targeting another cluster must
// not do: a local workload with matching labels belongs to someone else.
func TestBuild_RemoteFluxHelmReleaseRecoversNothingLocal(t *testing.T) {
	hr := helmRelease("flux-system", "podinfo")
	hr.Object["spec"] = map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "prod-kubeconfig"}}}
	dynamic := &fakeDynamic{objects: map[string]*unstructured.Unstructured{
		refKey(ResourceRef{Group: "helm.toolkit.fluxcd.io", Kind: "helmreleases", Namespace: "flux-system", Name: "podinfo"}): hr,
		refKey(ResourceRef{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "flux-system", Name: "podinfo"}):  hr,
	}}
	topo := &topology.Topology{Nodes: []topology.Node{
		topoNode("Deployment", "demo", "podinfo", map[string]string{fluxHelmNameLabel: "podinfo", fluxHelmNamespaceLabel: "flux-system"}),
	}}
	tree, _, err := NewBuilder(dynamic, topo).Build(context.Background(), "helmreleases", "flux-system", "podinfo", "helm.toolkit.fluxcd.io")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !tree.RemoteDestination {
		t.Fatal("RemoteDestination = false, want true for a kubeConfig HelmRelease")
	}
	for _, n := range tree.Nodes {
		if n.Ref.Kind == "Deployment" {
			t.Fatalf("remote HelmRelease picked up a local workload: %+v", n.Ref)
		}
	}
}

func TestFluxTreeFollowsObservedChartSourceAndGatesKinds(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(fmt.Sprintf("inline=%t", inline), func(t *testing.T) {
			hr := helmRelease("team", "app")
			hr.Object["spec"] = map[string]any{"chartRef": map[string]any{"kind": "HelmChart", "name": "chart", "namespace": "shared"}}
			if inline {
				hr.Object["spec"] = map[string]any{"chart": map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "repo", "namespace": "shared"}}}}
				hr.Object["status"] = map[string]any{"helmChart": "shared/chart"}
			}
			chart := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "HelmChart", "metadata": map[string]any{"name": "chart", "namespace": "shared", "labels": map[string]any{"private": "chart-label"}}, "spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "repo"}}}}
			repo := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "HelmRepository", "metadata": map[string]any{"name": "repo", "namespace": "shared"}}}
			hrr := ResourceRef{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "team", Name: "app"}
			cr := ResourceRef{Group: "source.toolkit.fluxcd.io", Kind: "HelmChart", Namespace: "shared", Name: "chart"}
			rr := ResourceRef{Group: "source.toolkit.fluxcd.io", Kind: "HelmRepository", Namespace: "shared", Name: "repo"}
			topo := &topology.Topology{Nodes: []topology.Node{{ID: "helmchart/shared/chart", Kind: "HelmChart", Name: "chart", Data: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "namespace": "shared", "labels": map[string]string{fluxHelmNameLabel: "app", fluxHelmNamespaceLabel: "team"}}}}}
			for _, denied := range []string{"", "HelmChart", "HelmRepository"} {
				t.Run(denied, func(t *testing.T) {
					getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(hrr): hr, refKey(cr): chart, refKey(rr): repo}}
					builder := NewBuilder(getter, topo).WithReadCheck(func(ref ResourceRef) bool { return ref.Kind != denied })
					tree, _, err := builder.Build(context.Background(), "HelmRelease", "team", "app", "helm.toolkit.fluxcd.io")
					if err != nil {
						t.Fatal(err)
					}
					wantNodes := 3
					if denied == "HelmChart" {
						wantNodes = 1
						if inline {
							wantNodes = 2
						}
					}
					if denied == "HelmRepository" {
						wantNodes = 2
					}
					wantEdges := wantNodes - 1
					if inline && denied == "" {
						wantEdges++
					}
					if len(tree.Nodes) != wantNodes || len(tree.Edges) != wantEdges {
						t.Fatalf("denied %s: %+v", denied, tree)
					}
					for _, call := range getter.recordedCalls() {
						if call.Kind == denied {
							t.Fatalf("denied kind fetched: %+v", call)
						}
					}
					if tree.Summary.Dependencies != wantNodes-1 || tree.Summary.Declared != 0 || tree.Summary.Degraded != 0 {
						t.Fatalf("dependency membership: %+v", tree.Summary)
					}
					if denied == "" {
						chartDependency := false
						for _, edge := range tree.Edges {
							if edge.Source == nodeID(hrr) && edge.Target == nodeID(cr) && edge.Type == EdgeSource {
								chartDependency = true
							}
						}
						if !chartDependency {
							t.Fatalf("chart treated as installed release content: %+v", tree.Edges)
						}
						found := false
						for _, edge := range tree.Edges {
							if edge.Source == nodeID(cr) && edge.Target == nodeID(rr) && edge.Type == EdgeSource {
								found = true
							}
						}
						if !found {
							t.Fatalf("chart source edge missing: %+v", tree.Edges)
						}
					}
				})
			}
			if inline {
				getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(hrr): hr, refKey(rr): repo}}
				tree, _, err := NewBuilder(getter, nil).Build(context.Background(), "HelmRelease", "team", "app", "helm.toolkit.fluxcd.io")
				if err != nil {
					t.Fatal(err)
				}
				findNode(t, tree, rr)
				if len(tree.Edges) != 2 {
					t.Fatalf("unobserved chart lost declared source: %+v", tree)
				}
				for _, edge := range tree.Edges {
					if edge.Source == nodeID(cr) {
						t.Fatalf("unobserved chart acquired source: %+v", edge)
					}
				}
			}

		})
	}
}

func TestFluxBootstrapSourceRetainsInventoryOwnership(t *testing.T) {
	ks := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": map[string]any{"name": "flux-system", "namespace": "flux-system"}, "spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "flux-system"}}, "status": map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "flux-system_flux-system_source.toolkit.fluxcd.io_GitRepository", "v": "v1"}}}}}}
	repo := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository", "metadata": map[string]any{"name": "flux-system", "namespace": "flux-system"}}}
	kr := ResourceRef{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "flux-system", Name: "flux-system"}
	rr := ResourceRef{Group: "source.toolkit.fluxcd.io", Kind: "GitRepository", Namespace: "flux-system", Name: "flux-system"}
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(kr): ks, refKey(rr): repo}}
	tree, _, err := NewBuilder(getter, nil).Build(context.Background(), "Kustomization", "flux-system", "flux-system", "kustomize.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	repoNode := findNode(t, tree, rr)
	if repoNode.Dependency || repoNode.Data["relationship"] != "source" || tree.Summary.Declared != 1 || tree.Summary.Dependencies != 0 {
		t.Fatalf("bootstrap membership lost: node=%+v summary=%+v", repoNode, tree.Summary)
	}
	if len(getter.recordedCalls()) != 2 {
		t.Fatalf("overlapping source fetched twice: %v", getter.recordedCalls())
	}
	if len(tree.Edges) != 1 || tree.Edges[0].Type != EdgeOwns {
		t.Fatalf("bootstrap inventory ownership overwritten: %+v", tree.Edges)
	}
}

func TestReadableGeneratedDescendantSurvivesDeniedIntermediate(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"namespace": "argocd", "name": "app"}, "spec": map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}}, "status": map[string]any{"resources": []any{map[string]any{"group": "apps", "kind": "Deployment", "namespace": "prod", "name": "app"}}}}}
	topo := &topology.Topology{Nodes: []topology.Node{
		{ID: "deployment/prod/app", Kind: topology.KindDeployment, Name: "app", Data: map[string]any{"namespace": "prod", "apiVersion": "apps/v1"}},
		{ID: "replicaset/prod/hidden", Kind: topology.KindReplicaSet, Name: "hidden", Data: map[string]any{"namespace": "prod", "apiVersion": "apps/v1", "labels": map[string]string{"private": "hidden-metadata"}}},
		{ID: "pod/prod/visible", Kind: topology.KindPod, Name: "visible", Data: map[string]any{"namespace": "prod", "apiVersion": "v1"}},
	}, Edges: []topology.Edge{{Source: "deployment/prod/app", Target: "replicaset/prod/hidden", Type: topology.EdgeManages}, {Source: "replicaset/prod/hidden", Target: "pod/prod/visible", Type: topology.EdgeManages}}}
	for _, denied := range []string{"ReplicaSet", "Deployment"} {
		t.Run(denied, func(t *testing.T) {
			getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "app"}): app}}
			tree, _, err := NewBuilder(getter, topo).WithReadCheck(func(ref ResourceRef) bool { return ref.Kind != denied }).Build(context.Background(), "Application", "argocd", "app", "argoproj.io")
			if err != nil {
				t.Fatal(err)
			}
			visible := false
			for _, n := range tree.Nodes {
				if n.Ref.Kind == denied {
					t.Fatalf("denied node emitted: %+v", n)
				}
				if n.Ref.Kind == "Pod" && n.Ref.Name == "visible" {
					visible = true
				}
				if denied == "ReplicaSet" && strings.Contains(fmt.Sprint(n.Data), "hidden-metadata") {
					t.Fatalf("denied metadata leaked: %+v", n.Data)
				}
			}
			if !visible || len(tree.Warnings) == 0 {
				t.Fatalf("readable descendant hidden or no incomplete warning: %+v", tree)
			}
			for _, e := range tree.Edges {
				for _, n := range tree.Nodes {
					if denied == "ReplicaSet" && n.Ref.Kind == "Pod" && e.Target == n.ID {
						t.Fatalf("invented shortcut to Pod across denied owner: %+v", e)
					}
				}
			}
		})
	}
}

func TestDeniedFluxSourceIsDisclosedWithoutFetchingIt(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "app", "namespace": "prod"},
		"spec":     map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "private"}},
	}}
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{
		refKey(ResourceRef{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "prod", Name: "app"}): root,
	}}
	tree, _, err := NewBuilder(getter, nil).WithReadCheck(func(ref ResourceRef) bool { return ref.Kind != "GitRepository" }).Build(context.Background(), "Kustomization", "prod", "app", "kustomize.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Nodes) != 1 || len(tree.Warnings) == 0 {
		t.Fatalf("tree = %+v", tree)
	}
	for _, call := range getter.recordedCalls() {
		if call.Kind == "GitRepository" {
			t.Fatalf("denied source fetched: %+v", call)
		}
	}
	if strings.Contains(strings.Join(tree.Warnings, " "), "private") {
		t.Fatalf("warning leaks source name: %v", tree.Warnings)
	}
}

func TestGeneratedTopologyReferencesKeepExactAPIIdentity(t *testing.T) {
	for _, tc := range []struct {
		node        topology.Node
		kind, group string
	}{
		{topology.Node{Kind: topology.KindDeployment, Name: "web", Data: map[string]any{"namespace": "team"}}, "Deployment", "apps"},
		{topology.Node{Kind: topology.KindCAPICluster, Name: "cluster", Data: map[string]any{"namespace": "team", "apiVersion": "cluster.x-k8s.io/v1beta1"}}, "Cluster", "cluster.x-k8s.io"},
		{topology.Node{Kind: topology.KindPodGroup, Name: "batch", Data: map[string]any{"namespace": "team", "apiVersion": "scheduling.volcano.sh/v1beta1"}}, "PodGroup", "scheduling.volcano.sh"},
		{topology.Node{Kind: topology.KindPodGroup, Name: "display", Data: map[string]any{"namespace": "team", "podCount": 7}}, "PodGroup", ""},
	} {
		ref := refFromTopologyNode(tc.node)
		if ref.Kind != tc.kind || ref.Group != tc.group || ref.Namespace != "team" {
			t.Fatalf("node=%+v ref=%+v", tc.node, ref)
		}
	}
}

func TestExactDeclarationCannotHydrateDeniedCoreCollision(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "app", "namespace": "team"},
		"spec":     map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}},
		"status":   map[string]any{"resources": []any{map[string]any{"group": "custom.example", "kind": "Secret", "namespace": "team", "name": "shared"}}},
	}}
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{
		refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "team", Name: "app"}): root,
	}}
	topo := &topology.Topology{Nodes: []topology.Node{
		{ID: "secret/team/shared", Kind: topology.KindSecret, Name: "shared", Status: topology.StatusHealthy, Data: map[string]any{"namespace": "team", "labels": map[string]string{"private": "denied-core-metadata"}}},
		{ID: "pod/team/private", Kind: topology.KindPod, Name: "private", Data: map[string]any{"namespace": "team"}},
	}, Edges: []topology.Edge{{Source: "secret/team/shared", Target: "pod/team/private", Type: topology.EdgeManages}}}
	result, _, err := NewBuilder(getter, topo).WithReadCheck(func(ref ResourceRef) bool {
		return ref.Kind != "Secret" || ref.Group == "custom.example"
	}).Build(context.Background(), "Application", "team", "app", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 2 {
		t.Fatalf("wrong-group descendants emitted: %+v", result.Nodes)
	}
	for _, n := range result.Nodes {
		if strings.Contains(fmt.Sprint(n.Data), "denied-core-metadata") || n.Ref.Kind == "Pod" {
			t.Fatalf("wrong-group hydration: %+v", n)
		}
		if n.Ref.Kind == "Secret" && (n.Health != "" || n.Ref.Group != "custom.example") {
			t.Fatalf("wrong-group health/identity: %+v", n)
		}
	}
	for _, call := range getter.recordedCalls() {
		if call.Kind == "Secret" && call.Group == "" {
			t.Fatalf("denied core fetch: %+v", call)
		}
	}
}

func TestCoreDeclarationNeverHydratesCustomCollision(t *testing.T) {
	for _, withCore := range []bool{false, true} {
		root := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
			"metadata": map[string]any{"name": "app", "namespace": "team"},
			"spec":     map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}},
			"status":   map[string]any{"resources": []any{map[string]any{"kind": "Service", "namespace": "team", "name": "dual"}}},
		}}
		getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "team", Name: "app"}): root}}
		topo := &topology.Topology{}
		if withCore {
			topo.Nodes = append(topo.Nodes, topology.Node{ID: "service/team/dual", Kind: topology.KindService, Name: "dual", Data: map[string]any{"namespace": "team", "labels": map[string]string{"source": "core"}}})
		}
		topo.Nodes = append(topo.Nodes, topology.Node{ID: "knative/team/dual", Kind: topology.KindKnativeService, Name: "dual", Status: topology.StatusHealthy, Data: map[string]any{"namespace": "team", "apiVersion": "serving.knative.dev/v1", "labels": map[string]string{"source": "custom"}}})
		tree, _, err := NewBuilder(getter, topo).Build(context.Background(), "Application", "team", "app", "argoproj.io")
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range tree.Nodes {
			if node.Ref.Kind == "Service" && (strings.Contains(fmt.Sprint(node.Data), "custom") || !withCore && node.Health != "") {
				t.Fatalf("core declaration hydrated from custom group: %+v", node)
			}
		}
	}
}

func TestRemoteInventoryDoesNotUseLocalReadPermissions(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"name": "fleet", "namespace": "argocd"},
		"spec":   map[string]any{"destination": map[string]any{"server": "https://remote.example"}},
		"status": map[string]any{"resources": []any{map[string]any{"kind": "Secret", "namespace": "remote", "name": "declared", "status": "Synced"}}},
	}}
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "fleet"}): root}}
	tree, _, err := NewBuilder(getter, nil).WithReadCheck(func(ref ResourceRef) bool { return ref.Kind != "Secret" }).Build(context.Background(), "Application", "argocd", "fleet", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	node := findNode(t, tree, ResourceRef{Kind: "Secret", Namespace: "remote", Name: "declared"})
	if !node.Remote || node.Sync != "Synced" || node.Data["labels"] != nil {
		t.Fatalf("remote declaration lost or enriched: %+v", node)
	}
	assertNoDynamicCall(t, getter, ResourceRef{Kind: "Secret", Namespace: "remote", Name: "declared"})
}

func TestExactLookupRetainsServedCalicoIdentities(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"name": "app", "namespace": "team"}, "spec": map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}}, "status": map[string]any{"resources": []any{map[string]any{"group": "crd.projectcalico.org", "kind": "NetworkPolicy", "namespace": "team", "name": "policy"}}}}}
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "team", Name: "app"}): root}}
	topo := &topology.Topology{Nodes: []topology.Node{{ID: "calico/team/policy", Kind: topology.KindCalicoNetworkPolicy, Name: "policy", Status: topology.StatusHealthy, Data: map[string]any{"namespace": "team", "apiVersion": "projectcalico.org/v3", "apiVersions": []string{"projectcalico.org/v3", "crd.projectcalico.org/v1"}, "labels": map[string]string{"observed": "true"}}}}}
	tree, _, err := NewBuilder(getter, topo).Build(context.Background(), "Application", "team", "app", "argoproj.io")
	if err != nil {
		t.Fatal(err)
	}
	policy := findNode(t, tree, ResourceRef{Group: "crd.projectcalico.org", Kind: "NetworkPolicy", Namespace: "team", Name: "policy"})
	if policy.Health != "Healthy" || !strings.Contains(fmt.Sprint(policy.Data), "observed") {
		t.Fatalf("served Calico identity lost: %+v", policy)
	}
}

func TestFluxSharedSourceIsDependencyForEachConsumer(t *testing.T) {
	repo := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository", "metadata": map[string]any{"namespace": "shared", "name": "repo"}, "spec": map[string]any{"url": "https://example.org/repo"}}}
	rr := ResourceRef{Group: "source.toolkit.fluxcd.io", Kind: "GitRepository", Namespace: "shared", Name: "repo"}
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(rr): repo}}
	for _, name := range []string{"one", "two"} {
		kr := ResourceRef{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "team", Name: name}
		getter.objects[refKey(kr)] = &unstructured.Unstructured{Object: map[string]any{"apiVersion": kr.Group + "/v1", "kind": kr.Kind, "metadata": map[string]any{"namespace": kr.Namespace, "name": name}, "spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "namespace": "shared", "name": "repo"}}}}
		tree, _, err := NewBuilder(getter, nil).Build(context.Background(), kr.Kind, kr.Namespace, name, kr.Group)
		if err != nil {
			t.Fatal(err)
		}
		source := findNode(t, tree, rr)
		if !source.Dependency || source.Data["sourceURL"] != "https://example.org/repo" || tree.Summary.Dependencies != 1 || tree.Summary.Declared != 0 || len(tree.Edges) != 1 || tree.Edges[0].Type != EdgeSource {
			t.Fatalf("shared source membership: %+v", tree)
		}
	}
	if len(getter.recordedCalls()) != 4 {
		t.Fatalf("source enrichment added reads: %v", getter.recordedCalls())
	}
}

func TestRemoteFluxInventoryOverlapDoesNotEnrichFromLocalSource(t *testing.T) {
	for _, withTopology := range []bool{false, true} {
		root := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": map[string]any{"namespace": "flux-system", "name": "fleet"}, "spec": map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "spoke"}}, "sourceRef": map[string]any{"kind": "GitRepository", "namespace": "prod", "name": "flux-system"}}, "status": map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "prod_flux-system_source.toolkit.fluxcd.io_GitRepository", "v": "v1"}}}}}}
		rr := ResourceRef{Group: "source.toolkit.fluxcd.io", Kind: "GitRepository", Namespace: "prod", Name: "flux-system"}
		repo := &unstructured.Unstructured{Object: map[string]any{"apiVersion": rr.Group + "/v1", "kind": rr.Kind, "metadata": map[string]any{"namespace": rr.Namespace, "name": rr.Name, "uid": "local-uid", "labels": map[string]any{"private": "local"}}, "spec": map[string]any{"url": "https://example.org/local"}, "status": map[string]any{"artifact": map[string]any{"revision": "local"}, "conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "FetchFailed", "message": "local failure"}}}}}
		getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(rr): repo, refKey(ResourceRef{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "flux-system", Name: "fleet"}): root}}
		var topo *topology.Topology
		if withTopology {
			topo = &topology.Topology{Nodes: []topology.Node{{ID: "local-source", Kind: topology.KindGitRepository, Name: rr.Name, Data: map[string]any{"namespace": rr.Namespace, "apiVersion": repo.GetAPIVersion(), "labels": map[string]string{"private": "local"}}}}}
		}
		tree, _, err := NewBuilder(getter, topo).Build(context.Background(), root.GetKind(), root.GetNamespace(), root.GetName(), "kustomize.toolkit.fluxcd.io")
		if err != nil {
			t.Fatal(err)
		}
		node := findNode(t, tree, rr)
		if !node.Remote || node.Dependency || node.Ref.UID != "" || node.Health != "" || node.Data["artifact"] != nil || node.Data["sourceURL"] != nil || node.Data["labels"] != nil || node.Data["relationship"] != "source" || len(tree.Edges) != 1 || tree.Edges[0].Type != EdgeOwns {
			t.Fatalf("topology=%t remote resource got local facts: %+v", withTopology, tree)
		}
	}
}

func TestHelmReleaseRootKeepsInstalledChartOverTopologyRevision(t *testing.T) {
	hr := helmRelease("team", "app")
	hr.Object["status"] = map[string]any{"lastAttemptedRevision": "6.15.0", "history": []any{map[string]any{"chartVersion": "6.15.0", "status": "deployed"}}}
	topo := &topology.Topology{Nodes: []topology.Node{{ID: "helmrelease/team/app", Kind: "HelmRelease", Name: "app", Data: map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "namespace": "team", "revision": 0}}}}
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "team", Name: "app"}): hr}}
	tree, _, err := NewBuilder(getter, topo).Build(context.Background(), "HelmRelease", "team", "app", "helm.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	if tree.Root.Data["revision"] != "6.15.0" {
		t.Fatalf("root revision = %#v", tree.Root.Data["revision"])
	}
}
