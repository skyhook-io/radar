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

// Two Volumes with the PV's handle (a leftover second install): the handle
// can't tell them apart, so neither direction claims the PV.
func TestLonghornAmbiguousVolumeNameLinksNothing(t *testing.T) {
	a, b := longhornVolume("longhorn-system", "vol-1"), longhornVolume("longhorn-old", "vol-1")
	dp := longhornDynamic(a, b)
	pv := csiPV("pv-a", "driver.longhorn.io", "vol-1")
	provider := &pvProvider{pvs: []*corev1.PersistentVolume{pv}}
	if rel := GetRelationshipsWithObject("persistentvolumes", "", "pv-a", pv, &Topology{}, provider, dp, nil); rel != nil && len(rel.StorageRefs) != 0 {
		t.Errorf("PV storage refs = %+v, want none when two Volumes share the handle", rel.StorageRefs)
	}
	if rel := GetRelationshipsWithObject("volumes", "longhorn-old", "vol-1", b, &Topology{}, provider, dp, nil); rel != nil && len(rel.Consumers) != 0 {
		t.Errorf("Volume consumers = %+v, want none when two Volumes share the name", rel.Consumers)
	}
}

type readOnlyLonghornDynamic struct {
	*genericIdentityDynamic
	readOnlyCalls, copyingCalls int
}

func (d *readOnlyLonghornDynamic) List(gvr schema.GroupVersionResource, ns string) ([]*unstructured.Unstructured, error) {
	d.copyingCalls++
	return d.genericIdentityDynamic.List(gvr, ns)
}

func (d *readOnlyLonghornDynamic) ListWatchedReadOnly(gvr schema.GroupVersionResource) ([]*unstructured.Unstructured, error) {
	d.readOnlyCalls++
	return d.resources[gvr], nil
}

// Matching a handle only reads names, so it uses the cache's read-only list
// rather than copying every Volume on each PV read.
func TestLonghornVolumeLookupReadsTheCacheWithoutCopying(t *testing.T) {
	vol := longhornVolume("longhorn-system", "vol-1")
	dp := &readOnlyLonghornDynamic{genericIdentityDynamic: longhornDynamic(vol)}
	pv := csiPV("pv-a", "driver.longhorn.io", "vol-1")
	rel := GetRelationshipsWithObject("persistentvolumes", "", "pv-a", pv, &Topology{}, &pvProvider{pvs: []*corev1.PersistentVolume{pv}}, dp, nil)
	if rel == nil || len(rel.StorageRefs) != 1 {
		t.Fatalf("PV storage refs = %+v, want its Volume", rel)
	}
	if dp.copyingCalls != 0 || dp.readOnlyCalls == 0 {
		t.Errorf("copying List calls = %d, read-only calls = %d; want the read-only list only", dp.copyingCalls, dp.readOnlyCalls)
	}
}

// Each Longhorn link is shown only when the caller may read what it names.
func TestLonghornLinksWithholdWhatTheCallerMayNotRead(t *testing.T) {
	vol := longhornVolume("longhorn-system", "vol-1")
	pv := csiPV("pv-a", "driver.longhorn.io", "vol-1")
	provider := &pvProvider{pvs: []*corev1.PersistentVolume{pv}}
	deny := func(kind string) func(ResourceRef) bool { return func(r ResourceRef) bool { return r.Kind != kind } }
	if rel := getRelationshipsWithObject("persistentvolumes", "", "pv-a", pv, &Topology{}, provider, longhornDynamic(vol), nil, deny("Volume")); rel != nil && len(rel.StorageRefs) != 0 {
		t.Errorf("Volume withheld: PV storage refs = %+v, want none", rel.StorageRefs)
	}
	if rel := getRelationshipsWithObject("volumes", "longhorn-system", "vol-1", vol, &Topology{}, provider, longhornDynamic(vol), nil, deny("PersistentVolume")); rel != nil && len(rel.Consumers) != 0 {
		t.Errorf("PV withheld: Volume consumers = %+v, want none", rel.Consumers)
	}
}
