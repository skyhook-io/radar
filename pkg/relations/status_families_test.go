package relations

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func clusterObject(apiVersion, kind, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name}}}
}

func TestPodsRunOnTheirNodeUntilTheyFinish(t *testing.T) {
	node := typed(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}, "", "v1", "Node")
	running := typed(&corev1.Pod{ObjectMeta: meta("web"), Spec: corev1.PodSpec{NodeName: "n1"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}, "", "v1", "Pod")
	checkFamily(t, PodPlacement, []runtime.Object{node, running}, running, resourceid.Ref{Kind: "Node", Name: "n1"}, "spec.nodeName")

	done := typed(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "job-1"}, Spec: corev1.PodSpec{NodeName: "n1"}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}, "", "v1", "Pod")
	if got := referrerRefs(NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{node, running, done}}}, ownership.Unrestricted()).Referrers(PodPlacement, refOf(node), nil)); len(got) != 1 || got[0] != refOf(running) {
		t.Errorf("the Node's Pods = %v; want the running one only", got)
	}
}

func TestReflectorMirrorsNameTheirSource(t *testing.T) {
	source := typed(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "registry"}}, "", "v1", "Secret")
	mirror := typed(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "registry", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "platform/registry"}}}, "", "v1", "Secret")
	checkFamily(t, Reflections, []runtime.Object{source, mirror}, mirror, refOf(source), "metadata.annotations[reflector.v1.k8s.emberstack.com/reflects]")

	cm := typed(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "settings", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "platform/registry"}}}, "", "v1", "ConfigMap")
	if got := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{source, cm}}}, ownership.Unrestricted()).References(Reflections, refOf(cm), cm); len(got.Declared) != 1 || got.Declared[0].Ref.Kind != "ConfigMap" || got.Declared[0].State != TargetAbsent {
		t.Errorf("a ConfigMap mirrors a ConfigMap, never a Secret: %+v", got.Declared)
	}
}

func TestStorageNamesVolumesClaimsAndClasses(t *testing.T) {
	class := "fast"
	sc := typed(clusterObject("storage.k8s.io/v1", "StorageClass", "fast"), "storage.k8s.io", "v1", "StorageClass")
	pv := typed(&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-1"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: "fast", ClaimRef: &corev1.ObjectReference{Namespace: "shop", Name: "data"}}}, "", "v1", "PersistentVolume")
	pvc := typed(&corev1.PersistentVolumeClaim{ObjectMeta: meta("data"), Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-1", StorageClassName: &class}}, "", "v1", "PersistentVolumeClaim")
	objects := []runtime.Object{sc, pv, pvc}
	v := NewView(allNamespaces{cluster: cluster{objects: objects}}, ownership.Unrestricted())

	if got := v.References(Storage, refOf(pvc), pvc); len(got.Declared) != 2 || got.Declared[0].Ref != refOf(pv) || got.Declared[1].Ref != refOf(sc) {
		t.Errorf("the claim declares %+v; want its volume and class", got.Declared)
	}
	if got := referrerRefs(v.Referrers(Storage, refOf(pvc), nil)); len(got) != 1 || got[0] != refOf(pv) {
		t.Errorf("the claim's referrers = %v; want the cluster-scoped volume bound to it", got)
	}
	if got := referrerRefs(v.Referrers(Storage, refOf(sc), nil)); len(got) != 2 {
		t.Errorf("the class's referrers = %v; want the claim and the volume", got)
	}
}

func TestMachinesAndRolloutsNameWhatTheirStatusRecords(t *testing.T) {
	node := typed(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "ip-10-0-0-1"}}, "", "v1", "Node")
	claim := clusterObject("karpenter.sh/v1", "NodeClaim", "general-abc")
	claim.Object["status"] = map[string]any{"nodeName": "ip-10-0-0-1"}
	checkFamily(t, MachineNodes, []runtime.Object{node, claim}, claim, refOf(node), "status.nodeName")

	machine := inNamespace(object("cluster.x-k8s.io/v1beta1", "Machine", "md-0-x", nil), "capi")
	machine.Object["status"] = map[string]any{"nodeRef": map[string]any{"name": "ip-10-0-0-1"}}
	checkFamily(t, MachineNodes, []runtime.Object{node, machine}, machine, refOf(node), "status.nodeRef")

	run := object("argoproj.io/v1alpha1", "AnalysisRun", "checkout-6c4f-2", nil)
	rollout := object("argoproj.io/v1alpha1", "Rollout", "checkout", nil)
	rollout.Object["status"] = map[string]any{"canary": map[string]any{"currentStepAnalysisRunStatus": map[string]any{"name": "checkout-6c4f-2", "status": "Running"}}}
	checkFamily(t, AnalysisRuns, []runtime.Object{run, rollout}, rollout, ref("argoproj.io", "AnalysisRun", "checkout-6c4f-2"), "status.canary.currentStepAnalysisRunStatus")
}

func TestASealedSecretProducesTheSecretOfItsName(t *testing.T) {
	secret := typed(&corev1.Secret{ObjectMeta: meta("db")}, "", "v1", "Secret")
	sealed := object("bitnami.com/v1alpha1", "SealedSecret", "db", map[string]any{"template": map[string]any{"metadata": map[string]any{"name": "ignored"}}})
	checkFamily(t, SealedSecrets, []runtime.Object{secret, sealed}, sealed, refOf(secret), "metadata.name")
}
