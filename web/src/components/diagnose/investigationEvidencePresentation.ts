import { parseLogLine } from "../../utils/log-format";
import {
  evidenceSemanticSnapshot,
  type InvestigationEvidenceObservation,
  type InvestigationEvidenceLimitation,
} from "./investigationEvidence";

/** Display significance only. Never use this to discard records or bind citations. */
export function evidenceDisplaySnapshot(
  observation: Pick<
    InvestigationEvidenceObservation,
    "data" | "tone" | "title" | "summary"
  >,
): string {
  const data = observation.data;
  switch (data.type) {
    case "resource": {
      const { metadata, ...resource } = data.resource;
      const identity = { ...metadata };
      delete identity.resourceVersion;
      delete identity.managedFields;
      const context = data.resourceContext;
      return JSON.stringify({
        type: data.type,
        resource: {
          ...resource,
          metadata: identity,
          status: statusWithoutObservationTimes(resource.status),
        },
        // These summaries can carry live state even when the raw object is cached.
        status: statusWithoutObservationTimes(context?.statusSummary),
        workload: context?.workloadSummary,
        scalers: context?.scaledBy,
        issues: context?.issueSummary,
        gitOps: data.gitOpsDiagnosis,
        warnings: data.warnings.map(warningWithoutElapsedTime),
      });
    }
    case "crash": {
      const { logLine, ...crash } = data.crash;
      return JSON.stringify({
        type: data.type,
        namespace: data.namespace,
        crash: {
          ...crash,
          logSource: undefined,
          logLine: parseLogLine(logLine).content,
        },
      });
    }
    case "events":
      return JSON.stringify({
        type: data.type,
        scope: data.scope,
        // Repetitions and collection time do not constitute a new finding.
        events: data.events
          .map(({ reason, message, type }) =>
            JSON.stringify({ reason, message, type }),
          )
          .sort(),
      });
    default:
      // Unknown evidence types keep their existing conservative comparison.
      return evidenceSemanticSnapshot(observation);
  }
}

// Only condition bookkeeping is normalized; similarly named configuration keys
// and arbitrary status fields retain their exact values.
function statusWithoutObservationTimes(status: unknown): unknown {
  if (!status || typeof status !== "object" || Array.isArray(status))
    return status;
  const state = status as Record<string, unknown>;
  if (!Array.isArray(state.conditions)) return status;
  return {
    ...state,
    conditions: state.conditions.map((condition: unknown) => {
      if (
        !condition ||
        typeof condition !== "object" ||
        Array.isArray(condition)
      )
        return condition;
      const finding = { ...(condition as Record<string, unknown>) };
      delete finding.lastTransitionTime;
      delete finding.lastUpdateTime;
      delete finding.lastProbeTime;
      delete finding.lastHeartbeatTime;
      return finding;
    }),
  };
}

function warningWithoutElapsedTime(warning: string): string {
  // Exact warning format from pkg/k8score/object_warnings.go. Preserve the
  // distinction between a recent failure and one present since creation.
  return warning
    .replace(/^(Condition `[^`]+` for ~)[\d.dhms]+/, "$1<elapsed>")
    .replace(/\(resource age: [\d.dhms]+\)\.$/, "(resource age: <elapsed>).");
}

export interface EvidenceCoverageGroup {
  label: string;
  summary: string;
  limitations: InvestigationEvidenceLimitation[];
  hasError: boolean;
  historyOnly: boolean;
}

/** Group the presentation, not the underlying limitations or health qualification. */
export function groupEvidenceCoverage(
  limitations: InvestigationEvidenceLimitation[],
): EvidenceCoverageGroup[] {
  const byLabel = new Map<string, InvestigationEvidenceLimitation[]>();
  for (const limitation of limitations) {
    const entries = byLabel.get(limitation.source) ?? [];
    entries.push(limitation);
    byLabel.set(limitation.source, entries);
  }
  return [...byLabel]
    .map(([label, entries]) => {
      const errors = entries.filter((entry) => entry.kind === "error");
      const historyOnly = entries.every(
        (entry) => entry.kind === "unknown" && entry.presentation === "history",
      );
      const truncated = entries.some((entry) => entry.kind === "truncated");
      const ordinaryLimits = entries.every(
        (entry) =>
          entry.kind === "truncated" ||
          (entry.kind === "unknown" && entry.presentation === "history"),
      );
      let summary: string;
      if (errors.length) {
        // Never replace permission/transport failures with a benign sampling note.
        summary = [...new Set(errors.map((entry) => entry.message))].join(
          " · ",
        );
      } else if (ordinaryLimits && label === "Recent changes") {
        summary = truncated
          ? "Some changes may be missing; change history is incomplete."
          : "Change history is incomplete.";
      } else if (ordinaryLimits && label === "Container logs" && truncated) {
        summary = "Only part of the logs was checked.";
      } else if (
        ordinaryLimits &&
        label === "Issue change correlation" &&
        truncated
      ) {
        summary = "Not all issues were checked against recent changes.";
      } else {
        summary = [...new Set(entries.map((entry) => entry.message))].join(
          " · ",
        );
      }
      return {
        label,
        summary,
        limitations: entries,
        hasError: errors.length > 0,
        historyOnly,
      };
    })
    .sort(
      (a, b) =>
        Number(b.hasError) - Number(a.hasError) ||
        Number(a.historyOnly) - Number(b.historyOnly),
    );
}
