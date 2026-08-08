import { describe, it, expect } from 'vitest'
import { routeMark, routeTone, routeChip, orderRoutes, scenariosFor, groupRoutes, vantageSignature, routeIdentity, isSlow, formatLatency, hostMatches, declaredHosts, routeHostOf, routeForOrigin, routeAsSeenFrom, originRouteEvidence, MARKS, edgeHelp, markHelp, edgeHelpIsRedundant } from './reachMarks'
import type { RouteResult, Hop } from './types'

const r = (o: Partial<RouteResult>): RouteResult => ({ route: 'GET /', outcome: 'verified', ...o })

describe('routeMark', () => {
  it('only real-traffic evidence is ever proved', () => {
    expect(routeMark(r({ outcome: 'verified', confidence: 'real' }))).toBe('proved')
    // An apiserver relay bypasses kube-proxy, NetworkPolicy and the mesh, so a
    // clean response through it can never be proof of the real path.
    expect(routeMark(r({ outcome: 'verified', confidence: 'indirect' }))).toBe('proxied')
  })

  it('a proxy-only failure never condemns the real path', () => {
    expect(routeMark(r({ outcome: 'unreachable', confidence: 'indirect' }))).not.toBe('failed')
    expect(routeMark(r({ outcome: 'unreachable', confidence: 'real' }))).toBe('failed')
  })

  it('a deliberate scale-to-zero is excluded, not failed', () => {
    expect(routeMark(r({ outcome: 'unreachable', confidence: 'real', benign: true }))).toBe('excluded')
  })

  it('reached is weaker than verified', () => {
    expect(routeMark(r({ outcome: 'reached', confidence: 'real' }))).toBe('answered')
  })

  it('running and stale override the outcome', () => {
    expect(routeMark(r({ outcome: 'verified', confidence: 'real' }), { running: true })).toBe('running')
    expect(routeMark(r({ outcome: 'verified', confidence: 'real' }), { stale: true })).toBe('stale')
  })

  it('not-tested is untested, never a failure', () => {
    expect(routeMark(r({ outcome: 'not-tested' }))).toBe('untested')
  })
})

describe('routeTone', () => {
  it('indirect verified is never green', () => {
    expect(routeTone(r({ outcome: 'verified', confidence: 'indirect' }))).toBe('degraded')
    expect(routeTone(r({ outcome: 'verified', confidence: 'real' }))).toBe('healthy')
  })

  it('not-tested is never red', () => {
    expect(routeTone(r({ outcome: 'not-tested' }))).toBe('unknown')
  })

  it('benign and indirect unreachables stay amber', () => {
    expect(routeTone(r({ outcome: 'unreachable', benign: true, confidence: 'real' }))).toBe('degraded')
    expect(routeTone(r({ outcome: 'unreachable', confidence: 'indirect' }))).toBe('degraded')
    expect(routeTone(r({ outcome: 'unreachable', confidence: 'real' }))).toBe('unhealthy')
  })
})

describe('routeChip', () => {
  it('names the caveat rather than claiming verification', () => {
    // Asserts the meaning: an indirect result must name the API-server relay
    // rather than claim a clean pass. ("Skipped the cluster network" was the old
    // wording - false, since the proxy still crosses the cluster network; what
    // it skips is the front door and the normal traffic path.)
    expect(routeChip(r({ outcome: 'verified', confidence: 'indirect' }))).toMatch(/API server/i)
    expect(routeChip(r({ outcome: 'verified', confidence: 'real' }))).toBe('got through')
  })
})

describe('orderRoutes', () => {
  it('leads with the worst outcome', () => {
    const out = orderRoutes([r({ route: 'a', outcome: 'verified' }), r({ route: 'b', outcome: 'unreachable' }), r({ route: 'c', outcome: 'not-tested' })])
    expect(out.map((x) => x.route)).toEqual(['b', 'c', 'a'])
  })
})

describe('marks vocabulary', () => {
  it('only proved and failed draw solid — everything softer must be dashed', () => {
    for (const [name, style] of Object.entries(MARKS)) {
      if (name === 'proved' || name === 'failed') expect(style.dash).toBe('none')
      else expect(style.dash).not.toBe('none')
    }
  })
})

describe('latency', () => {
  it('flags pathological latency only', () => {
    expect(isSlow({ layer: 'http', target: 't', vantage: 'local', ok: true, latencyNs: 11_000_000 })).toBe(false)
    expect(isSlow({ layer: 'http', target: 't', vantage: 'local', ok: true, latencyNs: 1_900_000_000 })).toBe(true)
  })

  it('formats across unit boundaries', () => {
    expect(formatLatency(11_000_000)).toBe('11 ms')
    expect(formatLatency(1_900_000_000)).toBe('1.9 s')
    expect(formatLatency(undefined)).toBe('')
  })
})

describe('scenariosFor', () => {
  const r2 = (o: Partial<RouteResult>): RouteResult => ({ route: 'h/', outcome: 'verified', ...o })

  it('folds routes that agree in every respect into one scenario', () => {
    // A Gateway route with three hostnames and one backend is one situation
    // with three front doors, not three situations.
    const s = scenariosFor(
      [
        r2({ route: 'a.example.com/', target: 'svc:8080', outcome: 'reached', confidence: 'indirect' }),
        r2({ route: 'b.example.com/', target: 'svc:8080', outcome: 'reached', confidence: 'indirect' }),
        r2({ route: 'c.example.com/', target: 'svc:8080', outcome: 'reached', confidence: 'indirect' }),
      ],
      [],
    )
    expect(s).toHaveLength(1)
    expect(s[0].hosts).toEqual(['a.example.com', 'b.example.com', 'c.example.com'])
    expect(s[0].sub).toMatch(/3 hostnames/)
  })

  it('keeps a host that behaves differently in its own scenario', () => {
    const s = scenariosFor(
      [
        r2({ route: 'a.example.com/', target: 'svc:8080', outcome: 'verified', confidence: 'real' }),
        r2({ route: 'b.example.com/', target: 'svc:8080', outcome: 'unreachable', confidence: 'real' }),
      ],
      [],
    )
    expect(s).toHaveLength(2)
    // Worst first, so the failure leads.
    expect(s[0].primary.outcome).toBe('unreachable')
  })

  it('separates different backends even when the outcome matches', () => {
    const s = scenariosFor(
      [r2({ route: 'a/', target: 'svc:80', outcome: 'verified' }), r2({ route: 'b/', target: 'svc:9090', outcome: 'verified' })],
      [],
    )
    expect(s).toHaveLength(2)
  })

  it('surfaces untested routes as scenarios — otherwise an unprobed resource shows no paths at all', () => {
    const s = scenariosFor([], [{ route: 'a.example.com/', reason: 'route not actively tested' }])
    expect(s).toHaveLength(1)
    expect(s[0].primary.outcome).toBe('not-tested')
  })

  it('keeps untested routes apart when their reasons differ', () => {
    // "port 443 can't be verified through the proxy" and "port 80 timed out"
    // are different situations, even though both are merely not-tested.
    const s = scenariosFor([], [
      { route: 'port 80', reason: 'the backend did not respond within the probe budget' },
      { route: 'port 443', reason: 'HTTPS backend - the API-server proxy speaks plain HTTP' },
    ])
    expect(s).toHaveLength(2)
    expect(s.map((x) => x.label).sort()).toEqual(['port 443', 'port 80'])
  })

  it('still folds untested routes that share one reason', () => {
    const s = scenariosFor([], [
      { route: 'a.example.com/', reason: 'route not actively tested' },
      { route: 'b.example.com/', reason: 'route not actively tested' },
    ])
    expect(s).toHaveLength(1)
  })

  it('gives a scenario a key that survives re-sorting', () => {
    // The strip is sorted worst-first, so a re-run that changes an outcome
    // re-orders it. Selection is stored by key precisely because a positional
    // index would silently move the user to a different path.
    const before = scenariosFor(
      [r2({ route: 'a/', target: 'svc:80', outcome: 'unreachable', confidence: 'real' }), r2({ route: 'b/', target: 'svc:90', outcome: 'verified', confidence: 'real' })],
      [],
    )
    expect(before[0].primary.target).toBe('svc:80')
    const bKey = before[1].key

    // Outcomes swap on the next run; worst-first now puts svc:90 first.
    const after = scenariosFor(
      [r2({ route: 'a/', target: 'svc:80', outcome: 'verified', confidence: 'real' }), r2({ route: 'b/', target: 'svc:90', outcome: 'unreachable', confidence: 'real' })],
      [],
    )
    expect(after[0].primary.target).toBe('svc:90')
    // The old index 1 now points at a different scenario - which is why the key,
    // not the index, is what selection must be stored by.
    expect(after[1].key).not.toBe(bKey)
    expect(new Set(after.map((x) => x.key)).size).toBe(after.length)
  })

  it('uses the single route label verbatim when nothing was grouped', () => {
    const s = scenariosFor([r2({ route: 'only.example.com/', target: 'svc:80' })], [])
    expect(s[0].label).toBe('only.example.com/')
  })

  // A GATEWAY declares hostnames per listener and has NO top-level hostnames -
  // reading only `hostnames` (an Ingress/HTTPRoute field) made every real
  // Gateway look like it served nothing. See internal/trace gatewayConfig.
  const gw = (name: string, ...hostnames: string[]): Hop => ({
    resource: { kind: 'Gateway', namespace: 'infra', name },
    edge: 'routes',
    findings: [],
    config: { listeners: hostnames.map((hostname, i) => ({ name: `l${i}`, port: 443, hostname })) },
  })
  const ingress = (name: string, ...hostnames: string[]): Hop => ({
    resource: { kind: 'Ingress', namespace: 'infra', name },
    edge: 'routes',
    findings: [],
    config: { hostnames },
  })

  it('splits two hosts that reach the same backend through DIFFERENT front doors', () => {
    // One Gateway can be broken while its sibling is fine. Folding them behind a
    // shared verdict hides exactly the difference worth debugging.
    const s = scenariosFor(
      [
        r2({ route: 'a.example.com/', target: 'svc:8080', outcome: 'verified', confidence: 'real' }),
        r2({ route: 'b.example.com/', target: 'svc:8080', outcome: 'verified', confidence: 'real' }),
      ],
      [],
      [gw('public-gw', 'a.example.com'), gw('internal-gw', 'b.example.com')],
    )
    expect(s).toHaveLength(2)
    expect(s.map((x) => x.entry).sort()).toEqual(['internal-gw', 'public-gw'])
    expect(s[0].sub).toMatch(/via /)
  })

  it('still folds hosts that share one front door', () => {
    const s = scenariosFor(
      [
        r2({ route: 'a.example.com/', target: 'svc:8080', outcome: 'verified', confidence: 'real' }),
        r2({ route: 'b.example.com/', target: 'svc:8080', outcome: 'verified', confidence: 'real' }),
      ],
      [],
      [gw('public-gw', 'a.example.com', 'b.example.com')],
    )
    expect(s).toHaveLength(1)
    expect(s[0].entry).toBe('public-gw')
  })

  it('does not invent a front door when no upstream serves the host', () => {
    const s = scenariosFor([r2({ route: 'a.example.com/', target: 'svc:80' })], [], [gw('other-gw', 'z.example.com')])
    expect(s[0].entry).toBeUndefined()
    expect(s[0].sub).not.toMatch(/via /)
  })

  it('reads an Ingress top-level hostname too, not only Gateway listeners', () => {
    const s = scenariosFor(
      [
        r2({ route: 'a.example.com/', target: 'svc:80', outcome: 'verified', confidence: 'real' }),
        r2({ route: 'b.example.com/', target: 'svc:80', outcome: 'verified', confidence: 'real' }),
      ],
      [],
      [ingress('public-ing', 'a.example.com'), gw('gw', 'b.example.com')],
    )
    expect(s.map((x) => x.entry).sort()).toEqual(['gw', 'public-ing'])
  })

  it('a wildcard listener serves its subdomains and keeps them one scenario', () => {
    const s = scenariosFor(
      [
        r2({ route: 'api.example.com/', target: 'svc:80', outcome: 'verified', confidence: 'real' }),
        r2({ route: 'web.example.com/', target: 'svc:80', outcome: 'verified', confidence: 'real' }),
      ],
      [],
      [gw('wild-gw', '*.example.com')],
    )
    expect(s).toHaveLength(1)
    expect(s[0].entry).toBe('wild-gw')
  })

  it('a catch-all listener does not claim a port-based route', () => {
    // The front-door fixture declares a listener with no hostname. Without the
    // route-label guard it matched ":80 → 8080" and reported a Gateway the
    // request never touched.
    const s = scenariosFor([r2({ route: 'GET / · :80 → 8080', target: ':80 → 8080' })], [], [gw('catch-all', '')])
    expect(s[0].entry).toBeUndefined()
  })

  it('an exact listener wins over a catch-all that would swallow every host', () => {
    // Otherwise a hostname-less listener matches everything and collapses the
    // scenarios the grouping exists to keep apart.
    const s = scenariosFor(
      [r2({ route: 'api.example.com/', target: 'svc:80', outcome: 'verified', confidence: 'real' })],
      [],
      [gw('catch-all', ''), gw('exact-gw', 'api.example.com')],
    )
    expect(s[0].entry).toBe('exact-gw')
  })
})

describe('routeHostOf', () => {
  it('reads the host out of a host-based label', () => {
    expect(routeHostOf('example.com/api')).toBe('example.com')
    expect(routeHostOf('*.example.com/')).toBe('*.example.com')
  })

  it('returns nothing for labels that are not hostnames', () => {
    // A catch-all Gateway listener legitimately serves ANY host, so feeding it
    // a port label would make it claim a route that never went near it.
    expect(routeHostOf(':80 → 8080')).toBe('')
    expect(routeHostOf('GET / · :80 → 8080')).toBe('')
    expect(routeHostOf('default backend')).toBe('')
    expect(routeHostOf('/api')).toBe('')
  })
})

describe('hostMatches', () => {
  it('matches exactly, case-insensitively', () => {
    expect(hostMatches('API.example.com', 'api.example.com')).toBe(true)
    expect(hostMatches('api.example.com', 'other.example.com')).toBe(false)
  })

  it('a wildcard covers exactly one extra label - never the apex or a deeper name', () => {
    expect(hostMatches('*.example.com', 'api.example.com')).toBe(true)
    expect(hostMatches('*.example.com', 'a.b.example.com')).toBe(false)
    expect(hostMatches('*.example.com', 'example.com')).toBe(false)
  })

  it('an empty declared hostname is the Gateway-API "any host" listener', () => {
    expect(hostMatches('', 'anything.example.com')).toBe(true)
    // ...but an empty REQUEST host matches nothing - there is no host to serve.
    expect(hostMatches('', '')).toBe(false)
  })
})

describe('declaredHosts', () => {
  it('collects Gateway listener hostnames, which live nowhere else on the hop', () => {
    expect(
      declaredHosts({
        resource: { kind: 'Gateway', name: 'g' },
        edge: '',
        findings: [],
        config: { listeners: [{ port: 443, hostname: 'a.example.com' }, { port: 80 }] },
      }),
    ).toEqual(['a.example.com', ''])
  })

  it('collects Ingress hostnames and TLS hosts', () => {
    expect(
      declaredHosts({
        resource: { kind: 'Ingress', name: 'i' },
        edge: '',
        findings: [],
        config: { hostnames: ['a.example.com'], tlsHosts: ['b.example.com'] },
      }),
    ).toEqual(['a.example.com', 'b.example.com'])
  })
})

describe('per-vantage route resolution', () => {
  // The rollup buckets by mechanism and takes worst-wins, so in-cluster success
  // + laptop failure collapses to unreachable. byVantage keeps both.
  const disagreeing: RouteResult = {
    route: 'checkout.example.com/',
    target: 'checkout:80',
    outcome: 'unreachable',
    confidence: 'real',
    evidence: 'connection refused',
    byVantage: [
      { vantage: 'in-cluster', path: 'data', outcome: 'verified', confidence: 'real', evidence: 'HTTP 200' },
      { vantage: 'local', path: 'data', outcome: 'unreachable', confidence: 'real', evidence: 'connection refused' },
      { vantage: 'local', path: 'apiserver', outcome: 'verified', confidence: 'indirect', evidence: 'HTTP 200 via proxy' },
    ],
  }

  it('gives each origin its OWN result, not the merged one', () => {
    expect(routeAsSeenFrom(disagreeing, 'incluster')!.outcome).toBe('verified')
    expect(routeAsSeenFrom(disagreeing, 'local')!.outcome).toBe('unreachable')
  })

  it('the vantage that worked no longer inherits the merged failure', () => {
    // This is the misattribution the whole change exists to remove.
    expect(routeMark(routeAsSeenFrom(disagreeing, 'incluster')!)).toBe('proved')
    expect(routeMark(routeAsSeenFrom(disagreeing, 'local')!)).toBe('failed')
  })

  it('the apiserver origin reads the relayed row, whatever machine issued it', () => {
    const v = routeForOrigin(disagreeing, 'apiserver')!
    expect(v.path).toBe('apiserver')
    expect(v.confidence).toBe('indirect')
    // ...and a relay can still never be proof.
    expect(routeMark(routeAsSeenFrom(disagreeing, 'apiserver')!)).toBe('proxied')
  })

  it('never hands a data-path row to the apiserver origin, or vice versa', () => {
    const dataOnly: RouteResult = {
      route: 'r',
      outcome: 'verified',
      byVantage: [{ vantage: 'local', path: 'data', outcome: 'verified', confidence: 'real' }],
    }
    expect(routeForOrigin(dataOnly, 'apiserver')).toBeUndefined()
  })

  it('reports nothing for an origin that produced nothing', () => {
    expect(routeForOrigin(disagreeing, 'incluster')).toBeDefined()
    const laptopOnly: RouteResult = {
      route: 'r',
      outcome: 'verified',
      byVantage: [{ vantage: 'local', path: 'data', outcome: 'verified', confidence: 'real' }],
    }
    expect(routeForOrigin(laptopOnly, 'incluster')).toBeUndefined()
  })

  it('falls back to the rollup when the producer sent no breakdown', () => {
    // Older backend, or a route with no probes: behave exactly as before rather
    // than blanking the view.
    const legacy: RouteResult = { route: 'r', outcome: 'verified', confidence: 'real', evidence: 'HTTP 200' }
    expect(routeAsSeenFrom(legacy, 'incluster')).toEqual(legacy)
    expect(routeForOrigin(legacy, 'incluster')).toBeUndefined()
  })

  it('is undefined for no route at all', () => {
    expect(routeAsSeenFrom(undefined, 'local')).toBeUndefined()
    expect(routeForOrigin(undefined, 'local')).toBeUndefined()
  })
})

describe('missing-row semantics', () => {
  // The subtle one: falling back to the rollup when the producer DID send a
  // breakdown and this origin simply is not in it re-creates the exact
  // misattribution byVantage exists to remove.
  const laptopOnly: RouteResult = {
    route: 'checkout.example.com/',
    outcome: 'unreachable',
    confidence: 'real',
    byVantage: [{ vantage: 'local', path: 'data', outcome: 'unreachable', confidence: 'real' }],
  }

  it('an origin absent from a breakdown is "not tested", not the rollup', () => {
    expect(originRouteEvidence(laptopOnly, 'incluster').kind).toBe('none')
    expect(routeAsSeenFrom(laptopOnly, 'incluster')).toBeUndefined()
  })

  it('what an origin found on OTHER routes cannot vouch for this one', () => {
    // in-cluster may well have probed a sibling route; this route says nothing
    // about it, and the absence is the evidence.
    const ev = originRouteEvidence(laptopOnly, 'incluster')
    expect(ev.kind).not.toBe('rollup')
  })

  it('a trace with NO breakdown still yields the rollup, flagged as such', () => {
    const legacy: RouteResult = { route: 'r', outcome: 'verified', confidence: 'real' }
    const ev = originRouteEvidence(legacy, 'incluster')
    expect(ev.kind).toBe('rollup')
    expect(ev.kind === 'rollup' && ev.result.outcome).toBe('verified')
  })

  it('an empty breakdown array counts as no breakdown', () => {
    const empty: RouteResult = { route: 'r', outcome: 'verified', byVantage: [] }
    expect(originRouteEvidence(empty, 'incluster').kind).toBe('rollup')
  })

  it("the origin that IS in the breakdown still gets its own row", () => {
    const ev = originRouteEvidence(laptopOnly, 'local')
    expect(ev.kind).toBe('own')
    expect(ev.kind === 'own' && ev.result.outcome).toBe('unreachable')
  })
})

describe('groupRoutes keeps apart what the board would otherwise speak for', () => {
  const base = (o: Partial<RouteResult>): RouteResult => ({
    route: 'a.example.com/', target: 'shop:80', outcome: 'verified', confidence: 'real', ...o,
  })

  // The whole point of the per-vantage split: two routes whose ROLLUPS agree can
  // still disagree per vantage. Folding them puts rs[0] in charge of the board,
  // so the route that fails from a laptop is represented by the one that works.
  it('splits routes whose rollups agree but whose vantages disagree', () => {
    const works = base({
      route: 'a.example.com/',
      byVantage: [{ vantage: 'local', path: 'data', outcome: 'verified' }],
    })
    const fails = base({
      route: 'b.example.com/',
      byVantage: [{ vantage: 'local', path: 'data', outcome: 'unreachable' }],
    })
    expect(groupRoutes([works, fails])).toHaveLength(2)
  })

  it('still folds routes that agree on every axis', () => {
    const one = base({ route: 'a.example.com/', byVantage: [{ vantage: 'local', path: 'data', outcome: 'verified' }] })
    const two = base({ route: 'b.example.com/', byVantage: [{ vantage: 'local', path: 'data', outcome: 'verified' }] })
    expect(groupRoutes([one, two])).toHaveLength(1)
  })

  it('keeps same-named Services in different namespaces apart', () => {
    const prod = base({ route: 'a.example.com/', targetNamespace: 'prod' })
    const staging = base({ route: 'b.example.com/', targetNamespace: 'staging' })
    expect(groupRoutes([prod, staging])).toHaveLength(2)
  })

  // Same outcome and layer, different reason: showing one and hiding the other
  // behind a tab that claims to speak for both loses the actual diagnosis.
  it('keeps differing evidence apart at every outcome, not just not-tested', () => {
    const refused = base({ route: 'a.example.com/', outcome: 'unreachable', failedLayer: 'tcp', evidence: 'connection refused' })
    const timeout = base({ route: 'b.example.com/', outcome: 'unreachable', failedLayer: 'tcp', evidence: 'timed out' })
    expect(groupRoutes([refused, timeout])).toHaveLength(2)
  })

  it('counts paths, not hostnames, when a fold shares one host', () => {
    const admin = base({ route: 'a.example.com/admin' })
    const web = base({ route: 'a.example.com/web' })
    const [s] = groupRoutes([admin, web])
    expect(s.routes).toHaveLength(2)
    expect(s.sub).toContain('2 paths')
  })

  it('vantageSignature is order-independent', () => {
    const a = base({ byVantage: [{ vantage: 'local', path: 'data', outcome: 'verified' }, { vantage: 'in-cluster', path: 'data', outcome: 'unreachable' }] })
    const b = base({ byVantage: [{ vantage: 'in-cluster', path: 'data', outcome: 'unreachable' }, { vantage: 'local', path: 'data', outcome: 'verified' }] })
    expect(vantageSignature(a)).toBe(vantageSignature(b))
  })
})

describe('a config-derived break belongs to no vantage', () => {
  const known: RouteResult = {
    route: 'a.example.com/', target: 'missing:80', outcome: 'unreachable',
    evidence: 'backend Service does not exist', basis: 'declared-config',
  }

  // It has no byVantage by construction, and the rollup fallback would otherwise
  // hand it to whichever origin happened to be selected - presenting a static
  // configuration fact as that vantage's failed dial.
  it('is reported as config, never as the selected origin\'s observation', () => {
    for (const id of ['local', 'incluster', 'apiserver']) {
      expect(originRouteEvidence(known, id).kind).toBe('config')
    }
  })

  it('still carries its evidence', () => {
    const ev = originRouteEvidence(known, 'local')
    expect(ev.kind === 'config' && ev.result.evidence).toBe('backend Service does not exist')
  })

  it('an observed route is unaffected', () => {
    const observed: RouteResult = {
      route: 'a/', target: 'shop:80', outcome: 'verified',
      byVantage: [{ vantage: 'local', path: 'data', outcome: 'verified' }],
    }
    expect(originRouteEvidence(observed, 'local').kind).toBe('own')
  })
})

describe('derived breaks are never described as requests', () => {
  const declared: RouteResult = {
    route: 'a/', target: 'missing:80', outcome: 'unreachable', basis: 'declared-config',
  }
  const state: RouteResult = {
    route: 'b/', target: 'shop:80', outcome: 'unreachable', basis: 'cluster-state',
  }

  // 'failed' is the mark for a request that was sent and did not arrive. Using
  // it for something never dialled is what made the page contradict itself:
  // "could not get through" beside "read from configuration".
  it('marks a derived break as config, not failed', () => {
    expect(routeMark(declared)).toBe('config')
    expect(routeMark(state)).toBe('config')
  })

  it('never says "could not get through" for either class', () => {
    expect(routeChip(declared)).not.toMatch(/get through/)
    expect(routeChip(state)).not.toMatch(/get through/)
  })

  // The two derived classes are different facts: one is broken regardless of
  // what the cluster is doing, the other changes when the workload does.
  it('distinguishes declared config from current cluster state', () => {
    expect(routeChip(declared)).not.toBe(routeChip(state))
  })

  it('an observed failure is still a failure', () => {
    expect(routeMark({ route: 'c/', target: 's:80', outcome: 'unreachable', confidence: 'real' })).toBe('failed')
  })
})

describe('routeIdentity is what a route IS, not what happened to it', () => {
  const base = { route: 'a.example.com/', target: 'shop:80', targetNamespace: 'prod' }

  // Selection anchored on the scenario group key moved the user to a different
  // path whenever a re-run changed an outcome - losing their place exactly when
  // they had just asked for new evidence about the path they were reading.
  it('survives a change in outcome, evidence and per-vantage results', () => {
    const before: RouteResult = { ...base, outcome: 'unreachable', evidence: 'refused' }
    const after: RouteResult = {
      ...base,
      outcome: 'verified',
      confidence: 'real',
      evidence: 'HTTP 200',
      byVantage: [{ vantage: 'local', path: 'data', outcome: 'verified' }],
    }
    expect(routeIdentity(after)).toBe(routeIdentity(before))
  })

  it('separates same-named backends in different namespaces', () => {
    expect(routeIdentity({ ...base, targetNamespace: 'prod', outcome: 'verified' })).not.toBe(
      routeIdentity({ ...base, targetNamespace: 'staging', outcome: 'verified' }),
    )
  })

  it('separates different paths on the same backend', () => {
    expect(routeIdentity({ ...base, route: 'a.example.com/admin', outcome: 'verified' })).not.toBe(
      routeIdentity({ ...base, route: 'a.example.com/web', outcome: 'verified' }),
    )
  })
})

describe('a folded tab is named for what it actually folds', () => {
  const notTested = (route: string, evidence: string): RouteResult => ({ route, outcome: 'not-tested', evidence })

  // hostOf returns the whole label when there is no path separator, so a
  // Service port and a Pod were counted and announced as "2 hostnames".
  it('does not call port- and pod-shaped labels hostnames', () => {
    const same = 'the backend did not respond within the probe budget'
    const [s] = groupRoutes([notTested('port 80', same), notTested('web-6c6d9468c-bftj4 port 8080', same)])
    expect(s.hosts).toEqual([])
    expect(s.sub).toContain('2 paths')
    expect(s.sub).not.toContain('hostname')
  })

  it('still says hostnames when every member really is one', () => {
    const r = (host: string): RouteResult => ({ route: `${host}/`, target: 'web:80', outcome: 'verified', confidence: 'real' })
    const [s] = groupRoutes([r('a.example.com'), r('b.example.com')])
    expect(s.hosts).toHaveLength(2)
    expect(s.sub).toContain('2 hostnames')
  })

  it('carries every folded label for the hover, hostname or not', () => {
    const same = 'skipped'
    const [s] = groupRoutes([notTested('port 80', same), notTested('web-abc port 8080', same)])
    expect(s.members).toEqual(['port 80', 'web-abc port 8080'])
  })
})

describe('a backend-scoped pass never reads as a bare pass', () => {
  const scoped = (outcome: 'verified' | 'reached'): RouteResult =>
    r({
      outcome,
      confidence: 'real',
      byVantage: [{ vantage: 'in-cluster', path: 'data', source: 'probe-job', outcome, segment: 'backend' }],
    })

  it('appends the segment to the outcome word', () => {
    expect(routeChip(scoped('verified'))).toBe('got through · backend')
    expect(routeChip(scoped('reached'))).toBe('answered · backend')
  })

  it('a route with no front door keeps the plain words', () => {
    expect(routeChip(r({ outcome: 'verified', confidence: 'real' }))).toBe('got through')
  })

  it('routes differing only in segment stay separate scenarios', () => {
    const a = scoped('verified')
    const b = r({ outcome: 'verified', confidence: 'real', byVantage: [{ vantage: 'in-cluster', path: 'data', source: 'probe-job', outcome: 'verified' }] })
    expect(vantageSignature(a)).not.toBe(vantageSignature(b))
  })
})

describe('a preserved candidate is one gap, not two scenarios', () => {
  it('a skip row for a host a not-tested route covers is absorbed', () => {
    const s = scenariosFor(
      [{ route: 'argocd:80', target: 'argocd:80', outcome: 'not-tested' }],
      [{ route: 'argocd:80', reason: 'the API-server proxy timed out' }],
    )
    expect(s).toHaveLength(1)
  })

  it('a skip row for an UNcovered host still surfaces', () => {
    const s = scenariosFor(
      [{ route: 'argocd:80', target: 'argocd:80', outcome: 'not-tested' }],
      [{ route: 'other-svc:9090', reason: 'not reachable from here' }],
    )
    expect(s).toHaveLength(2)
  })
})

describe('a structural line explains itself, not a test that never existed', () => {
  it('ownership and selection never claim they were "not tested"', () => {
    expect(edgeHelp('runs', 'config')).toContain('ownership')
    expect(edgeHelp('runs', 'config')).not.toContain('not tested')
    expect(edgeHelp('selects', 'config')).toContain('read from the cluster')
    expect(edgeHelp('selects', 'config')).not.toContain('not tested')
  })
  it('declared routing says what WOULD exercise it', () => {
    for (const l of ['routes to', 'sends to']) {
      expect(edgeHelp(l, 'config')).toContain('request through this entry')
    }
  })
  it('anything actually observed keeps the mark vocabulary', () => {
    expect(edgeHelp('selects', 'proved')).toBe(markHelp('proved'))
    expect(edgeHelp('routes to', 'failed')).toBe(markHelp('failed'))
  })
})

describe('the rest of the edge vocabulary', () => {
  it('a collapsed sibling and an off-path backend say why, not "not tested"', () => {
    expect(edgeHelp('also serves', 'config')).toContain('collapsed')
    expect(edgeHelp('also serves', 'config')).not.toContain('not tested')
    expect(edgeHelp('other host', 'excluded')).toContain('different host')
  })
  it("a line whose own label already says it does not say it twice", () => {
    // origin edges are labelled with their evidence
    expect(edgeHelpIsRedundant('not tested', '', 'untested')).toBe(true)
    expect(edgeHelpIsRedundant('HTTP 404 · reached', '', 'proxied')).toBe(false)
  })
})
