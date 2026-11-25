import { describe, expect, it } from 'vitest'
import { REACHABILITY, reachabilitySummary } from './reachability'
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
    expect(reachabilitySummary(run('not_run')).text).toBe('Reachability not checked')
    expect(reachabilitySummary({ allowed: true, namespace: 'prio', maxProbes: 5, routes: [] }).outcome).toBe('not_run')
  })

  it('says what was not tested, without dashes', () => {
    const scope = REACHABILITY.scope('prio')
    expect(scope).toContain('inside the cluster')
    expect(scope).toContain('access from the internet were not tested')
    expect(scope).not.toMatch(/[—–]/)
  })
})
