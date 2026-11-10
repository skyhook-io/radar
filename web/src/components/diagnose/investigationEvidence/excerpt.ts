import { sameKind } from "./observations";
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
 * chart. The role the agent gave the card never hides any of it; it only
 * sizes a chart, full height for what the story rests on and short for the
 * rest. A compact marker starts a chart folded behind the card's chevron,
 * except one the story rests on. Nothing here is the agent's text; every
 * excerpt is a slice of Radar's record.
 */
export type StoryExcerpt =
  | { kind: "lines"; head: string[]; rest: string[] }
  | { kind: "rows"; entries: ReturnType<typeof namedInventoryRows> }
  | { kind: "ranking"; rows: InvestigationRankingRow[] }
  | { kind: "posture"; findings: InvestigationPostureFinding[] }
  | { kind: "chart"; size: "full" | "short"; folded: boolean };

export interface StoryExcerptItem {
  role?: DiagnosisEvidenceRole;
  subject?: DiagnosisEvidenceSubject;
}

export function storyExcerpt(
  observation: InvestigationEvidenceObservation,
  items: readonly StoryExcerptItem[],
  options: { compact: boolean; preview?: boolean; scopeNamespace?: string },
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
  // So is the chart: a compact card drops the agent's note, not Radar's
  // record. Compact folds the chart, but the agent marks cards compact for
  // the prose's flow, not their weight, so a cause or symptom stays open.
  // Only the collapsed preview, which bounds its height, leaves it out.
  if (data.type === "metrics") {
    if (options.preview) return undefined;
    const rests = items.some(
      (item) => item.role === "cause" || item.role === "symptom",
    );
    return {
      kind: "chart",
      size: rests ? "full" : "short",
      folded: options.compact && !rests,
    };
  }
  if (options.compact) return undefined;
  const named = (row: { kind: string; namespace?: string; name: string }) =>
    items.some(
      (item) =>
        item.subject?.name === row.name &&
        sameKind(item.subject.kind, row.kind) &&
        (!item.subject.namespace ||
          row.namespace === undefined ||
          item.subject.namespace === row.namespace),
    );
  switch (data.type) {
    case "inventory": {
      const entries = namedInventoryRows(
        data.resources as InvestigationResourceSummary[],
        items,
        options.scopeNamespace,
        data.namespacesRead,
        data.namespacesReadOmitted,
      );
      return entries.length > 0 ? { kind: "rows", entries } : undefined;
    }
    case "ranking": {
      const rows = data.rows
        .filter((row) => row.target || named(row))
        .slice(0, 3);
      return rows.length > 0 ? { kind: "ranking", rows } : undefined;
    }
    case "posture": {
      const findings = data.findings
        .filter((finding) => finding.target || named(finding))
        .slice(0, 3);
      return findings.length > 0 ? { kind: "posture", findings } : undefined;
    }
    default:
      return undefined;
  }
}
