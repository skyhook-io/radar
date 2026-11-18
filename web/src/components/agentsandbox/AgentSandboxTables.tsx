import { useMemo } from 'react'
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
import { claimSandboxName, poolClaims, sandboxController } from './lists'
import { CANT_SCHEDULE, objectKey, type ServerReadings } from './labels'
import type { KindRead } from './data'
import { readInNamespace, readItems } from './data'
import { AGENT_SANDBOX_EXTENSIONS_GROUP, AGENT_SANDBOX_GROUP, SANDBOX_CLAIM_KIND, SANDBOX_KIND, SANDBOX_WARM_POOL_KIND } from './routes'
import {
  AGENT_SANDBOX_WIDE,
  AgentSandboxCoverageNotice,
  AgentSandboxHeader,
  Dash,
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

/** The reading, with what explains it under it. */
function StateCell({ o, status, requested, level }: { o: any; status: StatusBadge; requested?: string; level?: StatusBadge['level'] }) {
  const message = stateMessage(o, status)
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
  return created ? <span className="text-sm text-theme-text-secondary">{formatAge(created)}</span> : <Dash />
}

/** Narrow: the expiry with the age under it. */
function ExpiryAgeCell({ o }: { o: any }) {
  const created = o?.metadata?.creationTimestamp
  return (
    <>
      <ExpiryCell resource={o} />
      {created && <Sub>{formatAge(created)} old</Sub>}
    </>
  )
}

export function SandboxesTable({ read, claims, onInspect, inspected, title, server }: { read: KindRead; claims: any[]; onInspect: (r: SelectedResource) => void; inspected: SelectedResource | null; title?: string; server?: ServerReadings }) {
  const rows = useMemo(() => byReading(readItems(read), getSandboxStatus), [read])
  const multi = manyNamespaces(rows)
  // A Sandbox a claim names but doesn't own (adopted from a warm pool) still belongs to that claim.
  const claimFor = useMemo(() => {
    const m = new Map<string, any>()
    for (const c of claims) {
      const name = claimSandboxName(c)
      if (name) m.set(`${ns(c)}/${name}`, c)
    }
    return m
  }, [claims])
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
              if (!owner) return <Dash />
              return (
                <>
                  <RowLink refTo={owner} onInspect={onInspect} />
                  <Sub>{ownerWord(owner)}</Sub>
                </>
              )
            },
          },
          { header: 'Expiry', width: '16%', cell: (o: any) => <ExpiryCell resource={o} /> },
          { header: 'Age', width: '8%', cell: (o: any) => <AgeCell o={o} /> },
        ]
      : [{ header: 'Expiry, age', width: '24%', cell: (o: any) => <ExpiryAgeCell o={o} /> }]),
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
      empty={coverageEmptyText(read, 'Sandboxes')}
      minWidth={wide ? 760 : 560}
    />
  )
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
    if (pool === '-') return <Dash />
    const ref = { kind: 'SandboxWarmPool', group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace: ns(o), name: pool }
    // A pool Radar didn't find is named, not linked: "not found" only where
    // every warm pool in the namespace was read, else "not read".
    if (!pools.has(`${ns(o)}/${pool}`)) {
      return (
        <>
          <ObjectName name={pool} />
          <Sub>{readInNamespace(warmPools, ns(o)) ? 'not found' : 'not read'}</Sub>
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
    { header: 'State', width: wide ? '28%' : '34%', cell: (o) => <StateCell o={o} status={getSandboxClaimStatus(o)} level={server?.tones.get(objectKey('SandboxClaim', ns(o), nm(o)))} /> },
    { header: 'Warm pool', width: wide ? '16%' : '18%', cell: poolCell },
    ...(wide
      ? [
          {
            header: 'Sandbox',
            width: '16%',
            cell: (o: any) => {
              const sb = sandboxOf(o)
              return sb ? <RowLink refTo={sb} onInspect={onInspect} /> : <Dash />
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
        return t === '-' ? <Dash /> : <RowLink refTo={{ kind: 'SandboxTemplate', group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace: ns(o), name: t }} onInspect={onInspect} />
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

function CreateButton({ onCreate, label }: { onCreate?: () => void; label: string }) {
  if (!onCreate) return null
  return (
    <button type="button" onClick={onCreate} className="btn-secondary px-2.5 py-1 text-xs">
      Create {label}
    </button>
  )
}

export function SandboxesScreen({ data, namespaces, onClearNamespaces, onInspect, inspected, onCreate, server }: AgentSandboxScreenProps & { onCreate?: () => void; server?: ServerReadings }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <AgentSandboxHeader title="Sandboxes" namespaces={namespaces} onClearNamespaces={onClearNamespaces} actions={<CreateButton onCreate={onCreate} label={SANDBOX_KIND.kind} />} />
      <ScreenBody>
        <AgentSandboxCoverageNotice data={data} kinds={['sandboxes', 'claims']} />
        <SandboxesTable read={data.sandboxes} claims={readItems(data.claims)} onInspect={onInspect} inspected={inspected} server={server} />
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
