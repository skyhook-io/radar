import { useCallback, useEffect, useMemo, useRef, useState, type ComponentType, type MouseEvent as ReactMouseEvent, type ReactNode } from 'react'
import { clsx } from 'clsx'
import {
  AlertTriangle,
  ArrowDownUp,
  Check,
  CheckCircle2,
  ChevronDown,
  CircleAlert,
  CircleDot,
  GitBranch,
  HeartPulse,
  LayoutGrid,
  List,
  Loader2,
  Pause,
  Play,
  RefreshCw,
  RotateCcw,
  RotateCw,
  Search,
  Square,
  Tag,
  Trash2,
  Zap,
} from 'lucide-react'

import { HealthStatusBadge, SyncStatusBadge } from './GitOpsStatusBadge'
import { Tooltip } from '../ui/Tooltip'
import { PageHeader } from '../ui/PageHeader'
import { SummaryTile, type SummaryTone } from '../ui/SummaryTile'
import { FacetSection, FacetButton } from '../ui/Facet'
import { SortableTh, TH_CLASS, type SortDir } from '../ui/SortableTh'
import { DistributionBar } from '../ui/DistributionBar'
import { RowActionMenu, type RowActionItem } from '../ui/RowActionMenu'
import { useRefreshAnimation } from '../../hooks/useRefreshAnimation'
import { getGitOpsResourceStatus } from './detail-helpers'
import { isArgoSuspendedByRadar } from '../resources/resource-utils-argo'
import { toggleSet } from './GitOpsGraphFilterRail'
import { parseContextName } from '../../utils/context-name'

// =============================================================================
// GitOpsTableView — the canonical GitOps fleet list (Argo + Flux).
//
// Originally inline in radar/web/src/components/gitops/GitOpsView.tsx as the
// per-cluster Radar UI. Extracted here so Radar Hub's cross-cluster fleet
// GitOps view can mount the same component with different data wiring
// rather than reinventing the table, filter sidebar, status distribution,
// status pills, and tile view from scratch — bringing OSS and Hub to
// visual parity.
//
// Composition model:
//   - Pure presentational; all data flows in via `rows`, `counts`, `loading`
//   - Caller owns data fetching + normalization (use the exported
//     normalizeArgoApplication / normalizeFluxKustomization /
//     normalizeFluxHelmRelease helpers to convert raw CRDs to GitOpsRow)
//   - Fleet info (controller cluster, destination match) is rendered
//     INLINE in the canonical Application + Destination cells via
//     `row._cluster` / `row._destination` stamps. OSS rows leave these
//     undefined; the cells fall back to the standard rendering.
//   - Optional `crossClusterCount` + destination filter chips render only
//     when the caller passes them (Hub-only — single-cluster Radar omits)
//   - All UI state (mode, search, filters, sort, view mode) is owned
//     internally; caller doesn't need to thread state through props
// =============================================================================

// ----- Types -----------------------------------------------------------------

export type GitOpsMode = 'applications' | 'sources' | 'projects' | 'alerts'
// Resource categories with an implemented view. Sources/Projects/Alerts are
// still stubbed ("queued"), so only `applications` ships today — which is why
// the Scope switcher stays hidden until a second category lands. A one-option
// switcher is just noise (an always-selected pill that can't be changed).
const AVAILABLE_MODES: GitOpsMode[] = ['applications']
export type GitOpsViewMode = 'table' | 'tiles'
// 'urgency' is the curated DEFAULT order (what needs attention first) — not a
// column, so no header shows it as active; clicking any header replaces it with
// that column's own semantics.
export type SortKey = 'urgency' | 'name' | 'health' | 'sync' | 'lastSync' | 'project'

// Row-level actions surfaced from the table's three-dot menu. The set
// mirrors what the detail page exposes today; callers wire the mutations
// + dialogs and dispatch via `onRowAction`. Argo-only actions (refresh,
// hard-refresh, terminate) and Flux-only actions (reconcile,
// sync-with-source) are filtered per-tool inside the table.
export type GitOpsRowAction =
  | 'sync'
  | 'refresh'
  | 'hard-refresh'
  | 'terminate'
  | 'suspend'
  | 'resume'
  | 'reconcile'
  | 'sync-with-source'

// FleetClusterStamp + FleetDestinationStamp — optional fields the hub-side
// `_cluster` / `_destination` stamping projects into. Keep the types here so
// callers don't need to import a separate fleet types module; OSS leaves
// these undefined.
export interface FleetClusterStamp {
  id: string
  name: string
}
export type FleetDestinationMatch = 'in_cluster' | 'exact' | 'inferred' | 'unmatched'
// FleetDestinationConfidence is a coarse signal the frontend uses to style
// the destination chip. `high` = URL-equality match (either direct or
// via Argo cluster-secret), `medium` = name-equality match (more
// fragile, more likely to be a false positive when two clusters share
// a name). `unmatched` rows omit this field.
export type FleetDestinationConfidence = 'high' | 'medium'
export interface FleetDestinationStamp {
  cluster_id?: string
  cluster_name?: string
  namespace?: string
  match: FleetDestinationMatch
  // Confidence + reason are populated for non-unmatched rows. They power
  // the chip's visual treatment (a checkmark on high-confidence matches)
  // and its title= tooltip respectively. Both come from the hub —
  // adding new values shouldn't break the frontend (unknown confidence
  // falls back to no special styling).
  confidence?: FleetDestinationConfidence
  reason?: string
  raw_server?: string
  raw_name?: string
}

export interface GitOpsRow {
  id: string
  mode: GitOpsMode
  tool: 'argo' | 'flux'
  kindName: string // URL-segment kind ('applications', 'kustomizations', …)
  kind: string     // human-facing kind ('Application', 'Kustomization', …)
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
  terminating: boolean
  terminationStartedAt?: string
  raw: any
  // Hub-only fleet stamps; OSS leaves these undefined.
  _cluster?: FleetClusterStamp
  _destination?: FleetDestinationStamp
}

export type DestinationFilter = 'all' | 'this-cluster' | 'cross-cluster' | 'unmatched'

interface SummaryTileSpec {
  key: string
  label: string
  value: number
  tone: SummaryTone
  active: boolean
  // A status tile toggles its OWN dimension: `apply` sets it (when off),
  // `clear` resets just that dimension (when on) — composing with other
  // facets + search instead of wiping them. The Total tile resets everything.
  apply?: () => void
  clear?: () => void
}

// ----- Component props -------------------------------------------------------

export interface GitOpsTableViewProps {
  // Data
  rows: GitOpsRow[]
  loading?: boolean
  error?: Error | null
  // counts keyed "group/Kind" — e.g. "argoproj.io/Application" → 17. Drives
  // the Scope-section mode tabs and the empty-state check.
  counts: Record<string, number>
  // Caller refresh — typically invalidates its useQuery + refetches.
  onRefresh?: () => void
  // Row click — caller routes to its own detail page. When the host also
  // passes `rowHrefFor`, the callback receives the MouseEvent so it can
  // `preventDefault()` for same-tree nav (e.g. react-router) or skip the
  // preventDefault to let the anchor's default full-page navigation run
  // (required for cross-router-boundary links).
  onRowClick: (row: GitOpsRow, event?: ReactMouseEvent) => void
  /** When provided, the Application-name cell renders as a real `<a href>`
   *  and the `<tr>` drops its row-level click handler. Restores ⌘-click /
   *  middle-click / "Copy link" / hover URL preview / screen-reader link
   *  semantics. `onRowClick` still fires on unmodified clicks (event arg
   *  supplied) for analytics or to take over navigation. */
  rowHrefFor?: (row: GitOpsRow) => string

  // Called when the user clicks the destination cluster chip in the
  // Destination cell. Fleet-only; OSS leaves undefined. Caller routes to
  // the destination cluster's workloads view (filtered by the Argo
  // instance label) — the chip itself stops row-click propagation.
  onDestinationClick?: (row: GitOpsRow, destination: FleetDestinationStamp) => void
  /** Anchor equivalent of `onDestinationClick`. Same rationale as
   *  `rowHrefFor` — real `<a href>` for the destination chip. */
  destinationHrefFor?: (row: GitOpsRow, destination: FleetDestinationStamp) => string
  // Cross-cluster surfaces (Hub-only); OSS leaves these undefined.
  crossClusterCount?: number
  destinationFilter?: DestinationFilter
  onDestinationFilterChange?: (next: DestinationFilter) => void
  // Per-cluster RBAC denials on argocd secrets — surface as amber note.
  forbiddenSecretsClusters?: string[]

  // Customization
  // searchHotkey: when true, '/' focuses the search input (OSS keyboard
  // shortcut convention). Default false so hub-web doesn't fight whatever
  // global '/' binding it might have.
  searchHotkey?: boolean
  // emptyStateTitle / emptyStateBody override the "No GitOps resources
  // detected" copy. Hub passes "No GitOps resources across the fleet".
  // ReactNode (not just string) so a host can split the body across lines
  // (e.g. a `<br/>` between "what's wrong" and "what to do") or emphasize
  // part of it. Strings still work unchanged.
  emptyStateTitle?: ReactNode
  emptyStateBody?: ReactNode
  /**
   * Which side the filter rail sits on. Default 'left' (OSS Radar, which
   * has no app sidebar). A host with its own left navigation rail (the
   * Cloud hub) passes 'right' so the GitOps filters don't stack a second
   * column against that nav and instead match the host's other faceted
   * pages. Mobile stacking (filters above the table) is unaffected.
   */
  filtersSide?: 'left' | 'right'
  /**
   * Global namespace pick from the host's NamespaceSwitcher. Used to
   * surface "viewing in namespace: X" context and to power the Clear
   * filters affordance when no rows match. Host owns the state; shared
   * component is read-only.
   */
  globalNamespaces?: string[]
  /**
   * Resets the global namespace pick. When wired, the "Clear filters"
   * button drops it alongside view-local filter state.
   */
  onClearNamespaces?: () => void

  // Row-level action dispatcher. When provided, the table renders a
  // right-most three-dot menu per row with Sync / Refresh / Suspend / etc.
  // Caller owns the mutation hooks + any options dialogs (e.g. Argo
  // SyncOptionsDialog). When undefined the actions column is omitted
  // entirely — keeps Hub and other consumers' layout unchanged until they
  // opt in.
  onRowAction?: (row: GitOpsRow, action: GitOpsRowAction) => void
  // In-flight action state, keyed by `row.id`. Drives the per-item
  // spinner so the user can tell which Sync/Refresh is still running.
  pendingRowActions?: Map<string, Set<GitOpsRowAction>>
}

// ----- Main component --------------------------------------------------------

export function GitOpsTableView({
  rows: allRowsInput,
  loading,
  error,
  counts,
  onRefresh,
  onRowClick,
  rowHrefFor,
  onDestinationClick,
  destinationHrefFor,
  crossClusterCount,
  destinationFilter,
  onDestinationFilterChange,
  forbiddenSecretsClusters,
  searchHotkey,
  emptyStateTitle,
  emptyStateBody,
  globalNamespaces,
  onClearNamespaces,
  onRowAction,
  pendingRowActions,
  filtersSide = 'left',
}: GitOpsTableViewProps) {
  const searchInputRef = useRef<HTMLInputElement>(null)
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
  // Auto-sync / Manual / Suspended are INDEPENDENT row attributes (a Flux app can
  // be auto-reconciling AND suspended), so this is a multi-select facet like Sync
  // and Health — not the old single-select that conflated the mode (auto vs
  // manual) with the orthogonal suspended state.
  const [automationFilters, setAutomationFilters] = useState<Set<'auto' | 'manual' | 'suspended'>>(new Set())
  const toggleAutomation = useCallback((value: 'auto' | 'manual' | 'suspended') => {
    setAutomationFilters((prev) => {
      const next = new Set(prev)
      next.has(value) ? next.delete(value) : next.add(value)
      return next
    })
  }, [])
  const [lifecycleFilter, setLifecycleFilter] = useState<'all' | 'terminating' | 'active'>('all')
  const [sort, setSort] = useState<{ key: SortKey; dir: SortDir }>({ key: 'urgency', dir: 'asc' })
  // Shared refresh feedback (spin ≥400ms → checkmark) so clicking Refresh gives
  // the same visual confirmation as every other view, even when the refetch is
  // instant (the cache is already warm).
  const [triggerRefresh, , refreshPhase] = useRefreshAnimation(onRefresh ?? (() => {}))
  // Clicking a column sorts by it (starting at the column's natural direction —
  // e.g. last-sync newest-first); clicking the active column reverses.
  const onSort = useCallback(
    (key: SortKey) => setSort((prev) => (prev.key === key ? { key, dir: prev.dir === 'asc' ? 'desc' : 'asc' } : { key, dir: SORT_DEFAULT_DIR[key] })),
    [],
  )

  const hasLocalFilters =
    !!search ||
    syncFilters.size > 0 ||
    healthFilters.size > 0 ||
    projectFilters.size > 0 ||
    namespaceFilters.size > 0 ||
    labelFilters.size > 0 ||
    automationFilters.size > 0 ||
    lifecycleFilter !== 'all'
  const hasGlobalNamespaceFilter = !!onClearNamespaces && (globalNamespaces?.length ?? 0) > 0
  const hasAnyFilter = hasLocalFilters || hasGlobalNamespaceFilter

  // Optional '/' keyboard shortcut to focus search. Avoided as a default to
  // not collide with other surfaces' keyboard maps; OSS opts in via prop.
  useEffect(() => {
    if (!searchHotkey) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== '/') return
      const active = document.activeElement
      if (active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement) return
      e.preventDefault()
      searchInputRef.current?.focus()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [searchHotkey])

  const allRows = allRowsInput
  const statusSummary = summarizeGitOpsRows(allRows)

  // Per-mode counts (Applications/Sources/Projects/Alerts) — fed from the
  // caller's counts map.
  const modeCounts: Record<GitOpsMode, number> = {
    applications: allRows.length,
    sources: (counts['source.toolkit.fluxcd.io/GitRepository'] ?? 0)
      + (counts['source.toolkit.fluxcd.io/OCIRepository'] ?? 0)
      + (counts['source.toolkit.fluxcd.io/HelmRepository'] ?? 0),
    projects: counts['argoproj.io/AppProject'] ?? 0,
    alerts: counts['notification.toolkit.fluxcd.io/Alert'] ?? 0,
  }
  const totalGitOps = Object.values(counts).reduce((sum, n) => sum + n, 0)

  const projects = useMemo(
    () => countValues(allRows.map((row) => row.project).filter(Boolean)),
    [allRows],
  )
  const rowNamespaces = useMemo(
    () => countValues(allRows.map((row) => row.namespace || '(cluster)').filter(Boolean)),
    [allRows],
  )
  const syncCounts = useMemo(() => countMap(allRows.map((row) => row.sync)), [allRows])
  const healthCounts = useMemo(() => countMap(allRows.map((row) => row.health)), [allRows])
  const automationCounts = useMemo(() => ({
    auto: allRows.filter((row) => row.autoSync).length,
    manual: allRows.filter((row) => !row.autoSync).length,
    suspended: allRows.filter((row) => row.suspended).length,
  }), [allRows])
  // The Destination column earns its width only when destinations actually vary
  // — in single-cluster OSS every row is the same in-cluster API server, so the
  // column is dead weight. Show it when any row is cross-cluster (Hub) or when
  // destinations differ; hide when they all collapse to the in-cluster default.
  const showDestination = useMemo(() => {
    // Fleet/Hub mode owns the Destination column outright (it's the multi-cluster
    // product surface — the destination filter + cross-cluster matching live here).
    if (onDestinationFilterChange) return true
    if (allRows.some((r) => r._destination && r._destination.match !== 'in_cluster')) return true
    const dests = new Set(allRows.map((r) => r.destination).filter(Boolean))
    if (dests.size > 1) return true
    const only = [...dests][0] ?? ''
    return !!only && !/kubernetes\.default\.svc/i.test(only)
  }, [allRows, onDestinationFilterChange])
  const labels = useMemo(() => countLabels(allRows), [allRows])
  const filteredRows = useMemo(() => {
    const q = search.trim().toLowerCase()
    const activeLabels = [...labelFilters]
      .map((pair) => {
        const [key, ...rest] = pair.split('=')
        return { key, value: rest.join('=') }
      })
      .filter((label) => label.key && label.value)
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
      if (automationFilters.size > 0 && !(
        (automationFilters.has('auto') && row.autoSync) ||
        (automationFilters.has('manual') && !row.autoSync) ||
        (automationFilters.has('suspended') && row.suspended)
      )) return false
      if (lifecycleFilter === 'terminating' && !row.terminating) return false
      if (lifecycleFilter === 'active' && row.terminating) return false
      if (destinationFilter && destinationFilter !== 'all') {
        const match = row._destination?.match
        if (destinationFilter === 'this-cluster' && match !== 'in_cluster') return false
        // Cross-cluster covers BOTH match modes that point at a different
        // hub-connected cluster: 'exact' (URL match, high confidence) and
        // 'inferred' (name match, medium). Forgetting 'exact' here would
        // hide the very rows the URL-correlation work was meant to surface.
        if (destinationFilter === 'cross-cluster' && match !== 'exact' && match !== 'inferred') return false
        if (destinationFilter === 'unmatched' && match !== 'unmatched') return false
      }
      return true
    })
    return [...rows].sort((a, b) => compareRows(a, b, sort.key) * (sort.dir === 'asc' ? 1 : -1))
  }, [allRows, automationFilters, healthFilters, labelFilters, lifecycleFilter, mode, namespaceFilters, projectFilters, search, sort, syncFilters, destinationFilter])

  const terminatingCount = useMemo(() => allRows.filter((row) => row.terminating).length, [allRows])

  const clearAllFilters = useCallback(() => {
    setSearch('')
    setSyncFilters(new Set())
    setHealthFilters(new Set())
    setProjectFilters(new Set())
    setNamespaceFilters(new Set())
    setLabelFilters(new Set())
    setAutomationFilters(new Set())
    setLifecycleFilter('all')
    onClearNamespaces?.()
    onDestinationFilterChange?.('all')
  }, [onClearNamespaces, onDestinationFilterChange])

  // True when nothing is filtered at all — backs the Total tile's active state.
  const noOtherFiltersActive = useCallback(
    () =>
      search === '' &&
      syncFilters.size === 0 &&
      healthFilters.size === 0 &&
      projectFilters.size === 0 &&
      namespaceFilters.size === 0 &&
      labelFilters.size === 0 &&
      automationFilters.size === 0 &&
      lifecycleFilter === 'all' &&
      (!destinationFilter || destinationFilter === 'all'),
    [
      search,
      syncFilters,
      healthFilters,
      projectFilters,
      namespaceFilters,
      labelFilters,
      automationFilters,
      lifecycleFilter,
      destinationFilter,
    ],
  )

  // Empty-state — when there's truly nothing to show across all kinds.
  // `counts` is server-filtered by the global namespace pick, so a
  // namespace-scoped zero is NOT the same as cluster-empty. Fall through
  // to the actionable empty state below when the host owns a namespace
  // pick we can clear; otherwise the user lands here with no escape hatch.
  // Also require zero actual rows: the cold-cache retry can populate `rows`
  // before the separate counts map catches up, and a populated table must not
  // be hidden behind a "nothing here" screen.
  if (totalGitOps === 0 && allRowsInput.length === 0 && !loading && !hasGlobalNamespaceFilter) {
    return (
      <div className="flex h-full min-h-0 flex-1 items-start justify-center bg-theme-base px-4 pb-4 pt-[22vh]">
        <div className="rounded-lg border border-theme-border bg-theme-surface p-8 text-center">
          <GitBranch className="mx-auto h-8 w-8 text-theme-text-tertiary" />
          <h2 className="mt-3 text-base font-semibold text-theme-text-primary">
            {emptyStateTitle ?? 'No GitOps resources detected'}
          </h2>
          <p className="mt-1 text-sm text-theme-text-secondary">
            {emptyStateBody ?? 'No ArgoCD Applications or FluxCD resources were found.'}
          </p>
        </div>
      </div>
    )
  }

  const showCrossClusterTile = typeof crossClusterCount === 'number' && mode === 'applications'

  // Header tiles unify with the facet rail: each STATUS tile toggles its own
  // dimension and composes with the other facets + search (clicking "Out of
  // sync" adds sync=OutOfSync without wiping an active health filter or your
  // search). `active` = "this dimension is exactly this value". The Total tile
  // is the reset — it clears everything.
  const summaryTiles: SummaryTileSpec[] = [
    {
      key: 'total',
      label: 'Total Applications',
      value: allRows.length,
      tone: 'neutral',
      active: noOtherFiltersActive(),
      apply: clearAllFilters,
      clear: clearAllFilters,
    },
    {
      key: 'outOfSync',
      label: 'Out of sync',
      value: statusSummary.outOfSync,
      tone: 'warning',
      active: syncFilters.size === 1 && syncFilters.has('OutOfSync'),
      apply: () => setSyncFilters(new Set(['OutOfSync'])),
      clear: () => setSyncFilters(new Set()),
    },
    {
      key: 'degraded',
      label: 'Degraded',
      value: statusSummary.degraded,
      tone: 'error',
      active: healthFilters.size === 1 && healthFilters.has('Degraded'),
      apply: () => setHealthFilters(new Set(['Degraded'])),
      clear: () => setHealthFilters(new Set()),
    },
    {
      key: 'suspended',
      label: 'Suspended',
      value: statusSummary.suspended,
      tone: 'warning',
      active: automationFilters.size === 1 && automationFilters.has('suspended'),
      apply: () => setAutomationFilters(new Set(['suspended'])),
      clear: () => setAutomationFilters(new Set()),
    },
    {
      key: 'reconciling',
      label: 'Reconciling',
      value: syncCounts.get('Reconciling') ?? 0,
      tone: 'info',
      active: syncFilters.size === 1 && syncFilters.has('Reconciling'),
      apply: () => setSyncFilters(new Set(['Reconciling'])),
      clear: () => setSyncFilters(new Set()),
    },
    ...(showCrossClusterTile
      ? [
          {
            key: 'crossCluster',
            label: 'Cross-cluster',
            value: crossClusterCount!,
            tone: 'info' as const,
            active: destinationFilter === 'cross-cluster',
            apply: () => onDestinationFilterChange?.('cross-cluster'),
            clear: () => onDestinationFilterChange?.('all'),
          },
        ]
      : []),
  ]

  return (
    <div className="flex h-full min-w-0 flex-1 flex-col overflow-hidden bg-theme-base">
      {/* Full-width header on top; the filter facet + content sit below it. */}
      <div className="shrink-0 border-b border-theme-border bg-theme-base px-4 py-4">
        <PageHeader
          icon={GitBranch}
          title="GitOps"
          description="Applications and reconciliations with source, destination, sync, and health state."
          actions={summaryTiles.map((tile) => (
            <SummaryTile
              key={tile.key}
              label={tile.label}
              value={tile.value}
              tone={tile.tone}
              active={tile.active}
              onClick={() => {
                if (tile.active) tile.clear?.()
                else tile.apply?.()
              }}
            />
          ))}
        />
      </div>
      <div
        className={`flex min-w-0 flex-1 overflow-hidden max-[899px]:flex-col ${
          filtersSide === 'right' ? 'min-[900px]:flex-row-reverse' : ''
        }`}
      >
      <GitOpsFilterSidebar
        side={filtersSide}
        mode={mode}
        onModeChange={setMode}
        modeCounts={modeCounts}
        syncCounts={syncCounts}
        syncFilters={syncFilters}
        onToggleSync={(value) => toggleSet(syncFilters, setSyncFilters, value)}
        healthCounts={healthCounts}
        healthFilters={healthFilters}
        onToggleHealth={(value) => toggleSet(healthFilters, setHealthFilters, value)}
        automationFilters={automationFilters}
        automationCounts={automationCounts}
        onToggleAutomation={toggleAutomation}
        lifecycleFilter={lifecycleFilter}
        onLifecycleFilterChange={setLifecycleFilter}
        terminatingCount={terminatingCount}
        projects={projects}
        projectFilters={projectFilters}
        onToggleProject={(value) => toggleSet(projectFilters, setProjectFilters, value)}
        namespaces={rowNamespaces}
        namespaceFilters={namespaceFilters}
        onToggleNamespace={(value) => toggleSet(namespaceFilters, setNamespaceFilters, value)}
        onClear={clearAllFilters}
      />

      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        {forbiddenSecretsClusters && forbiddenSecretsClusters.length > 0 && mode === 'applications' && (
          // Hub-only graceful-degradation note: when the user lacks `get
          // secrets` in the argocd namespace on a controller, the hub
          // can't resolve server-URL destinations to Argo-registered
          // names. Direct name-match still works; surface so operators
          // understand why some destinations show as unmatched.
          <div className="shrink-0 border-b border-amber-500/30 bg-amber-500/10 px-4 py-2 text-xs text-amber-700 dark:text-amber-300">
            <span className="font-medium">Cross-cluster mapping limited for {forbiddenSecretsClusters.join(', ')}.</span>{' '}
            Owner needs <code className="inline-code">get secrets</code> in the <code className="inline-code">argocd</code> namespace there; destination
            correlation falls back to name-only resolution.
          </div>
        )}

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
            {filteredRows.length !== allRows.length && (
              <span className="text-[11px] text-theme-text-tertiary">
                Showing {filteredRows.length} of {allRows.length}
              </span>
            )}
            {/* Table mode sorts via clickable column headers (Resources-table
                pattern); tile mode has no headers, so it keeps a compact sort
                control wired to the same sort state. */}
            {viewMode === 'tiles' && (
              <GitOpsSortMenu sortKey={sort.key} onChange={(k) => setSort({ key: k, dir: SORT_DEFAULT_DIR[k] })} />
            )}
            {labels.length > 0 && (
              <LabelsDropdown
                labels={labels}
                activeLabels={labelFilters}
                onToggle={(value) => toggleSet(labelFilters, setLabelFilters, value)}
                onClear={() => setLabelFilters(new Set())}
                open={showLabelsDropdown}
                onOpenChange={setShowLabelsDropdown}
                search={labelSearch}
                onSearchChange={setLabelSearch}
              />
            )}
            {onDestinationFilterChange && mode === 'applications' && (
              <div className="ml-auto flex items-center gap-1 text-[11px]">
                <span className="text-theme-text-tertiary">Destination:</span>
                {(['all', 'this-cluster', 'cross-cluster', 'unmatched'] as DestinationFilter[]).map((v) => (
                  <button
                    key={v}
                    type="button"
                    onClick={() => onDestinationFilterChange(v)}
                    className={`rounded-md border px-2 py-0.5 font-medium transition-colors ${
                      destinationFilter === v
                        ? 'border-sky-500/40 bg-sky-500/10 text-sky-600 dark:text-sky-300'
                        : 'border-theme-border bg-theme-base text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
                    }`}
                  >
                    {v === 'all' ? 'All' : v === 'this-cluster' ? 'This cluster' : v === 'cross-cluster' ? 'Cross-cluster' : 'Unmatched'}
                  </button>
                ))}
              </div>
            )}
            {hasAnyFilter && (
              <Tooltip content={hasGlobalNamespaceFilter ? 'Reset all filters and the active namespace' : 'Reset all filters'}>
                <button
                  type="button"
                  onClick={clearAllFilters}
                  className="inline-flex h-8 items-center gap-1.5 rounded-md border border-theme-border bg-theme-base px-2.5 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
                >
                  <RotateCcw className="h-3.5 w-3.5" />
                  Clear filters
                </button>
              </Tooltip>
            )}
            <div className="flex shrink-0 items-center gap-0 overflow-hidden rounded-md border border-theme-border">
              <GitOpsIconToggle active={viewMode === 'table'} label="Table view" icon={List} onClick={() => setViewMode('table')} />
              <GitOpsIconToggle active={viewMode === 'tiles'} label="Tiles view" icon={LayoutGrid} onClick={() => setViewMode('tiles')} />
            </div>
            {onRefresh && (
              <Tooltip content="Refresh GitOps resources">
                <button
                  type="button"
                  onClick={triggerRefresh}
                  className="inline-flex h-8 w-8 items-center justify-center rounded-md border border-theme-border bg-theme-base text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
                >
                  {refreshPhase === 'success'
                    ? <Check className="h-3.5 w-3.5 text-emerald-500" />
                    : <RefreshCw className={clsx('h-3.5 w-3.5', (refreshPhase === 'spinning' || loading) && 'animate-spin')} />}
                </button>
              </Tooltip>
            )}
          </div>
        </div>

        {/* pb-20 keeps the last row (and its three-dot menu) scrollable clear
            of the app's fixed bottom-right overlay buttons; without the slack
            the bottom row's action trigger sits under them and can't be clicked
            once the list fills the viewport. Only needed when the actions column
            is present — consumers without onRowAction (e.g. Hub) skip the slack. */}
        <div className={clsx('min-h-0 min-w-0 flex-1 overflow-auto bg-theme-base', onRowAction && 'pb-20')}>
          {mode !== 'applications' ? (
            <div className="flex h-full items-center justify-center text-sm text-theme-text-secondary">
              {modeLabel(mode)} view is queued behind the application list.
            </div>
          ) : loading && filteredRows.length === 0 ? (
            <div className="flex h-full items-center justify-center text-sm text-theme-text-secondary">
              <Loader2 className="mr-2 h-4 w-4 animate-spin" /> Loading GitOps applications...
            </div>
          ) : error ? (
            <div className="p-4 text-sm text-red-500">Failed to load GitOps applications: {error.message}</div>
          ) : filteredRows.length === 0 ? (
            <div className="flex h-full flex-col items-center justify-center gap-3 text-sm text-theme-text-secondary">
              <p>{allRows.length === 0 && !hasGlobalNamespaceFilter ? 'No applications found.' : 'No applications match the current filters.'}</p>
              {hasGlobalNamespaceFilter && globalNamespaces && (
                <p className="text-xs text-theme-text-tertiary">
                  Viewing {globalNamespaces.length === 1 ? `namespace: ${globalNamespaces[0]}` : `${globalNamespaces.length} namespaces`}
                </p>
              )}
              {(hasGlobalNamespaceFilter || (allRows.length > 0 && hasLocalFilters)) && (
                <button
                  type="button"
                  onClick={clearAllFilters}
                  className="inline-flex items-center gap-1.5 rounded-md bg-theme-elevated px-3 py-1.5 text-sm text-theme-text-secondary transition-colors hover:bg-theme-border hover:text-theme-text-primary"
                >
                  <RotateCcw className="h-3.5 w-3.5" />
                  Clear filters
                </button>
              )}
            </div>
          ) : viewMode === 'tiles' ? (
            <GitOpsTiles rows={filteredRows} onOpen={onRowClick} hrefFor={rowHrefFor} />
          ) : (
            <GitOpsTable
              rows={filteredRows}
              sort={sort}
              onSort={onSort}
              onOpen={onRowClick}
              hrefFor={rowHrefFor}
              showDestination={showDestination}
              onDestinationClick={onDestinationClick}
              destinationHrefFor={destinationHrefFor}
              onRowAction={onRowAction}
              pendingRowActions={pendingRowActions}
            />
          )}
        </div>
      </div>
      </div>
    </div>
  )
}

// =============================================================================
// Subcomponents — kept private to the file; they're tightly coupled to
// GitOpsTableView's visual language and not generally useful elsewhere.
// =============================================================================

function GitOpsFilterSidebar({
  side,
  mode,
  onModeChange,
  modeCounts,
  syncCounts,
  syncFilters,
  onToggleSync,
  healthCounts,
  healthFilters,
  onToggleHealth,
  automationFilters,
  automationCounts,
  onToggleAutomation,
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
  side: 'left' | 'right'
  mode: GitOpsMode
  onModeChange: (mode: GitOpsMode) => void
  modeCounts: Record<GitOpsMode, number>
  syncCounts: Map<string, number>
  syncFilters: Set<string>
  onToggleSync: (value: string) => void
  healthCounts: Map<string, number>
  healthFilters: Set<string>
  onToggleHealth: (value: string) => void
  automationFilters: Set<'auto' | 'manual' | 'suspended'>
  automationCounts: { auto: number; manual: number; suspended: number }
  onToggleAutomation: (value: 'auto' | 'manual' | 'suspended') => void
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
    <aside
      className={`flex w-72 shrink-0 flex-col overflow-hidden border-theme-border bg-theme-surface/90 max-[899px]:max-h-72 max-[899px]:w-full max-[899px]:border-b ${
        side === 'right' ? 'border-l max-[899px]:border-l-0' : 'border-r max-[899px]:border-r-0'
      }`}
    >
      <div className="flex items-center justify-between border-b border-theme-border px-3 py-2">
        <span className="text-sm font-medium text-theme-text-secondary">Filters</span>
        <button type="button" onClick={onClear} className="text-[10px] font-medium text-blue-500 hover:text-blue-400">
          Clear
        </button>
      </div>
      <div className="flex-1 overflow-y-auto">
        {AVAILABLE_MODES.length > 1 && (
        <GitOpsFilterSection icon={GitBranch} title="Scope">
          <div className="grid grid-cols-2 gap-1">
            {AVAILABLE_MODES.map((item) => (
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
        </GitOpsFilterSection>
        )}

        <GitOpsFilterSection icon={CheckCircle2} title="Sync">
          <GitOpsFacetButton label="Synced" count={syncCounts.get('Synced') ?? 0} active={syncFilters.has('Synced')} tone="success" onClick={() => onToggleSync('Synced')} />
          <GitOpsFacetButton label="OutOfSync" count={syncCounts.get('OutOfSync') ?? 0} active={syncFilters.has('OutOfSync')} tone="warning" onClick={() => onToggleSync('OutOfSync')} />
          <GitOpsFacetButton label="Reconciling" count={syncCounts.get('Reconciling') ?? 0} active={syncFilters.has('Reconciling')} tone="info" onClick={() => onToggleSync('Reconciling')} />
          <GitOpsFacetButton label="Unknown" count={syncCounts.get('Unknown') ?? 0} active={syncFilters.has('Unknown')} onClick={() => onToggleSync('Unknown')} />
        </GitOpsFilterSection>

        <GitOpsFilterSection icon={HeartPulse} title="Health">
          <GitOpsFacetButton label="Healthy" count={healthCounts.get('Healthy') ?? 0} active={healthFilters.has('Healthy')} tone="success" onClick={() => onToggleHealth('Healthy')} />
          <GitOpsFacetButton label="Progressing" count={healthCounts.get('Progressing') ?? 0} active={healthFilters.has('Progressing')} tone="info" onClick={() => onToggleHealth('Progressing')} />
          <GitOpsFacetButton label="Degraded" count={healthCounts.get('Degraded') ?? 0} active={healthFilters.has('Degraded')} tone="error" onClick={() => onToggleHealth('Degraded')} />
          <GitOpsFacetButton label="Suspended" count={healthCounts.get('Suspended') ?? 0} active={healthFilters.has('Suspended')} tone="warning" onClick={() => onToggleHealth('Suspended')} />
          <GitOpsFacetButton label="Unknown" count={healthCounts.get('Unknown') ?? 0} active={healthFilters.has('Unknown')} onClick={() => onToggleHealth('Unknown')} />
        </GitOpsFilterSection>

        <GitOpsFilterSection icon={CircleDot} title="Automation (Sync policy)">
          <GitOpsFacetButton label="Auto-sync" count={automationCounts.auto} active={automationFilters.has('auto')} onClick={() => onToggleAutomation('auto')} />
          <GitOpsFacetButton label="Manual" count={automationCounts.manual} active={automationFilters.has('manual')} onClick={() => onToggleAutomation('manual')} />
          <GitOpsFacetButton label="Suspended" count={automationCounts.suspended} active={automationFilters.has('suspended')} tone="warning" onClick={() => onToggleAutomation('suspended')} />
        </GitOpsFilterSection>

        {terminatingCount > 0 && (
          <GitOpsFilterSection icon={Trash2} title="Lifecycle">
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
                        ? 'bg-orange-500 text-white'
                        : 'bg-skyhook-500 text-white'
                      : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
                  }`}
                >
                  {label}
                </button>
              ))}
            </div>
          </GitOpsFilterSection>
        )}

        <GitOpsFilterSection icon={CircleAlert} title="Projects">
          {projects.slice(0, 10).map((project) => (
            <GitOpsFacetButton
              key={project.name}
              label={project.name || '(none)'}
              count={project.count}
              active={projectFilters.has(project.name || '(none)')}
              onClick={() => onToggleProject(project.name || '(none)')}
            />
          ))}
        </GitOpsFilterSection>

        <GitOpsFilterSection icon={List} title="Namespaces">
          {namespaces.slice(0, 12).map((namespace) => (
            <GitOpsFacetButton
              key={namespace.name}
              label={namespace.name}
              count={namespace.count}
              active={namespaceFilters.has(namespace.name)}
              onClick={() => onToggleNamespace(namespace.name)}
            />
          ))}
        </GitOpsFilterSection>
      </div>
    </aside>
  )
}

// GitOps's filter rail is built from the shared faceted-filter primitives
// (ui/Facet). These aliases keep the existing GitOps* import surface
// (GitOpsGraphFilterRail imports them from here) while the look + behavior live
// in one place — change the facet once, every rail follows.
export const GitOpsFilterSection = FacetSection
export const GitOpsFacetButton = FacetButton

// A view-mode toggle (table/tiles) — not a facet; stays local.
export function GitOpsIconToggle({ active, label, icon: Icon, onClick }: { active: boolean; label: string; icon: ComponentType<{ className?: string }>; onClick: () => void }) {
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

// Tile mode has no sortable column headers, so it gets this themed sort menu
// (a native <select> can't be styled to match the rest of the toolbar).
const SORT_OPTIONS: [SortKey, string][] = [
  ['urgency', 'Priority'],
  ['health', 'Health'],
  ['sync', 'Sync'],
  ['lastSync', 'Last sync'],
  ['project', 'Project'],
  ['name', 'Name'],
]
function GitOpsSortMenu({ sortKey, onChange }: { sortKey: SortKey; onChange: (key: SortKey) => void }) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => { if (!ref.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])
  const current = SORT_OPTIONS.find(([k]) => k === sortKey)?.[1] ?? 'Name'
  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-label="Sort"
        className="inline-flex h-8 items-center gap-1.5 rounded-md border border-theme-border bg-theme-base px-2.5 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
      >
        <ArrowDownUp className="h-3.5 w-3.5" />
        Sort: {current}
        <ChevronDown className="h-3 w-3 opacity-60" />
      </button>
      {open && (
        <div className="absolute right-0 top-full z-50 mt-1 w-40 overflow-hidden rounded-lg border border-theme-border bg-theme-surface py-1 shadow-xl">
          {SORT_OPTIONS.map(([key, label]) => (
            <button
              key={key}
              type="button"
              onClick={() => { onChange(key); setOpen(false) }}
              className={clsx(
                'flex w-full items-center gap-2 px-2.5 py-1.5 text-left text-xs hover:bg-theme-hover',
                key === sortKey ? 'font-medium text-theme-text-primary' : 'text-theme-text-secondary',
              )}
            >
              <Check className={clsx('h-3.5 w-3.5 shrink-0', key === sortKey ? 'opacity-100 text-skyhook-500' : 'opacity-0')} />
              {label}
            </button>
          ))}
        </div>
      )}
    </div>
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
  const containerRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (!containerRef.current?.contains(e.target as Node)) onOpenChange(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open, onOpenChange])
  return (
    <div ref={containerRef} className="relative">
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
  // Single dimension: health. Earlier this mixed health (healthy /
  // progressing / degraded) with sync (outOfSync) which double-counted
  // rows that were both (e.g. Synced + Degraded → 2 segment increments
  // → total > rows.length → flex distorts proportions). Use health for
  // the bar — sync state is visible elsewhere (the OutOfSync summary
  // tile, the Sync column, the filter rail).
  const summary = summarizeGitOpsRows(rows)
  return (
    <DistributionBar
      ariaLabel="Health distribution"
      segments={[
        { key: 'degraded', count: summary.degraded, fillClass: 'bg-red-500' },
        { key: 'progressing', count: summary.progressing, fillClass: 'bg-sky-500' },
        { key: 'healthy', count: summary.healthy, fillClass: 'bg-emerald-500' },
        { key: 'unknown', count: Math.max(0, rows.length - summary.healthy - summary.progressing - summary.degraded), fillClass: 'bg-theme-text-tertiary/40' },
      ]}
    />
  )
}

function GitOpsTable({
  rows,
  sort,
  onSort,
  onOpen,
  hrefFor,
  showDestination = true,
  onDestinationClick,
  destinationHrefFor,
  onRowAction,
  pendingRowActions,
}: {
  rows: GitOpsRow[]
  sort: { key: SortKey; dir: SortDir }
  onSort: (key: SortKey) => void
  onOpen: (row: GitOpsRow, event?: ReactMouseEvent) => void
  hrefFor?: (row: GitOpsRow) => string
  showDestination?: boolean
  onDestinationClick?: (row: GitOpsRow, destination: FleetDestinationStamp) => void
  destinationHrefFor?: (row: GitOpsRow, destination: FleetDestinationStamp) => string
  onRowAction?: (row: GitOpsRow, action: GitOpsRowAction) => void
  pendingRowActions?: Map<string, Set<GitOpsRowAction>>
}) {
  const showActions = !!onRowAction
  return (
    <table className="w-full min-w-[1040px] table-fixed border-separate border-spacing-0 text-sm">
      <thead className="sticky top-0 z-10 bg-theme-base">
        <tr>
          <SortableTh label="Application" sortKey="name" activeKey={sort.key} direction={sort.dir} onSort={onSort} className={showActions ? 'w-[16%]' : 'w-[22%]'} />
          <SortableTh label="Project" sortKey="project" activeKey={sort.key} direction={sort.dir} onSort={onSort} className="w-[9%]" />
          <SortableTh label="Sync" sortKey="sync" activeKey={sort.key} direction={sort.dir} onSort={onSort} className="w-[9%]" />
          <SortableTh label="Health" sortKey="health" activeKey={sort.key} direction={sort.dir} onSort={onSort} className="w-[9%]" />
          <th className={clsx(TH_CLASS, showDestination ? 'w-[20%]' : 'w-[28%]')}>Source</th>
          {showDestination && <th className={clsx(TH_CLASS, 'w-[14%]')}>Destination</th>}
          <SortableTh label="Last Sync" sortKey="lastSync" activeKey={sort.key} direction={sort.dir} onSort={onSort} className="w-[10%]" />
          {showActions && (
            <th className={clsx(TH_CLASS, 'w-[6%] text-right')}>
              <span className="sr-only">Actions</span>
            </th>
          )}
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => {
          const href = hrefFor?.(row)
          return (
            <tr
              key={row.id}
              onClick={href ? undefined : () => onOpen(row)}
              className={clsx(
                'border-b border-theme-border bg-theme-base hover:bg-theme-hover',
                !href && 'cursor-pointer',
                row.terminating && 'opacity-70',
              )}
            >
              <TableCell>
                <div className="flex min-w-0 items-center gap-2">
                  <span className={`h-8 w-1 shrink-0 rounded-full ${statusStripe(row)}`} />
                  {row.terminating && (
                    <Tooltip content="Pending deletion — finalizers still running">
                      <span className="inline-flex shrink-0 items-center gap-1 rounded border border-orange-500/40 bg-orange-500/15 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-orange-400">
                        <Trash2 className="h-3 w-3" />
                        Terminating
                      </span>
                    </Tooltip>
                  )}
                  <div className="min-w-0">
                    {href ? (
                      <a
                        href={href}
                        onClick={(e) => {
                          if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return
                          onOpen(row, e)
                        }}
                        className="block truncate font-medium text-theme-text-primary hover:underline focus-visible:underline focus-visible:outline-none rounded-sm"
                      >
                        {row.name}
                      </a>
                    ) : (
                      <div className="truncate font-medium text-theme-text-primary">{row.name}</div>
                    )}
                    <div className="truncate text-xs text-theme-text-tertiary">
                      {row.tool === 'argo' ? 'ArgoCD' : 'FluxCD'} {row.kind}
                      {row._cluster && (
                        <span title={row._cluster.name !== shortClusterName(row._cluster.name) ? row._cluster.name : undefined}>
                          {' · '}{shortClusterName(row._cluster.name)}
                        </span>
                      )}
                    </div>
                  </div>
                </div>
              </TableCell>
              <TableCell>{row.project || '-'}</TableCell>
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
              {showDestination && (
                <TableCell>
                  <DestinationCell row={row} onDestinationClick={onDestinationClick} destinationHrefFor={destinationHrefFor} />
                  <div className="truncate text-xs text-theme-text-tertiary">{row.destinationNamespace || row.namespace || '-'}</div>
                </TableCell>
              )}
              <TableCell>
                {row.terminating
                  ? <span className="text-orange-400/80">Pending {formatRelativeAge(row.terminationStartedAt ?? '') || 'now'}</span>
                  : formatRelativeAge(row.lastSync || row.createdAt)}
              </TableCell>
              {showActions && onRowAction && (
                <td
                  className="overflow-visible border-b border-theme-border px-2 py-2 text-right align-middle"
                  onClick={(e) => e.stopPropagation()}
                >
                  <RowActionMenu items={buildRowActionItems(row, onRowAction, pendingRowActions)} />
                </td>
              )}
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}

// buildRowActionItems composes the per-row three-dot menu entries based on
// the row's tool (Argo vs Flux), current suspend state, terminating state,
// and Argo's operationState.phase (used to gate the Terminate entry — only
// shown while a sync is mid-flight, mirroring the detail-page condition).
function buildRowActionItems(
  row: GitOpsRow,
  onAction: (row: GitOpsRow, action: GitOpsRowAction) => void,
  pending?: Map<string, Set<GitOpsRowAction>>,
): RowActionItem[] {
  const inFlight = pending?.get(row.id)
  const isPending = (action: GitOpsRowAction) => inFlight?.has(action) ?? false
  const terminating = row.terminating
  const suspended = row.suspended
  // Disabled-reason copy matches what the detail page already shows so
  // operators see consistent language whichever surface they use.
  const terminatingReason = 'Resource is terminating; mutating actions are gated until finalizers complete.'
  const suspendedReason = 'Cannot sync while suspended. Resume first.'
  const items: RowActionItem[] = []

  if (row.tool === 'argo') {
    items.push({
      key: 'sync',
      label: 'Sync...',
      icon: ArrowDownUp,
      onClick: () => onAction(row, 'sync'),
      disabled: suspended || terminating,
      disabledReason: terminating ? terminatingReason : suspended ? suspendedReason : undefined,
      pending: isPending('sync'),
    })
    // Refresh / Hard refresh are read-style verbs — they re-read Git and
    // recompute status without mutating the cluster, so they stay enabled
    // during termination (matches the detail page + the backend carve-out).
    items.push({
      key: 'refresh',
      label: 'Refresh',
      icon: RefreshCw,
      onClick: () => onAction(row, 'refresh'),
      pending: isPending('refresh'),
    })
    items.push({
      key: 'hard-refresh',
      label: 'Hard refresh',
      icon: Zap,
      onClick: () => onAction(row, 'hard-refresh'),
      pending: isPending('hard-refresh'),
    })
    if (suspended) {
      items.push({
        key: 'resume',
        label: 'Resume',
        icon: Play,
        onClick: () => onAction(row, 'resume'),
        disabled: terminating,
        disabledReason: terminating ? terminatingReason : undefined,
        pending: isPending('resume'),
        divider: true,
      })
    } else {
      items.push({
        key: 'suspend',
        label: 'Suspend',
        icon: Pause,
        onClick: () => onAction(row, 'suspend'),
        disabled: terminating,
        disabledReason: terminating ? terminatingReason : undefined,
        pending: isPending('suspend'),
        divider: true,
      })
    }
    // Argo Terminate only makes sense while a sync is Running — gating
    // here matches the detail-page conditional (gitops detail mounts the
    // shortcut only when `isRunning`). For non-running rows we just omit
    // the entry rather than disabling it, to keep the menu tight.
    if (row.raw?.status?.operationState?.phase === 'Running') {
      items.push({
        key: 'terminate',
        label: 'Terminate sync',
        icon: Square,
        onClick: () => onAction(row, 'terminate'),
        pending: isPending('terminate'),
        danger: true,
      })
    }
    return items
  }

  // Flux (Kustomization / HelmRelease)
  items.push({
    key: 'reconcile',
    label: 'Reconcile',
    icon: RefreshCw,
    onClick: () => onAction(row, 'reconcile'),
    disabled: suspended || terminating,
    disabledReason: terminating ? terminatingReason : suspended ? suspendedReason : undefined,
    pending: isPending('reconcile'),
  })
  items.push({
    key: 'sync-with-source',
    label: 'Reconcile with source',
    icon: RotateCw,
    onClick: () => onAction(row, 'sync-with-source'),
    disabled: suspended || terminating,
    disabledReason: terminating ? terminatingReason : suspended ? suspendedReason : undefined,
    pending: isPending('sync-with-source'),
  })
  if (suspended) {
    items.push({
      key: 'resume',
      label: 'Resume',
      icon: Play,
      onClick: () => onAction(row, 'resume'),
      disabled: terminating,
      disabledReason: terminating ? terminatingReason : undefined,
      pending: isPending('resume'),
      divider: true,
    })
  } else {
    items.push({
      key: 'suspend',
      label: 'Suspend',
      icon: Pause,
      onClick: () => onAction(row, 'suspend'),
      disabled: terminating,
      disabledReason: terminating ? terminatingReason : undefined,
      pending: isPending('suspend'),
      divider: true,
    })
  }
  return items
}

function GitOpsTiles({
  rows,
  onOpen,
  hrefFor,
}: {
  rows: GitOpsRow[]
  onOpen: (row: GitOpsRow, event?: ReactMouseEvent) => void
  hrefFor?: (row: GitOpsRow) => string
}) {
  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(300px,1fr))] gap-3 p-4">
      {rows.map((row) => (
        <GitOpsTile key={row.id} row={row} onOpen={onOpen} href={hrefFor?.(row)} />
      ))}
    </div>
  )
}

function GitOpsTile({
  row,
  onOpen,
  href,
}: {
  row: GitOpsRow
  onOpen: (row: GitOpsRow, event?: ReactMouseEvent) => void
  href?: string
}) {
  const source = compactRepoSource(row.repository || row.chart, row.path || row.chart)
  const revision = row.targetRevision || ''
  const lastSyncRaw = row.lastSync || row.createdAt
  const recencyClass = recencyTone(lastSyncRaw)
  const dest = row.destination ? compactClusterURL(row.destination) : ''
  const ns = row.destinationNamespace || row.namespace
  const tileClass = clsx(
    'group relative flex min-w-0 flex-col overflow-hidden rounded-md border border-theme-border bg-theme-surface text-left shadow-theme-sm transition-all hover:border-theme-text-tertiary/40 hover:shadow-theme-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-theme-text-primary/20',
    row.terminating && 'opacity-80',
  )
  const body = (
    <>
      <div className={clsx('h-1 w-full', statusStripe(row))} />
      <div className="flex flex-1 flex-col gap-3 px-4 pb-4 pt-3">
        <div className="line-clamp-2 break-all text-[15px] font-semibold leading-tight text-theme-text-primary">
          {row.name}
        </div>
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
        <div className="flex flex-col gap-1 text-[12px]">
          {source && (
            <div className="truncate text-theme-text-secondary">{source}</div>
          )}
          {revision && (
            <div className="truncate font-mono text-[11px] text-theme-text-tertiary">{shortRevision(revision)}</div>
          )}
          {row.terminating ? (
            <div className="font-medium text-orange-400/80">Pending {formatRelativeAge(row.terminationStartedAt ?? '') || 'now'}</div>
          ) : (
            lastSyncRaw && <div className={clsx('font-medium', recencyClass)}>{formatRelativeAge(lastSyncRaw)}</div>
          )}
        </div>
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
    </>
  )
  if (href) {
    return (
      <a
        href={href}
        onClick={(e) => {
          if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return
          onOpen(row, e)
        }}
        className={tileClass}
      >
        {body}
      </a>
    )
  }
  return (
    <button type="button" onClick={() => onOpen(row)} className={tileClass}>
      {body}
    </button>
  )
}

// DestinationCell renders line 1 of the Destination column. Three modes:
//   - No fleet stamp (single-cluster OSS): show the raw `row.destination`
//     string from the Argo/Flux spec (typically `https://kubernetes.default.svc`
//     for in-cluster, or a public LB URL for hub-spoke).
//   - Fleet stamp with `in_cluster` or no destination match (Flux rows):
//     show muted "same cluster" — the workload lives where the controller
//     lives.
//   - Fleet stamp with `inferred` match: clickable chip with the
//     destination cluster's Radar-known name. Click stops row propagation
//     and calls `onDestinationClick` (caller routes to the destination
//     cluster's workloads view).
//   - Fleet stamp with `unmatched`: amber chip with the raw Argo
//     destination name + warning icon — signals the destination isn't a
//     Radar-connected cluster (onboarding hook).
function DestinationCell({
  row,
  onDestinationClick,
  destinationHrefFor,
}: {
  row: GitOpsRow
  onDestinationClick?: (row: GitOpsRow, destination: FleetDestinationStamp) => void
  destinationHrefFor?: (row: GitOpsRow, destination: FleetDestinationStamp) => string
}) {
  const dest = row._destination
  // Non-fleet (OSS) path — show the raw destination string.
  if (!dest) {
    return <div className="block truncate text-theme-text-primary">{row.destination || '-'}</div>
  }
  if (dest.match === 'in_cluster') {
    return <span className="block truncate text-theme-text-tertiary">same cluster</span>
  }
  if ((dest.match === 'exact' || dest.match === 'inferred') && dest.cluster_id && dest.cluster_name) {
    const short = shortClusterName(dest.cluster_name)
    // High-confidence (URL match): solid sky chip with a small ✓ marker.
    // Medium-confidence (name match): same chip styling but no marker, and
    // a slightly muted border tone so operators can tell at a glance
    // which destinations are URL-verified vs name-only. Tooltip carries
    // the human-readable reason from the hub.
    const highConfidence = dest.confidence === 'high'
    const tooltipReason = dest.reason ? ` (${dest.reason})` : ''
    const chipClass =
      'block max-w-full truncate rounded px-1.5 py-0.5 text-xs font-medium hover:bg-sky-500/20 dark:text-sky-300 ' +
      (highConfidence
        ? 'border border-sky-500/50 bg-sky-500/15 text-sky-700'
        : 'border border-sky-500/25 bg-sky-500/5 text-sky-600')
    const title = `Open workloads in ${dest.cluster_name}${tooltipReason}`
    const chipBody = `${highConfidence ? '✓ ' : ''}${short}`
    const destHref = destinationHrefFor?.(row, dest)
    if (destHref) {
      return (
        <a
          href={destHref}
          // The chip sits inside the row's `<td>`; when a host wires
          // `destinationHrefFor` without `rowHrefFor`, the `<tr>` retains
          // its own onClick. Stop the bubble so a click on the chip
          // doesn't also trigger row navigation.
          onClick={(e) => e.stopPropagation()}
          className={chipClass + ' focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-500/40'}
          title={title}
        >
          {chipBody}
        </a>
      )
    }
    return (
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation()
          onDestinationClick?.(row, dest)
        }}
        className={chipClass}
        title={title}
      >
        {chipBody}
      </button>
    )
  }
  const rawLabel = dest.raw_name || dest.raw_server || 'unknown'
  return (
    <span
      className="flex min-w-0 items-center gap-1 text-xs text-amber-600 dark:text-amber-300"
      title={`Not a Radar-connected cluster. Connect ${rawLabel} to see workloads.`}
    >
      <AlertTriangle className="h-3 w-3 shrink-0" />
      <span className="block truncate">{shortClusterName(rawLabel)}</span>
    </span>
  )
}

// shortClusterName strips kubectl-context-style prefixes so cluster
// chips show the human-recognizable suffix instead of the full provider
// id. Operators recognize `management-cluster` instantly;
// `gke_koalabackend_me-west1-a_management-cluster` is ~40 chars of noise.
//
// Delegates to parseContextName (utils/context-name.ts), which already
// covers GKE / EKS ARN / AKS provider formats with a CodeQL-clean
// linear-time regex. Falls back to the raw input when the parser can't
// extract a cluster name (kind, k3d, user-named) — keeps the chip from
// rendering blank for malformed inputs.
export function shortClusterName(full: string): string {
  return parseContextName(full).clusterName || full
}

function TableCell({ children }: { children: ReactNode }) {
  // overflow-hidden is the belt to inner `truncate`'s suspenders — in a
  // table-fixed layout, a long unbroken token (kubectl-context cluster
  // names, OCI repo URLs) will visually bleed into the next column unless
  // the cell itself clips. Callers should still use `block truncate` on
  // single-line content for the ellipsis; this prevents the cosmetic
  // disaster when they forget.
  return <td className="overflow-hidden border-b border-theme-border px-3 py-2 align-middle text-theme-text-secondary">{children}</td>
}

// =============================================================================
// Helpers — pure functions for row sorting / filtering / formatting. Exported
// where the callers' normalize pipeline needs them (e.g. summarizeGitOpsRows
// is used in OSS for the page-header counts).
// =============================================================================

export function summarizeGitOpsRows(rows: GitOpsRow[]) {
  return rows.reduce(
    (summary, row) => {
      if (row.sync === 'OutOfSync') summary.outOfSync++
      if (row.health === 'Degraded') summary.degraded++
      if (row.health === 'Healthy') summary.healthy++
      if (row.health === 'Progressing') summary.progressing++
      if (row.suspended) summary.suspended++
      if (row.sync === 'Reconciling' || row.health === 'Progressing') summary.reconciling++
      return summary
    },
    { outOfSync: 0, degraded: 0, healthy: 0, progressing: 0, suspended: 0, reconciling: 0 },
  )
}

// Natural direction per column, used the first time a column is clicked: the
// urgency-ordered facets default ascending (most-urgent first); recency defaults
// to newest-first.
const SORT_DEFAULT_DIR: Record<SortKey, SortDir> = { urgency: 'asc', name: 'asc', health: 'asc', sync: 'asc', lastSync: 'desc', project: 'asc' }

// Ascending comparator per column (the caller flips it for descending). health
// ascends worst→best (urgencyRank 0 = broken); lastSync ascends oldest→newest.
function compareRows(a: GitOpsRow, b: GitOpsRow, sortKey: SortKey) {
  if (sortKey === 'urgency') return urgencyRank(a) - urgencyRank(b) || a.name.localeCompare(b.name)
  if (sortKey === 'health') return healthRank(a) - healthRank(b) || a.name.localeCompare(b.name)
  if (sortKey === 'sync') return syncRank(a.sync) - syncRank(b.sync) || a.name.localeCompare(b.name)
  if (sortKey === 'lastSync') return (Date.parse(a.lastSync || a.createdAt) || 0) - (Date.parse(b.lastSync || b.createdAt) || 0)
  if (sortKey === 'project') return a.project.localeCompare(b.project) || a.name.localeCompare(b.name)
  return a.name.localeCompare(b.name)
}

// Sort the Health column by health STATUS, worst first — so clicking the column
// groups rows the way its header implies. (This is deliberately NOT the old
// composite "urgency" rank that also weighed sync / auto-sync: that conflated two
// dimensions and made the Health column look broken — an OutOfSync-but-Healthy
// row sorting above a Synced-Progressing one. A sync-aware triage ordering is a
// reasonable separate default, but not what "sort by Health" should mean.)
const HEALTH_RANK: Record<string, number> = {
  Degraded: 0, Missing: 0, Suspended: 1, Unknown: 2, Progressing: 3, Healthy: 4,
}
function healthRank(row: GitOpsRow): number {
  return HEALTH_RANK[row.health] ?? 2
}

// urgencyRank groups rows by what the operator should do about them — the curated
// DEFAULT order (sortKey 'urgency'), surfacing broken/drifted apps first.
//   0 broken (Terminating/Degraded/Missing) · 1 OutOfSync, manual · 2 OutOfSync,
//   auto · 3 Progressing/Reconciling · 4 Unknown · 5 Suspended · 6 Synced+Healthy.
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
  return ({ OutOfSync: 0, Reconciling: 1, Unknown: 2, Synced: 3 } as Record<string, number>)[sync] ?? 2
}

function modeLabel(mode: GitOpsMode) {
  return ({ applications: 'Applications', sources: 'Sources', projects: 'Projects', alerts: 'Alerts' } as const)[mode]
}

function statusStripe(row: GitOpsRow) {
  if (row.terminating) return 'bg-orange-500'
  if (row.health === 'Degraded') return 'bg-red-500'
  if (row.health === 'Progressing' || row.sync === 'Reconciling') return 'bg-sky-500'
  if (row.sync === 'OutOfSync') return 'bg-amber-500'
  if (row.health === 'Healthy' && row.sync === 'Synced') return 'bg-emerald-500'
  return 'bg-theme-text-tertiary'
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

function recencyTone(value: string): string {
  if (!value) return 'text-theme-text-tertiary'
  const time = Date.parse(value)
  if (!Number.isFinite(time)) return 'text-theme-text-tertiary'
  const diffMs = Date.now() - time
  if (diffMs < 10 * 60_000) return 'text-emerald-600 dark:text-emerald-400'
  if (diffMs > 7 * 24 * 60 * 60_000) return 'text-amber-600 dark:text-amber-400'
  return 'text-theme-text-secondary'
}

function shortRevision(rev: string): string {
  if (rev.length <= 12) return rev
  if (/^[0-9a-f]{12,}$/i.test(rev)) return rev.slice(0, 7)
  return rev
}

function compactRepoSource(repo: string, path: string): string {
  if (!repo) return ''
  let head = repo.replace(/^https?:\/\//, '').replace(/\.git$/, '')
  head = head.replace(/^(github\.com|gitlab\.com|bitbucket\.org)\//, '')
  return path ? `${head} · ${path}` : head
}

function compactClusterURL(dest: string): string {
  return dest
    .replace(/^https?:\/\//, '')
    .replace(/^kubernetes\.default\.svc(:\d+)?\/?$/, 'in-cluster')
}

// formatRelativeAge — inline relative-time formatter. Returns "" for unparseable
// inputs so callers can treat empty as "no timestamp" and skip the time
// cell gracefully.
function formatRelativeAge(rfc3339: string): string {
  if (!rfc3339) return ''
  const time = Date.parse(rfc3339)
  if (!Number.isFinite(time)) return ''
  const diffMs = Date.now() - time
  const s = Math.max(0, Math.floor(diffMs / 1000))
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  const d = Math.floor(h / 24)
  if (d < 30) return `${d}d ago`
  const mo = Math.floor(d / 30)
  if (mo < 12) return `${mo}mo ago`
  const y = Math.floor(d / 365)
  return `${y}y ago`
}

// =============================================================================
// Normalize helpers — convert raw Argo / Flux unstructured CRDs into the
// GitOpsRow shape the table consumes. Exported so callers do their own
// fetch+normalize pipeline (OSS fetches per-kind from the radar API;
// hub-web fetches via the fleet endpoint then maps the response).
// =============================================================================

export function normalizeArgoApplication(resource: any): GitOpsRow {
  const status = getGitOpsResourceStatus('applications', resource)
  const dest = resource.spec?.destination?.server ?? resource.spec?.destination?.name ?? ''
  // history?.length on the SAME optional-chain so a missing history
  // doesn't crash on `.length`. Argo populates status before history
  // on freshly-created Applications (between create and first sync),
  // so this isn't theoretical.
  const argoHistory = resource.status?.history
  const argoLastSync = resource.status?.operationState?.finishedAt ?? (argoHistory && argoHistory.length > 0 ? argoHistory[argoHistory.length - 1]?.deployedAt : undefined)
  return {
    id: `argo/applications/${resource.metadata?.namespace ?? ''}/${resource.metadata?.name ?? ''}`,
    mode: 'applications',
    tool: 'argo',
    kindName: 'applications',
    kind: 'Application',
    group: 'argoproj.io',
    name: resource.metadata?.name ?? '',
    namespace: resource.metadata?.namespace ?? '',
    project: resource.spec?.project ?? '',
    labels: (resource.metadata?.labels ?? {}) as Record<string, string>,
    sync: status?.sync ?? 'Unknown',
    health: status?.health ?? 'Unknown',
    suspended: (status?.suspended ?? false) || isArgoSuspendedByRadar(resource),
    repository: resource.spec?.source?.repoURL ?? '',
    targetRevision: resource.spec?.source?.targetRevision ?? '',
    path: resource.spec?.source?.path ?? '',
    chart: resource.spec?.source?.chart ?? '',
    destination: dest,
    destinationNamespace: resource.spec?.destination?.namespace ?? '',
    createdAt: resource.metadata?.creationTimestamp ?? '',
    lastSync: argoLastSync ?? '',
    autoSync: Boolean(resource.spec?.syncPolicy?.automated),
    terminating: isTerminating(resource),
    terminationStartedAt: terminationStartedAt(resource),
    raw: resource,
    _cluster: resource._cluster,
    _destination: resource._destination,
  }
}

export function normalizeFluxKustomization(resource: any, fluxSourceUrls?: Map<string, string>): GitOpsRow {
  const status = getGitOpsResourceStatus('kustomizations', resource)
  const sourceName = resource.spec?.sourceRef?.name ?? ''
  const sourceKind = resource.spec?.sourceRef?.kind ?? ''
  const repository = fluxSourceUrls?.get(`${sourceKind}/${resource.metadata?.namespace ?? ''}/${sourceName}`) ?? sourceName
  return {
    id: `flux/kustomizations/${resource.metadata?.namespace ?? ''}/${resource.metadata?.name ?? ''}`,
    mode: 'applications',
    tool: 'flux',
    kindName: 'kustomizations',
    kind: 'Kustomization',
    group: 'kustomize.toolkit.fluxcd.io',
    name: resource.metadata?.name ?? '',
    namespace: resource.metadata?.namespace ?? '',
    project: resource.metadata?.namespace ?? '',
    labels: (resource.metadata?.labels ?? {}) as Record<string, string>,
    sync: status?.sync ?? 'Unknown',
    health: status?.health ?? 'Unknown',
    suspended: status?.suspended ?? resource.spec?.suspend === true,
    repository,
    targetRevision: resource.status?.lastAppliedRevision ?? resource.status?.lastAttemptedRevision ?? '',
    path: resource.spec?.path ?? '',
    chart: '',
    destination: 'in-cluster',
    destinationNamespace: resource.spec?.targetNamespace ?? resource.metadata?.namespace ?? '',
    createdAt: resource.metadata?.creationTimestamp ?? '',
    lastSync: newestConditionTime(resource),
    autoSync: !resource.spec?.suspend,
    terminating: isTerminating(resource),
    terminationStartedAt: terminationStartedAt(resource),
    raw: resource,
    _cluster: resource._cluster,
    _destination: resource._destination,
  }
}

export function normalizeFluxHelmRelease(resource: any, fluxSourceUrls?: Map<string, string>): GitOpsRow {
  const status = getGitOpsResourceStatus('helmreleases', resource)
  const chartSpec = resource.spec?.chart?.spec ?? {}
  const sourceName = chartSpec.sourceRef?.name ?? ''
  const sourceKind = chartSpec.sourceRef?.kind ?? ''
  const repository = fluxSourceUrls?.get(`${sourceKind}/${resource.metadata?.namespace ?? ''}/${sourceName}`) ?? sourceName
  return {
    id: `flux/helmreleases/${resource.metadata?.namespace ?? ''}/${resource.metadata?.name ?? ''}`,
    mode: 'applications',
    tool: 'flux',
    kindName: 'helmreleases',
    kind: 'HelmRelease',
    group: 'helm.toolkit.fluxcd.io',
    name: resource.metadata?.name ?? '',
    namespace: resource.metadata?.namespace ?? '',
    project: resource.metadata?.namespace ?? '',
    labels: (resource.metadata?.labels ?? {}) as Record<string, string>,
    sync: status?.sync ?? 'Unknown',
    health: status?.health ?? 'Unknown',
    suspended: status?.suspended ?? resource.spec?.suspend === true,
    repository,
    targetRevision: chartSpec.version ?? resource.status?.lastAttemptedRevision ?? '',
    path: '',
    chart: chartSpec.chart ?? '',
    destination: 'in-cluster',
    destinationNamespace: resource.spec?.targetNamespace ?? resource.metadata?.namespace ?? '',
    createdAt: resource.metadata?.creationTimestamp ?? '',
    lastSync: newestConditionTime(resource),
    autoSync: !resource.spec?.suspend,
    terminating: isTerminating(resource),
    terminationStartedAt: terminationStartedAt(resource),
    raw: resource,
    _cluster: resource._cluster,
    _destination: resource._destination,
  }
}

// buildFluxSourceUrlMap — index Flux source CRs (GitRepository, HelmRepository,
// OCIRepository, Bucket) by `<kind>/<namespace>/<name>` so the
// normalize* helpers can resolve `spec.sourceRef.name` → an actual URL.
// Without this, the Source column shows the opaque CR name only.
export function buildFluxSourceUrlMap(sources: any[]): Map<string, string> {
  const out = new Map<string, string>()
  for (const src of sources) {
    const kind = src.kind ?? ''
    const ns = src.metadata?.namespace ?? ''
    const name = src.metadata?.name ?? ''
    const url = src.spec?.url ?? src.spec?.endpoint ?? ''
    if (!kind || !name || !url) continue
    out.set(`${kind}/${ns}/${name}`, url)
  }
  return out
}

function isTerminating(resource: any): boolean {
  return Boolean(resource?.metadata?.deletionTimestamp)
}

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
