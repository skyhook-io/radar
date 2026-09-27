import { describe, expect, it } from 'vitest'
import { previousIntegrationOffers, type IntegrationSettingsResponse } from './usePreviousIntegrationSettings'
import type { ConnectionResponse, IntegrationProfile, IntegrationProfiles } from '../components/settings/LocalConnectionSettings'

function response(overrides: Partial<IntegrationProfile> = {}): IntegrationSettingsResponse {
  const profile = { target: { context: 'development' }, state: 'auto', legacy: { url: 'https://backend.example', mode: 'auto' }, ...overrides } as IntegrationProfile
  return { management: 'local', integrationProfiles: { metrics: profile, argocd: profile, cost: profile } as IntegrationProfiles }
}

describe('previous integration eligibility projection', () => {
  it('keeps only eligibility and routing facts, never URLs or credentials', () => {
    expect(previousIntegrationOffers(response(), 'development')).toEqual({ metrics: 'previous', argocd: 'previous', cost: 'previous', explicitCostBackend: true })
  })
  it.each(['operator', 'cloud'] as const)('rejects %s management', management => {
    expect(Object.values(previousIntegrationOffers({ ...response(), management }, 'development')).some(Boolean)).toBe(false)
  })
  it.each(['saved', 'launch', 'target_changed', 'error'] as const)('rejects %s profiles', state => {
    expect(previousIntegrationOffers(response({ state }), 'development').metrics).toBeUndefined()
  })
  it('rejects missing offers, wrong context and errored bundles', () => {
    expect(previousIntegrationOffers(response({ legacy: undefined }), 'development').metrics).toBeUndefined()
    expect(previousIntegrationOffers(response(), 'staging').metrics).toBeUndefined()
    expect(previousIntegrationOffers(response(), '').metrics).toBeUndefined()
    const data = response()
    data.integrationProfiles!.metrics.legacy!.error = 'invalid credentials binding'
    expect(previousIntegrationOffers(data, 'development').metrics).toBeUndefined()
  })
  it('offers another cluster\'s saved settings once no previous settings apply', () => {
    const entry = { integration: 'metrics', binding: 'staging', url: 'https://mimir.example' } as ConnectionResponse['connections'][number]
    const data = response({ legacy: undefined, target: { context: 'development', binding: 'development' } as IntegrationProfile['target'] })
    expect(previousIntegrationOffers(data, 'development', [entry]).metrics).toBe('copy')
    expect(previousIntegrationOffers(data, 'development', [{ ...entry, binding: 'development' }]).metrics).toBeUndefined()
    expect(previousIntegrationOffers(data, 'development', [{ ...entry, url: '' }]).metrics).toBeUndefined()
    expect(previousIntegrationOffers(data, 'development', [{ ...entry, error: 'invalid' }]).metrics).toBeUndefined()
    expect(previousIntegrationOffers(response(), 'development', [entry]).metrics).toBe('previous')
    expect(previousIntegrationOffers(response({ state: 'saved', legacy: undefined }), 'development', [entry]).metrics).toBeUndefined()
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
