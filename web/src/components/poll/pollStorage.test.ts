import { describe, expect, it } from 'vitest'
import { COOLDOWN_MS, MIN_ACTIVE_DAYS, browserAllows, mergePollState, readPollState, recordActiveDay, writePollState } from './pollStorage'

function memoryStorage() {
  const data = new Map<string, string>()
  return { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v), data }
}

const DAY = 24 * 60 * 60 * 1000

describe('browser poll state', () => {
  it('is scoped by API base', () => {
    const s = memoryStorage()
    writePollState(s, '/api', { activeDays: 3 })
    expect(readPollState(s, '/api')?.activeDays).toBe(3)
    expect(readPollState(s, 'https://other/api')?.activeDays).toBe(0)
  })

  it('treats unavailable storage as "never show"', () => {
    const broken = { getItem: () => { throw new Error('denied') }, setItem: () => { throw new Error('denied') } }
    const state = readPollState(broken, '/api')
    expect(state).toBeNull()
    expect(browserAllows(state, 'r', Date.now())).toBe(false)
  })

  it('counts days of use, not launches', () => {
    const morning = new Date(2026, 9, 5, 9).getTime()
    let st = recordActiveDay({ activeDays: 0 }, morning)
    st = recordActiveDay(st, morning + 3 * 60 * 60 * 1000)
    expect(st.activeDays).toBe(1)
    st = recordActiveDay(st, morning + DAY)
    expect(st.activeDays).toBe(2)
  })

  it('never lets a stale tab erase "Don\'t ask again"', () => {
    const s = memoryStorage()
    writePollState(s, '/api', { activeDays: 6, never: true })
    const merged = mergePollState(s, '/api', { submittedRound: 'r', never: false })
    expect(merged?.never).toBe(true)
    expect(readPollState(s, '/api')?.never).toBe(true)
  })

  it('applies never, submitted, cooldown and the days-of-use minimum', () => {
    const now = 10 * COOLDOWN_MS
    const used = { activeDays: MIN_ACTIVE_DAYS }
    expect(browserAllows(used, 'r', now)).toBe(true)
    expect(browserAllows({ ...used, never: true }, 'r', now)).toBe(false)
    expect(browserAllows({ ...used, submittedRound: 'r' }, 'r', now)).toBe(false)
    expect(browserAllows({ ...used, submittedRound: 'old' }, 'r', now)).toBe(true)
    expect(browserAllows({ ...used, lastShownAt: now - COOLDOWN_MS + 1 }, 'r', now)).toBe(false)
    expect(browserAllows({ activeDays: MIN_ACTIVE_DAYS - 1 }, 'r', now)).toBe(false)
  })
})
