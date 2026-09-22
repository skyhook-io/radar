import { useState } from "react";
import { AlertTriangle, Check, ShieldCheck, ChevronDown } from "lucide-react";
import {
  TRANSITION_MENU,
  overlayExitMs,
  overlayTransitionStyle,
} from "../../utils/animation";
import { useAnimatedUnmount } from "../../hooks/useAnimatedUnmount";
import type { DiagnoseConsentCopy } from "../../context/DiagnoseCustomization";
import type { AgentInfo, ExecutionProfile } from "../../api/diagnose";

const OPENCODE_FULL_LOCAL_WARNING =
  "Radar runs OpenCode with --auto, which automatically approves actions that your configuration would normally ask about, including built-in tools and configured MCP servers. Explicit denials still apply. Radar does not enforce a CLI sandbox.";

const CURSOR_FULL_LOCAL_WARNING =
  "Radar passes Cursor --force, which auto-approves its built-in tools and every MCP server it loads, including your global servers. Cursor’s sandbox does not reliably confine those tools to Radar’s temporary workspace.";

// Segmented two-or-more-way selector — shared shape for the agent and execution
// profile pickers.
export function Segmented<T extends string | boolean>({
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
  // Presence outlives `open` by the menu exit so the list can fade out; the
  // click-away backdrop only exists while logically open.
  const { shouldRender, isOpen } = useAnimatedUnmount(
    open,
    overlayExitMs("menu"),
  );
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
          <div className="fixed inset-0 z-10" onClick={() => setOpen(false)} />
        )}
        {shouldRender && (
          <ul
            role="listbox"
            inert={!open || undefined}
            className={`absolute left-0 right-0 z-20 mt-1 max-h-72 origin-top overflow-y-auto rounded-md border border-theme-border bg-theme-surface py-1 shadow-theme-lg ${TRANSITION_MENU} ${
              isOpen
                ? "opacity-100 translate-y-0 scale-100"
                : "opacity-0 -translate-y-1 scale-[0.97]"
            } ${open ? "" : "pointer-events-none"}`}
            style={overlayTransitionStyle(isOpen, "menu")}
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
  const isOpenCode = selectedAgent === "opencode";
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
                        : isOpenCode
                          ? OPENCODE_FULL_LOCAL_WARNING
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
                ) : isClaude ? (
                  <>
                    Claude uses the permissions from your setup; Radar does not
                    override them.
                  </>
                ) : isOpenCode ? (
                  OPENCODE_FULL_LOCAL_WARNING
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
          placeholder={
            selectedAgent === "opencode"
              ? "Default (provider/model)"
              : "Default"
          }
          onChange={onSetModel}
          hint={
            selectedAgent === "opencode"
              ? "Leave empty for OpenCode's default, or enter a provider/model from opencode models."
              : "Leave empty for the agent's default, or enter a model identifier it supports."
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
              ) : agent === "opencode" ? (
                OPENCODE_FULL_LOCAL_WARNING
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
