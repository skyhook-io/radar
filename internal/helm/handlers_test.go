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
			wantOK: false, wantCode: auth.ErrCodeCloudRoleInsufficient},
		{name: "viewer denied owner-required op", groups: []string{"cloud:viewer"}, min: auth.RoleOwner,
			wantOK: false, wantCode: auth.ErrCodeCloudRoleInsufficient},
		{name: "viewer allowed viewer-required op", groups: []string{"cloud:viewer"}, min: auth.RoleViewer, wantOK: true},
		{name: "member allowed member-required op", groups: []string{"cloud:member"}, min: auth.RoleMember, wantOK: true},
		{name: "member denied owner-required op", groups: []string{"cloud:member"}, min: auth.RoleOwner,
			wantOK: false, wantCode: auth.ErrCodeCloudRoleInsufficient},
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

// TestSensitiveHelmHandlers_GateOnViewer asserts that every Helm
// handler we believe is gated actually 403s a Cloud viewer with
// error_code=cloud_role_insufficient. The unit test above
// (TestRequireCloudRole) covers the gate function in isolation; this
// test covers the *application* of the gate to each handler.
//
// Why both: the gate was retrofitted as a one-line `if !requireCloudRole(...)
// { return }` at the top of each handler. A future refactor that drops
// the line (or reorders it after a side-effecting call) would silently
// regress without the unit test failing. This test fails the moment
// the gate disappears from any covered handler.
//
// We don't need a real Helm client because the gate runs first; the
// handler short-circuits before touching `helm.GetClient()`.
func TestSensitiveHelmHandlers_GateOnViewer(t *testing.T) {
	h := NewHandlers(nil)

	// (name, method, handler) for every endpoint we believe is gated.
	// Sensitive reads → member; writes → member.
	cases := []struct {
		name    string
		method  string
		handler http.HandlerFunc
	}{
		{"GetManifest", http.MethodGet, h.handleGetManifest},
		{"GetValues", http.MethodGet, h.handleGetValues},
		{"GetDiff", http.MethodGet, h.handleGetDiff},
		{"GetNotesDiff", http.MethodGet, h.handleGetNotesDiff},
		{"GetHooksDiff", http.MethodGet, h.handleGetHooksDiff},
		{"GetResourceDiff", http.MethodGet, h.handleGetResourceDiff},
		{"PreviewValues", http.MethodPost, h.handlePreviewValues},
		{"ApplyValues", http.MethodPut, h.handleApplyValues},
		{"Rollback", http.MethodPost, h.handleRollback},
		{"RollbackStream", http.MethodPost, h.handleRollbackStream},
		{"Uninstall", http.MethodDelete, h.handleUninstall},
		{"Upgrade", http.MethodPost, h.handleUpgrade},
		{"UpgradeStream", http.MethodPost, h.handleUpgradeStream},
		{"Install", http.MethodPost, h.handleInstall},
		{"InstallStream", http.MethodPost, h.handleInstallStream},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/test", nil)
			req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{
				Username: "viewer-test",
				Groups:   []string{"cloud:viewer"},
			}))
			rec := httptest.NewRecorder()

			tc.handler(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (gate should fire before any helm work)", rec.Code)
			}
			var resp map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode body: %v (body=%s)", err, rec.Body.String())
			}
			if resp["error_code"] != auth.ErrCodeCloudRoleInsufficient {
				t.Errorf("error_code = %q, want %q", resp["error_code"], auth.ErrCodeCloudRoleInsufficient)
			}
		})
	}
}
