package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	integration "github.com/skyhook-io/radar/internal/integration"
)

func TestCNPGActionPartialOutcomeSerialization(t *testing.T) {
	err := &integration.ActionError{Status: http.StatusForbidden, Code: integration.ActionCodePartial, Message: "denied by admission policy", Completed: []string{"deleted PVC pg-2", "deleted PVC pg-2-wal"}}

	rec := httptest.NewRecorder()
	(&Server{}).writeCNPGActionError(rec, err, "destroyInstance", "db", "pg")
	body := rec.Body.String()
	if rec.Code != http.StatusForbidden || !strings.Contains(body, `"code":"partial"`) || !strings.Contains(body, `"completed":["deleted PVC pg-2","deleted PVC pg-2-wal"]`) {
		t.Errorf("response = %d %s", rec.Code, body)
	}
}

func TestCNPGClusterSessions_WaitsForPrimary(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds, withUID(cnpgObj("postgresql.cnpg.io/v1", "Cluster", "pgsessionswait", "analytics", map[string]any{"instances": int64(1)}, nil), "analytics-uid"))
	resp, err := http.Get(testServer.URL + "/api/cnpg/clusters/pgsessionswait/analytics/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got cnpgsvc.CNPGSessionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || got.State != "unavailable" || got.Error != "" || got.Reason != "Available once the primary is running" {
		t.Fatalf("%d: %+v", resp.StatusCode, got)
	}
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"pgsessionswait"}}
	perms.SetCanI("get", cnpgsvc.Group, "clusters", "pgsessionswait", true)
	perms.SetCanI("list", "", "pods", "pgsessionswait", true)
	perms.SetCanI("create", "", "pods/exec", "pgsessionswait", false)
	env.srv.permCache.Set("no-exec-before-primary", nil, perms)
	denied := env.authGet(t, "/api/cnpg/clusters/pgsessionswait/analytics/sessions", "no-exec-before-primary", "")
	defer denied.Body.Close()
	if err := json.NewDecoder(denied.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if denied.StatusCode != http.StatusOK || got.State != "denied" || got.Permission.Exec != integration.PermissionDenied {
		t.Fatalf("missing primary must not imply exec access: %d %+v", denied.StatusCode, got)
	}
}
