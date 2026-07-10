import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, RefreshCw } from 'lucide-react'
import {
  TimelineStrip,
  clampSelection,
  countEventsAfter,
  mergeGapRanges,
  pickDisplayBucketSizeMs,
  presetToSelection,
  type ScrubberBucket,
  type ScrubberPreset,
  type ScrubberRange,
  type TimelineLiveState,
} from '@skyhook-io/k8s-ui'
import type {
  TimelineSource,
  TimelineOverviewBucket,
  TimelineOverviewResult,
} from '../../api/timelineSource'
import { getApiBase } from '../../api/config'

const HOUR_MS = 60 * 60 * 1000
const DAY_MS = 24 * HOUR_MS
const EMPTY_BUCKETS: TimelineOverviewBucket[] = []
const MAX_STRIP_BARS = 512

// Per-request guard on the retained events endpoint: never brush wider than 7d.
const MAX_SELECTION_MS = 7 * DAY_MS

// Group the server's hour buckets into fixed display buckets aligned to the
// display size, summing counts. The host owns this so the pure scrubber only
// ever paints ready-to-draw bars.
export function groupBuckets(
  hourBuckets: TimelineOverviewBucket[],
  bucketSizeMs: number,
): ScrubberBucket[] {
  const slots = new Map<number, { total: number; warnings: number }>()
  for (const b of hourBuckets) {
    const slot = Math.floor(b.startMs / bucketSizeMs) * bucketSizeMs
    const cur = slots.get(slot) ?? { total: 0, warnings: 0 }
    cur.total += b.summary.total
    cur.warnings += b.summary.warnings
    slots.set(slot, cur)
  }
  return [...slots.entries()]
    .map(([slot, v]) => ({ startMs: slot, endMs: slot + bucketSizeMs, total: v.total, warnings: v.warnings }))
    .sort((a, b) => a.startMs - b.startMs)
}

function coverageNumber(v: unknown): number | undefined {
  return typeof v === 'number' && Number.isFinite(v) ? v : undefined
}

/**
 * Collect recording-gap spans from every bucket's backend-owned coverage field
 * into merged, domain-clipped time ranges. The hub emits event-time bounds
 * (`eventTimeStartMs` / `eventTimeEndMs`); a span missing either bound is
 * skipped. Merge/dedupe/clamp is delegated to the pure `mergeGapRanges`.
 */
export function extractRecordingGaps(
  hourBuckets: TimelineOverviewBucket[],
  domain?: ScrubberRange,
): ScrubberRange[] {
  const raw: ScrubberRange[] = []
  for (const b of hourBuckets) {
    const list = b.coverage ?? []
    for (const item of list) {
      if (!item) continue
      const start = coverageNumber(item.eventTimeStartMs)
      const end = coverageNumber(item.eventTimeEndMs)
      if (start == null || end == null) continue
      raw.push({ fromMs: start, toMs: end })
    }
  }
  return mergeGapRanges(raw, domain)
}

export function buildPresets(maxRangeDays: number): ScrubberPreset[] {
  const presets: ScrubberPreset[] = [
    { label: '1h', ms: HOUR_MS },
    { label: '6h', ms: 6 * HOUR_MS },
    { label: '24h', ms: DAY_MS },
    { label: '7d', ms: 7 * DAY_MS },
  ]
  // 30d is a domain-context preset — it clamps to the 7d per-request cap, but
  // signals the deeper retained window is available.
  if (maxRangeDays >= 30) presets.push({ label: '30d', ms: 30 * DAY_MS })
  return presets
}

/**
 * Extend an applied selection by 50% of its width in one direction, clamped to
 * the domain and the per-request cap. Used by the swimlane's "extend" affordance
 * — anchored on the edge that stays put (past extend keeps the recent edge).
 */
export function extendSelection(
  sel: ScrubberRange,
  dir: 'past' | 'future',
  domain: ScrubberRange,
  maxSelectionMs: number,
): ScrubberRange {
  const grow = (sel.toMs - sel.fromMs) * 0.5
  if (dir === 'past') {
    return clampSelection({ fromMs: sel.fromMs - grow, toMs: sel.toMs }, domain, maxSelectionMs, 'end').selection
  }
  return clampSelection({ fromMs: sel.fromMs, toMs: sel.toMs + grow }, domain, maxSelectionMs, 'start').selection
}

export interface ScrubberDomainInfo {
  domain: ScrubberRange
  maxSelectionMs: number
}

interface RetainedTimelineScrubberProps {
  source: TimelineSource
  selection: ScrubberRange
  // Any explicit range action (brush Run-query, pan, zoom, step, handle drag)
  // — the host treats this as a transition to FROZEN mode.
  onSelectionChange: (sel: ScrubberRange) => void
  // A domain clamp (the selection outgrew or fell outside the retained window)
  // is NOT a user range action: routed here so the host can preserve LIVE mode
  // (narrowing the width) instead of freezing.
  onSelectionClamp?: (sel: ScrubberRange) => void
  // Preset click (1h/6h/24h/7d) — distinct route so the host can enter LIVE mode
  // with that width. When omitted, presets fall back to a one-shot frozen
  // selection via onSelectionChange.
  onPresetSelect?: (widthMs: number) => void
  // The lens band (swimlane's visible window). Two-way synced by the host.
  lens?: ScrubberRange
  onLensChange?: (lens: ScrubberRange) => void
  lensResizable?: boolean
  // Lifts the server-derived domain + cap so the host can clamp extend requests.
  onDomainChange?: (info: ScrubberDomainInfo) => void
  // Lifts merged recording gaps so the host can thread them into the swimlane.
  onGapsChange?: (gaps: ScrubberRange[]) => void
  // Live/paused chip state + click handler (host-computed). The scrubber owns the
  // overview buckets, so it enriches a frozen state with the "new events" count
  // before handing the chip to TimelineStrip.
  liveState?: TimelineLiveState
  onLiveChipClick?: () => void
}

export function RetainedTimelineScrubber({ source, selection, onSelectionChange, onSelectionClamp, onPresetSelect, lens, onLensChange, lensResizable, onDomainChange, onGapsChange, liveState, onLiveChipClick }: RetainedTimelineScrubberProps) {
  const maxRangeDays = source.capabilities.maxRangeDays ?? 7
  const fetchOverview = source.fetchOverview

  const overview = useQuery<TimelineOverviewResult>({
    // apiBase scopes the key so a cluster swap (mutable module global) can't serve
    // the previous cluster's overview buckets.
    queryKey: ['timeline-overview', getApiBase(), maxRangeDays],
    queryFn: () => {
      const to = Date.now()
      const from = to - maxRangeDays * DAY_MS
      return fetchOverview!({ from, to })
    },
    enabled: !!fetchOverview,
    staleTime: 60_000,
    refetchInterval: 60_000,
  })

  // Stable "now" between refetches so the domain (and selection clamping) don't
  // jitter on every render. Must be render-stable even before the query
  // resolves: a bare Date.now() fallback changes every render, and the domain
  // effects below setState in the host — an update loop until data arrives.
  const [mountedAt] = useState(() => Date.now())
  // A LIVE selection's right edge tracks now (host's 30s tick), which runs ahead
  // of the overview's dataUpdatedAt (≤60s stale). Take the max so the domain
  // never trails the selection and the clamp effect can't rewrite a live edge
  // backward every tick (mode fight).
  const now = Math.max(overview.dataUpdatedAt || mountedAt, selection.toMs)
  // Stable empty fallback: a fresh [] literal here changes the gaps memo's
  // identity every pre-data render, and the onGapsChange lift effect keyed on
  // it would setState the host into an update loop.
  const hourBuckets = overview.data?.buckets ?? EMPTY_BUCKETS
  const availableFromMs = overview.data?.availableFromMs

  const domain = useMemo<ScrubberRange>(() => {
    // Clamp the domain floor to the queryable window. availableFromMs can point
    // at ancient synthesized-historical event times (resource creation dates on
    // long-lived clusters), which would stretch the strip over years of
    // unreachable nothing — the UI can never brush past maxRangeDays anyway.
    const floor = now - maxRangeDays * DAY_MS
    const fromMs = availableFromMs != null ? Math.max(availableFromMs, floor) : floor
    return { fromMs: Math.min(fromMs, now - HOUR_MS), toMs: now }
  }, [availableFromMs, now, maxRangeDays])

  const domainWidth = domain.toMs - domain.fromMs
  const maxSelectionMs = Math.min(MAX_SELECTION_MS, domainWidth)

  // The histogram spans the QUERY RANGE (selection) directly —
  // no ×8 framing, no minimap. The query is the view, so a narrow window is never
  // a sub-pixel sliver; the draggable lens band lives inside this span. Navigating
  // to other times is the query-range picker's job, not a spatial minimap's.
  const displayDomain = useMemo<ScrubberRange>(
    () => ({ fromMs: selection.fromMs, toMs: selection.toMs }),
    [selection.fromMs, selection.toMs],
  )
  const displayWidth = displayDomain.toMs - displayDomain.fromMs
  // Bucket size follows the WINDOW (lens), like the local strip: zooming the
  // window into a slice of a wide query re-buckets the histogram to match the
  // zoom level. Floored at the server rollup's hour granularity, and at a size
  // that keeps the whole query under ~MAX_STRIP_BARS bars.
  const lensWidthMs = lens ? Math.max(lens.toMs - lens.fromMs, HOUR_MS) : displayWidth
  const bucketSizeMs = Math.max(
    pickDisplayBucketSizeMs(lensWidthMs),
    Math.ceil(displayWidth / MAX_STRIP_BARS / HOUR_MS) * HOUR_MS,
  )

  const displayBuckets = useMemo(
    () => groupBuckets(hourBuckets, bucketSizeMs)
      .filter((b) => b.endMs > displayDomain.fromMs && b.startMs < displayDomain.toMs),
    [hourBuckets, bucketSizeMs, displayDomain.fromMs, displayDomain.toMs],
  )

  // Enrich a frozen chip with the count of events recorded after the frozen edge,
  // so the "Go live" CTA can pull the user toward the fresh data. Counted over
  // the FULL domain — the displayed span may cut off newer events. Live states
  // pass through unchanged.
  const fullBuckets = useMemo(() => groupBuckets(hourBuckets, HOUR_MS), [hourBuckets])
  const chipState = useMemo<TimelineLiveState | undefined>(() => {
    if (!liveState || liveState.kind !== 'frozen') return liveState
    return { ...liveState, newEventCount: countEventsAfter(fullBuckets, selection.toMs) }
  }, [liveState, fullBuckets, selection.toMs])
  const gaps = useMemo(() => extractRecordingGaps(hourBuckets, domain), [hourBuckets, domain])
  const presets = useMemo(() => buildPresets(maxRangeDays), [maxRangeDays])

  // Floor for dimming "not recorded yet": the first real event, clamped into the
  // visible domain. Below the domain floor (availableFromMs can predate the
  // retention window by years via synthesized historical event times) it collapses
  // to the domain start — the pre-retention dead zone is as unreachable as
  // pre-recording time. Above it (a fresh cluster whose oldest event is <1h old)
  // the [domain start .. first event] band dims as "not recorded yet" instead of
  // reading as a quiet hour.
  const historyFloorMs = availableFromMs != null
    ? Math.min(Math.max(availableFromMs, domain.fromMs), domain.toMs)
    : undefined

  // Lift the resolved domain + cap so the host can clamp extend requests to the
  // real retained window rather than an estimate.
  useEffect(() => {
    onDomainChange?.({ domain, maxSelectionMs })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [domain.fromMs, domain.toMs, maxSelectionMs])

  // Lift merged gaps so the swimlane can render matching offline bands + copy.
  useEffect(() => {
    onGapsChange?.(gaps)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [gaps])

  // Keep the controlled selection inside the (server-derived) domain. Fires once
  // the domain resolves or shrinks under the current selection. Route through
  // onSelectionClamp (falling back to onSelectionChange) so the host can tell
  // this apart from a user brush and keep LIVE mode alive.
  useEffect(() => {
    const { selection: clamped } = clampSelection(selection, domain, maxSelectionMs, 'end')
    if (clamped.fromMs !== selection.fromMs || clamped.toMs !== selection.toMs) {
      ;(onSelectionClamp ?? onSelectionChange)(clamped)
    }
    // Selection endpoints included: an externally-set selection (URL restore,
    // back-nav) outside the domain must clamp even when the domain itself
    // didn't change. Loop-safe — once clamped, the comparison is equal.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [domain.fromMs, domain.toMs, maxSelectionMs, selection.fromMs, selection.toMs])

  if (overview.isError) {
    return (
      <div className="flex items-center gap-2 px-4 py-1.5 text-xs text-theme-text-tertiary border-b border-theme-border bg-theme-surface">
        <AlertTriangle className="w-3.5 h-3.5 text-amber-400/70" />
        <span>Failed to load timeline overview</span>
        <button
          onClick={() => overview.refetch()}
          className="flex items-center gap-1 text-theme-text-secondary hover:text-theme-text-primary transition-colors"
        >
          <RefreshCw className="w-3 h-3" />
          Retry
        </button>
      </div>
    )
  }

  return (
    <div className="@container px-4 py-2 border-b border-theme-border bg-theme-surface">
      <TimelineStrip
        buckets={displayBuckets}
        loading={overview.isLoading}
        gaps={gaps}
        domain={domain}
        // No totalInQueryRange here — the hour-granular rollup can't produce an
        // exact count for an arbitrary sub-hour-aligned range, so the strip's
        // bucket-sum footer (± edge-bucket spillover) is the best available.
        historyUnavailableBeforeMs={historyFloorMs}
        selection={selection}
        onSelectionChange={onSelectionChange}
        maxSelectionMs={maxSelectionMs}
        presets={presets}
        onPresetSelect={(p) => (
          onPresetSelect
            ? onPresetSelect(p.ms)
            : onSelectionChange(presetToSelection(p.ms, now, domain, maxSelectionMs).selection)
        )}
        lens={lens}
        onLensChange={onLensChange}
        lensResizable={lensResizable}
        liveState={chipState}
        onLiveChipClick={onLiveChipClick}
      />
    </div>
  )
}
