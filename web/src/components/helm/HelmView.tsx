import { useState, useMemo, useRef, useEffect, useCallback, forwardRef } from 'react'
import { useRegisterShortcuts } from '../../hooks/useKeyboardShortcuts'
import { Package, Search, ArrowUpCircle, LayoutGrid, List, Shield, GitBranch, ChevronRight, RotateCcw, Clock } from 'lucide-react'
import { PaneLoader, PageHeader, SortableTh, FreshnessControl, type SortDir, Input } from '@skyhook-io/k8s-ui'
import { useConnection } from '../../context/ConnectionContext'
import { clsx } from 'clsx'
import { useHelmReleases, useHelmBatchUpgradeInfo, isForbiddenError } from '../../api/client'
import type { HelmOperation, HelmRelease, SelectedHelmRelease, UpgradeInfo, ChartSource } from '../../types'
import { getStatusColor, formatAge, isHelmReleaseActionable } from './helm-utils'
import { SEVERITY_BADGE, SEVERITY_DOT, SEVERITY_TEXT } from '../../utils/badge-colors'
import { Tooltip } from '../ui/Tooltip'
import { ChartBrowser } from './ChartBrowser'
import { InstallWizard } from './InstallWizard'

type ViewTab = 'releases' | 'charts'

interface HelmViewProps {
  namespaces: string[]
  selectedRelease?: SelectedHelmRelease | null
  onReleaseClick?: (namespace: string, name: string, storageNamespace?: string) => void
}

export function HelmView({ namespaces, selectedRelease, onReleaseClick }: HelmViewProps) {
  const [activeTab, setActiveTab] = useState<ViewTab>('releases')
  const [searchTerm, setSearchTerm] = useState('')
  const [selectedChart, setSelectedChart] = useState<{ repo: string; chart: string; version: string; source: ChartSource } | null>(null)

  const { data: releases, isLoading, error: releasesError, dataUpdatedAt: releasesUpdatedAt, isFetching: releasesFetching, refetch: refetchReleases } = useHelmReleases(namespaces)
  const isForbidden = isForbiddenError(releasesError)
  const releasesErrorMessage = releasesError instanceof Error ? releasesError.message : 'Failed to load Helm releases'

  // Lazy load upgrade info after releases are loaded
  const { data: upgradeInfo, isLoading: upgradeLoading, error: upgradeError, refetch: refetchUpgradeInfo } = useHelmBatchUpgradeInfo(
    namespaces,
    Boolean(releases && releases.length > 0)
  )
  const upgradeErrorMessage = upgradeError instanceof Error ? upgradeError.message : 'Upgrade checks failed'

  const { connection } = useConnection()
  const refetchAll = () => Promise.all([refetchReleases(), refetchUpgradeInfo()])

  // Filter releases by search term
  // Resources-table cycle: asc → desc → off (null restores the server's order).
  const [sort, setSort] = useState<{ key: HelmSortKey; dir: SortDir } | null>(null)
  const onSort = useCallback(
    (key: HelmSortKey) =>
      setSort((prev) => {
        if (!prev || prev.key !== key) return { key, dir: 'asc' }
        if (prev.dir === 'asc') return { key, dir: 'desc' }
        return null
      }),
    [],
  )

  const filteredReleases = useMemo(() => {
    if (!releases) return []
    const term = searchTerm.trim().toLowerCase()
    const filtered = term
      ? releases.filter(
          (r) =>
            r.name.toLowerCase().includes(term) ||
            r.namespace.toLowerCase().includes(term) ||
            r.chart.toLowerCase().includes(term)
        )
      : releases

    if (!sort) return filtered
    const factor = sort.dir === 'asc' ? 1 : -1
    return [...filtered].sort((a, b) => compareReleases(a, b, sort.key) * factor)
  }, [releases, searchTerm, sort])

  // Keyboard navigation state
  const searchInputRef = useRef<HTMLInputElement>(null)
  const highlightedRowRef = useRef<HTMLTableRowElement>(null)
  const [highlightedIndex, setHighlightedIndex] = useState(-1)
  const filteredReleasesCountRef = useRef(0)
  filteredReleasesCountRef.current = filteredReleases.length

  // Reset highlight when the visible order changes (search or sort) — otherwise
  // the index would point at whatever release shifted into that row.
  useEffect(() => { setHighlightedIndex(-1) }, [searchTerm, sort])

  // Scroll highlighted row into view
  useEffect(() => {
    if (highlightedIndex >= 0 && highlightedRowRef.current) {
      highlightedRowRef.current.scrollIntoView({ block: 'nearest' })
    }
  }, [highlightedIndex])

  // Helper: get release at highlighted index
  const getHighlightedRelease = useCallback(() => {
    if (highlightedIndex < 0 || highlightedIndex >= filteredReleases.length) return null
    return filteredReleases[highlightedIndex]
  }, [highlightedIndex, filteredReleases])

  // Register Helm keyboard shortcuts
  useRegisterShortcuts([
    {
      id: 'helm-search',
      keys: '/',
      description: 'Focus search',
      category: 'Search',
      scope: 'helm',
      handler: () => searchInputRef.current?.focus(),
    },
    {
      id: 'helm-nav-down',
      keys: 'j',
      description: 'Next row',
      category: 'Helm',
      scope: 'helm',
      handler: () => setHighlightedIndex(i => {
        const max = filteredReleasesCountRef.current - 1
        return i < max ? i + 1 : i
      }),
    },
    {
      id: 'helm-nav-down-arrow',
      keys: 'ArrowDown',
      description: 'Next row',
      category: 'Helm',
      scope: 'helm',
      handler: () => setHighlightedIndex(i => {
        const max = filteredReleasesCountRef.current - 1
        return i < max ? i + 1 : i
      }),
    },
    {
      id: 'helm-nav-up',
      keys: 'k',
      description: 'Previous row',
      category: 'Helm',
      scope: 'helm',
      handler: () => setHighlightedIndex(i => i > 0 ? i - 1 : 0),
    },
    {
      id: 'helm-nav-up-arrow',
      keys: 'ArrowUp',
      description: 'Previous row',
      category: 'Helm',
      scope: 'helm',
      handler: () => setHighlightedIndex(i => i > 0 ? i - 1 : 0),
    },
    {
      id: 'helm-open',
      keys: 'Enter',
      description: 'Open release detail',
      category: 'Helm',
      scope: 'helm',
      handler: () => {
        const release = getHighlightedRelease()
        if (release) onReleaseClick?.(release.namespace, release.name, release.storageNamespace)
      },
      enabled: highlightedIndex >= 0,
    },
    {
      id: 'helm-clear-highlight',
      keys: 'Escape',
      description: 'Clear highlight / blur search',
      category: 'Helm',
      scope: 'helm',
      handler: () => {
        if (highlightedIndex >= 0) setHighlightedIndex(-1)
        else searchInputRef.current?.blur()
      },
    },
  ])

  const handleChartSelect = (repo: string, chart: string, version: string, source: ChartSource) => {
    setSelectedChart({ repo, chart, version, source })
  }

  const handleInstallSuccess = (releaseNamespace: string, releaseName: string) => {
    setSelectedChart(null)
    setActiveTab('releases')
    refetchReleases()
    // Navigate to the new release
    onReleaseClick?.(releaseNamespace, releaseName)
  }

  return (
    <div className="flex h-full w-full">
      {/* Main Content */}
      <div className="flex-1 flex flex-col overflow-hidden min-w-0 w-full">
        {/* Page header — consistent across views; the tabs + search sit below. */}
        <div className="px-4 pt-4 pb-1">
          <PageHeader
            icon={Package}
            title="Helm"
            description="Installed Helm releases and the chart catalog for this cluster."
            actions={
              activeTab === 'releases' ? (
                <FreshnessControl
                  mode="snapshot"
                  dataUpdatedAt={releasesUpdatedAt}
                  isFetching={releasesFetching || upgradeLoading}
                  onRefresh={refetchAll}
                  connectionState={connection.state}
                />
              ) : undefined
            }
          />
        </div>
        {/* Tab bar */}
        <div className="flex items-center gap-1 px-4 pt-3 border-b border-theme-border bg-theme-surface/50">
          <button
            onClick={() => setActiveTab('releases')}
            className={clsx(
              'flex items-center gap-2 px-4 py-2.5 text-sm font-medium border-b-2 -mb-px transition-colors',
              activeTab === 'releases'
                ? 'text-theme-text-primary border-blue-500'
                : 'text-theme-text-secondary border-transparent hover:text-theme-text-primary hover:border-theme-border'
            )}
          >
            <List className="w-4 h-4" />
            Installed
            {releases && (
              <span className="badge-sm bg-theme-elevated">
                {releases.length}
              </span>
            )}
          </button>
          <button
            onClick={() => setActiveTab('charts')}
            className={clsx(
              'flex items-center gap-2 px-4 py-2.5 text-sm font-medium border-b-2 -mb-px transition-colors',
              activeTab === 'charts'
                ? 'text-theme-text-primary border-blue-500'
                : 'text-theme-text-secondary border-transparent hover:text-theme-text-primary hover:border-theme-border'
            )}
          >
            <LayoutGrid className="w-4 h-4" />
            Catalog
          </button>
        </div>

        {activeTab === 'releases' ? (
          <>
            {/* Releases Toolbar */}
            <div className="flex items-center gap-4 px-4 py-3 border-b border-theme-border bg-theme-surface/50 shrink-0">
              <div className="flex-1 relative">
                <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-theme-text-tertiary" />
                <Input
                  ref={searchInputRef}
                  placeholder="Search releases..."
                  value={searchTerm}
                  onChange={(e) => setSearchTerm(e.target.value)}
                  className="w-full max-w-md pl-10 pr-4 py-2 bg-theme-elevated border border-theme-border-light rounded-lg text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
              </div>
            </div>

            {/* Releases Table */}
            <div className="flex-1 overflow-auto">
              {upgradeError && (
                <div className="m-4 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-300">
                  Upgrade checks failed: {upgradeErrorMessage}
                </div>
              )}
              {isLoading ? (
                <PaneLoader className="h-full" />
              ) : isForbidden ? (
                <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary">
                  <Shield className="w-8 h-8 text-amber-400 mb-2" />
                  <p className="text-theme-text-secondary font-medium">Access Restricted</p>
                  <p className="text-sm mt-1">Insufficient permissions to list Helm releases</p>
                </div>
              ) : releasesError ? (
                <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary gap-3 px-6 text-center">
                  <Package className="w-10 h-10 text-amber-400" />
                  <div>
                    <p className="text-theme-text-secondary font-medium">Failed to load Helm releases</p>
                    <p className="text-sm mt-1 break-all">{releasesErrorMessage}</p>
                  </div>
                  <button
                    onClick={() => refetchReleases()}
                    className="px-3 py-1.5 text-sm text-theme-text-primary border border-theme-border rounded-lg hover:bg-theme-elevated transition-colors"
                  >
                    Retry
                  </button>
                </div>
              ) : filteredReleases.length === 0 ? (
                <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary gap-2">
                  <Package className="w-12 h-12 text-theme-text-disabled" />
                  <span>No Helm releases found</span>
                  {searchTerm && (
                    <button
                      onClick={() => setSearchTerm('')}
                      className="text-blue-400 hover:text-blue-300 text-sm"
                    >
                      Clear search
                    </button>
                  )}
                  {!searchTerm && (
                    <button
                      onClick={() => setActiveTab('charts')}
                      className="mt-2 px-4 py-2 text-sm text-skyhook-400 hover:text-skyhook-300 border border-skyhook-500/30 rounded-lg hover:bg-skyhook-500/10 transition-colors"
                    >
                      Browse charts to install
                    </button>
                  )}
                </div>
              ) : (
                <table className="w-full table-fixed">
                  <thead className="bg-theme-base sticky top-0 z-10">
                    <tr>
                      <SortableTh label="Name" sortKey="name" activeKey={sort?.key ?? null} direction={sort?.dir ?? 'asc'} onSort={onSort} className="w-[28%]" />
                      <SortableTh label="Namespace" sortKey="namespace" activeKey={sort?.key ?? null} direction={sort?.dir ?? 'asc'} onSort={onSort} className="w-[18%]" />
                      <SortableTh label="Chart" sortKey="chart" activeKey={sort?.key ?? null} direction={sort?.dir ?? 'asc'} onSort={onSort} className="w-[22%]" />
                      <SortableTh label="App Version" sortKey="appVersion" activeKey={sort?.key ?? null} direction={sort?.dir ?? 'asc'} onSort={onSort} className="w-24 hidden xl:table-cell" />
                      <SortableTh label="Status" sortKey="status" activeKey={sort?.key ?? null} direction={sort?.dir ?? 'asc'} onSort={onSort} className="w-40" />
                      <SortableTh label="Rev" sortKey="revision" activeKey={sort?.key ?? null} direction={sort?.dir ?? 'asc'} onSort={onSort} className="w-16" />
                      <SortableTh label="Updated" sortKey="updated" activeKey={sort?.key ?? null} direction={sort?.dir ?? 'asc'} onSort={onSort} className="w-24" />
                    </tr>
                  </thead>
                  <tbody className="table-divide-subtle">
                    {filteredReleases.map((release, index) => (
                      <ReleaseRow
                        key={releaseIdentityKey(release)}
                        ref={index === highlightedIndex ? highlightedRowRef : null}
                        release={release}
                        upgradeInfo={upgradeInfo?.releases[releaseIdentityKey(release)]}
                        isSelected={
                          selectedRelease?.namespace === release.namespace &&
                          selectedRelease?.name === release.name &&
                          (selectedRelease?.storageNamespace || selectedRelease?.namespace) === (release.storageNamespace || release.namespace)
                        }
                        isHighlighted={index === highlightedIndex}
                        onClick={() => onReleaseClick?.(release.namespace, release.name, release.storageNamespace)}
                        onMouseEnter={() => setHighlightedIndex(-1)}
                      />
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </>
        ) : (
          <ChartBrowser onChartSelect={handleChartSelect} />
        )}
      </div>

      {/* Install wizard modal */}
      {selectedChart && (
        <InstallWizard
          repo={selectedChart.repo}
          chartName={selectedChart.chart}
          version={selectedChart.version}
          source={selectedChart.source}
          onClose={() => setSelectedChart(null)}
          onSuccess={handleInstallSuccess}
        />
      )}
    </div>
  )
}

function releaseIdentityKey(release: Pick<HelmRelease, 'namespace' | 'name' | 'storageNamespace'>): string {
  return `${release.storageNamespace || release.namespace}/${release.name}`
}

type HelmSortKey = 'name' | 'namespace' | 'chart' | 'appVersion' | 'status' | 'revision' | 'updated'

function compareReleases(a: HelmRelease, b: HelmRelease, key: HelmSortKey): number {
  let cmp: number
  switch (key) {
    case 'revision':
      cmp = a.revision - b.revision
      break
    case 'updated':
      cmp = (Date.parse(a.updated) || 0) - (Date.parse(b.updated) || 0)
      break
    default:
      cmp = String(a[key] ?? '').localeCompare(String(b[key] ?? ''))
  }
  return cmp || a.name.localeCompare(b.name)
}

interface ReleaseRowProps {
  release: HelmRelease
  upgradeInfo?: UpgradeInfo
  isSelected: boolean
  isHighlighted?: boolean
  onClick: () => void
  onMouseEnter?: () => void
}

// Get actionable tooltip content for health issues
function getActionableTooltip(issue: string | undefined, summary: string | undefined, health: string): React.ReactNode {
  const issueDetails: Record<string, { description: string; action: string }> = {
    OOMKilled: {
      description: 'Container exceeded its memory limit and was killed.',
      action: 'Increase memory limits in Helm values or optimize app memory usage.',
    },
    CrashLoopBackOff: {
      description: 'Container is repeatedly crashing.',
      action: 'Check pod logs for crash reason.',
    },
    ImagePullBackOff: {
      description: 'Cannot pull container image.',
      action: 'Verify image name in Helm values and registry credentials.',
    },
  }

  const details = issue ? issueDetails[issue] : null

  return (
    <div className="max-w-xs">
      <div className={clsx(
        'font-medium',
        health === 'unhealthy' ? SEVERITY_TEXT.error : SEVERITY_TEXT.warning
      )}>
        {summary || issue || health}
      </div>
      {details && (
        <>
          <div className="text-theme-text-secondary text-[10px] mt-1">{details.description}</div>
          <div className="text-blue-400 text-[10px] mt-1.5 border-t border-theme-border pt-1.5">
            💡 {details.action}
          </div>
        </>
      )}
      {!details && issue && (
        <div className="text-blue-400 text-[10px] mt-1.5">Click release for details</div>
      )}
    </div>
  )
}

function getListOperation(release: HelmRelease): HelmOperation | undefined {
  if (release.lastOperation) {
    const releaseStatus = release.status.toLowerCase()
    if (release.lastOperation.status === 'failed' && releaseStatus === 'failed') {
      return undefined
    }
    if (isListOperation(release.lastOperation)) {
      return release.lastOperation
    }
  }
  return release.operations?.find(isListOperation)
}

function isListOperation(operation: HelmOperation): boolean {
  return operation.kind === 'upgrade_rolled_back' || operation.kind === 'rollback' || operation.status === 'stuck_pending'
}

function HelmOperationChip({ operation }: { operation: HelmOperation }) {
  const isPending = operation.status === 'stuck_pending'
  const isRollback = operation.kind === 'upgrade_rolled_back' || operation.kind === 'rollback'
  const Icon = isRollback ? RotateCcw : Clock
  const tone: keyof typeof SEVERITY_BADGE = operation.kind === 'rollback' ? 'info' : isPending ? 'warning' : 'alert'
  const label = operation.kind === 'upgrade_rolled_back'
    ? 'rolled back'
    : operation.kind === 'rollback'
      ? 'rollback'
      : 'stuck'

  return (
    <Tooltip content={
      <div className="max-w-xs">
        <div className="font-medium text-theme-text-primary">{operationSummary(operation)}</div>
        <div className="mt-1 text-[10px] text-theme-text-secondary">{operation.message}</div>
        {operation.failureDescription && (
          <div className="mt-1.5 border-t border-theme-border pt-1.5 text-[10px] text-theme-text-tertiary">
            {operation.failureDescription}
          </div>
        )}
      </div>
    }>
      <span className={clsx('badge-sm shrink-0', SEVERITY_BADGE[tone])} aria-label={operationSummary(operation)}>
        <Icon className="h-3 w-3" />
        <span className="sr-only">{label}</span>
        <span className="hidden 2xl:inline" aria-hidden="true">{label}</span>
      </span>
    </Tooltip>
  )
}

function operationSummary(operation: HelmOperation): string {
  switch (operation.kind) {
    case 'upgrade_rolled_back':
      return 'Helm rolled back after failed upgrade'
    case 'rollback':
      return 'Helm rollback applied'
    case 'pending':
      return 'Helm operation may be stuck'
    case 'upgrade_failed':
      return 'Helm upgrade failed'
    default:
      return 'Helm release failed'
  }
}

const ReleaseRow = forwardRef<HTMLTableRowElement, ReleaseRowProps>(
  function ReleaseRow({ release, upgradeInfo, isSelected, isHighlighted, onClick, onMouseEnter }, ref) {
  const listOperation = getListOperation(release)

  // Health badge styling
  const getHealthBadge = () => {
    if (!release.resourceHealth || release.resourceHealth === 'unknown') return null

    const healthStyles: Record<string, { badge: string; dot: string }> = {
      healthy: { badge: SEVERITY_BADGE.success, dot: SEVERITY_DOT.success },
      degraded: { badge: SEVERITY_BADGE.warning, dot: SEVERITY_DOT.warning },
      unhealthy: { badge: SEVERITY_BADGE.error, dot: SEVERITY_DOT.error },
    }

    const style = healthStyles[release.resourceHealth] || healthStyles.healthy
    const tooltipContent = getActionableTooltip(release.healthIssue, release.healthSummary, release.resourceHealth)

    return (
      <Tooltip content={tooltipContent}>
        <span className={clsx(
          'badge-sm shrink-0',
          style.badge
        )}>
          <span className={clsx('w-1.5 h-1.5 rounded-full', style.dot)} />
          {release.healthIssue || (release.resourceHealth !== 'healthy' ? release.healthSummary : null)}
        </span>
      </Tooltip>
    )
  }

  return (
    <tr
      ref={ref}
      onClick={onClick}
      onMouseEnter={onMouseEnter}
      className={clsx(
        'cursor-pointer transition-colors',
        isSelected
          ? 'selection-strong hover:bg-skyhook-500/30'
          : isHighlighted
            ? 'selection selection-ring'
            : 'hover:bg-theme-surface/50'
      )}
    >
      <td className="px-4 py-3">
        <div className="flex min-w-0 items-center gap-2">
          <Package className="w-4 h-4 text-theme-text-tertiary shrink-0" />
          <span className="min-w-0 truncate text-sm font-medium text-theme-text-primary">{release.name}</span>
          {getHealthBadge()}
          {listOperation && <HelmOperationChip operation={listOperation} />}
          {release.managedByFluxHelmRelease && (
            <Tooltip content={`Installed by Flux helm-controller via HelmRelease ${release.managedByFluxHelmRelease}. Changes here will be reverted at the next reconcile — manage via the GitOps tab.`}>
              <span className="badge-sm shrink-0 border border-theme-border bg-theme-elevated text-theme-text-secondary">
                <GitBranch className="w-3 h-3" />
                Flux
              </span>
            </Tooltip>
          )}
          {upgradeInfo?.updateAvailable && (
            <Tooltip content={`Upgrade available: ${release.chartVersion} → ${upgradeInfo.latestVersion}`}>
              <span className={clsx('badge-sm shrink-0', SEVERITY_BADGE.warning)}>
                <ArrowUpCircle className="w-3 h-3" />
              </span>
            </Tooltip>
          )}
        </div>
      </td>
      <td className="px-4 py-3 w-[18%]">
        <Tooltip content={release.namespace}>
          <span className="block truncate text-sm text-theme-text-secondary">{release.namespace}</span>
        </Tooltip>
      </td>
      <td className="px-4 py-3 w-[22%]">
        <Tooltip content={`${release.chart}-${release.chartVersion}`}>
          <span className="text-sm text-theme-text-secondary truncate block">
            {release.chart}-{release.chartVersion}
          </span>
        </Tooltip>
      </td>
      <td className="px-4 py-3 w-24 hidden xl:table-cell">
        {release.appVersion ? (
          <span className="text-sm text-theme-text-secondary">{release.appVersion}</span>
        ) : (
          <Tooltip content="This chart did not declare an appVersion in Chart.yaml.">
            <span className="text-sm text-theme-text-disabled cursor-help">—</span>
          </Tooltip>
        )}
      </td>
      <td className="px-4 py-3 w-40">
        {isHelmReleaseActionable(release.status) ? (
          <Tooltip content="Click row to view rollback / history / logs and recover">
            <span
              className={clsx('badge inline-flex items-center gap-1', getStatusColor(release.status))}
            >
              {release.status}
              <ChevronRight className="w-3 h-3 opacity-70" />
            </span>
          </Tooltip>
        ) : (
          <span
            className={clsx(
              'badge',
              getStatusColor(release.status)
            )}
          >
            {release.status}
          </span>
        )}
      </td>
      <td className="px-4 py-3 w-16">
        <span className="text-sm text-theme-text-secondary">{release.revision}</span>
      </td>
      <td className="px-4 py-3 w-24">
        <Tooltip content={release.updated}>
          <span className="text-sm text-theme-text-secondary">
            {formatAge(release.updated)}
          </span>
        </Tooltip>
      </td>
    </tr>
  )
})
