package server

import (
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	"github.com/skyhook-io/radar/internal/k8s"
)

// authorizeCNPGRuntime gates like the logs endpoint — namespace, the owning
// CNPG object, listing Pods — before the object is looked up. pods/proxy is
// deliberately not part of the gate: without it the Kubernetes facts still
// render and each source reports itself denied.
func (s *Server) authorizeCNPGRuntime(w http.ResponseWriter, r *http.Request, namespace, resource string) bool {
	if err := s.authorizeCNPGCachedRead(r, namespace, resource, cnpgsvc.GrantListPods); err != nil {
		s.writeCNPGCachedReadError(w, err, namespace, chi.URLParam(r, "name"))
		return false
	}
	return true
}

// cnpgRuntimeClient builds the caller's client for the proxy reads: the
// impersonated identity when auth is on (nil when impersonation fails — never
// Radar's own identity), with redirects refused so a proxied answer cannot
// steer the request to another path on the Pod.
func cnpgRuntimeClient(r *http.Request) kubernetes.Interface {
	cfg := k8s.ConfigFromContext(r.Context())
	if cfg == nil {
		return nil
	}
	hc, err := rest.HTTPClientFor(cfg)
	if err != nil {
		log.Printf("[cnpg] Failed to build runtime HTTP client: %v", err)
		return nil
	}
	noRedirect := *hc
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client, err := kubernetes.NewForConfigAndClient(cfg, &noRedirect)
	if err != nil {
		log.Printf("[cnpg] Failed to build runtime client: %v", err)
		return nil
	}
	return client
}

// handleCNPGClusterRuntime serves GET /api/cnpg/clusters/{namespace}/{name}/runtime:
// each instance's /pg/status and exporter metrics, read through the caller's
// own pods/proxy.
func (s *Server) handleCNPGClusterRuntime(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	resp, err := s.cnpgReader(r).ClusterRuntime(r.Context(), namespace, name)
	if err != nil {
		s.writeCNPGCachedReadError(w, err, namespace, name)
		return
	}
	s.writeJSON(w, resp)
}

// handleCNPGPoolerRuntime serves GET /api/cnpg/poolers/{namespace}/{name}/runtime:
// each pooler Pod's PgBouncer exporter, read through the caller's pods/proxy.
func (s *Server) handleCNPGPoolerRuntime(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.authorizeCNPGRuntime(w, r, namespace, "poolers") {
		return
	}
	reader := s.cnpgReader(r)
	cache := reader.Observations.Cache
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "resource cache not available")
		return
	}
	pooler, err := reader.Pooler(r.Context(), cache, namespace, name)
	pooler, err = cnpgCachedResourceResult(pooler, err, "Pooler", namespace, name)
	if err != nil {
		s.writeCNPGCachedReadError(w, err, namespace, name)
		return
	}
	resp, err := reader.PoolerRuntime(r.Context(), cache, pooler)
	if err != nil {
		s.writeCNPGCachedReadError(w, err, namespace, name)
		return
	}
	s.writeJSON(w, resp)
}

func cnpgRuntimeIdentity(r *http.Request) string {
	id := k8s.GetContextName()
	if user := auth.UserFromContext(r.Context()); user != nil {
		groups := append([]string(nil), user.Groups...)
		sort.Strings(groups)
		id += "\x00" + user.Username + "\x00" + strings.Join(groups, ",")
	}
	return id
}
