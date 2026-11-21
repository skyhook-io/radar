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

func TestGatewayParentRefInSpec(t *testing.T) {
	const routeNS = "app"
	spec := func(refs ...map[string]any) []any {
		out := make([]any, len(refs))
		for i, r := range refs {
			out[i] = r
		}
		return out
	}
	for _, tt := range []struct {
		name   string
		status map[string]any
		spec   []any
		want   bool
	}{
		{
			name:   "exact scoped match",
			status: map[string]any{"name": "gw", "namespace": "infra", "sectionName": "https"},
			spec:   spec(map[string]any{"name": "gw", "namespace": "infra", "sectionName": "https"}),
			want:   true,
		},
		{
			name:   "fully defaulted group/kind/namespace match",
			status: map[string]any{"name": "gw"},
			spec:   spec(map[string]any{"name": "gw", "namespace": routeNS}),
			want:   true,
		},
		{
			name:   "explicit core group differs from omitted group",
			status: map[string]any{"group": "", "kind": "Service", "name": "mesh"},
			spec:   spec(map[string]any{"name": "mesh"}),
			want:   false,
		},
		{
			name:   "explicit core Service matches explicit core Service",
			status: map[string]any{"group": "", "kind": "Service", "name": "mesh"},
			spec:   spec(map[string]any{"group": "", "kind": "Service", "name": "mesh"}),
			want:   true,
		},
		{
			name:   "different kind (ListenerSet vs Gateway) does not match",
			status: map[string]any{"kind": "ListenerSet", "name": "gw"},
			spec:   spec(map[string]any{"name": "gw"}),
			want:   false,
		},
		{
			name:   "omitted status namespace does not inherit explicit spec namespace",
			status: map[string]any{"name": "gw"},
			spec:   spec(map[string]any{"name": "gw", "namespace": "infra"}),
			want:   false,
		},
		{
			name:   "both omitted namespace resolve to route namespace",
			status: map[string]any{"name": "gw", "namespace": routeNS},
			spec:   spec(map[string]any{"name": "gw"}),
			want:   true,
		},
		{
			name:   "sectionName present in status only",
			status: map[string]any{"name": "gw", "sectionName": "https"},
			spec:   spec(map[string]any{"name": "gw"}),
			want:   false,
		},
		{
			name:   "sectionName present in spec only",
			status: map[string]any{"name": "gw"},
			spec:   spec(map[string]any{"name": "gw", "sectionName": "https"}),
			want:   false,
		},
		{
			name:   "equal port matches across int64 and float64",
			status: map[string]any{"name": "gw", "port": int64(443)},
			spec:   spec(map[string]any{"name": "gw", "port": float64(443)}),
			want:   true,
		},
		{
			name:   "differing port does not match",
			status: map[string]any{"name": "gw", "port": int64(443)},
			spec:   spec(map[string]any{"name": "gw", "port": int64(80)}),
			want:   false,
		},
		{
			name:   "port present in one only",
			status: map[string]any{"name": "gw", "port": int64(443)},
			spec:   spec(map[string]any{"name": "gw"}),
			want:   false,
		},
		{
			name:   "scoped status entry not re-admitted by unscoped spec ref",
			status: map[string]any{"name": "gw", "namespace": "infra", "sectionName": "old"},
			spec:   spec(map[string]any{"name": "gw", "namespace": "infra"}),
			want:   false,
		},
		{
			name:   "matches second spec entry",
			status: map[string]any{"name": "gw", "namespace": "infra", "sectionName": "b"},
			spec:   spec(map[string]any{"name": "gw", "namespace": "infra", "sectionName": "a"}, map[string]any{"name": "gw", "namespace": "infra", "sectionName": "b"}),
			want:   true,
		},
		{
			name:   "empty spec never matches",
			status: map[string]any{"name": "gw", "namespace": "infra"},
			spec:   nil,
			want:   false,
		},
		{
			name:   "empty status never matches",
			status: map[string]any{},
			spec:   spec(map[string]any{"name": "gw", "namespace": "infra"}),
			want:   false,
		},
	} {
		if got := GatewayParentRefInSpec(tt.status, tt.spec, routeNS); got != tt.want {
			t.Errorf("%s: GatewayParentRefInSpec = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestGatewayParentRefExpandsSpec(t *testing.T) {
	const routeNS = "app"
	spec := func(refs ...map[string]any) []any {
		out := make([]any, len(refs))
		for i, r := range refs {
			out[i] = r
		}
		return out
	}
	for _, tt := range []struct {
		name   string
		status map[string]any
		spec   []any
		want   bool
	}{
		{
			name:   "listener in status for an unscoped spec ref",
			status: map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": "gw", "namespace": routeNS, "sectionName": "web"},
			spec:   spec(map[string]any{"name": "gw"}),
			want:   true,
		},
		{
			name:   "port in status for an unscoped spec ref",
			status: map[string]any{"name": "gw", "port": int64(80)},
			spec:   spec(map[string]any{"name": "gw"}),
			want:   true,
		},
		{
			name:   "unscoped status is the exact case, not an expansion",
			status: map[string]any{"name": "gw"},
			spec:   spec(map[string]any{"name": "gw"}),
			want:   false,
		},
		{
			name:   "spec scoped by sectionName",
			status: map[string]any{"name": "gw", "sectionName": "http"},
			spec:   spec(map[string]any{"name": "gw", "sectionName": "https"}),
			want:   false,
		},
		{
			name:   "spec scoped by port only",
			status: map[string]any{"name": "gw", "sectionName": "web"},
			spec:   spec(map[string]any{"name": "gw", "port": int64(80)}),
			want:   false,
		},
		{
			name:   "different Gateway",
			status: map[string]any{"name": "gw-b", "sectionName": "web"},
			spec:   spec(map[string]any{"name": "gw-a"}),
			want:   false,
		},
		{
			name:   "different namespace",
			status: map[string]any{"name": "gw", "namespace": "infra", "sectionName": "web"},
			spec:   spec(map[string]any{"name": "gw"}),
			want:   false,
		},
		{
			name:   "mesh Service parent keeps exact matching",
			status: map[string]any{"group": "", "kind": "Service", "name": "svc", "port": int64(80)},
			spec:   spec(map[string]any{"group": "", "kind": "Service", "name": "svc"}),
			want:   false,
		},
		{
			name:   "matches the unscoped one of several spec refs",
			status: map[string]any{"name": "gw-b", "sectionName": "web"},
			spec:   spec(map[string]any{"name": "gw-a", "sectionName": "web"}, map[string]any{"name": "gw-b"}),
			want:   true,
		},
		{
			name:   "empty status",
			status: map[string]any{},
			spec:   spec(map[string]any{"name": "gw"}),
			want:   false,
		},
	} {
		if got := GatewayParentRefExpandsSpec(tt.status, tt.spec, routeNS); got != tt.want {
			t.Errorf("%s: GatewayParentRefExpandsSpec = %v, want %v", tt.name, got, tt.want)
		}
	}
}
