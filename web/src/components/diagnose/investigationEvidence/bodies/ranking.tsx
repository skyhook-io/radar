import { clsx } from "clsx";
import { Badge } from "@skyhook-io/k8s-ui";
import type { EvidenceDataOf } from "../cardParts";
import type { InvestigationRankingRow } from "../types";

export function RankingRow({
  row,
  divider = false,
}: {
  row: InvestigationRankingRow;
  divider?: boolean;
}) {
  return (
    <div
      data-ranking-row={row.target ? "target" : undefined}
      className={clsx(
        "flex min-w-0 items-center gap-2 px-2.5 py-1.5 font-mono text-xs",
        divider && "border-t border-theme-border/60",
        row.target && "bg-theme-hover/40",
      )}
    >
      <Badge tone="structural" size="sm">
        {row.kind}
      </Badge>
      <span className="min-w-0 flex-1 truncate text-theme-text-secondary">
        {row.namespace ? `${row.namespace}/` : ""}
        <span className={row.target ? "text-theme-text-primary" : undefined}>
          {row.name}
        </span>
      </span>
      <span className="shrink-0 tabular-nums text-theme-text-primary">
        {row.cpu}
        {row.cpuLimit ? (
          <span className="text-theme-text-tertiary">/{row.cpuLimit}</span>
        ) : null}
      </span>
      <span className="shrink-0 tabular-nums text-theme-text-primary">
        {row.memory}
        {row.memoryLimit ? (
          <span className="text-theme-text-tertiary">/{row.memoryLimit}</span>
        ) : null}
      </span>
      {row.restarts ? (
        <Badge severity="warning" size="sm">
          {row.restarts} restarts
        </Badge>
      ) : null}
      {row.target ? (
        <Badge tone="note" size="sm">
          this workload
        </Badge>
      ) : null}
    </div>
  );
}

export function RankingBody({ data }: { data: EvidenceDataOf<"ranking"> }) {
  return (
    <div className="space-y-1.5">
      <div className="flex gap-2 px-2.5 text-[11px] text-theme-text-tertiary">
        <span className="min-w-0 flex-1">ranked by {data.sort}</span>
        <span className="shrink-0">cpu</span>
        <span className="shrink-0">memory</span>
      </div>
      <div className="max-h-72 overflow-y-auto rounded-md border border-theme-border">
        {data.rows.map((row, index) => (
          <RankingRow
            key={`${row.kind}-${row.namespace ?? ""}-${row.name}`}
            row={row}
            divider={index > 0}
          />
        ))}
      </div>
    </div>
  );
}
