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

import "time"

// ResourceContext is the top-level enrichment block attached to a resource
// response. Every field is optional; the zero value is a valid (empty)
// "basic"-tier context.
//
// All fields are structured. A prose `Hints []string` projection was
// considered (and prototyped) but cut from v1: our dominant agent consumer
// composes triage prose from the structured fields itself, the additional
// wire bytes earned no net signal, and once shipped, agents pattern-matching
// on hint substrings would have ossified the wording. If a real consumer
// emerges that needs deterministic prose, add it as a separate
// `explain_resource` tool rather than re-introducing it inline here.
type ResourceContext struct {
	Tier            ContextTier        `json:"tier"`
	Owner           *ContextRef        `json:"owner,omitempty"`
	ManagedBy       []ContextRef       `json:"managedBy,omitempty"`
	Exposes         []ContextRef       `json:"exposes,omitempty"`
	SelectedBy      []ContextRef       `json:"selectedBy,omitempty"`
	ReferencedBy    *ReferencedBy      `json:"referencedBy,omitempty"`
	Uses            *UsesBlock         `json:"uses,omitempty"`
	RunsOn          *ContextRef        `json:"runsOn,omitempty"`
	ScaledBy        []ContextRef       `json:"scaledBy,omitempty"`
	StatusSummary   *StatusSummary     `json:"statusSummary,omitempty"`
	Scheduling      *SchedulingSummary `json:"scheduling,omitempty"`
	PodSummary      *PodSummary        `json:"podSummary,omitempty"`
	WorkloadSummary *WorkloadSummary   `json:"workloadSummary,omitempty"`
	ServiceSummary  *ServiceSummary    `json:"serviceSummary,omitempty"`
	IngressSummary  *IngressSummary    `json:"ingressSummary,omitempty"`
	NodeSummary     *NodeSummary       `json:"nodeSummary,omitempty"`
	PVCSummary      *PVCSummary        `json:"pvcSummary,omitempty"`
	JobSummary      *JobSummary        `json:"jobSummary,omitempty"`
	CronJobSummary  *CronJobSummary    `json:"cronJobSummary,omitempty"`
	HPASummary      *HPASummary        `json:"hpaSummary,omitempty"`
	IssueSummary    *IssueSummary      `json:"issueSummary,omitempty"`
	AuditSummary    *AuditSummary      `json:"auditSummary,omitempty"`
	PolicySummary   *PolicySummary     `json:"policySummary,omitempty"`
	AppReferences   *AppReferences     `json:"appReferences,omitempty"`
	Omitted         []OmittedField     `json:"omitted,omitempty"`
}

// ContextTier signals how much enrichment is included. "basic" is the
// always-on tier; "diagnostic" carries extra signals and is only produced when
// explicitly requested.
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
// ResourceSummaryContext (list/search rows). Carries Kind alongside Source so
// consumers can distinguish e.g. a Flux Kustomization from a Flux
// HelmRelease without re-parsing the Source string. Intentionally lacks
// Group to keep per-row bytes minimal.
type ManagedByRef struct {
	Kind      string `json:"kind"`   // "Application" | "Kustomization" | "HelmRelease" | "Deployment" | "DaemonSet" | "StatefulSet" | "Rollout" | …
	Source    string `json:"source"` // "argocd" | "flux" | "helm" | "native"
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// ResourceSummaryContext is the per-row enrichment attached to
// list_resources and search hits. The row-tier companion to
// ResourceContext (the detail-tier enrichment on GET responses) —
// optimised for bulk triage on lists at ≤ ~60 bytes per row. Always-on
// when the caller didn't opt out via context=none.
type ResourceSummaryContext struct {
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

type AppReferences struct {
	ServiceEnv              []ServiceEnvReference        `json:"serviceEnv,omitempty"`
	DuplicateEnv            []DuplicateEnvVarReference   `json:"duplicateEnv,omitempty"`
	RemovedServiceEnv       []RemovedServiceEnvReference `json:"removedServiceEnv,omitempty"`
	StaleSecretEnv          []StaleSecretEnvReference    `json:"staleSecretEnv,omitempty"`
	StaleSecretEnvTruncated bool                         `json:"staleSecretEnvTruncated,omitempty"`
}

type ServiceEnvReference struct {
	Status         string     `json:"status"`
	Container      string     `json:"container"`
	Env            string     `json:"env"`
	Value          string     `json:"value"`
	Service        ContextRef `json:"service"`
	ReferencedPort int32      `json:"referencedPort,omitempty"`
	ServicePorts   []string   `json:"servicePorts,omitempty"`
	Message        string     `json:"message,omitempty"`
}

type DuplicateEnvVarReference struct {
	Container         string                      `json:"container"`
	Env               string                      `json:"env"`
	Count             int                         `json:"count"`
	Occurrences       []DuplicateEnvVarOccurrence `json:"occurrences"`
	LastDeclaredValue string                      `json:"lastDeclaredValue"`
	Message           string                      `json:"message,omitempty"`
}

type DuplicateEnvVarOccurrence struct {
	Position int    `json:"position"`
	Value    string `json:"value"`
}

type RemovedServiceEnvReference struct {
	Container      string     `json:"container"`
	Env            string     `json:"env"`
	OldValue       string     `json:"oldValue"`
	Service        ContextRef `json:"service"`
	ReferencedPort int32      `json:"referencedPort,omitempty"`
	RemovedAt      time.Time  `json:"removedAt"`
	Message        string     `json:"message,omitempty"`
}

type StaleSecretEnvReference struct {
	Pod                string     `json:"pod,omitempty"`
	AffectedPods       int        `json:"affectedPods,omitempty"`
	Container          string     `json:"container"`
	Env                string     `json:"env,omitempty"`
	ReferenceKind      string     `json:"referenceKind"`
	EvidenceSource     string     `json:"evidenceSource"`
	Prefix             string     `json:"prefix,omitempty"`
	Secret             ContextRef `json:"secret"`
	Key                string     `json:"key,omitempty"`
	ContainerStartedAt time.Time  `json:"containerStartedAt"`
	SecretChangedAt    time.Time  `json:"secretChangedAt"`
	Message            string     `json:"message,omitempty"`
}

// ReferencedBy lists workload specs that directly reference the subject
// resource. It is intentionally factual: dynamic API reads and app-specific
// naming conventions are not inferred.
type ReferencedBy struct {
	Total     int            `json:"total"`
	Items     []ReferenceUse `json:"items,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
}

type ReferenceUse struct {
	Kind      string   `json:"kind"`
	Group     string   `json:"group,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
	Name      string   `json:"name"`
	Paths     []string `json:"paths,omitempty"`
}

// StatusSummary is the generic, deterministic status projection used for
// built-ins and CRDs. It intentionally carries raw condition facts rather than
// prose conclusions.
type StatusSummary struct {
	Phase      string             `json:"phase,omitempty"`
	Conditions []ConditionSummary `json:"conditions,omitempty"`
}

// ConditionSummary preserves one Kubernetes condition as bounded factual
// evidence, including the generation for which a controller computed it.
type ConditionSummary struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

// SchedulingSummary composes the independent scheduling decisions that can
// apply to one resource. Each observation retains the source's native detail
// instead of forcing admission, group placement, and Pod placement into one
// lifecycle.
type SchedulingSummary struct {
	Observations []SchedulingObservation `json:"observations,omitempty"`
}

// SchedulingObservation is one controller's decision for one scheduling
// domain and subject. Decision describes the domain, not the subject's whole
// lifecycle; provider-specific outcome and phase must be read together with
// it. Disruptions contains only affirmative conditions in the current object
// snapshot; historical transitions belong in Timeline. Adapters emit
// observations in a stable source-defined order.
type SchedulingObservation struct {
	Source            SchedulingSource   `json:"source"`
	Domain            SchedulingDomain   `json:"domain"`
	Subject           ContextRef         `json:"subject"`
	SubjectGeneration int64              `json:"subjectGeneration,omitempty"`
	Decision          SchedulingDecision `json:"decision"`
	PrimaryCondition  *ConditionSummary  `json:"primaryCondition,omitempty"`
	Queues            []SchedulingQueue  `json:"queues,omitempty"`
	Gates             []SchedulingGate   `json:"gates,omitempty"`
	Disruptions       []ConditionSummary `json:"disruptions,omitempty"`
	Kueue             *KueueScheduling   `json:"kueue,omitempty"`
}

// SchedulingSource identifies the controller or native API that produced an
// observation.
type SchedulingSource string

const (
	// SchedulingSourceKueue identifies Kueue Workload admission evidence.
	SchedulingSourceKueue SchedulingSource = "kueue"
)

// SchedulingDomain identifies the independent scheduling question answered by
// an observation.
type SchedulingDomain string

const (
	// SchedulingDomainAdmission answers whether a workload may consume quota
	// and start.
	SchedulingDomainAdmission SchedulingDomain = "admission"
	// SchedulingDomainGroupPlacement answers whether a multi-Pod group has been
	// placed as a unit.
	SchedulingDomainGroupPlacement SchedulingDomain = "group_placement"
	// SchedulingDomainPodPlacement answers whether an individual Pod can bind
	// to a node.
	SchedulingDomainPodPlacement SchedulingDomain = "pod_placement"
)

// SchedulingDecision is the normalized answer for one scheduling domain.
// Held means the subject's own state explicitly pauses progress. Unsatisfied
// means the controller evaluated the request but its requirements are not met,
// including when a referenced queue is inactive.
type SchedulingDecision string

const (
	// SchedulingDecisionSatisfied means the domain's requirements are met.
	SchedulingDecisionSatisfied SchedulingDecision = "satisfied"
	// SchedulingDecisionUnsatisfied means evaluated requirements are not met.
	SchedulingDecisionUnsatisfied SchedulingDecision = "unsatisfied"
	// SchedulingDecisionHeld means progress is explicitly paused.
	SchedulingDecisionHeld SchedulingDecision = "held"
	// SchedulingDecisionUnknown means the available evidence cannot answer.
	SchedulingDecisionUnknown SchedulingDecision = "unknown"
)

// SchedulingQueue describes a provider-owned queue in the scheduling path.
// Name remains available when RBAC prevents Radar from exposing a navigable
// Ref. Adapters order queues from submission toward entitlement and Roles in
// the constant order below; one queue may play both roles.
type SchedulingQueue struct {
	Name  string                `json:"name"`
	Roles []SchedulingQueueRole `json:"roles"`
	Ref   *ContextRef           `json:"ref,omitempty"`
}

// SchedulingQueueRole describes how a queue participates in admission.
type SchedulingQueueRole string

const (
	// SchedulingQueueSubmission is the queue to which the subject was
	// submitted.
	SchedulingQueueSubmission SchedulingQueueRole = "submission"
	// SchedulingQueueEntitlement is the queue or cohort that evaluates quota
	// entitlement.
	SchedulingQueueEntitlement SchedulingQueueRole = "entitlement"
)

// SchedulingGate is one named prerequisite for a scheduling decision. Name is
// provider-owned evidence; Ref is optional navigation and independently RBAC-
// gated. Decision describes this gate only. Adapters emit gates in a stable,
// actionable-first order.
type SchedulingGate struct {
	Kind                SchedulingGateKind `json:"kind"`
	Name                string             `json:"name"`
	Ref                 *ContextRef        `json:"ref,omitempty"`
	NativeState         string             `json:"nativeState,omitempty"`
	Decision            SchedulingDecision `json:"decision"`
	Message             string             `json:"message,omitempty"`
	LastTransitionTime  string             `json:"lastTransitionTime,omitempty"`
	RequeueAfterSeconds *int64             `json:"requeueAfterSeconds,omitempty"`
	RetryCount          *int64             `json:"retryCount,omitempty"`
}

// SchedulingGateKind identifies the native gate family without forcing all
// controllers to expose the same state vocabulary.
type SchedulingGateKind string

const (
	// SchedulingGateAdmissionCheck is a controller-managed admission check.
	SchedulingGateAdmissionCheck SchedulingGateKind = "admission_check"
	// SchedulingGatePreemption governs whether a workload may trigger
	// preemption; a closed gate does not by itself mean admission is blocked.
	SchedulingGatePreemption SchedulingGateKind = "preemption_gate"
	// SchedulingGatePodScheduling is a Pod spec scheduling gate.
	SchedulingGatePodScheduling SchedulingGateKind = "pod_scheduling_gate"
)

// KueueScheduling preserves Kueue-specific evidence alongside the normalized
// observation. Outcome describes terminal completion and must be interpreted
// with Phase and the observation Decision.
type KueueScheduling struct {
	Phase                      KueuePhase                `json:"phase"`
	Outcome                    KueueOutcome              `json:"outcome,omitempty"`
	Active                     *bool                     `json:"active,omitempty"`
	PodsReady                  *ConditionSummary         `json:"podsReady,omitempty"`
	WaitingForReplacementPods  *ConditionSummary         `json:"waitingForReplacementPods,omitempty"`
	PodSetAssignments          []KueuePodSetAssignment   `json:"podSetAssignments,omitempty"`
	PodSetAssignmentsTruncated bool                      `json:"podSetAssignmentsTruncated,omitempty"`
	RequeueState               *KueueRequeueState        `json:"requeueState,omitempty"`
	ConcurrentAdmission        *KueueConcurrentAdmission `json:"concurrentAdmission,omitempty"`
}

// KueuePhase is Radar's compact projection of the Kueue admission lifecycle.
type KueuePhase string

const (
	// KueuePhasePending means quota has not been reserved.
	KueuePhasePending KueuePhase = "pending"
	// KueuePhaseQuotaReserved means quota is reserved but admission is not
	// complete.
	KueuePhaseQuotaReserved KueuePhase = "quota_reserved"
	// KueuePhaseAdmitted means Kueue admitted the workload.
	KueuePhaseAdmitted KueuePhase = "admitted"
	// KueuePhaseFinished means Kueue reports a terminal condition.
	KueuePhaseFinished KueuePhase = "finished"
)

// KueueOutcome is the terminal result reported by Kueue.
type KueueOutcome string

const (
	// KueueOutcomeSucceeded is a successful terminal result.
	KueueOutcomeSucceeded KueueOutcome = "succeeded"
	// KueueOutcomeFailed is a failed terminal result.
	KueueOutcomeFailed KueueOutcome = "failed"
)

// KueuePodSetAssignment is the admitted count and resource assignment for one
// Kueue PodSet. Adapters emit assignments in stable name order and report
// truncation explicitly.
type KueuePodSetAssignment struct {
	Name               string                    `json:"name"`
	Count              *int64                    `json:"count,omitempty"`
	Resources          []KueueResourceAssignment `json:"resources,omitempty"`
	ResourcesTruncated bool                      `json:"resourcesTruncated,omitempty"`
}

// KueueResourceAssignment is one resource quantity and optional flavor. Flavor
// remains visible as native evidence when FlavorRef navigation is RBAC-denied.
type KueueResourceAssignment struct {
	Name      string      `json:"name"`
	Flavor    string      `json:"flavor,omitempty"`
	FlavorRef *ContextRef `json:"flavorRef,omitempty"`
	Usage     string      `json:"usage,omitempty"`
}

// KueueRequeueState is Kueue's retry count and next eligible admission time.
type KueueRequeueState struct {
	Count     *int64 `json:"count,omitempty"`
	RequeueAt string `json:"requeueAt,omitempty"`
}

// KueueConcurrentAdmission identifies the controller parent of a Kueue
// Workload variant. ParentName remains visible when ParentRef is RBAC-denied.
type KueueConcurrentAdmission struct {
	ParentName string      `json:"parentName"`
	ParentRef  *ContextRef `json:"parentRef,omitempty"`
}

type PodSummary struct {
	Phase        string                  `json:"phase,omitempty"`
	Ready        bool                    `json:"ready"`
	RestartCount int32                   `json:"restartCount,omitempty"`
	Containers   []ContainerStateSummary `json:"containers,omitempty"`
}

type ContainerStateSummary struct {
	Name                  string `json:"name"`
	Ready                 bool   `json:"ready"`
	RestartCount          int32  `json:"restartCount,omitempty"`
	State                 string `json:"state,omitempty"`
	Reason                string `json:"reason,omitempty"`
	LastTerminationReason string `json:"lastTerminationReason,omitempty"`
}

type WorkloadSummary struct {
	Replicas    *ReplicaSummary     `json:"replicas,omitempty"`
	Conditions  []ConditionSummary  `json:"conditions,omitempty"`
	RolloutRisk *RolloutRiskSummary `json:"rolloutRisk,omitempty"`
}

type ReplicaSummary struct {
	Desired     int32 `json:"desired,omitempty"`
	Ready       int32 `json:"ready,omitempty"`
	Available   int32 `json:"available,omitempty"`
	Updated     int32 `json:"updated,omitempty"`
	Unavailable int32 `json:"unavailable,omitempty"`
}

type RolloutRiskSummary struct {
	Reason                 string `json:"reason"`
	Replicas               int32  `json:"replicas"`
	MaxSurge               string `json:"maxSurge"`
	MaxUnavailable         string `json:"maxUnavailable"`
	ResolvedMaxSurge       int32  `json:"resolvedMaxSurge"`
	ResolvedMaxUnavailable int32  `json:"resolvedMaxUnavailable"`
	Message                string `json:"message"`
	Action                 string `json:"action"`
}

// ServiceSummary adds realized backend state for a Service. The raw Service
// spec already contains type/ports/selector; this block focuses on facts that
// require looking at related resources.
type ServiceSummary struct {
	SelectedPods *PodSelectionSummary `json:"selectedPods,omitempty"`
	Warnings     []ServiceWarning     `json:"warnings,omitempty"`
}

type PodSelectionSummary struct {
	Total        int          `json:"total"`
	Ready        int          `json:"ready"`
	NotReady     int          `json:"notReady,omitempty"`
	ReadyPods    []ContextRef `json:"readyPods,omitempty"`
	NotReadyPods []ContextRef `json:"notReadyPods,omitempty"`
	Truncated    bool         `json:"truncated,omitempty"`
}

type ServiceWarning string

const (
	ServiceWarningNoSelector     ServiceWarning = "no_selector"
	ServiceWarningNoSelectedPods ServiceWarning = "no_selected_pods"
	ServiceWarningNoReadyPods    ServiceWarning = "no_ready_pods"
)

type IngressSummary struct {
	Class           string           `json:"class,omitempty"`
	Addresses       []string         `json:"addresses,omitempty"`
	BackendServices []ContextRef     `json:"backendServices,omitempty"`
	TLSSecrets      []ContextRef     `json:"tlsSecrets,omitempty"`
	Warnings        []IngressWarning `json:"warnings,omitempty"`
}

type IngressWarning string

const (
	IngressWarningNoAddress IngressWarning = "no_address"
	IngressWarningNoClass   IngressWarning = "no_class"
	IngressWarningNoRules   IngressWarning = "no_rules"
)

type NodeSummary struct {
	ReadyStatus   string            `json:"readyStatus,omitempty"`
	Unschedulable bool              `json:"unschedulable,omitempty"`
	Capacity      map[string]string `json:"capacity,omitempty"`
	Allocatable   map[string]string `json:"allocatable,omitempty"`
	Taints        []TaintSummary    `json:"taints,omitempty"`
	Warnings      []NodeWarning     `json:"warnings,omitempty"`
}

type TaintSummary struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

type NodeWarning string

const (
	NodeWarningUnschedulable      NodeWarning = "unschedulable"
	NodeWarningNotReady           NodeWarning = "not_ready"
	NodeWarningDiskPressure       NodeWarning = "disk_pressure"
	NodeWarningMemoryPressure     NodeWarning = "memory_pressure"
	NodeWarningPIDPressure        NodeWarning = "pid_pressure"
	NodeWarningNetworkUnavailable NodeWarning = "network_unavailable"
)

type PVCSummary struct {
	Phase            string       `json:"phase,omitempty"`
	StorageClassName string       `json:"storageClassName,omitempty"`
	VolumeName       string       `json:"volumeName,omitempty"`
	RequestedStorage string       `json:"requestedStorage,omitempty"`
	CapacityStorage  string       `json:"capacityStorage,omitempty"`
	AccessModes      []string     `json:"accessModes,omitempty"`
	VolumeMode       string       `json:"volumeMode,omitempty"`
	Provisioner      string       `json:"provisioner,omitempty"`
	SelectedNode     string       `json:"selectedNode,omitempty"`
	BindCompleted    string       `json:"bindCompleted,omitempty"`
	Warnings         []PVCWarning `json:"warnings,omitempty"`
}

type PVCWarning string

const (
	PVCWarningPending PVCWarning = "pending"
	PVCWarningLost    PVCWarning = "lost"
)

type JobSummary struct {
	Active                   int32                     `json:"active,omitempty"`
	Succeeded                int32                     `json:"succeeded,omitempty"`
	Failed                   int32                     `json:"failed,omitempty"`
	Completions              int32                     `json:"completions,omitempty"`
	Parallelism              int32                     `json:"parallelism,omitempty"`
	BackoffLimit             int32                     `json:"backoffLimit,omitempty"`
	Suspended                bool                      `json:"suspended,omitempty"`
	ContainerCompletionSplit *ContainerCompletionSplit `json:"containerCompletionSplit,omitempty"`
}

type CronJobSummary struct {
	Schedule                 string                    `json:"schedule,omitempty"`
	Suspended                bool                      `json:"suspended,omitempty"`
	ActiveJobs               []ContextRef              `json:"activeJobs,omitempty"`
	LastScheduleTime         string                    `json:"lastScheduleTime,omitempty"`
	LastSuccessfulTime       string                    `json:"lastSuccessfulTime,omitempty"`
	ContainerCompletionSplit *ContainerCompletionSplit `json:"containerCompletionSplit,omitempty"`
}

// ContainerCompletionSplit is a fact-only observation. The running containers
// must be inspected before treating any as a stuck sidecar.
type ContainerCompletionSplit struct {
	Pod               string   `json:"pod,omitempty"`
	Job               string   `json:"job,omitempty"`
	ExitedContainer   string   `json:"exitedContainer,omitempty"`
	RunningContainers []string `json:"runningContainers,omitempty"`
	SinceSeconds      int64    `json:"sinceSeconds,omitempty"`
	Note              string   `json:"note,omitempty"`
}

type HPASummary struct {
	State   string             `json:"state"`
	Summary string             `json:"summary"`
	Target  *ContextRef        `json:"target,omitempty"`
	Bounds  *HPAReplicaBounds  `json:"bounds,omitempty"`
	Metrics []HPAMetricSummary `json:"metrics,omitempty"`
	Reasons []HPAReasonSummary `json:"reasons,omitempty"`
}

type HPAReplicaBounds struct {
	Min                int32 `json:"min"`
	Max                int32 `json:"max"`
	Current            int32 `json:"current"`
	Desired            int32 `json:"desired"`
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	Generation         int64 `json:"generation,omitempty"`
}

type HPAReasonSummary struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

type HPAMetricSummary struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Current string `json:"current,omitempty"`
	Target  string `json:"target,omitempty"`
	Status  string `json:"status"`
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

// AuditSummary is a rollup of static posture findings scoped to the subject
// resource. HighestSeverity uses the canonical Checks severity ladder; it is
// remediation priority, not proof of an active outage.
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
