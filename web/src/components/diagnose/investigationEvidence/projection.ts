import { CORE_RESOURCES } from "@skyhook-io/k8s-ui";
import { apiVersionToGroup } from "../../../utils/navigation";
import type { DiagnosisResourceRef } from "../diagnoseEvidenceTypes";
import type { RootCauseEvidence } from "../../../api/diagnose";
import type {
  InvestigationEvidenceData,
  InvestigationEvidenceGroup,
  InvestigationEvidenceKind,
  InvestigationEvidenceObservation,
  InvestigationEvidenceProjection,
  InvestigationEvidenceSource,
  InvestigationEvidenceTarget,
  InvestigationEvidenceTier,
  InvestigationEvidenceTurn,
  InvestigationRootCauseEvidenceLink,
  InvestigationRootCauseEvidenceResolution,
} from "./types";
import { adaptChanges } from "./adapters/changes";
import { adaptDiagnose } from "./adapters/diagnose";
import { adaptEvents } from "./adapters/events";
import { adaptHelmRelease } from "./adapters/helm";
import { adaptIssues } from "./adapters/issues";
import { adaptPodLogs, adaptWorkloadLogs } from "./adapters/logs";
import { adaptSubjectPermissions } from "./adapters/permissions";
import {
  adaptPrometheusRules,
  adaptQueryPrometheus,
} from "./adapters/prometheus";
import { adaptGetResource, adaptListResources } from "./adapters/resource";
import { adaptNeighborhood, adaptTopology } from "./adapters/topology";
import {
  ProjectionBuilder,
  invalidPayload,
  investigationResultLabel,
  kubernetesResource,
  nonEmptyString,
  parseJSON,
  record,
  relevanceForResource,
  scopeFromArgs,
} from "./builder";

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

export function isInvestigationEvidenceRef(value: string): boolean {
  return investigationEvidenceRefRe.test(value);
}

export function investigationSourceArgs(
  source: InvestigationEvidenceSource,
): Record<string, unknown> | undefined {
  return record(source.args ? parseJSON(source.args) : undefined);
}

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

/**
 * The pods every diagnose of the target listed as its own, gathered before
 * anything is classified. A metrics query is target evidence when it names
 * pods a producer established, and that must not depend on whether the agent
 * happened to run diagnose before or after the query: the same investigation
 * would otherwise read differently for the same facts.
 */
function collectEstablishedTargetPods(
  builder: ProjectionBuilder,
  turns: readonly InvestigationEvidenceTurn[],
): void {
  for (const turn of turns) {
    for (const item of turn.timeline) {
      if (
        item.kind !== "tool" ||
        item.tool !== "diagnose" ||
        item.radarEvidence !== true ||
        item.status !== "done" ||
        item.isError === true ||
        !nonEmptyString(item.result)
      ) {
        continue;
      }
      const value = record(parseJSON(item.result));
      const resource = kubernetesResource(value?.resource);
      if (!value || !resource || !Array.isArray(value.podNames)) continue;
      if (
        relevanceForResource(builder, {
          kind: resource.kind,
          group: apiVersionToGroup(resource.apiVersion),
          namespace: resource.metadata.namespace,
          name: resource.metadata.name,
        }) !== "target"
      ) {
        continue;
      }
      for (const pod of value.podNames) {
        if (nonEmptyString(pod)) builder.establishedTargetPods.add(pod);
      }
    }
  }
}

export function projectInvestigationEvidence(
  turns: readonly InvestigationEvidenceTurn[],
  target: InvestigationEvidenceTarget,
): InvestigationEvidenceProjection {
  const builder = new ProjectionBuilder(target);
  collectEstablishedTargetPods(builder, turns);
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
