package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// A discovery refresh in a test environment finds the resources the test
// registered again, rather than dropping them.
func TestTestDiscoverySurvivesRefresh(t *testing.T) {
	if err := InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), []APIResource{{
		Group: "aquasecurity.github.io", Version: "v1alpha1", Kind: "VulnerabilityReport", Name: "vulnerabilityreports",
		Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetTestDynamicState)
	d := GetResourceDiscovery()
	if _, ok := d.GetGVRWithGroup("VulnerabilityReport", "aquasecurity.github.io"); !ok {
		t.Fatal("registered kind not resolvable")
	}
	if err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.GetGVRWithGroup("VulnerabilityReport", "aquasecurity.github.io"); !ok {
		t.Fatal("a refresh dropped the registered kind")
	}
}
