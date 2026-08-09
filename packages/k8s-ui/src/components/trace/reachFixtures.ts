import type { Trace, PodStatus, ProbeResult } from './types'

/**
 * Fixtures for the dev-only state switcher. These exist so the empty, failing,
 * sampled and permission-denied states are reachable without engineering a real
 * cluster into each shape - they are NOT demo data and never render outside a
 * dev build.
 *
 * The prototype this view was built from carried a tenth state, "conflicting
 * vantages", where a synthetic probe succeeds and the real caller is refused.
 * It is deliberately absent: RouteResult holds ONE outcome and ONE confidence,
 * so two disagreeing origins cannot both be retained. Faking it here would
 * imply a capability the model does not have.
 */
export type DevState = 'mixed' | 'break' | 'front' | 'synth' | 'scale' | 'fanout' | 'none' | 'running' | 'stale' | 'rbac'

export const DEV_STATES: { id: DevState; label: string }[] = [
  { id: 'mixed', label: '1 · Mixed vantages' },
  { id: 'break', label: '2 · Confirmed break' },
  { id: 'front', label: '3 · Front door verified' },
  { id: 'synth', label: '4 · Synthetic only' },
  { id: 'scale', label: '5 · Aggregated scale' },
  { id: 'fanout', label: '6 · Gateway fan-out' },
  { id: 'none', label: 'No tests yet' },
  { id: 'running', label: 'Probe running' },
  { id: 'stale', label: 'Stale + changed' },
  { id: 'rbac', label: 'Probe not permitted' },
]

const NS = 'payments'
const SUBJECT = { kind: 'Service', name: 'checkout-api', namespace: NS }

const probe = (o: Partial<ProbeResult>): ProbeResult => ({
  layer: 'http',
  target: 'checkout-api:80',
  vantage: 'in-cluster',
  path: 'data',
  ok: true,
  tone: 'healthy',
  ...o,
})

const pod = (name: string, ready: boolean, ip: string, reason?: string): PodStatus => ({ name, ready, ip, reason })

const PODS = [
  pod('checkout-api-7d4c-d4wq', true, '10.244.1.17'),
  pod('checkout-api-7d4c-m8zt', true, '10.244.2.31'),
  pod('checkout-api-7d4c-k9x2', false, '10.244.3.9', 'readiness probe failing for 12m'),
]

function base(): Trace {
  return {
    subject: SUBJECT,
    verdict: 'healthy',
    brokenAt: -1,
    upstreams: [],
    downstream: [],
    coverage: { tested: 2, passed: 2, failed: 0, skipped: 1 },
    routes: [],
    notTested: [],
  }
}

function serviceHop(clusterIP = '10.96.14.2') {
  return {
    resource: SUBJECT,
    edge: 'service',
    findings: [],
    config: { clusterIP, serviceType: 'ClusterIP', selector: { app: 'checkout-api' }, ports: [{ port: 80, targetPort: '8080' }] },
    probes: [],
  }
}

function podsHop(pods: PodStatus[], probes: ProbeResult[], podTotal?: number) {
  const ready = pods.filter((p) => p.ready).length
  return {
    resource: { kind: 'Pods', name: '', namespace: NS },
    edge: 'service->pods',
    findings: [],
    meta: { ready: podTotal ? 236 : ready, selected: podTotal ?? pods.length },
    config: { pods, podTotal: podTotal ?? pods.length },
    probes,
  }
}

export function devTrace(state: DevState): Trace {
  const t = base()
  const okProbes = [
    probe({ target: '10.244.1.17:8080', detail: 'HTTP 200', latencyNs: 11_000_000 }),
    probe({ target: '10.244.2.31:8080', detail: 'HTTP 200', latencyNs: 9_000_000 }),
  ]

  if (state === 'none') {
    return {
      ...t,
      verdict: 'unknown',
      unknownClass: 'by-design',
      headline: 'Untested — configuration predicts this path but nothing has been observed',
      coverage: { tested: 0, passed: 0, failed: 0, skipped: 3 },
      downstream: [serviceHop(), podsHop(PODS, [])],
      routes: [
        { route: 'GET / · :80 → 8080', target: ':80 → 8080', outcome: 'not-tested' },
        { route: 'gRPC :9090 → 9090', target: ':9090 → 9090', outcome: 'not-tested' },
      ],
    }
  }

  if (state === 'break') {
    const failed = [
      probe({ layer: 'tcp', target: '10.244.1.17:8080', ok: false, tone: 'unhealthy', error: 'ECONNREFUSED', detail: 'connection refused' }),
      probe({ layer: 'tcp', target: '10.244.2.31:8080', ok: false, tone: 'unhealthy', error: 'ECONNREFUSED', detail: 'connection refused' }),
    ]
    return {
      ...t,
      verdict: 'broken',
      brokenAt: 1,
      headline: 'Unreachable — every eligible endpoint refused the connection on 8080',
      coverage: { tested: 2, passed: 0, failed: 2, skipped: 1 },
      downstream: [serviceHop(), podsHop(PODS, failed)],
      diagnosis: {
        causeCode: 'svc:port-mismatch',
        summary: 'The Service targetPort (8080) does not match the container port (8081).',
        nextAction: 'Edit the Service targetPort to 8081.',
        culpritResource: SUBJECT,
      },
      routes: [
        { route: 'GET / · :80 → 8080', target: ':80 → 8080', outcome: 'unreachable', failedLayer: 'tcp', confidence: 'real', evidence: 'ECONNREFUSED at both endpoints' },
        { route: 'gRPC :9090 → 9090', target: ':9090 → 9090', outcome: 'reached', confidence: 'indirect', evidence: 'stream reset via proxy' },
      ],
    }
  }

  if (state === 'scale') {
    const many: PodStatus[] = Array.from({ length: 8 }, (_, i) => pod(`checkout-api-7d4c-s${i}`, true, `10.244.4.${i + 10}`))
    many.push(pod('checkout-api-7d4c-q7r2', true, '10.244.4.99'))
    const probes = [
      ...many.slice(0, 7).map((p) => probe({ target: `${p.ip}:8080`, detail: 'HTTP 200', latencyNs: 12_000_000 })),
      probe({ target: '10.244.4.99:8080', ok: false, tone: 'unhealthy', layer: 'tcp', error: 'ECONNREFUSED', detail: 'connection refused' }),
      probe({ target: '10.244.4.10:8080', detail: 'HTTP 200', latencyNs: 1_900_000_000 }),
    ]
    return {
      ...t,
      verdict: 'degraded',
      headline: 'Mostly reachable — one eligible endpoint refuses connections and most were not probed',
      coverage: { tested: 8, passed: 7, failed: 1, skipped: 228 },
      downstream: [serviceHop(), podsHop(many, probes, 240)],
      routes: [{ route: 'GET / · :80 → 8080', target: ':80 → 8080', outcome: 'reached', confidence: 'real', evidence: '7 of 8 sampled endpoints returned HTTP 200' }],
    }
  }

  if (state === 'front') {
    const allReady = PODS.map((p) => ({ ...p, ready: true, reason: undefined }))
    return {
      ...t,
      headline: 'Reachable — the front door itself was exercised from outside the cluster',
      coverage: { tested: 3, passed: 3, failed: 0, skipped: 0 },
      upstreams: [
        {
          resource: { kind: 'Gateway', name: 'primary-gateway', namespace: NS },
          edge: 'gateway->service',
          findings: [],
          config: { addresses: ['203.0.113.7'], listeners: [{ name: 'https', port: 443, protocol: 'HTTPS' }] },
          probes: [probe({ layer: 'tls', vantage: 'local', target: '203.0.113.7:443', detail: 'TLS handshake OK' })],
        },
      ],
      downstream: [serviceHop(), podsHop(allReady, okProbes)],
      routes: [
        { route: 'front door · primary-gateway', target: ':443 → 80', outcome: 'verified', confidence: 'real', evidence: 'HTTP 200 · 41 ms from outside the cluster' },
        { route: 'GET / · :80 → 8080', target: ':80 → 8080', outcome: 'verified', confidence: 'real', evidence: 'HTTP 200 · 11 ms' },
      ],
    }
  }


  // A Gateway with several attached routes - the ONE shape where the topology
  // itself is the information rather than a formality. It is also the shape the
  // fixtures never covered, which is how a listener-hostname bug survived: the
  // front-door fixture declares a listener with no hostname, so the code path
  // that reads listener hostnames was never exercised here.
  if (state === 'fanout') {
    const GW = { kind: 'Gateway', name: 'primary-gateway', namespace: 'edge' }
    const routeHop = (name: string, host: string, findings: Trace['downstream'][number]['findings'] = []) => ({
      resource: { kind: 'HTTPRoute', name, namespace: NS },
      edge: 'Gateway->HTTPRoute',
      findings,
      config: { hostnames: [host] },
      probes: [],
    })
    return {
      ...t,
      subject: GW,
      verdict: 'degraded',
      headline: 'One of seven routes has no backend - the rest are configured and untested',
      upstreams: [],
      downstream: [
        routeHop('shop', 'shop.example.com'),
        routeHop('checkout', 'checkout.example.com', [
          { code: 'route:no-backend', severity: 'critical', message: "Service 'checkout-api' in namespace 'payments' doesn't exist, so this route's backend receives no traffic.", cause: 'Backend Service missing', action: 'Create the Service or fix the backendRef' },
        ]),
        routeHop('account', 'account.example.com'),
        routeHop('admin', 'admin.example.com', [
          { code: 'route:not-accepted', severity: 'warning', message: 'Not Accepted by the parent Gateway - hostname not covered by any listener.', cause: 'Hostname not served by a listener' },
        ]),
        routeHop('docs', 'docs.example.com'),
        routeHop('status', 'status.example.com'),
        routeHop('api', 'api.example.com'),
      ],
      coverage: { tested: 0, passed: 0, failed: 0, skipped: 7 },
      routes: [
        { route: 'checkout.example.com/', target: 'checkout-api:80', outcome: 'unreachable', confidence: 'real', evidence: 'no backend to reach', failedLayer: 'tcp' },
        { route: 'shop.example.com/', target: 'shop:80', outcome: 'verified', confidence: 'real', evidence: 'HTTP 200 · 14 ms' },
        { route: 'admin.example.com/', target: 'admin:80', outcome: 'not-tested', evidence: 'hostname not covered by any listener' },
      ],
      notTested: [
        { route: 'account.example.com/', reason: 'route not actively tested', reasonClass: 'coverage' },
        { route: 'docs.example.com/', reason: 'route not actively tested', reasonClass: 'coverage' },
      ],
    }
  }

  if (state === 'rbac') {
    return {
      ...t,
      verdict: 'degraded',
      headline: 'Only control-plane evidence — Radar is not permitted to probe the dataplane',
      coverage: { tested: 1, passed: 1, failed: 0, skipped: 2 },
      downstream: [
        serviceHop(),
        podsHop(PODS, [probe({ vantage: 'local', path: 'apiserver', target: 'checkout-api port 80', detail: 'HTTP 200 via proxy', latencyNs: 12_000_000 })]),
      ],
      routes: [{ route: 'GET / · :80 → 8080', target: ':80 → 8080', outcome: 'verified', confidence: 'indirect', evidence: 'HTTP 200 through the apiserver proxy' }],
      notTested: [{ route: 'gRPC :9090 → 9090', reason: 'probe creation denied by RBAC', reasonClass: 'vantage' }],
    }
  }

  if (state === 'synth') {
    return {
      ...t,
      verdict: 'degraded',
      headline: 'Reachable from a synthetic probe — the real caller is still unproven',
      downstream: [serviceHop(), podsHop(PODS, okProbes)],
      routes: [{ route: 'GET / · :80 → 8080', target: ':80 → 8080', outcome: 'verified', confidence: 'real', evidence: 'HTTP 200 · 11 ms from the probe Pod' }],
    }
  }

  // mixed / running / stale share the same trace; the last two are driven by
  // props (inClusterRunning, clusterChangedSinceTest) layered over it.
  return {
    ...t,
    headline: 'Reachable in-cluster on :80 — front door and real caller still unproven',
    downstream: [
      serviceHop(),
      podsHop(PODS, [...okProbes, probe({ vantage: 'local', path: 'apiserver', target: 'checkout-api port 80', detail: 'HTTP 200 via proxy', latencyNs: 12_000_000 })]),
    ],
    routes: [
      { route: 'GET / · :80 → 8080', target: ':80 → 8080', outcome: 'verified', confidence: 'real', evidence: 'DNS 2 ms · TCP 1 ms · HTTP 200 · 11 ms' },
      { route: 'gRPC :9090 → 9090', target: ':9090 → 9090', outcome: 'reached', confidence: 'indirect', evidence: 'connected, stream reset' },
    ],
    notTested: [{ route: 'front door · primary-gateway', reason: 'no request has entered through the Gateway', reasonClass: 'coverage' }],
  }
}
