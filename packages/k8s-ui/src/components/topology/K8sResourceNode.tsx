import { memo } from 'react'
import { Handle, Position } from '@xyflow/react'
import {
  ChevronDown,
  ChevronUp,
  TriangleAlert,
} from 'lucide-react'
import { clsx } from 'clsx'
import type { NodeKind, HealthStatus, PodSummary } from '../../types'
import { displayKind } from '../../types'
import { healthToSeverity, SEVERITY_DOT, SEVERITY_TEXT } from '../../utils/badge-colors'
import { workloadHue } from '../../utils/workload-colors'
import { ownershipOf } from '../../utils/topology-neighborhood'
import { midTruncate } from '../../utils/format'
import { Tooltip } from '../ui/Tooltip'
import { AuditBadgeTooltip, type AuditBadgeMessage } from '../audit/AuditBadgeTooltip'

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
      action: 'Open the pod to see the scheduler verdict (taints, resources, affinity).',
    },
    FailedScheduling: {
      title: 'Scheduling Failed',
      description: 'No suitable node found for this pod.',
      action: 'Check node resources, taints, tolerations, and affinity rules.',
    },
    Unschedulable: {
      title: 'Unschedulable',
      description: 'The scheduler tried every node and none fit.',
      action: 'Open the pod for the decomposed reason — arch/OS mismatch, untolerated taint, insufficient resources, or affinity.',
    },
    QuotaExceeded: {
      title: 'ResourceQuota Exceeded',
      description: 'A namespace ResourceQuota is at its hard limit, so new pods are rejected at admission.',
      action: 'Open the namespace to see quota usage; raise the quota or free usage.',
    },
    QuotaNearLimit: {
      title: 'ResourceQuota Near Limit',
      description: 'A namespace ResourceQuota is close to its hard limit and will soon block new pods.',
      action: 'Open the namespace to see quota usage.',
    },
    IPExhaustion: {
      title: 'IP Exhaustion (CNI)',
      description: 'The pod was scheduled but the CNI could not assign an IP — the node/subnet pool is exhausted.',
      action: 'Free IPs, scale the subnet/ENI pool, or move the pod to a node with capacity.',
    },
    SandboxCreationFailed: {
      title: 'Sandbox Creation Failed',
      description: 'The kubelet could not create the pod sandbox.',
      action: 'Check kubelet/CNI events on the node.',
    },
    VolumeMount: {
      title: 'Volume Mount Failed',
      description: 'The pod was scheduled but a volume could not be mounted.',
      action: 'Check the PVC/PV binding and the CSI driver on the node.',
    },
    VolumeAttach: {
      title: 'Volume Attach Failed',
      description: 'A volume could not be attached to the node.',
      action: 'Check the CSI driver and cloud-provider attach limits.',
    },
    VolumeMultiAttach: {
      title: 'Volume Multi-Attach',
      description: 'The volume is still attached to another node — a RWO volume cannot attach in two places.',
      action: 'Wait for the old pod to terminate, or cordon/drain the stale node.',
    },
    PodSecurityViolation: {
      title: 'Pod Security Violation',
      description: 'Pod Security Admission rejected the pod template at admission.',
      action: 'Align the pod securityContext with the namespace PSA level.',
    },
    WebhookDenied: {
      title: 'Admission Webhook Denied',
      description: 'A validating/mutating admission webhook rejected pod creation.',
      action: 'Check the webhook policy that denied the request.',
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

// Default dimensions for unknown CRD kinds
export const DEFAULT_NODE_DIMENSIONS = { width: 260, height: 84 }

// Node dimensions for ELK layout - sized for typical K8s resource names
export const NODE_DIMENSIONS: Record<NodeKind, { width: number; height: number }> = {
  Internet: { width: 120, height: 52 },
  Ingress: { width: 300, height: 84 },
  Gateway: { width: 300, height: 84 },
  HTTPRoute: { width: 300, height: 84 },
  GRPCRoute: { width: 300, height: 84 },
  TCPRoute: { width: 300, height: 84 },
  TLSRoute: { width: 300, height: 84 },
  Service: { width: 260, height: 84 },
  Deployment: { width: 280, height: 84 },
  Rollout: { width: 280, height: 84 },
  Application: { width: 300, height: 84 }, // ArgoCD Application
  Kustomization: { width: 300, height: 84 }, // FluxCD Kustomization
  HelmRelease: { width: 280, height: 84 }, // FluxCD HelmRelease
  GitRepository: { width: 280, height: 84 }, // FluxCD GitRepository
  DaemonSet: { width: 280, height: 84 },
  StatefulSet: { width: 280, height: 84 },
  ReplicaSet: { width: 280, height: 84 },
  Pod: { width: 300, height: 84 },
  PodGroup: { width: 200, height: 64 },
  ConfigMap: { width: 180, height: 84 },
  Secret: { width: 180, height: 84 },
  HorizontalPodAutoscaler: { width: 280, height: 84 },
  Job: { width: 180, height: 84 },
  CronJob: { width: 200, height: 84 },
  PersistentVolumeClaim: { width: 200, height: 84 },
  Namespace: { width: 180, height: 84 },
  Node: { width: 280, height: 84 },
  NodePool: { width: 260, height: 84 },
  NodeClaim: { width: 260, height: 84 },
  NodeClass: { width: 260, height: 84 },
  KnativeService: { width: 280, height: 84 },
  KnativeConfiguration: { width: 280, height: 84 },
  KnativeRevision: { width: 280, height: 84 },
  KnativeRoute: { width: 280, height: 84 },
  Broker: { width: 280, height: 84 },
  Channel: { width: 280, height: 84 },
  Trigger: { width: 280, height: 84 },
  PingSource: { width: 280, height: 84 },
  ApiServerSource: { width: 280, height: 84 },
  ContainerSource: { width: 280, height: 84 },
  SinkBinding: { width: 280, height: 84 },
  IngressRoute: { width: 280, height: 84 },
  IngressRouteTCP: { width: 280, height: 84 },
  IngressRouteUDP: { width: 280, height: 84 },
  Middleware: { width: 280, height: 84 },
  MiddlewareTCP: { width: 280, height: 84 },
  TraefikService: { width: 280, height: 84 },
  ServersTransport: { width: 280, height: 84 },
  ServersTransportTCP: { width: 300, height: 84 },
  TLSOption: { width: 280, height: 84 },
  TLSStore: { width: 280, height: 84 },
  HTTPProxy: { width: 280, height: 84 }, // Contour
  CAPICluster: { width: 280, height: 84 }, // Cluster API
  MachineDeployment: { width: 300, height: 84 },
  MachineSet: { width: 280, height: 84 },
  Machine: { width: 260, height: 84 },
  MachinePool: { width: 280, height: 84 },
  KubeadmControlPlane: { width: 300, height: 84 },
  ClusterClass: { width: 280, height: 84 },
  MachineHealthCheck: { width: 300, height: 84 },
}


// Status indicator color (for dot and left bar) - uses centralized severity colors
function getStatusDotColor(status: HealthStatus): string {
  const severity = healthToSeverity(status)
  return SEVERITY_DOT[severity]
}

// Cached style objects for status states (avoid creating new objects each render)
const STATUS_STYLES: Record<HealthStatus, React.CSSProperties> = {
  degraded: {
    border: '2px solid rgb(234 179 8 / 0.6)',
    backgroundColor: 'rgb(251 146 60 / 0.12)',
  },
  unhealthy: {
    border: '2px solid rgb(239 68 68 / 0.7)',
    backgroundColor: 'rgb(248 113 113 / 0.15)',
  },
  // neutral = intentional/idle (suspended, scaled-to-0) — sky outline, calm.
  // Distinct from `unknown`/`healthy` which carry no outline.
  neutral: {
    border: '2px solid rgb(56 189 248 / 0.55)',
    backgroundColor: 'rgb(56 189 248 / 0.1)',
  },
  healthy: {},
  unknown: {},
}

function getStatusStyle(status: HealthStatus): React.CSSProperties {
  return STATUS_STYLES[status] || STATUS_STYLES.healthy
}


// Format subtitle based on node kind. In summary mode the pod tier is
// collapsed, so workload/service nodes carry a podSummary — append it so the
// count of pods (and any unhealthy/pending) is still visible without children.
function getSubtitle(kind: NodeKind, nodeData: Record<string, unknown>): string {
  const base = baseSubtitle(kind, nodeData)
  const ps = nodeData.podSummary as PodSummary | undefined
  if (ps && SUMMARY_POD_KINDS.has(kind)) {
    let suffix = `${ps.total} pods`
    if (ps.unhealthy > 0) suffix += ` (${ps.unhealthy} unhealthy)`
    else if (ps.degraded > 0) suffix += ` (${ps.degraded} pending)`
    return base ? `${base} • ${suffix}` : suffix
  }
  return base
}

// Kinds that own pods and therefore carry a podSummary in summary mode.
const SUMMARY_POD_KINDS = new Set<NodeKind>([
  'Deployment', 'StatefulSet', 'DaemonSet', 'Rollout', 'Job', 'Service',
])

function baseSubtitle(kind: NodeKind, nodeData: Record<string, unknown>): string {
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
    case 'Gateway': {
      const listeners = nodeData.listenerCount as number || 0
      const addresses = nodeData.addresses as string[]
      const addr = addresses?.length ? addresses[0] : ''
      return addr ? `${listeners} listeners • ${addr}` : `${listeners} listeners`
    }
    case 'HTTPRoute':
    case 'GRPCRoute':
    case 'TCPRoute':
    case 'TLSRoute': {
      const hostnames = nodeData.hostnames as string[]
      const rulesCount = nodeData.rulesCount as number || 0
      const host = hostnames?.length ? hostnames[0] : ''
      return host ? `${host} • ${rulesCount} rules` : `${rulesCount} rules`
    }
    case 'HorizontalPodAutoscaler': {
      const min = nodeData.minReplicas ?? 1
      const max = nodeData.maxReplicas ?? 10
      const current = nodeData.current ?? 0
      return `${current} (${min}-${max})`
    }
    case 'ConfigMap':
      return `${nodeData.keys ?? 0} keys`
    case 'Secret':
      return `${nodeData.keys ?? 0} keys`
    case 'PersistentVolumeClaim': {
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
    case 'KnativeService': {
      const url = nodeData.url as string
      if (url) {
        // Show just the hostname from the URL for compactness
        try {
          return new URL(url).hostname
        } catch {
          return url
        }
      }
      return (nodeData.latestRevision as string) || ''
    }
    case 'Node': {
      const instanceType = nodeData.instanceType as string
      return instanceType || ''
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
    /** Hover-focus: when a sibling workload is focused, non-members dim. */
    dimmed?: boolean
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
  const { kind, name, status, nodeData, selected, dimmed, onExpand, onCollapse, isExpanded } = data
  // Cluster Audit findings joined onto this node by the host (web/ enriches each
  // node's data by auditKey). The host only counts "badge-worthy" findings —
  // reference-integrity / lifecycle, "this resource is actually broken" — not the
  // posture/best-practice nags that fire near-universally, so the indicator stays
  // a signal. Colored by worst severity (danger red, else warning amber).
  const auditDanger = typeof nodeData.auditDanger === 'number' ? nodeData.auditDanger : 0
  const auditWarning = typeof nodeData.auditWarning === 'number' ? nodeData.auditWarning : 0
  const auditTotal = auditDanger + auditWarning
  const auditMessages = Array.isArray(nodeData.auditMessages) ? (nodeData.auditMessages as AuditBadgeMessage[]) : []
  // Workload tint (application graph): a node owned by exactly one workload
  // carries that workload's hue. Only on healthy/unknown cards — degraded/
  // unhealthy already own the card background for health, which must win.
  const { ownerColorIndex } = ownershipOf(nodeData)
  const hue = ownerColorIndex !== null && (status === 'healthy' || status === 'unknown')
    ? workloadHue(ownerColorIndex)
    : undefined
  const subtitle = getSubtitle(kind, nodeData)
  const isInternet = kind === 'Internet'
  const isPodGroup = kind === 'PodGroup'
  const isSmallNode = kind === 'ConfigMap' || kind === 'Secret' || kind === 'HorizontalPodAutoscaler'
  const canExpand = isPodGroup && onExpand && !isExpanded
  const canCollapse = isPodGroup && onCollapse && isExpanded
  const statusIssue = nodeData.statusIssue as string | undefined
  const issueTooltip = getIssueTooltip(statusIssue)
  const policyStatus = nodeData.policyStatus as string | undefined

  // CSS class for icon (replaces Lucide SVG - saves ~5 DOM elements per node)
  const iconClass = `topology-icon topology-icon-${kind.toLowerCase()}`

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
            'selection border border-skyhook-500/30',
            'shadow-lg shadow-skyhook-500/20',
            selected && 'ring-2 ring-skyhook-400'
          )}
        >
          <span className="topology-icon topology-icon-internet" style={{ width: 20, height: 20 }} />
          <span className="text-sm font-medium text-skyhook-300">Internet</span>
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
          'relative rounded-lg overflow-hidden transition-opacity duration-150',
          'bg-theme-surface topology-node-card',
          selected && 'topology-node-selected',
          !selected && dimmed && 'opacity-30',
          isSmallNode && !dimmed && 'opacity-90',
          // Status bar via CSS pseudo-element (defined in index.css)
          (status === 'healthy' || status === 'unknown') && 'topology-node-status-bar',
          status === 'healthy' && 'topology-node-status-healthy',
          status === 'unknown' && 'topology-node-status-unknown'
        )}
        style={{
          width: NODE_DIMENSIONS[kind]?.width ?? DEFAULT_NODE_DIMENSIONS.width,
          ...getStatusStyle(status),
        }}
      >
        {/* Workload tint — layered over the surface, inset past the 4px status bar */}
        {hue && (
          <div
            className="pointer-events-none absolute inset-y-0 right-0 rounded-r-lg"
            style={{ left: 4, background: hue.wash }}
          />
        )}

        {/* Content */}
        <div className={clsx(
          'pl-3 pr-3',
          isSmallNode ? 'py-2' : 'py-2.5'
        )}>
          {/* Header row: icon + kind label + (right-aligned) policy badge + expand/collapse + status dot */}
          <div className="flex items-center gap-1.5 mb-0.5">
            <span className={iconClass} />
            <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary font-medium">
              {isPodGroup ? 'Pod Group' : displayKind(kind)}
            </span>
            <div className="ml-auto flex items-center gap-1.5">
              {policyStatus && (
                <Tooltip content={policyStatus === 'protected' ? 'Protected by NetworkPolicy' : 'No NetworkPolicy coverage'} position="right">
                  <span className={clsx(
                    'inline-flex items-center justify-center w-3 h-3 rounded-sm text-[8px] font-bold cursor-help leading-none',
                    policyStatus === 'protected'
                      ? 'bg-green-500/20 text-green-500 border border-green-500/30'
                      : 'bg-yellow-500/20 text-yellow-500 border border-yellow-500/30',
                  )}>
                    {policyStatus === 'protected' ? '✓' : '!'}
                  </span>
                </Tooltip>
              )}
              {canExpand && (
                <button
                  onClick={(e) => {
                    e.stopPropagation()
                    onExpand(id)
                  }}
                  className="p-0.5 hover:bg-theme-elevated rounded transition-colors"
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
                  className="p-0.5 hover:bg-theme-elevated rounded transition-colors"
                  title="Collapse back to group"
                >
                  <ChevronUp className="w-3.5 h-3.5 text-theme-text-secondary" />
                </button>
              )}
              {auditTotal > 0 && (
                <Tooltip
                  content={auditMessages.length > 0
                    ? <AuditBadgeTooltip messages={auditMessages} clickHint={false} />
                    : `${auditTotal} audit ${auditTotal === 1 ? 'finding' : 'findings'}${auditDanger > 0 ? ` · ${auditDanger} danger` : ''}`}
                  position="right"
                >
                  <TriangleAlert className={clsx('w-3 h-3 cursor-help', auditDanger > 0 ? SEVERITY_TEXT.error : SEVERITY_TEXT.warning)} />
                </Tooltip>
              )}
              {issueTooltip ? (
                <Tooltip content={issueTooltip} position="right">
                  <span className={clsx('w-1.5 h-1.5 rounded-full cursor-help', getStatusDotColor(status))} />
                </Tooltip>
              ) : (
                <span className={clsx('w-1.5 h-1.5 rounded-full', getStatusDotColor(status))} />
              )}
            </div>
          </div>

          {/* Name — middle-ellipsis so the differentiating suffix survives
              (a column of pods sharing a long prefix must stay tellable apart). */}
          <div className="text-sm font-medium text-theme-text-primary truncate pr-1">
            {midTruncate(name, 34)}
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
