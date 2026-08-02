package auth

import "testing"

func TestForwardedIdentityAllowed(t *testing.T) {
	cases := []struct {
		name      string
		username  string
		groups    []string
		cloudMode bool
		want      bool
	}{
		// Outside Cloud: operator owns the identity chain — never filtered,
		// including reserved principals a real admin legitimately carries.
		{"non-cloud: ordinary identity allowed", "alice@company.com", []string{"sre-team"}, false, true},
		{"non-cloud: system:masters allowed (operator's cluster)", "admin", []string{"system:masters"}, false, true},
		{"non-cloud: unprefixed groups allowed", "alice", []string{"platform", "sre"}, false, true},

		// Cloud: reserved-principal rejection.
		{"cloud: system:masters group rejected", "user_01ABC", []string{"radar:owner", "system:masters"}, true, false},
		{"cloud: system username rejected", "system:node:x", []string{"radar:owner"}, true, false},
		{"cloud: serviceaccount group rejected", "user_01ABC", []string{"system:serviceaccounts:kube-system"}, true, false},

		// Cloud: vocabulary allowlist.
		{"cloud: radar tier allowed", "user_01ABC", []string{"radar:owner", "radar:org:o1", "radar:user:u1"}, true, true},
		{"cloud: legacy cloud tier allowed", "user_01ABC", []string{"cloud:viewer", "cloud:org:o1"}, true, true},
		{"cloud: radar:idp group allowed", "user_01ABC", []string{"radar:idp:f81d4fae"}, true, true},
		{"cloud: radar:email allowed", "user_01ABC", []string{"radar:email:a@b.com"}, true, true},
		{"cloud: non-vocabulary group rejected", "user_01ABC", []string{"radar:owner", "sre-team"}, true, false},
		{"cloud: empty username rejected", "", []string{"radar:owner"}, true, false},
		{"cloud: internal synthetic username allowed", "cloud:system:alerts:o1", []string{"radar:owner"}, true, true},
		{"cloud: no groups, valid user, allowed", "user_01ABC", nil, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ForwardedIdentityAllowed(tc.username, tc.groups, tc.cloudMode)
			if got != tc.want {
				t.Errorf("ForwardedIdentityAllowed(%q, %v, cloud=%v) = %v, want %v",
					tc.username, tc.groups, tc.cloudMode, got, tc.want)
			}
		})
	}
}

func TestIsReservedPrincipal(t *testing.T) {
	reserved := []string{"system:masters", "system:authenticated", "system:node:x", "system:serviceaccount:kube-system:default", "system:serviceaccounts"}
	for _, p := range reserved {
		if !IsReservedPrincipal(p) {
			t.Errorf("IsReservedPrincipal(%q) = false, want true", p)
		}
	}
	// Radar's own namespaced synthetic principals must NOT be treated as reserved.
	notReserved := []string{"cloud:system:alerts:o1", "cloud:system", "radar:owner", "alice@company.com", "sre-team", ""}
	for _, p := range notReserved {
		if IsReservedPrincipal(p) {
			t.Errorf("IsReservedPrincipal(%q) = true, want false", p)
		}
	}
}
