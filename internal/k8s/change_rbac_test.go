package k8s

import "testing"

func TestGroupFromAPIVersion(t *testing.T) {
	cases := map[string]string{
		"v1":                           "",
		"apps/v1":                      "apps",
		"cluster.x-k8s.io/v1beta1":     "cluster.x-k8s.io",
		"rbac.authorization.k8s.io/v1": "rbac.authorization.k8s.io",
		"":                             "",
	}
	for in, want := range cases {
		if got := GroupFromAPIVersion(in); got != want {
			t.Errorf("GroupFromAPIVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// ResolveChangeGVR resolves cluster-only kinds from the static catalogue with no
// discovery wired — the path that must work before discovery warms up. Namespaced
// kinds need discovery (nil here) so they resolve to ok=false, which callers treat
// as fail-closed under auth. The group hint is authoritative: a hint disagreeing
// with a builtin's group is a CRD Kind collision and must NOT resolve to the
// builtin GVR (would authorize the CRD's rows against the builtin the user reads).
func TestResolveChangeGVR_StaticCatalogue(t *testing.T) {
	cases := []struct {
		name             string
		kind, group      string
		wantG, wantR     string
		wantClusterScope bool
		wantOK           bool
	}{
		{"core Node cluster-scoped", "Node", "", "", "nodes", true, true},
		{"ClusterRole matching group", "ClusterRole", "rbac.authorization.k8s.io", "rbac.authorization.k8s.io", "clusterroles", true, true},
		{"ClusterRole no hint", "ClusterRole", "", "rbac.authorization.k8s.io", "clusterroles", true, true},
		{"webhook cfg cluster-scoped", "ValidatingWebhookConfiguration", "", "admissionregistration.k8s.io", "validatingwebhookconfigurations", true, true},
		{"PersistentVolume cluster-scoped", "PersistentVolume", "", "", "persistentvolumes", true, true},
		// CRD colliding on Kind with a builtin: hint group disagrees → static
		// catalogue skipped, no discovery → fail closed (NOT the builtin GVR).
		{"ClusterRole CRD collision fails closed", "ClusterRole", "example.com", "", "", false, false},
		// Namespaced builtins resolve from the static catalogue (no discovery).
		{"Secret namespaced builtin", "Secret", "", "", "secrets", false, true},
		{"Deployment namespaced builtin", "Deployment", "apps", "apps", "deployments", false, true},
		{"Deployment collision fails closed", "Deployment", "example.com", "", "", false, false},
		// Unknown kind, no discovery → unresolved (fail closed downstream).
		{"unknown kind needs discovery", "WidgetCRD", "", "", "", false, false},
		{"empty kind", "", "", "", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, r, cs, ok := ResolveChangeGVR(tc.kind, tc.group)
			if ok != tc.wantOK || g != tc.wantG || r != tc.wantR || cs != tc.wantClusterScope {
				t.Errorf("ResolveChangeGVR(%q, %q) = (%q, %q, cs=%v, ok=%v), want (%q, %q, cs=%v, ok=%v)",
					tc.kind, tc.group, g, r, cs, ok, tc.wantG, tc.wantR, tc.wantClusterScope, tc.wantOK)
			}
		})
	}
}

// ChangeReadAllowed forces namespace "" for cluster-scoped kinds and fails closed
// on unresolved kinds; the authorize callback receives what the SAR would.
func TestChangeReadAllowed(t *testing.T) {
	// Node is cluster-scoped → authorize must be called with namespace "".
	var gotNs string
	allow := ChangeReadAllowed("Node", "v1", "default", func(group, resource, namespace string) bool {
		gotNs = namespace
		return true
	})
	if !allow || gotNs != "" {
		t.Fatalf("cluster-scoped Node: allow=%v ns=%q, want allow=true ns=\"\"", allow, gotNs)
	}

	// Namespaced builtin authorizes at the row's namespace.
	gotNs = "unset"
	ChangeReadAllowed("Secret", "v1", "team-a", func(_, _, namespace string) bool { gotNs = namespace; return true })
	if gotNs != "team-a" {
		t.Fatalf("namespaced Secret: authorize ns=%q, want \"team-a\"", gotNs)
	}

	// Unresolved kind (unknown CRD, no discovery) → fail closed, authorize never consulted.
	called := false
	if ChangeReadAllowed("WidgetCRD", "widgets.example.com/v1", "team-a", func(_, _, _ string) bool { called = true; return true }) {
		t.Fatal("unresolved kind should fail closed")
	}
	if called {
		t.Fatal("authorize must not be called for an unresolved kind")
	}
}
