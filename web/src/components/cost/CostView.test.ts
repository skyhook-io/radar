import { describe, expect, it } from 'vitest'
import { costUnavailableMessage, resourceKindForCostWorkload } from './CostView'

describe('resourceKindForCostWorkload', () => {
  it('does not link standalone aggregate rows as pods', () => {
    expect(resourceKindForCostWorkload('standalone')).toBeNull()
  })

  it('keeps static pod rows linkable to their mirror pod', () => {
    expect(resourceKindForCostWorkload('staticpod')).toBe('Pod')
  })
})

describe('costUnavailableMessage', () => {
  it('gives actionable Kubecost source and authentication guidance', () => {
    expect(costUnavailableMessage('source_unavailable')).toContain('Settings → Cost')
    expect(costUnavailableMessage('authentication_error')).toContain('API key')
    expect(costUnavailableMessage('configuration_mismatch')).toContain('not valid for this cluster')
    expect(costUnavailableMessage('deployment_configuration_error')).toContain('Helm cost values')
  })
})
