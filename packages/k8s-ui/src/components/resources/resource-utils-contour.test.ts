import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { getContourSecretReference, getHTTPProxyTLSSecret } from './resource-utils-contour'

const proxy = (secretName?: string) => ({
  metadata: { namespace: 'shop' },
  spec: { virtualhost: { fqdn: 'shop.example.com', tls: secretName === undefined ? {} : { secretName } } },
})

describe('getHTTPProxyTLSSecret', () => {
  it('resolves a bare name in the proxy namespace', () => {
    expect(getHTTPProxyTLSSecret(proxy('cert'))).toEqual({ namespace: 'shop', name: 'cert' })
  })
  it('resolves a delegated namespace/name', () => {
    expect(getHTTPProxyTLSSecret(proxy('certs/wildcard'))).toEqual({ namespace: 'certs', name: 'wildcard' })
  })
  it('defaults an empty namespace to the proxy namespace', () => {
    expect(getHTTPProxyTLSSecret(proxy('/cert'))).toEqual({ namespace: 'shop', name: 'cert' })
  })
  it('rejects names that cannot identify a Kubernetes Secret', () => {
    expect(getHTTPProxyTLSSecret(proxy('certs/a/b'))).toBeNull()
  })
  it('returns null without a secret', () => {
    expect(getHTTPProxyTLSSecret(proxy())).toBeNull()
  })
})

interface ReferenceCase {
  case: string
  raw: string
  defaultNamespace: string
  valid: boolean
  namespace?: string
  name?: string
}
const referenceCases: ReferenceCase[] = JSON.parse(readFileSync(new URL('../../../../../pkg/configrefs/testdata/contour_secret_refs.json', import.meta.url), 'utf8'))

describe('shared Go/TS Contour reference contract', () => {
  it.each(referenceCases)('$case', (tc) => {
    const expected = tc.valid ? { namespace: tc.namespace, name: tc.name } : undefined
    expect(getContourSecretReference(tc.raw, tc.defaultNamespace)).toEqual(expected)
    expect(getHTTPProxyTLSSecret({ ...proxy(tc.raw), metadata: { namespace: tc.defaultNamespace } })).toEqual(expected ?? null)
  })
})
