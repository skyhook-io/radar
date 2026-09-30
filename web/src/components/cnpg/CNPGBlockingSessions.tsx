import { useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { Lock } from 'lucide-react'
import { ActionConfirmDialog, PaneLoader, Tooltip, formatAge, toneFillClass, toneTextClass } from '@skyhook-io/k8s-ui'
import { formatCPUString, formatMemoryString, parseCPUToNanocores, parseMemoryToBytes } from '@skyhook-io/k8s-ui/utils/format'
import { usePodMetrics } from '../../api/client'
import { cnpgActionOutcomeLocked, useCNPGAction, useCNPGClusterCapabilities } from '../../api/cnpg'
import { useCNPGSessions, type CNPGBackend, type CNPGSessionInstance, type CNPGSessionsResponse } from '../../api/cnpg-sessions'
import { useToast } from '../ui/Toast'
import { buildBlockingTree, cnpgConnectionFigure, countVictims, type BlockingNode } from './blocking'
import { CNPGRefreshFailedNotice } from './shared'

function age(s?: number): string {
  if (s === undefined || s === null) return '—'
  if (s < 1) return `${Math.round(s * 1000)} ms`
  if (s < 90) return `${s.toFixed(0)} s`
  if (s < 5400) return `${Math.round(s / 60)} min`
  return `${(s / 3600).toFixed(1)} h`
}

/**
 * Who blocks whom, on one instance, read from pg_stat_activity with
 * pg_blocking_pids over the caller's pods/exec — plus connection headroom and
 * each instance's CPU and memory, so lock waits and resource pressure can be
 * told apart. The instance is chosen by the Sessions section's picker.
 */
export function CNPGBlockingSessions({
  namespace,
  cluster,
  pod,
  aggregatesGap,
  headroom = true,
}: {
  namespace: string
  cluster: string
  /** The instance to read; undefined reads the primary. */
  pod?: string
  /** Why the exporter's aggregate counts are not shown above; undefined when they are. */
  aggregatesGap?: string
  /** false when the host already shows the connection figure. */
  headroom?: boolean
}) {
  const q = useCNPGSessions(namespace, cluster, pod)
  const data = q.data

  return (
    <section className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
      <div className="flex flex-wrap items-center gap-3 border-b border-theme-border px-4 py-2.5">
        <div className="text-sm font-semibold text-theme-text-primary">Blocking{data?.pod ? <> on <span className="font-mono">{data.pod}</span></> : null}</div>
        {data?.capturedAt && <span className="text-xs text-theme-text-tertiary">read {formatAge(data.capturedAt)} ago</span>}
      </div>
      <div className="space-y-4 p-4">
        {!data && q.isLoading && <PaneLoader label="Reading pg_stat_activity…" className="h-20" />}
        {!data && !q.isLoading && <div className="text-sm text-theme-text-tertiary">Sessions could not be read: {q.error instanceof Error ? q.error.message : 'unknown error'}</div>}
        <CNPGRefreshFailedNotice queries={[q]} />
        {data && <Body namespace={namespace} cluster={cluster} data={data} aggregatesGap={aggregatesGap} headroom={headroom} />}
      </div>
      <div className="border-t border-theme-border px-4 py-2 text-xs text-theme-text-tertiary">
        Read with a fixed query (pg_stat_activity, pg_blocking_pids) run by psql in the instance through your own pods/exec. Query text is shown because that same access
        lets you open psql and read it yourself; it is cut at 200 characters.
      </div>
    </section>
  )
}

function Body({
  namespace,
  cluster,
  data,
  aggregatesGap,
  headroom,
}: {
  namespace: string
  cluster: string
  data: CNPGSessionsResponse
  aggregatesGap?: string
  headroom: boolean
}) {
  if (data.state === 'denied') {
    return (
      <div className="rounded-lg border border-dashed border-theme-border p-4">
        <div className="flex items-center gap-2 text-sm font-medium text-theme-text-primary">
          <Lock className="h-4 w-4" />
          Blocking detail needs {data.permission.grant}
        </div>
        <p className="mt-1 text-sm text-theme-text-secondary">
          Who blocks whom is read inside PostgreSQL, which needs exec into the instance.{' '}
          {aggregatesGap
            ? `The aggregate session counts are unavailable too: ${aggregatesGap}.`
            : 'The aggregate counts above come from the metrics exporter and still apply.'}{' '}
          Nothing here is shown as zero.
        </p>
      </div>
    )
  }
  if (data.state !== 'ok') {
    return (
      <>
        <div className="text-sm text-theme-text-tertiary">
          Sessions on {data.pod || 'the primary'} could not be read ({data.state}){data.error ? `: ${data.error}` : ''}.
        </div>
        <Resources namespace={namespace} instances={data.instances} />
      </>
    )
  }
  const sessions = data.sessions ?? []
  const roots = buildBlockingTree(sessions)
  return (
    <>
      {headroom && <Headroom data={data} />}
      <Resources namespace={namespace} instances={data.instances} />
      {roots.length === 0 ? (
        <div className="text-sm text-theme-text-secondary">No session is waiting on another session’s lock on {data.pod}.</div>
      ) : (
        <div className="space-y-2">
          {data.truncated && (
            <div className={clsx('text-xs', toneTextClass('degraded'))}>
              Showing {sessions.length} of {data.involvedTotal} sessions involved in blocking (oldest transactions first).
            </div>
          )}
          {roots.map((r) => (
            <Tree key={`${r.session.pid}`} node={r} depth={0} namespace={namespace} cluster={cluster} data={data} />
          ))}
        </div>
      )}
    </>
  )
}

function Headroom({ data }: { data: CNPGSessionsResponse }) {
  const figure = cnpgConnectionFigure(data)
  if (!figure) return null
  return (
    <div>
      <div className="flex flex-wrap items-baseline gap-x-3 text-sm">
        <span className="text-xs text-theme-text-tertiary">Connections</span>
        <span className={clsx('font-mono', figure.tone && toneTextClass(figure.tone))}>{figure.value}</span>
        <span className="text-xs text-theme-text-secondary">{figure.detail}</span>
      </div>
      <div className="mt-1 h-1.5 overflow-hidden rounded bg-theme-elevated">
        <div className={clsx('h-full', figure.tone ? toneFillClass(figure.tone) : 'bg-accent')} style={{ width: `${Math.min(100, (figure.ratio ?? 0) * 100)}%` }} />
      </div>
    </div>
  )
}

function Resources({ namespace, instances }: { namespace: string; instances: CNPGSessionInstance[] }) {
  // A missing metrics API is cluster-wide: one instance's answer stands for
  // all, so it is said once rather than on every card.
  const probe = usePodMetrics(namespace, instances[0]?.pod ?? '')
  if (instances.length === 0) return null
  if (probe.data === null) {
    return <div className="text-xs text-theme-text-tertiary">CPU and memory not measured: the metrics API (metrics-server) is not available.</div>
  }
  return (
    <div>
      <div className="mb-1 text-xs text-theme-text-tertiary">CPU and memory of each instance’s postgres container (metrics-server)</div>
      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {instances.map((i) => (
          <InstanceResources key={i.pod} namespace={namespace} inst={i} />
        ))}
      </div>
    </div>
  )
}

function usage(value: number, limit: number | undefined): { pct?: number; tone?: 'degraded' | 'unhealthy' } {
  if (!limit) return {}
  const pct = value / limit
  return { pct, tone: pct >= 0.9 ? 'unhealthy' : pct >= 0.75 ? 'degraded' : undefined }
}

function InstanceResources({ namespace, inst }: { namespace: string; inst: CNPGSessionInstance }) {
  const m = usePodMetrics(namespace, inst.pod)
  const c = m.data?.containers.find((x) => x.name === 'postgres')
  let body: ReactNode
  if (m.isLoading) body = <span className="text-theme-text-tertiary">reading…</span>
  else if (m.data === null) body = <span className="text-theme-text-tertiary">not measured: the metrics API (metrics-server) is not available</span>
  else if (!c) body = <span className="text-theme-text-tertiary">not measured{m.error instanceof Error ? `: ${m.error.message}` : ''}</span>
  else {
    const cpu = usage(parseCPUToNanocores(c.usage.cpu), inst.cpuLimit ? parseCPUToNanocores(inst.cpuLimit) : undefined)
    const mem = usage(parseMemoryToBytes(c.usage.memory), inst.memoryLimit ? parseMemoryToBytes(inst.memoryLimit) : undefined)
    body = (
      <span className="font-mono text-xs">
        <span className={cpu.tone && toneTextClass(cpu.tone)}>
          CPU {formatCPUString(c.usage.cpu)}
          {inst.cpuLimit ? ` / ${formatCPUString(inst.cpuLimit)}` : ' (no limit)'}
        </span>
        {' · '}
        <span className={mem.tone && toneTextClass(mem.tone)}>
          mem {formatMemoryString(c.usage.memory)}
          {inst.memoryLimit ? ` / ${formatMemoryString(inst.memoryLimit)}` : ' (no limit)'}
        </span>
      </span>
    )
  }
  return (
    <div className="rounded-lg border border-theme-border bg-theme-base px-3 py-2 text-sm">
      <div className="font-mono text-xs font-semibold">
        {inst.pod} <span className="font-sans font-normal text-theme-text-tertiary">{inst.role}</span>
      </div>
      <div className="mt-0.5">{body}</div>
    </div>
  )
}

function Tree({ node, depth, namespace, cluster, data }: { node: BlockingNode; depth: number; namespace: string; cluster: string; data: CNPGSessionsResponse }) {
  return (
    <div className={clsx(depth > 0 && 'ml-5 border-l border-theme-border pl-3')}>
      <SessionRow node={node} depth={depth} namespace={namespace} cluster={cluster} data={data} />
      {node.children.map((c) => (
        <Tree key={`${node.session.pid}-${c.session.pid}`} node={c} depth={depth + 1} namespace={namespace} cluster={cluster} data={data} />
      ))}
    </div>
  )
}

function SessionRow({ node, depth, namespace, cluster, data }: { node: BlockingNode; depth: number; namespace: string; cluster: string; data: CNPGSessionsResponse }) {
  const s = node.session
  const [signal, setSignal] = useState<'cancelBackend' | 'terminateBackend' | null>(null)
  const victims = countVictims(node)
  const waiting = s.waitEventType === 'Lock'
  return (
    <div className="my-1 rounded-lg border border-theme-border bg-theme-base p-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
        <span className="font-mono font-semibold">pid {s.pid}</span>
        {depth === 0 && victims > 0 && <span className={clsx('text-xs font-medium', toneTextClass('unhealthy'))}>blocks {victims}</span>}
        {waiting && <span className={clsx('text-xs', toneTextClass('degraded'))}>waiting on {s.waitEvent ?? 'a lock'}</span>}
        {node.cycle && <span className={clsx('text-xs', toneTextClass('unhealthy'))}>lock cycle</span>}
        {node.alsoWaitsOn.length > 0 && <span className="text-xs text-theme-text-tertiary">also waits on {node.alsoWaitsOn.join(', ')}</span>}
        <span className={clsx('text-xs', s.state?.startsWith('idle in transaction') ? toneTextClass('degraded') : 'text-theme-text-secondary')}>{s.state ?? '—'}</span>
        <span className="ml-auto flex gap-3 text-xs">
          <Tooltip content="pg_cancel_backend: stops the statement it is running now. An idle-in-transaction session is running nothing, so it keeps its locks.">
            <button type="button" className="text-accent-text hover:underline" onClick={() => setSignal('cancelBackend')}>
              Stop query
            </button>
          </Tooltip>
          <Tooltip content="pg_terminate_backend: ends the session and rolls back its open transaction.">
            <button type="button" className="text-accent-text hover:underline" onClick={() => setSignal('terminateBackend')}>
              Terminate…
            </button>
          </Tooltip>
        </span>
      </div>
      <div className="mt-1 flex flex-wrap gap-x-3 text-xs text-theme-text-secondary">
        <span className="font-mono">
          {s.user ?? '—'}@{s.database ?? '—'}
        </span>
        {s.application && <span>{s.application}</span>}
        {s.clientAddr && <span className="font-mono">{s.clientAddr}</span>}
        {s.waitEventType && !waiting && (
          <span>
            wait {s.waitEventType}/{s.waitEvent}
          </span>
        )}
        <span>transaction {age(s.xactAgeSeconds)}</span>
        <span>query {age(s.queryAgeSeconds)}</span>
        <span>connected {age(s.backendAgeSeconds)}</span>
      </div>
      {s.query && (
        <pre className="mt-1.5 whitespace-pre-wrap break-all rounded bg-theme-elevated px-2 py-1 font-mono text-[11.5px] text-theme-text-primary">
          {s.query}
          {s.queryTruncated ? ' …' : ''}
        </pre>
      )}
      {signal && <SignalDialog kind={signal} session={s} namespace={namespace} cluster={cluster} data={data} onClose={() => setSignal(null)} />}
    </div>
  )
}

function SignalDialog({
  kind,
  session,
  namespace,
  cluster,
  data,
  onClose,
}: {
  kind: 'cancelBackend' | 'terminateBackend'
  session: CNPGBackend
  namespace: string
  cluster: string
  data: CNPGSessionsResponse
  onClose: () => void
}) {
  const caps = useCNPGClusterCapabilities(namespace, cluster)
  const mutation = useCNPGAction('clusters', namespace, cluster)
  const { showSuccess } = useToast()
  const terminate = kind === 'terminateBackend'
  const fn = terminate ? 'pg_terminate_backend' : 'pg_cancel_backend'
  const idle = session.state?.startsWith('idle in transaction')
  const ctx = caps.data?.context
  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={() => {
        if (!caps.data) return
        mutation.mutate(
          {
            action: kind,
            request: {
              reviewedContext: caps.data.context,
              uid: caps.data.uid,
              facts: {},
              params: { pod: data.pod, podUID: data.podUID, pid: session.pid, backendStart: session.backendStart },
            },
            successMessage: '',
          },
          {
            onSuccess: (r) => {
              showSuccess(r.message)
              onClose()
            },
          },
        )
      }}
      title={terminate ? `Terminate backend ${session.pid}?` : `Cancel the query of backend ${session.pid}?`}
      subject={{ kind: 'Cluster', namespace, name: cluster }}
      context={ctx}
      effect={
        terminate
          ? `Ends the session of ${session.user ?? 'this user'} on ${data.pod}. Its open transaction is rolled back — work it did since the transaction began is lost — and its locks are released. The client sees its connection close.`
          : `Stops the statement backend ${session.pid} is running on ${data.pod}. The client gets an error for that statement; the session and its transaction stay open.`
      }
      warnings={[
        !terminate && idle ? 'This session is idle in a transaction: it is running no statement, so cancelling does nothing and it keeps its locks. Terminate it to release them.' : null,
        terminate && session.xactAgeSeconds !== undefined ? `The transaction has been open for ${age(session.xactAgeSeconds)}.` : null,
      ].filter(Boolean) as string[]}
      notes={[
        `Sent only if backend ${session.pid} still has the start time you reviewed (${session.backendStart}); a reused pid is a different session and is left alone.`,
      ]}
      writes={[
        {
          summary: `exec psql in Pod ${namespace}/${data.pod} (container postgres)`,
          detail: `SELECT ${fn}(pid) FROM pg_stat_activity\nWHERE pid = ${session.pid} AND backend_start = '${session.backendStart}'\n  AND backend_type = 'client backend'`,
        },
      ]}
      typedConfirmation={terminate ? String(session.pid) : undefined}
      confirmLabel={terminate ? 'Terminate backend' : 'Stop query'}
      disruptive={terminate}
      disabledReason={caps.data && data.permission.exec === 'denied' ? `Needs ${data.permission.grant}` : undefined}
      incompleteReason={!caps.data ? 'Reading the cluster…' : undefined}
      isLoading={mutation.isPending}
      error={mutation.error?.message}
      outcomeUnknown={cnpgActionOutcomeLocked(mutation.error)}
    />
  )
}
