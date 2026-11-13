import { describe, expect, it } from 'vitest'
import { countLabel } from './pipeline'

describe('countLabel', () => {
  it('shows an exact count as is', () => {
    expect(countLabel({ count: 12, lowerBound: false }, 'pods')).toEqual({ text: '12', title: '12 pods.' })
  })
  it('marks a lower bound', () => {
    expect(countLabel({ count: 12, lowerBound: true }, 'pods')).toEqual({ text: '≥12', title: 'At least 12 pods: some data was not readable or not evaluated.' })
  })
  it('never shows a lower-bound zero as none', () => {
    expect(countLabel({ count: 0, lowerBound: true }, 'pods')).toEqual({ text: '?', title: 'Unknown: some data was not readable or not evaluated.' })
  })
})
