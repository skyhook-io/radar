import { describe, expect, it } from 'vitest'
import { jobNameError, suggestManualJobName } from './job-name'

describe('suggestManualJobName', () => {
  it.each([
    'report',
    'a'.repeat(52),
    `${'a'.repeat(49)}-bc`,
    `${'a'.repeat(49)}.bc`,
  ])('suggests a valid name of at most 63 characters for %s', (cronJob) => {
    for (let i = 0; i < 20; i++) {
      const name = suggestManualJobName(cronJob)
      expect(name.length).toBeLessThanOrEqual(63)
      expect(jobNameError(name)).toBeNull()
      expect(name).toMatch(/-manual-[a-z0-9]{5}$/)
    }
  })

  it('keeps a short CronJob name whole', () => {
    expect(suggestManualJobName('report')).toMatch(/^report-manual-[a-z0-9]{5}$/)
  })
})

describe('jobNameError', () => {
  it.each([
    ['', 'Enter a Job name.'],
    ['a'.repeat(64), 'At most 63 characters'],
    ['Report', 'lowercase'],
    ['report_1', 'lowercase'],
    ['-report', 'lowercase'],
    ['report-', 'lowercase'],
  ])('rejects %j', (name, reason) => {
    expect(jobNameError(name)).toContain(reason)
  })

  it.each(['report-rerun', 'a'.repeat(63), 'report.v2-1'])('accepts %s', (name) => {
    expect(jobNameError(name)).toBeNull()
  })
})
