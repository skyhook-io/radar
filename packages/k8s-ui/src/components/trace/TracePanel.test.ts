import { describe, it, expect } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { routeOutcomeRank, coverageBannerTone, inClusterOutcome, inClusterEligible, RequestIndicator, completedRequestMode } from './TracePanel'
import type { ProbeResult, RouteResult, Coverage, Trace } from './types'

describe('protocol-honest probe copy', () => {
  it('labels a TCP-only test without inventing an HTTP request', () => {
    const html = renderToStaticMarkup(createElement(RequestIndicator, {
      requestMode: 'tcp',
      path: '/healthz',
      onApplyProbePath: () => {},
    }))
    expect(html).toContain('TCP connection')
    expect(html).not.toContain('GET')
    expect(html).not.toContain('Edit the request path')
  })

  it('does not claim an application request completed when every applicable probe skipped', () => {
    const html = renderToStaticMarkup(createElement(RequestIndicator, {
      requestMode: 'none',
      path: '/healthz',
      onApplyProbePath: () => {},
    }))
    expect(html).toContain('no application request completed from here')
    expect(html).not.toContain('GET')
    expect(html).not.toContain('TCP connection')
  })

  it('labels TCP routes as TCP in the test matrix', () => {
    const trace: Trace = {
      subject: { kind: 'Service', namespace: 'ns', name: 'database' },
      upstreams: [],
      verdict: 'unknown',
      brokenAt: -1,
      downstream: [{
        resource: { kind: 'Service', namespace: 'ns', name: 'database' },
        edge: 'entry:Service',
        findings: [],
        probes: [
          { layer: 'http', target: 'port 6379', port: 6379, vantage: 'local', path: 'apiserver', ok: false, skipped: true, reason: 'non-HTTP' },
          { layer: 'http', target: 'port 5432', port: 5432, vantage: 'local', path: 'apiserver', ok: false, skipped: true, reason: 'non-HTTP' },
        ],
      }],
      routes: [
        { route: 'database', target: 'database:6379', targetNamespace: 'ns', outcome: 'not-tested', inClusterRequest: { protocol: 'tcp' } },
        { route: 'database', target: 'database:5432', targetNamespace: 'ns', outcome: 'not-tested', inClusterRequest: { protocol: 'tcp' } },
      ],
    }
    expect(completedRequestMode(trace)).toBe('none')

    trace.downstream[0].probes?.push({
      layer: 'tcp',
      target: 'database:6379',
      port: 6379,
      vantage: 'in-cluster',
      path: 'data',
      ok: true,
    })
    expect(completedRequestMode(trace)).toBe('tcp')
  })

  it('distinguishes a preliminary TCP connection from a completed HTTPS request', () => {
    const trace: Trace = {
      subject: { kind: 'Service', namespace: 'ns', name: 'secure-api' },
      upstreams: [],
      verdict: 'unknown',
      brokenAt: -1,
      downstream: [{
        resource: { kind: 'Service', namespace: 'ns', name: 'secure-api' },
        edge: 'entry:Service',
        findings: [],
        probes: [{
          layer: 'tcp',
          target: 'secure-api:443',
          port: 443,
          vantage: 'in-cluster',
          path: 'data',
          ok: true,
        }, {
          layer: 'http',
          target: 'port 443',
          port: 443,
          vantage: 'in-cluster',
          path: 'apiserver',
          ok: false,
          skipped: true,
          reason: 'the API-server proxy cannot test HTTPS',
        }],
      }],
      routes: [{
        route: 'secure-api',
        target: 'secure-api:443',
        targetNamespace: 'ns',
        outcome: 'reached',
        inClusterRequest: { protocol: 'https', scheme: 'https', path: '/' },
      }],
    }

    expect(completedRequestMode(trace)).toBe('none')
    const indicator = renderToStaticMarkup(createElement(RequestIndicator, {
      requestMode: completedRequestMode(trace),
    }))
    expect(indicator).toContain('no application request completed')
  })
})

describe('in-cluster test - eligibility + outcome', () => {
  const p = (o: Partial<ProbeResult>): ProbeResult => ({ layer: 'http', target: 't', vantage: 'in-cluster', ok: true, ...o })
  it('eligible: indirect or failed routes, NOT real-verified or benign', () => {
    expect(inClusterEligible({ route: '/', outcome: 'verified', confidence: 'indirect' })).toBe(true)
    expect(inClusterEligible({ route: '/', outcome: 'unreachable', confidence: 'real' })).toBe(true)
    expect(inClusterEligible({ route: '/', outcome: 'verified', confidence: 'real' })).toBe(false)
    expect(inClusterEligible({ route: '/', outcome: 'unreachable', benign: true })).toBe(false)
  })
  it('outcome: a real-dataplane HTTP 2xx is verified+green (the payoff)', () => {
    expect(inClusterOutcome([p({ layer: 'tcp', ok: true }), p({ layer: 'http', ok: true, tone: 'healthy' })]))
      .toEqual({ label: 'verified', tone: 'healthy', severity: 'success' })
  })
  it('outcome: a failed HTTP is unreachable+red; tcp-only is port reachable', () => {
    expect(inClusterOutcome([p({ layer: 'http', ok: false })]).severity).toBe('error')
    expect(inClusterOutcome([p({ layer: 'tcp', ok: true })])).toEqual({ label: 'port reachable', tone: 'healthy', severity: 'success' })
  })
})

describe('coverageBannerTone - the single honest banner tone', () => {
  const cov = (o: Partial<Coverage>): Coverage => ({ tested: 0, passed: 0, failed: 0, skipped: 0, ...o })
  const real = (outcome: RouteResult['outcome']): RouteResult => ({ route: '/', outcome, confidence: 'real' })
  const indirect = (outcome: RouteResult['outcome']): RouteResult => ({ route: '/', outcome, confidence: 'indirect' })

  it('green ONLY on a real-traffic pass', () => {
    expect(coverageBannerTone(cov({ tested: 1, passed: 1 }), [real('verified')])).toBe('success')
  })
  it('indirect-only all-pass is info, never green', () => {
    expect(coverageBannerTone(cov({ tested: 1, passed: 1 }), [indirect('verified')])).toBe('info')
  })
  it('a real pass beside a SKIPPED (untested) sibling route is NOT green - partial coverage', () => {
    // Mirrors the backend CoverageVerdict (skipped>0 → unknown) and the full-tab banner.
    expect(coverageBannerTone(cov({ tested: 1, passed: 1, skipped: 1 }), [real('verified')])).toBe('info')
  })
  it('a real pass beside a proxy-only (indirect) unreachable sibling is NOT green', () => {
    expect(coverageBannerTone(cov({ tested: 2, passed: 1 }), [real('verified'), indirect('unreachable')])).toBe('info')
  })
  it('zero-tested is info, never green or red', () => {
    expect(coverageBannerTone(cov({ tested: 0, skipped: 2 }), [])).toBe('info')
  })
  it('partial (some passed, some failed) is warning', () => {
    expect(coverageBannerTone(cov({ tested: 2, passed: 1, failed: 1 }), [real('verified'), real('unreachable')])).toBe('warning')
  })
  it('none reachable is error', () => {
    expect(coverageBannerTone(cov({ tested: 1, passed: 0, failed: 1 }), [real('unreachable')])).toBe('error')
  })
  it('all server-error (reached, no pass) is warning not red - matches the degraded verdict', () => {
    expect(coverageBannerTone(cov({ tested: 1, passed: 0, failed: 1 }), [real('server-error')])).toBe('warning')
  })
  it('all-failed-but-BENIGN (intentional scale-to-0) is warning, never red', () => {
    const scaledZero: RouteResult = { route: '/', outcome: 'unreachable', confidence: 'indirect', benign: true }
    expect(coverageBannerTone(cov({ tested: 1, passed: 0, failed: 1 }), [scaledZero])).toBe('warning')
  })
  it('a genuine unreachable (not benign) stays red even next to a benign one', () => {
    const benign: RouteResult = { route: '/a', outcome: 'unreachable', benign: true }
    const realDown: RouteResult = { route: '/b', outcome: 'unreachable', confidence: 'real' }
    expect(coverageBannerTone(cov({ tested: 2, passed: 0, failed: 2 }), [benign, realDown])).toBe('error')
  })
})

describe('routeOutcomeRank - failures first', () => {
  it('sorts unreachable before reached before verified', () => {
    expect(routeOutcomeRank('unreachable')).toBeLessThan(routeOutcomeRank('reached'))
    expect(routeOutcomeRank('reached')).toBeLessThan(routeOutcomeRank('verified'))
  })
})
