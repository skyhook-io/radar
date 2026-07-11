import { useMemo, useState, useEffect, useRef, useCallback } from 'react'
import type { ReactNode } from 'react'
import { ChevronRight, Layers, Boxes, HeartPulse, Shapes, Globe, Tag } from 'lucide-react'
import { clsx } from 'clsx'
import { StatusDot, mapHealthToTone } from '../ui/status-tone'
import { Tooltip } from '../ui/Tooltip'
import { EmptyState } from '../ui/EmptyState'
import { BoardTableSkeleton, BoardRailSkeleton } from '../ui/BoardSkeleton'
import { SearchBox } from '../ui/SearchBox'
import { useFilterState, defineFilterSchema } from '../../filter-state'
import { PageHeader } from '../ui/PageHeader'
import { SummaryTile, type SummaryTone } from '../ui/SummaryTile'
import { Facet, type FacetTone } from '../ui/Facet'
import { SortableTh, TH_CLASS } from '../ui/SortableTh'
import { DistributionBar } from '../ui/DistributionBar'
import { useRegisterShortcuts } from '../../hooks/useKeyboardShortcuts'
import { pluralize } from '../../utils/pluralize'
import {
  type AppEntry,
  type AppRow,
  type AppHealth,
  type AppWorkloadClass,
  type AppSource,
  type AppCategory,
  HEALTH_ORDER,
  HEALTH_RANK,
  HEALTH_META,
  CLASS_ORDER,
  CLASS_META,
  CATEGORY_ORDER,
  CATEGORY_META,
  CHIP,
  CHIP_TONE,
  SOURCE_ORDER,
  SOURCE_META,
  envRank,
  isSystemNamespace,
  searchTextForEntry,
  foldAppGroups,
  batchSignalForApp,
  batchRuntimeForApp,
  type FoldedRow,
} from '../../utils/applications'
import { ReadyBar } from './ReadyBar'
import { ProvenanceBadge, ClassBadge, CategoryChip, VersionInfo, BatchSignalChip } from './AppChips'
import { AppIdentityTooltip, EnvHint } from './AppTooltips'

function ApplicationRuntimeCell({ apps, workloadClass, ready, desired }: { apps: AppRow[]; workloadClass: AppWorkloadClass; ready: number; desired: number }) {
  if (workloadClass !== 'job') return <ReadyBar ready={ready} desired={desired} />
  const runtimes = apps.map(batchRuntimeForApp)
  const worst = runtimes.reduce((current, runtime) => (HEALTH_RANK[runtime.health] ?? 0) > (HEALTH_RANK[current.health] ?? 0) ? runtime : current, runtimes[0])
  if (!worst) return <span className="text-xs text-theme-text-tertiary">Idle</span>
  const same = runtimes.every((runtime) => runtime.label === worst.label)
  const label = same ? worst.label : `${runtimes.length} states`
  return (
    <Tooltip content={runtimes.map((runtime, index) => `${apps[index]?.name ?? 'app'}: ${runtime.label} — ${runtime.detail}`).join('\n')} delay={150}>
      <span className="inline-flex items-center gap-1.5 text-xs text-theme-text-secondary"><StatusDot tone={mapHealthToTone(worst.health)} />{label}</span>
    </Tooltip>
  )
}

function applicationRuntimeLabel(apps: AppRow[], workloadClass: AppWorkloadClass, health: AppHealth): string {
  if (workloadClass !== 'job') return HEALTH_META[health].label
  const labels = Array.from(new Set(apps.map((app) => batchRuntimeForApp(app).label)))
  return labels.length === 1 ? labels[0] : 'Multiple batch states'
}

// ApplicationsView — the shared, variant-agnostic core behind the Applications
// surface. It owns the entire list chassis: a health hero header (PageHeader +
// SummaryTiles + DistributionBar), a left facet rail (Availability / Class /
// Type / Environment / Source + a single-cluster-only Show-system toggle), a
// search toolbar, sortable columns, app-group folding, and j/k keyboard nav.
// Data is injected as a discriminated AppEntry[] — the OSS single-cluster list
// and the Cloud fleet list both drive it, branching only on column headers and
// the per-instance row. Styling mirrors the Resources table so the surfaces
// read as one design.

// Status is the one health facet — map app health onto the shared facet
// tone so its dots read red/amber/green/grey like the GitOps sync+health facets.
const HEALTH_TONE: Record<AppHealth, FacetTone> = {
  unhealthy: 'error',
  degraded: 'warning',
  healthy: 'success',
  neutral: 'info', // Idle — sky, calm
  unknown: 'neutral',
}

// Sortable columns. `health` is the implicit default (worst-first then name);
// clicking a sortable header cycles asc → desc → off (back to default).
type SortKey = 'name' | 'ready' | 'version'
type SortDir = 'asc' | 'desc'

function compareEntries(a: AppEntry, b: AppEntry, key: SortKey): number {
  switch (key) {
    case 'name':
      return a.row.name.localeCompare(b.row.name)
    case 'ready':
      return runtimeSortValue(a) - runtimeSortValue(b)
    case 'version': {
      // Sort by distinct-version count first (skewed apps cluster), then the
      // first tag for a stable, human-meaningful order.
      const byCount = a.versions.length - b.versions.length
      if (byCount !== 0) return byCount
      return (a.versions[0] ?? '').localeCompare(b.versions[0] ?? '')
    }
  }
}

function runtimeSortValue(entry: AppEntry): number {
  return entry.workloadClass === 'job' ? (HEALTH_RANK[entry.health] ?? 0) : entry.readyRatio
}

// The env token an entry filters under. Single entries carry one env; fleet
// entries carry several — a fleet row matches an env facet if ANY of its slices
// do (the same inclusive policy as the Class facet).
function entryEnvs(e: AppEntry): string[] {
  return e.variant === 'single' ? [e.env || 'none'] : e.envs.map((s) => s.env || 'none')
}

export interface ApplicationsViewProps {
  entries: AppEntry[]
  variant: 'single' | 'fleet'
  onSelect: (key: string) => void
  title?: string
  description?: string
  /** Rendered instead of the built-in EmptyState when there are zero entries
   *  pre-filter (the fleet host injects a coverage/offline-aware empty). */
  emptySlot?: ReactNode
  /** Leading element in the header actions cluster (e.g. a freshness control). */
  headerActions?: ReactNode
  /** First fetch in flight — render the shape-stable skeleton (pulsing tiles,
   *  rail stubs, table rows) instead of collapsing the chassis. */
  loading?: boolean
}

const APPS_FILTER_SCHEMA = defineFilterSchema({
  health: { param: 'health', type: 'set' },
  class: { param: 'class', type: 'set' },
  type: { param: 'type', type: 'set' },
  env: { param: 'env', type: 'set' },
  source: { param: 'source', type: 'set' },
  q: { param: 'q', type: 'text' },
  system: { param: 'system', type: 'boolean' },
})

export function ApplicationsView({ entries: allEntries, variant, onSelect, title = 'Applications', description, emptySlot, headerActions, loading }: ApplicationsViewProps) {
  // Facets + search + show-system live in the URL (shareable, bookmarkable) via
  // the shared filter-state contract. Sort stays local: it's a compound
  // {key, dir} view-preference, not a result-narrowing filter, so it isn't
  // forced into the filter vocabulary.
  const filters = useFilterState(APPS_FILTER_SCHEMA)
  const textFilter = filters.values.q
  const fHealth = filters.values.health as Set<AppHealth>
  const fEnv = filters.values.env
  const fSource = filters.values.source as Set<AppSource>
  const fClass = filters.values.class as Set<AppWorkloadClass>
  const fType = filters.values.type as Set<AppCategory>
  const showSystem = filters.values.system
  const [sort, setSort] = useState<{ key: SortKey; dir: SortDir } | null>(null)

  // The Show-system toggle keys off per-entry workload namespaces, which only
  // single-cluster entries carry — fleet entries have no namespace facet, so
  // the toggle is computed and shown for the single variant only.
  // An app counts as system only when EVERY workload namespace is system —
  // hiding a partly-user app would be worse than showing a partly-system one.
  const isSystemApp = (e: AppEntry) =>
    e.variant === 'single' && e.namespaces.length > 0 && e.namespaces.every(isSystemNamespace)
  const all = useMemo(
    () => (variant !== 'single' || showSystem ? allEntries : allEntries.filter((e) => !isSystemApp(e))),
    [allEntries, showSystem, variant],
  )
  const systemCount = useMemo(() => (variant === 'single' ? allEntries.filter(isSystemApp).length : 0), [allEntries, variant])

  const entries = useMemo(() => {
    const t = textFilter.trim().toLowerCase()
    const filtered = all.filter((e) => {
      if (t && !searchTextForEntry(e).includes(t)) return false
      if (fHealth.size && !fHealth.has(e.health)) return false
      // Inclusive: a mixed app matches the filter of ANY class it contains.
      if (fClass.size && !e.classSet.some((c) => fClass.has(c))) return false
      if (fType.size && !fType.has(e.category)) return false
      if (fSource.size && !fSource.has(e.source)) return false
      if (fEnv.size && !entryEnvs(e).some((env) => fEnv.has(env))) return false
      return true
    })
    if (sort) {
      const factor = sort.dir === 'asc' ? 1 : -1
      filtered.sort((a, b) => compareEntries(a, b, sort.key) * factor || a.row.name.localeCompare(b.row.name))
    } else {
      filtered.sort((a, b) => (HEALTH_RANK[b.health] ?? 0) - (HEALTH_RANK[a.health] ?? 0) || a.row.name.localeCompare(b.row.name))
    }
    return filtered
  }, [all, textFilter, fHealth, fEnv, fSource, fClass, fType, sort])

  // ── App groups ──────────────────────────────────────────────────────────────
  // Instances sharing a wire `identity` fold into one ladder row (foldAppGroups).
  // THE COLLAPSE EXPERIMENT: default collapsed, contingent on (a) text search
  // auto-expanding into hidden instances, (b) instance rows one chevron away,
  // (c) the group chip visibly carrying confidence. If heuristic precision
  // disappoints in the field, default-expand by seeding expandedGroups with
  // every group key instead of an empty set.
  const FAMILY_AUTO_EXPAND_ON_SEARCH = true
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(new Set())
  const toggleGroup = (key: string) =>
    setExpandedGroups((s) => {
      const n = new Set(s)
      n.has(key) ? n.delete(key) : n.add(key)
      return n
    })

  // Fleet rows must scope non-portable identities to the cluster set that
  // produced them — only a portable (declared / cross-cluster-unified) key may
  // fold across clusters. Single-cluster has no such concern, so no localScope.
  const visibleRows = useMemo<FoldedRow<AppEntry>[]>(
    () => foldAppGroups(entries, expandedGroups, FAMILY_AUTO_EXPAND_ON_SEARCH && textFilter.trim() !== '',
      variant === 'fleet'
        ? {
            // Fall back to the row key so the scope is never empty — an empty
            // localScope would make foldAppGroups treat a non-portable identity
            // as un-scoped and fold it across unrelated rows.
            localScope: (e) => (e.variant === 'fleet' ? e.clusters.map((c) => c.id).sort().join(',') || e.row.key : ''),
            // The fold ladder reads the host's per-cluster env slices, not the
            // single identity.env (which the hub can stale when it joins one
            // overlay key across clusters with different envs). Fall back to the
            // identity env if a row somehow has no slices, so the ladder never
            // collapses to a misleading "0 envs".
            envsOf: (e) =>
              e.variant === 'fleet'
                ? e.envs.length
                  ? e.envs.map((s) => ({ env: s.env, health: s.health }))
                  : [{ env: e.row.identity?.env ?? '', health: e.health }]
                : [],
          }
        : undefined,
    ),
    [entries, expandedGroups, textFilter, variant],
  )

  // Row keyboard navigation — same contract as the Resources table: j/k or
  // arrows move a highlight, g g / G jump, Enter opens, Escape clears the
  // highlight. The search box hands off via ArrowDown.
  const [highlightedIndex, setHighlightedIndex] = useState(-1)
  const rowsRef = useRef(visibleRows)
  rowsRef.current = visibleRows
  useEffect(() => setHighlightedIndex(-1), [visibleRows])
  const firstOpenableVisibleRow = useCallback(() => {
    const first = rowsRef.current[0]
    if (!first) return null
    return first.kind === 'group' ? first.cells[0]?.firstKey ?? null : first.entry.row.key
  }, [])
  const moveHighlight = (delta: number) =>
    setHighlightedIndex((i) => Math.min(Math.max(i + delta, 0), rowsRef.current.length - 1))
  useRegisterShortcuts([
    { id: 'applications-nav-down', keys: 'j', description: 'Next row', category: 'Table', scope: 'applications', handler: () => moveHighlight(1) },
    { id: 'applications-nav-down-arrow', keys: 'ArrowDown', description: 'Next row', category: 'Table', scope: 'applications', handler: () => moveHighlight(1) },
    { id: 'applications-nav-up', keys: 'k', description: 'Previous row', category: 'Table', scope: 'applications', handler: () => moveHighlight(-1) },
    { id: 'applications-nav-up-arrow', keys: 'ArrowUp', description: 'Previous row', category: 'Table', scope: 'applications', handler: () => moveHighlight(-1) },
    { id: 'applications-nav-top', keys: 'g g', description: 'Jump to first row', category: 'Table', scope: 'applications', handler: () => setHighlightedIndex(rowsRef.current.length > 0 ? 0 : -1) },
    { id: 'applications-nav-bottom', keys: 'G', description: 'Jump to last row', category: 'Table', scope: 'applications', handler: () => setHighlightedIndex(rowsRef.current.length - 1) },
    {
      id: 'applications-open', keys: 'Enter', description: 'Open application', category: 'Table', scope: 'applications',
      handler: () => {
        const r = rowsRef.current[highlightedIndex]
        if (!r) return
        // Enter on a group toggles it; on an instance, opens it.
        if (r.kind === 'group') toggleGroup(r.key)
        else onSelect(r.entry.row.key)
      },
      enabled: highlightedIndex >= 0,
    },
    {
      id: 'applications-escape', keys: 'Escape', description: 'Clear row highlight', category: 'Table', scope: 'applications',
      handler: () => setHighlightedIndex(-1),
      enabled: highlightedIndex >= 0,
    },
  ])

  const counts = useMemo(() => {
    const health: Record<string, number> = {}
    const env: Record<string, number> = {}
    const source: Record<string, number> = {}
    const workloadClass: Record<string, number> = {}
    const category: Record<string, number> = {}
    for (const e of all) {
      health[e.health] = (health[e.health] ?? 0) + 1
      for (const c of e.classSet) workloadClass[c] = (workloadClass[c] ?? 0) + 1
      source[e.source] = (source[e.source] ?? 0) + 1
      category[e.category] = (category[e.category] ?? 0) + 1
      for (const en of entryEnvs(e)) env[en] = (env[en] ?? 0) + 1
    }
    return { health, env, source, workloadClass, category }
  }, [all])

  // asc → desc → off (null = default health-worst-first sort).
  const onSort = (key: SortKey) => {
    setSort((prev) => {
      if (!prev || prev.key !== key) return { key, dir: 'asc' }
      if (prev.dir === 'asc') return { key, dir: 'desc' }
      return null
    })
  }

  const total = all.length
  const envOptions = Object.entries(counts.env)
    .sort((a, b) => (envRank(b[0] === 'none' ? undefined : b[0]) ?? -1) - (envRank(a[0] === 'none' ? undefined : a[0]) ?? -1))
    .map(([env, count]) => ({ value: env, label: env === 'none' ? 'unlabeled' : env, count }))

  // First fetch, nothing to show yet: drive the shape-stable skeleton. The
  // loaded state hides zero-count health tiles, so without this the header
  // (tiles + distribution bar), rail, and table would all pop in at once.
  const initialLoading = Boolean(loading) && allEntries.length === 0

  // Clickable status tile wired to the health facet — tap to filter to that tier.
  const healthTile = (h: AppHealth, tone: SummaryTone) =>
    initialLoading && (h === 'healthy' || h === 'degraded' || h === 'unhealthy') ? (
      <SummaryTile key={h} label={HEALTH_META[h].label} value={0} tone={tone} loading />
    ) : counts.health[h] ? (
      <SummaryTile key={h} label={HEALTH_META[h].label} value={counts.health[h]} tone={tone} active={fHealth.has(h)} onClick={() => filters.toggle('health', h)} />
    ) : null

  // showSystem lives in the Filters rail, so Clear resets it too (and its
  // non-default ON state counts as an active filter that surfaces the button).
  const anyFilterActive = filters.isActive
  const clearAllFilters = () => {
    filters.clearAll()
  }

  return (
    <div className="flex h-full w-full min-w-0 flex-1 flex-col overflow-hidden bg-theme-base">
      {/* Header band: title + description + clickable status tiles + slim health
          bar. Same chassis as the GitOps view so the two list surfaces read as
          siblings (status in the header, filters in a left rail, search in a
          toolbar). */}
      <div className="shrink-0 border-b border-theme-border px-4 py-4">
        <PageHeader
          icon={Boxes}
          title={title}
          description={description}
          actions={
            <>
              {headerActions}
              <SummaryTile label={total === 1 ? 'application' : 'applications'} value={total} loading={initialLoading} />
              {healthTile('unhealthy', 'error')}
              {healthTile('degraded', 'warning')}
              {healthTile('healthy', 'success')}
              {healthTile('neutral', 'info')}
              {healthTile('unknown', 'neutral')}
            </>
          }
        />
        {initialLoading ? (
          <div className="mt-3 h-1.5 w-full animate-pulse rounded-full bg-theme-hover" aria-hidden />
        ) : (
          <DistributionBar
            className="mt-3"
            ariaLabel="Health distribution"
            segments={HEALTH_ORDER.map((h) => ({ key: h, count: counts.health[h] ?? 0, fillClass: HEALTH_META[h].bar }))}
          />
        )}
      </div>

      {/* Body: filter sidebar | content (toolbar + table). */}
      <div className="flex min-w-0 flex-1 overflow-hidden max-[899px]:flex-col">
        {/* Filters sidebar — titled, with Clear; mirrors the GitOps facet rail.
            Arbitrary 900px breakpoint (not `sm`) so the collapse is independent of
            the consuming app's Tailwind breakpoint config (Hub uses defaults). */}
        <aside className="flex w-52 shrink-0 flex-col overflow-hidden border-r border-theme-border bg-theme-surface/90 max-[899px]:max-h-72 max-[899px]:w-full max-[899px]:border-b max-[899px]:border-r-0">
          <div className="flex items-center justify-between border-b border-theme-border px-3 py-2">
            <span className="text-sm font-medium text-theme-text-secondary">Filters</span>
            {anyFilterActive && (
              <button type="button" onClick={clearAllFilters} className="text-[10px] font-medium text-blue-500 hover:text-blue-400">Clear</button>
            )}
          </div>
          <div className="flex-1 overflow-y-auto">
            {initialLoading ? (
              <BoardRailSkeleton
                sections={[
                  ['Status', 5],
                  ['Class', 3],
                  ['Type', 4],
                  ['Environment', 3],
                  ['Source', 3],
                ]}
              />
            ) : (
              <>
            <Facet icon={HeartPulse} title="Status" options={HEALTH_ORDER.map((h) => ({ value: h, label: HEALTH_META[h].label, count: counts.health[h] ?? 0, tone: HEALTH_TONE[h] }))} selected={fHealth} onToggle={(v) => filters.toggle('health', v)} />
            <Facet icon={Layers} title="Class" options={CLASS_ORDER.map((c) => ({ value: c, label: CLASS_META[c].label, count: counts.workloadClass[c] ?? 0 }))} selected={fClass} onToggle={(v) => filters.toggle('class', v)} />
            <Facet icon={Shapes} title="Type" options={CATEGORY_ORDER.map((c) => ({ value: c, label: CATEGORY_META[c].label, count: counts.category[c] ?? 0, tooltip: CATEGORY_META[c].tooltip }))} selected={fType} onToggle={(v) => filters.toggle('type', v)} />
            <Facet icon={Globe} title="Environment" info={<EnvHint />} options={envOptions} selected={fEnv} onToggle={(v) => filters.toggle('env', v)} />
            <Facet icon={Tag} title="Source" options={SOURCE_ORDER.map((s) => ({ value: s, label: SOURCE_META[s].label, count: counts.source[s] ?? 0 }))} selected={fSource} onToggle={(v) => filters.toggle('source', v)} />
            {systemCount > 0 && (
              <label className="flex cursor-pointer items-center gap-2 border-b border-theme-border px-3 py-2 text-[11px] text-theme-text-secondary hover:bg-theme-hover">
                <input type="checkbox" checked={showSystem} onChange={(e) => filters.setBoolean('system', e.target.checked)} className="accent-skyhook-500" />
                <span>Show system namespaces</span>
                <span className="ml-auto tabular-nums text-theme-text-tertiary">{systemCount}</span>
              </label>
            )}
              </>
            )}
          </div>
        </aside>

        {/* Content: toolbar (search + sort) over the scrollable table. */}
        <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
          <div className="flex shrink-0 items-center gap-3 border-b border-theme-border px-4 py-3">
            <SearchBox
              value={textFilter}
              onChange={(v) => filters.setString('q', v)}
              scope="applications"
              shortcutId="applications-search"
              className="max-w-md flex-1"
              onEnter={() => {
                const key = highlightedIndex >= 0 && rowsRef.current[highlightedIndex]?.kind === 'instance'
                  ? (rowsRef.current[highlightedIndex] as Extract<FoldedRow<AppEntry>, { kind: 'instance' }>).entry.row.key
                  : firstOpenableVisibleRow()
                if (key) onSelect(key)
              }}
              onArrowDown={() => {
                if (visibleRows.length > 0) setHighlightedIndex(0)
              }}
            />
            {/* Sorting is via the clickable column headers (Resources-table
                pattern) — no separate sort control. */}
          </div>

          <div className="min-w-0 flex-1 overflow-auto bg-theme-base">
          {initialLoading ? (
            <BoardTableSkeleton />
          ) : entries.length === 0 ? (
            emptySlot && allEntries.length === 0 ? (
              emptySlot
            ) : (
              <div className="p-4">
                <EmptyState
                  tone="filtered"
                  variant="card"
                  headline={total === 0 ? 'No applications detected yet' : 'No applications match the filters'}
                  body={total === 0 ? 'Deploy services, workers, or jobs to this cluster to see them grouped by app.' : 'Clear the filters above.'}
                />
              </div>
            )
          ) : (
              <table className="w-full text-left text-sm">
                <thead className="sticky top-0 z-10 bg-theme-base">
                  <tr>
                    <SortableTh label="Application" sortKey="name" activeKey={sort?.key ?? null} direction={sort?.dir ?? 'asc'} onSort={onSort} />
                    {variant === 'single' ? (
                      <>
                        <th className={TH_CLASS}>Namespace</th>
                        <th className={TH_CLASS}>Env</th>
                      </>
                    ) : (
                      <>
                        <th className={TH_CLASS}>Cluster</th>
                        <th className={TH_CLASS}>Envs</th>
                      </>
                    )}
                    <th className={TH_CLASS}>Class</th>
                    <SortableTh label="Runtime" sortKey="ready" activeKey={sort?.key ?? null} direction={sort?.dir ?? 'asc'} onSort={onSort} />
                    <SortableTh label="Version" sortKey="version" activeKey={sort?.key ?? null} direction={sort?.dir ?? 'asc'} onSort={onSort} />
                    <th className={TH_CLASS}>Workloads</th>
                    <th className={clsx(TH_CLASS, 'w-8')} />
                  </tr>
                </thead>
                <tbody>
                  {visibleRows.map((r, idx) => r.kind === 'group' ? (
                    <tr
                      key={`group:${r.key}`}
                      ref={idx === highlightedIndex ? (el) => el?.scrollIntoView({ block: 'nearest' }) : undefined}
                      aria-expanded={r.expanded}
                      className={clsx(
                        'group/row cursor-pointer border-b-subtle',
                        idx === highlightedIndex ? 'selection selection-ring' : 'hover:bg-theme-hover',
                      )}
                      onClick={() => toggleGroup(r.key)}
                    >
                      <td className="py-2.5 pl-3 pr-2">
                        <span className="flex items-center gap-2">
                          {/* Left status stripe (worst-child health) — the row status
                              gutter shared with the GitOps table. */}
                          <Tooltip content={applicationRuntimeLabel(r.members.map((member) => member.row), r.workloadClass, r.health)} delay={150}>
                            <span className={clsx('h-8 w-1 shrink-0 rounded-full', HEALTH_META[r.health].bar)} />
                          </Tooltip>
                          <ChevronRight className={clsx('h-3.5 w-3.5 shrink-0 text-theme-text-tertiary transition-transform', r.expanded && 'rotate-90')} aria-hidden />
                          <span className="truncate font-semibold text-theme-text-primary">{r.label}</span>
                          {r.workloadClass !== 'job' && <BatchSignalChip signal={firstBatchSignal(r.members.map((m) => m.row))} />}
                          <Tooltip
                            content={<AppIdentityTooltip identityKey={r.label} source={r.members[0]?.row.identity?.source} portable={r.members[0]?.row.identity?.portable} fleet={variant === 'fleet'} members={r.members.map((m) => ({ name: m.row.name, env: m.row.identity!.env, confidence: m.row.identity!.confidence, evidence: m.row.identity!.evidence }))} />}
                            delay={150}
                          >
                            <span className={`${CHIP} ${r.confidence === 'high' ? CHIP_TONE.emerald : CHIP_TONE.neutral}`}>
                              <Layers className="mr-1 h-3 w-3" aria-hidden />{r.cells.length} envs
                            </span>
                          </Tooltip>
                        </span>
                      </td>
                      <td className="px-2 py-2.5">
                        <span className="text-xs text-theme-text-tertiary">{pluralize(r.members.length, 'instance')}</span>
                      </td>
                      <td className="px-2 py-2.5">
                        {/* The ladder: env-ordered cells; click drills into that env's instance. */}
                        <span className="flex flex-wrap items-center gap-1">
                          {/* Ladder cells scale-capped: a handful inline, the
                              rest behind "+N" (expand shows every instance). */}
                          {r.cells.slice(0, 4).map((c) => (
                            <Tooltip key={c.env} content={`${c.env}${c.version ? ` · ${c.version}` : ''}${c.count > 1 ? ` · ${c.count} instances — expand to choose` : ' — open'}`} delay={150}>
                              <button
                                type="button"
                                onClick={(ev) => {
                                  ev.stopPropagation()
                                  if (c.count > 1) toggleGroup(r.key)
                                  else onSelect(c.firstKey)
                                }}
                                className={`${CHIP} ${CHIP_TONE.neutral} gap-1 hover:bg-theme-hover`}
                              >
                                <StatusDot tone={mapHealthToTone(c.health)} />{c.env}
                              </button>
                            </Tooltip>
                          ))}
                          {r.cells.length > 4 && (
                            <Tooltip content={`${r.cells.length - 4} more environments — expand to see all instances`} delay={150}>
                              <button
                                type="button"
                                onClick={(ev) => { ev.stopPropagation(); toggleGroup(r.key) }}
                                className={`${CHIP} ${CHIP_TONE.muted} hover:bg-theme-hover`}
                              >
                                +{r.cells.length - 4}
                              </button>
                            </Tooltip>
                          )}
                        </span>
                      </td>
                      <td className="px-2 py-2.5"><ClassBadge workloadClass={r.workloadClass} composition={r.classComposition} /></td>
                      <td className="px-2 py-2.5"><ApplicationRuntimeCell apps={r.members.map((member) => member.row)} workloadClass={r.workloadClass} ready={r.ready} desired={r.desired} /></td>
                      <td className="px-2 py-2.5">
                        {r.lag ? (
                          <Tooltip content={`Promotion lag: ${r.lag} (${r.cells.filter((c) => c.version).map((c) => `${c.env}=${c.version}`).join(', ')})`} delay={150}>
                            <span className={`${CHIP} ${CHIP_TONE.amber}`}>{r.lag}</span>
                          </Tooltip>
                        ) : (
                          <span className="text-theme-text-tertiary">—</span>
                        )}
                      </td>
                      <td className="px-2 py-2.5">
                        <span className="text-xs text-theme-text-secondary">{Object.entries(r.kinds).map(([k, n]) => pluralize(n, k)).join(' · ')}</span>
                      </td>
                      <td className="pr-2 text-right" />
                    </tr>
                  ) : ((e) => (
                    <tr
                      key={e.row.key}
                      ref={idx === highlightedIndex ? (el) => el?.scrollIntoView({ block: 'nearest' }) : undefined}
                      className={clsx(
                        'group/row cursor-pointer border-b-subtle',
                        idx === highlightedIndex ? 'selection selection-ring' : 'hover:bg-theme-hover',
                      )}
                      onClick={() => onSelect(e.row.key)}
                    >
                      <td className={clsx('py-2.5 pr-2', r.child ? 'pl-10' : 'pl-3')}>
                        <span className="flex items-center gap-2">
                          <Tooltip content={applicationRuntimeLabel([e.row], e.workloadClass, e.health)} delay={150}>
                            <span className={clsx('h-8 w-1 shrink-0 rounded-full', HEALTH_META[e.health].bar)} />
                          </Tooltip>
                          <span className="truncate font-medium text-theme-text-primary">{e.row.name}</span>
                          {e.workloadClass !== 'job' && <BatchSignalChip signal={batchSignalForApp(e.row)} />}
                          <ProvenanceBadge tier={e.row.tier} appKey={e.row.key} confidence={e.row.confidence} />
                          <CategoryChip category={e.category} addonReason={e.row.addonReason} />
                        </span>
                      </td>
                      {e.variant === 'single' ? (
                        <>
                          <td className="px-2 py-2.5">
                            {e.namespace ? (
                              <span className="truncate font-mono text-xs text-theme-text-secondary">{e.namespace}</span>
                            ) : e.namespaces.length > 1 ? (
                              <Tooltip content={e.namespaces.join(', ')} delay={150}>
                                <span className="text-xs text-theme-text-secondary">{e.namespaces.length} namespaces</span>
                              </Tooltip>
                            ) : (
                              <span className="text-theme-text-tertiary">—</span>
                            )}
                          </td>
                          <td className="px-2 py-2.5">
                            {e.env ? (
                              e.envInferred ? (
                                <Tooltip content={`Inferred from namespace "${e.namespace || e.env}" — confirm with an environment label.`} delay={150}>
                                  <span className={`${CHIP} italic ${CHIP_TONE.muted}`}>~{e.env}</span>
                                </Tooltip>
                              ) : (
                                <span className={`${CHIP} ${CHIP_TONE.neutral}`}>{e.env}</span>
                              )
                            ) : (
                              <Tooltip content={<EnvHint unlabeled />} delay={300}>
                                <span className="cursor-default text-theme-text-tertiary">—</span>
                              </Tooltip>
                            )}
                          </td>
                        </>
                      ) : (
                        <>
                          <td className="px-2 py-2.5">
                            {e.clusters.length === 1 ? (
                              <span className="truncate text-xs text-theme-text-secondary">{e.clusters[0].name}</span>
                            ) : e.clusters.length > 1 ? (
                              <Tooltip content={e.clusters.map((c) => c.name).join(', ')} delay={150}>
                                <span className="text-xs text-theme-text-secondary">{e.clusters.length} clusters</span>
                              </Tooltip>
                            ) : (
                              <span className="text-theme-text-tertiary">—</span>
                            )}
                          </td>
                          <td className="px-2 py-2.5">
                            {e.envs.length > 0 ? (
                              <span className="flex flex-wrap items-center gap-1">
                                {e.envs.map((s) =>
                                  s.inferred ? (
                                    <Tooltip key={s.env || 'none'} content={`${s.env || 'unlabeled'} — inferred from namespace; confirm with an environment label.`} delay={150}>
                                      <span className={`${CHIP} italic ${CHIP_TONE.muted} gap-1`}><StatusDot tone={mapHealthToTone(s.health)} />~{s.env}</span>
                                    </Tooltip>
                                  ) : (
                                    <span key={s.env || 'none'} className={`${CHIP} ${CHIP_TONE.neutral} gap-1`}><StatusDot tone={mapHealthToTone(s.health)} />{s.env || 'unlabeled'}</span>
                                  ),
                                )}
                              </span>
                            ) : (
                              <span className="text-theme-text-tertiary">—</span>
                            )}
                          </td>
                        </>
                      )}
                      <td className="px-2 py-2.5"><ClassBadge workloadClass={e.workloadClass} composition={e.classComposition} /></td>
                      <td className="px-2 py-2.5"><ApplicationRuntimeCell apps={[e.row]} workloadClass={e.workloadClass} ready={e.ready} desired={e.desired} /></td>
                      <td className="px-2 py-2.5">
                        {e.variant === 'fleet' ? (
                          e.versionSkew ? (
                            <Tooltip content={`Version skew across clusters: ${e.versions.join(', ')}`} delay={150}>
                              <span className={`${CHIP} ${CHIP_TONE.amber}`}>version skew</span>
                            </Tooltip>
                          ) : e.versions.length === 1 ? (
                            <span className="font-mono text-xs text-theme-text-secondary">{e.versions[0]}</span>
                          ) : e.versions.length > 1 ? (
                            <Tooltip content={e.versions.join(', ')} delay={150}>
                              <span className={`${CHIP} ${CHIP_TONE.neutral}`}>{e.versions.length} versions</span>
                            </Tooltip>
                          ) : (
                            <span className="text-theme-text-tertiary">—</span>
                          )
                        ) : (
                          <VersionInfo app={e.row} variant="cell" />
                        )}
                      </td>
                      <td className="px-2 py-2.5">
                        {Object.keys(e.kinds).length === 0 ? (
                          <span className="text-xs text-theme-text-tertiary">—</span>
                        ) : (
                          <span className="text-xs text-theme-text-secondary">{Object.entries(e.kinds).map(([k, n]) => pluralize(n, k)).join(' · ')}</span>
                        )}
                      </td>
                      <td className="pr-2 text-right"><ChevronRight className="inline h-4 w-4 text-theme-text-tertiary" /></td>
                    </tr>
                  ))(r.entry))}
                </tbody>
              </table>
          )}
          </div>
        </div>
      </div>
    </div>
  )
}

function firstBatchSignal(rows: AppRow[]) {
  let best = null as ReturnType<typeof batchSignalForApp>
  const rank = (tone: string | undefined) => {
    switch (tone) {
      case 'rose':
        return 5
      case 'amber':
        return 4
      case 'sky':
        return 3
      case 'emerald':
        return 2
      default:
        return 0
    }
  }
  for (const row of rows) {
    const signal = batchSignalForApp(row)
    if (signal && rank(signal.tone) > rank(best?.tone)) best = signal
  }
  return best
}
