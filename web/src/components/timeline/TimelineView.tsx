import { useState, useMemo, useRef, useCallback, useEffect } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import type { ReactNode } from 'react'
import { Network, AlertTriangle, RefreshCw } from 'lucide-react'
import {
  clampLensToSelection,
  deriveLiveSelection,
  isLensLatched,
  advanceLatchedLens,
  buildAppMembershipIndex,
  isPinnedLaneRef,
  LIVE_TICK_MS,
  type ScrubberRange,
  type TimelineLiveState,
  type TimelineGrouping,
  type TimelineSort,
  type PinnedLaneRef,
} from '@skyhook-io/k8s-ui'
import { TimelineList } from './TimelineList'
import type { ActivityFilterKey } from './TimelineList'
import { TimelineSwimlanes } from './TimelineSwimlanes'
import { RetainedTimelineScrubber, extendSelection, type ScrubberDomainInfo } from './RetainedTimelineScrubber'
import { LocalTimelineScrubber } from './LocalTimelineScrubber'
import { useTopology, useApplications } from '../../api/client'
import { useTimelineSource } from '../../context/TimelineSource'
import type { Topology } from '../../types'
import type { NavigateToResource } from '../../utils/navigation'
import { LargeClusterNamespacePicker } from '../shared/LargeClusterNamespacePicker'

// Stable empty array to avoid creating new references on every render
const EMPTY_EVENTS: never[] = []

// Pinned-lane persistence (survives refresh + view toggle). A pin record stores
// enough to render the label with zero event data — either a resource ref or an
// app-group ref. isPinnedLaneRef tolerates legacy entries (resource refs written
// before app-group pins existed carry no `type` discriminant).
const PINNED_LANES_KEY = 'radar.timeline.pinnedLanes'
function loadPinnedLanes(): PinnedLaneRef[] {
  try {
    const raw = localStorage.getItem(PINNED_LANES_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.filter(isPinnedLaneRef)
  } catch {
    return []
  }
}

// Helper to check if topology has meaningfully changed
function topologyContentEqual(a: Topology | undefined, b: Topology | undefined): boolean {
  if (a === b) return true
  if (!a || !b) return false
  if (a.nodes.length !== b.nodes.length) return false
  if (a.edges.length !== b.edges.length) return false
  // Compare node IDs (fast check for structural changes)
  const aNodeIds = a.nodes.map(n => n.id).sort().join(',')
  const bNodeIds = b.nodes.map(n => n.id).sort().join(',')
  return aNodeIds === bNodeIds
}

import type { TimeRange } from '../../types'

export type TimelineViewMode = 'list' | 'swimlane'
export type { ActivityTypeFilter } from './TimelineList'

// Retained-mode selection model: a relative live window, or a pinned absolute one.
type TimelineMode =
  // `all` marks a live window meant to cover the WHOLE data span: its width is
  // re-derived from the scrubber domain each tick, so a growing local ring
  // (toMs = now advances) never slides the left edge off the oldest data the
  // way a fixed widthMs would. widthMs remains the fallback until the domain
  // is known.
  | { kind: 'live'; widthMs: number; all?: boolean }
  | { kind: 'frozen'; fromMs: number; toMs: number }

// ---------------------------------------------------------------------------
// URL persistence for the retained-timeline control surface.
//
// Every control TimelineView owns is mirrored into the query string so a
// timeline is deep-linkable and the browser back/forward buttons restore it.
// The URL is the source of truth on mount and is rewritten on every user
// change; each param is OMITTED at its default so a pristine timeline keeps a
// clean URL. Live mode encodes only a relative window (never absolute times) so
// a restored live link is still live at restore time; the 30s live tick moves
// no absolute state and therefore never touches the URL.
// ---------------------------------------------------------------------------
// Default query: the last hour. Wide-enough for "what just happened" without
// burying the lanes in a day of history; presets/URL widen it deliberately.
const DEFAULT_LIVE_WIDTH_MS = 60 * 60 * 1000
const DAY_MS = 24 * 60 * 60 * 1000
// Fallback cap for a retained hand-entered ?from&to when the source doesn't
// declare maxRangeDays — mirrors the retained source's own default.
const DEFAULT_MAX_RANGE_DAYS = 7
const DEFAULT_VIEW: TimelineViewMode = 'swimlane'
const DEFAULT_GROUPING: TimelineGrouping = 'app'
const DEFAULT_SORT: TimelineSort = 'importance'
const ACTIVITY_KEYS: readonly ActivityFilterKey[] = ['changes', 'k8s_events', 'warnings', 'unhealthy']
const GROUPINGS: readonly TimelineGrouping[] = ['app', 'owner', 'flat']
const SORTS: readonly TimelineSort[] = ['importance', 'recent', 'name']
// Keys that update at typing / brush-commit frequency — their writes use
// history replace so they don't flood the back stack; discrete toggles push.
// `event` (the open drawer's selected id) joins this set: drawer selection is
// high-frequency (every open/select/close), so its writes replace rather than
// flood the back stack.
const HIGH_FREQ_KEYS = new Set(['q', 'from', 'to', 'window', 'event'])

interface PersistedTimelineState {
  viewMode: TimelineViewMode
  mode: TimelineMode
  showDeleted: boolean
  pinnedOnly: boolean
  search: string
  activityFilter: ActivityFilterKey[]
  kindFilter: string[]
  grouping: TimelineGrouping
  sort: TimelineSort
  selectedEventId: string | null
}

function parseView(sp: URLSearchParams): TimelineViewMode | undefined {
  const v = sp.get('view')
  return v === 'list' || v === 'swimlane' ? v : undefined
}

function parseActivity(sp: URLSearchParams): ActivityFilterKey[] | undefined {
  const raw = sp.get('activity')
  if (raw == null) return undefined
  return raw
    .split(',')
    .map((s) => s.trim())
    .filter((s): s is ActivityFilterKey => (ACTIVITY_KEYS as readonly string[]).includes(s))
}

function parseKinds(sp: URLSearchParams): string[] {
  const raw = sp.get('kinds')
  if (!raw) return []
  return raw.split(',').map((s) => s.trim()).filter(Boolean)
}

function parseEnum<T extends string>(value: string | null, allowed: readonly T[], fallback: T): T {
  return value != null && (allowed as readonly string[]).includes(value) ? (value as T) : fallback
}

// Absolute [from,to] wins (a frozen link); else a relative live window; else the
// pristine default live window. Only meaningful in retained mode. `maxRangeDays`
// caps a hand-entered ?from&to to the same horizon the preset/fetch path
// enforces (retained only; local loads the whole ring and passes it undefined).
function parseTimeMode(sp: URLSearchParams, isRetained: boolean, maxRangeDays?: number): TimelineMode {
  if (isRetained) {
    const from = sp.get('from')
    const to = sp.get('to')
    if (from != null && to != null) {
      const f = Number(from)
      const t = Number(to)
      if (Number.isInteger(f) && Number.isInteger(t) && f > 0 && f < t) {
        const fromMs = maxRangeDays != null ? Math.max(f, t - maxRangeDays * DAY_MS) : f
        return { kind: 'frozen', fromMs, toMs: t }
      }
    }
    const w = sp.get('window')
    if (w === 'all') {
      // The width is a fallback until the scrubber domain lands; the flag makes
      // the live selection track the whole span from then on.
      return { kind: 'live', widthMs: DEFAULT_LIVE_WIDTH_MS, all: true }
    }
    if (w != null) {
      const wm = Number(w)
      if (Number.isFinite(wm) && wm > 0) return { kind: 'live', widthMs: Math.round(wm) }
    }
  }
  return { kind: 'live', widthMs: DEFAULT_LIVE_WIDTH_MS }
}

function timeModeEqual(a: TimelineMode, b: TimelineMode): boolean {
  if (a.kind === 'live' && b.kind === 'live') return a.widthMs === b.widthMs && (a.all ?? false) === (b.all ?? false)
  if (a.kind === 'frozen' && b.kind === 'frozen') return a.fromMs === b.fromMs && a.toMs === b.toMs
  return false
}

function arraysEqual(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((x, i) => x === b[i])
}

// Rebuild the query string from state, preserving any foreign keys already on
// the URL and stripping the legacy home-page `filter` seed (superseded by
// `activity`). Every param is omitted at its default.
function writeTimelineParams(
  base: URLSearchParams,
  s: PersistedTimelineState,
  opts: { isRetained: boolean; requiresNamespaceFilter: boolean | undefined },
): URLSearchParams {
  const p = new URLSearchParams(base)
  const set = (k: string, v: string | null) => (v == null ? p.delete(k) : p.set(k, v))

  // Never persist the list view forced by a large cluster — only a real choice.
  set('view', !opts.requiresNamespaceFilter && s.viewMode === 'list' ? 'list' : null)

  // Live is encoded by `window` alone (a relative width, never absolute times, so
  // a restored live link is still live); `from`+`to` encode a frozen range. The
  // `mode` param is deliberately NOT used — App.tsx owns it for the topology view
  // and strips foreign `mode` values.
  if (opts.isRetained && s.mode.kind === 'frozen') {
    set('from', String(s.mode.fromMs))
    set('to', String(s.mode.toMs))
    set('window', null)
  } else if (opts.isRetained && s.mode.kind === 'live' && s.mode.all) {
    set('window', 'all')
    set('from', null)
    set('to', null)
  } else if (opts.isRetained && s.mode.kind === 'live' && s.mode.widthMs !== DEFAULT_LIVE_WIDTH_MS) {
    set('window', String(s.mode.widthMs))
    set('from', null)
    set('to', null)
  } else {
    set('window', null)
    set('from', null)
    set('to', null)
  }

  set('activity', s.activityFilter.length ? s.activityFilter.join(',') : null)
  set('kinds', s.kindFilter.length ? s.kindFilter.join(',') : null)
  set('deleted', s.showDeleted ? null : '0')
  set('pinnedOnly', s.pinnedOnly ? '1' : null)
  set('q', s.search.length ? s.search : null)
  set('grouping', s.grouping !== DEFAULT_GROUPING ? s.grouping : null)
  set('sort', s.sort !== DEFAULT_SORT ? s.sort : null)
  set('event', s.selectedEventId)
  p.delete('filter')
  return p
}

// A diff is "replace-worthy" when every changed key is high-frequency; a
// discrete toggle changing pushes a history entry so back/forward step through
// control states.
function onlyHighFreqDiffer(a: string, b: string): boolean {
  const pa = new URLSearchParams(a)
  const pb = new URLSearchParams(b)
  const keys = new Set<string>([...pa.keys(), ...pb.keys()])
  let any = false
  for (const k of keys) {
    if (pa.get(k) !== pb.get(k)) {
      any = true
      if (!HIGH_FREQ_KEYS.has(k)) return false
    }
  }
  return any
}

interface TimelineViewProps {
  namespaces: string[]
  onResourceClick?: NavigateToResource
  initialViewMode?: TimelineViewMode
  initialFilter?: 'all' | 'changes' | 'k8s_events' | 'warnings' | 'unhealthy'
  initialTimeRange?: TimeRange
  requiresNamespaceFilter?: boolean
  availableNamespaces?: { name: string }[]
  onNamespaceSelect?: (ns: string) => void
}

export function TimelineView({ namespaces, onResourceClick, initialViewMode, initialFilter, initialTimeRange, requiresNamespaceFilter, availableNamespaces, onNamespaceSelect }: TimelineViewProps) {
  // URL is the source of truth for every control below (deep-linkable +
  // back/forward-restorable). Read on mount, written on user change.
  const [searchParams, setSearchParams] = useSearchParams()

  // Force list view on large clusters without namespace filter; otherwise the
  // URL `view` (or the home-page seed) decides.
  const effectiveInitialMode = requiresNamespaceFilter ? 'list' : (parseView(searchParams) ?? initialViewMode ?? DEFAULT_VIEW)
  const [viewMode, setViewMode] = useState<TimelineViewMode>(effectiveInitialMode)
  // Shared across list + swimlane so the toggle carries across the view switch,
  // and so the swimlane fetch can exclude deletes server-side (before LIMIT)
  // rather than only hiding them client-side after the 10k cap.
  const [showDeleted, setShowDeleted] = useState(() => searchParams.get('deleted') !== '0')
  // ?pinnedOnly=1 is inert without pins: honoring it with no stored pins would
  // arm a filter that hides everything. Gate the read on stored pins so the param
  // can never arm on its own — ordering-proof, independent of when the empty-pins
  // reset effect runs relative to the URL-sync effect below.
  const [pinnedOnly, setPinnedOnly] = useState(() => searchParams.get('pinnedOnly') === '1' && loadPinnedLanes().length > 0)
  // Search / activity-type / kind lifted here too, so they survive the view
  // switch and drive both views through one source of truth.
  const [search, setSearch] = useState(() => searchParams.get('q') ?? '')
  // Seed the multi-select from the URL `activity` csv, else the home-page
  // deep-link preset: 'all'/undefined means no chips selected (everything).
  const [activityFilter, setActivityFilter] = useState<ActivityFilterKey[]>(
    () => parseActivity(searchParams) ?? (initialFilter && initialFilter !== 'all' ? [initialFilter] : []),
  )
  const [kindFilter, setKindFilter] = useState<string[]>(() => parseKinds(searchParams))
  // Lane grouping mode, lifted here so it survives the list↔swimlane switch like
  // the other view options.
  const [grouping, setGrouping] = useState<TimelineGrouping>(() => parseEnum(searchParams.get('grouping'), GROUPINGS, DEFAULT_GROUPING))
  // Lane sort mode, lifted alongside grouping for the same reason.
  const [sort, setSort] = useState<TimelineSort>(() => parseEnum(searchParams.get('sort'), SORTS, DEFAULT_SORT))
  // The swimlane drawer's open event (routable deep link). The swimlane reports
  // its selection here (open/select/close) and restores from it on mount; a stale
  // id it can't resolve after data settles is stripped back to null.
  const [selectedEventId, setSelectedEventId] = useState<string | null>(() => searchParams.get('event'))

  // Pinned lanes: stationary rows the user keeps in view while moving the lens.
  // State + localStorage persistence live here (the k8s-ui swimlane is pure and
  // owns no storage), so pins survive the list↔swimlane toggle and a refresh.
  const [pinnedLanes, setPinnedLanes] = useState<PinnedLaneRef[]>(() => loadPinnedLanes())
  useEffect(() => {
    try {
      localStorage.setItem(PINNED_LANES_KEY, JSON.stringify(pinnedLanes))
    } catch {
      // Storage unavailable (private mode / quota) — pins stay in-memory only.
    }
  }, [pinnedLanes])
  const togglePin = useCallback((ref: PinnedLaneRef) => {
    setPinnedLanes((prev) => (
      prev.some((p) => p.id === ref.id) ? prev.filter((p) => p.id !== ref.id) : [...prev, ref]
    ))
  }, [])
  // pinnedOnly is meaningless with no pins: unpinning the last lane (or landing
  // on ?pinnedOnly=1 with no stored pins) would otherwise leave the filter stuck
  // on, silently re-hiding everything the moment a lane is pinned again. Drop it
  // whenever pins empty out; the URL-sync effect carries the reset to the URL.
  useEffect(() => {
    if (pinnedLanes.length === 0) {
      setPinnedOnly((prev) => (prev ? false : prev))
    }
  }, [pinnedLanes])

  // App-group name → the Applications page's deep link (?app=<AppRow.key>),
  // mirroring resource-name navigation.
  const navigate = useNavigate()
  const handleAppClick = useCallback((appKey: string) => {
    navigate(`/applications?app=${encodeURIComponent(appKey)}`)
  }, [navigate])

  // Only fetch heavy swimlane data when actually showing swimlanes
  const showSwimlanes = viewMode === 'swimlane' && !requiresNamespaceFilter

  const timelineSource = useTimelineSource()
  const isRetained = timelineSource.capabilities.mode === 'retained'
  const isLocal = timelineSource.capabilities.mode === 'local'
  // Cap for a hand-entered ?from&to, mirroring the retained fetch window's cap.
  // Local mode loads the whole ring and carries no day horizon, so it stays
  // uncapped (undefined).
  const retainedMaxRangeDays = isRetained ? (timelineSource.capabilities.maxRangeDays ?? DEFAULT_MAX_RANGE_DAYS) : undefined
  // The local strip rides both views, matching retained: the ring fetch below
  // is enabled whenever the strip is shown, so list mode has data to bucket.
  // Large clusters that require a namespace filter skip the strip — the same
  // full-ring load the swimlane gate avoids — and the list then shows its own
  // range dropdown instead (selectionWindow is only passed when a scrubber is
  // on screen to own the range).
  const showLocalScrubber = isLocal && !requiresNamespaceFilter
  const showScrubber = isRetained || showLocalScrubber

  // Both sources drive a scrubber now: retained fetches a server overview, local
  // derives one client-side from the loaded ring. The time-selection machinery
  // (mode/selection/lens/live) is therefore active in both; the per-source
  // difference is the fetch window, the gap band (retention-only), and which
  // scrubber component renders.
  //   LIVE (relative)  — a fixed width pinned to now; slides on a 30s tick.
  //   FROZEN (absolute) — an explicit [from,to] pinned by any range action.
  // The concrete selection is DERIVED each render so it drives both the list and
  // swimlane fetches and survives the view toggle.
  const [mode, setMode] = useState<TimelineMode>(() => parseTimeMode(searchParams, isRetained || isLocal, retainedMaxRangeDays))
  // Freeze time surfaced on the paused chip ("as of HH:MM"); stamped when the
  // selection freezes. Null while live.
  const [frozenAsOfMs, setFrozenAsOfMs] = useState<number | null>(null)

  // 30s clock, ticked only while live. A live selection reads this; frozen mode
  // ignores it and the interval is torn down so nothing auto-updates.
  const [nowTick, setNowTick] = useState(() => Date.now())
  useEffect(() => {
    if (mode.kind !== 'live') return
    const id = setInterval(() => setNowTick(Date.now()), LIVE_TICK_MS)
    return () => clearInterval(id)
  }, [mode.kind])

  // Server-derived domain + per-request cap, lifted from the scrubber so extend
  // requests clamp to the real retained window (and "all" live widths track it).
  const [scrubberDomain, setScrubberDomain] = useState<ScrubberDomainInfo | null>(null)

  const selection = useMemo<ScrubberRange>(() => {
    if (mode.kind === 'live') {
      // An "all" live window re-derives its width from the current domain so a
      // growing ring never slides the left edge off the oldest held data.
      const width = mode.all && scrubberDomain ? scrubberDomain.maxSelectionMs : mode.widthMs
      return deriveLiveSelection(width, nowTick)
    }
    return { fromMs: mode.fromMs, toMs: mode.toMs }
  }, [mode, nowTick, scrubberDomain])

  // The LENS: the swimlane's visible window WITHIN the applied selection. Free
  // client-side exploration — kept in sync with both the scrubber band and the
  // swimlane, and always clamped inside the selection. Default: the most-recent
  // hour of the selection (what the swimlane shows at its default zoom), or the
  // whole selection if that's narrower.
  const DEFAULT_LENS_MS = 60 * 60 * 1000
  const [lensWindow, setLensWindow] = useState<ScrubberRange>(() => {
    const width = Math.min(DEFAULT_LENS_MS, selection.toMs - selection.fromMs)
    return { fromMs: selection.toMs - width, toMs: selection.toMs }
  })

  // Recording gaps lifted from the scrubber so the swimlane renders matching
  // offline bands + empty-state copy.
  const [gaps, setGaps] = useState<ScrubberRange[]>([])

  // List mode's lens source: the time span of the rows visible in the list's
  // scrollport, reported by the list on scroll. Dragging the strip band works
  // the other way: it sets a scroll target the list jumps to.
  const [listVisibleWindow, setListVisibleWindow] = useState<ScrubberRange | null>(null)
  const [listScrollToMs, setListScrollToMs] = useState<number | undefined>(undefined)

  // Latest selection for clamping the lens without re-creating the setter.
  const selectionRef = useRef(selection)
  selectionRef.current = selection

  // Carry the swimlane window into the list ONCE, at the moment of the switch.
  // Deriving the scroll target from the live lensWindow instead would re-scroll
  // the list on every live tick as the latched lens edge advances. Leaving list
  // view drops both the target and the last reported scrollport window — stale
  // values would otherwise flash as the band/lens on the next visit.
  const lensWindowRef = useRef(lensWindow)
  lensWindowRef.current = lensWindow
  const showScrubberRef = useRef(showScrubber)
  showScrubberRef.current = showScrubber
  useEffect(() => {
    if (viewMode === 'list') {
      setListScrollToMs(showScrubberRef.current ? lensWindowRef.current.toMs : undefined)
    } else {
      setListScrollToMs(undefined)
      setListVisibleWindow(null)
    }
  }, [viewMode])

  // Single writer for the lens: every update (band drag or swimlane pan/zoom) is
  // clamped inside the current selection so the lens can never leave the query.
  const setLens = useCallback((next: ScrubberRange) => {
    setLensWindow(clampLensToSelection(next, selectionRef.current))
  }, [])

  // Live slide: on each tick, a lens LATCHED to the live edge advances with the
  // selection (keeping width); one the user dragged into the past stays put and
  // is only re-clamped if the sliding left edge would push it out. Runs only in
  // live mode; frozen mode never ticks.
  useEffect(() => {
    if (mode.kind !== 'live') return
    setLensWindow((prev) => (
      isLensLatched(prev, selection)
        ? clampLensToSelection(advanceLatchedLens(prev, selection), selection)
        : clampLensToSelection(prev, selection)
    ))
  }, [nowTick, mode.kind, selection])

  const resetLensToRecent = useCallback((sel: ScrubberRange) => {
    const width = Math.min(DEFAULT_LENS_MS, sel.toMs - sel.fromMs)
    setLensWindow({ fromMs: sel.toMs - width, toMs: sel.toMs })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Picking a query preset shows the WHOLE new span (band == query): "Last 24h"
  // renders 24h, not just its recent hour. Window-narrowing stays reserved for
  // explicit zoom/drag, which routes through resetLensToRecent.
  const resetLensToFull = useCallback((sel: ScrubberRange) => {
    setLensWindow({ fromMs: sel.fromMs, toMs: sel.toMs })
  }, [])

  // Any explicit range action from the scrubber (brush Run-query, pan arrows,
  // zoom ±, grab-pan, handle drag, domain clamp) → FROZEN. Nothing auto-updates
  // until a manual refresh. The lens resets to the recent slice of the new range.
  const handleSelectionChange = useCallback((sel: ScrubberRange) => {
    setMode({ kind: 'frozen', fromMs: sel.fromMs, toMs: sel.toMs })
    setFrozenAsOfMs(Date.now())
    resetLensToRecent(sel)
  }, [resetLensToRecent])

  // A domain clamp (the derived selection outgrew the ring/retained window) is
  // NOT a user range action, so it must preserve the current mode: LIVE stays
  // LIVE, narrowed to the clamped width (a fresh <24h cluster keeps auto-
  // updating instead of freezing on first load); FROZEN stays frozen at the
  // clamped range. Never stamps "as of" — that belongs to real freezes only.
  const handleSelectionClamp = useCallback((sel: ScrubberRange) => {
    setMode((prev) => (
      prev.kind === 'live'
        // `all` survives a clamp: the clamp narrowed the window to what the
        // domain can hold right now, which is exactly what all-mode re-derives
        // next tick anyway.
        ? { kind: 'live', widthMs: sel.toMs - sel.fromMs, all: prev.all }
        : { kind: 'frozen', fromMs: sel.fromMs, toMs: sel.toMs }
    ))
    resetLensToRecent(sel)
  }, [resetLensToRecent])

  // Preset click → LIVE with that width (capped to the retained window). Pins to
  // now, starts the tick, and shows the whole new span (window == query).
  const handlePresetSelect = useCallback((widthMs: number) => {
    const capped = scrubberDomain ? Math.min(widthMs, scrubberDomain.maxSelectionMs) : widthMs
    // Only local mode has a domain-tracking maximum ("All" = the whole ring);
    // retained mode's cap is a fixed per-request limit, so its presets stay
    // plain fixed widths.
    const all = isLocal && scrubberDomain != null && widthMs >= scrubberDomain.maxSelectionMs
    const now = Date.now()
    setMode({ kind: 'live', widthMs: capped, all: all || undefined })
    setFrozenAsOfMs(null)
    setNowTick(now)
    resetLensToFull(deriveLiveSelection(capped, now))
  }, [isLocal, scrubberDomain, resetLensToFull])

  // "→ Now" → LIVE, width = current selection width. Pins to now and resets the
  // lens to the live edge.
  const handleJumpToNow = useCallback(() => {
    const cur = selectionRef.current
    const width = cur.toMs - cur.fromMs
    const now = Date.now()
    setMode({ kind: 'live', widthMs: width })
    setFrozenAsOfMs(null)
    setNowTick(now)
    resetLensToRecent(deriveLiveSelection(width, now))
  }, [resetLensToRecent])

  // The single scrubber-chip action:
  //   frozen           → return to LIVE at the current selection width.
  //   live + unlatched → re-latch the lens to the live edge (jump to now).
  //   live + latched   → no-op (already following now).
  const handleLiveChipClick = useCallback(() => {
    if (mode.kind === 'frozen') {
      handleJumpToNow()
      return
    }
    if (!isLensLatched(lensWindow, selectionRef.current)) {
      resetLensToRecent(selectionRef.current)
    }
  }, [mode.kind, lensWindow, resetLensToRecent, handleJumpToNow])

  // Extend grows the APPLIED selection 50% in one direction → FROZEN (explicit
  // range action). Reads the current concrete selection so it works from either
  // mode. The lens is preserved inside the now-larger selection.
  const handleExtendRequest = useCallback((dir: 'past' | 'future') => {
    const info = scrubberDomain
    if (!info) return
    const ext = extendSelection(selectionRef.current, dir, info.domain, info.maxSelectionMs)
    setMode({ kind: 'frozen', fromMs: ext.fromMs, toMs: ext.toMs })
    setFrozenAsOfMs(Date.now())
  }, [scrubberDomain])

  // Live/paused chip state (any scrubber source). `latched` reflects whether the
  // lens still rides the live edge — an unlatched live chip offers a jump-to-now.
  // (The frozen "new events" count is filled in by the scrubber, which owns the
  // overview buckets.) Consumed only where a scrubber renders; inert otherwise.
  const liveState = useMemo<TimelineLiveState | undefined>(() => {
    if (!isRetained && !isLocal) return undefined
    if (mode.kind === 'live') return { kind: 'live', latched: isLensLatched(lensWindow, selection) }
    return { kind: 'frozen', asOfMs: frozenAsOfMs ?? mode.toMs }
  }, [isRetained, isLocal, mode, frozenAsOfMs, lensWindow, selection])

  // --- URL <-> state binding -------------------------------------------------
  // Two effects with strictly-scoped deps keep the loop from feeding itself:
  //   * URL -> state derives every field from searchParams; each setter is guarded
  //     to no-op when the value is unchanged. Non-URL deps (e.g. pinnedLanes) can
  //     re-run it, but re-deriving from the same URL is idempotent and can't
  //     clobber user state back to an old value. It fires on mount and back/forward.
  //   * state -> URL keys on the persisted fields (searchParams read via a ref,
  //     off the dep list) so it writes on user changes but the browser-driven
  //     URL change lands as a no-op (target === current). The live tick moves no
  //     persisted field, so it writes nothing.
  const searchParamsRef = useRef(searchParams)
  searchParamsRef.current = searchParams
  // setSearchParams gets a new identity whenever the URL changes (react-router
  // closes over searchParams). If it sat in the write effect's deps, a
  // back/forward navigation would fire the write in the SAME commit as the
  // URL->state read — with pre-sync state — pushing the old URL back. State and
  // URL then swap values every commit until React aborts (#185). Reading the
  // setter through a ref keeps the write keyed on persisted state alone.
  const setSearchParamsRef = useRef(setSearchParams)
  setSearchParamsRef.current = setSearchParams
  const didMountUrlSyncRef = useRef(false)

  useEffect(() => {
    const sp = searchParams
    const nextView = requiresNamespaceFilter ? 'list' : (parseView(sp) ?? DEFAULT_VIEW)
    setViewMode((prev) => (prev === nextView ? prev : nextView))
    const nextMode = parseTimeMode(sp, isRetained || isLocal, retainedMaxRangeDays)
    setMode((prev) => (timeModeEqual(prev, nextMode) ? prev : nextMode))
    const nextDeleted = sp.get('deleted') !== '0'
    setShowDeleted((prev) => (prev === nextDeleted ? prev : nextDeleted))
    // Same guard as the lazy init: the param can only arm the filter when pins
    // exist, so a mount that runs this after the empty-pins reset can't re-arm it.
    const nextPinnedOnly = sp.get('pinnedOnly') === '1' && pinnedLanes.length > 0
    setPinnedOnly((prev) => (prev === nextPinnedOnly ? prev : nextPinnedOnly))
    const nextSearch = sp.get('q') ?? ''
    setSearch((prev) => (prev === nextSearch ? prev : nextSearch))
    const nextActivity = parseActivity(sp) ?? []
    setActivityFilter((prev) => (arraysEqual(prev, nextActivity) ? prev : nextActivity))
    const nextKinds = parseKinds(sp)
    setKindFilter((prev) => (arraysEqual(prev, nextKinds) ? prev : nextKinds))
    const nextGrouping = parseEnum(sp.get('grouping'), GROUPINGS, DEFAULT_GROUPING)
    setGrouping((prev) => (prev === nextGrouping ? prev : nextGrouping))
    const nextSort = parseEnum(sp.get('sort'), SORTS, DEFAULT_SORT)
    setSort((prev) => (prev === nextSort ? prev : nextSort))
    const nextEvent = sp.get('event')
    setSelectedEventId((prev) => (prev === nextEvent ? prev : nextEvent))
  }, [searchParams, isRetained, isLocal, requiresNamespaceFilter, pinnedLanes, retainedMaxRangeDays])

  useEffect(() => {
    const current = searchParamsRef.current
    const target = writeTimelineParams(
      current,
      { viewMode, mode, showDeleted, pinnedOnly, search, activityFilter, kindFilter, grouping, sort, selectedEventId },
      { isRetained: isRetained || isLocal, requiresNamespaceFilter },
    )
    const targetStr = target.toString()
    const currentStr = current.toString()
    if (targetStr === currentStr) {
      didMountUrlSyncRef.current = true
      return
    }
    // Mount-time normalization (stripping stale/invalid params, migrating the
    // legacy `filter` seed) must not leave a back entry.
    const replace = !didMountUrlSyncRef.current || onlyHighFreqDiffer(currentStr, targetStr)
    didMountUrlSyncRef.current = true
    setSearchParamsRef.current(target, { replace })
  }, [viewMode, mode, showDeleted, pinnedOnly, search, activityFilter, kindFilter, grouping, sort, selectedEventId, isRetained, isLocal, requiresNamespaceFilter])

  // Fetch all activity - zoom controls what's visible in the UI. The heavy 10k
  // ring feeds the swimlanes and the local strip's histogram, so it also runs in
  // list mode when that strip is shown; the list itself fetches its own 2000.
  const { data: activity, isLoading, isError, refetch } = timelineSource.useEvents({
    namespaces,
    timeRange: 'all',
    includeK8sEvents: true,
    includeManaged: true,
    includeDeleted: showDeleted,
    limit: 10000,
    // The local strip derives its histogram from this ring fetch, so it must
    // run in list mode too whenever the strip is shown.
    enabled: showSwimlanes || showLocalScrubber,
    fromMs: isRetained ? selection.fromMs : undefined,
    toMs: isRetained ? selection.toMs : undefined,
    sliding: isRetained && mode.kind === 'live',
  })

  // Fetch topology for service stack grouping — skip on large clusters (empty anyway)
  const { data: rawTopology } = useTopology(namespaces, 'resources', { enabled: showSwimlanes })

  // Server application grouping — the single grouping authority. Joined to the
  // timeline lanes client-side via the membership index. A failed/absent fetch
  // leaves the index undefined; the swimlane degrades to its legacy label
  // grouping (no crash, events still render).
  // Only the app-grouping swimlane path consumes the membership index; gate the
  // fetch (and its background poll) on that so list view / non-app groupings
  // don't drive an unused /applications poll. Disabled → appsData undefined →
  // appIndex undefined → the swimlane's legacy owner-label fallback.
  const { data: appsData, dataUpdatedAt: appsUpdatedAt } = useApplications(namespaces, {
    enabled: showSwimlanes && grouping === 'app',
  })
  const appIndex = useMemo(
    () => (appsData?.applications ? buildAppMembershipIndex(appsData.applications) : undefined),
    // Memoize on the fetch identity, not the array ref: React Query hands back a
    // fresh object each poll even when the data is equal, and rebuilding the
    // index would reshuffle lanes mid-view. dataUpdatedAt only advances on a real
    // successful fetch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [appsUpdatedAt],
  )

  // Stabilize topology reference to prevent unnecessary lane recomputation
  // Only update the stable topology when the content meaningfully changes
  const topologyRef = useRef<Topology | undefined>(undefined)
  const stableTopology = useMemo(() => {
    if (topologyContentEqual(topologyRef.current, rawTopology)) {
      return topologyRef.current
    }
    topologyRef.current = rawTopology
    return rawTopology
  }, [rawTopology])

  // Use stable reference for events to prevent unnecessary re-renders
  const events = activity ?? EMPTY_EVENTS

  // The scrubber sits above whichever view is active, sharing one selection
  // across list + swimlane. Retained draws its server-overview strip; local
  // derives the strip client-side from the loaded ring and omits the gap band.
  const wrap = (node: ReactNode): ReactNode => {
    if (!showScrubber) return node
    // In list mode the lens mirrors the rows visible in the list's scrollport
    // (scrolling moves it) — and dragging the band works the OTHER way too: it
    // scrolls the list to that time (two-way, like the swimlane). In swimlane
    // mode it is the interactive zoom window.
    const isListView = viewMode === 'list'
    const lens = isListView ? listVisibleWindow ?? undefined : lensWindow
    const onLensChange = isListView
      ? (l: ScrubberRange) => setListScrollToMs(l.toMs)
      : setLens
    return (
      <div className="flex-1 flex flex-col min-h-0">
        {isRetained ? (
          <RetainedTimelineScrubber
            source={timelineSource}
            selection={selection}
            onSelectionChange={handleSelectionChange}
            onSelectionClamp={handleSelectionClamp}
            onPresetSelect={handlePresetSelect}
            lens={lens}
            onLensChange={onLensChange}
            lensResizable={!isListView}
            onDomainChange={setScrubberDomain}
            onGapsChange={setGaps}
            liveState={liveState}
            onLiveChipClick={handleLiveChipClick}
          />
        ) : (
          <LocalTimelineScrubber
            events={events}
            loading={isLoading}
            isError={isError}
            selection={selection}
            onSelectionChange={handleSelectionChange}
            onSelectionClamp={handleSelectionClamp}
            onPresetSelect={handlePresetSelect}
            lens={lens}
            onLensChange={onLensChange}
            lensResizable={!isListView}
            onDomainChange={setScrubberDomain}
            liveState={liveState}
            onLiveChipClick={handleLiveChipClick}
          />
        )}
        <div className="flex-1 flex flex-col min-h-0">{node}</div>
      </div>
    )
  }

  if (viewMode === 'swimlane') {
    // Large cluster without namespace: show picker instead of swimlanes
    if (requiresNamespaceFilter) {
      return wrap(
        <div className="flex-1 flex flex-col">
          {/* Toolbar with view toggle so user can switch back to list */}
          <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border">
            <div />
            <ViewModeToggle viewMode={viewMode} onViewModeChange={setViewMode} />
          </div>
          <div className="flex-1 flex items-center justify-center">
            <div className="max-w-md w-full mx-4 text-center">
              <div className="bg-theme-surface border border-theme-border rounded-xl shadow-theme-lg p-6">
                <div className="w-12 h-12 mx-auto mb-4 rounded-full bg-skyhook-500/10 flex items-center justify-center">
                  <Network className="w-6 h-6 text-skyhook-400" />
                </div>
                <h2 className="text-lg font-semibold text-theme-text-primary mb-2">
                  Large Cluster Detected
                </h2>
                <p className="text-sm text-theme-text-secondary mb-5">
                  Swimlane view requires a namespace filter on large clusters.
                  Select a namespace or switch to list view.
                </p>
                <div className="relative">
                  <LargeClusterNamespacePicker
                    namespaces={availableNamespaces}
                    onSelect={(ns) => onNamespaceSelect?.(ns)}
                  />
                </div>
              </div>
            </div>
          </div>
        </div>
      )
    }

    // A failed fetch must not render as the swimlane "No events yet" empty state —
    // that reads as a quiet cluster rather than a load failure.
    if (isError) {
      return wrap(
        <div className="flex-1 flex flex-col">
          <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border">
            <div />
            <ViewModeToggle viewMode={viewMode} onViewModeChange={setViewMode} />
          </div>
          <div className="flex-1 flex flex-col items-center justify-center text-theme-text-tertiary gap-3">
            <AlertTriangle className="w-10 h-10 text-amber-400/70" />
            <p className="text-base">Failed to load timeline data</p>
            <button
              onClick={() => refetch()}
              className="flex items-center gap-2 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border-light rounded-lg hover:bg-theme-hover transition-colors"
            >
              <RefreshCw className="w-3.5 h-3.5" />
              Try again
            </button>
          </div>
        </div>
      )
    }

    return wrap(
      <TimelineSwimlanes
        events={events}
        isLoading={isLoading}
        onResourceClick={onResourceClick}
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        topology={stableTopology}
        namespaces={namespaces}
        showDeleted={showDeleted}
        onShowDeletedChange={setShowDeleted}
        pinnedOnly={pinnedOnly}
        onPinnedOnlyChange={setPinnedOnly}
        search={search}
        onSearchChange={setSearch}
        activityFilter={activityFilter}
        onActivityFilterChange={setActivityFilter}
        kindFilter={kindFilter}
        onKindFilterChange={setKindFilter}
        appIndex={appIndex}
        grouping={grouping}
        onGroupingChange={setGrouping}
        sort={sort}
        onSortChange={setSort}
        viewWindow={showScrubber ? lensWindow : undefined}
        onViewWindowChange={showScrubber ? setLens : undefined}
        bounds={showScrubber ? selection : undefined}
        onExtendRequest={showScrubber ? handleExtendRequest : undefined}
        nowMs={showScrubber ? nowTick : undefined}
        isLive={showScrubber && mode.kind === 'live'}
        onAppClick={handleAppClick}
        gaps={isRetained ? gaps : undefined}
        pinnedLanes={pinnedLanes}
        onTogglePin={togglePin}
        selectedEventId={selectedEventId}
        onSelectedEventChange={setSelectedEventId}
      />
    )
  }

  return wrap(
    <TimelineList
      namespaces={namespaces}
      currentView={viewMode}
      onViewChange={setViewMode}
      onResourceClick={onResourceClick}
      initialFilter={initialFilter}
      initialTimeRange={initialTimeRange}
      showDeleted={showDeleted}
      onShowDeletedChange={setShowDeleted}
      search={search}
      onSearchChange={setSearch}
      activityFilter={activityFilter}
      onActivityFilterChange={setActivityFilter}
      kindFilter={kindFilter}
      onKindFilterChange={setKindFilter}
      // The shared selection drives the list ONLY when a scrubber is on screen
      // to own the range — passing it scrubber-less would hide the list's own
      // range dropdown and leave the user with no time control at all.
      selectionWindow={showScrubber ? selection : undefined}
      sliding={showScrubber && mode.kind === 'live'}
      onVisibleWindowChange={setListVisibleWindow}
      // Seeded with the swimlane's window at the switch (see the viewMode
      // effect); afterwards, dragging the strip band retargets the scroll.
      scrollToMs={listScrollToMs}
    />
  )
}

function ViewModeToggle({ viewMode, onViewModeChange }: { viewMode: TimelineViewMode, onViewModeChange: (mode: TimelineViewMode) => void }) {
  return (
    <div className="flex items-center gap-1 bg-theme-base rounded-lg p-0.5 border border-theme-border">
      <button
        type="button"
        onClick={() => onViewModeChange('list')}
        className={`px-2 py-1 text-xs rounded-md transition-colors ${viewMode === 'list' ? 'bg-theme-surface text-theme-text-primary shadow-sm' : 'text-theme-text-secondary hover:text-theme-text-primary'}`}
      >
        List
      </button>
      <button
        type="button"
        onClick={() => onViewModeChange('swimlane')}
        className={`px-2 py-1 text-xs rounded-md transition-colors ${viewMode === 'swimlane' ? 'bg-theme-surface text-theme-text-primary shadow-sm' : 'text-theme-text-secondary hover:text-theme-text-primary'}`}
      >
        Swimlane
      </button>
    </div>
  )
}
