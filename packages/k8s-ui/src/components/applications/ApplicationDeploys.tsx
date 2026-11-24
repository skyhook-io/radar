import { ChevronRight } from "lucide-react";
import { type AppHistoryAnchor, type AppSourceRef, CHIP, CHIP_TONE } from "../../utils/applications";
import { AlertBanner } from "../ui/drawer-components";

export interface ApplicationDeploysProps {
  records: AppHistoryAnchor[];
  source: AppSourceRef;
  onOpenSource?: (source: AppSourceRef) => void;
  unavailable?: boolean;
  loading?: boolean;
  partialSources?: string[];
}

export function DeliveryRecord({ record, source, onOpenSource }: {
  record: AppHistoryAnchor;
  source: AppSourceRef;
  onOpenSource?: (source: AppSourceRef) => void;
}) {
  const revisionInTitle = record.type === "helm" && record.title === `Helm revision ${record.revision}`;
  return (
    <details className="group border-b border-theme-border last:border-0">
      <summary className="flex cursor-pointer list-none flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 text-sm hover:bg-theme-hover">
        <ChevronRight aria-hidden className="h-4 w-4 shrink-0 text-theme-text-tertiary group-open:rotate-90" />
        <span className="font-medium text-theme-text-primary">{record.title}</span>
        {!revisionInTitle && <span className="min-w-0 truncate font-mono text-xs text-theme-text-secondary">{record.revision}</span>}
        {record.status && <span className={`${CHIP} ${historyStatusTone(record.status)}`}>{record.status}</span>}
        {record.timestamp && <time dateTime={record.timestamp} className="ml-auto text-xs text-theme-text-tertiary">{new Date(record.timestamp).toLocaleString()}</time>}
      </summary>
      <div className="space-y-2 bg-theme-elevated px-5 py-3 text-sm text-theme-text-secondary">
        <p className="break-words">{sourceObjectLabel(source)} · {[source.namespace, source.name].filter(Boolean).join("/")}</p>
        {record.revision && <p className="break-all font-mono text-xs">Revision {record.revision}</p>}
        {record.source && <p className="break-words">{record.source}</p>}
        {record.message && <p className="break-words">{record.message}</p>}
        {record.initiatedBy && <p>Initiated by {record.initiatedBy}</p>}
        {onOpenSource && <button type="button" className="font-medium text-accent-text hover:underline" onClick={() => onOpenSource(source)}>View {sourceObjectLabel(source)}</button>}
      </div>
    </details>
  );
}

export function ApplicationDeploys({ records, source, partialSources, unavailable, loading, onOpenSource }: ApplicationDeploysProps) {
  return (
    <div className="min-h-0 flex-1 overflow-y-auto p-4 sm:p-6">
      <section className="overflow-hidden rounded-lg border border-theme-border bg-theme-surface">
        <header className="border-b border-theme-border px-4 py-3">
          <h2 className="text-sm font-semibold text-theme-text-primary">{sourceObjectLabel(source)} · {source.name}</h2>
          <p className="mt-1 text-xs text-theme-text-secondary">{source.namespace && `${source.namespace} · `}Recorded deliveries</p>
        </header>
        {unavailable ? <AlertBanner variant="warning" title="Delivery records could not be refreshed" /> : (
          <>
            {!!partialSources?.length && <AlertBanner variant="warning" title="Some delivery records are unavailable" items={partialSources} />}
            {loading && <p role="status" className="p-4 text-sm text-theme-text-secondary">Loading delivery records…</p>}
            {!loading && !records.length && !partialSources?.length && <p className="p-4 text-sm text-theme-text-secondary">This source has no recorded deliveries.</p>}
            {records.map(record => <DeliveryRecord key={JSON.stringify([record.type, record.title, record.timestamp, record.revision])} record={record} source={source} onOpenSource={onOpenSource} />)}
          </>
        )}
      </section>
    </div>
  );
}

export function historyStatusTone(status: string): string {
  const s = status.toLowerCase();
  if (s.includes("fail") || s.includes("error")) return CHIP_TONE.rose;
  if (s.includes("running") || s.includes("pending") || s.includes("progress"))
    return CHIP_TONE.amber;
  if (s.includes("succeed") || s.includes("deployed") || s.includes("ready"))
    return CHIP_TONE.emerald;
  return CHIP_TONE.muted;
}

export function sourceObjectLabel(source: AppSourceRef): string {
  if (source.type === "helm") return "Helm release";
  if (source.tool === "argocd" || source.kind.toLowerCase() === "application")
    return "Argo CD application";
  if (source.kind.toLowerCase() === "helmrelease") return "Flux HelmRelease";
  if (source.kind.toLowerCase() === "kustomization")
    return "Flux Kustomization";
  return "GitOps source";
}
