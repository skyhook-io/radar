import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { TimelineEvent } from '../../types'
import { TimelineList } from './TimelineList'
import { TimelineSwimlanes } from './TimelineSwimlanes'

const NOW = 1_700_000_000_000
const EVENTS: TimelineEvent[] = [
  {
    id: 'a', timestamp: new Date(NOW).toISOString(), source: 'informer',
    kind: 'Deployment', namespace: 'default', name: 'web', eventType: 'add',
  },
]

// Both pure views expose the same controlled-with-internal-fallback contract for
// the lifted filters, so a host can share one set of state across the toggle
// while standalone hosts (passing nothing) keep working on internal state.
describe('TimelineList controlled vs fallback', () => {
  it('reflects the controlled search value', () => {
    const html = renderToString(
      <TimelineList events={EVENTS} isLoading={false} search="ctrl-list" onSearchChange={() => {}} />,
    )
    expect(html).toContain('value="ctrl-list"')
  })

  it('falls back to internal search state when uncontrolled (always-open empty input)', () => {
    const html = renderToString(<TimelineList events={EVENTS} isLoading={false} />)
    // Always-open static search: empty input present, no collapsed magnifier.
    expect(html).toContain('value=""')
    expect(html).not.toContain('aria-label="Search"')
  })

  it('renders the shared toolbar chips (unified with the swimlane)', () => {
    const html = renderToString(<TimelineList events={EVENTS} isLoading={false} />)
    expect(html).toContain('K8s Events')
    expect(html).toContain('Problems')
  })

  it('shows an explicit truncation message after host-side filtering', () => {
    const html = renderToString(
      <TimelineList
        events={EVENTS}
        isLoading={false}
        truncatedAt={10_000}
        isTruncated
        truncationMessage="Only the newest source window was searched."
      />,
    )
    expect(html).toContain('Only the newest source window was searched.')
  })

  it('does not infer truncation when the host explicitly reports a complete source', () => {
    const events = Array.from({ length: 2 }, (_, index) => ({
      ...EVENTS[0],
      id: String(index),
    }))
    const html = renderToString(
      <TimelineList events={events} isLoading={false} truncatedAt={2} isTruncated={false} />,
    )
    expect(html).not.toContain('Showing the newest 2 events')
  })
})

describe('TimelineSwimlanes controlled vs fallback', () => {
  it('reflects the controlled search value', () => {
    const html = renderToString(
      <TimelineSwimlanes events={EVENTS} search="ctrl-swim" onSearchChange={() => {}} />,
    )
    expect(html).toContain('value="ctrl-swim"')
  })

  it('falls back to internal search state when uncontrolled (always-open empty input)', () => {
    const html = renderToString(<TimelineSwimlanes events={EVENTS} />)
    expect(html).toContain('value=""')
    expect(html).not.toContain('aria-label="Search"')
  })

  it('renders the same shared toolbar chips as the list', () => {
    const html = renderToString(<TimelineSwimlanes events={EVENTS} />)
    expect(html).toContain('K8s Events')
    expect(html).toContain('Problems')
  })

  it('renders the single View trigger (Sort + Group live in its popover)', () => {
    const html = renderToString(<TimelineSwimlanes events={EVENTS} />)
    expect(html).toContain('>View<')
  })
})

describe('TimelineList routine activity toggle', () => {
  const at = (id: string, over: Partial<TimelineEvent>): TimelineEvent => ({
    ...EVENTS[0], id, timestamp: new Date(NOW).toISOString(), ...over,
  })
  const ROUTINE: TimelineEvent[] = [
    at('pod-started', { kind: 'Pod', name: 'web-1', source: 'k8s_event', eventType: 'Normal', reason: 'Started' }),
    at('pod-pulled', { kind: 'Pod', name: 'web-2', source: 'k8s_event', eventType: 'Normal', reason: 'Pulled' }),
    at('rs-scaled', { kind: 'ReplicaSet', name: 'web-5f86', eventType: 'update' }),
  ]

  it('shows the checkbox with the hidden count and the (i) explanation', () => {
    const html = renderToString(
      <TimelineList events={EVENTS} isLoading={false} routineEvents={ROUTINE} onShowRoutineChange={() => {}} />,
    )
    expect(html).toContain('Show routine activity')
    expect(html).toContain('type="checkbox"')
    expect(html).toContain('· <!-- -->3 hidden')
    expect(html).toContain('aria-label="About routine activity"')
  })

  it('counts only hidden rows the active filters would show', () => {
    const html = renderToString(
      <TimelineList
        events={EVENTS}
        isLoading={false}
        kindFilter={['Pod']}
        onKindFilterChange={() => {}}
        routineEvents={ROUTINE}
        onShowRoutineChange={() => {}}
      />,
    )
    expect(html).toContain('2 routine events<!-- --> <!-- -->match')
  })

  it('says none hidden when nothing matching is routine', () => {
    const html = renderToString(
      <TimelineList events={EVENTS} isLoading={false} search="web" onSearchChange={() => {}} routineEvents={[]} onShowRoutineChange={() => {}} />,
    )
    expect(html).toContain('· <!-- -->none hidden')
  })

  it('reads shown, with the count, once the user opts in', () => {
    const html = renderToString(
      <TimelineList events={[...EVENTS, ...ROUTINE]} isLoading={false} showRoutine routineEvents={ROUTINE} onShowRoutineChange={() => {}} />,
    )
    expect(html).toContain('checked=""')
    expect(html).toContain('· <!-- -->3 shown')
  })

  it('explains an empty list whose matching rows are all routine', () => {
    const html = renderToString(
      <TimelineList events={[]} isLoading={false} routineEvents={ROUTINE} onShowRoutineChange={() => {}} />,
    )
    expect(html).toContain('All matching activity is routine')
    expect(html).toContain('3 routine events<!-- --> <!-- -->match<!-- -->. Warnings and failures would show here.')
    expect(html).toContain('Show routine activity')
    expect(html).not.toContain('No activity found')
  })

  it('has no toggle when the host does not offer it', () => {
    const html = renderToString(<TimelineList events={EVENTS} isLoading={false} />)
    expect(html).not.toContain('routine activity')
  })

  it('folds a resource\'s rows under its newest card, describing the rest', () => {
    const crash = (id: string, minutesAgo: number, over: Partial<TimelineEvent>) =>
      at(id, { kind: 'Pod', name: 'api-1', timestamp: new Date(NOW - minutesAgo * 60_000).toISOString(), ...over })
    const rows = [
      crash('newest', 0, { eventType: 'update', healthState: 'unhealthy', owner: { kind: 'ReplicaSet', name: 'api-5f86' } }),
      crash('b1', 1, { source: 'k8s_event', eventType: 'Warning', reason: 'BackOff' }),
      crash('b2', 2, { source: 'k8s_event', eventType: 'Warning', reason: 'BackOff' }),
      crash('u1', 3, { eventType: 'update', healthState: 'degraded' }),
    ]
    const folded = renderToString(<TimelineList events={rows} isLoading={false} foldPerResource={(e) => e.kind === 'Pod'} />)
    expect(folded).toContain('3<!-- --> more on this <!-- -->Pod<!-- -->: <!-- -->BackOff ×2, 1 status change')
    expect(folded).toContain('deploy<!-- -->/</span>api')
    expect(folded).toContain('data-event-id="newest"')
    const plain = renderToString(<TimelineList events={rows} isLoading={false} />)
    expect(plain).not.toContain('more on this')
  })

  it('opens a fold that holds the selected row, and counts each event\'s occurrences', () => {
    const crash = (id: string, minutesAgo: number, over: Partial<TimelineEvent>) =>
      at(id, { kind: 'Pod', name: 'api-1', timestamp: new Date(NOW - minutesAgo * 60_000).toISOString(), ...over })
    const rows = [
      crash('newest', 0, { eventType: 'update', healthState: 'unhealthy' }),
      crash('backoff', 1, { source: 'k8s_event', eventType: 'Warning', reason: 'BackOff', count: 43 }),
    ]
    const html = renderToString(
      <TimelineList events={rows} isLoading={false} selectedEventId="backoff" foldPerResource={() => true} />,
    )
    expect(html).toContain('BackOff ×43')
    expect(html).toContain('aria-expanded="true"')
    expect(html).toContain('data-event-id="backoff"')
  })

  it('keeps the (i) explanation reachable by keyboard', () => {
    const html = renderToString(<TimelineList events={EVENTS} isLoading={false} routineEvents={[]} onShowRoutineChange={() => {}} />)
    expect(html).toContain('<button type="button" aria-label="About routine activity"')
  })
})
