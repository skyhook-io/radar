import { TrafficGraph } from '@skyhook-io/k8s-ui'

const frame = { width: 1200, height: 700 } as const
const lastSeen = new Date(Date.now() - 12_000).toISOString()

function ep(name: string, namespace: string, kind = 'Service') {
  return { name, namespace, kind, workload: name }
}

function flow(
  source: ReturnType<typeof ep>,
  destination: ReturnType<typeof ep>,
  port: number,
  connections: number,
  l7?: { protocol: string; requests: number; errors: number; p95: number; status?: Record<string, number> },
) {
  return {
    source,
    destination,
    protocol: 'tcp',
    port,
    flowCount: Math.round(connections / 3),
    bytesSent: connections * 2400,
    bytesRecv: connections * 9100,
    connections,
    lastSeen,
    ...(l7
      ? {
          l7Protocol: l7.protocol,
          requestCount: l7.requests,
          errorCount: l7.errors,
          avgLatencyMs: l7.p95 / 3,
          latencyP50Ms: l7.p95 / 4,
          latencyP95Ms: l7.p95,
          latencyP99Ms: l7.p95 * 2.2,
          httpStatusCounts: l7.status,
          verdictCounts: { forwarded: connections },
        }
      : { verdictCounts: { forwarded: connections } }),
  }
}

const internet = { name: 'internet', namespace: '', kind: 'External', workload: '' }
const ingress = ep('ingress-nginx-controller', 'ingress-nginx')
const frontend = ep('frontend', 'shop')
const checkout = ep('checkout-api', 'shop')
const catalog = ep('catalog-api', 'shop')
const payments = ep('payments-db', 'payments')
const redis = ep('redis', 'shop')
const stripe = { name: 'api.stripe.com', namespace: '', kind: 'External', workload: '' }

const shopFlows = [
  flow(internet, ingress, 443, 1_840_000),
  flow(ingress, frontend, 8080, 1_620_000, { protocol: 'HTTP', requests: 412_000, errors: 380, p95: 42, status: { '2xx': 398_000, '3xx': 11_200, '4xx': 2_420, '5xx': 380 } }),
  flow(frontend, checkout, 8080, 284_000, { protocol: 'HTTP', requests: 96_400, errors: 2_140, p95: 310, status: { '2xx': 92_100, '4xx': 2_160, '5xx': 2_140 } }),
  flow(frontend, catalog, 9090, 512_000, { protocol: 'gRPC', requests: 188_000, errors: 12, p95: 18 }),
  flow(checkout, payments, 5432, 41_000),
  flow(checkout, redis, 6379, 356_000),
  flow(catalog, redis, 6379, 128_000),
  flow(checkout, stripe, 443, 8_900),
]

export function CheckoutServiceMap() {
  return (
    <div style={frame}>
      <TrafficGraph flows={shopFlows as any} hotPathThreshold={500_000} trafficSource="hubble" />
    </div>
  )
}

export function NamespaceGroups() {
  return (
    <div style={frame}>
      <TrafficGraph
        flows={shopFlows as any}
        hotPathThreshold={500_000}
        showNamespaceGroups
        serviceCategories={new Map([['api.stripe.com', 'cloud']])}
        trafficSource="hubble"
      />
    </div>
  )
}

export function IstioRequestRates() {
  const rateFlows = shopFlows.map((f) => ({ ...f, connections: Math.max(1, Math.round(f.connections / 3600)) }))
  return (
    <div style={frame}>
      <TrafficGraph flows={rateFlows as any} hotPathThreshold={140} trafficSource="istio" />
    </div>
  )
}
