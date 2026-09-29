import { describe, expect, it } from 'vitest'
import { formatBytes, lsnDistance, parseLsn } from './lsn'

describe('parseLsn', () => {
  it('reads the high and low halves', () => {
    expect(parseLsn('0/21000000')).toBe(0x21000000)
    expect(parseLsn('16/B374D848')).toBe(0x16 * 2 ** 32 + 0xb374d848)
  })
  it('is unknown, not zero, when it does not parse', () => {
    expect(parseLsn(undefined)).toBeUndefined()
    expect(parseLsn('')).toBeUndefined()
    expect(parseLsn('garbage')).toBeUndefined()
  })
})

describe('lsnDistance', () => {
  it('measures the backlog in bytes', () => {
    expect(lsnDistance('0/21000100', '0/21000000')).toBe(256)
    expect(lsnDistance('1/0', 'FFFFFFFF/0')).toBe(0)
  })
  it('is unknown when either side is', () => {
    expect(lsnDistance('0/1', undefined)).toBeUndefined()
  })
})

describe('formatBytes', () => {
  it('formats and keeps unknown visible', () => {
    expect(formatBytes(undefined)).toBe('—')
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(16 * 1024 * 1024)).toBe('16 MiB')
  })
})
