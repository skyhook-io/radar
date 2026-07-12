import { useEffect, useMemo, useState } from 'react'
import {
  TimelineStrip,
  clampSelection,
  countEventsAfter,
  pickDisplayBucketSizeMs,
  presetToSelection,
  type ScrubberPreset,
  type ScrubberRange,
  type TimelineLiveState,
} from '@skyhook-io/k8s-ui'
import type { TimelineEvent } from '../../types'
import { localOverviewFromEvents } from '../../api/timelineSource'
import { groupBuckets, buildPresets, type ScrubberDomainInfo } from './RetainedTimelineScrubber'

const HOUR_MS = 60 * 60 * 1000
const MINUTE_MS = 60_000
// Cap on how many bars the strip draws for the full query, so window-driven fine
// bucketing can't explode into thousands of divs on a wide query + tiny window.
// Generous (thin bars are cheap) so even a 24h query keeps ~3min bars — fine
// enough that a narrow window spans many, and an empty window shows none.
const MAX_STRIP_BARS = 512

interface LocalTimelineScrubberProps {
  // The loaded event ring. The local store ships the whole ring to the browser,
  // so the histogram + domain are derived from it entirely client-side.
  events: TimelineEvent[]
  loading?: boolean
  selection: ScrubberRange
  onSelectionChange: (sel: ScrubberRange) => void
  // A domain clamp (the selection outgrew or fell outside the ring's span) is
  // NOT a user range action: routed here so the host can preserve LIVE mode
  // (narrowing the width) instead of freezing on first load of a short ring.
  onSelectionClamp?: (sel: ScrubberRange) => void
  onPresetSelect?: (widthMs: number) => void
  lens?: ScrubberRange
  onLensChange?: (lens: ScrubberRange) => void
  lensResizable?: boolean
  onDomainChange?: (info: ScrubberDomainInfo) => void
  liveState?: TimelineLiveState
  onLiveChipClick?: () => void
  // The host ring fetch failed. The strip has no fetch of its own — it derives
  // its histogram from `events` — so on failure it would paint a hollow
  // "0 events" strip beside the host's error pane. Hide it instead; the host owns
  // the error UI (mirrors RetainedTimelineScrubber, which hides its strip too).
  isError?: boolean
}

// The local-mode strip. Same control surface as the retained scrubber, but the
// overview is computed from the loaded ring (no server rollup) and the domain is
// the ring's actual span. Deliberately renders NO gap hatching: coverage is a
// retention concept — locally we cannot know what we missed while not watching,
// so an honest strip omits it rather than inventing gap bands.
export function LocalTimelineScrubber({
  events,
  loading,
  selection,
  onSelectionChange,
  onSelectionClamp,
  onPresetSelect,
  lens,
  onLensChange,
  lensResizable,
  onDomainChange,
  liveState,
  onLiveChipClick,
  isError,
}: LocalTimelineScrubberProps) {
  const overview = useMemo(() => localOverviewFromEvents(events), [events])
  const hourBuckets = overview.buckets
  const availableFromMs = overview.availableFromMs

  // When recording began: the oldest LIVE-fed event (informer / k8s event).
  // Historical events are synthesized from resource metadata and can be years
  // old, so they don't mark where the ring's real coverage starts. The strip
  // dims the query region before this — "Radar wasn't watching yet" is the
  // honest answer to "why can't I scroll further back".
  const recordingStartMs = useMemo(() => {
    let min: number | null = null
    for (const e of events) {
      if (e.source === 'historical') continue
      const t = new Date(e.timestamp).getTime()
      if (Number.isFinite(t) && (min == null || t < min)) min = t
    }
    return min ?? undefined
  }, [events])

  // Stable "now" between renders so the domain doesn't jitter. A LIVE selection's
  // right edge tracks the host's tick, so take the max — the domain never trails
  // the selection (which would fight the clamp effect every tick).
  const [mountedAt] = useState(() => Date.now())
  const now = Math.max(mountedAt, selection.toMs)

  // Domain floor = the oldest event held in the ring (the span we actually have),
  // clamped so the strip always has at least an hour of width. No retention
  // horizon: the whole ring is in memory, so the domain is exactly what we hold.
  const domain = useMemo<ScrubberRange>(() => {
    const fromMs = availableFromMs != null ? Math.min(availableFromMs, now - HOUR_MS) : now - HOUR_MS
    return { fromMs, toMs: now }
  }, [availableFromMs, now])

  const domainWidth = domain.toMs - domain.fromMs
  // No per-request cap locally: the full ring is already loaded, so a brush can
  // span the entire domain.
  const maxSelectionMs = domainWidth

  // The histogram spans the QUERY RANGE (selection) directly —
  // no framing, no minimap. The query is the view, so a narrow window is never a
  // sub-pixel sliver; the draggable lens band lives inside this span.
  const displayDomain = useMemo<ScrubberRange>(
    () => ({ fromMs: selection.fromMs, toMs: selection.toMs }),
    [selection.fromMs, selection.toMs],
  )
  const displayWidth = displayDomain.toMs - displayDomain.fromMs

  // Bar granularity is driven by the WINDOW, not the whole query: a narrow window
  // on a 24h query must still span many fine bars, or it sits over one coarse bar
  // and an EMPTY window reads as populated (and midpoint colouring mislabels it).
  // The whole ring is in the browser, so we can rebucket raw events sub-hour.
  // Floor keeps the query itself from exceeding ~MAX_STRIP_BARS.
  const lensWidthMs = lens ? Math.max(lens.toMs - lens.fromMs, MINUTE_MS) : displayWidth
  const bucketSizeMs = Math.max(
    pickDisplayBucketSizeMs(lensWidthMs, MINUTE_MS),
    Math.ceil(displayWidth / MAX_STRIP_BARS / MINUTE_MS) * MINUTE_MS,
  )

  const displayBuckets = useMemo(() => {
    const source = bucketSizeMs >= HOUR_MS
      ? hourBuckets
      : localOverviewFromEvents(events, bucketSizeMs).buckets
    return groupBuckets(source, bucketSizeMs)
      .filter((b) => b.endMs > displayDomain.fromMs && b.startMs < displayDomain.toMs)
  }, [hourBuckets, bucketSizeMs, events, displayDomain.fromMs, displayDomain.toMs])

  // Enrich a frozen chip with the count of events after the frozen edge, so the
  // "Go live" CTA can pull the user toward fresh data. Counted over the FULL
  // domain — the displayed span may cut off newer events. Live states pass
  // through.
  const fullBuckets = useMemo(() => groupBuckets(hourBuckets, HOUR_MS), [hourBuckets])
  const chipState = useMemo<TimelineLiveState | undefined>(() => {
    if (!liveState || liveState.kind !== 'frozen') return liveState
    return { ...liveState, newEventCount: countEventsAfter(fullBuckets, selection.toMs) }
  }, [liveState, fullBuckets, selection.toMs])

  // Presets clamp to the domain: only offer windows the ring can actually fill,
  // so we never advertise 7d of history on a 20-minute-old cluster. Always keep
  // at least the smallest so there is a preset to click.
  const presets = useMemo<ScrubberPreset[]>(() => {
    const all = buildPresets(30)
    const fit = all.filter((p) => p.ms <= domainWidth)
    const base = fit.length > 0 ? fit : [all[0]]
    // One-click whole-ring selection. The strip caps a single brush at the
    // displayed span, so "everything we hold" needs a first-class control.
    return [...base, { label: 'All', ms: domainWidth }]
  }, [domainWidth])

  // Lift the resolved domain + cap so the host can clamp extend requests.
  useEffect(() => {
    onDomainChange?.({ domain, maxSelectionMs })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [domain.fromMs, domain.toMs, maxSelectionMs])

  // Keep the controlled selection inside the derived domain. Fires when the
  // domain resolves or shrinks under the current selection (e.g. a short ring).
  // Route through onSelectionClamp (falling back to onSelectionChange) so the
  // host can tell this apart from a user brush and keep LIVE mode alive.
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

  // A failed ring fetch would render a hollow "0 events" strip next to the host's
  // error pane. Hide instead — checked after all hooks so hook order is stable.
  if (isError) return null

  return (
    <div className="@container px-4 py-2 border-b border-theme-border bg-theme-surface">
      <TimelineStrip
        buckets={displayBuckets}
        loading={loading}
        domain={domain}
        historyUnavailableBeforeMs={recordingStartMs}
        // Exact count with the SAME predicate the toolbar chips use — edge
        // buckets spill a few events, so a bucket sum tells a second story.
        totalInQueryRange={events.reduce((n, e) => {
          const t = new Date(e.timestamp).getTime()
          return t >= selection.fromMs && t <= selection.toMs ? n + 1 : n
        }, 0)}
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
