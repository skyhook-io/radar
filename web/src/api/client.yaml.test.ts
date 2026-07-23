import { describe, expect, it } from 'vitest'
import { ApplyResourceError, formatApplyResourceError } from './client'

describe('formatApplyResourceError', () => {
  it('reports resources persisted before a multi-document failure', () => {
    expect(
      formatApplyResourceError(
        {
          error: 'document 3: admission denied the Service',
          failedIndex: 2,
          total: 4,
          results: [
            { kind: 'Namespace', namespace: '', name: 'checkout', created: true },
            { kind: 'Deployment', namespace: 'checkout', name: 'api', created: true },
          ],
        },
        422,
      ),
    ).toBe(
      '2 of 4 resources were applied before document 3 failed. Applied: Namespace checkout, Deployment checkout/api. admission denied the Service',
    )
  })

  it('preserves ordinary apply errors', () => {
    expect(formatApplyResourceError({ error: 'field is invalid' }, 422)).toBe('field is invalid')
  })

  it('preserves structured partial-apply results for recovery', () => {
    const result = { kind: 'Namespace', namespace: '', name: 'checkout', created: true }
    const error = new ApplyResourceError(
      {
        error: 'document 2: admission denied',
        failedIndex: 1,
        total: 3,
        results: [result],
      },
      422,
    )

    expect(error.message).toContain('1 of 3 resource was applied')
    expect(error.appliedResults).toEqual([result])
    expect(error.failedIndex).toBe(1)
    expect(error.total).toBe(3)
  })
})
