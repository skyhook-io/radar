import { describe, expect, it } from 'vitest'
import { getRayCronJobTimeZone, getRayCronJobStatus, getRayServiceStatus } from './resource-utils-ray'
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


describe('RayService observed lifecycle', () => {
  const data = (conditions: any[], spec = {}) => ({ metadata: { generation: 3 }, spec, status: { observedGeneration: 3, conditions } })
  it('prioritizes observed rollback over upgrade without hiding readiness in the detail', () => {
    expect(getRayServiceStatus(data(['Ready', 'UpgradeInProgress', 'RollbackInProgress'].map(type => ({ type, status: 'True' })))).text).toBe('RollingBack')
  })
  it('does not treat requested suspension as completed teardown', () => {
    expect(getRayServiceStatus(data([], { suspend: true })).text).toBe('Suspension requested')
    expect(getRayServiceStatus(data([{ type: 'Ready', status: 'True' }], { suspend: true })).text).toBe('Ready')
    expect(getRayServiceStatus(data([{ type: 'Suspending', status: 'True' }], { suspend: false })).text).toBe('Suspending')
    expect(getRayServiceStatus(data([{ type: 'Suspended', status: 'True' }])).text).toBe('Suspended')
  })
  it('retains stale and unknown observations without using deprecated Running as proof', () => {
    expect(getRayServiceStatus(data([{ type: 'Ready', status: 'False', observedGeneration: 2 }])).text).toBe('NotReady')
    for (const [status, label] of [['True', 'Ready'], ['False', 'NotReady'], ['Unknown', 'Unknown']]) {
      const resource = data([{ type: 'Ready', status }])
      resource.status.observedGeneration = 2
      expect(getRayServiceStatus(resource).text).toBe(status === 'True' ? `${label} (stale)` : label)
    }
    for (const condition of [{ type: 'Suspending', status: 'True' }, { type: 'Ready', status: 'False', reason: 'ValidationFailed' }, { type: 'Ready', status: 'False', reason: 'InitializingTimeout' }]) {
      const resource = data([condition], { suspend: false })
      resource.status.observedGeneration = 2
      expect(getRayServiceStatus(resource).text).toBe(condition.reason || condition.type)
    }
    const suspended = data([{ type: 'Suspended', status: 'True' }], { suspend: true })
    suspended.status.observedGeneration = 2
    expect(getRayServiceStatus(suspended).text).toBe('Suspended')
    expect(getRayServiceStatus({ status: { serviceStatus: 'Running', conditions: [{ type: 'Ready', status: 'Unknown' }] } }).text).toBe('Unknown')
    expect(getRayServiceStatus({ status: { serviceStatus: 'Running' } }).text).toBe('Unknown')
  })
})

import { getRayClusterStatus, getRayClusterWorkers, rayClusterWorkerCounts } from './resource-utils-ray'

describe('RayCluster readiness', () => {
  const root = (status: any = {}, spec = {}) => ({ metadata: { generation: 2 }, spec, status: { observedGeneration: 2, conditions: [{ type: 'HeadPodReady', status: 'True' }], ...status } })
  it('distinguishes running from ready and does not bless unready excess workers', () => {
    expect(getRayClusterStatus(root({ readyWorkerReplicas: 1, availableWorkerReplicas: 2, desiredWorkerReplicas: 2 })).level).toBe('degraded')
    expect(getRayClusterStatus(root({ readyWorkerReplicas: 1, availableWorkerReplicas: 2, desiredWorkerReplicas: 1 })).level).toBe('degraded')
    expect(getRayClusterStatus(root({ readyWorkerReplicas: 2, availableWorkerReplicas: 2, desiredWorkerReplicas: 2 })).text).toBe('Ready')
    expect(getRayClusterWorkers(root({ readyWorkerReplicas: 1, availableWorkerReplicas: 2, desiredWorkerReplicas: 2 }))).toBe('1/2')
  })
  it('decodes native omitted zeros only after a witnessed reconciliation', () => {
    expect(rayClusterWorkerCounts(root())).toEqual({ ready: 0, running: 0, desired: 0 })
    expect(getRayClusterStatus(root()).text).toBe('Ready')
    expect(getRayClusterWorkers({})).toBe('?/?')
    expect(getRayClusterStatus(root({ observedGeneration: undefined })).text).toBe('Head ready')
  })
  it('separates suspension intent and historical observations from health', () => {
    expect(getRayClusterStatus(root({}, { suspend: true })).text).toBe('Suspension requested')
    expect(getRayClusterStatus(root({ conditions: [{ type: 'RayClusterSuspended', status: 'True' }] })).text).toBe('Suspended')
    expect(getRayClusterStatus(root({ observedGeneration: 1 })).level).toBe('unknown')
    expect(getRayClusterStatus(root({ state: 'ready', conditions: [] })).level).toBe('unknown')
    expect(getRayClusterStatus(root({ conditions: [{ type: 'RayClusterProvisioned', status: 'True' }] })).level).toBe('neutral')
  })
})
