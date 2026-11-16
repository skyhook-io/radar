package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

// pinNamespaceScope starts Radar as `--namespace-scope` on namespace "apps",
// so the Velero installation in "velero" is outside it.
func pinNamespaceScope(t *testing.T) {
	t.Helper()
	previous := k8s.ForceNamespaceScope
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("apps")
	t.Cleanup(func() { k8s.ForceNamespaceScope = previous; k8s.SetFallbackNamespace("") })
}

func assertOutsidePin(t *testing.T, resp *http.Response, body string) {
	t.Helper()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "namespace scope") {
		t.Fatalf("status = %d, body %q; want 403 naming Radar's namespace scope", resp.StatusCode, body)
	}
}

// The pin is Radar's own bound, separate from the caller's RBAC: a caller
// allowed everywhere still gets nothing outside it, and nothing is created.
func TestVeleroRoutesRefuseNamespacesOutsideThePin(t *testing.T) {
	pinNamespaceScope(t)

	t.Run("a run's messages", func(t *testing.T) {
		env := newAuthTestServer(t)
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"velero"}}
		perms.SetCanI("get", "velero.io", "backups", "velero", true)
		env.srv.permCache.Set("alice", nil, perms)

		resp := env.authPost(t, "/api/velero/backups/velero/nightly/messages", "alice", "", "")
		defer resp.Body.Close()
		assertOutsidePin(t, resp, readBody(t, resp))
	})

	t.Run("the backups a storage location holds", func(t *testing.T) {
		env := newAuthTestServer(t)
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"velero"}}
		perms.SetCanI("list", "velero.io", "backups", "velero", true)
		env.srv.permCache.Set("alice", nil, perms)

		resp := env.authGet(t, "/api/velero/backupstoragelocations/velero/default/backups", "alice", "")
		defer resp.Body.Close()
		assertOutsidePin(t, resp, readBody(t, resp))
	})

	t.Run("delete backup", func(t *testing.T) {
		srv, dyn := deleteServer(t, deletableBackup("nightly-1", "uid-1", "Completed", "default"), storageLocation("default", "ReadWrite", "Available"))
		rec := serve(srv, http.MethodPost, "/api/velero/backups/velero/nightly-1/delete", `{"uid":"uid-1"}`)
		resp := rec.Result()
		defer resp.Body.Close()
		assertOutsidePin(t, resp, rec.Body.String())
		if n := len(deleteRequests(t, dyn)); n != 0 {
			t.Errorf("%d delete requests created outside the pin", n)
		}
	})

	t.Run("a deletion's status", func(t *testing.T) {
		srv, _ := deleteServer(t)
		rec := serve(srv, http.MethodGet, "/api/velero/backups/velero/nightly-1/deletion?uid=u", "")
		resp := rec.Result()
		defer resp.Body.Close()
		assertOutsidePin(t, resp, rec.Body.String())
	})

	t.Run("back up now", func(t *testing.T) {
		srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
		rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/backup", `{"resourceVersion":"7"}`, nil)
		resp := rec.Result()
		defer resp.Body.Close()
		assertOutsidePin(t, resp, rec.Body.String())
		if n := createdBackups(dyn); n != 0 {
			t.Errorf("%d backups created outside the pin", n)
		}
	})
}

func TestVeleroScheduleActivityRefusesPinBeforeDiscovery(t *testing.T) {
	pinNamespaceScope(t)
	srv, _ := backupNowServer(t)
	rec := serve(srv, http.MethodGet, "/api/velero/schedules/velero/nightly/activity", "")
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "namespace scope") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
