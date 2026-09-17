import { clsx } from "clsx";
import { Badge, StatusDot, mapHealthToTone } from "@skyhook-io/k8s-ui";
import type { DiagnosisEvidenceSubject } from "../../../../api/diagnose";
import {
  investigationSourceArgs,
  type InvestigationResourceSummary,
  type InvestigationEvidenceSource,
} from "..";
import { sameKind } from "../../investigationCase";
import type { EvidenceDataOf } from "../cardParts";

// The rows the citations on a card name, in citation order: one row when the
// name picks exactly one entry, otherwise a line saying the entry is absent
// or which entries the name could mean.
export function listingScopeNamespace(
  source: InvestigationEvidenceSource,
): string | undefined {
  const namespace = investigationSourceArgs(source)?.namespace;
  return typeof namespace === "string" && namespace ? namespace : undefined;
}

export function namedInventoryRows(
  resources: InvestigationResourceSummary[],
  items: readonly { subject?: DiagnosisEvidenceSubject }[],
  /** The namespace the listing call was scoped to; rows omit it then. */
  scopeNamespace?: string,
): {
  key: string;
  label: string;
  matches: number;
  row?: InvestigationResourceSummary;
}[] {
  const seen = new Set<string>();
  const out: {
    key: string;
    label: string;
    matches: number;
    row?: InvestigationResourceSummary;
  }[] = [];
  for (const item of items) {
    const subject = item.subject;
    if (!subject?.name) continue;
    // Agents write namespace "" for a cluster-scoped kind; that states none.
    const namespace = subject.namespace || undefined;
    const key = `${namespace ?? ""}/${subject.name}`;
    if (seen.has(key)) continue;
    seen.add(key);
    // A row without a namespace lives in the listing's scope, or in none
    // (a cluster-scoped kind); neither satisfies a namespace the citation
    // states unless it is the scope itself.
    const matches = resources.filter(
      (resource) =>
        resource.name === subject.name &&
        sameKind(resource.kind, subject.kind) &&
        (namespace === undefined ||
          (resource.namespace ?? scopeNamespace) === namespace),
    );
    out.push({
      key,
      label: namespace ? `${namespace}/${subject.name}` : subject.name,
      matches: matches.length,
      row: matches.length === 1 ? matches[0] : undefined,
    });
  }
  return out;
}

export function InventoryRow({
  resource,
  divider = false,
}: {
  resource: InvestigationResourceSummary;
  divider?: boolean;
}) {
  return (
    <div
      className={clsx(
        "flex min-w-0 items-center gap-2 px-2.5 py-1.5",
        divider && "border-t border-theme-border/60",
      )}
    >
      <StatusDot
        tone={mapHealthToTone(resource.summaryContext?.health ?? "")}
        className="shrink-0"
      />
      <Badge tone="structural" size="sm">
        {resource.kind}
      </Badge>
      <span className="min-w-0 flex-1">
        <span className="block truncate font-mono text-xs text-theme-text-secondary">
          {resource.namespace ? `${resource.namespace}/` : ""}
          {resource.name}
        </span>
        {resource.issue ? (
          <span className="block truncate text-xs text-warning-text">
            {resource.issue}
          </span>
        ) : null}
      </span>
      {resource.ready || resource.status ? (
        <span className="ml-auto shrink-0 font-mono text-xs text-theme-text-tertiary">
          {resource.ready || resource.status}
        </span>
      ) : null}
      {(resource.summaryContext?.issueCount ?? 0) > 0 ? (
        <Badge severity="warning" size="sm">
          {resource.summaryContext?.issueCount} issues
        </Badge>
      ) : null}
    </div>
  );
}

export function InventoryBody({ data }: { data: EvidenceDataOf<"inventory"> }) {
  return (
    <div className="max-h-72 overflow-y-auto rounded-md border border-theme-border">
      {data.resources.map((resource, index) => (
        <InventoryRow
          key={`${resource.kind}-${resource.namespace ?? ""}-${resource.name}`}
          resource={resource}
          divider={index > 0}
        />
      ))}
    </div>
  );
}
