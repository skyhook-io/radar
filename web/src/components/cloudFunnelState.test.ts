import { describe, expect, it } from 'vitest'
import { driverConnectAvailable, driverEscapeContent, effectiveConnectionState } from './cloudFunnelState'

describe('effectiveConnectionState', () => {
  it('trusts the render-time state when the server has not contradicted it', () => {
    expect(effectiveConnectionState('connected', false)).toBe('connected')
    expect(effectiveConnectionState('connecting', false)).toBe('connecting')
  })

  it('lets a prepare 503 override a stale connected state', () => {
    expect(effectiveConnectionState('connected', true)).toBe('disconnected')
    expect(driverEscapeContent(effectiveConnectionState('connected', true), false)).toBe('driver-unconnected')
    expect(driverConnectAvailable(effectiveConnectionState('connected', true))).toBe(false)
  })
})

describe('driverEscapeContent', () => {
  it('reads as a preference when the cluster is connected and nothing failed', () => {
    expect(driverEscapeContent('connected', false)).toBe('driver-alt')
  })

  it('reads as a broken in-app path only after a failed attempt on a connected cluster', () => {
    expect(driverEscapeContent('connected', true)).toBe('driver-escape')
  })

  it('names the missing cluster while connecting, regardless of any earlier failure', () => {
    expect(driverEscapeContent('connecting', false)).toBe('driver-unconnected')
    expect(driverEscapeContent('connecting', true)).toBe('driver-unconnected')
  })

  it('names the missing cluster when disconnected, regardless of any earlier failure', () => {
    expect(driverEscapeContent('disconnected', false)).toBe('driver-unconnected')
    expect(driverEscapeContent('disconnected', true)).toBe('driver-unconnected')
  })
})

describe('driverConnectAvailable', () => {
  it('offers the in-app connect only once connected', () => {
    expect(driverConnectAvailable('connected')).toBe(true)
    expect(driverConnectAvailable('connecting')).toBe(false)
    expect(driverConnectAvailable('disconnected')).toBe(false)
  })
})
