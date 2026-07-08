import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { TimelineEvent } from '../../types'
import type { ActivityFilterKey } from './timeline-filters'
import { countActiveViewOptions } from './timeline-filters'
import { TimelineToolbar, OptionMenu, DeletedEventsToggle, PinnedOnlyToggle } from './TimelineToolbar'

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
  it('renders the activity segmented control with derived counts', () => {
    const html = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    expect(html).toContain('All')
    expect(html).toContain('Changes')
    expect(html).toContain('Warnings')
    expect(html).toContain('Unhealthy')
    expect(html).toContain('K8s Events')
    expect(html).toContain('>3<') // changes count
  })

  it('shows the TOTAL event count on the All cell', () => {
    const html = renderToString(
      <TimelineToolbar
        {...baseProps}
        stats={{ total: 99, changes: 42, warnings: 7, unhealthy: 5, deleted: 3 }}
      />,
    )
    // Only the All cell surfaces the grand total.
    expect(html).toContain('>99<')
    expect(html).toContain('>42<')
    expect(html).toContain('>7<')
  })

  it('renders colored dots on the Changes / Warnings / Unhealthy cells', () => {
    const html = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    // Changes = info-blue, Warnings = amber, Unhealthy = rose.
    expect(html).toContain('bg-blue-500')
    expect(html).toContain('bg-amber-500')
    expect(html).toContain('bg-rose-500')
  })

  it('renders separate Sort and Group dropdowns (swimlane only), not a combined View menu', () => {
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
    // Two labeled triggers, each showing its current value inline.
    expect(withOpts).toContain('>Sort<')
    expect(withOpts).toContain('>Importance<')
    expect(withOpts).toContain('>Group<')
    expect(withOpts).toContain('>Applications<')
    expect(withOpts).not.toContain('>View<')
    // The view toggle is labeled — the words themselves are the affordance.
    expect(withOpts).toContain('>List<')
    expect(withOpts).toContain('>Timeline<')

    // List view (TimelineList) passes no viewOptions — neither dropdown renders.
    const noOpts = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} view="list" onViewChange={() => {}} />,
    )
    expect(noOpts).not.toContain('>Sort<')
    expect(noOpts).not.toContain('>Group<')
  })

  it('shows non-default Sort/Group values on the triggers (no hidden state)', () => {
    const some = renderToString(
      <TimelineToolbar
        {...baseProps}
        events={EVENTS}
        view="swimlane"
        onViewChange={() => {}}
        viewOptions={{
          sort: { value: 'recent', onChange: () => {} },
          grouping: { value: 'owner', onChange: () => {} },
        }}
      />,
    )
    expect(some).toContain('>Recent activity<')
    expect(some).toContain('>Owners<')
  })

  it('labels the activity pill group with a "Show" prefix', () => {
    const html = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    expect(html).toContain('>Show<')
  })

  it('renders the FreshnessControl instead of the refresh icon when freshness is supplied', () => {
    const html = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} onRefresh={() => {}} freshness={{ dataUpdatedAt: Date.now(), isFetching: false }} />,
    )
    expect(html).toContain('Auto-updating')
    expect(html).not.toContain('aria-label="Refresh"')
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
    expect(html).toContain('placeholder="Search... (press /)"')
    expect(html).toContain('value=""')
    expect(html).not.toContain('aria-label="Search"')
    // Static width class — never grows/shrinks on focus/typing.
    expect(html).toContain('w-56')
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
    expect(html.indexOf('placeholder="Search... (press /)"')).toBeLessThan(html.indexOf('>All<'))
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

  it('renders refresh only when onRefresh is given (no freshness)', () => {
    const withRefresh = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} onRefresh={() => {}} />,
    )
    expect(withRefresh).toContain('aria-label="Refresh"')
    const noRefresh = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    expect(noRefresh).not.toContain('aria-label="Refresh"')
  })

  it('marks multiple activity cells active simultaneously (multi-select)', () => {
    const html = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} activityFilter={['changes', 'warnings']} />,
    )
    expect(html).toContain('Changes')
    expect(html).toContain('Warnings')
    // Two pills pressed at once (semantic state, not styling class).
    expect(html.match(/aria-pressed="true"/g)?.length).toBeGreaterThanOrEqual(2)

    // With an empty selection only "All" is pressed (the deleted toggle also
    // carries aria-pressed, so assert on presence, not an exact count).
    const allEmpty = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} activityFilter={[]} />)
    expect(allEmpty).toContain('aria-pressed="true"')
  })

  // The live/paused chip moved out of the toolbar into the scrubber header — the
  // toolbar must never render it, whatever props flow through.
  it('never renders the live/paused chip', () => {
    const html = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    expect(html).not.toContain('timeline-live-chip')
    expect(html).not.toContain('timeline-paused-chip')
  })
})

describe('OptionMenu SSR', () => {
  const SORT_OPTS = [
    { value: 'importance', label: 'Importance', tooltip: 'x' },
    { value: 'recent', label: 'Recent activity', tooltip: 'y' },
  ]

  it('renders a labeled trigger showing the current value, popover closed', () => {
    const html = renderToString(
      <OptionMenu label="Sort" options={SORT_OPTS} value="recent" onChange={() => {}} />,
    )
    expect(html).toContain('>Sort<')
    expect(html).toContain('>Recent activity<')
    expect(html).toContain('aria-haspopup="menu"')
    expect(html).toContain('aria-expanded="false"')
    // Closed on the server: the radio rows are not in the initial markup.
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

describe('countActiveViewOptions', () => {
  it('returns 0 for all defaults (deleted shown, grouping on, sort by importance)', () => {
    expect(countActiveViewOptions({ grouping: 'app', sort: 'importance' })).toBe(0)
  })

  it('counts each non-default view choice: grouping off + sort off = 2 (deleted + kinds have their own toggles)', () => {
    expect(countActiveViewOptions({ grouping: 'owner', sort: 'recent' })).toBe(2)
  })

  it('counts a non-default sort on its own', () => {
    expect(countActiveViewOptions({ grouping: 'app', sort: 'name' })).toBe(1)
  })

  it('ignores grouping/sort when undefined (list view has neither)', () => {
    expect(countActiveViewOptions({})).toBe(0)
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
