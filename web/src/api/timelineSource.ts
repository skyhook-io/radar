// Timeline data-source abstraction.
//
// Radar's timeline can be backed by two stores:
//   - 'local'    — the in-process event store the Radar binary keeps (default,
//                  OSS standalone). Fetched via GET {apiBase}/changes.
//   - 'retained' — a longer-horizon history store answered upstream of Radar
//                  (relative to apiBase) as GET {apiBase}/timeline/events and
//                  GET {apiBase}/timeline/overview. This is an extension point:
//                  the standalone binary never selects it; a host that embeds
//                  RadarApp behind a proxy that serves retained history opts in
//                  via the `timelineSource` prop.
//
// Both sources expose the same `useEvents(query)` hook shape so the timeline
// wrappers stay source-agnostic: pick the source from context, call useEvents.
import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { quantizeBaseWindow } from '@skyhook-io/k8s-ui'
import { useChanges, apiFetch, ApiError, type UseChangesOptions } from './client'
import { apiUrl, getApiBase } from './config'
import type { TimelineEvent, TimeRange } from '../types'

// The query the wrappers pass. Superset of the local store's params so most
// call sites don't change shape when switching sources.
export type TimelineQuery = UseChangesOptions & {
  // Explicit [from,to] window in epoch-ms. When both are set the retained
  // source loads exactly this window (the scrubber's brush selection) instead
  // of deriving one from `timeRange`. Ignored by the local source.
  fromMs?: number
  toMs?: number
  // LIVE mode: the [from,to] window slides every tick. Quantize the BASE fetch
  // window to fixed steps so the react-query key only changes every few minutes;
  // the precise sliding window is still applied by the client-side filter, and
  // the trailing seam is covered by the live poll. Ignored by the local source.
  sliding?: boolean
}

export interface TimelineSourceCapabilities {
  mode: 'local' | 'retained'
  // Only meaningful for 'retained': caps how far back the 'all' range reaches.
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

export interface TimelineOverviewBucket {
  hourStartMs: number
  summary: {
    total: number
    adds: number
    updates: number
    deletes: number
    warnings: number
    worstHealth: string
    namespaces: string[]
  }
  coverage?: unknown
}

// Overview response. Tolerates both the legacy bare-array body and the new
// envelope that carries `availableFromMs` (oldest event-time the server holds
// for this cluster) so the scrubber domain reflects real retention.
export interface TimelineOverviewResult {
  buckets: TimelineOverviewBucket[]
  availableFromMs?: number
}

export interface TimelineEventsResult {
  data: TimelineEvent[] | undefined
  isLoading: boolean
  isError: boolean
  refetch: () => void
  // Present only for sources that report coverage (retained).
  coverage?: TimelineCoverageRecord[]
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
// Local source — thin wrapper over the existing useChanges behavior. Zero
// behavioral change for OSS standalone.
// ============================================================================

// Full ring size to pull when the host bounds the local list by the shared
// scrubber selection — matches the swimlane's ring fetch so both views cover the
// same window.
const LOCAL_RING_LIMIT = 10000

function useLocalEvents(query: TimelineQuery): TimelineEventsResult {
  // Without a window the dropdown-driven `since` fetch is untouched. When the
  // host drives an explicit [from,to] window (the shared scrubber selection),
  // the private range dropdown is bypassed: the local /changes endpoint is
  // `since`-based and can't express a frozen past window, so load the whole ring
  // and bound it to the selection client-side (applyClientFilters), exactly as
  // the swimlane derives its view from the loaded ring.
  const windowed = query.fromMs != null || query.toMs != null
  const { data, isLoading, isError, refetch } = useChanges(
    windowed ? { ...query, timeRange: 'all', limit: LOCAL_RING_LIMIT } : query,
  )
  const events = useMemo(
    () => (windowed && data ? applyClientFilters(data, query) : data),
    // `data` identity captures every server-side filter change (namespaces,
    // kinds, k8s-events, deleted — all in the useChanges query key); the
    // client-only window + cap are added here. The live tick advances
    // query.toMs, re-filtering to the sliding edge with no refetch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [windowed, data, query.fromMs, query.toMs, query.limit],
  )
  return { data: events, isLoading, isError, refetch }
}

export const localSource: TimelineSource = {
  capabilities: { mode: 'local' },
  useEvents: useLocalEvents,
}

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
// No coverage/gap field is emitted. Coverage is a retention concept — the hub
// records what it missed while not watching. Locally we cannot know what
// happened during downtime, so claiming a gap would be dishonest; we omit it.
export function localOverviewFromEvents(events: TimelineEvent[]): TimelineOverviewResult {
  const slots = new Map<number, LocalHourSlot>()
  let oldest = Number.POSITIVE_INFINITY

  for (const e of events) {
    const t = new Date(e.timestamp).getTime()
    if (!Number.isFinite(t)) continue
    if (t < oldest) oldest = t
    const hour = Math.floor(t / LOCAL_HOUR_MS) * LOCAL_HOUR_MS
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
    .map(([hourStartMs, s]) => ({
      hourStartMs,
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
    .sort((a, b) => a.hourStartMs - b.hourStartMs)

  return { buckets, availableFromMs: Number.isFinite(oldest) ? oldest : undefined }
}

// ============================================================================
// Retained source — streams NDJSON from {apiBase}/timeline/events.
// ============================================================================

const HOUR_MS = 60 * 60 * 1000
const DAY_MS = 24 * HOUR_MS

// Bound for the 'all' range when the host doesn't specify maxRangeDays.
const DEFAULT_RETAINED_MAX_RANGE_DAYS = 7

// Recent window the live poll re-fetches and merges over the loaded range. Wide
// enough that a missed 10s tick can't open a gap.
const LIVE_WINDOW_MS = 10 * 60 * 1000
const LIVE_REFETCH_MS = 10_000

function rangeSpanMs(range: TimeRange | undefined, maxRangeDays?: number): number {
  const cap = (maxRangeDays ?? DEFAULT_RETAINED_MAX_RANGE_DAYS) * DAY_MS
  let span: number
  switch (range) {
    case '5m':
      span = 5 * 60 * 1000
      break
    case '30m':
      span = 30 * 60 * 1000
      break
    case '1h':
      span = HOUR_MS
      break
    case '6h':
      span = 6 * HOUR_MS
      break
    case '24h':
      span = 24 * HOUR_MS
      break
    case '7d':
      span = 7 * DAY_MS
      break
    case '30d':
      span = 30 * DAY_MS
      break
    case 'all':
    case undefined:
      span = cap
      break
    default:
      span = HOUR_MS
  }
  return Math.min(span, cap)
}

interface RetainedWindowResult {
  events: TimelineEvent[]
  coverage: TimelineCoverageRecord[]
}

type TerminalRecord = { type: 'end' } | { type: 'error'; message?: string }

// De-dupe by id keeping the LAST occurrence — a later revision of an event
// replaces the earlier one.
function dedupeById(events: TimelineEvent[]): TimelineEvent[] {
  const byId = new Map<string, TimelineEvent>()
  for (const e of events) byId.set(e.id, e)
  return Array.from(byId.values())
}

async function fetchRetainedWindow(
  from: number,
  to: number,
  signal?: AbortSignal,
): Promise<RetainedWindowResult> {
  const res = await apiFetch(
    apiUrl(`/timeline/events?from=${Math.round(from)}&to=${Math.round(to)}`),
    signal ? { signal } : undefined,
  )
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
    const rec = JSON.parse(trimmed) as { type?: string; message?: string }
    if (rec.type === 'end') {
      terminal = { type: 'end' }
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
  if ((terminal as TerminalRecord).type === 'error') {
    throw new Error((terminal as { message?: string }).message || 'timeline stream error')
  }

  return { events: dedupeById(events), coverage }
}

// The retained endpoint scopes only by [from,to] (cluster is implicit in the
// apiBase path), so the store-side query params the local endpoint honors are
// applied client-side over the loaded window.
function applyClientFilters(events: TimelineEvent[], query: TimelineQuery): TimelineEvent[] {
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
  out = [...out].sort((a, b) => b.timestamp.localeCompare(a.timestamp))
  if (query.limit && out.length > query.limit) {
    out = out.slice(0, query.limit)
  }
  return out
}

function mergeWindows(
  base: RetainedWindowResult | undefined,
  live: RetainedWindowResult | undefined,
): RetainedWindowResult {
  // live is newer, so it overwrites base for shared ids.
  const events = dedupeById([...(base?.events ?? []), ...(live?.events ?? [])])
  const coverage = [...(base?.coverage ?? []), ...(live?.coverage ?? [])]
  return { events, coverage }
}

function createRetainedEventsHook(
  capabilities: TimelineSourceCapabilities,
): (query: TimelineQuery) => TimelineEventsResult {
  return function useRetainedEvents(query: TimelineQuery): TimelineEventsResult {
    const enabled = query.enabled ?? true

    // Pin the base window when the range (or cap) changes — not on every render —
    // so the query key is stable and react-query can cache it. Recency is the
    // live poll's job.
    const window = useMemo<TimelineRange>(() => {
      if (query.fromMs != null && query.toMs != null) {
        // Sliding: quantize so two ticks inside one step share a query key (no
        // refetch). The precise [from,to] is still enforced by applyClientFilters.
        if (query.sliding) {
          const q = quantizeBaseWindow(query.fromMs, query.toMs)
          return { from: q.fromMs, to: q.toMs }
        }
        return { from: query.fromMs, to: query.toMs }
      }
      const to = Date.now()
      return { from: to - rangeSpanMs(query.timeRange, capabilities.maxRangeDays), to }
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [query.fromMs, query.toMs, query.timeRange, query.sliding, capabilities.maxRangeDays])

    const base = useQuery<RetainedWindowResult>({
      // apiBase scopes the key: a host that swaps clusters (mutable module global)
      // must not serve the previous cluster's cached window.
      queryKey: ['timeline-retained', getApiBase(), 'base', window.from, window.to],
      queryFn: ({ signal }) => fetchRetainedWindow(window.from, window.to, signal),
      enabled,
      staleTime: LIVE_REFETCH_MS,
    })

    // Fixed recent-window re-poll. Key is stable; queryFn reads the clock fresh
    // each tick so the window slides without churning the cache key. Skipped for
    // a purely historical brush (its right edge is older than the live window),
    // where recency merging would only add events the time filter drops.
    const liveEnabled = enabled && (query.toMs == null || query.toMs >= Date.now() - LIVE_WINDOW_MS)
    const live = useQuery<RetainedWindowResult>({
      queryKey: ['timeline-retained', getApiBase(), 'live'],
      queryFn: ({ signal }) => {
        const to = Date.now()
        return fetchRetainedWindow(to - LIVE_WINDOW_MS, to, signal)
      },
      enabled: liveEnabled,
      refetchInterval: LIVE_REFETCH_MS,
    })

    const merged = useMemo(
      () => mergeWindows(base.data, live.data),
      [base.data, live.data],
    )

    const data = useMemo(() => {
      if (base.data === undefined && live.data === undefined) return undefined
      return applyClientFilters(merged.events, query)
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [
      merged,
      base.data,
      live.data,
      query.namespaces,
      query.kinds?.join(','),
      query.includeK8sEvents,
      query.includeDeleted,
      query.limit,
      query.fromMs,
      query.toMs,
    ])

    return {
      data,
      isLoading: base.isLoading,
      // Base failure is a real error; a failing live poll must not blank an
      // already-loaded range.
      isError: base.isError,
      refetch: () => {
        base.refetch()
        live.refetch()
      },
      coverage: merged.coverage,
    }
  }
}

async function fetchRetainedOverview(range: TimelineRange): Promise<TimelineOverviewResult> {
  const res = await apiFetch(apiUrl(`/timeline/overview?from=${Math.round(range.from)}&to=${Math.round(range.to)}`))
  if (!res.ok) {
    const errorData = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new ApiError(errorData.error || `HTTP ${res.status}`, res.status, errorData)
  }
  const body: unknown = await res.json()
  // Legacy hosts return a bare bucket array; the envelope adds availableFromMs.
  if (Array.isArray(body)) {
    return { buckets: body as TimelineOverviewBucket[] }
  }
  const env = body as { buckets?: TimelineOverviewBucket[]; availableFromMs?: number }
  return { buckets: env.buckets ?? [], availableFromMs: env.availableFromMs }
}

export function createRetainedSource(config: TimelineSourceConfig): TimelineSource {
  const capabilities: TimelineSourceCapabilities = {
    mode: 'retained',
    maxRangeDays: config.maxRangeDays,
  }
  return {
    capabilities,
    useEvents: createRetainedEventsHook(capabilities),
    fetchOverview: fetchRetainedOverview,
  }
}

export function resolveTimelineSource(config?: TimelineSourceConfig): TimelineSource {
  if (config?.mode === 'retained') return createRetainedSource(config)
  return localSource
}
