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
	ViewFix      View = "fix"
	// ViewSummary is the totals and coverage without rows.
	ViewSummary View = "summary"
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
	// PackageType is the scanner's ecosystem for the package (npm, jar,
	// debian, …), which decides how its versions are ordered.
	PackageType string `json:"packageType,omitempty"`
	// FixTarget is the one version to move to: the smallest listed fixed
	// version above the installed one, under the package's own version
	// ordering. Empty when there is no fix or Radar can't order the versions;
	// FixedVersion then stands as the scanner wrote it.
	FixTarget string `json:"fixTarget,omitempty"`
	// ecosystem is how the package's versions are ordered (ecosystemOf);
	// two packages share a name only within one ecosystem.
	ecosystem string
}

// Fix is one package upgrade and what it clears: every fixable CVE in that
// package, in every image that has it at the same versions.
type Fix struct {
	Package     string `json:"package"`
	PackageType string `json:"packageType,omitempty"`
	// To is the version that clears every listed CVE: for each CVE the
	// smallest fixed version above the installed one, read with the
	// package's own version ordering, and the highest of those. Empty when
	// some version couldn't be ordered; Listed then holds the scanner's
	// fixed versions as written.
	To     string   `json:"to,omitempty"`
	Listed []string `json:"listed,omitempty"`
	// From are the installed versions it replaces.
	From     []string `json:"from"`
	Severity Severity `json:"severity"`
	// Counts are the CVEs it clears, by severity, each counted once.
	Counts Counts `json:"counts"`
	// CVEs lists at most MaxCVEsPerFix of CVEsTotal, worst first.
	CVEs      []string `json:"cves"`
	CVEsTotal int      `json:"cvesTotal"`
	Images    int      `json:"images"`
	Workloads int      `json:"workloads"`
	// Affected lists at most MaxAffectedPerCVE images, widest first.
	Affected []FixImage `json:"affected"`
}

// FixImage is one image a fix applies to.
type FixImage struct {
	ImageID          string        `json:"imageId"`
	Image            string        `json:"image"`
	InstalledVersion string        `json:"installedVersion"`
	Workloads        []WorkloadRef `json:"workloads"`
	WorkloadsTotal   int           `json:"workloadsTotal"`
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
	Matching int `json:"matching"`
	// FindingsUnread is true when the request's findings budget ran out
	// before this image: its counts are unknown, not zero.
	FindingsUnread bool `json:"findingsUnread,omitempty"`
	// Workloads lists at most MaxWorkloadsPerRow of WorkloadsTotal.
	Workloads      []WorkloadRef `json:"workloads"`
	WorkloadsTotal int           `json:"workloadsTotal"`
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
	Fixable bool `json:"fixable"`
	// FixableImages counts the affected images where every package the CVE
	// is in has a fixed version: of Images, the ones an upgrade clears.
	FixableImages int    `json:"fixableImages"`
	Title         string `json:"title,omitempty"`
	Link          string `json:"link,omitempty"`
	Images        int    `json:"images"`
	Workloads     int    `json:"workloads"`
	// Affected lists the images, worst exposure first, at most
	// MaxAffectedPerCVE of them.
	Affected []CVEImage `json:"affected"`
}

// CVEImage is one image a CVE was found in, with each package it was found
// in there.
type CVEImage struct {
	ImageID  string            `json:"imageId"`
	Image    string            `json:"image"`
	Packages []AffectedPackage `json:"packages"`
	// Workloads lists at most MaxWorkloadsPerAffected of WorkloadsTotal.
	Workloads      []WorkloadRef `json:"workloads"`
	WorkloadsTotal int           `json:"workloadsTotal"`
}

// AffectedPackage is a package a CVE was found in, and where it is fixed.
type AffectedPackage struct {
	Package          string `json:"package"`
	InstalledVersion string `json:"installedVersion"`
	FixedVersion     string `json:"fixedVersion,omitempty"`
	FixTarget        string `json:"fixTarget,omitempty"`
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
	// FindingsUnread: scanned, but past the findings budget; counts unknown.
	FindingsUnread bool    `json:"findingsUnread,omitempty"`
	Counts         *Counts `json:"counts,omitempty"`
	Fixable        *Counts `json:"fixable,omitempty"`
}

// CleanImage is a running image whose report has no findings.
type CleanImage struct {
	ID        string    `json:"id"`
	Image     string    `json:"image"`
	ScannedAt time.Time `json:"scannedAt"`
	// Workloads lists at most MaxWorkloadsPerRow of WorkloadsTotal.
	Workloads      []WorkloadRef         `json:"workloads"`
	WorkloadsTotal int                   `json:"workloadsTotal"`
	Report         resourceapi.ObjectRef `json:"report"`
}

// Unscanned is a running image no report covers yet. Workloads lists at
// most MaxWorkloadsPerRow of WorkloadsTotal; the response lists at most
// MaxRows of Summary.ImagesUnscanned.
type Unscanned struct {
	Image          string        `json:"image"`
	Workloads      []WorkloadRef `json:"workloads"`
	WorkloadsTotal int           `json:"workloadsTotal"`
}

// Summary describes everything in scope, before the request's filters.
type Summary struct {
	ImagesScanned int `json:"imagesScanned"`
	// ImagesClean counts scanned images with no findings.
	ImagesClean        int `json:"imagesClean"`
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
// answer. Exactly one of Images, CVEs, Workloads and Fixes is set, by View;
// the summary view sets none of them, nor Unscanned.
type Response struct {
	Source    string     `json:"source"`
	View      View       `json:"view"`
	Summary   Summary    `json:"summary"`
	Images    []Image    `json:"images,omitempty"`
	CVEs      []CVE      `json:"cves,omitempty"`
	Workloads []Workload `json:"workloads,omitempty"`
	Fixes     []Fix      `json:"fixes,omitempty"`
	// Clean lists the scanned images with no findings (image view), and
	// CleanWorkloads the workloads whose every container is one (workload
	// view). They are kept out of the rows unless a search names them.
	Clean          []CleanImage  `json:"clean,omitempty"`
	CleanWorkloads []WorkloadRef `json:"cleanWorkloads,omitempty"`
	Unscanned      []Unscanned   `json:"unscanned,omitempty"`
	Coverage       Coverage      `json:"coverage"`
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
	// MaxCVEsPerFix bounds the CVE IDs listed per fix row.
	MaxCVEsPerFix = 50
	// MaxRows bounds the rows of one pivot.
	MaxRows = 2000
	// MaxWorkloadsPerRow bounds the workloads listed per image row.
	MaxWorkloadsPerRow = 100
	// MaxWorkloadsPerAffected bounds the workloads listed per image of a CVE.
	MaxWorkloadsPerAffected = 10
)

// SourceTrivyOperator is Source for Trivy Operator's VulnerabilityReports.
const SourceTrivyOperator = "trivy-operator"
