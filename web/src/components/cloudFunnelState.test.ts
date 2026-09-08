import { describe, expect, it } from 'vitest'
import { driverConnectUnavailableNote, driverEscapeContent } from './cloudFunnelState'

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

describe('driverConnectUnavailableNote', () => {
  it('offers the in-app connect once connected', () => {
    expect(driverConnectUnavailableNote('connected')).toBeNull()
  })

  it('explains the wait while connecting', () => {
    expect(driverConnectUnavailableNote('connecting')).toMatch(/still connecting/)
  })

  it('explains the missing cluster when disconnected', () => {
    expect(driverConnectUnavailableNote('disconnected')).toMatch(/isn't connected to a cluster/)
  })
})
