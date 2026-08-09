import { describe, it, expect } from 'vitest'
import { traceToSubgraph, outcomeToStatus } from './traceToSubgraph'
import { reachVerdict } from './reachVerdict'
import type { Trace } from './types'

const baseTrace = (o: Partial<Trace>): Trace => ({
  subject: { kind: 'Service', namespace: 'prod', name: 'echo' },
  upstreams: [],
  downstream: [],
  verdict: 'healthy',
  brokenAt: -1,
  ...o,
})

describe('outcomeToStatus - node = own health, NOT vantage', () => {
  it('verified over REAL traffic is healthy (green)', () => {
    expect(outcomeToStatus('verified', 'real')).toBe('healthy')
  })
  it('verified via the API proxy (indirect) is STILL healthy - the resource responded; the vantage caveat lives on the edge', () => {
    expect(outcomeToStatus('verified', 'indirect')).toBe('healthy')
  })
  it('reached is healthy (it responded); server-error is degraded (app 5xx); unreachable is unhealthy; not-tested is unknown', () => {
    expect(outcomeToStatus('reached', 'indirect')).toBe('healthy')
    expect(outcomeToStatus('server-error', 'real')).toBe('degraded')
    expect(outcomeToStatus('unreachable', 'real')).toBe('unhealthy')
    expect(outcomeToStatus('not-tested')).toBe('unknown')
  })
})

describe('traceToSubgraph', () => {
  it('maps 2 upstream ingresses + service + pods into nodes + routes-to edges', () => {
    const t = baseTrace({
      upstreams: [
        { resource: { kind: 'Ingress', namespace: 'prod', name: 'a' }, edge: 'Ingress->Service', findings: [] },
        { resource: { kind: 'Ingress', namespace: 'prod', name: 'b' }, edge: 'Ingress->Service', findings: [] },
      ],
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [] },
      ],
      routes: [{ route: 'echo', target: 'echo:80', outcome: 'verified', confidence: 'real' }],
    })
    const topo = traceToSubgraph(t)
    // Ingress a, Ingress b, Service echo, Pods = 4 nodes
    expect(topo.nodes).toHaveLength(4)
    const svc = topo.nodes.find((n) => n.kind === 'Service')
    expect(svc?.status).toBe('healthy') // verified+real
    // each ingress → service, service → pods = 3 edges, all routes-to
    expect(topo.edges).toHaveLength(3)
    expect(topo.edges.every((e) => e.type === 'routes-to')).toBe(true)
    const svcId = svc!.id
    expect(topo.edges.filter((e) => e.target === svcId)).toHaveLength(2) // two ingresses converge
  })

  it('a proxy-only reached service node stays HEALTHY - vantage lives on the edge, not the node', () => {
    const t = baseTrace({
      verdict: 'unknown',
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [] },
      ],
      routes: [{ route: 'echo', target: 'echo:80', outcome: 'verified', confidence: 'indirect' }],
    })
    const topo = traceToSubgraph(t)
    const svc = topo.nodes.find((n) => n.kind === 'Service')
    // The service RESPONDED (just via the proxy) → its own health is fine → green.
    // The "real in-cluster traffic not confirmed" caveat belongs to the PATH (the
    // edge style + the verdict), never to recoloring the node amber.
    expect(svc?.status).toBe('healthy')
    expect(svc?.status).not.toBe('degraded')
  })

  it('flags a critical-finding hop as unhealthy (the break)', () => {
    const t = baseTrace({
      verdict: 'broken',
      brokenAt: 1,
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [{ code: 'problem:x', severity: 'critical', message: '0/2 ready' }] },
      ],
      routes: [{ route: 'echo', target: 'echo:80', outcome: 'unreachable', confidence: 'real' }],
    })
    const topo = traceToSubgraph(t)
    expect(topo.nodes.find((n) => n.kind === 'Service')?.status).toBe('unhealthy') // unreachable route
    expect(topo.nodes.find((n) => n.kind === 'Pods')?.status).toBe('unhealthy') // critical finding
  })

  it('Service "0/N selected pods ready" is a backend SYMPTOM → Service amber (wired, no backend); the red fault sits on the Pods node', () => {
    const t = baseTrace({
      verdict: 'broken',
      brokenAt: 0,
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [
          { code: 'problem:0/1 selected pods ready', severity: 'critical', message: '0/1 selected pods ready' },
        ] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', meta: { ready: 0, selected: 1 }, findings: [
          { code: 'problem:CrashLoopBackOff', severity: 'critical', message: 'CrashLoopBackOff' },
        ] },
      ],
      // Indirect (proxy-only) unreachable → the route doesn't independently condemn
      // the Service node; only the finding does, and that's the symptom we cap.
      routes: [{ route: 'echo', target: 'echo:80', outcome: 'unreachable', confidence: 'indirect' }],
    })
    const topo = traceToSubgraph(t)
    expect(topo.nodes.find((n) => n.kind === 'Service')?.status).toBe('degraded') // wired, no backend - NOT red
    expect(topo.nodes.find((n) => n.kind === 'Pods')?.status).toBe('unhealthy') // the real fault
  })

  it('partial-ready backend (ready>0, one pod crashing) → Pods node amber, NOT red - it still serves traffic', () => {
    const t = baseTrace({
      verdict: 'degraded',
      brokenAt: -1,
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'dual' }, edge: 'entry:Service', findings: [] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', meta: { ready: 1, selected: 2 }, findings: [
          { code: 'problem:CrashLoopBackOff', severity: 'critical', message: 'CrashLoopBackOff' },
        ] },
      ],
      routes: [{ route: 'dual', outcome: 'reached', confidence: 'indirect' }],
    })
    const topo = traceToSubgraph(t)
    expect(topo.nodes.find((n) => n.kind === 'Pods')?.status).toBe('degraded') // serves via the ready pod - not the false red
  })

  it('does NOT color the Pods node for a NetworkPolicy finding (path concern, not own-health)', () => {
    // A would-deny is a PATH restriction; the pod itself answered HTTP 200. The
    // node must stay healthy - the policy lives in the hop detail + edge, never
    // the node dot. Painting it amber would over-claim the pod is unwell.
    const t = baseTrace({
      verdict: 'healthy',
      brokenAt: -1,
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [], probes: [{ layer: 'http', target: 'echo:80', vantage: 'local', path: 'apiserver', ok: true }] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [{ code: 'netpol:would-deny', severity: 'warning', message: 'A cluster network rule would block traffic on :80' }], probes: [{ layer: 'http', target: 'pod port 80', vantage: 'local', path: 'apiserver', ok: true }] },
      ],
      routes: [{ route: 'echo', target: 'echo:80', outcome: 'reached', confidence: '' }],
    })
    const topo = traceToSubgraph(t)
    expect(topo.nodes.find((n) => n.kind === 'Pods')?.status).toBe('healthy')
  })
})

describe('traceToSubgraph - router (Ingress/Gateway) node = front-door OWN health', () => {
  const router = (routes: Trace['routes']): Trace =>
    baseTrace({
      subject: { kind: 'Ingress', namespace: 'prod', name: 'gw' },
      verdict: 'degraded',
      downstream: [{ resource: { kind: 'Ingress', namespace: 'prod', name: 'gw' }, edge: 'entry:Ingress', findings: [] }],
      routes,
    })
  const routerStatus = (t: Trace) => traceToSubgraph(t).nodes.find((n) => n.kind === 'Ingress')?.status

  it('all routes server-error (5xx) → front door is amber, NOT red (traffic flowed, backend errored)', () => {
    expect(routerStatus(router([
      { route: '/a', target: 'a:80', outcome: 'server-error' },
      { route: '/b', target: 'b:80', outcome: 'server-error' },
    ]))).toBe('degraded')
  })

  it('benign scale-to-zero routes → front door NOT red (dormant ≠ broken)', () => {
    expect(routerStatus(router([
      { route: '/a', target: 'a:80', outcome: 'unreachable', benign: true },
    ]))).not.toBe('unhealthy')
  })

  it('every route a genuine (non-benign) unreachable → front door red', () => {
    expect(routerStatus(router([
      { route: '/a', target: 'a:80', outcome: 'unreachable' },
      { route: '/b', target: 'b:80', outcome: 'unreachable' },
    ]))).toBe('unhealthy')
  })

  it('a Service subject scaled to zero (benign unreachable) is amber, NOT red', () => {
    const t = baseTrace({
      subject: { kind: 'Service', namespace: 'prod', name: 'echo' },
      verdict: 'degraded',
      downstream: [{ resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [] }],
      routes: [{ route: 'echo', target: 'echo:80', outcome: 'unreachable', benign: true }],
    })
    expect(traceToSubgraph(t).nodes.find((n) => n.kind === 'Service')?.status).not.toBe('unhealthy')
  })

  it('a benign scale-to-0 route does NOT color its edge red nor cascade-block the pods behind it', () => {
    const t = baseTrace({
      subject: { kind: 'Ingress', namespace: 'prod', name: 'ing' },
      verdict: 'degraded',
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'dormant' }, edge: 'Ingress->Service', findings: [] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [] },
      ],
      routes: [{ route: '/x', target: 'dormant:80', outcome: 'unreachable', benign: true }],
    })
    const topo = traceToSubgraph(t)
    const svc = topo.nodes.find((n) => n.kind === 'Service')!
    expect(topo.edges.find((e) => e.target === svc.id)?.reachOutcome).not.toBe('unreachable')
    const pods = topo.nodes.find((n) => n.kind === 'Pods')!
    expect(topo.edges.find((e) => e.target === pods.id)?.reachOutcome).not.toBe('blocked')
  })
})

const multiTrace = (): Trace =>
  baseTrace({
    subject: { kind: 'Ingress', namespace: 'prod', name: 'multi' },
    verdict: 'degraded',
    downstream: [
      { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'Ingress->Service', findings: [] },
      { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [] },
      { resource: { kind: 'Service', namespace: 'prod', name: 'ghost' }, edge: 'Ingress->Service', findings: [{ code: 'missing_ref:x', severity: 'critical', message: 'Service ghost does not exist' }] },
    ],
    routes: [
      { route: '/web', target: 'echo:80', outcome: 'verified', confidence: 'real' },
      { route: '/api', target: 'ghost', outcome: 'unreachable', confidence: '' },
    ],
  })

describe('traceToSubgraph - multi-backend BRANCHES (no linear chaining)', () => {
  it('a second backend hangs off the ENTRY, not after the pods', () => {
    const topo = traceToSubgraph(multiTrace())
    const subjId = topo.nodes.find((n) => n.kind === 'Ingress')!.id
    const echoId = topo.nodes.find((n) => n.name === 'echo')!.id
    const ghostId = topo.nodes.find((n) => n.name === 'ghost')!.id
    const podsId = topo.nodes.find((n) => n.kind === 'Pods')!.id
    // ghost branches off the subject (multi), NOT off pods (the cycle-1 linear bug)
    expect(topo.edges.find((e) => e.target === ghostId)?.source).toBe(subjId)
    expect(topo.edges.find((e) => e.target === echoId)?.source).toBe(subjId)
    // pods hang off THEIR parent service (echo), not the subject
    expect(topo.edges.find((e) => e.target === podsId)?.source).toBe(echoId)
  })

  it('per-route truth on the EDGE; the router node stays NEUTRAL (no over-attribution)', () => {
    const topo = traceToSubgraph(multiTrace())
    const ghostId = topo.nodes.find((n) => n.name === 'ghost')!.id
    const echoId = topo.nodes.find((n) => n.name === 'echo')!.id
    // the multi Ingress node is NOT painted red though one of its routes is broken
    expect(topo.nodes.find((n) => n.kind === 'Ingress')?.status).not.toBe('unhealthy')
    // the broken route's color is on its edge
    expect(topo.edges.find((e) => e.target === ghostId)?.reachOutcome).toBe('unreachable')
    expect(['verified', 'reached']).toContain(topo.edges.find((e) => e.target === echoId)?.reachOutcome)
    // route labels on the edges
    expect(topo.edges.find((e) => e.target === ghostId)?.label).toBe('/api')
  })
})

describe('traceToSubgraph - frontier rule (no flow past a break)', () => {
  it('a broken Service does NOT draw a reached edge to its pods - it is blocked', () => {
    const t = baseTrace({
      verdict: 'broken',
      brokenAt: 0,
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'notready' }, edge: 'entry:Service', findings: [{ code: 'svc:no-endpoints', severity: 'critical', message: '0/2 ready' }] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [] },
      ],
      routes: [{ route: 'notready', target: 'notready:80', outcome: 'unreachable', confidence: 'real' }],
      subject: { kind: 'Service', namespace: 'prod', name: 'notready' },
    })
    const topo = traceToSubgraph(t)
    const podsId = topo.nodes.find((n) => n.kind === 'Pods')!.id
    const edge = topo.edges.find((e) => e.target === podsId)
    expect(edge?.reachOutcome).toBe('blocked')
    expect(edge?.reachOutcome).not.toBe('reached')
    expect(edge?.reachOutcome).not.toBe('verified')
  })
})

describe('traceToSubgraph - multi-upstream convergence (a broken sibling upstream must not block the subject)', () => {
  it('keeps a reached subject→pods edge + neutral upstream nodes when a sibling upstream has an unrelated broken route', () => {
    const t = baseTrace({
      subject: { kind: 'Service', namespace: 'prod', name: 'echo' },
      upstreams: [
        // `multi` carries a critical from its OWN other route (/api→ghost) - it must NOT
        // paint the multi node red, nor cascade-block the shared echo→pods.
        { resource: { kind: 'Ingress', namespace: 'prod', name: 'multi' }, edge: 'Ingress->Service', findings: [{ code: 'missing_ref:x', severity: 'critical', message: 'ghost missing' }] },
        { resource: { kind: 'Ingress', namespace: 'prod', name: 'wild' }, edge: 'Ingress->Service', findings: [] },
      ],
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [], probes: [{ layer: 'http', target: 'echo:80', vantage: 'local', path: 'apiserver', ok: true }] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [], probes: [{ layer: 'http', target: 'pod:80', vantage: 'local', path: 'apiserver', ok: true }] },
      ],
      verdict: 'unknown',
      routes: [{ route: 'echo', target: 'echo:80', outcome: 'reached', confidence: 'indirect' }],
    })
    const topo = traceToSubgraph(t)
    // upstream entry nodes are NEUTRAL - never colored by their own/other-route findings
    expect(topo.nodes.find((n) => n.name === 'multi')?.status).toBe('unknown')
    expect(topo.nodes.find((n) => n.name === 'wild')?.status).toBe('unknown')
    const svc = topo.nodes.find((n) => n.kind === 'Service')!
    const pods = topo.nodes.find((n) => n.kind === 'Pods')!
    // the subject→pods edge is REACHED, NOT blocked by the sibling upstream's break
    const svcToPods = topo.edges.find((e) => e.source === svc.id && e.target === pods.id)
    expect(svcToPods?.reachOutcome).toBe('reached')
    expect(svcToPods?.reachOutcome).not.toBe('blocked')
    // both UNPROBED converging upstream→subject edges fall back to the subject's reach
    const upEdges = topo.edges.filter((e) => e.target === svc.id)
    expect(upEdges).toHaveLength(2)
    expect(upEdges.every((e) => e.reachOutcome === 'reached')).toBe(true)
  })

  it('a PROBED-and-failed upstream entry shows unreachable, not the subject reach', () => {
    // The Service itself is healthy, but this entry path was probed and every probe
    // failed - the edge must reflect the failed entry, not inherit the subject's green.
    const t = baseTrace({
      subject: { kind: 'Service', namespace: 'prod', name: 'echo' },
      upstreams: [
        {
          resource: { kind: 'Ingress', namespace: 'prod', name: 'down' },
          edge: 'Ingress->Service',
          findings: [],
          probes: [{ layer: 'http', target: 'shop.example.com:80', vantage: 'in-cluster', ok: false }],
        },
      ],
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [], probes: [{ layer: 'http', target: 'echo:80', vantage: 'in-cluster', ok: true }] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [] },
      ],
      verdict: 'broken',
      reason: 'all entry paths into this resource are unreachable; the resource itself looks healthy',
      routes: [{ route: 'echo', target: 'echo:80', outcome: 'verified', confidence: 'real' }],
    })
    const topo = traceToSubgraph(t)
    const svc = topo.nodes.find((n) => n.kind === 'Service')!
    const upEdge = topo.edges.find((e) => e.target === svc.id)
    expect(upEdge?.reachOutcome).toBe('unreachable')
    expect(upEdge?.reachOutcome).not.toBe('reached')
  })
})

describe('reachVerdict - confidence is a level, not an alarm', () => {
  it('reached only via proxy → NEUTRAL (no green ✓ over-claim), but NEVER ⚠', () => {
    const v = reachVerdict(baseTrace({ verdict: 'unknown', routes: [{ route: 'echo', outcome: 'reached', confidence: 'indirect' }], headline: 'Reached via API server' }))
    // Proxy-only must not wear the green ✓ (a non-expert reads it as "works, done")
    // - it's neutral with a caveat. It is also never a ⚠ alarm (the path did reach).
    expect(v.icon).toBe('')
    expect(v.icon).not.toBe('⚠')
    expect(v.tone).toBe('neutral')
  })
  it('broken → ✗', () => {
    expect(reachVerdict(baseTrace({ verdict: 'broken', routes: [{ route: 'x', outcome: 'unreachable' }] })).icon).toBe('✗')
  })
  it('server-error → ⚠ (a real problem)', () => {
    expect(reachVerdict(baseTrace({ verdict: 'degraded', routes: [{ route: 'x', outcome: 'server-error', confidence: 'real' }] })).icon).toBe('⚠')
  })
  it('partial (some reachable, some not) → ⚠', () => {
    const v = reachVerdict(baseTrace({ verdict: 'degraded', routes: [{ route: 'a', outcome: 'reached', confidence: 'real' }, { route: 'b', outcome: 'unreachable' }] }))
    expect(v.icon).toBe('⚠')
  })
})

describe('traceToSubgraph - cycle 5 honesty fixes', () => {
  // Defect 8: a DNS-only live probe is not own-health - the node must read unknown.
  it('a backend whose only live probe is a DNS success is unknown, not green healthy', () => {
    const t = baseTrace({
      subject: { kind: 'Ingress', namespace: 'prod', name: 'ing' },
      downstream: [
        { resource: { kind: 'Ingress', namespace: 'prod', name: 'ing' }, edge: 'entry:Ingress', findings: [], probes: [] },
        { resource: { kind: 'Service', namespace: 'prod', name: 'ext' }, edge: 'Ingress->Service', findings: [], probes: [
          { layer: 'dns', target: 'ext', vantage: 'local', ok: true },
        ] },
      ],
      routes: [],
    })
    const g = traceToSubgraph(t)
    const ext = g.nodes.find((n) => n.name === 'ext')
    expect(ext?.status).not.toBe('healthy')
    expect(ext?.status).toBe('unknown')
  })

  // Defect 9: worst-route coloring must not cascade-block pods another route reached.
  it('two routes to one backend (port 80 verified, 9090 unreachable) - pods reached by port 80 are NOT blocked', () => {
    const t = baseTrace({
      subject: { kind: 'Ingress', namespace: 'prod', name: 'ing' },
      downstream: [
        { resource: { kind: 'Ingress', namespace: 'prod', name: 'ing' }, edge: 'entry:Ingress', findings: [], probes: [] },
        { resource: { kind: 'Service', namespace: 'prod', name: 'svc' }, edge: 'Ingress->Service', findings: [], probes: [] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [], probes: [
          { layer: 'http', target: ':80', vantage: 'in-cluster', ok: true, tone: 'healthy' },
        ] },
      ],
      routes: [
        { route: '/web', target: 'svc:80', outcome: 'verified', confidence: 'real' },
        { route: '/api', target: 'svc:9090', outcome: 'unreachable', confidence: 'real' },
      ],
    })
    const g = traceToSubgraph(t)
    const pods = g.nodes.find((n) => n.kind === 'Pods')
    expect((pods?.data as { subtitleOverride?: string })?.subtitleOverride).not.toBe('not reached - break upstream')
    expect(pods?.status).not.toBe('unknown')
  })
})

describe('traceToSubgraph - failed apiserver-proxy probes are indirect evidence, never red', () => {
  it('a node whose ONLY live evidence is a failed proxy probe stays unknown, not unhealthy', () => {
    const t = baseTrace({
      verdict: 'unknown',
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [], probes: [
          { layer: 'http', target: 'echo:80', vantage: 'local', path: 'apiserver', ok: false, tone: 'unhealthy' },
        ] },
      ],
      routes: [{ route: 'echo', target: 'echo:80', outcome: 'unreachable', confidence: 'indirect' }],
    })
    const svc = traceToSubgraph(t).nodes.find((n) => n.kind === 'Service')
    expect(svc?.status).not.toBe('unhealthy')
    expect(svc?.status).toBe('unknown')
  })

  it('a successful proxy probe still counts as positive evidence beside a failed one', () => {
    const t = baseTrace({
      verdict: 'unknown',
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [], probes: [
          { layer: 'http', target: 'pod-a port 80', vantage: 'local', path: 'apiserver', ok: true, tone: 'healthy' },
          { layer: 'http', target: 'pod-b port 80', vantage: 'local', path: 'apiserver', ok: false, tone: 'unhealthy' },
        ] },
      ],
      routes: [{ route: 'echo', target: 'echo:80', outcome: 'reached', confidence: 'indirect' }],
    })
    const pods = traceToSubgraph(t).nodes.find((n) => n.kind === 'Pods')
    // The failed proxy knock is dropped (indirect); the passing one keeps the
    // node green - it answered.
    expect(pods?.status).toBe('healthy')
  })

  it('a REAL (non-proxy) failure still paints the node red', () => {
    const t = baseTrace({
      verdict: 'broken',
      downstream: [
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [], probes: [
          { layer: 'http', target: '10.0.0.1:80', vantage: 'in-cluster', path: 'data', ok: false, tone: 'unhealthy' },
        ] },
      ],
      routes: [],
    })
    expect(traceToSubgraph(t).nodes.find((n) => n.kind === 'Pods')?.status).toBe('unhealthy')
  })

  it('a no-route edge whose failures are ALL proxy-vantage reads not-tested, never unreachable/blocked', () => {
    const t = baseTrace({
      verdict: 'unknown',
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [], probes: [
          { layer: 'http', target: 'pod port 80', vantage: 'local', path: 'apiserver', ok: false, tone: 'unhealthy' },
        ] },
      ],
      routes: [{ route: 'echo', target: 'echo:80', outcome: 'unreachable', confidence: 'indirect' }],
    })
    const topo = traceToSubgraph(t)
    const podsId = topo.nodes.find((n) => n.kind === 'Pods')!.id
    expect(topo.edges.find((e) => e.target === podsId)?.reachOutcome).toBe('not-tested')
  })
})

describe('traceToSubgraph - cycle 11 honesty fixes', () => {
  // Defect 3: routeMatchesRef's namespace guard only fired when targetNamespace was
  // SET. A same-ns route (no targetNamespace) could match a cross-ns same-named
  // backend hop, so routeFor's worst-match attached the same-ns route's unreachable
  // to the cross-ns backend and false-condemned it.
  it('a same-ns route\'s unreachable must NOT attach to a cross-ns same-named backend', () => {
    const t = baseTrace({
      subject: { kind: 'Ingress', namespace: 'prod', name: 'ing' },
      verdict: 'degraded',
      downstream: [
        { resource: { kind: 'Ingress', namespace: 'prod', name: 'ing' }, edge: 'entry:Ingress', findings: [] },
        { resource: { kind: 'Service', namespace: 'prod', name: 'api' }, edge: 'Ingress->Service', findings: [] },
        { resource: { kind: 'Service', namespace: 'other', name: 'api' }, edge: 'Ingress->Service', findings: [] },
      ],
      routes: [
        // Same-ns route (no targetNamespace → targets the subject ns 'prod').
        { route: 'api', target: 'api:80', outcome: 'unreachable', confidence: 'real' },
        // Cross-ns backendRef route - actually verified.
        { route: 'api', target: 'api:80', outcome: 'verified', confidence: 'real', targetNamespace: 'other' },
      ],
    })
    const g = traceToSubgraph(t)
    const crossNsEdge = g.edges.find((e) => e.target === 'Service/other/api')
    const sameNsEdge = g.edges.find((e) => e.target === 'Service/prod/api')
    // The cross-ns backend is verified - its own route - never the same-ns unreachable.
    expect(crossNsEdge?.reachOutcome).toBe('verified')
    // The same-ns backend keeps its own unreachable.
    expect(sameNsEdge?.reachOutcome).toBe('unreachable')
  })
})

describe('traceToSubgraph - overridden test path echoes on the route edge', () => {
  const ingressTrace = (): Trace =>
    baseTrace({
      subject: { kind: 'Ingress', namespace: 'prod', name: 'wild' },
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [] },
      ],
      routes: [{ route: '/web', target: 'echo:80', outcome: 'reached', confidence: 'indirect' }],
    })

  it('defaults to the DECLARED route path, no tooltip', () => {
    const e = traceToSubgraph(ingressTrace()).edges.find((e) => e.label === '/web')
    expect(e).toBeTruthy()
    expect(e?.labelTitle).toBeUndefined()
  })

  it('shows the OVERRIDDEN tested path as the label, keeps the declared path in the tooltip (never overwrites route identity)', () => {
    const edges = traceToSubgraph(ingressTrace(), '/test').edges
    expect(edges.find((e) => e.label === '/web')).toBeFalsy() // declared path no longer the visible label
    const e = edges.find((e) => e.label === '/test')
    expect(e).toBeTruthy()
    expect(e?.labelTitle).toContain('/web') // declared
    expect(e?.labelTitle).toContain('/test') // tested
  })

  it('the default "/" path is not treated as an override', () => {
    const e = traceToSubgraph(ingressTrace(), '/').edges.find((e) => e.label === '/web')
    expect(e).toBeTruthy()
    expect(e?.labelTitle).toBeUndefined()
  })
})

describe('traceToSubgraph - DNS success is not transport evidence', () => {
  // An ExternalName whose name resolves but whose every port fails is DOWN. The
  // DNS-ok row must not soften the node to "partially healthy" amber or keep
  // the edge green - resolving a name reaches nothing.
  it('dns-ok + every port failed → node unhealthy (not degraded), edge unreachable', () => {
    const t = baseTrace({
      verdict: 'broken',
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [] },
        { resource: { kind: 'ExternalName', namespace: 'prod', name: 'db.example.com' }, edge: 'Service->ExternalName', findings: [], probes: [
          { layer: 'dns', target: 'db.example.com', vantage: 'local', ok: true },
          { layer: 'tcp', target: 'db.example.com:5432', vantage: 'local', ok: false, tone: 'unhealthy' },
          { layer: 'http', target: 'db.example.com:80', vantage: 'local', ok: false, tone: 'unhealthy' },
        ] },
      ],
      routes: [],
    })
    const topo = traceToSubgraph(t)
    const ext = topo.nodes.find((n) => n.kind === 'ExternalName')
    expect(ext?.status).toBe('unhealthy')
    expect(topo.edges.find((e) => e.target === ext?.id)?.reachOutcome).toBe('unreachable')
  })

  it('dns-ok only (transport skipped) → node unknown, edge not-tested', () => {
    const t = baseTrace({
      verdict: 'unknown',
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [] },
        { resource: { kind: 'ExternalName', namespace: 'prod', name: 'db.example.com' }, edge: 'Service->ExternalName', findings: [], probes: [
          { layer: 'dns', target: 'db.example.com', vantage: 'local', ok: true },
          { layer: 'tcp', target: 'db.example.com:5432', vantage: 'local', ok: false, skipped: true, reason: 'not reachable from your machine' },
        ] },
      ],
      routes: [],
    })
    const topo = traceToSubgraph(t)
    const ext = topo.nodes.find((n) => n.kind === 'ExternalName')
    expect(ext?.status).toBe('unknown')
    expect(topo.edges.find((e) => e.target === ext?.id)?.reachOutcome).toBe('not-tested')
  })

  it('dns FAILURE is still a real break - node unhealthy, edge unreachable', () => {
    const t = baseTrace({
      verdict: 'broken',
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [] },
        { resource: { kind: 'ExternalName', namespace: 'prod', name: 'ghost.example.com' }, edge: 'Service->ExternalName', findings: [], probes: [
          { layer: 'dns', target: 'ghost.example.com', vantage: 'local', ok: false, error: 'NXDOMAIN' },
        ] },
      ],
      routes: [],
    })
    const topo = traceToSubgraph(t)
    const ext = topo.nodes.find((n) => n.kind === 'ExternalName')
    expect(ext?.status).toBe('unhealthy')
    expect(topo.edges.find((e) => e.target === ext?.id)?.reachOutcome).toBe('unreachable')
  })

  it('a real port success still reads reached/healthy beside the dns row', () => {
    const t = baseTrace({
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [] },
        { resource: { kind: 'ExternalName', namespace: 'prod', name: 'db.example.com' }, edge: 'Service->ExternalName', findings: [], probes: [
          { layer: 'dns', target: 'db.example.com', vantage: 'local', ok: true },
          { layer: 'http', target: 'db.example.com:80', vantage: 'local', ok: true, tone: 'healthy' },
        ] },
      ],
      routes: [],
    })
    const topo = traceToSubgraph(t)
    const ext = topo.nodes.find((n) => n.kind === 'ExternalName')
    expect(ext?.status).toBe('healthy')
    expect(topo.edges.find((e) => e.target === ext?.id)?.reachOutcome).toBe('reached')
  })
})

describe('traceToSubgraph - publishNotReadyAddresses subtitle honesty', () => {
  // meta.ready counts PUBLISHED endpoints for a publishNotReadyAddresses
  // Service (the Go side stamps ready = selected there) - "3/3 ready" over
  // crashlooping pods is a lie; say what the count actually is.
  it('renders a published count, not "N/M ready", when publishNotReadyAddresses is set', () => {
    const t = baseTrace({
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [], meta: { ready: 3, selected: 3, publishNotReadyAddresses: true } },
      ],
      routes: [],
    })
    const pods = traceToSubgraph(t).nodes.find((n) => n.kind === 'Pods')
    const sub = (pods?.data as { subtitleOverride?: string })?.subtitleOverride
    expect(sub).toBe('3 published (readiness not required)')
    expect(sub).not.toContain('3/3')
  })

  it('an ordinary Service keeps the "N/M ready" subtitle', () => {
    const t = baseTrace({
      downstream: [
        { resource: { kind: 'Service', namespace: 'prod', name: 'echo' }, edge: 'entry:Service', findings: [] },
        { resource: { kind: 'Pods', namespace: 'prod', name: '' }, edge: 'Service->Pods', findings: [], meta: { ready: 2, selected: 3 } },
      ],
      routes: [],
    })
    const pods = traceToSubgraph(t).nodes.find((n) => n.kind === 'Pods')
    expect((pods?.data as { subtitleOverride?: string })?.subtitleOverride).toBe('2/3 ready')
  })
})
