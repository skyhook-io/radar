import { AlertTriangle, CircleAlert, Clock3, GitBranch, GitCommit, ListChecks, RotateCw } from 'lucide-react'
import { clsx } from 'clsx'
import type { ReactNode } from 'react'
import type { GitOpsChange, GitOpsHistoryItem, GitOpsInsight, GitOpsIssue, GitOpsPlanItem } from '../../../types'
import { HealthStatusBadge, SyncStatusBadge } from '../GitOpsStatusBadge'

interface GitOpsStatusStripProps {
  insight?: GitOpsInsight | null
  loading?: boolean
}

export function GitOpsStatusStrip({ insight, loading }: GitOpsStatusStripProps) {
  const summary = insight?.summary
  if (loading) {
    return <div className="h-16 animate-pulse border-b border-theme-border bg-theme-surface/70" />
  }
  if (!summary) return null
  return (
    <div className="border-b border-theme-border bg-theme-surface/70 px-4 py-3">
      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-6">
        <StatusFact label="Health" value={summary.health || 'Unknown'} badge={<HealthStatusBadge health={(summary.health || 'Unknown') as any} />} />
        <StatusFact label="Sync" value={summary.sync || 'Unknown'} badge={<SyncStatusBadge sync={(summary.sync || 'Unknown') as any} />} />
        <StatusFact label="Operation" value={summary.operationPhase || 'Idle'} />
        <StatusFact label="Source" value={summary.source || '-'} />
        <StatusFact label="Revision" value={summary.lastRevision || summary.targetRevision || '-'} mono />
        <StatusFact label="Last reconcile" value={formatRelative(summary.lastReconcile)} />
      </div>
    </div>
  )
}

function StatusFact({ label, value, badge, mono = false }: { label: string; value: string; badge?: ReactNode; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <div className="text-[10px] font-medium uppercase tracking-wide text-theme-text-tertiary">{label}</div>
      <div className={clsx('mt-1 truncate text-sm text-theme-text-primary', mono && 'font-mono text-xs')}>
        {badge ?? value}
      </div>
    </div>
  )
}

export function GitOpsIssuesBand({ issues }: { issues?: GitOpsIssue[] | null }) {
  const list = issues ?? []
  if (list.length === 0) return null
  const top = list.slice(0, 3)
  return (
    <div className="border-b border-theme-border bg-theme-base px-4 py-3">
      <div className="flex flex-wrap items-start gap-2">
        {top.map((issue: GitOpsIssue, index: number) => (
          <div key={`${issue.reason}-${index}`} className={clsx(
            'min-w-[260px] flex-1 rounded-md border px-3 py-2',
            issue.severity === 'critical'
              ? 'border-red-500/30 bg-red-500/10'
              : issue.severity === 'warning'
                ? 'border-amber-500/30 bg-amber-500/10'
                : 'border-sky-500/30 bg-sky-500/10'
          )}>
            <div className="flex items-center gap-2">
              {issue.severity === 'critical' ? <CircleAlert className="h-3.5 w-3.5 text-red-500" /> : <AlertTriangle className="h-3.5 w-3.5 text-amber-500" />}
              <span className="truncate text-xs font-semibold text-theme-text-primary">{issue.reason}</span>
              <span className="ml-auto text-[10px] uppercase tracking-wide text-theme-text-tertiary">{issue.scope}</span>
            </div>
            <p className="mt-1 line-clamp-2 text-xs text-theme-text-secondary">{issue.message}</p>
            {issue.action && <p className="mt-1 text-[11px] text-theme-text-tertiary">{issue.action}</p>}
          </div>
        ))}
      </div>
    </div>
  )
}

interface GitOpsChangesViewProps {
  insight?: GitOpsInsight | null
  onOpenResource?: (ref: GitOpsChange['ref']) => void
}

export function GitOpsChangesView({ insight, onOpenResource }: GitOpsChangesViewProps) {
  const changes = insight?.changes ?? []
  const plan = insight?.plan ?? []
  if (!insight) {
    return <CenteredText>Loading GitOps changes...</CenteredText>
  }
  return (
    <div className="h-full overflow-auto bg-theme-base p-4">
      <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_420px]">
        <section className="rounded-md border border-theme-border bg-theme-surface">
          <SectionHeader icon={GitCommit} title="Changes" subtitle={insight.summary.partialReason} />
          {changes.length === 0 ? (
            <div className="p-4 text-sm text-theme-text-secondary">No changed resources reported by the GitOps controller.</div>
          ) : (
            <div className="divide-y divide-theme-border">
              {changes.map((change) => (
                <button
                  key={`${change.ref.group}/${change.ref.kind}/${change.ref.namespace}/${change.ref.name}`}
                  type="button"
                  onClick={() => onOpenResource?.(change.ref)}
                  className="grid w-full grid-cols-[minmax(0,1fr)_120px_120px] gap-3 px-4 py-3 text-left text-sm hover:bg-theme-hover"
                >
                  <div className="min-w-0">
                    <div className="truncate font-medium text-theme-text-primary">{change.ref.kind} / {change.ref.name}</div>
                    <div className="truncate text-xs text-theme-text-tertiary">{change.ref.namespace || '(cluster)'} {change.ref.group ? `· ${change.ref.group}` : ''}</div>
                    {change.message && <div className="mt-1 line-clamp-2 text-xs text-theme-text-secondary">{change.message}</div>}
                    {change.partial && <div className="mt-1 text-[11px] text-theme-text-tertiary">{change.partialNote}</div>}
                  </div>
                  <div><SyncStatusBadge sync={(change.sync || change.category || 'Unknown') as any} /></div>
                  <div><HealthStatusBadge health={(change.health || 'Unknown') as any} /></div>
                </button>
              ))}
            </div>
          )}
        </section>
        <GitOpsPlanPanel plan={plan} tool={insight.summary.tool} />
      </div>
    </div>
  )
}

function GitOpsPlanPanel({ plan, tool }: { plan?: GitOpsPlanItem[] | null; tool?: string }) {
  const items = plan ?? []
  return (
    <section className="rounded-md border border-theme-border bg-theme-surface">
      <SectionHeader icon={ListChecks} title="Sync Plan" subtitle={tool === 'argocd' ? 'Argo order uses phase, wave, kind, then name.' : 'Flux order follows source and dependency relationships.'} />
      <div className="max-h-[640px] overflow-auto">
        {items.length === 0 ? (
          <div className="p-4 text-sm text-theme-text-secondary">No plan data available.</div>
        ) : (
          <div className="divide-y divide-theme-border">
            {items.map((item) => (
              <div key={`${item.order}-${item.ref.kind}-${item.ref.name}`} className="grid grid-cols-[40px_minmax(0,1fr)] gap-2 px-4 py-3 text-sm">
                <div className="text-right font-mono text-xs text-theme-text-tertiary">#{item.order}</div>
                <div className="min-w-0">
                  <div className="truncate font-medium text-theme-text-primary">{item.ref.kind} / {item.ref.name}</div>
                  <div className="mt-1 flex flex-wrap gap-1">
                    {item.phase && <Chip label="phase" value={item.phase} />}
                    {item.waveSet && <Chip label="wave" value={String(item.wave)} />}
                    {item.hook && <Chip label="hook" value={item.hook} />}
                    {item.relationship && <Chip value={item.relationship} />}
                    {item.status && <Chip value={item.status} />}
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  )
}

export function GitOpsActivityInsightView({ insight }: { insight?: GitOpsInsight | null }) {
  if (!insight) return <CenteredText>Loading GitOps activity...</CenteredText>
  const operation = insight.history?.find((item) => item.phase && item.message)
  return (
    <div className="h-full overflow-auto bg-theme-base p-4">
      <div className="grid gap-4 xl:grid-cols-[420px_minmax(0,1fr)]">
        <section className="rounded-md border border-theme-border bg-theme-surface">
          <SectionHeader icon={RotateCw} title="Current Operation" />
          <div className="space-y-2 p-4 text-sm">
            <InfoRow label="Phase" value={operation?.phase || insight.summary.operationPhase || 'Idle'} />
            <InfoRow label="Revision" value={operation?.revision || insight.summary.lastRevision || insight.summary.targetRevision || '-'} mono />
            <InfoRow label="Message" value={operation?.message || '-'} />
            <InfoRow label="Last reconcile" value={formatRelative(insight.summary.lastReconcile)} />
          </div>
        </section>
        <section className="rounded-md border border-theme-border bg-theme-surface">
          <SectionHeader icon={Clock3} title="History" />
          <HistoryRows items={insight.history ?? []} rollback={insight.capabilities?.rollback ?? false} />
        </section>
      </div>
      <section className="mt-4 rounded-md border border-theme-border bg-theme-surface">
        <SectionHeader icon={AlertTriangle} title="Diagnosis" />
        {(insight.issues ?? []).length === 0 ? (
          <div className="p-4 text-sm text-theme-text-secondary">No active GitOps issues detected.</div>
        ) : (
          <div className="divide-y divide-theme-border">
            {(insight.issues ?? []).map((issue, index) => (
              <div key={`${issue.reason}-${index}`} className="px-4 py-3 text-sm">
                <div className="flex flex-wrap items-center gap-2">
                  <span className={clsx('h-2 w-2 rounded-full', issue.severity === 'critical' ? 'bg-red-500' : issue.severity === 'warning' ? 'bg-amber-500' : 'bg-sky-500')} />
                  <span className="font-medium text-theme-text-primary">{issue.reason}</span>
                  <span className="badge status-neutral">{issue.scope}</span>
                </div>
                <p className="mt-1 text-theme-text-secondary">{issue.message}</p>
                {issue.action && <p className="mt-1 text-xs text-theme-text-tertiary">{issue.action}</p>}
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  )
}

function HistoryRows({ items, rollback }: { items: GitOpsHistoryItem[]; rollback: boolean }) {
  if (items.length === 0) return <div className="p-4 text-sm text-theme-text-secondary">No history reported.</div>
  return (
    <div className="divide-y divide-theme-border">
      {items.map((item, index) => (
        <div key={`${item.id}-${item.revision}-${index}`} className="grid grid-cols-[minmax(0,1fr)_110px] gap-3 px-4 py-3 text-sm">
          <div className="min-w-0">
            <div className="truncate font-mono text-xs text-theme-text-primary">{item.revision || item.phase || '-'}</div>
            <div className="mt-1 truncate text-xs text-theme-text-tertiary">{item.source || item.message || 'GitOps history entry'}</div>
          </div>
          <div className="text-right">
            <div className="text-xs text-theme-text-secondary">{formatRelative(item.deployedAt)}</div>
            {rollback && item.revision && <div className="mt-1 text-[11px] text-theme-text-tertiary">Rollback capable</div>}
          </div>
        </div>
      ))}
    </div>
  )
}

function SectionHeader({ icon: Icon, title, subtitle }: { icon: typeof GitBranch; title: string; subtitle?: string }) {
  return (
    <div className="border-b border-theme-border px-4 py-3">
      <div className="flex items-center gap-2">
        <Icon className="h-4 w-4 text-theme-text-tertiary" />
        <h2 className="text-sm font-semibold text-theme-text-primary">{title}</h2>
      </div>
      {subtitle && <p className="mt-1 text-xs text-theme-text-tertiary">{subtitle}</p>}
    </div>
  )
}

function InfoRow({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid grid-cols-[120px_minmax(0,1fr)] gap-3">
      <span className="text-theme-text-tertiary">{label}</span>
      <span className={clsx('min-w-0 truncate text-theme-text-primary', mono && 'font-mono text-xs')}>{value}</span>
    </div>
  )
}

function Chip({ label, value }: { label?: string; value: string }) {
  return <span className="rounded border border-theme-border bg-theme-elevated px-1.5 py-0.5 text-[10px] text-theme-text-secondary">{label ? `${label}: ` : ''}{value}</span>
}

function CenteredText({ children }: { children: ReactNode }) {
  return <div className="flex h-full items-center justify-center text-sm text-theme-text-secondary">{children}</div>
}

function formatRelative(value?: string) {
  if (!value) return '-'
  const time = new Date(value).getTime()
  if (!Number.isFinite(time)) return value
  const seconds = Math.max(0, Math.floor((Date.now() - time) / 1000))
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return `${days}d ago`
}
