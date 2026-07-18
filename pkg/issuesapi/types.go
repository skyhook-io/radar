// Package issuesapi defines the stable JSON contract for Radar's /api/issues
// response. It is intentionally data-only so Radar Cloud can share the wire
// shape without importing Radar's internal issue detection implementation.
package issuesapi

import "time"

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
)

type Source string

const (
	SourceProblem    Source = "problem"
	SourceMissingRef Source = "missing_ref"
	SourceScheduling Source = "scheduling"
	SourceCondition  Source = "condition"
)

var Sources = []Source{
	SourceProblem,
	SourceMissingRef,
	SourceScheduling,
	SourceCondition,
}

type Category string

const (
	CategoryUnknown Category = "unknown"

	CategoryUnschedulable            Category = "unschedulable"
	CategoryQuotaExceeded            Category = "quota_exceeded"
	CategoryAdmissionWebhookBlocking Category = "admission_webhook_blocking"

	CategoryImagePullFailed     Category = "image_pull_failed"
	CategoryContainerWaiting    Category = "container_waiting"
	CategoryInitContainerFailed Category = "init_container_failed"

	CategoryCrashLoop         Category = "crashloop"
	CategoryOOMKilled         Category = "oom_killed"
	CategoryLivenessProbeFail Category = "liveness_probe_failed"
	CategoryReadinessFailed   Category = "readiness_failed"
	CategoryWorkloadDegraded  Category = "workload_degraded"
	CategoryHighRestart       Category = "high_restart"
	CategoryJobFailed         Category = "job_failed"
	CategoryCronJobFailed     Category = "cronjob_failed"

	CategoryMissingConfigRef         Category = "missing_config_ref"
	CategoryPDBBlocksEvictions       Category = "pdb_blocks_evictions"
	CategorySecretSyncFailed         Category = "secret_sync_failed"
	CategoryServiceNoEndpoints       Category = "service_no_endpoints"
	CategoryIngressBackendMissing    Category = "ingress_backend_missing"
	CategoryLoadBalancerPending      Category = "load_balancer_pending"
	CategoryGatewayNotReady          Category = "gateway_not_ready"
	CategoryGatewayRouteInvalid      Category = "gateway_route_invalid"
	CategoryDNSFailure               Category = "dns_failure"
	CategoryNetworkPolicyBlock       Category = "network_policy_block"
	CategoryPVCPending               Category = "pvc_pending"
	CategoryPVCLost                  Category = "pvc_lost"
	CategoryPVFailed                 Category = "pv_failed"
	CategoryPVCResizeFailed          Category = "pvc_resize_failed"
	CategoryVolumeMountFailed        Category = "volume_mount_failed"
	CategoryVolumeAccessModeConflict Category = "volume_access_mode_conflict"
	CategoryRolloutStalled           Category = "rollout_stalled"
	CategoryHPALimitedOrFailed       Category = "hpa_limited_or_failed"
	CategoryRBACForbidden            Category = "rbac_forbidden"
	CategoryCertificateNotReady      Category = "certificate_not_ready"
	CategoryPodSecurityViolation     Category = "pod_security_violation"
	CategoryTerminationStuck         Category = "termination_stuck"
	CategoryNodeNotReady             Category = "node_not_ready"
	CategoryAPIServiceUnavailable    Category = "apiservice_unavailable"
	CategoryNodeProvisioningFail     Category = "node_provisioning_failed"
	CategoryCrossplaneReconcile      Category = "crossplane_reconcile_failed"
	CategoryOperatorConditionFail    Category = "operator_condition_failed"
	CategoryGitOpsSyncFailed         Category = "gitops_sync_failed"
	// Specific GitOps failure modes — split out from the gitops_sync_failed
	// catch-all so the Issues page + MCP can distinguish "couldn't render from
	// Git" from "applied but a resource failed" from "drifted". gitops_sync_failed
	// remains the fallback for reasons none of these match.
	CategoryGitOpsRenderFailed    Category = "gitops_render_failed"    // ComparisonError / Flux build/artifact/source fetch
	CategoryGitOpsSpecInvalid     Category = "gitops_spec_invalid"     // InvalidSpecError (bad destination/source/project)
	CategoryGitOpsOperationFailed Category = "gitops_operation_failed" // sync apply failed (operationState / Flux install/upgrade)
	CategoryGitOpsOutOfSync       Category = "gitops_out_of_sync"      // live state drifted from desired
	CategoryGitOpsHealthDegraded  Category = "gitops_health_degraded"  // managed resources unhealthy/missing
	CategoryGitOpsStale           Category = "gitops_stale"            // sync/drift verdict frozen (controller down or re-compare backlog)
	CategoryHelmReleaseFailed     Category = "helm_release_failed"
	CategoryWebhookBackendDown    Category = "webhook_backend_down"
	CategoryControlPlaneNotReady  Category = "control_plane_not_ready"
	CategoryMachineNotReady       Category = "machine_not_ready"
)

type CategoryGroup string

const (
	GroupUnknown       CategoryGroup = "unknown"
	GroupScheduling    CategoryGroup = "scheduling"
	GroupStartup       CategoryGroup = "startup"
	GroupRuntime       CategoryGroup = "runtime"
	GroupConfiguration CategoryGroup = "configuration"
	GroupNetworking    CategoryGroup = "networking"
	GroupStorage       CategoryGroup = "storage"
	GroupScaling       CategoryGroup = "scaling"
	GroupSecurity      CategoryGroup = "security"
	GroupControlPlane  CategoryGroup = "control_plane"
)

var categoryGroup = map[Category]CategoryGroup{
	CategoryUnschedulable:            GroupScheduling,
	CategoryQuotaExceeded:            GroupScheduling,
	CategoryAdmissionWebhookBlocking: GroupScheduling,
	CategoryImagePullFailed:          GroupStartup,
	CategoryContainerWaiting:         GroupStartup,
	CategoryInitContainerFailed:      GroupStartup,
	CategoryCrashLoop:                GroupRuntime,
	CategoryOOMKilled:                GroupRuntime,
	CategoryLivenessProbeFail:        GroupRuntime,
	CategoryReadinessFailed:          GroupRuntime,
	CategoryWorkloadDegraded:         GroupRuntime,
	CategoryHighRestart:              GroupRuntime,
	CategoryJobFailed:                GroupRuntime,
	CategoryCronJobFailed:            GroupRuntime,
	CategoryMissingConfigRef:         GroupConfiguration,
	CategoryPDBBlocksEvictions:       GroupConfiguration,
	CategorySecretSyncFailed:         GroupConfiguration,
	CategoryServiceNoEndpoints:       GroupNetworking,
	CategoryIngressBackendMissing:    GroupNetworking,
	CategoryLoadBalancerPending:      GroupNetworking,
	CategoryGatewayNotReady:          GroupNetworking,
	CategoryGatewayRouteInvalid:      GroupNetworking,
	CategoryDNSFailure:               GroupNetworking,
	CategoryNetworkPolicyBlock:       GroupNetworking,
	CategoryPVCPending:               GroupStorage,
	CategoryPVCLost:                  GroupStorage,
	CategoryPVFailed:                 GroupStorage,
	CategoryPVCResizeFailed:          GroupStorage,
	CategoryVolumeMountFailed:        GroupStorage,
	CategoryVolumeAccessModeConflict: GroupStorage,
	CategoryRolloutStalled:           GroupScaling,
	CategoryHPALimitedOrFailed:       GroupScaling,
	CategoryRBACForbidden:            GroupSecurity,
	CategoryCertificateNotReady:      GroupSecurity,
	CategoryPodSecurityViolation:     GroupSecurity,
	CategoryTerminationStuck:         GroupControlPlane,
	CategoryNodeNotReady:             GroupControlPlane,
	CategoryAPIServiceUnavailable:    GroupControlPlane,
	CategoryNodeProvisioningFail:     GroupControlPlane,
	CategoryCrossplaneReconcile:      GroupControlPlane,
	CategoryOperatorConditionFail:    GroupControlPlane,
	CategoryGitOpsSyncFailed:         GroupControlPlane,
	CategoryGitOpsRenderFailed:       GroupControlPlane,
	CategoryGitOpsSpecInvalid:        GroupControlPlane,
	CategoryGitOpsOperationFailed:    GroupControlPlane,
	CategoryGitOpsOutOfSync:          GroupControlPlane,
	CategoryGitOpsHealthDegraded:     GroupControlPlane,
	CategoryGitOpsStale:              GroupControlPlane,
	CategoryHelmReleaseFailed:        GroupControlPlane,
	CategoryWebhookBackendDown:       GroupControlPlane,
	CategoryControlPlaneNotReady:     GroupControlPlane,
	CategoryMachineNotReady:          GroupControlPlane,
}

func GroupOf(c Category) CategoryGroup {
	if g, ok := categoryGroup[c]; ok {
		return g
	}
	return GroupUnknown
}

type Scope string

const (
	ScopeUnknown  Scope = "unknown"
	ScopeWorkload Scope = "workload"
	ScopeService  Scope = "service"
	ScopeIngress  Scope = "ingress"
	ScopePVC      Scope = "pvc"
	ScopeNode     Scope = "node"
)

type Ref struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

type Affected struct {
	Pods      int `json:"pods,omitempty"`
	Workloads int `json:"workloads,omitempty"`
	Services  int `json:"services,omitempty"`
	PVCs      int `json:"pvcs,omitempty"`
	Nodes     int `json:"nodes,omitempty"`
}

type DiagnosticRole string

const (
	DiagnosticRoleCandidate DiagnosticRole = "candidate"
	DiagnosticRoleRollup    DiagnosticRole = "rollup"
	DiagnosticRoleAffected  DiagnosticRole = "affected"
	DiagnosticRoleContext   DiagnosticRole = "context"
)

type DiagnosticContext struct {
	Role  DiagnosticRole   `json:"role,omitempty"`
	Facts []DiagnosticFact `json:"facts,omitempty"`
}

type DiagnosticFact struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
	// Confidence rates how certain a cross-subject causal link is, so the UI can
	// present a high-certainty structural edge differently from a heuristic one.
	// Empty for non-causal facts (rollup, restart evidence).
	Confidence    Confidence `json:"confidence,omitempty"`
	Refs          []Ref      `json:"refs,omitempty"`
	RelatedIssues []IssueRef `json:"related_issues,omitempty"`
}

// Confidence tiers a causal link by how deterministic the edge is:
//   - high: a declared structural edge (selector, ownerRef, claimName) — the link
//     is a fact, not an inference.
//   - medium: a direct field match whose causation needs a guard (pod.spec.nodeName
//     locates a pod on a failing node, but co-located ≠ caused-by).
//   - low: a heuristic/message-pattern match.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// IncidentParent links a SYMPTOM issue to the ROOT issue that explains it — the
// reverse of DiagnosticContext's root→symptom facts. Set only for causal links
// whose related issues are genuinely DOWNSTREAM of the root (a broken PVC, a
// pressured node, an unavailable metrics API, a not-ready Secret producer);
// never for selected_backend (where the related pods
// are the cause, not the symptom). Carries the link's Confidence so the UI can
// hedge a medium pointer ("related / verify") vs. a high one ("caused by"); it
// deliberately carries NO "hide this row" flag — demotion is presentation policy,
// not part of this contract. Assigned only when unambiguous: a single best root
// by confidence tier; distinct roots at the same tier leave it unset.
type IncidentParent struct {
	ID         string     `json:"id"`  // parent grouped-issue ID (within-cluster)
	Ref        Ref        `json:"ref"` // parent subject, for display + deep-link
	Category   Category   `json:"category,omitempty"`
	Confidence Confidence `json:"confidence,omitempty"`
	FactType   string     `json:"fact_type,omitempty"` // node_blast_radius | pvc_blast_radius | apiservice_hpa | secret_not_ready
}

type IssueRef struct {
	Ref      Ref      `json:"ref"`
	Reason   string   `json:"reason,omitempty"`
	Category Category `json:"category,omitempty"`
	Severity Severity `json:"severity,omitempty"`
	// Count is how many affected resources fold into this referenced issue from
	// the linking root's perspective — e.g. a PVC link points at one grouped
	// Deployment issue that 5 of the PVC's mounting pods fall under, so Count=5.
	// Omitted (0) when the link covers a single resource. It is the root-specific
	// subset, NOT the grouped issue's total membership, which can be larger.
	Count int `json:"count,omitempty"`
}

type ChangeContext struct {
	Changed  bool   `json:"changed"`
	What     string `json:"what,omitempty"`
	When     string `json:"when,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

type ChangeField struct {
	Path     string `json:"path"`
	OldValue any    `json:"oldValue,omitempty"`
	NewValue any    `json:"newValue,omitempty"`
}

// ChangeCategory classifies a RecentChange. The values drive both ranking and
// the per-issue correlation filter — producers and consumers must share these
// constants, not bare literals.
type ChangeCategory string

const (
	ChangeCategorySpecConfig    ChangeCategory = "spec_config"
	ChangeCategoryLifecycle     ChangeCategory = "lifecycle"
	ChangeCategoryRuntimeStatus ChangeCategory = "runtime_status"
)

type RecentChange struct {
	Source         string         `json:"source,omitempty"`
	Kind           string         `json:"kind"`
	Namespace      string         `json:"namespace,omitempty"`
	Name           string         `json:"name"`
	ChangeType     string         `json:"changeType"`
	Summary        string         `json:"summary,omitempty"`
	Timestamp      string         `json:"timestamp"`
	ChangeCategory ChangeCategory `json:"change_category,omitempty"`
	RankReason     string         `json:"rank_reason,omitempty"`
	Fields         []ChangeField  `json:"fields,omitempty"`
	// ConsumedBy lists workloads that mount or reference this ConfigMap via
	// their pod spec (volumes, envFrom, env valueFrom). Direct references
	// only — runtime consumers reading through an intermediary service are
	// not captured.
	ConsumedBy []string `json:"consumed_by,omitempty"`
}

type ClusterContext struct {
	DNS *ClusterDNSContext `json:"dns,omitempty"`
}

type ClusterDNSContext struct {
	Signals  []string            `json:"signals,omitempty"`
	Findings []ClusterDNSFinding `json:"findings,omitempty"`
	Hint     string              `json:"hint,omitempty"`
}

type ClusterDNSFinding struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Severity  string `json:"severity"`
	Reason    string `json:"reason"`
	Message   string `json:"message,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
}

type Issue struct {
	Severity      Severity      `json:"severity"`
	Source        Source        `json:"source"`
	Category      Category      `json:"category"`
	CategoryGroup CategoryGroup `json:"category_group"`
	ID            string        `json:"id"`
	GroupingScope Scope         `json:"grouping_scope"`
	Kind          string        `json:"kind"`
	Group         string        `json:"group,omitempty"`
	Namespace     string        `json:"namespace,omitempty"`
	Name          string        `json:"name"`
	Reason        string        `json:"reason"`
	Message       string        `json:"message,omitempty"`
	RawMessage    string        `json:"raw_message,omitempty"`
	// Cause / Action / Remediation* carry parsed domain diagnosis. They give the
	// Issues page + MCP a plain-English cause + next step when a detector has
	// enough evidence. All optional — empty for issues without a parser.
	// RemediationKind names a structured one-click fix (e.g. "create-namespace");
	// RemediationTarget is the resource it acts on.
	Cause             string `json:"cause,omitempty"`
	Action            string `json:"action,omitempty"`
	RemediationKind   string `json:"remediation_kind,omitempty"`
	RemediationTarget string `json:"remediation_target,omitempty"`
	// OperationRetryCount is the controller-operation retry count (e.g. Argo's
	// "(retried N times)") — distinct from RestartCount, which is pod/container
	// restarts. Stuck means the issue is not expected to self-recover (retries
	// exhausted, or a self-perpetuating drift loop).
	OperationRetryCount  int                `json:"operation_retry_count,omitempty"`
	Stuck                bool               `json:"stuck,omitempty"`
	FirstSeen            time.Time          `json:"first_seen,omitzero"`
	LastSeen             time.Time          `json:"last_seen,omitzero"`
	Count                int                `json:"count,omitempty"`
	Owner                Ref                `json:"owner,omitzero"`
	Fingerprint          string             `json:"-"`
	RestartCount         int32              `json:"restart_count,omitempty"`
	LastTerminatedReason string             `json:"last_terminated_reason,omitempty"`
	Affected             Affected           `json:"affected,omitzero"`
	Members              []Ref              `json:"members,omitempty"`
	MembersTruncated     bool               `json:"members_truncated,omitempty"`
	DiagnosticContext    *DiagnosticContext `json:"diagnostic_context,omitempty"`
	IncidentParent       *IncidentParent    `json:"incident_parent,omitempty"`
	ChangeContext        *ChangeContext     `json:"change_context,omitempty"`
	// IssueTiming is best-effort timing evidence for when this issue entered
	// the failing state, derived from K8s-native signals (condition
	// lastTransitionTime, resource phase, deletion timestamp) at detection
	// time. Absent when the signal is ambiguous — treat absence as "unknown",
	// not "started_at_resource_creation" or "started_after_resource_was_healthy".
	//
	// "started_at_resource_creation"        — evidence places the failing state
	//                                        during resource creation or first
	//                                        reconciliation.
	// "started_after_resource_was_healthy"  — evidence shows a meaningful
	//                                        healthy window before the failing
	//                                        condition appeared.
	//
	// This is timing evidence, not a root-cause verdict. A bad rollout or bad
	// config change can legitimately fail at resource creation.
	IssueTiming string `json:"issue_timing,omitempty"`
	// IssueTimingBasis documents the evidence used to derive IssueTiming so the
	// classification is auditable, not magic.
	//   "condition"       — condition.lastTransitionTime on the resource itself
	//   "owner_condition" — condition on the parent workload (e.g. Deployment.Available);
	//                       reflects workload-level health timing, not cause-specific timing
	//                       (a new image error on an already-degraded Deployment inherits
	//                       the Deployment's timing, not the image error's timing)
	//   "pod_creation"    — pod and Deployment creation timestamps compared; used for
	//                       crashloop pods on young Deployments where the Available
	//                       condition races with CrashLoopBackOff's brief ready windows
	//   "deletion"        — deletionTimestamp (always appeared after creation)
	//   "phase"           — resource Phase field (e.g. PVC Pending)
	//   "spec"            — structural spec invariant (no timestamp required)
	IssueTimingBasis string `json:"issue_timing_basis,omitempty"`
	// CorrelatedChanges lists recent non-status changes (spec/config and
	// lifecycle) on this issue's subject (and, for workload subjects, its
	// directly referenced ConfigMaps) within the correlation lookback window.
	// Deterministic evidence, not a causal claim — the consumer weighs
	// whether the change explains the issue.
	CorrelatedChanges []RecentChange `json:"correlated_changes,omitempty"`
	// NoRecentChanges states the lookback window contained no TRACKED
	// non-status changes for this issue's subject (and, for workloads, its
	// directly referenced ConfigMaps). It is scoped evidence, not proof the
	// incident is chronic — untracked dependencies (Secret values, resources
	// outside the tracked kind set, external systems) can still have changed.
	// Omitted when correlation was not attempted (cap reached, lookup failed)
	// or when the candidate fetch saturated and may have missed changes;
	// absence must never be read as "no changes".
	NoRecentChanges *NoRecentChangesMarker `json:"no_recent_changes,omitempty"`
}

// NoRecentChangesMarker carries the window so the "no changes" claim is
// scoped: a change 61 minutes ago truthfully reads as none under a 3600s
// window.
type NoRecentChangesMarker struct {
	WindowSeconds int `json:"window_seconds"`
}

type Response struct {
	Issues              []Issue         `json:"issues"`
	Total               int             `json:"total"`
	TotalMatched        int             `json:"total_matched"`
	FilterErrors        int             `json:"filter_errors,omitempty"`
	FilterErrorSample   string          `json:"filter_error_sample,omitempty"`
	Visibility          any             `json:"visibility,omitempty"`
	NarrowHint          string          `json:"narrowHint,omitempty"`
	ClusterContext      *ClusterContext `json:"cluster_context,omitempty"`
	RecentChanges       []RecentChange  `json:"recent_changes,omitempty"`
	RecentChangesReason string          `json:"recent_changes_reason,omitempty"`
	// CorrelationTruncated is set when per-issue change correlation skipped
	// some critical or warning issues (shared cap reached; criticals are
	// checked first). Under truncation, an issue without correlation markers
	// means "not checked", not "no changes".
	CorrelationTruncated bool `json:"correlation_truncated,omitempty"`
}

type BindingType string

const (
	BindingString BindingType = "string"
	BindingInt    BindingType = "int"
	BindingBool   BindingType = "bool"
)

type CELBinding struct {
	Name string
	Type BindingType
}

var CELBindings = []CELBinding{
	{Name: "severity", Type: BindingString},
	{Name: "source", Type: BindingString},
	{Name: "category", Type: BindingString},
	{Name: "category_group", Type: BindingString},
	{Name: "kind", Type: BindingString},
	{Name: "group", Type: BindingString},
	{Name: "ns", Type: BindingString},
	{Name: "name", Type: BindingString},
	{Name: "reason", Type: BindingString},
	{Name: "message", Type: BindingString},
	{Name: "cause", Type: BindingString},
	{Name: "action", Type: BindingString},
	{Name: "remediation_kind", Type: BindingString},
	{Name: "remediation_target", Type: BindingString},
	{Name: "count", Type: BindingInt},
	{Name: "first_seen", Type: BindingInt},
	{Name: "last_seen", Type: BindingInt},
	{Name: "grouping_scope", Type: BindingString},
	{Name: "restart_count", Type: BindingInt},
	{Name: "last_terminated_reason", Type: BindingString},
	// issue_timing / issue_timing_basis: filter to issues with specific timing evidence.
	// issue_timing == "started_at_resource_creation"        — failing state began during creation/first reconciliation.
	// issue_timing == "started_after_resource_was_healthy"  — a meaningful healthy window preceded the failing state.
	// issue_timing == ""                                    — no confident timing signal.
	{Name: "issue_timing", Type: BindingString},
	{Name: "issue_timing_basis", Type: BindingString},
	{Name: "operation_retry_count", Type: BindingInt},
	{Name: "stuck", Type: BindingBool},
}
