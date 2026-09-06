import type { AssessmentExplanation, Turn } from "./parts";

export function investigationExplanation(
  turns: readonly Turn[],
  assessmentSequence: number,
): AssessmentExplanation {
  const turn = [...turns]
    .reverse()
    .find((turn) => turn.explainAssessment === assessmentSequence);
  if (!turn) return { status: "idle" };
  if (turn.status === "running") return { status: "running" };
  if (turn.status === "error")
    return {
      status: "error",
      error: turn.error || "The explanation could not be completed.",
    };
  const text =
    turn.diagnosis?.report?.trim() || turn.diagnosis?.rootCause?.trim();
  return text
    ? { status: "done", text }
    : { status: "error", error: "The agent did not return an explanation." };
}
