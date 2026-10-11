package configrefs

import "testing"

func TestGatewayReferenceDefaults(t *testing.T) {
	for _, tt := range []struct {
		name            string
		ref             map[string]any
		service, parent bool
	}{
		{name: "omitted group and kind", ref: map[string]any{"name": "x"}, service: true, parent: true},
		{name: "explicit core Service", ref: map[string]any{"group": "", "kind": "Service"}, service: true},
		{name: "custom-group Service", ref: map[string]any{"group": "custom.example.io", "kind": "Service"}},
		{name: "explicit Gateway", ref: map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway"}, parent: true},
		{name: "core group without kind", ref: map[string]any{"group": ""}, service: true},
		{name: "ListenerSet parent", ref: map[string]any{"group": "gateway.networking.k8s.io", "kind": "ListenerSet"}},
	} {
		if got := GatewayBackendIsService(tt.ref); got != tt.service {
			t.Errorf("%s: GatewayBackendIsService = %v, want %v", tt.name, got, tt.service)
		}
		if got := GatewayParentIsGateway(tt.ref); got != tt.parent {
			t.Errorf("%s: GatewayParentIsGateway = %v, want %v", tt.name, got, tt.parent)
		}
	}
	if got := GatewayRefNamespace(map[string]any{}, "route"); got != "route" {
		t.Errorf("omitted namespace = %q", got)
	}
	if got := GatewayRefNamespace(map[string]any{"namespace": "other"}, "route"); got != "other" {
		t.Errorf("explicit namespace = %q", got)
	}
}
