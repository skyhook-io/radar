package helm

import (
	"time"

	"github.com/skyhook-io/radar/pkg/helmhistory"
	"github.com/skyhook-io/radar/pkg/k8score"
)

type HelmOperation = helmhistory.Operation

// HelmRelease represents a Helm release in the list view
type HelmRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Empty means Helm stores release metadata in Namespace.
	StorageNamespace string          `json:"storageNamespace,omitempty"`
	Chart            string          `json:"chart"`
	ChartVersion     string          `json:"chartVersion"`
	AppVersion       string          `json:"appVersion"`
	Status           string          `json:"status"`
	Revision         int             `json:"revision"`
	Updated          time.Time       `json:"updated"`
	LastOperation    *HelmOperation  `json:"lastOperation,omitempty"`
	Operations       []HelmOperation `json:"operations,omitempty"`
	// Health summary from owned resources
	ResourceHealth string `json:"resourceHealth,omitempty"` // healthy, degraded, unhealthy, unknown
	HealthIssue    string `json:"healthIssue,omitempty"`    // Primary issue if unhealthy (e.g., "OOMKilled")
	HealthSummary  string `json:"healthSummary,omitempty"`  // Brief summary like "2/3 pods ready"
	// ManagedByFluxHelmRelease names the Flux HelmRelease CR that owns this
	// release when Flux's helm-controller installed it. Empty for releases
	// installed via the helm CLI or other tools. Surfaces a "Managed by Flux"
	// affordance in the UI so the user goes to GitOps to manage (changing
	// values via `helm upgrade` here would get reverted on the next
	// reconciliation). Format: "namespace/name".
	ManagedByFluxHelmRelease string `json:"managedByFluxHelmRelease,omitempty"`
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
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Empty means Helm stores release metadata in Namespace.
	StorageNamespace string                `json:"storageNamespace,omitempty"`
	Chart            string                `json:"chart"`
	ChartVersion     string                `json:"chartVersion"`
	AppVersion       string                `json:"appVersion"`
	Status           string                `json:"status"`
	Revision         int                   `json:"revision"`
	Updated          time.Time             `json:"updated"`
	Description      string                `json:"description"`
	Notes            string                `json:"notes"`
	History          []HelmRevision        `json:"history"`
	Resources        []OwnedResource       `json:"resources"`
	ResourceHealth   string                `json:"resourceHealth,omitempty"`
	HealthIssue      string                `json:"healthIssue,omitempty"`
	HealthSummary    string                `json:"healthSummary,omitempty"`
	Hooks            []HelmHook            `json:"hooks,omitempty"`
	HookDiagnostics  []HookDiagnostic      `json:"hookDiagnostics,omitempty"`
	Readme           string                `json:"readme,omitempty"`
	Dependencies     []ChartDependency     `json:"dependencies,omitempty"`
	LastOperation    *HelmOperation        `json:"lastOperation,omitempty"`
	Operations       []HelmOperation       `json:"operations,omitempty"`
	OperationInsight *HelmOperationInsight `json:"operationInsight,omitempty"`
	// See HelmRelease.ManagedByFluxHelmRelease.
	ManagedByFluxHelmRelease string `json:"managedByFluxHelmRelease,omitempty"`
}

type HelmOperationInsight struct {
	State            string                `json:"state"`
	PrimaryResource  *OwnedResource        `json:"primaryResource,omitempty"`
	RelatedResources []OwnedResource       `json:"relatedResources,omitempty"`
	SignalCount      int                   `json:"signalCount,omitempty"`
	SuggestedCompare *HelmSuggestedCompare `json:"suggestedCompare,omitempty"`
}

type HelmSuggestedCompare struct {
	Revision1 int    `json:"revision1"`
	Revision2 int    `json:"revision2"`
	Reason    string `json:"reason,omitempty"`
}

// HelmHook represents a Helm hook (pre/post install, upgrade, etc.)
type HelmHook struct {
	Name              string     `json:"name"`
	Namespace         string     `json:"namespace,omitempty"`
	Kind              string     `json:"kind"`
	Path              string     `json:"path,omitempty"`
	ManifestDigest    string     `json:"-"`
	ManifestChanged   bool       `json:"manifestChanged,omitempty"`
	Events            []string   `json:"events"`
	Weight            int        `json:"weight"`
	Status            string     `json:"status,omitempty"`
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	CompletedAt       *time.Time `json:"completedAt,omitempty"`
	DeletePolicies    []string   `json:"deletePolicies,omitempty"`
	OutputLogPolicies []string   `json:"outputLogPolicies,omitempty"`
}

// HookDiagnostic summarizes failed or suspicious hook state for release forensics.
type HookDiagnostic struct {
	Name                      string        `json:"name"`
	Namespace                 string        `json:"namespace,omitempty"`
	Kind                      string        `json:"kind"`
	Events                    []string      `json:"events,omitempty"`
	Phase                     string        `json:"phase"`
	Message                   string        `json:"message"`
	Evidence                  *HookEvidence `json:"evidence,omitempty"`
	EvidenceUnavailable       bool          `json:"evidenceUnavailable,omitempty"`
	EvidenceUnavailableReason string        `json:"evidenceUnavailableReason,omitempty"`
}

// HookEvidence is best-effort live evidence for a failed or running hook.
type HookEvidence struct {
	Summary string              `json:"summary,omitempty"`
	Jobs    []HookJobEvidence   `json:"jobs,omitempty"`
	Pods    []HookPodEvidence   `json:"pods,omitempty"`
	Events  []HookEventEvidence `json:"events,omitempty"`
	Logs    []HookLogEvidence   `json:"logs,omitempty"`
	Errors  []string            `json:"errors,omitempty"`
}

type HookJobEvidence struct {
	Name       string   `json:"name"`
	Namespace  string   `json:"namespace,omitempty"`
	Status     string   `json:"status,omitempty"`
	Active     int32    `json:"active,omitempty"`
	Succeeded  int32    `json:"succeeded,omitempty"`
	Failed     int32    `json:"failed,omitempty"`
	Conditions []string `json:"conditions,omitempty"`
}

type HookPodEvidence struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace,omitempty"`
	Phase        string `json:"phase,omitempty"`
	Ready        string `json:"ready,omitempty"`
	RestartCount int32  `json:"restartCount,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Message      string `json:"message,omitempty"`
}

type HookEventEvidence struct {
	InvolvedKind string `json:"involvedKind"`
	InvolvedName string `json:"involvedName"`
	Type         string `json:"type,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Message      string `json:"message,omitempty"`
	Count        int32  `json:"count,omitempty"`
	LastSeen     string `json:"lastSeen,omitempty"`
}

type HookLogEvidence struct {
	Pod          string   `json:"pod"`
	Container    string   `json:"container"`
	Previous     bool     `json:"previous,omitempty"`
	Lines        []string `json:"lines,omitempty"`
	TotalLines   int      `json:"totalLines,omitempty"`
	MatchedLines int      `json:"matchedLines,omitempty"`
	Fallback     bool     `json:"fallback,omitempty"`
	Error        string   `json:"error,omitempty"`
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
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion,omitempty"` // e.g. "apps/v1", "cluster.x-k8s.io/v1beta1" — disambiguates CRD kind collisions on navigation
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Status     string `json:"status,omitempty"`  // Running, Pending, Failed, etc.
	Ready      string `json:"ready,omitempty"`   // e.g., "3/3" for deployments
	Message    string `json:"message,omitempty"` // Status message or reason
	Summary    string `json:"summary,omitempty"` // Brief status like "0/3 OOMKilled"
	Issue      string `json:"issue,omitempty"`   // Primary issue if unhealthy
}

// HelmValues represents the values for a release
type HelmValues struct {
	UserSupplied map[string]any `json:"userSupplied"`
	Computed     map[string]any `json:"computed,omitempty"`
}

// ValuesDiff represents a values diff between two revisions.
type ValuesDiff struct {
	Revision1 int    `json:"revision1"`
	Revision2 int    `json:"revision2"`
	AllValues bool   `json:"allValues"`
	Diff      string `json:"diff"`
}

// ManifestDiff represents a diff between two revisions
type ManifestDiff struct {
	Revision1 int    `json:"revision1"`
	Revision2 int    `json:"revision2"`
	Diff      string `json:"diff"`
}

// NotesDiff represents a release notes diff between two revisions.
type NotesDiff struct {
	Revision1 int    `json:"revision1"`
	Revision2 int    `json:"revision2"`
	Diff      string `json:"diff"`
}

// HooksDiff represents hook metadata changes between two revisions.
type HooksDiff struct {
	Revision1 int        `json:"revision1"`
	Revision2 int        `json:"revision2"`
	Added     []HelmHook `json:"added"`
	Removed   []HelmHook `json:"removed"`
	Modified  []HelmHook `json:"modified"`
	Unchanged []HelmHook `json:"unchanged"`
}

// ResourceRef identifies a rendered resource in a Helm revision.
type ResourceRef struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion,omitempty"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
}

// ResourceChange describes a rendered resource that exists in both revisions but
// changed in place.
type ResourceChange struct {
	ResourceRef
	Summary    string                `json:"summary,omitempty"`
	FieldCount int                   `json:"fieldCount"`
	Fields     []k8score.FieldChange `json:"fields"`
}

// ResourceDiff represents rendered resource changes between revisions.
type ResourceDiff struct {
	Revision1       int              `json:"revision1"`
	Revision2       int              `json:"revision2"`
	Added           []ResourceRef    `json:"added"`
	Removed         []ResourceRef    `json:"removed"`
	Modified        []ResourceChange `json:"modified"`
	Unchanged       []ResourceRef    `json:"unchanged"`
	ParseErrorCount int              `json:"parseErrorCount,omitempty"`
}

// UpgradeInfo contains information about available upgrades
type UpgradeInfo struct {
	CurrentVersion  string `json:"currentVersion"`
	LatestVersion   string `json:"latestVersion,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
	RepositoryName  string `json:"repositoryName,omitempty"`
	// SourceType is "repository" for classic HTTP-repo matches and "oci" when the
	// upgrade was discovered via a registered OCI source. Drives how the frontend
	// frames the upgrade and the "source not tracked" affordance.
	SourceType string `json:"sourceType,omitempty"`
	// ChartRef is the oci:// chart reference an OCI-sourced upgrade lives at
	// (display only — the upgrade path re-derives it from registered sources).
	ChartRef string `json:"chartRef,omitempty"`
	Error    string `json:"error,omitempty"`
	// Untracked marks the specific error state where Radar genuinely can't tell
	// where the chart comes from — i.e. registering a chart source could fix it.
	// It is deliberately NOT set for repo-side errors (stale/broken index, classic
	// ambiguity) so the UI doesn't steer the user to register an OCI source when
	// the real fix is refreshing or disambiguating repos.
	Untracked bool `json:"untracked,omitempty"`
}

// BatchUpgradeInfo contains upgrade info for multiple releases
type BatchUpgradeInfo struct {
	// Map of "storageNamespace/name" to UpgradeInfo. For ordinary releases,
	// storageNamespace is the same as the release namespace.
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
	LastUpdated time.Time `json:"lastUpdated"`
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

// StatusPriority returns a sort priority for Helm release statuses.
// Lower values sort first — failed and unhealthy releases are surfaced first.
func StatusPriority(status, resourceHealth string) int {
	if status == "failed" {
		return 0
	}
	if status == "pending-install" || status == "pending-upgrade" || status == "pending-rollback" {
		return 1
	}
	switch resourceHealth {
	case "unhealthy":
		return 2
	case "degraded":
		return 3
	}
	return 4
}

// ReleasePriority returns a sort priority for Helm release rows.
// Lower values sort first. Operation-aware callers should prefer this over
// StatusPriority so recovered failed upgrades are not buried as ordinary
// healthy deployed releases.
func ReleasePriority(r HelmRelease) int {
	if r.Status == "failed" {
		return 0
	}
	if r.Status == "pending-install" || r.Status == "pending-upgrade" || r.Status == "pending-rollback" {
		return 1
	}
	if r.LastOperation != nil {
		switch r.LastOperation.Kind {
		case helmhistory.KindUpgradeFailed, helmhistory.KindReleaseFailed, helmhistory.KindPending:
			return 1
		case helmhistory.KindUpgradeRolledBack:
			return 2
		}
	}
	switch r.ResourceHealth {
	case "unhealthy":
		return 3
	case "degraded":
		return 4
	}
	if r.LastOperation != nil && r.LastOperation.Kind == helmhistory.KindRollback {
		return 5
	}
	return 6
}
