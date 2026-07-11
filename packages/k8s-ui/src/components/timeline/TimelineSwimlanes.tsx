import { Fragment, useState, useMemo, useRef, useCallback, useEffect } from 'react'
import { clsx } from 'clsx'
import {
  AlertCircle,
  AlertTriangle,
  RefreshCw,
  ZoomIn,
  ZoomOut,
  ChevronLeft,
  ChevronRight,
  X,
  Clock,
  MemoryStick,
  Package,
  Ban,
  Box,
  Gauge,
  HardDrive,
  Timer,
  RotateCcw,
  Shield,
  Pin,
  ChevronsDownUp,
  ChevronsUpDown,
} from 'lucide-react'
import type { TimelineEvent, Topology } from '../../types'
import type { NavigateToResource } from '../../utils/navigation'
import { kindToPlural, apiVersionToGroup } from '../../utils/navigation'
import { PaneLoader } from '../ui/PaneLoader'
import { TimelineToolbar } from './TimelineToolbar'
import { sortTimelineLanes, TIMELINE_SORT_DEFAULT, type TimelineSort } from './timeline-lane-sort'
import {
  matchesActivityFilter,
  matchesTimelineSearch,
  mergeKindOptions,
  describeActiveFilters,
  type ActivityFilterKey,
} from './timeline-filters'
import { pluralize } from '../../utils/pluralize'
import { gitOpsRouteForKind } from '../../utils/gitops-route'
import { isChangeEvent, isHistoricalEvent, isOperation, displayKind } from '../../types'
import { DiffViewer } from './DiffViewer'
import { getHealthBadgeColor, getEventTypeColor } from '../../utils/badge-colors'
import { MiddleEllipsis } from '../ui/MiddleEllipsis'
import { Tooltip } from '../ui/Tooltip'
import { ResourceRefBadge } from '../ui/drawer-components'
import { buildResourceHierarchy, extractPinnedLanes, removePinnedLanes, isProblematicEvent, laneTrackEvents, isChildVisibleInWindow, collidingLaneKeys, laneCollisionKey, type ResourceLane as BaseResourceLane, type TimelineGrouping, type PinnedLaneRef } from '../../utils/resource-hierarchy'
import { groupQualifiesLaneId } from '../../utils/navigation'
import type { AppMembershipIndex } from '../../utils/applications'
import { Layers } from 'lucide-react'
import {
  formatAxisTime,
  formatFullTime,
  buildHealthSpans,
  buildLaneMemberSpans,
  sweepAggregateHealth,
  formatAggregateHealthTooltip,
  timeToX as sharedTimeToX,
} from './shared'
import { useRegisterShortcut } from '../../hooks/useKeyboardShortcuts'
import { clampLensToSelection, formatLensDuration, MIN_WINDOW_MS, type ScrubberRange } from './scrubber-math'

// Predefined zoom levels (window widths in hours): 15m, 30m, 1h, 2h, 4h, 8h,
// 12h, 1d, 2d, 3d, 7d. Hoisted so the controlled-window adapter can size the
// view from a window width instead of the internal `zoom` state.
const ZOOM_LEVELS = [0.25, 0.5, 1, 2, 4, 8, 12, 24, 48, 72, 168]

const HOUR_MS = 60 * 60 * 1000

// A lane with at most this many direct children auto-expands on first render:
// a small family's whole anatomy is cheap to show, so we reveal it by default.
// Larger families (a Deployment with 20 pods, a big app group) stay collapsed to
// keep the list scannable — the user opens them deliberately. 4 keeps a typical
// Deployment→ReplicaSet(+a few pods) or CronJob→Job×2 open while collapsing noisy
// fan-outs.
export const AUTO_COLLAPSE_THRESHOLD = 4

// Deterministic default expansion, computed from the lane tree alone (no
// storage, no window state) so SSR and the first client render agree. A lane
// with 1..THRESHOLD children starts expanded; 0 children or a large fan-out
// starts collapsed. Recurses to every depth so a small Job under a large CronJob
// still defaults open.
//
// STRUCTURAL count (not the in-window visible subset): the window is time-derived
// (effectiveNow) and would desync SSR from hydration, and keying the default on
// the lens would thrash a family open/closed as the user pans. The per-render
// window filter (visibleChildren in renderLane) then hides out-of-window rows from
// whatever this opened, so a structurally-small family reveals only its live
// members without a large fan-out ever auto-opening on a transient in-window dip.
export function computeAutoExpandedLanes(lanes: BaseResourceLane[]): Set<string> {
  const out = new Set<string>()
  const walk = (lane: BaseResourceLane): void => {
    const n = lane.children?.length ?? 0
    if (n > 0 && n <= AUTO_COLLAPSE_THRESHOLD) out.add(lane.id)
    for (const c of lane.children ?? []) walk(c)
  }
  for (const l of lanes) walk(l)
  return out
}

/** Every lane id in the tree (parents + all descendants), pre-order. */
export function collectLaneIdsDeep(lanes: BaseResourceLane[]): string[] {
  const out: string[] = []
  const walk = (lane: BaseResourceLane): void => {
    out.push(lane.id)
    for (const c of lane.children ?? []) walk(c)
  }
  for (const l of lanes) walk(l)
  return out
}

/**
 * Freeze each lane's auto-expand default at FIRST sighting. A live poll rebuilds the
 * whole lane tree every tick; recomputing the count-based default per tick would open
 * or close rows on their own as a family's child count drifts across
 * AUTO_COLLAPSE_THRESHOLD (a Deployment picking up a 5th pod would silently collapse).
 * Instead the structural default is computed once per lane id: a lane already recorded
 * in `prev` keeps its default; a newly-arrived lane adopts the current structural
 * default. Existing rows are never reopened/closed by arriving events. Pure (returns a
 * new map); the caller holds it in a ref across renders.
 */
export function reconcileAutoExpand(
  prev: ReadonlyMap<string, boolean>,
  lanes: BaseResourceLane[],
): Map<string, boolean> {
  const fresh = computeAutoExpandedLanes(lanes)
  const next = new Map(prev)
  for (const id of collectLaneIdsDeep(lanes)) {
    if (!next.has(id)) next.set(id, fresh.has(id))
  }
  return next
}

/**
 * Live-mode order hysteresis. In live mode the importance rank recomputes every poll
 * (recency buckets + new events shift scores), reshuffling lanes under the user's gaze.
 * This keeps lanes already on screen in their previous relative order and splices NEW
 * lanes in by their fresh rank: a new lane lands right after the surviving lane that
 * precedes it in the fresh ranking (or at the very top when none precedes it). Existing
 * lanes never move relative to each other, so the viewport doesn't jump. Pure + exported
 * for tests; the caller memoizes the previous order by lane id.
 */
export function mergeLaneOrderById(
  prevOrder: readonly string[],
  fresh: readonly string[],
): string[] {
  const prevSet = new Set(prevOrder)
  const freshSet = new Set(fresh)
  const survivors = prevOrder.filter((id) => freshSet.has(id))
  const insertAfter = new Map<string, string[]>()
  const front: string[] = []
  let lastSurvivor: string | null = null
  for (const id of fresh) {
    if (prevSet.has(id)) {
      lastSurvivor = id
      continue
    }
    if (lastSurvivor == null) front.push(id)
    else {
      const list = insertAfter.get(lastSurvivor)
      if (list) list.push(id)
      else insertAfter.set(lastSurvivor, [id])
    }
  }
  const out = [...front]
  for (const id of survivors) {
    out.push(id)
    const ins = insertAfter.get(id)
    if (ins) out.push(...ins)
  }
  return out
}

// Base resource-label column width; compact widens it (see the laneLabelPx /
// laneTrackInsetPx locals). The label column + 32px (mr-8) right gutter frame the
// event track; gap bands and the "Now" line map x into `calc(label + (100% -
// inset) * frac)`, so those per-mode locals MUST match the rendered label cells
// or the Now line and gap bands draw against a track shifted left of the real one.
const LANE_LABEL_PX = 360

// A gap band wider than this (px) has room for its "connector offline" caption.
const GAP_LABEL_MIN_PX = 80

/** A time window; shared shape with the scrubber's ScrubberRange. */
export type TimeWindow = ScrubberRange

/**
 * Clamp a view window into bounds, preserving width. A window wider than the
 * bounds collapses to the full bounds (fully zoomed out = bounds edge-to-edge).
 * This is the same invariant as the scrubber's lens clamp, reused so the two
 * can't drift.
 */
export const clampWindowToBounds = clampLensToSelection

/**
 * Step a view window to the next/previous zoom preset, keeping its END fixed,
 * then clamp into bounds. End-anchored: zooming out reaches farther back in
 * time, zooming in focuses on the most recent slice — the natural reading of
 * a timeline whose right edge is "now". (Center anchoring made both directions
 * eat into the recent edge, which read as the view drifting.)
 */
export function zoomWindowWithinBounds(
  win: TimeWindow,
  dir: 'in' | 'out',
  bounds?: TimeWindow,
): TimeWindow {
  const widthHours = (win.toMs - win.fromMs) / HOUR_MS
  const idx = ZOOM_LEVELS.findIndex((l) => l >= widthHours)
  const cur = idx === -1 ? ZOOM_LEVELS.length - 1 : idx
  const nextIdx = dir === 'in'
    ? Math.max(0, cur - 1)
    : Math.min(ZOOM_LEVELS.length - 1, cur + 1)
  const nextWidthMs = ZOOM_LEVELS[nextIdx] * HOUR_MS
  const next = { fromMs: win.toMs - nextWidthMs, toMs: win.toMs }
  return bounds ? clampWindowToBounds(next, bounds) : next
}

/**
 * Continuous analog of {@link zoomWindowWithinBounds} for smooth wheel/pinch
 * zoom: scale the window WIDTH by `factor` (>1 widens = zoom out, <1 narrows =
 * zoom in) instead of snapping to the preset ladder. End-anchored and
 * bounds-clamped exactly like the stepped version, so the wheel and the zoom
 * buttons land the view in the same place — they differ only in granularity.
 */
export function zoomWindowContinuous(
  win: TimeWindow,
  factor: number,
  bounds?: TimeWindow,
): TimeWindow {
  const curWidth = win.toMs - win.fromMs
  const maxWidth = bounds ? bounds.toMs - bounds.fromMs : Number.POSITIVE_INFINITY
  const nextWidth = Math.max(MIN_WINDOW_MS, Math.min(curWidth * factor, maxWidth))
  const next = { fromMs: win.toMs - nextWidth, toMs: win.toMs }
  return bounds ? clampWindowToBounds(next, bounds) : next
}

// Map a wheel delta to a continuous zoom factor. Normalizing the delta mode and
// clamping the per-event delta makes one mouse notch a consistent step across
// devices, while a trackpad pinch (many tiny ctrl-wheel events) accumulates
// smoothly. >1 zooms out (wider window), <1 zooms in. Pure + exported for tests.
const WHEEL_ZOOM_SENSITIVITY = 0.0025
export function wheelZoomFactor(deltaY: number, deltaMode = 0): number {
  let dy = deltaY
  if (deltaMode === 1) dy *= 16 // DOM_DELTA_LINE → ~16px/line
  else if (deltaMode === 2) dy *= 400 // DOM_DELTA_PAGE → ~one viewport
  dy = Math.max(-100, Math.min(100, dy))
  return Math.exp(dy * WHEEL_ZOOM_SENSITIVITY)
}

/**
 * True when the entire view window sits inside a recording gap — the swimlane is
 * empty because nothing was recorded, not because the period was quiet. Pure +
 * exported for tests.
 */
export function windowInsideGap(
  win: TimeWindow | undefined,
  gaps: TimeWindow[] | undefined,
): boolean {
  if (!win || !gaps || gaps.length === 0) return false
  return gaps.some((g) => g.fromMs <= win.fromMs && g.toMs >= win.toMs)
}

// ---------------------------------------------------------------------------
// Event-marker shape coding (created ▲ / modified ● / deleted ▼ / warning ◆ /
// historical ○). Colors live in text-* classes so the glyph fills via
// currentColor and dark: variants keep contrast.
// ---------------------------------------------------------------------------
type MarkerShape = 'triangle-up' | 'triangle-down' | 'diamond' | 'circle' | 'ring'

function eventShape(event: TimelineEvent): MarkerShape {
  if (isHistoricalEvent(event)) return 'ring'
  if (isProblematicEvent(event)) return 'diamond'
  if (isChangeEvent(event)) {
    switch (event.eventType) {
      case 'add': return 'triangle-up'
      case 'delete': return 'triangle-down'
      case 'update': return 'circle'
    }
  }
  return 'circle'
}

function eventColorClass(event: TimelineEvent): string {
  if (isProblematicEvent(event)) return 'text-amber-500 dark:text-amber-400'
  if (isHistoricalEvent(event)) return 'text-theme-text-tertiary'
  // ONE activity hue (color budget): every change/create/delete/
  // informational dot is info-blue — the SHAPE carries the event class
  // (▲ created / ● modified / ▼ deleted), hue is reserved for status.
  return 'text-blue-600 dark:text-blue-400'
}

// Markers are centered on their time — but a live event's time sits AT the Now
// line, so a centered glyph/pill crosses it (and can clip the track edge). Near
// the edge the anchor flips from center to right so the marker's right edge
// kisses the Now line instead — dots respect the Now line.
// 97.5% ≈ 32px on a ~1300px track — enough clearance for the widest ×N pill
// (a 99% threshold still let pill halves poke ~4px past the Now line).
const NOW_EDGE_ANCHOR_PCT = 97.5
function markerAnchor(x: number): { left: string; anchorClass: string } {
  const clamped = Math.min(x, 100)
  return clamped >= NOW_EDGE_ANCHOR_PCT
    ? { left: '100%', anchorClass: '-translate-x-full' }
    : { left: `${clamped}%`, anchorClass: '-translate-x-1/2' }
}

/** A pure SVG-free glyph; `size` is the height in px (triangles are ~1.18× wide). */
function MarkerGlyph({ shape, size, className }: { shape: MarkerShape; size: number; className?: string }) {
  const half = size / 1.7
  if (shape === 'triangle-up') {
    return <span className={className} style={{ display: 'block', width: 0, height: 0, borderLeft: `${half}px solid transparent`, borderRight: `${half}px solid transparent`, borderBottom: `${size}px solid currentColor` }} />
  }
  if (shape === 'triangle-down') {
    return <span className={className} style={{ display: 'block', width: 0, height: 0, borderLeft: `${half}px solid transparent`, borderRight: `${half}px solid transparent`, borderTop: `${size}px solid currentColor` }} />
  }
  if (shape === 'diamond') {
    const d = size * 0.8
    return <span className={clsx(className, 'rounded-[2px]')} style={{ display: 'block', width: d, height: d, background: 'currentColor', transform: 'rotate(45deg)' }} />
  }
  if (shape === 'ring') {
    return <span className={clsx(className, 'rounded-full')} style={{ display: 'block', width: size, height: size, border: '2px solid currentColor' }} />
  }
  return <span className={clsx(className, 'rounded-full')} style={{ display: 'block', width: size, height: size, background: 'currentColor' }} />
}

// ---------------------------------------------------------------------------
// Cluster near-overlapping markers into a single "×N" pill. Pure + exported so
// the collapse logic is contract-testable independently of layout.
// ---------------------------------------------------------------------------

/** Gap (in x-percent of the track) below which two markers are treated as overlapping. */
export const CLUSTER_MIN_GAP_PCT = 1.4

export interface PositionedTimelineEvent {
  event: TimelineEvent
  /** Horizontal position as a percentage (0-100) of the track width. */
  x: number
}

export interface TimelineEventCluster {
  /** Average x-percent of the cluster's members. */
  x: number
  events: TimelineEvent[]
  /** Highest-severity member — drives the pill glyph and the click target. */
  dominant: TimelineEvent
  count: number
}

/** Severity used to pick a cluster's dominant marker (higher wins; ties keep the earlier event). */
export function eventSeverityRank(event: TimelineEvent): number {
  if (isProblematicEvent(event)) return 3
  if (isChangeEvent(event)) {
    switch (event.eventType) {
      case 'delete': return 2
      case 'add': return 1
      case 'update': return 0
    }
  }
  return 0
}

/** A cluster may span at most this multiple of the min gap. Chaining alone
 *  (each neighbor within minGap) lets a dense stream collapse into one pill
 *  covering a long stretch of the track — a pill claiming events "at this
 *  position" while its members sit far apart in time. The cap breaks the
 *  chain, so a dense run renders as several pills at distinct positions. */
export const CLUSTER_MAX_SPAN_FACTOR = 2

/**
 * Collapse positioned events whose x-positions are within `minGap` of the
 * previous one into clusters, capped so one cluster never spans more than
 * CLUSTER_MAX_SPAN_FACTOR × minGap of the track. Input order is irrelevant
 * (sorted internally); a single event passes through as a count-1 cluster.
 */
export function clusterEventsByPosition(
  positioned: PositionedTimelineEvent[],
  minGap: number,
): TimelineEventCluster[] {
  const sorted = [...positioned].sort((a, b) => a.x - b.x)
  const clusters: TimelineEventCluster[] = []
  const maxSpan = minGap * CLUSTER_MAX_SPAN_FACTOR
  let cur: { events: TimelineEvent[]; dominant: TimelineEvent; count: number; sum: number; startX: number; lastX: number } | null = null
  const flush = () => {
    if (cur) clusters.push({ x: cur.sum / cur.count, events: cur.events, dominant: cur.dominant, count: cur.count })
  }
  for (const { event, x } of sorted) {
    if (cur && x - cur.lastX < minGap && x - cur.startX <= maxSpan) {
      cur.events.push(event)
      cur.count++
      cur.sum += x
      cur.lastX = x
      if (eventSeverityRank(event) > eventSeverityRank(cur.dominant)) cur.dominant = event
    } else {
      flush()
      cur = { events: [event], dominant: event, count: 1, sum: x, startX: x, lastX: x }
    }
  }
  flush()
  return clusters
}

/** One line of a cluster pill's hover breakdown: a shared label with a count. */
export interface ClusterBreakdownLine {
  label: string
  count: number
}

function breakdownLabel(event: TimelineEvent): string {
  if (isProblematicEvent(event)) return event.reason || 'Warning'
  if (isChangeEvent(event)) {
    if (event.eventType === 'add') return 'Created'
    if (event.eventType === 'delete') return 'Deleted'
    if (event.eventType === 'update') return 'Modified'
    return 'Changed'
  }
  return event.reason || 'Event'
}

/**
 * Group a cluster's members into "count× label" lines for the pill tooltip,
 * ordered by severity (warnings first), then count, then label. Labels reuse
 * the single-marker vocabulary: the K8s reason for warnings, the operation
 * for change events. Caps at `maxLines`; the remainder's event total is
 * reported as `more`.
 */
export function clusterBreakdown(
  events: TimelineEvent[],
  maxLines = 5,
): { lines: ClusterBreakdownLine[]; more: number } {
  const groups = new Map<string, { count: number; rank: number }>()
  for (const event of events) {
    const label = breakdownLabel(event)
    const rank = eventSeverityRank(event)
    const group = groups.get(label)
    if (group) {
      group.count++
      if (rank > group.rank) group.rank = rank
    } else {
      groups.set(label, { count: 1, rank })
    }
  }
  const ordered = [...groups.entries()]
    .map(([label, group]) => ({ label, count: group.count, rank: group.rank }))
    .sort((a, b) => b.rank - a.rank || b.count - a.count || a.label.localeCompare(b.label))
  const lines = ordered.slice(0, maxLines).map(({ label, count }) => ({ label, count }))
  const more = ordered.slice(maxLines).reduce((sum, group) => sum + group.count, 0)
  return { lines, more }
}

/** The drawer state a cluster opens into: a single-event cluster opens the
 *  one-event panel; a multi-event cluster opens in cluster mode with members
 *  ordered most-severe-first. `preferId` (URL restore) selects that member when
 *  it's present; otherwise the dominant (top of the severity order) is selected —
 *  the glyph the pill already promised. Shared by the click handler and the
 *  restore resolver so their ordering can't drift. */
export function clusterDrawerState(
  cluster: TimelineEventCluster,
  preferId?: string,
): { events: TimelineEvent[]; selectedId: string } {
  if (cluster.count === 1) {
    const ev = cluster.dominant
    return { events: [ev], selectedId: ev.id }
  }
  const ordered = [...cluster.events].sort((a, b) => eventSeverityRank(b) - eventSeverityRank(a))
  const selectedId = preferId && ordered.some((e) => e.id === preferId) ? preferId : ordered[0].id
  return { events: ordered, selectedId }
}

/** Resolve a persisted event id back to the drawer state it should open into,
 *  reproducing exactly what the current render shows. Walks the rendered rows
 *  (pinned rows first, then the visible lanes) in render order, honoring live
 *  expansion via `laneTrackEvents`, and runs the SAME position clustering the
 *  markers use on the containing row's track. Returns null when the id is on no
 *  rendered row's track (pruned, filtered out, or scrolled beyond the window) —
 *  the caller strips the stale param. Same window/filters → same clustering, so
 *  restoration is deterministic. Pure + exported for tests. */
export function resolveEventCluster(
  rows: BaseResourceLane[],
  expandedLanes: ReadonlySet<string>,
  id: string,
  timeToX: (timestampMs: number) => number,
): { events: TimelineEvent[]; selectedId: string } | null {
  const visit = (lane: BaseResourceLane): { events: TimelineEvent[]; selectedId: string } | null => {
    const expanded = expandedLanes.has(lane.id)
    const track = laneTrackEvents(lane, expanded)
    if (track.some((e) => e.id === id)) {
      const positioned = track
        .map((event) => ({ event, x: timeToX(new Date(event.timestamp).getTime()) }))
        .filter(({ x }) => x >= 0 && x <= 100)
      const cluster = clusterEventsByPosition(positioned, CLUSTER_MIN_GAP_PCT)
        .find((c) => c.events.some((e) => e.id === id))
      if (cluster) return clusterDrawerState(cluster, id)
    }
    if (expanded && lane.children) {
      for (const child of lane.children) {
        const found = visit(child)
        if (found) return found
      }
    }
    return null
  }
  for (const lane of rows) {
    const found = visit(lane)
    if (found) return found
  }
  return null
}

/** URL ↔ drawer reconciliation guard. A `selectedEventId` (mount deep-link OR a
 *  post-mount back/forward nav) must win over the "sync the drawer's id back to
 *  the URL" effect until its restore attempt settles — otherwise a transient
 *  closed-drawer null strips the param before the (possibly still-loading) rows
 *  can resolve it. Pending stays true only while the drawer is CLOSED and the id
 *  hasn't been resolved-or-ruled-out yet (`settledId`); an OPEN drawer is
 *  authoritative, so the report effect is free to sync the URL to it. A
 *  `dismissedId` (the id the user just closed) is NOT pending: the close is
 *  authoritative, so the report effect must be free to strip the param even
 *  before the URL has caught up — otherwise the still-present ?event=<id> echoes
 *  back as a fresh restore and reopens the drawer. Pure + exported for tests. */
export function restoreIsPending(
  selectedEventId: string | null,
  drawerSelectedId: string | null,
  settledId: string | null,
  dismissedId: string | null = null,
): boolean {
  if (selectedEventId == null) return false
  if (drawerSelectedId != null) return false
  if (selectedEventId === settledId) return false
  if (selectedEventId === dismissedId) return false
  return true
}

export interface TimelineSwimlanesProps {
  events: TimelineEvent[]
  isLoading?: boolean
  onResourceClick?: NavigateToResource
  viewMode?: 'list' | 'swimlane'
  onViewModeChange?: (mode: 'list' | 'swimlane') => void
  topology?: Topology
  namespaces?: string[]
  // RBAC capability flag (was a radar/web context); host passes it. Default false.
  hasLimitedAccess?: boolean
  // GitOps lane labels deep-link to a controller path; the host decides how to
  // navigate (radar router push, or cross-route-tree href). When omitted, GitOps lanes
  // fall back to onResourceClick (the resource drawer).
  onNavigatePath?: (path: string) => void
  // Controlled "show deleted" toggle. When omitted the component manages it
  // internally; the host passes it to share one toggle with the list view and
  // to drive server-side delete filtering on the underlying fetch.
  showDeleted?: boolean
  onShowDeletedChange?: (showDeleted: boolean) => void
  // Controlled pinned-only filter (falls back to internal state when absent) —
  // lifted so the host can URL-persist it with the rest of the controls.
  pinnedOnly?: boolean
  onPinnedOnlyChange?: (pinnedOnly: boolean) => void
  // Controlled-window adapter (the LENS). When `viewWindow` is set the component
  // renders exactly that window instead of its internal zoom/pan-derived one, and
  // pan / zoom / "→ Now" emit the would-be window via `onViewWindowChange` rather
  // than mutating internal state. `bounds` clamps the window (pan can't exit it;
  // zoom-out caps at its width). When a bound edge is reached, an "extend" hint
  // fires `onExtendRequest`. All optional — absent props leave the uncontrolled
  // path (OSS/local mode, WorkloadView) byte-identical.
  viewWindow?: TimeWindow
  onViewWindowChange?: (w: TimeWindow) => void
  bounds?: TimeWindow
  onExtendRequest?: (dir: 'past' | 'future') => void
  // Host-supplied clock for the "Now" line and health-span right edges. The
  // retained host passes its live tick so both advance while watching (and
  // freeze with the window in paused mode); without it they'd pin to mount
  // time and new events would render PAST the stale Now line. Local mode
  // omits it and keeps the mount-stable clock.
  nowMs?: number
  // Recording gaps (connector offline) as absolute time ranges. Rendered as
  // vertical hatched bands behind the lanes; also drives the empty-state copy
  // when the whole view window sits inside a gap. Retained-mode only — local
  // mode omits it and behavior is unchanged.
  gaps?: TimeWindow[]
  // Controlled shared-filter state. When omitted each is managed internally; the
  // host passes them so search / activity-type / kind survive the view switch
  // and match the list view exactly. Absent props = current standalone behavior.
  search?: string
  onSearchChange?: (value: string) => void
  activityFilter?: ActivityFilterKey[]
  onActivityFilterChange?: (keys: ActivityFilterKey[]) => void
  kindFilter?: string[]
  onKindFilterChange?: (kinds: string[]) => void
  // Server app-membership index (from GET /api/applications). When present and
  // grouping='app', lanes are grouped into app header lanes via the membership
  // cascade. Absent → the legacy label grouping (owner fallback) is used, no crash.
  appIndex?: AppMembershipIndex
  // Controlled grouping mode. When omitted managed internally (default 'app').
  // The host lifts it so it survives the view switch like the other view options.
  grouping?: TimelineGrouping
  onGroupingChange?: (grouping: TimelineGrouping) => void
  // Controlled lane sort. Lifted alongside grouping so it survives the view
  // switch; managed internally (default 'importance') when omitted.
  sort?: TimelineSort
  onSortChange?: (sort: TimelineSort) => void
  // Pinned resource lanes — a stationary section rendered ABOVE all other lanes,
  // in pin order, bypassing the visible-window filter and every grouping mode.
  // Pure: this component owns no storage. The host holds the list (and persists
  // it) and toggles via onTogglePin. Absent → no pin affordances render (the
  // uncontrolled/WorkloadView path is unchanged). A pinned resource with no
  // events in the loaded selection still renders as an empty track.
  pinnedLanes?: PinnedLaneRef[]
  onTogglePin?: (ref: PinnedLaneRef) => void
  // App-group name click → host navigates to the Applications page (?app=<key>).
  onAppClick?: (appKey: string) => void
  // Observable drawer selection, for URL routing. When the host wires these,
  // every drawer open/select/close (cluster row switches included) reports the
  // SELECTED event id upward (close → null); and a non-null `selectedEventId` on
  // a closed drawer is restored once lanes are built — resolved against the
  // current render (same window/filters → same clustering → deterministic) via
  // resolveEventCluster. An id absent after data settles reports null once (the
  // host strips the stale param). Absent → the drawer stays fully internal and
  // behavior is byte-identical.
  selectedEventId?: string | null
  onSelectedEventChange?: (id: string | null) => void
  // True while the retained view is in LIVE mode (window latched to now, polling
  // every tick). Enables order hysteresis: existing lanes keep their relative
  // order across polls so arriving data doesn't reshuffle the list under the user.
  // A user-initiated re-rank (sort / grouping / filter change, or leaving live)
  // adopts the fresh importance rank. Absent/false → fresh rank every render
  // (local mode / WorkloadView unchanged).
  isLive?: boolean
  // Strip the power-user toolbar (search / activity + kind filters / deleted /
  // view menu / counts / legend) for an embedded, single-subject swimlane where
  // those controls are overkill — e.g. the workload detail's Timeline tab. The
  // lanes, axis, Now line, and clustering are unchanged. Default false.
  compact?: boolean
}

interface ResourceLane extends BaseResourceLane {
  scoreBreakdown?: ScoreBreakdown // Debug: interestingness score breakdown
}

// Score breakdown for debugging
interface ScoreBreakdown {
  total: number
  kind: number
  problematic: number
  variety: number
  addDelete: number
  children: number
  empty: number
  systemNs: number
  recent5m: number
  recent30m: number
  noisy: number
  details: string
}

// Calculate "interestingness" score for sorting lanes
// Higher score = more interesting = should appear higher in list
function calculateInterestingness(lane: ResourceLane): number {
  return calculateInterestingnessWithBreakdown(lane).total
}

function calculateInterestingnessWithBreakdown(lane: ResourceLane): ScoreBreakdown {
  const allEvents = [...lane.events, ...(lane.children?.flatMap(c => c.events) || [])]
  const breakdown: ScoreBreakdown = {
    total: 0, kind: 0, problematic: 0, variety: 0, addDelete: 0,
    children: 0, empty: 0, systemNs: 0, recent5m: 0, recent30m: 0, noisy: 0, details: ''
  }

  // 1. Base: Kind priority (tiebreaker, lower values than before)
  const kindScores: Record<string, number> = {
    // GitOps controllers - top priority
    Application: 55, // ArgoCD Application
    Kustomization: 55, HelmRelease: 55, // FluxCD controllers
    GitRepository: 52, OCIRepository: 52, HelmRepository: 52, // FluxCD sources
    // Core workloads
    Deployment: 50, Rollout: 50, StatefulSet: 50, DaemonSet: 50,
    Service: 45, Ingress: 45, Gateway: 45,
    HTTPRoute: 42, GRPCRoute: 42, TCPRoute: 42, TLSRoute: 42,
    Job: 40, CronJob: 40, Workflow: 40, CronWorkflow: 40,
    Pod: 30,
    HorizontalPodAutoscaler: 25,
    ReplicaSet: 20,
    ConfigMap: 10, Secret: 10, PersistentVolumeClaim: 10,
  }
  breakdown.kind = kindScores[lane.kind] || 15

  // 2. Primary: Recency (dominates) - events in last 5 minutes
  const now = Date.now()
  const fiveMinutesAgo = now - 5 * 60 * 1000
  const thirtyMinutesAgo = now - 30 * 60 * 1000

  const eventsLast5m = allEvents.filter(e => new Date(e.timestamp).getTime() > fiveMinutesAgo)
  const eventsLast30m = allEvents.filter(e => {
    const t = new Date(e.timestamp).getTime()
    return t > thirtyMinutesAgo && t <= fiveMinutesAgo
  })

  breakdown.recent5m = Math.min(eventsLast5m.length * 30, 150)
  breakdown.recent30m = Math.min(eventsLast30m.length * 10, 50)

  // 3. Secondary: Problems (important signal) - +40 each, max 200
  const problematicCount = allEvents.filter(e => isProblematicEvent(e)).length
  breakdown.problematic = Math.min(problematicCount * 40, 200)

  // 4. Tertiary: Activity type
  const operations = new Set(allEvents.map(e => e.eventType).filter(t => isOperation(t)))
  breakdown.variety = operations.size * 10 // Up to 30 for all three types

  // Add/delete with caps
  const addCount = allEvents.filter(e => e.eventType === 'add').length
  const deleteCount = allEvents.filter(e => e.eventType === 'delete').length
  breakdown.addDelete = Math.min(addCount * 3, 30) + Math.min(deleteCount * 5, 30)

  // 5. Children bonus (flat, just organizational)
  if (lane.children && lane.children.length > 0) {
    breakdown.children = 10
  }

  // 6. Empty lane penalty (parent with 0 own events)
  if (lane.events.length === 0) {
    breakdown.empty = -30
  }

  // 7. System namespaces penalty
  const systemNamespaces = ['kube-system', 'kube-public', 'kube-node-lease', 'gke-managed-system']
  if (systemNamespaces.includes(lane.namespace)) {
    breakdown.systemNs = -30
  }

  // 8. Noisy penalty (many updates with no variety)
  const updateCount = allEvents.filter(e => e.eventType === 'update').length
  if (updateCount > 10 && operations.size === 1) {
    breakdown.noisy = -Math.min(updateCount, 40)
  }

  breakdown.total = breakdown.kind + breakdown.problematic + breakdown.variety +
    breakdown.addDelete + breakdown.children + breakdown.empty + breakdown.systemNs +
    breakdown.recent5m + breakdown.recent30m + breakdown.noisy

  // Build details string
  const parts: string[] = []
  parts.push(`kind:${breakdown.kind}`)
  if (breakdown.recent5m) parts.push(`5m:${breakdown.recent5m}`)
  if (breakdown.recent30m) parts.push(`30m:${breakdown.recent30m}`)
  if (breakdown.problematic) parts.push(`warn:${breakdown.problematic}`)
  if (breakdown.variety) parts.push(`var:${breakdown.variety}`)
  if (breakdown.addDelete) parts.push(`a/d:${breakdown.addDelete}`)
  if (breakdown.children) parts.push(`child:${breakdown.children}`)
  if (breakdown.empty) parts.push(`empty:${breakdown.empty}`)
  if (breakdown.systemNs) parts.push(`sys:${breakdown.systemNs}`)
  if (breakdown.noisy) parts.push(`noisy:${breakdown.noisy}`)
  breakdown.details = parts.join(' ')

  return breakdown
}

export function TimelineSwimlanes({ events, isLoading, onResourceClick, viewMode, onViewModeChange, topology, namespaces, hasLimitedAccess = false, onNavigatePath, showDeleted: showDeletedProp, onShowDeletedChange, pinnedOnly: pinnedOnlyProp, onPinnedOnlyChange, viewWindow, onViewWindowChange, bounds, onExtendRequest, nowMs, gaps, onAppClick, search: searchProp, onSearchChange, activityFilter: activityFilterProp, onActivityFilterChange, kindFilter: kindFilterProp, onKindFilterChange, appIndex, grouping: groupingProp, onGroupingChange, sort: sortProp, onSortChange, pinnedLanes, onTogglePin, selectedEventId, onSelectedEventChange, isLive, compact = false }: TimelineSwimlanesProps) {
  // Controlled when the host drives the visible window (retained-mode lens).
  const controlled = viewWindow != null
  // Compact gives the label column extra width — no namespace subtitle or nested
  // tree competing for it, and resource names (esp. hashed Pod/RS names) are long.
  // The overlay geometry (Now line, gap bands) must track the same width or drift.
  const laneLabelPx = compact ? 440 : LANE_LABEL_PX
  const laneTrackInsetPx = laneLabelPx + 32
  const laneLabelWidthClass = compact ? 'w-[440px]' : 'w-[360px]'
  // Timeline lane labels for GitOps CRs (Application/Kustomization/HelmRelease)
  // deep-link to GitOps detail rather than the resource drawer — the lane is
  // already telling the user "this controller had changes/events"; the GitOps
  // tab is the right place to investigate further.
  const handleLaneOpen = useCallback((kind: string, namespace: string, name: string, group?: string) => {
    const gitOpsPath = gitOpsRouteForKind(kind, namespace, name)
    if (gitOpsPath && onNavigatePath) {
      onNavigatePath(gitOpsPath)
      return
    }
    onResourceClick?.({ kind: kindToPlural(kind), namespace, name, group })
  }, [onNavigatePath, onResourceClick])
  const containerRef = useRef<HTMLDivElement>(null)
  const [zoom, setZoom] = useState(1)
  const [panOffset, setPanOffset] = useState(0)
  // The detail drawer. A single marker opens a one-event drawer (today's exact
  // panel); a cluster pill opens the SAME drawer carrying all N members so none
  // is unreachable. `selectedId` is the member whose detail is shown.
  const [drawer, setDrawer] = useState<{ events: TimelineEvent[]; selectedId: string } | null>(null)
  // The id the user just closed, so the restore effect doesn't reopen it from a
  // not-yet-stripped ?event= echo. Set on any user close (X / Escape / re-click),
  // cleared when the selection settles to null or a new drawer opens.
  const dismissedIdRef = useRef<string | null>(null)
  // The member currently detailed — drives the marker/pill highlight so the pill
  // whose cluster is open reads as selected.
  const selectedEvent = useMemo(
    () => (drawer ? drawer.events.find((e) => e.id === drawer.selectedId) ?? null : null),
    [drawer],
  )
  const [isDragging, setIsDragging] = useState(false)
  // `offset` drives the uncontrolled pan; `windowFrom/To` capture the window at
  // grab time for the controlled path (shifting an absolute window, not panOffset).
  const [dragStart, setDragStart] = useState({ x: 0, offset: 0, windowFrom: 0, windowTo: 0 })
  const [searchInternal, setSearchInternal] = useState('')
  const searchTerm = searchProp ?? searchInternal
  const setSearchTerm = onSearchChange ?? setSearchInternal
  const [activityFilterInternal, setActivityFilterInternal] = useState<ActivityFilterKey[]>([])
  const activityFilter = activityFilterProp ?? activityFilterInternal
  const setActivityFilter = onActivityFilterChange ?? setActivityFilterInternal
  const [kindFilterInternal, setKindFilterInternal] = useState<string[]>([])
  const kindFilter = kindFilterProp ?? kindFilterInternal
  const setKindFilter = onKindFilterChange ?? setKindFilterInternal
  // The user's explicit expand/collapse choices, per lane id. Overrides the
  // count-based auto default below — once the user touches a lane we never fight
  // them. Empty on first render, so the initial view is fully deterministic.
  const [userLaneOverrides, setUserLaneOverrides] = useState<Map<string, boolean>>(new Map())
  const [hasAutoZoomed, setHasAutoZoomed] = useState(false)
  // Legend is on-demand: the marker/health key is hidden until the user asks
  // for it via the toolbar's Legend button, rather than always occupying a row.
  const [showLegend, setShowLegend] = useState(false)
  // Grouping mode: 'app' (membership cascade) | 'owner' (owner/topology only) |
  // 'flat' (no parenting). Controlled by the host when provided, else internal.
  const [groupingInternal, setGroupingInternal] = useState<TimelineGrouping>('app')
  const grouping = groupingProp ?? groupingInternal
  const setGrouping = onGroupingChange ?? setGroupingInternal

  const [sortInternal, setSortInternal] = useState<TimelineSort>(TIMELINE_SORT_DEFAULT)
  const sort = sortProp ?? sortInternal
  const setSort = onSortChange ?? setSortInternal
  const [showDeletedInternal, setShowDeletedInternal] = useState(true)
  // Pinned-only filter: session-local; auto-inert when the last pin is removed
  // (the toggle disappears and effectivePinnedOnly falls back to false).
  const [pinnedOnlyInternal, setPinnedOnlyInternal] = useState(false)
  const pinnedOnly = pinnedOnlyProp ?? pinnedOnlyInternal
  const setPinnedOnly = onPinnedOnlyChange ?? setPinnedOnlyInternal
  const showDeleted = showDeletedProp ?? showDeletedInternal
  const setShowDeleted = onShowDeletedChange ?? setShowDeletedInternal

  // Clear every content filter at once (search + activity + kind + show-deleted).
  // Each setter already resolves to the controlled callback or the internal
  // state setter, so this works in both host-driven and standalone modes.
  const clearAllFilters = useCallback(() => {
    setSearchTerm('')
    setActivityFilter([])
    setKindFilter([])
    setShowDeleted(true)
  }, [setSearchTerm, setActivityFilter, setKindFilter, setShowDeleted])


  // Stable "now" time - captured once on mount, only changes when user interacts
  // This prevents the time window from auto-shifting and causing re-renders
  const [stableNow] = useState(() => Date.now())
  // Host clock wins when provided (retained live tick); local mode stays mount-stable.
  const effectiveNow = nowMs ?? stableNow

  // Track pixel width (container minus the label column + gutter) so gap bands
  // can decide whether they're wide enough to caption.
  const [trackPx, setTrackPx] = useState(0)
  useEffect(() => {
    const node = containerRef.current
    if (!node) return
    const measure = () => setTrackPx(Math.max(0, node.clientWidth - laneTrackInsetPx))
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(node)
    return () => ro.disconnect()
  }, [laneTrackInsetPx])

  // Auto-adjust zoom based on event distribution (only once on initial load).
  // Skipped when controlled — the host owns the window, so internal zoom is inert.
  useEffect(() => {
    if (hasAutoZoomed || events.length === 0 || controlled) return

    const now = Date.now()
    const timestamps = events.map(e => new Date(e.timestamp).getTime())
    const oldestEvent = Math.min(...timestamps)
    const eventAge = now - oldestEvent

    // Zoom levels: 0.25 (15m), 0.5 (30m), 1 (1h), 2 (2h), etc.
    // Pick the smallest zoom that fits all events with some margin
    let optimalZoom = 1
    if (eventAge < 10 * 60 * 1000) { // < 10 minutes
      optimalZoom = 0.25 // 15m window
    } else if (eventAge < 20 * 60 * 1000) { // < 20 minutes
      optimalZoom = 0.5 // 30m window
    } else if (eventAge < 45 * 60 * 1000) { // < 45 minutes
      optimalZoom = 1 // 1h window
    } else if (eventAge < 90 * 60 * 1000) { // < 90 minutes
      optimalZoom = 2 // 2h window
    }
    // else keep default 1h

    setZoom(optimalZoom)
    setHasAutoZoomed(true)
  }, [events, hasAutoZoomed, controlled])

  // Keyboard shortcuts
  useRegisterShortcut({
    id: 'swimlane-escape',
    keys: 'Escape',
    description: 'Close event detail',
    category: 'Timeline',
    scope: 'timeline',
    handler: () => {
      if (drawer) closeDrawer()
    },
  })

  // ↑/↓ step through a cluster drawer's members (inert for a single-event drawer).
  const moveClusterSelection = useCallback((dir: 1 | -1) => {
    setDrawer((prev) => {
      if (!prev || prev.events.length < 2) return prev
      const idx = prev.events.findIndex((e) => e.id === prev.selectedId)
      const next = (idx + dir + prev.events.length) % prev.events.length
      return { ...prev, selectedId: prev.events[next].id }
    })
  }, [])
  const clusterOpen = !!drawer && drawer.events.length > 1
  useRegisterShortcut({
    id: 'swimlane-cluster-prev',
    keys: 'ArrowUp',
    description: 'Previous clustered event',
    category: 'Timeline',
    scope: 'timeline',
    handler: (e) => {
      if (!clusterOpen) return
      e.preventDefault()
      moveClusterSelection(-1)
    },
  })
  useRegisterShortcut({
    id: 'swimlane-cluster-next',
    keys: 'ArrowDown',
    description: 'Next clustered event',
    category: 'Timeline',
    scope: 'timeline',
    handler: (e) => {
      if (!clusterOpen) return
      e.preventDefault()
      moveClusterSelection(1)
    },
  })

  // Apply the shared filters (deleted, activity-type, kind, search) through the
  // same predicates the list uses, so the two views can't drift.
  const filteredEvents = useMemo(() => {
    return events.filter((e) => {
      if (!showDeleted && e.eventType === 'delete') return false
      if (!matchesActivityFilter(e, activityFilter)) return false
      if (kindFilter.length > 0 && !kindFilter.includes(e.kind)) return false
      if (!matchesTimelineSearch(e, searchTerm)) return false
      return true
    })
  }, [events, searchTerm, showDeleted, activityFilter, kindFilter])

  // Kind dropdown options: seed set + every kind present in the (unfiltered)
  // events. The swimlane fetches all kinds, so deriving from events is stable.
  const kindOptions = useMemo(() => mergeKindOptions(events.map((e) => e.kind)), [events])

  // Build hierarchical lanes using owner references + topology edges
  // Uses the shared utility from utils/resource-hierarchy.ts
  const lanes = useMemo(() => {
    // Build the hierarchy using the shared utility
    const baseLanes = buildResourceHierarchy({
      events: filteredEvents,
      topology,
      grouping,
      appIndex,
    })

    // Add score breakdown to each lane (specific to swimlanes view). An app-group
    // header carries no own events, so its score is the MAX of its members —
    // otherwise the empty-lane penalty would sink every app to the bottom. The
    // top-level ordering is applied later by `orderedLanes` (sort-mode aware).
    const lanesWithScores: ResourceLane[] = baseLanes.map(lane => {
      if (lane.isAppGroup) {
        const memberScores = (lane.children ?? []).map(calculateInterestingness)
        return { ...lane, scoreBreakdown: { ...calculateInterestingnessWithBreakdown(lane), total: memberScores.length ? Math.max(...memberScores) : 0 } }
      }
      return { ...lane, scoreBreakdown: calculateInterestingnessWithBreakdown(lane) }
    })

    return lanesWithScores
  }, [filteredEvents, topology, grouping, appIndex])

  // Same-kind cross-group collisions among the visible lanes (CAPI vs CNPG
  // `Cluster`). Only lanes in this set show a disambiguating group chip — the API
  // group is otherwise invisible. Pure + memoized on the built lane tree.
  const collidingKeys = useMemo(() => collidingLaneKeys(lanes), [lanes])

  // Auto-expand default frozen per lane id at first sighting (see
  // reconcileAutoExpand) so a live poll's rebuilt tree can't reopen/close rows as
  // child counts drift. Held in a ref across renders; new lanes adopt their
  // structural default, existing lanes keep theirs.
  const autoExpandRef = useRef<Map<string, boolean>>(new Map())

  // Effective expansion = frozen auto default, then the user's per-lane overrides
  // layered on top. `lanes` holds every id (nested), and pinned rows are extracted
  // from it, so this one set covers both sections.
  const expandedLanes = useMemo(() => {
    const frozen = reconcileAutoExpand(autoExpandRef.current, lanes)
    autoExpandRef.current = frozen
    const eff = new Set<string>()
    for (const [id, auto] of frozen) if (auto) eff.add(id)
    for (const [id, expanded] of userLaneOverrides) {
      if (expanded) eff.add(id)
      else eff.delete(id)
    }
    return eff
  }, [lanes, userLaneOverrides])

  // Toggle lane expansion — pins the flipped state as a user override so it wins
  // over the auto default from here on.
  const toggleLane = useCallback((laneId: string) => {
    const isExpanded = expandedLanes.has(laneId)
    setUserLaneOverrides(prev => {
      const next = new Map(prev)
      next.set(laneId, !isExpanded)
      return next
    })
  }, [expandedLanes])

  // Bulk expand/collapse (RESOURCE header toggle): pin an override for every
  // lane that has children, so the whole tree opens/closes regardless of the
  // frozen auto defaults. REPLACES prior per-row overrides — "expand all" means
  // all, not "all except the two rows I once closed".
  const setAllExpanded = useCallback((expanded: boolean) => {
    const next = new Map<string, boolean>()
    const walk = (ls: ResourceLane[]) => {
      for (const l of ls) {
        if (l.children?.length) {
          next.set(l.id, expanded)
          walk(l.children as ResourceLane[])
        }
      }
    }
    walk(lanes)
    setUserLaneOverrides(next)
  }, [lanes])

  // One morphing toggle: when at least half the groups are open the
  // button offers collapse, otherwise expand — one control, state-aware.
  const expandableIds = useMemo(() => {
    const ids: string[] = []
    const walk = (ls: ResourceLane[]) => {
      for (const l of ls) {
        if (l.children?.length) {
          ids.push(l.id)
          walk(l.children as ResourceLane[])
        }
      }
    }
    walk(lanes)
    return ids
  }, [lanes])
  const mostlyExpanded =
    expandableIds.length > 0 &&
    expandableIds.filter((id) => expandedLanes.has(id)).length >= expandableIds.length / 2
  useRegisterShortcut({
    id: 'swimlane-toggle-expand-all',
    keys: 'e',
    description: 'Expand/collapse all resources',
    category: 'Timeline',
    scope: 'timeline',
    handler: (ev) => {
      ev.preventDefault()
      setAllExpanded(!mostlyExpanded)
    },
  })

  // Calculate visible time range. When controlled, the host's window replaces the
  // internal zoom/pan/stableNow math entirely (adapter layer — the uncontrolled
  // branch below is unchanged).
  const visibleTimeRange = useMemo(() => {
    if (viewWindow) {
      return {
        start: viewWindow.fromMs,
        end: viewWindow.toMs,
        windowMs: viewWindow.toMs - viewWindow.fromMs,
        now: effectiveNow,
      }
    }
    const windowMs = zoom * 60 * 60 * 1000
    const end = effectiveNow - panOffset
    const start = end - windowMs
    return { start, end, windowMs, now: effectiveNow }
  }, [zoom, panOffset, effectiveNow, viewWindow])

  // Apply the chosen ordering to the top-level lanes. 'recent' needs the visible
  // window (newest-in-view first), so this lives after visibleTimeRange. The
  // pinned section is ordered separately (strict pin order) and never flows here.
  const orderedLanes = useMemo(
    () => sortTimelineLanes(lanes, sort, {
      windowStart: visibleTimeRange.start,
      windowEnd: visibleTimeRange.end,
      scoreOf: (lane) => lane.scoreBreakdown?.total ?? calculateInterestingness(lane),
    }),
    [lanes, sort, visibleTimeRange],
  )

  // Live-mode order hysteresis. The importance rank recomputes every poll (recency
  // decay + new events), which reshuffles the whole list — the "screen jumps" the
  // user sees. While live, keep the prior relative order and splice new lanes in by
  // rank (mergeLaneOrderById). A user-initiated re-rank — sort / grouping / any
  // content-filter change, or leaving live — drops the hysteresis and adopts the
  // fresh rank. The live edge sliding (or a live pan) is NOT a re-rank; keeping order
  // calm across it is the whole point. Inert when !isLive (local / WorkloadView).
  const laneOrderRef = useRef<string[] | null>(null)
  const rankResetKey = useMemo(
    () => JSON.stringify([sort, grouping, searchTerm, showDeleted, activityFilter, kindFilter]),
    [sort, grouping, searchTerm, showDeleted, activityFilter, kindFilter],
  )
  const rankResetKeyRef = useRef(rankResetKey)
  const wasLiveRef = useRef(false)
  const stableOrderedLanes = useMemo(() => {
    const freshIds = orderedLanes.map((l) => l.id)
    const resetKeyChanged = rankResetKeyRef.current !== rankResetKey
    rankResetKeyRef.current = rankResetKey
    const enteringLive = !!isLive && !wasLiveRef.current
    wasLiveRef.current = !!isLive
    if (!isLive || resetKeyChanged || enteringLive || laneOrderRef.current == null) {
      laneOrderRef.current = freshIds
      return orderedLanes
    }
    const mergedIds = mergeLaneOrderById(laneOrderRef.current, freshIds)
    laneOrderRef.current = mergedIds
    const byId = new Map(orderedLanes.map((l) => [l.id, l]))
    return mergedIds.map((id) => byId.get(id)).filter((l): l is ResourceLane => l != null)
  }, [orderedLanes, isLive, rankResetKey])

  // Window-width label (the visible span, e.g. "15m" / "8h" / "3d"), rendered
  // only in local (uncontrolled) mode. Shares the scrubber's lens-width
  // formatting so the two never drift.
  const windowLabel = formatLensDuration(visibleTimeRange.windowMs)

  // "→ Now" is a local-mode affordance only: controlled/retained mode hides it
  // (the scrubber's live/paused chip owns the path back to now). Uncontrolled it
  // shows once the view has panned into the past, and resets the internal pan.
  const showJumpToNow = !controlled && panOffset > 0
  const handleJumpToNow = () => {
    setPanOffset(0)
  }

  // Extend affordances: when the controlled window is pinned against a bound edge
  // there may be more retained data just beyond the loaded selection. The future
  // side only offers extension when the selection actually ends before ~now (a
  // historical query) — you can't load data that doesn't exist yet.
  const EDGE_EPSILON_MS = 1000
  const atPastEdge = controlled && !!bounds && !!onExtendRequest
    && visibleTimeRange.start <= bounds.fromMs + EDGE_EPSILON_MS
  const atFutureEdge = controlled && !!bounds && !!onExtendRequest
    && visibleTimeRange.end >= bounds.toMs - EDGE_EPSILON_MS
    && bounds.toMs < effectiveNow - 60 * 1000

  // Events inside the current view window (post-filter). Drives the toolbar
  // "events" count so it's view-scoped like "resources" — a lens sitting in a
  // recording gap reads "0 resources · 0 events", not "0 resources · N events".
  const eventsInWindow = useMemo(() => {
    const { start, end } = visibleTimeRange
    return filteredEvents.filter((e) => {
      const t = new Date(e.timestamp).getTime()
      return t >= start && t <= end
    })
  }, [filteredEvents, visibleTimeRange])

  // Recording gaps clipped to the visible window, for the hatched lane bands.
  const visibleGaps = useMemo(() => {
    if (!gaps || gaps.length === 0) return []
    const { start, end } = visibleTimeRange
    const out: TimeWindow[] = []
    for (const g of gaps) {
      const fromMs = Math.max(g.fromMs, start)
      const toMs = Math.min(g.toMs, end)
      if (toMs > fromMs) out.push({ fromMs, toMs })
    }
    return out
  }, [gaps, visibleTimeRange])

  // Pin MOVES a row: pinned lanes (and pinned children inside groups) leave
  // the regular list entirely — the pinned section is their only home.
  const pinnedIdSetForFilter = useMemo(() => new Set((pinnedLanes ?? []).map((p) => p.id)), [pinnedLanes])
  const pinnedAppKeys = useMemo(
    () => new Set((pinnedLanes ?? []).flatMap((p) => (p.type === 'appGroup' ? [p.appKey] : []))),
    [pinnedLanes],
  )
  const unpinnedLanes = useMemo(
    () => removePinnedLanes(stableOrderedLanes, pinnedIdSetForFilter, pinnedAppKeys),
    [stableOrderedLanes, pinnedIdSetForFilter, pinnedAppKeys],
  )

  // Filter out lanes with no events in the visible time window
  const visibleLanes = useMemo(() => {
    const { start, end } = visibleTimeRange
    return unpinnedLanes.filter(lane => {
      const allLaneEvents = lane.allEventsSorted || []
      return allLaneEvents.some(e => {
        const t = new Date(e.timestamp).getTime()
        return t >= start && t <= end
      })
    })
  }, [unpinnedLanes, visibleTimeRange])

  // Pinned rows: resolved from the FULL lane list (pre-window-filter, any
  // grouping) so they stay put while the lens moves and even when they have no
  // events in the window. The regular list above has them removed (move, not
  // copy); counts add them back explicitly below.
  const pinnedLaneRows = useMemo(
    () => extractPinnedLanes(lanes, pinnedLanes ?? []),
    [lanes, pinnedLanes],
  )
  const effectivePinnedOnly = pinnedOnly && pinnedLaneRows.length > 0
  // Honest counts while filtered to pins: only pinned rows' in-window events.
  const pinnedEventsInWindow = useMemo(() => {
    if (pinnedLaneRows.length === 0) return 0
    const { start, end } = visibleTimeRange
    let n = 0
    for (const lane of pinnedLaneRows) {
      for (const e of lane.allEventsSorted || []) {
        const t = new Date(e.timestamp).getTime()
        if (t >= start && t <= end) n++
      }
    }
    return n
  }, [pinnedLaneRows, visibleTimeRange])

  const pinnedIdSet = useMemo(() => new Set((pinnedLanes ?? []).map((p) => p.id)), [pinnedLanes])
  // A pin button for a lane, or null when the host wired no pin handler. A pinned
  // lane's button is filled and always visible (in the pinned section or its
  // original spot); an unpinned one reveals on row hover.
  const renderPinButton = useCallback((lane: ResourceLane): React.ReactNode => {
    if (!onTogglePin) return null
    const pinned = pinnedIdSet.has(lane.id)
    const ref: PinnedLaneRef = lane.isAppGroup && lane.appKey
      ? { type: 'appGroup', id: lane.id, appKey: lane.appKey, appName: lane.title ?? lane.name }
      : { id: lane.id, kind: lane.kind, namespace: lane.namespace, name: lane.name }
    return (
      <PinButton
        pinned={pinned}
        alwaysVisible={pinned}
        isAppGroup={lane.isAppGroup}
        onToggle={() => onTogglePin(ref)}
      />
    )
  }, [onTogglePin, pinnedIdSet])

  // Generate time axis ticks
  const axisTicks = useMemo(() => {
    const { start, end } = visibleTimeRange
    const ticks: { time: number; label: string }[] = []
    const span = end - start
    if (span <= 0) return ticks

    // Pick the smallest "nice" interval that keeps the tick count under a cap —
    // so ANY window (a 15-minute view or a 2-year one) renders ~6–10 readable
    // ticks instead of hundreds smearing into a band. The ladder tops out at a
    // year; a wider window just steps by years.
    const MIN = 60 * 1000, HOUR = 60 * MIN, DAY = 24 * HOUR
    const NICE = [
      MIN, 2 * MIN, 5 * MIN, 10 * MIN, 15 * MIN, 30 * MIN,
      HOUR, 2 * HOUR, 3 * HOUR, 6 * HOUR, 12 * HOUR,
      DAY, 2 * DAY, 7 * DAY, 14 * DAY, 30 * DAY, 90 * DAY, 180 * DAY, 365 * DAY,
    ]
    const MAX_TICKS = 10
    const target = span / MAX_TICKS
    let intervalMs = NICE.find((n) => n >= target)
      ?? Math.ceil(target / (365 * DAY)) * 365 * DAY

    const firstTick = Math.ceil(start / intervalMs) * intervalMs
    for (let t = firstTick; t <= end; t += intervalMs) {
      ticks.push({ time: t, label: formatAxisTime(new Date(t)) })
    }

    return ticks
  }, [visibleTimeRange])

  // Convert timestamp to X position (0-100%)
  const timeToX = useCallback(
    (timestamp: number): number => {
      const { start, windowMs } = visibleTimeRange
      // A zero-width window (host bounds with fromMs === toMs) would divide by
      // zero and emit NaN into `left:` CSS — pin everything to the left edge.
      if (windowMs <= 0) return 0
      return ((timestamp - start) / windowMs) * 100
    },
    [visibleTimeRange]
  )

  // Vertical grid line positions (x-percent) shared by every lane backdrop.
  const gridXs = useMemo(
    () => axisTicks.map((t) => timeToX(t.time)).filter((x) => x > 0 && x < 100),
    [axisTicks, timeToX]
  )

  // Open the detail drawer for a marker or a cluster pill. A single-event cluster
  // opens today's one-event drawer (re-click toggles it closed). A multi-event
  // cluster opens the drawer in CLUSTER mode carrying every member, ordered
  // most-severe-first and preselecting the top one — that's exactly the event the
  // pill's glyph already promised, so the drawer opens on the resource the user
  // was looking at, with the other N-1 one click away. Re-clicking the same pill
  // toggles it closed.
  const openCluster = useCallback((cluster: TimelineEventCluster) => {
    // clusterDrawerState fixes the member ordering (dominant first) so the click
    // path and the URL-restore path can't drift; re-clicking the same cluster
    // toggles the drawer closed.
    const next = clusterDrawerState(cluster)
    const key = next.events.map((e) => e.id).join('|')
    const isToggleClose = !!drawer && drawer.events.map((e) => e.id).join('|') === key
    if (isToggleClose) {
      // Re-click on the open pill is a user close: record the dismissed id so the
      // restore effect doesn't resurrect it from the not-yet-stripped ?event=.
      dismissedIdRef.current = drawer!.selectedId
      setDrawer(null)
      return
    }
    // Opening (a new selection) clears any prior dismissal — the user intends to
    // see this drawer, so nothing must suppress it.
    dismissedIdRef.current = null
    setDrawer(next)
  }, [drawer])

  // User close (X button / Escape). Records the dismissed id so the restore
  // effect treats the id still sitting in ?event= as dismissed, not as a fresh
  // deep-link to re-open. Cleared once the selection actually settles to null.
  const closeDrawer = useCallback(() => {
    if (drawer) dismissedIdRef.current = drawer.selectedId
    setDrawer(null)
  }, [drawer])

  // --- Observable drawer selection (URL routing) ---------------------------
  // The rendered rows the resolver searches: pinned rows first, then the visible
  // lanes, matching render order so restore lands on the same row the user sees.
  // pinnedOnly hides the visible lanes entirely, so a restore must only search
  // rows that actually render — otherwise it lands on a hidden lane the user
  // can't see (the render below drops visibleLanes under effectivePinnedOnly).
  const resolverRows = useMemo(
    () => (effectivePinnedOnly ? pinnedLaneRows : [...pinnedLaneRows, ...visibleLanes]),
    [effectivePinnedOnly, pinnedLaneRows, visibleLanes],
  )
  // The id we've already resolved/ruled-out, so a settled miss reports null once.
  // Doubles as the guard's `settledId`: a fresh selectedEventId (never settled)
  // keeps restoreIsPending true so a still-loading restore wins over the strip.
  const restoredForRef = useRef<string | null>(null)
  // The last selectedEventId this effect observed, so it can tell a SELECTION
  // change (re-resolve the open drawer to it) from a DRAWER or window change
  // (leave an open drawer alone — a user opening a cluster, or a pan, is
  // authoritative and must not be re-resolved back to the prior selection).
  const prevSelRef = useRef<string | null>(selectedEventId ?? null)

  useEffect(() => {
    if (!onSelectedEventChange) return
    const want = selectedEventId ?? null
    const selectionChanged = prevSelRef.current !== want
    prevSelRef.current = want
    // Clearing the selection re-arms the machine: an id ruled out once must be
    // resolvable again if the user re-selects it (ref is keyed by id, not reset).
    // The dismissed id is cleared here too: once the selection has settled to
    // null the close is complete, so a later back/forward to the same id is a
    // genuine restore, not the close echo.
    if (want == null) {
      restoredForRef.current = null
      dismissedIdRef.current = null
      return
    }
    // Skip only when the drawer already shows the EXACT wanted event; otherwise a
    // new external selection (host list click, back/forward nav) re-resolves the
    // drawer to it — including a different member of the same time-cluster, so its
    // selectedId follows — instead of the report effect reverting to the stale id.
    if (drawer && drawer.selectedId === want) return
    // Only re-resolve an OPEN drawer when the selection itself changed. If this
    // fire is from a drawer change (the user just opened a different cluster) or a
    // pan, leave it — otherwise it snaps the click back to the stale selection.
    if (drawer && !selectionChanged) return
    if (restoredForRef.current === want) return
    // A user close wins: the id the user just dismissed must not be reopened by
    // the ?event=<id> the strip hasn't cleared yet. Once the selection settles to
    // null (above) the dismissal clears, so a real re-select/history-nav restores.
    if (dismissedIdRef.current === want) return
    const resolved = resolveEventCluster(resolverRows, expandedLanes, want, timeToX)
    if (resolved) {
      restoredForRef.current = want
      setDrawer(resolved)
      return
    }
    // Not on any row yet — keep waiting while data is still loading; only give up
    // (strip the stale param, exactly once) after the fetch has settled.
    if (isLoading) return
    restoredForRef.current = want
    onSelectedEventChange(null)
  }, [selectedEventId, drawer, resolverRows, expandedLanes, timeToX, isLoading, onSelectedEventChange])

  useEffect(() => {
    if (!onSelectedEventChange) return
    const want = selectedEventId ?? null
    if (restoreIsPending(want, drawer?.selectedId ?? null, restoredForRef.current, dismissedIdRef.current)) return
    const cur = drawer?.selectedId ?? null
    if (cur === want) return
    onSelectedEventChange(cur)
  }, [drawer, selectedEventId, onSelectedEventChange])

  // Zoom handlers - snap to predefined levels. Controlled: emit the would-be
  // window (end-anchored, clamped to bounds); uncontrolled: mutate `zoom`.
  const handleZoomIn = () => {
    if (controlled) { onViewWindowChange?.(zoomWindowWithinBounds(viewWindow!, 'in', bounds)); return }
    setZoom((z) => {
      const idx = ZOOM_LEVELS.findIndex(level => level >= z)
      return ZOOM_LEVELS[Math.max(0, idx - 1)]
    })
  }
  const handleZoomOut = () => {
    if (controlled) { onViewWindowChange?.(zoomWindowWithinBounds(viewWindow!, 'out', bounds)); return }
    setZoom((z) => {
      const idx = ZOOM_LEVELS.findIndex(level => level > z)
      return ZOOM_LEVELS[Math.min(ZOOM_LEVELS.length - 1, idx === -1 ? ZOOM_LEVELS.length - 1 : idx)]
    })
  }

  // Pan with mouse drag
  const handleMouseDown = (e: React.MouseEvent) => {
    if (e.button !== 0) return
    setIsDragging(true)
    setDragStart({ x: e.clientX, offset: panOffset, windowFrom: visibleTimeRange.start, windowTo: visibleTimeRange.end })
  }

  const handleMouseMove = useCallback(
    (e: MouseEvent) => {
      if (!isDragging || !containerRef.current) return

      const containerWidth = containerRef.current.clientWidth
      const dx = e.clientX - dragStart.x
      const { windowMs } = visibleTimeRange

      const timePerPixel = windowMs / containerWidth

      if (controlled) {
        // Mirror the uncontrolled shift (Δend = +dx·timePerPixel) as an absolute
        // window shift, clamped to bounds instead of the internal panOffset>=0 cap.
        const shift = dx * timePerPixel
        const next = { fromMs: dragStart.windowFrom + shift, toMs: dragStart.windowTo + shift }
        onViewWindowChange?.(bounds ? clampWindowToBounds(next, bounds) : next)
        return
      }

      const newOffset = dragStart.offset - dx * timePerPixel
      setPanOffset(Math.max(0, newOffset))
    },
    [isDragging, dragStart, visibleTimeRange, controlled, bounds, onViewWindowChange]
  )

  const handleMouseUp = useCallback(() => {
    setIsDragging(false)
  }, [])

  useEffect(() => {
    if (isDragging) {
      window.addEventListener('mousemove', handleMouseMove)
      window.addEventListener('mouseup', handleMouseUp)
      return () => {
        window.removeEventListener('mousemove', handleMouseMove)
        window.removeEventListener('mouseup', handleMouseUp)
      }
    }
  }, [isDragging, handleMouseMove, handleMouseUp])

  // Wheel: ctrl/cmd = zoom CONTINUOUSLY (scale by the wheel delta, not the preset
  // ladder — the +/- buttons keep the round preset stops); horizontal scroll =
  // pan the view window, same motion as dragging the canvas. Attached as a NATIVE
  // non-passive listener (not React's onWheel, which is passive at the root)
  // so preventDefault actually blocks the browser's horizontal back/forward
  // overscroll gesture.
  const handleWheel = useCallback((e: WheelEvent) => {
    if (e.ctrlKey || e.metaKey) {
      e.preventDefault()
      const factor = wheelZoomFactor(e.deltaY, e.deltaMode)
      if (controlled) {
        onViewWindowChange?.(zoomWindowContinuous(viewWindow!, factor, bounds))
        return
      }
      // Uncontrolled: `zoom` is a free hours value (rungs only snap in the
      // buttons), so scale it directly and clamp to the ladder's span.
      setZoom((z) => Math.max(ZOOM_LEVELS[0], Math.min(z * factor, ZOOM_LEVELS[ZOOM_LEVELS.length - 1])))
      return
    }

    // Horizontal gesture: trackpad swipe (|deltaX| dominates) or shift+wheel.
    const horizontal = Math.abs(e.deltaX) > Math.abs(e.deltaY) || e.shiftKey
    if (!horizontal || !containerRef.current) return
    e.preventDefault()
    const delta = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY
    const containerWidth = containerRef.current.clientWidth
    const shift = delta * (visibleTimeRange.windowMs / containerWidth)
    if (controlled) {
      const next = { fromMs: viewWindow!.fromMs + shift, toMs: viewWindow!.toMs + shift }
      onViewWindowChange?.(bounds ? clampWindowToBounds(next, bounds) : next)
    } else {
      setPanOffset((o) => Math.max(0, o - shift))
    }
  }, [controlled, viewWindow, bounds, onViewWindowChange, visibleTimeRange])

  // Ref-stable indirection + isLoading in deps: the loading early-return means
  // the container doesn't exist on first mount, so the listener must (re)attach
  // once the real tree appears — without re-subscribing on every handler change.
  const wheelHandlerRef = useRef(handleWheel)
  wheelHandlerRef.current = handleWheel
  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    const h = (e: WheelEvent) => wheelHandlerRef.current(e)
    el.addEventListener('wheel', h, { passive: false })
    return () => el.removeEventListener('wheel', h)
  }, [isLoading])

  // Toolbar chip counts tell ONE story with the strip: they count
  // within the LOADED range (bounds) so "All" matches the strip's loaded total
  // — not the whole loaded ring, whose 3,5xx total came from nowhere the user
  // could see. Unbounded (uncontrolled local mode) keeps counting the ring.
  // MUST sit above the isLoading early return — hooks after it violate the
  // rules of hooks (React #310) the moment loading flips.
  const queryScopedEvents = useMemo(() => {
    if (!bounds) return events
    return events.filter((e) => {
      const t = new Date(e.timestamp).getTime()
      return t >= bounds.fromMs && t <= bounds.toMs
    })
  }, [events, bounds])

  if (isLoading) {
    return <PaneLoader label="Loading timeline…" className="h-full w-full" />
  }

  // Compute empty state info (but don't early return - we need the toolbar visible)
  const hasFilteredEvents = visibleLanes.length === 0 && events.length > 0 && filteredEvents.length === 0

  // One lane row and — when expanded — its children, recursively. Shared by the
  // pinned section and the sorted lanes so the pinned copy reuses the exact track.
  //
  // Event attribution (the locked rule) is centralized in `laneTrackEvents`:
  //   - a COLLAPSED parent paints its whole-subtree aggregate (the roll-up)
  //   - an EXPANDED parent paints ONLY its own resource's events; each child
  //     row then renders its OWN slice below it (recursing, so a child that owns
  //     collapsed children of its own paints ITS aggregate).
  // So no event lands on two visible rows, and a resource that owns events in
  // the window can't render an empty track. This holds at every depth — a
  // CronJob member of an app group expands into its Job/Pod rows, each carrying
  // its own events instead of the ancestor swallowing them all.
  //
  // `keyPrefix` keeps a pinned copy's React key distinct from its original;
  // `depth` (0 = top level) drives the label style and child indentation.
  const renderLane = (lane: ResourceLane, keyPrefix = '', depth = 0, isLast = false): React.ReactNode => {
    const isExpanded = expandedLanes.has(lane.id)
    const hasChildren = !!lane.children?.length
    // When expanded, a parent renders only children that MOVE in the visible lens
    // — a child whose whole subtree sits outside the window would otherwise paint
    // an empty row. Same rule top-level lanes already follow (visibleLanes above).
    // Exemptions: a pinned child (its row is its home), a child the USER opened
    // (don't yank a row they deliberately expanded), and a structural app member
    // (a server-declared workload/Service/Ingress — hiding an app's own Service
    // makes a matched app read as incomplete). Auto-expanded children are NOT
    // exempt, so a stale auto-open falls away cleanly as the lens moves off it. The
    // chevron + count key off this visible set so "N resources" and "+N" match the
    // rows actually on screen; the collapsed roll-up already paints in-window only.
    const visibleChildren = hasChildren
      ? lane.children!.filter((child) =>
          isChildVisibleInWindow(child, visibleTimeRange.start, visibleTimeRange.end, {
            pinned: pinnedIdSet.has(child.id),
            userExpanded: userLaneOverrides.get(child.id) === true,
          }),
        )
      : []
    const hasVisibleChildren = visibleChildren.length > 0
    const trackEvents = laneTrackEvents(lane, isExpanded)
    const ownResource = { kind: lane.kind, namespace: lane.namespace, name: lane.name }
    // Show the group chip only when this lane collides with another visible lane
    // of a different API group (same kind+ns+name). The kind badge's title always
    // carries the full group (non-intrusive), collision or not.
    const showGroupChip = !lane.isAppGroup && !!lane.group && collidingKeys.has(laneCollisionKey(lane))
    // Tooltip carries the group only for CRD groups — built-in resources stay
    // visually untouched (the group appears nowhere for them, not even on hover).
    const kindBadgeTitle = !lane.isAppGroup && groupQualifiesLaneId(lane.group) ? `${displayKind(lane.kind)} · ${lane.group}` : undefined
    // An expanded APP header keeps a dimmed "ghost" aggregate sweep: the
    // rollup stays scannable while the children below carry the detail. Collapsed
    // parents/apps paint the full-strength aggregate. Expanded non-app parents
    // (a DaemonSet over its Pods) show their own events, not a rollup.
    const showGhostAggregate = hasVisibleChildren && isExpanded && lane.isAppGroup
    const track = (heightClass: string, small?: boolean) => (
      <LaneTrack
        className={clsx('flex-1 mr-8', heightClass)}
        events={trackEvents}
        ownResource={ownResource}
        // A collapsed parent/app-group row shows an AGGREGATE: sweep its members'
        // state families instead of blending latest-event-wins.
        aggregateLane={hasVisibleChildren && (!isExpanded || showGhostAggregate) ? lane : undefined}
        ghost={showGhostAggregate}
        startTime={visibleTimeRange.start}
        windowMs={visibleTimeRange.windowMs}
        now={visibleTimeRange.now}
        timeToX={timeToX}
        selectedEvent={selectedEvent}
        onSelectCluster={openCluster}
        small={small}
        gridXs={gridXs}
      />
    )
    const childRows = isExpanded && hasVisibleChildren && (
      <div
        className="bg-theme-surface/30"
        style={{ animation: 'swimlane-expand 250ms ease-out both' }}
      >
        {visibleChildren.map((child, idx) =>
          renderLane(child, keyPrefix, depth + 1, idx === visibleChildren.length - 1),
        )}
      </div>
    )

    if (depth === 0) {
      return (
        // An app group is a block, not a row: full-strength top/bottom borders
        // set the app boundary apart from the hairline dividers between the
        // resource rows inside it (an expanded group's last child suppresses
        // its own border, so the block edge is the only closing line).
        <div key={keyPrefix + lane.id} className={clsx(lane.isAppGroup && 'border-y border-theme-border')}>
          {/* Parent lane */}
          <div className={lane.isAppGroup && !(isExpanded && hasVisibleChildren) ? undefined : 'border-b-subtle'}>
            <div className="flex items-center">
              {/* Lane label */}
              <div className={clsx('relative shrink-0 border-r border-theme-border px-3 flex items-center gap-1 group/pin', laneLabelWidthClass, compact ? 'py-1' : 'py-2')}>
                {/* Descender: when expanded, carry the tree line from this row's
                    chevron down to its first child's incoming trunk (rail 0). */}
                {hasVisibleChildren && isExpanded && (
                  <span className="absolute bottom-0 top-1/2 w-px bg-theme-border" style={{ left: TREE_ROOT_RAIL_PX }} />
                )}
                {/* Expand/collapse button */}
                {hasVisibleChildren ? (
                  <button
                    onClick={() => toggleLane(lane.id)}
                    aria-label={isExpanded ? 'Collapse' : 'Expand'}
                    className="p-1 -m-0.5 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
                  >
                    <ChevronRight className={clsx(
                      'w-4 h-4 transition-transform',
                      isExpanded && 'rotate-90'
                    )} />
                  </button>
                ) : compact ? null : (
                  <div className="w-4" />
                )}
                {lane.isAppGroup ? (
                  <AppGroupLaneLabel lane={lane} memberCount={visibleChildren.length} onToggle={() => toggleLane(lane.id)} onAppClick={onAppClick} />
                ) : (
                  /* Two-line owner lane: [ name (bold) · +N · ⚠ ] on row 1, then a
                     quiet metadata row 2 [ kind · namespace ]. The name owns the
                     full first line so it stops losing the width fight to the kind
                     chip + namespace. Only the NAME navigates; the rest is inert. */
                  <div className="flex-1 min-w-0 flex flex-col gap-0.5">
                    <div className="flex items-center gap-1.5 min-w-0">
                      <Tooltip content={lane.name} wrapperClassName="min-w-0 flex-1">
                        <span
                          onClick={() => handleLaneOpen(lane.kind, lane.namespace, lane.name, lane.group)}
                          className={clsx('min-w-0 w-full text-sm text-theme-text-primary hover:text-accent-text hover:underline cursor-pointer', compact ? 'font-medium' : 'font-semibold font-mono')}
                        >
                          <MiddleEllipsis text={lane.name} className="block" />
                        </span>
                      </Tooltip>
                      {hasVisibleChildren && (
                        <span className="shrink-0 text-[11.5px] font-semibold text-theme-text-tertiary">
                          {`+${visibleChildren.length}`}
                        </span>
                      )}
                      <LaneWarnChip events={lane.allEventsSorted || []} />
                    </div>
                    <div className="flex items-center gap-1.5 min-w-0">
                      <KindChip kind={lane.kind} title={kindBadgeTitle} />
                      {showGroupChip && <GroupChip group={lane.group!} />}
                      {lane.namespace && !compact && (
                        <span className="min-w-0 truncate text-[11px] text-theme-text-tertiary">
                          {lane.namespace}
                        </span>
                      )}
                    </div>
                  </div>
                )}
                {/* Pin toggle. Real lanes pin the resource; app-group headers pin
                    the app (members re-resolve live). Navigation is scoped to the
                    name text, so the pin sits inert alongside it. */}
                {renderPinButton(lane)}
              </div>
              {track('h-11')}
            </div>
          </div>
          {childRows}
        </div>
      )
    }

    // Depth >= 1: an indented tree row. A row with its own children carries an
    // expand chevron; the connector's trunk stops half-way only on the last leaf.
    return (
      <div key={keyPrefix + lane.id} className={clsx('border-b-subtle', isLast && !(isExpanded && hasVisibleChildren) && 'border-b-0')}>
        <div className="flex">
          <ChildLaneLabel
            kind={lane.kind}
            group={lane.group}
            showGroupChip={showGroupChip}
            kindTitle={kindBadgeTitle}
            name={lane.name}
            labelWidthClass={laneLabelWidthClass}
            isLast={isLast && !(isExpanded && hasVisibleChildren)}
            depth={depth}
            hasChildren={hasVisibleChildren}
            expanded={isExpanded}
            onToggle={hasVisibleChildren ? () => toggleLane(lane.id) : undefined}
            onClick={() => handleLaneOpen(lane.kind, lane.namespace, lane.name, lane.group)}
            pinButton={renderPinButton(lane)}
            title={
              lane.nestedByContract ? `${lane.name} · linked by naming`
              : lane.matchedByName ? `${lane.name} · matched by name`
              : undefined
            }
          />
          {track('h-9', true)}
        </div>
        {childRows}
      </div>
    )
  }

  // Empty-state block — shared by the no-rows-at-all case and the
  // pinned-rows-but-nothing-else case (rendered below the pinned section).
  const emptyState = (
    <div className="flex flex-col items-center justify-center h-64 text-theme-text-tertiary">
      <AlertCircle className="w-12 h-12 mb-4 opacity-50" />
      {windowInsideGap(viewWindow, gaps) ? (
        <>
          {/* View window fully inside a recording gap: empty ≠ quiet. */}
          <p className="text-lg">Nothing was recorded here — the connector was offline</p>
          <p className="text-sm mt-1">Pan or zoom out — or drag the blue band on the strip above — to a recorded period</p>
        </>
      ) : hasFilteredEvents ? (
        <>
          <p className="text-lg">No matching events</p>
          <p className="text-sm mt-1">
            {describeActiveFilters({ search: searchTerm, activityFilter, kindFilter, showDeleted }) || 'Try adjusting your filters'}
          </p>
          {namespaces && namespaces.length > 0 && <p className="text-sm mt-1 text-theme-text-disabled">Searching in: {namespaces.length === 1 ? namespaces[0] : `${namespaces.length} namespaces`}</p>}
          <button
            type="button"
            onClick={clearAllFilters}
            className="mt-4 flex items-center gap-2 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-lg text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary transition-colors"
          >
            <X className="w-3.5 h-3.5" />
            Clear filters
          </button>
        </>
      ) : viewWindow && events.length > 0 ? (
        <>
          {/* Controlled window over loaded data: quiet ≠ waiting-for-live. Name
              the exact count sitting outside the window — the strip's bars show
              where, so point there instead of guessing at "a busier period". */}
          <p className="text-lg">Nothing changed in this view</p>
          <p className="text-sm mt-1">Only resources with activity are shown</p>
          <p className="text-sm mt-1">
            {pluralize(events.length, 'event')} elsewhere in the query range — move the window on the strip above to see them
          </p>
        </>
      ) : (
        <>
          <p className="text-lg">No events yet</p>
          <p className="text-sm mt-1">Events will appear here as resources change</p>
          {namespaces && namespaces.length > 0 && (
            <p className="text-sm mt-2 text-theme-text-secondary">
              Filtering by namespace: <span className="font-medium text-theme-text-primary">{namespaces.length === 1 ? namespaces[0] : `${namespaces.length} namespaces`}</span>
            </p>
          )}
          {hasLimitedAccess && (
            <p className="flex items-center gap-1 text-sm mt-2 text-amber-400/80">
              <Shield className="w-3.5 h-3.5" />
              Some resource types are not monitored due to RBAC restrictions
            </p>
          )}
        </>
      )}
    </div>
  )

  return (
    // Compact sizes to its content (up to the lane area's cap) so a workload with
    // two lanes doesn't reserve a full pane; the standalone view fills its parent.
    <div className={clsx('flex flex-col w-full', compact ? 'min-h-0' : 'h-full')}>
      {/* Toolbar with search and zoom. Hidden in compact mode (embedded
          single-subject swimlane) — the power-user controls are overkill there. */}
      {/* No overflow-hidden here: the toolbar's kinds/view-options popovers must overhang. */}
      {!compact && (
      <div className="border-b border-theme-border bg-theme-surface/30 relative z-40">
        <TimelineToolbar
          search={searchTerm}
          onSearchChange={setSearchTerm}
          searchShortcutId="swimlane-search"
          activityFilter={activityFilter}
          onActivityFilterChange={setActivityFilter}
          events={queryScopedEvents}
          showDeleted={showDeleted}
          onShowDeletedChange={setShowDeleted}
          kindFilter={kindFilter}
          onKindFilterChange={setKindFilter}
          kindOptions={kindOptions}
          counts={effectivePinnedOnly
            ? { resources: pinnedLaneRows.length, events: pinnedEventsInWindow }
            : { resources: visibleLanes.length + pinnedLaneRows.length, events: eventsInWindow.length }}
          pinnedCount={pinnedLaneRows.length}
          pinnedOnly={effectivePinnedOnly}
          onPinnedOnlyChange={setPinnedOnly}
          countsFiltered={!!searchTerm || activityFilter.length > 0 || kindFilter.length > 0}
          view={viewMode}
          onViewChange={onViewModeChange}
          viewOptions={{
            sort: { value: sort, onChange: setSort },
            grouping: { value: grouping, onChange: setGrouping },
          }}
          legend={{ shown: showLegend, onToggle: () => setShowLegend((v) => !v) }}
        />
        {/* Swimlane strip controls (local mode only): zoom + "→ Now" sit next to
            the timeline strip. Retained/controlled mode owns the window through
            the scrubber (lens-width chip = zoom, live/paused chip = "→ Now"), so
            this whole row is hidden there. */}
        {!controlled && (
          <div className="flex items-center gap-2 px-4 py-1.5">
            <Tooltip content="Zoom in (Ctrl+scroll)">
              <button
                onClick={handleZoomIn}
                aria-label="Zoom in"
                className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
              >
                <ZoomIn className="w-4 h-4" />
              </button>
            </Tooltip>
            <Tooltip content="Zoom out (Ctrl+scroll)">
              <button
                onClick={handleZoomOut}
                aria-label="Zoom out"
                className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
              >
                <ZoomOut className="w-4 h-4" />
              </button>
            </Tooltip>
            <span className="text-xs text-theme-text-tertiary">
              {windowLabel} window
            </span>
            {showJumpToNow && (
              <Tooltip content="Jump to current time">
                <button
                  onClick={handleJumpToNow}
                  className="px-2 py-1 text-xs text-accent-text hover:underline hover:bg-theme-elevated rounded"
                >
                  → Now
                </button>
              </Tooltip>
            )}
          </div>
        )}
        {/* Legend: an on-demand popover, not a permanent strip. Two labelled
            sections — point-in-time event markers and lane-health bars — each a
            2-column grid. Toggled by the toolbar's Legend button. */}
        {showLegend && (
          <>
            <div className="fixed inset-0 z-40" onClick={() => setShowLegend(false)} aria-hidden />
            <div className="absolute right-4 top-full z-50 mt-1 w-[280px] rounded-xl border border-theme-border bg-theme-surface p-4 shadow-theme-lg">
              <div className="text-[10px] font-bold uppercase leading-none tracking-[0.07em] text-theme-text-tertiary">Events</div>
              <div className="mt-2.5 grid grid-cols-2 gap-x-4 gap-y-2 text-xs text-theme-text-secondary">
                {/* One activity hue: shape carries the event class. */}
                <MarkerLegendItem shape="triangle-up" colorClass="text-blue-600 dark:text-blue-400" label="created" description="Resource was created" />
                <MarkerLegendItem shape="circle" colorClass="text-blue-600 dark:text-blue-400" label="modified" description="Resource change or informational Kubernetes event" />
                <MarkerLegendItem shape="triangle-down" colorClass="text-blue-600 dark:text-blue-400" label="deleted" description="Resource was removed" />
                <MarkerLegendItem shape="diamond" colorClass="text-amber-500 dark:text-amber-400" label="warning" description="Warning event (CrashLoopBackOff, Failed, etc.)" />
                <MarkerLegendItem shape="ring" colorClass="text-theme-text-tertiary" label="historical" description="Inferred from resource metadata (creation time, etc.)" />
                <ClusterLegendItem description="Nearby events collapsed into one pill" />
              </div>
              <div className="mt-3.5 text-[10px] font-bold uppercase leading-none tracking-[0.07em] text-theme-text-tertiary">Health</div>
              <div className="mt-2.5 grid grid-cols-2 gap-x-4 gap-y-2 text-xs text-theme-text-secondary">
                {/* Exactly three status colors; everything else is gray. */}
                <HealthLegendItem color={HEALTH_STRIP_COLORS.healthy} label="healthy" description="Resource is fully operational" />
                <HealthLegendItem color={HEALTH_STRIP_COLORS.degraded} label="degraded" description="Unexpected partial availability" />
                <HealthLegendItem color={HEALTH_STRIP_COLORS.unhealthy} label="unhealthy" description="Resource is failing or not ready" />
                <HealthLegendItem color="bg-gray-400/50" label="no signal" description="Not a health state: rolling out, idle/suspended, or nothing observed yet" />
                <HealthLegendItem label="mixed" swatchStyle={MIXED_HEALTH_STRIP_STYLE} description="members disagree — some OK, some degraded; expand or hover for who" />
              </div>
            </div>
          </>
        )}
      </div>
      )}

      {/* Timeline container. Compact caps the lane area (content-height until then,
          scrolls past it) so the swimlane grows with the number of lanes. */}
      <div className={clsx('overflow-y-auto overflow-x-hidden', compact ? 'max-h-[240px]' : 'flex-1')}>
        <div
          ref={containerRef}
          className="min-w-full"
          onMouseDown={handleMouseDown}
          style={{ cursor: isDragging ? 'grabbing' : 'grab' }}
        >
          {/* Time axis header */}
          <div className="sticky top-0 z-30 bg-theme-surface border-b border-theme-border">
            <div className="flex">
              <div className={clsx('shrink-0 border-r border-theme-border px-3 h-8 flex items-center', laneLabelWidthClass)}>
                <span className="text-[11px] font-semibold uppercase tracking-[0.1em] text-theme-text-tertiary">Resource</span>
                {/* Single morphing toggle: ≥half the groups open → offers
                    collapse-all; otherwise expand-all. Tooltip names the action
                    and its shortcut. ml-auto rides the Tooltip's own flex wrapper
                    so the button stays vertically centered (a plain span around it
                    inflates to the line-height and top-aligns the glyph). */}
                {/* Flat compact lanes never nest, so bulk expand/collapse is a no-op. */}
                {!compact && (
                  <Tooltip content={mostlyExpanded ? 'Collapse all (E)' : 'Expand all (E)'} wrapperClassName="ml-auto">
                    <button
                      type="button"
                      onClick={() => setAllExpanded(!mostlyExpanded)}
                      className="rounded p-1 text-theme-text-tertiary hover:bg-theme-elevated hover:text-theme-text-primary"
                      aria-label={mostlyExpanded ? 'Collapse all resources' : 'Expand all resources'}
                    >
                      {mostlyExpanded
                        ? <ChevronsDownUp className="h-3.5 w-3.5" />
                        : <ChevronsUpDown className="h-3.5 w-3.5" />}
                    </button>
                  </Tooltip>
                )}
              </div>
              <div className="flex-1 relative h-8 mr-8">
                {(() => {
                  // Clamp in live mode: a data load can push `now` past the
                  // still-latched window for one tick; pinning to the right edge
                  // keeps the marker steady instead of blinking on every refresh.
                  const rawNowX = timeToX(visibleTimeRange.now)
                  const nowX = isLive ? Math.min(rawNowX, 100) : rawNowX
                  const nowVisible = nowX >= 0 && nowX <= 100
                  return (
                    <>
                      {axisTicks.map((tick) => {
                        const x = timeToX(tick.time)
                        // Left cull at 1.5% (not 0): a centered label hugging the
                        // window start would bleed over the Resource column
                        // header. Also cull the tick whose label would collide
                        // with the "Now" label at the live edge.
                        if (x < (atPastEdge ? 3.5 : 1.5)) return null
                        if (x > (atFutureEdge ? 96.5 : 100)) return null
                        if (nowVisible && Math.abs(x - nowX) < 4) return null
                        return (
                          <div
                            key={tick.time}
                            // -translate-x-1/2 centers the tick+label ON the time
                            // position; positioned by the left edge, every label's
                            // visual center sat half a label to the right of the
                            // gridline (and "Now" right of the Now line).
                            className="absolute top-0 bottom-0 flex -translate-x-1/2 flex-col items-center"
                            style={{ left: `${x}%` }}
                          >
                            <div className="h-2 w-px bg-theme-hover" />
                            <span className="mt-0.5 whitespace-nowrap text-xs text-theme-text-tertiary">{tick.label}</span>
                          </div>
                        )
                      })}
                      {/* "Now" marker in header — the label right-aligns to the
                          line at the edge so it never spills into the gutter. */}
                      {nowVisible && (
                        <div
                          className={clsx(
                            'absolute top-0 bottom-0 z-20 flex flex-col',
                            nowX > 97 ? '-translate-x-full items-end' : '-translate-x-1/2 items-center',
                          )}
                          style={{ left: `${nowX}%` }}
                        >
                          <div className={clsx('h-2 w-0.5 bg-purple-500', nowX > 97 && 'translate-x-0.5')} />
                          <span className="mt-0.5 whitespace-nowrap text-xs font-medium text-purple-500">Now</span>
                        </div>
                      )}
                    </>
                  )
                })()}
                {/* Extend affordances at the loaded-range edges. Stop propagation so
                    clicking doesn't also start a pan drag on the container. */}
                {atPastEdge && (
                  <Tooltip content="Start of loaded history — load earlier" position="bottom">
                    <button
                      type="button"
                      onMouseDown={(e) => e.stopPropagation()}
                      onClick={(e) => { e.stopPropagation(); onExtendRequest?.('past') }}
                      className="absolute left-0 top-1/2 z-30 flex h-5 w-5 -translate-y-1/2 items-center justify-center rounded border border-theme-border bg-theme-elevated text-theme-text-secondary shadow-theme-sm transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
                      aria-label="Extend the loaded range further into the past"
                    >
                      <ChevronLeft className="h-3 w-3" />
                    </button>
                  </Tooltip>
                )}
                {atFutureEdge && (
                  <Tooltip content="End of loaded history — load more recent" position="bottom">
                    <button
                      type="button"
                      onMouseDown={(e) => e.stopPropagation()}
                      onClick={(e) => { e.stopPropagation(); onExtendRequest?.('future') }}
                      className="absolute right-0 top-1/2 z-30 flex h-5 w-5 -translate-y-1/2 items-center justify-center rounded border border-theme-border bg-theme-elevated text-theme-text-secondary shadow-theme-sm transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
                      aria-label="Extend the loaded range further toward now"
                    >
                      <ChevronRight className="h-3 w-3" />
                    </button>
                  </Tooltip>
                )}
              </div>
            </div>
          </div>

          {/* Pinned section + swimlanes, or empty state. Pins always render above
              everything; the empty-state message still shows below them when the
              non-pinned window is quiet. */}
          {pinnedLaneRows.length === 0 && visibleLanes.length === 0 ? (
            emptyState
          ) : (
          <div className="relative">
            {/* "Now" line through swimlanes. In live mode a data load can push
                `now` past the still-latched window for one tick — clamp to the
                right edge instead of vanishing, or the line blinks/jumps on
                every refresh ("the Now line breaks as new data loads"). */}
            {(() => {
              const rawNowX = timeToX(visibleTimeRange.now)
              const nowX = isLive ? Math.min(rawNowX, 100) : rawNowX
              if (nowX < 0 || nowX > 100) return null
              return (
                <div
                  className="absolute top-0 bottom-0 w-0.5 bg-purple-500/50 z-10 pointer-events-none"
                  style={{ left: `calc(${laneLabelPx}px + (100% - ${laneTrackInsetPx}px) * ${nowX / 100})` }}
                />
              )
            })()}
            {/* Recording-gap bands behind the lanes — same hatch language as the
                strip, inert, captioned when wide enough. */}
            {visibleGaps.map((g, i) => {
              const fromFrac = Math.max(0, Math.min(1, timeToX(g.fromMs) / 100))
              const toFrac = Math.max(0, Math.min(1, timeToX(g.toMs) / 100))
              if (toFrac <= fromFrac) return null
              const widthPx = trackPx * (toFrac - fromFrac)
              return (
                <div
                  key={`gap-${i}`}
                  className="pointer-events-none absolute top-0 bottom-0 z-0 flex items-center justify-center overflow-hidden"
                  style={{
                    left: `calc(${laneLabelPx}px + (100% - ${laneTrackInsetPx}px) * ${fromFrac})`,
                    width: `calc((100% - ${laneTrackInsetPx}px) * ${toFrac - fromFrac})`,
                    background:
                      'repeating-linear-gradient(45deg, transparent 0, transparent 5px, var(--border-default) 5px, var(--border-default) 6px)',
                    opacity: 0.5,
                  }}
                  data-testid="swimlane-gap"
                >
                  {widthPx > GAP_LABEL_MIN_PX && (
                    <span className="whitespace-nowrap rounded bg-theme-surface/70 px-1 text-[10px] uppercase tracking-wide text-theme-text-tertiary">
                      connector offline
                    </span>
                  )}
                </div>
              )
            })}
            {/* PINNED — stationary rows above everything, in pin order, never
                re-ranked by interestingness. Same time axis as the lanes below. */}
            {pinnedLaneRows.length > 0 && (
              <div data-testid="timeline-pinned-section">
                <div className="flex">
                  <div className={clsx('shrink-0 border-r border-theme-border px-3 py-1.5', laneLabelWidthClass)}>
                    <span className="text-[11px] font-semibold uppercase tracking-[0.1em] text-theme-text-tertiary">Pinned</span>
                  </div>
                  <div className="flex-1" />
                </div>
                {pinnedLaneRows.map((lane) => renderLane(lane, 'pinned:'))}
                {/* Subtle divider separating the pinned vantage from the ranked lanes. */}
                <div className="border-b border-theme-border" />
              </div>
            )}
            {effectivePinnedOnly
              ? null
              : visibleLanes.length === 0
                // Pinned rows carrying in-window events ARE the content — the
                // regular section below is just the end of the list, and a list
                // doesn't caption its own end. The full empty states render only
                // when the whole canvas would otherwise be blank.
                ? (pinnedEventsInWindow > 0 ? null : emptyState)
                : (
                  <>
                    {visibleLanes.map((lane) => renderLane(lane))}
                    {/* End-of-list caption: absence here means "nothing happened
                        to it in this range", not "it doesn't exist". Same voice
                        as the empty states. */}
                    <div className="flex items-center justify-center gap-2 py-6 text-sm text-theme-text-tertiary">
                      <Clock className="h-4 w-4 opacity-50" />
                      <span>{compact ? 'Only resources with activity in this time span are shown' : 'The timeline shows only resources with activity in the loaded range'}</span>
                    </div>
                  </>
                )}
          </div>
          )}
        </div>
      </div>

      {/* Event detail drawer — single event, or a cluster's full member list. */}
      {/* The bottom detail drawer is suppressed in compact mode: the embedded
          host (workload detail) already shows the full event list, so selection is
          reported upward via onSelectedEventChange instead of opening a redundant
          second detail surface. */}
      {drawer && !compact && (
        <EventDetailPanel
          events={drawer.events}
          selectedId={drawer.selectedId}
          onSelectId={(id) => setDrawer((prev) => (prev ? { ...prev, selectedId: id } : prev))}
          onClose={closeDrawer}
          onResourceClick={onResourceClick}
          allEvents={filteredEvents}
        />
      )}
    </div>
  )
}

// Health-strip segment colors (solid, pinned to the lane's bottom edge).
// Status is EXACTLY three colors: healthy green, degraded amber,
// unhealthy red. Anything else — rolling, idle, neutral, unknown — is gray
// ("not a health signal"). No fourth status color, ever.
const HEALTH_STRIP_COLORS: Record<string, string> = {
  healthy: 'bg-green-500',
  rolling: 'bg-gray-400/50',
  degraded: 'bg-amber-500 dark:bg-[#b8861e]',
  unhealthy: 'bg-red-500',
  neutral: 'bg-gray-400/50',
  idle: 'bg-gray-400/50',
}

function getHealthStripColor(health: string): string {
  return HEALTH_STRIP_COLORS[health] ?? 'bg-gray-400/50'
}

// Legend glyph item (event markers) with hover tooltip.
function MarkerLegendItem({ shape, colorClass, label, description }: { shape: MarkerShape; colorClass: string; label: string; description: string }) {
  return (
    <Tooltip content={description} position="top">
      <span className="flex items-center gap-1.5 cursor-help">
        <span className="inline-flex w-3 h-3 items-center justify-center">
          <MarkerGlyph shape={shape} size={11} className={colorClass} />
        </span>
        {/* leading-none: the default line-box's descender space drags the label's
            optical center below the glyph's — hug the text so centers agree. */}
        <span className="leading-none">{label}</span>
      </span>
    </Tooltip>
  )
}

// Legend sample for the "×N clustered" pill.
function ClusterLegendItem({ description }: { description: string }) {
  return (
    <Tooltip content={description} position="top">
      <span className="flex items-center gap-1.5 cursor-help">
        <span className="inline-flex h-4 items-center gap-1 rounded-full border border-theme-border bg-theme-surface px-1.5">
          <MarkerGlyph shape="circle" size={8} className="text-blue-600 dark:text-blue-400" />
          <span className="text-[11px] font-semibold leading-none tabular-nums text-theme-text-primary">×5</span>
        </span>
        <span className="leading-none">clustered</span>
      </span>
    </Tooltip>
  )
}

// Health strip legend item - shows a bar swatch.
function HealthLegendItem({ color, label, description, swatchStyle }: { color?: string; label: string; description: string; swatchStyle?: React.CSSProperties }) {
  return (
    <Tooltip content={description} position="top">
      <span className="flex items-center gap-1.5 cursor-help">
        <span className={clsx('h-[7px] rounded-[2px]', color)} style={{ width: '18px', ...swatchStyle }} />
        <span className="leading-none">{label}</span>
      </span>
    </Tooltip>
  )
}

// Amber warning-count chip (⚠ N). Red is reserved for "actually broken"
// (that shows on the health bar); the ⚠ summary is a warning tone, so it's amber.
function LaneWarnChip({ events }: { events: TimelineEvent[] }) {
  const issueCount = events.filter((e) => isCriticalIssue(e)).length
  if (issueCount === 0) return null
  return (
    <Tooltip content={`${pluralize(issueCount, 'problem')} (OOMKilled, CrashLoopBackOff, etc.)`} position="top">
      <span className="flex items-center gap-0.5 text-[11px] font-semibold px-1.5 py-0.5 rounded bg-amber-100 text-amber-700 dark:bg-amber-950/50 dark:text-amber-400">
        <AlertTriangle className="w-3 h-3" />
        {issueCount}
      </span>
    </Tooltip>
  )
}

// Indented child lane label with an L-shaped tree connector. `depth` (>=1) steps
// the indent + connector one rung right per level so a grandchild (a Pod under a
// CronJob member) reads as nested, not sibling. When the child owns a subtree of
// its own it carries an expand chevron so it can be drilled into. Only the name
// text navigates to the resource; the chevron toggles; the rest of the row is inert.
// Tree geometry. The depth-0 (top-level) expand chevron sits ~this many px from
// the label's left edge (px-3 + the -m-0.5 p-1 button ≈ 22px). Every child rail
// is derived from it so verticals align under the chevron directly above them.
const TREE_ROOT_RAIL_PX = 22
const CHILD_INDENT_STEP_PX = 22
/** Chip label — the real Kind, verbatim (matches the list; no abbreviation,
 *  no ALL-CAPS, no truncation). Kind-less events read as "Event". */
export function chipKindLabel(kind: string): { label: string; abbreviated: boolean } {
  // Some cluster components emit events whose involvedObject has NO kind (e.g.
  // GKE's resource-tracker "BigQueryUpload" events) — label those as plain events.
  if (!kind) return { label: 'Event', abbreviated: false }
  return { label: kind, abbreviated: false }
}

// The kind chip: neutral pill on the lane's second row (color budget —
// identity is monochrome). Shows the real Kind in its normal PascalCase; the
// name owns the first row so it doesn't lose the width fight to metadata.
function KindChip({ kind, title }: { kind: string; title?: string }) {
  const { label } = chipKindLabel(kind)
  const tip = title ?? (kind ? undefined : 'Kubernetes event — its source object reports no kind')
  return (
    <Tooltip content={tip} disabled={!tip} wrapperClassName="shrink-0">
      <span
        className="rounded border border-theme-border-light bg-theme-elevated px-1.5 py-0.5 font-mono text-[10px] font-semibold tracking-wide text-theme-text-secondary"
        aria-label={tip}
      >
        {label}
      </span>
    </Tooltip>
  )
}

// Disambiguating group chip — the ONLY place a lane's API group surfaces
// visually, shown solely when the lane collides with another visible lane of a
// different group (same kind+ns+name; CAPI vs CNPG `Cluster`). Quiet styling so
// it reads as a subtle qualifier next to the kind badge, not a status pill.
function GroupChip({ group }: { group: string }) {
  return (
    <Tooltip content={`API group ${group}`} wrapperClassName="shrink-0">
      <span
        className="rounded bg-theme-hover px-1 py-px text-[10px] font-medium text-theme-text-tertiary ring-1 ring-inset ring-theme-border"
        aria-label={`API group ${group}`}
      >
        {group}
      </span>
    </Tooltip>
  )
}

function ChildLaneLabel({ kind, group, showGroupChip, kindTitle, name, labelWidthClass = 'w-[360px]', isLast, onClick, pinButton, title, depth = 1, hasChildren, expanded, onToggle }: { kind: string; group?: string; showGroupChip?: boolean; kindTitle?: string; name: string; labelWidthClass?: string; isLast: boolean; onClick: () => void; pinButton?: React.ReactNode; title?: string; depth?: number; hasChildren?: boolean; expanded?: boolean; onToggle?: () => void }) {
  // Tree rails: the INCOMING trunk sits under the parent's chevron (rail d-1), the
  // row's own chevron sits on its CHILDREN's rail (rail d). Deriving both from one
  // ROOT keeps every level's vertical aligned under the chevron above it.
  const parentRailPx = TREE_ROOT_RAIL_PX + (depth - 1) * CHILD_INDENT_STEP_PX
  const selfRailPx = TREE_ROOT_RAIL_PX + depth * CHILD_INDENT_STEP_PX
  const contentPadPx = selfRailPx + 14
  return (
    <div
      // width matches the top-level label column exactly (compact widens it) — a
      // narrower child width left the label column's right border ragged.
      className={clsx('relative shrink-0 border-r border-theme-border/50 pr-3 flex items-center gap-1.5 group/pin bg-theme-surface/40', labelWidthClass)}
      style={{ paddingLeft: contentPadPx }}
    >
      {/* Incoming trunk (under the parent's chevron) — elbow (half height) on the
          last child, full height otherwise so the next sibling connects. */}
      <span className={clsx('absolute top-0 w-px bg-theme-border', isLast ? 'h-1/2' : 'h-full')} style={{ left: parentRailPx }} />
      {/* Horizontal branch from the parent rail into this row. */}
      <span className="absolute top-1/2 h-px bg-theme-border" style={{ left: parentRailPx, width: selfRailPx - parentRailPx }} />
      {/* Descender: when expanded, carry the line down from this row's chevron to
          its first child's incoming trunk (which sits on selfRail). */}
      {hasChildren && expanded && (
        <span className="absolute bottom-0 top-1/2 w-px bg-theme-border" style={{ left: selfRailPx }} />
      )}
      {hasChildren && onToggle && (
        <button
          onClick={(e) => { e.stopPropagation(); onToggle() }}
          // Centered ON this row's rail. bg keeps the line from striking the glyph.
          className="absolute top-1/2 z-10 -translate-x-1/2 -translate-y-1/2 rounded bg-theme-surface p-1 text-theme-text-tertiary hover:bg-theme-elevated hover:text-theme-text-primary"
          style={{ left: selfRailPx }}
          aria-label={expanded ? 'Collapse' : 'Expand'}
        >
          <ChevronRight className={clsx('w-4 h-4 transition-transform', expanded && 'rotate-90')} />
        </button>
      )}
      {/* Single-line child row: kind chip · name (middle-ellipsis). Colored chips
          are neutral. Only the NAME links to the resource. */}
      <KindChip kind={kind} title={kindTitle} />
      {showGroupChip && group && <GroupChip group={group} />}
      <Tooltip content={title ?? name} wrapperClassName="min-w-0 flex-1">
        <span
          onClick={onClick}
          className="min-w-0 w-full text-[13px] font-mono text-theme-text-secondary hover:text-accent-text hover:underline cursor-pointer"
        >
          <MiddleEllipsis text={name} className="block" />
        </span>
      </Tooltip>
      {pinButton}
    </div>
  )
}

// Pin toggle on a lane label. stopPropagation so it doesn't trigger the label's
// navigate-on-click. Filled + always visible when pinned; hover-reveal otherwise.
function PinButton({ pinned, onToggle, alwaysVisible, isAppGroup }: { pinned: boolean; onToggle: () => void; alwaysVisible?: boolean; isAppGroup?: boolean }) {
  const title = isAppGroup
    ? (pinned ? 'Unpin app' : 'Pin app — keep all its rows visible while you pan and zoom')
    : (pinned ? 'Unpin' : 'Pin — keep this row visible while you pan and zoom')
  return (
    <Tooltip content={title} wrapperClassName="shrink-0">
      <button
        type="button"
        onClick={(e) => { e.stopPropagation(); onToggle() }}
        className={clsx(
          'p-0.5 rounded hover:bg-theme-elevated transition-opacity',
          pinned ? 'text-accent-text opacity-100' : 'text-theme-text-tertiary hover:text-theme-text-primary',
          !pinned && !alwaysVisible && 'opacity-0 group-hover/pin:opacity-100',
        )}
        aria-label={isAppGroup ? (pinned ? 'Unpin app' : 'Pin app') : (pinned ? 'Unpin' : 'Pin')}
      >
        <Pin className={clsx('w-3.5 h-3.5', pinned && 'fill-current')} />
      </button>
    </Tooltip>
  )
}

// App-group header label — an app roll-up over its member root lanes. Shows the
// app name, an optional env chip, and the member count. Evidence rides the App
// chip's tooltip (attaching it to the whole flex row would need a layout-breaking
// wrapper).
function AppGroupLaneLabel({ lane, memberCount, onToggle, onAppClick }: { lane: ResourceLane; memberCount: number; onToggle: () => void; onAppClick?: (appKey: string) => void }) {
  // A pinned app absent from the current view (owner/flat grouping or vanished)
  // is dimmed and reads its own honest tooltip.
  const dimmed = lane.absentPinnedApp
  const linkable = onAppClick && lane.appKey
  const tooltip = lane.absentPinnedApp
    ? 'app not present in the current view/grouping'
    : (lane.evidence || undefined)
  return (
    <div
      // Two-line app header: App chip · [ name (bold) · +N · ⚠ / ns ]. The
      // namespace drops to a quiet subtitle so the name gets the full first line;
      // the row toggles expand, only the name links to Applications.
      className="flex-1 min-w-0 flex items-center gap-1.5 cursor-pointer rounded px-1 -mx-1 hover:bg-theme-surface/30"
      onClick={onToggle}
    >
      <Tooltip content={tooltip} disabled={!tooltip} wrapperClassName="shrink-0">
        <span
          className={clsx(
            'inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide ring-1 ring-inset',
            dimmed
              ? 'bg-theme-hover text-theme-text-tertiary ring-theme-border'
              : 'bg-accent-muted text-accent-text ring-transparent',
          )}
          aria-label={tooltip}
        >
          <Layers className="w-3 h-3" />
          App
        </span>
      </Tooltip>
      <div className="min-w-0 flex-1 flex flex-col gap-0.5">
        <div className="flex items-center gap-1.5 min-w-0">
          <Tooltip content={linkable ? 'Open in Applications' : (lane.title ?? lane.name)} wrapperClassName="min-w-0 flex-1">
            <span
              className={clsx(
                'min-w-0 w-full text-sm font-semibold',
                dimmed ? 'text-theme-text-secondary' : 'text-theme-text-primary',
                linkable && 'hover:text-accent-text hover:underline cursor-pointer',
              )}
              // Name click navigates to the app's Applications page — mirroring how a
              // resource name routes to its detail; the row body still toggles expand.
              onClick={linkable ? (e) => { e.stopPropagation(); onAppClick!(lane.appKey!) } : undefined}
              role={linkable ? 'button' : undefined}
              aria-label={linkable ? 'Open in Applications' : undefined}
            >
              <MiddleEllipsis text={lane.title ?? lane.name} className="block" />
            </span>
          </Tooltip>
          <span className="shrink-0 text-[11.5px] font-semibold text-theme-text-tertiary">
            {`+${memberCount}`}
          </span>
          <LaneWarnChip events={lane.allEventsSorted || []} />
        </div>
        {lane.namespace && (
          <span className="text-[11px] text-theme-text-tertiary truncate">{lane.namespace}</span>
        )}
      </div>
    </div>
  )
}

export interface LaneTrackProps {
  /** Events attributed to this lane (already scoped/attributed by the caller). */
  events: TimelineEvent[]
  /** The lane's own resource identity — used to derive its health spans. */
  ownResource?: { kind: string; namespace: string; name: string }
  /** When set, paint the members' aggregate sweep instead of a single resource's
   *  timeline (a collapsed parent / app-group rollup). */
  aggregateLane?: ResourceLane
  /** Dim + thin the aggregate to a ghost (an expanded app header's rollup). */
  ghost?: boolean
  startTime: number
  windowMs: number
  now: number
  timeToX: (ms: number) => number
  selectedEvent?: TimelineEvent | null
  onSelectCluster?: (cluster: TimelineEventCluster) => void
  small?: boolean
  /** Vertical grid-line x positions (0–100); omit for just the center guide. */
  gridXs?: number[]
  /** Extra classes for the track container (height, flex, right margin). */
  className?: string
}

/**
 * One lane's timeline strip — the backdrop grid, the centered health bar, and the
 * event-dot markers riding on it. This is THE composable unit shared by the full
 * timeline page (`TimelineSwimlanes`) and the workload/app detail embed, so the
 * health-bar + dot rendering can never diverge between them again.
 */
export function LaneTrack({
  events,
  ownResource,
  aggregateLane,
  ghost,
  startTime,
  windowMs,
  now,
  timeToX,
  selectedEvent = null,
  onSelectCluster,
  small,
  gridXs,
  className,
}: LaneTrackProps) {
  return (
    <div className={clsx('relative', className)}>
      <LaneBackdrop gridXs={gridXs ?? []} />
      <HealthBarTrack
        events={events}
        ownResource={ownResource}
        aggregateLane={aggregateLane}
        ghost={ghost}
        startTime={startTime}
        windowMs={windowMs}
        now={now}
      />
      <LaneEventMarkers
        events={events}
        timeToX={timeToX}
        selectedEvent={selectedEvent}
        onSelectCluster={onSelectCluster ?? (() => {})}
        small={small}
      />
    </div>
  )
}

// Lane backdrop: subtle vertical grid lines + a horizontal center guide.
function LaneBackdrop({ gridXs }: { gridXs: number[] }) {
  return (
    <div className="absolute inset-0 z-0 pointer-events-none">
      {gridXs.map((x, i) => (
        <div key={i} className="absolute top-0 bottom-0 w-px bg-theme-border/40" style={{ left: `${x}%` }} />
      ))}
      <div className="absolute left-0 right-0 top-1/2 h-px bg-theme-border/40" />
    </div>
  )
}

// The 'mixed' segment (members disagree): a green/amber HEALTH weave, not a grey
// hatch (grey stripes read as "no data", not "busy"). Solid green means
// everyone's fine, solid amber all degraded, and this weave = they disagree — so
// the texture stays a health signal, distinct from the recording-gap hatch.
const MIXED_HEALTH_STRIP_STYLE: React.CSSProperties = {
  backgroundImage:
    'repeating-linear-gradient(115deg, var(--color-success) 0 5px, var(--color-warning) 5px 10px)',
}

interface HealthBarTrackProps {
  ownResource?: { kind: string; namespace: string; name: string }
  events: TimelineEvent[]
  /** When set, the row is a collapsed parent / app group: sweep its members'
   *  honest spans into state-family segments instead of blending its subtree
   *  events into one latest-event-wins timeline. */
  aggregateLane?: ResourceLane
  /** Dim + thin the aggregate to a "ghost" — an expanded app header keeps the
   *  rollup scannable without competing with the children below it. */
  ghost?: boolean
  startTime: number
  windowMs: number
  now: number
}

function HealthBarTrack({ events, startTime, windowMs, now, ownResource, aggregateLane, ghost }: HealthBarTrackProps) {
  if (aggregateLane) {
    return (
      <AggregateHealthTrack
        lane={aggregateLane}
        ghost={ghost}
        startTime={startTime}
        windowMs={windowMs}
        now={now}
      />
    )
  }
  // Filter to change events for health state computation
  const changeEvents = events.filter(e => isChangeEvent(e))

  // Build health spans from events
  const { spans, createdAt, createdBeforeWindow } = buildHealthSpans(
    changeEvents,
    startTime,
    now,
    events, // All events for createdAt extraction
    ownResource
  )

  // Every row gets a health line even with no derived spans (event dots
  // floating on nothing read as footnotes with no page). Paint a faint neutral
  // baseline so the row still has its line.
  if (spans.length === 0) {
    return <div className="absolute top-1/2 -translate-y-1/2 left-0 right-0 z-0 h-[7px] rounded-[2px] bg-gray-400/25" />
  }

  return (
    <div className="absolute inset-0 z-0">
      {spans.map((span, i) => {
        const left = sharedTimeToX(span.start, startTime, windowMs)
        const right = sharedTimeToX(span.end, startTime, windowMs)
        const width = right - left

        // Skip spans outside visible range
        if (right < 0 || left > 100) return null

        // Clamp to visible range
        const clampedLeft = Math.max(0, left)
        let clampedWidth = Math.min(100 - clampedLeft, width - (clampedLeft - left))

        if (clampedWidth <= 0) return null
        // A real span narrower than ~a pixel still deserves ink: a cron pod's
        // 3-second healthy run must read as a tick, not vanish entirely.
        clampedWidth = Math.max(clampedWidth, 0.12)

        const showCreatedBefore = createdBeforeWindow && i === 0 && createdAt != null
        // Positioning lives on the Tooltip wrapper so the span itself can fill it.
        return (
          <Tooltip
            key={i}
            content={`Health: ${span.health} · ${new Date(span.start).toLocaleTimeString()} - ${new Date(span.end).toLocaleTimeString()}`}
            wrapperClassName="absolute top-1/2 -translate-y-1/2 h-[7px]"
            wrapperStyle={{ left: `${clampedLeft}%`, width: `${clampedWidth}%` }}
          >
            <div className={clsx('relative h-full w-full', getHealthStripColor(span.health))}>
              {showCreatedBefore && (
                <span className="absolute bottom-[9px] left-0.5 text-[9px] text-theme-text-tertiary whitespace-nowrap pointer-events-none">
                  ← {formatCreatedBefore(new Date(createdAt!))}
                </span>
              )}
            </div>
          </Tooltip>
        )
      })}
    </div>
  )
}

// The collapsed-parent / app-group health strip: an interval sweep over each
// member's honest spans. A slice whose member states share one family paints
// that family's dominant state; a slice where families disagree paints the
// neutral 'mixed' texture. The tooltip names who's off.
function AggregateHealthTrack({ lane, startTime, windowMs, now, ghost }: {
  lane: ResourceLane
  startTime: number
  windowMs: number
  now: number
  ghost?: boolean
}) {
  const endTime = startTime + windowMs
  const segments = useMemo(() => {
    const members = buildLaneMemberSpans(lane, startTime, now)
    return sweepAggregateHealth(members, startTime, endTime)
  }, [lane, startTime, endTime, now])

  // Group headers get their line too — a sweep with no segments still
  // paints the faint neutral baseline (same as leaf rows) so event pills never
  // float on nothing.
  if (segments.length === 0) {
    return <div className={clsx('absolute top-1/2 -translate-y-1/2 left-0 right-0 z-0 rounded-[2px] bg-gray-400/25', ghost ? 'h-[4px]' : 'h-[7px]')} />
  }

  return (
    <div className={clsx('absolute inset-0 z-0', ghost && 'opacity-40')}>
      {segments.map((seg, i) => {
        const left = sharedTimeToX(seg.start, startTime, windowMs)
        const right = sharedTimeToX(seg.end, startTime, windowMs)
        if (right < 0 || left > 100) return null
        const clampedLeft = Math.max(0, left)
        let clampedWidth = Math.min(100 - clampedLeft, (right - left) - (clampedLeft - left))
        if (clampedWidth <= 0) return null
        clampedWidth = Math.max(clampedWidth, 0.12)
        return (
          <Tooltip
            key={i}
            content={formatAggregateHealthTooltip(seg)}
            wrapperClassName={ghost ? 'absolute top-1/2 -translate-y-1/2 h-[4px]' : 'absolute top-1/2 -translate-y-1/2 h-[7px]'}
            wrapperStyle={{ left: `${clampedLeft}%`, width: `${clampedWidth}%` }}
          >
            <div
              className={clsx('h-full w-full', !seg.mixed && getHealthStripColor(seg.health))}
              style={seg.mixed ? MIXED_HEALTH_STRIP_STYLE : undefined}
            />
          </Tooltip>
        )
      })}
    </div>
  )
}

// Relative "created before window" label (mirrors the shared HealthSpan helper).
function formatCreatedBefore(date: Date): string {
  const diffHours = (Date.now() - date.getTime()) / (1000 * 60 * 60)
  const diffDays = diffHours / 24
  if (diffDays < 1) return `${Math.round(diffHours)}h ago`
  if (diffDays < 7) return `${Math.round(diffDays)}d ago`
  return date.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

// Renders a lane's event markers, collapsing near-overlapping ones into "×N" pills.
function LaneEventMarkers({ events, timeToX, selectedEvent, onSelectCluster, small }: {
  events: TimelineEvent[]
  timeToX: (t: number) => number
  selectedEvent: TimelineEvent | null
  onSelectCluster: (cluster: TimelineEventCluster) => void
  small?: boolean
}) {
  const clusters = useMemo(() => {
    const positioned = events
      .map((event) => ({ event, x: timeToX(new Date(event.timestamp).getTime()) }))
      .filter(({ x }) => x >= 0 && x <= 100)
    return clusterEventsByPosition(positioned, CLUSTER_MIN_GAP_PCT)
  }, [events, timeToX])

  return (
    <div className="absolute inset-0 z-10">
      {clusters.map((cluster, i) => {
        const selected = !!selectedEvent && cluster.events.some((e) => e.id === selectedEvent.id)
        if (cluster.count === 1) {
          return (
            <EventMarker
              key={`m-${cluster.dominant.id}-${i}`}
              event={cluster.dominant}
              x={cluster.x}
              selected={selected}
              onClick={() => onSelectCluster(cluster)}
              small={small}
            />
          )
        }
        return (
          <ClusterPill
            key={`c-${cluster.dominant.id}-${i}`}
            cluster={cluster}
            selected={selected}
            onClick={() => onSelectCluster(cluster)}
            small={small}
          />
        )
      })}
    </div>
  )
}

// A collapsed "dominant-glyph ×N" pill. Clicking opens the drawer listing every
// member so none of the N is unreachable.
function ClusterPill({ cluster, selected, onClick, small }: {
  cluster: TimelineEventCluster
  selected?: boolean
  onClick: () => void
  small?: boolean
}) {
  const { lines, more } = clusterBreakdown(cluster.events)
  return (
    <Tooltip
      content={
        <div className="space-y-0.5">
          <div className="font-medium">{cluster.count} events</div>
          {lines.map((line) => (
            <div key={line.label} className="tabular-nums">
              {line.count}× {line.label}
            </div>
          ))}
          {more > 0 && <div className="text-theme-text-tertiary">…and {more} more</div>}
          <div className="text-theme-text-tertiary">click to browse</div>
        </div>
      }
      position="top"
      delay={100}
      wrapperClassName={clsx('absolute top-1/2 -translate-y-1/2 z-20', markerAnchor(cluster.x).anchorClass)}
      wrapperStyle={{ left: markerAnchor(cluster.x).left }}
    >
      <button
        aria-label={`Open ${cluster.count} clustered events`}
        className={clsx(
          'inline-flex items-center gap-1 rounded-full border border-theme-border bg-theme-surface shadow-theme-sm transition-transform',
          small ? 'px-1 py-px' : 'px-1.5 py-0.5',
          selected ? 'ring-2 ring-accent scale-105' : 'hover:scale-105'
        )}
        onClick={(e) => {
          e.stopPropagation()
          onClick()
        }}
      >
        {/* Red once per problem: a critical-dominant cluster shows the
            error glyph — one ⊘ ×N, not N shouting icons or a generic diamond. */}
        {isCriticalIssue(cluster.dominant)
          ? <Ban className={clsx('text-red-600 dark:text-red-400', small ? 'h-2.5 w-2.5' : 'h-3 w-3')} />
          : <MarkerGlyph shape={eventShape(cluster.dominant)} size={small ? 9 : 11} className={eventColorClass(cluster.dominant)} />}
        <span className="text-[11px] font-semibold tabular-nums text-theme-text-primary">×{cluster.count}</span>
      </button>
    </Tooltip>
  )
}

// Critical issue reasons that should be prominently highlighted with icons
// This should align with PROBLEMATIC_REASONS in resource-hierarchy.ts
const CRITICAL_ISSUE_REASONS = new Set([
  // Container state issues
  'BackOff', 'CrashLoopBackOff', 'Failed', 'Error',
  'OOMKilling', 'OOMKilled',
  'CreateContainerConfigError', 'CreateContainerError', 'RunContainerError',
  'InvalidImageName', 'ErrImagePull', 'ImagePullBackOff',
  'ContainerStatusUnknown',

  // Pod scheduling/lifecycle issues
  'FailedScheduling', 'FailedMount', 'FailedAttachVolume',
  'FailedCreate', 'FailedDelete', 'Unhealthy', 'Killing', 'Evicted',
  'FailedSync', 'FailedValidation',
  'FailedPreStopHook', 'FailedPostStartHook',
  'HostPortConflict', 'InsufficientMemory', 'InsufficientCPU',

  // Node conditions
  'NodeNotReady', 'NetworkNotReady', 'KubeletNotReady',
  'MemoryPressure', 'DiskPressure', 'PIDPressure',
  'NodeStatusUnknown',

  // Deployment/workload issues
  'ProgressDeadlineExceeded', 'ReplicaFailure',
  'MinimumReplicasUnavailable',

  // HPA issues
  'FailedGetScale', 'FailedRescale', 'FailedUpdateScale',
  'FailedGetResourceMetric', 'FailedComputeMetricsReplicas',

  // PVC/storage issues
  'ProvisioningFailed', 'FailedBinding', 'VolumeFailedDelete',

  // Job issues
  'DeadlineExceeded', 'BackoffLimitExceeded',
])

// Get the appropriate icon for a critical issue
function getIssueIcon(reason: string | undefined): React.ComponentType<{ className?: string }> | null {
  if (!reason) return null

  // Memory issues (OOM)
  if (reason === 'OOMKilled' || reason === 'OOMKilling' ||
      reason === 'InsufficientMemory' || reason === 'MemoryPressure') return MemoryStick

  // Crash/restart issues
  if (reason === 'CrashLoopBackOff' || reason === 'BackOff') return RefreshCw

  // Image pull issues
  if (reason === 'ImagePullBackOff' || reason === 'ErrImagePull' || reason === 'InvalidImageName') return Package

  // Container creation/runtime errors
  if (reason === 'CreateContainerConfigError' || reason === 'CreateContainerError' ||
      reason === 'RunContainerError' || reason === 'ContainerStatusUnknown') return Box

  // Scheduling/mount/node issues
  if (reason === 'FailedScheduling' || reason === 'FailedMount' || reason === 'FailedAttachVolume' ||
      reason === 'NodeNotReady' || reason === 'NetworkNotReady' || reason === 'KubeletNotReady' ||
      reason === 'NodeStatusUnknown' || reason === 'HostPortConflict') return Ban

  // Resource pressure (disk, CPU, PID)
  if (reason === 'DiskPressure' || reason === 'PIDPressure' || reason === 'InsufficientCPU') return Gauge

  // Deployment rollout issues
  if (reason === 'ProgressDeadlineExceeded' || reason === 'ReplicaFailure' ||
      reason === 'MinimumReplicasUnavailable') return RotateCcw

  // HPA scaling issues
  if (reason === 'FailedGetScale' || reason === 'FailedRescale' || reason === 'FailedUpdateScale' ||
      reason === 'FailedGetResourceMetric' || reason === 'FailedComputeMetricsReplicas') return Gauge

  // PVC/storage issues
  if (reason === 'ProvisioningFailed' || reason === 'FailedBinding' || reason === 'VolumeFailedDelete') return HardDrive

  // Job timeout issues
  if (reason === 'DeadlineExceeded' || reason === 'BackoffLimitExceeded') return Timer

  // Probe failures and general unhealthy
  if (reason === 'Unhealthy') return AlertTriangle

  // General failures - use warning circle
  if (reason.startsWith('Failed') || reason === 'Evicted' || reason === 'Killing' || reason === 'Error') return AlertCircle

  return null
}

// Check if event is a critical issue that deserves special highlighting
function isCriticalIssue(event: TimelineEvent): boolean {
  return !!(event.reason && CRITICAL_ISSUE_REASONS.has(event.reason))
}

interface EventMarkerProps {
  event: TimelineEvent
  x: number
  selected?: boolean
  onClick: () => void
  dimmed?: boolean // For aggregated child events
  small?: boolean // For child lane events
}

function EventMarker({ event, x, selected, onClick, dimmed, small }: EventMarkerProps) {
  const isChange = isChangeEvent(event)
  const isProblematic = isProblematicEvent(event) // Includes warnings + problematic reasons like BackOff
  const isHistorical = isHistoricalEvent(event)
  const isCritical = isCriticalIssue(event)
  const IssueIcon = getIssueIcon(event.reason)

  const getMarkerStyle = () => {
    // Historical events use outline style (border instead of fill)
    // Non-historical use solid fill
    if (isHistorical) {
      // Outline style for historical - visible border, subtle background
      if (isProblematic) {
        return 'bg-amber-500/20 border-2 border-dashed border-amber-500/60'
      }
      if (isChange) {
        switch (event.eventType) {
          case 'add':
            return 'bg-green-500/20 border-2 border-dashed border-green-500/60'
          case 'delete':
            return 'bg-red-500/20 border-2 border-dashed border-red-500/60'
          case 'update':
            return 'bg-skyhook-500/20 border-2 border-dashed border-skyhook-500/60'
        }
      }
      return 'bg-theme-hover/30 border-2 border-dashed border-theme-border-light'
    }

    // Critical issues get red background to stand out
    if (isCritical) {
      return 'bg-red-500'
    }

    // Solid fill for real-time events.
    // Problematic events (warnings, BackOff, etc.) are always amber/orange.
    if (isProblematic) {
      return dimmed ? 'bg-amber-500/50' : 'bg-amber-500'
    }
    if (isChange) {
      switch (event.eventType) {
        case 'add':
          return dimmed ? 'bg-green-500/50' : 'bg-green-500'
        case 'delete':
          return dimmed ? 'bg-red-500/50' : 'bg-red-500'
        case 'update':
          return dimmed ? 'bg-blue-500/50' : 'bg-blue-500'
      }
    }
    return dimmed ? 'bg-theme-text-tertiary/50' : 'bg-theme-text-tertiary'
  }

  const markerClasses = getMarkerStyle()

  // Build tooltip text - focus on what happened, explain the color meaning
  const getRelativeTime = (timestamp: string) => {
    const diff = Date.now() - new Date(timestamp).getTime()
    const mins = Math.floor(diff / 60000)
    if (mins < 1) return 'just now'
    if (mins < 60) return `${mins}m ago`
    const hours = Math.floor(mins / 60)
    if (hours < 24) return `${hours}h ago`
    return `${Math.floor(hours / 24)}d ago`
  }

  // Get human-readable operation label with color indicator
  const getOperationLabel = () => {
    if (isProblematic) {
      return `⚠ ${event.reason || 'Warning'}`
    }
    if (isChange) {
      switch (event.eventType) {
        case 'add': return '● Created'
        case 'delete': return '● Deleted'
        case 'update': return '● Modified'
        default: return '● Changed'
      }
    }
    if (event.reason) {
      return `● ${event.reason}`
    }
    return '● Event'
  }

  const tooltipLines: string[] = []
  tooltipLines.push(getOperationLabel())
  if (event.message) {
    // Truncate long messages — the tooltip is a scannable preview, the drawer
    // carries the full text. Wide enough to keep a k8s event's gist (reason +
    // first clause) without spilling into a paragraph.
    const msg = event.message.length > 110 ? event.message.slice(0, 110) + '…' : event.message
    tooltipLines.push(msg)
  }
  tooltipLines.push(getRelativeTime(event.timestamp))
  if (isHistoricalEvent(event)) tooltipLines.push('(from metadata)')

  const tooltipText = tooltipLines.join(' · ')

  // Critical issues get larger markers with icons
  if (isCritical && IssueIcon && !small) {
    return (
      <Tooltip
        content={tooltipText}
        position="top"
        delay={100}
        className="max-w-sm!"
        wrapperClassName={clsx('absolute top-1/2 -translate-y-1/2 z-20', markerAnchor(x).anchorClass)}
        wrapperStyle={{ left: markerAnchor(x).left }}
      >
        <button
          // Icon-only marker: the tooltip is mouse-only, so mirror it into an
          // accessible name (event type · message · time) for screen readers.
          aria-label={tooltipText}
          className={clsx(
            'rounded-full transition-all flex items-center justify-center',
            'w-5 h-5',
            markerClasses,
            selected ? 'ring-2 ring-white ring-offset-2 ring-offset-theme-base scale-125' : 'hover:scale-110',
            'shadow-sm'
          )}
          onClick={(e) => {
            e.stopPropagation()
            onClick()
          }}
        >
          <IssueIcon className="w-3 h-3 text-white" />
        </button>
      </Tooltip>
    )
  }

  // Shape-coded glyph (▲ created / ● modified / ▼ deleted / ◆ warning / ○ historical).
  const shape = eventShape(event)
  const size = small ? 11 : 13

  return (
    <Tooltip
      content={tooltipText}
      position="top"
      delay={100}
      className="max-w-sm!"
      wrapperClassName={clsx(
        'absolute top-1/2 -translate-y-1/2',
        markerAnchor(x).anchorClass,
        isHistorical ? 'z-5' : 'z-10'
      )}
      wrapperStyle={{ left: markerAnchor(x).left }}
    >
      <button
        // Icon-only glyph: the tooltip is mouse-only, so mirror it into an
        // accessible name (event type · message · time) for screen readers.
        aria-label={tooltipText}
        className={clsx(
          'flex items-center justify-center transition-transform',
          selected ? 'scale-150' : 'hover:scale-125'
        )}
        style={{ width: `${size + 4}px`, height: `${size + 4}px` }}
        onClick={(e) => {
          e.stopPropagation()
          onClick()
        }}
      >
        <MarkerGlyph shape={shape} size={size} className={clsx(eventColorClass(event), selected && 'drop-shadow')} />
      </button>
    </Tooltip>
  )
}

interface EventDetailPanelProps {
  // The drawer's events. Length 1 = a single marker (today's exact panel);
  // length > 1 = a cluster pill's members, listed so none is unreachable.
  events: TimelineEvent[]
  selectedId: string
  onSelectId: (id: string) => void
  onClose: () => void
  onResourceClick?: NavigateToResource
  // Every event in view — powers the ±15-min correlation feed ("what else
  // happened around this deploy?"). Absent → the correlation section is omitted.
  allEvents?: TimelineEvent[]
}

// The ±15-min window a single event's rail pulls correlated neighbours from
// (deploy ↔ incident debugging — "what else happened around this moment?").
const CORRELATION_WINDOW_MS = 15 * 60_000

// One row in the drawer's rail: glyph (severity tone via color) +
// reason/type + owning resource + time. Reuses the marker glyph vocabulary so the
// list reads the same as the track it summarizes.
function ClusterEventRow({ event, active, onClick }: { event: TimelineEvent; active: boolean; onClick: () => void }) {
  const isProblematic = isProblematicEvent(event)
  return (
    <li>
      <button
        type="button"
        onClick={onClick}
        aria-current={active}
        className={clsx(
          'w-full flex items-center gap-2 px-3 py-1.5 text-left border-l-2 transition-colors',
          active
            ? 'border-accent bg-theme-elevated'
            : 'border-transparent hover:bg-theme-hover',
        )}
      >
        <span className="inline-flex w-3 h-3 shrink-0 items-center justify-center">
          <MarkerGlyph shape={eventShape(event)} size={10} className={eventColorClass(event)} />
        </span>
        <span className="flex-1 min-w-0">
          <span className={clsx('block text-xs font-medium truncate', isProblematic ? 'text-amber-700 dark:text-amber-300' : 'text-theme-text-primary')}>
            {event.reason || event.eventType}
          </span>
          <span className="block text-[11px] text-theme-text-tertiary truncate font-mono">{event.name}</span>
        </span>
        <span className="text-[11px] text-theme-text-tertiary tabular-nums shrink-0">
          {formatAxisTime(new Date(event.timestamp))}
        </span>
      </button>
    </li>
  )
}

export function EventDetailPanel({ events, selectedId, onSelectId, onClose, onResourceClick, allEvents }: EventDetailPanelProps) {
  // ONE drawer anatomy: rail left, detail right — for one event or
  // fifty. The shape never changes with count, so muscle memory holds. A single
  // clicked dot renders as a rail of one plus its ±15-min correlated neighbors,
  // so "what else happened around this?" is a click away, not a separate feed.
  const railEvents = useMemo(() => {
    if (events.length !== 1 || !allEvents) return events
    const origin = events[0]
    const t0 = new Date(origin.timestamp).getTime()
    const neighbors = allEvents
      .filter((e) => e.id !== origin.id && Math.abs(new Date(e.timestamp).getTime() - t0) <= CORRELATION_WINDOW_MS)
      .sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime())
      .slice(0, 30)
    return [origin, ...neighbors]
  }, [events, allEvents])

  // Opens just tall enough for one event (header + rail + property grid) —
  // the timeline above is the main surface, so the drawer defaults out of its
  // way; drag the top edge when a long message or diff needs room. No backdrop
  // — the timeline stays interactive, so the next dot is clickable without
  // closing.
  const [heightPx, setHeightPx] = useState(272)
  const resizeRef = useRef<{ startY: number; startH: number } | null>(null)
  useEffect(() => {
    const onMove = (e: PointerEvent) => {
      const r = resizeRef.current
      if (!r) return
      const max = Math.round(window.innerHeight * 0.8)
      setHeightPx(Math.min(max, Math.max(240, r.startH + (r.startY - e.clientY))))
    }
    const onUp = () => { resizeRef.current = null }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
  }, [])

  const selected = railEvents.find((e) => e.id === selectedId) ?? events[0]
  const selIdx = railEvents.findIndex((e) => e.id === selected.id)
  const step = (dir: 1 | -1) => {
    const next = railEvents[selIdx + dir]
    if (next) onSelectId(next.id)
  }
  const isProblematic = isProblematicEvent(selected)
  const isHistorical = isHistoricalEvent(selected)
  // Index where correlated neighbors start (single-origin rails only) — a
  // divider separates "the dot you clicked" from "what happened around it".
  const neighborsFrom = events.length === 1 && railEvents.length > 1 ? 1 : -1
  const openResource = () =>
    onResourceClick?.({ kind: kindToPlural(selected.kind), namespace: selected.namespace, name: selected.name, group: apiVersionToGroup(selected.apiVersion) })

  return (
    <div
      className="fixed bottom-0 left-0 right-0 z-50 flex flex-col border-t border-theme-border bg-theme-surface shadow-theme-lg"
      style={{ height: heightPx }}
    >
      {/* Resize handle — drag the top edge. */}
      <div
        onPointerDown={(e) => { e.preventDefault(); resizeRef.current = { startY: e.clientY, startH: heightPx } }}
        className="group absolute -top-1.5 left-0 right-0 flex h-3 cursor-ns-resize items-center justify-center"
        aria-label="Drag to resize"
      >
        <span className="h-1 w-10 rounded-full bg-theme-border opacity-0 transition-opacity group-hover:opacity-100" />
      </div>

      {/* Header echoes the lit dot — glyph + reason + resource + time — and
          ‹ › steps through the rail without touching the timeline. */}
      <div className="flex shrink-0 items-center justify-between gap-3 border-b border-theme-border px-4 py-2">
        <div className="flex min-w-0 items-center gap-2 text-sm">
          <span className="inline-flex h-4 w-4 shrink-0 items-center justify-center">
            <MarkerGlyph shape={eventShape(selected)} size={12} className={eventColorClass(selected)} />
          </span>
          <span className={clsx('shrink-0 font-medium', isProblematic ? 'text-amber-700 dark:text-amber-300' : 'text-theme-text-primary')}>
            {selected.reason || selected.eventType}
          </span>
          <span className="badge-sm shrink-0 bg-theme-elevated text-theme-text-secondary">{displayKind(selected.kind) || 'Event'}</span>
          <span className="min-w-0 truncate font-medium text-theme-text-primary">{selected.name}</span>
          {selected.namespace && <span className="shrink-0 text-xs text-theme-text-tertiary">in {selected.namespace}</span>}
          <span className="shrink-0 text-xs tabular-nums text-theme-text-tertiary">{formatFullTime(new Date(selected.timestamp))}</span>
          {events.length > 1 && (
            <span className="badge-sm shrink-0 bg-theme-elevated text-theme-text-secondary">{events.length} events</span>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <button
            onClick={() => step(-1)}
            disabled={selIdx <= 0}
            className="rounded p-1 text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary disabled:opacity-30"
            aria-label="Previous event"
          >
            <ChevronLeft className="h-4 w-4" />
          </button>
          <span className="text-xs tabular-nums text-theme-text-tertiary">{`${selIdx + 1} of ${railEvents.length}`}</span>
          <button
            onClick={() => step(1)}
            disabled={selIdx >= railEvents.length - 1}
            className="rounded p-1 text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary disabled:opacity-30"
            aria-label="Next event"
          >
            <ChevronRight className="h-4 w-4" />
          </button>
          <Tooltip content="Close (Esc)">
            <button
              onClick={onClose}
              aria-label="Close"
              className="ml-1 rounded p-1.5 text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary"
            >
              <X className="h-4 w-4" />
            </button>
          </Tooltip>
        </div>
      </div>

      <div className="flex min-h-0 flex-1">
        {/* The rail — always renders, even as a rail of one. */}
        <ul className="w-72 shrink-0 overflow-auto border-r border-theme-border py-1" aria-label="Drawer events">
          {railEvents.map((ev, i) => (
            <Fragment key={ev.id}>
              {i === neighborsFrom && (
                <li aria-hidden className="mt-1 border-t border-theme-border px-3 pb-1 pt-2 text-[10px] font-bold uppercase tracking-[0.07em] text-theme-text-tertiary">
                  Within ±15 min
                </li>
              )}
              <ClusterEventRow event={ev} active={ev.id === selected.id} onClick={() => onSelectId(ev.id)} />
            </Fragment>
          ))}
        </ul>

        {/* Detail pane: fields as properties, then message, diff, actions. */}
        <div className="min-w-0 flex-1 overflow-auto p-4">
          <dl className="grid grid-cols-[96px_1fr] items-baseline gap-x-3 gap-y-2 text-sm">
            <dt className="text-xs text-theme-text-tertiary">Reason</dt>
            <dd className="flex flex-wrap items-center gap-2">
              <span className={clsx(
                'font-medium',
                isProblematic ? 'text-amber-700 dark:text-amber-300' : 'text-theme-text-primary',
              )}>
                {selected.reason || selected.eventType}
              </span>
              {!isChangeEvent(selected) && selected.eventType && (
                <span className={clsx('badge-sm', getEventTypeColor(selected.eventType))}>{selected.eventType}</span>
              )}
              {selected.count && selected.count > 1 && (
                <span className="text-xs text-theme-text-tertiary">×{selected.count}</span>
              )}
            </dd>
            <dt className="text-xs text-theme-text-tertiary">Resource</dt>
            <dd className="flex min-w-0 flex-wrap items-center gap-1.5">
              {/* The ONE click target for the resource — the same badge used by
                  every Radar drawer, so it reads as "this navigates". */}
              <ResourceRefBadge
                resourceRef={{ kind: selected.kind || 'Event', namespace: selected.namespace ?? '', name: selected.name }}
                onClick={onResourceClick ? openResource : undefined}
              />
              {selected.namespace && <span className="text-xs text-theme-text-tertiary">in {selected.namespace}</span>}
            </dd>
            <dt className="text-xs text-theme-text-tertiary">Time</dt>
            <dd className="flex items-center gap-2 tabular-nums">
              {formatFullTime(new Date(selected.timestamp))}
              {isHistorical && (
                <span className="badge-sm bg-theme-hover text-theme-text-secondary">
                  <Clock className="h-3 w-3" />
                  from metadata
                </span>
              )}
            </dd>
            {selected.healthState && selected.healthState !== 'unknown' && (
              <>
                <dt className="text-xs text-theme-text-tertiary">Health</dt>
                <dd><span className={clsx('badge-sm', getHealthBadgeColor(selected.healthState))}>{selected.healthState}</span></dd>
              </>
            )}
          </dl>
          {selected.message && (
            <p className={clsx('mt-3 text-sm', isProblematic ? 'text-amber-700 dark:text-amber-200' : 'text-theme-text-secondary')}>
              {selected.message}
            </p>
          )}
          {selected.diff && (
            <div className="mt-3">
              <DiffViewer diff={selected.diff} />
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
