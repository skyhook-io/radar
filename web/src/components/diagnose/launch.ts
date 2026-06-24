// Builds the shell command that hands an investigation off to the user's own
// interactive agent CLI, pointed at Radar's MCP. This is the "escape hatch" to the
// full agent (their config, their other MCPs, interactive approvals) — distinct
// from the contained in-panel engine. The launched agent gets the full /mcp mount
// (read+write); its own per-tool approval prompts gate any changes.
import { type RunSummary } from "../../api/diagnose";

// sq POSIX-single-quotes a string so it's a safe single shell argument regardless
// of content. Findings carry backticks, quotes, and newlines — inside single
// quotes the shell treats everything literally; embedded single quotes become
// the standard '\'' sequence. Never build the command without this.
function sq(s: string): string {
  return "'" + s.replace(/'/g, "'\\''") + "'";
}

export function launchAgentLabel(run: RunSummary): string {
  if (run.agent === "codex") return "Codex";
  if (run.agent === "cursor-agent") return "Cursor";
  return "Claude Code";
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
): string | null {
  if (!run.sessionId || run.status === "stale") return null;

  if (run.agent === "cursor-agent") {
    // Cursor's --resume is workspace-scoped, and Radar runs each investigation in a
    // throwaway per-run workspace the user doesn't have — no command can reattach
    // both that session and Radar's MCP in the user's own terminal. So there's no
    // hand-off for Cursor; the in-panel investigation is self-contained.
    return null;
  }
  if (run.agent === "codex") {
    // Codex threads are stored globally by id (cwd-independent); -c re-attaches MCP.
    return `codex resume ${sq(run.sessionId)} -c ${sq(`mcp_servers.radar.url="${mcpUrl}"`)}`;
  }
  // Claude Code: --resume is cwd-scoped, and Radar runs its headless sessions from
  // the home dir (see claudeAgent). The Radar terminal starts there too, but a user
  // pasting this into their own terminal is usually in a project dir — so prefix
  // `cd ~` to resolve the session wherever it's run (harmless in the home-dir case).
  // --mcp-config MERGES radar alongside the user's own servers.
  const cfg = JSON.stringify({
    mcpServers: { radar: { type: "http", url: mcpUrl } },
  });
  return `cd ~ && claude --resume ${sq(run.sessionId)} --mcp-config ${sq(cfg)}`;
}
