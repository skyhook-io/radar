import { describe, it, expect } from 'vitest'
import { podReach, podProbeKey } from './podReach'
import type { ProbeResult } from './types'

describe('podProbeKey', () => {
  it('extracts the pod identity from a "<name> port N" probe target', () => {
    expect(podProbeKey('fleet-abc-xyz port 80')).toBe('fleet-abc-xyz') // apiserver path
    expect(podProbeKey('10.244.0.33:8080')).toBe('10.244.0.33') // data path (ip:port)
    expect(podProbeKey('10.244.0.33 port 8080')).toBe('10.244.0.33')
    expect(podProbeKey('pod-1')).toBe('pod-1')
    expect(podProbeKey(undefined)).toBe('')
  })
})

const p = (o: Partial<ProbeResult>): ProbeResult => ({ layer: 'http', target: 'pod-1', vantage: 'local', ok: true, ...o })

describe('podReach', () => {
  it('no live probes → not tested, no vantage', () => {
    expect(podReach([])).toEqual({ tone: 'neutral', text: 'not tested', vantage: '' })
    expect(podReach([p({ skipped: true })])).toEqual({ tone: 'neutral', text: 'not tested', vantage: '' })
  })

  it('healthy 2xx via api proxy → healthy + "via API server"', () => {
    const r = podReach([p({ path: 'apiserver', ok: true, tone: 'healthy', detail: 'HTTP 200' })])
    expect(r.tone).toBe('healthy')
    expect(r.text).toBe('HTTP 200')
    expect(r.vantage).toBe('via API server')
  })

  it('in-cluster direct dial → labels the vantage "real"', () => {
    const r = podReach([p({ vantage: 'in-cluster', path: 'data', ok: true, detail: 'HTTP 200' })])
    expect(r.vantage).toBe('real')
  })

  it('a failure wins over an ok sibling (never mask a bad pod)', () => {
    const r = podReach([
      p({ layer: 'tcp', ok: true }),
      p({ layer: 'http', ok: false, tone: 'unhealthy', error: 'connection refused' }),
    ])
    expect(r.tone).toBe('unhealthy')
    expect(r.text).toBe('connection refused')
  })

  it('a 5xx reads degraded, not unhealthy', () => {
    const r = podReach([p({ layer: 'http', ok: true, tone: 'degraded', detail: 'HTTP 503' })])
    expect(r.tone).toBe('degraded')
    expect(r.text).toBe('HTTP 503')
  })

  it('a reached 404 (ok, tone "reached") is NOT green - neutral, matching the honest headline', () => {
    const r = podReach([p({ layer: 'http', ok: true, tone: 'reached', detail: 'HTTP 404' })])
    expect(r.tone).toBe('neutral')
    expect(r.text).toBe('HTTP 404')
  })

  it('a TCP-only success is "port reachable", not verified - neutral, not green', () => {
    const r = podReach([p({ layer: 'tcp', ok: true, detail: 'connected' })])
    expect(r.tone).toBe('neutral')
  })

  it('a TLS-only success (no HTTP 2xx) is neutral, not green', () => {
    const r = podReach([p({ layer: 'tcp', ok: true }), p({ layer: 'tls', ok: true, detail: 'valid' })])
    expect(r.tone).toBe('neutral')
  })

  it('a real HTTP 2xx stays green (verified path unchanged)', () => {
    const r = podReach([p({ layer: 'tcp', ok: true }), p({ layer: 'http', ok: true, tone: 'healthy', detail: 'HTTP 200' })])
    expect(r.tone).toBe('healthy')
    expect(r.text).toBe('HTTP 200')
  })
})
