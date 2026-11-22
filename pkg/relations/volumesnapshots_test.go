package relations

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func vsObject(ns, name string, spec, status map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1",
		"kind":       "VolumeSnapshot",
		"metadata":   map[string]any{"namespace": ns, "name": name},
		"spec":       spec,
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

func vscObject(name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1",
		"kind":       "VolumeSnapshotContent",
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}}
}

func TestVolumeSnapshotsLinkObservedBindingsClassesAndSourcePVC(t *testing.T) {
	class := clusterObject("snapshot.storage.k8s.io/v1", "VolumeSnapshotClass", "csi-snapclass")
	pvc := typed(&corev1.PersistentVolumeClaim{ObjectMeta: meta("data")}, "", "v1", "PersistentVolumeClaim")
	vsc := vscObject("snapcontent-1", map[string]any{
		"volumeSnapshotClassName": "csi-snapclass",
		"volumeSnapshotRef": map[string]any{
			"namespace": "shop",
			"name":      "daily",
			"uid":       "uid-123",
		},
	})
	vs := vsObject("shop", "daily",
		map[string]any{
			"volumeSnapshotClassName": "csi-snapclass",
			"source":                  map[string]any{"persistentVolumeClaimName": "data"},
		},
		map[string]any{"boundVolumeSnapshotContentName": "snapcontent-1"},
	)

	v := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{class, pvc, vsc, vs}}}, ownership.Unrestricted())

	got := v.References(VolumeSnapshots, refOf(vs), vs)
	if len(got.Declared) != 3 ||
		got.Declared[0].Ref != refOf(pvc) || !slices.Equal(got.Declared[0].Paths, []string{"spec.source.persistentVolumeClaimName"}) ||
		got.Declared[1].Ref != refOf(class) || !slices.Equal(got.Declared[1].Paths, []string{"spec.volumeSnapshotClassName"}) ||
		got.Declared[2].Ref != refOf(vsc) || !slices.Equal(got.Declared[2].Paths, []string{"status.boundVolumeSnapshotContentName"}) {
		t.Fatalf("VolumeSnapshot references = %+v", got.Declared)
	}

	vscRefs := v.References(VolumeSnapshots, refOf(vsc), vsc)
	if len(vscRefs.Declared) != 2 || vscRefs.Declared[0].Ref != refOf(vs) || vscRefs.Declared[1].Ref != refOf(class) {
		t.Fatalf("VolumeSnapshotContent references = %+v", vscRefs.Declared)
	}

	if r := referrerRefs(v.Referrers(VolumeSnapshots, refOf(vsc), nil)); len(r) != 1 || r[0] != refOf(vs) {
		t.Errorf("VolumeSnapshotContent referrers = %v; want [%v]", r, refOf(vs))
	}
	if r := referrerRefs(v.Referrers(VolumeSnapshots, refOf(pvc), nil)); len(r) != 1 || r[0] != refOf(vs) {
		t.Errorf("source PVC referrers = %v; want [%v]", r, refOf(vs))
	}
	if r := referrerRefs(v.Referrers(VolumeSnapshots, refOf(class), nil)); len(r) != 2 {
		t.Errorf("VolumeSnapshotClass referrers = %v; want both snapshot and content", r)
	}
}

func TestVolumeSnapshotsIgnoreUnboundPreProvisionedRefs(t *testing.T) {
	vscUnbound := vscObject("snapcontent-pre", map[string]any{
		"volumeSnapshotRef": map[string]any{
			"namespace": "shop",
			"name":      "pre",
		},
	})
	vsUnbound := vsObject("shop", "pre",
		map[string]any{
			"source": map[string]any{"volumeSnapshotContentName": "snapcontent-pre"},
		},
		nil,
	)

	v := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{vscUnbound, vsUnbound}}}, ownership.Unrestricted())
	if got := v.References(VolumeSnapshots, refOf(vsUnbound), vsUnbound); len(got.Declared) != 0 {
		t.Errorf("unbound pre-provisioned snapshot linked requested content: %+v", got.Declared)
	}
	if got := v.References(VolumeSnapshots, refOf(vscUnbound), vscUnbound); len(got.Declared) != 0 {
		t.Errorf("unbound VolumeSnapshotContent without uid linked snapshot: %+v", got.Declared)
	}
}

func TestVolumeSnapshotsReportAbsentAndWithheldTargets(t *testing.T) {
	class := clusterObject("snapshot.storage.k8s.io/v1", "VolumeSnapshotClass", "csi-snapclass")
	vs := vsObject("shop", "daily",
		map[string]any{"volumeSnapshotClassName": "csi-snapclass"},
		map[string]any{"boundVolumeSnapshotContentName": "missing-content"},
	)

	absent := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{class, vs}}}, ownership.Unrestricted()).
		References(VolumeSnapshots, refOf(vs), vs)
	if len(absent.Declared) != 2 ||
		absent.Declared[0].Ref != refOf(class) || absent.Declared[0].State != TargetPresent ||
		absent.Declared[1].Ref.Kind != "VolumeSnapshotContent" || absent.Declared[1].State != TargetAbsent {
		t.Fatalf("absent content check = %+v", absent.Declared)
	}

	withheld := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{class, vs}}}, denies{"VolumeSnapshotContent", "VolumeSnapshotClass"}).
		References(VolumeSnapshots, refOf(vs), vs)
	if !withheld.Coverage.Narrowed || len(withheld.Declared) != 2 ||
		withheld.Declared[0].State != TargetWithheld || withheld.Declared[1].State != TargetWithheld {
		t.Fatalf("withheld cluster-scoped targets = %+v (coverage=%+v)", withheld.Declared, withheld.Coverage)
	}
}

func TestRestoredVolumeSnapshotsCoverDataSourceDataSourceRefAndDedup(t *testing.T) {
	snapGroup := volumeSnapshotGroup
	otherGroup := "example.com"
	otherNS := "backups"

	snapLocal := vsObject("shop", "daily", map[string]any{}, nil)
	snapRemote := vsObject("backups", "gold", map[string]any{}, nil)

	pvcBothSame := typed(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "restored-local"},
		Spec: corev1.PersistentVolumeClaimSpec{
			DataSource: &corev1.TypedLocalObjectReference{
				APIGroup: &snapGroup,
				Kind:     "VolumeSnapshot",
				Name:     "daily",
			},
			DataSourceRef: &corev1.TypedObjectReference{
				APIGroup: &snapGroup,
				Kind:     "VolumeSnapshot",
				Name:     "daily",
			},
		},
	}, "", "v1", "PersistentVolumeClaim")

	pvcCrossNS := typed(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "restored-cross"},
		Spec: corev1.PersistentVolumeClaimSpec{
			DataSourceRef: &corev1.TypedObjectReference{
				APIGroup:  &snapGroup,
				Kind:      "VolumeSnapshot",
				Name:      "gold",
				Namespace: &otherNS,
			},
		},
	}, "", "v1", "PersistentVolumeClaim")

	pvcOtherGroup := typed(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "other-populator"},
		Spec: corev1.PersistentVolumeClaimSpec{
			DataSource: &corev1.TypedLocalObjectReference{
				APIGroup: &otherGroup,
				Kind:     "VolumeSnapshot",
				Name:     "daily",
			},
		},
	}, "", "v1", "PersistentVolumeClaim")

	objects := []runtime.Object{snapLocal, snapRemote, pvcBothSame, pvcCrossNS, pvcOtherGroup}
	v := NewView(allNamespaces{cluster: cluster{objects: objects}}, ownership.Unrestricted())

	localRefs := v.References(RestoredVolumeSnapshots, refOf(pvcBothSame), pvcBothSame)
	if len(localRefs.Declared) != 1 || localRefs.Declared[0].Ref != refOf(snapLocal) || !slices.Equal(localRefs.Declared[0].Paths, []string{"spec.dataSource"}) {
		t.Fatalf("mirrored dataSource/dataSourceRef should deduplicate: %+v", localRefs.Declared)
	}

	crossRefs := v.References(RestoredVolumeSnapshots, refOf(pvcCrossNS), pvcCrossNS)
	if len(crossRefs.Declared) != 1 || crossRefs.Declared[0].Ref != resourceid.NewRef(volumeSnapshotGroup, "VolumeSnapshot", "backups", "gold") {
		t.Fatalf("cross-namespace dataSourceRef = %+v", crossRefs.Declared)
	}

	if otherRefs := v.References(RestoredVolumeSnapshots, refOf(pvcOtherGroup), pvcOtherGroup); len(otherRefs.Declared) != 0 {
		t.Errorf("wrong API group should be ignored: %+v", otherRefs.Declared)
	}

	if r := referrerRefs(v.Referrers(RestoredVolumeSnapshots, refOf(snapLocal), nil)); len(r) != 1 || r[0] != refOf(pvcBothSame) {
		t.Errorf("local snapshot restored PVCs = %v; want [%v]", r, refOf(pvcBothSame))
	}
	if r := referrerRefs(v.Referrers(RestoredVolumeSnapshots, refOf(snapRemote), nil)); len(r) != 1 || r[0] != refOf(pvcCrossNS) {
		t.Errorf("cross-namespace snapshot restored PVCs = %v; want [%v]", r, refOf(pvcCrossNS))
	}
}
