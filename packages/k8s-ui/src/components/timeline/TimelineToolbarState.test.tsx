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
