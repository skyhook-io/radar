import { describe, expect, it } from "vitest";
import type { RunSummary } from "../../api/diagnose";
import { buildLaunchCommand, launchAgentLabel } from "./launch";

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
