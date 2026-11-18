import type { ReactNode } from 'react'
import { Boxes } from 'lucide-react'
import { Badge, PaneLoader, Tooltip, healthToSeverity, type StatusBadge } from '@skyhook-io/k8s-ui'
import { formatSandboxExpiry, getSandboxExpiry } from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import { resourceCoverageNotice, resourceEmptyMessage, resourceNarrowedNotice } from '@skyhook-io/k8s-ui/utils/resource-coverage'
import type { SelectedResource } from '../../types'
import { useConnection } from '../../context/ConnectionContext'
import { refToSelectedResource } from '../../utils/navigation'
import { FilterChips, Notice, RefreshFailedNotice, ScreenEmptyState, namespaceChip } from '../workspace/layout'
import type { AgentSandboxListKey, AgentSandboxLists, KindRead } from './data'
import { readingLabel } from './labels'

/** A kind list's props: the three kinds as the resource list endpoint read them. */
export interface AgentSandboxScreenProps {
  data: AgentSandboxLists
  /** The namespace filter the reads applied; empty for all namespaces. */
  namespaces: string[]
  onClearNamespaces: () => void
  onInspect: (resource: SelectedResource) => void
  inspected: SelectedResource | null
}

export const AGENT_SANDBOX_LABEL = 'Agent Sandbox'

export const KIND_LABEL: Record<AgentSandboxListKey | 'templates', string> = {
  sandboxes: 'Sandboxes',
  claims: 'SandboxClaims',
  warmPools: 'SandboxWarmPools',
  templates: 'SandboxTemplates',
}

export function agentSandboxResource(ref: { kind: string; group: string; namespace: string; name: string }): SelectedResource {
  return refToSelectedResource(ref)
}

/** The title line, and the namespace filter and the screen's own filters as chips when they narrow it. */
export function AgentSandboxHeader({ title, namespaces, onClearNamespaces, actions, chips: filters = [] }: { title: string; namespaces: string[]; onClearNamespaces: () => void; actions?: ReactNode; chips?: { label: string; onClear: () => void }[] }) {
  const chips = [...namespaceChip(namespaces, onClearNamespaces), ...filters]
  return (
    <div className="px-5 pt-4 xl:px-7">
      <div className="flex flex-wrap items-center gap-x-2">
        <span className="flex items-center gap-1.5 text-sm text-theme-text-tertiary">
          <Boxes className="h-3.5 w-3.5" />
          {AGENT_SANDBOX_LABEL}
          <span>/</span>
        </span>
        <h1 className="text-lg font-semibold text-theme-text-primary">{title}</h1>
        {actions && <div className="ml-auto">{actions}</div>}
      </div>
      {chips.length > 0 && (
        <div className="mt-2">
          <FilterChips chips={chips} />
        </div>
      )}
    </div>
  )
}

/** Why a kind was not read in full, in a few words; undefined when it was. */
export function readGapText(read: KindRead): string | undefined {
  switch (read.state) {
    case 'loading':
      return 'still loading'
    case 'failed':
      return read.denied ? 'no access' : `couldn’t be read: ${read.message}`
    case 'read':
      return resourceCoverageNotice(read.coverage, 'objects')?.replace(/^Partial results\. /, '') ?? resourceNarrowedNotice(read.coverage)
  }
  return undefined
}

/** Says which kinds this screen reads were not read in full, and why. What wasn't read shows as unknown. */
export function AgentSandboxCoverageNotice({ data, kinds }: { data: AgentSandboxLists; kinds: readonly AgentSandboxListKey[] }) {
  const gaps = kinds.flatMap((k) => {
    const text = readGapText(data[k])
    return text ? [`${KIND_LABEL[k]} (${text.replace(/\.$/, '')})`] : []
  })
  if (gaps.length === 0) return null
  return <Notice>Radar couldn’t read all of the Agent Sandbox objects on this screen: {gaps.join('; ')}. What it couldn’t read shows as unknown.</Notice>
}

/**
 * Loading, error and not-installed states the kind lists share. The screen
 * renders once any kind it reads has an answer; a kind that failed is named
 * on the screen rather than blocking it.
 */
export function AgentSandboxGate({
  data,
  keys,
  discoveryPending,
  discovered,
  isFetching,
  refetch,
  refreshQueries,
  children,
}: {
  data: AgentSandboxLists
  /** The kinds this screen needs at least one answer from. */
  keys: readonly AgentSandboxListKey[]
  discoveryPending: boolean
  discovered: boolean
  isFetching: boolean
  refetch: () => void
  refreshQueries: { isRefetchError: boolean; error: Error | null; dataUpdatedAt: number }[]
  children: ReactNode
}) {
  const { connection } = useConnection()
  const reads = keys.map((k) => data[k])
  // One kind still loading doesn't hold the screen once another has answered; the notice names it.
  if (discoveryPending || (reads.some((r) => r.state === 'loading') && reads.every((r) => r.state === 'loading' || r.state === 'notServed'))) return <PaneLoader label="Loading Agent Sandbox…" className="flex-1" />
  if (!discovered) {
    return <ScreenEmptyState icon={Boxes} title="Agent Sandbox is not installed" detail={`No agents.x-k8s.io resources are served in ${connection.context || 'this cluster'}.`} />
  }
  if (reads.every((r) => r.state === 'notServed')) {
    return <ScreenEmptyState icon={Boxes} title="Not served by this Agent Sandbox" detail={`Agent Sandbox is installed in ${connection.context || 'this cluster'}, but its version doesn't serve this kind.`} />
  }
  if (reads.every((r) => r.state === 'failed' || r.state === 'notServed')) {
    const failed = reads.find((r): r is Extract<KindRead, { state: 'failed' }> => r.state === 'failed')
    const denied = reads.every((r) => r.state !== 'failed' || r.denied)
    return (
      <ScreenEmptyState
        icon={Boxes}
        title={denied ? 'No access to Agent Sandbox objects' : 'Agent Sandbox data unavailable'}
        detail={denied ? 'Your permissions don’t allow listing them in the namespaces you picked.' : failed?.message ?? 'Radar couldn’t load the Agent Sandbox objects.'}
        action={
          denied ? undefined : (
            <button type="button" onClick={refetch} disabled={isFetching} className="btn-secondary mt-3 px-3 py-1.5 text-sm">
              {isFetching ? 'Retrying…' : 'Retry'}
            </button>
          )
        }
      />
    )
  }
  return (
    <>
      <RefreshFailedNotice queries={refreshQueries} className="px-5 pt-3 xl:px-7" />
      {children}
    </>
  )
}

/**
 * A reading as a badge, colored by how much it needs attention, in plain
 * words; the raw reason the controller or Kubernetes reported is on hover.
 * `level` colors it as the server's Needs attention row about it does.
 */
export function ReadingBadge({ status, label: override, title, level }: { status: StatusBadge; label?: string; title?: string; level?: StatusBadge['level'] }) {
  const { label, reason } = readingLabel(status)
  const badge = (
    <Badge severity={healthToSeverity(level ?? status.level)} size="sm">
      {override ?? label}
    </Badge>
  )
  const tip = title ?? (reason ? `Reason: ${reason}` : undefined)
  return tip ? <Tooltip content={tip}>{badge}</Tooltip> : badge
}

/** An object's name in a table: mono, one line, cut with an ellipsis and whole on hover. */
export function ObjectName({ name }: { name: string }) {
  return (
    <Tooltip content={name}>
      <span className="block min-w-0 truncate font-mono text-sm">{name}</span>
    </Tooltip>
  )
}

/** A link inside a table row that opens its own object, not the row's. */
export function RowLink({ refTo, onInspect, children, inline }: { refTo: { kind: string; group: string; namespace: string; name: string }; onInspect: (r: SelectedResource) => void; children?: ReactNode; inline?: boolean }) {
  return (
    <Tooltip content={`${refTo.kind} ${refTo.name}`} wrapperClassName="min-w-0 max-w-full">
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation()
          onInspect(agentSandboxResource(refTo))
        }}
        onKeyDown={(e) => e.stopPropagation()}
        className={inline ? 'max-w-full truncate text-left font-mono text-accent-text hover:underline' : 'block max-w-full truncate text-left font-mono text-sm text-accent-text hover:underline'}
      >
        {children ?? refTo.name}
      </button>
    </Tooltip>
  )
}

/**
 * The width the Agent Sandbox tables show every column at, the same as the
 * Substrate tables'. Below it each table folds its secondary columns into the
 * cell they qualify, so nothing scrolls sideways.
 */
export { SUBSTRATE_WIDE as AGENT_SANDBOX_WIDE } from '../substrate/shared'

/** Absence in a table cell or fact, in words: "no expiry", "not bound", "unknown". */
export function Absent({ children, title }: { children: ReactNode; title?: string }) {
  const text = <span className="text-sm text-theme-text-tertiary">{children}</span>
  return title ? <Tooltip content={title}>{text}</Tooltip> : text
}

/**
 * Words a controller, the scheduler or the kubelet wrote, verbatim and set
 * apart from Radar's own: labelled, in mono, behind a rule.
 */
/**
 * Text that wraps only at its spaces. Each word is kept whole, since a break at
 * a hyphen inside an image reference or URL ("does-not-" / "exist:1") reads as
 * two words; one wider than the line still wraps within itself.
 */
export function wrapAtSpaces(text: string): ReactNode[] {
  return text
    .split(/(\s+)/)
    .filter(Boolean)
    .map((part, i) => (/^\s/.test(part) ? part : <span key={i} className="inline-block max-w-full align-top">{part}</span>))
}

export function SourceText({ label, text }: { label?: string; text: string }) {
  return (
    <div className="min-w-0 text-xs">
      {label && <div className="text-theme-text-tertiary">{label}</div>}
      <div className="mt-0.5 whitespace-pre-wrap break-words border-l-2 border-theme-border pl-2 font-mono text-[11.5px] text-theme-text-secondary">{wrapAtSpaces(text)}</div>
    </div>
  )
}

/** "expires in 12m", "expired 2h ago", or undefined when none is set; the exact time on hover. */
export function expiryText(value: string | undefined, now = Date.now()): string | undefined {
  if (!value) return undefined
  const at = Date.parse(value)
  if (!Number.isFinite(at)) return value
  return `${at >= now ? 'expires' : 'expired'} ${formatSandboxExpiry(value, now)}`
}

/**
 * The object's own spec.shutdownTime. With none set, a claim or a Sandbox
 * nothing claims has no expiry; a claimed Sandbox may still end with its
 * claim, so it says only that it sets none of its own.
 */
export function ExpiryCell({ resource, claimed }: { resource: any; claimed?: boolean }) {
  const value = getSandboxExpiry(resource)
  const text = expiryText(value)
  if (!text || !value) {
    return claimed ? <Absent title="This Sandbox sets no shutdownTime; its claim’s expiry can still end it.">none of its own</Absent> : <Absent>no expiry</Absent>
  }
  return (
    <Tooltip content={new Date(value).toLocaleString()}>
      <span className="text-sm text-theme-text-secondary">{text}</span>
    </Tooltip>
  )
}

/** A table's empty text: what was read, so an unread kind never reads as none. */
export function coverageEmptyText(read: KindRead, kind: string): string {
  switch (read.state) {
    case 'loading':
      return `${kind} are still loading.`
    case 'failed':
      return read.denied ? `Unknown: no access to ${kind} here.` : `Unknown: ${kind} couldn’t be read (${read.message}).`
    case 'notServed':
      return `${kind} aren’t served in this cluster: their CRD isn’t installed.`
  }
  return `${resourceEmptyMessage(kind, read.coverage, false)}.`
}
