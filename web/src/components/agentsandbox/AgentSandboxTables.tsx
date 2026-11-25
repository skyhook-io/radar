import { useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Plus } from 'lucide-react'
import { SearchBox, Tooltip, toneTextClass, type StatusBadge } from '@skyhook-io/k8s-ui'
import { formatAge, summarizeSchedulerMessage } from '@skyhook-io/k8s-ui/components/resources/resource-utils'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import {
  getAgentRuntimeStatus,
  getSandboxWarmPoolReadyOfDesired,
  getSandboxClaimStatus,
  getSandboxClaimWarmPool,
  getSandboxExpiry,
  getSandboxOperatingMode,
  getSandboxStatus,
  getSandboxWarmPoolStatus,
  getSandboxWarmPoolTemplate,
  sandboxPodName,
} from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import type { SelectedResource } from '../../types'
import { ScreenBody, SectionTable, Segments, Sub, type TableColumn } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { claimSandboxName, poolClaims, sandboxController } from './lists'
import { notReadyMembers, objectKey, schedulerReason, warmPoolBadge, type ServerReadings } from './labels'
import type { KindRead } from './data'
import { readInNamespace, readItems } from './data'
import { AGENT_SANDBOX_EXTENSIONS_GROUP, AGENT_SANDBOX_GROUP, SANDBOXES_PATH, SANDBOX_CLAIMS_PATH, SANDBOX_CLAIM_KIND, SANDBOX_KIND, SANDBOX_WARM_POOL_KIND } from './routes'
import { useListFilters } from './listFilters'
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

function singleNamespaceOf(rows: any[]): string | undefined {
  const seen = new Set(rows.map(ns).filter(Boolean))
  return seen.size === 1 ? [...seen][0] : undefined
}

function isPartialRead(read: KindRead): boolean {
  return read.state === 'read' && read.coverage.status === 'partial'
}

const countBadge = (n: number, partial: boolean) => (partial ? `≥${n}` : n)

const RANK: Record<StatusBadge['level'], number> = { unhealthy: 0, alert: 1, degraded: 2, unknown: 3, healthy: 4, neutral: 5 }

const INSPECTED_NAME_WIDTH = 'clamp(9rem, 11.25vw, 13rem)'
const INSPECTED_STATE_WIDTH = 'clamp(10.5rem, 13vw, 16rem)'

function useTableLayout(inspected: SelectedResource | null) {
  const wide = useMediaQuery(AGENT_SANDBOX_WIDE)
  // A secondary column takes spare width so the fixed columns stay to the left of the overlay.
  const roomy = useMediaQuery('(min-width: 1600px)')
  return { wide, compact: !!inspected && !roomy }
}

/** Rows that need a look first (using the server's escalated tone when present), then by namespace and name. */
function byReading(rows: any[], levelOf: (row: any) => StatusBadge['level']): any[] {
  return [...rows].sort((a, b) => {
    const r = RANK[levelOf(a)] - RANK[levelOf(b)]
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
 * One readable reason under a reading in a table row: the scheduler's verdict
 * in plain words, a concise suspension-conflict summary, a stripped container
 * waiting reason, or the controller's message. Nothing for a settled state.
 */
export function stateMessage(o: any, status: StatusBadge): string | undefined {
  if (status.level === 'healthy' || status.level === 'neutral') return undefined
  const scheduled = Array.isArray(o?.status?.conditions) ? o.status.conditions.find((c: any) => c?.type === 'PodScheduled') : undefined
  if (scheduled?.status === 'False' && scheduled.reason === status.text && scheduled.message) {
    const plain = summarizeSchedulerMessage(scheduled.message, { plain: true })
    if (plain) return `${plain[0].toUpperCase()}${plain.slice(1)}`
  }
  if (status.text === 'Suspension blocked') {
    const podName = sandboxPodName(o)
    return podName ? `Conflicting Pod "${podName}" is not owned by this Sandbox` : 'Conflicting Pod is not owned by this Sandbox'
  }
  if (status.text === 'PodFailed') {
    return 'Pod exited with failure.'
  }
  const message = getAgentRuntimeStatus(o)?.message
  if (!message || letters(message) === letters(status.text)) return undefined
  // Strip noisy controller boilerplate so container waiting reasons stay short and readable.
  const stripped = message.replace(/^Pod exists with phase:\s*Pending;\s*/i, '')
  return stripped ? `${stripped[0].toUpperCase()}${stripped.slice(1)}` : message
}

/** The reading, with what explains it under it, unless another column already says it. */
function StateCell({ o, status, requested, level, hideMessage, compact }: { o: any; status: StatusBadge; requested?: string; level?: StatusBadge['level']; hideMessage?: boolean; compact: boolean }) {
  const message = hideMessage ? undefined : stateMessage(o, status)
  const rawMessage = hideMessage ? undefined : getAgentRuntimeStatus(o)?.message
  return (
    <>
      <span className="inline-flex flex-wrap items-center gap-x-1.5">
        <ReadingBadge status={status} level={level} />
        {requested && <span className="text-xs text-theme-text-tertiary">requested {requested}</span>}
      </span>
      {message && (
        <Sub>
          <Tooltip content={rawMessage || message}><span className="break-words">{compact && status.text === 'Suspension blocked' ? 'Sandbox doesn’t own the Pod' : message}</span></Tooltip>
        </Sub>
      )}
    </>
  )
}

function AgeCell({ o }: { o: any }) {
  const created = o?.metadata?.creationTimestamp
  return created ? <span className="text-sm text-theme-text-secondary">{formatAge(created)}</span> : <Absent>unknown</Absent>
}

function OpenCell() {
  return <span className="whitespace-nowrap text-xs font-medium text-accent-text">Open →</span>
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
  const rows = useMemo(
    () => byReading(readItems(read), (o) => server?.tones.get(objectKey('Sandbox', ns(o), nm(o))) ?? getSandboxStatus(o).level),
    [read, server],
  )
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
  const { wide, compact } = useTableLayout(inspected)
  const ownerOf = (o: any) => {
    const claim = claimFor.get(rowKey(o))
    return claim ? { kind: 'SandboxClaim', group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace: ns(o), name: nm(claim) } : sandboxController(o)
  }
  const ownerWord = (owner: { kind: string }) => (owner.kind === 'SandboxClaim' ? 'claim' : 'warm pool')
  // Narrow: the claim or pool under the name, the age under the expiry.
  const columns: TableColumn<any>[] = [
    {
      header: 'Sandbox',
      width: compact ? INSPECTED_NAME_WIDTH : wide ? '21%' : '29%',
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
    { header: 'State', width: compact ? INSPECTED_STATE_WIDTH : wide ? '33%' : '41%', cell: (o) => <StateCell o={o} status={getSandboxStatus(o)} requested={requestedMode(o)} level={server?.tones.get(objectKey('Sandbox', ns(o), nm(o)))} compact={compact} /> },
    ...(wide
      ? [
          {
            header: 'Claim or pool',
            width: compact ? undefined : '17%',
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
          { header: 'Expiry', width: '14%', cell: (o: any) => <ExpiryCell resource={o} claimed={ownerOf(o)?.kind === 'SandboxClaim'} /> },
          { header: 'Age', width: '8%', cell: (o: any) => <AgeCell o={o} /> },
        ]
      : [{ header: 'Expiry, age', width: compact ? undefined : '21%', cell: (o: any) => <ExpiryAgeCell o={o} claimed={ownerOf(o)?.kind === 'SandboxClaim'} /> }]),
    { header: '', width: wide ? '7%' : '9%', className: 'text-right', cell: () => <OpenCell /> },
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
      empty={empty ?? coverageEmptyText(read, 'Sandboxes', 'sandboxes')}
      minWidth={wide ? 760 : 560}
    />
  )
}

/** The claim's state is the controller's WarmPoolNotFound. */
export function reportsMissingPool(o: any): boolean {
  return getSandboxClaimStatus(o).text === 'WarmPoolNotFound'
}

export function ClaimsTable({ read, warmPools, onInspect, inspected, server, empty }: { read: KindRead; warmPools: KindRead; onInspect: (r: SelectedResource) => void; inspected: SelectedResource | null; server?: ServerReadings; empty?: string }) {
  const rows = useMemo(
    () => byReading(readItems(read), (o) => server?.tones.get(objectKey('SandboxClaim', ns(o), nm(o))) ?? getSandboxClaimStatus(o).level),
    [read, server],
  )
  const multi = manyNamespaces(rows)
  const pools = useMemo(() => new Set(readItems(warmPools).map(rowKey)), [warmPools])
  const { wide, compact } = useTableLayout(inspected)
  const sandboxOf = (o: any) => {
    const name = claimSandboxName(o)
    return name ? { kind: 'Sandbox', group: AGENT_SANDBOX_GROUP, namespace: ns(o), name } : undefined
  }
  const poolCell = (o: any) => {
    const pool = getSandboxClaimWarmPool(o)
    if (pool === '-') return <Absent>no warm pool</Absent>
    const ref = { kind: 'SandboxWarmPool', group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace: ns(o), name: pool }
    // A pool Radar didn't find is named, not linked: "not found" only where
    // every warm pool in the namespace was read, else "not read".
    if (!pools.has(`${ns(o)}/${pool}`)) {
      const absent = readInNamespace(warmPools, ns(o))
      return (
        <>
          <ObjectName name={pool} />
          <Sub>
            <span className={absent ? toneTextClass('unhealthy') : undefined}>{absent ? 'not found' : 'not read'}</span>
          </Sub>
        </>
      )
    }
    return <RowLink refTo={ref} onInspect={onInspect} />
  }
  const bindingCell = (o: any) => {
    const sb = sandboxOf(o)
    if (sb) return <RowLink refTo={sb} onInspect={onInspect} />
    if (getSandboxClaimStatus(o).text === 'Expired') {
      return <Absent title="This claim expired without a bound Sandbox listed in status.">not bound (expired)</Absent>
    }
    return <Absent title="The claim’s status names no Sandbox now.">not bound</Absent>
  }

  const columns: TableColumn<any>[] = [
    { header: 'Claim', width: compact ? INSPECTED_NAME_WIDTH : wide ? '19%' : '20%', cell: (o) => <NameCell o={o} multi={multi} /> },
    { header: 'State', width: compact ? INSPECTED_STATE_WIDTH : wide ? '25%' : '30%', cell: (o) => <StateCell o={o} status={getSandboxClaimStatus(o)} level={server?.tones.get(objectKey('SandboxClaim', ns(o), nm(o)))} hideMessage={reportsMissingPool(o)} compact={compact} /> },
    { header: 'Warm pool & binding', width: compact ? undefined : wide ? '31%' : '24%', cell: (o) => (
      <div className="space-y-1">
        {poolCell(o)}
        <Sub>{bindingCell(o)}</Sub>
      </div>
    ) },
    ...(wide
      ? [
          { header: 'Expiry', width: '11%', cell: (o: any) => <ExpiryCell resource={o} /> },
          { header: 'Age', width: '7%', cell: (o: any) => <AgeCell o={o} /> },
        ]
      : [{ header: 'Expiry, age', width: '17%', cell: (o: any) => <ExpiryAgeCell o={o} /> }]),
    { header: '', width: wide ? '7%' : '9%', className: 'text-right', cell: () => <OpenCell /> },
  ]
  return (
    <SectionTable
      columns={columns}
      rows={rows}
      rowKey={rowKey}
      rowResource={(o) => agentSandboxResource({ kind: 'SandboxClaim', group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace: ns(o), name: nm(o) })}
      onInspect={onInspect}
      inspected={inspected}
      empty={empty ?? coverageEmptyText(read, 'SandboxClaims', 'claims')}
      minWidth={wide ? 760 : 560}
    />
  )
}

/** The Claims list narrowed to claims referencing one warm pool. */
export function poolClaimsPath(namespace: string, name: string): string {
  return `${SANDBOX_CLAIMS_PATH}&pool=${encodeURIComponent(`${namespace}/${name}`)}`
}

export function WarmPoolsTable({
  read,
  claims,
  claimsRead,
  onInspect,
  inspected,
  title,
  server,
  empty,
}: {
  read: KindRead
  claims: any[]
  claimsRead: KindRead
  onInspect: (r: SelectedResource) => void
  inspected: SelectedResource | null
  title?: string
  /** What the server's summary says about these pools. */
  server?: ServerReadings
  empty?: string
}) {
  const navigate = useWorkspaceNavigate()
  const rows = useMemo(
    () => byReading(readItems(read), (o) => server?.tones.get(objectKey('SandboxWarmPool', ns(o), nm(o))) ?? getSandboxWarmPoolStatus(o).level),
    [read, server],
  )
  const multi = manyNamespaces(rows)
  const { wide, compact } = useTableLayout(inspected)
  // Narrow: ready of desired under the state, the age under the name.
  const columns: TableColumn<any>[] = [
    {
      header: 'Warm pool',
      width: compact ? INSPECTED_NAME_WIDTH : wide ? '19%' : '24%',
      cell: (o) => (
        <>
          <NameCell o={o} multi={multi} />
          {!wide && o?.metadata?.creationTimestamp && <Sub>{formatAge(o.metadata.creationTimestamp)} old</Sub>}
        </>
      ),
    },
    {
      header: 'State',
      width: compact ? INSPECTED_STATE_WIDTH : wide ? '24%' : '28%',
      cell: (o) => (
        <>
          <PoolStateBadge o={o} server={server} />
          <PoolStateDetail o={o} server={server} compact={compact} />
          {!wide && <Sub>{getSandboxWarmPoolReadyOfDesired(o)} ready</Sub>}
        </>
      ),
    },
    ...(wide
      ? [
          {
            header: 'Ready',
            width: '10%',
            cell: (o: any) => (
              <>
                <span className="font-mono text-sm">{getSandboxWarmPoolReadyOfDesired(o)}</span>
                <Sub>of desired</Sub>
              </>
            ),
          },
        ]
      : []),
    {
      header: 'Template',
      width: compact ? undefined : wide ? '17%' : '20%',
      cell: (o) => {
        const t = getSandboxWarmPoolTemplate(o)
        if (t === '-') return <Absent>no template</Absent>
        const key = objectKey('SandboxWarmPool', ns(o), nm(o))
        const pool = server?.warmPools.get(key)
        const rel = server?.rows.get(key)?.relations?.find((r) => r.role === 'template' && r.ref.name === t)
        const presence = rel?.presence ?? (pool?.template === t ? pool.templatePresence : undefined)
        if (presence !== 'present') {
          return (
            <>
              <ObjectName name={t} />
              <Sub>
                <span className={presence === 'absent' ? toneTextClass('unhealthy') : undefined}>{presence === 'absent' ? 'not found' : 'not read'}</span>
              </Sub>
            </>
          )
        }
        return <RowLink refTo={{ kind: 'SandboxTemplate', group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace: ns(o), name: t }} onInspect={onInspect} />
      },
    },
    {
      header: 'Claims referencing',
      width: wide ? '17%' : '19%',
      cell: (o) => {
        if (!readInNamespace(claimsRead, ns(o))) return <Absent>unknown</Absent>
        const served = poolClaims(o, claims)
        if (served.length === 0) return <Absent>none</Absent>
        const bound = served.filter((c) => claimSandboxName(c) && getSandboxClaimStatus(c).text !== 'Expired').length
        const expired = served.filter((c) => getSandboxClaimStatus(c).text === 'Expired').length
        const detail = expired > 0 ? `${bound} currently bound · ${expired} expired` : `${bound} currently bound`
        return (
          <>
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                navigate(poolClaimsPath(ns(o), nm(o)))
              }}
              onKeyDown={(e) => e.stopPropagation()}
              className="text-left text-sm text-accent-text hover:underline"
            >
              {served.length} referencing
            </button>
            <Sub>{detail}</Sub>
          </>
        )
      },
    },
    ...(wide ? [{ header: 'Age', width: '7%', cell: (o: any) => <AgeCell o={o} /> }] : []),
    { header: '', width: wide ? '7%' : '9%', className: 'text-right', cell: () => <OpenCell /> },
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
      empty={empty ?? coverageEmptyText(read, 'SandboxWarmPools', 'warmPools')}
      minWidth={wide ? 760 : 560}
    />
  )
}

/** One canonical warm pool badge reading, shared with the drawer/page header pill. */
function PoolStateBadge({ o, server }: { o: any; server?: ServerReadings }) {
  const b = warmPoolBadge(o, server)
  return <ReadingBadge status={b.status} label={b.label} level={b.level} title={b.title} />
}

/** The Sandboxes list narrowed to one warm pool's sandboxes that aren't ready. */
export function poolNotReadyPath(namespace: string, name: string): string {
  return `${SANDBOXES_PATH}&pool=${encodeURIComponent(`${namespace}/${name}`)}&state=notReady`
}

/**
 * Under a pool's state, from the server's row about it: the cause (e.g.
 * scheduler verdict in plain words) and the sandboxes it names as not ready.
 */
function PoolStateDetail({ o, server, compact }: { o: any; server?: ServerReadings; compact: boolean }) {
  const navigate = useWorkspaceNavigate()
  const row = server?.rows.get(objectKey('SandboxWarmPool', ns(o), nm(o)))
  const reason = schedulerReason(row)
  const members = notReadyMembers(row)
  return (
    <>
      {reason && (
        <Sub>
          <Tooltip content={reason.raw}><span>{compact ? reason.plain : `Can’t schedule · ${reason.plain}`}</span></Tooltip>
        </Sub>
      )}
      {!reason && row?.rule === 'reserveBelowTarget' && row.reading && (
        <Sub>
          <Tooltip content={row.reading}><span>{row.reading}</span></Tooltip>
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
            Show all members that aren’t ready
          </button>
        </Sub>
      )}
    </>
  )
}

function CreateButton({ onCreate, label }: { onCreate?: () => void; label: string }) {
  if (!onCreate) return null
  return (
    <button type="button" onClick={onCreate} className="btn-secondary inline-flex items-center gap-1.5 px-2.5 py-1 text-xs">
      <Plus className="h-3.5 w-3.5" />
      Create {label}
    </button>
  )
}

/**
 * The Sandboxes list's own filters, from the URL: `pool` (namespace/name)
 * keeps one warm pool's sandboxes, `state=notReady` those whose reading
 * isn't Ready, `state=expired` those whose reading is Expired.
 */
export function filterSandboxes(read: KindRead, pool: string, state: string): KindRead {
  if (read.state !== 'read' || (!pool && state !== 'notReady' && state !== 'expired' && state !== 'recentlyExpired')) return read
  const items = read.items.filter((o) => {
    if (pool) {
      const owner = sandboxController(o)
      if (owner?.kind !== 'SandboxWarmPool' || `${owner.namespace}/${owner.name}` !== pool) return false
    }
    if (state === 'notReady') return getSandboxStatus(o).level !== 'healthy'
    if (state === 'expired') return getSandboxStatus(o).text === 'Expired'
    if (state === 'recentlyExpired') return getSandboxStatus(o).text === 'Expired' && recentlyExpired(o)
    return true
  })
  return { ...read, items }
}

/** Keeps the objects whose name contains `search`, ignoring case. */
function recentlyExpired(o: any): boolean {
  const expiry = getSandboxExpiry(o)
  if (!expiry) return false
  const elapsed = Date.now() - Date.parse(expiry)
  return elapsed >= 0 && elapsed <= 7 * 24 * 60 * 60 * 1000
}

export function filterByName(read: KindRead, search: string): KindRead {
  const q = search.trim().toLowerCase()
  if (read.state !== 'read' || !q) return read
  return { ...read, items: read.items.filter((o) => nm(o).toLowerCase().includes(q)) }
}

function filterItems(read: KindRead, predicate: (o: any) => boolean): KindRead {
  if (read.state !== 'read') return read
  return { ...read, items: read.items.filter(predicate) }
}

/** The list's name search, kept in the Resources page's own `search` URL param so its links narrow these lists too. */
function useNameSearch(): [string, (value: string) => void] {
  const [params, setParams] = useSearchParams()
  const set = (value: string) => {
    const p = new URLSearchParams(params)
    if (value) p.set('search', value)
    else p.delete('search')
    setParams(p, { replace: true })
  }
  return [params.get('search') ?? '', set]
}

function useUrlParam<T extends string>(key: string, valid: readonly T[], fallback: T): [T, (value: T) => void] {
  const [params, setParams] = useSearchParams()
  const raw = params.get(key)
  const value = raw && (valid as readonly string[]).includes(raw) ? (raw as T) : fallback
  const set = (next: T) => {
    const p = new URLSearchParams(params)
    if (next === fallback) p.delete(key)
    else p.set(key, next)
    setParams(p, { replace: true })
  }
  return [value, set]
}

/** What an empty list says when its filters, not the cluster, emptied it. */
function noMatchText(noun: string, search: string, narrowed: boolean, read: KindRead): string | undefined {
  if (read.state !== 'read' || read.items.length > 0) return undefined
  if (narrowed) return `No ${noun} match these filters.`
  return search.trim() ? `No ${noun} match this search.` : undefined
}

function NameSearch({ value, onChange, noun, id }: { value: string; onChange: (value: string) => void; noun: string; id: string }) {
  return <SearchBox value={value} onChange={onChange} scope="resources" shortcutId={id} placeholder={`Search ${noun} by name`} className="w-64 max-w-full" />
}

function PoolSelect({
  pools,
  value,
  onChange,
  multiNamespace,
}: {
  pools: { key: string; namespace: string; name: string }[]
  value: string
  onChange: (value: string) => void
  multiNamespace: boolean
}) {
  if (pools.length === 0) return null
  return (
    <select
      aria-label="Filter by warm pool"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="h-8 rounded-lg border border-theme-border bg-theme-elevated px-2.5 text-xs text-theme-text-primary focus:outline-none focus:ring-2 focus:ring-accent"
    >
      <option value="">All warm pools</option>
      {pools.map((p) => (
        <option key={p.key} value={p.key}>
          {multiNamespace ? `${p.namespace}/${p.name}` : p.name}
        </option>
      ))}
    </select>
  )
}

function listSubtitle(count: number, noun: [string, string], partial: boolean, namespaces: string[], items: any[], attentionCount: number): string {
  const prefix = partial ? '≥' : ''
  const parts = [`${prefix}${count} ${count === 1 ? noun[0] : noun[1]}`]
  if (attentionCount > 0) parts.push(`${prefix}${attentionCount} need attention`)
  if (namespaces.length > 0) parts.push(`in ${namespaces.join(', ')}`)
  else {
    const single = singleNamespaceOf(items)
    if (single) parts.push(`in namespace ${single}`)
  }
  return parts.join(' · ')
}

const SANDBOX_SEGMENTS = ['all', 'attention', 'ready', 'settled'] as const
type SandboxSegment = (typeof SANDBOX_SEGMENTS)[number]

function isSandboxAttention(o: any, server?: ServerReadings): boolean {
  const level = server?.tones.get(objectKey('Sandbox', ns(o), nm(o))) ?? getSandboxStatus(o).level
  return level !== 'healthy' && level !== 'neutral'
}

export function SandboxesScreen({ data, namespaces, onClearNamespaces, onInspect, inspected, onCreate, server }: AgentSandboxScreenProps & { onCreate?: () => void; server?: ServerReadings }) {
  const [params, setParams] = useSearchParams()
  const [search, setSearch] = useNameSearch()
  const [segment, setSegment] = useUrlParam<SandboxSegment>('segment', SANDBOX_SEGMENTS, 'all')
  const pool = params.get('pool') ?? ''
  const rawState = params.get('state')
  const state = rawState === 'notReady' || rawState === 'expired' || rawState === 'recentlyExpired' ? rawState : ''
  const clear = (key: string) => {
    const p = new URLSearchParams(params)
    p.delete(key)
    setParams(p, { replace: true })
  }
  const setPool = (value: string) => {
    const p = new URLSearchParams(params)
    if (value) p.set('pool', value)
    else p.delete('pool')
    setParams(p, { replace: true })
  }
  const listFilters = useListFilters(SANDBOX_KIND)
  const chips = [
    ...(pool ? [{ label: `Warm pool: ${pool.slice(pool.indexOf('/') + 1)}`, onClear: () => clear('pool') }] : []),
    ...(state === 'notReady' ? [{ label: 'Not ready', onClear: () => clear('state') }] : []),
    ...(state === 'expired' || state === 'recentlyExpired' ? [{ label: state === 'expired' ? 'Expired' : 'Expired in the last 7 days', onClear: () => clear('state') }] : []),
    ...listFilters.chips,
  ]
  const { apply } = listFilters
  const baseItems = readItems(data.sandboxes)
  const partial = isPartialRead(data.sandboxes)
  const poolOptions = useMemo(() => {
    const map = new Map<string, { key: string; namespace: string; name: string }>()
    for (const p of readItems(data.warmPools)) {
      const k = rowKey(p)
      map.set(k, { key: k, namespace: ns(p), name: nm(p) })
    }
    for (const s of baseItems) {
      const owner = sandboxController(s)
      if (owner?.kind === 'SandboxWarmPool') {
        const k = `${owner.namespace}/${owner.name}`
        if (!map.has(k)) map.set(k, { key: k, namespace: owner.namespace, name: owner.name })
      }
    }
    return [...map.values()].sort((a, b) => a.key.localeCompare(b.key))
  }, [data.warmPools, baseItems])
  const multiPoolNs = new Set(poolOptions.map((p) => p.namespace)).size > 1

  const counts = useMemo(() => {
    let attention = 0
    let ready = 0
    let settled = 0
    for (const o of baseItems) {
      const level = server?.tones.get(objectKey('Sandbox', ns(o), nm(o))) ?? getSandboxStatus(o).level
      if (level === 'healthy') ready++
      else if (level === 'neutral') settled++
      else attention++
    }
    return { all: baseItems.length, attention, ready, settled }
  }, [baseItems, server])

  const bySegment = useMemo(() => {
    const base = filterSandboxes(data.sandboxes, pool, state)
    if (segment === 'all') return base
    return filterItems(base, (o) => {
      const level = server?.tones.get(objectKey('Sandbox', ns(o), nm(o))) ?? getSandboxStatus(o).level
      if (segment === 'attention') return isSandboxAttention(o, server)
      if (segment === 'ready') return level === 'healthy'
      return level === 'neutral'
    })
  }, [data.sandboxes, pool, state, segment, server])

  const sandboxes = useMemo(() => filterByName(apply(bySegment), search), [bySegment, search, apply])
  const narrowed = !!pool || !!state || segment !== 'all' || listFilters.narrows
  const empty = noMatchText('Sandboxes', search, narrowed, sandboxes)
  const showSegmentCounts = !(search || pool || state || listFilters.narrows)
  const subtitle = data.sandboxes.state === 'read' ? listSubtitle(counts.all, ['sandbox', 'sandboxes'], partial, namespaces, baseItems, counts.attention) : undefined

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <AgentSandboxHeader title="Sandboxes" subtitle={subtitle} namespaces={namespaces} onClearNamespaces={onClearNamespaces} chips={chips} actions={<CreateButton onCreate={onCreate} label={SANDBOX_KIND.kind} />} />
      <ScreenBody>
        <div className="flex flex-wrap items-center gap-2">
          <Segments
            label="Filter sandboxes by state"
            value={segment}
            onChange={setSegment}
            options={[
              { id: 'all', label: 'All', count: showSegmentCounts ? countBadge(counts.all, partial) : undefined },
              { id: 'attention', label: 'Needs attention', count: showSegmentCounts ? countBadge(counts.attention, partial) : undefined },
              { id: 'ready', label: 'Ready', count: showSegmentCounts ? countBadge(counts.ready, partial) : undefined },
              { id: 'settled', label: 'Settled', count: showSegmentCounts ? countBadge(counts.settled, partial) : undefined },
            ]}
          />
          <PoolSelect pools={poolOptions} value={pool} onChange={setPool} multiNamespace={multiPoolNs} />
          <div className="ml-auto">
            <NameSearch value={search} onChange={setSearch} noun="sandboxes" id="agent-sandbox-sandboxes-search" />
          </div>
        </div>
        <AgentSandboxCoverageNotice data={data} kinds={['sandboxes', 'claims']} />
        <SandboxesTable read={sandboxes} claims={data.claims} onInspect={onInspect} inspected={inspected} server={server} empty={empty} />
      </ScreenBody>
    </div>
  )
}

const CLAIM_SEGMENTS = ['all', 'attention', 'bound', 'expired'] as const
type ClaimSegment = (typeof CLAIM_SEGMENTS)[number]

function classifyClaim(o: any): 'attention' | 'bound' | 'expired' {
  const status = getSandboxClaimStatus(o)
  if (status.text === 'Expired') return 'expired'
  return claimSandboxName(o) ? 'bound' : 'attention'
}

function isClaimAttention(o: any, server?: ServerReadings): boolean {
  const level = server?.tones.get(objectKey('SandboxClaim', ns(o), nm(o))) ?? getSandboxClaimStatus(o).level
  return level === 'unhealthy' || level === 'alert' || level === 'degraded'
}

export function ClaimsScreen({ data, namespaces, onClearNamespaces, onInspect, inspected, onCreate, server }: AgentSandboxScreenProps & { onCreate?: () => void; server?: ServerReadings }) {
  const [params, setParams] = useSearchParams()
  const [search, setSearch] = useNameSearch()
  const [segment, setSegment] = useUrlParam<ClaimSegment>('segment', CLAIM_SEGMENTS, 'all')
  const recent = params.get('state') === 'recentlyExpired'
  const pool = params.get('pool') ?? ''
  const setPool = (value: string) => {
    const p = new URLSearchParams(params)
    if (value) p.set('pool', value)
    else p.delete('pool')
    setParams(p, { replace: true })
  }
  const { apply, narrows, chips: listChips } = useListFilters(SANDBOX_CLAIM_KIND)
  const chips = [
    ...(pool ? [{ label: `Warm pool: ${pool.slice(pool.indexOf('/') + 1)}`, onClear: () => setPool('') }] : []),
    ...(recent ? [{ label: 'Expired in the last 7 days', onClear: () => {
      const next = new URLSearchParams(params)
      next.delete('state')
      setParams(next, { replace: true })
    } }] : []),
    ...listChips,
  ]
  const baseItems = readItems(data.claims)
  const partial = isPartialRead(data.claims)
  const poolOptions = useMemo(() => {
    const map = new Map<string, { key: string; namespace: string; name: string }>()
    for (const p of readItems(data.warmPools)) {
      const k = rowKey(p)
      map.set(k, { key: k, namespace: ns(p), name: nm(p) })
    }
    for (const c of baseItems) {
      const ref = getSandboxClaimWarmPool(c)
      if (ref && ref !== '-') {
        const k = `${ns(c)}/${ref}`
        if (!map.has(k)) map.set(k, { key: k, namespace: ns(c), name: ref })
      }
    }
    return [...map.values()].sort((a, b) => a.key.localeCompare(b.key))
  }, [data.warmPools, baseItems])
  const multiPoolNs = new Set(poolOptions.map((p) => p.namespace)).size > 1

  const counts = useMemo(() => {
    let attention = 0
    let bound = 0
    let expired = 0
    for (const o of baseItems) {
      const bucket = classifyClaim(o)
      if (isClaimAttention(o, server)) attention++
      if (bucket === 'bound') bound++
      if (bucket === 'expired') expired++
    }
    return { all: baseItems.length, attention, bound, expired }
  }, [baseItems, server])

  const narrowedRead = useMemo(() => {
    let next = data.claims
    if (pool) {
      next = filterItems(next, (c) => `${ns(c)}/${getSandboxClaimWarmPool(c)}` === pool)
    }
    if (segment !== 'all') {
      next = filterItems(next, (c) => segment === 'attention' ? isClaimAttention(c, server) : classifyClaim(c) === segment)
    }
    if (recent) next = filterItems(next, (c) => getSandboxClaimStatus(c).text === 'Expired' && recentlyExpired(c))
    return next
  }, [data.claims, pool, segment, server, recent])

  const claims = useMemo(() => filterByName(apply(narrowedRead), search), [narrowedRead, search, apply])
  const empty = noMatchText('SandboxClaims', search, narrows || !!pool || recent || segment !== 'all', claims)
  const showSegmentCounts = !(search || pool || recent || narrows)
  const subtitle = data.claims.state === 'read' ? listSubtitle(counts.all, ['claim', 'claims'], partial, namespaces, baseItems, counts.attention) : undefined

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <AgentSandboxHeader title="Claims" subtitle={subtitle} namespaces={namespaces} onClearNamespaces={onClearNamespaces} chips={chips} actions={<CreateButton onCreate={onCreate} label={SANDBOX_CLAIM_KIND.kind} />} />
      <ScreenBody>
        <div className="flex flex-wrap items-center gap-2">
          <Segments
            label="Filter claims by state"
            value={segment}
            onChange={setSegment}
            options={[
              { id: 'all', label: 'All', count: showSegmentCounts ? countBadge(counts.all, partial) : undefined },
              { id: 'attention', label: 'Needs attention', count: showSegmentCounts ? countBadge(counts.attention, partial) : undefined },
              { id: 'bound', label: 'Bound', count: showSegmentCounts ? countBadge(counts.bound, partial) : undefined },
              { id: 'expired', label: 'Expired', count: showSegmentCounts ? countBadge(counts.expired, partial) : undefined },
            ]}
          />
          <PoolSelect pools={poolOptions} value={pool} onChange={setPool} multiNamespace={multiPoolNs} />
          <div className="ml-auto">
            <NameSearch value={search} onChange={setSearch} noun="claims" id="agent-sandbox-claims-search" />
          </div>
        </div>
        <AgentSandboxCoverageNotice data={data} kinds={['claims', 'warmPools']} />
        <ClaimsTable read={claims} warmPools={data.warmPools} onInspect={onInspect} inspected={inspected} server={server} empty={empty} />
      </ScreenBody>
    </div>
  )
}

const WARM_POOL_SEGMENTS = ['all', 'attention', 'ready'] as const
type WarmPoolSegment = (typeof WARM_POOL_SEGMENTS)[number]

function isWarmPoolAttention(o: any, server?: ServerReadings): boolean {
  const key = objectKey('SandboxWarmPool', ns(o), nm(o))
  if (server?.rows.has(key)) return true
  const level = server?.tones.get(key) ?? getSandboxWarmPoolStatus(o).level
  return level !== 'healthy' && level !== 'neutral'
}

export function WarmPoolsScreen({ data, namespaces, onClearNamespaces, onInspect, inspected, onCreate, server }: AgentSandboxScreenProps & { onCreate?: () => void; server?: ServerReadings }) {
  const [search, setSearch] = useNameSearch()
  const [segment, setSegment] = useUrlParam<WarmPoolSegment>('segment', WARM_POOL_SEGMENTS, 'all')
  const { apply, narrows, chips } = useListFilters(SANDBOX_WARM_POOL_KIND)
  const baseItems = readItems(data.warmPools)
  const partial = isPartialRead(data.warmPools)

  const counts = useMemo(() => {
    let attention = 0
    let ready = 0
    for (const o of baseItems) {
      if (isWarmPoolAttention(o, server)) attention++
      else if (getSandboxWarmPoolStatus(o).level === 'healthy') ready++
    }
    return { all: baseItems.length, attention, ready }
  }, [baseItems, server])

  const bySegment = useMemo(() => {
    if (segment === 'all') return data.warmPools
    return filterItems(data.warmPools, (o) =>
      segment === 'attention' ? isWarmPoolAttention(o, server) : !isWarmPoolAttention(o, server) && getSandboxWarmPoolStatus(o).level === 'healthy',
    )
  }, [data.warmPools, segment, server])

  const pools = useMemo(() => filterByName(apply(bySegment), search), [bySegment, search, apply])
  const empty = noMatchText('SandboxWarmPools', search, narrows || segment !== 'all', pools)
  const showSegmentCounts = !(search || narrows)
  const subtitle = data.warmPools.state === 'read' ? listSubtitle(counts.all, ['warm pool', 'warm pools'], partial, namespaces, baseItems, counts.attention) : undefined

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <AgentSandboxHeader title="Warm pools" subtitle={subtitle} namespaces={namespaces} onClearNamespaces={onClearNamespaces} chips={chips} actions={<CreateButton onCreate={onCreate} label={SANDBOX_WARM_POOL_KIND.kind} />} />
      <ScreenBody>
        <div className="flex flex-wrap items-center gap-2">
          <Segments
            label="Filter warm pools by state"
            value={segment}
            onChange={setSegment}
            options={[
              { id: 'all', label: 'All', count: showSegmentCounts ? countBadge(counts.all, partial) : undefined },
              { id: 'attention', label: 'Needs attention', count: showSegmentCounts ? countBadge(counts.attention, partial) : undefined },
              { id: 'ready', label: 'Ready', count: showSegmentCounts ? countBadge(counts.ready, partial) : undefined },
            ]}
          />
          <div className="ml-auto">
            <NameSearch value={search} onChange={setSearch} noun="warm pools" id="agent-sandbox-warm-pools-search" />
          </div>
        </div>
        <AgentSandboxCoverageNotice data={data} kinds={['warmPools', 'claims']} />
        <WarmPoolsTable read={pools} claims={readItems(data.claims)} claimsRead={data.claims} onInspect={onInspect} inspected={inspected} server={server} empty={empty} />
      </ScreenBody>
    </div>
  )
}
