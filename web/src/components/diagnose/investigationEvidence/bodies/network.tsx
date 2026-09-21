import { useContext } from "react";
import { AlertTriangle } from "lucide-react";
import { Badge, StatusDot, ResourceLink } from "@skyhook-io/k8s-ui";
import { EvidenceNavigationContext } from "../navigation";
import { type EvidenceDataOf, severityBadge } from "../cardParts";

export function DNSBody({ data }: { data: EvidenceDataOf<"dns"> }) {
  const { onOpenResource } = useContext(EvidenceNavigationContext);
  return (
    <div className="space-y-2">
      {(data.dns.signals ?? []).map((signal) => (
        <p
          key={signal}
          className="text-xs leading-relaxed text-theme-text-secondary"
        >
          {signal}
        </p>
      ))}
      {(data.dns.coreDNSFindings ?? []).map((finding) => (
        <div
          key={`${finding.kind}-${finding.namespace}-${finding.name}-${finding.reason}`}
          className="rounded-md border border-theme-border bg-theme-base/40 p-2"
        >
          <div className="flex flex-wrap gap-1.5">
            <Badge tone="structural" size="sm">
              {finding.kind}
            </Badge>
            <span className="font-mono text-xs">
              <ResourceLink
                name={finding.name}
                kind={finding.kind}
                namespace={finding.namespace}
                group=""
                label={`${finding.namespace}/${finding.name}`}
                onNavigate={
                  finding.kind.toLowerCase() === "configmap" && onOpenResource
                    ? (ref) => onOpenResource(ref)
                    : undefined
                }
              />
            </span>
            <Badge severity={severityBadge(finding.severity)} size="sm">
              {finding.severity}
            </Badge>
          </div>
          <p className="mt-1 text-xs text-theme-text-secondary">
            {finding.reason}
            {finding.message ? ` — ${finding.message}` : ""}
          </p>
        </div>
      ))}
    </div>
  );
}

export function NetworkBody({ data }: { data: EvidenceDataOf<"network"> }) {
  const { network } = data;
  const stats = [
    ["Tested", network.summary.tested],
    ["Passed", network.summary.passed],
    ["Failed", network.summary.failed],
    ["Inferred", network.summary.derived ?? 0],
    ["Skipped", network.summary.skipped],
  ] as const;
  return (
    <div className="space-y-3">
      <div className="grid grid-cols-3 gap-1.5 @min-[620px]/evidence:grid-cols-5">
        {stats.map(([label, value]) => (
          <div
            key={label}
            className="rounded-md border border-theme-border bg-theme-base/40 px-2 py-1.5 text-center"
          >
            <div className="font-mono text-sm font-semibold text-theme-text-primary">
              {value}
            </div>
            <div className="text-xs uppercase tracking-wide text-theme-text-tertiary">
              {label}
            </div>
          </div>
        ))}
      </div>
      {network.diagnosis ? (
        <div className="rounded-md border border-theme-border bg-theme-base/40 px-2.5 py-2">
          <p className="text-xs font-medium leading-relaxed text-theme-text-primary">
            {network.diagnosis.summary}
          </p>
          {network.diagnosis.nextAction ? (
            <p className="mt-1 border-l-2 border-accent/50 pl-2 text-xs leading-relaxed text-theme-text-secondary">
              Next check: {network.diagnosis.nextAction}
            </p>
          ) : null}
        </div>
      ) : (
        <p className="text-xs leading-relaxed text-theme-text-secondary">
          {network.summary.headline}
        </p>
      )}
      {network.routes.length > 0 ? (
        <ol className="max-h-64 space-y-1.5 overflow-y-auto pr-1">
          {network.routes.map((route, index) => (
            <li
              key={`${route.route}-${route.target ?? ""}-${index}`}
              className="flex min-w-0 items-start gap-2 rounded-md border border-theme-border/70 bg-theme-base/30 px-2.5 py-2"
            >
              <StatusDot
                tone={networkOutcomeTone(route.outcome, route.benign)}
                className="mt-1 shrink-0"
              />
              <span className="min-w-0 flex-1">
                <span className="block truncate font-mono text-xs text-theme-text-primary">
                  {route.route}
                  {route.target ? ` → ${route.target}` : ""}
                </span>
                {route.evidence ? (
                  <span className="mt-0.5 block text-xs leading-relaxed text-theme-text-secondary">
                    {route.evidence}
                  </span>
                ) : null}
              </span>
              <Badge
                severity={networkOutcomeSeverity(route.outcome, route.benign)}
                size="sm"
              >
                {route.benign
                  ? "intentional"
                  : route.outcome.replaceAll("_", " ")}
              </Badge>
            </li>
          ))}
        </ol>
      ) : null}
    </div>
  );
}

function networkOutcomeTone(
  outcome: string,
  benign?: boolean,
): "healthy" | "degraded" | "unhealthy" | "unknown" {
  if (benign) return "degraded";
  const normalized = outcome.toLowerCase();
  if (normalized.includes("verified") || normalized.includes("reached"))
    return "healthy";
  if (normalized.includes("fail") || normalized.includes("unreachable"))
    return "unhealthy";
  if (normalized.includes("skip") || normalized.includes("not"))
    return "unknown";
  return "degraded";
}

function networkOutcomeSeverity(outcome: string, benign?: boolean) {
  switch (networkOutcomeTone(outcome, benign)) {
    case "healthy":
      return "success" as const;
    case "degraded":
      return "warning" as const;
    case "unhealthy":
      return "error" as const;
    case "unknown":
      return "neutral" as const;
  }
}

export function RelationshipsBody({
  data,
}: {
  data: EvidenceDataOf<"relationships">;
}) {
  return (
    <div className="space-y-2.5">
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge tone="structural" size="sm">
          {data.root.kind}
        </Badge>
        <span className="font-mono text-xs text-theme-text-secondary">
          {data.root.namespace ? `${data.root.namespace}/` : ""}
          {data.root.name}
        </span>
        <span className="text-xs text-theme-text-tertiary">
          {data.nodes.length} resources · {data.edges.length} direct
          relationships
        </span>
      </div>
      <div className="max-h-44 overflow-y-auto pr-1">
        <div className="flex flex-wrap gap-1.5">
          {data.nodes.map((node) => (
            <span
              key={node.id}
              className="inline-flex items-center gap-1 rounded-md border border-theme-border bg-theme-base px-2 py-1 text-xs"
            >
              <Badge tone="structural" size="sm">
                {node.kind}
              </Badge>
              <span className="font-mono text-theme-text-secondary">
                {node.name}
              </span>
            </span>
          ))}
        </div>
      </div>
      {data.edges.length > 0 ? (
        <div className="grid max-h-52 gap-1 overflow-y-auto pr-1 @min-[680px]/evidence:grid-cols-2">
          {data.edges.map((edge, index) => (
            <div
              key={
                edge.id ?? `${edge.source}-${edge.target}-${edge.type}-${index}`
              }
              className="flex min-w-0 items-center gap-1.5 rounded bg-theme-base/50 px-2 py-1.5 font-mono text-xs text-theme-text-secondary"
            >
              <span className="truncate">{edge.source}</span>
              <span className="shrink-0 text-theme-text-tertiary">→</span>
              <span className="truncate">{edge.target}</span>
              <Badge tone="structural" size="sm">
                {edge.label || edge.type}
              </Badge>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

export function TopologyBody({ data }: { data: EvidenceDataOf<"topology"> }) {
  return (
    <div className="space-y-2.5">
      <div className="grid grid-cols-2 gap-2">
        <TopologyStat label="Nodes" value={data.stats.nodes} />
        <TopologyStat label="Relationships" value={data.stats.edges} />
      </div>
      {data.problems.map((problem) => (
        <p
          key={problem}
          className="flex items-start gap-1.5 text-xs text-warning-text"
        >
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          {problem}
        </p>
      ))}
      <div className="max-h-64 space-y-2 overflow-y-auto pr-1">
        {data.namespaces.map((namespace) => (
          <div key={namespace.namespace}>
            <div className="text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
              {namespace.namespace || "cluster-scoped"}
            </div>
            <ul className="mt-1 space-y-1 font-mono text-xs text-theme-text-secondary">
              {namespace.chains.map((chain) => (
                <li key={chain}>{chain}</li>
              ))}
            </ul>
          </div>
        ))}
      </div>
      {data.warnings.map((warning) => (
        <p key={warning} className="text-xs text-warning-text">
          {warning}
        </p>
      ))}
    </div>
  );
}

function TopologyStat({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-md border border-theme-border bg-theme-base/40 px-3 py-2">
      <div className="font-mono text-lg font-semibold text-theme-text-primary">
        {value}
      </div>
      <div className="text-xs uppercase tracking-wide text-theme-text-tertiary">
        {label}
      </div>
    </div>
  );
}
