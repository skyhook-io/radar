import { memo } from 'react'
import { Handle, Position } from '@xyflow/react'
import {
  ChevronDown,
  ChevronUp,
} from 'lucide-react'
import { getTopologyIcon } from '../../utils/resource-icons'
import { clsx } from 'clsx'
import type { NodeKind, HealthStatus } from '../../types'
import { healthToSeverity, SEVERITY_DOT } from '../../utils/badge-colors'
import { Tooltip } from '../ui/Tooltip'

// Get actionable tooltip content for health issues
function getIssueTooltip(issue: string | undefined): React.ReactNode {
  if (!issue) return null

  const issueDetails: Record<string, { title: string; description: string; action: string }> = {
    OOMKilled: {
      title: 'Out of Memory (OOMKilled)',
      description: 'Container exceeded its memory limit and was killed by the kernel.',
      action: 'Increase memory limits or optimize memory usage.',
    },
    CrashLoopBackOff: {
      title: 'CrashLoopBackOff',
      description: 'Container is repeatedly crashing and Kubernetes is backing off restarts.',
      action: 'Check container logs for crash reason.',
    },
    ImagePullBackOff: {
      title: 'ImagePullBackOff',
      description: 'Kubernetes cannot pull the container image.',
      action: 'Verify image name, tag, and registry credentials.',
    },
    ErrImagePull: {
      title: 'Image Pull Error',
      description: 'Failed to pull the container image.',
      action: 'Check image name and registry access.',
    },
    CreateContainerConfigError: {
      title: 'Container Config Error',
      description: 'Invalid container configuration (e.g., missing ConfigMap/Secret).',
      action: 'Verify referenced ConfigMaps and Secrets exist.',
    },
    Pending: {
      title: 'Pending',
      description: 'Pod is waiting to be scheduled to a node.',
      action: 'Check for resource constraints or node availability.',
    },
    FailedScheduling: {
      title: 'Scheduling Failed',
      description: 'No suitable node found for this pod.',
      action: 'Check node resources, taints, tolerations, and affinity rules.',
    },
    Evicted: {
      title: 'Pod Evicted',
      description: 'Pod was evicted from the node (usually due to resource pressure).',
      action: 'Check node resource usage and set appropriate resource requests.',
    },
  }

  const details = issueDetails[issue]
  if (!details) {
    return (
      <div className="max-w-xs">
        <div className="font-medium text-red-400">{issue}</div>
        <div className="text-theme-text-secondary text-[10px] mt-1">Click to view details</div>
      </div>
    )
  }

  return (
    <div className="max-w-xs">
      <div className="font-medium text-red-400">{details.title}</div>
      <div className="text-theme-text-secondary text-[10px] mt-1">{details.description}</div>
      <div className="text-blue-400 text-[10px] mt-1.5 border-t border-theme-border pt-1.5">
        💡 {details.action}
      </div>
    </div>
  )
}

// Node dimensions for ELK layout - sized for typical K8s resource names
export const NODE_DIMENSIONS: Record<NodeKind, { width: number; height: number }> = {
  Internet: { width: 120, height: 52 },
  Ingress: { width: 300, height: 56 },
  Service: { width: 260, height: 56 },
  Deployment: { width: 280, height: 56 },
  Rollout: { width: 280, height: 56 },
  Application: { width: 300, height: 56 }, // ArgoCD Application
  Kustomization: { width: 300, height: 56 }, // FluxCD Kustomization
  HelmRelease: { width: 280, height: 56 }, // FluxCD HelmRelease
  GitRepository: { width: 280, height: 56 }, // FluxCD GitRepository
  DaemonSet: { width: 280, height: 56 },
  StatefulSet: { width: 280, height: 56 },
  ReplicaSet: { width: 280, height: 56 },
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
  return getTopologyIcon(kind)
}

// Status indicator color (for dot and left bar) - uses centralized severity colors
function getStatusDotColor(status: HealthStatus): string {
  const severity = healthToSeverity(status)
  return SEVERITY_DOT[severity]
}

// Border style for problem states - wraps entire card
function getStatusBorderStyle(status: HealthStatus): React.CSSProperties {
  switch (status) {
    case 'degraded':
      return { border: '2px solid rgb(234 179 8 / 0.6)' } // yellow-500/60
    case 'unhealthy':
      return { border: '2px solid rgb(239 68 68 / 0.7)' } // red-500/70
    default:
      return {}
  }
}

// Background style for problem states - works in both light and dark mode
function getStatusBgStyle(status: HealthStatus): React.CSSProperties {
  switch (status) {
    case 'degraded':
      // Warm orange/yellow tint
      return { backgroundColor: 'rgb(251 146 60 / 0.12)' } // orange-400/12
    case 'unhealthy':
      // Red tint
      return { backgroundColor: 'rgb(248 113 113 / 0.15)' } // red-400/15
    default:
      return {}
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
    case 'Rollout':
    case 'DaemonSet':
    case 'StatefulSet':
    case 'ReplicaSet':
      return 'text-emerald-400'
    case 'Application':
      return 'text-orange-400' // ArgoCD brand color
    case 'Kustomization':
    case 'HelmRelease':
      return 'text-sky-400' // FluxCD - distinct from ArgoCD
    case 'GitRepository':
      return 'text-teal-400' // FluxCD source
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
      return 'text-theme-text-secondary'
  }
}

// Format subtitle based on node kind
function getSubtitle(kind: NodeKind, nodeData: Record<string, unknown>): string {
  switch (kind) {
    case 'Deployment':
    case 'Rollout':
    case 'DaemonSet':
    case 'StatefulSet':
    case 'ReplicaSet': {
      // Use statusSummary if available (includes issue info like "0/3 OOMKilled")
      const statusSummary = nodeData.statusSummary as string
      if (statusSummary) {
        return statusSummary
      }
      const ready = nodeData.readyReplicas ?? 0
      const total = nodeData.totalReplicas ?? 0
      return `${ready}/${total} ready`
    }
    case 'Application': {
      // ArgoCD Application - show sync and health status
      const syncStatus = (nodeData.syncStatus as string) || 'Unknown'
      const healthStatus = (nodeData.healthStatus as string) || 'Unknown'
      return `${syncStatus} • ${healthStatus}`
    }
    case 'Kustomization': {
      // FluxCD Kustomization - show ready status and resource count
      const ready = (nodeData.ready as string) || 'Unknown'
      const resources = nodeData.resourceCount as number
      return resources ? `${ready} • ${resources} resources` : ready
    }
    case 'HelmRelease': {
      // FluxCD HelmRelease - show ready status and revision
      const ready = (nodeData.ready as string) || 'Unknown'
      const revision = nodeData.revision as number
      return revision ? `${ready} • rev ${revision}` : ready
    }
    case 'GitRepository': {
      // FluxCD GitRepository - show ready status and branch/revision
      const ready = (nodeData.ready as string) || 'Unknown'
      const branch = nodeData.branch as string
      return branch ? `${ready} • ${branch}` : ready
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
  const statusIssue = nodeData.statusIssue as string | undefined
  const issueTooltip = getIssueTooltip(statusIssue)

  // Special styling for Internet node
  const InternetIcon = getTopologyIcon('Internet')

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
          <InternetIcon className="w-5 h-5 text-blue-400" />
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
          'bg-theme-surface topology-node-card',
          'transition-all duration-150',
          selected && 'ring-2 ring-blue-400',
          isSmallNode ? 'opacity-90' : ''
        )}
        style={{
          width: NODE_DIMENSIONS[kind]?.width || 180,
          border: 'none',
          ...getStatusBorderStyle(status),
          ...getStatusBgStyle(status),
        }}
      >
        {/* Status bar on left - only shown for healthy/unknown states */}
        {(status === 'healthy' || status === 'unknown') && (
          <div
            className={clsx(
              'absolute left-0 top-0 bottom-0 w-1',
              getStatusDotColor(status)
            )}
          />
        )}

        {/* Content */}
        <div className={clsx(
          'pl-3 pr-3',
          isSmallNode ? 'py-2' : 'py-2.5'
        )}>
          {/* Header row: icon + kind label + expand/collapse + status dot */}
          <div className="flex items-center gap-1.5 mb-0.5">
            <Icon className={clsx('w-3.5 h-3.5', getIconColor(kind))} />
            <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary font-medium">
              {isPodGroup ? 'Pod Group' : kind}
            </span>
            {/* Expand/Collapse button for PodGroup */}
            {canExpand && (
              <button
                onClick={(e) => {
                  e.stopPropagation()
                  onExpand(id)
                }}
                className="ml-auto p-0.5 hover:bg-theme-elevated rounded transition-colors"
                title="Expand to show individual pods"
              >
                <ChevronDown className="w-3.5 h-3.5 text-theme-text-secondary" />
              </button>
            )}
            {canCollapse && (
              <button
                onClick={(e) => {
                  e.stopPropagation()
                  onCollapse(id)
                }}
                className="ml-auto p-0.5 hover:bg-theme-elevated rounded transition-colors"
                title="Collapse back to group"
              >
                <ChevronUp className="w-3.5 h-3.5 text-theme-text-secondary" />
              </button>
            )}
            {issueTooltip ? (
              <Tooltip content={issueTooltip} position="right">
                <span
                  className={clsx(
                    canExpand || canCollapse ? '' : 'ml-auto',
                    'w-1.5 h-1.5 rounded-full cursor-help',
                    getStatusDotColor(status)
                  )}
                />
              </Tooltip>
            ) : (
              <span
                className={clsx(
                  canExpand || canCollapse ? '' : 'ml-auto',
                  'w-1.5 h-1.5 rounded-full',
                  getStatusDotColor(status)
                )}
              />
            )}
          </div>

          {/* Name */}
          <div className="text-sm font-medium text-theme-text-primary truncate pr-1">
            {name}
          </div>

          {/* Subtitle */}
          {subtitle && (
            <div className="text-xs text-theme-text-secondary truncate mt-0.5">
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
