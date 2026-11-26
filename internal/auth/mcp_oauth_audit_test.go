package auth

import (
	"bytes"
	"log"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func captureMCPAudit(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previous)
		log.SetFlags(flags)
	})
	return &buf
}

func mcpAuditLines(buf *bytes.Buffer) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.HasPrefix(line, "[auth] MCP ") {
			lines = append(lines, line)
		}
	}
	return lines
}

func requireMCPAuditLine(t *testing.T, lines []string, event string, want ...string) {
	t.Helper()
	for _, line := range lines {
		if !strings.HasPrefix(line, "[auth] MCP "+event+":") {
			continue
		}
		for _, field := range want {
			if !strings.Contains(line, field) {
				t.Fatalf("%q line missing %s: %s", event, field, line)
			}
		}
		return
	}
	t.Fatalf("no %q line in:\n%s", event, strings.Join(lines, "\n"))
}

func requireNoMCPSecrets(t *testing.T, buf *bytes.Buffer, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(buf.String(), secret) {
			t.Fatalf("audit log contains secret value %q:\n%s", secret, buf)
		}
	}
}

func TestMCPOAuthAuditApproveIssueRefreshAndClientRevoke(t *testing.T) {
	buf := captureMCPAudit(t)
	f := newMCPOAuthFixture(t)
	page := f.authorize(f.request("/mcp-readonly"), f.cookies)
	form := f.consentForm(page)
	consent := f.consent(page, form, f.s.origin, f.cookies)
	location, _ := url.Parse(consent.Header().Get("Location"))
	code := location.Query().Get("code")
	_, first := f.token(f.exchangeForm(code, "/mcp-readonly"))
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.client.ID}, "refresh_token": {first["refresh_token"].(string)}}
	w, second := f.token(refresh)
	if w.Code != 200 {
		t.Fatalf("refresh: %d", w.Code)
	}
	revoke := url.Values{"client_id": {f.client.ID}, "token": {second["access_token"].(string)}}
	r := httptest.NewRequest("POST", "/auth/mcp/revoke", strings.NewReader(revoke.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f.s.HandleRevoke(httptest.NewRecorder(), r)

	lines := mcpAuditLines(buf)
	client := `client_id="` + f.client.ID + `"`
	name := `client_name_self_reported="Example MCP client"`
	requireMCPAuditLine(t, lines, "client registered", client, name, `return_host="127.0.0.1:54321"`)
	for _, event := range []string{"access approved", "tokens issued", "tokens refreshed"} {
		requireMCPAuditLine(t, lines, event, `user="alice@example.com"`, client, name, `return_host="127.0.0.1:54321"`, `endpoint="read-only"`)
	}
	requireMCPAuditLine(t, lines, "grant revoked", `user="alice@example.com"`, client, `reason="revoked by the client"`)
	requireNoMCPSecrets(t, buf, code, f.verifier, form.Get("csrf"), form.Get("request"), f.s.cfg.Secret, "client-state", f.client.RedirectURIs[0],
		first["access_token"].(string), first["refresh_token"].(string), second["access_token"].(string), second["refresh_token"].(string))
}

func TestMCPOAuthAuditDeny(t *testing.T) {
	buf := captureMCPAudit(t)
	f := newMCPOAuthFixture(t)
	page := f.authorize(f.request("/mcp"), f.cookies)
	form := f.consentForm(page)
	form.Set("decision", "deny")
	if w := f.consent(page, form, f.s.origin, f.cookies); w.Code != 303 {
		t.Fatalf("deny: %d", w.Code)
	}
	lines := mcpAuditLines(buf)
	requireMCPAuditLine(t, lines, "access denied", `user="alice@example.com"`, `client_id="`+f.client.ID+`"`, `endpoint="full"`, `reason="user denied consent"`)
	for _, line := range lines {
		if strings.HasPrefix(line, "[auth] MCP access approved:") {
			t.Fatalf("denial logged an approval: %s", line)
		}
	}
	requireNoMCPSecrets(t, buf, form.Get("csrf"), form.Get("request"), f.s.cfg.Secret, "client-state", f.client.RedirectURIs[0])
}

func TestMCPOAuthAuditReplay(t *testing.T) {
	t.Run("authorization code", func(t *testing.T) {
		buf := captureMCPAudit(t)
		f := newMCPOAuthFixture(t)
		code := f.code("/mcp")
		_, token := f.token(f.exchangeForm(code, "/mcp"))
		if w, _ := f.token(f.exchangeForm(code, "/mcp")); w.Code != 400 {
			t.Fatal("code replay accepted")
		}
		lines := mcpAuditLines(buf)
		requireMCPAuditLine(t, lines, "replay detected, grant revoked", `user="alice@example.com"`, `client_id="`+f.client.ID+`"`, `reason="authorization code reused"`)
		requireNoMCPSecrets(t, buf, code, f.verifier, token["access_token"].(string), token["refresh_token"].(string))
	})
	t.Run("refresh token", func(t *testing.T) {
		buf := captureMCPAudit(t)
		f := newMCPOAuthFixture(t)
		_, first := f.token(f.exchangeForm(f.code("/mcp"), "/mcp"))
		refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.client.ID}, "refresh_token": {first["refresh_token"].(string)}}
		_, second := f.token(refresh)
		if w, _ := f.token(refresh); w.Code != 400 {
			t.Fatal("refresh replay accepted")
		}
		lines := mcpAuditLines(buf)
		requireMCPAuditLine(t, lines, "replay detected, grant revoked", `user="alice@example.com"`, `client_id="`+f.client.ID+`"`, `reason="superseded refresh token reused"`)
		replays := 0
		for _, line := range lines {
			if strings.HasPrefix(line, "[auth] MCP replay detected") {
				replays++
			}
		}
		if w, _ := f.token(refresh); w.Code != 400 || replays != 1 {
			t.Fatalf("replay logged %d times", replays)
		}
		requireNoMCPSecrets(t, buf, first["access_token"].(string), first["refresh_token"].(string), second["access_token"].(string), second["refresh_token"].(string))
	})
}

func TestMCPOAuthAuditSessionRevoke(t *testing.T) {
	buf := captureMCPAudit(t)
	f := newMCPOAuthFixture(t)
	_, token := f.token(f.exchangeForm(f.code("/mcp"), "/mcp"))
	f.s.RevokeSession("browser-sid", "logout")
	f.s.RevokeSession("browser-sid", "logout")
	var revoked []string
	for _, line := range mcpAuditLines(buf) {
		if strings.HasPrefix(line, "[auth] MCP grant revoked:") {
			revoked = append(revoked, line)
		}
	}
	if len(revoked) != 1 {
		t.Fatalf("want one revocation line per grant, got %d:\n%s", len(revoked), strings.Join(revoked, "\n"))
	}
	requireMCPAuditLine(t, revoked, "grant revoked", `user="alice@example.com"`, `client_id="`+f.client.ID+`"`, `endpoint="full"`, `reason="logout"`)
	requireNoMCPSecrets(t, buf, "browser-sid", token["access_token"].(string), token["refresh_token"].(string), f.s.cfg.Secret)
}
