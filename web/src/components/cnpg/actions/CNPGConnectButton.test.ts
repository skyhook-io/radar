import { describe, expect, it } from 'vitest'
import { claimConnectRequest, cnpgConnectParamValue } from './CNPGConnectButton'

describe('Connect requests', () => {
  it('names the Cluster and the surface', () => {
    expect(cnpgConnectParamValue('db', 'pg', 'page')).toBe('db/pg')
    expect(cnpgConnectParamValue('db', 'pg', 'drawer')).toBe('db/pg@drawer')
  })

  it('answers one request once, however many buttons see it', () => {
    expect(claimConnectRequest('entry-1')).toBe(true)
    expect(claimConnectRequest('entry-1')).toBe(false)
    expect(claimConnectRequest('entry-2')).toBe(true)
  })
})
