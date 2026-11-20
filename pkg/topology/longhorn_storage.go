package topology

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/longhorn"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

var longhornVolumeKind = resourceid.GroupKind{Group: longhorn.Group, Kind: "Volume"}

// longhornHandle is the Longhorn Volume a PV is backed by, or "".
func longhornHandle(pv *corev1.PersistentVolume) string {
	if pv == nil || pv.Spec.CSI == nil || pv.Spec.CSI.Driver != longhorn.CSIDriver {
		return ""
	}
	return pv.Spec.CSI.VolumeHandle
}

// longhornVolumesNamed are the Longhorn Volumes with this name in every
// namespace, read with Radar's identity: a PV's CSI handle names no
// namespace, so it identifies a Volume only when no other Volume shares the
// name, whether or not the caller may see that other one. ok is false when
// some Volumes couldn't be read, as while the cache warms up: uniqueness is
// then unproven. The gap is kept without its namespace, which the caller may
// not be allowed to know.
func (n *neighborhood) longhornVolumesNamed(session relations.Session, name string) (refs []resourceid.Ref, ok bool) {
	read := session.Indexed(longhornVolumeKind, "", nil, ownership.SearchScope{AllNamespaces: true})
	if len(read.Gaps) > 0 {
		for _, gap := range read.Gaps {
			n.cover("direct", relations.Coverage{NotChecked: []ownership.Gap{{Kind: gap.Kind, Reason: gap.Reason}}})
		}
		return nil, false
	}
	for _, o := range read.Objects {
		if o.Ref.Name == name {
			refs = append(refs, o.Ref)
		}
	}
	return refs, true
}

// longhornStorage links a Longhorn Volume and the PV whose CSI handle names
// it, in both directions, only when exactly one Volume has that name: the
// live PV is the proof, never the names Longhorn last recorded. Each link is
// emitted only when the caller may read what it names.
func (n *neighborhood) longhornStorage(session relations.Session, subject resourceid.Ref, obj runtime.Object, rel *Relationships) {
	switch subject.GroupKind() {
	case resourceid.GroupKind{Kind: "PersistentVolume"}:
		pv, _ := obj.(*corev1.PersistentVolume)
		handle := longhornHandle(pv)
		if handle == "" {
			return
		}
		if volumes, ok := n.longhornVolumesNamed(session, handle); ok && len(volumes) == 1 && n.allowed(volumes[0]) {
			rel.StorageRefs = appendResourceRef(rel.StorageRefs, refOf(volumes[0]))
		}
	case longhornVolumeKind:
		if volumes, ok := n.longhornVolumesNamed(session, subject.Name); !ok || len(volumes) != 1 || volumes[0] != subject {
			return
		}
		// The shared provider answers a denied kind with an empty list; a
		// caller who can't read PVs must see a narrowed answer, not "unused".
		if n.access.CanRead(resourceid.GroupKind{Kind: "PersistentVolume"}, "") == ownership.Denied {
			n.cover("special", relations.Coverage{Narrowed: true})
			return
		}
		provider := relationDirectProvider{session: session, n: n}
		pvs, err := provider.PersistentVolumes()
		if err != nil {
			// The read recorded its gap; a partial PV list can't show all
			// of the Volume's use.
			return
		}
		for _, pv := range pvs {
			if longhornHandle(pv) != subject.Name {
				continue
			}
			if ref := resourceid.NewRef("", "PersistentVolume", "", pv.Name); n.allowed(ref) {
				rel.Consumers = appendResourceRef(rel.Consumers, refOf(ref))
			}
		}
	}
}
