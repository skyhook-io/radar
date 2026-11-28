package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connectionruntime"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/pkg/argoapi"
	"github.com/skyhook-io/radar/pkg/prom"
	"github.com/skyhook-io/radar/pkg/urlutil"
)

// handleArgoCDStatus reports the live Argo CD integration state for the Settings
// Overview: configured (a URL or token is set), connected (a probe has landed),
// and the resolved address. Read-only and NOT owner-gated — the Overview is shown
// to every user, and this mirrors the Prometheus status endpoint. It reports
// state and never waits on the server. Get() nudges a throttled background
// reconnect when disconnected, so opening Overview after a restart helps the
// integration come back.
func (s *Server) handleArgoCDStatus(w http.ResponseWriter, r *http.Request) {
	set := connectionruntime.SetFor(k8s.BoundOrCurrent(r.Context()))
	manager := set.ArgoCD
	resp := struct {
		Configured bool `json:"configured"`
		Connected  bool `json:"connected"`
		// Connecting: not connected yet, and a connection is being made.
		Connecting bool `json:"connecting,omitempty"`
		// Anonymous: connected without a token because the install serves
		// reads to everyone — Settings can say there is nothing to add.
		Anonymous bool   `json:"anonymous,omitempty"`
		Address   string `json:"address,omitempty"`
		Reason    string `json:"reason,omitempty"`
	}{}
	if manager == nil || set.ArgoSettingsError() != nil {
		if err := set.ArgoSettingsError(); err != nil {
			resp.Reason = prom.RedactURLs(err.Error())
		}
		if s.localConnections != nil {
			view := s.readLocalConnectionViews()[config.IntegrationArgoCD]
			resp.Configured = view.URL != "" || view.SecretSet || view.State == "launch"
		} else {
			settings := s.integrationRuntime.Settings().ArgoCD
			resp.Configured = settings.URL != "" || settings.Token != ""
		}
		s.writeJSON(w, resp)
		return
	}
	_, resp.Connected = manager.Get()
	resp.Configured = manager.IsConfigured()
	if resp.Connected {
		resp.Address = manager.Address()
		resp.Anonymous = manager.AnonymousReadAllowed()
		if err := manager.LastReadError(); err != nil && resp.Configured {
			resp.Reason = "Radar's last read from Argo CD failed: " + argoAPIHealthFailure(err, manager.TokenSet()) + "."
		}
	} else {
		// While a reconnect runs, the last failure still says why it is needed.
		resp.Connecting = manager.Connecting()
		if manager.TokenBindingUpgradeRequired() {
			resp.Reason = "Re-enter the Argo CD token in Settings to bind it to this kubeconfig source."
			if s.configManagement() == "operator" {
				resp.Reason = "The saved Argo CD token is not bound to this kubeconfig source. Ask the operator to provision the token through startup configuration."
			}
		} else if err := manager.LastProbeError(); err != nil && (resp.Configured || errors.Is(err, portforward.ErrAutoDisabled)) {
			resp.Reason = "Radar couldn't connect to Argo CD: " + argoAPIHealthFailure(err, manager.TokenSet()) + "."
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleApplyArgoCDConfig re-points the running Argo CD client at new settings
// after the candidate passes its connection check. Failed checks leave the
// saved settings and live member in place.
func (s *Server) handleApplyArgoCDConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireConfigEditable(w, r) {
		return
	}
	if !s.requireCloudRole(w, r, auth.RoleOwner, "modify Radar configuration") {
		return
	}
	if s.localConnections != nil && s.configManagement() == "local" {
		s.writeError(w, http.StatusConflict, "Use the cluster-scoped saved connections endpoint to change this local integration")
		return
	}
	// When the integration is provisioned from the environment, it is the
	// declarative source of truth: refuse UI edits rather than silently accept a
	// change the env config would override on the next probe. This is the only
	// user-facing path that mutates the Argo connection, so gating it here makes
	// env-managed effectively read-only.
	conn := k8s.Current()
	base := s.integrationRuntime.Snapshot()
	if base.ArgoEnv {
		s.writeError(w, http.StatusConflict,
			"Argo CD is configured from the environment (RADAR_ARGOCD_TOKEN) — edit the deployment to change it.")
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
		// Same strict validation as the env input: a persisted URL is returned by
		// GET /api/config (only the token is redacted), so a credential-bearing URL
		// (userinfo or ?token=…) must be rejected here too, not just http(s).
		if err := argocd.ValidateServerURL(rawURL); err != nil {
			s.writeError(w, http.StatusBadRequest, "Argo CD URL is invalid — it "+err.Error()+" (e.g. https://argocd.example.com)")
			return
		}
	}

	// Adopting a CLI token requires an explicit server URL. TokenFromCLI selects
	// the token for the CLI's current-context server, but an empty URL then puts
	// the manager into Kubernetes auto-discovery, which would probe whatever Argo
	// the current cluster exposes — sending the CLI credential to a possibly
	// different server. The two meanings of "empty URL" must not cross.
	if body.UseCLIToken && rawURL == "" {
		s.writeError(w, http.StatusBadRequest, "Provide the Argo CD server URL when connecting with a CLI session token.")
		return
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
			log.Printf("[argocd] CLI token lookup failed: %s", sanitizeForLog(err.Error()))
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
	if token != "" && !suppliesNewToken && !sameIntegrationOrigin(rawURL, prev.ArgoCDURL) {
		s.writeError(w, http.StatusBadRequest,
			"Changing the Argo CD URL requires re-entering the API token — a token is bound to the server it was issued by.")
		return
	}

	argo := argoapi.Connection{URL: rawURL, Token: token, InsecureTLS: body.ArgoCDInsecureTLS}
	tokenContext, tokenBinding := prev.ArgoCDTokenContext, prev.ArgoCDTokenBinding
	if suppliesNewToken {
		tokenContext, tokenBinding = "", ""
		if rawURL == "" && token != "" {
			tokenContext, tokenBinding = conn.ContextName(), conn.ContextBinding()
			if k8s.IsInCluster() {
				tokenBinding = argocd.InClusterTokenBinding
			}
		}
	}
	edit := s.integrationRuntime.PrepareShared(conn, base, func(next *connectionruntime.Settings) {
		next.ArgoCD = argo
		next.ArgoTokenContext, next.ArgoTokenBinding = tokenContext, tokenBinding
	}, config.IntegrationArgoCD)
	manager := edit.Set().ArgoCD
	if manager == nil {
		manager = argocd.NewConnectionManager(conn, k8s.IsInCluster(), conn.ContextBinding())
		manager.RestoreConfig(rawURL, token, body.ArgoCDInsecureTLS, tokenContext, tokenBinding)
		defer manager.Close()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if err := manager.Probe(ctx); err != nil {
		edit.Discard()
		if portforward.IsMemberClosed(ctx, err) {
			s.writeError(w, http.StatusServiceUnavailable, argoConnectionClosedMessage)
			return
		}
		log.Printf("[argocd] Probe of candidate settings failed: %s", sanitizeForLog(manager.RedactToken(err.Error())))
		switch {
		case errors.Is(err, argocd.ErrTokenBindingUpgrade):
			s.writeError(w, http.StatusBadRequest, "Re-enter the Argo CD token in Settings to bind it to this kubeconfig source.")
		case errors.Is(err, argocd.ErrTokenInvalid):
			s.writeError(w, http.StatusBadRequest, "Argo CD rejected the token — check that it is valid and not expired.")
		default:
			s.writeError(w, http.StatusBadRequest, "Argo CD server unreachable — check the URL and network path.")
		}
		return
	}
	address := manager.Address()
	if err := edit.Commit(func() error {
		_, err := config.Update(func(c *config.Config) {
			c.ArgoCDURL, c.ArgoCDToken, c.ArgoCDInsecureTLS = rawURL, token, body.ArgoCDInsecureTLS
			c.ArgoCDTokenContext, c.ArgoCDTokenBinding = tokenContext, tokenBinding
		})
		return err
	}); err != nil {
		s.integrationCommitError(w, "Argo CD", err)
		return
	}

	s.writeJSON(w, struct {
		Connected bool   `json:"connected"`
		Address   string `json:"address,omitempty"`
		TokenSet  bool   `json:"tokenSet"`
	}{
		Connected: true,
		Address:   address,
		TokenSet:  token != "",
	})
}

// sameIntegrationOrigin reports whether two integration URLs address the same server.
// Empty means auto-discovery (the in-cluster argocd-server); two empties are
// the same origin, and empty vs explicit is a change. Comparison is on
// scheme + host + effective port (default ports normalized, so
// https://host and https://host:443 are the same origin), case-insensitive —
// a token is bound to an origin, not a path.
func sameIntegrationOrigin(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return a == b
	}
	na, oka := urlutil.NormalizeOrigin(a)
	nb, okb := urlutil.NormalizeOrigin(b)
	if !oka || !okb {
		return a == b
	}
	return na == nb
}
