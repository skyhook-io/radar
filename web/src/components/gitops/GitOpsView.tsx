import { useEffect, useMemo, useRef, useState, type ComponentType, type ReactNode } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { clsx } from 'clsx'
import { CheckCircle2, CircleAlert, CircleDot, Clock3, GitBranch, GitCommit, HeartPulse, LayoutGrid, List, Loader2, Pause, Play, RefreshCw, RotateCw, Search, Table2, Tag, XCircle } from 'lucide-react'
import {
  GitOpsActivityInsightView,
  GitOpsChangesView,
  GitOpsIssuesBand,
  GitOpsTreeGraph,
  GitOpsStatusStrip,
  HealthStatusBadge,
  StatusDot,
  SyncStatusBadge,
  initNavigationMap,
  kindToPlural,
  mapHealthToTone,
  type APIResource,
  type GitOpsResourceTree,
  type GitOpsInsightRef,
  type GitOpsTreeFilters,
  type GitOpsTreeRef,
  type GitOpsTreeNode,
  type GitOpsTreePreset,
  type SelectedResource,
} from '@skyhook-io/k8s-ui'
import {
  argoStatusToGitOpsStatus,
  fluxConditionsToGitOpsStatus,
  type FluxCondition,
  type GitOpsStatus,
} from '@skyhook-io/k8s-ui/types/gitops'

import {
  fetchJSON,
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
      const [applications, kustomizations, helmReleases] = await Promise.all([
        hasApplications ? fetchResourceList('applications', 'argoproj.io', namespacesParam) : Promise.resolve([]),
        hasKustomizations ? fetchResourceList('kustomizations', 'kustomize.toolkit.fluxcd.io', namespacesParam) : Promise.resolve([]),
        hasHelmReleases ? fetchResourceList('helmreleases', 'helm.toolkit.fluxcd.io', namespacesParam) : Promise.resolve([]),
      ])
      return [
        ...applications.map((r) => normalizeArgoApplication(r)),
        ...kustomizations.map((r) => normalizeFluxKustomization(r)),
        ...helmReleases.map((r) => normalizeFluxHelmRelease(r)),
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
      return true
    })
    return [...rows].sort((a, b) => compareRows(a, b, sortKey))
  }, [allRows, automationFilter, healthFilters, labelFilters, mode, namespaceFilters, projectFilters, search, sortKey, syncFilters])

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
        <FilterSection icon={GitBranch} title="Scope">
          <div className="grid grid-cols-2 gap-1">
            {(['applications', 'sources', 'projects', 'alerts'] as GitOpsMode[]).map((item) => (
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
            className="cursor-pointer border-b border-theme-border bg-theme-base hover:bg-theme-hover"
          >
            <TableCell>
              <div className="flex min-w-0 items-center gap-2">
                <span className={`h-8 w-1 shrink-0 rounded-full ${statusStripe(row)}`} />
                <div className="min-w-0">
                  <div className="truncate font-medium text-theme-text-primary">{row.name}</div>
                  <div className="truncate text-xs text-theme-text-tertiary">{row.tool === 'argo' ? 'ArgoCD' : 'FluxCD'} {row.kind}</div>
                </div>
              </div>
            </TableCell>
            <TableCell>{row.project || '-'}</TableCell>
            <TableCell><SyncStatusBadge sync={row.sync as any} suspended={row.suspended} /></TableCell>
            <TableCell><HealthStatusBadge health={row.health as any} /></TableCell>
            <TableCell>
              <div className="truncate text-theme-text-primary">{row.repository || row.chart || '-'}</div>
              <div className="truncate text-xs text-theme-text-tertiary">{[row.targetRevision, row.path || row.chart].filter(Boolean).join(' · ') || '-'}</div>
            </TableCell>
            <TableCell>
              <div className="truncate text-theme-text-primary">{row.destination || '-'}</div>
              <div className="truncate text-xs text-theme-text-tertiary">{row.destinationNamespace || row.namespace || '-'}</div>
            </TableCell>
            <TableCell>{formatRelative(row.lastSync || row.createdAt)}</TableCell>
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
      className="group relative flex min-w-0 flex-col overflow-hidden rounded-md border border-theme-border bg-theme-surface text-left shadow-theme-sm transition-all hover:border-theme-text-tertiary/40 hover:shadow-theme-md"
    >
      {/* Top accent strip — sync-state color, sole color above the badge row */}
      <div className={clsx('h-1 w-full', statusStripe(row))} />
      <div className="flex flex-1 flex-col gap-3 px-4 pb-4 pt-3">
        {/* Tier 1 — name. Wrap up to 2 lines, then break-words to avoid clipping. */}
        <div className="line-clamp-2 break-all text-[15px] font-semibold leading-tight text-theme-text-primary">
          {row.name}
        </div>
        {/* Tier 2 — sync + health badges, the secondary scan target */}
        <div className="flex flex-wrap gap-1.5">
          <SyncStatusBadge sync={row.sync as any} suspended={row.suspended} />
          <HealthStatusBadge health={row.health as any} />
        </div>
        {/* Tier 3 — source / revision / recency. The operational answers. */}
        <div className="flex flex-col gap-1 text-[12px]">
          {source && (
            <div className="truncate text-theme-text-secondary">{source}</div>
          )}
          {revision && (
            <div className="truncate font-mono text-[11px] text-theme-text-tertiary">{shortRevision(revision)}</div>
          )}
          {lastSyncRaw && (
            <div className={clsx('font-medium', recencyClass)}>{formatRelative(lastSyncRaw)}</div>
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
type TopologyMode = 'graph' | 'table'

function GitOpsDetailView({ namespaces, onOpenResource }: GitOpsViewProps) {
  const location = useLocation()
  const navigate = useNavigate()
  const parts = location.pathname.split('/').filter(Boolean)
  const kind = parts[2] || 'applications'
  const namespace = parts[3] === '_' ? '' : decodePathPart(parts[3] || '')
  const name = decodePathPart(parts[4] || '')
  const group = new URLSearchParams(location.search).get('apiGroup') || (KIND_BY_NAME.get(kind)?.group ?? '')
  const apiKind = KIND_BY_NAME.get(kind)

  const resourceQ = useResource<any>(kind, namespace, name, group)
  const treeQ = useGitOpsTree(kind, namespace, name, group, namespaces)
  const insightsQ = useGitOpsInsights(kind, namespace, name, group, namespaces)
  const status = resourceQ.data ? getGitOpsStatus(kind, resourceQ.data) : null
  const tool = getTool(kind, group)
  const [appView, setAppView] = useState<GitOpsAppView>('topology')
  // When the user clicks an actionable issue alert ("OutOfSync — NodePool
  // default is out of sync · View →"), we navigate to Changes and focus
  // that resource. The ref is stringified to a stable key so GitOpsChangesView
  // can find and scroll it; cleared after a few seconds so the highlight
  // doesn't persist past its purpose.
  const [changesFocusKey, setChangesFocusKey] = useState<string | null>(null)
  const [topologyMode, setTopologyMode] = useState<TopologyMode>('graph')
  const [graphPreset, setGraphPreset] = useState<GitOpsTreePreset>('compact')
  const [graphSearch, setGraphSearch] = useState('')
  const [graphKinds, setGraphKinds] = useState<Set<string>>(new Set())
  const [graphSync, setGraphSync] = useState<Set<string>>(new Set())
  const [graphHealth, setGraphHealth] = useState<Set<string>>(new Set())
  const [graphNamespaces, setGraphNamespaces] = useState<Set<string>>(new Set())
  const [graphRoles, setGraphRoles] = useState<Set<string>>(new Set())
  const [graphFullscreen, setGraphFullscreen] = useState(false)

  const argoSync = useArgoSync()
  const argoRefresh = useArgoRefresh()
  const argoTerminate = useArgoTerminate()
  const argoSuspend = useArgoSuspend()
  const argoResume = useArgoResume()
  const argoRollback = useArgoRollback()
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
  const graphFilters = useMemo<GitOpsTreeFilters>(() => ({
    kinds: graphKinds,
    sync: graphSync,
    health: graphHealth,
    namespaces: graphNamespaces,
    roles: graphRoles,
  }), [graphHealth, graphKinds, graphNamespaces, graphRoles, graphSync])
  const resourceNodes = useMemo(() => filterTreeNodes(tree, graphSearch, graphFilters), [tree, graphFilters, graphSearch])
  const graphFacets = useMemo(() => buildTreeFacets(tree), [tree])

  function openResourceFromTree(ref: GitOpsTreeRef | GitOpsInsightRef) {
    if (isGitOpsDetailRef(ref) && isValidKubernetesName(ref.name)) {
      const detailKind = kindToPlural(ref.kind)
      const params = new URLSearchParams()
      if (ref.group) params.set('apiGroup', ref.group)
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
      if (status?.suspended) return
      if (isArgoApp) setSyncDialogOpen(true)
      else if (isFlux) fluxReconcile.mutate({ kind, namespace, name })
    },
    enabled: shortcutsEnabled && (isArgoApp || isFlux),
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
            <div className="mb-1.5 flex items-center gap-1.5 text-xs">
              <button type="button" onClick={() => navigate('/gitops')} className="text-sky-500 hover:text-sky-400">
                GitOps
              </button>
              <span className="text-theme-text-tertiary">/</span>
              <span className="font-medium text-theme-text-primary">{tool === 'argo' ? 'ArgoCD' : 'FluxCD'}</span>
              <span className="text-theme-text-tertiary">/</span>
              <span className="font-medium text-theme-text-primary">{apiKind?.kind ?? kind}</span>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="min-w-0 truncate text-lg font-semibold text-theme-text-primary">
                {namespace ? `${namespace}/` : ''}{name}
              </h1>
              {status && (
                <>
                  <SyncStatusBadge sync={status.sync} suspended={status.suspended} />
                  <HealthStatusBadge health={status.health} />
                </>
              )}
            </div>
            {/* Header carries the *spec/identity* facts — Project + Destination
                are who-and-where, not status. Source + Revision live in the
                status strip below where they show *live* values; surfacing
                them here too created visual duplication between target spec
                and observed state. */}
            {/* No width cap: each fact sizes to its content and the row wraps
                naturally at narrow viewports. Capping at max-w-5xl forced
                truncation on full-width screens that had room to spare. */}
            <div className="mt-2 flex flex-wrap gap-x-5 gap-y-0.5 text-[11px] text-theme-text-tertiary">
              <AppFact label="Project" value={detailRow?.project || '-'} />
              <AppFact label="Destination" value={[detailRow?.destination, detailRow?.destinationNamespace].filter(Boolean).join(' / ') || '-'} />
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {isArgoApp && (
              <>
                <ActionButton label="Sync…" icon={RefreshCw} loading={argoSync.isPending} onClick={() => setSyncDialogOpen(true)} disabled={status?.suspended} primary />
                <ActionButton
                  label="Refresh"
                  icon={RotateCw}
                  loading={argoRefresh.isPending && refreshKind === 'normal'}
                  onClick={() => {
                    setRefreshKind('normal')
                    argoRefresh.mutate({ namespace, name, hard: false })
                  }}
                />
                <ActionButton
                  label="Hard refresh"
                  icon={RotateCw}
                  loading={argoRefresh.isPending && refreshKind === 'hard'}
                  onClick={() => {
                    setRefreshKind('hard')
                    argoRefresh.mutate({ namespace, name, hard: true })
                  }}
                />
                {isRunning && <ActionButton label="Terminate" icon={XCircle} loading={argoTerminate.isPending} onClick={() => argoTerminate.mutate({ namespace, name })} danger />}
                {status?.suspended
                  ? <ActionButton label="Enable auto-sync" icon={Play} loading={argoResume.isPending} onClick={() => argoResume.mutate({ namespace, name })} />
                  : <ActionButton label="Disable auto-sync" icon={Pause} loading={argoSuspend.isPending} onClick={() => argoSuspend.mutate({ namespace, name })} />}
              </>
            )}
            {isFlux && (
              <>
                <ActionButton label="Reconcile" icon={RefreshCw} loading={fluxReconcile.isPending} onClick={() => fluxReconcile.mutate({ kind, namespace, name })} disabled={status?.suspended} primary />
                {isFluxWorkload && (
                  <ActionButton
                    label="Sync with source"
                    icon={GitCommit}
                    loading={fluxSyncWithSource.isPending}
                    onClick={() => fluxSyncWithSource.mutate({ kind, namespace, name })}
                  />
                )}
                {status?.suspended
                  ? <ActionButton label="Resume" icon={Play} loading={fluxResume.isPending} onClick={() => fluxResume.mutate({ kind, namespace, name })} />
                  : <ActionButton label="Suspend" icon={Pause} loading={fluxSuspend.isPending} onClick={() => fluxSuspend.mutate({ kind, namespace, name })} />}
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
          />
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
              <ViewButton active={appView === 'changes'} icon={GitCommit} label="Changes" onClick={() => setAppView('changes')} />
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
                  {/* Same filter rail; toggle picks the rendering. */}
                  <div className="flex items-center gap-1 rounded-md border border-theme-border bg-theme-surface p-0.5">
                    <ViewButton active={topologyMode === 'graph'} icon={GitBranch} label="Graph" onClick={() => setTopologyMode('graph')} />
                    <ViewButton active={topologyMode === 'table'} icon={Table2} label="Table" onClick={() => setTopologyMode('table')} />
                  </div>
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
                  {topologyMode === 'graph' && (
                    <button
                      type="button"
                      onClick={() => setGraphFullscreen(!graphFullscreen)}
                      className="rounded-md border border-theme-border bg-theme-surface px-2 py-1 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
                    >
                      {graphFullscreen ? 'Exit fullscreen' : 'Fullscreen'}
                    </button>
                  )}
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
                {topologyMode === 'graph' ? (
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
                ) : (
                  <GitOpsResourceTable
                    nodes={resourceNodes}
                    capabilities={insightsQ.data?.capabilities}
                    selectiveLoading={argoSync.isPending}
                    onSelectiveSync={(refs) => argoSync.mutate({ namespace, name, resources: refs })}
                    onOpen={openResourceFromTree}
                  />
                )}
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
              // The Rollback button is gated on parseRollbackID returning a
              // value, so by the time we get here `rollbackTarget?.id` is a
              // positive integer. Defensive parse anyway in case the row data
              // refreshed mid-modal.
              const id = parseRollbackID(rollbackTarget?.id)
              if (id == null) return
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

// Kinds where Argo can't compute meaningful health — Health=Unknown for these
// is *expected absence*, not a problem. Rendering them like a real Unknown
// signal floods the table with apparent issues. We render them as a quieter
// gray tone with a "no health computation available for this kind" tooltip.
const HEALTHLESS_KINDS = new Set([
  'ServiceAccount', 'ConfigMap', 'Secret',
  'Role', 'ClusterRole', 'RoleBinding', 'ClusterRoleBinding',
  'CustomResourceDefinition', 'Namespace',
  'ResourceQuota', 'LimitRange',
  'PriorityClass', 'RuntimeClass',
  'StorageClass', 'PersistentVolume',
  'NetworkPolicy', 'PodDisruptionBudget',
  'IngressClass', 'GatewayClass',
  'ValidatingWebhookConfiguration', 'MutatingWebhookConfiguration',
  'Lease',
])

// Sort priority so problems land at the top by default. Lower = more severe.
// OutOfSync (drift, requires action) ranks above any health state because it's
// the GitOps-specific signal; within sync states, health-failure rises.
const SYNC_PRIORITY: Record<string, number> = {
  OutOfSync: 0,
  Missing: 1,
  Unknown: 2,
  Synced: 3,
  Pruned: 4,
}
const HEALTH_PRIORITY: Record<string, number> = {
  Degraded: 0,
  Missing: 1,
  Unknown: 2,
  Progressing: 3,
  Suspended: 4,
  Healthy: 5,
}

type SortColumn = 'status' | 'name' | 'namespace' | 'age'
type SortDir = 'asc' | 'desc'

function nodeSyncRank(s?: string): number {
  return SYNC_PRIORITY[s ?? ''] ?? 2
}
function nodeHealthRank(h?: string, kind?: string): number {
  // Health=Unknown on a kind that doesn't report health is not a problem;
  // sink it below Healthy so it doesn't drag healthless rows to the top.
  if ((!h || h === 'Unknown') && kind && HEALTHLESS_KINDS.has(kind)) return 6
  return HEALTH_PRIORITY[h ?? ''] ?? 2
}

function compareNodes(a: GitOpsTreeNode, b: GitOpsTreeNode, col: SortColumn, dir: SortDir): number {
  const mul = dir === 'asc' ? 1 : -1
  let cmp = 0
  switch (col) {
    case 'status': {
      // Composite: sync first, then health within same sync. "Problems first"
      // is the asc direction (lower priority = more severe).
      cmp = nodeSyncRank(a.sync) - nodeSyncRank(b.sync)
      if (cmp === 0) cmp = nodeHealthRank(a.health, a.ref.kind) - nodeHealthRank(b.health, b.ref.kind)
      if (cmp === 0) cmp = (a.ref.kind || '').localeCompare(b.ref.kind || '')
      if (cmp === 0) cmp = (a.ref.name || '').localeCompare(b.ref.name || '')
      return cmp * mul
    }
    case 'name':
      return ((a.ref.kind || '') + a.ref.name).localeCompare((b.ref.kind || '') + b.ref.name) * mul
    case 'namespace':
      return ((a.ref.namespace || '') + a.ref.kind + a.ref.name).localeCompare((b.ref.namespace || '') + b.ref.kind + b.ref.name) * mul
    case 'age': {
      const at = typeof a.data?.createdAt === 'string' ? Date.parse(a.data.createdAt) : 0
      const bt = typeof b.data?.createdAt === 'string' ? Date.parse(b.data.createdAt) : 0
      // Newer first when desc — invert because larger timestamp = newer
      return (bt - at) * mul
    }
  }
}

function GitOpsResourceTable({
  nodes,
  capabilities,
  selectiveLoading,
  onSelectiveSync,
  onOpen,
}: {
  nodes: GitOpsTreeNode[]
  capabilities?: { selectiveSync?: boolean; unsupportedReason?: string; warnings?: string[] }
  selectiveLoading?: boolean
  onSelectiveSync?: (refs: Array<{ group?: string; kind: string; namespace?: string; name: string }>) => void
  onOpen: (ref: GitOpsTreeRef, node: GitOpsTreeNode) => void
}) {
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [sortCol, setSortCol] = useState<SortColumn>('status')
  const [sortDir, setSortDir] = useState<SortDir>('asc')
  const [groupByKind, setGroupByKind] = useState(false)
  const baseRows = nodes.filter((node) => node.role !== 'root' && node.role !== 'group')
  const visibleIDs = useMemo(() => new Set(baseRows.map((node) => node.id)), [baseRows])
  const selectedRows = baseRows.filter((node) => selected.has(node.id))
  useEffect(() => {
    setSelected((prev) => {
      const next = new Set([...prev].filter((id) => visibleIDs.has(id)))
      return next.size === prev.size ? prev : next
    })
  }, [visibleIDs])
  // Drop the namespace column entirely when nothing in the current view is
  // namespaced — most cluster-addon apps are 100% cluster-scoped and the
  // empty column is dead weight.
  const showNamespaceColumn = useMemo(() => baseRows.some((n) => !!n.ref.namespace), [baseRows])
  // Sort once for the flat view. Grouped view sorts within each kind group
  // so the same priority order applies.
  const sortedRows = useMemo(() => [...baseRows].sort((a, b) => compareNodes(a, b, sortCol, sortDir)), [baseRows, sortCol, sortDir])
  // Group by kind: stable iteration preserves the sort order within groups.
  // Group order itself follows the most severe row in each group so groups
  // with problems surface above all-Synced groups.
  const grouped = useMemo(() => {
    if (!groupByKind) return null
    const map = new Map<string, GitOpsTreeNode[]>()
    for (const row of sortedRows) {
      const k = row.ref.kind || 'Unknown'
      if (!map.has(k)) map.set(k, [])
      map.get(k)!.push(row)
    }
    const groups = Array.from(map.entries()).map(([kind, rows]) => ({
      kind,
      rows,
      worstRank: Math.min(...rows.map((r) => nodeSyncRank(r.sync) * 10 + nodeHealthRank(r.health, r.ref.kind))),
    }))
    groups.sort((a, b) => a.worstRank - b.worstRank)
    return groups
  }, [groupByKind, sortedRows])
  if (baseRows.length === 0) {
    return <div className="flex h-full items-center justify-center text-sm text-theme-text-secondary">No resources match the current filters.</div>
  }
  return (
    <div className="flex h-full min-h-0 flex-col bg-theme-base">
      {/* Toolbar: sort/group controls live above the table so the user can
          retune what's visible without scrolling away to a side rail. */}
      <div className="flex shrink-0 items-center gap-2 border-b border-theme-border bg-theme-surface px-3 py-1.5 text-[11px] text-theme-text-tertiary">
        <span>{baseRows.length} resources</span>
        <span className="text-theme-text-tertiary/50">·</span>
        <button
          type="button"
          onClick={() => setGroupByKind((v) => !v)}
          className={clsx(
            'rounded px-2 py-0.5 text-[11px] transition-colors',
            groupByKind
              ? 'bg-theme-elevated text-theme-text-primary'
              : 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary',
          )}
        >
          Group by kind {groupByKind ? '✓' : ''}
        </button>
        <span className="ml-auto text-theme-text-tertiary">Sort: {sortCol === 'status' ? 'problems first' : sortCol} ({sortDir === 'asc' ? 'asc' : 'desc'})</span>
        {sortCol !== 'status' && (
          <button
            type="button"
            onClick={() => { setSortCol('status'); setSortDir('asc') }}
            className="rounded px-2 py-0.5 text-[11px] text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
          >
            Reset sort
          </button>
        )}
      </div>
      {selectedRows.length > 0 && (
        <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-theme-border bg-theme-surface px-3 py-2 text-xs">
          <span className="font-medium text-theme-text-primary">{selectedRows.length} selected</span>
          <button
            type="button"
            onClick={() => setSelected(new Set())}
            className="rounded px-2 py-1 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
          >
            Clear
          </button>
          <Tooltip
            content={capabilities?.selectiveSync ? 'Sync selected resources' : capabilities?.unsupportedReason || 'Selective sync is not supported for this GitOps tool'}
            delay={120}
            wrapperClassName="ml-auto"
          >
            <button
              type="button"
              disabled={!capabilities?.selectiveSync || selectiveLoading}
              onClick={() => onSelectiveSync?.(selectedRows.map((node) => ({
                group: node.ref.group,
                kind: node.ref.kind,
                namespace: node.ref.namespace,
                name: node.ref.name,
              })))}
              className="rounded-md border border-theme-border bg-theme-base px-2 py-1 font-medium text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary disabled:cursor-not-allowed disabled:opacity-50"
            >
              {selectiveLoading ? 'Syncing...' : 'Sync selected'}
            </button>
          </Tooltip>
          {capabilities?.warnings?.[0] && <span className="basis-full text-[11px] text-theme-text-tertiary">{capabilities.warnings[0]}</span>}
        </div>
      )}
      <div className="min-h-0 flex-1 overflow-auto">
        <table className="w-full table-fixed border-separate border-spacing-0 text-sm">
          <thead className="sticky top-0 z-10 bg-theme-surface">
            <tr className="text-left text-[11px] uppercase tracking-wide text-theme-text-tertiary">
              <TableHead className="w-[44px]">
                <input
                  type="checkbox"
                  checked={baseRows.length > 0 && selectedRows.length === baseRows.length}
                  ref={(input) => {
                    if (input) input.indeterminate = selectedRows.length > 0 && selectedRows.length < baseRows.length
                  }}
                  onChange={(event) => setSelected(event.target.checked ? new Set(baseRows.map((node) => node.id)) : new Set())}
                  className="h-3.5 w-3.5"
                />
              </TableHead>
              <SortableHead label="Status" col="status" sortCol={sortCol} sortDir={sortDir} onSort={(c, d) => { setSortCol(c); setSortDir(d) }} className="w-[110px]" />
              <SortableHead label="Resource" col="name" sortCol={sortCol} sortDir={sortDir} onSort={(c, d) => { setSortCol(c); setSortDir(d) }} className={showNamespaceColumn ? 'w-[50%]' : 'w-[65%]'} />
              {showNamespaceColumn && (
                <SortableHead label="Namespace" col="namespace" sortCol={sortCol} sortDir={sortDir} onSort={(c, d) => { setSortCol(c); setSortDir(d) }} className="w-[20%]" />
              )}
              <SortableHead label="Age" col="age" sortCol={sortCol} sortDir={sortDir} onSort={(c, d) => { setSortCol(c); setSortDir(d) }} className="w-[100px]" />
            </tr>
          </thead>
          <tbody>
            {grouped
              ? grouped.flatMap((group) => [
                  <tr key={`group-${group.kind}`} className="bg-theme-base/40">
                    <td colSpan={4 + (showNamespaceColumn ? 1 : 0)} className="border-b border-theme-border px-3 py-1 text-[11px] font-semibold uppercase tracking-wider text-theme-text-secondary">
                      {group.kind} <span className="ml-1 text-theme-text-tertiary">({group.rows.length})</span>
                      {(() => {
                        const issues = group.rows.filter((r) => nodeSyncRank(r.sync) < 3 || (nodeHealthRank(r.health, r.ref.kind) < 3))
                        return issues.length > 0 ? (
                          <span className="ml-2 text-amber-600 dark:text-amber-400">· {issues.length} need{issues.length === 1 ? 's' : ''} attention</span>
                        ) : null
                      })()}
                    </td>
                  </tr>,
                  ...group.rows.map((node) => (
                    <ResourceRow
                      key={node.id}
                      node={node}
                      selected={selected}
                      setSelected={setSelected}
                      onOpen={onOpen}
                      showNamespaceColumn={showNamespaceColumn}
                    />
                  )),
                ])
              : sortedRows.map((node) => (
                  <ResourceRow
                    key={node.id}
                    node={node}
                    selected={selected}
                    setSelected={setSelected}
                    onOpen={onOpen}
                    showNamespaceColumn={showNamespaceColumn}
                  />
                ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

// SortableHead: clickable column header. Three-state cycle:
//   asc → desc → reset to default sort (status/asc)
// Reset doesn't return to a column-specific default; the user just gets the
// same "problems first" view they started with, which is almost always what
// they want when they decide they're done sorting by something else.
function SortableHead({
  label,
  col,
  sortCol,
  sortDir,
  onSort,
  className,
}: {
  label: string
  col: SortColumn
  sortCol: SortColumn
  sortDir: SortDir
  onSort: (col: SortColumn, dir: SortDir) => void
  className?: string
}) {
  const active = sortCol === col
  return (
    <TableHead className={className}>
      <button
        type="button"
        onClick={() => {
          if (!active) onSort(col, 'asc')
          else if (sortDir === 'asc') onSort(col, 'desc')
          else onSort('status', 'asc')
        }}
        className={clsx(
          'inline-flex items-center gap-1 text-left text-[11px] uppercase tracking-wide transition-colors',
          active ? 'text-theme-text-primary' : 'text-theme-text-tertiary hover:text-theme-text-secondary',
        )}
      >
        {label}
        <span className="text-[10px]">
          {active ? (sortDir === 'asc' ? '▲' : '▼') : '⇅'}
        </span>
      </button>
    </TableHead>
  )
}

// ResourceRow renders a single resource. Key visual decisions:
//   - Status column has both sync + health dots side by side. Tooltip on each
//     dot carries the full text. This is the primary scan target.
//   - Resource column merges Kind (small mono prefix) + Name (medium weight)
//     so the user sees the kind context without burning a column on it.
//   - Healthless kinds get a quieter Unknown treatment (slate dot, not amber)
//     and a tooltip explaining why.
function ResourceRow({
  node,
  selected,
  setSelected,
  onOpen,
  showNamespaceColumn,
}: {
  node: GitOpsTreeNode
  selected: Set<string>
  setSelected: React.Dispatch<React.SetStateAction<Set<string>>>
  onOpen: (ref: GitOpsTreeRef, node: GitOpsTreeNode) => void
  showNamespaceColumn: boolean
}) {
  const sync = node.sync || 'Unknown'
  const rawHealth = node.health || node.topologyStatus || 'Unknown'
  const isHealthless = HEALTHLESS_KINDS.has(node.ref.kind)
  const healthDisplay = isHealthless && rawHealth === 'Unknown' ? '—' : rawHealth
  const healthTone = isHealthless && rawHealth === 'Unknown'
    ? 'unknown'
    : mapHealthToTone(rawHealth)
  const syncTone = syncToneOf(sync)
  const created = typeof node.data?.createdAt === 'string' ? node.data.createdAt : ''
  return (
    <tr
      onClick={() => onOpen(node.ref, node)}
      className="group cursor-pointer bg-theme-base hover:bg-theme-hover/60"
    >
      <TableCell>
        <input
          type="checkbox"
          checked={selected.has(node.id)}
          onClick={(event) => event.stopPropagation()}
          onChange={(event) => {
            setSelected((prev) => {
              const next = new Set(prev)
              if (event.target.checked) next.add(node.id)
              else next.delete(node.id)
              return next
            })
          }}
          className="h-3.5 w-3.5"
        />
      </TableCell>
      <TableCell>
        <div className="flex items-center gap-2">
          <Tooltip content={`Sync: ${sync}`} delay={200}>
            <StatusDot tone={syncTone} size="md" />
          </Tooltip>
          <Tooltip
            content={isHealthless && rawHealth === 'Unknown' ? 'No health computation for this kind' : `Health: ${rawHealth}`}
            delay={200}
          >
            <StatusDot tone={healthTone} size="md" />
          </Tooltip>
          <span className={clsx(
            'truncate text-[11px]',
            syncTone === 'unhealthy' || syncTone === 'degraded' ? 'font-medium text-amber-700 dark:text-amber-400' : 'text-theme-text-tertiary',
          )}>
            {sync === 'Synced' && (healthDisplay === '—' || healthDisplay === 'Healthy') ? 'OK' : sync}
          </span>
        </div>
      </TableCell>
      <TableCell>
        <div className="flex min-w-0 items-baseline gap-2">
          <span className="shrink-0 font-mono text-[10px] uppercase tracking-wide text-theme-text-tertiary">{node.ref.kind}</span>
          <span className="min-w-0 truncate font-medium text-theme-text-primary">{node.ref.name}</span>
        </div>
      </TableCell>
      {showNamespaceColumn && (
        <TableCell>
          {node.ref.namespace ? (
            <span className="text-theme-text-secondary">{node.ref.namespace}</span>
          ) : (
            <span className="text-theme-text-tertiary/60">cluster</span>
          )}
        </TableCell>
      )}
      <TableCell>
        {created ? (
          <Tooltip content={new Date(created).toLocaleString()} delay={400}>
            <span className="text-theme-text-secondary">{formatRelative(created)}</span>
          </Tooltip>
        ) : (
          <span className="text-theme-text-tertiary/60">—</span>
        )}
      </TableCell>
    </tr>
  )
}

// Sync states use the same tone palette as health states so the eye treats
// sync and health dots as a single status indicator pair. OutOfSync rises
// to "degraded" (amber) — it's the GitOps equivalent of "the world isn't
// what you asked for"; not red because the live state itself isn't broken.
function syncToneOf(sync: string): 'healthy' | 'degraded' | 'unhealthy' | 'neutral' | 'unknown' {
  switch (sync) {
    case 'Synced':
    case 'Pruned':
      return 'healthy'
    case 'OutOfSync':
      return 'degraded'
    case 'Missing':
      return 'unhealthy'
    case 'Reconciling':
      return 'neutral'
    default:
      return 'unknown'
  }
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

function filterTreeNodes(tree: GitOpsResourceTree | null, query: string, filters: GitOpsTreeFilters): GitOpsTreeNode[] {
  const q = query.trim().toLowerCase()
  const kinds = setFromFilter(filters.kinds)
  const sync = setFromFilter(filters.sync)
  const health = setFromFilter(filters.health)
  const namespaces = setFromFilter(filters.namespaces)
  const roles = setFromFilter(filters.roles)
  return (tree?.nodes ?? []).filter((node) => {
    if (q && ![
      node.ref.kind,
      node.ref.name,
      node.ref.namespace,
      node.ref.group,
      node.sync,
      node.health,
    ].some((value) => String(value ?? '').toLowerCase().includes(q))) return false
    if (kinds && !kinds.has(node.ref.kind)) return false
    if (sync && !sync.has(node.sync || 'Unknown')) return false
    if (health && !health.has(node.health || 'Unknown')) return false
    if (namespaces && !namespaces.has(node.ref.namespace || '(cluster)')) return false
    if (roles && !roles.has(node.role)) return false
    return true
  })
}

function setFromFilter(values?: Set<string> | string[]): Set<string> | undefined {
  if (!values) return undefined
  const set = values instanceof Set ? values : new Set(values)
  return set.size > 0 ? set : undefined
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
  if (ref.group === 'source.toolkit.fluxcd.io') {
    return kind === 'gitrepository' || kind === 'helmrepository' || kind === 'helmchart' || kind === 'bucket' || kind === 'ocirepository'
  }
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
    raw: resource,
  }
}

function normalizeFluxKustomization(resource: any): GitOpsRow {
  const status = getGitOpsStatus('kustomizations', resource)
  const sourceRef = resource.spec?.sourceRef ?? {}
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
    repository: [sourceRef.kind, sourceRef.namespace ? `${sourceRef.namespace}/` : '', sourceRef.name].filter(Boolean).join(' '),
    targetRevision: resource.status?.lastAppliedRevision ?? resource.status?.lastAttemptedRevision ?? '',
    path: resource.spec?.path ?? '',
    chart: '',
    destination: resource.spec?.kubeConfig?.secretRef?.name ? `kubeconfig/${resource.spec.kubeConfig.secretRef.name}` : 'in-cluster',
    destinationNamespace: resource.spec?.targetNamespace ?? resource.metadata?.namespace ?? '',
    createdAt: resource.metadata?.creationTimestamp ?? '',
    lastSync: newestConditionTime(resource),
    autoSync: !resource.spec?.suspend,
    raw: resource,
  }
}

function normalizeFluxHelmRelease(resource: any): GitOpsRow {
  const status = getGitOpsStatus('helmreleases', resource)
  const chartSpec = resource.spec?.chart?.spec ?? {}
  const sourceRef = chartSpec.sourceRef ?? {}
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
    repository: [sourceRef.kind, sourceRef.namespace ? `${sourceRef.namespace}/` : '', sourceRef.name].filter(Boolean).join(' '),
    targetRevision: chartSpec.version ?? resource.status?.lastAttemptedRevision ?? '',
    path: '',
    chart: chartSpec.chart ?? '',
    destination: resource.spec?.kubeConfig?.secretRef?.name ? `kubeconfig/${resource.spec.kubeConfig.secretRef.name}` : 'in-cluster',
    destinationNamespace: resource.spec?.targetNamespace ?? resource.metadata?.namespace ?? '',
    createdAt: resource.metadata?.creationTimestamp ?? '',
    lastSync: newestConditionTime(resource),
    autoSync: !resource.spec?.suspend,
    raw: resource,
  }
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
  if (sortKey === 'health') return healthRank(a.health) - healthRank(b.health) || a.name.localeCompare(b.name)
  if (sortKey === 'sync') return syncRank(a.sync) - syncRank(b.sync) || a.name.localeCompare(b.name)
  if (sortKey === 'lastSync') return (Date.parse(b.lastSync || b.createdAt) || 0) - (Date.parse(a.lastSync || a.createdAt) || 0)
  if (sortKey === 'project') return a.project.localeCompare(b.project) || a.name.localeCompare(b.name)
  return a.name.localeCompare(b.name)
}

function healthRank(health: string) {
  return { Degraded: 0, Missing: 1, Progressing: 2, Suspended: 3, Unknown: 4, Healthy: 5 }[health] ?? 4
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
  if (!value) return '-'
  const time = Date.parse(value)
  if (!Number.isFinite(time)) return value
  const diff = Date.now() - time
  if (diff < 0) return new Date(time).toLocaleString()
  const minutes = Math.floor(diff / 60_000)
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 60) return `${days}d ago`
  return new Date(time).toLocaleDateString()
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
  icon: Icon,
  loading,
  disabled,
  danger,
  primary,
  onClick,
}: {
  label: string
  icon: ComponentType<{ className?: string }>
  loading?: boolean
  disabled?: boolean
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
  return (
    <Tooltip content={label}>
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
