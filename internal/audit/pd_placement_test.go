package audit

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestRunFromCacheFlagsPDVolumeReachingHyperdiskOnlyNode(t *testing.T) {
	defer k8s.ResetTestState()
	yes := true
	client := fake.NewClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "e2-node", Labels: map[string]string{"node.kubernetes.io/instance-type": "e2-standard-2"}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n4d-node", Labels: map[string]string{"node.kubernetes.io/instance-type": "n4d-standard-4"}}},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "premium-rwo"}, Provisioner: "pd.csi.storage.gke.io", Parameters: map[string]string{"type": "pd-ssd"}},
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc-1", Annotations: map[string]string{"pv.kubernetes.io/provisioned-by": "pd.csi.storage.gke.io"}},
			Spec: corev1.PersistentVolumeSpec{
				StorageClassName:       "premium-rwo",
				PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "pd.csi.storage.gke.io", VolumeHandle: "d"}},
			},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "redis-data-redis-master-0", Namespace: "redis"},
			Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pvc-1"},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "redis-master-0", Namespace: "redis", OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "StatefulSet", Name: "redis-master", UID: "sts", Controller: &yes,
			}}},
			Spec: corev1.PodSpec{
				NodeName:   "e2-node",
				Containers: []corev1.Container{{Name: "redis", Image: "redis:7"}},
				Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "redis-data-redis-master-0"},
				}}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}

	results := RunFromCache(k8s.GetResourceCache(), nil, nil)
	var found bool
	for _, f := range results.Findings {
		if f.CheckID != "pdVolumeHyperdiskOnlyNode" {
			continue
		}
		found = true
		if f.Kind != "StatefulSet" || f.Name != "redis-master" || !strings.Contains(f.Message, "n4d-node (n4d-standard-4)") {
			t.Errorf("finding = %+v", f)
		}
	}
	if !found {
		t.Fatalf("expected a pdVolumeHyperdiskOnlyNode finding, got %+v", results.Findings)
	}

	denied := RunFromCache(k8s.GetResourceCache(), nil, &RunOptions{Scope: resolveReadScope(nil, nil, nil, denyAll)})
	for _, f := range denied.Findings {
		if f.CheckID == "pdVolumeHyperdiskOnlyNode" {
			t.Fatalf("a reader who can't list nodes must not see node placement: %+v", f)
		}
	}
}
