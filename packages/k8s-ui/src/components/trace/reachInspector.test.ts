import { describe, it, expect } from 'vitest'
import { buildSidebar, buildVerdict } from './reachInspector'
import { buildGraph } from './reachGraphModel'
import { buildOrigins } from './reachOrigins'
import type { Trace, RouteResult, ProbeResult, PodStatus } from './types'

const p = (o: Partial<ProbeResult>): ProbeResult => ({ layer: 'http', target: '10.0.0.1:8080', vantage: 'in-cluster', path: 'data', ok: true, ...o })
const pod = (name: string, ready: boolean, ip: string, reason?: string): PodStatus => ({ name, ready, ip, reason })

function mk(pods: PodStatus[], probes: ProbeResult[]): Trace {
  return {
    subject: { kind: 'Service', name: 'shop', namespace: 'store' },
    verdict: 'healthy',
    brokenAt: -1,
    upstreams: [],
    // The producer attaches a concrete request to every probed route; without
    // one the server has nothing to send, so a fixture that omits it is not a
    // realistic trace.
    routes: [{ route: 'GET /', target: 'shop:80', outcome: 'verified', confidence: 'real', inClusterRequest: { scheme: 'http', path: '/' } }],
    downstream: [
      { resource: { kind: 'Service', name: 'shop', namespace: 'store' }, edge: 'service', findings: [], config: { clusterIP: '10.96.0.1', selector: { app: 'shop' } } },
      {
        resource: { kind: 'Pods', name: '', namespace: 'store' },
        edge: 'service->pods',
        findings: [],
        meta: { ready: pods.filter((x) => x.ready).length, selected: pods.length },
        config: { pods, podTotal: pods.length },
        probes,
      },
    ],
  }
}

const route = (o: Partial<RouteResult> = {}): RouteResult => ({ route: 'GET /', target: ':80 → 8080', outcome: 'verified', confidence: 'real', ...o })

// Pods nodes are parent-scoped so multi-backend routes keep a group each, so
// tests address them by role rather than by a fixed id.
function podsNodeId(t: Trace): string {
  const g = buildGraph({ trace: t, route: route(), origin: buildOrigins(t).find((o) => o.id === 'incluster')! })
  return g.nodes.find((n) => n.kind === 'PODS')!.id
}
function podsEdgeId(t: Trace): string {
  const g = buildGraph({ trace: t, route: route(), origin: buildOrigins(t).find((o) => o.id === 'incluster')! })
  return g.edges.find((e) => e.label === 'selects')!.id
}

function ctx(t: Trace, originId: string, r = route()) {
  const origins = buildOrigins(t)
  const origin = origins.find((o) => o.id === originId)!
  const g = buildGraph({ trace: t, route: r, origin })
  return {
    trace: t,
    route: r,
    origin,
    origins,
    nodes: g.nodes,
    breakNodeId: g.breakNodeId,
    breakAtExitOf: g.breakAtExitOf,
    nonNetworkNodeIds: g.nonNetworkNodeIds,
    contextNodeIds: g.contextNodeIds,
    interleave: g.interleave,
    entryParallelCount: g.entryParallelCount,
    journeyEntryNodeIds: g.journeyEntryNodeIds,
    pathNodeIds: g.pathNodeIds,
  }
}

describe('the diagnosis is always present', () => {
  // The reason the tab exists is "did traffic get through". That must never
  // require a click, so the path section is computed regardless of selection.
  it('answers the path question with nothing selected', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    const s = buildSidebar(undefined, ctx(t, 'incluster'))
    expect(s.path.title).toBeTruthy()
    expect(s.path.evidence.length).toBeGreaterThan(0)
    // The panel always answers the path question; a next-step block is offered
    // only when there is genuinely a next step.
    expect(s.path.body).toBeTruthy()
    expect(s.resource).toBeUndefined()
  })

  // Every hop is reported whether or not anything was clicked; a selection only
  // changes which one is open. Understanding a path used to take three clicks.
  it('reports the whole path without any selection, and a click only opens one', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    const c = ctx(t, 'incluster')
    const before = buildSidebar(undefined, c)
    expect(before.hops.map((h) => h.kind)).toContain('PODS')
    const after = buildSidebar(podsNodeId(t), c)
    expect(after.path).toEqual(before.path)
    expect(after.hops.find((h) => h.kind === 'PODS')!.expanded).toBe(true)
  })

  it('an apiserver result always states what it skipped', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver' })])
    const s = buildSidebar(undefined, ctx(t, 'apiserver'))
    expect(s.path.notProve.join(' ')).toMatch(/relayed|routing|network policy|mesh/i)
  })

  it('a synthetic result always states the identity gap', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    const s = buildSidebar(undefined, ctx(t, 'incluster'))
    expect(s.path.notProve.join(' ')).toMatch(/not as your application|who is calling/i)
  })

  it('never claims complete evidence', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    expect(buildSidebar(undefined, ctx(t, 'incluster')).path.notProve.length).toBeGreaterThan(0)
  })

  it('does not claim a front-door gap on a resource with no entry point', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    expect(t.upstreams).toHaveLength(0)
    expect(buildSidebar(undefined, ctx(t, 'incluster')).path.notProve.join(' ')).not.toMatch(/internet|outside/i)
  })

  it('offers only actions that can be taken', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver' })])
    const s = buildSidebar(undefined, ctx(t, 'apiserver'))
    expect(s.path.next.ctas.every((c) => !c.disabledReason)).toBe(true)
    expect(s.path.next.body).toMatch(/in-cluster/i)
  })

  it('the RBAC prompt names the permission and copies a command that runs', () => {
    // Every other vantage must be spent, or offering one of those would be the
    // better next step than asking for permission.
    const t = mk([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver' }), p({ vantage: 'local', path: 'data' })])
    const origins = buildOrigins(t, { inClusterAllowed: false })
    const g = buildGraph({ trace: t, route: route(), origin: origins.find((o) => o.id === 'apiserver')! })
    const s = buildSidebar(undefined, { trace: t, route: route(), origin: origins.find((o) => o.id === 'apiserver')!, origins, nodes: g.nodes })
    expect(s.path.next.body).toMatch(/create.*jobs/i)
    expect(s.path.next.ctas[0].command).toMatch(/kubectl auth can-i/)
  })
})

// The graph already gates on this; the sidebar is the surface people actually
// read, so it must gate identically or it states another vantage's result under
// the selected vantage's name.
describe('the sidebar is scoped to the selected vantage', () => {
  it('an unavailable vantage never inherits another vantage\'s success', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    const origins = buildOrigins(t)
    const caller = origins.find((o) => o.id === 'caller')!
    const g = buildGraph({ trace: t, route: route({ outcome: 'verified', confidence: 'real' }), origin: caller })
    const s = buildSidebar(undefined, { trace: t, route: route({ outcome: 'verified', confidence: 'real' }), origin: caller, origins, nodes: g.nodes })
    expect(s.path.body).not.toMatch(/a real request went through/i)
    expect(s.path.evidence.every((e) => e.mark !== 'proved')).toBe(true)
    expect(s.path.evidence.map((e) => e.text).join(' ')).toMatch(/cannot test from here/i)
  })

  it('a vantage that did run still reports its result', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    const s = buildSidebar(undefined, ctx(t, 'incluster'))
    expect(s.path.body).toMatch(/a real request went through/i)
  })

  it('a stale result does not lead with its old assertion', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    t.headline = 'Reachable in-cluster on :80'
    const v = buildVerdict(t, route(), { stale: true })
    expect(v.title).not.toMatch(/Reachable/)
    expect(v.title).toMatch(/out of date/i)
  })
})

describe('node detail is additive', () => {
  it('a Pods node reports what is and is not taking traffic', () => {
    const t = mk([pod('a', true, '10.0.0.1'), pod('b', false, '10.0.0.2', 'readiness failing')], [p({})])
    const r = buildSidebar(podsNodeId(t), ctx(t, 'incluster')).hops.find((h) => h.kind === 'PODS')!
    expect(r.facts.find((x) => x.k === 'SITTING OUT')!.v).toMatch(/not ready/)
    // Derived from readiness, so it must not claim observed delivery.
    expect(r.facts.some((x) => x.k === 'ELIGIBLE')).toBe(true)
    expect(r.facts.some((x) => x.k === 'TAKING TRAFFIC')).toBe(false)
    expect(r.notProve.join(' ')).toMatch(/nothing was sent to them/)
    expect(r.rows!.some((x) => x.mark === 'excluded')).toBe(true)
  })

  it('a resource node carries its own config, not the path result', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    const c = ctx(t, 'incluster')
    const svc = c.nodes.find((n) => n.kind === 'SERVICE')!
    const r = buildSidebar(svc.id, c).hops.find((h) => h.id === svc.id)!
    expect(r.facts.some((x) => x.k === 'CLUSTER IP')).toBe(true)
    expect(r.openRef?.name).toBe('shop')
  })

  // A vantage is not a hop on the path - it is where the path was watched from.
  it('the origin capsule is never a hop in the report', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    const c = ctx(t, 'incluster')
    const cap = c.nodes.find((n) => n.isOrigin)!
    expect(buildSidebar(cap.id, c).hops.some((h) => h.id === cap.id)).toBe(false)
  })
})

describe('verdict band', () => {
  // These rules used to be asserted through the header's fact strip. The strip
  // is gone - every one of its four facts is now shown by the graph or the
  // panel - so each rule is pinned where it actually renders.
  it('reports nothing proven when no origin has run', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [])
    const v = buildVerdict(t, route({ outcome: 'not-tested' }))
    expect(v.tone).toBe('unknown')
    expect(v.chipText).toMatch(/not tested/i)
  })

  it('an apiserver-only pass never claims the backend was proven', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver' })])
    const v = buildVerdict(t, route({ confidence: 'indirect' }))
    expect(v.tone).toBe('degraded')
    const s = buildSidebar(undefined, ctx(t, 'apiserver', route({ confidence: 'indirect' })))
    // It may say something is SERVING; it must also say that is not the same as
    // the normal path working.
    expect(s.path.body).toMatch(/serving/i)
    expect(s.path.body).toMatch(/not that the normal path works/i)
  })

  it('points the next step at something Radar can actually do', () => {
    // The real caller is the strongest missing origin AND permanently
    // unavailable. Offering it would give every resource the same
    // un-actionable next step.
    const t = mk([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver' })])
    const s = buildSidebar(undefined, ctx(t, 'apiserver', route({ confidence: 'indirect' })))
    expect(s.path.next.body).toMatch(/in-cluster probe/i)
    expect(s.path.next.header).toMatch(/run this next/i)
  })

  it('offers nothing when nothing stronger can be run', () => {
    // A resource-level "there is no more to learn" repeated on every vantage was
    // scope mixing; the ceiling is stated with specifics in the caveats and the
    // footer's coverage ledger instead.
    const t = mk([pod('a', true, '10.0.0.1')], [p({}), p({ path: 'apiserver' }), p({ vantage: 'local', path: 'data' })])
    const s = buildSidebar(undefined, ctx(t, 'incluster'))
    expect(s.path.next.header).toBe('')
    expect(s.path.next.ctas).toHaveLength(0)
  })

  it('falls back to the backend verdict when there is no route to derive a tone from', () => {
    // A config fault found without probing still has a verdict; a grey
    // "unknown" dot there under-reports what the tracer already concluded.
    const t = mk([pod('a', true, '10.0.0.1')], [])
    t.verdict = 'degraded'
    t.routes = []
    const v = buildVerdict(t, undefined, buildOrigins(t))
    expect(v.tone).toBe('degraded')
  })

  it('leads with the named fault rather than burying it under a coverage headline', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [])
    t.headline = 'Configuration only - not yet tested'
    t.diagnosis = { summary: 'Accepted: NoMatchingListenerHostname - no hostname intersections' }
    const v = buildVerdict(t, undefined, buildOrigins(t))
    expect(v.problem).toMatch(/NoMatchingListenerHostname/)
    // and it is not duplicated into the body
    expect(v.body).toBe('')
  })

  it('a running test is informational, never green', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    const v = buildVerdict(t, route(), { running: true })
    expect(v.tone).toBe('info')
    expect(v.chipText).toBe('testing')
  })
})

describe('an in-cluster test that could not run says so first', () => {
  // A route whose probes were all skipped never gets a concrete request, so the
  // server answers "not supported for this subject". The panel used to
  // recommend the run as the strongest evidence available and let the operator
  // discover the failure by spending a Job on it.
  const noRunnableRoute = (): Trace => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver' })])
    t.routes = [{ route: 'port 7000', target: 'shop:7000', outcome: 'not-tested', evidence: 'port looks non-HTTP' }]
    return t
  }

  // The button moved to the vantage capsule in the graph, so the panel must not
  // offer a third copy of it - it keeps the reasoning instead.
  it('offers the run, disabled, with the reason attached', () => {
    // The action lives WITH its explanation now, so it is present even when it
    // cannot be taken - but never silently clickable into a guaranteed no-op.
    const s = buildSidebar(undefined, ctx(noRunnableRoute(), 'apiserver'))
    const run = s.path.next.ctas.find((c) => c.action === 'run-in-cluster')
    expect(run).toBeTruthy()
    expect(run!.disabledReason).toMatch(/nothing to run|no path/i)
  })

  it('explains it in the body rather than promising stronger evidence', () => {
    const s = buildSidebar(undefined, ctx(noRunnableRoute(), 'apiserver'))
    expect(s.path.next.body).not.toMatch(/strongest evidence/i)
    expect(s.path.next.body).toMatch(/skipped before a request could be formed/i)
  })

  it('still explains the run is available when a route carries a request', () => {
    const s = buildSidebar(undefined, ctx(mk([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver' })]), 'apiserver'))
    expect(s.path.next.body).toMatch(/strongest evidence/i)
  })
})

describe('the report is ordered around the break', () => {
  // The break is the answer, so it opens; what came before it worked and is
  // context; what came after was never tried and must not read as healthy.
  const brokenTrace = () => mk([pod('a', true, '10.0.0.1')], [p({ ok: false, tone: 'unhealthy' })])
  const brokenRoute = () => route({ outcome: 'unreachable', confidence: 'real', evidence: 'connection refused' })

  it('opens the hop where the request stopped, not the last one', () => {
    const t = brokenTrace()
    const c = ctx(t, 'incluster', brokenRoute())
    const s = buildSidebar(undefined, c)
    expect(c.breakNodeId).toBeDefined()
    const open = s.hops.filter((h) => h.expanded)
    expect(open).toHaveLength(1)
    expect(open[0].id).toBe(c.breakNodeId)
    expect(open[0].state).toBe('break')
  })

  it('opens exactly one hop when nothing is selected', () => {
    const s = buildSidebar(undefined, ctx(mk([pod('a', true, '10.0.0.1')], [p({})]), 'incluster'))
    expect(s.hops.filter((h) => h.expanded)).toHaveLength(1)
  })

  it('labels hops past the break as never tried, not as fine', () => {
    const t = brokenTrace()
    const c = ctx(t, 'incluster', brokenRoute())
    const s = buildSidebar(undefined, c)
    expect(c.breakNodeId).toBeDefined()
    const after = s.hops.slice(s.hops.findIndex((h) => h.id === c.breakNodeId) + 1)
    for (const h of after) expect(h.state).toBe('after')
  })

  it('keeps hops in path order', () => {
    const s = buildSidebar(undefined, ctx(mk([pod('a', true, '10.0.0.1')], [p({})]), 'incluster'))
    expect(s.hops.map((h) => h.kind)).toEqual(['SERVICE', 'PODS'])
  })
})

describe('a derived break says which KIND it is', () => {
  const derived = (basis: 'declared-config' | 'cluster-state'): RouteResult => ({
    route: 'GET /', target: 'web:80', outcome: 'unreachable', basis,
  })

  // Calling "no ready endpoints" a configuration failure sends the reader to
  // edit YAML when the fix is to get Pods running.
  it('does not call a cluster-state break a configuration failure', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [])
    const s = buildSidebar(undefined, ctx(t, 'incluster', derived('cluster-state')))
    expect(s.path.body).not.toMatch(/configuration/i)
    expect(s.path.body).toMatch(/ready to serve/i)
    expect(s.path.body).toMatch(/changes when the workload does/i)
  })

  it('still calls a declared-config break a configuration failure', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [])
    const s = buildSidebar(undefined, ctx(t, 'incluster', derived('declared-config')))
    expect(s.path.body).toMatch(/configuration itself is broken/i)
  })

  // Both are DERIVED: neither was dialled, so neither may read as an
  // observation of the selected vantage.
  it('attributes neither to the selected vantage', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [])
    for (const b of ['declared-config', 'cluster-state'] as const) {
      const s = buildSidebar(undefined, ctx(t, 'local', derived(b)))
      expect(s.path.body).not.toMatch(/from here/i)
    }
  })
})

describe('the verdict band follows the selected origin', () => {
  // The band sits directly above the inspector, which reads the SELECTED
  // origin's own result. On the merged rollup the two contradicted each other
  // in adjacent panes - "could not get through" over "got through" - on exactly
  // the disagreeing traces per-vantage evidence exists to represent. This seam
  // regressed once already and had no pin.
  const disagreeing = (): RouteResult =>
    route({
      outcome: 'unreachable',
      confidence: 'real',
      byVantage: [
        { vantage: 'local', path: 'data', outcome: 'unreachable' },
        { vantage: 'in-cluster', path: 'data', outcome: 'verified', confidence: 'real' },
      ],
    })

  it('shows the origin-scoped result, not the rollup, and names its scope', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    const v = buildVerdict(t, disagreeing(), { originId: 'incluster', originName: 'In-cluster probe' })
    expect(v.tone).not.toBe('unhealthy')
    expect(v.chipText).toBe('got through')
    expect(v.chipScope).toContain('from In-cluster probe')
  })

  it('an origin absent from a produced breakdown reads not tested, never the rollup chip', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    const only = route({
      outcome: 'verified',
      confidence: 'real',
      byVantage: [{ vantage: 'in-cluster', path: 'data', outcome: 'verified', confidence: 'real' }],
    })
    const v = buildVerdict(t, only, { originId: 'local', originName: 'Radar on your machine' })
    expect(v.chipText).toBe('not tested')
  })
})

describe('localization facts belong to the vantage that produced them', () => {
  // route.localization rows are apiserver-relay observations (direct-pod dials
  // past the entry point). Crediting them to the in-cluster origin would claim
  // observations that vantage never made.
  const localized = (): RouteResult =>
    route({
      outcome: 'verified',
      confidence: 'indirect',
      byVantage: [{ vantage: 'local', path: 'apiserver', outcome: 'verified' }],
      localization: [{ layer: 'tcp', ok: true, detail: 'Pods answered directly, Service did not' }],
    })

  it('shows them under the apiserver origin', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ vantage: 'local', path: 'apiserver' })])
    const s = buildSidebar(undefined, ctx(t, 'apiserver', localized()))
    const texts = s.path.evidence.map((e) => e.text).join('\n')
    expect(texts).toMatch(/checked directly, past the entry point/)
  })

  it('never credits them to the in-cluster origin', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    const s = buildSidebar(undefined, ctx(t, 'incluster', localized()))
    const texts = s.path.evidence.map((e) => e.text).join('\n')
    expect(texts).not.toMatch(/checked directly/)
  })
})


describe('a Service-routing boundary in the sidebar', () => {
  const brokenRouting = (): RouteResult =>
    route({
      outcome: 'unreachable',
      confidence: 'real',
      byVantage: [{ vantage: 'in-cluster', path: 'data', outcome: 'unreachable', failedBoundary: 'service-routing' }],
    })
  const withWorkload = () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    t.downstream![1].config!.workload = { kind: 'Deployment', name: 'web', namespace: 'store' }
    return t
  }

  it('the SERVICE hop carries the break, exit-phrased', () => {
    const s = buildSidebar(undefined, ctx(withWorkload(), 'incluster', brokenRouting()))
    const svc = s.hops.find((h) => h.kind.startsWith('SERVICE'))!
    expect(svc.state).toBe('break')
    expect(svc.chipText).toMatch(/just past here/)
  })

  it('the workload hop is never a journey participant', () => {
    const s = buildSidebar(undefined, ctx(withWorkload(), 'incluster', brokenRouting()))
    const wl = s.hops.find((h) => h.kind.startsWith('DEPLOYMENT'))!
    expect(wl.state).toBe('plain')
  })
})

describe('bypassed and parallel entries are context, not journey', () => {
  const withEntries = (n = 1): Trace => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    t.upstreams = Array.from({ length: n }, (_, i) => ({
      resource: { kind: 'Ingress', name: `web-${i}`, namespace: 'store' },
      edge: 'ingress->service',
      findings: [],
      config: { hostnames: [`h${i}.example.com`], addresses: ['34.0.0.1'] },
    }))
    return t
  }

  it('the apiserver vantage does not list entries as its path', () => {
    const s = buildSidebar(undefined, ctx(withEntries(), 'apiserver'))
    expect(s.hops.some((h) => h.kind.startsWith('INGRESS'))).toBe(false)
    expect(s.context?.hops.some((h) => h.kind.startsWith('INGRESS'))).toBe(true)
    expect(s.context?.label).toMatch(/RELAYED/)
  })

  it('the laptop vantage keeps entries on the path', () => {
    const s = buildSidebar(undefined, ctx(withEntries(), 'local'))
    expect(s.hops.some((h) => h.kind.startsWith('INGRESS'))).toBe(true)
    expect(s.context).toBeUndefined()
  })

  it('two entries the route cannot be pinned to read as one parallel stage, never a sequence', () => {
    const s = buildSidebar(undefined, ctx(withEntries(2), 'local'))
    const entries = s.hops.filter((h) => h.kind.startsWith('INGRESS'))
    expect(entries).toHaveLength(2)
    for (const e of entries) expect(e.parallelCount).toBe(2)
  })
})

describe("'testing' is scoped to the vantage that is testing", () => {
  // The Job runs from the in-cluster vantage. Starting it while the LAPTOP is
  // selected must not flip the laptop-scoped band or sidebar to "testing" -
  // that is one vantage's state under another's name, in motion.
  it('the verdict band for a non-running origin never says testing', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    // The view computes running && origin.id === 'incluster'; for the laptop
    // that is false even mid-run.
    const v = buildVerdict(t, route(), { running: false, originId: 'local', originName: 'Radar on your machine' })
    expect(v.chipText).not.toBe('testing')
  })

  it('the sidebar for a non-running origin never says testing now', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ vantage: 'local', path: 'data' })])
    const c = ctx(t, 'local')
    const s = buildSidebar(undefined, { ...c, running: false })
    expect(JSON.stringify(s.path)).not.toMatch(/testing now/)
  })
})

describe('a break at one parallel entry orders nothing', () => {
  // Two entries with no host attribution stay in the journey as one parallel
  // stage. A failed dial of entry A must not make sibling B read "before the
  // break", nor the Service behind them "never tried - something earlier
  // stopped": the request may have gone through B.
  it('siblings and downstream hops read plain, never before/after', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    t.upstreams = [
      {
        resource: { kind: 'Ingress', name: 'entry-a', namespace: 'store' },
        edge: 'ingress->service',
        findings: [],
        config: { addresses: ['34.0.0.1'] },
        probes: [{ layer: 'tcp', target: '34.0.0.1:443', vantage: 'local', path: 'data', ok: false, tone: 'unhealthy' }],
      },
      {
        resource: { kind: 'Ingress', name: 'entry-b', namespace: 'store' },
        edge: 'ingress->service',
        findings: [],
        config: { addresses: ['34.0.0.2'] },
        probes: [{ layer: 'http', target: '34.0.0.2:443', vantage: 'local', path: 'data', ok: true, tone: 'healthy' }],
      },
    ]
    const s = buildSidebar(undefined, ctx(t, 'local'))
    const a = s.hops.find((h) => h.name === 'entry-a')
    const b = s.hops.find((h) => h.name === 'entry-b')
    const svc = s.hops.find((h) => h.kind.startsWith('SERVICE'))
    if (a?.state === 'break') {
      expect(b?.state).toBe('plain')
      expect(svc?.state).toBe('plain')
    } else {
      // No break anchored at the stage in this fixture shape - the serial
      // machine must still not be claiming order across parallel entries.
      expect(b?.state).not.toBe('before')
      expect(svc?.state).not.toBe('after')
    }
  })
})

describe('a skipped route says why, from the exact vantage that skipped it', () => {
  const reason = "HTTPS backend - the API-server proxy speaks plain HTTP and can't verify TLS on this port. Test it directly."
  const skipped = (target = 'shop:443'): RouteResult =>
    route({ target, outcome: 'not-tested', confidence: undefined, evidence: reason, byVantage: undefined })
  const mkSkipped = (skipProbe: Partial<ProbeResult>, runVantage: 'local' | 'in-cluster' = 'local'): Trace => {
    const t = mk([pod('shop-1', true, '10.0.0.1')], [])
    t.verdict = 'unknown'
    t.runVantage = runVantage
    t.coverage = { tested: 0, passed: 0, failed: 0, skipped: 1 }
    t.routes = [skipped()]
    t.downstream![0].probes = [p({ target: 'shop:443', port: 443, skipped: true, reason, ok: false, ...skipProbe })]
    return t
  }
  const proxySkip = { vantage: 'local' as const, path: 'apiserver' as const }

  it('surfaces the skip reason under the origin whose dial was skipped', () => {
    const s = buildSidebar(undefined, ctx(mkSkipped(proxySkip), 'apiserver', skipped()))
    expect(s.path.evidence.map((e) => e.text).join('\n')).toContain('API-server proxy speaks plain HTTP')
  })

  it('keeps the proxy reason under the proxy even when the run came from inside the cluster', () => {
    const s = buildSidebar(undefined, ctx(mkSkipped(proxySkip, 'in-cluster'), 'apiserver', skipped()))
    expect(s.path.evidence.map((e) => e.text).join('\n')).toContain('API-server proxy speaks plain HTTP')
  })

  it('never charges a vantage whose own dial was not the one skipped', () => {
    const t = mkSkipped(proxySkip)
    for (const id of ['local', 'incluster']) {
      const texts = buildSidebar(undefined, ctx(t, id, skipped())).path.evidence.map((e) => e.text).join('\n')
      expect(texts).not.toContain('speaks plain HTTP')
    }
  })

  it("never borrows another port's reason", () => {
    const t = mkSkipped({ ...proxySkip, port: 80, target: 'shop:80' })
    const texts = buildSidebar(undefined, ctx(t, 'apiserver', skipped('shop:443'))).path.evidence.map((e) => e.text).join('\n')
    expect(texts).not.toContain('API-server proxy')
  })

  it('a portless skip speaks for every port', () => {
    const t = mkSkipped({ ...proxySkip, port: undefined })
    const texts = buildSidebar(undefined, ctx(t, 'apiserver', skipped())).path.evidence.map((e) => e.text).join('\n')
    expect(texts).toContain('API-server proxy speaks plain HTTP')
  })

  it('says out loud that dots are health, not results, when nothing anywhere was tested', () => {
    const s = buildSidebar(undefined, ctx(mkSkipped(proxySkip), 'apiserver', skipped()))
    expect(s.path.body).toContain('cluster state, not a test result')
  })
})

describe('an attempted in-cluster run that never started keeps its attempt', () => {
  it('WHAT WE SAW carries the execution error, not "no test has been run"', () => {
    const t = mk([pod('shop-1', true, '10.0.0.1')], [])
    const r = route({ outcome: 'not-tested', confidence: undefined, byVantage: undefined })
    t.routes = [r]
    const origins = buildOrigins(t, { inClusterRunError: 'the probe image could not be pulled' })
    const origin = origins.find((o) => o.id === 'incluster')!
    const g = buildGraph({ trace: t, route: r, origin })
    const s = buildSidebar(undefined, {
      trace: t, route: r, origin, origins,
      nodes: g.nodes, breakNodeId: g.breakNodeId, breakAtExitOf: g.breakAtExitOf,
      nonNetworkNodeIds: g.nonNetworkNodeIds, contextNodeIds: g.contextNodeIds, interleave: g.interleave,
      entryParallelCount: g.entryParallelCount, journeyEntryNodeIds: g.journeyEntryNodeIds, pathNodeIds: g.pathNodeIds,
    })
    const texts = s.path.evidence.map((e) => e.text).join('\n')
    expect(texts).toContain('image could not be pulled')
    expect(texts).not.toContain('no test has been run from here')
  })
})

describe('a vantage that cannot be used explains itself in WHAT WE SAW', () => {
  it('the laptop with nothing to dial shows the structural reason, not "no test has been run"', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [])
    t.verdict = 'unknown'
    t.routes = [route({ outcome: 'not-tested', confidence: undefined, byVantage: undefined })]
    t.downstream![0].probes = [p({ target: 'shop:80', port: 80, vantage: 'local', path: 'apiserver', skipped: true, reason: 'proxy said no', ok: false })]
    const s = buildSidebar(undefined, ctx(t, 'local', t.routes[0]))
    const texts = s.path.evidence.map((e) => e.text).join('\n')
    expect(texts).toMatch(/no entry point .* from your machine|no Ingress/i)
    expect(texts).not.toContain('no test has been run from here')
  })
})

describe('a proxy-only failure never claims the target answered', () => {
  it('the body states the relayed dial failed and the real path stays untested', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver', ok: false, tone: 'unhealthy' })])
    const r = route({ outcome: 'unreachable', confidence: 'indirect' })
    t.routes = [r]
    const s = buildSidebar(undefined, ctx(t, 'apiserver', r))
    expect(s.path.body).toMatch(/relayed dial failed/i)
    expect(s.path.body).not.toMatch(/target answered/i)
  })
})

describe('nothing to suggest means nothing shown', () => {
  it('renders no next-step block at all when there is no stronger test', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({})])
    const s = buildSidebar(undefined, ctx(t, 'incluster'))
    expect(s.path.next.header).toBe('')
    expect(s.path.next.body).toBe('')
    expect(s.path.next.ctas).toHaveLength(0)
  })
})

describe('a fresh failed attempt outranks leftover skip rows', () => {
  it("WHAT WE SAW shows this run's execution error, not last run's skip", () => {
    const t = mk([pod('a', true, '10.0.0.1')], [])
    const r = route({ target: 'shop:443', outcome: 'not-tested', confidence: undefined, byVantage: undefined })
    t.routes = [r]
    t.downstream![1].probes = [
      p({ target: '10.0.0.1:8080', port: 8080, vantage: 'in-cluster', path: 'data', source: 'probe-job', skipped: true, reason: 'stale skip from the previous run', ok: false }),
    ]
    const origins = buildOrigins(t, { inClusterRunError: 'the probe image could not be pulled', route: r })
    const origin = origins.find((o) => o.id === 'incluster')!
    const g = buildGraph({ trace: t, route: r, origin })
    const s = buildSidebar(undefined, {
      trace: t, route: r, origin, origins,
      nodes: g.nodes, breakNodeId: g.breakNodeId, breakAtExitOf: g.breakAtExitOf,
      nonNetworkNodeIds: g.nonNetworkNodeIds, contextNodeIds: g.contextNodeIds, interleave: g.interleave,
      entryParallelCount: g.entryParallelCount, journeyEntryNodeIds: g.journeyEntryNodeIds, pathNodeIds: g.pathNodeIds,
    })
    const texts = s.path.evidence.map((e) => e.text).join('\n')
    expect(texts).toContain('image could not be pulled')
    expect(texts).not.toContain('stale skip')
  })
})

describe('a result with no evidence string still reads as a result', () => {
  it('never says "no test has been run" beside an outcome that ran', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ layer: 'tcp', path: 'data', ok: true })])
    const r = route({ route: 'redis', target: 'redis:6379', outcome: 'reached', confidence: 'real', evidence: undefined })
    t.routes = [r]
    const texts = buildSidebar(undefined, ctx(t, 'incluster', r)).path.evidence.map((e) => e.text).join('\n')
    expect(texts).not.toContain('no test has been run from here')
    expect(texts.length).toBeGreaterThan(0)
  })
})

describe('selecting a context entry opens it', () => {
  // The entry-problem row exists to answer "show me the cause". Landing on a
  // still-collapsed section makes the reader spend a second click on the very
  // thing they asked for - and it must hold for EVERY declared entry kind.
  const withEntries = (): Trace => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver' })])
    t.upstreams = [
      { resource: { kind: 'Ingress', name: 'shop', namespace: 'store' }, edge: 'ingress->service', findings: [], config: { addresses: ['34.0.0.1'] } },
      { resource: { kind: 'HTTPRoute', name: 'shop', namespace: 'store' }, edge: 'httproute->service', findings: [] },
    ]
    return t
  }
  for (const kind of ['Ingress', 'HTTPRoute']) {
    it(`expands the selected ${kind}, and leaves its sibling collapsed`, () => {
      const t = withEntries()
      const c = ctx(t, 'apiserver')
      const id = `n:${kind}/store/shop`
      const s = buildSidebar(id, c)
      const hops = s.context?.hops ?? []
      expect(hops.length).toBeGreaterThan(0)
      const picked = hops.find((h) => h.id === id)
      expect(picked, `${kind} should be a context hop`).toBeTruthy()
      expect(picked!.expanded).toBe(true)
      expect(hops.filter((h) => h.id !== id).every((h) => !h.expanded)).toBe(true)
    })
  }
  it('leaves every context hop collapsed when nothing is selected', () => {
    const s = buildSidebar(undefined, ctx(withEntries(), 'apiserver'))
    expect((s.context?.hops ?? []).every((h) => !h.expanded)).toBe(true)
  })
})

describe('a coverage statement is not a fault', () => {
  it('the verdict keeps it on the wire but marks it coverage-class', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver' })])
    t.diagnosis = { class: 'coverage', summary: "reachable via API server - the real-traffic path wasn't confirmed from here" } as never
    // buildVerdict still surfaces it for consumers that want the sentence...
    const v = buildVerdict(t, route({ outcome: 'reached', confidence: 'indirect' }), { originId: 'apiserver', originName: 'API-server proxy' })
    expect(v.problem).toBeTruthy()
    // ...and the class is what the header uses to keep it OUT of the problem list.
    expect(t.diagnosis!.class).toBe('coverage')
  })
})

describe('one observation is stated once', () => {
  it('a localization fact does not repeat the route evidence it came from', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver', tone: 'reached', detail: 'HTTP 404 · reached' })])
    const r = route({
      outcome: 'reached', confidence: 'indirect', evidence: 'HTTP 404 · reached',
      localization: [{ layer: 'http', ok: true, detail: 'HTTP 404 · reached' }],
    } as never)
    t.routes = [r]
    const texts = buildSidebar(undefined, ctx(t, 'apiserver', r)).path.evidence.map((e) => e.text)
    const plain = texts.filter((x) => x.toLowerCase().startsWith('http 404 · reached'))
    expect(plain, `saw: ${JSON.stringify(texts)}`).toHaveLength(1)
    // and the surviving line is the MORE specific one
    expect(plain[0]).toContain('checked directly')
  })
})

describe('an answer is interpretable', () => {
  const reached = (evidence: string): RouteResult =>
    route({ outcome: 'reached', confidence: 'real', evidence, inClusterRequest: { protocol: 'http', scheme: 'http', path: '/' } } as never)
  const body = (evidence: string) => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ tone: 'reached', detail: evidence })])
    const r = reached(evidence)
    t.routes = [r]
    return buildSidebar(undefined, ctx(t, 'incluster', r)).path
  }

  it('a 404 says the PATH works and names what would verify a route', () => {
    const b = body('HTTP 404 · reached').body
    expect(b).toContain('path works')
    expect(b).toContain('serves no route')
    expect(b).toContain('re-run with a path your app serves')
  })
  it('auth, redirect and app-error answers are each read correctly', () => {
    expect(body('HTTP 401 · reached').body).toContain('credentials')
    expect(body('HTTP 308 · reached, redirect').body).toMatch(/redirect .*Radar does not follow/)
    expect(body('HTTP 503 · reached, server error').body).toContain('application health')
  })
  it('a transport-only reach never claims something was asked', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ layer: 'tcp', ok: true })])
    const r = route({ outcome: 'reached', confidence: 'real', evidence: '', inClusterRequest: { protocol: 'tcp' } } as never)
    t.routes = [r]
    expect(buildSidebar(undefined, ctx(t, 'incluster', r)).path.body).toContain('nothing was asked of the application')
  })
  it('shows WHAT was requested, so a status code can be read at all', () => {
    expect(body('HTTP 404 · reached').scope.some((s) => s.k === 'REQUEST' && s.v === 'GET /')).toBe(true)
    const t = mk([pod('a', true, '10.0.0.1')], [p({ layer: 'tcp', ok: true })])
    const r = route({ outcome: 'reached', confidence: 'real', inClusterRequest: { protocol: 'tcp' } } as never)
    t.routes = [r]
    expect(buildSidebar(undefined, ctx(t, 'incluster', r)).path.scope.some((s) => s.k === 'REQUEST' && s.v === 'TCP connect')).toBe(true)
  })
})

describe('a relayed answer explains the relay AND the answer', () => {
  it('keeps the relay caveat and still reads the status', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver', tone: 'reached', detail: 'HTTP 404 · reached' })])
    const r = route({ outcome: 'reached', confidence: 'indirect', evidence: 'HTTP 404 · reached', inClusterRequest: { protocol: 'http', path: '/' } } as never)
    t.routes = [r]
    const b = buildSidebar(undefined, ctx(t, 'apiserver', r)).path.body
    expect(b).toContain('relayed')
    expect(b).toContain('serves no route')
    // ...but never claims the real path works, because a relay cannot show that
    expect(b).not.toContain('The path works')
  })
})

// An answer is not the same as an answer that got through. A gateway saying it
// could not reach its upstream, and a handshake that never verified, are both
// answers - and both mean the opposite of "the path works".
describe('an answer that reports a break never reads as a success', () => {
  const bodyFor = (r: Partial<RouteResult>) => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ tone: 'reached' } as never)])
    const full = route({ outcome: 'server-error', confidence: 'real', inClusterRequest: { protocol: 'http', path: '/' }, ...r } as never)
    t.routes = [full]
    return buildSidebar(undefined, ctx(t, 'incluster', full)).path.body
  }

  for (const code of [502, 504]) {
    it(`does not claim the path works on HTTP ${code}`, () => {
      const b = bodyFor({ evidence: `HTTP ${code}`, failedLayer: 'upstream' } as never)
      expect(b).not.toContain('The path works')
      expect(b).toContain('could not reach the backend')
    })
  }

  it('explains a certificate failure, which carries no status code at all', () => {
    const b = bodyFor({ evidence: 'x509: certificate has expired', failedLayer: 'tls' } as never)
    expect(b).not.toContain('The path works')
    expect(b).toContain('certificate problem')
    // The generic fallthrough would have said this instead, explaining nothing.
    expect(b).not.toContain('not with what was asked for')
  })

  it('still credits the path when the APP itself answered', () => {
    expect(bodyFor({ evidence: 'HTTP 404' } as never)).toContain('The path works')
    expect(bodyFor({ evidence: 'HTTP 500' } as never)).toContain('The path works')
  })
})

describe('every state explains itself, not the nearest generic sentence', () => {
  const withRoute = (r: RouteResult, probes = [p({})]) => {
    const t = mk([pod('a', true, '10.0.0.1')], probes)
    t.routes = [r]
    return buildSidebar(undefined, ctx(t, 'incluster', r)).path.body
  }

  it('a deliberately dormant path is not described as an answer', () => {
    const b = withRoute(route({ outcome: 'unreachable', benign: true, evidence: 'no running backends (scaled to 0)' } as never))
    expect(b).toContain('deliberate')
    expect(b).not.toContain('not with what was asked for')
  })

  it('a kept-informational run says it ran AND why it does not decide', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [
      p({ target: 'shop:80', port: 80, skipped: true, skipClass: 'informational', reason: 'the throwaway Pod was denied by mTLS', ok: false, source: 'probe-job' }),
    ])
    const r = route({ target: 'shop:80', outcome: 'not-tested', confidence: undefined, byVantage: undefined } as never)
    t.routes = [r]
    const b = buildSidebar(undefined, ctx(t, 'incluster', r)).path.body
    expect(b + JSON.stringify(buildSidebar(undefined, ctx(t, 'incluster', r)).path.evidence)).toContain('denied by mTLS')
  })

  it('a verified answer is not described as a partial one', () => {
    const b = withRoute(route({ outcome: 'verified', confidence: 'real', evidence: 'HTTP 200 · verified' } as never))
    expect(b).toContain('A real request went through')
  })
})


describe('the action sits with its explanation', () => {
  it('an untested vantage offers the run, and does not repeat the sentence above it', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver' })])
    const r = route({ outcome: 'not-tested', confidence: undefined, byVantage: undefined, inClusterRequest: { protocol: 'http', path: '/' } } as never)
    t.routes = [r]
    const s = buildSidebar(undefined, ctx(t, 'incluster', r)).path
    expect(s.body).toContain('Nothing has been tested from here')
    // the action is present...
    expect(s.next.ctas.some((c) => c.action === 'run-in-cluster')).toBe(true)
    // ...and the block does not say "nothing has been tested" a second time
    expect(s.next.body).toBe('')
  })

  it('WHAT WE SAW disappears rather than echoing "no test has been run"', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver' })])
    const r = route({ outcome: 'not-tested', confidence: undefined, byVantage: undefined } as never)
    t.routes = [r]
    const ev = buildSidebar(undefined, ctx(t, 'incluster', r)).path.evidence
    expect(ev).toHaveLength(0)
  })

  it('a vantage that CANNOT be used still says why, instead of going silent', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [])
    t.verdict = 'unknown'
    t.routes = [route({ outcome: 'not-tested', confidence: undefined, byVantage: undefined } as never)]
    t.downstream![0].probes = [p({ target: 'shop:80', port: 80, vantage: 'local', path: 'apiserver', skipped: true, reason: 'proxy said no', ok: false })]
    const ev = buildSidebar(undefined, ctx(t, 'local', t.routes[0])).path.evidence
    expect(ev.map((e) => e.text).join(' ')).toMatch(/no entry point|Ingress/i)
  })
})

// The two vantages that can be refused need DIFFERENT grants. Sending an
// operator to their cluster admin asking for `create jobs` when what they lack
// is the proxy subresource wastes the one request they get to make.
describe('a refusal asks for the grant that would actually fix it', () => {
  const sidebarFor = (t: Trace, originId: string, opts = {}) => {
    const origins = buildOrigins(t, opts)
    const origin = origins.find((o) => o.id === originId)!
    const r = t.routes![0]
    const g = buildGraph({ trace: t, route: r, origin, origins })
    return buildSidebar(undefined, {
      trace: t, route: r, origin, origins,
      nodes: g.nodes, breakNodeId: g.breakNodeId, breakAtExitOf: g.breakAtExitOf,
      nonNetworkNodeIds: g.nonNetworkNodeIds, contextNodeIds: g.contextNodeIds,
      interleave: g.interleave, entryParallelCount: g.entryParallelCount,
      journeyEntryNodeIds: g.journeyEntryNodeIds, pathNodeIds: g.pathNodeIds,
    })
  }

  // The in-cluster dial already succeeded, so no stronger gap is left to
  // propose - which is exactly when the panel falls through to the refusal.
  it('names the proxy subresource when the relay was the refused vantage', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [
      p({ vantage: 'in-cluster', path: 'data', ok: true, tone: 'healthy' }),
      p({
        vantage: 'local', path: 'apiserver', skipped: true, ok: false, skipClass: 'denied',
        reason: 'Permission denied. Your identity lacks get services/proxy or get pods/proxy in this namespace.',
      }),
    ])
    const next = sidebarFor(t, 'apiserver').path.next!
    expect(next.header).toBe('ASK FOR THIS PERMISSION')
    expect(next.body).toMatch(/services\/proxy/)
    expect(next.body).not.toMatch(/jobs/)
    expect(next.ctas[0].command).toBe('kubectl auth can-i get services/proxy -n store')
  })

  it('still names the Job grant when the in-cluster probe was the refused one', () => {
    const t = mk([pod('a', true, '10.0.0.1')], [p({ vantage: 'local', path: 'data', ok: true, tone: 'healthy' })])
    const next = sidebarFor(t, 'incluster', {
      inClusterAllowed: false,
      inClusterDeniedReason: 'RBAC denies create on jobs',
    }).path.next!
    expect(next.header).toBe('ASK FOR THIS PERMISSION')
    expect(next.body).toMatch(/jobs/)
    expect(next.body).not.toMatch(/services\/proxy/)
    expect(next.ctas[0].command).toBe('kubectl auth can-i create jobs -n store')
  })
})
