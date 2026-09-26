import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import type { Diagnosis } from "../../api/diagnose";
import { ResultCard, TurnView, assessmentCopyText, type Turn } from "./parts";

const storyDiagnosis: Diagnosis = {
  rootCause: "Auth to MongoDB fails after revision 8.",
  summary:
    "The app cannot log in to its database, and it started with the last deploy.",
  certainty: "likely",
  unresolved: ["Whether the Atlas password was rotated on the provider side."],
  report:
    "The container dies on start.\n\n[[radar:evidence=0]]\n\nSo the build changed.",
  remediation: [
    "Roll back to revision 7",
    "Test the stored password with `mongosh`",
  ],
  steps: [
    {
      text: "Roll back to revision 7",
      kind: "mitigate",
      precondition: "revision 7 still authenticates",
    },
    { text: "Test the stored password with `mongosh`", kind: "verify" },
  ],
  recommendedIndex: 1,
  recommendedReason: "reversible",
  confidence: 0.8,
};

describe("ResultCard under the story contract", () => {
  it("leads with the summary, the agent's certainty and what is not established", () => {
    const html = renderToStaticMarkup(
      <ResultCard diagnosis={storyDiagnosis} section="conclusion" />,
    );
    expect(html).toContain("data-assessment-headline");
    expect(html).toContain(
      "The app cannot log in to its database, and it started with the last deploy.",
    );
    expect(html).toContain("Likely");
    expect(html).toContain("Auth to MongoDB fails after revision 8.");
    expect(html).toContain("Still open");
    expect(html).toContain("Whether the Atlas password was rotated");
    // The story is the host's to render with placed cards: no second copy.
    expect(html).not.toContain("Full analysis");
    expect(html).not.toContain("[[radar:evidence=0]]");
  });

  it("frames adverse Radar cards on a healthy verdict by the agent's position, without a coloured box", () => {
    const healthy = {
      ...storyDiagnosis,
      healthy: true,
      rootCause: "",
      remediation: [],
      unresolved: [],
    };
    const explained = renderToStaticMarkup(
      <ResultCard
        diagnosis={healthy}
        section="conclusion"
        healthSignals={[
          {
            title: "Readiness probe failing",
            status: "explained",
            claim: "never left endpoints",
            sourceId: "s1",
          },
        ]}
        onRevealSource={vi.fn()}
      />,
    );
    expect(explained).toContain("Still open");
    expect(explained).toContain(
      "reads it as not a live problem: never left endpoints",
    );
    expect(explained).not.toContain("data-health-flag");
    expect(explained).not.toContain("conflicts with captured evidence");
    const flagged = renderToStaticMarkup(
      <ResultCard
        diagnosis={healthy}
        section="conclusion"
        healthSignals={[
          { title: "Readiness probe failing", status: "unaddressed" },
        ]}
      />,
    );
    expect(flagged).toContain('data-health-flag="unaddressed"');
    expect(flagged).toContain("no explanation is linked to it");
  });

  it("keeps the flag banner on a healthy verdict whose story places cards but has no summary", () => {
    const html = renderToStaticMarkup(
      <ResultCard
        diagnosis={{
          ...storyDiagnosis,
          healthy: true,
          summary: "",
          rootCause: "",
          remediation: [],
          unresolved: [],
        }}
        section="conclusion"
        healthSignals={[
          { title: "Readiness probe failing", status: "unaddressed" },
        ]}
      />,
    );
    expect(html).toContain('data-health-flag="unaddressed"');
    expect(html).not.toContain("[[radar:evidence=0]]");
  });

  it("keeps the inline story on a read-only earlier healthy assessment", () => {
    const html = renderToStaticMarkup(
      <ResultCard
        diagnosis={{
          ...storyDiagnosis,
          healthy: true,
          rootCause: "",
          remediation: [],
          unresolved: [],
          report:
            "The pod is fine.\n\n[[radar:evidence=0]]\n\n" +
            "Detail. ".repeat(60),
        }}
        section="conclusion"
        storyInline
        readOnlyAssessment
      />,
    );
    expect(html).toContain("Full analysis");
    expect(html).not.toContain("[[radar:evidence=0]]");
  });

  it("lists Radar's own read limits under Still open beside the agent's items", () => {
    const html = renderToStaticMarkup(
      <ResultCard
        diagnosis={{ ...storyDiagnosis, unresolved: [] }}
        section="conclusion"
        assessmentLimits={["Previous logs: could not be read"]}
      />,
    );
    expect(html).toContain("Still open");
    expect(html).toContain("Previous logs: could not be read");
  });

  it("says nothing when the agent listed nothing unresolved: the certainty word carries it", () => {
    const html = renderToStaticMarkup(
      <ResultCard
        diagnosis={{
          ...storyDiagnosis,
          unresolved: [],
          certainty: "established",
        }}
        section="conclusion"
      />,
    );
    expect(html).not.toContain("nothing unresolved");
    expect(html).not.toContain("data-assessment-unresolved");
  });

  it("renders typed steps with their kind and precondition and applies only a mitigate step", () => {
    const onApply = vi.fn();
    const html = renderToStaticMarkup(
      <ResultCard
        diagnosis={storyDiagnosis}
        section="actions"
        onApply={onApply}
      />,
    );
    expect(html).toContain('data-step-kind="mitigate"');
    expect(html).toContain('data-step-kind="verify"');
    expect(html).toContain("Only if revision 7 still authenticates");
    const doubled = renderToStaticMarkup(
      <ResultCard
        diagnosis={{
          ...storyDiagnosis,
          steps: [
            {
              ...storyDiagnosis.steps![0],
              precondition: "if the crash is deliberate",
            },
          ],
          remediation: [storyDiagnosis.remediation[0]],
        }}
        section="actions"
      />,
    );
    expect(doubled).toContain("Only if the crash is deliberate");
    expect(html).toContain("Apply…");
    const full = renderToStaticMarkup(
      <ResultCard
        diagnosis={storyDiagnosis}
        section="full"
        onApply={onApply}
      />,
    );
    expect(full).toContain("Next steps");
    expect(full).not.toContain(">Remediation<");

    const verifyRecommended = renderToStaticMarkup(
      <ResultCard
        diagnosis={{ ...storyDiagnosis, recommendedIndex: 2 }}
        section="actions"
        onApply={onApply}
      />,
    );
    expect(verifyRecommended).not.toContain("Apply…");

    // Folded alternatives stay readable as one-line rows with their kind.
    const compact = renderToStaticMarkup(
      <ResultCard
        diagnosis={storyDiagnosis}
        section="actions"
        onApply={onApply}
        compactActions
      />,
    );
    expect(compact).toContain('data-step-folded="verify"');
    expect(compact).toContain("Test the stored password with mongosh");
  });

  it("keeps the previous shape for a diagnosis without the story fields", () => {
    const html = renderToStaticMarkup(
      <ResultCard
        diagnosis={{
          rootCause: "Image tag is invalid.",
          report: "Long analysis.",
          remediation: ["Fix the tag"],
        }}
        section="full"
      />,
    );
    expect(html).toContain("Likely cause");
    expect(html).toContain("Full analysis");
    expect(html).toContain("Remediation");
    expect(html).not.toContain("data-assessment-headline");
  });

  it("shows an inconclusive verdict's blocking question and its verify steps", () => {
    const html = renderToStaticMarkup(
      <ResultCard
        diagnosis={{
          rootCause: "",
          inconclusive: true,
          summary:
            "Radar could not read the pod logs, so the cause is unknown.",
          unresolved: ["Whether the container logs show an error on start."],
          report: "I could not read logs.",
          remediation: ["Grant `get pods/log` and re-run"],
          steps: [
            { text: "Grant `get pods/log` and re-run", kind: "investigate" },
          ],
        }}
        section="full"
      />,
    );
    expect(html).toContain("What blocked a conclusion");
    expect(html).toContain('data-step-kind="investigate"');
    expect(html).not.toContain("Apply…");
  });
});

describe("assessmentCopyText", () => {
  it("gives a teammate the whole report: story, every step and where it came from", () => {
    const context = { target: "Deployment shop/api", agent: "Claude Code" };
    const channel = assessmentCopyText(storyDiagnosis, { context });
    const full = assessmentCopyText(storyDiagnosis, { context, full: true });
    expect(channel).toContain("open the investigation in Radar");
    expect(full).not.toContain("open the investigation in Radar");
    expect(full).toContain("From a local Radar investigation run with Claude Code.");
    expect(full.length).toBeGreaterThan(channel.length);
    expect(full).toContain(
      "1. **Recommended:** [Mitigate] Roll back to revision 7 (only if revision 7 still authenticates)",
    );
    expect(full).toContain("2. [Verify] Test the stored password");
    expect(full).toContain("Why step 1: reversible");
  });
  it("marks no step in the full report when the agent pointed at none, or at a non-mitigating one", () => {
    const none = assessmentCopyText(
      { ...storyDiagnosis, recommendedIndex: 0 },
      { full: true },
    );
    const verify = assessmentCopyText(
      { ...storyDiagnosis, recommendedIndex: 2 },
      { full: true },
    );
    for (const text of [none, verify]) {
      expect(text).not.toContain("Recommended:");
      expect(text).not.toContain("Why step");
    }
  });
  it("carries Radar's own qualifications so the pasted text is no more confident than the screen", () => {
    const text = assessmentCopyText(storyDiagnosis, {
      limits: ["Change history: incomplete"],
      signals: [
        "Radar flagged Warning event · the agent reads it as not a live problem: probe timeout",
      ],
      flags: [
        "Radar flagged CrashLoopBackOff · no explanation is linked to it",
      ],
    });
    expect(text).toContain(
      "Radar flagged:\n- Radar flagged CrashLoopBackOff · no explanation is linked to it\nRead those cards before treating this as an all-clear.",
    );
    expect(text).toContain("- Change history: incomplete");
    expect(text).toContain(
      "- Radar flagged Warning event · the agent reads it as not a live problem: probe timeout",
    );
    expect(text.indexOf("Radar flagged:")).toBeLessThan(
      text.indexOf("Still open:"),
    );
  });
  it("names the recommended step and counts the rest for a legacy remediation list", () => {
    const text = assessmentCopyText(
      {
        ...storyDiagnosis,
        steps: undefined,
        remediation: ["a", "b", "c"],
        recommendedIndex: 2,
      },
      { context: { target: "Deployment shop/api" } },
    );
    expect(text).toContain("Next step: b");
    expect(text).toContain("2 more steps in Radar.");
  });

  it("puts the healthy word on the headline and the flags before Still open", () => {
    const text = assessmentCopyText(
      {
        ...storyDiagnosis,
        healthy: true,
        summary: "Fine.",
        rootCause: "",
        unresolved: ["One thing."],
      },
      { flags: ["Radar flagged BackOff · no explanation is linked to it"] },
    );
    expect(text).toContain("**Fine.**");
    expect(text).toContain("Healthy");
    expect(text.indexOf("Radar flagged:")).toBeLessThan(
      text.indexOf("Still open:"),
    );
  });

  it("copies the headline with its caveats, cause, story and steps", () => {
    const text = assessmentCopyText(storyDiagnosis);
    expect(text).toContain("**The app cannot log in to its database");
    expect(text).toContain("— Likely (agent's word)");
    expect(text).toContain("Still open:\n- Whether the Atlas password");
    expect(text).toContain("Auth to MongoDB fails");
    expect(text).toContain("So the build changed.");
    expect(text).not.toContain("[[radar:evidence=0]]");
    expect(text).toContain(
      "1. [Mitigate] Roll back to revision 7 (only if revision 7 still authenticates)",
    );
  });

  it("writes a channel-ready note with context, receipts and the one next step", () => {
    const text = assessmentCopyText(storyDiagnosis, {
      context: {
        target: "Deployment shop/api",
        cluster: "prod-east",
        agent: "Claude Code",
      },
      receipts: [
        {
          title: "Current logs · api-7d4 / api",
          role: "Cause",
          lines: ["MongoServerError: Authentication failed.", ""],
          gap: "Only the previous container instance was checked.",
        },
      ],
    });
    expect(
      text.startsWith("**Deployment shop/api** · prod-east · via Claude Code"),
    ).toBe(true);
    expect(text).toContain(
      "Evidence (Radar):\n- **Current logs · api-7d4 / api** · Cause\n  > MongoServerError: Authentication failed.\n  Not shown: Only the previous container instance was checked.",
    );
    expect(text).toContain("Next step: [Mitigate] Roll back to revision 7");
    expect(text).not.toContain("So the build changed.");
    expect(text).toContain("open the investigation in Radar.");
  });
});

describe("TurnView and assessments", () => {
  const turn = (patch: Partial<Turn>): Turn =>
    ({
      status: "done",
      timeline: [],
      diagnosis: storyDiagnosis,
      ...patch,
    }) as Turn;

  it("renders a revised question turn as an assessment, not a conversational answer", () => {
    const html = renderToStaticMarkup(
      <TurnView
        turn={turn({ question: "could it be the build?" })}
        assessment
        turnIndex={1}
      />,
    );
    expect(html).toContain("data-assessment-headline");
    expect(html).toContain("Earlier assessment");
    expect(html).not.toContain(">Answer<");
    // Read-only: Apply is never offered on a copy kept for the record.
    expect(html).not.toContain("Apply…");
    // The story is inline here as plain prose, markers stripped.
    expect(html).toContain("Full analysis");
    expect(html).not.toContain("[[radar:evidence=0]]");
  });

  it("folds the agent's evidence ledger under the turn, out of Findings", () => {
    const html = renderToStaticMarkup(
      <TurnView
        turn={turn({
          diagnosis: {
            rootCause: "x",
            report: "The story.",
            remediation: [],
            notes: "Result A shows one exit; it does not cover the other two.",
          },
        })}
        assessment
        hideConclusion
        turnIndex={0}
      />,
    );
    expect(html).toContain("data-turn-working-notes");
    expect(html).toContain("Evidence considered");
    expect(html).toContain("does not cover the other two");
    expect(html).toContain("Assessment · shown in Findings");
  });

  it("keeps a pointer for the current assessment and the full answer for a plain question", () => {
    const pointer = renderToStaticMarkup(
      <TurnView turn={turn({})} assessment hideConclusion turnIndex={0} />,
    );
    expect(pointer).toContain("Assessment · shown in Findings");
    expect(pointer).not.toContain("data-assessment-headline");
    const answer = renderToStaticMarkup(
      <TurnView
        turn={turn({
          question: "what is a PDB?",
          diagnosis: { rootCause: "", report: "A budget.", remediation: [] },
        })}
        turnIndex={2}
      />,
    );
    expect(answer).toContain(">Answer<");
    expect(answer).toContain("A budget.");
  });
});
