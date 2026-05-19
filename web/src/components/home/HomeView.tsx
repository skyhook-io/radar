import { useMemo } from 'react'
import { useDashboard, useDashboardCRDs, useDashboardHelm } from '../../api/client'
import type { DashboardResponse } from '../../api/client'
import type { ExtendedMainView, Topology, SelectedResource } from '../../types'
import { kindToPlural } from '../../utils/navigation'
import { TopologyPreview } from './TopologyPreview'
import { HelmSummary } from './HelmSummary'
import { ActivitySummary } from './ActivitySummary'
import { TrafficSummary } from './TrafficSummary'
import { CertificateHealthCard } from './CertificateHealthCard'
import { NetworkPolicyCoverageCard } from './NetworkPolicyCoverageCard'
import { CostCard } from './CostCard'
import { GitOpsControllersCard } from './GitOpsControllersCard'
import { AuditCard, PaneLoader, StatusDot, mapHealthToTone } from '@skyhook-io/k8s-ui'
import { ClusterHealthCard } from './ClusterHealthCard'
import { AlertTriangle, Loader2, Shield } from 'lucide-react'
import { clsx } from 'clsx'

interface HomeViewProps {
  namespaces: string[]
  topology: Topology | null
  onNavigateToView: (view: ExtendedMainView, params?: Record<string, string>) => void
  onNavigateToResourceKind: (kind: string, group?: string, filters?: Record<string, string[]>) => void
  onNavigateToResource: (resource: SelectedResource) => void
}

export function HomeView({ namespaces, topology, onNavigateToView, onNavigateToResourceKind, onNavigateToResource }: HomeViewProps) {
  const { data, isLoading, error } = useDashboard(namespaces)

  // SSE is cluster-wide on small/medium clusters; the picker only narrows the
  // dashboard summary, so re-apply the filter here or the legend disagrees.
  const scopedTopology = useMemo<Topology | null>(() => {
    if (!topology) return null
    if (namespaces.length === 0) return topology
    const nsSet = new Set(namespaces)
    const nodes = topology.nodes.filter(n => {
      const ns = n.data.namespace as string | undefined
      return !ns || nsSet.has(ns)
    })
    const nodeIds = new Set(nodes.map(n => n.id))
    const edges = topology.edges.filter(e => nodeIds.has(e.source) && nodeIds.has(e.target))
    return { nodes, edges }
  }, [topology, namespaces])
  // CRDs and Helm load lazily after main dashboard to keep initial load fast
  const { data: crdsData } = useDashboardCRDs(namespaces)
  const { data: helmData } = useDashboardHelm(namespaces)

  if (isLoading) {
    return <PaneLoader label="Loading dashboard…" className="flex-1" />
  }

  if (error || !data) {
    return (
      <div className="flex-1 flex items-center justify-center text-theme-text-secondary">
        <p>Failed to load dashboard data</p>
      </div>
    )
  }

  if (data.accessRestricted) {
    return (
      <div className="flex-1 flex items-center justify-center bg-theme-base">
        <div className="flex flex-col items-center gap-3 max-w-md text-center">
          <div className="w-12 h-12 rounded-full bg-amber-500/10 flex items-center justify-center">
            <Shield className="w-6 h-6 text-amber-500" />
          </div>
          <p className="text-lg font-medium text-theme-text-primary">No Namespace Access</p>
          <p className="text-sm text-theme-text-secondary">
            Your account does not have access to any namespaces in this cluster. Contact your administrator to add a Kubernetes RoleBinding or ClusterRoleBinding for your user.
          </p>
        </div>
      </div>
    )
  }

  const hasProblems = data.problems && data.problems.length > 0

  const stillLoading = data.deferredLoading || (data.partialData && data.partialData.length > 0)

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-[1600px] mx-auto px-6 py-6 space-y-6">
        {stillLoading && (
          <div className="flex items-center gap-2 text-xs text-theme-text-tertiary">
            <Loader2 className="w-3 h-3 animate-spin" />
            <span>
              {data.partialData && data.partialData.length > 0
                ? `Still loading: ${data.partialData.join(', ')}`
                : 'Loading remaining resources…'}
            </span>
          </div>
        )}
        {/* Row 1: Cluster Health Card (combined health + resource counts) */}
        <ClusterHealthCard
          health={data.health}
          counts={data.resourceCounts}
          cluster={data.cluster}
          metrics={data.metrics}
          metricsServerAvailable={data.metricsServerAvailable}
          topCRDs={crdsData?.topCRDs}
          problems={data.problems ?? []}
          nodeVersionSkew={data.nodeVersionSkew}
          onNavigateToKind={onNavigateToResourceKind}
          onNavigateToView={() => onNavigateToView('resources')}
          onWarningEventsClick={() => onNavigateToView('timeline', { view: 'list', filter: 'warnings', time: 'all' })}
          onUnhealthyClick={() => onNavigateToView('timeline', { view: 'list', filter: 'unhealthy', time: 'all' })}
        />

        {/* Row 2: Main content columns — teasers left, problems right (if any) */}
        <div className={clsx(
          'grid gap-6',
          hasProblems ? 'grid-cols-1 lg:grid-cols-[1fr_420px]' : 'grid-cols-1'
        )}>
          {/* Left column: teaser cards */}
          <div className="flex flex-col gap-6 auto-rows-min">
            {/* Primary cards — 2-col grid */}
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-6">
              <TopologyPreview
                topology={scopedTopology}
                summary={data.topologySummary}
                onNavigate={() => onNavigateToView('topology')}
              />
              <HelmSummary
                data={helmData}
                onNavigate={() => onNavigateToView('helm')}
              />
              <ActivitySummary
                namespaces={namespaces}
                topology={scopedTopology}
                onNavigate={() => onNavigateToView('timeline')}
              />
              <TrafficSummary
                data={data.trafficSummary}
                onNavigate={() => onNavigateToView('traffic')}
              />
              <CostCard onNavigate={() => onNavigateToView('cost')} />
            </div>

            {/* Health & compliance cards — 3-col when enough cards, 2-col fallback */}
            {(data.certificateHealth || data.networkPolicyCoverage || data.audit || data.gitopsControllers) && (() => {
              const healthCards = [
                data.certificateHealth && (
                  <CertificateHealthCard
                    key="certs"
                    data={data.certificateHealth}
                    onNavigate={() => onNavigateToResourceKind('secrets', undefined, { type: ['TLS'] })}
                  />
                ),
                data.networkPolicyCoverage && (
                  <NetworkPolicyCoverageCard
                    key="netpol"
                    data={data.networkPolicyCoverage}
                    onNavigate={() => onNavigateToResourceKind('networkpolicies', 'networking.k8s.io')}
                  />
                ),
                data.gitopsControllers && (
                  <GitOpsControllersCard
                    key="gitops-controllers"
                    data={data.gitopsControllers}
                    onNavigate={() => onNavigateToView('gitops')}
                  />
                ),
                data.audit && (
                  <AuditCard
                    key="audit"
                    data={data.audit}
                    onNavigate={() => onNavigateToView('audit')}
                  />
                ),
              ].filter(Boolean)

              return (
                <div className={clsx(
                  'grid gap-6',
                  healthCards.length >= 3 ? 'grid-cols-1 sm:grid-cols-2 lg:grid-cols-3' : 'grid-cols-1 sm:grid-cols-2'
                )}>
                  {healthCards}
                </div>
              )
            })()}
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
    <div className="rounded-xl bg-theme-surface shadow-theme-sm flex flex-col lg:max-h-[calc(100vh-280px)] lg:sticky lg:top-0">
      <div className="flex items-center justify-between px-5 py-3 border-b border-theme-border/50 shrink-0">
        <div className="flex items-center gap-2">
          <AlertTriangle className="w-4 h-4 text-red-500" />
          <span className="text-xs font-semibold uppercase tracking-wider text-red-500">Unhealthy Workloads</span>
        </div>
        <span className="badge status-unhealthy rounded-full">{problems.length}</span>
      </div>
      <div className="overflow-y-auto flex-1 min-h-0">
        <div className="divide-y divide-theme-border">
          {problems.map((p, i) => (
            <button
              key={`${p.kind}-${p.namespace}-${p.name}-${i}`}
              className="w-full flex items-center gap-2 px-3 py-1.5 hover:bg-theme-hover transition-colors text-left"
              onClick={() => onResourceClick({
                kind: kindToPlural(p.kind),
                namespace: p.namespace,
                name: p.name,
                group: p.group,
              })}
            >
              <StatusDot tone={mapHealthToTone(p.severity)} className="shrink-0" />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-1.5">
                  <span className="text-[10px] text-theme-text-tertiary bg-theme-elevated px-1 py-0.5 rounded">{p.kind}</span>
                  <span className="text-xs text-theme-text-primary truncate font-medium">{p.name}</span>
                  <span className="text-[10px] text-theme-text-tertiary ml-auto shrink-0">{p.duration || p.age}</span>
                </div>
                <div className="flex items-center gap-1.5 mt-0.5">
                  <span className="text-[11px] text-theme-text-secondary truncate">{p.reason}</span>
                  <span className="text-[10px] text-theme-text-tertiary shrink-0">{p.namespace}</span>
                </div>
                {p.message && (
                  <div className="text-[10px] text-theme-text-tertiary truncate mt-0.5">{p.message}</div>
                )}
              </div>
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
