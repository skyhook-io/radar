package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

func TestRequireCloudRole_NoTier(t *testing.T) {
	cases := []struct {
		name   string
		cloud  string
		groups []string
		want   bool
	}{
		{"OSS caller without a tier keeps its own config", "false", []string{"oidc:devs"}, true},
		{"Cloud system identity has no tier", "true", []string{"radar:system", "radar:org:o1"}, false},
		{"Cloud owner", "true", []string{"radar:owner", "radar:org:o1"}, true},
		{"Cloud member below owner", "true", []string{"radar:member", "radar:org:o1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RADAR_CLOUD_MODE", tc.cloud)
			req := httptest.NewRequest(http.MethodPut, "/api/settings", nil)
			req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "u", Groups: tc.groups}))
			rec := httptest.NewRecorder()
			if got := (&Server{}).requireCloudRole(rec, req, auth.RoleOwner, "modify Radar configuration"); got != tc.want {
				t.Fatalf("allowed = %v, want %v (body %s)", got, tc.want, rec.Body.String())
			}
			if !tc.want && !strings.Contains(rec.Body.String(), auth.ErrCodeCloudRoleInsufficient) {
				t.Errorf("denial must carry %s: %s", auth.ErrCodeCloudRoleInsufficient, rec.Body.String())
			}
		})
	}
}

// OCI chart sources feed upgrade discovery for every user, so a Cloud user
// with no cluster RBAC must not be able to add one.
func TestHelmSourceConfigWrite_RequiresCloudOwner(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "true")
	for groups, want := range map[string]bool{
		"radar:viewer,radar:org:o1": false,
		"radar:member,radar:org:o1": false,
		"radar:owner,radar:org:o1":  true,
	} {
		t.Run(groups, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/helm/oci-sources", nil)
			req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "u", Groups: strings.Split(groups, ",")}))
			rec := httptest.NewRecorder()
			if got := (&Server{}).requireHelmSourceConfigWrite(rec, req); got != want {
				t.Fatalf("allowed = %v, want %v (body %s)", got, want, rec.Body.String())
			}
		})
	}
}
