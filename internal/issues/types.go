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
// tool emit. Severity is normalized to a 2-tier vocabulary
// (critical/warning) so consumers don't need to translate between the
// parallel severity scales the underlying sources use. Info-level
// detections are posture/inert noise and are dropped at compose (see
// compose.go) — the issue stream is "what's broken now", not an audit.
package issues

import "github.com/skyhook-io/radar/pkg/issuesapi"

// Severity is the normalized issue severity. The public Issues contract is
// critical|warning only:
//
//	critical = problem.critical
//	warning  = problem.<any non-critical except info> | CRD-condition False
//
// problem severities other than "critical" collapse to warning — see fromProblem
// (the mapping is non-critical by exclusion, not an explicit allow-list). The one
// exception is problem.info: inert/posture findings (deprecated-RBAC residue,
// singleton-StatefulSet headless-DNS trivia) are DROPPED at the Problem→Issue
// boundary in Compose and never become Issues — they belong to audit/posture,
// not the live "what's broken now" stream.
type Severity = issuesapi.Severity

const (
	SeverityCritical = issuesapi.SeverityCritical
	SeverityWarning  = issuesapi.SeverityWarning
)

// Source records which underlying detection channel emitted this issue.
// It is an OUTPUT label (for SPA copy that explains why a row appeared,
// and as a CEL filter binding), not an input filter — issues composes all
// four sources unconditionally; detection provenance is not a triage axis.
type Source = issuesapi.Source

const (
	SourceProblem    = issuesapi.SourceProblem
	SourceMissingRef = issuesapi.SourceMissingRef
	SourceScheduling = issuesapi.SourceScheduling
	SourceCondition  = issuesapi.SourceCondition
)

// Ref is a lightweight resource reference for the grouping subject and
// owner pointers. Group is the API group (empty for core) — carried so
// owner/affected deep-links can disambiguate CRDs from core kinds.
type Ref = issuesapi.Ref

// Issue is the unified cluster-health record.
//
// Flat (pre-group) rows are snapshot-derived. GroupIssues folds them and sets
// Count to the affected-resource fan-out EXCLUDING the subject (the subject is
// the row header, surfaced separately) — so a single-resource issue has
// Count = 0 (omitted on the wire), and a 50-pod crashloop under one Deployment
// has Count = 50. For problem / missing_ref / scheduling, LastSeen is the
// compose time and FirstSeen backs off by the observed problem duration; for
// condition rows, both timestamps are the condition's lastTransitionTime.
type Issue = issuesapi.Issue
