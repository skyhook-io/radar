import { describe, it, expect } from 'vitest'
import { traceFingerprint, staticPollUnreliable } from './traceFingerprint'
import type { Trace, Hop } from './types'

// The load-bearing invariant: a ?probe=true trace (or the in-cluster merged
// trace) and a ?probe=false trace built from the SAME cluster state must
// fingerprint IDENTICALLY - the probe response is the staleness baseline, and
// each static poll is compared against it. Anything probing mutates (probes,
// verdict, routes, probe:* findings, netpol:would-deny, the targetPort
// advisory's severity) must therefore be invisible to the fingerprint, while
// every real static-state change must move it.

const svcHop = (over: Partial<Hop> = {}): Hop => ({
  resource: { kind: 'Service', namespace: 'ns', name: 'web' },
  edge: 'subject',
  findings: [],
  config: { serviceType: 'ClusterIP', ports: [{ port: 80, targetPort: '8080' }] },
  ...over,
})

const podsHop = (over: Partial<Hop> = {}): Hop => ({
  resource: { kind: 'Pods', namespace: 'ns', name: '' },
  edge: 'Service->Pods',
  findings: [],
  meta: { ready: 2, selected: 3 },
  ...over,
})

const staticTrace = (): Trace => ({
  subject: { kind: 'Service', namespace: 'ns', name: 'web' },
  upstreams: [],
  downstream: [
    svcHop({
      findings: [
        { code: 'svc:targetport-no-listener', severity: 'warning', message: 'targetPort matches no declared containerPort' },
      ],
    }),
    podsHop({
      findings: [
        { code: 'netpol:would-deny', severity: 'warning', message: 'a NetworkPolicy would deny this' },
        { code: 'problem:CrashLoopBackOff', severity: 'critical', message: 'back-off restarting' },
      ],
    }),
  ],
  verdict: 'degraded',
  brokenAt: -1,
})

// The probed twin of the SAME cluster state: probes attached, verdict revised,
// routes built from probes, findings reconciled (targetPort downgraded to
// info, netpol prediction removed after a clean divergence, probe:* finding
// added) and re-sorted.
const probedTwin = (): Trace => {
  const t = staticTrace()
  t.downstream[0].probes = [
    { layer: 'tcp', target: 'web:80', vantage: 'local', path: 'apiserver', ok: true, port: 80 },
    { layer: 'http', target: 'web:80', vantage: 'local', path: 'apiserver', ok: true, tone: 'healthy', port: 80 },
  ]
  t.downstream[0].findings = [
    { code: 'svc:targetport-no-listener', severity: 'info', message: 'a live probe reached it - nothing to fix' },
  ]
  t.downstream[1].probes = [{ layer: 'tcp', target: '10.0.0.1:8080', vantage: 'in-cluster', path: 'data', ok: false, error: 'refused' }]
  t.downstream[1].findings = [
    { code: 'problem:CrashLoopBackOff', severity: 'critical', message: 'back-off restarting' },
    { code: 'probe:data-path-only-broken', severity: 'warning', message: 'data path dropped while the proxy reached it' },
  ]
  t.verdict = 'broken'
  t.brokenAt = 1
  t.reason = 'the entry is unreachable'
  t.headline = '1 of 1 routes unreachable'
  t.coverage = { tested: 1, passed: 0, failed: 1, skipped: 0 }
  t.routes = [{ route: 'web', target: 'web:80', outcome: 'unreachable', confidence: 'real', evidence: 'refused' }]
  return t
}

describe('traceFingerprint - probe-invariance (same state, two wire shapes)', () => {
  it('a probed trace fingerprints identically to the static trace of the same cluster state', () => {
    expect(traceFingerprint(probedTwin())).toBe(traceFingerprint(staticTrace()))
  })

  it('is invariant to finding ORDER (probing re-sorts findings by severity)', () => {
    const a = staticTrace()
    const b = staticTrace()
    b.downstream[1].findings = [...b.downstream[1].findings].reverse()
    expect(traceFingerprint(a)).toBe(traceFingerprint(b))
  })

  it('ignores route outcomes - two probe runs with different results over the same state match', () => {
    const a = probedTwin()
    const b = probedTwin()
    b.routes = [{ route: 'web', target: 'web:80', outcome: 'verified', confidence: 'real' }]
    b.verdict = 'healthy'
    expect(traceFingerprint(a)).toBe(traceFingerprint(b))
  })
})

describe('traceFingerprint - real cluster changes move it', () => {
  it('changes when endpoint readiness changes', () => {
    const changed = staticTrace()
    changed.downstream[1].meta = { ready: 0, selected: 3 }
    expect(traceFingerprint(changed)).not.toBe(traceFingerprint(staticTrace()))
  })

  it('changes when a finding appears or escalates', () => {
    const appeared = staticTrace()
    appeared.downstream[0].findings.push({ code: 'svc:no-ready-endpoints', severity: 'critical', message: '0 ready' })
    expect(traceFingerprint(appeared)).not.toBe(traceFingerprint(staticTrace()))

    const escalated = staticTrace()
    escalated.downstream[1].findings = [{ code: 'problem:CrashLoopBackOff', severity: 'warning', message: 'back-off' }]
    expect(traceFingerprint(escalated)).not.toBe(traceFingerprint(staticTrace()))
  })

  it('changes when a hop is added or its identity changes', () => {
    const added = staticTrace()
    added.upstreams.push({ resource: { kind: 'Ingress', namespace: 'ns', name: 'edge' }, edge: 'Ingress->Service', findings: [] })
    expect(traceFingerprint(added)).not.toBe(traceFingerprint(staticTrace()))
  })

  it('changes when the declared route config changes (port mapping / ingress rule)', () => {
    const remapped = staticTrace()
    remapped.downstream[0].config = { serviceType: 'ClusterIP', ports: [{ port: 80, targetPort: '9090' }] }
    expect(traceFingerprint(remapped)).not.toBe(traceFingerprint(staticTrace()))

    const ruled = staticTrace()
    ruled.downstream[0].config = {
      ...ruled.downstream[0].config,
      rules: [{ hosts: ['shop.example.com'], paths: ['/api'], backends: [{ kind: 'Service', name: 'web', port: '80' }] }],
    }
    expect(traceFingerprint(ruled)).not.toBe(traceFingerprint(staticTrace()))
  })

  it('changes when the Service selector flips even though ready/selected counts stay equal', () => {
    // app=a→app=b with the SAME endpoint counts: without the selector in the
    // fingerprint the stale snapshot would keep vouching for the old backend set.
    const base = staticTrace()
    base.downstream[0].config = { ...base.downstream[0].config, selector: { app: 'a' } }
    const flipped = staticTrace()
    flipped.downstream[0].config = { ...flipped.downstream[0].config, selector: { app: 'b' } }
    expect(traceFingerprint(flipped)).not.toBe(traceFingerprint(base))
  })

  it('is invariant to selector KEY ORDER (deterministic serialization)', () => {
    const a = staticTrace()
    a.downstream[0].config = { ...a.downstream[0].config, selector: { app: 'x', tier: 'web' } }
    const b = staticTrace()
    b.downstream[0].config = { ...b.downstream[0].config, selector: { tier: 'web', app: 'x' } }
    expect(traceFingerprint(a)).toBe(traceFingerprint(b))
  })

  it('changes when clusterIP changes (Service recreated with a new VIP)', () => {
    const base = staticTrace()
    base.downstream[0].config = { ...base.downstream[0].config, clusterIP: '10.0.0.1' }
    const changed = staticTrace()
    changed.downstream[0].config = { ...changed.downstream[0].config, clusterIP: '10.0.0.2' }
    expect(traceFingerprint(changed)).not.toBe(traceFingerprint(base))
  })

  // KNOWN BLIND SPOT (documented in traceFingerprint.ts header): netpol:would-deny
  // is necessarily excluded because probing rewrites/removes it, so a NetworkPolicy
  // applied AFTER a probe snapshot does NOT move the fingerprint. The deferred
  // server-side static-state version/etag will close this gap.
  it.skip('does NOT detect a NetworkPolicy added after the snapshot (accepted client-side limitation)', () => {
    const base = staticTrace()
    const withNetpol = staticTrace()
    withNetpol.downstream[1].findings = [
      ...withNetpol.downstream[1].findings,
      { code: 'netpol:would-deny', severity: 'critical', message: 'a NetworkPolicy would deny this' },
    ]
    // Intentionally identical - the client fingerprint can't see this change.
    expect(traceFingerprint(withNetpol)).toBe(traceFingerprint(base))
  })
})

describe('staticPollUnreliable - a poll we couldn\'t fully build is not staleness evidence', () => {
  it('a healthy fully-built poll is reliable', () => {
    expect(staticPollUnreliable(staticTrace())).toBe(false)
  })

  it('a budget-timeout PARTIAL poll (verdict unknown + investigate) is unreliable', () => {
    const partial = staticTrace()
    partial.verdict = 'unknown'
    partial.unknownClass = 'investigate'
    partial.reason = 'diagnosis timed out - partial result; try again or run in-cluster'
    expect(staticPollUnreliable(partial)).toBe(true)
  })

  it('a by-design unknown (selectorless Service) stays reliable - it is a stable read', () => {
    const byDesign = staticTrace()
    byDesign.verdict = 'unknown'
    byDesign.unknownClass = 'by-design'
    expect(staticPollUnreliable(byDesign)).toBe(false)
  })

  it('a transient pod-lister failure (endpointSource=unknown) is unreliable', () => {
    const degraded = staticTrace()
    degraded.downstream[1].meta = { endpointSource: 'unknown' }
    expect(staticPollUnreliable(degraded)).toBe(true)
  })

  it('load-bearing: a degraded poll DIFFERS in fingerprint yet must NOT invalidate a good snapshot', () => {
    // The healthy snapshot baseline...
    const snapshot = staticTrace()
    const snapshotFp = traceFingerprint(snapshot)
    // ...and a later transient-failure poll whose counts/source flipped.
    const degradedPoll = staticTrace()
    degradedPoll.downstream[1].meta = { endpointSource: 'unknown' }
    // The fingerprint WOULD mismatch (proving the naive mask would false-fire)...
    expect(traceFingerprint(degradedPoll)).not.toBe(snapshotFp)
    // ...but the guard marks the poll unreliable, so the mask skips the comparison
    // and keeps the snapshot.
    expect(staticPollUnreliable(degradedPoll)).toBe(true)
  })
})
