package netpol

import "testing"

func TestCalicoPolicySelectorsFailClosed(t *testing.T) {
	labels := map[string]string{"app": "frontend", "tier": "web"}
	tests := []struct {
		expression string
		want       bool
		valid      bool
	}{
		{"all()", true, true},
		{"app == 'frontend' && tier in {'web', 'api'}", true, true},
		{"app == 'backend' || has(tier)", true, true},
		{"app ==", false, false},
		{"app ??? 'frontend'", false, false},
	}
	for _, test := range tests {
		expression, valid := compileCalicoSelector(test.expression)
		if valid != test.valid {
			t.Errorf("compileCalicoSelector(%q) valid = %v, want %v", test.expression, valid, test.valid)
		}
		if valid && expression(labels) != test.want {
			t.Errorf("compileCalicoSelector(%q)(labels) = %v, want %v", test.expression, expression(labels), test.want)
		}
	}
}
