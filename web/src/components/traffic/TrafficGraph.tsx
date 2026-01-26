import { useMemo, useEffect, useState, useCallback, useRef } from 'react'
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

interface TrafficGraphProps {
  flows: AggregatedFlow[]
  hotPathThreshold?: number
  showNamespaceGroups?: boolean
  serviceCategories?: Map<string, string>
  addonMode?: AddonMode
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

// Phase 4.1: Namespace color palette for grouping
// Using maximally distinct colors spread across the hue spectrum
const NAMESPACE_PALETTE = [
  '#dc2626', // red-600
  '#2563eb', // blue-600
  '#16a34a', // green-600
  '#9333ea', // purple-600
  '#ea580c', // orange-600
  '#0891b2', // cyan-600
  '#c026d3', // fuchsia-600
  '#65a30d', // lime-600
  '#0d9488', // teal-600
  '#e11d48', // rose-600
  '#7c3aed', // violet-600
  '#ca8a04', // yellow-600
  '#4f46e5', // indigo-600
  '#db2777', // pink-600
  '#059669', // emerald-600
  '#d97706', // amber-600
]

// Named colors for common environments
const NAMESPACE_NAMED_COLORS: Record<string, string> = {
  production: '#991b1b',  // Red-800
  prod: '#991b1b',
  staging: '#854d0e',     // Yellow-800
  stg: '#854d0e',
  dev: '#1e40af',         // Blue-800
  development: '#1e40af',
  default: '#374151',     // Gray-700
}

// Track assigned colors to avoid repetition (shared with sidebar)
const assignedColors = new Map<string, string>()
let colorIndex = 0

function getNamespaceColor(namespace: string | undefined): string {
  if (!namespace) return '#44403c' // Stone-700 for external
  const lower = namespace.toLowerCase()

  // Check for named environments first
  if (NAMESPACE_NAMED_COLORS[lower]) {
    return NAMESPACE_NAMED_COLORS[lower]
  }

  // Check if already assigned
  if (assignedColors.has(namespace)) {
    return assignedColors.get(namespace)!
  }

  // Assign next color in sequence (avoids hash collisions)
  const color = NAMESPACE_PALETTE[colorIndex % NAMESPACE_PALETTE.length]
  assignedColors.set(namespace, color)
  colorIndex++
  return color
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
          <Globe className="w-4 h-4 text-purple-400 flex-shrink-0" />
        ) : isInternet ? (
          <Globe className="w-4 h-4 text-sky-400 flex-shrink-0" />
        ) : isAddon ? (
          <div className="w-2 h-2 rounded-full flex-shrink-0 bg-purple-500" />
        ) : isAddonNode ? (
          <div className="w-2 h-2 rounded-full flex-shrink-0 bg-purple-400" />
        ) : (
          <div
            className={clsx(
              'w-2 h-2 rounded-full flex-shrink-0',
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
      {data.ports && data.ports.length > 0 && (
        <div className="mt-1.5 space-y-0.5">
          {data.ports.slice(0, MAX_VISIBLE_PORTS).map((portInfo) => (
            <div key={portInfo.port} className="flex items-center justify-between gap-1 text-xs">
              <span className={clsx(
                'font-mono',
                (hasNamespaceColor || isAddonNode) ? 'text-cyan-300' : 'text-blue-600 dark:text-blue-400'
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
          {data.ports.length > MAX_VISIBLE_PORTS && (
            <div className={clsx(
              'text-xs',
              (hasNamespaceColor || isAddonNode) ? 'text-white/50' : 'text-theme-text-tertiary'
            )}>
              +{data.ports.length - MAX_VISIBLE_PORTS} more
            </div>
          )}
        </div>
      )}
      {/* Total connections (only if no ports shown) */}
      {(!data.ports || data.ports.length === 0) && data.totalConnections && data.totalConnections > 0 && (
        <div className="mt-1">
          <span className={clsx(
            'text-xs truncate',
            data.isHotPath
              ? 'text-orange-400 font-medium'
              : (hasNamespaceColor || isAddonNode)
                ? 'text-white/70'
                : 'text-theme-text-tertiary'
          )}>
            {formatConnections(data.totalConnections)} conn
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
          <svg width="24" height="8" className="flex-shrink-0">
            <line x1="0" y1="4" x2="24" y2="4" stroke="#f97316" strokeWidth="2" strokeDasharray="4 2" />
          </svg>
          <span className="text-theme-text-secondary">Hot path (top 10%)</span>
        </div>
        <div className="flex items-center gap-2">
          <svg width="24" height="8" className="flex-shrink-0">
            <line x1="0" y1="4" x2="24" y2="4" stroke="#3b82f6" strokeWidth="2" />
          </svg>
          <span className="text-theme-text-secondary">HTTP / gRPC</span>
        </div>
        <div className="flex items-center gap-2">
          <svg width="24" height="8" className="flex-shrink-0">
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
}: {
  selection: Selection
  onClose: () => void
  flows: AggregatedFlow[]
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
                  Total connections: <span className="text-theme-text-primary font-medium">
                    {formatConnections(nodeData.totalConnections)}
                  </span>
                </div>
              )}
            </div>

            {/* Stats grid */}
            {nodeStats && (nodeStats.totalBytes > 0 || nodeStats.lastSeen) && (
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
                {Object.keys(nodeStats.protocols).length > 0 && (
                  <div className="p-2 rounded bg-theme-elevated text-xs col-span-2">
                    <div className="text-theme-text-tertiary mb-1">Protocols</div>
                    <div className="flex flex-wrap gap-1.5">
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
                        <ArrowRight className="h-3 w-3 text-theme-text-tertiary flex-shrink-0" />
                        <span className="text-blue-400 font-mono">:{flow.port}</span>
                      </div>
                      <div className="flex items-center gap-2 text-[10px]">
                        <span className="px-1 py-0.5 rounded bg-theme-bg text-theme-text-tertiary uppercase">
                          {flow.protocol || 'tcp'}
                        </span>
                        <span className="text-theme-text-secondary">
                          {formatConnections(flow.connections)} conn
                        </span>
                        {(flow.bytesSent > 0 || flow.bytesRecv > 0) && (
                          <span className="text-theme-text-tertiary">
                            {formatBytes(flow.bytesSent + flow.bytesRecv)}
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
                        <ArrowRight className="h-3 w-3 text-theme-text-tertiary flex-shrink-0" />
                        <span className="text-theme-text-primary truncate flex-1">
                          {flow.destination.name}
                        </span>
                        <span className="text-blue-400 font-mono">:{flow.port}</span>
                      </div>
                      <div className="flex items-center gap-2 text-[10px]">
                        <span className="px-1 py-0.5 rounded bg-theme-bg text-theme-text-tertiary uppercase">
                          {flow.protocol || 'tcp'}
                        </span>
                        <span className="text-theme-text-secondary">
                          {formatConnections(flow.connections)} conn
                        </span>
                        {(flow.bytesSent > 0 || flow.bytesRecv > 0) && (
                          <span className="text-theme-text-tertiary">
                            {formatBytes(flow.bytesSent + flow.bytesRecv)}
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
                <ArrowRight className="h-4 w-4 text-theme-text-tertiary flex-shrink-0" />
                <span className="text-theme-text-primary truncate">{edgeData.target.split('/').pop()}</span>
              </div>

              <div className="grid grid-cols-2 gap-2 text-xs">
                <div className="p-2 rounded bg-theme-elevated">
                  <div className="text-theme-text-tertiary">Port</div>
                  <div className="text-theme-text-primary font-medium">{edgeData.port}</div>
                </div>
                <div className="p-2 rounded bg-theme-elevated">
                  <div className="text-theme-text-tertiary">Protocol</div>
                  <div className="text-theme-text-primary font-medium uppercase">{edgeData.protocol}</div>
                </div>
                <div className="p-2 rounded bg-theme-elevated">
                  <div className="text-theme-text-tertiary">Connections</div>
                  <div className="text-theme-text-primary font-medium">{formatConnections(edgeData.connections)}</div>
                </div>
                {edgeData.flow && (
                  <div className="p-2 rounded bg-theme-elevated">
                    <div className="text-theme-text-tertiary">Data</div>
                    <div className="text-theme-text-primary font-medium">
                      {formatBytes(edgeData.flow.bytesSent + edgeData.flow.bytesRecv)}
                    </div>
                  </div>
                )}
              </div>

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

export function TrafficGraph({ flows, hotPathThreshold = 0, showNamespaceGroups = false, serviceCategories, addonMode = 'show' }: TrafficGraphProps) {
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

      // Phase 2.3: Hot path styling (orange for hot, blue for http/grpc, gray for others)
      const strokeColor = isHotEdge
        ? '#f97316'  // orange-500
        : flow.protocol === 'http' || flow.protocol === 'grpc'
          ? '#3b82f6'  // blue-500
          : '#6b7280'  // gray-500

      // Phase 2.1: Edge width based on connection count
      const strokeWidth = getEdgeWidth(flow.connections)

      // Phase 2.2: Edge label - just connection count (port shown on node)
      const edgeLabel = formatConnections(flow.connections)

      edgeList.push({
        id: edgeId,
        source: sourceId,
        target: destId,
        type: 'smoothstep',
        animated: isHotEdge, // Animate hot paths
        label: edgeLabel,
        labelBgStyle: {
          fill: isHotEdge ? '#7c2d12' : '#1f2937', // orange-900 for hot, gray-800 for normal
          fillOpacity: 0.9,
        },
        labelStyle: {
          fontSize: 10,
          fill: isHotEdge ? '#fed7aa' : '#d1d5db', // orange-200 for hot, gray-300 for normal
          fontWeight: isHotEdge ? 600 : 400,
        },
        style: {
          strokeWidth,
          stroke: strokeColor,
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
  }, [flows, hotPathThreshold, showNamespaceGroups, serviceCategories, addonMode])

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

      // Apply positions from ELK to nodes
      let positionedNodes = rawNodes.map(node => {
        const elkNode = layoutResult.children?.find(n => n.id === node.id)
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
      let finalEdges = [...rawEdges]

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
  }, [])

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
  }, [flowByEdgeId])

  const onPaneClick = useCallback(() => {
    setSelection(null)
  }, [])

  // FitView handler component - must be inside ReactFlow
  const FitViewOnChange = () => {
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
    }, [fitView, layoutedNodes])

    return null
  }

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
        <FitViewOnChange />
      </ReactFlow>

      {/* Legend */}
      <TrafficLegend />

      {/* Details panel */}
      {selection && (
        <DetailsPanel
          selection={selection}
          onClose={() => setSelection(null)}
          flows={flows}
        />
      )}
    </div>
  )
}
