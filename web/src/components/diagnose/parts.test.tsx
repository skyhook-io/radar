import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import {
  AgentControls,
  ConsentCard,
  COPILOT_EFFORT_OPTIONS,
  EFFORT_OPTIONS,
} from "./parts";
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

  it("describes what Radar actually removes for a safeguarded Copilot run", () => {
    const html = renderAgent(
      "copilot",
      ["safeguarded", "full-local"],
      "safeguarded",
    );
    expect(html).toContain("only Radar");
    expect(html).toContain("shell, file edits and web access are removed");
    expect(html).toContain("AGENTS.md");
    // Must not fall through to the generic copy now that Copilot is supported.
    expect(html).not.toContain("documented restrictions");
    expect(html).not.toContain("Codex");
  });

  it("offers Copilot a reasoning-effort control", () => {
    const html = renderAgent(
      "copilot",
      ["safeguarded", "full-local"],
      "safeguarded",
    );
    // Previously Codex-only; Copilot has the same knob with a wider range.
    expect(html).toContain("Reasoning effort");
  });

  it("gives Claude no reasoning-effort control", () => {
    const html = renderAgent(
      "claude",
      ["safeguarded", "full-local"],
      "safeguarded",
    );
    expect(html).not.toContain("Reasoning effort");
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

// The menus are curated subsets of each CLI's vocabulary, and the server rejects
// a level the selected agent doesn't accept (ai.ReasoningEfforts). Codex being
// offered xhigh would surface as an unexplainable 400.
describe("reasoning-effort menus", () => {
  const values = (opts: typeof EFFORT_OPTIONS) => opts.map((o) => o.value);

  it("offers Copilot's wider tiers only to Copilot", () => {
    expect(values(COPILOT_EFFORT_OPTIONS)).toEqual(
      expect.arrayContaining(["xhigh", "max"]),
    );
    expect(values(EFFORT_OPTIONS)).not.toEqual(
      expect.arrayContaining(["xhigh"]),
    );
    expect(values(EFFORT_OPTIONS)).not.toEqual(expect.arrayContaining(["max"]));
  });

  it("keeps the default (empty) choice on both menus", () => {
    expect(values(EFFORT_OPTIONS)).toContain("");
    expect(values(COPILOT_EFFORT_OPTIONS)).toContain("");
  });

  // Radar forces medium for Codex but passes no --effort at all for Copilot: the
  // flag is rejected outright when the account's model resolves to "auto".
  it("does not promise Copilot a Radar-chosen default", () => {
    const copilotDefault = COPILOT_EFFORT_OPTIONS.find((o) => o.value === "");
    expect(copilotDefault?.description).not.toContain("medium");
    const codexDefault = EFFORT_OPTIONS.find((o) => o.value === "");
    expect(codexDefault?.description).toContain("medium");
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

  // The transcript carries cluster data and Copilot exports sessions to GitHub by
  // default, so the fact that Radar disables that is a consent-relevant claim.
  it("tells Copilot users the session is never exported to GitHub", () => {
    for (const profile of ["safeguarded", "full-local"] as ExecutionProfile[]) {
      const html = renderToStaticMarkup(
        <ConsentCard
          agentName="GitHub Copilot CLI"
          agent="copilot"
          profile={profile}
          onApprove={noop}
          onCancel={noop}
        />,
      );
      expect(html).toContain("never exported to GitHub");
    }
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
