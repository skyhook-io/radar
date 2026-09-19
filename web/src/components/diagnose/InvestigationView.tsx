// A view over one durable, server-side investigation run. It SUBSCRIBES to the
// run's event stream (replay + live) and reconstructs the transcript; it does not
// own the run's lifetime — the server does. So closing the panel or navigating
// away just unsubscribes; the run keeps going and re-subscribing replays it.
import {
  investigationDisclosureSettleDelay,
  prefersReducedMotion,
} from "./useDisclosureReveal";
import {
  investigationExplanation,
  supportsAssessmentExplanation,
} from "./investigationExplanation";
import {
  initialInvestigationPane,
  investigationEvidenceShouldMarkUnread,
  investigationEvidenceAnnouncement,
  investigationIsReadOnly,
  investigationInteractionsBlocked,
  canOfferInvestigationApply,
  investigationApplyAttemptVerified,
  investigationAssessmentNeedsCurrentStateVerification,
  investigationApplyRejectionIsDefinitive,
  investigationApplyCompletionEffects,
  investigationTurnWithTerminalEvent,
  investigationApplyTerminalNeedsClusterRefresh,
  investigationClosedEventIsLive,
  investigationClosedRunIsUnavailable,
  investigationEvidenceInputsEqual,
  investigationEvidenceCoverageLimited,
  investigationEvidenceConflictsWithHealthy,
  investigationHealthConflictExplainedBy,
  investigationAssessmentTurnIndexes,
  investigationEvidenceCoverageGaps,
  investigationHealthSignals,
  investigationIsAssessmentTurn,
  investigationSettledAnswerTurnIndexes,
  investigationEndedBeforeConclusion,
  type InvestigationHistoryUnavailableState,
  investigationHistoryUnavailablePresentation,
  investigationPaneCenteredScrollTop,
  canStopInvestigation,
  canContinueInvestigation,
  canInvestigateFurther,
} from "./investigationState";
import type { AssessmentExplanation } from "./parts";
import { investigationRunningLabel } from "./toolCallLabel";
import { AGENT_ROLE_LABELS } from "./AgentCase";
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
import { Badge } from "@skyhook-io/k8s-ui";
import {
  Send,
  AlertTriangle,
  ArrowDown,
  ArrowRight,
  Activity,
  CheckCircle2,
  FileClock,
  Files,
  Loader2,
} from "lucide-react";
import {
  subscribeRun,
  addTurn,
  stopRun,
  DiagnoseError,
  type DiagnoseStreamEvent,
  type RunSummary,
} from "../../api/diagnose";
import { useDiagnose } from "./DiagnoseContext";
import {
  TurnView,
  ResultCard,
  AssessmentSources,
  ApplyDialog,
  appendThinking,
  mergeStartupSignal,
  upsertTool,
  type Turn,
  remediationHeadline,
} from "./parts";
import {
  investigationActivitySourceDomId,
  investigationEvidenceStepIdsByTurn,
  investigationEvidenceSourceDomId,
  projectInvestigationEvidence,
  resolveInvestigationRootCauseEvidence,
} from "./investigationEvidence";
import { resolveInvestigationCase } from "./investigationCase";
import {
  InvestigationEvidencePane,
  type InvestigationTimelineScope,
} from "./InvestigationEvidencePane";
import { partitionInvestigationEvidence } from "./investigationEvidencePartition";
import type { DiagnosisResourceRef } from "./diagnoseEvidenceTypes";
import { formatInvestigationTarget } from "./target";
import { parseContextName } from "../../utils/context-name";
import type { InvestigationSourceExcerpt } from "./investigationSourceFocus";
import { diagnosisHasStoryShape } from "./investigationStory";
import { groupEvidenceCoverage } from "./investigationEvidencePresentation";

const RECHECK_QUESTION =
  "Did the fix resolve the issue? Re-check the resource's current status and health now, and say whether it's healthy.";

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
  onOpenTimeline,
}: {
  run: RunSummary;
  agentLabel: string;
  maximized: boolean;
  /** Opens an unambiguous evidence subject in Radar's native resource views. */
  onOpenResource?: (ref: DiagnosisResourceRef) => void;
  /** Opens Radar's Timeline filtered to the resource a changes card is about. */
  onOpenTimeline?: (scope: InvestigationTimelineScope) => void;
}) {
  const { kind, namespace, name } = run;
  const { refreshRuns, openInvestigation, startError, dismissError, agents } =
    useDiagnose();
  // Capabilities are the declared ones of the agent that ran this run, not
  // the picker's: a reopened run keeps the backend it was made with.
  const runAgent = agents.find((agent) => agent.name === run.agent);
  const explanationEnabled = supportsAssessmentExplanation(runAgent);
  const canApply = runAgent?.apply === true;
  // Read through a ref by the stream callback, which lives as long as the
  // run and would otherwise keep the value from before the agents loaded.
  const verifiesAfterApplyRef = useRef(false);
  verifiesAfterApplyRef.current = runAgent?.verification === true;
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
  const explanationRequestSerial = useRef(0);
  const [explanationRequest, setExplanationRequest] = useState<{
    sequence: number;
    status: "running" | "error";
    error?: string;
  } | null>(null);
  const [explanationReveal, setExplanationReveal] = useState<{
    sequence: number;
    request: number;
    currentAssessmentIndex: number;
  } | null>(null);
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
    excerpt?: InvestigationSourceExcerpt;
  }>();
  const scrollRef = useRef<HTMLDivElement>(null);
  const evidenceScrollRef = useRef<HTMLDivElement>(null);
  // display:none reports scrollTop=0 even though the browser retains the pane's
  // position. Keep the visible position for evidence arriving behind Activity.
  const evidenceScrollTopRef = useRef(0);
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
    setExplanationRequest(null);
    explanationRequestSerial.current++;
    setExplanationReveal(null);
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
    evidenceScrollTopRef.current = 0;
    latestEvidenceUpdateSourceIdRef.current = undefined;
    setEvidenceRevealRequest(undefined);
    evidenceRevealRequestIdRef.current = 0;
    setActivityRevealRequest(undefined);
    activityRevealRequestIdRef.current = 0;
    evidenceCardLayoutRef.current.clear();
    evidenceProjectionTurnsRef.current = [];
    const cancel = subscribeRun(run.id, {
      onEvent: (ev: DiagnoseStreamEvent, sequence?: number) => {
        const live = replayCompleteRef.current;
        switch (ev.type) {
          case "turn":
            flushReveal(live); // close out the prior turn's reasoning before the new one
            setRequestPending(false);
            if (ev.explainAssessment) setExplanationRequest(null);
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
                actor: ev.actor,
                explainAssessment: ev.explainAssessment,
                timeline: [],
                diagnosis: null,
                error: null,
                status: "running",
                apply: ev.apply,
                verify: ev.verify,
              },
            ]);
            break;
          case "phase":
            // Startup phases only feed the pending status line; a replayed
            // finished turn is not running, so nothing shows for it.
            updateLast((t) => {
              const startup = mergeStartupSignal(t.startup, ev);
              return startup === t.startup ? t : { ...t, startup };
            });
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
            updateLast((t) => ({
              ...investigationTurnWithTerminalEvent(t, ev, live),
              resultSequence: sequence,
            }));
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
              // On a backend that verifies, a successful apply is one compound
              // server-owned job whose next durable event is the automatic
              // read-only verification turn; hold the controls through that
              // adjacent event so there is no idle flash. A backend that
              // declares no verification sends no such turn, so nothing waits.
              if (effects.verificationPending && verifiesAfterApplyRef.current)
                setVerificationPending(true);
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
              if (latest?.explainAssessment) {
                setNarrowPane("evidence");
              } else if (
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
  const lastTurn = turns.at(-1);
  const unavailable = investigationIsReadOnly(run.status, gone);
  const canContinue = canContinueInvestigation(run, lastTurn?.status, gone);
  const canStop = canStopInvestigation(run, busy, gone, lastTurn?.status);
  const readOnly = unavailable || !canContinue;
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

  const askExplanation = (sequence: number) => {
    if (interactionsBlocked) return;
    setActionError(null);
    const serial = ++explanationRequestSerial.current;
    const previousTurns = turnsRef.current.length;
    setExplanationRequest({ sequence, status: "running" });
    setRequestPending(true);
    addTurn(run.id, { explainAssessment: sequence }).catch((e) => {
      if (serial !== explanationRequestSerial.current) return;
      // If the stream already accepted the turn, it owns progress and failure.
      if (
        turnsRef.current
          .slice(previousTurns)
          .some((turn) => turn.explainAssessment === sequence)
      )
        return;
      setRequestPending(false);
      setExplanationRequest({
        sequence,
        status: "error",
        error:
          e instanceof DiagnoseError
            ? e.message
            : "Couldn't request an explanation.",
      });
    });
  };

  const explanationFor = (
    assessment: Turn,
  ): AssessmentExplanation | undefined => {
    const sequence = assessment.resultSequence;
    if (!sequence || !investigationIsAssessmentTurn(assessment))
      return undefined;
    const saved = investigationExplanation(turns, sequence);
    if ((readOnly || !explanationEnabled) && saved.status === "idle")
      return undefined;
    const state =
      explanationRequest?.sequence === sequence ? explanationRequest : saved;
    return {
      ...state,
      onGenerate:
        explanationEnabled && !interactionsBlocked
          ? () => askExplanation(sequence)
          : undefined,
      openRequest:
        explanationReveal?.sequence === sequence &&
        explanationReveal.currentAssessmentIndex === currentAssessmentIdx
          ? explanationReveal.request
          : undefined,
    };
  };

  const viewExplanation = (sequence: number) => {
    paneSelectionTouchedRef.current = true;
    setNarrowPane("evidence");
    setExplanationReveal((previous) => ({
      sequence,
      request: (previous?.request ?? 0) + 1,
      currentAssessmentIndex: currentAssessmentIdx,
    }));
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
    // A revising follow-up is an assessment turn too; its steps are the ones
    // Findings offers.
    if (
      investigationIsAssessmentTurn(t) &&
      (t.diagnosis?.remediation?.length ?? 0) > 0
    )
      lastRemediationIdx = i;
    if (t.apply) {
      lastApplyAttemptIdx = i;
      lastApplyOutcome = t.applyOutcome;
    }
  });

  // Initial and explicit verification turns update the Findings assessment,
  // and so does a question whose verdict revises it and is complete. Other
  // questions remain conversational answers in Activity.
  const assessmentIndexes = investigationAssessmentTurnIndexes(turns);
  const currentAssessmentIdx = assessmentIndexes.at(-1) ?? -1;
  const initialAssessmentIdx = assessmentIndexes[0] ?? -1;
  const hasMultipleAssessments = assessmentIndexes.length > 1;
  const currentAssessment =
    currentAssessmentIdx >= 0 ? turns[currentAssessmentIdx] : undefined;

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
  // The agent's case binds for every assessment, healthy and inconclusive
  // included, so it is resolved independently of the root cause.
  const investigationCase = useMemo(
    () =>
      currentAssessment?.diagnosis
        ? resolveInvestigationCase(
            projection,
            currentAssessment.diagnosis,
            currentAssessmentIdx,
          )
        : undefined,
    [currentAssessment, currentAssessmentIdx, projection],
  );
  // Findings is the newest assessment and nothing else: its own citations,
  // its own case, its own story. Earlier assessments stay complete in
  // Activity; answers that did not revise the assessment stay there too.
  const paneResolution = rootCauseEvidenceResolution;
  const storyShape = diagnosisHasStoryShape(currentAssessment?.diagnosis);
  const visibleEvidenceGroupIds = useMemo(
    () =>
      new Set(
        partitionInvestigationEvidence(
          projection.groups,
          paneResolution,
          investigationCase,
          storyShape,
        ).collectionByGroup.keys(),
      ),
    [projection.groups, paneResolution, investigationCase, storyShape],
  );
  const evidenceStepIdsByTurn = useMemo(
    () =>
      investigationEvidenceStepIdsByTurn(projection, visibleEvidenceGroupIds),
    [projection, visibleEvidenceGroupIds],
  );

  const animateEvidenceGroupIds = useMemo(() => {
    if (suppressEvidenceMotionRef.current) return new Set<string>();
    return new Set(
      projection.groups
        .filter((group) => {
          if (!visibleEvidenceGroupIds.has(group.id)) return false;
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
  }, [projection.groups, turns, visibleEvidenceGroupIds]);
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
      if (!evidenceStepIdsByTurn.get(source.turnIndex)?.has(source.stepId))
        return false;
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
    const scrollTop = evidenceScrollRef.current?.offsetParent
      ? evidenceScrollRef.current.scrollTop
      : evidenceScrollTopRef.current;
    if (hasNewLiveSource && scrollTop > 80) {
      latestEvidenceUpdateSourceIdRef.current = newLiveSources.at(-1)?.id;
      setEvidenceUpdateAvailable(true);
    }
    for (const source of projection.sources) {
      seenEvidenceSourceIdsRef.current.add(source.id);
    }
  }, [projection.sources, turns, narrowPane, evidenceStepIdsByTurn]);

  // The projection can fold a fresh observation into an existing evidence
  // source. Keep the scrolled-away cue reliable for that in-place revision too.
  useEffect(() => {
    const scrollTop = evidenceScrollRef.current?.offsetParent
      ? evidenceScrollRef.current.scrollTop
      : evidenceScrollTopRef.current;
    if (animateEvidenceGroupIds.size > 0 && scrollTop > 80) {
      const latestChangedSource = projection.groups
        .filter((group) => animateEvidenceGroupIds.has(group.id))
        .map((group) => group.chronologicalLatest.source)
        .filter((source) =>
          evidenceStepIdsByTurn.get(source.turnIndex)?.has(source.stepId),
        )
        .sort((left, right) => left.order - right.order)
        .at(-1);
      if (latestChangedSource) {
        latestEvidenceUpdateSourceIdRef.current = latestChangedSource.id;
        setEvidenceUpdateAvailable(true);
      }
    }
  }, [animateEvidenceGroupIds, projection.groups, evidenceStepIdsByTurn]);

  // Evidence is inserted into semantic tiers rather than blindly appended. Keep
  // the first card a user is reading fixed in place when a live result lands above
  // it. Native scroll anchoring varies across nested grids, so this pane owns the
  // policy explicitly (and leaves the top of the story free to update when the
  // reader has not scrolled away from it).
  const evidenceLayoutRevision = `${projection.groups
    .filter((group) => visibleEvidenceGroupIds.has(group.id))
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
    (sourceId: string, excerpt?: InvestigationSourceExcerpt) => {
      paneSelectionTouchedRef.current = true;
      activityRevealRequestIdRef.current += 1;
      setActivityRevealRequest({
        sourceId,
        excerpt,
        requestId: activityRevealRequestIdRef.current,
      });
      setNarrowPane("activity");
      window.setTimeout(
        () =>
          focusAfterPaneSwitch(
            investigationActivitySourceDomId(sourceId),
            false,
          ),
        investigationDisclosureSettleDelay(prefersReducedMotion()),
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
  const findingsTabAccessibleLabel =
    "Findings: current assessment, Radar evidence, and next steps";
  const currentAssessmentCoverageLimited = investigationEvidenceCoverageLimited(
    currentAssessmentProjection,
    kind,
  );
  // The agent's notes per source. In the legacy shape this is Assessment
  // details; in the story shape the notes sit on the cards, so it appears
  // only inside Captured results and only when something has no card: a
  // note that pins to no single observation, or a citation Radar rejected.
  const assessmentSourcesNode =
    currentAssessment?.diagnosis &&
    (rootCauseEvidenceResolution?.links.length ||
      investigationCase?.items.length ||
      currentAssessment.diagnosis.unlinkedEvidence ||
      currentAssessment.diagnosis.evidenceMalformed ||
      currentAssessment.diagnosis.omittedEntries) ? (
      <AssessmentSources
        renderedGroupIds={visibleEvidenceGroupIds}
        resolution={rootCauseEvidenceResolution}
        investigationCase={investigationCase}
        unlinkedEvidence={currentAssessment.diagnosis.unlinkedEvidence}
        evidenceMalformed={currentAssessment.diagnosis.evidenceMalformed}
        omittedEntries={currentAssessment.diagnosis.omittedEntries}
        onViewSource={viewActivitySource}
      />
    ) : undefined;
  const recordNotes =
    storyShape &&
    (investigationCase?.items.some((item) => item.placement === "source") ||
      currentAssessment?.diagnosis?.unlinkedEvidence ||
      currentAssessment?.diagnosis?.evidenceMalformed ||
      currentAssessment?.diagnosis?.omittedEntries)
      ? assessmentSourcesNode
      : undefined;
  const assessmentLimits = useMemo(() => {
    const lines = groupEvidenceCoverage(currentAssessmentProjection.limitations)
      // A capped preview or a history bound is bookkeeping for the record; Still
      // open keeps the reads Radar could not complete.
      .filter((group) => !group.historyOnly && !group.bookkeepingOnly)
      .map((group) => `${group.label}: ${group.summary}`);
    // Two coverage gaps carry no limitation of their own; name the one that
    // applies.
    const gaps = investigationEvidenceCoverageGaps(
      currentAssessmentProjection,
      kind,
    );
    if (gaps.noEvidence)
      lines.push("No evidence was recorded for this assessment");
    else if (gaps.noTargetDiagnosis)
      lines.push(
        "Radar's full diagnose of this workload was not collected, so this check is partial",
      );
    return lines;
  }, [currentAssessmentProjection, kind]);
  const healthSignals = useMemo(
    () =>
      currentAssessment?.diagnosis?.healthy
        ? investigationHealthSignals(projection, investigationCase?.items)
        : [],
    [currentAssessment, projection, investigationCase],
  );
  // While the first assessment is still running the pane keeps the story
  // shape, so the page fills in rather than rearranging when the verdict lands.
  const storyShell = !currentAssessment && lastTurn?.status === "running";
  const currentAssessmentEvidenceConflict =
    currentAssessment?.diagnosis?.healthy === true &&
    investigationEvidenceConflictsWithHealthy(projection);
  // The banner qualifies THIS assessment, so only this assessment's own case
  // may reframe it — and that is the only case the pane renders.
  const currentAssessmentEvidenceConflictExplainedBy = useMemo(() => {
    if (!currentAssessmentEvidenceConflict) return undefined;
    return (
      investigationHealthConflictExplainedBy(
        projection,
        investigationCase?.items,
      ) ?? undefined
    );
  }, [currentAssessmentEvidenceConflict, projection, investigationCase]);
  const settledAnswerTurns = useMemo(
    () => investigationSettledAnswerTurnIndexes(turns, currentAssessmentIdx),
    [turns, currentAssessmentIdx],
  );
  const hasEvidenceCollectedAfterAssessment =
    currentAssessmentIdx >= 0 &&
    projection.sources.some(
      (source) =>
        source.turnIndex > currentAssessmentIdx &&
        !settledAnswerTurns.has(source.turnIndex),
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
    (currentAssessment.diagnosis.remediation?.length ?? 0) > 0,
  );
  const earlierPlan =
    assessmentNeedsCurrentStateVerification ||
    hasEvidenceCollectedAfterAssessment;
  // The jump link names the decision it leads to, not the section.
  const nextStepLabel = (() => {
    const dx = currentAssessment?.diagnosis;
    if (!dx) return "Next steps ↓";
    const index = dx.recommendedIndex ? dx.recommendedIndex - 1 : 0;
    const text =
      dx.steps?.[index]?.text ?? dx.remediation?.[index] ?? dx.remediation?.[0];
    if (!text) return "Next steps ↓";
    const headline = remediationHeadline(text);
    return `Next: ${headline.length > 56 ? `${headline.slice(0, 56).trimEnd()}…` : headline} ↓`;
  })();
  // What a reader in a channel needs with the verdict: where, when, and the
  // receipts behind it, cause first, at most three.
  const assessmentCopy = useMemo(() => {
    const items = [...(investigationCase?.items ?? [])]
      .filter((item) => item.placement === "card" && item.observation)
      .sort((a, b) => Number(b.role === "cause") - Number(a.role === "cause"))
      .slice(0, 3);
    return {
      context: {
        target: formatInvestigationTarget(run),
        cluster: run.context,
        startedAt: run.createdAt,
        agent: agentLabel,
      },
      receipts: items.map((item) => {
        const observation = item.observation!;
        const lines =
          observation.data.type === "logs"
            ? (observation.data.logs?.lines ?? []).slice(-2)
            : observation.summary
              ? [observation.summary]
              : [];
        return {
          title: observation.title,
          role: item.role ? AGENT_ROLE_LABELS[item.role] : undefined,
          lines,
          gap: item.gap || undefined,
        };
      }),
    };
  }, [investigationCase, run, agentLabel]);
  // While the agent works, name the read in flight instead of describing the
  // pane's mechanics, with how many reads so far and how long it has been.
  // The clock starts when this client sees the turn running; a replay of a
  // finished turn never shows it.
  const turnRunning = lastTurn?.status === "running";
  const runningSinceRef = useRef<number | undefined>(undefined);
  const [, setRunningTick] = useState(0);
  useEffect(() => {
    if (!turnRunning) {
      runningSinceRef.current = undefined;
      return;
    }
    runningSinceRef.current ??= Date.now();
    const id = window.setInterval(() => setRunningTick((t) => t + 1), 1000);
    return () => window.clearInterval(id);
  }, [turnRunning]);
  // A finished read is not what the agent is doing eight seconds later; after
  // a quiet spell the label says it is thinking rather than naming a stale read.
  const lastTool = [...(lastTurn?.timeline ?? [])]
    .reverse()
    .find((item) => item.kind === "tool");
  const toolSignature = lastTool
    ? `${lastTool.id}:${lastTool.status}`
    : undefined;
  const toolChangedAtRef = useRef<number>(Date.now());
  const toolSeenRef = useRef(toolSignature);
  useEffect(() => {
    toolSeenRef.current = toolSignature;
    toolChangedAtRef.current = Date.now();
  }, [toolSignature]);
  const investigatingLabel = investigationRunningLabel({
    lastTool: lastTool && lastTool.kind === "tool" ? lastTool : undefined,
    reads: (lastTurn?.timeline ?? []).filter((item) => item.kind === "tool")
      .length,
    // The render that first shows a changed tool is not quiet yet; the effect
    // that stamps the change runs after it.
    quietFor:
      toolSeenRef.current === toolSignature
        ? Date.now() - toolChangedAtRef.current
        : 0,
    elapsedSeconds: runningSinceRef.current
      ? Math.round((Date.now() - runningSinceRef.current) / 1000)
      : 0,
  });
  const showSplitWorkspace = maximized;
  const splitGridClass = showSplitWorkspace
    ? "@min-[1000px]/investigation:grid-cols-[minmax(320px,min(30%,520px))_minmax(0,1fr)]"
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
    paneSelectionTouchedRef.current = true;
    setNarrowPane(pane);
    if (pane === "evidence") {
      setUnreadEvidence(false);
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
    const source = projection.sources.find((item) => item.id === sourceId);
    if (
      source &&
      evidenceStepIdsByTurn.get(source.turnIndex)?.has(source.stepId)
    ) {
      viewEvidenceSource(source.id);
      return;
    }
    evidenceScrollRef.current?.scrollTo({
      top: 0,
      behavior: prefersReducedMotion() ? "auto" : "smooth",
    });
    setEvidenceUpdateAvailable(false);
  };

  const composer = !unavailable ? (
    <div className="shrink-0 border-t border-theme-border px-3 py-2.5">
      {canStop ? (
        <button
          type="button"
          onClick={stop}
          className="w-full rounded-lg border border-theme-border py-1.5 text-sm text-theme-text-secondary hover:bg-theme-hover"
        >
          Stop agent
        </button>
      ) : run.status === "stopping" ? (
        <div className="px-3 py-2 text-xs text-theme-text-secondary">
          Stopping investigation…
        </div>
      ) : !canContinue ? (
        <div className="px-3 py-2 text-xs text-theme-text-secondary">
          {run.trigger === "background" ? (
            <>
              <p>Automatic investigation · read-only.</p>
              {canInvestigateFurther(run, gone) ? (
                <div className="mt-2 flex flex-wrap items-center gap-2">
                  <button
                    className="btn-brand px-3 py-2 text-xs"
                    onClick={() =>
                      openInvestigation({
                        kind,
                        group: run.group,
                        namespace,
                        name,
                        issueId: run.issueId,
                      })
                    }
                  >
                    Investigate further
                  </button>
                  <span>
                    Re-checks current state, including recent automatic findings
                    when available.
                  </span>
                </div>
              ) : (
                <p>
                  Start a new investigation on this resource to continue
                  digging.
                </p>
              )}
            </>
          ) : (
            "This investigation is read-only."
          )}
        </div>
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
              busy ||
              requestPending ||
              verificationPending
            }
            placeholder={
              !streamReady
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
  ) : null;

  const nextStepsSection =
    hasNextSteps && currentAssessment?.diagnosis ? (
      <section
        ref={nextStepsRef}
        tabIndex={-1}
        aria-labelledby={`${workspaceId}-next-steps`}
        className="investigation-next-steps rounded-xl border p-4 outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
      >
        <h2
          id={`${workspaceId}-next-steps`}
          className="text-lg font-semibold text-theme-text-primary"
        >
          {earlierPlan ? "Earlier proposed steps" : "Next steps"}
        </h2>
        <ResultCard
          diagnosis={currentAssessment.diagnosis}
          section="actions"
          compactActions
          actionNotice={
            earlierPlan
              ? assessmentNeedsCurrentStateVerification
                ? "Proposed before the apply attempt. Current state has not been verified."
                : "Proposed before the latest evidence. Reassess before applying."
              : undefined
          }
          onApply={
            canOfferInvestigationApply({
              currentAssessmentIdx,
              lastRemediationIdx,
              lastApplyAttemptIdx,
              localApplyAttemptAssessmentIdx,
              interactionsBlocked,
              canApply,
              hasNewerEvidence: hasEvidenceCollectedAfterAssessment,
            })
              ? requestApply
              : undefined
          }
          animate={currentAssessment.animateResult !== false}
          showDisclaimer={false}
        />
      </section>
    ) : undefined;

  return (
    <div
      data-investigation-workspace
      className={`@container/investigation relative flex min-h-0 flex-1 flex-col bg-theme-surface ${maximized ? "investigation-split-enabled" : ""}`}
    >
      {stale ? (
        <div className="flex items-center gap-2 border-b border-amber-500/35 bg-amber-500/10 px-3 py-2 text-xs text-theme-text-secondary">
          <AlertTriangle className="h-4 w-4 shrink-0 text-amber-500" />
          <span className="min-w-0 flex-1">
            This investigation ran on{" "}
            <span className="font-medium text-theme-text-primary">
              {parseContextName(run.context).clusterName}
            </span>
            . It is read-only because its agent session was closed after a
            cluster switch.
          </span>
        </div>
      ) : null}
      {!stale && gone ? (
        <div className="flex items-center gap-2 border-b border-theme-border bg-theme-elevated px-3 py-2 text-xs text-theme-text-secondary">
          <AlertTriangle className="h-4 w-4 shrink-0 text-amber-500" />
          <span className="min-w-0 flex-1">
            This investigation is closed and read-only.{" "}
            {turns.length > 0
              ? "Evidence already loaded in this view is preserved, but Radar can no longer continue the run."
              : "It may be private, your access may have changed, or its history may have been cleared. Check your account and organization, or ask the creator for access."}
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
          <div className="relative flex min-h-0 flex-1 flex-col">
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
                          agentLabel={agentLabel}
                          turnIndex={index}
                          evidenceStepIds={evidenceStepIdsByTurn.get(index)}
                          onViewEvidence={viewEvidenceSource}
                          sourceRevealRequest={activityRevealRequest}
                          onViewExplanation={
                            turn.explainAssessment
                              ? () => viewExplanation(turn.explainAssessment!)
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
                          assessment={assessmentIndexes.includes(index)}
                          // The current assessment is Findings; Activity
                          // keeps a pointer to it and the full verdict of
                          // every earlier one, so a revision never erases
                          // what was said before.
                          hideConclusion={index === currentAssessmentIdx}
                          explanation={
                            assessmentIndexes.includes(index) &&
                            index !== currentAssessmentIdx
                              ? explanationFor(turn)
                              : undefined
                          }
                          assessmentSources={(() => {
                            // Every turn with a case shows what it cited;
                            // only the current assessment's notes annotate
                            // Findings, the rest are read-only here.
                            if (
                              turn.apply ||
                              turn.status !== "done" ||
                              !turn.diagnosis ||
                              index === currentAssessmentIdx
                            )
                              return undefined;
                            const turnCase = resolveInvestigationCase(
                              projection,
                              turn.diagnosis,
                              index,
                            );
                            const turnResolution = turn.diagnosis.rootCause
                              ? resolveInvestigationRootCauseEvidence(
                                  projection,
                                  turn.diagnosis.rootCauseEvidence,
                                  index,
                                )
                              : undefined;
                            return turnCase.items.length ||
                              turnResolution?.links.length ||
                              turn.diagnosis.unlinkedEvidence ||
                              turn.diagnosis.evidenceMalformed ||
                              turn.diagnosis.omittedEntries ? (
                              <AssessmentSources
                                renderedGroupIds={visibleEvidenceGroupIds}
                                resolution={turnResolution}
                                investigationCase={turnCase}
                                unlinkedEvidence={
                                  turn.diagnosis.unlinkedEvidence
                                }
                                evidenceMalformed={
                                  turn.diagnosis.evidenceMalformed
                                }
                                omittedEntries={turn.diagnosis.omittedEntries}
                                readOnly
                                onViewSource={viewActivitySource}
                              />
                            ) : undefined;
                          })()}
                        />
                        {showSplitWorkspace &&
                        index === currentAssessmentIdx &&
                        turn.timeline.length === 0 ? (
                          <p className="hidden text-sm text-theme-text-tertiary @min-[1000px]/investigation:block">
                            No reasoning or tool activity was recorded for this
                            assessment. See Findings.
                          </p>
                        ) : null}
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
                                Assessment, evidence and next steps
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
                            {applyOutcomeUncertain &&
                            !displayedVerificationError
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
          </div>
          {composer}
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
              if (event.currentTarget.offsetParent === null) return;
              evidenceScrollTopRef.current = event.currentTarget.scrollTop;
              if (event.currentTarget.scrollTop <= 40) {
                setEvidenceUpdateAvailable(false);
                latestEvidenceUpdateSourceIdRef.current = undefined;
              }
            }}
            className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden px-3 py-3 [overflow-anchor:none] [scrollbar-gutter:stable]"
          >
            <div ref={evidenceContentRef} className="min-w-0 space-y-4">
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
                <div className="space-y-4">
                  {/* One column, one reading order: assessment, analysis, next
                      steps. The page already holds four columns; Findings is
                      not a fifth, and it fills the pane it is given. */}
                  <div className="space-y-3">
                    <section
                      aria-labelledby={`${workspaceId}-assessment-heading`}
                      className="investigation-assessment rounded-xl border p-3"
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
                          Some evidence below was collected after this
                          assessment. Validate the conclusion against it before
                          acting.
                        </p>
                      ) : null}
                      {currentAssessment?.diagnosis ? (
                        <ResultCard
                          key={
                            currentAssessment.resultSequence ??
                            currentAssessmentIdx
                          }
                          diagnosis={currentAssessment.diagnosis}
                          assessmentLimits={
                            storyShape ? assessmentLimits : undefined
                          }
                          assessmentCopy={assessmentCopy}
                          healthSignals={storyShape ? healthSignals : undefined}
                          onRevealSource={viewEvidenceSource}
                          assessmentSources={
                            storyShape ? undefined : assessmentSourcesNode
                          }
                          assessmentAction={
                            hasNextSteps ? (
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
                                {earlierPlan
                                  ? "Earlier proposed steps ↓"
                                  : nextStepLabel}
                              </button>
                            ) : null
                          }
                          explanation={explanationFor(currentAssessment)}
                          section="conclusion"
                          animate={currentAssessment.animateResult !== false}
                          showDisclaimer={false}
                          revisedAfter={
                            currentAssessment.question &&
                            !currentAssessment.verify &&
                            hasMultipleAssessments
                              ? currentAssessment.question
                              : undefined
                          }
                          coverageLimited={currentAssessmentCoverageLimited}
                          evidenceConflict={currentAssessmentEvidenceConflict}
                          evidenceConflictExplainedBy={
                            currentAssessmentEvidenceConflictExplainedBy
                          }
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
                          {busy || requestPending ? (
                            <>
                              <span className="min-w-0 flex-1 truncate">
                                {investigatingLabel.current}
                              </span>
                              {investigatingLabel.meta ? (
                                <span className="shrink-0 tabular-nums text-theme-text-tertiary/80">
                                  {investigatingLabel.meta}
                                </span>
                              ) : null}
                            </>
                          ) : (
                            <span>
                              The agent did not provide a final assessment.
                            </span>
                          )}
                        </div>
                      )}
                      {displayedStatusCheckError ? (
                        <div className="mt-2 flex items-center gap-2 rounded-lg border border-red-500/30 bg-red-500/5 px-3 py-2 text-xs text-theme-text-secondary">
                          <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-red-400" />
                          <span className="min-w-0 flex-1">
                            {applyOutcomeUncertain &&
                            !displayedVerificationError
                              ? displayedStatusCheckError
                              : `Verification did not complete: ${displayedStatusCheckError}`}
                          </span>
                          <button
                            type="button"
                            onClick={checkStatus}
                            disabled={interactionsBlocked}
                            className="shrink-0 rounded-md border border-theme-border px-2 py-1 font-medium text-theme-text-primary hover:bg-theme-hover disabled:opacity-50"
                          >
                            {applyOutcomeUncertain &&
                            !displayedVerificationError
                              ? "Check current status"
                              : "Check current status again"}
                          </button>
                        </div>
                      ) : null}
                    </section>

                    {hasMultipleAssessments ? (
                      <button
                        type="button"
                        data-investigation-earlier-assessments
                        onClick={viewActivity}
                        className="flex items-center gap-1.5 self-start rounded-md px-1.5 py-1 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
                      >
                        <FileClock className="h-3.5 w-3.5" aria-hidden />
                        {assessmentIndexes.length - 1 === 1
                          ? "1 earlier assessment"
                          : `${assessmentIndexes.length - 1} earlier assessments`}
                        <span className="text-theme-text-tertiary">
                          · in Activity
                        </span>
                      </button>
                    ) : null}
                  </div>

                  <InvestigationEvidencePane
                    projection={projection}
                    rootCauseEvidence={paneResolution}
                    investigationCase={investigationCase}
                    story={
                      storyShape && currentAssessment?.diagnosis
                        ? {
                            summary: currentAssessment.diagnosis.summary,
                            report: currentAssessment.diagnosis.report,
                            evidence: currentAssessment.diagnosis.evidence,
                          }
                        : undefined
                    }
                    storyShell={storyShell}
                    summarizedLimits={storyShape ? assessmentLimits : undefined}
                    collecting={
                      explanationRequest?.status !== "running" &&
                      (requestPending ||
                        (busy &&
                          (!lastTurn?.explainAssessment ||
                            lastTurn.timeline.some(
                              (item) => item.kind === "tool",
                            ))))
                    }
                    animateGroupIds={animateEvidenceGroupIds}
                    onViewSource={viewActivitySource}
                    onViewActivity={viewActivity}
                    onOpenResource={stale ? undefined : onOpenResource}
                    onOpenTimeline={stale ? undefined : onOpenTimeline}
                    revealRequest={evidenceRevealRequest}
                    onRevealReady={revealEvidenceSource}
                    afterEvidence={nextStepsSection}
                    recordNotes={recordNotes}
                  />
                </div>
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
        context={run.context}
        fix={pendingFix}
        reason={currentAssessment?.diagnosis?.recommendedReason}
        precondition={
          currentAssessment?.diagnosis?.recommendedIndex
            ? currentAssessment.diagnosis.steps?.[
                currentAssessment.diagnosis.recommendedIndex - 1
              ]?.precondition
            : undefined
        }
        managedBy={run.managedBy}
        confidence={turns[lastRemediationIdx]?.diagnosis?.confidence}
      />
    </div>
  );
}
