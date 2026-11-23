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
	// ViewFindings is an export only: one CSV row per finding, image and
	// workload container. It reads as the image view.
	ViewFindings View = "findings"
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

// Counts are findings per severity. A finding is one CVE in one package
// version of one image; Fix.Counts counts CVE IDs instead.
type Counts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
}

// WorkloadRef names a workload, and the container in it when the reference is
// about one image. In an image row's list each entry is one container, so
// its WorkloadsTotal counts containers: a workload running the image in two
// containers counts twice. A CVE's or fix's image lists each workload once,
// without a container, as the row's own Workloads count does.
type WorkloadRef struct {
	Kind      string `json:"kind"`
	Group     string `json:"group"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Container string `json:"container,omitempty"`
}

// Finding is one vulnerability in one image.
type Finding struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	// SeverityFromScore is true when the scanner gave no severity and
	// Severity is the CVSS v3 rating of Score.
	SeverityFromScore bool `json:"severityFromScore,omitempty"`
	// Score is the scanner's numeric score, as reported; absent when it gave
	// none.
	Score            *float64 `json:"score,omitempty"`
	Package          string   `json:"package"`
	InstalledVersion string   `json:"installedVersion"`
	// FixedVersion is the scanner's fixed-release text, which can list a
	// release on each of several lines ("1.2.5, 2.0.1"). Empty when the
	// scanner knows no fixed release.
	FixedVersion string `json:"fixedVersion,omitempty"`
	Title        string `json:"title,omitempty"`
	Link         string `json:"link,omitempty"`
	// PackageType is the scanner's ecosystem for the package (npm, jar,
	// debian, …), when it reports one.
	PackageType string `json:"packageType,omitempty"`
	// FixTarget is the one version to move to: the smallest listed fixed
	// version above the installed one, under the package's own version
	// ordering. Empty when there is no fix, Radar can't order the versions,
	// or no listed release is above the installed one; FixedVersion then
	// stands as the scanner wrote it.
	FixTarget string `json:"fixTarget,omitempty"`
	// PackageURL is the scanner's purl for the package version.
	PackageURL string `json:"packageUrl,omitempty"`
	// publishedAt and lastModified are the scanner database's dates for the
	// CVE (zero when it doesn't say): the CSV's, and CVE.PublishedAt's source.
	publishedAt, lastModified time.Time
	// ecosystem is how the package's versions are ordered (ecosystemOf);
	// two packages share a name only within one ecosystem.
	ecosystem string
}

// RiskFactor is a Pod setting of a workload that widens what a compromised
// container reaches, read from the specs of its Pods that run (and a
// CronJob's template) with the Best practices checks' rules
// (audit.PodRisks). A row's RiskFactors are its workloads' together.
type RiskFactor string

const (
	RiskPrivileged RiskFactor = "privileged"
	RiskHostAccess RiskFactor = "hostAccess"
	RiskRunsAsRoot RiskFactor = "runsAsRoot"
	// RiskMayRunAsRoot: no setting rules root out and none sets a UID, so
	// the image's USER decides, which Radar doesn't read. A workload that
	// runs as root doesn't also carry it.
	RiskMayRunAsRoot RiskFactor = "mayRunAsRoot"
	// RiskDangerousCapabilities: a container adds a capability the
	// dangerousCapabilities or insecureCapabilities check flags.
	RiskDangerousCapabilities RiskFactor = "dangerousCapabilities"
	// RiskPrivilegeEscalation: a container doesn't set
	// allowPrivilegeEscalation: false.
	RiskPrivilegeEscalation RiskFactor = "privilegeEscalation"
	// RiskHostPort: a container binds a port on the node.
	RiskHostPort RiskFactor = "hostPort"
)

// RiskSetting is one setting behind a risk factor: the Best practices check
// that flags it and that check's message.
type RiskSetting struct {
	Factor RiskFactor `json:"factor"`
	// Check is empty for a setting no Best practices check flags (a host
	// port).
	Check   string `json:"check,omitempty"`
	Message string `json:"message"`
}

// RiskyWorkload is a workload with a risk factor, without a container: each
// setting says which container, if it is one's.
type RiskyWorkload struct {
	WorkloadRef
	Factors  []RiskFactor  `json:"factors"`
	Settings []RiskSetting `json:"settings"`
}

// PodRisk is set on every answer; an older Radar sends none. Workloads are
// the workloads the answer's rows name with any factor, each with all its
// factors but only the settings behind those the screen marks: SeriousRisks,
// and those the risk filter selects. Rows carry every factor in
// RiskFactors, and so does the CSV. Counts are the workloads in scope with
// each factor, before the filters; a workload that runs as root isn't
// counted as may.
type PodRisk struct {
	Workloads []RiskyWorkload    `json:"workloads,omitempty"`
	Counts    map[RiskFactor]int `json:"counts"`
}

// Fix is one package upgrade and what it clears: every fixable CVE in that
// package, in every image that has it at the same versions.
type Fix struct {
	Package     string `json:"package"`
	PackageType string `json:"packageType,omitempty"`
	// To is the lowest listed release that fixes every one of its CVEs,
	// read with the package's own version ordering and on each CVE's
	// release lines. Empty when no such release can be chosen (the versions
	// can't be ordered, or no listed release is above the installed one);
	// FixedVersions then holds each distinct FixedVersion text as the
	// scanner wrote it.
	To            string   `json:"to,omitempty"`
	FixedVersions []string `json:"fixedVersions,omitempty"`
	// From are the installed versions it replaces.
	From []string `json:"from"`
	// Severity is the worst of the CVEs it clears.
	Severity Severity `json:"severity"`
	// Counts are the CVEs it clears, by severity, each counted once.
	Counts Counts `json:"counts"`
	// CVEs lists at most MaxCVEsPerFix of CVEsTotal, worst first.
	CVEs      []string `json:"cves"`
	CVEsTotal int      `json:"cvesTotal"`
	// Images and Workloads count every image and distinct workload that
	// needs it.
	Images    int `json:"images"`
	Workloads int `json:"workloads"`
	// Affected lists at most MaxAffectedPerCVE images, those an exposed
	// workload runs first, then the widest.
	Affected []FixImage `json:"affected"`
	// Exposed are the row's workloads with a route from outside the cluster,
	// at most MaxWorkloadsPerRow of ExposedTotal.
	Exposed      []ExposedWorkload `json:"exposed,omitempty"`
	ExposedTotal int               `json:"exposedTotal,omitempty"`
	// ExposureUnknown is true when none of the row's workloads has a route
	// Radar sees but one of them runs in a namespace where Radar couldn't
	// read every route kind (Exposure.Gaps): no route seen there is not
	// "not exposed".
	ExposureUnknown bool `json:"exposureUnknown,omitempty"`
	// RiskFactors are the risk factors of the row's workloads, together.
	RiskFactors []RiskFactor `json:"riskFactors,omitempty"`
}

// FixImage is one image a fix applies to.
type FixImage struct {
	ImageID string `json:"imageId"`
	Image   string `json:"image"`
	// InstalledVersions are the versions of the package in this image the
	// fix replaces, sorted.
	InstalledVersions []string      `json:"installedVersions"`
	Workloads         []WorkloadRef `json:"workloads"`
	WorkloadsTotal    int           `json:"workloadsTotal"`
}

// Image is one scanned image (by digest when the scanner recorded one) and
// where it runs.
type Image struct {
	// ID is the digest, or the image reference when no digest was recorded.
	ID     string `json:"id"`
	Image  string `json:"image"`
	Digest string `json:"digest,omitempty"`
	OS     string `json:"os,omitempty"`
	// OSEndOfLife is the scanner's word that the OS release no longer gets
	// security updates (Trivy's eosl).
	OSEndOfLife bool `json:"osEndOfLife,omitempty"`
	// Scanner wrote the image's newest report; absent when it names none.
	Scanner *Scanner `json:"scanner,omitempty"`
	// ScannedAt is when the report was last updated, or created when it
	// never was.
	ScannedAt time.Time `json:"scannedAt"`
	// Counts and Fixable describe every finding in the image, before the
	// request's filters.
	Counts Counts `json:"counts"`
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
	// Findings are the Matching findings, worst first, at most
	// MaxFindingsPerImage of them. FindingsTotal counts every finding in the
	// image, before the filters: the sum of Counts.
	Findings      []Finding `json:"findings"`
	FindingsTotal int       `json:"findingsTotal"`
	// Report is the image's newest scan report. Findings merge every current report about the image.
	Report resourceapi.ObjectRef `json:"report"`
	// Exposed are the row's workloads with a route from outside the cluster,
	// at most MaxWorkloadsPerRow of ExposedTotal.
	Exposed      []ExposedWorkload `json:"exposed,omitempty"`
	ExposedTotal int               `json:"exposedTotal,omitempty"`
	// ExposureUnknown is true when none of the row's workloads has a route
	// Radar sees but one of them runs in a namespace where Radar couldn't
	// read every route kind (Exposure.Gaps): no route seen there is not
	// "not exposed".
	ExposureUnknown bool `json:"exposureUnknown,omitempty"`
	// RiskFactors are the risk factors of the row's workloads, together.
	RiskFactors []RiskFactor `json:"riskFactors,omitempty"`
}

// CVE is one vulnerability across every image that carries it.
type CVE struct {
	ID string `json:"id"`
	// Severity and Score are the worst among the findings that pass the
	// request's filters.
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
	// PublishedAt is the earliest publication date among its findings; not
	// when it was detected.
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	// lastModified is the latest of its findings', for the CSV.
	lastModified time.Time
	// Images and Workloads count every image and distinct workload with it.
	Images    int `json:"images"`
	Workloads int `json:"workloads"`
	// Affected lists the images, those an exposed workload runs first, then
	// the widest, at most
	// MaxAffectedPerCVE of them.
	Affected []CVEImage `json:"affected"`
	// Exposed are the row's workloads with a route from outside the cluster,
	// at most MaxWorkloadsPerRow of ExposedTotal.
	Exposed      []ExposedWorkload `json:"exposed,omitempty"`
	ExposedTotal int               `json:"exposedTotal,omitempty"`
	// ExposureUnknown is true when none of the row's workloads has a route
	// Radar sees but one of them runs in a namespace where Radar couldn't
	// read every route kind (Exposure.Gaps): no route seen there is not
	// "not exposed".
	ExposureUnknown bool `json:"exposureUnknown,omitempty"`
	// RiskFactors are the risk factors of the row's workloads, together.
	RiskFactors []RiskFactor `json:"riskFactors,omitempty"`
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
	// Routes are its routes from outside the cluster, at most
	// MaxRoutesPerWorkload of RoutesTotal in a list read.
	Routes      []Route `json:"routes,omitempty"`
	RoutesTotal int     `json:"routesTotal,omitempty"`
	// ExposureUnknown is true when it has no route Radar sees and runs in a
	// namespace where Radar couldn't read every route kind (Exposure.Gaps).
	ExposureUnknown bool `json:"exposureUnknown,omitempty"`
	// RiskFactors are the risk factors of the row's workloads, together.
	RiskFactors []RiskFactor `json:"riskFactors,omitempty"`
}

// WorkloadContainer is one container of a workload and an image it runs:
// during a rollout the same container appears once per image. Scanned is
// false when no report covers the image yet; its counts are then unknown, not
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
	ID          string    `json:"id"`
	Image       string    `json:"image"`
	OSEndOfLife bool      `json:"osEndOfLife,omitempty"`
	ScannedAt   time.Time `json:"scannedAt"`
	// Workloads lists at most MaxWorkloadsPerRow of WorkloadsTotal.
	Workloads      []WorkloadRef         `json:"workloads"`
	WorkloadsTotal int                   `json:"workloadsTotal"`
	Report         resourceapi.ObjectRef `json:"report"`
	// Exposed are the row's workloads with a route from outside the cluster,
	// at most MaxWorkloadsPerRow of ExposedTotal.
	Exposed      []ExposedWorkload `json:"exposed,omitempty"`
	ExposedTotal int               `json:"exposedTotal,omitempty"`
	// ExposureUnknown is true when none of the row's workloads has a route
	// Radar sees but one of them runs in a namespace where Radar couldn't
	// read every route kind (Exposure.Gaps): no route seen there is not
	// "not exposed".
	ExposureUnknown bool `json:"exposureUnknown,omitempty"`
	// RiskFactors are the risk factors of the row's workloads, together.
	RiskFactors []RiskFactor `json:"riskFactors,omitempty"`
}

// Scanner names the tool and version that wrote a report.
type Scanner struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Unscanned is a running image no report covers yet. Workloads lists at
// most MaxWorkloadsPerRow of WorkloadsTotal; the response lists at most
// MaxRows of Summary.ImagesUnscanned.
type Unscanned struct {
	Image string `json:"image"`
	// OtherBuildScanned is true when, for one of these workloads (the
	// filtered one, under a workload filter), Trivy's report of the object
	// it scanned is of another build of this tag: Trivy Operator scans by
	// tag, so after a tag moves it scans the registry's build, not the one
	// these Pods pulled.
	OtherBuildScanned bool          `json:"otherBuildScanned,omitempty"`
	Workloads         []WorkloadRef `json:"workloads"`
	WorkloadsTotal    int           `json:"workloadsTotal"`
	// Exposed are the row's workloads with a route from outside the cluster,
	// at most MaxWorkloadsPerRow of ExposedTotal.
	Exposed      []ExposedWorkload `json:"exposed,omitempty"`
	ExposedTotal int               `json:"exposedTotal,omitempty"`
	// ExposureUnknown is true when none of the row's workloads has a route
	// Radar sees but one of them runs in a namespace where Radar couldn't
	// read every route kind (Exposure.Gaps): no route seen there is not
	// "not exposed".
	ExposureUnknown bool `json:"exposureUnknown,omitempty"`
	// RiskFactors are the risk factors of the row's workloads, together.
	RiskFactors []RiskFactor `json:"riskFactors,omitempty"`
}

// Summary describes everything in scope, before the request's filters.
type Summary struct {
	ImagesScanned int `json:"imagesScanned"`
	// ImagesClean counts scanned images with no findings.
	ImagesClean        int `json:"imagesClean"`
	ImagesUnscanned    int `json:"imagesUnscanned"`
	ImagesWithCritical int `json:"imagesWithCritical"`
	// ImagesOSEndOfLife counts scanned images whose OS the scanner reports as
	// end of life. Always sent, so its presence says the eol filter is read.
	ImagesOSEndOfLife int `json:"imagesOsEndOfLife"`
	// FixableCriticalHighCVEs counts distinct Critical and High CVE IDs that
	// have a fixed version in at least one affected image.
	FixableCriticalHighCVEs     int `json:"fixableCriticalHighCves"`
	WorkloadsWithCriticalOrHigh int `json:"workloadsWithCriticalOrHigh"`
	// Workloads counts the workloads in scope: those with a Pod that isn't
	// finished, and CronJobs that aren't suspended.
	Workloads int `json:"workloads"`
	// Counts sums each scanned image's findings.
	Counts Counts `json:"counts"`
	// NewestScan and OldestScan bound the scanned images' ScannedAt; absent
	// when no image is scanned.
	NewestScan *time.Time `json:"newestScan,omitempty"`
	OldestScan *time.Time `json:"oldestScan,omitempty"`
}

// Coverage says what the answer leaves out. Gaps use the shared read codes:
// reports the caller may not read or whose access couldn't be checked, and
// what Radar couldn't read to join them with (a gap's Kinds names it: Pods,
// CronJobs, Jobs, ReplicaSets, Namespaces). Counts cover the rest.
type Coverage struct {
	// Complete is true when there are no gaps, no image's findings went
	// unread and no row, clean or unscanned entry was left out. Images with no report yet don't make
	// it false: see Summary.ImagesUnscanned.
	Complete bool              `json:"complete"`
	Gaps     []resourceapi.Gap `json:"gaps"`
	// InactiveReports counts reports about objects that no longer run (an
	// older ReplicaSet, a deleted Pod); they are left out.
	InactiveReports int `json:"inactiveReports"`
	// Truncated is true when the request reached MaxFindings: the images
	// past it carry FindingsUnread, their counts are unknown (zero on the
	// wire), and the finding totals in Summary are lower bounds.
	Truncated bool `json:"truncated,omitempty"`
	// RowsOmitted counts the pivot's rows left out, past MaxRows or an MCP
	// request's limit. Lists inside a row are capped separately, each beside
	// its total.
	RowsOmitted int `json:"rowsOmitted,omitempty"`
	// CleanOmitted and UnscannedOmitted count what the same caps left out of
	// Clean or CleanWorkloads, and of Unscanned.
	CleanOmitted     int `json:"cleanOmitted,omitempty"`
	UnscannedOmitted int `json:"unscannedOmitted,omitempty"`
}

// Response is GET /api/vulnerabilities and the MCP list_vulnerabilities
// answer. Only the View's pivot (Images, CVEs, Workloads or Fixes) is set,
// and it is omitted when empty; the summary view sets none of them, nor
// Unscanned.
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
	// Unscanned follows a search or workload filter, but not the severity
	// and fix filters: an unscanned image's findings are unknown.
	Unscanned []Unscanned `json:"unscanned,omitempty"`
	Coverage  Coverage    `json:"coverage"`
	// Exposure says where Radar looked for routes from outside the cluster;
	// set on every answer.
	Exposure *Exposure `json:"exposure,omitempty"`
	// PodRisk says which workloads the rows name run with risky Pod
	// settings; set on every answer.
	PodRisk *PodRisk `json:"podRisk,omitempty"`
	// risk is every workload's in scope, so a caller that trims the rows
	// can name the risky ones again (KeepNamedRisks); marked are the
	// factors the risk filter selects, whose settings the answer carries.
	risk   map[WorkloadRef]*workloadRisk
	marked riskBits
	// WorkloadRuns is whether Filter.Workload names something that runs in
	// the answer's scope, so an empty answer can't pass for "no CVEs". Not
	// on the wire: the UI and agents learn it differently.
	WorkloadRuns bool `json:"-"`
	// export is set on an export read (Request.Export), for WriteCSV.
	export *exportExtras
}

// Filter narrows the pivot. The summary is always unfiltered.
type Filter struct {
	// Severities keeps findings at these levels; empty keeps all.
	Severities []Severity
	// FixableOnly keeps findings with a fixed version.
	FixableOnly bool
	// Query keeps the rows whose image or workload namespace/name contains
	// it, with all their findings, and elsewhere the findings whose CVE ID or
	// package contains it (case-insensitive).
	Query string
	// Workload keeps the images one workload runs, still listing every
	// workload that runs them. The Kind matches in any case; an empty Group
	// matches any group.
	Workload *WorkloadRef
	// ExposedOnly keeps the images a workload with a route from outside the
	// cluster runs (By workload: those workloads), still listing every
	// workload that runs them.
	ExposedOnly bool
	// OSEndOfLifeOnly keeps the scanned images whose OS the scanner reports
	// as end of life (By workload: the workloads that run one). Unscanned
	// images stay, as under a severity filter: their OS is unknown.
	OSEndOfLifeOnly bool
	// RiskFactors keeps the images a workload with any of these factors
	// runs (By workload: those workloads), still listing every workload
	// that runs them. RiskMayRunAsRoot also keeps those that run as root:
	// they may too. The risk= query (ParseRiskFactors).
	RiskFactors []RiskFactor
}

const (
	// MaxFindings bounds how many report entries one request reads.
	MaxFindings = 200_000
	// MaxFindingsPerImage bounds the findings listed per image row: the
	// worst, which is what a row is read for, and a payload the tab can poll.
	MaxFindingsPerImage = 50
	// MaxAffectedPerCVE bounds the images listed per CVE or fix row.
	MaxAffectedPerCVE = 50
	// MaxCVEsPerFix bounds the CVE IDs listed per fix row.
	MaxCVEsPerFix = 50
	// MaxRows bounds the rows of one pivot.
	MaxRows = 2000
	// MaxWorkloadsPerRow bounds the workloads listed per image row.
	MaxWorkloadsPerRow = 100
	// MaxWorkloadsPerAffected bounds the workloads listed per image of a CVE
	// or fix row.
	MaxWorkloadsPerAffected = 10
	// MaxRoutesPerWorkload bounds the routes listed per exposed workload: a
	// list read repeats them in every row the workload is in.
	MaxRoutesPerWorkload = 3
)

// SourceTrivyOperator is Source for Trivy Operator's VulnerabilityReports.
const SourceTrivyOperator = "trivy-operator"
