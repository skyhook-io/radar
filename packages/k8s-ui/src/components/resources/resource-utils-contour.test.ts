import { describe, expect, it } from 'vitest'
import { getHTTPProxyTLSSecret } from './resource-utils-contour'

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
  it('splits only at the first slash', () => {
    expect(getHTTPProxyTLSSecret(proxy('certs/a/b'))).toEqual({ namespace: 'certs', name: 'a/b' })
  })
  it('returns null without a secret', () => {
    expect(getHTTPProxyTLSSecret(proxy())).toBeNull()
  })
})
