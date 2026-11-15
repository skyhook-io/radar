// Package vulnerabilities rolls scanner findings up to the images that live
// workloads run right now: by image, by CVE and by workload. The wire shape
// names no scanner; Trivy Operator's VulnerabilityReports are the one source
// read today (Source says so).
package vulnerabilities

import (
	"time"

	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// View picks the pivot a response is grouped by.
type View string

const (
	ViewImage    View = "image"
	ViewCVE      View = "cve"
	ViewWorkload View = "workload"
)

// Severity is a scanner's severity, lower-cased. Anything a scanner reports
// outside the four named levels is "unknown".
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityUnknown  Severity = "unknown"
)

// Counts are findings per severity.
type Counts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
}

// WorkloadRef names a workload, and the container in it when the reference is
// about one image.
type WorkloadRef struct {
	Kind      string `json:"kind"`
	Group     string `json:"group"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Container string `json:"container,omitempty"`
}

// Finding is one vulnerability in one image.
type Finding struct {
	ID               string   `json:"id"`
	Severity         Severity `json:"severity"`
	Score            *float64 `json:"score,omitempty"`
	Package          string   `json:"package"`
	InstalledVersion string   `json:"installedVersion"`
	// FixedVersion is empty when the scanner knows no fixed release.
	FixedVersion string `json:"fixedVersion,omitempty"`
	Title        string `json:"title,omitempty"`
	Link         string `json:"link,omitempty"`
}

// Image is one scanned image (by digest when the scanner recorded one) and
// where it runs.
type Image struct {
	// ID is the digest, or the image reference when no digest was recorded.
	ID        string    `json:"id"`
	Image     string    `json:"image"`
	Digest    string    `json:"digest,omitempty"`
	OS        string    `json:"os,omitempty"`
	ScannedAt time.Time `json:"scannedAt"`
	Counts    Counts    `json:"counts"`
	// Fixable counts the findings with a fixed version, per severity.
	Fixable Counts `json:"fixable"`
	// Matching is how many findings pass the request's filters.
	Matching  int           `json:"matching"`
	Workloads []WorkloadRef `json:"workloads"`
	// Findings are the matching findings, worst first, at most
	// MaxFindingsPerImage of them.
	Findings      []Finding `json:"findings"`
	FindingsTotal int       `json:"findingsTotal"`
	// Report is the newest scan report the findings come from.
	Report resourceapi.ObjectRef `json:"report"`
}

// CVE is one vulnerability across every image that carries it.
type CVE struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Score    *float64 `json:"score,omitempty"`
	Packages []string `json:"packages"`
	// Fixable is true when some affected image has a fixed version.
	Fixable   bool   `json:"fixable"`
	Title     string `json:"title,omitempty"`
	Link      string `json:"link,omitempty"`
	Images    int    `json:"images"`
	Workloads int    `json:"workloads"`
	// Affected lists the images, worst exposure first, at most
	// MaxAffectedPerCVE of them.
	Affected []CVEImage `json:"affected"`
}

// CVEImage is one image a CVE was found in.
type CVEImage struct {
	ImageID          string        `json:"imageId"`
	Image            string        `json:"image"`
	Package          string        `json:"package"`
	InstalledVersion string        `json:"installedVersion"`
	FixedVersion     string        `json:"fixedVersion,omitempty"`
	Workloads        []WorkloadRef `json:"workloads"`
}

// Workload is one running workload and what its containers' images carry.
type Workload struct {
	Kind       string              `json:"kind"`
	Group      string              `json:"group"`
	Namespace  string              `json:"namespace"`
	Name       string              `json:"name"`
	Counts     Counts              `json:"counts"`
	Fixable    Counts              `json:"fixable"`
	Containers []WorkloadContainer `json:"containers"`
}

// WorkloadContainer is one container of a workload. Scanned is false when no
// report covers the container's image yet; its counts are then unknown, not
// zero.
type WorkloadContainer struct {
	Name      string     `json:"name"`
	Image     string     `json:"image"`
	ImageID   string     `json:"imageId,omitempty"`
	Scanned   bool       `json:"scanned"`
	ScannedAt *time.Time `json:"scannedAt,omitempty"`
	Counts    *Counts    `json:"counts,omitempty"`
	Fixable   *Counts    `json:"fixable,omitempty"`
}

// Unscanned is a running image no report covers yet.
type Unscanned struct {
	Image     string        `json:"image"`
	Workloads []WorkloadRef `json:"workloads"`
}

// Summary describes everything in scope, before the request's filters.
type Summary struct {
	ImagesScanned      int `json:"imagesScanned"`
	ImagesUnscanned    int `json:"imagesUnscanned"`
	ImagesWithCritical int `json:"imagesWithCritical"`
	// FixableCriticalHighCVEs counts distinct Critical and High CVE IDs that
	// have a fixed version in at least one affected image.
	FixableCriticalHighCVEs int `json:"fixableCriticalHighCves"`
	// WorkloadsAffected counts workloads with a Critical or High finding.
	WorkloadsAffected int `json:"workloadsAffected"`
	// Workloads counts the workloads in scope: those with a Pod that isn't
	// finished, and CronJobs that aren't suspended.
	Workloads  int        `json:"workloads"`
	Counts     Counts     `json:"counts"`
	NewestScan *time.Time `json:"newestScan,omitempty"`
	OldestScan *time.Time `json:"oldestScan,omitempty"`
}

// Coverage says what the answer leaves out. Gaps name namespaces whose
// reports the caller may not read (user_forbidden) or whose access couldn't
// be checked (user_permission_check_failed); counts cover the rest.
type Coverage struct {
	Complete bool              `json:"complete"`
	Gaps     []resourceapi.Gap `json:"gaps"`
	// InactiveReports counts reports about objects that no longer run (an
	// older ReplicaSet, a deleted Pod); they are left out.
	InactiveReports int `json:"inactiveReports"`
	// Truncated is true when some images' findings weren't read because the
	// request reached MaxFindings; their counts read as zero.
	Truncated bool `json:"truncated,omitempty"`
	// RowsOmitted counts rows past MaxRows left out of the pivot.
	RowsOmitted int `json:"rowsOmitted,omitempty"`
}

// Response is GET /api/vulnerabilities and the MCP list_vulnerabilities
// answer. Exactly one of Images, CVEs and Workloads is set, by View.
type Response struct {
	Source    string      `json:"source"`
	View      View        `json:"view"`
	Summary   Summary     `json:"summary"`
	Images    []Image     `json:"images,omitempty"`
	CVEs      []CVE       `json:"cves,omitempty"`
	Workloads []Workload  `json:"workloads,omitempty"`
	Unscanned []Unscanned `json:"unscanned"`
	Coverage  Coverage    `json:"coverage"`
}

// Filter narrows the pivot. The summary is always unfiltered.
type Filter struct {
	// Severities keeps findings at these levels; empty keeps all.
	Severities []Severity
	// FixableOnly keeps findings with a fixed version.
	FixableOnly bool
	// Query keeps rows whose image, CVE ID, package or workload contains it
	// (case-insensitive).
	Query string
	// Workload keeps one workload's images and findings.
	Workload *WorkloadRef
}

const (
	// MaxFindings bounds how many report entries one request reads.
	MaxFindings = 200_000
	// MaxFindingsPerImage bounds the findings listed per image row.
	MaxFindingsPerImage = 200
	// MaxAffectedPerCVE bounds the images listed per CVE row.
	MaxAffectedPerCVE = 50
	// MaxRows bounds the rows of one pivot.
	MaxRows = 2000
)

// SourceTrivyOperator is Source for Trivy Operator's VulnerabilityReports.
const SourceTrivyOperator = "trivy-operator"
