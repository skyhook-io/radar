// Package issues unifies cluster health signals into a single
// normalized envelope. It composes:
//   - problem    — radar's hardcoded per-kind live-state detection
//     (failing Deployments, NotReady Nodes, pending PVCs…)
//   - missing_ref — direct by-name references to objects that do not exist
//     (missing PVCs, ConfigMaps, Secrets, backend Services, roleRefs…)
//   - scheduling — why a Pod can't run: unschedulable (arch/taint/resources/
//     affinity, with the offending node label named), rejected at admission
//     (quota/LimitRange/PodSecurity/webhook — no Pod is even created), or
//     stuck post-bind (CNI IP exhaustion, volume attach/mount)
//   - condition  — generic CRD .status.conditions[].status=False fallback
//     (Argo/Flux/Knative/Crossplane/cert-manager/KEDA)
//
// All four describe LIVE OPERATIONAL STATE — "what is failing right
// now". Two adjacent signals are deliberately NOT composed here, each
// with its own home: raw K8s Warning events (get_events + the timeline)
// and policy/posture — Kyverno PolicyReports + static best-practice
// findings (runAsRoot, missing probes, no PDB, deprecated APIs, …) which
// live in pkg/audit + /api/audit + MCP get_cluster_audit. A healthy pod
// can have many audit findings, a crashing pod can have zero. Combining
// them would force consumers to disambiguate "is this critical
// operational or critical posture?" at every callsite.
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
//	critical = problem.critical
//	warning  = problem.<any non-critical> | CRD-condition False
//	info     = reserved (currently unused)
//
// problem severities other than "critical" all collapse to warning — see
// fromProblem. Today that's "high"/"medium", but the mapping is non-critical
// by exclusion, not by an explicit allow-list.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
)

// Source records which underlying detection channel emitted this issue.
// It is an OUTPUT label (for SPA copy that explains why a row appeared,
// and as a CEL filter binding), not an input filter — issues composes all
// four sources unconditionally; detection provenance is not a triage axis.
type Source string

const (
	SourceProblem    Source = "problem"     // radar's hardcoded per-kind detection
	SourceMissingRef Source = "missing_ref" // dangling-ref detection (Pod→missing PVC/CM/Secret/SA, HPA→missing target, Ingress→missing backend, etc.)
	SourceScheduling Source = "scheduling"  // placement/admission/post-bind failures (unschedulable, quota/PodSecurity/webhook, CNI/volume)
	SourceCondition  Source = "condition"   // generic CRD .status.conditions[].status=False fallback
)

// Ref is a lightweight resource reference, used for owner pointers.
type Ref struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// Issue is the unified cluster-health record.
//
// All current sources are snapshot-derived with Count = 1. For problem /
// missing_ref / scheduling, LastSeen is the compose time and FirstSeen backs
// off by the observed problem duration; for condition rows, both timestamps
// are the condition's lastTransitionTime.
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
	// RestartCount + LastTerminatedReason carry Pod crash-debugging
	// context from k8s.Problem through to issues consumers (MCP `issues`
	// tool + /api/issues + hub fleet_issues). Populated only for Pod
	// problem rows where the kubelet has recorded crash data. Together
	// they answer "is this chronic or acute?" (RestartCount) and "what
	// kind of failure?" (LastTerminatedReason: OOMKilled / Error /
	// Completed) without the agent needing a follow-up get_resource call.
	RestartCount         int32  `json:"restart_count,omitempty"`
	LastTerminatedReason string `json:"last_terminated_reason,omitempty"`
	// Cluster is left empty here; the hub injects it when emitting
	// cross-cluster envelopes via fleet_issues.
	Cluster string `json:"cluster,omitempty"`
}

// Filters narrows a Compose call. Empty fields are unconstrained.
type Filters struct {
	Namespaces []string
	Severities []Severity
	Kinds      []string
	// Limit caps the returned slice. Zero means default (200).
	Limit int
	// Filter is an optional compiled CEL predicate evaluated against
	// each composed Issue's row bindings (source is exposed there, so a
	// power user can still slice by detection method). Compile happens in
	// the handler (and is cached); this layer just runs the program.
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
