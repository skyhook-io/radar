import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import type { RefObject } from 'react'
import { clsx } from 'clsx'
import { MultiSelectPicker } from '../ui/MultiSelectPicker'
import { Tooltip } from '../ui/Tooltip'
import {
  Boxes,
  Pin,
  Trash2,
  Check,
  ChevronDown,
  RefreshCw,
  SlidersHorizontal,
  List,
  GanttChart,
} from 'lucide-react'
import { SearchBox } from '../ui/SearchBox'
import { type ShortcutScope } from '../../hooks/useKeyboardShortcuts'
import { useRefreshAnimation } from '../../hooks/useRefreshAnimation'
import { pluralize } from '../../utils/pluralize'
import type { TimelineEvent, TimeRange } from '../../types'
import type { TimelineGrouping } from '../../utils/resource-hierarchy'
import type { ActivityFilterKey, ActivitySource, ActivityStats } from './timeline-filters'
import { activityKeysToSelection, computeActivityStats, selectionToActivityKeys } from './timeline-filters'
import type { TimelineSort } from './timeline-lane-sort'

// Swimlane view options, surfaced together in the "View" menu so the toolbar
// stays a single row. The list view omits these — its View menu shows filters only.
export interface TimelineViewOptions {
  // Which lane ordering is active: 'importance' (default) | 'recent' | 'name'.
  sort: { value: TimelineSort; onChange: (value: TimelineSort) => void }
  // How lanes are grouped: 'app' (server app membership) | 'owner' (owner +
  // topology parenting only) | 'flat' (every resource its own lane).
  grouping: { value: TimelineGrouping; onChange: (value: TimelineGrouping) => void }
}

// The lane orderings, in the order they read in the menu (default first).
const SORT_OPTIONS: { value: TimelineSort; label: string; tooltip: string }[] = [
  { value: 'importance', label: 'Importance', tooltip: 'Rank lanes by interestingness — health problems, activity, and variety float to the top.' },
  { value: 'recent', label: 'Recent activity', tooltip: 'Order lanes by their most recent event in view — what just moved bubbles to the top.' },
  { value: 'name', label: 'Name (A→Z)', tooltip: 'Alphabetical by lane name (app groups by their title), case-insensitive.' },
]

// The 3-way grouping choices, in escalating-detail order (most grouped → flat).
const GROUPING_OPTIONS: { value: TimelineGrouping; label: string; tooltip: string }[] = [
  { value: 'app', label: 'Application', tooltip: 'Group lanes into the applications defined by the server (workload grouping + evidence).' },
  { value: 'owner', label: 'Workload', tooltip: 'Group by owning workload (Deployment→ReplicaSet→Pod, Service→Deployment).' },
  { value: 'flat', label: 'None', tooltip: 'No grouping — every resource is its own lane (K8s Events still attach to their owner).' },
]

export interface TimelineToolbarProps {
  // Search
  search: string
  onSearchChange: (value: string) => void
  searchScope?: ShortcutScope
  searchShortcutId: string
  searchClassName?: string

  // Activity-type cells. Multi-select with union semantics: an empty array means
  // "all". Counts derive from the full events array (or an explicit precomputed
  // stats object) so both views show identical totals.
  activityFilter: ActivityFilterKey[]
  onActivityFilterChange: (keys: ActivityFilterKey[]) => void
  events?: TimelineEvent[]
  stats?: ActivityStats

  // Deleted toggle — renders in the filter row (a scope filter past the
  // divider), not the View menu.
  showDeleted: boolean
  onShowDeletedChange: (showDeleted: boolean) => void
  // Pinned-only filter — only rendered when there are pinned rows at all.
  pinnedCount?: number
  pinnedOnly?: boolean
  onPinnedOnlyChange?: (pinnedOnly: boolean) => void

  // Kind filter — its own toolbar chip (between the activity control and View),
  // not a View-menu option: it changes which events are visible, so it's a filter,
  // not a view preference. Multi-select: an empty array means "all kinds".
  kindFilter: string[]
  onKindFilterChange: (kinds: string[]) => void
  kindOptions: string[]

  // Time-range dropdown — rendered only when the range props are supplied
  // (retained mode omits them, so the dropdown disappears).
  rangeOptions?: { value: TimeRange; label: string }[]
  timeRange?: TimeRange
  onTimeRangeChange?: (range: TimeRange) => void

  // Right-side counts. Render what's given: `resources` is swimlane-only.
  counts?: { events: number; resources?: number }
  countsFiltered?: boolean

  // View toggle
  view?: 'list' | 'swimlane'
  onViewChange?: (view: 'list' | 'swimlane') => void

  // Manual refresh button. Radar's own timeline omits it (the view is live);
  // hosts whose data is a one-shot load can wire it.
  onRefresh?: () => void

  // Swimlane-only view options (sort + grouping). Rendered inside the single
  // "View" menu as labeled Sort and Group sections.
  viewOptions?: TimelineViewOptions

  // Legend toggle (swimlane-only). When supplied, a "Legend" button renders in the
  // right-side controls; the host owns the shown/hidden state so the marker+health
  // key stays on-demand rather than permanently occupying a toolbar row.
  legend?: { shown: boolean; onToggle: () => void }
}

export function TimelineToolbar({
  search,
  onSearchChange,
  searchScope = 'timeline',
  searchShortcutId,
  // Compact static width (single-row layout): the row is shared with the
  // meta controls, so the search takes a fixed slot; focus and typing can
  // never move the controls to its right.
  searchClassName = 'w-44 shrink-0',
  activityFilter,
  onActivityFilterChange,
  events,
  stats: statsProp,
  showDeleted,
  onShowDeletedChange,
  pinnedCount = 0,
  pinnedOnly = false,
  onPinnedOnlyChange,
  kindFilter,
  onKindFilterChange,
  kindOptions,
  rangeOptions,
  timeRange,
  onTimeRangeChange,
  counts,
  countsFiltered,
  view,
  onViewChange,
  onRefresh,
  viewOptions,
  legend,
}: TimelineToolbarProps) {
  const stats = statsProp ?? computeActivityStats(events)
  const [handleRefresh, isRefreshAnimating] = useRefreshAnimation(onRefresh ?? (() => {}))
  const showRange = !!rangeOptions && timeRange !== undefined && !!onTimeRangeChange

  // Two-axis read of the carried keys: SOURCE (what stream) × PROBLEMS (only
  // the severity slice of it). Both writers emit canonical key sets, so the
  // URL/state stay in the shared ActivityFilterKey vocabulary.
  const activitySel = activityKeysToSelection(activityFilter)
  const setSource = (source: ActivitySource) =>
    onActivityFilterChange(selectionToActivityKeys({ ...activitySel, source }))
  const toggleProblems = () =>
    onActivityFilterChange(selectionToActivityKeys({ ...activitySel, problemsOnly: !activitySel.problemsOnly }))
  // The problems count follows the picked source — it previews exactly what
  // the toggle would show, not a fixed global number.
  const problemsCount =
    activitySel.source === 'changes' ? stats.unhealthy
    : activitySel.source === 'k8s_events' ? stats.warnings
    : stats.warnings + stats.unhealthy

  return (
    // ONE control row: search + filter chips left, table controls
    // right. The former two-row stack was 90% air on its second row. The left
    // group scrolls within itself when the container is narrower than its
    // intrinsic width (e.g. the ~800px resource-drawer embed).
    // @container = inline-size CONTAINMENT (not just a query context): without
    // it the row's intrinsic min-content width propagates up and widens the
    // whole timeline pane past the viewport instead of the left group shrinking.
    <div className="@container/toolbar border-b border-theme-border bg-theme-surface/50">
      <div className="flex items-center gap-3 px-4 py-2">
        {/* -m-1 p-1: the overflow-x-auto clip box would otherwise shave the
            search input's 2px focus ring on the left/top edge; the padding gives
            the ring room and the negative margin cancels the layout shift. The
            Kinds popover is portaled so this scroll container can't clip it. */}
        <div className="-m-1 flex min-w-0 items-center gap-2 overflow-x-auto p-1">
          {/* Search — always open, compact static width, FIRST. The `/` shortcut
              still focuses it (SearchBox owns the shortcut) and the clear × stays. */}
          <SearchBox
            value={search}
            onChange={onSearchChange}
            scope={searchScope}
            shortcutId={searchShortcutId}
            className={searchClassName}
            placeholder="Search..."
          />

          {/* Activity filter — two orthogonal axes: a single-select SOURCE pick
              (which stream) plus a PROBLEMS toggle (only the severity slice of
              the picked source). Zero-count chips dim in place rather than
              disappearing — position memory without visual weight. */}
          <div role="radiogroup" aria-label="Activity source" className="flex shrink-0 items-center gap-1.5">
            <SourceChip
              active={activitySel.source === 'all'}
              onClick={() => setSource('all')}
              label="All"
              count={stats.total}
              tooltip="Everything — resource changes and K8s Events"
            />
            <SourceChip
              active={activitySel.source === 'changes'}
              onClick={() => setSource('changes')}
              label="Changes"
              count={stats.changes}
              tooltip="Spec & status changes to the resource itself — creates, updates, deletes"
            />
            <SourceChip
              active={activitySel.source === 'k8s_events'}
              onClick={() => setSource('k8s_events')}
              label="K8s Events"
              count={stats.k8sEvents}
              tooltip="Native Kubernetes Event objects — what the cluster reported: scheduling, image pulls, restarts (Normal + Warning)"
            />
          </div>
          <ProblemsToggle
            active={activitySel.problemsOnly}
            count={problemsCount}
            source={activitySel.source}
            onClick={toggleProblems}
          />

          {/* Kinds filter — dashed "add a filter" chip beside the type chips. */}
          <KindsMenu
            kindFilter={kindFilter}
            onKindFilterChange={onKindFilterChange}
            kindOptions={kindOptions}
          />

          {/* Deleted is SCOPE, not a type: it filters which resources
              exist, not which events show — so it sits past a divider, out of
              the type-chip group. */}
          <span className="mx-0.5 h-5 w-px shrink-0 bg-theme-border" aria-hidden />
          <DeletedEventsToggle showDeleted={showDeleted} onChange={onShowDeletedChange} />

          {/* Pinned-only — appears only once something is pinned; hidden chrome
              otherwise. Covers pinned rows of both kinds (resources and apps). */}
          {pinnedCount > 0 && onPinnedOnlyChange && (
            <PinnedOnlyToggle pinnedOnly={pinnedOnly} pinnedCount={pinnedCount} onChange={onPinnedOnlyChange} />
          )}

          {/* Time range — omitted when an external control owns the range */}
          {showRange && (
            <select
              value={timeRange}
              onChange={(e) => onTimeRangeChange!(e.target.value as TimeRange)}
              className="shrink-0 appearance-none bg-theme-elevated text-theme-text-primary text-sm rounded-lg px-3 py-2 border border-theme-border-light focus:outline-none focus:ring-2 focus:ring-skyhook-500"
            >
              {rangeOptions!.map((range) => (
                <option key={range.value} value={range.value}>
                  {range.label}
                </option>
              ))}
            </select>
          )}
        </div>

        {/* META group — right-aligned: Showing · View (sort + group) · Legend ·
            view toggle. Its position is width-driven, never interaction. */}
        <div className="ml-auto flex min-w-0 shrink-0 items-center gap-2">
          {counts && (
            <span className="min-w-0 truncate text-xs text-theme-text-tertiary">
              {/* "Showing": the visible-slice count, worded apart from the
                  strip's loaded total so the two numbers don't read as one. */}
              {counts.resources !== undefined && `Showing ${pluralize(counts.resources, 'resource')} · `}
              {counts.resources === undefined && 'Showing '}
              {pluralize(counts.events, 'event')}
              {countsFiltered && ' (filtered)'}
            </span>
          )}

          {/* View menu — Sort + Group as labeled sections behind one trigger:
              the single control row has no room for two dropdowns that
              each restate their value. */}
          {viewOptions && <ViewMenu viewOptions={viewOptions} />}

          {onRefresh && (
            <Tooltip content="Refresh" position="bottom" wrapperClassName="shrink-0">
              <button
                type="button"
                onClick={handleRefresh}
                disabled={isRefreshAnimating}
                aria-label="Refresh"
                className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg disabled:opacity-50"
              >
                <RefreshCw className={clsx('w-4 h-4', isRefreshAnimating && 'animate-spin')} />
              </button>
            </Tooltip>
          )}

          {/* Legend toggle — the marker/health key is on-demand, not a
              permanent toolbar row. Host owns the shown state. */}
          {legend && (
            <button
              type="button"
              onClick={legend.onToggle}
              aria-pressed={legend.shown}
              className={clsx(
                'shrink-0 rounded-lg border px-2.5 py-1.5 text-sm font-medium transition-colors',
                legend.shown
                  ? 'border-theme-border bg-theme-hover text-theme-text-primary'
                  : 'border-theme-border-light text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary',
              )}
            >
              Legend
            </button>
          )}

          {/* View toggle — labeled, and LAST so it hugs the toolbar's right
              edge. The rightmost slot is the only position that stays fixed
              across the two views while the variable-width counts and menus
              beside it change; an anchored control can be re-clicked without
              re-aiming. */}
          {onViewChange && (
            <div className="flex shrink-0 items-center gap-1 bg-theme-elevated rounded-lg p-1">
              <button
                type="button"
                onClick={() => onViewChange('list')}
                className={clsx(
                  'flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-xs font-medium transition-colors',
                  view === 'list' ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary'
                )}
              >
                <List className="w-4 h-4" />
                List
              </button>
              <button
                type="button"
                onClick={() => onViewChange('swimlane')}
                className={clsx(
                  'flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-xs font-medium transition-colors',
                  view === 'swimlane' ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary'
                )}
              >
                <GanttChart className="w-4 h-4" />
                Timeline
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

// One pill of the single-select SOURCE pick. A zero-count pill dims in place
// and goes inert: position memory without visual weight — chips that
// vanish make the row reflow and the user hunt.
function SourceChip({ active, onClick, label, count, tooltip }: {
  active: boolean
  onClick: () => void
  label: string
  count?: number
  tooltip?: string
}) {
  const dead = !active && count === 0
  return (
    <Tooltip content={tooltip} disabled={!tooltip || dead} position="bottom" wrapperClassName="shrink-0">
      <button
        type="button"
        role="radio"
        aria-checked={active}
        onClick={dead ? undefined : onClick}
        disabled={dead}
        className={clsx(
          'flex items-center gap-1.5 whitespace-nowrap rounded-full border px-2.5 py-1.5 text-sm transition-colors',
          active
            ? 'border-theme-border-light bg-theme-hover font-semibold text-theme-text-primary'
            : 'border-theme-border text-theme-text-secondary',
          dead ? 'cursor-default opacity-45' : !active && 'hover:bg-theme-hover hover:text-theme-text-primary',
        )}
      >
        <span>{label}</span>
        {count !== undefined && (
          <span className="tabular-nums text-xs text-theme-text-tertiary">{count.toLocaleString()}</span>
        )}
      </button>
    </Tooltip>
  )
}

// The severity slice of the picked source: Warning events, unhealthy/degraded
// changes, or both under "All". Engaged state is amber — the timeline's
// problem hue — and the count previews what the toggle would show.
function ProblemsToggle({ active, count, source, onClick }: {
  active: boolean
  count: number
  source: ActivitySource
  onClick: () => void
}) {
  const dead = !active && count === 0
  const tooltip =
    source === 'changes' ? 'Only changes that left the resource unhealthy or degraded.'
    : source === 'k8s_events' ? 'Only Warning-type events (e.g. ImagePullBackOff, FailedScheduling).'
    : 'Only problem activity: Warning events and changes that left a resource unhealthy or degraded. Combines with the source pick.'
  return (
    <Tooltip content={tooltip} position="bottom" disabled={dead} wrapperClassName="shrink-0">
      <button
        type="button"
        aria-pressed={active}
        onClick={dead ? undefined : onClick}
        disabled={dead}
        className={clsx(
          'flex items-center gap-1.5 whitespace-nowrap rounded-full border px-2.5 py-1.5 text-sm transition-colors',
          active
            ? 'border-amber-400/60 bg-amber-100 font-semibold text-amber-800 dark:border-amber-500/40 dark:bg-amber-950/50 dark:text-amber-300'
            : 'border-theme-border text-theme-text-secondary',
          dead ? 'cursor-default opacity-45' : !active && 'hover:bg-theme-hover hover:text-theme-text-primary',
        )}
      >
        <span className="inline-block h-1.5 w-1.5 rounded-full bg-amber-500" aria-hidden />
        <span>Problems</span>
        <span className={clsx('tabular-nums text-xs', active ? 'text-amber-700/80 dark:text-amber-400/80' : 'text-theme-text-tertiary')}>
          {count.toLocaleString()}
        </span>
      </button>
    </Tooltip>
  )
}


interface KindsMenuProps {
  kindFilter: string[]
  onKindFilterChange: (kinds: string[]) => void
  kindOptions: string[]
}

// The shared markup behind both the Sort and Group controls: a radiogroup of
// vertical check rows (identical structure; differ only in options/value).
function SegmentedRadioGroup<T extends string>({
  label,
  options,
  value,
  onChange,
}: {
  label: string
  options: { value: T; label: string; tooltip: string }[]
  value: T
  onChange: (value: T) => void
}) {
  // Product menu idiom (see TopologyControls): vertical check rows, not inline
  // segments — long labels ("Recent activity") stay one line instead of wrapping
  // into uneven boxes.
  return (
    <div role="radiogroup" aria-label={label} className="-mx-1 flex flex-col">
      {options.map((opt) => {
        const active = value === opt.value
        return (
          <Tooltip key={opt.value} content={opt.tooltip} position="left" wrapperClassName="w-full">
            <button
              type="button"
              role="radio"
              aria-checked={active}
              onClick={() => onChange(opt.value)}
              className={clsx(
                'flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm transition-colors',
                'hover:bg-theme-hover focus:bg-theme-hover focus:outline-none',
                active ? 'text-theme-text-primary font-medium' : 'text-theme-text-secondary',
              )}
            >
              <Check className={clsx('h-3.5 w-3.5 shrink-0 text-skyhook-500', active ? 'opacity-100' : 'opacity-0')} />
              <span className="truncate">{opt.label}</span>
            </button>
          </Tooltip>
        )
      })}
    </div>
  )
}

/**
 * The "View" button + popover holding the Sort and Group sections (one
 * trigger — the single control row has no room for two dropdowns that each
 * restate their value; the sections inside are clearly labeled).
 */
export function ViewMenu({ viewOptions }: { viewOptions: TimelineViewOptions }) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement | null>(null)
  usePopoverDismiss(open, setOpen, rootRef)

  return (
    <div ref={rootRef} className="relative shrink-0">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className={clsx(
          'flex items-center gap-1.5 rounded-lg border border-theme-border-light px-2.5 py-1.5 text-sm font-medium transition-colors',
          open
            ? 'bg-theme-elevated text-theme-text-primary'
            : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary',
        )}
      >
        <SlidersHorizontal className="h-4 w-4" />
        <span>View</span>
        <ChevronDown className="w-3.5 h-3.5 text-theme-text-tertiary" />
      </button>

      {open && (
        <div
          role="menu"
          aria-label="View options"
          className="absolute right-0 top-full z-50 mt-1 min-w-[14rem] rounded-lg border border-theme-border bg-theme-elevated p-2 shadow-theme-lg"
        >
          <span className="block px-2 pb-1 text-[10px] font-bold uppercase tracking-[0.08em] text-theme-text-tertiary">Sort</span>
          <SegmentedRadioGroup
            label="Lane sort"
            options={SORT_OPTIONS}
            value={viewOptions.sort.value}
            onChange={(v) => { viewOptions.sort.onChange(v); setOpen(false) }}
          />
          <span aria-hidden className="my-2 block h-px bg-theme-border" />
          <span className="block px-2 pb-1 text-[10px] font-bold uppercase tracking-[0.08em] text-theme-text-tertiary">Group by</span>
          <SegmentedRadioGroup
            label="Lane grouping"
            options={GROUPING_OPTIONS}
            value={viewOptions.grouping.value}
            onChange={(v) => { viewOptions.grouping.onChange(v); setOpen(false) }}
          />
        </div>
      )}
    </div>
  )
}

// Pinned-only filter chip — same quiet/active pattern as the deleted toggle:
// engaged (non-default) state gets the accent treatment.
export function PinnedOnlyToggle({
  pinnedOnly,
  pinnedCount,
  onChange,
}: {
  pinnedOnly: boolean
  pinnedCount: number
  onChange: (pinnedOnly: boolean) => void
}) {
  return (
    <Tooltip
      content={pinnedOnly
        ? 'Showing pinned rows only. Click to show all rows again.'
        : `Show only the ${pinnedCount} pinned row${pinnedCount === 1 ? '' : 's'} (resources and apps).`}
      position="bottom"
    >
      <button
        type="button"
        aria-pressed={pinnedOnly}
        aria-label={pinnedOnly ? 'Show all rows' : 'Show pinned rows only'}
        onClick={() => onChange(!pinnedOnly)}
        className={clsx(
          'flex h-9 items-center gap-1.5 rounded-lg border px-2.5 text-sm transition-colors',
          pinnedOnly
            ? 'border-accent bg-accent-muted text-accent-text'
            : 'border-theme-border bg-theme-surface text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary',
        )}
      >
        <Pin className="h-4 w-4" />
        {pinnedOnly && <span className="text-xs font-medium">only</span>}
      </button>
    </Tooltip>
  )
}

// Quiet icon toggle for delete-event visibility. Lives with the other filters
// (activity segments, Kinds) — it filters WHICH events are visible, so it does
// not belong in the View menu. Default (shown) renders quiet; the NON-default
// hidden state gets the active treatment, like any engaged filter.
export function DeletedEventsToggle({
  showDeleted,
  onChange,
}: {
  showDeleted: boolean
  onChange: (show: boolean) => void
}) {
  return (
    <Tooltip
      content={showDeleted
        ? 'Delete events are shown. Click to hide them — resources whose only events are deletions will disappear from the list.'
        : 'Delete events are hidden (resources with only deletions are not listed). Click to show them.'}
      position="bottom"
    >
      <button
        type="button"
        aria-pressed={!showDeleted}
        aria-label={showDeleted ? 'Hide delete events' : 'Show delete events'}
        onClick={() => onChange(!showDeleted)}
        // Dashed pill like Kinds: Deleted is SCOPE (which resources exist),
        // not an event type — the dashed family marks the scope controls.
        className={clsx(
          'flex items-center gap-1.5 whitespace-nowrap rounded-full border px-2.5 py-1.5 text-sm transition-colors',
          showDeleted
            ? 'border-dashed border-theme-border-light text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
            : 'border-accent bg-accent-muted text-accent-text',
        )}
      >
        <Trash2 className="h-4 w-4" />
        <span>{showDeleted ? 'Deleted' : 'Deleted hidden'}</span>
      </button>
    </Tooltip>
  )
}

// Shared popover dismissal for the toolbar's menus (View, Kinds): outside
// pointerdown and Escape both close. Extracted so both behave identically.
function usePopoverDismiss(
  open: boolean,
  setOpen: (open: boolean) => void,
  rootRef: RefObject<HTMLDivElement | null>,
) {
  useEffect(() => {
    if (!open) return
    const onDown = (e: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { e.stopPropagation(); setOpen(false) }
    }
    window.addEventListener('pointerdown', onDown)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointerdown', onDown)
      window.removeEventListener('keydown', onKey)
    }
  }, [open, setOpen, rootRef])
}

/**
 * The "Kinds" filter chip + popover. Same visual family and dismissal behaviour as
 * the View menu, but it's a filter (changes which events are visible), so it owns
 * its own badge = number of selected kinds, hidden when none. The popover body is
 * the shared MultiSelectPicker — same search + Clear all / Select all + checkbox
 * list + summary/Done shell as the namespace scope picker. Empty selection = no
 * filter ("All kinds").
 */
function KindsMenu({ kindFilter, onKindFilterChange, kindOptions }: KindsMenuProps) {
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState('')
  const rootRef = useRef<HTMLDivElement | null>(null)
  const panelRef = useRef<HTMLDivElement | null>(null)
  const [anchor, setAnchor] = useState<{ left: number; top: number; maxHeight: number } | null>(null)
  const activeCount = kindFilter.length
  const selected = useMemo(() => new Set(kindFilter), [kindFilter])

  // The chip lives inside the filters row's horizontal scroll container, which
  // clips absolutely-positioned children. Portal the popover to the body and pin
  // it to the button with fixed coords so it escapes the clip. Dismiss must treat
  // BOTH the chip and the portaled panel as "inside".
  const PANEL_WIDTH = 256
  useLayoutEffect(() => {
    if (!open) { setAnchor(null); return }
    const place = () => {
      const r = rootRef.current?.getBoundingClientRect()
      if (!r) return
      const left = Math.max(8, Math.min(r.left, window.innerWidth - PANEL_WIDTH - 8))
      const spaceBelow = window.innerHeight - r.bottom - 8
      const spaceAbove = r.top - 8
      // scrollHeight = natural (uncapped) height, so measuring stays stable even
      // after we've capped the rendered panel on a previous pass.
      const panelH = panelRef.current?.scrollHeight ?? 0
      // Near the viewport bottom the popover would open off-screen. Flip it above
      // the chip when it doesn't fit below but has more room above; cap the height
      // to the chosen gap so the list scrolls instead of clipping, and never let
      // the flipped-up panel escape the top edge.
      const flipUp = panelH > spaceBelow && spaceAbove > spaceBelow
      const maxHeight = Math.max(120, flipUp ? spaceAbove : spaceBelow)
      const top = flipUp
        ? Math.max(4, r.top - 4 - Math.min(panelH, maxHeight))
        : r.bottom + 4
      setAnchor({ left, top, maxHeight })
    }
    place()
    // Re-place after the panel mounts so the first open can measure its height and
    // flip; the initial pass runs with panelH=0 (panel not yet in the DOM).
    const raf = requestAnimationFrame(place)
    window.addEventListener('scroll', place, true)
    window.addEventListener('resize', place)
    return () => {
      cancelAnimationFrame(raf)
      window.removeEventListener('scroll', place, true)
      window.removeEventListener('resize', place)
    }
  }, [open])

  useEffect(() => {
    if (!open) return
    const onDown = (e: PointerEvent) => {
      const t = e.target as Node
      if (rootRef.current?.contains(t) || panelRef.current?.contains(t)) return
      setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { e.stopPropagation(); setOpen(false) }
    }
    window.addEventListener('pointerdown', onDown)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointerdown', onDown)
      window.removeEventListener('keydown', onKey)
    }
  }, [open])

  // Reset the filter whenever the popover closes by any path (Done, chip toggle,
  // outside-click, Escape) so a reopen starts fresh — mirrors the namespace picker.
  useEffect(() => {
    if (!open) setSearch('')
  }, [open])

  return (
    <div ref={rootRef} className="relative shrink-0">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        // Dashed pill: reads as "add a filter", distinct from the solid
        // always-on activity pills beside it.
        className={clsx(
          'flex items-center gap-1.5 rounded-full border border-dashed px-2.5 py-1.5 text-sm transition-colors',
          open || activeCount > 0
            ? 'border-theme-border bg-theme-hover text-theme-text-primary'
            : 'border-theme-border-light text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary',
        )}
      >
        <Boxes className="w-4 h-4" />
        <span>Kinds</span>
        {activeCount > 0 && (
          <span className="inline-flex min-w-[1.25rem] justify-center rounded-full bg-accent px-1.5 text-xs font-medium text-white tabular-nums">
            {activeCount}
          </span>
        )}
        <ChevronDown className="w-3.5 h-3.5 text-theme-text-tertiary" />
      </button>

      {open && anchor && createPortal(
        <div
          ref={panelRef}
          style={{ position: 'fixed', left: anchor.left, top: anchor.top, width: PANEL_WIDTH, maxHeight: anchor.maxHeight }}
          className="z-50 overflow-y-auto rounded-md border border-theme-border bg-theme-surface shadow-theme-lg"
        >
          <MultiSelectPicker
            items={kindOptions}
            selected={selected}
            onSelectionChange={(next) => onKindFilterChange([...next])}
            onClearAll={() => onKindFilterChange([])}
            onDone={() => setOpen(false)}
            search={search}
            onSearchChange={setSearch}
            searchPlaceholder="Filter kinds"
            summaryEmptyLabel="All kinds"
            noItemsLabel="No kinds available."
            clearAllDisabled={activeCount === 0}
            clearAllAriaLabel="Clear kind selection"
          />
        </div>,
        document.body,
      )}
    </div>
  )
}
