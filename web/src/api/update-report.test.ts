import { describe, expect, it } from 'vitest'
import { claimDailyUpdateCheck, utcDay } from './update-report'

function memoryStorage() {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
    values,
  }
}

describe('daily browser update checks', () => {
  it('uses UTC days', () => {
    expect(utcDay(new Date('2026-08-29T23:59:59-07:00'))).toBe('2026-08-30')
  })

  it('attempts once per API base and UTC day', () => {
    const storage = memoryStorage()
    expect(claimDailyUpdateCheck(storage, '/api', new Date('2026-08-29T10:00:00Z'))).toBe('2026-08-29')
    expect(claimDailyUpdateCheck(storage, '/api', new Date('2026-08-29T20:00:00Z'))).toBeNull()
    expect(claimDailyUpdateCheck(storage, '/api', new Date('2026-08-30T10:00:00Z'))).toBe('2026-08-30')
  })

  it('tracks different API bases independently', () => {
    const storage = memoryStorage()
    const now = new Date('2026-08-29T10:00:00Z')
    expect(claimDailyUpdateCheck(storage, '/api', now)).toBe('2026-08-29')
    expect(claimDailyUpdateCheck(storage, '/c/cluster-a/api', now)).toBe('2026-08-29')
  })

  it('tracks installations behind the same API base independently', () => {
    const storage = memoryStorage()
    const now = new Date('2026-08-29T10:00:00Z')
    expect(claimDailyUpdateCheck(storage, '/api', now, '1700000000')).toBe('2026-08-29')
    expect(claimDailyUpdateCheck(storage, '/api', now, '1800000000')).toBe('2026-08-29')
  })

  it('does not duplicate a check when installation scope becomes available', () => {
    const storage = memoryStorage()
    const now = new Date('2026-08-29T10:00:00Z')
    expect(claimDailyUpdateCheck(storage, '/api', now)).toBe('2026-08-29')
    expect(claimDailyUpdateCheck(storage, '/api', now, '1700000000')).toBeNull()
  })

  it('does not duplicate a scoped check when installation scope is temporarily unavailable', () => {
    const storage = memoryStorage()
    const now = new Date('2026-08-29T10:00:00Z')
    expect(claimDailyUpdateCheck(storage, '/api', now, '1700000000')).toBe('2026-08-29')
    expect(claimDailyUpdateCheck(storage, '/api', now)).toBeNull()
  })

  it('replaces a malformed stored claim', () => {
    const storage = memoryStorage()
    const now = new Date('2026-08-29T10:00:00Z')
    storage.values.set('radar-browser-update-check:/api', 'not-json')

    expect(claimDailyUpdateCheck(storage, '/api', now, '1700000000')).toBe('2026-08-29')
    expect(storage.values.get('radar-browser-update-check:/api')).toBe(
      JSON.stringify({ day: '2026-08-29', installScope: '1700000000' }),
    )
  })
})
