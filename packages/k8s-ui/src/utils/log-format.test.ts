import { describe, expect, it } from 'vitest'

import { formatEntriesForCopy, type CopyableLogEntry } from './log-format'

const ESC = '\u001b'

const entry = (over: Partial<CopyableLogEntry> = {}): CopyableLogEntry => ({
  timestamp: '2024-01-20T10:30:00.123456789Z',
  content: 'connection refused',
  container: 'app',
  pod: 'api-7d9f',
  ...over,
})

describe('formatEntriesForCopy', () => {
  it('emits the raw RFC3339 timestamp rather than any display format', () => {
    const text = formatEntriesForCopy([entry()], { showTimestamps: true, showPodName: false })
    expect(text).toBe('2024-01-20T10:30:00.123456789Z connection refused')
  })

  it('omits timestamps when the viewer has them hidden', () => {
    const text = formatEntriesForCopy([entry()], { showTimestamps: false, showPodName: false })
    expect(text).toBe('connection refused')
  })

  it('attributes pod and container when pod names are shown', () => {
    const text = formatEntriesForCopy([entry()], { showTimestamps: false, showPodName: true })
    expect(text).toBe('[api-7d9f/app] connection refused')
  })

  it('falls back to the pod alone when no container is known', () => {
    const text = formatEntriesForCopy([entry({ container: undefined })], { showTimestamps: false, showPodName: true })
    expect(text).toBe('[api-7d9f] connection refused')
  })

  it('drops pod attribution for entries with no pod, even when the toggle is on', () => {
    const text = formatEntriesForCopy([entry({ pod: undefined })], { showTimestamps: false, showPodName: true })
    expect(text).toBe('connection refused')
  })

  it('skips an empty timestamp instead of emitting a leading space', () => {
    const text = formatEntriesForCopy([entry({ timestamp: '' })], { showTimestamps: true, showPodName: false })
    expect(text).toBe('connection refused')
  })

  it('strips ANSI escapes from the content', () => {
    const text = formatEntriesForCopy(
      [entry({ content: `${ESC}[31mconnection refused${ESC}[0m` })],
      { showTimestamps: false, showPodName: false },
    )
    expect(text).toBe('connection refused')
  })

  it('preserves raw JSON content rather than any collapsed rendering', () => {
    const json = '{"level":"error","msg":"connection refused","stack":"a\\nb"}'
    const text = formatEntriesForCopy([entry({ content: json })], { showTimestamps: false, showPodName: false })
    expect(text).toBe(json)
  })

  it('joins entries with newlines and returns empty for no entries', () => {
    const text = formatEntriesForCopy(
      [entry({ content: 'first' }), entry({ content: 'second' })],
      { showTimestamps: false, showPodName: false },
    )
    expect(text).toBe('first\nsecond')
    expect(formatEntriesForCopy([], { showTimestamps: true, showPodName: true })).toBe('')
  })
})
