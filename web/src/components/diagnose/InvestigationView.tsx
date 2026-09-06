// A view over one durable, server-side investigation run. It SUBSCRIBES to the
// run's event stream (replay + live) and reconstructs the transcript; it does not
// own the run's lifetime — the server does. So closing the panel or navigating
// away just unsubscribes; the run keeps going and re-subscribing replays it.
import {
  Fragment,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Badge, Collapse, CollapseChevron } from "@skyhook-io/k8s-ui";
import {
  Send,
  AlertTriangle,
  ArrowDown,
  ArrowRight,
  Activity,
  CheckCircle2,
  Files,
  Loader2,
} from "lucide-react";
import {
  subscribeRun,
  addTurn,
  stopRun,
  DiagnoseError,
  type Diagnosis,
  type DiagnoseStreamEvent,
  type RunSummary,
} from "../../api/diagnose";
import { useDiagnose } from "./DiagnoseContext";
import {
  TurnView,
  ResultCard,
  ApplyDialog,
  appendThinking,
  upsertTool,
  type Turn,
} from "./parts";
import {
  investigationActivitySourceDomId,
  investigationEvidenceStepIdsByTurn,
  investigationEvidenceSourceDomId,
  projectInvestigationEvidence,
  resolveInvestigationRootCauseEvidence,
  type InvestigationEvidenceProjection,
  type InvestigationEvidenceTurn,
} from "./investigationEvidence";
import {
  INVESTIGATION_DISCLOSURE_SETTLE_MS,
  InvestigationEvidencePane,
} from "./InvestigationEvidencePane";
import type { DiagnosisResourceRef } from "./diagnoseEvidenceTypes";
import { formatInvestigationTarget } from "./target";

const RECHECK_QUESTION =
  "Did the fix resolve the issue? Re-check the resource's current status and health now, and say whether it's healthy.";

function prefersReducedMotion(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

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
}): boolean {
  return (
    input.currentAssessmentIdx === input.lastRemediationIdx &&
    input.currentAssessmentIdx >
      Math.max(
        input.lastApplyAttemptIdx,
        input.localApplyAttemptAssessmentIdx,
      ) &&
    !input.interactionsBlocked &&
    !input.hosted
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

const HEALTH_CONFLICT_EVIDENCE_KINDS = new Set([
  "issue",
  "startup",
  "crash",
  "resource",
  "logs",
  "events",
  "dns",
  "network",
]);

/**
 * A model-authored all-clear must not overrule active adverse Radar evidence.
 * Context-only warnings (for example a Helm ownership advisory) and ordinary
 * recent changes are deliberately excluded: they are useful context, not proof
 * that the investigated resource is unhealthy.
 */
export function investigationEvidenceConflictsWithHealthy(projection: {
  groups: readonly {
    historical: boolean;
    kind: string;
    latest: {
      relevance: "target" | "producer-related" | "broader";
      tier: "key" | "supporting" | "context" | "checked";
      tone: string;
    };
  }[];
}): boolean {
  return projection.groups.some(
    (group) =>
      !group.historical &&
      group.latest.relevance !== "broader" &&
      (group.latest.tier === "key" || group.latest.tier === "supporting") &&
      (group.latest.tone === "warning" || group.latest.tone === "error") &&
      HEALTH_CONFLICT_EVIDENCE_KINDS.has(group.kind),
  );
}

export function investigationEndedBeforeConclusion(
  status: RunSummary["status"],
  lastTurn: Pick<Turn, "status" | "apply"> | undefined,
): boolean {
  return (
    (status === "error" || status === "stopped") &&
    (lastTurn === undefined ||
      (lastTurn.status === "error" && lastTurn.apply !== true))
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

export function InvestigationStartErrorAlert({
  error,
  onDismiss,
}: {
  error: string;
  onDismiss: () => void;
}) {
  return (
    <div
      role="alert"
      className="flex items-start gap-2 border-b border-red-500/30 bg-red-500/10 px-3 py-2.5 text-sm text-theme-text-primary"
    >
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-red-400" />
      <div className="min-w-0 flex-1">
        <div className="font-medium">
          Couldn&apos;t start a new investigation
        </div>
        <div className="text-theme-text-secondary">{error}</div>
      </div>
      <button
        type="button"
        onClick={onDismiss}
        className="shrink-0 rounded px-1.5 py-0.5 text-xs text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
      >
        Dismiss
      </button>
    </div>
  );
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

function captureEvidenceCardLayout(container: HTMLElement) {
  const containerTop = container.getBoundingClientRect().top;
  return new Map(
    Array.from(container.querySelectorAll<HTMLElement>("[data-evidence-card]"))
      .filter((card) => !!card.id)
      .map((card) => {
        const rect = card.getBoundingClientRect();
        return [
          card.id,
          {
            top: rect.top - containerTop + container.scrollTop,
            height: rect.height,
          },
        ] as const;
      }),
  );
}

export function InvestigationView({
  run,
  agentLabel,
  maximized,
  onOpenResource,
}: {
  run: RunSummary;
  agentLabel: string;
  maximized: boolean;
  /** Opens an unambiguous evidence subject in Radar's native resource views. */
  onOpenResource?: (ref: DiagnosisResourceRef) => void;
}) {
  const { kind, namespace, name } = run;
  // Apply is off for hosted agents (read-only server-side). Keyed on the selected
  // agent, which matches run.agent unless a deployment mixes hosted + local agents.
  const { refreshRuns, openInvestigation, startError, dismissError, hosted } =
    useDiagnose();
  // Investigate again means look again, so it asks for a new session explicitly and only
  // carries the issue forward — being handed the previous answer is the one
  // thing someone clicking this doesn't want.
  const retryDiagnosis = useCallback(
    () =>
      openInvestigation({
        kind,
        group: run.group,
        namespace,
        name,
        issueId: run.issueId,
        fresh: true,
      }),
    [openInvestigation, kind, namespace, name, run.group, run.issueId],
  );
  const queryClient = useQueryClient();
  const [turns, setTurns] = useState<Turn[]>([]);
  // The run is gone server-side (evicted past the retention cap, or lost on a
  // restart) — the stream 404s / closes with nothing to replay. Without this we'd
  // show a silent blank panel; instead we render a "no longer available" state.
  const [gone, setGone] = useState(false);
  const [busy, setBusy] = useState(false);
  const [requestPending, setRequestPending] = useState(false);
  const [streamReady, setStreamReady] = useState(false);
  const [historyUnavailable, setHistoryUnavailable] =
    useState<InvestigationHistoryUnavailableState | null>(null);
  const [input, setInput] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const [verificationError, setVerificationError] = useState<string | null>(
    null,
  );
  // Retire the assessment as soon as the operator confirms Apply, before the
  // HTTP request crosses the network. A lost response is ambiguous: the server
  // may have accepted and completed the write even though fetch rejected. Only
  // a later structured verification assessment makes that action eligible again.
  const [localApplyAttemptAssessmentIdx, setLocalApplyAttemptAssessmentIdx] =
    useState(-1);
  const [applyOutcomeUncertain, setApplyOutcomeUncertain] = useState<
    string | null
  >(null);
  // Covers the short event-stream hand-off after an apply succeeds and before
  // the server-owned verification turn arrives. This is presentation state only:
  // the durable server job, never the browser, schedules verification.
  const [verificationPending, setVerificationPending] = useState(false);
  // The panes are simultaneous above the workspace breakpoint and tabs below it.
  // Successful/stale history opens on its outcome; running and ended-early runs
  // open on Activity, where the user can immediately see what happened.
  const [narrowPane, setNarrowPane] = useState<"activity" | "evidence">(() =>
    initialInvestigationPane(run.status),
  );
  const [unreadEvidence, setUnreadEvidence] = useState(false);
  const [evidenceUpdateAvailable, setEvidenceUpdateAvailable] = useState(false);
  const [evidenceRevealRequest, setEvidenceRevealRequest] = useState<{
    sourceId: string;
    requestId: number;
  }>();
  const [activityRevealRequest, setActivityRevealRequest] = useState<{
    sourceId: string;
    requestId: number;
  }>();
  const scrollRef = useRef<HTMLDivElement>(null);
  const evidenceScrollRef = useRef<HTMLDivElement>(null);
  const evidenceContentRef = useRef<HTMLDivElement>(null);
  const nextStepsRef = useRef<HTMLElement>(null);
  const evidenceCardLayoutRef = useRef(
    new Map<string, { top: number; height: number }>(),
  );
  const latestEvidenceUpdateSourceIdRef = useRef<string | undefined>(undefined);
  const evidenceProjectionTurnsRef = useRef<readonly Turn[]>([]);
  // Replay is accumulated off-screen and committed once at its boundary. This
  // avoids painting a saved transcript turn-by-turn on initial load or reconnect.
  const turnsRef = useRef<Turn[]>([]);
  const replayTurnsRef = useRef<Turn[]>([]);
  // Every subscription starts with replay, including reconnects to a running run.
  // Motion only resumes after the server's explicit replay_complete boundary.
  const suppressEvidenceMotionRef = useRef(true);
  const seenEvidenceGroupRevisionsRef = useRef<Map<string, number>>(new Map());
  const seenEvidenceSourceIdsRef = useRef<Set<string>>(new Set());
  const replayCompleteRef = useRef(false);
  const streamInFlightRef = useRef(false);
  const pendingApplyRef = useRef(false);
  // A replayed historical apply marker must reconstruct the transcript without
  // invalidating queries for the cluster connected today. Remember whether the
  // pending marker belongs to activity this view actually watched (including a
  // locally accepted request whose marker arrived during reconnect replay).
  const pendingApplyStartedLiveRef = useRef(false);
  // Covers the interval after the local Apply confirmation but before its SSE
  // turn marker arrives. pendingApplyRef takes over once the stream confirms it.
  const localApplyRequestRef = useRef(false);
  const paneSelectionTouchedRef = useRef(false);
  const evidenceRevealRequestIdRef = useRef(0);
  const activityRevealRequestIdRef = useRef(0);
  const workspaceId = useId();
  const activityTabId = `${workspaceId}-activity-tab`;
  const activityPaneId = `${workspaceId}-activity-pane`;
  const findingsTabId = `${workspaceId}-findings-tab`;
  const findingsPaneId = `${workspaceId}-findings-pane`;
  // Stick-to-bottom: follow streaming output while the user is at/near the bottom,
  // detach the moment they scroll up to read history, re-attach when they return.
  // Tracked from scroll events (the user's intent) — NOT post-render geometry, which
  // mis-detaches whenever a streamed chunk is taller than the threshold.
  const pinnedRef = useRef(true);
  const [showJump, setShowJump] = useState(false);
  const STICK_THRESHOLD = 64; // px from bottom counted as "at the bottom"

  // After a successful apply, refresh the cluster-state views so the fix shows in
  // the surrounding UI (Issues, the resource, topology, …), not just the transcript.
  const refreshClusterState = useCallback(() => {
    for (const key of [
      ["issues"],
      ["dashboard"],
      ["topology"],
      ["applications"],
      ["audit"],
      ["gitops-insights"],
      ["gitops-tree"],
      ["resource", kind, namespace, name],
    ]) {
      queryClient.invalidateQueries({ queryKey: key });
    }
  }, [queryClient, kind, namespace, name]);

  const updateTurns = (fn: (prev: Turn[]) => Turn[]) => {
    if (!replayCompleteRef.current) {
      replayTurnsRef.current = fn(replayTurnsRef.current);
      return;
    }
    const next = fn(turnsRef.current);
    turnsRef.current = next;
    setTurns(next);
  };
  const updateLast = (fn: (t: Turn) => Turn) =>
    updateTurns((prev) =>
      prev.map((t, i) => (i === prev.length - 1 ? fn(t) : t)),
    );

  // Progressive reasoning reveal: the agent hands us each thinking block whole, but
  // dumping a paragraph at once reads as a jarring pop. Instead we buffer it and
  // drip it into the transcript line-by-line so it streams the way Claude Code /
  // Codex feel live. A tool call, the final report, or an error flushes the buffer
  // instantly (reasoning must fully precede its own tool, and the result can't wait
  // on an animation) — which also makes tab-reopen replay fast-forward for free,
  // since every turn ends in one of those events.
  const revealBufRef = useRef("");
  const revealTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const stopReveal = () => {
    if (revealTimerRef.current) {
      clearInterval(revealTimerRef.current);
      revealTimerRef.current = null;
    }
  };
  const flushReveal = (animate = replayCompleteRef.current) => {
    stopReveal();
    const rest = revealBufRef.current;
    revealBufRef.current = "";
    if (rest)
      updateLast((t) => ({
        ...t,
        timeline: appendThinking(t.timeline, rest, animate),
      }));
  };
  // Next reveal unit: a whole line, but cap a long unwrapped line at a sentence
  // boundary so prose paragraphs (no hard breaks) still reveal in pieces.
  const nextRevealUnit = (buf: string): [string, string] => {
    const nl = buf.indexOf("\n");
    let cut = nl === -1 ? buf.length : nl + 1;
    if (cut > 160) {
      const seg = buf.slice(0, 160);
      const s = Math.max(
        seg.lastIndexOf(". "),
        seg.lastIndexOf("? "),
        seg.lastIndexOf("! "),
      );
      cut = s > 40 ? s + 2 : 160;
    }
    return [buf.slice(0, cut), buf.slice(cut)];
  };
  const pumpReveal = () => {
    if (revealTimerRef.current) return;
    revealTimerRef.current = setInterval(() => {
      if (!revealBufRef.current) {
        stopReveal();
        return;
      }
      // Drain faster when a backlog builds so the reveal can't fall behind a fast
      // model — pace is cosmetic, never a bottleneck on the actual investigation.
      const units = revealBufRef.current.length > 900 ? 3 : 1;
      let take = "";
      for (let k = 0; k < units && revealBufRef.current; k++) {
        const [u, rest] = nextRevealUnit(revealBufRef.current);
        take += u;
        revealBufRef.current = rest;
      }
      if (take)
        updateLast((t) => ({
          ...t,
          timeline: appendThinking(t.timeline, take, true),
        }));
    }, 150);
  };

  // Subscribe to the run's event stream; rebuild the transcript from scratch on
  // (re)subscribe — the server replays everything, so a fresh tab reconstructs the
  // whole conversation.
  useEffect(() => {
    turnsRef.current = [];
    replayTurnsRef.current = [];
    setTurns([]);
    setGone(false);
    setBusy(false);
    setRequestPending(false);
    setStreamReady(false);
    setHistoryUnavailable(null);
    setActionError(null);
    setVerificationError(null);
    setVerificationPending(false);
    setLocalApplyAttemptAssessmentIdx(-1);
    setApplyOutcomeUncertain(null);
    pendingApplyRef.current = false;
    pendingApplyStartedLiveRef.current = false;
    localApplyRequestRef.current = false;
    replayCompleteRef.current = false;
    streamInFlightRef.current = false;
    suppressEvidenceMotionRef.current = true;
    seenEvidenceGroupRevisionsRef.current.clear();
    seenEvidenceSourceIdsRef.current.clear();
    revealBufRef.current = "";
    stopReveal();
    paneSelectionTouchedRef.current = false;
    setNarrowPane(initialInvestigationPane(run.status));
    setUnreadEvidence(false);
    setEvidenceUpdateAvailable(false);
    latestEvidenceUpdateSourceIdRef.current = undefined;
    setEvidenceRevealRequest(undefined);
    evidenceRevealRequestIdRef.current = 0;
    setActivityRevealRequest(undefined);
    activityRevealRequestIdRef.current = 0;
    evidenceCardLayoutRef.current.clear();
    evidenceProjectionTurnsRef.current = [];
    const cancel = subscribeRun(run.id, {
      onEvent: (ev: DiagnoseStreamEvent) => {
        const live = replayCompleteRef.current;
        switch (ev.type) {
          case "turn":
            flushReveal(live); // close out the prior turn's reasoning before the new one
            setRequestPending(false);
            if (ev.apply) {
              pendingApplyStartedLiveRef.current =
                pendingApplyStartedLiveRef.current ||
                live ||
                localApplyRequestRef.current;
              pendingApplyRef.current = true;
              localApplyRequestRef.current = false;
              // The stream now owns the exact outcome. Keep the assessment
              // retired, but replace the transport-uncertainty banner with the
              // streamed apply result / error when it arrives.
              setApplyOutcomeUncertain(null);
            }
            if (ev.verify) {
              setVerificationPending(false);
              setVerificationError(null);
            }
            streamInFlightRef.current = true;
            if (live) setBusy(true);
            updateTurns((prev) => [
              ...prev,
              {
                question: ev.question,
                timeline: [],
                diagnosis: null,
                error: null,
                status: "running",
                apply: ev.apply,
                verify: ev.verify,
              },
            ]);
            break;
          case "thinking":
            if (ev.token) {
              if (live) {
                revealBufRef.current += ev.token;
                pumpReveal();
              } else {
                updateLast((t) => ({
                  ...t,
                  timeline: appendThinking(t.timeline, ev.token!, false),
                }));
              }
            }
            break;
          case "step":
            flushReveal(live); // reasoning fully precedes the tool it led to
            if (ev.step)
              updateLast((t) => ({
                ...t,
                timeline: upsertTool(t.timeline, ev.step!, live),
              }));
            break;
          case "done": {
            flushReveal(live); // the result can't wait on a reveal animation
            const isApply = pendingApplyRef.current;
            const applyStartedLive = pendingApplyStartedLiveRef.current;
            streamInFlightRef.current = false;
            updateLast((t) => investigationTurnWithTerminalEvent(t, ev, live));
            if (live) setBusy(false);
            if (isApply) {
              pendingApplyRef.current = false;
              const effects = investigationApplyCompletionEffects({
                live,
                applyStartedLive,
                stale: run.status === "stale",
              });
              pendingApplyStartedLiveRef.current = false;
              if (effects.refreshClusterState) refreshClusterState();
              // A successful apply is one compound server-owned job. Its next
              // durable event is the automatic read-only verification turn; hold
              // the controls through that adjacent event so there is no idle flash.
              if (effects.verificationPending) setVerificationPending(true);
            }
            if (live || (isApply && applyStartedLive)) refreshRuns();
            break;
          }
          case "error": {
            flushReveal(live);
            streamInFlightRef.current = false;
            const verificationScheduled =
              live && ev.verificationScheduled === true;
            const applyMayHaveMutated =
              investigationApplyTerminalNeedsClusterRefresh({
                localApplyRequestPending: localApplyRequestRef.current,
                streamedApplyPending: pendingApplyRef.current,
                streamedApplyStartedLive: pendingApplyStartedLiveRef.current,
                terminalEventIsLive: live,
              });
            {
              const activeTurns = replayCompleteRef.current
                ? turnsRef.current
                : replayTurnsRef.current;
              if (live && activeTurns.at(-1)?.verify) {
                setVerificationError(
                  ev.error || "The verification could not be completed.",
                );
              }
            }
            updateLast((t) => investigationTurnWithTerminalEvent(t, ev, live));
            if (live && !verificationScheduled) setBusy(false);
            setRequestPending(false);
            pendingApplyRef.current = false;
            pendingApplyStartedLiveRef.current = false;
            localApplyRequestRef.current = false;
            setVerificationPending(verificationScheduled);
            if (applyMayHaveMutated) refreshClusterState();
            if (live) refreshRuns();
            break;
          }
          case "history_unavailable":
            setHistoryUnavailable({
              error: ev.error || "Radar could not read the saved history.",
              retryable: ev.retryable === true,
            });
            setStreamReady(false);
            setBusy(false);
            setRequestPending(false);
            break;
          case "replay_complete":
            turnsRef.current = replayTurnsRef.current;
            setTurns(replayTurnsRef.current);
            if (!paneSelectionTouchedRef.current) {
              const latest = replayTurnsRef.current.at(-1);
              if (
                latest?.status === "done" &&
                latest.question &&
                !latest.verify &&
                !latest.apply
              ) {
                setNarrowPane("activity");
              }
            }
            replayCompleteRef.current = true;
            suppressEvidenceMotionRef.current = false;
            setHistoryUnavailable(null);
            setStreamReady(true);
            setBusy(streamInFlightRef.current);
            break;
        }
      },
      onReplayStart: () => {
        // `open` fires for reconnects too. Transitioning from live seeds the replay
        // staging buffer from the committed transcript. If an initial replay itself
        // reconnects, preserve its uncommitted prefix: Last-Event-ID only sends the
        // suffix, so resetting here would silently drop already-received history.
        const wasLive = replayCompleteRef.current;
        if (wasLive) {
          flushReveal(true);
          replayTurnsRef.current = turnsRef.current;
        }
        replayCompleteRef.current = false;
        suppressEvidenceMotionRef.current = true;
        setStreamReady(false);
      },
      // The run can no longer produce events (evicted / gone). Stale runs emit their
      // own error event + banner before closing, so this only bites the case where a
      // run vanishes while we still think it's running — clear the spinner and mark
      // the open turn terminal so it can't shimmer forever.
      onClosed: (reason) => {
        stopReveal();
        const applyMayHaveMutated =
          investigationApplyTerminalNeedsClusterRefresh({
            localApplyRequestPending: localApplyRequestRef.current,
            streamedApplyPending: pendingApplyRef.current,
            streamedApplyStartedLive: pendingApplyStartedLiveRef.current,
            terminalEventIsLive: investigationClosedEventIsLive({
              reason,
              subscribedRunStatus: run.status,
              replayComplete: replayCompleteRef.current,
            }),
          });
        pendingApplyRef.current = false;
        pendingApplyStartedLiveRef.current = false;
        localApplyRequestRef.current = false;
        if (applyMayHaveMutated) refreshClusterState();
        if (!replayCompleteRef.current) {
          turnsRef.current = replayTurnsRef.current;
          setTurns(replayTurnsRef.current);
        }
        replayCompleteRef.current = false;
        streamInFlightRef.current = false;
        setBusy(false);
        setRequestPending(false);
        setVerificationPending(false);
        setHistoryUnavailable(null);
        setStreamReady(true);
        // A durable close is expected for stale history. A 404/eviction is gone;
        // don't relabel a successfully reconstructed stale transcript as missing.
        const unavailable = investigationClosedRunIsUnavailable({
          reason,
          subscribedRunStatus: run.status,
        });
        setGone(unavailable);
        if (unavailable) {
          const current = turnsRef.current;
          const next = current.map((t, i) =>
            i === current.length - 1 && t.status === "running"
              ? {
                  ...t,
                  status: "error" as const,
                  error: applyMayHaveMutated
                    ? "This investigation is no longer available. The requested change may have completed; Radar refreshed cluster state, but you should start a new investigation to verify it before applying anything again."
                    : "This investigation is no longer available. Start a new investigation to analyze the current cluster.",
                }
              : t,
          );
          turnsRef.current = next;
          setTurns(next);
        }
      },
    });
    return () => {
      stopReveal();
      cancel();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [run.id]);

  // Follow the bottom IFF still pinned, on anything that changes rendered height:
  // new transcript content (turns), or Activity becoming visible after live work
  // arrived behind the Findings tab. useLayoutEffect runs
  // before paint, so the jump is invisible and it overrides browser scroll-anchoring
  // (which would otherwise nudge us off the bottom when the remediation card lands).
  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (el && pinnedRef.current) el.scrollTop = el.scrollHeight;
  }, [turns, narrowPane]);

  // User scroll updates the pin state: scrolling up past the threshold detaches;
  // scrolling back within it re-attaches. Programmatic scroll-to-bottom lands at
  // distance≈0, so it keeps us pinned — no fight with the auto-follow.
  const onScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    const atBottom =
      el.scrollHeight - el.scrollTop - el.clientHeight < STICK_THRESHOLD;
    pinnedRef.current = atBottom;
    setShowJump(!atBottom);
  };
  const jumpToBottom = () => {
    const el = scrollRef.current;
    if (!el) return;
    pinnedRef.current = true;
    setShowJump(false);
    el.scrollTo({
      top: el.scrollHeight,
      behavior: prefersReducedMotion() ? "auto" : "smooth",
    });
  };

  const stale = run.status === "stale";
  const readOnly = investigationIsReadOnly(run.status, gone);
  const lastTurn = turns.at(-1);
  const endedEarly = investigationEndedBeforeConclusion(run.status, lastTurn);
  const rebuildingReplay = !streamReady && turns.length === 0;
  const historyUnavailablePresentation = historyUnavailable
    ? investigationHistoryUnavailablePresentation(historyUnavailable)
    : null;
  const interactionsBlocked = investigationInteractionsBlocked({
    streamReady,
    busy,
    requestPending,
    readOnly,
    verificationPending,
  });

  const submitFollowup = () => {
    const q = input.trim();
    if (!q || interactionsBlocked) return;
    setInput("");
    setActionError(null);
    setNarrowPane("activity");
    suppressEvidenceMotionRef.current = false;
    pinnedRef.current = true; // a user-initiated turn always follows to the bottom
    setRequestPending(true);
    addTurn(run.id, { question: q }).catch((e) => {
      setRequestPending(false);
      setActionError(e instanceof DiagnoseError ? e.message : "Couldn't send.");
    });
  };
  const stop = () => stopRun(run.id);

  // Ask a canned follow-up (e.g. "explain simply") — a one-tap path that turns the
  // prompt's plain-language instruction into something the user controls.
  const askFollowup = (q: string) => {
    if (interactionsBlocked) return;
    setActionError(null);
    setNarrowPane("activity");
    suppressEvidenceMotionRef.current = false;
    pinnedRef.current = true;
    setRequestPending(true);
    addTurn(run.id, { question: q }).catch((e) => {
      setRequestPending(false);
      setActionError(e instanceof DiagnoseError ? e.message : "Couldn't send.");
    });
  };

  // Apply: a user-confirmed remediation turn. Any step is applyable; the chosen
  // step's text is sent so the server binds the apply to it.
  const [confirmApply, setConfirmApply] = useState(false);
  const [pendingFix, setPendingFix] = useState("");
  const requestApply = (fix: string) => {
    if (interactionsBlocked) return;
    setPendingFix(fix);
    setConfirmApply(true);
  };
  const runApply = () => {
    setConfirmApply(false);
    if (interactionsBlocked) return;
    setActionError(null);
    setVerificationError(null);
    setApplyOutcomeUncertain(null);
    setNarrowPane("activity");
    suppressEvidenceMotionRef.current = false;
    pinnedRef.current = true;
    // Pessimistic by design: once the operator confirms a write, a transport
    // failure cannot prove that it did not run. Retire this assessment before
    // fetch and require a later verification before Apply can return.
    setLocalApplyAttemptAssessmentIdx((previous) =>
      Math.max(previous, currentAssessmentIdx),
    );
    localApplyRequestRef.current = true;
    setRequestPending(true);
    addTurn(run.id, { apply: true, fix: pendingFix }).catch((e) => {
      setRequestPending(false);
      if (investigationApplyRejectionIsDefinitive(e)) {
        localApplyRequestRef.current = false;
        setLocalApplyAttemptAssessmentIdx(-1);
        setApplyOutcomeUncertain(null);
        setActionError(e.message.trim() || "Couldn't apply.");
        return;
      }
      refreshClusterState();
      const detail = e instanceof DiagnoseError ? e.message.trim() : "";
      setApplyOutcomeUncertain(
        detail
          ? `${detail} Radar has not verified the current state; check it before applying again.`
          : "Radar couldn't confirm whether the apply request completed. Cluster state was refreshed; check current status before applying again.",
      );
    });
  };
  const checkStatus = () => {
    if (interactionsBlocked) return Promise.resolve();
    setActionError(null);
    setVerificationError(null);
    setNarrowPane("activity");
    suppressEvidenceMotionRef.current = false;
    pinnedRef.current = true;
    setRequestPending(true);
    return addTurn(run.id, {
      question: RECHECK_QUESTION,
      verify: true,
    }).catch((error) => {
      setRequestPending(false);
      setVerificationError(
        error instanceof DiagnoseError
          ? error.message
          : "Couldn't check status.",
      );
    });
  };

  // Apply tracks the latest turn that produced remediation (so follow-ups don't
  // strip it). Any accepted apply attempt, including a stopped or failed one,
  // retires that assessment until a later verification produces a new one.
  let lastRemediationIdx = -1;
  let lastApplyAttemptIdx = -1;
  let lastApplyOutcome: Turn["applyOutcome"];
  turns.forEach((t, i) => {
    if (
      t.status === "done" &&
      !t.apply &&
      (!t.question || t.verify) &&
      (t.diagnosis?.remediation?.length ?? 0) > 0
    )
      lastRemediationIdx = i;
    if (t.apply) {
      lastApplyAttemptIdx = i;
      lastApplyOutcome = t.applyOutcome;
    }
  });

  // Initial and explicit verification turns update the Evidence-pane assessment.
  // Ordinary questions remain conversational answers in Activity.
  const assessmentIndexes: number[] = [];
  turns.forEach((t, i) => {
    const dx = t.diagnosis;
    const structured =
      !!dx &&
      (!!dx.rootCause ||
        (dx.remediation?.length ?? 0) > 0 ||
        dx.healthy ||
        dx.inconclusive);
    if (
      t.status === "done" &&
      !t.apply &&
      (!t.question || t.verify) &&
      structured
    )
      assessmentIndexes.push(i);
  });
  const currentAssessmentIdx = assessmentIndexes.at(-1) ?? -1;
  const initialAssessmentIdx = assessmentIndexes[0] ?? -1;
  const hasMultipleAssessments = assessmentIndexes.length > 1;
  const currentAssessment =
    currentAssessmentIdx >= 0 ? turns[currentAssessmentIdx] : undefined;
  const initialAssessment =
    initialAssessmentIdx >= 0 ? turns[initialAssessmentIdx] : undefined;

  const laterVerificationRecorded = investigationApplyAttemptVerified({
    localApplyAttemptAssessmentIdx,
    currentAssessmentIdx,
    currentAssessmentIsVerification:
      turns[currentAssessmentIdx]?.verify === true,
  });
  useEffect(() => {
    if (!laterVerificationRecorded) return;
    setLocalApplyAttemptAssessmentIdx(-1);
    setApplyOutcomeUncertain(null);
    localApplyRequestRef.current = false;
  }, [laterVerificationRecorded]);

  if (
    !investigationEvidenceInputsEqual(evidenceProjectionTurnsRef.current, turns)
  ) {
    evidenceProjectionTurnsRef.current = turns;
  }
  const evidenceProjectionTurns = evidenceProjectionTurnsRef.current;
  const projection = useMemo(
    () =>
      projectInvestigationEvidence(evidenceProjectionTurns, {
        kind,
        group: run.group,
        namespace,
        name,
      }),
    [evidenceProjectionTurns, kind, namespace, name, run.group],
  );
  const currentAssessmentProjection = useMemo(
    () =>
      projectInvestigationEvidence(
        currentAssessmentIdx >= 0
          ? [evidenceProjectionTurns[currentAssessmentIdx]]
          : [],
        { kind, group: run.group, namespace, name },
      ),
    [
      evidenceProjectionTurns,
      currentAssessmentIdx,
      kind,
      namespace,
      name,
      run.group,
    ],
  );
  const rootCauseEvidenceResolution = useMemo(
    () =>
      currentAssessment?.diagnosis?.rootCause
        ? resolveInvestigationRootCauseEvidence(
            projection,
            currentAssessment.diagnosis.rootCauseEvidence,
            currentAssessmentIdx,
          )
        : undefined,
    [currentAssessment, currentAssessmentIdx, projection],
  );
  const evidenceStepIdsByTurn = useMemo(() => {
    return investigationEvidenceStepIdsByTurn(
      projection,
      rootCauseEvidenceResolution,
    );
  }, [projection, rootCauseEvidenceResolution]);

  const animateEvidenceGroupIds = useMemo(() => {
    if (suppressEvidenceMotionRef.current) return new Set<string>();
    return new Set(
      projection.groups
        .filter((group) => {
          const seen = seenEvidenceGroupRevisionsRef.current.get(group.id) ?? 0;
          return (
            group.observations.length > seen &&
            group.observations
              .slice(seen)
              .some(
                (observation) =>
                  turns[observation.source.turnIndex]?.timeline[
                    observation.source.timelineIndex
                  ]?.animate === true,
              )
          );
        })
        .map((group) => group.id),
    );
  }, [projection.groups, turns]);
  useEffect(() => {
    for (const group of projection.groups) {
      seenEvidenceGroupRevisionsRef.current.set(
        group.id,
        group.observations.length,
      );
    }
  }, [projection.groups]);

  // A repeated check can revise an existing card without changing the group
  // count, so new live sources—not card count—drive the inactive-tab pulse.
  // Replayed sources are marked seen without pulsing the tab.
  useEffect(() => {
    const newLiveSources = projection.sources.filter((source) => {
      if (seenEvidenceSourceIdsRef.current.has(source.id)) return false;
      return (
        turns[source.turnIndex]?.timeline[source.timelineIndex]?.animate ===
        true
      );
    });
    const hasNewLiveSource = newLiveSources.length > 0;
    if (
      investigationEvidenceShouldMarkUnread({
        hasNewLiveSource,
        selectedPane: narrowPane,
        evidencePaneVisible:
          evidenceScrollRef.current !== null &&
          evidenceScrollRef.current.offsetParent !== null,
      })
    ) {
      setUnreadEvidence(true);
    }
    if (hasNewLiveSource && (evidenceScrollRef.current?.scrollTop ?? 0) > 80) {
      latestEvidenceUpdateSourceIdRef.current = newLiveSources.at(-1)?.id;
      setEvidenceUpdateAvailable(true);
    }
    for (const source of projection.sources) {
      seenEvidenceSourceIdsRef.current.add(source.id);
    }
  }, [projection.sources, turns, narrowPane]);

  // The projection can fold a fresh observation into an existing evidence
  // source. Keep the scrolled-away cue reliable for that in-place revision too.
  useEffect(() => {
    if (
      animateEvidenceGroupIds.size > 0 &&
      (evidenceScrollRef.current?.scrollTop ?? 0) > 80
    ) {
      const latestChangedSource = projection.groups
        .filter((group) => animateEvidenceGroupIds.has(group.id))
        .map((group) => group.chronologicalLatest.source)
        .sort((left, right) => left.order - right.order)
        .at(-1);
      if (latestChangedSource) {
        latestEvidenceUpdateSourceIdRef.current = latestChangedSource.id;
      }
      setEvidenceUpdateAvailable(true);
    }
  }, [animateEvidenceGroupIds, projection.groups]);

  // Evidence is inserted into semantic tiers rather than blindly appended. Keep
  // the first card a user is reading fixed in place when a live result lands above
  // it. Native scroll anchoring varies across nested grids, so this pane owns the
  // policy explicitly (and leaves the top of the story free to update when the
  // reader has not scrolled away from it).
  const evidenceLayoutRevision = `${projection.groups
    .map(
      (group) =>
        `${group.id}:${group.observations.length}:${group.latest.tier}:${group.historical ? 1 : 0}`,
    )
    .join("|")}|limitations:${projection.limitations
    .map(
      (limitation) =>
        `${limitation.kind}:${limitation.source}:${limitation.sources.length}:${limitation.message}`,
    )
    .join(",")}`;
  useLayoutEffect(() => {
    const container = evidenceScrollRef.current;
    if (!container || container.offsetParent === null) return;
    const cards = Array.from(
      container.querySelectorAll<HTMLElement>("[data-evidence-card]"),
    );
    const current = captureEvidenceCardLayout(container);

    const previous = evidenceCardLayoutRef.current;
    if (
      !suppressEvidenceMotionRef.current &&
      previous.size > 0 &&
      container.scrollTop > 8
    ) {
      const anchor = cards
        .map((card) => ({ card, layout: previous.get(card.id) }))
        .filter(
          (
            entry,
          ): entry is {
            card: HTMLElement;
            layout: { top: number; height: number };
          } => !!entry.layout,
        )
        .sort((a, b) => a.layout.top - b.layout.top)
        .find(
          ({ layout }) => layout.top + layout.height >= container.scrollTop - 1,
        );
      if (anchor) {
        const nextTop = current.get(anchor.card.id)?.top;
        if (nextTop != null) {
          const delta = nextTop - anchor.layout.top;
          if (Math.abs(delta) > 1) container.scrollTop += delta;
        }
      }
    }

    // Record positions after any scroll correction so the next insertion compares
    // against what the user actually saw.
    evidenceCardLayoutRef.current = captureEvidenceCardLayout(container);
  }, [evidenceLayoutRevision, currentAssessmentIdx, narrowPane]);

  // Disclosure animations and responsive reflow can move cards without changing
  // the evidence projection. Continuously refresh the baseline after those layout
  // changes; the layout effect above remains the only place that adjusts scroll.
  useEffect(() => {
    const container = evidenceScrollRef.current;
    const content = evidenceContentRef.current;
    if (!container || !content || typeof ResizeObserver === "undefined") {
      return;
    }
    let frame: number | undefined;
    const refreshLayoutBaseline = () => {
      if (frame !== undefined) cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        frame = undefined;
        if (container.offsetParent !== null) {
          // A responsive transition can reveal Findings without changing the
          // selected narrow-pane tab (for example, maximizing into split view).
          // Once the evidence is onscreen it is no longer unread.
          setUnreadEvidence(false);
          evidenceCardLayoutRef.current = captureEvidenceCardLayout(container);
        }
      });
    };
    const observer = new ResizeObserver(refreshLayoutBaseline);
    // Observe the pane itself so crossing the responsive visibility boundary is
    // detected even when the projected evidence content has not changed size.
    observer.observe(container);
    observer.observe(content);
    for (const card of container.querySelectorAll<HTMLElement>(
      "[data-evidence-card]",
    )) {
      observer.observe(card);
    }
    refreshLayoutBaseline();
    return () => {
      observer.disconnect();
      if (frame !== undefined) cancelAnimationFrame(frame);
    };
  }, [evidenceLayoutRevision, narrowPane, maximized]);

  const focusAfterPaneSwitch = useCallback(
    (domId: string, evidence: boolean) => {
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          const marker = document.getElementById(domId);
          const target = evidence
            ? ((marker?.closest(
                "[data-evidence-card], [data-evidence-source-container]",
              ) as HTMLElement | null) ?? marker)
            : marker;
          const container = evidence
            ? evidenceScrollRef.current
            : scrollRef.current;
          if (target && container) {
            const containerRect = container.getBoundingClientRect();
            const targetRect = target.getBoundingClientRect();
            container.scrollTo({
              top: investigationPaneCenteredScrollTop({
                scrollTop: container.scrollTop,
                viewportHeight: container.clientHeight,
                contentHeight: container.scrollHeight,
                targetTop: targetRect.top - containerRect.top,
                targetHeight: targetRect.height,
              }),
              behavior: prefersReducedMotion() ? "auto" : "smooth",
            });
          }
          target?.focus({ preventScroll: true });
        });
      });
    },
    [],
  );
  const viewEvidenceSource = useCallback((sourceId: string) => {
    paneSelectionTouchedRef.current = true;
    setNarrowPane("evidence");
    setUnreadEvidence(false);
    // This path reveals the exact changed source, so the broader scrolled-away
    // cue has served its purpose even when the centered card remains below 80px.
    setEvidenceUpdateAvailable(false);
    latestEvidenceUpdateSourceIdRef.current = undefined;
    evidenceRevealRequestIdRef.current += 1;
    setEvidenceRevealRequest({
      sourceId,
      requestId: evidenceRevealRequestIdRef.current,
    });
  }, []);
  const revealEvidenceSource = useCallback(
    (sourceId: string) => {
      focusAfterPaneSwitch(investigationEvidenceSourceDomId(sourceId), true);
    },
    [focusAfterPaneSwitch],
  );
  const viewActivitySource = useCallback(
    (sourceId: string) => {
      paneSelectionTouchedRef.current = true;
      activityRevealRequestIdRef.current += 1;
      setActivityRevealRequest({
        sourceId,
        requestId: activityRevealRequestIdRef.current,
      });
      setNarrowPane("activity");
      window.setTimeout(
        () =>
          focusAfterPaneSwitch(
            investigationActivitySourceDomId(sourceId),
            false,
          ),
        prefersReducedMotion() ? 0 : INVESTIGATION_DISCLOSURE_SETTLE_MS,
      );
    },
    [focusAfterPaneSwitch],
  );

  const verificationRunning = turns.some(
    (turn) => turn.verify && turn.status === "running",
  );
  const toolCallCount = turns.reduce(
    (count, turn) =>
      count + turn.timeline.filter((item) => item.kind === "tool").length,
    0,
  );
  const latestVerification = [...turns].reverse().find((turn) => turn.verify);
  const displayedVerificationError =
    latestVerification?.status === "error"
      ? latestVerification.error || "The verification could not be completed."
      : verificationError;
  const displayedStatusCheckError =
    displayedVerificationError || applyOutcomeUncertain;
  const currentKeyFindingCount = projection.groups.filter(
    (group) => !group.historical && group.latest.tier === "key",
  ).length;
  const findingsTabAccessibleLabel =
    "Findings: current assessment, Radar evidence, and next steps";
  const currentAssessmentCoverageLimited = investigationEvidenceCoverageLimited(
    currentAssessmentProjection,
  );
  const currentAssessmentEvidenceConflict =
    currentAssessment?.diagnosis?.healthy === true &&
    investigationEvidenceConflictsWithHealthy(projection);
  const hasEvidenceCollectedAfterAssessment =
    currentAssessmentIdx >= 0 &&
    projection.sources.some(
      (source) => source.turnIndex > currentAssessmentIdx,
    );
  const assessmentNeedsCurrentStateVerification =
    investigationAssessmentNeedsCurrentStateVerification({
      currentAssessmentIdx,
      lastApplyAttemptIdx,
      lastApplyOutcome,
      localApplyAttemptAssessmentIdx,
    });
  const hasNextSteps = Boolean(
    currentAssessment?.diagnosis &&
    !assessmentNeedsCurrentStateVerification &&
    !hasEvidenceCollectedAfterAssessment &&
    (currentAssessment.diagnosis.remediation?.length ?? 0) > 0,
  );
  const showSplitWorkspace = maximized;
  const splitGridClass = showSplitWorkspace
    ? "@min-[1000px]/investigation:grid-cols-[minmax(360px,520px)_minmax(0,1fr)]"
    : "";
  const splitTabClass = showSplitWorkspace
    ? "@min-[1000px]/investigation:hidden"
    : "";
  const splitPaneClass = showSplitWorkspace
    ? "@min-[1000px]/investigation:flex"
    : "";
  const splitActivityBorderClass = showSplitWorkspace
    ? "@min-[1000px]/investigation:border-r @min-[1000px]/investigation:border-theme-border"
    : "";

  const selectPane = (pane: "activity" | "evidence") => {
    const switchingToEvidence =
      pane === "evidence" && narrowPane !== "evidence";
    paneSelectionTouchedRef.current = true;
    setNarrowPane(pane);
    if (pane === "evidence") {
      setUnreadEvidence(false);
      // An ordinary switch starts at the beginning of the compiled story.
      // Source links use viewEvidenceSource instead and retain exact-card focus.
      if (switchingToEvidence) {
        setEvidenceUpdateAvailable(false);
        latestEvidenceUpdateSourceIdRef.current = undefined;
        requestAnimationFrame(() => {
          evidenceScrollRef.current?.scrollTo({ top: 0 });
        });
      }
    }
  };
  const viewActivity = () => {
    paneSelectionTouchedRef.current = true;
    setNarrowPane("activity");
    requestAnimationFrame(() => {
      scrollRef.current?.scrollTo({ top: 0 });
      document.getElementById(activityPaneId)?.focus({ preventScroll: true });
    });
  };
  const onTabKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    let pane: "activity" | "evidence" | undefined;
    if (event.key === "ArrowLeft" || event.key === "Home") pane = "activity";
    if (event.key === "ArrowRight" || event.key === "End") pane = "evidence";
    if (!pane) return;
    event.preventDefault();
    selectPane(pane);
    document
      .getElementById(pane === "activity" ? activityTabId : findingsTabId)
      ?.focus();
  };
  const revealLatestEvidenceUpdate = () => {
    const sourceId = latestEvidenceUpdateSourceIdRef.current;
    if (sourceId) {
      viewEvidenceSource(sourceId);
      return;
    }
    evidenceScrollRef.current?.scrollTo({
      top: 0,
      behavior: prefersReducedMotion() ? "auto" : "smooth",
    });
    setEvidenceUpdateAvailable(false);
  };

  return (
    <div className="@container/investigation relative flex min-h-0 flex-1 flex-col bg-theme-surface">
      {stale ? (
        <div className="flex items-center gap-2 border-b border-amber-500/35 bg-amber-500/10 px-3 py-2 text-xs text-theme-text-secondary">
          <AlertTriangle className="h-4 w-4 shrink-0 text-amber-500" />
          <span className="min-w-0 flex-1">
            Captured on{" "}
            <span className="font-medium text-theme-text-primary">
              {run.context || "a different cluster"}
            </span>
            . The active cluster changed, so this investigation is read-only.
          </span>
          <button
            type="button"
            onClick={retryDiagnosis}
            className="shrink-0 rounded-md border border-amber-500/40 px-2 py-1 font-medium text-warning-text hover:bg-amber-500/10"
          >
            Investigate current cluster
          </button>
        </div>
      ) : null}
      {!stale && gone ? (
        <div className="flex items-center gap-2 border-b border-theme-border bg-theme-elevated px-3 py-2 text-xs text-theme-text-secondary">
          <AlertTriangle className="h-4 w-4 shrink-0 text-amber-500" />
          <span className="min-w-0 flex-1">
            This investigation is closed and read-only.{" "}
            {turns.length > 0
              ? "Evidence already loaded in this view is preserved, but Radar can no longer continue the run."
              : "No saved evidence is available."}
          </span>
          <button
            type="button"
            onClick={retryDiagnosis}
            className="shrink-0 rounded-md border border-theme-border px-2 py-1 font-medium text-theme-text-primary hover:bg-theme-hover"
          >
            Investigate again
          </button>
        </div>
      ) : null}
      {!stale && !gone && endedEarly ? (
        <div
          role="status"
          className="flex items-center gap-2 border-b border-theme-border bg-theme-elevated px-3 py-2 text-xs text-theme-text-secondary"
        >
          <AlertTriangle className="h-4 w-4 shrink-0 text-amber-500" />
          <span className="min-w-0 flex-1">
            {run.status === "stopped"
              ? "This investigation was stopped before it reached a conclusion."
              : "This investigation ended before it reached a complete conclusion."}
            <span className="ml-1">
              Evidence collected so far is preserved. See Activity for the final
              error or stopped state.
            </span>
          </span>
          <button
            type="button"
            onClick={retryDiagnosis}
            className="shrink-0 rounded-md border border-theme-border px-2 py-1 font-medium text-theme-text-primary hover:bg-theme-hover"
          >
            Investigate again
          </button>
        </div>
      ) : null}

      {startError ? (
        <InvestigationStartErrorAlert
          error={startError}
          onDismiss={dismissError}
        />
      ) : null}

      {!gone && historyUnavailablePresentation ? (
        <div
          role={historyUnavailablePresentation.loading ? "status" : "alert"}
          className={`flex items-start gap-2 border-b px-3 py-2.5 text-xs text-theme-text-secondary ${
            historyUnavailablePresentation.loading
              ? "border-amber-500/35 bg-amber-500/10"
              : "border-red-500/30 bg-red-500/10"
          }`}
        >
          {historyUnavailablePresentation.loading ? (
            <Loader2 className="mt-0.5 h-4 w-4 shrink-0 animate-spin text-amber-500" />
          ) : (
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-red-400" />
          )}
          <span className="min-w-0">
            <span className="block font-medium text-theme-text-primary">
              {historyUnavailablePresentation.title}
            </span>
            <span>{historyUnavailablePresentation.detail}</span>
          </span>
        </div>
      ) : null}

      <div
        role="group"
        aria-label="Investigation workspace"
        className={`grid grid-cols-2 border-b border-theme-border bg-theme-base/40 p-1 ${splitTabClass}`}
      >
        <button
          type="button"
          id={activityTabId}
          aria-controls={activityPaneId}
          aria-pressed={narrowPane === "activity"}
          aria-label="Activity: agent reasoning and tool calls"
          onKeyDown={onTabKeyDown}
          onClick={() => selectPane("activity")}
          className={`flex items-center justify-center gap-1.5 rounded-md px-2 py-1.5 text-xs font-medium ${
            narrowPane === "activity"
              ? "selection-strong selection-text selection-ring"
              : "text-theme-text-secondary hover:bg-theme-hover"
          }`}
        >
          <Activity className="h-3.5 w-3.5" />
          Activity
        </button>
        <button
          type="button"
          id={findingsTabId}
          aria-controls={findingsPaneId}
          aria-pressed={narrowPane === "evidence"}
          aria-label={findingsTabAccessibleLabel}
          onKeyDown={onTabKeyDown}
          onClick={() => selectPane("evidence")}
          className={`relative flex items-center justify-center gap-1.5 rounded-md px-2 py-1.5 text-xs font-medium ${
            narrowPane === "evidence"
              ? "selection-strong selection-text selection-ring"
              : "text-theme-text-secondary hover:bg-theme-hover"
          }`}
        >
          <Files className="h-3.5 w-3.5" />
          Findings
          {unreadEvidence && narrowPane !== "evidence" ? (
            <span className="h-1.5 w-1.5 rounded-full bg-accent" aria-hidden />
          ) : null}
        </button>
        <span className="sr-only" role="status" aria-live="polite">
          {investigationEvidenceAnnouncement({
            unreadEvidence,
            evidenceUpdateAvailable,
          })}
        </span>
      </div>

      <div className={`grid min-h-0 flex-1 ${splitGridClass}`}>
        <section
          id={activityPaneId}
          tabIndex={-1}
          aria-label="Activity: agent reasoning and tool calls"
          aria-busy={busy || requestPending || rebuildingReplay}
          className={`${
            narrowPane === "activity" ? "flex" : "hidden"
          } relative min-h-0 min-w-0 flex-col outline-none ${splitPaneClass} ${splitActivityBorderClass}`}
        >
          <div
            className={`hidden items-center justify-between border-b border-theme-border/60 px-3 py-2 ${splitPaneClass}`}
          >
            <div className="flex min-w-0 items-center gap-2">
              <Activity className="h-4 w-4 shrink-0 text-theme-text-tertiary" />
              <div className="min-w-0">
                <h2 className="truncate text-sm font-semibold text-theme-text-primary">
                  Activity
                </h2>
                <p className="truncate text-[11px] text-theme-text-tertiary">
                  Agent reasoning and tool calls
                </p>
              </div>
            </div>
            <span className="inline-flex items-center gap-1.5 text-[11px] text-theme-text-tertiary">
              {toolCallCount > 0
                ? `${toolCallCount} ${toolCallCount === 1 ? "tool call" : "tool calls"}`
                : turns.length > 0
                  ? `${turns.length} ${turns.length === 1 ? "turn" : "turns"}`
                  : null}
            </span>
          </div>
          <div
            ref={scrollRef}
            data-investigation-activity-scroll
            onScroll={onScroll}
            className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden px-3 py-3 [scrollbar-gutter:stable]"
          >
            <div>
              <div className="space-y-4">
                {turns.length === 0 && !gone ? (
                  <div className="flex min-h-36 flex-col items-center justify-center rounded-lg border border-dashed border-theme-border px-4 text-center">
                    {historyUnavailablePresentation &&
                    !historyUnavailablePresentation.loading ? (
                      <AlertTriangle className="h-5 w-5 text-red-400" />
                    ) : !streamReady || busy ? (
                      <Loader2 className="h-5 w-5 animate-spin text-accent" />
                    ) : (
                      <Activity className="h-5 w-5 text-theme-text-tertiary" />
                    )}
                    <p className="mt-2 text-sm font-medium text-theme-text-secondary">
                      {!streamReady
                        ? historyUnavailablePresentation?.loading
                          ? "Retrying saved activity"
                          : historyUnavailablePresentation
                            ? "Saved activity unavailable"
                            : "Loading saved activity"
                        : busy
                          ? "Starting the investigation"
                          : "No activity recorded"}
                    </p>
                    <p className="mt-1 text-xs text-theme-text-tertiary">
                      {!streamReady
                        ? historyUnavailablePresentation?.loading
                          ? "Radar will continue when saved history is available."
                          : historyUnavailablePresentation
                            ? "Radar could not restore this run from saved history."
                            : "Restoring this run from saved history."
                        : busy
                          ? "Reasoning and tool activity will appear here."
                          : "No saved activity is available for this run."}
                    </p>
                  </div>
                ) : null}
                {turns.map((turn, index) => {
                  const isLast = index === turns.length - 1;
                  const verifiedHealthy =
                    turn.verify &&
                    turn.diagnosis?.healthy === true &&
                    !currentAssessmentCoverageLimited &&
                    !currentAssessmentEvidenceConflict;
                  const canCheck =
                    isLast &&
                    (turn.status === "done" || turn.status === "error") &&
                    !!turn.apply &&
                    !readOnly;
                  return (
                    <Fragment key={index}>
                      <TurnView
                        turn={turn}
                        turnIndex={index}
                        evidenceStepIds={evidenceStepIdsByTurn.get(index)}
                        onViewEvidence={viewEvidenceSource}
                        sourceRevealRequest={activityRevealRequest}
                        onAsk={
                          isLast &&
                          !interactionsBlocked &&
                          !!turn.question &&
                          !turn.verify
                            ? askFollowup
                            : undefined
                        }
                        onCheckStatus={
                          canCheck && !interactionsBlocked
                            ? checkStatus
                            : undefined
                        }
                        onRetryDiagnosis={
                          isLast &&
                          turn.status === "error" &&
                          !turn.question &&
                          !turn.apply &&
                          !stale
                            ? retryDiagnosis
                            : undefined
                        }
                        hideConclusion={assessmentIndexes.includes(index)}
                      />
                      {index === currentAssessmentIdx ? (
                        <button
                          type="button"
                          onClick={() => selectPane("evidence")}
                          className={`group flex w-full items-center gap-2.5 rounded-lg border px-3 py-2.5 text-left transition-colors ${
                            verifiedHealthy
                              ? "border-emerald-500/30 bg-emerald-500/5 hover:bg-emerald-500/10"
                              : "border-accent/30 bg-accent/5 hover:bg-accent/10"
                          } ${splitTabClass}`}
                        >
                          <span
                            className={`flex h-7 w-7 shrink-0 items-center justify-center rounded-full ${
                              verifiedHealthy
                                ? "bg-emerald-500/15 text-emerald-500"
                                : "bg-accent/10 text-accent-text"
                            }`}
                          >
                            {verifiedHealthy ? (
                              <CheckCircle2 className="h-4 w-4" />
                            ) : (
                              <Files className="h-4 w-4" />
                            )}
                          </span>
                          <span className="min-w-0 flex-1">
                            <span className="block text-xs font-semibold text-theme-text-primary">
                              {turn.verify
                                ? "Verification complete"
                                : "Assessment ready"}
                            </span>
                            <span className="block text-[11px] text-theme-text-tertiary">
                              {currentKeyFindingCount > 0
                                ? `${currentKeyFindingCount} ${currentKeyFindingCount === 1 ? "key finding" : "key findings"} ready to review`
                                : "Findings compiled from Radar results"}
                            </span>
                          </span>
                          <span className="inline-flex shrink-0 items-center gap-1 text-xs font-medium text-accent-text">
                            View Findings
                            <ArrowRight className="h-3.5 w-3.5 transition-transform group-hover:translate-x-0.5" />
                          </span>
                        </button>
                      ) : null}
                    </Fragment>
                  );
                })}
                {(actionError || displayedStatusCheckError) && (
                  <div className="flex items-start gap-2 rounded-lg border border-red-500/30 bg-red-500/10 p-3 text-sm text-theme-text-primary">
                    <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-red-400" />
                    <div className="min-w-0 flex-1">
                      <span>{displayedStatusCheckError || actionError}</span>
                      {displayedStatusCheckError ? (
                        <button
                          type="button"
                          onClick={checkStatus}
                          disabled={interactionsBlocked}
                          className="mt-2 block rounded-md border border-red-500/30 px-2 py-1 text-xs font-medium text-theme-text-primary hover:bg-red-500/10 disabled:opacity-50"
                        >
                          {applyOutcomeUncertain && !displayedVerificationError
                            ? "Check current status"
                            : "Check current status again"}
                        </button>
                      ) : null}
                    </div>
                  </div>
                )}
              </div>
            </div>
          </div>
          {showJump ? (
            <button
              type="button"
              onClick={jumpToBottom}
              className="absolute bottom-3 left-1/2 z-10 flex -translate-x-1/2 items-center gap-1.5 rounded-full border border-theme-border bg-theme-elevated px-3 py-1.5 text-xs font-medium text-theme-text-secondary shadow-theme-md hover:bg-theme-hover hover:text-theme-text-primary"
            >
              <ArrowDown className="h-3.5 w-3.5" />
              {busy ? "Jump to latest" : "Scroll to bottom"}
            </button>
          ) : null}
        </section>
        <section
          id={findingsPaneId}
          aria-label="Findings: current assessment, Radar evidence, and next steps"
          aria-busy={
            busy || requestPending || verificationPending || rebuildingReplay
          }
          className={`${
            narrowPane === "evidence" ? "flex" : "hidden"
          } relative min-h-0 min-w-0 flex-col ${splitPaneClass}`}
        >
          <div
            className={`hidden items-center justify-between gap-3 border-b border-theme-border/60 px-3 py-2 ${splitPaneClass}`}
          >
            <div className="flex min-w-0 items-center gap-2">
              <Files className="h-4 w-4 shrink-0 text-theme-text-tertiary" />
              <div className="min-w-0">
                <h2 className="truncate text-sm font-semibold text-theme-text-primary">
                  Findings
                </h2>
                <p className="truncate text-[11px] text-theme-text-tertiary">
                  Current assessment, evidence, and next steps
                </p>
              </div>
            </div>
            <div className="flex shrink-0 items-center gap-2">
              {evidenceUpdateAvailable ? (
                <button
                  type="button"
                  onClick={revealLatestEvidenceUpdate}
                  className="inline-flex items-center gap-1 rounded-full border border-accent/30 bg-accent/5 px-2 py-1 text-[11px] font-medium text-accent-text hover:bg-accent/10"
                >
                  <Files className="h-3 w-3" />
                  See new evidence
                </button>
              ) : null}
            </div>
          </div>
          {evidenceUpdateAvailable ? (
            <div className={`absolute right-4 top-2 z-20 ${splitTabClass}`}>
              <button
                type="button"
                onClick={revealLatestEvidenceUpdate}
                className="inline-flex items-center gap-1 rounded-full border border-accent/30 bg-theme-elevated px-2 py-1 text-[11px] font-medium text-accent-text shadow-theme-sm hover:bg-theme-hover"
              >
                <Files className="h-3 w-3" />
                See new evidence
              </button>
            </div>
          ) : null}
          <div
            ref={evidenceScrollRef}
            data-investigation-findings-scroll
            onScroll={(event) => {
              if (event.currentTarget.scrollTop <= 40) {
                setEvidenceUpdateAvailable(false);
                latestEvidenceUpdateSourceIdRef.current = undefined;
              }
            }}
            className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden px-3 py-3 [overflow-anchor:none] [scrollbar-gutter:stable]"
          >
            <div
              ref={evidenceContentRef}
              className="mx-auto max-w-5xl space-y-6"
            >
              {rebuildingReplay ? (
                <div className="flex min-h-28 flex-col items-center justify-center rounded-lg border border-dashed border-theme-border px-4 text-center">
                  {historyUnavailablePresentation &&
                  !historyUnavailablePresentation.loading ? (
                    <AlertTriangle className="h-5 w-5 text-red-400" />
                  ) : (
                    <Loader2 className="h-5 w-5 animate-spin text-accent" />
                  )}
                  <p className="mt-2 text-sm font-medium text-theme-text-secondary">
                    {historyUnavailablePresentation?.loading
                      ? "Retrying saved evidence"
                      : historyUnavailablePresentation
                        ? "Saved evidence unavailable"
                        : "Loading saved evidence"}
                  </p>
                  <p className="mt-1 text-xs text-theme-text-tertiary">
                    {historyUnavailablePresentation?.loading
                      ? "Radar will continue when saved history is available."
                      : historyUnavailablePresentation
                        ? "Radar could not restore evidence from saved history."
                        : "Restoring the assessment and evidence from saved history."}
                  </p>
                </div>
              ) : (
                <>
                  <section
                    aria-labelledby={`${workspaceId}-assessment-heading`}
                    className="rounded-xl border border-theme-border bg-theme-elevated/50 p-4"
                  >
                    <div className="flex flex-wrap items-center gap-1.5">
                      <h2
                        id={`${workspaceId}-assessment-heading`}
                        className="text-lg font-semibold text-theme-text-primary"
                      >
                        {assessmentNeedsCurrentStateVerification
                          ? "Assessment before apply"
                          : hasEvidenceCollectedAfterAssessment
                            ? "Earlier assessment"
                            : !currentAssessment
                              ? "Assessment"
                              : currentAssessment.verify
                                ? "Verification result"
                                : currentAssessmentIdx ===
                                      initialAssessmentIdx &&
                                    hasMultipleAssessments
                                  ? "Initial assessment"
                                  : "Assessment"}
                      </h2>
                      <span className="text-xs text-theme-text-tertiary">
                        AI assessment
                      </span>
                      {hasNextSteps ? (
                        <button
                          type="button"
                          onClick={() => {
                            const section = nextStepsRef.current;
                            const scroller = evidenceScrollRef.current;
                            if (!section || !scroller) return;
                            section.focus({ preventScroll: true });
                            scroller.scrollTo({
                              top:
                                scroller.scrollTop +
                                section.getBoundingClientRect().top -
                                scroller.getBoundingClientRect().top -
                                12,
                              behavior: prefersReducedMotion()
                                ? "auto"
                                : "smooth",
                            });
                          }}
                          className="ml-auto rounded-md px-2 py-1 text-xs font-medium text-accent-text hover:bg-theme-hover"
                        >
                          Next steps ↓
                        </button>
                      ) : null}
                      {assessmentNeedsCurrentStateVerification ? (
                        <Badge severity="warning" size="sm">
                          Current state unverified
                        </Badge>
                      ) : null}
                      {hasEvidenceCollectedAfterAssessment &&
                      !assessmentNeedsCurrentStateVerification ? (
                        <Badge severity="info" size="sm">
                          Newer evidence below
                        </Badge>
                      ) : null}
                      {verificationRunning || verificationPending ? (
                        <Badge severity="info" size="sm">
                          Verifying…
                        </Badge>
                      ) : null}
                    </div>
                    {assessmentNeedsCurrentStateVerification ? (
                      <p className="mt-0.5 text-xs text-theme-text-tertiary">
                        {verificationRunning || verificationPending
                          ? "This assessment predates the apply attempt. Radar is checking the current state now."
                          : "This assessment predates the apply attempt; cluster state after it has not been verified."}
                      </p>
                    ) : hasEvidenceCollectedAfterAssessment ? (
                      <p className="mt-0.5 text-xs text-theme-text-tertiary">
                        Some evidence below was collected after this assessment.
                        Validate the conclusion against it before acting.
                      </p>
                    ) : null}
                    {currentAssessment?.diagnosis ? (
                      <ResultCard
                        diagnosis={currentAssessment.diagnosis}
                        onAsk={!interactionsBlocked ? askFollowup : undefined}
                        section="conclusion"
                        animate={currentAssessment.animateResult !== false}
                        showDisclaimer={false}
                        coverageLimited={currentAssessmentCoverageLimited}
                        evidenceConflict={currentAssessmentEvidenceConflict}
                      />
                    ) : (
                      <div className="mt-2 flex items-center gap-2 rounded-md bg-theme-surface/60 px-2.5 py-2 text-xs text-theme-text-tertiary">
                        {busy || requestPending ? (
                          <span
                            className="h-1.5 w-1.5 shrink-0 animate-pulse rounded-full bg-accent"
                            aria-hidden
                          />
                        ) : (
                          <Activity
                            className="h-3.5 w-3.5 shrink-0"
                            aria-hidden
                          />
                        )}
                        <span>
                          {busy || requestPending
                            ? "Forming an assessment as evidence arrives…"
                            : "The agent did not provide a final assessment."}
                        </span>
                      </div>
                    )}
                    {currentAssessment?.verify &&
                    currentAssessmentIdx !== initialAssessmentIdx &&
                    initialAssessment?.diagnosis ? (
                      <PriorConclusion
                        diagnosis={initialAssessment.diagnosis}
                      />
                    ) : null}
                    {displayedStatusCheckError ? (
                      <div className="mt-2 flex items-center gap-2 rounded-lg border border-red-500/30 bg-red-500/5 px-3 py-2 text-xs text-theme-text-secondary">
                        <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-red-400" />
                        <span className="min-w-0 flex-1">
                          {applyOutcomeUncertain && !displayedVerificationError
                            ? displayedStatusCheckError
                            : `Verification did not complete: ${displayedStatusCheckError}`}
                        </span>
                        <button
                          type="button"
                          onClick={checkStatus}
                          disabled={interactionsBlocked}
                          className="shrink-0 rounded-md border border-theme-border px-2 py-1 font-medium text-theme-text-primary hover:bg-theme-hover disabled:opacity-50"
                        >
                          {applyOutcomeUncertain && !displayedVerificationError
                            ? "Check current status"
                            : "Check current status again"}
                        </button>
                      </div>
                    ) : null}
                  </section>

                  <InvestigationEvidencePane
                    projection={projection}
                    rootCauseEvidence={rootCauseEvidenceResolution}
                    collecting={busy || requestPending}
                    animateGroupIds={animateEvidenceGroupIds}
                    onViewSource={viewActivitySource}
                    onViewActivity={viewActivity}
                    onOpenResource={stale ? undefined : onOpenResource}
                    revealRequest={evidenceRevealRequest}
                    onRevealReady={revealEvidenceSource}
                    afterEvidence={
                      hasNextSteps && currentAssessment?.diagnosis ? (
                        <section
                          ref={nextStepsRef}
                          tabIndex={-1}
                          aria-labelledby={`${workspaceId}-next-steps`}
                          className="rounded-xl border border-theme-border bg-theme-elevated/50 p-4 outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
                        >
                          <h2
                            id={`${workspaceId}-next-steps`}
                            className="text-lg font-semibold text-theme-text-primary"
                          >
                            Next steps
                          </h2>
                          <ResultCard
                            diagnosis={currentAssessment.diagnosis}
                            section="actions"
                            compactActions
                            onApply={
                              canOfferInvestigationApply({
                                currentAssessmentIdx,
                                lastRemediationIdx,
                                lastApplyAttemptIdx,
                                localApplyAttemptAssessmentIdx,
                                interactionsBlocked,
                                hosted,
                              })
                                ? requestApply
                                : undefined
                            }
                            animate={currentAssessment.animateResult !== false}
                            showDisclaimer={false}
                          />
                        </section>
                      ) : undefined
                    }
                  />
                </>
              )}
            </div>
          </div>
        </section>
      </div>

      <ApplyDialog
        open={confirmApply}
        onClose={() => setConfirmApply(false)}
        onConfirm={runApply}
        agentLabel={agentLabel}
        resourceLabel={formatInvestigationTarget(run)}
        fix={pendingFix}
        managedBy={run.managedBy}
        confidence={turns[lastRemediationIdx]?.diagnosis?.confidence}
      />

      <div className={`grid shrink-0 ${splitGridClass}`}>
        <div
          className={`border-t border-theme-border px-3 py-2.5 ${splitActivityBorderClass}`}
        >
          {busy ? (
            <button
              type="button"
              onClick={stop}
              className="w-full rounded-lg border border-theme-border py-1.5 text-sm text-theme-text-secondary hover:bg-theme-hover"
            >
              Stop agent
            </button>
          ) : (
            <div className="flex items-end gap-2">
              <textarea
                value={input}
                onChange={(event) => setInput(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" && !event.shiftKey) {
                    event.preventDefault();
                    submitFollowup();
                  }
                }}
                rows={1}
                disabled={
                  !streamReady ||
                  readOnly ||
                  requestPending ||
                  verificationPending
                }
                placeholder={
                  readOnly
                    ? stale
                      ? "Cluster changed — investigate again"
                      : "Investigation closed — start a new one"
                    : !streamReady
                      ? historyUnavailablePresentation?.loading
                        ? "Retrying investigation history…"
                        : historyUnavailablePresentation
                          ? "Investigation history unavailable"
                          : "Loading investigation history…"
                      : verificationPending
                        ? "Waiting to verify the applied change…"
                        : requestPending
                          ? "Agent is working…"
                          : "Ask a follow-up or refine…"
                }
                className="max-h-32 min-h-[38px] flex-1 resize-none rounded-lg border border-theme-border bg-theme-base px-3 py-2 text-sm text-theme-text-primary placeholder:text-theme-text-tertiary focus:border-accent focus:outline-none disabled:opacity-50"
              />
              <button
                type="button"
                onClick={submitFollowup}
                disabled={!input.trim() || interactionsBlocked}
                className="shrink-0 rounded-lg btn-brand p-2 disabled:opacity-40"
                aria-label="Send follow-up"
              >
                <Send className="h-4 w-4" />
              </button>
            </div>
          )}
        </div>
        <div
          aria-hidden="true"
          className={`hidden border-t border-theme-border ${splitPaneClass}`}
        />
      </div>
    </div>
  );
}

function PriorConclusion({ diagnosis }: { diagnosis: Diagnosis }) {
  const [open, setOpen] = useState(false);
  const regionId = useId();
  return (
    <div className="mt-2 overflow-hidden rounded-lg border border-theme-border bg-theme-base/35">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={regionId}
        onClick={() => setOpen((value) => !value)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-theme-text-secondary hover:bg-theme-hover"
      >
        <CollapseChevron open={open} className="h-3.5 w-3.5" />
        <span className="font-medium">Initial assessment</span>
        <span className="text-theme-text-tertiary">
          before the latest status check
        </span>
      </button>
      <div id={regionId}>
        <Collapse open={open}>
          <div className="border-t border-theme-border/60 px-3 pb-3">
            <ResultCard
              diagnosis={diagnosis}
              section="conclusion"
              showDisclaimer={false}
            />
          </div>
        </Collapse>
      </div>
    </div>
  );
}
