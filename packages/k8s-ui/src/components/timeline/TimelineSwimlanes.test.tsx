import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import {
  TimelineSwimlanes,
  EventDetailPanel,
  chipKindLabel,
  clampWindowToBounds,
  zoomWindowWithinBounds,
  zoomWindowContinuous,
  wheelZoomFactor,
  clusterEventsByPosition,
  clusterBreakdown,
  clusterDrawerState,
  resolveEventCluster,
  restoreIsPending,
  eventSeverityRank,
  windowInsideGap,
  computeAutoExpandedLanes,
  reconcileAutoExpand,
  mergeLaneOrderById,
  collectLaneIdsDeep,
  AUTO_COLLAPSE_THRESHOLD,
  CLUSTER_MIN_GAP_PCT,
  CLUSTER_MAX_SPAN_FACTOR,
  type PositionedTimelineEvent,
  type TimeWindow,
} from './TimelineSwimlanes'
import type { EventType, TimelineEvent } from '../../types/core'
import type { ResourceLane } from '../../utils/resource-hierarchy'
import { buildAppMembershipIndex, type AppRow } from '../../utils/applications'

const mkEvent = (id: string, eventType: EventType = 'update'): TimelineEvent => ({
  id,
  timestamp: new Date(NOW).toISOString(),
  source: 'informer',
  kind: 'Pod',
  namespace: 'default',
  name: id,
  eventType,
})

const positioned = (...items: [TimelineEvent, number][]): PositionedTimelineEvent[] =>
  items.map(([event, x]) => ({ event, x }))

const HOUR = 60 * 60 * 1000
const DAY = 24 * HOUR
const NOW = 1_700_000_000_000
const BOUNDS: TimeWindow = { fromMs: NOW - 7 * DAY, toMs: NOW }

describe('clampWindowToBounds (view window ⊂ bounds)', () => {
  it('leaves a window already inside the bounds untouched', () => {
    const win: TimeWindow = { fromMs: NOW - 3 * HOUR, toMs: NOW - 2 * HOUR }
    expect(clampWindowToBounds(win, BOUNDS)).toEqual(win)
  })

  it('shifts a window past the future edge back inside, preserving width', () => {
    const win: TimeWindow = { fromMs: NOW - HOUR, toMs: NOW + HOUR }
    const width = win.toMs - win.fromMs
    const clamped = clampWindowToBounds(win, BOUNDS)
    expect(clamped.toMs).toBe(BOUNDS.toMs)
    expect(clamped.toMs - clamped.fromMs).toBe(width)
  })

  it('caps a window wider than the bounds to the full bounds (fully zoomed out)', () => {
    const win: TimeWindow = { fromMs: NOW - 30 * DAY, toMs: NOW }
    expect(clampWindowToBounds(win, BOUNDS)).toEqual({ fromMs: BOUNDS.fromMs, toMs: BOUNDS.toMs })
  })
})

describe('zoomWindowWithinBounds (preset stepping, end-anchored)', () => {
  it('zooms in to the next-smaller preset, keeping the END fixed (focus on recent)', () => {
    // 2h window → zoom in → 1h window ending at the same moment.
    const win: TimeWindow = { fromMs: NOW - 3 * HOUR, toMs: NOW - HOUR }
    const next = zoomWindowWithinBounds(win, 'in', BOUNDS)
    expect(next.toMs - next.fromMs).toBe(HOUR)
    expect(next.toMs).toBe(win.toMs)
  })

  it('zooms out to the next-larger preset, reaching farther back (END fixed)', () => {
    const win: TimeWindow = { fromMs: NOW - 90 * 60 * 1000, toMs: NOW - 30 * 60 * 1000 } // 1h wide
    const next = zoomWindowWithinBounds(win, 'out', BOUNDS)
    expect(next.toMs - next.fromMs).toBe(2 * HOUR)
    expect(next.toMs).toBe(win.toMs)
  })

  it('caps zoom-out at the bounds width', () => {
    const win: TimeWindow = { fromMs: NOW - 5 * DAY, toMs: NOW } // 5d, near the 7d bounds
    const next = zoomWindowWithinBounds(win, 'out', BOUNDS)
    // Next preset (7d) equals the bounds width → snaps to the full bounds.
    expect(next).toEqual({ fromMs: BOUNDS.fromMs, toMs: BOUNDS.toMs })
  })

  it('works without bounds (uncapped) for the general case', () => {
    const win: TimeWindow = { fromMs: NOW - 2 * HOUR, toMs: NOW }
    const next = zoomWindowWithinBounds(win, 'in')
    expect(next.toMs - next.fromMs).toBe(HOUR)
  })
})

describe('wheelZoomFactor (continuous wheel zoom)', () => {
  it('returns a factor > 1 scrolling down (zoom out) and < 1 up (zoom in)', () => {
    expect(wheelZoomFactor(100)).toBeGreaterThan(1)
    expect(wheelZoomFactor(-100)).toBeLessThan(1)
  })

  it('is symmetric: a down notch and an up notch cancel out', () => {
    expect(wheelZoomFactor(100) * wheelZoomFactor(-100)).toBeCloseTo(1, 10)
  })

  it('a zero delta is a no-op (factor 1)', () => {
    expect(wheelZoomFactor(0)).toBe(1)
  })

  it('clamps a jumpy delta so one event can never leap octaves', () => {
    // A 2000px delta is treated the same as the 100px clamp — a bounded step.
    expect(wheelZoomFactor(2000)).toBe(wheelZoomFactor(100))
  })

  it('normalizes line-mode deltas so wheel feel is device-independent', () => {
    // deltaMode 1 (lines): a small line delta scales up to the pixel equivalent.
    expect(wheelZoomFactor(6.25, 1)).toBeCloseTo(wheelZoomFactor(100, 0), 10)
  })

  it('accumulates smoothly for a trackpad pinch (tiny sub-notch deltas)', () => {
    // Ten small pinch events compound toward — but stay gentler than — one notch.
    const pinch = wheelZoomFactor(-4) ** 10
    expect(pinch).toBeLessThan(1)
    expect(pinch).toBeGreaterThan(wheelZoomFactor(-100))
  })
})

describe('zoomWindowContinuous (smooth window scaling, end-anchored)', () => {
  it('scales the window width by the factor, keeping the END fixed', () => {
    const win: TimeWindow = { fromMs: NOW - 2 * HOUR, toMs: NOW - HOUR } // 1h wide
    const next = zoomWindowContinuous(win, 1.5, BOUNDS)
    expect(next.toMs - next.fromMs).toBeCloseTo(1.5 * HOUR, 0)
    expect(next.toMs).toBe(win.toMs)
  })

  it('lands between preset rungs — the whole point (no ladder snap)', () => {
    const win: TimeWindow = { fromMs: NOW - HOUR, toMs: NOW } // 1h
    const next = zoomWindowContinuous(win, 1.3, BOUNDS)
    const widthHours = (next.toMs - next.fromMs) / HOUR
    expect(widthHours).toBeCloseTo(1.3, 5) // not 1 and not the next rung (2)
  })

  it('caps zoom-out at the bounds width', () => {
    const win: TimeWindow = { fromMs: NOW - 6 * DAY, toMs: NOW } // 6d, near 7d bounds
    const next = zoomWindowContinuous(win, 4, BOUNDS) // would be 24d
    expect(next).toEqual({ fromMs: BOUNDS.fromMs, toMs: BOUNDS.toMs })
  })

  it('floors zoom-in at the shared 15-minute window floor', () => {
    const win: TimeWindow = { fromMs: NOW - 30 * 60_000, toMs: NOW } // 30min
    const next = zoomWindowContinuous(win, 0.01, BOUNDS) // would be 18s
    expect(next.toMs - next.fromMs).toBe(15 * 60_000)
    expect(next.toMs).toBe(win.toMs)
  })
})

describe('clusterEventsByPosition (collapse near-overlapping markers)', () => {
  it('returns no clusters for an empty input', () => {
    expect(clusterEventsByPosition([], CLUSTER_MIN_GAP_PCT)).toEqual([])
  })

  it('passes non-overlapping events through as count-1 clusters (position preserved)', () => {
    const input = positioned(
      [mkEvent('a'), 10],
      [mkEvent('b'), 50],
      [mkEvent('c'), 90],
    )
    const out = clusterEventsByPosition(input, CLUSTER_MIN_GAP_PCT)
    expect(out).toHaveLength(3)
    expect(out.map((c) => c.count)).toEqual([1, 1, 1])
    expect(out.map((c) => c.x)).toEqual([10, 50, 90])
  })

  it('collapses events within min-gap into one cluster at the average position', () => {
    const out = clusterEventsByPosition(
      positioned([mkEvent('a'), 10], [mkEvent('b'), 10.5], [mkEvent('c'), 11]),
      CLUSTER_MIN_GAP_PCT,
    )
    expect(out).toHaveLength(1)
    expect(out[0].count).toBe(3)
    expect(out[0].events.map((e) => e.id)).toEqual(['a', 'b', 'c'])
    expect(out[0].x).toBeCloseTo(10.5)
  })

  it('picks the most-severe member as the cluster dominant', () => {
    // update (rank 0) + warning (rank 3) → warning dominates regardless of order.
    const out = clusterEventsByPosition(
      positioned([mkEvent('u', 'update'), 20], [mkEvent('w', 'Warning'), 20.3]),
      CLUSTER_MIN_GAP_PCT,
    )
    expect(out).toHaveLength(1)
    expect(out[0].count).toBe(2)
    expect(out[0].dominant.id).toBe('w')
  })

  it('ranks severity warning > delete > add > update', () => {
    expect(eventSeverityRank(mkEvent('x', 'Warning'))).toBeGreaterThan(eventSeverityRank(mkEvent('x', 'delete')))
    expect(eventSeverityRank(mkEvent('x', 'delete'))).toBeGreaterThan(eventSeverityRank(mkEvent('x', 'add')))
    expect(eventSeverityRank(mkEvent('x', 'add'))).toBeGreaterThan(eventSeverityRank(mkEvent('x', 'update')))
  })
})

describe('clusterBreakdown (pill hover breakdown)', () => {
  it('groups members into count×label lines, most-severe label first', () => {
    const { lines, more } = clusterBreakdown([
      mkEvent('u1', 'update'),
      mkEvent('u2', 'update'),
      mkEvent('a1', 'add'),
      { ...mkEvent('w1', 'Warning'), reason: 'BackOff' },
    ])
    expect(more).toBe(0)
    expect(lines).toEqual([
      { label: 'BackOff', count: 1 },
      { label: 'Created', count: 1 },
      { label: 'Modified', count: 2 },
    ])
  })

  it('merges same-reason warnings into one line with a count', () => {
    const { lines } = clusterBreakdown([
      { ...mkEvent('w1', 'Warning'), reason: 'BackOff' },
      { ...mkEvent('w2', 'Warning'), reason: 'BackOff' },
      { ...mkEvent('w3', 'Warning'), reason: 'Unhealthy' },
    ])
    expect(lines).toEqual([
      { label: 'BackOff', count: 2 },
      { label: 'Unhealthy', count: 1 },
    ])
  })

  it('caps at maxLines and reports the remaining event total as more', () => {
    const { lines, more } = clusterBreakdown(
      [
        { ...mkEvent('w1', 'Warning'), reason: 'BackOff' },
        { ...mkEvent('w2', 'Warning'), reason: 'Unhealthy' },
        { ...mkEvent('w3', 'Warning'), reason: 'Failed' },
        mkEvent('d1', 'delete'),
        mkEvent('a1', 'add'),
        mkEvent('u1', 'update'),
      ],
      4,
    )
    expect(lines).toHaveLength(4)
    expect(lines[3]).toEqual({ label: 'Deleted', count: 1 })
    expect(more).toBe(2)
  })

  it('labels a reason-less warning as Warning', () => {
    const { lines } = clusterBreakdown([mkEvent('w1', 'Warning')])
    expect(lines).toEqual([{ label: 'Warning', count: 1 }])
  })
})

describe('chipKindLabel — the real Kind, verbatim', () => {
  it('returns the full Kind unchanged — no abbreviation, ALL-CAPS, or truncation', () => {
    expect(chipKindLabel('Deployment')).toEqual({ label: 'Deployment', abbreviated: false })
    expect(chipKindLabel('PodDisruptionBudget')).toEqual({ label: 'PodDisruptionBudget', abbreviated: false })
    expect(chipKindLabel('HorizontalPodAutoscaler')).toEqual({ label: 'HorizontalPodAutoscaler', abbreviated: false })
    expect(chipKindLabel('VerticalPodAutoscalerCheckpoint')).toEqual({ label: 'VerticalPodAutoscalerCheckpoint', abbreviated: false })
    expect(chipKindLabel('ClusterSecretStore')).toEqual({ label: 'ClusterSecretStore', abbreviated: false })
  })
  it('labels kind-less events as Event', () => {
    expect(chipKindLabel('')).toEqual({ label: 'Event', abbreviated: false })
  })
})

describe('bulk expand/collapse toggle (single morphing control)', () => {
  it('renders ONE state-aware toggle in the resource header, tooltip naming action + shortcut', () => {
    const ev = (name: string): TimelineEvent => ({
      id: name, timestamp: new Date().toISOString(), source: 'informer',
      kind: 'Deployment', namespace: 'team-a', name, eventType: 'update',
    })
    // Flat lanes (no groups) → nothing expanded → the toggle offers Expand.
    const html = renderToString(<TimelineSwimlanes events={[ev('web')]} />)
    expect(html).toContain('aria-label="Expand all resources"')
    expect(html).not.toContain('aria-label="Collapse all resources"')
    // The "(E)" shortcut hint rides the custom Tooltip (not SSR'd) — the
    // aria-labels above are the state-aware affordance.
  })
})

describe('compact mode (embedded detail-view swimlane)', () => {
  const ev = (name: string): TimelineEvent => ({
    id: name, timestamp: new Date().toISOString(), source: 'informer',
    kind: 'Deployment', namespace: 'team-a', name, eventType: 'update',
  })

  it('hides the bulk expand/collapse toggle — flat lanes never nest', () => {
    const full = renderToString(<TimelineSwimlanes events={[ev('web')]} />)
    expect(full).toContain('aria-label="Expand all resources"')
    const compact = renderToString(<TimelineSwimlanes events={[ev('web')]} grouping="flat" compact />)
    expect(compact).not.toContain('aria-label="Expand all resources"')
    expect(compact).not.toContain('aria-label="Collapse all resources"')
  })

  it('widens the label column (no namespace subtitle or tree competing for it)', () => {
    const full = renderToString(<TimelineSwimlanes events={[ev('web')]} />)
    expect(full).toContain('w-[360px]')
    const compact = renderToString(<TimelineSwimlanes events={[ev('web')]} grouping="flat" compact />)
    expect(compact).toContain('w-[440px]')
    expect(compact).not.toContain('w-[360px]')
  })
})

describe('EventDetailPanel cluster mode (a ×N pill exposes every member)', () => {
  // A mixed cluster: one warning dominates the pill glyph; three benign updates
  // hide behind it. The founder's bug was that only one of these ever showed.
  const T0 = 1_700_000_000_000
  const mk = (id: string, eventType: EventType, atMs: number, opts: Partial<TimelineEvent> = {}): TimelineEvent => ({
    id, timestamp: new Date(atMs).toISOString(),
    source: eventType === 'Warning' ? 'k8s_event' : 'informer',
    kind: 'Pod', namespace: 'default', name: 'web', eventType, ...opts,
  })
  const clusterEvents: TimelineEvent[] = [
    mk('add-web', 'add', T0),
    mk('upd-web', 'update', T0 + 30_000),
    mk('warn-web', 'Warning', T0 + 60_000, { reason: 'BackOff', message: 'Back-off restarting failed container' }),
    mk('del-web', 'delete', T0 + 90_000),
  ]
  // Ordered most-severe-first, as the drawer opens it (Warning → delete → add → update).
  const ordered = [...clusterEvents].sort((a, b) => eventSeverityRank(b) - eventSeverityRank(a))

  it('renders a row for every clustered event plus the summary header', () => {
    const html = renderToString(
      <EventDetailPanel events={ordered} selectedId={ordered[0].id} onSelectId={() => {}} onClose={() => {}} />,
    )
    // Honest summary: the "N events" badge + a member list with all four rows.
    expect(html).toContain('events</span>')
    expect(html).toContain('aria-label="Drawer events"')
    expect((html.match(/<li>/g) ?? []).length).toBe(4)
    expect(html).toContain('BackOff')
    expect(html).toContain('>add<')
    expect(html).toContain('>update<')
    expect(html).toContain('>delete<')
  })

  it('preselects the most-severe member (the Warning the pill glyph promised)', () => {
    // ordered[0] is the Warning; its message renders in the detail pane by default.
    expect(ordered[0].id).toBe('warn-web')
    const html = renderToString(
      <EventDetailPanel events={ordered} selectedId={ordered[0].id} onSelectId={() => {}} onClose={() => {}} />,
    )
    expect(html).toContain('Back-off restarting failed container')
  })

  it('shows the detail of whichever member is selected (list click swaps it)', () => {
    // Selecting a benign member swaps the detail pane away from the warning.
    const html = renderToString(
      <EventDetailPanel events={ordered} selectedId="upd-web" onSelectId={() => {}} onClose={() => {}} />,
    )
    // With a benign member selected, its detail shows instead of the warning body.
    expect(html).not.toContain('Back-off restarting failed container')
    // Still lists all four members.
    expect((html.match(/<li>/g) ?? []).length).toBe(4)
  })

  it('N=1 renders the SAME rail+detail anatomy — a rail of one, selected', () => {
    const single = mk('warn-web', 'Warning', T0, { reason: 'BackOff', message: 'Back-off restarting failed container' })
    const html = renderToString(
      <EventDetailPanel events={[single]} selectedId={single.id} onSelectId={() => {}} onClose={() => {}} />,
    )
    // One anatomy: the rail always renders — a single event is a rail of one.
    expect(html).toContain('Back-off restarting failed container')
    expect(html).toContain('aria-label="Drawer events"')
    expect((html.match(/<li/g) ?? []).length).toBe(1)
    expect(html).toContain('1 of 1') // header stepper reflects the rail of one
    expect(html).not.toContain(' events</span>') // no misleading "1 events" badge
  })

  it('N=1 with allEvents grows the rail with ±15-min correlated neighbors', () => {
    const single = mk('warn-web', 'Warning', T0, { reason: 'BackOff', message: 'Back-off restarting failed container' })
    const neighbor = mk('upd-api', 'update', T0 + 2 * 60_000, { name: 'api' })
    const far = mk('upd-db', 'update', T0 + 60 * 60_000, { name: 'db' }) // outside ±15m
    const html = renderToString(
      <EventDetailPanel events={[single]} selectedId={single.id} onSelectId={() => {}} onClose={() => {}} allEvents={[single, neighbor, far]} />,
    )
    expect(html).toContain('Within ±15 min')
    expect(html).toContain('>api<') // the neighbor joined the rail
    expect(html).not.toContain('>db<') // out-of-window event did not
    expect(html).toContain('1 of 2')
  })
})

describe('toolbar lens controls (controlled vs uncontrolled)', () => {
  it('shows zoom buttons and the window label in uncontrolled mode', () => {
    const html = renderToString(<TimelineSwimlanes events={[]} />)
    expect(html).toContain('aria-label="Zoom in"')
    expect(html).toContain('aria-label="Zoom out"')
    expect(html).toContain(' window')
  })

  it('hides zoom buttons and the window label when controlled (viewWindow set)', () => {
    const html = renderToString(
      <TimelineSwimlanes
        events={[]}
        viewWindow={{ fromMs: NOW - HOUR, toMs: NOW }}
        bounds={{ fromMs: NOW - 24 * HOUR, toMs: NOW }}
        onViewWindowChange={() => {}}
      />,
    )
    expect(html).not.toContain('aria-label="Zoom in"')
    expect(html).not.toContain('aria-label="Zoom out"')
    expect(html).not.toContain(' window')
  })

  it('hides the strip "→ Now" button when controlled — the scrubber chip owns the path back to now', () => {
    // Even with the window a full hour behind the latest edge, controlled mode no
    // longer renders its own jump button; the scrubber's live/paused chip covers it.
    const html = renderToString(
      <TimelineSwimlanes
        events={[]}
        viewWindow={{ fromMs: NOW - 2 * HOUR, toMs: NOW - HOUR }}
        bounds={{ fromMs: NOW - 24 * HOUR, toMs: NOW }}
        onViewWindowChange={() => {}}
      />,
    )
    expect(html).not.toContain('Jump to current time')
    expect(html).not.toContain('→ Now')
  })
})

describe('windowInsideGap (view fully inside a recording gap)', () => {
  const GAPS: TimeWindow[] = [{ fromMs: NOW - 12 * HOUR, toMs: NOW - 6 * HOUR }]

  it('is true when the window sits entirely inside a gap', () => {
    const win: TimeWindow = { fromMs: NOW - 10 * HOUR, toMs: NOW - 8 * HOUR }
    expect(windowInsideGap(win, GAPS)).toBe(true)
  })

  it('is true at the exact gap edges (inclusive)', () => {
    const win: TimeWindow = { fromMs: NOW - 12 * HOUR, toMs: NOW - 6 * HOUR }
    expect(windowInsideGap(win, GAPS)).toBe(true)
  })

  it('is false when the window only partially overlaps a gap', () => {
    const win: TimeWindow = { fromMs: NOW - 8 * HOUR, toMs: NOW - 4 * HOUR }
    expect(windowInsideGap(win, GAPS)).toBe(false)
  })

  it('is false when the window is fully outside every gap', () => {
    const win: TimeWindow = { fromMs: NOW - 3 * HOUR, toMs: NOW - HOUR }
    expect(windowInsideGap(win, GAPS)).toBe(false)
  })

  it('is false with no gaps or no window', () => {
    expect(windowInsideGap({ fromMs: NOW - HOUR, toMs: NOW }, [])).toBe(false)
    expect(windowInsideGap(undefined, GAPS)).toBe(false)
  })
})

describe('app-group header lane (grouping=app + appIndex)', () => {
  it('renders an app header with the app name and member count', () => {
    const now = Date.now()
    const ev = (name: string): TimelineEvent => ({
      id: name, timestamp: new Date(now).toISOString(), source: 'informer',
      kind: 'Deployment', namespace: 'team-a', name, eventType: 'update',
    })
    const rows: AppRow[] = [{
      key: 'team-a/app/billing', name: 'billing', health: 'healthy',
      identity: { key: 'billing', env: 'prod', confidence: 'high', evidence: 'app.kubernetes.io/instance' },
      workloads: [
        { kind: 'Deployment', namespace: 'team-a', name: 'billing-api', health: 'healthy', ready: 1, desired: 1, restarts: 0 },
        { kind: 'Deployment', namespace: 'team-a', name: 'billing-worker', health: 'healthy', ready: 1, desired: 1, restarts: 0 },
      ],
    }]
    const html = renderToString(
      <TimelineSwimlanes events={[ev('billing-api'), ev('billing-worker')]} grouping="app" appIndex={buildAppMembershipIndex(rows)} />,
    )
    expect(html).toContain('billing')
    expect(html).toContain('+2')
  })

  it('keeps a server-declared member visible even when its events are outside the window', () => {
    const now = 1_700_000_000_000
    const ev = (name: string, iso: string): TimelineEvent => ({
      id: name, timestamp: iso, source: 'informer',
      kind: 'Deployment', namespace: 'team-a', name, eventType: 'update',
    })
    const rows: AppRow[] = [{
      key: 'team-a/app/billing', name: 'billing', health: 'healthy',
      identity: { key: 'billing', env: 'prod', confidence: 'high', evidence: 'app.kubernetes.io/instance' },
      workloads: [
        { kind: 'Deployment', namespace: 'team-a', name: 'billing-api', health: 'healthy', ready: 1, desired: 1, restarts: 0 },
        { kind: 'Deployment', namespace: 'team-a', name: 'billing-worker', health: 'healthy', ready: 1, desired: 1, restarts: 0 },
      ],
    }]
    const html = renderToString(
      <TimelineSwimlanes
        events={[ev('billing-api', new Date(now).toISOString()), ev('billing-worker', new Date(now - 10 * 24 * 3600_000).toISOString())]}
        grouping="app"
        appIndex={buildAppMembershipIndex(rows)}
        viewWindow={{ fromMs: now - 3600_000, toMs: now + 3600_000 }}
      />,
    )
    // Both members are STRUCTURAL (the server's app identity declares them), so
    // the out-of-window one stays — hiding the app's own workloads/Service made
    // a matched app read as incomplete. The window noise filter still applies
    // to evidence/name-matched lanes (covered in resource-hierarchy tests).
    expect(html).toContain('billing-api')
    expect(html).toContain('billing-worker')
    expect(html).toContain('+2')
  })
})

describe('pinned resource lanes', () => {
  it('renders the PINNED caption and a pinned row even with zero events in the selection', () => {
    // No events at all for the pinned resource — the row is synthesized from the
    // pin record so the eye keeps its target (empty track = "nothing happened").
    const html = renderToString(
      <TimelineSwimlanes
        events={[]}
        pinnedLanes={[{ id: 'Deployment/team-a/web', kind: 'Deployment', namespace: 'team-a', name: 'web' }]}
        onTogglePin={() => {}}
      />,
    )
    expect(html).toContain('Pinned')
    expect(html).toContain('web')
    expect(html).toContain('team-a')
    // Pinned row's toggle is the filled Unpin affordance.
    expect(html).toContain('aria-label="Unpin"')
  })

  it('renders a pinned row above the sorted lanes when the resource has events', () => {
    const now = Date.now()
    const ev = (name: string): TimelineEvent => ({
      id: name, timestamp: new Date(now).toISOString(), source: 'informer',
      kind: 'Deployment', namespace: 'team-a', name, eventType: 'update',
    })
    const html = renderToString(
      <TimelineSwimlanes
        events={[ev('web'), ev('api')]}
        pinnedLanes={[{ id: 'Deployment/team-a/web', kind: 'Deployment', namespace: 'team-a', name: 'web' }]}
        onTogglePin={() => {}}
      />,
    )
    const pinnedIdx = html.indexOf('Pinned')
    const webIdx = html.indexOf('web')
    expect(pinnedIdx).toBeGreaterThanOrEqual(0)
    // The pinned caption precedes the (duplicated) pinned row.
    expect(webIdx).toBeGreaterThan(pinnedIdx)
  })

  it('renders a pinned APP-GROUP header (live members) with the filled Unpin affordance', () => {
    const now = Date.now()
    const ev = (name: string): TimelineEvent => ({
      id: name, timestamp: new Date(now).toISOString(), source: 'informer',
      kind: 'Deployment', namespace: 'team-a', name, eventType: 'update',
    })
    const rows: AppRow[] = [{
      key: 'team-a/app/billing', name: 'billing', health: 'healthy',
      identity: { key: 'billing', env: 'prod', confidence: 'high', evidence: 'app.kubernetes.io/instance' },
      workloads: [
        { kind: 'Deployment', namespace: 'team-a', name: 'billing-api', health: 'healthy', ready: 1, desired: 1, restarts: 0 },
        { kind: 'Deployment', namespace: 'team-a', name: 'billing-worker', health: 'healthy', ready: 1, desired: 1, restarts: 0 },
      ],
    }]
    const html = renderToString(
      <TimelineSwimlanes
        events={[ev('billing-api'), ev('billing-worker')]}
        grouping="app"
        appIndex={buildAppMembershipIndex(rows)}
        pinnedLanes={[{ type: 'appGroup', id: 'app:team-a/app/billing', appKey: 'team-a/app/billing', appName: 'billing' }]}
        onTogglePin={() => {}}
      />,
    )
    expect(html).toContain('Pinned')
    expect(html).toContain('billing')
    // The pinned group's toggle shows the app-specific filled Unpin affordance.
    expect(html).toContain('aria-label="Unpin app"')
    expect(html).toContain('fill-current')
  })

  it('synthesizes a quiet pinned header when the pinned app is absent (owner grouping)', () => {
    const now = Date.now()
    const ev = (name: string): TimelineEvent => ({
      id: name, timestamp: new Date(now).toISOString(), source: 'informer',
      kind: 'Deployment', namespace: 'team-a', name, eventType: 'update',
    })
    const html = renderToString(
      <TimelineSwimlanes
        events={[ev('unrelated')]}
        grouping="owner"
        pinnedLanes={[{ type: 'appGroup', id: 'app:team-a/app/gone', appKey: 'team-a/app/gone', appName: 'gone' }]}
        onTogglePin={() => {}}
      />,
    )
    expect(html).toContain('gone')
    expect(html).toContain('app not present in the current view/grouping')
  })
})

describe('app-group name → Applications link', () => {
  const now = Date.now()
  const ev = (kind: string, name: string): TimelineEvent => ({
    id: `${kind}/${name}`, timestamp: new Date(now).toISOString(), source: 'informer',
    kind, namespace: 'team-a', name, eventType: 'update',
  })
  const index = {
    byResource: new Map([
      ['Deployment/team-a/billing-api', { appKey: 'billing', appName: 'billing' }],
      ['Service/team-a/billing-api', { appKey: 'billing', appName: 'billing' }],
    ]),
    byEvidence: new Map(),
  } as any

  it('renders the Open-in-Applications affordance for a real app group', () => {
    const html = renderToString(
      <TimelineSwimlanes
        events={[ev('Deployment', 'billing-api'), ev('Service', 'billing-api')]}
        grouping="app" appIndex={index} onAppClick={() => {}}
      />,
    )
    expect(html).toContain('Open in Applications')
  })

  it('renders no link without an onAppClick handler', () => {
    const html = renderToString(
      <TimelineSwimlanes
        events={[ev('Deployment', 'billing-api'), ev('Service', 'billing-api')]}
        grouping="app" appIndex={index}
      />,
    )
    expect(html).not.toContain('Open in Applications')
  })
})

describe('resolveEventCluster (restore a persisted event id to its drawer state)', () => {
  // A restored id opens the drawer in whatever mode the CURRENT render gives it:
  // inside a ×N pill → cluster mode with that pill's members; standalone → single.
  // Same window/filters → same clustering, so resolution is deterministic.
  const WIN_START = NOW - HOUR
  // Linear map of the 1h window onto 0-100%: start→0, end→100.
  const timeToX = (t: number): number => ((t - WIN_START) / HOUR) * 100
  const at = (id: string, tMs: number, eventType: EventType = 'update'): TimelineEvent => ({
    id, timestamp: new Date(tMs).toISOString(), source: 'informer', kind: 'Pod', namespace: 'default', name: id, eventType,
  })
  const lane = (over: Partial<ResourceLane> & Pick<ResourceLane, 'id' | 'name' | 'events'>): ResourceLane => ({
    kind: 'Pod', namespace: 'default', isWorkload: false, ...over,
  })

  // Two events ~10s apart at the window midpoint cluster together (< 1.4% gap);
  // a third 20min later stands alone.
  const near1 = at('near1', NOW - 30 * 60 * 1000)
  const near2 = at('near2', NOW - 30 * 60 * 1000 + 10_000)
  const solo = at('solo', NOW - 10 * 60 * 1000)
  const flat = lane({ id: 'Pod/default/flat', name: 'flat', events: [near1, near2, solo] })

  it('resolves an id inside a ×N pill to cluster mode carrying every member, that id selected', () => {
    const out = resolveEventCluster([flat], new Set(), 'near2', timeToX)
    expect(out).not.toBeNull()
    expect(out!.events.map((e) => e.id).sort()).toEqual(['near1', 'near2'])
    expect(out!.selectedId).toBe('near2')
  })

  it('resolves a standalone id to a single-event drawer', () => {
    const out = resolveEventCluster([flat], new Set(), 'solo', timeToX)
    expect(out).toEqual({ events: [solo], selectedId: 'solo' })
  })

  it('returns null for an id on no rendered row (pruned / filtered out)', () => {
    expect(resolveEventCluster([flat], new Set(), 'ghost', timeToX)).toBeNull()
  })

  it('returns null for an id whose marker sits outside the view window', () => {
    // Positioned before the window start → x < 0 → dropped like the markers do.
    const past = lane({ id: 'Pod/default/past', name: 'past', events: [at('past', WIN_START - 5 * 60 * 1000)] })
    expect(resolveEventCluster([past], new Set(), 'past', timeToX)).toBeNull()
  })

  it('respects expansion state: a collapsed parent rolls its child event into its own pill', () => {
    const own = at('own', NOW - 30 * 60 * 1000)
    const childEv = at('childEv', NOW - 30 * 60 * 1000 + 10_000)
    const child = lane({ id: 'Pod/default/child', name: 'child', events: [childEv] })
    const parent = lane({
      id: 'Deployment/default/parent', kind: 'Deployment', name: 'parent',
      events: [own], children: [child], allEventsSorted: [own, childEv],
    })
    // Collapsed: the child event paints on the parent's roll-up, clustered with
    // the parent's own event → cluster mode.
    const collapsed = resolveEventCluster([parent], new Set(), 'childEv', timeToX)
    expect(collapsed!.events.map((e) => e.id).sort()).toEqual(['childEv', 'own'])
    expect(collapsed!.selectedId).toBe('childEv')
    // Expanded: the parent row paints only its own event; the child event resolves
    // on the child's own (uncluttered) row → single-event drawer.
    const expanded = resolveEventCluster([parent], new Set(['Deployment/default/parent']), 'childEv', timeToX)
    expect(expanded).toEqual({ events: [childEv], selectedId: 'childEv' })
  })

  it('searches pinned rows before visible lanes, in render order', () => {
    const pinned = lane({ id: 'Pod/default/pinned', name: 'pinned', events: [at('pinnedEv', NOW - 20 * 60 * 1000)] })
    const out = resolveEventCluster([pinned, flat], new Set(), 'pinnedEv', timeToX)
    expect(out).toEqual({ events: [at('pinnedEv', NOW - 20 * 60 * 1000)], selectedId: 'pinnedEv' })
  })
})

describe('restoreIsPending (URL ↔ drawer reconciliation guard)', () => {
  // [selectedEventId, drawerSelectedId, settledId, expectedPending, why]
  // dismissedId defaults to null (3-arg call) — the pre-dismissal behavior.
  const cases: [string | null, string | null, string | null, boolean, string][] = [
    [null, null, null, false, 'no id to restore'],
    ['X', null, null, true, 'fresh id, drawer closed, never settled — restore must win over strip'],
    ['X', null, 'X', false, 'already resolved/ruled-out — report is free to sync the URL'],
    ['X', 'X', 'X', false, 'drawer already shows it'],
    ['Y', 'X', null, false, 'open drawer (showing X) is authoritative even for a different id'],
    ['Y', null, 'X', true, 'a NEW fresh id (Y) is pending even though a prior id (X) settled'],
  ]
  it.each(cases)('selected=%s drawer=%s settled=%s → pending=%s (%s)', (sel, drawer, settled, want) => {
    expect(restoreIsPending(sel, drawer, settled)).toBe(want)
  })

  // [selectedEventId, drawerSelectedId, settledId, dismissedId, expectedPending, why]
  const dismissedCases: [string | null, string | null, string | null, string | null, boolean, string][] = [
    ['X', null, null, 'X', false, 'user just dismissed X — the not-yet-stripped ?event=X must NOT read as a pending restore (no reopen; report free to strip)'],
    ['X', null, null, 'Y', true, 'a different id (Y) was dismissed — X is still a genuine pending restore'],
    ['X', 'X', null, 'X', false, 'open drawer already shows X (dismissed is moot while open)'],
    ['X', null, 'X', 'X', false, 'settled AND dismissed — still not pending'],
  ]
  it.each(dismissedCases)(
    'selected=%s drawer=%s settled=%s dismissed=%s → pending=%s (%s)',
    (sel, drawer, settled, dismissed, want) => {
      expect(restoreIsPending(sel, drawer, settled, dismissed)).toBe(want)
    },
  )
})

// The restore effect and the report effect share ONE decision: attempt a restore
// (open) exactly when restoreIsPending is true; report/strip the URL exactly when
// it's false. The dismissed-id ref set on a user close (and cleared when the
// selection settles to null) is what lets a user close win over the ?event= echo
// without losing deep-link / history-nav restore. This models the two ref rules
// the component applies (set-on-close, clear-on-null) around that shared predicate.
describe('drawer close vs echo state machine (dismissed-id)', () => {
  // Faithful mirror of the component's two dismissed-id ref rules:
  //   - user close records the open id
  //   - selection settling to null clears it
  const onClose = (openId: string): string => openId
  const onSelectionSettledNull = (): string | null => null

  it('open → close → echo(same id) stays closed across the not-yet-stripped ?event=', () => {
    // Drawer open on D, URL ?event=D. User closes: dismissed := D, drawer closes,
    // but selectedEventId is still D (the strip hasn't landed).
    let dismissed: string | null = onClose('D')
    // Restore effect must NOT reopen; report effect IS free to strip → both driven
    // by restoreIsPending === false.
    expect(restoreIsPending('D', null, null, dismissed)).toBe(false)
    // A live poll re-renders mid-strip with selectedEventId still echoing D — still
    // suppressed (the echo can't ride the poll into a reopen).
    expect(restoreIsPending('D', null, null, dismissed)).toBe(false)
    // Strip lands: selection settles to null → dismissed clears.
    dismissed = onSelectionSettledNull()
    expect(dismissed).toBeNull()
  })

  it('close then genuinely re-select the SAME id → restore attempts (opens)', () => {
    let dismissed: string | null = onClose('D')
    expect(restoreIsPending('D', null, null, dismissed)).toBe(false) // close wins
    dismissed = onSelectionSettledNull() // strip settled → cleared
    // User selects D again: fresh id, drawer closed, nothing dismissed → restore.
    expect(restoreIsPending('D', null, null, dismissed)).toBe(true)
  })

  it('back/forward to the SAME id after close → restore attempts (history is not an echo)', () => {
    let dismissed: string | null = onClose('D')
    expect(restoreIsPending('D', null, null, dismissed)).toBe(false)
    dismissed = onSelectionSettledNull() // the URL passed through clean, clearing dismissed
    // History nav re-drives ?event=D → the cleared dismissal lets it restore.
    expect(restoreIsPending('D', null, null, dismissed)).toBe(true)
  })

  it('ruled-out-id retry (A2) is unaffected: a settled miss stays non-pending regardless of dismissal', () => {
    // settledId === want (ruled out) keeps it non-pending; dismissal is orthogonal.
    expect(restoreIsPending('D', null, 'D', null)).toBe(false)
    expect(restoreIsPending('D', null, 'D', 'D')).toBe(false)
  })
})

describe('clusterDrawerState (drawer payload a cluster opens into)', () => {
  const mk = (id: string, type: EventType = 'update'): TimelineEvent => mkEvent(id, type)

  it('opens a count-1 cluster as a single-event drawer', () => {
    const only = mk('only')
    expect(clusterDrawerState({ x: 5, events: [only], dominant: only, count: 1 }))
      .toEqual({ events: [only], selectedId: 'only' })
  })

  it('orders a multi-event cluster most-severe-first and defaults to the dominant', () => {
    const upd = mk('upd', 'update')
    const warn = mk('warn', 'Warning')
    const out = clusterDrawerState({ x: 5, events: [upd, warn], dominant: warn, count: 2 })
    expect(out.events.map((e) => e.id)).toEqual(['warn', 'upd'])
    expect(out.selectedId).toBe('warn')
  })

  it('prefers a valid preferId over the dominant', () => {
    const upd = mk('upd', 'update')
    const warn = mk('warn', 'Warning')
    expect(clusterDrawerState({ x: 5, events: [upd, warn], dominant: warn, count: 2 }, 'upd').selectedId).toBe('upd')
  })

  it('ignores a preferId absent from the cluster, falling back to the dominant', () => {
    const upd = mk('upd', 'update')
    const warn = mk('warn', 'Warning')
    expect(clusterDrawerState({ x: 5, events: [upd, warn], dominant: warn, count: 2 }, 'ghost').selectedId).toBe('warn')
  })
})

describe('empty state is pin-aware', () => {
  const now = Date.now()
  const ev = (name: string): TimelineEvent => ({
    id: name, timestamp: new Date(now).toISOString(), source: 'informer',
    kind: 'Deployment', namespace: 'team-a', name, eventType: 'update',
  })

  it('renders NO empty state when pinned rows carry the window content (list just ends)', () => {
    const html = renderToString(
      <TimelineSwimlanes
        events={[ev('web')]}
        pinnedLanes={[{ id: 'Deployment/team-a/web', kind: 'Deployment', namespace: 'team-a', name: 'web' }]}
        onTogglePin={() => {}}
      />,
    )
    expect(html).not.toContain('No events in this window')
    expect(html).not.toContain('No matching events')
  })

  it('keeps the full empty state when pinned rows are empty in-window too (whole canvas blank)', () => {
    const html = renderToString(
      <TimelineSwimlanes
        events={[]}
        pinnedLanes={[{ id: 'Deployment/team-a/web', kind: 'Deployment', namespace: 'team-a', name: 'web' }]}
        onTogglePin={() => {}}
      />,
    )
    expect(html).toContain('aria-label="Unpin"')
  })
})

// --- auto-collapse by child count (item 2) ----------------------------------
describe('computeAutoExpandedLanes (count-based default expansion)', () => {
  const mkLane = (id: string, children: ResourceLane[] = []): ResourceLane => ({
    id, kind: 'Deployment', namespace: 'default', name: id, isWorkload: true, events: [], children,
  })
  const kids = (parentId: string, n: number): ResourceLane[] =>
    Array.from({ length: n }, (_, i) => mkLane(`${parentId}-c${i}`))

  it('auto-expands a small family (<= threshold children)', () => {
    const lane = mkLane('web', kids('web', AUTO_COLLAPSE_THRESHOLD))
    expect(computeAutoExpandedLanes([lane]).has('web')).toBe(true)
  })

  it('keeps a large family collapsed (> threshold children)', () => {
    const lane = mkLane('api', kids('api', AUTO_COLLAPSE_THRESHOLD + 1))
    expect(computeAutoExpandedLanes([lane]).has('api')).toBe(false)
  })

  it('never expands a childless leaf', () => {
    expect(computeAutoExpandedLanes([mkLane('leaf')]).has('leaf')).toBe(false)
  })

  it('applies the rule at every depth (small Job under a large CronJob)', () => {
    const job = mkLane('cron-job1', kids('cron-job1', 2)) // small → expand
    const bigCron = mkLane('cron', [job, ...kids('cron', AUTO_COLLAPSE_THRESHOLD)]) // >threshold → collapse
    const set = computeAutoExpandedLanes([bigCron])
    expect(set.has('cron')).toBe(false)
    expect(set.has('cron-job1')).toBe(true)
  })

  it('is deterministic and pure (same input → same set, no throw on empty)', () => {
    expect(computeAutoExpandedLanes([])).toEqual(new Set())
    const lane = mkLane('web', kids('web', 2))
    expect(computeAutoExpandedLanes([lane])).toEqual(computeAutoExpandedLanes([lane]))
  })
})

describe('mergeLaneOrderById (live-mode order hysteresis)', () => {
  it('keeps existing lanes in their PRIOR order when the fresh rank reshuffles them', () => {
    // prior order a,b,c ; fresh rank flips to c,b,a — existing lanes must not move.
    expect(mergeLaneOrderById(['a', 'b', 'c'], ['c', 'b', 'a'])).toEqual(['a', 'b', 'c'])
  })

  it('is identity when nothing changed', () => {
    expect(mergeLaneOrderById(['a', 'b', 'c'], ['a', 'b', 'c'])).toEqual(['a', 'b', 'c'])
  })

  it('drops lanes absent from the fresh set (a resource left the window)', () => {
    expect(mergeLaneOrderById(['a', 'b', 'c'], ['a', 'c'])).toEqual(['a', 'c'])
  })

  it('splices a NEW lane in right after the surviving lane that precedes it by rank', () => {
    // fresh puts new "x" between a and b → it lands after a.
    expect(mergeLaneOrderById(['a', 'b'], ['a', 'x', 'b'])).toEqual(['a', 'x', 'b'])
  })

  it('places a new top-ranked lane at the very front (no preceding survivor)', () => {
    expect(mergeLaneOrderById(['a', 'b'], ['x', 'a', 'b'])).toEqual(['x', 'a', 'b'])
  })

  it('keeps multiple new lanes in fresh relative order after their shared anchor', () => {
    expect(mergeLaneOrderById(['a', 'b'], ['a', 'x', 'y', 'b'])).toEqual(['a', 'x', 'y', 'b'])
  })

  it('anchors a new lane by its fresh predecessor even when survivors are reshuffled', () => {
    // survivors keep prior order [a,b,c]; fresh ranks them c,a,new,b — "new" follows a.
    expect(mergeLaneOrderById(['a', 'b', 'c'], ['c', 'a', 'new', 'b'])).toEqual(['a', 'new', 'b', 'c'])
  })

  it('adopts the fresh order wholesale from an empty prior (first live render)', () => {
    expect(mergeLaneOrderById([], ['a', 'b', 'c'])).toEqual(['a', 'b', 'c'])
  })

  it('is pure — does not mutate its inputs', () => {
    const prev = ['a', 'b']
    const fresh = ['a', 'x', 'b']
    mergeLaneOrderById(prev, fresh)
    expect(prev).toEqual(['a', 'b'])
    expect(fresh).toEqual(['a', 'x', 'b'])
  })
})

describe('reconcileAutoExpand (auto-expand default frozen once per lane id)', () => {
  const mkLane = (id: string, children: ResourceLane[] = []): ResourceLane => ({
    id, kind: 'Deployment', namespace: 'default', name: id, isWorkload: true, events: [], children,
  })
  const kids = (parentId: string, n: number): ResourceLane[] =>
    Array.from({ length: n }, (_, i) => mkLane(`${parentId}-c${i}`))

  it('freezes a small family OPEN even after it grows past the threshold on a later tick', () => {
    // First sighting: 2 children (<= threshold) → default open.
    const first = reconcileAutoExpand(new Map(), [mkLane('web', kids('web', 2))])
    expect(first.get('web')).toBe(true)
    // Later tick: same lane now has THRESHOLD+3 children (would collapse fresh) —
    // but the frozen default must not flip, so arriving pods don't close the row.
    const grown = reconcileAutoExpand(first, [mkLane('web', kids('web', AUTO_COLLAPSE_THRESHOLD + 3))])
    expect(grown.get('web')).toBe(true)
  })

  it('freezes a large family COLLAPSED even after it shrinks below the threshold', () => {
    const first = reconcileAutoExpand(new Map(), [mkLane('api', kids('api', AUTO_COLLAPSE_THRESHOLD + 2))])
    expect(first.get('api')).toBe(false)
    const shrunk = reconcileAutoExpand(first, [mkLane('api', kids('api', 2))])
    expect(shrunk.get('api')).toBe(false)
  })

  it('gives a NEWLY-arrived lane its current structural default without touching existing lanes', () => {
    const prev = reconcileAutoExpand(new Map(), [mkLane('web', kids('web', AUTO_COLLAPSE_THRESHOLD + 5))])
    expect(prev.get('web')).toBe(false)
    const withNew = reconcileAutoExpand(prev, [
      mkLane('web', kids('web', AUTO_COLLAPSE_THRESHOLD + 5)),
      mkLane('cache', kids('cache', 2)), // new small family → opens
    ])
    expect(withNew.get('web')).toBe(false) // untouched
    expect(withNew.get('cache')).toBe(true) // new lane gets its default
  })

  it('does not mutate the previous map (pure accumulation into a fresh map)', () => {
    const prev = reconcileAutoExpand(new Map(), [mkLane('web', kids('web', 2))])
    const before = new Map(prev)
    reconcileAutoExpand(prev, [mkLane('web', kids('web', 2)), mkLane('new', kids('new', 1))])
    expect(prev).toEqual(before)
  })

  it('collectLaneIdsDeep enumerates parents and all descendants', () => {
    const tree = [mkLane('p', [mkLane('c1', [mkLane('g1')]), mkLane('c2')])]
    expect(collectLaneIdsDeep(tree)).toEqual(['p', 'c1', 'g1', 'c2'])
  })
})

describe('group collision chip (same kind+ns+name, different API group)', () => {
  const clusterEv = (id: string, apiVersion: string): TimelineEvent => ({
    id, timestamp: new Date(NOW).toISOString(), source: 'informer',
    kind: 'Cluster', namespace: 'prod', name: 'main', eventType: 'update', apiVersion,
  })

  it('renders a group chip on BOTH colliding lanes (SSR)', () => {
    const html = renderToString(
      <TimelineSwimlanes
        events={[clusterEv('capi', 'cluster.x-k8s.io/v1beta1'), clusterEv('cnpg', 'postgresql.cnpg.io/v1')]}
        grouping="flat"
        viewWindow={{ fromMs: NOW - HOUR, toMs: NOW + HOUR }}
      />,
    )
    // The chip is the "API group <g>" title; both groups get one.
    expect(html).toContain('API group cluster.x-k8s.io')
    expect(html).toContain('API group postgresql.cnpg.io')
  })

  it('renders NO group chip when the CRD lane is unique', () => {
    const html = renderToString(
      <TimelineSwimlanes
        events={[clusterEv('cnpg', 'postgresql.cnpg.io/v1')]}
        grouping="flat"
        viewWindow={{ fromMs: NOW - HOUR, toMs: NOW + HOUR }}
      />,
    )
    expect(html).not.toContain('API group')
    // The group still rides the kind badge's title tooltip (non-intrusive).
    expect(html).toContain('postgresql.cnpg.io')
  })
})

describe('cluster span cap (a pill never spans far-apart events)', () => {
  it('splits a dense chain that exceeds the max span into separate pills', () => {
    // Each neighbor is within min-gap, but the chain crawls across 5× the gap.
    // Unbounded chaining would collapse an hour of distinct positions into one
    // pill; the cap breaks it at CLUSTER_MAX_SPAN_FACTOR × gap.
    const step = CLUSTER_MIN_GAP_PCT * 0.9
    const input = positioned(...Array.from({ length: 7 }, (_, i) => [mkEvent(`e${i}`), 10 + i * step] as [TimelineEvent, number]))
    const out = clusterEventsByPosition(input, CLUSTER_MIN_GAP_PCT)
    expect(out.length).toBeGreaterThan(1)
    for (const c of out) {
      const xs = c.events.map((e) => input.find((p) => p.event.id === e.id)!.x)
      expect(Math.max(...xs) - Math.min(...xs)).toBeLessThanOrEqual(CLUSTER_MIN_GAP_PCT * CLUSTER_MAX_SPAN_FACTOR)
    }
  })

  it('keeps a tight burst as one pill', () => {
    const out = clusterEventsByPosition(
      positioned([mkEvent('a'), 10], [mkEvent('b'), 10.3], [mkEvent('c'), 10.6]),
      CLUSTER_MIN_GAP_PCT,
    )
    expect(out).toHaveLength(1)
    expect(out[0].count).toBe(3)
  })
})
