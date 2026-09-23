package topology

import "testing"

func TestStampAuditKeys(t *testing.T) {
	nodes := []Node{
		{Kind: "Role", Name: "cloud-role", Data: map[string]any{"apiVersion": "iam.aws.upbound.io/v1beta1"}},
		{Kind: KindDeployment, Name: "api", Data: map[string]any{"namespace": "prod", "apiVersion": "apps/v1"}},
		{Kind: "IngressRoute", Name: "r", Data: map[string]any{"namespace": "web", "apiVersion": "traefik.io/v1alpha1"}}, // CRD → group ""
		{Kind: KindIstioGateway, Name: "gw", Data: map[string]any{"namespace": "mesh"}},                                  // collision → real kind "Gateway"
		{Kind: KindNamespace, Name: "team-a", Data: nil},                                                                 // nil Data + cluster-scoped (no ns)
	}

	out := stampAuditKeys(nodes)

	want := map[string]string{
		"cloud-role": "iam.aws.upbound.io|Role||cloud-role",
		"api":        "apps|Deployment|prod|api",
		"r":          "traefik.io|IngressRoute|web|r",
		"gw":         "|Gateway|mesh|gw", // remapped from KindIstioGateway, group still "" (audit convention)
		"team-a":     "|Namespace||team-a",
	}
	for _, n := range out {
		got, _ := n.Data["auditKey"].(string)
		if got != want[n.Name] {
			t.Errorf("auditKey for %q = %q, want %q", n.Name, got, want[n.Name])
		}
	}
	if got := out[3].Data["resourceKind"]; got != "Gateway" {
		t.Errorf("resourceKind for Istio Gateway = %v, want Gateway", got)
	}
	if _, ok := out[1].Data["resourceKind"]; ok {
		t.Error("ordinary topology node unexpectedly received resourceKind")
	}
}
