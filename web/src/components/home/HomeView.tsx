import { useDashboard, useDashboardCRDs } from '../../api/client'
import type { DashboardResponse } from '../../api/client'
import type { ExtendedMainView, Topology, SelectedResource } from '../../types'
import { kindToPlural } from '../../utils/navigation'
import { TopologyPreview } from './TopologyPreview'
import { HelmSummary } from './HelmSummary'
import { ActivitySummary } from './ActivitySummary'
import { TrafficSummary } from './TrafficSummary'
import { ClusterHealthCard } from './ClusterHealthCard'
import { AlertTriangle, Loader2 } from 'lucide-react'
import { clsx } from 'clsx'

interface HomeViewProps {
  namespaces: string[]
  topology: Topology | null
  onNavigateToView: (view: ExtendedMainView, params?: Record<string, string>) => void
  onNavigateToResourceKind: (kind: string, group?: string) => void
  onNavigateToResource: (resource: SelectedResource) => void
}

export function HomeView({ namespaces, topology, onNavigateToView, onNavigateToResourceKind, onNavigateToResource }: HomeViewProps) {
  const { data, isLoading, error } = useDashboard(namespaces)
  // CRDs load lazily after main dashboard to keep initial load fast
  const { data: crdsData } = useDashboardCRDs(namespaces)

  if (isLoading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <div className="flex flex-col items-center gap-3">
          <Loader2 className="w-6 h-6 animate-spin text-theme-text-tertiary" />
          <span className="text-sm text-theme-text-tertiary">Loading dashboard...</span>
        </div>
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
        {/* Row 1: Cluster Health Card (combined health + resource counts) */}
        <ClusterHealthCard
          health={data.health}
          counts={data.resourceCounts}
          cluster={data.cluster}
          metrics={data.metrics}
          topCRDs={crdsData?.topCRDs}
          problems={data.problems ?? []}
          onNavigateToKind={onNavigateToResourceKind}
          onNavigateToView={() => onNavigateToView('resources')}
          onWarningEventsClick={() => onNavigateToView('timeline', { view: 'list', filter: 'warnings', time: 'all' })}
          onUnhealthyClick={() => onNavigateToView('timeline', { view: 'list', filter: 'unhealthy', time: 'all' })}
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
              namespaces={namespaces}
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
// Problems Panel (right sidebar, scrollable)
// ============================================================================

interface ProblemsPanelProps {
  problems: DashboardResponse['problems']
  onResourceClick: (resource: SelectedResource) => void
}


function ProblemsPanel({ problems, onResourceClick }: ProblemsPanelProps) {
  return (
    <div className="rounded-lg border-[3px] border-red-500/30 bg-theme-surface/50 flex flex-col lg:max-h-[calc(100vh-280px)] lg:sticky lg:top-0">
      <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border shrink-0">
        <div className="flex items-center gap-2">
          <AlertTriangle className="w-4 h-4 text-red-500" />
          <span className="text-sm font-semibold text-red-500">Unhealthy Workloads</span>
        </div>
        <span className="text-[11px] bg-red-500/10 text-red-500 px-2 py-0.5 rounded-full font-medium">{problems.length}</span>
      </div>
      <div className="overflow-y-auto flex-1 min-h-0">
        <div className="divide-y divide-theme-border">
          {problems.map((p, i) => (
            <button
              key={`${p.kind}-${p.namespace}-${p.name}-${i}`}
              className="w-full flex items-center gap-2 px-3 py-1.5 hover:bg-theme-hover transition-colors text-left cursor-pointer"
              onClick={() => onResourceClick({
                kind: kindToPlural(p.kind),
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
