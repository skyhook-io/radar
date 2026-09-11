import { describe, expect, it } from 'vitest'

import {
  LOG_EXPORT_FORMATS,
  previewLogExport,
  serializeLogEntries,
  type ExportableLogEntry,
  type LogExportFormat,
} from './log-export'

const ESC = '\u001b'

const entry = (over: Partial<ExportableLogEntry> = {}): ExportableLogEntry => ({
  timestamp: '2024-01-20T10:30:00.123456789Z',
  content: 'connection refused',
  container: 'app',
  pod: 'api-7d9f',
  ...over,
})

const opts = (over: Partial<{ format: LogExportFormat; showTimestamps: boolean; showPodName: boolean }> = {}) => ({
  format: 'txt' as LogExportFormat,
  showTimestamps: true,
  showPodName: true,
  ...over,
})

describe('serializeLogEntries — text', () => {
  it('emits the raw RFC3339 timestamp rather than any display format', () => {
    const { content } = serializeLogEntries([entry()], opts({ showPodName: false }))
    expect(content).toBe('2024-01-20T10:30:00.123456789Z connection refused')
  })

  it('omits timestamps when the viewer has them hidden', () => {
    const { content } = serializeLogEntries([entry()], opts({ showTimestamps: false, showPodName: false }))
    expect(content).toBe('connection refused')
  })

  it('attributes pod and container when pod names are shown', () => {
    const { content } = serializeLogEntries([entry()], opts({ showTimestamps: false }))
    expect(content).toBe('[api-7d9f/app] connection refused')
  })

  it('falls back to the pod alone when no container is known', () => {
    const { content } = serializeLogEntries([entry({ container: undefined })], opts({ showTimestamps: false }))
    expect(content).toBe('[api-7d9f] connection refused')
  })

  it('drops pod attribution for entries with no pod, even when the toggle is on', () => {
    const { content } = serializeLogEntries([entry({ pod: undefined })], opts({ showTimestamps: false }))
    expect(content).toBe('connection refused')
  })

  it('skips an empty timestamp instead of emitting a leading space', () => {
    const { content } = serializeLogEntries([entry({ timestamp: '' })], opts({ showPodName: false }))
    expect(content).toBe('connection refused')
  })

  it('strips ANSI escapes', () => {
    const { content } = serializeLogEntries(
      [entry({ content: `${ESC}[31mconnection refused${ESC}[0m` })],
      opts({ showTimestamps: false, showPodName: false }),
    )
    expect(content).toBe('connection refused')
  })

  it('preserves raw JSON content rather than any collapsed rendering', () => {
    const json = '{"level":"error","msg":"connection refused","stack":"a\\nb"}'
    const { content } = serializeLogEntries([entry({ content: json })], opts({ showTimestamps: false, showPodName: false }))
    expect(content).toBe(json)
  })

  it('joins entries with newlines and returns empty for no entries', () => {
    const { content } = serializeLogEntries(
      [entry({ content: 'first' }), entry({ content: 'second' })],
      opts({ showTimestamps: false, showPodName: false }),
    )
    expect(content).toBe('first\nsecond')
    expect(serializeLogEntries([], opts()).content).toBe('')
  })
})

describe('serializeLogEntries — structured formats', () => {
  it('keeps the timestamp field in JSON even when the display toggle is off', () => {
    const { content, mime, extension } = serializeLogEntries(
      [entry()],
      opts({ format: 'json', showTimestamps: false }),
    )
    expect(JSON.parse(content)).toEqual([{
      timestamp: '2024-01-20T10:30:00.123456789Z',
      pod: 'api-7d9f',
      container: 'app',
      content: 'connection refused',
    }])
    expect(mime).toBe('application/json;charset=utf-8')
    expect(extension).toBe('json')
  })

  it('keeps the pod key in JSON even when pod attribution is off, since the display toggle is not a schema', () => {
    const { content } = serializeLogEntries([entry()], opts({ format: 'json', showPodName: false }))
    expect(Object.keys(JSON.parse(content)[0])).toEqual(['timestamp', 'pod', 'container', 'content'])
  })

  it('omits the pod key only for entries that genuinely have no pod', () => {
    const { content } = serializeLogEntries([entry({ pod: undefined })], opts({ format: 'json' }))
    expect(Object.keys(JSON.parse(content)[0])).toEqual(['timestamp', 'container', 'content'])
  })

  it('strips ANSI from structured content too', () => {
    const { content } = serializeLogEntries(
      [entry({ content: `${ESC}[31mboom${ESC}[0m` })],
      opts({ format: 'json' }),
    )
    expect(JSON.parse(content)[0].content).toBe('boom')
  })

  it('writes a CSV header matching the columns it emits', () => {
    const { content, mime } = serializeLogEntries([entry()], opts({ format: 'csv' }))
    expect(content.split('\n')).toEqual([
      'timestamp,pod,container,content',
      '"2024-01-20T10:30:00.123456789Z","api-7d9f","app","connection refused"',
    ])
    expect(mime).toBe('text/csv;charset=utf-8')
  })

  it('keeps the CSV pod column when pod attribution is off but the data has pods', () => {
    const { content } = serializeLogEntries([entry()], opts({ format: 'csv', showPodName: false }))
    expect(content.split('\n')[0]).toBe('timestamp,pod,container,content')
  })

  it('drops the CSV pod column only when no entry has a pod', () => {
    const { content } = serializeLogEntries([entry({ pod: undefined })], opts({ format: 'csv' }))
    expect(content.split('\n')).toEqual([
      'timestamp,container,content',
      '"2024-01-20T10:30:00.123456789Z","app","connection refused"',
    ])
  })

  it('keeps the column count aligned when only some entries carry a pod', () => {
    const { content } = serializeLogEntries(
      [entry({ pod: undefined }), entry()],
      opts({ format: 'csv' }),
    )
    const [header, ...rows] = content.split('\n')
    expect(header.split(',').length).toBe(4)
    for (const row of rows) expect(row.split('","').length).toBe(4)
  })

  it('neutralizes cells a spreadsheet would evaluate as a formula', () => {
    const { content } = serializeLogEntries(
      [entry({ content: '=cmd|\' /c calc\'!A1' }), entry({ content: '+1' }), entry({ content: '-rf /' }), entry({ content: '@SUM(A1)' })],
      opts({ format: 'csv', showPodName: false }),
    )
    const cells = content.split('\n').slice(1).map(r => r.split(',').pop())
    expect(cells).toEqual([
      '"\'=cmd|\' /c calc\'!A1"',
      '"\'+1"',
      '"\'-rf /"',
      '"\'@SUM(A1)"',
    ])
  })

  it('leaves ordinary content unprefixed', () => {
    const { content } = serializeLogEntries([entry()], opts({ format: 'csv', showPodName: false }))
    expect(content).toContain('"connection refused"')
  })

  it('doubles quotes inside a CSV cell', () => {
    const { content } = serializeLogEntries(
      [entry({ content: 'said "hello"', pod: undefined })],
      opts({ format: 'csv' }),
    )
    expect(content.split('\n')[1]).toBe('"2024-01-20T10:30:00.123456789Z","app","said ""hello"""')
  })

  it('emits a header-only CSV and an empty JSON array for no entries', () => {
    expect(serializeLogEntries([], opts({ format: 'csv' })).content).toBe('timestamp,container,content')
    expect(serializeLogEntries([], opts({ format: 'json' })).content).toBe('[]')
  })

  it('declares a charset so non-ASCII content survives a spreadsheet import', () => {
    for (const format of LOG_EXPORT_FORMATS) {
      expect(serializeLogEntries([], opts({ format })).mime).toContain('charset=utf-8')
    }
  })
})

describe('previewLogExport', () => {
  it('shows the first two lines of a text export', () => {
    expect(previewLogExport(
      [entry({ content: 'first' }), entry({ content: 'second' }), entry({ content: 'third' })],
      opts({ showTimestamps: false, showPodName: false }),
    )).toEqual(['first', 'second'])
  })

  it('shows one compact object for JSON, not the pretty-printed array', () => {
    const [line, ...rest] = previewLogExport([entry()], opts({ format: 'json' }))
    expect(rest).toEqual([])
    expect(line).toBe('{"timestamp":"2024-01-20T10:30:00.123456789Z","pod":"api-7d9f","container":"app","content":"connection refused"}')
  })

  it('shows the CSV header alongside a row so the columns are legible', () => {
    expect(previewLogExport([entry()], opts({ format: 'csv' }))).toEqual([
      'timestamp,pod,container,content',
      '"2024-01-20T10:30:00.123456789Z","api-7d9f","app","connection refused"',
    ])
  })

  it('previews exactly what the payload contains', () => {
    for (const format of ['txt', 'json', 'csv'] as LogExportFormat[]) {
      const o = opts({ format })
      const preview = previewLogExport([entry()], o)
      const { content } = serializeLogEntries([entry()], o)
      for (const line of preview) {
        // JSON pretty-prints across lines, so compare on the field values instead.
        if (format === 'json') expect(JSON.parse(content)[0]).toEqual(JSON.parse(line))
        else expect(content.split('\n')).toContain(line)
      }
    }
  })

  it('returns nothing when there is nothing to export', () => {
    expect(previewLogExport([], opts())).toEqual([])
  })
})
