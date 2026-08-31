import { describe, expect, it } from 'vitest'
import { markDailyUpdateCheckAttempt, utcDay } from './update-report'

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
    expect(markDailyUpdateCheckAttempt(storage, '/api', new Date('2026-08-29T10:00:00Z'))).toBe(true)
    expect(markDailyUpdateCheckAttempt(storage, '/api', new Date('2026-08-29T20:00:00Z'))).toBe(false)
    expect(markDailyUpdateCheckAttempt(storage, '/api', new Date('2026-08-30T10:00:00Z'))).toBe(true)
  })

  it('tracks different API bases independently', () => {
    const storage = memoryStorage()
    const now = new Date('2026-08-29T10:00:00Z')
    expect(markDailyUpdateCheckAttempt(storage, '/api', now)).toBe(true)
    expect(markDailyUpdateCheckAttempt(storage, '/c/cluster-a/api', now)).toBe(true)
  })

  it('replaces an old stored value', () => {
    const storage = memoryStorage()
    const now = new Date('2026-08-29T10:00:00Z')
    storage.values.set('radar-browser-update-check:/api', '{"day":"2026-08-28"}')

    expect(markDailyUpdateCheckAttempt(storage, '/api', now)).toBe(true)
    expect(storage.values.get('radar-browser-update-check:/api')).toBe('2026-08-29')
  })

  it('does not claim an attempt when storage is unavailable', () => {
    const storage = {
      getItem: () => null,
      setItem: () => { throw new Error('blocked') },
    }
    expect(markDailyUpdateCheckAttempt(storage, '/api', new Date('2026-08-29T10:00:00Z'))).toBe(false)
  })
})
