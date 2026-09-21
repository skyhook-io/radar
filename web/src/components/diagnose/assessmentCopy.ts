import type { Diagnosis } from "../../api/diagnose";
import { storyPlainText } from "./investigationStory";

/**
 * The whole assessment as text: headline, certainty, what is unresolved, the
 * technical cause, the story without its markers, and the steps. Copying only
 * the headline would ship the persuasive half without its caveats.
 */
/** What Radar adds to the agent's verdict when the assessment is copied for a channel. */
export interface AssessmentCopyRadar {
  /** Reads Radar could not complete, as shown in Still open. */
  limits?: readonly string[];
  /** Adverse Radar cards the agent explained, as shown in Still open. */
  signals?: readonly string[];
  /** Adverse Radar cards nothing explains, as shown above a healthy verdict. */
  flags?: readonly string[];
  /** Where and when: the header line a reader in a channel needs first. */
  context?: {
    target: string;
    cluster?: string;
    startedAt?: string;
    agent?: string;
  };
  /** The placed cards, as short excerpts: the receipts behind the headline. */
  receipts?: readonly {
    title: string;
    role?: string;
    lines: readonly string[];
    /** The agent's statement of what this result does not cover. */
    gap?: string;
  }[];
}

/**
 * The assessment as Markdown for a channel or a ticket: context, headline,
 * cause, the receipts, what is still open, the next step. The full story and
 * every captured result stay in Radar; the text says so.
 */
export function assessmentCopyText(
  diagnosis: Diagnosis,
  radar: AssessmentCopyRadar = {},
): string {
  const parts: string[] = [];
  if (radar.context) {
    const when = radar.context.startedAt
      ? new Date(radar.context.startedAt).toLocaleString(undefined, {
          dateStyle: "medium",
          timeStyle: "short",
        })
      : undefined;
    parts.push(
      [
        `**${radar.context.target}**`,
        radar.context.cluster,
        when,
        radar.context.agent ? `via ${radar.context.agent}` : undefined,
      ]
        .filter(Boolean)
        .join(" · "),
    );
  }
  if (diagnosis.summary) {
    const certainty = diagnosis.healthy
      ? "Healthy"
      : diagnosis.certainty
        ? CERTAINTY_LABEL[diagnosis.certainty]
        : undefined;
    parts.push(
      `**${diagnosis.summary.trim()}**${certainty ? ` — ${certainty} (agent's word)` : ""}`,
    );
  } else if (diagnosis.certainty) {
    parts.push(`Certainty (agent): ${CERTAINTY_LABEL[diagnosis.certainty]}`);
  }
  // The pasted text must not read more confident than the screen: Radar's
  // own qualifications travel with the agent's.
  const flags = (radar.flags ?? []).filter((item) => item.trim());
  if (flags.length > 0)
    parts.push(
      [
        "Radar flagged:",
        ...flags.map((item) => `- ${item}`),
        "Read those cards before treating this as an all-clear.",
      ].join("\n"),
    );
  const unresolved = [
    ...(diagnosis.unresolved ?? []),
    ...(radar.limits ?? []),
    ...(radar.signals ?? []),
  ].filter((item) => item.trim());
  if (unresolved.length > 0)
    parts.push(
      ["Still open:", ...unresolved.map((item) => `- ${item}`)].join("\n"),
    );
  if (diagnosis.rootCause) parts.push(storyPlainText(diagnosis.rootCause));
  const receipts = (radar.receipts ?? []).filter((receipt) => receipt.title);
  if (receipts.length > 0) {
    parts.push(
      [
        "Evidence (Radar):",
        ...receipts.map((receipt) =>
          [
            `- **${receipt.title}**${receipt.role ? ` · ${receipt.role}` : ""}`,
            ...receipt.lines
              .filter((line) => line.trim())
              .map((line) => `  > ${line.trim()}`),
            ...(receipt.gap ? [`  Not shown: ${receipt.gap}`] : []),
          ].join("\n"),
        ),
      ].join("\n"),
    );
  } else if (!radar.context) {
    const story = storyPlainText(diagnosis.report ?? "");
    if (story) parts.push(story);
  }
  const steps = diagnosis.steps?.length
    ? diagnosis.steps.map(
        (step, index) =>
          `${index + 1}. [${STEP_KIND_LABEL[step.kind]}] ${step.text}${step.precondition ? ` (only if ${step.precondition})` : ""}`,
      )
    : (diagnosis.remediation ?? []).map(
        (text, index) => `${index + 1}. ${text}`,
      );
  if (steps.length > 0) {
    if (radar.context) {
      // A channel gets the recommended step; the rest wait in Radar.
      const lead = diagnosis.recommendedIndex
        ? diagnosis.recommendedIndex - 1
        : 0;
      const first = steps[lead] ?? steps[0];
      const rest = steps.length - 1;
      parts.push(
        [
          `Next step: ${first.replace(/^\d+\. /, "")}`,
          diagnosis.recommendedReason
            ? `Why: ${diagnosis.recommendedReason}`
            : undefined,
          rest > 0
            ? `${rest} more ${rest === 1 ? "step" : "steps"} in Radar.`
            : undefined,
        ]
          .filter(Boolean)
          .join("\n"),
      );
    } else parts.push(["Next steps:", ...steps].join("\n"));
  }
  if (radar.context)
    parts.push(
      "Full analysis and every captured result: open the investigation in Radar.",
    );
  return parts.join("\n\n");
}

export const CERTAINTY_LABEL: Record<
  NonNullable<Diagnosis["certainty"]>,
  string
> = {
  established: "Established",
  likely: "Likely",
  suspected: "Suspected",
};

export const STEP_KIND_LABEL: Record<
  NonNullable<Diagnosis["steps"]>[number]["kind"],
  string
> = {
  mitigate: "Mitigate",
  verify: "Verify",
  investigate: "Investigate",
};
