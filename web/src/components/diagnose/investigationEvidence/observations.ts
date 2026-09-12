import {
  type HPADiagnosisState,
  type Issue,
  type IssueRecentChange,
  type Topology,
  defaultConditionTone,
  displayKind,
  hpaStateLabel,
  hpaStateLevel,
  stripAnsi,
} from "@skyhook-io/k8s-ui";
import {
  type DiagnosisChangeContext,
  type DiagnosisEvidenceLimitationBase,
  type DiagnosisPodLogEntry,
  diagnosisSeverityTone,
} from "../diagnoseEvidenceTypes";
import { investigationResourceEvidenceSummary } from "../investigationResourceEvidenceModel";
import { stableHash } from "./identity";
import { nonEmptyString, parseJSON, record, stringArray } from "./parse";
import {
  type InvestigationEventEvidence,
  type InvestigationEvidenceGroup,
  type InvestigationEvidenceKind,
  type InvestigationEvidenceLimitation,
  type InvestigationEvidenceObservation,
  type InvestigationEvidenceRelevance,
  type InvestigationEvidenceSource,
  type InvestigationEvidenceTarget,
  type InvestigationEvidenceTier,
  type InvestigationGitOpsDiagnosis,
  type InvestigationKubernetesResource,
  type InvestigationResourceContext,
  type InvestigationSemanticDomain,
} from "./types";

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
export function topologyPartiality(
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
export function addTopologyLimitations(
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
export function scopeFromArgs(source: InvestigationEvidenceSource): string {
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
export function investigationResultLabel(
  source: InvestigationEvidenceSource,
): string {
  return INVESTIGATION_RESULT_LABELS[source.tool] ?? "Investigation result";
}
export function resourceMatchesTarget(
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
export function relevanceForResource(
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
export function sourceArgsRelevance(
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
export function evidenceTierForRelevance(
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
export function isDiagnosableWorkloadKind(kind: string): boolean {
  return DIAGNOSABLE_WORKLOAD_KINDS.has(kind.toLowerCase());
}
export function previousFromArgs(source: InvestigationEvidenceSource): boolean {
  return (
    record(source.args ? parseJSON(source.args) : undefined)?.previous === true
  );
}
export class ProjectionBuilder {
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

  /**
   * Pods a target-scoped diagnose bundle listed as the workload's own, by
   * controller ownership. Membership for an agent's pod-level Prometheus
   * query is proved against this set, never inferred from a name.
   */
  readonly establishedTargetPods = new Set<string>();

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
export function contextFrom(
  value: unknown,
): InvestigationResourceContext | undefined {
  const candidate = record(value);
  if (!candidate || !nonEmptyString(candidate.tier)) return undefined;
  return candidate as unknown as InvestigationResourceContext;
}
export function addNarrowHint(
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
        " returned part of the matching results to keep this investigation fast. More may exist.",
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
      `Radar left out part of the resource context (${omitted.reason.replaceAll("_", " ")}).`,
      omitted.reason === "budget_exceeded" ? "truncated" : "unknown",
    );
  }
  if (context.referencedBy?.truncated) {
    const shown = context.referencedBy.items?.length ?? 0;
    builder.limit(
      source,
      "Relationships",
      `Radar returned ${shown} of ${context.referencedBy.total} referenced-by relationships.`,
      "truncated",
    );
  }
  if (context.appReferences?.staleSecretEnvTruncated) {
    builder.limit(
      source,
      "Application references",
      "Radar returned part of the stale Secret references.",
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
      "Radar returned part of the affected-resource list.",
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
  const adverseScaler = adverseScalerStates(context)[0];
  if (replicas?.desired !== undefined) {
    const desired = replicas.desired;
    const ready = replicas.ready ?? 0;
    const readiness = `${ready}/${desired} replicas ready`;
    return adverseScaler
      ? `${readiness} · HPA: ${hpaStateLabel(adverseScaler)}`
      : readiness;
  }
  if (adverseScaler) return `HPA: ${hpaStateLabel(adverseScaler)}`;
  if (context?.statusSummary?.phase) return context.statusSummary.phase;
  if (context?.issueSummary?.topReason) return context.issueSummary.topReason;
  return (
    investigationResourceEvidenceSummary(resource) ||
    warnings[0] ||
    resource.metadata.namespace
  );
}
// A scaler that cannot act (no metrics, cannot read the target) or is pinned
// at its ceiling is a captured Radar fact about the workload, so it lifts the
// card the way an adverse condition does. Ordinary scale-ups and scale-downs
// are the autoscaler working and stay as detail.
function adverseScalerStates(
  context: InvestigationResourceContext | undefined,
): HPADiagnosisState[] {
  return (context?.scaledBy ?? []).flatMap((scaler) => {
    const state = scaler.hpaSummary?.state;
    if (!state) return [];
    return hpaStateLevel(state) === "unhealthy" || state === "limited_max"
      ? [state]
      : [];
  });
}
export function addResourceObservation(
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
  const hasAdverseState =
    replicaShortfall ||
    adverseCondition ||
    gitOpsAdverse ||
    adverseScalerStates(context).length > 0;
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
export function addIssueObservation(
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
export function addEvents(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  values: InvestigationEventEvidence[],
  identity: string,
  complete = true,
  emptyIsAuthoritative = false,
  relevance: InvestigationEvidenceRelevance = "broader",
  emptyReceipt: { title: string; message?: string } = {
    title: "No warning events",
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
export function addChanges(
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
      title: "No recorded changes in this window",
      summary: scope,
      data: {
        type: "receipt",
        checked: "changes",
        scope,
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
export function changesSubjectFromArgs(
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
export function addLogs(
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
export function invalidPayload(
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
