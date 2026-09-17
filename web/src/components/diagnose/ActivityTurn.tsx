import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  Loader2,
  CheckCircle2,
  AlertTriangle,
  ShieldCheck,
  Sparkles,
  RefreshCw,
  Maximize2,
  HelpCircle,
} from "lucide-react";
import { stringify as toYaml } from "yaml";
import { codeToHtml } from "shiki";
import { DialogPortal } from "@skyhook-io/k8s-ui/components/ui/DialogPortal";
import { useTheme } from "../../context/ThemeContext";
import type {
  Diagnosis,
  DiagnoseStep,
  ApplyMutationOutcome,
  DiagnoseStreamEvent,
  MCPServerStatus,
} from "../../api/diagnose";
import { Collapse, CollapseChevron } from "@skyhook-io/k8s-ui";
import { Tooltip } from "../ui/Tooltip";
import type { InvestigationSourceExcerpt } from "./investigationSourceFocus";
import {
  highlightRelatedEvidence,
  locateSourceExcerpt,
} from "./investigationSourceFocus";
import {
  investigationActivitySourceDomId,
  investigationEvidenceSourceId,
} from "./investigationEvidence";
import { useDisclosureReveal } from "./useDisclosureReveal";
import { Segmented } from "./AgentControls";
import { ApplyOutcomeCard } from "./ApplyDialog";
import {
  type AssessmentExplanation,
  ResultCard,
  WorkingNotes,
} from "./AssessmentCard";
import { AIMarkdown, CopyButton } from "./AIMarkdown";
import { prettyTool } from "./toolCallLabel";

// Turn is one round of the conversation: the initial investigation (no question)
// or a follow-up, each with its own transcript + result.
export type Turn = {
  resultSequence?: number;
  explainAssessment?: number;
  question?: string;
  actor?: string;
  timeline: TimelineItem[];
  diagnosis: Diagnosis | null;
  error: string | null;
  status: "running" | "done" | "error";
  // apply turns execute the recommended fix (write tools) — they report an
  // outcome, not a root cause, so the UI frames them differently.
  apply?: boolean;
  // Set from the apply turn's terminal stream event. Green success is reserved
  // for an explicit producer-confirmed mutation.
  applyOutcome?: ApplyMutationOutcome;
  // Verification turns are structurally a fresh health assessment, even though
  // they carry a question. Keeping the bit explicit prevents the UI from
  // misclassifying them as ordinary conversational follow-ups on replay.
  verify?: boolean;
  // Set from the replay/live boundary when the terminal event arrives. Historical
  // conclusions render immediately; conclusions observed live enter smoothly.
  animateResult?: boolean;
  // The latest startup phase the server reported before the agent's first
  // message. It drives the pending status line only; it is never a transcript item.
  startup?: StartupSignal;
};

export type StartupSignal = {
  phase: "investigating" | "connected" | "ready";
  model?: string;
  toolCount?: number;
  mcpServers?: MCPServerStatus[];
};

const STARTUP_PHASE_RANK: Record<StartupSignal["phase"], number> = {
  investigating: 0,
  connected: 1,
  ready: 2,
};

// Folds a phase event into the turn's startup signal. The handshake and the
// CLI's init line are reported by different goroutines, so a later event can
// name an earlier phase; the furthest phase wins and the init facts are kept.
export function mergeStartupSignal(
  prev: StartupSignal | undefined,
  event: Pick<
    DiagnoseStreamEvent,
    "phase" | "model" | "toolCount" | "mcpServers"
  >,
): StartupSignal | undefined {
  const phase = event.phase;
  if (phase !== "investigating" && phase !== "connected" && phase !== "ready")
    return prev;
  const facts =
    phase === "ready"
      ? {
          model: event.model,
          toolCount: event.toolCount,
          mcpServers: event.mcpServers,
        }
      : {};
  if (prev && STARTUP_PHASE_RANK[prev.phase] >= STARTUP_PHASE_RANK[phase]) {
    return phase === "ready" ? { ...prev, ...facts } : prev;
  }
  return { ...prev, ...facts, phase };
}

function startupLabel(startup: StartupSignal, agentLabel: string): string {
  switch (startup.phase) {
    case "investigating":
      return `${agentLabel} starting…`;
    case "connected":
      return "Connected to Radar's tools";
    case "ready": {
      const parts = [`${agentLabel} ready`];
      if (startup.model) parts.push(startup.model);
      if (startup.toolCount !== undefined)
        parts.push(
          `${startup.toolCount} Radar ${startup.toolCount === 1 ? "tool" : "tools"}`,
        );
      return parts.join(" · ");
    }
  }
}

// TimelineItem is one ordered transcript entry: agent reasoning, or a tool call.
export type TimelineItem =
  | { kind: "thinking"; text: string; animate?: boolean }
  | {
      kind: "tool";
      id: string;
      tool: string;
      status: string;
      ms?: number;
      summary?: string;
      result?: string;
      evidenceRef?: string;
      radarEvidence?: boolean;
      truncated?: boolean;
      // Tri-state by design: false = producer confirmed success, true = producer
      // confirmed failure, undefined = this replay cannot establish the outcome.
      isError?: boolean;
      // Arrival motion is event-local so a replayed running turn can keep receiving
      // live tool calls without reanimating the history reconstructed before it.
      animate?: boolean;
    };

export function appendThinking(
  prev: TimelineItem[],
  text: string,
  animate = true,
): TimelineItem[] {
  const normalized = text.replace(/\*\*\r?\n(?=\*\*)/g, "**\n\n");
  // Agent CLIs commonly emit each short bold planning update as a complete
  // stream event. Preserve those as discrete chronological beats instead of
  // producing invalid/run-on Markdown such as `**one****two**`. Ordinary token
  // chunks still concatenate into their current beat exactly as emitted.
  const blocks = normalized.split(/\n{2,}(?=\s*\*\*)/);
  const next = [...prev];
  for (const block of blocks) {
    if (!block) continue;
    const last = next[next.length - 1];
    const beginsBeat = /^\s*\*\*/.test(block);
    const priorBeatComplete =
      last?.kind === "thinking" && /\*\*\s*$/.test(last.text);
    // Some agents repeat a bold phase heading at stream boundaries. Suppress
    // only that presentation artifact; identical ordinary lines can be real
    // evidence/reasoning and must survive both replay and live chunking.
    if (
      last?.kind === "thinking" &&
      beginsBeat &&
      priorBeatComplete &&
      last.text.trim() === block.trim()
    )
      continue;
    if (last?.kind === "thinking" && !(beginsBeat && priorBeatComplete)) {
      next[next.length - 1] = {
        ...last,
        text: (last.text + block).slice(-4000),
        animate: last.animate === true || animate,
      };
      continue;
    }
    next.push({
      kind: "thinking",
      text: block.trimStart(),
      animate,
    });
  }
  return next;
}

export function upsertTool(
  prev: TimelineItem[],
  step: DiagnoseStep,
  animate = true,
): TimelineItem[] {
  const i = prev.findIndex((it) => it.kind === "tool" && it.id === step.id);
  if (i >= 0) {
    const next = [...prev];
    const cur = next[i] as Extract<TimelineItem, { kind: "tool" }>;
    // The `done` event omits the tool name + input; keep them from `running`.
    next[i] = {
      ...cur,
      ...step,
      kind: "tool",
      tool: step.tool || cur.tool,
      summary: step.summary || cur.summary,
      animate: cur.animate === true || animate,
    };
    return next;
  }
  return [...prev, { kind: "tool", ...step, animate }];
}

export function TurnView({
  turn,
  agentLabel,
  onApply,
  onViewExplanation,
  onCheckStatus,
  onRetryDiagnosis,
  hideConclusion = false,
  assessment = false,
  explanation,
  turnIndex,
  evidenceStepIds,
  onViewEvidence,
  sourceRevealRequest,
  assessmentSources,
}: {
  turn: Turn;
  agentLabel?: string;
  onApply?: (fix: string) => void;
  onViewExplanation?: () => void;
  onCheckStatus?: () => void;
  onRetryDiagnosis?: () => void;
  /** Sources and agent items an answer turn cited; answers otherwise show none. */
  assessmentSources?: ReactNode;
  // The current assessment is Findings; the transcript keeps a pointer to it
  // rather than a second copy (reasoning + tool calls still show).
  hideConclusion?: boolean;
  /** This turn is (or was) an assessment: render its full verdict, never as a conversational answer. */
  assessment?: boolean;
  /** A saved plain-language explanation of an earlier assessment, shown with it here. */
  explanation?: AssessmentExplanation;
  turnIndex?: number;
  evidenceStepIds?: ReadonlySet<string>;
  onViewEvidence?: (sourceId: string) => void;
  sourceRevealRequest?: {
    sourceId: string;
    requestId: number;
    excerpt?: InvestigationSourceExcerpt;
  };
}) {
  // A follow-up (a turn the user asked a question on) is a conversational reply,
  // not a fresh diagnosis — render it as a plain answer, never the root-cause
  // anchor or a remediation card — unless its verdict revised the assessment.
  const followup =
    !!turn.question && !turn.apply && !turn.verify && !assessment;
  // Whether the done turn has anything for ResultCard to render — mirrors its
  // branch order exactly (apply → followup → structured/healthy), since a followup
  // ONLY ever renders FollowupAnswer (report/rootCause), never the remediation list.
  // When false, TurnView shows the narration or an explicit empty note, not a blank.
  const dx = turn.diagnosis;
  const hasResult = dx
    ? followup
      ? !!(dx.report?.trim() || dx.rootCause?.trim()) // FollowupAnswer
      : dx.healthy && !dx.rootCause
        ? true // AllClearCard
        : dx.inconclusive && !dx.rootCause
          ? true // InconclusiveCard
          : !!dx.rootCause ||
            (dx.remediation?.length ?? 0) > 0 ||
            !!dx.report?.trim()
    : false;
  return (
    <div className="space-y-2">
      {turn.explainAssessment ? (
        <button
          type="button"
          onClick={onViewExplanation}
          className="flex items-center gap-1.5 rounded-md py-2 text-xs text-accent-text hover:underline"
        >
          <HelpCircle className="h-3.5 w-3.5" />
          {turn.status === "running"
            ? "Explaining assessment…"
            : turn.status === "error"
              ? "Explanation failed"
              : "Plain-language explanation"}
          <span className="text-theme-text-tertiary">· View in Findings</span>
        </button>
      ) : (
        turn.question &&
        (turn.verify ? (
          <div className="flex items-center gap-2 rounded-md border border-theme-border/60 bg-theme-base/40 px-2.5 py-2 text-xs text-theme-text-secondary">
            <RefreshCw className="h-3.5 w-3.5 shrink-0 text-accent" />
            <span className="font-medium text-theme-text-primary">
              Automatic verification
            </span>
            <span className="text-theme-text-tertiary">
              Re-checking after apply
            </span>
          </div>
        ) : (
          <div className="flex justify-end">
            <div className="max-w-[85%]">
              {turn.actor && (
                <div className="mb-0.5 text-right text-[10px] text-theme-text-tertiary">
                  {turn.actor}
                </div>
              )}
              <div className="rounded-lg rounded-br-sm bg-accent/10 px-3 py-1.5 text-sm text-theme-text-primary [overflow-wrap:anywhere]">
                {turn.question}
              </div>
            </div>
          </div>
        ))
      )}
      <Timeline
        items={turn.timeline}
        running={turn.status === "running"}
        applyMode={turn.apply}
        followup={followup}
        startup={turn.startup}
        agentLabel={agentLabel}
        turnIndex={turnIndex}
        evidenceStepIds={evidenceStepIds}
        onViewEvidence={onViewEvidence}
        sourceRevealRequest={sourceRevealRequest}
      />
      {!turn.explainAssessment &&
        turn.status === "done" &&
        (turn.apply ? (
          <ApplyOutcomeCard
            diagnosis={turn.diagnosis}
            applyOutcome={turn.applyOutcome}
            onCheckStatus={onCheckStatus}
            animate={turn.animateResult !== false}
          />
        ) : hideConclusion && hasResult ? (
          assessment ? (
            <p
              data-turn-assessment-pointer
              className="flex items-center gap-1.5 text-xs text-theme-text-tertiary"
            >
              <Sparkles className="h-3.5 w-3.5 text-accent" aria-hidden />
              Assessment · shown in Findings
            </p>
          ) : null
        ) : hasResult ? (
          <ResultCard
            diagnosis={turn.diagnosis!}
            onApply={onApply}
            followup={followup}
            onCheckStatus={onCheckStatus}
            animate={turn.animateResult !== false}
            assessmentSources={assessmentSources}
            explanation={explanation}
            storyInline={assessment}
            readOnlyAssessment={assessment}
          />
        ) : (
          <EmptyResult animate={turn.animateResult !== false} />
        ))}
      {!turn.explainAssessment &&
      !turn.apply &&
      turn.status === "done" &&
      turn.diagnosis?.notes ? (
        <WorkingNotes notes={turn.diagnosis.notes} />
      ) : null}
      {turn.explainAssessment ? null : turn.status === "error" && turn.apply ? (
        <ApplyOutcomeCard
          diagnosis={turn.diagnosis}
          error={turn.error}
          applyOutcome={turn.applyOutcome}
          onCheckStatus={onCheckStatus}
          animate={turn.animateResult !== false}
        />
      ) : turn.status === "error" && turn.error ? (
        <div className="flex items-start gap-2 rounded-lg border border-red-500/30 bg-red-500/10 p-3 text-sm text-theme-text-primary">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-red-400" />
          <div className="flex min-w-0 flex-col gap-2">
            <span className="whitespace-pre-wrap break-words">
              {turn.error}
            </span>
            {onRetryDiagnosis && (
              <button
                type="button"
                onClick={onRetryDiagnosis}
                className="btn-brand self-start px-3 py-1 text-xs"
              >
                Retry investigation
              </button>
            )}
            {onCheckStatus && (
              <button
                type="button"
                onClick={onCheckStatus}
                className="btn-brand self-start px-3 py-1 text-xs"
              >
                Check current status
              </button>
            )}
          </div>
        </div>
      ) : null}
    </div>
  );
}

export function Timeline({
  items,
  running,
  applyMode,
  followup,
  startup,
  agentLabel = "Agent",
  turnIndex,
  evidenceStepIds,
  onViewEvidence,
  sourceRevealRequest,
}: {
  items: TimelineItem[];
  running: boolean;
  applyMode?: boolean;
  followup?: boolean;
  startup?: StartupSignal;
  agentLabel?: string;
  turnIndex?: number;
  evidenceStepIds?: ReadonlySet<string>;
  onViewEvidence?: (sourceId: string) => void;
  sourceRevealRequest?: {
    sourceId: string;
    requestId: number;
    excerpt?: InvestigationSourceExcerpt;
  };
}) {
  const heading = applyMode
    ? "Applying fix"
    : followup
      ? "Working"
      : "Investigation";
  // The live status verb tracks the running tool ("Reading logs…") so the wait is
  // informative, not a generic spinner; before the first item it reports the
  // startup phases the server actually observed, never a timer-based guess.
  const activeTool = [...items]
    .reverse()
    .find((it) => it.kind === "tool" && it.status !== "done") as
    Extract<TimelineItem, { kind: "tool" }> | undefined;
  const runningLabel = applyMode
    ? "Applying the fix…"
    : activeTool
      ? toolActivity(activeTool.tool)
      : items.length > 0
        ? "Working…"
        : startup
          ? startupLabel(startup, agentLabel)
          : followup
            ? "Thinking…"
            : "Starting investigation…";
  const failedServers = (startup?.mcpServers ?? []).filter(
    (server) => server.status !== "connected",
  );
  const radarServer = failedServers.find((server) => server.name === "radar");
  const allDefiniteFailures = failedServers.every((server) =>
    mcpStatusIsFailure(server.status),
  );
  return (
    <div className="space-y-1.5">
      {items.length > 0 && (
        <div className="text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
          {heading}
        </div>
      )}
      {failedServers.length > 0 && (
        <div
          role="status"
          className="flex items-start gap-1.5 rounded border border-semantic-warning/40 bg-semantic-warning/10 p-2 text-[11px] leading-snug text-theme-text-secondary"
        >
          <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0 text-semantic-warning" />
          <span>
            {failedServers.map((server, i) => (
              <span key={server.name}>
                {i > 0 ? "; " : ""}
                MCP server{" "}
                <span className="font-medium text-theme-text-primary">
                  {server.name}
                </span>{" "}
                {mcpStatusPhrase(server.status)}
              </span>
            ))}
            {radarServer
              ? mcpStatusIsFailure(radarServer.status)
                ? ` at startup. ${agentLabel} had no Radar tools this turn, so it could not use Radar's cluster evidence.`
                : ` at startup. Radar's tools may not have been available to ${agentLabel} this turn.`
              : allDefiniteFailures
                ? ` at startup. ${agentLabel} ran this turn without those tools.`
                : ` at startup. Those tools may not have been available to ${agentLabel} this turn.`}
          </span>
        </div>
      )}
      {items.map((it, i) => {
        if (it.kind === "thinking") {
          return (
            <ThinkingBlock
              key={i}
              text={it.text}
              animate={it.animate !== false}
              live={running && i === items.length - 1}
            />
          );
        }
        const sourceId =
          turnIndex === undefined
            ? undefined
            : investigationEvidenceSourceId(turnIndex, it.id);
        return (
          <ToolRow
            key={it.id}
            step={it}
            sourceId={sourceId}
            hasEvidence={evidenceStepIds?.has(it.id) ?? false}
            onViewEvidence={onViewEvidence}
            revealRequestId={
              sourceRevealRequest && sourceId === sourceRevealRequest.sourceId
                ? sourceRevealRequest.requestId
                : undefined
            }
            sourceExcerpt={
              sourceRevealRequest && sourceRevealRequest.sourceId === sourceId
                ? sourceRevealRequest.excerpt
                : undefined
            }
            animate={it.animate !== false}
          />
        );
      })}
      {running && <RunningStatus label={runningLabel} />}
    </div>
  );
}

// The model's reasoning between tool calls — muted + subordinate to the tool
// rows, and clamped once its beat is over so the chronology stays scannable.
// The final beat remains fully visible while it is streaming; completed prose
// is still available through an overflow-aware disclosure.
function ThinkingBlock({
  text,
  animate,
  live,
}: {
  text: string;
  animate: boolean;
  live: boolean;
}) {
  const [expanded, setExpanded] = useState(false);
  const [contentHeight, setContentHeight] = useState<number>();
  const contentRef = useRef<HTMLDivElement>(null);
  const contentId = useId();
  const reveal = useDisclosureReveal<HTMLDivElement>();
  // Two lines of 12px/19.5px prose plus 8px paragraph/container spacing. Unlike a disclosure
  // this starts with a visible preview, so animate measured height using the
  // same 200ms ease-out/reduced-motion treatment as Collapse.
  const previewHeight = 47;
  const clamped = !live && !expanded;
  useEffect(() => {
    const content = contentRef.current;
    if (!content) return;
    const measure = () => setContentHeight(content.scrollHeight);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(content);
    return () => observer.disconnect();
  }, [text]);

  return (
    <div
      ref={reveal.elementRef}
      className={animate ? "animate-transcript-enter" : ""}
    >
      <div
        id={contentId}
        className="overflow-hidden transition-[height] duration-200 ease-out motion-reduce:transition-none"
        style={{
          height: clamped
            ? Math.min(contentHeight ?? previewHeight, previewHeight)
            : contentHeight,
        }}
      >
        <div ref={contentRef}>
          <AIMarkdown className="py-0.5 text-xs leading-relaxed text-theme-text-tertiary [overflow-wrap:anywhere] [&_li]:text-theme-text-tertiary [&_p]:my-0.5 [&_strong]:font-medium [&_strong]:text-theme-text-secondary">
            {text}
          </AIMarkdown>
        </div>
      </div>
      {!live && ((contentHeight ?? 0) > previewHeight || expanded) ? (
        <button
          type="button"
          aria-controls={contentId}
          aria-expanded={expanded}
          onClick={() => {
            setExpanded(!expanded);
            reveal.revealAfterToggle(!expanded);
          }}
          className="text-[11px] font-medium text-theme-text-tertiary hover:text-accent-text"
        >
          {expanded ? "Show less" : "Show reasoning"}
        </button>
      ) : null}
    </div>
  );
}

// The live "working" line: spinner + shimmering activity verb, plus an elapsed
// counter and — if the same activity sits with no update for a while — a soft
// "still working" reassurance, so a long investigation reads as progress and a
// genuine hang is at least legible (a non-expert can't otherwise tell them apart).
// Self-contained: counts from when this line mounts; the stall timer resets each
// time the label changes (i.e. whenever the agent moves to a new tool/phase).
function RunningStatus({ label }: { label: string }) {
  const [elapsed, setElapsed] = useState(0);
  const elapsedRef = useRef(0);
  const lastChangeRef = useRef(0);
  const prevLabelRef = useRef(label);
  // Reset the stall timer synchronously when the label changes (i.e. the agent moved
  // to a new tool/phase) — doing it during render, not in an effect, so an already-
  // stalled line never flashes "no update for Ns" for a tick before resetting.
  if (prevLabelRef.current !== label) {
    prevLabelRef.current = label;
    lastChangeRef.current = elapsedRef.current;
  }
  useEffect(() => {
    const id = setInterval(() => {
      elapsedRef.current += 1;
      setElapsed(elapsedRef.current);
    }, 1000);
    return () => clearInterval(id);
  }, []);
  const sinceChange = elapsed - lastChangeRef.current;
  const stalled = elapsed >= 30 && sinceChange >= 30;
  const counter = runningElapsedLabel(elapsed);
  return (
    <div className="flex items-center gap-2 pt-1 text-xs">
      <Loader2 className="h-3 w-3 shrink-0 animate-spin text-accent" />
      <span className="ai-shimmer min-w-0 truncate">{label}</span>
      {stalled && (
        <span className="shrink-0 text-theme-text-tertiary">
          · still working — no update for {sinceChange}s
        </span>
      )}
      {counter && (
        <span className="ml-auto shrink-0 tabular-nums text-theme-text-tertiary">
          {counter}
        </span>
      )}
    </div>
  );
}

// The wait the operator feels is the whole turn's, so the counter is a
// row-level figure set apart from the label: "Connected to Radar's tools"
// followed by "10s" read as if the handshake took that long.
export function runningElapsedLabel(elapsed: number): string | undefined {
  return elapsed >= 3 ? `${elapsed}s elapsed` : undefined;
}

// Claude Code reports each MCP server as connected, failed, needs-auth, or
// pending. Only the first two are definite outcomes; anything else is left as
// the CLI's own word so the warning never claims more than it knows.
function mcpStatusIsFailure(status: string): boolean {
  return status === "failed" || status === "needs-auth";
}

function mcpStatusPhrase(status: string): string {
  if (status === "failed") return "failed to connect";
  if (status === "needs-auth") return "needs authentication";
  return `is ${status}`;
}

// Maps a running tool to a human verb so the status line reads as activity, not
// machinery. Falls back to the prettified tool name for anything unmapped.
function toolActivity(tool: string): string {
  const t = tool.toLowerCase();
  if (t.includes("log")) return "Reading logs…";
  if (t.includes("event")) return "Checking recent events…";
  if (t.includes("list")) return "Scanning related resources…";
  if (t.includes("describe") || t.includes("get_resource"))
    return "Inspecting the resource…";
  if (t.includes("resource")) return "Inspecting the resource…";
  if (t.includes("metric") || t.includes("top")) return "Checking metrics…";
  if (t.includes("topology") || t.includes("graph"))
    return "Tracing dependencies…";
  return `${prettyTool(tool)}…`;
}

function ToolRow({
  step,
  sourceId,
  hasEvidence,
  onViewEvidence,
  revealRequestId,
  sourceExcerpt,
  animate,
}: {
  step: Extract<TimelineItem, { kind: "tool" }>;
  sourceId?: string;
  hasEvidence?: boolean;
  onViewEvidence?: (sourceId: string) => void;
  revealRequestId?: number;
  sourceExcerpt?: InvestigationSourceExcerpt;
  animate: boolean;
}) {
  const [open, setOpen] = useState(revealRequestId !== undefined);
  const [showFull, setShowFull] = useState(false);
  const [argumentsOpen, setArgumentsOpen] = useState(false);
  const toolReveal = useDisclosureReveal<HTMLDivElement>();
  const argumentsReveal = useDisclosureReveal<HTMLDivElement>();
  const argumentsId = useId();
  const argumentsPreview = step.summary ? compactArgs(step.summary) : "";
  const inlineArguments =
    argumentsPreview.length <= 240 && !argumentsPreview.includes("\n");
  const detailId = `investigation-tool-detail-${useId().replaceAll(":", "")}`;
  const hasDetail = !!(step.summary || step.result);
  useEffect(() => {
    if (revealRequestId !== undefined && hasDetail) setOpen(true);
  }, [hasDetail, revealRequestId]);
  // Offer the rich dialog when the result is structured or non-trivial in size.
  const richResult =
    !!step.result && (isJsonPayload(step.result) || step.result.length > 200);
  const done = step.status === "done";
  const durationLabel = toolDurationLabel(step.ms);
  const errorReason =
    step.isError === true ? toolErrorReason(step.result) : undefined;
  const outcomeLabel = !done
    ? "Running"
    : step.isError === true
      ? "Tool failed"
      : step.isError === false
        ? "Tool completed"
        : "Tool finished; outcome not recorded";
  const rowContent = (
    <>
      {!done ? (
        <Loader2
          aria-label={outcomeLabel}
          className="h-3.5 w-3.5 shrink-0 animate-spin text-accent"
        />
      ) : step.isError === true ? (
        <AlertTriangle
          aria-label={outcomeLabel}
          className="h-3.5 w-3.5 shrink-0 text-red-400"
        />
      ) : step.isError === false ? (
        <CheckCircle2
          aria-label={outcomeLabel}
          className="h-3.5 w-3.5 shrink-0 text-emerald-400"
        />
      ) : (
        <HelpCircle
          aria-label={outcomeLabel}
          className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary"
        />
      )}
      <span className="shrink-0 font-mono text-xs text-theme-text-secondary">
        {prettyTool(step.tool)}
      </span>
      {step.summary && !open && (
        <span className="min-w-0 flex-1 shrink-[4] truncate font-mono text-[11px] text-theme-text-tertiary">
          {argumentsPreview}
        </span>
      )}
      {errorReason && !open && (
        // The arguments give way first: they are still readable expanded,
        // while the reason is the one thing this row exists to say. It still
        // clips at the row's edge; the full text is a hover and a click away.
        <Tooltip content={errorReason} wrapperClassName="min-w-0 truncate">
          <span className="investigation-tool-reason block min-w-0 truncate text-[11px] text-semantic-error">
            {middleTruncate(errorReason)}
          </span>
        </Tooltip>
      )}
      {durationLabel && (
        <span className="ml-auto shrink-0 text-[11px] text-theme-text-tertiary">
          {durationLabel}
        </span>
      )}
      {hasDetail && <CollapseChevron open={open} className="h-3.5 w-3.5" />}
    </>
  );
  return (
    <div
      onMouseEnter={(event) =>
        highlightRelatedEvidence(event.currentTarget, sourceId)
      }
      onMouseLeave={(event) => highlightRelatedEvidence(event.currentTarget)}
      ref={toolReveal.elementRef}
      id={sourceId ? investigationActivitySourceDomId(sourceId) : undefined}
      tabIndex={sourceId ? -1 : undefined}
      role={sourceId ? "group" : undefined}
      aria-label={
        sourceId
          ? `${prettyTool(step.tool)} ${outcomeLabel.toLowerCase()}`
          : undefined
      }
      className={`${animate ? "animate-transcript-enter" : ""} scroll-mt-3 rounded-md border border-theme-border/60 bg-theme-base/40 outline-none focus:ring-2 focus:ring-accent/50`}
    >
      <div className="flex min-w-0 items-stretch">
        {hasDetail ? (
          <button
            type="button"
            onClick={() => {
              setOpen(!open);
              toolReveal.revealAfterToggle(!open);
            }}
            aria-expanded={open}
            aria-controls={detailId}
            className="flex min-w-0 flex-1 items-center gap-2 px-2 py-1.5 text-left text-sm hover:bg-theme-hover"
          >
            {rowContent}
          </button>
        ) : (
          <div className="flex min-w-0 flex-1 items-center gap-2 px-2 py-1.5 text-left text-sm">
            {rowContent}
          </div>
        )}
        {hasEvidence && sourceId && onViewEvidence ? (
          <button
            type="button"
            onClick={() => onViewEvidence(sourceId)}
            aria-label={`View evidence from ${prettyTool(step.tool)}`}
            className="investigation-evidence-jump shrink-0 px-2 text-[11px] font-medium text-accent-text hover:bg-theme-hover"
          >
            Show evidence
          </button>
        ) : null}
      </div>
      {hasDetail && (
        <div id={detailId}>
          <Collapse open={open}>
            <div className="space-y-2 border-t border-theme-border/60 px-2 py-2">
              {step.summary &&
                (inlineArguments ? (
                  <div
                    className="break-words font-mono text-[11px] leading-relaxed text-theme-text-secondary [overflow-wrap:anywhere]"
                    aria-label="Tool arguments"
                  >
                    {argumentsPreview}
                  </div>
                ) : (
                  <div ref={argumentsReveal.elementRef}>
                    <button
                      type="button"
                      aria-expanded={argumentsOpen}
                      aria-controls={argumentsId}
                      onClick={() => {
                        setArgumentsOpen(!argumentsOpen);
                        argumentsReveal.revealAfterToggle(!argumentsOpen);
                      }}
                      className="flex items-center gap-1 py-1 text-xs text-theme-text-tertiary hover:text-theme-text-primary"
                    >
                      <CollapseChevron
                        open={argumentsOpen}
                        className="h-3.5 w-3.5"
                      />
                      {argumentsOpen ? "Hide arguments" : "Show arguments"}
                    </button>
                    <div id={argumentsId}>
                      <Collapse open={argumentsOpen} mountLazily>
                        <PayloadBlock label="Arguments" text={step.summary} />
                      </Collapse>
                    </div>
                  </div>
                ))}
              {step.result && (
                <PayloadBlock
                  label="Original result"
                  detail={step.ms != null ? `${step.ms}ms` : undefined}
                  text={step.result}
                  sourceExcerpt={sourceExcerpt}
                  revealRequestId={revealRequestId}
                  truncated={step.truncated}
                  action={
                    richResult ? (
                      <button
                        onClick={() => setShowFull(true)}
                        className="flex items-center gap-1 text-[11px] text-accent hover:underline"
                      >
                        <Maximize2 className="h-3 w-3" />
                        View payload
                      </button>
                    ) : undefined
                  }
                />
              )}
            </div>
          </Collapse>
        </div>
      )}
      {step.result && (
        <ToolResultDialog
          open={showFull}
          onClose={() => setShowFull(false)}
          title={prettyTool(step.tool)}
          text={step.result}
          truncated={step.truncated}
        />
      )}
    </div>
  );
}

// Only outliers carry information on the collapsed row: a slow log fetch or a
// probe that hung. Sub-second calls stay silent; the exact figure lives in the
// expanded result header.
export function toolDurationLabel(ms: number | undefined): string | undefined {
  if (ms == null || ms < 2000) return undefined;
  return `${Math.round(ms / 1000)}s`;
}

// The producer's own words for a failed call, reduced to one line. Radar's MCP
// errors are plain text; a JSON envelope with an `error` field is unwrapped.
// Radar's not-found errors append retry hints for the agent after an em dash;
// only the clause before it says what failed, so the hints are dropped.
export function toolErrorReason(
  result: string | undefined,
): string | undefined {
  if (!result) return undefined;
  let text = result;
  try {
    const parsed: unknown = JSON.parse(result);
    if (
      parsed &&
      typeof parsed === "object" &&
      typeof (parsed as { error?: unknown }).error === "string"
    ) {
      text = (parsed as { error: string }).error;
    }
  } catch {
    // plain text
  }
  const line = text
    .split("\n")
    .map((part) => part.trim())
    .find((part) => part.length > 0);
  if (!line) return undefined;
  const clause = line.split(" — ")[0].trim();
  return clause || line;
}

const COLLAPSED_REASON_MAX = 100;

const COLLAPSED_REASON_TAIL = 36;

// Keeps both ends of a long reason: the leading words say what failed and the
// tail usually carries the identifier, which a plain end-truncation would lose.
export function middleTruncate(
  text: string,
  max = COLLAPSED_REASON_MAX,
  tail = COLLAPSED_REASON_TAIL,
): string {
  if (text.length <= max) return text;
  const head = Math.max(1, max - tail - 1);
  return `${text.slice(0, head).trimEnd()}…${text.slice(-tail).trimStart()}`;
}

// isJsonPayload / formatJson — a tool result is "structured" if it parses as JSON.
function isJsonPayload(text: string): boolean {
  try {
    JSON.parse(text);
    return true;
  } catch {
    return false;
  }
}

function formatJson(text: string): string | null {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return null;
  }
}

// PayloadBlock — compact inline view of a tool input/result: pretty JSON (scrolled
// to keep indentation) or wrapped text (logs/prose), with copy + optional action.
function PayloadBlock({
  label,
  detail,
  text,
  truncated,
  action,
  sourceExcerpt,
  revealRequestId,
}: {
  label: string;
  detail?: string;
  text: string;
  truncated?: boolean;
  action?: ReactNode;
  sourceExcerpt?: InvestigationSourceExcerpt;
  revealRequestId?: number;
}) {
  const json = formatJson(text);
  const display = json ?? text;
  const range = locateSourceExcerpt(display, sourceExcerpt);
  const rangeStart = range?.start;
  const rangeEnd = range?.end;
  const preRef = useRef<HTMLPreElement>(null);
  const markRef = useRef<HTMLElement>(null);
  useEffect(() => {
    if (rangeStart === undefined || revealRequestId === undefined) return;
    const frame = requestAnimationFrame(() => {
      const pre = preRef.current,
        mark = markRef.current;
      if (pre && mark)
        pre.scrollTop +=
          mark.getBoundingClientRect().top -
          pre.getBoundingClientRect().top -
          pre.clientHeight / 3;
    });
    return () => cancelAnimationFrame(frame);
  }, [revealRequestId, rangeStart, rangeEnd]);
  return (
    <div>
      <div className="mb-0.5 flex items-center justify-between gap-2">
        <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary">
          {label}
          {detail && (
            <span className="ml-1.5 normal-case tracking-normal">
              · {detail}
            </span>
          )}
        </span>
        <div className="flex items-center gap-2">
          {action}
          <CopyButton
            text={json ?? text}
            label={`Copy ${label.toLowerCase()}`}
          />
        </div>
      </div>
      <pre
        ref={preRef}
        className={`max-h-64 overflow-auto rounded bg-theme-elevated p-1.5 font-mono text-[11px] text-theme-text-secondary ${json && !range ? "" : "whitespace-pre-wrap [overflow-wrap:anywhere]"}`}
      >
        {range ? (
          <>
            {display.slice(0, range.start)}
            <mark
              ref={markRef}
              className="bg-accent-muted text-theme-text-primary ring-1 ring-accent/40"
            >
              {display.slice(range.start, range.end)}
            </mark>
            {display.slice(range.end)}
          </>
        ) : (
          display
        )}
      </pre>
      {truncated && (
        <div className="mt-0.5 text-[10px] text-amber-500">
          Capped at 32 KB — partial output.
        </div>
      )}
    </div>
  );
}

// ToolResultDialog — the rich payload viewer: syntax-highlighted + searchable via
// CodeViewer, with a JSON⇄YAML toggle for structured results (YAML default — k8s
// reads better) and plain text for non-JSON (logs/prose).
function ToolResultDialog({
  open,
  onClose,
  title,
  text,
  truncated,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  text: string;
  truncated?: boolean;
}) {
  const { theme } = useTheme();
  const [fmt, setFmt] = useState<"yaml" | "json">("yaml");
  const parsed = useMemo<{ ok: boolean; value?: unknown }>(() => {
    try {
      return { ok: true, value: JSON.parse(text) };
    } catch {
      return { ok: false };
    }
  }, [text]);

  const display = !parsed.ok
    ? text
    : fmt === "yaml"
      ? safeYaml(parsed.value)
      : JSON.stringify(parsed.value, null, 2);
  const language = parsed.ok ? fmt : "text";

  // Progressive syntax highlighting: render the plain text instantly, then swap in
  // shiki's highlighted HTML once it resolves. A slow/failed highlighter load never
  // blocks the payload (unlike CodeViewer's "Loading…" gate) — worst case it stays
  // plain. Native browser find still works on the rendered text.
  const [html, setHtml] = useState<string | null>(null);
  useEffect(() => {
    if (!open) return;
    setHtml(null);
    let alive = true;
    codeToHtml(display, {
      lang: language,
      theme: theme === "light" ? "github-light" : "github-dark",
    })
      .then((h) => alive && setHtml(h))
      .catch(() => {}); // keep the plain pre on failure
    return () => {
      alive = false;
    };
  }, [open, display, language, theme]);
  return (
    <DialogPortal open={open} onClose={onClose} className="w-[min(90vw,820px)]">
      <div className="flex items-center justify-between gap-3 border-b border-theme-border p-3">
        <div className="min-w-0">
          <div className="truncate font-mono text-sm text-theme-text-primary">
            {title}
          </div>
          {truncated && (
            <div className="text-[11px] text-amber-500">
              Capped at 32 KB — partial output.
            </div>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {parsed.ok && (
            <div className="w-32">
              <Segmented<"yaml" | "json">
                value={fmt}
                onChange={setFmt}
                options={[
                  { value: "yaml", label: "YAML" },
                  { value: "json", label: "JSON" },
                ]}
              />
            </div>
          )}
          <CopyButton text={display} label="Copy tool result" />
        </div>
      </div>
      {html ? (
        <div
          className="animate-code-colorize m-3 max-h-[60vh] overflow-auto rounded-md border border-theme-border bg-theme-base p-3 text-xs leading-relaxed [&_pre]:!m-0 [&_pre]:!bg-transparent [&_pre]:font-mono [&_pre]:!text-xs [&_pre]:!leading-relaxed"
          dangerouslySetInnerHTML={{ __html: html }}
        />
      ) : (
        <pre className="m-3 max-h-[60vh] overflow-auto rounded-md border border-theme-border bg-theme-base p-3 font-mono text-xs leading-relaxed text-theme-text-secondary">
          {display}
        </pre>
      )}
    </DialogPortal>
  );
}

function safeYaml(value: unknown): string {
  try {
    return toYaml(value, { lineWidth: 0 });
  } catch {
    return JSON.stringify(value, null, 2);
  }
}

function compactArgs(raw: string): string {
  try {
    const o = JSON.parse(raw);
    return Object.entries(o)
      .map(([k, v]) => `${k}=${typeof v === "string" ? v : JSON.stringify(v)}`)
      .join(" ");
  } catch {
    return raw;
  }
}

// A follow-up reply: the agent answering a question, not re-diagnosing. Plain
// neutral block — no root-cause anchor, no remediation/apply.
export function FollowupAnswer({
  diagnosis,
  animate,
}: {
  diagnosis: Diagnosis;
  animate: boolean;
}) {
  const text = diagnosis.report || diagnosis.rootCause;
  if (!text) return null;
  return (
    <div
      className={`mt-1 rounded-lg border border-theme-border bg-theme-elevated p-3 ${animate ? "animate-result-in" : ""}`}
    >
      <div className="mb-1.5 flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
          <Sparkles className="h-3.5 w-3.5 text-accent" />
          Answer
        </div>
        <CopyButton text={text} label="Copy answer" />
      </div>
      <AIMarkdown className="text-sm [overflow-wrap:anywhere] [&_code]:font-normal [&_h2:first-child]:mt-0 [&_h2]:mb-1.5 [&_h2]:mt-3 [&_h2]:text-xs [&_h2]:font-semibold [&_h2]:uppercase [&_h2]:tracking-wide [&_h2]:text-theme-text-tertiary [&_h3]:text-sm [&_li]:text-theme-text-secondary [&_p]:my-1.5 [&_p]:text-theme-text-secondary [&_p:first-child]:mt-0">
        {text}
      </AIMarkdown>
    </div>
  );
}

// A done turn that produced no renderable result at all (empty diagnosis, no
// narration). Without this the turn would render blank — which reads as "the tool
// broke." Make the dead-end explicit and point at the recovery (a follow-up).
function EmptyResult({ animate }: { animate: boolean }) {
  return (
    <div
      className={`mt-1 flex items-start gap-2 rounded-lg border border-theme-border bg-theme-elevated p-3 text-sm text-theme-text-secondary ${animate ? "animate-result-in" : ""}`}
    >
      <ShieldCheck className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
      <span>
        The investigation finished without a clear conclusion. Try a follow-up
        question, or investigate again.
      </span>
    </div>
  );
}
