// Package checks is the shared, dependency-light model behind the "Checks"
// remediation queue. It groups effective findings by check into prioritized
// rows (Check) — one implementation used by both OSS Radar (single cluster)
// and Radar Hub (fleet) so the two surfaces can't drift.
//
// It is deliberately k8s-free (stdlib only): Radar Hub imports it without
// pulling k8s.io type libraries into a service that never touches Kubernetes.
// The k8s-aware audit engine (pkg/audit) layers on top — its CheckMeta is an
// alias of this package's, and it provides the raw->effective converter.
package checks

import (
	"sort"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Severity is the canonical Checks severity ladder. Distinct from the raw
// detector severity (the "warning"/"danger" Radar emits): operational
// criticality and compliance risk are different axes, so Checks gets its own
// 4-tier vocabulary.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

// Raw detector severities Radar emits (mirrors pkg/audit.Severity{Danger,Warning}).
const (
	rawDanger  = "danger"
	rawWarning = "warning"
)

// MapSeverity maps a raw detector severity to the Checks ladder: danger->high,
// warning->medium. critical/low are only reachable via an org severity override
// (Hub) — the detector never emits them directly. Unknown inputs fall to medium.
func MapSeverity(raw string) Severity {
	switch raw {
	case rawDanger:
		return SeverityHigh
	case rawWarning:
		return SeverityMedium
	default:
		return SeverityMedium
	}
}

// ValidSeverity reports whether s is one of the four ladder values.
func ValidSeverity(s string) bool {
	switch Severity(s) {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
		return true
	}
	return false
}

// SeverityRank orders the ladder for "worst first" sorting.
func SeverityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 4
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 1
	default:
		return 0
	}
}

// SourceRadarBuiltin is the only finding source in V1: Radar's built-in
// detectors. External detectors (Trivy, Polaris, …) would add more later.
const SourceRadarBuiltin = "radar_builtin"

// CheckMeta is a check's static definition (catalog entry). pkg/audit aliases
// this type so the audit engine's registry and this package share one shape.
type CheckMeta struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category,omitempty"` // Security|Reliability|Efficiency — the bucket findings emit under
	// DefaultSeverity is the Checks-ladder level (MapSeverity of the detector's
	// raw severity) a finding carries with no org override — its common/base
	// case for checks that ramp or split by context. May be empty for catalogs
	// from a Radar predating the field; consumers fall back to "Default".
	DefaultSeverity string      `json:"defaultSeverity,omitempty"`
	Description     string      `json:"description"`
	Remediation     string      `json:"remediation"`
	Frameworks      []string    `json:"frameworks,omitempty"`
	References      []Reference `json:"references,omitempty"`
	// BadgeWorthy marks a check whose finding means the specific resource is
	// actually broken (reference-integrity, lifecycle, will-break-on-upgrade) —
	// worth a per-resource badge on the topology graph and resource list.
	// Deliberately FALSE for security-posture and best-practice checks (runAsRoot,
	// missing limits, single-replica…) that fire on nearly every resource and
	// would turn the badges into noise; those stay in the Checks/Audit views.
	BadgeWorthy bool `json:"badgeWorthy,omitempty"`
}

// Reference is an authoritative link for a check (Kubernetes docs, CIS,
// NSA/CISA, …), surfaced in the expanded card so users can go deeper than the
// brief inline copy.
type Reference struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// ResourceRef is the canonical resource identity. group/namespace are emitted
// even when empty (core group / cluster-scoped) so consumers never optional-
// chain. ClusterID scopes the ref to its source cluster — the disambiguator
// when display names collapse across clusters in the fleet view ("" for OSS
// single-cluster).
type ResourceRef struct {
	ClusterID string `json:"cluster_id"`
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// EffectiveFindingState explains how policy shaped a finding. Visibility is
// always "visible" for findings present in the effective output (hidden ones
// are dropped upstream); the field exists for forward-compat with a config view
// that surfaces hidden rows. Source distinguishes detector defaults from
// config-driven outcomes (a severity override).
type EffectiveFindingState struct {
	Visibility       string `json:"visibility"`       // visible|hidden
	Source           string `json:"source"`           // detector_default|org_config
	ScoreImpact      string `json:"scoreImpact"`      // counts|excluded
	AlertImpact      string `json:"alertImpact"`      // alerts|muted
	ComplianceImpact string `json:"complianceImpact"` // counts|excluded_by_config
	Reason           string `json:"reason,omitempty"`
}

// DefaultEffectiveState is the detector-default state — no policy shaping. The
// OSS path uses this for every finding (local settings only drop findings, they
// never override severity); the Hub overrides Source/Reason when org config
// changes a finding's effective severity.
func DefaultEffectiveState() EffectiveFindingState {
	return EffectiveFindingState{
		Visibility:       "visible",
		Source:           "detector_default",
		ScoreImpact:      "counts",
		AlertImpact:      "alerts",
		ComplianceImpact: "counts",
	}
}

// EffectiveFinding is one per-resource check failure, carrying both the raw
// detector severity (OriginalSeverity) and the resolved ladder value
// (EffectiveSeverity) plus the state explaining any policy shaping.
type EffectiveFinding struct {
	Source            string                `json:"source"`
	Resource          ResourceRef           `json:"resource"`
	CheckID           string                `json:"checkID"`
	Category          string                `json:"category"`
	OriginalSeverity  string                `json:"originalSeverity"`
	EffectiveSeverity Severity              `json:"effectiveSeverity"`
	Message           string                `json:"message"`
	State             EffectiveFindingState `json:"state"`
}

// Check is one row of the remediation queue: a single failing check, rolling up
// every resource that fails it. Subject is the most-severe representative
// resource; Findings holds the per-resource detail underneath. (Distinct from
// CheckMeta, which is the check's static definition.)
type Check struct {
	ID                    string             `json:"id"`
	Source                string             `json:"source"`
	Subject               ResourceRef        `json:"subject"`
	CheckID               string             `json:"checkID"`
	Category              string             `json:"category"`
	EffectiveSeverity     Severity           `json:"effectiveSeverity"`
	Title                 string             `json:"title"`
	Message               string             `json:"message"`
	AffectedFindings      int                `json:"affectedFindings"`
	AffectedResources     int                `json:"affectedResources"`
	RepresentativeFinding EffectiveFinding   `json:"representativeFinding"`
	Findings              []EffectiveFinding `json:"findings"`
	// Environment is the source cluster's environment label (e.g. "prod"),
	// surfaced in the UI as a context tag. Empty for OSS single-cluster (no
	// fleet environment) and for unlabeled clusters.
	Environment string `json:"environment,omitempty"`
}

// Categories — mirrors pkg/audit's category vocabulary. Kept here so the
// priority weights are self-contained.
const (
	CategorySecurity    = "Security"
	CategoryReliability = "Reliability"
	CategoryEfficiency  = "Efficiency"
)

// BuildChecks groups effective findings by checkID into the remediation queue:
// one Check per failing check, aggregating every resource that fails it. The
// subject is the highest-severity representative resource. Ordering is
// deterministic and worst-first — severity, then blast radius (affected
// resources), then checkID — matching the rendered queue; each Check's member
// findings are also sorted worst-first. clusterID scopes the row ID; env (may
// be "") is surfaced as the check's Environment context tag.
func BuildChecks(findings []EffectiveFinding, catalog map[string]CheckMeta, clusterID, env string) []Check {
	type bucket struct {
		findings []EffectiveFinding
		resKeys  map[string]bool
	}
	buckets := map[string]*bucket{}
	order := []string{}
	for _, f := range findings {
		b := buckets[f.CheckID]
		if b == nil {
			b = &bucket{resKeys: map[string]bool{}}
			buckets[f.CheckID] = b
			order = append(order, f.CheckID)
		}
		b.findings = append(b.findings, f)
		b.resKeys[refKey(f.Resource)] = true
	}

	checks := make([]Check, 0, len(order))
	for _, checkID := range order {
		b := buckets[checkID]
		// Order member findings worst-first so the drawer reads top-down by
		// severity and findings[0] is the most-severe representative.
		sortEffectiveFindings(b.findings)
		rep := b.findings[0]
		worst := rep.EffectiveSeverity
		meta := catalog[checkID]
		title := meta.Title
		if title == "" {
			title = checkID
		}
		message := rep.Message
		if message == "" {
			message = meta.Description
		}

		checks = append(checks, Check{
			ID:                    clusterID + "|" + SourceRadarBuiltin + "|" + checkID,
			Source:                SourceRadarBuiltin,
			Subject:               rep.Resource,
			CheckID:               checkID,
			Category:              rep.Category,
			EffectiveSeverity:     worst,
			Title:                 title,
			Message:               message,
			AffectedFindings:      len(b.findings),
			AffectedResources:     len(b.resKeys),
			RepresentativeFinding: rep,
			Findings:              b.findings,
			Environment:           env,
		})
	}

	// Worst-first, matching the rendered queue: severity, then blast radius,
	// then checkID for stability. Consumers re-group/re-sort for display; this
	// is the canonical default order.
	sort.SliceStable(checks, func(i, j int) bool {
		ri, rj := SeverityRank(checks[i].EffectiveSeverity), SeverityRank(checks[j].EffectiveSeverity)
		if ri != rj {
			return ri > rj
		}
		if checks[i].AffectedResources != checks[j].AffectedResources {
			return checks[i].AffectedResources > checks[j].AffectedResources
		}
		return checks[i].CheckID < checks[j].CheckID
	})
	return checks
}

// refKey dedups resources within a check. ClusterID is constant per
// BuildChecks call, so it isn't part of the intra-check key.
func refKey(r ResourceRef) string {
	return resourceid.ResourceKey(r.Group, r.Kind, r.Namespace, r.Name)
}

func sortEffectiveFindings(findings []EffectiveFinding) {
	sort.SliceStable(findings, func(i, j int) bool {
		ri, rj := SeverityRank(findings[i].EffectiveSeverity), SeverityRank(findings[j].EffectiveSeverity)
		if ri != rj {
			return ri > rj
		}
		if findings[i].Resource.Kind != findings[j].Resource.Kind {
			return findings[i].Resource.Kind < findings[j].Resource.Kind
		}
		if findings[i].Resource.Namespace != findings[j].Resource.Namespace {
			return findings[i].Resource.Namespace < findings[j].Resource.Namespace
		}
		return findings[i].Resource.Name < findings[j].Resource.Name
	})
}
