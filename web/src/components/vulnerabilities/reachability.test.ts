import { describe, expect, it } from 'vitest'
import { REACHABILITY, reachabilitySummary, scopeOf } from './reachability'
import { normalizeReachability, reachabilityRequestKeys, type VulnerabilityReachability, type VulnerabilityReachabilityOutcome } from '../../api/vulnerabilities'

function run(...outcomes: VulnerabilityReachabilityOutcome[]): VulnerabilityReachability {
  return {
    allowed: true,
    namespace: 'prio',
    maxProbes: 5,
    routes: [{ route: { via: 'LoadBalancer', namespace: 'prio', name: 'lb-app' }, checks: outcomes.map((outcome, i) => ({ route: `r${i}`, target: `lb-app:${80 + i}`, outcome })) }],
  }
}

describe('reachabilitySummary', () => {
  it('claims an answer only when every check answered', () => {
    expect(reachabilitySummary(run('answered', 'answered'))).toEqual({ text: 'Answers inside the cluster', outcome: 'answered' })
    expect(reachabilitySummary(run('answered', 'not_run')).text).toBe('1 of 2 answer inside the cluster')
    expect(reachabilitySummary(run('answered', 'no_answer')).outcome).toBe('degraded')
  })

  it('names a failed connection and a run that tested nothing', () => {
    expect(reachabilitySummary(run('no_answer')).text).toBe('No answer inside the cluster')
    expect(reachabilitySummary(run('degraded')).text).toBe('Answers inside the cluster, with a problem')
    expect(reachabilitySummary(run('answered', 'degraded')).text).toBe('Answers inside the cluster, with a problem')
    expect(reachabilitySummary(run('degraded', 'no_answer'))).toEqual({ text: '1 of 2 answer inside the cluster', outcome: 'degraded' })
    expect(reachabilitySummary(run('not_run')).text).toBe('Reachability not checked')
    expect(reachabilitySummary({ allowed: true, namespace: 'prio', maxProbes: 5, routes: [] }).outcome).toBe('not_run')
  })

  it('never reads all clear past a route it could not probe', () => {
    const r = run('answered')
    r.routes!.push({ route: { via: 'TCPRoute', namespace: 'prio', name: 'tcp' }, note: "Radar's in-cluster probe doesn't test a TCPRoute yet.", checks: [] })
    expect(reachabilitySummary(r)).toEqual({ text: 'Answers inside the cluster, 1 route not checked', outcome: 'degraded' })
    r.routes![0].checks[0].outcome = 'not_run'
    expect(reachabilitySummary(r).text).toBe('Reachability not checked')
  })

  it('counts the routes the check left out', () => {
    expect(reachabilitySummary({ ...run('answered'), routesTotal: 4 })).toEqual({ text: 'Answers inside the cluster, 3 routes not checked', outcome: 'degraded' })
  })

  it('claims an answer in its scope line only when one came', () => {
    expect(scopeOf(run('not_run'))).toBe(REACHABILITY.nothingRan)
    expect(scopeOf(run('no_answer'))).toBe(REACHABILITY.scopeNoAnswer('prio'))
    expect(scopeOf(run('no_answer'))).not.toContain('proves')
    expect(scopeOf(run('degraded', 'no_answer'))).toBe(REACHABILITY.scope('prio'))
  })

  it('says what was not tested, without dashes', () => {
    const scope = REACHABILITY.scope('prio')
    expect(scope).toContain('inside the cluster')
    expect(scope).toContain('access from the internet were not tested')
    expect(scope).not.toMatch(/[—–]/)
  })
})

describe('reachabilityRequestKeys', () => {
  it('keys the confirmed requests as the server does', () => {
    const req = { protocol: 'http', scheme: 'http', path: '/' }
    const plan: VulnerabilityReachability = {
      allowed: true, namespace: 'prio', maxProbes: 5,
      routes: [{ route: { via: 'LoadBalancer', namespace: 'prio', name: 'web' }, checks: [
        { route: 'a', target: 'web:80', inClusterRequest: req },
        { route: 'a', target: 'web:80', inClusterRequest: req },
        { route: 'c', target: 'idle:80', benign: true, inClusterRequest: req },
        { route: 'd' },
      ] }],
    }
    expect(reachabilityRequestKeys(plan)).toEqual(['a\x1fweb:80\x1fhttp\x1fhttp\x1f\x1f/'])
  })
})

describe('normalizeReachability', () => {
  it('reads a route with null checks as one with none', () => {
    const raw = { allowed: true, namespace: 'prio', maxProbes: 5, routes: [{ route: { via: 'Ingress', namespace: 'prio', name: 'old' }, note: 'stale', checks: null }] } as unknown as VulnerabilityReachability
    const r = normalizeReachability(raw)
    expect(r.routes![0].checks).toEqual([])
    expect(reachabilitySummary(r).text).toBe('Reachability not checked')
  })
})
