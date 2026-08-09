import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { AgentControls, ConsentCard } from "./parts";
import type { AgentInfo, ExecutionProfile } from "../../api/diagnose";

const noop = vi.fn();

function renderAgent(
  agent: string,
  profiles: ExecutionProfile[],
  profile: ExecutionProfile,
) {
  const agents: AgentInfo[] = [
    {
      name: agent,
      label: agent,
      path: agent,
      version: "",
      present: true,
      supported: true,
      profiles,
    },
  ];
  return renderToStaticMarkup(
    <AgentControls
      agents={agents}
      selectedAgent={agent}
      onSelectAgent={noop}
      profile={profile}
      onSetProfile={noop}
      model=""
      onSetModel={noop}
      effort=""
      onSetEffort={noop}
    />,
  );
}

describe("AgentControls execution profile explanation", () => {
  it("shows Cursor's fixed full-local warning even when the stored profile is stale", () => {
    const html = renderAgent("cursor-agent", ["full-local"], "safeguarded");
    expect(html).toContain("must use this agent");
    expect(html).toContain("normal setup");
    expect(html).toContain("always loads your global MCP servers");
    expect(html).toContain("still enables the agent CLI");
    expect(html).toContain("does not constrain external MCP servers");
    expect(html).not.toContain("always runs this agent with safeguards");
  });

  it("discloses the Claude configuration that Radar cannot exclude", () => {
    const html = renderAgent(
      "claude",
      ["safeguarded", "full-local"],
      "safeguarded",
    );
    expect(html).toContain("built-in tools are disabled");
    expect(html).toContain("settings, hooks, and CLAUDE.md instructions still apply");
    expect(html).toContain("Your claude setup");
    expect(html).not.toContain("other agent configuration is excluded");
  });

  it("explains that Claude full-local inherits the user's permissions", () => {
    const html = renderAgent(
      "claude",
      ["safeguarded", "full-local"],
      "full-local",
    );
    expect(html).toContain("permissions from your setup");
    expect(html).toContain("Radar does not override them");
    expect(html).not.toContain("Radar still enables the agent CLI");
  });

  it("does not describe an unknown safeguarded agent as Codex", () => {
    const html = renderAgent("future-agent", ["safeguarded"], "safeguarded");
    expect(html).toContain("documented restrictions");
    expect(html).not.toContain("Codex");
    expect(html).not.toContain("built-in tools are disabled");
  });

  it("labels the selectable full-local profile as the user's agent setup", () => {
    const html = renderAgent(
      "codex",
      ["safeguarded", "full-local"],
      "safeguarded",
    );
    expect(html).toContain("Your codex setup");
    expect(html).not.toContain("Full local setup");
  });
});

describe("ConsentCard execution profile treatment", () => {
  it("uses warning chrome and explicit consequences for the user's agent setup", () => {
    const html = renderToStaticMarkup(
      <ConsentCard
        agentName="Cursor Agent"
        agent="cursor-agent"
        profile="full-local"
        onApprove={noop}
        onCancel={noop}
      />,
    );
    expect(html).toContain("Run using your Cursor Agent setup?");
    expect(html).toContain("Continue with my agent setup");
    expect(html).toContain("border-amber-500/40");
    expect(html).toContain("text-amber-500");
    expect(html).toContain("Radar cannot constrain");
    expect(html).toContain("always loads your global MCP servers");
    expect(html).not.toContain("text-accent");
  });

  it("does not claim Radar enables a sandbox for Claude's normal setup", () => {
    const html = renderToStaticMarkup(
      <ConsentCard
        agentName="Claude Code"
        agent="claude"
        profile="full-local"
        onApprove={noop}
        onCancel={noop}
      />,
    );
    expect(html).toContain("permissions from your setup");
    expect(html).toContain("Radar does not override them");
    expect(html).not.toContain("Radar still enables the agent CLI");
  });
});

// The consent card's error box is the ONLY place a refused approval is
// explained: the card stays mounted on failure, focus doesn't move, and nothing
// else on screen changes. Red (not the card's own amber) and role="alert", or a
// screen-reader user re-presses a button the server will never accept.
describe("ConsentCard error", () => {
  const base = {
    agentName: "Claude",
    profile: "safeguarded" as ExecutionProfile,
    onApprove: noop,
    onCancel: noop,
  };

  it("renders the server's refusal as an alert", () => {
    const html = renderToStaticMarkup(
      <ConsentCard {...base} error="An owner has to allow it once." />,
    );
    expect(html).toContain('role="alert"');
    expect(html).toContain("An owner has to allow it once.");
  });

  it("uses the error tone, not the card's warning tone", () => {
    // full-local renders the card itself amber; an amber box on it reads as
    // another paragraph of body copy rather than a failure.
    const html = renderToStaticMarkup(
      <ConsentCard
        {...base}
        profile={"full-local" as ExecutionProfile}
        error="Refused."
      />,
    );
    expect(html).toContain("border-red-500/30");
  });

  it("renders no alert when the approval hasn't failed", () => {
    const html = renderToStaticMarkup(<ConsentCard {...base} />);
    expect(html).not.toContain('role="alert"');
  });
});
