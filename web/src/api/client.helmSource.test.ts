import { describe, expect, it } from 'vitest'
import { HELM_REPOSITORY_ADD_INVALIDATION_KEYS } from './client'

describe('Helm repository source recovery invalidation', () => {
  it('refreshes repositories, exact candidates, and upgrade state after add', () => {
    expect(HELM_REPOSITORY_ADD_INVALIDATION_KEYS).toEqual([
      ['helm-repositories'],
      ['helm-source-candidates'],
      ['helm-upgrade-info'],
      ['helm-batch-upgrade-info'],
    ])
  })
})
