import { useState, useMemo } from 'react'
import { Virtuoso } from 'react-virtuoso'
import type { TrafficFlow } from '../../types'
import { clsx } from 'clsx'
import { ChevronDown, ChevronUp, ShieldCheck } from 'lucide-react'
import { SEVERITY_BADGE, SEVERITY_TEXT } from '@skyhook-io/k8s-ui/utils/badge-colors'
import { pluralize, StatusDot } from '@skyhook-io/k8s-ui'
import type { StatusTone } from '@skyhook-io/k8s-ui'
import { useFlowSearch } from './TrafficFlowListContext'
import { useQuery } from '@tanstack/react-query'
import { fetchJSON } from '../../api/client'
import { Tooltip } from '../ui/Tooltip'

// DNS response code names
const DNS_RCODES: Record<number, string> = {
  0: 'NOERROR',
  1: 'FORMERR',
  2: 'SERVFAIL',
  3: 'NXDOMAIN',
  5: 'REFUSED',
}

function formatLatency(ns: number): string {
  const ms = ns / 1e6
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)}s`
  if (ms >= 1) return `${ms.toFixed(1)}ms`
  return `${(ms * 1000).toFixed(0)}µs`
}

function formatBytes(bytes: number): string {
  if (bytes >= 1_000_000) return `${(bytes / 1_000_000).toFixed(1)}MB`
  if (bytes >= 1_000) return `${(bytes / 1_000).toFixed(1)}KB`
  return `${bytes}B`
}

function statusColor(status: number): string {
  if (status >= 500) return SEVERITY_TEXT.error
  if (status >= 400) return SEVERITY_TEXT.warning
  if (status >= 300) return SEVERITY_TEXT.neutral
  if (status >= 200) return SEVERITY_TEXT.success
  return 'text-theme-text-secondary'
}

const VERDICT_BADGE: Record<string, string> = {
  forwarded: SEVERITY_BADGE.success,
  dropped: SEVERITY_BADGE.error,
  error: SEVERITY_BADGE.warning,
}

type SortField = 'time' | 'latency' | 'status' | 'method' | 'source' | 'destination'
type SortDir = 'asc' | 'desc'

interface TrafficFlowListProps {
  flows: TrafficFlow[]
}

export function TrafficFlowList({ flows }: TrafficFlowListProps) {
  const [search] = useFlowSearch()
  const [sortField, setSortField] = useState<SortField>('time')
  const [sortDir, setSortDir] = useState<SortDir>('desc')
  const [expandedIdx, setExpandedIdx] = useState<number | null>(null)

  const toggleSort = (field: SortField) => {
    if (sortField === field) {
      setSortDir(d => d === 'asc' ? 'desc' : 'asc')
    } else {
      setSortField(field)
      setSortDir(field === 'time' ? 'desc' : 'asc')
    }
  }

  // Deduplicate HTTP REQUEST/RESPONSE pairs: prefer RESPONSE (has status + latency).
  // Keep orphan REQUESTs (no matching RESPONSE) as they indicate missing responses.
  // Deduplicate HTTP REQUEST/RESPONSE pairs: prefer RESPONSE (has status + latency).
  // REQUEST goes client→server, RESPONSE goes server→client (src/dst swapped).
  const deduped = useMemo(() => {
    // RESPONSE goes server→client, REQUEST goes client→server (src/dst swapped).
    // Normalize key: always client|server|method|path
    const responseKeys = new Set<string>()
    for (const f of flows) {
      if (f.l7Protocol === 'HTTP' && f.l7Type === 'RESPONSE') {
        responseKeys.add(`${f.destination.name}|${f.source.name}|${f.httpMethod}|${f.httpPath}`)
      }
    }
    return flows.filter(f => {
      if (f.l7Protocol === 'HTTP' && f.l7Type === 'REQUEST') {
        return !responseKeys.has(`${f.source.name}|${f.destination.name}|${f.httpMethod}|${f.httpPath}`)
      }
      return true
    })
  }, [flows])

  const filtered = useMemo(() => {
    if (!search) return deduped
    const q = search.toLowerCase()
    return deduped.filter(f =>
      f.source.name.toLowerCase().includes(q) ||
      f.destination.name.toLowerCase().includes(q) ||
      f.httpPath?.toLowerCase().includes(q) ||
      f.httpMethod?.toLowerCase().includes(q) ||
      f.dnsQuery?.toLowerCase().includes(q) ||
      f.l7Protocol?.toLowerCase().includes(q) ||
      f.verdict?.toLowerCase().includes(q)
    )
  }, [deduped, search])

  const sorted = useMemo(() => {
    const mult = sortDir === 'asc' ? 1 : -1
    return [...filtered].sort((a, b) => {
      switch (sortField) {
        case 'time': return mult * (a.lastSeen.localeCompare(b.lastSeen))
        case 'latency': return mult * ((a.latencyNs ?? 0) - (b.latencyNs ?? 0))
        case 'status': return mult * ((a.httpStatus ?? 0) - (b.httpStatus ?? 0))
        case 'method': return mult * ((a.httpMethod ?? '').localeCompare(b.httpMethod ?? ''))
        case 'source': return mult * (a.source.name.localeCompare(b.source.name))
        case 'destination': return mult * (a.destination.name.localeCompare(b.destination.name))
        default: return 0
      }
    })
  }, [filtered, sortField, sortDir])

  const SortHeader = ({ field, label, className }: { field: SortField; label: string; className?: string }) => (
    <button
      onClick={() => toggleSort(field)}
      className={clsx('flex items-center gap-0.5 hover:text-theme-text-primary transition-colors', className)}
    >
      {label}
      {sortField === field && (
        sortDir === 'asc' ? <ChevronUp className="w-3 h-3" /> : <ChevronDown className="w-3 h-3" />
      )}
    </button>
  )

  return (
    <div className="absolute inset-0 flex flex-col">
      {/* Table header */}
      <div className="grid grid-cols-[6rem_minmax(0,1fr)_minmax(0,1fr)_minmax(0,2fr)_3.5rem_4rem_5.5rem] items-center gap-x-3 px-3 py-1 border-b border-theme-border text-[10px] text-theme-text-tertiary uppercase tracking-wider font-medium">
        <SortHeader field="time" label="Time" />
        <SortHeader field="source" label="Source" />
        <SortHeader field="destination" label="Destination" />
        <span>Request</span>
        <SortHeader field="status" label="Status" className="justify-end" />
        <SortHeader field="latency" label="Latency" className="justify-end" />
        <span className="text-right">Verdict</span>
      </div>

      {/* Flow rows — virtualized so tens of thousands of Hubble/Cilium flows
          don't all become DOM. Virtuoso measures variable row heights, so the
          expand/collapse panel still works. */}
      {sorted.length === 0 ? (
        <div className="flex-1 flex items-center justify-center text-sm text-theme-text-tertiary">
          {search ? 'No flows match the search' : 'No flows to display'}
        </div>
      ) : (
        <Virtuoso
          className="flex-1"
          data={sorted}
          itemContent={(i, flow) => {
            const isExpanded = expandedIdx === i
            const isHTTP = flow.l7Protocol === 'HTTP'
            const isDNS = flow.l7Protocol === 'DNS'
            const time = flow.lastSeen ? new Date(flow.lastSeen).toLocaleTimeString() : ''

            return (
              <div key={i}>
                <button
                  onClick={() => setExpandedIdx(isExpanded ? null : i)}
                  className={clsx(
                    'w-full grid grid-cols-[6rem_minmax(0,1fr)_minmax(0,1fr)_minmax(0,2fr)_3.5rem_4rem_5.5rem] items-center gap-x-3 px-3 py-1.5 text-xs text-left hover:bg-theme-hover transition-colors border-b border-theme-border/50',
                    isExpanded && 'bg-theme-elevated',
                    flow.verdict === 'dropped' && 'bg-red-500/5',
                    flow.httpStatus && flow.httpStatus >= 500 && 'bg-red-500/5',
                  )}
                >
                  {/* Time */}
                  <span className="text-theme-text-tertiary tabular-nums whitespace-nowrap">{time}</span>

                  {/* Source */}
                  <Tooltip content={flow.source.namespace ? `${flow.source.namespace}/${flow.source.name}` : flow.source.name} wrapperClassName="min-w-0">
                  <span className="truncate text-theme-text-primary">
                    {flow.source.name}
                  </span>
                  </Tooltip>

                  {/* Destination. An unoriented conversation has no caller and no
                      callee — the two ends are ordered arbitrarily — so it is marked
                      rather than presented as a direction the source established. */}
                  <Tooltip
                    content={
                      flow.directionUnknown
                        ? 'Direction unknown: this source could not determine which end opened the conversation'
                        : flow.destination.namespace
                          ? `${flow.destination.namespace}/${flow.destination.name}`
                          : flow.destination.name
                    }
                    wrapperClassName="min-w-0"
                  >
                  <span className="truncate text-theme-text-primary">
                    {flow.destination.name}
                    {flow.directionUnknown && (
                      <span className="ml-1 text-theme-text-tertiary">(direction unknown)</span>
                    )}
                  </span>
                  </Tooltip>

                  {/* Request info */}
                  <div className="flex items-center gap-1.5 min-w-0">
                    {isHTTP && (
                      <>
                        <span className={clsx('shrink-0 badge badge-sm text-[10px]', SEVERITY_BADGE.info)}>{flow.httpMethod}</span>
                        <Tooltip content={flow.httpPath ?? ''} wrapperClassName="min-w-0">
                        <span className="truncate text-theme-text-secondary">{flow.httpPath}</span>
                        </Tooltip>
                        {flow.l7Type === 'REQUEST' && <span className={clsx('shrink-0 text-[9px]', SEVERITY_TEXT.warning)}>no response</span>}
                      </>
                    )}
                    {isDNS && (
                      <>
                        <span className={clsx('shrink-0 badge badge-sm text-[10px]', SEVERITY_BADGE.neutral)}>DNS</span>
                        <Tooltip content={flow.dnsQuery ?? ''} wrapperClassName="min-w-0">
                        <span className="truncate text-theme-text-secondary">{flow.dnsQuery}</span>
                        </Tooltip>
                      </>
                    )}
                    {!isHTTP && !isDNS && (
                      <span className="text-theme-text-tertiary uppercase">{flow.protocol}:{flow.port}</span>
                    )}
                  </div>

                  {/* Status */}
                  <span className={clsx('text-right tabular-nums font-medium whitespace-nowrap',
                    isHTTP && flow.httpStatus ? statusColor(flow.httpStatus) : 'text-theme-text-tertiary'
                  )}>
                    {isHTTP && flow.httpStatus ? flow.httpStatus : isDNS && flow.dnsRCode != null ? (DNS_RCODES[flow.dnsRCode] ?? flow.dnsRCode) : '—'}
                  </span>

                  {/* Latency */}
                  <span className="text-right tabular-nums text-theme-text-secondary whitespace-nowrap">
                    {flow.latencyNs && flow.latencyNs > 0 ? formatLatency(flow.latencyNs) : '—'}
                  </span>

                  {/* Verdict */}
                  <span className={clsx('text-right badge badge-sm text-[10px] capitalize', VERDICT_BADGE[flow.verdict] ?? SEVERITY_BADGE.neutral)}>
                    {flow.verdict}
                  </span>
                </button>

                {/* Expanded detail */}
                {isExpanded && (
                  <div className="px-3 py-2 bg-theme-elevated border-b border-theme-border space-y-2 text-xs">
                    <div className="grid grid-cols-2 gap-x-6 gap-y-1">
                      <div>
                        <span className="text-theme-text-tertiary">Source: </span>
                        <span className="text-theme-text-primary">{flow.source.name}</span>
                        {flow.source.namespace && <span className="text-theme-text-tertiary"> ({flow.source.namespace})</span>}
                        {flow.sourceService && <span className="text-theme-text-tertiary"> via {flow.sourceService}</span>}
                      </div>
                      <div>
                        <span className="text-theme-text-tertiary">Destination: </span>
                        <span className="text-theme-text-primary">{flow.destination.name}</span>
                        {flow.destination.namespace && <span className="text-theme-text-tertiary"> ({flow.destination.namespace})</span>}
                        {flow.destService && <span className="text-theme-text-tertiary"> via {flow.destService}</span>}
                      </div>
                      <div>
                        <span className="text-theme-text-tertiary">Protocol: </span>
                        <span className="text-theme-text-primary">{flow.l7Protocol ? `${flow.l7Protocol} / ${flow.protocol}` : flow.protocol}</span>
                        {flow.httpProtocol && <span className="text-theme-text-tertiary"> ({flow.httpProtocol})</span>}
                      </div>
                      <div>
                        <span className="text-theme-text-tertiary">Port: </span>
                        <span className="text-theme-text-primary">{flow.port}</span>
                      </div>
                      {flow.trafficDirection && (
                        <div>
                          <span className="text-theme-text-tertiary">Direction: </span>
                          <span className="text-theme-text-primary capitalize">{flow.trafficDirection}</span>
                        </div>
                      )}
                      {flow.l7Type && (
                        <div>
                          <span className="text-theme-text-tertiary">L7 Type: </span>
                          <span className="text-theme-text-primary">{flow.l7Type}</span>
                        </div>
                      )}
                      <div>
                        <span className="text-theme-text-tertiary">Data: </span>
                        <span className="text-theme-text-primary">
                          {formatBytes(flow.bytesSent)} sent, {formatBytes(flow.bytesRecv)} recv
                        </span>
                      </div>
                      {flow.latencyNs && flow.latencyNs > 0 && (
                        <div>
                          <span className="text-theme-text-tertiary">Latency: </span>
                          <span className="text-theme-text-primary">{formatLatency(flow.latencyNs)}</span>
                        </div>
                      )}
                    </div>

                    {/* DNS details */}
                    {isDNS && (
                      <div className="pt-1 border-t border-theme-border/50">
                        {flow.dnsIPs && flow.dnsIPs.length > 0 && (
                          <div>
                            <span className="text-theme-text-tertiary">Resolved IPs: </span>
                            <span className="text-theme-text-primary">{flow.dnsIPs.join(', ')}</span>
                          </div>
                        )}
                        {flow.dnsQTypes && flow.dnsQTypes.length > 0 && (
                          <div>
                            <span className="text-theme-text-tertiary">Query Type: </span>
                            <span className="text-theme-text-primary">{flow.dnsQTypes.join(', ')}</span>
                          </div>
                        )}
                        {flow.dnsTTL != null && flow.dnsTTL > 0 && (
                          <div>
                            <span className="text-theme-text-tertiary">TTL: </span>
                            <span className="text-theme-text-primary">{flow.dnsTTL}s</span>
                          </div>
                        )}
                      </div>
                    )}

                    {/* HTTP headers */}
                    {flow.httpHeaders && flow.httpHeaders.length > 0 && (
                      <div className="pt-1 border-t border-theme-border/50">
                        <span className="text-theme-text-tertiary">Headers: </span>
                        <div className="mt-0.5 space-y-0.5">
                          {flow.httpHeaders.map((h, j) => (
                            <div key={j} className="text-theme-text-secondary font-mono text-[10px]">{h}</div>
                          ))}
                        </div>
                      </div>
                    )}

                    {/* Drop reason + policy correlation */}
                    {flow.dropReasonDesc && (
                      <div className="pt-1 border-t border-theme-border/50">
                        <span className={SEVERITY_TEXT.error}>Drop reason: {flow.dropReasonDesc}</span>
                      </div>
                    )}
                    {flow.verdict === 'dropped' && (
                      <PolicyCorrelation flow={flow} />
                    )}
                  </div>
                )}
              </div>
            )
          }}
        />
      )}

      {/* Footer */}
      <div className="px-3 py-1.5 border-t border-theme-border text-[10px] text-theme-text-tertiary">
        {pluralize(sorted.length, 'flow')}
        {search && ` (filtered from ${flows.length})`}
      </div>
    </div>
  )
}

interface PolicyEvaluation {
  selectingPolicies: { name: string; namespace?: string; kind: string; effect: string; reason: string }[]
  verdict: 'admitted' | 'denied' | 'undecidable' | 'no-policy'
  reason?: string
}

/** Hubble reports which policies decided a flow only for policy drops; every
 *  other drop reason is not a policy question at all. */
const POLICY_DROP_REASONS = ['POLICY_DENIED', 'POLICY_DENY']

function isPolicyDrop(flow: TrafficFlow): boolean {
  if (!flow.dropReasonDesc) return true
  return POLICY_DROP_REASONS.some((r) => flow.dropReasonDesc!.toUpperCase().includes(r))
}

const EFFECT_TONE: Record<string, StatusTone> = {
  admits: 'healthy',
  does_not_admit: 'neutral',
  undecidable: 'unknown',
}

function policyRef(p: { kind: string; namespace?: string; name: string }): string {
  return p.namespace ? `${p.namespace}/${p.name}` : p.name
}

function PolicyCorrelation({ flow }: { flow: TrafficFlow }) {
  const src = flow.source
  const dst = flow.destination
  const direction = flow.trafficDirection === 'ingress' || flow.trafficDirection === 'egress' ? flow.trafficDirection : ''
  const evaluated = direction === 'egress' ? src : dst
  const observed = flow.lastSeen ? new Date(flow.lastSeen).toLocaleTimeString() : ''

  // Only a pod has policies applied to it; a drop at a node or an external
  // endpoint is not a NetworkPolicy question.
  const canQuery = isPolicyDrop(flow) && (!direction || (evaluated?.kind === 'Pod' && !!evaluated.namespace && !!evaluated.name))

  const { data, isLoading, error } = useQuery<PolicyEvaluation, Error>({
    queryKey: ['policy-evaluate', direction, dst?.namespace, dst?.name, dst?.kind, dst?.ip, src?.namespace, src?.name, src?.kind, src?.ip, flow.port, flow.protocol],
    queryFn: () => {
      const params = new URLSearchParams()
      if (direction) params.set('direction', direction)
      if (dst?.namespace) params.set('namespace', dst.namespace)
      if (dst?.name) params.set('podName', dst.name)
      if (dst?.kind) params.set('destinationKind', dst.kind)
      if (dst?.ip) params.set('destinationIP', dst.ip)
      if (src?.namespace) params.set('sourceNamespace', src.namespace)
      if (src?.name) params.set('sourcePodName', src.name)
      if (src?.kind) params.set('sourceKind', src.kind)
      if (src?.ip) params.set('sourceIP', src.ip)
      if (flow.port) params.set('port', String(flow.port))
      if (flow.protocol) params.set('protocol', flow.protocol)
      return fetchJSON(`/network-policies/evaluate?${params}`)
    },
    enabled: canQuery,
    staleTime: 30000,
  })

  const hubble = flow.policyVerdict
  const hubbleDenied = hubble?.deniedBy ?? []
  const hubbleAllowed = hubble?.allowedBy ?? []

  if (!isPolicyDrop(flow)) {
    return (
      <div className="pt-1 border-t border-theme-border/50 text-[10px] text-theme-text-tertiary">
        Not evaluated against NetworkPolicies — {flow.dropReasonDesc} is not a policy verdict
      </div>
    )
  }

  // The plugin's own attribution is the ground truth; the static evaluation
  // below is what the policies say about the pod as it is now.
  const headline = hubbleDenied.length > 0
    ? { text: `Denied by ${hubbleDenied.map((p) => `${p.kind} ${policyRef(p)}`).join(', ')}`, tone: 'unhealthy' as StatusTone, source: 'reported by the network plugin' }
    : hubbleAllowed.length > 0
      ? { text: `Allowed by ${hubbleAllowed.map((p) => `${p.kind} ${policyRef(p)}`).join(', ')}`, tone: 'healthy' as StatusTone, source: 'reported by the network plugin' }
      : null

  if (!canQuery && !headline) {
    return (
      <div className="pt-1 border-t border-theme-border/50 text-[10px] text-theme-text-tertiary">
        {evaluated?.kind && evaluated.kind !== 'Pod'
          ? `Policies apply to pods; the ${direction === 'egress' ? 'source' : 'destination'} is ${evaluated.kind === 'Host' ? 'a node or the host network' : evaluated.kind === 'External' ? 'outside the cluster' : 'not identified'}`
          : 'Not enough is known about this flow to evaluate policies'}
      </div>
    )
  }

  const staticVerdict = data?.verdict
  const staticHeadline = staticVerdict === 'admitted'
    ? { text: 'A current NetworkPolicy admits this connection', tone: 'healthy' as StatusTone }
    : staticVerdict === 'denied'
      ? { text: 'No current NetworkPolicy admits this connection', tone: 'unhealthy' as StatusTone }
      : staticVerdict === 'no-policy'
        ? { text: 'No NetworkPolicy applies to this pod', tone: 'neutral' as StatusTone }
        : staticVerdict === 'undecidable'
          ? { text: 'Not decidable from current NetworkPolicies', tone: 'unknown' as StatusTone }
          : null

  const rows = data?.selectingPolicies ?? []

  return (
    <div className="pt-1 border-t border-theme-border/50 space-y-1">
      {headline && (
        <div className="flex items-start gap-1.5">
          <StatusDot tone={headline.tone} className="mt-1 shrink-0" />
          <div className="min-w-0 text-[10px]">
            <span className="text-theme-text-primary font-medium">{headline.text}</span>
            <span className="text-theme-text-tertiary"> · {headline.source}</span>
          </div>
        </div>
      )}
      {canQuery && isLoading && (
        <div className="text-[10px] text-theme-text-tertiary">Checking current NetworkPolicies…</div>
      )}
      {canQuery && error && (
        <div className="text-[10px] text-theme-text-tertiary">Couldn't check current NetworkPolicies: {error.message}</div>
      )}
      {staticHeadline && (
        <div className="flex items-start gap-1.5">
          <StatusDot tone={staticHeadline.tone} className="mt-1 shrink-0" />
          <div className="min-w-0 text-[10px]">
            <span className="text-theme-text-primary font-medium">{staticHeadline.text}</span>
            <span className="text-theme-text-tertiary"> · against policies as they are now{observed ? `, flow seen ${observed}` : ''}</span>
            {data?.reason && <div className="text-theme-text-tertiary">{data.reason}</div>}
          </div>
        </div>
      )}
      {rows.length > 0 && (
        <div className="ml-4 space-y-1">
          <div className="flex items-center gap-1 text-theme-text-secondary">
            <ShieldCheck className="w-3 h-3" />
            <span className="text-[10px] font-medium">
              {rows.length} {rows.length === 1 ? 'policy applies' : 'policies apply'} to {evaluated?.namespace}/{evaluated?.name}
            </span>
          </div>
          {rows.map((p, i) => (
            <div key={i} className="flex items-start gap-1.5">
              <StatusDot tone={EFFECT_TONE[p.effect] ?? 'unknown'} className="mt-1 shrink-0" />
              <div className="min-w-0">
                <span className="text-[10px] text-theme-text-primary font-medium">{p.name}</span>
                <span className="text-[10px] text-theme-text-tertiary ml-1">({p.kind})</span>
                <div className="text-[10px] text-theme-text-tertiary">{p.reason}</div>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
