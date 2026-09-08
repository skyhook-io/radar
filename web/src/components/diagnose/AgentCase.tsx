import { clsx } from "clsx";
import { Badge } from "@skyhook-io/k8s-ui";

import type { DiagnosisEvidenceRole } from "../../api/diagnose";

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
    <Badge
      tone="agent"
      size="sm"
      className="shrink-0"
      title={`The agent labelled this evidence "${AGENT_ROLE_LABELS[role]}"`}
    >
      {AGENT_ROLE_LABELS[role]}
    </Badge>
  );
}

/**
 * One sentence from the agent about a Radar fact. The dashed rule and the
 * "Agent" label keep the register distinct from the card's own content.
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
      <span className="mt-px shrink-0 text-[10px] font-semibold uppercase tracking-wide text-accent-text">
        Agent
      </span>
      {role ? <AgentRoleChip role={role} /> : null}
      <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">{claim}</span>
    </p>
  );
}
