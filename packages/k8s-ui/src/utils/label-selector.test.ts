import { describe, expect, it } from 'vitest'
import { matchesLabelSelector, unsupportedSelectorOperator } from './label-selector'

describe('matchesLabelSelector', () => {
  const labels = { role: 'control-plane', os: 'linux' }
  it('matches labels and the four operators', () => {
    expect(matchesLabelSelector({ matchLabels: { os: 'linux' } }, labels)).toBe(true)
    expect(matchesLabelSelector({ matchLabels: { os: 'windows' } }, labels)).toBe(false)
    expect(matchesLabelSelector({ matchExpressions: [{ key: 'role', operator: 'In', values: ['control-plane', 'etcd'] }] }, labels)).toBe(true)
    expect(matchesLabelSelector({ matchExpressions: [{ key: 'role', operator: 'In', values: ['worker'] }] }, labels)).toBe(false)
    expect(matchesLabelSelector({ matchExpressions: [{ key: 'zone', operator: 'NotIn', values: ['a'] }] }, labels)).toBe(true)
    expect(matchesLabelSelector({ matchExpressions: [{ key: 'os', operator: 'NotIn', values: ['linux'] }] }, labels)).toBe(false)
    expect(matchesLabelSelector({ matchExpressions: [{ key: 'role', operator: 'Exists' }] }, labels)).toBe(true)
    expect(matchesLabelSelector({ matchExpressions: [{ key: 'role', operator: 'DoesNotExist' }] }, labels)).toBe(false)
    expect(matchesLabelSelector({}, labels)).toBe(true)
  })
  it('names the first operator it cannot evaluate', () => {
    expect(unsupportedSelectorOperator({ matchExpressions: [{ key: 'a', operator: 'Exists' }, { key: 'a', operator: 'Gt' }] })).toBe('Gt')
    expect(unsupportedSelectorOperator({ matchExpressions: [{ key: 'a', operator: 'In', values: ['x'] }] })).toBeUndefined()
  })
})
