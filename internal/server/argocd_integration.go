package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/config"
)

// handleApplyArgoCDConfig re-points the running Argo CD client at new settings
// immediately and persists them, mirroring handleApplyPrometheusURL. The probe
// runs against the candidate settings BEFORE anything is persisted, so a bad
// URL or token can't land on disk; on probe failure the previous settings are
// restored on the live client.
func (s *Server) handleApplyArgoCDConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloudRole(w, r, auth.RoleOwner, "modify Radar configuration") {
		return
	}
	var body struct {
		ArgoCDURL string `json:"argoCdUrl"`
		// ArgoCDToken is a pointer so "not editing the token" (nil — keep
		// what's on disk) is distinct from "clear the token" (present but
		// empty). GET /api/config redacts the value, so a UI round-trip
		// would otherwise silently wipe it.
		ArgoCDToken       *string `json:"argoCdToken"`
		ArgoCDInsecureTLS bool    `json:"argoCdInsecureTls"`
		UseCLIToken       bool    `json:"useCliToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	rawURL := strings.TrimSpace(body.ArgoCDURL)
	if rawURL != "" {
		if u, err := url.Parse(rawURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			s.writeError(w, http.StatusBadRequest, "Argo CD URL must be a valid HTTP(S) URL (e.g., https://argocd.example.com)")
			return
		}
	}

	prev := config.Load()

	token := prev.ArgoCDToken
	suppliesNewToken := body.ArgoCDToken != nil || body.UseCLIToken
	if body.ArgoCDToken != nil {
		token = *body.ArgoCDToken
	}
	if body.UseCLIToken {
		cliToken, err := argocd.TokenFromCLI(rawURL)
		if err != nil {
			log.Printf("[argocd] CLI token lookup failed: %v", err)
			s.writeError(w, http.StatusBadRequest,
				"No Argo CD CLI session found for this server — run `argocd login` first, or paste a token directly")
			return
		}
		token = cliToken
	}

	// A token is minted for one Argo CD server; carrying a stored token to a
	// DIFFERENT origin would both fail and leak the credential to whatever host
	// the new URL points at (Radar probes it with Bearer <token>). So when the
	// origin changes and the caller reuses the stored token instead of supplying
	// a fresh one, refuse — the token must be re-entered for the new server.
	if token != "" && !suppliesNewToken && !sameArgoOrigin(rawURL, prev.ArgoCDURL) {
		s.writeError(w, http.StatusBadRequest,
			"Changing the Argo CD URL requires re-entering the API token — a token is bound to the server it was issued by.")
		return
	}

	argocd.SetConfig(rawURL, token, body.ArgoCDInsecureTLS)
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if err := argocd.Probe(ctx); err != nil {
		argocd.SetConfig(prev.ArgoCDURL, prev.ArgoCDToken, prev.ArgoCDInsecureTLS)
		// The upstream error can embed the raw response body (proxy headers, a
		// render error with Secret data). Log it server-side; return only the
		// mapped guidance so nothing from Argo's body reaches the browser.
		log.Printf("[argocd] Probe of candidate settings failed: %v", err)
		if errors.Is(err, argocd.ErrTokenInvalid) {
			s.writeError(w, http.StatusBadRequest, "Argo CD rejected the token — check that it is valid and not expired.")
			return
		}
		s.writeError(w, http.StatusBadRequest, "Argo CD server unreachable — check the URL and network path.")
		return
	}

	if _, err := config.Update(func(c *config.Config) {
		c.ArgoCDURL = rawURL
		c.ArgoCDToken = token
		c.ArgoCDInsecureTLS = body.ArgoCDInsecureTLS
	}); err != nil {
		// The running client must agree with the on-disk config; roll it back.
		argocd.SetConfig(prev.ArgoCDURL, prev.ArgoCDToken, prev.ArgoCDInsecureTLS)
		log.Printf("[argocd] Failed to persist Argo CD config: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, struct {
		Connected bool   `json:"connected"`
		Address   string `json:"address,omitempty"`
		TokenSet  bool   `json:"tokenSet"`
	}{
		Connected: true,
		Address:   argocd.Address(),
		TokenSet:  token != "",
	})
}

// sameArgoOrigin reports whether two Argo CD URLs address the same server.
// Empty means auto-discovery (the in-cluster argocd-server); two empties are
// the same origin, and empty vs explicit is a change. Comparison is on
// scheme + host + effective port (default ports normalized, so
// https://host and https://host:443 are the same origin), case-insensitive —
// a token is bound to an origin, not a path.
func sameArgoOrigin(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return a == b
	}
	na, oka := normalizeOrigin(a)
	nb, okb := normalizeOrigin(b)
	if !oka || !okb {
		return a == b
	}
	return na == nb
}

// normalizeOrigin returns a lowercased scheme://host:port with the scheme's
// default port filled in when omitted, so default-port and explicit-port forms
// of the same server compare equal.
func normalizeOrigin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return scheme + "://" + host + ":" + port, true
}
