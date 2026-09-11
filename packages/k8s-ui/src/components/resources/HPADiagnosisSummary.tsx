import type { ReactNode } from "react";
import { Badge, type BadgeSeverity } from "../ui/Badge";
import { StatusDot } from "../ui/status-tone";
import { hpaStateLabel, hpaStateLevel } from "./resource-utils-hpa";
import type {
  HPADiagnosisState,
  HPADiagnosisView,
  HPAReasonSummary,
} from "../../types";

export function hpaBadgeSeverity(state: HPADiagnosisState): BadgeSeverity {
  switch (hpaStateLevel(state)) {
    case "healthy":
      return "success";
    case "unhealthy":
      return "error";
    case "degraded":
      return "warning";
    case "alert":
      return "alert";
    case "neutral":
      return "info";
    default:
      return "neutral";
  }
}

/**
 * A reason that named the state, or repeats the summary verbatim, says the
 * headline over again — only the controller's own sentence behind it (the
 * reason's detail) adds anything a reader does not already have.
 */
export function isHPAReasonRedundant(
  diagnosis: HPADiagnosisView,
  reason: HPAReasonSummary,
): boolean {
  return reason.id === diagnosis.state || reason.message === diagnosis.summary;
}

export interface HPAReasonGroups {
  /** Reasons whose message restates the headline; render their detail, not their message. */
  redundant: HPAReasonSummary[];
  /** Reasons that carry something the headline does not. */
  additional: HPAReasonSummary[];
}

export function hpaReasonGroups(diagnosis: HPADiagnosisView): HPAReasonGroups {
  const reasons = diagnosis.reasons ?? [];
  const redundant = reasons.filter((reason) =>
    isHPAReasonRedundant(diagnosis, reason),
  );
  return {
    redundant,
    additional: reasons.filter((reason) => !redundant.includes(reason)),
  };
}

function formatReasonID(id: string): string {
  return id.replace(/_/g, " ");
}

export interface HPADiagnosisSummaryProps {
  diagnosis: HPADiagnosisView;
  /**
   * `detail` — the HPA detail page: a boxed Evidence block per reason.
   * `inline` — an evidence card on the workload the HPA scales: the headline
   * once, then the controller's condition quoted and attributed.
   */
  variant: "detail" | "inline";
  /**
   * Host-rendered identity for the `inline` variant (the scaler's name, owner
   * links) placed before the state indicator. Navigation stays with the host.
   */
  header?: ReactNode;
}

export function HPADiagnosisSummary({
  diagnosis,
  variant,
  header,
}: HPADiagnosisSummaryProps) {
  const { redundant, additional } = hpaReasonGroups(diagnosis);
  const bounds = diagnosis.bounds;

  if (variant === "inline") {
    return (
      <div className="space-y-1 text-xs">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          {header}
          <span className="inline-flex items-center gap-1.5 text-theme-text-secondary">
            <StatusDot
              tone={hpaStateLevel(diagnosis.state)}
              className="shrink-0"
            />
            {hpaStateLabel(diagnosis.state)}
          </span>
          {bounds ? (
            <span className="font-mono tabular-nums text-theme-text-tertiary">
              {bounds.current}/{bounds.desired} replicas · bounds {bounds.min}-
              {bounds.max}
            </span>
          ) : null}
        </div>
        <p className="text-theme-text-secondary">{diagnosis.summary}</p>
        {redundant.map((reason) =>
          reason.detail ? (
            <p
              key={`${reason.id}-${reason.detail}`}
              className="text-theme-text-tertiary"
            >
              {/* Same rule as the detail variant: only a condition-built
                  reason carries the controller's own sentence. */}
              {reason.conditionType ? (
                <>
                  Kubernetes {reason.conditionType}
                  {reason.conditionReason ? ` · ${reason.conditionReason}` : ""}
                  : &ldquo;{reason.detail}&rdquo;
                </>
              ) : (
                reason.detail
              )}
            </p>
          ) : null,
        )}
        {additional.length > 0 ? (
          <ul className="list-disc space-y-0.5 pl-4 text-theme-text-secondary marker:text-theme-text-tertiary">
            {additional.map((reason) => (
              <li key={`${reason.id}-${reason.message}`}>
                {reason.message}
                {reason.detail ? (
                  <span className="text-theme-text-tertiary">
                    {" "}
                    · {reason.detail}
                  </span>
                ) : null}
              </li>
            ))}
          </ul>
        ) : null}
      </div>
    );
  }

  const reasons = diagnosis.reasons ?? [];
  return (
    <div className="card-inner space-y-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="text-sm font-medium text-theme-text-primary">
            {diagnosis.summary}
          </div>
          {bounds ? (
            <div className="mt-1 text-xs text-theme-text-secondary">
              {bounds.current}/{bounds.desired} replicas, bounds {bounds.min}-
              {bounds.max}
            </div>
          ) : null}
        </div>
        <Badge severity={hpaBadgeSeverity(diagnosis.state)}>
          {hpaStateLabel(diagnosis.state)}
        </Badge>
      </div>
      {reasons.length > 0 && (
        <div className="space-y-2">
          {reasons.map((reason) => (
            <div
              key={`${reason.id}-${reason.message}`}
              className="rounded border border-theme-border bg-theme-surface p-2"
            >
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <span className="font-medium text-theme-text-primary">
                  Evidence
                </span>
                <span className="text-theme-text-tertiary">
                  {formatReasonID(reason.id)}
                </span>
                {reason.conditionType && (
                  <span className="text-theme-text-tertiary">
                    {reason.conditionType}
                  </span>
                )}
                {reason.conditionReason && (
                  <span className="text-theme-text-tertiary">
                    {reason.conditionReason}
                  </span>
                )}
              </div>
              {/* Radar's reading leads, as the reason is built to; the
                  controller's own sentence follows it, quoted and attributed
                  the same way the inline variant attributes it. Unlabelled
                  beside the condition chips, it read as Radar's words. */}
              {!isHPAReasonRedundant(diagnosis, reason) && (
                <div className="mt-1 text-xs text-theme-text-secondary">
                  {reason.message}
                </div>
              )}
              {reason.detail &&
                // `detail` is the controller's own sentence only on a reason
                // built from a condition, which is the only kind that carries
                // a condition type. Elsewhere it is Radar's — a list of metric
                // names, a note about an unobserved generation — and quoting
                // that as Kubernetes would put words in its mouth.
                (reason.conditionType ? (
                  <p className="mt-1 text-xs text-theme-text-tertiary">
                    Kubernetes: &ldquo;{reason.detail}&rdquo;
                  </p>
                ) : (
                  <p className="mt-1 text-xs text-theme-text-tertiary">
                    {reason.detail}
                  </p>
                ))}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
