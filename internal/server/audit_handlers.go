package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
	bp "github.com/skyhook-io/radar/pkg/audit"
)

// apiResourceKindMap maps lowercase plural API resource names to Go Kind names
// for the resource types that the audit scanner checks.
var apiResourceKindMap = map[string]string{
	"pods":         "Pod",
	"deployments":  "Deployment",
	"statefulsets": "StatefulSet",
	"daemonsets":   "DaemonSet",
	"services":     "Service",
	"ingresses":    "Ingress",
	"configmaps":   "ConfigMap",
	"secrets":      "Secret",
}

func apiResourceToKind(resource string) string {
	if kind, ok := apiResourceKindMap[resource]; ok {
		return kind
	}
	return resource
}

// auditCache caches scan results for a short TTL so that
// dashboard polls and per-resource lookups share the same scan.
var auditCache struct {
	mu        sync.RWMutex
	results   *bp.ScanResults
	nsKey     string // comma-joined namespace filter used for this result
	expiresAt time.Time
}

const auditCacheTTL = 5 * time.Second

// getCachedResults returns cached scan results if fresh, or runs a new scan.
func getCachedResults(cache *k8s.ResourceCache, namespaces []string) *bp.ScanResults {
	nsKey := strings.Join(namespaces, ",")

	auditCache.mu.RLock()
	if auditCache.results != nil && auditCache.nsKey == nsKey && time.Now().Before(auditCache.expiresAt) {
		r := auditCache.results
		auditCache.mu.RUnlock()
		return r
	}
	auditCache.mu.RUnlock()

	results := audit.RunFromCache(cache, namespaces, nil)

	auditCache.mu.Lock()
	auditCache.results = results
	auditCache.nsKey = nsKey
	auditCache.expiresAt = time.Now().Add(auditCacheTTL)
	auditCache.mu.Unlock()

	return results
}

// applyAuditSettings filters results based on user settings.
func applyAuditSettings(results *bp.ScanResults, cfg settings.AuditConfig) *bp.ScanResults {
	return bp.ApplySettings(results, cfg.IgnoredNamespaces, cfg.DisabledChecks)
}

// getAuditConfig returns the current audit config with defaults applied.
func getAuditConfig() settings.AuditConfig {
	s := settings.Load()
	if s.Audit != nil {
		return *s.Audit
	}
	return settings.DefaultAuditConfig()
}

// handleAudit returns full audit scan results.
// GET /api/audit?namespace=X&namespaces=X,Y
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Cache not initialized")
		return
	}
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, &bp.ScanResults{Summary: bp.ScanSummary{Categories: map[string]bp.CategorySummary{}}})
		return
	}
	results := getCachedResults(cache, namespaces)
	results = applyAuditSettings(results, getAuditConfig())
	s.writeJSON(w, results)
}

// handleAuditResource returns findings for a specific resource.
// GET /api/audit/resource/{kind}/{namespace}/{name}
func (s *Server) handleAuditResource(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Cache not initialized")
		return
	}

	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, []bp.Finding{})
		return
	}
	results := getCachedResults(cache, namespaces)
	results = applyAuditSettings(results, getAuditConfig())
	index := bp.IndexByResource(results.Findings)

	// Try exact kind first, then map API resource name (e.g. "deployments") to Go kind (e.g. "Deployment").
	// This handler is the UI's per-resource audit drill-down — group isn't on
	// the URL today (the UI doesn't list grouped CRDs here yet), so we look
	// up with group="" which matches the built-ins the audit suite scans.
	// When CRD audit lands (#35 follow-up), thread group through the URL.
	findings := index[bp.ResourceKey("", kind, namespace, name)]
	if findings == nil {
		goKind := apiResourceToKind(kind)
		if goKind != kind {
			findings = index[bp.ResourceKey("", goKind, namespace, name)]
		}
	}
	if findings == nil {
		findings = []bp.Finding{}
	}
	s.writeJSON(w, findings)
}

// handleGetAuditSettings returns the current audit configuration.
// GET /api/settings/audit
func (s *Server) handleGetAuditSettings(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, getAuditConfig())
}

// handlePutAuditSettings updates the audit configuration.
// PUT /api/settings/audit
func (s *Server) handlePutAuditSettings(w http.ResponseWriter, r *http.Request) {
	var cfg settings.AuditConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	updated, err := settings.Update(func(s *settings.Settings) {
		s.Audit = &cfg
	})
	if err != nil {
		log.Printf("[audit] Failed to save audit settings: %v", err)
		s.writeError(w, http.StatusInternalServerError, "Failed to save settings: "+err.Error())
		return
	}
	if updated.Audit != nil {
		s.writeJSON(w, *updated.Audit)
	} else {
		s.writeJSON(w, settings.DefaultAuditConfig())
	}
}
