import { describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import type { TimelineEventsResult, TimelineSource } from '../../api/timelineSource'

// The list reads its source from context; hand it a fixed empty result so the
// test is about the note row alone.
const eventsResult: TimelineEventsResult = { data: [], isLoading: false, isFetching: false, isError: false, refetch: () => {} }
const fakeSource: TimelineSource = {
  capabilities: { mode: 'local', ringLimit: 10_000 },
  useEvents: () => eventsResult,
}
vi.mock('../../context/TimelineSource', () => ({ useTimelineSource: () => fakeSource }))

import { TimelineRetentionNoteProvider } from '../../context/TimelineCustomization'
import { TimelineList } from './TimelineList'

describe('TimelineList history-limit row', () => {
  const props = {
    namespaces: [],
    showDeleted: false,
    onShowDeletedChange: () => {},
    showRoutine: false,
    onShowRoutineChange: () => {},
    search: '',
    onSearchChange: () => {},
    activityFilter: [],
    onActivityFilterChange: () => {},
    kindFilter: [],
    onKindFilterChange: () => {},
  }
  const withNote = (node: React.ReactNode) => (
    <TimelineRetentionNoteProvider value={<a href="/plans">Host note</a>}>{node}</TimelineRetentionNoteProvider>
  )

  it('shows the host note in its own row', () => {
    const html = renderToStaticMarkup(withNote(<TimelineList {...props} />))
    expect(html).toContain('data-testid="timeline-retention-note"')
    expect(html).toContain('<a href="/plans">Host note</a>')
  })

  it('renders no row without a host note (standalone Radar)', () => {
    expect(renderToStaticMarkup(<TimelineList {...props} />)).not.toContain('timeline-retention-note')
  })

  it('leaves the note to the view when a scrubber owns the range', () => {
    const html = renderToStaticMarkup(withNote(<TimelineList {...props} selectionWindow={{ fromMs: 0, toMs: 1 }} />))
    expect(html).not.toContain('timeline-retention-note')
  })
})
