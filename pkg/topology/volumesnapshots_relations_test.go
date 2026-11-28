package topology

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/ownership"
)

func TestVolumeSnapshotRelationshipsProjectBothDirectionsAndWithholdDeniedTargets(t *testing.T) {
	snapGroup := "snapshot.storage.k8s.io"
	class := projectionCR(snapGroup, "VolumeSnapshotClass", "", "csi-snapclass", map[string]any{})
	sourcePVC := projectionObject(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db-data"},
	}, "", "PersistentVolumeClaim")
	restoredPVC := projectionObject(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db-clone"},
		Spec: corev1.PersistentVolumeClaimSpec{
			DataSource: &corev1.TypedLocalObjectReference{
				APIGroup: &snapGroup,
				Kind:     "VolumeSnapshot",
				Name:     "daily",
			},
		},
	}, "", "PersistentVolumeClaim")
	vsc := projectionCR(snapGroup, "VolumeSnapshotContent", "", "snapcontent-1", map[string]any{
		"spec": map[string]any{
			"volumeSnapshotClassName": "csi-snapclass",
			"volumeSnapshotRef": map[string]any{
				"namespace": "shop",
				"name":      "daily",
				"uid":       "uid-1",
			},
		},
	})
	staleVSC := projectionCR(snapGroup, "VolumeSnapshotContent", "", "snapcontent-stale", map[string]any{
		"spec": map[string]any{
			"volumeSnapshotRef": map[string]any{
				"namespace": "shop",
				"name":      "daily",
				"uid":       "uid-old",
			},
		},
	})
	vs := projectionCR(snapGroup, "VolumeSnapshot", "shop", "daily", map[string]any{
		"spec": map[string]any{
			"volumeSnapshotClassName": "csi-snapclass",
			"source":                  map[string]any{"persistentVolumeClaimName": "db-data"},
		},
		"status": map[string]any{
			"boundVolumeSnapshotContentName": "snapcontent-1",
		},
	})
	vs.SetUID("uid-1")

	session := newProjectionSession(t, class, sourcePVC, restoredPVC, vsc, staleVSC, vs)

	vsRel := projectionAnswer(t, session, vs, ownership.Unrestricted(), ownership.Unrestricted())
	if !slices.Contains(vsRel.Dependencies, refOf(projectionRef(vsc))) || !slices.Contains(vsRel.Dependencies, refOf(projectionRef(class))) {
		t.Fatalf("VolumeSnapshot Dependencies = %+v; want bound content and class", vsRel.Dependencies)
	}
	if slices.Contains(vsRel.Dependencies, refOf(projectionRef(staleVSC))) {
		t.Fatalf("retained VolumeSnapshotContent for an old UID leaked into replacement snapshot Dependencies: %+v", vsRel.Dependencies)
	}
	if slices.Contains(vsRel.Dependents, refOf(projectionRef(vsc))) {
		t.Fatalf("VolumeSnapshotContent should not duplicate into VolumeSnapshot Dependents: %+v", vsRel.Dependents)
	}
	if !slices.Contains(vsRel.StorageRefs, refOf(projectionRef(sourcePVC))) {
		t.Fatalf("VolumeSnapshot StorageRefs = %+v; want source PVC", vsRel.StorageRefs)
	}
	if !slices.Contains(vsRel.Consumers, refOf(projectionRef(restoredPVC))) {
		t.Fatalf("VolumeSnapshot Consumers = %+v; want restored PVC", vsRel.Consumers)
	}

	vscRel := projectionAnswer(t, session, vsc, ownership.Unrestricted(), ownership.Unrestricted())
	if !slices.Contains(vscRel.Dependents, refOf(projectionRef(vs))) || slices.Contains(vscRel.Dependencies, refOf(projectionRef(vs))) {
		t.Fatalf("VolumeSnapshotContent Dependents=%+v Dependencies=%+v; want snapshot in Dependents only", vscRel.Dependents, vscRel.Dependencies)
	}
	if !slices.Contains(vscRel.Dependencies, refOf(projectionRef(class))) {
		t.Fatalf("VolumeSnapshotContent Dependencies = %+v; want class", vscRel.Dependencies)
	}

	staleRel := projectionAnswer(t, session, staleVSC, ownership.Unrestricted(), ownership.Unrestricted())
	if slices.Contains(staleRel.Dependents, refOf(projectionRef(vs))) {
		t.Fatalf("retained VolumeSnapshotContent should not link to replacement snapshot with another UID: %+v", staleRel.Dependents)
	}

	sourceRel := projectionAnswer(t, session, sourcePVC, ownership.Unrestricted(), ownership.Unrestricted())
	if !slices.Contains(sourceRel.Consumers, refOf(projectionRef(vs))) {
		t.Fatalf("source PVC Consumers = %+v; want snapshot", sourceRel.Consumers)
	}

	restoredRel := projectionAnswer(t, session, restoredPVC, ownership.Unrestricted(), ownership.Unrestricted())
	if !slices.Contains(restoredRel.ConfigRefs, refOf(projectionRef(vs))) {
		t.Fatalf("restored PVC ConfigRefs = %+v; want snapshot", restoredRel.ConfigRefs)
	}

	deniedRel := projectionAnswer(t, session, vs, projectionAccess{denied: []string{"VolumeSnapshotContent", "VolumeSnapshotClass"}}, ownership.Unrestricted())
	if len(deniedRel.Dependencies) != 0 {
		t.Fatalf("denied cluster-scoped snapshot targets leaked into Dependencies: %+v", deniedRel.Dependencies)
	}
}
