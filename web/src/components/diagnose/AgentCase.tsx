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
      wrapperClassName="shrink-0"
    >
      <Badge tone="agent" size="sm">
        {AGENT_ROLE_LABELS[role]}
      </Badge>
    </Tooltip>
  );
}

/**
 * One sentence from the agent about a Radar fact. The dashed rule, the sparkle
 * and the "Agent's note:" lead-in keep the register distinct from the card's
 * own content: the fact above is Radar's, the sentence is the agent's reading.
 */
export function AgentClaimNote({
  claim,
  role,
  className,
}: {
  claim: string;
  role?: DiagnosisEvidenceRole;
  className?: string;
}) {
  if (!claim) return null;
  return (
    <p
      data-agent-claim
      className={clsx(
        "flex min-w-0 items-start gap-2 border-t border-dashed border-theme-border/70 text-xs leading-relaxed text-theme-text-secondary",
        className,
      )}
    >
      <Sparkles className="mt-0.5 h-3 w-3 shrink-0 text-accent" aria-hidden />
      {role ? <AgentRoleChip role={role} /> : null}
      <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">
        <span className="font-semibold text-accent-text">Agent's note:</span>{" "}
        {claim}
      </span>
    </p>
  );
}
