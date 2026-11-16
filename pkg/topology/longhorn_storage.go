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

// longhornVolumesNamed lists the cached Longhorn Volumes with this name;
// more than one (a leftover second install) means a handle can't tell them apart.
func longhornVolumesNamed(name string, dp DynamicProvider) []ResourceRef {
	if name == "" || dp == nil {
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
	var refs []ResourceRef
	for _, v := range volumes {
		if v != nil && v.GetName() == name {
			refs = append(refs, ResourceRef{Kind: "Volume", Group: longhorn.Group, Namespace: v.GetNamespace(), Name: name})
		}
	}
	return refs
}

// longhornVolumeRef is the Volume a Longhorn PV is backed by, only when
// exactly one cached Volume has its handle: a handle alone doesn't prove the
// Volume exists.
func longhornVolumeRef(pv *corev1.PersistentVolume, dp DynamicProvider) *ResourceRef {
	if refs := longhornVolumesNamed(longhornHandle(pv), dp); len(refs) == 1 {
		return &refs[0]
	}
	return nil
}

// longhornVolumePVs are the PVs backed by the named Longhorn Volume, unless
// another cached Volume shares its name.
func longhornVolumePVs(name string, provider ResourceProvider, dp DynamicProvider) []ResourceRef {
	if len(longhornVolumesNamed(name, dp)) > 1 {
		return nil
	}
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
