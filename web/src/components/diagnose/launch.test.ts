import { describe, expect, it } from "vitest";
import type { RunSummary } from "../../api/diagnose";
import {
  buildLaunchCommand,
  launchAgentLabel,
  launchTrustHint,
} from "./launch";

describe("OpenCode terminal handoff", () => {
  it("uses the correct label without launching a different agent", () => {
    const run: RunSummary = {
      id: "run-test",
      kind: "Deployment",
      group: "apps",
      namespace: "default",
      name: "demo",
      context: "test",
      agent: "opencode",
      status: "done",
      sessionId: "ses_opencode",
      createdAt: "2026-09-21T00:00:00Z",
      updatedAt: "2026-09-21T00:00:00Z",
    };
    expect(launchAgentLabel(run)).toBe("OpenCode");
    expect(buildLaunchCommand(run, "http://localhost:9280/mcp")).toBeNull();
  });
});

describe("launchTrustHint", () => {
  const base: RunSummary = {
    id: "run-test",
    kind: "Deployment",
    group: "apps",
    namespace: "default",
    name: "demo",
    context: "test",
    agent: "claude",
    status: "done",
    sessionId: "ses_1",
    createdAt: "2026-09-21T00:00:00Z",
    updatedAt: "2026-09-21T00:00:00Z",
  };

  it("warns about Claude Code's folder trust prompt", () => {
    expect(launchTrustHint(base)).toMatch(/trust Radar's agent folder/);
  });

  it("stays quiet for agents without that prompt", () => {
    expect(launchTrustHint({ ...base, agent: "codex" })).toBeNull();
  });
});

describe("Claude Code resume command", () => {
  const run: RunSummary = {
    id: "run-test",
    kind: "Deployment",
    group: "apps",
    namespace: "default",
    name: "demo",
    context: "test",
    agent: "claude",
    status: "done",
    sessionId: "ses_1",
    createdAt: "2026-09-21T00:00:00Z",
    updatedAt: "2026-09-21T00:00:00Z",
  };

  it("resumes from the folder Radar runs Claude in", () => {
    expect(buildLaunchCommand(run, "http://localhost:9280/mcp", "posix")).toBe(
      'mkdir -p -m 700 ~/.radar/agent && cd ~/.radar/agent && claude --resume \'ses_1\' --mcp-config \'{"mcpServers":{"radar":{"type":"http","url":"http://localhost:9280/mcp"}}}\'',
    );
  });

  // Windows PowerShell 5.1 has no && and strips the quotes inside a native
  // argument; --% passes the JSON through with C-runtime escaping instead.
  it("uses a form Windows PowerShell 5.1 runs", () => {
    expect(
      buildLaunchCommand(run, "http://localhost:9280/mcp", "powershell"),
    ).toBe(
      'New-Item -ItemType Directory -Force -Path "$HOME\\.radar\\agent" -ErrorAction Stop | Out-Null; ' +
        'Set-Location -LiteralPath "$HOME\\.radar\\agent" -ErrorAction Stop; ' +
        "& (Get-Command claude -CommandType Application -ErrorAction Stop)[0].Source --% " +
        '--resume ses_1 --mcp-config "{\\"mcpServers\\":{\\"radar\\":{\\"type\\":\\"http\\",\\"url\\":\\"http://localhost:9280/mcp\\"}}}"',
    );
  });

  it("gives Codex the same pass-through under PowerShell", () => {
    expect(
      buildLaunchCommand(
        { ...run, agent: "codex", sessionId: "019a-b2" },
        "http://localhost:9280/mcp",
        "powershell",
      ),
    ).toBe(
      "& (Get-Command codex -CommandType Application -ErrorAction Stop)[0].Source --% " +
        'resume 019a-b2 -c "mcp_servers.radar.url=\\"http://localhost:9280/mcp\\""',
    );
  });

  it("offers no PowerShell command it can't pass through intact", () => {
    expect(
      buildLaunchCommand(
        { ...run, sessionId: 'a"b' },
        "http://localhost:9280/mcp",
        "powershell",
      ),
    ).toBeNull();
    expect(
      buildLaunchCommand(run, "http://localhost:9280/%41/mcp", "powershell"),
    ).toBeNull();
  });
});

describe("an agent the hand-off doesn't know", () => {
  const base: RunSummary = {
    id: "run-x",
    kind: "Deployment",
    group: "apps",
    namespace: "default",
    name: "demo",
    context: "test",
    status: "done",
    sessionId: "ses_1",
    createdAt: "2026-09-21T00:00:00Z",
    updatedAt: "2026-09-21T00:00:00Z",
  };

  it("is named by its own label, with no Claude warning and no command", () => {
    const run = { ...base, agent: "gemini" };
    expect(launchAgentLabel(run)).toBe("Gemini CLI");
    expect(launchTrustHint(run)).toBeNull();
    expect(
      buildLaunchCommand(run, "http://localhost:9280/mcp", "posix"),
    ).toBeNull();
  });

  it("falls back to the agent's own name when Radar has no label for it", () => {
    expect(launchAgentLabel({ ...base, agent: "newagent" })).toBe("newagent");
  });

  it("still resumes a run from before Radar recorded the agent as Claude Code", () => {
    expect(launchAgentLabel(base)).toBe("Claude Code");
    expect(
      buildLaunchCommand(base, "http://localhost:9280/mcp", "posix"),
    ).toMatch(/claude --resume 'ses_1'/);
  });
});
