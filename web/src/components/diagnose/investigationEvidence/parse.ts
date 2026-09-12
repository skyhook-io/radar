import {
  type Issue,
  type IssueRecentChange,
  stripAnsi,
} from "@skyhook-io/k8s-ui";
import { type TimeSeries } from "@skyhook-io/k8s-ui/components/charts";
import {
  type DiagnosisChangeContext,
  type DiagnosisCrashCause,
  type DiagnosisFilteredLogs,
  type DiagnosisPodContainerRef,
  type DiagnosisPodLogEntry,
  type DiagnosisResourceRef,
  type DiagnosisStartupBlocker,
} from "../diagnoseEvidenceTypes";
import {
  type InvestigationEventEvidence,
  type InvestigationGitOpsDiagnosis,
  type InvestigationKubernetesResource,
  type InvestigationNetworkEvidence,
  type InvestigationNetworkRoute,
  type InvestigationResourceSummary,
  type InvestigationTopologyEdge,
  type InvestigationTopologyNode,
} from "./types";

export function record(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}
export function nonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0;
}
export function stringArray(value: unknown): string[] | undefined {
  return Array.isArray(value) && value.every((item) => typeof item === "string")
    ? value
    : undefined;
}
export function parseJSON(value: string): unknown {
  try {
    return JSON.parse(value);
  } catch {
    return undefined;
  }
}
export function kubernetesResource(
  value: unknown,
): InvestigationKubernetesResource | undefined {
  const resource = record(value);
  const metadata = record(resource?.metadata);
  // Core Secrets intentionally use Radar's current safe detail contract rather
  // than a Kubernetes object: identity + type + key names, with no values. Make
  // that producer shape canonical for the projection instead of rejecting the
  // exact evidence the agent saw.
  if (
    resource?.kind === "Secret" &&
    !metadata &&
    nonEmptyString(resource.name) &&
    (resource.namespace === undefined ||
      typeof resource.namespace === "string") &&
    (resource.type === undefined || typeof resource.type === "string") &&
    Array.isArray(resource.keys) &&
    resource.keys.every((key) => typeof key === "string")
  ) {
    return {
      ...resource,
      apiVersion: "v1",
      metadata: {
        name: resource.name,
        namespace: resource.namespace as string | undefined,
        ...(record(resource.labels) ? { labels: resource.labels } : {}),
        ...(record(resource.annotations)
          ? { annotations: resource.annotations }
          : {}),
      },
    } as InvestigationKubernetesResource;
  }
  if (
    !resource ||
    !nonEmptyString(resource.apiVersion) ||
    !nonEmptyString(resource.kind) ||
    !metadata ||
    !nonEmptyString(metadata.name)
  ) {
    return undefined;
  }
  return resource as unknown as InvestigationKubernetesResource;
}
export function gitOpsDiagnosis(
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
export function resourceSummary(
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
export function issue(value: unknown): Issue | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.id) ||
    (candidate.severity !== "critical" && candidate.severity !== "warning") ||
    !nonEmptyString(candidate.kind) ||
    !nonEmptyString(candidate.name) ||
    !nonEmptyString(candidate.reason)
  ) {
    return undefined;
  }
  return candidate as unknown as Issue;
}
export function recentChange(value: unknown): IssueRecentChange | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.kind) ||
    (candidate.apiVersion !== undefined &&
      !nonEmptyString(candidate.apiVersion)) ||
    !nonEmptyString(candidate.name) ||
    !nonEmptyString(candidate.changeType) ||
    !nonEmptyString(candidate.timestamp)
  ) {
    return undefined;
  }
  return candidate as unknown as IssueRecentChange;
}
export function diagnosisChangeContext(
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
export function event(value: unknown): InvestigationEventEvidence | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.reason) ||
    !nonEmptyString(candidate.message) ||
    !nonEmptyString(candidate.type) ||
    typeof candidate.count !== "number" ||
    !nonEmptyString(candidate.lastTimestamp)
  ) {
    return undefined;
  }
  return candidate as unknown as InvestigationEventEvidence;
}
export function filteredLogs(
  value: unknown,
): DiagnosisFilteredLogs | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    (!Array.isArray(candidate.lines) && candidate.lines !== null) ||
    (Array.isArray(candidate.lines) &&
      !candidate.lines.every((line) => typeof line === "string")) ||
    typeof candidate.totalLines !== "number" ||
    typeof candidate.matchedLines !== "number" ||
    typeof candidate.fallback !== "boolean"
  ) {
    return undefined;
  }
  return {
    ...(candidate as unknown as DiagnosisFilteredLogs),
    lines: Array.isArray(candidate.lines)
      ? candidate.lines.map((line) => stripAnsi(line))
      : candidate.lines,
  } as DiagnosisFilteredLogs;
}
export function resourceRef(value: unknown): DiagnosisResourceRef | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.kind) ||
    !nonEmptyString(candidate.name)
  ) {
    return undefined;
  }
  return candidate as unknown as DiagnosisResourceRef;
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
export function networkEvidence(
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
export function topologyNode(
  value: unknown,
): InvestigationTopologyNode | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.id) ||
    !nonEmptyString(candidate.kind) ||
    !nonEmptyString(candidate.name)
  ) {
    return undefined;
  }
  return candidate as unknown as InvestigationTopologyNode;
}
export function topologyEdge(
  value: unknown,
): InvestigationTopologyEdge | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.source) ||
    !nonEmptyString(candidate.target) ||
    !nonEmptyString(candidate.type)
  ) {
    return undefined;
  }
  return candidate as unknown as InvestigationTopologyEdge;
}
export function startupBlocker(
  value: unknown,
): DiagnosisStartupBlocker | undefined {
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
export function crashCause(value: unknown): DiagnosisCrashCause | undefined {
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
export function podContainerRef(
  value: unknown,
): DiagnosisPodContainerRef | undefined {
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
export function parseLogEntry(
  value: unknown,
): DiagnosisPodLogEntry | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.pod) ||
    !nonEmptyString(candidate.container)
  ) {
    return undefined;
  }
  if (candidate.logs !== undefined && !filteredLogs(candidate.logs))
    return undefined;
  if (candidate.error !== undefined && typeof candidate.error !== "string")
    return undefined;
  return candidate as unknown as DiagnosisPodLogEntry;
}
export function nonNegativeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0;
}
export function stringRecord(
  value: unknown,
): Record<string, string> | undefined {
  const candidate = record(value);
  if (!candidate) return undefined;
  return Object.values(candidate).every((item) => typeof item === "string")
    ? (candidate as Record<string, string>)
    : undefined;
}
export function timeSeries(value: unknown): TimeSeries | undefined {
  const item = record(value);
  if (!item || !Array.isArray(item.dataPoints)) return undefined;
  const labels = record(item.labels) ?? {};
  if (Object.values(labels).some((label) => typeof label !== "string")) {
    return undefined;
  }
  const dataPoints: TimeSeries["dataPoints"] = [];
  for (const raw of item.dataPoints) {
    const point = record(raw);
    if (!point || typeof point.timestamp !== "number") return undefined;
    if (
      point.value !== undefined &&
      point.value !== null &&
      typeof point.value !== "number"
    ) {
      return undefined;
    }
    dataPoints.push({
      timestamp: point.timestamp,
      value: typeof point.value === "number" ? point.value : null,
    });
  }
  return { labels: labels as Record<string, string>, dataPoints };
}
