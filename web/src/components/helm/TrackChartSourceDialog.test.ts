import { describe, expect, it } from 'vitest'
import { getSourceIssueCopy, isValidHelmRepositoryURL, sourceStateLabel } from './TrackChartSourceDialog'

describe('chart source recovery presentation', () => {
  it('keeps recorded, configured, and available states distinct', () => {
    expect(sourceStateLabel(false, false, false)).toBe('Not recorded')
    expect(sourceStateLabel(true, false, false)).toBe('Recorded · not configured here')
    expect(sourceStateLabel(true, true, false)).toBe('Recorded and configured · unavailable or version missing')
    expect(sourceStateLabel(true, true, true)).toBe('Recorded · configured · available')
  })

  it('makes ambiguity explicit rather than selecting a source', () => {
    expect(getSourceIssueCopy('ambiguous_source')?.body).toContain('will not guess')
  })

  it('accepts only HTTP repository URLs', () => {
    expect(isValidHelmRepositoryURL('https://charts.example.test')).toBe(true)
    expect(isValidHelmRepositoryURL('oci://registry.example.test/charts')).toBe(false)
  })
})
