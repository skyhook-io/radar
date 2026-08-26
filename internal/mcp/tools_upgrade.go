package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/upgrade"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

// upgradeFindingsPageSize caps tier-2 findings per call; offset + scanId page
// through the rest so the cap never strands a finding.
const upgradeFindingsPageSize = 25

type upgradeReadinessInput struct {
	Target  string `json:"target,omitempty" jsonschema:"target Kubernetes minor version, e.g. \"1.34\"; defaults to the next minor above the cluster's current version"`
	Check   string `json:"check,omitempty" jsonschema:"check id to expand into findings with evidence and remediation, e.g. \"node-drain-feasibility\"; ids come from the overview's checks list"`
	Level   string `json:"level,omitempty" jsonschema:"filter expanded findings to this level and above: blocker, warning, or review; omit to return all levels, most severe first"`
	Offset  int    `json:"offset,omitempty" jsonschema:"skip the first N matching findings when expanding a check; requires scanId from the response being continued"`
	ScanID  string `json:"scan_id,omitempty" jsonschema:"scan snapshot id from a previous response's scanId; required when offset > 0 so pages never silently mix two scans. Ignored when refresh=true (a refresh replaces the snapshot by design)"`
	Refresh bool   `json:"refresh,omitempty" jsonschema:"bypass the cached scan and run a fresh live scan; use only after changing something; rejected when offset > 0"`
}

type upgradeCheckRow struct {
	ID           string                       `json:"id"`
	Title        string                       `json:"title"`
	Category     string                       `json:"category"`
	Status       upgradereadiness.CheckStatus `json:"status"`
	Findings     int                          `json:"findings"`
	Summary      string                       `json:"summary"`
	Caveat       string                       `json:"caveat,omitempty"`
	EvidenceNote string                       `json:"evidenceNote,omitempty"`
}

type upgradeFinding struct {
	Title       string                        `json:"title"`
	Level       upgradereadiness.Level        `json:"level"`
	Resource    *upgradereadiness.ResourceRef `json:"resource,omitempty"`
	ManagedBy   *upgradereadiness.ResourceRef `json:"managedBy,omitempty"`
	Evidence    upgradereadiness.Evidence     `json:"evidence"`
	AppliesFrom string                        `json:"appliesFrom,omitempty"`
	Impact      string                        `json:"impact"`
	Remediation string                        `json:"remediation"`
	Reference   *upgradereadiness.Reference   `json:"reference,omitempty"`
}

type upgradeCheckDetail struct {
	ID                string                       `json:"id"`
	Title             string                       `json:"title"`
	Category          string                       `json:"category"`
	Status            upgradereadiness.CheckStatus `json:"status"`
	Summary           string                       `json:"summary"`
	Caveat            string                       `json:"caveat,omitempty"`
	EvidenceNote      string                       `json:"evidenceNote,omitempty"`
	Scope             string                       `json:"scope,omitempty"`
	Inspected         int                          `json:"inspected,omitempty"`
	FindingsTotal     int                          `json:"findingsTotal"`
	Offset            int                          `json:"offset,omitempty"`
	Findings          []upgradeFinding             `json:"findings"`
	FindingsTruncated int                          `json:"findingsTruncated,omitempty"`
}

type upgradeReadinessResult struct {
	CurrentVersion  string `json:"currentVersion"`
	TargetVersion   string `json:"targetVersion"`
	ReviewedThrough string `json:"reviewedThrough"`
	// ObservedAt is when the underlying scan ran; cached responses keep the
	// original stamp so a fix-then-rescan loop can tell pre-fix evidence from
	// a failed fix.
	ObservedAt time.Time `json:"observedAt"`
	ScanID     string    `json:"scanId"`
	FromCache  bool      `json:"fromCache,omitempty"`
	// Verdict is omitted entirely when coverage.state is no_access — printing
	// a verdict computed from nothing is exactly what the certainty contract
	// forbids.
	Verdict       upgradereadiness.Verdict  `json:"verdict,omitempty"`
	VerdictCaveat string                    `json:"verdictCaveat,omitempty"`
	Summary       *upgradereadiness.Summary `json:"summary,omitempty"`
	Coverage      upgradereadiness.Coverage `json:"coverage"`
	Notice        string                    `json:"notice,omitempty"`
	Checks        []upgradeCheckRow         `json:"checks,omitempty"`
	Check         *upgradeCheckDetail       `json:"check,omitempty"`
}

// mcpUpgradeAuthorizer implements upgrade.EvidenceAuthorizer over the
// MCP request context. It must reproduce the HTTP surface's authorization
// decisions, not shortcut them: every method resolves through the same
// SubjectAccessReview primitives Server.canRead / canReadSubresource /
// filterNamespacesByCanRead use, so cluster-wide pod visibility still does not
// imply cluster-scoped reads.
type mcpUpgradeAuthorizer struct {
	ctx context.Context
}

// Namespaces mirrors Server.upgradeReadinessNamespaces: the browsing namespace
// picker is ignored (an upgrade affects the cluster), the user's RBAC ceiling
// still bounds evidence, and --namespace-scope remains an explicit hard
// boundary.
func (a mcpUpgradeAuthorizer) Namespaces() []string {
	if k8s.ForceNamespaceScope {
		if namespace := k8s.GetNamespaceScopeTarget(); namespace != "" {
			return filterNamespacesForUser(a.ctx, []string{namespace})
		}
		return []string{}
	}
	return filterNamespacesForUser(a.ctx, nil)
}

func (a mcpUpgradeAuthorizer) CanList(group, resource, namespace string) bool {
	return canReadInNamespace(a.ctx, group, resource, namespace, "list")
}

// subjectCanISubresource is overridden in tests to bypass the live apiserver.
var subjectCanISubresource = pkgauth.SubjectCanISubresource

func (a mcpUpgradeAuthorizer) CanGetSubresource(group, resource, subresource string) bool {
	user := pkgauth.UserFromContext(a.ctx)
	if user == nil {
		return true
	}
	client := k8s.GetClient()
	if client == nil {
		return false
	}
	allowed, err := subjectCanISubresource(a.ctx, client, user.Username, user.Groups, "", group, resource, subresource, "get")
	if err != nil {
		log.Printf("[mcp] upgrade authorization failed for get on %s/%s: %v", resource, subresource, err)
		return false
	}
	return allowed
}

func (a mcpUpgradeAuthorizer) FilterNamespacesByCanList(group, resource string, namespaces []string) []string {
	return filterNamespacesByCanRead(a.ctx, group, resource, "list", namespaces)
}

func handleGetUpgradeReadiness(ctx context.Context, req *mcp.CallToolRequest, input upgradeReadinessInput) (*mcp.CallToolResult, any, error) {
	if err := validateUpgradeReadinessInput(input); err != nil {
		return nil, nil, err
	}
	if k8s.GetResourceCache() == nil {
		return nil, nil, errNotConnected()
	}
	outcome, err := upgrade.ScanMemoized(ctx, mcpUpgradeAuthorizer{ctx: ctx}, input.Target, input.Refresh)
	if err != nil {
		return nil, nil, mapUpgradeScanError(err)
	}
	result, err := shapeUpgradeReadiness(outcome, input)
	if err != nil {
		return nil, nil, err
	}
	return toJSONResult(result)
}

func validateUpgradeReadinessInput(input upgradeReadinessInput) error {
	if input.Offset < 0 {
		return fmt.Errorf("offset must be >= 0")
	}
	if input.Offset > 0 && input.Check == "" {
		return fmt.Errorf("offset applies only when expanding a check — pass check=<id>")
	}
	if input.Offset > 0 && input.ScanID == "" {
		return fmt.Errorf("offset > 0 requires scan_id (the scanId of the response being continued), so pages never silently mix two scans — restart from offset 0 if you no longer have it")
	}
	if input.Refresh && input.Offset > 0 {
		return fmt.Errorf("refresh cannot be combined with offset > 0 — a fresh scan invalidates the page sequence; refresh first, then page from offset 0")
	}
	if input.Level != "" && input.Check == "" {
		return fmt.Errorf("level applies only when expanding a check — pass check=<id>")
	}
	if _, err := parseUpgradeLevel(input.Level); err != nil {
		return err
	}
	return nil
}

func mapUpgradeScanError(err error) error {
	switch {
	case errors.Is(err, upgradereadiness.ErrInvalidTargetVersion), errors.Is(err, upgradereadiness.ErrNonForwardTarget):
		current := k8s.GetServerVersion()
		return fmt.Errorf("%s. The cluster is currently on %s and the check catalog is reviewed through %s — pass a forward minor version (e.g. \"1.34\") or omit target to analyze the next minor", err.Error(), current, upgradereadiness.ReviewedThrough)
	case errors.Is(err, upgradereadiness.ErrInvalidCurrentVersion):
		return fmt.Errorf("unable to determine the cluster's current Kubernetes version — the scan cannot run")
	case errors.Is(err, upgrade.ErrScanNotReady):
		return errNotConnected()
	default:
		return err
	}
}

var upgradeLevelRank = map[upgradereadiness.Level]int{
	upgradereadiness.LevelBlocker: 0,
	upgradereadiness.LevelWarning: 1,
	upgradereadiness.LevelReview:  2,
}

func parseUpgradeLevel(value string) (upgradereadiness.Level, error) {
	if value == "" {
		return "", nil
	}
	level := upgradereadiness.Level(value)
	if _, ok := upgradeLevelRank[level]; !ok {
		return "", fmt.Errorf("unknown level %q (want: blocker, warning, review)", value)
	}
	return level, nil
}

var upgradeCheckStatusRank = map[upgradereadiness.CheckStatus]int{
	upgradereadiness.CheckBlocked:       0,
	upgradereadiness.CheckWarning:       1,
	upgradereadiness.CheckReview:        2,
	upgradereadiness.CheckUnknown:       3,
	upgradereadiness.CheckPassed:        4,
	upgradereadiness.CheckNotApplicable: 5,
}

// shapeUpgradeReadiness minifies a scan into tier 1 (overview) or tier 2
// (one check expanded). It never mutates the shared ScanResults.
func shapeUpgradeReadiness(outcome upgrade.ScanOutcome, input upgradeReadinessInput) (upgradeReadinessResult, error) {
	results := outcome.Results
	out := upgradeReadinessResult{
		CurrentVersion:  results.CurrentVersion,
		TargetVersion:   results.TargetVersion,
		ReviewedThrough: results.ReviewedThrough,
		ObservedAt:      outcome.ObservedAt,
		ScanID:          outcome.ScanID,
		FromCache:       outcome.FromCache,
		Coverage:        results.Coverage,
	}
	// refresh supersedes the snapshot binding: the caller explicitly asked
	// for a new scan, so the previous scan_id being replaced is the expected
	// outcome, not the silent-page-mixing hazard the binding exists to catch.
	if input.ScanID != "" && !input.Refresh && input.ScanID != outcome.ScanID {
		return upgradeReadinessResult{}, fmt.Errorf("scan changed — the scan %s you were paging has been replaced by %s (observedAt %s); restart from offset 0", input.ScanID, outcome.ScanID, outcome.ObservedAt.UTC().Format(time.RFC3339))
	}
	if results.Coverage.State == "no_access" {
		out.Notice = "The current identity cannot read any namespaced resources in this cluster, so the scan inspected nothing and no readiness verdict is possible. This is an access limitation, not a pass. Ask a cluster administrator for read access, then re-run."
		return out, nil
	}

	out.Verdict = results.Verdict
	summary := results.Summary
	out.Summary = &summary
	if results.Summary.Unknown > 0 {
		plural := "s"
		if results.Summary.Unknown == 1 {
			plural = ""
		}
		out.VerdictCaveat = fmt.Sprintf("%d check%s had incomplete evidence and may hide blockers — report them alongside the verdict, never as passed", results.Summary.Unknown, plural)
	}

	if input.Check == "" {
		out.Checks = shapeUpgradeCheckRows(results.Checks)
		return out, nil
	}

	check := findUpgradeCheck(results.Checks, input.Check)
	if check == nil {
		ids := make([]string, 0, len(results.Checks))
		for i := range results.Checks {
			ids = append(ids, results.Checks[i].ID)
		}
		slices.Sort(ids)
		return upgradeReadinessResult{}, fmt.Errorf("unknown check %q — valid ids: %s", input.Check, strings.Join(ids, ", "))
	}
	level, _ := parseUpgradeLevel(input.Level)
	out.Check = shapeUpgradeCheckDetail(check, level, input.Offset)
	return out, nil
}

func shapeUpgradeCheckRows(checks []upgradereadiness.Check) []upgradeCheckRow {
	rows := make([]upgradeCheckRow, 0, len(checks))
	for i := range checks {
		check := &checks[i]
		rows = append(rows, upgradeCheckRow{
			ID:           check.ID,
			Title:        check.Title,
			Category:     check.Category,
			Status:       check.Status,
			Findings:     len(check.Findings),
			Summary:      check.Summary,
			Caveat:       check.Caveat,
			EvidenceNote: check.EvidenceNote,
		})
	}
	slices.SortStableFunc(rows, func(a, b upgradeCheckRow) int {
		if d := upgradeCheckStatusRank[a.Status] - upgradeCheckStatusRank[b.Status]; d != 0 {
			return d
		}
		if d := strings.Compare(a.Category, b.Category); d != 0 {
			return d
		}
		return strings.Compare(a.Title, b.Title)
	})
	return rows
}

func findUpgradeCheck(checks []upgradereadiness.Check, id string) *upgradereadiness.Check {
	for i := range checks {
		if checks[i].ID == id {
			return &checks[i]
		}
	}
	return nil
}

func shapeUpgradeCheckDetail(check *upgradereadiness.Check, level upgradereadiness.Level, offset int) *upgradeCheckDetail {
	detail := &upgradeCheckDetail{
		ID:            check.ID,
		Title:         check.Title,
		Category:      check.Category,
		Status:        check.Status,
		Summary:       check.Summary,
		Caveat:        check.Caveat,
		EvidenceNote:  check.EvidenceNote,
		Scope:         check.Scope,
		Inspected:     check.Inspected,
		FindingsTotal: len(check.Findings),
		Offset:        offset,
		Findings:      []upgradeFinding{},
	}
	matching := make([]*upgradereadiness.Finding, 0, len(check.Findings))
	for i := range check.Findings {
		finding := &check.Findings[i]
		if level != "" && upgradeLevelRank[finding.Level] > upgradeLevelRank[level] {
			continue
		}
		matching = append(matching, finding)
	}
	slices.SortStableFunc(matching, func(a, b *upgradereadiness.Finding) int {
		return upgradeLevelRank[a.Level] - upgradeLevelRank[b.Level]
	})
	if offset >= len(matching) {
		return detail
	}
	page := matching[offset:]
	if len(page) > upgradeFindingsPageSize {
		detail.FindingsTruncated = len(page) - upgradeFindingsPageSize
		page = page[:upgradeFindingsPageSize]
	}
	for _, finding := range page {
		row := upgradeFinding{
			Title:       finding.Title,
			Level:       finding.Level,
			Resource:    finding.Resource,
			ManagedBy:   finding.ManagedBy,
			Evidence:    finding.Evidence,
			AppliesFrom: finding.AppliesFrom,
			Impact:      finding.Impact,
			Remediation: finding.Remediation,
		}
		// Only the first reference survives minification — doc URLs are the
		// least token-efficient field and the remediation text stands alone.
		if len(finding.References) > 0 {
			ref := finding.References[0]
			row.Reference = &ref
		}
		detail.Findings = append(detail.Findings, row)
	}
	return detail
}
