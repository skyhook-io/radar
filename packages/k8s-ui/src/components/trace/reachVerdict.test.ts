import { describe, it, expect } from 'vitest'
import { reachVerdict, hostFromTarget, VIA_API_SERVER } from './reachVerdict'
import type { Trace, RouteResult, Hop, ProbeResult } from './types'

// Anti-drift pin: the one operator-facing name for the apiserver-proxy vantage
// MUST stay identical to the Go const VantageAPIServer (coverage.go), which
// TestVantageAPIServerName pins to the same literal.
describe('VIA_API_SERVER - single vantage name, must match the Go const', () => {
  it('is exactly "via API server"', () => {
    expect(VIA_API_SERVER).toBe('via API server')
  })
})

const trace = (o: Partial<Trace>): Trace => ({
  subject: { kind: 'Service', namespace: 'p', name: 's' },
  upstreams: [], downstream: [], verdict: 'healthy', brokenAt: -1, ...o,
})
const route = (o: Partial<RouteResult>): RouteResult => ({ route: '/', outcome: 'verified', ...o })

// An Ingress subject whose entry-host probe(s) live on its own hop. The subject is the
// FRONT DOOR, so the verdict must speak to it - not let the backend route speak for it.
const probe = (o: Partial<ProbeResult>): ProbeResult => ({ layer: 'http', target: 'https://shop.example.com/', vantage: 'local', ok: true, ...o })
const ingressTrace = (hostProbes: ProbeResult[], routes: RouteResult[] = [route({ outcome: 'reached', confidence: 'indirect' })]): Trace =>
  trace({
    subject: { kind: 'Ingress', namespace: 'p', name: 'ing' },
    downstream: [{ resource: { kind: 'Ingress', name: 'ing' }, edge: 'entry:Ingress', findings: [], probes: hostProbes } as Hop],
    routes,
  })

describe('hostFromTarget - host extraction (linear, no ReDoS-prone regex)', () => {
  it('strips scheme, path, and trailing numeric port', () => {
    expect(hostFromTarget('http://shop.example.com:80/api/v1')).toBe('shop.example.com')
    expect(hostFromTarget('https://host:8443/')).toBe('host')
    expect(hostFromTarget('svc.ns:9090')).toBe('svc.ns')
    expect(hostFromTarget('plainhost')).toBe('plainhost')
    expect(hostFromTarget('')).toBe('')
    expect(hostFromTarget(undefined)).toBe('')
  })
})

describe('reachVerdict - a front-door 502/504 is a network fault, never "check app logs"', () => {
  it('a degraded HTTP front door (502/504) says it could not reach the backend, and never asserts the network is fine or points at app logs', () => {
    const v = reachVerdict(
      ingressTrace([probe({ layer: 'http', ok: true, tone: 'degraded', detail: "HTTP 502 · the front door couldn't reach the backend" })]),
      true,
    )
    expect(v.tone).toBe('degraded')
    expect(v.text).toContain("couldn't reach the backend")
    // The 5xx reclassification (app 5xx = reached) leaves only 502/504/cert here, so
    // these old-bucket assertions must never appear on a real network-path fault.
    expect(v.text).not.toContain('the app returned a server error')
    expect(v.text).not.toContain('network path is fine')
    expect(v.text).not.toContain("check the app's logs")
  })
})

describe('reachVerdict - a degraded route names the layer that failed (per-layer model)', () => {
  it('failedLayer "tls" reads as a TLS/certificate failure, never a generic "server error"', () => {
    const v = reachVerdict(trace({ verdict: 'degraded', routes: [route({ outcome: 'server-error', confidence: 'real', failedLayer: 'tls' })] }), true)
    expect(v.tone).toBe('degraded')
    expect(v.text).toMatch(/TLS|certificate/i)
    expect(v.text).not.toContain('server error')
    expect(v.text).not.toContain('app')
  })
  it('failedLayer "upstream" (a 502/504) reads as HTTP-reached but the backend/upstream unreachable', () => {
    const v = reachVerdict(trace({ verdict: 'degraded', routes: [route({ outcome: 'server-error', confidence: 'real', failedLayer: 'upstream' })] }), true)
    expect(v.tone).toBe('degraded')
    expect(v.text).toContain("couldn't reach its backend")
    expect(v.text).not.toContain('server error')
  })
})

describe('reachVerdict - an ingress-controller problem leads the verdict', () => {
  it('no-controller finding on the entry hop headlines the banner (not "backend reachable, entry not tested")', () => {
    const t = trace({
      subject: { kind: 'Ingress', namespace: 'p', name: 'orphan' },
      verdict: 'degraded',
      downstream: [{ resource: { kind: 'Ingress', name: 'orphan' }, edge: 'entry:Ingress', findings: [
        { code: 'ingress:no-controller', severity: 'warning', message: 'No ingress controller is handling this - the routing rules exist but nothing is serving them.' },
      ], probes: [probe({ layer: 'dns', ok: false, skipped: true, reason: 'no resolve' })] } as Hop],
      routes: [route({ outcome: 'reached', confidence: 'indirect' })],
    })
    const v = reachVerdict(t, true)
    expect(v.tone).toBe('degraded')
    expect(v.text).toContain('No ingress controller is handling this')
    expect(v.text).not.toContain('entry point wasn')
  })
})

describe('reachVerdict - no live backend leads with the cause, not "via API server"', () => {
  const backendDownTrace = (podsFindings: Hop['findings'], selected: number): Trace =>
    trace({
      verdict: 'broken',
      brokenAt: 0,
      downstream: [
        // The Service symptom carries a Pod ref in reality (linkNoReadyToCulprit) -
        // it must NOT outrank the specific pod-failure cause for the headline.
        { resource: { kind: 'Service', name: 's' }, edge: 'entry:Service', findings: [
          { code: `problem:0/${selected} selected pods ready`, severity: 'critical', message: `0/${selected} selected pods ready`, resource: { kind: 'Pod', name: 'culprit' } },
        ] } as Hop,
        { resource: { kind: 'Pods', name: '' }, edge: 'Service->Pods', meta: { ready: 0, selected }, findings: podsFindings } as Hop,
      ],
      routes: [route({ outcome: 'unreachable', confidence: 'indirect', evidence: 'No ready backend endpoints' })],
    })

  it('pods wired but 0 ready (crashloop) → headline NAMES the failing container from the pod diagnosis, not a generic "backend app"', () => {
    const v = reachVerdict(backendDownTrace([
      { code: 'problem:CrashLoopBackOff', severity: 'critical', message: 'CrashLoopBackOff', cause: 'container "c" is crashlooping after exit code 1.', resource: { kind: 'Pod', name: 'crashloop-x' } },
    ], 1), true)
    expect(v.tone).toBe('unhealthy')
    expect(v.text).toBe('No healthy backend - container "c" is crashlooping after exit code 1.')
    expect(v.text).not.toContain('management API')
    expect(v.text).not.toContain('the backend app')
    expect(v.caveat).toContain('wired')
    expect(v.caveat).toContain('the break is in the pods')
    expect(v.caveat).toContain('0/1 pod ready')
  })

  it('image-pull failure reads plainly + pluralizes the pod count', () => {
    const v = reachVerdict(backendDownTrace([
      { code: 'problem:ImagePullBackOff', severity: 'critical', message: 'ImagePullBackOff', resource: { kind: 'Pod', name: 'bad' } },
    ], 2), true)
    expect(v.text).toContain("can't pull its image")
    expect(v.caveat).toContain('0/2 pods ready')
  })

  it('0 ready but NO failure finding (fresh rollout / pending) → amber "may be starting", NOT a hard ✗ - a transitional state isn\'t an outage', () => {
    const v = reachVerdict(backendDownTrace([], 1), true)
    expect(v.tone).toBe('degraded')
    expect(v.icon).toBe('⚠')
    expect(v.text).toContain('No ready backend yet')
    expect(v.text).not.toContain('No healthy backend')
    expect(v.caveat).toContain('starting')
  })

  it('partial-ready floored (1/2 pods, one crashing) → headline LEADS with the "N of M ready" count + the problem, never the optimistic "Reached"', () => {
    const v = reachVerdict(trace({
      verdict: 'degraded',
      downstream: [
        { resource: { kind: 'Service', name: 's' }, edge: 'entry:Service', findings: [] } as Hop,
        { resource: { kind: 'Pods', name: '' }, edge: 'Service->Pods', meta: { ready: 1, selected: 2 }, findings: [
          { code: 'problem:CrashLoopBackOff', severity: 'critical', message: 'Container "web" is crash-looping (CrashLoopBackOff)' },
        ] } as Hop,
      ],
      routes: [route({ outcome: 'reached', confidence: 'indirect' })],
    }), true)
    expect(v.tone).toBe('degraded')
    // The readiness split leads the warning message.
    expect(v.text).toBe('1 of 2 pods ready - Container "web" is crash-looping (CrashLoopBackOff)')
    expect(v.text).not.toContain('Reached')
  })

  it('partial-ready count is NOT doubled when the finding already carries one', () => {
    const v = reachVerdict(trace({
      verdict: 'degraded',
      downstream: [
        { resource: { kind: 'Service', name: 's' }, edge: 'entry:Service', findings: [] } as Hop,
        { resource: { kind: 'Pods', name: '' }, edge: 'Service->Pods', meta: { ready: 1, selected: 2 }, findings: [
          { code: 'problem:CrashLoopBackOff', severity: 'critical', message: 'A pod is crash-looping (1 of 2 down)' },
        ] } as Hop,
      ],
      routes: [route({ outcome: 'reached', confidence: 'indirect' })],
    }), true)
    expect(v.text).toBe('A pod is crash-looping (1 of 2 down)')
  })

  it('pod-state finding headlines the clean `cause`, not the raw kubelet `message` (no back-off/pod-UID noise)', () => {
    const v = reachVerdict(trace({
      verdict: 'degraded',
      downstream: [
        { resource: { kind: 'Service', name: 's' }, edge: 'entry:Service', findings: [] } as Hop,
        { resource: { kind: 'Pods', name: '' }, edge: 'Service->Pods', meta: { ready: 1, selected: 2 }, findings: [
          { code: 'problem:CrashLoopBackOff', severity: 'critical',
            message: 'CrashLoopBackOff - back-off 5m0s restarting failed container=c pod=dual-bad-844665666-9tl6z_radar-netdiag(7fd8a1c5)',
            cause: 'Container "c" is crash-looping (CrashLoopBackOff)' },
        ] } as Hop,
      ],
      routes: [route({ outcome: 'reached', confidence: 'indirect' })],
    }), true)
    expect(v.text).toBe('1 of 2 pods ready - Container "c" is crash-looping (CrashLoopBackOff)')
    expect(v.text).not.toContain('back-off')
    expect(v.text).not.toContain('pod=')
  })

  it('does NOT prepend "N of M pods ready" when the headline is a NON-pod concern (partial pods, but a config finding leads)', () => {
    const v = reachVerdict(trace({
      verdict: 'degraded',
      downstream: [
        { resource: { kind: 'Service', name: 's' }, edge: 'entry:Service', findings: [
          { code: 'netpol:would-deny', severity: 'critical', message: 'A cluster network rule would block traffic to these pods on :80 - no rule allows it.' },
        ] } as Hop,
        { resource: { kind: 'Pods', name: '' }, edge: 'Service->Pods', meta: { ready: 1, selected: 2 }, findings: [] } as Hop,
      ],
      routes: [route({ outcome: 'reached', confidence: 'indirect' })],
    }), true)
    expect(v.text).not.toContain('pods ready')
    expect(v.text).not.toMatch(/1 of 2/)
  })

  it('multi-backend: the "N of M ready" count comes from the DIAGNOSED hop, not the first Pods hop', () => {
    const v = reachVerdict(trace({
      verdict: 'degraded',
      downstream: [
        { resource: { kind: 'Service', name: 's' }, edge: 'entry:Service', findings: [] } as Hop,
        // First Pods hop: a different backend, partially up, NO finding.
        { resource: { kind: 'Pods', name: 'a' }, edge: 'Service->Pods', meta: { ready: 1, selected: 2 }, findings: [] } as Hop,
        // Second Pods hop: the DIAGNOSED backend - carries the critical finding + its own count.
        { resource: { kind: 'Pods', name: 'b' }, edge: 'Service->Pods', meta: { ready: 1, selected: 3 }, findings: [
          { code: 'problem:CrashLoopBackOff', severity: 'critical', message: 'Container "c" is crash-looping (CrashLoopBackOff)' },
        ] } as Hop,
      ],
      routes: [route({ outcome: 'reached', confidence: 'indirect' })],
    }), true)
    expect(v.text).toBe('1 of 3 pods ready - Container "c" is crash-looping (CrashLoopBackOff)')
    expect(v.text).not.toContain('1 of 2')
  })

  it('multi-backend with ONE healthy backend does NOT condemn "No healthy backend" - a serving sibling means the entry can still serve', () => {
    const v = reachVerdict(trace({
      subject: { kind: 'Ingress', namespace: 'p', name: 'multi' },
      verdict: 'degraded',
      downstream: [
        { resource: { kind: 'Ingress', name: 'multi' }, edge: 'entry:Ingress', findings: [] } as Hop,
        { resource: { kind: 'Pods', name: '' }, edge: 'Ingress->Pods', meta: { ready: 0, selected: 1 }, findings: [
          { code: 'problem:CrashLoopBackOff', severity: 'critical', message: 'CrashLoopBackOff', resource: { kind: 'Pod', name: 'dead' } },
        ] } as Hop,
        { resource: { kind: 'Pods', name: '' }, edge: 'Ingress->Pods', meta: { ready: 2, selected: 2 }, findings: [] } as Hop,
      ],
      routes: [route({ route: '/ok', outcome: 'verified', confidence: 'real' }), route({ route: '/dead', outcome: 'unreachable', confidence: 'indirect' })],
    }), true)
    expect(v.text).not.toContain('No healthy backend')
  })

  it('an unreadable (RBAC-redacted) sibling backend blocks the "No healthy backend" headline - an unseen backend might be serving', () => {
    const v = reachVerdict(trace({
      subject: { kind: 'Ingress', namespace: 'p', name: 'multi' },
      verdict: 'broken',
      downstream: [
        { resource: { kind: 'Pods', name: '' }, edge: 'Ingress->Pods', meta: { ready: 0, selected: 1 }, findings: [
          { code: 'problem:CrashLoopBackOff', severity: 'critical', message: 'CrashLoopBackOff', resource: { kind: 'Pod', name: 'dead' } },
        ] } as Hop,
        { resource: { kind: 'Service', name: 'other' }, edge: 'Ingress->Service', findings: [
          { code: 'rbac:cross-namespace-redacted', severity: 'info', message: 'backend in another namespace - not readable' },
        ] } as Hop,
      ],
      routes: [route({ route: '/dead', outcome: 'unreachable', confidence: 'indirect' })],
    }), true)
    expect(v.text).not.toContain('No healthy backend')
  })

  it('a selectorless (manual-endpoints) sibling backend blocks "No healthy backend" - we can\'t judge it from pod readiness', () => {
    const v = reachVerdict(trace({
      subject: { kind: 'Ingress', namespace: 'p', name: 'mix' },
      verdict: 'broken',
      downstream: [
        { resource: { kind: 'Pods', name: '' }, edge: 'Ingress->Pods', meta: { ready: 0, selected: 1 }, findings: [
          { code: 'problem:CrashLoopBackOff', severity: 'critical', message: 'CrashLoopBackOff', resource: { kind: 'Pod', name: 'dead' } },
        ] } as Hop,
        { resource: { kind: 'Service', name: 'manual' }, edge: 'Ingress->Service', meta: { selectorless: true }, findings: [
          { code: 'svc:selectorless', severity: 'info', message: 'Service has no selector; endpoints are managed manually' },
        ] } as Hop,
      ],
      routes: [route({ route: '/dead', outcome: 'unreachable', confidence: 'indirect' })],
    }), true)
    expect(v.text).not.toContain('No healthy backend')
  })

  it('scaled-to-0 (0 pods SELECTED) does NOT trigger backend-down - it keeps its own benign read', () => {
    const v = reachVerdict(trace({
      verdict: 'degraded',
      downstream: [
        { resource: { kind: 'Pods', name: '' }, edge: 'Service->Pods', meta: { ready: 0, selected: 0 }, findings: [] } as Hop,
      ],
      routes: [route({ outcome: 'unreachable', confidence: 'indirect', benign: true })],
    }), true)
    expect(v.text).not.toContain('No healthy backend')
  })
})

describe('reachVerdict - Ingress front door is stated separately from the backend', () => {
  it('host REACHED from outside → ✓ "Front door confirmed from outside" (success is first-class, not buried)', () => {
    // A genuine 2xx carries tone 'healthy' - only that earns "confirmed" (a bare
    // TCP/TLS connect or a 5xx must not, per the front-door honesty fix).
    const v = reachVerdict(ingressTrace([probe({ layer: 'http', ok: true, tone: 'healthy', detail: 'HTTP 200' })]), true)
    expect(v.icon).toBe('✓')
    expect(v.text).toContain('Front door confirmed from outside')
  })
  it('host DNS skipped (split-horizon) + backend reachable → neutral, names front door NOT verified, never "confirmed"', () => {
    const v = reachVerdict(ingressTrace([probe({ layer: 'dns', ok: false, skipped: true, reason: 'doesn’t resolve from here' })]), true)
    expect(v.icon).toBe('')
    expect(v.tone).toBe('neutral')
    // Headline connects the two facts (backend up BUT entry untested); the caveat
    // explains how the backend was reached + the action. Never claim the entry
    // works, and never the self-contradicting "verify from outside".
    expect(v.text).toBe('Backend answered via API server - but the Ingress entry point wasn’t tested')
    // Short caveat stays on screen; the full why + fix lives in detail (tooltip).
    expect(v.caveat?.toLowerCase()).toContain('didn’t resolve')
    expect(v.caveat!.length).toBeLessThan(90) // one line, not a wall of text
    expect(v.detail?.toLowerCase()).toContain('request shop.example.com from a client that can resolve it')
    expect(v.caveat).not.toContain('verify from outside')
    expect(v.detail).not.toContain('verify from outside')
    expect(v.text).not.toContain('confirmed from inside')
  })
  it('host genuinely FAILED (transport) → ✗ front door unreachable', () => {
    const v = reachVerdict(ingressTrace([probe({ layer: 'tcp', ok: false, error: 'connection refused' })]), true)
    expect(v.icon).toBe('✗')
    expect(v.text).toContain('Front door unreachable')
  })
  it('host returned an HTTP 3xx/4xx (TCP ok + http reached) → neutral "HTTP responded", never the dishonest "transport only / HTTP wasn’t checked"', () => {
    // Common ingress shape: a preceding TCP-ok probe AND an HTTP 301/404. The front
    // door DID return an HTTP response, so the copy must not claim transport-only.
    const v = reachVerdict(ingressTrace([
      probe({ layer: 'tcp', ok: true }),
      probe({ layer: 'http', ok: true, tone: 'reached', detail: 'HTTP 301' }),
    ]), true)
    expect(v.icon).toBe('')
    expect(v.tone).toBe('neutral')
    expect(v.text).toContain('Front door reached - HTTP responded')
    expect(v.text).not.toContain('transport only')
    expect(v.caveat).toContain('HTTP 301')
    expect(v.caveat).not.toContain('HTTP response wasn’t checked')
  })
  it('host transport-only (TCP ok, NO http probe) → still "transport only - HTTP wasn’t checked"', () => {
    const v = reachVerdict(ingressTrace([probe({ layer: 'tcp', ok: true })]), true)
    expect(v.icon).toBe('')
    expect(v.tone).toBe('neutral')
    expect(v.text).toContain('transport only')
    expect(v.caveat).toContain('HTTP response wasn’t checked')
  })
})

// The honesty rule the redesign exists to enforce: confidence is a LEVEL, not an
// alarm. A reached/200 - even via the proxy - reads ✓; ⚠ is reserved for real
// problems (server-error, partial, unreachable). The Tree + Diagram both use this.
describe('reachVerdict - never a ⚠ on a success', () => {
  it('reached via proxy (indirect) is NEUTRAL with a caveat - not a green ✓ (overclaims), never a ⚠ (it did reach)', () => {
    const v = reachVerdict(trace({ verdict: 'unknown', routes: [route({ outcome: 'reached', confidence: 'indirect' })] }))
    expect(v.icon).toBe('')
    expect(v.tone).toBe('neutral')
    expect(v.icon).not.toBe('⚠')
    expect(v.caveat).toBeTruthy()
  })
  it('verified real traffic reads ✓', () => {
    const v = reachVerdict(trace({ verdict: 'healthy', routes: [route({ outcome: 'verified', confidence: 'real' })] }))
    expect(v.icon).toBe('✓')
  })
  it('REACHED-not-verified real route is NEUTRAL, not a green ✓ - "real" confidence means the live path was exercised, not that the route was verified end-to-end (mirrors coverageBannerTone realPass)', () => {
    const v = reachVerdict(trace({ verdict: 'healthy', routes: [route({ outcome: 'reached', confidence: 'real' })] }))
    expect(v.icon).not.toBe('✓')
    expect(v.tone).toBe('neutral')
    expect(v.caveat).toBeTruthy()
  })
  it('a 5xx server-error IS a ⚠ (a real problem)', () => {
    const v = reachVerdict(trace({ verdict: 'degraded', routes: [route({ outcome: 'server-error' })] }))
    expect(v.icon).toBe('⚠')
  })
  it('partial (some unreachable) is ⚠', () => {
    const v = reachVerdict(trace({
      verdict: 'degraded',
      routes: [route({ outcome: 'verified', confidence: 'real' }), route({ route: '/api', outcome: 'unreachable' })],
    }))
    expect(v.icon).toBe('⚠')
  })
  it('fully broken is ✗', () => {
    const v = reachVerdict(trace({ verdict: 'broken', routes: [route({ outcome: 'unreachable' })] }))
    expect(v.icon).toBe('✗')
  })
  it('un-probed clean config reads "Config valid - not yet tested" (neutral, no green ✓ until a real probe)', () => {
    const v = reachVerdict(trace({ verdict: 'healthy', routes: [] }))
    expect(v.icon).toBe('')
    expect(v.tone).toBe('neutral')
    expect(v.text).toContain('Config valid')
    expect(v.text).toContain('not yet tested')
    expect(v.caveat).toContain('run the test')
  })
  it('un-probed DEGRADED config (e.g. targetPort mismatch) is a ⚠ with the cause, never "valid"', () => {
    const v = reachVerdict(trace({ verdict: 'degraded', routes: [], diagnosis: { summary: 'Service targetPort likely wrong' } as Trace['diagnosis'] }))
    expect(v.icon).toBe('⚠')
    expect(v.text).toContain('targetPort')
    expect(v.text).not.toContain('looks valid')
  })
})

describe('reachVerdict - benign / indirect / front-door honesty', () => {
  it('benign-only (scaled-to-0) probed trace reads amber dormant, NOT "Config looks healthy"', () => {
    const v = reachVerdict(trace({ verdict: 'degraded', routes: [route({ outcome: 'unreachable', benign: true })] }))
    expect(v.tone).toBe('degraded')
    expect(v.text.toLowerCase()).toContain('scaled to 0')
    expect(v.text).not.toContain('Config looks healthy')
  })

  it('indirect-only unreachable does NOT hard-condemn (✗) - falls to neutral/unknown keeping the honest headline', () => {
    const v = reachVerdict(trace({
      verdict: 'degraded',
      headline: 'Unreachable via API server - real path not confirmed',
      routes: [route({ outcome: 'unreachable', confidence: 'indirect' })],
    }))
    expect(v.icon).not.toBe('✗')
    expect(v.tone).not.toBe('unhealthy')
  })

  it('a REAL (non-indirect) unreachable still hard-condemns (✗)', () => {
    const v = reachVerdict(trace({ verdict: 'broken', routes: [route({ outcome: 'unreachable', confidence: 'real' })] }))
    expect(v.icon).toBe('✗')
  })

  it('front door 2xx but a backend route actually FAILED → ⚠/degraded, not a green ✓', () => {
    const v = reachVerdict(ingressTrace(
      [probe({ layer: 'http', ok: true, tone: 'healthy', detail: 'HTTP 200' })],
      [route({ outcome: 'unreachable', confidence: 'real' })],
    ), true)
    expect(v.icon).toBe('⚠')
    expect(v.tone).toBe('degraded')
  })

  it('front door 2xx with backend untested keeps the green ✓', () => {
    const v = reachVerdict(ingressTrace(
      [probe({ layer: 'http', ok: true, tone: 'healthy', detail: 'HTTP 200' })],
      [],
    ), true)
    expect(v.icon).toBe('✓')
  })

  it('un-probed ExternalName surfaces the alias + invites the test (no condemnation)', () => {
    const t = trace({
      subject: { kind: 'Service', namespace: 'p', name: 'ext' },
      downstream: [{ resource: { kind: 'ExternalName', name: 'db.example.com' }, edge: 'Service->ExternalName', findings: [] } as Hop],
      routes: [],
    })
    const v = reachVerdict(t, false)
    expect(v.text).toContain('db.example.com')
    expect(v.icon).not.toBe('✗')
  })

  it('probed ExternalName reached directly is NEUTRAL (not a green real-traffic ✓)', () => {
    const t = trace({
      subject: { kind: 'Service', namespace: 'p', name: 'ext' },
      downstream: [{ resource: { kind: 'ExternalName', name: 'db.example.com' }, edge: 'Service->ExternalName', findings: [] } as Hop],
      routes: [route({ outcome: 'reached', confidence: 'indirect' })],
    })
    const v = reachVerdict(t, true)
    expect(v.icon).toBe('')
    expect(v.tone).toBe('neutral')
  })
})

describe('reachVerdict - a confirmed host must not declare an untested sibling host confirmed (defect 11)', () => {
  it('2xx on one host + a different declared host whose probe SKIPPED → neutral "one confirmed, another not tested" (no green ✓)', () => {
    const t = ingressTrace([
      probe({ layer: 'http', target: 'https://api.example.com/', ok: true, tone: 'healthy', detail: 'HTTP 200' }),
      probe({ layer: 'dns', target: 'https://admin.internal/', ok: false, skipped: true, reason: "didn't resolve from here" }),
    ], [route({ outcome: 'verified', confidence: 'real' })])
    const v = reachVerdict(t, true)
    expect(v.icon).not.toBe('✓')
    expect(v.tone).toBe('neutral')
    expect(v.text).toContain('not tested')
  })

  it('2xx and the only skipped probe is a LOWER LAYER of the SAME host → still a clean green ✓', () => {
    const t = ingressTrace([
      probe({ layer: 'http', target: 'https://api.example.com/', ok: true, tone: 'healthy', detail: 'HTTP 200' }),
      probe({ layer: 'tls', target: 'https://api.example.com/', ok: true, skipped: true, reason: 'skipped' }),
    ], [route({ outcome: 'verified', confidence: 'real' })])
    const v = reachVerdict(t, true)
    expect(v.icon).toBe('✓')
    expect(v.text).toContain('confirmed')
  })
})

describe('reachVerdict - cycle 5 honesty fixes', () => {
  // Defect 6: a mixed 200 + 5xx front door must not read green "confirmed".
  it('one host 200 + sibling host 5xx → ⚠ partial, never "confirmed from outside"', () => {
    const t = ingressTrace(
      [
        probe({ target: 'https://a.example.com/', ok: true, tone: 'healthy' }),
        probe({ target: 'https://b.example.com/', ok: true, tone: 'degraded', detail: 'HTTP 503 · reached, server error' }),
      ],
      [route({ outcome: 'verified', confidence: 'real' })],
    )
    const v = reachVerdict(t, true)
    expect(v.icon).toBe('⚠')
    expect(v.text).not.toContain('confirmed from outside')
  })

  // Defect 7: a proxy-only (indirect) unreachable must not be counted as a confirmed
  // backend failure in the partial branch - it false-condemns an untested path.
  it('one verified/real + one unreachable/indirect → never "1 unreachable" (the proxy-failed route was never tested)', () => {
    const t = trace({
      subject: { kind: 'Service', namespace: 'p', name: 's' },
      routes: [
        route({ route: ':80', outcome: 'verified', confidence: 'real' }),
        route({ route: ':9090', outcome: 'unreachable', confidence: 'indirect' }),
      ],
    })
    const v = reachVerdict(t, true)
    expect(v.text).not.toContain('unreachable')
    expect(v.tone).not.toBe('degraded')
  })
})

describe('reachVerdict - a 2xx host must not declare a sibling 3xx/4xx host confirmed (cycle 11)', () => {
  // The single-host path treats a 3xx/4xx as http-reached → neutral "route not
  // verified". The multi-host 2xx branch must apply the SAME rule to a sibling host:
  // a sibling that returned 3xx/4xx (tone 'reached') is reached, not confirmed - so
  // the banner must NOT claim a blanket "Front door confirmed from outside".
  it('2xx on one host + a sibling host that returned 3xx/4xx → neutral, names the unverified sibling (no green ✓)', () => {
    const t = ingressTrace([
      probe({ layer: 'http', target: 'https://api.example.com/', ok: true, tone: 'healthy', detail: 'HTTP 200' }),
      probe({ layer: 'http', target: 'https://www.example.com/', ok: true, tone: 'reached', detail: 'HTTP 301' }),
    ], [route({ outcome: 'verified', confidence: 'real' })])
    const v = reachVerdict(t, true)
    expect(v.icon).not.toBe('✓')
    expect(v.tone).toBe('neutral')
    expect(v.text).not.toContain('confirmed from outside')
    expect(v.text).toContain('reached but not verified')
  })
})

describe('reachVerdict - a broken-from-upstream verdict headlines the reason, not optimistic probe text', () => {
  it('leads with the verdict reason when there is no static finding (entry paths unreachable)', () => {
    // Healthy Service, but every entry path failed: verdict=broken with a reason
    // and NO finding. The banner must lead with the reason and a red icon, never
    // keep the optimistic "Reachable - verified" headline beside the red mark.
    const v = reachVerdict(
      trace({
        verdict: 'broken',
        reason: 'all entry paths into this resource are unreachable; the resource itself looks healthy',
        headline: 'Reachable - verified',
        routes: [route({ outcome: 'verified', confidence: 'real' })],
      }),
      true,
    )
    expect(v.icon).toBe('✗')
    expect(v.text).toContain('all entry paths')
    expect(v.text).not.toBe('Reachable - verified')
  })
})
