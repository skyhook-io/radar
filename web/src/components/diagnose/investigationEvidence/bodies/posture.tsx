import { clsx } from "clsx";
import { Badge } from "@skyhook-io/k8s-ui";
import { severityBadge, type EvidenceDataOf } from "../cardParts";
import type { InvestigationPostureFinding } from "../types";

export function PostureFindingRow({
  finding,
  divider = false,
}: {
  finding: InvestigationPostureFinding;
  divider?: boolean;
}) {
  return (
    <div
      data-posture-finding={finding.target ? "target" : undefined}
      className={clsx(
        "space-y-1 px-2.5 py-1.5 text-xs",
        divider && "border-t border-theme-border/60",
      )}
    >
      <div className="flex min-w-0 flex-wrap items-center gap-1.5">
        <Badge severity={severityBadge(finding.severity)} size="sm">
          {finding.severity}
        </Badge>
        <span className="font-medium text-theme-text-primary">
          {finding.check}
        </span>
        {finding.category ? (
          <span className="text-theme-text-tertiary">{finding.category}</span>
        ) : null}
        <span className="ml-auto min-w-0 truncate font-mono text-theme-text-tertiary">
          {finding.kind} {finding.namespace ? `${finding.namespace}/` : ""}
          {finding.name}
        </span>
      </div>
      <p className="text-theme-text-secondary [overflow-wrap:anywhere]">
        {finding.message}
      </p>
      {finding.remediation ? (
        <p className="text-theme-text-tertiary [overflow-wrap:anywhere]">
          {finding.remediation}
        </p>
      ) : null}
    </div>
  );
}

export function PostureBody({ data }: { data: EvidenceDataOf<"posture"> }) {
  return (
    <div className="max-h-80 overflow-y-auto rounded-md border border-theme-border">
      {data.findings.map((finding, index) => (
        <PostureFindingRow
          key={`${finding.kind}-${finding.namespace ?? ""}-${finding.name}-${finding.check}`}
          finding={finding}
          divider={index > 0}
        />
      ))}
    </div>
  );
}
