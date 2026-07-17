// Presentational pieces of the Diagnose surface — pure + prop-driven (no
// persistence, no app/routing knowledge) so they lift cleanly into k8s-ui later
// and Cloud can reuse them. The stateful controller lives in DiagnoseContext;
// the run logic in InvestigationView.
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
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
import {
  type Diagnosis,
  type DiagnoseStep,
  type AgentInfo,
  type RunSummary,
} from "../../api/diagnose";
import { StatusDot } from "@skyhook-io/k8s-ui";
import { Markdown } from "../ui/Markdown";

// Segmented two-or-more-way selector — shared shape for the agent picker and the
// isolation toggle.
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

// AgentControls is the full AI-diagnosis config block (agent, isolation, model,
// effort) — pure + prop-driven. It lives in Settings, not the investigation panel,
// since these are set-once preferences rather than per-run knobs.
export function AgentControls({
  agents,
  selectedAgent,
  onSelectAgent,
  isolated,
  onSetIsolated,
  model,
  onSetModel,
  effort,
  onSetEffort,
}: {
  agents: AgentInfo[];
  selectedAgent: string;
  onSelectAgent: (name: string) => void;
  isolated: boolean;
  onSetIsolated: (v: boolean) => void;
  model: string;
  onSetModel: (v: string) => void;
  effort: string;
  onSetEffort: (v: string) => void;
}) {
  const isCodex = selectedAgent === "codex";
  const isClaude = selectedAgent === "claude";
  const isCursor = selectedAgent === "cursor-agent";
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
      {isCodex && (
        <div>
          <Segmented<boolean>
            label="Environment"
            value={isolated}
            onChange={onSetIsolated}
            options={[
              { value: true, label: "Isolated (recommended)" },
              { value: false, label: "My setup" },
            ]}
          />
          {isolated ? (
            <p className="mt-1.5 text-[11px] leading-snug text-theme-text-tertiary">
              Runs Codex on its own — no access to your other MCP servers,
              guidelines, or project files.
            </p>
          ) : (
            <div className="mt-1.5 flex items-start gap-1.5 rounded border border-amber-500/40 bg-amber-500/10 p-2 text-[11px] leading-snug text-theme-text-secondary">
              <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0 text-amber-500" />
              <span>
                Runs Codex with your full setup — your own MCP servers (which may
                be write- or network-capable) and guidelines, and it can read
                local files. Only choose this if you rely on that config.
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
      ) : (
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
              : !isolated
                ? "“My setup” uses your own Codex config's model; set a slug here to override it."
                : "Leave empty for Codex's default, or enter a model your Codex version supports."
          }
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
};

// TimelineItem is one ordered transcript entry: agent reasoning, or a tool call.
type TimelineItem =
  | { kind: "thinking"; text: string }
  | {
      kind: "tool";
      id: string;
      tool: string;
      status: string;
      ms?: number;
      summary?: string;
      result?: string;
      truncated?: boolean;
    };

export function appendThinking(
  prev: TimelineItem[],
  text: string,
): TimelineItem[] {
  const last = prev[prev.length - 1];
  if (last && last.kind === "thinking") {
    const next = [...prev];
    next[next.length - 1] = { ...last, text: (last.text + text).slice(-4000) };
    return next;
  }
  return [...prev, { kind: "thinking", text }];
}

export function upsertTool(
  prev: TimelineItem[],
  step: DiagnoseStep,
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
    };
    return next;
  }
  return [...prev, { kind: "tool", ...step }];
}

export function TurnView({
  turn,
  synthLabel,
  reveal = "full",
  onApply,
  onAsk,
  onCheckStatus,
  onRetryDiagnosis,
  hideVerdict = false,
}: {
  turn: Turn;
  synthLabel?: string | null;
  reveal?: "rca" | "full";
  onApply?: (fix: string) => void;
  onAsk?: (question: string) => void;
  onCheckStatus?: () => void;
  onRetryDiagnosis?: () => void;
  // In the maximized workspace the pinned turn's verdict renders in the side rail,
  // so the transcript suppresses its own copy (reasoning + tool calls still show).
  hideVerdict?: boolean;
}) {
  // A follow-up (a turn the user asked a question on) is a conversational reply,
  // not a fresh diagnosis — render it as a plain answer, never the root-cause
  // anchor or a remediation card.
  const followup = !!turn.question && !turn.apply;
  // Whether the done turn has anything for ResultCard to render — mirrors its
  // branch order exactly (apply → followup → structured/healthy), since a followup
  // ONLY ever renders FollowupAnswer (report/rootCause), never the remediation list.
  // When false, TurnView shows the narration or an explicit empty note, not a blank.
  const dx = turn.diagnosis;
  const hasVerdict = dx
    ? turn.apply
      ? true // ApplyOutcomeCard always renders an outcome
      : dx.healthy && !dx.rootCause
        ? true // AllClearCard (checked before followup in ResultCard)
        : dx.inconclusive && !dx.rootCause
          ? true // InconclusiveCard
          : followup
            ? !!(dx.report?.trim() || dx.rootCause?.trim()) // FollowupAnswer
            : !!dx.rootCause ||
              (dx.remediation?.length ?? 0) > 0 ||
              !!dx.report?.trim()
    : false;
  return (
    <div className="space-y-2">
      {turn.question && (
        <div className="flex justify-end">
          <div className="max-w-[85%] rounded-lg rounded-br-sm bg-accent/10 px-3 py-1.5 text-sm text-theme-text-primary [overflow-wrap:anywhere]">
            {turn.question}
          </div>
        </div>
      )}
      <Timeline
        items={turn.timeline}
        running={turn.status === "running"}
        applyMode={turn.apply}
        followup={followup}
        synthLabel={synthLabel}
      />
      {turn.status === "done" &&
        (hideVerdict && hasVerdict ? null : hasVerdict ? (
          <ResultCard
            diagnosis={turn.diagnosis!}
            onApply={onApply}
            onAsk={onAsk}
            apply={turn.apply}
            followup={followup}
            reveal={reveal}
            onCheckStatus={onCheckStatus}
          />
        ) : (
          <EmptyResult />
        ))}
      {turn.status === "error" && turn.error && (
        <div className="flex items-start gap-2 rounded-lg border border-red-500/30 bg-red-500/10 p-3 text-sm text-theme-text-primary">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-red-400" />
          <div className="flex min-w-0 flex-col gap-2">
            <span className="whitespace-pre-wrap break-words">{turn.error}</span>
            {onRetryDiagnosis && (
              <button
                type="button"
                onClick={onRetryDiagnosis}
                className="btn-brand self-start px-3 py-1 text-xs"
              >
                Retry diagnosis
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

// RunContextCard opens every investigation with what RADAR already knows — the
// health frame the server captured at run start. It renders instantly (no agent
// round-trip), so the agent's boot time reads as "context, then deepening"
// instead of dead air — and it anchors the verdict against Radar's own signal.
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

// The first-run consent + trust card. Its copy is the OSS BYO-local trust story
// ("your own agent, on your machine, nothing to Radar").
// TODO(cloud): this copy must become embedder-overridable for Radar Cloud, where
//   the agent runs in the cloud (the company's self-hosted instance + their key /
//   local LLM, OR our SaaS) — a different, honestly-different trust story ("runs in
//   your Radar Cloud, audited, managed key"). Plumb an override through the
//   DiagnoseCustomization seam (same place Hub overrides the entry button) before
//   the k8s-ui lift, so OSS and Cloud don't ship the same claim over different data
//   flows. This is also a natural "upgrade to Cloud" surface.
export function ConsentCard({
  agentName,
  agent,
  isolated = true,
  onOpenSettings,
  onApprove,
  onCancel,
}: {
  agentName: string;
  agent?: string;
  isolated?: boolean;
  onOpenSettings?: () => void;
  onApprove: () => void;
  onCancel: () => void;
}) {
  // Cursor can't be isolated (no flag suppresses its global MCP servers), so it
  // gets its own honest framing rather than the isolated/my-setup pair.
  const isCursor = agent === "cursor-agent";
  return (
    <div className="rounded-lg border border-theme-border bg-theme-elevated p-4">
      <div className="mb-2 flex items-center gap-2">
        <ShieldCheck className="h-4 w-4 text-accent" />
        <div className="text-sm font-medium text-theme-text-primary">
          {isolated && !isCursor
            ? "Run a read-only AI investigation?"
            : "Run an AI investigation?"}
        </div>
      </div>
      <p className="text-sm leading-relaxed text-theme-text-secondary">
        This runs{" "}
        <span className="font-medium text-theme-text-primary">
          your own {agentName}
        </span>{" "}
        on your machine — no Radar cloud, no API key, no account. Radar sends
        this resource&apos;s spec, recent events, and pod logs to it (and on to
        its model provider under your account, not to Radar). Transcripts are
        kept in your local Radar history on this machine until cleared.
        {isolated && !isCursor && (
          <>
            {" "}
            Through Radar the agent can only{" "}
            <span className="font-medium">read</span> — it cannot change your
            cluster.
          </>
        )}
      </p>
      <ul className="mt-2 space-y-1 text-xs text-theme-text-tertiary">
        {isCursor ? (
          <li>
            • Through Radar the agent only <span className="font-medium">reads</span>{" "}
            your cluster. But Cursor also loads your own global MCP servers and
            Radar can&apos;t exclude them (unlike Claude or Codex), so if any of
            those can make changes, Cursor could use them.
          </li>
        ) : isolated ? (
          <li>
            • Isolated: only Radar&apos;s read-only investigation tools — your
            other CLI config and MCP servers are excluded.
            {agent === "codex" && (
              <>
                {" "}
                Codex&apos;s sandboxed shell can still <em>read</em> files on
                your machine (it cannot write or reach the network).
              </>
            )}
          </li>
        ) : (
          <li>
            • &ldquo;My setup&rdquo;: the agent also runs with your own CLI
            config + MCP servers and can read local files. Only Radar&apos;s own
            tools are read-only.
          </li>
        )}
      </ul>
      {onOpenSettings && (
        <button
          onClick={onOpenSettings}
          className="mt-3 text-xs text-accent hover:underline"
        >
          Change the agent and how it runs in Settings
        </button>
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
          Approve &amp; investigate
        </button>
      </div>
    </div>
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
              in this diagnosis — consider asking a follow-up to verify before
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
  synthLabel,
}: {
  items: TimelineItem[];
  running: boolean;
  applyMode?: boolean;
  followup?: boolean;
  synthLabel?: string | null;
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
    | Extract<TimelineItem, { kind: "tool" }>
    | undefined;
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
      {items.map((it, i) =>
        it.kind === "thinking" ? (
          // The model's reasoning between tool calls — muted + subordinate to the
          // tool rows. Rendered as markdown so Codex's summary headers read cleanly.
          <AIMarkdown
            key={i}
            className="animate-transcript-enter py-0.5 text-xs leading-relaxed text-theme-text-tertiary [overflow-wrap:anywhere] [&_li]:text-theme-text-tertiary [&_p]:my-0.5 [&_strong]:font-medium [&_strong]:text-theme-text-secondary"
          >
            {it.text}
          </AIMarkdown>
        ) : (
          <ToolRow key={it.id} step={it} />
        ),
      )}
      {running &&
        (synthLabel ? (
          <SynthBeat label={synthLabel} />
        ) : (
          <RunningStatus label={runningLabel} />
        ))}
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

// A staged "thinking" beat — a calm breathing dot + shimmering label. Used both in
// the timeline (pre-verdict) and between the root-cause and remediation cards.
function SynthBeat({ label }: { label: string }) {
  return (
    <div className="flex items-center gap-2 pt-1 text-xs animate-transcript-enter">
      <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-accent animate-synth-pulse" />
      <span className="ai-shimmer font-medium">{label}…</span>
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

function ToolRow({ step }: { step: Extract<TimelineItem, { kind: "tool" }> }) {
  const [open, setOpen] = useState(false);
  const [showFull, setShowFull] = useState(false);
  const hasDetail = !!(step.summary || step.result);
  // Offer the rich dialog when the result is structured or non-trivial in size.
  const richResult =
    !!step.result && (isJsonPayload(step.result) || step.result.length > 200);
  return (
    <div className="animate-transcript-enter rounded-md border border-theme-border/60 bg-theme-base/40">
      <button
        onClick={() => hasDetail && setOpen((v) => !v)}
        className={`flex w-full items-center gap-2 px-2 py-1.5 text-left text-sm ${
          hasDetail ? "hover:bg-theme-hover" : "cursor-default"
        }`}
      >
        {step.status === "done" ? (
          <CheckCircle2 className="h-3.5 w-3.5 shrink-0 text-emerald-400" />
        ) : (
          <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-accent" />
        )}
        <span className="font-mono text-xs text-theme-text-secondary">
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
        {hasDetail && (
          <ChevronRight
            className={`h-3.5 w-3.5 shrink-0 text-theme-text-tertiary transition-transform ${open ? "rotate-90" : ""}`}
          />
        )}
      </button>
      {hasDetail && (
        <Collapse open={open}>
          <div className="space-y-2 border-t border-theme-border/60 px-2 py-2">
            {step.summary && <PayloadBlock label="Input" text={step.summary} />}
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
          <CopyButton text={json ?? text} />
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
          <CopyButton text={display} />
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

// Collapse — the Radar-standard expand/collapse motion (grid-template-rows
// 0fr↔1fr) used across issue rows. Children stay mounted so close animates too.
function Collapse({ open, children }: { open: boolean; children: ReactNode }) {
  return (
    <div
      className={`issue-details-motion ${open ? "issue-details-motion-open" : ""}`}
    >
      <div className="overflow-hidden">{children}</div>
    </div>
  );
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
  followup,
  reveal = "full",
  onCheckStatus,
}: {
  diagnosis: Diagnosis;
  onApply?: (fix: string) => void;
  onAsk?: (question: string) => void;
  apply?: boolean;
  followup?: boolean;
  reveal?: "rca" | "full";
  onCheckStatus?: () => void;
}) {
  // Apply turns report what changed — an outcome, not a diagnosis. Frame as a
  // success confirmation (emerald) rather than the amber root-cause anchor.
  if (apply)
    return (
      <ApplyOutcomeCard diagnosis={diagnosis} onCheckStatus={onCheckStatus} />
    );
  if (diagnosis.healthy && !diagnosis.rootCause)
    return <AllClearCard diagnosis={diagnosis} />;
  // Couldn't-determine is its own honest state — never a confident all-clear, never
  // the alarming root-cause anchor.
  if (diagnosis.inconclusive && !diagnosis.rootCause)
    return <InconclusiveCard diagnosis={diagnosis} />;
  // Follow-ups are conversational replies, not fresh diagnoses — plain answer.
  if (followup) return <FollowupAnswer diagnosis={diagnosis} />;

  // A turn with no structured root cause and no remediation (e.g. "looks healthy",
  // or a clarifying question) is not a diagnosis — render it neutrally rather than
  // forcing the alarming root-cause anchor onto a non-problem.
  const structured =
    !!diagnosis.rootCause || (diagnosis.remediation?.length ?? 0) > 0;
  if (!structured) return <FollowupAnswer diagnosis={diagnosis} />;

  return (
    <DiagnosisResult
      diagnosis={diagnosis}
      onApply={onApply}
      onAsk={onAsk}
      reveal={reveal}
    />
  );
}

const EXPLAIN_SIMPLY_PROMPT =
  "Explain this in plain language for someone who isn't a Kubernetes expert — what's broken, why it matters, and what each remediation step actually does. Gloss any k8s terms.";

// The diagnosis result: root cause + remediation (any step applyable) + the
// agent's full analysis on demand.
function DiagnosisResult({
  diagnosis,
  onApply,
  onAsk,
  reveal = "full",
}: {
  diagnosis: Diagnosis;
  onApply?: (fix: string) => void;
  onAsk?: (question: string) => void;
  reveal?: "rca" | "full";
}) {
  const [showAnalysis, setShowAnalysis] = useState(false);
  // Only a real structured root cause anchors the amber card; the full prose lives
  // in "Full analysis" (never relabel the report as a root cause).
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
  return (
    <div className="mt-3 space-y-2 animate-result-in">
      {/* Root cause — the anchor: distinct tone + heavier type so it pops. */}
      {rootCause && (
        <div
          className="rounded-lg border border-amber-500/30 bg-amber-500/5 p-3 animate-verdict-reveal"
          style={{ "--glow": "rgb(245 158 11)" } as React.CSSProperties}
        >
          <div className="mb-1 flex items-center justify-between gap-2">
            <div className="relative flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-amber-500">
              <AlertTriangle className="h-3.5 w-3.5" />
              Root cause
              <span className="absolute -bottom-0.5 left-0 right-0 h-px bg-amber-500/60 animate-underline-sweep" />
            </div>
            <div className="flex items-center gap-2">
              {diagnosis.confidence != null ? (
                <ConfidenceMeter value={diagnosis.confidence} />
              ) : (
                <ConfidenceUnstated />
              )}
              <CopyButton text={rootCause} />
            </div>
          </div>
          <AIMarkdown className="text-sm font-medium text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_p]:my-0 [&_p]:text-theme-text-primary">
            {rootCause}
          </AIMarkdown>
          {onAsk && reveal === "full" && (
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

      {/* Between the root cause and the remediation, a beat lands where the steps
          will appear — the verdict unfolds rather than dumping all at once. */}
      {reveal === "rca" && hasRemediation && (
        <SynthBeat label="Weighing remediation options" />
      )}

      {/* Remediation — copyable steps; the recommended one is highlighted as the
          default, and any step can be applied (Apply binds to that step's text). */}
      {reveal === "full" && hasRemediation && (
        <div
          className="rounded-lg border border-theme-border bg-theme-elevated p-3 animate-verdict-reveal"
          style={{ "--glow": "var(--accent)" } as React.CSSProperties}
        >
          <div className="mb-2 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
            <Wrench className="h-3.5 w-3.5 text-accent" />
            Remediation
          </div>
          <ol className="space-y-2">
            {remediation.map((r, i) => {
              const isRec = recValid && i === recIdx! - 1;
              return (
                <li
                  key={i}
                  className={`animate-transcript-enter ${
                    isRec
                      ? "rounded-lg border border-accent/40 bg-accent/5 p-2.5"
                      : ""
                  }`}
                  style={{ animationDelay: `${260 + i * 70}ms` }}
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
                      {canApply && (
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
                      <CopyButton text={r} />
                    </div>
                  </div>
                </li>
              );
            })}
          </ol>
          {!recValid && (
            <p className="mt-2 flex items-start gap-1.5 text-[11px] leading-snug text-theme-text-tertiary">
              <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0" />
              No safe one-click fix — the agent flagged this as needing your
              judgement. Review the steps, or resume in your agent to apply them
              interactively.
            </p>
          )}
        </div>
      )}

      {/* Full analysis — the agent's detailed evidence, on demand. */}
      {reveal === "full" && diagnosis.report && (
        <div className="rounded-lg border border-theme-border bg-theme-elevated">
          <button
            onClick={() => setShowAnalysis((v) => !v)}
            className="flex w-full items-center gap-1.5 px-3 py-2 text-xs font-medium uppercase tracking-wide text-theme-text-tertiary hover:text-theme-text-primary"
          >
            <ChevronRight
              className={`h-3.5 w-3.5 transition-transform ${showAnalysis ? "rotate-90" : ""}`}
            />
            Full analysis
          </button>
          <Collapse open={showAnalysis}>
            <div className="border-t border-theme-border/60 px-3 py-2">
              <AIMarkdown className="text-sm [overflow-wrap:anywhere] [&_h2:first-child]:mt-0 [&_h2]:mb-1.5 [&_h2]:mt-3 [&_h2]:text-xs [&_h2]:font-semibold [&_h2]:uppercase [&_h2]:tracking-wide [&_h2]:text-theme-text-tertiary [&_h3]:text-sm [&_li]:text-theme-text-secondary [&_p]:my-1.5 [&_p]:text-theme-text-secondary">
                {diagnosis.report}
              </AIMarkdown>
            </div>
          </Collapse>
        </div>
      )}

      {reveal === "full" && (
        <div className="flex items-start gap-1 px-0.5 text-[11px] text-theme-text-tertiary">
          <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0" />
          <span>AI-generated — review before applying</span>
        </div>
      )}
    </div>
  );
}

function AllClearCard({ diagnosis }: { diagnosis: Diagnosis }) {
  const text =
    diagnosis.report || "No active problem found for this resource.";
  return (
    <div className="mt-3 space-y-2 animate-result-in">
      <div
        className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-3 animate-verdict-reveal"
        style={{ "--glow": "rgb(16 185 129)" } as React.CSSProperties}
      >
        <div className="mb-1 flex items-center justify-between gap-2">
          <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-emerald-500">
            <CheckCircle2 className="h-3.5 w-3.5" />
            No problems found
          </div>
          <CopyButton text={text} />
        </div>
        <AIMarkdown className="text-sm text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_li]:text-theme-text-primary [&_p]:my-1 [&_p]:text-theme-text-primary [&_p:first-child]:mt-0 [&_p:last-child]:mb-0">
          {text}
        </AIMarkdown>
      </div>
      <div className="flex items-start gap-1 px-0.5 text-[11px] text-theme-text-tertiary">
        <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0" />
        <span>AI-generated — verify if symptoms persist</span>
      </div>
    </div>
  );
}

// The agent investigated but couldn't determine an answer. A distinct, honest
// state — neutral (not the alarming amber root cause, not the reassuring emerald
// all-clear) — so "I couldn't tell" never reads as "you're fine."
function InconclusiveCard({ diagnosis }: { diagnosis: Diagnosis }) {
  const text =
    diagnosis.report ||
    "The investigation couldn't reach a clear conclusion — some checks were blocked or the evidence was ambiguous.";
  return (
    <div className="mt-3 space-y-2 animate-result-in">
      <div
        className="rounded-lg border border-theme-border bg-theme-elevated p-3 animate-verdict-reveal"
        style={{ "--glow": "rgb(100 116 139)" } as React.CSSProperties}
      >
        <div className="mb-1 flex items-center justify-between gap-2">
          <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-theme-text-secondary">
            <HelpCircle className="h-3.5 w-3.5" />
            Couldn&apos;t determine
          </div>
          <CopyButton text={text} />
        </div>
        <AIMarkdown className="text-sm text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_li]:text-theme-text-primary [&_p]:my-1 [&_p]:text-theme-text-primary [&_p:first-child]:mt-0 [&_p:last-child]:mb-0">
          {text}
        </AIMarkdown>
      </div>
      <div className="flex items-start gap-1 px-0.5 text-[11px] text-theme-text-tertiary">
        <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0" />
        <span>
          Try a follow-up with more detail, or re-run after granting access.
        </span>
      </div>
    </div>
  );
}

// A follow-up reply: the agent answering a question, not re-diagnosing. Plain
// neutral block — no root-cause anchor, no remediation/apply.
function FollowupAnswer({ diagnosis }: { diagnosis: Diagnosis }) {
  const text = diagnosis.report || diagnosis.rootCause;
  if (!text) return null;
  return (
    <div className="mt-1 rounded-lg border border-theme-border bg-theme-elevated p-3">
      <div className="mb-1.5 flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
          <Sparkles className="h-3.5 w-3.5 text-accent" />
          Answer
        </div>
        <CopyButton text={text} />
      </div>
      <AIMarkdown className="text-sm [overflow-wrap:anywhere] [&_code]:font-normal [&_h2:first-child]:mt-0 [&_h2]:mb-1.5 [&_h2]:mt-3 [&_h2]:text-xs [&_h2]:font-semibold [&_h2]:uppercase [&_h2]:tracking-wide [&_h2]:text-theme-text-tertiary [&_h3]:text-sm [&_li]:text-theme-text-secondary [&_p]:my-1.5 [&_p]:text-theme-text-secondary [&_p:first-child]:mt-0">
        {text}
      </AIMarkdown>
    </div>
  );
}

// A done turn that produced no renderable verdict at all (empty diagnosis, no
// narration). Without this the turn would render blank — which reads as "the tool
// broke." Make the dead-end explicit and point at the recovery (a follow-up).
function EmptyResult() {
  return (
    <div className="mt-1 flex items-start gap-2 rounded-lg border border-theme-border bg-theme-elevated p-3 text-sm text-theme-text-secondary">
      <ShieldCheck className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
      <span>
        The investigation finished without a clear result. Try a follow-up
        question, or re-run Diagnose.
      </span>
    </div>
  );
}

// The result of an apply turn: a success confirmation of what changed, not a
// diagnosis. Emerald + checkmark so it reads as an outcome.
function ApplyOutcomeCard({
  diagnosis,
  onCheckStatus,
}: {
  diagnosis: Diagnosis;
  onCheckStatus?: () => void;
}) {
  const outcome = diagnosis.report || diagnosis.rootCause;
  return (
    <div className="mt-3 space-y-2">
      <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-3">
        <div className="mb-1 flex items-center justify-between gap-2">
          <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-emerald-500">
            <CheckCircle2 className="h-3.5 w-3.5" />
            Applied
          </div>
          {outcome && <CopyButton text={outcome} />}
        </div>
        {outcome && (
          <AIMarkdown className="text-sm text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_li]:text-theme-text-primary [&_p]:my-1 [&_p]:text-theme-text-primary [&_p:first-child]:mt-0 [&_p:last-child]:mb-0">
            {outcome}
          </AIMarkdown>
        )}
        {onCheckStatus && (
          <button
            onClick={onCheckStatus}
            className="mt-3 flex w-full items-center justify-center gap-1.5 rounded-lg border border-emerald-500/40 py-2 text-sm font-medium text-emerald-500 hover:bg-emerald-500/10"
          >
            <RefreshCw className="h-4 w-4" />
            Check status
          </button>
        )}
      </div>
      <div className="flex items-center gap-1 px-0.5 text-[11px] text-theme-text-tertiary">
        <ShieldCheck className="h-3 w-3 shrink-0" />
        <span className="truncate">
          Applied by AI — verify the change took effect
        </span>
      </div>
    </div>
  );
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      onClick={() => {
        navigator.clipboard?.writeText(text);
        setCopied(true);
        setTimeout(() => setCopied(false), 1200);
      }}
      className="shrink-0 rounded p-1 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
      aria-label="Copy"
    >
      {copied ? (
        <Check className="h-3.5 w-3.5 text-emerald-400" />
      ) : (
        <Copy className="h-3.5 w-3.5" />
      )}
    </button>
  );
}

function prettyTool(tool: string): string {
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
        {band} confidence
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
      Confidence not stated
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
