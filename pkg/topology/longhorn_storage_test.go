package topology

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func csiPV(name, driver, handle string) runtime.Object {
	return projectionObject(&corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{
			CSI: &corev1.CSIPersistentVolumeSource{Driver: driver, VolumeHandle: handle},
		}},
	}, "", "PersistentVolume")
}

func longhornVolume(namespace, name string) runtime.Object {
	return projectionCR("longhorn.io", "Volume", namespace, name, map[string]any{})
}

func longhornAnswer(t *testing.T, s *projectionSession, obj runtime.Object, access ownership.Access) (*Relationships, map[string]relations.Coverage) {
	t.Helper()
	captured, ok := relations.CaptureSubject(s, projectionRef(obj), obj)
	if !ok {
		t.Fatal("capture failed")
	}
	return RelationsAnswer(captured, access, ownership.Unrestricted(), ownership.NewView(captured, access), projectionRef(obj), obj, nil, nil)
}

var (
	lhVolumeRef = ResourceRef{Kind: "Volume", Group: "longhorn.io", Namespace: "longhorn-system", Name: "vol-1"}
	lhPVRef     = ResourceRef{Kind: "PersistentVolume", Name: "pv-a"}
)

// A Longhorn Volume is in use by the PV whose CSI handle names it, and the PV
// leads back to it: the live PV is the proof, never the names Longhorn last
// recorded.
func TestLonghornVolumeAndPVLinkThroughTheCSIHandle(t *testing.T) {
	vol := longhornVolume("longhorn-system", "vol-1")
	pv := csiPV("pv-a", "driver.longhorn.io", "vol-1")
	s := newProjectionSession(t, vol, pv,
		csiPV("pv-b", "driver.longhorn.io", "vol-2"),
		csiPV("pv-c", "other.csi.example.com", "vol-1"))

	rel, _ := longhornAnswer(t, s, vol, ownership.Unrestricted())
	if !slices.Equal(rel.Consumers, []ResourceRef{lhPVRef}) {
		t.Errorf("Volume consumers = %+v, want only pv-a (Longhorn driver, matching handle)", rel.Consumers)
	}
	rel, _ = longhornAnswer(t, s, pv, ownership.Unrestricted())
	if !slices.Equal(rel.StorageRefs, []ResourceRef{lhVolumeRef}) {
		t.Errorf("PV storage refs = %+v, want [%+v]", rel.StorageRefs, lhVolumeRef)
	}
}

// A handle alone doesn't prove the Volume exists.
func TestLonghornPVWithoutItsVolumeLinksNothing(t *testing.T) {
	orphan := csiPV("pv-b", "driver.longhorn.io", "vol-gone")
	s := newProjectionSession(t, longhornVolume("longhorn-system", "vol-1"), orphan)
	if rel, _ := longhornAnswer(t, s, orphan, ownership.Unrestricted()); len(rel.StorageRefs) != 0 {
		t.Errorf("PV whose Volume isn't there: storage refs = %+v, want none", rel.StorageRefs)
	}
}

// Two Volumes with the PV's handle (a leftover second install): the handle
// can't tell them apart, so neither direction links, also for a caller who
// may see only one of them.
func TestLonghornAmbiguousVolumeNameLinksNothing(t *testing.T) {
	a, b := longhornVolume("longhorn-system", "vol-1"), longhornVolume("longhorn-old", "vol-1")
	pv := csiPV("pv-a", "driver.longhorn.io", "vol-1")
	s := newProjectionSession(t, a, b, pv)
	for _, access := range []ownership.Access{ownership.Unrestricted(), projectionAccess{namespaces: []string{"longhorn-system"}}} {
		if rel, _ := longhornAnswer(t, s, pv, access); len(rel.StorageRefs) != 0 {
			t.Errorf("%T: PV storage refs = %+v, want none when two Volumes share the handle", access, rel.StorageRefs)
		}
		if rel, _ := longhornAnswer(t, s, a, access); len(rel.Consumers) != 0 {
			t.Errorf("%T: Volume consumers = %+v, want none when two Volumes share the name", access, rel.Consumers)
		}
	}
}

// Each Longhorn link is shown only when the caller may read what it names.
func TestLonghornLinksWithholdWhatTheCallerMayNotRead(t *testing.T) {
	vol := longhornVolume("longhorn-system", "vol-1")
	pv := csiPV("pv-a", "driver.longhorn.io", "vol-1")
	s := newProjectionSession(t, vol, pv)
	rel, coverage := longhornAnswer(t, s, pv, projectionAccess{denied: []string{"Volume"}})
	if len(rel.StorageRefs) != 0 {
		t.Errorf("Volume withheld: PV storage refs = %+v, want none", rel.StorageRefs)
	}
	if !coverage["special"].Narrowed {
		t.Errorf("Volume withheld: PV coverage = %+v, want narrowed", coverage)
	}
	if rel, _ := longhornAnswer(t, s, pv, projectionAccess{namespaces: []string{"shop"}}); len(rel.StorageRefs) != 0 {
		t.Errorf("Longhorn namespace not readable: PV storage refs = %+v, want none", rel.StorageRefs)
	}
	rel, coverage = longhornAnswer(t, s, vol, projectionAccess{denied: []string{"PersistentVolume"}})
	if len(rel.Consumers) != 0 {
		t.Errorf("PV withheld: Volume consumers = %+v, want none", rel.Consumers)
	}
	if !coverage["special"].Narrowed {
		t.Errorf("PV withheld: Volume coverage = %+v, want narrowed", coverage)
	}
}

// While the Volumes can't all be read (the cache warming up, a list error),
// uniqueness is unproven: neither direction links, and the answer says it
// couldn't check, without naming the namespace it couldn't read.
func TestLonghornUnreadableVolumesLinkNothingAndSaySo(t *testing.T) {
	vol := longhornVolume("longhorn-system", "vol-1")
	pv := csiPV("pv-a", "driver.longhorn.io", "vol-1")
	s := newProjectionSession(t, vol, pv)
	s.gaps = map[resourceid.GroupKind]ownership.Gap{longhornVolumeKind: {Kind: longhornVolumeKind, Namespace: "longhorn-old", Reason: readreason.KindSyncPending}}
	want := ownership.Gap{Kind: longhornVolumeKind, Reason: readreason.KindSyncPending}
	rel, coverage := longhornAnswer(t, s, pv, ownership.Unrestricted())
	if len(rel.StorageRefs) != 0 {
		t.Errorf("PV storage refs = %+v, want none while Volumes are unread", rel.StorageRefs)
	}
	if !slices.Contains(coverage["direct"].NotChecked, want) {
		t.Errorf("PV coverage = %+v, want %+v", coverage, want)
	}
	rel, coverage = longhornAnswer(t, s, vol, ownership.Unrestricted())
	if len(rel.Consumers) != 0 {
		t.Errorf("Volume consumers = %+v, want none while Volumes are unread", rel.Consumers)
	}
	if !slices.Contains(coverage["direct"].NotChecked, want) {
		t.Errorf("Volume coverage = %+v, want %+v", coverage, want)
	}
}

// A PV list that couldn't be read links nothing from the Volume and says so.
func TestLonghornUnreadablePVsLinkNothingAndSaySo(t *testing.T) {
	vol := longhornVolume("longhorn-system", "vol-1")
	s := newProjectionSession(t, vol, csiPV("pv-a", "driver.longhorn.io", "vol-1"))
	pvKind := resourceid.GroupKind{Kind: "PersistentVolume"}
	s.gaps = map[resourceid.GroupKind]ownership.Gap{pvKind: {Kind: pvKind, Reason: readreason.ReadError}}
	rel, coverage := longhornAnswer(t, s, vol, ownership.Unrestricted())
	if len(rel.Consumers) != 0 {
		t.Errorf("Volume consumers = %+v, want none from an unread PV list", rel.Consumers)
	}
	if !slices.Contains(coverage["direct"].NotChecked, ownership.Gap{Kind: pvKind, Reason: readreason.ReadError}) {
		t.Errorf("coverage = %+v, want the PV read error", coverage)
	}
}
