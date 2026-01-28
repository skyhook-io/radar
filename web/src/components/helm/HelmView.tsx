import { useState, useMemo } from 'react'
import { Package, Search, RefreshCw, ArrowUpCircle, LayoutGrid, List } from 'lucide-react'
import { clsx } from 'clsx'
import { useHelmReleases, useHelmBatchUpgradeInfo } from '../../api/client'
import type { HelmRelease, SelectedHelmRelease, UpgradeInfo, ChartSource } from '../../types'
import { getStatusColor, formatAge, truncate } from './helm-utils'
import { Tooltip } from '../ui/Tooltip'
import { ChartBrowser } from './ChartBrowser'
import { InstallWizard } from './InstallWizard'

type ViewTab = 'releases' | 'charts'

interface HelmViewProps {
  namespace: string
  selectedRelease?: SelectedHelmRelease | null
  onReleaseClick?: (namespace: string, name: string) => void
}

export function HelmView({ namespace, selectedRelease, onReleaseClick }: HelmViewProps) {
  const [activeTab, setActiveTab] = useState<ViewTab>('releases')
  const [searchTerm, setSearchTerm] = useState('')
  const [selectedChart, setSelectedChart] = useState<{ repo: string; chart: string; version: string; source: ChartSource } | null>(null)

  const { data: releases, isLoading, refetch } = useHelmReleases(namespace || undefined)

  // Lazy load upgrade info after releases are loaded
  const { data: upgradeInfo, isLoading: upgradeLoading } = useHelmBatchUpgradeInfo(
    namespace || undefined,
    Boolean(releases && releases.length > 0)
  )

  const isFullyLoaded = !isLoading && !upgradeLoading

  // Filter releases by search term
  const filteredReleases = useMemo(() => {
    if (!releases) return []
    if (!searchTerm) return releases
    const term = searchTerm.toLowerCase()
    return releases.filter(
      (r) =>
        r.name.toLowerCase().includes(term) ||
        r.namespace.toLowerCase().includes(term) ||
        r.chart.toLowerCase().includes(term)
    )
  }, [releases, searchTerm])

  const handleChartSelect = (repo: string, chart: string, version: string, source: ChartSource) => {
    setSelectedChart({ repo, chart, version, source })
  }

  const handleInstallSuccess = (releaseNamespace: string, releaseName: string) => {
    setSelectedChart(null)
    setActiveTab('releases')
    refetch()
    // Navigate to the new release
    onReleaseClick?.(releaseNamespace, releaseName)
  }

  return (
    <div className="flex h-full w-full">
      {/* Main Content */}
      <div className="flex-1 flex flex-col overflow-hidden min-w-0 w-full">
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
              <span className="text-xs bg-theme-elevated px-1.5 py-0.5 rounded">
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
              <div className="flex items-center gap-2 text-theme-text-secondary">
                <Package className="w-5 h-5" />
                <span className="font-medium">Helm Releases</span>
                {!isFullyLoaded && (
                  <RefreshCw className="w-3.5 h-3.5 animate-spin text-theme-text-tertiary" />
                )}
              </div>
              <div className="flex-1 relative">
                <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-theme-text-tertiary" />
                <input
                  type="text"
                  placeholder="Search releases..."
                  value={searchTerm}
                  onChange={(e) => setSearchTerm(e.target.value)}
                  className="w-full max-w-md pl-10 pr-4 py-2 bg-theme-elevated border border-theme-border-light rounded-lg text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
              </div>
              <button
                onClick={() => refetch()}
                className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg"
                title="Refresh"
              >
                <RefreshCw className="w-4 h-4" />
              </button>
            </div>

            {/* Releases Table */}
            <div className="flex-1 overflow-auto">
              {isLoading ? (
                <div className="flex items-center justify-center h-full text-theme-text-tertiary">
                  Loading...
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
                      className="mt-2 px-4 py-2 text-sm text-blue-400 hover:text-blue-300 border border-blue-500/30 rounded-lg hover:bg-blue-500/10 transition-colors"
                    >
                      Browse charts to install
                    </button>
                  )}
                </div>
              ) : (
                <table className="w-full table-fixed">
                  <thead className="bg-theme-surface sticky top-0 z-10">
                    <tr>
                      <th className="text-left px-4 py-3 text-xs font-medium text-theme-text-secondary uppercase tracking-wide">
                        Name
                      </th>
                      <th className="text-left px-4 py-3 text-xs font-medium text-theme-text-secondary uppercase tracking-wide w-32">
                        Namespace
                      </th>
                      <th className="text-left px-4 py-3 text-xs font-medium text-theme-text-secondary uppercase tracking-wide w-48">
                        Chart
                      </th>
                      <th className="text-left px-4 py-3 text-xs font-medium text-theme-text-secondary uppercase tracking-wide w-24 hidden xl:table-cell">
                        App Version
                      </th>
                      <th className="text-left px-4 py-3 text-xs font-medium text-theme-text-secondary uppercase tracking-wide w-28">
                        Status
                      </th>
                      <th className="text-left px-4 py-3 text-xs font-medium text-theme-text-secondary uppercase tracking-wide w-20">
                        Rev
                      </th>
                      <th className="text-left px-4 py-3 text-xs font-medium text-theme-text-secondary uppercase tracking-wide w-24">
                        Updated
                      </th>
                    </tr>
                  </thead>
                  <tbody className="table-divide-subtle">
                    {filteredReleases.map((release) => (
                      <ReleaseRow
                        key={`${release.namespace}-${release.name}`}
                        release={release}
                        upgradeInfo={upgradeInfo?.releases[`${release.namespace}/${release.name}`]}
                        isSelected={
                          selectedRelease?.namespace === release.namespace &&
                          selectedRelease?.name === release.name
                        }
                        onClick={() => onReleaseClick?.(release.namespace, release.name)}
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

interface ReleaseRowProps {
  release: HelmRelease
  upgradeInfo?: UpgradeInfo
  isSelected: boolean
  onClick: () => void
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
        health === 'unhealthy' ? 'text-red-400' : 'text-yellow-400'
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

function ReleaseRow({ release, upgradeInfo, isSelected, onClick }: ReleaseRowProps) {
  // Health badge styling
  const getHealthBadge = () => {
    if (!release.resourceHealth || release.resourceHealth === 'unknown') return null

    const healthStyles: Record<string, { bg: string; text: string; dot: string }> = {
      healthy: { bg: 'bg-green-500/10', text: 'text-green-400', dot: 'bg-green-500' },
      degraded: { bg: 'bg-yellow-500/10', text: 'text-yellow-400', dot: 'bg-yellow-500' },
      unhealthy: { bg: 'bg-red-500/10', text: 'text-red-400', dot: 'bg-red-500' },
    }

    const style = healthStyles[release.resourceHealth] || healthStyles.healthy
    const tooltipContent = getActionableTooltip(release.healthIssue, release.healthSummary, release.resourceHealth)

    return (
      <Tooltip content={tooltipContent}>
        <span className={clsx(
          'flex items-center gap-1 px-1.5 py-0.5 text-xs font-medium rounded shrink-0',
          style.bg, style.text
        )}>
          <span className={clsx('w-1.5 h-1.5 rounded-full', style.dot)} />
          {release.healthIssue || (release.resourceHealth !== 'healthy' ? release.healthSummary : null)}
        </span>
      </Tooltip>
    )
  }

  return (
    <tr
      onClick={onClick}
      className={clsx(
        'cursor-pointer transition-colors',
        isSelected
          ? 'bg-blue-500/20 hover:bg-blue-500/30'
          : 'hover:bg-theme-surface/50'
      )}
    >
      <td className="px-4 py-3">
        <div className="flex items-center gap-2">
          <Package className="w-4 h-4 text-theme-text-tertiary shrink-0" />
          <span className="text-sm text-theme-text-primary font-medium truncate">{release.name}</span>
          {getHealthBadge()}
          {upgradeInfo?.updateAvailable && (
            <Tooltip content={`Upgrade available: ${release.chartVersion} → ${upgradeInfo.latestVersion}`}>
              <span className="flex items-center gap-0.5 px-1.5 py-0.5 text-xs font-medium rounded bg-amber-500/20 text-amber-400 border border-amber-500/30 shrink-0">
                <ArrowUpCircle className="w-3 h-3" />
              </span>
            </Tooltip>
          )}
        </div>
      </td>
      <td className="px-4 py-3 w-32">
        <span className="text-sm text-theme-text-secondary">{release.namespace}</span>
      </td>
      <td className="px-4 py-3 w-48">
        <Tooltip content={`${release.chart}-${release.chartVersion}`}>
          <span className="text-sm text-theme-text-secondary truncate block">
            {truncate(`${release.chart}-${release.chartVersion}`, 35)}
          </span>
        </Tooltip>
      </td>
      <td className="px-4 py-3 w-24 hidden xl:table-cell">
        <span className="text-sm text-theme-text-secondary">{release.appVersion || '-'}</span>
      </td>
      <td className="px-4 py-3 w-28">
        <span
          className={clsx(
            'inline-flex items-center px-2 py-0.5 rounded text-xs font-medium',
            getStatusColor(release.status)
          )}
        >
          {release.status}
        </span>
      </td>
      <td className="px-4 py-3 w-20">
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
}
