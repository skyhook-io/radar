import { useDashboard } from '../../api/client'
import type { DashboardResponse, DashboardMetrics, DashboardCRDCount } from '../../api/client'
import type { ExtendedMainView, Topology, SelectedResource } from '../../types'
import { TopologyPreview } from './TopologyPreview'
import { HelmSummary } from './HelmSummary'
import { ActivitySummary } from './ActivitySummary'
import { TrafficSummary } from './TrafficSummary'
import { HealthRing } from './HealthRing'
import {
  AlertTriangle, XCircle, CheckCircle, Loader2,
  Box, Layers, Globe, Server, Network as NetworkIcon, Briefcase,
  Database, Container, Cpu, MemoryStick, Puzzle, ArrowRight,
} from 'lucide-react'
import { clsx } from 'clsx'

interface HomeViewProps {
  namespace: string
  topology: Topology | null
  onNavigateToView: (view: ExtendedMainView) => void
  onNavigateToResourceKind: (kind: string, group?: string) => void
  onNavigateToResource: (resource: SelectedResource) => void
}

export function HomeView({ namespace, topology, onNavigateToView, onNavigateToResourceKind, onNavigateToResource }: HomeViewProps) {
  const { data, isLoading, error } = useDashboard(namespace || undefined)

  if (isLoading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Loader2 className="w-6 h-6 animate-spin text-theme-text-tertiary" />
      </div>
    )
  }

  if (error || !data) {
    return (
      <div className="flex-1 flex items-center justify-center text-theme-text-secondary">
        <p>Failed to load dashboard data</p>
      </div>
    )
  }

  const hasProblems = data.problems && data.problems.length > 0

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-[1600px] mx-auto px-6 py-6 space-y-5">
        {/* Row 1: Health Banner + Resource Counts */}
        <HealthBanner health={data.health} cluster={data.cluster} metrics={data.metrics} />
        <ResourceCountsGrid
          counts={data.resourceCounts}
          topCRDs={data.topCRDs}
          onNavigateToKind={onNavigateToResourceKind}
          onNavigateToView={() => onNavigateToView('resources')}
        />

        {/* Row 2: Main content columns — teasers left, problems right (if any) */}
        <div className={clsx(
          'grid gap-5',
          hasProblems ? 'grid-cols-1 lg:grid-cols-[1fr_420px]' : 'grid-cols-1'
        )}>
          {/* Left column: teaser cards in 2-col grid */}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-5 auto-rows-min">
            <TopologyPreview
              topology={topology}
              summary={data.topologySummary}
              onNavigate={() => onNavigateToView('topology')}
            />
            <HelmSummary
              data={data.helmReleases}
              onNavigate={() => onNavigateToView('helm')}
            />
            <ActivitySummary
              namespace={namespace || undefined}
              topology={topology}
              onNavigate={() => onNavigateToView('timeline')}
            />
            <TrafficSummary
              data={data.trafficSummary}
              onNavigate={() => onNavigateToView('traffic')}
            />
          </div>

          {/* Right column: problems panel */}
          {hasProblems && (
            <ProblemsPanel
              problems={data.problems}
              onResourceClick={onNavigateToResource}
            />
          )}
        </div>
      </div>
    </div>
  )
}

// ============================================================================
// Health Banner
// ============================================================================

function HealthBanner({ health, cluster, metrics }: {
  health: DashboardResponse['health']
  cluster: DashboardResponse['cluster']
  metrics: DashboardMetrics | null
}) {
  const total = health.healthy + health.warning + health.error
  const allHealthy = health.warning === 0 && health.error === 0

  const ringSegments = [
    { value: health.healthy, color: '#22c55e' }, // green-500
    { value: health.warning, color: '#eab308' }, // yellow-500
    { value: health.error, color: '#ef4444' },   // red-500
  ]

  return (
    <div className={clsx(
      'rounded-lg border px-5 py-4',
      allHealthy
        ? 'border-green-500/20 bg-green-500/5'
        : health.error > 0
          ? 'border-red-500/20 bg-red-500/5'
          : 'border-yellow-500/20 bg-yellow-500/5'
    )}>
      <div className="flex items-center justify-between gap-4">
        {/* Left: Ring + Pod health counts */}
        <div className="flex items-center gap-4">
          <HealthRing segments={ringSegments} size={44} strokeWidth={5} label={String(total)} />
          <div className="flex items-center gap-5">
            <div className="flex items-center gap-1.5">
              <CheckCircle className="w-3.5 h-3.5 text-green-500" />
              <span className="text-sm text-theme-text-primary font-medium">{health.healthy}</span>
              <span className="text-sm text-theme-text-secondary">healthy</span>
            </div>
            {health.warning > 0 && (
              <div className="flex items-center gap-1.5">
                <AlertTriangle className="w-3.5 h-3.5 text-yellow-500" />
                <span className="text-sm text-theme-text-primary font-medium">{health.warning}</span>
                <span className="text-sm text-theme-text-secondary">warning</span>
              </div>
            )}
            {health.error > 0 && (
              <div className="flex items-center gap-1.5">
                <XCircle className="w-3.5 h-3.5 text-red-500" />
                <span className="text-sm text-theme-text-primary font-medium">{health.error}</span>
                <span className="text-sm text-theme-text-secondary">error</span>
              </div>
            )}
          </div>
        </div>

        {/* Right: Warning events + Metrics */}
        <div className="flex items-center gap-5">
          {health.warningEvents > 0 && (
            <div className="flex items-center gap-1.5">
              <AlertTriangle className="w-3.5 h-3.5 text-yellow-500" />
              <span className="text-sm text-theme-text-primary font-medium">{health.warningEvents}</span>
              <span className="text-sm text-theme-text-secondary hidden sm:inline">warning events</span>
            </div>
          )}
          {metrics?.cpu && (
            <MetricBar
              icon={<Cpu className="w-3.5 h-3.5 text-theme-text-tertiary" />}
              label="CPU"
              percent={metrics.cpu.usagePercent}
            />
          )}
          {metrics?.memory && (
            <MetricBar
              icon={<MemoryStick className="w-3.5 h-3.5 text-theme-text-tertiary" />}
              label="Mem"
              percent={metrics.memory.usagePercent}
            />
          )}
          <span className="text-xs text-theme-text-tertiary hidden lg:inline">
            {total} pods{cluster.name && ` on ${cluster.name}`}
          </span>
        </div>
      </div>
    </div>
  )
}

function MetricBar({ icon, label, percent }: { icon: React.ReactNode; label: string; percent: number }) {
  const barColor = percent > 85 ? 'bg-red-500' : percent > 65 ? 'bg-yellow-500' : 'bg-blue-500'
  return (
    <div className="flex items-center gap-1.5">
      {icon}
      <span className="text-xs text-theme-text-secondary">{label}</span>
      <div className="w-16 h-1.5 bg-theme-border rounded-full overflow-hidden">
        <div className={clsx('h-full rounded-full', barColor)} style={{ width: `${Math.min(percent, 100)}%` }} />
      </div>
      <span className="text-xs text-theme-text-primary font-medium">{percent}%</span>
    </div>
  )
}

// ============================================================================
// Problems Panel (right sidebar, scrollable)
// ============================================================================

interface ProblemsPanelProps {
  problems: DashboardResponse['problems']
  onResourceClick: (resource: SelectedResource) => void
}

// Convert problem kind to the plural API resource name for navigation
const KIND_TO_RESOURCE: Record<string, string> = {
  'Pod': 'pods',
  'Deployment': 'deployments',
  'StatefulSet': 'statefulsets',
  'DaemonSet': 'daemonsets',
  'Node': 'nodes',
  'Job': 'jobs',
  'CronJob': 'cronjobs',
  'Service': 'services',
  'Ingress': 'ingresses',
  'ReplicaSet': 'replicasets',
}

function problemKindToResource(kind: string): string {
  return KIND_TO_RESOURCE[kind] || kind.toLowerCase() + 's'
}

function ProblemsPanel({ problems, onResourceClick }: ProblemsPanelProps) {
  return (
    <div className="rounded-lg border border-theme-border bg-theme-surface/50 flex flex-col lg:max-h-[calc(100vh-280px)] lg:sticky lg:top-0">
      <div className="px-3 py-2 border-b border-theme-border shrink-0">
        <h3 className="text-xs font-medium text-theme-text-primary flex items-center gap-1.5">
          <AlertTriangle className="w-3.5 h-3.5 text-yellow-500" />
          Problems
          <span className="text-[10px] bg-red-500/10 text-red-500 px-1.5 py-0.5 rounded-full">{problems.length}</span>
        </h3>
      </div>
      <div className="overflow-y-auto flex-1 min-h-0">
        <div className="divide-y divide-theme-border">
          {problems.map((p, i) => (
            <button
              key={`${p.kind}-${p.namespace}-${p.name}-${i}`}
              className="w-full flex items-center gap-2 px-3 py-1.5 hover:bg-theme-hover transition-colors text-left"
              onClick={() => onResourceClick({
                kind: problemKindToResource(p.kind),
                namespace: p.namespace,
                name: p.name,
              })}
            >
              <span className={clsx(
                'w-1.5 h-1.5 rounded-full shrink-0',
                p.status === 'error' ? 'bg-red-500' : 'bg-yellow-500'
              )} />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-1.5">
                  <span className="text-[10px] text-theme-text-tertiary bg-theme-elevated px-1 py-0.5 rounded">{p.kind}</span>
                  <span className="text-xs text-theme-text-primary truncate font-medium">{p.name}</span>
                  <span className="text-[10px] text-theme-text-tertiary ml-auto shrink-0">{p.age}</span>
                </div>
                <div className="flex items-center gap-1.5 mt-0.5">
                  <span className="text-[11px] text-theme-text-secondary truncate">{p.reason}</span>
                  <span className="text-[10px] text-theme-text-tertiary shrink-0">{p.namespace}</span>
                </div>
              </div>
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}

// ============================================================================
// Resource Counts Grid
// ============================================================================

interface ResourceCountsGridProps {
  counts: DashboardResponse['resourceCounts']
  topCRDs: DashboardCRDCount[]
  onNavigateToKind: (kind: string, group?: string) => void
  onNavigateToView: () => void
}

function ResourceCountsGrid({ counts, topCRDs, onNavigateToKind, onNavigateToView }: ResourceCountsGridProps) {
  const cards = [
    {
      label: 'Pods',
      icon: Box,
      total: counts.pods.total,
      detail: counts.pods.failed > 0
        ? `${counts.pods.failed} failed`
        : counts.pods.pending > 0
          ? `${counts.pods.pending} pending`
          : `${counts.pods.running} running`,
      hasIssues: counts.pods.failed > 0 || counts.pods.pending > 0,
      kind: 'pods',
    },
    {
      label: 'Deployments',
      icon: Layers,
      total: counts.deployments.total,
      detail: `${counts.deployments.available} available`,
      hasIssues: counts.deployments.unavailable > 0,
      kind: 'deployments',
    },
    {
      label: 'StatefulSets',
      icon: Database,
      total: counts.statefulSets.total,
      detail: `${counts.statefulSets.ready} ready`,
      hasIssues: counts.statefulSets.unready > 0,
      kind: 'statefulsets',
    },
    {
      label: 'DaemonSets',
      icon: Container,
      total: counts.daemonSets.total,
      detail: `${counts.daemonSets.ready} ready`,
      hasIssues: counts.daemonSets.unready > 0,
      kind: 'daemonsets',
    },
    {
      label: 'Services',
      icon: Globe,
      total: counts.services,
      kind: 'services',
    },
    {
      label: 'Nodes',
      icon: Server,
      total: counts.nodes.total,
      detail: `${counts.nodes.ready} ready`,
      hasIssues: counts.nodes.notReady > 0,
      kind: 'nodes',
    },
    {
      label: 'Ingresses',
      icon: NetworkIcon,
      total: counts.ingresses,
      kind: 'ingresses',
    },
    {
      label: 'Jobs',
      icon: Briefcase,
      total: counts.jobs.total,
      detail: `${counts.jobs.active} active`,
      hasIssues: counts.jobs.failed > 0,
      kind: 'jobs',
    },
  ]

  const hasCRDs = topCRDs && topCRDs.length > 0

  return (
    <div className="rounded-lg border border-theme-border bg-theme-surface/50 overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-2.5 border-b border-theme-border">
        <span className="text-sm font-medium text-theme-text-primary">Resources</span>
        <button
          onClick={onNavigateToView}
          className="flex items-center gap-1 text-xs font-medium text-blue-500 hover:text-blue-400 transition-colors"
        >
          Browse All Resources
          <ArrowRight className="w-3.5 h-3.5" />
        </button>
      </div>

      {/* Built-in resource cards */}
      <div className="grid grid-cols-2 sm:grid-cols-4 lg:grid-cols-8 gap-3 p-4">
        {cards.map((card) => (
          <button
            key={card.kind}
            onClick={() => onNavigateToKind(card.kind)}
            className="flex flex-col items-center gap-1.5 px-3 py-3.5 rounded-lg border border-theme-border bg-theme-surface hover:bg-theme-hover hover:border-theme-border-light transition-colors"
          >
            <card.icon className={clsx(
              'w-5 h-5',
              card.hasIssues ? 'text-yellow-500' : 'text-theme-text-tertiary'
            )} />
            <span className="text-2xl font-semibold text-theme-text-primary">{card.total}</span>
            <span className="text-xs text-theme-text-secondary">{card.label}</span>
            {card.detail && (
              <span className={clsx(
                'text-xs',
                card.hasIssues ? 'text-yellow-500' : 'text-theme-text-tertiary'
              )}>
                {card.detail}
              </span>
            )}
          </button>
        ))}
      </div>

      {/* CRD resource cards */}
      {hasCRDs && (
        <div className="grid grid-cols-2 sm:grid-cols-4 lg:grid-cols-8 gap-3 px-4 pb-4">
          {topCRDs.map((crd) => (
            <button
              key={`${crd.group}/${crd.kind}`}
              onClick={() => onNavigateToKind(crd.name, crd.group)}
              className="flex flex-col items-center gap-1 px-3 py-2.5 rounded-lg border border-dashed border-theme-border bg-theme-surface/30 hover:bg-theme-hover hover:border-theme-border-light transition-colors"
            >
              <Puzzle className="w-4 h-4 text-theme-text-tertiary" />
              <span className="text-xl font-semibold text-theme-text-primary">{crd.count}</span>
              <span className="text-xs text-theme-text-secondary truncate w-full text-center">{crd.kind}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
