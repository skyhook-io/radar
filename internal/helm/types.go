package helm

import (
	"time"
)

// HelmRelease represents a Helm release in the list view
type HelmRelease struct {
	Name         string    `json:"name"`
	Namespace    string    `json:"namespace"`
	Chart        string    `json:"chart"`
	ChartVersion string    `json:"chartVersion"`
	AppVersion   string    `json:"appVersion"`
	Status       string    `json:"status"`
	Revision     int       `json:"revision"`
	Updated      time.Time `json:"updated"`
	// Health summary from owned resources
	ResourceHealth string `json:"resourceHealth,omitempty"` // healthy, degraded, unhealthy, unknown
	HealthIssue    string `json:"healthIssue,omitempty"`    // Primary issue if unhealthy (e.g., "OOMKilled")
	HealthSummary  string `json:"healthSummary,omitempty"`  // Brief summary like "2/3 pods ready"
}

// HelmRevision represents a single revision in the release history
type HelmRevision struct {
	Revision    int       `json:"revision"`
	Status      string    `json:"status"`
	Chart       string    `json:"chart"`
	AppVersion  string    `json:"appVersion"`
	Description string    `json:"description"`
	Updated     time.Time `json:"updated"`
}

// HelmReleaseDetail contains full details of a Helm release
type HelmReleaseDetail struct {
	Name         string            `json:"name"`
	Namespace    string            `json:"namespace"`
	Chart        string            `json:"chart"`
	ChartVersion string            `json:"chartVersion"`
	AppVersion   string            `json:"appVersion"`
	Status       string            `json:"status"`
	Revision     int               `json:"revision"`
	Updated      time.Time         `json:"updated"`
	Description  string            `json:"description"`
	Notes        string            `json:"notes"`
	History      []HelmRevision    `json:"history"`
	Resources    []OwnedResource   `json:"resources"`
	Hooks        []HelmHook        `json:"hooks,omitempty"`
	Readme       string            `json:"readme,omitempty"`
	Dependencies []ChartDependency `json:"dependencies,omitempty"`
}

// HelmHook represents a Helm hook (pre/post install, upgrade, etc.)
type HelmHook struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`
	Events []string `json:"events"`
	Weight int      `json:"weight"`
	Status string   `json:"status,omitempty"`
}

// ChartDependency represents a chart dependency
type ChartDependency struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Repository string `json:"repository,omitempty"`
	Condition  string `json:"condition,omitempty"`
	Enabled    bool   `json:"enabled"`
}

// OwnedResource represents a K8s resource created by a Helm release
type OwnedResource struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status,omitempty"`  // Running, Pending, Failed, etc.
	Ready     string `json:"ready,omitempty"`   // e.g., "3/3" for deployments
	Message   string `json:"message,omitempty"` // Status message or reason
	Summary   string `json:"summary,omitempty"` // Brief status like "0/3 OOMKilled"
	Issue     string `json:"issue,omitempty"`   // Primary issue if unhealthy
}

// HelmValues represents the values for a release
type HelmValues struct {
	UserSupplied map[string]any `json:"userSupplied"`
	Computed     map[string]any `json:"computed,omitempty"`
}

// ManifestDiff represents a diff between two revisions
type ManifestDiff struct {
	Revision1 int    `json:"revision1"`
	Revision2 int    `json:"revision2"`
	Diff      string `json:"diff"`
}

// UpgradeInfo contains information about available upgrades
type UpgradeInfo struct {
	CurrentVersion  string `json:"currentVersion"`
	LatestVersion   string `json:"latestVersion,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
	RepositoryName  string `json:"repositoryName,omitempty"`
	Error           string `json:"error,omitempty"`
}

// BatchUpgradeInfo contains upgrade info for multiple releases
type BatchUpgradeInfo struct {
	// Map of "namespace/name" to UpgradeInfo
	Releases map[string]*UpgradeInfo `json:"releases"`
}

// ApplyValuesRequest is the request body for applying new values to a release
type ApplyValuesRequest struct {
	Values map[string]any `json:"values"`
}

// ValuesPreviewResponse contains the preview of a values change
type ValuesPreviewResponse struct {
	CurrentValues map[string]any `json:"currentValues"`
	NewValues     map[string]any `json:"newValues"`
	ManifestDiff  string         `json:"manifestDiff"`
}

// HelmRepository represents a configured Helm repository
type HelmRepository struct {
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	LastUpdated time.Time `json:"lastUpdated,omitempty"`
}

// ChartInfo contains basic information about a Helm chart
type ChartInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	AppVersion  string `json:"appVersion,omitempty"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Repository  string `json:"repository"`
	Home        string `json:"home,omitempty"`
	Deprecated  bool   `json:"deprecated,omitempty"`
}

// ChartDetail contains detailed information about a chart version
type ChartDetail struct {
	ChartInfo
	Readme       string         `json:"readme,omitempty"`
	Values       map[string]any `json:"values,omitempty"`
	ValuesSchema string         `json:"valuesSchema,omitempty"`
	Maintainers  []Maintainer   `json:"maintainers,omitempty"`
	Sources      []string       `json:"sources,omitempty"`
	Keywords     []string       `json:"keywords,omitempty"`
}

// Maintainer represents a chart maintainer
type Maintainer struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	URL   string `json:"url,omitempty"`
}

// InstallRequest is the request body for installing a new chart
type InstallRequest struct {
	ReleaseName     string         `json:"releaseName"`
	Namespace       string         `json:"namespace"`
	ChartName       string         `json:"chartName"`
	Version         string         `json:"version"`
	Repository      string         `json:"repository"`
	Values          map[string]any `json:"values,omitempty"`
	CreateNamespace bool           `json:"createNamespace,omitempty"`
}

// ChartSearchResult contains search results for charts
type ChartSearchResult struct {
	Charts []ChartInfo `json:"charts"`
	Total  int         `json:"total"`
}

// ============================================================================
// ArtifactHub Types
// ============================================================================

// ArtifactHubChart represents a chart from ArtifactHub with rich metadata
type ArtifactHubChart struct {
	PackageID   string                `json:"packageId"`
	Name        string                `json:"name"`
	Version     string                `json:"version"`
	AppVersion  string                `json:"appVersion,omitempty"`
	Description string                `json:"description,omitempty"`
	LogoURL     string                `json:"logoUrl,omitempty"`
	HomeURL     string                `json:"homeUrl,omitempty"`
	Deprecated  bool                  `json:"deprecated,omitempty"`
	Repository  ArtifactHubRepository `json:"repository"`
	Stars       int                   `json:"stars"`
	License     string                `json:"license,omitempty"`
	CreatedAt   int64                 `json:"createdAt,omitempty"` // Unix timestamp
	UpdatedAt   int64                 `json:"updatedAt,omitempty"` // Unix timestamp
	Signed      bool                  `json:"signed,omitempty"`
	Security    *ArtifactHubSecurity  `json:"security,omitempty"`
	OrgCount    int                   `json:"productionOrgsCount,omitempty"` // Production organizations using this
	HasSchema   bool                  `json:"hasValuesSchema,omitempty"`
	Keywords    []string              `json:"keywords,omitempty"`
}

// ArtifactHubRepository contains repository info from ArtifactHub
type ArtifactHubRepository struct {
	Name              string `json:"name"`
	URL               string `json:"url"`
	Official          bool   `json:"official,omitempty"`
	VerifiedPublisher bool   `json:"verifiedPublisher,omitempty"`
	OrganizationName  string `json:"organizationName,omitempty"`
}

// ArtifactHubSecurity contains security report summary
type ArtifactHubSecurity struct {
	Critical int `json:"critical,omitempty"`
	High     int `json:"high,omitempty"`
	Medium   int `json:"medium,omitempty"`
	Low      int `json:"low,omitempty"`
	Unknown  int `json:"unknown,omitempty"`
}

// ArtifactHubSearchResult contains search results from ArtifactHub
type ArtifactHubSearchResult struct {
	Charts []ArtifactHubChart `json:"charts"`
	Total  int                `json:"total"`
	Facets []ArtifactHubFacet `json:"facets,omitempty"`
}

// ArtifactHubFacet represents a search facet (for filtering)
type ArtifactHubFacet struct {
	Title   string                   `json:"title"`
	Options []ArtifactHubFacetOption `json:"options"`
}

// ArtifactHubFacetOption represents a facet option
type ArtifactHubFacetOption struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Total int    `json:"total"`
}

// ArtifactHubChartDetail contains detailed chart info from ArtifactHub
type ArtifactHubChartDetail struct {
	ArtifactHubChart
	Readme       string                      `json:"readme,omitempty"`
	Values       string                      `json:"values,omitempty"` // Default values as string
	ValuesSchema string                      `json:"valuesSchema,omitempty"`
	Maintainers  []ArtifactHubMaintainer     `json:"maintainers,omitempty"`
	Links        []ArtifactHubLink           `json:"links,omitempty"`
	Versions     []ArtifactHubVersionSummary `json:"availableVersions,omitempty"`
	Install      string                      `json:"install,omitempty"` // Install instructions
}

// ArtifactHubMaintainer represents a chart maintainer
type ArtifactHubMaintainer struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

// ArtifactHubLink represents a useful link for the chart
type ArtifactHubLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ArtifactHubVersionSummary contains version summary info
type ArtifactHubVersionSummary struct {
	Version   string `json:"version"`
	CreatedAt int64  `json:"ts,omitempty"`
}

// InstallProgress represents progress during a Helm install
type InstallProgress struct {
	Phase   string `json:"phase"`            // e.g., "downloading", "installing", "waiting"
	Message string `json:"message"`          // Human-readable status message
	Detail  string `json:"detail,omitempty"` // Additional detail (e.g., command output)
}
