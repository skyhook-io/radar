import {
  EVIDENCE_KIND_TRAITS,
  evidenceKindIsFocused,
} from "./investigationEvidenceKinds";
import { evidenceDisplaySnapshot } from "./investigationEvidencePresentation";
import {
  investigationSourceArgs,
  type InvestigationEvidenceData,
  type InvestigationEvidenceGroup,
  type InvestigationEvidenceObservation,
  type InvestigationEvidenceProjection,
  type InvestigationRootCauseEvidenceResolution,
  type InvestigationEvidenceSource,
} from "./investigationEvidence";
import type {
  InvestigationCaseItem,
  InvestigationCaseResolution,
} from "./investigationCase";
import type { DiagnosisEvidenceRole } from "../../api/diagnose";

/** Item identity for the excluded-hypothesis lookup; index alone repeats across turns. */
export function investigationCaseItemKey(item: InvestigationCaseItem): string {
  return `${item.source.id}\u0000${item.index}`;
}

const EVIDENCE_ROLE_RANK: Readonly<Record<DiagnosisEvidenceRole, number>> = {
  cause: 0,
  symptom: 1,
  // A ruled-out card keeps Radar's place in the list; the role only labels it.
  rules_out: 2,
  context: 3,
  benign: 4,
  demoted: 5,
};

const UNLABELLED_RANK = 2;

function sourceNamesOneResource(source: InvestigationEvidenceSource): boolean {
  const args = investigationSourceArgs(source);
  return (
    typeof args?.kind === "string" &&
    args.kind !== "" &&
    typeof args.name === "string" &&
    args.name !== ""
  );
}

export function investigationCaseByGroup(
  investigationCase?: InvestigationCaseResolution,
): Map<string, InvestigationCaseItem[]> {
  const byGroup = new Map<string, InvestigationCaseItem[]>();
  for (const item of investigationCase?.items ?? []) {
    if (!item.groupId || item.placement === "source") continue;
    const items = byGroup.get(item.groupId) ?? [];
    items.push(item);
    byGroup.set(item.groupId, items);
  }
  return byGroup;
}

export function evidenceTypePrefersFullRow(
  type: InvestigationEvidenceData["type"],
): boolean {
  return EVIDENCE_KIND_TRAITS[type].fullRow;
}

// Supporting evidence becomes a two-column grid when the pane is wide enough.
// Keep compact cards paired only with an adjacent compact card. Otherwise a
// full-row card between them strands a conspicuous empty half-row (and makes the
// visual order look accidental), as does an odd card at the end of a run.
export function investigationEvidenceFullRowFlags(
  types: readonly InvestigationEvidenceData["type"][],
): boolean[] {
  const fullRow = types.map(evidenceTypePrefersFullRow);
  let compactRunStart = 0;

  for (let index = 0; index <= types.length; index += 1) {
    if (index < types.length && !fullRow[index]) continue;
    const compactRunLength = index - compactRunStart;
    if (compactRunLength % 2 === 1) fullRow[index - 1] = true;
    compactRunStart = index + 1;
  }

  return fullRow;
}

type EvidenceCollection = "main" | "workload" | "earlier";

export function partitionInvestigationEvidence(
  groups: InvestigationEvidenceGroup[],
  resolution?: InvestigationRootCauseEvidenceResolution,
  investigationCase?: InvestigationCaseResolution,
  /**
   * Under a story every captured result about the target belongs in the one
   * inventory the reader opens to see what Radar recorded; nothing about the
   * target is folded into a second tier. Broader results stay cited-only.
   */
  inventory = false,
) {
  // Selection: legacy root-cause links plus every placed agent item, of any
  // role (placement promotes a group into main the way a citation does; the
  // role does not matter here). Ordering below is the only place roles act,
  // and nothing the agent sends can remove a group from a collection.
  const caseByGroup = investigationCaseByGroup(investigationCase);
  const selected = new Set([
    ...(resolution?.status === "linked"
      ? resolution.links.map((link) => link.originalGroupId)
      : []),
    ...caseByGroup.keys(),
  ]);
  const collections: Record<EvidenceCollection, InvestigationEvidenceGroup[]> =
    {
      main: [],
      workload: [],
      earlier: [],
    };
  const collectionByGroup = new Map<string, EvidenceCollection>();
  const adverse = (group: InvestigationEvidenceGroup) =>
    ["error", "alert", "warning"].includes(group.latest.tone);
  // Broader cards are facts about something other than the target. They are
  // withheld unless cited, and the pane says how many were withheld so a
  // reader knows the agent looked at things it did not build its case on.
  let hiddenBroader = 0;
  // Broader metrics are facts about something other than the target. They are
  // withheld unless cited, and the pane says how many were withheld so a
  // reader knows the agent ran queries it did not build its case on.
  let hiddenMetrics = 0;
  for (const group of groups) {
    const broader = group.latest.relevance === "broader";
    // Citations select tool results, not individual rows in a broad search.
    // Only a focused, unambiguous fact can be promoted by a source citation.
    // An agent item names this observation when its subject does, or when the
    // call it cites was itself scoped to one resource; a subject-less item on
    // a broad query only proves the query returned one row and is gated like
    // a citation of its source.
    const namedByAgent = (caseByGroup.get(group.id) ?? []).some(
      (item) => item.subject || sourceNamesOneResource(item.source),
    );
    if (broader && !namedByAgent) {
      const citingSource =
        resolution?.links.find((item) => item.originalGroupId === group.id)
          ?.source ?? caseByGroup.get(group.id)?.[0]?.source;
      const focused = evidenceKindIsFocused(group.latest.data.type);
      const sourceGroups = citingSource
        ? groups.filter(
            (candidate) =>
              evidenceKindIsFocused(candidate.latest.data.type) &&
              candidate.observations.some(
                (observation) => observation.source.id === citingSource.id,
              ),
          )
        : [];
      if (!selected.has(group.id) || !focused || sourceGroups.length !== 1) {
        // The two counts head separate lines in the pane, so they have to be
        // disjoint: a withheld chart announced by both would read as two
        // withheld results.
        if (!group.historical) {
          if (group.latest.data.type === "metrics") hiddenMetrics += 1;
          else hiddenBroader += 1;
        }
        continue;
      }
    }
    const main =
      !group.historical &&
      (selected.has(group.id) ||
        (!broader &&
          (inventory ||
            group.latest.tier === "key" ||
            (group.latest.tier === "supporting" && adverse(group)))));
    const collection = main
      ? "main"
      : group.historical
        ? "earlier"
        : "workload";
    collections[collection].push(group);
    collectionByGroup.set(group.id, collection);
  }
  // A group takes its strongest role (lowest rank); within one role the
  // agent's own item order decides. Unlabelled and ruled-out groups keep
  // Radar's order among themselves.
  const roleOrder = (group: InvestigationEvidenceGroup) => {
    let rank: number | undefined;
    let itemIndex = Number.POSITIVE_INFINITY;
    for (const item of caseByGroup.get(group.id) ?? []) {
      const itemRank = EVIDENCE_ROLE_RANK[item.role];
      if (rank === undefined || itemRank < rank) {
        rank = itemRank;
        itemIndex = item.index;
      } else if (itemRank === rank) {
        itemIndex = Math.min(itemIndex, item.index);
      }
    }
    if (rank === undefined || rank === UNLABELLED_RANK) {
      return { rank: UNLABELLED_RANK, itemIndex: Number.POSITIVE_INFINITY };
    }
    return { rank, itemIndex };
  };
  collections.main.sort((left, right) => {
    const leftRole = roleOrder(left);
    const rightRole = roleOrder(right);
    const agentOrder =
      Number.isFinite(leftRole.itemIndex) ||
      Number.isFinite(rightRole.itemIndex)
        ? leftRole.itemIndex - rightRole.itemIndex
        : 0;
    return (
      leftRole.rank - rightRole.rank ||
      agentOrder ||
      Number(right.latest.tier === "key") -
        Number(left.latest.tier === "key") ||
      Number(adverse(right)) - Number(adverse(left)) ||
      left.firstOrder - right.firstOrder
    );
  });
  // A healthy workload's collection opens with several "nothing here"
  // receipts; the cards that carry facts (its vitals above all) come first,
  // and the receipts keep their order among themselves.
  collections.workload.sort(
    (left, right) =>
      Number(left.kind === "receipt") - Number(right.kind === "receipt"),
  );
  return { ...collections, collectionByGroup, hiddenBroader, hiddenMetrics };
}

export function investigationEvidenceRevealCollection(
  projection: InvestigationEvidenceProjection,
  sourceId: string,
  partition = partitionInvestigationEvidence(projection.groups),
): Exclude<EvidenceCollection, "main"> | "coverage" | undefined {
  const source = projection.sources.find((item) => item.id === sourceId);
  const collection = source?.primaryGroupId
    ? partition.collectionByGroup.get(source.primaryGroupId)
    : undefined;
  if (collection) return collection === "main" ? undefined : collection;
  if (
    projection.limitations.some((limitation) =>
      limitation.sources.some((source) => source.id === sourceId),
    )
  )
    return "coverage";
  return undefined;
}

export function previousDifferentObservations(
  group: InvestigationEvidenceGroup,
  citedOrder?: number,
  boundOrders: ReadonlySet<number> = new Set(),
) {
  const seen = new Set([evidenceDisplaySnapshot(group.latest)]);
  const pinned = (observation: InvestigationEvidenceObservation) =>
    observation.source.order === citedOrder ||
    boundOrders.has(observation.source.order);
  return [...group.observations].reverse().filter((observation) => {
    if (observation === group.latest) return false;
    // An observation bound to an agent item always keeps its own row: display
    // equivalence is a folding rule for unpinned rechecks, never a reason to
    // move a claim onto different content.
    if (boundOrders.has(observation.source.order)) return true;
    // A pinned observation wins over an unpinned twin with the same display
    // content.
    if (
      !pinned(observation) &&
      group.observations.some(
        (other) =>
          pinned(other) &&
          evidenceDisplaySnapshot(other) ===
            evidenceDisplaySnapshot(observation),
      )
    )
      return false;
    const snapshot = evidenceDisplaySnapshot(observation);
    if (seen.has(snapshot)) return false;
    seen.add(snapshot);
    return true;
  });
}

export function investigationEvidenceShouldRevealHistory(
  group: InvestigationEvidenceGroup,
  sourceId?: string,
  /** Sources whose observation carries an agent item and so always has a row. */
  boundSourceIds: ReadonlySet<string> = new Set(),
): boolean {
  return (
    Boolean(sourceId) &&
    group.latest.source.id !== sourceId &&
    group.observations.some(
      (observation) =>
        observation.source.id === sourceId &&
        (boundSourceIds.has(observation.source.id) ||
          evidenceDisplaySnapshot(observation) !==
            evidenceDisplaySnapshot(group.latest)),
    )
  );
}
