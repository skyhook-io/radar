import { describe, expect, it } from 'vitest'
import { getSecretStoreProviderType, getSecretStoreProviderKey } from './resource-utils-eso'

// The provider label answers "which backend holds these secrets?", so it has to
// track the upstream `spec.provider` field names rather than plausible ones.

describe('getSecretStoreProviderType', () => {
  it('names the AWS product rather than assuming Secrets Manager', () => {
    // One `aws` key addresses three services; `service` is required and is the
    // only thing that distinguishes them.
    const forService = (service: string) =>
      getSecretStoreProviderType({ spec: { provider: { aws: { service, region: 'us-east-1' } } } })

    expect(forService('SecretsManager')).toBe('AWS Secrets Manager')
    expect(forService('ParameterStore')).toBe('AWS Parameter Store')
    expect(forService('CertificateManager')).toBe('AWS Certificate Manager')
  })

  it('falls back to the vendor when an AWS store declares no service', () => {
    // Invalid per the CRD, but naming a product we cannot confirm is worse than
    // naming only the vendor.
    expect(getSecretStoreProviderType({ spec: { provider: { aws: {} } } })).toBe('AWS')
  })

  it('recognises Bitwarden under its real field name', () => {
    expect(getSecretStoreProviderType({ spec: { provider: { bitwardensecretsmanager: {} } } }))
      .toBe('Bitwarden Secrets Manager')
  })

  it('labels providers added after the map was first written', () => {
    expect(getSecretStoreProviderType({ spec: { provider: { openBao: {} } } })).toBe('OpenBao')
    expect(getSecretStoreProviderType({ spec: { provider: { conjur: {} } } })).toBe('CyberArk Conjur')
  })

  it('keeps v1beta1-only providers, which v1 clusters never send', () => {
    expect(getSecretStoreProviderType({ spec: { provider: { device42: {} } } })).toBe('Device42')
  })

  it('shows the raw key rather than Unknown for an unrecognised provider', () => {
    expect(getSecretStoreProviderType({ spec: { provider: { somefutureprovider: {} } } }))
      .toBe('somefutureprovider')
  })

  it('reports nothing when there is no provider at all', () => {
    expect(getSecretStoreProviderType({ spec: {} })).toBe('Unknown')
  })
})

describe('getSecretStoreProviderKey', () => {
  it('stays at the vendor level, which is what the badge colours by', () => {
    const key = (service: string) =>
      getSecretStoreProviderKey({ spec: { provider: { aws: { service } } } })
    expect(key('SecretsManager')).toBe('aws')
    expect(key('ParameterStore')).toBe('aws')
  })
})
