import { useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Tooltip, type StatusBadge } from '@skyhook-io/k8s-ui'
import { formatAge, summarizeSchedulerMessage } from '@skyhook-io/k8s-ui/components/resources/resource-utils'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import {
  getAgentRuntimeStatus,
  getReadyOfDesired,
  getSandboxClaimStatus,
  getSandboxClaimWarmPool,
  getSandboxOperatingMode,
  getSandboxStatus,
  getSandboxWarmPoolStatus,
  getSandboxWarmPoolTemplate,
} from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import type { SelectedResource } from '../../types'
import { ScreenBody, SectionTable, Sub, type TableColumn } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { claimSandboxName, poolClaims, sandboxController } from './lists'
import { CANT_SCHEDULE, notReadyMembers, objectKey, schedulerReason, type ServerReadings } from './labels'
import type { KindRead } from './data'
import { readInNamespace, readItems } from './data'
import { AGENT_SANDBOX_EXTENSIONS_GROUP, AGENT_SANDBOX_GROUP, SANDBOXES_PATH, SANDBOX_CLAIM_KIND, SANDBOX_KIND, SANDBOX_WARM_POOL_KIND } from './routes'
import {
  AGENT_SANDBOX_WIDE,
  AgentSandboxCoverageNotice,
  AgentSandboxHeader,
  Absent,
  ExpiryCell,
  ObjectName,
  ReadingBadge,
  RowLink,
  agentSandboxResource,
  coverageEmptyText,
  type AgentSandboxScreenProps,
} from './shared'

const ns = (o: any): string => o?.metadata?.namespace ?? ''
const nm = (o: any): string => o?.metadata?.name ?? ''
const rowKey = (o: any) => `${ns(o)}/${nm(o)}`
const manyNamespaces = (rows: any[]) => new Set(rows.map(ns)).size > 1

const RANK: Record<StatusBadge['level'], number> = { unhealthy: 0, alert: 1, degraded: 2, unknown: 3, healthy: 4, neutral: 5 }

/** Rows that need a look first, then by namespace and name. */
function byReading(rows: any[], status: (row: any) => StatusBadge): any[] {
  return [...rows].sort((a, b) => {
    const r = RANK[status(a).level] - RANK[status(b).level]
    if (r !== 0) return r
    return rowKey(a).localeCompare(rowKey(b))
  })
}


function NameCell({ o, multi }: { o: any; multi: boolean }) {
  return (
    <>
      <ObjectName name={nm(o)} />
      {multi && <Sub>{ns(o)}</Sub>}
    </>
  )
}

/**
 * What the user asked for in spec.operatingMode, only where the state hasn't
 * caught up with it: a suspension asked for and not done, or a resume. An
 * expired or failed Sandbox stays "Running" in its spec, which says nothing.
 */
export function requestedMode(o: any): string | undefined {
  const mode = getSandboxOperatingMode(o)
  const text = getSandboxStatus(o).text
  // Suspending and Suspension blocked already say a suspension was asked for.
  if (mode === 'Suspended' && !['Suspended', 'Suspending', 'Suspension blocked'].includes(text)) return 'suspended'
  if (mode !== 'Suspended' && (text === 'Suspended' || text === 'Suspending')) return mode.toLowerCase()
  return undefined
}

const letters = (t: string) => t.toLowerCase().replace(/[^a-z]/g, '')

/**
 * The line under a reading: the scheduler's verdict in plain words when the
 * Pod can't be placed, else the controller's message. Nothing for a settled
 * state ("Sandbox has expired") or one that only repeats the badge ("Pod failed").
 */
export function stateMessage(o: any, status: StatusBadge): string | undefined {
  if (status.level === 'healthy' || status.level === 'neutral') return undefined
  const scheduled = Array.isArray(o?.status?.conditions) ? o.status.conditions.find((c: any) => c?.type === 'PodScheduled') : undefined
  if (scheduled?.status === 'False' && scheduled.reason === status.text && scheduled.message) {
    const plain = summarizeSchedulerMessage(scheduled.message, { plain: true })
    if (plain) return `${plain[0].toUpperCase()}${plain.slice(1)}`
  }
  const message = getAgentRuntimeStatus(o)?.message
  if (!message || letters(message) === letters(status.text)) return undefined
  return message
}

/** The reading, with what explains it under it, unless another column already says it. */
function StateCell({ o, status, requested, level, hideMessage }: { o: any; status: StatusBadge; requested?: string; level?: StatusBadge['level']; hideMessage?: boolean }) {
  const message = hideMessage ? undefined : stateMessage(o, status)
  return (
    <>
      <span className="inline-flex flex-wrap items-center gap-x-1.5">
        <ReadingBadge status={status} level={level} />
        {requested && <span className="text-xs text-theme-text-tertiary">requested {requested}</span>}
      </span>
      {message && (
        <Sub>
          <Tooltip content={message}><span className="line-clamp-2">{message}</span></Tooltip>
        </Sub>
      )}
    </>
  )
}

function AgeCell({ o }: { o: any }) {
  const created = o?.metadata?.creationTimestamp
  return created ? <span className="text-sm text-theme-text-secondary">{formatAge(created)}</span> : <Absent>unknown</Absent>
}

/** Narrow: the expiry with the age under it. */
function ExpiryAgeCell({ o, claimed }: { o: any; claimed?: boolean }) {
  const created = o?.metadata?.creationTimestamp
  return (
    <>
      <ExpiryCell resource={o} claimed={claimed} />
      {created && <Sub>{formatAge(created)} old</Sub>}
    </>
  )
}

/**
 * What owns a Sandbox, from its owner references: a claim or a warm pool,
 * else what else owns it, else nothing. "standalone" only where every claim
 * in its namespace was read, since a claim can name a Sandbox it doesn't own.
 */
export function OwnerAbsent({ o, claims }: { o: any; claims: KindRead }) {
  const refs: any[] = Array.isArray(o?.metadata?.ownerReferences) ? o.metadata.ownerReferences : []
  const other = refs.find((r) => r?.controller) ?? refs[0]
  if (other?.kind && other?.name) {
    return (
      <>
        <ObjectName name={other.name} />
        <Sub>{other.kind}</Sub>
      </>
    )
  }
  if (readInNamespace(claims, ns(o))) return <Absent title="No warm pool or claim owns it, and no claim names it.">standalone</Absent>
  return <Absent title="No warm pool or claim owns it. Radar couldn’t read every claim here, so one may name it.">no warm pool; claims not read</Absent>
}

export function SandboxesTable({ read, claims: claimsRead, onInspect, inspected, title, server, empty }: { read: KindRead; claims: KindRead; onInspect: (r: SelectedResource) => void; inspected: SelectedResource | null; title?: string; server?: ServerReadings; empty?: string }) {
  const rows = useMemo(() => byReading(readItems(read), getSandboxStatus), [read])
  const multi = manyNamespaces(rows)
  // A Sandbox a claim names but doesn't own (adopted from a warm pool) still belongs to that claim.
  const claimFor = useMemo(() => {
    const m = new Map<string, any>()
    for (const c of readItems(claimsRead)) {
      const name = claimSandboxName(c)
      if (name) m.set(`${ns(c)}/${name}`, c)
    }
    return m
  }, [claimsRead])
  const wide = useMediaQuery(AGENT_SANDBOX_WIDE)
  const ownerOf = (o: any) => {
    const claim = claimFor.get(rowKey(o))
    return claim ? { kind: 'SandboxClaim', group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace: ns(o), name: nm(claim) } : sandboxController(o)
  }
  const ownerWord = (owner: { kind: string }) => (owner.kind === 'SandboxClaim' ? 'claim' : 'warm pool')
  // Narrow: the claim or pool under the name, the age under the expiry.
  const columns: TableColumn<any>[] = [
    {
      header: 'Sandbox',
      width: wide ? '22%' : '30%',
      cell: (o) => {
        const owner = wide ? undefined : ownerOf(o)
        return (
          <>
            <NameCell o={o} multi={multi} />
            {owner && (
              <Sub>
                {ownerWord(owner)} <RowLink refTo={owner} onInspect={onInspect} inline />
              </Sub>
            )}
            {!wide && !owner && (
              <Sub>
                <OwnerAbsent o={o} claims={claimsRead} />
              </Sub>
            )}
          </>
        )
      },
    },
    { header: 'State', width: wide ? '36%' : '46%', cell: (o) => <StateCell o={o} status={getSandboxStatus(o)} requested={requestedMode(o)} level={server?.tones.get(objectKey('Sandbox', ns(o), nm(o)))} /> },
    ...(wide
      ? [
          {
            header: 'Claim or pool',
            width: '18%',
            cell: (o: any) => {
              const owner = ownerOf(o)
              if (!owner) return <OwnerAbsent o={o} claims={claimsRead} />
              return (
                <>
                  <RowLink refTo={owner} onInspect={onInspect} />
                  <Sub>{ownerWord(owner)}</Sub>
                </>
              )
            },
          },
          { header: 'Expiry', width: '16%', cell: (o: any) => <ExpiryCell resource={o} claimed={ownerOf(o)?.kind === 'SandboxClaim'} /> },
          { header: 'Age', width: '8%', cell: (o: any) => <AgeCell o={o} /> },
        ]
      : [{ header: 'Expiry, age', width: '24%', cell: (o: any) => <ExpiryAgeCell o={o} claimed={ownerOf(o)?.kind === 'SandboxClaim'} /> }]),
  ]
  return (
    <SectionTable
      title={title}
      columns={columns}
      rows={rows}
      rowKey={rowKey}
      rowResource={(o) => agentSandboxResource({ kind: 'Sandbox', group: AGENT_SANDBOX_GROUP, namespace: ns(o), name: nm(o) })}
      onInspect={onInspect}
      inspected={inspected}
      empty={empty ?? coverageEmptyText(read, 'Sandboxes')}
      minWidth={wide ? 760 : 560}
    />
  )
}

/** The claim's state is the controller's WarmPoolNotFound. */
export function reportsMissingPool(o: any): boolean {
  return getSandboxClaimStatus(o).text === 'WarmPoolNotFound'
}

export function ClaimsTable({ read, warmPools, onInspect, inspected, server }: { read: KindRead; warmPools: KindRead; onInspect: (r: SelectedResource) => void; inspected: SelectedResource | null; server?: ServerReadings }) {
  const rows = useMemo(() => byReading(readItems(read), getSandboxClaimStatus), [read])
  const multi = manyNamespaces(rows)
  const pools = useMemo(() => new Set(readItems(warmPools).map(rowKey)), [warmPools])
  const wide = useMediaQuery(AGENT_SANDBOX_WIDE)
  const sandboxOf = (o: any) => {
    const name = claimSandboxName(o)
    return name ? { kind: 'Sandbox', group: AGENT_SANDBOX_GROUP, namespace: ns(o), name } : undefined
  }
  const poolCell = (o: any) => {
    const pool = getSandboxClaimWarmPool(o)
    if (pool === '-') return <Absent>no warm pool</Absent>
    const ref = { kind: 'SandboxWarmPool', group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace: ns(o), name: pool }
    // A pool Radar didn't find is named, not linked: "not found" only where
    // every warm pool in the namespace was read, else "not read". Where the
    // claim's state already says the pool is missing, its name is enough.
    if (!pools.has(`${ns(o)}/${pool}`)) {
      const absent = readInNamespace(warmPools, ns(o))
      return (
        <>
          <ObjectName name={pool} />
          {!(absent && reportsMissingPool(o)) && <Sub>{absent ? 'not found' : 'not read'}</Sub>}
        </>
      )
    }
    return <RowLink refTo={ref} onInspect={onInspect} />
  }
  // Narrow: the bound Sandbox under the name, the age under the expiry.
  const columns: TableColumn<any>[] = [
    {
      header: 'Claim',
      width: wide ? '20%' : '28%',
      cell: (o) => {
        const sb = wide ? undefined : sandboxOf(o)
        return (
          <>
            <NameCell o={o} multi={multi} />
            {sb && (
              <Sub>
                sandbox <RowLink refTo={sb} onInspect={onInspect} inline />
              </Sub>
            )}
          </>
        )
      },
    },
    // A missing warm pool reads once, as the state, and its name sits in its own column; the controller's sentence is in the drawer.
    { header: 'State', width: wide ? '28%' : '34%', cell: (o) => <StateCell o={o} status={getSandboxClaimStatus(o)} level={server?.tones.get(objectKey('SandboxClaim', ns(o), nm(o)))} hideMessage={reportsMissingPool(o)} /> },
    { header: 'Warm pool', width: wide ? '16%' : '18%', cell: poolCell },
    ...(wide
      ? [
          {
            header: 'Sandbox',
            width: '16%',
            cell: (o: any) => {
              const sb = sandboxOf(o)
              return sb ? <RowLink refTo={sb} onInspect={onInspect} /> : <Absent title="The claim’s status names no Sandbox now.">not bound</Absent>
            },
          },
          { header: 'Expiry', width: '13%', cell: (o: any) => <ExpiryCell resource={o} /> },
          { header: 'Age', width: '7%', cell: (o: any) => <AgeCell o={o} /> },
        ]
      : [{ header: 'Expiry, age', width: '20%', cell: (o: any) => <ExpiryAgeCell o={o} /> }]),
  ]
  return (
    <SectionTable
      columns={columns}
      rows={rows}
      rowKey={rowKey}
      rowResource={(o) => agentSandboxResource({ kind: 'SandboxClaim', group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace: ns(o), name: nm(o) })}
      onInspect={onInspect}
      inspected={inspected}
      empty={coverageEmptyText(read, 'SandboxClaims')}
      minWidth={wide ? 760 : 560}
    />
  )
}

export function WarmPoolsTable({
  read,
  claims,
  claimsRead,
  onInspect,
  inspected,
  title,
  server,
}: {
  read: KindRead
  claims: any[]
  claimsRead: KindRead
  onInspect: (r: SelectedResource) => void
  inspected: SelectedResource | null
  title?: string
  /** What the server's summary says about these pools. */
  server?: ServerReadings
}) {
  const rows = useMemo(() => byReading(readItems(read), getSandboxWarmPoolStatus), [read])
  const multi = manyNamespaces(rows)
  const wide = useMediaQuery(AGENT_SANDBOX_WIDE)
  // Narrow: ready of desired under the state, the age under the name.
  const columns: TableColumn<any>[] = [
    {
      header: 'Warm pool',
      width: wide ? '24%' : '28%',
      cell: (o) => (
        <>
          <NameCell o={o} multi={multi} />
          {!wide && o?.metadata?.creationTimestamp && <Sub>{formatAge(o.metadata.creationTimestamp)} old</Sub>}
        </>
      ),
    },
    {
      header: 'State',
      width: wide ? '16%' : '24%',
      cell: (o) => (
        <>
          <PoolStateBadge o={o} server={server} />
          <PoolStateDetail o={o} server={server} />
          {!wide && <Sub>{getReadyOfDesired(o)} ready</Sub>}
        </>
      ),
    },
    ...(wide
      ? [
          {
            header: 'Ready',
            width: '12%',
            cell: (o: any) => (
              <>
                <span className="font-mono text-sm">{getReadyOfDesired(o)}</span>
                <Sub>of desired</Sub>
              </>
            ),
          },
        ]
      : []),
    {
      header: 'Template',
      width: wide ? '18%' : '24%',
      cell: (o) => {
        const t = getSandboxWarmPoolTemplate(o)
        return t === '-' ? <Absent>no template</Absent> : <RowLink refTo={{ kind: 'SandboxTemplate', group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace: ns(o), name: t }} onInspect={onInspect} />
      },
    },
    {
      header: 'Claims served',
      width: wide ? '18%' : '24%',
      cell: (o) => {
        if (!readInNamespace(claimsRead, ns(o))) return <span className="text-sm text-theme-text-tertiary">unknown</span>
        const served = poolClaims(o, claims)
        if (served.length === 0) return <span className="text-sm text-theme-text-tertiary">none</span>
        const bound = served.filter((c) => claimSandboxName(c)).length
        return (
          <>
            <span className="text-sm">{served.length}</span>
            <Sub>{bound} bound</Sub>
          </>
        )
      },
    },
    ...(wide ? [{ header: 'Age', width: '10%', cell: (o: any) => <AgeCell o={o} /> }] : []),
  ]
  return (
    <SectionTable
      title={title}
      columns={columns}
      rows={rows}
      rowKey={rowKey}
      rowResource={(o) => agentSandboxResource({ kind: 'SandboxWarmPool', group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace: ns(o), name: nm(o) })}
      onInspect={onInspect}
      inspected={inspected}
      empty={coverageEmptyText(read, 'SandboxWarmPools')}
      minWidth={wide ? 760 : 560}
    />
  )
}

/** A pool's reading, "Can’t schedule" instead of "Warming" when the server says its sandboxes can't be placed, colored as the server's row about it. */
function PoolStateBadge({ o, server }: { o: any; server?: ServerReadings }) {
  const status = getSandboxWarmPoolStatus(o)
  const level = server?.tones.get(objectKey('SandboxWarmPool', ns(o), nm(o)))
  if (status.text === 'Warming' && server?.unschedulable.has(rowKey(o))) {
    return <ReadingBadge status={status} label={CANT_SCHEDULE} level={level} title="Warming, but the scheduler can’t place its sandboxes, so it isn’t refilling" />
  }
  return <ReadingBadge status={status} level={level} />
}

/** The Sandboxes list narrowed to one warm pool's sandboxes that aren't ready. */
export function poolNotReadyPath(namespace: string, name: string): string {
  return `${SANDBOXES_PATH}&pool=${encodeURIComponent(`${namespace}/${name}`)}&state=notReady`
}

/**
 * Under a pool's state, from the server's row about it: the scheduler's
 * verdict in plain words (its own message on hover, in full in the drawer),
 * and the sandboxes it names as not ready, as a link to them.
 */
function PoolStateDetail({ o, server }: { o: any; server?: ServerReadings }) {
  const navigate = useWorkspaceNavigate()
  const row = server?.rows.get(objectKey('SandboxWarmPool', ns(o), nm(o)))
  const reason = schedulerReason(row)
  const members = notReadyMembers(row)
  return (
    <>
      {reason && (
        <Sub>
          <Tooltip content={reason.raw}><span className="line-clamp-2">{reason.plain}</span></Tooltip>
        </Sub>
      )}
      {members.length > 0 && (
        <Sub>
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation()
              navigate(poolNotReadyPath(ns(o), nm(o)))
            }}
            onKeyDown={(e) => e.stopPropagation()}
            className="text-accent-text hover:underline"
          >
            {members.length} {members.length === 1 ? 'sandbox' : 'sandboxes'} not ready
          </button>
        </Sub>
      )}
    </>
  )
}

function CreateButton({ onCreate, label }: { onCreate?: () => void; label: string }) {
  if (!onCreate) return null
  return (
    <button type="button" onClick={onCreate} className="btn-secondary px-2.5 py-1 text-xs">
      Create {label}
    </button>
  )
}

/**
 * The Sandboxes list's own filters, from the URL: `pool` (namespace/name)
 * keeps one warm pool's sandboxes, `state=notReady` those whose reading
 * isn't Ready. A warm pool's "not ready" link sets both.
 */
export function filterSandboxes(read: KindRead, pool: string, state: string): KindRead {
  if (read.state !== 'read' || (!pool && state !== 'notReady')) return read
  const items = read.items.filter((o) => {
    if (pool) {
      const owner = sandboxController(o)
      if (owner?.kind !== 'SandboxWarmPool' || `${owner.namespace}/${owner.name}` !== pool) return false
    }
    return state !== 'notReady' || getSandboxStatus(o).level !== 'healthy'
  })
  return { ...read, items }
}

export function SandboxesScreen({ data, namespaces, onClearNamespaces, onInspect, inspected, onCreate, server }: AgentSandboxScreenProps & { onCreate?: () => void; server?: ServerReadings }) {
  const [params, setParams] = useSearchParams()
  const pool = params.get('pool') ?? ''
  const state = params.get('state') === 'notReady' ? 'notReady' : ''
  const clear = (key: string) => {
    const p = new URLSearchParams(params)
    p.delete(key)
    setParams(p, { replace: true })
  }
  const chips = [
    ...(pool ? [{ label: `Warm pool: ${pool.slice(pool.indexOf('/') + 1)}`, onClear: () => clear('pool') }] : []),
    ...(state ? [{ label: 'Not ready', onClear: () => clear('state') }] : []),
  ]
  const sandboxes = useMemo(() => filterSandboxes(data.sandboxes, pool, state), [data.sandboxes, pool, state])
  const filtered = chips.length > 0 && sandboxes.state === 'read' && sandboxes.items.length === 0
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <AgentSandboxHeader title="Sandboxes" namespaces={namespaces} onClearNamespaces={onClearNamespaces} chips={chips} actions={<CreateButton onCreate={onCreate} label={SANDBOX_KIND.kind} />} />
      <ScreenBody>
        <AgentSandboxCoverageNotice data={data} kinds={['sandboxes', 'claims']} />
        <SandboxesTable read={sandboxes} claims={data.claims} onInspect={onInspect} inspected={inspected} server={server} empty={filtered ? 'No Sandboxes match these filters.' : undefined} />
      </ScreenBody>
    </div>
  )
}

export function ClaimsScreen({ data, namespaces, onClearNamespaces, onInspect, inspected, onCreate, server }: AgentSandboxScreenProps & { onCreate?: () => void; server?: ServerReadings }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <AgentSandboxHeader title="Claims" namespaces={namespaces} onClearNamespaces={onClearNamespaces} actions={<CreateButton onCreate={onCreate} label={SANDBOX_CLAIM_KIND.kind} />} />
      <ScreenBody>
        <AgentSandboxCoverageNotice data={data} kinds={['claims', 'warmPools']} />
        <ClaimsTable read={data.claims} warmPools={data.warmPools} onInspect={onInspect} inspected={inspected} server={server} />
      </ScreenBody>
    </div>
  )
}

export function WarmPoolsScreen({ data, namespaces, onClearNamespaces, onInspect, inspected, onCreate, server }: AgentSandboxScreenProps & { onCreate?: () => void; server?: ServerReadings }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <AgentSandboxHeader title="Warm pools" namespaces={namespaces} onClearNamespaces={onClearNamespaces} actions={<CreateButton onCreate={onCreate} label={SANDBOX_WARM_POOL_KIND.kind} />} />
      <ScreenBody>
        <AgentSandboxCoverageNotice data={data} kinds={['warmPools', 'claims']} />
        <WarmPoolsTable read={data.warmPools} claims={readItems(data.claims)} claimsRead={data.claims} onInspect={onInspect} inspected={inspected} server={server} />
      </ScreenBody>
    </div>
  )
}
