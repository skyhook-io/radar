import { clsx } from "clsx";
import { Sparkles } from "lucide-react";
import { Badge } from "@skyhook-io/k8s-ui";

import type { DiagnosisEvidenceRole } from "../../api/diagnose";
import { Tooltip } from "../ui/Tooltip";

export const AGENT_ROLE_LABELS: Readonly<
  Record<DiagnosisEvidenceRole, string>
> = {
  cause: "Cause",
  symptom: "Symptom",
  context: "Context",
  benign: "Not a problem",
  demoted: "Less relevant",
  rules_out: "Rules out",
};

/**
 * The agent's framing of a Radar fact. Always the agent tone, never a
 * severity tone: a chip must not read as a Radar finding.
 */
export function AgentRoleChip({ role }: { role: DiagnosisEvidenceRole }) {
  return (
    <Tooltip
      content={`The agent labelled this evidence "${AGENT_ROLE_LABELS[role]}". Radar recorded the fact; the label is the agent's reading of it.`}
      // The label has to share the sentence's baseline. Centring the badge box
      // drops it ~1px (padding and border make the box taller than the line),
      // and the badge's own baseline comes from the sparkle, landing 1px high.
      wrapperClassName="shrink-0 align-[-1px]"
    >
      <Badge tone="agent" size="sm">
        <Sparkles className="h-2.5 w-2.5 shrink-0" aria-hidden />
        {AGENT_ROLE_LABELS[role]}
      </Badge>
    </Tooltip>
  );
}

/**
 * One sentence from the agent about a Radar fact. Everything the agent
 * contributed to a card lives in this one row — the sparkle, the role, and the
 * sentence — so the register is unmistakable: the fact above is Radar's, this
 * line is the agent's reading of it. The role travelled with the card title
 * once, where it sat in the same slot as Radar's own badges and could be read
 * as Radar having established it.
 *
 * A `rules_out` note carries the hypothesis it excludes, as text beside the
 * chip rather than inside it: "Rules out" alone forces the reader to find the
 * hypothesis in a block further down and match it back, and a hypothesis long
 * enough to be useful makes a badge that swallows the row.
 */
export function AgentClaimNote({
  claim,
  role,
  excludes,
  subject,
  className,
}: {
  claim: string;
  role?: DiagnosisEvidenceRole;
  /**
   * The hypothesis this item excludes. Rendered only for `rules_out`, where
   * the chip frames it as rejected: a hypothesis is a claim the agent
   * DISPROVED, so on any other card it would read as a statement of fact.
   */
  excludes?: string;
  /** The observation the note was bound to, when it is not on that card. */
  subject?: string;
  className?: string;
}) {
  if (!claim && !role) return null;
  return (
    <p
      data-agent-claim
      className={clsx(
        "flex min-w-0 items-start gap-2 border-t border-dashed border-theme-border/70 text-xs leading-relaxed text-theme-text-secondary",
        className,
      )}
    >
      <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">
        {/* The chip and its sparkle carry this visually; the word is what
            survives when colour and iconography do not. "Agent" rather than
            "Agent's note" because a role can arrive with no sentence. */}
        <span className="sr-only">Agent: </span>
        {role ? (
          <>
            <AgentRoleChip role={role} />{" "}
          </>
        ) : null}
        {excludes && role === "rules_out" ? (
          <>
            <span className="italic text-theme-text-tertiary">{excludes}</span>
            {" — "}
          </>
        ) : null}
        {claim ? (
          <>
            {subject ? (
              <span className="text-theme-text-tertiary">{subject} · </span>
            ) : null}
            {renderClaim(claim)}
          </>
        ) : null}
      </span>
    </p>
  );
}

/**
 * Claims are one sentence of prose in which the agent marks identifiers with
 * backticks. Only that inline-code span is honoured: a full Markdown render
 * would wrap the sentence in a block and break the shared baseline with the
 * role chip and the lead-in.
 */
function renderClaim(claim: string) {
  const parts = claim.split("`");
  if (parts.length < 3) return claim;
  return parts.map((part, i) =>
    i % 2 === 1 && i < parts.length - 1 ? (
      <code
        key={i}
        className="inline-code rounded border border-theme-border/60 bg-theme-base px-1 py-px font-mono text-[11px] font-normal text-theme-text-primary"
      >
        {part}
      </code>
    ) : (
      part
    ),
  );
}
