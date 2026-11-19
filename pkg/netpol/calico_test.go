package netpol

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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

// Calico doesn't apply a preDNAT or doNotTrack policy to workload endpoints.
func TestCalicoHostOnlyPoliciesSelectNoWorkload(t *testing.T) {
	policy := func(spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "projectcalico.org/v3", "kind": "GlobalNetworkPolicy", "spec": spec}}
	}
	labels := map[string]string{"app": "web"}
	for _, tc := range []struct {
		name string
		spec map[string]any
		want bool
	}{
		{"an ordinary policy", map[string]any{"selector": "all()"}, true},
		{"preDNAT", map[string]any{"selector": "all()", "preDNAT": true, "applyOnForward": true}, false},
		{"doNotTrack", map[string]any{"selector": "all()", "doNotTrack": true, "applyOnForward": true}, false},
		{"both flags false", map[string]any{"selector": "all()", "preDNAT": false, "doNotTrack": false}, true},
	} {
		matcher := CompileCalicoPolicyMatcher(policy(tc.spec))
		matched, valid := matcher.Matches(labels, nil, "", nil)
		lazily, decided := matcher.MatchesLazily(labels, nil, "", nil)
		if matched != tc.want || !valid || lazily != tc.want || !decided {
			t.Errorf("%s: Matches = %v, %v; MatchesLazily = %v, %v; want %v, decided", tc.name, matched, valid, lazily, decided, tc.want)
		}
	}
}
