package topology

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	k8score "github.com/skyhook-io/radar/pkg/k8score"
)

type sealedSecretDynamicProvider struct {
	gvr       schema.GroupVersionResource
	resources []*unstructured.Unstructured
}

func (p *sealedSecretDynamicProvider) List(schema.GroupVersionResource, string) ([]*unstructured.Unstructured, error) {
	return nil, nil
}
func (p *sealedSecretDynamicProvider) ListNamespaces(gvr schema.GroupVersionResource, _ []string) ([]*unstructured.Unstructured, error) {
	if gvr == p.gvr {
		return p.resources, nil
	}
	return nil, nil
}
func (p *sealedSecretDynamicProvider) Get(schema.GroupVersionResource, string, string) (*unstructured.Unstructured, error) {
	return nil, nil
}
func (p *sealedSecretDynamicProvider) GetWatchedResources() []schema.GroupVersionResource {
	return []schema.GroupVersionResource{p.gvr}
}
func (p *sealedSecretDynamicProvider) GetDiscoveryStatus() k8score.CRDDiscoveryStatus {
	return k8score.CRDDiscoveryComplete
}
func (p *sealedSecretDynamicProvider) GetGVR(string) (schema.GroupVersionResource, bool) {
	return schema.GroupVersionResource{}, false
}
func (p *sealedSecretDynamicProvider) GetGVRWithGroup(kind, group string) (schema.GroupVersionResource, bool) {
	return p.gvr, kind == "SealedSecret" && group == "bitnami.com"
}
func (p *sealedSecretDynamicProvider) GetKindForGVR(gvr schema.GroupVersionResource) string {
	if gvr == p.gvr {
		return "SealedSecret"
	}
	return ""
}
func (p *sealedSecretDynamicProvider) IsCRD(kind string) bool { return kind == "SealedSecret" }

// deployWithRefs builds a Deployment that references a ConfigMap, Secret, and
// PVC by name via pod-spec volumes (the shapes extractWorkloadReferences reads).
func deployWithRefs(ns, name, cmName, secretName, pvcName string) *appsv1.Deployment {
	vols := []corev1.Volume{}
	if cmName != "" {
		vols = append(vols, corev1.Volume{Name: "cm", VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cmName}}}})
	}
	if secretName != "" {
		vols = append(vols, corev1.Volume{Name: "sec", VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: secretName}}})
	}
	if pvcName != "" {
		vols = append(vols, corev1.Volume{Name: "data", VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName}}})
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Volumes: vols}},
		},
	}
}

func cm(ns, name string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}
func secret(ns, name string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}
func pvc(ns, name string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

func TestWorkloadIdentityEdges(t *testing.T) {
	deployment := deployWithRefs("app", "web", "", "", "")
	deployment.Spec.Template.Spec.ServiceAccountName = "web-identity"
	provider := &mockProvider{
		deployments: []*appsv1.Deployment{deployment},
		serviceAccounts: []*corev1.ServiceAccount{
			{ObjectMeta: metav1.ObjectMeta{Name: "web-identity", Namespace: "app"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "unused", Namespace: "app"}},
		},
	}

	topo, err := NewBuilder(provider).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	assertTopologyEdge(t, topo, "serviceaccount/app/web-identity", "deployment/app/web")
	assertTopologyNodeAbsent(t, topo, "serviceaccount/app/unused")
}

func TestSealedSecretLinksToSecretConsumerWithoutSecretVisibility(t *testing.T) {
	deployment := deployWithRefs("app", "web", "", "generated-secret", "")
	gvr := schema.GroupVersionResource{Group: "bitnami.com", Version: "v1alpha1", Resource: "sealedsecrets"}
	sealedSecret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "bitnami.com/v1alpha1",
		"kind":       "SealedSecret",
		"metadata":   map[string]any{"name": "encrypted", "namespace": "app"},
		"spec":       map[string]any{"template": map[string]any{"metadata": map[string]any{"name": "generated-secret"}}},
	}}

	topo, err := NewBuilder(&mockProvider{deployments: []*appsv1.Deployment{deployment}}).
		WithDynamic(&sealedSecretDynamicProvider{gvr: gvr, resources: []*unstructured.Unstructured{sealedSecret}}).
		Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	assertTopologyEdge(t, topo, "sealedsecret/app/encrypted", "deployment/app/web")
}

func assertTopologyEdge(t *testing.T, topology *Topology, source, target string) {
	t.Helper()
	for _, edge := range topology.Edges {
		if edge.Source == source && edge.Target == target {
			return
		}
	}
	t.Fatalf("expected edge %s -> %s", source, target)
}

func assertTopologyNodeAbsent(t *testing.T, topology *Topology, id string) {
	t.Helper()
	for _, node := range topology.Nodes {
		if node.ID == id {
			t.Fatalf("expected node %s to be absent", id)
		}
	}
}

// The ConfigMap/Secret/PVC→workload matching is inverted into a consumer index.
// This pins the invariants that inversion must preserve: referenced resources
// get nodes + EdgeConfigures/EdgeUses edges to every referencing workload,
// unreferenced ones are omitted, and matching is strictly namespace-scoped
// (a same-named resource in another namespace must NOT cross-link).
func TestConfigSecretPVCEdges_InvertedMatching(t *testing.T) {
	provider := &mockProvider{
		deployments: []*appsv1.Deployment{
			deployWithRefs("app", "web", "shared-cm", "tls", "data"),
			deployWithRefs("app", "worker", "shared-cm", "", ""), // also refs shared-cm
			deployWithRefs("other", "web", "shared-cm", "", ""),  // same names, different ns
		},
		configMaps: []*corev1.ConfigMap{
			cm("app", "shared-cm"), cm("app", "unused-cm"),
			cm("other", "shared-cm"),
		},
		secrets: []*corev1.Secret{secret("app", "tls"), secret("app", "unused-secret")},
		pvcs:    []*corev1.PersistentVolumeClaim{pvc("app", "data"), pvc("app", "unused-pvc")},
	}

	opts := DefaultBuildOptions()
	opts.IncludeSecrets = true
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	nodeIDs := map[string]bool{}
	for _, n := range topo.Nodes {
		nodeIDs[n.ID] = true
	}
	edgeSet := map[string]bool{}
	for _, e := range topo.Edges {
		edgeSet[e.Source+" -> "+e.Target] = true
	}

	mustNode := func(id string) {
		t.Helper()
		if !nodeIDs[id] {
			t.Errorf("expected node %q to be present", id)
		}
	}
	noNode := func(id string) {
		t.Helper()
		if nodeIDs[id] {
			t.Errorf("expected node %q to be absent (unreferenced)", id)
		}
	}
	mustEdge := func(src, dst string) {
		t.Helper()
		if !edgeSet[src+" -> "+dst] {
			t.Errorf("expected edge %s -> %s", src, dst)
		}
	}
	noEdge := func(src, dst string) {
		t.Helper()
		if edgeSet[src+" -> "+dst] {
			t.Errorf("did not expect edge %s -> %s", src, dst)
		}
	}

	// shared-cm in app is referenced by BOTH web and worker → node + 2 edges.
	mustNode("configmap/app/shared-cm")
	mustEdge("configmap/app/shared-cm", "deployment/app/web")
	mustEdge("configmap/app/shared-cm", "deployment/app/worker")

	// tls secret + data PVC referenced only by app/web.
	mustNode("secret/app/tls")
	mustEdge("secret/app/tls", "deployment/app/web")
	mustNode("persistentvolumeclaim/app/data")
	mustEdge("persistentvolumeclaim/app/data", "deployment/app/web")

	// Unreferenced resources: no node (and no edge).
	noNode("configmap/app/unused-cm")
	noNode("secret/app/unused-secret")
	noNode("persistentvolumeclaim/app/unused-pvc")

	// Namespace scoping: other/shared-cm links only to other/web, never across
	// namespaces despite the identical name.
	mustNode("configmap/other/shared-cm")
	mustEdge("configmap/other/shared-cm", "deployment/other/web")
	noEdge("configmap/other/shared-cm", "deployment/app/web")
	noEdge("configmap/app/shared-cm", "deployment/other/web")
}
