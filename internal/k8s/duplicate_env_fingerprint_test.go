package k8s

import "testing"

func TestDuplicateEnvVarFingerprintRoundTrip(t *testing.T) {
	want := DuplicateEnvVarFingerprint{
		Namespace:    "prod",
		WorkloadName: "web",
		Container:    "app",
		EnvName:      "APP:MODE",
	}

	encoded := FormatDuplicateEnvVarFingerprint(want.Namespace, want.WorkloadName, want.Container, want.EnvName)
	if encoded != "dup-env:prod:web:app:APP:MODE" {
		t.Fatalf("FormatDuplicateEnvVarFingerprint() = %q", encoded)
	}
	got, ok := ParseDuplicateEnvVarFingerprint(encoded)
	if !ok || got != want {
		t.Fatalf("ParseDuplicateEnvVarFingerprint(%q) = %+v, %t; want %+v, true", encoded, got, ok, want)
	}
}

func TestParseDuplicateEnvVarFingerprintRejectsOtherShapes(t *testing.T) {
	for _, fingerprint := range []string{
		"",
		"other:prod:web:app:APP_MODE",
		"dup-env:prod:web:APP_MODE",
	} {
		if got, ok := ParseDuplicateEnvVarFingerprint(fingerprint); ok {
			t.Fatalf("ParseDuplicateEnvVarFingerprint(%q) = %+v, true; want false", fingerprint, got)
		}
	}
}
