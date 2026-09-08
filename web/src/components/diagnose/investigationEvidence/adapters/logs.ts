import { displayKind } from "@skyhook-io/k8s-ui";
import type {
  InvestigationEvidenceRelevance,
  InvestigationEvidenceSource,
} from "../types";
import {
  type ProjectionBuilder,
  evidenceTierForRelevance,
  filteredLogs,
  invalidPayload,
  nonEmptyString,
  nonNegativeInteger,
  parseJSON,
  parseLogEntry,
  previousFromArgs,
  record,
  relevanceForResource,
  resourceMatchesTarget,
  sourceArgsRelevance,
  stringArray,
} from "../builder";
import { addLogs, addNarrowHint } from "../observations";

export function adaptPodLogs(
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

export function adaptWorkloadLogs(
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
  const noStreams = `Radar found ${value.pods} pod${value.pods === 1 ? "" : "s"} but got no logs from ${value.pods === 1 ? "it" : "them"}, so the logs were not checked.`;
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
