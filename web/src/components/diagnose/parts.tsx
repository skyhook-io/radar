// Presentational pieces of the Diagnose surface — pure + prop-driven (no
// persistence, no app/routing knowledge) so they lift cleanly into k8s-ui later
// and Cloud can reuse them. The stateful controller lives in DiagnoseContext;
// the run logic in InvestigationView.
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
  Copy,
  Check,
  ShieldCheck,
  ChevronRight,
  ChevronDown,
  Wrench,
  Sparkles,
  RefreshCw,
  Maximize2,
  HelpCircle,
} from "lucide-react";
import { stringify as toYaml } from "yaml";
import { codeToHtml } from "shiki";
import { DialogPortal } from "@skyhook-io/k8s-ui/components/ui/DialogPortal";
import { useTheme } from "../../context/ThemeContext";
import { type DiagnoseConsentCopy } from "../../context/DiagnoseCustomization";
import {
  type Diagnosis,
  type DiagnoseStep,
  type AgentInfo,
  type ApplyMutationOutcome,
  type ExecutionProfile,
  type RunSummary,
} from "../../api/diagnose";
import { Collapse, CollapseChevron, StatusDot } from "@skyhook-io/k8s-ui";
import { Markdown } from "../ui/Markdown";
import { Tooltip } from "../ui/Tooltip";
import {
  investigationActivitySourceDomId,
  investigationEvidenceSourceId,
} from "./investigationEvidence";

const CURSOR_FULL_LOCAL_WARNING =
  "Radar passes Cursor --force, which auto-approves its built-in tools and every MCP server it loads, including your global servers. Cursor’s sandbox does not reliably confine those tools to Radar’s temporary workspace.";

// Segmented two-or-more-way selector — shared shape for the agent and execution
// profile pickers.
function Segmented<T extends string | boolean>({
  label,
  options,
  value,
  onChange,
}: {
  label?: string;
  options: { value: T; label: string }[];
  value: T;
  onChange: (v: T) => void;
}) {
  return (
    <div>
      {label && (
        <div className="mb-1.5 text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
          {label}
        </div>
      )}
      <div className="flex gap-1 rounded-lg border border-theme-border bg-theme-base p-1">
        {options.map((o) => (
          <button
            key={String(o.value)}
            onClick={() => onChange(o.value)}
            className={`flex-1 rounded-md px-2 py-1.5 text-xs font-medium transition-colors ${
              o.value === value
                ? "selection-strong selection-text selection-ring"
                : "text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
            }`}
          >
            {o.label}
          </button>
        ))}
      </div>
    </div>
  );
}

type Option = { value: string; label: string; description?: string };

// Claude Code's --model takes version-stable ALIASES that always resolve to the
// user's installed latest of that tier (per `claude --help`), so this list never
// rots across model updates. "" = the agent's own default. Descriptions mirror
// Claude Code's own /model picker so the tradeoff is legible.
const CLAUDE_MODEL_OPTIONS: Option[] = [
  {
    value: "",
    label: "Default",
    description: "Use Claude Code's configured model",
  },
  {
    value: "opus",
    label: "Opus",
    description: "Most capable — best for complex problems",
  },
  {
    value: "sonnet",
    label: "Sonnet",
    description: "Balanced — efficient for routine work",
  },
  { value: "haiku", label: "Haiku", description: "Fastest — quick checks" },
];
// Codex has no stable alias set and no way to enumerate models, and slugs change
// across versions — so we take a free-text override rather than a list that rots.
const EFFORT_OPTIONS: Option[] = [
  {
    value: "",
    label: "Default",
    description: "Recommended — Radar's default (medium)",
  },
  { value: "low", label: "Low", description: "Fastest, least reasoning" },
  { value: "medium", label: "Medium", description: "Balanced depth" },
  { value: "high", label: "High", description: "Most thorough, slowest" },
];

function TextField({
  label,
  value,
  placeholder,
  onChange,
  hint,
}: {
  label: string;
  value: string;
  placeholder?: string;
  onChange: (v: string) => void;
  hint?: string;
}) {
  return (
    <div>
      <div className="mb-1.5 text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
        {label}
      </div>
      <input
        type="text"
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        className="w-full rounded-md border border-theme-border bg-theme-base px-2 py-1.5 text-xs text-theme-text-primary placeholder:text-theme-text-tertiary"
      />
      {hint && (
        <p className="mt-1 text-[11px] leading-snug text-theme-text-tertiary">
          {hint}
        </p>
      )}
    </div>
  );
}

// SelectMenu is a themed dropdown (button + popover list) matching the app's other
// custom dropdowns — unlike a native <select> it renders option descriptions and
// stays on-theme in both light/dark.
function SelectMenu({
  label,
  value,
  options,
  onChange,
  hint,
}: {
  label: string;
  value: string;
  options: Option[];
  onChange: (v: string) => void;
  hint?: string;
}) {
  const [open, setOpen] = useState(false);
  const current = options.find((o) => o.value === value) ?? options[0];
  return (
    <div>
      <div className="mb-1.5 text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
        {label}
      </div>
      <div className="relative">
        <button
          onClick={() => setOpen((v) => !v)}
          aria-haspopup="listbox"
          aria-expanded={open}
          className="flex w-full items-center justify-between gap-2 rounded-md border border-theme-border bg-theme-base px-2.5 py-1.5 text-left text-xs text-theme-text-primary hover:bg-theme-hover"
        >
          <span className="truncate">{current?.label}</span>
          <ChevronDown className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
        </button>
        {open && (
          <>
            <div
              className="fixed inset-0 z-10"
              onClick={() => setOpen(false)}
            />
            <ul
              role="listbox"
              className="absolute left-0 right-0 z-20 mt-1 max-h-72 overflow-y-auto rounded-md border border-theme-border bg-theme-surface py-1 shadow-theme-lg"
            >
              {options.map((o) => {
                const sel = o.value === value;
                return (
                  <li key={o.value}>
                    <button
                      role="option"
                      aria-selected={sel}
                      onClick={() => {
                        onChange(o.value);
                        setOpen(false);
                      }}
                      className="flex w-full items-start gap-2 px-2.5 py-1.5 text-left hover:bg-theme-hover"
                    >
                      <Check
                        className={`mt-0.5 h-3.5 w-3.5 shrink-0 ${sel ? "text-accent" : "opacity-0"}`}
                      />
                      <span className="min-w-0">
                        <span className="block text-xs font-medium text-theme-text-primary">
                          {o.label}
                        </span>
                        {o.description && (
                          <span className="block text-[11px] leading-snug text-theme-text-tertiary">
                            {o.description}
                          </span>
                        )}
                      </span>
                    </button>
                  </li>
                );
              })}
            </ul>
          </>
        )}
      </div>
      {hint && (
        <p className="mt-1 text-[11px] leading-snug text-theme-text-tertiary">
          {hint}
        </p>
      )}
    </div>
  );
}

// AgentControls is the full AI investigation config block (agent, execution profile, model,
// effort) — pure + prop-driven. It lives in Settings, not the investigation panel,
// since these are set-once preferences rather than per-run knobs.
export function AgentControls({
  agents,
  selectedAgent,
  onSelectAgent,
  profile,
  onSetProfile,
  model,
  onSetModel,
  effort,
  onSetEffort,
}: {
  agents: AgentInfo[];
  selectedAgent: string;
  onSelectAgent: (name: string) => void;
  profile: ExecutionProfile;
  onSetProfile: (v: ExecutionProfile) => void;
  model: string;
  onSetModel: (v: string) => void;
  effort: string;
  onSetEffort: (v: string) => void;
}) {
  const isCodex = selectedAgent === "codex";
  const isClaude = selectedAgent === "claude";
  const isCursor = selectedAgent === "cursor-agent";
  const selectedAgentInfo = agents.find((a) => a.name === selectedAgent);
  const selectedAgentLabel =
    selectedAgentInfo?.label || selectedAgent || "agent";
  const profiles = selectedAgentInfo?.profiles ?? [];
  const shownProfile = profiles.includes(profile)
    ? profile
    : (profiles[0] ?? profile);
  const profileLabels: Record<ExecutionProfile, string> = {
    safeguarded: "Radar safeguards",
    "full-local": `Your ${selectedAgentLabel} setup`,
  };
  return (
    <div className="space-y-3">
      {agents.length >= 2 && (
        <Segmented
          label="Agent"
          value={selectedAgent}
          onChange={onSelectAgent}
          options={agents.map((a) => ({
            value: a.name,
            label: a.label || a.name,
          }))}
        />
      )}
      {profiles.length > 0 && (
        <div>
          {profiles.length > 1 ? (
            <>
              <Segmented<ExecutionProfile>
                label="How Radar runs it"
                value={shownProfile}
                onChange={onSetProfile}
                options={profiles.map((value) => ({
                  value,
                  label: profileLabels[value],
                }))}
              />
              {shownProfile === "safeguarded" ? (
                <p className="mt-1.5 text-[11px] leading-snug text-theme-text-tertiary">
                  {isClaude
                    ? "Claude’s built-in tools are disabled, and MCP access is limited to Radar’s read-only investigation tools. Your Claude settings, hooks, and CLAUDE.md instructions still apply and are outside Radar’s control."
                    : isCodex
                      ? "Radar excludes your Codex configuration and other MCP servers. Codex’s sandboxed shell can still read files on this machine; it cannot write or reach the network."
                      : "Radar uses this agent’s safeguarded execution profile. Review the agent’s documented restrictions before continuing."}
                </p>
              ) : (
                <div className="mt-1.5 flex items-start gap-1.5 rounded border border-amber-500/40 bg-amber-500/10 p-2 text-[11px] leading-snug text-theme-text-secondary">
                  <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0 text-amber-500" />
                  <span>
                    Uses your agent&apos;s normal configuration and other
                    configured tools and MCP servers. Radar cannot constrain
                    that external tooling; it may access local files or the
                    network and may be able to change your cluster.{" "}
                    {isCursor
                      ? CURSOR_FULL_LOCAL_WARNING
                      : isClaude
                        ? "Claude uses the permissions from your setup; Radar does not override them."
                        : "Radar still enables the agent CLI’s own sandbox, but that sandbox does not constrain external MCP servers."}{" "}
                    Choose this only when you need that setup.
                  </span>
                </div>
              )}
            </>
          ) : shownProfile === "safeguarded" ? (
            <div className="flex items-start gap-1.5 rounded border border-theme-border bg-theme-base p-2 text-[11px] leading-snug text-theme-text-secondary">
              <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0 text-accent" />
              <span>
                Radar always runs this agent with safeguards.
                {isClaude
                  ? " Claude’s built-in tools are disabled, and MCP access is limited to Radar’s read-only investigation tools. Your Claude settings, hooks, and CLAUDE.md instructions still apply and are outside Radar’s control."
                  : isCodex
                    ? " Your Codex configuration and other MCP servers are excluded. Codex’s sandboxed shell can still read files on this machine; it cannot write or reach the network."
                    : " Review the agent’s documented restrictions before continuing."}
              </span>
            </div>
          ) : (
            <div className="flex items-start gap-1.5 rounded border border-amber-500/40 bg-amber-500/10 p-2 text-[11px] leading-snug text-theme-text-secondary">
              <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0 text-amber-500" />
              <span>
                Radar must use this agent&apos;s normal setup. Radar cannot
                constrain its external tools or MCP servers; they may access
                local files or the network and may be able to change your
                cluster.{" "}
                {isCursor ? (
                  CURSOR_FULL_LOCAL_WARNING
                ) : (
                  <>
                    Radar still enables the agent CLI&apos;s own sandbox, but
                    that sandbox does not constrain external MCP servers.
                  </>
                )}
              </span>
            </div>
          )}
        </div>
      )}
      {isClaude ? (
        <SelectMenu
          label="Model"
          value={model}
          options={CLAUDE_MODEL_OPTIONS}
          onChange={onSetModel}
          hint="Aliases always resolve to the latest of that tier."
        />
      ) : isCodex || isCursor ? (
        <TextField
          label="Model"
          value={model}
          placeholder={
            isCursor
              ? "Default (e.g. auto, gpt-5.2, composer-2.5)"
              : "Default (e.g. gpt-5-codex, o3)"
          }
          onChange={onSetModel}
          hint={
            isCursor
              ? "Leave empty for your Cursor default, or enter a model slug Cursor supports."
              : shownProfile === "full-local"
                ? "Your Codex setup uses its configured model; set a slug here to override it."
                : "Leave empty for Codex's default, or enter a model your Codex version supports."
          }
        />
      ) : (
        <TextField
          label="Model"
          value={model}
          placeholder="Default"
          onChange={onSetModel}
          hint="Leave empty for the agent's default, or enter a model identifier it supports."
        />
      )}
      {isCodex && (
        <SelectMenu
          label="Reasoning effort"
          value={effort}
          options={EFFORT_OPTIONS}
          onChange={onSetEffort}
        />
      )}
    </div>
  );
}

// Turn is one round of the conversation: the initial investigation (no question)
// or a follow-up, each with its own transcript + result.
export type Turn = {
  question?: string;
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
};

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
  onApply,
  onAsk,
  onCheckStatus,
  onRetryDiagnosis,
  hideConclusion = false,
  turnIndex,
  evidenceStepIds,
  onViewEvidence,
  sourceRevealRequest,
}: {
  turn: Turn;
  onApply?: (fix: string) => void;
  onAsk?: (question: string) => void;
  onCheckStatus?: () => void;
  onRetryDiagnosis?: () => void;
  // In the maximized workspace the pinned turn's conclusion renders in the side rail,
  // so the transcript suppresses its own copy (reasoning + tool calls still show).
  hideConclusion?: boolean;
  turnIndex?: number;
  evidenceStepIds?: ReadonlySet<string>;
  onViewEvidence?: (sourceId: string) => void;
  sourceRevealRequest?: { sourceId: string; requestId: number };
}) {
  // A follow-up (a turn the user asked a question on) is a conversational reply,
  // not a fresh diagnosis — render it as a plain answer, never the root-cause
  // anchor or a remediation card.
  const followup = !!turn.question && !turn.apply && !turn.verify;
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
      {turn.question &&
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
            <div className="max-w-[85%] rounded-lg rounded-br-sm bg-accent/10 px-3 py-1.5 text-sm text-theme-text-primary [overflow-wrap:anywhere]">
              {turn.question}
            </div>
          </div>
        ))}
      <Timeline
        items={turn.timeline}
        running={turn.status === "running"}
        applyMode={turn.apply}
        followup={followup}
        turnIndex={turnIndex}
        evidenceStepIds={evidenceStepIds}
        onViewEvidence={onViewEvidence}
        sourceRevealRequest={sourceRevealRequest}
      />
      {turn.status === "done" &&
        (turn.apply ? (
          <ApplyOutcomeCard
            diagnosis={turn.diagnosis}
            applyOutcome={turn.applyOutcome}
            onCheckStatus={onCheckStatus}
            animate={turn.animateResult !== false}
          />
        ) : hideConclusion && hasResult ? null : hasResult ? (
          <ResultCard
            diagnosis={turn.diagnosis!}
            onApply={onApply}
            onAsk={onAsk}
            followup={followup}
            onCheckStatus={onCheckStatus}
            animate={turn.animateResult !== false}
          />
        ) : (
          <EmptyResult animate={turn.animateResult !== false} />
        ))}
      {turn.status === "error" && turn.apply ? (
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

// RunContextCard opens every investigation with what RADAR already knows — the
// health frame the server captured at run start. It renders instantly (no agent
// round-trip), so the agent's boot time reads as "context, then deepening"
// instead of dead air — and it anchors the conclusion against Radar's own signal.
function healthLineTone(severity?: string): "unhealthy" | "degraded" | "alert" {
  if (severity === "critical") return "unhealthy";
  if (severity === "warning") return "degraded";
  return "alert";
}

export function RunContextCard({ run }: { run: RunSummary }) {
  const h = run.health;
  const issueCount = h?.issueCount ?? 0;
  const issues = h?.issues ?? [];
  const findings = h?.auditFindings ?? [];
  const lines: ReactNode[] = [];
  if (issues.length > 0) {
    // The actual issue rows Radar's engine flagged — the reason bolded, the
    // engine's own detail sentence after it. Concrete beats a count.
    for (const [i, line] of issues.entries()) {
      lines.push(
        <div key={`issue-${i}`} className="flex items-start gap-1.5">
          <StatusDot
            tone={healthLineTone(line.severity)}
            className="mt-1 shrink-0"
          />
          <span className="min-w-0">
            <span className="font-medium text-theme-text-primary">
              {line.reason}
            </span>
            {line.message ? <> — {line.message}</> : null}
          </span>
        </div>,
      );
    }
    if (issueCount > issues.length) {
      lines.push(
        <div key="more" className="pl-3.5 text-theme-text-tertiary">
          +{issueCount - issues.length} more active issue
          {issueCount - issues.length === 1 ? "" : "s"}
        </div>,
      );
    }
  } else if (h?.health === "healthy") {
    lines.push(
      <div key="healthy" className="flex items-center gap-1.5">
        <StatusDot tone="healthy" className="shrink-0" />
        Reported healthy — 0 active issues
      </div>,
    );
  } else if (h) {
    lines.push(
      <div key="none" className="flex items-center gap-1.5">
        <StatusDot tone="unknown" className="shrink-0" />0 active issues
        {h.health ? ` — status ${h.health}` : ""}
      </div>,
    );
  }
  for (const [i, f] of findings.entries()) {
    lines.push(
      <div key={`audit-${i}`} className="pl-3.5 text-theme-text-tertiary">
        Audit: <span className="font-medium">{f.reason}</span>
        {f.message ? <> — {f.message}</> : null}
      </div>,
    );
  }
  if ((h?.auditCount ?? 0) > findings.length && findings.length > 0) {
    lines.push(
      <div key="audit-more" className="pl-3.5 text-theme-text-tertiary">
        +{h!.auditCount! - findings.length} more audit finding
        {h!.auditCount! - findings.length === 1 ? "" : "s"}
      </div>,
    );
  }
  if (run.managedBy) {
    lines.push(
      <div key="managed" className="pl-3.5 text-theme-text-tertiary">
        Managed by {run.managedBy}
      </div>,
    );
  }
  if (lines.length === 0) return null;
  return (
    <div className="rounded-md border border-theme-border/60 bg-theme-base/40 px-2.5 py-2">
      <div className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
        Radar&apos;s read at start
      </div>
      <div className="space-y-1 text-xs text-theme-text-secondary">{lines}</div>
    </div>
  );
}

// ConsentCardShell owns the card chrome (icon, layout, settings link, Approve/
// Cancel) so every copy tier — the OSS default, the hosted fallback, and a host
// override — feeds the same frame instead of duplicating it.
function ConsentCardShell({
  title,
  body,
  bullets,
  settingsLabel,
  approveLabel = "Approve & investigate",
  warning = false,
  error,
  onOpenSettings,
  onApprove,
  onCancel,
}: DiagnoseConsentCopy & {
  warning?: boolean;
  // Why the last approval failed, straight from the server. The card stays up on
  // failure so retrying works in place, which means this is the ONLY chance to
  // say why — a host that records consent above the individual refuses the
  // wrong person here, and only its message knows who to ask.
  error?: string | null;
  onOpenSettings?: () => void;
  onApprove: () => void;
  onCancel: () => void;
}) {
  // `settingsLabel: null` hides the link outright — a host with one fixed agent
  // has nothing behind it. `undefined` keeps the OSS default label.
  const resolvedSettingsLabel =
    settingsLabel === undefined
      ? "Change the agent and how it runs in Settings"
      : settingsLabel;
  return (
    <div
      className={
        warning
          ? "rounded-lg border border-amber-500/40 bg-amber-500/10 p-4"
          : "rounded-lg border border-theme-border bg-theme-elevated p-4"
      }
    >
      <div className="mb-2 flex items-center gap-2">
        {warning ? (
          <AlertTriangle className="h-4 w-4 text-amber-500" />
        ) : (
          <ShieldCheck className="h-4 w-4 text-accent" />
        )}
        <div className="text-sm font-medium text-theme-text-primary">
          {title}
        </div>
      </div>
      <div className="text-sm leading-relaxed text-theme-text-secondary">
        {body}
      </div>
      {bullets && bullets.length > 0 && (
        <ul className="mt-2 space-y-1 text-xs text-theme-text-tertiary">
          {bullets.map((b, i) => (
            <li key={i}>• {b}</li>
          ))}
        </ul>
      )}
      {onOpenSettings && resolvedSettingsLabel && (
        <button
          onClick={onOpenSettings}
          className="mt-3 text-xs text-accent hover:underline"
        >
          {resolvedSettingsLabel}
        </button>
      )}
      {/* Red, not amber: the full-local card is itself amber, so an amber box on
          it reads as another paragraph of body copy. role="alert" because nothing
          else moves on failure — focus stays on Approve, so without it a screen
          reader says nothing and the user re-presses a button that cannot succeed. */}
      {error && (
        <div
          role="alert"
          className="mt-3 flex items-start gap-2 rounded-md border border-red-500/30 bg-red-500/10 p-2 text-xs text-theme-text-primary"
        >
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-red-400" />
          <span>{error}</span>
        </div>
      )}
      <div className="mt-4 flex gap-2">
        <button
          onClick={onCancel}
          className="flex-1 rounded-lg border border-theme-border py-1.5 text-sm text-theme-text-secondary hover:bg-theme-hover"
        >
          Cancel
        </button>
        <button
          onClick={onApprove}
          className="flex-1 rounded-lg btn-brand py-1.5 text-sm"
        >
          {approveLabel}
        </button>
      </div>
    </div>
  );
}

// The first-run consent + trust card. The copy is checkable fact about a data
// flow — not marketing — and the wrong claim is a lie, not a typo. It resolves
// in two tiers:
//
//   1. `copy` — the embedding host tells its own trust story. Only the host
//      knows where its agent runs, whose key pays, and where transcripts live,
//      so any host that runs the agent somewhere other than the OSS local CLI
//      MUST override rather than let Radar assert the local story over its flow.
//   2. No `copy` — the OSS bring-your-own-local-CLI default, the only tier where
//      "on your machine / no Radar cloud / your account" actually holds.
export function ConsentCard({
  agentName,
  agent,
  profile,
  copy,
  error,
  onOpenSettings,
  onApprove,
  onCancel,
}: {
  agentName: string;
  agent?: string;
  profile: ExecutionProfile;
  copy?: DiagnoseConsentCopy;
  error?: string | null;
  onOpenSettings?: () => void;
  onApprove: () => void;
  onCancel: () => void;
}) {
  const chrome = { onOpenSettings, onApprove, onCancel, error };

  // Tier 1: a host (e.g. radar-hub-web) supplied its own copy — use it verbatim.
  if (copy) return <ConsentCardShell {...copy} {...chrome} />;

  return (
    <ConsentCardShell
      {...chrome}
      warning={profile === "full-local"}
      approveLabel={
        profile === "full-local"
          ? "Continue with my agent setup"
          : "Approve & investigate"
      }
      title={
        profile === "safeguarded"
          ? "Run an AI investigation with Radar safeguards?"
          : `Run using your ${agentName} setup?`
      }
      body={
        <>
          This runs{" "}
          <span className="font-medium text-theme-text-primary">
            your own {agentName}
          </span>{" "}
          on your machine — no Radar cloud, no API key, no account. Radar sends
          this resource&apos;s spec, recent events, and pod logs to it (and on
          to its model provider under your account, not to Radar). Transcripts
          are kept in your local Radar history on this machine until cleared.
          {profile === "safeguarded" && (
            <>
              {" "}
              Radar&apos;s investigation tools can only{" "}
              <span className="font-medium">read</span> your cluster.
            </>
          )}
        </>
      }
      bullets={
        profile === "safeguarded"
          ? [
              agent === "claude" ? (
                <>
                  Radar safeguards disable Claude&apos;s built-in tools and
                  limit MCP access to Radar&apos;s read-only investigation
                  tools. Your Claude settings, hooks, and CLAUDE.md instructions
                  still apply and are outside Radar&apos;s control.
                </>
              ) : agent === "codex" ? (
                <>
                  Radar safeguards exclude your Codex configuration and other
                  MCP servers. Codex&apos;s sandboxed shell can still read files
                  on this machine; it cannot write or reach the network.
                </>
              ) : (
                <>
                  Radar uses this agent&apos;s safeguarded execution profile.
                  Review the agent&apos;s documented restrictions before
                  continuing.
                </>
              ),
            ]
          : [
              <>
                Radar cannot constrain the agent&apos;s other configured tools
                or MCP servers. They may access local files or the network and
                may be able to change your cluster.
              </>,
              agent === "cursor-agent" ? (
                CURSOR_FULL_LOCAL_WARNING
              ) : agent === "claude" ? (
                <>
                  Claude uses the permissions from your setup; Radar does not
                  override them.
                </>
              ) : (
                <>
                  Radar still enables the agent CLI&apos;s own sandbox, but that
                  sandbox does not constrain external MCP servers.
                </>
              ),
            ]
      }
    />
  );
}

// The Apply confirmation — wider than a generic confirm so the recommended fix
// (rendered markdown) is legible, making it unambiguous what the one click does.
export function ApplyDialog({
  open,
  onClose,
  onConfirm,
  agentLabel,
  resourceLabel,
  fix,
  managedBy,
  confidence,
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  agentLabel: string;
  resourceLabel: string;
  fix?: string;
  managedBy?: string; // GitOps/Helm owner of the resource, if any
  confidence?: number;
}) {
  const fixText = fix?.trim();
  const lowConfidence = confidence != null && confidence < 0.5;
  // A GitOps/Helm-managed resource needs an explicit acknowledgment before applying
  // a direct change — it's the canonical footgun (the controller reverts it). Gating
  // (not just warning) makes the user opt into "yes, I know this may be undone."
  // TODO(SKY-1075): once Radar can connect the user's SCM (GitHub/GitLab/…), replace
  //   direct apply on managed resources with "open a PR against the Git source"
  //   instead — the durable fix. See Linear SKY-1075.
  const [acked, setAcked] = useState(false);
  useEffect(() => {
    if (open) setAcked(false);
  }, [open]);
  const applyBlocked = !!managedBy && !acked;
  return (
    <DialogPortal open={open} onClose={onClose} className="max-w-lg w-full">
      <div className="flex items-start gap-3 border-b border-theme-border p-4">
        <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-amber-500/20">
          <AlertTriangle className="h-5 w-5 text-amber-500" />
        </div>
        <div className="min-w-0 flex-1">
          <h3 className="text-lg font-semibold text-theme-text-primary">
            Apply this fix?
          </h3>
          <p className="mt-1 text-sm text-theme-text-secondary">
            Let {agentLabel} apply the recommended change to{" "}
            <span className="font-medium text-theme-text-primary">
              {resourceLabel}
            </span>
            .
          </p>
        </div>
      </div>

      {fixText && (
        <div className="border-b border-theme-border p-4">
          <div className="mb-1.5 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-accent">
            <Sparkles className="h-3.5 w-3.5" />
            What will happen
          </div>
          <AIMarkdown className="max-h-48 overflow-auto text-sm text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_p]:my-0 [&_p]:text-theme-text-primary [&_pre]:my-1.5">
            {fixText}
          </AIMarkdown>
        </div>
      )}

      <div className="space-y-2 p-4">
        {/* The star warning: when we KNOW a controller owns this resource, a live
            change reverts on the next reconcile — say so authoritatively. */}
        {managedBy && (
          <div className="space-y-2 rounded border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-theme-text-primary">
            <div className="flex items-start gap-2">
              <RefreshCw className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
              <span>
                <span className="font-medium">Managed by {managedBy}.</span>{" "}
                Unless you turn off auto-sync, a direct change here will be
                undone within minutes when {managedBy} re-syncs from Git — the
                durable fix is to change it in Git (the {managedBy} source).
              </span>
            </div>
            <label className="flex cursor-pointer items-center gap-2 pl-6 text-xs text-theme-text-secondary">
              <input
                type="checkbox"
                checked={acked}
                onChange={(e) => setAcked(e.target.checked)}
                className="h-3.5 w-3.5 accent-amber-500"
              />
              I understand {managedBy} may revert this — apply anyway.
            </label>
          </div>
        )}
        {lowConfidence && (
          <div className="flex items-start gap-2 rounded border border-theme-border bg-theme-elevated p-3 text-sm text-theme-text-secondary">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
            <span>
              The agent had <span className="font-medium">low confidence</span>{" "}
              in this conclusion — consider asking a follow-up to verify before
              applying.
            </span>
          </div>
        )}
        <div className="flex items-start gap-2 rounded border border-theme-border bg-theme-base/50 p-3 text-sm text-theme-text-secondary">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
          <span>
            {agentLabel} will change your cluster using your kubeconfig
            credentials. Review the change above; if you&apos;re not sure, ask a
            follow-up first.
          </span>
        </div>
      </div>

      <div className="flex items-center justify-end gap-3 border-t border-theme-border p-4">
        <button
          onClick={onClose}
          className="rounded-lg px-4 py-2 text-sm font-medium text-theme-text-secondary transition-colors hover:bg-theme-elevated hover:text-theme-text-primary"
        >
          Cancel
        </button>
        <button
          onClick={onConfirm}
          disabled={applyBlocked}
          className="flex items-center gap-1.5 rounded-lg btn-brand px-4 py-2 text-sm font-medium disabled:cursor-not-allowed disabled:opacity-50"
        >
          <Wrench className="h-4 w-4" />
          Apply fix
        </button>
      </div>
    </DialogPortal>
  );
}

export function Timeline({
  items,
  running,
  applyMode,
  followup,
  turnIndex,
  evidenceStepIds,
  onViewEvidence,
  sourceRevealRequest,
}: {
  items: TimelineItem[];
  running: boolean;
  applyMode?: boolean;
  followup?: boolean;
  turnIndex?: number;
  evidenceStepIds?: ReadonlySet<string>;
  onViewEvidence?: (sourceId: string) => void;
  sourceRevealRequest?: { sourceId: string; requestId: number };
}) {
  const heading = applyMode
    ? "Applying fix"
    : followup
      ? "Working"
      : "Investigation";
  // The live status verb tracks the running tool ("Reading logs…") so the wait is
  // informative, not a generic spinner; falls back to a phase-appropriate label.
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
        : followup
          ? "Thinking…"
          : "Starting investigation…";
  return (
    <div className="space-y-1.5">
      {items.length > 0 && (
        <div className="text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
          {heading}
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
  const [overflowing, setOverflowing] = useState(false);
  const contentRef = useRef<HTMLDivElement>(null);
  const contentId = useId();
  const clamped = !live && !expanded;

  // Measure the clamped box itself: short beats keep the compact styling but
  // never expose an inert toggle. Pane and font reflow are re-measured.
  useEffect(() => {
    const content = contentRef.current;
    if (!content || !clamped) return;

    const checkOverflow = () =>
      setOverflowing(content.scrollHeight > content.clientHeight + 1);
    checkOverflow();

    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(checkOverflow);
    observer.observe(content);
    return () => observer.disconnect();
  }, [clamped, text]);

  return (
    <div className={animate ? "animate-transcript-enter" : ""}>
      <div
        id={contentId}
        ref={contentRef}
        className={clamped ? "line-clamp-2" : ""}
      >
        <AIMarkdown className="py-0.5 text-xs leading-relaxed text-theme-text-tertiary [overflow-wrap:anywhere] [&_li]:text-theme-text-tertiary [&_p]:my-0.5 [&_strong]:font-medium [&_strong]:text-theme-text-secondary">
          {text}
        </AIMarkdown>
      </div>
      {!live && (overflowing || expanded) ? (
        <button
          type="button"
          aria-controls={contentId}
          aria-expanded={expanded}
          onClick={() => setExpanded((value) => !value)}
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
  return (
    <div className="flex items-center gap-2 pt-1 text-xs">
      <Loader2 className="h-3 w-3 shrink-0 animate-spin text-accent" />
      <span className="ai-shimmer">{label}</span>
      {elapsed >= 3 && (
        <span className="shrink-0 text-theme-text-tertiary">· {elapsed}s</span>
      )}
      {stalled && (
        <span className="shrink-0 text-theme-text-tertiary">
          · still working — no update for {sinceChange}s
        </span>
      )}
    </div>
  );
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
  animate,
}: {
  step: Extract<TimelineItem, { kind: "tool" }>;
  sourceId?: string;
  hasEvidence?: boolean;
  onViewEvidence?: (sourceId: string) => void;
  revealRequestId?: number;
  animate: boolean;
}) {
  const [open, setOpen] = useState(revealRequestId !== undefined);
  const [showFull, setShowFull] = useState(false);
  const detailId = `investigation-tool-detail-${useId().replaceAll(":", "")}`;
  const hasDetail = !!(step.summary || step.result);
  useEffect(() => {
    if (revealRequestId !== undefined && hasDetail) setOpen(true);
  }, [hasDetail, revealRequestId]);
  // Offer the rich dialog when the result is structured or non-trivial in size.
  const richResult =
    !!step.result && (isJsonPayload(step.result) || step.result.length > 200);
  const done = step.status === "done";
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
      {step.summary && (
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-theme-text-tertiary">
          {compactArgs(step.summary)}
        </span>
      )}
      {step.ms != null && (
        <span className="ml-auto shrink-0 text-[11px] text-theme-text-tertiary">
          {step.ms}ms
        </span>
      )}
      {hasDetail && <CollapseChevron open={open} className="h-3.5 w-3.5" />}
    </>
  );
  return (
    <div
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
            onClick={() => setOpen((v) => !v)}
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
            className="shrink-0 border-l border-theme-border/60 px-2 text-[11px] font-medium text-accent hover:bg-theme-hover"
          >
            Evidence
          </button>
        ) : null}
      </div>
      {hasDetail && (
        <div id={detailId}>
          <Collapse open={open}>
            <div className="space-y-2 border-t border-theme-border/60 px-2 py-2">
              {step.summary && (
                <PayloadBlock label="Input" text={step.summary} />
              )}
              {step.result && (
                <PayloadBlock
                  label="Result"
                  text={step.result}
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
  text,
  truncated,
  action,
}: {
  label: string;
  text: string;
  truncated?: boolean;
  action?: ReactNode;
}) {
  const json = formatJson(text);
  return (
    <div>
      <div className="mb-0.5 flex items-center justify-between gap-2">
        <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary">
          {label}
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
        className={`max-h-64 overflow-auto rounded bg-theme-elevated p-1.5 font-mono text-[11px] text-theme-text-secondary ${json ? "" : "whitespace-pre-wrap [overflow-wrap:anywhere]"}`}
      >
        {json ?? text}
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

export function ResultCard({
  diagnosis,
  onApply,
  onAsk,
  apply,
  applyOutcome,
  followup,
  section = "full",
  onCheckStatus,
  animate = true,
  showDisclaimer = true,
  coverageLimited = false,
  evidenceConflict = false,
  compactActions = false,
}: {
  diagnosis: Diagnosis;
  onApply?: (fix: string) => void;
  onAsk?: (question: string) => void;
  apply?: boolean;
  applyOutcome?: ApplyMutationOutcome;
  followup?: boolean;
  section?: "full" | "conclusion" | "actions";
  onCheckStatus?: () => void;
  animate?: boolean;
  /** The Findings workspace already labels the enclosing assessment as AI. */
  showDisclaimer?: boolean;
  /** Qualifies a healthy assessment when structured evidence is absent or partial. */
  coverageLimited?: boolean;
  /** Marks a healthy agent assessment that conflicts with same-turn Key evidence. */
  evidenceConflict?: boolean;
  /** Show only the recommended (or first) action until the user asks for more. */
  compactActions?: boolean;
}) {
  // Apply turns report mutation truth, not a diagnosis. The outcome-specific
  // card decides whether that truth is confirmed, failed, or still unknown.
  if (apply)
    return (
      <ApplyOutcomeCard
        diagnosis={diagnosis}
        applyOutcome={applyOutcome}
        onCheckStatus={onCheckStatus}
        animate={animate}
      />
    );
  // A question remains conversational even if the model happens to set a health
  // flag in its structured envelope. Never promote an ordinary answer into an
  // authoritative investigation conclusion.
  if (followup)
    return <FollowupAnswer diagnosis={diagnosis} animate={animate} />;
  if (diagnosis.healthy && !diagnosis.rootCause)
    return section === "actions" ? null : (
      <AllClearCard
        diagnosis={diagnosis}
        animate={animate}
        showDisclaimer={showDisclaimer}
        coverageLimited={coverageLimited}
        evidenceConflict={evidenceConflict}
      />
    );
  // Couldn't-determine is its own honest state — never a confident all-clear, never
  // the alarming root-cause anchor.
  if (diagnosis.inconclusive && !diagnosis.rootCause)
    return section === "actions" ? null : (
      <InconclusiveCard diagnosis={diagnosis} animate={animate} />
    );
  // A turn with no structured root cause and no remediation (e.g. "looks healthy",
  // or a clarifying question) is not a diagnosis — render it neutrally rather than
  // forcing the alarming root-cause anchor onto a non-problem.
  const structured =
    !!diagnosis.rootCause || (diagnosis.remediation?.length ?? 0) > 0;
  if (!structured)
    return <FollowupAnswer diagnosis={diagnosis} animate={animate} />;

  return (
    <DiagnosisResult
      diagnosis={diagnosis}
      onApply={onApply}
      onAsk={onAsk}
      section={section}
      animate={animate}
      showDisclaimer={showDisclaimer}
      compactActions={compactActions}
    />
  );
}

const EXPLAIN_SIMPLY_PROMPT =
  "Explain this in plain language for someone who isn't a Kubernetes expert — what's broken, why it matters, and what each remediation step actually does. Gloss any k8s terms.";

// The diagnosis result: likely cause + remediation + the
// agent's full analysis on demand.
function DiagnosisResult({
  diagnosis,
  onApply,
  onAsk,
  section = "full",
  animate,
  showDisclaimer,
  compactActions,
}: {
  diagnosis: Diagnosis;
  onApply?: (fix: string) => void;
  onAsk?: (question: string) => void;
  section?: "full" | "conclusion" | "actions";
  animate: boolean;
  showDisclaimer: boolean;
  compactActions: boolean;
}) {
  const [showAnalysis, setShowAnalysis] = useState(false);
  const [showAllSteps, setShowAllSteps] = useState(false);
  const analysisId = useId();
  // Only a real structured cause anchors the amber card; the full prose lives in
  // "Full analysis" (never relabel the report as a causal assessment).
  const rootCause = diagnosis.rootCause;
  const remediation = diagnosis.remediation || [];
  const hasRemediation = remediation.length > 0;
  const recIdx = diagnosis.recommendedIndex;
  const recValid =
    recIdx != null && recIdx >= 1 && recIdx <= remediation.length;
  // Apply is offered ONLY when the agent pointed at a safe step (recommended_index).
  // When it returns 0 / none ("needs human judgement"), we honor that and don't
  // offer one-click apply — the steps stay copy-only with a note.
  const canApply = !!onApply && recValid;
  const showConclusion = section !== "actions";
  const showActions = section !== "conclusion";
  const remediationEntries = remediation.map((text, index) => ({
    text,
    index,
  }));
  const primaryActionIndex = recValid ? recIdx! - 1 : 0;
  const visibleRemediation =
    compactActions && !showAllSteps
      ? remediationEntries.filter(({ index }) => index === primaryActionIndex)
      : remediationEntries;
  const hiddenStepCount = remediation.length - visibleRemediation.length;
  return (
    <div className={`mt-3 space-y-2 ${animate ? "animate-result-in" : ""}`}>
      {/* Likely cause — agent-authored, visually prominent without claiming proof. */}
      {showConclusion && rootCause && (
        <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 p-3">
          <div className="mb-1 flex items-center justify-between gap-2">
            <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-amber-500">
              <AlertTriangle className="h-3.5 w-3.5" />
              Likely cause
            </div>
            <div className="flex items-center gap-2">
              {diagnosis.confidence != null ? (
                <ConfidenceMeter value={diagnosis.confidence} />
              ) : (
                <ConfidenceUnstated />
              )}
              <CopyButton text={rootCause} label="Copy likely cause" />
            </div>
          </div>
          <AIMarkdown className="text-sm font-medium text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_p]:my-0 [&_p]:text-theme-text-primary">
            {rootCause}
          </AIMarkdown>
          {onAsk && (
            <button
              onClick={() => onAsk(EXPLAIN_SIMPLY_PROMPT)}
              className="mt-2 inline-flex items-center gap-1 rounded-md border border-theme-border px-2 py-1 text-[11px] font-medium text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
            >
              <HelpCircle className="h-3 w-3" />
              Explain simply
            </button>
          )}
        </div>
      )}

      {/* Remediation — every step is copyable. Only the explicitly recommended
          step can be applied, and only when the caller enables apply. */}
      {showActions && hasRemediation && (
        <div className="rounded-lg border border-theme-border bg-theme-elevated p-3">
          <div className="mb-2 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
            <Wrench className="h-3.5 w-3.5 text-accent" />
            Remediation
          </div>
          <ol className="space-y-2">
            {visibleRemediation.map(({ text: r, index: i }) => {
              const isRec = recValid && i === recIdx! - 1;
              return (
                <li
                  key={i}
                  className={
                    isRec
                      ? "rounded-lg border border-accent/40 bg-accent/5 p-2.5"
                      : ""
                  }
                >
                  <div className="flex items-start gap-2">
                    <span
                      className={`mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded-full text-[10px] ${
                        isRec
                          ? "bg-accent/20 text-accent"
                          : "bg-theme-base text-theme-text-tertiary"
                      }`}
                    >
                      {i + 1}
                    </span>
                    <div className="min-w-0 flex-1">
                      {isRec && (
                        <div className="mb-1">
                          <div className="flex items-center gap-1 text-[10px] font-semibold uppercase tracking-wide text-accent">
                            <Sparkles className="h-3 w-3" />
                            Recommended
                          </div>
                          {diagnosis.recommendedReason && (
                            <div className="mt-0.5 text-[11px] leading-snug text-theme-text-tertiary">
                              {diagnosis.recommendedReason}
                            </div>
                          )}
                        </div>
                      )}
                      <AIMarkdown className="text-sm [overflow-wrap:anywhere] [&_p]:my-0 [&_pre]:my-1.5">
                        {r}
                      </AIMarkdown>
                    </div>
                    {/* Action cluster: compact Apply (recommended = subtly
                        filled, others = ghost) sits next to Copy so each row's
                        actions stay together. The ellipsis signals a confirm
                        dialog follows — it doesn't apply immediately. */}
                    <div className="flex shrink-0 items-center gap-0.5">
                      {canApply && isRec && (
                        <button
                          onClick={() => onApply!(r)}
                          className={`inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs font-medium text-accent transition-colors ${
                            isRec
                              ? "border border-accent/40 bg-accent/10 hover:bg-accent/20"
                              : "hover:bg-accent/10"
                          }`}
                        >
                          <Wrench className="h-3 w-3" />
                          Apply…
                        </button>
                      )}
                      <CopyButton
                        text={r}
                        label={`Copy remediation step ${i + 1}`}
                      />
                    </div>
                  </div>
                </li>
              );
            })}
          </ol>
          {compactActions && remediation.length > 1 ? (
            <button
              type="button"
              onClick={() => setShowAllSteps((value) => !value)}
              className="mt-2 inline-flex items-center gap-1 rounded-md px-1.5 py-1 text-[11px] font-medium text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
            >
              <ChevronRight
                className={`h-3.5 w-3.5 transition-transform ${showAllSteps ? "rotate-90" : ""}`}
              />
              {showAllSteps
                ? recValid
                  ? "Show only recommended step"
                  : "Show only first step"
                : `Show ${hiddenStepCount} more ${hiddenStepCount === 1 ? "step" : "steps"}`}
            </button>
          ) : null}
          {!recValid && (
            <p className="mt-2 flex items-start gap-1.5 text-[11px] leading-snug text-theme-text-tertiary">
              <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0" />
              No one-click fix is available. Review these steps and apply them
              manually, or ask the agent to continue.
            </p>
          )}
        </div>
      )}

      {/* Full analysis — the agent's detailed evidence, on demand. */}
      {showConclusion && diagnosis.report && (
        <div className="rounded-lg border border-theme-border bg-theme-elevated">
          <button
            type="button"
            aria-expanded={showAnalysis}
            aria-controls={analysisId}
            onClick={() => setShowAnalysis((v) => !v)}
            className="flex w-full items-center gap-1.5 px-3 py-2 text-xs font-medium uppercase tracking-wide text-theme-text-tertiary hover:text-theme-text-primary"
          >
            <ChevronRight
              className={`h-3.5 w-3.5 transition-transform ${showAnalysis ? "rotate-90" : ""}`}
            />
            Full analysis
          </button>
          <div id={analysisId}>
            <Collapse open={showAnalysis}>
              <div className="border-t border-theme-border/60 px-3 py-2">
                <AIMarkdown className="text-sm [overflow-wrap:anywhere] [&_h2:first-child]:mt-0 [&_h2]:mb-1.5 [&_h2]:mt-3 [&_h2]:text-xs [&_h2]:font-semibold [&_h2]:uppercase [&_h2]:tracking-wide [&_h2]:text-theme-text-tertiary [&_h3]:text-sm [&_li]:text-theme-text-secondary [&_p]:my-1.5 [&_p]:text-theme-text-secondary">
                  {diagnosis.report}
                </AIMarkdown>
              </div>
            </Collapse>
          </div>
        </div>
      )}

      {showConclusion && showDisclaimer && (
        <div className="flex items-start gap-1 px-0.5 text-[11px] text-theme-text-tertiary">
          <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0" />
          <span>AI-generated — review before applying</span>
        </div>
      )}
    </div>
  );
}

function AllClearCard({
  diagnosis,
  animate,
  showDisclaimer,
  coverageLimited,
  evidenceConflict,
}: {
  diagnosis: Diagnosis;
  animate: boolean;
  showDisclaimer: boolean;
  coverageLimited: boolean;
  evidenceConflict: boolean;
}) {
  const [showAnalysis, setShowAnalysis] = useState(false);
  const analysisId = useId();
  const report =
    diagnosis.report ||
    "The agent did not identify a problem in the evidence it checked.";
  const detailed = report.length > 320 || report.split("\n").length > 2;
  const summary = detailed
    ? "The agent found no active problem in the evidence it reviewed."
    : report;
  return (
    <div className={`mt-3 space-y-2 ${animate ? "animate-result-in" : ""}`}>
      <div
        className={`rounded-lg border p-3 ${
          evidenceConflict
            ? "border-amber-500/40 bg-amber-500/5"
            : coverageLimited
              ? "border-amber-500/30 bg-amber-500/5"
              : "border-emerald-500/30 bg-emerald-500/5"
        }`}
      >
        <div className="mb-1 flex items-center justify-between gap-2">
          <div
            className={`flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide ${
              evidenceConflict || coverageLimited
                ? "text-amber-500"
                : "text-emerald-500"
            }`}
          >
            {evidenceConflict || coverageLimited ? (
              <AlertTriangle className="h-3.5 w-3.5" />
            ) : (
              <CheckCircle2 className="h-3.5 w-3.5" />
            )}
            {evidenceConflict
              ? "Assessment conflicts with captured evidence"
              : coverageLimited
                ? "No problem identified in available evidence"
                : "No problem found in checked evidence"}
          </div>
          <CopyButton text={report} label="Copy assessment" />
        </div>
        <AIMarkdown className="text-sm text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_li]:text-theme-text-primary [&_p]:my-1 [&_p]:text-theme-text-primary [&_p:first-child]:mt-0 [&_p:last-child]:mb-0">
          {summary}
        </AIMarkdown>
        {evidenceConflict ? (
          <p className="mt-2 text-xs text-theme-text-secondary">
            Radar also captured evidence of an active problem. Review that
            evidence before treating the agent&apos;s conclusion as an
            all-clear.
          </p>
        ) : coverageLimited ? (
          <p className="mt-2 text-xs text-theme-text-secondary">
            Some evidence could not be summarized or is unavailable. Review
            Radar&apos;s observations and Activity before treating this as an
            all-clear.
          </p>
        ) : null}
      </div>
      {detailed ? (
        <div className="rounded-lg border border-theme-border bg-theme-elevated">
          <button
            type="button"
            aria-expanded={showAnalysis}
            aria-controls={analysisId}
            onClick={() => setShowAnalysis((value) => !value)}
            className="flex w-full items-center gap-1.5 px-3 py-2 text-xs font-medium uppercase tracking-wide text-theme-text-tertiary hover:text-theme-text-primary"
          >
            <ChevronRight
              className={`h-3.5 w-3.5 transition-transform ${showAnalysis ? "rotate-90" : ""}`}
            />
            Full analysis
          </button>
          <div id={analysisId}>
            <Collapse open={showAnalysis}>
              <div className="border-t border-theme-border/60 px-3 py-2">
                <AIMarkdown className="text-sm [overflow-wrap:anywhere] [&_p]:my-1.5 [&_p]:text-theme-text-secondary [&_p:first-child]:mt-0 [&_p:last-child]:mb-0">
                  {report}
                </AIMarkdown>
              </div>
            </Collapse>
          </div>
        </div>
      ) : null}
      {showDisclaimer ? (
        <div className="flex items-start gap-1 px-0.5 text-[11px] text-theme-text-tertiary">
          <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0" />
          <span>AI-generated — verify if symptoms persist</span>
        </div>
      ) : null}
    </div>
  );
}

// The agent investigated but couldn't determine an answer. A distinct, honest
// state — neutral (not the alarming amber root cause, not the reassuring emerald
// all-clear) — so "I couldn't tell" never reads as "you're fine."
function InconclusiveCard({
  diagnosis,
  animate,
}: {
  diagnosis: Diagnosis;
  animate: boolean;
}) {
  const text =
    diagnosis.report ||
    "The investigation couldn't reach a clear conclusion — some information was unavailable or the evidence was ambiguous.";
  return (
    <div className={`mt-3 space-y-2 ${animate ? "animate-result-in" : ""}`}>
      <div className="rounded-lg border border-theme-border bg-theme-elevated p-3">
        <div className="mb-1 flex items-center justify-between gap-2">
          <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-theme-text-secondary">
            <HelpCircle className="h-3.5 w-3.5" />
            Couldn&apos;t determine
          </div>
          <CopyButton text={text} label="Copy assessment" />
        </div>
        <AIMarkdown className="text-sm text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_li]:text-theme-text-primary [&_p]:my-1 [&_p]:text-theme-text-primary [&_p:first-child]:mt-0 [&_p:last-child]:mb-0">
          {text}
        </AIMarkdown>
      </div>
      <div className="flex items-start gap-1 px-0.5 text-[11px] text-theme-text-tertiary">
        <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0" />
        <span>
          Try a follow-up with more context, or investigate again after
          addressing any errors shown in Activity.
        </span>
      </div>
    </div>
  );
}

// A follow-up reply: the agent answering a question, not re-diagnosing. Plain
// neutral block — no root-cause anchor, no remediation/apply.
function FollowupAnswer({
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

// The result of an apply turn. Mutation truth comes from Radar write-tool
// results, not the agent's prose or process exit. Missing outcome metadata is
// therefore fail-closed as unknown; only explicit confirmation renders green.
function ApplyOutcomeCard({
  diagnosis,
  error,
  applyOutcome,
  onCheckStatus,
  animate,
}: {
  diagnosis: Diagnosis | null;
  error?: string | null;
  applyOutcome?: ApplyMutationOutcome;
  onCheckStatus?: () => void;
  animate: boolean;
}) {
  const outcome = diagnosis?.report || diagnosis?.rootCause;
  const status = applyOutcome ?? "unknown";
  const confirmed = status === "confirmed";
  const failed = status === "failed";
  const heading = confirmed
    ? "Applied"
    : failed
      ? "Not applied"
      : "Outcome unknown";
  const detail = error || outcome;
  const fallbackDetail = confirmed
    ? "Radar confirmed that the change was applied. Check the current state to confirm its effect."
    : failed
      ? "Radar could not confirm that the change was applied. Review Activity before retrying."
      : "Radar could not confirm whether the change was applied. Check the current state before retrying.";
  const containerClass = confirmed
    ? "border-emerald-500/30 bg-emerald-500/5"
    : failed
      ? "border-red-500/30 bg-red-500/5"
      : "border-amber-500/40 bg-amber-500/10";
  const accentClass = confirmed
    ? "text-emerald-500"
    : failed
      ? "text-red-400"
      : "text-amber-400";
  const buttonClass = confirmed
    ? "border-emerald-500/40 text-emerald-500 hover:bg-emerald-500/10"
    : "border-amber-500/40 text-amber-400 hover:bg-amber-500/10";
  const provenance = confirmed
    ? error
      ? "Radar confirmed the change — the agent report is incomplete"
      : "Radar confirmed the change was applied"
    : failed
      ? "Radar did not confirm that the change was applied"
      : "Radar cannot confirm whether the change was applied";
  return (
    <div className={`mt-3 space-y-2 ${animate ? "animate-result-in" : ""}`}>
      <div className={`rounded-lg border p-3 ${containerClass}`}>
        <div className="mb-1 flex items-center justify-between gap-2">
          <div
            className={`flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide ${accentClass}`}
          >
            {confirmed ? (
              <CheckCircle2 className="h-3.5 w-3.5" />
            ) : (
              <AlertTriangle className="h-3.5 w-3.5" />
            )}
            {heading}
          </div>
          {detail && <CopyButton text={detail} label="Copy apply result" />}
        </div>
        <AIMarkdown className="text-sm text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_li]:text-theme-text-primary [&_p]:my-1 [&_p]:text-theme-text-primary [&_p:first-child]:mt-0 [&_p:last-child]:mb-0">
          {detail || fallbackDetail}
        </AIMarkdown>
        {!confirmed && !failed && (
          <div className="mt-2 flex items-start gap-1.5 text-xs font-medium text-amber-300">
            <RefreshCw className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            <span>
              Check the current state before trying to apply this change again.
            </span>
          </div>
        )}
        {onCheckStatus && !failed && (
          <button
            type="button"
            onClick={onCheckStatus}
            className={`mt-3 flex w-full items-center justify-center gap-1.5 rounded-lg border py-2 text-sm font-medium ${buttonClass}`}
          >
            <RefreshCw className="h-4 w-4" />
            Check current status
          </button>
        )}
      </div>
      <div className="flex items-center gap-1 px-0.5 text-[11px] text-theme-text-tertiary">
        <ShieldCheck className="h-3 w-3 shrink-0" />
        <span className="truncate">{provenance}</span>
      </div>
    </div>
  );
}

function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Tooltip
      content={copied ? "Copied" : label}
      delay={100}
      wrapperClassName="shrink-0"
    >
      <button
        onClick={() => {
          navigator.clipboard?.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1200);
        }}
        className="shrink-0 rounded p-1 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
        aria-label={copied ? `${label} — copied` : label}
        aria-live="polite"
      >
        {copied ? (
          <Check className="h-3.5 w-3.5 text-emerald-400" />
        ) : (
          <Copy className="h-3.5 w-3.5" />
        )}
      </button>
    </Tooltip>
  );
}

export function prettyTool(tool: string): string {
  return tool.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
}

// A coarse band, not a precise %: a two-sig-fig confidence on an LLM judgement
// reads as calibrated when it isn't.
function confidenceLabel(c: number): string {
  if (c >= 0.8) return "High";
  if (c >= 0.5) return "Medium";
  return "Low";
}

// Confidence shown as a band + three discrete pips (Low=1, Med=2, High=3 filled).
// Deliberately discrete, NOT a continuous bar: a filled fraction reads as a precise
// percentage, which is exactly the false calibration `confidenceLabel` exists to
// avoid (an LLM's two-sig-fig confidence isn't that precise). Accent-toned so it
// reads as "trust in the analysis," not problem severity.
function ConfidenceMeter({ value }: { value: number }) {
  const band = confidenceLabel(value);
  const filled = band === "High" ? 3 : band === "Medium" ? 2 : 1;
  return (
    <span className="flex items-center gap-1.5">
      <span className="text-[11px] text-theme-text-tertiary">
        Agent confidence: {band}
      </span>
      <span className="flex items-center gap-0.5" aria-hidden>
        {[0, 1, 2].map((i) => (
          <span
            key={i}
            className={`h-1 w-2.5 rounded-full ${i < filled ? "bg-accent" : "bg-theme-base"}`}
          />
        ))}
      </span>
    </span>
  );
}

// Shown when the model returned no confidence at all — so "unknown confidence"
// is visible rather than silently absent (which looks identical to high confidence
// minus the badge) on a trust-bearing surface.
function ConfidenceUnstated() {
  return (
    <span className="text-[11px] text-theme-text-tertiary">
      Agent confidence: not stated
    </span>
  );
}

// LLMs occasionally open a ```fence mid-line ("run this: ```bash kubectl …") or
// put the command on the same line as the ```lang marker. GFM then won't parse
// it as a fence — it leaks the literal ``` and renders an empty code box. Coerce
// fence markers onto their own lines and push trailing content off the opener so
// the block renders. (Well-formed markdown is unaffected.)
function tidyFences(md: string): string {
  if (!md || !md.includes("```")) return md;
  return md
    .replace(/([^\n])```/g, "$1\n\n```") // opener/closer must start a line
    .replace(/```([A-Za-z0-9_-]*)[ \t]+(\S)/g, "```$1\n$2"); // content off the opener line
}

// Diagnosis output is dense with inline `code`; the shared chip's brand tint is
// too loud at that density, so neutralize it (border/bg only) for this surface.
const SOFT_INLINE_CODE =
  "[&_.inline-code]:border-theme-border/60 [&_.inline-code]:bg-theme-base [&_.inline-code]:font-normal";

// Markdown for agent-generated text — normalizes flaky fences + softens code.
function AIMarkdown({
  className,
  children,
}: {
  className?: string;
  children: string;
}) {
  return (
    <Markdown className={`${SOFT_INLINE_CODE} ${className ?? ""}`}>
      {tidyFences(children)}
    </Markdown>
  );
}
