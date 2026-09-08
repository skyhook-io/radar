import {
  CORE_RESOURCES,
  defaultConditionTone,
  displayKind,
  englishPlural,
  kindToPlural,
  stripAnsi,
  type Issue,
  type IssueRecentChange,
  type Topology,
} from "@skyhook-io/k8s-ui";
import { fnv1a32 } from "@skyhook-io/k8s-ui/utils/structure-hash";
import type { TimeSeries } from "@skyhook-io/k8s-ui/components/charts";
import { apiVersionToGroup } from "../../utils/navigation";

import {
  diagnosisSeverityTone,
  type DiagnosisChangeContext,
  type DiagnosisCrashCause,
  type DiagnosisDNSContext,
  type DiagnosisEvidenceLimitationBase,
  type DiagnosisEvidenceTone,
  type DiagnosisFilteredLogs,
  type DiagnosisPodContainerRef,
  type DiagnosisPodLogEntry,
  type DiagnosisResourceContext,
  type DiagnosisResourceRef,
  type DiagnosisStartupBlocker,
} from "./diagnoseEvidenceTypes";
import type { RootCauseEvidence } from "../../api/diagnose";
import { investigationResourceEvidenceSummary } from "./investigationResourceEvidenceModel";
import { metricsUnitForExpression } from "./investigationMetrics";

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

type InvestigationSemanticDomain = "issue" | "startup" | "crash" | "dns";

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
      message: string;
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

function exactDiagnosisResourceRef(ref: {
  kind?: unknown;
  group?: unknown;
  namespace?: unknown;
  name?: unknown;
}): DiagnosisResourceRef | undefined {
  const { kind, name } = ref;
  if (
    !nonEmptyString(kind) ||
    kind.trim() !== kind ||
    !nonEmptyString(name) ||
    name.trim() !== name ||
    (ref.group !== undefined &&
      (typeof ref.group !== "string" || ref.group.trim() !== ref.group)) ||
    (ref.namespace !== undefined &&
      (typeof ref.namespace !== "string" ||
        ref.namespace.trim() !== ref.namespace))
  ) {
    return undefined;
  }
  const group = ref.group || undefined;
  const namespace = ref.namespace || undefined;
  const knownKinds = CORE_RESOURCES.filter(
    (resource) =>
      resource.kind.toLowerCase() === kind.toLowerCase() &&
      (group === undefined || resource.group === group),
  );
  const knownScopes = new Set(
    knownKinds.map((resource) => resource.namespaced),
  );
  // This is deliberately only a negative guard. Unknown kinds/groups may be
  // cluster-scoped CRDs, so suppress the link only when Radar's existing
  // resource metadata identifies the kind's scope without ambiguity.
  if (knownScopes.size === 1) {
    if (knownScopes.has(true) && !namespace) return undefined;
    if (knownScopes.has(false) && namespace) return undefined;
  }
  return {
    kind,
    name,
    ...(group ? { group } : {}),
    ...(namespace ? { namespace } : {}),
  };
}

/**
 * The Kubernetes resource an evidence item is about, when the producer payload
 * states one unambiguously. Pod-shaped evidence resolves only through the
 * namespace its producing check actually read; the investigation target's
 * namespace is deliberately never borrowed.
 */
export function investigationEvidenceSubjectRef(
  data: InvestigationEvidenceData,
): DiagnosisResourceRef | undefined {
  const ref = ((): DiagnosisResourceRef | undefined => {
    switch (data.type) {
      case "issue":
        return {
          kind: data.issue.kind,
          group: data.issue.group,
          namespace: data.issue.namespace,
          name: data.issue.name,
        };
      case "startup":
        return data.subject;
      case "resource": {
        const apiVersion = data.resource.apiVersion;
        const group = apiVersionToGroup(apiVersion);
        return {
          kind: data.resource.kind,
          group: group || undefined,
          namespace: data.resource.metadata.namespace,
          name: data.resource.metadata.name,
        };
      }
      case "logs":
        if (!data.namespace) return undefined;
        return { kind: "Pod", namespace: data.namespace, name: data.pod };
      case "crash":
        if (data.crash.pods.length !== 1 || !data.namespace) return undefined;
        return {
          kind: "Pod",
          namespace: data.namespace,
          name: data.crash.pods[0],
        };
      case "network":
        return data.network.subject;
      case "relationships":
        return data.root;
      case "helm":
        // A resource ref cannot carry the storage namespace, and opening the
        // release by name alone would read the wrong storage.
        if (
          data.release.storageNamespace !== undefined &&
          data.release.storageNamespace !== data.release.namespace
        ) {
          return undefined;
        }
        return {
          kind: "HelmRelease",
          group: "helm.sh",
          namespace: data.release.namespace,
          name: data.release.name,
        };
      case "permissions":
        // Users and Groups are principals, not Kubernetes objects.
        if (data.subject.kind !== "ServiceAccount") return undefined;
        return {
          kind: "ServiceAccount",
          namespace: data.subject.namespace,
          name: data.subject.name,
        };
      case "metrics":
        return data.subject;
      default:
        return undefined;
    }
  })();
  return ref ? exactDiagnosisResourceRef(ref) : undefined;
}

function domToken(value: string): string {
  // Underscore is reserved as the escape delimiter, so raw input can never
  // impersonate an encoded code point ("/" vs. the literal "_x2f_"). The
  // empty sentinel is safe for the same reason: a literal "_empty_" is escaped.
  if (!value) return "_empty_";
  return Array.from(value, (character) =>
    /[A-Za-z0-9-]/.test(character)
      ? character
      : `_x${character.codePointAt(0)!.toString(16)}_`,
  ).join("");
}

export function investigationEvidenceSourceId(
  turnIndex: number,
  stepId: string,
): string {
  return `turn-${turnIndex}-step-${domToken(stepId)}`;
}

export function investigationActivitySourceDomId(sourceId: string): string {
  return `investigation-activity-${sourceId}`;
}

export function investigationEvidenceSourceDomId(sourceId: string): string {
  return `investigation-evidence-${sourceId}`;
}

const investigationEvidenceRefRe = /^ev_[a-z2-7]{26,128}_[a-z2-7]{26,128}$/;

export function resolveInvestigationRootCauseEvidence(
  projection: InvestigationEvidenceProjection,
  evidence: RootCauseEvidence | undefined,
  assessmentTurnIndex: number,
): InvestigationRootCauseEvidenceResolution {
  if (!evidence || evidence.status === "missing") {
    return { status: "missing", links: [] };
  }
  if (evidence.status !== "linked") {
    return { status: "invalid", links: [] };
  }
  const refs = evidence.refs;
  if (
    !refs ||
    refs.length < 1 ||
    refs.length > 3 ||
    new Set(refs).size !== refs.length ||
    refs.some((ref) => !investigationEvidenceRefRe.test(ref)) ||
    refs.some((ref) => ref.split("_")[1] !== refs[0].split("_")[1])
  ) {
    return { status: "invalid", links: [] };
  }

  const byRef = new Map<string, InvestigationEvidenceSource[]>();
  for (const source of projection.evidenceRefSources) {
    if (source.turnIndex !== assessmentTurnIndex) continue;
    if (!source.evidenceRef) continue;
    const matches = byRef.get(source.evidenceRef) ?? [];
    matches.push(source);
    byRef.set(source.evidenceRef, matches);
  }
  const citableSourceIds = new Set(
    projection.citableSources
      .filter((source) => source.turnIndex === assessmentTurnIndex)
      .map((source) => source.id),
  );
  const links: InvestigationRootCauseEvidenceLink[] = [];
  for (const ref of refs) {
    const matches = byRef.get(ref);
    // Match the server's fail-closed binding: every current-turn occurrence
    // counts before success/completeness eligibility is considered.
    if (matches?.length !== 1 || !citableSourceIds.has(matches[0].id)) {
      return { status: "invalid", links: [] };
    }
    const source = matches[0];
    const originalGroup = source.primaryGroupId
      ? projection.groups.find((group) => group.id === source.primaryGroupId)
      : undefined;
    links.push({
      source,
      originalGroupId: originalGroup?.observations.some(
        (observation) => observation.source.id === source.id,
      )
        ? originalGroup.id
        : undefined,
    });
  }
  return { status: "linked", links };
}

export function investigationEvidenceStepIdsByTurn(
  projection: InvestigationEvidenceProjection,
  visibleGroupIds?: ReadonlySet<string>,
): Map<number, Set<string>> {
  const byTurn = new Map<number, Set<string>>();
  const linkedSourceIds = new Set<string>();
  for (const group of projection.groups) {
    if (visibleGroupIds && !visibleGroupIds.has(group.id)) continue;
    for (const observation of group.observations) {
      if (visibleGroupIds && observation.source.primaryGroupId !== group.id)
        continue;
      linkedSourceIds.add(observation.source.id);
    }
  }
  for (const limitation of projection.limitations) {
    for (const source of limitation.sources) linkedSourceIds.add(source.id);
  }
  const navigableSources = new Map(
    [...projection.sources, ...projection.citableSources].map((source) => [
      source.id,
      source,
    ]),
  );
  for (const source of navigableSources.values()) {
    if (!linkedSourceIds.has(source.id)) continue;
    const stepIds = byTurn.get(source.turnIndex) ?? new Set<string>();
    stepIds.add(source.stepId);
    byTurn.set(source.turnIndex, stepIds);
  }
  return byTurn;
}

function stableHash(value: string): string {
  // FNV-1a keeps long resource/log identities out of DOM IDs. The raw identity
  // remains the Map key, so a hash collision can never merge evidence.
  return fnv1a32(value).toString(36);
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function nonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0;
}

function stringArray(value: unknown): string[] | undefined {
  return Array.isArray(value) && value.every((item) => typeof item === "string")
    ? value
    : undefined;
}

function parseJSON(value: string): unknown {
  try {
    return JSON.parse(value);
  } catch {
    return undefined;
  }
}

function kubernetesResource(
  value: unknown,
): InvestigationKubernetesResource | undefined {
  const resource = record(value);
  const metadata = record(resource?.metadata);
  // Core Secrets intentionally use Radar's current safe detail contract rather
  // than a Kubernetes object: identity + type + key names, with no values. Make
  // that producer shape canonical for the projection instead of rejecting the
  // exact evidence the agent saw.
  if (
    resource?.kind === "Secret" &&
    !metadata &&
    nonEmptyString(resource.name) &&
    (resource.namespace === undefined ||
      typeof resource.namespace === "string") &&
    (resource.type === undefined || typeof resource.type === "string") &&
    Array.isArray(resource.keys) &&
    resource.keys.every((key) => typeof key === "string")
  ) {
    return {
      ...resource,
      apiVersion: "v1",
      metadata: {
        name: resource.name,
        namespace: resource.namespace as string | undefined,
        ...(record(resource.labels) ? { labels: resource.labels } : {}),
        ...(record(resource.annotations)
          ? { annotations: resource.annotations }
          : {}),
      },
    } as InvestigationKubernetesResource;
  }
  if (
    !resource ||
    !nonEmptyString(resource.apiVersion) ||
    !nonEmptyString(resource.kind) ||
    !metadata ||
    !nonEmptyString(metadata.name)
  ) {
    return undefined;
  }
  return resource as unknown as InvestigationKubernetesResource;
}

function gitOpsDiagnosis(
  value: unknown,
): InvestigationGitOpsDiagnosis | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    (candidate.tool !== "argocd" && candidate.tool !== "flux")
  ) {
    return undefined;
  }
  for (const field of [
    "sync",
    "health",
    "operationPhase",
    "ready",
    "appliedRevision",
  ] as const) {
    if (candidate[field] !== undefined && typeof candidate[field] !== "string")
      return undefined;
  }
  if (
    candidate.suspended !== undefined &&
    typeof candidate.suspended !== "boolean"
  )
    return undefined;
  return candidate as unknown as InvestigationGitOpsDiagnosis;
}

function resourceSummary(
  value: unknown,
): InvestigationResourceSummary | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.kind) ||
    !nonEmptyString(candidate.name)
  ) {
    return undefined;
  }
  if (
    candidate.namespace !== undefined &&
    typeof candidate.namespace !== "string"
  ) {
    return undefined;
  }
  for (const field of ["status", "ready", "issue", "age"] as const) {
    if (
      candidate[field] !== undefined &&
      typeof candidate[field] !== "string"
    ) {
      return undefined;
    }
  }
  if (
    (candidate.terminating !== undefined &&
      typeof candidate.terminating !== "boolean") ||
    (candidate.restarts !== undefined && typeof candidate.restarts !== "number")
  ) {
    return undefined;
  }
  const summaryContext = record(candidate.summaryContext);
  if (
    candidate.summaryContext !== undefined &&
    (!summaryContext ||
      (summaryContext.health !== undefined &&
        typeof summaryContext.health !== "string") ||
      (summaryContext.issueCount !== undefined &&
        typeof summaryContext.issueCount !== "number"))
  ) {
    return undefined;
  }
  return candidate as unknown as InvestigationResourceSummary;
}

function issue(value: unknown): Issue | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.id) ||
    (candidate.severity !== "critical" && candidate.severity !== "warning") ||
    !nonEmptyString(candidate.kind) ||
    !nonEmptyString(candidate.name) ||
    !nonEmptyString(candidate.reason)
  ) {
    return undefined;
  }
  return candidate as unknown as Issue;
}

function recentChange(value: unknown): IssueRecentChange | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.kind) ||
    (candidate.apiVersion !== undefined &&
      !nonEmptyString(candidate.apiVersion)) ||
    !nonEmptyString(candidate.name) ||
    !nonEmptyString(candidate.changeType) ||
    !nonEmptyString(candidate.timestamp)
  ) {
    return undefined;
  }
  return candidate as unknown as IssueRecentChange;
}

function diagnosisChangeContext(
  value: unknown,
): DiagnosisChangeContext | undefined {
  const candidate = record(value);
  if (!candidate || typeof candidate.changed !== "boolean") return undefined;
  for (const field of ["what", "when", "evidence"] as const) {
    if (candidate[field] !== undefined && typeof candidate[field] !== "string")
      return undefined;
  }
  return {
    changed: candidate.changed,
    ...(typeof candidate.what === "string"
      ? {
          what:
            candidate.what === "pod_template"
              ? "The workload's Pod template changed"
              : candidate.what,
        }
      : {}),
    ...(typeof candidate.when === "string" ? { when: candidate.when } : {}),
    ...(typeof candidate.evidence === "string"
      ? { evidence: candidate.evidence }
      : {}),
  };
}

function event(value: unknown): InvestigationEventEvidence | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.reason) ||
    !nonEmptyString(candidate.message) ||
    !nonEmptyString(candidate.type) ||
    typeof candidate.count !== "number" ||
    !nonEmptyString(candidate.lastTimestamp)
  ) {
    return undefined;
  }
  return candidate as unknown as InvestigationEventEvidence;
}

function filteredLogs(value: unknown): DiagnosisFilteredLogs | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    (!Array.isArray(candidate.lines) && candidate.lines !== null) ||
    (Array.isArray(candidate.lines) &&
      !candidate.lines.every((line) => typeof line === "string")) ||
    typeof candidate.totalLines !== "number" ||
    typeof candidate.matchedLines !== "number" ||
    typeof candidate.fallback !== "boolean"
  ) {
    return undefined;
  }
  return {
    ...(candidate as unknown as DiagnosisFilteredLogs),
    lines: Array.isArray(candidate.lines)
      ? candidate.lines.map((line) => stripAnsi(line))
      : candidate.lines,
  } as DiagnosisFilteredLogs;
}

function resourceRef(value: unknown): DiagnosisResourceRef | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.kind) ||
    !nonEmptyString(candidate.name)
  ) {
    return undefined;
  }
  return candidate as unknown as DiagnosisResourceRef;
}

function networkRoute(value: unknown): InvestigationNetworkRoute | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.route) ||
    !nonEmptyString(candidate.outcome)
  ) {
    return undefined;
  }
  for (const field of [
    "target",
    "failedLayer",
    "confidence",
    "evidence",
  ] as const) {
    if (candidate[field] !== undefined && typeof candidate[field] !== "string")
      return undefined;
  }
  if (candidate.benign !== undefined && typeof candidate.benign !== "boolean")
    return undefined;
  return candidate as unknown as InvestigationNetworkRoute;
}

function networkEvidence(
  value: Record<string, unknown>,
): InvestigationNetworkEvidence | undefined {
  const subject = resourceRef(value.subject);
  const summary = record(value.summary);
  const verdict = value.verdict;
  const routesRaw = value.routes === undefined ? [] : value.routes;
  if (
    !subject ||
    (verdict !== "healthy" &&
      verdict !== "degraded" &&
      verdict !== "broken" &&
      verdict !== "unknown") ||
    !summary ||
    typeof summary.tested !== "number" ||
    typeof summary.passed !== "number" ||
    typeof summary.failed !== "number" ||
    typeof summary.skipped !== "number" ||
    !nonEmptyString(summary.headline) ||
    (summary.derived !== undefined && typeof summary.derived !== "number") ||
    !Array.isArray(routesRaw)
  ) {
    return undefined;
  }
  const routes = routesRaw
    .map(networkRoute)
    .filter((route): route is InvestigationNetworkRoute => Boolean(route));
  if (routes.length !== routesRaw.length) return undefined;
  const diagnosis = record(value.diagnosis);
  if (
    diagnosis &&
    (!nonEmptyString(diagnosis.summary) ||
      ["class", "severity", "route", "nextAction"].some(
        (field) =>
          diagnosis[field] !== undefined &&
          typeof diagnosis[field] !== "string",
      ))
  ) {
    return undefined;
  }
  if (value.reason !== undefined && typeof value.reason !== "string")
    return undefined;
  return {
    subject,
    verdict,
    reason: value.reason as string | undefined,
    diagnosis: diagnosis as
      InvestigationNetworkEvidence["diagnosis"] | undefined,
    summary: summary as unknown as InvestigationNetworkEvidence["summary"],
    routes,
  };
}

function topologyNode(value: unknown): InvestigationTopologyNode | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.id) ||
    !nonEmptyString(candidate.kind) ||
    !nonEmptyString(candidate.name)
  ) {
    return undefined;
  }
  return candidate as unknown as InvestigationTopologyNode;
}

function topologyEdge(value: unknown): InvestigationTopologyEdge | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.source) ||
    !nonEmptyString(candidate.target) ||
    !nonEmptyString(candidate.type)
  ) {
    return undefined;
  }
  return candidate as unknown as InvestigationTopologyEdge;
}

type TopologyPartiality = {
  warnings: string[];
  largeCluster: boolean;
  hiddenKinds: string[];
  requiresNamespaceFilter: boolean;
  crdDiscoveryStatus?: NonNullable<Topology["crdDiscoveryStatus"]>;
  estimatedNodes?: number;
  summaryMode: boolean;
};

/**
 * Both get_topology wire shapes carry the same completeness metadata. Keep the
 * adapter strict: silently dropping a malformed flag would make a partial graph
 * look complete in Evidence.
 */
function topologyPartiality(
  value: Record<string, unknown>,
): TopologyPartiality | undefined {
  const warnings =
    value.warnings === undefined ? [] : stringArray(value.warnings);
  const hiddenKinds =
    value.hiddenKinds === undefined ? [] : stringArray(value.hiddenKinds);
  const discovery = value.crdDiscoveryStatus;
  const estimatedNodes = value.estimatedNodes;
  if (
    !warnings ||
    !hiddenKinds ||
    (value.largeCluster !== undefined &&
      typeof value.largeCluster !== "boolean") ||
    (value.requiresNamespaceFilter !== undefined &&
      typeof value.requiresNamespaceFilter !== "boolean") ||
    (value.summaryMode !== undefined &&
      typeof value.summaryMode !== "boolean") ||
    (discovery !== undefined &&
      discovery !== "idle" &&
      discovery !== "discovering" &&
      discovery !== "ready") ||
    (estimatedNodes !== undefined &&
      (typeof estimatedNodes !== "number" ||
        !Number.isSafeInteger(estimatedNodes) ||
        estimatedNodes < 0))
  ) {
    return undefined;
  }
  return {
    warnings,
    largeCluster: value.largeCluster === true,
    hiddenKinds,
    requiresNamespaceFilter: value.requiresNamespaceFilter === true,
    crdDiscoveryStatus: discovery as
      NonNullable<Topology["crdDiscoveryStatus"]> | undefined,
    estimatedNodes,
    summaryMode: value.summaryMode === true,
  };
}

function addTopologyLimitations(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  partiality: TopologyPartiality,
): void {
  for (const warning of partiality.warnings) {
    const normalized = warning.toLowerCase();
    builder.limit(
      source,
      "Topology coverage",
      warning,
      normalized.includes("large graph") || normalized.includes("too large")
        ? "truncated"
        : "unknown",
    );
  }

  const scaleDetails: string[] = [];
  const estimate = partiality.estimatedNodes
    ? ` (about ${partiality.estimatedNodes} estimated nodes)`
    : "";
  if (partiality.requiresNamespaceFilter) {
    scaleDetails.push(
      `The all-namespace topology was not built because the cluster is too large${estimate}; run a namespace-scoped topology search to collect a smaller graph.`,
    );
  } else if (partiality.largeCluster) {
    scaleDetails.push(
      `Large-cluster optimizations were active${estimate}; high-cardinality detail may be grouped.`,
    );
  }
  if (partiality.hiddenKinds.length > 0) {
    scaleDetails.push(
      `Resource kinds omitted by the large-cluster optimization: ${partiality.hiddenKinds.join(", ")}.`,
    );
  }
  if (partiality.summaryMode) {
    scaleDetails.push(
      "Summary mode collapsed individual Pods into workload or Service counts.",
    );
  }
  if (scaleDetails.length > 0) {
    builder.limit(
      source,
      "Topology scale",
      scaleDetails.join(" "),
      "truncated",
    );
  }

  if (
    partiality.crdDiscoveryStatus === "idle" ||
    partiality.crdDiscoveryStatus === "discovering"
  ) {
    builder.limit(
      source,
      "Custom Resource topology",
      partiality.crdDiscoveryStatus === "idle"
        ? "Custom Resource discovery had not started when this topology was captured; Custom Resource nodes and relationships may be missing."
        : "Custom Resource discovery was still in progress when this topology was captured; Custom Resource nodes and relationships may be missing.",
      "unknown",
    );
  }
}

function scopeFromArgs(source: InvestigationEvidenceSource): string {
  if (!source.args) return "requested scope";
  const args = record(parseJSON(source.args));
  if (!args) return "requested scope";
  const kind = nonEmptyString(args.kind) ? displayKind(args.kind) : undefined;
  const namespace = nonEmptyString(args.namespace) ? args.namespace : undefined;
  const name = nonEmptyString(args.name) ? args.name : undefined;
  if (kind && name) {
    return kind + " " + (namespace ? namespace + "/" : "") + name;
  }
  if (kind && namespace) return kind + " resources in " + namespace;
  if (kind) return kind + " resources";
  if (namespace && name) return namespace + "/" + name;
  if (namespace) return "namespace " + namespace;
  if (name) return name;
  return "requested scope";
}

const INVESTIGATION_RESULT_LABELS: Readonly<Record<string, string>> = {
  diagnose: "Workload diagnosis",
  issues: "Issue scan",
  get_resource: "Resource details",
  list_resources: "Resource inventory",
  get_events: "Kubernetes events",
  get_pod_logs: "Container logs",
  get_changes: "Recent changes",
  get_neighborhood: "Relationships",
  get_topology: "Topology",
  get_workload_logs: "Workload logs",
  get_prometheus_rules: "Alert rules",
  get_helm_release: "Helm release",
  get_subject_permissions: "Permissions",
  query_prometheus: "Prometheus query",
};

function investigationResultLabel(source: InvestigationEvidenceSource): string {
  return INVESTIGATION_RESULT_LABELS[source.tool] ?? "Investigation result";
}

function resourceMatchesTarget(
  target: InvestigationEvidenceTarget,
  resource: {
    kind: string;
    group?: string;
    namespace?: string;
    name: string;
  },
): boolean {
  return (
    resource.kind.toLowerCase() === target.kind.toLowerCase() &&
    (resource.group ?? "").toLowerCase() === target.group.toLowerCase() &&
    (resource.namespace ?? "") === (target.namespace ?? "") &&
    resource.name === target.name
  );
}

function relevanceForResource(
  builder: ProjectionBuilder,
  resource: {
    kind: string;
    group?: string;
    namespace?: string;
    name: string;
  },
): InvestigationEvidenceRelevance {
  return resourceMatchesTarget(builder.target, resource) ? "target" : "broader";
}

function sourceArgsRelevance(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  impliedKind?: string,
): InvestigationEvidenceRelevance {
  const args = record(source.args ? parseJSON(source.args) : undefined);
  const kind = nonEmptyString(args?.kind) ? args.kind : impliedKind;
  if (!kind || !nonEmptyString(args?.name)) return "broader";
  return relevanceForResource(builder, {
    kind,
    // get_events/get_changes/issues cannot express an API group today. An
    // omitted group is therefore unspecified, not proof that the caller meant
    // the core API group. Use the known investigation target for that missing
    // dimension; if a producer does provide a group, exact matching still
    // applies (including an explicitly empty core group).
    group: typeof args?.group === "string" ? args.group : builder.target.group,
    namespace: nonEmptyString(args?.namespace) ? args.namespace : undefined,
    name: args.name,
  });
}

function evidenceTierForRelevance(
  intended: InvestigationEvidenceTier,
  relevance: InvestigationEvidenceRelevance,
): InvestigationEvidenceTier {
  return relevance === "broader" ? "context" : intended;
}

const DIAGNOSABLE_WORKLOAD_KINDS = new Set([
  "pod",
  "deployment",
  "statefulset",
  "daemonset",
  "rollout",
]);

function isDiagnosableWorkloadKind(kind: string): boolean {
  return DIAGNOSABLE_WORKLOAD_KINDS.has(kind.toLowerCase());
}

function previousFromArgs(source: InvestigationEvidenceSource): boolean {
  return (
    record(source.args ? parseJSON(source.args) : undefined)?.previous === true
  );
}

class ProjectionBuilder {
  readonly groups: InvestigationEvidenceGroup[] = [];
  readonly sources: InvestigationEvidenceSource[] = [];
  readonly limitations: InvestigationEvidenceLimitation[] = [];
  readonly projectedSources = new Set<string>();
  readonly checkedSources = new Set<string>();
  readonly limitedSources = new Set<string>();
  readonly semanticCoverageBySource = new Map<
    string,
    Set<InvestigationSemanticDomain>
  >();

  private readonly groupByIdentity = new Map<
    string,
    InvestigationEvidenceGroup
  >();
  private readonly limitationByIdentity = new Map<
    string,
    InvestigationEvidenceLimitation
  >();

  constructor(readonly target: InvestigationEvidenceTarget) {}

  addSource(source: InvestigationEvidenceSource): void {
    this.sources.push(source);
  }

  coverSemantic(
    source: InvestigationEvidenceSource,
    domain: InvestigationSemanticDomain,
  ): void {
    const covered = this.semanticCoverageBySource.get(source.id);
    if (covered) {
      covered.add(domain);
    } else {
      this.semanticCoverageBySource.set(source.id, new Set([domain]));
    }
  }

  observe(
    identity: string,
    kind: InvestigationEvidenceKind,
    source: InvestigationEvidenceSource,
    observation: Omit<
      InvestigationEvidenceObservation,
      "source" | "revision" | "historical" | "changedFromPrevious" | "relevance"
    > & { relevance: InvestigationEvidenceRelevance },
  ): void {
    // Logs and synthesized startup/crash evidence do not carry a stable object
    // UID (and logs do not carry namespace in the producer row). Keep different
    // proof scopes separate so a same-named sibling can never inherit stronger
    // target provenance merely by colliding on its display identity.
    const partitionByRelevance =
      kind === "logs" ||
      kind === "startup" ||
      kind === "crash" ||
      (kind === "receipt" && identity.startsWith("previous-log-absence:"));
    const mapKey = groupKey(
      kind,
      identity,
      partitionByRelevance
        ? `${observation.relevance}\u0000${scopeFromArgs(source)}`
        : undefined,
    );
    let group = this.groupByIdentity.get(mapKey);
    const previousObservation = group?.observations.at(-1);
    const changedFromPrevious = previousObservation
      ? evidenceSemanticSnapshot(previousObservation) !==
        evidenceSemanticSnapshot(observation)
      : false;
    const next: InvestigationEvidenceObservation = {
      ...observation,
      source,
      revision: group ? group.observations.length + 1 : 1,
      historical: false,
      changedFromPrevious,
    };
    if (!group) {
      group = {
        id: `evidence-${kind}-${stableHash(mapKey)}`,
        identity,
        kind,
        historical: false,
        firstOrder: source.order,
        observations: [],
        latest: next,
        chronologicalLatest: next,
      };
      this.groupByIdentity.set(mapKey, group);
      this.groups.push(group);
    }
    group.observations.push(next);
    group.chronologicalLatest = next;
    const relevanceRank: Record<InvestigationEvidenceRelevance, number> = {
      target: 0,
      "producer-related": 1,
      broader: 2,
    };
    // A broad inventory/read can re-observe an item whose relationship to the
    // target was already established by a scoped producer. Keep the broad read
    // in revision history, but never let weaker provenance replace the card's
    // authoritative observation. Equal provenance still advances normally.
    // An alert rule's relevance is derived from its live instances, so a later
    // read of the same rule is a state transition, not weaker provenance.
    if (
      kind === "alerts" ||
      relevanceRank[next.relevance] <= relevanceRank[group.latest.relevance]
    ) {
      group.latest = next;
    }
    this.projectedSources.add(source.id);
    if (next.tier === "checked") this.checkedSources.add(source.id);
  }

  /** Relevance of the current card for an unpartitioned identity, if observed. */
  latestRelevance(
    kind: InvestigationEvidenceKind,
    identity: string,
  ): InvestigationEvidenceRelevance | undefined {
    return this.groupByIdentity.get(groupKey(kind, identity))?.latest.relevance;
  }

  limit(
    source: InvestigationEvidenceSource,
    label: string,
    message: string | undefined,
    kind: DiagnosisEvidenceLimitationBase["kind"],
    presentation?: InvestigationEvidenceLimitation["presentation"],
  ): void {
    if (!message?.trim()) return;
    const normalized = message.trim();
    const key = `${kind}\u0000${presentation ?? ""}\u0000${label}\u0000${normalized}`;
    const existing = this.limitationByIdentity.get(key);
    if (existing) {
      if (!existing.sources.some((item) => item.id === source.id)) {
        existing.sources.push(source);
      }
    } else {
      const limitation: InvestigationEvidenceLimitation = {
        source: label,
        message: normalized,
        kind,
        ...(presentation ? { presentation } : {}),
        firstOrder: source.order,
        sources: [source],
      };
      this.limitationByIdentity.set(key, limitation);
      this.limitations.push(limitation);
    }
    this.limitedSources.add(source.id);
  }
}

/**
 * The one place a group's map key is built. NUL separates the parts so no
 * kind or identity text can collide with another kind's key.
 */
function groupKey(
  kind: InvestigationEvidenceKind,
  identity: string,
  partition?: string,
): string {
  return `${kind}\u0000${identity}${partition === undefined ? "" : `\u0000${partition}`}`;
}

export function evidenceSemanticSnapshot(
  observation: Pick<
    InvestigationEvidenceObservation,
    "tone" | "title" | "summary" | "data"
  >,
): string {
  if (observation.data.type === "resource") {
    // Tool-specific context can change the summary/tone without changing the
    // object. Keep that context in history, not in the resource-change signal.
    return JSON.stringify({
      type: "resource",
      resource: observation.data.resource,
    });
  }
  const data =
    observation.data.type === "alerts"
      ? {
          type: observation.data.type,
          // An instance's sampled value and activation time move on every
          // evaluation; the finding is which instances exist and their state.
          rule: observation.data.rule,
          instances: observation.data.instances.map((instance) => ({
            state: instance.state,
            labels: instance.labels,
          })),
          annotations: observation.data.annotations,
        }
      : observation.data.type === "issue"
        ? {
            type: observation.data.type,
            // These are the finding and details shown in the evidence card.
            // Collection timestamps and detector bookkeeping do not change it.
            issue: {
              kind: observation.data.issue.kind,
              group: observation.data.issue.group,
              namespace: observation.data.issue.namespace,
              name: observation.data.issue.name,
              reason: observation.data.issue.reason,
              severity: observation.data.issue.severity,
              cause: observation.data.issue.cause,
              message: observation.data.issue.message,
            },
          }
        : observation.data;
  return JSON.stringify({
    tone: observation.tone,
    title: observation.title,
    summary: observation.summary,
    data,
  });
}

function contextFrom(value: unknown): InvestigationResourceContext | undefined {
  const candidate = record(value);
  if (!candidate || !nonEmptyString(candidate.tier)) return undefined;
  return candidate as unknown as InvestigationResourceContext;
}

function addNarrowHint(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  value: Record<string, unknown>,
): void {
  if (nonEmptyString(value.narrowHint)) {
    const label = investigationResultLabel(source);
    builder.limit(
      source,
      label,
      label +
        " was narrowed to keep this investigation bounded. Additional matching evidence may exist.",
      "truncated",
    );
  }
}

function addResourceContextLimitations(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  context: InvestigationResourceContext | undefined,
): void {
  if (!context) return;
  for (const value of Array.isArray(context.omitted) ? context.omitted : []) {
    const omitted = record(value);
    if (
      !omitted ||
      !nonEmptyString(omitted.field) ||
      !nonEmptyString(omitted.reason)
    )
      continue;
    builder.limit(
      source,
      omitted.field,
      `Resource context omitted: ${omitted.reason.replaceAll("_", " ")}.`,
      omitted.reason === "budget_exceeded" ? "truncated" : "unknown",
    );
  }
  if (context.referencedBy?.truncated) {
    const shown = context.referencedBy.items?.length ?? 0;
    builder.limit(
      source,
      "Relationships",
      `Referenced-by relationships were truncated (${shown} of ${context.referencedBy.total} returned).`,
      "truncated",
    );
  }
  if (context.appReferences?.staleSecretEnvTruncated) {
    builder.limit(
      source,
      "Application references",
      "Additional stale Secret environment reference groups were omitted.",
      "truncated",
    );
  }
}

function addIssueLimitations(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  value: Issue,
): void {
  if (value.members_truncated) {
    builder.limit(
      source,
      `Radar Issue ${value.id}`,
      "The affected-resource member list was truncated.",
      "truncated",
    );
  }
}

function resourceObservationSummary(
  resource: InvestigationKubernetesResource,
  context: InvestigationResourceContext | undefined,
  warnings: string[],
  gitOps?: InvestigationGitOpsDiagnosis,
  detailedIssueShown = false,
): string | undefined {
  if (!detailedIssueShown && context?.issueSummary?.topReason) {
    return context.issueSummary.topReason;
  }
  if (gitOps?.health) return `Health ${gitOps.health}`;
  if (gitOps?.ready) return `Ready ${gitOps.ready}`;
  if (gitOps?.sync) return `Sync ${gitOps.sync}`;
  if (gitOps?.suspended) return "Reconciliation suspended";
  const replicas = context?.workloadSummary?.replicas;
  if (replicas?.desired !== undefined) {
    const desired = replicas.desired;
    const ready = replicas.ready ?? 0;
    return `${ready}/${desired} replicas ready`;
  }
  if (context?.statusSummary?.phase) return context.statusSummary.phase;
  if (context?.issueSummary?.topReason) return context.issueSummary.topReason;
  return (
    investigationResourceEvidenceSummary(resource) ||
    warnings[0] ||
    resource.metadata.namespace
  );
}

function addResourceObservation(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  resource: InvestigationKubernetesResource,
  context: InvestigationResourceContext | undefined,
  warnings: string[],
  gitOpsDiagnosis: InvestigationGitOpsDiagnosis | undefined,
  hasDetailedCriticalIssue: boolean,
  relevance: InvestigationEvidenceRelevance,
): void {
  const issueSummary = context?.issueSummary;
  const severity = issueSummary?.highestSeverity ?? "";
  const critical = severity.toLowerCase() === "critical";
  const hasLiveIssue = (issueSummary?.count ?? 0) > 0;
  const replicas = context?.workloadSummary?.replicas;
  const desired = replicas?.desired;
  const ready = replicas ? (replicas.ready ?? 0) : undefined;
  const available = replicas ? (replicas.available ?? 0) : undefined;
  const replicaShortfall =
    desired !== undefined &&
    desired > 0 &&
    ((ready !== undefined && ready < desired) ||
      (available !== undefined && available < desired) ||
      (replicas?.unavailable ?? 0) > 0);
  const adverseCondition = (context?.statusSummary?.conditions ?? []).some(
    (condition) => defaultConditionTone(condition) === "fail",
  );
  const gitOpsAdverse = Boolean(
    gitOpsDiagnosis &&
    (gitOpsDiagnosis.health?.toLowerCase() === "degraded" ||
      gitOpsDiagnosis.health?.toLowerCase() === "missing" ||
      gitOpsDiagnosis.sync?.toLowerCase() === "outofsync" ||
      gitOpsDiagnosis.ready?.toLowerCase().startsWith("false") ||
      ["failed", "error"].includes(
        gitOpsDiagnosis.operationPhase?.toLowerCase() ?? "",
      )),
  );
  const hasAdverseState = replicaShortfall || adverseCondition || gitOpsAdverse;
  const intendedTier: InvestigationEvidenceTier =
    critical && !hasDetailedCriticalIssue
      ? "key"
      : hasLiveIssue
        ? "supporting"
        : hasAdverseState
          ? "supporting"
          : "context";
  const tier = evidenceTierForRelevance(intendedTier, relevance);
  const tone = hasLiveIssue
    ? diagnosisSeverityTone(severity)
    : hasAdverseState
      ? "warning"
      : warnings.length > 0
        ? "warning"
        : "neutral";
  const namespace = resource.metadata.namespace;
  const identity = `${resource.apiVersion}:${resource.kind}:${namespace ?? ""}:${resource.metadata.name}`;
  builder.observe(identity, "resource", source, {
    tier,
    relevance,
    tone,
    title: `${resource.kind} ${namespace ? `${namespace}/` : ""}${resource.metadata.name}`,
    summary: resourceObservationSummary(
      resource,
      context,
      warnings,
      gitOpsDiagnosis,
      hasDetailedCriticalIssue,
    ),
    data: {
      type: "resource",
      resource,
      resourceContext: context,
      warnings,
      gitOpsDiagnosis,
    },
  });
  addResourceContextLimitations(builder, source, context);
}

function addIssueObservation(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  value: Issue,
  producerRelevance: InvestigationEvidenceRelevance = "broader",
  pods?: string[],
): void {
  const matchesTarget = resourceMatchesTarget(builder.target, {
    kind: value.kind,
    group: value.group ?? "",
    namespace: value.namespace,
    name: value.name,
  });
  const relevance = matchesTarget ? "target" : producerRelevance;
  builder.observe(`issue:${value.id}`, "issue", source, {
    tier:
      relevance === "broader"
        ? "context"
        : value.severity === "critical"
          ? "key"
          : "supporting",
    tone: diagnosisSeverityTone(value.severity),
    relevance,
    title: value.reason,
    summary: value.cause || value.message,
    data: {
      type: "issue",
      issue: value,
      relevance,
      ...(pods && pods.length > 0 ? { pods } : {}),
    },
  });
  addIssueLimitations(builder, source, value);
}

function startupBlocker(value: unknown): DiagnosisStartupBlocker | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.kind) ||
    !nonEmptyString(candidate.name) ||
    !nonEmptyString(candidate.reason) ||
    !nonEmptyString(candidate.severity) ||
    !nonEmptyString(candidate.message)
  ) {
    return undefined;
  }
  return candidate as unknown as DiagnosisStartupBlocker;
}

function crashCause(value: unknown): DiagnosisCrashCause | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !Array.isArray(candidate.pods) ||
    !candidate.pods.every((pod) => typeof pod === "string") ||
    !nonEmptyString(candidate.container) ||
    !nonEmptyString(candidate.state) ||
    typeof candidate.exitCode !== "number" ||
    !nonEmptyString(candidate.logLine) ||
    !nonEmptyString(candidate.logSource) ||
    !nonEmptyString(candidate.logLineSelection)
  ) {
    return undefined;
  }
  return {
    ...(candidate as unknown as DiagnosisCrashCause),
    logLine: stripAnsi(candidate.logLine as string),
  };
}

function addEvents(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  values: InvestigationEventEvidence[],
  identity: string,
  complete = true,
  emptyIsAuthoritative = false,
  relevance: InvestigationEvidenceRelevance = "broader",
  emptyReceipt: { title: string; message: string } = {
    title: "No matching warning events",
    message: "The warning-event query completed and returned no groups.",
  },
): void {
  const scope = scopeFromArgs(source);
  if (values.length === 0) {
    if (!source.confirmedSuccess || !complete) return;
    if (!emptyIsAuthoritative) {
      builder.limit(
        source,
        "Events",
        "No events were returned. This result does not establish that no events occurred.",
        "unknown",
      );
      return;
    }
    builder.observe(identity, "receipt", source, {
      tier: evidenceTierForRelevance("checked", relevance),
      relevance,
      tone: "neutral",
      title: emptyReceipt.title,
      summary: scope,
      data: {
        type: "receipt",
        checked: "events",
        scope,
        message: emptyReceipt.message,
      },
    });
    return;
  }
  const lead = leadEvent(values);
  builder.observe(identity, "events", source, {
    tier: evidenceTierForRelevance(
      values.some((item) => item.type.toLowerCase() === "warning")
        ? "supporting"
        : "context",
      relevance,
    ),
    relevance,
    tone: values.some((item) => item.type.toLowerCase() === "warning")
      ? "warning"
      : "info",
    title: "Kubernetes events",
    summary: `${lead.reason}: ${lead.message}${
      values.length > 1 ? ` · ${values.length} event groups` : ""
    } · ${scope}`,
    data: { type: "events", events: values, scope },
  });
}

/**
 * The newest warning, else the newest event. The card lead must be what
 * happened, not how many groups the producer returned.
 */
function leadEvent(
  values: InvestigationEventEvidence[],
): InvestigationEventEvidence {
  const newest = (items: InvestigationEventEvidence[]) =>
    items.reduce((best, item) =>
      Date.parse(item.lastTimestamp) > Date.parse(best.lastTimestamp)
        ? item
        : best,
    );
  const warnings = values.filter(
    (item) => item.type.toLowerCase() === "warning",
  );
  return newest(warnings.length > 0 ? warnings : values);
}

function addChanges(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  values: IssueRecentChange[],
  identity: string,
  changeContext?: DiagnosisChangeContext,
  complete = true,
  emptyIsAuthoritative = false,
  relevance: InvestigationEvidenceRelevance = "broader",
  subject?: { kind?: string; namespace?: string; name: string },
  window?: string,
): void {
  const scope = scopeFromArgs(source);
  if (values.length === 0 && !changeContext?.changed) {
    if (!source.confirmedSuccess || !complete) return;
    if (!emptyIsAuthoritative) {
      builder.limit(
        source,
        "Recent changes",
        `No changes were returned for ${scope}. This result does not establish a complete change history.`,
        "unknown",
        "history",
      );
      return;
    }
    builder.observe(identity, "receipt", source, {
      tier: evidenceTierForRelevance("checked", relevance),
      relevance,
      tone: "neutral",
      title: "No tracked recent changes",
      summary: scope,
      data: {
        type: "receipt",
        checked: "changes",
        scope,
        message: "The requested change window returned no tracked changes.",
      },
    });
    return;
  }
  builder.observe(identity, "changes", source, {
    // A producer-correlated change supports the diagnosis. A merely recent
    // edit is chronology/context and must not imply causality by proximity.
    tier: evidenceTierForRelevance(
      changeContext?.changed ? "supporting" : "context",
      relevance,
    ),
    relevance,
    tone: "info",
    title: `Recent changes${window ? ` · last ${window}` : ""}`,
    summary: changeContext?.changed
      ? changeContext.what === "The workload's Pod template changed" &&
        changeContext.when
        ? `Pod template changed; newest ReplicaSet created ${changeContext.when} ago`
        : `${changeContext.what || "A workload change was observed"}${changeContext.when ? ` · ${changeContext.when} ago` : ""}`
      : `${values.length} change${values.length === 1 ? "" : "s"} · ${scope}`,
    data: {
      type: "changes",
      changes: values,
      scope,
      changeContext,
      subject,
    },
  });
}

function changesSubjectFromArgs(
  source: InvestigationEvidenceSource,
): { kind?: string; namespace?: string; name: string } | undefined {
  const args = record(source.args ? parseJSON(source.args) : undefined);
  if (!nonEmptyString(args?.name)) return undefined;
  return {
    ...(nonEmptyString(args.kind) ? { kind: args.kind } : {}),
    ...(nonEmptyString(args.namespace) ? { namespace: args.namespace } : {}),
    name: args.name,
  };
}

function addLogs(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  value: DiagnosisPodLogEntry,
  previous: boolean,
  warnings: string[] = [],
  relevance: InvestigationEvidenceRelevance = "broader",
  namespace?: string,
): void {
  const lines = (value.logs?.lines ?? []).map((line) => stripAnsi(line));
  const normalizedWarnings = warnings.map((warning) => stripAnsi(warning));
  const normalizedError = value.error ? stripAnsi(value.error) : undefined;
  if (lines.length === 0) {
    builder.limit(
      source,
      `${value.pod} / ${value.container}`,
      value.error ||
        "No log lines were available. This does not mean the container is healthy.",
      value.error ? "error" : "unknown",
    );
    return;
  }
  // A producer-filtered excerpt is a candidate, not proof that its contents are
  // adverse. Query strings and routine request logs can contain words such as
  // "warning" or "critical" and still be successful traffic. Promote only an
  // explicit failure/error signature; keep benign excerpts available in Context.
  const diagnosticSignal = [...lines, ...normalizedWarnings].some((line) =>
    /(?:\b(?:error|exception|failed|failure|fatal|panic|crash|denied|refused|timeout|timed out|unhealthy|oomkill|back-?off)\b|\s5\d\d(?:\s|$))/i.test(
      line,
    ),
  );
  const selectedEvidence = value.logs?.fallback !== true && diagnosticSignal;
  const identity = `logs:${previous ? "previous" : "current"}:${value.pod}:${value.container}`;
  builder.observe(identity, "logs", source, {
    // FilterLogs' raw-tail fallback is useful provenance, but the producer did
    // not select it as diagnostic signal. Keep it in Context; only filtered
    // excerpts are Supporting evidence.
    tier: evidenceTierForRelevance(
      selectedEvidence ? "supporting" : "context",
      relevance,
    ),
    relevance,
    tone: selectedEvidence || normalizedError ? "warning" : "neutral",
    title: `${previous ? "Previous" : "Current"} logs · ${value.pod} / ${value.container}`,
    summary:
      lines.length > 0
        ? `${lines.length} selected line${lines.length === 1 ? "" : "s"}`
        : "No log lines available",
    data: {
      type: "logs",
      pod: value.pod,
      container: value.container,
      namespace,
      previous,
      logs: value.logs ? { ...value.logs, lines } : undefined,
      warnings: normalizedWarnings,
      error: normalizedError,
    },
  });
  if (normalizedError) {
    builder.limit(
      source,
      `${value.pod} / ${value.container}`,
      normalizedError,
      "error",
    );
  }
}

function podContainerRef(value: unknown): DiagnosisPodContainerRef | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.pod) ||
    !nonEmptyString(candidate.container)
  ) {
    return undefined;
  }
  return candidate as unknown as DiagnosisPodContainerRef;
}

function parseLogEntry(value: unknown): DiagnosisPodLogEntry | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.pod) ||
    !nonEmptyString(candidate.container)
  ) {
    return undefined;
  }
  if (candidate.logs !== undefined && !filteredLogs(candidate.logs))
    return undefined;
  if (candidate.error !== undefined && typeof candidate.error !== "string")
    return undefined;
  return candidate as unknown as DiagnosisPodLogEntry;
}

function nonNegativeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0;
}

function diagnoseCrashQueryCoversTarget(
  source: InvestigationEvidenceSource,
): boolean {
  const args = record(source.args ? parseJSON(source.args) : undefined);
  if (!args) return false;

  // Crash retirement is target-wide. It therefore requires the producer's
  // normal all-container, full-time-window read. A caller-selected container,
  // since window, or shorter-than-default tail can validate that slice only;
  // it cannot clear a smoking gun from a stream it never revisited.
  if (nonEmptyString(args.container) || nonEmptyString(args.since))
    return false;
  if (args.tail_lines === undefined) return true;
  return (
    nonNegativeInteger(args.tail_lines) &&
    (args.tail_lines === 0 || args.tail_lines >= 100)
  );
}

/**
 * A missing crash candidate is meaningful only when diagnose actually read the
 * complete log surface that its crash classifier consumes. Keep this tied to
 * the current producer contract: a partial pod sample, a capped response, or a
 * failed stream is absence of evidence and must not clear earlier crash proof.
 */
function diagnoseCrashCoverageComplete(
  value: Record<string, unknown>,
): boolean {
  if (
    !nonNegativeInteger(value.pods) ||
    value.pods === 0 ||
    value.logsError !== undefined ||
    (value.crashCauseTruncated !== undefined &&
      typeof value.crashCauseTruncated !== "boolean") ||
    value.crashCauseTruncated === true
  ) {
    return false;
  }

  const coverage = record(value.logCoverage);
  if (
    !coverage ||
    !nonNegativeInteger(coverage.resolvedPods) ||
    !nonNegativeInteger(coverage.selectedPods) ||
    !nonNegativeInteger(coverage.shownLines) ||
    !nonNegativeInteger(coverage.totalLines) ||
    !nonNegativeInteger(coverage.shownPods) ||
    !nonNegativeInteger(coverage.totalPods) ||
    coverage.resolvedPods !== value.pods ||
    coverage.selectedPods !== coverage.resolvedPods ||
    (coverage.selectionTruncated !== undefined &&
      typeof coverage.selectionTruncated !== "boolean") ||
    coverage.selectionTruncated === true ||
    (coverage.contentTruncated !== undefined &&
      typeof coverage.contentTruncated !== "boolean") ||
    coverage.contentTruncated === true ||
    coverage.shownLines !== coverage.totalLines ||
    coverage.shownPods !== coverage.totalPods
  ) {
    return false;
  }

  const currentRaw = value.logsCurrent;
  const previousRaw = value.logsPrevious;
  if (
    !Array.isArray(currentRaw) ||
    currentRaw.length === 0 ||
    !Array.isArray(previousRaw)
  ) {
    return false;
  }

  const current = currentRaw.map(parseLogEntry);
  const previous = previousRaw.map(parseLogEntry);
  if (
    current.some(
      (entry) => !entry || entry.error !== undefined || !entry.logs,
    ) ||
    previous.some((entry) => !entry || !entry.logs)
  ) {
    return false;
  }

  const currentEntries = current as DiagnosisPodLogEntry[];
  const previousEntries = previous as DiagnosisPodLogEntry[];
  const streamKey = (entry: DiagnosisPodContainerRef) =>
    `${entry.pod}\u0000${entry.container}`;
  const currentKeys = new Set(currentEntries.map(streamKey));
  const previousKeys = new Set(previousEntries.map(streamKey));
  if (
    currentKeys.size !== current.length ||
    previousKeys.size !== previous.length ||
    currentKeys.size !== previousKeys.size ||
    [...currentKeys].some((key) => !previousKeys.has(key))
  ) {
    return false;
  }

  const absencesRaw = value.expectedPreviousLogAbsences;
  if (absencesRaw !== undefined && !Array.isArray(absencesRaw)) return false;
  const absences = Array.isArray(absencesRaw)
    ? absencesRaw.map(podContainerRef)
    : [];
  if (absences.some((entry) => !entry)) return false;
  const absenceEntries = absences as DiagnosisPodContainerRef[];
  const absenceKeys = new Set(absenceEntries.map(streamKey));
  if ([...absenceKeys].some((key) => !previousKeys.has(key))) return false;

  return previousEntries.every((entry) => {
    if (entry.error === undefined) return true;
    return nonEmptyString(entry.error) && absenceKeys.has(streamKey(entry));
  });
}

function invalidPayload(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  section = investigationResultLabel(source),
): void {
  builder.limit(
    source,
    section,
    "Radar couldn't summarize this investigation step. Review it in Activity.",
    "unknown",
  );
}

function adaptDiagnose(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  if (value) {
    const network = networkEvidence(value);
    if (network) {
      const relevance = relevanceForResource(builder, network.subject);
      const adverse =
        network.verdict === "broken" || network.verdict === "degraded";
      builder.observe(
        `network:${network.subject.group ?? ""}:${network.subject.kind}:${network.subject.namespace ?? ""}:${network.subject.name}`,
        "network",
        source,
        {
          tier: evidenceTierForRelevance(
            adverse ? "supporting" : "context",
            relevance,
          ),
          relevance,
          tone:
            network.verdict === "broken"
              ? "error"
              : network.verdict === "degraded"
                ? "warning"
                : network.verdict === "healthy"
                  ? "info"
                  : "neutral",
          title: `${network.subject.kind} path · ${network.subject.namespace ? `${network.subject.namespace}/` : ""}${network.subject.name}`,
          summary:
            network.diagnosis?.summary ||
            network.reason ||
            network.summary.headline,
          data: { type: "network", network },
        },
      );
      if (network.summary.skipped > 0) {
        builder.limit(
          source,
          "Network path coverage",
          `${network.summary.skipped} intended route${network.summary.skipped === 1 ? " was" : "s were"} not tested. ${network.summary.headline}`,
          "unknown",
        );
      }
      return;
    }
  }
  const resource = kubernetesResource(value?.resource);
  if (!value || !resource) {
    invalidPayload(builder, source);
    return;
  }
  const bundleRelevance = relevanceForResource(builder, {
    kind: resource.kind,
    group: apiVersionToGroup(resource.apiVersion),
    namespace: resource.metadata.namespace,
    name: resource.metadata.name,
  });
  const relatedRelevance: InvestigationEvidenceRelevance =
    bundleRelevance === "target" ? "producer-related" : "broader";
  const bundledRowRelevance = (
    kind: string,
    name: string,
  ): InvestigationEvidenceRelevance => {
    if (bundleRelevance !== "target") return "broader";
    const sameAsRoot =
      kind.toLowerCase() === resource.kind.toLowerCase() &&
      name === resource.metadata.name;
    const group = sameAsRoot
      ? apiVersionToGroup(resource.apiVersion)
      : kind.toLowerCase() === "pod"
        ? ""
        : undefined;
    return resourceMatchesTarget(builder.target, {
      kind,
      group,
      namespace: resource.metadata.namespace,
      name,
    })
      ? "target"
      : "producer-related";
  };
  addNarrowHint(builder, source, value);

  const relatedRaw = value.relatedIssues;
  const related = Array.isArray(relatedRaw)
    ? relatedRaw.map(issue).filter((item): item is Issue => Boolean(item))
    : [];
  const relatedValid =
    relatedRaw === undefined ||
    (Array.isArray(relatedRaw) && related.length === relatedRaw.length);
  if (!relatedValid) {
    invalidPayload(builder, source, "Classified issues");
  }
  const blockersRaw = value.startupBlockers;
  let blockersValid = blockersRaw === undefined || Array.isArray(blockersRaw);
  // Pods of one workload usually share a blocker word for word. Merge those
  // into one finding with the pod list; anything else keeps its own card and
  // its own identity so saved runs match as before.
  const blockerGroups: Array<{
    blocker: DiagnosisStartupBlocker;
    pods: string[];
    foldedInto?: Issue;
  }> = [];
  if (Array.isArray(blockersRaw)) {
    const merged = new Map<string, (typeof blockerGroups)[number]>();
    for (const raw of blockersRaw) {
      const blocker = startupBlocker(raw);
      if (!blocker) {
        blockersValid = false;
        invalidPayload(builder, source, "Startup evidence");
        continue;
      }
      const key =
        blocker.kind === "Pod"
          ? `${blocker.reason} ${blocker.severity} ${blocker.message}`
          : undefined;
      const existing = key ? merged.get(key) : undefined;
      if (existing) {
        if (!existing.pods.includes(blocker.name)) {
          existing.pods.push(blocker.name);
        }
        continue;
      }
      const entry = { blocker, pods: [blocker.name] };
      if (key) merged.set(key, entry);
      blockerGroups.push(entry);
    }
  }
  for (const item of related) {
    // `diagnose` is itself scoped to one resource and declares these rows as
    // related evidence. That producer contract is stronger than a broad
    // `issues` query, even when the related row is a child Pod.
    // A classified issue and the pods' startup blocker are the same fact
    // seen from the workload and from its pods; one card carries both.
    const folded = blockerGroups.find(
      (group) =>
        !group.foldedInto &&
        group.blocker.kind === "Pod" &&
        group.blocker.reason === item.reason &&
        (group.blocker.message === item.message ||
          group.blocker.message === item.cause),
    );
    if (folded) folded.foldedInto = item;
    addIssueObservation(builder, source, item, relatedRelevance, folded?.pods);
  }

  const context = contextFrom(value.resourceContext);
  if (value.resourceContext !== undefined && !context) {
    invalidPayload(builder, source, "Resource context");
  }
  const gitOps =
    value.gitopsDiagnosis === undefined
      ? undefined
      : gitOpsDiagnosis(value.gitopsDiagnosis);
  if (value.gitopsDiagnosis !== undefined && !gitOps) {
    invalidPayload(builder, source, "GitOps status");
  }
  if (
    source.confirmedSuccess &&
    related.length === 0 &&
    relatedValid &&
    isDiagnosableWorkloadKind(resource.kind)
  ) {
    const scope = scopeFromArgs(source);
    builder.observe(`issues:diagnose:${scope}`, "receipt", source, {
      tier: evidenceTierForRelevance("checked", bundleRelevance),
      relevance: bundleRelevance,
      tone: "neutral",
      title: "No classified workload issues",
      summary: scope,
      data: {
        type: "receipt",
        checked: "issues",
        scope,
        message:
          "Radar's workload diagnosis completed without a classified live issue for this resource.",
      },
    });
  }
  const warnings = stringArray(value.warnings) ?? [];
  addResourceObservation(
    builder,
    source,
    resource,
    context,
    warnings,
    gitOps,
    related.some((item) => item.severity === "critical"),
    bundleRelevance,
  );

  if (Array.isArray(blockersRaw)) {
    for (const { blocker, pods, foldedInto } of blockerGroups) {
      if (foldedInto) continue;
      const grouped = blocker.kind === "Pod" && pods.length > 1;
      builder.observe(
        grouped
          ? `startup:Pod:${blocker.reason}:${fnv1a32(blocker.message).toString(36)}`
          : `startup:${blocker.kind}:${blocker.name}:${blocker.reason}`,
        "startup",
        source,
        {
          tier: evidenceTierForRelevance(
            "key",
            bundledRowRelevance(blocker.kind, blocker.name),
          ),
          relevance: bundledRowRelevance(blocker.kind, blocker.name),
          tone: diagnosisSeverityTone(blocker.severity),
          title: blocker.reason,
          summary: grouped
            ? `${pods.length} pods · ${blocker.message}`
            : blocker.message,
          data: {
            type: "startup",
            blocker,
            ...(grouped ? { pods } : {}),
            subject: grouped
              ? undefined
              : (() => {
                  const namespace = resource.metadata.namespace;
                  if (!namespace) return undefined;
                  const rootGroup = apiVersionToGroup(resource.apiVersion);
                  const group =
                    blocker.kind === resource.kind
                      ? rootGroup
                      : blocker.kind === "Pod"
                        ? ""
                        : blocker.kind === "ReplicaSet"
                          ? "apps"
                          : undefined;
                  if (group === undefined) return undefined;
                  return {
                    kind: blocker.kind,
                    ...(group ? { group } : {}),
                    namespace,
                    name: blocker.name,
                  };
                })(),
          },
        },
      );
    }
  } else if (blockersRaw !== undefined) {
    invalidPayload(builder, source, "Startup evidence");
  }

  const crashesRaw = value.crashCause;
  let crashesValid =
    (crashesRaw === undefined || Array.isArray(crashesRaw)) &&
    (value.crashCauseTruncated === undefined ||
      typeof value.crashCauseTruncated === "boolean");
  if (Array.isArray(crashesRaw)) {
    for (const raw of crashesRaw) {
      const crash = crashCause(raw);
      if (!crash) {
        crashesValid = false;
        invalidPayload(builder, source, "Crash evidence");
        continue;
      }
      const pods = [...crash.pods].sort().join(",");
      const crashRelevance =
        crash.pods.length === 1
          ? bundledRowRelevance("Pod", crash.pods[0])
          : relatedRelevance;
      builder.observe(
        `crash:${pods}:${crash.container}:${crash.state}:${crash.reason ?? ""}`,
        "crash",
        source,
        {
          tier: evidenceTierForRelevance("key", crashRelevance),
          relevance: crashRelevance,
          tone: "error",
          title: `${crash.container} ${crash.reason || crash.state}`,
          summary: crash.logLine,
          data: {
            type: "crash",
            crash,
            namespace: resource.metadata.namespace,
          },
        },
      );
    }
  } else if (crashesRaw !== undefined) {
    invalidPayload(builder, source, "Crash evidence");
  }
  if (value.crashCauseTruncated === true) {
    builder.limit(
      source,
      "Crash evidence",
      "Additional crash-cause candidates were omitted.",
      "truncated",
    );
  }

  if (
    source.confirmedSuccess &&
    bundleRelevance === "target" &&
    isDiagnosableWorkloadKind(resource.kind)
  ) {
    if (relatedValid) builder.coverSemantic(source, "issue");
    if (blockersValid) builder.coverSemantic(source, "startup");
    if (
      crashesValid &&
      diagnoseCrashQueryCoversTarget(source) &&
      diagnoseCrashCoverageComplete(value)
    ) {
      builder.coverSemantic(source, "crash");
    }
    // DNSContext is emitted only for positive symptoms/configuration. The
    // producer has no explicit negative DNS coverage receipt, so its absence
    // cannot safely retire earlier DNS evidence.
  }

  const expectedAbsencesRaw = value.expectedPreviousLogAbsences;
  const expectedAbsences = Array.isArray(expectedAbsencesRaw)
    ? expectedAbsencesRaw
        .map(podContainerRef)
        .filter((item): item is DiagnosisPodContainerRef => Boolean(item))
    : [];
  if (
    expectedAbsencesRaw !== undefined &&
    (!Array.isArray(expectedAbsencesRaw) ||
      expectedAbsences.length !== expectedAbsencesRaw.length)
  ) {
    invalidPayload(builder, source, "Previous-log status");
  }
  const expectedAbsenceKeys = new Set(
    expectedAbsences.map((item) => `${item.pod}\u0000${item.container}`),
  );
  if (source.confirmedSuccess) {
    for (const item of expectedAbsences) {
      const logRelevance = bundledRowRelevance("Pod", item.pod);
      builder.observe(
        `previous-log-absence:${item.pod}:${item.container}`,
        "receipt",
        source,
        {
          tier: evidenceTierForRelevance("checked", logRelevance),
          relevance: logRelevance,
          tone: "neutral",
          title: "No previous container instance expected",
          summary: `${item.pod} / ${item.container}`,
          data: {
            type: "receipt",
            checked: "logs",
            scope: `${item.pod} / ${item.container}`,
            message:
              "Captured container status shows zero restarts and no prior termination, so a previous log stream should not exist.",
          },
        },
      );
    }
  }

  for (const [field, previous] of [
    ["logsCurrent", false],
    ["logsPrevious", true],
  ] as const) {
    const raw = value[field];
    if (Array.isArray(raw)) {
      for (const item of raw) {
        const entry = parseLogEntry(item);
        if (
          entry &&
          previous &&
          expectedAbsenceKeys.has(`${entry.pod}\u0000${entry.container}`) &&
          (entry.logs?.lines?.length ?? 0) === 0
        ) {
          continue;
        }
        if (entry) {
          addLogs(
            builder,
            source,
            entry,
            previous,
            [],
            bundledRowRelevance("Pod", entry.pod),
            resource.metadata.namespace,
          );
        } else
          invalidPayload(
            builder,
            source,
            previous ? "Previous logs" : "Current logs",
          );
      }
      if (raw.length === 0 && field === "logsCurrent") {
        builder.limit(
          source,
          "Current logs",
          "No container logs were available, so Radar could not evaluate them.",
          "unknown",
        );
      }
    } else if (raw !== undefined) {
      invalidPayload(
        builder,
        source,
        previous ? "Previous logs" : "Current logs",
      );
    } else if (
      field === "logsCurrent" &&
      typeof value.pods === "number" &&
      value.pods > 0 &&
      !nonEmptyString(value.logsError)
    ) {
      // Empty slices are omitted by the Go producer. With resolved pods this
      // means the read yielded no stream rows, not that logs proved anything.
      builder.limit(
        source,
        "Current logs",
        "No container logs were available, so Radar could not evaluate them.",
        "unknown",
      );
    }
  }
  if (nonEmptyString(value.logsError)) {
    builder.limit(source, "Logs", value.logsError, "error");
  }
  const logCoverage = record(value.logCoverage);
  if (logCoverage?.selectionTruncated === true) {
    const selected =
      typeof logCoverage.selectedPods === "number"
        ? logCoverage.selectedPods
        : "some";
    const resolved =
      typeof logCoverage.resolvedPods === "number"
        ? logCoverage.resolvedPods
        : "the resolved";
    builder.limit(
      source,
      "Log pod coverage",
      `Log collection selected ${selected} of ${resolved} pods.`,
      "truncated",
    );
  }
  if (logCoverage?.contentTruncated === true) {
    const shown =
      typeof logCoverage.shownLines === "number"
        ? logCoverage.shownLines
        : "a subset of";
    const total =
      typeof logCoverage.totalLines === "number"
        ? logCoverage.totalLines
        : "the returned";
    builder.limit(
      source,
      "Log excerpt coverage",
      `The response includes ${shown} of ${total} selected log lines after the size limit; these are excerpts, not the container's full log history.`,
      "truncated",
    );
  }

  const eventsRaw = value.events;
  if (Array.isArray(eventsRaw)) {
    const events = eventsRaw
      .map(event)
      .filter((item): item is InvestigationEventEvidence => Boolean(item));
    if (events.length === eventsRaw.length) {
      addEvents(
        builder,
        source,
        events,
        `events:diagnose:${scopeFromArgs(source)}`,
        typeof value.eventsTotalGroups !== "number" ||
          value.eventsTotalGroups <= events.length,
        true,
        relatedRelevance,
      );
    } else {
      invalidPayload(builder, source, "Events");
    }
  } else if (
    eventsRaw === undefined &&
    value.gitopsDiagnosis === undefined &&
    !nonEmptyString(value.eventsError)
  ) {
    addEvents(
      builder,
      source,
      [],
      `events:diagnose:${scopeFromArgs(source)}`,
      true,
      true,
      relatedRelevance,
    );
  } else if (eventsRaw !== undefined) {
    invalidPayload(builder, source, "Events");
  }
  if (nonEmptyString(value.eventsError)) {
    builder.limit(source, "Events", value.eventsError, "error");
  }
  if (
    typeof value.eventsTotalGroups === "number" &&
    Array.isArray(eventsRaw) &&
    value.eventsTotalGroups > eventsRaw.length
  ) {
    builder.limit(
      source,
      "Events",
      `Radar received ${eventsRaw.length} of ${value.eventsTotalGroups} event groups.`,
      "truncated",
    );
  }

  const changesRaw = value.recentChanges;
  const changesCoverageLimitedRaw = value.recentChangesCoverageLimited;
  const changesCoverageLimitedValid =
    changesCoverageLimitedRaw === undefined ||
    typeof changesCoverageLimitedRaw === "boolean";
  const changesCoverageLimited = changesCoverageLimitedRaw === true;
  if (!changesCoverageLimitedValid) {
    invalidPayload(builder, source, "Recent changes");
  }
  const changeContext =
    value.changeContext === undefined
      ? undefined
      : diagnosisChangeContext(value.changeContext);
  if (value.changeContext !== undefined && !changeContext) {
    invalidPayload(builder, source, "Change correlation");
  }
  const diagnoseChangesSubject = {
    kind: resource.kind,
    namespace: resource.metadata.namespace,
    name: resource.metadata.name,
  };
  if (Array.isArray(changesRaw)) {
    const changes = changesRaw
      .map(recentChange)
      .filter((item): item is IssueRecentChange => Boolean(item));
    if (changes.length === changesRaw.length) {
      addChanges(
        builder,
        source,
        changes,
        `changes:diagnose:${scopeFromArgs(source)}`,
        changeContext,
        value.recentChangesSaturated !== true &&
          !changesCoverageLimited &&
          changesCoverageLimitedValid,
        true,
        relatedRelevance,
        diagnoseChangesSubject,
      );
    } else {
      invalidPayload(builder, source, "Recent changes");
    }
  } else if (
    changesRaw === undefined &&
    value.gitopsDiagnosis === undefined &&
    !nonEmptyString(value.recentChangesError)
  ) {
    addChanges(
      builder,
      source,
      [],
      `changes:diagnose:${scopeFromArgs(source)}`,
      changeContext,
      value.recentChangesSaturated !== true &&
        !changesCoverageLimited &&
        changesCoverageLimitedValid,
      true,
      relatedRelevance,
      diagnoseChangesSubject,
    );
  } else if (changesRaw !== undefined) {
    invalidPayload(builder, source, "Recent changes");
  }
  if (nonEmptyString(value.recentChangesError)) {
    builder.limit(source, "Recent changes", value.recentChangesError, "error");
  }
  if (value.recentChangesSaturated === true) {
    builder.limit(
      source,
      "Recent changes",
      "The recent-change result limit was reached; additional changes may exist in the requested window.",
      "truncated",
    );
  }
  if (changesCoverageLimited) {
    builder.limit(
      source,
      "Recent changes",
      "Recent-change coverage is limited because Radar could not confirm permission to read every referenced source. The visible result cannot prove that no recent changes exist.",
      "unknown",
    );
  }

  const dns = record(value.dnsContext);
  if (dns) {
    const signals = stringArray(dns.signals) ?? [];
    const findings = Array.isArray(dns.coreDNSFindings)
      ? dns.coreDNSFindings
      : [];
    if (signals.length > 0 || findings.length > 0) {
      const adverse =
        findings.length > 0 ||
        signals.some((signal) =>
          /\b(error|failed|failures?|timeouts?|nxdomain|servfail)\b/i.test(
            signal,
          ),
        );
      builder.observe(`dns:${scopeFromArgs(source)}`, "dns", source, {
        tier: evidenceTierForRelevance(
          adverse ? "supporting" : "context",
          relatedRelevance,
        ),
        relevance: relatedRelevance,
        tone: adverse ? "warning" : "info",
        title: adverse ? "DNS failure signals" : "DNS configuration",
        summary:
          signals[0] ||
          `${findings.length} CoreDNS finding${findings.length === 1 ? "" : "s"}`,
        data: { type: "dns", dns: dns as unknown as DiagnosisDNSContext },
      });
    }
  } else if (value.dnsContext !== undefined) {
    invalidPayload(builder, source, "DNS context");
  }
}

function adaptIssues(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  const raw = value?.issues;
  if (
    !value ||
    !Array.isArray(raw) ||
    typeof value.total !== "number" ||
    typeof value.total_matched !== "number"
  ) {
    invalidPayload(builder, source);
    return;
  }
  const issues = raw.map(issue).filter((item): item is Issue => Boolean(item));
  if (issues.length !== raw.length) {
    invalidPayload(builder, source);
    return;
  }
  addNarrowHint(builder, source, value);
  if (
    value.total_matched > issues.length &&
    !nonEmptyString(value.narrowHint)
  ) {
    builder.limit(
      source,
      "Issues",
      `The query returned ${issues.length} of ${value.total_matched} matching issues.`,
      "truncated",
    );
  }
  if (typeof value.filter_errors === "number" && value.filter_errors > 0) {
    builder.limit(
      source,
      "Issues filter",
      nonEmptyString(value.filter_error_sample)
        ? value.filter_error_sample
        : `${value.filter_errors} issue rows could not be evaluated by the filter.`,
      "error",
    );
  }
  if (value.recent_changes_truncated === true) {
    builder.limit(
      source,
      "Issue-related changes",
      "The issue response omitted some recent changes.",
      "truncated",
    );
  }
  if (value.correlation_truncated === true) {
    builder.limit(
      source,
      "Issue change correlation",
      "Change correlation was not evaluated for every returned issue.",
      "truncated",
    );
  }
  const visibility = record(value.visibility);
  if (
    visibility &&
    nonEmptyString(visibility.state) &&
    visibility.state !== "ok"
  ) {
    builder.limit(
      source,
      "Issue visibility",
      nonEmptyString(visibility.impact)
        ? visibility.impact
        : `Radar reported ${visibility.state} visibility for this issue query.`,
      "unknown",
    );
  }
  const issueChangesRaw = value.recent_changes;
  if (Array.isArray(issueChangesRaw)) {
    const changes = issueChangesRaw
      .map(recentChange)
      .filter((item): item is IssueRecentChange => Boolean(item));
    if (changes.length === issueChangesRaw.length) {
      addChanges(
        builder,
        source,
        changes,
        `changes:issues:${source.args ?? scopeFromArgs(source)}`,
        undefined,
        value.recent_changes_truncated !== true,
      );
    } else {
      invalidPayload(builder, source, "Issue-related changes");
    }
  } else if (issueChangesRaw !== undefined) {
    invalidPayload(builder, source, "Issue-related changes");
  }
  const clusterDNS = record(record(value.cluster_context)?.dns);
  if (clusterDNS) {
    const signals = stringArray(clusterDNS.signals) ?? [];
    const findings = Array.isArray(clusterDNS.findings)
      ? clusterDNS.findings
      : [];
    if (signals.length > 0 || findings.length > 0) {
      builder.observe(`dns:issues:${scopeFromArgs(source)}`, "dns", source, {
        tier: "context",
        relevance: "broader",
        tone: "warning",
        title: "Cluster DNS signals",
        summary:
          signals[0] ||
          `${findings.length} CoreDNS finding${findings.length === 1 ? "" : "s"}`,
        data: {
          type: "dns",
          dns: {
            signals,
            coreDNSFindings: findings as DiagnosisDNSContext["coreDNSFindings"],
          },
        },
      });
    }
  }
  if (issues.length === 0) {
    if (!source.confirmedSuccess || builder.limitedSources.has(source.id))
      return;
    const args = record(source.args ? parseJSON(source.args) : undefined);
    if (!nonEmptyString(args?.namespace)) {
      builder.limit(
        source,
        "Issues",
        "Radar found no matching issues, but the search covered only namespaces the current user can access.",
        "unknown",
      );
      return;
    }
    const scope = scopeFromArgs(source);
    const relevance = sourceArgsRelevance(builder, source);
    builder.observe(`issues:${source.args ?? scope}`, "receipt", source, {
      tier: evidenceTierForRelevance("checked", relevance),
      relevance,
      tone: "neutral",
      title: "No matching live issues",
      summary: scope,
      data: {
        type: "receipt",
        checked: "issues",
        scope,
        message:
          "Radar's live-issue query completed and returned no matching issues.",
      },
    });
  } else {
    for (const item of issues) addIssueObservation(builder, source, item);
  }
}

function adaptGetResource(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  // Current producer modes are discriminated by the resource's own identity,
  // never by guessing an undocumented legacy wrapper:
  //   1. bare Kubernetes resource
  //   2. {resource, resourceContext, warnings}
  //   3. the same wrapper with requested extras.
  // Core Secrets use the producer's deliberately value-free detail shape,
  // normalized by kubernetesResource above.
  const bare = kubernetesResource(payload);
  const wrapper = record(payload);
  const resource = bare ?? kubernetesResource(wrapper?.resource);
  if (!resource) {
    invalidPayload(builder, source);
    return;
  }
  const value = bare ? undefined : wrapper;
  const context = value ? contextFrom(value.resourceContext) : undefined;
  if (value?.resourceContext !== undefined && !context) {
    invalidPayload(builder, source, "Resource context");
  }
  const warnings = stringArray(value?.warnings) ?? [];
  const relevance = relevanceForResource(builder, {
    kind: resource.kind,
    group: apiVersionToGroup(resource.apiVersion),
    namespace: resource.metadata.namespace,
    name: resource.metadata.name,
  });
  addResourceObservation(
    builder,
    source,
    resource,
    context,
    warnings,
    undefined,
    false,
    relevance,
  );
  if (!value) return;
  addNarrowHint(builder, source, value);

  const errors: Array<[string, string]> = [
    ["eventsError", "Events"],
    ["recentChangesError", "Recent changes"],
    ["metricsError", "Metrics"],
    ["revisionsError", "Revisions"],
    ["includeError", "Requested include"],
  ];
  for (const [field, label] of errors) {
    if (nonEmptyString(value[field])) {
      builder.limit(source, label, value[field] as string, "error");
    }
  }

  if (Array.isArray(value.events)) {
    const events = value.events
      .map(event)
      .filter((item): item is InvestigationEventEvidence => Boolean(item));
    if (events.length === value.events.length) {
      addEvents(
        builder,
        source,
        events,
        `events:get-resource:${scopeFromArgs(source)}`,
        (typeof value.eventsTotalGroups !== "number" ||
          value.eventsTotalGroups <= events.length) &&
          !nonEmptyString(value.eventsError),
        true,
        relevance,
      );
      if (
        typeof value.eventsTotalGroups === "number" &&
        value.eventsTotalGroups > events.length
      ) {
        builder.limit(
          source,
          "Events",
          `Radar received ${events.length} of ${value.eventsTotalGroups} event groups.`,
          "truncated",
        );
      }
    } else {
      invalidPayload(builder, source, "Events");
    }
  }
  const resourceChangesSubject = {
    kind: resource.kind,
    namespace: resource.metadata.namespace,
    name: resource.metadata.name,
  };
  const recentChangesRaw = value.recentChanges;
  const recentChangesSaturatedRaw = value.recentChangesSaturated;
  const recentChangesCoverageLimitedRaw = value.recentChangesCoverageLimited;
  const hasRecentChangesResult =
    recentChangesRaw !== undefined ||
    recentChangesSaturatedRaw !== undefined ||
    recentChangesCoverageLimitedRaw !== undefined;
  const recentChangesMetadataValid =
    typeof recentChangesSaturatedRaw === "boolean" &&
    typeof recentChangesCoverageLimitedRaw === "boolean";
  if (hasRecentChangesResult && !recentChangesMetadataValid) {
    invalidPayload(builder, source, "Recent changes");
  }
  if (Array.isArray(recentChangesRaw)) {
    const changes = recentChangesRaw
      .map(recentChange)
      .filter((item): item is IssueRecentChange => Boolean(item));
    if (changes.length === recentChangesRaw.length) {
      addChanges(
        builder,
        source,
        changes,
        `changes:get-resource:${scopeFromArgs(source)}`,
        undefined,
        recentChangesMetadataValid &&
          recentChangesSaturatedRaw === false &&
          recentChangesCoverageLimitedRaw === false &&
          !nonEmptyString(value.recentChangesError),
        true,
        relevance,
        resourceChangesSubject,
      );
    } else {
      invalidPayload(builder, source, "Recent changes");
    }
  } else if (
    recentChangesRaw === undefined &&
    recentChangesMetadataValid &&
    !nonEmptyString(value.recentChangesError)
  ) {
    addChanges(
      builder,
      source,
      [],
      `changes:get-resource:${scopeFromArgs(source)}`,
      undefined,
      recentChangesSaturatedRaw === false &&
        recentChangesCoverageLimitedRaw === false,
      true,
      relevance,
      resourceChangesSubject,
    );
  } else if (recentChangesRaw !== undefined) {
    invalidPayload(builder, source, "Recent changes");
  }
  if (recentChangesSaturatedRaw === true) {
    builder.limit(
      source,
      "Recent changes",
      "The recent-change result limit was reached; additional changes may exist in the requested window.",
      "truncated",
    );
  }
  if (recentChangesCoverageLimitedRaw === true) {
    builder.limit(
      source,
      "Recent changes",
      `Change history for ${scopeFromArgs(source)} is incomplete.`,
      "unknown",
      "history",
    );
  }
}

function adaptListResources(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  if (!Array.isArray(payload)) {
    invalidPayload(builder, source);
    return;
  }
  const resources = payload
    .map(resourceSummary)
    .filter((item): item is InvestigationResourceSummary => Boolean(item));
  if (resources.length !== payload.length) {
    invalidPayload(builder, source);
    return;
  }
  const scope = scopeFromArgs(source);
  const args = record(parseJSON(source.args ?? ""));
  const kinds = [...new Set(resources.map((resource) => resource.kind))];
  const noun =
    kinds.length === 1
      ? kindToPlural(kinds[0]) === kinds[0].toLowerCase()
        ? displayKind(kinds[0])
        : englishPlural(displayKind(kinds[0]))
      : "Resources";
  const namespace = nonEmptyString(args?.namespace)
    ? args.namespace
    : undefined;
  const title = namespace ? `${noun} in ${namespace}` : noun;
  if (resources.length === 0) {
    // list_resources intentionally returns [] for some RBAC-filtered reads;
    // even a successful transport outcome therefore cannot prove absence.
    builder.limit(
      source,
      "Resource inventory",
      `Radar found no matching resources for ${scope}, but access restrictions may have hidden some results.`,
      "unknown",
    );
    return;
  }
  const hasAdverseResource = resources.some((resource) => {
    const health = resource.summaryContext?.health?.toLowerCase();
    return (
      health === "unhealthy" ||
      health === "degraded" ||
      (resource.summaryContext?.issueCount ?? 0) > 0
    );
  });
  builder.observe(`inventory:${source.args ?? scope}`, "inventory", source, {
    tier: "context",
    relevance: "broader",
    tone: hasAdverseResource ? "warning" : "neutral",
    title,
    summary: `${resources.length} returned${nonEmptyString(args?.group) ? ` · ${args.group}` : ""}`,
    data: { type: "inventory", resources, scope },
  });
}

function adaptEvents(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  // The producer serializes an empty result as a nil slice, which is null.
  const eventsRaw = value?.events === null ? [] : value?.events;
  if (!value || !Array.isArray(eventsRaw)) {
    invalidPayload(builder, source);
    return;
  }
  const events = eventsRaw
    .map(event)
    .filter((item): item is InvestigationEventEvidence => Boolean(item));
  if (events.length !== eventsRaw.length) {
    invalidPayload(builder, source);
    return;
  }
  addNarrowHint(builder, source, value);
  // The producer answers a namespace the caller cannot read with an empty
  // list and marks it. Only that case is a coverage gap. Any other complete,
  // successful empty read answers the question it asked and is filed as a
  // checked receipt; a gap there would contradict an events card from another
  // call in the same turn. The receipt names the one remaining ambiguity for
  // producers that predate the marker.
  if (value.accessDenied === true) {
    builder.limit(
      source,
      "Events",
      `Events in ${scopeFromArgs(source)} are not readable with your permissions.`,
      "error",
    );
    return;
  }
  addEvents(
    builder,
    source,
    events,
    `events:${source.args ?? scopeFromArgs(source)}`,
    !nonEmptyString(value.narrowHint),
    true,
    sourceArgsRelevance(builder, source),
    {
      title: "No events matched",
      message:
        "The events query completed and returned nothing for this scope. Events outside its window or filters are not covered; a namespace you cannot read also returns nothing.",
    },
  );
}

function adaptPodLogs(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  const logs = filteredLogs(payload) ?? filteredLogs(value);
  if (!logs) {
    invalidPayload(builder, source);
    return;
  }
  if (value) addNarrowHint(builder, source, value);
  const warnings = stringArray(value?.warnings) ?? [];
  const args = record(source.args ? parseJSON(source.args) : undefined);
  const pod = nonEmptyString(args?.name) ? args.name : "Pod";
  const container = nonEmptyString(args?.container)
    ? args.container
    : "default container";
  addLogs(
    builder,
    source,
    { pod, container, logs },
    previousFromArgs(source),
    warnings,
    sourceArgsRelevance(builder, source, "Pod"),
    nonEmptyString(args?.namespace) ? args.namespace : undefined,
  );
}

function adaptChanges(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  // The producer serializes an empty result as a nil slice, which is null.
  const changesRaw = value?.changes === null ? [] : value?.changes;
  if (!value || !Array.isArray(changesRaw)) {
    invalidPayload(builder, source, "Recent changes");
    return;
  }
  const changes = changesRaw
    .map(recentChange)
    .filter((item): item is IssueRecentChange => Boolean(item));
  if (changes.length !== changesRaw.length) {
    invalidPayload(builder, source, "Recent changes");
    return;
  }
  addNarrowHint(builder, source, value);
  let sourceErrors = 0;
  if (Array.isArray(value.sourcesErrored)) {
    for (const sourceError of value.sourcesErrored) {
      if (nonEmptyString(sourceError)) {
        sourceErrors += 1;
        builder.limit(source, "Recent changes", sourceError, "error");
      }
    }
  } else if (value.sourcesErrored !== undefined) {
    invalidPayload(builder, source, "Recent changes source coverage");
  }
  // Reads of one resource's history are revisions of one card however the
  // window or cap differs; the window is stated in the title instead.
  const args = record(source.args ? parseJSON(source.args) : undefined);
  addChanges(
    builder,
    source,
    changes,
    `changes:${scopeFromArgs(source)}`,
    undefined,
    !nonEmptyString(value.narrowHint) && sourceErrors === 0,
    false,
    sourceArgsRelevance(builder, source),
    changesSubjectFromArgs(source),
    nonEmptyString(args?.since) ? args.since : undefined,
  );
}

function adaptNeighborhood(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  const root = resourceRef(value?.root);
  const subgraph = record(value?.subgraph);
  if (
    !value ||
    !root ||
    !subgraph ||
    !Array.isArray(subgraph.nodes) ||
    !Array.isArray(subgraph.edges) ||
    typeof value.truncated !== "boolean"
  ) {
    invalidPayload(builder, source);
    return;
  }
  const nodes = subgraph.nodes
    .map(topologyNode)
    .filter((item): item is InvestigationTopologyNode => Boolean(item));
  const edges = subgraph.edges
    .map(topologyEdge)
    .filter((item): item is InvestigationTopologyEdge => Boolean(item));
  if (
    nodes.length !== subgraph.nodes.length ||
    edges.length !== subgraph.edges.length
  ) {
    invalidPayload(builder, source);
    return;
  }
  addNarrowHint(builder, source, value);
  if (value.truncated === true && !nonEmptyString(value.narrowHint)) {
    builder.limit(
      source,
      "Relationships",
      "The relationship view reached its resource limit and may be incomplete.",
      "truncated",
    );
  }
  for (const raw of Array.isArray(value.omitted) ? value.omitted : []) {
    const omitted = record(raw);
    if (
      !omitted ||
      !nonEmptyString(omitted.field) ||
      !nonEmptyString(omitted.reason)
    )
      continue;
    builder.limit(
      source,
      omitted.field,
      `Relationship context omitted: ${omitted.reason.replaceAll("_", " ")}.`,
      omitted.reason === "budget_exceeded" ? "truncated" : "unknown",
    );
  }
  builder.observe(
    `relationships:${root.group ?? ""}:${root.kind}:${root.namespace ?? ""}:${root.name}`,
    "relationships",
    source,
    {
      tier: "context",
      relevance: relevanceForResource(builder, {
        kind: root.kind,
        group: root.group ?? "",
        namespace: root.namespace,
        name: root.name,
      }),
      tone: "info",
      title: `Relationships around ${root.kind} ${root.name}`,
      summary: `${nodes.length} nodes · ${edges.length} relationships`,
      data: {
        type: "relationships",
        root,
        nodes,
        edges,
        truncated: value.truncated,
      },
    },
  );
}

function adaptTopology(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  if (!value) {
    invalidPayload(builder, source);
    return;
  }
  const partiality = topologyPartiality(value);
  if (!partiality) {
    invalidPayload(builder, source, "Topology coverage metadata");
    return;
  }
  const stats = record(value.stats);
  if (
    stats &&
    typeof stats.nodes === "number" &&
    typeof stats.edges === "number" &&
    Array.isArray(value.namespaces)
  ) {
    const namespaces = value.namespaces.flatMap((raw) => {
      const namespace = record(raw);
      const chains = stringArray(namespace?.chains);
      return namespace && nonEmptyString(namespace.namespace) && chains
        ? [{ namespace: namespace.namespace, chains }]
        : [];
    });
    if (namespaces.length !== value.namespaces.length) {
      invalidPayload(builder, source);
      return;
    }
    const problems =
      value.problems === undefined ? [] : stringArray(value.problems);
    if (!problems) {
      invalidPayload(builder, source, "Topology problems");
      return;
    }
    addTopologyLimitations(builder, source, partiality);
    builder.observe(
      `topology:${source.args ?? scopeFromArgs(source)}`,
      "topology",
      source,
      {
        tier: "context",
        relevance: "broader",
        tone: problems.length > 0 ? "warning" : "info",
        title: "Resource topology",
        summary: `${stats.nodes} nodes · ${stats.edges} relationships`,
        data: {
          type: "topology",
          stats: { nodes: stats.nodes, edges: stats.edges },
          namespaces,
          problems,
          warnings: partiality.warnings,
        },
      },
    );
    return;
  }

  if (!Array.isArray(value.nodes) || !Array.isArray(value.edges)) {
    invalidPayload(builder, source);
    return;
  }
  const nodes = value.nodes
    .map(topologyNode)
    .filter((item): item is InvestigationTopologyNode => Boolean(item));
  const edges = value.edges
    .map(topologyEdge)
    .filter((item): item is InvestigationTopologyEdge => Boolean(item));
  if (
    nodes.length !== value.nodes.length ||
    edges.length !== value.edges.length
  ) {
    invalidPayload(builder, source);
    return;
  }
  const problems = nodes
    .filter((node) => node.status === "unhealthy" || node.status === "degraded")
    .map((node) => `${node.kind} ${node.name}: ${node.status}`);
  addTopologyLimitations(builder, source, partiality);
  builder.observe(
    `topology:${source.args ?? scopeFromArgs(source)}`,
    "topology",
    source,
    {
      tier: "context",
      relevance: "broader",
      tone: problems.length > 0 ? "warning" : "info",
      title: "Resource topology",
      summary: `${nodes.length} nodes · ${edges.length} relationships`,
      data: {
        type: "topology",
        stats: { nodes: nodes.length, edges: edges.length },
        namespaces: [],
        problems,
        warnings: partiality.warnings,
      },
    },
  );
}

const WORKLOAD_LOG_KINDS: Readonly<Record<string, string>> = {
  deployments: "Deployment",
  statefulsets: "StatefulSet",
  daemonsets: "DaemonSet",
  rollouts: "Rollout",
  jobs: "Job",
  workflows: "Workflow",
};

/** `get_workload_logs` states what it read as `<plural>/<namespace>/<name>`. */
function workloadLogsSubject(
  value: string,
): { kind: string; namespace: string; name: string } | undefined {
  const parts = value.split("/");
  if (parts.length !== 3) return undefined;
  const kind = WORKLOAD_LOG_KINDS[parts[0]];
  if (!kind || !parts[1] || !parts[2]) return undefined;
  return { kind, namespace: parts[1], name: parts[2] };
}

function adaptWorkloadLogs(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  const workload = nonEmptyString(value?.workload)
    ? workloadLogsSubject(value.workload)
    : undefined;
  if (!value || !workload || !nonNegativeInteger(value.pods)) {
    invalidPayload(builder, source);
    return;
  }
  // The producer names the workload without an API group; as with other
  // group-less producers, the investigation target supplies that dimension.
  const workloadRelevance = relevanceForResource(builder, {
    ...workload,
    group: builder.target.group,
  });
  const scope = `${displayKind(workload.kind)} ${workload.namespace}/${workload.name}`;
  const previous = previousFromArgs(source);
  addNarrowHint(builder, source, value);
  if (nonEmptyString(value.logsError)) {
    builder.limit(source, "Workload logs", value.logsError, "error");
    return;
  }
  if (value.pods === 0) {
    // The producer replaces the stream list with a message when it resolved
    // no pods; any other shape is not this response.
    if (
      typeof value.logs !== "string" ||
      (value.emptyMessage !== undefined &&
        typeof value.emptyMessage !== "string") ||
      value.narrowHint !== undefined
    ) {
      invalidPayload(builder, source);
      return;
    }
    if (!source.confirmedSuccess) return;
    const message = nonEmptyString(value.emptyMessage)
      ? value.emptyMessage
      : nonEmptyString(value.logs)
        ? value.logs
        : "The workload resolved no pods, so there were no log streams to read.";
    builder.observe(
      `workload-logs:${previous ? "previous" : "current"}:${scope}`,
      "receipt",
      source,
      {
        tier: evidenceTierForRelevance("checked", workloadRelevance),
        relevance: workloadRelevance,
        tone: "neutral",
        title: "No pods to read logs from",
        summary: scope,
        data: { type: "receipt", checked: "logs", scope, message },
      },
    );
    return;
  }
  const logsRaw = value.logs;
  const noStreams = `No log streams were returned for the ${value.pods} resolved pod${value.pods === 1 ? "" : "s"}, so Radar could not evaluate them.`;
  if (!Array.isArray(logsRaw)) {
    if (logsRaw === undefined || logsRaw === null) {
      builder.limit(source, "Workload logs", noStreams, "unknown");
    } else {
      invalidPayload(builder, source);
    }
    return;
  }
  const warnings = stringArray(value.warnings) ?? [];
  for (const raw of logsRaw) {
    const entry = parseLogEntry(raw);
    if (!entry) {
      invalidPayload(
        builder,
        source,
        previous ? "Previous logs" : "Current logs",
      );
      continue;
    }
    // A Pod target is named exactly by its own row; a workload target relates
    // to its rows the way diagnose relates to the pods it resolved.
    const rowRelevance: InvestigationEvidenceRelevance = resourceMatchesTarget(
      builder.target,
      {
        kind: "Pod",
        group: "",
        namespace: workload.namespace,
        name: entry.pod,
      },
    )
      ? "target"
      : workloadRelevance === "target"
        ? "producer-related"
        : "broader";
    addLogs(
      builder,
      source,
      entry,
      previous,
      warnings,
      rowRelevance,
      workload.namespace,
    );
  }
  if (logsRaw.length === 0) {
    builder.limit(source, "Workload logs", noStreams, "unknown");
  }
}

function stringRecord(value: unknown): Record<string, string> | undefined {
  const candidate = record(value);
  if (!candidate) return undefined;
  return Object.values(candidate).every((item) => typeof item === "string")
    ? (candidate as Record<string, string>)
    : undefined;
}

/**
 * Pods a producer has already tied to the investigated workload in this
 * projection: the streams diagnose or a workload-logs read resolved for it,
 * its crash candidates, and its Pod startup blockers. A pod name matched
 * against this set is producer-established membership, never inference from
 * a name prefix, which a sibling such as `api-worker` would also satisfy.
 */
function producerEstablishedTargetPods(
  builder: ProjectionBuilder,
): Set<string> {
  const pods = new Set<string>();
  const namespace = builder.target.namespace;
  if (!namespace) return pods;
  for (const group of builder.groups) {
    for (const observation of group.observations) {
      if (observation.relevance === "broader") continue;
      const data = observation.data;
      if (data.type === "logs" && data.namespace === namespace) {
        pods.add(data.pod);
      } else if (data.type === "crash" && data.namespace === namespace) {
        for (const pod of data.crash.pods) pods.add(pod);
      } else if (
        data.type === "startup" &&
        data.subject?.kind === "Pod" &&
        data.subject.namespace === namespace
      ) {
        pods.add(data.subject.name);
      }
    }
  }
  return pods;
}

// kube-state-metrics label names for the owning workload.
const WORKLOAD_LABEL_BY_KIND: Readonly<Record<string, string>> = {
  deployment: "deployment",
  statefulset: "statefulset",
  daemonset: "daemonset",
  rollout: "rollout",
  job: "job_name",
  cronjob: "cronjob",
};

/**
 * Whether a Prometheus label set names the investigated resource: the target
 * namespace plus a label identifying it directly, or a `pod` label naming a
 * pod a producer already tied to it.
 */
function labelsNameTarget(
  target: InvestigationEvidenceTarget,
  labels: Record<string, string>,
  targetPods: ReadonlySet<string>,
): boolean {
  if (!target.namespace || labels.namespace !== target.namespace) return false;
  const kind = target.kind.toLowerCase();
  if (kind === "pod") return labels.pod === target.name;
  const workloadLabel = WORKLOAD_LABEL_BY_KIND[kind];
  if (workloadLabel && labels[workloadLabel] === target.name) return true;
  if (
    labels.workload === target.name &&
    (labels.workload_type === undefined ||
      labels.workload_type.toLowerCase() === kind)
  ) {
    return true;
  }
  return labels.pod !== undefined && targetPods.has(labels.pod);
}

/**
 * The label set places the instance in another namespace outright. Only a
 * namespaced target can be excluded this way; for a cluster-scoped target
 * every namespace label is "different" and proves nothing.
 */
function labelsPlaceElsewhere(
  target: InvestigationEvidenceTarget,
  labels: Record<string, string>,
): boolean {
  return (
    Boolean(target.namespace) &&
    nonEmptyString(labels.namespace) &&
    labels.namespace !== target.namespace
  );
}

function alertRule(value: unknown):
  | {
      rule: InvestigationAlertRule;
      instances: Omit<InvestigationAlertInstance, "namesTarget">[];
      annotations: Record<string, string>;
    }
  | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.group) ||
    !nonEmptyString(candidate.name) ||
    !nonEmptyString(candidate.type) ||
    typeof candidate.query !== "string"
  ) {
    return undefined;
  }
  for (const field of ["state", "health"] as const) {
    if (candidate[field] !== undefined && typeof candidate[field] !== "string")
      return undefined;
  }
  const labels =
    candidate.labels === undefined ? {} : stringRecord(candidate.labels);
  const annotations =
    candidate.annotations === undefined
      ? {}
      : stringRecord(candidate.annotations);
  if (!labels || !annotations) return undefined;
  const alertsRaw = candidate.alerts === undefined ? [] : candidate.alerts;
  if (!Array.isArray(alertsRaw)) return undefined;
  const instances: Omit<InvestigationAlertInstance, "namesTarget">[] = [];
  for (const raw of alertsRaw) {
    const instance = record(raw);
    const instanceLabels =
      instance?.labels === undefined ? {} : stringRecord(instance.labels);
    if (
      !instance ||
      !nonEmptyString(instance.state) ||
      !instanceLabels ||
      (instance.activeAt !== undefined &&
        typeof instance.activeAt !== "string") ||
      (instance.value !== undefined && typeof instance.value !== "string")
    ) {
      return undefined;
    }
    instances.push({
      state: instance.state,
      activeAt: instance.activeAt as string | undefined,
      value: instance.value as string | undefined,
      labels: instanceLabels,
    });
  }
  return {
    rule: {
      group: candidate.group,
      name: candidate.name,
      type: candidate.type,
      state: candidate.state as string | undefined,
      health: candidate.health as string | undefined,
      query: candidate.query,
      labels,
    },
    instances,
    annotations,
  };
}

function targetIdentity(target: InvestigationEvidenceTarget): string {
  return `${displayKind(target.kind)} ${target.namespace ? `${target.namespace}/` : ""}${target.name}`;
}

function adaptPrometheusRules(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  if (
    !value ||
    !Array.isArray(value.rules) ||
    typeof value.count !== "number" ||
    value.count !== value.rules.length ||
    (value.truncated !== undefined && typeof value.truncated !== "boolean") ||
    (value.note !== undefined && typeof value.note !== "string")
  ) {
    invalidPayload(builder, source);
    return;
  }
  const rules = value.rules
    .map(alertRule)
    .filter((item): item is NonNullable<typeof item> => Boolean(item));
  if (rules.length !== value.rules.length) {
    invalidPayload(builder, source);
    return;
  }
  const args = record(source.args ? parseJSON(source.args) : undefined);
  const stateFilter = nonEmptyString(args?.state)
    ? args.state.toLowerCase()
    : undefined;
  const typeFilter = nonEmptyString(args?.type)
    ? args.type.toLowerCase()
    : undefined;
  const truncated = value.truncated === true;
  if (truncated) {
    builder.limit(
      source,
      "Alert rules",
      `The rule list was capped, so additional matching rules may exist${nonEmptyString(value.note) ? `; ${value.note}` : ""}.`,
      "truncated",
    );
  }
  const identity = targetIdentity(builder.target);
  const targetPods = producerEstablishedTargetPods(builder);
  // Rule names are not unique within a group, and group names are unique
  // only per rule file, which the producer does not return. The expression
  // and static labels distinguish rules; identical rows within one response
  // cannot be told apart at all, so they stay scoped to this read rather
  // than inherit each other's history in whatever order they arrive.
  const ruleDefinition = (rule: InvestigationAlertRule) =>
    `alerts:${rule.group}:${rule.name}:${stableHash(
      JSON.stringify([rule.query, Object.entries(rule.labels).sort()]),
    )}`;
  const definitionCounts = new Map<string, number>();
  for (const { rule } of rules) {
    const definition = ruleDefinition(rule);
    definitionCounts.set(
      definition,
      (definitionCounts.get(definition) ?? 0) + 1,
    );
  }
  const seenRuleIdentities = new Map<string, number>();
  let namesTarget = false;
  let healthGap = false;
  let alertingCount = 0;
  let everyInstanceElsewhere = true;
  for (const { rule, instances: rawInstances, annotations } of rules) {
    // Recording rules carry no state and never describe a resource.
    if (rule.type.toLowerCase() !== "alerting") continue;
    alertingCount += 1;
    const instances = rawInstances.map((instance) => ({
      ...instance,
      namesTarget: labelsNameTarget(
        builder.target,
        instance.labels,
        targetPods,
      ),
    }));
    if (
      instances.length === 0 ||
      instances.some(
        (instance) => !labelsPlaceElsewhere(builder.target, instance.labels),
      )
    ) {
      everyInstanceElsewhere = false;
    }
    const definition = ruleDefinition(rule);
    const ambiguous = (definitionCounts.get(definition) ?? 0) > 1;
    const ordinal = seenRuleIdentities.get(definition) ?? 0;
    seenRuleIdentities.set(definition, ordinal + 1);
    const ruleIdentity = ambiguous
      ? `${definition}:${source.id}:${ordinal}`
      : definition;
    const previousRelevance = ambiguous
      ? undefined
      : builder.latestRelevance("alerts", ruleIdentity);
    const relevance: InvestigationEvidenceRelevance = instances.some(
      (instance) => instance.namesTarget,
    )
      ? "target"
      : labelsNameTarget(builder.target, rule.labels, targetPods)
        ? "producer-related"
        : // The same rule read again with no instances left has resolved for
          // the target it named before; keep that provenance so the
          // resolution stays visible instead of the stale firing card.
          instances.length === 0 &&
            previousRelevance !== undefined &&
            previousRelevance !== "broader"
          ? previousRelevance
          : "broader";
    if (relevance !== "broader") namesTarget = true;
    const state = (rule.state ?? "").toLowerCase();
    const active = state === "firing" || state === "pending";
    const severity = (rule.labels.severity ?? "").toLowerCase();
    const targetInstances = instances.filter(
      (instance) => instance.namesTarget,
    ).length;
    builder.observe(ruleIdentity, "alerts", source, {
      tier: evidenceTierForRelevance(
        active ? "supporting" : "context",
        relevance,
      ),
      relevance,
      tone:
        state === "firing"
          ? severity === "critical"
            ? "error"
            : "alert"
          : state === "pending"
            ? "warning"
            : "neutral",
      title: `${rule.name}${state ? ` ${state}` : ""}`,
      summary:
        instances.length === 0
          ? `No active instances · ${rule.group}`
          : `${instances.length} active instance${instances.length === 1 ? "" : "s"}${
              targetInstances > 0
                ? `, ${targetInstances} naming ${identity}`
                : ""
            } · ${rule.group}`,
      data: { type: "alerts", rule, instances, annotations },
    });
    const health = (rule.health ?? "").toLowerCase();
    if (health === "err") {
      healthGap = true;
      builder.limit(
        source,
        `Alert rule ${rule.name}`,
        "Prometheus reported an evaluation error for this rule, so its state may be stale or missing.",
        "error",
      );
    } else if (health !== "ok") {
      // Anything but a reported "ok" evaluation, including an unrecognized
      // value, is an unevaluated rule for the purpose of a negative.
      healthGap = true;
      builder.limit(
        source,
        `Alert rule ${rule.name}`,
        "Prometheus has not reported a successful evaluation for this rule, so its state is unknown.",
        "unknown",
      );
    }
  }
  // A negative is only as strong as the query: an active-state filter over
  // every alerting rule (a name or group filter never looked at the rest), a
  // complete list, every rule evaluated, and every returned instance placed
  // in another namespace outright. An instance this projection merely fails
  // to recognize is not evidence of absence.
  if (
    source.confirmedSuccess &&
    !truncated &&
    !namesTarget &&
    !healthGap &&
    typeFilter !== "record" &&
    !nonEmptyString(args?.name) &&
    !nonEmptyString(args?.group) &&
    (stateFilter === "firing" || stateFilter === "pending") &&
    (alertingCount === 0 || everyInstanceElsewhere)
  ) {
    const filter = `state=${stateFilter}`;
    builder.observe(`alerts:receipt:${filter}`, "receipt", source, {
      tier: "checked",
      relevance: "target",
      tone: "neutral",
      title: `No ${stateFilter} alert rules name this ${displayKind(builder.target.kind)}`,
      summary: identity,
      data: {
        type: "receipt",
        checked: "alerts",
        scope: identity,
        message:
          alertingCount === 0
            ? `0 alerting rules returned (filter: ${filter}).`
            : `${alertingCount} alerting rule${alertingCount === 1 ? "" : "s"} returned (filter: ${filter}), every instance in another namespace; none names ${identity}.`,
      },
    });
  }
}

function helmOwnedResource(
  value: unknown,
): InvestigationHelmOwnedResource | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.kind) ||
    !nonEmptyString(candidate.name) ||
    typeof candidate.namespace !== "string"
  ) {
    return undefined;
  }
  for (const field of [
    "apiVersion",
    "status",
    "ready",
    "message",
    "summary",
    "issue",
  ] as const) {
    if (candidate[field] !== undefined && typeof candidate[field] !== "string")
      return undefined;
  }
  return candidate as unknown as InvestigationHelmOwnedResource;
}

function helmOperation(value: unknown): InvestigationHelmOperation | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.kind) ||
    !nonEmptyString(candidate.status) ||
    typeof candidate.message !== "string"
  ) {
    return undefined;
  }
  for (const field of [
    "revision",
    "failedRevision",
    "rollbackRevision",
  ] as const) {
    if (candidate[field] !== undefined && typeof candidate[field] !== "number")
      return undefined;
  }
  if (candidate.updated !== undefined && typeof candidate.updated !== "string")
    return undefined;
  return candidate as unknown as InvestigationHelmOperation;
}

function adaptHelmRelease(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  if (
    !value ||
    !nonEmptyString(value.name) ||
    !nonEmptyString(value.namespace) ||
    typeof value.chart !== "string" ||
    typeof value.chartVersion !== "string" ||
    !nonEmptyString(value.status) ||
    typeof value.revision !== "number" ||
    typeof value.updated !== "string"
  ) {
    invalidPayload(builder, source);
    return;
  }
  for (const field of [
    "appVersion",
    "description",
    "storageNamespace",
    "resourceHealth",
    "healthIssue",
    "healthSummary",
    "managedByFluxHelmRelease",
  ] as const) {
    if (value[field] !== undefined && typeof value[field] !== "string") {
      invalidPayload(builder, source);
      return;
    }
  }
  const resourcesRaw =
    value.resources === undefined || value.resources === null
      ? []
      : value.resources;
  if (!Array.isArray(resourcesRaw)) {
    invalidPayload(builder, source, "Helm resources");
    return;
  }
  const resources = resourcesRaw
    .map(helmOwnedResource)
    .filter((item): item is InvestigationHelmOwnedResource => Boolean(item));
  if (resources.length !== resourcesRaw.length) {
    invalidPayload(builder, source, "Helm resources");
    return;
  }
  const lastOperation =
    value.lastOperation === undefined
      ? undefined
      : helmOperation(value.lastOperation);
  if (value.lastOperation !== undefined && !lastOperation) {
    invalidPayload(builder, source, "Helm operation");
    return;
  }
  const release: InvestigationHelmRelease = {
    name: value.name,
    namespace: value.namespace,
    chart: value.chart,
    chartVersion: value.chartVersion,
    appVersion: value.appVersion as string | undefined,
    status: value.status,
    revision: value.revision,
    updated: value.updated,
    description: value.description as string | undefined,
    ...(nonEmptyString(value.storageNamespace)
      ? { storageNamespace: value.storageNamespace }
      : {}),
    resourceHealth: value.resourceHealth as string | undefined,
    healthIssue: value.healthIssue as string | undefined,
    healthSummary: value.healthSummary as string | undefined,
    managedByFluxHelmRelease: value.managedByFluxHelmRelease as
      string | undefined,
    lastOperation,
    resources,
  };
  // The release manages the target when the target is among the resources
  // Helm rendered for it; a release is never the investigated object itself.
  // Without an API version the owned row cannot tell colliding kinds apart
  // (CNPG vs CAPI Cluster), so it establishes nothing.
  const relevance: InvestigationEvidenceRelevance = resources.some(
    (owned) =>
      owned.apiVersion !== undefined &&
      resourceMatchesTarget(builder.target, {
        kind: owned.kind,
        group: apiVersionToGroup(owned.apiVersion),
        namespace: owned.namespace || undefined,
        name: owned.name,
      }),
  )
    ? "producer-related"
    : "broader";
  const status = release.status.toLowerCase();
  const operationFailed =
    lastOperation?.status === "failed" ||
    lastOperation?.status === "stuck_pending";
  const adverse =
    status !== "deployed" || Boolean(release.healthIssue) || operationFailed;
  const chartLabel = `${release.chart}${release.chartVersion ? ` ${release.chartVersion}` : ""}`;
  // Helm keys a release by where its metadata is stored; two releases with
  // one name and namespace can live in different storage namespaces.
  const storage = release.storageNamespace ?? release.namespace;
  builder.observe(
    `helm:${storage}:${release.namespace}:${release.name}`,
    "helm",
    source,
    {
      tier: evidenceTierForRelevance(
        adverse ? "supporting" : "context",
        relevance,
      ),
      relevance,
      tone:
        status.includes("failed") || lastOperation?.status === "failed"
          ? "error"
          : adverse
            ? "warning"
            : "info",
      title: `Helm release ${release.namespace}/${release.name}`,
      summary: release.healthIssue
        ? `${release.status} · ${release.healthIssue}`
        : `${chartLabel} · ${release.status} · revision ${release.revision}`,
      data: { type: "helm", release },
    },
  );
  // Every comparison the producer attempted and could not finish, not just the
  // values read. Four of these are Cloud-role denials, so dropping any of them
  // lets a reader see a release card whose comparison was withheld and take it
  // for complete. The labels follow the producer's own fields: `diff` is the
  // manifest, `valuesDiff` the values — naming one for the other sends a reader
  // to the wrong thing.
  for (const [field, label] of [
    ["valuesError", "Helm values"],
    ["valuesDiffError", "Helm values diff"],
    ["diffError", "Helm manifest diff"],
    ["notesDiffError", "Helm notes diff"],
    ["resourceDiffError", "Helm resource diff"],
  ] as const) {
    const message = value[field];
    if (nonEmptyString(message)) {
      builder.limit(source, label, message, "error");
    }
  }
}

function permissionSubject(
  value: unknown,
): InvestigationPermissionSubject | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.kind) ||
    !nonEmptyString(candidate.name) ||
    (candidate.namespace !== undefined &&
      typeof candidate.namespace !== "string")
  ) {
    return undefined;
  }
  return {
    kind: candidate.kind,
    name: candidate.name,
    ...(nonEmptyString(candidate.namespace)
      ? { namespace: candidate.namespace }
      : {}),
  };
}

function accessCheck(value: unknown): InvestigationAccessCheck | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.verb) ||
    !nonEmptyString(candidate.resource) ||
    typeof candidate.namespace !== "string" ||
    typeof candidate.allowed !== "boolean" ||
    (candidate.denied !== undefined && typeof candidate.denied !== "boolean")
  ) {
    return undefined;
  }
  for (const field of [
    "group",
    "subresource",
    "resourceName",
    "reason",
    "evaluationError",
  ] as const) {
    if (candidate[field] !== undefined && typeof candidate[field] !== "string")
      return undefined;
  }
  return {
    ...(candidate as unknown as InvestigationAccessCheck),
    denied: candidate.denied === true,
  };
}

function permissionBinding(
  value: unknown,
): InvestigationPermissionBinding | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.bindingKind) ||
    !nonEmptyString(candidate.bindingName) ||
    !nonEmptyString(candidate.roleKind) ||
    !nonEmptyString(candidate.roleName) ||
    !nonNegativeInteger(candidate.rulesCount)
  ) {
    return undefined;
  }
  for (const field of [
    "bindingNamespace",
    "roleNamespace",
    "inheritedFromGroup",
  ] as const) {
    if (candidate[field] !== undefined && typeof candidate[field] !== "string")
      return undefined;
  }
  return candidate as unknown as InvestigationPermissionBinding;
}

// Kinds whose spec carries a PodSpec, by API group. Anything else with a
// `spec.template.spec` (an ApplicationSet, for one) templates something that
// is not a Pod and runs as no account at all.
const POD_TEMPLATE_KINDS: Readonly<Record<string, readonly string[]>> = {
  "": ["Pod"],
  apps: ["Deployment", "StatefulSet", "DaemonSet", "ReplicaSet"],
  batch: ["Job", "CronJob"],
  "argoproj.io": ["Rollout"],
};

function podSpecServiceAccount(
  resource: InvestigationKubernetesResource,
): string | undefined {
  const group = apiVersionToGroup(resource.apiVersion);
  if (!POD_TEMPLATE_KINDS[group]?.includes(resource.kind)) return undefined;
  const spec = record(resource.spec);
  const podSpec =
    resource.kind === "Pod"
      ? spec
      : resource.kind === "CronJob"
        ? record(
            record(record(record(spec?.jobTemplate)?.spec)?.template)?.spec,
          )
        : record(record(spec?.template)?.spec);
  if (!podSpec) return undefined;
  // An unset serviceAccountName runs as the namespace's `default` account.
  return nonEmptyString(podSpec.serviceAccountName)
    ? podSpec.serviceAccountName
    : "default";
}

/**
 * The ServiceAccount the target runs as, read from a resource observation of
 * the target in the same turn. Only the captured resource can state this; the
 * permission subject is never assumed related by name alone.
 */
function targetServiceAccountName(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
): string | undefined {
  let newest: InvestigationEvidenceObservation | undefined;
  for (const group of builder.groups) {
    if (group.kind !== "resource") continue;
    for (const observation of group.observations) {
      if (
        observation.source.turnIndex !== source.turnIndex ||
        observation.relevance !== "target" ||
        observation.data.type !== "resource" ||
        (newest && observation.source.order < newest.source.order)
      ) {
        continue;
      }
      newest = observation;
    }
  }
  if (!newest || newest.data.type !== "resource") return undefined;
  return (
    newest.data.resourceContext?.uses?.serviceAccount?.name ??
    podSpecServiceAccount(newest.data.resource)
  );
}

function adaptSubjectPermissions(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  const subject = value ? permissionSubject(value.subject) : undefined;
  if (!value || !subject) {
    invalidPayload(builder, source);
    return;
  }
  // Only a captured resource can tie a principal to the target. Any other
  // subject the agent chose to check is context, however adverse the answer.
  const relevance: InvestigationEvidenceRelevance =
    subject.kind === "ServiceAccount" &&
    subject.namespace !== undefined &&
    subject.namespace === builder.target.namespace &&
    targetServiceAccountName(builder, source) === subject.name
      ? "target"
      : "broader";
  const subjectLabel = `${displayKind(subject.kind)} ${subject.namespace ? `${subject.namespace}/` : ""}${subject.name}`;
  const subjectKey = `${subject.kind}:${subject.namespace ?? ""}:${subject.name}`;

  if (value.accessCheck !== undefined) {
    const check = accessCheck(value.accessCheck);
    if (!check) {
      invalidPayload(builder, source, "Access check");
      return;
    }
    const resourceLabel = `${check.resource}${check.subresource ? `/${check.subresource}` : ""}${check.group ? `.${check.group}` : ""}`;
    // An authorizer that could not decide has established neither answer. The
    // evaluation error already reaches the coverage strip below, but a card
    // reading "cannot verb resource" is the part an operator acts on, and a
    // webhook returning partial data is not a denial.
    // Only when the authorizer decided nothing. Kubernetes returns this error
    // alongside a real verdict too — one webhook failing while another allows —
    // and rewriting a decided allow or deny as "could not check" loses the
    // answer and puts an allow in the warning list.
    const unresolved =
      nonEmptyString(check.evaluationError) && !check.allowed && !check.denied;
    const denied = !check.allowed && !unresolved;
    const verdict = unresolved
      ? `Could not be evaluated: ${check.evaluationError}`
      : check.reason ||
        (check.allowed
          ? "Allowed by RBAC"
          : check.denied
            ? "Explicitly denied"
            : "No RBAC rule allows it");
    builder.observe(
      `permissions:check:${subjectKey}:${check.verb}:${check.group ?? ""}:${check.resource}:${check.subresource ?? ""}:${check.namespace}:${check.resourceName ?? ""}`,
      "permissions",
      source,
      {
        tier: evidenceTierForRelevance(
          denied || unresolved ? "supporting" : "context",
          relevance,
        ),
        relevance,
        tone: denied || unresolved ? "warning" : "info",
        title: unresolved
          ? `Could not check whether ${subjectLabel} can ${check.verb} ${resourceLabel}`
          : `${subjectLabel} ${denied ? "cannot" : "can"} ${check.verb} ${resourceLabel}`,
        summary: `${verdict} · ${check.namespace ? `namespace ${check.namespace}` : "cluster-wide"}${check.resourceName ? ` · ${check.resourceName}` : ""}`,
        data: { type: "permissions", subject, accessCheck: check },
      },
    );
    if (nonEmptyString(check.evaluationError)) {
      builder.limit(source, "Permissions", check.evaluationError, "error");
    }
    return;
  }

  const bindingsRaw = value.bindings;
  if (!Array.isArray(bindingsRaw)) {
    invalidPayload(builder, source);
    return;
  }
  const bindings = bindingsRaw
    .map(permissionBinding)
    .filter((item): item is InvestigationPermissionBinding => Boolean(item));
  if (
    bindings.length !== bindingsRaw.length ||
    !Array.isArray(value.flatRules) ||
    !value.flatRules.every((rule) => stringArray(record(rule)?.verbs)) ||
    (value.truncated !== undefined && typeof value.truncated !== "boolean") ||
    (value.podsTotal !== undefined && !nonNegativeInteger(value.podsTotal)) ||
    (value.usedByPods !== undefined && !stringArray(value.usedByPods))
  ) {
    invalidPayload(builder, source);
    return;
  }
  const truncated = value.truncated === true;
  const flatRulesCount = value.flatRules.length;
  const usedByPods = stringArray(value.usedByPods) ?? [];
  builder.observe(`permissions:subject:${subjectKey}`, "permissions", source, {
    tier: evidenceTierForRelevance("context", relevance),
    relevance,
    tone: "info",
    title: `Permissions of ${subjectLabel}`,
    summary: `${bindings.length} binding${bindings.length === 1 ? "" : "s"} · ${flatRulesCount}${truncated ? "+" : ""} effective rule${flatRulesCount === 1 && !truncated ? "" : "s"}`,
    data: {
      type: "permissions",
      subject,
      bindings,
      flatRulesCount,
      truncated,
      usedByPods,
      podsTotal: value.podsTotal as number | undefined,
    },
  });
  if (truncated) {
    builder.limit(
      source,
      "Permissions",
      nonEmptyString(value.narrowHint)
        ? value.narrowHint
        : "The effective rule list was truncated; it is not the subject's complete permission set.",
      "truncated",
    );
  }
  if (nonEmptyString(value.subjectWarning)) {
    builder.limit(source, "Permissions", value.subjectWarning, "unknown");
  }
  if (
    typeof value.podsTotal === "number" &&
    value.podsTotal > usedByPods.length
  ) {
    builder.limit(
      source,
      "Permissions",
      `Only ${usedByPods.length} of ${value.podsTotal} pods running as this subject were listed.`,
      "truncated",
    );
  }
}

function timeSeries(value: unknown): TimeSeries | undefined {
  const item = record(value);
  if (!item || !Array.isArray(item.dataPoints)) return undefined;
  const labels = record(item.labels) ?? {};
  if (Object.values(labels).some((label) => typeof label !== "string")) {
    return undefined;
  }
  const dataPoints: TimeSeries["dataPoints"] = [];
  for (const raw of item.dataPoints) {
    const point = record(raw);
    if (!point || typeof point.timestamp !== "number") return undefined;
    if (
      point.value !== undefined &&
      point.value !== null &&
      typeof point.value !== "number"
    ) {
      return undefined;
    }
    dataPoints.push({
      timestamp: point.timestamp,
      value: typeof point.value === "number" ? point.value : null,
    });
  }
  return { labels: labels as Record<string, string>, dataPoints };
}

function metricsSelector(
  value: unknown,
): InvestigationMetricsSelector | undefined {
  const item = record(value);
  if (!item || typeof item.metric !== "string" || !Array.isArray(item.matchers))
    return undefined;
  const matchers: InvestigationMetricsSelector["matchers"] = [];
  for (const raw of item.matchers) {
    const matcher = record(raw);
    if (
      !matcher ||
      typeof matcher.label !== "string" ||
      typeof matcher.op !== "string" ||
      typeof matcher.value !== "string"
    ) {
      return undefined;
    }
    matchers.push({
      label: matcher.label,
      op: matcher.op,
      value: matcher.value,
    });
  }
  return { metric: item.metric, matchers };
}

const WORKLOAD_SELECTOR_LABELS: Readonly<Record<string, string | undefined>> =
  {
    deployment: "deployment",
    statefulset: "statefulset",
    daemonset: "daemonset",
    workload: undefined,
  };

/**
 * A regex matcher names the target only in the exact forms the plan admits,
 * with the name's regex metacharacters (a dot in a Kubernetes name) escaped
 * or absent. "api-?.*" also selects "apiworker-…" and an unescaped "api.v2"
 * also selects "api-v2", so neither may claim the target.
 */
function regexNamesExactly(
  value: string,
  name: string,
  suffix: string,
): boolean {
  const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return value === `${escaped}${suffix}`;
}

function selectorNamesTarget(
  target: InvestigationEvidenceTarget,
  matcher: InvestigationMetricsSelector["matchers"][number],
): boolean {
  const { label, op, value } = matcher;
  if (label === "pod") {
    // A Pod target is named only by its own exact name: its "<name>-" prefix
    // selects other pods, never itself.
    if (target.kind.toLowerCase() === "pod") {
      if (op === "=") return value === target.name;
      if (op === "=~") return regexNamesExactly(value, target.name, "");
      return false;
    }
    if (op === "=~") return regexNamesExactly(value, target.name, "-.*");
    if (op === "=") return value.startsWith(`${target.name}-`);
    return false;
  }
  if (!(label in WORKLOAD_SELECTOR_LABELS)) return false;
  const requiredKind = WORKLOAD_SELECTOR_LABELS[label];
  if (requiredKind && target.kind.toLowerCase() !== requiredKind) return false;
  if (op === "=") return value === target.name;
  if (op === "=~") return regexNamesExactly(value, target.name, "");
  return false;
}

/**
 * A metrics result is about the target only when every selector says so:
 * the target namespace plus a pod or workload matcher naming it. A query with
 * any bare or foreign selector (a cluster denominator, a sibling workload, an
 * alternation) could be about anything, so it stays broader.
 */
export function metricsScope(
  target: InvestigationEvidenceTarget,
  selectors: readonly InvestigationMetricsSelector[],
  selectorsUnknown: boolean,
): InvestigationEvidenceRelevance {
  if (selectorsUnknown || selectors.length === 0 || !target.namespace) {
    return "broader";
  }
  let allNameTarget = true;
  for (const selector of selectors) {
    // Prometheus anchors regex matchers, so namespace=~"shop" is exact too.
    const inNamespace = selector.matchers.some(
      (matcher) =>
        matcher.label === "namespace" &&
        ((matcher.op === "=" && matcher.value === target.namespace) ||
          (matcher.op === "=~" &&
            regexNamesExactly(matcher.value, target.namespace ?? "", ""))),
    );
    if (!inNamespace) return "broader";
    if (!selector.matchers.some((matcher) => selectorNamesTarget(target, matcher)))
      allNameTarget = false;
  }
  return allNameTarget ? "target" : "producer-related";
}

function metricsWindowLabel(data: {
  mode: "range" | "instant";
  start?: string;
  end?: string;
  step?: string;
}): string | undefined {
  if (data.mode !== "range" || !data.start || !data.end) return undefined;
  const startMs = Date.parse(data.start);
  const endMs = Date.parse(data.end);
  if (!Number.isFinite(startMs) || !Number.isFinite(endMs) || endMs <= startMs)
    return undefined;
  const minutes = Math.round((endMs - startMs) / 60_000);
  const window =
    minutes >= 60 * 24 * 2
      ? `${Math.round(minutes / (60 * 24))}d`
      : minutes >= 120
        ? `${Math.round(minutes / 60)}h`
        : `${minutes}m`;
  return data.step ? `${window} window · ${data.step} step` : `${window} window`;
}

function adaptQueryPrometheus(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  const mode = value?.type;
  if (
    !value ||
    !nonEmptyString(value.query) ||
    (mode !== "range" && mode !== "instant") ||
    !Array.isArray(value.series) ||
    !Array.isArray(value.selectors) ||
    (value.selectorsUnknown !== undefined &&
      typeof value.selectorsUnknown !== "boolean") ||
    (value.truncated !== undefined && typeof value.truncated !== "boolean")
  ) {
    invalidPayload(builder, source, "Prometheus query");
    return;
  }
  const series = value.series
    .map(timeSeries)
    .filter((item): item is TimeSeries => Boolean(item));
  const selectors = value.selectors
    .map(metricsSelector)
    .filter((item): item is InvestigationMetricsSelector => Boolean(item));
  if (
    series.length !== value.series.length ||
    selectors.length !== value.selectors.length
  ) {
    invalidPayload(builder, source, "Prometheus query");
    return;
  }
  const selectorsUnknown = value.selectorsUnknown === true;
  const relevance = metricsScope(builder.target, selectors, selectorsUnknown);
  const start = nonEmptyString(value.start) ? value.start : undefined;
  const end = nonEmptyString(value.end) ? value.end : undefined;
  const step = nonEmptyString(value.step) ? value.step : undefined;
  const note = nonEmptyString(value.note) ? value.note : undefined;
  if (value.truncated === true) {
    const summary = record(value.summary);
    const cardinality = record(summary?.labelCardinality);
    const widest = cardinality
      ? Object.entries(cardinality).find(
          ([, count]) => typeof count === "number",
        )
      : undefined;
    const parts = [
      typeof summary?.seriesCount === "number"
        ? `${summary.seriesCount} series`
        : undefined,
      typeof summary?.totalDataPoints === "number"
        ? `${summary.totalDataPoints} samples`
        : undefined,
      widest ? `${widest[1]} distinct ${widest[0]} values` : undefined,
    ].filter((part): part is string => Boolean(part));
    builder.limit(
      source,
      "Prometheus query",
      `The result of \`${value.query}\` was too large to keep${parts.length ? ` (${parts.join(", ")})` : ""}, so Radar cannot chart it.${note ? ` ${note}` : ""}`,
      "truncated",
    );
    return;
  }
  const windowLabel = metricsWindowLabel({ mode, start, end, step });
  const data: InvestigationMetricsEvidence = {
    type: "metrics",
    origin: "query",
    query: value.query,
    mode,
    start,
    end,
    step,
    unit: metricsUnitForExpression(value.query, selectors),
    series,
    truncated: false,
    note,
    selectors,
    selectorsUnknown,
    subject:
      relevance === "target"
        ? {
            kind: builder.target.kind,
            ...(builder.target.group ? { group: builder.target.group } : {}),
            namespace: builder.target.namespace,
            name: builder.target.name,
          }
        : undefined,
  };
  builder.observe(
    `metrics:${mode}:${value.query}:${start ?? ""}:${end ?? ""}:${step ?? ""}`,
    "metrics",
    source,
    {
      tier: evidenceTierForRelevance("supporting", relevance),
      relevance,
      tone: "neutral",
      title: mode === "range" ? "Prometheus metrics" : "Prometheus values",
      summary: [
        series.length === 0
          ? "No series matched"
          : `${series.length} series`,
        windowLabel,
      ]
        .filter((part): part is string => Boolean(part))
        .join(" · "),
      data,
    },
  );
}

const ADAPTERS: Record<
  string,
  (
    builder: ProjectionBuilder,
    source: InvestigationEvidenceSource,
    payload: unknown,
  ) => void
> = {
  diagnose: adaptDiagnose,
  issues: adaptIssues,
  get_resource: adaptGetResource,
  list_resources: adaptListResources,
  get_events: adaptEvents,
  get_pod_logs: adaptPodLogs,
  get_changes: adaptChanges,
  get_neighborhood: adaptNeighborhood,
  get_topology: adaptTopology,
  get_workload_logs: adaptWorkloadLogs,
  get_prometheus_rules: adaptPrometheusRules,
  get_helm_release: adaptHelmRelease,
  get_subject_permissions: adaptSubjectPermissions,
  query_prometheus: adaptQueryPrometheus,
};

export function projectInvestigationEvidence(
  turns: readonly InvestigationEvidenceTurn[],
  target: InvestigationEvidenceTarget,
): InvestigationEvidenceProjection {
  const builder = new ProjectionBuilder(target);
  const evidenceRefSources: InvestigationEvidenceSource[] = [];
  const citableSources: InvestigationEvidenceSource[] = [];
  let order = 0;
  for (const [turnIndex, turn] of turns.entries()) {
    for (const [timelineIndex, item] of turn.timeline.entries()) {
      const itemOrder = order;
      order += 1;
      if (item.kind !== "tool") continue;
      // Full-local agents may load user MCP servers whose bare tool names collide
      // with Radar's. Only results matched by the server to the active private
      // transport ledger may enter the surface labelled "Radar evidence".
      if (item.radarEvidence !== true) continue;
      const source: InvestigationEvidenceSource = {
        id: investigationEvidenceSourceId(turnIndex, item.id),
        turnIndex,
        timelineIndex,
        stepId: item.id,
        tool: item.tool,
        args: item.summary,
        order: itemOrder,
        phase: turn.verify
          ? "verification"
          : turn.apply
            ? "apply"
            : turn.question
              ? "followup"
              : "initial",
        confirmedSuccess: item.status === "done" && item.isError === false,
        evidenceRef: item.evidenceRef,
      };
      if (item.evidenceRef) evidenceRefSources.push(source);
      if (item.status !== "done") continue;
      if (
        item.evidenceRef &&
        investigationEvidenceRefRe.test(item.evidenceRef) &&
        item.isError === false &&
        !item.truncated &&
        nonEmptyString(item.result)
      ) {
        citableSources.push(source);
      }
      const adapt = ADAPTERS[item.tool];
      if (!adapt) continue;
      builder.addSource(source);
      if (item.isError === true) {
        builder.limit(
          source,
          investigationResultLabel(source),
          item.result || "This investigation step failed.",
          "error",
        );
        continue;
      }
      if (item.truncated) {
        builder.limit(
          source,
          investigationResultLabel(source),
          "Only part of this investigation result was saved, so Radar could not summarize it here.",
          "truncated",
        );
        continue;
      }
      if (!nonEmptyString(item.result)) {
        builder.limit(
          source,
          investigationResultLabel(source),
          "This investigation step did not return details Radar could summarize.",
          "unknown",
        );
        continue;
      }
      const payload = parseJSON(item.result);
      if (payload === undefined) {
        invalidPayload(builder, source);
        continue;
      }
      adapt(builder, source, payload);
      if (item.isError !== false) {
        builder.limit(
          source,
          investigationResultLabel(source),
          "Radar cannot confirm whether this investigation step completed successfully. Available evidence is shown, but an empty result cannot confirm that nothing was found.",
          "unknown",
        );
      }
    }
  }

  const tierRank: Record<InvestigationEvidenceTier, number> = {
    key: 0,
    supporting: 1,
    context: 2,
    checked: 3,
  };
  const primaryBySource = new Map<
    string,
    { groupId: string; rank: number; order: number }
  >();
  for (const group of builder.groups) {
    for (const observation of group.observations) {
      const candidate = {
        groupId: group.id,
        rank: tierRank[observation.tier],
        order: group.firstOrder,
      };
      const current = primaryBySource.get(observation.source.id);
      if (
        !current ||
        candidate.rank < current.rank ||
        (candidate.rank === current.rank && candidate.order < current.order)
      ) {
        primaryBySource.set(observation.source.id, candidate);
      }
    }
  }
  for (const source of builder.sources) {
    source.primaryGroupId = primaryBySource.get(source.id)?.groupId;
  }

  // The investigation target, rather than the producer tool, is the proof
  // boundary for semantic diagnosis domains. A later successful target
  // diagnosis can therefore retire an exact-target issue first observed by
  // `issues`, while a broad or sibling read still cannot clear it.
  const targetProofScope = [
    builder.target.group.toLowerCase(),
    builder.target.kind.toLowerCase(),
    builder.target.namespace ?? "",
    builder.target.name,
  ].join("/");
  const semanticCoverageKey = (kind: InvestigationEvidenceKind) =>
    `semantic:${kind}:${targetProofScope}`;
  const collectionCoverageKey = (
    kind: InvestigationEvidenceKind,
    identity: string,
  ) => `collection:${kind}:${identity}`;
  const previousLogCoverageKey = (
    source: InvestigationEvidenceSource,
    podContainer: string,
  ) => `previous-log:${source.tool}:${scopeFromArgs(source)}:${podContainer}`;
  const retirementKey = (
    group: InvestigationEvidenceGroup,
    observation: InvestigationEvidenceObservation,
  ): string | undefined => {
    switch (group.kind) {
      case "issue":
      case "startup":
      case "crash":
      case "dns":
        return semanticCoverageKey(group.kind);
      case "events":
      case "changes":
        return collectionCoverageKey(group.kind, group.identity);
      case "logs":
        return group.identity.startsWith("logs:previous:")
          ? previousLogCoverageKey(
              observation.source,
              group.identity.slice("logs:previous:".length),
            )
          : undefined;
      case "network":
        return collectionCoverageKey(group.kind, group.identity);
      default:
        return undefined;
    }
  };

  // Each completed verification contributes only the exact proof scopes its
  // successful producers covered. Keep every verification: supersession is
  // monotonic until a newer relevant observation reopens that semantic item.
  const verificationCoverage: Array<{
    turnIndex: number;
    keys: Set<string>;
  }> = [];
  turns.forEach((turn, turnIndex) => {
    if (!turn.verify || turn.status !== "done") return;
    const keys = new Set<string>();
    for (const group of builder.groups) {
      for (const observation of group.observations) {
        if (
          observation.source.turnIndex !== turnIndex ||
          !observation.source.confirmedSuccess ||
          observation.relevance === "broader"
        ) {
          continue;
        }
        if (observation.data.type === "receipt") {
          switch (observation.data.checked) {
            case "issues":
              keys.add(semanticCoverageKey("issue"));
              break;
            case "events":
              keys.add(collectionCoverageKey("events", group.identity));
              break;
            case "changes":
              keys.add(collectionCoverageKey("changes", group.identity));
              break;
            case "logs":
              if (group.identity.startsWith("previous-log-absence:")) {
                keys.add(
                  previousLogCoverageKey(
                    observation.source,
                    group.identity.slice("previous-log-absence:".length),
                  ),
                );
              }
              break;
            case "inventory":
            case "alerts":
              break;
          }
          continue;
        }
        if (
          observation.source.tool === "diagnose" &&
          observation.data.type === "resource"
        ) {
          for (const kind of builder.semanticCoverageBySource.get(
            observation.source.id,
          ) ?? []) {
            keys.add(semanticCoverageKey(kind));
          }
        }
        if (
          observation.source.tool === "diagnose" &&
          group.kind === "network"
        ) {
          keys.add(collectionCoverageKey("network", group.identity));
        }
      }
    }
    verificationCoverage.push({ turnIndex, keys });
  });

  for (const group of builder.groups) {
    for (const observation of group.observations) {
      const key =
        observation.relevance !== "broader"
          ? retirementKey(group, observation)
          : undefined;
      observation.historical = Boolean(
        key &&
        verificationCoverage.some(
          (verification) =>
            verification.turnIndex > observation.source.turnIndex &&
            verification.keys.has(key),
        ),
      );
    }
    const latestRelevantObservation = [...group.observations]
      .reverse()
      .find((observation) => observation.relevance !== "broader");
    group.historical = latestRelevantObservation?.historical ?? false;
  }

  return {
    groups: builder.groups,
    limitations: builder.limitations,
    sources: builder.sources,
    evidenceRefSources,
    citableSources,
    coverage: {
      attempted: builder.sources.length,
      projected: builder.projectedSources.size,
      limited: builder.limitedSources.size,
      checked: builder.checkedSources.size,
    },
  };
}
