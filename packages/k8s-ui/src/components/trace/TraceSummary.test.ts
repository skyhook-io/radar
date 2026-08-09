import { describe, it, expect } from 'vitest'
import { summarizeTrace } from './TraceSummary'
import type { Trace, Coverage, RouteResult } from './types'

const trace = (o: Partial<Trace>): Trace => ({
  subject: { kind: 'Service', namespace: 'prod', name: 'api' },
  upstreams: [], downstream: [], verdict: 'healthy', brokenAt: -1, ...o,
})
const cov = (o: Partial<Coverage>): Coverage => ({ tested: 0, passed: 0, failed: 0, skipped: 0, ...o })
const route = (o: Partial<RouteResult>): RouteResult => ({ route: '/', outcome: 'verified', ...o })

describe('summarizeTrace - the passive drawer glance', () => {
  it('minimal when not probed (static / config-only): invites the run positively, no tone, no worst line', () => {
    const v = summarizeTrace(trace({}))
    expect(v.kind).toBe('minimal')
    expect(v.ctaLabel).toBe('Run test →')
    // Positive, action-first framing - never the negative "not tested".
    expect(v.headline.toLowerCase()).not.toContain('not tested')
    expect(v.headline.toLowerCase()).toContain('path')
    expect(v.subtitle).toBeTruthy()
    expect(v.tone).toBeUndefined()
    expect(v.worst).toBeUndefined()
  })

  it('a coverage projection with tested===0 is NOT a probed glance - falls through to config-only (never "not yet tested" headline + "See test detail")', () => {
    const v = summarizeTrace(trace({
      verdict: 'healthy',
      headline: 'Configuration only - not yet tested',
      coverage: cov({ tested: 0, skipped: 1 }),
      notTested: [{}] as Trace['notTested'],
    }))
    expect(v.headline).not.toContain('Configuration only')
    expect(v.ctaLabel).toBe('Run test →')
  })

  // The drawer performs NO detection of its own - every config-only (unprobed) trace
  // yields the minimal invite button. Config findings, verdicts, and the diagnosis are
  // owned by the resource's Operational Issues section; the drawer never echoes them.
  it('a config red flag (e.g. a NetworkPolicy would-deny) is NOT surfaced in the drawer - it stays the invite button', () => {
    const v = summarizeTrace(trace({
      verdict: 'degraded',
      downstream: [
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [
          { code: 'netpol:would-deny', severity: 'warning', message: 'A cluster network rule would block traffic to these pods on :80' },
        ] },
      ],
    }))
    expect(v.kind).toBe('minimal')
    expect(v.ctaLabel).toBe('Run test →')
    expect(v.headline).not.toContain('network rule')
    expect(v.tone).toBeUndefined()
  })

  it('partial failure: warning tone + the worst-offender line + Open Reachability CTA', () => {
    const v = summarizeTrace(trace({
      headline: '1 of 2 routes reachable · 1 unreachable',
      coverage: cov({ tested: 2, passed: 1, failed: 1 }),
      routes: [
        route({ route: '/web', outcome: 'verified', confidence: 'real' }),
        route({ route: '/admin', target: 'admin:9090', outcome: 'unreachable', confidence: 'real', evidence: 'connection refused' }),
      ],
    }))
    expect(v.kind).toBe('glance')
    expect(v.tone).toBe('warning')
    expect(v.worst).toEqual({ route: '/admin', target: 'admin:9090', evidence: 'connection refused', failingCount: 1 })
    expect(v.ctaLabel).toBe('Open Reachability →')
  })

  it('all unreachable (none passed): error tone + worst line', () => {
    const v = summarizeTrace(trace({
      headline: 'None of 1 routes reachable',
      coverage: cov({ tested: 1, passed: 0, failed: 1 }),
      routes: [route({ route: '/', target: 'api:80', outcome: 'unreachable', confidence: 'real', evidence: 'connection refused' })],
    }))
    expect(v.tone).toBe('error')
    expect(v.worst?.route).toBe('/')
    // A single failing route is just "the failing route" - not "worst of N".
    expect(v.worst?.failingCount).toBe(1)
  })

  it('multiple failing routes: failingCount reflects all of them (label becomes "Worst of N")', () => {
    const v = summarizeTrace(trace({
      headline: '1 of 3 routes reachable · 2 unreachable',
      coverage: cov({ tested: 3, passed: 1, failed: 2 }),
      routes: [
        route({ route: '/web', outcome: 'verified', confidence: 'real' }),
        route({ route: '/admin', target: 'admin:9090', outcome: 'unreachable', confidence: 'real', evidence: 'connection refused' }),
        route({ route: '/api', target: 'api:80', outcome: 'server-error', confidence: 'real', evidence: 'HTTP 503' }),
      ],
    }))
    expect(v.worst?.failingCount).toBe(2)
    // Worst-first: unreachable outranks server-error.
    expect(v.worst?.route).toBe('/admin')
  })

  it('healthy verified: calm - success tone, NO worst line, "See test detail" CTA', () => {
    const v = summarizeTrace(trace({
      headline: 'All 1 routes reachable',
      coverage: cov({ tested: 1, passed: 1 }),
      routes: [route({ outcome: 'verified', confidence: 'real' })],
    }))
    expect(v.kind).toBe('glance')
    expect(v.tone).toBe('success')
    expect(v.worst).toBeUndefined()
    expect(v.ctaLabel).toBe('See test detail →')
  })

  it('benign scale-to-0 reads amber and is NEVER surfaced as the worst offender', () => {
    const v = summarizeTrace(trace({
      headline: 'No running backends (scaled to 0)',
      coverage: cov({ tested: 1, passed: 0, failed: 1 }),
      routes: [route({ route: '/', outcome: 'unreachable', benign: true })],
    }))
    expect(v.tone).toBe('warning') // benign → amber, not red
    expect(v.worst).toBeUndefined() // not called out as a failure
  })

  it('surfaces the not-tested count from notTested length', () => {
    const v = summarizeTrace(trace({
      headline: 'All 1 tested routes reachable · 2 not tested',
      coverage: cov({ tested: 1, passed: 1, skipped: 2 }),
      routes: [route({ outcome: 'verified', confidence: 'real' })],
      notTested: [{}, {}] as Trace['notTested'],
    }))
    expect(v.notTested).toBe(2)
  })

  it('a broken / degraded / unknown verdict is NOT surfaced in the drawer - no config headline, just the invite button', () => {
    for (const verdict of ['broken', 'degraded', 'unknown'] as const) {
      const v = summarizeTrace(trace({
        verdict,
        headline: 'Configuration only - not yet tested',
        reason: 'RBAC denied: cannot list pods in namespace prod',
        diagnosis: { summary: '0/2 selected pods ready' } as Trace['diagnosis'],
      }))
      expect(v.kind).toBe('minimal')
      expect(v.ctaLabel).toBe('Run test →')
      expect(v.headline).not.toContain('0/2 selected pods ready')
      expect(v.headline).not.toContain('RBAC denied')
      expect(v.headline).not.toContain('Configuration only')
      expect(v.tone).toBeUndefined()
    }
  })

  it('clean static (healthy, not probed) is a minimal entry point - the Ports section already shows the wiring, so the glance must NOT restate "routes to N pods on :80"', () => {
    const v = summarizeTrace(trace({
      verdict: 'healthy',
      headline: 'Configuration only - not yet tested',
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [], config: { ports: [{ port: 80, targetPort: '8080' }], hostnames: ['shop.example.com'] } },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [], meta: { ready: 1, selected: 1 } },
      ],
    }))
    expect(v.kind).toBe('minimal')
    expect(v.headline.toLowerCase()).not.toContain('not tested')
    expect(v.headline).not.toContain('running pod')
    expect(v.headline).not.toContain('shop.example.com')
    expect(v.headline).not.toContain('port 80')
    expect(v.subtitle).toBe('A quick live connection test from where Radar runs.')
    expect(v.ctaLabel).toBe('Run test →')
  })

  it('path-owning subject (Ingress) clean+unprobed: NO wiring restatement - "routes to N pods" both repeats the Rules section and overclaims the unverified front door', () => {
    const v = summarizeTrace(trace({
      subject: { kind: 'Ingress', namespace: 'prod', name: 'wild' },
      verdict: 'healthy',
      headline: 'Configuration only - not yet tested',
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [], config: { ports: [{ port: 80 }], hostnames: ['shop.example.com'] } },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [], meta: { ready: 1, selected: 1 } },
      ],
    }))
    expect(v.kind).toBe('minimal')
    expect(v.headline.toLowerCase()).not.toContain('not tested')
    expect(v.headline.toLowerCase()).toContain('front door')
    // Never asserts the backend wiring as if it were the verified entry.
    expect(v.headline).not.toContain('routes to')
    expect(v.headline).not.toContain('running pod')
    expect(v.headline).not.toContain('shop.example.com')
    expect(v.subtitle).toBe('A quick live probe of the declared host and path.')
    expect(v.tone).toBeUndefined()
    expect(v.ctaLabel).toBe('Run test →')
  })

  it('Service subject clean+unprobed does NOT restate the wiring - the Ports section already covers port→running-pod, so the glance stays a minimal Run entry point', () => {
    const v = summarizeTrace(trace({
      subject: { kind: 'Service', namespace: 'prod', name: 'echo' },
      verdict: 'healthy',
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [], config: { ports: [{ port: 80 }], hostnames: ['shop.example.com'] } },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [], meta: { ready: 1, selected: 1 } },
      ],
    }))
    expect(v.kind).toBe('minimal')
    expect(v.headline.toLowerCase()).not.toContain('not tested')
    expect(v.headline).not.toContain('running pod')
    expect(v.headline).not.toContain('shop.example.com')
  })

  it('0 ready pods (crashing / scaled-to-zero / no selector match) is NOT surfaced - the invite button, not a "no running pods" line', () => {
    const v = summarizeTrace(trace({
      verdict: 'healthy',
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [{ code: 'svc:no-ready-endpoints', severity: 'warning', message: 'no ready endpoints' }], config: { ports: [{ port: 80 }] } },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [], meta: { ready: 0, selected: 2 } },
      ],
    }))
    expect(v.kind).toBe('minimal')
    expect(v.headline).not.toContain('running pod')
    expect(v.headline).not.toContain('endpoints')
    expect(v.ctaLabel).toBe('Run test →')
  })
})
