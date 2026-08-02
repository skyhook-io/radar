// Timeline data-source abstraction.
//
// Radar's timeline can be backed by two stores:
//   - 'local'    — the in-process event store the Radar binary keeps (default,
//                  OSS standalone). Fetched via GET {apiBase}/timeline/events.
//   - 'retained' — a longer-horizon history store answered upstream of Radar
//                  (relative to apiBase) as GET {apiBase}/timeline/events and
//                  GET {apiBase}/timeline/overview. This is an extension point:
//                  the standalone binary never selects it; a host that embeds
//                  RadarApp behind a proxy that serves retained history opts in
//                  via the `timelineSource` prop.
//
// Both sources expose the same `useEvents(query)` hook shape so the timeline
// wrappers stay source-agnostic: pick the source from context, call useEvents.
import { useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LIVE_TICK_MS } from '@skyhook-io/k8s-ui'
import { apiFetch, mergeDeltaEvents, ApiError, type UseChangesOptions } from './client'
import { apiUrl, getApiBase } from './config'
import type { TimelineEvent, TimeRange } from '../types'

// The query the wrappers pass. Superset of the local store's params so most
// call sites don't change shape when switching sources.
export type TimelineQuery = UseChangesOptions & {
  // Explicit [from,to] window in epoch-ms. When both are set the retained
  // source loads exactly this window (the scrubber's brush selection) instead
  // of deriving one from `timeRange`. The local source can't express a frozen
  // past window server-side, so it loads the whole ring and bounds it to
  // [from,to] client-side.
  fromMs?: number
  toMs?: number
}

export interface TimelineSourceCapabilities {
  mode: 'local' | 'retained'
  // The server's per-request row ceiling for this source — the number the
  // truncation copy must cite (the local binary pages at 10k, a retained
  // backend at 50k).
  ringLimit: number
  // Only meaningful for 'retained': the maximum lookback the retained backend
  // serves. Clamps EVERY derived range (rangeSpanMs, not just 'all'), the
  // from-edge of an explicit [from,to] window, and the scrubber's selectable
  // domain — nothing can reach further back than this. Defaults to
  // DEFAULT_RETAINED_MAX_RANGE_DAYS when unset.
  maxRangeDays?: number
}

// A single window [from,to] in epoch-ms. Cluster identity is implicit in the
// configured apiBase path, so no cluster id is carried here.
export interface TimelineRange {
  from: number
  to: number
}

// Coverage lines emitted inline in the retained events stream (gaps / retention
// boundaries). Shape is owned by the retained backend; kept opaque here and
// surfaced on the result so a future UI can render coverage without another
// round-trip. Not consumed by the timeline wrappers today.
export interface TimelineCoverageRecord {
  type: 'coverage'
  [key: string]: unknown
}

// A recording-coverage span within an overview bucket. Event-time bounds are
// owned by the retained (hub) backend; on the wire a bucket may carry one span
// or several — fetchRetainedOverview normalizes both to an array so consumers
// only ever see TimelineCoverageSpan[].
export interface TimelineCoverageSpan {
  eventTimeStartMs?: number
  eventTimeEndMs?: number
}

export interface TimelineOverviewBucket {
  // Bucket start in epoch-ms — hourly for the retained rollup, sub-hour when the
  // local overview rebuckets finer. Named for what it is, not its granularity.
  startMs: number
  summary: {
    total: number
    adds: number
    updates: number
    deletes: number
    warnings: number
    // Server-owned health rollup. localOverviewFromEvents only emits
    // 'healthy'/'unhealthy', but the retained (hub) rollup can carry the full
    // HealthLevel vocabulary, so this stays an open string.
    worstHealth: string
    namespaces: string[]
  }
  coverage?: TimelineCoverageSpan[]
}

// Overview response envelope: the hourly `buckets` plus `availableFromMs` (the
// oldest event-time the server holds for this cluster) so the scrubber domain
// reflects real retention.
export interface TimelineOverviewResult {
  buckets: TimelineOverviewBucket[]
  availableFromMs?: number
}

export interface TimelineEventsResult {
  data: TimelineEvent[] | undefined
  isLoading: boolean
  // Broad "is loading" signal: any fetch for the requested data is in flight,
  // INCLUDING routine background polls (every ~10s on the retained source).
  // Do not drive user-visible loaders from this — they would flicker on every
  // poll; use isLoading (true only with no data at all) for loaders. Period
  // changes re-window the loaded ring client-side in both sources, so they
  // never fetch and never signal either flag; the one exception is a frozen
  // [from,to] selection reaching below a truncated ring, which fetches its
  // own window once and signals both flags while it does.
  isFetching: boolean
  isError: boolean
  // The failure behind isError, when one is available. Surfaced so the UI can
  // show the server's actionable message (e.g. the retained row-cap "narrow the
  // from/to range") instead of a generic "failed to load".
  error?: Error | null
  refetch: () => void
  // Present only for sources that report coverage (retained).
  coverage?: TimelineCoverageRecord[]
  // The load serving the data was row-capped, so the OLDEST part of what it
  // asked for is not loaded — the ring load when the ring serves, the window
  // load when a frozen selection fetched its own window. Hosts must surface
  // this — rendering a truncated result without a note reads as "nothing
  // older happened".
  truncated?: boolean
}

export interface TimelineSource {
  capabilities: TimelineSourceCapabilities
  useEvents: (query: TimelineQuery) => TimelineEventsResult
  // Optional hourly rollup. Only the retained source implements it.
  fetchOverview?: (range: TimelineRange) => Promise<TimelineOverviewResult>
}

// Config carried by the RadarApp `timelineSource` prop. Absent = local.
export interface TimelineSourceConfig {
  mode: 'retained'
  maxRangeDays?: number
}

// ============================================================================
// Local source — the same ring-and-delta hook as retained, pointed at the
// Radar binary's own /timeline/events endpoint. One mechanism, two depths.
// ============================================================================

// The local binary's per-request row ceiling (its /timeline/events pages at
// 10k, matching the default in-process ring size).
const LOCAL_RING_LIMIT = 10000

// The local anti-entropy cadence: a full ring reload is a cheap same-host
// request, so server-side evictions surface within minutes.
const LOCAL_FULL_RESYNC_MS = 5 * 60 * 1000

const LOCAL_HOUR_MS = 60 * 60 * 1000

interface LocalHourSlot {
  total: number
  adds: number
  updates: number
  deletes: number
  warnings: number
  namespaces: Set<string>
}

// Client-side overview for the local source. The Radar binary loads the whole
// event ring into the browser (the swimlane's 10k fetch), so the scrubber
// histogram is derived here instead of from a server rollup — the local store
// has no /timeline/overview endpoint. Buckets by hour to match the retained
// overview shape the scrubber host already groups.
//
// `availableFromMs` is the oldest event time held in the ring: the scrubber
// domain floor comes from whoever holds the data, exactly as the retained
// source reports its own retention floor.
//
// `bucketSizeMs` defaults to the retained rollup's hourly granularity; a
// tightly framed strip passes a sub-hour size to rebucket the same events
// finer. The bucket's `startMs` is its start regardless of size — hourly or not.
//
// No coverage/gap field is emitted. Coverage is a retention concept — the hub
// records what it missed while not watching. Locally we cannot know what
// happened during downtime, so claiming a gap would be dishonest; we omit it.
export function localOverviewFromEvents(events: TimelineEvent[], bucketSizeMs = LOCAL_HOUR_MS): TimelineOverviewResult {
  const slots = new Map<number, LocalHourSlot>()
  let oldest = Number.POSITIVE_INFINITY

  for (const e of events) {
    const t = new Date(e.timestamp).getTime()
    if (!Number.isFinite(t)) continue
    if (t < oldest) oldest = t
    const hour = Math.floor(t / bucketSizeMs) * bucketSizeMs
    let slot = slots.get(hour)
    if (!slot) {
      slot = { total: 0, adds: 0, updates: 0, deletes: 0, warnings: 0, namespaces: new Set() }
      slots.set(hour, slot)
    }
    slot.total++
    if (e.eventType === 'add') slot.adds++
    else if (e.eventType === 'update') slot.updates++
    else if (e.eventType === 'delete') slot.deletes++
    if (e.eventType === 'Warning') slot.warnings++
    if (e.namespace) slot.namespaces.add(e.namespace)
  }

  const buckets: TimelineOverviewBucket[] = [...slots.entries()]
    .map(([startMs, s]) => ({
      startMs,
      summary: {
        total: s.total,
        adds: s.adds,
        updates: s.updates,
        deletes: s.deletes,
        warnings: s.warnings,
        worstHealth: s.warnings > 0 ? 'unhealthy' : 'healthy',
        namespaces: [...s.namespaces],
      },
    }))
    .sort((a, b) => a.startMs - b.startMs)

  return { buckets, availableFromMs: Number.isFinite(oldest) ? oldest : undefined }
}

// ============================================================================
// Retained source — streams NDJSON from {apiBase}/timeline/events.
// ============================================================================

const HOUR_MS = 60 * 60 * 1000
const DAY_MS = 24 * HOUR_MS

// Bound for the 'all' range when the host doesn't specify maxRangeDays.
const DEFAULT_RETAINED_MAX_RANGE_DAYS = 7

// The retained source is the SAME accumulate model as the local source,
// pointed at the hub: one ring load (the newest RETAINED_RING_LIMIT events
// over the retention depth), then a poll for everything INGESTED since a
// server-issued cursor. Ingestion order is what makes the poll complete —
// a late-arriving or revised event has an old event time but a fresh
// ingestion time, so it rides the same poll as brand-new events. Period
// changes re-window the loaded ring client-side; no fetch.

// Matches the hub's per-response row cap. A window holding more arrives as
// the newest RETAINED_RING_LIMIT events with `truncated` set — ring
// semantics, exactly like the local source's capped ring, never a silent cut.
export const RETAINED_RING_LIMIT = 50_000

// Depth ceiling of the initial ring load; the hub rejects wider windows.
const RETAINED_MAX_DEPTH_DAYS = 31

// Delta poll cadence. Same order as the local source's SSE-nudged refetches.
const RETAINED_POLL_MS = 10_000

// A row-capped delta page sets `more`; the fetch pages forward immediately,
// bounded so a pathological feed can't spin a single poll forever.
const RETAINED_DELTA_MAX_PAGES = 10

// Anti-entropy: a periodic full ring reload, the retained twin of the local
// source's FULL_RESYNC_MS. Catches what deltas structurally can't — server-
// side deletions, coverage-gap updates, and a stale truncated flag. Hourly,
// not the local 5 minutes: a full ring is a heavy transfer and the delta feed
// carries revisions, so entropy accumulates far slower here.
const RETAINED_FULL_RESYNC_MS = 60 * 60 * 1000

// Client-vs-hub clock skew allowance, applied at both edges of the ring:
// the initial window's recent edge extends past the client clock by this
// slack (a clock BEHIND the hub would otherwise exclude already-ingested
// events in (clientNow, hubNow] — and the delta cursor already covers them,
// so no poll would ever deliver them; the window slides back by the same
// slack to stay within the hub's maximum range), and the delta-merge prune
// floor sits below the retention depth by the same slack (a clock AHEAD of
// the hub would otherwise prune oldest events the hub still retains).
export const RETAINED_CLOCK_SKEW_SLACK_MS = 5 * 60 * 1000

// How often a preset-derived window's sliding lower bound refreshes on an
// IDLE ring (the no-op cached return keeps ring identity stable, so the memo
// otherwise never re-samples the clock and a "24h" label slowly overstays).
const RETAINED_PRESET_TICK_MS = 5 * 60 * 1000

// The client-side spans the `timeRange` presets resolve to when no explicit
// [from,to] window rides the query — the retained twin of the local source's
// server-side `since` resolution. Preset arms return their nominal span; only
// 'all'/unset falls back to the ring depth — clamping a preset wider than a
// shallow host is the CALLER's job (Math.min at the use site). Exported for
// unit tests; not re-exported publicly.
export function rangeSpanMs(range: TimeRange | undefined, capMs?: number): number | undefined {
  switch (range) {
    case '5m': return 5 * 60 * 1000
    case '30m': return 30 * 60 * 1000
    case '1h': return HOUR_MS
    case '6h': return 6 * HOUR_MS
    case '24h': return 24 * HOUR_MS
    case '7d': return 7 * DAY_MS
    case '30d': return 30 * DAY_MS
    default: return capMs
  }
}

interface RetainedWindowResult {
  events: TimelineEvent[]
  coverage: TimelineCoverageRecord[]
  // Next delta cursor (opaque, server-issued). Absent when talking to a hub
  // that predates the delta feed.
  cursor?: string
  // Delta page was row-capped; poll again immediately from `cursor`.
  more: boolean
  // Ring load shipped only the newest slice of the window.
  truncated: boolean
}

type TerminalRecord =
  | { type: 'end'; cursor?: string; more?: boolean; truncated?: boolean }
  | { type: 'error'; message?: string }

// De-dupe by id keeping the LAST occurrence — a later revision of an event
// replaces the earlier one.
function dedupeById(events: TimelineEvent[]): TimelineEvent[] {
  const byId = new Map<string, TimelineEvent>()
  for (const e of events) byId.set(e.id, e)
  return Array.from(byId.values())
}

// Shared NDJSON stream reader for both events endpoints (window and delta).
async function fetchRetainedStream(path: string, signal?: AbortSignal): Promise<RetainedWindowResult> {
  const res = await apiFetch(apiUrl(path), signal ? { signal } : undefined)
  if (!res.ok) {
    const errorData = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new ApiError(errorData.error || `HTTP ${res.status}`, res.status, errorData)
  }
  if (!res.body) {
    throw new Error('timeline stream has no body')
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  const events: TimelineEvent[] = []
  const coverage: TimelineCoverageRecord[] = []
  let terminal: TerminalRecord | null = null
  let buf = ''

  const handleLine = (line: string): void => {
    const trimmed = line.trim()
    if (!trimmed) return
    const rec = JSON.parse(trimmed) as { type?: string; message?: string; cursor?: string; more?: boolean; truncated?: boolean }
    if (rec.type === 'end') {
      terminal = { type: 'end', cursor: rec.cursor, more: rec.more, truncated: rec.truncated }
    } else if (rec.type === 'error') {
      terminal = { type: 'error', message: rec.message }
    } else if (rec.type === 'coverage') {
      coverage.push(rec as TimelineCoverageRecord)
    } else {
      events.push(rec as unknown as TimelineEvent)
    }
  }

  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    let idx: number
    while ((idx = buf.indexOf('\n')) >= 0) {
      handleLine(buf.slice(0, idx))
      buf = buf.slice(idx + 1)
    }
  }
  handleLine(buf)

  // Absence of a terminal record means the response was truncated — treat as a
  // failure so the UI doesn't render a partial window as complete.
  if (!terminal) {
    throw new Error('timeline stream truncated (missing terminal record)')
  }
  const end = terminal as TerminalRecord
  if (end.type === 'error') {
    throw new Error(end.message || 'timeline stream error')
  }
  return {
    events: dedupeById(events),
    coverage,
    cursor: end.cursor,
    more: end.more === true,
    truncated: end.truncated === true,
  }
}

// The ring load: newest `limit` events overlapping [from, to], plus the delta
// cursor to continue from. Exported for unit tests; not re-exported publicly.
export async function fetchRetainedWindow(
  from: number,
  to: number,
  signal?: AbortSignal,
  limit?: number,
  namespaces?: string[],
): Promise<RetainedWindowResult> {
  const limitParam = limit ? `&limit=${limit}` : ''
  return fetchRetainedStream(
    `/timeline/events?from=${Math.round(from)}&to=${Math.round(to)}${limitParam}${namespacesParam(namespaces)}`,
    signal,
  )
}

// A namespace-scoped consumer must scope the SERVER query, not just the
// client filter: the ring is row-capped, so an all-namespace ring on a busy
// cluster could spend its whole budget on other namespaces' events. A server
// that scopes only by time ignores the parameter and the client filter still
// applies.
function namespacesParam(namespaces?: string[]): string {
  if (!namespaces || namespaces.length === 0) return ''
  return `&namespaces=${encodeURIComponent(namespaces.join(','))}`
}

// The delta poll: everything ingested since the cursor, in ingestion order.
// Exported for unit tests; not re-exported publicly.
export async function fetchRetainedDelta(
  cursor: string,
  signal?: AbortSignal,
  namespaces?: string[],
): Promise<RetainedWindowResult> {
  return fetchRetainedStream(
    `/timeline/events?since=${encodeURIComponent(cursor)}${namespacesParam(namespaces)}`,
    signal,
  )
}

// Mirrors the Go store's TimelineEvent.IsManaged (pkg/timeline/types.go):
// a resource managed by another — owned, or one of the churn kinds. Keep the
// two predicates in lockstep.
function isManagedTimelineEvent(e: TimelineEvent): boolean {
  return e.owner != null || e.kind === 'ReplicaSet' || e.kind === 'Pod' || e.kind === 'Event'
}

// The retained endpoint scopes only by [from,to] (cluster is implicit in the
// apiBase path), so the store-side query params the local endpoint honors are
// applied client-side over the loaded window. Exported for unit tests; not part
// of the package's public surface.
export function applyClientFilters(events: TimelineEvent[], query: TimelineQuery): TimelineEvent[] {
  let out = events
  if (query.namespaces && query.namespaces.length > 0) {
    const set = new Set(query.namespaces)
    out = out.filter((e) => set.has(e.namespace))
  }
  if (query.kinds && query.kinds.length > 0) {
    const set = new Set(query.kinds)
    out = out.filter((e) => set.has(e.kind))
  }
  if (query.includeK8sEvents === false) {
    out = out.filter((e) => e.source !== 'k8s_event')
  }
  if (query.includeDeleted === false) {
    out = out.filter((e) => e.eventType !== 'delete')
  }
  // Enforced here — not only server-side — so both sources honor it
  // identically: the retained endpoint has no include_managed param, and the
  // local path's filter presets can override the server-side flag (the 'all'
  // preset re-includes managed). Only an explicit false filters; the default
  // keeps machinery rows, which the swimlane's pod/RS child lanes require.
  if (query.includeManaged === false) {
    out = out.filter((e) => !isManagedTimelineEvent(e))
  }
  // An explicit brush window bounds the result by event time so the live poll's
  // recent-window merge can't leak events past the selected [from,to].
  if (query.fromMs != null || query.toMs != null) {
    out = out.filter((e) => {
      const t = new Date(e.timestamp).getTime()
      if (query.fromMs != null && t < query.fromMs) return false
      if (query.toMs != null && t > query.toMs) return false
      return true
    })
  }
  out = [...out].sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime())
  if (query.limit && out.length > query.limit) {
    out = out.slice(0, query.limit)
  }
  return out
}

// The retained ring: what the single query holds. Same shape idea as the
// local source's cached page, plus retention extras (coverage, truncation).
export interface RetainedRing {
  events: TimelineEvent[]
  coverage: TimelineCoverageRecord[]
  truncated: boolean
  // The delta cursor rides the SAME react-query commit as the events it
  // describes: an aborted or discarded fetch discards both together, so the
  // cursor can never advance past events that were thrown away. Absent = the
  // hub predates the delta feed; polls become no-ops (manual refresh still
  // resyncs) instead of hammering it with a full reload every tick.
  cursor?: string
  // When the last FULL ring load ran — the anti-entropy resync clock.
  loadedAtMs: number
}

// Whether an explicit [from,to] selection reaches below what the loaded ring
// holds. A truncated ring kept only the NEWEST rows, so a selection whose
// from-edge is older than the ring's oldest loaded event would render rows the
// server still holds as empty — such a window must fetch itself. An
// untruncated ring holds everything its depth covers, so every selection
// slices it client-side with no fetch.
//
// The ring stays authoritative only while the window reaches to/past the
// NEWEST loaded ring row: every ring row then falls inside the window, and
// the ring being the server's globally-newest-ringLimit rows makes them the
// newest-ringLimit rows within the window too — a server window fetch would
// return the identical set. A window whose to-edge is BELOW the newest ring
// row excludes the freshest rows, so a server fetch spends that budget on
// older rows the truncated ring dropped — such a window must fetch itself.
// The guard is ring-relative, not wall-clock: distance from the clock proves
// nothing about coverage (under heavy churn the ring's whole budget can sit
// inside the last few minutes, leaving a just-frozen window's slice empty).
//
// Two allowances keep the LIVE sliding selection — an explicit [from,to]
// re-derived from the clock on a coarse tick — on the zero-fetch ring path:
// the newest-row edge caps at `now` (a hub clock ahead of the client stamps
// ring rows in the client's future; they must not outrun a to-edge that
// tracks the client clock), and the comparison tolerates one tick of lag
// (delta merges land fresh rows while the live to-edge waits for its next
// re-derive; without the tolerance every merge would re-key a full-window
// fetch until the tick catches up).
//
// An empty truncated ring (or one with no parseable timestamps) proves
// nothing about coverage, so it fails toward fetching. Exported for unit
// tests; not re-exported publicly.
const LIVE_WINDOW_TICK_SLACK_MS = 2 * LIVE_TICK_MS
export function needsWindowFetch(
  ring: Pick<RetainedRing, 'events' | 'truncated'>,
  fromMs: number,
  toMs: number,
  now: number,
): boolean {
  if (!ring.truncated) return false
  let oldest = Number.POSITIVE_INFINITY
  let newest = Number.NEGATIVE_INFINITY
  for (const e of ring.events) {
    const t = new Date(e.timestamp).getTime()
    if (!Number.isFinite(t)) continue
    if (t < oldest) oldest = t
    if (t > newest) newest = t
  }
  if (Number.isFinite(newest) && toMs >= Math.min(newest, now) - LIVE_WINDOW_TICK_SLACK_MS) return false
  return fromMs < oldest
}

// A frozen window never slides and never polls, so it refetches only on a
// genuinely new mount past this staleness. Not cached forever: a late-ingested
// event CAN land inside an old window, so an aged cache entry may still
// refresh.
const FROZEN_WINDOW_STALE_MS = 10 * 60 * 1000

// Ring keys whose next fetch must be a full reload (manual refresh). A side
// flag, not a side cursor: consuming it only switches the PATH of the next
// fetch — all sync state that must stay consistent with the data lives inside
// RetainedRing itself.
const retainedForceResync = new Set<string>()

// Ring-fetch orchestration, the retained twin of client.ts's
// runDeltaSyncFetch: full ring load when there's no cached page (or a resync
// is due), else delta pages merged in with replace-by-id. Extracted so the
// full-load → delta-poll → cursor-reset contract is exercisable without a
// React render; state (the cached page, the force flag) is passed in
// explicitly.
export async function runRetainedRingFetch(deps: {
  ringKey: string
  cached: RetainedRing | undefined
  forceResync: Set<string>
  // Retention depth of the backend. Undefined = unbounded (a local store may
  // be configured to retain forever): the full load starts at epoch zero and
  // delta merges never age-prune - the ring row cap is the only bound.
  capMs?: number
  now: number
  signal?: AbortSignal
  // Ring row cap: the server's per-request ceiling for this source (the hub
  // serves up to 50k; the local binary pages at 10k). Also bounds delta
  // accumulation client-side.
  ringLimit?: number
  // Scope the server query when the consumer is namespace-scoped; see
  // namespacesParam.
  namespaces?: string[]
  // Anti-entropy cadence: how stale the ring may get before a full reload
  // (server-side evictions and retention prunes are invisible to deltas).
  // The local binary reloads cheaply so it keeps its historical 5-minute
  // cadence; a Cloud ring is a heavy transfer and resyncs hourly.
  resyncMs?: number
}): Promise<RetainedRing> {
  const {
    ringKey, cached, forceResync, capMs, now, signal,
    ringLimit = RETAINED_RING_LIMIT, namespaces, resyncMs = RETAINED_FULL_RESYNC_MS,
  } = deps
  // Anti-entropy: past the resync window (or on manual refresh), fall through
  // to a full reload regardless of cursor state — refreshing coverage,
  // recomputing the truncated flag, and dropping anything deltas can't
  // retract.
  // A negative age (loadedAtMs in the future — backward clock step, suspend/
  // resume) counts as due: the resync clock must fail toward refreshing, not
  // toward silently suspending anti-entropy for hours.
  const ringAge = cached != null ? now - cached.loadedAtMs : 0
  const resyncDue = forceResync.has(ringKey) || (cached != null && (ringAge < 0 || ringAge > resyncMs))
  if (cached && !resyncDue) {
    if (!cached.cursor) {
      return cached
    }
    try {
      let cursor = cached.cursor
      let merged = cached.events
      let changed = false
      for (let page = 0; page < RETAINED_DELTA_MAX_PAGES; page++) {
        const delta = await fetchRetainedDelta(cursor, signal, namespaces)
        if (delta.cursor) cursor = delta.cursor
        if (delta.events.length) {
          merged = mergeDeltaEvents(merged, delta.events, Number.POSITIVE_INFINITY)
          changed = true
        }
        if (!delta.more) break
      }
      // Returning the cached reference on a no-op delta skips re-renders. An
      // idle cluster's cursor is stable by construction — verified against the
      // hub: EventsIngestedSince starts its frontier AT `since` and advances
      // it only past emitted rows, and the read-side clamp can only lower a
      // frontier that already rests at or below the visibility edge.
      if (!changed && cursor === cached.cursor) return cached
      // Two bounds keep an always-open tab from growing without limit: the
      // retention floor (the server TTL-deletes past the same boundary,
      // held below the depth by the skew slack so a client clock ahead of
      // the hub cannot prune events the hub still retains) and the ring row
      // cap — accumulation past it drops the OLDEST rows, the same
      // semantics as the initial load and the local source's ring, and
      // flips `truncated` so the UI says so.
      const floor = capMs != null ? now - capMs - RETAINED_CLOCK_SKEW_SLACK_MS : Number.NEGATIVE_INFINITY
      const pruned = capMs != null ? merged.filter((e) => new Date(e.timestamp).getTime() >= floor) : merged
      const capped = pruned.length > ringLimit
      return {
        events: capped ? pruned.slice(0, ringLimit) : pruned,
        coverage: cached.coverage,
        truncated: cached.truncated || capped,
        cursor,
        loadedAtMs: cached.loadedAtMs,
      }
    } catch (err) {
      // A rejected cursor (hub restarted onto an older build, operator wiped
      // the store) is recoverable: fall through to a full reload. Anything
      // else propagates — react-query keeps the cached ring visible.
      if (!(err instanceof ApiError && err.status === 400)) {
        throw err
      }
    }
  }
  // The recent edge extends past the client clock by the skew slack (a client
  // clock behind the hub would otherwise leave a permanent hole: events in
  // (clientNow, hubNow] are under the returned cursor, so no delta re-delivers
  // them). The window slides back by the same slack to stay within the hub's
  // maximum range.
  const to = now + RETAINED_CLOCK_SKEW_SLACK_MS
  const full = await fetchRetainedWindow(capMs != null ? to - capMs : 0, to, signal, ringLimit, namespaces)
  // Consume the flag only while this fetch still owns the query. A resolved
  // load can still be discarded — a manual refresh cancels the in-flight poll
  // AFTER its network call finished — and deleting then would eat the flag
  // that refresh just armed, turning it into a silent no-op delta poll.
  if (!signal?.aborted) forceResync.delete(ringKey)
  return {
    events: full.events,
    coverage: full.coverage,
    truncated: full.truncated,
    cursor: full.cursor,
    loadedAtMs: now,
  }
}

// One events hook for every mode. Local and hub-backed timelines share the
// whole cycle — ring load, cursor deltas, client-side re-windowing — and
// differ only in depth (capMs) and the server's per-request row ceiling
// (ringLimit). The endpoint path is identical (`{apiBase}/timeline/events`);
// apiBase alone decides which server answers.
function createRingEventsHook(opts: {
  // Undefined = unbounded depth (the server's own retention is the bound).
  capMs?: number
  ringLimit: number
  resyncMs: number
}): (query: TimelineQuery) => TimelineEventsResult {
  const { capMs, ringLimit, resyncMs } = opts
  return function useRingEvents(query: TimelineQuery): TimelineEventsResult {
    const enabled = query.enabled ?? true
    const queryClient = useQueryClient()

    // The key carries the depth, the apiBase (a host that swaps clusters must
    // not serve the previous cluster's ring), and the namespace scope (the
    // ring is row-capped, so the SERVER query must be scoped for a
    // namespace-scoped consumer — an all-namespace ring could spend its whole
    // budget elsewhere). It does NOT carry the query's [from,to]: period
    // changes re-window the loaded ring client-side in applyClientFilters,
    // issuing no fetch. A frozen window the ring cannot answer runs its own
    // separately-keyed query below instead of widening the ring.
    const apiBase = getApiBase()
    const ringNamespacesKey = query.namespaces?.length ? [...query.namespaces].sort().join(',') : ''
    const queryKey = useMemo(
      () => ['timeline-ring', apiBase, capMs ?? 'unbounded', ringNamespacesKey],
      [apiBase, ringNamespacesKey],
    )

    const ring = useQuery<RetainedRing>({
      queryKey,
      queryFn: ({ signal }) =>
        runRetainedRingFetch({
          ringKey: JSON.stringify(queryKey),
          cached: queryClient.getQueryData<RetainedRing>(queryKey),
          forceResync: retainedForceResync,
          capMs,
          now: Date.now(),
          signal,
          ringLimit,
          namespaces: ringNamespacesKey ? ringNamespacesKey.split(',') : undefined,
          resyncMs,
        }),
      enabled,
      refetchInterval: RETAINED_POLL_MS,
      staleTime: 5000,
    })

    // On a store holding more than ringLimit rows, an explicit frozen
    // [from,to] older than the ring's oldest loaded event has NO rows in the
    // ring even though the server holds them — slicing would render the deep
    // past as empty. Such a window fetches itself: same endpoint, same wire
    // contract in both modes. Ring-covered and live-edge windows never arm
    // this query (see needsWindowFetch), so period changes and the live tick
    // stay zero-fetch.
    //
    // The clock sample only refreshes when a dep changes; that is safe here
    // because the predicate is ring-relative — the clock only caps the
    // newest-row comparison against skew-stamped future rows, and a stale
    // sample makes that cap tighter (toward the ring), never looser.
    const windowFromMs = query.fromMs
    const windowToMs = query.toMs
    const windowNeeded = useMemo(
      () =>
        enabled && windowFromMs != null && windowToMs != null && ring.data != null &&
        needsWindowFetch(ring.data, windowFromMs, windowToMs, Date.now()),
      [enabled, windowFromMs, windowToMs, ring.data],
    )
    const windowQuery = useQuery<RetainedWindowResult>({
      queryKey: ['timeline-window', apiBase, windowFromMs, windowToMs, ringNamespacesKey],
      queryFn: ({ signal }) => {
        if (windowFromMs == null || windowToMs == null) {
          // Unreachable: `enabled` requires both bounds. Guards the invariant
          // without a non-null assertion.
          throw new Error('timeline window query ran without an explicit [from,to]')
        }
        return fetchRetainedWindow(
          windowFromMs,
          windowToMs,
          signal,
          ringLimit,
          ringNamespacesKey ? ringNamespacesKey.split(',') : undefined,
        )
      },
      enabled: windowNeeded,
      staleTime: FROZEN_WINDOW_STALE_MS,
    })

    const kindsKey = query.kinds?.join(',')
    const namespacesKey = query.namespaces?.join(',')
    // A preset-derived window slides with the clock; on an idle ring the memo
    // would never re-sample it (the no-op cached return keeps ring identity
    // stable), freezing a "24h" bound overnight. A coarse tick keeps the label
    // honest without meaningful churn.
    const derivedWindow =
      query.fromMs == null && query.toMs == null && !!query.timeRange && query.timeRange !== 'all'
    // Gated on `enabled` too: a disabled consumer (an unopened tab) must not
    // pay a periodic re-filter of the shared ring. The bump on arming keeps
    // the window fresh across a disable→enable gap (tab reopened hours later
    // must not show the stale bound until the next tick).
    const tickActive = derivedWindow && enabled
    const [presetTick, setPresetTick] = useState(0)
    useEffect(() => {
      if (!tickActive) return
      setPresetTick((t) => t + 1)
      const id = setInterval(() => setPresetTick((t) => t + 1), RETAINED_PRESET_TICK_MS)
      return () => clearInterval(id)
    }, [tickActive])
    const data = useMemo(() => {
      if (windowNeeded) {
        // Serving from the frozen-window fetch. Same client filters as the
        // ring path so the two are indistinguishable to consumers; undefined
        // while in flight so the host shows a loader, not an empty ring slice.
        return windowQuery.data ? applyClientFilters(windowQuery.data.events, query) : undefined
      }
      if (!ring.data) return undefined
      // A `timeRange` preset without an explicit [from,to] window resolves to
      // a client-side bound over the ring — the retained twin of the local
      // source's server-side `since` resolution. Without this, a consumer
      // like the Applications History tab picking "24h" would silently get
      // the whole ring.
      let effective = query
      if (derivedWindow) {
        const span = rangeSpanMs(query.timeRange, capMs)
        if (span != null) {
          effective = { ...query, fromMs: Date.now() - (capMs != null ? Math.min(span, capMs) : span) }
        }
      }
      return applyClientFilters(ring.data.events, effective)
      // Every filter is client-side here (the ring key carries none of them),
      // so each rides the memo: array-valued ones via join keys so identity
      // churn from the host doesn't re-filter. The live tick advances
      // query.toMs, re-windowing to the sliding edge with no refetch.
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [
      ring.data,
      windowNeeded,
      windowQuery.data,
      namespacesKey,
      kindsKey,
      query.includeK8sEvents,
      query.includeDeleted,
      query.includeManaged,
      query.limit,
      query.fromMs,
      query.toMs,
      query.timeRange,
      capMs,
      derivedWindow,
      presetTick,
    ])

    if (windowNeeded) {
      // Every signal follows the query that serves the data: the ring keeps
      // polling in the background, but its flags describe data the consumer
      // is not looking at.
      return {
        data,
        isLoading: windowQuery.isLoading,
        isFetching: windowQuery.isFetching,
        isError: windowQuery.isError,
        error: windowQuery.error,
        refetch: () => {
          windowQuery.refetch()
        },
        coverage: windowQuery.data?.coverage,
        truncated: windowQuery.data?.truncated,
      }
    }

    return {
      data,
      isLoading: ring.isLoading,
      isFetching: ring.isFetching,
      isError: ring.isError,
      error: ring.error,
      refetch: () => {
        // Manual refresh is the resync backstop: flag the ring so the next
        // fetch reloads it whole instead of trusting accumulated state.
        retainedForceResync.add(JSON.stringify(queryKey))
        ring.refetch()
      },
      coverage: ring.data?.coverage,
      truncated: ring.data?.truncated,
    }
  }
}

// The retained (hub) wire shape: the bucket start ships as `hourStartMs` and
// coverage may be a single span or an array. fetchRetainedOverview remaps this
// to the client `TimelineOverviewBucket` (startMs + a normalized coverage array)
// at this one parse boundary so nothing downstream sees the wire quirks.
interface RawOverviewBucket {
  hourStartMs: number
  summary: TimelineOverviewBucket['summary']
  coverage?: TimelineCoverageSpan | TimelineCoverageSpan[]
}

function normalizeCoverage(
  cov: TimelineCoverageSpan | TimelineCoverageSpan[] | undefined,
): TimelineCoverageSpan[] | undefined {
  if (cov == null) return undefined
  return Array.isArray(cov) ? cov : [cov]
}

async function fetchRetainedOverview(range: TimelineRange): Promise<TimelineOverviewResult> {
  const res = await apiFetch(apiUrl(`/timeline/overview?from=${Math.round(range.from)}&to=${Math.round(range.to)}`))
  if (!res.ok) {
    const errorData = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new ApiError(errorData.error || `HTTP ${res.status}`, res.status, errorData)
  }
  const body: unknown = await res.json()
  const env = body as { buckets?: RawOverviewBucket[]; availableFromMs?: number }
  const buckets: TimelineOverviewBucket[] = (env.buckets ?? []).map((b) => ({
    startMs: b.hourStartMs,
    summary: b.summary,
    coverage: normalizeCoverage(b.coverage),
  }))
  return { buckets, availableFromMs: env.availableFromMs }
}

export const localSource: TimelineSource = {
  capabilities: { mode: 'local', ringLimit: LOCAL_RING_LIMIT },
  useEvents: createRingEventsHook({
    // No depth cap: the binary's own ring size and --timeline-retention are
    // the real bounds, and retention may legitimately be unlimited. The ring
    // row ceiling still bounds client-side growth.
    ringLimit: LOCAL_RING_LIMIT,
    resyncMs: LOCAL_FULL_RESYNC_MS,
  }),
}

export function createRetainedSource(config: TimelineSourceConfig): TimelineSource {
  const capabilities: TimelineSourceCapabilities = {
    mode: 'retained',
    ringLimit: RETAINED_RING_LIMIT,
    maxRangeDays: config.maxRangeDays,
  }
  return {
    capabilities,
    useEvents: createRingEventsHook({
      capMs:
        Math.min(capabilities.maxRangeDays ?? DEFAULT_RETAINED_MAX_RANGE_DAYS, RETAINED_MAX_DEPTH_DAYS) * DAY_MS,
      ringLimit: RETAINED_RING_LIMIT,
      resyncMs: RETAINED_FULL_RESYNC_MS,
    }),
    fetchOverview: fetchRetainedOverview,
  }
}

export function resolveTimelineSource(config?: TimelineSourceConfig): TimelineSource {
  if (config?.mode === 'retained') return createRetainedSource(config)
  return localSource
}
