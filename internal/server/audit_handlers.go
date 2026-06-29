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
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
	bp "github.com/skyhook-io/radar/pkg/audit"
	chk "github.com/skyhook-io/radar/pkg/checks"
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
	// `?raw=true` returns the unfiltered scan, skipping the local
	// ~/.radar/settings.json audit filters. Radar Cloud's Hub requests raw so
	// it can own the effective Checks config (org policy) centrally rather than
	// inheriting each cluster's local settings as if they were team policy.
	// Standalone Radar and the embedded per-cluster audit view omit the param
	// and keep applying local settings.
	if queryTrue(r, "raw") {
		// Raw fan-out (the Hub): just the findings + catalog. The Hub applies
		// org policy and builds its own rollup, so computing one here is waste.
		s.writeJSON(w, results)
		return
	}
	results = applyAuditSettings(results, getAuditConfig())
	// Attach the remediation-queue rollup for standalone + embedded per-cluster
	// views. Build on a shallow copy: applyAuditSettings returns the shared
	// cache object verbatim when no settings filter, so mutating it would race
	// concurrent readers. clusterID/env are empty — single-cluster, and the web
	// supplies cluster context from the URL when embedded.
	resp := *results
	resp.GroupedChecks = chk.BuildChecks(bp.EffectiveFindings(results.Findings, ""), results.Checks, "", "")
	s.writeJSON(w, &resp)
}

// queryTrue reports whether a query param parses as a truthy boolean. Tolerant
// of the usual forms (true/1/t); anything else (incl. absent) reads false.
func queryTrue(r *http.Request, key string) bool {
	switch strings.ToLower(r.URL.Query().Get(key)) {
	case "true", "1", "t", "yes":
		return true
	}
	return false
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
	// Honor ?raw=true here too (mirrors handleAudit), so the per-resource
	// drill-down and the list endpoint can't drift on the raw contract: Radar
	// Cloud's Hub owns effective Checks config, standalone keeps local settings.
	if !queryTrue(r, "raw") {
		results = applyAuditSettings(results, getAuditConfig())
	}
	index := bp.IndexByResource(results.Findings)

	// Resolve to the Go kind (built-in plural "deployments" → "Deployment"), then
	// look up with the audit's keying convention. Findings carry group backfilled
	// from the builtin table (built-ins → their group e.g. "apps"; CRDs → ""), so
	// a bare group="" lookup missed every built-in finding keyed under a real
	// group (Deployment under "apps", Job under "batch", …) — the drawer showed
	// nothing for them. Group still resolves to "" for CRDs, so those keep working.
	goKind := kind
	if mapped := apiResourceToKind(kind); mapped != kind {
		goKind = mapped
	}
	group := bp.GroupForBuiltinKind(goKind)
	findings := index[bp.ResourceKey(group, goKind, namespace, name)]
	if findings == nil && group != "" {
		// Defensive: resolve even if a finding for this kind was emitted with an
		// empty group despite the builtin table classifying it under a real one.
		findings = index[bp.ResourceKey("", goKind, namespace, name)]
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
	if !s.requireCloudRole(w, r, auth.RoleOwner, "modify audit settings") {
		return
	}
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
