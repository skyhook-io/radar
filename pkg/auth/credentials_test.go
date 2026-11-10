package auth

import "testing"

func TestParseCredentialMode(t *testing.T) {
	for in, want := range map[string]CredentialMode{"": CredentialsImpersonate, "impersonate": CredentialsImpersonate, " user-token ": CredentialsUserToken} {
		got, err := ParseCredentialMode(in)
		if err != nil || got != want {
			t.Errorf("ParseCredentialMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseCredentialMode("token"); err == nil {
		t.Error("unknown mode accepted")
	}
}

func TestIsSystemIdentity(t *testing.T) {
	for _, tc := range []struct {
		user   string
		groups []string
		want   bool
	}{
		{"radar:system:hub-timeline-puller", []string{"radar:system"}, true},
		{"radar:ai:org_1", []string{"radar:ai:reader"}, true},
		{"radar:system:", []string{"radar:system"}, false},
		{"radar:ai:", []string{"radar:ai:reader"}, false},
		{"radar:system:x", []string{"radar:ai:reader"}, false},
		{"radar:ai:org_1", []string{"radar:system"}, false},
		{"radar:system:x", []string{"radar:system", "radar:owner"}, false},
		{"radar:system:x", nil, false},
		{"alice", []string{"radar:system"}, false},
	} {
		if got := IsSystemIdentity(tc.user, tc.groups); got != tc.want {
			t.Errorf("IsSystemIdentity(%q, %v) = %v, want %v", tc.user, tc.groups, got, tc.want)
		}
	}
}
