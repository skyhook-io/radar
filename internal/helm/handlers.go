package helm

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
)

// IsForbiddenError checks if an error is a Kubernetes RBAC forbidden error
func IsForbiddenError(err error) bool {
	if err == nil {
		return false
	}
	errLower := strings.ToLower(err.Error())
	return strings.Contains(errLower, "forbidden") || strings.Contains(errLower, "unauthorized")
}

// userCreds pulls the auth user off the request for *AsUser helpers.
// Returns ("", nil) when no user is attached (auth disabled / local binary),
// which the *AsUser methods treat as "use the SA identity".
func userCreds(r *http.Request) (string, []string) {
	if user := auth.UserFromContext(r.Context()); user != nil {
		return user.Username, user.Groups
	}
	return "", nil
}

// Handlers provides HTTP handlers for Helm endpoints
type Handlers struct {
	// resolveNamespaces maps a request to the namespaces a Helm list should
	// query. It returns (nil, true) for cluster-wide access, (namespaces, true)
	// to list those namespaces and merge, and (_, false) when the identity has
	// no namespace access. Injected by the server so the helm package doesn't
	// depend on its per-user RBAC plumbing. May be nil in tests that don't
	// exercise the list endpoints.
	resolveNamespaces func(r *http.Request) ([]string, bool)
}

// NewHandlers creates a new Handlers instance. resolveNamespaces lets the list
// endpoints degrade gracefully for namespace-restricted identities (see
// handleListReleases); pass nil only in tests that don't hit those routes.
func NewHandlers(resolveNamespaces func(r *http.Request) ([]string, bool)) *Handlers {
	return &Handlers{resolveNamespaces: resolveNamespaces}
}

// listNamespaces resolves which namespaces a Helm list should query. Falls back
// to cluster-wide (nil, true) when no resolver is wired.
func (h *Handlers) listNamespaces(r *http.Request) ([]string, bool) {
	if h.resolveNamespaces == nil {
		return nil, true
	}
	return h.resolveNamespaces(r)
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
		r.Get("/releases/{namespace}/{name}/values/diff", h.handleGetValuesDiff)
		r.Get("/releases/{namespace}/{name}/values", h.handleGetValues)
		r.Get("/releases/{namespace}/{name}/diff", h.handleGetDiff)
		r.Get("/releases/{namespace}/{name}/notes/diff", h.handleGetNotesDiff)
		r.Get("/releases/{namespace}/{name}/hooks/diff", h.handleGetHooksDiff)
		r.Get("/releases/{namespace}/{name}/resources/diff", h.handleGetResourceDiff)
		r.Get("/releases/{namespace}/{name}/upgrade-info", h.handleCheckUpgrade)
		r.Get("/releases/{namespace}/{name}/versions", h.handleAvailableVersions)
		r.Get("/upgrade-check", h.handleBatchUpgradeCheck)
		// Actions (write operations)
		r.Post("/releases/{namespace}/{name}/rollback", h.handleRollback)
		r.Post("/releases/{namespace}/{name}/rollback-stream", h.handleRollbackStream)
		r.Post("/releases/{namespace}/{name}/upgrade", h.handleUpgrade)
		r.Post("/releases/{namespace}/{name}/upgrade-stream", h.handleUpgradeStream)
		r.Post("/releases/{namespace}/{name}/values/preview", h.handlePreviewValues)
		r.Put("/releases/{namespace}/{name}/values", h.handleApplyValues)
		r.Delete("/releases/{namespace}/{name}", h.handleUninstall)

		// Chart browser (local repositories)
		r.Get("/repositories", h.handleListRepositories)
		r.Post("/repositories/{name}/update", h.handleUpdateRepository)

		// Registered OCI chart sources (the OCI analog of `helm repo add`) — let
		// Radar track upgrades for the user's own OCI-published charts.
		r.Get("/oci-sources", h.handleListOCISources)
		r.Post("/oci-sources", h.handleAddOCISource)
		r.Delete("/oci-sources", h.handleRemoveOCISource)
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

	// Resolve which namespaces to list. An explicit ?namespace= is honored by
	// the resolver (via the request query); when none is given, a
	// namespace-restricted identity resolves to its accessible namespaces
	// instead of a cluster-wide `list secrets` that would 403.
	namespaces, ok := h.listNamespaces(r)
	if !ok {
		writeJSON(w, []HelmRelease{})
		return
	}

	username, groups := userCreds(r)
	releases, err := client.ListReleasesAcrossNamespaces(namespaces, username, groups)
	if err != nil {
		if IsForbiddenError(err) {
			writeError(w, http.StatusForbidden, "insufficient permissions to list Helm releases")
			return
		}
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

	username, groups := userCreds(r)
	release, err := client.GetReleaseAsUser(namespace, name, username, groups)
	if err != nil {
		if IsForbiddenError(err) {
			writeError(w, http.StatusForbidden, "insufficient permissions to get Helm release")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	EnrichHookDiagnosticsWithClusterEvidence(r.Context(), release, k8s.ClientFromContext(r.Context()))

	writeJSON(w, release)
}

// handleGetManifest returns the rendered manifest for a release.
// Member+ only — manifests can inline literal Secret resources with
// base64-encoded data, which K8s 'view' (the default cloud:viewer
// binding) excludes.
func (h *Handlers) handleGetManifest(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "view Helm release manifests") {
		return
	}
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

	username, groups := userCreds(r)
	manifest, err := client.GetManifestAsUser(namespace, name, revision, username, groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return as plain text YAML
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(manifest))
}

// handleGetValues returns the values for a release. Member+ only —
// values may contain credentials set via --set or values.yaml.
func (h *Handlers) handleGetValues(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "view Helm release values") {
		return
	}
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	allValues := r.URL.Query().Get("all") == "true"
	revision := 0
	if revStr := r.URL.Query().Get("revision"); revStr != "" {
		rev, err := strconv.Atoi(revStr)
		if err != nil || rev <= 0 {
			writeError(w, http.StatusBadRequest, "invalid revision parameter")
			return
		}
		revision = rev
	}

	username, groups := userCreds(r)
	values, err := client.GetValuesRevisionAsUser(namespace, name, allValues, revision, username, groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, values)
}

// handleGetValuesDiff returns a values diff between two release revisions.
// Member+ only — values often contain credentials.
func (h *Handlers) handleGetValuesDiff(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "diff Helm release values") {
		return
	}
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	rev1, rev2, ok := parseRevisionPair(w, r)
	if !ok {
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	allValues := r.URL.Query().Get("all") == "true"
	username, groups := userCreds(r)
	diff, err := client.GetValuesDiffAsUser(namespace, name, rev1, rev2, allValues, username, groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, diff)
}

// handleGetDiff returns the diff between two revisions. Member+ only
// — same surface as GetManifest (renders both revisions).
func (h *Handlers) handleGetDiff(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "diff Helm release manifests") {
		return
	}
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	rev1, rev2, ok := parseRevisionPair(w, r)
	if !ok {
		return
	}

	username, groups := userCreds(r)
	diff, err := client.GetManifestDiffAsUser(namespace, name, rev1, rev2, username, groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, diff)
}

// handleGetNotesDiff returns a release notes diff between two revisions.
func (h *Handlers) handleGetNotesDiff(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "diff Helm release notes") {
		return
	}
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}
	rev1, rev2, ok := parseRevisionPair(w, r)
	if !ok {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	username, groups := userCreds(r)
	diff, err := client.GetNotesDiffAsUser(namespace, name, rev1, rev2, username, groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, diff)
}

// handleGetHooksDiff returns a release hook metadata diff between two revisions.
func (h *Handlers) handleGetHooksDiff(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "diff Helm release hooks") {
		return
	}
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}
	rev1, rev2, ok := parseRevisionPair(w, r)
	if !ok {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	username, groups := userCreds(r)
	diff, err := client.GetHooksDiffAsUser(namespace, name, rev1, rev2, username, groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, diff)
}

// handleGetResourceDiff returns added/removed rendered resources between revisions.
func (h *Handlers) handleGetResourceDiff(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "diff Helm release resources") {
		return
	}
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}
	rev1, rev2, ok := parseRevisionPair(w, r)
	if !ok {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	username, groups := userCreds(r)
	diff, err := client.GetResourceDiffAsUser(namespace, name, rev1, rev2, username, groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, diff)
}

func parseRevisionPair(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	rev1Str := r.URL.Query().Get("revision1")
	rev2Str := r.URL.Query().Get("revision2")
	if rev1Str == "" || rev2Str == "" {
		writeError(w, http.StatusBadRequest, "revision1 and revision2 parameters are required")
		return 0, 0, false
	}
	rev1, err := strconv.Atoi(rev1Str)
	if err != nil || rev1 <= 0 {
		writeError(w, http.StatusBadRequest, "invalid revision1 parameter")
		return 0, 0, false
	}
	rev2, err := strconv.Atoi(rev2Str)
	if err != nil || rev2 <= 0 {
		writeError(w, http.StatusBadRequest, "invalid revision2 parameter")
		return 0, 0, false
	}
	if rev1 == rev2 {
		writeError(w, http.StatusBadRequest, "revision1 and revision2 must differ")
		return 0, 0, false
	}
	return rev1, rev2, true
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

	username, groups := userCreds(r)
	info, err := client.CheckForUpgradeAsUser(namespace, name, username, groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, info)
}

// handleAvailableVersions returns the newest-first list of chart versions this
// release could be upgraded/downgraded to, so the upgrade dialog can offer a
// specific target version. Returns [] when the source can't be resolved.
func (h *Handlers) handleAvailableVersions(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	username, groups := userCreds(r)
	versions, err := client.AvailableVersionsAsUser(namespace, name, username, groups)
	if err != nil {
		if IsForbiddenError(err) {
			writeError(w, http.StatusForbidden, "insufficient permissions to read Helm release")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if versions == nil {
		versions = []string{}
	}
	writeJSON(w, versions)
}

// handleBatchUpgradeCheck checks all releases for upgrades at once
func (h *Handlers) handleBatchUpgradeCheck(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespaces, ok := h.listNamespaces(r)
	if !ok {
		writeJSON(w, &BatchUpgradeInfo{Releases: map[string]*UpgradeInfo{}})
		return
	}

	username, groups := userCreds(r)
	info, err := client.BatchCheckUpgradesAcrossNamespaces(namespaces, username, groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, info)
}

// handleRollback rolls back a release to a previous revision
func (h *Handlers) handleRollback(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "rollback Helm releases") {
		return
	}
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

	auth.AuditLog(r, namespace, name)
	var rollbackErr error
	if user := auth.UserFromContext(r.Context()); user != nil {
		rollbackErr = client.RollbackAsUser(namespace, name, revision, user.Username, user.Groups)
	} else {
		rollbackErr = client.Rollback(namespace, name, revision)
	}
	if err := rollbackErr; err != nil {
		if IsForbiddenError(err) {
			writeError(w, http.StatusForbidden, "insufficient permissions to rollback Helm release")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "success", "message": "Rollback completed"})
}

// handleRollbackStream rolls back a release with SSE progress streaming
func (h *Handlers) handleRollbackStream(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "rollback Helm releases") {
		return
	}
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

	progressCh := make(chan InstallProgress, 10)
	defer close(progressCh)

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- client.RollbackWithProgress(namespace, name, revision, progressCh)
	}()

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

		case err := <-resultCh:
			if err != nil {
				event := map[string]any{
					"type":    "error",
					"message": err.Error(),
				}
				data, _ := json.Marshal(event)
				w.Write([]byte("data: " + string(data) + "\n\n"))
			} else {
				event := map[string]any{
					"type":    "complete",
					"message": "Rollback completed successfully",
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

// handleUninstall removes a release
func (h *Handlers) handleUninstall(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "uninstall Helm releases") {
		return
	}
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

	auth.AuditLog(r, namespace, name)
	var uninstallErr error
	if user := auth.UserFromContext(r.Context()); user != nil {
		uninstallErr = client.UninstallAsUser(namespace, name, user.Username, user.Groups)
	} else {
		uninstallErr = client.Uninstall(namespace, name)
	}
	if err := uninstallErr; err != nil {
		if IsForbiddenError(err) {
			writeError(w, http.StatusForbidden, "insufficient permissions to uninstall Helm release")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "success", "message": "Release uninstalled"})
}

// handleUpgrade upgrades a release to a new version
func (h *Handlers) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "upgrade Helm releases") {
		return
	}
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
	repositoryName := r.URL.Query().Get("repository")

	auth.AuditLog(r, namespace, name)
	var upgradeErr error
	if user := auth.UserFromContext(r.Context()); user != nil {
		upgradeErr = client.UpgradeAsUser(namespace, name, version, repositoryName, user.Username, user.Groups)
	} else {
		upgradeErr = client.Upgrade(namespace, name, version, repositoryName)
	}
	if err := upgradeErr; err != nil {
		if IsForbiddenError(err) {
			writeError(w, http.StatusForbidden, "insufficient permissions to upgrade Helm release")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "success", "message": "Upgrade completed"})
}

// handleUpgradeStream upgrades a release with SSE progress streaming
func (h *Handlers) handleUpgradeStream(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "upgrade Helm releases") {
		return
	}
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
	repositoryName := r.URL.Query().Get("repository")

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

	progressCh := make(chan InstallProgress, 10)
	defer close(progressCh)

	resultCh := make(chan error, 1)
	go func() {
		if user := auth.UserFromContext(r.Context()); user != nil {
			resultCh <- client.UpgradeWithProgressAsUser(namespace, name, version, repositoryName, user.Username, user.Groups, progressCh)
			return
		}
		resultCh <- client.UpgradeWithProgress(namespace, name, version, repositoryName, progressCh)
	}()

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

		case err := <-resultCh:
			if err != nil {
				event := map[string]any{
					"type":    "error",
					"message": err.Error(),
				}
				data, _ := json.Marshal(event)
				w.Write([]byte("data: " + string(data) + "\n\n"))
			} else {
				event := map[string]any{
					"type":    "complete",
					"message": "Upgrade completed successfully",
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

// handlePreviewValues previews the effect of new values on a release.
// Member+ — renders the chart with proposed values, same surface as
// GetManifest.
func (h *Handlers) handlePreviewValues(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "preview Helm release values") {
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

	preview, err := client.PreviewValuesChange(namespace, name, req.Values)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, preview)
}

// handleApplyValues applies new values to a release
func (h *Handlers) handleApplyValues(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "apply Helm release values") {
		return
	}
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

	auth.AuditLog(r, namespace, name)
	var applyErr error
	if user := auth.UserFromContext(r.Context()); user != nil {
		applyErr = client.ApplyValuesAsUser(namespace, name, req.Values, user.Username, user.Groups)
	} else {
		applyErr = client.ApplyValues(namespace, name, req.Values)
	}
	if err := applyErr; err != nil {
		if IsForbiddenError(err) {
			writeError(w, http.StatusForbidden, "insufficient permissions to apply Helm values")
			return
		}
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

// handleUpdateRepository updates the index for a specific repository.
//
// Deliberately NOT gated by requireCloudRole: this fetches chart
// metadata from external repos (artifacthub.io, oci://, etc.) and
// caches it on the radar pod's local filesystem. It mutates pod-local
// state, not cluster state — refresh-the-catalog rather than
// modify-the-cluster. requireHelmWrite still gates it because a future
// install/upgrade depends on a fresh repo cache, but a viewer
// triggering a repo refresh has no security or product cost beyond a
// few HTTP calls to public chart hosts.
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

// ociSourceRequest is the body for registering/unregistering an OCI chart source.
type ociSourceRequest struct {
	Source string `json:"source"`
}

// handleListOCISources returns the registered OCI chart-source prefixes.
func (h *Handlers) handleListOCISources(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, ListOCISources())
}

// handleAddOCISource registers an OCI chart-source prefix. Gated by
// requireHelmWrite (same as repo refresh): it mutates pod-local config and
// underpins later upgrades, but is not a cluster mutation.
func (h *Handlers) handleAddOCISource(w http.ResponseWriter, r *http.Request) {
	if !requireHelmWrite(w, r) {
		return
	}
	var req ociSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	sources, err := AddOCISource(req.Source)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, sources)
}

// handleRemoveOCISource unregisters an OCI chart-source prefix.
func (h *Handlers) handleRemoveOCISource(w http.ResponseWriter, r *http.Request) {
	if !requireHelmWrite(w, r) {
		return
	}
	var req ociSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	sources, err := RemoveOCISource(req.Source)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, sources)
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
	if !requireCloudRole(w, r, auth.RoleMember, "install Helm releases") {
		return
	}
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

	auth.AuditLog(r, req.Namespace, req.ReleaseName)
	var release *HelmRelease
	var installErr error
	if user := auth.UserFromContext(r.Context()); user != nil {
		release, installErr = client.InstallAsUser(&req, user.Username, user.Groups)
	} else {
		release, installErr = client.Install(&req)
	}
	if err := installErr; err != nil {
		log.Printf("[helm] install %q/%q (chart=%q repo=%q) failed: %v", req.Namespace, req.ReleaseName, req.ChartName, req.Repository, err)
		writeInstallError(w, err)
		return
	}

	writeJSON(w, release)
}

// handleInstallStream installs a Helm release with SSE progress streaming
func (h *Handlers) handleInstallStream(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "install Helm releases") {
		return
	}
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
	auth.AuditLog(r, req.Namespace, req.ReleaseName)
	user := auth.UserFromContext(r.Context())
	resultCh := make(chan installResult, 1)
	go func() {
		var release *HelmRelease
		var err error
		if user != nil {
			release, err = client.InstallWithProgressAsUser(&req, progressCh, user.Username, user.Groups)
		} else {
			release, err = client.InstallWithProgress(&req, progressCh)
		}
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
				log.Printf("[helm] install %q/%q (chart=%q repo=%q) failed: %v", req.Namespace, req.ReleaseName, req.ChartName, req.Repository, result.err)
				data, _ := json.Marshal(installStreamErrorEvent(result.err))
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
	if status >= 500 && k8s.MarkDisconnectedIfClusterUnreachable(message) {
		status = http.StatusServiceUnavailable
	}
	if status >= 500 {
		errorlog.Record("helm", "error", "%s", message)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// writeErrorCode is writeError with a stable machine-readable error_code
// in the response body so the frontend + MCP clients can branch on the error
// type without parsing the human message. Used for role-gated 403s and
// any other case where the consumer wants to react differently per code.
func writeErrorCode(w http.ResponseWriter, status int, code, message string) {
	if status >= 500 {
		errorlog.Record("helm", "error", "%s", message)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error":      message,
		"error_code": code,
	})
}

// requireCloudRole gates a handler on the caller's Cloud role tier.
// Returns true if the request should proceed.
//
// When the caller has no Cloud role (OSS deploy, or running outside
// Cloud's tunnel), CloudRole.AtLeast bypasses the gate — radar OSS
// continues to use only K8s RBAC for authorization, no Cloud-specific
// product gating. This is the same behavior as before; the gate is
// strictly additive for Cloud-attributed callers.
//
// When the caller IS Cloud-attributed and their tier is below `min`,
// returns 403 with error_code=cloud_role_insufficient so the frontend can
// render a friendly "your role doesn't allow this" message instead of
// a generic auth failure.
func requireCloudRole(w http.ResponseWriter, r *http.Request, min auth.CloudRole, opName string) bool {
	role := auth.CloudRoleFromContext(r.Context())
	if role.AtLeast(min) {
		return true
	}
	username := "unknown"
	if u := auth.UserFromContext(r.Context()); u != nil {
		username = u.Username
	}
	// All user-controlled values use %q so log-line injection via CR/LF
	// in headers or path is escaped. opName is a compile-time literal.
	log.Printf("[helm] Cloud role %q denied %s for user %q (need at least %q): %q", role, opName, username, min, r.URL.Path)
	writeErrorCode(w, http.StatusForbidden, auth.ErrCodeCloudRoleInsufficient,
		"Your Radar Cloud role ("+role.String()+") cannot "+opName+". Requires "+string(min)+" or higher.")
	return false
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
