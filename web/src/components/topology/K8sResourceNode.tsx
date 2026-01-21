import { memo } from 'react'
import { Handle, Position } from '@xyflow/react'
import {
  Globe,
  Network,
  Server,
  Box,
  Layers,
  Container,
  FileJson,
  Lock,
  Gauge,
  Clock,
  CalendarClock,
  Database,
  HardDrive,
  ChevronDown,
  ChevronUp,
  Boxes,
} from 'lucide-react'
import { clsx } from 'clsx'
import type { NodeKind, HealthStatus } from '../../types'

// Node dimensions for ELK layout - sized for typical K8s resource names
export const NODE_DIMENSIONS: Record<NodeKind, { width: number; height: number }> = {
  Internet: { width: 120, height: 52 },
  Ingress: { width: 280, height: 56 },
  Service: { width: 220, height: 56 },
  Deployment: { width: 240, height: 56 },
  DaemonSet: { width: 240, height: 56 },
  StatefulSet: { width: 240, height: 56 },
  ReplicaSet: { width: 240, height: 56 },
  Pod: { width: 300, height: 56 },
  PodGroup: { width: 200, height: 64 },
  ConfigMap: { width: 180, height: 48 },
  Secret: { width: 180, height: 48 },
  HPA: { width: 160, height: 48 },
  Job: { width: 180, height: 56 },
  CronJob: { width: 200, height: 56 },
  PVC: { width: 200, height: 48 },
  Namespace: { width: 180, height: 48 },
}

// Icon mapping for node kinds
function getIcon(kind: NodeKind) {
  switch (kind) {
    case 'Internet':
      return Globe
    case 'Ingress':
      return Network
    case 'Service':
      return Server
    case 'Deployment':
    case 'DaemonSet':
    case 'StatefulSet':
      return Box
    case 'ReplicaSet':
      return Layers
    case 'Pod':
      return Container
    case 'PodGroup':
      return Boxes
    case 'ConfigMap':
      return FileJson
    case 'Secret':
      return Lock
    case 'HPA':
      return Gauge
    case 'Job':
      return Clock
    case 'CronJob':
      return CalendarClock
    case 'PVC':
      return HardDrive
    case 'Namespace':
      return Database
    default:
      return Box
  }
}

// Status bar color (left border)
function getStatusBarColor(status: HealthStatus): string {
  switch (status) {
    case 'healthy':
      return 'bg-green-500'
    case 'degraded':
      return 'bg-yellow-500'
    case 'unhealthy':
      return 'bg-red-500'
    default:
      return 'bg-slate-500'
  }
}

// Icon color based on kind
function getIconColor(kind: NodeKind): string {
  switch (kind) {
    case 'Internet':
      return 'text-blue-400'
    case 'Ingress':
      return 'text-violet-400'
    case 'Service':
      return 'text-blue-400'
    case 'Deployment':
    case 'DaemonSet':
    case 'StatefulSet':
    case 'ReplicaSet':
      return 'text-emerald-400'
    case 'Pod':
    case 'PodGroup':
      return 'text-lime-400'
    case 'ConfigMap':
      return 'text-amber-400'
    case 'Secret':
      return 'text-red-400'
    case 'HPA':
      return 'text-pink-400'
    case 'Job':
    case 'CronJob':
      return 'text-purple-400'
    case 'PVC':
      return 'text-cyan-400'
    default:
      return 'text-slate-400'
  }
}

// Format subtitle based on node kind
function getSubtitle(kind: NodeKind, nodeData: Record<string, unknown>): string {
  switch (kind) {
    case 'Deployment':
    case 'DaemonSet':
    case 'StatefulSet':
    case 'ReplicaSet': {
      const ready = nodeData.readyReplicas ?? 0
      const total = nodeData.totalReplicas ?? 0
      return `${ready}/${total} ready`
    }
    case 'Pod':
      return (nodeData.phase as string) || 'Unknown'
    case 'Service': {
      const svcType = (nodeData.type as string) || 'ClusterIP'
      const port = nodeData.port
      return port ? `${svcType} :${port}` : svcType
    }
    case 'Ingress':
      return (nodeData.hostname as string) || 'No host'
    case 'HPA': {
      const min = nodeData.minReplicas ?? 1
      const max = nodeData.maxReplicas ?? 10
      const current = nodeData.current ?? 0
      return `${current} (${min}-${max})`
    }
    case 'ConfigMap':
      return `${nodeData.keys ?? 0} keys`
    case 'Secret':
      return `${nodeData.keys ?? 0} keys`
    case 'PVC': {
      const storage = (nodeData.storage as string) || ''
      const phase = (nodeData.phase as string) || ''
      return storage ? `${storage} (${phase})` : phase
    }
    case 'PodGroup': {
      const count = (nodeData.podCount as number) || 0
      const healthy = (nodeData.healthy as number) || 0
      const unhealthy = (nodeData.unhealthy as number) || 0
      if (unhealthy > 0) {
        return `${count} pods (${unhealthy} unhealthy)`
      }
      return `${count} pods (${healthy} healthy)`
    }
    case 'Internet':
      return ''
    default:
      return ''
  }
}

interface K8sResourceNodeProps {
  data: {
    kind: NodeKind
    name: string
    status: HealthStatus
    nodeData: Record<string, unknown>
    selected?: boolean
    onExpand?: (nodeId: string) => void
    onCollapse?: (nodeId: string) => void
    isExpanded?: boolean
  }
  id: string
}

export const K8sResourceNode = memo(function K8sResourceNode({
  data,
  id,
}: K8sResourceNodeProps) {
  const { kind, name, status, nodeData, selected, onExpand, onCollapse, isExpanded } = data
  const Icon = getIcon(kind)
  const subtitle = getSubtitle(kind, nodeData)
  const isInternet = kind === 'Internet'
  const isPodGroup = kind === 'PodGroup'
  const isSmallNode = kind === 'ConfigMap' || kind === 'Secret' || kind === 'HPA'
  const canExpand = isPodGroup && onExpand && !isExpanded
  const canCollapse = isPodGroup && onCollapse && isExpanded

  // Special styling for Internet node
  if (isInternet) {
    return (
      <>
        <Handle
          type="target"
          position={Position.Left}
          className="!bg-transparent !border-0 !w-0 !h-0"
        />
        <div
          className={clsx(
            'flex items-center gap-2 px-4 py-2 rounded-full',
            'bg-blue-500/10 border border-blue-500/30',
            'shadow-lg shadow-blue-500/20',
            selected && 'ring-2 ring-blue-400'
          )}
        >
          <Globe className="w-5 h-5 text-blue-400" />
          <span className="text-sm font-medium text-blue-300">Internet</span>
          <span className="w-2 h-2 rounded-full bg-green-500" />
        </div>
        <Handle
          type="source"
          position={Position.Right}
          className="!bg-transparent !border-0 !w-0 !h-0"
        />
      </>
    )
  }

  return (
    <>
      <Handle
        type="target"
        position={Position.Left}
        className="!bg-transparent !border-0 !w-0 !h-0"
      />

      <div
        className={clsx(
          'relative rounded-lg overflow-hidden',
          'bg-slate-800 border border-slate-600',
          'shadow-md shadow-black/20',
          'transition-all duration-150',
          selected && 'ring-2 ring-indigo-400 border-indigo-400',
          isSmallNode ? 'opacity-90' : ''
        )}
        style={{
          minWidth: NODE_DIMENSIONS[kind]?.width || 180,
        }}
      >
        {/* Status bar on left */}
        <div
          className={clsx(
            'absolute left-0 top-0 bottom-0 w-1',
            getStatusBarColor(status)
          )}
        />

        {/* Content */}
        <div className={clsx(
          'pl-3 pr-3',
          isSmallNode ? 'py-2' : 'py-2.5'
        )}>
          {/* Header row: icon + kind label + expand/collapse + status dot */}
          <div className="flex items-center gap-1.5 mb-0.5">
            <Icon className={clsx('w-3.5 h-3.5', getIconColor(kind))} />
            <span className="text-[10px] uppercase tracking-wide text-slate-500 font-medium">
              {isPodGroup ? 'Pod Group' : kind}
            </span>
            {/* Expand/Collapse button for PodGroup */}
            {canExpand && (
              <button
                onClick={(e) => {
                  e.stopPropagation()
                  onExpand(id)
                }}
                className="ml-auto p-0.5 hover:bg-slate-700 rounded transition-colors"
                title="Expand to show individual pods"
              >
                <ChevronDown className="w-3.5 h-3.5 text-slate-400" />
              </button>
            )}
            {canCollapse && (
              <button
                onClick={(e) => {
                  e.stopPropagation()
                  onCollapse(id)
                }}
                className="ml-auto p-0.5 hover:bg-slate-700 rounded transition-colors"
                title="Collapse back to group"
              >
                <ChevronUp className="w-3.5 h-3.5 text-slate-400" />
              </button>
            )}
            <span
              className={clsx(
                canExpand || canCollapse ? '' : 'ml-auto',
                'w-1.5 h-1.5 rounded-full',
                getStatusBarColor(status)
              )}
            />
          </div>

          {/* Name */}
          <div className="text-sm font-medium text-white truncate pr-1">
            {name}
          </div>

          {/* Subtitle */}
          {subtitle && (
            <div className="text-xs text-slate-400 truncate mt-0.5">
              {subtitle}
            </div>
          )}
        </div>
      </div>

      <Handle
        type="source"
        position={Position.Right}
        className="!bg-transparent !border-0 !w-0 !h-0"
      />
    </>
  )
})
