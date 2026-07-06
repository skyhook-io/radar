import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import {
  findBucketAt,
  ScrubberPendingOverlay,
  TimelineScrubber,
  applyScrubberCommand,
  selectionChangeClearsPending,
  barHeight,
  clampSelection,
  clampLensToSelection,
  countEventsAfter,
  shouldShowLensBand,
  dragExceedsThreshold,
  formatLensDuration,
  formatScrubberPillPrecise,
  layoutPendingPillCenters,
  mergeGapRanges,
  panSelection,
  pickDisplayBucketSizeMs,
  presetToSelection,
  setLensWidth,
  stepLensWidth,
  stepSelection,
  zoomSelection,
  type ScrubberRange,
  type ScrubberBucket,
} from './TimelineScrubber'

const MIN = 60_000
const HOUR = 60 * 60 * 1000
const DAY = 24 * HOUR
const NOW = 1_700_000_000_000
const DOMAIN: ScrubberRange = { fromMs: NOW - 30 * DAY, toMs: NOW }

function countMatches(html: string, needle: string): number {
  return html.split(needle).length - 1
}

describe('barHeight (bucket → bar mapping)', () => {
  it('is zero for empty buckets or empty data', () => {
    expect(barHeight(0, 100, 44)).toBe(0)
    expect(barHeight(5, 0, 44)).toBe(0)
  })

  it('gives a sparse bar a visible floor instead of a sub-pixel sliver', () => {
    const h = barHeight(1, 10_000, 44)
    expect(h).toBeGreaterThanOrEqual(Math.max(2, 44 * 0.08))
  })

  it('caps at the track height and grows monotonically with total', () => {
    expect(barHeight(10_000, 10_000, 44)).toBeLessThanOrEqual(44)
    expect(barHeight(100, 10_000, 44)).toBeLessThan(barHeight(2_500, 10_000, 44))
  })
})

describe('clampSelection (domain + maxSelectionMs)', () => {
  it('caps width at maxSelectionMs and flags it, keeping the right edge', () => {
    const sel: ScrubberRange = { fromMs: NOW - 10 * DAY, toMs: NOW }
    const { selection, clampedToMax } = clampSelection(sel, DOMAIN, 7 * DAY, 'end')
    expect(clampedToMax).toBe(true)
    expect(selection.toMs).toBe(NOW)
    expect(selection.toMs - selection.fromMs).toBe(7 * DAY)
  })

  it('shifts a selection that runs past the domain back inside it', () => {
    const sel: ScrubberRange = { fromMs: NOW - HOUR, toMs: NOW + 5 * DAY }
    const { selection } = clampSelection(sel, DOMAIN, 7 * DAY, 'end')
    expect(selection.toMs).toBeLessThanOrEqual(DOMAIN.toMs)
    expect(selection.fromMs).toBeGreaterThanOrEqual(DOMAIN.fromMs)
  })

  it('enforces a minimum width so handles never overlap', () => {
    const sel: ScrubberRange = { fromMs: NOW - 1000, toMs: NOW }
    const { selection } = clampSelection(sel, DOMAIN, 7 * DAY, 'end')
    expect(selection.toMs - selection.fromMs).toBeGreaterThanOrEqual(MIN)
  })

  it('does not flag clampedToMax when the selection already fits', () => {
    const sel: ScrubberRange = { fromMs: NOW - 2 * DAY, toMs: NOW }
    const { clampedToMax } = clampSelection(sel, DOMAIN, 7 * DAY, 'end')
    expect(clampedToMax).toBe(false)
  })
})

describe('clampLensToSelection (lens ⊂ selection)', () => {
  const SEL: ScrubberRange = { fromMs: NOW - 4 * HOUR, toMs: NOW }

  it('leaves a lens already inside the selection untouched', () => {
    const lens: ScrubberRange = { fromMs: NOW - 3 * HOUR, toMs: NOW - 2 * HOUR }
    expect(clampLensToSelection(lens, SEL)).toEqual(lens)
  })

  it('shifts an overhanging lens back inside, preserving its width', () => {
    const lens: ScrubberRange = { fromMs: NOW - HOUR, toMs: NOW + 2 * HOUR }
    const width = lens.toMs - lens.fromMs
    const clamped = clampLensToSelection(lens, SEL)
    expect(clamped.toMs).toBe(SEL.toMs)
    expect(clamped.toMs - clamped.fromMs).toBe(width)
    expect(clamped.fromMs).toBeGreaterThanOrEqual(SEL.fromMs)
  })

  it('shifts a lens hanging off the left edge back inside', () => {
    const lens: ScrubberRange = { fromMs: NOW - 6 * HOUR, toMs: NOW - 5 * HOUR }
    const width = lens.toMs - lens.fromMs
    const clamped = clampLensToSelection(lens, SEL)
    expect(clamped.fromMs).toBe(SEL.fromMs)
    expect(clamped.toMs - clamped.fromMs).toBe(width)
  })

  it('collapses a lens wider than the selection to the full selection', () => {
    const lens: ScrubberRange = { fromMs: NOW - 10 * HOUR, toMs: NOW + 5 * HOUR }
    expect(clampLensToSelection(lens, SEL)).toEqual({ fromMs: SEL.fromMs, toMs: SEL.toMs })
  })
})

describe('formatLensDuration (chip label)', () => {
  it('renders sub-hour widths as minutes', () => {
    expect(formatLensDuration(15 * 60_000)).toBe('15m')
    expect(formatLensDuration(30 * 60_000)).toBe('30m')
  })
  it('renders hour widths as hours, days as days', () => {
    expect(formatLensDuration(HOUR)).toBe('1h')
    expect(formatLensDuration(8 * HOUR)).toBe('8h')
    expect(formatLensDuration(DAY)).toBe('1d')
    expect(formatLensDuration(7 * DAY)).toBe('7d')
  })
})

describe('setLensWidth (centered resize, clamped)', () => {
  const SEL: ScrubberRange = { fromMs: NOW - 24 * HOUR, toMs: NOW }

  it('resizes around the current center, preserving the midpoint', () => {
    const lens: ScrubberRange = { fromMs: NOW - 5 * HOUR, toMs: NOW - 3 * HOUR } // center = NOW-4h
    const center = (lens.fromMs + lens.toMs) / 2
    const next = setLensWidth(lens, 8 * HOUR, SEL)
    expect(next.toMs - next.fromMs).toBe(8 * HOUR)
    expect((next.fromMs + next.toMs) / 2).toBe(center)
  })

  it('never grows wider than the selection', () => {
    const lens: ScrubberRange = { fromMs: NOW - 3 * HOUR, toMs: NOW - HOUR }
    const next = setLensWidth(lens, 48 * HOUR, SEL)
    expect(next).toEqual({ fromMs: SEL.fromMs, toMs: SEL.toMs })
  })

  it('never shrinks below the minimum lens width', () => {
    const lens: ScrubberRange = { fromMs: NOW - 2 * HOUR, toMs: NOW - HOUR }
    const next = setLensWidth(lens, 1000, SEL)
    expect(next.toMs - next.fromMs).toBe(MIN)
  })

  it('shifts a centered resize back inside when it overhangs an edge', () => {
    const lens: ScrubberRange = { fromMs: NOW - 30 * 60_000, toMs: NOW } // hugs the right edge
    const next = setLensWidth(lens, 4 * HOUR, SEL)
    expect(next.toMs - next.fromMs).toBe(4 * HOUR)
    expect(next.toMs).toBeLessThanOrEqual(SEL.toMs)
    expect(next.fromMs).toBeGreaterThanOrEqual(SEL.fromMs)
  })
})

describe('stepLensWidth (ladder + factor stepping)', () => {
  const SEL: ScrubberRange = { fromMs: NOW - 7 * DAY, toMs: NOW }
  const LADDER = [0.25, 0.5, 1, 2, 4, 8, 12, 24].map((h) => h * HOUR)

  it('widens to the next-larger ladder rung, centered', () => {
    const lens: ScrubberRange = { fromMs: NOW - 3 * HOUR, toMs: NOW - HOUR } // 2h, center NOW-2h
    const center = (lens.fromMs + lens.toMs) / 2
    const next = stepLensWidth(lens, LADDER, 1, SEL)
    expect(next.toMs - next.fromMs).toBe(4 * HOUR)
    expect((next.fromMs + next.toMs) / 2).toBe(center)
  })

  it('narrows to the next-smaller ladder rung', () => {
    const lens: ScrubberRange = { fromMs: NOW - 5 * HOUR, toMs: NOW - HOUR } // 4h
    const next = stepLensWidth(lens, LADDER, -1, SEL)
    expect(next.toMs - next.fromMs).toBe(2 * HOUR)
  })

  it('stays on the smallest rung at the narrow bound', () => {
    const lens: ScrubberRange = { fromMs: NOW - 15 * 60_000, toMs: NOW } // 15m = smallest rung
    const next = stepLensWidth(lens, LADDER, -1, SEL)
    expect(next.toMs - next.fromMs).toBe(15 * 60_000)
  })

  it('stays on the largest rung at the wide bound', () => {
    const lens: ScrubberRange = { fromMs: NOW - 24 * HOUR, toMs: NOW } // 24h = largest rung
    const next = stepLensWidth(lens, LADDER, 1, SEL)
    expect(next.toMs - next.fromMs).toBe(24 * HOUR)
  })

  it('falls back to doubling / halving when no ladder is given', () => {
    const lens: ScrubberRange = { fromMs: NOW - 3 * HOUR, toMs: NOW - HOUR } // 2h
    expect(stepLensWidth(lens, 2, 1, SEL).toMs - stepLensWidth(lens, 2, 1, SEL).fromMs).toBe(4 * HOUR)
    expect(stepLensWidth(lens, 2, -1, SEL).toMs - stepLensWidth(lens, 2, -1, SEL).fromMs).toBe(HOUR)
  })

  it('clamps a ladder step to the selection width', () => {
    const narrowSel: ScrubberRange = { fromMs: NOW - 3 * HOUR, toMs: NOW } // 3h selection
    const lens: ScrubberRange = { fromMs: NOW - 3 * HOUR, toMs: NOW } // already full
    const next = stepLensWidth(lens, LADDER, 1, narrowSel)
    expect(next.toMs - next.fromMs).toBeLessThanOrEqual(3 * HOUR)
  })
})

describe('shouldShowLensBand (pending keeps visual priority)', () => {
  const lens: ScrubberRange = { fromMs: NOW - HOUR, toMs: NOW }
  const pending: ScrubberRange = { fromMs: NOW - 2 * HOUR, toMs: NOW }

  it('shows when a lens is set and nothing is staged', () => {
    expect(shouldShowLensBand(lens, null)).toBe(true)
  })

  it('hides while a pending brush is staged', () => {
    expect(shouldShowLensBand(lens, pending)).toBe(false)
  })

  it('hides when no lens is provided', () => {
    expect(shouldShowLensBand(undefined, null)).toBe(false)
    expect(shouldShowLensBand(null, null)).toBe(false)
  })
})

describe('presetToSelection', () => {
  it('sets the brush to [now - ms, now]', () => {
    const { selection } = presetToSelection(DAY, NOW, DOMAIN, 7 * DAY)
    expect(selection.toMs).toBe(NOW)
    expect(selection.toMs - selection.fromMs).toBe(DAY)
  })

  it('clamps a preset wider than maxSelectionMs (e.g. 30d → 7d cap)', () => {
    const { selection, clampedToMax } = presetToSelection(30 * DAY, NOW, DOMAIN, 7 * DAY)
    expect(clampedToMax).toBe(true)
    expect(selection.toMs - selection.fromMs).toBe(7 * DAY)
  })
})

describe('pan / step / zoom (keyboard + button nudges)', () => {
  it('pans while preserving width and clamps at the domain edge', () => {
    const sel: ScrubberRange = { fromMs: NOW - 2 * DAY, toMs: NOW - DAY }
    const width = sel.toMs - sel.fromMs
    const panned = panSelection(sel, HOUR, DOMAIN)
    expect(panned.toMs - panned.fromMs).toBe(width)
    expect(panned.fromMs).toBe(sel.fromMs + HOUR)

    // Pushing past the right edge pins to the domain without changing width.
    const pinned = panSelection(sel, 5 * DAY, DOMAIN)
    expect(pinned.toMs).toBe(DOMAIN.toMs)
    expect(pinned.toMs - pinned.fromMs).toBe(width)
  })

  it('steps by the selection width', () => {
    const sel: ScrubberRange = { fromMs: NOW - 4 * DAY, toMs: NOW - 3 * DAY }
    const stepped = stepSelection(sel, 1, DOMAIN)
    expect(stepped.fromMs).toBe(NOW - 3 * DAY)
    expect(stepped.toMs).toBe(NOW - 2 * DAY)
  })

  it('zooms around the center', () => {
    const sel: ScrubberRange = { fromMs: NOW - 3 * DAY, toMs: NOW - DAY }
    const center = (sel.fromMs + sel.toMs) / 2
    const { selection } = zoomSelection(sel, 0.5, DOMAIN, 7 * DAY)
    expect((selection.fromMs + selection.toMs) / 2).toBe(center)
    expect(selection.toMs - selection.fromMs).toBe(DAY)
  })
})

describe('TimelineScrubber rendering', () => {
  const buckets: ScrubberBucket[] = [
    { startMs: NOW - 3 * HOUR, endMs: NOW - 2 * HOUR, total: 12, warnings: 4 },
    { startMs: NOW - 2 * HOUR, endMs: NOW - HOUR, total: 0, warnings: 0 },
    { startMs: NOW - HOUR, endMs: NOW, total: 30, warnings: 0 },
  ]
  const selection: ScrubberRange = { fromMs: NOW - 2 * HOUR, toMs: NOW }

  it('renders one bar per non-empty bucket', () => {
    const html = renderToString(
      <TimelineScrubber
        buckets={buckets}
        domain={{ fromMs: NOW - 4 * HOUR, toMs: NOW }}
        selection={selection}
        onSelectionChange={() => {}}
      />,
    )
    expect(countMatches(html, 'data-testid="scrubber-bar"')).toBe(2)
  })

  it('renders a hatched, inert gap band from the gaps prop', () => {
    const html = renderToString(
      <TimelineScrubber
        buckets={buckets}
        gaps={[{ fromMs: NOW - 3 * HOUR, toMs: NOW - 2 * HOUR }]}
        domain={{ fromMs: NOW - 4 * HOUR, toMs: NOW }}
        selection={selection}
        onSelectionChange={() => {}}
      />,
    )
    expect(countMatches(html, 'data-testid="scrubber-gap"')).toBe(1)
    expect(html).toContain('No data recorded — connector was offline')
    expect(html).toContain('pointer-events-none')
    expect(html).toContain('repeating-linear-gradient')
  })

  it('still renders a gap band from the legacy coverage prop', () => {
    const html = renderToString(
      <TimelineScrubber
        buckets={buckets}
        coverage={[{ startMs: NOW - 3 * HOUR, endMs: NOW - 2 * HOUR, reason: 'restart' }]}
        domain={{ fromMs: NOW - 4 * HOUR, toMs: NOW }}
        selection={selection}
        onSelectionChange={() => {}}
      />,
    )
    expect(countMatches(html, 'data-testid="scrubber-gap"')).toBe(1)
  })

  it('dims the pre-retention region when historyUnavailableBeforeMs is set', () => {
    const html = renderToString(
      <TimelineScrubber
        buckets={buckets}
        historyUnavailableBeforeMs={NOW - 2 * HOUR}
        domain={{ fromMs: NOW - 4 * HOUR, toMs: NOW }}
        selection={selection}
        onSelectionChange={() => {}}
      />,
    )
    expect(html).toContain('data-testid="scrubber-preretention"')
  })

  it('renders preset chips', () => {
    const html = renderToString(
      <TimelineScrubber
        buckets={buckets}
        domain={{ fromMs: NOW - 30 * DAY, toMs: NOW }}
        selection={selection}
        onSelectionChange={() => {}}
        presets={[{ label: '1h', ms: HOUR }, { label: '7d', ms: 7 * DAY }]}
        onPresetSelect={() => {}}
      />,
    )
    expect(html).toContain('1h')
    expect(html).toContain('7d')
  })

  it('exposes focusable slider handles with aria labels', () => {
    const html = renderToString(
      <TimelineScrubber
        buckets={buckets}
        domain={{ fromMs: NOW - 4 * HOUR, toMs: NOW }}
        selection={selection}
        onSelectionChange={() => {}}
      />,
    )
    expect(html).toContain('aria-label="Selection start"')
    expect(html).toContain('aria-label="Selection end"')
    expect(countMatches(html, 'role="slider"')).toBe(2)
  })

  it('renders the lens band when a lens prop is set', () => {
    const html = renderToString(
      <TimelineScrubber
        buckets={buckets}
        domain={{ fromMs: NOW - 4 * HOUR, toMs: NOW }}
        selection={selection}
        onSelectionChange={() => {}}
        lens={{ fromMs: NOW - HOUR, toMs: NOW }}
        onLensChange={() => {}}
      />,
    )
    expect(html).toContain('data-testid="scrubber-lens"')
    expect(html).toContain('aria-label="Visible window within the selection"')
  })

  it('renders the lens-width chip when lens + onLensChange are set', () => {
    const html = renderToString(
      <TimelineScrubber
        buckets={buckets}
        domain={{ fromMs: NOW - 4 * HOUR, toMs: NOW }}
        selection={selection}
        onSelectionChange={() => {}}
        lens={{ fromMs: NOW - HOUR, toMs: NOW }}
        onLensChange={() => {}}
        lensPresets={[{ label: '15m', ms: 15 * 60 * 1000 }, { label: '1h', ms: HOUR }]}
      />,
    )
    expect(html).toContain('data-testid="scrubber-lens-chip"')
    expect(html).toContain('aria-label="Narrow view window"')
    expect(html).toContain('aria-label="Widen view window"')
    expect(html).toContain('aria-label="View window duration — choose preset"')
    // Center label shows the lens' human duration (1h lens).
    expect(html).toContain('1h')
  })

  it('does not render the chip without onLensChange (read-only lens)', () => {
    const html = renderToString(
      <TimelineScrubber
        buckets={buckets}
        domain={{ fromMs: NOW - 4 * HOUR, toMs: NOW }}
        selection={selection}
        onSelectionChange={() => {}}
        lens={{ fromMs: NOW - HOUR, toMs: NOW }}
      />,
    )
    expect(html).not.toContain('data-testid="scrubber-lens-chip"')
  })

  it('does not render the preset popover on the closed (initial) chip render', () => {
    const html = renderToString(
      <TimelineScrubber
        buckets={buckets}
        domain={{ fromMs: NOW - 4 * HOUR, toMs: NOW }}
        selection={selection}
        onSelectionChange={() => {}}
        lens={{ fromMs: NOW - HOUR, toMs: NOW }}
        onLensChange={() => {}}
        lensPresets={[{ label: '15m', ms: 15 * 60 * 1000 }, { label: '1h', ms: HOUR }]}
      />,
    )
    // The menu is state-driven — closed on first paint.
    expect(html).not.toContain('data-testid="scrubber-lens-preset-menu"')
  })

  it('renders the chip label as static text (no preset button) when lensPresets is absent', () => {
    const html = renderToString(
      <TimelineScrubber
        buckets={buckets}
        domain={{ fromMs: NOW - 4 * HOUR, toMs: NOW }}
        selection={selection}
        onSelectionChange={() => {}}
        lens={{ fromMs: NOW - HOUR, toMs: NOW }}
        onLensChange={() => {}}
      />,
    )
    expect(html).toContain('data-testid="scrubber-lens-chip"')
    expect(html).not.toContain('aria-label="View window duration — choose preset"')
  })

  it('omits the lens band when no lens prop is set', () => {
    const html = renderToString(
      <TimelineScrubber
        buckets={buckets}
        domain={{ fromMs: NOW - 4 * HOUR, toMs: NOW }}
        selection={selection}
        onSelectionChange={() => {}}
      />,
    )
    expect(html).not.toContain('data-testid="scrubber-lens"')
  })

  it('does not render the pending popover on the initial (applied-only) render', () => {
    const html = renderToString(
      <TimelineScrubber
        buckets={buckets}
        domain={{ fromMs: NOW - 4 * HOUR, toMs: NOW }}
        selection={selection}
        onSelectionChange={() => {}}
      />,
    )
    // Popover renders only while a pending selection exists — none on first paint.
    expect(html).not.toContain('scrubber-pending-popover')
    expect(html).not.toContain('Run query')
  })
})

describe('applyScrubberCommand (staged-commit contract)', () => {
  const staged: ScrubberRange = { fromMs: NOW - 2 * HOUR, toMs: NOW }

  it('stages a brush mutation without committing (query not run)', () => {
    const { state, commit } = applyScrubberCommand({ pending: null }, { kind: 'brush', range: staged })
    expect(commit).toBeNull()
    expect(state.pending).toEqual(staged)
  })

  it('run commits the pending range exactly once and clears it', () => {
    const { state, commit } = applyScrubberCommand({ pending: staged }, { kind: 'run' })
    expect(commit).toEqual(staged)
    expect(state.pending).toBeNull()
  })

  it('run with nothing staged is a no-op (never fires a commit)', () => {
    const { state, commit } = applyScrubberCommand({ pending: null }, { kind: 'run' })
    expect(commit).toBeNull()
    expect(state.pending).toBeNull()
  })

  it('reset discards the pending range and never commits', () => {
    const { state, commit } = applyScrubberCommand({ pending: staged }, { kind: 'reset' })
    expect(commit).toBeNull()
    expect(state.pending).toBeNull()
  })

  it('apply (preset/step/zoom) commits immediately and clears any pending', () => {
    const next: ScrubberRange = { fromMs: NOW - HOUR, toMs: NOW }
    const { state, commit } = applyScrubberCommand({ pending: staged }, { kind: 'apply', range: next })
    expect(commit).toEqual(next)
    expect(state.pending).toBeNull()
  })

  it('sync (external selection change) drops pending without committing', () => {
    const { state, commit } = applyScrubberCommand({ pending: staged }, { kind: 'sync' })
    expect(commit).toBeNull()
    expect(state.pending).toBeNull()
  })
})

describe('selectionChangeClearsPending (live tick must not wipe a staged brush)', () => {
  it('keeps pending while live so the 30s tick slide leaves the staged brush intact', () => {
    expect(selectionChangeClearsPending('live')).toBe(false)
  })
  it('clears pending on a genuine change when frozen or unset', () => {
    expect(selectionChangeClearsPending('frozen')).toBe(true)
    expect(selectionChangeClearsPending(undefined)).toBe(true)
  })
})

describe('grab-and-pan (middle drag)', () => {
  it('panning the applied range stages a pending with identical width, no commit', () => {
    const applied: ScrubberRange = { fromMs: NOW - 3 * DAY, toMs: NOW - DAY }
    const panned = panSelection(applied, HOUR, DOMAIN)
    const { state, commit } = applyScrubberCommand({ pending: null }, { kind: 'brush', range: panned })
    expect(commit).toBeNull()
    expect(state.pending).not.toBeNull()
    expect(state.pending!.toMs - state.pending!.fromMs).toBe(applied.toMs - applied.fromMs)
    expect(state.pending!.fromMs).toBe(applied.fromMs + HOUR)
  })

  it('pan clamps at both domain edges without changing width', () => {
    const sel: ScrubberRange = { fromMs: DOMAIN.fromMs + DAY, toMs: DOMAIN.fromMs + 2 * DAY }
    const left = panSelection(sel, -5 * DAY, DOMAIN)
    expect(left.fromMs).toBe(DOMAIN.fromMs)
    expect(left.toMs - left.fromMs).toBe(DAY)

    const right = panSelection(sel, 60 * DAY, DOMAIN)
    expect(right.toMs).toBe(DOMAIN.toMs)
    expect(right.toMs - right.fromMs).toBe(DAY)
  })

  it('sub-threshold movement is a plain click: drag never latches, nothing staged', () => {
    // Below the threshold the move handler bails before dispatching any brush
    // command — no zero-delta pending, no popover flash.
    expect(dragExceedsThreshold(100, 100)).toBe(false)
    expect(dragExceedsThreshold(100, 102)).toBe(false)
    expect(dragExceedsThreshold(100, 98)).toBe(false)
    // At/over the threshold the drag latches (either direction).
    expect(dragExceedsThreshold(100, 103)).toBe(true)
    expect(dragExceedsThreshold(100, 97)).toBe(true)
  })
})

describe('formatScrubberPillPrecise', () => {
  it('includes seconds so pending edges are exact to the second', () => {
    const label = formatScrubberPillPrecise(NOW)
    // Two colons → HH:MM:SS (the compact pill has only one).
    expect(label.split(':').length - 1).toBe(2)
  })
})

describe('ScrubberPendingOverlay (staged rendering)', () => {
  const pending: ScrubberRange = { fromMs: NOW - 2 * HOUR, toMs: NOW }

  function render() {
    return renderToString(
      <ScrubberPendingOverlay
        fromX={120}
        toX={480}
        fromMs={pending.fromMs}
        toMs={pending.toMs}
        trackHeight={44}
        width={640}
        domain={DOMAIN}
        onStartPointerDown={() => {}}
        onEndPointerDown={() => {}}
        onStartKeyDown={() => {}}
        onEndKeyDown={() => {}}
        onRun={() => {}}
        onReset={() => {}}
      />,
    )
  }

  it('renders the Run/Reset popover', () => {
    const html = render()
    expect(html).toContain('scrubber-pending-popover')
    expect(html).toContain('Run query')
    expect(html).toContain('Reset')
  })

  it('renders a precise timestamp pill on each pending edge', () => {
    const html = render()
    expect(countMatches(html, 'scrubber-pending-pill')).toBe(2)
    expect(html).toContain(formatScrubberPillPrecise(pending.fromMs))
    expect(html).toContain(formatScrubberPillPrecise(pending.toMs))
  })

  it('renders dashed edge guides and aria-labelled dot handles', () => {
    const html = render()
    expect(countMatches(html, 'scrubber-pending-guide')).toBe(2)
    expect(html).toContain('aria-label="Pending selection start"')
    expect(html).toContain('aria-label="Pending selection end"')
    expect(countMatches(html, 'role="slider"')).toBe(2)
  })

  it('places pills below the baseline and the popover inside the plot area', () => {
    const html = render()
    const trackHeight = 44

    // Both pills anchor under the baseline — never in the popover's headroom.
    const pillTops = [...html.matchAll(
      /style="[^"]*?top:(-?\d+(?:\.\d+)?)px[^"]*?"[^>]*data-testid="scrubber-pending-pill"/g,
    )].map((m) => Number(m[1]))
    expect(pillTops).toHaveLength(2)
    for (const top of pillTops) expect(top).toBeGreaterThan(trackHeight)

    // The popover sits INSIDE the plot near the top edge — above the strip it
    // collides with the header controls, below with the pills.
    const popover = html.match(
      /style="[^"]*?top:(-?\d+(?:\.\d+)?)px[^"]*?"[^>]*data-testid="scrubber-pending-popover"/,
    )
    expect(popover).not.toBeNull()
    const top = Number(popover![1])
    expect(top).toBeGreaterThanOrEqual(0)
    expect(top).toBeLessThan(trackHeight)
  })
})

describe('layoutPendingPillCenters (bottom pill clamping)', () => {
  const W = 640
  const PILL = 84

  it('keeps pills centered on their edges when the range is wide and inside', () => {
    const { fromCenter, toCenter } = layoutPendingPillCenters(120, 480, W, PILL, PILL)
    expect(fromCenter).toBe(120)
    expect(toCenter).toBe(480)
  })

  it('shifts edge-hugging pills inward instead of clipping them', () => {
    const { fromCenter, toCenter } = layoutPendingPillCenters(0, W, W, PILL, PILL)
    expect(fromCenter).toBe(PILL / 2)
    expect(toCenter).toBe(W - PILL / 2)
  })

  it('pushes pills apart on a very narrow range so they never overlap', () => {
    const { fromCenter, toCenter } = layoutPendingPillCenters(320, 322, W, PILL, PILL)
    expect(toCenter - fromCenter).toBeGreaterThanOrEqual(PILL + 4)
    // Both remain fully inside the container.
    expect(fromCenter - PILL / 2).toBeGreaterThanOrEqual(0)
    expect(toCenter + PILL / 2).toBeLessThanOrEqual(W)
  })

  it('re-anchors a pushed-apart pair that lands on a container edge', () => {
    const { fromCenter, toCenter } = layoutPendingPillCenters(2, 4, W, PILL, PILL)
    expect(fromCenter - PILL / 2).toBeGreaterThanOrEqual(0)
    expect(toCenter - fromCenter).toBeGreaterThanOrEqual(PILL + 4)
  })
})

describe('mergeGapRanges (recording-gap dedupe / merge / clamp)', () => {
  it('drops zero-width spans', () => {
    const gaps = mergeGapRanges([
      { fromMs: 100, toMs: 100 },
      { fromMs: 500, toMs: 500 },
    ])
    expect(gaps).toEqual([])
  })

  it('dedupes identical spans', () => {
    const gaps = mergeGapRanges([
      { fromMs: 100, toMs: 200 },
      { fromMs: 100, toMs: 200 },
    ])
    expect(gaps).toEqual([{ fromMs: 100, toMs: 200 }])
  })

  it('merges overlapping and back-to-back spans into one band', () => {
    const gaps = mergeGapRanges([
      { fromMs: 100, toMs: 300 },
      { fromMs: 250, toMs: 400 }, // overlaps
      { fromMs: 400, toMs: 500 }, // adjacent
      { fromMs: 700, toMs: 800 }, // disjoint
    ])
    expect(gaps).toEqual([
      { fromMs: 100, toMs: 500 },
      { fromMs: 700, toMs: 800 },
    ])
  })

  it('normalizes reversed edges before merging', () => {
    const gaps = mergeGapRanges([{ fromMs: 300, toMs: 100 }])
    expect(gaps).toEqual([{ fromMs: 100, toMs: 300 }])
  })

  it('clips to the domain and drops spans entirely outside it', () => {
    const domain: ScrubberRange = { fromMs: 200, toMs: 600 }
    const gaps = mergeGapRanges(
      [
        { fromMs: 100, toMs: 400 }, // clipped to 200
        { fromMs: 500, toMs: 900 }, // clipped to 600
        { fromMs: 1000, toMs: 1100 }, // outside → dropped
      ],
      domain,
    )
    expect(gaps).toEqual([
      { fromMs: 200, toMs: 400 },
      { fromMs: 500, toMs: 600 },
    ])
  })
})

describe('pickDisplayBucketSizeMs (adaptive strip bucket width)', () => {
  const H = HOUR

  it('keeps 1h bars at a 7-day domain (168 bars)', () => {
    expect(pickDisplayBucketSizeMs(7 * DAY)).toBe(H)
  })

  it('keeps 1h bars at the live ~7.66-day domain (no smearing across a gap)', () => {
    expect(pickDisplayBucketSizeMs(7.66 * DAY)).toBe(H)
  })

  it('steps to 3h bars around a 22-day domain', () => {
    expect(pickDisplayBucketSizeMs(22 * DAY)).toBe(3 * H)
  })

  it('steps to 12h bars around a 92-day domain', () => {
    expect(pickDisplayBucketSizeMs(92 * DAY)).toBe(12 * H)
  })

  it('never exceeds the coarsest rung (24h) for very deep retention', () => {
    expect(pickDisplayBucketSizeMs(3650 * DAY)).toBe(24 * H)
  })
})

describe('countEventsAfter (frozen "new events" pull)', () => {
  const bucket = (startMs: number, total: number): ScrubberBucket => ({
    startMs,
    endMs: startMs + HOUR,
    total,
    warnings: 0,
  })

  it('is zero for an empty bucket list', () => {
    expect(countEventsAfter([], NOW)).toBe(0)
  })

  it('sums totals of buckets starting at/after the toMs hour, floored to the hour', () => {
    // toMs mid-hour floors to its hour start, so the bucket AT that hour counts.
    const hourStart = Math.floor(NOW / HOUR) * HOUR
    const buckets = [
      bucket(hourStart - 2 * HOUR, 5), // before → excluded
      bucket(hourStart, 3), // the toMs hour → included
      bucket(hourStart + HOUR, 7), // after → included
    ]
    expect(countEventsAfter(buckets, hourStart + 30 * MIN)).toBe(10)
  })

  it('excludes buckets that start strictly before the toMs hour', () => {
    const hourStart = Math.floor(NOW / HOUR) * HOUR
    const buckets = [bucket(hourStart - HOUR, 9)]
    expect(countEventsAfter(buckets, hourStart)).toBe(0)
  })

  it('returns the raw sum (display cap is applied by the chip, not the helper)', () => {
    const hourStart = Math.floor(NOW / HOUR) * HOUR
    const buckets = [bucket(hourStart, 1500)]
    expect(countEventsAfter(buckets, hourStart)).toBe(1500)
  })
})

describe('TimelineScrubber live/paused chip (SSR)', () => {
  const SEL: ScrubberRange = { fromMs: NOW - HOUR, toMs: NOW }
  const base = {
    buckets: [] as ScrubberBucket[],
    domain: DOMAIN,
    selection: SEL,
    onSelectionChange: () => {},
  }

  it('omits both chips when no liveState is given', () => {
    const html = renderToString(<TimelineScrubber {...base} />)
    expect(html).not.toContain('timeline-live-chip')
    expect(html).not.toContain('timeline-paused-chip')
  })

  it('renders an inert Live chip when live + latched (no jump affordance)', () => {
    const html = renderToString(
      <TimelineScrubber {...base} liveState={{ kind: 'live', latched: true }} />,
    )
    expect(html).toContain('timeline-live-chip')
    expect(html).toContain('Live')
    expect(html).toContain('Following now')
    expect(html).not.toContain('jump to now')
  })

  it('renders a clickable Live chip with a jump affordance when live + unlatched', () => {
    const html = renderToString(
      <TimelineScrubber {...base} liveState={{ kind: 'live', latched: false }} />,
    )
    expect(html).toContain('timeline-live-chip')
    expect(html).toContain('jump to now')
  })

  it('renders the frozen slot: quiet "as of" caption + a "Go live" CTA, neutral when fresh', () => {
    const html = renderToString(
      <TimelineScrubber {...base} liveState={{ kind: 'frozen', asOfMs: Date.now() }} />,
    )
    expect(html).toContain('timeline-paused-chip')
    expect(html).toContain('as of')
    expect(html).toContain('Go live')
    expect(html).not.toContain('text-amber-600')
  })

  it('turns the "as of" caption amber once the freeze time is stale (>15m)', () => {
    const html = renderToString(
      <TimelineScrubber
        {...base}
        liveState={{ kind: 'frozen', asOfMs: Date.now() - 20 * 60_000 }}
      />,
    )
    expect(html).toContain('timeline-paused-chip')
    expect(html).toContain('text-amber-600')
  })

  it('appends the new-events count to the CTA, capped at 999+', () => {
    const some = renderToString(
      <TimelineScrubber {...base} liveState={{ kind: 'frozen', asOfMs: NOW, newEventCount: 214 }} />,
    )
    expect(some).toContain('214 new')

    const capped = renderToString(
      <TimelineScrubber {...base} liveState={{ kind: 'frozen', asOfMs: NOW, newEventCount: 4321 }} />,
    )
    expect(capped).toContain('999+ new')

    const none = renderToString(
      <TimelineScrubber {...base} liveState={{ kind: 'frozen', asOfMs: NOW, newEventCount: 0 }} />,
    )
    expect(none).not.toContain('new')
  })
})

describe('findBucketAt', () => {
  const buckets = [
    { startMs: 0, endMs: 100, total: 5, warnings: 1 },
    { startMs: 100, endMs: 200, total: 0, warnings: 0 },
  ]
  it('returns the bucket containing the position (start-inclusive, end-exclusive)', () => {
    expect(findBucketAt(0, buckets)?.total).toBe(5)
    expect(findBucketAt(99, buckets)?.total).toBe(5)
    expect(findBucketAt(100, buckets)?.total).toBe(0)
  })
  it('returns null outside all buckets', () => {
    expect(findBucketAt(200, buckets)).toBeNull()
    expect(findBucketAt(-1, buckets)).toBeNull()
  })
})
