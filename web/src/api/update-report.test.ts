import { describe, expect, it } from 'vitest'
import { claimDailyUpdateCheck, utcDay } from './update-report'

function memoryStorage() {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
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
})
