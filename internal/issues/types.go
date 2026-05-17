// Package issues unifies cluster health signals from radar's three
// parallel sources — hardcoded problem detection, audit findings, and
// K8s Warning events — into a single normalized envelope. It also adds
// a generic CRD condition-based fallback so any CRD with a False
// Ready/Available/Reconciled/Healthy/Synced condition surfaces a
// warning without per-integration code.
//
// The Issue type is what /api/issues and the hub's fleet_issues MCP
// tool emit. Severity is normalized to a 3-tier vocabulary
// (critical/warning/info) so consumers don't need to translate
// between the parallel severity scales the underlying sources use.
package issues

import (
	"time"

	"github.com/skyhook-io/radar/internal/filter"
)

// CELFilter aliased so callers don't need a separate import to set
// Filters.Filter.
type CELFilter = filter.Filter

// Severity is the normalized 3-tier severity. Mapping rules:
//
//	critical = problem.critical | audit.danger
//	warning  = problem.high|medium | audit.warning | event.Warning | CRD-condition False
//	info     = reserved (currently unused)
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
)

// Source records which underlying detection channel emitted this
// issue. Useful for filtering ("only show me problems, not audit
// findings") and for SPA copy that explains why a row appeared.
type Source string

const (
	SourceProblem   Source = "problem"   // radar's hardcoded per-kind detection
	SourceAudit     Source = "audit"     // best-practice audit findings
	SourceEvent     Source = "event"     // K8s Warning events (recent)
	SourceCondition Source = "condition" // generic CRD .status.conditions[].status=False fallback
	SourceKyverno   Source = "kyverno"   // Kyverno PolicyReport findings (opt-in)
)

// Ref is a lightweight resource reference, used for owner pointers.
type Ref struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// Issue is the unified cluster-health record.
//
// FirstSeen / LastSeen / Count are populated for events (which arrive
// pre-aggregated from the K8s API). For problems and audit findings,
// FirstSeen and LastSeen are both the snapshot time and Count = 1.
type Issue struct {
	Severity  Severity  `json:"severity"`
	Source    Source    `json:"source"`
	Kind      string    `json:"kind"`
	Group     string    `json:"group,omitempty"`
	Namespace string    `json:"namespace,omitempty"`
	Name      string    `json:"name"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message,omitempty"`
	FirstSeen time.Time `json:"first_seen,omitzero"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
	Count     int       `json:"count,omitempty"`
	Owner     Ref       `json:"owner,omitzero"`
	// Cluster is left empty here; the hub injects it when emitting
	// cross-cluster envelopes via fleet_issues.
	Cluster string `json:"cluster,omitempty"`
}

// Filters narrows a Compose call. Empty fields are unconstrained.
type Filters struct {
	Namespaces []string
	Severities []Severity
	Sources    []Source
	Kinds      []string
	// Since restricts event-source issues to this lookback window.
	// Other sources are always current-snapshot, so this only affects
	// SourceEvent. Zero means "no time restriction" (all cached events).
	Since time.Duration
	// Limit caps the returned slice. Zero means default (200).
	Limit int
	// IncludeAudit defaults to false — audit findings are loud (50–200
	// per cluster) and the LLM use case usually wants problems first.
	// Set true to opt in.
	IncludeAudit bool
	// IncludeEvents defaults to false. Warning events are the noisiest
	// source by an order of magnitude — a single broken Pod emits a
	// FailedScheduling / BackOff / etc. Event every few seconds, and
	// the event informer retains them for the cache window (default 1h+).
	// On a multi-thousand-Pod cluster this floods the Issue list with
	// rows that mostly duplicate `problem` source (a CrashLoopBackOff
	// Pod already shows up under SourceProblem). Treat events as opt-in
	// like audit; when enabled the caller should also pass a Since
	// window (handler defaults to 1h when events are on and Since is
	// zero).
	IncludeEvents bool
	// IncludeKyverno defaults to false. Kyverno PolicyReport findings
	// are loud (a baseline cluster-pss profile alone emits 10+ findings
	// per workload) and the default Issue view should not be dominated
	// by best-practice/policy noise. Opt in via include_kyverno=true
	// or by passing "kyverno" in the source list.
	IncludeKyverno bool
	// Filter is an optional compiled CEL predicate evaluated against
	// each composed Issue's row bindings. Compile happens in the
	// handler (and is cached); this layer just runs the program.
	Filter *CELFilter
	// CanReadClusterScoped authorizes cluster-scoped Issue rows before
	// they are returned. Handlers provide a per-user SAR-backed predicate;
	// nil preserves auth-mode=none and tests where the provider's own
	// permissions are the only gate.
	CanReadClusterScoped func(kind, group string) bool
}

const (
	DefaultLimit = 200
	MaxLimit     = 1000
	// NoLimit disables the result cap. Pass as Filters.Limit when the
	// caller needs the full matched set (e.g. building a per-resource
	// issue index for summaryContext — capping there would silently zero
	// out counts for resources whose issues fall in the tail beyond
	// MaxLimit on large clusters). Stats.TotalMatched is reliable
	// regardless; this just turns off the post-sort slice.
	NoLimit = -1
)
