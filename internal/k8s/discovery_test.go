package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestIsMoreStableVersion(t *testing.T) {
	tests := []struct {
		newVer string
		oldVer string
		want   bool
	}{
		{"v1", "v1alpha1", true},
		{"v1", "v1beta1", true},
		{"v1beta1", "v1alpha1", true},
		{"v1alpha1", "v1beta1", false},
		{"v1beta1", "v1", false},
		{"v2", "v1", true},
		{"v1", "v2", false},
		{"v1beta2", "v1beta1", true},
		{"v1beta1", "v1beta2", false},
	}
	for _, tt := range tests {
		t.Run(tt.newVer+"_vs_"+tt.oldVer, func(t *testing.T) {
			got := isMoreStableVersion(tt.newVer, tt.oldVer)
			if got != tt.want {
				t.Errorf("isMoreStableVersion(%q, %q) = %v, want %v", tt.newVer, tt.oldVer, got, tt.want)
			}
		})
	}
}

func TestGetGVRWithGroup_DisambiguatesSameKind(t *testing.T) {
	// Simulate two CRDs with the same Kind but different groups:
	// - argoproj.io/v1alpha1 Application
	// - app.k8s.io/v1beta1 Application
	d := &ResourceDiscovery{
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
		resources: []APIResource{
			{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application", Name: "applications", Namespaced: true, IsCRD: true},
			{Group: "app.k8s.io", Version: "v1beta1", Kind: "Application", Name: "applications", Namespaced: true, IsCRD: true},
		},
	}

	// GetGVRWithGroup should return the correct group regardless of map contents
	gvr, ok := d.GetGVRWithGroup("Application", "argoproj.io")
	if !ok {
		t.Fatal("expected to find Application in argoproj.io")
	}
	if gvr.Group != "argoproj.io" {
		t.Errorf("expected group argoproj.io, got %s", gvr.Group)
	}
	if gvr.Version != "v1alpha1" {
		t.Errorf("expected version v1alpha1, got %s", gvr.Version)
	}

	gvr, ok = d.GetGVRWithGroup("Application", "app.k8s.io")
	if !ok {
		t.Fatal("expected to find Application in app.k8s.io")
	}
	if gvr.Group != "app.k8s.io" {
		t.Errorf("expected group app.k8s.io, got %s", gvr.Group)
	}
	if gvr.Version != "v1beta1" {
		t.Errorf("expected version v1beta1, got %s", gvr.Version)
	}

	// Non-existent group should return false
	_, ok = d.GetGVRWithGroup("Application", "nonexistent.io")
	if ok {
		t.Error("expected not to find Application in nonexistent.io")
	}
}
