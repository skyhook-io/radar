package topology

import "testing"

func TestStampAuditKeys(t *testing.T) {
	nodes := []Node{
		{Kind: KindDeployment, Name: "api", Data: map[string]any{"namespace": "prod"}},
		{Kind: "IngressRoute", Name: "r", Data: map[string]any{"namespace": "web"}},     // CRD → group ""
		{Kind: KindIstioGateway, Name: "gw", Data: map[string]any{"namespace": "mesh"}}, // collision → real kind "Gateway"
		{Kind: KindNamespace, Name: "team-a", Data: nil},                                // nil Data + cluster-scoped (no ns)
	}

	out := stampAuditKeys(nodes)

	want := map[string]string{
		"api":    "apps|Deployment|prod|api",
		"r":      "|IngressRoute|web|r",
		"gw":     "|Gateway|mesh|gw", // remapped from KindIstioGateway, group still "" (audit convention)
		"team-a": "|Namespace||team-a",
	}
	for _, n := range out {
		got, _ := n.Data["auditKey"].(string)
		if got != want[n.Name] {
			t.Errorf("auditKey for %q = %q, want %q", n.Name, got, want[n.Name])
		}
	}
}
