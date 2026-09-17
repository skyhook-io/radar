import type {
  DiagnosisEvidenceRole,
  DiagnosisEvidenceSubject,
} from "../../../api/diagnose";
import { namedInventoryRows } from "./bodies/inventory";
import { stripAnsi } from "@skyhook-io/k8s-ui";
import type {
  InvestigationEvidenceObservation,
  InvestigationPostureFinding,
  InvestigationRankingRow,
  InvestigationResourceSummary,
} from "./types";

/** How many selected log lines a card carries; the rest wait in the record. */
export const VISIBLE_LOG_EVIDENCE_LINES = 12;

/**
 * What a card placed in the story shows inline, under its header, before the
 * fold. The citation decides it: the lines Radar selected, the row a subject
 * names, the workload's own row in a ranking, the findings on it, or the
 * chart when the card is the cause or the symptom. Nothing here is the
 * agent's text; every excerpt is a slice of Radar's record.
 */
export type StoryExcerpt =
  | { kind: "lines"; head: string[]; rest: string[] }
  | { kind: "rows"; entries: ReturnType<typeof namedInventoryRows> }
  | { kind: "ranking"; rows: InvestigationRankingRow[] }
  | { kind: "posture"; findings: InvestigationPostureFinding[] }
  | { kind: "chart" };

export interface StoryExcerptItem {
  role?: DiagnosisEvidenceRole;
  subject?: DiagnosisEvidenceSubject;
}

export function storyExcerpt(
  observation: InvestigationEvidenceObservation,
  items: readonly StoryExcerptItem[],
  options: { compact: boolean; scopeNamespace?: string },
): StoryExcerpt | undefined {
  const data = observation.data;
  // The lines are the card: a compact log card keeps them.
  if (data.type === "logs") {
    const lines = (data.logs?.lines ?? [])
      .slice(-VISIBLE_LOG_EVIDENCE_LINES)
      .map((line) => stripAnsi(line));
    if (lines.length === 0) return undefined;
    return { kind: "lines", head: lines.slice(-2), rest: lines.slice(0, -2) };
  }
  if (options.compact) return undefined;
  const named = (name: string) =>
    items.some((item) => item.subject?.name === name);
  switch (data.type) {
    case "metrics":
      // A chart placed as the cause or the symptom is the card.
      return items.some(
        (item) => item.role === "cause" || item.role === "symptom",
      )
        ? { kind: "chart" }
        : undefined;
    case "inventory": {
      const entries = namedInventoryRows(
        data.resources as InvestigationResourceSummary[],
        items,
        options.scopeNamespace,
      );
      return entries.length > 0 ? { kind: "rows", entries } : undefined;
    }
    case "ranking": {
      const rows = data.rows
        .filter((row) => row.target || named(row.name))
        .slice(0, 3);
      return rows.length > 0 ? { kind: "ranking", rows } : undefined;
    }
    case "posture": {
      const findings = data.findings
        .filter((finding) => finding.target || named(finding.name))
        .slice(0, 3);
      return findings.length > 0 ? { kind: "posture", findings } : undefined;
    }
    default:
      return undefined;
  }
}
