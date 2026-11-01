import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { RunSummary } from "../../api/diagnose";
import { ContinueInAgentButton } from "./ContinueInAgentButton";
import { canContinueInOwnAgent } from "./investigationState";

let mockLocalTerminal = true;
vi.mock("../../contexts/CapabilitiesContext", () => ({
  useCapabilitiesContext: () => ({
    localTerminal: mockLocalTerminal,
    mcpReadOnly: false,
  }),
}));
beforeEach(() => {
  mockLocalTerminal = true;
});

function run(overrides: Partial<RunSummary> = {}): RunSummary {
  return {
    id: "r1",
    kind: "Deployment",
    group: "apps",
    namespace: "gl-crash-config",
    name: "api",
    context: "test",
    agent: "claude",
    status: "done",
    sessionId: "ses_1",
    createdAt: "2026-10-01T00:00:00Z",
    updatedAt: "2026-10-01T00:00:00Z",
    ...overrides,
  };
}

const settled = {
  run: run(),
  busy: false,
  requestPending: false,
  gone: false,
  streamReady: true,
  verificationPending: false,
  latestTurnStatus: "done" as const,
};

describe("canContinueInOwnAgent", () => {
  it("offers the hand-off once the verdict is in", () => {
    expect(canContinueInOwnAgent(settled)).toBe(true);
  });

  it("waits while any turn is writing to the session", () => {
    expect(canContinueInOwnAgent({ ...settled, busy: true })).toBe(false);
    expect(canContinueInOwnAgent({ ...settled, requestPending: true })).toBe(
      false,
    );
    expect(
      canContinueInOwnAgent({ ...settled, latestTurnStatus: "running" }),
    ).toBe(false);
    expect(
      canContinueInOwnAgent({
        ...settled,
        run: run({ status: "running" }),
        latestTurnStatus: undefined,
      }),
    ).toBe(false);
    expect(
      canContinueInOwnAgent({ ...settled, verificationPending: true }),
    ).toBe(false);
  });

  it("waits for a replay to catch up before trusting the transcript", () => {
    expect(canContinueInOwnAgent({ ...settled, streamReady: false })).toBe(
      false,
    );
  });

  it("waits through stopping, whose terminal frame precedes the exit", () => {
    expect(
      canContinueInOwnAgent({ ...settled, run: run({ status: "stopping" }) }),
    ).toBe(false);
  });

  it("trusts a finished transcript over a lagging run summary", () => {
    expect(
      canContinueInOwnAgent({ ...settled, run: run({ status: "running" }) }),
    ).toBe(true);
  });

  it("stays hidden on a run that is gone", () => {
    expect(canContinueInOwnAgent({ ...settled, gone: true })).toBe(false);
  });
});

describe("ContinueInAgentButton", () => {
  const command = "cd ~/.radar/agent && claude --resume 'ses_1'";

  it("splits a labelled terminal action from a menu of other ways", () => {
    const html = renderToStaticMarkup(
      <ContinueInAgentButton run={run()} command={command} />,
    );
    expect(html).toContain("Continue in Claude Code");
    expect(html).toContain('aria-label="More ways to continue"');
    expect(html).toContain('aria-haspopup="menu"');
    expect(html).toContain('aria-expanded="false"');
    expect(html.match(/<button/g)).toHaveLength(2);
  });

  it("names the agent that ran the investigation", () => {
    const html = renderToStaticMarkup(
      <ContinueInAgentButton
        run={run({ agent: "codex" })}
        command="codex resume 'ses_1'"
      />,
    );
    expect(html).toContain("Continue in Codex");
  });

  it("warns that Claude Code's trust prompt exits on Enter", () => {
    const html = renderToStaticMarkup(
      <ContinueInAgentButton run={run()} command={command} />,
    );
    expect(html).toContain("data-continue-trust-hint");
    expect(html).toContain("agent folder");
    // Announced with the button, not only shown under it.
    const hintId = html.match(/id="([^"]+)" data-continue-trust-hint/)?.[1];
    expect(hintId).toBeTruthy();
    expect(html).toContain(`aria-describedby="${hintId}"`);
  });

  it("does not show the trust hint for Codex", () => {
    const html = renderToStaticMarkup(
      <ContinueInAgentButton
        run={run({ agent: "codex" })}
        command="codex resume 'ses_1'"
      />,
    );
    expect(html).not.toContain("data-continue-trust-hint");
    expect(html).not.toContain("agent folder");
  });

  it("opens the command menu from one button when Radar has no terminal", () => {
    mockLocalTerminal = false;
    const html = renderToStaticMarkup(
      <ContinueInAgentButton run={run()} command={command} />,
    );
    expect(html).toContain("Continue in Claude Code");
    expect(html).toContain('aria-haspopup="menu"');
    expect(html).not.toContain("More ways to continue");
    expect(html.match(/<button/g)).toHaveLength(1);
    // The hint for the copied command lives in the menu instead.
    expect(html).not.toContain("data-continue-trust-hint");
  });
});
