import {
  stripAnsi,
  type Issue,
  type IssueRecentChange,
} from "@skyhook-io/k8s-ui";
import { fnv1a32 } from "@skyhook-io/k8s-ui/utils/structure-hash";
import { apiVersionToGroup } from "../../../../utils/navigation";
import {
  diagnosisSeverityTone,
  type DiagnosisChangeContext,
  type DiagnosisCrashCause,
  type DiagnosisDNSContext,
  type DiagnosisPodContainerRef,
  type DiagnosisPodLogEntry,
  type DiagnosisStartupBlocker,
} from "../../diagnoseEvidenceTypes";
import type {
  InvestigationEventEvidence,
  InvestigationEvidenceRelevance,
  InvestigationEvidenceSource,
  InvestigationGitOpsDiagnosis,
  InvestigationNetworkEvidence,
  InvestigationNetworkRoute,
} from "../types";
import { addDiagnoseMetrics } from "./prometheus";
import {
  type ProjectionBuilder,
  contextFrom,
  event,
  evidenceTierForRelevance,
  invalidPayload,
  issue,
  kubernetesResource,
  nonEmptyString,
  nonNegativeInteger,
  parseJSON,
  parseLogEntry,
  recentChange,
  record,
  relevanceForResource,
  resourceMatchesTarget,
  resourceRef,
  scopeFromArgs,
  stringArray,
} from "../builder";
import {
  addChanges,
  addEvents,
  addIssueObservation,
  addLogs,
  addNarrowHint,
  addResourceObservation,
} from "../observations";

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

export function adaptDiagnose(
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
      title: "No live issues for this resource",
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
      // One blocker keeps one identity however many pods share it. Keying a
      // merged card differently from a single-pod one made a re-diagnose that
      // crossed one pod look like a new fact, so the same reason appeared
      // twice; a Pod blocker is the same finding whether it holds one pod or
      // nine.
      const grouped = blocker.kind === "Pod" && pods.length > 1;
      builder.observe(
        blocker.kind === "Pod"
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
            // Always carry the pods, whether one or nine: they are what puts
            // this workload's pods into the set a later Prometheus query is
            // proved against, and a merged card that dropped them quietly
            // weakened attribution elsewhere.
            pods,
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
      "Radar returned part of the crash-cause candidates.",
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
          title: "No previous logs expected",
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

  addDiagnoseMetrics(
    builder,
    source,
    value.metrics,
    {
      kind: resource.kind,
      ...(apiVersionToGroup(resource.apiVersion)
        ? { group: apiVersionToGroup(resource.apiVersion) }
        : {}),
      namespace: resource.metadata.namespace,
      name: resource.metadata.name,
    },
    bundleRelevance,
  );
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
