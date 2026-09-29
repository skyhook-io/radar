import { useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { Lock } from 'lucide-react'
import { PaneLoader, StatusDot, formatAge, toneFillClass, toneTextClass } from '@skyhook-io/k8s-ui'
import { useCNPGClusterCapabilities, useCNPGRuntime, type CNPGRuntimeInstance } from '../../api/cnpg'
import { Notice } from '../capacity/shared'
import { Segments } from './shared'
import { CNPGInstanceActions } from './actions/CNPGInstanceActions'
import { CNPGStorage } from './CNPGStorage'
import { CNPGTrends, useSampleBuffer, type CNPGIntervalTarget, type Sample } from './CNPGTrends'

type Section = 'replication' | 'sessions' | 'transactions' | 'storage' | 'slots' | 'trends'

const SECTIONS: { id: Section; label: string }[] = [
  { id: 'replication', label: 'Replication' },
  { id: 'sessions', label: 'Sessions' },
  { id: 'transactions', label: 'Transactions' },
  { id: 'storage', label: 'Storage & WAL' },
  { id: 'slots', label: 'Slots' },
  { id: 'trends', label: 'Trends' },
]

function bytes(n?: number): string {
  if (n === undefined) return '—'
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let v = n
  let i = 0
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${u[i]}`
}

function seconds(s?: number): string {
  if (s === undefined) return '—'
  if (s < 1) return `${(s * 1000).toFixed(0)} ms`
  if (s < 90) return `${s.toFixed(1)} s`
  if (s < 5400) return `${Math.round(s / 60)} min`
  return `${(s / 3600).toFixed(1)} h`
}

function lagTone(s?: number) {
  if (s === undefined) return 'unknown' as const
  if (s >= 30) return 'unhealthy' as const
  if (s >= 5) return 'degraded' as const
  return 'healthy' as const
}

function SourceState({ label, state, error }: { label: string; state: string; error?: string }) {
  if (state === 'ok') return null
  const text =
    state === 'denied'
      ? `${label}: no access (needs get pods/proxy)`
      : state === 'partial'
        ? `${label}: partial${error ? ` · ${error}` : ''}`
        : `${label}: ${state}${error ? ` · ${error}` : ''}`
  return <div className="text-xs text-theme-text-tertiary">{text}</div>
}

export function CNPGClusterRuntime({
  namespace,
  name,
  onOpenLogs,
  onOpenInterval,
}: {
  namespace: string
  name: string
  onOpenLogs?: (pod: string) => void
  /** Opens Logs or Activity bounded to an interval selected on a trend chart. */
  onOpenInterval?: (target: CNPGIntervalTarget, since: string, until: string) => void
}) {
  const q = useCNPGRuntime(namespace, name)
  const [section, setSection] = useState<Section>('replication')
  const samples = useSampleBuffer(q.data)

  if (!q.data && q.isLoading) return <PaneLoader label="Reading live state…" className="h-40" />
  if (!q.data) {
    return (
      <div className="p-4">
        <Notice>Runtime data could not be loaded: {q.error instanceof Error ? q.error.message : 'unknown error'}</Notice>
      </div>
    )
  }
  const data = q.data
  if (data.permission.proxy === 'denied') {
    return (
      <div className="p-4">
        <div className="max-w-2xl rounded-xl border border-dashed border-theme-border p-5">
          <div className="flex items-center gap-2 font-medium text-theme-text-primary">
            <Lock className="h-4 w-4" />
            Runtime data unavailable for {name}
          </div>
          <p className="mt-2 text-sm text-theme-text-secondary">
            Radar reads replication, sessions, WAL and slots from each instance through the Kubernetes API proxy, and your identity is not allowed to use it.
            Nothing below is shown as zero; it is omitted.
          </p>
          <pre className="mt-2 rounded-md bg-theme-elevated px-3 py-2 font-mono text-xs text-theme-text-primary">{`requires: ${data.permission.grant ?? `get pods/proxy in ${namespace}`}`}</pre>
          <p className="mt-2 text-sm text-theme-text-secondary">Still available: Overview, Protection, Activity, Logs, Spec & status and YAML, and the volumes below.</p>
        </div>
        <div className="mt-4">
          <CNPGStorage namespace={namespace} name={name} />
        </div>
      </div>
    )
  }

  const primary = data.instances.find((i) => i.role === 'primary')
  const replicas = data.instances.filter((i) => i.role !== 'primary')

  return (
    <div className="space-y-4 p-4">
      <div className="flex flex-wrap items-center gap-3">
        <Segments label="Runtime section" value={section} onChange={setSection} options={SECTIONS} />
        <span className="text-xs text-theme-text-tertiary">
          Live from each instance · sampled {formatAge(data.sampledAt)} ago
        </span>
      </div>

      {section === 'replication' && (
        <ReplicationView namespace={namespace} cluster={name} primary={primary} replicas={replicas} onOpenLogs={onOpenLogs} />
      )}
      {section === 'sessions' && <SessionsView primary={primary} />}
      {section === 'transactions' && <TransactionsView primary={primary} samples={samples} />}
      {section === 'storage' && <StorageView namespace={namespace} name={name} instances={data.instances} />}
      {section === 'slots' && <SlotsView primary={primary} />}
      {section === 'trends' && <CNPGTrends namespace={namespace} name={name} samples={samples} onOpenInterval={onOpenInterval} />}
    </div>
  )
}

function Card({ title, children, footer }: { title: ReactNode; children: ReactNode; footer?: ReactNode }) {
  return (
    <section className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
      <div className="border-b border-theme-border px-4 py-2.5 text-sm font-semibold text-theme-text-primary">{title}</div>
      <div className="p-4">{children}</div>
      {footer && <div className="border-t border-theme-border px-4 py-2 text-xs text-theme-text-tertiary">{footer}</div>}
    </section>
  )
}

function ReplicationView({
  namespace,
  cluster,
  primary,
  replicas,
  onOpenLogs,
}: {
  namespace: string
  cluster: string
  primary?: CNPGRuntimeInstance
  replicas: CNPGRuntimeInstance[]
  onOpenLogs?: (pod: string) => void
}) {
  const rows = new Map((primary?.status.replication ?? []).map((r) => [r.applicationName, r]))
  // The instance manager keeps answering on a fenced Pod with PostgreSQL
  // stopped, so fencing comes from the Cluster, not from the runtime read.
  const fenced = new Set(useCNPGClusterCapabilities(namespace, cluster).data?.facts.instances.filter((i) => i.fenced).map((i) => i.pod))
  return (
    <Card
      title="Instances and replication"
      footer="Replication rows come from the primary's pg_stat_replication through the instance manager. Lag is time behind the primary for replay."
    >
      <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-[minmax(220px,280px)_minmax(0,1fr)]">
        <div className="rounded-lg border border-theme-border border-l-4 border-l-accent bg-theme-base p-3">
          <div className="flex items-center gap-2">
            <span className="font-mono text-sm font-semibold">{primary?.pod ?? 'No primary reported'}</span>
            <span className="badge-sm bg-theme-elevated text-theme-text-secondary">primary</span>
          </div>
          {primary && (
            <>
              <div className="mt-1 font-mono text-xs text-theme-text-secondary">
                LSN {primary.status.currentLsn ?? '—'} · TL {primary.status.timeline ?? '—'}
              </div>
              <SourceState label="Status" state={primary.status.state} error={primary.status.error} />
              <div className="mt-2 flex flex-wrap gap-3 text-xs">
                {onOpenLogs && <button type="button" className="text-accent-text hover:underline" onClick={() => onOpenLogs(primary.pod)}>Logs</button>}
                <CNPGInstanceActions namespace={namespace} cluster={cluster} pod={primary.pod} />
              </div>
            </>
          )}
        </div>
        <div className="space-y-2">
          {replicas.length === 0 && <div className="text-sm text-theme-text-tertiary">Single instance: no replica to fail over to.</div>}
          {replicas.map((r) => {
            const rep = rows.get(r.pod)
            const tone = rep ? lagTone(rep.replayLag) : 'unknown'
            const pct = rep?.replayLag !== undefined ? Math.min(100, (rep.replayLag / 60) * 100) : 0
            return (
              <div key={r.pod} className="rounded-lg border border-theme-border bg-theme-base p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <StatusDot tone={tone} />
                  <span className="font-mono text-sm font-semibold">{r.pod}</span>
                  <span className={clsx('text-xs', toneTextClass(tone))}>
                    {rep ? [rep.state, rep.syncState].filter(Boolean).join(' · ') : fenced.has(r.pod) ? 'fenced · PostgreSQL stopped' : r.role === 'unknown' ? 'role unknown' : primary?.status.state === 'ok' ? 'not streaming from the primary' : 'unknown'}
                  </span>
                  <span className="ml-auto font-mono text-xs text-theme-text-secondary">
                    {rep ? `replay lag ${seconds(rep.replayLag)}` : 'lag unknown'}
                  </span>
                </div>
                <div className="mt-2 flex items-center gap-2">
                  <div className="h-1 flex-1 overflow-hidden rounded bg-theme-elevated">
                    <div className={clsx('h-full', tone === 'unknown' ? 'bg-transparent' : toneFillClass(tone))} style={{ width: `${pct}%` }} />
                  </div>
                  <span className="text-[11px] text-theme-text-tertiary">60 s scale</span>
                </div>
                <div className="mt-1 font-mono text-xs text-theme-text-tertiary">
                  received {r.status.receivedLsn ?? '—'} · replayed {r.status.replayLsn ?? '—'}
                  {r.status.replayPaused ? ' · replay paused' : ''}
                </div>
                <SourceState label="Status" state={r.status.state} error={r.status.error} />
                <div className="mt-2 flex flex-wrap gap-3 text-xs">
                  {onOpenLogs && <button type="button" className="text-accent-text hover:underline" onClick={() => onOpenLogs(r.pod)}>Logs</button>}
                  <CNPGInstanceActions namespace={namespace} cluster={cluster} pod={r.pod} />
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </Card>
  )
}

function Unavailable({ inst, what }: { inst?: CNPGRuntimeInstance; what: string }) {
  if (!inst) return <div className="text-sm text-theme-text-tertiary">No primary reported, so {what} is unknown.</div>
  return <SourceState label={what} state={inst.metrics.state} error={inst.metrics.error} />
}

function SessionsView({ primary }: { primary?: CNPGRuntimeInstance }) {
  const m = primary?.metrics
  if (!m || m.state !== 'ok') return <Card title="Sessions"><Unavailable inst={primary} what="Sessions" /></Card>
  const rows = [...(m.sessions ?? [])].sort((a, b) => b.count - a.count)
  const idleTx = rows.filter((r) => r.state.startsWith('idle in transaction')).reduce((s, r) => s + r.count, 0)
  return (
    <Card
      title={<>Sessions on {primary!.pod}</>}
      footer="Counts by state, database, user and application from the metrics exporter (platform users excluded). Individual sessions and query text are not shown; query text appears in Logs when PostgreSQL logs it."
    >
      <div className="mb-3 flex flex-wrap gap-6 text-sm">
        <Metric label="Connections" value={m.sessionsTotal !== undefined ? `${m.sessionsTotal}${m.maxConnections ? ` / ${m.maxConnections}` : ''}` : '—'} tone={m.maxConnections && m.sessionsTotal && m.sessionsTotal / m.maxConnections > 0.85 ? 'degraded' : undefined} />
        <Metric label="Waiting on locks" value={m.waitingBackends ?? '—'} tone={m.waitingBackends ? 'degraded' : undefined} />
        <Metric label="Idle in transaction" value={idleTx} tone={idleTx ? 'degraded' : undefined} />
        <Metric label="Oldest transaction" value={seconds(m.oldestXactSeconds)} tone={m.oldestXactSeconds !== undefined && m.oldestXactSeconds > 300 ? 'degraded' : undefined} />
      </div>
      {rows.length === 0 ? (
        <div className="text-sm text-theme-text-tertiary">{m.missing?.includes('backends') ? 'The exporter does not publish session metrics on this cluster.' : 'No client sessions.'}</div>
      ) : (
        <table className="w-full text-sm">
          <thead className="text-left text-[11px] uppercase tracking-wide text-theme-text-tertiary">
            <tr><th className="py-1.5 pr-3">State</th><th className="pr-3">Database</th><th className="pr-3">User</th><th className="pr-3">Application</th><th className="text-right">Sessions</th></tr>
          </thead>
          <tbody className="table-divide-subtle">
            {rows.map((r, i) => (
              <tr key={i}>
                <td className={clsx('py-1.5 pr-3', r.state.startsWith('idle in transaction') && toneTextClass('degraded'))}>{r.state}</td>
                <td className="pr-3 font-mono text-xs">{r.database}</td>
                <td className="pr-3 font-mono text-xs">{r.user}</td>
                <td className="pr-3 text-xs text-theme-text-secondary">{r.application || '—'}</td>
                <td className="text-right font-mono">{r.count}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  )
}

function Metric({ label, value, tone }: { label: string; value: ReactNode; tone?: 'degraded' | 'unhealthy' }) {
  return (
    <div>
      <div className="text-xs text-theme-text-tertiary">{label}</div>
      <div className={clsx('font-mono text-base', tone ? toneTextClass(tone) : 'text-theme-text-primary')}>{value}</div>
    </div>
  )
}

function rate(samples: Sample[], key: 'commits' | 'rollbacks'): number | undefined {
  const pts = samples.filter((s, i) => s[key] !== undefined && s.metricsAt !== undefined && s.metricsAt !== samples[i - 1]?.metricsAt)
  if (pts.length < 2) return undefined
  const a = pts[pts.length - 2]
  const b = pts[pts.length - 1]
  const dt = ((b.metricsAt as number) - (a.metricsAt as number)) / 1000
  const dv = (b[key] as number) - (a[key] as number)
  if (dt <= 0 || dv < 0) return undefined
  return dv / dt
}

function TransactionsView({ primary, samples }: { primary?: CNPGRuntimeInstance; samples: Sample[] }) {
  const m = primary?.metrics
  if (!m || m.state !== 'ok') return <Card title="Transactions"><Unavailable inst={primary} what="Transactions" /></Card>
  const commits = rate(samples, 'commits')
  const rollbacks = rate(samples, 'rollbacks')
  const hit = m.blksHit !== undefined && m.blksRead !== undefined && m.blksHit + m.blksRead > 0 ? (m.blksHit / (m.blksHit + m.blksRead)) * 100 : undefined
  return (
    <Card title="Transactions" footer="Rates are computed from two consecutive samples taken while this page is open; the exporter refreshes about every 30 s.">
      <div className="flex flex-wrap gap-8">
        <Metric label="Commits / s" value={commits !== undefined ? commits.toFixed(1) : 'collecting…'} />
        <Metric label="Rollbacks / s" value={rollbacks !== undefined ? rollbacks.toFixed(1) : 'collecting…'} />
        <Metric label="Cache hit ratio" value={hit !== undefined ? `${hit.toFixed(1)} %` : '—'} />
        <Metric label="Deadlocks (total)" value={m.deadlocksTotal ?? '—'} tone={m.deadlocksTotal ? 'degraded' : undefined} />
        <Metric label="Oldest transaction" value={seconds(m.oldestXactSeconds)} />
      </div>
      {m.xidAge && m.xidAge.length > 0 && (
        <div className="mt-4 text-sm">
          <div className="text-xs text-theme-text-tertiary">Transaction ID age (wraparound at ~2 billion)</div>
          {m.xidAge.map((x) => (
            <div key={x.database} className="flex gap-3 font-mono text-xs">
              <span className="w-40 truncate">{x.database}</span>
              <span className={x.age > 1_000_000_000 ? toneTextClass('degraded') : undefined}>{(x.age / 1_000_000).toFixed(0)} M</span>
            </div>
          ))}
        </div>
      )}
    </Card>
  )
}

function StorageView({ namespace, name, instances }: { namespace: string; name: string; instances: CNPGRuntimeInstance[] }) {
  return <CNPGStorage namespace={namespace} name={name} primary={instances.find((i) => i.role === 'primary')} />
}

function SlotsView({ primary }: { primary?: CNPGRuntimeInstance }) {
  const slots = primary?.status.slots ?? []
  return (
    <Card title="Replication slots (primary)" footer="Inactive slots retain WAL until they are consumed or dropped.">
      {primary?.status.state !== 'ok' ? (
        <SourceState label="Status" state={primary?.status.state ?? 'error'} error={primary?.status.error} />
      ) : slots.length === 0 ? (
        <div className="text-sm text-theme-text-tertiary">No replication slots.</div>
      ) : (
        <table className="w-full text-sm">
          <thead className="text-left text-[11px] uppercase tracking-wide text-theme-text-tertiary">
            <tr><th className="py-1.5 pr-3">Slot</th><th className="pr-3">Type</th><th className="pr-3">State</th><th className="pr-3">WAL status</th><th className="text-right">Retained</th></tr>
          </thead>
          <tbody className="table-divide-subtle">
            {slots.map((s) => (
              <tr key={s.name}>
                <td className="py-1.5 pr-3 font-mono text-xs">{s.name}</td>
                <td className="pr-3">{s.type ?? '—'}</td>
                <td className={clsx('pr-3', s.active === false && toneTextClass('degraded'))}>{s.active === undefined ? '—' : s.active ? 'active' : 'inactive'}</td>
                <td className="pr-3">{s.walStatus ?? '—'}</td>
                <td className="text-right font-mono">{bytes(s.retainedBytes)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  )
}
