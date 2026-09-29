package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const oidcMCPReturnCookieName = "radar_oidc_mcp_return"

type oidcMCPReturn struct {
	State   string `json:"state"`
	Target  string `json:"target"`
	Expires int64  `json:"expires"`
}

func (h *OIDCHandler) validMCPReturn(target string) bool {
	if len(target) > 2048 || strings.ContainsAny(target, "\r\n\\") {
		return false
	}
	u, err := url.Parse(target)
	return err == nil && u.Scheme == "" && u.Host == "" && u.User == nil && u.Fragment == "" && u.RawPath == "" && u.Path == h.basePath+"/auth/mcp/authorize"
}

func (h *OIDCHandler) setMCPReturnCookie(w http.ResponseWriter, r *http.Request, state string) {
	target := r.URL.Query().Get("return_to")
	if !h.validMCPReturn(target) {
		http.SetCookie(w, h.clearFlowCookie(oidcMCPReturnCookieName, r))
		return
	}
	payload, _ := json.Marshal(oidcMCPReturn{state, target, time.Now().Add(10 * time.Minute).Unix()})
	mac := hmac.New(sha256.New, []byte(h.cfg.Secret))
	mac.Write(payload)
	value := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, h.newFlowCookie(oidcMCPReturnCookieName, value, r))
}

func (h *OIDCHandler) mcpReturnTarget(r *http.Request, state string) string {
	fallback := h.basePath + "/"
	cookie, err := r.Cookie(oidcMCPReturnCookieName)
	if err != nil || len(cookie.Value) > 4096 {
		return fallback
	}
	encoded, signature, ok := strings.Cut(cookie.Value, ".")
	if !ok {
		return fallback
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return fallback
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return fallback
	}
	mac := hmac.New(sha256.New, []byte(h.cfg.Secret))
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return fallback
	}
	var continuation oidcMCPReturn
	if json.Unmarshal(payload, &continuation) != nil || continuation.State != state || continuation.Expires <= time.Now().Unix() || !h.validMCPReturn(continuation.Target) {
		return fallback
	}
	return continuation.Target
}
