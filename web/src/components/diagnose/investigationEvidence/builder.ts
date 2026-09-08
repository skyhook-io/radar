import {
  displayKind,
  stripAnsi,
  type Issue,
  type IssueRecentChange,
} from "@skyhook-io/k8s-ui";
import { fnv1a32 } from "@skyhook-io/k8s-ui/utils/structure-hash";
import type {
  DiagnosisEvidenceLimitationBase,
  DiagnosisFilteredLogs,
  DiagnosisPodLogEntry,
  DiagnosisResourceRef,
} from "../diagnoseEvidenceTypes";
import type {
  InvestigationEventEvidence,
  InvestigationEvidenceGroup,
  InvestigationEvidenceKind,
  InvestigationEvidenceLimitation,
  InvestigationEvidenceObservation,
  InvestigationEvidenceRelevance,
  InvestigationEvidenceSource,
  InvestigationEvidenceTarget,
  InvestigationEvidenceTier,
  InvestigationKubernetesResource,
  InvestigationResourceContext,
  InvestigationSemanticDomain,
} from "./types";

export function stableHash(value: string): string {
  // FNV-1a keeps long resource/log identities out of DOM IDs. The raw identity
  // remains the Map key, so a hash collision can never merge evidence.
  return fnv1a32(value).toString(36);
}

export function record(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

export function nonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0;
}

export function stringArray(value: unknown): string[] | undefined {
  return Array.isArray(value) && value.every((item) => typeof item === "string")
    ? value
    : undefined;
}

export function parseJSON(value: string): unknown {
  try {
    return JSON.parse(value);
  } catch {
    return undefined;
  }
}

export function kubernetesResource(
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

export function issue(value: unknown): Issue | undefined {
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

export function recentChange(value: unknown): IssueRecentChange | undefined {
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

export function event(value: unknown): InvestigationEventEvidence | undefined {
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

export function filteredLogs(
  value: unknown,
): DiagnosisFilteredLogs | undefined {
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

export function resourceRef(value: unknown): DiagnosisResourceRef | undefined {
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

export function parseLogEntry(
  value: unknown,
): DiagnosisPodLogEntry | undefined {
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

export function nonNegativeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0;
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
