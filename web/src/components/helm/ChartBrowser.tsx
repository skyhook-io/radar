import { useState, useMemo } from 'react'
import { Search, RefreshCw, Package, Database, AlertCircle, ExternalLink, ChevronDown, Star, Shield, BadgeCheck, Building2, Globe, ArrowUpDown, FileJson, PenTool } from 'lucide-react'
import { clsx } from 'clsx'
import { useHelmRepositories, useSearchCharts, useUpdateRepository, useArtifactHubSearch, type ArtifactHubSortOption } from '../../api/client'
import { useCanHelmWrite } from '../../contexts/CapabilitiesContext'
import type { ChartInfo, HelmRepository, ArtifactHubChart, ChartSource } from '../../types'
import { formatAge } from './helm-utils'
import { Tooltip } from '../ui/Tooltip'

interface ChartBrowserProps {
  onChartSelect: (repo: string, chart: string, version: string, source: ChartSource) => void
}

export function ChartBrowser({ onChartSelect }: ChartBrowserProps) {
  const [chartSource, setChartSource] = useState<ChartSource>('local')
  const [searchTerm, setSearchTerm] = useState('')
  const [selectedRepo, setSelectedRepo] = useState<string>('all')
  const [showAllVersions, setShowAllVersions] = useState(false)
  const [repoDropdownOpen, setRepoDropdownOpen] = useState(false)
  // ArtifactHub filters
  const [showOfficialOnly, setShowOfficialOnly] = useState(false)
  const [showVerifiedOnly, setShowVerifiedOnly] = useState(false)
  const [artifactHubSort, setArtifactHubSort] = useState<ArtifactHubSortOption>('relevance')

  const canHelmWrite = useCanHelmWrite()

  // Local repo hooks
  const { data: repositories, isLoading: reposLoading } = useHelmRepositories()
  const { data: searchResult, isLoading: chartsLoading, refetch: refetchCharts } = useSearchCharts(
    searchTerm,
    showAllVersions,
    chartSource === 'local'
  )
  const updateRepoMutation = useUpdateRepository()

  // ArtifactHub hook - only search when there's a search term
  const { data: artifactHubResult, isLoading: artifactHubLoading } = useArtifactHubSearch(
    searchTerm,
    { official: showOfficialOnly, verified: showVerifiedOnly, limit: 60, sort: artifactHubSort },
    chartSource === 'artifacthub' && searchTerm.length > 0
  )

  // Filter local charts by selected repository
  const filteredLocalCharts = useMemo(() => {
    if (!searchResult?.charts) return []
    if (selectedRepo === 'all') return searchResult.charts
    return searchResult.charts.filter(c => c.repository === selectedRepo)
  }, [searchResult?.charts, selectedRepo])

  // Group local charts by repository for display
  const chartsByRepo = useMemo(() => {
    const groups = new Map<string, ChartInfo[]>()
    for (const chart of filteredLocalCharts) {
      const existing = groups.get(chart.repository) || []
      existing.push(chart)
      groups.set(chart.repository, existing)
    }
    return groups
  }, [filteredLocalCharts])

  const handleUpdateRepo = async (repoName: string) => {
    await updateRepoMutation.mutateAsync(repoName)
    refetchCharts()
  }

  const handleUpdateAllRepos = async () => {
    if (!repositories) return
    for (const repo of repositories) {
      try {
        await updateRepoMutation.mutateAsync(repo.name)
      } catch {
        // Continue with other repos
      }
    }
    refetchCharts()
  }

  const isLoading = chartSource === 'local' ? chartsLoading : artifactHubLoading
  const totalCount = chartSource === 'local'
    ? filteredLocalCharts.length
    : (artifactHubResult?.charts.length ?? 0)

  return (
    <div className="flex flex-col h-full">
      {/* Toolbar */}
      <div className="flex items-center gap-4 px-4 py-3 border-b border-theme-border bg-theme-surface/50 shrink-0">
        <div className="flex items-center gap-2 text-theme-text-secondary">
          <Package className="w-5 h-5" />
          <span className="font-medium">Charts</span>
          {!isLoading && (
            <span className="text-xs bg-theme-elevated px-2 py-0.5 rounded">
              {totalCount}
            </span>
          )}
        </div>

        {/* Source toggle */}
        <div className="flex items-center bg-theme-elevated rounded-lg p-0.5 border border-theme-border-light">
          <button
            onClick={() => setChartSource('local')}
            className={clsx(
              'flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md transition-colors',
              chartSource === 'local'
                ? 'bg-blue-500/20 text-blue-400 font-medium'
                : 'text-theme-text-secondary hover:text-theme-text-primary'
            )}
          >
            <Database className="w-3.5 h-3.5" />
            My Repos
          </button>
          <button
            onClick={() => setChartSource('artifacthub')}
            className={clsx(
              'flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md transition-colors',
              chartSource === 'artifacthub'
                ? 'bg-blue-500/20 text-blue-400 font-medium'
                : 'text-theme-text-secondary hover:text-theme-text-primary'
            )}
          >
            <Globe className="w-3.5 h-3.5" />
            ArtifactHub
          </button>
        </div>

        {/* Repository filter dropdown (only for local) */}
        {chartSource === 'local' && (
          <div className="relative">
            <button
              onClick={() => setRepoDropdownOpen(!repoDropdownOpen)}
              className="flex items-center gap-2 px-3 py-2 bg-theme-elevated border border-theme-border-light rounded-lg text-sm text-theme-text-primary hover:bg-theme-hover transition-colors"
            >
              <Database className="w-4 h-4 text-theme-text-tertiary" />
              <span>{selectedRepo === 'all' ? 'All Repositories' : selectedRepo}</span>
              <ChevronDown className={clsx('w-4 h-4 text-theme-text-tertiary transition-transform', repoDropdownOpen && 'rotate-180')} />
            </button>

            {repoDropdownOpen && (
              <div className="absolute top-full left-0 mt-1 w-64 bg-theme-surface border border-theme-border rounded-lg shadow-xl z-50 py-1 max-h-64 overflow-auto">
                <button
                  onClick={() => { setSelectedRepo('all'); setRepoDropdownOpen(false) }}
                  className={clsx(
                    'w-full px-3 py-2 text-left text-sm hover:bg-theme-hover flex items-center justify-between',
                    selectedRepo === 'all' ? 'text-blue-400' : 'text-theme-text-primary'
                  )}
                >
                  <span>All Repositories</span>
                  {selectedRepo === 'all' && <span className="text-xs">✓</span>}
                </button>
                <div className="border-t border-theme-border my-1" />
                {reposLoading ? (
                  <div className="px-3 py-2 text-sm text-theme-text-tertiary">Loading...</div>
                ) : repositories?.length === 0 ? (
                  <div className="px-3 py-2 text-sm text-theme-text-tertiary">No repositories configured</div>
                ) : (
                  repositories?.map(repo => (
                    <RepoDropdownItem
                      key={repo.name}
                      repo={repo}
                      isSelected={selectedRepo === repo.name}
                      onSelect={() => { setSelectedRepo(repo.name); setRepoDropdownOpen(false) }}
                      onUpdate={() => handleUpdateRepo(repo.name)}
                      isUpdating={updateRepoMutation.isPending}
                      canUpdate={canHelmWrite}
                    />
                  ))
                )}
              </div>
            )}
          </div>
        )}

        {/* ArtifactHub filters */}
        {chartSource === 'artifacthub' && (
          <div className="flex items-center gap-3">
            <label className="flex items-center gap-1.5 text-sm text-theme-text-secondary cursor-pointer">
              <input
                type="checkbox"
                checked={showOfficialOnly}
                onChange={(e) => setShowOfficialOnly(e.target.checked)}
                className="rounded border-theme-border text-blue-500 focus:ring-blue-500"
              />
              <BadgeCheck className="w-3.5 h-3.5 text-blue-400" />
              Official
            </label>
            <label className="flex items-center gap-1.5 text-sm text-theme-text-secondary cursor-pointer">
              <input
                type="checkbox"
                checked={showVerifiedOnly}
                onChange={(e) => setShowVerifiedOnly(e.target.checked)}
                className="rounded border-theme-border text-blue-500 focus:ring-blue-500"
              />
              <Shield className="w-3.5 h-3.5 text-green-400" />
              Verified
            </label>
            {/* Sort dropdown */}
            <div className="flex items-center gap-1.5">
              <ArrowUpDown className="w-3.5 h-3.5 text-theme-text-tertiary" />
              <select
                value={artifactHubSort}
                onChange={(e) => setArtifactHubSort(e.target.value as ArtifactHubSortOption)}
                className="bg-theme-elevated border border-theme-border-light rounded px-2 py-1 text-sm text-theme-text-primary focus:outline-none focus:ring-2 focus:ring-blue-500"
              >
                <option value="relevance">Relevance</option>
                <option value="stars">Stars</option>
                <option value="last_updated">Last Updated</option>
              </select>
            </div>
          </div>
        )}

        {/* Search */}
        <div className="flex-1 relative">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-theme-text-tertiary" />
          <input
            type="text"
            placeholder={chartSource === 'local' ? "Search charts..." : "Search ArtifactHub..."}
            value={searchTerm}
            onChange={(e) => setSearchTerm(e.target.value)}
            className="w-full max-w-md pl-10 pr-4 py-2 bg-theme-elevated border border-theme-border-light rounded-lg text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
        </div>

        {/* Options (only for local) */}
        {chartSource === 'local' && (
          <>
            <label className="flex items-center gap-2 text-sm text-theme-text-secondary cursor-pointer">
              <input
                type="checkbox"
                checked={showAllVersions}
                onChange={(e) => setShowAllVersions(e.target.checked)}
                className="rounded border-theme-border text-blue-500 focus:ring-blue-500"
              />
              All versions
            </label>

            {/* Refresh button */}
            <Tooltip content={canHelmWrite ? "Update all repositories" : "Helm write permissions required (rbac.helm=true)"}>
              <button
                onClick={handleUpdateAllRepos}
                disabled={updateRepoMutation.isPending || !canHelmWrite}
                className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg disabled:opacity-50"
              >
                <RefreshCw className={clsx('w-4 h-4', updateRepoMutation.isPending && 'animate-spin')} />
              </button>
            </Tooltip>
          </>
        )}
      </div>

      {/* Chart grid */}
      <div className="flex-1 overflow-auto p-4">
        {isLoading ? (
          <div className="flex items-center justify-center h-32 text-theme-text-tertiary">
            Loading charts...
          </div>
        ) : chartSource === 'local' ? (
          // Local charts view
          filteredLocalCharts.length === 0 ? (
            <div className="flex flex-col items-center justify-center h-64 text-theme-text-tertiary gap-3">
              <AlertCircle className="w-12 h-12 text-theme-text-disabled" />
              <div className="text-center">
                <p className="text-lg font-medium text-theme-text-secondary">No charts found</p>
                {searchTerm ? (
                  <p className="text-sm mt-1">Try a different search term</p>
                ) : repositories?.length === 0 ? (
                  <div className="text-sm mt-1">
                    <p>No Helm repositories configured.</p>
                    <p className="mt-1">
                      Add repositories using <code className="bg-theme-elevated px-1 rounded">helm repo add</code>
                    </p>
                    <p className="mt-2">
                      Or try searching on <button onClick={() => setChartSource('artifacthub')} className="text-blue-400 hover:underline">ArtifactHub</button>
                    </p>
                  </div>
                ) : (
                  <p className="text-sm mt-1">Try updating your repositories</p>
                )}
              </div>
            </div>
          ) : selectedRepo === 'all' ? (
            // Grouped by repository
            <div className="space-y-6">
              {Array.from(chartsByRepo.entries()).map(([repoName, charts]) => (
                <div key={repoName}>
                  <div className="flex items-center gap-2 mb-3">
                    <Database className="w-4 h-4 text-theme-text-tertiary" />
                    <h3 className="text-sm font-medium text-theme-text-secondary">{repoName}</h3>
                    <span className="text-xs text-theme-text-tertiary">({charts.length})</span>
                  </div>
                  <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-3">
                    {charts.map((chart, idx) => (
                      <LocalChartCard
                        key={`${chart.repository}-${chart.name}-${chart.version}-${idx}`}
                        chart={chart}
                        onSelect={() => onChartSelect(chart.repository, chart.name, chart.version, 'local')}
                      />
                    ))}
                  </div>
                </div>
              ))}
            </div>
          ) : (
            // Single repository view
            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-3">
              {filteredLocalCharts.map((chart, idx) => (
                <LocalChartCard
                  key={`${chart.repository}-${chart.name}-${chart.version}-${idx}`}
                  chart={chart}
                  onSelect={() => onChartSelect(chart.repository, chart.name, chart.version, 'local')}
                />
              ))}
            </div>
          )
        ) : (
          // ArtifactHub view
          !searchTerm ? (
            // No search term yet - show prompt
            <div className="flex flex-col items-center justify-center h-64 text-theme-text-tertiary gap-3">
              <Globe className="w-12 h-12 text-theme-text-disabled" />
              <div className="text-center">
                <p className="text-lg font-medium text-theme-text-secondary">Search ArtifactHub</p>
                <p className="text-sm mt-1">Enter a search term to find charts from the community</p>
              </div>
            </div>
          ) : !artifactHubResult?.charts.length ? (
            // Searched but no results
            <div className="flex flex-col items-center justify-center h-64 text-theme-text-tertiary gap-3">
              <AlertCircle className="w-12 h-12 text-theme-text-disabled" />
              <div className="text-center">
                <p className="text-lg font-medium text-theme-text-secondary">No charts found</p>
                <p className="text-sm mt-1">Try a different search term or adjust filters</p>
              </div>
            </div>
          ) : (
            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-3">
              {artifactHubResult.charts.map((chart, idx) => (
                <ArtifactHubChartCard
                  key={`${chart.repository.name}-${chart.name}-${chart.version}-${idx}`}
                  chart={chart}
                  onSelect={() => onChartSelect(chart.repository.name, chart.name, chart.version, 'artifacthub')}
                />
              ))}
            </div>
          )
        )}
      </div>
    </div>
  )
}

interface RepoDropdownItemProps {
  repo: HelmRepository
  isSelected: boolean
  onSelect: () => void
  onUpdate: () => void
  isUpdating: boolean
  canUpdate: boolean
}

function RepoDropdownItem({ repo, isSelected, onSelect, onUpdate, isUpdating, canUpdate }: RepoDropdownItemProps) {
  return (
    <div className="flex items-center justify-between px-3 py-2 hover:bg-theme-hover group">
      <button
        onClick={onSelect}
        className={clsx(
          'flex-1 text-left text-sm truncate',
          isSelected ? 'text-blue-400' : 'text-theme-text-primary'
        )}
      >
        {repo.name}
        {repo.lastUpdated && (
          <span className="text-xs text-theme-text-tertiary ml-2">
            {formatAge(repo.lastUpdated)}
          </span>
        )}
      </button>
      <button
        onClick={(e) => { e.stopPropagation(); onUpdate() }}
        disabled={isUpdating || !canUpdate}
        className="p-1 text-theme-text-tertiary hover:text-theme-text-primary opacity-0 group-hover:opacity-100 transition-opacity disabled:opacity-50"
        title={canUpdate ? "Update repository" : "Helm write permissions required (rbac.helm=true)"}
      >
        <RefreshCw className={clsx('w-3.5 h-3.5', isUpdating && 'animate-spin')} />
      </button>
    </div>
  )
}

interface LocalChartCardProps {
  chart: ChartInfo
  onSelect: () => void
}

function LocalChartCard({ chart, onSelect }: LocalChartCardProps) {
  return (
    <button
      onClick={onSelect}
      className="flex flex-col p-3 bg-theme-elevated/30 hover:bg-theme-elevated/50 border border-theme-border-light rounded-lg text-left transition-colors group"
    >
      <div className="flex items-start gap-3">
        {chart.icon ? (
          <img
            src={chart.icon}
            alt=""
            className="w-10 h-10 rounded object-contain bg-white/10 p-1"
            onError={(e) => {
              (e.target as HTMLImageElement).style.display = 'none'
            }}
          />
        ) : (
          <div className="w-10 h-10 rounded bg-theme-hover flex items-center justify-center">
            <Package className="w-5 h-5 text-theme-text-tertiary" />
          </div>
        )}
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <h4 className="text-sm font-medium text-theme-text-primary truncate">{chart.name}</h4>
            {chart.deprecated && (
              <span className="px-1 py-0.5 text-[10px] rounded bg-amber-500/20 text-amber-400 border border-amber-500/30">
                deprecated
              </span>
            )}
          </div>
          <div className="flex items-center gap-2 mt-0.5">
            <span className="text-xs text-theme-text-tertiary">{chart.version}</span>
            {chart.appVersion && (
              <>
                <span className="text-xs text-theme-text-disabled">|</span>
                <span className="text-xs text-theme-text-tertiary">App: {chart.appVersion}</span>
              </>
            )}
          </div>
        </div>
        <ExternalLink className="w-4 h-4 text-theme-text-tertiary opacity-0 group-hover:opacity-100 shrink-0" />
      </div>
      {chart.description && (
        <p className="mt-2 text-xs text-theme-text-secondary line-clamp-2">
          {chart.description}
        </p>
      )}
    </button>
  )
}

interface ArtifactHubChartCardProps {
  chart: ArtifactHubChart
  onSelect: () => void
}

function ArtifactHubChartCard({ chart, onSelect }: ArtifactHubChartCardProps) {
  // Format Unix timestamp to relative age
  const lastUpdated = chart.updatedAt
    ? formatAge(new Date(chart.updatedAt * 1000).toISOString())
    : null

  return (
    <button
      onClick={onSelect}
      className="flex flex-col p-4 bg-theme-elevated/30 hover:bg-theme-elevated/50 border border-theme-border-light rounded-lg text-left transition-colors group"
    >
      {/* Header row */}
      <div className="flex items-start gap-3">
        {/* Logo */}
        {chart.logoUrl ? (
          <img
            src={chart.logoUrl}
            alt=""
            className="w-12 h-12 rounded object-contain bg-white/10 p-1 shrink-0"
            onError={(e) => {
              (e.target as HTMLImageElement).style.display = 'none'
            }}
          />
        ) : (
          <div className="w-12 h-12 rounded bg-theme-hover flex items-center justify-center shrink-0">
            <Package className="w-6 h-6 text-theme-text-tertiary" />
          </div>
        )}

        {/* Name and org */}
        <div className="flex-1 min-w-0">
          <h4 className="text-sm font-medium text-theme-text-primary truncate">{chart.name}</h4>
          <div className="flex items-center gap-2 mt-0.5 text-xs text-theme-text-tertiary">
            <span className="flex items-center gap-1">
              <Building2 className="w-3 h-3" />
              {chart.repository.organizationName || chart.repository.name}
            </span>
            <span className="flex items-center gap-1">
              <Globe className="w-3 h-3" />
              {chart.repository.name}
            </span>
          </div>
        </div>

        {/* Stats - top right */}
        <div className="text-right shrink-0">
          <div className="flex items-center justify-end gap-2">
            {chart.stars > 0 && (
              <span className="flex items-center gap-1 px-2 py-0.5 text-xs border border-theme-border rounded">
                <Star className="w-3 h-3" />
                {chart.stars}
              </span>
            )}
          </div>
          {lastUpdated && (
            <p className="text-xs text-theme-text-tertiary mt-1">Updated {lastUpdated}</p>
          )}
          <p className="text-xs text-theme-text-secondary mt-0.5">Version {chart.version}</p>
        </div>
      </div>

      {/* Description */}
      {chart.description && (
        <p className="mt-3 text-xs text-theme-text-secondary line-clamp-2">
          {chart.description}
        </p>
      )}

      {/* Footer - badges row */}
      <div className="flex items-center justify-between mt-3 pt-3 border-t border-theme-border-light/50">
        {/* Keywords/category */}
        <div className="flex items-center gap-2">
          {chart.deprecated && (
            <span className="px-2 py-0.5 text-[10px] rounded bg-amber-500/20 text-amber-400 border border-amber-500/30">
              deprecated
            </span>
          )}
          {chart.keywords && chart.keywords.length > 0 && (
            <span className="px-2 py-0.5 text-[10px] rounded bg-theme-elevated text-theme-text-tertiary border border-theme-border-light truncate max-w-[150px]">
              {chart.keywords[0]}
            </span>
          )}
        </div>

        {/* Feature badges */}
        <div className="flex items-center gap-1">
          {/* Values Schema */}
          <Tooltip content={chart.hasValuesSchema ? 'Has values schema' : 'No values schema'}>
            <span className={clsx(
              'p-1.5 rounded',
              chart.hasValuesSchema ? 'bg-orange-500/20 text-orange-400' : 'bg-theme-elevated/50 text-theme-text-disabled'
            )}>
              <FileJson className="w-4 h-4" />
            </span>
          </Tooltip>

          {/* Signed */}
          <Tooltip content={chart.signed ? 'Signed package' : 'Not signed'}>
            <span className={clsx(
              'p-1.5 rounded',
              chart.signed ? 'bg-blue-500/20 text-blue-400' : 'bg-theme-elevated/50 text-theme-text-disabled'
            )}>
              <PenTool className="w-4 h-4" />
            </span>
          </Tooltip>

          {/* Verified Publisher */}
          <Tooltip content={chart.repository.verifiedPublisher ? 'Verified publisher' : 'Not verified'}>
            <span className={clsx(
              'p-1.5 rounded',
              chart.repository.verifiedPublisher ? 'bg-green-500/20 text-green-400' : 'bg-theme-elevated/50 text-theme-text-disabled'
            )}>
              <Shield className="w-4 h-4" />
            </span>
          </Tooltip>

          {/* Official */}
          <Tooltip content={chart.repository.official ? 'Official package' : 'Community package'}>
            <span className={clsx(
              'p-1.5 rounded',
              chart.repository.official ? 'bg-green-500/20 text-green-400' : 'bg-theme-elevated/50 text-theme-text-disabled'
            )}>
              <Star className={clsx('w-4 h-4', chart.repository.official && 'fill-current')} />
            </span>
          </Tooltip>
        </div>
      </div>
    </button>
  )
}
