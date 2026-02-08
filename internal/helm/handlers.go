package helm

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
)

// Handlers provides HTTP handlers for Helm endpoints
type Handlers struct{}

// NewHandlers creates a new Handlers instance
func NewHandlers() *Handlers {
	return &Handlers{}
}

// RegisterRoutes registers Helm routes on the given router
func (h *Handlers) RegisterRoutes(r chi.Router) {
	r.Route("/helm", func(r chi.Router) {
		// Release management
		r.Get("/releases", h.handleListReleases)
		r.Post("/releases", h.handleInstall)
		r.Post("/releases/install-stream", h.handleInstallStream)
		r.Get("/releases/{namespace}/{name}", h.handleGetRelease)
		r.Get("/releases/{namespace}/{name}/manifest", h.handleGetManifest)
		r.Get("/releases/{namespace}/{name}/values", h.handleGetValues)
		r.Get("/releases/{namespace}/{name}/diff", h.handleGetDiff)
		r.Get("/releases/{namespace}/{name}/upgrade-info", h.handleCheckUpgrade)
		r.Get("/upgrade-check", h.handleBatchUpgradeCheck)
		// Actions (write operations)
		r.Post("/releases/{namespace}/{name}/rollback", h.handleRollback)
		r.Post("/releases/{namespace}/{name}/upgrade", h.handleUpgrade)
		r.Post("/releases/{namespace}/{name}/values/preview", h.handlePreviewValues)
		r.Put("/releases/{namespace}/{name}/values", h.handleApplyValues)
		r.Delete("/releases/{namespace}/{name}", h.handleUninstall)

		// Chart browser (local repositories)
		r.Get("/repositories", h.handleListRepositories)
		r.Post("/repositories/{name}/update", h.handleUpdateRepository)
		r.Get("/charts", h.handleSearchCharts)
		r.Get("/charts/{repo}/{chart}", h.handleGetChartDetail)
		r.Get("/charts/{repo}/{chart}/{version}", h.handleGetChartDetailVersion)

		// ArtifactHub integration
		r.Get("/artifacthub/search", h.handleArtifactHubSearch)
		r.Get("/artifacthub/charts/{repo}/{chart}", h.handleArtifactHubChart)
		r.Get("/artifacthub/charts/{repo}/{chart}/{version}", h.handleArtifactHubChartVersion)
	})
}

// handleListReleases returns all Helm releases
func (h *Handlers) handleListReleases(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := r.URL.Query().Get("namespace")

	releases, err := client.ListReleases(namespace)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, releases)
}

// handleGetRelease returns details for a specific release
func (h *Handlers) handleGetRelease(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	release, err := client.GetRelease(namespace, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, release)
}

// handleGetManifest returns the rendered manifest for a release
func (h *Handlers) handleGetManifest(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Optional revision parameter
	revision := 0
	if revStr := r.URL.Query().Get("revision"); revStr != "" {
		if rev, err := strconv.Atoi(revStr); err == nil {
			revision = rev
		}
	}

	manifest, err := client.GetManifest(namespace, name, revision)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return as plain text YAML
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(manifest))
}

// handleGetValues returns the values for a release
func (h *Handlers) handleGetValues(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	allValues := r.URL.Query().Get("all") == "true"

	values, err := client.GetValues(namespace, name, allValues)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, values)
}

// handleGetDiff returns the diff between two revisions
func (h *Handlers) handleGetDiff(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	rev1Str := r.URL.Query().Get("revision1")
	rev2Str := r.URL.Query().Get("revision2")

	if rev1Str == "" || rev2Str == "" {
		writeError(w, http.StatusBadRequest, "revision1 and revision2 parameters are required")
		return
	}

	rev1, err := strconv.Atoi(rev1Str)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid revision1 parameter")
		return
	}

	rev2, err := strconv.Atoi(rev2Str)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid revision2 parameter")
		return
	}

	diff, err := client.GetManifestDiff(namespace, name, rev1, rev2)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, diff)
}

// handleCheckUpgrade checks if a newer version is available
func (h *Handlers) handleCheckUpgrade(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	info, err := client.CheckForUpgrade(namespace, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, info)
}

// handleBatchUpgradeCheck checks all releases for upgrades at once
func (h *Handlers) handleBatchUpgradeCheck(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := r.URL.Query().Get("namespace")

	info, err := client.BatchCheckUpgrades(namespace)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, info)
}

// handleRollback rolls back a release to a previous revision
func (h *Handlers) handleRollback(w http.ResponseWriter, r *http.Request) {
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	revStr := r.URL.Query().Get("revision")
	if revStr == "" {
		writeError(w, http.StatusBadRequest, "revision parameter is required")
		return
	}

	revision, err := strconv.Atoi(revStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid revision parameter")
		return
	}

	if err := client.Rollback(namespace, name, revision); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "success", "message": "Rollback completed"})
}

// handleUninstall removes a release
func (h *Handlers) handleUninstall(w http.ResponseWriter, r *http.Request) {
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if err := client.Uninstall(namespace, name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "success", "message": "Release uninstalled"})
}

// handleUpgrade upgrades a release to a new version
func (h *Handlers) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	version := r.URL.Query().Get("version")
	if version == "" {
		writeError(w, http.StatusBadRequest, "version parameter is required")
		return
	}

	if err := client.Upgrade(namespace, name, version); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "success", "message": "Upgrade completed"})
}

// handlePreviewValues previews the effect of new values on a release
func (h *Handlers) handlePreviewValues(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	var req ApplyValuesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	preview, err := client.PreviewValuesChange(namespace, name, req.Values)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, preview)
}

// handleApplyValues applies new values to a release
func (h *Handlers) handleApplyValues(w http.ResponseWriter, r *http.Request) {
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	var req ApplyValuesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if err := client.ApplyValues(namespace, name, req.Values); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "success", "message": "Values applied successfully"})
}

// ============================================================================
// Chart Browser Handlers
// ============================================================================

// handleListRepositories returns all configured Helm repositories
func (h *Handlers) handleListRepositories(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	repos, err := client.ListRepositories()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, repos)
}

// handleUpdateRepository updates the index for a specific repository
func (h *Handlers) handleUpdateRepository(w http.ResponseWriter, r *http.Request) {
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	repoName := chi.URLParam(r, "name")
	if repoName == "" {
		writeError(w, http.StatusBadRequest, "repository name is required")
		return
	}

	if err := client.UpdateRepository(repoName); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "success", "message": "Repository updated"})
}

// handleSearchCharts searches for charts across all repositories
func (h *Handlers) handleSearchCharts(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	query := r.URL.Query().Get("query")
	allVersions := r.URL.Query().Get("allVersions") == "true"

	result, err := client.SearchCharts(query, allVersions)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, result)
}

// handleGetChartDetail returns detailed info about a chart (latest version)
func (h *Handlers) handleGetChartDetail(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	repoName := chi.URLParam(r, "repo")
	chartName := chi.URLParam(r, "chart")

	detail, err := client.GetChartDetail(repoName, chartName, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, detail)
}

// handleGetChartDetailVersion returns detailed info about a specific chart version
func (h *Handlers) handleGetChartDetailVersion(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	repoName := chi.URLParam(r, "repo")
	chartName := chi.URLParam(r, "chart")
	version := chi.URLParam(r, "version")

	detail, err := client.GetChartDetail(repoName, chartName, version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, detail)
}

// handleInstall installs a new Helm release (non-streaming version)
func (h *Handlers) handleInstall(w http.ResponseWriter, r *http.Request) {
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	var req InstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Validate required fields
	if req.ReleaseName == "" {
		writeError(w, http.StatusBadRequest, "releaseName is required")
		return
	}
	if req.Namespace == "" {
		writeError(w, http.StatusBadRequest, "namespace is required")
		return
	}
	if req.ChartName == "" {
		writeError(w, http.StatusBadRequest, "chartName is required")
		return
	}
	if req.Repository == "" {
		writeError(w, http.StatusBadRequest, "repository is required")
		return
	}

	release, err := client.Install(&req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, release)
}

// handleInstallStream installs a Helm release with SSE progress streaming
func (h *Handlers) handleInstallStream(w http.ResponseWriter, r *http.Request) {
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	var req InstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Validate required fields
	if req.ReleaseName == "" {
		writeError(w, http.StatusBadRequest, "releaseName is required")
		return
	}
	if req.Namespace == "" {
		writeError(w, http.StatusBadRequest, "namespace is required")
		return
	}
	if req.ChartName == "" {
		writeError(w, http.StatusBadRequest, "chartName is required")
		return
	}
	if req.Repository == "" {
		writeError(w, http.StatusBadRequest, "repository is required")
		return
	}

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Create progress channel
	progressCh := make(chan InstallProgress, 10)
	defer close(progressCh)

	// Start install in goroutine
	resultCh := make(chan installResult, 1)
	go func() {
		release, err := client.InstallWithProgress(&req, progressCh)
		resultCh <- installResult{release: release, err: err}
	}()

	// Stream progress events
	for {
		select {
		case progress, ok := <-progressCh:
			if !ok {
				return
			}
			event := map[string]any{
				"type":    "progress",
				"phase":   progress.Phase,
				"message": progress.Message,
			}
			if progress.Detail != "" {
				event["detail"] = progress.Detail
			}
			data, _ := json.Marshal(event)
			w.Write([]byte("data: " + string(data) + "\n\n"))
			flusher.Flush()

		case result := <-resultCh:
			if result.err != nil {
				event := map[string]any{
					"type":    "error",
					"message": result.err.Error(),
				}
				data, _ := json.Marshal(event)
				w.Write([]byte("data: " + string(data) + "\n\n"))
			} else {
				event := map[string]any{
					"type":    "complete",
					"release": result.release,
				}
				data, _ := json.Marshal(event)
				w.Write([]byte("data: " + string(data) + "\n\n"))
			}
			flusher.Flush()
			return

		case <-r.Context().Done():
			return
		}
	}
}

type installResult struct {
	release *HelmRelease
	err     error
}

// Helper functions

// requireHelmWrite checks if the service account has Helm write permissions.
// Uses secrets/create as a sentinel check — if the service account can create
// secrets, it likely has the broad RBAC granted by rbac.helm=true.
// Returns true if the request should proceed, false if an error was written.
func requireHelmWrite(w http.ResponseWriter, r *http.Request) bool {
	caps, err := k8s.CheckCapabilities(r.Context())
	if err != nil {
		log.Printf("[helm] Failed to check capabilities for %s %s: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusInternalServerError, "failed to check capabilities: "+err.Error())
		return false
	}
	if !caps.HelmWrite {
		log.Printf("[helm] Denied %s %s: helmWrite capability not available", r.Method, r.URL.Path)
		writeError(w, http.StatusForbidden, "Helm write operations require additional RBAC permissions. Set rbac.helm=true in the Radar Helm chart values.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// ============================================================================
// ArtifactHub Handlers
// ============================================================================

// handleArtifactHubSearch searches for charts on ArtifactHub
func (h *Handlers) handleArtifactHubSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("query")
	if query == "" {
		query = "*" // Search all
	}

	// Parse pagination params
	offset := 0
	limit := 60
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if val, err := strconv.Atoi(offsetStr); err == nil {
			offset = val
		}
	}
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if val, err := strconv.Atoi(limitStr); err == nil && val > 0 && val <= 100 {
			limit = val
		}
	}

	// Parse filters
	official := r.URL.Query().Get("official") == "true"
	verified := r.URL.Query().Get("verified") == "true"

	// Parse sort parameter (relevance, stars, last_updated)
	sort := r.URL.Query().Get("sort")

	result, err := SearchArtifactHub(query, offset, limit, official, verified, sort)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, result)
}

// handleArtifactHubChart gets chart details from ArtifactHub (latest version)
func (h *Handlers) handleArtifactHubChart(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	chartName := chi.URLParam(r, "chart")

	detail, err := GetArtifactHubChart(repoName, chartName, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, detail)
}

// handleArtifactHubChartVersion gets chart details from ArtifactHub for a specific version
func (h *Handlers) handleArtifactHubChartVersion(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	chartName := chi.URLParam(r, "chart")
	version := chi.URLParam(r, "version")

	detail, err := GetArtifactHubChart(repoName, chartName, version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, detail)
}
