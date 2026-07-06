import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import type { ReactNode, RefObject } from 'react'
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
import { StatusDot, type StatusTone } from '../ui/status-tone'
import { type ShortcutScope } from '../../hooks/useKeyboardShortcuts'
import { useRefreshAnimation } from '../../hooks/useRefreshAnimation'
import { pluralize } from '../../utils/pluralize'
import type { TimelineEvent, TimeRange } from '../../types'
import type { TimelineGrouping } from '../../utils/resource-hierarchy'
import type { ActivityFilterKey, ActivityStats } from './timeline-filters'
import { computeActivityStats, countActiveViewOptions } from './timeline-filters'
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
  { value: 'app', label: 'Applications', tooltip: 'Group lanes into the applications defined by the server (workload grouping + evidence).' },
  { value: 'owner', label: 'Owners', tooltip: 'Group only by owner references and topology (Deployment→ReplicaSet→Pod, Service→Deployment).' },
  { value: 'flat', label: 'Flat', tooltip: 'No grouping — every resource is its own lane (K8s events still attach to their owner).' },
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

  // Deleted toggle — lives inside the View menu's Filters section.
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

  // Refresh
  onRefresh?: () => void

  // Swimlane-only view options (Sort, Group by app). When supplied, the View menu
  // gains Sort and Group sections above the always-present Filters section.
  viewOptions?: TimelineViewOptions
}

export function TimelineToolbar({
  search,
  onSearchChange,
  searchScope = 'timeline',
  searchShortcutId,
  // Static width, never grows/shrinks: the search is the first control in the
  // filters row and must not move any control when it gains focus or text. A
  // flexible width let typing shift (or wrap) everything to its right.
  searchClassName = 'w-56 shrink-0',
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
}: TimelineToolbarProps) {
  const stats = statsProp ?? computeActivityStats(events)
  const [handleRefresh, isRefreshAnimating] = useRefreshAnimation(onRefresh ?? (() => {}))
  const showRange = !!rangeOptions && timeRange !== undefined && !!onTimeRangeChange

  const toggleActivityKey = (key: ActivityFilterKey) => {
    onActivityFilterChange(
      activityFilter.includes(key)
        ? activityFilter.filter((k) => k !== key)
        : [...activityFilter, key],
    )
  }

  return (
    // Container query, not a viewport media query: this same toolbar renders
    // inside the ~800px resource-drawer embed within a wide viewport, so it must
    // react to its OWN width. `@container/toolbar` establishes the query context;
    // the layout below flips single-row → stacked purely on available width — no
    // JS, no resize listener, and nothing that interaction (focus/typing) can move.
    <div className="@container/toolbar border-b border-theme-border bg-theme-surface/50">
      {/* Threshold = the single-row natural width (measured ~1362px: filters ~944
          + meta ~374 + gap + padding) plus ~28px slack. Below it, the row can't
          hold both groups without truncating, so we stack rather than cramp. */}
      <div className="flex flex-col gap-3 px-4 py-3 @[1390px]/toolbar:flex-row @[1390px]/toolbar:items-center">
        {/* FILTERS group: search first, then activity segments + Kinds + deleted
            toggle + pinned-only. min-w-0 lets the group shrink without pushing the
            meta group off-screen; controls keep their intrinsic size. overflow-x-auto
            is the safety net below the group's intrinsic width (e.g. an ~800px drawer
            embed): the fixed controls scroll horizontally WITHIN the row instead of
            clipping or forcing a page-level scrollbar. The Kinds popover is portaled
            so this scroll container can't clip it. */}
        <div className="flex min-w-0 items-center gap-3 overflow-x-auto">
          {/* Search — always open, static width, FIRST. The `/` shortcut still
              focuses it (SearchBox owns the shortcut) and the clear × stays; there
              is deliberately no collapse/expand so it can never shift its neighbors. */}
          <SearchBox
            value={search}
            onChange={onSearchChange}
            scope={searchScope}
            shortcutId={searchShortcutId}
            className={searchClassName}
          />

          {/* Activity filter — one joined segmented control. Short labels; full names
              live in the tooltips. Multi-select: "All" clears; each other cell toggles
              its key, and several can be active at once. Severity dots replace icons. */}
          <div className="flex shrink-0 items-stretch divide-x divide-theme-border overflow-hidden rounded-lg border border-theme-border bg-theme-surface">
            <SegmentCell
              active={activityFilter.length === 0}
              onClick={() => onActivityFilterChange([])}
              label="All"
              count={stats.total}
              tooltip="Show all activity: resource changes and K8s events"
            />
            <SegmentCell
              active={activityFilter.includes('changes')}
              onClick={() => toggleActivityKey('changes')}
              label="Changes"
              count={stats.changes}
              tooltip="Resource mutations: creates, updates, deletes detected by watching K8s API"
            />
            <SegmentCell
              active={activityFilter.includes('warnings')}
              onClick={() => toggleActivityKey('warnings')}
              label="Warnings"
              count={stats.warnings}
              dotTone="degraded"
              tooltip="Native Kubernetes Warning Events (e.g., ImagePullBackOff, FailedScheduling)"
            />
            <SegmentCell
              active={activityFilter.includes('unhealthy')}
              onClick={() => toggleActivityKey('unhealthy')}
              label="Unhealthy"
              count={stats.unhealthy}
              dotTone="unhealthy"
              tooltip="Resource changes with unhealthy or degraded health state"
            />
            <SegmentCell
              active={activityFilter.includes('k8s_events')}
              onClick={() => toggleActivityKey('k8s_events')}
              label="K8s Events"
              tooltip="All native Kubernetes Events (Normal + Warning types)"
            />
          </div>

          {/* Kinds filter — its own chip in the View button's visual family, sitting
              between the activity control and View. Changes which events are visible,
              so it's a filter, not a view preference. */}
          <KindsMenu
            kindFilter={kindFilter}
            onKindFilterChange={onKindFilterChange}
            kindOptions={kindOptions}
          />

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
              className="shrink-0 appearance-none bg-theme-elevated text-theme-text-primary text-sm rounded-lg px-3 py-2 border border-theme-border-light focus:outline-none focus:ring-2 focus:ring-blue-500"
            >
              {rangeOptions!.map((range) => (
                <option key={range.value} value={range.value}>
                  {range.label}
                </option>
              ))}
            </select>
          )}
        </div>

        {/* META group: counts, view toggle, View menu, refresh. When stacked it
            sits on its own row, right-aligned (self-end); single-row it floats to
            the right (ml-auto). Its position is width-driven, never interaction. */}
        <div className="flex min-w-0 items-center gap-3 self-end @[1390px]/toolbar:ml-auto @[1390px]/toolbar:self-auto">
          {counts && (
            <span className="min-w-0 truncate text-xs text-theme-text-tertiary">
              {counts.resources !== undefined && `${pluralize(counts.resources, 'resource')} · `}
              {pluralize(counts.events, 'event')}
              {countsFiltered && ' (filtered)'}
            </span>
          )}

          {/* View toggle */}
          {onViewChange && (
            <div className="flex shrink-0 items-center gap-1 bg-theme-elevated rounded-lg p-1">
              <button
                type="button"
                onClick={() => onViewChange('list')}
                className={clsx(
                  'p-2 rounded-md transition-colors',
                  view === 'list' ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary'
                )}
                title="List view"
              >
                <List className="w-4 h-4" />
              </button>
              <button
                type="button"
                onClick={() => onViewChange('swimlane')}
                className={clsx(
                  'p-2 rounded-md transition-colors',
                  view === 'swimlane' ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary'
                )}
                title="Swimlane view"
              >
                <GanttChart className="w-4 h-4" />
              </button>
            </div>
          )}

          {/* View menu — Sort/Group only. Filters (Kinds, Deletions) live in the
              toolbar with the other filters. */}
          <ViewMenu
            viewOptions={viewOptions}
          />

          {/* Refresh */}
          {onRefresh && (
            <button
              type="button"
              onClick={handleRefresh}
              disabled={isRefreshAnimating}
              className="shrink-0 p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg disabled:opacity-50"
              title="Refresh"
            >
              <RefreshCw className={clsx('w-4 h-4', isRefreshAnimating && 'animate-spin')} />
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

interface SegmentCellProps {
  active: boolean
  onClick: () => void
  label: string
  count?: number
  dotTone?: StatusTone
  tooltip?: string
}

function SegmentCell({ active, onClick, label, count, dotTone, tooltip }: SegmentCellProps) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      title={tooltip}
      className={clsx(
        'flex items-center gap-1.5 px-3 py-1.5 text-sm transition-colors',
        active
          ? 'bg-accent-muted text-accent-text font-medium'
          : 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary',
      )}
    >
      {dotTone && <StatusDot tone={dotTone} />}
      <span>{label}</span>
      {count !== undefined && (
        <span className="tabular-nums text-xs text-theme-text-tertiary">{count}</span>
      )}
    </button>
  )
}


function SectionLabel({ children }: { children: ReactNode }) {
  return (
    <span className="px-1 text-[10px] font-bold uppercase tracking-[0.09em] text-theme-text-tertiary">
      {children}
    </span>
  )
}

interface KindsMenuProps {
  kindFilter: string[]
  onKindFilterChange: (kinds: string[]) => void
  kindOptions: string[]
}

// A joined segmented radiogroup — the shared markup behind both the Sort and
// Group controls (identical structure; differ only in options/value).
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
          <button
            key={opt.value}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onChange(opt.value)}
            title={opt.tooltip}
            className={clsx(
              'flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm transition-colors',
              'hover:bg-theme-hover focus:bg-theme-hover focus:outline-none',
              active ? 'text-theme-text-primary font-medium' : 'text-theme-text-secondary',
            )}
          >
            <Check className={clsx('h-3.5 w-3.5 shrink-0 text-skyhook-500', active ? 'opacity-100' : 'opacity-0')} />
            <span className="truncate">{opt.label}</span>
          </button>
        )
      })}
    </div>
  )
}

interface ViewMenuContentProps {
  viewOptions?: TimelineViewOptions
}

/**
 * The View menu's content: the Sort and Group sections (swimlane only; list view
 * passes neither, so the menu isn't rendered at all). Pure so it's SSR-testable.
 * Kinds and Show-deleted are their own toolbar chips, not part of this panel.
 */
export function ViewOptionsPanel({ viewOptions }: ViewMenuContentProps) {
  const hasSort = !!viewOptions?.sort
  const hasGroup = !!viewOptions?.grouping

  return (
    <div className="flex flex-col gap-3 p-1">
      {hasSort && (
        <div className="flex flex-col gap-1">
          <SectionLabel>Sort</SectionLabel>
          <SegmentedRadioGroup
            label="Lane sort"
            options={SORT_OPTIONS}
            value={viewOptions!.sort.value}
            onChange={viewOptions!.sort.onChange}
          />
        </div>
      )}

      {hasGroup && (
        <div className={clsx('flex flex-col gap-1', hasSort && 'border-t border-theme-border pt-2')}>
          <SectionLabel>Group</SectionLabel>
          <SegmentedRadioGroup
            label="Lane grouping"
            options={GROUPING_OPTIONS}
            value={viewOptions!.grouping.value}
            onChange={viewOptions!.grouping.onChange}
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
        className={clsx(
          'flex h-9 items-center gap-1.5 rounded-lg border px-2.5 text-sm transition-colors',
          showDeleted
            ? 'border-theme-border bg-theme-surface text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
            : 'border-accent bg-accent-muted text-accent-text',
        )}
      >
        <Trash2 className="h-4 w-4" />
        {!showDeleted && <span className="text-xs font-medium">hidden</span>}
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
 * The "View" button + popover holding the Sort and Group sections. Reuses the
 * outside-click + Escape pattern; the button carries a badge counting non-default
 * view choices. Renders nothing when the host wires neither Sort nor Group (list
 * view), so the button never opens an empty popover. Kinds and Show-deleted are
 * separate toolbar chips.
 */
function ViewMenu(props: ViewMenuContentProps) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement | null>(null)
  // List view passes no sort/grouping, so the panel would be empty — don't render
  // a View button that opens a blank popover.
  const hasContent = !!props.viewOptions?.sort || !!props.viewOptions?.grouping
  const activeCount = countActiveViewOptions({
    grouping: props.viewOptions?.grouping.value,
    sort: props.viewOptions?.sort.value,
  })

  usePopoverDismiss(open, setOpen, rootRef)

  if (!hasContent) return null

  return (
    <div ref={rootRef} className="relative">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        title="View options"
        className={clsx(
          'flex items-center gap-1.5 rounded-lg border border-theme-border-light px-3 py-1.5 text-sm transition-colors',
          open
            ? 'bg-theme-elevated text-theme-text-primary'
            : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary',
        )}
      >
        <SlidersHorizontal className="w-4 h-4" />
        <span>View</span>
        {activeCount > 0 && (
          <span className="inline-flex min-w-[1.25rem] justify-center rounded-full bg-accent px-1.5 text-xs font-medium text-white tabular-nums">
            {activeCount}
          </span>
        )}
        <ChevronDown className="w-3.5 h-3.5 text-theme-text-tertiary" />
      </button>

      {open && (
        <div
          role="menu"
          aria-label="View options"
          className="absolute right-0 top-full z-50 mt-1 min-w-[15rem] rounded-lg border border-theme-border bg-theme-elevated p-1 shadow-theme-lg"
        >
          <ViewOptionsPanel {...props} />
        </div>
      )}
    </div>
  )
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
        title="Filter by kind"
        className={clsx(
          'flex items-center gap-1.5 rounded-lg border border-theme-border-light px-3 py-1.5 text-sm transition-colors',
          open
            ? 'bg-theme-elevated text-theme-text-primary'
            : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary',
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
