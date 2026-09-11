package server

import (
	"net/http"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

// Per-user authorization on POST /api/velero/{kind}/{ns}/{name}/messages.
//
// Invisible without auth, like the other gates here: canRead returns true for
// every tuple when there is no authenticated user, so a local Radar runs this
// endpoint with authorization switched off entirely. These tests are the only
// place the gate executes against a real user.
//
// Worth gating at all because the obvious permission is the wrong one. Reading
// the messages works by creating a DownloadRequest, and `create
// downloadrequests` is a grant Velero's own CLI needs — it travels with
// operator tooling and is often held far more widely than the right to read any
// given backup. Gate on that alone and someone who cannot open a Backup can
// still read the errors it recorded.

func TestVeleroRunMessages_RequiresAccessToTheRunItself(t *testing.T) {
	env := newAuthTestServer(t)

	perms := &auth.UserPermissions{AllowedNamespaces: []string{"velero"}}
	perms.SetCanI("get", "velero.io", "backups", "velero", false)
	env.srv.permCache.Set("mallory", nil, perms)

	resp := env.authPost(t, "/api/velero/backups/velero/nightly/messages", "mallory", "", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — a caller who cannot read the Backup must not read its errors", resp.StatusCode)
	}
}

// Restores go through the same handler but a different resource name, and a
// gate that checked only one of them would authorize the wrong question for the
// other. A stuck restore is the more urgent kind, so this is not the branch to
// leave open.
func TestVeleroRunMessages_GatesRestoresSeparately(t *testing.T) {
	env := newAuthTestServer(t)

	perms := &auth.UserPermissions{AllowedNamespaces: []string{"velero"}}
	// Allowed on backups, denied on restores: the two must not share a verdict.
	perms.SetCanI("get", "velero.io", "backups", "velero", true)
	perms.SetCanI("get", "velero.io", "restores", "velero", false)
	env.srv.permCache.Set("bob", nil, perms)

	resp := env.authPost(t, "/api/velero/restores/velero/recovery/messages", "bob", "", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — backup access is not restore access", resp.StatusCode)
	}
}

// The gate must not be the only thing standing between a legitimate caller and
// the feature: an authorized user has to get past it. They will not reach a
// controller in a unit test, so anything other than 403 means the gate let them
// through, which is what this asserts.
func TestVeleroRunMessages_LetsAnAuthorizedCallerThrough(t *testing.T) {
	env := newAuthTestServer(t)

	perms := &auth.UserPermissions{AllowedNamespaces: []string{"velero"}}
	perms.SetCanI("get", "velero.io", "backups", "velero", true)
	env.srv.permCache.Set("alice", nil, perms)

	resp := env.authPost(t, "/api/velero/backups/velero/nightly/messages", "alice", "", "")
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		t.Error("an authorized caller was refused by the gate")
	}
}

// An unsupported kind is rejected before anything is created, so the closed set
// cannot be widened by a URL. Checked here because the same handler serves every
// value of {kind}.
func TestVeleroRunMessages_RejectsKindsWithNoResults(t *testing.T) {
	env := newAuthTestServer(t)

	perms := &auth.UserPermissions{AllowedNamespaces: []string{"velero"}}
	perms.SetCanI("get", "velero.io", "schedules", "velero", true)
	env.srv.permCache.Set("alice", nil, perms)

	resp := env.authPost(t, "/api/velero/schedules/velero/nightly/messages", "alice", "", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a kind that has no results file", resp.StatusCode)
	}
}
