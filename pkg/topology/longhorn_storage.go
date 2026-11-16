package topology

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/pkg/longhorn"
)

// longhornHandle is the Longhorn Volume a PV is backed by, or "".
func longhornHandle(pv *corev1.PersistentVolume) string {
	if pv == nil || pv.Spec.CSI == nil || pv.Spec.CSI.Driver != longhorn.CSIDriver {
		return ""
	}
	return pv.Spec.CSI.VolumeHandle
}

// longhornVolumeRef is the Volume a Longhorn PV is backed by, only when that
// Volume is in the cache: a handle alone doesn't prove the Volume exists.
func longhornVolumeRef(pv *corev1.PersistentVolume, dp DynamicProvider) *ResourceRef {
	handle := longhornHandle(pv)
	if handle == "" || dp == nil {
		return nil
	}
	gvr, ok := dp.GetGVRWithGroup("Volume", longhorn.Group)
	if !ok {
		return nil
	}
	volumes, err := dp.List(gvr, "")
	if err != nil {
		return nil
	}
	for _, v := range volumes {
		if v != nil && v.GetName() == handle {
			return &ResourceRef{Kind: "Volume", Group: longhorn.Group, Namespace: v.GetNamespace(), Name: handle}
		}
	}
	return nil
}

// longhornVolumePVs are the PVs backed by the named Longhorn Volume.
func longhornVolumePVs(name string, provider ResourceProvider, dp DynamicProvider) []ResourceRef {
	pvs, _ := provider.PersistentVolumes()
	var refs []ResourceRef
	for _, pv := range pvs {
		if longhornHandle(pv) == name {
			ref := ResourceRef{Kind: "PersistentVolume", Name: pv.Name}
			enrichRef(&ref, dp)
			refs = append(refs, ref)
		}
	}
	return refs
}
