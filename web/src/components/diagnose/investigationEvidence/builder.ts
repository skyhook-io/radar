import { apiVersionToGroup } from "../../../utils/navigation";
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
  investigationEvidenceRefRe,
  investigationEvidenceSourceId,
} from "./identity";
import {
  ProjectionBuilder,
  invalidPayload,
  investigationResultLabel,
  relevanceForResource,
  scopeFromArgs,
} from "./observations";
import { kubernetesResource, nonEmptyString, parseJSON, record } from "./parse";
import {
  type InvestigationEvidenceGroup,
  type InvestigationEvidenceKind,
  type InvestigationEvidenceObservation,
  type InvestigationEvidenceProjection,
  type InvestigationEvidenceSource,
  type InvestigationEvidenceTarget,
  type InvestigationEvidenceTier,
  type InvestigationEvidenceTurn,
} from "./types";

/**
 * Adapters whose verdict depends on which pods a producer established as the
 * target's. They run after every other tool in the transcript, so the answer
 * does not depend on whether the agent happened to query Prometheus before or
 * after the read that named the pods. Their source order is captured before
 * they are queued, so deferring the classification does not reorder evidence.
 */
const MEMBERSHIP_DEPENDENT_ADAPTERS = new Set([
  "query_prometheus",
  "get_prometheus_rules",
]);
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
        // Confirmed success, the same test the sources use. This set is what
        // proves a Prometheus selector is about the target, so a result that
        // never said it succeeded must not put pods into it.
        item.isError !== false ||
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
  const deferred: {
    adapt: (
      builder: ProjectionBuilder,
      source: InvestigationEvidenceSource,
      payload: unknown,
    ) => void;
    source: InvestigationEvidenceSource;
    payload: unknown;
  }[] = [];
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
      if (MEMBERSHIP_DEPENDENT_ADAPTERS.has(item.tool)) {
        deferred.push({ adapt, source, payload });
      } else {
        adapt(builder, source, payload);
      }
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

  // Membership is settled now: every read that could name one of the target's
  // pods has been adapted, so these see the same set whatever order the agent
  // worked in.
  for (const { adapt, source, payload } of deferred) {
    adapt(builder, source, payload);
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
    // Qualifications read in the order the investigation produced them, which
    // is the transcript's order and not the order the adapters happened to
    // run in: the membership-dependent ones are adapted last.
    limitations: [...builder.limitations].sort(
      (a, b) => a.firstOrder - b.firstOrder,
    ),
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
