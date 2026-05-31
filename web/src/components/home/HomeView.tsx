import { useMemo, type ReactNode } from 'react'
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
            {/* Live band — Topology + Timeline always render, so a fixed 2-up never strands.
                These are the richest visuals and the most-used live views, so they get the width. */}
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-6">
              <TopologyPreview
                topology={scopedTopology}
                summary={data.topologySummary}
                onNavigate={() => onNavigateToView('topology')}
              />
              <ActivitySummary
                namespaces={namespaces}
                topology={scopedTopology}
                onNavigate={() => onNavigateToView('timeline')}
              />
            </div>

            {/* Explore band — flex-grow wrap so the row always fills. The conditional
                Cost card self-hides via BandItem's empty:hidden when OpenCost is absent,
                leaving Traffic + Helm to stretch rather than stranding an empty cell. */}
            <div className="flex flex-wrap gap-6">
              <BandItem>
                <TrafficSummary
                  data={data.trafficSummary}
                  onNavigate={() => onNavigateToView('traffic')}
                />
              </BandItem>
              <BandItem>
                <HelmSummary
                  data={helmData}
                  onNavigate={() => onNavigateToView('helm')}
                />
              </BandItem>
              <BandItem>
                <CostCard onNavigate={() => onNavigateToView('cost')} />
              </BandItem>
            </div>

            {/* Posture band — same flex-grow wrap so any subset of compliance cards
                fills its row instead of stranding the last one (the old 3-col grid
                left Cluster Audit alone with two empty cells beside it). */}
            {(data.certificateHealth || data.networkPolicyCoverage || data.audit || data.gitopsControllers) && (
              <div className="flex flex-wrap gap-6">
                {data.certificateHealth && (
                  <BandItem>
                    <CertificateHealthCard
                      data={data.certificateHealth}
                      onNavigate={() => onNavigateToResourceKind('secrets', undefined, { type: ['TLS'] })}
                    />
                  </BandItem>
                )}
                {data.networkPolicyCoverage && (
                  <BandItem>
                    <NetworkPolicyCoverageCard
                      data={data.networkPolicyCoverage}
                      onNavigate={() => onNavigateToResourceKind('networkpolicies', 'networking.k8s.io')}
                    />
                  </BandItem>
                )}
                {data.gitopsControllers && (
                  <BandItem>
                    <GitOpsControllersCard
                      data={data.gitopsControllers}
                      onNavigate={() => onNavigateToView('gitops')}
                    />
                  </BandItem>
                )}
                {data.audit && (
                  <BandItem>
                    <AuditCard
                      data={data.audit}
                      onNavigate={() => onNavigateToView('audit')}
                    />
                  </BandItem>
                )}
              </div>
            )}
          </div>

          {/* Right column: problems panel */}
          {hasProblems && (
            <ProblemsPanel
              problems={data.problems}
              onNavigateToIssues={() => onNavigateToView('issues')}
              onResourceClick={onNavigateToResource}
            />
          )}
        </div>
      </div>
    </div>
  )
}

// A self-tiling flex item: grows to share the row, clamps to a sensible min
// width, and removes itself (empty:hidden) when its card renders null — so a
// data-gated card (e.g. Cost without OpenCost) can't leave a phantom column.
function BandItem({ children }: { children: ReactNode }) {
  return <div className="flex-1 min-w-[260px] empty:hidden [&>*]:w-full">{children}</div>
}

// ============================================================================
// Problems Panel (right sidebar, scrollable)
// ============================================================================

interface ProblemsPanelProps {
  problems: DashboardResponse['problems']
  onNavigateToIssues: () => void
  onResourceClick: (resource: SelectedResource) => void
}


function ProblemsPanel({ problems, onNavigateToIssues, onResourceClick }: ProblemsPanelProps) {
  return (
    <div className="rounded-xl bg-theme-surface shadow-theme-sm flex flex-col lg:max-h-[calc(100vh-280px)] lg:sticky lg:top-0">
      <div className="flex items-center justify-between px-5 py-3 border-b border-theme-border/50 shrink-0">
        <div className="flex items-center gap-2">
          <AlertTriangle className="w-4 h-4 text-red-500" />
          <span className="text-xs font-semibold uppercase tracking-wider text-red-500">Active Issues</span>
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            className="rounded-md px-2 py-1 text-xs font-medium text-accent-text transition-colors hover:bg-accent-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-radar-accent)]/40"
            onClick={onNavigateToIssues}
          >
            View all
          </button>
          <span className="badge status-unhealthy rounded-full">{problems.length}</span>
        </div>
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
