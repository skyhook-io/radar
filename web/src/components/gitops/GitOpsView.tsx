import { useEffect, useMemo, useRef, useState, type ComponentType, type ReactNode } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { clsx } from 'clsx'
import { CheckCircle2, ChevronDown, ChevronRight, CircleAlert, CircleDot, Clock3, GitBranch, GitCommit, HeartPulse, LayoutGrid, List, Loader2, Pause, Play, RefreshCw, RotateCw, Search, Settings, Tag, Trash2, XCircle } from 'lucide-react'
import yaml from 'yaml'
import {
  GitOpsActivityInsightView,
  GitOpsChangesView,
  GitOpsIssuesBand,
  GitOpsTreeGraph,
  GitOpsStatusStrip,
  HealthStatusBadge,
  SyncStatusBadge,
  formatCompactAge,
  formatRelativeAgeTime,
  initNavigationMap,
  kindToPlural,
  type APIResource,
  type GitOpsResourceTree,
  type GitOpsInsightRef,
  type GitOpsTreeFilters,
  type GitOpsTreeRef,
  type GitOpsTreePreset,
  type SelectedResource,
} from '@skyhook-io/k8s-ui'
import {
  argoStatusToGitOpsStatus,
  fluxConditionsToGitOpsStatus,
  type FluxCondition,
  type GitOpsStatus,
} from '@skyhook-io/k8s-ui/types/gitops'
import { useToast } from '../ui/Toast'

import {
  fetchJSON,
  useApplyResource,
  useArgoRefresh,
  useArgoResume,
  useArgoRollback,
  useArgoSuspend,
  useArgoSync,
  useArgoTerminate,
  useFluxReconcile,
  useFluxResume,
  useFluxSuspend,
  useFluxSyncWithSource,
  useGitOpsInsights,
  useGitOpsTree,
  useResource,
} from '../../api/client'
import { useAPIResources } from '../../api/apiResources'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { useRegisterShortcut } from '../../hooks/useKeyboardShortcuts'
import { Tooltip } from '../ui/Tooltip'
import { CodeViewer } from '../ui/CodeViewer'
import { SyncOptionsDialog } from './SyncOptionsDialog'
import { RollbackDialog } from './RollbackDialog'
import type { GitOpsHistoryItem } from '@skyhook-io/k8s-ui'

const GITOPS_KINDS: APIResource[] = [
  { name: 'applications', kind: 'Application', group: 'argoproj.io', version: 'v1alpha1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'applicationsets', kind: 'ApplicationSet', group: 'argoproj.io', version: 'v1alpha1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'appprojects', kind: 'AppProject', group: 'argoproj.io', version: 'v1alpha1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'kustomizations', kind: 'Kustomization', group: 'kustomize.toolkit.fluxcd.io', version: 'v1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'helmreleases', kind: 'HelmRelease', group: 'helm.toolkit.fluxcd.io', version: 'v2', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'gitrepositories', kind: 'GitRepository', group: 'source.toolkit.fluxcd.io', version: 'v1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'ocirepositories', kind: 'OCIRepository', group: 'source.toolkit.fluxcd.io', version: 'v1beta2', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'helmrepositories', kind: 'HelmRepository', group: 'source.toolkit.fluxcd.io', version: 'v1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'alerts', kind: 'Alert', group: 'notification.toolkit.fluxcd.io', version: 'v1beta3', namespaced: true, verbs: ['list', 'get'], isCrd: true },
]

const KIND_BY_NAME = new Map(GITOPS_KINDS.map((k) => [k.name, k]))

interface ResourceCountsResponse {
  counts: Record<string, number>
  forbidden?: string[]
}

type GitOpsMode = 'applications' | 'sources' | 'projects' | 'alerts'
type GitOpsViewMode = 'table' | 'tiles'
type SortKey = 'name' | 'health' | 'sync' | 'lastSync' | 'project'

interface GitOpsRow {
  id: string
  mode: GitOpsMode
  tool: 'argo' | 'flux'
  kindName: string
  kind: string
  group: string
  name: string
  namespace: string
  project: string
  labels: Record<string, string>
  sync: string
  health: string
  suspended: boolean
  repository: string
  targetRevision: string
  path: string
  chart: string
  destination: string
  destinationNamespace: string
  createdAt: string
  lastSync: string
  autoSync: boolean
  // True when metadata.deletionTimestamp is set. Drives the small
  // [Terminating] indicator on the row + on the detail page header,
  // so users can spot zombie resources without having to drill in.
  terminating: boolean
  // RFC3339 timestamp from metadata.deletionTimestamp. Used in the
  // fleet's Last Sync column to render "Pending {N}{unit} ago" instead of the
  // stale last-reconcile time when the row is Terminating.
  terminationStartedAt?: string
  raw: any
}

interface GitOpsViewProps {
  namespaces: string[]
  onOpenResource: (resource: SelectedResource) => void
}

export function GitOpsView({ namespaces, onOpenResource }: GitOpsViewProps) {
  const location = useLocation()
  if (location.pathname.startsWith('/gitops/detail/')) {
    return <GitOpsDetailView namespaces={namespaces} onOpenResource={onOpenResource} />
  }
  return <GitOpsTableView namespaces={namespaces} />
}

function GitOpsTableView({ namespaces }: { namespaces: string[] }) {
  const navigate = useNavigate()
  const searchInputRef = useRef<HTMLInputElement>(null)
  const namespacesParam = namespaces.join(',')
  const { data: apiResources, isLoading: apiResourcesLoading } = useAPIResources()

  useEffect(() => {
    initNavigationMap([...(apiResources ?? []), ...GITOPS_KINDS])
  }, [apiResources])

  const [mode, setMode] = useState<GitOpsMode>('applications')
  const [viewMode, setViewMode] = useState<GitOpsViewMode>('table')
  const [search, setSearch] = useState('')
  const [syncFilters, setSyncFilters] = useState<Set<string>>(new Set())
  const [healthFilters, setHealthFilters] = useState<Set<string>>(new Set())
  const [projectFilters, setProjectFilters] = useState<Set<string>>(new Set())
  const [namespaceFilters, setNamespaceFilters] = useState<Set<string>>(new Set())
  const [labelFilters, setLabelFilters] = useState<Set<string>>(new Set())
  const [showLabelsDropdown, setShowLabelsDropdown] = useState(false)
  const [labelSearch, setLabelSearch] = useState('')
  const [automationFilter, setAutomationFilter] = useState<'all' | 'auto' | 'manual' | 'suspended'>('all')
  // Lifecycle filter: surface zombies (terminating but stuck) and let the
  // user filter them in/out. Default is 'all' so the fleet doesn't hide
  // problem resources by accident; 'terminating' focuses to investigate
  // stuck cleanups; 'active' hides them when the user wants to ignore
  // resources that are on their way out.
  const [lifecycleFilter, setLifecycleFilter] = useState<'all' | 'terminating' | 'active'>('all')
  const [sortKey, setSortKey] = useState<SortKey>('health')

  useRegisterShortcut({
    id: 'gitops-focus-search',
    keys: '/',
    category: 'GitOps',
    description: 'Focus GitOps search',
    scope: 'gitops',
    handler: (event) => {
      event.preventDefault()
      searchInputRef.current?.focus()
    },
    allowInInputs: false,
  })

  const countsQuery = useQuery({
    queryKey: ['gitops-resource-counts', namespacesParam],
    queryFn: async () => {
      const params = new URLSearchParams()
      if (namespaces.length > 0) params.set('namespaces', namespacesParam)
      return fetchJSON<ResourceCountsResponse>(`/resource-counts?${params}`)
    },
    staleTime: 10_000,
    refetchInterval: 60_000,
  })

  const applicationQuery = useQuery({
    queryKey: ['gitops-applications-main', namespaces, apiResources?.length ?? 0],
    queryFn: async () => {
      const hasApplications = hasAPIResource(apiResources, 'applications', 'argoproj.io')
      const hasKustomizations = hasAPIResource(apiResources, 'kustomizations', 'kustomize.toolkit.fluxcd.io')
      const hasHelmReleases = hasAPIResource(apiResources, 'helmreleases', 'helm.toolkit.fluxcd.io')
      // Flux source CRs carry the actual URL. Reconcilers (Kustomization,
      // HelmRelease) only reference the source by name. We list sources
      // alongside the reconcilers and build one lookup map so the fleet's
      // Source column can render the URL (e.g. github.com/owner/repo)
      // instead of the opaque CR name (e.g. "GitRepository podinfo").
      // Listing the sources cluster-wide is cheap — they're cached by the
      // dynamic informer and there are few per cluster — but skip the
      // request entirely when no Flux CRDs are installed.
      const hasFluxSources = hasKustomizations || hasHelmReleases
      const hasGitRepos = hasFluxSources && hasAPIResource(apiResources, 'gitrepositories', 'source.toolkit.fluxcd.io')
      const hasHelmRepos = hasFluxSources && hasAPIResource(apiResources, 'helmrepositories', 'source.toolkit.fluxcd.io')
      const hasOCIRepos = hasFluxSources && hasAPIResource(apiResources, 'ocirepositories', 'source.toolkit.fluxcd.io')
      const hasBuckets = hasFluxSources && hasAPIResource(apiResources, 'buckets', 'source.toolkit.fluxcd.io')
      const [applications, kustomizations, helmReleases, gitRepos, helmRepos, ociRepos, buckets] = await Promise.all([
        hasApplications ? fetchResourceList('applications', 'argoproj.io', namespacesParam) : Promise.resolve([]),
        hasKustomizations ? fetchResourceList('kustomizations', 'kustomize.toolkit.fluxcd.io', namespacesParam) : Promise.resolve([]),
        hasHelmReleases ? fetchResourceList('helmreleases', 'helm.toolkit.fluxcd.io', namespacesParam) : Promise.resolve([]),
        hasGitRepos ? fetchResourceList('gitrepositories', 'source.toolkit.fluxcd.io', '') : Promise.resolve([]),
        hasHelmRepos ? fetchResourceList('helmrepositories', 'source.toolkit.fluxcd.io', '') : Promise.resolve([]),
        hasOCIRepos ? fetchResourceList('ocirepositories', 'source.toolkit.fluxcd.io', '') : Promise.resolve([]),
        hasBuckets ? fetchResourceList('buckets', 'source.toolkit.fluxcd.io', '') : Promise.resolve([]),
      ])
      const fluxSourceUrls = buildFluxSourceUrlMap([...gitRepos, ...helmRepos, ...ociRepos, ...buckets])
      return [
        ...applications.map((r) => normalizeArgoApplication(r)),
        ...kustomizations.map((r) => normalizeFluxKustomization(r, fluxSourceUrls)),
        ...helmReleases.map((r) => normalizeFluxHelmRelease(r, fluxSourceUrls)),
      ]
    },
    enabled: !apiResourcesLoading,
    staleTime: 30_000,
    refetchInterval: 120_000,
  })

  const gitopsCounts = useMemo(() => {
    const counts = countsQuery.data?.counts ?? {}
    const out: Record<string, number> = {}
    for (const k of GITOPS_KINDS) {
      out[k.group ? `${k.group}/${k.kind}` : k.name] = counts[`${k.group}/${k.kind}`] ?? counts[k.name] ?? 0
    }
    return out
  }, [countsQuery.data])

  const totalGitOps = Object.values(gitopsCounts).reduce((sum, n) => sum + n, 0)
  const allRows = applicationQuery.data ?? []
  const statusSummary = summarizeGitOpsRows(allRows)

  const modeCounts = {
    applications: allRows.length,
    sources: (gitopsCounts['source.toolkit.fluxcd.io/GitRepository'] ?? 0) + (gitopsCounts['source.toolkit.fluxcd.io/OCIRepository'] ?? 0) + (gitopsCounts['source.toolkit.fluxcd.io/HelmRepository'] ?? 0),
    projects: gitopsCounts['argoproj.io/AppProject'] ?? 0,
    alerts: gitopsCounts['notification.toolkit.fluxcd.io/Alert'] ?? 0,
  }

  const projects = useMemo(() => countValues(allRows.map((row) => row.project).filter(Boolean)), [allRows])
  const rowNamespaces = useMemo(() => countValues(allRows.map((row) => row.namespace || '(cluster)').filter(Boolean)), [allRows])
  const syncCounts = useMemo(() => countMap(allRows.map((row) => row.sync)), [allRows])
  const healthCounts = useMemo(() => countMap(allRows.map((row) => row.health)), [allRows])
  const labels = useMemo(() => countLabels(allRows), [allRows])
  const filteredRows = useMemo(() => {
    const q = search.trim().toLowerCase()
    const activeLabels = [...labelFilters].map((pair) => {
      const [key, ...rest] = pair.split('=')
      return { key, value: rest.join('=') }
    }).filter((label) => label.key && label.value)
    const rows = allRows.filter((row) => {
      if (mode !== 'applications') return false
      if (q && ![
        row.name,
        row.namespace,
        row.project,
        row.repository,
        row.path,
        row.chart,
        row.destination,
        row.targetRevision,
        row.kind,
      ].some((value) => value.toLowerCase().includes(q))) return false
      if (syncFilters.size > 0 && !syncFilters.has(row.sync)) return false
      if (healthFilters.size > 0 && !healthFilters.has(row.health)) return false
      if (projectFilters.size > 0 && !projectFilters.has(row.project || '(none)')) return false
      if (namespaceFilters.size > 0 && !namespaceFilters.has(row.namespace || '(cluster)')) return false
      if (activeLabels.length > 0 && !activeLabels.every(({ key, value }) => row.labels[key] === value)) return false
      if (automationFilter === 'auto' && !row.autoSync) return false
      if (automationFilter === 'manual' && row.autoSync) return false
      if (automationFilter === 'suspended' && !row.suspended) return false
      if (lifecycleFilter === 'terminating' && !row.terminating) return false
      if (lifecycleFilter === 'active' && row.terminating) return false
      return true
    })
    return [...rows].sort((a, b) => compareRows(a, b, sortKey))
  }, [allRows, automationFilter, healthFilters, labelFilters, lifecycleFilter, mode, namespaceFilters, projectFilters, search, sortKey, syncFilters])

  const terminatingCount = useMemo(() => allRows.filter((row) => row.terminating).length, [allRows])

  function openRow(row: GitOpsRow) {
    const ns = row.namespace || '_'
    const params = new URLSearchParams()
    params.set('apiGroup', row.group)
    navigate({ pathname: gitOpsDetailPath(row.kindName, ns, row.name), search: params.toString() })
  }

  function refetch() {
    applicationQuery.refetch()
  }

  const isInitialLoading = apiResourcesLoading || countsQuery.isLoading || applicationQuery.isLoading

  if (totalGitOps === 0 && applicationQuery.isFetched && countsQuery.isFetched && !isInitialLoading) {
    return (
      <div className="flex h-full min-h-0 flex-1 items-center justify-center bg-theme-base p-4">
        <div className="rounded-lg border border-theme-border bg-theme-surface p-8 text-center">
          <GitBranch className="mx-auto h-8 w-8 text-theme-text-tertiary" />
          <h2 className="mt-3 text-base font-semibold text-theme-text-primary">No GitOps resources detected</h2>
          <p className="mt-1 text-sm text-theme-text-secondary">
            Radar did not find ArgoCD Applications or FluxCD resources in this cluster.
          </p>
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-full min-w-0 flex-1 overflow-hidden bg-theme-base max-lg:flex-col">
      <GitOpsFilterSidebar
        mode={mode}
        onModeChange={setMode}
        modeCounts={modeCounts}
        syncCounts={syncCounts}
        syncFilters={syncFilters}
        onToggleSync={(value) => toggleSet(syncFilters, setSyncFilters, value)}
        healthCounts={healthCounts}
        healthFilters={healthFilters}
        onToggleHealth={(value) => toggleSet(healthFilters, setHealthFilters, value)}
        automationFilter={automationFilter}
        onAutomationFilterChange={setAutomationFilter}
        lifecycleFilter={lifecycleFilter}
        onLifecycleFilterChange={setLifecycleFilter}
        terminatingCount={terminatingCount}
        projects={projects}
        projectFilters={projectFilters}
        onToggleProject={(value) => toggleSet(projectFilters, setProjectFilters, value)}
        namespaces={rowNamespaces}
        namespaceFilters={namespaceFilters}
        onToggleNamespace={(value) => toggleSet(namespaceFilters, setNamespaceFilters, value)}
        onClear={() => {
          setSearch('')
          setSyncFilters(new Set())
          setHealthFilters(new Set())
          setProjectFilters(new Set())
          setNamespaceFilters(new Set())
          setLabelFilters(new Set())
          setAutomationFilter('all')
          setLifecycleFilter('all')
        }}
      />

      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        <div className="shrink-0 border-b border-theme-border bg-theme-base px-4 py-3">
          <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between">
            <div className="min-w-0">
              <h1 className="text-lg font-semibold text-theme-text-primary">GitOps</h1>
              <p className="truncate text-sm text-theme-text-secondary">
                Applications and reconciliations with source, destination, sync, and health state.
              </p>
            </div>
            <div className="flex shrink-0 flex-wrap justify-end gap-2">
              <SummaryTile label="Applications" value={allRows.length} />
              <SummaryTile label="Out of sync" value={statusSummary.outOfSync} tone="warning" />
              <SummaryTile label="Degraded" value={statusSummary.degraded} tone="error" />
              <SummaryTile label="Suspended" value={statusSummary.suspended} tone="warning" />
              <SummaryTile label="Reconciling" value={statusSummary.reconciling} tone="info" />
            </div>
          </div>
        </div>

        <div className="shrink-0 border-b border-theme-border bg-theme-surface/70 px-4 py-3">
          <StatusDistribution rows={filteredRows} />
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <div className="relative w-full max-w-md">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-theme-text-tertiary" />
              <input
                ref={searchInputRef}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search applications, repos, paths..."
                className="h-8 w-full rounded-md border border-theme-border bg-theme-base pl-8 pr-3 text-sm text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:ring-1 focus:ring-blue-500/50"
              />
            </div>
            {/* Surface the filter denominator so users know whether they're
                seeing all rows, a search-narrowed slice, or a sidebar-filtered
                slice. The KPI tiles count the unfiltered universe; this caption
                counts the visible result set. */}
            {filteredRows.length !== allRows.length && (
              <span className="text-[11px] text-theme-text-tertiary">
                Showing {filteredRows.length} of {allRows.length}
              </span>
            )}
            <select
              value={sortKey}
              onChange={(e) => setSortKey(e.target.value as SortKey)}
              className="h-8 rounded-md border border-theme-border bg-theme-base px-2 text-xs text-theme-text-primary focus:outline-none focus:ring-1 focus:ring-blue-500/50"
            >
              <option value="health">Sort: health</option>
              <option value="sync">Sort: sync</option>
              <option value="lastSync">Sort: last sync</option>
              <option value="project">Sort: project</option>
              <option value="name">Sort: name</option>
            </select>
            {labels.length > 0 && (
              <LabelsDropdown
                labels={labels}
                activeLabels={labelFilters}
                onToggle={(value) => toggleSet(labelFilters, setLabelFilters, value)}
                onClear={() => setLabelFilters(new Set())}
                open={showLabelsDropdown}
                onOpenChange={(open) => {
                  setShowLabelsDropdown(open)
                  if (open) setLabelSearch('')
                }}
                search={labelSearch}
                onSearchChange={setLabelSearch}
              />
            )}
            <div className="flex overflow-hidden rounded-md border border-theme-border">
              <IconToggle active={viewMode === 'table'} label="Table" icon={List} onClick={() => setViewMode('table')} />
              <IconToggle active={viewMode === 'tiles'} label="Tiles" icon={LayoutGrid} onClick={() => setViewMode('tiles')} />
            </div>
            <Tooltip content="Refresh GitOps resources">
              <button
                type="button"
                onClick={refetch}
                className="inline-flex h-8 w-8 items-center justify-center rounded-md border border-theme-border bg-theme-base text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
              >
                <RefreshCw className={`h-3.5 w-3.5 ${applicationQuery.isFetching ? 'animate-spin' : ''}`} />
              </button>
            </Tooltip>
          </div>
        </div>

        <div className="min-h-0 min-w-0 flex-1 overflow-auto bg-theme-base">
          {mode !== 'applications' ? (
            <div className="flex h-full items-center justify-center text-sm text-theme-text-secondary">
              {modeLabel(mode)} view is queued behind the application list.
            </div>
          ) : applicationQuery.isLoading ? (
            <div className="flex h-full items-center justify-center text-sm text-theme-text-secondary">
              <Loader2 className="mr-2 h-4 w-4 animate-spin" /> Loading GitOps applications...
            </div>
          ) : applicationQuery.error ? (
            <div className="p-4 text-sm text-red-500">Failed to load GitOps applications: {(applicationQuery.error as Error).message}</div>
          ) : filteredRows.length === 0 ? (
            <div className="flex h-full items-center justify-center text-sm text-theme-text-secondary">
              No applications match the current filters.
            </div>
          ) : viewMode === 'tiles' ? (
            <GitOpsTiles rows={filteredRows} onOpen={openRow} />
          ) : (
            <GitOpsTable rows={filteredRows} onOpen={openRow} />
          )}
        </div>
      </div>
    </div>
  )
}

function GitOpsFilterSidebar({
  mode,
  onModeChange,
  modeCounts,
  syncCounts,
  syncFilters,
  onToggleSync,
  healthCounts,
  healthFilters,
  onToggleHealth,
  automationFilter,
  onAutomationFilterChange,
  lifecycleFilter,
  onLifecycleFilterChange,
  terminatingCount,
  projects,
  projectFilters,
  onToggleProject,
  namespaces,
  namespaceFilters,
  onToggleNamespace,
  onClear,
}: {
  mode: GitOpsMode
  onModeChange: (mode: GitOpsMode) => void
  modeCounts: Record<GitOpsMode, number>
  syncCounts: Map<string, number>
  syncFilters: Set<string>
  onToggleSync: (value: string) => void
  healthCounts: Map<string, number>
  healthFilters: Set<string>
  onToggleHealth: (value: string) => void
  automationFilter: 'all' | 'auto' | 'manual' | 'suspended'
  onAutomationFilterChange: (value: 'all' | 'auto' | 'manual' | 'suspended') => void
  lifecycleFilter: 'all' | 'terminating' | 'active'
  onLifecycleFilterChange: (value: 'all' | 'terminating' | 'active') => void
  terminatingCount: number
  projects: Array<{ name: string; count: number }>
  projectFilters: Set<string>
  onToggleProject: (value: string) => void
  namespaces: Array<{ name: string; count: number }>
  namespaceFilters: Set<string>
  onToggleNamespace: (value: string) => void
  onClear: () => void
}) {
  return (
    <aside className="flex w-72 shrink-0 flex-col overflow-hidden border-r border-theme-border bg-theme-surface/90 max-lg:max-h-72 max-lg:w-full max-lg:border-b max-lg:border-r-0">
      <div className="flex items-center justify-between border-b border-theme-border px-3 py-2">
        <span className="text-sm font-medium text-theme-text-secondary">GitOps Filters</span>
        <button type="button" onClick={onClear} className="text-[10px] font-medium text-blue-500 hover:text-blue-400">
          Clear
        </button>
      </div>
      <div className="flex-1 overflow-y-auto">
        {/* Sources/Projects/Alerts modes are placeholder surfaces that route to
            a "queued behind the application list" pane — confusing to expose
            in the primary nav while they're not built. Restore here once the
            corresponding views ship. */}
        <FilterSection icon={GitBranch} title="Scope">
          <div className="grid grid-cols-2 gap-1">
            {(['applications'] as GitOpsMode[]).map((item) => (
              <button
                key={item}
                type="button"
                onClick={() => onModeChange(item)}
                className={`rounded-md px-2 py-1.5 text-left text-[11px] transition-colors ${
                  mode === item
                    ? 'bg-skyhook-500 text-white'
                    : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
                }`}
              >
                <div className="font-medium">{modeLabel(item)}</div>
                <div className={mode === item ? 'text-white/70' : 'text-theme-text-tertiary'}>{modeCounts[item]}</div>
              </button>
            ))}
          </div>
        </FilterSection>

        <FilterSection icon={CheckCircle2} title="Sync">
          <FacetButton label="Synced" count={syncCounts.get('Synced') ?? 0} active={syncFilters.has('Synced')} tone="success" onClick={() => onToggleSync('Synced')} />
          <FacetButton label="OutOfSync" count={syncCounts.get('OutOfSync') ?? 0} active={syncFilters.has('OutOfSync')} tone="warning" onClick={() => onToggleSync('OutOfSync')} />
          <FacetButton label="Reconciling" count={syncCounts.get('Reconciling') ?? 0} active={syncFilters.has('Reconciling')} tone="info" onClick={() => onToggleSync('Reconciling')} />
          <FacetButton label="Unknown" count={syncCounts.get('Unknown') ?? 0} active={syncFilters.has('Unknown')} onClick={() => onToggleSync('Unknown')} />
        </FilterSection>

        <FilterSection icon={HeartPulse} title="Health">
          <FacetButton label="Healthy" count={healthCounts.get('Healthy') ?? 0} active={healthFilters.has('Healthy')} tone="success" onClick={() => onToggleHealth('Healthy')} />
          <FacetButton label="Progressing" count={healthCounts.get('Progressing') ?? 0} active={healthFilters.has('Progressing')} tone="info" onClick={() => onToggleHealth('Progressing')} />
          <FacetButton label="Degraded" count={healthCounts.get('Degraded') ?? 0} active={healthFilters.has('Degraded')} tone="error" onClick={() => onToggleHealth('Degraded')} />
          <FacetButton label="Suspended" count={healthCounts.get('Suspended') ?? 0} active={healthFilters.has('Suspended')} tone="warning" onClick={() => onToggleHealth('Suspended')} />
          <FacetButton label="Unknown" count={healthCounts.get('Unknown') ?? 0} active={healthFilters.has('Unknown')} onClick={() => onToggleHealth('Unknown')} />
        </FilterSection>

        <FilterSection icon={CircleDot} title="Automation">
          <div className="grid grid-cols-2 gap-1">
            {([
              ['all', 'All'],
              ['auto', 'Auto-sync'],
              ['manual', 'Manual'],
              ['suspended', 'Suspended'],
            ] as const).map(([value, label]) => (
              <button
                key={value}
                type="button"
                onClick={() => onAutomationFilterChange(value)}
                className={`rounded-md px-2 py-1.5 text-[11px] font-medium transition-colors ${
                  automationFilter === value
                    ? 'bg-skyhook-500 text-white'
                    : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
                }`}
              >
                {label}
              </button>
            ))}
          </div>
        </FilterSection>

        {terminatingCount > 0 && (
          <FilterSection icon={Trash2} title="Lifecycle">
            <div className="grid grid-cols-3 gap-1">
              {([
                ['all', 'All'],
                ['active', 'Active'],
                ['terminating', `Terminating (${terminatingCount})`],
              ] as const).map(([value, label]) => (
                <button
                  key={value}
                  type="button"
                  onClick={() => onLifecycleFilterChange(value)}
                  className={`rounded-md px-2 py-1.5 text-[11px] font-medium transition-colors ${
                    lifecycleFilter === value
                      ? value === 'terminating'
                        // Distinct tone for the Terminating mode — orange
                        // mirrors the [Term] chip + insight Issue color, so
                        // the active state visually links to its consequence.
                        ? 'bg-orange-500 text-white'
                        : 'bg-skyhook-500 text-white'
                      : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
                  }`}
                >
                  {label}
                </button>
              ))}
            </div>
          </FilterSection>
        )}

        <FilterSection icon={CircleAlert} title="Projects">
          {projects.slice(0, 10).map((project) => (
            <FacetButton
              key={project.name}
              label={project.name || '(none)'}
              count={project.count}
              active={projectFilters.has(project.name || '(none)')}
              onClick={() => onToggleProject(project.name || '(none)')}
            />
          ))}
        </FilterSection>

        <FilterSection icon={List} title="Namespaces">
          {namespaces.slice(0, 12).map((namespace) => (
            <FacetButton
              key={namespace.name}
              label={namespace.name}
              count={namespace.count}
              active={namespaceFilters.has(namespace.name)}
              onClick={() => onToggleNamespace(namespace.name)}
            />
          ))}
        </FilterSection>
      </div>
    </aside>
  )
}

function FilterSection({ icon: Icon, title, children }: { icon: ComponentType<{ className?: string }>; title: string; children: ReactNode }) {
  return (
    <section className="border-b border-theme-border px-3 py-2">
      <div className="mb-1.5 flex items-center gap-2">
        <Icon className="h-3.5 w-3.5 text-theme-text-tertiary" />
        <span className="text-[10px] font-medium uppercase tracking-wider text-theme-text-tertiary">{title}</span>
      </div>
      <div className="space-y-0.5">{children}</div>
    </section>
  )
}

function FacetButton({
  label,
  count,
  active,
  tone = 'neutral',
  onClick,
}: {
  label: string
  count: number
  active: boolean
  tone?: 'neutral' | 'success' | 'warning' | 'error' | 'info'
  onClick: () => void
}) {
  const dot = {
    neutral: 'bg-theme-text-tertiary',
    success: 'bg-emerald-500',
    warning: 'bg-amber-500',
    error: 'bg-red-500',
    info: 'bg-sky-500',
  }[tone]
  return (
    <button
      type="button"
      onClick={onClick}
      className={`flex w-full items-center gap-2 rounded px-2 py-1 text-left text-[11px] transition-colors ${
        active ? 'bg-blue-500/15 text-blue-500' : 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
      }`}
    >
      <span className={`h-2 w-2 shrink-0 rounded-full ${dot}`} />
      <span className="min-w-0 flex-1 truncate font-medium">{label}</span>
      {count > 0 && <span className="tabular-nums text-theme-text-tertiary">{count}</span>}
    </button>
  )
}

function IconToggle({ active, label, icon: Icon, onClick }: { active: boolean; label: string; icon: ComponentType<{ className?: string }>; onClick: () => void }) {
  return (
    <Tooltip content={label}>
      <button
        type="button"
        onClick={onClick}
        className={`inline-flex h-8 w-8 items-center justify-center transition-colors ${
          active ? 'bg-skyhook-500 text-white' : 'bg-theme-base text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
        }`}
      >
        <Icon className="h-3.5 w-3.5" />
      </button>
    </Tooltip>
  )
}

function LabelsDropdown({
  labels,
  activeLabels,
  onToggle,
  onClear,
  open,
  onOpenChange,
  search,
  onSearchChange,
}: {
  labels: Array<{ name: string; count: number }>
  activeLabels: Set<string>
  onToggle: (value: string) => void
  onClear: () => void
  open: boolean
  onOpenChange: (open: boolean) => void
  search: string
  onSearchChange: (value: string) => void
}) {
  const filtered = search.trim()
    ? labels.filter((label) => label.name.toLowerCase().includes(search.trim().toLowerCase()))
    : labels
  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => onOpenChange(!open)}
        className={`inline-flex h-8 items-center gap-1.5 rounded-md border px-2.5 text-xs transition-colors ${
          activeLabels.size > 0
            ? 'border-emerald-500/40 bg-emerald-500/15 text-emerald-600 dark:text-emerald-300'
            : 'border-theme-border bg-theme-base text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
        }`}
      >
        <Tag className="h-3.5 w-3.5" />
        Labels
        {activeLabels.size > 0 && (
          <span className="rounded bg-emerald-500/20 px-1 text-[10px] tabular-nums">{activeLabels.size}</span>
        )}
      </button>
      {open && (
        <div className="absolute right-0 top-full z-50 mt-1 w-80 overflow-hidden rounded-lg border border-theme-border bg-theme-surface shadow-xl">
          <div className="border-b border-theme-border p-2">
            <div className="mb-2 text-xs text-theme-text-secondary">
              Selected labels are combined with <span className="font-semibold text-theme-text-primary">AND</span>.
            </div>
            <div className="flex items-center gap-2">
              <div className="relative flex-1">
                <Search className="pointer-events-none absolute left-2 top-1/2 h-3 w-3 -translate-y-1/2 text-theme-text-tertiary" />
                <input
                  type="text"
                  value={search}
                  onChange={(e) => onSearchChange(e.target.value)}
                  placeholder="Search labels..."
                  autoFocus
                  className="h-7 w-full rounded border border-theme-border bg-theme-elevated pl-7 pr-2 text-xs text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:ring-1 focus:ring-blue-500/50"
                />
              </div>
              {activeLabels.size > 0 && (
                <button
                  type="button"
                  onClick={() => {
                    onClear()
                    onOpenChange(false)
                  }}
                  className="shrink-0 rounded px-1 py-0.5 text-xs text-theme-text-tertiary hover:text-theme-text-primary"
                >
                  Clear
                </button>
              )}
            </div>
          </div>
          <div className="max-h-72 overflow-y-auto py-1">
            {filtered.map((label) => {
              const active = activeLabels.has(label.name)
              return (
                <button
                  key={label.name}
                  type="button"
                  onClick={() => onToggle(label.name)}
                  className={`flex w-full items-center justify-between gap-2 px-3 py-1.5 text-left text-xs transition-colors ${
                    active
                      ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-300'
                      : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
                  }`}
                >
                  <Tooltip content={label.name} delay={400} wrapperClassName="min-w-0 flex-1">
                    <span className="block w-full truncate">{label.name}</span>
                  </Tooltip>
                  <span className="shrink-0 tabular-nums text-theme-text-tertiary">({label.count})</span>
                </button>
              )
            })}
            {filtered.length === 0 && (
              <div className="px-3 py-2 text-xs text-theme-text-tertiary">No labels match.</div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function StatusDistribution({ rows }: { rows: GitOpsRow[] }) {
  const summary = summarizeGitOpsRows(rows)
  const total = rows.length || 1
  const segments = [
    { key: 'healthy', value: summary.healthy, className: 'bg-emerald-500' },
    { key: 'progressing', value: summary.progressing, className: 'bg-sky-500' },
    { key: 'degraded', value: summary.degraded, className: 'bg-red-500' },
    { key: 'outOfSync', value: summary.outOfSync, className: 'bg-amber-500' },
    { key: 'unknown', value: Math.max(0, rows.length - summary.healthy - summary.progressing - summary.degraded), className: 'bg-theme-text-tertiary/40' },
  ].filter((segment) => segment.value > 0)
  return (
    <div className="h-2 overflow-hidden rounded-full bg-theme-elevated">
      <div className="flex h-full w-full">
        {segments.map((segment) => (
          <div
            key={segment.key}
            className={segment.className}
            style={{ width: `${Math.max(1, (segment.value / total) * 100)}%` }}
          />
        ))}
      </div>
    </div>
  )
}

function GitOpsTable({ rows, onOpen }: { rows: GitOpsRow[]; onOpen: (row: GitOpsRow) => void }) {
  return (
    <table className="w-full min-w-[1040px] table-fixed border-separate border-spacing-0 text-sm">
      <thead className="sticky top-0 z-10 bg-theme-surface">
        <tr className="text-left text-[11px] uppercase tracking-wide text-theme-text-tertiary">
          <TableHead className="w-[24%]">Application</TableHead>
          <TableHead className="w-[9%]">Project</TableHead>
          <TableHead className="w-[9%]">Sync</TableHead>
          <TableHead className="w-[9%]">Health</TableHead>
          <TableHead className="w-[22%]">Source</TableHead>
          <TableHead className="w-[15%]">Destination</TableHead>
          <TableHead className="w-[12%]">Last Sync</TableHead>
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <tr
            key={row.id}
            onClick={() => onOpen(row)}
            // Subtle row-level fade for Terminating to reinforce the
            // "this is on its way out" reading; the orange status stripe
            // + chip are the primary lifecycle indicators, this is just
            // weight tuning so a row of 5 zombies doesn't shout the same
            // visual weight as 5 active applications.
            className={clsx(
              'cursor-pointer border-b border-theme-border bg-theme-base hover:bg-theme-hover',
              row.terminating && 'opacity-70',
            )}
          >
            <TableCell>
              <div className="flex min-w-0 items-center gap-2">
                <span className={`h-8 w-1 shrink-0 rounded-full ${statusStripe(row)}`} />
                {/* Terminating chip moves to the leftmost slot (before the
                    name) so it's the first thing the eye lands on when
                    scanning. Previously it sat after the name where it
                    competed with status badges for attention. */}
                {row.terminating && (
                  <Tooltip content="Pending deletion — finalizers still running">
                    <span className="inline-flex shrink-0 items-center gap-1 rounded border border-orange-500/40 bg-orange-500/15 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-orange-400">
                      <Trash2 className="h-3 w-3" />
                      Terminating
                    </span>
                  </Tooltip>
                )}
                <div className="min-w-0">
                  <div className="truncate font-medium text-theme-text-primary">{row.name}</div>
                  <div className="truncate text-xs text-theme-text-tertiary">{row.tool === 'argo' ? 'ArgoCD' : 'FluxCD'} {row.kind}</div>
                </div>
              </div>
            </TableCell>
            <TableCell>{row.project || '-'}</TableCell>
            {/* Sync / Health cells: when row is Terminating, the controller
                isn't reconciling and the badges reflect frozen pre-deletion
                state. Replace with a muted dash so the row reads as "no
                live status — see Terminating chip for the actual state". */}
            <TableCell>
              {row.terminating
                ? <span className="text-[11px] text-theme-text-tertiary">—</span>
                : <SyncStatusBadge sync={row.sync as any} suspended={row.suspended} />}
            </TableCell>
            <TableCell>
              {row.terminating
                ? <span className="text-[11px] text-theme-text-tertiary">—</span>
                : <HealthStatusBadge health={row.health as any} />}
            </TableCell>
            <TableCell>
              <div className="truncate text-theme-text-primary">{row.repository || row.chart || '-'}</div>
              <div className="truncate text-xs text-theme-text-tertiary">{[row.targetRevision, row.path || row.chart].filter(Boolean).join(' · ') || '-'}</div>
            </TableCell>
            <TableCell>
              <div className="truncate text-theme-text-primary">{row.destination || '-'}</div>
              <div className="truncate text-xs text-theme-text-tertiary">{row.destinationNamespace || row.namespace || '-'}</div>
            </TableCell>
            {/* Last Sync column: for Terminating rows, "33d ago" is stale.
                Show the deletion-pending duration instead, so the time
                column answers the *current* operational question. */}
            <TableCell>
              {row.terminating
                ? <span className="text-orange-400/80">Pending {formatRelative(row.terminationStartedAt ?? '') || 'now'}</span>
                : formatRelative(row.lastSync || row.createdAt)}
            </TableCell>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function GitOpsTiles({ rows, onOpen }: { rows: GitOpsRow[]; onOpen: (row: GitOpsRow) => void }) {
  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(300px,1fr))] gap-3 p-4">
      {rows.map((row) => (
        <GitOpsTile key={row.id} row={row} onOpen={onOpen} />
      ))}
    </div>
  )
}

// Tier hierarchy: name (primary scan target) > sync/health badges > source +
// revision + recency (operational answers) > cluster + namespace + project
// (footer metadata). Critically: never truncate the name. Spacing rhythm 4/8/12
// to make hierarchy felt, not just sized.
function GitOpsTile({ row, onOpen }: { row: GitOpsRow; onOpen: (row: GitOpsRow) => void }) {
  const source = compactRepoSource(row.repository || row.chart, row.path || row.chart)
  const revision = row.targetRevision || ''
  const lastSyncRaw = row.lastSync || row.createdAt
  const recencyClass = recencyTone(lastSyncRaw)
  const dest = row.destination ? compactClusterURL(row.destination) : ''
  const ns = row.destinationNamespace || row.namespace
  return (
    <button
      type="button"
      onClick={() => onOpen(row)}
      className={clsx(
        'group relative flex min-w-0 flex-col overflow-hidden rounded-md border border-theme-border bg-theme-surface text-left shadow-theme-sm transition-all hover:border-theme-text-tertiary/40 hover:shadow-theme-md',
        row.terminating && 'opacity-80',
      )}
    >
      {/* Top accent strip — sync-state color, sole color above the badge row */}
      <div className={clsx('h-1 w-full', statusStripe(row))} />
      <div className="flex flex-1 flex-col gap-3 px-4 pb-4 pt-3">
        {/* Tier 1 — name. Wrap up to 2 lines, then break-words to avoid clipping. */}
        <div className="line-clamp-2 break-all text-[15px] font-semibold leading-tight text-theme-text-primary">
          {row.name}
        </div>
        {/* Tier 2 — lifecycle dominates when Terminating; otherwise sync + health.
            Suppressing sync/health for Terminating tiles avoids the same
            stale-state contradiction we removed from the detail title row. */}
        <div className="flex flex-wrap gap-1.5">
          {row.terminating ? (
            <span className="badge border border-orange-500/40 bg-orange-500/15 text-orange-400" title="Pending deletion — finalizers still running">
              <Trash2 className="h-3 w-3" />
              Terminating
            </span>
          ) : (
            <>
              <SyncStatusBadge sync={row.sync as any} suspended={row.suspended} />
              <HealthStatusBadge health={row.health as any} />
            </>
          )}
        </div>
        {/* Tier 3 — source / revision / recency. The operational answers. */}
        <div className="flex flex-col gap-1 text-[12px]">
          {source && (
            <div className="truncate text-theme-text-secondary">{source}</div>
          )}
          {revision && (
            <div className="truncate font-mono text-[11px] text-theme-text-tertiary">{shortRevision(revision)}</div>
          )}
          {row.terminating ? (
            <div className="font-medium text-orange-400/80">Pending {formatRelative(row.terminationStartedAt ?? '') || 'now'}</div>
          ) : (
            lastSyncRaw && <div className={clsx('font-medium', recencyClass)}>{formatRelative(lastSyncRaw)}</div>
          )}
        </div>
        {/* Tier 4 — footer chips. Quiet, but reachable. */}
        {(dest || ns || row.project) && (
          <div className="mt-auto flex flex-wrap items-center gap-x-1.5 gap-y-1 border-t border-theme-border/60 pt-3 text-[11px] text-theme-text-tertiary">
            {dest && <span className="truncate" title={row.destination}>{dest}</span>}
            {dest && ns && <span aria-hidden>·</span>}
            {ns && <span className="truncate">{ns}</span>}
            {row.project && row.project !== 'default' && (
              <>
                <span aria-hidden>·</span>
                <span className="truncate">{row.project}</span>
              </>
            )}
          </div>
        )}
      </div>
    </button>
  )
}

// Render the source as `org/repo · path` instead of full URL. Keep `.git`
// off, drop scheme + host. Falls back to whatever's there if it doesn't
// parse as a github-style URL — Helm chart repos and bare hostnames just
// pass through.
function compactRepoSource(repo: string, path: string): string {
  if (!repo) return ''
  let head = repo.replace(/^https?:\/\//, '').replace(/\.git$/, '')
  // Strip well-known SaaS hosts so the org/repo part dominates
  head = head.replace(/^(github\.com|gitlab\.com|bitbucket\.org)\//, '')
  return path ? `${head} · ${path}` : head
}

// Drop common Kubernetes service URL prefixes so cluster destinations show
// as a recognizable label, not a verbose service URL the user has to parse.
function compactClusterURL(dest: string): string {
  return dest
    .replace(/^https?:\/\//, '')
    .replace(/^kubernetes\.default\.svc(:\d+)?\/?$/, 'in-cluster')
}

function shortRevision(rev: string): string {
  // Already short? Pass through (tags, branch names like "HEAD", short SHAs)
  if (rev.length <= 12) return rev
  // Long SHA → 7 chars (git default short)
  if (/^[0-9a-f]{12,}$/i.test(rev)) return rev.slice(0, 7)
  return rev
}

// Color the relative time so a quick glance answers "fresh / stale / old".
// Thresholds intentionally generous: <10m green, <1d default, >7d amber.
// Most production apps reconcile within minutes; >7d signals drift or a
// disabled sync controller.
function recencyTone(value: string): string {
  if (!value) return 'text-theme-text-tertiary'
  const time = Date.parse(value)
  if (!Number.isFinite(time)) return 'text-theme-text-tertiary'
  const diffMs = Date.now() - time
  if (diffMs < 10 * 60_000) return 'text-emerald-600 dark:text-emerald-400'
  if (diffMs > 7 * 24 * 60 * 60_000) return 'text-amber-600 dark:text-amber-400'
  return 'text-theme-text-secondary'
}

function TableHead({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <th className={`border-b border-theme-border px-3 py-2 font-medium ${className}`}>{children}</th>
}

function TableCell({ children }: { children: ReactNode }) {
  return <td className="border-b border-theme-border px-3 py-2 align-middle text-theme-text-secondary">{children}</td>
}

// Three top-level views per detail page:
//   topology — the resource tree, with an internal graph/table toggle since
//              both views share the same filter rail and dataset
//   changes  — drift between desired and live state
//   activity — current operation, history, diagnosis
type GitOpsAppView = 'topology' | 'changes' | 'activity'

function GitOpsDetailView({ namespaces, onOpenResource }: GitOpsViewProps) {
  const location = useLocation()
  const navigate = useNavigate()
  const { showError, showSuccess } = useToast()
  const parts = location.pathname.split('/').filter(Boolean)
  const kind = parts[2] || 'applications'
  const namespace = parts[3] === '_' ? '' : decodePathPart(parts[3] || '')
  const name = decodePathPart(parts[4] || '')
  const group = new URLSearchParams(location.search).get('apiGroup') || (KIND_BY_NAME.get(kind)?.group ?? '')
  const apiKind = KIND_BY_NAME.get(kind)
  // Parent lineage from the ?from=kind|namespace|name query param. Set by
  // openResourceFromTree when the user clicks a child GitOps node from a
  // parent's graph. Renders an extra breadcrumb segment + "↑ Open parent"
  // button so the user always knows where they came from. Falls back to
  // null (no breadcrumb) for direct/deep links.
  const parent = useMemo<{ kind: string; namespace: string; name: string; group: string } | null>(() => {
    const raw = new URLSearchParams(location.search).get('from')
    if (!raw) return null
    const [pKind = '', pNs = '', pName = ''] = raw.split('|')
    if (!pKind || !pName) return null
    return {
      kind: pKind,
      namespace: pNs,
      name: pName,
      group: KIND_BY_NAME.get(pKind)?.group ?? '',
    }
  }, [location.search])

  const resourceQ = useResource<any>(kind, namespace, name, group)
  const treeQ = useGitOpsTree(kind, namespace, name, group, namespaces)
  const insightsQ = useGitOpsInsights(kind, namespace, name, group, namespaces)
  const status = resourceQ.data ? getGitOpsStatus(kind, resourceQ.data) : null
  const tool = getTool(kind, group)
  // Argo "auto-sync ON" is determined by spec.syncPolicy.automated being set,
  // not by health.status === Suspended (which is Argo's CronJob-style suspend).
  // The toggle button reads from this so the label flips correctly when an
  // app is in Manual mode or suspended via Radar's annotations.
  const argoAutoSyncEnabled = kind === 'applications' && Boolean(resourceQ.data?.spec?.syncPolicy?.automated)
  // Radar-driven Argo suspension is signaled by annotations that record the
  // pre-suspend prune/selfHeal state for restoration on resume. When present,
  // the app is in a deliberately-paused state (vs. Manual mode, which is a
  // normal operational choice) and should surface a Suspended chip alongside
  // the other status indicators.
  const argoSuspendedByRadar =
    kind === 'applications' &&
    Boolean(
      resourceQ.data?.metadata?.annotations?.['radarhq.io/suspended-prune'] ||
        resourceQ.data?.metadata?.annotations?.['radarhq.io/suspended-selfheal'] ||
        resourceQ.data?.metadata?.annotations?.['skyhook.io/suspended-prune'] ||
        resourceQ.data?.metadata?.annotations?.['skyhook.io/suspended-selfheal'],
    )
  const effectiveSuspended = (status?.suspended ?? false) || argoSuspendedByRadar
  // Lifecycle gate: when the resource is pending deletion, mutating
  // actions are futile (the controller is processing finalizers and
  // ignores reconcile/sync triggers). Surface it visually + disable
  // the affected buttons. Read-style verbs (Refresh, Hard refresh,
  // Terminate) intentionally remain enabled — see the corresponding
  // carve-out in pkg/gitops/operations.go.
  const terminating = !!insightsQ.data?.summary?.terminating
  const terminatingDescriptions = describeTerminating(insightsQ.data?.summary)
  const terminatingChipTooltip = terminatingDescriptions.chipTooltip
  const terminatingActionTooltip = terminatingDescriptions.actionDisabledTooltip
  const [appView, setAppView] = useState<GitOpsAppView>('topology')
  // When the user clicks an actionable issue alert ("OutOfSync — NodePool
  // default is out of sync · View →"), we navigate to Changes and focus
  // that resource. The ref is stringified to a stable key so GitOpsChangesView
  // can find and scroll it; cleared after a few seconds so the highlight
  // doesn't persist past its purpose.
  const [changesFocusKey, setChangesFocusKey] = useState<string | null>(null)
  const [graphPreset, setGraphPreset] = useState<GitOpsTreePreset>('compact')
  const [graphSearch, setGraphSearch] = useState('')
  const [graphKinds, setGraphKinds] = useState<Set<string>>(new Set())
  const [graphSync, setGraphSync] = useState<Set<string>>(new Set())
  const [graphHealth, setGraphHealth] = useState<Set<string>>(new Set())
  const [graphNamespaces, setGraphNamespaces] = useState<Set<string>>(new Set())
  const [graphRoles, setGraphRoles] = useState<Set<string>>(new Set())
  const [graphFullscreen, setGraphFullscreen] = useState(false)
  const [helmValuesOpen, setHelmValuesOpen] = useState(false)

  const argoSync = useArgoSync()
  const argoRefresh = useArgoRefresh()
  const argoTerminate = useArgoTerminate()
  const argoSuspend = useArgoSuspend()
  const argoResume = useArgoResume()
  const argoRollback = useArgoRollback()
  const applyResource = useApplyResource()
  const fluxReconcile = useFluxReconcile()
  const fluxSyncWithSource = useFluxSyncWithSource()
  const fluxSuspend = useFluxSuspend()
  const fluxResume = useFluxResume()

  const [syncDialogOpen, setSyncDialogOpen] = useState(false)
  // Doubles as the "open" flag (truthy = dialog open) and the data carrier
  // for which history entry to roll back to.
  const [rollbackTarget, setRollbackTarget] = useState<GitOpsHistoryItem | null>(null)
  // Disambiguates which refresh button is in flight (both share argoRefresh).
  const [refreshKind, setRefreshKind] = useState<'normal' | 'hard'>('normal')

  const detailRow = resourceQ.data ? normalizeDetailResource(kind, group, resourceQ.data) : null
  const tree = treeQ.data ?? null
  const helmValues = useMemo(() => extractHelmValues(kind, resourceQ.data), [kind, resourceQ.data])
  const graphFilters = useMemo<GitOpsTreeFilters>(() => ({
    kinds: graphKinds,
    sync: graphSync,
    health: graphHealth,
    namespaces: graphNamespaces,
    roles: graphRoles,
  }), [graphHealth, graphKinds, graphNamespaces, graphRoles, graphSync])
  const graphFacets = useMemo(() => buildTreeFacets(tree), [tree])

  function openResourceFromTree(ref: GitOpsTreeRef | GitOpsInsightRef) {
    if (isGitOpsDetailRef(ref) && isValidKubernetesName(ref.name)) {
      const detailKind = kindToPlural(ref.kind)
      const params = new URLSearchParams()
      if (ref.group) params.set('apiGroup', ref.group)
      // Lineage breadcrumb support: when the user opens a child GitOps CR
      // from inside a parent's tree, encode the parent into the URL so
      // the child page can render "GitOps / parent / child" + "↑ Open
      // parent" affordance. Encoded as kind|namespace|name (a single
      // "from" param keeps the URL short; multi-level lineage isn't
      // supported here yet — the deepest valid breadcrumb is parent →
      // child. Going further would need either a chain encoding or
      // history-state walking, both deferred until the use case shows up).
      const fromKind = apiKind?.name ?? kind
      if (fromKind && name) {
        params.set('from', `${fromKind}|${namespace || ''}|${name}`)
      }
      navigate({ pathname: gitOpsDetailPath(detailKind, ref.namespace || '_', ref.name), search: params.toString() })
      return
    }
    onOpenResource({ kind: kindToPlural(ref.kind), namespace: ref.namespace || '', name: ref.name, group: ref.group })
  }

  const isRunning = resourceQ.data?.status?.operationState?.phase === 'Running'
  const isFluxWorkload = kind === 'kustomizations' || kind === 'helmreleases'
  const isFlux = tool === 'flux'
  const isArgoApp = kind === 'applications'
  const graphShellClass = graphFullscreen
    ? 'fixed inset-0 z-[80] flex min-h-0 min-w-0 flex-col bg-theme-base'
    : 'flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden'

  // Set the browser tab title so users with multiple resource tabs open can
  // tell which is which without focusing each tab. Restore on unmount so a
  // stray "Radar — argocd/foo" doesn't outlive its page.
  useEffect(() => {
    const previous = document.title
    document.title = `${name} — Radar`
    return () => { document.title = previous }
  }, [name])

  // Detail-page shortcuts. Skip when a modal is already open so a stray "s"
  // in an input field doesn't pop another sync dialog.
  const shortcutsEnabled = !syncDialogOpen && !rollbackTarget
  useRegisterShortcut({
    id: 'gitops-detail-sync',
    keys: 's',
    description: isArgoApp ? 'Open sync options' : 'Reconcile',
    category: 'GitOps',
    scope: 'gitops',
    handler: () => {
      if (effectiveSuspended || terminating) return
      if (isArgoApp) setSyncDialogOpen(true)
      else if (isFlux) fluxReconcile.mutate({ kind, namespace, name })
    },
    enabled: shortcutsEnabled && (isArgoApp || isFlux) && !effectiveSuspended && !terminating,
  })
  useRegisterShortcut({
    id: 'gitops-detail-refresh',
    keys: 'r',
    description: 'Refresh application',
    category: 'GitOps',
    scope: 'gitops',
    handler: () => {
      if (!isArgoApp) return
      setRefreshKind('normal')
      argoRefresh.mutate({ namespace, name, hard: false })
    },
    enabled: shortcutsEnabled && isArgoApp,
  })
  useRegisterShortcut({
    id: 'gitops-detail-hard-refresh',
    keys: 'Shift+R',
    description: 'Hard refresh (re-resolve source from Git)',
    category: 'GitOps',
    scope: 'gitops',
    handler: () => {
      if (!isArgoApp) return
      setRefreshKind('hard')
      argoRefresh.mutate({ namespace, name, hard: true })
    },
    enabled: shortcutsEnabled && isArgoApp,
  })
  useRegisterShortcut({
    id: 'gitops-detail-terminate',
    keys: 't',
    description: 'Terminate running sync',
    category: 'GitOps',
    scope: 'gitops',
    handler: () => {
      if (isArgoApp && isRunning) argoTerminate.mutate({ namespace, name })
    },
    enabled: shortcutsEnabled && isArgoApp && isRunning,
  })

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-theme-base">
      {!graphFullscreen && <div className="shrink-0 border-b border-theme-border bg-theme-base px-4 py-3">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            {/* Breadcrumb collapses into the title row: parent ("GitOps") +
                resource name on one line. Tool + Kind are *properties* of
                this resource, not navigation, so they live as a neutral
                chip alongside the status badges instead of as breadcrumb
                segments. Future nested cases (app-of-apps) can extend the
                breadcrumb with intermediate parent links here. */}
            <div className="flex flex-wrap items-center gap-2">
              <button
                type="button"
                onClick={() => navigate('/gitops')}
                className="shrink-0 text-xs font-medium text-sky-500 transition-colors hover:text-sky-400"
              >
                GitOps
              </button>
              <span className="shrink-0 text-xs text-theme-text-tertiary">/</span>
              {/* Parent breadcrumb segment when the user opened this CR
                  from inside another CR's tree (app-of-apps, Flux
                  Kustomization-applies-Kustomization, etc). Without this,
                  navigation between nested GitOps surfaces feels like
                  unrelated jumps; with it, the lineage is always visible
                  and clickable. The segment renders smaller than the
                  current title to avoid competing for visual weight. */}
              {parent && (
                <>
                  <button
                    type="button"
                    onClick={() => {
                      const params = new URLSearchParams()
                      if (parent.group) params.set('apiGroup', parent.group)
                      navigate({
                        pathname: gitOpsDetailPath(parent.kind, parent.namespace || '_', parent.name),
                        search: params.toString(),
                      })
                    }}
                    className="shrink-0 truncate max-w-[200px] text-xs font-medium text-sky-500 transition-colors hover:text-sky-400"
                    title={`Open parent: ${parent.namespace ? `${parent.namespace}/` : ''}${parent.name}`}
                  >
                    {parent.namespace ? `${parent.namespace}/` : ''}{parent.name}
                  </button>
                  <span className="shrink-0 text-xs text-theme-text-tertiary">/</span>
                </>
              )}
              <h1 className="min-w-0 truncate text-lg font-semibold text-theme-text-primary">
                {namespace ? `${namespace}/` : ''}{name}
              </h1>
              {/* When Terminating, suppress Sync/Health badges entirely.
                  They reflect the last observed reconcile state and become
                  factually stale the moment deletion is initiated — Flux
                  is processing finalizers, not syncing, and the resource
                  isn't "Progressing" toward a healthy state, it's being
                  torn down. Showing "Syncing · Progressing · Terminating"
                  side-by-side is contradictory and actively misleading;
                  Argo CD's own UI suppresses these badges for the same
                  reason. The Terminating chip becomes the sole status
                  indicator. */}
              {status && !terminating && (
                <>
                  <SyncStatusBadge sync={status.sync} suspended={effectiveSuspended} />
                  {!effectiveSuspended && <HealthStatusBadge health={status.health} />}
                </>
              )}
              {terminating && (
                <Tooltip content={terminatingChipTooltip}>
                  <span className="badge border border-orange-500/40 bg-orange-500/10 text-orange-400">
                    <Trash2 className="h-3 w-3" />
                    Terminating
                  </span>
                </Tooltip>
              )}
              <span className="inline-flex shrink-0 items-center rounded border border-theme-border bg-theme-hover/50 px-1.5 py-0.5 text-[11px] font-medium text-theme-text-secondary">
                {tool === 'argo' ? 'ArgoCD' : 'FluxCD'} · {apiKind?.kind ?? kind}
              </span>
            </div>
            {/* Header carries the *spec/config* facts — where this app lives
                and how it syncs. The deployment row (status strip below)
                carries dynamic per-deploy facts (latest revision, age,
                resource health). Splitting static config from live state
                avoids the "I changed nothing but everything looks different"
                effect of mixing them. */}
            <div className="mt-2 flex flex-wrap gap-x-5 gap-y-0.5 text-[11px] text-theme-text-tertiary">
              <AppFact label="Project" value={detailRow?.project || '-'} />
              {detailRow?.repository && <AppFact label="Source" value={formatSourceRepo(detailRow.repository)} />}
              {detailRow?.path && <AppFact label="Path" value={detailRow.path} />}
              {detailRow?.chart && <AppFact label="Chart" value={detailRow.chart} />}
              <AppFact label="Destination" value={formatDestination(detailRow?.destination, detailRow?.destinationNamespace)} />
              {insightsQ.data?.summary?.autoSyncMode && (
                <AppFact label="Sync mode" value={insightsQ.data.summary.autoSyncMode} />
              )}
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {isArgoApp && (
              <>
                <ActionButton label="Sync…" description="Apply manifests from Git to the cluster. Opens an options dialog (prune, dry-run, revision)." icon={RefreshCw} loading={argoSync.isPending} onClick={() => setSyncDialogOpen(true)} disabled={effectiveSuspended || terminating} disabledReason={terminating ? terminatingActionTooltip : undefined} primary />
                <ActionButton
                  label="Refresh"
                  description="Re-check Git for new commits and recompute sync status. Doesn't apply anything."
                  icon={RotateCw}
                  loading={argoRefresh.isPending && refreshKind === 'normal'}
                  onClick={() => {
                    setRefreshKind('normal')
                    argoRefresh.mutate({ namespace, name, hard: false })
                  }}
                />
                <ActionButton
                  label="Hard refresh"
                  description="Like Refresh, but also bypasses Argo's manifest cache (re-renders Helm/Kustomize)."
                  icon={RotateCw}
                  loading={argoRefresh.isPending && refreshKind === 'hard'}
                  onClick={() => {
                    setRefreshKind('hard')
                    argoRefresh.mutate({ namespace, name, hard: true })
                  }}
                />
                {isRunning && <ActionButton label="Terminate" description="Cancel the in-progress sync operation." icon={XCircle} loading={argoTerminate.isPending} onClick={() => argoTerminate.mutate({ namespace, name })} danger />}
                {argoAutoSyncEnabled
                  ? <ActionButton label="Disable auto-sync" description="Stop Argo from automatically syncing Git changes. Manual Sync still works." icon={Pause} loading={argoSuspend.isPending} onClick={() => argoSuspend.mutate({ namespace, name })} disabled={terminating} disabledReason={terminating ? terminatingActionTooltip : undefined} />
                  : <ActionButton label="Enable auto-sync" description="Re-enable automatic syncing of Git changes to the cluster." icon={Play} loading={argoResume.isPending} onClick={() => argoResume.mutate({ namespace, name })} disabled={terminating} disabledReason={terminating ? terminatingActionTooltip : undefined} />}
              </>
            )}
            {isFlux && (
              <>
                <ActionButton label="Reconcile" description="Tell Flux to reconcile this resource now: re-read its source, re-apply the manifests, update status. Skips waiting for the regular reconciliation interval." icon={RefreshCw} loading={fluxReconcile.isPending} onClick={() => fluxReconcile.mutate({ kind, namespace, name })} disabled={effectiveSuspended || terminating} disabledReason={terminating ? terminatingActionTooltip : undefined} primary />
                {isFluxWorkload && (
                  <ActionButton
                    label="Sync with source"
                    description="Reconcile the upstream source CR (GitRepository/HelmRepository) first — re-fetching from Git or Helm — then reconcile this resource against the refreshed source. Useful right after pushing a commit when you don't want to wait for the source's poll interval."
                    icon={GitCommit}
                    loading={fluxSyncWithSource.isPending}
                    onClick={() => fluxSyncWithSource.mutate({ kind, namespace, name })}
                    disabled={terminating}
                    disabledReason={terminating ? terminatingActionTooltip : undefined}
                  />
                )}
                {status?.suspended
                  ? <ActionButton label="Resume" description="Resume Flux reconciliation. Flux will start applying changes from the source again on its normal interval." icon={Play} loading={fluxResume.isPending} onClick={() => fluxResume.mutate({ kind, namespace, name })} disabled={terminating} disabledReason={terminating ? terminatingActionTooltip : undefined} />
                  : <ActionButton label="Suspend" description="Pause Flux reconciliation. The resource stays exactly as-is — new commits in the source won't be applied until you resume." icon={Pause} loading={fluxSuspend.isPending} onClick={() => fluxSuspend.mutate({ kind, namespace, name })} disabled={terminating} disabledReason={terminating ? terminatingActionTooltip : undefined} />}
              </>
            )}
          </div>
        </div>
      </div>}
      {!graphFullscreen && (
        <>
          <GitOpsStatusStrip insight={insightsQ.data} loading={insightsQ.isLoading} />
          <GitOpsIssuesBand
            issues={insightsQ.data?.issues}
            terminating={terminating}
            onSelectIssue={(issue) => {
              const ref = issue.refs?.[0]
              if (!ref) return
              setAppView('changes')
              setChangesFocusKey(insightChangeKey(ref))
              // Window the highlight: 4s is long enough to find the row
              // visually but short enough that it doesn't linger if the user
              // navigates away and comes back.
              window.setTimeout(() => setChangesFocusKey(null), 4000)
            }}
            remediationPending={applyResource.isPending || argoSync.isPending}
            onRemediate={(remediation) => {
              if (remediation.kind === 'create-namespace' && remediation.target) {
                const nsName = remediation.target
                const yaml = `apiVersion: v1\nkind: Namespace\nmetadata:\n  name: ${nsName}\n`
                applyResource.mutate(
                  { yaml, mode: 'apply' },
                  {
                    onSuccess: () => {
                      // Defer the success toast until we know whether the
                      // follow-on sync was triggered, so a successful
                      // create-namespace followed by a sync failure doesn't
                      // produce a "Triggering a sync" toast that argoSync's
                      // own error toast immediately contradicts.
                      if (kind === 'applications') {
                        argoSync.mutate(
                          { namespace, name },
                          {
                            onSuccess: () => {
                              showSuccess(
                                `Created namespace ${nsName}`,
                                'Sync triggered to retry the apply.',
                              )
                            },
                            onError: () => {
                              // argoSync's mutation meta already raises its
                              // own "Failed to trigger sync" toast; here we
                              // tell the user explicitly that the *namespace*
                              // landed even though the sync didn't, so they
                              // know to click Sync manually.
                              showSuccess(
                                `Created namespace ${nsName}`,
                                "Couldn't trigger sync automatically — click Sync to retry.",
                              )
                            },
                          },
                        )
                      } else {
                        // Non-Argo callers don't get the auto-sync chain; the
                        // create-namespace landing is the only thing to report.
                        showSuccess(`Created namespace ${nsName}`)
                      }
                    },
                    onError: (err: unknown) => {
                      const msg = err instanceof Error ? err.message : 'Unknown error'
                      showError(
                        `Couldn't create namespace ${nsName}`,
                        msg.includes('forbidden')
                          ? 'Radar lacks RBAC to create namespaces in this cluster. Create it manually or have a cluster-admin do it.'
                          : msg,
                      )
                    },
                  },
                )
              }
            }}
          />
          {helmValues && (
            <div className="shrink-0 border-b border-theme-border bg-theme-base">
              <button
                type="button"
                onClick={() => setHelmValuesOpen((v) => !v)}
                className="flex w-full items-center gap-2 px-4 py-2 text-left text-xs text-theme-text-secondary hover:bg-theme-hover"
                aria-expanded={helmValuesOpen}
              >
                {helmValuesOpen ? (
                  <ChevronDown className="h-3.5 w-3.5 shrink-0" />
                ) : (
                  <ChevronRight className="h-3.5 w-3.5 shrink-0" />
                )}
                <Settings className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
                <span className="font-medium text-theme-text-primary">Helm values</span>
                <span className="tabular-nums text-theme-text-tertiary">
                  {helmValues.keyCount} {helmValues.keyCount === 1 ? 'key' : 'keys'}
                </span>
                {helmValues.source === 'argo-parameters' && (
                  <span className="text-[10px] uppercase tracking-wider text-theme-text-tertiary">parameters</span>
                )}
              </button>
              {helmValuesOpen && (
                <div className="border-t border-theme-border bg-theme-surface px-4 py-3">
                  <CodeViewer code={helmValues.yaml} language="yaml" showLineNumbers maxHeight="320px" />
                </div>
              )}
            </div>
          )}
        </>
      )}

      {resourceQ.isLoading ? (
        <div className="flex flex-1 items-center justify-center text-theme-text-secondary">
          <Loader2 className="mr-2 h-4 w-4 animate-spin" /> Loading GitOps resource…
        </div>
      ) : resourceQ.error ? (
        <div className="p-4 text-sm text-red-500">Failed to load resource: {(resourceQ.error as Error).message}</div>
      ) : (
        <div className={graphShellClass}>
          <div className="flex shrink-0 items-center justify-between gap-3 border-b border-theme-border bg-theme-base px-4 py-2">
            <div className="flex items-center gap-1 rounded-md border border-theme-border bg-theme-surface p-1">
              <ViewButton active={appView === 'topology'} icon={GitBranch} label="Topology" onClick={() => setAppView('topology')} />
              <ViewButton active={appView === 'changes'} icon={GitCommit} label="Resources" onClick={() => setAppView('changes')} />
              <ViewButton active={appView === 'activity'} icon={Clock3} label="Activity" onClick={() => setAppView('activity')} />
            </div>
            {graphFullscreen ? (
              <div className="min-w-0 flex-1 truncate text-sm font-medium text-theme-text-primary">
                {namespace ? `${namespace}/` : ''}{name}
              </div>
            ) : (
              appView === 'topology' && tree && <TopologyCounts tree={tree} />
            )}
            <div className="flex items-center gap-2">
              {appView === 'topology' && (
                <>
                  <button
                    type="button"
                    onClick={() => {
                      setGraphSearch('')
                      setGraphKinds(new Set())
                      setGraphSync(new Set())
                      setGraphHealth(new Set())
                      setGraphNamespaces(new Set())
                      setGraphRoles(new Set())
                    }}
                    className="rounded px-2 py-1 text-xs text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
                  >
                    Clear filters
                  </button>
                  <button
                    type="button"
                    onClick={() => setGraphFullscreen(!graphFullscreen)}
                    className="rounded-md border border-theme-border bg-theme-surface px-2 py-1 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
                  >
                    {graphFullscreen ? 'Exit fullscreen' : 'Fullscreen'}
                  </button>
                </>
              )}
            </div>
          </div>

          {appView === 'activity' ? (
            <GitOpsActivityInsightView
              insight={insightsQ.data}
              error={insightsQ.error as Error | null}
              // Only Argo apps support rollback. Skip the callback entirely
              // for entries with non-numeric IDs (Flux conditions reuse the
              // ID slot for condition.type) so the button doesn't render and
              // then silently fail when clicked.
              onRollback={isArgoApp ? (item) => {
                if (parseRollbackID(item.id) == null) return
                setRollbackTarget(item)
              } : undefined}
            />
          ) : appView === 'changes' ? (
            <GitOpsChangesView
              insight={insightsQ.data}
              error={insightsQ.error as Error | null}
              onOpenResource={openResourceFromTree}
              focusKey={changesFocusKey}
              tree={tree}
            />
          ) : (
            <div className="grid min-h-0 min-w-0 flex-1 grid-cols-[280px_minmax(0,1fr)] max-lg:grid-cols-1">
              <GitOpsGraphFilterRail
                facets={graphFacets}
                preset={graphPreset}
                onPresetChange={setGraphPreset}
                search={graphSearch}
                onSearchChange={setGraphSearch}
                kinds={graphKinds}
                onToggleKind={(value) => toggleSet(graphKinds, setGraphKinds, value)}
                sync={graphSync}
                onToggleSync={(value) => toggleSet(graphSync, setGraphSync, value)}
                health={graphHealth}
                onToggleHealth={(value) => toggleSet(graphHealth, setGraphHealth, value)}
                namespaces={graphNamespaces}
                onToggleNamespace={(value) => toggleSet(graphNamespaces, setGraphNamespaces, value)}
                roles={graphRoles}
                onToggleRole={(value) => toggleSet(graphRoles, setGraphRoles, value)}
              />
              <div className="min-h-0 min-w-0 border-l border-theme-border max-lg:border-l-0 max-lg:border-t">
                <GitOpsTreeGraph
                  tree={tree}
                  loading={treeQ.isLoading}
                  error={treeQ.error as Error | null}
                  onNodeClick={openResourceFromTree}
                  preset={graphPreset}
                  onPresetChange={setGraphPreset}
                  query={graphSearch}
                  onQueryChange={setGraphSearch}
                  filters={graphFilters}
                  showToolbar={false}
                />
              </div>
            </div>
          )}
        </div>
      )}
      {/* Modals — portaled to body, only render the ones for the current tool. */}
      {isArgoApp && (
        <>
          <SyncOptionsDialog
            open={syncDialogOpen}
            appLabel={`${namespace}/${name}`}
            pending={argoSync.isPending}
            onCancel={() => setSyncDialogOpen(false)}
            onConfirm={(opts) => {
              // Close on success only — on failure keep the dialog open so
              // the user doesn't lose their form context (revision, advanced
              // toggles). The error toast fires globally via mutation meta.
              argoSync.mutate({ namespace, name, ...opts }, {
                onSuccess: () => setSyncDialogOpen(false),
              })
            }}
          />
          <RollbackDialog
            open={!!rollbackTarget}
            appLabel={`${namespace}/${name}`}
            revision={rollbackTarget?.revision || ''}
            historyId={rollbackTarget?.id}
            pending={argoRollback.isPending}
            onCancel={() => setRollbackTarget(null)}
            onConfirm={(opts) => {
              // Defensive: history may have refreshed between modal-open and
              // confirm, leaving the captured id no longer parseable.
              const id = parseRollbackID(rollbackTarget?.id)
              if (id == null) {
                showError('Rollback target became invalid', 'The history entry changed while the dialog was open. Reselect a target and try again.')
                setRollbackTarget(null)
                return
              }
              argoRollback.mutate({ namespace, name, id, ...opts }, {
                onSuccess: () => setRollbackTarget(null),
              })
            }}
          />
        </>
      )}
    </div>
  )
}

// formatSourceRepo drops the protocol prefix from a Git source URL so the
// row reads as "github.com/org/repo" instead of the redundant
// "https://github.com/org/repo". Leaves non-https URLs alone so SSH-style
// origins (`git@github.com:org/repo`) and HTTP-only on-prem mirrors still
// render as the user wrote them.
function formatSourceRepo(repo: string): string {
  return repo.replace(/^https?:\/\//, '')
}

type HelmValuesSource = 'flux' | 'argo-object' | 'argo-string' | 'argo-parameters'
interface HelmValuesData {
  yaml: string
  keyCount: number
  source: HelmValuesSource
}

// Both Flux HelmRelease and Argo CD Application-with-Helm-source carry user
// overrides for chart values, but spell them differently. We surface them via
// a single disclosure on the GitOps detail page; this helper normalizes the
// four flavors we may encounter into one renderable shape.
function extractHelmValues(kind: string, resource: any): HelmValuesData | null {
  if (!resource) return null
  if (kind === 'helmreleases') {
    const values = resource?.spec?.values
    if (values && typeof values === 'object' && Object.keys(values).length > 0) {
      return { yaml: safeStringifyYaml(values), keyCount: Object.keys(values).length, source: 'flux' }
    }
    return null
  }
  if (kind === 'applications') {
    const helm = resource?.spec?.source?.helm
    if (helm?.valuesObject && typeof helm.valuesObject === 'object' && Object.keys(helm.valuesObject).length > 0) {
      return {
        yaml: safeStringifyYaml(helm.valuesObject),
        keyCount: Object.keys(helm.valuesObject).length,
        source: 'argo-object',
      }
    }
    if (typeof helm?.values === 'string' && helm.values.trim() !== '') {
      const parsed = tryParseYaml(helm.values)
      const keyCount = parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? Object.keys(parsed).length : 0
      return { yaml: helm.values, keyCount, source: 'argo-string' }
    }
    if (Array.isArray(helm?.parameters) && helm.parameters.length > 0) {
      const obj: Record<string, unknown> = {}
      for (const param of helm.parameters) {
        if (param?.name) obj[param.name] = param.value
      }
      if (Object.keys(obj).length === 0) return null
      return {
        yaml: safeStringifyYaml(obj),
        keyCount: Object.keys(obj).length,
        source: 'argo-parameters',
      }
    }
  }
  return null
}

function safeStringifyYaml(value: unknown): string {
  try {
    return yaml.stringify(value)
  } catch {
    return JSON.stringify(value, null, 2)
  }
}

function tryParseYaml(value: string): unknown {
  try {
    return yaml.parse(value)
  } catch {
    return null
  }
}

// formatDestination collapses the canonical in-cluster API server URL
// ("https://kubernetes.default.svc" or the variant Argo writes when the
// destination is the same cluster the controller runs in) to the friendlier
// "in-cluster". Other server values pass through unchanged. The namespace
// is appended with an explicit "Namespace:" qualifier so the relationship
// reads unambiguously — bare `/ ns` could be mistaken for a sub-path.
function formatDestination(server: string | undefined, namespace: string | undefined): string {
  let host = (server || '').trim()
  if (host === '' || host === 'https://kubernetes.default.svc' || host === 'in-cluster') {
    host = 'in-cluster'
  } else {
    host = host.replace(/^https?:\/\//, '')
  }
  return namespace ? `${host}, Namespace: ${namespace}` : host
}

function AppFact({ label, value }: { label: string; value: string }) {
  // inline-flex (not flex) so each fact sizes to its content; the parent's
  // flex-wrap handles row breaks at narrow viewports. max-w-full is the
  // safety net for the rare case of a single value wider than the screen
  // (truncate + tooltip kicks in then). Without it, very long destinations
  // would force the page to scroll horizontally.
  return (
    <span className="inline-flex min-w-0 max-w-full items-baseline gap-1">
      <span className="shrink-0 text-theme-text-tertiary">{label}:</span>
      <Tooltip content={value} delay={400} wrapperClassName="min-w-0">
        <span className="block truncate text-theme-text-primary">{value}</span>
      </Tooltip>
    </span>
  )
}

function ViewButton({
  active,
  icon: Icon,
  label,
  onClick,
}: {
  active: boolean
  icon: ComponentType<{ className?: string }>
  label: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`inline-flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium transition-colors ${
        active
          ? 'bg-skyhook-500 text-white'
          : 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
      }`}
    >
      <Icon className="h-3.5 w-3.5" />
      {label}
    </button>
  )
}

function GitOpsGraphFilterRail({
  facets,
  preset,
  onPresetChange,
  search,
  onSearchChange,
  kinds,
  onToggleKind,
  sync,
  onToggleSync,
  health,
  onToggleHealth,
  namespaces,
  onToggleNamespace,
  roles,
  onToggleRole,
}: {
  facets: ReturnType<typeof buildTreeFacets>
  preset: GitOpsTreePreset
  onPresetChange: (preset: GitOpsTreePreset) => void
  search: string
  onSearchChange: (value: string) => void
  kinds: Set<string>
  onToggleKind: (value: string) => void
  sync: Set<string>
  onToggleSync: (value: string) => void
  health: Set<string>
  onToggleHealth: (value: string) => void
  namespaces: Set<string>
  onToggleNamespace: (value: string) => void
  roles: Set<string>
  onToggleRole: (value: string) => void
}) {
  return (
    <aside className="min-h-0 overflow-y-auto bg-theme-surface/90 max-lg:h-48 max-lg:max-h-48">
      <div className="border-b border-theme-border px-3 py-3">
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-theme-text-tertiary" />
          <input
            value={search}
            onChange={(event) => onSearchChange(event.target.value)}
            placeholder="Filter resources..."
            className="h-8 w-full rounded-md border border-theme-border bg-theme-base pl-8 pr-3 text-sm text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:ring-1 focus:ring-blue-500/50"
          />
        </div>
      </div>
      <FilterSection icon={GitBranch} title="Graph">
        <div className="grid grid-cols-2 gap-1">
          {(['compact', 'workloads', 'app', 'full'] as GitOpsTreePreset[]).map((value) => (
            <button
              key={value}
              type="button"
              onClick={() => onPresetChange(value)}
              className={`rounded-md px-2 py-1.5 text-left text-[11px] font-medium transition-colors ${
                preset === value
                  ? 'bg-skyhook-500 text-white'
                  : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
              }`}
            >
              {value === 'app' ? 'Declared' : value[0].toUpperCase() + value.slice(1)}
            </button>
          ))}
        </div>
      </FilterSection>
      <FilterSection icon={List} title="Kinds">
        {facets.kinds.slice(0, 14).map((item) => (
          <FacetButton key={item.name} label={item.name} count={item.count} active={kinds.has(item.name)} onClick={() => onToggleKind(item.name)} />
        ))}
      </FilterSection>
      <FilterSection icon={CheckCircle2} title="Sync">
        {facets.sync.map((item) => (
          <FacetButton key={item.name} label={item.name} count={item.count} active={sync.has(item.name)} tone={syncTone(item.name)} onClick={() => onToggleSync(item.name)} />
        ))}
      </FilterSection>
      <FilterSection icon={HeartPulse} title="Health">
        {facets.health.map((item) => (
          <FacetButton key={item.name} label={item.name} count={item.count} active={health.has(item.name)} tone={healthTone(item.name)} onClick={() => onToggleHealth(item.name)} />
        ))}
      </FilterSection>
      <FilterSection icon={CircleDot} title="Role">
        {facets.roles.map((item) => (
          <FacetButton key={item.name} label={roleLabel(item.name)} count={item.count} active={roles.has(item.name)} onClick={() => onToggleRole(item.name)} />
        ))}
      </FilterSection>
      <FilterSection icon={LayoutGrid} title="Namespaces">
        {facets.namespaces.slice(0, 12).map((item) => (
          <FacetButton key={item.name} label={item.name} count={item.count} active={namespaces.has(item.name)} onClick={() => onToggleNamespace(item.name)} />
        ))}
      </FilterSection>
    </aside>
  )
}

function buildTreeFacets(tree: GitOpsResourceTree | null) {
  const nodes = tree?.nodes ?? []
  return {
    kinds: countValues(nodes.filter((node) => node.role !== 'group').map((node) => node.ref.kind).filter(Boolean)),
    sync: countValues(nodes.map((node) => node.sync || 'Unknown')),
    health: countValues(nodes.map((node) => node.health || 'Unknown')),
    namespaces: countValues(nodes.map((node) => node.ref.namespace || '(cluster)')),
    roles: countValues(nodes.map((node) => node.role)),
  }
}

function normalizeDetailResource(kind: string, group: string, resource: any): GitOpsRow | null {
  if (kind === 'applications') return normalizeArgoApplication(resource)
  if (kind === 'kustomizations') return normalizeFluxKustomization(resource)
  if (kind === 'helmreleases') return normalizeFluxHelmRelease(resource)
  const status = getGitOpsStatus(kind, resource)
  return {
    id: `${group}/${kind}/${resource.metadata?.namespace ?? ''}/${resource.metadata?.name ?? ''}`,
    mode: 'applications',
    tool: getTool(kind, group),
    kindName: kind,
    kind: resource.kind ?? kind,
    group,
    name: resource.metadata?.name ?? '',
    namespace: resource.metadata?.namespace ?? '',
    project: resource.metadata?.namespace ?? '',
    labels: resource.metadata?.labels ?? {},
    sync: status?.sync ?? 'Unknown',
    health: status?.health ?? 'Unknown',
    suspended: status?.suspended ?? resource.spec?.suspend === true,
    repository: resource.spec?.url ?? resource.spec?.sourceRef?.name ?? '',
    targetRevision: resource.status?.artifact?.revision ?? resource.status?.lastAppliedRevision ?? resource.status?.lastAttemptedRevision ?? '',
    path: resource.spec?.path ?? '',
    chart: resource.spec?.chart?.spec?.chart ?? '',
    destination: 'in-cluster',
    destinationNamespace: resource.spec?.targetNamespace ?? resource.metadata?.namespace ?? '',
    createdAt: resource.metadata?.creationTimestamp ?? '',
    lastSync: newestConditionTime(resource),
    autoSync: !resource.spec?.suspend,
    terminating: isTerminating(resource),
    terminationStartedAt: terminationStartedAt(resource),
    raw: resource,
  }
}

function syncTone(value: string): 'neutral' | 'success' | 'warning' | 'error' | 'info' {
  if (value === 'Synced') return 'success'
  if (value === 'OutOfSync') return 'warning'
  if (value === 'Reconciling') return 'info'
  return 'neutral'
}

function healthTone(value: string): 'neutral' | 'success' | 'warning' | 'error' | 'info' {
  if (value === 'Healthy') return 'success'
  if (value === 'Degraded' || value === 'Missing') return 'error'
  if (value === 'Progressing') return 'info'
  if (value === 'Suspended') return 'warning'
  return 'neutral'
}

function roleLabel(value: string) {
  return {
    root: 'Root',
    declared: 'Declared',
    generated: 'Generated',
    group: 'Groups',
  }[value] ?? value
}

function gitOpsDetailPath(kind: string, namespace: string, name: string): string {
  return `/gitops/detail/${encodeURIComponent(kind)}/${encodeURIComponent(namespace || '_')}/${encodeURIComponent(name)}`
}

function decodePathPart(value: string): string {
  try {
    return decodeURIComponent(value)
  } catch {
    return value
  }
}

function isGitOpsDetailRef(ref: GitOpsTreeRef | GitOpsInsightRef): boolean {
  const kind = ref.kind.toLowerCase()
  if (ref.group === 'argoproj.io') {
    return kind === 'application' || kind === 'applicationset' || kind === 'appproject'
  }
  if (ref.group === 'kustomize.toolkit.fluxcd.io') return kind === 'kustomization'
  if (ref.group === 'helm.toolkit.fluxcd.io') return kind === 'helmrelease'
  // Flux source CRs (GitRepository/HelmRepository/OCIRepository/Bucket/HelmChart)
  // are NOT GitOps detail-page CRs — they're config objects with spec/status
  // but no managed-resource tree. The standard resource drawer renders them
  // cleanly. Keep this in sync with pkg/gitops/tree/graph.go classifyGitOpsKind.
  return false
}

function isValidKubernetesName(name: string): boolean {
  return /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name)
}

function hasAPIResource(resources: APIResource[] | undefined, name: string, group: string): boolean {
  return (resources ?? []).some((resource) => resource.name === name && resource.group === group)
}

async function fetchResourceList(kind: string, group: string, namespacesParam: string): Promise<any[]> {
  const params = new URLSearchParams()
  if (namespacesParam) params.set('namespaces', namespacesParam)
  if (group) params.set('group', group)
  const res = await fetch(apiUrl(`/resources/${kind}?${params}`), {
    credentials: getCredentialsMode(),
    headers: getAuthHeaders(),
  })
  if (res.status === 400 || res.status === 403 || res.status === 404) return []
  if (!res.ok) throw new Error(`Failed to fetch ${kind}: HTTP ${res.status}`)
  return res.json()
}

function normalizeArgoApplication(resource: any): GitOpsRow {
  const status = getGitOpsStatus('applications', resource)
  const source = resource.spec?.source ?? resource.spec?.sources?.[0] ?? {}
  const destination = resource.spec?.destination ?? {}
  return {
    id: `argoproj.io/applications/${resource.metadata?.namespace ?? ''}/${resource.metadata?.name ?? ''}`,
    mode: 'applications',
    tool: 'argo',
    kindName: 'applications',
    kind: 'Application',
    group: 'argoproj.io',
    name: resource.metadata?.name ?? '',
    namespace: resource.metadata?.namespace ?? '',
    project: resource.spec?.project ?? 'default',
    labels: resource.metadata?.labels ?? {},
    sync: status?.sync ?? resource.status?.sync?.status ?? 'Unknown',
    health: status?.health ?? resource.status?.health?.status ?? 'Unknown',
    suspended: status?.suspended ?? false,
    repository: source.repoURL ?? '',
    targetRevision: source.targetRevision ?? resource.status?.sync?.revision ?? '',
    path: source.path ?? '',
    chart: source.chart ?? '',
    destination: destination.name ?? destination.server ?? '',
    destinationNamespace: destination.namespace ?? '',
    createdAt: resource.metadata?.creationTimestamp ?? '',
    lastSync: resource.status?.operationState?.finishedAt ?? resource.status?.reconciledAt ?? '',
    autoSync: Boolean(resource.spec?.syncPolicy?.automated),
    terminating: isTerminating(resource),
    terminationStartedAt: terminationStartedAt(resource),
    raw: resource,
  }
}

function normalizeFluxKustomization(resource: any, fluxSourceUrls?: Map<string, string>): GitOpsRow {
  const status = getGitOpsStatus('kustomizations', resource)
  const sourceRef = resource.spec?.sourceRef ?? {}
  const resolvedRepo = resolveFluxSourceRepo(sourceRef, resource.metadata?.namespace, fluxSourceUrls)
  return {
    id: `kustomize.toolkit.fluxcd.io/kustomizations/${resource.metadata?.namespace ?? ''}/${resource.metadata?.name ?? ''}`,
    mode: 'applications',
    tool: 'flux',
    kindName: 'kustomizations',
    kind: 'Kustomization',
    group: 'kustomize.toolkit.fluxcd.io',
    name: resource.metadata?.name ?? '',
    namespace: resource.metadata?.namespace ?? '',
    project: resource.metadata?.labels?.['kustomize.toolkit.fluxcd.io/name'] ?? resource.metadata?.namespace ?? '',
    labels: resource.metadata?.labels ?? {},
    sync: status?.sync ?? 'Unknown',
    health: resource.spec?.suspend ? 'Suspended' : (status?.health ?? 'Unknown'),
    suspended: resource.spec?.suspend === true,
    repository: resolvedRepo,
    targetRevision: resource.status?.lastAppliedRevision ?? resource.status?.lastAttemptedRevision ?? '',
    path: resource.spec?.path ?? '',
    chart: '',
    destination: resource.spec?.kubeConfig?.secretRef?.name ? `kubeconfig/${resource.spec.kubeConfig.secretRef.name}` : 'in-cluster',
    destinationNamespace: resource.spec?.targetNamespace ?? resource.metadata?.namespace ?? '',
    createdAt: resource.metadata?.creationTimestamp ?? '',
    lastSync: newestConditionTime(resource),
    autoSync: !resource.spec?.suspend,
    terminating: isTerminating(resource),
    terminationStartedAt: terminationStartedAt(resource),
    raw: resource,
  }
}

function normalizeFluxHelmRelease(resource: any, fluxSourceUrls?: Map<string, string>): GitOpsRow {
  const status = getGitOpsStatus('helmreleases', resource)
  const chartSpec = resource.spec?.chart?.spec ?? {}
  const sourceRef = chartSpec.sourceRef ?? {}
  const resolvedRepo = resolveFluxSourceRepo(sourceRef, resource.metadata?.namespace, fluxSourceUrls)
  return {
    id: `helm.toolkit.fluxcd.io/helmreleases/${resource.metadata?.namespace ?? ''}/${resource.metadata?.name ?? ''}`,
    mode: 'applications',
    tool: 'flux',
    kindName: 'helmreleases',
    kind: 'HelmRelease',
    group: 'helm.toolkit.fluxcd.io',
    name: resource.metadata?.name ?? '',
    namespace: resource.metadata?.namespace ?? '',
    project: resource.metadata?.labels?.['helm.toolkit.fluxcd.io/name'] ?? resource.metadata?.namespace ?? '',
    labels: resource.metadata?.labels ?? {},
    sync: status?.sync ?? 'Unknown',
    health: resource.spec?.suspend ? 'Suspended' : (status?.health ?? 'Unknown'),
    suspended: resource.spec?.suspend === true,
    repository: resolvedRepo,
    targetRevision: chartSpec.version ?? resource.status?.lastAttemptedRevision ?? '',
    path: '',
    chart: chartSpec.chart ?? '',
    destination: resource.spec?.kubeConfig?.secretRef?.name ? `kubeconfig/${resource.spec.kubeConfig.secretRef.name}` : 'in-cluster',
    destinationNamespace: resource.spec?.targetNamespace ?? resource.metadata?.namespace ?? '',
    createdAt: resource.metadata?.creationTimestamp ?? '',
    lastSync: newestConditionTime(resource),
    autoSync: !resource.spec?.suspend,
    terminating: isTerminating(resource),
    terminationStartedAt: terminationStartedAt(resource),
    raw: resource,
  }
}

// buildFluxSourceUrlMap indexes Flux source CRs (GitRepository, HelmRepository,
// OCIRepository, Bucket) by "<kind>/<namespace>/<name>" so reconciler row
// normalization can resolve `spec.sourceRef` to the source's actual URL.
// Without this, the fleet "Source" column reads "GitRepository podinfo"
// (the CR's name) — same name as could appear elsewhere; useless for the
// "where does this app live?" scan. Argo Application rows already show the
// URL directly because Argo bakes it into the Application spec.
function buildFluxSourceUrlMap(sources: any[]): Map<string, string> {
  const out = new Map<string, string>()
  for (const s of sources) {
    const kind = s?.kind
    const name = s?.metadata?.name
    const namespace = s?.metadata?.namespace
    const url = s?.spec?.url
    if (!kind || !name || !namespace || !url) continue
    out.set(`${kind}/${namespace}/${name}`, url)
  }
  return out
}

// resolveFluxSourceRepo returns the source's URL when the source is in cache
// and we can resolve it. Falls back to the legacy "Kind name" string so we
// never show worse information than before. defaultNamespace handles the
// common case where sourceRef.namespace is omitted (defaults to the
// reconciler's own namespace per Flux convention).
function resolveFluxSourceRepo(sourceRef: any, defaultNamespace: string | undefined, urlMap: Map<string, string> | undefined): string {
  const legacy = [sourceRef?.kind, sourceRef?.namespace ? `${sourceRef.namespace}/` : '', sourceRef?.name].filter(Boolean).join(' ')
  if (!urlMap || !sourceRef?.kind || !sourceRef?.name) return legacy
  const ns = sourceRef.namespace || defaultNamespace || ''
  if (!ns) return legacy
  const url = urlMap.get(`${sourceRef.kind}/${ns}/${sourceRef.name}`)
  return url || legacy
}

// isTerminating reads metadata.deletionTimestamp from a raw K8s object.
// Truthy when the resource has been marked for deletion (the controller
// is processing finalizers, or finalizers are stuck and the resource is
// a zombie). The fleet view paints a small Terminating indicator on
// these rows; the detail view drives the [Terminating] chip + action
// disabling off the same signal via the insights summary.
function isTerminating(resource: any): boolean {
  return Boolean(resource?.metadata?.deletionTimestamp)
}

// terminationStartedAt extracts the RFC3339 deletion timestamp, or
// undefined when the resource isn't being deleted. Centralized so all
// three normalizers (Argo, Flux Kustomization, Flux HelmRelease) agree
// on the field path.
function terminationStartedAt(resource: any): string | undefined {
  return resource?.metadata?.deletionTimestamp || undefined
}

function newestConditionTime(resource: any): string {
  const times = (resource.status?.conditions ?? [])
    .map((condition: any) => condition.lastTransitionTime)
    .filter(Boolean)
    .sort()
  return times[times.length - 1] ?? ''
}

function toggleSet(set: Set<string>, setter: (next: Set<string>) => void, value: string) {
  const next = new Set(set)
  if (next.has(value)) next.delete(value)
  else next.add(value)
  setter(next)
}

function countValues(values: string[]) {
  const counts = new Map<string, number>()
  for (const value of values) {
    const key = value || '(none)'
    counts.set(key, (counts.get(key) ?? 0) + 1)
  }
  return [...counts.entries()]
    .map(([name, count]) => ({ name, count }))
    .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name))
}

function countMap(values: string[]) {
  const counts = new Map<string, number>()
  for (const value of values) {
    counts.set(value || 'Unknown', (counts.get(value || 'Unknown') ?? 0) + 1)
  }
  return counts
}

function countLabels(rows: GitOpsRow[]) {
  const counts = new Map<string, number>()
  for (const row of rows) {
    for (const [key, value] of Object.entries(row.labels)) {
      if (!value) continue
      if (key.includes('pod-template-hash') || key.includes('controller-revision-hash')) continue
      const pair = `${key}=${value}`
      counts.set(pair, (counts.get(pair) ?? 0) + 1)
    }
  }
  return [...counts.entries()]
    .map(([name, count]) => ({ name, count }))
    .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name))
    .slice(0, 30)
}

function compareRows(a: GitOpsRow, b: GitOpsRow, sortKey: SortKey) {
  if (sortKey === 'health') return urgencyRank(a) - urgencyRank(b) || a.name.localeCompare(b.name)
  if (sortKey === 'sync') return syncRank(a.sync) - syncRank(b.sync) || a.name.localeCompare(b.name)
  if (sortKey === 'lastSync') return (Date.parse(b.lastSync || b.createdAt) || 0) - (Date.parse(a.lastSync || a.createdAt) || 0)
  if (sortKey === 'project') return a.project.localeCompare(b.project) || a.name.localeCompare(b.name)
  return a.name.localeCompare(b.name)
}

// urgencyRank groups rows by what the operator should do about them, not by
// the raw sync/health labels. The key insight: an OutOfSync app with
// auto-sync ON is healing itself — it sorts after an OutOfSync app with no
// auto-sync (which won't heal). Suspended sorts near the bottom because it's
// intentionally non-green, not a problem to fix.
//
// Tiers:
//   0. Truly broken — Terminating, Degraded, Missing. Won't self-heal.
//   1. OutOfSync with no auto-sync. Drifted and stuck waiting for a human.
//   2. OutOfSync with auto-sync on. Healing in progress.
//   3. Progressing / Reconciling. Mid-rollout.
//   4. Unknown / other. Indeterminate state.
//   5. Suspended. Intentional non-green; bottom of the urgent half.
//   6. Synced + Healthy. Calm steady state.
function urgencyRank(row: GitOpsRow): number {
  if (row.terminating) return 0
  if (row.health === 'Degraded' || row.health === 'Missing') return 0
  if (row.sync === 'OutOfSync' && !row.autoSync) return 1
  if (row.sync === 'OutOfSync') return 2
  if (row.health === 'Progressing' || row.sync === 'Reconciling') return 3
  if (row.suspended || row.health === 'Suspended') return 5
  if (row.health === 'Healthy' && row.sync === 'Synced') return 6
  return 4
}

function syncRank(sync: string) {
  return { OutOfSync: 0, Reconciling: 1, Unknown: 2, Synced: 3 }[sync] ?? 2
}

function modeLabel(mode: GitOpsMode) {
  return {
    applications: 'Applications',
    sources: 'Sources',
    projects: 'Projects',
    alerts: 'Alerts',
  }[mode]
}

function statusStripe(row: GitOpsRow) {
  // Lifecycle dominates: a Terminating resource paints orange regardless of
  // its (now stale) sync/health values. Without this guard, a row with
  // sync=OutOfSync + terminating=true paints amber and reads as "needs
  // sync attention" — but the resource is being deleted, so sync is moot.
  if (row.terminating) return 'bg-orange-500'
  if (row.health === 'Degraded') return 'bg-red-500'
  if (row.health === 'Progressing' || row.sync === 'Reconciling') return 'bg-sky-500'
  if (row.sync === 'OutOfSync') return 'bg-amber-500'
  if (row.health === 'Healthy' && row.sync === 'Synced') return 'bg-emerald-500'
  return 'bg-theme-text-tertiary'
}

// insightChangeKey produces the same key shape that GitOpsChangesView uses
// for its row keys, so we can pinpoint which row to scroll/highlight when
// the user clicks an alert. Keep in sync with the row key in
// GitOpsChangesView (kind/namespace/name; group is intentionally omitted
// because issue refs may not carry it).
function insightChangeKey(ref: { kind: string; namespace?: string; name: string }): string {
  return `${ref.kind}/${ref.namespace || ''}/${ref.name}`
}

function formatRelative(value: string) {
  return formatRelativeAgeTime(value)
}

function SummaryTile({ label, value, tone = 'neutral' }: { label: string; value: number; tone?: 'neutral' | 'warning' | 'error' | 'info' }) {
  const toneClass = {
    neutral: 'text-theme-text-primary',
    warning: 'text-amber-600 dark:text-amber-300',
    error: 'text-red-600 dark:text-red-300',
    info: 'text-sky-600 dark:text-sky-300',
  }[tone]
  return (
    <div className="rounded-md border border-theme-border bg-theme-base px-3 py-2">
      <div className={`text-sm font-semibold ${toneClass}`}>{value}</div>
      <div className="text-xs text-theme-text-tertiary">{label}</div>
    </div>
  )
}

function ActionButton({
  label,
  description,
  icon: Icon,
  loading,
  disabled,
  disabledReason,
  danger,
  primary,
  onClick,
}: {
  label: string
  description?: string
  icon: ComponentType<{ className?: string }>
  loading?: boolean
  disabled?: boolean
  // Replaces the tooltip when the button is disabled. The user otherwise
  // hits a greyed-out button with no explanation — "Suspend (action label)"
  // tells them nothing about *why* it's disabled. With this set the tooltip
  // becomes "Cannot reconcile a resource pending deletion" or similar.
  disabledReason?: string
  danger?: boolean
  primary?: boolean
  onClick: () => void
}) {
  // primary → brand fill (one per page); danger → red (destructive);
  // default → bordered ghost on theme surface (secondary actions).
  const variantClass = primary
    ? 'btn-brand'
    : danger
      ? 'border border-red-500/40 bg-red-500/10 text-red-500 hover:bg-red-500/20'
      : 'border border-theme-border bg-theme-surface text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
  const tooltip = disabled && disabledReason ? disabledReason : (description || label)
  return (
    <Tooltip content={tooltip}>
      <button
        type="button"
        onClick={onClick}
        disabled={loading || disabled}
        className={`inline-flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${variantClass}`}
      >
        {loading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Icon className="h-3.5 w-3.5" />}
        {label}
      </button>
    </Tooltip>
  )
}


// describeTerminating produces two complementary strings:
//   - chipTooltip: full context for the Terminating chip's hover (finalizers
//     + age + why disabled). Wraps gracefully in the tooltip's max-w-xs
//     layout so multiple sentences are fine.
//   - actionDisabledTooltip: one tight sentence for disabled action buttons
//     so the tooltip stays small and anchored near the button. The full
//     explanation already lives in the lifecycle banner above; the
//     button tooltip just needs to say "you can't do this right now,
//     here's why in one line".
//
// Examples:
//   chipTooltip: "Pending deletion 21d ago. Finalizers: finalizers.fluxcd.io.
//                 Mutating actions are disabled until cleanup completes."
//   actionDisabledTooltip: "Disabled — resource is pending deletion (21d)"
function describeTerminating(summary?: { terminationStartedAt?: string; finalizers?: string[] }): {
  chipTooltip: string
  actionDisabledTooltip: string
} {
  const ageText = formatRelativeAge(summary?.terminationStartedAt)
  const ageSuffix = ageText ? ` ${ageText} ago` : ''
  const finalizers = summary?.finalizers ?? []
  const finSuffix = finalizers.length > 0 ? ` Finalizers: ${finalizers.join(', ')}.` : ''
  const ageInline = ageText ? ` (${ageText})` : ''
  return {
    chipTooltip: `Pending deletion${ageSuffix}.${finSuffix} Mutating actions are disabled until cleanup completes.`,
    actionDisabledTooltip: `Disabled — resource is pending deletion${ageInline}`,
  }
}

// formatRelativeAge — small inline relative-time formatter. Tier
// breakpoints kept in sync with pkg/gitops/insights/insights.go::formatAgeShort
// and pkg/audit/checks.go::formatDurationShort so UI, lifecycle Issue
// messages, and audit findings agree on units. Adding a new tier (e.g.
// "weeks") in one and not the others would let the same duration render
// differently across surfaces. Returns "" when the input can't be parsed;
// callers should treat empty as "no timestamp" and skip the age suffix
// gracefully.
function formatRelativeAge(rfc3339?: string): string {
  return formatCompactAge(rfc3339)
}

// Parse an Argo HistoryItem.id into the int64 the rollback API needs.
// Returns null when the id is missing, non-numeric (Flux condition rows
// reuse the slot for condition.type), or non-positive. Number("") is 0
// which passes Number.isFinite — guard with > 0 explicitly.
function parseRollbackID(id: string | undefined): number | null {
  if (!id) return null
  const n = Number(id)
  if (!Number.isFinite(n) || n <= 0) return null
  return n
}

// Inline counts for the topology toolbar — answers "how many resources, how
// many of them are healthy / drifted" at a glance, without making the user
// count facets in the filter rail.
function TopologyCounts({ tree }: { tree: GitOpsResourceTree }) {
  const nodes = (tree.nodes ?? []).filter((n) => n.role !== 'group' && n.role !== 'root')
  const total = nodes.length
  if (total === 0) return null
  const healthy = nodes.filter((n) => (n.health || '').toLowerCase() === 'healthy').length
  const degraded = nodes.filter((n) => {
    const h = (n.health || '').toLowerCase()
    return h === 'degraded' || h === 'missing' || h === 'unhealthy'
  }).length
  const outOfSync = nodes.filter((n) => (n.sync || '').toLowerCase() === 'outofsync').length
  return (
    <div className="hidden min-w-0 flex-1 items-center gap-3 truncate text-[11px] text-theme-text-tertiary sm:flex">
      <span><span className="text-theme-text-primary">{total}</span> resources</span>
      {healthy > 0 && <span className="flex items-center gap-1"><span className="h-1.5 w-1.5 rounded-full bg-emerald-500" /> {healthy} healthy</span>}
      {/* Bad-news counts use status colors on the number itself so the worst
          fact in the row visually pops, not just the dot next to it. */}
      {degraded > 0 && <span className="flex items-center gap-1 font-medium text-red-600 dark:text-red-400"><span className="h-1.5 w-1.5 rounded-full bg-red-500" /> {degraded} degraded</span>}
      {outOfSync > 0 && <span className="flex items-center gap-1 font-medium text-amber-700 dark:text-amber-400"><span className="h-1.5 w-1.5 rounded-full bg-amber-500" /> {outOfSync} out of sync</span>}
    </div>
  )
}

function summarizeGitOpsRows(rows: GitOpsRow[]) {
  return rows.reduce((summary, row) => {
    if (row.sync === 'OutOfSync') summary.outOfSync++
    if (row.health === 'Degraded') summary.degraded++
    if (row.health === 'Healthy') summary.healthy++
    if (row.health === 'Progressing') summary.progressing++
    if (row.suspended) summary.suspended++
    if (row.sync === 'Reconciling' || row.health === 'Progressing') summary.reconciling++
    return summary
  }, { outOfSync: 0, degraded: 0, healthy: 0, progressing: 0, suspended: 0, reconciling: 0 })
}

function getGitOpsStatus(kind: string, resource: any): GitOpsStatus | null {
  if (kind === 'applications') {
    return argoStatusToGitOpsStatus(resource.status ?? {})
  }
  const conditions = (resource.status?.conditions ?? []) as FluxCondition[]
  return fluxConditionsToGitOpsStatus(conditions, resource.spec?.suspend === true)
}

function getTool(kind: string, group?: string): 'argo' | 'flux' {
  if (group === 'argoproj.io' || kind === 'applications' || kind === 'applicationsets' || kind === 'appprojects') return 'argo'
  return 'flux'
}
