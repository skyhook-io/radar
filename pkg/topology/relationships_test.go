package topology

import (
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
)

// stubProvider supplies a typed K8s resource for the kinds GetRelationships
// inspects for Pod hygiene fields and ManagedBy synthesis. Other methods
// return empty slices so calls fall through cleanly.
type stubProvider struct {
	pods     []*corev1.Pod
	services []*corev1.Service
	pdbs     []*policyv1.PodDisruptionBudget
}

func (s *stubProvider) Pods() ([]*corev1.Pod, error)         { return s.pods, nil }
func (s *stubProvider) Services() ([]*corev1.Service, error) { return s.services, nil }
func (s *stubProvider) Deployments() ([]*appsv1.Deployment, error) {
	return nil, nil
}
func (s *stubProvider) DaemonSets() ([]*appsv1.DaemonSet, error)     { return nil, nil }
func (s *stubProvider) StatefulSets() ([]*appsv1.StatefulSet, error) { return nil, nil }
func (s *stubProvider) ReplicaSets() ([]*appsv1.ReplicaSet, error)   { return nil, nil }
func (s *stubProvider) Jobs() ([]*batchv1.Job, error)                { return nil, nil }
func (s *stubProvider) CronJobs() ([]*batchv1.CronJob, error)        { return nil, nil }
func (s *stubProvider) Ingresses() ([]*networkingv1.Ingress, error)  { return nil, nil }
func (s *stubProvider) ConfigMaps() ([]*corev1.ConfigMap, error)     { return nil, nil }
func (s *stubProvider) Secrets() ([]*corev1.Secret, error)           { return nil, nil }
func (s *stubProvider) PersistentVolumeClaims() ([]*corev1.PersistentVolumeClaim, error) {
	return nil, nil
}
func (s *stubProvider) PersistentVolumes() ([]*corev1.PersistentVolume, error) { return nil, nil }
func (s *stubProvider) HorizontalPodAutoscalers() ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
	return nil, nil
}
func (s *stubProvider) PodDisruptionBudgets() ([]*policyv1.PodDisruptionBudget, error) {
	return s.pdbs, nil
}
func (s *stubProvider) NetworkPolicies() ([]*networkingv1.NetworkPolicy, error) { return nil, nil }
func (s *stubProvider) Nodes() ([]*corev1.Node, error)                          { return nil, nil }
func (s *stubProvider) GetResourceStatus(kind, namespace, name string) *ResourceStatus {
	return nil
}

func TestGetCascadeDeletePreview_GroupQualifiedCollisions(t *testing.T) {
	capiGVR := schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta1", Resource: "clusters"}
	cnpgGVR := schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
	dp := &stubDP{
		gvrByGroup: map[string]schema.GroupVersionResource{
			"cluster.x-k8s.io/clusters":   capiGVR,
			"postgresql.cnpg.io/clusters": cnpgGVR,
		},
		kindByGVR: map[schema.GroupVersionResource]string{
			capiGVR: "Cluster",
			cnpgGVR: "Cluster",
		},
	}
	topo := &Topology{
		Nodes: []Node{
			{ID: "capicluster/fleet/prod", Kind: KindCAPICluster, Name: "prod", Data: map[string]any{"namespace": "fleet", "apiVersion": "cluster.x-k8s.io/v1beta1"}},
			{ID: "machinedeployment/fleet/capi-workers", Kind: KindMachineDeployment, Name: "capi-workers", Data: map[string]any{"namespace": "fleet", "apiVersion": "cluster.x-k8s.io/v1beta1"}},
			{ID: "machine/fleet/capi-worker-1", Kind: KindMachine, Name: "capi-worker-1", Data: map[string]any{"namespace": "fleet", "apiVersion": "cluster.x-k8s.io/v1beta1"}},
			{ID: "cluster/fleet/prod", Kind: NodeKind("Cluster"), Name: "prod", Data: map[string]any{"namespace": "fleet", "apiVersion": "postgresql.cnpg.io/v1"}},
			{ID: "backup/fleet/cnpg-backup", Kind: NodeKind("Backup"), Name: "cnpg-backup", Data: map[string]any{"namespace": "fleet", "apiVersion": "postgresql.cnpg.io/v1"}},
		},
		Edges: []Edge{
			{Source: "capicluster/fleet/prod", Target: "machinedeployment/fleet/capi-workers", Type: EdgeManages},
			{Source: "machinedeployment/fleet/capi-workers", Target: "machine/fleet/capi-worker-1", Type: EdgeManages},
			{Source: "cluster/fleet/prod", Target: "backup/fleet/cnpg-backup", Type: EdgeManages},
		},
	}

	capi := GetCascadeDeletePreview(ResourceRef{Kind: "clusters", Namespace: "fleet", Name: "prod", Group: "cluster.x-k8s.io"}, topo, dp)
	if !capi.RootResolved {
		t.Fatal("expected CAPI root to resolve")
	}
	if len(capi.Dependents) != 2 || capi.Dependents[0].Name != "capi-workers" || capi.Dependents[1].Name != "capi-worker-1" {
		t.Fatalf("CAPI dependents = %+v, want its MachineDeployment and transitive Machine", capi.Dependents)
	}
	for _, dep := range capi.Dependents {
		if dep.Group != "cluster.x-k8s.io" {
			t.Errorf("CAPI dependent %s group = %q, want cluster.x-k8s.io", dep.Name, dep.Group)
		}
	}

	cnpg := GetCascadeDeletePreview(ResourceRef{Kind: "clusters", Namespace: "fleet", Name: "prod", Group: "postgresql.cnpg.io"}, topo, dp)
	if !cnpg.RootResolved {
		t.Fatal("expected CNPG root to resolve")
	}
	if len(cnpg.Dependents) != 1 || cnpg.Dependents[0].Name != "cnpg-backup" {
		t.Fatalf("CNPG dependents = %+v, want only cnpg-backup", cnpg.Dependents)
	}
	if cnpg.Dependents[0].Group != "postgresql.cnpg.io" {
		t.Errorf("CNPG dependent group = %q, want postgresql.cnpg.io", cnpg.Dependents[0].Group)
	}

	dp.gvr = map[string]schema.GroupVersionResource{"clusters": cnpgGVR}
	unqualified := GetCascadeDeletePreview(ResourceRef{Kind: "clusters", Namespace: "fleet", Name: "prod"}, topo, dp)
	if unqualified.RootResolved {
		t.Fatalf("unqualified collided root = %+v, want unresolved rather than an arbitrary group", unqualified)
	}
}

func TestGetCascadeDeletePreview_ResolutionState(t *testing.T) {
	topo := &Topology{Nodes: []Node{{ID: "pod/demo/leaf", Kind: KindPod, Name: "leaf", Data: map[string]any{"namespace": "demo"}}}}

	leaf := GetCascadeDeletePreview(ResourceRef{Kind: "pods", Namespace: "demo", Name: "leaf"}, topo, nil)
	if !leaf.RootResolved || len(leaf.Dependents) != 0 {
		t.Fatalf("resolved leaf preview = %+v, want resolved with zero dependents", leaf)
	}

	absent := GetCascadeDeletePreview(ResourceRef{Kind: "pods", Namespace: "demo", Name: "missing"}, topo, nil)
	if absent.RootResolved || len(absent.Dependents) != 0 {
		t.Fatalf("absent preview = %+v, want unresolved with zero dependents", absent)
	}

	discoveryMiss := GetCascadeDeletePreview(ResourceRef{Kind: "clusters", Namespace: "fleet", Name: "prod", Group: "cluster.x-k8s.io"}, topo, &stubDP{})
	if discoveryMiss.RootResolved {
		t.Fatalf("discovery miss preview = %+v, want unresolved without group-blind fallback", discoveryMiss)
	}
}

func TestGetCascadeDeletePreview_RouteCollisionUsesGroup(t *testing.T) {
	knativeGVR := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "routes"}
	openshiftGVR := schema.GroupVersionResource{Group: "route.openshift.io", Version: "v1", Resource: "routes"}
	dp := &stubDP{
		gvrByGroup: map[string]schema.GroupVersionResource{
			"serving.knative.dev/routes": knativeGVR,
			"route.openshift.io/routes":  openshiftGVR,
		},
		kindByGVR: map[schema.GroupVersionResource]string{knativeGVR: "Route", openshiftGVR: "Route"},
	}
	topo := &Topology{
		Nodes: []Node{
			{ID: "knativeroute/demo/shop", Kind: KindKnativeRoute, Name: "shop", Data: map[string]any{"namespace": "demo", "apiVersion": "serving.knative.dev/v1"}},
			{ID: "revision/demo/shop-v1", Kind: KindKnativeRevision, Name: "shop-v1", Data: map[string]any{"namespace": "demo", "apiVersion": "serving.knative.dev/v1"}},
			{ID: "route/demo/shop", Kind: NodeKind("Route"), Name: "shop", Data: map[string]any{"namespace": "demo", "apiVersion": "route.openshift.io/v1"}},
			{ID: "service/demo/shop", Kind: KindService, Name: "shop", Data: map[string]any{"namespace": "demo"}},
		},
		Edges: []Edge{
			{Source: "knativeroute/demo/shop", Target: "revision/demo/shop-v1", Type: EdgeManages},
			{Source: "route/demo/shop", Target: "service/demo/shop", Type: EdgeManages},
		},
	}

	knative := GetCascadeDeletePreview(ResourceRef{Kind: "routes", Namespace: "demo", Name: "shop", Group: "serving.knative.dev"}, topo, dp)
	if !knative.RootResolved || len(knative.Dependents) != 1 || knative.Dependents[0].Name != "shop-v1" {
		t.Fatalf("Knative preview = %+v, want only shop-v1", knative)
	}
	openshift := GetCascadeDeletePreview(ResourceRef{Kind: "routes", Namespace: "demo", Name: "shop", Group: "route.openshift.io"}, topo, dp)
	if !openshift.RootResolved || len(openshift.Dependents) != 1 || openshift.Dependents[0].Kind != "Service" {
		t.Fatalf("OpenShift preview = %+v, want only Service/shop", openshift)
	}
}

func TestGetCascadeDeletePreview_CalicoGroupCollisionUsesQualifiedRoot(t *testing.T) {
	projectGVR := schema.GroupVersionResource{Group: "projectcalico.org", Version: "v3", Resource: "networkpolicies"}
	legacyGVR := schema.GroupVersionResource{Group: "crd.projectcalico.org", Version: "v1", Resource: "networkpolicies"}
	dp := &stubDP{
		gvrByGroup: map[string]schema.GroupVersionResource{
			"projectcalico.org/networkpolicies":     projectGVR,
			"projectcalico.org/networkpolicy":       projectGVR,
			"crd.projectcalico.org/networkpolicies": legacyGVR,
			"crd.projectcalico.org/networkpolicy":   legacyGVR,
		},
		kindByGVR: map[schema.GroupVersionResource]string{
			projectGVR: "NetworkPolicy",
			legacyGVR:  "NetworkPolicy",
		},
	}
	projectID := "caliconetworkpolicy/demo/shared/projectcalico.org"
	legacyID := "caliconetworkpolicy/demo/shared/crd.projectcalico.org"
	topo := &Topology{
		Nodes: []Node{
			{ID: projectID, Kind: KindCalicoNetworkPolicy, Name: "shared", Data: map[string]any{"namespace": "demo", "apiVersion": "projectcalico.org/v3"}},
			{ID: legacyID, Kind: KindCalicoNetworkPolicy, Name: "shared", Data: map[string]any{"namespace": "demo", "apiVersion": "crd.projectcalico.org/v1"}},
			{ID: "deployment/demo/project", Kind: KindDeployment, Name: "project", Data: map[string]any{"namespace": "demo"}},
			{ID: "deployment/demo/legacy", Kind: KindDeployment, Name: "legacy", Data: map[string]any{"namespace": "demo"}},
		},
		Edges: []Edge{
			{Source: projectID, Target: "deployment/demo/project", Type: EdgeManages},
			{Source: legacyID, Target: "deployment/demo/legacy", Type: EdgeManages},
		},
	}

	for _, test := range []struct {
		group, wantDependent string
	}{
		{"projectcalico.org", "project"},
		{"crd.projectcalico.org", "legacy"},
	} {
		t.Run(test.group, func(t *testing.T) {
			preview := GetCascadeDeletePreview(ResourceRef{
				Kind: "networkpolicies", Namespace: "demo", Name: "shared", Group: test.group,
			}, topo, dp)
			if !preview.RootResolved || len(preview.Dependents) != 1 || preview.Dependents[0].Name != test.wantDependent {
				t.Fatalf("%s preview = %+v, want only %s dependent", test.group, preview, test.wantDependent)
			}
		})
	}
}

// TestGetRelationships_PodHygieneFields covers T2: pods carry
// ServiceAccount, Node, and ManagedBy refs derived from spec + labels.
func TestGetRelationships_PodHygieneFields(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "demo",
			Name:      "web-abc-xyz",
			Annotations: map[string]string{
				argoTrackingIDAnnotation: "argocd_guestbook:apps/Deployment:demo/web",
			},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: "web-sa",
			NodeName:           "node-1",
		},
	}
	provider := &stubProvider{pods: []*corev1.Pod{pod}}

	topo := &Topology{
		Nodes: []Node{{ID: "pod/demo/web-abc-xyz", Kind: KindPod, Name: "web-abc-xyz"}},
		Edges: []Edge{},
	}

	rel := GetRelationships("Pod", "demo", "web-abc-xyz", topo, provider, nil)
	if rel == nil {
		t.Fatal("expected non-nil Relationships for pod with hygiene fields")
	}
	if rel.ServiceAccount == nil || rel.ServiceAccount.Kind != "ServiceAccount" || rel.ServiceAccount.Name != "web-sa" || rel.ServiceAccount.Namespace != "demo" {
		t.Errorf("ServiceAccount: want {Kind:ServiceAccount NS:demo Name:web-sa}, got %+v", rel.ServiceAccount)
	}
	if rel.Node == nil || rel.Node.Kind != "Node" || rel.Node.Name != "node-1" {
		t.Errorf("Node: want {Kind:Node Name:node-1}, got %+v", rel.Node)
	}
	if len(rel.ManagedBy) != 1 || rel.ManagedBy[0].Kind != "Application" || rel.ManagedBy[0].Name != "guestbook" {
		t.Errorf("ManagedBy: want [{Application/argocd/guestbook}], got %+v", rel.ManagedBy)
	}
}

// TestGetRelationships_PodHygieneFields_EmptySAandUnscheduled verifies that
// optional fields are properly omitted when the source data is empty. The
// nil-result short-circuit must also still kick in.
func TestGetRelationships_PodHygieneFields_EmptySAandUnscheduled(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: "lone"},
		Spec:       corev1.PodSpec{ /* SA empty, NodeName empty */ },
	}
	provider := &stubProvider{pods: []*corev1.Pod{pod}}
	topo := &Topology{
		Nodes: []Node{{ID: "pod/demo/lone", Kind: KindPod, Name: "lone"}},
	}

	rel := GetRelationships("Pod", "demo", "lone", topo, provider, nil)
	if rel != nil {
		t.Errorf("expected nil for pod with no edges and no hygiene data, got %+v", rel)
	}
}

func TestGetRelationships_CalicoHostEndpointNode(t *testing.T) {
	tests := []struct {
		name       string
		apiVersion string
		node       string
		wantNode   bool
	}{
		{name: "Calico CRD group", apiVersion: "crd.projectcalico.org/v1", node: "worker-1", wantNode: true},
		{name: "Calico API group", apiVersion: "projectcalico.org/v3", node: "worker-1", wantNode: true},
		{name: "foreign group", apiVersion: "networking.example.io/v1", node: "worker-1"},
		{name: "near-match group", apiVersion: "extension.projectcalico.org/v1", node: "worker-1"},
		{name: "missing node", apiVersion: "projectcalico.org/v3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := map[string]any{}
			if tt.node != "" {
				spec["node"] = tt.node
			}
			endpoint := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": tt.apiVersion,
				"kind":       "HostEndpoint",
				"metadata":   map[string]any{"name": "infra-1"},
				"spec":       spec,
			}}

			rel := GetRelationshipsWithObject("hostendpoints", "", "infra-1", endpoint, &Topology{}, nil, nil, nil)
			if !tt.wantNode {
				if rel != nil {
					t.Fatalf("relationships = %+v, want nil", rel)
				}
				return
			}
			if rel == nil || rel.Node == nil {
				t.Fatalf("relationships = %+v, want Node relationship", rel)
			}
			if *rel.Node != (ResourceRef{Kind: "Node", Name: "worker-1"}) {
				t.Errorf("Node = %+v, want core Node/worker-1", rel.Node)
			}
		})
	}
}

func TestGetRelationships_ConfiguresDispatchesByKind(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"},
			{ID: "serviceaccount/demo/web", Kind: KindServiceAccount, Name: "web"},
			{ID: "sealedsecret/demo/web", Kind: KindSealedSecret, Name: "web"},
			{ID: "configmap/demo/web", Kind: KindConfigMap, Name: "web"},
			{ID: "destinationrule/demo/web", Kind: KindDestinationRule, Name: "web"},
			{ID: "podmonitor/demo/web", Kind: KindPodMonitor, Name: "web"},
		},
		Edges: []Edge{
			{ID: "sa-to-web", Source: "serviceaccount/demo/web", Target: "deployment/demo/web", Type: EdgeConfigures},
			{ID: "sealed-to-web", Source: "sealedsecret/demo/web", Target: "deployment/demo/web", Type: EdgeConfigures},
			{ID: "config-to-web", Source: "configmap/demo/web", Target: "deployment/demo/web", Type: EdgeConfigures},
			{ID: "destination-rule-to-web", Source: "destinationrule/demo/web", Target: "deployment/demo/web", Type: EdgeConfigures},
			{ID: "monitor-to-web", Source: "podmonitor/demo/web", Target: "deployment/demo/web", Type: EdgeConfigures},
		},
	}

	rel := GetRelationships("Deployment", "demo", "web", topo, nil, nil)
	if rel == nil {
		t.Fatal("expected relationships")
	}
	if rel.ServiceAccount == nil || rel.ServiceAccount.Name != "web" {
		t.Fatalf("expected workload ServiceAccount, got %+v", rel.ServiceAccount)
	}
	if len(rel.ConfigRefs) != 3 {
		t.Fatalf("expected all non-identity configurers in ConfigRefs, got %+v", rel.ConfigRefs)
	}
	gotConfigKinds := make(map[string]bool, len(rel.ConfigRefs))
	for _, ref := range rel.ConfigRefs {
		gotConfigKinds[ref.Kind] = true
	}
	for _, kind := range []string{"SealedSecret", "ConfigMap", "DestinationRule"} {
		if !gotConfigKinds[kind] {
			t.Errorf("ConfigRefs missing %s; got kinds=%v", kind, gotConfigKinds)
		}
	}
	if gotConfigKinds["PodMonitor"] {
		t.Errorf("PodMonitor observes the workload and must not be projected as configuration")
	}
}

func TestGetRelationships_WorkloadIncludesServiceEntrypoints(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"},
			{ID: "service/demo/web", Kind: KindService, Name: "web"},
			{ID: "ingress/demo/web", Kind: KindIngress, Name: "web"},
			{ID: "httproute/demo/web", Kind: KindHTTPRoute, Name: "web"},
			{ID: "ingressroute/demo/web", Kind: KindIngressRoute, Name: "web"},
		},
		Edges: []Edge{
			{ID: "service-to-workload", Source: "service/demo/web", Target: "deployment/demo/web", Type: EdgeExposes},
			{ID: "ingress-to-service", Source: "ingress/demo/web", Target: "service/demo/web", Type: EdgeRoutesTo},
			{ID: "http-route-to-service", Source: "httproute/demo/web", Target: "service/demo/web", Type: EdgeRoutesTo},
			{ID: "ingress-route-to-service", Source: "ingressroute/demo/web", Target: "service/demo/web", Type: EdgeExposes},
		},
	}

	rel := GetRelationshipsWithIndex("Deployment", "demo", "web", topo, nil, nil, IndexByResource(topo))
	if rel == nil {
		t.Fatal("expected workload relationships")
	}
	if len(rel.Ingresses) != 1 || rel.Ingresses[0].Name != "web" {
		t.Fatalf("Ingresses = %+v, want demo/web", rel.Ingresses)
	}
	if len(rel.Routes) != 2 {
		t.Fatalf("Routes = %+v, want HTTPRoute and IngressRoute", rel.Routes)
	}
	kinds := map[string]bool{}
	for _, ref := range rel.Routes {
		kinds[ref.Kind] = true
	}
	if !kinds["HTTPRoute"] || !kinds["IngressRoute"] {
		t.Fatalf("Routes = %+v, want HTTPRoute and IngressRoute", rel.Routes)
	}

	serviceRel := GetRelationshipsWithIndex("Service", "demo", "web", topo, nil, nil, IndexByResource(topo))
	if serviceRel == nil || len(serviceRel.Routes) != 1 || serviceRel.Routes[0].Kind != "IngressRoute" {
		t.Fatalf("Service Routes = %+v, want IngressRoute", serviceRel)
	}
	if len(serviceRel.Services) != 0 {
		t.Fatalf("Service Services = %+v, route CRDs must not be labeled as Services", serviceRel.Services)
	}
}

func TestGetRelationships_ServiceEntrypointsUseExactAPIGroup(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/core", Kind: KindDeployment, Name: "core", Data: map[string]any{"namespace": "demo", "apiVersion": "apps/v1"}},
			{ID: "deployment/demo/custom", Kind: KindDeployment, Name: "custom", Data: map[string]any{"namespace": "demo", "apiVersion": "apps/v1"}},
			{ID: "service/demo/api", Kind: KindService, Name: "api", Data: map[string]any{"namespace": "demo", "apiVersion": "v1"}},
			{ID: "service/demo/api/platform.example.io", Kind: KindService, Name: "api", Data: map[string]any{"namespace": "demo", "apiVersion": "platform.example.io/v1"}},
			{ID: "ingress/demo/core", Kind: KindIngress, Name: "core", Data: map[string]any{"namespace": "demo", "apiVersion": "networking.k8s.io/v1"}},
			{ID: "route/demo/custom/platform.example.io", Kind: NodeKind("Route"), Name: "custom", Data: map[string]any{"namespace": "demo", "apiVersion": "platform.example.io/v1"}},
		},
		Edges: []Edge{
			{ID: "core-service", Source: "service/demo/api", Target: "deployment/demo/core", Type: EdgeExposes},
			{ID: "custom-service", Source: "service/demo/api/platform.example.io", Target: "deployment/demo/custom", Type: EdgeExposes},
			{ID: "core-entrypoint", Source: "ingress/demo/core", Target: "service/demo/api", Type: EdgeRoutesTo},
			{ID: "custom-entrypoint", Source: "route/demo/custom/platform.example.io", Target: "service/demo/api/platform.example.io", Type: EdgeRoutesTo},
		},
	}
	idx := IndexByResource(topo)

	core := GetRelationshipsWithIndex("Deployment", "demo", "core", topo, nil, nil, idx)
	if core == nil || len(core.Ingresses) != 1 || core.Ingresses[0].Name != "core" || len(core.Routes) != 0 {
		t.Fatalf("core workload borrowed custom Service entrypoints: %+v", core)
	}
	custom := GetRelationshipsWithIndex("Deployment", "demo", "custom", topo, nil, nil, idx)
	if custom == nil || len(custom.Services) != 1 || custom.Services[0].Group != "platform.example.io" || len(custom.Routes) != 0 || len(custom.Ingresses) != 0 {
		t.Fatalf("custom Service kind was given core Service entrypoint semantics: %+v", custom)
	}
}

func TestGetRelationships_ServiceEntrypointsSkipKnativeService(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "ingress/demo/web", Kind: KindIngress, Name: "web", Data: map[string]any{"namespace": "demo"}},
			{ID: "knativeservice/demo/web", Kind: KindKnativeService, Name: "web", Data: map[string]any{"namespace": "demo", "apiVersion": "serving.knative.dev/v1"}},
			{ID: "service/demo/web", Kind: KindService, Name: "web", Data: map[string]any{"namespace": "demo"}},
		},
		Edges: []Edge{
			{ID: "ingress-to-knative", Source: "ingress/demo/web", Target: "knativeservice/demo/web", Type: EdgeRoutesTo},
			{ID: "knative-to-service", Source: "knativeservice/demo/web", Target: "service/demo/web", Type: EdgeExposes},
		},
	}

	rel := GetRelationshipsWithIndex("Service", "demo", "web", topo, nil, nil, IndexByResource(topo))
	if len(rel.Ingresses) != 0 {
		t.Fatalf("core Service inherited an entrypoint through a Knative Service: %+v", rel.Ingresses)
	}
}

func TestGetRelationshipsWithObjectMatchesTypedBuiltinGroup(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "demo"}},
			{ID: "service/demo/web", Kind: KindService, Name: "web", Data: map[string]any{"namespace": "demo"}},
		},
		Edges: []Edge{{Source: "service/demo/web", Target: "deployment/demo/web", Type: EdgeExposes}},
	}
	deployment := &appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: "web"}}

	rel := GetRelationshipsWithObject("deployments", "demo", "web", deployment, topo, nil, nil, IndexByResource(topo))
	if rel == nil || len(rel.Services) != 1 || rel.Services[0].Name != "web" {
		t.Fatalf("typed Deployment lost topology relationships: %+v", rel)
	}
}

func TestGetRelationshipsEmitsKubernetesKindForPseudoNode(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "knativeservice/demo/shop", Kind: KindKnativeService, Name: "shop", Data: map[string]any{"namespace": "demo", "apiVersion": "serving.knative.dev/v1"}},
			{ID: "revision/demo/shop-v1", Kind: KindKnativeRevision, Name: "shop-v1", Data: map[string]any{"namespace": "demo", "apiVersion": "serving.knative.dev/v1"}},
		},
		Edges: []Edge{{Source: "knativeservice/demo/shop", Target: "revision/demo/shop-v1", Type: EdgeManages}},
	}

	rel := GetRelationships("Revision", "demo", "shop-v1", topo, nil, nil)
	if rel == nil || rel.Owner == nil || rel.Owner.Kind != "Service" || rel.Owner.Group != "serving.knative.dev" {
		t.Fatalf("pseudo owner leaked as a non-Kubernetes tuple: %+v", rel)
	}
}

// TestGetRelationships_IncomingEdgeProtects_DispatchesByKind verifies that
// incoming "protects" edges split into rel.PDBs vs rel.NetworkPolicies based
// on the source kind.
//
// (Cluster-scoped NetworkPolicy variants like ClusterNetworkPolicy and
// CiliumClusterwideNetworkPolicy use a 2-segment node ID — parseNodeID
// rejects those today, so they never reach this dispatch. Pre-existing
// behavior, out of scope here.)
func TestGetRelationships_IncomingEdgeProtects_DispatchesByKind(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"},
			{ID: "poddisruptionbudget/demo/web-pdb", Kind: KindPDB, Name: "web-pdb"},
			{ID: "networkpolicy/demo/web-np", Kind: KindNetworkPolicy, Name: "web-np"},
			{ID: "ciliumnetworkpolicy/demo/web-cnp", Kind: KindCiliumNetworkPolicy, Name: "web-cnp"},
		},
		Edges: []Edge{
			{ID: "pdb-to-web", Source: "poddisruptionbudget/demo/web-pdb", Target: "deployment/demo/web", Type: EdgeProtects},
			{ID: "np-to-web", Source: "networkpolicy/demo/web-np", Target: "deployment/demo/web", Type: EdgeProtects},
			{ID: "cnp-to-web", Source: "ciliumnetworkpolicy/demo/web-cnp", Target: "deployment/demo/web", Type: EdgeProtects},
		},
	}

	rel := GetRelationships("Deployment", "demo", "web", topo, nil, nil)
	if rel == nil {
		t.Fatal("GetRelationships returned nil for deployment with 3 incoming protects edges")
	}

	if len(rel.PDBs) != 1 || rel.PDBs[0].Kind != "PodDisruptionBudget" || rel.PDBs[0].Name != "web-pdb" {
		t.Errorf("rel.PDBs: want [PodDisruptionBudget/web-pdb], got %+v", rel.PDBs)
	}

	if len(rel.NetworkPolicies) != 2 {
		t.Fatalf("rel.NetworkPolicies: want 2 entries (NetworkPolicy + CiliumNetworkPolicy), got %d (%+v)", len(rel.NetworkPolicies), rel.NetworkPolicies)
	}
	gotKinds := make(map[string]bool, 2)
	for _, ref := range rel.NetworkPolicies {
		gotKinds[ref.Kind] = true
	}
	for _, expected := range []string{"NetworkPolicy", "CiliumNetworkPolicy"} {
		if !gotKinds[expected] {
			t.Errorf("rel.NetworkPolicies missing %s; got kinds=%v", expected, gotKinds)
		}
	}
}

func TestGetRelationships_IncomingEdgeUsesSplitsScalersFromStorage(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"},
			{ID: "horizontalpodautoscaler/demo/web-hpa", Kind: KindHPA, Name: "web-hpa"},
			{ID: "persistentvolumeclaim/demo/web-data", Kind: KindPVC, Name: "web-data"},
		},
		Edges: []Edge{
			{ID: "hpa-to-web", Source: "horizontalpodautoscaler/demo/web-hpa", Target: "deployment/demo/web", Type: EdgeUses},
			{ID: "pvc-to-web", Source: "persistentvolumeclaim/demo/web-data", Target: "deployment/demo/web", Type: EdgeUses},
		},
	}

	rel := GetRelationships("Deployment", "demo", "web", topo, nil, nil)
	if rel == nil {
		t.Fatal("GetRelationships returned nil for deployment with incoming uses edges")
	}
	if len(rel.Scalers) != 1 || rel.Scalers[0].Kind != "HorizontalPodAutoscaler" || rel.Scalers[0].Name != "web-hpa" {
		t.Errorf("rel.Scalers: want [HorizontalPodAutoscaler/web-hpa], got %+v", rel.Scalers)
	}
	if len(rel.StorageRefs) != 1 || rel.StorageRefs[0].Kind != "PersistentVolumeClaim" || rel.StorageRefs[0].Name != "web-data" {
		t.Errorf("rel.StorageRefs: want [PersistentVolumeClaim/web-data], got %+v", rel.StorageRefs)
	}

	pvcRel := GetRelationships("PersistentVolumeClaim", "demo", "web-data", topo, nil, nil)
	if pvcRel == nil {
		t.Fatal("GetRelationships returned nil for PVC with outgoing uses edge")
	}
	if pvcRel.ScaleTarget != nil {
		t.Errorf("PVC relationship should not expose a scale target, got %+v", pvcRel.ScaleTarget)
	}
	if len(pvcRel.Consumers) != 1 || pvcRel.Consumers[0].Kind != "Deployment" || pvcRel.Consumers[0].Name != "web" {
		t.Errorf("PVC consumers: want [Deployment/web], got %+v", pvcRel.Consumers)
	}
}

// TestGetRelationships_OutgoingEdgeProtects_NotSurfaced verifies that outgoing
// EdgeProtects edges (a PDB / NetworkPolicy / CiliumNetworkPolicy / etc. pointing
// at the workloads it protects) are intentionally NOT projected into the
// Relationships of the source resource. The PDBs / NetworkPolicies fields are
// reserved for the INCOMING-direction semantic ("things that act on me").
//
// Surfacing the outgoing direction requires a new Protects/SelectedWorkloads
// field, which is out of scope here. Until that field lands, querying a PDB
// or NetworkPolicy that has only outgoing protects edges returns nil.
//
// This also guards B1 (the old bug that wrote outgoing protects into
// rel.ScaleTarget) and the post-B1 over-fix (writing them into rel.PDBs,
// which conflated PDB-side and NP-side outgoing edges).
func TestGetRelationships_OutgoingEdgeProtects_NotSurfaced(t *testing.T) {
	cases := []struct {
		name       string
		queryKind  string
		queryName  string // must match the name component of sourceID below
		sourceID   string
		sourceKind NodeKind
	}{
		{"PDB outgoing", "PodDisruptionBudget", "web-pdb", "poddisruptionbudget/demo/web-pdb", KindPDB},
		{"NetworkPolicy outgoing", "NetworkPolicy", "deny-egress", "networkpolicy/demo/deny-egress", KindNetworkPolicy},
		{"CiliumNetworkPolicy outgoing", "CiliumNetworkPolicy", "cnp-1", "ciliumnetworkpolicy/demo/cnp-1", KindCiliumNetworkPolicy},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			topo := &Topology{
				Nodes: []Node{
					{ID: c.sourceID, Kind: c.sourceKind, Name: c.queryName},
					{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"},
					{ID: "deployment/demo/api", Kind: KindDeployment, Name: "api"},
				},
				Edges: []Edge{
					{ID: "src-to-web", Source: c.sourceID, Target: "deployment/demo/web", Type: EdgeProtects},
					{ID: "src-to-api", Source: c.sourceID, Target: "deployment/demo/api", Type: EdgeProtects},
				},
			}

			// Control: the SAME topology, queried from the workload side, MUST
			// surface the policy via incoming-EdgeProtects dispatch. If this
			// fails, the test below would pass for the wrong reason — the
			// edges or node IDs aren't matching at all. Catches the
			// vacuous-pass class of mistakes.
			incoming := GetRelationships("Deployment", "demo", "web", topo, nil, nil)
			if incoming == nil {
				t.Fatalf("control assertion failed: querying the target Deployment should surface the policy via incoming EdgeProtects, got nil relationships")
			}
			switch c.sourceKind {
			case KindPDB:
				if len(incoming.PDBs) == 0 {
					t.Fatalf("control: expected workload to see incoming PDB, got %+v", incoming)
				}
			case KindNetworkPolicy, KindCiliumNetworkPolicy:
				if len(incoming.NetworkPolicies) == 0 {
					t.Fatalf("control: expected workload to see incoming NetworkPolicy, got %+v", incoming)
				}
			}

			// Actual assertion: querying from the source policy side should
			// NOT surface its targets (outgoing direction intentionally
			// unsurfaced until a Protects[] field exists).
			rel := GetRelationships(c.queryKind, "demo", c.queryName, topo, nil, nil)
			if rel != nil {
				t.Errorf("want nil (outgoing protects intentionally not surfaced), got %+v", rel)
			}
		})
	}
}

// TestGetRelationships_NoProtects_FieldsOmitted ensures the new split fields
// stay nil when no protects edges exist, so JSON omitempty keeps the wire
// format identical for unrelated resources.
func TestGetRelationships_NoProtects_FieldsOmitted(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/lone", Kind: KindDeployment, Name: "lone"},
			{ID: "replicaset/demo/lone-abc", Kind: KindReplicaSet, Name: "lone-abc"},
		},
		Edges: []Edge{
			{ID: "lone-rs", Source: "deployment/demo/lone", Target: "replicaset/demo/lone-abc", Type: EdgeManages},
		},
	}

	rel := GetRelationships("Deployment", "demo", "lone", topo, nil, nil)
	if rel == nil {
		t.Fatal("GetRelationships returned nil for deployment with a child")
	}
	if len(rel.PDBs) != 0 {
		t.Errorf("rel.PDBs: want empty, got %+v", rel.PDBs)
	}
	if len(rel.NetworkPolicies) != 0 {
		t.Errorf("rel.NetworkPolicies: want empty, got %+v", rel.NetworkPolicies)
	}
}
