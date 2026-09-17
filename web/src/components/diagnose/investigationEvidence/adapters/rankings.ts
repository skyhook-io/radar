import { nonEmptyString, parseJSON, record } from "../parse";
import {
  invalidPayload,
  evidenceTierForRelevance,
  sameKind,
  scopeFromArgs,
  type ProjectionBuilder,
} from "../observations";
import type {
  InvestigationEvidenceSource,
  InvestigationRankingRow,
} from "../types";

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
    // Rows carry kind, namespace and name and no API group; the kinds a
    // ranking holds (pods, the built-in workloads, nodes) are not ambiguous.
    const target =
      (item.kind === "Pod" && builder.establishedTargetPods.has(item.name)) ||
      (sameKind(item.kind, builder.target.kind) &&
        (namespace ?? "") === (builder.target.namespace ?? "") &&
        item.name === builder.target.name);
    const ownerRaw = record(item.owner);
    const owner =
      ownerRaw && nonEmptyString(ownerRaw.kind) && nonEmptyString(ownerRaw.name)
        ? {
            kind: ownerRaw.kind,
            ...(nonEmptyString(ownerRaw.group)
              ? { group: ownerRaw.group }
              : {}),
            ...(namespace ? { namespace } : {}),
            name: ownerRaw.name,
          }
        : undefined;
    return [
      {
        kind: item.kind,
        namespace,
        name: item.name,
        ...(owner ? { owner } : {}),
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
  // A pod ranking of the workload's own namespace that holds none of its
  // established pods says so on the card, so the reader does not scan for a
  // row that is not there. "Not in these results" is all that is known: a
  // capped ranking omits low consumers, and the tool's skipped count does
  // not say which pods it skipped.
  const rankedNamespace = nonEmptyString(args?.namespace)
    ? args.namespace
    : undefined;
  const targetPodsAbsent =
    value.kind === "pods" &&
    targetRows === 0 &&
    builder.establishedTargetPods.size > 0 &&
    (rankedNamespace === undefined ||
      rankedNamespace === builder.target.namespace);
  const skipped =
    typeof value.skippedNoMetrics === "number" && value.skippedNoMetrics > 0
      ? value.skippedNoMetrics
      : 0;
  builder.observe(`ranking:${value.kind}:${sort}:${scope}`, "ranking", source, {
    tier: evidenceTierForRelevance("context", relevance),
    relevance,
    tone: "neutral",
    title: `Top ${value.kind} by ${sort}`,
    summary: [
      `${rows.length} ranked`,
      targetRows > 0
        ? `${targetRows === 1 ? "this workload's row" : `${targetRows} of its pods`} marked`
        : undefined,
      rankedNamespace ? `in ${rankedNamespace}` : "cluster-wide",
      targetPodsAbsent
        ? "this workload's pods aren't in these results"
        : undefined,
      skipped > 0
        ? `${skipped} ${value.kind === "nodes" ? (skipped === 1 ? "node" : "nodes") : skipped === 1 ? "pod" : "pods"} omitted: no metrics`
        : undefined,
    ]
      .filter(Boolean)
      .join(" · "),
    data: { type: "ranking", kind: value.kind, sort, rows, scope },
  });
}
