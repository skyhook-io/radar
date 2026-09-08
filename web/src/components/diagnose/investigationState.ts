// Pure presentation decisions over the durable transcript and evidence projection.
// Keep React/DOM orchestration in InvestigationView; these rules have no UI runtime.
import { evidenceKindIsAdverse } from "./investigationEvidenceKinds";
import {
  DiagnoseError,
  type DiagnoseStreamEvent,
  type RunSummary,
} from "../../api/diagnose";
import type { Turn } from "./parts";
import type {
  InvestigationEvidenceProjection,
  InvestigationEvidenceTurn,
} from "./investigationEvidence";

export function initialInvestigationPane(
  status: RunSummary["status"],
): "activity" | "evidence" {
  return status === "done" || status === "stale" ? "evidence" : "activity";
}

export function investigationEvidenceShouldMarkUnread({
  hasNewLiveSource,
  selectedPane,
  evidencePaneVisible,
}: {
  hasNewLiveSource: boolean;
  selectedPane: "activity" | "evidence";
  evidencePaneVisible: boolean;
}): boolean {
  return (
    hasNewLiveSource && selectedPane === "activity" && !evidencePaneVisible
  );
}

export function investigationEvidenceAnnouncement({
  unreadEvidence,
  evidenceUpdateAvailable,
}: {
  unreadEvidence: boolean;
  evidenceUpdateAvailable: boolean;
}): string {
  if (unreadEvidence) return "New evidence available";
  if (evidenceUpdateAvailable) {
    return "New evidence available in Findings.";
  }
  return "";
}

export function investigationIsReadOnly(
  status: RunSummary["status"],
  gone: boolean,
): boolean {
  return status === "stale" || gone;
}

export function investigationInteractionsBlocked(input: {
  streamReady: boolean;
  busy: boolean;
  requestPending: boolean;
  readOnly: boolean;
  verificationPending: boolean;
}): boolean {
  return (
    !input.streamReady ||
    input.busy ||
    input.requestPending ||
    input.readOnly ||
    input.verificationPending
  );
}

export function canOfferInvestigationApply(input: {
  currentAssessmentIdx: number;
  lastRemediationIdx: number;
  lastApplyAttemptIdx: number;
  localApplyAttemptAssessmentIdx: number;
  interactionsBlocked: boolean;
  hosted: boolean;
  hasNewerEvidence: boolean;
}): boolean {
  return (
    input.currentAssessmentIdx === input.lastRemediationIdx &&
    input.currentAssessmentIdx >
      Math.max(
        input.lastApplyAttemptIdx,
        input.localApplyAttemptAssessmentIdx,
      ) &&
    !input.interactionsBlocked &&
    !input.hosted &&
    !input.hasNewerEvidence
  );
}

export function investigationApplyAttemptVerified(input: {
  localApplyAttemptAssessmentIdx: number;
  currentAssessmentIdx: number;
  currentAssessmentIsVerification: boolean;
}): boolean {
  return (
    input.localApplyAttemptAssessmentIdx >= 0 &&
    input.currentAssessmentIdx > input.localApplyAttemptAssessmentIdx &&
    input.currentAssessmentIsVerification
  );
}

export function investigationAssessmentNeedsCurrentStateVerification(input: {
  currentAssessmentIdx: number;
  lastApplyAttemptIdx: number;
  lastApplyOutcome: Turn["applyOutcome"];
  localApplyAttemptAssessmentIdx: number;
}): boolean {
  if (input.currentAssessmentIdx < 0) return false;

  // Once the durable apply turn exists it is authoritative for the local
  // pessimistic marker. A producer-confirmed failure means no write occurred;
  // every other outcome (including a running/missing outcome) leaves the
  // pre-write assessment unsafe to present as current.
  if (input.lastApplyAttemptIdx > input.currentAssessmentIdx) {
    return input.lastApplyOutcome !== "failed";
  }

  return input.localApplyAttemptAssessmentIdx >= input.currentAssessmentIdx;
}

export function investigationApplyRejectionIsDefinitive(
  error: unknown,
): error is DiagnoseError {
  return error instanceof DiagnoseError && error.status < 500;
}

export function investigationApplyCompletionEffects(input: {
  live: boolean;
  applyStartedLive: boolean;
  stale: boolean;
}): {
  refreshClusterState: boolean;
  verificationPending: boolean;
} {
  const belongsToThisLiveView = input.live || input.applyStartedLive;
  return {
    refreshClusterState: belongsToThisLiveView,
    verificationPending: belongsToThisLiveView && !input.stale,
  };
}

export function investigationTurnWithTerminalEvent(
  turn: Turn,
  event: Pick<
    DiagnoseStreamEvent,
    "type" | "diagnosis" | "error" | "applyOutcome"
  >,
  animateResult: boolean,
): Turn {
  if (event.type === "done") {
    return {
      ...turn,
      diagnosis: event.diagnosis ?? null,
      error: null,
      status: "done",
      applyOutcome: event.applyOutcome,
      animateResult,
    };
  }
  if (event.type === "error" && turn.status === "running") {
    return {
      ...turn,
      error: event.error || "The investigation failed.",
      status: "error",
      applyOutcome: event.applyOutcome,
      animateResult,
    };
  }
  return turn;
}

export function investigationApplyTerminalNeedsClusterRefresh(input: {
  localApplyRequestPending: boolean;
  streamedApplyPending: boolean;
  streamedApplyStartedLive: boolean;
  terminalEventIsLive: boolean;
}): boolean {
  return (
    input.localApplyRequestPending ||
    (input.streamedApplyPending &&
      (input.streamedApplyStartedLive || input.terminalEventIsLive))
  );
}

export function investigationClosedEventIsLive(input: {
  reason: "run_closed" | "unavailable";
  subscribedRunStatus: RunSummary["status"];
  replayComplete: boolean;
}): boolean {
  // A retained stale run replays `replay_complete` immediately before its
  // durable `closed` sentinel. The replay flag is therefore already true when
  // that historical close arrives; the status captured when this subscription
  // opened is what distinguishes it from a run that closed while being watched.
  if (input.reason === "run_closed") {
    return input.subscribedRunStatus !== "stale";
  }
  return input.replayComplete;
}

export function investigationClosedRunIsUnavailable(input: {
  reason: "run_closed" | "unavailable";
  subscribedRunStatus: RunSummary["status"];
}): boolean {
  if (input.reason === "unavailable") return true;
  // A running stream is finalized only when its cluster context changes; its
  // refreshed summary will be stale, not gone. Retained stale streams likewise
  // end with their durable closed sentinel. Other terminal runs can close only
  // after retention eviction, so those are genuinely unavailable.
  return !["running", "stale"].includes(input.subscribedRunStatus);
}

function isCompletedEvidenceTool(
  item: InvestigationEvidenceTurn["timeline"][number],
) {
  return item.kind === "tool" && item.status === "done";
}

/**
 * Reasoning reveal replaces a Turn every 150 ms, but completed tool records are
 * immutable. Compare only the fields the evidence projector consumes so those
 * cosmetic transcript updates do not repeatedly parse every retained payload.
 */
export function investigationEvidenceInputsEqual(
  previous: readonly InvestigationEvidenceTurn[],
  next: readonly InvestigationEvidenceTurn[],
): boolean {
  if (previous.length !== next.length) return false;
  for (let turnIndex = 0; turnIndex < previous.length; turnIndex += 1) {
    const a = previous[turnIndex];
    const b = next[turnIndex];
    if (
      a.question !== b.question ||
      a.apply !== b.apply ||
      a.verify !== b.verify ||
      a.status !== b.status
    ) {
      return false;
    }

    let aIndex = 0;
    let bIndex = 0;
    while (true) {
      while (
        aIndex < a.timeline.length &&
        !isCompletedEvidenceTool(a.timeline[aIndex])
      ) {
        aIndex += 1;
      }
      while (
        bIndex < b.timeline.length &&
        !isCompletedEvidenceTool(b.timeline[bIndex])
      ) {
        bIndex += 1;
      }
      const aDone = aIndex >= a.timeline.length;
      const bDone = bIndex >= b.timeline.length;
      if (aDone || bDone) {
        if (aDone !== bDone) return false;
        break;
      }
      if (aIndex !== bIndex || a.timeline[aIndex] !== b.timeline[bIndex]) {
        return false;
      }
      aIndex += 1;
      bIndex += 1;
    }
  }
  return true;
}

/**
 * The turn whose agent case annotates the Evidence pane. A follow-up answer
 * that cites evidence ("chart this and cite it") must reach Findings, so the
 * newest completed non-apply, non-explanation turn carrying a bound case or
 * linked root-cause refs wins; earlier turns keep their case read-only.
 */
export function investigationLiveCaseTurnIndex(
  turns: readonly Pick<
    Turn,
    "status" | "apply" | "explainAssessment" | "diagnosis"
  >[],
  currentAssessmentIdx: number,
): number {
  for (let index = turns.length - 1; index >= 0; index -= 1) {
    const turn = turns[index];
    if (turn.status !== "done" || turn.apply || turn.explainAssessment)
      continue;
    const diagnosis = turn.diagnosis;
    if (!diagnosis) continue;
    if (
      diagnosis.evidence?.some((item) => item.status === "linked") ||
      (diagnosis.rootCause && diagnosis.rootCauseEvidence?.status === "linked")
    ) {
      return Math.max(index, currentAssessmentIdx);
    }
  }
  return currentAssessmentIdx;
}

export function investigationEvidenceCoverageLimited(
  projection: Pick<
    InvestigationEvidenceProjection,
    "limitations" | "coverage"
  > & {
    sources: readonly {
      id: string;
      tool: string;
      confirmedSuccess: boolean;
    }[];
    groups: readonly {
      latest: {
        relevance: "target" | "producer-related" | "broader";
        source: { id: string };
      };
    }[];
  },
): boolean {
  const completeDiagnosisSourceIds = new Set(
    projection.sources
      .filter((source) => source.tool === "diagnose" && source.confirmedSuccess)
      .map((source) => source.id),
  );
  const hasTargetDiagnosis = projection.groups.some(
    (group) =>
      group.latest.relevance !== "broader" &&
      completeDiagnosisSourceIds.has(group.latest.source.id),
  );
  return (
    projection.limitations.length > 0 ||
    projection.coverage.projected === 0 ||
    !hasTargetDiagnosis
  );
}

/**
 * A model-authored all-clear must not overrule active adverse Radar evidence.
 * Context-only warnings (for example a Helm ownership advisory) and ordinary
 * recent changes are deliberately excluded: they are useful context, not proof
 * that the investigated resource is unhealthy.
 */
interface HealthConflictGroup {
  id?: string;
  historical: boolean;
  kind: string;
  latest: {
    relevance: "target" | "producer-related" | "broader";
    tier: "key" | "supporting" | "context" | "checked";
    tone: string;
    title?: string;
  };
}

export function investigationHealthConflictGroups<
  G extends HealthConflictGroup,
>(projection: { groups: readonly G[] }): G[] {
  return projection.groups.filter(
    (group) =>
      !group.historical &&
      group.latest.relevance !== "broader" &&
      (group.latest.tier === "key" || group.latest.tier === "supporting") &&
      // Radar's adverse tones run warning → alert → error; "high" severity
      // from the Go side lands on alert, so leaving it out silently excused
      // every high-severity finding.
      (group.latest.tone === "warning" ||
        group.latest.tone === "alert" ||
        group.latest.tone === "error") &&
      evidenceKindIsAdverse(group.kind),
  );
}

export function investigationEvidenceConflictsWithHealthy(projection: {
  groups: readonly HealthConflictGroup[];
}): boolean {
  return investigationHealthConflictGroups(projection).length > 0;
}

/**
 * The banner over a healthy verdict points at adverse Radar evidence. When the
 * agent placed a "less relevant" or "rules out" note on every such card, the
 * evidence is still shown but the reader has the agent's reason next to it,
 * so the banner can say that instead of accusing evidence the agent addressed.
 * Returns the titles of the explained cards, or null when any conflict is
 * unexplained (or there is no conflict).
 */
export function investigationHealthConflictExplainedBy(
  projection: { groups: readonly HealthConflictGroup[] },
  caseItems:
    | readonly {
        role: string;
        claim?: string;
        placement: "card" | "revision" | "source";
        groupId?: string;
        carried?: boolean;
      }[]
    | undefined,
): string[] | null {
  const conflicting = investigationHealthConflictGroups(projection);
  if (conflicting.length === 0 || !caseItems) return null;
  const titles: string[] = [];
  for (const group of conflicting) {
    const explained = caseItems.some(
      (item) =>
        // The note has to be on the card as it stands now and has to say
        // something: a note on a superseded revision of the card, or an empty
        // claim, leaves the reader nothing to read while quietly softening
        // the banner. The caller passes the displayed assessment's own items,
        // so a different turn's note cannot explain this verdict either.
        item.placement === "card" &&
        (item.claim ?? "").trim() !== "" &&
        item.groupId === group.id &&
        (item.role === "demoted" || item.role === "rules_out"),
    );
    if (!explained) return null;
    titles.push(group.latest.title ?? group.kind);
  }
  return titles;
}

export function investigationEndedBeforeConclusion(
  status: RunSummary["status"],
  lastTurn: Pick<Turn, "status" | "apply" | "explainAssessment"> | undefined,
): boolean {
  return (
    (status === "error" || status === "stopped") &&
    (lastTurn === undefined ||
      (lastTurn.status === "error" &&
        lastTurn.apply !== true &&
        !lastTurn.explainAssessment))
  );
}

export interface InvestigationHistoryUnavailableState {
  error: string;
  retryable: boolean;
}

export function investigationHistoryUnavailablePresentation(
  state: InvestigationHistoryUnavailableState,
): { title: string; detail: string; loading: boolean } {
  if (state.retryable) {
    const error = state.error.trim();
    return {
      title: "Saved history is temporarily unavailable",
      detail: error
        ? `${error}${/[.!?]$/.test(error) ? " " : ". "}Radar is retrying without discarding this run.`
        : "Radar is retrying without discarding this run.",
      loading: true,
    };
  }
  return {
    title: "Saved history is unavailable",
    detail:
      state.error || "Radar could not restore this run from its saved history.",
    loading: false,
  };
}

export function investigationPaneCenteredScrollTop({
  scrollTop,
  viewportHeight,
  contentHeight,
  targetTop,
  targetHeight,
}: {
  scrollTop: number;
  viewportHeight: number;
  contentHeight: number;
  /** Target top relative to the scroll viewport, before this adjustment. */
  targetTop: number;
  targetHeight: number;
}): number {
  const centered =
    scrollTop + targetTop - Math.max(0, (viewportHeight - targetHeight) / 2);
  return Math.max(0, Math.min(centered, contentHeight - viewportHeight));
}

export function canStopInvestigation(
  run: RunSummary,
  busy: boolean,
  gone: boolean,
  latestTurnStatus?: Turn["status"],
): boolean {
  // The transcript is fresher than the polled run summary. Once it has a
  // terminal frame, a lagging/failed summary refresh must not resurrect Stop.
  const transcriptTerminal =
    latestTurnStatus === "done" || latestTurnStatus === "error";
  return (
    run.trigger !== "background" &&
    run.status !== "stale" &&
    run.status !== "stopping" &&
    !gone &&
    !transcriptTerminal &&
    (busy || run.status === "running")
  );
}

export function canContinueInvestigation(
  run: RunSummary,
  latestTurnStatus?: Turn["status"],
  gone = false,
): boolean {
  const transcriptTerminal =
    latestTurnStatus === "done" || latestTurnStatus === "error";
  const summaryIsLaggingTerminalTranscript =
    run.status === "running" &&
    run.trigger !== "background" &&
    transcriptTerminal;
  return (
    !gone &&
    run.status !== "stale" &&
    run.status !== "stopping" &&
    (run.canContinue !== false || summaryIsLaggingTerminalTranscript)
  );
}

export function canInvestigateFurther(run: RunSummary, gone = false): boolean {
  return (
    !gone &&
    run.trigger === "background" &&
    run.status === "done" &&
    !!run.issueId
  );
}
