package topology

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
