import { describe, expect, it } from 'vitest'
import { claimConnectRequest, cnpgConnectParamValue, registerConnectButton } from './CNPGConnectButton'

describe('Connect requests', () => {
  it('names the Cluster and the surface', () => {
    expect(cnpgConnectParamValue('db', 'pg', 'page')).toBe('db/pg')
    expect(cnpgConnectParamValue('db', 'pg', 'drawer')).toBe('db/pg@drawer')
  })

  it('lets one mounted button answer a request, and a remount answer it again', () => {
    const page = Symbol('page')
    const drawer = Symbol('drawer')
    const unregisterPage = registerConnectButton(page)
    const unregisterDrawer = registerConnectButton(drawer)
    expect(claimConnectRequest('entry-1', page)).toBe(true)
    expect(claimConnectRequest('entry-1', drawer)).toBe(false)
    expect(claimConnectRequest('entry-2', drawer)).toBe(true)

    unregisterPage()
    const remounted = Symbol('page again')
    registerConnectButton(remounted)
    unregisterDrawer()
    expect(claimConnectRequest('entry-2', remounted)).toBe(true)
  })
})
