package netpol

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A deny-only spec governs the direction but can't allow anything in it.
func TestCiliumMayAllowIgnoresDenyOnlySpecs(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Labels: map[string]string{"app": "rec"}}}
	id := CiliumIdentityFor(pod, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}})
	policy := func(sections ...string) *unstructured.Unstructured {
		spec := map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"app": "rec"}}}
		for _, s := range sections {
			spec[s] = []any{map[string]any{"fromEntities": []any{"cluster"}}}
		}
		return &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	}
	denyAllForm := func(section string, rules []any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
			"endpointSelector": map[string]any{"matchLabels": map[string]any{"app": "rec"}},
			section:            rules,
		}}}
	}
	tests := []struct {
		name            string
		policy          *unstructured.Unstructured
		dir             Direction
		governs, allows bool
	}{
		{"ingress allow", policy("ingress"), DirectionIngress, true, true},
		{"ingress deny only", policy("ingressDeny"), DirectionIngress, true, false},
		{"egress allow, asked about ingress", policy("egress"), DirectionIngress, false, false},
		{"egress allow", policy("egress"), DirectionEgress, true, true},
		{"no rule sections", policy(), DirectionIngress, true, true},
		{"empty ingress rule is Cilium's deny-all", denyAllForm("ingress", []any{map[string]any{}}), DirectionIngress, true, false},
		{"empty ingress section", denyAllForm("ingress", []any{}), DirectionIngress, true, false},
		{"empty egress rule is Cilium's deny-all", denyAllForm("egress", []any{map[string]any{}}), DirectionEgress, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if governs, decided, _ := CiliumGoverns(tt.policy, id, tt.dir); !decided || governs != tt.governs {
				t.Errorf("CiliumGoverns = %v (decided %v), want %v", governs, decided, tt.governs)
			}
			if allows, decided, _ := CiliumMayAllow(tt.policy, id, tt.dir); !decided || allows != tt.allows {
				t.Errorf("CiliumMayAllow = %v (decided %v), want %v", allows, decided, tt.allows)
			}
		})
	}
}
