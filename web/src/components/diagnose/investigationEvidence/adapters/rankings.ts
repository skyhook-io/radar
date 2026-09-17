import { nonEmptyString, parseJSON, record } from "../parse";
import {
  invalidPayload,
  evidenceTierForRelevance,
  resourceMatchesTarget,
  scopeFromArgs,
  type ProjectionBuilder,
} from "../observations";
import type {
  InvestigationEvidenceSource,
  InvestigationRankingRow,
} from "../types";
import { apiVersionToGroup } from "../../../../utils/navigation";

function quantity(milli: unknown, unit: "m" | "Mi"): string {
  return typeof milli === "number" ? `${milli}${unit}` : "";
}

// A ranking is one card: the rows as the tool ordered them, with the
// investigated resource's own row (or its pods) marked so the reader sees
// where it stands without hunting.
export function adaptTopResources(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  if (!value || !nonEmptyString(value.kind)) {
    invalidPayload(builder, source);
    return;
  }
  if (value.metricsAvailable === false) {
    builder.limit(
      source,
      "Live metrics",
      nonEmptyString(value.reason)
        ? value.reason
        : "Live metrics were not available for this ranking.",
      "unknown",
    );
    return;
  }
  const raw = Array.isArray(value.items)
    ? value.items
    : Array.isArray(value.workloads)
      ? value.workloads
      : [];
  const rows: InvestigationRankingRow[] = raw.flatMap((entry) => {
    const item = record(entry);
    if (!item || !nonEmptyString(item.kind) || !nonEmptyString(item.name))
      return [];
    const namespace = nonEmptyString(item.namespace)
      ? item.namespace
      : undefined;
    const target =
      (item.kind === "Pod" && builder.establishedTargetPods.has(item.name)) ||
      resourceMatchesTarget(builder.target, {
        kind: item.kind,
        group: nonEmptyString(item.apiVersion)
          ? apiVersionToGroup(item.apiVersion)
          : undefined,
        namespace,
        name: item.name,
      });
    return [
      {
        kind: item.kind,
        namespace,
        name: item.name,
        cpu: quantity(item.cpuMilli, "m"),
        memory: quantity(item.memoryMi, "Mi"),
        ...(typeof item.cpuLimitMilli === "number" && item.cpuLimitMilli > 0
          ? { cpuLimit: quantity(item.cpuLimitMilli, "m") }
          : {}),
        ...(typeof item.memoryLimitMi === "number" && item.memoryLimitMi > 0
          ? { memoryLimit: quantity(item.memoryLimitMi, "Mi") }
          : {}),
        ...(typeof item.restarts === "number"
          ? { restarts: item.restarts }
          : {}),
        ...(nonEmptyString(item.ready) ? { ready: item.ready } : {}),
        ...(nonEmptyString(item.status) ? { status: item.status } : {}),
        target,
      },
    ];
  });
  const args = record(parseJSON(source.args ?? ""));
  const scope = scopeFromArgs(source);
  const sort = nonEmptyString(value.sort)
    ? value.sort
    : nonEmptyString(args?.sort)
      ? args.sort
      : "cpu";
  const relevance = rows.some((row) => row.target)
    ? "producer-related"
    : "broader";
  const targetRows = rows.filter((row) => row.target).length;
  builder.observe(`ranking:${value.kind}:${sort}:${scope}`, "ranking", source, {
    tier: evidenceTierForRelevance("context", relevance),
    relevance,
    tone: "neutral",
    title: `Top ${value.kind} by ${sort}`,
    summary: `${rows.length} ranked${targetRows > 0 ? ` · ${targetRows === 1 ? "this workload's row" : `${targetRows} of its pods`} marked` : ""} · ${scope}`,
    data: { type: "ranking", kind: value.kind, sort, rows, scope },
  });
}
