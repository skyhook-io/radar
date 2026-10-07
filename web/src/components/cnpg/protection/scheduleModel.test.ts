import { describe, expect, it } from 'vitest'
import { scheduleExpression, simpleSchedule } from './scheduleModel'

describe('simple schedules', () => {
  it('round-trips only the exact common forms without interpreting custom cron', () => {
    for (const value of ['0 30 2 * * *', '0 0 23 * * 0', '0 15 6 31 * *'])
      expect(scheduleExpression(simpleSchedule(value)!)).toBe(value)
    for (const value of [
      '@daily',
      '@every 1h',
      '0 0 0 */2 * 1',
      '30 0 2 * * *',
      '0 0 2 1 * 1',
      '0 60 2 * * *',
      '0 0 24 * * *',
    ])
      expect(simpleSchedule(value)).toBeNull()
  })
})
