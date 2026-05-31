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
	CategoryServiceNoEndpoints       Category = "service_no_endpoints"
	CategoryIngressBackendMissing    Category = "ingress_backend_missing"
	CategoryDNSFailure               Category = "dns_failure"
	CategoryNetworkPolicyBlock       Category = "network_policy_block"
	CategoryPVCPending               Category = "pvc_pending"
	CategoryPVCLost                  Category = "pvc_lost"
	CategoryVolumeMountFailed        Category = "volume_mount_failed"
	CategoryVolumeAccessModeConflict Category = "volume_access_mode_conflict"
	CategoryRolloutStalled           Category = "rollout_stalled"
	CategoryHPALimitedOrFailed       Category = "hpa_limited_or_failed"
	CategoryRBACForbidden            Category = "rbac_forbidden"
	CategoryCertificateNotReady      Category = "certificate_not_ready"
	CategoryPodSecurityViolation     Category = "pod_security_violation"
	CategoryNodeNotReady             Category = "node_not_ready"
	CategoryOperatorConditionFail    Category = "operator_condition_failed"
	CategoryGitOpsSyncFailed         Category = "gitops_sync_failed"
	CategoryWebhookBackendDown       Category = "webhook_backend_down"
	CategoryControlPlaneNotReady     Category = "control_plane_not_ready"
	CategoryMachineNotReady          Category = "machine_not_ready"
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
	CategoryServiceNoEndpoints:       GroupNetworking,
	CategoryIngressBackendMissing:    GroupNetworking,
	CategoryDNSFailure:               GroupNetworking,
	CategoryNetworkPolicyBlock:       GroupNetworking,
	CategoryPVCPending:               GroupStorage,
	CategoryPVCLost:                  GroupStorage,
	CategoryVolumeMountFailed:        GroupStorage,
	CategoryVolumeAccessModeConflict: GroupStorage,
	CategoryRolloutStalled:           GroupScaling,
	CategoryHPALimitedOrFailed:       GroupScaling,
	CategoryRBACForbidden:            GroupSecurity,
	CategoryCertificateNotReady:      GroupSecurity,
	CategoryPodSecurityViolation:     GroupSecurity,
	CategoryNodeNotReady:             GroupControlPlane,
	CategoryOperatorConditionFail:    GroupControlPlane,
	CategoryGitOpsSyncFailed:         GroupControlPlane,
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

type Issue struct {
	Severity             Severity      `json:"severity"`
	Source               Source        `json:"source"`
	Category             Category      `json:"category,omitempty"`
	CategoryGroup        CategoryGroup `json:"category_group,omitempty"`
	ID                   string        `json:"id,omitempty"`
	GroupingScope        Scope         `json:"grouping_scope,omitempty"`
	Kind                 string        `json:"kind"`
	Group                string        `json:"group,omitempty"`
	Namespace            string        `json:"namespace,omitempty"`
	Name                 string        `json:"name"`
	Reason               string        `json:"reason"`
	Message              string        `json:"message,omitempty"`
	FirstSeen            time.Time     `json:"first_seen,omitzero"`
	LastSeen             time.Time     `json:"last_seen,omitzero"`
	Count                int           `json:"count,omitempty"`
	Owner                Ref           `json:"owner,omitzero"`
	Fingerprint          string        `json:"-"`
	RestartCount         int32         `json:"restart_count,omitempty"`
	LastTerminatedReason string        `json:"last_terminated_reason,omitempty"`
	Affected             Affected      `json:"affected,omitzero"`
	Members              []Ref         `json:"members,omitempty"`
	MembersTruncated     bool          `json:"members_truncated,omitempty"`
}

type Response struct {
	Issues            []Issue `json:"issues"`
	Total             int     `json:"total"`
	TotalMatched      int     `json:"total_matched"`
	FilterErrors      int     `json:"filter_errors,omitempty"`
	FilterErrorSample string  `json:"filter_error_sample,omitempty"`
	Visibility        any     `json:"visibility,omitempty"`
	NarrowHint        string  `json:"narrowHint,omitempty"`
}

type BindingType string

const (
	BindingString BindingType = "string"
	BindingInt    BindingType = "int"
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
	{Name: "count", Type: BindingInt},
	{Name: "first_seen", Type: BindingInt},
	{Name: "last_seen", Type: BindingInt},
	{Name: "grouping_scope", Type: BindingString},
	{Name: "restart_count", Type: BindingInt},
	{Name: "last_terminated_reason", Type: BindingString},
}
