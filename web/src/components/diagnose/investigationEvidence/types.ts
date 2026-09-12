import { type Issue, type IssueRecentChange } from "@skyhook-io/k8s-ui";
import { type TimeSeries } from "@skyhook-io/k8s-ui/components/charts";
import {
  type DiagnosisChangeContext,
  type DiagnosisCrashCause,
  type DiagnosisDNSContext,
  type DiagnosisEvidenceLimitationBase,
  type DiagnosisEvidenceTone,
  type DiagnosisFilteredLogs,
  type DiagnosisResourceContext,
  type DiagnosisResourceRef,
  type DiagnosisStartupBlocker,
} from "../diagnoseEvidenceTypes";
/**
 * The projection deliberately consumes only the small, structural portion of
 * Turn that it needs. TimelineItem is private to parts.tsx today; Turn[] is
 * structurally assignable to this type without coupling evidence extraction to
 * the transcript renderer.
 */
export interface InvestigationEvidenceTurn {
  timeline: readonly InvestigationEvidenceTimelineItem[];
  question?: string;
  apply?: boolean;
  verify?: boolean;
  status?: "running" | "done" | "error";
}
/** The resource the investigation was opened for. */
export interface InvestigationEvidenceTarget {
  kind: string;
  /** Kubernetes API group; empty means core. */
  group: string;
  namespace?: string;
  name: string;
}
export type InvestigationEvidencePhase =
  "initial" | "followup" | "verification" | "apply";
export type InvestigationEvidenceTimelineItem =
  | { kind: "thinking"; text: string }
  | {
      kind: "tool";
      id: string;
      tool: string;
      status: string;
      summary?: string;
      result?: string;
      evidenceRef?: string;
      radarEvidence?: boolean;
      truncated?: boolean;
      isError?: boolean;
    };
export type InvestigationEvidenceTier =
  "key" | "supporting" | "context" | "checked";
export type InvestigationEvidenceRelevance =
  "target" | "producer-related" | "broader";
export type InvestigationEvidenceKind =
  | "issue"
  | "startup"
  | "crash"
  | "resource"
  | "logs"
  | "events"
  | "changes"
  | "dns"
  | "network"
  | "relationships"
  | "topology"
  | "inventory"
  | "receipt"
  | "alerts"
  | "helm"
  | "permissions"
  | "metrics";
export type InvestigationSemanticDomain = "issue" | "startup" | "crash" | "dns";
export interface InvestigationEvidenceSource {
  /** DOM-safe stable identity derived from turn index + the agent step ID. */
  id: string;
  turnIndex: number;
  timelineIndex: number;
  stepId: string;
  tool: string;
  /** The exact agent-emitted tool input shown in Activity. */
  args?: string;
  /** Stable flattened transcript order. */
  order: number;
  /** Which chronological phase of the run produced this source. */
  phase: InvestigationEvidencePhase;
  /** True only when the agent transport explicitly marked the tool result successful. */
  confirmedSuccess: boolean;
  /** Server-issued, turn-scoped identity for this exact retained result. */
  evidenceRef?: string;
  /**
   * The highest-priority evidence group produced by this call. Evidence panes
   * use it for one unique Activity → Evidence anchor even when a bundle fans
   * out into several cards.
   */
  primaryGroupId?: string;
}
export interface InvestigationResourceContext extends DiagnosisResourceContext {
  issueSummary?: {
    count: number;
    highestSeverity?: string;
    topReason?: string;
    bySource?: Record<string, number>;
  };
  auditSummary?: {
    count: number;
    highestSeverity?: string;
    topFinding?: string;
  };
  policySummary?: unknown;
  podSummary?: unknown;
}
export interface InvestigationGitOpsDiagnosis {
  tool: "argocd" | "flux";
  sync?: string;
  health?: string;
  operationPhase?: string;
  suspended?: boolean;
  ready?: string;
  appliedRevision?: string;
}
export interface InvestigationKubernetesResource {
  apiVersion: string;
  kind: string;
  metadata: {
    name: string;
    namespace?: string;
    [key: string]: unknown;
  };
  spec?: unknown;
  status?: unknown;
  summaryContext?: unknown;
  [key: string]: unknown;
}
/** Current `list_resources` row shape (`ai/context.ResourceSummary`). */
export interface InvestigationResourceSummary {
  kind: string;
  name: string;
  namespace?: string;
  status?: string;
  ready?: string;
  issue?: string;
  age?: string;
  terminating?: boolean;
  restarts?: number;
  lastTerminatedReason?: string;
  lastRestartedAge?: string;
  summaryContext?: {
    health?: string;
    issueCount?: number;
    managedBy?: {
      kind: string;
      source: string;
      name: string;
      namespace?: string;
    };
  };
  [key: string]: unknown;
}
export interface InvestigationEventEvidence {
  reason: string;
  message: string;
  type: string;
  count: number;
  lastTimestamp: string;
}
export interface InvestigationTopologyNode {
  id: string;
  kind: string;
  name: string;
  status?: string;
  data?: Record<string, unknown> | null;
}
export interface InvestigationTopologyEdge {
  id?: string;
  source: string;
  target: string;
  type: string;
  label?: string;
}
export interface InvestigationNetworkRoute {
  route: string;
  target?: string;
  outcome: string;
  failedLayer?: string;
  confidence?: string;
  evidence?: string;
  benign?: boolean;
}
export interface InvestigationNetworkEvidence {
  subject: DiagnosisResourceRef;
  verdict: "healthy" | "degraded" | "broken" | "unknown";
  reason?: string;
  diagnosis?: {
    class?: string;
    severity?: string;
    summary: string;
    route?: string;
    nextAction?: string;
  };
  summary: {
    tested: number;
    passed: number;
    failed: number;
    derived?: number;
    skipped: number;
    headline: string;
  };
  routes: InvestigationNetworkRoute[];
}
/** One Prometheus rule as `get_prometheus_rules` flattens it (group stamped on). */
export interface InvestigationAlertRule {
  group: string;
  name: string;
  type: string;
  state?: string;
  health?: string;
  query?: string;
  labels: Record<string, string>;
}
/** One active instance of an alerting rule, with its own label set. */
export interface InvestigationAlertInstance {
  state: string;
  activeAt?: string;
  value?: string;
  labels: Record<string, string>;
  /** The instance's labels name the investigated resource. */
  namesTarget: boolean;
}
export interface InvestigationHelmOperation {
  kind: string;
  status: string;
  message: string;
  revision?: number;
  failedRevision?: number;
  rollbackRevision?: number;
  updated?: string;
}
export interface InvestigationHelmOwnedResource {
  kind: string;
  apiVersion?: string;
  name: string;
  namespace: string;
  status?: string;
  ready?: string;
  message?: string;
  summary?: string;
  issue?: string;
}
export interface InvestigationHelmRelease {
  name: string;
  namespace: string;
  /** Set only when Helm stores the release metadata elsewhere. */
  storageNamespace?: string;
  chart: string;
  chartVersion: string;
  appVersion?: string;
  status: string;
  revision: number;
  updated: string;
  description?: string;
  resourceHealth?: string;
  healthIssue?: string;
  healthSummary?: string;
  managedByFluxHelmRelease?: string;
  lastOperation?: InvestigationHelmOperation;
  resources: InvestigationHelmOwnedResource[];
}
export interface InvestigationPermissionSubject {
  kind: string;
  namespace?: string;
  name: string;
}
export interface InvestigationAccessCheck {
  verb: string;
  group?: string;
  resource: string;
  subresource?: string;
  /** Empty means a cluster-scoped resource or cluster-wide request. */
  namespace: string;
  resourceName?: string;
  allowed: boolean;
  denied: boolean;
  reason?: string;
  evaluationError?: string;
}
export interface InvestigationPermissionBinding {
  bindingKind: string;
  bindingNamespace?: string;
  bindingName: string;
  roleKind: string;
  roleNamespace?: string;
  roleName: string;
  rulesCount: number;
  inheritedFromGroup?: string;
}
export type InvestigationEvidenceData =
  | {
      type: "issue";
      issue: Issue;
      /**
       * A broad `issues` query can return failures from other resources. Keep
       * those factual observations, but do not let agent selection alone imply
       * that they explain the resource under investigation.
       */
      relevance: "target" | "producer-related" | "broader";
      /** Pods whose startup blocker repeats this issue word for word. */
      pods?: string[];
    }
  | {
      type: "startup";
      blocker: DiagnosisStartupBlocker;
      /** Exact blocker object when the diagnosis producer established it. */
      subject?: DiagnosisResourceRef;
      /**
       * Every pod the producer reported with this exact blocker. A DaemonSet
       * with seven pending pods is one finding, not seven; the card carries the
       * pod list instead of repeating itself.
       */
      pods?: string[];
    }
  | {
      type: "crash";
      crash: DiagnosisCrashCause;
      /** Namespace of the producing check's subject; pods live there. */
      namespace?: string;
    }
  | {
      type: "resource";
      resource: InvestigationKubernetesResource;
      resourceContext?: InvestigationResourceContext;
      warnings: string[];
      gitOpsDiagnosis?: InvestigationGitOpsDiagnosis;
    }
  | {
      type: "logs";
      pod: string;
      container: string;
      /** Namespace the producing call actually read; absent when unstated. */
      namespace?: string;
      previous: boolean;
      logs?: DiagnosisFilteredLogs;
      warnings: string[];
      error?: string;
    }
  | {
      type: "events";
      events: InvestigationEventEvidence[];
      scope: string;
    }
  | {
      type: "changes";
      changes: IssueRecentChange[];
      scope: string;
      changeContext?: DiagnosisChangeContext;
      /** The resource whose change history the producer read, when it named one. */
      subject?: { kind?: string; namespace?: string; name: string };
    }
  | { type: "dns"; dns: DiagnosisDNSContext }
  | { type: "network"; network: InvestigationNetworkEvidence }
  | {
      type: "relationships";
      root: DiagnosisResourceRef;
      nodes: InvestigationTopologyNode[];
      edges: InvestigationTopologyEdge[];
      truncated: boolean;
    }
  | {
      type: "topology";
      stats: { nodes: number; edges: number };
      namespaces: Array<{ namespace: string; chains: string[] }>;
      problems: string[];
      warnings: string[];
    }
  | {
      type: "inventory";
      resources: InvestigationResourceSummary[];
      scope: string;
    }
  | {
      type: "receipt";
      checked:
        "issues" | "events" | "changes" | "inventory" | "logs" | "alerts";
      scope: string;
      /**
       * Only when there is something to add. The card already shows the title
       * and the scope, so a line that restates either costs the reader a read
       * and returns nothing; this carries the reason the answer is what it is,
       * or the limit of what it proves.
       */
      message?: string;
    }
  | {
      type: "alerts";
      rule: InvestigationAlertRule;
      instances: InvestigationAlertInstance[];
      annotations: Record<string, string>;
    }
  | { type: "helm"; release: InvestigationHelmRelease }
  | {
      type: "permissions";
      subject: InvestigationPermissionSubject;
      /** Present for the access-check response shape. */
      accessCheck?: InvestigationAccessCheck;
      /** Present for the subject-permissions response shape. */
      bindings?: InvestigationPermissionBinding[];
      flatRulesCount?: number;
      truncated?: boolean;
      usedByPods?: string[];
      podsTotal?: number;
    }
  | InvestigationMetricsEvidence;
/** One PromQL vector selector as the producer tokenized it. */
export interface InvestigationMetricsSelector {
  metric: string;
  matchers: Array<{ label: string; op: string; value: string }>;
}
/**
 * A Prometheus result captured during the investigation. `origin` says which
 * producer ran the query; the shape is shared so every metrics card renders
 * the same way. `subject` is set only when the query's selectors are all
 * scoped to the investigation target.
 */
export interface InvestigationMetricsEvidence {
  type: "metrics";
  origin: "query" | "diagnose";
  query: string;
  mode: "range" | "instant";
  start?: string;
  end?: string;
  step?: string;
  unit?: string;
  label?: string;
  series: TimeSeries[];
  truncated: boolean;
  summary?: unknown;
  note?: string;
  selectors?: InvestigationMetricsSelector[];
  selectorsUnknown?: boolean;
  subject?: DiagnosisResourceRef;
  pods?: number;
  partial?: boolean;
}
export interface InvestigationEvidenceObservation {
  source: InvestigationEvidenceSource;
  revision: number;
  /** This exact observation predates a later successful verification of its proof scope. */
  historical: boolean;
  /** Whether this semantic item differs from its immediately previous observation. */
  changedFromPrevious: boolean;
  /**
   * How this producer-backed observation relates to the resource being
   * investigated. Broader observations remain useful context, but agent
   * selection alone must never promote them as support for this target.
   */
  relevance: InvestigationEvidenceRelevance;
  tier: InvestigationEvidenceTier;
  tone: DiagnosisEvidenceTone;
  title: string;
  summary?: string;
  data: InvestigationEvidenceData;
}
export interface InvestigationEvidenceGroup {
  /** Stable DOM-safe identity for this semantic evidence item. */
  id: string;
  /** Raw deterministic identity used to merge repeated observations. */
  identity: string;
  kind: InvestigationEvidenceKind;
  /** Latest observation predates the most recent completed verification turn. */
  historical: boolean;
  firstOrder: number;
  observations: InvestigationEvidenceObservation[];
  /** Strongest-provenance observation, newest when provenance is equal. */
  latest: InvestigationEvidenceObservation;
  /** Newest observation regardless of proof strength; used for chronology. */
  chronologicalLatest: InvestigationEvidenceObservation;
}
export interface InvestigationEvidenceLimitation extends DiagnosisEvidenceLimitationBase {
  /** A qualified history result, not a failed collection. */
  presentation?: "history";
  firstOrder: number;
  sources: InvestigationEvidenceSource[];
}
export interface InvestigationEvidenceCoverage {
  /** Completed calls to a tool with a typed evidence adapter. */
  attempted: number;
  /** Adapted calls that contributed at least one evidence group. */
  projected: number;
  /** Adapted calls with a producer-declared or transport limitation. */
  limited: number;
  /** Calls that produced a strict, successful zero-result receipt. */
  checked: number;
}
export interface InvestigationEvidenceProjection {
  groups: InvestigationEvidenceGroup[];
  limitations: InvestigationEvidenceLimitation[];
  sources: InvestigationEvidenceSource[];
  /** Every retained tool item carrying a server-issued ref, eligible or not. */
  evidenceRefSources: InvestigationEvidenceSource[];
  /** Complete confirmed-success sources eligible for server-authored links. */
  citableSources: InvestigationEvidenceSource[];
  coverage: InvestigationEvidenceCoverage;
}
export interface InvestigationRootCauseEvidenceLink {
  source: InvestigationEvidenceSource;
  /** Canonical semantic group containing this source’s primary observation. */
  originalGroupId?: string;
}
export interface InvestigationRootCauseEvidenceResolution {
  status: "linked" | "missing" | "invalid";
  links: InvestigationRootCauseEvidenceLink[];
}
