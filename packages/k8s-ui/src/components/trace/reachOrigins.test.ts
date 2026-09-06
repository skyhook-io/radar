import { describe, it, expect } from 'vitest'
import { buildOrigins, headlineEvidenceOrigin, defaultOrigin, strongestGap, originOf, localIdentity, originSkipReason } from './reachOrigins'
import type { Trace, ProbeResult, RouteResult } from './types'

const p = (o: Partial<ProbeResult>): ProbeResult => ({ layer: 'http', target: 'svc:80', vantage: 'in-cluster', ok: true, ...o })

const traceWith = (probes: ProbeResult[]): Trace => ({
  subject: { kind: 'Service', name: 'shop', namespace: 'store' },
  verdict: 'healthy',
  brokenAt: -1,
  upstreams: [],
  downstream: [{ resource: { kind: 'Pods', name: '', namespace: 'store' }, edge: 'service->pods', findings: [], probes }],
})

describe('originOf', () => {
  it('the apiserver path wins over vantage — a relay is control plane wherever it is called from', () => {
    expect(originOf(p({ vantage: 'in-cluster', path: 'apiserver' }))).toBe('apiserver')
    expect(originOf(p({ vantage: 'in-cluster', path: 'data' }))).toBe('incluster')
    expect(originOf(p({ vantage: 'local', path: 'data' }))).toBe('local')
  })
})

describe('buildOrigins', () => {
  it('always surfaces the origins Radar cannot use, so synthetic evidence never looks complete', () => {
    const os = buildOrigins(traceWith([p({})]))
    const caller = os.find((o) => o.id === 'caller')
    const external = os.find((o) => o.id === 'external')
    expect(caller?.unsupported).toBe(true)
    expect(external?.unsupported).toBe(true)
    expect(caller?.unavailable).toBeTruthy()
    expect(external?.unavailable).toBeTruthy()
  })

  it('orders by evidence strength, strongest first', () => {
    const os = buildOrigins(traceWith([]))
    // radar-incluster is absent here: this fixture's Radar runs on a laptop.
    expect(os.map((o) => o.id)).toEqual(['caller', 'external', 'incluster', 'local', 'apiserver'])
  })

  it('marks the in-cluster probe proved on clean dataplane evidence', () => {
    const os = buildOrigins(traceWith([p({ vantage: 'in-cluster', path: 'data', ok: true })]))
    expect(os.find((o) => o.id === 'incluster')?.mark).toBe('proved')
  })

  it('never marks the apiserver origin proved, however clean the response', () => {
    const os = buildOrigins(traceWith([p({ vantage: 'local', path: 'apiserver', ok: true, tone: 'healthy' })]))
    expect(os.find((o) => o.id === 'apiserver')?.mark).toBe('proxied')
  })

  it('a failure among an origin’s probes wins over its successes', () => {
    const os = buildOrigins(traceWith([p({ ok: true }), p({ ok: false, tone: 'unhealthy' })]))
    expect(os.find((o) => o.id === 'incluster')?.mark).toBe('failed')
  })

  it('an all-skipped origin is blocked, not failed, and says why', () => {
    const os = buildOrigins(traceWith([p({ vantage: 'local', path: 'data', skipped: true, reason: 'ClusterIP is not routable from your host' })]))
    const local = os.find((o) => o.id === 'local')
    expect(local?.mark).toBe('blocked')
    expect(local?.unavailable).toMatch(/not routable/)
  })

  // A refused relay says nothing about the workload. Scoring it as a failure
  // painted the vantage red for a permissions gap, and 'blocked' would have
  // hidden that there is a grant to ask for.
  it('reads a refused relay as not permitted, and says what to grant', () => {
    const os = buildOrigins(
      traceWith([
        p({
          vantage: 'local',
          path: 'apiserver',
          skipped: true,
          skipClass: 'denied',
          reason: 'Permission denied. Your identity lacks get services/proxy or get pods/proxy in this namespace.',
        }),
      ]),
    )
    const api = os.find((o) => o.id === 'apiserver')
    expect(api?.mark).toBe('denied')
    expect(api?.unavailable).toMatch(/services\/proxy/)
  })

  it('reports denied rather than untested when the probe is not permitted', () => {
    const os = buildOrigins(traceWith([]), { inClusterAllowed: false, inClusterDeniedReason: 'RBAC denies create on jobs' })
    const ic = os.find((o) => o.id === 'incluster')
    expect(ic?.mark).toBe('denied')
    expect(ic?.unavailable).toMatch(/RBAC/)
  })

  it('running beats any prior mark so nothing turns green optimistically', () => {
    const os = buildOrigins(traceWith([p({ ok: true })]), { inClusterRunning: true })
    expect(os.find((o) => o.id === 'incluster')?.mark).toBe('running')
  })

  it('stale evidence is greyed rather than carried forward as proof', () => {
    const os = buildOrigins(traceWith([p({ ok: true })]), { stale: true })
    expect(os.find((o) => o.id === 'incluster')?.mark).toBe('stale')
  })
})

describe('defaultOrigin', () => {
  it('opens on the strongest origin that actually produced evidence', () => {
    const os = buildOrigins(traceWith([p({ vantage: 'local', path: 'apiserver' })]))
    expect(defaultOrigin(os)).toBe('apiserver')
  })

  it('prefers real dataplane evidence over a relay when both exist', () => {
    const os = buildOrigins(traceWith([p({ vantage: 'local', path: 'apiserver' }), p({ vantage: 'in-cluster', path: 'data' })]))
    expect(defaultOrigin(os)).toBe('incluster')
  })

  it('falls back to a usable origin when nothing has run', () => {
    expect(defaultOrigin(buildOrigins(traceWith([])))).toBe('incluster')
  })
})

describe('strongestGap', () => {
  it('names the strongest untested origin as the remaining gap', () => {
    const os = buildOrigins(traceWith([p({ vantage: 'in-cluster', path: 'data' })]))
    // caller is unsupported+blocked and is the strongest thing still missing.
    expect(strongestGap(os)?.id).toBe('caller')
  })
})

// Once the graph and inspector became route-scoped, the rail scoring an origin
// across EVERY route at once could contradict them: a vantage that failed on one
// path read as failed while the graph showed it succeeding on the selected one.
describe('rail marks follow the selected route', () => {
  const twoRoutes = (): RouteResult => ({
    route: 'a.example.com/',
    target: 'a:80',
    outcome: 'unreachable',
    confidence: 'real',
    byVantage: [
      { vantage: 'in-cluster', path: 'data', outcome: 'verified', confidence: 'real' },
      { vantage: 'local', path: 'data', outcome: 'unreachable', confidence: 'real' },
    ],
  })
  const t = traceWith([
    { layer: 'http', target: 'a:80', vantage: 'in-cluster', path: 'data', ok: false, tone: 'unhealthy' },
  ])

  it('scores each origin by its own result for THIS route', () => {
    const os = buildOrigins(t, { route: twoRoutes() })
    expect(os.find((o) => o.id === 'incluster')!.mark).toBe('proved')
    expect(os.find((o) => o.id === 'local')!.mark).toBe('failed')
  })

  it('without a route it falls back to the pooled scan, as before', () => {
    // A failing in-cluster probe is on the trace, so the pooled path marks it failed.
    const os = buildOrigins(t, {})
    expect(os.find((o) => o.id === 'incluster')!.mark).toBe('failed')
  })

  it('an origin with no result for this route is untested, not inherited', () => {
    const onlyLocal: RouteResult = {
      route: 'a/',
      outcome: 'verified',
      byVantage: [{ vantage: 'local', path: 'data', outcome: 'verified', confidence: 'real' }],
    }
    const os = buildOrigins(traceWith([]), { route: onlyLocal })
    expect(os.find((o) => o.id === 'local')!.mark).toBe('proved')
    expect(os.find((o) => o.id === 'incluster')!.mark).toBe('untested')
  })
})

// The laptop vantage used to answer "we can't tell where on the network you are"
// for every trace forever, even though DNS had already resolved the address.
describe('the laptop vantage says where it actually dialled', () => {
  const dns = (scope?: 'public' | 'private' | 'mixed', skipped = false): ProbeResult =>
    ({ layer: 'dns', target: 'checkout.example.com', vantage: 'local', ok: true, addressScope: scope, skipped })

  it('a globally routable address is stated, with the inference it does NOT support', () => {
    const s = localIdentity([dns('public')])
    expect(s).toMatch(/public address/)
    // a public address is not proof the packet crossed the internet
    expect(s).toMatch(/VPN|split-horizon/)
  })

  it('a private address rules the public internet out', () => {
    expect(localIdentity([dns('private')])).toMatch(/did not come from the public internet/)
  })

  it('split-horizon resolves both ways and commits to neither', () => {
    expect(localIdentity([dns('mixed')])).toMatch(/both public and private/)
  })

  it('falls back to the honest unknown when nothing resolved', () => {
    expect(localIdentity([])).toMatch(/can’t tell where/)
    expect(localIdentity([dns(undefined)])).toMatch(/can’t tell where/)
  })

  it('ignores skipped lookups, which observed nothing', () => {
    expect(localIdentity([dns('public', true)])).toMatch(/can’t tell where/)
  })

  it('does not make the external vantage supported - that is a different claim', () => {
    const t = traceWith([{ layer: 'dns', target: 'h', vantage: 'local', ok: true, addressScope: 'public' }])
    expect(buildOrigins(t, {}).find((o) => o.id === 'external')!.unsupported).toBe(true)
  })
})

describe('Radar-as-a-Pod is not the throwaway probe Job', () => {
  // Both are (in-cluster, data). Collapsed into one origin, Radar's own dials
  // were credited to a Job that was never created, under the namespace default
  // account rather than Radar's own.
  it('routes an explicitly-sourced probe to its own origin', () => {
    expect(originOf(p({ vantage: 'in-cluster', path: 'data', source: 'radar' }))).toBe('radar-incluster')
    expect(originOf(p({ vantage: 'in-cluster', path: 'data', source: 'probe-job' }))).toBe('incluster')
  })

  // An older producer sends no source. Radar can only have dialled in-cluster
  // itself when it IS in the cluster; otherwise the only way to get such a
  // probe is the Job. runVantage is the producer's own statement about itself.
  it('resolves an unsourced in-cluster probe from runVantage', () => {
    const probe = p({ vantage: 'in-cluster', path: 'data' })
    expect(originOf(probe, 'in-cluster')).toBe('radar-incluster')
    expect(originOf(probe, 'local')).toBe('incluster')
    expect(originOf(probe)).toBe('incluster')
  })

  it('never reassigns an apiserver relay, whatever the source', () => {
    expect(originOf(p({ vantage: 'in-cluster', path: 'apiserver', source: 'radar' }), 'in-cluster')).toBe('apiserver')
  })

  // Radar not being a Pod is a deployment detail, not a hole in the evidence -
  // the throwaway Job covers the same ground. Listing it as "never tested" pads
  // the coverage caveat beside genuine gaps like "as your application".
  it('omits Radar-in-cluster entirely when Radar runs on a laptop', () => {
    const os = buildOrigins(traceWith([]))
    expect(os.find((o) => o.id === 'radar-incluster')).toBeUndefined()
    expect(os.map((o) => o.id)).toEqual(['caller', 'external', 'incluster', 'local', 'apiserver'])
  })

  it('offers it once Radar actually runs in the cluster', () => {
    const t = traceWith([p({ vantage: 'in-cluster', path: 'data', source: 'radar', ok: true })])
    t.runVantage = 'in-cluster'
    const radar = buildOrigins(t).find((o) => o.id === 'radar-incluster')
    expect(radar?.unsupported).toBeFalsy()
  })

  // The mirror rule. A Radar that runs as a Pod has no process on the operator's
  // machine, so a workstation vantage there is not a test anyone can run - it is
  // a control that would never do anything, standing where a real gap should be.
  it('omits the workstation vantage entirely when Radar runs in the cluster', () => {
    const t = traceWith([p({ vantage: 'in-cluster', path: 'data', source: 'radar', ok: true })])
    t.runVantage = 'in-cluster'
    const os = buildOrigins(t)
    expect(os.find((o) => o.id === 'local')).toBeUndefined()
    expect(os.map((o) => o.id)).toEqual(['caller', 'external', 'incluster', 'radar-incluster', 'apiserver'])
  })

  it('keeps the external gap declared there — it is what nothing can prove', () => {
    const t = traceWith([p({ vantage: 'in-cluster', path: 'data', source: 'radar', ok: true })])
    t.runVantage = 'in-cluster'
    expect(buildOrigins(t).find((o) => o.id === 'external')?.unsupported).toBe(true)
  })

  // Hiding a vantage must never hide evidence. If a laptop-side result somehow
  // arrives on an in-cluster trace, the row it belongs to has to stay.
  it('keeps the workstation vantage when it actually produced a result', () => {
    const t = traceWith([p({ vantage: 'local', path: 'data', ok: true })])
    t.runVantage = 'in-cluster'
    expect(buildOrigins(t).find((o) => o.id === 'local')).toBeDefined()
  })

  it('keeps it on a laptop trace that has not been probed yet', () => {
    const t = traceWith([])
    t.runVantage = 'local'
    expect(buildOrigins(t).find((o) => o.id === 'local')).toBeDefined()
  })

  it('describes Radar-in-cluster with Radar\'s own identity, not the Job\'s', () => {
    const t = traceWith([p({ vantage: 'in-cluster', path: 'data', source: 'radar', ok: true })])
    t.runVantage = 'in-cluster'
    const radar = buildOrigins(t).find((o) => o.id === 'radar-incluster')
    expect(radar?.identity).toMatch(/Radar/)
    expect(radar?.identity).not.toMatch(/default account/)
    expect(radar?.mark).toBe('proved')
  })
})

describe('an unanswered capability check is not a denial', () => {
  // The SSAR takes a moment. While it is in flight the answer is UNKNOWN, and
  // saying "not permitted" for those frames claims a denial that was never
  // established - the exact overclaim this view exists to prevent.
  it('reads as untested, not denied, while the check is in flight', () => {
    const t = traceWith([])
    const pending = buildOrigins(t, { inClusterAllowed: undefined }).find((o) => o.id === 'incluster')!
    expect(pending.mark).toBe('untested')
    expect(pending.unavailable).toBeUndefined()
  })

  it('reads as denied once the check actually says no', () => {
    const t = traceWith([])
    const denied = buildOrigins(t, { inClusterAllowed: false, inClusterDeniedReason: 'needs create jobs' }).find(
      (o) => o.id === 'incluster',
    )!
    expect(denied.mark).toBe('denied')
    expect(denied.unavailable).toMatch(/create jobs/)
  })
})

describe('a demoted Job run is inconclusive, never "never tried"', () => {
  // The producer demotes a probe that DIALLED and was refused/degraded to a
  // skip with skipClass 'informational' - a disposition, not a coverage gap.
  it('maps all-skipped-with-informational to inconclusive', () => {
    const t: Trace = {
      subject: { kind: 'Service', name: 'web', namespace: 'prod' },
      verdict: 'unknown',
      brokenAt: -1,
      upstreams: [],
      downstream: [
        {
          resource: { kind: 'Service', name: 'web', namespace: 'prod' },
          edge: 'service',
          findings: [],
          probes: [
            { layer: 'http', target: 'web:80', vantage: 'in-cluster', path: 'data', source: 'probe-job', ok: false, skipped: true, skipClass: 'informational', reason: 'kept informational' },
          ],
        },
      ],
    }
    const o = buildOrigins(t).find((x) => x.id === 'incluster')!
    expect(o.mark).toBe('inconclusive')
  })

  it('an ordinary all-skipped origin still reads blocked', () => {
    const t: Trace = {
      subject: { kind: 'Service', name: 'web', namespace: 'prod' },
      verdict: 'unknown',
      brokenAt: -1,
      upstreams: [],
      downstream: [
        {
          resource: { kind: 'Service', name: 'web', namespace: 'prod' },
          edge: 'service',
          findings: [],
          probes: [
            { layer: 'tcp', target: '10.96.0.1:80', vantage: 'local', path: 'data', ok: false, skipped: true, reason: 'a ClusterIP is not routable from a laptop' },
          ],
        },
      ],
    }
    const o = buildOrigins(t).find((x) => x.id === 'local')!
    expect(o.mark).toBe('blocked')
  })
})

describe('first paint agrees with the headline', () => {
  it('an indirect rollup default-selects the API-server vantage', () => {
    expect(headlineEvidenceOrigin({ route: 'GET /', outcome: 'reached', confidence: 'indirect' } as any)).toBe('apiserver')
  })

  it('a real in-cluster verify default-selects the probe vantage', () => {
    expect(
      headlineEvidenceOrigin({
        route: 'GET /',
        outcome: 'verified',
        confidence: 'real',
        byVantage: [{ vantage: 'in-cluster', path: 'data', source: 'probe-job', outcome: 'verified', confidence: 'real' }],
      } as any),
    ).toBe('incluster')
  })

  it('an untested route decides nothing', () => {
    expect(headlineEvidenceOrigin({ route: 'GET /', outcome: 'not-tested' } as any)).toBeUndefined()
  })
})

describe('a laptop with nothing to dial says so', () => {
  const base = () => ({
    subject: { kind: 'Service', name: 'shop', namespace: 'store' },
    verdict: 'unknown', brokenAt: -1, upstreams: [],
    downstream: [{ resource: { kind: 'Service', name: 'shop', namespace: 'store' }, edge: 'service', findings: [],
      probes: [{ layer: 'http', target: 'shop:80', vantage: 'local', path: 'apiserver', ok: false, skipped: true, reason: 'x', port: 80 }] }],
  }) as never
  it('after a run with no front door, the local vantage states why it cannot be used', () => {
    const local = buildOrigins(base()).find((o) => o.id === 'local')!
    expect(local.mark).toBe('blocked')
    expect(local.unavailable).toMatch(/no entry point .* from your machine|no Ingress/i)
  })
  it('with no probes at all it stays plainly untested - nothing is claimed', () => {
    const t = base() as { downstream: { probes?: unknown[] }[] }
    t.downstream[0].probes = []
    const local = buildOrigins(t as never).find((o) => o.id === 'local')!
    expect(local.mark).toBe('untested')
    expect(local.unavailable).toBeUndefined()
  })
  it('with a front door the laptop CAN dial, nothing is claimed either', () => {
    const t = base() as { upstreams: unknown[] }
    t.upstreams = [{ resource: { kind: 'Ingress', name: 'in', namespace: 'store' }, edge: 'ingress->service', findings: [] }]
    const local = buildOrigins(t as never).find((o) => o.id === 'local')!
    expect(local.unavailable).toBeUndefined()
  })
})

describe('an externally addressable Service is not told it has no entry point', () => {
  const mkTyped = (serviceType?: string) => ({
    subject: { kind: 'Service', name: 'shop', namespace: 'store' },
    verdict: 'unknown', brokenAt: -1, upstreams: [],
    downstream: [{ resource: { kind: 'Service', name: 'shop', namespace: 'store' }, edge: 'service', findings: [],
      config: { serviceType },
      probes: [{ layer: 'http', target: 'shop:80', vantage: 'local', path: 'apiserver', ok: false, skipped: true, reason: 'x', port: 80 }] }],
  }) as never
  it('LoadBalancer: the claim is about Radar, not the Service', () => {
    const local = buildOrigins(mkTyped('LoadBalancer')).find((o) => o.id === 'local')!
    expect(local.unavailable).toMatch(/doesn.t dial/i)
    expect(local.unavailable).not.toMatch(/no Ingress/i)
  })
  it('ClusterIP keeps the structural no-entry-point wording', () => {
    const local = buildOrigins(mkTyped('ClusterIP')).find((o) => o.id === 'local')!
    expect(local.unavailable).toMatch(/no Ingress/i)
  })
})

describe('a skip reason never crosses backends', () => {
  it('a named backend takes reasons only from its own hop', () => {
    const t = {
      subject: { kind: 'Ingress', name: 'entry', namespace: 'store' },
      verdict: 'unknown', brokenAt: -1, upstreams: [], runVantage: 'local',
      downstream: [
        { resource: { kind: 'Service', name: 'shop-a', namespace: 'store' }, edge: 'service', findings: [],
          probes: [{ layer: 'http', target: 'port 80', vantage: 'local', path: 'apiserver', ok: false, skipped: true, reason: 'A-side reason', port: 80 }] },
        { resource: { kind: 'Service', name: 'shop-b', namespace: 'store' }, edge: 'service', findings: [],
          probes: [{ layer: 'http', target: 'port 80', vantage: 'local', path: 'apiserver', ok: false, skipped: true, reason: 'B-side reason', port: 80 }] },
      ],
    } as never
    expect(originSkipReason(t, 'apiserver', { route: 'r', target: 'shop-b:80', outcome: 'not-tested' } as never)).toBe('B-side reason')
    expect(originSkipReason(t, 'apiserver', { route: 'r', target: 'shop-a:80', outcome: 'not-tested' } as never)).toBe('A-side reason')
  })
  it("a named backend with no skips of its own borrows nothing", () => {
    const t = {
      subject: { kind: 'Ingress', name: 'entry', namespace: 'store' },
      verdict: 'unknown', brokenAt: -1, upstreams: [], runVantage: 'local',
      downstream: [
        { resource: { kind: 'Service', name: 'shop-a', namespace: 'store' }, edge: 'service', findings: [],
          probes: [{ layer: 'http', target: 'port 80', vantage: 'local', path: 'apiserver', ok: false, skipped: true, reason: 'A-side reason', port: 80 }] },
        { resource: { kind: 'Service', name: 'shop-b', namespace: 'store' }, edge: 'service', findings: [], probes: [] },
      ],
    } as never
    expect(originSkipReason(t, 'apiserver', { route: 'r', target: 'shop-b:80', outcome: 'not-tested' } as never)).toBeUndefined()
  })
})
