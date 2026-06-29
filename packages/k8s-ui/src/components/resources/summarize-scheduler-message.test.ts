import { describe, it, expect } from 'vitest'
import { summarizeSchedulerMessage } from './resource-utils'

describe('summarizeSchedulerMessage', () => {
  it('strips the "0/N nodes are available:" prefix and the preemption tail', () => {
    const msg =
      '0/5 nodes are available: 2 Insufficient cpu, 3 node(s) had untolerated taint {dedicated: gpu}. ' +
      'preemption: 0/5 nodes are available: 5 No preemption victims found for incoming pod.'
    expect(summarizeSchedulerMessage(msg)).toBe(
      '2 Insufficient cpu, 3 node(s) had untolerated taint {dedicated: gpu}',
    )
  })

  it('returns the clause list without a node prefix unchanged (minus trailing period)', () => {
    expect(summarizeSchedulerMessage('0/2 nodes are available: 2 Insufficient memory.')).toBe(
      '2 Insufficient memory',
    )
  })

  it('handles the bare " preemption:" tail variant', () => {
    expect(
      summarizeSchedulerMessage('0/3 nodes are available: 3 Insufficient cpu preemption: not helpful'),
    ).toBe('3 Insufficient cpu')
  })

  it('returns empty string for empty/undefined input (so detail is omitted, message stays the stable label)', () => {
    expect(summarizeSchedulerMessage('')).toBe('')
    expect(summarizeSchedulerMessage(undefined)).toBe('')
  })
})
