// Package resourcecontext defines the canonical data-transfer types for
// radar's normalized resource-context layer.
//
// The layer is the unified server-side projection of facts that today are
// scattered across topology relationships, the audit engine, the issues
// composer, PolicyReport informers, GitOps owner detection, and other
// per-resource lookups. Consumers — both MCP tools and REST handlers —
// receive the same shape; presentation specifics (token budgets, prose
// hints, tier-based filtering) live in the caller, not in this package.
//
// This package is types-only: it deliberately depends on nothing from
// pkg/topology or internal/* so that it can be imported by any layer
// (handlers, generators, tests) without producing import cycles.
//
// All enum string values are snake_case — both for readability in Go code
// and for stable, machine-friendly JSON output.
package resourcecontext

// ResourceContext is the top-level enrichment block attached to a resource
// response. Every field is optional; the zero value is a valid (empty)
// "basic"-tier context.
//
// Hints is an optional, presentation-only field — populated by AI-facing
// callers (MCP, /api/ai/*) and omitted by UI-facing callers. The structured
// fields above are the canonical facts; hints are a derived prose
// projection.
type ResourceContext struct {
	Tier          ContextTier    `json:"tier"`
	ManagedBy     []ContextRef   `json:"managedBy,omitempty"`
	Exposes       []ContextRef   `json:"exposes,omitempty"`
	SelectedBy    []ContextRef   `json:"selectedBy,omitempty"`
	Uses          *UsesBlock     `json:"uses,omitempty"`
	RunsOn        *ContextRef    `json:"runsOn,omitempty"`
	ScaledBy      []ContextRef   `json:"scaledBy,omitempty"`
	IssueSummary  *IssueSummary  `json:"issueSummary,omitempty"`
	AuditSummary  *AuditSummary  `json:"auditSummary,omitempty"`
	PolicySummary *PolicySummary `json:"policySummary,omitempty"`
	Hints         []string       `json:"hints,omitempty"`
	Omitted       []OmittedField `json:"omitted,omitempty"`
}

// ContextTier signals how much enrichment is included. "basic" is the
// always-on tier; "diagnostic" carries extra signals (added in a later
// phase) and is only produced when explicitly requested.
type ContextTier string

const (
	TierBasic      ContextTier = "basic"
	TierDiagnostic ContextTier = "diagnostic"
)

// ContextRef is a typed pointer to another Kubernetes object that the
// subject relates to. Group is omitted for core/v1 kinds; Namespace is
// omitted for cluster-scoped objects. The structural reason for the link
// is implied by the parent field name (selectedBy → selector match,
// runsOn → node binding, etc.) rather than re-encoded per-ref.
type ContextRef struct {
	Kind      string `json:"kind"`
	Group     string `json:"group,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// ManagedByRef is the compact form of a "managed-by" pointer used in
// SummaryContext (list/search rows). Carries Kind alongside Source so
// consumers can distinguish e.g. a Flux Kustomization from a Flux
// HelmRelease without re-parsing the Source string. Intentionally lacks
// Group to keep per-row bytes minimal.
type ManagedByRef struct {
	Kind      string `json:"kind"`             // "Application" | "Kustomization" | "HelmRelease" | "Deployment" | "DaemonSet" | "StatefulSet" | "Rollout" | …
	Source    string `json:"source"`           // "argocd" | "flux" | "helm" | "native"
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// SummaryContext is the per-row enrichment attached to list_resources
// and search hits. Always-on, intentionally minimal (≤ ~60 bytes).
type SummaryContext struct {
	ManagedBy  *ManagedByRef `json:"managedBy,omitempty"`
	Health     string        `json:"health,omitempty"`
	IssueCount int           `json:"issueCount,omitempty"`
}

// UsesBlock groups the namespaced configuration objects a workload reads
// at runtime (env, mounts, identity).
type UsesBlock struct {
	ConfigMaps     []ContextRef `json:"configMaps,omitempty"`
	Secrets        []ContextRef `json:"secrets,omitempty"`
	ServiceAccount *ContextRef  `json:"serviceAccount,omitempty"`
	PVCs           []ContextRef `json:"pvcs,omitempty"`
}

// IssueSummary is a rollup of internal issue-engine findings scoped to
// the subject resource. Pre-computed by callers and passed into the
// generator — this package does not import internal/issues.
type IssueSummary struct {
	Count           int            `json:"count"`
	HighestSeverity string         `json:"highestSeverity,omitempty"`
	TopReason       string         `json:"topReason,omitempty"`
	BySource        map[string]int `json:"bySource,omitempty"`
}

// AuditSummary is a rollup of audit-engine findings scoped to the
// subject resource.
type AuditSummary struct {
	Count           int    `json:"count"`
	HighestSeverity string `json:"highestSeverity,omitempty"`
	TopFinding      string `json:"topFinding,omitempty"`
}

// PolicySummary aggregates external policy-engine signals. Only Kyverno
// is wired in v1; the type is a struct (not a map) so additional engines
// can be added without breaking JSON consumers.
type PolicySummary struct {
	Kyverno *KyvernoSummary `json:"kyverno,omitempty"`
}

// KyvernoSummary rolls up PolicyReport results for the subject. Top
// carries up to 3 noteworthy findings.
type KyvernoSummary struct {
	Fail int              `json:"fail"`
	Warn int              `json:"warn"`
	Pass int              `json:"pass"`
	Top  []KyvernoFinding `json:"top,omitempty"`
}

// KyvernoFinding is a single PolicyReport result for the subject.
type KyvernoFinding struct {
	Policy  string `json:"policy"`
	Rule    string `json:"rule"`
	Result  string `json:"result"`
	Message string `json:"message,omitempty"`
}

// OmittedField records a field that was intentionally dropped from the
// response. See the "omitted.field path convention" in the v1 contract:
//   - top-level field: bare name (e.g. "selectedBy")
//   - nested: dotted path (e.g. "policySummary.kyverno")
//   - whole resourceContext skipped: "*"
type OmittedField struct {
	Field  string        `json:"field"`
	Reason OmittedReason `json:"reason"`
}

// OmittedReason is the closed enum of reasons a field can be omitted.
type OmittedReason string

const (
	OmittedRBACDenied     OmittedReason = "rbac_denied"
	OmittedBudgetExceeded OmittedReason = "budget_exceeded"
	OmittedCacheCold      OmittedReason = "cache_cold"
	OmittedNotInstalled   OmittedReason = "not_installed"
)
