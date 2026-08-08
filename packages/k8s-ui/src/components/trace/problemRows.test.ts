import { describe, it, expect } from 'vitest'
import { problemRows } from './ReachabilityView'
import { buildOrigins } from './reachOrigins'
import type { Trace, RouteResult } from './types'

const base = (): Trace => ({
  subject: { kind: 'Service', name: 'shop', namespace: 'store' },
  verdict: 'degraded', brokenAt: -1,
  upstreams: [{ resource: { kind: 'HTTPRoute', name: 'shop', namespace: 'store' }, edge: 'httproute->service', findings: [] }],
  downstream: [{ resource: { kind: 'Service', name: 'shop', namespace: 'store' }, edge: 'service', findings: [] }],
  routes: [],
} as never)

describe('the header problem list', () => {
  it('drops a coverage-class diagnosis - the headline already says it', () => {
    const t = base()
    t.diagnosis = { class: 'coverage', summary: 'reachable via API server - the real-traffic path was not confirmed from here' } as never
    expect(problemRows(t, undefined, buildOrigins(t), 'apiserver')).toHaveLength(0)
  })

  it('keeps a fault-class diagnosis and ranks it above warnings', () => {
    const t = base()
    t.diagnosis = { summary: 'container is crashlooping', culpritResource: { kind: 'Pod', name: 'shop-1', namespace: 'store' } } as never
    t.entryProblems = [{ resource: { kind: 'HTTPRoute', name: 'shop', namespace: 'store' }, summary: 'Not attached: no listener matches its hosts', severity: 'warning' }] as never
    const rows = problemRows(t, undefined, buildOrigins(t), 'apiserver')
    expect(rows.map((r) => r.severity)).toEqual(['critical', 'warning'])
    expect(rows[0].nodeId).toBe('n:Pod/store/shop-1')
    expect(rows[1].nodeId).toBe('n:HTTPRoute/store/shop')
  })

  it('points at a fault only ANOTHER vantage saw, addressing its capsule', () => {
    const t = base()
    const r: RouteResult = {
      route: 'shop', target: 'shop:80', outcome: 'reached', confidence: 'indirect',
      byVantage: [
        { vantage: 'in-cluster', path: 'data', source: 'probe-job', outcome: 'unreachable', confidence: 'real', evidence: 'connection refused' },
      ],
    } as never
    t.routes = [r]
    const rows = problemRows(t, r, buildOrigins(t), 'apiserver')
    const ptr = rows.find((x) => x.scope === 'ANOTHER VANTAGE')
    expect(ptr, 'a fault another vantage saw must not hide behind a click').toBeTruthy()
    expect(ptr!.nodeId).toBe('origin:incluster')
  })

  it('says nothing when nothing is wrong', () => {
    expect(problemRows(base(), undefined, buildOrigins(base()), 'apiserver')).toHaveLength(0)
  })
})
