package k8s

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
)

const CNPGPDBTestNamespace = "db"

// CNPGPDBTestFixture mirrors what CloudNativePG v1.30.1 creates for a healthy
// Cluster "pg": pods pg-1 (primary) .. pg-N, each Ready on its own Ready node
// node-<i>. For tests only.
type CNPGPDBTestFixture struct {
	Cluster *unstructured.Unstructured
	PDB     *policyv1.PodDisruptionBudget
	Pods    []*corev1.Pod
	PDBs    []*policyv1.PodDisruptionBudget
	Nodes   map[string]*corev1.Node
}

func NewCNPGPDBTestFixture(instances int) *CNPGPDBTestFixture {
	ns := CNPGPDBTestNamespace
	uid := types.UID("cluster-uid")
	cluster := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": "pg", "namespace": ns, "uid": string(uid)},
		"spec":       map[string]any{"instances": int64(instances)},
		"status": map[string]any{
			"phase":          "Cluster in healthy state",
			"readyInstances": int64(instances),
			"currentPrimary": "pg-1",
			"targetPrimary":  "pg-1",
		},
	}}
	yes := true
	owner := metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg", UID: uid, Controller: &yes}
	one := intstr.FromInt32(1)
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-primary", Namespace: ns, Generation: 1, OwnerReferences: []metav1.OwnerReference{owner}, Labels: map[string]string{"cnpg.io/cluster": "pg"}},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &one,
			Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"cnpg.io/cluster": "pg", "cnpg.io/instanceRole": "primary"}},
		},
		Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1, ExpectedPods: 1, CurrentHealthy: 1, DesiredHealthy: 1},
	}
	f := &CNPGPDBTestFixture{Cluster: cluster, PDB: pdb, PDBs: []*policyv1.PodDisruptionBudget{pdb}, Nodes: map[string]*corev1.Node{}}
	for i := 1; i <= instances; i++ {
		role := "replica"
		if i == 1 {
			role = "primary"
		}
		node := fmt.Sprintf("node-%d", i)
		f.Pods = append(f.Pods, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pg-%d", i), Namespace: ns, OwnerReferences: []metav1.OwnerReference{owner},
				Labels: map[string]string{"cnpg.io/cluster": "pg", "cnpg.io/instanceRole": role}},
			Spec:   corev1.PodSpec{NodeName: node},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
		})
		f.Nodes[node] = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	}
	return f
}

// Install loads the fixture into the test caches, with the budget blocked
// since a day ago and an unrecognised twin budget "plain" (same spec and
// status, no CloudNativePG identity). mode "no-cluster" leaves Clusters
// unwatched; "no-nodes" leaves the nodes out.
func (f *CNPGPDBTestFixture) Install(mode string) error {
	f.PDB.CreationTimestamp = metav1.NewTime(time.Now().Add(-48 * time.Hour))
	f.PDB.Status.Conditions = []metav1.Condition{{Type: policyv1.DisruptionAllowedCondition, Status: metav1.ConditionFalse, Reason: "InsufficientPods", LastTransitionTime: metav1.NewTime(time.Now().Add(-24 * time.Hour))}}
	objects := []runtime.Object{f.PDB, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: f.PDB.Namespace}}}
	for _, p := range f.Pods {
		objects = append(objects, p)
	}
	if mode != "no-nodes" {
		for _, n := range f.Nodes {
			objects = append(objects, n)
		}
	}
	twin := f.PDB.DeepCopy()
	twin.Name, twin.OwnerReferences = "plain", nil
	twin.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "plain"}}
	objects = append(objects, twin)
	ResetResourceCache()
	if err := InitTestResourceCache(fakeclientset.NewClientset(objects...)); err != nil {
		return err
	}
	var dynObjects []runtime.Object
	if f.Cluster != nil {
		dynObjects = append(dynObjects, f.Cluster)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{cnpgClusterGVR: "ClusterList"}, dynObjects...)
	if err := InitTestDynamicResourceCache(dyn, []APIResource{{Group: cnpgClusterGVR.Group, Version: cnpgClusterGVR.Version, Kind: "Cluster", Name: cnpgClusterGVR.Resource, Namespaced: true, Verbs: []string{"list", "watch"}}}); err != nil {
		return err
	}
	if mode == "no-cluster" {
		return nil
	}
	WarmupCommonCRDs()
	if !GetDynamicResourceCache().WaitForSync(cnpgClusterGVR, 3*time.Second) {
		return fmt.Errorf("clusters did not sync")
	}
	return nil
}
