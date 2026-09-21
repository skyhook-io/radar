import { describe, expect, it } from "vitest";
import {
  investigationExplanation,
  supportsAssessmentExplanation,
} from "./investigationExplanation";
import type { AgentInfo } from "../../api/diagnose";
import type { Turn } from "./parts";

const turn = (overrides: Partial<Turn> = {}): Turn => ({
  timeline: [],
  diagnosis: null,
  error: null,
  status: "done",
  ...overrides,
});
describe("assessment-local explanation", () => {
  it("requires explicit hosted support without enabling mutations or changing local support", () => {
    const agent: AgentInfo = {
      name: "hub",
      label: "Managed agent",
      path: "",
      version: "",
      supported: true,
      present: true,
      hosted: true,
    };
    expect(supportsAssessmentExplanation(undefined)).toBe(false);
    expect(supportsAssessmentExplanation(agent)).toBe(false);
    expect(
      supportsAssessmentExplanation({
        ...agent,
        assessmentExplanations: false,
      }),
    ).toBe(false);
    expect(
      supportsAssessmentExplanation({ ...agent, assessmentExplanations: true }),
    ).toBe(true);
    expect(supportsAssessmentExplanation({ ...agent, hosted: false })).toBe(
      true,
    );
  });
  it("does not mistake an ordinary answer for an explanation", () => {
    expect(
      investigationExplanation([turn({ question: "Explain simply" })], 2),
    ).toEqual({ status: "idle" });
  });
  it("restores the answer for its originating assessment without using newer answers", () => {
    const answer = turn({
      explainAssessment: 2,
      diagnosis: { report: "Saved explanation" } as Turn["diagnosis"],
    });
    expect(
      investigationExplanation(
        [answer, turn({ explainAssessment: 8, status: "running" })],
        2,
      ),
    ).toEqual({ status: "done", text: "Saved explanation" });
  });
  it("restores pending progress and lets the latest retry supersede a failure", () => {
    const failed = turn({
      explainAssessment: 2,
      status: "error",
      error: "Stopped",
    });
    expect(investigationExplanation([failed], 2)).toEqual({
      status: "error",
      error: "Stopped",
    });
    expect(
      investigationExplanation(
        [failed, turn({ explainAssessment: 2, status: "running" })],
        2,
      ),
    ).toEqual({ status: "running" });
  });
  it("never displays thinking as an answer when the agent returns nothing", () => {
    expect(
      investigationExplanation(
        [
          turn({
            explainAssessment: 2,
            timeline: [{ kind: "thinking", text: "Let me think" }],
          }),
        ],
        2,
      ),
    ).toEqual({
      status: "error",
      error: "The agent did not return an explanation.",
    });
  });
});
