import { describe, expect, it } from 'vitest'
import { vulnerabilitiesPath, type VulnerabilityTraffic } from '../../api/vulnerabilities'
import { TRAFFIC_FILTER, exposedTip, trafficSeenOf, trafficSentence, trafficWindowText } from './parts'

const hubble: VulnerabilityTraffic = {
  source: 'hubble', fromOutside: true, windowSeconds: 900, historyLimited: true,
  workloads: [{ kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'web' }], workloadsTotal: 1,
}
const caretta: VulnerabilityTraffic = { ...hubble, source: 'caretta', fromOutside: false, historyLimited: false }

describe('Traffic on the Exposed mark', () => {
  it('asks for the optional filter only when it is on', () => {
    expect(vulnerabilitiesPath({ view: 'fix', trafficSeen: true })).toBe('/vulnerabilities?view=fix&traffic=1')
    expect(vulnerabilitiesPath({ view: 'fix', trafficSeen: false })).toBe('/vulnerabilities?view=fix')
  })

  it('names the filter for what the source counts', () => {
    expect(TRAFFIC_FILTER.chip(hubble)).toBe('Traffic from outside')
    expect(TRAFFIC_FILTER.chip(caretta)).toBe('Traffic seen')
    expect(TRAFFIC_FILTER.chipTip(caretta)).toContain('in the last 15 minutes')
    expect(TRAFFIC_FILTER.chipTip(caretta)).toContain('may still be reachable')
    expect(TRAFFIC_FILTER.chipTip(hubble)).toContain('from outside the cluster')
  })

  it('states the window', () => {
    expect(trafficWindowText(900)).toBe('15 minutes')
    expect(trafficWindowText(60)).toBe('1 minute')
    expect(trafficWindowText(3600)).toBe('1 hour')
    expect(trafficWindowText(40)).toBe('40 seconds')
  })

  it('says outside the cluster only for a source that can tell', () => {
    expect(trafficSentence(hubble)).toContain('from outside the cluster within the last 15 minutes')
    expect(trafficSentence(hubble)).toContain('SNAT')
    expect(trafficSentence(caretta)).not.toContain('from outside the cluster in')
    expect(trafficSentence(caretta)).toContain('in the last 15 minutes')
    expect(trafficSentence(caretta)).toContain("can't tell callers outside the cluster from nodes")
  })

  it('adds to the Exposed tooltip, and leaves it alone when nothing was seen', () => {
    expect(exposedTip('Exposed via LoadBalancer shop/web', caretta)).toMatch(/^Exposed via LoadBalancer shop\/web\. Live Traffic \(Caretta\) saw/)
    expect(exposedTip('Exposed via LoadBalancer shop/web', undefined)).toBe('Exposed via LoadBalancer shop/web')
  })

  it('never claims absence', () => {
    for (const t of [hubble, caretta]) {
      for (const text of [trafficSentence(t), TRAFFIC_FILTER.chip(t), TRAFFIC_FILTER.chipTip(t)]) {
        expect(text).not.toMatch(/no traffic|not reachable|unreachable/i)
        expect(text).not.toContain('—')
      }
    }
  })

  it('marks any container of a seen workload, and nothing without an answer', () => {
    const seen = trafficSeenOf(hubble)
    expect(seen?.seen.has('Deployment/shop/web')).toBe(true)
    expect(seen?.seen.has('Deployment/shop/api')).toBe(false)
    expect(trafficSeenOf(undefined)).toBeNull()
  })
})
