package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

// userWithGroups builds an authenticated user carrying the given groups, used
// to drive the Cloud-role gate (cloud:<tier> prefix).
func userWithGroups(groups ...string) *auth.User {
	return &auth.User{Username: "u@example.com", Groups: groups}
}

func putConfigStatus(t *testing.T, user *auth.User) (int, string) {
	t.Helper()
	// Redirect config persistence to a temp HOME so pass-through cases that
	// reach config.Update() don't clobber the developer's real ~/.radar.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	s := &Server{}
	r := httptest.NewRequest("PUT", "/api/config", strings.NewReader(`{}`))
	if user != nil {
		r = r.WithContext(auth.ContextWithUser(r.Context(), user))
	}
	w := httptest.NewRecorder()
	s.handlePutConfig(w, r)
	return w.Code, w.Body.String()
}

func TestPutConfig_RoleGate(t *testing.T) {
	// Non-owner Cloud roles are rejected with the stable error_code so the
	// SPA can branch on it.
	for _, tier := range []string{"cloud:viewer", "cloud:member"} {
		code, body := putConfigStatus(t, userWithGroups(tier))
		if code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403; body=%s", tier, code, body)
		}
		var resp map[string]string
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("%s: bad json: %v", tier, err)
		}
		if resp["error_code"] != auth.ErrCodeCloudRoleInsufficient {
			t.Errorf("%s: error_code = %q, want %q", tier, resp["error_code"], auth.ErrCodeCloudRoleInsufficient)
		}
	}
}

func TestPutConfig_OwnerAndOSSBypassRoleGate(t *testing.T) {
	// Owners and non-Cloud callers (no role group → OSS / OIDC / kubectl
	// plugin) must get past the role gate. They may still fail later writing
	// the config file, but they must NOT be 403'd by the gate — a single-user
	// laptop owns its own config.
	cases := []struct {
		name string
		user *auth.User
	}{
		{"owner", userWithGroups("cloud:owner")},
		{"no-role-groups", userWithGroups("devs")},
		{"no-user", nil},
	}
	for _, tc := range cases {
		code, body := putConfigStatus(t, tc.user)
		if code == http.StatusForbidden {
			t.Errorf("%s: got 403 from role gate, want pass-through; body=%s", tc.name, body)
		}
	}
}
