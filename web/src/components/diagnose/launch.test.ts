import { describe, expect, it } from "vitest";
import { buildLaunchCommand } from "./launch";
import type { RunSummary } from "../../api/diagnose";

const MCP = "http://127.0.0.1:9280/mcp";

function run(over: Partial<RunSummary> = {}): RunSummary {
  return {
    id: "r1",
    kind: "Deployment",
    namespace: "default",
    name: "web",
    context: "kind-radar",
    agent: "copilot",
    profile: "safeguarded",
    status: "done",
    sessionId: "sess-1",
    createdAt: "",
    updatedAt: "",
    ...over,
  };
}

describe("buildLaunchCommand for Copilot", () => {
  // The hand-off resumes the SAME session the headless turns filled with cluster
  // data. Copilot exports sessions to GitHub web and mobile by default, and the
  // consent card promises Radar never lets that happen — so the flags have to be
  // repeated on the interactive continuation, not just the headless turns.
  it("keeps the GitHub export disabled on resume", () => {
    const cmd = buildLaunchCommand(run(), MCP)!;
    expect(cmd).toContain("--no-remote-export");
    expect(cmd).toContain("--no-remote ");
  });

  // A safeguarded run's session lives in Radar's own Copilot home; without the
  // redirect the id simply isn't found. Full-local runs use the user's own home.
  it("points at Radar's Copilot home only for safeguarded runs", () => {
    expect(buildLaunchCommand(run(), MCP)).toContain(
      'COPILOT_HOME="$HOME/.radar/copilot-home" copilot --resume=',
    );
    const local = buildLaunchCommand(run({ profile: "full-local" }), MCP)!;
    expect(local).not.toContain("COPILOT_HOME");
    expect(local.startsWith("copilot --resume=")).toBe(true);
  });

  // --resume takes an OPTIONAL value: passed as a separate argument the id would
  // be read as a positional prompt and start a brand-new investigation.
  it("uses the =value form for --resume", () => {
    const cmd = buildLaunchCommand(run(), MCP)!;
    expect(cmd).toContain("--resume='sess-1'");
    expect(cmd).not.toContain("--resume 'sess-1'");
  });

  it("re-attaches Radar's MCP server", () => {
    expect(buildLaunchCommand(run(), MCP)).toContain(MCP);
  });

  it("offers no hand-off without a resumable session", () => {
    expect(buildLaunchCommand(run({ sessionId: undefined }), MCP)).toBeNull();
    expect(buildLaunchCommand(run({ status: "stale" }), MCP)).toBeNull();
  });
});
