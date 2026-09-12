import { type Issue, type IssueRecentChange } from "@skyhook-io/k8s-ui";
import { fnv1a32 } from "@skyhook-io/k8s-ui/utils/structure-hash";
import { type TimeSeries } from "@skyhook-io/k8s-ui/components/charts";
import { apiVersionToGroup } from "../../../../utils/navigation";
import {
  type DiagnosisDNSContext,
  type DiagnosisPodContainerRef,
  type DiagnosisPodLogEntry,
  type DiagnosisResourceRef,
  type DiagnosisStartupBlocker,
  diagnosisSeverityTone,
} from "../../diagnoseEvidenceTypes";
import { metricsWindowLabel } from "./prometheus";
import {
  ProjectionBuilder,
  addChanges,
  addEvents,
  addIssueObservation,
  addLogs,
  addNarrowHint,
  addResourceObservation,
  contextFrom,
  evidenceTierForRelevance,
  invalidPayload,
  isDiagnosableWorkloadKind,
  relevanceForResource,
  resourceMatchesTarget,
  scopeFromArgs,
} from "../observations";
import {
  crashCause,
  diagnosisChangeContext,
  event,
  gitOpsDiagnosis,
  issue,
  kubernetesResource,
  networkEvidence,
  nonEmptyString,
  nonNegativeInteger,
  parseJSON,
  parseLogEntry,
  podContainerRef,
  recentChange,
  record,
  startupBlocker,
  stringArray,
  timeSeries,
} from "../parse";
import {
  type InvestigationEventEvidence,
  type InvestigationEvidenceRelevance,
  type InvestigationEvidenceSource,
  type InvestigationMetricsEvidence,
} from "../types";

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
      title: "Radar's diagnosis found no live issues",
      summary: scope,
      data: {
        type: "receipt",
        checked: "issues",
        scope,
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
/**
 * One half of a Go↔TS contract: `diagnoseMetricsCategories` in
 * internal/mcp/tools_diagnose_metrics.go decides which categories the producer
 * captures, and a series whose category is missing here is discarded. Change
 * both together.
 */
const DIAGNOSE_METRICS_LABELS: Record<string, string> = {
  cpu: "CPU usage",
  memory: "Memory working set",
  restarts: "Restarts",
};
/**
 * The vitals `diagnose` captured inside the same call: one chart per category
 * over the exact pods the bundle covers. They take the bundle's relevance, so a
 * neighbour's diagnose never charts as evidence for the target, and they carry
 * the diagnosed resource as their subject so recorded changes can be marked
 * on them. An absent field means Prometheus was not available or the read was
 * not permitted, which is not a collection failure the producer reported.
 */
function addDiagnoseMetrics(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  raw: unknown,
  subject: DiagnosisResourceRef,
  relevance: InvestigationEvidenceRelevance,
): void {
  if (raw === undefined) return;
  const value = record(raw);
  const window = record(value?.window);
  if (
    !value ||
    !window ||
    !nonEmptyString(window.start) ||
    !nonEmptyString(window.end) ||
    !nonEmptyString(window.step) ||
    !(Date.parse(window.end) > Date.parse(window.start)) ||
    !nonNegativeInteger(value.pods) ||
    (value.partial !== undefined && typeof value.partial !== "boolean") ||
    (value.omittedPods !== undefined &&
      !nonNegativeInteger(value.omittedPods)) ||
    (value.error !== undefined && typeof value.error !== "string") ||
    !Array.isArray(value.series)
  ) {
    invalidPayload(builder, source, "Workload metrics");
    return;
  }
  const scope = scopeFromArgs(source);
  const partial = value.partial === true;
  // The pods the chart covers: the ones the workload controlled when the
  // bundle was captured, which a rollout during the window can miss.
  const podsLabel = partial
    ? `first ${value.pods} of ${typeof value.omittedPods === "number" ? value.pods + value.omittedPods : "the"} current pods`
    : `${value.pods} current pod${value.pods === 1 ? "" : "s"}`;
  const windowLabel = metricsWindowLabel({
    mode: "range",
    start: window.start,
    end: window.end,
    step: window.step,
  });
  for (const rawEntry of value.series) {
    const entry = record(rawEntry);
    const rawSeries = entry?.series;
    const series = Array.isArray(rawSeries)
      ? rawSeries
          .map(timeSeries)
          .filter((item): item is TimeSeries => Boolean(item))
      : undefined;
    if (
      !entry ||
      !nonEmptyString(entry.category) ||
      !Object.hasOwn(DIAGNOSE_METRICS_LABELS, entry.category) ||
      !nonEmptyString(entry.query) ||
      !nonEmptyString(entry.unit) ||
      !Array.isArray(rawSeries) ||
      !series ||
      series.length !== rawSeries.length
    ) {
      invalidPayload(builder, source, "Workload metrics");
      continue;
    }
    const label = DIAGNOSE_METRICS_LABELS[entry.category];
    const data: InvestigationMetricsEvidence = {
      type: "metrics",
      origin: "diagnose",
      query: entry.query,
      mode: "range",
      start: window.start,
      end: window.end,
      step: window.step,
      unit: entry.unit,
      label,
      series,
      truncated: false,
      subject,
      pods: value.pods,
      partial,
    };
    builder.observe(
      `metrics:diagnose:${subject.group ?? ""}:${subject.kind}:${subject.namespace ?? ""}:${subject.name}:${entry.category}`,
      "metrics",
      source,
      {
        tier: evidenceTierForRelevance("supporting", relevance),
        relevance,
        tone: "neutral",
        title: `${label} · ${scope}`,
        summary: [
          series.length === 0 ? "No samples in the window" : podsLabel,
          windowLabel,
          partial ? "partial pod set" : undefined,
        ]
          .filter((part): part is string => Boolean(part))
          .join(" · "),
        data,
      },
    );
  }
  if (nonEmptyString(value.error)) {
    builder.limit(source, "Workload metrics", value.error, "error");
  }
}
