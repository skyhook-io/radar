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
    expect(launchTrustHint(base)).toMatch(/trust this folder/);
  });

  it("stays quiet for agents without that prompt", () => {
    expect(launchTrustHint({ ...base, agent: "codex" })).toBeNull();
  });
});
