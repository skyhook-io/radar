import { describe, it, expect } from 'vitest'
import { parseGoTimeString } from './parse-go-time'

describe('parseGoTimeString', () => {
  it('parses Go default time format (no fractional seconds)', () => {
    expect(parseGoTimeString('2026-07-27 08:27:41 +0000 UTC').toISOString()).toBe(
      '2026-07-27T08:27:41.000Z',
    )
  })

  it('truncates Go nanosecond precision to milliseconds', () => {
    expect(
      parseGoTimeString('2026-07-27 08:27:41.123456789 +0000 UTC').toISOString(),
    ).toBe('2026-07-27T08:27:41.123Z')
  })

  it('pads fractional seconds to 3 digits (Go strips trailing zeros)', () => {
    // Go emits ".1" / ".12" when nanoseconds end in zeros; without padding
    // the constructed ISO string violates the .sss profile and Safari rejects
    // it, re-introducing the bug for certs that happen to expire on these
    // round nanoseconds.
    expect(parseGoTimeString('2026-07-27 08:27:41.1 +0000 UTC').toISOString()).toBe(
      '2026-07-27T08:27:41.100Z',
    )
    expect(parseGoTimeString('2026-07-27 08:27:41.12 +0000 UTC').toISOString()).toBe(
      '2026-07-27T08:27:41.120Z',
    )
    expect(parseGoTimeString('2026-07-27 08:27:41.5 +0000 UTC').toISOString()).toBe(
      '2026-07-27T08:27:41.500Z',
    )
  })

  it('handles non-UTC offsets and ignores tz abbreviation', () => {
    expect(parseGoTimeString('2026-07-27 08:27:41 -0700 PDT').toISOString()).toBe(
      '2026-07-27T15:27:41.000Z',
    )
  })

  it('falls back to native parser for ISO 8601', () => {
    expect(parseGoTimeString('2026-07-27T08:27:41Z').toISOString()).toBe(
      '2026-07-27T08:27:41.000Z',
    )
  })

  it('returns Invalid Date for unparseable input', () => {
    expect(isNaN(parseGoTimeString('not a date').getTime())).toBe(true)
  })

  it('regex normalizes nanosecond inputs without overflow into seconds', () => {
    const d = parseGoTimeString('2026-07-27 08:27:41.999999999 +0000 UTC')
    expect(d.getUTCMilliseconds()).toBe(999)
    expect(d.getUTCSeconds()).toBe(41)
  })
})
