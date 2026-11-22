package relations

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

const volumeSnapshotGroup = "snapshot.storage.k8s.io"

var (
	kindVolumeSnapshot        = resourceid.GroupKind{Group: volumeSnapshotGroup, Kind: "VolumeSnapshot"}
	kindVolumeSnapshotContent = resourceid.GroupKind{Group: volumeSnapshotGroup, Kind: "VolumeSnapshotContent"}
	kindVolumeSnapshotClass   = resourceid.GroupKind{Group: volumeSnapshotGroup, Kind: "VolumeSnapshotClass"}
)

// VolumeSnapshots is the family of CSI snapshot bindings, classes, and source
// claims: a VolumeSnapshot's observed bound VolumeSnapshotContent, its
// VolumeSnapshotClass, and its source PersistentVolumeClaim, plus a
// VolumeSnapshotContent's controller-bound VolumeSnapshot and class.
var VolumeSnapshots = &Family{
	ReferrersIndex: referrersIndex("volumeSnapshots"),
	Name:           "volumeSnapshots",
	Projection: project("uses", false, "Dependencies", "Dependents").
		whenTarget(kindPVC, "uses", true, "StorageRefs", "Consumers").
		whenTarget(kindVolumeSnapshot, "uses", true, "Dependents", "Dependencies"),
	Sources: []Source{
		{Kind: kindVolumeSnapshot, Targets: []resourceid.GroupKind{kindVolumeSnapshotContent, kindVolumeSnapshotClass, kindPVC}},
		{Kind: kindVolumeSnapshotContent, Targets: []resourceid.GroupKind{kindVolumeSnapshot, kindVolumeSnapshotClass}},
	},
	Targets: []resourceid.GroupKind{kindVolumeSnapshotContent, kindVolumeSnapshotClass, kindPVC, kindVolumeSnapshot},
	// VolumeSnapshotContent and VolumeSnapshotClass are cluster-scoped while
	// VolumeSnapshot and PersistentVolumeClaim are namespaced.
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, kindVolumeSnapshot); ok {
			// Only status.boundVolumeSnapshotContentName proves binding;
			// spec.source.volumeSnapshotContentName is a request only.
			if content, _, _ := unstructured.NestedString(u.Object, "status", "boundVolumeSnapshotContentName"); content != "" {
				visit(Declaration{
					Target: resourceid.NewRef(volumeSnapshotGroup, "VolumeSnapshotContent", "", content),
					Path:   "status.boundVolumeSnapshotContentName",
				})
			}
			if class, _, _ := unstructured.NestedString(u.Object, "spec", "volumeSnapshotClassName"); class != "" {
				visit(Declaration{
					Target: resourceid.NewRef(volumeSnapshotGroup, "VolumeSnapshotClass", "", class),
					Path:   "spec.volumeSnapshotClassName",
				})
			}
			if pvc, _, _ := unstructured.NestedString(u.Object, "spec", "source", "persistentVolumeClaimName"); pvc != "" && u.GetNamespace() != "" {
				visit(Declaration{
					Target: resourceid.NewRef("", "PersistentVolumeClaim", u.GetNamespace(), pvc),
					Path:   "spec.source.persistentVolumeClaimName",
				})
			}
			return nil
		}
		if u, ok := unstructuredOf(obj, kindVolumeSnapshotContent); ok {
			// A populated UID distinguishes an observed controller binding from
			// a pre-provisioned reference waiting to bind.
			refName, _, _ := unstructured.NestedString(u.Object, "spec", "volumeSnapshotRef", "name")
			refNS, _, _ := unstructured.NestedString(u.Object, "spec", "volumeSnapshotRef", "namespace")
			refUID, _, _ := unstructured.NestedString(u.Object, "spec", "volumeSnapshotRef", "uid")
			if refName != "" && refNS != "" && refUID != "" {
				visit(Declaration{
					Target: resourceid.NewRef(volumeSnapshotGroup, "VolumeSnapshot", refNS, refName),
					Path:   "spec.volumeSnapshotRef",
				})
			}
			if class, _, _ := unstructured.NestedString(u.Object, "spec", "volumeSnapshotClassName"); class != "" {
				visit(Declaration{
					Target: resourceid.NewRef(volumeSnapshotGroup, "VolumeSnapshotClass", "", class),
					Path:   "spec.volumeSnapshotClassName",
				})
			}
		}
		return nil
	},
}

// RestoredVolumeSnapshots is the family of PersistentVolumeClaims restored from
// a CSI VolumeSnapshot via spec.dataSource or spec.dataSourceRef.
var RestoredVolumeSnapshots = &Family{
	ScanRationale:  "Indexing PersistentVolumeClaim dataSource references on every claim update exceeds the per-update admission bar (BenchmarkRelationIndexUpdate), mirroring Storage.",
	Name:           "restoredVolumeSnapshots",
	Projection:     project("configures", true, "ConfigRefs", "Consumers"),
	Sources:        []Source{{Kind: kindPVC}},
	Targets:        []resourceid.GroupKind{kindVolumeSnapshot},
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		pvc, ok := obj.(*corev1.PersistentVolumeClaim)
		if !ok {
			return nil
		}
		var first resourceid.Ref
		if ds := pvc.Spec.DataSource; ds != nil && ds.APIGroup != nil && *ds.APIGroup == volumeSnapshotGroup && ds.Kind == "VolumeSnapshot" && ds.Name != "" && pvc.Namespace != "" {
			first = resourceid.NewRef(volumeSnapshotGroup, "VolumeSnapshot", pvc.Namespace, ds.Name)
			visit(Declaration{Target: first, Path: "spec.dataSource"})
		}
		if dsr := pvc.Spec.DataSourceRef; dsr != nil && dsr.APIGroup != nil && *dsr.APIGroup == volumeSnapshotGroup && dsr.Kind == "VolumeSnapshot" && dsr.Name != "" {
			ns := pvc.Namespace
			if dsr.Namespace != nil && *dsr.Namespace != "" {
				ns = *dsr.Namespace
			}
			if ns != "" {
				ref := resourceid.NewRef(volumeSnapshotGroup, "VolumeSnapshot", ns, dsr.Name)
				if ref != first {
					visit(Declaration{Target: ref, Path: "spec.dataSourceRef"})
				}
			}
		}
		return nil
	},
}
