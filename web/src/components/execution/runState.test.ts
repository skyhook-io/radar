import { describe, expect, it } from 'vitest'
import { runStateLabel } from './runState'

describe('runStateLabel', () => {
  it('reads a deleting Job with no active Pods as Deleting, not Pending', () => {
    expect(runStateLabel({ phase: 'Pending', deleting: true })).toBe('Deleting')
  })

  it('keeps the phase for other runs, marking deletion', () => {
    expect(runStateLabel({ phase: 'Running', deleting: true })).toBe('Running · deleting')
    expect(runStateLabel({ phase: 'Pending' })).toBe('Pending')
    expect(runStateLabel({ phase: 'Succeeded', deleting: true }, () => 'OK')).toBe('OK · deleting')
  })
})
