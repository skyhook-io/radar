import { describe, expect, it } from 'vitest'
import { REACHABILITY, reachabilitySummary, scopeOf } from './reachability'
import type { VulnerabilityReachability, VulnerabilityReachabilityOutcome } from '../../api/vulnerabilities'

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

  it('says nothing was tested when no probe ran', () => {
    expect(scopeOf(run('not_run'))).toBe(REACHABILITY.nothingRan)
    expect(scopeOf(run('no_answer'))).toBe(REACHABILITY.scope('prio'))
  })

  it('says what was not tested, without dashes', () => {
    const scope = REACHABILITY.scope('prio')
    expect(scope).toContain('inside the cluster')
    expect(scope).toContain('access from the internet were not tested')
    expect(scope).not.toMatch(/[—–]/)
  })
})
