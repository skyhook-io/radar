import { describe, it, expect } from 'vitest'
import { problemRows } from './ReachabilityView'
import { buildOrigins } from './reachOrigins'
import { buildGraph } from './reachGraphModel'
import type { Trace, RouteResult } from './types'

const base = (): Trace => ({
  subject: { kind: 'Service', name: 'shop', namespace: 'store' },
  verdict: 'degraded', brokenAt: -1,
  upstreams: [{ resource: { kind: 'HTTPRoute', name: 'shop', namespace: 'store' }, edge: 'httproute->service', findings: [] }],
  downstream: [
    { resource: { kind: 'Service', name: 'shop', namespace: 'store' }, edge: 'service', findings: [] },
    { resource: { kind: 'Pods', name: '', namespace: 'store' }, edge: 'service->pods', findings: [], config: { pods: [{ name: 'shop-1', ready: true, ip: '10.0.0.1' }], podTotal: 1 } },
  ],
  routes: [],
} as never)

/** The ids the graph actually draws, which is what a header row may address. */
const nodesOf = (t: Trace, r?: RouteResult) => {
  const origins = buildOrigins(t)
  // Pass every origin, as the app does - the capsules ARE nodes a header row
  // may address.
  const g = buildGraph({ trace: t, route: r ?? ({ route: 'shop', target: 'shop:80', outcome: 'reached' } as never), origin: origins.find((o) => o.id === 'apiserver')!, origins })
  return new Set(g.nodes.map((n) => n.id))
}

describe('the header problem list', () => {
  it('drops a coverage-class diagnosis - the headline already says it', () => {
    const t = base()
    t.diagnosis = { class: 'coverage', summary: 'reachable via API server - the real-traffic path was not confirmed from here' } as never
    expect(problemRows(t, buildOrigins(t), nodesOf(t))).toHaveLength(0)
  })

  it('renders the severity the detector assigned, never inventing critical', () => {
    const t = base()
    t.diagnosis = { summary: 'a network rule would block traffic', severity: 'warning' } as never
    expect(problemRows(t, buildOrigins(t), nodesOf(t))[0].severity).toBe('warning')
    t.diagnosis = { summary: 'container is crashlooping', severity: 'critical' } as never
    expect(problemRows(t, buildOrigins(t), nodesOf(t))[0].severity).toBe('critical')
  })

  it('ranks critical above warning', () => {
    const t = base()
    t.diagnosis = { summary: 'container is crashlooping', severity: 'critical' } as never
    t.entryProblems = [{ resource: { kind: 'HTTPRoute', name: 'shop', namespace: 'store' }, summary: 'Not attached: no listener matches its hosts', severity: 'warning' }] as never
    expect(problemRows(t, buildOrigins(t), nodesOf(t)).map((r) => r.severity)).toEqual(['critical', 'warning'])
  })

  it('is INDEPENDENT of what is selected - the header is the resource', () => {
    const t = base()
    const r: RouteResult = {
      route: 'shop', target: 'shop:80', outcome: 'reached', confidence: 'indirect',
      byVantage: [{ vantage: 'in-cluster', path: 'data', source: 'probe-job', outcome: 'unreachable', confidence: 'real', evidence: 'connection refused' }],
    } as never
    t.routes = [r]
    const rows = problemRows(t, buildOrigins(t), nodesOf(t, r))
    const ptr = rows.find((x) => x.scope === 'SEEN FROM')
    expect(ptr, 'a fault a vantage saw must be listed however the reader is browsing').toBeTruthy()
    expect(ptr!.nodeId).toBe('origin:incluster')
    // the same rows regardless of which vantage the reader has open
    expect(problemRows(t, buildOrigins(t), nodesOf(t, r))).toEqual(rows)
  })

  it('never addresses a node the graph does not draw', () => {
    const t = base()
    // A Pod culprit: the graph aggregates Pods, so an individual Pod node does not exist.
    t.diagnosis = { summary: 'container is crashlooping', severity: 'critical', culpritResource: { kind: 'Pod', name: 'shop-1', namespace: 'store' } } as never
    const known = nodesOf(t)
    const row = problemRows(t, buildOrigins(t), known)[0]
    expect(row.nodeId === '' || known.has(row.nodeId)).toBe(true)
  })

  it('says nothing when nothing is wrong', () => {
    const t = base()
    expect(problemRows(t, buildOrigins(t), nodesOf(t))).toHaveLength(0)
  })
})

// One entry can carry several findings, and one origin several failed routes,
// so a key built from the subject alone collides. React silently mis-renders or
// drops rows on duplicate keys - which would hide a fault in the list whose
// whole job is to surface faults.
describe('every row can be told apart', () => {
  it('gives two findings on ONE entry distinct keys, and keeps both rows', () => {
    const t = base()
    const ref = { kind: 'HTTPRoute', name: 'shop', namespace: 'store' }
    t.entryProblems = [
      { resource: ref, summary: 'Not attached: no listener matches its hosts', severity: 'warning' },
      { resource: ref, summary: 'Backend service port 8080 does not exist', severity: 'critical' },
    ] as never
    const rows = problemRows(t, buildOrigins(t), nodesOf(t))
    expect(rows).toHaveLength(2)
    expect(new Set(rows.map((r) => r.key)).size).toBe(2)
  })

  it('keeps keys unique across the whole list, whatever the mix', () => {
    const t = base()
    t.diagnosis = { summary: 'container is crashlooping', severity: 'critical' } as never
    t.entryProblems = [
      { resource: { kind: 'HTTPRoute', name: 'shop', namespace: 'store' }, summary: 'Not attached', severity: 'warning' },
      { resource: { kind: 'HTTPRoute', name: 'shop', namespace: 'store' }, summary: 'Backend port missing', severity: 'warning' },
    ] as never
    const rows = problemRows(t, buildOrigins(t), nodesOf(t))
    expect(rows.length).toBeGreaterThan(1)
    expect(new Set(rows.map((r) => r.key)).size).toBe(rows.length)
  })
})
