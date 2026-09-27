import { ReachabilityView, type Trace } from '@skyhook-io/k8s-ui'

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

const mixed = {
  ...base,
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
} as unknown as Trace

const untested = {
  ...base,
  verdict: 'unknown',
  unknownClass: 'by-design',
  headline: 'Untested — configuration predicts this path but nothing has been observed',
  coverage: { tested: 0, passed: 0, failed: 0, skipped: 3 },
  downstream: [serviceHop(), podsHop(PODS, [])],
  routes: [
    { route: 'GET / · :80 → 8080', target: ':80 → 8080', outcome: 'not-tested' },
    { route: 'gRPC :9090 → 9090', target: ':9090 → 9090', outcome: 'not-tested' },
  ],
} as unknown as Trace

const wrap = { width: 1100, height: 640, display: 'flex', flexDirection: 'column' } as const
const noop = () => {}
const testedAt = new Date(Date.now() - 90_000)

export function Verified() {
  return (
    <div style={wrap}>
      <ReachabilityView
        trace={mixed}
        probed
        testedAt={testedAt}
        probePath="/healthz"
        onRunProbes={noop}
        onRunInCluster={noop}
        inClusterAllowed
        onRefresh={noop}
        onNavigateToResource={noop}
      />
    </div>
  )
}

export function ConfirmedBreak() {
  return (
    <div style={wrap}>
      <ReachabilityView
        trace={broken}
        probed
        testedAt={testedAt}
        onRunProbes={noop}
        onRunInCluster={noop}
        inClusterAllowed
        onNavigateToResource={noop}
      />
    </div>
  )
}

export function NotTestedYet() {
  return (
    <div style={wrap}>
      <ReachabilityView
        trace={untested}
        onRunProbes={noop}
        onRunInCluster={noop}
        inClusterAllowed={false}
        inClusterDeniedReason="you don't have create jobs in payments"
        onNavigateToResource={noop}
      />
    </div>
  )
}
