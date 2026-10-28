package configrefs

import "testing"

func TestEffectiveInstrumentationAnnotationNativePrecedence(t *testing.T) {
	for _, tc := range []struct {
		namespace, pod, want string
		fromNamespace        bool
	}{
		{"", "", "", false}, {"", "true", "true", false}, {"shared/java", "", "shared/java", true},
		{"shared/java", "true", "shared/java", true}, {"true", "named", "named", false},
		{"shared/java", "false", "false", false}, {"false", "true", "true", false},
		{"true", "TRUE", "true", true}, {"false", "FALSE", "FALSE", false},
	} {
		got, ns := EffectiveInstrumentationAnnotation(map[string]string{"key": tc.namespace}, map[string]string{"key": tc.pod}, "key")
		if got != tc.want || ns != tc.fromNamespace {
			t.Fatalf("ns=%q pod=%q: got %q %v, want %q %v", tc.namespace, tc.pod, got, ns, tc.want, tc.fromNamespace)
		}
	}
}
