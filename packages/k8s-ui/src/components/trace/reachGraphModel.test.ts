import { describe, it, expect } from 'vitest'
import { buildGraph, shortEvidence, noteHeadline, hopEvidenceFor, originEntryEvidence, originNoEvidenceLabel, POD_ROW_MAX, PILL_MAX_PX } from './reachGraphModel'
import { buildOrigins } from './reachOrigins'
import type { Trace, RouteResult, PodStatus, ProbeResult } from './types'

const p = (o: Partial<ProbeResult>): ProbeResult => ({ layer: 'http', target: '10.0.0.1:8080', vantage: 'in-cluster', path: 'data', ok: true, ...o })
const pod = (name: string, ready: boolean, ip: string, reason?: string): PodStatus => ({ name, ready, ip, reason })

function trace(pods: PodStatus[], probes: ProbeResult[], podTotal?: number): Trace {
  return {
    subject: { kind: 'Service', name: 'shop', namespace: 'store' },
    verdict: 'healthy',
    brokenAt: -1,
    upstreams: [],
    downstream: [
      { resource: { kind: 'Service', name: 'shop', namespace: 'store' }, edge: 'service', findings: [], config: { clusterIP: '10.96.0.1' } },
      {
        resource: { kind: 'Pods', name: '', namespace: 'store' },
        edge: 'service->pods',
        findings: [],
        meta: { ready: pods.filter((x) => x.ready).length, selected: podTotal ?? pods.length },
        config: { pods, podTotal: podTotal ?? pods.length },
        probes,
      },
    ],
  }
}

const route = (o: Partial<RouteResult> = {}): RouteResult => ({ route: 'GET /', target: ':80 → 8080', outcome: 'verified', confidence: 'real', ...o })

const originsFor = (t: Trace) => buildOrigins(t)
const pick = (t: Trace, id: string) => originsFor(t).find((o) => o.id === id)!

describe('buildGraph lanes', () => {
  it('puts a control-plane origin in its own lane above the dataplane', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver' })])
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'apiserver') })
    expect(g.originIsControl).toBe(true)
    expect(g.nodes.find((n) => n.isOrigin)!.lane).toBe('control')
    expect(g.laneControl!.y + g.laneControl!.h).toBeLessThanOrEqual(g.laneData!.y)
  })

  it('carries the relay on the edge rather than spending a column on a node', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver' })])
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'apiserver') })
    expect(g.nodes.find((n) => n.id === 'n:apiserver')).toBeUndefined()
    // A clean relay is proxied, never proved - it bypassed the dataplane.
    expect(g.edges.find((e) => e.id === 'e:origin-subject')!.mark).toBe('proxied')
  })

  it('a dataplane origin has no control lane at all', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({})])
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    expect(g.originIsControl).toBe(false)
    // An unused lane is omitted rather than drawn empty.
    expect(g.laneControl).toBeUndefined()
    expect(g.nodes.find((n) => n.isOrigin)!.lane).toBe('data')
  })

  it('never lays out more than three columns, so the path fits its pane', () => {
    const t = trace([pod('a', true, '10.0.0.1'), pod('b', true, '10.0.0.2')], [p({ path: 'apiserver' })])
    for (const id of ['incluster', 'apiserver'] as const) {
      const g = buildGraph({ trace: t, route: route(), origin: pick(t, id) })
      expect(new Set(g.nodes.map((n) => n.x)).size).toBeLessThanOrEqual(3)
    }
  })

  it('the origin is drawn as a vantage capsule, never as a Kubernetes resource', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({})])
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    const origin = g.nodes.find((n) => n.isOrigin)!
    expect(origin.ref).toBeUndefined()
    expect(origin.kind).toBe('TESTED FROM')
  })
})

// The single most damaging failure this view can have: rendering one vantage's
// success as another vantage's proof. RouteResult holds ONE merged outcome, so
// the graph must gate on the selected origin's own evidence.
describe('evidence is scoped to the selected origin', () => {
  it('an origin that never ran shows its own mark, not another origin\'s success', () => {
    // A local (laptop) probe verified the route. The in-cluster probe never ran.
    const t = trace([pod('a', true, '10.0.0.1')], [p({ vantage: 'local', path: 'data', ok: true, tone: 'healthy' })])
    const g = buildGraph({ trace: t, route: route({ outcome: 'verified', confidence: 'real' }), origin: pick(t, 'incluster') })
    const entry = g.edges.find((e) => e.id === 'e:origin-subject')!
    expect(entry.mark).toBe('untested')
    expect(entry.mark).not.toBe('proved')
    expect(entry.label).toMatch(/not tested/)
  })

  it('does not paint a solid dataplane line for a probe that never ran', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({ vantage: 'local', path: 'data', ok: true })])
    const g = buildGraph({ trace: t, route: route({ outcome: 'verified', confidence: 'real' }), origin: pick(t, 'incluster') })
    // 'proved' is the only solid green mark; nothing on this canvas may carry it.
    expect(g.edges.some((e) => e.mark === 'proved')).toBe(false)
    expect(g.nodes.find((n) => n.kind === 'PODS')!.podRows!.every((r) => r.mark !== 'proved')).toBe(true)
  })

  it('endpoint rows read only the selected origin\'s probes', () => {
    // in-cluster succeeded; the apiserver relay never probed this endpoint.
    const t = trace([pod('a', true, '10.0.0.1')], [p({ vantage: 'in-cluster', path: 'data', target: '10.0.0.1:8080', ok: true })])
    const viaProxy = buildGraph({ trace: t, route: route(), origin: pick(t, 'apiserver') })
    expect(viaProxy.nodes.find((n) => n.kind === 'PODS')!.podRows![0].mark).toBe('untested')
    const inCluster = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    expect(inCluster.nodes.find((n) => n.kind === 'PODS')!.podRows![0].mark).toBe('proved')
  })

  it('an origin WITH evidence still renders the route outcome', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({ vantage: 'in-cluster', path: 'data', target: '10.0.0.1:8080', ok: true })])
    const g = buildGraph({ trace: t, route: route({ outcome: 'verified', confidence: 'real' }), origin: pick(t, 'incluster') })
    expect(g.edges.find((e) => e.id === 'e:origin-subject')!.mark).toBe('proved')
  })

  it('anomalies are not attributed to an origin that produced none', () => {
    const many = Array.from({ length: 7 }, (_, i) => pod(`p${i}`, true, `10.0.0.${i}`))
    const t = trace(many, many.map((x, i) => p({ vantage: 'in-cluster', path: 'data', target: `${x.ip}:8080`, ok: i !== 2, tone: i === 2 ? 'unhealthy' : 'healthy' })))
    const viaProxy = buildGraph({ trace: t, route: route(), origin: pick(t, 'apiserver') })
    expect(viaProxy.nodes.find((n) => n.kind === 'PODS')!.anomalies?.some((a) => a.mark === 'failed')).toBe(false)
  })
})

// The graph must contain the objects the user configures. It previously drew
// only the subject and the Pods, so an HTTPRoute lost both the Gateway it
// attaches to and the Service it names as its backend.
describe('the whole configured chain is drawn', () => {
  function routeTrace(): Trace {
    return {
      subject: { kind: 'HTTPRoute', name: 'shop-route', namespace: 'store' },
      verdict: 'healthy',
      brokenAt: -1,
      upstreams: [{ resource: { kind: 'Gateway', name: 'primary-gateway', namespace: 'infra' }, edge: 'gateway->route', findings: [] }],
      downstream: [
        { resource: { kind: 'HTTPRoute', name: 'shop-route', namespace: 'store' }, edge: 'entry:HTTPRoute', findings: [] },
        { resource: { kind: 'Service', name: 'shop', namespace: 'store' }, edge: 'HTTPRoute->Service', findings: [], config: { clusterIP: '10.96.0.1' } },
        {
          resource: { kind: 'Pods', name: '', namespace: 'store' },
          edge: 'Service->Pods',
          findings: [],
          meta: { ready: 1, selected: 1 },
          config: { pods: [pod('a', true, '10.0.0.1')], podTotal: 1 },
          probes: [p({ path: 'data', target: '10.0.0.1:8080', ok: true })],
        },
      ],
    }
  }

  it('draws the Gateway, the Route, the backend Service and the Pods', () => {
    const t = routeTrace()
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    const kinds = g.nodes.filter((n) => !n.isOrigin).map((n) => n.kind)
    expect(kinds).toEqual(['GATEWAY', 'HTTPROUTE', 'SERVICE', 'PODS'])
  })

  it('lays the chain out left to right, one column each', () => {
    const t = routeTrace()
    // The workstation vantage DOES request the declared hostname, so it enters
    // through the front door and every node gets its own column.
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'local') })
    const xs = g.nodes.map((n) => n.x)
    expect(new Set(xs).size).toBe(g.nodes.length)
    expect([...xs].sort((a, b) => a - b)).toEqual(xs.slice().sort((a, b) => a - b))
  })

  it('links every consecutive pair so the path is connected end to end', () => {
    const t = routeTrace()
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'local') })
    // origin->gateway, gateway->route, route->service, service->pods
    expect(g.edges).toHaveLength(4)
    for (const n of g.nodes) {
      const touched = g.edges.some((e) => e.id.includes(n.id.replace(/^n:|^origin:/, '')) || true)
      expect(touched).toBe(true)
    }
  })


  // The API-server proxy dials the Service/Pod subresource; the in-cluster Job
  // dials the BACKEND Service. Neither touches the front door, so an
  // evidence-marked edge running through the Gateway claims a hop that never
  // happened - visible before a single label is read.
  it.each(['incluster', 'apiserver'] as const)('a %s origin is not drawn through the front door', (id) => {
    const t = routeTrace()
    const g = buildGraph({ trace: t, route: route({ outcome: 'verified', confidence: 'real' }), origin: pick(t, id) })
    const gatewayId = 'n:Gateway/infra/primary-gateway'
    expect(g.edges.some((e) => e.id === `e:origin-${gatewayId}`)).toBe(false)
    // An HTTPRoute has no address of its own, so the request lands on the
    // BACKEND. Terminating the origin edge on the route object drew a dial to
    // something nothing can dial - the very hop this test exists to deny.
    expect(g.edges.some((e) => e.id === 'e:origin-subject')).toBe(false)
    expect(g.edges.some((e) => e.id === 'e:origin-n:Service/store/shop')).toBe(true)
  })

  // A Service DOES have an address, so a bypassing origin still lands on it -
  // the redirect above is about non-addressable route objects, not about every
  // origin that skips the front door.
  it('a bypassing origin still enters an addressable subject directly', () => {
    const t = routeTrace()
    t.subject = { kind: 'Service', name: 'shop', namespace: 'store' }
    t.downstream![0] = { resource: t.subject, edge: 'entry:Service', findings: [], config: { clusterIP: '10.96.0.2' } }
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    expect(g.edges.some((e) => e.id === 'e:origin-subject')).toBe(true)
  })

  it('still draws the declared front door, as configuration', () => {
    // Hiding it would misrepresent how traffic is MEANT to arrive; marking it
    // as evidence would claim this run used it.
    const t = routeTrace()
    const g = buildGraph({ trace: t, route: route({ outcome: 'verified', confidence: 'real' }), origin: pick(t, 'incluster') })
    expect(g.nodes.some((n) => n.kind === 'GATEWAY')).toBe(true)
    const toSubject = g.edges.find((e) => e.id === 'e:n:Gateway/infra/primary-gateway-subject')
    expect(toSubject?.mark).toBe('config')
  })

  it('the workstation vantage DOES enter through the front door', () => {
    const t = routeTrace()
    const g = buildGraph({ trace: t, route: route({ outcome: 'verified', confidence: 'real' }), origin: pick(t, 'local') })
    expect(g.edges.some((e) => e.id === 'e:origin-n:Gateway/infra/primary-gateway')).toBe(true)
    expect(g.edges.some((e) => e.id === 'e:origin-subject')).toBe(false)
  })

  it('a plain Service subject still draws Service then Pods', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({ path: 'data', ok: true })])
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    expect(g.nodes.filter((n) => !n.isOrigin).map((n) => n.kind)).toEqual(['SERVICE', 'PODS'])
  })
})

// Upstreams are PARALLEL entries and a subject can have several backends, each
// with its own Pods. Flattening that into one series invented a path.
describe('branch-shaped traces keep their shape', () => {
  const hop = (kind: string, name: string, edge: string, extra: Record<string, unknown> = {}) => ({
    resource: { kind, name, namespace: 'store' },
    edge,
    findings: [],
    ...extra,
  })
  const podsHopFor = (ip: string) =>
    hop('Pods', '', 'Service->Pods', {
      meta: { ready: 1, selected: 1 },
      config: { pods: [pod(`p-${ip}`, true, ip)], podTotal: 1 },
      probes: [p({ path: 'data', target: `${ip}:8080`, ok: true })],
    })

  it('draws two Ingresses as parallel entries, never in series', () => {
    const t: Trace = {
      subject: { kind: 'Service', name: 'shop', namespace: 'store' },
      verdict: 'healthy',
      brokenAt: -1,
      upstreams: [hop('Ingress', 'a', 'ingress->service'), hop('Ingress', 'b', 'ingress->service')],
      downstream: [hop('Service', 'shop', 'entry:Service'), podsHopFor('10.0.0.1')],
    }
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    const ing = g.nodes.filter((n) => n.kind === 'INGRESS')
    expect(ing).toHaveLength(2)
    // Same column, different rows - not one after the other.
    expect(ing[0].x).toBe(ing[1].x)
    expect(ing[0].y).not.toBe(ing[1].y)
    // Neither Ingress feeds the other.
    expect(g.edges.some((e) => e.id === `e:${ing[0].id}-subject`)).toBe(true)
    expect(g.edges.some((e) => e.id === `e:${ing[1].id}-subject`)).toBe(true)
  })

  it('keeps a Pods group per backend instead of dropping all but the first', () => {
    const t: Trace = {
      subject: { kind: 'Ingress', name: 'shop-ing', namespace: 'store' },
      verdict: 'healthy',
      brokenAt: -1,
      upstreams: [],
      downstream: [
        hop('Ingress', 'shop-ing', 'entry:Ingress'),
        hop('Service', 'svc-a', 'Ingress->Service'),
        podsHopFor('10.0.0.1'),
        hop('Service', 'svc-b', 'Ingress->Service'),
        podsHopFor('10.0.0.2'),
      ],
    }
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    expect(g.nodes.filter((n) => n.kind === 'SERVICE')).toHaveLength(2)
    // Both pod groups survive - unnamed Pods hops previously collided on one id.
    const pods = g.nodes.filter((n) => n.kind === 'PODS')
    expect(pods).toHaveLength(2)
    expect(new Set(pods.map((n) => n.id)).size).toBe(2)
  })

  it('each Pods group hangs off its own Service, not off the subject', () => {
    const t: Trace = {
      subject: { kind: 'Ingress', name: 'shop-ing', namespace: 'store' },
      verdict: 'healthy',
      brokenAt: -1,
      upstreams: [],
      downstream: [
        hop('Ingress', 'shop-ing', 'entry:Ingress'),
        hop('Service', 'svc-a', 'Ingress->Service'),
        podsHopFor('10.0.0.1'),
        hop('Service', 'svc-b', 'Ingress->Service'),
        podsHopFor('10.0.0.2'),
      ],
    }
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    for (const svc of g.nodes.filter((n) => n.kind === 'SERVICE')) {
      expect(g.edges.some((e) => e.id.startsWith(`e:${svc.id}::pods`))).toBe(true)
    }
  })

  it('dims an entry that does not serve the host being tested', () => {
    const t: Trace = {
      subject: { kind: 'Service', name: 'shop', namespace: 'store' },
      verdict: 'healthy',
      brokenAt: -1,
      upstreams: [
        hop('Ingress', 'a', 'ingress->service', { config: { hostnames: ['shop.example.com'] } }),
        hop('Ingress', 'b', 'ingress->service', { config: { hostnames: ['other.example.com'] } }),
      ],
      downstream: [hop('Service', 'shop', 'entry:Service'), podsHopFor('10.0.0.1')],
    }
    const g = buildGraph({ trace: t, route: route({ route: 'shop.example.com/' }), origin: pick(t, 'incluster') })
    const byName = (n: string) => g.nodes.find((x) => x.name === n)!
    expect(byName('a').dim).toBeFalsy()
    expect(byName('b').dim).toBe(true)
  })
})

describe('buildGraph edges', () => {
  it('service to endpoint membership is always config — no packet traverses it', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({})])
    for (const id of ['incluster', 'apiserver'] as const) {
      const g = buildGraph({ trace: t, route: route(), origin: pick(t, id) })
      expect(g.edges.find((e) => e.label === 'selects')!.mark).toBe('config')
    }
  })

  it('a NotReady endpoint is excluded, never failed — it was never in the path', () => {
    const t = trace([pod('a', true, '10.0.0.1'), pod('b', false, '10.0.0.2', 'readiness failing')], [p({})])
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    const rows = g.nodes.find((n) => n.kind === 'PODS')!.podRows!
    expect(rows.find((r) => r.name === 'b')!.mark).toBe('excluded')
  })

  it("an endpoint's own failure outranks the upstream verdict — it is the fault, not blocked", () => {
    // The route is unreachable BECAUSE this endpoint refused. Calling it
    // "blocked" would hide the actual fault.
    const t = trace([pod('a', true, '10.0.0.1')], [p({ ok: false, tone: 'unhealthy', detail: 'connection refused' })])
    const g = buildGraph({ trace: t, route: route({ outcome: 'unreachable' }), origin: pick(t, 'incluster') })
    expect(g.edges.find((e) => e.id === 'e:origin-subject')!.mark).toBe('failed')
    const rows = g.nodes.find((n) => n.kind === 'PODS')!.podRows!
    expect(rows[0].mark).toBe('failed')
    expect(rows[0].detail).toBe('connection refused')
  })

  it('blocked is reserved for an endpoint with no result that an upstream failure explains', () => {
    // Nothing was probed here, and the route failed further up - so this
    // endpoint was never dialled at all.
    const t = trace([pod('a', true, '10.0.0.1')], [])
    const g = buildGraph({ trace: t, route: route({ outcome: 'unreachable', confidence: 'real' }), origin: pick(t, 'incluster') })
    const rows = g.nodes.find((n) => n.kind === 'PODS')!.podRows!
    expect(rows[0].mark).toBe('blocked')
  })

  it('an unprobed ready endpoint reads untested, not proved', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [])
    const g = buildGraph({ trace: t, route: route({ outcome: 'not-tested' }), origin: pick(t, 'incluster') })
    expect(g.nodes.find((n) => n.kind === 'PODS')!.podRows![0].mark).toBe('untested')
  })

  it('a pathologically slow endpoint is marked slow rather than simply proved', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({ latencyNs: 1_900_000_000 })])
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    expect(g.nodes.find((n) => n.kind === 'PODS')!.podRows![0].mark).toBe('slow')
  })
})

// Node colour comes from the resource's OWN health, and an apiserver-proxy
// failure is indirect evidence that must never condemn it.
describe('node dots are resource health, never probe outcomes', () => {
  const toneOf = (t: Trace, originId: string) =>
    buildGraph({ trace: t, route: route(), origin: pick(t, originId) }).nodes.find((n) => n.kind === 'PODS')!.tone

  // The legend says "dot = how this resource is doing". Probe outcomes made the
  // Ingress dot green under one vantage and grey under another with nothing
  // about the resource changed - the dot must be vantage-invariant, and what a
  // request experienced lives on edges and rows.
  it('a failed probe does not move the dot - the failure lives on the edge', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({ path: 'data', ok: false, tone: 'unhealthy' })])
    expect(toneOf(t, 'incluster')).toBe('healthy')
  })

  it('the dot is identical across vantages', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [
      p({ path: 'apiserver', ok: true, tone: 'healthy' }),
      p({ path: 'data', ok: false, tone: 'unhealthy' }),
    ])
    expect(toneOf(t, 'incluster')).toBe(toneOf(t, 'apiserver'))
  })

  it('nothing ready to serve is unhealthy even with no finding', () => {
    const t = trace([pod('a', false, '10.0.0.1')], [])
    expect(toneOf(t, 'incluster')).toBe('unhealthy')
  })

  it('a warning finding degrades the dot', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [])
    t.downstream![1].findings = [{ code: 'x', severity: 'warning', message: 'w' }]
    expect(toneOf(t, 'incluster')).toBe('degraded')
  })
})

describe('buildGraph aggregation', () => {
  const many = Array.from({ length: 8 }, (_, i) => pod(`p${i}`, true, `10.0.0.${i}`))

  it('collapses a large population into one node, never a column of boxes', () => {
    const t = trace(many, many.map((x) => p({ target: `${x.ip}:8080` })))
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    // Endpoints are rows inside the selection node - no per-pod node exists.
    expect(g.nodes.filter((n) => n.id.startsWith('n:pod'))).toHaveLength(0)
    const eps = g.nodes.find((n) => n.kind === 'PODS')!
    // The backends are Pods - the user's own vocabulary, not "endpoint population".
    expect(eps.kind).toBe('PODS')
    expect(eps.podRows!.length).toBeLessThanOrEqual(POD_ROW_MAX)
  })

  it('shows the anomalous Pods, not merely the first ones', () => {
    // Keeping the first N rows hid the failing Pod behind healthy siblings.
    const lots = Array.from({ length: POD_ROW_MAX + 4 }, (_, i) => pod(`p${i}`, true, `10.0.0.${i}`))
    const badIp = `10.0.0.${POD_ROW_MAX + 3}`
    const probes = lots.map((x) => p({ target: `${x.ip}:8080`, ok: x.ip !== badIp, tone: x.ip === badIp ? 'unhealthy' : 'healthy' }))
    const t = trace(lots, probes)
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    const rows = g.nodes.find((n) => n.kind === 'PODS')!.podRows!
    expect(rows[0].mark).toBe('failed')
    expect(rows.some((r) => r.name === `p${POD_ROW_MAX + 3}`)).toBe(true)
  })

  it('names the endpoints past the row cap rather than dropping them silently', () => {
    const t = trace(many, many.map((x) => p({ target: `${x.ip}:8080` })))
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    const eps = g.nodes.find((n) => n.kind === 'PODS')!
    expect(eps.moreRows).toBe(many.length - POD_ROW_MAX)
  })

  it('keeps a failing endpoint visible instead of averaging it into the aggregate', () => {
    const probes = many.map((x, i) => p({ target: `${x.ip}:8080`, ok: i !== 2, tone: i === 2 ? 'unhealthy' : 'healthy' }))
    const t = trace(many, probes)
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    const eps = g.nodes.find((n) => n.kind === 'PODS')!
    expect(eps.anomalies?.some((a) => a.mark === 'failed')).toBe(true)
    expect(eps.podRows!.some((r) => r.mark === 'failed')).toBe(true)
  })

  it('states the unprobed remainder — unprobed is not proven', () => {
    const t = trace(many, many.map((x) => p({ target: `${x.ip}:8080` })), 240)
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    const eps = g.nodes.find((n) => n.kind === 'PODS')!
    const omitted = eps.anomalies?.find((a) => a.mark === 'untested')
    expect(omitted?.text).toMatch(/not probed/)
  })

  it('counts NotReady endpoints as excluded from routing', () => {
    const withBad = [...many, pod('bad', false, '10.0.1.1', 'readiness failing')]
    const t = trace(withBad, many.map((x) => p({ target: `${x.ip}:8080` })))
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    const eps = g.nodes.find((n) => n.kind === 'PODS')!
    expect(eps.anomalies?.some((a) => a.mark === 'excluded')).toBe(true)
  })

  it('lists every endpoint as its own row below the aggregation threshold', () => {
    const few = [pod('a', true, '10.0.0.1'), pod('b', true, '10.0.0.2')]
    const t = trace(few, few.map((x) => p({ target: `${x.ip}:8080` })))
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    const eps = g.nodes.find((n) => n.kind === 'PODS')!
    expect(eps.kind).toBe('PODS')
    expect(eps.podRows).toHaveLength(2)
    expect(eps.moreRows).toBe(0)
  })
})

// The layout is computed rather than hand-placed precisely so that content of
// any length lays out without collisions. These pin that guarantee.
describe('layout collisions', () => {
  const overlaps = (a: { x: number; y: number; w: number; h: number }, b: { x: number; y: number; w: number; h: number }) =>
    a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h

  const scenarios: [string, Trace][] = [
    ['single ready pod', trace([pod('a', true, '10.0.0.1')], [p({})])],
    [
      'long names and many pods',
      trace(
        Array.from({ length: 4 }, (_, i) => pod(`a-very-long-workload-name-with-hash-${i}-abcdef123456`, i !== 3, `10.0.0.${i}`, 'readiness probe failing for 12m')),
        [p({})],
      ),
    ],
    ['no backends at all', trace([], [])],
    ['large sampled population', trace(Array.from({ length: 9 }, (_, i) => pod(`p${i}`, true, `10.0.0.${i}`)), [p({})], 240)],
  ]

  for (const [name, t] of scenarios) {
    for (const originId of ['incluster', 'apiserver'] as const) {
      it(`never overlaps two nodes — ${name}, from ${originId}`, () => {
        const g = buildGraph({ trace: t, route: route(), origin: pick(t, originId) })
        for (let i = 0; i < g.nodes.length; i++) {
          for (let j = i + 1; j < g.nodes.length; j++) {
            expect(overlaps(g.nodes[i], g.nodes[j]), `${g.nodes[i].id} overlaps ${g.nodes[j].id}`).toBe(false)
          }
        }
      })

      // The pill's BOX must clear every node, not merely its centre point - a
      // centre-only check passed while a wide pill overran its gutter onto the
      // node beside it.
      it(`keeps every edge pill's full box clear of every node — ${name}, from ${originId}`, () => {
        const g = buildGraph({ trace: t, route: route(), origin: pick(t, originId) })
        const PILL_H = 20
        for (const e of g.edges) {
          const box = { x: e.px - PILL_MAX_PX / 2, y: e.py - PILL_H / 2, w: PILL_MAX_PX, h: PILL_H }
          for (const n of g.nodes) {
            expect(overlaps(box, n), `pill ${e.id} overruns onto node ${n.id}`).toBe(false)
          }
        }
      })
    }
  }

  it('keeps every node inside the reported canvas', () => {
    const t = trace([pod('a', true, '10.0.0.1'), pod('b', false, '10.0.0.2', 'x')], [p({})])
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'apiserver') })
    for (const n of g.nodes) {
      expect(n.x + n.w).toBeLessThanOrEqual(g.canvas.w)
      expect(n.y + n.h).toBeLessThanOrEqual(g.canvas.h)
    }
  })

  it('truncates a long edge label and keeps the full text as a title', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({})])
    const long = 'an extremely long piece of probe evidence that would otherwise overrun its gutter entirely'
    const g = buildGraph({ trace: t, route: route({ evidence: long }), origin: pick(t, 'incluster') })
    const entry = g.edges.find((e) => e.id === 'e:origin-subject')!
    expect(entry.label.length).toBeLessThan(long.length)
    expect(entry.title).toBe(long)
  })
})

describe('publishNotReadyAddresses', () => {
  // With this set the dataplane routes to NotReady Pods, so none of them is
  // excluded. Honouring it in the subtitle but not in the rows told the user a
  // Pod was "never routed to" while it was in fact serving traffic.
  const publishing = (pods: PodStatus[], probes: ProbeResult[]) => {
    const t = trace(pods, probes)
    t.downstream[1].meta = { ready: pods.length, selected: pods.length, publishNotReadyAddresses: true }
    return t
  }

  it('does not call a NotReady endpoint excluded when not-ready addresses are published', () => {
    const t = publishing([pod('a', false, '10.0.0.1', 'readiness failing')], [p({ path: 'data', target: '10.0.0.1:8080', ok: true })])
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    const rows = g.nodes.find((n) => n.kind === 'PODS')!.podRows!
    expect(rows[0].mark).not.toBe('excluded')
    expect(rows[0].detail).toMatch(/sent traffic anyway/)
  })

  it('reports no excluded endpoints in the population anomalies', () => {
    const t = publishing([pod('a', false, '10.0.0.1', 'x'), pod('b', true, '10.0.0.2')], [p({ path: 'data', ok: true })])
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    expect(g.nodes.find((n) => n.kind === 'PODS')!.anomalies?.some((a) => a.mark === 'excluded')).toBe(false)
  })

  it('still excludes NotReady endpoints when the Service withholds them', () => {
    const t = trace([pod('a', false, '10.0.0.1', 'x')], [p({ path: 'data', ok: true })])
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    expect(g.nodes.find((n) => n.kind === 'PODS')!.podRows![0].mark).toBe('excluded')
  })

  it('never renders the published count as a readiness count', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({})])
    t.downstream[1].meta = { ready: 3, selected: 3, publishNotReadyAddresses: true }
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    const eps = g.nodes.find((n) => n.kind === 'PODS')!
    expect(eps.sub).toMatch(/not-ready Pods are sent traffic too/)
    // The "N of M ready" phrasing would be a lie here: `ready` is a PUBLISHED
    // count when the Service publishes not-ready addresses.
    expect(eps.sub).not.toMatch(/\d+ of \d+ ready/)
  })
})

// The backend produces a parsed cause + action per hop. The graph consumed them
// only to pick a dot colour, so the one sentence answering "what is wrong with
// this hop" sat behind a click.
describe('hop findings are carried onto the node', () => {
  const withFindings = (findings: Trace['downstream'][number]['findings']): Trace => ({
    subject: { kind: 'Service', name: 'checkout', namespace: 'store' },
    verdict: 'degraded',
    brokenAt: -1,
    upstreams: [],
    downstream: [{ resource: { kind: 'Service', name: 'checkout', namespace: 'store' }, edge: 'entry:Service', findings }],
  })

  it('prefers the parsed cause, which is written to be short', () => {
    const t = withFindings([
      { code: 'svc:port', severity: 'warning', message: 'Service targetPort 80->:3006 matches no port the ready pods declare', cause: 'Service targetPort likely wrong', action: 'Confirm the targetPort' },
    ])
    const n = buildGraph({ trace: t, route: route(), origin: pick(t, 'local') }).nodes.find((x) => x.kind === 'SERVICE')!
    expect(n.notes?.[0].text).toBe('Service targetPort likely wrong')
    // the long message and the remediation stay for the hover
    expect(n.notes?.[0].detail).toMatch(/matches no port/)
    expect(n.notes?.[0].detail).toMatch(/Confirm the targetPort/)
  })

  it('falls back to the message when no cause was parsed', () => {
    const t = withFindings([{ code: 'svc:nopods', severity: 'warning', message: 'Selector matches no pods' }])
    const n = buildGraph({ trace: t, route: route(), origin: pick(t, 'local') }).nodes.find((x) => x.kind === 'SERVICE')!
    expect(n.notes?.[0].text).toBe('Selector matches no pods')
  })

  it('leads with the worst severity and collapses the tail', () => {
    const t = withFindings([
      { code: 'a', severity: 'info', message: 'info one' },
      { code: 'b', severity: 'critical', message: 'critical one' },
      { code: 'c', severity: 'warning', message: 'warning one' },
    ])
    const n = buildGraph({ trace: t, route: route(), origin: pick(t, 'local') }).nodes.find((x) => x.kind === 'SERVICE')!
    expect(n.notes?.[0].text).toBe('critical one')
    expect(n.notes?.[1].text).toBe('warning one')
    expect(n.notes?.[2].text).toMatch(/\+1 more/)
  })

  it('reserves height for a note, so a node cannot grow into the row below', () => {
    const t = withFindings([{ code: 'x', severity: 'warning', message: 'a'.repeat(120) }])
    const withNote = buildGraph({ trace: t, route: route(), origin: pick(t, 'local') }).nodes.find((x) => x.kind === 'SERVICE')!
    const bare = buildGraph({ trace: withFindings([]), route: route(), origin: pick(withFindings([]), 'local') }).nodes.find((x) => x.kind === 'SERVICE')!
    expect(withNote.h).toBeGreaterThan(bare.h)
  })

  // The stronger invariant now that notes are headlines: a node's height is
  // bounded by the LAYOUT, not by how much the producer wrote. A message ten
  // times longer must not make the box ten times taller.
  it('does not grow with the length of the message', () => {
    const node = (msg: string) => {
      const t = withFindings([{ code: 'x', severity: 'warning', message: msg }])
      return buildGraph({ trace: t, route: route(), origin: pick(t, 'local') }).nodes.find((x) => x.kind === 'SERVICE')!.h
    }
    expect(node('a'.repeat(400))).toBe(node('a'.repeat(120)))
  })

  it('a clean hop carries no notes at all', () => {
    const t = withFindings([])
    const n = buildGraph({ trace: t, route: route(), origin: pick(t, 'local') }).nodes.find((x) => x.kind === 'SERVICE')!
    expect(n.notes ?? []).toHaveLength(0)
  })
})

// On a Gateway with several attached routes, every branch rendered identically:
// changing the selected scenario changed nothing on screen, so there was no way
// to tell which branch you were diagnosing.
describe('fan-out branch focus', () => {
  const routeHop = (name: string, host: string) => ({
    resource: { kind: 'HTTPRoute', name, namespace: 'edge' },
    edge: 'Gateway->HTTPRoute',
    findings: [],
    config: { hostnames: [host] },
  })
  const fanout = (): Trace => ({
    subject: { kind: 'Gateway', name: 'primary-gateway', namespace: 'edge' },
    verdict: 'degraded',
    brokenAt: -1,
    upstreams: [],
    downstream: [routeHop('shop', 'shop.example.com'), routeHop('checkout', 'checkout.example.com'), routeHop('admin', 'admin.example.com')],
  })
  const branch = (t: Trace, r: RouteResult, name: string) =>
    buildGraph({ trace: t, route: r, origin: pick(t, 'local') }).nodes.find((n) => n.name === name)!

  it('marks the branch serving the selected host, and dims its siblings', () => {
    const t = fanout()
    const r = route({ route: 'checkout.example.com/', target: 'checkout-api:80' })
    expect(branch(t, r, 'checkout').dim).toBeFalsy()
    expect(branch(t, r, 'shop').dim).toBe(true)
    expect(branch(t, r, 'admin').dim).toBe(true)
  })

  it('selecting a different scenario moves the focus', () => {
    const t = fanout()
    expect(branch(t, route({ route: 'shop.example.com/' }), 'shop').dim).toBeFalsy()
    expect(branch(t, route({ route: 'shop.example.com/' }), 'checkout').dim).toBe(true)
  })

  it('says WHY a sibling is dimmed rather than just greying it', () => {
    const t = fanout()
    expect(branch(t, route({ route: 'checkout.example.com/' }), 'shop').sub).toMatch(/not on the selected path/)
  })

  it('dims the edge into an off-path branch too', () => {
    const t = fanout()
    const g = buildGraph({ trace: t, route: route({ route: 'checkout.example.com/' }), origin: pick(t, 'local') })
    const off = g.edges.find((e) => e.id.includes('shop'))!
    const on = g.edges.find((e) => e.id.includes('checkout'))!
    expect(off.mark).toBe('excluded')
    expect(on.mark).toBe('config')
  })

  it('dims NOTHING when the scenario matches no branch', () => {
    // A graph with every branch greyed out says "none of this is relevant",
    // which is worse than saying nothing.
    const t = fanout()
    const g = buildGraph({ trace: t, route: route({ route: 'unknown.example.com/', target: 'nope:80' }), origin: pick(t, 'local') })
    expect(g.nodes.filter((n) => n.dim)).toHaveLength(0)
  })

  it('dims nothing when every branch matches', () => {
    const t = fanout()
    t.downstream = [routeHop('a', 'x.example.com'), routeHop('b', 'x.example.com')]
    const g = buildGraph({ trace: t, route: route({ route: 'x.example.com/' }), origin: pick(t, 'local') })
    expect(g.nodes.filter((n) => n.dim)).toHaveLength(0)
  })

  it('falls back to the backend NAME when the scenario names no host', () => {
    const t = fanout()
    const g = buildGraph({ trace: t, route: route({ route: ':80 -> 8080', target: 'checkout:80' }), origin: pick(t, 'local') })
    expect(g.nodes.find((n) => n.name === 'checkout')!.dim).toBeFalsy()
    expect(g.nodes.find((n) => n.name === 'shop')!.dim).toBe(true)
  })
})

describe('exception-first collapsing of a wide fan-out', () => {
  const rh = (name: string, host: string, findings: Trace['downstream'][number]['findings'] = []) => ({
    resource: { kind: 'HTTPRoute', name, namespace: 'edge' },
    edge: 'Gateway->HTTPRoute',
    findings,
    config: { hostnames: [host] },
  })
  const wide = (): Trace => ({
    subject: { kind: 'Gateway', name: 'gw', namespace: 'edge' },
    verdict: 'degraded',
    brokenAt: -1,
    upstreams: [],
    downstream: [
      rh('shop', 'shop.example.com'),
      rh('checkout', 'checkout.example.com'),
      rh('account', 'account.example.com'),
      rh('admin', 'admin.example.com', [{ code: 'x', severity: 'warning', message: 'Not accepted by the parent Gateway' }]),
      rh('docs', 'docs.example.com'),
      rh('status', 'status.example.com'),
      rh('api', 'api.example.com'),
    ],
  })

  it('keeps the selected branch and anything with findings; collapses the quiet rest', () => {
    const g = buildGraph({ trace: wide(), route: route({ route: 'checkout.example.com/' }), origin: pick(wide(), 'local') })
    const names = g.nodes.map((n) => n.name)
    expect(names).toContain('checkout') // selected
    expect(names).toContain('admin') // has a finding
    expect(names).toContain('5 more routes') // shop, account, docs, status, api
    expect(names).not.toContain('shop')
  })

  it('collapses by relevance, never by position', () => {
    // "api" sorts last but is the selected branch, so it must survive.
    const g = buildGraph({ trace: wide(), route: route({ route: 'api.example.com/' }), origin: pick(wide(), 'local') })
    expect(g.nodes.map((n) => n.name)).toContain('api')
  })

  it('says the collapsed rows are quiet, not truncated', () => {
    const g = buildGraph({ trace: wide(), route: route({ route: 'checkout.example.com/' }), origin: pick(wide(), 'local') })
    const more = g.nodes.find((n) => n.id === 'collapsed:backends')!
    expect(more.sub).toMatch(/nothing found/)
    expect(g.edges.find((e) => e.id === 'e:subject-collapsed')?.mark).toBe('config')
  })

  it('does not collapse a fan-out small enough to read whole', () => {
    const t = wide()
    t.downstream = t.downstream.slice(0, 3)
    const g = buildGraph({ trace: t, route: route({ route: 'checkout.example.com/' }), origin: pick(t, 'local') })
    expect(g.nodes.some((n) => n.id === 'collapsed:backends')).toBe(false)
    expect(g.nodes.map((n) => n.name)).toContain('shop')
  })

  it('no node overlaps once collapsed', () => {
    const g = buildGraph({ trace: wide(), route: route({ route: 'checkout.example.com/' }), origin: pick(wide(), 'local') })
    for (let i = 0; i < g.nodes.length; i++)
      for (let j = i + 1; j < g.nodes.length; j++) {
        const a = g.nodes[i], b = g.nodes[j]
        const hit = a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h
        expect(hit).toBe(false)
      }
  })
})

// End-to-end proof that the misattribution is gone: one trace, one route, two
// vantages that disagree - each origin must render its OWN truth.
describe('the graph reads the selected origin\'s own result', () => {
  const disagreeing = (): RouteResult => ({
    route: 'checkout.example.com/',
    target: 'checkout:80',
    // the merged rollup says the whole route failed...
    outcome: 'unreachable',
    confidence: 'real',
    evidence: 'connection refused',
    // ...while in-cluster actually succeeded.
    byVantage: [
      { vantage: 'in-cluster', path: 'data', outcome: 'verified', confidence: 'real', evidence: 'HTTP 200' },
      { vantage: 'local', path: 'data', outcome: 'unreachable', confidence: 'real', evidence: 'connection refused' },
    ],
  })

  const edgeMark = (originId: 'incluster' | 'local') => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({ vantage: originId === 'incluster' ? 'in-cluster' : 'local', path: 'data', ok: true })])
    const g = buildGraph({ trace: t, route: disagreeing(), origin: pick(t, originId) })
    return g.edges.find((e) => e.id === 'e:origin-subject')!.mark
  }

  it('shows the in-cluster vantage as proved even though the rollup says unreachable', () => {
    expect(edgeMark('incluster')).toBe('proved')
  })

  it('shows the laptop vantage as failed', () => {
    expect(edgeMark('local')).toBe('failed')
  })

  it('does not mark Pods "blocked by an earlier failure" for the vantage that got through', () => {
    // deliveryBlocked previously followed the merged outcome, so a working
    // vantage's Pods were labelled as never-reached.
    const t = trace([pod('a', true, '10.0.0.1')], [p({ vantage: 'in-cluster', path: 'data', ok: true })])
    const g = buildGraph({ trace: t, route: disagreeing(), origin: pick(t, 'incluster') })
    const rows = g.nodes.find((n) => n.kind === 'PODS')!.podRows!
    expect(rows[0].mark).not.toBe('blocked')
  })
})

describe('one route\'s evidence never spreads across sibling backends', () => {
  // A multi-backend Route: two backends, each with its own Pods hop - the shape
  // the producer fan-out emits and the shape that makes cross-attribution
  // possible in the first place.
  const multiBackend = (): Trace => ({
    subject: { kind: 'HTTPRoute', name: 'shop-route', namespace: 'store' },
    verdict: 'degraded',
    brokenAt: -1,
    upstreams: [{ resource: { kind: 'Gateway', name: 'gw', namespace: 'infra' }, edge: 'gateway->route', findings: [] }],
    downstream: [
      { resource: { kind: 'HTTPRoute', name: 'shop-route', namespace: 'store' }, edge: 'entry:HTTPRoute', findings: [] },
      { resource: { kind: 'Service', name: 'web', namespace: 'store' }, edge: 'HTTPRoute->Service', findings: [], config: { clusterIP: '10.96.0.1' } },
      {
        resource: { kind: 'Pods', name: '', namespace: 'store' },
        edge: 'Service->Pods', findings: [], meta: { ready: 1, selected: 1 },
        config: { pods: [pod('w1', true, '10.0.0.1')], podTotal: 1 },
      },
      { resource: { kind: 'Service', name: 'api', namespace: 'store' }, edge: 'HTTPRoute->Service', findings: [], config: { clusterIP: '10.96.0.2' } },
      {
        resource: { kind: 'Pods', name: '', namespace: 'store' },
        edge: 'Service->Pods', findings: [], meta: { ready: 1, selected: 1 },
        config: { pods: [pod('a1', true, '10.0.0.2')], podTotal: 1 },
      },
    ],
  })

  const brokenOnWeb = (): RouteResult => ({
    route: 'shop.example.com/web', target: 'web:80', targetNamespace: 'store',
    outcome: 'unreachable', confidence: 'real',
    byVantage: [{ vantage: 'in-cluster', path: 'data', outcome: 'unreachable', failedBoundary: 'service-routing' }],
  })

  // The producer establishes a boundary for ONE backend. Painting it on every
  // Service->Pods edge condemns a sibling whose pods were never probed.
  it('paints "breaks here" only on the branch that established it', () => {
    const t = multiBackend()
    const g = buildGraph({ trace: t, route: brokenOnWeb(), origin: pick(t, 'incluster') })
    const broken = g.edges.filter((e) => e.label === 'breaks here')
    expect(broken).toHaveLength(1)
    expect(broken[0].id).toBe('e:n:Service/store/web::pods')
  })

  // Same leak, other edge: a bypassing origin dials the route's backend, not
  // every backend the Route happens to declare.
  it('lands the bypass edge only on the selected route\'s backend', () => {
    const t = multiBackend()
    const g = buildGraph({ trace: t, route: brokenOnWeb(), origin: pick(t, 'incluster') })
    const dialled = g.edges.filter((e) => e.label === 'dialled directly')
    expect(dialled).toHaveLength(1)
    expect(dialled[0].id).toBe('e:origin-n:Service/store/web')
  })

  it('colours nothing when the owning branch cannot be identified', () => {
    const t = multiBackend()
    const orphan: RouteResult = {
      route: 'shop.example.com/gone', target: 'gone:80', targetNamespace: 'store',
      outcome: 'unreachable', confidence: 'real',
      byVantage: [{ vantage: 'in-cluster', path: 'data', outcome: 'unreachable', failedBoundary: 'service-routing' }],
    }
    const g = buildGraph({ trace: t, route: orphan, origin: pick(t, 'incluster') })
    expect(g.edges.some((e) => e.label === 'breaks here')).toBe(false)
  })
})

describe('evidence written for a hover is shortened for a pill', () => {
  // The producer writes full explanations. Rendered whole, the edge pill
  // truncated mid-word to "Connectior refused...." and the pod row ran outside
  // its node box.
  it('keeps only the first sentence', () => {
    expect(shortEvidence('Connection refused. Nothing is listening on the port.')).toBe('Connection refused')
  })

  it('drops a trailing full stop', () => {
    expect(shortEvidence('Connection refused.')).toBe('Connection refused')
  })

  // "·" separates parts of ONE fact, not sentences - cutting there would throw
  // away the latency that makes the row worth reading.
  it('leaves a dot-separated detail intact', () => {
    expect(shortEvidence('HTTP 200 · 41 ms')).toBe('HTTP 200 · 41 ms')
  })

  it('leaves a decimal point alone', () => {
    expect(shortEvidence('slow · 1.4 s')).toBe('slow · 1.4 s')
  })

  it('is safe on empty input', () => {
    expect(shortEvidence(undefined)).toBe('')
  })

  it('shortens the edge pill but keeps the full text on the hover', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({ vantage: 'in-cluster', path: 'data', ok: false, tone: 'unhealthy' })])
    const long = 'Connection refused. Nothing is listening on the port.'
    const g = buildGraph({
      trace: t,
      route: route({ outcome: 'unreachable', confidence: 'real', evidence: long }),
      origin: pick(t, 'incluster'),
    })
    const edge = g.edges.find((e) => e.id === 'e:origin-subject')!
    expect(edge.label).toBe('Connection refused')
    // The explanation is moved to the hover, not thrown away.
    expect(edge.title).toBe(long)
  })
})

describe('the workload behind the Service appears in the graph', () => {
  // The PRODUCER resolves the workload from the Pods' owner chain and sends it
  // as the pods hop's config.workload. The UI never substitutes the workload
  // the reader happened to open: a nil from the producer means the Pods have no
  // single owner, and drawing the opened workload there would claim another
  // workload's Pods for it.
  const traceWithWorkload = (name = 'external-secrets-webhook') => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({})])
    t.downstream![1].config!.workload = { kind: 'Deployment', name, namespace: 'store' }
    return t
  }
  const withWorkload = () => {
    const t = traceWithWorkload()
    return buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
  }

  it('draws it between the Service and its Pods', () => {
    const g = withWorkload()
    const svc = g.nodes.find((n) => n.kind === 'SERVICE')!
    const wl = g.nodes.find((n) => n.kind === 'DEPLOYMENT')!
    const pods = g.nodes.find((n) => n.kind === 'PODS')!
    expect(wl.name).toBe('external-secrets-webhook')
    expect(svc.x).toBeLessThan(wl.x)
    expect(wl.x).toBeLessThan(pods.x)
  })

  // It routes nothing, so neither of its edges may claim observed traffic.
  it('declares both its edges rather than claiming traffic', () => {
    const g = withWorkload()
    const into = g.edges.find((e) => e.id.endsWith('-workload'))!
    const outOf = g.edges.find((e) => e.label === 'runs')!
    expect(into.mark).toBe('config')
    expect(outOf.mark).toBe('config')
  })

  // The producer localises the break to the SERVICE's own routing. Inserting a
  // node after the Service must not slide that blame onto the workload, which
  // routes nothing.
  it('a Service-routing break is a boundary span, and no node is blamed', () => {
    const t = traceWithWorkload('web')
    const broken = route({
      outcome: 'unreachable',
      confidence: 'real',
      byVantage: [{ vantage: 'in-cluster', path: 'data', outcome: 'unreachable', failedBoundary: 'service-routing' }],
    })
    const g = buildGraph({
      trace: t,
      route: broken,
      origin: pick(t, 'incluster'),
    })
    // One observed break on the edge leaving the Service...
    const breaks = g.edges.filter((e) => e.label === 'breaks here')
    expect(breaks).toHaveLength(1)
    expect(breaks[0].id).toMatch(/-workload$/)
    expect(breaks[0].boundary).toBe('start')
    // ...continued as the SAME break across the span the workload sits inside -
    // failed colour, marked continuation, no pill (empty label), never a second
    // failed observation.
    const cont = g.edges.find((e) => e.boundary === 'continuation')!
    expect(cont.mark).toBe('failed')
    expect(cont.label).toBe('')
    // The break anchors to the Service's EXIT. A halo may sit on the Service
    // itself (the route edge INTO it failed) but never on the workload or the
    // Pods - that would blame a node for its segment's failure.
    expect(g.breakAtExitOf).toBe('n:Service/store/shop')
    expect([undefined, g.breakAtExitOf]).toContain(g.breakNodeId)
    expect(g.breakNodeId).not.toBe('n:workload')
    expect(g.nonNetworkNodeIds).toContain('n:workload')
  })

  it('is absent when no workload is in scope', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({})])
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    expect(g.nodes.some((n) => n.kind === 'DEPLOYMENT')).toBe(false)
    // The count lives on the name line; the sub stays silent when it would
    // only repeat it.
    expect(g.nodes.find((n) => n.kind === 'PODS')!.name).toMatch(/eligible/)
  })
})

describe('only the vantage being exercised reads as running', () => {
  // The running flag used to reach every origin, so one probe run animated all
  // of them - the same "one vantage's state under another's name" this view
  // exists to prevent, in motion.
  const t = () => trace([pod('a', true, '10.0.0.1')], [p({ vantage: 'local', path: 'data', ok: true })])

  it('marks the in-cluster edge running and leaves the others alone', () => {
    const tr = t()
    const g = buildGraph({
      trace: tr,
      route: route(),
      origin: pick(tr, 'incluster'),
      origins: buildOrigins(tr),
      running: true,
    })
    const originEdges = g.edges.filter((e) => e.id.startsWith('e:origin'))
    expect(originEdges.some((e) => e.mark === 'running')).toBe(true)
    // The laptop probed and succeeded; a run elsewhere must not repaint it.
    const local = g.edges.find((e) => e.id.includes('local'))
    if (local) expect(local.mark).not.toBe('running')
  })

  it('marks nothing running when no test is in flight', () => {
    const tr = t()
    const g = buildGraph({ trace: tr, route: route(), origin: pick(tr, 'incluster'), origins: buildOrigins(tr) })
    expect(g.edges.some((e) => e.mark === 'running')).toBe(false)
  })
})

describe('a finding on a node is a headline, not the whole message', () => {
  // Rendered in full this was nine lines inside a graph node, burying the path
  // the node sits on.
  const gatewayMsg =
    "Accepted: NoMatchingListenerHostname - Gateway primary-gateway-system/primary-gateway listeners http, https: There were no hostname intersections between the HTTPRoute and this parent ref's Listener(s)."

  it('translates a known condition reason into plain language', () => {
    // "Accepted: NoMatchingListenerHostname" is precise and user-hostile - it
    // reads as if the route WAS accepted. Known Gateway API reasons get plain
    // words; the raw message stays on the hover.
    expect(noteHeadline(gatewayMsg)).toBe('Not attached: no listener matches its hosts')
  })

  // A colon usually introduces the very thing worth naming, so it is not a cut
  // point - cutting there would leave a bare "Accepted". An UNKNOWN reason
  // passes through untranslated rather than being guessed at.
  it('does not cut at a colon, and passes unknown reasons through', () => {
    expect(noteHeadline('Accepted: SomeVendorSpecificReason')).toBe('Accepted: SomeVendorSpecificReason')
  })

  it('cuts at a sentence break too', () => {
    expect(noteHeadline('Connection refused. Nothing is listening on the port.')).toBe('Connection refused')
  })

  it('truncates a headline with no break at all', () => {
    const out = noteHeadline('x'.repeat(120))
    expect(out.length).toBeLessThanOrEqual(46)
    expect(out.endsWith('…')).toBe(true)
  })

  it('leaves a short message alone', () => {
    expect(noteHeadline('No ready endpoints')).toBe('No ready endpoints')
  })

  it('puts the whole message on the hover', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({})])
    t.downstream[0].findings = [{ code: 'gw:x', severity: 'warning', message: gatewayMsg }]
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    const note = g.nodes.find((n) => n.kind === 'SERVICE')!.notes![0]
    expect(note.text).toBe('Not attached: no listener matches its hosts')
    expect(note.detail).toBe(gatewayMsg)
  })
})

describe('the selected vantage is a real scope boundary', () => {
  // Every drawn vantage contributes edges. Reading the break off all of them let
  // a workstation failure order the report while the in-cluster vantage the
  // reader had selected was succeeding.
  it('does not take the break from another vantage\'s failed edge', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [
      p({ vantage: 'in-cluster', path: 'data', ok: true, tone: 'healthy' }),
      p({ vantage: 'local', path: 'data', ok: false, tone: 'unhealthy' }),
    ])
    const good = route({
      outcome: 'verified',
      confidence: 'real',
      byVantage: [
        { vantage: 'in-cluster', path: 'data', outcome: 'verified' },
        { vantage: 'local', path: 'data', outcome: 'unreachable' },
      ],
    })
    const g = buildGraph({ trace: t, route: good, origin: pick(t, 'incluster'), origins: buildOrigins(t) })
    expect(g.breakNodeId).toBeUndefined()
  })

  it('still finds the break on the selected vantage\'s own edge', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({ vantage: 'in-cluster', path: 'data', ok: false, tone: 'unhealthy' })])
    const bad = route({ outcome: 'unreachable', confidence: 'real' })
    const g = buildGraph({ trace: t, route: bad, origin: pick(t, 'incluster'), origins: buildOrigins(t) })
    expect(g.breakNodeId).toBeDefined()
  })

  // Sorting rendered nodes by position turned PARALLEL backends into what read
  // as a serial chain, and swept in branches the route never touches.
  it('reports the route\'s own chain in traversal order - the workload is display, not journey', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({})])
    t.downstream![1].config!.workload = { kind: 'Deployment', name: 'web', namespace: 'store' }
    const g = buildGraph({
      trace: t,
      route: route(),
      origin: pick(t, 'incluster'),
    })
    const kinds = g.pathNodeIds.map((id) => g.nodes.find((n) => n.id === id)!.kind)
    // The Deployment renders inline but is NOT a hop the request traverses -
    // it rides in `interleave` for display order instead.
    expect(kinds).toEqual(['SERVICE', 'PODS'])
    expect(g.interleave).toEqual([{ id: 'n:workload', afterId: 'n:Service/store/shop' }])
  })

  it('emits no duplicates and only nodes that exist', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({})])
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    expect(new Set(g.pathNodeIds).size).toBe(g.pathNodeIds.length)
    for (const id of g.pathNodeIds) expect(g.nodes.some((n) => n.id === id)).toBe(true)
  })
})

describe('only the relay may downgrade a proved result', () => {
  // A laptop is outside the cluster, but a request it sends to a real address
  // travels the real network - "proxied" means "bypassed the dataplane", which
  // is a claim about the API-server relay and nothing else.
  it('keeps a laptop success as proved', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({ vantage: 'local', path: 'data', ok: true, tone: 'healthy' })])
    const g = buildGraph({ trace: t, route: route({ outcome: 'verified', confidence: 'real' }), origin: pick(t, 'local') })
    expect(g.edges.find((e) => e.id === 'e:origin-subject')!.mark).toBe('proved')
  })

  it('still downgrades the API-server relay', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({ path: 'apiserver', ok: true, tone: 'healthy' })])
    const g = buildGraph({ trace: t, route: route({ outcome: 'verified', confidence: 'real' }), origin: pick(t, 'apiserver') })
    expect(g.edges.find((e) => e.id === 'e:origin-subject')!.mark).toBe('proxied')
  })
})

// Re-derives an edge's cubic from its rendered path so a test can walk the
// SAME curve the browser draws, rather than trusting a single reported point.
function edgeCubic(e: { d: string }) {
  const m = e.d.match(/M([\d.-]+),([\d.-]+) C([\d.-]+),([\d.-]+) ([\d.-]+),([\d.-]+) ([\d.-]+),([\d.-]+)/)!
  const n = m.slice(1).map(Number)
  return { p0: [n[0], n[1]], p1: [n[2], n[3]], p2: [n[4], n[5]], p3: [n[6], n[7]] } as const
}
function cubicPoint(c: ReturnType<typeof edgeCubic>, t: number) {
  const u = 1 - t
  const f = (a: number, b: number, cc: number, d: number) => u * u * u * a + 3 * u * u * t * b + 3 * u * t * t * cc + t * t * t * d
  return { x: f(c.p0[0], c.p1[0], c.p2[0], c.p3[0]), y: f(c.p0[1], c.p1[1], c.p2[1], c.p3[1]) }
}

describe('an edge that skips a column routes around it', () => {
  // The in-cluster probe bypasses an HTTPRoute, so its edge spans from the
  // origin to the BACKEND - straight through the Route node sitting between
  // them, parking its pill on top of the very node it does not touch.
  const routeTrace = (): Trace => ({
    subject: { kind: 'HTTPRoute', name: 'shop-route', namespace: 'store' },
    verdict: 'healthy',
    brokenAt: -1,
    upstreams: [{ resource: { kind: 'Gateway', name: 'gw', namespace: 'infra' }, edge: 'gateway->route', findings: [] }],
    downstream: [
      {
        resource: { kind: 'HTTPRoute', name: 'shop-route', namespace: 'store' },
        edge: 'entry:HTTPRoute',
        // A REAL route carries findings, which make the node tall. A bare node
        // is short enough that a too-shallow detour still clears it - which is
        // exactly how a broken detour passed its first test.
        findings: [
          {
            code: 'gw:no-listener',
            severity: 'warning',
            message:
              "Accepted: NoMatchingListenerHostname - Gateway primary-gateway-system/primary-gateway listeners http, https: There were no hostname intersections between the HTTPRoute and this parent ref's Listener(s).",
          },
        ],
      },
      { resource: { kind: 'Service', name: 'shop', namespace: 'store' }, edge: 'HTTPRoute->Service', findings: [], config: { clusterIP: '10.96.0.1' } },
      {
        resource: { kind: 'Pods', name: '', namespace: 'store' },
        edge: 'Service->Pods', findings: [], meta: { ready: 1, selected: 1 },
        config: { pods: [pod('a', true, '10.0.0.1')], podTotal: 1 },
      },
    ],
  })

  const bypassEdge = () => {
    const t = routeTrace()
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster'), origins: buildOrigins(t) })
    const e = g.edges.find((x) => x.id === 'e:origin-n:Service/store/shop')!
    return { g, e }
  }

  it('clears the node it passes, instead of crossing it', () => {
    const { g, e } = bypassEdge()
    const routeNode = g.nodes.find((n) => n.kind === 'HTTPROUTE')!
    // The pill rides the curve, so its height is a fair probe of where the line
    // actually goes at the point it crosses that column.
    expect(e.py).toBeGreaterThan(routeNode.y + routeNode.h)
  })

  // The obstacle's HEIGHT is what broke the first attempt: a cubic reaches only
  // ~3/4 of the way to its control points, so clearance has to be solved for,
  // not assumed.
  it('clears a tall node by as much as a short one', () => {
    const { g, e } = bypassEdge()
    const routeNode = g.nodes.find((n) => n.kind === 'HTTPROUTE')!
    expect(routeNode.h).toBeGreaterThan(90)
    expect(e.py - (routeNode.y + routeNode.h)).toBeGreaterThan(8)
  })

  // The API-server capsule sits in the control lane, well ABOVE the route it
  // bypasses. Forcing every detour downward sent it swooping under the node and
  // back up to the Service - longer, and it reads as a detour to nowhere.
  it('routes an edge coming from above OVER the node, not under it', () => {
    const t = routeTrace()
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'apiserver'), origins: buildOrigins(t) })
    const e = g.edges.find((x) => x.id === 'e:origin-n:Service/store/shop')!
    const routeNode = g.nodes.find((n) => n.kind === 'HTTPROUTE')!
    expect(e.py).toBeLessThan(routeNode.y)
  })

  it('still routes an edge coming from below UNDER the node', () => {
    const { g, e } = bypassEdge()
    const routeNode = g.nodes.find((n) => n.kind === 'HTTPROUTE')!
    expect(e.py).toBeGreaterThan(routeNode.y + routeNode.h)
  })

  // The pill rides the curve's midpoint, so it can sit clear while the LINE
  // still grazes the node's far corner - an asymmetric edge has already
  // descended by the time it crosses there. Sample the whole span.
  it('clears the node along its entire width, not just at the midpoint', () => {
    const t = routeTrace()
    for (const originId of ['apiserver', 'incluster'] as const) {
      const g = buildGraph({ trace: t, route: route(), origin: pick(t, originId), origins: buildOrigins(t) })
      const e = g.edges.find((x) => x.id === 'e:origin-n:Service/store/shop')!
      const n = g.nodes.find((x) => x.kind === 'HTTPROUTE')!
      const c = edgeCubic(e)
      for (let i = 1; i < 20; i++) {
        const pt = cubicPoint(c, i / 20)
        if (pt.x < n.x || pt.x > n.x + n.w) continue
        const clears = pt.y < n.y || pt.y > n.y + n.h
        expect(clears).toBe(true)
      }
    }
  })

  it('reserves canvas height for the detour', () => {
    const { g, e } = bypassEdge()
    expect(g.canvas.h).toBeGreaterThanOrEqual(e.py)
  })

  // Only edges that actually skip something detour; an ordinary hop-to-hop edge
  // must stay where it was.
  it('leaves an adjacent-column edge alone', () => {
    const t = trace([pod('a', true, '10.0.0.1')], [p({})])
    const g = buildGraph({ trace: t, route: route(), origin: pick(t, 'incluster') })
    const e = g.edges.find((x) => x.label === 'selects')!
    const pods = g.nodes.find((n) => n.kind === 'PODS')!
    expect(e.py).toBeLessThan(pods.y + pods.h)
  })
})

describe('an entry hop reports its own result, not the route rollup', () => {
  // The route is built from the BACKEND's probes. A laptop that dialled a public
  // Ingress and got an answer therefore had no route row at all, and the entry
  // edge - drawn from the rollup - read "not tested" while the trace held six
  // successful dials against that very Ingress.
  const withIngress = (): Trace => ({
    subject: { kind: 'Service', name: 'web', namespace: 'prod' },
    verdict: 'healthy',
    brokenAt: -1,
    upstreams: [
      {
        resource: { kind: 'Ingress', name: 'web', namespace: 'prod' },
        edge: 'ingress->service',
        findings: [],
        config: { hostnames: ['web.example.com'], addresses: ['34.23.121.209'] },
        probes: [
          { layer: 'tcp', target: '34.23.121.209:443', vantage: 'local', ok: true },
          { layer: 'http', target: 'web.example.com', vantage: 'local', ok: true, tone: 'reached', detail: 'HTTP 404 · reached' },
        ],
      },
    ],
    downstream: [
      {
        resource: { kind: 'Service', name: 'web', namespace: 'prod' },
        edge: 'service',
        findings: [],
        config: { clusterIP: '10.96.0.1' },
        // From a laptop the ClusterIP is only reachable through the relay.
        probes: [{ layer: 'http', target: 'web:80', vantage: 'local', path: 'apiserver', ok: true, tone: 'healthy', detail: 'HTTP 404' }],
      },
      {
        resource: { kind: 'Pods', name: '', namespace: 'prod' },
        edge: 'service->pods', findings: [], meta: { ready: 1, selected: 1 },
        config: { pods: [pod('a', true, '10.0.0.1')], podTotal: 1 },
      },
    ],
  })

  // The route carries only the relayed backend result, so it has no local/data row.
  const routeViaRelay = () =>
    route({
      outcome: 'reached',
      confidence: 'indirect',
      byVantage: [{ vantage: 'local', path: 'apiserver', outcome: 'reached' }],
    })

  it('marks the entry edge from the entry\'s own dials', () => {
    const t = withIngress()
    const g = buildGraph({ trace: t, route: routeViaRelay(), origin: pick(t, 'local'), origins: buildOrigins(t) })
    const e = g.edges.find((x) => x.id === 'e:origin-n:Ingress/prod/web')!
    // The dial ANSWERED with a 404 (tone 'reached') - real evidence the entry
    // serves, not a clean pass. It must not draw the same solid green as a 2xx:
    // that painted 'HTTP 404 · reached' as healthy at a glance.
    expect(e.mark).toBe('answered')
    expect(e.label).not.toMatch(/not tested/i)
  })

  it('does not credit the entry to a vantage that never dialled it', () => {
    const t = withIngress()
    const g = buildGraph({ trace: t, route: routeViaRelay(), origin: pick(t, 'incluster'), origins: buildOrigins(t) })
    const e = g.edges.find((x) => x.id === 'e:origin-n:Ingress/prod/web')
    if (e) expect(e.mark).not.toBe('proved')
  })

  // A proved glyph beside the words "not tested" is the contradiction this
  // whole change exists to remove; the pair must always agree.
  it('never pairs an evidence glyph with the words not tested', () => {
    const t = withIngress()
    const g = buildGraph({ trace: t, route: routeViaRelay(), origin: pick(t, 'local'), origins: buildOrigins(t) })
    const evidence: Mark[] = ['proved', 'answered', 'proxied', 'failed', 'slow']
    for (const e of g.edges) {
      if (evidence.includes(e.mark)) expect(e.label).not.toMatch(/^not tested$/i)
    }
    const capsule = g.nodes.find((n) => n.isOrigin)!
    const verdict = capsule.anomalies?.[0]
    if (verdict && evidence.includes(verdict.mark)) expect(verdict.text).not.toMatch(/^not tested$/i)
  })

  // A hop this vantage never dialled is untested AT THAT HOP, whatever it
  // managed elsewhere - an HTTPRoute has no address to dial at all.
  it('marks an undialled hop untested even when the vantage reached others', () => {
    const t = withIngress()
    t.upstreams.push({
      resource: { kind: 'HTTPRoute', name: 'web', namespace: 'prod' },
      edge: 'route->service',
      findings: [],
      config: { hostnames: ['web.example.com'] },
      probes: [{ layer: 'tcp', target: '', vantage: 'local', ok: false, skipped: true, reason: 'route has no own address' }],
    })
    const g = buildGraph({ trace: t, route: routeViaRelay(), origin: pick(t, 'local'), origins: buildOrigins(t) })
    const e = g.edges.find((x) => x.id === 'e:origin-n:HTTPRoute/prod/web')!
    expect(e.mark).toBe('untested')
    expect(e.label).toMatch(/not tested/i)
  })

  it('prefers the most specific layer that answered', () => {
    const t = withIngress()
    const ev = hopEvidenceFor(t.upstreams[0], buildOrigins(t).find((o) => o.id === 'local')!)!
    expect(ev.title).toMatch(/HTTP 404/)
  })
})

describe('an all-skipped origin never claims a network verdict', () => {
  it('capsule and edge share one label, and it is not "not routable"', () => {
    const o = { id: 'apiserver', mark: 'blocked' } as never
    expect(originNoEvidenceLabel(o)).toBe('couldn’t test')
    const trace = {
      subject: { kind: 'Service', name: 'shop', namespace: 'store' },
      verdict: 'unknown',
      brokenAt: -1,
      upstreams: [],
      routes: [],
      downstream: [
        {
          resource: { kind: 'Service', name: 'shop', namespace: 'store' },
          edge: 'service',
          findings: [],
          probes: [{ layer: 'http', target: 'shop:443', vantage: 'local', path: 'apiserver', ok: false, skipped: true, reason: 'x' }],
        },
      ],
    } as never
    const ev = originEntryEvidence(trace, { route: 'r', target: 'shop:443', outcome: 'not-tested' } as never, {
      id: 'apiserver',
      mark: 'blocked',
      lane: 'control',
    } as never)
    expect(ev.label).toBe('couldn’t test')
  })
  it('an attempted in-cluster run that could not start keeps its own words', () => {
    expect(originNoEvidenceLabel({ id: 'incluster', mark: 'blocked', unavailable: 'image pull failed' } as never)).toBe('test couldn’t run')
  })
})

describe('entry-problem rows can address a graph node', () => {
  it('the node id an EntryProblem resolves to matches the graph upstream node id', () => {
    const t = {
      subject: { kind: 'Service', name: 'shop', namespace: 'store' },
      verdict: 'degraded', brokenAt: -1,
      upstreams: [{ resource: { kind: 'HTTPRoute', name: 'shop', namespace: 'store' }, edge: 'httproute->service', findings: [] }],
      downstream: [{ resource: { kind: 'Service', name: 'shop', namespace: 'store' }, edge: 'service', findings: [] }],
      routes: [{ route: 'shop', target: 'shop:80', outcome: 'reached', confidence: 'real' }],
    } as never
    const g = buildGraph({ trace: t, route: route({ target: 'shop:80', outcome: 'reached' }), origin: buildOrigins(t).find((o) => o.id === 'apiserver')! })
    // Mirrors the id the EntryProblems row builds from EntryProblem.resource.
    const fromProblem = 'n:HTTPRoute/store/shop'
    expect(g.nodes.some((n) => n.id === fromProblem)).toBe(true)
  })
})
