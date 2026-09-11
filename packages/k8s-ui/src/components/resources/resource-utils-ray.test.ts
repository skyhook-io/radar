import { describe, expect, it } from 'vitest'
import { getRayCronJobTimeZone, getRayCronJobStatus } from './resource-utils-ray'
import { getCellFilterValue } from './resource-utils'

describe('getRayCronJobTimeZone', () => {
  it('shows the declared zone', () => {
    expect(getRayCronJobTimeZone({ spec: { schedule: '0 2 * * *', timeZone: 'America/New_York' } }))
      .toBe('America/New_York')
  })

  it('does not present an undeclared zone as UTC', () => {
    // KubeRay reads the schedule in the operator pod's local zone when timeZone
    // is unset, so the object cannot say what the cron expression means.
    expect(getRayCronJobTimeZone({ spec: { schedule: '0 2 * * *' } })).toBe('Operator local')
  })
})

describe('getRayCronJobStatus', () => {
  it('reports only what spec.suspend says, which is all the CRD carries', () => {
    // RayCronJobStatus holds lastScheduleTime and nothing else — there is no
    // controller-reported phase for this badge to reflect.
    expect(getRayCronJobStatus({ spec: { suspend: true } }).text).toBe('Suspended')
    expect(getRayCronJobStatus({ spec: {} }).text).toBe('Active')
  })
})

describe('getCellFilterValue for raycronjobs timeZone', () => {
  it('offers the same value the cell renders, so undeclared rows stay filterable', () => {
    // The generic fallback reads spec.timeZone and yields '' when unset, which
    // would hide those rows from a filter whose column shows them a value.
    expect(getCellFilterValue({ spec: { schedule: '0 2 * * *' } }, 'timeZone', 'raycronjobs'))
      .toBe('Operator local')
    expect(getCellFilterValue({ spec: { timeZone: 'Europe/Berlin' } }, 'timeZone', 'raycronjobs'))
      .toBe('Europe/Berlin')
  })
})
