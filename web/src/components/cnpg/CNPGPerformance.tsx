import type { ReactNode } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import { PaneLoader, Tooltip, formatAge, toneTextClass, formatGrant } from '@skyhook-io/k8s-ui'
import { useCNPGRuntime, type CNPGRuntimeInstance } from '../../api/cnpg'
import { useCNPGSessions, type CNPGSessionsResponse } from '../../api/cnpg-sessions'
import { useCNPGClusterHistory } from '../../api/cnpg-history'
import { CNPGBlockingSessions } from './CNPGBlockingSessions'
import { cnpgConnectionFigure } from './blocking'
import { cnpgCheckpointView, cnpgDatabaseHealthRows, cnpgIdAge, cnpgPickedInstance, cnpgSessionAggregatesGap, cnpgSessionsCardShowsConnections, cnpgTransactionRates, type CNPGTransactionRates } from './runtimeModel'
import { formatBytes } from './lsn'
import { historyLatest, latestRate } from './trendSamples'
import { CNPGTrends, useSampleBuffer, type CNPGChartGroup, type CNPGIntervalTarget, type Sample } from './CNPGTrends'
import { Notice, RefreshFailedNotice, Segments } from '../workspace/layout'
import { Card, Metric, ProxyDenied, Unavailable, SourceState, seconds } from './runtimeParts'

type Section = 'sessions' | 'health' | 'history'

const SECTIONS: { id: Section; label: string }[] = [
  { id: 'sessions', label: 'Sessions' },
  { id: 'health', label: 'Database health' },
  { id: 'history', label: 'History' },
]

/**
 * Performance: who is connected and what is blocked (Sessions), how the
 * databases are doing (Database health), and how it changed (History). The
 * instance picker drives Sessions and Database health; History covers the
 * cluster.
 */
export function CNPGPerformance({
  namespace,
  name,
  onOpenInterval,
}: {
  namespace: string
  name: string
  /** Opens Logs or Activity bounded to an interval selected on a trend chart. */
  onOpenInterval?: (target: CNPGIntervalTarget, since: string, until: string) => void
}) {
  const q = useCNPGRuntime(namespace, name)
  // In the URL so Back from Logs or Activity returns to the same section.
  const [searchParams, setSearchParams] = useSearchParams()
  const location = useLocation()
  const section = SECTIONS.find((x) => x.id === searchParams.get('section'))?.id ?? 'sessions'
  const setSection = (next: Section) =>
    setSearchParams(
      (prev) => {
        const params = new URLSearchParams(prev)
        if (next === 'sessions') params.delete('section')
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
        <Notice>Live instance data could not be loaded: {q.error instanceof Error ? q.error.message : 'unknown error'}</Notice>
      </div>
    )
  }
  const data = q.data
  const denied = data.permission.proxy === 'denied'
  const grant = formatGrant(data.permission.grant) ?? `get pods/proxy in namespace ${namespace}`
  const primary = data.instances.find((i) => i.role === 'primary')
  // Sessions and Database health read one instance's exporter; the primary
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
  const charts = (searchParams.get('charts') as CNPGChartGroup | null) ?? undefined

  return (
    <div className="space-y-4 p-4">
      <div className="flex flex-wrap items-center gap-3">
        <Segments label="Performance section" value={section} onChange={setSection} options={SECTIONS} />
        {section !== 'history' && (
          <span className="text-xs text-theme-text-tertiary">
            {denied ? 'Live instance data needs access you do not have' : `Live from each instance · sampled ${formatAge(data.sampledAt)} ago`}
          </span>
        )}
      </div>
      <RefreshFailedNotice queries={[q]} />

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
      {section === 'health' && (
        <TransactionsView namespace={namespace} cluster={name} primary={primary} instance={picked} picker={picker} samples={samples} deniedGrant={denied ? grant : undefined} />
      )}
      {section === 'history' && (
        <CNPGTrends
          namespace={namespace}
          name={name}
          samples={samples}
          onOpenInterval={onOpenInterval}
          instance={picked?.pod}
          picker={picker}
          samplingDenied={denied ? grant : undefined}
          group={charts}
          standbys={data.instances.filter((i) => i.role === 'replica').map((i) => i.pod)}
          instancePods={data.instances.map((i) => i.pod)}
        />
      )}
    </div>
  )
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
  if (!m || (m.state !== 'ok' && m.state !== 'partial')) return <Card title="Sessions"><Unavailable inst={primary} what="Sessions" /></Card>
  const rows = [...(m.sessions ?? [])].sort((a, b) => b.count - a.count)
  const measured = m.sessionsTotal !== undefined
  const idleTx = measured
    ? Object.entries(m.sessionsByState ?? {}).filter(([state]) => state.startsWith('idle in transaction')).reduce((s, [, count]) => s + count, 0)
    : undefined
  const connections = cnpgConnectionFigure(exec, m)
  return (
    <Card
      title={<>Sessions on {primary!.pod}</>}
      footer="Counts by state, database, user and application from the metrics exporter (platform users excluded). Individual blocking sessions, with their query text, are below when you can exec into the instance."
    >
      <SourceState label="Sessions" state={m.state} reason={m.reason} error={m.error} />
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
            ? m.state === 'partial'
              ? 'No client sessions seen in what was read.'
              : 'No client sessions.'
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
  // The page samples the primary's counters whichever instance is picked, so
  // sampled rates are the primary's and do not follow the picker.
  const canSample = !deniedGrant && (primary?.metrics.state === 'ok' || primary?.metrics.state === 'partial')
  const rates = cnpgTransactionRates(
    { commits: historyLatest(history.data, 'tps', 'commits'), rollbacks: historyLatest(history.data, 'tps', 'rollbacks') },
    canSample ? { commits: latestRate(samples, 'commits'), rollbacks: latestRate(samples, 'rollbacks') } : null,
    deniedGrant ? `needs ${deniedGrant}` : '—',
  )
  const throughputTitle = rates.source === 'prometheus' ? 'Cluster throughput' : 'Throughput on the primary'
  return (
    <div className="space-y-4">
      <RefreshFailedNotice queries={[history]} />
      {deniedGrant ? (
        <>
          {rates.source !== 'none' && (
            <Card title="Cluster throughput" footer={RATES_FOOTER.prometheus}>
              <Rates rates={rates} />
            </Card>
          )}
          <ProxyDenied what="Per-instance transaction figures, cache hit ratio, deadlocks, transaction and multixact ID age, and extension versions" grant={deniedGrant} />
        </>
      ) : (
        <>
          <Card title={throughputTitle} footer={RATES_FOOTER[rates.source]}>
            <Rates rates={rates} />
          </Card>
          {picker}
          <TransactionsCard inst={instance} />
          <CheckpointsCard inst={instance} />
        </>
      )}
    </div>
  )
}

const RATES_FOOTER = {
  prometheus: 'Prometheus’s latest commit and rollback rates across every instance and database of the cluster.',
  sampled: "Rates are the change between the primary exporter's last two query runs seen while this page is open, never across a change of primary.",
  none: 'No current commit or rollback rate: Prometheus has no recent point, and this page samples only the primary through its exporter.',
}

function Rates({ rates }: { rates: CNPGTransactionRates }) {
  return (
    <div>
      <div className="flex gap-8">
        <Metric label="Commits / s" value={rates.commits} />
        <Metric label="Rollbacks / s" value={rates.rollbacks} />
      </div>
      {rates.at !== undefined && (
        <div className="mt-0.5 text-[11px] text-theme-text-tertiary">
          {rates.source === 'prometheus' ? 'From Prometheus, point' : 'Last Prometheus point'} {formatAge(new Date(rates.at * 1000).toISOString())} ago
        </div>
      )}
    </div>
  )
}

function TransactionsCard({ inst }: { inst?: CNPGRuntimeInstance }) {
  const m = inst?.metrics
  if (!m || (m.state !== 'ok' && m.state !== 'partial')) {
    return (
      <Card title="Database health">
        <Unavailable inst={inst} what="Database health" />
      </Card>
    )
  }
  const hit = m.blksHit !== undefined && m.blksRead !== undefined && m.blksHit + m.blksRead > 0 ? (m.blksHit / (m.blksHit + m.blksRead)) * 100 : undefined
  return (
    <Card title={<>Database health on {inst!.pod}</>} footer="This instance’s exporter. Counters are cumulative since the last statistics reset.">
      <SourceState label="Database health" state={m.state} reason={m.reason} error={m.error} />
      <div className="flex flex-wrap gap-8">
        <Metric label="Cache hit ratio" value={hit !== undefined ? `${hit.toFixed(1)} %` : '—'} />
        <Metric label="Deadlocks (total)" value={m.deadlocksTotal ?? '—'} tone={m.deadlocksTotal ? 'degraded' : undefined} />
        <Metric label="Oldest transaction" value={seconds(m.oldestXactSeconds)} />
      </div>
      <DatabaseHealth m={m} />
      <ExtensionUpdates rows={m.extensionUpdates} missing={m.missing} partial={m.state === 'partial'} />
    </Card>
  )
}

function CheckpointsCard({ inst }: { inst?: CNPGRuntimeInstance }) {
  const m = inst?.metrics
  if (!m || (m.state !== 'ok' && m.state !== 'partial')) return <Card title="Checkpoints"><Unavailable inst={inst} what="Checkpoints" /></Card>
  const c = m.checkpoints
  if (!c) {
    return (
      <Card title={<>Checkpoints on {inst!.pod}</>}>
        <SourceState label="Checkpoints" state={m.state} reason={m.reason} error={m.error} />
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
      <SourceState label="Checkpoints" state={m.state} reason={m.reason} error={m.error} />
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

function ExtensionUpdates({ rows, missing, partial }: { partial?: boolean; rows?: { database: string; extension: string; installedVersion: string; defaultVersion: string }[] | null; missing?: string[] }) {
  return (
    <div className="mt-4 text-sm">
      <div className="text-xs text-theme-text-tertiary">Extensions with updates available</div>
      {missing?.includes(EXTENSIONS_FAMILY) || !rows ? (
        <NotExported what="Extension versions" family={EXTENSIONS_FAMILY} />
      ) : rows.length === 0 ? (
        <div className="text-xs text-theme-text-secondary">{partial ? 'No extension updates seen in what was read.' : 'Every installed extension is at its default version.'}</div>
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
