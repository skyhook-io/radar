import { describe, expect, it } from 'vitest'
import { jobNameError, manualJobIndexRoom, suggestManualJobName } from './job-name'

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

// An Indexed Job's Pods get the hostname <job>-<index>, a DNS label of at most 63.
describe('Indexed templates', () => {
  it.each([
    [undefined, 0],
    [{ completions: 5 }, 0],
    [{ completionMode: 'NonIndexed', completions: 5 }, 0],
    [{ completionMode: 'Indexed' }, 2],
    [{ completionMode: 'Indexed', completions: 1 }, 2],
    [{ completionMode: 'Indexed', completions: 10 }, 2],
    [{ completionMode: 'Indexed', completions: 11 }, 3],
    [{ completionMode: 'Indexed', completions: 1000 }, 4],
  ])('index room for %j is %i', (spec, room) => {
    expect(manualJobIndexRoom(spec)).toBe(room)
  })

  it.each([
    ['a'.repeat(52), 1000],
    ['report.v2', 3],
  ])('suggests a name whose widest Pod hostname fits for %s', (cronJob, completions) => {
    const name = suggestManualJobName(cronJob, manualJobIndexRoom({ completionMode: 'Indexed', completions }))
    const hostname = `${name}-${completions - 1}`
    expect(hostname.length).toBeLessThanOrEqual(63)
    expect(hostname).toMatch(/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/)
    expect(jobNameError(name, true)).toBeNull()
  })

  it('rejects dots', () => {
    expect(jobNameError('report.rerun', true)).toContain("'.'")
    expect(jobNameError('report.rerun')).toBeNull()
  })
})
