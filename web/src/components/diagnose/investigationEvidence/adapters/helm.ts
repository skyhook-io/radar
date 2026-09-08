import { apiVersionToGroup } from "../../../../utils/navigation";
import type {
  InvestigationEvidenceRelevance,
  InvestigationEvidenceSource,
  InvestigationHelmOperation,
  InvestigationHelmOwnedResource,
  InvestigationHelmRelease,
} from "../types";
import {
  type ProjectionBuilder,
  evidenceTierForRelevance,
  invalidPayload,
  nonEmptyString,
  record,
  resourceMatchesTarget,
} from "../builder";

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

export function adaptHelmRelease(
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
  if (nonEmptyString(value.valuesError)) {
    builder.limit(source, "Helm values", value.valuesError, "error");
  }
}
