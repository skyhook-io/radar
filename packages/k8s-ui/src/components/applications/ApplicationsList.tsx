import { useMemo, useState, useEffect, useRef, useCallback } from 'react'
import { ChevronRight, ChevronUp, ChevronDown, Layers, Info } from 'lucide-react'
import { clsx } from 'clsx'
import { StatusDot, mapHealthToTone } from '../ui/status-tone'
import { Tooltip } from '../ui/Tooltip'
import { EmptyState } from '../ui/EmptyState'
import { SearchBox } from '../ui/SearchBox'
import { useRegisterShortcuts } from '../../hooks/useKeyboardShortcuts'
import { pluralize } from '../../utils/pluralize'
import {
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
  categoryOf,
  envRank,
  healthOf,
  isSystemNamespace,
  namespaceOf,
  namespacesOf,
  resolveEnv,
  identityEnvInferred,
  sourceOf,
  workloadClassOf,
  classSetOf,
  classCompositionOf,
  foldAppGroups,
  type FoldedRow,
} from '../../utils/applications'
import { ReadyBar } from './ReadyBar'
import { ProvenanceBadge, ClassBadge, CategoryChip, VersionInfo } from './AppChips'
import { AppIdentityTooltip, EnvHint } from './AppTooltips'

// ApplicationsList — pure, single-cluster dense list of logical apps. Health
// dot + name + provenance/add-on/mixed chips; a Namespace column; an env pill
// (namespace-inferred shown as ~env); class; ready bar; version; workloads. A
// facet rail + a health hero bar sit alongside. Data + selection are injected.
// Styling mirrors the Resources table so the two read as one design.

interface AppEntry {
  row: AppRow
  health: AppHealth
  versions: string[]
  namespace: string
  namespaces: string[]
  env: string
  envInferred: boolean
  kinds: Record<string, number>
  workloadClass: AppWorkloadClass
  /** Distinct contained classes — the inclusive facet-matching set. */
  classSet: AppWorkloadClass[]
  classComposition: { cls: AppWorkloadClass; count: number }[]
  category: AppCategory
  ready: number
  desired: number
  /** ready/desired as a fraction for sorting; -1 when nothing is desired. */
  readyRatio: number
  source: AppSource
}

function buildEntry(row: AppRow, discoveredEnvs?: ReadonlySet<string>): AppEntry {
  const kinds: Record<string, number> = {}
  let ready = 0
  let desired = 0
  for (const wl of row.workloads || []) {
    kinds[wl.kind] = (kinds[wl.kind] ?? 0) + 1
    ready += wl.ready ?? 0
    desired += wl.desired ?? 0
  }
  const namespace = namespaceOf(row)
  // The server's identity classification carries the authoritative env (label/
  // declared/discovered); plain rows fall back to the trio + discovered-token
  // namespace heuristic.
  const resolved = resolveEnv(undefined, namespace, discoveredEnvs)
  const env = row.identity?.env ?? resolved.env
  const inferred = row.identity ? identityEnvInferred(row.identity) : resolved.inferred
  return {
    row,
    health: healthOf(row.health),
    versions: Array.from(new Set((row.versions || []).filter(Boolean))),
    namespace,
    namespaces: namespacesOf(row),
    env,
    envInferred: inferred,
    kinds,
    workloadClass: workloadClassOf(row.workload_class),
    classSet: classSetOf(row),
    classComposition: classCompositionOf(row),
    category: categoryOf(row.category),
    ready,
    desired,
    readyRatio: desired > 0 ? ready / desired : -1,
    source: sourceOf(row.tier),
  }
}

const envLabel = (env: string) => (env ? env : 'unlabeled')

function searchTextForEntry(e: AppEntry): string {
  const workloadText = (e.row.workloads || []).flatMap((wl) => [wl.kind, wl.namespace, wl.name, wl.version])
  return [
    e.row.name,
    e.row.key,
    e.namespace,
    SOURCE_META[e.source].label,
    CLASS_META[e.workloadClass].label,
    ...e.classSet.map((c) => CLASS_META[c].label),
    CATEGORY_META[e.category].label,
    ...e.versions,
    envLabel(e.env),
    ...Object.keys(e.kinds),
    ...workloadText,
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
}

export function Facet<T extends string>({ title, info, options, selected, onToggle }: { title: string; info?: React.ReactNode; options: { value: T; label: string; count: number; tone?: string; tooltip?: string }[]; selected: Set<T>; onToggle: (v: T) => void }) {
  const visible = options.filter((o) => o.count > 0)
  if (visible.length === 0) return null
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-1 px-1 text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
        {title}
        {info && (
          <Tooltip content={info} delay={150} position="right">
            <Info className="h-3 w-3 cursor-default text-theme-text-tertiary/70 hover:text-theme-text-secondary" aria-label={`About ${title}`} />
          </Tooltip>
        )}
      </div>
      {visible.map((o) => {
        const on = selected.has(o.value)
        const button = (
          <button
            key={o.value}
            type="button"
            onClick={() => onToggle(o.value)}
            className={`flex w-full items-center justify-between gap-2 rounded px-2 py-1 text-left text-xs ${on ? 'selection selection-ring text-theme-text-primary' : 'text-theme-text-secondary hover:bg-theme-hover'}`}
          >
            <span className={`truncate ${o.tone ?? ''}`}>{o.label}</span>
            <span className="font-mono tabular-nums text-theme-text-tertiary">{o.count}</span>
          </button>
        )
        return o.tooltip ? (
          <Tooltip key={o.value} content={o.tooltip} delay={300} position="right" wrapperClassName="w-full">
            {button}
          </Tooltip>
        ) : (
          button
        )
      })}
    </div>
  )
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
      return a.readyRatio - b.readyRatio
    case 'version': {
      // Sort by distinct-version count first (skewed apps cluster), then the
      // first tag for a stable, human-meaningful order.
      const byCount = a.versions.length - b.versions.length
      if (byCount !== 0) return byCount
      return (a.versions[0] ?? '').localeCompare(b.versions[0] ?? '')
    }
  }
}

function SortHeader({ label, sortKey, sort, onSort, className }: { label: string; sortKey: SortKey; sort: { key: SortKey; dir: SortDir } | null; onSort: (k: SortKey) => void; className?: string }) {
  const active = sort?.key === sortKey
  const ariaSort = active ? (sort!.dir === 'asc' ? 'ascending' : 'descending') : 'none'
  return (
    <th aria-sort={ariaSort} className={clsx('px-2 py-2 text-left text-[10px] font-medium uppercase tracking-wide cursor-pointer select-none text-theme-text-tertiary hover:text-theme-text-primary', className)} onClick={() => onSort(sortKey)}>
      <span className="inline-flex items-center gap-1">
        {label}
        {active ? (sort!.dir === 'asc' ? <ChevronUp className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />) : <span className="w-3" />}
      </span>
    </th>
  )
}

export interface ApplicationsListProps {
  apps: AppRow[]
  onSelect: (key: string) => void
}

export function ApplicationsList({ apps, onSelect }: ApplicationsListProps) {
  const [textFilter, setTextFilter] = useState('')
  const [fHealth, setFHealth] = useState<Set<AppHealth>>(new Set())
  const [fEnv, setFEnv] = useState<Set<string>>(new Set())
  const [fSource, setFSource] = useState<Set<AppSource>>(new Set())
  const [fClass, setFClass] = useState<Set<AppWorkloadClass>>(new Set())
  const [fType, setFType] = useState<Set<AppCategory>>(new Set())
  const [showSystem, setShowSystem] = useState(false)
  const [sort, setSort] = useState<{ key: SortKey; dir: SortDir } | null>(null)

  // Env tokens this CLUSTER proved (identity classifications on the wire) feed
  // the namespace heuristic, so sibling-less rows in discovered env namespaces
  // namespace still label without any hardcoded vocabulary.
  const discoveredEnvs = useMemo(() => new Set(apps.map((a) => a.identity?.env).filter((e): e is string => !!e)), [apps])
  const allRaw = useMemo<AppEntry[]>(() => apps.map((a) => buildEntry(a, discoveredEnvs)), [apps, discoveredEnvs])
  // System namespaces are filtered before facet counts so the counts reflect
  // what the user is actually looking at (consistent with the other facets).
  // An app counts as system only when EVERY workload namespace is system —
  // hiding a partly-user app would be worse than showing a partly-system one.
  const isSystemApp = (e: AppEntry) => e.namespaces.length > 0 && e.namespaces.every(isSystemNamespace)
  const all = useMemo(() => (showSystem ? allRaw : allRaw.filter((e) => !isSystemApp(e))), [allRaw, showSystem])
  const systemCount = useMemo(() => allRaw.filter(isSystemApp).length, [allRaw])

  const entries = useMemo(() => {
    const t = textFilter.trim().toLowerCase()
    const filtered = all.filter((e) => {
      if (t && !searchTextForEntry(e).includes(t)) return false
      if (fHealth.size && !fHealth.has(e.health)) return false
      // Inclusive: a mixed app matches the filter of ANY class it contains.
      if (fClass.size && !e.classSet.some((c) => fClass.has(c))) return false
      if (fType.size && !fType.has(e.category)) return false
      if (fSource.size && !fSource.has(e.source)) return false
      if (fEnv.size && !fEnv.has(e.env || 'none')) return false
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

  const visibleRows = useMemo<FoldedRow<AppEntry>[]>(
    () => foldAppGroups(entries, expandedGroups, FAMILY_AUTO_EXPAND_ON_SEARCH && textFilter.trim() !== ''),
    [entries, expandedGroups, textFilter],
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
      env[e.env || 'none'] = (env[e.env || 'none'] ?? 0) + 1
    }
    return { health, env, source, workloadClass, category }
  }, [all])

  const toggle = <T,>(set: Set<T>, setter: (s: Set<T>) => void, v: T) => {
    const next = new Set(set)
    next.has(v) ? next.delete(v) : next.add(v)
    setter(next)
  }

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

  return (
    <div className="flex w-full flex-1 flex-col gap-4">
      {/* Health spectrum hero */}
      <div className="flex flex-col gap-1.5 rounded-md border border-theme-border bg-theme-surface px-4 py-3">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
          <span className="font-medium text-theme-text-primary">
            {entries.length < total ? `${entries.length} of ${total} applications` : pluralize(total, 'application')}
          </span>
          {HEALTH_ORDER.map((h) => (counts.health[h] ? <span key={h} className={HEALTH_META[h].text}>{HEALTH_META[h].label} {counts.health[h]}</span> : null))}
          <span className="ml-auto text-theme-text-tertiary">
            {sort ? `Sorted by ${sort.key} ${sort.dir === 'asc' ? '↑' : '↓'}` : 'Sorted by status'}
          </span>
        </div>
        <div className="flex h-2 w-full overflow-hidden rounded-full bg-theme-hover">
          {HEALTH_ORDER.map((h) => (counts.health[h] ? <span key={h} className={HEALTH_META[h].bar} style={{ width: `${(counts.health[h] / total) * 100}%` }} title={`${HEALTH_META[h].label} ${counts.health[h]}`} /> : null))}
        </div>
      </div>

      <SearchBox
        value={textFilter}
        onChange={setTextFilter}
        scope="applications"
        shortcutId="applications-search"
        className="max-w-md"
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

      <div className="flex w-full gap-4">
        {/* Facet rail */}
        <aside className="hidden w-[200px] shrink-0 flex-col gap-4 lg:flex">
          <Facet title="Availability" options={HEALTH_ORDER.map((h) => ({ value: h, label: HEALTH_META[h].label, count: counts.health[h] ?? 0, tone: HEALTH_META[h].text }))} selected={fHealth} onToggle={(v) => toggle(fHealth, setFHealth, v)} />
          <Facet title="Class" options={CLASS_ORDER.map((c) => ({ value: c, label: CLASS_META[c].label, count: counts.workloadClass[c] ?? 0 }))} selected={fClass} onToggle={(v) => toggle(fClass, setFClass, v)} />
          <Facet title="Type" options={CATEGORY_ORDER.map((c) => ({ value: c, label: CATEGORY_META[c].label, count: counts.category[c] ?? 0, tooltip: CATEGORY_META[c].tooltip }))} selected={fType} onToggle={(v) => toggle(fType, setFType, v)} />
          <Facet title="Environment" info={<EnvHint />} options={envOptions} selected={fEnv} onToggle={(v) => toggle(fEnv, setFEnv, v)} />
          <Facet title="Source" options={SOURCE_ORDER.map((s) => ({ value: s, label: SOURCE_META[s].label, count: counts.source[s] ?? 0 }))} selected={fSource} onToggle={(v) => toggle(fSource, setFSource, v)} />
          {systemCount > 0 && (
            <label className="flex cursor-pointer items-center gap-2 rounded px-2 py-1 text-xs text-theme-text-secondary hover:bg-theme-hover">
              <input type="checkbox" checked={showSystem} onChange={(e) => setShowSystem(e.target.checked)} className="accent-skyhook-500" />
              <span>Show system namespaces</span>
              <span className="ml-auto font-mono tabular-nums text-theme-text-tertiary">{systemCount}</span>
            </label>
          )}
        </aside>

        {/* Table */}
        <div className="min-w-0 flex-1">
          {entries.length === 0 ? (
            <EmptyState tone="filtered" variant="card" headline="No applications match the filters" body="Clear the filters above." />
          ) : (
            <div className="overflow-hidden rounded-md border border-theme-border">
              <table className="w-full text-left text-sm">
                <thead>
                  <tr className="border-b border-theme-border bg-theme-base">
                    <SortHeader label="Application" sortKey="name" sort={sort} onSort={onSort} className="pl-3 pr-2" />
                    <th className="px-2 py-2 text-[10px] font-medium uppercase tracking-wide text-theme-text-tertiary">Namespace</th>
                    <th className="px-2 py-2 text-[10px] font-medium uppercase tracking-wide text-theme-text-tertiary">Env</th>
                    <th className="px-2 py-2 text-[10px] font-medium uppercase tracking-wide text-theme-text-tertiary">Class</th>
                    <SortHeader label="Ready" sortKey="ready" sort={sort} onSort={onSort} />
                    <SortHeader label="Version" sortKey="version" sort={sort} onSort={onSort} />
                    <th className="px-2 py-2 text-[10px] font-medium uppercase tracking-wide text-theme-text-tertiary">Workloads</th>
                    <th className="w-8" />
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
                          <ChevronRight className={clsx('h-3.5 w-3.5 shrink-0 text-theme-text-tertiary transition-transform', r.expanded && 'rotate-90')} aria-hidden />
                          <Tooltip content={HEALTH_META[r.health].label} delay={150}>
                            <span className="inline-flex"><StatusDot tone={mapHealthToTone(r.health)} /></span>
                          </Tooltip>
                          <span className="truncate font-semibold text-theme-text-primary">{r.label}</span>
                          <Tooltip
                            content={<AppIdentityTooltip identityKey={r.label} members={r.members.map((m) => ({ name: m.row.name, env: m.row.identity!.env, confidence: m.row.identity!.confidence, evidence: m.row.identity!.evidence }))} />}
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
                      <td className="px-2 py-2.5"><ReadyBar ready={r.ready} desired={r.desired} /></td>
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
                          <Tooltip content={HEALTH_META[e.health].label} delay={150}>
                            <span className="inline-flex"><StatusDot tone={mapHealthToTone(e.health)} /></span>
                          </Tooltip>
                          <span className="truncate font-medium text-theme-text-primary">{e.row.name}</span>
                          <ProvenanceBadge tier={e.row.tier} appKey={e.row.key} confidence={e.row.confidence} />
                          <CategoryChip category={e.category} addonReason={e.row.addonReason} />
                        </span>
                      </td>
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
                      <td className="px-2 py-2.5"><ClassBadge workloadClass={e.workloadClass} composition={e.classComposition} /></td>
                      <td className="px-2 py-2.5"><ReadyBar ready={e.ready} desired={e.desired} /></td>
                      <td className="px-2 py-2.5">
                        <VersionInfo app={e.row} variant="cell" />
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
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
