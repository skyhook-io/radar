import { Server, HardDrive, Globe, Tag, Activity, ExternalLink } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import { MetricsChart } from '../../ui/MetricsChart'
import { formatMemoryString } from '../../../utils/format'
import type { MetricsDataPoint } from '../../../types/core'

interface NodeRendererProps {
  data: any
  relationships?: { pods?: any[] }
  onViewPods?: () => void
  metrics?: { usage?: { cpu: string; memory: string }; timestamp?: string }
  metricsHistory?: { dataPoints?: MetricsDataPoint[]; collectionError?: string }
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

// Extract problems from node status and spec
function getNodeProblems(data: any): string[] {
  const problems: string[] = []
  const conditions = data.status?.conditions || []
  const spec = data.spec || {}

  // Check if unschedulable
  if (spec.unschedulable) {
    problems.push('Node is cordoned (unschedulable)')
  }

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

export function NodeRenderer({ data, relationships, onViewPods, metrics, metricsHistory, hideMetricsServer }: NodeRendererProps) {
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

  // Extract platform info from labels
  const instanceType = labels['node.kubernetes.io/instance-type']
  const zone = labels['topology.kubernetes.io/zone']
  const region = labels['topology.kubernetes.io/region']
  const nodePool = labels['cloud.google.com/gke-nodepool'] || labels['eks.amazonaws.com/nodegroup']
  const machineFamily = labels['cloud.google.com/machine-family']
  const hasPlatformInfo = instanceType || zone || region || nodePool || machineFamily

  return (
    <>
      {/* Problems alert - shown at top when there are issues */}
      {hasProblems && (
        <AlertBanner variant="error" title="Issues Detected" items={problems} />
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
        <div className="space-y-1">
          <div className="grid grid-cols-4 gap-2 text-xs text-theme-text-tertiary font-medium mb-2">
            <span>Resource</span>
            <span>Capacity</span>
            <span>Allocatable</span>
            <span>In Use</span>
          </div>
          <div className="grid grid-cols-4 gap-2 text-sm">
            <span className="text-theme-text-secondary">CPU</span>
            <span className="text-theme-text-primary">{capacity.cpu || '-'}</span>
            <span className="text-theme-text-primary">{allocatable.cpu || '-'}</span>
            <span className="text-theme-text-primary font-medium">{metrics?.usage?.cpu || '-'}</span>
          </div>
          <div className="grid grid-cols-4 gap-2 text-sm">
            <span className="text-theme-text-secondary">Memory</span>
            <span className="text-theme-text-primary">{formatMemory(capacity.memory)}</span>
            <span className="text-theme-text-primary">{formatMemory(allocatable.memory)}</span>
            <span className="text-theme-text-primary font-medium">{metrics?.usage?.memory ? formatMemory(metrics.usage.memory) : '-'}</span>
          </div>
          <div className="grid grid-cols-4 gap-2 text-sm">
            <span className="text-theme-text-secondary">Pods</span>
            <span className="text-theme-text-primary">{capacity.pods || '-'}</span>
            <span className="text-theme-text-primary">{allocatable.pods || '-'}</span>
            <span className="text-theme-text-primary font-medium">{relationships?.pods?.length ?? '-'}</span>
          </div>
          <div className="grid grid-cols-4 gap-2 text-sm">
            <span className="text-theme-text-secondary">Ephemeral Storage</span>
            <span className="text-theme-text-primary">{formatStorage(capacity['ephemeral-storage'])}</span>
            <span className="text-theme-text-primary">{formatStorage(allocatable['ephemeral-storage'])}</span>
            <span className="text-theme-text-primary">-</span>
          </div>
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
      {!hideMetricsServer && (metrics?.usage || metricsHistory?.dataPoints?.length || metricsHistory?.collectionError) && (
        <Section title="Resource Usage" icon={Activity} defaultExpanded>
          {metricsHistory?.collectionError && !metricsHistory?.dataPoints?.length && (
            <div className="mb-3 rounded-lg border border-yellow-500/30 bg-yellow-500/5 px-3 py-2 text-xs text-yellow-400">
              <span className="font-medium">Metrics collection error:</span>{' '}
              <span className="break-all">{metricsHistory.collectionError}</span>
            </div>
          )}
          {metricsHistory?.dataPoints && metricsHistory.dataPoints.length > 0 ? (
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
          ) : metrics?.usage ? (
            /* Fallback to simple display if no history yet */
            <div className="space-y-3">
              <div className="card-inner-lg">
                <div className="flex items-center justify-between mb-2">
                  <span className="text-sm font-medium text-theme-text-primary">CPU</span>
                </div>
                <div className="flex items-baseline gap-2">
                  <span className="text-lg font-medium text-blue-400">{metrics.usage.cpu}</span>
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
                  <span className="text-lg font-medium text-purple-400">{formatMemory(metrics.usage.memory)}</span>
                  <span className="text-sm text-theme-text-tertiary">
                    / {formatMemory(allocatable.memory) || formatMemory(capacity.memory) || '?'} allocatable
                  </span>
                </div>
              </div>
            </div>
          ) : (
            <div className="text-xs text-theme-text-tertiary">Collecting metrics data...</div>
          )}
          {metrics?.timestamp && (
            <div className="mt-2 text-xs text-theme-text-tertiary">
              Last updated: {new Date(metrics.timestamp).toLocaleTimeString()}
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
