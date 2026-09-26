import { describe, expect, it } from 'vitest'
import { previousIntegrationOffers, type IntegrationSettingsResponse } from './usePreviousIntegrationSettings'
import type { IntegrationProfile, IntegrationProfiles } from '../components/settings/LocalConnectionSettings'

function response(overrides: Partial<IntegrationProfile> = {}): IntegrationSettingsResponse {
  const profile = { target: { context: 'development' }, state: 'auto', legacy: { url: 'https://backend.example', mode: 'auto' }, ...overrides } as IntegrationProfile
  return { management: 'local', integrationProfiles: { metrics: profile, argocd: profile, cost: profile } as IntegrationProfiles }
}

describe('previous integration eligibility projection', () => {
  it('keeps only eligibility and routing facts, never URLs or credentials', () => {
    expect(previousIntegrationOffers(response(), 'development')).toEqual({ metrics: true, argocd: true, cost: true, explicitCostBackend: true })
  })
  it.each(['operator', 'cloud'] as const)('rejects %s management', management => {
    expect(Object.values(previousIntegrationOffers({ ...response(), management }, 'development')).some(Boolean)).toBe(false)
  })
  it.each(['saved', 'launch', 'target_changed', 'error'] as const)('rejects %s profiles', state => {
    expect(previousIntegrationOffers(response({ state }), 'development').metrics).toBe(false)
  })
  it('rejects missing offers, wrong context and errored bundles', () => {
    expect(previousIntegrationOffers(response({ legacy: undefined }), 'development').metrics).toBe(false)
    expect(previousIntegrationOffers(response(), 'staging').metrics).toBe(false)
    expect(previousIntegrationOffers(response(), '').metrics).toBe(false)
    const data = response()
    data.integrationProfiles!.metrics.legacy!.error = 'invalid credentials binding'
    expect(previousIntegrationOffers(data, 'development').metrics).toBe(false)
  })
  it.each([
    ['auto', 'https://cost.example', true],
    ['kubecost', 'https://cost.example', true],
    ['prometheus', 'https://cost.example', false],
    ['auto', '', false],
    ['kubecost', '', false],
  ])('identifies the independent Cost alternative for %s / %s', (mode, url, expected) => {
    const data = response()
    Object.assign(data.integrationProfiles!.cost.legacy!, { mode, url })
    expect(previousIntegrationOffers(data, 'development').explicitCostBackend).toBe(expected)
  })
})
