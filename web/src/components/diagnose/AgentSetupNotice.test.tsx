import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { AgentSetupNotice } from "./AgentSetupNotice";
import type { DiagnoseSetup } from "./DiagnoseContext";

const render = (
  setupState: DiagnoseSetup,
  { checkingAgents = false, cliOverride = false } = {},
) =>
  renderToStaticMarkup(
    <AgentSetupNotice
      setupState={setupState}
      cliOverride={cliOverride}
      checkingAgents={checkingAgents}
      agentsCheckFailed={false}
      recheckAgents={async () => {}}
    />,
  );

describe("AgentSetupNotice", () => {
  it("offers installs and a re-check, never a restart, when no CLI is found", () => {
    const html = render("needs-install");
    expect(html).toContain("Claude Code");
    expect(html).toContain("OpenCode");
    expect(html).toContain("Check again");
    expect(html).not.toMatch(/restart/i);
    // A desktop user can't set an environment variable for a Dock launch.
    expect(html).not.toContain("RADAR_AI_CLI_BIN");
  });

  it("shows the check in progress", () => {
    const html = render("needs-install", { checkingAgents: true });
    expect(html).toContain("Checking…");
    expect(html).toContain("disabled");
  });

  it("names the override only when the server says it is set", () => {
    for (const state of ["needs-restart", "needs-install"] as const) {
      const html = render(state, { cliOverride: true });
      expect(html).toContain("RADAR_AI_CLI_BIN");
      expect(html).toContain("restart Radar");
      // Detection is off while the variable is set, so an install can't help.
      expect(html).not.toContain("Install one of these");
      expect(html).not.toContain("Check again");
    }
  });

  it("says nothing it can't know when an agent is reported but off", () => {
    // Radar Hub reports a hosted agent that is supported but not enabled when
    // its own runner is down: no CLI, no startup output, no override.
    const html = render("needs-restart");
    expect(html).toContain("aren&#x27;t available right now");
    expect(html).not.toMatch(/RADAR_AI_CLI_BIN|agent CLI|startup output/);
    expect(html).not.toContain("Install one of these");
    expect(html).toContain("Check again");
  });
});
