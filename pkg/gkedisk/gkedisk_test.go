package gkedisk

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHyperdiskOnly(t *testing.T) {
	for machineType, want := range map[string]bool{
		"n4d-standard-4":   true,
		"n4-custom-4-8192": true,
		"c4a-highcpu-8":    true,
		"C4-standard-2":    true,
		"e2-standard-2":    false,
		"n2d-standard-4":   false,
		"c3-standard-4":    false, // partial PD support: never flagged
		"":                 false,
	} {
		if got := HyperdiskOnly(machineType); got != want {
			t.Errorf("HyperdiskOnly(%q) = %v, want %v", machineType, got, want)
		}
	}
}

func TestParseIncompatibleAttach(t *testing.T) {
	msg := `AttachVolume.Attach failed for volume "pvc-d28294a7" : rpc error: code = Internal desc = Failed to Attach: failed cloud service attach disk call: googleapi: Error 400: pd-ssd disk type cannot be used by n4d-standard-4 machine type., badRequest`
	disk, machine, ok := ParseIncompatibleAttach(msg)
	if !ok || disk != "pd-ssd" || machine != "n4d-standard-4" {
		t.Fatalf("got %q %q %v", disk, machine, ok)
	}
	if _, _, ok := ParseIncompatibleAttach(`ControllerPublish not permitted on node "x" due to backoff condition`); ok {
		t.Fatal("backoff echo must not parse as an incompatibility")
	}
}

func TestIsAttachBackoffEcho(t *testing.T) {
	if !IsAttachBackoffEcho(`rpc error: code = InvalidArgument desc = ControllerPublish not permitted on node "projects/p/zones/z/instances/n" due to backoff condition`) {
		t.Fatal("expected backoff echo")
	}
	if IsAttachBackoffEcho("googleapi: Error 400: pd-ssd disk type cannot be used by n4d-standard-4 machine type.") {
		t.Fatal("real error must not be a backoff echo")
	}
}

func TestDiskType(t *testing.T) {
	classes := map[string]*storagev1.StorageClass{
		"premium-rwo":  {ObjectMeta: metav1.ObjectMeta{Name: "premium-rwo"}, Provisioner: CSIDriver, Parameters: map[string]string{"type": "pd-ssd"}},
		"standard":     {ObjectMeta: metav1.ObjectMeta{Name: "standard"}, Provisioner: InTreeProvision},
		"hyperdisk":    {ObjectMeta: metav1.ObjectMeta{Name: "hyperdisk"}, Provisioner: CSIDriver, Parameters: map[string]string{"type": "hyperdisk-balanced"}},
		"mixed-case":   {ObjectMeta: metav1.ObjectMeta{Name: "mixed-case"}, Provisioner: CSIDriver, Parameters: map[string]string{"Type": "hyperdisk-balanced"}},
		"ebs":          {ObjectMeta: metav1.ObjectMeta{Name: "ebs"}, Provisioner: "ebs.csi.aws.com", Parameters: map[string]string{"type": "gp3"}},
		"mismatch-csi": {ObjectMeta: metav1.ObjectMeta{Name: "mismatch-csi"}, Provisioner: "ebs.csi.aws.com"},
	}
	lookup := func(name string) *storagev1.StorageClass { return classes[name] }
	csiPV := func(sc string) *corev1.PersistentVolume {
		return &corev1.PersistentVolume{ObjectMeta: provisioned(CSIDriver), Spec: corev1.PersistentVolumeSpec{
			StorageClassName:       sc,
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: CSIDriver}},
		}}
	}
	inTreePV := &corev1.PersistentVolume{ObjectMeta: provisioned(InTreeProvision), Spec: corev1.PersistentVolumeSpec{
		StorageClassName:       "standard",
		PersistentVolumeSource: corev1.PersistentVolumeSource{GCEPersistentDisk: &corev1.GCEPersistentDiskVolumeSource{PDName: "d"}},
	}}
	staticPV := csiPV("premium-rwo")
	staticPV.Annotations = nil
	ebsPV := &corev1.PersistentVolume{ObjectMeta: provisioned("ebs.csi.aws.com"), Spec: corev1.PersistentVolumeSpec{
		StorageClassName:       "ebs",
		PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "ebs.csi.aws.com"}},
	}}

	cases := []struct {
		name   string
		pv     *corev1.PersistentVolume
		want   string
		wantOK bool
	}{
		{"csi pd-ssd", csiPV("premium-rwo"), "pd-ssd", true},
		{"csi hyperdisk", csiPV("hyperdisk"), "hyperdisk-balanced", true},
		{"csi case-insensitive parameter key", csiPV("mixed-case"), "hyperdisk-balanced", true},
		{"in-tree default type", inTreePV, "pd-standard", true},
		{"static PV borrowing a class name", staticPV, "", false},
		{"other cloud", ebsPV, "", false},
		{"unknown class", csiPV("gone"), "", false},
		{"class from another provisioner", csiPV("mismatch-csi"), "", false},
		{"no class", csiPV(""), "", false},
		{"nil", nil, "", false},
	}
	for _, tc := range cases {
		got, ok := DiskType(tc.pv, lookup)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("%s: DiskType = %q, %v; want %q, %v", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}

// A rejected Hyperdisk type must not be told its machine "supports only
// Hyperdisk", nor be advised to move to Hyperdisk.
func TestIncompatibilityForHyperdiskTypes(t *testing.T) {
	cause := IncompatibilityCause("hyperdisk-extreme", "n4-standard-4")
	if strings.Contains(cause, "only Hyperdisk") || !strings.Contains(cause, "hyperdisk-extreme disks on n4-standard-4") {
		t.Errorf("cause = %q", cause)
	}
	action := IncompatibilityAction("hyperdisk-balanced")
	if strings.Contains(action, "Hyperdisk StorageClass") || !strings.Contains(action, "supports hyperdisk-balanced") {
		t.Errorf("action = %q", action)
	}
	if !strings.Contains(IncompatibilityAction("pd-ssd"), "Hyperdisk StorageClass") {
		t.Error("PD types should still be offered the Hyperdisk move")
	}
}

func provisioned(by string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Annotations: map[string]string{"pv.kubernetes.io/provisioned-by": by}}
}

func TestIncompatibilityCause(t *testing.T) {
	if got := IncompatibilityCause("pd-ssd", "n4d-standard-4"); !strings.Contains(got, "N4D machine series supports only Hyperdisk") {
		t.Errorf("Hyperdisk-only cause = %q", got)
	}
	if got := IncompatibilityCause("pd-standard", "c3-standard-4"); strings.Contains(got, "only Hyperdisk") || !strings.Contains(got, "pd-standard disks on c3-standard-4") {
		t.Errorf("partial-support cause must not claim Hyperdisk-only: %q", got)
	}
}
