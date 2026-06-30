import { useMemo, type ReactNode } from 'react'
import { useDashboard, useDashboardCRDs, useDashboardHelm, useIssues, type IssuesResponse } from '../../api/client'
import { useConnection } from '../../context/ConnectionContext'
import type { ExtendedMainView, Topology, SelectedResource } from '../../types'
import { TopologyPreview } from './TopologyPreview'
import { HelmSummary } from './HelmSummary'
import { ActivitySummary } from './ActivitySummary'
import { TrafficSummary } from './TrafficSummary'
import { CertificateHealthCard } from './CertificateHealthCard'
import { NetworkPolicyCoverageCard } from './NetworkPolicyCoverageCard'
import { CostCard } from './CostCard'
import { GitOpsControllersCard } from './GitOpsControllersCard'
import {
  AuditCard,
  FreshnessControl,
  PaneLoader,
  StatusDot,
  categoryLabel,
  groupLabel,
  subjectRef,
  type Issue,
} from '@skyhook-io/k8s-ui'
import { formatCompactAge } from '@skyhook-io/k8s-ui/utils/format'
import { ClusterHealthCard } from './ClusterHealthCard'
import { AlertTriangle, CheckCircle, Loader2, Shield } from 'lucide-react'
import { clsx } from 'clsx'

interface HomeViewProps {
  namespaces: string[]
  topology: Topology | null
  onNavigateToView: (view: ExtendedMainView, params?: Record<string, string>) => void
  onNavigateToResourceKind: (kind: string, group?: string, filters?: Record<string, string[]>) => void
  onNavigateToResource: (resource: SelectedResource) => void
  /**
   * Optional override for the Certificate Health card's click. When an embedded
   * host (Radar Cloud) takes Certs over with its own fleet page, it passes this
   * to route there instead of Radar's TLS-secrets resource list. Omitted in
   * standalone OSS → the card drills into secrets as before.
   */
  onNavigateToCerts?: () => void
}

export function HomeView({ namespaces, topology, onNavigateToView, onNavigateToResourceKind, onNavigateToResource, onNavigateToCerts }: HomeViewProps) {
  const { data, isLoading, error, dataUpdatedAt, refetch } = useDashboard(namespaces)
  const { connection } = useConnection()
  const { data: issuesData, isLoading: issuesLoading, isFetching: issuesFetching, error: issuesError } = useIssues(namespaces)
  const issues = issuesData?.issues ?? []
  const issueCount = issuesData?.total_matched ?? issuesData?.total ?? issues.length
  const hasCriticalIssues = issues.some((issue) => issue.severity === 'critical')

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
          freshness={
            <FreshnessControl
              mode="auto"
              dataUpdatedAt={dataUpdatedAt}
              onRefresh={() => refetch()}
              connectionState={connection.state}
            />
          }
          health={data.health}
          counts={data.resourceCounts}
          cluster={data.cluster}
          metrics={data.metrics}
          metricsServerAvailable={data.metricsServerAvailable}
          topCRDs={crdsData?.topCRDs}
          issueCount={issueCount}
          hasCriticalIssues={hasCriticalIssues}
          nodeVersionSkew={data.nodeVersionSkew}
          onNavigateToKind={onNavigateToResourceKind}
          onNavigateToView={() => onNavigateToView('resources')}
          onWarningEventsClick={() => onNavigateToView('timeline', { view: 'list', filter: 'warnings', time: 'all' })}
          onIssuesClick={() => onNavigateToView('issues')}
        />

        {/* Row 2: Main content columns - teasers left, issues right */}
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-[1fr_420px]">
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
                      onNavigate={onNavigateToCerts ?? (() => onNavigateToResourceKind('secrets', undefined, { type: ['TLS'] }))}
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
                      onNavigate={() => onNavigateToView('checks')}
                    />
                  </BandItem>
                )}
              </div>
            )}
          </div>

          <ProblemsPanel
            issues={issues}
            issueCount={issueCount}
            visibility={issuesData?.visibility}
            hasData={!!issuesData}
            isLoading={issuesLoading && !issuesData}
            isRefreshing={issuesFetching && !!issuesData}
            error={issuesError}
            totalReturned={issues.length}
            onNavigateToIssues={() => onNavigateToView('issues')}
            onResourceClick={onNavigateToResource}
          />
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
  issues: Issue[]
  issueCount: number
  visibility?: IssuesResponse['visibility']
  hasData: boolean
  isLoading: boolean
  isRefreshing: boolean
  error: unknown
  totalReturned: number
  onNavigateToIssues: () => void
  onResourceClick: (resource: SelectedResource) => void
}


function ProblemsPanel({
  issues,
  issueCount,
  visibility,
  hasData,
  isLoading,
  isRefreshing,
  error,
  totalReturned,
  onNavigateToIssues,
  onResourceClick,
}: ProblemsPanelProps) {
  const hasCriticalIssues = issues.some((issue) => issue.severity === 'critical')
  const hasIssues = issueCount > 0
  const hasHardError = !!error && !hasData
  const hasLimitedVisibility = !!visibility?.impact
  const isTruncated = issueCount > totalReturned
  const titleClass = hasCriticalIssues
    ? 'text-red-500'
    : hasIssues || hasHardError || hasLimitedVisibility
      ? 'text-amber-500'
      : 'text-theme-text-secondary'
  const countClass = hasHardError
    ? 'status-unknown'
    : hasCriticalIssues
      ? 'status-unhealthy'
      : hasIssues || hasLimitedVisibility
        ? 'status-degraded'
        : 'status-healthy'
  const countLabel = isLoading ? '...' : hasHardError ? 'error' : String(issueCount)

  return (
    <div className="rounded-xl bg-theme-surface shadow-theme-sm flex flex-col lg:max-h-[calc(100vh-280px)] lg:sticky lg:top-0">
      <div className="flex items-center justify-between px-5 py-3 border-b border-theme-border/50 shrink-0">
        <div className="flex items-center gap-2">
          <AlertTriangle className={clsx('w-4 h-4', titleClass)} />
          <span className={clsx('text-xs font-semibold uppercase tracking-wider', titleClass)}>Active Issues</span>
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            className="rounded-md px-2 py-1 text-xs font-medium text-accent-text transition-colors hover:bg-accent-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-radar-accent)]/40"
            onClick={onNavigateToIssues}
          >
            View all
          </button>
          {isRefreshing && !isLoading && (
            <Loader2 className="h-3.5 w-3.5 animate-spin text-theme-text-tertiary" aria-label="Refreshing issues" />
          )}
          <span className={clsx('badge rounded-full', countClass)}>{countLabel}</span>
        </div>
      </div>
      <div className="overflow-y-auto flex-1 min-h-0">
        {isLoading ? (
          <ProblemsPanelState
            icon={<Loader2 className="h-5 w-5 animate-spin text-theme-text-tertiary" />}
            title="Loading issues"
            body="Checking live cluster issues for the selected scope."
          />
        ) : hasHardError ? (
          <ProblemsPanelState
            icon={<AlertTriangle className="h-5 w-5 text-amber-500" />}
            title="Issues unavailable"
            body={formatIssueError(error)}
          />
        ) : (
          <>
            {!!error && (
              <ProblemsPanelNotice tone="warning">
                Issue refresh failed. Showing the last successful result.
              </ProblemsPanelNotice>
            )}
            {visibility?.impact && (
              <ProblemsPanelNotice tone="warning">
                Limited visibility - {visibility.impact} Results may be incomplete.
              </ProblemsPanelNotice>
            )}
            {isTruncated && (
              <ProblemsPanelNotice tone="neutral">
                Showing {totalReturned} of {issueCount} issues. Narrow by namespace to see the rest.
              </ProblemsPanelNotice>
            )}

            {issues.length === 0 ? (
              <ProblemsPanelState
                icon={
                  hasLimitedVisibility
                    ? <AlertTriangle className="h-5 w-5 text-amber-500" />
                    : <CheckCircle className="h-5 w-5 text-green-500" />
                }
                title={hasLimitedVisibility ? 'No visible active issues' : 'No active issues'}
                body={
                  hasLimitedVisibility
                    ? 'Radar could not read every workload source in this scope.'
                    : 'No critical or warning issues across the selected scope.'
                }
              />
            ) : (
              <div className="divide-y divide-theme-border">
                {issues.map((issue) => {
                  const ref = subjectRef(issue)
                  const age = issue.first_seen ? formatCompactAge(issue.first_seen) : ''
                  const startedAtDeploy = issue.issue_timing === 'started_at_resource_creation'

                  return (
                    <button
                      key={issue.id}
                      className="w-full flex items-center gap-2 px-3 py-1.5 hover:bg-theme-hover transition-colors text-left"
                      onClick={() => onResourceClick({
                        kind: ref.kind,
                        namespace: ref.namespace ?? '',
                        name: ref.name,
                        group: ref.group ?? '',
                      })}
                    >
                      <StatusDot tone={issue.severity === 'critical' ? 'unhealthy' : 'degraded'} className="shrink-0" />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-1.5">
                          <span className="text-[10px] text-theme-text-tertiary bg-theme-elevated px-1 py-0.5 rounded">{issue.kind}</span>
                          <span className="text-xs text-theme-text-primary truncate font-medium">{issue.name}</span>
                          {(age || startedAtDeploy) && (
                            <span className="ml-auto flex shrink-0 items-center gap-1">
                              {age && <span className="text-[10px] text-theme-text-tertiary tabular-nums">{age}</span>}
                              {startedAtDeploy && <span className="badge-sm text-[10px] text-theme-text-secondary">since deploy</span>}
                            </span>
                          )}
                        </div>
                        <div className="flex items-center gap-1.5 mt-0.5">
                          <span className="text-[11px] text-theme-text-secondary truncate">{categoryLabel(issue.category)}</span>
                          <span className="text-[10px] text-theme-text-tertiary shrink-0">{groupLabel(issue.category_group)}</span>
                          {issue.namespace && <span className="text-[10px] text-theme-text-tertiary shrink-0">{issue.namespace}</span>}
                        </div>
                        {(issue.reason || issue.message) && (
                          <div className="text-[10px] text-theme-text-tertiary truncate mt-0.5">
                            {issue.reason}
                            {issue.reason && issue.message ? ' - ' : ''}
                            {issue.message}
                          </div>
                        )}
                      </div>
                    </button>
                  )
                })}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}

function ProblemsPanelState({ icon, title, body }: { icon: ReactNode; title: string; body: string }) {
  return (
    <div className="flex min-h-[220px] flex-col items-center justify-center gap-2 px-6 py-10 text-center">
      <div className="flex h-9 w-9 items-center justify-center rounded-full bg-theme-elevated">
        {icon}
      </div>
      <p className="text-sm font-medium text-theme-text-primary">{title}</p>
      <p className="max-w-[280px] text-xs leading-5 text-theme-text-secondary">{body}</p>
    </div>
  )
}

function ProblemsPanelNotice({ tone, children }: { tone: 'warning' | 'neutral'; children: ReactNode }) {
  return (
    <div className="px-3 pt-3">
      <div
        className={clsx(
          'rounded-lg border px-3 py-2 text-xs leading-5',
          tone === 'warning'
            ? 'border-amber-500/20 bg-amber-500/10 text-theme-text-secondary'
            : 'border-theme-border bg-theme-elevated text-theme-text-secondary',
        )}
      >
        {children}
      </div>
    </div>
  )
}

function formatIssueError(error: unknown): string {
  return error instanceof Error && error.message
    ? error.message
    : 'Failed to load active issues.'
}
