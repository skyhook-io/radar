import { describe, expect, it } from "vitest";
import { investigationExplanation } from "./investigationExplanation";
import type { Turn } from "./parts";

const turn = (overrides: Partial<Turn> = {}): Turn => ({
  timeline: [],
  diagnosis: null,
  error: null,
  status: "done",
  ...overrides,
});
describe("assessment-local explanation", () => {
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
