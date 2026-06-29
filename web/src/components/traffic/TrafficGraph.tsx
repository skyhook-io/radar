import { useMemo, useEffect, useState, useCallback, useRef, type MutableRefObject } from 'react'
import {
  ReactFlow,
  Background,
  Controls,
  useNodesState,
  useEdgesState,
  useReactFlow,
  MarkerType,
  Handle,
  Position,
  type Node,
  type Edge,
  type NodeMouseHandler,
  type EdgeMouseHandler,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import ELK from 'elkjs/lib/elk.bundled.js'
import type { AggregatedFlow } from '../../types'
import { clsx } from 'clsx'
import { X, ArrowRight, Globe, Server, Activity, Puzzle } from 'lucide-react'
import { isClusterAddon, type AddonMode } from './TrafficView'
import { SEVERITY_BADGE, SEVERITY_TEXT } from '@skyhook-io/k8s-ui/utils/badge-colors'
import { getNamespaceColor } from '../../utils/traffic-colors'
import { Tooltip } from '../ui/Tooltip'

const elk = new ELK()

// ELK layout options for traffic graph
const elkOptions = {
  'elk.algorithm': 'layered',
  'elk.direction': 'RIGHT',
  'elk.spacing.nodeNode': '50',
  'elk.layered.spacing.nodeNodeBetweenLayers': '150',
  'elk.layered.spacing.edgeNodeBetweenLayers': '40',
  'elk.edgeRouting': 'ORTHOGONAL',
  'elk.layered.nodePlacement.strategy': 'NETWORK_SIMPLEX',
  'elk.layered.crossingMinimization.strategy': 'LAYER_SWEEP',
}

// Exported selection info for parent components (e.g., to filter flow list)
export interface TrafficGraphSelection {
  type: 'node' | 'edge'
  // For node: the node ID (ns/name or just name)
  // For edge: source and destination IDs
  nodeId?: string
  sourceId?: string
  destId?: string
  port?: number
}

interface TrafficGraphProps {
  flows: AggregatedFlow[]
  hotPathThreshold?: number
  showNamespaceGroups?: boolean
  serviceCategories?: Map<string, string>
  addonMode?: AddonMode
  trafficSource?: string
  onSelectionChange?: (selection: TrafficGraphSelection | null) => void
}

// Phase 2.1: Calculate edge width based on connection count (log scale)
function getEdgeWidth(connections: number): number {
  // 1K -> 1.5px, 10K -> 2.5px, 100K -> 3.5px, 1M -> 4.5px, 10M -> 5.5px
  return Math.min(6, Math.max(1.5, Math.log10(Math.max(connections, 1000)) - 1.5))
}

// Phase 2.2: Format connection counts for display
function formatConnections(count: number): string {
  if (count >= 1_000_000_000) return `${(count / 1_000_000_000).toFixed(1)}B`
  if (count >= 1_000_000) return `${(count / 1_000_000).toFixed(1)}M`
  if (count >= 1_000) return `${(count / 1_000).toFixed(0)}K`
  return count.toString()
}

function formatLatency(ms: number): string {
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)}s`
  if (ms >= 1) return `${ms.toFixed(1)}ms`
  return `${(ms * 1000).toFixed(0)}µs`
}

function latencyColor(ms: number): string {
  if (ms > 500) return SEVERITY_TEXT.error
  if (ms > 200) return SEVERITY_TEXT.warning
  if (ms > 50) return SEVERITY_TEXT.info
  return SEVERITY_TEXT.success
}

const STATUS_COLORS: Record<string, { bg: string; text: string }> = {
  '2xx': { bg: 'bg-emerald-500', text: SEVERITY_TEXT.success },
  '3xx': { bg: 'bg-amber-500', text: SEVERITY_TEXT.neutral },
  '4xx': { bg: 'bg-amber-500', text: SEVERITY_TEXT.warning },
  '5xx': { bg: 'bg-red-500', text: SEVERITY_TEXT.error },
}

const VERDICT_BADGE: Record<string, string> = {
  forwarded: SEVERITY_BADGE.success,
  dropped: SEVERITY_BADGE.error,
  error: SEVERITY_BADGE.warning,
}

function StatusDistributionBar({ counts }: { counts: Record<string, number> }) {
  const total = Object.values(counts).reduce((a, b) => a + b, 0)
  if (total === 0) return null
  const order = ['2xx', '3xx', '4xx', '5xx']
  return (
    <div className="space-y-1">
      <div className="flex h-2 rounded overflow-hidden gap-px">
        {order.map(key => {
          const count = counts[key] ?? 0
          if (count === 0) return null
          const pct = (count / total) * 100
          return <div key={key} className={clsx('h-full', STATUS_COLORS[key]?.bg ?? 'bg-gray-500')} style={{ width: `${pct}%` }} />
        })}
      </div>
      <div className="flex flex-wrap gap-2 text-[10px]">
        {order.map(key => {
          const count = counts[key] ?? 0
          if (count === 0) return null
          return (
            <span key={key} className={STATUS_COLORS[key]?.text ?? 'text-gray-400'}>
              {key}: {count}
            </span>
          )
        })}
      </div>
    </div>
  )
}

// Port info with connection count
interface PortInfo {
  port: number
  connections: number
}

interface TrafficNodeData extends Record<string, unknown> {
  label: string
  namespace?: string
  kind: string
  workload?: string
  connections?: number
  totalConnections?: number // Total connections for this node
  namespaceColor?: string // Background color for namespace grouping
  isHotPath?: boolean // Whether this node is on a hot path
  isAddonNode?: boolean // Whether this is a cluster addon node
  serviceCategory?: string // For external nodes: database, cloud, etc.
  ports?: PortInfo[] // All inbound ports sorted by connection count
  nodeHeight?: number // Dynamic height based on ports
  connLabel?: string // Label for connections: "conn" or "req/s"
}

// Service category colors for external nodes
const SERVICE_CATEGORY_COLORS: Record<string, { bg: string; border: string; dot: string }> = {
  database: { bg: 'bg-violet-500/20', border: 'border-violet-500/50', dot: 'bg-violet-500' },
  cloud: { bg: 'bg-cyan-500/20', border: 'border-cyan-500/50', dot: 'bg-cyan-500' },
  monitoring: { bg: 'bg-lime-500/20', border: 'border-lime-500/50', dot: 'bg-lime-500' },
  payment: { bg: 'bg-emerald-500/20', border: 'border-emerald-500/50', dot: 'bg-emerald-500' },
  auth: { bg: 'bg-amber-500/20', border: 'border-amber-500/50', dot: 'bg-amber-500' },
  email: { bg: 'bg-pink-500/20', border: 'border-pink-500/50', dot: 'bg-pink-500' },
  messaging: { bg: 'bg-purple-500/20', border: 'border-purple-500/50', dot: 'bg-purple-500' },
  cache: { bg: 'bg-orange-500/20', border: 'border-orange-500/50', dot: 'bg-orange-500' },
  infra: { bg: 'bg-slate-500/20', border: 'border-slate-500/50', dot: 'bg-slate-500' },
  web: { bg: 'bg-blue-500/20', border: 'border-blue-500/50', dot: 'bg-blue-500' },
}

const NODE_WIDTH = 180
const NODE_BASE_HEIGHT = 56 // Base height without ports
const NODE_PORT_HEIGHT = 18 // Height per port row
const MAX_VISIBLE_PORTS = 4 // Maximum ports to show before "+N more"

// Custom node component
function TrafficNode({ data }: { data: TrafficNodeData }) {
  const isExternal = data.kind.toLowerCase() === 'external'
  const isInternet = data.kind === 'Internet'
  const isAddonInternet = data.kind === 'AddonInternet' // Separate internet for addon traffic
  const isAddon = data.kind === 'Addon'
  const isAddonNode = data.isAddonNode // Node is part of addon group
  const categoryColors = data.serviceCategory ? SERVICE_CATEGORY_COLORS[data.serviceCategory] : null
  const hasNamespaceColor = !isExternal && !isInternet && !isAddon && !isAddonNode && !isAddonInternet && data.namespaceColor

  return (
    <div
      className={clsx(
        'px-3 py-2 rounded-lg border shadow-sm relative transition-all',
        isAddonInternet
          ? 'bg-purple-500/30 border-purple-400/50' // Purple internet for addons (outside group)
          : isInternet
            ? 'bg-sky-500/20 border-sky-500/50'
            : isAddon
              ? 'bg-purple-500/20 border-purple-500/50'
              : isAddonNode
                ? 'bg-purple-900/60 border-purple-500/50'
                : isExternal
                  ? categoryColors
                    ? `${categoryColors.bg} ${categoryColors.border}`
                    : 'bg-yellow-500/10 border-yellow-500/30'
                  : hasNamespaceColor
                    ? 'border-white/20'
                    : 'bg-theme-surface border-theme-border',
        data.isHotPath && 'ring-2 ring-orange-500/50'
      )}
      style={{
        width: NODE_WIDTH,
        backgroundColor: hasNamespaceColor ? data.namespaceColor : undefined,
      }}
    >
      {/* Handles for edge connections */}
      <Handle type="target" position={Position.Left} className="!bg-gray-400 !w-2 !h-2" />
      <Handle type="source" position={Position.Right} className="!bg-gray-400 !w-2 !h-2" />

      <div className="flex items-center gap-2">
        {isAddonInternet ? (
          <Globe className="w-4 h-4 text-purple-400 shrink-0" />
        ) : isInternet ? (
          <Globe className="w-4 h-4 text-sky-400 shrink-0" />
        ) : isAddon ? (
          <div className="w-2 h-2 rounded-full shrink-0 bg-purple-500" />
        ) : isAddonNode ? (
          <div className="w-2 h-2 rounded-full shrink-0 bg-purple-400" />
        ) : (
          <div
            className={clsx(
              'w-2 h-2 rounded-full shrink-0',
              data.isHotPath
                ? 'bg-orange-500'
                : isExternal
                  ? categoryColors?.dot || 'bg-yellow-500'
                  : 'bg-green-500'
            )}
          />
        )}
        <div className="flex-1 min-w-0">
          <div className={clsx(
            'text-sm font-medium truncate',
            isAddonInternet ? 'text-purple-300' : isInternet ? 'text-sky-300' : (isAddon || isAddonNode) ? 'text-purple-200' : hasNamespaceColor ? 'text-white' : 'text-theme-text-primary'
          )}>{data.label}</div>
          {data.namespace ? (
            <div className={clsx(
              'text-xs truncate',
              (hasNamespaceColor || isAddonNode) ? 'text-white/70' : 'text-theme-text-tertiary'
            )}>
              {data.namespace}
            </div>
          ) : isAddonInternet ? (
            <div className="text-xs text-purple-400/70 truncate">
              Inbound traffic
            </div>
          ) : isInternet ? (
            <div className="text-xs text-sky-400/70 truncate">
              Inbound traffic
            </div>
          ) : isAddon ? (
            <div className="text-xs text-purple-400/70 truncate">
              Monitoring, logging, etc.
            </div>
          ) : isExternal && data.serviceCategory && (
            <div className="text-xs text-theme-text-tertiary truncate capitalize">
              {data.serviceCategory}
            </div>
          )}
        </div>
      </div>
      {data.workload && data.workload !== data.label && (
        <div className={clsx(
          'text-xs mt-1 truncate',
          hasNamespaceColor ? 'text-white/70' : 'text-theme-text-tertiary'
        )}>
          {data.workload}
        </div>
      )}
      {/* Ports section */}
      {data.ports && data.ports.filter(p => p.port !== 0).length > 0 && (
        <div className="mt-1.5 space-y-0.5">
          {data.ports.filter(p => p.port !== 0).slice(0, MAX_VISIBLE_PORTS).map((portInfo) => (
            <div key={portInfo.port} className="flex items-center justify-between gap-1 text-xs">
              <span className={clsx(
                'font-mono',
                (hasNamespaceColor || isAddonNode) ? 'text-cyan-300' : 'text-blue-600 dark:text-blue-300'
              )}>
                :{portInfo.port}
              </span>
              <span className={clsx(
                'truncate',
                data.isHotPath
                  ? 'text-orange-400'
                  : (hasNamespaceColor || isAddonNode)
                    ? 'text-white/60'
                    : 'text-theme-text-tertiary'
              )}>
                {formatConnections(portInfo.connections)}
              </span>
            </div>
          ))}
          {data.ports.filter(p => p.port !== 0).length > MAX_VISIBLE_PORTS && (
            <div className={clsx(
              'text-xs',
              (hasNamespaceColor || isAddonNode) ? 'text-white/50' : 'text-theme-text-tertiary'
            )}>
              +{data.ports.filter(p => p.port !== 0).length - MAX_VISIBLE_PORTS} more
            </div>
          )}
        </div>
      )}
      {/* Total connections (only if no ports shown, or all ports are 0) */}
      {(!data.ports || data.ports.filter(p => p.port !== 0).length === 0) && data.totalConnections && data.totalConnections > 0 && (
        <div className="mt-1">
          <span className={clsx(
            'text-xs truncate',
            data.isHotPath
              ? 'text-orange-400 font-medium'
              : (hasNamespaceColor || isAddonNode)
                ? 'text-white/70'
                : 'text-theme-text-tertiary'
          )}>
            {formatConnections(data.totalConnections)} {data.connLabel || 'conn'}
          </span>
        </div>
      )}
    </div>
  )
}

// Legend component
function TrafficLegend() {
  return (
    <div className="absolute bottom-2 left-2 bg-theme-surface border border-theme-border rounded-lg p-2.5 text-xs z-10 shadow-lg max-w-xs">
      <div className="font-medium text-theme-text-primary mb-2">Legend</div>
      <div className="space-y-1.5">
        <div className="flex items-center gap-2">
          <svg width="24" height="8" className="shrink-0">
            <line x1="0" y1="4" x2="24" y2="4" stroke="#f97316" strokeWidth="2" strokeDasharray="4 2" />
          </svg>
          <span className="text-theme-text-secondary">Hot path (top 10%)</span>
        </div>
        <div className="flex items-center gap-2">
          <svg width="24" height="8" className="shrink-0">
            <line x1="0" y1="4" x2="24" y2="4" stroke="#3b82f6" strokeWidth="2" />
          </svg>
          <span className="text-theme-text-secondary">HTTP / gRPC</span>
        </div>
        <div className="flex items-center gap-2">
          <svg width="24" height="8" className="shrink-0">
            <line x1="0" y1="4" x2="24" y2="4" stroke="#06b6d4" strokeWidth="2" />
          </svg>
          <span className="text-theme-text-secondary">DNS</span>
        </div>
        <div className="flex items-center gap-2">
          <svg width="24" height="8" className="shrink-0">
            <line x1="0" y1="4" x2="24" y2="4" stroke="#ef4444" strokeWidth="2" />
          </svg>
          <span className="text-theme-text-secondary">Errors (5xx)</span>
        </div>
        <div className="flex items-center gap-2">
          <svg width="24" height="8" className="shrink-0">
            <line x1="0" y1="4" x2="24" y2="4" stroke="#ef4444" strokeWidth="2" strokeDasharray="6 3" />
          </svg>
          <span className="text-theme-text-secondary">Dropped</span>
        </div>
        <div className="flex items-center gap-2">
          <svg width="24" height="8" className="shrink-0">
            <line x1="0" y1="4" x2="24" y2="4" stroke="#6b7280" strokeWidth="2" />
          </svg>
          <span className="text-theme-text-secondary">TCP</span>
        </div>
        <div className="pt-1.5 border-t border-theme-border text-theme-text-tertiary italic">
          Thicker = more traffic
        </div>
      </div>

    </div>
  )
}

// Selection state types
type SelectionType = 'node' | 'edge' | null
interface Selection {
  type: SelectionType
  id: string
  data?: TrafficNodeData | EdgeData
}

interface EdgeData {
  source: string
  target: string
  port: number
  connections: number
  protocol: string
  flow?: AggregatedFlow
}

// Format bytes for display
function formatBytes(bytes: number): string {
  if (bytes >= 1_000_000_000) return `${(bytes / 1_000_000_000).toFixed(1)} GB`
  if (bytes >= 1_000_000) return `${(bytes / 1_000_000).toFixed(1)} MB`
  if (bytes >= 1_000) return `${(bytes / 1_000).toFixed(1)} KB`
  return `${bytes} B`
}

// Details panel component
function DetailsPanel({
  selection,
  onClose,
  flows,
  isIstio,
}: {
  selection: Selection
  onClose: () => void
  flows: AggregatedFlow[]
  isIstio: boolean
}) {
  if (!selection) return null

  const isNode = selection.type === 'node'
  const nodeData = isNode ? (selection.data as TrafficNodeData) : null
  const edgeData = !isNode ? (selection.data as EdgeData) : null

  // Find related flows for node
  const relatedFlows = isNode
    ? flows.filter(f => {
        const sourceId = f.source.namespace ? `${f.source.namespace}/${f.source.name}` : f.source.name
        const destId = f.destination.namespace ? `${f.destination.namespace}/${f.destination.name}` : f.destination.name
        return sourceId === selection.id || destId === selection.id
      })
    : []

  // Categorize flows by direction
  const incomingFlows = relatedFlows.filter(f => {
    const destId = f.destination.namespace ? `${f.destination.namespace}/${f.destination.name}` : f.destination.name
    return destId === selection.id
  })
  const outgoingFlows = relatedFlows.filter(f => {
    const sourceId = f.source.namespace ? `${f.source.namespace}/${f.source.name}` : f.source.name
    return sourceId === selection.id
  })

  // Compute aggregate stats for node
  const nodeStats = isNode ? {
    totalBytes: relatedFlows.reduce((sum, f) => sum + f.bytesSent + f.bytesRecv, 0),
    protocols: relatedFlows.reduce((acc, f) => {
      const proto = f.protocol?.toUpperCase() || 'TCP'
      acc[proto] = (acc[proto] || 0) + f.connections
      return acc
    }, {} as Record<string, number>),
    lastSeen: relatedFlows.reduce((latest, f) => {
      if (!f.lastSeen) return latest
      return !latest || f.lastSeen > latest ? f.lastSeen : latest
    }, null as string | null),
    flowCount: relatedFlows.reduce((sum, f) => sum + (f.flowCount || 1), 0),
    totalRequests: relatedFlows.reduce((sum, f) => sum + (f.requestCount ?? 0), 0),
    totalErrors: relatedFlows.reduce((sum, f) => sum + (f.errorCount ?? 0), 0),
    l7Protocols: relatedFlows.reduce((acc, f) => {
      if (f.l7Protocol) acc.add(f.l7Protocol)
      return acc
    }, new Set<string>()),
    // Aggregate latency across edges (median of P50s, max of P95s)
    latencyP50Ms: (() => {
      const p50s = relatedFlows.map(f => f.latencyP50Ms).filter((v): v is number => v != null && v > 0)
      if (p50s.length === 0) return undefined
      p50s.sort((a, b) => a - b)
      return p50s[Math.floor(p50s.length / 2)]
    })(),
    latencyP95Ms: (() => {
      const p95s = relatedFlows.map(f => f.latencyP95Ms).filter((v): v is number => v != null && v > 0)
      return p95s.length > 0 ? Math.max(...p95s) : undefined
    })(),
    // Aggregate HTTP status distribution
    httpStatusCounts: relatedFlows.reduce((acc, f) => {
      if (f.httpStatusCounts) {
        for (const [k, v] of Object.entries(f.httpStatusCounts)) {
          acc[k] = (acc[k] || 0) + v
        }
      }
      return acc
    }, {} as Record<string, number>),
    // Aggregate verdict counts
    verdictCounts: relatedFlows.reduce((acc, f) => {
      if (f.verdictCounts) {
        for (const [k, v] of Object.entries(f.verdictCounts)) {
          acc[k] = (acc[k] || 0) + v
        }
      }
      return acc
    }, {} as Record<string, number>),
  } : null

  return (
    <div className="absolute top-2 right-2 w-80 max-h-[calc(100%-1rem)] bg-theme-surface border border-theme-border rounded-lg shadow-xl overflow-hidden flex flex-col z-50">
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2 border-b border-theme-border bg-theme-elevated">
        <div className="flex items-center gap-2">
          {isNode ? (
            nodeData?.kind === 'Internet' ? (
              <Globe className="h-4 w-4 text-sky-400" />
            ) : nodeData?.kind === 'Addon' ? (
              <Server className="h-4 w-4 text-purple-400" />
            ) : nodeData?.kind.toLowerCase() === 'external' ? (
              <Globe className="h-4 w-4 text-yellow-500" />
            ) : (
              <Server className="h-4 w-4 text-blue-500" />
            )
          ) : (
            <Activity className="h-4 w-4 text-green-500" />
          )}
          <span className="text-sm font-medium text-theme-text-primary">
            {isNode ? (nodeData?.kind === 'Internet' ? 'Internet Traffic' : nodeData?.kind === 'Addon' ? 'Cluster Addons' : 'Service Details') : 'Connection Details'}
          </span>
        </div>
        <button
          onClick={onClose}
          className="p-1 rounded hover:bg-theme-hover text-theme-text-secondary"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto p-3 space-y-3">
        {isNode && nodeData && (
          <>
            {/* Node info */}
            <div className="space-y-1">
              <div className="text-sm font-medium text-theme-text-primary">{nodeData.label}</div>
              {nodeData.namespace && (
                <div className="text-xs text-theme-text-secondary">
                  Namespace: <span className="text-theme-text-primary">{nodeData.namespace}</span>
                </div>
              )}
              <div className="text-xs text-theme-text-secondary">
                Type: <span className={clsx(
                  'px-1.5 py-0.5 rounded text-[10px]',
                  nodeData.kind === 'Internet'
                    ? 'bg-sky-500/20 text-sky-400'
                    : nodeData.kind === 'Addon'
                      ? 'bg-purple-500/20 text-purple-400'
                      : nodeData.kind.toLowerCase() === 'external'
                        ? 'bg-yellow-500/20 text-yellow-400'
                        : 'bg-blue-500/20 text-blue-400'
                )}>{nodeData.kind === 'Addon' ? 'Cluster Addons' : nodeData.kind}</span>
              </div>
              {nodeData.workload && nodeData.workload !== nodeData.label && (
                <div className="text-xs text-theme-text-secondary">
                  Workload: <span className="text-theme-text-primary">{nodeData.workload}</span>
                </div>
              )}
              {nodeData.serviceCategory && (
                <div className="text-xs text-theme-text-secondary">
                  Service: <span className="text-theme-text-primary capitalize">{nodeData.serviceCategory}</span>
                </div>
              )}
              {nodeData.totalConnections && (
                <div className="text-xs text-theme-text-secondary">
                  {isIstio ? 'Total request rate' : 'Total connections'}: <span className="text-theme-text-primary font-medium">
                    {formatConnections(nodeData.totalConnections)}{isIstio ? '/s' : ''}
                  </span>
                </div>
              )}
            </div>

            {/* Stats grid */}
            {nodeStats && (nodeStats.totalBytes > 0 || nodeStats.lastSeen || nodeStats.totalRequests > 0) && (
              <div className="grid grid-cols-2 gap-2">
                {nodeStats.totalBytes > 0 && (
                  <div className="p-2 rounded bg-theme-elevated text-xs">
                    <div className="text-theme-text-tertiary">Data transferred</div>
                    <div className="text-theme-text-primary font-medium">{formatBytes(nodeStats.totalBytes)}</div>
                  </div>
                )}
                {nodeStats.flowCount > 1 && (
                  <div className="p-2 rounded bg-theme-elevated text-xs">
                    <div className="text-theme-text-tertiary">Raw flows</div>
                    <div className="text-theme-text-primary font-medium">{nodeStats.flowCount.toLocaleString()}</div>
                  </div>
                )}
                {nodeStats.totalRequests > 0 && (
                  <div className="p-2 rounded bg-theme-elevated text-xs">
                    <div className="text-theme-text-tertiary">Requests</div>
                    <div className="text-theme-text-primary font-medium">{formatConnections(nodeStats.totalRequests)}/s</div>
                  </div>
                )}
                {nodeStats.totalErrors > 0 && (
                  <div className="p-2 rounded bg-red-500/10 border border-red-500/30 text-xs">
                    <div className="text-red-400">Errors (5xx)</div>
                    <div className="text-red-400 font-medium">
                      {formatConnections(nodeStats.totalErrors)}/s
                      {nodeStats.totalRequests > 0 && (
                        <span className="text-red-300 ml-1">
                          ({((nodeStats.totalErrors / nodeStats.totalRequests) * 100).toFixed(1)}%)
                        </span>
                      )}
                    </div>
                  </div>
                )}
                {Object.keys(nodeStats.protocols).length > 0 && (
                  <div className="p-2 rounded bg-theme-elevated text-xs col-span-2">
                    <div className="text-theme-text-tertiary mb-1">Protocols</div>
                    <div className="flex flex-wrap gap-1.5">
                      {nodeStats.l7Protocols.size > 0 && Array.from(nodeStats.l7Protocols).map(proto => (
                        <span key={`l7-${proto}`} className={clsx('inline-flex items-center gap-1 px-1.5 py-0.5 rounded badge', SEVERITY_BADGE.info)}>
                          <span className="font-medium">{proto}</span>
                        </span>
                      ))}
                      {Object.entries(nodeStats.protocols)
                        .sort((a, b) => b[1] - a[1])
                        .map(([proto, count]) => (
                          <span key={proto} className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-theme-bg text-theme-text-secondary">
                            <span className="font-medium">{proto}</span>
                            <span className="text-theme-text-tertiary">{formatConnections(count)}</span>
                          </span>
                        ))}
                    </div>
                  </div>
                )}
              </div>
            )}

            {/* Node latency */}
            {nodeStats?.latencyP50Ms && (
              <div className="pt-1">
                <div className="text-[10px] text-theme-text-tertiary mb-1">Latency</div>
                <div className="flex gap-2 text-xs">
                  <span className={clsx('font-medium', latencyColor(nodeStats.latencyP50Ms))}>
                    P50: {formatLatency(nodeStats.latencyP50Ms)}
                  </span>
                  {nodeStats.latencyP95Ms && (
                    <span className={clsx('font-medium', latencyColor(nodeStats.latencyP95Ms))}>
                      P95: {formatLatency(nodeStats.latencyP95Ms)}
                    </span>
                  )}
                </div>
              </div>
            )}

            {/* Node HTTP status distribution */}
            {nodeStats?.httpStatusCounts && Object.keys(nodeStats.httpStatusCounts).length > 0 && (
              <div className="pt-1">
                <div className="text-[10px] text-theme-text-tertiary mb-1">HTTP Status</div>
                <StatusDistributionBar counts={nodeStats.httpStatusCounts} />
              </div>
            )}

            {/* Node verdict summary */}
            {nodeStats?.verdictCounts && (nodeStats.verdictCounts.dropped ?? 0) > 0 && (
              <div className="pt-1">
                <div className="flex flex-wrap gap-1">
                  {Object.entries(nodeStats.verdictCounts).map(([verdict, count]) => (
                    <span
                      key={verdict}
                      className={clsx('inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium',
                        VERDICT_BADGE[verdict] ?? SEVERITY_BADGE.neutral
                      )}
                    >
                      {verdict}: {count}
                    </span>
                  ))}
                </div>
              </div>
            )}

            {/* Incoming connections */}
            {incomingFlows.length > 0 && (
              <div className="space-y-1">
                <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wide">
                  Incoming ({incomingFlows.length})
                </div>
                <div className="space-y-1.5 max-h-48 overflow-y-auto">
                  {incomingFlows
                    .sort((a, b) => b.connections - a.connections)
                    .map((flow, i) => (
                    <div key={i} className="text-xs p-2 rounded bg-theme-elevated space-y-1">
                      <div className="flex items-center gap-1.5">
                        <span className="text-theme-text-primary truncate flex-1">
                          {flow.source.name}
                        </span>
                        <ArrowRight className="h-3 w-3 text-theme-text-tertiary shrink-0" />
                        {flow.port !== 0 && (
                          <span className="text-blue-400 font-mono">:{flow.port}</span>
                        )}
                      </div>
                      <div className="flex items-center gap-2 text-[10px]">
                        <span className="px-1 py-0.5 rounded bg-theme-bg text-theme-text-tertiary uppercase">
                          {flow.protocol || 'tcp'}
                        </span>
                        <span className="text-theme-text-secondary">
                          {formatConnections(flow.connections)} {isIstio ? 'req/s' : 'conn'}
                        </span>
                        {(flow.bytesSent > 0 || flow.bytesRecv > 0) && (
                          <span className="text-theme-text-tertiary">
                            {formatBytes(flow.bytesSent + flow.bytesRecv)}
                          </span>
                        )}
                        {flow.errorCount && flow.errorCount > 0 && (
                          <span className="text-red-400">
                            {formatConnections(flow.errorCount)} err
                          </span>
                        )}
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* Outgoing connections */}
            {outgoingFlows.length > 0 && (
              <div className="space-y-1">
                <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wide">
                  Outgoing ({outgoingFlows.length})
                </div>
                <div className="space-y-1.5 max-h-48 overflow-y-auto">
                  {outgoingFlows
                    .sort((a, b) => b.connections - a.connections)
                    .map((flow, i) => (
                    <div key={i} className="text-xs p-2 rounded bg-theme-elevated space-y-1">
                      <div className="flex items-center gap-1.5">
                        <ArrowRight className="h-3 w-3 text-theme-text-tertiary shrink-0" />
                        <span className="text-theme-text-primary truncate flex-1">
                          {flow.destination.name}
                        </span>
                        {flow.port !== 0 && (
                          <span className="text-blue-400 font-mono">:{flow.port}</span>
                        )}
                      </div>
                      <div className="flex items-center gap-2 text-[10px]">
                        <span className="px-1 py-0.5 rounded bg-theme-bg text-theme-text-tertiary uppercase">
                          {flow.protocol || 'tcp'}
                        </span>
                        <span className="text-theme-text-secondary">
                          {formatConnections(flow.connections)} {isIstio ? 'req/s' : 'conn'}
                        </span>
                        {(flow.bytesSent > 0 || flow.bytesRecv > 0) && (
                          <span className="text-theme-text-tertiary">
                            {formatBytes(flow.bytesSent + flow.bytesRecv)}
                          </span>
                        )}
                        {flow.errorCount && flow.errorCount > 0 && (
                          <span className="text-red-400">
                            {formatConnections(flow.errorCount)} err
                          </span>
                        )}
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </>
        )}

        {!isNode && edgeData && (
          <>
            {/* Edge/Flow info */}
            <div className="space-y-2">
              <div className="flex items-center gap-2 text-sm">
                <span className="text-theme-text-primary truncate">{edgeData.source.split('/').pop()}</span>
                <ArrowRight className="h-4 w-4 text-theme-text-tertiary shrink-0" />
                <span className="text-theme-text-primary truncate">{edgeData.target.split('/').pop()}</span>
              </div>

              <div className="grid grid-cols-2 gap-2 text-xs">
                {edgeData.port !== 0 && (
                  <div className="p-2 rounded bg-theme-elevated">
                    <div className="text-theme-text-tertiary">Port</div>
                    <div className="text-theme-text-primary font-medium">{edgeData.port}</div>
                  </div>
                )}
                <div className="p-2 rounded bg-theme-elevated">
                  <div className="text-theme-text-tertiary">Protocol</div>
                  <div className="text-theme-text-primary font-medium uppercase">
                    {edgeData.flow?.l7Protocol
                      ? `${edgeData.flow.l7Protocol} / ${edgeData.protocol}`
                      : edgeData.protocol}
                  </div>
                </div>
                <div className="p-2 rounded bg-theme-elevated">
                  <div className="text-theme-text-tertiary">{isIstio ? 'Request Rate' : 'Connections'}</div>
                  <div className="text-theme-text-primary font-medium">
                    {formatConnections(edgeData.connections)}{isIstio ? '/s' : ''}
                  </div>
                </div>
                {edgeData.flow && (edgeData.flow.bytesSent > 0 || edgeData.flow.bytesRecv > 0) && (
                  <div className="p-2 rounded bg-theme-elevated">
                    <div className="text-theme-text-tertiary">Data</div>
                    <div className="text-theme-text-primary font-medium">
                      {formatBytes(edgeData.flow.bytesSent + edgeData.flow.bytesRecv)}
                    </div>
                  </div>
                )}
                {edgeData.flow?.requestCount && edgeData.flow.requestCount > 0 && (
                  <div className="p-2 rounded bg-theme-elevated">
                    <div className="text-theme-text-tertiary">Requests</div>
                    <div className="text-theme-text-primary font-medium">
                      {formatConnections(edgeData.flow.requestCount)}/s
                    </div>
                  </div>
                )}
                {edgeData.flow?.errorCount && edgeData.flow.errorCount > 0 && (
                  <div className="p-2 rounded bg-red-500/10 border border-red-500/30">
                    <div className="text-red-400">Errors (5xx)</div>
                    <div className="text-red-400 font-medium">
                      {formatConnections(edgeData.flow.errorCount)}/s
                      {edgeData.flow.requestCount && edgeData.flow.requestCount > 0 && (
                        <span className="text-red-300 ml-1">
                          ({((edgeData.flow.errorCount / edgeData.flow.requestCount) * 100).toFixed(1)}%)
                        </span>
                      )}
                    </div>
                  </div>
                )}
              </div>

              {/* Latency percentiles */}
              {edgeData.flow && (edgeData.flow.latencyP50Ms || edgeData.flow.latencyP95Ms || edgeData.flow.latencyP99Ms) ? (
                <div className="pt-2 border-t border-theme-border">
                  <div className="text-[10px] text-theme-text-tertiary mb-1.5">Latency</div>
                  <div className="grid grid-cols-3 gap-1.5 text-xs">
                    {[
                      { label: 'P50', value: edgeData.flow.latencyP50Ms },
                      { label: 'P95', value: edgeData.flow.latencyP95Ms },
                      { label: 'P99', value: edgeData.flow.latencyP99Ms },
                    ].map(({ label, value }) => (
                      <div key={label} className="p-1.5 rounded bg-theme-elevated text-center">
                        <div className="text-theme-text-tertiary text-[9px]">{label}</div>
                        <div className={clsx('font-medium', latencyColor(value ?? 0))}>
                          {value ? formatLatency(value) : '—'}
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              ) : null}

              {/* HTTP status distribution */}
              {edgeData.flow?.httpStatusCounts && Object.keys(edgeData.flow.httpStatusCounts).length > 0 && (
                <div className="pt-2 border-t border-theme-border">
                  <div className="text-[10px] text-theme-text-tertiary mb-1.5">HTTP Status</div>
                  <StatusDistributionBar counts={edgeData.flow.httpStatusCounts} />
                </div>
              )}

              {/* Top HTTP paths */}
              {edgeData.flow?.topHTTPPaths && edgeData.flow.topHTTPPaths.length > 0 && (
                <div className="pt-2 border-t border-theme-border">
                  <div className="text-[10px] text-theme-text-tertiary mb-1.5">Top Paths</div>
                  <div className="space-y-1 max-h-40 overflow-y-auto">
                    {edgeData.flow.topHTTPPaths.map((p, i) => (
                      <div key={i} className="flex items-center gap-1.5 text-[10px]">
                        <span className={clsx('shrink-0 px-1 py-0.5 rounded badge font-medium', SEVERITY_BADGE.info)}>{p.method}</span>
                        <Tooltip content={p.path} wrapperClassName="min-w-0 flex-1">
                        <span className="text-theme-text-primary truncate flex-1">{p.path || '/'}</span>
                        </Tooltip>
                        <span className="shrink-0 text-theme-text-secondary">{p.count}</span>
                        {p.avgMs ? <span className="shrink-0 text-theme-text-tertiary">{formatLatency(p.avgMs)}</span> : null}
                        {p.errorPct ? <span className={clsx('shrink-0', p.errorPct > 10 ? SEVERITY_TEXT.error : SEVERITY_TEXT.warning)}>{p.errorPct.toFixed(0)}%err</span> : null}
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* Top DNS queries */}
              {edgeData.flow?.topDNSQueries && edgeData.flow.topDNSQueries.length > 0 && (
                <div className="pt-2 border-t border-theme-border">
                  <div className="text-[10px] text-theme-text-tertiary mb-1.5">DNS Queries</div>
                  <div className="space-y-1 max-h-40 overflow-y-auto">
                    {edgeData.flow.topDNSQueries.map((q, i) => (
                      <div key={i} className="flex items-center gap-1.5 text-[10px]">
                        <Tooltip content={q.query} wrapperClassName="min-w-0 flex-1">
                        <span className="text-theme-text-primary truncate flex-1">{q.query}</span>
                        </Tooltip>
                        <span className="shrink-0 text-theme-text-secondary">{q.count}</span>
                        {q.nxCount ? <span className={clsx('shrink-0', SEVERITY_TEXT.warning)}>NX:{q.nxCount}</span> : null}
                        {q.avgTTL ? <span className="shrink-0 text-theme-text-tertiary">TTL:{q.avgTTL}s</span> : null}
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* Verdict breakdown */}
              {edgeData.flow?.verdictCounts && Object.keys(edgeData.flow.verdictCounts).length > 1 && (
                <div className="pt-2 border-t border-theme-border">
                  <div className="text-[10px] text-theme-text-tertiary mb-1.5">Verdicts</div>
                  <div className="flex flex-wrap gap-1">
                    {Object.entries(edgeData.flow.verdictCounts).map(([verdict, count]) => (
                      <span
                        key={verdict}
                        className={clsx('inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium',
                          verdict === 'forwarded' ? 'bg-green-500/20 text-green-300' :
                          verdict === 'dropped' ? 'bg-red-500/20 text-red-300' :
                          'bg-orange-500/20 text-orange-300'
                        )}
                      >
                        {verdict}: {count}
                      </span>
                    ))}
                  </div>
                  {edgeData.flow.dropReasons && Object.keys(edgeData.flow.dropReasons).length > 0 && (
                    <div className="mt-1 space-y-0.5">
                      {Object.entries(edgeData.flow.dropReasons).map(([reason, count]) => (
                        <div key={reason} className={clsx('text-[9px] pl-1', SEVERITY_TEXT.error)}>
                          {reason}: {count}
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              )}

              {edgeData.flow && (
                <div className="space-y-1 pt-2 border-t border-theme-border">
                  <div className="text-xs text-theme-text-secondary">
                    Source: <span className="text-theme-text-primary">{edgeData.flow.source.name}</span>
                    {edgeData.flow.source.namespace && (
                      <span className="text-theme-text-tertiary"> ({edgeData.flow.source.namespace})</span>
                    )}
                  </div>
                  <div className="text-xs text-theme-text-secondary">
                    Destination: <span className="text-theme-text-primary">{edgeData.flow.destination.name}</span>
                    {edgeData.flow.destination.namespace && (
                      <span className="text-theme-text-tertiary"> ({edgeData.flow.destination.namespace})</span>
                    )}
                  </div>
                  {edgeData.flow.destination.kind.toLowerCase() === 'external' && (
                    <div className="text-xs text-yellow-400 mt-1">
                      External service
                    </div>
                  )}
                </div>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  )
}

// Group container node component for addon grouping
function AddonGroupNode({ data }: { data: { width: number; height: number } }) {
  return (
    <div
      className="rounded-xl border-2 border-dashed border-purple-500/50 bg-purple-950/40 cursor-move"
      style={{ width: data.width, height: data.height }}
    >
      {/* Handle for incoming edges (left side, vertically centered) */}
      <Handle
        type="target"
        position={Position.Left}
        className="!bg-purple-400 !w-3 !h-3 !border-2 !border-purple-600"
        style={{ top: '50%' }}
      />
      {/* Handle for outgoing edges (right side) */}
      <Handle
        type="source"
        position={Position.Right}
        className="!bg-purple-400 !w-3 !h-3 !border-2 !border-purple-600"
        style={{ top: '50%' }}
      />
      {/* Label at top */}
      <div className="absolute top-2 left-3 flex items-center gap-1.5 px-2 py-1 rounded-md bg-purple-500/30 border border-purple-500/40">
        <Puzzle className="w-3.5 h-3.5 text-purple-400" />
        <span className="text-xs font-medium text-purple-300">Cluster Addons</span>
      </div>
    </div>
  )
}

const nodeTypes = {
  traffic: TrafficNode,
  addonGroup: AddonGroupNode,
}

// Fits the view once nodes have been laid out. Module-scope (not defined inside
// TrafficGraph's render) so it keeps a stable identity — otherwise it remounts
// every render and its effect churns. Must render inside <ReactFlow> for the
// useReactFlow() context; the trigger state is passed in as props.
function FitViewOnChange({
  shouldFitViewRef,
  layoutedNodes,
}: {
  shouldFitViewRef: MutableRefObject<boolean>
  layoutedNodes: Node<TrafficNodeData>[]
}) {
  const { fitView } = useReactFlow()

  useEffect(() => {
    if (shouldFitViewRef.current && layoutedNodes.length > 0) {
      // Small delay to ensure nodes are rendered
      const timer = setTimeout(() => {
        fitView({ padding: 0.2, duration: 200 })
        shouldFitViewRef.current = false
      }, 50)
      return () => clearTimeout(timer)
    }
  }, [fitView, layoutedNodes, shouldFitViewRef])

  return null
}

export function TrafficGraph({ flows, hotPathThreshold = 0, showNamespaceGroups = false, serviceCategories, addonMode = 'show', trafficSource = '', onSelectionChange }: TrafficGraphProps) {
  const isIstio = trafficSource === 'istio'
  const connLabel = isIstio ? 'req/s' : 'conn'
  const [layoutedNodes, setLayoutedNodes] = useState<Node<TrafficNodeData>[]>([])
  const [layoutedEdges, setLayoutedEdges] = useState<Edge[]>([])
  const [selection, setSelection] = useState<Selection | null>(null)

  // Store flow data by edge ID for click handler lookup
  const flowByEdgeId = useMemo(() => {
    const map = new Map<string, AggregatedFlow>()
    flows.forEach(flow => {
      const sourceId = flow.source.namespace
        ? `${flow.source.namespace}/${flow.source.name}`
        : flow.source.name
      const destId = flow.destination.namespace
        ? `${flow.destination.namespace}/${flow.destination.name}`
        : flow.destination.name
      const edgeId = `${sourceId}->${destId}:${flow.port}`
      map.set(edgeId, flow)
    })
    return map
  }, [flows])

  // Build nodes and edges from flows
  const { rawNodes, rawEdges, addonGroupEdge, addonGroupOutEdge } = useMemo(() => {
    const nodeMap = new Map<string, Node<TrafficNodeData>>()
    const edgeList: Edge[] = []
    const connectionCounts = new Map<string, number>() // Count of edges per node
    const totalConnections = new Map<string, number>() // Sum of connections per node
    const hotNodes = new Set<string>() // Nodes on hot paths

    // Track primary port for each node (most common destination port)
    const nodePorts = new Map<string, Map<number, number>>() // nodeId -> port -> connection count

    // Count connections per node
    flows.forEach((flow) => {
      const sourceId = flow.source.namespace
        ? `${flow.source.namespace}/${flow.source.name}`
        : flow.source.name
      const destId = flow.destination.namespace
        ? `${flow.destination.namespace}/${flow.destination.name}`
        : flow.destination.name

      connectionCounts.set(sourceId, (connectionCounts.get(sourceId) || 0) + 1)
      connectionCounts.set(destId, (connectionCounts.get(destId) || 0) + 1)

      // Sum total connections
      totalConnections.set(sourceId, (totalConnections.get(sourceId) || 0) + flow.connections)
      totalConnections.set(destId, (totalConnections.get(destId) || 0) + flow.connections)

      // Track ports for destination nodes
      if (!nodePorts.has(destId)) nodePorts.set(destId, new Map())
      const portCounts = nodePorts.get(destId)!
      portCounts.set(flow.port, (portCounts.get(flow.port) || 0) + flow.connections)

      // Track hot path nodes (Phase 2.3)
      if (flow.connections >= hotPathThreshold && hotPathThreshold > 0) {
        hotNodes.add(sourceId)
        hotNodes.add(destId)
      }
    })

    // Get all ports for a node, sorted by connection count (descending)
    const getAllPorts = (nodeId: string): PortInfo[] => {
      const ports = nodePorts.get(nodeId)
      if (!ports || ports.size === 0) return []
      const portList: PortInfo[] = []
      ports.forEach((connections, port) => {
        portList.push({ port, connections })
      })
      // Sort by connection count descending
      return portList.sort((a, b) => b.connections - a.connections)
    }

    // Calculate node height based on number of ports
    const getNodeHeight = (ports: PortInfo[]): number => {
      if (ports.length === 0) return NODE_BASE_HEIGHT
      const visiblePorts = Math.min(ports.length, MAX_VISIBLE_PORTS)
      const hasMore = ports.length > MAX_VISIBLE_PORTS
      return NODE_BASE_HEIGHT + (visiblePorts * NODE_PORT_HEIGHT) + (hasMore ? NODE_PORT_HEIGHT : 0)
    }

    // Track if we have an edge to the addon group (for internet → group)
    let addonGroupEdge: { connections: number; flow: AggregatedFlow } | null = null
    // Track if we have an edge from the addon group (for group → kubernetes)
    let addonGroupOutEdge: { connections: number; flow: AggregatedFlow; targetId: string } | null = null

    flows.forEach((flow) => {
      // Special case: AddonGroupTarget is a virtual node - edge goes to the group itself
      const isAddonGroupTarget = flow.destination.kind === 'AddonGroupTarget'
      // Special case: AddonGroupSource means edge comes from the group
      const isAddonGroupSource = flow.source.kind === 'AddonGroupSource'
      // Special case: SkipEdge means create the node but don't create an edge
      const skipEdgeSource = flow.source.kind === 'SkipEdge'
      const skipEdgeDest = flow.destination.kind === 'SkipEdge'

      // Compute IDs
      const sourceId = flow.source.namespace
        ? `${flow.source.namespace}/${flow.source.name}`
        : flow.source.name
      const destId = flow.destination.namespace
        ? `${flow.destination.namespace}/${flow.destination.name}`
        : flow.destination.name

      // Create source node (skip for SkipEdge and AddonGroupSource)
      if (!skipEdgeSource && !isAddonGroupSource && !nodeMap.has(sourceId)) {
        const isAddonInternet = flow.source.kind === 'AddonInternet'
        // AddonInternet stays OUTSIDE the group - it's a separate internet entry point
        const sourceIsAddon = addonMode === 'group' && isClusterAddon(flow.source.name, flow.source.namespace)
        // Source nodes don't have inbound ports displayed (they're the source)
        nodeMap.set(sourceId, {
          id: sourceId,
          type: 'traffic',
          position: { x: 0, y: 0 },
          data: {
            label: isAddonInternet ? 'Internet' : flow.source.name, // Display "Internet" for addon internet
            namespace: flow.source.namespace,
            kind: isAddonInternet ? 'AddonInternet' : flow.source.kind, // Keep AddonInternet kind for styling
            workload: flow.source.workload,
            connections: connectionCounts.get(sourceId),
            totalConnections: totalConnections.get(sourceId),
            namespaceColor: showNamespaceGroups && !sourceIsAddon ? getNamespaceColor(flow.source.namespace) : undefined,
            isHotPath: hotNodes.has(sourceId),
            isAddonNode: sourceIsAddon, // AddonInternet is NOT an addon node
            serviceCategory: flow.source.kind.toLowerCase() === 'external' ? serviceCategories?.get(flow.source.name) : undefined,
            nodeHeight: NODE_BASE_HEIGHT,
            connLabel,
          },
        })
      }

      // For AddonGroupTarget, store for creating edge to group later
      if (isAddonGroupTarget) {
        addonGroupEdge = { connections: flow.connections, flow }
        return // Don't create node or regular edge
      }

      // For AddonGroupSource, store for creating edge from group later
      if (isAddonGroupSource) {
        // We still need to create the destination node (kubernetes)
        if (!nodeMap.has(destId)) {
          const destPorts = getAllPorts(destId)
          nodeMap.set(destId, {
            id: destId,
            type: 'traffic',
            position: { x: 0, y: 0 },
            data: {
              label: flow.destination.name,
              namespace: flow.destination.namespace,
              kind: flow.destination.kind,
              workload: flow.destination.workload,
              connections: connectionCounts.get(destId),
              totalConnections: totalConnections.get(destId),
              namespaceColor: showNamespaceGroups ? getNamespaceColor(flow.destination.namespace) : undefined,
              isHotPath: hotNodes.has(destId),
              isAddonNode: false,
              serviceCategory: undefined,
              ports: destPorts,
              nodeHeight: getNodeHeight(destPorts),
              connLabel,
            },
          })
        }
        // Store for group edge creation
        addonGroupOutEdge = { connections: flow.connections, flow, targetId: destId }
        return
      }

      // Create destination node (skip for SkipEdge dest - handled above or by source flow)
      if (!nodeMap.has(destId) && !skipEdgeDest) {
        const destIsAddon = addonMode === 'group' && isClusterAddon(flow.destination.name, flow.destination.namespace)
        const destPorts = getAllPorts(destId)
        nodeMap.set(destId, {
          id: destId,
          type: 'traffic',
          position: { x: 0, y: 0 },
          data: {
            label: flow.destination.name,
            namespace: flow.destination.namespace,
            kind: flow.destination.kind,
            workload: flow.destination.workload,
            connections: connectionCounts.get(destId),
            totalConnections: totalConnections.get(destId),
            namespaceColor: showNamespaceGroups && !destIsAddon ? getNamespaceColor(flow.destination.namespace) : undefined,
            isHotPath: hotNodes.has(destId),
            isAddonNode: destIsAddon,
            serviceCategory: flow.destination.kind.toLowerCase() === 'external' ? serviceCategories?.get(flow.destination.name) : undefined,
            ports: destPorts,
            nodeHeight: getNodeHeight(destPorts),
            connLabel,
          },
        })
      }

      // Skip creating edge for SkipEdge flows (they're just for node creation)
      if (skipEdgeSource || skipEdgeDest) {
        return
      }

      // Create edge with visual encoding (Phase 2.1, 2.2, 2.3)
      const edgeId = `${sourceId}->${destId}:${flow.port}`
      const isHotEdge = flow.connections >= hotPathThreshold && hotPathThreshold > 0
      const hasErrors = (flow.errorCount ?? 0) > 0

      // Phase 2.3: Hot path styling (orange for hot, red for errors, blue for http/grpc, cyan for dns, gray for others)
      const hasDrops = (flow.verdictCounts?.dropped ?? 0) > 0
      const strokeColor = hasErrors
        ? '#ef4444'  // red-500 for error flows
        : isHotEdge
          ? '#f97316'  // orange-500
          : flow.l7Protocol === 'HTTP' || flow.l7Protocol === 'gRPC'
            ? '#3b82f6'  // blue-500
            : flow.l7Protocol === 'DNS'
              ? '#06b6d4'  // cyan-500
              : '#6b7280'  // gray-500

      // Phase 2.1: Edge width based on connection count
      const strokeWidth = getEdgeWidth(flow.connections)

      // Phase 2.2: Edge label - connection count with unit suffix + L7 details
      const connStr = isIstio
        ? `${formatConnections(flow.connections)}/s`
        : formatConnections(flow.connections)
      const l7Label = flow.l7Protocol ? `${flow.l7Protocol} · ` : ''
      const latencyLabel = flow.latencyP50Ms ? ` · ${formatLatency(flow.latencyP50Ms)}` : ''
      const errorLabel = hasErrors
        ? ` · ${formatConnections(flow.errorCount ?? 0)} err`
        : ''
      const edgeLabel = `${l7Label}${connStr}${latencyLabel}${errorLabel}`

      edgeList.push({
        id: edgeId,
        source: sourceId,
        target: destId,
        type: 'smoothstep',
        animated: isHotEdge, // Animate hot paths
        label: edgeLabel,
        labelBgStyle: {
          fill: hasErrors ? '#7f1d1d' : isHotEdge ? '#7c2d12' : '#1f2937', // red-900, orange-900, gray-800
          fillOpacity: 0.9,
        },
        labelStyle: {
          fontSize: 10,
          fill: hasErrors ? '#fecaca' : isHotEdge ? '#fed7aa' : '#d1d5db', // red-200, orange-200, gray-300
          fontWeight: (isHotEdge || hasErrors) ? 600 : 400,
        },
        style: {
          strokeWidth,
          stroke: strokeColor,
          ...(hasDrops && { strokeDasharray: '6 3' }),
        },
        markerEnd: {
          type: MarkerType.ArrowClosed,
          width: 16,
          height: 16,
          color: strokeColor,
        },
      })
    })

    return {
      rawNodes: Array.from(nodeMap.values()),
      rawEdges: edgeList,
      addonGroupEdge, // Pass this for adding after group is created
      addonGroupOutEdge, // Pass this for adding group → kubernetes edge
    }
  }, [flows, hotPathThreshold, showNamespaceGroups, serviceCategories, addonMode, isIstio, connLabel])

  // Apply ELK layout
  const applyLayout = useCallback(async () => {
    if (rawNodes.length === 0) {
      setLayoutedNodes([])
      setLayoutedEdges([])
      return
    }

    const elkGraph = {
      id: 'root',
      layoutOptions: elkOptions,
      children: rawNodes.map(node => ({
        id: node.id,
        width: NODE_WIDTH,
        height: node.data.nodeHeight || NODE_BASE_HEIGHT,
      })),
      edges: rawEdges.map(edge => ({
        id: edge.id,
        sources: [edge.source],
        targets: [edge.target],
      })),
    }

    // Store the addon group edge info for later
    const groupEdgeInfo = addonGroupEdge
    const groupOutEdgeInfo = addonGroupOutEdge

    try {
      const layoutResult = await elk.layout(elkGraph)

      // Index ELK's positioned children by id once — a .find() per node here is
      // O(nodes²) and bites on dense traffic graphs.
      const elkPositions = new Map((layoutResult.children ?? []).map(n => [n.id, n]))

      // Apply positions from ELK to nodes
      let positionedNodes = rawNodes.map(node => {
        const elkNode = elkPositions.get(node.id)
        return {
          ...node,
          position: {
            x: elkNode?.x || 0,
            y: elkNode?.y || 0,
          },
        }
      })

      // If grouping addons, create a parent group node and set parentId on children
      let finalNodes: Node[] = positionedNodes
      if (addonMode === 'group') {
        const addonNodeIds = new Set(positionedNodes.filter(n => n.data.isAddonNode).map(n => n.id))

        if (addonNodeIds.size > 0) {
          // Calculate bounding box of addon nodes
          const padding = 24
          const labelHeight = 28
          let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity

          positionedNodes.forEach(node => {
            if (!addonNodeIds.has(node.id)) return
            const x = node.position.x
            const y = node.position.y
            const width = NODE_WIDTH
            const height = node.data.nodeHeight || NODE_BASE_HEIGHT
            minX = Math.min(minX, x)
            minY = Math.min(minY, y)
            maxX = Math.max(maxX, x + width)
            maxY = Math.max(maxY, y + height)
          })

          const groupX = minX - padding
          const groupY = minY - padding - labelHeight
          const groupWidth = maxX - minX + padding * 2
          const groupHeight = maxY - minY + padding * 2 + labelHeight

          // Create the group node
          const groupNode: Node = {
            id: 'addon-group',
            type: 'addonGroup',
            position: { x: groupX, y: groupY },
            data: {
              width: groupWidth,
              height: groupHeight,
            },
            style: { width: groupWidth, height: groupHeight },
            draggable: true,
            selectable: true,
          }

          // Update addon nodes to be children of the group (positions relative to group)
          positionedNodes = positionedNodes.map(node => {
            if (addonNodeIds.has(node.id)) {
              return {
                ...node,
                parentId: 'addon-group',
                extent: 'parent' as const,
                position: {
                  x: node.position.x - groupX,
                  y: node.position.y - groupY,
                },
              }
            }
            return node
          })

          finalNodes = [groupNode, ...positionedNodes]
        }
      }

      // Build final edges list
      const finalEdges = [...rawEdges]

      // Add edge from addon-internet to addon-group if we have one
      if (addonMode === 'group' && groupEdgeInfo) {
        const { connections } = groupEdgeInfo
        const sourceId = 'addon-internet'
        const isHotEdge = connections >= hotPathThreshold && hotPathThreshold > 0

        finalEdges.push({
          id: `${sourceId}->addon-group`,
          source: sourceId,
          target: 'addon-group',
          type: 'smoothstep',
          animated: isHotEdge,
          label: formatConnections(connections),
          labelBgStyle: {
            fill: '#581c87', // purple-900
            fillOpacity: 0.9,
          },
          labelStyle: {
            fontSize: 10,
            fill: '#e9d5ff', // purple-200
            fontWeight: 500,
          },
          style: {
            strokeWidth: getEdgeWidth(connections),
            stroke: '#a855f7', // purple-500
          },
          markerEnd: {
            type: MarkerType.ArrowClosed,
            width: 16,
            height: 16,
            color: '#a855f7',
          },
        })
      }

      // Add edge from addon-group to kubernetes if we have one
      if (addonMode === 'group' && groupOutEdgeInfo) {
        const { connections, targetId } = groupOutEdgeInfo
        const isHotEdge = connections >= hotPathThreshold && hotPathThreshold > 0

        finalEdges.push({
          id: `addon-group->${targetId}`,
          source: 'addon-group',
          target: targetId,
          type: 'smoothstep',
          animated: isHotEdge,
          label: formatConnections(connections),
          labelBgStyle: {
            fill: '#581c87', // purple-900
            fillOpacity: 0.9,
          },
          labelStyle: {
            fontSize: 10,
            fill: '#e9d5ff', // purple-200
            fontWeight: 500,
          },
          style: {
            strokeWidth: getEdgeWidth(connections),
            stroke: '#a855f7', // purple-500
          },
          markerEnd: {
            type: MarkerType.ArrowClosed,
            width: 16,
            height: 16,
            color: '#a855f7',
          },
        })
      }

      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      setLayoutedNodes(finalNodes as any)
      setLayoutedEdges(finalEdges)
    } catch (error) {
      console.error('ELK layout error:', error)
      // Fallback to simple layout
      const positionedNodes = rawNodes.map((node, index) => ({
        ...node,
        position: {
          x: 100 + (index % 3) * 250,
          y: 50 + Math.floor(index / 3) * 100,
        },
      }))
      setLayoutedNodes(positionedNodes)
      setLayoutedEdges(rawEdges)
    }
  }, [rawNodes, rawEdges, addonMode, addonGroupEdge, addonGroupOutEdge, hotPathThreshold])

  // Run layout when flows change
  useEffect(() => {
    applyLayout()
  }, [applyLayout])

  const [nodes, setNodes, onNodesChange] = useNodesState(layoutedNodes)
  const [edges, setEdges, onEdgesChange] = useEdgesState(layoutedEdges)

  // Track if we need to fit view after layout
  const shouldFitViewRef = useRef(false)
  const prevFlowCountRef = useRef(flows.length)

  // Update nodes and edges when layout changes
  useEffect(() => {
    // Check if flow count changed (filter/namespace change)
    if (flows.length !== prevFlowCountRef.current) {
      shouldFitViewRef.current = true
      prevFlowCountRef.current = flows.length
    }
    setNodes(layoutedNodes)
    setEdges(layoutedEdges)
  }, [layoutedNodes, layoutedEdges, setNodes, setEdges, flows.length])

  // Click handlers
  const onNodeClick: NodeMouseHandler<Node<TrafficNodeData>> = useCallback((_event, node) => {
    setSelection({
      type: 'node',
      id: node.id,
      data: node.data,
    })
    onSelectionChange?.({ type: 'node', nodeId: node.id })
  }, [onSelectionChange])

  const onEdgeClick: EdgeMouseHandler<Edge> = useCallback((_event, edge) => {
    const flow = flowByEdgeId.get(edge.id)
    setSelection({
      type: 'edge',
      id: edge.id,
      data: {
        source: edge.source,
        target: edge.target,
        port: flow?.port || 0,
        connections: flow?.connections || 0,
        protocol: flow?.protocol || 'tcp',
        flow,
      },
    })
    onSelectionChange?.({ type: 'edge', sourceId: edge.source, destId: edge.target, port: flow?.port })
  }, [flowByEdgeId, onSelectionChange])

  const onPaneClick = useCallback(() => {
    setSelection(null)
    onSelectionChange?.(null)
  }, [onSelectionChange])

  return (
    <div className="w-full h-full relative">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onNodeClick={onNodeClick}
        onEdgeClick={onEdgeClick}
        onPaneClick={onPaneClick}
        nodeTypes={nodeTypes}
        defaultEdgeOptions={{
          type: 'smoothstep',
          style: { strokeWidth: 2, stroke: '#6b7280' },
        }}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        proOptions={{ hideAttribution: true }}
        minZoom={0.1}
        maxZoom={2}
        edgesReconnectable={false}
        nodesConnectable={false}
      >
        <Background />
        <Controls />
        <FitViewOnChange shouldFitViewRef={shouldFitViewRef} layoutedNodes={layoutedNodes} />
      </ReactFlow>

      {/* Legend */}
      <TrafficLegend />

      {/* Details panel */}
      {selection && (
        <DetailsPanel
          selection={selection}
          onClose={() => setSelection(null)}
          flows={flows}
          isIstio={isIstio}
        />
      )}
    </div>
  )
}
