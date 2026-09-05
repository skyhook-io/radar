package topology

import "testing"

func TestTargetRefMatchesTopologyKind(t *testing.T) {
	for _, test := range []struct {
		kind       string
		apiVersion string
		want       bool
	}{
		{kind: "Deployment", apiVersion: "apps/v1", want: true},
		{kind: "Deployment", want: true},
		{kind: "Rollout", apiVersion: "argoproj.io/v1alpha1", want: true},
		{kind: "Rollout", apiVersion: "rollouts.example.io/v1"},
		{kind: "Deployment", apiVersion: "apps.example.io/v1"},
		{kind: "Widget", apiVersion: "example.io/v1"},
	} {
		if got := targetRefMatchesTopologyKind(test.kind, test.apiVersion); got != test.want {
			t.Errorf("targetRefMatchesTopologyKind(%q, %q) = %v, want %v", test.kind, test.apiVersion, got, test.want)
		}
	}
}
