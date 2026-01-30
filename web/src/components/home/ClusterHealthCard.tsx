import type { DashboardResponse, DashboardMetrics, DashboardCRDCount, DashboardProblem } from '../../api/client'
import { HealthRing } from './HealthRing'
import {
  AlertTriangle, CheckCircle, XCircle,
  Cpu, MemoryStick, Database, Container, Globe, Network as NetworkIcon, Briefcase, Clock,
  ArrowRight, Server, Boxes,
} from 'lucide-react'
import { clsx } from 'clsx'
import { formatCPUMillicores, formatMemoryMiB } from '../../utils/format'

interface ClusterHealthCardProps {
  health: DashboardResponse['health']
  counts: DashboardResponse['resourceCounts']
  cluster: DashboardResponse['cluster']
  metrics: DashboardMetrics | null
  topCRDs: DashboardCRDCount[]
  problems: DashboardProblem[]
  onNavigateToKind: (kind: string, group?: string) => void
  onNavigateToView: () => void
  onWarningEventsClick?: () => void
  onUnhealthyClick?: () => void
}

// Get platform display name and icon path
function getPlatformInfo(platform: string): { name: string; icon: string | null } {
  const platformLower = platform.toLowerCase()
  if (platformLower.includes('gke') || platformLower.includes('google')) {
    return { name: 'Google Kubernetes Engine', icon: '/icons/google_kubernetes_engine.png' }
  }
  if (platformLower.includes('eks') || platformLower.includes('amazon') || platformLower.includes('aws')) {
    return { name: 'Amazon EKS', icon: '/icons/aws_eks.png' }
  }
  if (platformLower.includes('aks') || platformLower.includes('azure')) {
    return { name: 'Azure Kubernetes Service', icon: '/icons/azure-aks.svg' }
  }
  if (platformLower.includes('openshift')) {
    return { name: 'OpenShift', icon: null }
  }
  if (platformLower.includes('rancher')) {
    return { name: 'Rancher', icon: null }
  }
  if (platformLower.includes('k3s')) {
    return { name: 'K3s', icon: null }
  }
  if (platformLower.includes('kind')) {
    return { name: 'kind', icon: null }
  }
  if (platformLower.includes('minikube')) {
    return { name: 'Minikube', icon: null }
  }
  if (platformLower.includes('docker')) {
    return { name: 'Docker Desktop', icon: null }
  }
  return { name: platform || 'Kubernetes', icon: null }
}

export function ClusterHealthCard({
  health,
  counts,
  cluster,
  metrics,
  topCRDs: _topCRDs,
  problems,
  onNavigateToKind,
  onNavigateToView,
  onWarningEventsClick,
  onUnhealthyClick,
}: ClusterHealthCardProps) {
  void _topCRDs // Reserved for future CRD display

  // Pods ring segments
  const podsTotal = health.healthy + health.warning + health.error
  const podsRingSegments = [
    { value: health.healthy, color: '#22c55e' }, // green-500
    { value: health.warning, color: '#eab308' }, // yellow-500
    { value: health.error, color: '#ef4444' },   // red-500
  ]

  // Deployments ring segments
  const deploymentsRingSegments = [
    { value: counts.deployments.available, color: '#22c55e' },
    { value: counts.deployments.unavailable, color: '#ef4444' },
  ]

  // Nodes ring segments
  const nodesRingSegments = [
    { value: counts.nodes.ready, color: '#22c55e' },
    { value: counts.nodes.notReady, color: '#ef4444' },
  ]

  // Simple 6 core resources
  const secondaryResources = [
    { kind: 'statefulsets', label: 'StatefulSets', icon: Database, total: counts.statefulSets.total, subtitle: `${counts.statefulSets.ready} ready`, hasIssues: counts.statefulSets.unready > 0 },
    { kind: 'daemonsets', label: 'DaemonSets', icon: Container, total: counts.daemonSets.total, subtitle: `${counts.daemonSets.ready} ready`, hasIssues: counts.daemonSets.unready > 0 },
    { kind: 'services', label: 'Services', icon: Globe, total: counts.services },
    { kind: 'ingresses', label: 'Ingresses', icon: NetworkIcon, total: counts.ingresses },
    { kind: 'jobs', label: 'Jobs', icon: Briefcase, total: counts.jobs.total, subtitle: `${counts.jobs.active} active`, hasIssues: counts.jobs.failed > 0 },
    { kind: 'cronjobs', label: 'CronJobs', icon: Clock, total: counts.cronJobs.total, subtitle: `${counts.cronJobs.active} active` },
  ]
  const platformInfo = getPlatformInfo(cluster.platform)

  return (
    <div className="rounded-lg border border-theme-border-light bg-theme-surface/50 overflow-hidden">
      {/* Main health section - three columns */}
      <div className="px-6 py-5 border-b border-theme-border-light">
        <div className="flex items-stretch gap-8">
          {/* Left: Cluster info */}
          <div className="flex flex-col justify-center min-w-[180px] pr-8 border-r border-theme-border/50">
            <div className="flex items-center gap-2 mb-2">
              {platformInfo.icon ? (
                <img src={platformInfo.icon} alt={platformInfo.name} className="w-5 h-5 object-contain" />
              ) : (
                <Server className="w-4 h-4 text-theme-text-tertiary" />
              )}
              <span className="text-xs text-theme-text-secondary">{platformInfo.name}</span>
            </div>
            <h2 className="text-sm font-semibold text-theme-text-primary truncate mb-1" title={cluster.name}>
              {cluster.name || 'Cluster'}
            </h2>
            <div className="flex flex-col gap-1 text-xs text-theme-text-tertiary">
              {cluster.version && (
                <span>Kubernetes {cluster.version}</span>
              )}
              <span>{counts.namespaces} namespaces</span>
            </div>
            {/* Action buttons for issues */}
            <div className="flex flex-col gap-2 mt-3">
              {health.warningEvents > 0 && (
                <button
                  onClick={onWarningEventsClick}
                  title="Native Kubernetes Warning events (e.g., ImagePullBackOff, FailedScheduling)"
                  className="flex items-center gap-1.5 w-fit px-2.5 py-1.5 bg-yellow-500/10 hover:bg-yellow-500/20 border border-yellow-500/30 rounded-md transition-colors cursor-pointer"
                >
                  <AlertTriangle className="w-3.5 h-3.5 text-yellow-500" />
                  <span className="text-xs text-yellow-500 font-medium">{health.warningEvents} Warning Events</span>
                </button>
              )}
              {problems.length > 0 && (
                <button
                  onClick={onUnhealthyClick}
                  title="View timeline of unhealthy/degraded workload events"
                  className="flex items-center gap-1.5 w-fit px-2.5 py-1.5 bg-red-500/10 hover:bg-red-500/20 border border-red-500/30 rounded-md transition-colors cursor-pointer"
                >
                  <AlertTriangle className="w-3.5 h-3.5 text-red-500" />
                  <span className="text-xs text-red-500 font-medium">View unhealthy workload events</span>
                </button>
              )}
            </div>
          </div>

          {/* Center: Three health rings */}
          <div className="flex-1 flex items-center justify-center gap-12">
            {/* Pods Ring */}
            <button
              onClick={() => onNavigateToKind('pods')}
              className="flex flex-col items-center gap-2 cursor-pointer hover:-translate-y-1 hover:scale-105 transition-all duration-200"
            >
              <HealthRing segments={podsRingSegments} size={88} strokeWidth={8} label={String(podsTotal)} />
              <span className="text-xs font-medium text-theme-text-secondary">Pods</span>
              <div className="flex items-center gap-2 text-[11px]">
                {health.healthy > 0 && (
                  <span className="flex items-center gap-0.5 text-green-500">
                    <CheckCircle className="w-3 h-3" />
                    {health.healthy}
                  </span>
                )}
                {health.warning > 0 && (
                  <span className="flex items-center gap-0.5 text-yellow-500">
                    <AlertTriangle className="w-3 h-3" />
                    {health.warning}
                  </span>
                )}
                {health.error > 0 && (
                  <span className="flex items-center gap-0.5 text-red-500">
                    <XCircle className="w-3 h-3" />
                    {health.error}
                  </span>
                )}
              </div>
            </button>

            {/* Deployments Ring */}
            <button
              onClick={() => onNavigateToKind('deployments')}
              className="flex flex-col items-center gap-2 cursor-pointer hover:-translate-y-1 hover:scale-105 transition-all duration-200"
            >
              <HealthRing segments={deploymentsRingSegments} size={88} strokeWidth={8} label={String(counts.deployments.total)} />
              <span className="text-xs font-medium text-theme-text-secondary">Deployments</span>
              <div className="flex items-center gap-2 text-[11px]">
                <span className="text-green-500">{counts.deployments.available} available</span>
                {counts.deployments.unavailable > 0 && (
                  <span className="text-red-500">{counts.deployments.unavailable} unavailable</span>
                )}
              </div>
            </button>

            {/* Nodes Ring */}
            <button
              onClick={() => onNavigateToKind('nodes')}
              className="flex flex-col items-center gap-2 cursor-pointer hover:-translate-y-1 hover:scale-105 transition-all duration-200"
            >
              <HealthRing segments={nodesRingSegments} size={88} strokeWidth={8} label={String(counts.nodes.total)} />
              <span className="text-xs font-medium text-theme-text-secondary">Nodes</span>
              <div className="flex items-center gap-2 text-[11px]">
                <span className="text-green-500">{counts.nodes.ready} ready</span>
                {counts.nodes.notReady > 0 && (
                  <span className="text-red-500">{counts.nodes.notReady} not ready</span>
                )}
              </div>
            </button>
          </div>

          {/* Right: Resource utilization */}
          <div className="flex flex-col justify-center min-w-[280px] pl-8 border-l border-theme-border/50">
            <div className="flex items-center gap-2 mb-3">
              <Boxes className="w-4 h-4 text-theme-text-tertiary" />
              <span className="text-xs text-theme-text-secondary">Resource Utilization</span>
            </div>

            <div className="space-y-3">
              {metrics?.cpu && (
                <div className="space-y-2">
                  <div className="flex items-center gap-1.5 text-xs text-theme-text-secondary">
                    <Cpu className="w-3.5 h-3.5 text-theme-text-tertiary" />
                    CPU
                  </div>
                  <ResourceBar
                    label="Used"
                    used={formatCPUMillicores(metrics.cpu.usageMillis)}
                    total={formatCPUMillicores(metrics.cpu.capacityMillis)}
                    percent={metrics.cpu.usagePercent}
                  />
                  <ResourceBar
                    label="Requested"
                    used={formatCPUMillicores(metrics.cpu.requestsMillis)}
                    total={formatCPUMillicores(metrics.cpu.capacityMillis)}
                    percent={metrics.cpu.requestPercent}
                  />
                </div>
              )}
              {metrics?.memory && (
                <div className="space-y-2">
                  <div className="flex items-center gap-1.5 text-xs text-theme-text-secondary">
                    <MemoryStick className="w-3.5 h-3.5 text-theme-text-tertiary" />
                    Memory
                  </div>
                  <ResourceBar
                    label="Used"
                    used={formatMemoryMiB(metrics.memory.usageMillis)}
                    total={formatMemoryMiB(metrics.memory.capacityMillis)}
                    percent={metrics.memory.usagePercent}
                  />
                  <ResourceBar
                    label="Requested"
                    used={formatMemoryMiB(metrics.memory.requestsMillis)}
                    total={formatMemoryMiB(metrics.memory.capacityMillis)}
                    percent={metrics.memory.requestPercent}
                  />
                </div>
              )}
              {!metrics?.cpu && !metrics?.memory && (
                <span className="text-xs text-theme-text-tertiary">Metrics unavailable</span>
              )}
            </div>

          </div>
        </div>
      </div>

      {/* Secondary resources row + Browse All button */}
      <div className="flex">
        {/* Left: Resources row - evenly spread */}
        <div className="flex-1 grid grid-cols-6 px-4 py-2.5 bg-theme-surface/30">
          {secondaryResources.map((res) => (
            <button
              key={res.kind}
              onClick={() => onNavigateToKind(res.kind)}
              className="flex items-center justify-center gap-1.5 px-2 py-1 rounded hover:bg-theme-hover transition-colors cursor-pointer text-sm"
            >
              <res.icon className={clsx('w-3.5 h-3.5', res.hasIssues ? 'text-yellow-500' : 'text-theme-text-tertiary')} />
              <span className="text-theme-text-primary font-medium">{res.total}</span>
              <span className="text-theme-text-secondary">{res.label}</span>
              {res.subtitle && (
                <span className={clsx('text-xs', res.hasIssues ? 'text-yellow-500' : 'text-theme-text-tertiary')}>
                  ({res.subtitle})
                </span>
              )}
            </button>
          ))}
        </div>

        {/* Right: Browse All Resources button */}
        <button
          onClick={onNavigateToView}
          className="flex items-center gap-2 px-5 text-sm font-medium text-blue-500 hover:text-blue-400 hover:bg-blue-500/5 transition-colors cursor-pointer border-l border-theme-border shrink-0"
        >
          Browse All Resources
          <ArrowRight className="w-4 h-4" />
        </button>
      </div>
    </div>
  )
}

function ResourceBar({
  label,
  used,
  total,
  percent,
}: {
  label: string
  used: string
  total: string
  percent: number
}) {
  const barColor = percent > 85 ? 'bg-red-500' : percent > 60 ? 'bg-yellow-500' : 'bg-green-500'

  return (
    <div>
      <div className="flex justify-between items-baseline mb-0.5">
        <span className="text-[10px] text-theme-text-tertiary">{label}: {used} / {total}</span>
        <span className="text-[10px] font-medium text-theme-text-secondary">{percent}%</span>
      </div>
      <div className="h-2 bg-theme-border rounded overflow-hidden">
        <div
          className={clsx('h-full transition-all', barColor)}
          style={{ width: `${Math.min(percent, 100)}%` }}
        />
      </div>
    </div>
  )
}
