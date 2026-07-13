import { describe, expect, it } from 'vitest'
import {
  getRightsizingScanSurfaceState,
  RIGHTSIZING_SCAN_DESCRIPTION,
  RIGHTSIZING_SCAN_METHODOLOGY,
} from './RightsizingScanView'

describe('rightsizing scan copy', () => {
  it('sets expectations without claiming efficiency or savings', () => {
    const copy = `${RIGHTSIZING_SCAN_DESCRIPTION} ${RIGHTSIZING_SCAN_METHODOLOGY}`
    expect(copy).toContain('CPU and memory requests')
    expect(copy).toContain('increase, reduce, or review')
    expect(copy).toContain('Radar never changes them')
    expect(copy).toContain('CPU P95 and memory maximum')
    expect(copy).toContain('Memory reductions require verifiable restart history')
    expect(copy.toLowerCase()).not.toContain('efficiency')
    expect(copy.toLowerCase()).not.toContain('savings')
  })

  it('retains a prior snapshot after a failed rerun but treats a first-run failure as fatal', () => {
    expect(
      getRightsizingScanSurfaceState({
        statusLoading: false,
        hasStatus: true,
        connected: true,
        pending: false,
        hasResult: true,
        hasError: true,
        resultState: 'complete',
      }),
    ).toBe('results')
    expect(
      getRightsizingScanSurfaceState({
        statusLoading: false,
        hasStatus: true,
        connected: true,
        pending: false,
        hasResult: false,
        hasError: true,
      }),
    ).toBe('fatal_error')
  })

  it('does not auto-scan a connected first visit', () => {
    expect(
      getRightsizingScanSurfaceState({
        statusLoading: false,
        hasStatus: true,
        connected: true,
        pending: false,
        hasResult: false,
        hasError: false,
      }),
    ).toBe('first_run')
  })
})
