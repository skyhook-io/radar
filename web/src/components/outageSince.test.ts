import { describe, expect, it } from 'vitest'
import { outageSinceLabel } from './outageSince'

describe('outageSinceLabel', () => {
  it('shows only the time for an outage that started today', () => {
    const now = new Date(2026, 9, 30, 16, 0)
    const started = new Date(2026, 9, 30, 14, 2)
    expect(outageSinceLabel(started.toISOString(), now)).toBe(started.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }))
  })

  it('adds the date for an outage that started on an earlier day', () => {
    const now = new Date(2026, 9, 30, 9, 0)
    const started = new Date(2026, 9, 29, 22, 15)
    expect(outageSinceLabel(started.toISOString(), now)).toContain(started.toLocaleDateString([], { month: 'short', day: 'numeric' }))
  })

  it('shows nothing when the server sent no time or an invalid one', () => {
    expect(outageSinceLabel(undefined)).toBeNull()
    expect(outageSinceLabel('not a date')).toBeNull()
  })
})
