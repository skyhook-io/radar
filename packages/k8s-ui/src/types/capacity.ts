import type { Issue } from "../components/issues/types";

export const CAPACITY_SCHEMA_VERSION = "v1alpha1" as const;

export interface CapacitySubjectRef {
  group?: string;
  kind: string;
  namespace?: string;
  name: string;
}

export interface CapacityResourceIdentity {
  ref: CapacitySubjectRef;
  apiVersion?: string;
  uid?: string;
}

export type CapacityCertainty =
  "exact" | "lower_bound" | "upper_bound" | "unknown";
export type CapacityGranularity =
  "aggregate" | "aggregate_not_binpacked" | "per_node";
export type CapacityResourceVector = Record<string, string>;

export interface CapacityQuantityObservation {
  resources: CapacityResourceVector;
  certainty: CapacityCertainty;
  sources: string[];
  asOf: string;
  granularity: CapacityGranularity;
}

export interface CapacityResourceUtilization {
  resource: string;
  usage: string;
  allocatable: string;
  percent: number;
}

export interface CapacityUsageObservation {
  quantity: CapacityQuantityObservation;
  coveredNodes: number;
  totalNodes: number;
  coveredAllocatable: CapacityQuantityObservation;
  utilization: CapacityResourceUtilization[];
}

export type CapacityConfidence = "high" | "medium" | "low";
export type CapacityConditionStatus = "True" | "False" | "Unknown";

export interface CapacityCondition {
  type: string;
  status: CapacityConditionStatus;
  reason?: string;
  message?: string;
  observedGeneration?: number;
  lastTransitionTime?: string;
}

export interface CapacityBoundedResultMeta {
  total: number;
  returned: number;
  truncated: boolean;
}

export interface CapacityPageInfo {
  hasMore: boolean;
  nextCursor?: string;
}

export type CapacityIntegrationState =
  "available" | "not_detected" | "denied" | "syncing";

export interface CapacityClusterContext {
  contextName: string;
  clusterName?: string;
}

export type CapacityProviderType = "karpenter";
export type CapacityControllerMode = "self_managed" | "eks_auto" | "unknown";

export interface CapacityProviderFeatures {
  staticCapacity?: boolean;
  metrics?: boolean;
}

export interface CapacityAPIKind {
  group: string;
  kind: string;
  resource: string;
  versions: string[];
}

export interface CapacityProvider {
  type: CapacityProviderType;
  controllerMode: CapacityControllerMode;
  apiVersionsByKind: Record<string, string[]>;
  nodeClassKinds: CapacityAPIKind[];
  features: CapacityProviderFeatures;
}

export type CapacityCoverageSource =
  | "nodePools"
  | "nodeClaims"
  | "nodeClasses"
  | "nodes"
  | "pods"
  | "workloads"
  | "karpenterObjectEvents"
  | "nodeMetrics"
  | "timeline"
  | "autoscalerStatus";

export type CapacityCoverageStatus =
  "available" | "partial" | "denied" | "syncing" | "unavailable" | "error";
export type CapacityCoverageScope =
  "cluster" | "all_authorized_namespaces" | "explicit_namespaces";

export interface CapacitySourceCoverage {
  status: CapacityCoverageStatus;
  reasonCode?: string;
  reason?: string;
  scope: CapacityCoverageScope;
  namespaces?: string[];
  observedAt?: string;
  observationStart?: string;
  itemCount?: number;
  impactFields: string[];
}

export type CapacityCoverageBySource = Partial<
  Record<CapacityCoverageSource, CapacitySourceCoverage>
>;

export interface CapacityResponseMeta {
  schemaVersion: typeof CAPACITY_SCHEMA_VERSION;
  generatedAt: string;
  clusterContext: CapacityClusterContext;
  provider: CapacityProvider;
  coverage: CapacityCoverageBySource;
}

export type CapacityPoolMode = "dynamic" | "static" | "unknown";

export interface CapacityNodeClassReference {
  group: string;
  kind: string;
  name: string;
  apiVersion?: string;
}

export interface CapacityNodeClassObservation {
  reference: CapacityNodeClassReference;
  resource?: CapacityResourceIdentity;
  ready?: boolean;
  conditions: CapacityCondition[];
}

export interface CapacityRequirement {
  key: string;
  operator: string;
  values: string[];
  minValues?: number;
}

export interface CapacityTaint {
  key: string;
  value?: string;
  effect: string;
}

export interface CapacityToleration {
  key?: string;
  operator?: string;
  value?: string;
  effect?: string;
  tolerationSeconds?: number;
}

export interface CapacityPoolConfiguration {
  weight?: number;
  replicas?: number;
  labels: Record<string, string>;
  requirements: CapacityRequirement[];
  taints: CapacityTaint[];
  startupTaints: CapacityTaint[];
  expireAfter?: string;
  terminationGracePeriod?: string;
}

export interface CapacityLimitPressure {
  resource: string;
  provisioned: string;
  limit: string;
  percent?: number;
  overLimit: boolean;
}

export interface CapacityLedger {
  configuredLimit?: CapacityQuantityObservation;
  provisioned?: CapacityQuantityObservation;
  allocatable?: CapacityQuantityObservation;
  scheduledRequests?: CapacityQuantityObservation;
  inFlightCapacity?: CapacityQuantityObservation;
  actualUsage?: CapacityUsageObservation;
  aggregateUnallocatedRequests?: CapacityQuantityObservation;
  limitHeadroom?: CapacityQuantityObservation;
  limitPressure: CapacityLimitPressure[];
}

export interface CapacityDisruptionBudget {
  nodes: string;
  reasons: string[];
  schedule?: string;
  duration?: string;
}

export interface CapacityDisruptionPolicy {
  consolidationPolicy?: string;
  consolidateAfter?: string;
  budgets: CapacityDisruptionBudget[];
}

export interface CapacityClaimLifecycleSummary {
  total: number;
  pending: number;
  launched: number;
  registered: number;
  initialized: number;
  ready: number;
  failed: number;
  terminating: number;
}

export interface CapacityNodeLifecycleSummary {
  total: number;
  ready: number;
  notReady: number;
  cordoned: number;
  terminating: number;
}

export interface CapacityCompositionBucket {
  value: string;
  count: number;
  resources?: CapacityQuantityObservation;
}

export interface CapacityPoolComposition {
  capacityTypes: CapacityCompositionBucket[];
  capacityTypesMeta: CapacityBoundedResultMeta;
  instanceTypes: CapacityCompositionBucket[];
  instanceTypesMeta: CapacityBoundedResultMeta;
  zones: CapacityCompositionBucket[];
  zonesMeta: CapacityBoundedResultMeta;
  architectures: CapacityCompositionBucket[];
  architecturesMeta: CapacityBoundedResultMeta;
  images: CapacityCompositionBucket[];
  imagesMeta: CapacityBoundedResultMeta;
}

export interface CapacityWorkloadAttribution {
  owner: CapacitySubjectRef;
  podCount: number;
  requests?: CapacityQuantityObservation;
}

export interface CapacityWorkloadSummary {
  scheduledPodCount: number;
  workloadCount: number;
  topScheduled: CapacityWorkloadAttribution[];
  topScheduledMeta: CapacityBoundedResultMeta;
  pendingEligibleGroupCount: number;
}

export interface CapacityPostureFact {
  code: string;
  summary: string;
  detail?: string;
  sourcePaths: string[];
}

export interface CapacityPoolObservation {
  resource: CapacityResourceIdentity;
  generation: number;
  observedGeneration?: number;
  createdAt?: string;
  updatedAt?: string;
  mode: CapacityPoolMode;
  ready?: boolean;
  conditions: CapacityCondition[];
  nodeClass?: CapacityNodeClassObservation;
  configuration: CapacityPoolConfiguration;
  ledger: CapacityLedger;
  disruption: CapacityDisruptionPolicy;
  claims?: CapacityClaimLifecycleSummary;
  nodes?: CapacityNodeLifecycleSummary;
  composition?: CapacityPoolComposition;
  workloads?: CapacityWorkloadSummary;
  issues: Issue[];
  facts: CapacityPostureFact[];
  coverage: CapacityCoverageBySource;
}

export interface CapacityPoolSummary {
  resource: CapacityResourceIdentity;
  mode: CapacityPoolMode;
  ready?: boolean;
  nodeClass?: CapacityNodeClassReference;
  ledger: CapacityLedger;
  claims?: CapacityClaimLifecycleSummary;
  nodes?: CapacityNodeLifecycleSummary;
  issueCount: number;
  factCount: number;
}

export type CapacityMemberType = "node" | "claim" | "workload";
export type CapacityClaimStage =
  | "pending"
  | "launched"
  | "registered"
  | "initialized"
  | "ready"
  | "failed"
  | "terminating"
  | "unknown";

export interface CapacityNodeMember {
  ready?: boolean;
  cordoned: boolean;
  conditions: CapacityCondition[];
  instanceType?: string;
  capacityType?: string;
  zone?: string;
  architecture?: string;
  image?: string;
  allocatable?: CapacityQuantityObservation;
  scheduledRequests?: CapacityQuantityObservation;
  actualUsage?: CapacityUsageObservation;
  podCount?: number;
}

export interface CapacityClaimMember {
  stage: CapacityClaimStage;
  conditions: CapacityCondition[];
  nodeName?: string;
  node?: CapacityResourceIdentity;
  capacity?: CapacityQuantityObservation;
}

export interface CapacityWorkloadMember {
  owner: CapacitySubjectRef;
  podCount: number;
  requests?: CapacityQuantityObservation;
  nodes: CapacityResourceIdentity[];
  nodesMeta: CapacityBoundedResultMeta;
}

export interface CapacityPoolMember {
  type: CapacityMemberType;
  resource: CapacityResourceIdentity;
  node?: CapacityNodeMember;
  claim?: CapacityClaimMember;
  workload?: CapacityWorkloadMember;
}

export interface CapacityActionSummary {
  code: string;
  count: number;
  highestSeverity?: string;
  pools: CapacityResourceIdentity[];
  truncated: boolean;
}

export interface CapacityOverviewSummary {
  actions: CapacityActionSummary[];
  aggregateDemand?: CapacityQuantityObservation;
  scheduling?: CapacitySchedulingCapacity;
  claimStages?: CapacityClaimLifecycleSummary;
  /** Absent when NodePools were not observed — never render that as zero. */
  poolCount?: number;
  claimCount?: number;
  nodeCount?: number;
  pendingPodCount?: number;
  orphanedClaimCount?: number;
  unpooledNodeCount?: number;
  /** All observed nodes, every manager included; `scheduling` above stays Karpenter-scoped forever. */
  clusterScheduling?: CapacitySchedulingCapacity;
  /** Detected capacity managers; interpretation is coverage-gated. */
  managers: CapacityManagerSummary[];
  /** Nodes with no group-identity evidence — a presentation bucket, never "static". */
  unattributedNodeCount?: number;
}

export type CapacityManager =
  "karpenter" | "cluster_autoscaler" | "gke_autoscaler" | "aks_autoscaler";

export type CapacityManagerRollupStatus = "healthy" | "degraded" | "unknown";

export interface CapacityManagerSummary {
  manager: CapacityManager;
  groupCount: number;
  status: CapacityManagerRollupStatus;
  detail?: string;
}

export interface CapacityScalingFact {
  code: string;
  summary: string;
}

export interface CapacityAutoscalerBackoff {
  errorClass?: string;
  errorCode?: string;
  errorMessage?: string;
}

/**
 * One node group as the autoscaler itself reports it (zonal MIG / VMSS).
 * ID is the published name's basename and never changes when the child gains
 * or loses a parent group. `asOf` nil means the payload published null —
 * "not probed", never zero and never now.
 */
export interface CapacityAutoscalerChildObservation {
  id: string;
  name: string;
  minSize?: number;
  maxSize?: number;
  target?: number;
  health?: string;
  readyNodes?: number;
  totalNodes?: number;
  backoff?: CapacityAutoscalerBackoff;
  asOf?: string;
}

/**
 * A logical node group a user recognizes (Karpenter NodePool, GKE node pool,
 * EKS managed node group, AKS agent pool). Identity comes from node labels or
 * CRD evidence, never from parsing provider group names.
 */
export type CapacityGroupPlatform =
  | "karpenter" | "gke" | "eks" | "aks"
  | "kops" | "doks" | "ack" | "lke" | "scaleway";

export interface CapacityGroupSummary {
  id: string;
  name: string;
  /** The identity domain the row exists BY (node label / CRD family) — always
   *  present and distinct from manager: a static EKS managed node group has a
   *  platform and no manager. Render unknown future values via a humanized
   *  fallback, never crash. */
  platform: CapacityGroupPlatform | (string & {});
  manager?: CapacityManager;
  managerValidated: boolean;
  nodeCount: number;
  readyNodeCount: number;
  allocatable?: CapacityQuantityObservation;
  scheduledRequests?: CapacityQuantityObservation;
  scaling: CapacityScalingFact[];
  children: CapacityAutoscalerChildObservation[];
  childrenMeta: CapacityBoundedResultMeta;
}

/**
 * Cluster-level scheduling ledger across Karpenter-pooled nodes only. Each
 * value carries its own certainty; an absent value means the source was not
 * observed (unavailable ≠ zero).
 */
export interface CapacitySchedulingCapacity {
  scheduledRequests?: CapacityQuantityObservation;
  allocatable?: CapacityQuantityObservation;
  inFlightCapacity?: CapacityQuantityObservation;
  /** SUBSET of scheduledRequests from pods with spec.priority < 0 (potential preemption victims). Never additive with scheduledRequests. */
  negativePriorityRequests?: CapacityQuantityObservation;
}

export interface CapacityOverviewResponse extends CapacityResponseMeta {
  state: CapacityIntegrationState;
  summary: CapacityOverviewSummary;
  pools: CapacityPoolSummary[];
  poolsTruncated: boolean;
  /** Logical-group inventory across every manager; empty is a true zero only under observed node coverage. */
  groups: CapacityGroupSummary[];
  /** Autoscaler-known groups with no joinable nodes (scale-to-zero included) — never counted as logical groups. */
  orphanAutoscalerGroups: CapacityAutoscalerChildObservation[];
  orphanAutoscalerGroupsMeta: CapacityBoundedResultMeta;
}

export interface CapacityPoolDetailResponse extends CapacityResponseMeta {
  state: CapacityIntegrationState;
  pool?: CapacityPoolObservation;
}

export interface CapacityPoolListResponse extends CapacityResponseMeta {
  state: CapacityIntegrationState;
  items: CapacityPoolSummary[];
  page: CapacityPageInfo;
}

export interface CapacityMemberListResponse extends CapacityResponseMeta {
  state: CapacityIntegrationState;
  pool: CapacityResourceIdentity;
  type: CapacityMemberType;
  items: CapacityPoolMember[];
  page: CapacityPageInfo;
}

export type CapacityDemandState =
  | "waiting_for_scheduler"
  | "held"
  | "awaiting_capacity"
  | "blocked"
  | "unknown";

export interface CapacityDemandCounts {
  podCount: number;
  groupCount: number;
}

export interface CapacityDemandSummary {
  total: CapacityDemandCounts;
  byState: Record<CapacityDemandState, CapacityDemandCounts>;
}

export interface CapacitySchedulingConstraint {
  predicate: string;
  key?: string;
  operator?: string;
  values: string[];
  sourcePath: string;
}

export interface CapacitySchedulingSignature {
  fingerprint: string;
  constraints: CapacitySchedulingConstraint[];
  constraintsMeta: CapacityBoundedResultMeta;
  tolerations: CapacityToleration[];
  tolerationsMeta: CapacityBoundedResultMeta;
}

export interface CapacitySchedulerReason {
  code: string;
  source: string;
  message?: string;
  count: number;
  firstSeen: string;
  lastSeen: string;
}

export type CapacityPoolEvaluationResult =
  "declared_compatible" | "incompatible" | "unknown";

export interface CapacityEvaluationEvidence {
  predicate: string;
  sourcePath: string;
  observedValues: string[];
  expectedValues: string[];
  confidence: CapacityConfidence;
  explanation: string;
}

export interface CapacityPoolEvaluation {
  pool: CapacitySubjectRef;
  result: CapacityPoolEvaluationResult;
  evidence: CapacityEvaluationEvidence[];
  evidenceMeta: CapacityBoundedResultMeta;
  unknownPredicates: string[];
  unknownPredicatesMeta: CapacityBoundedResultMeta;
}

export interface CapacityPoolEvaluationCounts {
  declaredCompatible: number;
  incompatible: number;
  unknown: number;
}

export interface CapacityDemandGroup {
  id: string;
  fingerprint: string;
  owner?: CapacitySubjectRef;
  pods: CapacityResourceIdentity[];
  podsMeta: CapacityBoundedResultMeta;
  namespace: string;
  firstSeen: string;
  lastSeen: string;
  podCount: number;
  /** Pods holding a scheduler node nomination (preemption in progress; best-effort — never changes state or eligibility). */
  nominatedPodCount?: number;
  perPodRequests: CapacityQuantityObservation;
  aggregateRequests: CapacityQuantityObservation;
  schedulingSignature: CapacitySchedulingSignature;
  state: CapacityDemandState;
  schedulerReasons: CapacitySchedulerReason[];
  schedulerReasonsMeta: CapacityBoundedResultMeta;
  poolEvaluations: CapacityPoolEvaluation[];
  poolEvaluationsMeta: CapacityBoundedResultMeta;
  poolEvaluationCounts: CapacityPoolEvaluationCounts;
  issues: Issue[];
}

export interface CapacityDemandResponse extends CapacityResponseMeta {
  state: CapacityIntegrationState;
  summary?: CapacityDemandSummary;
  items: CapacityDemandGroup[];
  page: CapacityPageInfo;
}

export type CapacityActivityType =
  | "provision"
  | "launch_failure"
  | "registration_failure"
  | "initialization_failure"
  | "disruption"
  | "interruption"
  | "termination"
  | "config_change";
/** `ended` terminalizes an episode without asserting a cause — the subject went
 *  away before reaching its goal and no failure evidence was recorded. It is
 *  deliberately neither `completed` nor `failed`. */
export type CapacityActivityState =
  | "open"
  | "completed"
  | "ended"
  | "failed"
  | "observed"
  | "blocked"
  | "unknown";
export type CapacityEvidenceSource =
  "condition" | "k8s_event" | "resource_change";
export type CapacityEvidenceRelationship =
  "direct" | "structural" | "correlated";

export interface CapacityActivityEvidence {
  at: string;
  source: CapacityEvidenceSource;
  reasonCode: string;
  rawReason?: string;
  rawMessage?: string;
  relationship: CapacityEvidenceRelationship;
  confidence: CapacityConfidence;
  refs: CapacityResourceIdentity[];
}

export interface CapacityActivityEpisode {
  id: string;
  type: CapacityActivityType;
  state: CapacityActivityState;
  summary: string;
  primaryReasonCode?: string;
  startedAt: string;
  endedAt?: string;
  durationSeconds?: number;
  pool?: CapacityResourceIdentity;
  claim?: CapacityResourceIdentity;
  node?: CapacityResourceIdentity;
  evidence: CapacityActivityEvidence[];
  evidenceMeta: CapacityBoundedResultMeta;
}

export type CapacityCursorStatus = "valid" | "epoch_changed" | "evicted";

export interface CapacityObservationGap {
  detectedAt: string;
  reason: string;
  previousCursor?: string;
  newEpoch?: string;
}

export interface CapacityRetention {
  mode: string;
  maxEvents?: number;
  maxAgeSeconds?: number;
}

export interface CapacityObservationWindow {
  startedAt: string;
  endedAt: string;
  sources: CapacityEvidenceSource[];
  retention: CapacityRetention;
  gaps: CapacityObservationGap[];
}

export interface CapacityActivityTypeCounts {
  total: number;
  byState: Partial<Record<CapacityActivityState, number>>;
}

export interface CapacityActivityAggregate {
  total: number;
  byType: Partial<Record<CapacityActivityType, CapacityActivityTypeCounts>>;
}

export interface CapacityActivityResponse extends CapacityResponseMeta {
  state: CapacityIntegrationState;
  items: CapacityActivityEpisode[];
  page: CapacityPageInfo;
  cursorStatus: CapacityCursorStatus;
  cursorGap?: CapacityObservationGap;
  observation: CapacityObservationWindow;
  /** Whole-window rollup; present only on first-page responses and never narrowed by the type filter. */
  aggregate?: CapacityActivityAggregate;
}
