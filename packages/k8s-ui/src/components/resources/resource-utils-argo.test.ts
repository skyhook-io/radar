import { describe, expect, it } from 'vitest'
import { getArgoApplicationStatus, isArgoOperationInProgress } from './resource-utils-argo'

describe('getArgoApplicationStatus', () => {
  // A Suspended Argo app is intentionally paused — neutral (sky), matching the
  // backend rollup (mapArgoHealth) + the GitOps badge, so it doesn't read amber
  // on the resource table while reading Idle in Applications.
  it('maps health Suspended to neutral (sky), not degraded', () => {
    const badge = getArgoApplicationStatus({ status: { health: { status: 'Suspended' }, sync: { status: 'Synced' } } })
    expect(badge.level).toBe('neutral')
    expect(badge.text).toBe('Suspended')
  })

  it('still maps a healthy synced app to healthy', () => {
    const badge = getArgoApplicationStatus({ status: { health: { status: 'Healthy' }, sync: { status: 'Synced' } } })
    expect(badge.level).toBe('healthy')
  })

  it('still maps a degraded app to unhealthy', () => {
    const badge = getArgoApplicationStatus({ status: { health: { status: 'Degraded' }, sync: { status: 'Synced' } } })
    expect(badge.level).toBe('unhealthy')
  })

  it('surfaces an operation being terminated', () => {
    const badge = getArgoApplicationStatus({ status: { operationState: { phase: 'Terminating' } } })
    expect(badge.level).toBe('degraded')
    expect(badge.text).toBe('Terminating')
  })
})

describe('isArgoOperationInProgress', () => {
  it.each([
    [{ status: { operationState: { phase: 'Running' } } }, true],
    [{ status: { operationState: { phase: 'Terminating' } } }, true],
    [{ operation: { sync: {} }, status: { operationState: { phase: 'Succeeded' } } }, true],
    [{ status: { operationState: { phase: 'Succeeded' } } }, false],
  ])('maps %o to %s', (app, expected) => {
    expect(isArgoOperationInProgress(app)).toBe(expected)
  })
})
