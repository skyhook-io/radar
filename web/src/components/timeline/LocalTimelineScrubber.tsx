import { useEffect, useMemo, useState } from 'react'
import {
  TimelineScrubber,
  SWIMLANE_ZOOM_LEVELS,
  clampSelection,
  countEventsAfter,
  formatLensDuration,
  pickDisplayBucketSizeMs,
  presetToSelection,
  type ScrubberPreset,
  type ScrubberRange,
  type TimelineLiveState,
} from '@skyhook-io/k8s-ui'
import type { TimelineEvent } from '../../types'
import { localOverviewFromEvents } from '../../api/timelineSource'
import { groupBuckets, buildPresets, frameDomainForSelection, type ScrubberDomainInfo } from './RetainedTimelineScrubber'

const HOUR_MS = 60 * 60 * 1000
const MINUTE_MS = 60_000

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
  onDomainChange?: (info: ScrubberDomainInfo) => void
  liveState?: TimelineLiveState
  onLiveChipClick?: () => void
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
  onDomainChange,
  liveState,
  onLiveChipClick,
}: LocalTimelineScrubberProps) {
  const overview = useMemo(() => localOverviewFromEvents(events), [events])
  const hourBuckets = overview.buckets
  const availableFromMs = overview.availableFromMs

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

  // The strip DISPLAYS a framed sub-range of the domain — otherwise a narrow
  // selection (and the lens inside it) collapses to a sub-pixel sliver.
  // Derived, so a live selection sliding forward carries its frame along. One
  // rule, no modes: the minimap row above the track is the stable full-span
  // anchor, and clicking it jumps the selection (which re-frames here).
  const displayDomain = useMemo(
    () => frameDomainForSelection(selection, domain),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [selection.fromMs, selection.toMs, domain.fromMs, domain.toMs],
  )
  const displayWidth = displayDomain.toMs - displayDomain.fromMs

  // The whole ring is in the browser, so a tightly framed window can rebucket
  // the raw events at sub-hour granularity instead of stretching hour bars.
  const bucketSizeMs = pickDisplayBucketSizeMs(displayWidth, MINUTE_MS)

  const displayBuckets = useMemo(() => {
    const source = bucketSizeMs >= HOUR_MS
      ? hourBuckets
      : localOverviewFromEvents(events, bucketSizeMs).buckets
    return groupBuckets(source, bucketSizeMs)
      .filter((b) => b.endMs > displayDomain.fromMs && b.startMs < displayDomain.toMs)
  }, [hourBuckets, bucketSizeMs, events, displayDomain.fromMs, displayDomain.toMs])

  // Enrich a frozen chip with the count of events after the frozen edge, so the
  // "Go live" CTA can pull the user toward fresh data. Counted over the FULL
  // domain — the framed display may cut off newer events. Live states pass
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
    // One-click whole-ring selection. The framed strip caps a single brush at
    // the frame width, so "everything we hold" needs a first-class control.
    return [...base, { label: 'All', ms: domainWidth }]
  }, [domainWidth])

  // Lens-chip width ladder: the swimlane's zoom rungs, capped to the current
  // selection — the lens can never show more than what's selected.
  const lensPresets = useMemo<ScrubberPreset[]>(() => {
    const selWidth = selection.toMs - selection.fromMs
    return SWIMLANE_ZOOM_LEVELS
      .map((h) => h * HOUR_MS)
      .filter((ms) => ms <= selWidth)
      .map((ms) => ({ label: formatLensDuration(ms), ms }))
  }, [selection.fromMs, selection.toMs])

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

  return (
    <div className="px-4 py-2 border-b border-theme-border bg-theme-surface">
      <TimelineScrubber
        buckets={displayBuckets}
        loading={loading}
        domain={displayDomain}
        selection={selection}
        onSelectionChange={onSelectionChange}
        maxSelectionMs={maxSelectionMs}
        presets={presets}
        onPresetSelect={(p) => (
          onPresetSelect
            ? onPresetSelect(p.ms)
            : onSelectionChange(presetToSelection(p.ms, now, domain, maxSelectionMs).selection)
        )}
        fullDomain={domain}
        fullBuckets={fullBuckets}
        onMinimapJump={(centerMs) => {
          const width = selection.toMs - selection.fromMs
          onSelectionChange(clampSelection(
            { fromMs: centerMs - width / 2, toMs: centerMs + width / 2 },
            domain, maxSelectionMs, 'center',
          ).selection)
        }}
        lens={lens}
        onLensChange={onLensChange}
        lensPresets={lensPresets}
        liveState={chipState}
        onLiveChipClick={onLiveChipClick}
      />
    </div>
  )
}
