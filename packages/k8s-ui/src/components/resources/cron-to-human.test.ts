import { describe, expect, it } from 'vitest'
import { cronToHuman } from './resource-utils'

describe('cronToHuman', () => {
  it.each([
    // The reported bug: step-minute with a wildcard hour was caught by the
    // "Every hour at :MM" branch before the interval branch.
    ['*/5 * * * *', 'Every 5 minutes'],
    ['*/15 * * * *', 'Every 15 minutes'],
    ['*/30 * * * *', 'Every 30 minutes'],
    ['*/1 * * * *', 'Every minute'],
    // Literal minute must still read as "Every hour at :MM" (not regressed).
    ['30 * * * *', 'Every hour at :30'],
    ['0 * * * *', 'Every hour at :00'],
    ['5 * * * *', 'Every hour at :05'],
    // Every minute.
    ['* * * * *', 'Every minute'],
    // Step-hour (the #952 fix) must still work.
    ['0 */6 * * *', 'Every 6 hours'],
    ['0 */1 * * *', 'Every hour'],
    // Daily patterns.
    ['0 0 * * *', 'Daily at midnight'],
    ['0 9 * * *', 'Daily at 9:00'],
    // Weekdays — only when hour:minute are literal.
    ['0 9 * * 1-5', 'Weekdays at 9:00'],
    ['0 9 * * MON-FRI', 'Weekdays at 9:00'],
    ['30 14 * * 1-5', 'Weekdays at 14:30'],
    // Constrained step-minute must NOT claim an unconstrained interval — these run
    // only in a window, so we fall back to the raw cron rather than mislead.
    ['*/5 9 * * *', '*/5 9 * * *'],
    ['*/5 * * * 1-5', '*/5 * * * 1-5'],
    ['*/5 9 * * 1-5', '*/5 9 * * 1-5'],
    ['*/1 9 * * *', '*/1 9 * * *'],
    // Falls back to the raw expression for shapes we don't humanize.
    ['15 14 1 * *', '15 14 1 * *'],
    ['*/5', '*/5'],
    ['', '-'],
  ])('humanizes %s -> %s', (cron, expected) => {
    expect(cronToHuman(cron)).toBe(expected)
  })
})
