package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestKagentCredentialGVR(t *testing.T) {
	if err := InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), []APIResource{
		{Group: "api.kagent.dev", Version: "v1alpha3", Kind: "RemoteMCPServer", Name: "remotemcpservers", Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}},
		{Group: "api.kagent.dev", Version: "v1alpha3", Kind: "ModelConfig", Name: "modelconfigs", Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}},
		{Group: "api.kagent.dev", Version: "v1alpha3", Kind: "Agent", Name: "agents", Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}},
		{Group: "kagent.dev", Version: "v1alpha2", Kind: "ModelConfig", Name: "modelconfigs", Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetTestDynamicState)

	for _, tt := range []struct {
		kind, group  string
		wantResource string
		wantOK       bool
	}{
		{"remotemcpservers", "api.kagent.dev", "remotemcpservers", true},
		{"RemoteMCPServer", "", "remotemcpservers", true},
		{"modelconfigs", "api.kagent.dev", "modelconfigs", true},
		{"ModelConfig", "api.kagent.dev", "modelconfigs", true},
		{"agents", "api.kagent.dev", "", false},
		{"modelconfigs", "kagent.dev", "", false},
		{"pods", "", "", false},
	} {
		gvr, ok := KagentCredentialGVR(tt.kind, tt.group)
		if ok != tt.wantOK || gvr.Resource != tt.wantResource {
			t.Errorf("KagentCredentialGVR(%q, %q) = %q, %v; want %q, %v", tt.kind, tt.group, gvr.Resource, ok, tt.wantResource, tt.wantOK)
		}
		group, resource := ExactNamespaceAccessResource(tt.kind, tt.group)
		if (resource != "") != tt.wantOK || tt.wantOK && group != "api.kagent.dev" {
			t.Errorf("ExactNamespaceAccessResource(%q, %q) = %s/%s, want exact gate %v", tt.kind, tt.group, group, resource, tt.wantOK)
		}
	}
}

// Before discovery knows kagent, a read naming its group is still checked.
func TestKagentCredentialGVRUndiscovered(t *testing.T) {
	if err := InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetTestDynamicState)
	for kind, want := range map[string]string{"remotemcpservers": "remotemcpservers", "RemoteMCPServer": "remotemcpservers", "ModelConfig": "modelconfigs", "modelconfigs": "modelconfigs"} {
		if gvr, ok := KagentCredentialGVR(kind, "api.kagent.dev"); !ok || gvr.Resource != want {
			t.Errorf("%s = %q, %v; want %s, true", kind, gvr.Resource, ok, want)
		}
	}
	if _, ok := KagentCredentialGVR("modelconfigs", ""); ok {
		t.Error("an undiscovered kind with no group resolved to kagent")
	}
	if _, ok := KagentCredentialGVR("agents", "api.kagent.dev"); ok {
		t.Error("an undiscovered Agent read was gated as a credential kind")
	}
}
