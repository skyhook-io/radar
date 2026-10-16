package topology

import (
	"fmt"
	"testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	"strings"
)

type unavailablePodInventory struct{ mockProvider }

func (*unavailablePodInventory) InventoryError(resource, _ string) error {
	if resource == "pods" {
		return &InventoryError{Resource: resource, State: InventoryUnavailable}
	}
	return nil
}

func (*unavailablePodInventory) Pods() ([]*corev1.Pod, error) {
	return nil, fmt.Errorf("pods not available (RBAC not granted)")
}

func TestUnavailableInventoryDoesNotDuplicateRBACWarning(t *testing.T) {
	graph, err := NewBuilder(&unavailablePodInventory{}).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.IncompleteInventories) != 1 || graph.IncompleteInventories[0].State != InventoryUnavailable {
		t.Fatalf("missing structured unavailable evidence: %+v", graph.IncompleteInventories)
	}
	if len(graph.Warnings) != 1 || !strings.Contains(graph.Warnings[0], "RBAC not granted") {
		t.Fatalf("duplicated or relabeled RBAC warning: %v", graph.Warnings)
	}
}

type inventoryNamespaceReadCounter struct {
	mockProvider
	namespaceReads int
}

func (p *inventoryNamespaceReadCounter) Namespaces() ([]*corev1.Namespace, error) {
	p.namespaceReads++
	return nil, fmt.Errorf("namespaces not available (RBAC not granted)")
}

func TestAbsentCalicoDoesNotReadNamespaceLabels(t *testing.T) {
	provider := &inventoryNamespaceReadCounter{}
	dynamic := &genericIdentityDynamic{}
	graph, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	if provider.namespaceReads != 0 || len(graph.IncompleteInventories) != 0 {
		t.Fatalf("absent integration requested namespace evidence: reads=%d inventories=%+v", provider.namespaceReads, graph.IncompleteInventories)
	}
}

type inventoryDiscoveryDynamic struct {
	*genericIdentityDynamic
	discovery *k8score.ResourceDiscovery
}

func (p *inventoryDiscoveryDynamic) IsCRD(kind string) bool {
	return p.discovery.IsCRD(kind)
}

func (p *inventoryDiscoveryDynamic) IsCRDGVR(gvr schema.GroupVersionResource) bool {
	return p.discovery.IsCRDGVR(gvr)
}

func TestInventoryWrapperPreservesExactCRDDiscovery(t *testing.T) {
	for _, tc := range []struct {
		kind, builtinGroup, group, resource string
	}{
		{"Job", "batch", "batch.volcano.sh", "jobs"},
		{"Service", "", "platform.example.io", "services"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			discovery, err := k8score.NewResourceDiscovery(fakeclientset.NewClientset().Discovery())
			if err != nil {
				t.Fatal(err)
			}
			gvr := schema.GroupVersionResource{Group: tc.group, Version: "v1", Resource: tc.resource}
			discovery.AddAPIResource(k8score.APIResource{Group: tc.builtinGroup, Version: "v1", Kind: tc.kind, Name: tc.resource, Namespaced: true})
			discovery.AddAPIResource(k8score.APIResource{Group: gvr.Group, Version: gvr.Version, Kind: tc.kind, Name: gvr.Resource, Namespaced: true, IsCRD: true})
			if discovery.IsCRD(tc.kind) || !discovery.IsCRDGVR(gvr) {
				t.Fatal("fixture must distinguish the bare built-in kind from the exact CRD")
			}
			owner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "parent"}
			dynamic := &inventoryDiscoveryDynamic{genericIdentityDynamic: &genericIdentityDynamic{
				watched:   []schema.GroupVersionResource{gvr},
				kinds:     map[schema.GroupVersionResource]string{gvr: tc.kind},
				resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {genericIdentityObject(gvr, tc.kind, "team", "child", owner)}},
				listCalls: map[schema.GroupVersionResource]int{},
			}, discovery: discovery}
			provider := &mockProvider{deployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: "team"}}}}
			graph, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
			if err != nil {
				t.Fatal(err)
			}
			id := strings.ToLower(tc.kind) + "/team/child/" + tc.group
			if nodeByID(graph.Nodes, id) == nil {
				t.Fatalf("cached CRD %s disappeared behind the inventory wrapper", id)
			}
			for _, edge := range graph.Edges {
				if edge.Source == "deployment/team/parent" && edge.Target == id && edge.Type == EdgeManages {
					return
				}
			}
			t.Fatalf("cached CRD %s lost its owner relationship", id)
		})
	}
}

func TestInventoryWrapperPreservesBareCRDFallback(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	source := &genericIdentityDynamic{kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}}
	wrapped := inventoryDynamicProvider{DynamicProvider: &dynamicProviderWithoutExactCRD{DynamicProvider: source}}
	if !isCRDGVR(wrapped, gvr, "Widget") {
		t.Fatal("provider without exact discovery lost its supported bare-kind lookup")
	}
}

func TestDynamicInventoryWarningQualifiesCollidingKind(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "crd.projectcalico.org", Version: "v1", Resource: "networkpolicies"}
	tracker := &topologyInventoryTracker{}
	wrapped := inventoryDynamicProvider{DynamicProvider: &genericIdentityDynamic{kinds: map[schema.GroupVersionResource]string{gvr: "NetworkPolicy"}}, tracker: tracker}
	wrapped.record(gvr, fmt.Errorf("permission denied"))
	for _, warning := range tracker.warnings {
		if warning.Group != gvr.Group || !strings.Contains(warning.Message, "NetworkPolicy.crd.projectcalico.org") {
			t.Fatalf("custom policy failure looked like a built-in: %+v", warning)
		}
	}
	if len(tracker.warnings) != 1 {
		t.Fatalf("expected one qualified warning: %+v", tracker.warnings)
	}
}

func TestCertificateRelationshipsQualifySecretGapsWithoutUnrelatedFailures(t *testing.T) {
	cert := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "Certificate", "metadata": map[string]any{"name": "tls", "namespace": "team"}, "spec": map[string]any{"secretName": "tls-secret"}}}
	secret := InventoryWarningFor("secrets", &InventoryError{Resource: "secrets", State: InventoryPending})
	graph := &Topology{IncompleteInventories: []InventoryWarning{
		secret,
		InventoryWarningFor("pods", &InventoryError{Resource: "pods", State: InventoryPending}),
		{Resource: "nodepools", Group: "karpenter.sh", State: InventoryUnavailable, Message: "NodePool.karpenter.sh data could not be read"},
	}}
	rel := GetRelationshipsWithObject("Certificate", "team", "tls", cert, graph, &stubProvider{}, nil, nil)
	if rel == nil || len(rel.IncompleteInventories) != 1 || rel.IncompleteInventories[0].Resource != "secrets" {
		t.Fatalf("Certificate omitted its Secret gap or inherited unrelated sources: %+v", rel)
	}
	graph.IncompleteInventories = []InventoryWarning{InventoryWarningFor("secrets", &InventoryError{Resource: "secrets", State: InventoryUncovered})}
	if warnings := relevantInventoryWarnings(graph, "Certificate", "cert-manager.io", "team", cert, &partialRelationshipInventory{}); len(warnings) != 0 {
		t.Fatalf("covered Certificate inherited unrelated namespace gaps: %+v", warnings)
	}
}

type warmingGraphProvider struct {
	stubProvider
	state InventoryState
}

func (p *warmingGraphProvider) InventoryError(resource, namespace string) error {
	if resource == "pods" && p.state != "" {
		return &InventoryError{Resource: resource, State: p.state}
	}
	return nil
}
func (p *warmingGraphProvider) Pods() ([]*corev1.Pod, error) {
	return p.pods, p.InventoryError("pods", "")
}
func TestTopologyPendingInventoryPreservesFirstPaint(t *testing.T) {
	provider := &warmingGraphProvider{state: InventoryPending, stubProvider: stubProvider{
		pods:     []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "visible", Namespace: "team", Labels: map[string]string{"app": "waiting"}}}},
		services: []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "front", Namespace: "team"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "waiting"}}}},
	}}
	for _, view := range []ViewMode{ViewModeResources, ViewModeTraffic} {
		opts := DefaultBuildOptions()
		opts.ViewMode = view
		graph, err := NewBuilder(provider).Build(opts)
		if err != nil {
			t.Fatal(err)
		}
		visible := false
		for _, node := range graph.Nodes {
			if node.Kind == KindPod || node.Kind == KindPodGroup {
				visible = true
			}
		}
		if !visible {
			t.Fatalf("%s dropped observed Pods during first paint", view)
		}
		if len(graph.IncompleteInventories) != 1 || graph.IncompleteInventories[0].State != InventoryPending {
			t.Fatalf("inventory state lost: %+v", graph.IncompleteInventories)
		}
		if len(graph.Warnings) != 1 || !strings.HasPrefix(graph.Warnings[0], "Topology view:") {
			t.Fatalf("loading was presented as a failed inventory: %v", graph.Warnings)
		}
		for _, warning := range graph.Warnings {
			if strings.Contains(warning, "Failed to list Pods") {
				t.Fatalf("loading classified as failure: %s", warning)
			}
		}
		rel := GetRelationshipsWithObject("ConfigMap", "team", "settings", &corev1.ConfigMap{}, graph, provider, nil, nil)
		if rel == nil || len(rel.Warnings) == 0 {
			t.Fatal("graph-derived relationships claimed complete absence")
		}
		if !strings.Contains(rel.Warnings[0], "still loading") {
			t.Fatalf("loading copy: %v", rel.Warnings)
		}
		provider.state = ""
		if !InventoryWarningsChanged(graph, provider) {
			t.Fatal("empty inventory becoming ready could leave a stale loading warning")
		}
		provider.state = InventoryPending
	}
}
func TestInventoryWarningsDistinguishLoadingAndPartialData(t *testing.T) {
	pending := InventoryWarningFor("pods", &InventoryError{Resource: "pods", State: InventoryPending})
	partial := InventoryWarningFor("pods", &InventoryError{Resource: "pods", State: InventoryUncovered})
	if pending.Message != "Pod data is still loading" || !strings.Contains(partial.Message, "may be incomplete") {
		t.Fatalf("state-specific copy: %+v %+v", pending, partial)
	}
}

type unavailableCertificateInventory struct{ stubProvider }

func (*unavailableCertificateInventory) Secrets() ([]*corev1.Secret, error) {
	return nil, &InventoryError{Resource: "secrets", State: InventoryPending}
}
func TestCertificateExpiryDoesNotHideInventoryFailure(t *testing.T) {
	provider := &unavailableCertificateInventory{}
	if data, err := GetCertificateExpiry(provider, nil); data != nil || err == nil {
		t.Fatalf("failed read claimed empty expiry: %+v %v", data, err)
	}
	if data, err := GetCertificateExpiry(provider, []string{}); len(data) != 0 || err != nil {
		t.Fatalf("denied namespace set attempted inventory: %+v %v", data, err)
	}
}

func TestRelationshipInventoriesOnlyDescribeRelevantSources(t *testing.T) {
	secret := InventoryWarningFor("secrets", &InventoryError{Resource: "secrets", State: InventoryUnavailable})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "team"}}
	graph := &Topology{IncompleteInventories: []InventoryWarning{secret}}
	rel := GetRelationshipsWithObject("Pod", "team", "plain", pod, graph, &stubProvider{}, nil, nil)
	if rel != nil && len(rel.Warnings) > 0 {
		t.Fatalf("unreferenced disabled Secret inventory tainted Pod: %+v", rel)
	}
	pod.Spec.Containers = []corev1.Container{{EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "credentials"}}}}}}
	rel = GetRelationshipsWithObject("Pod", "team", "plain", pod, graph, &stubProvider{}, nil, nil)
	if rel != nil && len(rel.IncompleteInventories) != 0 {
		t.Fatalf("Pod declaration does not require Secret graph inventory: %+v", rel)
	}
	graph.IncompleteInventories = []InventoryWarning{InventoryWarningFor("pods", &InventoryError{Resource: "pods", State: InventoryUncovered})}
	rel = GetRelationshipsWithObject("ConfigMap", "team", "settings", &corev1.ConfigMap{}, graph, &partialRelationshipInventory{}, nil, nil)
	if rel != nil && len(rel.Warnings) > 0 {
		t.Fatalf("covered namespace inherited global scope warning: %+v", rel)
	}
	application := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application"}}
	if warnings := relevantInventoryWarnings(graph, "Application", "argoproj.io", "argocd", application, &partialRelationshipInventory{}); len(warnings) != 1 {
		t.Fatalf("watched controller root hid cross-namespace inventory gaps: %+v", warnings)
	}
	graph.IncompleteInventories = []InventoryWarning{InventoryWarningFor("pods", &InventoryError{Resource: "pods", State: InventoryPending})}
	rel = GetRelationshipsWithObject("ConfigMap", "team", "settings", &corev1.ConfigMap{}, graph, &stubProvider{}, nil, nil)
	if rel == nil || len(rel.Warnings) != 1 {
		t.Fatalf("missing consumer looked complete: %+v", rel)
	}
}

type scopedCertificateInventory struct{ mockProvider }

func (*scopedCertificateInventory) InventoryError(resource, namespace string) error {
	if namespace == "covered" {
		return nil
	}
	return &InventoryError{Resource: resource, State: InventoryUncovered, Namespace: namespace}
}
func TestCertificateExpiryPreservesVisibleSubset(t *testing.T) {
	provider := &scopedCertificateInventory{mockProvider{secrets: []*corev1.Secret{genTLSSecret(t, "covered", "cert", 30)}}}
	if data, err := GetCertificateExpiry(provider, []string{"unwatched"}); data != nil || err == nil {
		t.Fatalf("entirely unwatched certificates claimed absent: %v %v", data, err)
	}
	for _, namespaces := range [][]string{nil, {"covered"}, {"covered", "unwatched"}} {
		data, err := GetCertificateExpiry(provider, namespaces)
		if err != nil || len(data) != 1 {
			t.Fatalf("visible certificate discarded for %v: %v %v", namespaces, data, err)
		}
	}
}

func TestDisabledCertificateInventoryRetainsEmptyAnnotationMap(t *testing.T) {
	provider := &disabledCertificateInventory{}
	data, err := GetCertificateExpiry(provider, nil)
	if err != nil || len(data) != 0 {
		t.Fatalf("disabled optional certificate annotations: %v %v", data, err)
	}
}

type disabledCertificateInventory struct {
	unavailableCertificateInventory
}

func (*disabledCertificateInventory) InventoryError(resource, namespace string) error {
	return &InventoryError{Resource: resource, State: InventoryUnavailable}
}

func TestServiceAndCustomSubjectsRetainSupportingWarnings(t *testing.T) {
	graph := &Topology{IncompleteInventories: []InventoryWarning{InventoryWarningFor("deployments", &InventoryError{Resource: "deployments", State: InventoryPending})}}
	for _, subject := range []struct{ kind, group string }{{"Service", ""}, {"Rollout", "argoproj.io"}} {
		warnings := relevantInventoryWarnings(graph, subject.kind, subject.group, "team", nil, &stubProvider{})
		if len(warnings) != 1 {
			t.Fatalf("%s lost missing workload evidence: %+v", subject.kind, warnings)
		}
	}
	for _, resource := range []string{"secrets", "configmaps", "nodes"} {
		graph.IncompleteInventories = []InventoryWarning{InventoryWarningFor(resource, &InventoryError{Resource: resource, State: InventoryPending})}
		if warnings := relevantInventoryWarnings(graph, "Widget", "example.io", "team", nil, &stubProvider{}); len(warnings) != 1 {
			t.Fatalf("generic ownership/reference source %s lost qualification: %+v", resource, warnings)
		}
	}
}

func TestCustomWorkloadReferencesRetainSecretWarning(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"envFrom": []any{map[string]any{"secretRef": map[string]any{"name": "credentials"}}}}}}}}}}
	graph := &Topology{IncompleteInventories: []InventoryWarning{InventoryWarningFor("secrets", &InventoryError{Resource: "secrets", State: InventoryPending})}}
	if warnings := relevantInventoryWarnings(graph, "Rollout", "argoproj.io", "team", object, &stubProvider{}); len(warnings) != 1 {
		t.Fatalf("tracked Rollout Secret inventory lost: %v", warnings)
	}
}

func TestPodAncestryWarningsFollowItsOwner(t *testing.T) {
	graph := &Topology{IncompleteInventories: []InventoryWarning{InventoryWarningFor("deployments", &InventoryError{Resource: "deployments", State: InventoryUnavailable})}}
	pod := &corev1.Pod{}
	if warnings := relevantInventoryWarnings(graph, "Pod", "", "team", pod, &stubProvider{}); len(warnings) != 0 {
		t.Fatalf("unowned Pod depends on Deployment inventory: %v", warnings)
	}
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "task"}}
	if warnings := relevantInventoryWarnings(graph, "Pod", "", "team", pod, &stubProvider{}); len(warnings) != 0 {
		t.Fatalf("Job Pod depends on Deployment inventory: %v", warnings)
	}
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "app"}}
	if warnings := relevantInventoryWarnings(graph, "Pod", "", "team", pod, &stubProvider{}); len(warnings) != 1 {
		t.Fatalf("Deployment ancestry looked complete: %v", warnings)
	}
}
