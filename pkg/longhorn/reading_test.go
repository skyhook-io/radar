package longhorn

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func engine(volume string, active bool, modes map[string]any) *unstructured.Unstructured {
	status := map[string]any{}
	if modes != nil {
		status["replicaModeMap"] = modes
	}
	return &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"volumeName": volume, "active": active}, "status": status}}
}

func TestHealthyReplicas(t *testing.T) {
	rw := map[string]any{"a": "RW", "b": "RW", "c": "ERR"}
	for _, c := range []struct {
		name    string
		engines []*unstructured.Unstructured
		read    bool
		want    int64
		unknown string
	}{
		{"one engine", []*unstructured.Unstructured{engine("v", false, rw), engine("other", true, nil)}, true, 2, ""},
		{"migrating, one active", []*unstructured.Unstructured{engine("v", false, map[string]any{"a": "RW"}), engine("v", true, rw)}, true, 2, ""},
		{"migrating, none active", []*unstructured.Unstructured{engine("v", false, rw), engine("v", false, rw)}, true, 0, ReplicasEngineAmbiguous},
		{"stopped engine", []*unstructured.Unstructured{engine("v", true, nil)}, true, 0, ReplicasNoModes},
		{"empty modes", []*unstructured.Unstructured{engine("v", true, map[string]any{})}, true, 0, ReplicasNoModes},
		{"no engine", nil, true, 0, ReplicasNoEngine},
		{"engines unread", []*unstructured.Unstructured{engine("v", true, rw)}, false, 0, ReplicasEngineUnread},
	} {
		got, unknown := healthyReplicas("v", c.engines, c.read)
		if unknown != c.unknown || (unknown == "") != (got != nil) || (got != nil && *got != c.want) {
			t.Errorf("%s: got %v %q, want %d %q", c.name, got, unknown, c.want, c.unknown)
		}
	}
}

func TestUseOf(t *testing.T) {
	pv := func(name, handle string, phase corev1.PersistentVolumePhase) *corev1.PersistentVolume {
		return &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: CSIDriver, VolumeHandle: handle}},
				ClaimRef:               &corev1.ObjectReference{Namespace: "app", Name: "data"},
			},
			Status: corev1.PersistentVolumeStatus{Phase: phase},
		}
	}
	all := func(string) bool { return true }
	if u := UseOf("v", []*corev1.PersistentVolume{pv("released", "v", corev1.VolumeReleased)}, all); u.State != UseBound || u.Claim != nil || u.ClaimWithheld {
		t.Errorf("a Released PV's old claim was named: %+v", u)
	}
	if u := UseOf("v", []*corev1.PersistentVolume{pv("a", "v", corev1.VolumeBound), pv("b", "v", corev1.VolumeBound)}, all); u.State != UseUnknown || u.PersistentVolume != "" {
		t.Errorf("two PVs on one handle: %+v, want unknown", u)
	}
}
