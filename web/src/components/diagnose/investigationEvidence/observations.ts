import {
  defaultConditionTone,
  hpaStateLabel,
  hpaStateLevel,
  stripAnsi,
  type HPADiagnosisState,
  type Issue,
  type IssueRecentChange,
} from "@skyhook-io/k8s-ui";
import {
  diagnosisSeverityTone,
  type DiagnosisChangeContext,
  type DiagnosisPodLogEntry,
} from "../diagnoseEvidenceTypes";
import { investigationResourceEvidenceSummary } from "../investigationResourceEvidenceModel";
import type {
  InvestigationEventEvidence,
  InvestigationEvidenceRelevance,
  InvestigationEvidenceSource,
  InvestigationEvidenceTier,
  InvestigationGitOpsDiagnosis,
  InvestigationKubernetesResource,
  InvestigationResourceContext,
} from "./types";
import {
  type ProjectionBuilder,
  evidenceTierForRelevance,
  investigationResultLabel,
  nonEmptyString,
  record,
  resourceMatchesTarget,
  scopeFromArgs,
} from "./builder";

export function addNarrowHint(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  value: Record<string, unknown>,
): void {
  if (nonEmptyString(value.narrowHint)) {
    const label = investigationResultLabel(source);
    builder.limit(
      source,
      label,
      label +
        " returned part of the matching results to keep this investigation fast. More may exist.",
      "truncated",
    );
  }
}

function addResourceContextLimitations(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  context: InvestigationResourceContext | undefined,
): void {
  if (!context) return;
  for (const value of Array.isArray(context.omitted) ? context.omitted : []) {
    const omitted = record(value);
    if (
      !omitted ||
      !nonEmptyString(omitted.field) ||
      !nonEmptyString(omitted.reason)
    )
      continue;
    builder.limit(
      source,
      omitted.field,
      `Radar left out part of the resource context (${omitted.reason.replaceAll("_", " ")}).`,
      omitted.reason === "budget_exceeded" ? "truncated" : "unknown",
    );
  }
  if (context.referencedBy?.truncated) {
    const shown = context.referencedBy.items?.length ?? 0;
    builder.limit(
      source,
      "Relationships",
      `Radar returned ${shown} of ${context.referencedBy.total} referenced-by relationships.`,
      "truncated",
    );
  }
  if (context.appReferences?.staleSecretEnvTruncated) {
    builder.limit(
      source,
      "Application references",
      "Radar returned part of the stale Secret references.",
      "truncated",
    );
  }
}

function addIssueLimitations(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  value: Issue,
): void {
  if (value.members_truncated) {
    builder.limit(
      source,
      `Radar Issue ${value.id}`,
      "Radar returned part of the affected-resource list.",
      "truncated",
    );
  }
}

function resourceObservationSummary(
  resource: InvestigationKubernetesResource,
  context: InvestigationResourceContext | undefined,
  warnings: string[],
  gitOps?: InvestigationGitOpsDiagnosis,
  detailedIssueShown = false,
): string | undefined {
  if (!detailedIssueShown && context?.issueSummary?.topReason) {
    return context.issueSummary.topReason;
  }
  if (gitOps?.health) return `Health ${gitOps.health}`;
  if (gitOps?.ready) return `Ready ${gitOps.ready}`;
  if (gitOps?.sync) return `Sync ${gitOps.sync}`;
  if (gitOps?.suspended) return "Reconciliation suspended";
  const replicas = context?.workloadSummary?.replicas;
  const adverseScaler = adverseScalerStates(context)[0];
  if (replicas?.desired !== undefined) {
    const desired = replicas.desired;
    const ready = replicas.ready ?? 0;
    const readiness = `${ready}/${desired} replicas ready`;
    return adverseScaler
      ? `${readiness} · HPA: ${hpaStateLabel(adverseScaler)}`
      : readiness;
  }
  if (adverseScaler) return `HPA: ${hpaStateLabel(adverseScaler)}`;
  if (context?.statusSummary?.phase) return context.statusSummary.phase;
  if (context?.issueSummary?.topReason) return context.issueSummary.topReason;
  return (
    investigationResourceEvidenceSummary(resource) ||
    warnings[0] ||
    resource.metadata.namespace
  );
}

// A scaler that cannot act (no metrics, cannot read the target) or is pinned
// at its ceiling is a captured Radar fact about the workload, so it lifts the
// card the way an adverse condition does. Ordinary scale-ups and scale-downs
// are the autoscaler working and stay as detail.
function adverseScalerStates(
  context: InvestigationResourceContext | undefined,
): HPADiagnosisState[] {
  return (context?.scaledBy ?? []).flatMap((scaler) => {
    const state = scaler.hpaSummary?.state;
    if (!state) return [];
    return hpaStateLevel(state) === "unhealthy" || state === "limited_max"
      ? [state]
      : [];
  });
}

export function addResourceObservation(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  resource: InvestigationKubernetesResource,
  context: InvestigationResourceContext | undefined,
  warnings: string[],
  gitOpsDiagnosis: InvestigationGitOpsDiagnosis | undefined,
  hasDetailedCriticalIssue: boolean,
  relevance: InvestigationEvidenceRelevance,
): void {
  const issueSummary = context?.issueSummary;
  const severity = issueSummary?.highestSeverity ?? "";
  const critical = severity.toLowerCase() === "critical";
  const hasLiveIssue = (issueSummary?.count ?? 0) > 0;
  const replicas = context?.workloadSummary?.replicas;
  const desired = replicas?.desired;
  const ready = replicas ? (replicas.ready ?? 0) : undefined;
  const available = replicas ? (replicas.available ?? 0) : undefined;
  const replicaShortfall =
    desired !== undefined &&
    desired > 0 &&
    ((ready !== undefined && ready < desired) ||
      (available !== undefined && available < desired) ||
      (replicas?.unavailable ?? 0) > 0);
  const adverseCondition = (context?.statusSummary?.conditions ?? []).some(
    (condition) => defaultConditionTone(condition) === "fail",
  );
  const gitOpsAdverse = Boolean(
    gitOpsDiagnosis &&
    (gitOpsDiagnosis.health?.toLowerCase() === "degraded" ||
      gitOpsDiagnosis.health?.toLowerCase() === "missing" ||
      gitOpsDiagnosis.sync?.toLowerCase() === "outofsync" ||
      gitOpsDiagnosis.ready?.toLowerCase().startsWith("false") ||
      ["failed", "error"].includes(
        gitOpsDiagnosis.operationPhase?.toLowerCase() ?? "",
      )),
  );
  const hasAdverseState =
    replicaShortfall ||
    adverseCondition ||
    gitOpsAdverse ||
    adverseScalerStates(context).length > 0;
  const intendedTier: InvestigationEvidenceTier =
    critical && !hasDetailedCriticalIssue
      ? "key"
      : hasLiveIssue
        ? "supporting"
        : hasAdverseState
          ? "supporting"
          : "context";
  const tier = evidenceTierForRelevance(intendedTier, relevance);
  const tone = hasLiveIssue
    ? diagnosisSeverityTone(severity)
    : hasAdverseState
      ? "warning"
      : warnings.length > 0
        ? "warning"
        : "neutral";
  const namespace = resource.metadata.namespace;
  const identity = `${resource.apiVersion}:${resource.kind}:${namespace ?? ""}:${resource.metadata.name}`;
  builder.observe(identity, "resource", source, {
    tier,
    relevance,
    tone,
    title: `${resource.kind} ${namespace ? `${namespace}/` : ""}${resource.metadata.name}`,
    summary: resourceObservationSummary(
      resource,
      context,
      warnings,
      gitOpsDiagnosis,
      hasDetailedCriticalIssue,
    ),
    data: {
      type: "resource",
      resource,
      resourceContext: context,
      warnings,
      gitOpsDiagnosis,
    },
  });
  addResourceContextLimitations(builder, source, context);
}

export function addIssueObservation(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  value: Issue,
  producerRelevance: InvestigationEvidenceRelevance = "broader",
  pods?: string[],
): void {
  const matchesTarget = resourceMatchesTarget(builder.target, {
    kind: value.kind,
    group: value.group ?? "",
    namespace: value.namespace,
    name: value.name,
  });
  const relevance = matchesTarget ? "target" : producerRelevance;
  builder.observe(`issue:${value.id}`, "issue", source, {
    tier:
      relevance === "broader"
        ? "context"
        : value.severity === "critical"
          ? "key"
          : "supporting",
    tone: diagnosisSeverityTone(value.severity),
    relevance,
    title: value.reason,
    summary: value.cause || value.message,
    data: {
      type: "issue",
      issue: value,
      relevance,
      ...(pods && pods.length > 0 ? { pods } : {}),
    },
  });
  addIssueLimitations(builder, source, value);
}

export function addEvents(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  values: InvestigationEventEvidence[],
  identity: string,
  complete = true,
  emptyIsAuthoritative = false,
  relevance: InvestigationEvidenceRelevance = "broader",
  emptyReceipt: { title: string; message: string } = {
    title: "No warning events",
    message: "The warning-event query completed and returned no groups.",
  },
): void {
  const scope = scopeFromArgs(source);
  if (values.length === 0) {
    if (!source.confirmedSuccess || !complete) return;
    if (!emptyIsAuthoritative) {
      builder.limit(
        source,
        "Events",
        "No events were returned. This result does not establish that no events occurred.",
        "unknown",
      );
      return;
    }
    builder.observe(identity, "receipt", source, {
      tier: evidenceTierForRelevance("checked", relevance),
      relevance,
      tone: "neutral",
      title: emptyReceipt.title,
      summary: scope,
      data: {
        type: "receipt",
        checked: "events",
        scope,
        message: emptyReceipt.message,
      },
    });
    return;
  }
  const lead = leadEvent(values);
  builder.observe(identity, "events", source, {
    tier: evidenceTierForRelevance(
      values.some((item) => item.type.toLowerCase() === "warning")
        ? "supporting"
        : "context",
      relevance,
    ),
    relevance,
    tone: values.some((item) => item.type.toLowerCase() === "warning")
      ? "warning"
      : "info",
    title: "Kubernetes events",
    summary: `${lead.reason}: ${lead.message}${
      values.length > 1 ? ` · ${values.length} event groups` : ""
    } · ${scope}`,
    data: { type: "events", events: values, scope },
  });
}

/**
 * The newest warning, else the newest event. The card lead must be what
 * happened, not how many groups the producer returned.
 */
function leadEvent(
  values: InvestigationEventEvidence[],
): InvestigationEventEvidence {
  const newest = (items: InvestigationEventEvidence[]) =>
    items.reduce((best, item) =>
      Date.parse(item.lastTimestamp) > Date.parse(best.lastTimestamp)
        ? item
        : best,
    );
  const warnings = values.filter(
    (item) => item.type.toLowerCase() === "warning",
  );
  return newest(warnings.length > 0 ? warnings : values);
}

export function addChanges(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  values: IssueRecentChange[],
  identity: string,
  changeContext?: DiagnosisChangeContext,
  complete = true,
  emptyIsAuthoritative = false,
  relevance: InvestigationEvidenceRelevance = "broader",
  subject?: { kind?: string; namespace?: string; name: string },
  window?: string,
): void {
  const scope = scopeFromArgs(source);
  if (values.length === 0 && !changeContext?.changed) {
    if (!source.confirmedSuccess || !complete) return;
    if (!emptyIsAuthoritative) {
      builder.limit(
        source,
        "Recent changes",
        `No changes were returned for ${scope}. This result does not establish a complete change history.`,
        "unknown",
        "history",
      );
      return;
    }
    builder.observe(identity, "receipt", source, {
      tier: evidenceTierForRelevance("checked", relevance),
      relevance,
      tone: "neutral",
      title: "No recorded changes in this window",
      summary: scope,
      data: {
        type: "receipt",
        checked: "changes",
        scope,
        message: "The requested change window returned no tracked changes.",
      },
    });
    return;
  }
  builder.observe(identity, "changes", source, {
    // A producer-correlated change supports the diagnosis. A merely recent
    // edit is chronology/context and must not imply causality by proximity.
    tier: evidenceTierForRelevance(
      changeContext?.changed ? "supporting" : "context",
      relevance,
    ),
    relevance,
    tone: "info",
    title: `Recent changes${window ? ` · last ${window}` : ""}`,
    summary: changeContext?.changed
      ? changeContext.what === "The workload's Pod template changed" &&
        changeContext.when
        ? `Pod template changed; newest ReplicaSet created ${changeContext.when} ago`
        : `${changeContext.what || "A workload change was observed"}${changeContext.when ? ` · ${changeContext.when} ago` : ""}`
      : `${values.length} change${values.length === 1 ? "" : "s"} · ${scope}`,
    data: {
      type: "changes",
      changes: values,
      scope,
      changeContext,
      subject,
    },
  });
}

export function addLogs(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  value: DiagnosisPodLogEntry,
  previous: boolean,
  warnings: string[] = [],
  relevance: InvestigationEvidenceRelevance = "broader",
  namespace?: string,
): void {
  const lines = (value.logs?.lines ?? []).map((line) => stripAnsi(line));
  const normalizedWarnings = warnings.map((warning) => stripAnsi(warning));
  const normalizedError = value.error ? stripAnsi(value.error) : undefined;
  if (lines.length === 0) {
    builder.limit(
      source,
      `${value.pod} / ${value.container}`,
      value.error ||
        "No log lines were available. This does not mean the container is healthy.",
      value.error ? "error" : "unknown",
    );
    return;
  }
  // A producer-filtered excerpt is a candidate, not proof that its contents are
  // adverse. Query strings and routine request logs can contain words such as
  // "warning" or "critical" and still be successful traffic. Promote only an
  // explicit failure/error signature; keep benign excerpts available in Context.
  const diagnosticSignal = [...lines, ...normalizedWarnings].some((line) =>
    /(?:\b(?:error|exception|failed|failure|fatal|panic|crash|denied|refused|timeout|timed out|unhealthy|oomkill|back-?off)\b|\s5\d\d(?:\s|$))/i.test(
      line,
    ),
  );
  const selectedEvidence = value.logs?.fallback !== true && diagnosticSignal;
  const identity = `logs:${previous ? "previous" : "current"}:${value.pod}:${value.container}`;
  builder.observe(identity, "logs", source, {
    // FilterLogs' raw-tail fallback is useful provenance, but the producer did
    // not select it as diagnostic signal. Keep it in Context; only filtered
    // excerpts are Supporting evidence.
    tier: evidenceTierForRelevance(
      selectedEvidence ? "supporting" : "context",
      relevance,
    ),
    relevance,
    tone: selectedEvidence || normalizedError ? "warning" : "neutral",
    title: `${previous ? "Previous" : "Current"} logs · ${value.pod} / ${value.container}`,
    summary:
      lines.length > 0
        ? `${lines.length} selected line${lines.length === 1 ? "" : "s"}`
        : "No log lines available",
    data: {
      type: "logs",
      pod: value.pod,
      container: value.container,
      namespace,
      previous,
      logs: value.logs ? { ...value.logs, lines } : undefined,
      warnings: normalizedWarnings,
      error: normalizedError,
    },
  });
  if (normalizedError) {
    builder.limit(
      source,
      `${value.pod} / ${value.container}`,
      normalizedError,
      "error",
    );
  }
}
