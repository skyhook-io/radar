import type { Diagnosis } from "../../api/diagnose";
import { CERTAINTY_LABEL } from "./assessmentCopy";

// A code span is literal: its contents (`__init__`, `*.svc.*`) are never
// read as markdown. Splitting on it first keeps cleanup to the prose.
const CODE_SPAN = /(`[^`\n]+`)/g;

function isCodeSpan(part: string): boolean {
  return part.length > 2 && part.startsWith("`") && part.endsWith("`");
}

type VerdictPart = { code: boolean; text: string };

// A verdict split into prose and literal code, prose with its bold and
// single-character emphasis markers dropped. Doubled underscores stay: in a
// verdict they are far more often an identifier (checkout__config) than
// markdown. Shared by every place that shows a verdict sentence.
export function verdictParts(text: string): VerdictPart[] {
  return text.split(CODE_SPAN).flatMap((part): VerdictPart[] => {
    if (isCodeSpan(part)) return [{ code: true, text: part.slice(1, -1) }];
    const prose = part
      .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
      .replace(/\*\*/g, "")
      .replace(/(^|\W)[*_](\S(?:[^*_\n]*\S)?)[*_](?=\W|$)/g, "$1$2");
    return prose ? [{ code: false, text: prose }] : [];
  });
}

// A verdict as one plain line, for places that show it outside Findings: the
// history and the completion card. Markdown links keep their text; emphasis
// markers are dropped and code keeps its exact contents.
export function plainVerdictText(text: string | undefined): string {
  return verdictParts(text ?? "")
    .map((part) => part.text)
    .join("")
    .replace(/\s+/g, " ")
    .trim();
}

// The sentence a finished assessment stands on: its headline when it has
// one, else the technical cause. Empty for an answer that settled nothing.
export function verdictSentence(diagnosis: {
  summary?: string;
  rootCause?: string;
}): string {
  return plainVerdictText(diagnosis.summary?.trim() || diagnosis.rootCause);
}

// How sure the assessment is, in the words Findings shows beside it, so a
// sentence repeated elsewhere never reads more certain than Findings does.
export function assessmentStateLabel(
  diagnosis: Partial<
    Pick<Diagnosis, "inconclusive" | "healthy" | "rootCause" | "certainty">
  >,
  evidenceConflict = false,
): { text: string; warn: boolean } | undefined {
  if (diagnosis.inconclusive && !diagnosis.rootCause) {
    return { text: "Couldn't determine", warn: false };
  }
  if (diagnosis.healthy && !diagnosis.rootCause) {
    return evidenceConflict
      ? { text: "Conflicts with captured evidence", warn: true }
      : { text: "Healthy", warn: false };
  }
  return diagnosis.certainty
    ? { text: CERTAINTY_LABEL[diagnosis.certainty], warn: false }
    : undefined;
}
