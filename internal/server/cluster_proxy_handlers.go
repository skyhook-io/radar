package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
	"github.com/skyhook-io/radar/pkg/prom"
)

// clusterProxyUnavailable gates saving a per-context proxy like the local
// terminal: the proxy reroutes how this host reaches the cluster, so only the
// person at a loopback-bound local Radar may set it. On a shared listener
// without authentication anyone reaching the address could otherwise send
// Radar's Kubernetes traffic through a proxy of their choosing.
func (s *Server) clusterProxyUnavailable(r *http.Request) (int, string) {
	if s.configManagement() != "local" {
		return http.StatusForbidden, "A cluster proxy can be set only in local CLI and Desktop Radar."
	}
	if cloud.IsAuthenticatedTunnelRequest(r.Context()) {
		return http.StatusForbidden, "A cluster proxy can't be set over the Radar Hub tunnel."
	}
	if s.sharedListener() || !requestHostIsLoopback(r) {
		return http.StatusForbidden, "A cluster proxy can be set only over a loopback address."
	}
	return 0, ""
}

type contextProxyRequest struct {
	ProxyURL string `json:"proxyUrl"`
}

type contextProxyResponse struct {
	Context  string `json:"context"`
	ProxyURL string `json:"proxyUrl,omitempty"`
	// Current is true when the context is the active one: the caller retries
	// the connection to start using the proxy.
	Current bool `json:"current"`
}

// handleSetContextProxy saves (or, with an empty proxyUrl, removes) the proxy
// Radar uses for a context, like Lens's per-cluster HTTP proxy setting,
// without editing the kubeconfig. A new proxy is saved only after the
// context's API server answered through it; the active connection is left
// alone until the caller retries it.
func (s *Server) handleSetContextProxy(w http.ResponseWriter, r *http.Request) {
	if status, message := s.clusterProxyUnavailable(r); status != 0 {
		s.writeError(w, status, message)
		return
	}
	name, err := url.PathUnescape(chi.URLParam(r, "name"))
	if err != nil || name == "" {
		s.writeError(w, http.StatusBadRequest, "context name is required")
		return
	}
	var req contextProxyRequest
	if err := decodeBoundedJSONBody(w, r, 8*1024, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "proxy request is too large")
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid proxy request")
		return
	}
	proxyURL := strings.TrimSpace(req.ProxyURL)
	var probedBinding string
	if proxyURL != "" {
		if _, err := k8s.ParseClusterProxyURL(proxyURL); err != nil {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// A 401 or 403 came from the API server, so the proxy got through;
		// the credentials are a separate problem the connection screen explains.
		probedBinding, err = testContextProxy(r.Context(), name, proxyURL)
		if err != nil && !apierrors.IsUnauthorized(err) && !apierrors.IsForbidden(err) {
			switch {
			case k8s.IsContextNotFound(err):
				s.writeError(w, http.StatusNotFound, err.Error())
			case k8s.IsClientSetupFailed(err):
				s.writeError(w, http.StatusBadRequest, err.Error())
			default:
				s.writeErrorCode(w, http.StatusBadRequest, "proxy_unreachable",
					"Radar couldn't reach the cluster through this proxy, so it wasn't saved: "+err.Error())
			}
			return
		}
	}
	binding := k8s.ContextBindingFor(name)
	if binding == "" || (proxyURL == "" && !contextExists(name)) {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("kubeconfig context %q not found", name))
		return
	}
	if proxyURL != "" && binding != probedBinding {
		s.writeError(w, http.StatusConflict, fmt.Sprintf("kubeconfig context %q changed while Radar tested the proxy; try again", name))
		return
	}
	if err := settings.SetClusterProxy(binding, proxyURL); err != nil {
		log.Printf("[proxy] Failed to save proxy for context %q: %v", name, err)
		s.writeError(w, http.StatusInternalServerError, "failed to save the proxy: "+err.Error())
		return
	}
	s.writeJSON(w, contextProxyResponse{Context: name, ProxyURL: prom.SafeAddress(proxyURL), Current: name == k8s.GetContextName()})
}

var (
	testContextProxy  = k8s.TestContextProxy
	availableContexts = k8s.GetAvailableContexts
	contextExists     = k8s.ContextExists
)
