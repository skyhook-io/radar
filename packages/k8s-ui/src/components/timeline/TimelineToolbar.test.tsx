import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { TimelineEvent } from '../../types'
import type { ActivityFilterKey, ActivitySelection } from './timeline-filters'
import { activityKeysToSelection, selectionToActivityKeys } from './timeline-filters'
import { TimelineToolbar, ViewMenu, DeletedEventsToggle, PinnedOnlyToggle } from './TimelineToolbar'

function ev(partial: Partial<TimelineEvent>): TimelineEvent {
  return {
    id: Math.random().toString(36),
    timestamp: '2026-01-01T00:00:00Z',
    source: 'informer',
    kind: 'Pod',
    namespace: 'default',
    name: 'thing',
    eventType: 'update',
    ...partial,
  }
}

const EVENTS: TimelineEvent[] = [
  ev({ source: 'informer', eventType: 'add' }),
  ev({ source: 'informer', eventType: 'update', healthState: 'unhealthy' }),
  ev({ source: 'informer', eventType: 'delete' }),
  ev({ source: 'k8s_event', eventType: 'Warning', reason: 'BackOff' }),
]

const baseProps = {
  search: '',
  onSearchChange: () => {},
  searchShortcutId: 'test-search',
  activityFilter: [] as ActivityFilterKey[],
  onActivityFilterChange: () => {},
  showDeleted: true,
    onShowDeletedChange: () => {},
  kindFilter: [] as string[],
  onKindFilterChange: () => {},
  kindOptions: ['Deployment', 'Pod'],
}

describe('TimelineToolbar SSR', () => {
  it('renders the two-axis activity control: source radiogroup + Problems toggle', () => {
    const html = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    expect(html).toContain('aria-label="Activity source"')
    expect(html).toContain('>All<')
    expect(html).toContain('>Changes<')
    expect(html).toContain('>K8s Events<')
    expect(html).toContain('>Problems<')
    // The old severity pills are gone — they were subsets posing as peers.
    expect(html).not.toContain('>Warnings<')
    expect(html).not.toContain('>Unhealthy<')
    expect(html).toContain('>3<') // changes count
  })

  it('shows per-source counts and a source-scoped Problems count', () => {
    const html = renderToString(
      <TimelineToolbar
        {...baseProps}
        stats={{ total: 99, changes: 42, k8sEvents: 57, warnings: 7, unhealthy: 5, deleted: 3 }}
      />,
    )
    expect(html).toContain('>99<')
    expect(html).toContain('>42<')
    expect(html).toContain('>57<')
    // Source = All → Problems previews warnings + unhealthy.
    expect(html).toContain('>12<')
  })

  it('scopes the Problems count to the picked source', () => {
    const stats = { total: 99, changes: 42, k8sEvents: 57, warnings: 7, unhealthy: 5, deleted: 3 }
    const changesOnly = renderToString(
      <TimelineToolbar {...baseProps} stats={stats} activityFilter={['changes']} />,
    )
    expect(changesOnly).toContain('>5<') // unhealthy slice
    const eventsOnly = renderToString(
      <TimelineToolbar {...baseProps} stats={stats} activityFilter={['k8s_events']} />,
    )
    expect(eventsOnly).toContain('>7<') // warnings slice
  })

  it('reads legacy key sets back into the two axes (deep-link compat)', () => {
    // ['warnings'] = problems within K8s Events: that segment is checked and
    // the Problems toggle is pressed.
    const html = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} activityFilter={['warnings']} />,
    )
    expect(html).toContain('aria-checked="true"')
    expect(html).toContain('aria-pressed="true"')
  })

  it('renders the amber dot on the Problems toggle only (no per-pill colors)', () => {
    const html = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    expect(html).toContain('bg-amber-500')
    expect(html).not.toContain('bg-blue-500')
    expect(html).not.toContain('bg-rose-500')
  })

  it('renders ONE View trigger (Sort + Group live in its popover), swimlane only', () => {
    const withOpts = renderToString(
      <TimelineToolbar
        {...baseProps}
        events={EVENTS}
        view="swimlane"
        onViewChange={() => {}}
        viewOptions={{
          sort: { value: 'importance', onChange: () => {} },
          grouping: { value: 'app', onChange: () => {} },
        }}
      />,
    )
    // The trigger renders; the popover is closed on the server, so the Sort /
    // Group rows are not in the initial markup.
    expect(withOpts).toContain('>View<')
    expect(withOpts).not.toContain('role="radiogroup" aria-label="Lane sort"')
    // The view toggle is labeled — the words themselves are the affordance.
    expect(withOpts).toContain('>List<')
    expect(withOpts).toContain('>Timeline<')

    // List view (TimelineList) passes no viewOptions — no View trigger.
    const noOpts = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} view="list" onViewChange={() => {}} />,
    )
    expect(noOpts).not.toContain('>View<')
  })

  it('dims a zero-count chip in place instead of hiding it (position memory)', () => {
    // EVENTS has 3 changes and 1 warning K8s event; craft stats with zeros.
    const html = renderToString(
      <TimelineToolbar
        {...baseProps}
        stats={{ total: 5, changes: 0, k8sEvents: 5, warnings: 0, unhealthy: 0, deleted: 0 }}
      />,
    )
    // Changes 0 and Problems 0 dim + disable; K8s Events stays interactive.
    expect(html).toContain('opacity-45')
    expect(html.match(/opacity-45/g)?.length).toBe(2)
    expect(html.match(/disabled=""/g)?.length).toBe(2)
  })

  it('renders the Kinds chip with its own badge = selected kinds, hidden when none', () => {
    const none = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    // Chip always renders its label; badge hidden with no selection.
    expect(none).toContain('>Kinds<')
    expect(none).not.toContain('bg-accent px-1.5')

    const two = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} kindFilter={['Pod', 'Service']} />,
    )
    expect(two).toContain('bg-accent px-1.5')
    expect(two).toContain('>2<')
  })

  it('keeps Show-deleted (View menu) and the kinds picker (Kinds chip) out of the bar on SSR', () => {
    const html = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} showDeleted={false} kindFilter={['Pod']} />,
    )
    // Both popovers are closed on the server: the Kinds chip is present, but its
    // picker body and the View menu's Show-deleted switch are not yet in the DOM.
    expect(html).toContain('>Kinds<')
    expect(html).not.toContain('Filter kinds')
    expect(html).not.toContain('Show deleted')
  })

  it('renders the search always open with a static width (no collapse)', () => {
    const html = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    // Always a real text input, even when empty — the collapsed magnifier button
    // is retired for this toolbar.
    expect(html).toContain('placeholder="Search..."')
    expect(html).not.toContain('(press /)')
    expect(html).toContain('value=""')
    expect(html).not.toContain('aria-label="Search"')
    // Compact static width — focus/typing can never resize it.
    expect(html).toContain('w-44')
  })

  it('reflects the controlled search value', () => {
    const html = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} search="checkout-api" />,
    )
    expect(html).toContain('value="checkout-api"')
  })

  it('places the search first in the filters row, before the activity segments', () => {
    const html = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    // Founder-locked order: search leads the filters row.
    const searchIdx = html.indexOf('placeholder="Search..."')
    expect(searchIdx).toBeGreaterThan(-1)
    expect(searchIdx).toBeLessThan(html.indexOf('>All<'))
  })

  it('renders the range dropdown only when range props are supplied', () => {
    const withRange = renderToString(
      <TimelineToolbar
        {...baseProps}
        events={EVENTS}
        rangeOptions={[{ value: '1h', label: '1 hour' }, { value: '24h', label: '24 hours' }]}
        timeRange="1h"
        onTimeRangeChange={() => {}}
      />,
    )
    expect(withRange).toContain('1 hour')
    expect(withRange).toContain('24 hours')

    const withoutRange = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    expect(withoutRange).not.toContain('24 hours')
  })

  it('renders swimlane-style counts (resources · events) and list-style (events only)', () => {
    const swim = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} counts={{ resources: 4, events: 12 }} />,
    )
    expect(swim).toContain('4 resources · ')
    expect(swim).toContain('12 events')

    const list = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} counts={{ events: 12 }} />,
    )
    expect(list).toContain('12 events')
    expect(list).not.toContain('resources · ')
  })

  it('renders refresh only when onRefresh is given', () => {
    const withRefresh = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} onRefresh={() => {}} />,
    )
    expect(withRefresh).toContain('aria-label="Refresh"')
    const noRefresh = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    expect(noRefresh).not.toContain('aria-label="Refresh"')
  })

  it('normalizes an unmappable legacy multi-select to the widest reading (All, problems off)', () => {
    const html = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} activityFilter={['changes', 'warnings']} />,
    )
    // Showing more than a stale link intended beats silently hiding activity —
    // and the Problems toggle stays unpressed.
    expect(html).toContain('aria-checked="true"')
    expect(html).not.toContain('aria-pressed="true"')
  })

  // The live/paused chip moved out of the toolbar into the scrubber header — the
  // toolbar must never render it, whatever props flow through.
  it('never renders the live/paused chip', () => {
    const html = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    expect(html).not.toContain('timeline-live-chip')
    expect(html).not.toContain('timeline-paused-chip')
  })
})

describe('ViewMenu SSR', () => {
  it('renders the View trigger with the popover closed', () => {
    const html = renderToString(
      <ViewMenu
        viewOptions={{
          sort: { value: 'recent', onChange: () => {} },
          grouping: { value: 'app', onChange: () => {} },
        }}
      />,
    )
    expect(html).toContain('>View<')
    expect(html).toContain('aria-haspopup="menu"')
    expect(html).toContain('aria-expanded="false"')
    // Closed on the server: the Sort/Group radio rows are not in the markup.
    expect(html).not.toContain('role="radio"')
  })

  it('DeletedEventsToggle: labeled "Deleted", marked "Deleted hidden" when hiding', () => {
    const shown = renderToString(<DeletedEventsToggle showDeleted={true} onChange={() => {}} />)
    expect(shown).toContain('aria-pressed="false"')
    expect(shown).toContain('Hide delete events')
    expect(shown).toContain('>Deleted<')
    expect(shown).not.toContain('Deleted hidden')
    const hiding = renderToString(<DeletedEventsToggle showDeleted={false} onChange={() => {}} />)
    expect(hiding).toContain('aria-pressed="true"')
    expect(hiding).toContain('Show delete events')
    expect(hiding).toContain('Deleted hidden')
  })
})

describe('activity two-axis mapping (source × problems ↔ ActivityFilterKey[])', () => {
  const CANONICAL: [ActivitySelection, ActivityFilterKey[]][] = [
    [{ source: 'all', problemsOnly: false }, []],
    [{ source: 'changes', problemsOnly: false }, ['changes']],
    [{ source: 'k8s_events', problemsOnly: false }, ['k8s_events']],
    [{ source: 'changes', problemsOnly: true }, ['unhealthy']],
    [{ source: 'k8s_events', problemsOnly: true }, ['warnings']],
    [{ source: 'all', problemsOnly: true }, ['warnings', 'unhealthy']],
  ]

  it('round-trips all six reachable states through the shared key vocabulary', () => {
    for (const [sel, keys] of CANONICAL) {
      expect(selectionToActivityKeys(sel)).toEqual(keys)
      expect(activityKeysToSelection(keys)).toEqual(sel)
    }
  })

  it('falls back to All/off for legacy multi-select key sets', () => {
    expect(activityKeysToSelection(['changes', 'warnings'])).toEqual({ source: 'all', problemsOnly: false })
    expect(activityKeysToSelection(['changes', 'k8s_events'])).toEqual({ source: 'all', problemsOnly: false })
  })
})

describe('PinnedOnlyToggle', () => {
  it('is hidden from the toolbar when nothing is pinned', () => {
    const html = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} pinnedCount={0} pinnedOnly={false} onPinnedOnlyChange={() => {}} />)
    expect(html).not.toContain('Show pinned rows only')
  })

  it('renders in the toolbar once rows are pinned, quiet by default', () => {
    const html = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} pinnedCount={2} pinnedOnly={false} onPinnedOnlyChange={() => {}} />)
    expect(html).toContain('Show pinned rows only')
    expect(html).toContain('aria-pressed="false"')
  })

  it('shows the engaged treatment when active', () => {
    const html = renderToString(<PinnedOnlyToggle pinnedOnly={true} pinnedCount={2} onChange={() => {}} />)
    expect(html).toContain('aria-pressed="true"')
    expect(html).toContain('only')
  })
})
