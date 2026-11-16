package topology

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type pvProvider struct {
	stubProvider
	pvs []*corev1.PersistentVolume
}

func (p *pvProvider) PersistentVolumes() ([]*corev1.PersistentVolume, error) { return p.pvs, nil }

var longhornVolumesGVR = schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "volumes"}

func csiPV(name, driver, handle string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{
			CSI: &corev1.CSIPersistentVolumeSource{Driver: driver, VolumeHandle: handle},
		}},
	}
}

func longhornVolume(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "longhorn.io/v1beta2", "kind": "Volume",
		"metadata": map[string]any{"namespace": namespace, "name": name},
	}}
}

func longhornDynamic(volumes ...*unstructured.Unstructured) *genericIdentityDynamic {
	return &genericIdentityDynamic{
		watched:   []schema.GroupVersionResource{longhornVolumesGVR},
		kinds:     map[schema.GroupVersionResource]string{longhornVolumesGVR: "Volume"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{longhornVolumesGVR: volumes},
		listCalls: map[schema.GroupVersionResource]int{},
	}
}

// A Longhorn Volume is in use by the PV whose CSI handle names it: the live
// PV is the proof, never the names Longhorn last recorded.
func TestLonghornVolumeRelatesToThePVThatNamesIt(t *testing.T) {
	provider := &pvProvider{pvs: []*corev1.PersistentVolume{
		csiPV("pv-a", "driver.longhorn.io", "vol-1"),
		csiPV("pv-b", "driver.longhorn.io", "vol-2"),
		csiPV("pv-c", "other.csi.example.com", "vol-1"),
	}}
	vol := longhornVolume("longhorn-system", "vol-1")
	rel := GetRelationshipsWithObject("volumes", "longhorn-system", "vol-1", vol, &Topology{}, provider, longhornDynamic(vol), nil)
	if rel == nil || len(rel.Consumers) != 1 || rel.Consumers[0].Kind != "PersistentVolume" || rel.Consumers[0].Name != "pv-a" || rel.Consumers[0].Group != "" {
		t.Fatalf("Volume consumers = %+v, want only PersistentVolume pv-a (Longhorn driver, matching handle)", rel)
	}
}

// The PV leads back to its Longhorn Volume only when that Volume exists.
func TestLonghornPVRelatesToItsVolume(t *testing.T) {
	vol := longhornVolume("longhorn-system", "vol-1")
	dp := longhornDynamic(vol)
	pv := csiPV("pv-a", "driver.longhorn.io", "vol-1")
	rel := GetRelationshipsWithObject("persistentvolumes", "", "pv-a", pv, &Topology{}, &pvProvider{pvs: []*corev1.PersistentVolume{pv}}, dp, nil)
	want := ResourceRef{Kind: "Volume", Group: "longhorn.io", Namespace: "longhorn-system", Name: "vol-1"}
	if rel == nil || len(rel.StorageRefs) != 1 || rel.StorageRefs[0] != want {
		t.Fatalf("PV storage refs = %+v, want [%+v]", rel, want)
	}

	orphan := csiPV("pv-b", "driver.longhorn.io", "vol-gone")
	rel = GetRelationshipsWithObject("persistentvolumes", "", "pv-b", orphan, &Topology{}, &pvProvider{pvs: []*corev1.PersistentVolume{orphan}}, dp, nil)
	if rel != nil && len(rel.StorageRefs) != 0 {
		t.Errorf("PV whose Volume isn't there: storage refs = %+v, want none", rel.StorageRefs)
	}
}
