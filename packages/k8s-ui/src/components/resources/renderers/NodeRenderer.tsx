import { Server, HardDrive, Globe, Tag, Activity, ExternalLink } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import { MetricsChart } from '../../ui/MetricsChart'
import { formatMemoryString } from '../../../utils/format'
import { getExtendedCapacityRows } from '../../../utils/extended-resources'
import type { MetricsDataPoint } from '../../../types/core'
import { MetricsUnavailableNotice } from './MetricsUnavailableNotice'

interface NodeRendererProps {
  data: any
  relationships?: { pods?: any[] }
  onViewPods?: () => void
  metrics?: { usage?: { cpu: string; memory: string }; timestamp?: string }
  metricsHistory?: { dataPoints?: MetricsDataPoint[]; collectionError?: string; metricsUnavailableReason?: string; metricsUnavailableDiagnosis?: string }
  metricsUnavailable?: boolean
  hideMetricsServer?: boolean
}

// Helper to handle undefined values
function formatMemory(value: string | undefined): string {
  if (!value) return '-'
  return formatMemoryString(value)
}

// Format storage values the same way as memory
function formatStorage(value: string | undefined): string {
  return formatMemory(value)
}

// Extract genuine problems from node status. Cordoned (unschedulable) is
// deliberately NOT included here — it's an intentional operator action
// (cordon/drain), surfaced separately as a calm advisory, not a red error.
function getNodeProblems(data: any): string[] {
  const problems: string[] = []
  const conditions = data.status?.conditions || []

  for (const cond of conditions) {
    // NotReady is a problem when status is not True
    if (cond.type === 'Ready' && cond.status !== 'True') {
      problems.push(`Node is NotReady${cond.message ? ': ' + cond.message : ''}`)
    }

    // These conditions are problems when True
    if (cond.status === 'True') {
      if (cond.type === 'DiskPressure') {
        problems.push(`Disk pressure${cond.message ? ': ' + cond.message : ''}`)
      }
      if (cond.type === 'MemoryPressure') {
        problems.push(`Memory pressure${cond.message ? ': ' + cond.message : ''}`)
      }
      if (cond.type === 'PIDPressure') {
        problems.push(`PID pressure${cond.message ? ': ' + cond.message : ''}`)
      }
      if (cond.type === 'NetworkUnavailable') {
        problems.push(`Network unavailable${cond.message ? ': ' + cond.message : ''}`)
      }
    }
  }

  return problems
}

export function NodeRenderer({ data, relationships, onViewPods, metrics, metricsHistory, metricsUnavailable, hideMetricsServer }: NodeRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const metadata = data.metadata || {}
  const labels = metadata.labels || {}
  const nodeInfo = status.nodeInfo || {}
  const capacity = status.capacity || {}
  const allocatable = status.allocatable || {}
  const addresses = status.addresses || []
  const taints = spec.taints || []

  // Check for problems
  const problems = getNodeProblems(data)
  const hasProblems = problems.length > 0
  const isCordoned = !!spec.unschedulable
  const showMetricsUnavailable = !!metricsUnavailable && !metricsHistory?.collectionError
  const hasMetricsHistory = !!metricsHistory?.dataPoints?.length
  const currentMetrics = metricsUnavailable ? undefined : metrics

  // Extract platform info from labels
  const instanceType = labels['node.kubernetes.io/instance-type']
  const zone = labels['topology.kubernetes.io/zone']
  const region = labels['topology.kubernetes.io/region']
  const nodePool = labels['cloud.google.com/gke-nodepool'] || labels['eks.amazonaws.com/nodegroup']
  const machineFamily = labels['cloud.google.com/machine-family']
  const hasPlatformInfo = instanceType || zone || region || nodePool || machineFamily

  return (
    <>
      {/* Problems alert - shown at top when there are genuine issues */}
      {hasProblems && (
        <AlertBanner variant="error" title="Issues Detected" items={problems} />
      )}

      {/* Cordoned is intentional but consequential — it removes scheduling
          capacity and a forgotten cordon strands a node. So it's a warning (amber),
          matching the node table badge + the Cordoned audit check — NOT the calm
          sky of a no-op intentional state (suspended/idle), and not a red error. */}
      {isCordoned && (
        <AlertBanner
          variant="warning"
          title="Cordoned (unschedulable)"
          message="New pods won't be scheduled here. Uncordon to resume scheduling."
        />
      )}

      {/* Node Info */}
      <Section title="Node Info" icon={Server}>
        <PropertyList>
          <Property label="OS" value={nodeInfo.osImage} />
          <Property label="Architecture" value={nodeInfo.architecture} />
          <Property label="Kernel" value={nodeInfo.kernelVersion} />
          <Property label="Container Runtime" value={nodeInfo.containerRuntimeVersion} />
          <Property label="Kubelet" value={nodeInfo.kubeletVersion} />
          <Property label="Kube-Proxy" value={nodeInfo.kubeProxyVersion} />
        </PropertyList>
      </Section>

      {/* Capacity */}
      <Section title="Capacity" icon={HardDrive}>
        <div className="space-y-2">
          {[
            {
              label: 'CPU',
              capacity: capacity.cpu,
              allocatable: allocatable.cpu,
              inUse: currentMetrics?.usage?.cpu,
            },
            {
              label: 'Memory',
              capacity: formatMemory(capacity.memory),
              allocatable: formatMemory(allocatable.memory),
              inUse: currentMetrics?.usage?.memory ? formatMemory(currentMetrics.usage.memory) : undefined,
            },
            {
              label: 'Pods',
              capacity: capacity.pods,
              allocatable: allocatable.pods,
              inUse: relationships?.pods?.length,
            },
            {
              label: 'Ephemeral Storage',
              capacity: formatStorage(capacity['ephemeral-storage']),
              allocatable: formatStorage(allocatable['ephemeral-storage']),
              inUse: undefined,
            },
            ...getExtendedCapacityRows(capacity, allocatable).map((row) => ({
              label: row.key,
              capacity: row.capacity,
              allocatable: row.allocatable,
              inUse: undefined,
            })),
          ].map((row) => (
            <div key={row.label} className="card-inner">
              <div className="text-xs font-medium text-theme-text-secondary mb-1">{row.label}</div>
              <div className="space-y-0.5 text-xs">
                <div className="flex items-baseline justify-between gap-2">
                  <span className="text-theme-text-tertiary">Capacity</span>
                  <span className="text-theme-text-primary tabular-nums">{row.capacity ?? '-'}</span>
                </div>
                <div className="flex items-baseline justify-between gap-2">
                  <span className="text-theme-text-tertiary">Allocatable</span>
                  <span className="text-theme-text-primary tabular-nums">{row.allocatable ?? '-'}</span>
                </div>
                <div className="flex items-baseline justify-between gap-2">
                  <span className="text-theme-text-tertiary">In Use</span>
                  <span className="text-theme-text-primary font-medium tabular-nums">{row.inUse ?? '-'}</span>
                </div>
              </div>
            </div>
          ))}
        </div>
        {onViewPods && (
          <div className="mt-3 pt-3 border-t border-theme-border">
            <button
              onClick={onViewPods}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-blue-400 hover:text-blue-300 bg-blue-500/10 hover:bg-blue-500/20 border border-blue-500/30 rounded transition-colors"
            >
              <ExternalLink className="w-3 h-3" />
              View Pods
            </button>
          </div>
        )}
      </Section>

      {/* Resource Usage (from metrics-server) — hidden when Prometheus has CPU/memory data */}
      {!hideMetricsServer && (currentMetrics?.usage || hasMetricsHistory || metricsHistory?.collectionError || showMetricsUnavailable) && (
        <Section title="Resource Usage" icon={Activity} defaultExpanded>
          {showMetricsUnavailable && (
            <MetricsUnavailableNotice rawError={metricsHistory?.metricsUnavailableReason} diagnosis={metricsHistory?.metricsUnavailableDiagnosis} />
          )}
          {metricsHistory?.collectionError && (
            <div className="mb-3 rounded-lg border border-yellow-500/30 bg-yellow-500/5 px-3 py-2 text-xs text-yellow-400">
              <span className="font-medium">Metrics collection error:</span>{' '}
              <span className="break-all">{metricsHistory.collectionError}</span>
            </div>
          )}
          {hasMetricsHistory && metricsHistory?.dataPoints ? (
            <div className="space-y-4">
              {/* CPU Usage with Chart */}
              <div className="card-inner-lg">
                <div className="text-xs text-theme-text-tertiary mb-1 flex items-center justify-between">
                  <span>CPU</span>
                  <span className="text-theme-text-quaternary">
                    {allocatable.cpu || capacity.cpu || '?'} allocatable
                  </span>
                </div>
                <MetricsChart
                  dataPoints={metricsHistory.dataPoints}
                  type="cpu"
                  height={60}
                  showAxis={true}
                />
              </div>

              {/* Memory Usage with Chart */}
              <div className="card-inner-lg">
                <div className="text-xs text-theme-text-tertiary mb-1 flex items-center justify-between">
                  <span>Memory</span>
                  <span className="text-theme-text-quaternary">
                    {formatMemory(allocatable.memory) || formatMemory(capacity.memory) || '?'} allocatable
                  </span>
                </div>
                <MetricsChart
                  dataPoints={metricsHistory.dataPoints}
                  type="memory"
                  height={60}
                  showAxis={true}
                />
              </div>
            </div>
          ) : showMetricsUnavailable ? null : currentMetrics?.usage ? (
            /* Fallback to simple display if no history yet */
            <div className="space-y-3">
              <div className="card-inner-lg">
                <div className="flex items-center justify-between mb-2">
                  <span className="text-sm font-medium text-theme-text-primary">CPU</span>
                </div>
                <div className="flex items-baseline gap-2">
                  <span className="text-lg font-medium text-blue-400">{currentMetrics.usage.cpu}</span>
                  <span className="text-sm text-theme-text-tertiary">
                    / {allocatable.cpu || capacity.cpu || '?'} allocatable
                  </span>
                </div>
              </div>
              <div className="card-inner-lg">
                <div className="flex items-center justify-between mb-2">
                  <span className="text-sm font-medium text-theme-text-primary">Memory</span>
                </div>
                <div className="flex items-baseline gap-2">
                  <span className="text-lg font-medium text-purple-400">{formatMemory(currentMetrics.usage.memory)}</span>
                  <span className="text-sm text-theme-text-tertiary">
                    / {formatMemory(allocatable.memory) || formatMemory(capacity.memory) || '?'} allocatable
                  </span>
                </div>
              </div>
            </div>
          ) : (
            <div className="text-xs text-theme-text-tertiary">Collecting metrics data...</div>
          )}
          {!showMetricsUnavailable && currentMetrics?.timestamp && (
            <div className="mt-2 text-xs text-theme-text-tertiary">
              Last updated: {new Date(currentMetrics.timestamp).toLocaleTimeString()}
            </div>
          )}
        </Section>
      )}

      {/* Addresses */}
      {addresses.length > 0 && (
        <Section title="Addresses" icon={Globe}>
          <PropertyList>
            {addresses.map((addr: any) => (
              <Property key={`${addr.type}-${addr.address}`} label={addr.type} value={addr.address} />
            ))}
          </PropertyList>
        </Section>
      )}

      {/* Platform Info */}
      {hasPlatformInfo && (
        <Section title="Platform" icon={Tag}>
          <PropertyList>
            <Property label="Instance Type" value={instanceType} />
            <Property label="Zone" value={zone} />
            <Property label="Region" value={region} />
            <Property label="Node Pool" value={nodePool} />
            <Property label="Machine Family" value={machineFamily} />
          </PropertyList>
        </Section>
      )}

      {/* Taints */}
      {taints.length > 0 && (
        <Section title={`Taints (${taints.length})`} defaultExpanded={taints.length <= 5}>
          <div className="space-y-1">
            {taints.map((taint: any, i: number) => (
              <div key={`${taint.key}-${taint.effect}-${i}`} className="text-sm">
                <span className={clsx(
                  'badge',
                  taint.effect === 'NoSchedule' ? 'bg-yellow-500/20 text-yellow-400' :
                  taint.effect === 'NoExecute' ? 'bg-red-500/20 text-red-400' :
                  'bg-blue-500/20 text-blue-400'
                )}>
                  {taint.key}{taint.value ? `=${taint.value}` : ''}:{taint.effect}
                </span>
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Conditions */}
      <ConditionsSection conditions={status.conditions} />
    </>
  )
}
