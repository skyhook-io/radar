import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ApiError } from '../../api/client'
import { TimelineLoadError, TimelineStaleNote } from './TimelineLoadError'

const driverError = 'open PostgreSQL timeline database: dial tcp 10.0.0.5:5432: connect: connection refused'

function databaseDown(): ApiError {
  return new ApiError('Timeline history is unavailable because Radar can\'t connect to its PostgreSQL database.', 503, {
    error_code: 'timeline_database_unreachable',
    detail: driverError,
  })
}

function render(error: Error, onOpenDiagnostics?: () => void): string {
  return renderToStaticMarkup(
    <TimelineLoadError error={error} onRetry={() => {}} onOpenDiagnostics={onOpenDiagnostics} />,
  )
}

describe('TimelineLoadError', () => {
  it('names the database and points to the logs when PostgreSQL is unreachable', () => {
    const html = render(databaseDown(), () => {})
    expect(html).toContain('Timeline history is unavailable')
    expect(html).toContain('can&#x27;t connect to its PostgreSQL database')
    expect(html).toContain('[timeline]')
    // The driver's error names the database host; it belongs in the logs and
    // diagnostics, not on the page.
    expect(html).not.toContain(driverError)
    expect(html).toContain('Open diagnostics')
    expect(html).not.toContain('Failed to load timeline data')
  })

  it('omits the diagnostics link when the host cannot open diagnostics', () => {
    expect(render(databaseDown())).not.toContain('Open diagnostics')
  })

  it('keeps the generic state, without a diagnostics link, for other failures', () => {
    const html = render(new ApiError('upstream timed out', 502, { error: 'upstream timed out' }), () => {})
    expect(html).toContain('Failed to load timeline data')
    expect(html).toContain('upstream timed out')
    expect(html).not.toContain('Open diagnostics')
  })
})

describe('TimelineStaleNote', () => {
  const note = (error: Error) =>
    renderToStaticMarkup(<TimelineStaleNote error={error} subject="timeline" onRetry={() => {}} />)

  it('names the database and points to the logs when PostgreSQL is the cause', () => {
    const html = note(databaseDown())
    expect(html).toContain('can&#x27;t connect to its PostgreSQL database, so the timeline may be stale')
    expect(html).toContain('[timeline]')
    expect(html).not.toContain(driverError)
  })

  it('keeps the generic note for other failures', () => {
    const html = note(new ApiError('upstream timed out', 502, { error: 'upstream timed out' }))
    expect(html).toContain('Live updates are failing, so the timeline may be stale')
    expect(html).not.toContain('PostgreSQL')
  })
})
