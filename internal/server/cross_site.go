package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/skyhook-io/radar/internal/cloud"
)

// browserOriginAllowed decides whether a request may act with Radar's cluster
// authority. It guards every state-changing /api route (via requireSameOrigin)
// and the pod exec and local-terminal WebSocket upgrades, so a trusted origin
// set by the operator applies to all of them.
//
// Allowed: the authenticated Hub tunnel (its marker cannot be set by a
// browser), an origin listed in --trusted-origins, Sec-Fetch-Site
// "same-origin" or "none", no Origin at all (curl, scripts, MCP), an Origin
// matching the request's Host, and the Vite dev proxy under --dev.
func (s *Server) browserOriginAllowed(r *http.Request) bool {
	if cloud.IsAuthenticatedTunnelRequest(r.Context()) {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin != "" && s.originTrusted(origin) {
		return true
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "cross-site":
		return false
	}
	if origin == "" {
		return true
	}
	return sameAuthorityOriginOK(r) || s.viteDevProxyOriginOK(r)
}

// browserReadAllowed decides whether a GET or HEAD under /api may run. It
// refuses only what the browser itself labels as coming from another site, so
// curl, scripts and browsers that send no Sec-Fetch-Site are unaffected, and
// a proxy that rewrites Host is too: the browser compares the page with the
// address it sent the request to, both of which are Radar's public address.
//
// "same-site" is refused as well. A site ignores the port, so any other page
// served from localhost is same-site with a local Radar.
//
// Browsers send Sec-Fetch-Site only to HTTPS and localhost addresses. Over
// plain HTTP at any other address a cross-site read carries nothing to tell it
// apart, so it passes. Writes do not depend on this: they also check Origin.
func (s *Server) browserReadAllowed(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return cloud.IsAuthenticatedTunnelRequest(r.Context()) || s.originTrusted(r.Header.Get("Origin"))
	}
	return true
}

// sameAuthorityOriginOK compares the Origin with the Host the request arrived
// with. An HTTPS request (TLS here, or X-Forwarded-Proto from a terminating
// proxy) never accepts a plaintext origin.
func sameAuthorityOriginOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if (r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")) &&
		!strings.EqualFold(u.Scheme, "https") {
		return false
	}
	originAuthority, originOK := normalizeOrigin(origin)
	requestAuthority, requestOK := normalizeOrigin(u.Scheme + "://" + r.Host)
	return originOK && requestOK && originAuthority == requestAuthority
}

func (s *Server) viteDevProxyOriginOK(r *http.Request) bool {
	if !s.devMode || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	u, err := url.Parse(r.Header.Get("Origin"))
	return err == nil &&
		u.Scheme == "http" &&
		u.Port() == "9273" &&
		browserLoopbackHostname(u.Hostname()) &&
		requestHostIsLoopback(r)
}

func (s *Server) originTrusted(origin string) bool {
	normalized, ok := normalizeOrigin(origin)
	if !ok {
		return false
	}
	_, trusted := s.trustedOrigins[normalized]
	return trusted
}

// ParseTrustedOrigins validates a comma-separated --trusted-origins value.
// An entry it cannot use is reported in the error and left out, and the rest
// are still returned: a typo in an optional setting must not stop Radar from
// starting, so the caller logs the error and carries on.
func ParseTrustedOrigins(raw string) ([]string, error) {
	var origins []string
	var errs []error
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if err := validateTrustedOrigin(entry); err != nil {
			errs = append(errs, fmt.Errorf("%q: %w", entry, err))
			continue
		}
		origins = append(origins, entry)
	}
	return origins, errors.Join(errs...)
}

func validateTrustedOrigin(entry string) error {
	if strings.Contains(entry, "*") {
		return fmt.Errorf("wildcards are not allowed; list each origin exactly, e.g. https://radar.example.com")
	}
	u, err := url.Parse(entry)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("expected http(s)://host[:port], e.g. https://radar.example.com")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("an origin is scheme://host[:port] only, with no path, query or credentials")
	}
	if _, ok := normalizeOrigin(entry); !ok {
		return fmt.Errorf("invalid port")
	}
	return nil
}

// crossSiteExemptRoutes lists state-changing routes that must accept a request a
// browser initiated from another site. It is empty on purpose.
//
// Add an entry only for a route that a third party is genuinely meant to call
// cross-site, such as a webhook receiver, and write down who calls it and why it
// is safe without an origin check. OIDC back-channel logout is the one real
// cross-site caller today and it is registered outside /api, so it never
// reaches this middleware.
//
// prefix matches a whole path segment, so "/api/hooks" covers "/api/hooks" and
// "/api/hooks/github" but not "/api/hooks-admin".
var crossSiteExemptRoutes = []struct {
	method string
	prefix string
	why    string
}{}

func crossSiteExempt(r *http.Request) bool {
	for _, e := range crossSiteExemptRoutes {
		if r.Method != e.method {
			continue
		}
		if r.URL.Path == e.prefix || strings.HasPrefix(r.URL.Path, strings.TrimSuffix(e.prefix, "/")+"/") {
			return true
		}
	}
	return false
}

// requireSameOrigin rejects /api requests that a browser initiated from
// another site.
//
// Writes: a page can send a POST with no preflight when the content type is one
// a form could produce, so CORS never gets consulted before the handler runs.
// CORS only decides whether the page may read the response; the write has
// already happened.
//
// Reads: an <img> or link on another site sends a GET with no preflight either.
// The page cannot read the answer, but some GETs do work on the server before
// answering, such as a trace with probe=true calling in-cluster backends or an
// image lookup pulling from a registry. See browserReadAllowed.
//
// OPTIONS passes through so the CORS preflight is still answered. The pod exec
// and local-terminal WebSocket upgrades also check browserOriginAllowed
// themselves and must keep doing so: they are GET, and a trusted origin may
// open them.
func (s *Server) requireSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed := false
		switch r.Method {
		case http.MethodOptions:
			allowed = true
		case http.MethodGet, http.MethodHead:
			allowed = s.browserReadAllowed(r)
		default:
			allowed = crossSiteExempt(r) || s.browserOriginAllowed(r)
		}
		if allowed {
			next.ServeHTTP(w, r)
			return
		}
		log.Printf("[origin] refused %s %s: origin=%q host=%q sec-fetch-site=%q",
			r.Method, r.URL.Path, r.Header.Get("Origin"), r.Host, r.Header.Get("Sec-Fetch-Site"))
		s.writeErrorCode(w, http.StatusForbidden, "cross_origin_refused", crossOriginRefusalMessage(r))
	})
}

// crossOriginRefusalMessage is shown to the operator as the detail of the
// failed action. A legitimate user only sees it when a proxy hides Radar's own
// address, so it says what to change.
func crossOriginRefusalMessage(r *http.Request) string {
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return "Radar refused this request because the browser reported it came from another website."
	}
	return fmt.Sprintf("Radar refused this request because it came from %s, but reached Radar addressed to %s. "+
		"If %s is the address you open Radar at, set your proxy to keep the original Host header, "+
		"or add %s to Radar's trusted origins (Helm value trustedOrigins, RADAR_TRUSTED_ORIGINS, or --trusted-origins).", origin, r.Host, origin, origin)
}
