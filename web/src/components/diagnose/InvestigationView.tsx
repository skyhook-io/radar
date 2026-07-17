// A view over one durable, server-side investigation run. It SUBSCRIBES to the
// run's event stream (replay + live) and reconstructs the transcript; it does not
// own the run's lifetime — the server does. So closing the panel or navigating
// away just unsubscribes; the run keeps going and re-subscribing replays it.
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Send, AlertTriangle, ArrowDown } from "lucide-react";
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
  RunContextCard,
  appendThinking,
  upsertTool,
  type Turn,
} from "./parts";

const RECHECK_QUESTION =
  "Did the fix resolve the issue? Re-check the resource's current status and health now, and say whether it's healthy.";

export function InvestigationView({
  run,
  agentLabel,
  maximized,
}: {
  run: RunSummary;
  agentLabel: string;
  maximized: boolean;
}) {
  const { kind, namespace, name } = run;
  const { refreshRuns, openInvestigation, startError } = useDiagnose();
  const retryDiagnosis = useCallback(
    () => openInvestigation({ kind, namespace, name }),
    [openInvestigation, kind, namespace, name],
  );
  const queryClient = useQueryClient();
  const [turns, setTurns] = useState<Turn[]>([]);
  // The run is gone server-side (evicted past the retention cap, or lost on a
  // restart) — the stream 404s / closes with nothing to replay. Without this we'd
  // show a silent blank panel; instead we render a "no longer available" state.
  const [gone, setGone] = useState(false);
  const [busy, setBusy] = useState(false);
  const [input, setInput] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const pendingApplyRef = useRef(false);
  // Set when THIS view initiates an apply, consumed once on that apply's done event
  // to auto-run the health re-check (the verification). Ref, not derived from the
  // stream, so replaying a past apply on reopen never re-fires the re-check.
  const autoRecheckRef = useRef(false);
  // Stick-to-bottom: follow streaming output while the user is at/near the bottom,
  // detach the moment they scroll up to read history, re-attach when they return.
  // Tracked from scroll events (the user's intent) — NOT post-render geometry, which
  // mis-detaches whenever a streamed chunk is taller than the threshold.
  const pinnedRef = useRef(true);
  const [showJump, setShowJump] = useState(false);
  const STICK_THRESHOLD = 64; // px from bottom counted as "at the bottom"

  // Staged synthesis beats: when a real diagnosis is ready, hold it for a beat and
  // narrate the closing reasoning ("Formulating the root cause…" → "Weighing
  // remediation options…") before revealing the verdict. These ARE the phases the
  // model just ran; pacing their presentation makes the payoff feel earned instead
  // of dumped. Apply outcomes and plain follow-ups skip it (no root cause to build).
  const [synth, setSynth] = useState<string | null>(null);
  // Controls how much of a freshly-revealed diagnosis the card shows, so the verdict
  // unfolds in beats: "rca" = root cause only (+ a "weighing remediation" beat),
  // "full" = everything. null/"full" for replayed turns (no choreography on rebuild).
  const [reveal, setReveal] = useState<"rca" | "full" | null>(null);
  const synthTimers = useRef<ReturnType<typeof setTimeout>[]>([]);
  const clearSynth = () => {
    synthTimers.current.forEach(clearTimeout);
    synthTimers.current = [];
    setSynth(null);
  };

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

  const updateLast = (fn: (t: Turn) => Turn) =>
    setTurns((prev) => prev.map((t, i) => (i === prev.length - 1 ? fn(t) : t)));

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
  const flushReveal = () => {
    stopReveal();
    const rest = revealBufRef.current;
    revealBufRef.current = "";
    if (rest)
      updateLast((t) => ({ ...t, timeline: appendThinking(t.timeline, rest) }));
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
          timeline: appendThinking(t.timeline, take),
        }));
    }, 150);
  };

  // Subscribe to the run's event stream; rebuild the transcript from scratch on
  // (re)subscribe — the server replays everything, so a fresh tab reconstructs the
  // whole conversation.
  useEffect(() => {
    setTurns([]);
    setGone(false);
    setBusy(false);
    setActionError(null);
    pendingApplyRef.current = false;
    revealBufRef.current = "";
    stopReveal();
    clearSynth();
    setReveal(null);
    // Was the run ALREADY finished when we opened it? Then this subscribe is a
    // replay of history — show every verdict immediately, no staged-reveal beats.
    // The choreography is only for a verdict we watch land live (status running at
    // open). Captured here, not read live, so a follow-up later doesn't re-trigger
    // it for the replayed turns.
    const replaying = run.status !== "running";
    const cancel = subscribeRun(run.id, {
      onEvent: (ev: DiagnoseStreamEvent) => {
        switch (ev.type) {
          case "turn":
            flushReveal(); // close out the prior turn's reasoning before the new one
            clearSynth();
            setReveal(null);
            if (ev.apply) pendingApplyRef.current = true;
            setBusy(true);
            setTurns((prev) => [
              ...prev,
              {
                question: ev.question,
                timeline: [],
                diagnosis: null,
                error: null,
                status: "running",
                apply: ev.apply,
              },
            ]);
            break;
          case "thinking":
            if (ev.token) {
              revealBufRef.current += ev.token;
              pumpReveal();
            }
            break;
          case "step":
            flushReveal(); // reasoning fully precedes the tool it led to
            if (ev.step)
              updateLast((t) => ({
                ...t,
                timeline: upsertTool(t.timeline, ev.step!),
              }));
            break;
          case "done": {
            flushReveal(); // the result can't wait on a reveal animation
            const dx = (ev.diagnosis ?? null) as Diagnosis | null;
            const isApply = pendingApplyRef.current;
            const finalize = () => {
              setBusy(false);
              if (isApply) {
                pendingApplyRef.current = false;
                refreshClusterState();
                // Verify the write automatically: re-check health as a follow-up
                // turn. Guarded by autoRecheckRef so replaying a past apply on
                // reopen never re-fires it.
                if (autoRecheckRef.current && run.status !== "stale") {
                  autoRecheckRef.current = false;
                  setTimeout(() => {
                    addTurn(run.id, { question: RECHECK_QUESTION }).catch(
                      () => {},
                    );
                  }, 900);
                }
              }
              refreshRuns();
            };
            const showCard = (stage: "rca" | "full") => {
              setReveal(stage);
              updateLast((t) => ({ ...t, diagnosis: dx, status: "done" }));
            };
            // Only a real, structured diagnosis earns the staged reveal — and only
            // when watched live. On replay (the run was already finished when we
            // opened it) the beats would just stall showing a verdict that's
            // already known, so we skip straight to the full card.
            const hasRC = !!dx?.rootCause;
            const hasRem = (dx?.remediation?.length ?? 0) > 0;
            const allClear = !!dx?.healthy && !hasRC;
            const inconclusive = !!dx?.inconclusive && !hasRC;
            const structured =
              !!dx && (allClear || inconclusive || hasRC || hasRem);
            if (!isApply && structured && !replaying) {
              const STEP = 2000;
              // Beat 1 (in the timeline): formulating, before any card is shown.
              setReveal(null);
              setSynth(
                allClear
                  ? "Confirming health"
                  : inconclusive
                    ? "Weighing the evidence"
                    : hasRC
                      ? "Formulating the root cause"
                      : "Analyzing the findings",
              );
              synthTimers.current.push(
                setTimeout(() => {
                  setSynth(null);
                  // Reveal the root cause. If remediation follows, the card shows a
                  // "weighing remediation options" beat where the steps will land.
                  showCard(hasRC && hasRem ? "rca" : "full");
                  if (hasRC && hasRem) {
                    synthTimers.current.push(
                      setTimeout(() => {
                        setReveal("full");
                        finalize();
                      }, STEP),
                    );
                  } else {
                    finalize();
                  }
                }, STEP),
              );
            } else {
              setReveal("full");
              updateLast((t) => ({ ...t, diagnosis: dx, status: "done" }));
              finalize();
            }
            break;
          }
          case "error":
            flushReveal();
            updateLast((t) => ({
              ...t,
              error: ev.error || "The investigation failed.",
              status: "error",
            }));
            setBusy(false);
            pendingApplyRef.current = false;
            refreshRuns();
            break;
        }
      },
      // The run can no longer produce events (evicted / gone). Stale runs emit their
      // own error event + banner before closing, so this only bites the case where a
      // run vanishes while we still think it's running — clear the spinner and mark
      // the open turn terminal so it can't shimmer forever.
      onClosed: () => {
        stopReveal();
        clearSynth();
        setBusy(false);
        setGone(true); // render decides: empty → gone state; mid-run → terminal error
        updateLast((t) =>
          t.status === "running"
            ? {
                ...t,
                status: "error",
                error:
                  "This investigation is no longer available. Re-run Diagnose to analyze the current cluster.",
              }
            : t,
        );
      },
    });
    return () => {
      stopReveal();
      clearSynth();
      cancel();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [run.id]);

  // Follow the bottom IFF still pinned, on anything that changes rendered height:
  // new transcript content (turns), the staged verdict reveal (reveal: rca→full
  // adds the remediation card), and synthesis beats (synth). useLayoutEffect runs
  // before paint, so the jump is invisible and it overrides browser scroll-anchoring
  // (which would otherwise nudge us off the bottom when the remediation card lands).
  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (el && pinnedRef.current) el.scrollTop = el.scrollHeight;
  }, [turns, reveal, synth]);

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
    el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
  };

  const stale = run.status === "stale";

  const submitFollowup = () => {
    const q = input.trim();
    if (!q || busy || stale) return;
    setInput("");
    setActionError(null);
    pinnedRef.current = true; // a user-initiated turn always follows to the bottom
    addTurn(run.id, { question: q }).catch((e) =>
      setActionError(e instanceof DiagnoseError ? e.message : "Couldn't send."),
    );
  };
  const stop = () => stopRun(run.id);

  // Ask a canned follow-up (e.g. "explain simply") — a one-tap path that turns the
  // prompt's plain-language instruction into something the user controls.
  const askFollowup = (q: string) => {
    if (busy || stale) return;
    setActionError(null);
    pinnedRef.current = true;
    addTurn(run.id, { question: q }).catch((e) =>
      setActionError(e instanceof DiagnoseError ? e.message : "Couldn't send."),
    );
  };

  // Apply: a user-confirmed remediation turn. Any step is applyable; the chosen
  // step's text is sent so the server binds the apply to it.
  const [confirmApply, setConfirmApply] = useState(false);
  const [pendingFix, setPendingFix] = useState("");
  const requestApply = (fix: string) => {
    setPendingFix(fix);
    setConfirmApply(true);
  };
  const runApply = () => {
    setConfirmApply(false);
    setActionError(null);
    autoRecheckRef.current = true; // verify the write automatically once it lands
    addTurn(run.id, { apply: true, fix: pendingFix }).catch((e) => {
      autoRecheckRef.current = false; // the apply never started — don't auto-recheck
      setActionError(e instanceof DiagnoseError ? e.message : "Couldn't apply.");
    });
  };
  const checkStatus = () => addTurn(run.id, { question: RECHECK_QUESTION }).catch(() => {});

  // Apply tracks the latest turn that produced remediation (so follow-ups don't
  // strip it) and is blocked on a stale (context-switched) run.
  let lastRemediationIdx = -1;
  turns.forEach((t, i) => {
    if (
      t.status === "done" &&
      !t.apply &&
      (t.diagnosis?.remediation?.length ?? 0) > 0
    )
      lastRemediationIdx = i;
  });

  // The "primary verdict" — the latest initial-style structured diagnosis (root
  // cause / remediation / healthy / inconclusive), excluding apply outcomes and
  // conversational follow-ups. In the maximized workspace this pins to a side rail
  // so it (and Apply) stay in view while the transcript scrolls as evidence.
  let pinnedIdx = -1;
  turns.forEach((t, i) => {
    const dx = t.diagnosis;
    const structured =
      !!dx &&
      (!!dx.rootCause ||
        (dx.remediation?.length ?? 0) > 0 ||
        dx.healthy ||
        dx.inconclusive);
    if (t.status === "done" && !t.apply && !t.question && structured)
      pinnedIdx = i;
  });
  const pinned = maximized && pinnedIdx >= 0;

  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div className="flex min-h-0 flex-1">
      <div
        ref={scrollRef}
        onScroll={onScroll}
        className="flex-1 overflow-y-auto overflow-x-hidden px-4 py-3 [scrollbar-gutter:stable]"
      >
        <div className={maximized ? "mx-auto max-w-3xl" : ""}>
          <div className="space-y-4">
            {stale && (
              <div className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-xs text-theme-text-secondary">
                <div className="flex items-start gap-2">
                  <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
                  <span>
                    This investigation ran against{" "}
                    <span className="font-medium text-theme-text-primary">
                      {run.context || "a different cluster"}
                    </span>
                    . The cluster context has changed — it's read-only now.
                  </span>
                </div>
                <button
                  onClick={() => openInvestigation({ kind, namespace, name })}
                  className="mt-2 inline-flex items-center gap-1.5 rounded-md border border-amber-500/50 px-2.5 py-1 font-medium text-amber-600 hover:bg-amber-500/10 dark:text-amber-400"
                >
                  <Send className="h-3 w-3" />
                  Re-run on current cluster
                </button>
              </div>
            )}
            {gone && turns.length === 0 && (
              <div className="rounded-lg border border-theme-border bg-theme-elevated p-3 text-sm text-theme-text-secondary">
                <div className="flex items-start gap-2">
                  <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
                  <span>
                    This investigation is no longer available — history keeps
                    the most recent investigations, and this one has been
                    cleared. Re-run Diagnose to analyze the current cluster.
                  </span>
                </div>
                <button
                  onClick={() => openInvestigation({ kind, namespace, name })}
                  className="mt-2 inline-flex items-center gap-1.5 rounded-md border border-theme-border px-2.5 py-1 font-medium text-theme-text-primary hover:bg-theme-hover"
                >
                  <Send className="h-3 w-3" />
                  Re-run Diagnose
                </button>
              </div>
            )}
            <RunContextCard run={run} />
            {turns.map((t, i) => {
              const isLast = i === turns.length - 1;
              const canApply = i === lastRemediationIdx && !stale;
              const canCheck = isLast && t.status === "done" && !!t.apply;
              return (
                <TurnView
                  key={i}
                  turn={t}
                  synthLabel={isLast ? synth : null}
                  reveal={isLast ? (reveal ?? "full") : "full"}
                  onApply={canApply ? requestApply : undefined}
                  onAsk={isLast && !busy && !stale ? askFollowup : undefined}
                  onCheckStatus={canCheck ? checkStatus : undefined}
                  onRetryDiagnosis={
                    isLast &&
                    t.status === "error" &&
                    !t.question &&
                    !t.apply &&
                    !stale
                      ? retryDiagnosis
                      : undefined
                  }
                  hideVerdict={pinned && i === pinnedIdx}
                />
              );
            })}
            {(actionError || startError) && (
              <div className="flex items-start gap-2 rounded-lg border border-red-500/30 bg-red-500/10 p-3 text-sm text-theme-text-primary">
                <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-red-400" />
                <span>{actionError || startError}</span>
              </div>
            )}
          </div>
        </div>
      </div>
      {pinned && (
        <aside
          className={`w-[400px] shrink-0 overflow-y-auto border-l border-theme-border px-4 py-3 ${busy ? "opacity-70" : ""}`}
        >
          <div className="mb-1 text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
            {busy ? "Verdict · revising…" : "Verdict"}
          </div>
          <ResultCard
            diagnosis={turns[pinnedIdx].diagnosis!}
            onApply={
              pinnedIdx === lastRemediationIdx && !stale ? requestApply : undefined
            }
            onAsk={!busy && !stale ? askFollowup : undefined}
            reveal="full"
          />
        </aside>
      )}
      </div>

      {showJump && (
        <button
          onClick={jumpToBottom}
          className="absolute bottom-20 left-1/2 z-10 flex -translate-x-1/2 items-center gap-1.5 rounded-full border border-theme-border bg-theme-elevated px-3 py-1.5 text-xs font-medium text-theme-text-secondary shadow-theme-md transition hover:bg-theme-hover hover:text-theme-text-primary"
        >
          <ArrowDown className="h-3.5 w-3.5" />
          {busy ? "Jump to latest" : "Scroll to bottom"}
        </button>
      )}

      <ApplyDialog
        open={confirmApply}
        onClose={() => setConfirmApply(false)}
        onConfirm={runApply}
        agentLabel={agentLabel}
        resourceLabel={`${kind} ${namespace ? `${namespace}/` : ""}${name}`}
        fix={pendingFix}
        managedBy={run.managedBy}
        confidence={turns[lastRemediationIdx]?.diagnosis?.confidence}
      />

      <div
        className={`border-t border-theme-border px-3 py-2.5 ${maximized ? "[&>*]:mx-auto [&>*]:max-w-3xl" : ""}`}
      >
        {busy ? (
          <button
            onClick={stop}
            className="w-full rounded-lg border border-theme-border py-1.5 text-sm text-theme-text-secondary hover:bg-theme-hover"
          >
            Stop
          </button>
        ) : (
          <div className="flex items-end gap-2">
            <textarea
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  submitFollowup();
                }
              }}
              rows={1}
              disabled={stale}
              placeholder={
                stale
                  ? "Cluster changed — re-run Diagnose"
                  : "Ask a follow-up or refine…"
              }
              className="max-h-32 min-h-[38px] flex-1 resize-none rounded-lg border border-theme-border bg-theme-base px-3 py-2 text-sm text-theme-text-primary placeholder:text-theme-text-tertiary focus:border-accent focus:outline-none disabled:opacity-50"
            />
            <button
              onClick={submitFollowup}
              disabled={!input.trim() || stale}
              className="shrink-0 rounded-lg btn-brand p-2 disabled:opacity-40"
              aria-label="Send follow-up"
            >
              <Send className="h-4 w-4" />
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
