package helm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

// TestRequireCloudRole exercises the role gate without standing up a
// Helm client — the gate runs first, so we never hit the client. This
// guards the gate against regressions ("forgot to wire requireCloudRole
// on a new write endpoint", "swapped the comparison the wrong way").
func TestRequireCloudRole(t *testing.T) {
	cases := []struct {
		name     string
		groups   []string
		min      auth.CloudRole
		wantOK   bool
		wantCode string // expected error_code when wantOK == false
	}{
		// No user attached (OSS / non-Cloud): gate bypasses.
		{name: "no user — bypass", groups: nil, min: auth.RoleMember, wantOK: true},

		// Cloud user without role group (header stripped, misconfig):
		// CloudRole is RoleNone, AtLeast bypasses. This is permissive
		// by design — the assumption is that radar-hub guarantees the
		// role group when forwarding.
		{name: "user without cloud:* — bypass", groups: []string{"developers"}, min: auth.RoleOwner, wantOK: true},

		// Cloud-attributed users — gate enforces.
		{name: "viewer denied member-required op", groups: []string{"cloud:viewer"}, min: auth.RoleMember,
			wantOK: false, wantCode: "cloud_role_insufficient"},
		{name: "viewer denied owner-required op", groups: []string{"cloud:viewer"}, min: auth.RoleOwner,
			wantOK: false, wantCode: "cloud_role_insufficient"},
		{name: "viewer allowed viewer-required op", groups: []string{"cloud:viewer"}, min: auth.RoleViewer, wantOK: true},
		{name: "member allowed member-required op", groups: []string{"cloud:member"}, min: auth.RoleMember, wantOK: true},
		{name: "member denied owner-required op", groups: []string{"cloud:member"}, min: auth.RoleOwner,
			wantOK: false, wantCode: "cloud_role_insufficient"},
		{name: "owner allowed owner-required op", groups: []string{"cloud:owner"}, min: auth.RoleOwner, wantOK: true},

		// Defensive: a stuffed lower tier alongside owner shouldn't
		// downgrade the user. CloudRoleFromGroups picks the highest.
		{name: "stuffed viewer + owner — allowed", groups: []string{"cloud:viewer", "cloud:owner"}, min: auth.RoleOwner, wantOK: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			ctx := req.Context()
			if tc.groups != nil {
				ctx = auth.ContextWithUser(ctx, &auth.User{Username: "test-user", Groups: tc.groups})
			} else {
				_ = context.Background()
			}
			req = req.WithContext(ctx)
			rec := httptest.NewRecorder()

			ok := requireCloudRole(rec, req, tc.min, "test-op")
			if ok != tc.wantOK {
				t.Errorf("requireCloudRole = %v, want %v (status=%d, body=%s)",
					ok, tc.wantOK, rec.Code, rec.Body.String())
			}
			if !tc.wantOK {
				if rec.Code != http.StatusForbidden {
					t.Errorf("status = %d, want 403", rec.Code)
				}
				var resp map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("decode body: %v (body=%s)", err, rec.Body.String())
				}
				if resp["error_code"] != tc.wantCode {
					t.Errorf("error_code = %q, want %q", resp["error_code"], tc.wantCode)
				}
				if resp["error"] == "" {
					t.Error("error message is empty")
				}
			}
		})
	}
}
