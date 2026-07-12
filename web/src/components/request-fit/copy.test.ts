import { describe, expect, it } from 'vitest'
import { getRequestFitScanSurfaceState, REQUEST_FIT_SCAN_DESCRIPTION, REQUEST_FIT_SCAN_METHODOLOGY } from './RequestFitScanView'

describe('request-fit scan copy', () => {
  it('sets expectations without claiming efficiency or savings', () => {
    const copy = `${REQUEST_FIT_SCAN_DESCRIPTION} ${REQUEST_FIT_SCAN_METHODOLOGY}`
    expect(copy).toContain('increase, reduce, or review')
    expect(copy).toContain('Radar never changes requests')
    expect(copy).toContain('CPU P95 and memory maximum')
    expect(copy.toLowerCase()).not.toContain('efficiency')
    expect(copy.toLowerCase()).not.toContain('savings')
  })

  it('retains a prior snapshot after a failed rerun but treats a first-run failure as fatal', () => {
    expect(getRequestFitScanSurfaceState({ statusLoading: false, hasStatus: true, connected: true, pending: false, hasResult: true, hasError: true, resultState: 'complete' })).toBe('results')
    expect(getRequestFitScanSurfaceState({ statusLoading: false, hasStatus: true, connected: true, pending: false, hasResult: false, hasError: true })).toBe('fatal_error')
  })

  it('does not auto-scan a connected first visit', () => {
    expect(getRequestFitScanSurfaceState({ statusLoading: false, hasStatus: true, connected: true, pending: false, hasResult: false, hasError: false })).toBe('first_run')
  })
})
