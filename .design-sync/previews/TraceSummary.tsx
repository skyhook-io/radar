import { TraceSummary, type Trace } from '@skyhook-io/k8s-ui'

const NS = 'payments'
const SUBJECT = { kind: 'Service', name: 'checkout-api', namespace: NS }

const probe = (o: Record<string, unknown>) => ({
  layer: 'http',
  target: 'checkout-api:80',
  vantage: 'in-cluster',
  path: 'data',
  ok: true,
  tone: 'healthy',
  ...o,
})

const pod = (name: string, ready: boolean, ip: string, reason?: string) => ({ name, ready, ip, reason })

const PODS = [
  pod('checkout-api-7d4c-d4wq', true, '10.244.1.17'),
  pod('checkout-api-7d4c-m8zt', true, '10.244.2.31'),
  pod('checkout-api-7d4c-k9x2', false, '10.244.3.9', 'readiness probe failing for 12m'),
]

const serviceHop = () => ({
  resource: SUBJECT,
  edge: 'service',
  findings: [],
  config: { clusterIP: '10.96.14.2', serviceType: 'ClusterIP', selector: { app: 'checkout-api' }, ports: [{ port: 80, targetPort: '8080' }] },
  probes: [],
})

const podsHop = (pods: ReturnType<typeof pod>[], probes: unknown[], podTotal?: number) => ({
  resource: { kind: 'Pods', name: '', namespace: NS },
  edge: 'service->pods',
  findings: [],
  meta: { ready: podTotal ? 236 : pods.filter((p) => p.ready).length, selected: podTotal ?? pods.length },
  config: { pods, podTotal: podTotal ?? pods.length },
  probes,
})

const base = {
  subject: SUBJECT,
  verdict: 'healthy',
  brokenAt: -1,
  upstreams: [],
  downstream: [],
  coverage: { tested: 2, passed: 2, failed: 0, skipped: 1 },
  routes: [],
  notTested: [],
}

const okProbes = [
  probe({ target: '10.244.1.17:8080', detail: 'HTTP 200', latencyNs: 11_000_000 }),
  probe({ target: '10.244.2.31:8080', detail: 'HTTP 200', latencyNs: 9_000_000 }),
]

const untested = {
  ...base,
  verdict: 'unknown',
  unknownClass: 'by-design',
  headline: 'Untested — configuration predicts this path but nothing has been observed',
  coverage: { tested: 0, passed: 0, failed: 0, skipped: 3 },
  downstream: [serviceHop(), podsHop(PODS, [])],
} as unknown as Trace

const ingressUntested = {
  ...untested,
  subject: { kind: 'Ingress', name: 'storefront', namespace: NS },
} as unknown as Trace

const verified = {
  ...base,
  headline: 'Reachable — the front door itself was exercised from outside the cluster',
  coverage: { tested: 3, passed: 3, failed: 0, skipped: 0 },
  downstream: [serviceHop(), podsHop(PODS.map((p) => ({ ...p, ready: true, reason: undefined })), okProbes)],
  routes: [
    { route: 'front door · primary-gateway', target: ':443 → 80', outcome: 'verified', confidence: 'real', evidence: 'HTTP 200 · 41 ms from outside the cluster' },
    { route: 'GET / · :80 → 8080', target: ':80 → 8080', outcome: 'verified', confidence: 'real', evidence: 'HTTP 200 · 11 ms' },
  ],
} as unknown as Trace

const broken = {
  ...base,
  verdict: 'broken',
  brokenAt: 1,
  headline: 'Unreachable — every eligible endpoint refused the connection on 8080',
  coverage: { tested: 2, passed: 0, failed: 2, skipped: 1 },
  downstream: [
    serviceHop(),
    podsHop(PODS, [
      probe({ layer: 'tcp', target: '10.244.1.17:8080', ok: false, tone: 'unhealthy', error: 'ECONNREFUSED', detail: 'connection refused' }),
      probe({ layer: 'tcp', target: '10.244.2.31:8080', ok: false, tone: 'unhealthy', error: 'ECONNREFUSED', detail: 'connection refused' }),
    ]),
  ],
  routes: [
    { route: 'GET / · :80 → 8080', target: ':80 → 8080', outcome: 'unreachable', failedLayer: 'tcp', confidence: 'real', evidence: 'ECONNREFUSED at both endpoints' },
    { route: 'gRPC :9090 → 9090', target: ':9090 → 9090', outcome: 'reached', confidence: 'indirect', evidence: 'stream reset via proxy' },
  ],
} as unknown as Trace

const indirectOnly = {
  ...base,
  verdict: 'degraded',
  headline: 'Only control-plane evidence — Radar is not permitted to probe the dataplane',
  coverage: { tested: 1, passed: 1, failed: 0, skipped: 2 },
  downstream: [
    serviceHop(),
    podsHop(PODS, [probe({ vantage: 'local', path: 'apiserver', target: 'checkout-api port 80', detail: 'HTTP 200 via proxy', latencyNs: 12_000_000 })]),
  ],
  routes: [{ route: 'GET / · :80 → 8080', target: ':80 → 8080', outcome: 'verified', confidence: 'indirect', evidence: 'HTTP 200 through the apiserver proxy' }],
  notTested: [{ route: 'gRPC :9090 → 9090', reason: 'probe creation denied by RBAC', reasonClass: 'vantage' }],
} as unknown as Trace

const wrap = { width: 460 }
const noop = () => {}

export function NotTestedService() {
  return <div style={wrap}><TraceSummary trace={untested} onOpenReachability={noop} /></div>
}

export function NotTestedIngress() {
  return <div style={wrap}><TraceSummary trace={ingressUntested} onOpenReachability={noop} /></div>
}

export function Verified() {
  return <div style={wrap}><TraceSummary trace={verified} onOpenReachability={noop} /></div>
}

export function Failing() {
  return <div style={wrap}><TraceSummary trace={broken} onOpenReachability={noop} /></div>
}

export function IndirectOnly() {
  return <div style={wrap}><TraceSummary trace={indirectOnly} onOpenReachability={noop} /></div>
}
