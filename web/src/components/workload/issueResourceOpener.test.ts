import { afterEach, describe, expect, it, vi } from 'vitest'
import { recordUsageEvent } from '../../api/usage-data'
import { issueResourceOpener } from './issueResourceOpener'

vi.mock('../../api/usage-data', () => ({ recordUsageEvent: vi.fn() }))

describe('issueResourceOpener', () => {
  afterEach(() => vi.mocked(recordUsageEvent).mockClear())

  it('opens the resource and counts it once', () => {
    const navigate = vi.fn()
    issueResourceOpener(navigate)?.({ kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'cart' })
    expect(navigate).toHaveBeenCalledWith({ kind: 'deployments', namespace: 'shop', name: 'cart', group: 'apps' })
    expect(vi.mocked(recordUsageEvent).mock.calls).toEqual([[{ type: 'ui', name: 'issue_action:open' }]])
  })

  it('leaves links off when the host has nowhere to navigate', () => {
    expect(issueResourceOpener(undefined)).toBeUndefined()
  })
})
