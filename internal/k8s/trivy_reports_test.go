package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestTrivyReportGVR(t *testing.T) {
	if err := InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), []APIResource{{
		Group: "aquasecurity.github.io", Version: "v1alpha1", Kind: "VulnerabilityReport", Name: "vulnerabilityreports",
		Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetTestDynamicState)

	tests := []struct {
		kind, group  string
		wantResource string
		wantOK       bool
	}{
		{"vulnerabilityreports", "aquasecurity.github.io", "vulnerabilityreports", true},
		{"VulnerabilityReport", "", "vulnerabilityreports", true},
		{"pods", "", "", false},
		{"vulnerabilityreports", "example.com", "", false},
		// Not discovered yet: still checked, under the name as given.
		{"ExposedSecretReports", "aquasecurity.github.io", "exposedsecretreports", true},
		{"exposedsecretreports", "", "", false},
	}
	for _, tt := range tests {
		gvr, ok := TrivyReportGVR(tt.kind, tt.group)
		if ok != tt.wantOK || gvr.Resource != tt.wantResource {
			t.Errorf("TrivyReportGVR(%q, %q) = %q, %v; want %q, %v", tt.kind, tt.group, gvr.Resource, ok, tt.wantResource, tt.wantOK)
		}
	}
}
