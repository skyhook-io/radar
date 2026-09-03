import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import {
  AgentControls,
  ConsentCard,
  ResultCard,
  Timeline,
  TurnView,
  appendThinking,
  type Turn,
} from "./parts";
import type {
  AgentInfo,
  Diagnosis,
  ExecutionProfile,
} from "../../api/diagnose";

const noop = vi.fn();

describe("appendThinking", () => {
  it("separates adjacent bold reasoning headings without changing ordinary chunks", () => {
    const first = appendThinking([], "**Inspecting workload**", false);
    const headings = appendThinking(first, "**Reading crash logs**", false);
    const chunks = appendThinking(headings, " and events", false);

    expect(headings).toEqual([
      {
        kind: "thinking",
        text: "**Inspecting workload**",
        animate: false,
      },
      {
        kind: "thinking",
        text: "**Reading crash logs**",
        animate: false,
      },
    ]);
    expect(chunks[1]).toMatchObject({
      text: "**Reading crash logs** and events",
    });

    expect(
      appendThinking([], "**Checking logs**\n**Checking events**", false),
    ).toMatchObject([
      { kind: "thinking", text: "**Checking logs**" },
      { kind: "thinking", text: "**Checking events**" },
    ]);
  });

  it("deduplicates an exact repeated reasoning beat", () => {
    const first = appendThinking([], "**Inspecting workload**", false);
    expect(appendThinking(first, "**Inspecting workload**", false)).toEqual(
      first,
    );
  });

  it("preserves repeated ordinary lines identically in live chunks and replay", () => {
    const line = "The retry produced the same timeout.\n";
    const replay = appendThinking([], line + line, false);
    const live = appendThinking(appendThinking([], line, true), line, true);

    expect(
      live.map((item) => (item.kind === "thinking" ? item.text : undefined)),
    ).toEqual(
      replay.map((item) => (item.kind === "thinking" ? item.text : undefined)),
    );
    expect(live).toMatchObject([
      {
        kind: "thinking",
        text: line + line,
        animate: true,
      },
    ]);
  });
});

describe("Timeline reasoning density", () => {
  const reasoning = {
    kind: "thinking" as const,
    text: "A long reasoning beat that may occupy several lines in the activity pane.",
    animate: false,
  };

  it("clamps completed reasoning to two lines", () => {
    const html = renderToStaticMarkup(
      <Timeline items={[reasoning]} running={false} />,
    );

    expect(html).toContain("line-clamp-2");
    expect(html).not.toContain("animate-transcript-enter");
    expect(html).not.toContain("Show reasoning");
  });

  it("keeps the final live reasoning beat unclamped", () => {
    const html = renderToStaticMarkup(<Timeline items={[reasoning]} running />);

    expect(html).not.toContain("line-clamp-2");
  });

  it("clamps an earlier reasoning beat once a live tool follows it", () => {
    const html = renderToStaticMarkup(
      <Timeline
        items={[
          reasoning,
          {
            kind: "tool",
            id: "tool-1",
            tool: "get_resource",
            status: "running",
            animate: false,
          },
        ]}
        running
      />,
    );

    expect(html).toContain("line-clamp-2");
  });
});

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
    expect(html).toContain("--force");
    expect(html).toContain("auto-approves its built-in tools");
    expect(html).toContain("including your global servers");
    expect(html).toContain("does not reliably confine those tools");
    expect(html).not.toContain("still enables the agent CLI");
    expect(html).not.toContain("does not constrain external MCP servers");
    expect(html).not.toContain("always runs this agent with safeguards");
  });

  it("discloses the Claude configuration that Radar cannot exclude", () => {
    const html = renderAgent(
      "claude",
      ["safeguarded", "full-local"],
      "safeguarded",
    );
    expect(html).toContain("built-in tools are disabled");
    expect(html).toContain(
      "settings, hooks, and CLAUDE.md instructions still apply",
    );
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
    expect(html).toContain("--force");
    expect(html).toContain("auto-approves its built-in tools");
    expect(html).toContain("including your global servers");
    expect(html).toContain("does not reliably confine those tools");
    expect(html).not.toContain("still enables the agent CLI");
    expect(html).not.toContain("does not constrain external MCP servers");
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

describe("ResultCard conclusion states", () => {
  const diagnosis = (patch: Partial<Diagnosis>): Diagnosis => ({
    rootCause: "",
    report: "",
    remediation: [],
    ...patch,
  });

  const conclusionCases: Array<[Diagnosis, string]> = [
    [diagnosis({ rootCause: "The image does not exist." }), "Likely cause"],
    [
      diagnosis({ healthy: true, report: "The workload is ready." }),
      "No active problems found",
    ],
    [diagnosis({ inconclusive: true }), "Couldn&#x27;t determine"],
  ];

  it.each(conclusionCases)(
    "preserves the outcome-specific heading %#",
    (value, heading) => {
      const html = renderToStaticMarkup(<ResultCard diagnosis={value} />);
      expect(html).toContain(heading);
      expect(html).not.toContain("Verdict");
    },
  );

  it("qualifies a healthy assessment when structured evidence coverage is absent", () => {
    const html = renderToStaticMarkup(
      <ResultCard
        diagnosis={diagnosis({
          healthy: true,
          report: "The workload appears ready.",
        })}
        coverageLimited
      />,
    );

    expect(html).toContain("No problem identified in completed checks");
    expect(html).toContain("Structured evidence is incomplete or unavailable");
    expect(html).toContain("border-amber-500/30");
    expect(html).toContain("text-amber-500");
    expect(html).not.toContain("border-emerald-500/30");
    expect(html).not.toContain("text-emerald-500");
    expect(html).not.toContain(">No active problems found</div>");
  });

  it("reserves the verified healthy treatment for an adequately covered all-clear", () => {
    const html = renderToStaticMarkup(
      <ResultCard
        diagnosis={diagnosis({
          healthy: true,
          report: "The workload is ready.",
        })}
      />,
    );

    expect(html).toContain("No active problems found");
    expect(html).toContain("border-emerald-500/30");
    expect(html).toContain("text-emerald-500");
    expect(html).not.toContain("No problem identified in completed checks");
  });

  it("does not overstate the rank of evidence that conflicts with an all-clear", () => {
    const html = renderToStaticMarkup(
      <ResultCard
        diagnosis={diagnosis({
          healthy: true,
          report: "The workload appears ready.",
        })}
        evidenceConflict
      />,
    );

    expect(html).toContain("Assessment conflicts with captured evidence");
    expect(html).toContain("Radar also captured evidence of an active problem");
    expect(html).not.toContain("Key evidence");
    expect(html).toContain("border-amber-500/40");
    expect(html).not.toContain("border-emerald-500/30");
  });

  it("keeps a follow-up framed as an answer rather than a new conclusion", () => {
    const html = renderToStaticMarkup(
      <ResultCard
        diagnosis={diagnosis({ report: "The restart count is unchanged." })}
        followup
      />,
    );
    expect(html).toContain("Answer");
    expect(html).not.toContain("Likely cause");
    expect(html).not.toContain("Conclusion");
  });

  it.each([
    diagnosis({ healthy: true, report: "It is healthy right now." }),
    diagnosis({
      inconclusive: true,
      report: "I cannot tell from the available checks.",
    }),
  ])("keeps health-flagged ordinary follow-ups conversational", (value) => {
    const html = renderToStaticMarkup(
      <ResultCard diagnosis={value} followup />,
    );

    expect(html).toContain("Answer");
    expect(html).not.toContain("No active problems found");
    expect(html).not.toContain("Couldn&#x27;t determine");
  });

  it("can place the conclusion before evidence and actions after it", () => {
    const value = diagnosis({
      rootCause: "The image does not exist.",
      remediation: ["Push the image, then restart the rollout."],
      recommendedIndex: 1,
    });
    const conclusion = renderToStaticMarkup(
      <ResultCard diagnosis={value} section="conclusion" />,
    );
    const actions = renderToStaticMarkup(
      <ResultCard diagnosis={value} section="actions" />,
    );

    expect(conclusion).toContain("Likely cause");
    expect(conclusion).not.toContain("Remediation");
    expect(actions).toContain("Remediation");
    expect(actions).not.toContain("Likely cause");
  });

  it("does not stagger remediation rows after the result card arrives", () => {
    const html = renderToStaticMarkup(
      <ResultCard
        diagnosis={diagnosis({
          rootCause: "The image does not exist.",
          remediation: ["Push the image.", "Restart the rollout."],
          recommendedIndex: 1,
        })}
      />,
    );

    expect(html).not.toContain("animation-delay");
  });

  it("keeps only the recommended action in the first Findings scan", () => {
    const html = renderToStaticMarkup(
      <ResultCard
        diagnosis={diagnosis({
          rootCause: "The image does not exist.",
          remediation: [
            "Inspect the registry.",
            "Push the missing image.",
            "Restart the rollout.",
          ],
          recommendedIndex: 2,
        })}
        section="actions"
        compactActions
      />,
    );

    expect(html).toContain("Push the missing image.");
    expect(html).not.toContain("Inspect the registry.");
    expect(html).not.toContain("Restart the rollout.");
    expect(html).toContain("Show 2 more steps");
  });
});

describe("TurnView tool outcome truth", () => {
  function turn(isError: boolean | undefined): Turn {
    return {
      timeline: [
        {
          kind: "tool",
          id: "tool-1",
          tool: "diagnose",
          status: "done",
          isError,
        },
      ],
      diagnosis: null,
      error: null,
      status: "running",
    };
  }

  it("uses success color only for producer-confirmed success", () => {
    const success = renderToStaticMarkup(<TurnView turn={turn(false)} />);
    const unknown = renderToStaticMarkup(<TurnView turn={turn(undefined)} />);

    expect(success).toContain("text-emerald-400");
    expect(unknown).toContain("text-theme-text-tertiary");
    expect(unknown).not.toContain("text-emerald-400");
    expect(unknown).toContain("lucide-circle-question-mark");
    expect(unknown).not.toContain("lucide-circle-check-big");
  });

  it("renders a producer-confirmed failure as an error", () => {
    const html = renderToStaticMarkup(<TurnView turn={turn(true)} />);
    expect(html).toContain("Tool failed");
    expect(html).toContain("text-red-400");
  });

  it("does not expose an inert button when a tool has no detail", () => {
    const html = renderToStaticMarkup(<TurnView turn={turn(false)} />);
    expect(html).not.toContain("<button");
  });

  it("visibly marks a tool row reached from Findings programmatically", () => {
    const html = renderToStaticMarkup(
      <TurnView
        turn={turn(false)}
        turnIndex={0}
        evidenceStepIds={new Set(["tool-1"])}
        onViewEvidence={noop}
      />,
    );

    expect(html).toContain("focus:ring-2");
    expect(html).not.toContain("focus-visible:ring-2");
  });

  it("connects a detail disclosure to the region it controls", () => {
    const detailed = turn(false);
    const item = detailed.timeline[0];
    if (item.kind === "tool") item.summary = '{"ready":true}';
    const html = renderToStaticMarkup(<TurnView turn={detailed} />);
    const controlled = html.match(/aria-controls="([^"]+)"/)?.[1];

    expect(controlled).toBeTruthy();
    expect(html).toContain(`id="${controlled}"`);
  });

  it("renders replayed activity and its conclusion without arrival motion", () => {
    const replayed = turn(false);
    replayed.status = "done";
    replayed.timeline[0].animate = false;
    replayed.diagnosis = {
      healthy: true,
      rootCause: "",
      report: "The workload is ready.",
      remediation: [],
    };
    replayed.animateResult = false;

    const html = renderToStaticMarkup(<TurnView turn={replayed} />);
    expect(html).not.toContain("animate-transcript-enter");
    expect(html).not.toContain("animate-result-in");
  });

  it("frames automatic verification as system activity, not user chat", () => {
    const verification = turn(false);
    verification.question = "internal verification prompt";
    verification.verify = true;

    const html = renderToStaticMarkup(<TurnView turn={verification} />);
    expect(html).toContain("Automatic verification");
    expect(html).toContain("Re-checking after apply");
    expect(html).not.toContain("internal verification prompt");
  });

  it("renders an unknown apply prominently and offers current-state verification", () => {
    const apply = turn(false);
    apply.apply = true;
    apply.status = "error";
    apply.applyOutcome = "unknown";
    apply.error =
      "The apply stopped before Radar could confirm whether the change completed.";

    const html = renderToStaticMarkup(
      <TurnView turn={apply} onCheckStatus={noop} />,
    );
    expect(html).toContain("Outcome unknown");
    expect(html).toContain("border-amber-500/40");
    expect(html).toContain(
      "At this point, Radar could not safely allow another apply without",
    );
    expect(html).toContain("Check current status");
    expect(html).toContain("could confirm whether the change completed");
    expect(html).not.toContain(">Applied</div>");
  });

  it("reserves green Applied for a producer-confirmed mutation", () => {
    const apply = turn(undefined);
    apply.apply = true;
    apply.status = "done";
    apply.applyOutcome = "confirmed";
    apply.diagnosis = {
      rootCause: "",
      report: "Deployment updated.",
      remediation: [],
    };

    const html = renderToStaticMarkup(<TurnView turn={apply} />);
    expect(html).toContain("Applied");
    expect(html).toContain("border-emerald-500/30");
    expect(html).toContain("Mutation confirmed by a Radar write-tool result");
    expect(html).not.toContain("Outcome unknown");
    expect(html).not.toContain("Not applied");
  });

  it("renders an authoritative apply failure as Not applied and never green", () => {
    const apply = turn(undefined);
    apply.apply = true;
    apply.status = "error";
    apply.applyOutcome = "failed";
    apply.error = "The write tool rejected the change.";

    const html = renderToStaticMarkup(
      <TurnView turn={apply} onCheckStatus={noop} />,
    );
    expect(html).toContain("Not applied");
    expect(html).toContain("border-red-500/30");
    expect(html).toContain("The write tool rejected the change.");
    expect(html).not.toContain("border-emerald-500/30");
    expect(html).not.toContain("Check current status");
  });

  it("keeps confirmed mutation truth when the agent report is incomplete", () => {
    const apply = turn(undefined);
    apply.apply = true;
    apply.status = "error";
    apply.applyOutcome = "confirmed";
    apply.error =
      "A Radar write tool confirmed the mutation, but the agent ended before completing its report.";

    const html = renderToStaticMarkup(<TurnView turn={apply} />);
    expect(html).toContain("Applied");
    expect(html).toContain("border-emerald-500/30");
    expect(html).toContain("agent report incomplete");
    expect(html).toContain("confirmed the mutation");
    expect(html).not.toContain("Not applied");
  });

  it("fails closed when an apply terminal event has no outcome metadata", () => {
    const apply = turn(undefined);
    apply.apply = true;
    apply.status = "done";
    apply.diagnosis = {
      rootCause: "",
      report: "The agent says the deployment was updated.",
      remediation: [],
    };

    const html = renderToStaticMarkup(<TurnView turn={apply} />);
    expect(html).toContain("Outcome unknown");
    expect(html).toContain("border-amber-500/40");
    expect(html).not.toContain("border-emerald-500/30");
  });

  it.each([
    { healthy: true, report: "It is healthy right now." },
    {
      inconclusive: true,
      report: "I cannot tell from the available checks.",
    },
  ])(
    "does not promote a health-flagged question into a conclusion",
    (patch) => {
      const followup = turn(false);
      followup.question = "Is it healthy now?";
      followup.status = "done";
      followup.diagnosis = {
        rootCause: "",
        remediation: [],
        ...patch,
      };

      const html = renderToStaticMarkup(<TurnView turn={followup} />);
      expect(html).toContain("Answer");
      expect(html).not.toContain("No active problems found");
      expect(html).not.toContain("Couldn&#x27;t determine");
    },
  );
});
