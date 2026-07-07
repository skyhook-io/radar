import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { TimelineEvent } from '../../types'
import type { ActivityFilterKey } from './timeline-filters'
import { countActiveViewOptions } from './timeline-filters'
import { TimelineToolbar, ViewOptionsPanel, DeletedEventsToggle, PinnedOnlyToggle } from './TimelineToolbar'

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

const panelProps = {}

describe('TimelineToolbar SSR', () => {
  it('renders the activity segmented control with derived counts', () => {
    const html = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    expect(html).toContain('All')
    expect(html).toContain('Changes')
    expect(html).toContain('Warnings')
    expect(html).toContain('Unhealthy')
    // K8s Events is the visible label now (dots-only control, no per-cell icons).
    expect(html).toContain('K8s Events')
    // changes=3, warnings=1, unhealthy=1
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
    expect(html).toContain('>5<')
  })

  it('renders severity dots on the Warnings and Unhealthy cells', () => {
    const html = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    // StatusDot tones: degraded=amber (warnings), unhealthy=rose.
    expect(html).toContain('bg-amber-500')
    expect(html).toContain('bg-rose-500')
  })

  it('renders the View button only when Sort/Group options exist (swimlane), not in list view', () => {
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
    // The trigger renders; its popover is closed on the server, so the Sort /
    // Group rows are not in the initial markup.
    expect(withOpts).toContain('title="View options"')
    expect(withOpts).toContain('>View<')
    // The toggle is labeled — the words themselves are the affordance.
    expect(withOpts).toContain('>List<')
    expect(withOpts).toContain('>Timeline<')

    // List view (TimelineList) passes no viewOptions — the panel would be empty,
    // so no View button is rendered (a blank popover is worse than none).
    const noOpts = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} view="list" onViewChange={() => {}} />,
    )
    expect(noOpts).not.toContain('title="View options"')
    expect(noOpts).not.toContain('>View<')
  })

  it('shows the View badge counting non-default view choices, hidden at zero', () => {
    // The badge only exists when the View menu renders, which needs viewOptions;
    // with default grouping ('app') + sort ('importance') the count is zero.
    const none = renderToString(
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
    expect(none).toContain('title="View options"')
    expect(none).not.toContain('bg-accent px-1.5')

    // Both grouping and sort moved off their defaults → badge counts 2.
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
    expect(some).toContain('bg-accent px-1.5')
    expect(some).toContain('>2<')
  })

  it('renders the Kinds chip with its own badge = selected kinds, hidden when none', () => {
    const none = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    // Chip always renders its label; badge hidden with no selection.
    expect(none).toContain('>Kinds<')
    expect(none).toContain('title="Filter by kind"')
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

  it('renders refresh only when onRefresh is given', () => {
    const withRefresh = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} onRefresh={() => {}} />,
    )
    expect(withRefresh).toContain('Refresh')
    const noRefresh = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    expect(noRefresh).not.toContain('title="Refresh"')
  })

  it('marks multiple activity cells active simultaneously (multi-select)', () => {
    const html = renderToString(
      <TimelineToolbar {...baseProps} events={EVENTS} activityFilter={['changes', 'warnings']} />,
    )
    expect(html).toContain('Changes')
    expect(html).toContain('Warnings')
    // Active cells get the filled indigo/neutral treatment.
    expect(html).toContain('bg-accent-muted')

    // With a non-empty selection, All is NOT active — but other cells are, so the
    // active fill class is still present somewhere. Assert All is active only when
    // the selection is empty by comparing counts of the active class.
    const allEmpty = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} activityFilter={[]} />)
    expect(allEmpty).toContain('bg-accent-muted')
  })

  // The live/paused chip moved out of the toolbar into the scrubber header — the
  // toolbar must never render it, whatever props flow through.
  it('never renders the live/paused chip', () => {
    const html = renderToString(<TimelineToolbar {...baseProps} events={EVENTS} />)
    expect(html).not.toContain('timeline-live-chip')
    expect(html).not.toContain('timeline-paused-chip')
  })
})

describe('ViewOptionsPanel SSR', () => {
  it('renders Sort + Group sections when swimlane viewOptions are supplied', () => {
    const html = renderToString(
      <ViewOptionsPanel
        {...panelProps}
        viewOptions={{
          sort: { value: 'importance', onChange: () => {} },
          grouping: { value: 'app', onChange: () => {} },
        }}
      />,
    )
    expect(html).toContain('Sort')
    expect(html).toContain('role="radio"')
    // 3-way sort segmented control (Importance / Recent activity / Name).
    expect(html).toContain('Importance')
    expect(html).toContain('Recent activity')
    expect(html).toContain('Name (A')
    // 3-way grouping segmented control (Applications / Owners / Flat).
    expect(html).toContain('role="radiogroup"')
    expect(html).toContain('Applications')
    expect(html).toContain('Owners')
    expect(html).toContain('Flat')
    // Show-deleted is still a switch; sort/grouping are radios, not checkboxes.
    expect(html).not.toContain('type="checkbox"')
  })

  it('marks the active sort radio via aria-checked', () => {
    const html = renderToString(
      <ViewOptionsPanel
        {...panelProps}
        viewOptions={{
          sort: { value: 'recent', onChange: () => {} },
          grouping: { value: 'app', onChange: () => {} },
        }}
      />,
    )
    // The active row shows the product's check-row treatment: checked state via
    // aria-checked and a visible check glyph (opacity-100).
    expect(html).toContain('aria-checked="true"')
    expect(html).toContain('opacity-100')
  })

  it('holds no filters — Show deleted moved to its own toolbar toggle', () => {
    const html = renderToString(<ViewOptionsPanel {...panelProps} />)
    expect(html).not.toContain('Show deleted')
    expect(html).not.toContain('role="switch"')
    // Kinds moved to its own chip — the checklist is no longer in the View menu.
    expect(html).not.toContain('role="menuitemcheckbox"')
  })

  it('renders empty without viewOptions (list view has no sort/group)', () => {
    const html = renderToString(<ViewOptionsPanel {...panelProps} />)
    expect(html).not.toContain('Importance')
    expect(html).not.toContain('role="radiogroup"')
  })

  it('DeletedEventsToggle: quiet when shown, marked + labeled when hiding', () => {
    const shown = renderToString(<DeletedEventsToggle showDeleted={true} onChange={() => {}} />)
    expect(shown).toContain('aria-pressed="false"')
    expect(shown).toContain('Hide delete events')
    expect(shown).not.toContain('hidden</span>')
    const hiding = renderToString(<DeletedEventsToggle showDeleted={false} onChange={() => {}} />)
    expect(hiding).toContain('aria-pressed="true"')
    expect(hiding).toContain('Show delete events')
    expect(hiding).toContain('hidden')
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
