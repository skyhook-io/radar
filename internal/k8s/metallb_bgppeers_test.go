package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestMetalLBBGPPeerGVR(t *testing.T) {
	if err := InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), []APIResource{
		{Group: "metallb.io", Version: "v1beta2", Kind: "BGPPeer", Name: "bgppeers", Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}},
		{Group: "metallb.io", Version: "v1beta1", Kind: "IPAddressPool", Name: "ipaddresspools", Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetTestDynamicState)

	for _, tt := range []struct {
		kind, group  string
		wantResource string
		wantOK       bool
	}{
		{"bgppeers", "metallb.io", "bgppeers", true},
		{"BGPPeer", "", "bgppeers", true},
		{"ipaddresspools", "metallb.io", "", false},
		{"bgppeers", "crd.projectcalico.org", "", false},
		{"pods", "", "", false},
	} {
		gvr, ok := MetalLBBGPPeerGVR(tt.kind, tt.group)
		if ok != tt.wantOK || gvr.Resource != tt.wantResource {
			t.Errorf("MetalLBBGPPeerGVR(%q, %q) = %q, %v; want %q, %v", tt.kind, tt.group, gvr.Resource, ok, tt.wantResource, tt.wantOK)
		}
		group, resource := ExactNamespaceAccessResource(tt.kind, tt.group)
		if (resource != "") != tt.wantOK || tt.wantOK && group != "metallb.io" {
			t.Errorf("ExactNamespaceAccessResource(%q, %q) = %s/%s, want exact gate %v", tt.kind, tt.group, group, resource, tt.wantOK)
		}
	}
}

// Before discovery knows MetalLB, a read naming its group is still checked.
func TestMetalLBBGPPeerGVRUndiscovered(t *testing.T) {
	if err := InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetTestDynamicState)
	for _, kind := range []string{"bgppeers", "BGPPeer"} {
		if gvr, ok := MetalLBBGPPeerGVR(kind, "metallb.io"); !ok || gvr.Resource != "bgppeers" {
			t.Errorf("%s = %q, %v; want bgppeers, true", kind, gvr.Resource, ok)
		}
	}
	if _, ok := MetalLBBGPPeerGVR("bgppeers", ""); ok {
		t.Error("an undiscovered kind with no group resolved to MetalLB")
	}
}
