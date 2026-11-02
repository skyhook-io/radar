// Builds the shell command that hands an investigation off to the user's own
// interactive agent CLI, pointed at Radar's MCP. This is the "escape hatch" to the
// full agent (their config, their other MCPs, interactive approvals) — distinct
// from the contained in-panel engine. The endpoint follows the server MCP mode.
// In full mode, the client approval prompts gate changes.
import { type RunSummary } from "../../api/diagnose";
import { agentLabelFor } from "./DiagnoseContext";

// sq POSIX-single-quotes a string so it's a safe single shell argument regardless
// of content. Findings carry backticks, quotes, and newlines — inside single
// quotes the shell treats everything literally; embedded single quotes become
// the standard '\'' sequence. Never build the command without this.
function sq(s: string): string {
  return "'" + s.replace(/'/g, "'\\''") + "'";
}

// Radar also creates it before each Claude turn, but a run from before Radar
// used it can be continued first. Unquoted so the shell expands ~.
const AGENT_DIR = "~/.radar/agent";

// A run with no recorded agent predates recording it, when Claude Code was
// the only one. Any other agent is named by its own label, with no Claude
// specifics, and gets no resume command it hasn't been taught.
function isClaudeRun(run: RunSummary): boolean {
  return !run.agent || run.agent === "claude";
}

export function launchAgentLabel(run: RunSummary): string {
  return isClaudeRun(run) ? "Claude Code" : agentLabelFor(run.agent ?? "");
}

// Claude Code asks once whether to trust the folder the resume runs in, and
// its default answer exits, so a user who presses Enter loses the session.
export function launchTrustHint(run: RunSummary): string | null {
  if (!isClaudeRun(run)) return null;
  return "The first time, Claude Code may ask to trust Radar's agent folder. Select Yes before pressing Enter; the default exits.";
}

// openInTerminal asks App (which owns the DockProvider) to open Radar's local
// terminal running `command`. The AI surface is portaled above the DockProvider,
// so it can't use the dock hook directly — it dispatches this event instead.
export function openInTerminal(command: string, title: string) {
  window.dispatchEvent(
    new CustomEvent("radar:open-local-terminal", {
      detail: { command, title },
    }),
  );
}

// buildLaunchCommand resumes the investigation's ACTUAL agent session interactively
// (not a re-seeded prompt), so the full transcript — every tool call, finding, and
// the agent's reasoning — carries over. Returns null when there's no resumable
// session yet (first turn still running) or the run is stale (different cluster).
// The MCP server is re-attached on resume (it's configured per-launch, not stored
// in the session).
export function buildLaunchCommand(
  run: RunSummary,
  mcpUrl: string,
  shell: "posix" | "powershell" = "posix",
): string | null {
  if (!run.sessionId || run.status === "stale") return null;

  // OpenCode terminal handoff needs a persistent MCP configuration outside the run's working directory.
  if (run.agent === "opencode") return null;

  if (run.agent === "cursor-agent") {
    // Cursor's --resume is workspace-scoped, and Radar runs each investigation in a
    // throwaway per-run working directory the user doesn't have — no command can reattach
    // both that session and Radar's MCP in the user's own terminal. So there's no
    // hand-off for Cursor; the in-panel investigation is self-contained.
    return null;
  }
  if (run.agent === "codex") {
    // Codex threads are stored globally by id (cwd-independent); -c re-attaches MCP.
    if (shell === "powershell") {
      if (!psSafe(run.sessionId, mcpUrl)) return null;
      return psNative(
        "codex",
        `resume ${run.sessionId} -c "mcp_servers.radar.url=\\"${mcpUrl}\\""`,
      );
    }
    return `codex resume ${sq(run.sessionId)} -c ${sq(`mcp_servers.radar.url="${mcpUrl}"`)}`;
  }
  if (!isClaudeRun(run)) return null;
  // Claude Code: resume from the folder Radar runs Claude turns in (claudeWorkDir
  // in agent_claude.go). Older versions find a session only from the folder it
  // was created in, and Claude Code remembers the folder-trust answer there,
  // which it never does for the home dir. --mcp-config MERGES radar alongside
  // the user's own servers.
  const cfg = JSON.stringify({
    mcpServers: { radar: { type: "http", url: mcpUrl } },
  });
  if (shell === "powershell") {
    if (!psSafe(run.sessionId, mcpUrl)) return null;
    const dir = '"$HOME\\.radar\\agent"';
    return `New-Item -ItemType Directory -Force -Path ${dir} -ErrorAction Stop | Out-Null; Set-Location -LiteralPath ${dir} -ErrorAction Stop; ${psNative("claude", `--resume ${run.sessionId} --mcp-config "${cfg.replace(/"/g, '\\"')}"`)}`;
  }
  return `mkdir -p -m 700 ${AGENT_DIR} && cd ${AGENT_DIR} && claude --resume ${sq(run.sessionId)} --mcp-config ${sq(cfg)}`;
}

// Radar's terminal on Windows is Windows PowerShell 5.1, which has no `&&`
// or `mkdir -p`, and drops the double quotes inside an argument it passes to a
// program. `--%` hands the rest of the line to the program unparsed, where the
// C runtime turns \" back into ". It applies to programs, not to npm's .ps1
// shims, so the program is found as an application (.exe or .cmd). A failing
// step stops the line (-ErrorAction Stop), so the agent never starts in the
// wrong folder.
function psNative(program: string, rawArgs: string): string {
  return `& (Get-Command ${program} -CommandType Application -ErrorAction Stop)[0].Source --% ${rawArgs}`;
}

// The --% text can't carry a quote, a % or a backslash, so a session id or URL
// holding one gets no command rather than a broken one.
function psSafe(sessionId: string, mcpUrl: string): boolean {
  return /^[\w-]+$/.test(sessionId) && !/["%\\]/.test(mcpUrl);
}
