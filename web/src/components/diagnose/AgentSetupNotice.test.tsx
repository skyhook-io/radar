import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { AgentSetupNotice } from "./AgentSetupNotice";
import type { DiagnoseSetup } from "./DiagnoseContext";

const render = (setupState: DiagnoseSetup, checkingAgents = false) =>
  renderToStaticMarkup(
    <AgentSetupNotice
      setupState={setupState}
      checkingAgents={checkingAgents}
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
    const html = render("needs-install", true);
    expect(html).toContain("Checking…");
    expect(html).toContain("disabled");
  });

  it("names the misconfigured override instead of asking for an install", () => {
    const html = render("needs-restart");
    expect(html).toContain("RADAR_AI_CLI_BIN");
    expect(html).toContain("restart Radar");
    expect(html).not.toContain("Install one of these");
    expect(html).not.toContain("Check again");
  });
});
