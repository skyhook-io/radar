import type { InvestigationTimelineScope } from "./navigation";
import { clsx } from "clsx";
import { EVIDENCE_KIND_TRAITS } from "../investigationEvidenceKinds";
import {
  FileClock,
  FileSearch,
  Info,
  SquareArrowOutUpRight,
} from "lucide-react";
import { defaultConditionTone, displayKind } from "@skyhook-io/k8s-ui";
import type {
  InvestigationEvidenceData,
  InvestigationEvidenceGroup,
  InvestigationEvidenceObservation,
  InvestigationEvidenceSource,
  InvestigationEvidenceTier,
} from ".";
import type { DiagnosisResourceRef } from "../diagnoseEvidenceTypes";
import { Tooltip } from "../../ui/Tooltip";

export function OpenResourceButton({
  resourceRef,
  onOpenResource,
  compact = false,
}: {
  resourceRef?: DiagnosisResourceRef;
  onOpenResource?: (ref: DiagnosisResourceRef) => void;
  compact?: boolean;
}) {
  if (!resourceRef || !onOpenResource) return null;
  const identity = `${resourceRef.namespace ? `${resourceRef.namespace}/` : ""}${resourceRef.name}`;
  const label = `Open current ${displayKind(resourceRef.kind)} ${identity} in Radar`;
  return (
    <Tooltip
      content={label}
      delay={350}
      position="left"
      wrapperClassName="flex shrink-0"
    >
      <button
        type="button"
        aria-label={label}
        onClick={() => onOpenResource(resourceRef)}
        className={clsx(
          "flex h-7 shrink-0 items-center justify-center rounded text-theme-text-tertiary transition-colors hover:bg-theme-hover hover:text-accent-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent",
          "gap-1 px-2 text-xs font-medium",
        )}
      >
        {compact ? null : (
          <span>Open current {displayKind(resourceRef.kind)}</span>
        )}
        <SquareArrowOutUpRight className="h-3.5 w-3.5" aria-hidden />
      </button>
    </Tooltip>
  );
}

export function OpenTimelineButton({
  scope,
  onOpenTimeline,
}: {
  scope: InvestigationTimelineScope;
  onOpenTimeline?: (scope: InvestigationTimelineScope) => void;
}) {
  if (!onOpenTimeline) return null;
  const identity = `${scope.namespace ? `${scope.namespace}/` : ""}${scope.name}`;
  const label = `Open the Timeline for ${identity}`;
  return (
    <Tooltip
      content={label}
      delay={350}
      position="left"
      wrapperClassName="flex shrink-0"
    >
      <button
        type="button"
        aria-label={label}
        onClick={() => onOpenTimeline(scope)}
        className="flex h-7 shrink-0 items-center justify-center gap-1 rounded px-2 text-xs font-medium text-theme-text-tertiary transition-colors hover:bg-theme-hover hover:text-accent-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      >
        <FileClock className="h-3.5 w-3.5" aria-hidden />
        <span className="hidden @min-[540px]/card:inline">Timeline</span>
      </button>
    </Tooltip>
  );
}

export function uniquePrimarySources(
  group: InvestigationEvidenceGroup,
): InvestigationEvidenceSource[] {
  // One tool call can contribute repeated revisions to the same semantic group.
  // It still owns one navigation destination, and DOM ids must remain unique.
  const sources = new Map<string, InvestigationEvidenceSource>();
  for (const observation of group.observations) {
    const { source } = observation;
    if (source.primaryGroupId === group.id && !sources.has(source.id)) {
      sources.set(source.id, source);
    }
  }
  return [...sources.values()];
}

export type EvidenceDataOf<T extends InvestigationEvidenceData["type"]> =
  Extract<InvestigationEvidenceData, { type: T }>;

export function gitOpsValueSeverity(label: string, value: string) {
  const normalized = value.toLowerCase();
  if (
    normalized === "healthy" ||
    normalized === "synced" ||
    normalized === "succeeded" ||
    (label === "Ready" && normalized.startsWith("true"))
  ) {
    return "success" as const;
  }
  if (
    normalized === "degraded" ||
    normalized === "missing" ||
    normalized === "failed" ||
    normalized === "error" ||
    (label === "Ready" && normalized.startsWith("false"))
  ) {
    return "error" as const;
  }
  if (normalized === "outofsync") return "warning" as const;
  if (normalized === "progressing" || normalized === "running")
    return "info" as const;
  return "neutral" as const;
}

export function ResourceFact({
  label,
  value,
}: {
  label: string;
  value: unknown;
}) {
  if (value === undefined || value === null || value === "") return null;
  return (
    <div className="flex justify-between gap-2 @min-[560px]/evidence:block">
      <dt className="text-theme-text-tertiary">{label}</dt>
      <dd className="font-mono font-medium text-theme-text-primary">
        {String(value)}
      </dd>
    </div>
  );
}

export function conditionStatusTone(condition: {
  type: string;
  status: string;
}): "healthy" | "degraded" | "unhealthy" | "unknown" {
  switch (defaultConditionTone(condition)) {
    case "ok":
      return "healthy";
    case "warning":
      return "degraded";
    case "fail":
      return "unhealthy";
    case "unknown":
      return "unknown";
  }
}

export function EvidenceCaveat({ data }: { data: InvestigationEvidenceData }) {
  // Changes get a tooltip rather than a sentence because the wording depends
  // on whether the reported age was collected with the change.
  if (data.type === "changes") {
    return (
      <Tooltip
        content={`A change alone does not establish the cause.${data.changeContext?.when ? " The reported age is as of collection." : ""}`}
        position="left"
        className="pointer-events-none"
        wrapperClassName="flex shrink-0"
      >
        <button
          type="button"
          aria-label="About change evidence"
          className="flex h-7 w-7 items-center justify-center rounded-md text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
        >
          <Info className="h-3.5 w-3.5" aria-hidden />
        </button>
      </Tooltip>
    );
  }
  const text = EVIDENCE_KIND_TRAITS[data.type].caveat;
  if (!text) return null;
  return (
    <p className="flex items-start gap-1.5 text-xs leading-relaxed text-theme-text-tertiary">
      <Info className="mt-0.5 h-3 w-3 shrink-0" aria-hidden />
      {text}
    </p>
  );
}

export function EvidenceIcon({
  observation,
  prominence,
}: {
  observation: InvestigationEvidenceObservation;
  prominence: "primary" | "supporting" | "secondary";
}) {
  const Icon = evidenceIcon(observation.data.type);
  return (
    <span
      className={clsx(
        "flex shrink-0 items-center justify-center text-theme-text-tertiary",
        prominence === "primary" ? "h-7 w-7" : "h-6 w-6",
      )}
    >
      <Icon
        className={prominence === "primary" ? "h-4 w-4" : "h-3.5 w-3.5"}
        aria-hidden
      />
    </span>
  );
}

export function SourceButton({
  ariaLabel,
  buttonLabel = "View result",
  compact = false,
  onClick,
}: {
  ariaLabel?: string;
  buttonLabel?: string;
  compact?: boolean;
  onClick: () => void;
}) {
  const tooltip = "Open the original tool result";
  return (
    <Tooltip
      content={tooltip}
      delay={350}
      position="left"
      wrapperClassName="flex shrink-0"
    >
      <button
        type="button"
        aria-label={ariaLabel ?? tooltip}
        onClick={onClick}
        className={clsx(
          "flex h-7 shrink-0 items-center justify-center rounded-md text-theme-text-tertiary hover:bg-theme-hover hover:text-accent-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50",
          "gap-1 px-2 text-xs font-medium",
        )}
      >
        <FileSearch className="h-3.5 w-3.5" aria-hidden />
        <span
          className={compact ? "hidden @min-[540px]/card:inline" : undefined}
        >
          {buttonLabel}
        </span>
      </button>
    </Tooltip>
  );
}

function evidenceIcon(type: InvestigationEvidenceData["type"]) {
  return EVIDENCE_KIND_TRAITS[type].icon;
}

export function severityBadge(value: string) {
  const tone = value.toLowerCase();
  if (
    tone === "error" ||
    tone === "critical" ||
    tone === "failed" ||
    tone === "blocker"
  )
    return "error" as const;
  if (tone === "alert" || tone === "high") return "alert" as const;
  if (tone === "warning" || tone === "medium") return "warning" as const;
  if (tone === "info" || tone === "low" || tone === "review")
    return "info" as const;
  return "neutral" as const;
}

export function phaseLabel(
  phase: InvestigationEvidenceObservation["source"]["phase"],
): string {
  switch (phase) {
    case "initial":
      return "Initial";
    case "followup":
      return "Follow-up";
    case "verification":
      return "Verification";
    case "apply":
      return "Apply";
  }
}

export function toneBorder(
  tone: InvestigationEvidenceObservation["tone"],
  tier: InvestigationEvidenceTier,
  prominence: "primary" | "supporting" | "secondary",
): string {
  if (prominence !== "primary") return "border-theme-border/70";
  // A supporting adverse card is what the healthy-conflict banner points at,
  // so it must be tellable from its neutral neighbours: a thin rule, lighter
  // than the Key tier's.
  if (tier === "supporting" && tone === "error")
    return "border-l-2 border-l-semantic-error border-theme-border";
  if (tier === "supporting" && (tone === "warning" || tone === "alert"))
    return "border-l-2 border-l-semantic-warning border-theme-border";
  if (tier !== "key") return "border-theme-border";
  if (tone === "error")
    return "border-l-[3px] border-l-red-500 border-theme-border";
  if (tone === "alert")
    return "border-l-[3px] border-l-orange-500 border-theme-border";
  return "border-l-[3px] border-l-amber-500 border-theme-border";
}
