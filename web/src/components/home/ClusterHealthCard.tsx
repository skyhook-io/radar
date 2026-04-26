import { useState } from 'react'
import type { DashboardResponse, DashboardMetrics, DashboardCRDCount, DashboardProblem } from '../../api/client'
import { HealthRing } from './HealthRing'
import {
  AlertTriangle, CheckCircle, XCircle,
  Cpu, MemoryStick, Database, Container, Globe, Network as NetworkIcon, Briefcase, Clock,
  ArrowRight, Server, Boxes, Shield, Radio, Info,
} from 'lucide-react'
import { clsx } from 'clsx'
import { formatCPUMillicores, formatMemoryMiB } from '../../utils/format'
import { useCapabilitiesContext } from '../../contexts/CapabilitiesContext'
import { MCPSetupDialog } from './MCPSetupDialog'
import { pluralize, parseContextName } from '@skyhook-io/k8s-ui'
import { Tooltip } from '../ui/Tooltip'

interface ClusterHealthCardProps {
  health: DashboardResponse['health']
  counts: DashboardResponse['resourceCounts']
  cluster: DashboardResponse['cluster']
  metrics: DashboardMetrics | null
  metricsServerAvailable: boolean
  topCRDs?: DashboardCRDCount[] // Loaded lazily, may be undefined
  problems: DashboardProblem[]
  nodeVersionSkew: DashboardResponse['nodeVersionSkew']
  onNavigateToKind: (kind: string, group?: string) => void
  onNavigateToView: () => void
  onWarningEventsClick?: () => void
  onUnhealthyClick?: () => void
}

function getMetricsInstallHint(platform: string): string {
  const p = platform.toLowerCase()
  if (p.includes('minikube')) return 'minikube addons enable metrics-server'
  if (p.includes('gke') || p.includes('aks')) return 'metrics-server is usually pre-installed on this platform — check if it was disabled or removed'
  if (p.includes('eks')) return 'kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml'
  return 'kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml'
}

function MetricsUnavailableHint({ platform, metricsServerAvailable }: { platform: string; metricsServerAvailable: boolean }) {
  if (metricsServerAvailable) {
    return <span className="text-xs text-theme-text-tertiary">Waiting for metrics data...</span>
  }

  const hint = getMetricsInstallHint(platform)
  const isPreInstalled = platform.toLowerCase().includes('gke') || platform.toLowerCase().includes('aks')

  return (
    <Tooltip
      content={
        <div className="space-y-1">
          <div className="font-medium">How to fix</div>
          <div>{isPreInstalled ? hint : <>Install by running:<br /><code className="text-[10px] opacity-80">{hint}</code></>}</div>
        </div>
      }
      position="bottom"
      className="!whitespace-normal !max-w-sm"
    >
      <span className="flex items-center gap-1.5 text-xs text-theme-text-tertiary">
        <Info className="w-3 h-3 shrink-0" />
        <span>Requires <span className="text-theme-text-secondary">metrics-server</span> to display CPU & memory usage</span>
      </span>
    </Tooltip>
  )
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
  metricsServerAvailable,
  topCRDs: _topCRDs,
  problems,
  nodeVersionSkew,
  onNavigateToKind,
  onNavigateToView,
  onWarningEventsClick,
  onUnhealthyClick,
}: ClusterHealthCardProps) {
  void _topCRDs // Reserved for future CRD display

  const [mcpDialogOpen, setMcpDialogOpen] = useState(false)
  const { mcpEnabled } = useCapabilitiesContext()
  const mcpUrl = `${window.location.origin}/mcp`

  const restricted = counts.restricted ?? []
  const isRestricted = (kind: string) => restricted.includes(kind)

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
  const cordonedCount = counts.nodes.cordoned ?? 0
  const nodesRingSegments = [
    { value: counts.nodes.ready, color: '#22c55e' },
    { value: cordonedCount, color: '#eab308' }, // amber for cordoned
    { value: counts.nodes.notReady, color: '#ef4444' },
  ]

  // Secondary resource counts
  // Show whichever networking type has more resources: Ingresses or Routes (Gateway API)
  const routeCount = counts.routes ?? 0
  const ingressCount = counts.ingresses ?? 0

  type SecondaryResource = { kind: string; group?: string; label: string; icon: typeof Globe; total: number; subtitle?: string; hasIssues?: boolean }
  const secondaryResources: SecondaryResource[] = [
    { kind: 'statefulsets', label: 'StatefulSets', icon: Database, total: counts.statefulSets.total, subtitle: `${counts.statefulSets.ready} ready`, hasIssues: counts.statefulSets.unready > 0 },
    { kind: 'daemonsets', label: 'DaemonSets', icon: Container, total: counts.daemonSets.total, subtitle: `${counts.daemonSets.ready} ready`, hasIssues: counts.daemonSets.unready > 0 },
    { kind: 'services', label: 'Services', icon: Globe, total: counts.services },
    routeCount > ingressCount
      ? { kind: 'httproutes', group: 'gateway.networking.k8s.io', label: 'Routes', icon: Globe, total: routeCount }
      : { kind: 'ingresses', label: 'Ingresses', icon: NetworkIcon, total: ingressCount },
    { kind: 'jobs', label: 'Jobs', icon: Briefcase, total: counts.jobs.total, subtitle: `${counts.jobs.active} active`, hasIssues: counts.jobs.failed > 0 },
    { kind: 'cronjobs', label: 'CronJobs', icon: Clock, total: counts.cronJobs.total, subtitle: `${counts.cronJobs.active} active` },
  ]
  const platformInfo = getPlatformInfo(cluster.platform)
  // The raw cluster.name is the kubeconfig context (e.g.
  // `gke_koalabackend_us-east1-b_nonprod-cluster-us-east1`). That string
  // is the user's primary orientation cue, but the bit they actually
  // recognize is the short clusterName. We promote that, push the raw
  // path into a tooltip, and surface project/region as muted metadata.
  const parsedContext = parseContextName(cluster.name || '')
  const headlineName = parsedContext.clusterName || cluster.name || 'Cluster'

  return (
    <div className="rounded-xl bg-theme-surface shadow-theme-sm overflow-hidden">
      {/* Main health section - three columns */}
      <div className="px-6 py-5 border-b border-theme-border/50">
        <div className="flex items-stretch gap-8">
          {/* Left: Cluster info */}
          <div className="flex flex-col justify-center w-[300px] shrink-0 pr-8 border-r border-theme-border/50">
            <div className="flex items-center gap-2 mb-1.5">
              {platformInfo.icon ? (
                <img src={platformInfo.icon} alt={platformInfo.name} className="w-5 h-5 object-contain" />
              ) : (
                <Server className="w-4 h-4 text-theme-text-tertiary" />
              )}
              <span className="text-xs text-theme-text-secondary truncate">{platformInfo.name}</span>
            </div>
            <h2
              className="text-xl font-semibold text-theme-text-primary truncate mb-1.5 leading-tight"
              title={cluster.name}
            >
              {headlineName}
            </h2>
            <div className="flex flex-col gap-0.5 text-xs text-theme-text-tertiary">
              {(parsedContext.account || parsedContext.region) && (
                <span className="truncate font-mono" title={[parsedContext.account, parsedContext.region].filter(Boolean).join(' · ')}>
                  {[parsedContext.account, parsedContext.region].filter(Boolean).join(' · ')}
                </span>
              )}
              {cluster.version && (
                <span>Kubernetes {cluster.version}</span>
              )}
              <span><span className="font-mono">{counts.namespaces}</span> namespaces</span>
              {cluster.name && cluster.name !== headlineName && (
                <span
                  className="font-mono text-[10px] text-theme-text-disabled break-all leading-snug pt-0.5"
                  title={cluster.name}
                >
                  {cluster.name}
                </span>
              )}
            </div>
            {nodeVersionSkew && (
              <Tooltip
                content={
                  <div className="space-y-1.5">
                    <div className="font-medium">Node version skew detected</div>
                    {Object.entries(nodeVersionSkew.versions).map(([version, nodes]) => (
                      <div key={version}>
                        <span className="font-mono font-medium">v{version}</span>
                        <span className="text-theme-text-tertiary"> — {pluralize(nodes.length, 'node')}</span>
                        <div className="text-[10px] text-theme-text-tertiary pl-2">{nodes.join(', ')}</div>
                      </div>
                    ))}
                  </div>
                }
                position="bottom"
                className="!whitespace-normal !max-w-sm"
              >
                <span className="flex items-center gap-1.5 mt-1 text-xs text-yellow-500">
                  <AlertTriangle className="w-3 h-3 shrink-0" />
                  Version skew: v{nodeVersionSkew.minVersion} — v{nodeVersionSkew.maxVersion}
                </span>
              </Tooltip>
            )}
            {/* MCP Server indicator */}
            {mcpEnabled && (
              <button
                onClick={() => setMcpDialogOpen(true)}
                className="flex items-center gap-2 mt-3 px-2.5 py-2 bg-purple-500/5 hover:bg-purple-500/10 border border-purple-500/20 rounded-md transition-colors w-full"
              >
                <Radio className="w-3.5 h-3.5 text-purple-400 animate-pulse shrink-0" />
                <div className="flex flex-col gap-0.5 min-w-0 flex-1 text-left">
                  <span className="text-xs font-medium text-purple-400">MCP Server Live</span>
                  <span className="text-[10px] text-theme-text-tertiary truncate font-mono" title={mcpUrl}>
                    HTTP · {mcpUrl}
                  </span>
                </div>
                <Info className="w-3.5 h-3.5 text-purple-400/60 shrink-0" />
              </button>
            )}
            <MCPSetupDialog open={mcpDialogOpen} onClose={() => setMcpDialogOpen(false)} mcpUrl={mcpUrl} />
          </div>

          {/* Center: Three health rings */}
          <div className="flex-1 flex items-center justify-center gap-12">
            {/* Pods Ring */}
            {isRestricted('pods') ? (
              <RestrictedRing label="Pods" />
            ) : (
              <button
                onClick={() => onNavigateToKind('pods')}
                className="flex flex-col items-center gap-2 hover:-translate-y-1 hover:scale-105 transition-all duration-200"
              >
                <HealthRing segments={podsRingSegments} size={88} strokeWidth={8} label={String(podsTotal)} />
                <span className="text-sm font-semibold uppercase tracking-wider text-theme-text-secondary">Pods</span>
                <div className="flex items-center gap-2 text-xs font-mono">
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
            )}

            {/* Deployments Ring */}
            {isRestricted('deployments') ? (
              <RestrictedRing label="Deployments" />
            ) : (
              <button
                onClick={() => onNavigateToKind('deployments')}
                className="flex flex-col items-center gap-2 hover:-translate-y-1 hover:scale-105 transition-all duration-200"
              >
                <HealthRing segments={deploymentsRingSegments} size={88} strokeWidth={8} label={String(counts.deployments.total)} />
                <span className="text-sm font-semibold uppercase tracking-wider text-theme-text-secondary">Deployments</span>
                <div className="flex items-center gap-2 text-xs font-mono">
                  <span className="text-green-500">{counts.deployments.available} available</span>
                  {counts.deployments.unavailable > 0 && (
                    <span className="text-red-500">{counts.deployments.unavailable} unavailable</span>
                  )}
                </div>
              </button>
            )}

            {/* Nodes Ring */}
            {isRestricted('nodes') ? (
              <RestrictedRing label="Nodes" />
            ) : (
              <button
                onClick={() => onNavigateToKind('nodes')}
                className="flex flex-col items-center gap-2 hover:-translate-y-1 hover:scale-105 transition-all duration-200"
              >
                <HealthRing segments={nodesRingSegments} size={88} strokeWidth={8} label={String(counts.nodes.total)} />
                <span className="text-sm font-semibold uppercase tracking-wider text-theme-text-secondary">Nodes</span>
                <div className="flex items-center gap-2 text-xs font-mono">
                  <span className="text-green-500">{counts.nodes.ready} ready</span>
                  {cordonedCount > 0 && (
                    <span className="text-yellow-500">{cordonedCount} cordoned</span>
                  )}
                  {counts.nodes.notReady > 0 && (
                    <span className="text-red-500">{counts.nodes.notReady} not ready</span>
                  )}
                </div>
              </button>
            )}
          </div>

          {/* Right: Resource utilization */}
          <div className="flex flex-col justify-center w-[300px] shrink-0 pl-8 border-l border-theme-border/50">
            <div className="flex items-center gap-2 mb-3">
              <Boxes className="w-4 h-4 text-theme-text-tertiary" />
              <span className="text-[10px] uppercase tracking-wider text-theme-text-tertiary">Resource Utilization</span>
            </div>

            <div className="space-y-3">
              {metrics?.cpu && (
                <div className="space-y-2">
                  <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-theme-text-tertiary">
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
                  <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-theme-text-tertiary">
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
                <MetricsUnavailableHint platform={cluster.platform} metricsServerAvailable={metricsServerAvailable} />
              )}
            </div>

          </div>
        </div>
      </div>

      {/* Secondary resources row — matches top row's 3-column layout */}
      <div className="flex items-stretch px-6 py-2.5 bg-theme-surface/30">
        {/* Left column: Warning indicators (aligned with cluster info) */}
        <div className="flex flex-col justify-center gap-1 w-1/4 shrink-0 pr-4 border-r border-theme-border/50">
          {health.warningEvents > 0 && (
            <button
              onClick={onWarningEventsClick}
              title="Native Kubernetes Warning events (e.g., ImagePullBackOff, FailedScheduling)"
              className="badge status-degraded w-fit gap-1.5 hover:opacity-80 transition-opacity"
            >
              <AlertTriangle className="w-3.5 h-3.5 shrink-0" />
              <span><span className="font-mono">{health.warningEvents}</span> Warning Events</span>
            </button>
          )}
          {problems.length > 0 && (
            <button
              onClick={onUnhealthyClick}
              title="View timeline of unhealthy/degraded workload events"
              className="badge status-unhealthy w-fit gap-1.5 hover:opacity-80 transition-opacity"
            >
              <AlertTriangle className="w-3.5 h-3.5 shrink-0" />
              <span>View unhealthy workload events</span>
            </button>
          )}
        </div>

        {/* Center column: Resources (aligned with health rings) */}
        <div className="w-1/2 grid grid-cols-3 items-center justify-items-center px-4">
          {secondaryResources.map((res) => (
            <button
              key={res.kind}
              onClick={() => onNavigateToKind(res.kind, res.group)}
              className="flex items-center gap-1.5 px-2 py-1 rounded hover:bg-theme-hover transition-colors text-sm whitespace-nowrap"
            >
              {isRestricted(res.kind) ? (
                <>
                  <Shield className="w-3.5 h-3.5 text-amber-400/60" />
                  <span className="text-theme-text-disabled">{res.label}</span>
                </>
              ) : (
                <>
                  <res.icon className={clsx('w-3.5 h-3.5', res.hasIssues ? 'text-yellow-500' : 'text-theme-text-tertiary')} />
                  <span className="text-theme-text-primary font-medium font-mono">{res.total}</span>
                  <span className="text-theme-text-secondary">{res.label}</span>
                </>
              )}
            </button>
          ))}
        </div>

        {/* Right column: Browse All (aligned with resource utilization) */}
        <div className="flex items-center justify-center w-1/4 shrink-0 pl-4 border-l border-theme-border/50">
          <button
            onClick={onNavigateToView}
            className="flex items-center gap-2 text-base font-medium text-theme-text-secondary hover:text-theme-text-primary transition-colors"
          >
            Browse All Resources
            <ArrowRight className="w-5 h-5" />
          </button>
        </div>
      </div>
    </div>
  )
}

function RestrictedRing({ label }: { label: string }) {
  const radius = 36
  const circumference = 2 * Math.PI * radius
  const arcLength = 0.75 * circumference
  const gapLength = circumference - arcLength
  return (
    <div className="flex flex-col items-center gap-2">
      <div className="relative w-[88px] h-[88px] flex items-center justify-center">
        <svg width={88} height={88} viewBox="0 0 88 88" className="absolute inset-0">
          <circle
            cx={44}
            cy={44}
            r={radius}
            fill="none"
            stroke="currentColor"
            strokeWidth={8}
            strokeDasharray={`6 4 ${arcLength - 10} ${gapLength + 10}`}
            strokeLinecap="round"
            transform="rotate(135 44 44)"
            className="text-theme-border"
          />
        </svg>
        <Shield className="w-6 h-6 text-amber-400" />
      </div>
      <span className="text-xs font-semibold uppercase tracking-wider text-theme-text-secondary">{label}</span>
      <span className="text-[11px] text-amber-400">Restricted</span>
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
        <span className="text-[10px] text-theme-text-tertiary font-mono">{label}: {used} / {total}</span>
        <span className="text-[10px] font-medium text-theme-text-secondary font-mono">{percent}%</span>
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
