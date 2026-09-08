import {
  displayKind,
  englishPlural,
  kindToPlural,
  type IssueRecentChange,
} from "@skyhook-io/k8s-ui";
import { apiVersionToGroup } from "../../../../utils/navigation";
import type {
  InvestigationEventEvidence,
  InvestigationEvidenceSource,
  InvestigationResourceSummary,
} from "../types";
import {
  type ProjectionBuilder,
  contextFrom,
  event,
  invalidPayload,
  kubernetesResource,
  nonEmptyString,
  parseJSON,
  recentChange,
  record,
  relevanceForResource,
  scopeFromArgs,
  stringArray,
} from "../builder";
import {
  addChanges,
  addEvents,
  addNarrowHint,
  addResourceObservation,
} from "../observations";

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

export function adaptGetResource(
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

export function adaptListResources(
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
      `Radar found no matching resources for ${scope}. Anything you do not have permission to read was not searched.`,
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
