import type { ReactNode } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import { Lock } from 'lucide-react'
import { PaneLoader, Tooltip, formatAge, toneTextClass, formatGrant } from '@skyhook-io/k8s-ui'
import { useCNPGRuntime, type CNPGRuntimeInstance } from '../../api/cnpg'
import { useCNPGSessions, type CNPGSessionsResponse } from '../../api/cnpg-sessions'
import { useCNPGClusterHistory } from '../../api/cnpg-history'
import { CNPGStorage } from './CNPGStorage'
import { CNPGBlockingSessions } from './CNPGBlockingSessions'
import { cnpgConnectionFigure } from './blocking'
import { CNPGReplicationView } from './CNPGReplicationView'
import { cnpgCheckpointView, cnpgDatabaseHealthRows, cnpgIdAge, cnpgPickedInstance, cnpgSessionAggregatesGap, cnpgSessionsCardShowsConnections, cnpgTransactionRates, type CNPGTransactionRates } from './runtimeModel'
import { formatBytes } from './lsn'
import { historyLatest, latestRate } from './trendSamples'
import { CNPGTrends, useSampleBuffer, type CNPGIntervalTarget, type Sample } from './CNPGTrends'
import { Notice, RefreshFailedNotice, Segments } from '../workspace/layout'

type Section = 'replication' | 'sessions' | 'transactions' | 'storage' | 'slots' | 'trends'

const SECTIONS: { id: Section; label: string }[] = [
  { id: 'replication', label: 'Replication' },
  { id: 'sessions', label: 'Sessions' },
  { id: 'transactions', label: 'Transactions' },
  { id: 'storage', label: 'Storage & WAL' },
  { id: 'slots', label: 'Slots' },
  { id: 'trends', label: 'Trends' },
]

function seconds(s?: number): string {
  if (s === undefined) return '—'
  if (s < 1) return `${(s * 1000).toFixed(0)} ms`
  if (s < 90) return `${s.toFixed(1)} s`
  if (s < 5400) return `${Math.round(s / 60)} min`
  return `${(s / 3600).toFixed(1)} h`
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
  // In the URL so Back from Logs or Activity returns to the same section.
  const [searchParams, setSearchParams] = useSearchParams()
  const location = useLocation()
  const section = SECTIONS.find((x) => x.id === searchParams.get('section'))?.id ?? 'replication'
  const setSection = (next: Section) =>
    setSearchParams(
      (prev) => {
        const params = new URLSearchParams(prev)
        if (next === 'replication') params.delete('section')
        else params.set('section', next)
        return params
      },
      { replace: true, state: location.state },
    )
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
  const denied = data.permission.proxy === 'denied'
  const grant = formatGrant(data.permission.grant) ?? `get pods/proxy in namespace ${namespace}`
  const primary = data.instances.find((i) => i.role === 'primary')
  const replicas = data.instances.filter((i) => i.role !== 'primary')
  // Sessions and Transactions read one instance's exporter; the primary
  // unless another is picked (kept in the URL like the section).
  // With no primary reported, the first instance, so standbys stay reachable.
  const picked = cnpgPickedInstance(data.instances, searchParams.get('instance'))
  const setPicked = (pod: string) =>
    setSearchParams(
      (prev) => {
        const params = new URLSearchParams(prev)
        if (primary && pod === primary.pod) params.delete('instance')
        else params.set('instance', pod)
        return params
      },
      { replace: true, state: location.state },
    )
  const picker =
    data.instances.length > 0 && picked ? (
      <label className="flex items-center gap-2 text-xs text-theme-text-secondary">
        Instance
        <select
          value={picked.pod}
          onChange={(e) => setPicked(e.target.value)}
          className="rounded border border-theme-border bg-theme-base px-1.5 py-0.5 font-mono text-xs text-theme-text-primary"
        >
          {data.instances.map((i) => (
            <option key={i.pod} value={i.pod}>
              {i.pod}
              {i.role === 'primary' ? ' (primary)' : i.role === 'replica' ? ' (standby)' : ''}
            </option>
          ))}
        </select>
      </label>
    ) : null

  return (
    <div className="space-y-4 p-4">
      <div className="flex flex-wrap items-center gap-3">
        <Segments label="Runtime section" value={section} onChange={setSection} options={SECTIONS} />
        {section !== 'trends' && section !== 'storage' && (
          <span className="text-xs text-theme-text-tertiary">
            {denied ? 'Live instance data needs access you do not have' : `Live from each instance · sampled ${formatAge(data.sampledAt)} ago`}
          </span>
        )}
      </div>
      <RefreshFailedNotice queries={[q]} />

      {section === 'replication' &&
        (denied ? (
          <ProxyDenied what="Replication lag, LSNs and instance state" grant={grant} />
        ) : (
          <CNPGReplicationView namespace={namespace} cluster={name} primary={primary} replicas={replicas} onOpenLogs={onOpenLogs} card={Card} />
        ))}
      {section === 'sessions' &&
        (denied ? (
          <>
            {picker}
            <ProxyDenied what="Session counts by state, lock waits and connection headroom" grant={grant} />
            <CNPGBlockingSessions namespace={namespace} cluster={name} pod={picked?.pod} aggregatesGap={`they need ${grant}`} />
          </>
        ) : (
          <SessionsView namespace={namespace} cluster={name} instance={picked} picker={picker} />
        ))}
      {section === 'transactions' && (
        <TransactionsView namespace={namespace} cluster={name} primary={primary} instance={picked} picker={picker} samples={samples} deniedGrant={denied ? grant : undefined} />
      )}
      {section === 'storage' && (denied ? <CNPGStorage namespace={namespace} name={name} /> : <StorageView namespace={namespace} name={name} instances={data.instances} />)}
      {section === 'slots' && (denied ? <ProxyDenied what="Replication slots and the WAL they retain" grant={grant} /> : <SlotsView primary={primary} />)}
      {section === 'trends' && (
        <CNPGTrends
          namespace={namespace}
          name={name}
          samples={samples}
          onOpenInterval={onOpenInterval}
          instance={picked?.pod}
          picker={picker}
          samplingDenied={denied ? grant : undefined}
        />
      )}
    </div>
  )
}

// Denied is not zero: the section says what it would show and the grant it needs.
function ProxyDenied({ what, grant }: { what: string; grant: string }) {
  return (
    <div className="max-w-2xl rounded-xl border border-dashed border-theme-border p-5">
      <div className="flex items-center gap-2 font-medium text-theme-text-primary">
        <Lock className="h-4 w-4" />
        No access to live instance data
      </div>
      <p className="mt-2 text-sm text-theme-text-secondary">
        {what} are read from each instance through the Kubernetes API proxy, which your identity may not use. Nothing is shown as zero; it is omitted.
      </p>
      <pre className="mt-2 rounded-md bg-theme-elevated px-3 py-2 font-mono text-xs text-theme-text-primary">{`requires: ${grant}`}</pre>
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

function Unavailable({ inst, what }: { inst?: CNPGRuntimeInstance; what: string }) {
  if (!inst) return <div className="text-sm text-theme-text-tertiary">No instance is reported, so {what} is unknown.</div>
  return <SourceState label={what} state={inst.metrics.state} error={inst.metrics.error} />
}

function SessionsView({ namespace, cluster, instance, picker }: { namespace: string; cluster: string; instance?: CNPGRuntimeInstance; picker?: ReactNode }) {
  // The pod is always named: left to the server, "the primary" is its own
  // reading of status.currentPrimary, which can differ from the one picked.
  const blocking = useCNPGSessions(namespace, cluster, instance?.pod)
  const exec = blocking.data?.state === 'ok' && blocking.data.pod === instance?.pod ? blocking.data : undefined
  const aggregatesGap = cnpgSessionAggregatesGap(instance)
  return (
    <div className="space-y-4">
      {picker}
      <SessionAggregates primary={instance} exec={exec} />
      <CNPGBlockingSessions namespace={namespace} cluster={cluster} pod={instance?.pod} aggregatesGap={aggregatesGap} headroom={!cnpgSessionsCardShowsConnections(instance, exec)} />
    </div>
  )
}

// `primary` is the instance shown: the primary unless another was picked.
function SessionAggregates({ primary, exec }: { primary?: CNPGRuntimeInstance; exec?: CNPGSessionsResponse }) {
  const m = primary?.metrics
  if (!m || m.state !== 'ok') return <Card title="Sessions"><Unavailable inst={primary} what="Sessions" /></Card>
  const rows = [...(m.sessions ?? [])].sort((a, b) => b.count - a.count)
  const measured = m.sessionsTotal !== undefined
  const idleTx = measured ? rows.filter((r) => r.state.startsWith('idle in transaction')).reduce((s, r) => s + r.count, 0) : undefined
  const connections = cnpgConnectionFigure(exec, m)
  return (
    <Card
      title={<>Sessions on {primary!.pod}</>}
      footer="Counts by state, database, user and application from the metrics exporter (platform users excluded). Individual blocking sessions, with their query text, are below when you can exec into the instance."
    >
      <div className="mb-3 flex flex-wrap gap-6 text-sm">
        <Metric
          label="Connections"
          value={connections ? <Tooltip content={connections.detail}>{connections.value}</Tooltip> : '—'}
          caption={connections?.limit}
          tone={connections?.tone}
        />
        <Metric label="Waiting on locks" value={m.waitingBackends ?? '—'} tone={m.waitingBackends ? 'degraded' : undefined} />
        <Metric label="Idle in transaction" value={idleTx ?? '—'} tone={idleTx ? 'degraded' : undefined} />
        <Metric label="Oldest transaction" value={seconds(m.oldestXactSeconds)} tone={m.oldestXactSeconds !== undefined && m.oldestXactSeconds > 300 ? 'degraded' : undefined} />
      </div>
      {rows.length === 0 ? (
        <div className="text-sm text-theme-text-tertiary">
          {measured
            ? 'No client sessions.'
            : 'Sessions unknown: this sample from the exporter has no cnpg_backends_total, so sessions were not measured. PostgreSQL may not be accepting connections, or the exporter does not collect it.'}
        </div>
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

function Metric({ label, value, tone, caption }: { label: string; value: ReactNode; tone?: 'degraded' | 'unhealthy'; caption?: ReactNode }) {
  return (
    <div>
      <div className="text-xs text-theme-text-tertiary">{label}</div>
      <div className={clsx('font-mono text-base', tone ? toneTextClass(tone) : 'text-theme-text-primary')}>{value}</div>
      {caption && <div className="text-[11px] text-theme-text-tertiary">{caption}</div>}
    </div>
  )
}

function TransactionsView({
  namespace,
  cluster,
  primary,
  instance,
  picker,
  samples,
  deniedGrant,
}: {
  namespace: string
  cluster: string
  primary?: CNPGRuntimeInstance
  instance?: CNPGRuntimeInstance
  picker?: ReactNode
  samples: Sample[]
  /** pods/proxy is denied: no exporter figures and no sampling, but Prometheus rates may still be readable. */
  deniedGrant?: string
}) {
  const history = useCNPGClusterHistory(namespace, cluster, '15m')
  const isPrimary = !!instance && instance === primary
  const canSample = !deniedGrant && isPrimary && instance?.metrics.state === 'ok'
  const rates = cnpgTransactionRates(
    { commits: historyLatest(history.data, 'tps', 'commits'), rollbacks: historyLatest(history.data, 'tps', 'rollbacks') },
    // The page samples the primary's counters only, so sampled rates are the primary's.
    canSample ? { commits: latestRate(samples, 'commits'), rollbacks: latestRate(samples, 'rollbacks') } : null,
    deniedGrant ? `needs ${deniedGrant}` : !isPrimary ? 'primary only' : '—',
  )
  return (
    <div className="space-y-4">
      <RefreshFailedNotice queries={[history]} />
      {!deniedGrant && picker}
      {deniedGrant ? (
        <>
          {rates.source !== 'none' && (
            <Card title="Transactions (cluster)" footer={RATES_FOOTER.prometheus}>
              <Rates rates={rates} />
            </Card>
          )}
          <ProxyDenied what="Per-instance transaction figures, cache hit ratio, deadlocks, transaction and multixact ID age, and extension versions" grant={deniedGrant} />
        </>
      ) : (
        <>
          <TransactionsCard inst={instance} rates={rates} />
          <CheckpointsCard inst={instance} />
        </>
      )}
    </div>
  )
}

const RATES_FOOTER = {
  prometheus: 'Commit and rollback rates are Prometheus’s latest rate across every instance and database of the cluster; the other figures are this instance’s.',
  sampled: "Rates are the change between the primary exporter's last two query runs seen while this page is open, never across a change of primary.",
  none: 'No current commit or rollback rate: Prometheus has no recent point, and this page samples only the primary through its exporter.',
}

function Rates({ rates }: { rates: CNPGTransactionRates }) {
  const cluster = rates.source === 'prometheus' ? ' (cluster)' : ''
  return (
    <div>
      <div className="flex gap-8">
        <Metric label={`Commits / s${cluster}`} value={rates.commits} />
        <Metric label={`Rollbacks / s${cluster}`} value={rates.rollbacks} />
      </div>
      {rates.at !== undefined && (
        <div className="mt-0.5 text-[11px] text-theme-text-tertiary">
          {rates.source === 'prometheus' ? 'From Prometheus, point' : 'Last Prometheus point'} {formatAge(new Date(rates.at * 1000).toISOString())} ago
        </div>
      )}
    </div>
  )
}

function TransactionsCard({ inst, rates }: { inst?: CNPGRuntimeInstance; rates: CNPGTransactionRates }) {
  const m = inst?.metrics
  if (!m || m.state !== 'ok') {
    return (
      <Card title="Transactions" footer={rates.source === 'prometheus' ? RATES_FOOTER.prometheus : undefined}>
        {rates.source === 'prometheus' && (
          <div className="mb-3 flex flex-wrap gap-8">
            <Rates rates={rates} />
          </div>
        )}
        <Unavailable inst={inst} what="Transactions" />
      </Card>
    )
  }
  const hit = m.blksHit !== undefined && m.blksRead !== undefined && m.blksHit + m.blksRead > 0 ? (m.blksHit / (m.blksHit + m.blksRead)) * 100 : undefined
  return (
    <Card title={<>Transactions on {inst!.pod}</>} footer={RATES_FOOTER[rates.source]}>
      <div className="flex flex-wrap gap-8">
        <Rates rates={rates} />
        <Metric label="Cache hit ratio" value={hit !== undefined ? `${hit.toFixed(1)} %` : '—'} />
        <Metric label="Deadlocks (total)" value={m.deadlocksTotal ?? '—'} tone={m.deadlocksTotal ? 'degraded' : undefined} />
        <Metric label="Oldest transaction" value={seconds(m.oldestXactSeconds)} />
      </div>
      <DatabaseHealth m={m} />
      <ExtensionUpdates rows={m.extensionUpdates} missing={m.missing} />
    </Card>
  )
}

function CheckpointsCard({ inst }: { inst?: CNPGRuntimeInstance }) {
  const m = inst?.metrics
  if (!m || m.state !== 'ok') return <Card title="Checkpoints"><Unavailable inst={inst} what="Checkpoints" /></Card>
  const c = m.checkpoints
  if (!c) {
    return (
      <Card title={<>Checkpoints on {inst!.pod}</>}>
        <div className="text-xs text-theme-text-tertiary">
          Unknown: the exporter reported neither pg_stat_checkpointer (PostgreSQL 17+) nor pg_stat_bgwriter checkpoint counters.
        </div>
      </Card>
    )
  }
  const view = cnpgCheckpointView(c, inst!.role)
  const n = (v?: number) => (v === undefined ? '—' : v.toLocaleString())
  return (
    <Card
      title={<>Checkpoints on {inst!.pod}</>}
      footer={
        <>
          Cumulative since the statistics were last reset, from {c.source}.{' '}
          {c.source === 'pg_stat_checkpointer'
            ? 'A standby performs restartpoints instead of checkpoints; they are counted separately.'
            : 'Before PostgreSQL 17 a standby counts its restartpoints as checkpoints.'}{' '}
          Many requested checkpoints usually mean max_wal_size is too small for the write load.
        </>
      }
    >
      <div className="flex flex-wrap gap-8">
        <Metric label="Timed" value={n(c.timed)} />
        <Metric label="Requested" value={n(c.requested)} tone={view.pressure ? 'degraded' : undefined} />
        {view.share !== undefined && <Metric label="Requested share" value={view.share} />}
        <Metric label="Buffers written" value={n(c.buffersWritten)} />
        {view.showRestartpoints ? (
          <>
            <Metric label="Restartpoints timed" value={n(c.restartpointsTimed)} />
            <Metric label="Restartpoints requested" value={n(c.restartpointsRequested)} />
            <Metric label="Restartpoints done" value={n(c.restartpointsDone)} />
          </>
        ) : null}
      </div>
    </Card>
  )
}

function NotExported({ what, family }: { what: string; family: string }) {
  return (
    <div className="text-xs text-theme-text-tertiary">
      {what} unknown: the exporter did not report <span className="font-mono">{family}</span> (a custom monitoring configuration may have replaced the default query).
    </div>
  )
}

const AGE_FAMILIES: { family: string; what: string }[] = [
  { family: 'cnpg_pg_stat_database_xact_rollback', what: 'Rollback ratio' },
  { family: 'cnpg_pg_stat_database_temp_bytes', what: 'Temporary files' },
  { family: 'cnpg_pg_database_xid_age', what: 'Transaction ID age' },
  { family: 'cnpg_pg_database_mxid_age', what: 'Multixact ID age' },
]

function DatabaseHealth({ m }: { m: CNPGRuntimeInstance['metrics'] }) {
  const rows = cnpgDatabaseHealthRows(m)
  const absent = AGE_FAMILIES.filter((f) => m.missing?.includes(f.family))
  // Whether a database accepts connections is not in the exporter's output, so
  // a missing value is only said to be unreported, never explained.
  const cell = (v: number | undefined, fmt: (v: number) => ReactNode) =>
    v !== undefined ? (
      fmt(v)
    ) : (
      <Tooltip content="Not reported by the exporter">
        <span className="text-theme-text-tertiary">—</span>
      </Tooltip>
    )
  const age = (v: number) => (
    <Tooltip content={`${v.toLocaleString()} of ~2 billion before wraparound`}>
      <span className={v > 1_000_000_000 ? toneTextClass('degraded') : undefined}>{cnpgIdAge(v)}</span>
    </Tooltip>
  )
  return (
    <div className="mt-4 text-sm">
      <div className="text-xs text-theme-text-tertiary">Per database · counters since the last statistics reset · ID wraparound at ~2 billion</div>
      {rows.length > 0 && (
        <table className="mt-1 w-full text-sm">
          <thead className="text-left text-[11px] uppercase tracking-wide text-theme-text-tertiary">
            <tr>
              <th className="py-1.5 pr-3">Database</th>
              <th className="pr-3 text-right">Rollback ratio</th>
              <th className="pr-3 text-right">Temp files</th>
              <th className="pr-3 text-right">Temp bytes</th>
              <th className="pr-3 text-right">Transaction ID age</th>
              <th className="text-right">Multixact ID age</th>
            </tr>
          </thead>
          <tbody className="table-divide-subtle">
            {rows.map((r) => (
              <tr key={r.database}>
                <td className="py-1.5 pr-3 font-mono text-xs">{r.database}</td>
                <td className="pr-3 text-right font-mono text-xs">
                  {r.noTransactions ? (
                    <span className="text-theme-text-tertiary">no transactions</span>
                  ) : (
                    cell(r.rollbackRatio, (v) => <span className={v > 0.1 ? toneTextClass('degraded') : undefined}>{(v * 100).toFixed(1)} %</span>)
                  )}
                </td>
                <td className="pr-3 text-right font-mono text-xs">{cell(r.tempFiles, (v) => v.toLocaleString())}</td>
                <td className="pr-3 text-right font-mono text-xs">{cell(r.tempBytes, formatBytes)}</td>
                <td className="pr-3 text-right font-mono text-xs">{cell(r.xidAge, age)}</td>
                <td className="text-right font-mono text-xs">{cell(r.mxidAge, age)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {absent.map((f) => (
        <NotExported key={f.family} what={f.what} family={f.family} />
      ))}
      {rows.length === 0 && absent.length === 0 && <div className="text-xs text-theme-text-tertiary">The exporter reported no per-database rows.</div>}
    </div>
  )
}

const EXTENSIONS_FAMILY = 'cnpg_pg_extensions_update_available'

function ExtensionUpdates({ rows, missing }: { rows?: { database: string; extension: string; installedVersion: string; defaultVersion: string }[] | null; missing?: string[] }) {
  return (
    <div className="mt-4 text-sm">
      <div className="text-xs text-theme-text-tertiary">Extensions with updates available</div>
      {missing?.includes(EXTENSIONS_FAMILY) || !rows ? (
        <NotExported what="Extension versions" family={EXTENSIONS_FAMILY} />
      ) : rows.length === 0 ? (
        <div className="text-xs text-theme-text-secondary">Every installed extension is at its default version.</div>
      ) : (
        <>
          {rows.map((x) => (
            <div key={`${x.database}/${x.extension}`} className="flex gap-3 font-mono text-xs">
              <span className="w-40 truncate">{x.database}</span>
              <span className="w-40 truncate">{x.extension}</span>
              <span>
                {x.installedVersion} → {x.defaultVersion}
              </span>
            </div>
          ))}
          <div className="mt-1 text-[11.5px] text-theme-text-tertiary">
            From the primary's metrics exporter (default query pg_extensions). The installed version differs from the default version the server's packages provide; <span className="font-mono">ALTER EXTENSION … UPDATE</span> applies it.
          </div>
        </>
      )}
    </div>
  )
}

function StorageView({ namespace, name, instances }: { namespace: string; name: string; instances: CNPGRuntimeInstance[] }) {
  return <CNPGStorage namespace={namespace} name={name} primary={instances.find((i) => i.role === 'primary')} />
}

function SlotsView({ primary }: { primary?: CNPGRuntimeInstance }) {
  const slots = primary?.status.slots ?? []
  const readable = primary?.status.state === 'ok' || primary?.status.state === 'partial'
  return (
    <Card title="Replication slots (primary)" footer="Inactive slots retain WAL until they are consumed or dropped.">
      {!readable || !primary?.status.slots ? (
        <SourceState label="Status" state={readable ? 'partial' : primary?.status.state ?? 'error'} error={primary?.status.error ?? primary?.status.reason ?? 'slots were not read'} />
      ) : slots.length === 0 ? (
        <div className="text-sm text-theme-text-tertiary">No replication slots.</div>
      ) : (
        <>
        {primary.status.state === 'partial' && <SourceState label="Status" state="partial" error={primary.status.reason} />}
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
                <td className="text-right font-mono">{formatBytes(s.retainedBytes)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        </>
      )}
    </Card>
  )
}
